package claude

import (
	"encoding/json"
	"github.com/yolorouter/yolorouter/internal/protocols"
	"strings"
	"testing"
)

// --- StreamEncoder ---

func TestClaudeStreamEncoder_TextStream(t *testing.T) {
	enc := NewStreamEncoder()

	events := enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaMessageStart{ID: "msg_1", Model: "claude-3-opus"},
		protocols.DeltaText{Text: "Hello "},
		protocols.DeltaText{Text: "world!"},
		protocols.DeltaDone{StopReason: "stop"},
	})
	// message_delta + message_stop are deferred to EncodeDone so any DeltaUsage
	// has a chance to be applied before Done.
	events = append(events, enc.EncodeDone()...)

	// Expected events:
	// 1. message_start
	// 2. content_block_start (text)
	// 3. content_block_delta (Hello )
	// 4. content_block_delta (world!)
	// 5. content_block_stop
	// 6. message_delta
	// 7. message_stop
	if len(events) != 7 {
		t.Fatalf("events count = %d, want 7", len(events))
	}

	if events[0].Event != "message_start" {
		t.Errorf("events[0] Event = %q", events[0].Event)
	}
	if events[1].Event != "content_block_start" {
		t.Errorf("events[1] Event = %q", events[1].Event)
	}
	if events[2].Event != "content_block_delta" {
		t.Errorf("events[2] Event = %q", events[2].Event)
	}
	if events[3].Event != "content_block_delta" {
		t.Errorf("events[3] Event = %q", events[3].Event)
	}
	if events[4].Event != "content_block_stop" {
		t.Errorf("events[4] Event = %q", events[4].Event)
	}
	if events[5].Event != "message_delta" {
		t.Errorf("events[5] Event = %q", events[5].Event)
	}
	if events[6].Event != "message_stop" {
		t.Errorf("events[6] Event = %q", events[6].Event)
	}

	// Verify message_start data contains model
	var startData map[string]interface{}
	_ = json.Unmarshal([]byte(events[0].Data), &startData)
	msg := startData["message"].(map[string]interface{})
	if msg["model"] != "claude-3-opus" {
		t.Errorf("message_start model = %v", msg["model"])
	}

	// Verify message_delta stop_reason
	var deltaData map[string]interface{}
	_ = json.Unmarshal([]byte(events[5].Data), &deltaData)
	delta := deltaData["delta"].(map[string]interface{})
	if delta["stop_reason"] != "end_turn" {
		t.Errorf("stop_reason = %v", delta["stop_reason"])
	}
}

func TestClaudeStreamEncoder_ThinkingThenText(t *testing.T) {
	enc := NewStreamEncoder()

	events := enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaMessageStart{ID: "msg_2", Model: "claude-3-5-sonnet"},
		protocols.DeltaThinking{Text: "Let me think..."},
		protocols.DeltaText{Text: "The answer is 42."},
		protocols.DeltaDone{StopReason: "stop"},
	})
	events = append(events, enc.EncodeDone()...)

	// Expected:
	// 1. message_start
	// 2. content_block_start (thinking)
	// 3. content_block_delta (thinking)
	// 4. content_block_stop (thinking)
	// 5. content_block_start (text)
	// 6. content_block_delta (text)
	// 7. content_block_stop
	// 8. message_delta
	// 9. message_stop
	if len(events) != 9 {
		t.Fatalf("events count = %d, want 9", len(events))
	}

	// Verify thinking block start
	var blockStart map[string]interface{}
	_ = json.Unmarshal([]byte(events[2].Data), &blockStart)
	delta := blockStart["delta"].(map[string]interface{})
	if delta["type"] != "thinking_delta" {
		t.Errorf("thinking delta type = %v", delta["type"])
	}
}

