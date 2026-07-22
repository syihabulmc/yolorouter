package claude

import (
	"encoding/json"
	"net/http"
	"reflect"
	"testing"

	"github.com/yolorouter/yolorouter/internal/protocols"
)

// ─── Helpers ────────────────────────────────────────────────────────────────

func intPtr(n int) *int { return &n }

// makeBody marshals a map into JSON bytes, failing the test on error.
func makeBody(t *testing.T, v map[string]interface{}) []byte {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("makeBody: %v", err)
	}
	return b
}

// parseBody unmarshals JSON bytes into a map, failing the test on error.
func parseBody(t *testing.T, b []byte) map[string]interface{} {
	t.Helper()
	var m map[string]interface{}
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatalf("parseBody: %v", err)
	}
	return m
}

// thinkingType reads the thinking.type string from the body.
func thinkingType(t *testing.T, m map[string]interface{}) string {
	t.Helper()
	th, ok := m["thinking"].(map[string]interface{})
	if !ok {
		return ""
	}
	s, _ := th["type"].(string)
	return s
}

// thinkingBudget reads thinking.budget_tokens (int) from the body.
func thinkingBudget(t *testing.T, m map[string]interface{}) int {
	t.Helper()
	th, ok := m["thinking"].(map[string]interface{})
	if !ok {
		return 0
	}
	b, _ := th["budget_tokens"].(float64)
	return int(b)
}

func hasCacheControl(block map[string]interface{}) bool {
	_, ok := block["cache_control"]
	return ok
}

// irReqThinking builds a minimal IRRequest with Reasoning.Enabled=true.
func irReqThinking(model string, budget *int, maxTokens *int, temp *float64, topP *float64) *protocols.IRRequest {
	ir := &protocols.IRRequest{
		Model: model,
		Reasoning: protocols.IRReasoningConfig{
			Enabled:      true,
			BudgetTokens: budget,
		},
		Generation: protocols.IRGenerationConfig{
			MaxTokens:   maxTokens,
			Temperature: temp,
			TopP:        topP,
		},
	}
	return ir
}

// bodyWithThinking builds a body as already encoded by the encoder
// (with thinking={type:enabled,budget_tokens:N} and temperature=1).
func bodyWithThinking(t *testing.T, budget int, maxTokens int) []byte {
	t.Helper()
	return makeBody(t, map[string]interface{}{
		"model":      "placeholder",
		"max_tokens": float64(maxTokens),
		"thinking": map[string]interface{}{
			"type":          "enabled",
			"budget_tokens": float64(budget),
		},
		"temperature": 1.0,
	})
}

// ─── isAdaptiveModel ────────────────────────────────────────────────────────

func TestIsAdaptiveModel(t *testing.T) {
	cases := []struct {
		model string
		want  bool
	}{
		{"claude-opus-4-8-20250514", true},
		{"claude-opus-4-7-20250514", true},
		{"claude-opus-4-6-20250514", true},
		{"claude-sonnet-4-6-20250514", true},
		// Bedrock ARN format with dots
		{"anthropic.claude-opus-4-6-20250514-v1:0", true},
		{"anthropic.claude-sonnet-4-6-20250514-v1:0", true},
		// New adaptive-only models (no manual thinking support, adaptive only)
		{"claude-fable-5-20260301", true},
		{"claude-mythos-5-20260301", true},
		{"claude-mythos-preview-20260301", true},
		// Legacy models
		{"claude-sonnet-4-5-20251115", false},
		{"claude-haiku-3-5-20251022", false},
		{"claude-opus-3-20240229", false},
		// Case-insensitive
		{"CLAUDE-OPUS-4-6-20250514", true},
		// Version-boundary negatives: sonnet-4-60 is not sonnet-4-6
		{"claude-sonnet-4-60-20260101", false},
		{"claude-opus-4-60-20260101", false},
	}
	for _, tc := range cases {
		t.Run(tc.model, func(t *testing.T) {
			if got := isAdaptiveModel(tc.model); got != tc.want {
				t.Errorf("isAdaptiveModel(%q) = %v, want %v", tc.model, got, tc.want)
			}
		})
	}
}

// ─── Adaptive Thinking ──────────────────────────────────────────────────────

// New models: thinking is upgraded to adaptive and output_config is written.
func TestOptimizeBody_AdaptiveModel_NewThinking(t *testing.T) {
	models := []string{
		"claude-opus-4-8-20250514",
		"claude-opus-4-6-20250514",
		"claude-sonnet-4-6-20250514",
	}
	for _, model := range models {
		t.Run(model, func(t *testing.T) {
			body := bodyWithThinking(t, 4096, 8192)
			ir := irReqThinking(model, intPtr(4096), intPtr(8192), nil, nil)

			out, injected := OptimizeBody(body, ir, true, "")
			if !injected {
				t.Fatal("expected thinkingInjected=true")
			}
			m := parseBody(t, out)
			if thinkingType(t, m) != "adaptive" {
				t.Errorf("thinking.type = %q, want adaptive", thinkingType(t, m))
			}
			oc, _ := m["output_config"].(map[string]interface{})
			if oc == nil || oc["effort"] != "max" {
				t.Errorf("output_config.effort not max, got %v", oc)
			}
			// budget_tokens should not be present
			if th, ok := m["thinking"].(map[string]interface{}); ok {
				if _, has := th["budget_tokens"]; has {
					t.Error("adaptive thinking should not have budget_tokens")
				}
			}
		})
	}
}

