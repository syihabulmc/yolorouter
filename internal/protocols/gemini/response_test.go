package gemini

import (
	"encoding/json"
	"github.com/yolorouter/yolorouter/internal/protocols"
	"github.com/yolorouter/yolorouter/internal/protocols/chat"
	"strings"
	"testing"
)

// --- Gemini Response Encoder ---

func TestGeminiResponseEncoder_Basic(t *testing.T) {
	resp := protocols.NewIRResponse("resp-1", "deepseek-chat")
	resp.Content = "Hello!"
	resp.StopReason = "stop"
	resp.Usage = protocols.IRUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}

	data := ResponseEncoder{}.EncodeResponse(resp)

	var result map[string]interface{}
	_ = json.Unmarshal(data, &result)

	candidates := result["candidates"].([]interface{})
	if len(candidates) == 0 {
		t.Fatal("no candidates")
	}
	cand := candidates[0].(map[string]interface{})
	content := cand["content"].(map[string]interface{})
	parts := content["parts"].([]interface{})
	if len(parts) == 0 {
		t.Fatal("no parts")
	}
	part := parts[0].(map[string]interface{})
	if part["text"] != "Hello!" {
		t.Errorf("text = %v", part["text"])
	}
	if cand["finishReason"] != "STOP" {
		t.Errorf("finishReason = %v", cand["finishReason"])
	}

	usage := result["usageMetadata"].(map[string]interface{})
	if usage["promptTokenCount"] != float64(10) {
		t.Errorf("promptTokenCount = %v", usage["promptTokenCount"])
	}
}

func TestGeminiResponseEncoder_WithThinking(t *testing.T) {
	resp := protocols.NewIRResponse("resp-1", "model")
	resp.ReasoningContent = "Thinking..."
	resp.Content = "Answer"
	resp.StopReason = "stop"

	data := ResponseEncoder{}.EncodeResponse(resp)

	var result map[string]interface{}
	_ = json.Unmarshal(data, &result)

	parts := result["candidates"].([]interface{})[0].(map[string]interface{})["content"].(map[string]interface{})["parts"].([]interface{})
	if len(parts) != 2 {
		t.Fatalf("parts count = %d, want 2", len(parts))
	}

	// First part: thinking
	thinkPart := parts[0].(map[string]interface{})
	if thinkPart["text"] != "Thinking..." {
		t.Errorf("thinking text = %v", thinkPart["text"])
	}
	if thinkPart["thought"] != true {
		t.Error("thought should be true")
	}

	// Second part: answer
	answerPart := parts[1].(map[string]interface{})
	if answerPart["text"] != "Answer" {
		t.Errorf("answer text = %v", answerPart["text"])
	}
}

func TestGeminiResponseEncoder_ToolCalls(t *testing.T) {
	resp := protocols.NewIRResponse("resp-1", "model")
	resp.ToolCalls = []protocols.IRToolCall{
		{ID: "call_1", Name: "get_weather", Arguments: `{"city":"Beijing"}`},
	}
	resp.StopReason = "tool_calls"

	data := ResponseEncoder{}.EncodeResponse(resp)

	var result map[string]interface{}
	_ = json.Unmarshal(data, &result)

	parts := result["candidates"].([]interface{})[0].(map[string]interface{})["content"].(map[string]interface{})["parts"].([]interface{})
	if len(parts) != 1 {
		t.Fatalf("parts count = %d, want 1", len(parts))
	}

	fc := parts[0].(map[string]interface{})["functionCall"].(map[string]interface{})
	if fc["name"] != "get_weather" {
		t.Errorf("functionCall name = %v", fc["name"])
	}
	args := fc["args"].(map[string]interface{})
	if args["city"] != "Beijing" {
		t.Errorf("args.city = %v", args["city"])
	}
}

func TestGeminiResponseEncoder_CacheTokens(t *testing.T) {
	resp := protocols.NewIRResponse("resp-1", "model")
	resp.Content = "ok"
	resp.Usage = protocols.IRUsage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 150, CacheReadTokens: 80}

	data := ResponseEncoder{}.EncodeResponse(resp)

	var result map[string]interface{}
	_ = json.Unmarshal(data, &result)

	usage := result["usageMetadata"].(map[string]interface{})
	if usage["cachedContentTokenCount"] != float64(80) {
		t.Errorf("cachedContentTokenCount = %v", usage["cachedContentTokenCount"])
	}
}

