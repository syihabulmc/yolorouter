package chat

import (
	"encoding/json"
	"github.com/yolorouter/yolorouter/internal/protocols"
	"github.com/yolorouter/yolorouter/internal/protocols/claude"
	"strings"
	"testing"
)

// In-package test helpers: this subpackage keeps its own pointer helpers,
// independent of the same-named implementations in the gemini/responses test packages.
func ptrBool(v bool) *bool        { return &v }
func ptrInt(v int) *int           { return &v }
func ptrFloat(v float64) *float64 { return &v }

// --- OpenAI Request Encoder ---

func TestOpenAIEncodeRequest_BasicChat(t *testing.T) {
	irReq := &protocols.IRRequest{
		Model:  "deepseek-chat",
		System: "You are helpful.",
		Messages: []protocols.IRMessage{
			{Role: protocols.RoleUser, Content: []protocols.IRContentBlock{protocols.BlockText{Text: "Hello"}}},
			{Role: protocols.RoleAssistant, Content: []protocols.IRContentBlock{protocols.BlockText{Text: "Hi!"}}},
			{Role: protocols.RoleUser, Content: []protocols.IRContentBlock{protocols.BlockText{Text: "How are you?"}}},
		},
		Stream: protocols.IRStreamConfig{Enabled: true},
	}

	enc := RequestEncoder{}
	data, err := enc.EncodeRequest(irReq)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}

	var req map[string]interface{}
	if err := json.Unmarshal(data, &req); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if req["model"] != "deepseek-chat" {
		t.Errorf("model = %v", req["model"])
	}
	if req["stream"] != true {
		t.Error("stream should be true")
	}

	msgs, ok := req["messages"].([]interface{})
	if !ok {
		t.Fatal("messages is not an array")
	}
	if len(msgs) != 4 { // system + 3 messages
		t.Errorf("messages count = %d, want 4", len(msgs))
	}

	// First message should be system
	sysMsg := msgs[0].(map[string]interface{})
	if sysMsg["role"] != "system" {
		t.Errorf("first message role = %q", sysMsg["role"])
	}
}

func TestOpenAIEncodeRequest_ToolResult(t *testing.T) {
	irReq := &protocols.IRRequest{
		Model: "gpt-4",
		Messages: []protocols.IRMessage{
			{Role: protocols.RoleUser, Content: []protocols.IRContentBlock{protocols.BlockText{Text: "Weather?"}}},
			{
				Role:    protocols.RoleAssistant,
				Content: []protocols.IRContentBlock{protocols.BlockText{Text: ""}},
				ToolCalls: []protocols.IRToolCall{
					{ID: "call_abc", Name: "get_weather", Arguments: `{"city":"Beijing"}`},
				},
			},
			{
				Role:       protocols.RoleTool,
				ToolCallID: "call_abc",
				Content: []protocols.IRContentBlock{
					protocols.BlockToolResult{
						ToolUseID: "call_abc",
						Content:   json.RawMessage(`"Sunny, 25°C"`),
					},
				},
			},
		},
		Stream: protocols.IRStreamConfig{Enabled: false},
	}

	data, err := RequestEncoder{}.EncodeRequest(irReq)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}

	var req map[string]interface{}
	_ = json.Unmarshal(data, &req)
	msgs := req["messages"].([]interface{})

	// Tool result message
	toolMsg := msgs[2].(map[string]interface{})
	if toolMsg["role"] != "tool" {
		t.Errorf("tool message role = %q", toolMsg["role"])
	}
	if toolMsg["tool_call_id"] != "call_abc" {
		t.Errorf("tool_call_id = %v", toolMsg["tool_call_id"])
	}
	if toolMsg["content"] != "Sunny, 25°C" {
		t.Errorf("content = %v", toolMsg["content"])
	}

	// Assistant message with tool_calls
	assistantMsg := msgs[1].(map[string]interface{})
	tcs, ok := assistantMsg["tool_calls"].([]interface{})
	if !ok {
		t.Fatal("assistant message missing tool_calls")
	}
	if len(tcs) != 1 {
		t.Errorf("tool_calls count = %d", len(tcs))
	}
}

func TestOpenAIEncodeRequest_ThinkingContent(t *testing.T) {
	irReq := &protocols.IRRequest{
		Model: "deepseek-reasoner",
		Messages: []protocols.IRMessage{
			{
				Role: protocols.RoleAssistant,
				Content: []protocols.IRContentBlock{
					protocols.BlockThinking{Thinking: "Let me reason..."},
					protocols.BlockText{Text: "Here is the answer."},
				},
			},
		},
		Stream: protocols.IRStreamConfig{Enabled: false},
	}

	data, err := RequestEncoder{}.EncodeRequest(irReq)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}

	var req map[string]interface{}
	_ = json.Unmarshal(data, &req)
	msgs := req["messages"].([]interface{})
	msg := msgs[0].(map[string]interface{})

	if msg["reasoning_content"] != "Let me reason..." {
		t.Errorf("reasoning_content = %v", msg["reasoning_content"])
	}
	if msg["content"] != "Here is the answer." {
		t.Errorf("content = %v", msg["content"])
	}
}