// New models: reasoning_effort maps to output_config.effort.
func TestOptimizeBody_AdaptiveModel_Effort(t *testing.T) {
	cases := []struct{ effort, wantEffort string }{
		{"low", "low"},
		{"medium", "medium"},
		{"high", "high"},
		{"", "max"}, // unset -> defaults to highest quality
	}
	for _, tc := range cases {
		t.Run(tc.effort, func(t *testing.T) {
			body := bodyWithThinking(t, 4096, 8192)
			ir := irReqThinking("claude-sonnet-4-6-20250514", intPtr(4096), intPtr(8192), nil, nil)
			ir.Reasoning.Effort = tc.effort

			out, injected := OptimizeBody(body, ir, true, "")
			if !injected {
				t.Fatal("expected thinkingInjected=true")
			}
			m := parseBody(t, out)
			oc, _ := m["output_config"].(map[string]interface{})
			if oc == nil || oc["effort"] != tc.wantEffort {
				t.Errorf("effort=%q: output_config.effort=%v, want %v", tc.effort, oc["effort"], tc.wantEffort)
			}
		})
	}
}

// New models: the encoder already forced temperature=1 and removed top_p
// (required by Anthropic's thinking mode). The optimizer does not restore these
// fields; confirm temperature=1 is still present on the adaptive success path.
func TestOptimizeBody_AdaptiveModel_KeepsTemperatureOne(t *testing.T) {
	// Body after the encoder forced temperature=1 (simulating encoder behavior)
	body := makeBody(t, map[string]interface{}{
		"model":       "claude-sonnet-4-6-20250514",
		"max_tokens":  8192,
		"temperature": float64(1), // already forced by the encoder
		"thinking":    map[string]interface{}{"type": "enabled", "budget_tokens": 4096},
	})
	ir := irReqThinking("claude-sonnet-4-6-20250514", intPtr(4096), intPtr(8192), nil, nil)

	out, injected := OptimizeBody(body, ir, false, "")
	if !injected {
		t.Fatal("expected thinkingInjected=true")
	}
	m := parseBody(t, out)
	// Adaptive path: thinking becomes adaptive, temperature stays 1
	if thinkingType(t, m) != "adaptive" {
		t.Errorf("thinking type = %v, want adaptive", thinkingType(t, m))
	}
	if temp, _ := m["temperature"].(float64); temp != 1.0 {
		t.Errorf("temperature = %v, want 1.0 (Anthropic thinking requirement)", temp)
	}
}

// Legacy model, budget within normal range: keep {type:enabled}, budget unchanged.
func TestOptimizeBody_LegacyModel_BudgetNormal(t *testing.T) {
	budget := 4096
	maxTokens := 8192
	body := bodyWithThinking(t, budget, maxTokens)
	ir := irReqThinking("claude-sonnet-4-5-20251115", intPtr(budget), intPtr(maxTokens), nil, nil)

	out, injected := OptimizeBody(body, ir, true, "")
	if !injected {
		t.Fatal("expected thinkingInjected=true")
	}
	m := parseBody(t, out)
	if thinkingType(t, m) != "enabled" {
		t.Errorf("thinking.type = %q, want enabled", thinkingType(t, m))
	}
	if got := thinkingBudget(t, m); got != budget {
		t.Errorf("budget_tokens = %d, want %d", got, budget)
	}
}

// Legacy model, reasoning_effort=high -> budget_tokens=80000, max_tokens=4096 -> clamped to 4095.
func TestOptimizeBody_LegacyModel_BudgetClampUpper(t *testing.T) {
	budget := 80000
	maxTokens := 4096
	body := bodyWithThinking(t, budget, maxTokens)
	ir := irReqThinking("claude-sonnet-4-5-20251115", intPtr(budget), intPtr(maxTokens), nil, nil)

	out, injected := OptimizeBody(body, ir, true, "")
	if !injected {
		t.Fatal("expected thinkingInjected=true")
	}
	m := parseBody(t, out)
	if got := thinkingBudget(t, m); got != maxTokens-1 {
		t.Errorf("budget_tokens = %d, want %d", got, maxTokens-1)
	}
}

// Legacy model, reasoning_effort=low -> budget_tokens=1000 -> raised to the lower bound 1024.
func TestOptimizeBody_LegacyModel_BudgetClampLower(t *testing.T) {
	budget := 1000
	maxTokens := 4096
	body := bodyWithThinking(t, budget, maxTokens)
	ir := irReqThinking("claude-sonnet-4-5-20251115", intPtr(budget), intPtr(maxTokens), nil, nil)

	out, injected := OptimizeBody(body, ir, true, "")
	if !injected {
		t.Fatal("expected thinkingInjected=true")
	}
	m := parseBody(t, out)
	if got := thinkingBudget(t, m); got != 1024 {
		t.Errorf("budget_tokens = %d, want 1024", got)
	}
}

