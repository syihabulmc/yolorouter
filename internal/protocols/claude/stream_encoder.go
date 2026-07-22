package claude

import (
	"encoding/json"

	"github.com/yolorouter/yolorouter/internal/protocols"
)

// StreamEncoder encodes IR deltas into Claude SSE format.
// It maintains a state machine for Claude's content block lifecycle:
// message_start → content_block_start/delta/stop → message_delta → message_stop.
type StreamEncoder struct {
	usage               protocols.IRUsage
	model               string
	id                  string
	started             bool
	blockIndex          int
	blockOpen           bool
	blockType           string // "text", "thinking", "tool_use"
	toolCallIDs         map[int]string
	stopped             bool
	pendingStopReason   string // stop reason received via DeltaDone, deferred until EncodeDone emits it
	pendingStopSequence string // stop_sequence received via DeltaDone; non-empty only when stop_reason==stop_sequence
}

func NewStreamEncoder() *StreamEncoder {
	return &StreamEncoder{
		toolCallIDs: make(map[int]string),
	}
}

func (e *StreamEncoder) EncodeDeltas(deltas []protocols.IRStreamDelta) []protocols.SSEEvent {
	var events []protocols.SSEEvent

	for _, delta := range deltas {
		switch d := delta.(type) {
		case protocols.DeltaMessageStart:
			e.model = d.Model
			e.id = d.ID
			e.started = true
			events = append(events, protocols.SSEEvent{
				Event: "message_start",
				Data:  e.marshalMessageStart(d.ID, d.Model),
			})

		case protocols.DeltaText:
			if !e.blockOpen || e.blockType != "text" {
				events = append(events, e.closeBlock()...)
				events = append(events, e.openBlock("text", "")...)
			}
			events = append(events, protocols.SSEEvent{
				Event: "content_block_delta",
				Data:  e.marshalContentDelta(e.blockIndex, map[string]interface{}{"type": "text_delta", "text": d.Text}),
			})

		case protocols.DeltaThinking:
			if !e.blockOpen || e.blockType != "thinking" {
				events = append(events, e.closeBlock()...)
				events = append(events, e.openBlock("thinking", "")...)
			}
			events = append(events, protocols.SSEEvent{
				Event: "content_block_delta",
				Data:  e.marshalContentDelta(e.blockIndex, map[string]interface{}{"type": "thinking_delta", "thinking": d.Text}),
			})

		case protocols.DeltaToolCallStart:
			events = append(events, e.closeBlock()...)
			e.toolCallIDs[d.Index] = d.ID
			events = append(events, e.openBlockToolUse(d.ID, d.Name)...)

		case protocols.DeltaToolCallArgs:
			events = append(events, protocols.SSEEvent{
				Event: "content_block_delta",
				Data: e.marshalContentDelta(e.blockIndex, map[string]interface{}{
					"type":         "input_json_delta",
					"partial_json": d.Arguments,
				}),
			})

		case protocols.DeltaUsage:
			// Field-level merge (IRUsage.Merge only overwrites non-zero fields):
			// an upstream provider may split usage across multiple partial chunks
			// (e.g. some Azure/LiteLLM deployments send prompt usage and completion
			// usage in separate frames for reasoning models). A whole-struct
			// last-wins merge would let a completion-only chunk zero out the
			// PromptTokens/Cache fields that were already collected, under-reporting
			// input_tokens both in the client SSE stream and for billing.
			e.usage.Merge(d.Usage)

		case protocols.DeltaDone:
			events = append(events, e.closeBlock()...)
			reason := d.StopReason
			e.pendingStopSequence = d.StopSequence
			if len(e.toolCallIDs) > 0 {
				reason = "tool_calls"
				e.pendingStopSequence = ""
			}
			// Don't emit message_delta + message_stop immediately -- under the OpenAI
			// include_usage protocol, finish_reason and usage often arrive in the same
			// SSE frame (decoder emit order: DeltaDone first, then DeltaUsage). Emitting
			// right away would leave e.usage.CompletionTokens at 0, so the client would
			// receive a message_delta with usage:{output_tokens:0}. Deferring to
			// EncodeDone (called by the caller once the stream has truly ended)
			// guarantees DeltaUsage has already been applied.
			e.pendingStopReason = reason

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
	// Only called when the caller considers the stream to have truly ended
	// (finishErr == nil). At this point all DeltaUsage deltas have already been
	// applied to e.usage, so message_delta's output_tokens is accurate.
	if e.stopped || e.pendingStopReason == "" {
		return nil
	}
	e.stopped = true
	events := e.emitMessageDelta(e.pendingStopReason)
	events = append(events, protocols.SSEEvent{Event: "message_stop", Data: `{"type":"message_stop"}`})
	return events
}

func (e *StreamEncoder) Usage() protocols.IRUsage {
	return e.usage
}

func (e *StreamEncoder) openBlock(blockType string, id string) []protocols.SSEEvent {
	e.blockOpen = true
	e.blockType = blockType
	idx := e.blockIndex

	block := map[string]interface{}{"type": blockType}
	if blockType == "text" {
		block["text"] = ""
	}

	data := map[string]interface{}{
		"type":          "content_block_start",
		"index":         idx,
		"content_block": block,
	}
	d, _ := json.Marshal(data)
	return []protocols.SSEEvent{{Event: "content_block_start", Data: string(d)}}
}

func (e *StreamEncoder) openBlockToolUse(id, name string) []protocols.SSEEvent {
	e.blockOpen = true
	e.blockType = "tool_use"
	idx := e.blockIndex

	block := map[string]interface{}{
		"type":  "tool_use",
		"id":    id,
		"name":  name,
		"input": map[string]interface{}{},
	}
	data := map[string]interface{}{
		"type":          "content_block_start",
		"index":         idx,
		"content_block": block,
	}
	d, _ := json.Marshal(data)
	return []protocols.SSEEvent{{Event: "content_block_start", Data: string(d)}}
}

func (e *StreamEncoder) closeBlock() []protocols.SSEEvent {
	if !e.blockOpen {
		return nil
	}
	e.blockOpen = false
	e.blockIndex++

	data := map[string]interface{}{
		"type":  "content_block_stop",
		"index": e.blockIndex - 1,
	}
	d, _ := json.Marshal(data)
	return []protocols.SSEEvent{{Event: "content_block_stop", Data: string(d)}}
}

func (e *StreamEncoder) emitMessageDelta(stopReason string) []protocols.SSEEvent {
	reason := irToClaudeStopReason(stopReason)

	var stopSeqVal interface{}
	if e.pendingStopSequence != "" {
		stopSeqVal = e.pendingStopSequence
	}

	deltaData := map[string]interface{}{
		"type": "message_delta",
		"delta": map[string]interface{}{
			"stop_reason":   reason,
			"stop_sequence": stopSeqVal,
		},
		"usage": map[string]interface{}{
			"output_tokens": e.usage.CompletionTokens,
		},
	}
	d, _ := json.Marshal(deltaData)
	return []protocols.SSEEvent{{Event: "message_delta", Data: string(d)}}
}

func (e *StreamEncoder) marshalMessageStart(id, model string) string {
	msg := map[string]interface{}{
		"id":            id,
		"type":          "message",
		"role":          "assistant",
		"content":       []interface{}{},
		"model":         model,
		"stop_reason":   nil,
		"stop_sequence": nil,
		"usage": map[string]interface{}{
			"input_tokens":  e.usage.PromptTokens,
			"output_tokens": 0,
		},
	}
	data := map[string]interface{}{
		"type":    "message_start",
		"message": msg,
	}
	d, _ := json.Marshal(data)
	return string(d)
}

func (e *StreamEncoder) marshalContentDelta(index int, delta map[string]interface{}) string {
	data := map[string]interface{}{
		"type":  "content_block_delta",
		"index": index,
		"delta": delta,
	}
	d, _ := json.Marshal(data)
	return string(d)
}

// ResponseEncoder encodes IR responses into Claude Messages API JSON format.
type ResponseEncoder struct{}

func (ResponseEncoder) EncodeResponse(resp *protocols.IRResponse) json.RawMessage {
	var blocks []interface{}

	if resp.ReasoningContent != "" {
		blocks = append(blocks, map[string]interface{}{
			"type":     "thinking",
			"thinking": resp.ReasoningContent,
		})
	}

	if resp.Content != "" {
		blocks = append(blocks, map[string]interface{}{
			"type": "text",
			"text": resp.Content,
		})
	}

	for _, tc := range resp.ToolCalls {
		var input interface{} = map[string]interface{}{}
		if tc.Arguments != "" {
			var parsed interface{}
			if json.Unmarshal([]byte(tc.Arguments), &parsed) == nil {
				input = parsed
			}
		}
		blocks = append(blocks, map[string]interface{}{
			"type":  "tool_use",
			"id":    tc.ID,
			"name":  tc.Name,
			"input": input,
		})
	}

	if len(blocks) == 0 {
		blocks = append(blocks, map[string]interface{}{
			"type": "text",
			"text": "",
		})
	}

	stopReason := irToClaudeStopReason(resp.StopReason)
	if len(resp.ToolCalls) > 0 {
		stopReason = "tool_use"
	}

	var stopSeqVal interface{}
	if resp.StopSequence != "" {
		stopSeqVal = resp.StopSequence
	}

	result := map[string]interface{}{
		"id":            resp.ID,
		"type":          "message",
		"role":          "assistant",
		"content":       blocks,
		"model":         resp.Model,
		"stop_reason":   stopReason,
		"stop_sequence": stopSeqVal,
		"usage": map[string]interface{}{
			"input_tokens":  resp.Usage.PromptTokens,
			"output_tokens": resp.Usage.CompletionTokens,
		},
	}

	if resp.Usage.CacheWriteTokens > 0 || resp.Usage.CacheReadTokens > 0 {
		usage := result["usage"].(map[string]interface{})
		if resp.Usage.CacheWriteTokens > 0 {
			usage["cache_creation_input_tokens"] = resp.Usage.CacheWriteTokens
		}
		if resp.Usage.CacheReadTokens > 0 {
			usage["cache_read_input_tokens"] = resp.Usage.CacheReadTokens
		}
	}

	data, _ := json.Marshal(result)
	return data
}