func TestOpenAIEncodeRequest_GenerationConfig(t *testing.T) {
	irReq := &protocols.IRRequest{
		Model: "gpt-4",
		Messages: []protocols.IRMessage{
			{Role: protocols.RoleUser, Content: []protocols.IRContentBlock{protocols.BlockText{Text: "Hi"}}},
		},
		Generation: protocols.IRGenerationConfig{
			Temperature:      ptrFloat(0.5),
			MaxTokens:        ptrInt(100),
			TopP:             ptrFloat(0.9),
			StopSequences:    []string{"stop"},
			PresencePenalty:  ptrFloat(0.1),
			FrequencyPenalty: ptrFloat(0.2),
		},
		Stream: protocols.IRStreamConfig{Enabled: false},
	}

	data, err := RequestEncoder{}.EncodeRequest(irReq)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}

	var req map[string]interface{}
	_ = json.Unmarshal(data, &req)

	if req["temperature"] != 0.5 {
		t.Errorf("temperature = %v", req["temperature"])
	}
	if req["max_tokens"] != float64(100) {
		t.Errorf("max_tokens = %v", req["max_tokens"])
	}
	if req["top_p"] != 0.9 {
		t.Errorf("top_p = %v", req["top_p"])
	}
	if req["presence_penalty"] != 0.1 {
		t.Errorf("presence_penalty = %v", req["presence_penalty"])
	}
	if req["frequency_penalty"] != 0.2 {
		t.Errorf("frequency_penalty = %v", req["frequency_penalty"])
	}
}

func TestOpenAIEncodeRequest_ReasoningEffort(t *testing.T) {
	tests := []struct {
		name   string
		budget int
		want   string
	}{
		{"low", 500, "low"},
		{"medium", 5000, "medium"},
		{"high", 80000, "high"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			irReq := &protocols.IRRequest{
				Model: "o1",
				Messages: []protocols.IRMessage{
					{Role: protocols.RoleUser, Content: []protocols.IRContentBlock{protocols.BlockText{Text: "Hi"}}},
				},
				Reasoning: protocols.IRReasoningConfig{Enabled: true, BudgetTokens: &tt.budget},
				Stream:    protocols.IRStreamConfig{Enabled: false},
			}

			data, _ := RequestEncoder{}.EncodeRequest(irReq)
			var req map[string]interface{}
			_ = json.Unmarshal(data, &req)

			if req["reasoning_effort"] != tt.want {
				t.Errorf("reasoning_effort = %v, want %q", req["reasoning_effort"], tt.want)
			}
		})
	}
}

func TestOpenAIEncodeRequest_Tools(t *testing.T) {
	irReq := &protocols.IRRequest{
		Model: "gpt-4",
		Messages: []protocols.IRMessage{
			{Role: protocols.RoleUser, Content: []protocols.IRContentBlock{protocols.BlockText{Text: "Hi"}}},
		},
		Tools: []protocols.IRToolSpec{
			{Name: "get_weather", Description: "Get weather", Parameters: json.RawMessage(`{"type":"object"}`)},
		},
		ToolChoice:     protocols.ToolChoiceNamed,
		ToolChoiceName: "get_weather",
		Stream:         protocols.IRStreamConfig{Enabled: false},
	}

	data, _ := RequestEncoder{}.EncodeRequest(irReq)
	var req map[string]interface{}
	_ = json.Unmarshal(data, &req)

	tools := req["tools"].([]interface{})
	if len(tools) != 1 {
		t.Fatalf("tools count = %d", len(tools))
	}
	tool := tools[0].(map[string]interface{})
	fn := tool["function"].(map[string]interface{})
	if fn["name"] != "get_weather" {
		t.Errorf("tool name = %v", fn["name"])
	}

	// Tool choice
	tc := req["tool_choice"]
	tcMap, ok := tc.(map[string]interface{})
	if !ok {
		t.Fatalf("tool_choice type = %T", tc)
	}
	fnChoice := tcMap["function"].(map[string]interface{})
	if fnChoice["name"] != "get_weather" {
		t.Errorf("tool_choice function name = %v", fnChoice["name"])
	}
}

func TestOpenAIEncodeRequest_StreamOptions(t *testing.T) {
	irReq := &protocols.IRRequest{
		Model: "gpt-4",
		Messages: []protocols.IRMessage{
			{Role: protocols.RoleUser, Content: []protocols.IRContentBlock{protocols.BlockText{Text: "Hi"}}},
		},
		Stream: protocols.IRStreamConfig{Enabled: true},
	}

	data, _ := RequestEncoder{}.EncodeRequest(irReq)
	var req map[string]interface{}
	_ = json.Unmarshal(data, &req)

	so, ok := req["stream_options"].(map[string]interface{})
	if !ok {
		t.Fatal("stream_options missing")
	}
	if so["include_usage"] != true {
		t.Error("stream_options.include_usage should be true")
	}
}