// Legacy model, max_tokens=1024 (<=1024) -> skip thinking, body has no thinking field.
func TestOptimizeBody_LegacyModel_MaxTokensTooSmall_SkipsThinking(t *testing.T) {
	body := bodyWithThinking(t, 4096, 1024)
	ir := irReqThinking("claude-sonnet-4-5-20251115", intPtr(4096), intPtr(1024), nil, nil)

	out, injected := OptimizeBody(body, ir, true, "")
	if injected {
		t.Fatal("expected thinkingInjected=false when max_tokens<=1024")
	}
	m := parseBody(t, out)
	if _, has := m["thinking"]; has {
		t.Error("thinking should be removed when max_tokens<=1024")
	}
}

// Legacy model, max_tokens=1024, user set temperature=0.7 and top_p=0.9 -> restore sampling params.
func TestOptimizeBody_LegacyModel_MaxTokensTooSmall_RestoresSamplingParams(t *testing.T) {
	body := bodyWithThinking(t, 4096, 1024) // encoder already set temperature=1 and removed top_p
	temp := 0.7
	topP := 0.9
	ir := irReqThinking("claude-sonnet-4-5-20251115", intPtr(4096), intPtr(1024), &temp, &topP)

	out, injected := OptimizeBody(body, ir, true, "")
	if injected {
		t.Fatal("expected thinkingInjected=false")
	}
	m := parseBody(t, out)
	if got, _ := m["temperature"].(float64); got != 0.7 {
		t.Errorf("temperature = %v, want 0.7", got)
	}
	if _, has := m["top_p"]; !has {
		t.Error("top_p should be restored")
	}
	if got, _ := m["top_p"].(float64); got != 0.9 {
		t.Errorf("top_p = %v, want 0.9", got)
	}
}

// Any model, Reasoning.Enabled=false -> body has no thinking, injected=false.
func TestOptimizeBody_ReasoningDisabled(t *testing.T) {
	body := makeBody(t, map[string]interface{}{
		"model":       "claude-sonnet-4-6-20250514",
		"max_tokens":  float64(4096),
		"temperature": 0.7,
	})
	ir := &protocols.IRRequest{
		Model:     "claude-sonnet-4-6-20250514",
		Reasoning: protocols.IRReasoningConfig{Enabled: false},
	}

	out, injected := OptimizeBody(body, ir, true, "")
	if injected {
		t.Fatal("expected thinkingInjected=false when Reasoning.Enabled=false")
	}
	m := parseBody(t, out)
	if _, has := m["thinking"]; has {
		t.Error("thinking should not be present when Reasoning.Enabled=false")
	}
}

// ─── InjectBetaHeaders ──────────────────────────────────────────────────────

// Adaptive models do not need a beta header.
func TestInjectBetaHeaders_NewModel(t *testing.T) {
	req, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", nil)
	ir := &protocols.IRRequest{Model: "claude-opus-4-6-20250514"}
	InjectBetaHeaders(req, ir, true)
	if got := req.Header.Get("anthropic-beta"); got != "" {
		t.Errorf("adaptive model should not inject beta header, got %q", got)
	}
}

func TestInjectBetaHeaders_LegacyModel(t *testing.T) {
	req, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", nil)
	ir := &protocols.IRRequest{Model: "claude-sonnet-4-5-20251115"}
	InjectBetaHeaders(req, ir, true)
	if got := req.Header.Get("anthropic-beta"); got != "interleaved-thinking-2025-05-14" {
		t.Errorf("anthropic-beta = %q, want interleaved-thinking-2025-05-14", got)
	}
}

// Legacy model already has a beta header -> comma-join, do not overwrite.
func TestInjectBetaHeaders_AppendsToExisting(t *testing.T) {
	req, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", nil)
	req.Header.Set("anthropic-beta", "existing-feature-2025-01-01")
	ir := &protocols.IRRequest{Model: "claude-sonnet-4-5-20251115"} // legacy model
	InjectBetaHeaders(req, ir, true)
	got := req.Header.Get("anthropic-beta")
	want := "existing-feature-2025-01-01,interleaved-thinking-2025-05-14"
	if got != want {
		t.Errorf("anthropic-beta = %q, want %q", got, want)
	}
}

// thinkingInjected=false -> no beta header written.
func TestInjectBetaHeaders_NoInjectionWhenNotInjected(t *testing.T) {
	req, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", nil)
	ir := &protocols.IRRequest{Model: "claude-opus-4-6-20250514"}
	InjectBetaHeaders(req, ir, false)
	if got := req.Header.Get("anthropic-beta"); got != "" {
		t.Errorf("expected no anthropic-beta header, got %q", got)
	}
}

// ─── InjectBetaHeadersFromBody (passthrough path) ────────────────────────────

// passthrough + legacy model + thinking.enabled -> inject interleaved-thinking beta.
func TestInjectBetaHeadersFromBody_LegacyThinking(t *testing.T) {
	req, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", nil)
	body := []byte(`{"model":"claude-sonnet-4-5-20251115","max_tokens":4096,"thinking":{"type":"enabled","budget_tokens":4000}}`)
	InjectBetaHeadersFromBody(req, body, "claude-sonnet-4-5-20251115")
	if got := req.Header.Get("anthropic-beta"); got != "interleaved-thinking-2025-05-14" {
		t.Errorf("anthropic-beta = %q, want interleaved-thinking-2025-05-14", got)
	}
}