func TestGeminiResponseEncoder_EmptyResponse(t *testing.T) {
	resp := protocols.NewIRResponse("resp-1", "model")
	resp.StopReason = "stop"

	data := ResponseEncoder{}.EncodeResponse(resp)

	var result map[string]interface{}
	_ = json.Unmarshal(data, &result)

	parts := result["candidates"].([]interface{})[0].(map[string]interface{})["content"].(map[string]interface{})["parts"].([]interface{})
	if len(parts) != 1 {
		t.Fatalf("parts count = %d, want 1 (empty text fallback)", len(parts))
	}
}

// --- Gemini Stream Encoder ---

func TestGeminiStreamEncoder_TextDeltas(t *testing.T) {
	enc := NewStreamEncoder()

	deltas := []protocols.IRStreamDelta{
		protocols.DeltaMessageStart{ID: "resp-1", Model: "deepseek-chat"},
		protocols.DeltaText{Text: "Hello "},
		protocols.DeltaText{Text: "world!"},
	}

	events := enc.EncodeDeltas(deltas)
	// MessageStart doesn't produce events, only text deltas
	if len(events) != 2 {
		t.Fatalf("events count = %d, want 2", len(events))
	}

	for i, event := range events {
		if !strings.Contains(event.Data, "Hello ") && !strings.Contains(event.Data, "world!") {
			t.Errorf("event[%d] data = %s", i, event.Data)
		}
	}
}

func TestGeminiStreamEncoder_ThinkingDeltas(t *testing.T) {
	enc := NewStreamEncoder()

	deltas := []protocols.IRStreamDelta{
		protocols.DeltaThinking{Text: "Let me think..."},
		protocols.DeltaText{Text: "Hello"},
	}

	events := enc.EncodeDeltas(deltas)
	if len(events) != 2 {
		t.Fatalf("events count = %d, want 2", len(events))
	}

	// Regression: thinking content must carry the thought:true marker, otherwise
	// client SDKs display the reasoning summary as regular output (matches the
	// behavior of Codex's response.reasoning_summary_text.delta event).
	if !strings.Contains(events[0].Data, "Let me think...") {
		t.Errorf("thinking event missing text content: %s", events[0].Data)
	}
	if !strings.Contains(events[0].Data, `"thought":true`) {
		t.Errorf("thinking event must carry the thought:true marker to avoid being shown as regular text by the client, actual: %s", events[0].Data)
	}

	// The regular text part must not carry thought:true.
	if !strings.Contains(events[1].Data, "Hello") {
		t.Errorf("text event missing content: %s", events[1].Data)
	}
	if strings.Contains(events[1].Data, `"thought":true`) {
		t.Errorf("regular text part must not carry thought:true: %s", events[1].Data)
	}
}

func TestGeminiStreamEncoder_ToolCallAccumulation(t *testing.T) {
	enc := NewStreamEncoder()

	deltas := []protocols.IRStreamDelta{
		protocols.DeltaToolCallStart{Index: 0, ID: "call_1", Name: "get_weather"},
		protocols.DeltaToolCallArgs{Index: 0, Arguments: `{"ci`},
		protocols.DeltaToolCallArgs{Index: 0, Arguments: `ty":"Beijing"}`},
	}

	events := enc.EncodeDeltas(deltas)
	// Only the last arg chunk completes the JSON, so should emit tool call event
	if len(events) == 0 {
		t.Fatal("Expected at least one event from tool call accumulation")
	}

	lastEvent := events[len(events)-1]
	if !strings.Contains(lastEvent.Data, "get_weather") {
		t.Errorf("tool call event data = %s", lastEvent.Data)
	}
}

func TestGeminiStreamEncoder_UsageDelta(t *testing.T) {
	enc := NewStreamEncoder()

	enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaUsage{Usage: protocols.IRUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}},
	})

	usage := enc.Usage()
	if usage.PromptTokens != 10 {
		t.Errorf("PromptTokens = %d", usage.PromptTokens)
	}
}

// TestGeminiStreamEncoder_PartialUsageDoesNotOverwritePrompt matches the claude
// / responses encoder behavior: when the upstream splits a usage chunk across
// two frames, the Gemini encoder must not use whole-struct last-wins semantics
// to overwrite PromptTokens that was already collected.
func TestGeminiStreamEncoder_PartialUsageDoesNotOverwritePrompt(t *testing.T) {
	enc := NewStreamEncoder()

	enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaUsage{Usage: protocols.IRUsage{PromptTokens: 100, CacheReadTokens: 30, CacheIncludedInPrompt: true}},
	})
	enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaUsage{Usage: protocols.IRUsage{CompletionTokens: 50}},
	})

	usage := enc.Usage()
	if usage.PromptTokens != 100 {
		t.Errorf("PromptTokens = %d, want 100 — a partial completion-only usage chunk must not overwrite it", usage.PromptTokens)
	}
	if usage.CompletionTokens != 50 {
		t.Errorf("CompletionTokens = %d, want 50", usage.CompletionTokens)
	}
	if usage.CacheReadTokens != 30 {
		t.Errorf("CacheReadTokens = %d, want 30 (field-level merge protection)", usage.CacheReadTokens)
	}
	if !usage.CacheIncludedInPrompt {
		t.Error("CacheIncludedInPrompt must remain true (locks the pricing basis)")
	}
}

