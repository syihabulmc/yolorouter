package responses

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/yolorouter/yolorouter/internal/protocols"
)

// RequestEncoder encodes an IR request into an OpenAI Responses API request body.
// Used for every outbound direction where the client enters via any protocol
// (Chat/Claude/Gemini/Responses) and the upstream call goes out over /v1/responses.
type RequestEncoder struct{}

func (RequestEncoder) Protocol() protocols.ProtocolID { return protocols.ProtocolResponses }

func (RequestEncoder) EgressPath(_ string, _ bool) string {
	return "/responses"
}

func (RequestEncoder) SetupRequest(req *http.Request, apiKey string) {
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
}

func (RequestEncoder) EncodeRequest(ir *protocols.IRRequest) (json.RawMessage, error) {
	if ir == nil {
		return nil, fmt.Errorf("nil IR request")
	}

	out := map[string]any{
		"model": ir.Model,
	}

	if ir.System != "" {
		out["instructions"] = ir.System
	}

	input, err := encodeResponsesInput(ir.Messages)
	if err != nil {
		return nil, fmt.Errorf("encode input: %w", err)
	}
	out["input"] = input

	if ir.Generation.MaxTokens != nil && *ir.Generation.MaxTokens > 0 {
		out["max_output_tokens"] = *ir.Generation.MaxTokens
	}
	if ir.Generation.Temperature != nil {
		out["temperature"] = *ir.Generation.Temperature
	}
	if ir.Generation.TopP != nil {
		out["top_p"] = *ir.Generation.TopP
	}
	if ir.Stream.Enabled {
		out["stream"] = true
	}

	if ir.Reasoning.Enabled && ir.Reasoning.Effort != "" {
		out["reasoning"] = map[string]any{"effort": ir.Reasoning.Effort}
	}

	if len(ir.Tools) > 0 {
		out["tools"] = encodeResponsesTools(ir.Tools)
	}
	if tc := encodeResponsesToolChoice(ir.ToolChoice, ir.ToolChoiceName); tc != nil {
		out["tool_choice"] = tc
	}

	return json.Marshal(out)
}

// encodeResponsesInput encodes the IR message array into a Responses input array.
// Key rules (consistent with the Claude<->Responses wire conversion rules):
//   - The content part's type depends on role: user/system/tool -> input_text/input_image;
//     replaying assistant history -> output_text. The upstream validates type against role
//     and rejects a mismatch with a 400.
//   - protocols.BlockToolUse -> a standalone function_call item (cannot be embedded in content)
//   - protocols.BlockToolResult -> a standalone function_call_output item
//   - protocols.BlockThinking / protocols.BlockRedactedThinking -> dropped (the previous
//     turn's intermediate reasoning is meaningless for this request)
func encodeResponsesInput(messages []protocols.IRMessage) ([]map[string]any, error) {
	var items []map[string]any

	for _, msg := range messages {
		roleStr := roleToString(msg.Role)

		// Tool messages: emit function_call_output item directly.
		// Tool results are always carried in protocols.BlockToolResult (consistent with the
		// RoleTool branch in chat/codec.go and gemini/encoder.go). We must unwrap them via
		// BlockToolResult here, otherwise function_call_output.output ends up empty and the
		// upstream model never receives any tool result.
		// Note: a single RoleTool message may carry multiple BlockToolResult entries (e.g. the
		// Gemini ingress merges multiple functionResponse parts from the same content into one
		// message); each tool result must produce its own function_call_output with its own
		// call_id, and must not be merged — otherwise a later tool call would lose its
		// corresponding output and the call_ids would get misaligned.
		if msg.Role == protocols.RoleTool {
			emitted := false
			for _, b := range msg.Content {
				tr, ok := b.(protocols.BlockToolResult)
				if !ok {
					continue
				}
				callID := tr.ToolUseID
				if callID == "" {
					callID = msg.ToolCallID
				}
				items = append(items, map[string]any{
					"type":    "function_call_output",
					"call_id": callID,
					"output":  toolResultOutput(tr.Content),
				})
				emitted = true
			}
			// Defensive fallback: if there is no BlockToolResult at all, synthesize an item
			// from BlockText so the tool message isn't dropped entirely.
			if !emitted {
				output := ""
				for _, b := range msg.Content {
					if t, ok := b.(protocols.BlockText); ok {
						output += t.Text
					}
				}
				items = append(items, map[string]any{
					"type":    "function_call_output",
					"call_id": msg.ToolCallID,
					"output":  output,
				})
			}
			continue
		}

		// Assistant messages may carry both ToolCalls and content.
		// Handle content first, then append tool_calls as standalone function_call items.
		contentParts := encodeContentParts(roleStr, msg.Content)
		if len(contentParts) > 0 {
			items = append(items, map[string]any{
				"role":    roleStr,
				"content": contentParts,
			})
		}
		for _, tc := range msg.ToolCalls {
			args := tc.Arguments
			if args == "" {
				args = "{}"
			}
			items = append(items, map[string]any{
				"type":      "function_call",
				"call_id":   tc.ID,
				"name":      tc.Name,
				"arguments": args,
			})
		}
	}

	if len(items) == 0 {
		return nil, fmt.Errorf("no encodable input items")
	}
	return items, nil
}

