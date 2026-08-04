package responses

import (
	"encoding/json"
	"github.com/yolorouter/yolorouter/internal/protocols"
	"github.com/yolorouter/yolorouter/internal/protocols/claude"
	"github.com/yolorouter/yolorouter/internal/protocols/gemini"
	"strings"
	"testing"
)

// --- RequestDecoder ---

func TestResponsesRequestDecoder_BasicInput(t *testing.T) {
	body := json.RawMessage(`{
		"model": "gpt-4",
		"input": "Hello, how are you?"
	}`)

	irReq, err := RequestDecoder{}.DecodeRequest(body, "gpt-4", false)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if irReq.Model != "gpt-4" {
		t.Errorf("Model = %q", irReq.Model)
	}
	if len(irReq.Messages) != 1 {
		t.Fatalf("Messages count = %d, want 1", len(irReq.Messages))
	}
	if irReq.Messages[0].Role != protocols.RoleUser {
		t.Errorf("Role = %v", irReq.Messages[0].Role)
	}
	if len(irReq.Messages[0].Content) != 1 {
		t.Fatalf("Content blocks = %d", len(irReq.Messages[0].Content))
	}
	txt, ok := irReq.Messages[0].Content[0].(protocols.BlockText)
	if !ok || txt.Text != "Hello, how are you?" {
		t.Errorf("Text = %v", irReq.Messages[0].Content[0])
	}
}

func TestResponsesRequestDecoder_InstructionsAndItems(t *testing.T) {
	body := json.RawMessage(`{
		"model": "claude-3-opus",
		"instructions": "You are helpful.",
		"input": [
			{"role": "user", "content": "Hi"},
			{"role": "assistant", "content": "Hello!"},
			{"role": "user", "content": "What is 2+2?"}
		]
	}`)

	irReq, err := RequestDecoder{}.DecodeRequest(body, "claude-3-opus", true)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if irReq.System != "You are helpful." {
		t.Errorf("System = %q", irReq.System)
	}
	if len(irReq.Messages) != 3 {
		t.Fatalf("Messages count = %d, want 3", len(irReq.Messages))
	}
	if !irReq.Stream.Enabled {
		t.Error("Stream should be enabled")
	}
}

func TestResponsesRequestDecoder_Tools(t *testing.T) {
	body := json.RawMessage(`{
		"model": "gpt-4",
		"input": "Weather?",
		"tools": [{"type": "function", "name": "get_weather", "description": "Get weather", "parameters": {"type": "object"}}],
		"tool_choice": "auto"
	}`)

	irReq, err := RequestDecoder{}.DecodeRequest(body, "gpt-4", false)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if len(irReq.Tools) != 1 {
		t.Fatalf("Tools count = %d, want 1", len(irReq.Tools))
	}
	if irReq.Tools[0].Name != "get_weather" {
		t.Errorf("Tool name = %q", irReq.Tools[0].Name)
	}
	if irReq.ToolChoice != protocols.ToolChoiceAuto {
		t.Errorf("ToolChoice = %v", irReq.ToolChoice)
	}
}

func TestResponsesRequestDecoder_Reasoning(t *testing.T) {
	body := json.RawMessage(`{
		"model": "o3",
		"input": "Think hard",
		"reasoning": {"effort": "high"}
	}`)

	irReq, err := RequestDecoder{}.DecodeRequest(body, "o3", false)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if !irReq.Reasoning.Enabled {
		t.Error("Reasoning should be enabled")
	}
	if irReq.Reasoning.Effort != "high" {
		t.Errorf("Effort = %q", irReq.Reasoning.Effort)
	}
}

