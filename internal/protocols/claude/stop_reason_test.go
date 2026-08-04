package claude

import (
	"encoding/json"
	"testing"

	"github.com/yolorouter/yolorouter/internal/protocols"
)

// TestStopReason_TruncationOutranksToolUse covers a run that emitted tool calls
// and was then cut off by max tokens. Reporting "tool_use" would tell the
// client the accumulated arguments are complete and safe to execute, when a
// truncated argument stream may not even be valid JSON. The truncation must
// win.
func TestStopReason_TruncationOutranksToolUse(t *testing.T) {
	body := ResponseEncoder{}.EncodeResponse(&protocols.IRResponse{
		ID:         "msg_1",
		Model:      "claude-opus-4-8",
		StopReason: "length",
		ToolCalls: []protocols.IRToolCall{
			{ID: "toolu_1", Name: "get_weather", Arguments: `{"city":"San Fr`},
		},
	})

	var got struct {
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.StopReason != "max_tokens" {
		t.Errorf("stop_reason = %q, want max_tokens (truncation must not be masked by tool_use)", got.StopReason)
	}
}

// TestStopReason_ToolUseStillWinsOnCleanStop guards the normal case: without an
// abnormal termination, tool calls still drive the stop reason.
func TestStopReason_ToolUseStillWinsOnCleanStop(t *testing.T) {
	body := ResponseEncoder{}.EncodeResponse(&protocols.IRResponse{
		ID:         "msg_2",
		Model:      "claude-opus-4-8",
		StopReason: "stop",
		ToolCalls: []protocols.IRToolCall{
			{ID: "toolu_2", Name: "get_weather", Arguments: `{"city":"SF"}`},
		},
	})

	var got struct {
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.StopReason != "tool_use" {
		t.Errorf("stop_reason = %q, want tool_use", got.StopReason)
	}
}

// TestStopReason_RefusalRoundTrip covers Anthropic's "refusal" stop reason
// (streaming classifiers intervening on a policy violation). It must normalise
// into the IR vocabulary as content_filter — otherwise the abnormal-termination
// guard never fires and a refusal can be masked by a tool-call inference — and
// must come back out as "refusal", which is what Anthropic's enum allows.
// "content_filter" is NOT in that enum, so leaking it produces illegal wire.
func TestStopReason_RefusalRoundTrip(t *testing.T) {
	if got := claudeMapStopReason("refusal"); got != "content_filter" {
		t.Errorf("claudeMapStopReason(refusal) = %q, want content_filter", got)
	}
	if got := irToClaudeStopReason("content_filter"); got != "refusal" {
		t.Errorf("irToClaudeStopReason(content_filter) = %q, want refusal", got)
	}

	// End-to-end: a refused run carrying tool calls must not be reported as a
	// clean tool_use finish, and must land on a legal Anthropic value.
	legal := map[string]bool{
		"end_turn": true, "max_tokens": true, "stop_sequence": true,
		"tool_use": true, "pause_turn": true, "refusal": true,
		"model_context_window_exceeded": true,
	}
	body := ResponseEncoder{}.EncodeResponse(&protocols.IRResponse{
		ID:         "msg_r",
		Model:      "claude-opus-4-8",
		StopReason: claudeMapStopReason("refusal"),
		ToolCalls: []protocols.IRToolCall{
			{ID: "toolu_1", Name: "run", Arguments: `{"cmd":"rm -r`},
		},
	})
	var got struct {
		StopReason string `json:"stop_reason"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if got.StopReason != "refusal" {
		t.Errorf("stop_reason = %q, want refusal (must not be masked by tool_use)", got.StopReason)
	}
	if !legal[got.StopReason] {
		t.Errorf("stop_reason %q is not in Anthropic's enum", got.StopReason)
	}
}

// TestStopReason_IRVocabularyMapsIntoAnthropicEnum covers the conversion
// boundary: every IR-vocabulary value maps into Anthropic's closed enum, an
// absent stop reason becomes JSON null ("" is not a valid enum value), and an
// unknown upstream value passes through verbatim and may remain outside the
// enum.
func TestStopReason_IRVocabularyMapsIntoAnthropicEnum(t *testing.T) {
	legal := map[string]bool{
		"end_turn": true, "max_tokens": true, "stop_sequence": true,
		"tool_use": true, "pause_turn": true, "refusal": true,
		"model_context_window_exceeded": true,
	}
	for _, ir := range []string{"stop", "length", "tool_calls", "content_filter"} {
		if got := irToClaudeStopReason(ir); !legal[got] {
			t.Errorf("irToClaudeStopReason(%q) = %q, which is not in Anthropic's stop_reason enum", ir, got)
		}
	}

	// An absent stop reason must reach the wire as null, never as "".
	body := ResponseEncoder{}.EncodeResponse(&protocols.IRResponse{
		ID: "msg_empty", Model: "claude-opus-4-8", StopReason: "",
	})
	var got map[string]interface{}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if v, present := got["stop_reason"]; !present || v != nil {
		t.Errorf("stop_reason = %#v, want JSON null for an empty IR stop reason", v)
	}

	// Documented trade-off: unknown upstream values pass through and may fall
	// outside the enum. Asserted so the behaviour is a decision, not a surprise.
	if got := irToClaudeStopReason("malformed_function_call"); got != "malformed_function_call" {
		t.Errorf("unknown value must pass through verbatim, got %q", got)
	}
}