func TestOpenAIResponseDecoder_Basic(t *testing.T) {
	body := json.RawMessage(`{
		"id": "chatcmpl-123",
		"model": "gpt-4",
		"choices": [{
			"message": {"role": "assistant", "content": "Hello!"},
			"finish_reason": "stop"
		}],
		"usage": {
			"prompt_tokens": 10,
			"completion_tokens": 5,
			"total_tokens": 15
		}
	}`)

	resp, err := ResponseDecoder{}.DecodeResponse(body)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if resp.ID != "chatcmpl-123" {
		t.Errorf("ID = %q", resp.ID)
	}
	if resp.Content != "Hello!" {
		t.Errorf("Content = %q", resp.Content)
	}
	if resp.StopReason != "stop" {
		t.Errorf("StopReason = %q", resp.StopReason)
	}
	if resp.Usage.PromptTokens != 10 {
		t.Errorf("PromptTokens = %d", resp.Usage.PromptTokens)
	}
}

func TestOpenAIResponseDecoder_ToolCalls(t *testing.T) {
	body := json.RawMessage(`{
		"id": "chatcmpl-456",
		"model": "gpt-4",
		"choices": [{
			"message": {
				"role": "assistant",
				"tool_calls": [{
					"id": "call_abc",
					"type": "function",
					"function": {"name": "get_weather", "arguments": "{\"city\":\"Beijing\"}"}
				}]
			},
			"finish_reason": "tool_calls"
		}],
		"usage": {"prompt_tokens": 10, "completion_tokens": 20, "total_tokens": 30}
	}`)

	resp, err := ResponseDecoder{}.DecodeResponse(body)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if len(resp.ToolCalls) != 1 {
		t.Fatalf("ToolCalls count = %d", len(resp.ToolCalls))
	}
	if resp.ToolCalls[0].Name != "get_weather" {
		t.Errorf("ToolCall Name = %q", resp.ToolCalls[0].Name)
	}
	if resp.ToolCalls[0].ID != "call_abc" {
		t.Errorf("ToolCall ID = %q", resp.ToolCalls[0].ID)
	}
}

func TestOpenAIResponseDecoder_CacheTokens(t *testing.T) {
	body := json.RawMessage(`{
		"id": "chatcmpl-789",
		"model": "deepseek",
		"choices": [{"message": {"role": "assistant", "content": "ok"}, "finish_reason": "stop"}],
		"usage": {
			"prompt_tokens": 100,
			"completion_tokens": 50,
			"total_tokens": 150,
			"prompt_tokens_details": {"cached_tokens": 80}
		}
	}`)

	resp, _ := ResponseDecoder{}.DecodeResponse(body)
	if resp.Usage.CacheReadTokens != 80 {
		t.Errorf("CacheReadTokens = %d, want 80", resp.Usage.CacheReadTokens)
	}
}

func TestOpenAIStreamDecoder_Basic(t *testing.T) {
	dec := NewStreamDecoder()

	chunks := []string{
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-4\",\"choices\":[{\"delta\":{\"role\":\"assistant\",\"content\":\"Hel\"}}]}\n\n",
		"data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-4\",\"choices\":[{\"delta\":{\"content\":\"lo!\"}}]}\n\n",
		"data: [DONE]\n\n",
	}

	var allDeltas []protocols.IRStreamDelta
	for _, chunk := range chunks {
		deltas, err := dec.DecodeChunk(chunk)
		if err != nil {
			t.Fatalf("DecodeChunk: %v", err)
		}
		allDeltas = append(allDeltas, deltas...)
	}

	// Should have: MessageStart, protocols.DeltaText("Hel"), protocols.DeltaText("lo!"), protocols.DeltaDone
	hasStart, hasText, hasDone := false, false, false
	for _, d := range allDeltas {
		switch v := d.(type) {
		case protocols.DeltaMessageStart:
			hasStart = true
			if v.ID != "chatcmpl-1" {
				t.Errorf("MessageStart ID = %q", v.ID)
			}
		case protocols.DeltaText:
			hasText = true
		case protocols.DeltaDone:
			hasDone = true
		}
	}
	if !hasStart {
		t.Error("Missing protocols.DeltaMessageStart")
	}
	if !hasText {
		t.Error("Missing protocols.DeltaText")
	}
	if !hasDone {
		t.Error("Missing protocols.DeltaDone")
	}
}