func TestClaudeStreamEncoder_ToolCalls(t *testing.T) {
	enc := NewStreamEncoder()

	events := enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaMessageStart{ID: "msg_3", Model: "claude-3-opus"},
		protocols.DeltaToolCallStart{Index: 0, ID: "toolu_abc", Name: "get_weather"},
		protocols.DeltaToolCallArgs{Index: 0, Arguments: `{"city":"Beijing"}`},
		protocols.DeltaDone{StopReason: "tool_calls"},
	})
	events = append(events, enc.EncodeDone()...)

	// Expected:
	// 1. message_start
	// 2. content_block_start (tool_use)
	// 3. content_block_delta (input_json_delta)
	// 4. content_block_stop
	// 5. message_delta (stop_reason = tool_use)
	// 6. message_stop
	if len(events) != 6 {
		t.Fatalf("events count = %d, want 6", len(events))
	}

	// Verify tool_use block start
	var blockStartData map[string]interface{}
	_ = json.Unmarshal([]byte(events[1].Data), &blockStartData)
	block := blockStartData["content_block"].(map[string]interface{})
	if block["type"] != "tool_use" {
		t.Errorf("block type = %v", block["type"])
	}
	if block["name"] != "get_weather" {
		t.Errorf("block name = %v", block["name"])
	}

	// Verify input_json_delta
	var argsDelta map[string]interface{}
	_ = json.Unmarshal([]byte(events[2].Data), &argsDelta)
	argsDeltaInner := argsDelta["delta"].(map[string]interface{})
	if argsDeltaInner["type"] != "input_json_delta" {
		t.Errorf("delta type = %v", argsDeltaInner["type"])
	}
	if argsDeltaInner["partial_json"] != `{"city":"Beijing"}` {
		t.Errorf("partial_json = %v", argsDeltaInner["partial_json"])
	}
}

func TestClaudeStreamEncoder_MultipleToolCalls(t *testing.T) {
	enc := NewStreamEncoder()

	events := enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaMessageStart{ID: "msg_4", Model: "claude-3-opus"},
		protocols.DeltaToolCallStart{Index: 0, ID: "toolu_1", Name: "func_a"},
		protocols.DeltaToolCallArgs{Index: 0, Arguments: `{"x":1}`},
		protocols.DeltaToolCallStart{Index: 1, ID: "toolu_2", Name: "func_b"},
		protocols.DeltaToolCallArgs{Index: 1, Arguments: `{"y":2}`},
		protocols.DeltaDone{StopReason: "tool_calls"},
	})
	events = append(events, enc.EncodeDone()...)

	// Expected:
	// 1. message_start
	// 2. content_block_start (tool_use #0)
	// 3. content_block_delta (input_json_delta)
	// 4. content_block_stop
	// 5. content_block_start (tool_use #1)
	// 6. content_block_delta (input_json_delta)
	// 7. content_block_stop
	// 8. message_delta
	// 9. message_stop
	if len(events) != 9 {
		t.Fatalf("events count = %d, want 9", len(events))
	}

	// First tool call at index 0
	var block0 map[string]interface{}
	_ = json.Unmarshal([]byte(events[1].Data), &block0)
	if block0["index"] != float64(0) {
		t.Errorf("first tool block index = %v", block0["index"])
	}

	// Second tool call at index 1
	var block1 map[string]interface{}
	_ = json.Unmarshal([]byte(events[4].Data), &block1)
	if block1["index"] != float64(1) {
		t.Errorf("second tool block index = %v", block1["index"])
	}
}

func TestClaudeStreamEncoder_UsageTracking(t *testing.T) {
	enc := NewStreamEncoder()

	enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaUsage{Usage: protocols.IRUsage{PromptTokens: 100, CompletionTokens: 50}},
	})

	usage := enc.Usage()
	if usage.PromptTokens != 100 {
		t.Errorf("PromptTokens = %d", usage.PromptTokens)
	}
	if usage.CompletionTokens != 50 {
		t.Errorf("CompletionTokens = %d", usage.CompletionTokens)
	}
}