// passthrough + adaptive model -> do not inject a beta header.
func TestInjectBetaHeadersFromBody_AdaptiveNoHeader(t *testing.T) {
	req, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", nil)
	body := []byte(`{"model":"claude-opus-4-6-20250514","max_tokens":4096,"thinking":{"type":"adaptive"}}`)
	InjectBetaHeadersFromBody(req, body, "claude-opus-4-6-20250514")
	if got := req.Header.Get("anthropic-beta"); got != "" {
		t.Errorf("adaptive model should not get beta, got %q", got)
	}
}

// passthrough + no thinking field -> do not inject.
func TestInjectBetaHeadersFromBody_NoThinking(t *testing.T) {
	req, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", nil)
	body := []byte(`{"model":"claude-sonnet-4-5-20251115","max_tokens":4096}`)
	InjectBetaHeadersFromBody(req, body, "claude-sonnet-4-5-20251115")
	if got := req.Header.Get("anthropic-beta"); got != "" {
		t.Errorf("no thinking field, should not inject beta, got %q", got)
	}
}

// ─── cache_control injection ─────────────────────────────────────────────────

// No tools/system/assistant, only a user message -> no injectable positions, body unchanged.
func TestInjectCacheControl_NoInjectablePositions(t *testing.T) {
	body := makeBody(t, map[string]interface{}{
		"model":      "claude-sonnet-4-6-20250514",
		"max_tokens": float64(4096),
		"messages": []interface{}{
			map[string]interface{}{
				"role":    "user",
				"content": []interface{}{map[string]interface{}{"type": "text", "text": "hi"}},
			},
		},
	})
	ir := &protocols.IRRequest{Model: "claude-sonnet-4-6-20250514"}
	out, _ := OptimizeBody(body, ir, true, "")
	m := parseBody(t, out)
	msgs := m["messages"].([]interface{})
	userMsg := msgs[0].(map[string]interface{})
	content := userMsg["content"].([]interface{})
	block := content[0].(map[string]interface{})
	if hasCacheControl(block) {
		t.Error("user message should not get cache_control")
	}
}

// tools + system(string) + assistant -> one breakpoint injected at each of the three positions.
func TestInjectCacheControl_InjectsAllThreePositions(t *testing.T) {
	body := makeBody(t, map[string]interface{}{
		"model":      "claude-sonnet-4-6-20250514",
		"max_tokens": float64(4096),
		"system":     "You are helpful.",
		"tools": []interface{}{
			map[string]interface{}{"name": "search", "input_schema": map[string]interface{}{}},
		},
		"messages": []interface{}{
			map[string]interface{}{
				"role":    "user",
				"content": []interface{}{map[string]interface{}{"type": "text", "text": "hi"}},
			},
			map[string]interface{}{
				"role":    "assistant",
				"content": []interface{}{map[string]interface{}{"type": "text", "text": "hello"}},
			},
		},
	})
	ir := &protocols.IRRequest{Model: "claude-sonnet-4-6-20250514"}
	out, _ := OptimizeBody(body, ir, true, "")
	m := parseBody(t, out)

	// tools should have cache_control at the end
	tools := m["tools"].([]interface{})
	lastTool := tools[len(tools)-1].(map[string]interface{})
	if !hasCacheControl(lastTool) {
		t.Error("last tool should have cache_control")
	}

	// system should be converted to an array and carry cache_control
	sys, ok := m["system"].([]interface{})
	if !ok || len(sys) == 0 {
		t.Fatal("system should be converted to array")
	}
	sysBlock := sys[len(sys)-1].(map[string]interface{})
	if !hasCacheControl(sysBlock) {
		t.Error("last system block should have cache_control")
	}

	// the last block of the last assistant message should have cache_control
	msgs := m["messages"].([]interface{})
	lastMsg := msgs[len(msgs)-1].(map[string]interface{})
	content := lastMsg["content"].([]interface{})
	lastBlock := content[len(content)-1].(map[string]interface{})
	if !hasCacheControl(lastBlock) {
		t.Error("last assistant block should have cache_control")
	}
}

// 4 breakpoints already present -> no new ones added.
func TestInjectCacheControl_AlreadyFull_NoNewBreakpoints(t *testing.T) {
	body := makeBody(t, map[string]interface{}{
		"model":      "claude-sonnet-4-6-20250514",
		"max_tokens": float64(4096),
		"system": []interface{}{
			map[string]interface{}{
				"type":          "text",
				"text":          "sys",
				"cache_control": map[string]interface{}{"type": "ephemeral"},
			},
		},
		"tools": []interface{}{
			map[string]interface{}{
				"name":          "t1",
				"input_schema":  map[string]interface{}{},
				"cache_control": map[string]interface{}{"type": "ephemeral"},
			},
			map[string]interface{}{
				"name":          "t2",
				"input_schema":  map[string]interface{}{},
				"cache_control": map[string]interface{}{"type": "ephemeral"},
			},
			map[string]interface{}{
				"name":          "t3",
				"input_schema":  map[string]interface{}{},
				"cache_control": map[string]interface{}{"type": "ephemeral"},
			},
		},
		"messages": []interface{}{
			map[string]interface{}{
				"role":    "user",
				"content": []interface{}{map[string]interface{}{"type": "text", "text": "hi"}},
			},
		},
	})
	ir := &protocols.IRRequest{Model: "claude-sonnet-4-6-20250514"}
	out, _ := OptimizeBody(body, ir, true, "")
	m := parseBody(t, out)

	total := countCacheControls(m)
	if total != 4 {
		t.Errorf("cache_control count = %d, want 4 (no new injections)", total)
	}
}