func TestGeminiStreamEncoder_DoneDelta(t *testing.T) {
	enc := NewStreamEncoder()
	enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaUsage{Usage: protocols.IRUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}},
	})

	// protocols.DeltaDone no longer emits immediately; finish chunk is deferred to EncodeDone
	// so that usage arriving after protocols.DeltaDone (OpenAI stream ordering) is included.
	intermediate := enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaDone{StopReason: "stop"},
	})
	if len(intermediate) != 0 {
		t.Fatalf("EncodeDeltas(protocols.DeltaDone) should return 0 events, got %d", len(intermediate))
	}

	events := enc.EncodeDone()
	if len(events) != 1 {
		t.Fatalf("EncodeDone events count = %d, want 1", len(events))
	}
	if !strings.Contains(events[0].Data, "STOP") {
		t.Errorf("finish chunk = %s", events[0].Data)
	}
	if !strings.Contains(events[0].Data, "usageMetadata") {
		t.Error("finish chunk missing usageMetadata")
	}
}

// --- Gemini Stream Decoder ---

func TestGeminiStreamDecoder_Basic(t *testing.T) {
	dec := NewStreamDecoder()

	chunk := "data: {\"candidates\":[{\"content\":{\"role\":\"model\",\"parts\":[{\"text\":\"Hello!\"}]}}],\"modelVersion\":\"deepseek-chat\"}\n\n"

	deltas, err := dec.DecodeChunk(chunk)
	if err != nil {
		t.Fatalf("DecodeChunk: %v", err)
	}

	hasStart, hasText := false, false
	for _, d := range deltas {
		switch v := d.(type) {
		case protocols.DeltaMessageStart:
			hasStart = true
			if v.Model != "deepseek-chat" {
				t.Errorf("Model = %q", v.Model)
			}
		case protocols.DeltaText:
			hasText = true
			if v.Text != "Hello!" {
				t.Errorf("Text = %q", v.Text)
			}
		}
	}
	if !hasStart {
		t.Error("Missing protocols.DeltaMessageStart")
	}
	if !hasText {
		t.Error("Missing protocols.DeltaText")
	}
}

func TestGeminiStreamDecoder_ThinkingParts(t *testing.T) {
	dec := NewStreamDecoder()

	chunk := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"Thinking...\",\"thought\":true}]}}]}\n\n"

	deltas, _ := dec.DecodeChunk(chunk)

	foundThinking := false
	for _, d := range deltas {
		if _, ok := d.(protocols.DeltaThinking); ok {
			foundThinking = true
		}
	}
	if !foundThinking {
		t.Error("Missing protocols.DeltaThinking for thought=true part")
	}
}

func TestGeminiStreamDecoder_FinishReason(t *testing.T) {
	dec := NewStreamDecoder()

	chunk := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"ok\"}]},\"finishReason\":\"MAX_TOKENS\"}]}\n\n"

	deltas, _ := dec.DecodeChunk(chunk)

	foundDone := false
	for _, d := range deltas {
		if done, ok := d.(protocols.DeltaDone); ok {
			foundDone = true
			if done.StopReason != "length" {
				t.Errorf("StopReason = %q, want %q", done.StopReason, "length")
			}
		}
	}
	if !foundDone {
		t.Error("Missing protocols.DeltaDone for finishReason")
	}
}

func TestGeminiStreamDecoder_Usage(t *testing.T) {
	dec := NewStreamDecoder()

	chunk := "data: {\"usageMetadata\":{\"promptTokenCount\":10,\"candidatesTokenCount\":5,\"totalTokenCount\":15},\"candidates\":[]}\n\n"

	deltas, _ := dec.DecodeChunk(chunk)

	foundUsage := false
	for _, d := range deltas {
		if u, ok := d.(protocols.DeltaUsage); ok {
			foundUsage = true
			if u.Usage.PromptTokens != 10 {
				t.Errorf("PromptTokens = %d", u.Usage.PromptTokens)
			}
		}
	}
	if !foundUsage {
		t.Error("Missing protocols.DeltaUsage")
	}
}