// TestClaudeStreamEncoder_PartialUsageDoesNotOverwritePrompt covers the case where
// the upstream sends usage across multiple partial chunks (e.g. some Azure/LiteLLM
// setups on reasoning models split prompt usage and completion usage into two frames).
// If the encoder applies last-wins on the whole struct, a later completion-only chunk
// would overwrite the already-collected PromptTokens with 0, under-reporting both the
// client SSE and the billed input_tokens. Usage must therefore be merged field by field.
func TestClaudeStreamEncoder_PartialUsageDoesNotOverwritePrompt(t *testing.T) {
	enc := NewStreamEncoder()

	// First frame: complete prompt usage.
	enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaUsage{Usage: protocols.IRUsage{PromptTokens: 100, CompletionTokens: 0}},
	})
	// Second frame: completion only, prompt=0 (partial usage chunk shape).
	enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaUsage{Usage: protocols.IRUsage{PromptTokens: 0, CompletionTokens: 50}},
	})

	usage := enc.Usage()
	if usage.PromptTokens != 100 {
		t.Errorf("PromptTokens = %d, want 100 — a partial completion-only usage chunk must not overwrite the already-collected prompt", usage.PromptTokens)
	}
	if usage.CompletionTokens != 50 {
		t.Errorf("CompletionTokens = %d, want 50", usage.CompletionTokens)
	}
}

func TestClaudeStreamEncoder_UsageNotOverwrittenByZero(t *testing.T) {
	enc := NewStreamEncoder()

	enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaUsage{Usage: protocols.IRUsage{PromptTokens: 100, CompletionTokens: 50}},
	})
	enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaUsage{Usage: protocols.IRUsage{PromptTokens: 0, CompletionTokens: 0}},
	})

	usage := enc.Usage()
	if usage.PromptTokens != 100 {
		t.Errorf("PromptTokens should stay 100, got %d", usage.PromptTokens)
	}
}

func TestClaudeStreamEncoder_EncodeDoneNil(t *testing.T) {
	enc := NewStreamEncoder()
	events := enc.EncodeDone()
	if events != nil {
		t.Errorf("EncodeDone should return nil, got %v", events)
	}
}

// TestClaudeStreamEncoder_MessageDeltaIncludesOutputTokens covers a real incident:
// a chat upstream frequently emits finish_reason and usage in the same SSE frame, so
// the decoder emits deltas in the order [DeltaText, DeltaDone, DeltaUsage], with
// DeltaDone arriving before DeltaUsage. An older version emitted message_delta
// immediately when handling DeltaDone, at which point e.usage.CompletionTokens was
// still 0 and the client received usage:{output_tokens:0}. Deferring message_delta +
// message_stop to EncodeDone guarantees all preceding DeltaUsage has been applied so
// output_tokens carries the real value.
func TestClaudeStreamEncoder_MessageDeltaIncludesOutputTokens(t *testing.T) {
	enc := NewStreamEncoder()

	// Simulate the delta order a chat decoder returns when one frame carries both
	// finish_reason and usage.
	events := enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaMessageStart{ID: "msg_x", Model: "deepseek-v4-pro"},
		protocols.DeltaText{Text: "hi"},
		protocols.DeltaDone{StopReason: "stop"},
		protocols.DeltaUsage{Usage: protocols.IRUsage{PromptTokens: 8, CompletionTokens: 147, TotalTokens: 155}},
	})
	events = append(events, enc.EncodeDone()...)

	// Find the message_delta frame and assert output_tokens.
	var deltaData map[string]interface{}
	for _, e := range events {
		if e.Event == "message_delta" {
			_ = json.Unmarshal([]byte(e.Data), &deltaData)
			break
		}
	}
	if deltaData == nil {
		t.Fatal("EncodeDone must emit message_delta so the client receives stop_reason + output_tokens")
	}
	usage, ok := deltaData["usage"].(map[string]interface{})
	if !ok {
		t.Fatal("message_delta.usage must be present")
	}
	got, _ := usage["output_tokens"].(float64)
	if int(got) != 147 {
		t.Errorf("message_delta.usage.output_tokens = %v, want 147 — otherwise the client reads 0 tokens", got)
	}
}

