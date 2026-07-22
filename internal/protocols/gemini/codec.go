package gemini

import (
	"encoding/json"
	"fmt"
	"github.com/yolorouter/yolorouter/internal/protocols"
	"strings"
)

// RequestDecoder decodes Gemini generateContent requests into IR.
type RequestDecoder struct{}

func (RequestDecoder) Protocol() protocols.ProtocolID { return protocols.ProtocolGemini }

func (d RequestDecoder) DecodeRequest(body json.RawMessage, model string, isStream bool) (*protocols.IRRequest, error) {
	var req geminiChatRequest
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("parse gemini request: %w", err)
	}

	irReq := &protocols.IRRequest{
		Model:          model,
		Stream:         protocols.IRStreamConfig{Enabled: isStream, IncludeUsage: true},
		Vendor:         protocols.NewVendorBag(),
		SourceProtocol: string(protocols.ProtocolGemini),
		RawBody:        body,
	}

	// System instruction
	if req.SystemInstruction != nil {
		irReq.System = extractGeminiText(req.SystemInstruction.Parts)
	}

	// Messages with tool call ID tracking
	nameToID := make(map[string]string)
	var messages []protocols.IRMessage
	for _, content := range req.Contents {
		msg := decodeGeminiContent(content, nameToID)
		if msg != nil {
			messages = append(messages, *msg)
		}
	}
	irReq.Messages = messages

	// Tools
	if len(req.Tools) > 0 {
		irReq.Tools = decodeGeminiTools(req.Tools)
	}

	// Tool config
	if req.ToolConfig != nil && req.ToolConfig.FunctionCallingConfig != nil {
		cfg := req.ToolConfig.FunctionCallingConfig
		switch cfg.Mode {
		case "NONE":
			irReq.ToolChoice = protocols.ToolChoiceNone
		case "ANY":
			if len(cfg.AllowedFunctionNames) == 1 {
				irReq.ToolChoice = protocols.ToolChoiceNamed
				irReq.ToolChoiceName = cfg.AllowedFunctionNames[0]
			} else {
				irReq.ToolChoice = protocols.ToolChoiceRequired
			}
		default:
			irReq.ToolChoice = protocols.ToolChoiceAuto
		}
	}

	// Generation config
	if req.GenerationConfig != nil {
		gc := req.GenerationConfig
		irReq.Generation = protocols.IRGenerationConfig{
			Temperature:   gc.Temperature,
			MaxTokens:     gc.MaxOutputTokens,
			TopP:          gc.TopP,
			TopK:          gc.TopK,
			Seed:          gc.Seed,
			StopSequences: gc.StopSequences,
		}

		if gc.ThinkingConfig != nil && gc.ThinkingConfig.ThinkingBudget != nil {
			budget := *gc.ThinkingConfig.ThinkingBudget
			irReq.Reasoning = protocols.IRReasoningConfig{
				Enabled:         budget > 0,
				BudgetTokens:    &budget,
				IncludeThoughts: gc.ThinkingConfig.IncludeThoughts != nil && *gc.ThinkingConfig.IncludeThoughts,
			}
		}

		if gc.ResponseMimeType == "application/json" {
			irReq.ResponseFormat = &protocols.IRResponseFormat{
				Type:   "json_object",
				Schema: json.RawMessage(marshalJSON(gc.ResponseSchema)),
			}
		}
	}

	// Safety settings
	for _, ss := range req.SafetySettings {
		irReq.SafetySettings = append(irReq.SafetySettings, protocols.IRSafetySetting{
			Category:  ss.Category,
			Threshold: ss.Threshold,
		})
	}

	// Vendor bag
	if req.CachedContent != "" {
		irReq.Vendor.Set("cachedContent", req.CachedContent)
	}
	if req.GenerationConfig != nil {
		irReq.Vendor.Set("generationConfig", req.GenerationConfig)
	}
	if len(req.SafetySettings) > 0 {
		irReq.Vendor.Set("safetySettings", req.SafetySettings)
	}
	if req.ToolConfig != nil {
		irReq.Vendor.Set("toolConfig", req.ToolConfig)
	}

	return irReq, nil
}

func decodeGeminiContent(content geminiContent, nameToID map[string]string) *protocols.IRMessage {
	role := protocols.RoleUser
	switch content.Role {
	case "model":
		role = protocols.RoleAssistant
	case "":
		role = protocols.RoleUser
	}

	var blocks []protocols.IRContentBlock
	var toolCalls []protocols.IRToolCall
	hasFunctionResponse := false

	for _, part := range content.Parts {
		switch {
		case part.FunctionCall != nil:
			id := "call_" + protocols.RandomString(12)
			nameToID[part.FunctionCall.Name] = id
			blocks = append(blocks, protocols.BlockToolUse{
				ID:    id,
				Name:  part.FunctionCall.Name,
				Input: json.RawMessage(marshalJSON(part.FunctionCall.Args)),
			})
			toolCalls = append(toolCalls, protocols.IRToolCall{
				ID:        id,
				Name:      part.FunctionCall.Name,
				Arguments: marshalJSON(part.FunctionCall.Args),
			})

		case part.FunctionResp != nil:
			hasFunctionResponse = true
			toolUseID := part.FunctionResp.Name
			if generatedID, ok := nameToID[part.FunctionResp.Name]; ok {
				toolUseID = generatedID
			}
			blocks = append(blocks, protocols.BlockToolResult{
				ToolUseID: toolUseID,
				Content:   json.RawMessage(marshalJSON(part.FunctionResp.Response)),
			})

		case part.InlineData != nil:
			blocks = append(blocks, protocols.BlockImage{
				MediaType: part.InlineData.MimeType,
				Data:      part.InlineData.Data,
			})

		case part.Text != "":
			if part.Thought != nil && *part.Thought {
				blocks = append(blocks, protocols.BlockThinking{Thinking: part.Text})
			} else {
				blocks = append(blocks, protocols.BlockText{Text: part.Text})
			}
		}
	}

	if hasFunctionResponse {
		role = protocols.RoleTool
	}

	if len(blocks) == 0 && len(toolCalls) == 0 {
		return nil
	}

	return &protocols.IRMessage{
		Role:      role,
		Content:   blocks,
		ToolCalls: toolCalls,
	}
}

func decodeGeminiTools(rawTools []json.RawMessage) []protocols.IRToolSpec {
	var specs []protocols.IRToolSpec
	for _, raw := range rawTools {
		var tool geminiTool
		if err := json.Unmarshal(raw, &tool); err != nil {
			continue
		}
		for _, fd := range tool.FunctionDeclarations {
			params := fd.Parameters
			if params == nil {
				params = fd.ParametersJsonSchema
			}
			if params == nil {
				params = json.RawMessage("{}")
			}
			specs = append(specs, protocols.IRToolSpec{
				Name:        fd.Name,
				Description: fd.Description,
				Parameters:  params,
			})
		}
	}
	return specs
}

func extractGeminiText(parts []geminiPart) string {
	var texts []string
	for _, p := range parts {
		if p.Text != "" {
			texts = append(texts, p.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func marshalJSON(v interface{}) string {
	data, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(data)
}