func TestGeminiStreamDecoder_FunctionCall(t *testing.T) {
	dec := NewStreamDecoder()

	chunk := "data: {\"candidates\":[{\"content\":{\"parts\":[{\"functionCall\":{\"name\":\"get_weather\",\"args\":{\"city\":\"Beijing\"}}}]}}]}\n\n"

	deltas, _ := dec.DecodeChunk(chunk)

	var foundStart, foundArgs bool
	for _, d := range deltas {
		switch v := d.(type) {
		case protocols.DeltaToolCallStart:
			foundStart = true
			if v.Name != "get_weather" {
				t.Errorf("Name = %q", v.Name)
			}
		case protocols.DeltaToolCallArgs:
			foundArgs = true
			if !strings.Contains(v.Arguments, "Beijing") {
				t.Errorf("Arguments = %q", v.Arguments)
			}
		}
	}
	if !foundStart {
		t.Error("Missing protocols.DeltaToolCallStart")
	}
	if !foundArgs {
		t.Error("Missing protocols.DeltaToolCallArgs")
	}
}

// --- Round-trip: Gemini → IR → OpenAI → IR → Gemini ---

func TestRoundTrip_BasicChat(t *testing.T) {
	geminiReq := json.RawMessage(`{
		"systemInstruction": {"parts": [{"text": "You are helpful."}]},
		"contents": [
			{"role": "user", "parts": [{"text": "Hello"}]},
			{"role": "model", "parts": [{"text": "Hi!"}]},
			{"role": "user", "parts": [{"text": "How are you?"}]}
		],
		"generationConfig": {"temperature": 0.7, "maxOutputTokens": 1000}
	}`)

	// Step 1: Decode Gemini → IR
	irReq, err := RequestDecoder{}.DecodeRequest(geminiReq, "deepseek-chat", false)
	if err != nil {
		t.Fatalf("Step 1 (Gemini decode): %v", err)
	}

	if irReq.System != "You are helpful." {
		t.Errorf("System = %q", irReq.System)
	}
	if len(irReq.Messages) != 3 {
		t.Fatalf("Messages = %d, want 3", len(irReq.Messages))
	}

	// Step 2: Encode IR → OpenAI
	openaiBody, err := chat.RequestEncoder{}.EncodeRequest(irReq)
	if err != nil {
		t.Fatalf("Step 2 (OpenAI encode): %v", err)
	}

	var openaiReq map[string]interface{}
	_ = json.Unmarshal(openaiBody, &openaiReq)
	msgs := openaiReq["messages"].([]interface{})
	if len(msgs) != 4 { // system + 3
		t.Errorf("OpenAI messages = %d, want 4", len(msgs))
	}

	// Step 3: Simulate OpenAI response → IR
	openaiResp := json.RawMessage(`{
		"id": "chatcmpl-test",
		"model": "deepseek-chat",
		"choices": [{"message": {"role": "assistant", "content": "I'm doing great!"}, "finish_reason": "stop"}],
		"usage": {"prompt_tokens": 20, "completion_tokens": 10, "total_tokens": 30}
	}`)

	irResp, err := chat.ResponseDecoder{}.DecodeResponse(openaiResp)
	if err != nil {
		t.Fatalf("Step 3 (OpenAI decode): %v", err)
	}

	// Step 4: Encode IR → Gemini response
	geminiRespData := ResponseEncoder{}.EncodeResponse(irResp)

	var geminiResp map[string]interface{}
	_ = json.Unmarshal(geminiRespData, &geminiResp)

	parts := geminiResp["candidates"].([]interface{})[0].(map[string]interface{})["content"].(map[string]interface{})["parts"].([]interface{})
	text := parts[0].(map[string]interface{})["text"].(string)
	if text != "I'm doing great!" {
		t.Errorf("Final Gemini response text = %q", text)
	}
}