func TestOpenAIStreamDecoder_Usage(t *testing.T) {
	dec := NewStreamDecoder()

	chunk := "data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-4\",\"choices\":[],\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":5,\"total_tokens\":15}}\n\n"

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

// TestDeepSeekCacheTokensDecode: DeepSeek splits prompt_tokens into
// prompt_cache_hit_tokens + prompt_cache_miss_tokens. The hit half is a cache
// READ; the miss half is the plain non-cached remainder, NOT a cache write.
// Reporting it as a write both prices those tokens at the cache-write rate and
// drives the logged net input to zero (prompt - hit - miss == 0).
func TestDeepSeekCacheTokensDecode(t *testing.T) {
	const body = `{"id":"x","model":"deepseek-chat","choices":[{"message":{"content":"hi"},"finish_reason":"stop"}],` +
		`"usage":{"prompt_tokens":1000,"completion_tokens":10,"total_tokens":1010,` +
		`"prompt_cache_hit_tokens":600,"prompt_cache_miss_tokens":400}}`

	resp, err := ResponseDecoder{}.DecodeResponse([]byte(body))
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if resp.Usage.CacheReadTokens != 600 {
		t.Errorf("CacheReadTokens = %d, want 600", resp.Usage.CacheReadTokens)
	}
	if resp.Usage.CacheWriteTokens != 0 {
		t.Errorf("CacheWriteTokens = %d, want 0 (miss tokens are not a cache write)", resp.Usage.CacheWriteTokens)
	}
	if resp.Usage.PromptTokens != 1000 {
		t.Errorf("PromptTokens = %d, want 1000", resp.Usage.PromptTokens)
	}

	// Same expectations on the streaming path.
	dec := NewStreamDecoder()
	chunk := "data: {\"id\":\"x\",\"model\":\"deepseek-chat\",\"choices\":[]," +
		"\"usage\":{\"prompt_tokens\":1000,\"completion_tokens\":10,\"total_tokens\":1010," +
		"\"prompt_cache_hit_tokens\":600,\"prompt_cache_miss_tokens\":400}}\n\n"
	deltas, _ := dec.DecodeChunk(chunk)
	found := false
	for _, d := range deltas {
		u, ok := d.(protocols.DeltaUsage)
		if !ok {
			continue
		}
		found = true
		if u.Usage.CacheReadTokens != 600 {
			t.Errorf("stream CacheReadTokens = %d, want 600", u.Usage.CacheReadTokens)
		}
		if u.Usage.CacheWriteTokens != 0 {
			t.Errorf("stream CacheWriteTokens = %d, want 0", u.Usage.CacheWriteTokens)
		}
	}
	if !found {
		t.Error("missing protocols.DeltaUsage")
	}
}

func TestOpenAIStreamDecoder_Finish(t *testing.T) {
	dec := NewStreamDecoder()

	// Send an incomplete chunk
	_, _ = dec.DecodeChunk("data: {\"id\":\"chatcmpl-1\",\"model\":\"gpt-4\",\"choices\":[{\"delta\":{\"content\":\"Hi\"}}]}\n")

	// Finish should flush remaining buffer
	deltas, err := dec.Finish()
	if err != nil {
		t.Fatalf("Finish: %v", err)
	}

	foundText := false
	for _, d := range deltas {
		if _, ok := d.(protocols.DeltaText); ok {
			foundText = true
		}
	}
	if !foundText {
		t.Error("Finish should flush buffered text")
	}
}

func TestOpenAIEncoder_ImageContent(t *testing.T) {
	irReq := &protocols.IRRequest{
		Model: "gpt-4o",
		Messages: []protocols.IRMessage{
			{
				Role: protocols.RoleUser,
				Content: []protocols.IRContentBlock{
					protocols.BlockText{Text: "What's this?"},
					protocols.BlockImage{MediaType: "image/png", Data: "base64data", IsURL: false},
				},
			},
		},
		Stream: protocols.IRStreamConfig{Enabled: false},
	}

	data, _ := RequestEncoder{}.EncodeRequest(irReq)
	var req map[string]interface{}
	_ = json.Unmarshal(data, &req)
	msgs := req["messages"].([]interface{})
	msg := msgs[0].(map[string]interface{})

	// Should have array content (complex)
	contentArr, ok := msg["content"].([]interface{})
	if !ok {
		t.Fatal("content should be an array for multimodal")
	}
	if len(contentArr) != 2 {
		t.Errorf("content parts = %d, want 2", len(contentArr))
	}

	// Second part should be image_url
	imgPart := contentArr[1].(map[string]interface{})
	if imgPart["type"] != "image_url" {
		t.Errorf("image part type = %v", imgPart["type"])
	}
}

func TestOpenAIProtocol(t *testing.T) {
	enc := RequestEncoder{}
	if enc.Protocol() != protocols.ProtocolOpenAI {
		t.Errorf("Protocol = %v", enc.Protocol())
	}
}

func TestOpenAIEncodeRequest_ResponseFormat(t *testing.T) {
	irReq := &protocols.IRRequest{
		Model: "gpt-4",
		Messages: []protocols.IRMessage{
			{Role: protocols.RoleUser, Content: []protocols.IRContentBlock{protocols.BlockText{Text: "Hi"}}},
		},
		ResponseFormat: &protocols.IRResponseFormat{
			Type:   "json_object",
			Schema: nil,
		},
		Stream: protocols.IRStreamConfig{Enabled: false},
	}

	data, _ := RequestEncoder{}.EncodeRequest(irReq)
	var req map[string]interface{}
	_ = json.Unmarshal(data, &req)

	rf, ok := req["response_format"].(map[string]interface{})
	if !ok {
		t.Fatal("response_format missing")
	}
	if rf["type"] != "json_object" {
		t.Errorf("response_format type = %v", rf["type"])
	}
}

// Ensure OpenAI encoder handles empty messages gracefully
func TestOpenAIEncodeRequest_EmptyMessages(t *testing.T) {
	irReq := &protocols.IRRequest{
		Model:    "gpt-4",
		Messages: nil,
		Stream:   protocols.IRStreamConfig{Enabled: false},
	}

	data, err := RequestEncoder{}.EncodeRequest(irReq)
	if err != nil {
		t.Fatalf("EncodeRequest with empty messages: %v", err)
	}
	if !strings.Contains(string(data), `"messages"`) {
		t.Error("Missing messages field")
	}
}

// --- Extended params: decoder + encoder ---

func TestOpenAIDecodeRequest_ExtendedParams(t *testing.T) {
	body := json.RawMessage(`{
		"model": "gpt-4",
		"messages": [{"role": "user", "content": "Hi"}],
		"top_k": 40,
		"top_a": 0.1,
		"min_p": 0.05,
		"seed": 42,
		"repetition_penalty": 1.1,
		"logprobs": true,
		"top_logprobs": 5
	}`)

	dec := RequestDecoder{}
	req, err := dec.DecodeRequest(body, "gpt-4", false)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}

	g := req.Generation
	if g.TopK == nil || *g.TopK != 40 {
		t.Errorf("TopK = %v, want 40", g.TopK)
	}
	if g.TopA == nil || *g.TopA != 0.1 {
		t.Errorf("TopA = %v, want 0.1", g.TopA)
	}
	if g.MinP == nil || *g.MinP != 0.05 {
		t.Errorf("MinP = %v, want 0.05", g.MinP)
	}
	if g.Seed == nil || *g.Seed != 42 {
		t.Errorf("Seed = %v, want 42", g.Seed)
	}
	if g.RepetitionPenalty == nil || *g.RepetitionPenalty != 1.1 {
		t.Errorf("RepetitionPenalty = %v, want 1.1", g.RepetitionPenalty)
	}
	if g.LogProbs == nil || *g.LogProbs != true {
		t.Errorf("LogProbs = %v, want true", g.LogProbs)
	}
	if g.TopLogProbs == nil || *g.TopLogProbs != 5 {
		t.Errorf("TopLogProbs = %v, want 5", g.TopLogProbs)
	}
}

