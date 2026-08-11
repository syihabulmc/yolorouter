package chat

import (
	"encoding/json"
	"fmt"
	"github.com/yolorouter/yolorouter/internal/protocols"
	"strings"
)

// RequestDecoder decodes OpenAI Chat Completions requests into IR.
type RequestDecoder struct{}

func (RequestDecoder) Protocol() protocols.ProtocolID { return protocols.ProtocolOpenAI }

func (d RequestDecoder) DecodeRequest(body json.RawMessage, model string, isStream bool) (*protocols.IRRequest, error) {
	var req struct {
		Model       string          `json:"model"`
		Messages    json.RawMessage `json:"messages"`
		Stream      bool            `json:"stream"`
		Temperature *float64        `json:"temperature,omitempty"`
		MaxTokens   *int            `json:"max_tokens,omitempty"`
		// MaxCompletionTokens is the same ceiling under the name the reasoning
		// models require and current SDKs send. Reading only max_tokens drops
		// the ceiling of exactly the newest and most expensive requests, and it
		// disappears silently: the field is simply absent from whatever this
		// request is re-encoded into.
		MaxCompletionTokens *int     `json:"max_completion_tokens,omitempty"`
		TopP                *float64 `json:"top_p,omitempty"`
		TopK                *int     `json:"top_k,omitempty"`
		TopA                *float64 `json:"top_a,omitempty"`
		MinP                *float64 `json:"min_p,omitempty"`
		Seed                *int64   `json:"seed,omitempty"`
		// Stop is OpenAI's "stop" field, which the API accepts as EITHER a
		// single string OR an array of strings — decoded via decodeStopSequences
		// below rather than a fixed shape, since a plain json.RawMessage array
		// element type would reject the scalar form outright.
		Stop              json.RawMessage `json:"stop,omitempty"`
		PresencePenalty   *float64        `json:"presence_penalty,omitempty"`
		FrequencyPenalty  *float64        `json:"frequency_penalty,omitempty"`
		RepetitionPenalty *float64        `json:"repetition_penalty,omitempty"`
		LogProbs          *bool           `json:"logprobs,omitempty"`
		TopLogProbs       *int            `json:"top_logprobs,omitempty"`
		Tools             json.RawMessage `json:"tools,omitempty"`
		ToolChoice        json.RawMessage `json:"tool_choice,omitempty"`
		ResponseFormat    json.RawMessage `json:"response_format,omitempty"`
		StreamOptions     *struct {
			IncludeUsage bool `json:"include_usage"`
		} `json:"stream_options,omitempty"`
		ReasoningEffort string `json:"reasoning_effort,omitempty"`
		Reasoning       *struct {
			BudgetTokens *int    `json:"budget_tokens,omitempty"`
			Effort       *string `json:"effort,omitempty"`
		} `json:"reasoning,omitempty"`
	}

	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("parse openai request: %w", err)
	}

	irReq := &protocols.IRRequest{
		Model:          model,
		Stream:         protocols.IRStreamConfig{Enabled: isStream, IncludeUsage: req.StreamOptions != nil && req.StreamOptions.IncludeUsage},
		Vendor:         protocols.NewVendorBag(),
		SourceProtocol: string(protocols.ProtocolOpenAI),
		RawBody:        body,
	}

	// Messages: role:"system" entries are extracted into IRRequest.System (mirroring the
	// responses/claude/gemini decoders) rather than kept as RoleSystem entries in Messages,
	// so that egress encoders that only read IRRequest.System (claude, gemini) see them.
	messages, system := decodeOpenAIMessages(req.Messages)
	irReq.Messages = messages
	irReq.System = system

	// Generation config
	// AllowExtendedParams=true: ingress is OpenAI Chat, so non-standard extended params
	// (top_k/top_a/min_p/repetition_penalty) were explicitly sent by the caller and should
	// be forwarded to the egress provider as-is.
	irReq.Generation = protocols.IRGenerationConfig{
		Temperature:         req.Temperature,
		MaxTokens:           pickCeiling(req.MaxTokens, req.MaxCompletionTokens),
		TopP:                req.TopP,
		TopK:                req.TopK,
		TopA:                req.TopA,
		MinP:                req.MinP,
		Seed:                req.Seed,
		PresencePenalty:     req.PresencePenalty,
		FrequencyPenalty:    req.FrequencyPenalty,
		RepetitionPenalty:   req.RepetitionPenalty,
		LogProbs:            req.LogProbs,
		TopLogProbs:         req.TopLogProbs,
		AllowExtendedParams: true,
	}
	irReq.Generation.StopSequences = decodeStopSequences(req.Stop)

	// Tools
	irReq.Tools = decodeOpenAITools(req.Tools)
	irReq.ToolChoice, irReq.ToolChoiceName = decodeOpenAIToolChoice(req.ToolChoice)

	// Reasoning: support both reasoning_effort string and reasoning object ({budget_tokens, effort}).
	// reasoning object takes priority over reasoning_effort string when both are present.
	// Validation: only enable when effort is one of known values OR budget_tokens > 0.
	if req.Reasoning != nil {
		validEffort := req.Reasoning.Effort != nil && isKnownReasoningEffort(*req.Reasoning.Effort)
		validBudget := req.Reasoning.BudgetTokens != nil && *req.Reasoning.BudgetTokens > 0
		if validEffort || validBudget {
			rc := protocols.IRReasoningConfig{Enabled: true}
			if validEffort {
				rc.Effort = *req.Reasoning.Effort
			} else if validBudget {
				// budget-only: derive the effort string from the budget so that
				// cross-protocol conversion (e.g. Chat -> Responses egress) can
				// encode it correctly.
				rc.Effort = budgetToEffort(*req.Reasoning.BudgetTokens)
			}
			if validBudget {
				rc.BudgetTokens = req.Reasoning.BudgetTokens
			} else {
				// Derive budget from effort when no explicit budget provided
				budget := effortToBudget(rc.Effort)
				rc.BudgetTokens = &budget
			}
			irReq.Reasoning = rc
		}
	}
	if !irReq.Reasoning.Enabled && req.ReasoningEffort != "" && isKnownReasoningEffort(req.ReasoningEffort) {
		budget := effortToBudget(req.ReasoningEffort)
		irReq.Reasoning = protocols.IRReasoningConfig{
			Enabled:      true,
			BudgetTokens: &budget,
			Effort:       req.ReasoningEffort,
		}
	}

	// Response format
	if req.ResponseFormat != nil {
		irReq.ResponseFormat = decodeOpenAIResponseFormat(req.ResponseFormat)
	}

	return irReq, nil
}

