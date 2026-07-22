package claude

import (
	"encoding/json"
	"github.com/yolorouter/yolorouter/internal/protocols"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// --- ResponseDecoder ---

func TestClaudeResponseDecoder_BasicText(t *testing.T) {
	body := json.RawMessage(`{
		"id": "msg_123",
		"model": "claude-3-5-sonnet-20241022",
		"stop_reason": "end_turn",
		"content": [{"type": "text", "text": "Hello!"}],
		"usage": {"input_tokens": 10, "output_tokens": 5}
	}`)

	irResp, err := ResponseDecoder{}.DecodeResponse(body)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if irResp.ID != "msg_123" {
		t.Errorf("ID = %q", irResp.ID)
	}
	if irResp.Model != "claude-3-5-sonnet-20241022" {
		t.Errorf("Model = %q", irResp.Model)
	}
	if irResp.Content != "Hello!" {
		t.Errorf("Content = %q", irResp.Content)
	}
	if irResp.StopReason != "stop" {
		t.Errorf("StopReason = %q", irResp.StopReason)
	}
	if irResp.Usage.PromptTokens != 10 {
		t.Errorf("PromptTokens = %d", irResp.Usage.PromptTokens)
	}
	if irResp.Usage.CompletionTokens != 5 {
		t.Errorf("CompletionTokens = %d", irResp.Usage.CompletionTokens)
	}
	if irResp.Usage.TotalTokens != 15 {
		t.Errorf("TotalTokens = %d", irResp.Usage.TotalTokens)
	}
}

func TestClaudeResponseDecoder_ToolUse(t *testing.T) {
	body := json.RawMessage(`{
		"id": "msg_456",
		"model": "claude-3-opus",
		"stop_reason": "tool_use",
		"content": [
			{"type": "text", "text": "Let me check."},
			{"type": "tool_use", "id": "toolu_abc", "name": "get_weather", "input": {"city": "Beijing"}}
		],
		"usage": {"input_tokens": 20, "output_tokens": 30}
	}`)

	irResp, err := ResponseDecoder{}.DecodeResponse(body)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if irResp.StopReason != "tool_calls" {
		t.Errorf("StopReason = %q, want tool_calls", irResp.StopReason)
	}
	if irResp.Content != "Let me check." {
		t.Errorf("Content = %q", irResp.Content)
	}
	if len(irResp.ToolCalls) != 1 {
		t.Fatalf("ToolCalls count = %d, want 1", len(irResp.ToolCalls))
	}
	tc := irResp.ToolCalls[0]
	if tc.ID != "toolu_abc" {
		t.Errorf("ToolCall ID = %q", tc.ID)
	}
	if tc.Name != "get_weather" {
		t.Errorf("ToolCall Name = %q", tc.Name)
	}
	if tc.Arguments != `{"city": "Beijing"}` {
		t.Errorf("ToolCall Arguments = %q", tc.Arguments)
	}
}

func TestClaudeResponseDecoder_Thinking(t *testing.T) {
	body := json.RawMessage(`{
		"id": "msg_789",
		"model": "claude-3-5-sonnet-20241022",
		"stop_reason": "end_turn",
		"content": [
			{"type": "thinking", "thinking": "Let me reason about this..."},
			{"type": "text", "text": "The answer is 42."}
		],
		"usage": {"input_tokens": 100, "output_tokens": 50}
	}`)

	irResp, err := ResponseDecoder{}.DecodeResponse(body)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if irResp.ReasoningContent != "Let me reason about this..." {
		t.Errorf("ReasoningContent = %q", irResp.ReasoningContent)
	}
	if irResp.Content != "The answer is 42." {
		t.Errorf("Content = %q", irResp.Content)
	}
}

func TestClaudeResponseDecoder_CacheTokens(t *testing.T) {
	body := json.RawMessage(`{
		"id": "msg_cache",
		"model": "claude-3-5-sonnet-20241022",
		"stop_reason": "end_turn",
		"content": [{"type": "text", "text": "ok"}],
		"usage": {
			"input_tokens": 100,
			"output_tokens": 10,
			"cache_creation_input_tokens": 50,
			"cache_read_input_tokens": 30
		}
	}`)

	irResp, err := ResponseDecoder{}.DecodeResponse(body)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if irResp.Usage.CacheWriteTokens != 50 {
		t.Errorf("CacheWriteTokens = %d, want 50", irResp.Usage.CacheWriteTokens)
	}
	if irResp.Usage.CacheReadTokens != 30 {
		t.Errorf("CacheReadTokens = %d, want 30", irResp.Usage.CacheReadTokens)
	}
}

func TestClaudeResponseDecoder_EmptyToolInput(t *testing.T) {
	body := json.RawMessage(`{
		"id": "msg_empty_input",
		"model": "claude-3-opus",
		"stop_reason": "tool_use",
		"content": [
			{"type": "tool_use", "id": "toolu_noinput", "name": "list_files", "input": {}}
		],
		"usage": {"input_tokens": 5, "output_tokens": 3}
	}`)

	irResp, err := ResponseDecoder{}.DecodeResponse(body)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if len(irResp.ToolCalls) != 1 {
		t.Fatalf("ToolCalls count = %d", len(irResp.ToolCalls))
	}
	if irResp.ToolCalls[0].Arguments != "{}" {
		t.Errorf("Empty input should be '{}', got %q", irResp.ToolCalls[0].Arguments)
	}
}

func TestClaudeResponseDecoder_MaxTokens(t *testing.T) {
	body := json.RawMessage(`{
		"id": "msg_max",
		"model": "claude-3-opus",
		"stop_reason": "max_tokens",
		"content": [{"type": "text", "text": "truncated"}],
		"usage": {"input_tokens": 10, "output_tokens": 100}
	}`)

	irResp, err := ResponseDecoder{}.DecodeResponse(body)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if irResp.StopReason != "length" {
		t.Errorf("StopReason = %q, want length", irResp.StopReason)
	}
}

// --- StreamDecoder ---

func TestClaudeStreamDecoder_MessageStart(t *testing.T) {
	dec := NewStreamDecoder()

	deltas, err := dec.DecodeChunk(`data: {"type":"message_start","message":{"id":"msg_s1","model":"claude-3-opus","usage":{"input_tokens":50,"output_tokens":0}}}`)
	if err != nil {
		t.Fatalf("DecodeChunk: %v", err)
	}

	foundStart := false
	for _, d := range deltas {
		if ms, ok := d.(protocols.DeltaMessageStart); ok {
			foundStart = true
			if ms.ID != "msg_s1" {
				t.Errorf("ID = %q", ms.ID)
			}
			if ms.Model != "claude-3-opus" {
				t.Errorf("Model = %q", ms.Model)
			}
		}
	}
	if !foundStart {
		t.Error("Missing protocols.DeltaMessageStart")
	}
}

func TestClaudeStreamDecoder_TextDelta(t *testing.T) {
	dec := NewStreamDecoder()

	deltas, _ := dec.DecodeChunk(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello "}}`)
	deltas2, _ := dec.DecodeChunk(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"world!"}}`)
	deltas = append(deltas, deltas2...)

	texts := []string{}
	for _, d := range deltas {
		if td, ok := d.(protocols.DeltaText); ok {
			texts = append(texts, td.Text)
		}
	}
	if len(texts) != 2 {
		t.Fatalf("text deltas = %d, want 2", len(texts))
	}
	if texts[0] != "Hello " || texts[1] != "world!" {
		t.Errorf("texts = %v", texts)
	}
}

func TestClaudeStreamDecoder_ThinkingDelta(t *testing.T) {
	dec := NewStreamDecoder()

	deltas, _ := dec.DecodeChunk(`data: {"type":"content_block_delta","index":0,"delta":{"type":"thinking_delta","thinking":"Let me think..."}}`)

	found := false
	for _, d := range deltas {
		if td, ok := d.(protocols.DeltaThinking); ok {
			found = true
			if td.Text != "Let me think..." {
				t.Errorf("Thinking = %q", td.Text)
			}
		}
	}
	if !found {
		t.Error("Missing protocols.DeltaThinking")
	}
}

func TestClaudeStreamDecoder_ToolUse(t *testing.T) {
	dec := NewStreamDecoder()

	// content_block_start for tool_use
	deltas1, _ := dec.DecodeChunk(`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_abc","name":"get_weather"}}`)

	// input_json_delta
	deltas2, _ := dec.DecodeChunk(`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{\"city\":"}}`)
	deltas3, _ := dec.DecodeChunk(`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"\"Beijing\"}"}}`)

	allDeltas := append(deltas1, deltas2...)
	allDeltas = append(allDeltas, deltas3...)

	var foundStart bool
	var argsParts []string
	for _, d := range allDeltas {
		switch v := d.(type) {
		case protocols.DeltaToolCallStart:
			foundStart = true
			if v.ID != "toolu_abc" {
				t.Errorf("ToolCall ID = %q", v.ID)
			}
			if v.Name != "get_weather" {
				t.Errorf("ToolCall Name = %q", v.Name)
			}
		case protocols.DeltaToolCallArgs:
			argsParts = append(argsParts, v.Arguments)
		}
	}
	if !foundStart {
		t.Error("Missing protocols.DeltaToolCallStart")
	}
	if len(argsParts) != 2 {
		t.Fatalf("protocols.DeltaToolCallArgs count = %d, want 2", len(argsParts))
	}
	combined := argsParts[0] + argsParts[1]
	if combined != `{"city":"Beijing"}` {
		t.Errorf("Combined args = %q", combined)
	}
}

func TestClaudeStreamDecoder_MultipleToolCalls(t *testing.T) {
	dec := NewStreamDecoder()

	// First tool call
	_, _ = dec.DecodeChunk(`data: {"type":"content_block_start","index":1,"content_block":{"type":"tool_use","id":"toolu_1","name":"func_a"}}`)
	_, _ = dec.DecodeChunk(`data: {"type":"content_block_delta","index":1,"delta":{"type":"input_json_delta","partial_json":"{}"}}`)
	_, _ = dec.DecodeChunk(`data: {"type":"content_block_stop","index":1}}`)

	// Second tool call
	deltas, _ := dec.DecodeChunk(`data: {"type":"content_block_start","index":2,"content_block":{"type":"tool_use","id":"toolu_2","name":"func_b"}}`)

	var starts []protocols.DeltaToolCallStart
	for _, d := range deltas {
		if s, ok := d.(protocols.DeltaToolCallStart); ok {
			starts = append(starts, s)
		}
	}
	if len(starts) != 1 {
		t.Fatalf("protocols.DeltaToolCallStart count = %d, want 1", len(starts))
	}
	// Index should increment (0 for first, 1 for second)
	if starts[0].Index != 1 {
		t.Errorf("Second tool call Index = %d, want 1", starts[0].Index)
	}
	if starts[0].Name != "func_b" {
		t.Errorf("Second tool call Name = %q", starts[0].Name)
	}
}

func TestClaudeStreamDecoder_MessageDeltaWithStopReason(t *testing.T) {
	dec := NewStreamDecoder()

	deltas, _ := dec.DecodeChunk(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":42}}`)

	var foundDone, foundUsage bool
	for _, d := range deltas {
		switch v := d.(type) {
		case protocols.DeltaDone:
			foundDone = true
			if v.StopReason != "stop" {
				t.Errorf("StopReason = %q, want stop", v.StopReason)
			}
		case protocols.DeltaUsage:
			foundUsage = true
			if v.Usage.CompletionTokens != 42 {
				t.Errorf("CompletionTokens = %d", v.Usage.CompletionTokens)
			}
		}
	}
	if !foundDone {
		t.Error("Missing protocols.DeltaDone")
	}
	if !foundUsage {
		t.Error("Missing protocols.DeltaUsage")
	}
}

func TestClaudeStreamDecoder_IgnoresEventLines(t *testing.T) {
	dec := NewStreamDecoder()

	deltas, err := dec.DecodeChunk(`event: message_start`)
	if err != nil {
		t.Fatalf("DecodeChunk: %v", err)
	}
	if len(deltas) != 0 {
		t.Errorf("event: lines should be ignored, got %d deltas", len(deltas))
	}
}

func TestClaudeStreamDecoder_IgnoresEmptyLines(t *testing.T) {
	dec := NewStreamDecoder()

	deltas, err := dec.DecodeChunk(``)
	if err != nil {
		t.Fatalf("DecodeChunk: %v", err)
	}
	if len(deltas) != 0 {
		t.Errorf("empty lines should be ignored, got %d deltas", len(deltas))
	}
}

func TestClaudeStreamDecoder_MessageStartWithCacheUsage(t *testing.T) {
	dec := NewStreamDecoder()

	deltas, _ := dec.DecodeChunk(`data: {"type":"message_start","message":{"id":"msg_c","model":"claude-3-opus","usage":{"input_tokens":100,"output_tokens":0,"cache_creation_input_tokens":20,"cache_read_input_tokens":30}}}`)

	for _, d := range deltas {
		if ms, ok := d.(protocols.DeltaMessageStart); ok {
			_ = ms
		}
	}
	// Internal usage should track cache tokens
	if dec.usage.CacheWriteTokens != 20 {
		t.Errorf("CacheWriteTokens = %d, want 20", dec.usage.CacheWriteTokens)
	}
	if dec.usage.CacheReadTokens != 30 {
		t.Errorf("CacheReadTokens = %d, want 30", dec.usage.CacheReadTokens)
	}
}

func TestClaudeStreamDecoder_FinishReturnsNilWithoutStart(t *testing.T) {
	dec := NewStreamDecoder()
	deltas, err := dec.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	if len(deltas) != 0 {
		t.Errorf("Finish should return nil before message_start, got %d deltas", len(deltas))
	}
}

// TestClaudeStreamDecoder_FinishEmitsUsageWhenMessageDeltaMissing is a regression
// test for a real incident: some upstream Anthropic-compatible endpoints skip the
// standard message_delta usage event and jump straight from content_block_stop to
// message_stop. A previous version of Finish() returned nil, so the input_tokens
// parsed from message_start were never propagated to the outer collector, leaving
// prompt_tokens and cost at 0 in the relay logs. This test asserts that even when
// the upstream never sends message_delta, Finish() must still emit the accumulated
// usage so the caller at least receives input_tokens.
func TestClaudeStreamDecoder_FinishEmitsUsageWhenMessageDeltaMissing(t *testing.T) {
	dec := NewStreamDecoder()
	// Simulate the real upstream sequence: message_start carries input_tokens=335,
	// then content_block_start / content_block_delta / content_block_stop, and
	// finally message_stop directly without any message_delta.
	_, _ = dec.DecodeChunk(`data: {"type":"message_start","message":{"id":"msg_x","model":"deepseek-v4-pro","usage":{"input_tokens":335,"output_tokens":0}}}` + "\n")
	_, _ = dec.DecodeChunk(`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n")
	_, _ = dec.DecodeChunk(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}` + "\n")
	_, _ = dec.DecodeChunk(`data: {"type":"content_block_stop","index":0}` + "\n")
	_, _ = dec.DecodeChunk(`data: {"type":"message_stop"}` + "\n")

	deltas, err := dec.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	var usage *protocols.IRUsage
	for _, d := range deltas {
		if du, ok := d.(protocols.DeltaUsage); ok {
			u := du.Usage
			usage = &u
		}
	}
	if usage == nil {
		t.Fatalf("Finish must emit DeltaUsage when message_delta missing; got %d deltas, none DeltaUsage", len(deltas))
	}
	if usage.PromptTokens != 335 {
		t.Errorf("PromptTokens = %d, want 335 (input_tokens from message_start)", usage.PromptTokens)
	}
}

// TestClaudeStreamDecoder_FinishDoesNotEmitWhenTruncated tightens the fallback: only
// after receiving message_stop may the input_tokens from message_start be treated as
// billable successful usage. Otherwise an upstream that is truncated after message_start
// (EOF or an upstream error event) would be counted as a successful charge, introducing
// a billing-correctness risk.
func TestClaudeStreamDecoder_FinishDoesNotEmitWhenTruncated(t *testing.T) {
	dec := NewStreamDecoder()
	// Only message_start + content_block_* were sent, no message_stop (simulating a truncated stream).
	_, _ = dec.DecodeChunk(`data: {"type":"message_start","message":{"id":"msg_x","model":"deepseek-v4-pro","usage":{"input_tokens":335,"output_tokens":0}}}` + "\n")
	_, _ = dec.DecodeChunk(`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}` + "\n")
	_, _ = dec.DecodeChunk(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hello"}}` + "\n")

	deltas, err := dec.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}
	for _, d := range deltas {
		if _, ok := d.(protocols.DeltaUsage); ok {
			t.Fatal("Finish must NOT emit DeltaUsage before message_stop — otherwise a truncated stream would be billed as success")
		}
	}
}

// TestClaudeStreamDecoder_ErrorEventPropagatesToFinish covers the case where a Claude
// upstream type:"error" event used to be silently ignored, so an explicit in-stream
// failure was settled as success and charged to the user. After the fix, Finish must
// return a non-nil error so the caller takes the failure path.
func TestClaudeStreamDecoder_ErrorEventPropagatesToFinish(t *testing.T) {
	dec := NewStreamDecoder()
	_, _ = dec.DecodeChunk(`data: {"type":"message_start","message":{"id":"msg_x","model":"deepseek-v4-pro","usage":{"input_tokens":50,"output_tokens":0}}}` + "\n")
	_, _ = dec.DecodeChunk(`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"partial"}}` + "\n")
	_, _ = dec.DecodeChunk(`data: {"type":"error","error":{"type":"overloaded_error","message":"Service is overloaded"}}` + "\n")

	deltas, err := dec.Finish()
	require.Error(t, err, "a Claude type:\"error\" event must make Finish return an error so the caller can rewrite to 502 / not charge")
	assert.Contains(t, err.Error(), "overloaded_error")
	// Partial usage may still be retained (so provider_cost analysis records upstream consumption), but DeltaDone must not be emitted.
	for _, d := range deltas {
		if _, ok := d.(protocols.DeltaDone); ok {
			t.Fatal("Finish must not emit DeltaDone on the error path, otherwise the relay helper would treat the stream as success")
		}
	}
}

// TestClaudeStreamDecoder_ErrorEventAfterDeltaDoneStillFails covers a trickier scenario:
// message_delta has already emitted DeltaDone (the standard protocol path), and then the
// upstream sends an error event (rare but theoretically possible, e.g. a provider reporting
// a background tool failure after the terminating frame). Finish must still return an error,
// and must not be wrongly suppressed by doneEmitted=true.
func TestClaudeStreamDecoder_ErrorEventAfterDeltaDoneStillFails(t *testing.T) {
	dec := NewStreamDecoder()
	_, _ = dec.DecodeChunk(`data: {"type":"message_start","message":{"id":"msg_x","model":"x","usage":{"input_tokens":5,"output_tokens":0}}}` + "\n")
	_, _ = dec.DecodeChunk(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}` + "\n")
	_, _ = dec.DecodeChunk(`data: {"type":"error","error":{"type":"api_error","message":"background failure"}}` + "\n")

	_, err := dec.Finish()
	require.Error(t, err, "an error event arriving after DeltaDone must still make Finish return an error")
	assert.Contains(t, err.Error(), "api_error")
}

// --- claudeMapStopReason ---

func TestClaudeMapStopReason(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"end_turn", "stop"},
		{"tool_use", "tool_calls"},
		{"max_tokens", "length"},
		{"stop_sequence", "stop"},
		{"", "stop"},
		{"unknown_reason", "unknown_reason"},
	}
	for _, tt := range tests {
		got := claudeMapStopReason(tt.input)
		if got != tt.want {
			t.Errorf("claudeMapStopReason(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}