// 2 breakpoints already present -> inject more up to the budget.
func TestInjectCacheControl_PartialFill(t *testing.T) {
	body := makeBody(t, map[string]interface{}{
		"model":      "claude-sonnet-4-6-20250514",
		"max_tokens": float64(4096),
		"system": []interface{}{
			map[string]interface{}{
				"type":          "text",
				"text":          "sys",
				"cache_control": map[string]interface{}{"type": "ephemeral"},
			},
		},
		"tools": []interface{}{
			map[string]interface{}{
				"name":          "t1",
				"input_schema":  map[string]interface{}{},
				"cache_control": map[string]interface{}{"type": "ephemeral"},
			},
		},
		"messages": []interface{}{
			map[string]interface{}{
				"role":    "user",
				"content": []interface{}{map[string]interface{}{"type": "text", "text": "hi"}},
			},
			map[string]interface{}{
				"role":    "assistant",
				"content": []interface{}{map[string]interface{}{"type": "text", "text": "hello"}},
			},
		},
	})
	ir := &protocols.IRRequest{Model: "claude-sonnet-4-6-20250514"}
	out, _ := OptimizeBody(body, ir, true, "")
	m := parseBody(t, out)
	total := countCacheControls(m)
	if total != 3 {
		// tools(1 existing) + system(1 existing) + assistant block(1 new) = 3
		// budget starts at 4-2=2, injects 1 more for assistant
		t.Errorf("cache_control count = %d, want 3", total)
	}
}

// system is a string -> convert to an array before injecting (valid Anthropic format).
func TestInjectCacheControl_StringSystemConverted(t *testing.T) {
	body := makeBody(t, map[string]interface{}{
		"model":      "claude-sonnet-4-6-20250514",
		"max_tokens": float64(4096),
		"system":     "You are helpful.",
		"messages": []interface{}{
			map[string]interface{}{
				"role":    "user",
				"content": []interface{}{map[string]interface{}{"type": "text", "text": "hi"}},
			},
		},
	})
	ir := &protocols.IRRequest{Model: "claude-sonnet-4-6-20250514"}
	out, _ := OptimizeBody(body, ir, true, "")
	m := parseBody(t, out)

	sys, ok := m["system"].([]interface{})
	if !ok || len(sys) == 0 {
		t.Fatal("system should be converted to array")
	}
	sysBlock, ok := sys[0].(map[string]interface{})
	if !ok {
		t.Fatal("system[0] should be a block object")
	}
	if sysBlock["type"] != "text" {
		t.Errorf("system[0].type = %v, want text", sysBlock["type"])
	}
	if sysBlock["text"] != "You are helpful." {
		t.Errorf("system[0].text = %v, want 'You are helpful.'", sysBlock["text"])
	}
	if !hasCacheControl(sysBlock) {
		t.Error("system[0] should have cache_control")
	}
}

// assistant content contains thinking blocks -> skip thinking, inject into the last non-thinking block.
func TestInjectCacheControl_SkipsThinkingBlocks(t *testing.T) {
	body := makeBody(t, map[string]interface{}{
		"model":      "claude-sonnet-4-6-20250514",
		"max_tokens": float64(4096),
		"messages": []interface{}{
			map[string]interface{}{
				"role":    "user",
				"content": []interface{}{map[string]interface{}{"type": "text", "text": "hi"}},
			},
			map[string]interface{}{
				"role": "assistant",
				"content": []interface{}{
					map[string]interface{}{"type": "text", "text": "I'll think."},
					map[string]interface{}{"type": "thinking", "thinking": "..."},
					map[string]interface{}{"type": "redacted_thinking", "data": "..."},
				},
			},
		},
	})
	ir := &protocols.IRRequest{Model: "claude-sonnet-4-6-20250514"}
	out, _ := OptimizeBody(body, ir, true, "")
	m := parseBody(t, out)

	msgs := m["messages"].([]interface{})
	lastMsg := msgs[len(msgs)-1].(map[string]interface{})
	content := lastMsg["content"].([]interface{})

	// the text block (index 0) should have cache_control
	textBlock := content[0].(map[string]interface{})
	if textBlock["type"] != "text" {
		t.Fatalf("expected text block at index 0, got %v", textBlock["type"])
	}
	if !hasCacheControl(textBlock) {
		t.Error("text block (last non-thinking) should have cache_control")
	}

	// thinking block should not have cache_control
	thinkBlock := content[1].(map[string]interface{})
	if hasCacheControl(thinkBlock) {
		t.Error("thinking block should not have cache_control")
	}
	redactedBlock := content[2].(map[string]interface{})
	if hasCacheControl(redactedBlock) {
		t.Error("redacted_thinking block should not have cache_control")
	}
}

