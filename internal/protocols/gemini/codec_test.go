package gemini

import (
	"encoding/json"
	"github.com/yolorouter/yolorouter/internal/protocols"
	"testing"
)

// --- Gemini Request Decoder ---

func TestGeminiDecodeRequest_BasicChat(t *testing.T) {
	body := json.RawMessage(`{
		"contents": [
			{"role": "user", "parts": [{"text": "Hello"}]},
			{"role": "model", "parts": [{"text": "Hi there!"}]},
			{"role": "user", "parts": [{"text": "How are you?"}]}
		]
	}`)

	dec := RequestDecoder{}
	req, err := dec.DecodeRequest(body, "deepseek-chat", true)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}

	if req.Model != "deepseek-chat" {
		t.Errorf("Model = %q, want %q", req.Model, "deepseek-chat")
	}
	if len(req.Messages) != 3 {
		t.Fatalf("Messages count = %d, want 3", len(req.Messages))
	}
	if req.Messages[0].Role != protocols.RoleUser {
		t.Errorf("Message[0] Role = %v, want protocols.RoleUser", req.Messages[0].Role)
	}
	if req.Messages[1].Role != protocols.RoleAssistant {
		t.Errorf("Message[1] Role = %v, want protocols.RoleAssistant", req.Messages[1].Role)
	}
	if req.Stream.Enabled != true {
		t.Error("Stream.Enabled = false, want true")
	}
	if req.SourceProtocol != "gemini" {
		t.Errorf("SourceProtocol = %q, want %q", req.SourceProtocol, "gemini")
	}
}

func TestGeminiDecodeRequest_SystemInstruction(t *testing.T) {
	body := json.RawMessage(`{
		"systemInstruction": {"parts": [{"text": "You are a helpful assistant."}]},
		"contents": [
			{"role": "user", "parts": [{"text": "Hi"}]}
		]
	}`)

	req, err := RequestDecoder{}.DecodeRequest(body, "model", false)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if req.System != "You are a helpful assistant." {
		t.Errorf("System = %q", req.System)
	}
}

func TestGeminiDecodeRequest_GenerationConfig(t *testing.T) {
	body := json.RawMessage(`{
		"contents": [{"role": "user", "parts": [{"text": "test"}]}],
		"generationConfig": {
			"temperature": 0.7,
			"topP": 0.9,
			"topK": 40,
			"maxOutputTokens": 2048,
			"stopSequences": ["stop"],
			"thinkingConfig": {
				"thinkingBudget": 8000,
				"includeThoughts": true
			}
		}
	}`)

	req, err := RequestDecoder{}.DecodeRequest(body, "model", false)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}

	g := req.Generation
	if g.Temperature == nil || *g.Temperature != 0.7 {
		t.Errorf("Temperature = %v, want 0.7", g.Temperature)
	}
	if g.TopP == nil || *g.TopP != 0.9 {
		t.Errorf("TopP = %v, want 0.9", g.TopP)
	}
	if g.TopK == nil || *g.TopK != 40 {
		t.Errorf("TopK = %v, want 40", g.TopK)
	}
	if g.MaxTokens == nil || *g.MaxTokens != 2048 {
		t.Errorf("MaxTokens = %v, want 2048", g.MaxTokens)
	}
	if len(g.StopSequences) != 1 || g.StopSequences[0] != "stop" {
		t.Errorf("StopSequences = %v", g.StopSequences)
	}

	// Reasoning config
	if !req.Reasoning.Enabled {
		t.Error("Reasoning.Enabled = false, want true")
	}
	if req.Reasoning.BudgetTokens == nil || *req.Reasoning.BudgetTokens != 8000 {
		t.Errorf("Reasoning.BudgetTokens = %v", req.Reasoning.BudgetTokens)
	}
	if !req.Reasoning.IncludeThoughts {
		t.Error("Reasoning.IncludeThoughts = false, want true")
	}
}