// decodeStopSequences decodes OpenAI's "stop" field, which the API accepts as
// EITHER a single string OR an array of strings. A single string yields one
// stop sequence; an array yields one entry per string element (non-string
// elements are ignored, matching the previous array-only decoder's leniency);
// an absent or null field yields no stop sequences.
func decodeStopSequences(raw json.RawMessage) []string {
	if len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var single string
	if json.Unmarshal(raw, &single) == nil {
		return []string{single}
	}
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) != nil {
		return nil
	}
	var out []string
	for _, s := range arr {
		var str string
		if json.Unmarshal(s, &str) == nil {
			out = append(out, str)
		}
	}
	return out
}

// decodeOpenAIMessages decodes the OpenAI Chat "messages" array into IR messages.
// It also extracts any role:"system" messages into a separate system string
// (joined with "\n\n" if there is more than one) instead of leaving them as
// RoleSystem entries in the returned messages, matching the other protocol decoders.
func decodeOpenAIMessages(raw json.RawMessage) ([]protocols.IRMessage, string) {
	type openaiMessage struct {
		Role             string          `json:"role"`
		Content          json.RawMessage `json:"content"`
		ReasoningContent string          `json:"reasoning_content,omitempty"`
		ToolCalls        json.RawMessage `json:"tool_calls,omitempty"`
		ToolCallID       string          `json:"tool_call_id,omitempty"`
	}

	var msgs []openaiMessage
	if json.Unmarshal(raw, &msgs) != nil {
		return nil, ""
	}

	var result []protocols.IRMessage
	var systemParts []string
	for _, m := range msgs {
		irMsg := protocols.IRMessage{ToolCallID: m.ToolCallID}

		switch m.Role {
		case "system", "developer":
			// OpenAI's "developer" role carries the same system-level
			// instruction precedence as "system" (it replaced "system" for
			// o1-class models); coercing it to a user message via the
			// default branch would lose that precedence on cross-protocol
			// routing, so it is folded into System exactly like "system".
			if text := extractOpenAISystemText(m.Content); text != "" {
				systemParts = append(systemParts, text)
			}
			continue
		case "tool":
			irMsg.Role = protocols.RoleTool
			irMsg.Content = []protocols.IRContentBlock{
				protocols.BlockToolResult{
					ToolUseID: m.ToolCallID,
					Content:   m.Content,
				},
			}
		case "assistant":
			irMsg.Role = protocols.RoleAssistant
			irMsg.Content = decodeOpenAIAssistantContent(m.Content, m.ReasoningContent)
			irMsg.ToolCalls = decodeOpenAIToolCalls(m.ToolCalls)
		default: // "user"
			irMsg.Role = protocols.RoleUser
			irMsg.Content = decodeOpenAIUserContent(m.Content)
		}

		result = append(result, irMsg)
	}
	return result, strings.Join(systemParts, "\n\n")
}