// adaptive model + max_tokens=512 (<=1024) -> thinking is still injected (adaptive has no budget constraint).
func TestOptimizeBody_AdaptiveModel_SmallMaxTokens_StillInjectsThinking(t *testing.T) {
	body := bodyWithThinking(t, 4096, 512)
	ir := irReqThinking("claude-opus-4-6-20250514", intPtr(4096), intPtr(512), nil, nil)

	out, injected := OptimizeBody(body, ir, false, "")
	if !injected {
		t.Fatal("adaptive model with max_tokens=512 should still inject thinking")
	}
	m := parseBody(t, out)
	if thinkingType(t, m) != "adaptive" {
		t.Errorf("thinking.type = %q, want adaptive", thinkingType(t, m))
	}
	if _, has := m["output_config"]; !has {
		t.Error("output_config should be present for adaptive thinking")
	}
}

// adaptive model, body already has other output_config fields -> merge instead of full overwrite.
func TestOptimizeBody_AdaptiveModel_OutputConfigMerge(t *testing.T) {
	body := makeBody(t, map[string]interface{}{
		"model":       "claude-sonnet-4-6-20250514",
		"max_tokens":  float64(8192),
		"thinking":    map[string]interface{}{"type": "enabled", "budget_tokens": float64(4096)},
		"temperature": float64(1),
		"output_config": map[string]interface{}{
			"future_field": "keep_me",
		},
	})
	ir := irReqThinking("claude-sonnet-4-6-20250514", intPtr(4096), intPtr(8192), nil, nil)

	out, injected := OptimizeBody(body, ir, false, "")
	if !injected {
		t.Fatal("expected thinkingInjected=true")
	}
	m := parseBody(t, out)
	oc, ok := m["output_config"].(map[string]interface{})
	if !ok {
		t.Fatal("output_config should be present")
	}
	if oc["effort"] != "max" {
		t.Errorf("output_config.effort = %v, want max", oc["effort"])
	}
	if oc["future_field"] != "keep_me" {
		t.Errorf("output_config.future_field = %v, want keep_me (should not be dropped)", oc["future_field"])
	}
}

// last assistant message content is a string -> continue to the previous assistant and inject into its array content.
func TestInjectCacheControl_LastAssistantStringContent_FallsBackToPrevious(t *testing.T) {
	body := makeBody(t, map[string]interface{}{
		"model":      "claude-sonnet-4-6-20250514",
		"max_tokens": float64(4096),
		"messages": []interface{}{
			map[string]interface{}{
				"role":    "user",
				"content": []interface{}{map[string]interface{}{"type": "text", "text": "q1"}},
			},
			map[string]interface{}{
				"role":    "assistant",
				"content": []interface{}{map[string]interface{}{"type": "text", "text": "detailed reply"}},
			},
			map[string]interface{}{
				"role":    "user",
				"content": []interface{}{map[string]interface{}{"type": "text", "text": "follow-up"}},
			},
			map[string]interface{}{
				"role":    "assistant",
				"content": "sure", // string content -- cannot inject, should continue rather than break
			},
		},
	})
	ir := &protocols.IRRequest{Model: "claude-sonnet-4-6-20250514"}
	out, _ := OptimizeBody(body, ir, true, "")
	m := parseBody(t, out)

	msgs := m["messages"].([]interface{})
	// msgs[1] is the assistant with array content and should be the injection target
	prevAssistant := msgs[1].(map[string]interface{})
	content := prevAssistant["content"].([]interface{})
	block := content[0].(map[string]interface{})
	if !hasCacheControl(block) {
		t.Error("previous assistant with array content should get cache_control when last assistant has string content")
	}
}

// legacy model, thinking map exists but has no budget_tokens -> fall back to irReq.BudgetTokens.
func TestOptimizeBody_LegacyModel_BudgetFallbackToIRReq(t *testing.T) {
	// thinking map has a type but no budget_tokens (abnormal encoder case)
	body := makeBody(t, map[string]interface{}{
		"model":       "claude-sonnet-4-5-20251115",
		"max_tokens":  float64(8192),
		"thinking":    map[string]interface{}{"type": "enabled"}, // no budget_tokens
		"temperature": float64(1),
	})
	ir := irReqThinking("claude-sonnet-4-5-20251115", intPtr(2000), intPtr(8192), nil, nil)

	out, injected := OptimizeBody(body, ir, false, "")
	if !injected {
		t.Fatal("expected thinkingInjected=true")
	}
	m := parseBody(t, out)
	if got := thinkingBudget(t, m); got != 2000 {
		t.Errorf("budget_tokens = %d, want 2000 (from irReq.BudgetTokens fallback)", got)
	}
}

// appendBeta deduplicates repeated beta values, avoiding duplicate writes.
func TestAppendBeta_Dedup(t *testing.T) {
	req, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", nil)
	req.Header.Set("anthropic-beta", "feature-a")
	ir := &protocols.IRRequest{Model: "claude-sonnet-4-5-20251115"}
	InjectBetaHeaders(req, ir, true)
	InjectBetaHeaders(req, ir, true) // second call should not write a duplicate
	got := req.Header.Get("anthropic-beta")
	want := "feature-a,interleaved-thinking-2025-05-14"
	if got != want {
		t.Errorf("appendBeta dedup: got %q, want %q", got, want)
	}
}