// TestOpenAIDecodeRequest_StopScalar is a regression test for the fix
// that made the decoder accept "stop" as a plain string, not just an array —
// OpenAI's Chat Completions API documents "stop" as EITHER a string OR an
// array of strings, and a scalar "stop" used to fail the top-level JSON
// unmarshal, erroring the whole decode.
func TestOpenAIDecodeRequest_StopScalar(t *testing.T) {
	body := json.RawMessage(`{
		"model": "gpt-4",
		"messages": [{"role": "user", "content": "Hi"}],
		"stop": "END"
	}`)

	dec := RequestDecoder{}
	req, err := dec.DecodeRequest(body, "gpt-4", false)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if got := req.Generation.StopSequences; len(got) != 1 || got[0] != "END" {
		t.Errorf("StopSequences = %v, want [\"END\"]", got)
	}
}

// TestOpenAIDecodeRequest_StopArray covers the array form of "stop", which
// the decoder already supported before the scalar fix.
func TestOpenAIDecodeRequest_StopArray(t *testing.T) {
	body := json.RawMessage(`{
		"model": "gpt-4",
		"messages": [{"role": "user", "content": "Hi"}],
		"stop": ["A", "B"]
	}`)

	dec := RequestDecoder{}
	req, err := dec.DecodeRequest(body, "gpt-4", false)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	want := []string{"A", "B"}
	got := req.Generation.StopSequences
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("StopSequences = %v, want %v", got, want)
	}
}

// TestOpenAIDecodeRequest_StopAbsent covers the no-"stop" case, which must
// decode successfully with an empty StopSequences slice.
func TestOpenAIDecodeRequest_StopAbsent(t *testing.T) {
	body := json.RawMessage(`{
		"model": "gpt-4",
		"messages": [{"role": "user", "content": "Hi"}]
	}`)

	dec := RequestDecoder{}
	req, err := dec.DecodeRequest(body, "gpt-4", false)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if got := req.Generation.StopSequences; len(got) != 0 {
		t.Errorf("StopSequences = %v, want empty", got)
	}
}

