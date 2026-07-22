package chat

import (
	"encoding/json"
	"fmt"
	"github.com/yolorouter/yolorouter/internal/protocols"
	"net/http"
	"strings"
)

// RequestEncoder encodes IR into OpenAI Chat Completions request format.
type RequestEncoder struct{}

func (RequestEncoder) Protocol() protocols.ProtocolID { return protocols.ProtocolOpenAI }

func (RequestEncoder) EgressPath(_ string, _ bool) string {
	return "/v1/chat/completions"
}

func (RequestEncoder) SetupRequest(req *http.Request, apiKey string) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)
}

func (RequestEncoder) EncodeRequest(ir *protocols.IRRequest) (json.RawMessage, error) {
	msgs := make([]interface{}, 0, len(ir.Messages)+1)

	// System message
	if ir.System != "" {
		msgs = append(msgs, map[string]interface{}{
			"role":    "system",
			"content": ir.System,
		})
	}

	// Messages
	for _, msg := range ir.Messages {
		encoded, err := encodeOpenAIMessage(msg)
		if err != nil {
			return nil, fmt.Errorf("encode message: %w", err)
		}
		msgs = append(msgs, encoded...)
	}

	if len(msgs) == 0 {
		msgs = append(msgs, map[string]interface{}{
			"role": "user", "content": "",
		})
	}

	req := map[string]interface{}{
		"model":    ir.Model,
		"messages": msgs,
		"stream":   ir.Stream.Enabled,
	}

	// Generation config
	setIfNotNil(req, "temperature", ir.Generation.Temperature)
	if ir.Generation.MaxTokens != nil && *ir.Generation.MaxTokens > 0 {
		req["max_tokens"] = *ir.Generation.MaxTokens
	}
	setIfNotNil(req, "top_p", ir.Generation.TopP)
	if len(ir.Generation.StopSequences) > 0 {
		req["stop"] = ir.Generation.StopSequences
	}
	setIfNotNil(req, "presence_penalty", ir.Generation.PresencePenalty)
	setIfNotNil(req, "frequency_penalty", ir.Generation.FrequencyPenalty)
	// seed / logprobs / top_logprobs are standard OpenAI Chat params: always forward.
	setIfNotNil(req, "seed", ir.Generation.Seed)
	setIfNotNil(req, "logprobs", ir.Generation.LogProbs)
	setIfNotNil(req, "top_logprobs", ir.Generation.TopLogProbs)
	// Non-standard extended params (top_k / top_a / min_p / repetition_penalty) are only
	// forwarded when the ingress was also OpenAI Chat — i.e. the caller explicitly sent them.
	// In cross-protocol paths (Claude→Chat, Gemini→Chat) they are dropped to avoid sending
	// unknown fields to strict OpenAI-compatible providers.
	if ir.Generation.AllowExtendedParams {
		setIfNotNil(req, "top_k", ir.Generation.TopK)
		setIfNotNil(req, "top_a", ir.Generation.TopA)
		setIfNotNil(req, "min_p", ir.Generation.MinP)
		setIfNotNil(req, "repetition_penalty", ir.Generation.RepetitionPenalty)
	}

	// Tools
	if len(ir.Tools) > 0 {
		tools := make([]interface{}, 0, len(ir.Tools))
		for _, t := range ir.Tools {
			tool := map[string]interface{}{
				"type": "function",
				"function": map[string]interface{}{
					"name":       t.Name,
					"parameters": json.RawMessage(t.Parameters),
				},
			}
			if t.Description != "" {
				tool["function"].(map[string]interface{})["description"] = t.Description
			}
			tools = append(tools, tool)
		}
		req["tools"] = tools
	}

	// Tool choice
	switch ir.ToolChoice {
	case protocols.ToolChoiceNone:
		req["tool_choice"] = "none"
	case protocols.ToolChoiceRequired:
		req["tool_choice"] = "required"
	case protocols.ToolChoiceNamed:
		req["tool_choice"] = map[string]interface{}{
			"type":     "function",
			"function": map[string]string{"name": ir.ToolChoiceName},
		}
	}

	// Reasoning: use explicit effort string when provided; fall back to
	// budget-derived effort only when effort is absent or unknown.
	if ir.Reasoning.Enabled {
		effort := ir.Reasoning.Effort
		if !isKnownReasoningEffort(effort) {
			if ir.Reasoning.BudgetTokens != nil {
				effort = budgetToEffort(*ir.Reasoning.BudgetTokens)
			} else {
				effort = "medium"
			}
		}
		req["reasoning_effort"] = effort
	}

	// Response format
	if ir.ResponseFormat != nil {
		switch ir.ResponseFormat.Type {
		case "json_object":
			req["response_format"] = map[string]interface{}{"type": "json_object"}
		case "json_schema":
			req["response_format"] = map[string]interface{}{
				"type": "json_schema",
				"json_schema": map[string]interface{}{
					"name":   ir.ResponseFormat.Name,
					"schema": json.RawMessage(ir.ResponseFormat.Schema),
					"strict": ir.ResponseFormat.Strict,
				},
			}
		}
	}

	// Stream options
	if ir.Stream.Enabled {
		req["stream_options"] = map[string]interface{}{"include_usage": true}
	}

	data, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal openai request: %w", err)
	}
	return data, nil
}

