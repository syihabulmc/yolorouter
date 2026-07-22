package claude

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/yolorouter/yolorouter/internal/protocols"
)

// RequestDecoder parses an Anthropic Messages API request body (POST /v1/messages) into IR.
// It handles ingress decoding when the client enters via /v1/messages and the upstream
// requires a different protocol.
type RequestDecoder struct{}

func (RequestDecoder) Protocol() protocols.ProtocolID { return protocols.ProtocolClaude }

// claudeRequestWire mirrors the Claude wire request format (decoder-only).
type claudeRequestWire struct {
	Model         string              `json:"model"`
	System        json.RawMessage     `json:"system,omitempty"`
	Messages      []claudeMessageWire `json:"messages"`
	MaxTokens     int                 `json:"max_tokens"`
	Temperature   *float64            `json:"temperature,omitempty"`
	TopP          *float64            `json:"top_p,omitempty"`
	TopK          *int                `json:"top_k,omitempty"`
	StopSequences []string            `json:"stop_sequences,omitempty"`
	Stream        bool                `json:"stream,omitempty"`
	Tools         []claudeToolWire    `json:"tools,omitempty"`
	ToolChoice    json.RawMessage     `json:"tool_choice,omitempty"`
	Thinking      *claudeThinkingWire `json:"thinking,omitempty"`
}

type claudeMessageWire struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"`
}

type claudeContentBlockWire struct {
	Type      string             `json:"type"`
	Text      string             `json:"text,omitempty"`
	ID        string             `json:"id,omitempty"`
	Name      string             `json:"name,omitempty"`
	Input     json.RawMessage    `json:"input,omitempty"`
	ToolUseID string             `json:"tool_use_id,omitempty"`
	Content   json.RawMessage    `json:"content,omitempty"`
	IsError   bool               `json:"is_error,omitempty"`
	Thinking  string             `json:"thinking,omitempty"`
	Source    *claudeImageSource `json:"source,omitempty"`
}

type claudeImageSource struct {
	Type      string `json:"type"`
	MediaType string `json:"media_type,omitempty"`
	Data      string `json:"data,omitempty"`
	URL       string `json:"url,omitempty"`
}