func TestGeminiDecodeRequest_FunctionCallAndResponse(t *testing.T) {
	body := json.RawMessage(`{
		"contents": [
			{"role": "user", "parts": [{"text": "What's the weather?"}]},
			{"role": "model", "parts": [{"functionCall": {"name": "get_weather", "args": {"city": "Beijing"}}}]},
			{"role": "function", "parts": [{"functionResponse": {"name": "get_weather", "response": {"temperature": 25}}}]},
			{"role": "model", "parts": [{"text": "The temperature in Beijing is 25°C."}]}
		]
	}`)

	req, err := RequestDecoder{}.DecodeRequest(body, "model", false)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}

	if len(req.Messages) != 4 {
		t.Fatalf("Messages count = %d, want 4", len(req.Messages))
	}

	// Message[1]: model function call
	msg1 := req.Messages[1]
	if msg1.Role != protocols.RoleAssistant {
		t.Errorf("Message[1] Role = %v, want protocols.RoleAssistant", msg1.Role)
	}
	foundToolUse := false
	for _, b := range msg1.Content {
		if tu, ok := b.(protocols.BlockToolUse); ok {
			foundToolUse = true
			if tu.Name != "get_weather" {
				t.Errorf("ToolUse Name = %q", tu.Name)
			}
			if tu.ID == "" {
				t.Error("ToolUse ID is empty, should be auto-generated")
			}
		}
	}
	if !foundToolUse {
		t.Error("No protocols.BlockToolUse found in model function call message")
	}

	// Message[2]: function response → should be protocols.RoleTool with correct ToolUseID
	msg2 := req.Messages[2]
	if msg2.Role != protocols.RoleTool {
		t.Errorf("Message[2] Role = %v, want protocols.RoleTool", msg2.Role)
	}
	foundToolResult := false
	for _, b := range msg2.Content {
		if tr, ok := b.(protocols.BlockToolResult); ok {
			foundToolResult = true
			if tr.ToolUseID == "" {
				t.Error("protocols.BlockToolResult ToolUseID is empty")
			}
			// ToolUseID should match the generated ID from the function call
			if tr.ToolUseID == "get_weather" {
				t.Error("protocols.BlockToolResult ToolUseID = function name, should be generated ID")
			}
		}
	}
	if !foundToolResult {
		t.Error("No protocols.BlockToolResult found in function response message")
	}
}

func TestGeminiDecodeRequest_ToolConfig(t *testing.T) {
	tests := []struct {
		name     string
		body     string
		expected protocols.IRToolChoice
	}{
		{
			"AUTO mode",
			`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"toolConfig":{"functionCallingConfig":{"mode":"AUTO"}}}`,
			protocols.ToolChoiceAuto,
		},
		{
			"NONE mode",
			`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"toolConfig":{"functionCallingConfig":{"mode":"NONE"}}}`,
			protocols.ToolChoiceNone,
		},
		{
			"ANY mode with single function",
			`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["get_weather"]}}}`,
			protocols.ToolChoiceNamed,
		},
		{
			"ANY mode with multiple functions",
			`{"contents":[{"role":"user","parts":[{"text":"hi"}]}],"toolConfig":{"functionCallingConfig":{"mode":"ANY","allowedFunctionNames":["fn1","fn2"]}}}`,
			protocols.ToolChoiceRequired,
		},
	}

	dec := RequestDecoder{}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			req, err := dec.DecodeRequest(json.RawMessage(tt.body), "model", false)
			if err != nil {
				t.Fatalf("DecodeRequest: %v", err)
			}
			if req.ToolChoice != tt.expected {
				t.Errorf("ToolChoice = %v, want %v", req.ToolChoice, tt.expected)
			}
		})
	}
}

func TestGeminiDecodeRequest_Tools(t *testing.T) {
	body := json.RawMessage(`{
		"contents": [{"role": "user", "parts": [{"text": "test"}]}],
		"tools": [{"functionDeclarations": [
			{"name": "get_weather", "description": "Get weather", "parameters": {"type": "object", "properties": {"city": {"type": "string"}}}},
			{"name": "get_time", "parameters": null}
		]}]
	}`)

	req, err := RequestDecoder{}.DecodeRequest(body, "model", false)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if len(req.Tools) != 2 {
		t.Fatalf("Tools count = %d, want 2", len(req.Tools))
	}
	if req.Tools[0].Name != "get_weather" {
		t.Errorf("Tools[0].Name = %q", req.Tools[0].Name)
	}
	if req.Tools[0].Description != "Get weather" {
		t.Errorf("Tools[0].Description = %q", req.Tools[0].Description)
	}
	if req.Tools[1].Parameters == nil || string(req.Tools[1].Parameters) == "" {
		t.Error("Tools[1].Parameters should default to {}")
	}
}

func TestGeminiDecodeRequest_ThinkingParts(t *testing.T) {
	body := json.RawMessage(`{
		"contents": [
			{"role": "model", "parts": [
				{"text": "Let me think about this...", "thought": true},
				{"text": "Here is my answer."}
			]}
		]
	}`)

	req, err := RequestDecoder{}.DecodeRequest(body, "model", false)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if len(req.Messages) != 1 {
		t.Fatalf("Messages count = %d, want 1", len(req.Messages))
	}

	msg := req.Messages[0]
	if len(msg.Content) != 2 {
		t.Fatalf("Content blocks = %d, want 2", len(msg.Content))
	}

	thinking, ok := msg.Content[0].(protocols.BlockThinking)
	if !ok {
		t.Fatal("Content[0] is not protocols.BlockThinking")
	}
	if thinking.Thinking != "Let me think about this..." {
		t.Errorf("Thinking text = %q", thinking.Thinking)
	}

	text, ok := msg.Content[1].(protocols.BlockText)
	if !ok {
		t.Fatal("Content[1] is not protocols.BlockText")
	}
	if text.Text != "Here is my answer." {
		t.Errorf("Text = %q", text.Text)
	}
}