func TestResponsesRequestDecoder_FunctionCall(t *testing.T) {
	body := json.RawMessage(`{
		"model": "gpt-4",
		"input": [
			{"role": "user", "content": "Weather?"},
			{"type": "function_call", "call_id": "call_abc", "name": "get_weather", "arguments": "{\"city\":\"Beijing\"}"},
			{"type": "function_call_output", "call_id": "call_abc", "output": "Sunny, 25C"}
		]
	}`)

	irReq, err := RequestDecoder{}.DecodeRequest(body, "gpt-4", false)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	// Should have: user, assistant (tool call), tool (result)
	if len(irReq.Messages) != 3 {
		t.Fatalf("Messages count = %d, want 3", len(irReq.Messages))
	}
	if irReq.Messages[1].Role != protocols.RoleAssistant {
		t.Errorf("Tool call msg role = %v", irReq.Messages[1].Role)
	}
	if len(irReq.Messages[1].ToolCalls) != 1 {
		t.Fatalf("ToolCalls count = %d", len(irReq.Messages[1].ToolCalls))
	}
	if irReq.Messages[1].ToolCalls[0].Name != "get_weather" {
		t.Errorf("ToolCall name = %q", irReq.Messages[1].ToolCalls[0].Name)
	}
	if irReq.Messages[2].Role != protocols.RoleTool {
		t.Errorf("Tool result role = %v", irReq.Messages[2].Role)
	}
	if irReq.Messages[2].ToolCallID != "call_abc" {
		t.Errorf("ToolCallID = %q", irReq.Messages[2].ToolCallID)
	}
}

func TestResponsesRequestDecoder_SystemItems(t *testing.T) {
	body := json.RawMessage(`{
		"model": "gpt-4",
		"instructions": "Be helpful.",
		"input": [
			{"role": "system", "content": "Always answer in Chinese."},
			{"role": "user", "content": "Hello"}
		]
	}`)

	irReq, err := RequestDecoder{}.DecodeRequest(body, "gpt-4", false)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	// System items should be prepended before instructions
	if !strings.Contains(irReq.System, "Always answer in Chinese.") {
		t.Errorf("System = %q", irReq.System)
	}
	if !strings.Contains(irReq.System, "Be helpful.") {
		t.Errorf("System = %q", irReq.System)
	}
}

func TestResponsesRequestDecoder_ToolChoiceNamed(t *testing.T) {
	body := json.RawMessage(`{
		"model": "gpt-4",
		"input": "Test",
		"tools": [{"type": "function", "name": "foo"}],
		"tool_choice": {"type": "function", "name": "foo"}
	}`)

	irReq, err := RequestDecoder{}.DecodeRequest(body, "gpt-4", false)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if irReq.ToolChoice != protocols.ToolChoiceNamed {
		t.Errorf("ToolChoice = %v", irReq.ToolChoice)
	}
	if irReq.ToolChoiceName != "foo" {
		t.Errorf("ToolChoiceName = %q", irReq.ToolChoiceName)
	}
}

func TestResponsesRequestDecoder_GenerationConfig(t *testing.T) {
	temp := 0.7
	topP := 0.9
	maxTokens := 1000
	body := json.RawMessage(`{
		"model": "gpt-4",
		"input": "Test",
		"temperature": 0.7,
		"top_p": 0.9,
		"max_output_tokens": 1000
	}`)

	irReq, err := RequestDecoder{}.DecodeRequest(body, "gpt-4", false)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if irReq.Generation.Temperature == nil || *irReq.Generation.Temperature != temp {
		t.Errorf("Temperature = %v", irReq.Generation.Temperature)
	}
	if irReq.Generation.TopP == nil || *irReq.Generation.TopP != topP {
		t.Errorf("TopP = %v", irReq.Generation.TopP)
	}
	if irReq.Generation.MaxTokens == nil || *irReq.Generation.MaxTokens != maxTokens {
		t.Errorf("MaxTokens = %v", irReq.Generation.MaxTokens)
	}
}

// --- ResponseEncoder ---