type claudeToolWire struct {
	Type        string          `json:"type,omitempty"`
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"input_schema,omitempty"`
}

type claudeThinkingWire struct {
	Type         string `json:"type"`
	BudgetTokens int    `json:"budget_tokens,omitempty"`
}

func (RequestDecoder) DecodeRequest(body json.RawMessage, model string, isStream bool) (*protocols.IRRequest, error) {
	var wire claudeRequestWire
	if err := json.Unmarshal(body, &wire); err != nil {
		return nil, fmt.Errorf("parse claude request: %w", err)
	}

	ir := &protocols.IRRequest{
		Model:  model,
		Stream: protocols.IRStreamConfig{Enabled: isStream || wire.Stream},
		Generation: protocols.IRGenerationConfig{
			Temperature:   wire.Temperature,
			TopP:          wire.TopP,
			TopK:          wire.TopK,
			StopSequences: wire.StopSequences,
		},
		SourceProtocol: "anthropic",
	}
	if wire.MaxTokens > 0 {
		mt := wire.MaxTokens
		ir.Generation.MaxTokens = &mt
	}

	// system: either a string or an array of content blocks
	ir.System = extractClaudeSystemText(wire.System)

	// messages
	messages, err := decodeClaudeMessages(wire.Messages)
	if err != nil {
		return nil, fmt.Errorf("decode messages: %w", err)
	}
	ir.Messages = messages

	// tools
	if len(wire.Tools) > 0 {
		ir.Tools = decodeClaudeTools(wire.Tools)
	}
	if len(wire.ToolChoice) > 0 {
		ir.ToolChoice, ir.ToolChoiceName = decodeClaudeToolChoice(wire.ToolChoice)
	}

	// thinking -> reasoning: populate both BudgetTokens and Effort (mapped to a tier
	// supported by the target model) so the downstream Responses encoder can emit
	// reasoning.effort for the upstream request.
	if wire.Thinking != nil && wire.Thinking.Type == "enabled" {
		ir.Reasoning = protocols.IRReasoningConfig{
			Enabled:      true,
			BudgetTokens: &wire.Thinking.BudgetTokens,
			Effort:       claudeBudgetTokensToEffort(wire.Thinking.BudgetTokens, model),
		}
	}

	return ir, nil
}

// claudeBudgetTokensToEffort maps a Claude thinking.budget_tokens value to a Responses
// reasoning.effort tier.
// Rule: <=1000 -> low; <50000 -> medium; >=50000 -> high.
// "pro" series models do not accept low and are floored at medium.
func claudeBudgetTokensToEffort(budget int, model string) string {
	var effort string
	switch {
	case budget <= 1000:
		effort = "low"
	case budget < 50000:
		effort = "medium"
	default:
		effort = "high"
	}
	if effort == "low" && isProReasoningModelForResponses(model) {
		return "medium"
	}
	return effort
}

// isProReasoningModelForResponses reports whether the model belongs to the
// gpt-5.*-pro / o*-pro family. "pro" must be its own "-" separated segment
// (gpt-5.4-pro, o3-pro) so it doesn't falsely match -prod or -proxy.
func isProReasoningModelForResponses(model string) bool {
	for _, seg := range strings.Split(strings.ToLower(model), "-") {
		if seg == "pro" {
			return true
		}
	}
	return false
}

func extractClaudeSystemText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	// try string form first
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	// then try an array of content blocks: concatenate every type==text block
	var blocks []claudeContentBlockWire
	if json.Unmarshal(raw, &blocks) == nil {
		var b strings.Builder
		for _, block := range blocks {
			if block.Type == "text" && block.Text != "" {
				if b.Len() > 0 {
					b.WriteString("\n\n")
				}
				b.WriteString(block.Text)
			}
		}
		return b.String()
	}
	return ""
}

func decodeClaudeMessages(wires []claudeMessageWire) ([]protocols.IRMessage, error) {
	var out []protocols.IRMessage
	for _, msg := range wires {
		role := parseRole(msg.Role)

		// content may be either a string or an array of content blocks
		var text string
		if json.Unmarshal(msg.Content, &text) == nil {
			out = append(out, protocols.IRMessage{
				Role:    role,
				Content: []protocols.IRContentBlock{protocols.BlockText{Text: text}},
			})
			continue
		}

		var blocks []claudeContentBlockWire
		if err := json.Unmarshal(msg.Content, &blocks); err != nil {
			return nil, fmt.Errorf("parse message content: %w", err)
		}

		// Claude's tool_use and tool_result blocks may be interleaved with regular
		// content in the same message. IR design: fold them into the same
		// protocols.IRMessage's Content + ToolCalls.
		// Note: role=user + tool_result is converted into a separate role=tool message in IR.
		var current protocols.IRMessage
		current.Role = role
		hasContent := false
		var pendingToolResults []protocols.IRMessage

		for _, b := range blocks {
			switch b.Type {
			case "text":
				if b.Text == "" {
					continue
				}
				current.Content = append(current.Content, protocols.BlockText{Text: b.Text})
				hasContent = true
			case "image":
				if b.Source == nil {
					continue
				}
				img := protocols.BlockImage{MediaType: b.Source.MediaType}
				if b.Source.URL != "" {
					img.Data = b.Source.URL
					img.IsURL = true
				} else {
					img.Data = b.Source.Data
				}
				current.Content = append(current.Content, img)
				hasContent = true
			case "tool_use":
				current.ToolCalls = append(current.ToolCalls, protocols.IRToolCall{
					ID:        b.ID,
					Name:      b.Name,
					Arguments: string(b.Input),
				})
				hasContent = true
			case "tool_result":
				// Converted into a separate protocols.RoleTool message.
				// Important: the result text must be wrapped in protocols.BlockToolResult
				// (not BlockText). The downstream egress encoders (the RoleTool branch in
				// chat/codec.go and gemini/encoder.go) only type-assert BlockToolResult to
				// extract the content; a BlockText would be silently ignored, leaving the
				// tool message content empty for the OpenAI/Gemini upstream request -- the
				// model would never see any tool result. This keeps behavior consistent
				// with the chat/responses/gemini ingress decoders, which all produce
				// BlockToolResult.
				text := extractClaudeToolResultText(b.Content)
				contentJSON, _ := json.Marshal(text) // normalize to a JSON string so egress's extractTextFromRaw can cleanly extract it
				pendingToolResults = append(pendingToolResults, protocols.IRMessage{
					Role:       protocols.RoleTool,
					ToolCallID: b.ToolUseID,
					Content: []protocols.IRContentBlock{
						protocols.BlockToolResult{
							ToolUseID: b.ToolUseID,
							Content:   json.RawMessage(contentJSON),
							IsError:   b.IsError,
						},
					},
				})
			case "thinking":
				if b.Thinking == "" {
					continue
				}
				current.Content = append(current.Content, protocols.BlockThinking{Thinking: b.Thinking})
				hasContent = true
			}
		}

		if hasContent {
			out = append(out, current)
		}
		// tool_result blocks are appended as separate protocols.RoleTool messages
		out = append(out, pendingToolResults...)
	}
	return out, nil
}

func extractClaudeToolResultText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var blocks []claudeContentBlockWire
	if json.Unmarshal(raw, &blocks) == nil {
		var b strings.Builder
		for _, block := range blocks {
			if block.Type == "text" && block.Text != "" {
				b.WriteString(block.Text)
			}
		}
		return b.String()
	}
	return string(raw)
}

func decodeClaudeTools(wires []claudeToolWire) []protocols.IRToolSpec {
	out := make([]protocols.IRToolSpec, 0, len(wires))
	for _, t := range wires {
		out = append(out, protocols.IRToolSpec{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  t.InputSchema,
		})
	}
	return out
}

func decodeClaudeToolChoice(raw json.RawMessage) (protocols.IRToolChoice, string) {
	if len(raw) == 0 {
		return protocols.ToolChoiceAuto, ""
	}
	var obj struct {
		Type string `json:"type"`
		Name string `json:"name"`
	}
	if err := json.Unmarshal(raw, &obj); err != nil {
		return protocols.ToolChoiceAuto, ""
	}
	switch obj.Type {
	case "any":
		return protocols.ToolChoiceRequired, ""
	case "none":
		return protocols.ToolChoiceNone, ""
	case "tool":
		return protocols.ToolChoiceNamed, obj.Name
	}
	return protocols.ToolChoiceAuto, ""
}

func parseRole(s string) protocols.Role {
	switch strings.ToLower(s) {
	case "system":
		return protocols.RoleSystem
	case "user":
		return protocols.RoleUser
	case "assistant":
		return protocols.RoleAssistant
	case "tool":
		return protocols.RoleTool
	}
	return protocols.RoleUser
}