func TestGeminiDecodeRequest_ImageParts(t *testing.T) {
	body := json.RawMessage(`{
		"contents": [
			{"role": "user", "parts": [
				{"text": "What's in this image?"},
				{"inlineData": {"mimeType": "image/png", "data": "base64data"}}
			]}
		]
	}`)

	req, err := RequestDecoder{}.DecodeRequest(body, "model", false)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}

	msg := req.Messages[0]
	if len(msg.Content) != 2 {
		t.Fatalf("Content blocks = %d, want 2", len(msg.Content))
	}

	img, ok := msg.Content[1].(protocols.BlockImage)
	if !ok {
		t.Fatal("Content[1] is not protocols.BlockImage")
	}
	if img.MediaType != "image/png" {
		t.Errorf("MediaType = %q", img.MediaType)
	}
	if img.Data != "base64data" {
		t.Errorf("Data = %q", img.Data)
	}
}

func TestGeminiDecodeRequest_InvalidJSON(t *testing.T) {
	_, err := RequestDecoder{}.DecodeRequest(json.RawMessage(`{invalid}`), "model", false)
	if err == nil {
		t.Error("Expected error for invalid JSON")
	}
}

func TestGeminiDecodeRequest_VendorBag(t *testing.T) {
	body := json.RawMessage(`{
		"contents": [{"role": "user", "parts": [{"text": "test"}]}],
		"cachedContent": "cached-123",
		"safetySettings": [{"category": "HARM_CATEGORY_HARASSMENT", "threshold": "BLOCK_NONE"}]
	}`)

	req, err := RequestDecoder{}.DecodeRequest(body, "model", false)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}

	if _, ok := req.Vendor.Get("cachedContent"); !ok {
		t.Error("Vendor should have cachedContent")
	}
	if _, ok := req.Vendor.Get("safetySettings"); !ok {
		t.Error("Vendor should have safetySettings")
	}
}

func TestGeminiDecodeRequest_ParametersJsonSchema(t *testing.T) {
	// Gemini CLI (google-genai-sdk >= 1.30.0) sends parametersJsonSchema instead of parameters.
	body := json.RawMessage(`{
		"contents": [{"role": "user", "parts": [{"text": "test"}]}],
		"tools": [{"functionDeclarations": [
			{"name": "update_topic", "description": "desc", "parametersJsonSchema": {"type": "object", "properties": {"title": {"type": "string"}}, "required": ["title"]}},
			{"name": "both_fields", "parameters": {"type": "object"}, "parametersJsonSchema": {"type": "object", "properties": {"x": {"type": "string"}}}}
		]}]
	}`)

	req, err := RequestDecoder{}.DecodeRequest(body, "gemini-3.5-flash", false)
	if err != nil {
		t.Fatalf("DecodeRequest: %v", err)
	}
	if len(req.Tools) != 2 {
		t.Fatalf("Tools count = %d, want 2", len(req.Tools))
	}

	// parametersJsonSchema should be used when parameters is absent
	tool0 := req.Tools[0]
	if tool0.Name != "update_topic" {
		t.Errorf("Tools[0].Name = %q", tool0.Name)
	}
	if string(tool0.Parameters) != `{"type": "object", "properties": {"title": {"type": "string"}}, "required": ["title"]}` {
		t.Errorf("Tools[0].Parameters = %s (expected parametersJsonSchema content)", tool0.Parameters)
	}

	// parameters takes priority over parametersJsonSchema when both present
	tool1 := req.Tools[1]
	if string(tool1.Parameters) != `{"type": "object"}` {
		t.Errorf("Tools[1].Parameters = %s (expected parameters to take priority)", tool1.Parameters)
	}
}

// --- Extended params ---

func TestGeminiEncodeRequest_Seed(t *testing.T) {
	seed := int64(123)
	irReq := &protocols.IRRequest{
		Model: "gemini-2.0-flash",
		Messages: []protocols.IRMessage{
			{Role: protocols.RoleUser, Content: []protocols.IRContentBlock{protocols.BlockText{Text: "Hi"}}},
		},
		Generation: protocols.IRGenerationConfig{
			Seed: &seed,
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

	gc, ok := req["generationConfig"].(map[string]interface{})
	if !ok {
		t.Fatal("generationConfig missing")
	}
	if gc["seed"] != float64(123) {
		t.Errorf("seed = %v, want 123", gc["seed"])
	}
}

func TestGeminiEncodeRequest_SeedNilOmitted(t *testing.T) {
	irReq := &protocols.IRRequest{
		Model: "gemini-2.0-flash",
		Messages: []protocols.IRMessage{
			{Role: protocols.RoleUser, Content: []protocols.IRContentBlock{protocols.BlockText{Text: "Hi"}}},
		},
		Generation: protocols.IRGenerationConfig{},
		Stream:     protocols.IRStreamConfig{Enabled: false},
	}

	data, _ := RequestEncoder{}.EncodeRequest(irReq)
	var req map[string]interface{}
	_ = json.Unmarshal(data, &req)

	// generationConfig should not exist (or not have seed) when all generation fields are nil
	if gc, ok := req["generationConfig"].(map[string]interface{}); ok {
		if _, hasSeed := gc["seed"]; hasSeed {
			t.Error("seed key should not appear when Seed is nil")
		}
	}
}
