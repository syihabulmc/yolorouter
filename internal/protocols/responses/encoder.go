package responses

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/yolorouter/yolorouter/internal/protocols"
)

// --- Responses Request Decoder ---

// RequestDecoder decodes an OpenAI Responses API request into IR.
type RequestDecoder struct{}

func (RequestDecoder) Protocol() protocols.ProtocolID { return protocols.ProtocolResponses }

func (RequestDecoder) DecodeRequest(body json.RawMessage, model string, isStream bool) (*protocols.IRRequest, error) {
	var req struct {
		Model           string          `json:"model"`
		Input           json.RawMessage `json:"input,omitempty"`
		Instructions    json.RawMessage `json:"instructions,omitempty"`
		MaxOutputTokens *int            `json:"max_output_tokens,omitempty"`
		Temperature     *float64        `json:"temperature,omitempty"`
		TopP            *float64        `json:"top_p,omitempty"`
		Tools           json.RawMessage `json:"tools,omitempty"`
		ToolChoice      json.RawMessage `json:"tool_choice,omitempty"`
		Reasoning       json.RawMessage `json:"reasoning,omitempty"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		return nil, fmt.Errorf("parse responses request: %w", err)
	}

	irReq := &protocols.IRRequest{
		Model:  model,
		Stream: protocols.IRStreamConfig{Enabled: isStream},
		Generation: protocols.IRGenerationConfig{
			Temperature: req.Temperature,
			TopP:        req.TopP,
			MaxTokens:   req.MaxOutputTokens,
		},
		SourceProtocol: "responses",
	}

	// instructions → system
	if len(req.Instructions) > 0 {
		var instrStr string
		if json.Unmarshal(req.Instructions, &instrStr) == nil && instrStr != "" {
			irReq.System = instrStr
		}
	}

	// input → messages
	messages, systemFromInput, err := decodeResponsesInput(req.Input)
	if err != nil {
		return nil, err
	}
	irReq.Messages = messages
	if systemFromInput != "" {
		if irReq.System != "" {
			irReq.System = systemFromInput + "\n\n" + irReq.System
		} else {
			irReq.System = systemFromInput
		}
	}

	// tools
	if len(req.Tools) > 0 {
		tools, err := decodeResponsesTools(req.Tools)
		if err == nil {
			irReq.Tools = tools
		}
	}

	// tool_choice
	if len(req.ToolChoice) > 0 {
		irReq.ToolChoice, irReq.ToolChoiceName = decodeResponsesToolChoice(req.ToolChoice)
	}

	// reasoning
	if len(req.Reasoning) > 0 {
		var reasoning struct {
			Effort string `json:"effort"`
		}
		if json.Unmarshal(req.Reasoning, &reasoning) == nil && reasoning.Effort != "" {
			irReq.Reasoning = protocols.IRReasoningConfig{
				Enabled: true,
				Effort:  reasoning.Effort,
			}
		}
	}

	return irReq, nil
}

func decodeResponsesInput(input json.RawMessage) ([]protocols.IRMessage, string, error) {
	if len(input) == 0 {
		return nil, "", fmt.Errorf("input is empty")
	}

	// Simple string input
	var inputStr string
	if json.Unmarshal(input, &inputStr) == nil {
		return []protocols.IRMessage{
			{Role: protocols.RoleUser, Content: []protocols.IRContentBlock{protocols.BlockText{Text: inputStr}}},
		}, "", nil
	}

	var items []struct {
		Type      string          `json:"type,omitempty"`
		Role      string          `json:"role,omitempty"`
		Content   json.RawMessage `json:"content,omitempty"`
		CallID    string          `json:"call_id,omitempty"`
		Name      string          `json:"name,omitempty"`
		Arguments string          `json:"arguments,omitempty"`
		Output    string          `json:"output,omitempty"`
	}
	if err := json.Unmarshal(input, &items); err != nil {
		return nil, "", fmt.Errorf("parse responses input: %w", err)
	}

	var systemParts []string
	var messages []protocols.IRMessage

	for _, item := range items {
		switch {
		case item.Role == "system" || item.Role == "developer":
			text := extractTextFromResponsesContent(item.Content)
			if text != "" {
				systemParts = append(systemParts, text)
			}

		case item.Type == "function_call":
			args := item.Arguments
			if args == "" {
				args = "{}"
			}
			callID := item.CallID
			if callID == "" {
				callID = "call_" + protocols.RandomString(12)
			}
			// Merge into last assistant message if possible
			if len(messages) > 0 && messages[len(messages)-1].Role == protocols.RoleAssistant {
				last := &messages[len(messages)-1]
				last.ToolCalls = append(last.ToolCalls, protocols.IRToolCall{
					ID:        callID,
					Name:      item.Name,
					Arguments: args,
				})
			} else {
				messages = append(messages, protocols.IRMessage{
					Role: protocols.RoleAssistant,
					ToolCalls: []protocols.IRToolCall{
						{ID: callID, Name: item.Name, Arguments: args},
					},
				})
			}

		case item.Type == "function_call_output":
			content := item.Output
			if content == "" {
				content = "(empty)"
			}
			contentJSON, _ := json.Marshal(content)
			messages = append(messages, protocols.IRMessage{
				Role:       protocols.RoleTool,
				ToolCallID: item.CallID,
				Content: []protocols.IRContentBlock{
					protocols.BlockToolResult{
						ToolUseID: item.CallID,
						Content:   json.RawMessage(contentJSON),
					},
				},
			})

		case item.Role == "user":
			text := extractTextFromResponsesContent(item.Content)
			messages = append(messages, protocols.IRMessage{
				Role:    protocols.RoleUser,
				Content: []protocols.IRContentBlock{protocols.BlockText{Text: text}},
			})

		case item.Role == "assistant":
			text := extractTextFromResponsesContent(item.Content)
			messages = append(messages, protocols.IRMessage{
				Role:    protocols.RoleAssistant,
				Content: []protocols.IRContentBlock{protocols.BlockText{Text: text}},
			})

		default:
			if len(item.Content) > 0 {
				text := extractTextFromResponsesContent(item.Content)
				if text != "" {
					messages = append(messages, protocols.IRMessage{
						Role:    protocols.RoleUser,
						Content: []protocols.IRContentBlock{protocols.BlockText{Text: text}},
					})
				}
			}
		}
	}

	var system string
	if len(systemParts) > 0 {
		system = strings.Join(systemParts, "\n\n")
	}
	return messages, system, nil
}

func extractTextFromResponsesContent(raw json.RawMessage) string {
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

func decodeResponsesTools(raw json.RawMessage) ([]protocols.IRToolSpec, error) {
	var tools []struct {
		Type        string          `json:"type"`
		Name        string          `json:"name,omitempty"`
		Description string          `json:"description,omitempty"`
		Parameters  json.RawMessage `json:"parameters,omitempty"`
	}
	if err := json.Unmarshal(raw, &tools); err != nil {
		return nil, err
	}
	var out []protocols.IRToolSpec
	for _, t := range tools {
		if t.Type != "function" {
			continue
		}
		params := t.Parameters
		if len(params) == 0 || string(params) == "null" {
			params = json.RawMessage(`{"type":"object","properties":{}}`)
		}
		out = append(out, protocols.IRToolSpec{
			Name:        t.Name,
			Description: t.Description,
			Parameters:  params,
		})
	}
	return out, nil
}

func decodeResponsesToolChoice(raw json.RawMessage) (protocols.IRToolChoice, string) {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		switch s {
		case "auto":
			return protocols.ToolChoiceAuto, ""
		case "required":
			return protocols.ToolChoiceRequired, ""
		case "none":
			return protocols.ToolChoiceNone, ""
		default:
			return protocols.ToolChoiceAuto, ""
		}
	}

	var m struct {
		Type     string `json:"type"`
		Name     string `json:"name"`
		Function struct {
			Name string `json:"name"`
		} `json:"function"`
	}
	if json.Unmarshal(raw, &m) == nil && m.Type == "function" {
		name := m.Name
		if name == "" {
			name = m.Function.Name
		}
		if name != "" {
			return protocols.ToolChoiceNamed, name
		}
	}
	return protocols.ToolChoiceAuto, ""
}

// --- Responses Response Encoder ---

// ResponseEncoder encodes IR responses into OpenAI Responses API format.
type ResponseEncoder struct{}

func (ResponseEncoder) EncodeResponse(resp *protocols.IRResponse) json.RawMessage {
	var outputs []interface{}

	// Reasoning content
	if resp.ReasoningContent != "" {
		outputs = append(outputs, map[string]interface{}{
			"type": "reasoning",
			"id":   generateResponsesItemID(),
			"summary": []interface{}{
				map[string]interface{}{"type": "summary_text", "text": resp.ReasoningContent},
			},
		})
	}

	// Text content
	if resp.Content != "" {
		outputs = append(outputs, map[string]interface{}{
			"type":    "message",
			"id":      generateResponsesMessageID(),
			"role":    "assistant",
			"content": makeResponsesContentParts(resp.Content),
			"status":  "completed",
		})
	}

	// Tool calls
	for _, tc := range resp.ToolCalls {
		args := tc.Arguments
		if args == "" {
			args = "{}"
		}
		callID := tc.ID
		if callID == "" {
			callID = "call_" + protocols.RandomString(24)
		}
		outputs = append(outputs, map[string]interface{}{
			"type":      "function_call",
			"id":        generateResponsesItemID(),
			"call_id":   callID,
			"name":      tc.Name,
			"arguments": args,
			"status":    "completed",
		})
	}

	// Empty fallback
	if len(outputs) == 0 {
		outputs = append(outputs, map[string]interface{}{
			"type":    "message",
			"id":      generateResponsesMessageID(),
			"role":    "assistant",
			"content": makeResponsesContentParts(""),
			"status":  "completed",
		})
	}

	status := "completed"
	var incompleteDetails interface{}
	if resp.StopReason == "length" {
		status = "incomplete"
		incompleteDetails = map[string]interface{}{"reason": "max_output_tokens"}
	}

	usage := responsesWireUsage(resp.Usage)

	result := map[string]interface{}{
		"id":                 resp.ID,
		"object":             "response",
		"model":              resp.Model,
		"status":             status,
		"output":             outputs,
		"usage":              usage,
		"incomplete_details": incompleteDetails,
	}

	data, _ := json.Marshal(result)
	return data
}

func makeResponsesContentParts(text string) []interface{} {
	return []interface{}{
		map[string]interface{}{
			"type":        "output_text",
			"text":        text,
			"annotations": []interface{}{},
		},
	}
}

// --- Responses Stream Encoder ---

type responsesToolState struct {
	outputIdx int
	itemID    string
	callID    string
	name      string
	args      string
}

// StreamEncoder encodes IR deltas into Responses API SSE events.
type StreamEncoder struct {
	responseID        string
	model             string
	seqNum            int
	usage             protocols.IRUsage
	createdSent       bool
	completed         bool
	pendingStopReason string // Stop reason received via DeltaDone; response.completed is deferred to EncodeDone
	hasStop           bool   // Whether DeltaDone has been received (distinguishes a legit empty stopReason="" from not-yet-received)
	outputIndex       int

	// Message state
	messageAdded    bool
	messageItemID   string
	accumulatedText string

	// Reasoning state
	reasoningAdded  bool
	reasoningItemID string

	// Tool call state (keyed by IR index)
	toolStates map[int]*responsesToolState
}

func NewStreamEncoder() *StreamEncoder {
	return &StreamEncoder{
		toolStates: make(map[int]*responsesToolState),
	}
}

func (e *StreamEncoder) EncodeDeltas(deltas []protocols.IRStreamDelta) []protocols.SSEEvent {
	var events []protocols.SSEEvent

	for _, delta := range deltas {
		switch d := delta.(type) {
		case protocols.DeltaMessageStart:
			e.model = d.Model
			e.responseID = d.ID
			if e.responseID == "" {
				e.responseID = generateResponsesResponseID()
			}

		case protocols.DeltaText:
			events = append(events, e.ensureCreated()...)
			events = append(events, e.ensureMessage()...)
			e.accumulatedText += d.Text
			events = append(events, e.makeEvent("response.output_text.delta", map[string]interface{}{
				"output_index":  e.outputIndex,
				"content_index": 0,
				"delta":         d.Text,
				"item_id":       e.messageItemID,
			}))

		case protocols.DeltaThinking:
			events = append(events, e.ensureCreated()...)
			events = append(events, e.closeMessage()...)
			if !e.reasoningAdded {
				e.reasoningAdded = true
				e.reasoningItemID = generateResponsesItemID()
				events = append(events, e.makeEvent("response.output_item.added", map[string]interface{}{
					"output_index": e.outputIndex,
					"item": map[string]interface{}{
						"type": "reasoning",
						"id":   e.reasoningItemID,
					},
				}))
			}
			events = append(events, e.makeEvent("response.reasoning_summary_text.delta", map[string]interface{}{
				"output_index":  e.outputIndex,
				"summary_index": 0,
				"delta":         d.Text,
				"item_id":       e.reasoningItemID,
			}))

		case protocols.DeltaToolCallStart:
			events = append(events, e.ensureCreated()...)
			events = append(events, e.closeReasoning()...)
			events = append(events, e.closeMessage()...)
			toolItemID := generateResponsesItemID()
			callID := d.ID
			if callID == "" {
				callID = "call_" + protocols.RandomString(24)
			}
			idx := e.outputIndex
			e.outputIndex++
			e.toolStates[d.Index] = &responsesToolState{
				outputIdx: idx,
				itemID:    toolItemID,
				callID:    callID,
				name:      d.Name,
			}
			events = append(events, e.makeEvent("response.output_item.added", map[string]interface{}{
				"output_index": idx,
				"item": map[string]interface{}{
					"type":    "function_call",
					"id":      toolItemID,
					"call_id": callID,
					"name":    d.Name,
					"status":  "in_progress",
				},
			}))

		case protocols.DeltaToolCallArgs:
			ts, ok := e.toolStates[d.Index]
			if !ok {
				continue
			}
			ts.args += d.Arguments
			events = append(events, e.makeEvent("response.function_call_arguments.delta", map[string]interface{}{
				"output_index": ts.outputIdx,
				"delta":        d.Arguments,
				"item_id":      ts.itemID,
				"call_id":      ts.callID,
				"name":         ts.name,
			}))

		case protocols.DeltaUsage:
			// Field-level merge (via IRUsage.Merge, which only overwrites non-zero fields):
			// the upstream may send partial usage chunks across multiple frames (for example,
			// some OpenAI-compatible upstreams split prompt usage and completion usage into
			// two separate frames for reasoning models). A whole-struct last-wins merge would
			// let an already-populated PromptTokens/Cache field get zeroed out by a later
			// completion-only chunk, causing both the client-facing SSE stream and billing to
			// under-report input_tokens.
			e.usage.Merge(d.Usage)

		case protocols.DeltaDone:
			// Mid-stream item terminators (output_item.done etc.) are sent immediately and
			// don't affect the client.
			// response.completed is deferred to EncodeDone: when a Chat upstream sends
			// finish_reason and usage in the same frame, the decoder emits DeltaDone before
			// DeltaUsage, so calling makeCompleted right away would make
			// response.completed.usage read all zeros and leave the downstream client unable
			// to see the real token counts.
			events = append(events, e.closeAllTools()...)
			events = append(events, e.closeReasoning()...)
			events = append(events, e.closeMessage()...)
			e.pendingStopReason = d.StopReason
			e.hasStop = true

		case protocols.DeltaUnknown:
			var value json.RawMessage
			if json.Unmarshal(d.Raw, &value) == nil {
				events = append(events, protocols.SSEEvent{Data: string(value)})
			}
		}
	}

	return events
}

func (e *StreamEncoder) EncodeDone() []protocols.SSEEvent {
	// Only called when the caller considers the stream to have truly finished
	// (finishErr == nil). At this point every DeltaUsage has already been applied to
	// e.usage, so response.completed's usage is accurate.
	if e.completed || !e.hasStop {
		return nil
	}
	e.completed = true
	return []protocols.SSEEvent{e.makeCompleted(e.pendingStopReason)}
}

func (e *StreamEncoder) Usage() protocols.IRUsage {
	return e.usage
}

func (e *StreamEncoder) ensureCreated() []protocols.SSEEvent {
	if e.createdSent {
		return nil
	}
	e.createdSent = true
	respObj := map[string]interface{}{
		"id":     e.responseID,
		"object": "response",
		"model":  e.model,
		"status": "in_progress",
		"output": []interface{}{},
	}
	return []protocols.SSEEvent{
		e.makeEvent("response.created", map[string]interface{}{"response": respObj}),
		e.makeEvent("response.in_progress", map[string]interface{}{"response": respObj}),
	}
}

func (e *StreamEncoder) ensureMessage() []protocols.SSEEvent {
	if e.messageAdded {
		return nil
	}
	e.messageAdded = true
	e.messageItemID = generateResponsesMessageID()
	e.accumulatedText = ""
	idx := e.outputIndex
	return []protocols.SSEEvent{
		e.makeEvent("response.output_item.added", map[string]interface{}{
			"output_index": idx,
			"item": map[string]interface{}{
				"type":   "message",
				"id":     e.messageItemID,
				"role":   "assistant",
				"status": "in_progress",
				"content": []interface{}{
					map[string]interface{}{"type": "input_text", "text": ""},
				},
			},
		}),
		e.makeEvent("response.content_part.added", map[string]interface{}{
			"item_id":       e.messageItemID,
			"output_index":  idx,
			"content_index": 0,
			"part":          map[string]interface{}{"type": "output_text", "text": ""},
		}),
	}
}

func (e *StreamEncoder) closeMessage() []protocols.SSEEvent {
	if !e.messageAdded {
		return nil
	}
	idx := e.outputIndex
	id := e.messageItemID
	text := e.accumulatedText
	e.messageAdded = false
	e.messageItemID = ""
	e.accumulatedText = ""
	e.outputIndex++
	return []protocols.SSEEvent{
		e.makeEvent("response.output_text.done", map[string]interface{}{
			"output_index":  idx,
			"content_index": 0,
			"item_id":       id,
			"text":          text,
		}),
		e.makeEvent("response.content_part.done", map[string]interface{}{
			"item_id":       id,
			"output_index":  idx,
			"content_index": 0,
			"part":          map[string]interface{}{"type": "output_text", "text": text},
		}),
		e.makeEvent("response.output_item.done", map[string]interface{}{
			"output_index": idx,
			"item": map[string]interface{}{
				"type":    "message",
				"id":      id,
				"role":    "assistant",
				"status":  "completed",
				"content": makeResponsesContentParts(text),
			},
		}),
	}
}

func (e *StreamEncoder) closeReasoning() []protocols.SSEEvent {
	if !e.reasoningAdded {
		return nil
	}
	idx := e.outputIndex
	id := e.reasoningItemID
	e.reasoningAdded = false
	e.reasoningItemID = ""
	e.outputIndex++
	return []protocols.SSEEvent{
		e.makeEvent("response.reasoning_summary_text.done", map[string]interface{}{
			"output_index":  idx,
			"summary_index": 0,
			"item_id":       id,
		}),
		e.makeEvent("response.output_item.done", map[string]interface{}{
			"output_index": idx,
			"item": map[string]interface{}{
				"type": "reasoning",
				"id":   id,
			},
		}),
	}
}

func (e *StreamEncoder) closeAllTools() []protocols.SSEEvent {
	if len(e.toolStates) == 0 {
		return nil
	}
	var events []protocols.SSEEvent
	for _, ts := range e.toolStates {
		events = append(events,
			e.makeEvent("response.function_call_arguments.done", map[string]interface{}{
				"output_index": ts.outputIdx,
				"arguments":    ts.args,
				"item_id":      ts.itemID,
				"call_id":      ts.callID,
				"name":         ts.name,
			}),
			e.makeEvent("response.output_item.done", map[string]interface{}{
				"output_index": ts.outputIdx,
				"item": map[string]interface{}{
					"type":      "function_call",
					"id":        ts.itemID,
					"call_id":   ts.callID,
					"name":      ts.name,
					"arguments": ts.args,
					"status":    "completed",
				},
			}),
		)
	}
	e.toolStates = make(map[int]*responsesToolState)
	return events
}

// responsesWireUsage renders IR usage as the Responses API usage object,
// shared by the streaming response.completed event and the non-streaming
// response so the two shapes cannot drift apart.
//
// Emits GROSS counts: the Responses API documents
// input_tokens_details.cached_tokens as a breakdown OF input_tokens, so the
// cached portion must sit inside the input total. Forwarding the raw IR
// PromptTokens would be wrong for an Anthropic upstream, whose count is net —
// the cache portion would vanish and the response would claim
// cached_tokens > input_tokens.
func responsesWireUsage(u protocols.IRUsage) map[string]interface{} {
	// A record the gateway itself refused publishes nothing: emitting sanitized
	// counts would hand the client — and any downstream gateway billing from
	// them — numbers we already decided were impossible. null is the wire's
	// existing word for "unknown", and unknown is not zero.
	// HasNegativeCount as well as the flag: the non-streaming decoders keep a
	// bad count without marking it, and IRNonStreamRelay encodes the response
	// BEFORE the billing gate runs — so without this the client would receive
	// sanitized-looking usage for a record the gateway then refuses to bill.
	if u.Invalid || protocols.HasNegativeCount(u) {
		return nil
	}
	usage := map[string]interface{}{
		"input_tokens":  u.GrossPromptTokens(),
		"output_tokens": u.CompletionTokens,
		"total_tokens":  u.GrossTotalTokens(),
	}
	// BOTH members are in the schema's required list for input_tokens_details
	// ([cached_tokens, cache_write_tokens]), so the object and both members are
	// emitted unconditionally — a strict-validating downstream rejects the
	// response otherwise. An earlier revision gated them on being non-zero, on
	// the mistaken belief that the write was our own extension rather than
	// OpenAI's field.
	usage["input_tokens_details"] = map[string]interface{}{
		"cached_tokens":                 u.CacheReadTokens,
		protocols.CacheWriteDetailField: u.CacheWriteTokens,
	}
	return usage
}

func (e *StreamEncoder) makeCompleted(stopReason string) protocols.SSEEvent {
	status := "completed"
	var details interface{}
	if stopReason == "length" {
		status = "incomplete"
		details = map[string]interface{}{"reason": "max_output_tokens"}
	}
	usage := responsesWireUsage(e.usage)
	return e.makeEvent("response.completed", map[string]interface{}{
		"response": map[string]interface{}{
			"id":                 e.responseID,
			"object":             "response",
			"model":              e.model,
			"status":             status,
			"output":             []interface{}{},
			"usage":              usage,
			"incomplete_details": details,
		},
	})
}

func (e *StreamEncoder) makeEvent(eventType string, data map[string]interface{}) protocols.SSEEvent {
	data["type"] = eventType
	d, _ := json.Marshal(data)
	e.seqNum++
	return protocols.SSEEvent{Data: string(d)}
}

// ID generators for Responses API
func generateResponsesResponseID() string { return "resp_" + protocols.RandomString(32) }
func generateResponsesItemID() string     { return "rs_" + protocols.RandomString(24) }
func generateResponsesMessageID() string  { return "msg_" + protocols.RandomString(24) }