// TestClaudeStreamEncoder_NoMessageStopWhenEncodeDoneSkipped covers the failure path:
// when finishErr != nil the caller does NOT call EncodeDone, ensuring message_delta /
// message_stop are not emitted either — so the client SDK treats the failed request as
// a truncated stream, consistent with a server-side 502 settlement.
func TestClaudeStreamEncoder_NoMessageStopWhenEncodeDoneSkipped(t *testing.T) {
	enc := NewStreamEncoder()

	events := enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaMessageStart{ID: "msg_fail", Model: "x"},
		protocols.DeltaText{Text: "partial"},
		protocols.DeltaDone{StopReason: "stop"},
	})
	// The caller does not call EncodeDone (the finishErr != nil path).
	for _, e := range events {
		if e.Event == "message_delta" || e.Event == "message_stop" {
			t.Fatalf("the failure path must not emit %s when EncodeDone is skipped, to avoid the client treating a failed request as a complete response", e.Event)
		}
	}
}

func TestClaudeStreamEncoder_UnknownDelta(t *testing.T) {
	enc := NewStreamEncoder()

	events := enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaUnknown{Raw: json.RawMessage(`{"custom":"data"}`)},
	})

	if len(events) != 1 {
		t.Fatalf("events count = %d, want 1", len(events))
	}
	if !strings.Contains(events[0].Data, `"custom"`) {
		t.Errorf("Unknown delta data = %s", events[0].Data)
	}
}

func TestClaudeStreamEncoder_StopReasonMappings(t *testing.T) {
	tests := []struct {
		stopReason string
		want       string
	}{
		{"stop", "end_turn"},
		{"tool_calls", "tool_use"},
		{"length", "max_tokens"},
	}
	for _, tt := range tests {
		enc := NewStreamEncoder()
		events := enc.EncodeDeltas([]protocols.IRStreamDelta{
			protocols.DeltaMessageStart{ID: "msg_sr", Model: "model"},
			protocols.DeltaText{Text: "ok"},
			protocols.DeltaDone{StopReason: tt.stopReason},
		})
		events = append(events, enc.EncodeDone()...)

		// Find message_delta event
		var deltaData map[string]interface{}
		for _, e := range events {
			if e.Event == "message_delta" {
				_ = json.Unmarshal([]byte(e.Data), &deltaData)
				break
			}
		}
		if deltaData == nil {
			t.Fatal("no message_delta event found")
		}
		delta := deltaData["delta"].(map[string]interface{})
		if delta["stop_reason"] != tt.want {
			t.Errorf("stop_reason %q → %v, want %v", tt.stopReason, delta["stop_reason"], tt.want)
		}
	}
}

// --- ResponseEncoder ---

func TestClaudeResponseEncoder_BasicText(t *testing.T) {
	resp := protocols.NewIRResponse("msg_1", "claude-3-opus")
	resp.Content = "Hello!"
	resp.StopReason = "stop"
	resp.Usage = protocols.IRUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}

	data := ResponseEncoder{}.EncodeResponse(resp)

	var result map[string]interface{}
	_ = json.Unmarshal(data, &result)

	if result["id"] != "msg_1" {
		t.Errorf("id = %v", result["id"])
	}
	if result["stop_reason"] != "end_turn" {
		t.Errorf("stop_reason = %v", result["stop_reason"])
	}

	content := result["content"].([]interface{})
	if len(content) != 1 {
		t.Fatalf("content blocks = %d, want 1", len(content))
	}
	block := content[0].(map[string]interface{})
	if block["type"] != "text" {
		t.Errorf("block type = %v", block["type"])
	}
	if block["text"] != "Hello!" {
		t.Errorf("block text = %v", block["text"])
	}

	usage := result["usage"].(map[string]interface{})
	if usage["input_tokens"] != float64(10) {
		t.Errorf("input_tokens = %v", usage["input_tokens"])
	}
	if usage["output_tokens"] != float64(5) {
		t.Errorf("output_tokens = %v", usage["output_tokens"])
	}
}