func encodeContentParts(role string, blocks []protocols.IRContentBlock) []map[string]any {
	var parts []map[string]any
	for _, b := range blocks {
		switch v := b.(type) {
		case protocols.BlockText:
			if v.Text == "" {
				continue
			}
			partType := "input_text"
			if role == "assistant" {
				partType = "output_text"
			}
			parts = append(parts, map[string]any{
				"type": partType,
				"text": v.Text,
			})
		case protocols.BlockImage:
			url := v.Data
			if !v.IsURL {
				url = fmt.Sprintf("data:%s;base64,%s", v.MediaType, v.Data)
			}
			parts = append(parts, map[string]any{
				"type":      "input_image",
				"image_url": url,
			})
		case protocols.BlockToolUse, protocols.BlockToolResult, protocols.BlockThinking, protocols.BlockRedactedThinking:
			// Already handled separately or dropped by the caller.
			continue
		}
	}
	return parts
}

func encodeResponsesTools(tools []protocols.IRToolSpec) []map[string]any {
	out := make([]map[string]any, 0, len(tools))
	for _, t := range tools {
		params := t.Parameters
		if len(params) == 0 {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		item := map[string]any{
			"type":        "function",
			"name":        t.Name,
			"description": t.Description,
			"parameters":  params,
		}
		if t.Strict {
			item["strict"] = true
		}
		out = append(out, item)
	}
	return out
}

func encodeResponsesToolChoice(choice protocols.IRToolChoice, name string) any {
	switch choice {
	case protocols.ToolChoiceAuto:
		return nil
	case protocols.ToolChoiceNone:
		return "none"
	case protocols.ToolChoiceRequired:
		return "required"
	case protocols.ToolChoiceNamed:
		if name == "" {
			return nil
		}
		return map[string]any{"type": "function", "name": name}
	}
	return nil
}

// toolResultOutput converts a tool result's BlockToolResult.Content into the string
// output required by Responses function_call_output. Rules:
//   - empty RawMessage -> ""
//   - a valid JSON string (including empty string "" and null) -> its decoded string
//     value (an empty string stays empty and is never rewritten)
//   - any other structured value (object/array/number, e.g. Gemini's {"temperature":25})
//     -> the raw JSON text passed through unchanged
//
// We don't reuse extractTextFromResponsesContent here because that helper returns ""
// both for "a legitimately empty string" and for "failed to extract from an object",
// which are indistinguishable and would wrongly turn a valid empty tool result into
// literal JSON text.
func toolResultOutput(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if err := json.Unmarshal(raw, &s); err == nil {
		return s // a valid JSON string (including empty string) or null
	}
	return string(raw) // structured value such as object/array, passed through as-is
}

func roleToString(r protocols.Role) string {
	switch r {
	case protocols.RoleSystem:
		return "system"
	case protocols.RoleUser:
		return "user"
	case protocols.RoleAssistant:
		return "assistant"
	case protocols.RoleTool:
		return "tool"
	}
	return "user"
}