func TestResponsesResponseEncoder_TextOnly(t *testing.T) {
	resp := protocols.NewIRResponse("resp_123", "gpt-4")
	resp.Content = "Hello!"
	resp.StopReason = "stop"
	resp.Usage = protocols.IRUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}

	data := ResponseEncoder{}.EncodeResponse(resp)

	var result map[string]interface{}
	_ = json.Unmarshal(data, &result)

	if result["id"] != "resp_123" {
		t.Errorf("id = %v", result["id"])
	}
	if result["status"] != "completed" {
		t.Errorf("status = %v", result["status"])
	}

	outputs := result["output"].([]interface{})
	if len(outputs) != 1 {
		t.Fatalf("output count = %d, want 1", len(outputs))
	}
	msg := outputs[0].(map[string]interface{})
	if msg["type"] != "message" {
		t.Errorf("output type = %v", msg["type"])
	}

	usage := result["usage"].(map[string]interface{})
	if usage["input_tokens"] != float64(10) {
		t.Errorf("input_tokens = %v", usage["input_tokens"])
	}
}

func TestResponsesResponseEncoder_ToolCalls(t *testing.T) {
	resp := protocols.NewIRResponse("resp_456", "gpt-4")
	resp.ToolCalls = []protocols.IRToolCall{
		{ID: "call_abc", Name: "get_weather", Arguments: `{"city":"Beijing"}`},
	}
	resp.StopReason = "tool_calls"

	data := ResponseEncoder{}.EncodeResponse(resp)

	var result map[string]interface{}
	_ = json.Unmarshal(data, &result)

	outputs := result["output"].([]interface{})
	if len(outputs) != 1 {
		t.Fatalf("output count = %d", len(outputs))
	}
	fc := outputs[0].(map[string]interface{})
	if fc["type"] != "function_call" {
		t.Errorf("type = %v", fc["type"])
	}
	if fc["call_id"] != "call_abc" {
		t.Errorf("call_id = %v", fc["call_id"])
	}
	if fc["name"] != "get_weather" {
		t.Errorf("name = %v", fc["name"])
	}
}

func TestResponsesResponseEncoder_Reasoning(t *testing.T) {
	resp := protocols.NewIRResponse("resp_789", "o3")
	resp.ReasoningContent = "Let me think..."
	resp.Content = "The answer is 42."
	resp.StopReason = "stop"

	data := ResponseEncoder{}.EncodeResponse(resp)

	var result map[string]interface{}
	_ = json.Unmarshal(data, &result)

	outputs := result["output"].([]interface{})
	if len(outputs) != 2 {
		t.Fatalf("output count = %d, want 2", len(outputs))
	}

	reasoning := outputs[0].(map[string]interface{})
	if reasoning["type"] != "reasoning" {
		t.Errorf("first output type = %v", reasoning["type"])
	}
	summary := reasoning["summary"].([]interface{})
	summaryText := summary[0].(map[string]interface{})
	if summaryText["text"] != "Let me think..." {
		t.Errorf("reasoning text = %v", summaryText["text"])
	}

	msg := outputs[1].(map[string]interface{})
	if msg["type"] != "message" {
		t.Errorf("second output type = %v", msg["type"])
	}
}

func TestResponsesResponseEncoder_Incomplete(t *testing.T) {
	resp := protocols.NewIRResponse("resp_inc", "gpt-4")
	resp.Content = "truncated"
	resp.StopReason = "length"

	data := ResponseEncoder{}.EncodeResponse(resp)

	var result map[string]interface{}
	_ = json.Unmarshal(data, &result)

	if result["status"] != "incomplete" {
		t.Errorf("status = %v", result["status"])
	}
	details, ok := result["incomplete_details"].(map[string]interface{})
	if !ok || details["reason"] != "max_output_tokens" {
		t.Errorf("incomplete_details = %v", result["incomplete_details"])
	}
}

func TestResponsesResponseEncoder_CacheTokens(t *testing.T) {
	resp := protocols.NewIRResponse("resp_cache", "gpt-4")
	resp.Content = "ok"
	resp.Usage = protocols.IRUsage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110, CacheReadTokens: 80}

	data := ResponseEncoder{}.EncodeResponse(resp)

	var result map[string]interface{}
	_ = json.Unmarshal(data, &result)

	usage := result["usage"].(map[string]interface{})
	details, ok := usage["input_tokens_details"].(map[string]interface{})
	if !ok || details["cached_tokens"] != float64(80) {
		t.Errorf("input_tokens_details = %v", usage["input_tokens_details"])
	}
}