// appendBeta correctly merges a multi-value header (written via multiple Add calls).
func TestAppendBeta_MultiValueHeader(t *testing.T) {
	req, _ := http.NewRequest("POST", "https://api.anthropic.com/v1/messages", nil)
	req.Header.Add("anthropic-beta", "feature-a")
	req.Header.Add("anthropic-beta", "feature-b")
	ir := &protocols.IRRequest{Model: "claude-sonnet-4-5-20251115"}
	InjectBetaHeaders(req, ir, true)
	got := req.Header.Get("anthropic-beta")
	// both feature-a and feature-b should be kept, with the new beta appended
	if got != "feature-a,feature-b,interleaved-thinking-2025-05-14" {
		t.Errorf("multi-value header merge: got %q, want feature-a,feature-b,interleaved-thinking-2025-05-14", got)
	}
}

func TestInjectCacheControlWithCustomPrompt(t *testing.T) {
	t.Run("creates array form when system field is entirely absent", func(t *testing.T) {
		body := []byte(`{"model":"claude-sonnet-4-6","messages":[{"role":"user","content":"hi"}]}`)
		out := InjectCacheControl(body, "自定义提示词")
		var m map[string]interface{}
		if err := json.Unmarshal(out, &m); err != nil {
			t.Fatalf("unmarshal failed: %v", err)
		}
		sys, ok := m["system"].([]interface{})
		if !ok || len(sys) != 1 {
			t.Fatalf("expected system to be a 1-element array, got %#v", m["system"])
		}
		block := sys[0].(map[string]interface{})
		if block["text"] != "自定义提示词" {
			t.Errorf("text = %v, want 自定义提示词", block["text"])
		}
		if _, has := block["cache_control"]; !has {
			t.Error("expected cache_control on the injected block, got none -- the appended custom text must be able to receive cache_control")
		}
	})

	t.Run("normalizes string system into a two-block array", func(t *testing.T) {
		body := []byte(`{"model":"claude-sonnet-4-6","system":"原始系统提示","messages":[]}`)
		out := InjectCacheControl(body, "自定义追加")
		var m map[string]interface{}
		_ = json.Unmarshal(out, &m)
		sys, ok := m["system"].([]interface{})
		if !ok || len(sys) != 2 {
			t.Fatalf("expected system to be a 2-element array, got %#v", m["system"])
		}
		if sys[0].(map[string]interface{})["text"] != "原始系统提示" {
			t.Errorf("first block text = %v, want 原始系统提示", sys[0].(map[string]interface{})["text"])
		}
		if sys[1].(map[string]interface{})["text"] != "自定义追加" {
			t.Errorf("second block text = %v, want 自定义追加", sys[1].(map[string]interface{})["text"])
		}
	})

	t.Run("appends a new block when system is already an array", func(t *testing.T) {
		body := []byte(`{"model":"claude-sonnet-4-6","system":[{"type":"text","text":"块1"}],"messages":[]}`)
		out := InjectCacheControl(body, "追加块")
		var m map[string]interface{}
		_ = json.Unmarshal(out, &m)
		sys := m["system"].([]interface{})
		if len(sys) != 2 {
			t.Fatalf("expected 2 blocks, got %d", len(sys))
		}
		if sys[1].(map[string]interface{})["text"] != "追加块" {
			t.Errorf("second block text = %v, want 追加块", sys[1].(map[string]interface{})["text"])
		}
	})

	t.Run("empty customPrompt does not append and does not affect existing cache_control behavior", func(t *testing.T) {
		body := []byte(`{"model":"claude-sonnet-4-6","system":"原始","messages":[]}`)
		out := InjectCacheControl(body, "")
		var m map[string]interface{}
		_ = json.Unmarshal(out, &m)
		sys := m["system"].([]interface{})
		if len(sys) != 1 {
			t.Fatalf("expected system untouched (1 block), got %d blocks", len(sys))
		}
	})

	t.Run("budget exhausted: appended text is still sent but does not receive cache_control", func(t *testing.T) {
		// Build a request that already uses all 4 cache_control markers (3 tools + 1 assistant block)
		body := []byte(`{
			"model":"claude-sonnet-4-6",
			"tools":[
				{"name":"t1","cache_control":{"type":"ephemeral"}},
				{"name":"t2","cache_control":{"type":"ephemeral"}},
				{"name":"t3","cache_control":{"type":"ephemeral"}}
			],
			"messages":[{"role":"assistant","content":[{"type":"text","text":"reply","cache_control":{"type":"ephemeral"}}]}]
		}`)
		out := InjectCacheControl(body, "追加文本")
		var m map[string]interface{}
		_ = json.Unmarshal(out, &m)
		sys, ok := m["system"].([]interface{})
		if !ok || len(sys) != 1 {
			t.Fatalf("expected system created with 1 block despite budget exhausted, got %#v", m["system"])
		}
		if sys[0].(map[string]interface{})["text"] != "追加文本" {
			t.Errorf("text = %v, want 追加文本", sys[0].(map[string]interface{})["text"])
		}
		if _, has := sys[0].(map[string]interface{})["cache_control"]; has {
			t.Error("budget is exhausted; the new block should not receive cache_control")
		}
		// Confirm the original 4 markers were not evicted or rewritten
		tools := m["tools"].([]interface{})
		for i, tool := range tools {
			if _, has := tool.(map[string]interface{})["cache_control"]; !has {
				t.Errorf("tool[%d]'s original cache_control was unexpectedly removed", i)
			}
		}
	})
}