func TestClaudeResponseEncoder_ToolCalls(t *testing.T) {
	resp := protocols.NewIRResponse("msg_2", "claude-3-opus")
	resp.ToolCalls = []protocols.IRToolCall{
		{ID: "toolu_1", Name: "get_weather", Arguments: `{"city":"Beijing"}`},
	}
	resp.StopReason = "tool_calls"

	data := ResponseEncoder{}.EncodeResponse(resp)

	var result map[string]interface{}
	_ = json.Unmarshal(data, &result)

	if result["stop_reason"] != "tool_use" {
		t.Errorf("stop_reason = %v, want tool_use", result["stop_reason"])
	}

	content := result["content"].([]interface{})
	if len(content) != 1 {
		t.Fatalf("content blocks = %d, want 1", len(content))
	}
	block := content[0].(map[string]interface{})
	if block["type"] != "tool_use" {
		t.Errorf("block type = %v", block["type"])
	}
	if block["id"] != "toolu_1" {
		t.Errorf("block id = %v", block["id"])
	}
	if block["name"] != "get_weather" {
		t.Errorf("block name = %v", block["name"])
	}
	input := block["input"].(map[string]interface{})
	if input["city"] != "Beijing" {
		t.Errorf("input.city = %v", input["city"])
	}
}

func TestClaudeResponseEncoder_ThinkingAndText(t *testing.T) {
	resp := protocols.NewIRResponse("msg_3", "claude-3-5-sonnet")
	resp.ReasoningContent = "Let me reason..."
	resp.Content = "The answer is 42."
	resp.StopReason = "stop"

	data := ResponseEncoder{}.EncodeResponse(resp)

	var result map[string]interface{}
	_ = json.Unmarshal(data, &result)

	content := result["content"].([]interface{})
	if len(content) != 2 {
		t.Fatalf("content blocks = %d, want 2", len(content))
	}

	// First block: thinking
	thinkBlock := content[0].(map[string]interface{})
	if thinkBlock["type"] != "thinking" {
		t.Errorf("first block type = %v", thinkBlock["type"])
	}
	if thinkBlock["thinking"] != "Let me reason..." {
		t.Errorf("thinking = %v", thinkBlock["thinking"])
	}

	// Second block: text
	textBlock := content[1].(map[string]interface{})
	if textBlock["type"] != "text" {
		t.Errorf("second block type = %v", textBlock["type"])
	}
}

func TestClaudeResponseEncoder_CacheTokens(t *testing.T) {
	resp := protocols.NewIRResponse("msg_4", "claude-3-opus")
	resp.Content = "ok"
	resp.Usage = protocols.IRUsage{
		PromptTokens:     100,
		CompletionTokens: 10,
		CacheWriteTokens: 50,
		CacheReadTokens:  30,
	}

	data := ResponseEncoder{}.EncodeResponse(resp)

	var result map[string]interface{}
	_ = json.Unmarshal(data, &result)

	usage := result["usage"].(map[string]interface{})
	if usage["cache_creation_input_tokens"] != float64(50) {
		t.Errorf("cache_creation_input_tokens = %v", usage["cache_creation_input_tokens"])
	}
	if usage["cache_read_input_tokens"] != float64(30) {
		t.Errorf("cache_read_input_tokens = %v", usage["cache_read_input_tokens"])
	}
}

func TestClaudeResponseEncoder_NoCacheTokens(t *testing.T) {
	resp := protocols.NewIRResponse("msg_5", "claude-3-opus")
	resp.Content = "ok"
	resp.Usage = protocols.IRUsage{PromptTokens: 10, CompletionTokens: 5}

	data := ResponseEncoder{}.EncodeResponse(resp)

	var result map[string]interface{}
	_ = json.Unmarshal(data, &result)

	usage := result["usage"].(map[string]interface{})
	if _, has := usage["cache_creation_input_tokens"]; has {
		t.Error("cache_creation_input_tokens should not be set when 0")
	}
	if _, has := usage["cache_read_input_tokens"]; has {
		t.Error("cache_read_input_tokens should not be set when 0")
	}
}