func TestOpenAIEncodeRequest_ExtendedParams(t *testing.T) {
	seed := int64(42)
	irReq := &protocols.IRRequest{
		Model: "gpt-4",
		Messages: []protocols.IRMessage{
			{Role: protocols.RoleUser, Content: []protocols.IRContentBlock{protocols.BlockText{Text: "Hi"}}},
		},
		Generation: protocols.IRGenerationConfig{
			TopK:                ptrInt(40),
			TopA:                ptrFloat(0.1),
			MinP:                ptrFloat(0.05),
			Seed:                &seed,
			RepetitionPenalty:   ptrFloat(1.1),
			LogProbs:            ptrBool(true),
			TopLogProbs:         ptrInt(5),
			AllowExtendedParams: true, // simulate Chat ingress
		},
		Stream: protocols.IRStreamConfig{Enabled: false},
	}

	data, err := RequestEncoder{}.EncodeRequest(irReq)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}

	var req map[string]interface{}
	if err := json.Unmarshal(data, &req); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	cases := []struct {
		key  string
		want interface{}
	}{
		{"top_k", float64(40)},
		{"top_a", 0.1},
		{"min_p", 0.05},
		{"seed", float64(42)},
		{"repetition_penalty", 1.1},
		{"logprobs", true},
		{"top_logprobs", float64(5)},
	}
	for _, tc := range cases {
		if req[tc.key] != tc.want {
			t.Errorf("%s = %v (%T), want %v (%T)", tc.key, req[tc.key], req[tc.key], tc.want, tc.want)
		}
	}
}

// TestOpenAIEncodeRequest_ExtendedParamsOmitNil verifies that nil extended params
// do not appear in the encoded output (no spurious keys sent to upstream).
func TestOpenAIEncodeRequest_ExtendedParamsOmitNil(t *testing.T) {
	irReq := &protocols.IRRequest{
		Model: "gpt-4",
		Messages: []protocols.IRMessage{
			{Role: protocols.RoleUser, Content: []protocols.IRContentBlock{protocols.BlockText{Text: "Hi"}}},
		},
		Generation: protocols.IRGenerationConfig{},
		Stream:     protocols.IRStreamConfig{Enabled: false},
	}

	data, _ := RequestEncoder{}.EncodeRequest(irReq)
	var req map[string]interface{}
	_ = json.Unmarshal(data, &req)

	for _, key := range []string{"top_k", "top_a", "min_p", "seed", "repetition_penalty", "logprobs", "top_logprobs"} {
		if _, ok := req[key]; ok {
			t.Errorf("key %q should not appear when nil", key)
		}
	}
}

// TestOpenAIEncodeRequest_ExtendedParamsDroppedCrossProtocol verifies that non-standard
// extended params are NOT forwarded when AllowExtendedParams is false (cross-protocol path,
// e.g. Claude→Chat or Gemini→Chat egress), even if the IR fields are populated.
func TestOpenAIEncodeRequest_ExtendedParamsDroppedCrossProtocol(t *testing.T) {
	seed := int64(7)
	irReq := &protocols.IRRequest{
		Model: "gpt-4",
		Messages: []protocols.IRMessage{
			{Role: protocols.RoleUser, Content: []protocols.IRContentBlock{protocols.BlockText{Text: "Hi"}}},
		},
		Generation: protocols.IRGenerationConfig{
			TopK:                ptrInt(50),
			TopA:                ptrFloat(0.2),
			MinP:                ptrFloat(0.01),
			RepetitionPenalty:   ptrFloat(1.2),
			Seed:                &seed, // standard OpenAI param — still forwarded
			AllowExtendedParams: false, // cross-protocol: drop non-standard params
		},
		Stream: protocols.IRStreamConfig{Enabled: false},
	}

	data, _ := RequestEncoder{}.EncodeRequest(irReq)
	var req map[string]interface{}
	_ = json.Unmarshal(data, &req)

	// Non-standard extended params must be absent
	for _, key := range []string{"top_k", "top_a", "min_p", "repetition_penalty"} {
		if _, ok := req[key]; ok {
			t.Errorf("key %q must not appear in cross-protocol encoding (AllowExtendedParams=false)", key)
		}
	}
	// seed is standard OpenAI Chat API — must still be present
	if req["seed"] != float64(7) {
		t.Errorf("seed = %v, want 7 (standard param must be forwarded regardless of AllowExtendedParams)", req["seed"])
	}
}

// --- Reasoning encode round-trip ---

