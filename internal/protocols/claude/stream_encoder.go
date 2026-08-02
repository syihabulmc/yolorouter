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
		// Full usage (input + cache), not just output_tokens. For OpenAI/Gemini/
		// Responses upstreams the input usage only arrives at stream end — long
		// after message_start went out with input_tokens 0 — so this terminal
		// message_delta is the ONLY event that can carry the real input and cache
		// breakdown to the client. Anthropic's spec documents only output_tokens
		// here, but its own web-search example ships input_tokens and the cache
		// fields on message_delta, so the extra members are compatible.
		"usage": claudeUsageMap(e.usage),
	}
	d, _ := json.Marshal(deltaData)
	return []protocols.SSEEvent{{Event: "message_delta", Data: string(d)}}
}

// claudeUsageMap builds Anthropic's usage object from an IRUsage: net input
// (NetPromptTokens, since Anthropic's input_tokens excludes cache), output, and
// the cache breakdown when non-zero. Shared by the streaming terminal
// message_delta and the non-streaming ResponseEncoder so the two output paths
// cannot drift apart.
func claudeUsageMap(u protocols.IRUsage) map[string]interface{} {
	// A record the gateway itself refused publishes nothing: emitting sanitized
	// counts would hand the client — and any downstream gateway billing from
	// them — numbers we already decided were impossible. null is the wire's
	// existing word for "unknown", and unknown is not zero.
	// Invalid alone: the verdict is settled once at the decoder exit (see
	// IRUsage.IsIncoherent), so this reads the same answer the billing gate
	// reads, instead of re-judging with a narrower predicate and disagreeing.
	if u.Invalid {
		return nil
	}
	m := map[string]interface{}{
		"input_tokens":  u.NetPromptTokens(),
		"output_tokens": u.CompletionTokens,
	}
	if u.CacheWriteTokens > 0 {
		m["cache_creation_input_tokens"] = u.CacheWriteTokens
	}
	if u.CacheReadTokens > 0 {
		m["cache_read_input_tokens"] = u.CacheReadTokens
	}
	return m
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
		// Net input per Anthropic's convention. Usually still 0 here: only an
		// Anthropic upstream reports usage early enough for message_start to
		// carry it; the other three protocols report at stream end, which is
		// why emitMessageDelta carries the authoritative counts.
		"usage": map[string]interface{}{
			"input_tokens":  e.usage.NetPromptTokens(),
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
		"usage":         claudeUsageMap(resp.Usage),
	}

	data, _ := json.Marshal(result)
	return data
}