func TestClaudeResponseEncoder_EmptyResponse(t *testing.T) {
	resp := protocols.NewIRResponse("msg_6", "claude-3-opus")
	resp.StopReason = "stop"

	data := ResponseEncoder{}.EncodeResponse(resp)

	var result map[string]interface{}
	_ = json.Unmarshal(data, &result)

	content := result["content"].([]interface{})
	if len(content) != 1 {
		t.Fatalf("content blocks = %d, want 1 (empty text fallback)", len(content))
	}
	block := content[0].(map[string]interface{})
	if block["type"] != "text" {
		t.Errorf("fallback block type = %v", block["type"])
	}
	if block["text"] != "" {
		t.Errorf("fallback block text = %v, want empty", block["text"])
	}
}

func TestClaudeResponseEncoder_ToolCallsWithEmptyArgs(t *testing.T) {
	resp := protocols.NewIRResponse("msg_7", "claude-3-opus")
	resp.ToolCalls = []protocols.IRToolCall{
		{ID: "toolu_x", Name: "list_files", Arguments: ""},
	}

	data := ResponseEncoder{}.EncodeResponse(resp)

	var result map[string]interface{}
	_ = json.Unmarshal(data, &result)

	content := result["content"].([]interface{})
	block := content[0].(map[string]interface{})
	input := block["input"].(map[string]interface{})
	if len(input) != 0 {
		t.Errorf("empty args should map to {}, got %v", input)
	}
}

// --- Round-trip: Claude Stream Decode → Encode ---

func TestRoundTrip_ClaudeStreamTextOnly(t *testing.T) {
	dec := NewStreamDecoder()
	enc := NewStreamEncoder()

	chunks := []string{
		`data: {"type":"message_start","message":{"id":"msg_rt","model":"claude-3-opus","usage":{"input_tokens":10,"output_tokens":0}}}`,
		`data: {"type":"content_block_start","index":0,"content_block":{"type":"text","text":""}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"Hello "}}`,
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"world!"}}`,
		`data: {"type":"content_block_stop","index":0}`,
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn","stop_sequence":null},"usage":{"output_tokens":5}}`,
		`data: {"type":"message_stop"}`,
	}

	var allEvents []protocols.SSEEvent
	for _, chunk := range chunks {
		deltas, _ := dec.DecodeChunk(chunk)
		events := enc.EncodeDeltas(deltas)
		allEvents = append(allEvents, events...)
	}
	// Append the fallback deltas from decoder.Finish (e.g. the DeltaDone+DeltaUsage of a
	// message_stop-only stream).
	finalDeltas, _ := dec.Finish()
	allEvents = append(allEvents, enc.EncodeDeltas(finalDeltas)...)
	allEvents = append(allEvents, enc.EncodeDone()...)

	// Should produce Claude SSE events
	foundStart, foundDelta, foundStop := false, false, false
	for _, e := range allEvents {
		switch e.Event {
		case "message_start":
			foundStart = true
		case "content_block_delta":
			if strings.Contains(e.Data, "Hello ") {
				foundDelta = true
			}
		case "message_stop":
			foundStop = true
		}
	}
	if !foundStart {
		t.Error("Missing message_start")
	}
	if !foundDelta {
		t.Error("Missing content_block_delta with 'Hello '")
	}
	if !foundStop {
		t.Error("Missing message_stop")
	}
}

func TestRoundTrip_ClaudeResponseEncodeDecode(t *testing.T) {
	// Encode an IR response to Claude format, then decode it back
	original := protocols.NewIRResponse("msg_round", "claude-3-opus")
	original.Content = "Test response"
	original.StopReason = "stop"
	original.Usage = protocols.IRUsage{PromptTokens: 20, CompletionTokens: 10, TotalTokens: 30}

	encoded := ResponseEncoder{}.EncodeResponse(original)
	decoded, err := ResponseDecoder{}.DecodeResponse(encoded)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}

	if decoded.Content != original.Content {
		t.Errorf("Content = %q, want %q", decoded.Content, original.Content)
	}
	if decoded.StopReason != original.StopReason {
		t.Errorf("StopReason = %q, want %q", decoded.StopReason, original.StopReason)
	}
	if decoded.Usage.PromptTokens != original.Usage.PromptTokens {
		t.Errorf("PromptTokens = %d, want %d", decoded.Usage.PromptTokens, original.Usage.PromptTokens)
	}
}