// --- StreamEncoder ---

func TestResponsesStreamEncoder_TextStream(t *testing.T) {
	enc := NewStreamEncoder()

	events := enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaMessageStart{ID: "resp_s1", Model: "gpt-4"},
		protocols.DeltaText{Text: "Hello "},
		protocols.DeltaText{Text: "world!"},
		protocols.DeltaDone{StopReason: "stop"},
	})
	// response.completed is deferred to EncodeDone so any DeltaUsage has a chance to apply before Done.
	events = append(events, enc.EncodeDone()...)

	// Expected events:
	// 1. response.created
	// 2. response.in_progress
	// 3. response.output_item.added (message)
	// 4. response.content_part.added
	// 5. response.output_text.delta ("Hello ")
	// 6. response.output_text.delta ("world!")
	// 7. response.output_text.done
	// 8. response.content_part.done
	// 9. response.output_item.done
	// 10. response.completed
	if len(events) != 10 {
		t.Fatalf("events count = %d, want 10", len(events))
	}

	// Check first event is response.created
	var first map[string]interface{}
	_ = json.Unmarshal([]byte(events[0].Data), &first)
	if first["type"] != "response.created" {
		t.Errorf("first event type = %v", first["type"])
	}

	// Check last event is response.completed
	var last map[string]interface{}
	_ = json.Unmarshal([]byte(events[len(events)-1].Data), &last)
	if last["type"] != "response.completed" {
		t.Errorf("last event type = %v", last["type"])
	}
}

// TestResponsesStreamEncoder_CompletedIncludesUsageEvenWhenUsageDeltaArrivesAfterDone
// covers the client-side token race: when a single upstream chat frame carries both
// finish_reason and usage, the decoder emits in the order [DeltaText, DeltaDone, DeltaUsage],
// with DeltaDone arriving before DeltaUsage. If response.completed were emitted immediately on
// DeltaDone, e.usage would still be 0 and the Codex client would read usage:{0,0,0}. By deferring
// response.completed to EncodeDone, all preceding DeltaUsage frames are guaranteed to be applied.
func TestResponsesStreamEncoder_CompletedIncludesUsageEvenWhenUsageDeltaArrivesAfterDone(t *testing.T) {
	enc := NewStreamEncoder()

	events := enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaMessageStart{ID: "resp_x", Model: "glm-5.1"},
		protocols.DeltaText{Text: "hi"},
		protocols.DeltaDone{StopReason: "stop"},
		protocols.DeltaUsage{Usage: protocols.IRUsage{PromptTokens: 14, CompletionTokens: 109, TotalTokens: 123}},
	})
	events = append(events, enc.EncodeDone()...)

	// Find the response.completed frame to assert its usage.
	var completedData map[string]interface{}
	for _, e := range events {
		var ev map[string]interface{}
		if json.Unmarshal([]byte(e.Data), &ev) == nil && ev["type"] == "response.completed" {
			completedData = ev
			break
		}
	}
	if completedData == nil {
		t.Fatal("EncodeDone must emit response.completed so the Codex client can read usage")
	}
	resp, ok := completedData["response"].(map[string]interface{})
	if !ok {
		t.Fatal("response.completed must contain a response field")
	}
	usage, ok := resp["usage"].(map[string]interface{})
	if !ok {
		t.Fatal("response.completed.response.usage must be present")
	}
	gotIn, _ := usage["input_tokens"].(float64)
	gotOut, _ := usage["output_tokens"].(float64)
	gotTotal, _ := usage["total_tokens"].(float64)
	if int(gotIn) != 14 || int(gotOut) != 109 || int(gotTotal) != 123 {
		t.Errorf("response.completed.usage = {in:%v out:%v total:%v}, want {14, 109, 123} — "+
			"otherwise the Codex client reads 0 tokens from the SSE stream", gotIn, gotOut, gotTotal)
	}
}