// TestChatEncodeRequest_ReasoningEffortAndBudget verifies that when both
// reasoning.effort and reasoning.budget_tokens are present, the explicit
// effort string takes priority over the budget-derived value.
func TestChatEncodeRequest_ReasoningEffortAndBudget(t *testing.T) {
	budget := 100 // <= 1000 threshold → budget-only would derive "low"
	irReq := &protocols.IRRequest{
		Model: "o1",
		Messages: []protocols.IRMessage{
			{Role: protocols.RoleUser, Content: []protocols.IRContentBlock{protocols.BlockText{Text: "hi"}}},
		},
		Reasoning: protocols.IRReasoningConfig{
			Enabled:      true,
			Effort:       "high", // explicit effort must win
			BudgetTokens: &budget,
		},
		Stream: protocols.IRStreamConfig{Enabled: false},
	}

	data, err := RequestEncoder{}.EncodeRequest(irReq)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}
	var body map[string]interface{}
	if err := json.Unmarshal(data, &body); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if got := body["reasoning_effort"]; got != "high" {
		t.Errorf("reasoning_effort = %v, want high (explicit effort must not be overridden by budget thresholds)", got)
	}
}

// TestChatEncodeRequest_ReasoningBudgetOnly verifies that when only
// budget_tokens is set (no explicit effort), the effort is derived from budget.
func TestChatEncodeRequest_ReasoningBudgetOnly(t *testing.T) {
	budget := 500 // <= 1000 → "low"
	irReq := &protocols.IRRequest{
		Model: "o1",
		Messages: []protocols.IRMessage{
			{Role: protocols.RoleUser, Content: []protocols.IRContentBlock{protocols.BlockText{Text: "hi"}}},
		},
		Reasoning: protocols.IRReasoningConfig{
			Enabled:      true,
			Effort:       "", // no explicit effort
			BudgetTokens: &budget,
		},
		Stream: protocols.IRStreamConfig{Enabled: false},
	}

	data, _ := RequestEncoder{}.EncodeRequest(irReq)
	var body map[string]interface{}
	_ = json.Unmarshal(data, &body)
	if got := body["reasoning_effort"]; got != "low" {
		t.Errorf("reasoning_effort = %v, want low (budget 500 <= 1000 threshold)", got)
	}
}

// TestChatEncodeRequest_ReasoningEffortOnly verifies that when only effort
// string is set (no budget), the explicit effort is forwarded as-is.
func TestChatEncodeRequest_ReasoningEffortOnly(t *testing.T) {
	irReq := &protocols.IRRequest{
		Model: "o1",
		Messages: []protocols.IRMessage{
			{Role: protocols.RoleUser, Content: []protocols.IRContentBlock{protocols.BlockText{Text: "hi"}}},
		},
		Reasoning: protocols.IRReasoningConfig{
			Enabled:      true,
			Effort:       "medium",
			BudgetTokens: nil,
		},
		Stream: protocols.IRStreamConfig{Enabled: false},
	}

	data, _ := RequestEncoder{}.EncodeRequest(irReq)
	var body map[string]interface{}
	_ = json.Unmarshal(data, &body)
	if got := body["reasoning_effort"]; got != "medium" {
		t.Errorf("reasoning_effort = %v, want medium", got)
	}
}

// --- System message normalization (decoder) ---

// TestOpenAIDecodeRequest_SystemMessageNormalizedIntoIRSystem verifies that a
// role:"system" Chat message is extracted into IRRequest.System rather than
// left as a RoleSystem entry in Messages, matching the responses/claude/gemini
// decoders. Without this, egress encoders that only read IRRequest.System
// (claude, gemini) would silently drop the client's system prompt.
func TestOpenAIDecodeRequest_SystemMessageNormalizedIntoIRSystem(t *testing.T) {
	body := json.RawMessage(`{
		"model": "gpt-4",
		"messages": [
			{"role": "system", "content": "You are a helpful assistant."},
			{"role": "user", "content": "Hi"}
		]
	}`)

	req, err := RequestDecoder{}.DecodeRequest(body, "gpt-4", false)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}

	if req.System != "You are a helpful assistant." {
		t.Errorf("System = %q, want %q", req.System, "You are a helpful assistant.")
	}
	for _, m := range req.Messages {
		if m.Role == protocols.RoleSystem {
			t.Fatal("Messages must not contain a RoleSystem entry after decoding")
		}
	}
	if len(req.Messages) != 1 || req.Messages[0].Role != protocols.RoleUser {
		t.Fatalf("Messages = %+v, want a single user message", req.Messages)
	}
}

// TestOpenAIDecodeRequest_MultipleSystemMessagesJoined verifies that multiple
// role:"system" messages are concatenated into IRRequest.System with a
// "\n\n" separator, mirroring the responses decoder's behavior.
func TestOpenAIDecodeRequest_MultipleSystemMessagesJoined(t *testing.T) {
	body := json.RawMessage(`{
		"model": "gpt-4",
		"messages": [
			{"role": "system", "content": "First rule."},
			{"role": "system", "content": "Second rule."},
			{"role": "user", "content": "Hi"}
		]
	}`)

	req, err := RequestDecoder{}.DecodeRequest(body, "gpt-4", false)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	want := "First rule.\n\nSecond rule."
	if req.System != want {
		t.Errorf("System = %q, want %q", req.System, want)
	}
}