func TestRoundTrip_ToolCalls(t *testing.T) {
	geminiReq := json.RawMessage(`{
		"contents": [
			{"role": "user", "parts": [{"text": "Weather in Beijing?"}]},
			{"role": "model", "parts": [{"functionCall": {"name": "get_weather", "args": {"city": "Beijing"}}}]},
			{"role": "function", "parts": [{"functionResponse": {"name": "get_weather", "response": {"temp": 25}}}]},
			{"role": "model", "parts": [{"text": "It's 25°C in Beijing."}]}
		],
		"tools": [{"functionDeclarations": [{"name": "get_weather", "description": "Get weather", "parameters": {"type": "object"}}]}]
	}`)

	// Gemini → IR
	irReq, err := RequestDecoder{}.DecodeRequest(geminiReq, "gpt-4", false)
	if err != nil {
		t.Fatalf("Gemini decode: %v", err)
	}

	// Verify tool result has proper tool_call_id (not degraded to text)
	toolMsg := irReq.Messages[2]
	if toolMsg.Role != protocols.RoleTool {
		t.Fatalf("Function response role = %v, want protocols.RoleTool", toolMsg.Role)
	}
	foundResult := false
	for _, b := range toolMsg.Content {
		if tr, ok := b.(protocols.BlockToolResult); ok {
			foundResult = true
			if tr.ToolUseID == "" {
				t.Error("protocols.BlockToolResult has empty ToolUseID")
			}
		}
	}
	if !foundResult {
		t.Error("No protocols.BlockToolResult found")
	}

	// IR → OpenAI
	openaiBody, err := chat.RequestEncoder{}.EncodeRequest(irReq)
	if err != nil {
		t.Fatalf("OpenAI encode: %v", err)
	}

	// Verify tool message has tool_call_id (not degraded text)
	var req map[string]interface{}
	_ = json.Unmarshal(openaiBody, &req)
	msgs := req["messages"].([]interface{})

	toolOpenAIMsg := msgs[2].(map[string]interface{})
	if toolOpenAIMsg["role"] != "tool" {
		t.Errorf("OpenAI tool message role = %q", toolOpenAIMsg["role"])
	}
	if _, ok := toolOpenAIMsg["tool_call_id"]; !ok {
		t.Error("OpenAI tool message missing tool_call_id")
	}
}