// TestResponsesStreamEncoder_NoCompletedWhenEncodeDoneSkipped covers the failure path:
// when finishErr != nil the caller does NOT call EncodeDone, ensuring response.completed is not
// emitted. This lets the client SDK treat the failed request as a truncated stream, consistent
// with the server settling it as a 502.
func TestResponsesStreamEncoder_NoCompletedWhenEncodeDoneSkipped(t *testing.T) {
	enc := NewStreamEncoder()

	events := enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaMessageStart{ID: "resp_fail", Model: "x"},
		protocols.DeltaText{Text: "partial"},
		protocols.DeltaDone{StopReason: "stop"},
	})
	for _, e := range events {
		var ev map[string]interface{}
		if json.Unmarshal([]byte(e.Data), &ev) == nil && ev["type"] == "response.completed" {
			t.Fatal("on the failure path (EncodeDone not called), response.completed must not appear, so the client does not treat a failed request as a complete response")
		}
	}
}

func TestResponsesStreamEncoder_ThinkingThenText(t *testing.T) {
	enc := NewStreamEncoder()

	events := enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaMessageStart{ID: "resp_s2", Model: "o3"},
		protocols.DeltaThinking{Text: "Reasoning..."},
		protocols.DeltaText{Text: "Answer."},
		protocols.DeltaDone{StopReason: "stop"},
	})

	// Find reasoning_summary_text.delta event
	foundReasoning := false
	for _, e := range events {
		if strings.Contains(e.Data, "reasoning_summary_text.delta") {
			foundReasoning = true
			break
		}
	}
	if !foundReasoning {
		t.Error("Missing reasoning_summary_text.delta event")
	}

	// Find output_text.delta event
	foundText := false
	for _, e := range events {
		if strings.Contains(e.Data, "output_text.delta") {
			foundText = true
			break
		}
	}
	if !foundText {
		t.Error("Missing output_text.delta event")
	}
}

func TestResponsesStreamEncoder_ToolCalls(t *testing.T) {
	enc := NewStreamEncoder()

	events := enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaMessageStart{ID: "resp_s3", Model: "gpt-4"},
		protocols.DeltaToolCallStart{Index: 0, ID: "call_abc", Name: "get_weather"},
		protocols.DeltaToolCallArgs{Index: 0, Arguments: `{"city":"Beijing"}`},
		protocols.DeltaDone{StopReason: "tool_calls"},
	})

	// Check for function_call events
	foundItemAdded := false
	foundArgsDelta := false
	for _, e := range events {
		if strings.Contains(e.Data, "function_call") && strings.Contains(e.Data, "response.output_item.added") {
			foundItemAdded = true
		}
		if strings.Contains(e.Data, "function_call_arguments.delta") {
			foundArgsDelta = true
		}
	}
	if !foundItemAdded {
		t.Error("Missing function_call output_item.added event")
	}
	if !foundArgsDelta {
		t.Error("Missing function_call_arguments.delta event")
	}
}

func TestResponsesStreamEncoder_UsageTracking(t *testing.T) {
	enc := NewStreamEncoder()

	enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaUsage{Usage: protocols.IRUsage{PromptTokens: 100, CompletionTokens: 50}},
	})

	usage := enc.Usage()
	if usage.PromptTokens != 100 {
		t.Errorf("PromptTokens = %d", usage.PromptTokens)
	}
}

// TestResponsesStreamEncoder_PartialUsageDoesNotOverwritePrompt covers partial usage chunks:
// the upstream may send usage across multiple chunks, so a whole-struct last-wins merge would let
// an already-collected PromptTokens be overwritten to 0 by a later completion-only chunk. The merge
// must instead be applied field by field.
func TestResponsesStreamEncoder_PartialUsageDoesNotOverwritePrompt(t *testing.T) {
	enc := NewStreamEncoder()

	// First frame: complete prompt usage.
	enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaUsage{Usage: protocols.IRUsage{PromptTokens: 100, CompletionTokens: 0}},
	})
	// Second frame: completion only, prompt=0.
	enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaUsage{Usage: protocols.IRUsage{PromptTokens: 0, CompletionTokens: 50}},
	})

	usage := enc.Usage()
	if usage.PromptTokens != 100 {
		t.Errorf("PromptTokens = %d, want 100 — a completion-only partial usage chunk must not overwrite the already-collected prompt", usage.PromptTokens)
	}
	if usage.CompletionTokens != 50 {
		t.Errorf("CompletionTokens = %d, want 50", usage.CompletionTokens)
	}
}

