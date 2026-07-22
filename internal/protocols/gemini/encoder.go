package gemini

import (
	"encoding/json"
	"fmt"
	"github.com/yolorouter/yolorouter/internal/protocols"
	"net/http"
	"strings"
)

// RequestEncoder encodes IR into Gemini generateContent request format.
type RequestEncoder struct{}

func (RequestEncoder) Protocol() protocols.ProtocolID { return protocols.ProtocolGemini }

func (RequestEncoder) EgressPath(model string, stream bool) string {
	clean := strings.TrimSuffix(model, "-thinking")
	return fmt.Sprintf("/models/%s:generateContent", clean)
}

func (RequestEncoder) SetupRequest(req *http.Request, apiKey string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("x-goog-api-key", apiKey)
}

func (RequestEncoder) EncodeRequest(ir *protocols.IRRequest) (json.RawMessage, error) {
	// System instruction
	geminiReq := map[string]interface{}{}
	if ir.System != "" {
		geminiReq["systemInstruction"] = map[string]interface{}{
			"parts": []interface{}{map[string]interface{}{"text": ir.System}},
		}
	}

	// Contents
	contents := encodeGeminiMessages(ir.Messages)
	if len(contents) > 0 {
		geminiReq["contents"] = contents
	}

	// Generation config
	gc := map[string]interface{}{}
	if ir.Generation.Temperature != nil {
		gc["temperature"] = *ir.Generation.Temperature
	}
	if ir.Generation.MaxTokens != nil && *ir.Generation.MaxTokens > 0 {
		gc["maxOutputTokens"] = *ir.Generation.MaxTokens
	}
	if ir.Generation.TopP != nil {
		gc["topP"] = *ir.Generation.TopP
	}
	if ir.Generation.TopK != nil {
		gc["topK"] = *ir.Generation.TopK
	}
	if len(ir.Generation.StopSequences) > 0 {
		gc["stopSequences"] = ir.Generation.StopSequences
	}
	if ir.Generation.Seed != nil {
		gc["seed"] = *ir.Generation.Seed
	}
	if ir.Reasoning.Enabled {
		thinkConfig := map[string]interface{}{
			"includeThoughts": true,
		}
		if ir.Reasoning.BudgetTokens != nil {
			thinkConfig["thinkingBudget"] = *ir.Reasoning.BudgetTokens
		}
		gc["thinkingConfig"] = thinkConfig
	}
	if len(gc) > 0 {
		geminiReq["generationConfig"] = gc
	}

	// Tools
	if len(ir.Tools) > 0 {
		functions := make([]interface{}, 0, len(ir.Tools))
		for _, t := range ir.Tools {
			fn := map[string]interface{}{
				"name":       t.Name,
				"parameters": json.RawMessage(t.Parameters),
			}
			if t.Description != "" {
				fn["description"] = t.Description
			}
			functions = append(functions, fn)
		}
		geminiReq["tools"] = []interface{}{
			map[string]interface{}{"functionDeclarations": functions},
		}
	}

	// Tool choice
	if ir.ToolChoice == protocols.ToolChoiceNone || ir.ToolChoice == protocols.ToolChoiceRequired || ir.ToolChoice == protocols.ToolChoiceNamed {
		tc := map[string]interface{}{}
		switch ir.ToolChoice {
		case protocols.ToolChoiceNone:
			tc["mode"] = "NONE"
		case protocols.ToolChoiceRequired:
			tc["mode"] = "ANY"
		case protocols.ToolChoiceNamed:
			tc["mode"] = "ANY"
			tc["allowedFunctionNames"] = []string{ir.ToolChoiceName}
		}
		geminiReq["toolConfig"] = map[string]interface{}{
			"functionCallingConfig": tc,
		}
	}

	// Safety settings
	if len(ir.SafetySettings) > 0 {
		var settings []interface{}
		for _, ss := range ir.SafetySettings {
			settings = append(settings, map[string]interface{}{
				"category":  ss.Category,
				"threshold": ss.Threshold,
			})
		}
		geminiReq["safetySettings"] = settings
	}

	data, err := json.Marshal(geminiReq)
	if err != nil {
		return nil, fmt.Errorf("marshal gemini request: %w", err)
	}
	return data, nil
}

func encodeGeminiMessages(messages []protocols.IRMessage) []interface{} {
	var result []interface{}
	for _, msg := range messages {
		role := "user"
		if msg.Role == protocols.RoleAssistant {
			role = "model"
		}

		parts := encodeGeminiParts(msg)
		if len(parts) == 0 && len(msg.ToolCalls) == 0 {
			continue
		}

		// Tool results need "function" role
		if msg.Role == protocols.RoleTool {
			role = "function"
		}

		content := map[string]interface{}{
			"role":  role,
			"parts": parts,
		}
		result = append(result, content)
	}
	return result
}

func encodeGeminiParts(msg protocols.IRMessage) []interface{} {
	var parts []interface{}

	for _, b := range msg.Content {
		switch v := b.(type) {
		case protocols.BlockText:
			parts = append(parts, map[string]interface{}{"text": v.Text})
		case protocols.BlockThinking:
			parts = append(parts, map[string]interface{}{"text": v.Thinking, "thought": true})
		case protocols.BlockImage:
			if v.IsURL {
				parts = append(parts, map[string]interface{}{
					"fileData": map[string]interface{}{
						"mimeType": v.MediaType,
						"fileUri":  v.Data,
					},
				})
			} else {
				parts = append(parts, map[string]interface{}{
					"inlineData": map[string]interface{}{
						"mimeType": v.MediaType,
						"data":     v.Data,
					},
				})
			}
		case protocols.BlockToolUse:
			var args map[string]interface{}
			if json.Unmarshal(v.Input, &args) != nil {
				args = map[string]interface{}{}
			}
			parts = append(parts, map[string]interface{}{
				"functionCall": map[string]interface{}{
					"name": v.Name,
					"args": args,
				},
			})
		case protocols.BlockToolResult:
			content := extractGeminiToolResultContent(v.Content)
			parts = append(parts, map[string]interface{}{
				"functionResponse": map[string]interface{}{
					"name":     v.ToolUseID,
					"response": map[string]interface{}{"result": content},
				},
			})
		}
	}

	// protocols.IRMessage.ToolCalls (OpenAI format)
	for _, tc := range msg.ToolCalls {
		var args map[string]interface{}
		if json.Unmarshal([]byte(tc.Arguments), &args) != nil {
			args = map[string]interface{}{}
		}
		parts = append(parts, map[string]interface{}{
			"functionCall": map[string]interface{}{
				"name": tc.Name,
				"args": args,
			},
		})
	}

	if len(parts) == 0 && msg.Role == protocols.RoleTool {
		parts = append(parts, map[string]interface{}{"text": ""})
	}
	return parts
}

func extractGeminiToolResultContent(raw json.RawMessage) interface{} {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var obj interface{}
	if json.Unmarshal(raw, &obj) == nil {
		return obj
	}
	return string(raw)
}