func TestRoundTrip_StreamingToolCall(t *testing.T) {
	// Simulate OpenAI streaming tool call fragments
	dec := chat.NewStreamDecoder()
	enc := NewStreamEncoder()

	chunks := []string{
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"deepseek\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"tool_calls\":[{\"index\":0,\"id\":\"call_abc\",\"function\":{\"name\":\"get_weather\",\"arguments\":\"\"}}]}}]}\n\n",
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"deepseek\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"{\\\"ci\"}}]}}]}\n\n",
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"deepseek\",\"choices\":[{\"delta\":{\"tool_calls\":[{\"index\":0,\"function\":{\"arguments\":\"ty\\\":\\\"Beijing\\\"}\"}}]}}]}\n\n",
	}

	var allEvents []protocols.SSEEvent
	for _, chunk := range chunks {
		deltas, _ := dec.DecodeChunk(chunk)
		events := enc.EncodeDeltas(deltas)
		allEvents = append(allEvents, events...)
	}

	// Should have at least one event with the complete function call
	foundComplete := false
	for _, event := range allEvents {
		if strings.Contains(event.Data, "get_weather") && strings.Contains(event.Data, "Beijing") {
			foundComplete = true
		}
	}
	if !foundComplete {
		t.Error("Streaming round-trip: no complete function call event found")
		t.Logf("Events:")
		for i, e := range allEvents {
			t.Logf("  [%d] %s", i, e.Data)
		}
	}
}

// --- Finish reason mapping ---

// --- ResponseDecoder ---

func TestGeminiResponseDecoder_BasicText(t *testing.T) {
	body := json.RawMessage(`{
		"candidates": [{"content":{"role":"model","parts":[{"text":"Hello!"}]},"finishReason":"STOP"}],
		"usageMetadata":{"promptTokenCount":10,"candidatesTokenCount":5,"totalTokenCount":15},
		"modelVersion":"gemini-2.0-flash"
	}`)

	irResp, err := ResponseDecoder{}.DecodeResponse(body)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if irResp.Content != "Hello!" {
		t.Errorf("Content = %q", irResp.Content)
	}
	if irResp.StopReason != "stop" {
		t.Errorf("StopReason = %q", irResp.StopReason)
	}
	if irResp.Model != "gemini-2.0-flash" {
		t.Errorf("Model = %q", irResp.Model)
	}
	if irResp.Usage.PromptTokens != 10 {
		t.Errorf("PromptTokens = %d", irResp.Usage.PromptTokens)
	}
	if irResp.Usage.CompletionTokens != 5 {
		t.Errorf("CompletionTokens = %d", irResp.Usage.CompletionTokens)
	}
}

func TestGeminiResponseDecoder_FunctionCall(t *testing.T) {
	body := json.RawMessage(`{
		"candidates": [{"content":{"role":"model","parts":[{"functionCall":{"name":"get_weather","args":{"city":"Beijing"}}}]},"finishReason":"STOP"}],
		"modelVersion":"gemini-2.0-flash"
	}`)

	irResp, err := ResponseDecoder{}.DecodeResponse(body)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if len(irResp.ToolCalls) != 1 {
		t.Fatalf("ToolCalls count = %d, want 1", len(irResp.ToolCalls))
	}
	tc := irResp.ToolCalls[0]
	if tc.Name != "get_weather" {
		t.Errorf("ToolCall Name = %q", tc.Name)
	}
	if !strings.Contains(tc.Arguments, "Beijing") {
		t.Errorf("ToolCall Arguments = %q", tc.Arguments)
	}
}

func TestGeminiResponseDecoder_ThoughtParts(t *testing.T) {
	body := json.RawMessage(`{
		"candidates": [{"content":{"role":"model","parts":[
			{"text":"Thinking...","thought":true},
			{"text":"The answer is 42."}
		]},"finishReason":"STOP"}],
		"modelVersion":"gemini-2.0-flash-thinking"
	}`)

	irResp, err := ResponseDecoder{}.DecodeResponse(body)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if irResp.ReasoningContent != "Thinking..." {
		t.Errorf("ReasoningContent = %q", irResp.ReasoningContent)
	}
	if irResp.Content != "The answer is 42." {
		t.Errorf("Content = %q", irResp.Content)
	}
}

func TestGeminiResponseDecoder_CachedTokens(t *testing.T) {
	body := json.RawMessage(`{
		"candidates": [{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],
		"usageMetadata":{"promptTokenCount":100,"candidatesTokenCount":10,"totalTokenCount":110,"cachedContentTokenCount":80},
		"modelVersion":"gemini-2.0-flash"
	}`)

	irResp, err := ResponseDecoder{}.DecodeResponse(body)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if irResp.Usage.CacheReadTokens != 80 {
		t.Errorf("CacheReadTokens = %d, want 80", irResp.Usage.CacheReadTokens)
	}
}

// TestGeminiThinkingTokenAccounting: thinking tokens bill at the output rate, so
// CompletionTokens has to cover them — but the two Gemini backends disagree on
// where they live. Vertex AI reports thoughtsTokenCount alongside
// candidatesTokenCount; the Google AI endpoint folds them into
// candidatesTokenCount. Nothing in the response says which convention applies,
// so the decoder derives output from totalTokenCount - promptTokenCount, which
// is correct under both. Adding candidates + thoughts unconditionally would
// double-charge thinking on the folded-in backend.
func TestGeminiThinkingTokenAccounting(t *testing.T) {
	cases := []struct {
		name           string
		usageMeta      string
		wantPrompt     int
		wantCompletion int
		wantReasoning  int
	}{
		{
			// Vertex AI shape: 100 prompt + 10 answer + 40 thinking = 150.
			name:           "thoughts reported separately from candidates",
			usageMeta:      `{"promptTokenCount":100,"candidatesTokenCount":10,"thoughtsTokenCount":40,"totalTokenCount":150}`,
			wantPrompt:     100,
			wantCompletion: 50,
			wantReasoning:  40,
		},
		{
			// Tool results are input fed back to the model, and Google counts
			// them in the total: prompt 100 + tools 200 + answer 10 + thinking
			// 40 = 350. Leaving them in the subtraction would bill 250 output
			// tokens instead of 50, at the (higher) output rate.
			name:           "tool-use prompt billed as input",
			usageMeta:      `{"promptTokenCount":100,"toolUsePromptTokenCount":200,"candidatesTokenCount":10,"thoughtsTokenCount":40,"totalTokenCount":350}`,
			wantPrompt:     300,
			wantCompletion: 50,
			wantReasoning:  40,
		},
		{
			// Tool use without thinking — the common function-calling shape.
			name:           "tool-use prompt without thinking",
			usageMeta:      `{"promptTokenCount":100,"toolUsePromptTokenCount":200,"candidatesTokenCount":10,"totalTokenCount":310}`,
			wantPrompt:     300,
			wantCompletion: 10,
			wantReasoning:  0,
		},
		{
			// Google AI shape: candidates already covers the 40 thinking tokens,
			// so total is 100 + 50. Summing would wrongly yield 90.
			name:           "thoughts already folded into candidates",
			usageMeta:      `{"promptTokenCount":100,"candidatesTokenCount":50,"thoughtsTokenCount":40,"totalTokenCount":150}`,
			wantPrompt:     100,
			wantCompletion: 50,
			wantReasoning:  40,
		},
		{
			// No thinking at all — the ordinary case must stay untouched.
			name:           "non-thinking model",
			usageMeta:      `{"promptTokenCount":100,"candidatesTokenCount":10,"totalTokenCount":110}`,
			wantPrompt:     100,
			wantCompletion: 10,
			wantReasoning:  0,
		},
		{
			// Total missing: fall back to the sum rather than reporting no output.
			name:           "total omitted falls back to sum",
			usageMeta:      `{"promptTokenCount":100,"candidatesTokenCount":10,"thoughtsTokenCount":40}`,
			wantPrompt:     100,
			wantCompletion: 50,
			wantReasoning:  40,
		},
		{
			// A total at or below the prompt can't be right; the sum is the
			// safer reading.
			name:           "total not larger than prompt falls back to sum",
			usageMeta:      `{"promptTokenCount":100,"candidatesTokenCount":10,"thoughtsTokenCount":40,"totalTokenCount":100}`,
			wantPrompt:     100,
			wantCompletion: 50,
			wantReasoning:  40,
		},
		{
			// promptTokenCount already includes the cached portion, so a cache
			// hit must not shift the subtraction.
			name:           "cached prompt does not distort the subtraction",
			usageMeta:      `{"promptTokenCount":100,"candidatesTokenCount":10,"thoughtsTokenCount":40,"cachedContentTokenCount":80,"totalTokenCount":150}`,
			wantPrompt:     100,
			wantCompletion: 50,
			wantReasoning:  40,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := json.RawMessage(`{
				"candidates": [{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],
				"usageMetadata":` + tc.usageMeta + `,
				"modelVersion":"gemini-2.5-pro"
			}`)
			irResp, err := ResponseDecoder{}.DecodeResponse(body)
			if err != nil {
				t.Fatalf("DecodeResponse: %v", err)
			}
			if irResp.Usage.PromptTokens != tc.wantPrompt {
				t.Errorf("PromptTokens = %d, want %d", irResp.Usage.PromptTokens, tc.wantPrompt)
			}
			if irResp.Usage.CompletionTokens != tc.wantCompletion {
				t.Errorf("CompletionTokens = %d, want %d", irResp.Usage.CompletionTokens, tc.wantCompletion)
			}
			if irResp.Usage.ReasoningTokens != tc.wantReasoning {
				t.Errorf("ReasoningTokens = %d, want %d", irResp.Usage.ReasoningTokens, tc.wantReasoning)
			}
			// The triple must be self-consistent whichever branch produced the
			// completion count, otherwise the gateway rejects the whole usage
			// as incoherent and bills nothing.
			if got := irResp.Usage.PromptTokens + irResp.Usage.CompletionTokens; got > irResp.Usage.TotalTokens {
				t.Errorf("prompt+completion = %d exceeds total %d", got, irResp.Usage.TotalTokens)
			}

			// The streaming path must agree exactly — both decoders share
			// usageMetadata, and this is what keeps them from drifting.
			dec := NewStreamDecoder()
			deltas, _ := dec.DecodeChunk("data: {\"usageMetadata\":" + tc.usageMeta + ",\"candidates\":[]}\n\n")
			found := false
			for _, d := range deltas {
				u, ok := d.(protocols.DeltaUsage)
				if !ok {
					continue
				}
				found = true
				if u.Usage.CompletionTokens != tc.wantCompletion {
					t.Errorf("stream CompletionTokens = %d, want %d", u.Usage.CompletionTokens, tc.wantCompletion)
				}
			}
			if !found {
				t.Error("missing protocols.DeltaUsage")
			}
		})
	}
}