// TestClaudeInjectionNullBodyNoPanic is a regression test: when the body is the
// JSON literal null (Decode yields m==nil), none of the three injection paths
// (OptimizeBody/InjectCacheControl/InjectCustomSystemPromptOnly) may panic, and
// each must return the body unchanged.
func TestClaudeInjectionNullBodyNoPanic(t *testing.T) {
	null := []byte(`null`)
	if out, _ := OptimizeBody(null, nil, true, "自定义提示词"); string(out) != "null" {
		t.Errorf("OptimizeBody null body should be returned unchanged, got %s", out)
	}
	if out := InjectCacheControl(null, "自定义提示词"); string(out) != "null" {
		t.Errorf("InjectCacheControl null body should be returned unchanged, got %s", out)
	}
	if out := InjectCustomSystemPromptOnly(null, "自定义提示词"); string(out) != "null" {
		t.Errorf("InjectCustomSystemPromptOnly null body should be returned unchanged, got %s", out)
	}
}

// TestClaudeMalformedSystemPreserved is a regression test: when the Claude system
// field is malformed (object/number/bool/null), do not overwrite the client's
// original value and skip injection (matching the preserve semantics of the other
// relay-side protocols); create it when missing; put the custom text directly when
// it is an empty string.
func TestClaudeMalformedSystemPreserved(t *testing.T) {
	// Each case gives the original malformed system value, which must be preserved
	// value-for-value after injection (not a loose "as long as it isn't an array"
	// assertion, which would let a buggy implementation turn an object into a
	// string/nil/another wrong type slip through). All three injection paths must preserve it.
	malformed := []struct {
		name string
		body string
		want any // the value system should equal after json.Unmarshal
	}{
		{"object", `{"model":"claude-sonnet-4-6","system":{"x":1},"messages":[]}`, map[string]any{"x": float64(1)}},
		{"number", `{"model":"claude-sonnet-4-6","system":42,"messages":[]}`, float64(42)},
		{"bool", `{"model":"claude-sonnet-4-6","system":true,"messages":[]}`, true},
		{"null", `{"model":"claude-sonnet-4-6","system":null,"messages":[]}`, nil},
	}
	inject := map[string]func([]byte, string) []byte{
		"InjectCacheControl":           func(b []byte, p string) []byte { return InjectCacheControl(b, p) },
		"InjectCustomSystemPromptOnly": func(b []byte, p string) []byte { return InjectCustomSystemPromptOnly(b, p) },
		"OptimizeBody":                 func(b []byte, p string) []byte { out, _ := OptimizeBody(b, nil, true, p); return out },
	}
	for _, tc := range malformed {
		for fnName, fn := range inject {
			out := fn([]byte(tc.body), "自定义文本")
			var m map[string]interface{}
			if err := json.Unmarshal(out, &m); err != nil {
				t.Fatalf("[%s/%s] unmarshal failed: %v (out=%s)", fnName, tc.name, err, out)
			}
			if !reflect.DeepEqual(m["system"], tc.want) {
				t.Errorf("[%s/%s] malformed system should preserve the original value verbatim %#v, got %#v (out=%s)", fnName, tc.name, tc.want, m["system"], out)
			}
		}
	}
	// missing -> create
	out := InjectCustomSystemPromptOnly([]byte(`{"model":"claude-sonnet-4-6","messages":[]}`), "自定义文本")
	var m map[string]interface{}
	_ = json.Unmarshal(out, &m)
	if sys, ok := m["system"].([]interface{}); !ok || len(sys) != 1 || sys[0].(map[string]interface{})["text"] != "自定义文本" {
		t.Errorf("missing system should create an array containing the custom text, got %#v", m["system"])
	}
	// empty string -> put the custom text directly (do not keep an empty block)
	out2 := InjectCustomSystemPromptOnly([]byte(`{"model":"claude-sonnet-4-6","system":"","messages":[]}`), "自定义文本")
	var m2 map[string]interface{}
	_ = json.Unmarshal(out2, &m2)
	sys2 := m2["system"].([]interface{})
	if len(sys2) != 1 || sys2[0].(map[string]interface{})["text"] != "自定义文本" {
		t.Errorf("empty-string system should be normalized to an array containing only the custom text, got %#v", m2["system"])
	}
}

// TestClaudeTrailingGarbageNotTruncated is a regression test: all three Claude
// entry points return the body unchanged and skip injection when the body has trailing garbage.
func TestClaudeTrailingGarbageNotTruncated(t *testing.T) {
	body := []byte(`{"model":"claude-sonnet-4-6","messages":[]}garbage`)
	if out, _ := OptimizeBody(body, nil, true, "自定义文本"); string(out) != string(body) {
		t.Errorf("OptimizeBody with trailing garbage should be returned unchanged, got %s", out)
	}
	if out := InjectCacheControl(body, "自定义文本"); string(out) != string(body) {
		t.Errorf("InjectCacheControl with trailing garbage should be returned unchanged, got %s", out)
	}
	if out := InjectCustomSystemPromptOnly(body, "自定义文本"); string(out) != string(body) {
		t.Errorf("InjectCustomSystemPromptOnly with trailing garbage should be returned unchanged, got %s", out)
	}
}