// encodeOpenAIMessage encodes one IR message into one or more OpenAI Chat
// messages. It returns a slice because a single RoleTool message may carry
// multiple BlockToolResult blocks (e.g. a Gemini ingress merges several
// functionResponse parts from the same content into one message), while
// OpenAI Chat requires each tool_call_id to have its own separate role=tool
// message — they must be split apart individually and never merged into one.
func encodeOpenAIMessage(msg protocols.IRMessage) ([]interface{}, error) {
	switch msg.Role {
	case protocols.RoleSystem:
		return []interface{}{map[string]interface{}{
			"role":    "system",
			"content": blocksToOpenAIContent(msg.Content),
		}}, nil

	case protocols.RoleTool:
		// Generate one role=tool message per BlockToolResult, using each block's
		// own tool_call_id; fall back to msg.ToolCallID when a block has no ID.
		// Merging them would misalign call_id and drop subsequent tool results.
		var toolMsgs []interface{}
		for _, b := range msg.Content {
			tr, ok := b.(protocols.BlockToolResult)
			if !ok {
				continue
			}
			callID := tr.ToolUseID
			if callID == "" {
				callID = msg.ToolCallID
			}
			toolMsgs = append(toolMsgs, map[string]interface{}{
				"role":         "tool",
				"content":      extractTextFromRaw(tr.Content),
				"tool_call_id": callID,
			})
		}
		// Defensive fallback: when there is no BlockToolResult at all,
		// synthesize one message from text content so the tool message
		// is not silently dropped.
		if len(toolMsgs) == 0 {
			toolMsgs = append(toolMsgs, map[string]interface{}{
				"role":         "tool",
				"content":      blocksToOpenAIContent(msg.Content),
				"tool_call_id": msg.ToolCallID,
			})
		}
		return toolMsgs, nil

	case protocols.RoleUser, protocols.RoleAssistant:
		role := "user"
		if msg.Role == protocols.RoleAssistant {
			role = "assistant"
		}

		result := map[string]interface{}{
			"role": role,
		}

		// Content
		reasoning := ""
		var textParts []string
		var contentParts []interface{}

		for _, b := range msg.Content {
			switch v := b.(type) {
			case protocols.BlockText:
				textParts = append(textParts, v.Text)
				contentParts = append(contentParts, map[string]interface{}{
					"type": "text", "text": v.Text,
				})
			case protocols.BlockThinking:
				reasoning += v.Thinking
			case protocols.BlockRedactedThinking:
				// skip
			case protocols.BlockImage:
				url := fmt.Sprintf("data:%s;base64,%s", v.MediaType, v.Data)
				if v.IsURL {
					url = v.Data
				}
				contentParts = append(contentParts, map[string]interface{}{
					"type":      "image_url",
					"image_url": map[string]string{"url": url},
				})
			case protocols.BlockToolUse:
				// Handled via ToolCalls below
			case protocols.BlockToolResult:
				text := extractTextFromRaw(v.Content)
				textParts = append(textParts, text)
				contentParts = append(contentParts, map[string]interface{}{
					"type": "text", "text": text,
				})
			}
		}

		if len(contentParts) > 0 {
			hasComplex := false
			for _, p := range contentParts {
				if _, ok := p.(map[string]interface{}); ok {
					if t := p.(map[string]interface{})["type"]; t != "text" {
						hasComplex = true
						break
					}
				}
			}
			if hasComplex {
				result["content"] = contentParts
			} else {
				result["content"] = strings.Join(textParts, "\n")
			}
		} else if len(msg.ToolCalls) == 0 {
			result["content"] = ""
		}

		// Reasoning content (for assistant messages)
		if role == "assistant" && reasoning != "" {
			result["reasoning_content"] = reasoning
		}

		// Tool calls
		if len(msg.ToolCalls) > 0 || hasToolUseBlocks(msg.Content) {
			tcs := msg.ToolCalls
			if len(tcs) == 0 {
				for _, b := range msg.Content {
					if tu, ok := b.(protocols.BlockToolUse); ok {
						tcs = append(tcs, protocols.IRToolCall{
							ID:        tu.ID,
							Name:      tu.Name,
							Arguments: string(tu.Input),
						})
					}
				}
			}
			toolCalls := make([]interface{}, 0, len(tcs))
			for _, tc := range tcs {
				toolCalls = append(toolCalls, map[string]interface{}{
					"id":   tc.ID,
					"type": "function",
					"function": map[string]interface{}{
						"name":      tc.Name,
						"arguments": tc.Arguments,
					},
				})
			}
			result["tool_calls"] = toolCalls
		}

		return []interface{}{result}, nil

	default:
		return []interface{}{map[string]interface{}{
			"role": "user", "content": "",
		}}, nil
	}
}

func hasToolUseBlocks(blocks []protocols.IRContentBlock) bool {
	for _, b := range blocks {
		if _, ok := b.(protocols.BlockToolUse); ok {
			return true
		}
	}
	return false
}

func blocksToOpenAIContent(blocks []protocols.IRContentBlock) string {
	var texts []string
	for _, b := range blocks {
		if t, ok := b.(protocols.BlockText); ok {
			texts = append(texts, t.Text)
		}
	}
	return strings.Join(texts, "\n")
}

func extractTextFromRaw(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	return string(raw)
}

func setIfNotNil[T any](m map[string]interface{}, key string, v *T) {
	if v != nil {
		m[key] = *v
	}
}