// TestGeminiNegativeUsageRejected: folding thoughts into candidates sums two
// numbers, and a sum hides the sign of its parts — candidates=10 with
// thoughts=-5 would look like an ordinary 5 by the time the gateway's coherence
// check sees it. The block must therefore be rejected at decode time, leaving
// usage unknown (which bills nothing) instead of billing a laundered figure.
func TestGeminiNegativeUsageRejected(t *testing.T) {
	bodies := map[string]string{
		"negative thoughts":   `{"promptTokenCount":100,"candidatesTokenCount":10,"thoughtsTokenCount":-5,"totalTokenCount":105}`,
		"negative candidates": `{"promptTokenCount":100,"candidatesTokenCount":-10,"totalTokenCount":90}`,
		"negative prompt":     `{"promptTokenCount":-100,"candidatesTokenCount":10,"totalTokenCount":10}`,
		"negative cached":     `{"promptTokenCount":100,"candidatesTokenCount":10,"cachedContentTokenCount":-80,"totalTokenCount":110}`,
		"negative total":      `{"promptTokenCount":100,"candidatesTokenCount":10,"totalTokenCount":-1}`,
	}
	for name, um := range bodies {
		t.Run(name, func(t *testing.T) {
			body := json.RawMessage(`{
				"candidates": [{"content":{"role":"model","parts":[{"text":"ok"}]},"finishReason":"STOP"}],
				"usageMetadata":` + um + `,
				"modelVersion":"gemini-2.5-pro"
			}`)
			irResp, err := ResponseDecoder{}.DecodeResponse(body)
			if err != nil {
				t.Fatalf("DecodeResponse: %v", err)
			}
			// All-zero usage is what the gateway reads as "unknown", never as
			// a free request (irUsageToUsage returns nil for it).
			if irResp.Usage.PromptTokens != 0 || irResp.Usage.CompletionTokens != 0 || irResp.Usage.TotalTokens != 0 {
				t.Errorf("expected usage left unset, got %+v", irResp.Usage)
			}

			// The streaming path emits a MARKED delta rather than nothing.
			// Emitting nothing was the earlier contract and it lost the
			// verdict: this decoder rejects before IRUsage.Merge ever sees the
			// frame, so an earlier valid frame's counts stayed in the
			// accumulator and the following DeltaDone billed them as coherent.
			dec := NewStreamDecoder()
			chunk := "data: {\"usageMetadata\":" + um + ",\"candidates\":[]}\n\n"
			deltas, _ := dec.DecodeChunk(chunk)
			var sawUsage bool
			for _, d := range deltas {
				u, ok := d.(protocols.DeltaUsage)
				if !ok {
					continue
				}
				sawUsage = true
				if !u.Usage.Invalid {
					t.Errorf("rejected block must be marked Invalid, got %+v", u.Usage)
				}
				if u.Usage.PromptTokens != 0 || u.Usage.CompletionTokens != 0 || u.Usage.TotalTokens != 0 {
					t.Errorf("a rejected block must carry no counts, got %+v", u.Usage)
				}
				// The mark has to survive the accumulation every relay performs.
				acc := protocols.IRUsage{PromptTokens: 500, CompletionTokens: 5}
				acc.Merge(u.Usage)
				if !acc.Invalid {
					t.Error("Invalid must propagate through Merge, or earlier counts stay billable")
				}
			}
			if !sawUsage {
				t.Error("expected a marked usage delta so the rejection reaches the accumulator")
			}
		})
	}
}