// extractOpenAISystemText extracts plain text from an OpenAI message "content" field,
// which may be either a plain string or an array of content parts (e.g. [{"type":"text","text":"..."}]).
func extractOpenAISystemText(raw json.RawMessage) string {
	if len(raw) == 0 {
		return ""
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	if json.Unmarshal(raw, &parts) == nil {
		var texts []string
		for _, p := range parts {
			if p.Text != "" {
				texts = append(texts, p.Text)
			}
		}
		return strings.Join(texts, "")
	}
	return ""
}

func decodeOpenAIAssistantContent(content json.RawMessage, reasoning string) []protocols.IRContentBlock {
	var blocks []protocols.IRContentBlock

	if reasoning != "" {
		blocks = append(blocks, protocols.BlockThinking{Thinking: reasoning})
	}

	// Try string content first
	var str string
	if json.Unmarshal(content, &str) == nil {
		if str != "" {
			blocks = append(blocks, protocols.BlockText{Text: str})
		}
		return blocks
	}

	// Array content
	var parts []map[string]interface{}
	if json.Unmarshal(content, &parts) != nil {
		return blocks
	}

	for _, p := range parts {
		t, _ := p["type"].(string)
		switch t {
		case "text":
			if text, ok := p["text"].(string); ok && text != "" {
				blocks = append(blocks, protocols.BlockText{Text: text})
			}
		case "image_url":
			if img := parseImageURLPart(p); img != nil {
				blocks = append(blocks, *img)
			}
		}
	}
	return blocks
}

func decodeOpenAIUserContent(content json.RawMessage) []protocols.IRContentBlock {
	var str string
	if json.Unmarshal(content, &str) == nil {
		return []protocols.IRContentBlock{protocols.BlockText{Text: str}}
	}

	var parts []map[string]interface{}
	if json.Unmarshal(content, &parts) != nil {
		return []protocols.IRContentBlock{protocols.BlockText{Text: string(content)}}
	}

	var blocks []protocols.IRContentBlock
	for _, p := range parts {
		t, _ := p["type"].(string)
		switch t {
		case "text":
			if text, ok := p["text"].(string); ok {
				blocks = append(blocks, protocols.BlockText{Text: text})
			}
		case "image_url":
			if img := parseImageURLPart(p); img != nil {
				blocks = append(blocks, *img)
			}
		default:
			// Pass through as text
			if text, ok := p["text"].(string); ok {
				blocks = append(blocks, protocols.BlockText{Text: text})
			}
		}
	}
	if len(blocks) == 0 {
		blocks = append(blocks, protocols.BlockText{Text: string(content)})
	}
	return blocks
}

func decodeOpenAIToolCalls(raw json.RawMessage) []protocols.IRToolCall {
	if len(raw) == 0 {
		return nil
	}
	type toolCall struct {
		ID       string `json:"id"`
		Type     string `json:"type"`
		Function struct {
			Name      string `json:"name"`
			Arguments string `json:"arguments"`
		} `json:"function"`
	}
	var tcs []toolCall
	if json.Unmarshal(raw, &tcs) != nil {
		return nil
	}
	result := make([]protocols.IRToolCall, len(tcs))
	for i, tc := range tcs {
		result[i] = protocols.IRToolCall{ID: tc.ID, Name: tc.Function.Name, Arguments: tc.Function.Arguments}
	}
	return result
}

func decodeOpenAITools(raw json.RawMessage) []protocols.IRToolSpec {
	if len(raw) == 0 {
		return nil
	}
	var tools []struct {
		Type     string `json:"type"`
		Function struct {
			Name        string          `json:"name"`
			Description string          `json:"description,omitempty"`
			Parameters  json.RawMessage `json:"parameters,omitempty"`
		} `json:"function"`
	}
	if json.Unmarshal(raw, &tools) != nil {
		return nil
	}
	specs := make([]protocols.IRToolSpec, 0, len(tools))
	for _, t := range tools {
		if t.Type != "function" {
			continue
		}
		params := t.Function.Parameters
		if params == nil {
			params = json.RawMessage("{}")
		}
		specs = append(specs, protocols.IRToolSpec{
			Name:        t.Function.Name,
			Description: t.Function.Description,
			Parameters:  params,
		})
	}
	return specs
}

func decodeOpenAIToolChoice(raw json.RawMessage) (protocols.IRToolChoice, string) {
	if len(raw) == 0 {
		return protocols.ToolChoiceAuto, ""
	}
	var str string
	if json.Unmarshal(raw, &str) == nil {
		switch str {
		case "none":
			return protocols.ToolChoiceNone, ""
		case "required":
			return protocols.ToolChoiceRequired, ""
		default:
			return protocols.ToolChoiceAuto, ""
		}
	}
	var obj struct {
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if json.Unmarshal(raw, &obj) == nil && obj.Function.Name != "" {
		return protocols.ToolChoiceNamed, obj.Function.Name
	}
	return protocols.ToolChoiceAuto, ""
}

func decodeOpenAIResponseFormat(raw json.RawMessage) *protocols.IRResponseFormat {
	if len(raw) == 0 {
		return nil
	}
	var rf struct {
		Type       string `json:"type"`
		JSONSchema *struct {
			Name   string          `json:"name"`
			Schema json.RawMessage `json:"schema"`
			Strict bool            `json:"strict"`
		} `json:"json_schema"`
	}
	if json.Unmarshal(raw, &rf) != nil {
		return nil
	}
	switch rf.Type {
	case "json_object":
		return &protocols.IRResponseFormat{Type: "json_object"}
	case "json_schema":
		if rf.JSONSchema != nil {
			return &protocols.IRResponseFormat{
				Type:   "json_schema",
				Name:   rf.JSONSchema.Name,
				Schema: rf.JSONSchema.Schema,
				Strict: rf.JSONSchema.Strict,
			}
		}
	}
	return nil
}

// parseImageURLPart extracts a protocols.BlockImage from an image_url content part.
func parseImageURLPart(p map[string]interface{}) *protocols.BlockImage {
	imgURL, _ := p["image_url"].(map[string]interface{})
	if imgURL == nil {
		return nil
	}
	url, _ := imgURL["url"].(string)
	isURL := true
	mediaType := "image/png"
	if strings.HasPrefix(url, "data:image/") {
		if idx := strings.Index(url, ";"); idx > 0 {
			mediaType = url[5:idx]
		}
		if dataIdx := findDataAfterBase64(url); dataIdx > 0 {
			url = url[dataIdx:]
			isURL = false
		}
	}
	return &protocols.BlockImage{MediaType: mediaType, Data: url, IsURL: isURL}
}

func findDataAfterBase64(url string) int {
	const marker = ";base64,"
	idx := 0
	for i := 0; i < len(url)-len(marker); i++ {
		if url[i:i+len(marker)] == marker {
			idx = i + len(marker)
			break
		}
	}
	return idx
}

func isKnownReasoningEffort(effort string) bool {
	switch effort {
	case "low", "medium", "high":
		return true
	default:
		return false
	}
}

func effortToBudget(effort string) int {
	switch effort {
	case "low":
		return 1000
	case "medium":
		return 10000
	case "high":
		return 80000
	default:
		return 0
	}
}

func budgetToEffort(budget int) string {
	switch {
	case budget <= 1000:
		return "low"
	case budget < 50000:
		return "medium"
	default:
		return "high"
	}
}

// pickCeiling settles which of OpenAI's two spellings of the output ceiling
// applies when a request carries both.
//
// The lower one wins. OpenAI's own precedence depends on the model — the
// reasoning models honour max_completion_tokens and reject max_tokens outright,
// older ones know only max_tokens — and the decoder does not know which model
// the request will end up at, since a candidate can rewrite it. Taking the
// lower is the only choice that cannot raise a ceiling the caller stated: a
// request that says "at most 4096" under either name is not asking for more
// than 4096, whichever spelling the destination reads.
//
// This is a choice, not a rule read off a specification: no upstream defines
// what a body carrying both spellings means, because no upstream reads both.
// It changes what this decoder used to do — max_tokens was taken and the other
// name ignored — and it changes it only for requests that state both, which a
// client sends by mistake or in transition. Erring low keeps the mistake cheap.
func pickCeiling(maxTokens, maxCompletionTokens *int) *int {
	switch {
	case maxTokens == nil:
		return maxCompletionTokens
	case maxCompletionTokens == nil:
		return maxTokens
	case *maxCompletionTokens < *maxTokens:
		return maxCompletionTokens
	default:
		return maxTokens
	}
}