// --- Round-trip: Responses decode → IR → Claude encode ---

func TestRoundTrip_ResponsesToClaude(t *testing.T) {
	body := json.RawMessage(`{
		"model": "claude-3-opus",
		"instructions": "Be helpful.",
		"input": [
			{"role": "user", "content": "Hello"},
			{"role": "assistant", "content": "Hi!"},
			{"role": "user", "content": "How are you?"}
		],
		"tools": [{"type": "function", "name": "test_fn", "parameters": {"type": "object"}}],
		"temperature": 0.7
	}`)

	// Decode Responses → IR
	irReq, err := RequestDecoder{}.DecodeRequest(body, "claude-3-opus", false)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}

	// Encode IR → Claude
	claudeBody, err := claude.RequestEncoder{}.EncodeRequest(irReq)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}

	var claudeReq map[string]interface{}
	_ = json.Unmarshal(claudeBody, &claudeReq)

	if claudeReq["model"] != "claude-3-opus" {
		t.Errorf("model = %v", claudeReq["model"])
	}
	if claudeReq["system"] != "Be helpful." {
		t.Errorf("system = %v", claudeReq["system"])
	}
	msgs := claudeReq["messages"].([]interface{})
	if len(msgs) != 3 { // user + assistant + user
		t.Errorf("messages count = %d", len(msgs))
	}
	tools := claudeReq["tools"].([]interface{})
	if len(tools) != 1 {
		t.Errorf("tools count = %d", len(tools))
	}
}

// --- Round-trip: Responses decode → IR → Gemini encode ---

func TestRoundTrip_ResponsesToGemini(t *testing.T) {
	body := json.RawMessage(`{
		"model": "gemini-2.0-flash",
		"instructions": "Be helpful.",
		"input": "Hello",
		"temperature": 0.5
	}`)

	irReq, err := RequestDecoder{}.DecodeRequest(body, "gemini-2.0-flash", false)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}

	geminiBody, err := gemini.RequestEncoder{}.EncodeRequest(irReq)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}

	var geminiReq map[string]interface{}
	_ = json.Unmarshal(geminiBody, &geminiReq)

	if geminiReq["systemInstruction"] == nil {
		t.Error("Missing systemInstruction")
	}
	contents := geminiReq["contents"].([]interface{})
	if len(contents) != 1 {
		t.Errorf("contents count = %d", len(contents))
	}
}

// --- Round-trip: Claude response → IR → Responses response ---

func TestRoundTrip_ClaudeResponseToResponses(t *testing.T) {
	claudeResp := json.RawMessage(`{
		"id": "msg_rt",
		"model": "claude-3-opus",
		"stop_reason": "end_turn",
		"content": [
			{"type": "thinking", "thinking": "Let me reason..."},
			{"type": "text", "text": "The answer is 42."}
		],
		"usage": {"input_tokens": 100, "output_tokens": 50}
	}`)

	// Claude → IR
	irResp, err := claude.ResponseDecoder{}.DecodeResponse(claudeResp)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}

	// IR → Responses
	responsesData := ResponseEncoder{}.EncodeResponse(irResp)

	var result map[string]interface{}
	_ = json.Unmarshal(responsesData, &result)

	if result["status"] != "completed" {
		t.Errorf("status = %v", result["status"])
	}

	outputs := result["output"].([]interface{})
	if len(outputs) != 2 {
		t.Fatalf("output count = %d, want 2 (reasoning + message)", len(outputs))
	}

	// First output: reasoning
	reasoning := outputs[0].(map[string]interface{})
	if reasoning["type"] != "reasoning" {
		t.Errorf("first output type = %v", reasoning["type"])
	}

	// Second output: message
	msg := outputs[1].(map[string]interface{})
	if msg["type"] != "message" {
		t.Errorf("second output type = %v", msg["type"])
	}
}