// TestOpenAIDecodeRequest_SystemMessageArrayContent verifies that a system
// message whose content is an array of text parts (rather than a plain
// string) is still normalized into IRRequest.System.
func TestOpenAIDecodeRequest_SystemMessageArrayContent(t *testing.T) {
	body := json.RawMessage(`{
		"model": "gpt-4",
		"messages": [
			{"role": "system", "content": [{"type": "text", "text": "Be concise."}]},
			{"role": "user", "content": "Hi"}
		]
	}`)

	req, err := RequestDecoder{}.DecodeRequest(body, "gpt-4", false)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if req.System != "Be concise." {
		t.Errorf("System = %q, want %q", req.System, "Be concise.")
	}
}

// TestOpenAIDecodeRequest_DeveloperRoleNormalizedIntoIRSystem verifies that a
// role:"developer" Chat message — OpenAI's o1-class replacement for
// "system", carrying the same system-level instruction precedence — is
// folded into IRRequest.System exactly like role:"system", instead of
// falling through the role switch's default branch into a RoleUser message.
// Coercing it to a user message would lose that precedence on cross-protocol
// routing (e.g. dropping it from the top-level "system" field of a Claude
// egress request).
func TestOpenAIDecodeRequest_DeveloperRoleNormalizedIntoIRSystem(t *testing.T) {
	body := json.RawMessage(`{
		"model": "gpt-4",
		"messages": [
			{"role": "developer", "content": "Always answer in JSON."},
			{"role": "user", "content": "Hi"}
		]
	}`)

	req, err := RequestDecoder{}.DecodeRequest(body, "gpt-4", false)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}

	if req.System != "Always answer in JSON." {
		t.Errorf("System = %q, want %q", req.System, "Always answer in JSON.")
	}
	if len(req.Messages) != 1 || req.Messages[0].Role != protocols.RoleUser {
		t.Fatalf("Messages = %+v, want a single user message (the developer message must not become a user message)", req.Messages)
	}
}

// TestOpenAIToClaudeRoundTrip_SystemPromptSurvives is an end-to-end regression
// test for the bug where an OpenAI Chat ingress request with a system message,
// routed to a Claude egress, silently dropped the system prompt: the claude
// encoder only reads IRRequest.System, and the chat decoder used to leave the
// system message as a RoleSystem entry in Messages instead of populating it.
func TestOpenAIToClaudeRoundTrip_SystemPromptSurvives(t *testing.T) {
	const systemPrompt = "You are a pirate. Always respond in pirate speak."
	body := json.RawMessage(`{
		"model": "gpt-4",
		"messages": [
			{"role": "system", "content": "You are a pirate. Always respond in pirate speak."},
			{"role": "user", "content": "Hello"}
		]
	}`)

	irReq, err := RequestDecoder{}.DecodeRequest(body, "claude-3-opus", false)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}

	claudeBody, err := claude.RequestEncoder{}.EncodeRequest(irReq)
	if err != nil {
		t.Fatalf("EncodeRequest: %v", err)
	}

	var out struct {
		System   string `json:"system"`
		Messages []struct {
			Role string `json:"role"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(claudeBody, &out); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if out.System != systemPrompt {
		t.Errorf("claude request system = %q, want %q (system prompt dropped)", out.System, systemPrompt)
	}
	for _, m := range out.Messages {
		if m.Role == "system" {
			t.Error("claude request messages must not contain a role=system entry")
		}
	}
}

// --- StreamEncoder partial usage ---

// TestChatStreamEncoder_PartialUsageDoesNotOverwritePrompt aligns the chat
// encoder with the claude / responses / gemini encoders: DeltaUsage must also
// merge at the field level, so that a later completion-only chunk cannot
// overwrite prompt + cache fields that were already collected.
func TestChatStreamEncoder_PartialUsageDoesNotOverwritePrompt(t *testing.T) {
	enc := NewStreamEncoder()

	enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaUsage{Usage: protocols.IRUsage{PromptTokens: 100, CacheReadTokens: 30, CacheIncludedInPrompt: true}},
	})
	enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaUsage{Usage: protocols.IRUsage{CompletionTokens: 50}},
	})

	usage := enc.Usage()
	if usage.PromptTokens != 100 {
		t.Errorf("PromptTokens = %d, want 100 — must not be overwritten by a partial completion-only usage chunk", usage.PromptTokens)
	}
	if usage.CompletionTokens != 50 {
		t.Errorf("CompletionTokens = %d, want 50", usage.CompletionTokens)
	}
	if usage.CacheReadTokens != 30 {
		t.Errorf("CacheReadTokens = %d, want 30 (protected by field-level merge)", usage.CacheReadTokens)
	}
	if !usage.CacheIncludedInPrompt {
		t.Error("CacheIncludedInPrompt must stay true (pricing basis is locked to avoid cross-protocol double billing)")
	}
}