// TestClaudeStreamEncoder_MessageDeltaCarriesFullUsage is the regression guard
// for a real production defect: a Claude Code client relaying through an
// OpenAI-protocol upstream received message_start with input_tokens 0 and a
// message_delta carrying only output_tokens, so the client displayed zero input
// and no cache information even though the gateway had the correct counts.
//
// The numbers are the ones from that request. Under the OpenAI convention the
// upstream reports a gross prompt (2 net + 906 write + 36678 read = 37586) with
// the cache split alongside, so the encoder must convert back to Anthropic's
// net convention and emit the cache breakdown.
func TestClaudeStreamEncoder_MessageDeltaCarriesFullUsage(t *testing.T) {
	enc := NewStreamEncoder()

	events := enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaMessageStart{ID: "msg_usage", Model: "claude-opus-4-8"},
		protocols.DeltaText{Text: "hi"},
		// The OpenAI include_usage protocol delivers finish_reason and usage in
		// the same frame, decoded as DeltaDone followed by DeltaUsage.
		protocols.DeltaDone{StopReason: "stop"},
		protocols.DeltaUsage{Usage: protocols.IRUsage{
			PromptTokens:          37586,
			CompletionTokens:      123,
			CacheWriteTokens:      906,
			CacheReadTokens:       36678,
			CacheIncludedInPrompt: true,
		}},
	})
	events = append(events, enc.EncodeDone()...)

	var deltaEvent *protocols.SSEEvent
	for i := range events {
		if events[i].Event == "message_delta" {
			deltaEvent = &events[i]
		}
	}
	if deltaEvent == nil {
		t.Fatal("no message_delta emitted")
	}

	var payload map[string]interface{}
	if err := json.Unmarshal([]byte(deltaEvent.Data), &payload); err != nil {
		t.Fatalf("message_delta is not valid JSON: %v", err)
	}
	usage, ok := payload["usage"].(map[string]interface{})
	if !ok {
		t.Fatal("message_delta has no usage object")
	}

	for _, want := range []struct {
		field string
		value float64
	}{
		{"input_tokens", 2},
		{"output_tokens", 123},
		{"cache_creation_input_tokens", 906},
		{"cache_read_input_tokens", 36678},
	} {
		got, present := usage[want.field]
		if !present {
			t.Errorf("message_delta usage is missing %s — the client cannot recover it from any other event", want.field)
			continue
		}
		if got != want.value {
			t.Errorf("message_delta usage %s = %v, want %v", want.field, got, want.value)
		}
	}
}

// TestClaudeStreamEncoder_MessageDeltaOmitsZeroCacheFields keeps the cache
// members optional: a request that touched no cache must not advertise
// zero-valued cache fields, matching Anthropic's own omission behaviour.
func TestClaudeStreamEncoder_MessageDeltaOmitsZeroCacheFields(t *testing.T) {
	enc := NewStreamEncoder()

	events := enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaMessageStart{ID: "msg_nocache", Model: "claude-opus-4-8"},
		protocols.DeltaText{Text: "hi"},
		protocols.DeltaDone{StopReason: "stop"},
		protocols.DeltaUsage{Usage: protocols.IRUsage{
			PromptTokens:          40,
			CompletionTokens:      7,
			CacheIncludedInPrompt: true,
		}},
	})
	events = append(events, enc.EncodeDone()...)

	var deltaEvent *protocols.SSEEvent
	for i := range events {
		if events[i].Event == "message_delta" {
			deltaEvent = &events[i]
		}
	}
	if deltaEvent == nil {
		t.Fatal("no message_delta emitted")
	}

	var payload map[string]interface{}
	_ = json.Unmarshal([]byte(deltaEvent.Data), &payload)
	usage := payload["usage"].(map[string]interface{})

	if usage["input_tokens"] != float64(40) {
		t.Errorf("input_tokens = %v, want 40", usage["input_tokens"])
	}
	for _, field := range []string{"cache_creation_input_tokens", "cache_read_input_tokens"} {
		if _, present := usage[field]; present {
			t.Errorf("%s must be omitted when zero", field)
		}
	}
}