// TestResponsesWireUsage_AnthropicUpstreamEmitsGross guards the cross-protocol
// combination that used to under-report: the Responses API defines
// input_tokens_details.cached_tokens as a breakdown OF input_tokens, so an
// Anthropic upstream's NET input must be converted before being emitted.
func TestResponsesWireUsage_AnthropicUpstreamEmitsGross(t *testing.T) {
	usage := responsesWireUsage(protocols.IRUsage{
		PromptTokens:     2,
		CompletionTokens: 123,
		CacheWriteTokens: 906,
		CacheReadTokens:  36678,
	})

	if usage["input_tokens"] != 37586 {
		t.Errorf("input_tokens = %v, want 37586 (net 2 + write 906 + read 36678)", usage["input_tokens"])
	}
	if usage["total_tokens"] != 37709 {
		t.Errorf("total_tokens = %v, want 37709", usage["total_tokens"])
	}
	details := usage["input_tokens_details"].(map[string]interface{})
	if details["cached_tokens"] != 36678 {
		t.Errorf("cached_tokens = %v, want 36678", details["cached_tokens"])
	}
}

// TestResponsesDecoder_NegativeCacheWriteSurvivesForRejection guards the
// coherence contract: token counts arrive as signed JSON from third-party
// upstreams, and the gateway's usageIsCoherent is what turns an impossible
// record into "unknown, not billed".
//
// That only works if the impossible value actually reaches it. Gating the
// assignment on `> 0` made a negative cache-write vanish here, leaving a record
// that looks sound and gets billed. Both decoders must therefore copy the value
// through verbatim and let the coherence check reject it.
func TestResponsesDecoder_NegativeCacheWriteSurvivesForRejection(t *testing.T) {
	t.Run("non-streaming", func(t *testing.T) {
		body := json.RawMessage(`{
			"id": "resp_neg", "model": "gpt-x", "status": "completed", "output": [],
			"usage": {"input_tokens": 100, "output_tokens": 5, "total_tokens": 105,
			          "cache_creation_input_tokens": -50}
		}`)

		resp, err := ResponseDecoder{}.DecodeResponse(body)
		if err != nil {
			t.Fatalf("decode: %v", err)
		}
		if resp.Usage.CacheWriteTokens != -50 {
			t.Errorf("CacheWriteTokens = %d, want -50 preserved so the coherence check can reject the record",
				resp.Usage.CacheWriteTokens)
		}
	})

	t.Run("streaming", func(t *testing.T) {
		d := NewStreamDecoder()
		d.collectUsage(&responsesUsage{
			InputTokens:              100,
			OutputTokens:             5,
			TotalTokens:              105,
			CacheCreationInputTokens: -50,
		})
		// The streaming path folds the frame through IRUsage.Merge, which copies
		// only values greater than zero — so the negative is erased from the
		// field, but Merge has already marked the accumulated record Invalid at
		// its entry (IsIncoherent runs before the copy). The verdict survives;
		// the raw value does not need to.
		if !d.usage.Invalid {
			t.Errorf("usage must be marked Invalid so the coherence check can reject the record, got %+v", d.usage)
		}
	})
}

// TestResponsesWireUsage_RequiredDetailMembersAlwaysPresent pins the required
// wire shape: ResponseUsage.input_tokens_details always contains cached_tokens
// and cache_write_tokens. Both must appear even when zero, or a
// strict-validating downstream rejects the response.
func TestResponsesWireUsage_RequiredDetailMembersAlwaysPresent(t *testing.T) {
	usage := responsesWireUsage(protocols.IRUsage{PromptTokens: 10, CompletionTokens: 2})

	details, ok := usage["input_tokens_details"].(map[string]interface{})
	if !ok {
		t.Fatal("input_tokens_details is required and must always be present")
	}
	for _, k := range []string{"cached_tokens", "cache_write_tokens"} {
		if _, present := details[k]; !present {
			t.Errorf("%s is in the schema's required list and must be emitted even when zero", k)
		}
	}
}