func TestGeminiResponseDecoder_MaxTokensFinish(t *testing.T) {
	body := json.RawMessage(`{
		"candidates": [{"content":{"role":"model","parts":[{"text":"truncated"}]},"finishReason":"MAX_TOKENS"}],
		"modelVersion":"gemini-2.0-flash"
	}`)

	irResp, err := ResponseDecoder{}.DecodeResponse(body)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if irResp.StopReason != "length" {
		t.Errorf("StopReason = %q, want length", irResp.StopReason)
	}
}

func TestGeminiResponseDecoder_FunctionCallEmptyArgs(t *testing.T) {
	body := json.RawMessage(`{
		"candidates": [{"content":{"role":"model","parts":[{"functionCall":{"name":"list_items","args":{}}}]},"finishReason":"STOP"}],
		"modelVersion":"gemini-2.0-flash"
	}`)

	irResp, err := ResponseDecoder{}.DecodeResponse(body)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if len(irResp.ToolCalls) != 1 {
		t.Fatalf("ToolCalls count = %d, want 1", len(irResp.ToolCalls))
	}
	if irResp.ToolCalls[0].Arguments != "{}" {
		t.Errorf("Empty args should be '{}', got %q", irResp.ToolCalls[0].Arguments)
	}
}

func TestMapToGeminiFinishReason(t *testing.T) {
	tests := []struct {
		reason       string
		hasToolCalls bool
		want         string
	}{
		{"stop", false, "STOP"},
		{"tool_calls", false, "STOP"},
		{"length", false, "MAX_TOKENS"},
		{"content_filter", false, "SAFETY"},
		{"", true, "STOP"},
		{"unknown", false, "STOP"},
	}

	for _, tt := range tests {
		got := mapToGeminiFinishReason(tt.reason, tt.hasToolCalls)
		if got != tt.want {
			t.Errorf("mapToGeminiFinishReason(%q, %v) = %q, want %q", tt.reason, tt.hasToolCalls, got, tt.want)
		}
	}
}

// TestBuildGeminiUsage_AnthropicUpstreamEmitsGross guards the cross-protocol
// combination that used to under-report: Gemini's promptTokenCount is the total
// effective prompt size with cachedContentTokenCount a breakdown of it, so an
// Anthropic upstream's NET input must be converted before being emitted.
func TestBuildGeminiUsage_AnthropicUpstreamEmitsGross(t *testing.T) {
	meta := buildGeminiUsage(protocols.IRUsage{
		PromptTokens:     2,
		CompletionTokens: 123,
		CacheWriteTokens: 906,
		CacheReadTokens:  36678,
	})

	if meta["promptTokenCount"] != 37586 {
		t.Errorf("promptTokenCount = %v, want 37586 (net 2 + write 906 + read 36678)", meta["promptTokenCount"])
	}
	if meta["totalTokenCount"] != 37709 {
		t.Errorf("totalTokenCount = %v, want 37709", meta["totalTokenCount"])
	}
	if meta["cachedContentTokenCount"] != 36678 {
		t.Errorf("cachedContentTokenCount = %v, want 36678", meta["cachedContentTokenCount"])
	}
}
