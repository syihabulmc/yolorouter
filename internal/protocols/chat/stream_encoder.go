package chat

import (
	"encoding/json"
	"github.com/yolorouter/yolorouter/internal/protocols"
	"strings"
)

// StreamEncoder encodes IR deltas into OpenAI Chat Completions SSE format.
type StreamEncoder struct {
	usage        protocols.IRUsage
	model        string
	started      bool
	id           string
	hasToolCalls bool

	// IncludeUsage mirrors the caller's stream_options.include_usage=true
	// request: when true, EncodeDone emits one extra usage-only chunk
	// (empty choices, populated usage) before the [DONE] terminator, the
	// same shape a real OpenAI streaming response uses. Only meaningful for
	// a cross-protocol egress, where this encoder — not the upstream itself
	// — is what produces the client-facing SSE frames; the gateway wires
	// this from rc.WantsStreamUsage. Defaults to false (no usage chunk),
	// matching the pre-existing behavior for every caller that doesn't set
	// it.
	IncludeUsage bool
}

func NewStreamEncoder() *StreamEncoder {
	return &StreamEncoder{}
}

func (e *StreamEncoder) EncodeDeltas(deltas []protocols.IRStreamDelta) []protocols.SSEEvent {
	var events []protocols.SSEEvent

	for _, delta := range deltas {
		switch d := delta.(type) {
		case protocols.DeltaMessageStart:
			e.model = d.Model
			e.id = d.ID
			e.started = true

		case protocols.DeltaText:
			events = append(events, openaiTextChunk(e.id, e.model, d.Text))

		case protocols.DeltaThinking:
			events = append(events, openaiReasoningChunk(e.id, e.model, d.Text))

		case protocols.DeltaToolCallStart:
			e.hasToolCalls = true
			events = append(events, openaiToolCallStartChunk(e.id, e.model, d.Index, d.ID, d.Name))

		case protocols.DeltaToolCallArgs:
			events = append(events, openaiToolCallArgsChunk(e.id, e.model, d.Index, d.Arguments))

		case protocols.DeltaUsage:
			// Field-level merge (via IRUsage.Merge), aligned with the claude /
			// responses / gemini encoders: when the upstream sends usage across
			// multiple partial chunks, a later completion-only frame must not
			// zero out PromptTokens / cache fields that were already collected
			// — otherwise we under-report billed usage and the client reads
			// wrong usage numbers.
			e.usage.Merge(d.Usage)

		case protocols.DeltaDone:
			events = append(events, openaiFinishChunk(e.id, e.model, d.StopReason, e.hasToolCalls))

		case protocols.DeltaUnknown:
			var value json.RawMessage
			if json.Unmarshal(d.Raw, &value) == nil {
				events = append(events, protocols.SSEEvent{Data: string(value)})
			}
		}
	}

	return events
}

// EncodeDone emits the OpenAI SSE stream terminator (`data: [DONE]`),
// preceded by a usage-only chunk when IncludeUsage is set and usage has
// meaningful token counts — mirroring how a real OpenAI stream with
// stream_options.include_usage=true sends one final chunk with empty
// choices and populated usage right before [DONE]. IRStreamRelay calls this
// once, only on a clean end (no upstream error), after every delta has been
// encoded — so a cross-protocol stream, whose egress decoder produces no
// OpenAI-style [DONE] sentinel (or usage chunk) of its own, still terminates
// the way an OpenAI SDK expects. On the same-protocol passthrough path this
// encoder is bypassed entirely and the upstream's own [DONE] (and usage
// chunk, if any) is forwarded verbatim, so there is no risk of a duplicate
// terminator or usage frame.
func (e *StreamEncoder) EncodeDone() []protocols.SSEEvent {
	var events []protocols.SSEEvent
	if e.IncludeUsage && hasMeaningfulChatUsage(e.usage) {
		events = append(events, openaiUsageChunk(e.id, e.model, e.usage))
	}
	events = append(events, protocols.SSEEvent{Data: "[DONE]"})
	return events
}

// hasMeaningfulChatUsage reports whether usage carries at least one non-zero
// token count, guarding against emitting an empty (all-zero) usage chunk
// when the upstream never actually reported usage.
func hasMeaningfulChatUsage(u protocols.IRUsage) bool {
	// Never put an impossible record on the wire: the client would read it as
	// authoritative, and a downstream gateway would bill it.
	if u.Invalid {
		return false
	}
	return u.PromptTokens > 0 || u.CompletionTokens > 0 || u.TotalTokens > 0
}

// openaiUsageChunk builds the final usage-only SSE frame OpenAI's
// stream_options.include_usage=true sends before [DONE]: same envelope as a
// content chunk, empty choices, populated usage. Mirrors ResponseEncoder's
// non-stream usage block (including the cached_tokens detail) so the
// stream and non-stream shapes stay consistent.
func openaiUsageChunk(id, model string, usage protocols.IRUsage) protocols.SSEEvent {
	chunk := map[string]interface{}{
		"id":      id,
		"object":  "chat.completion.chunk",
		"model":   model,
		"choices": []interface{}{},
		"usage":   openAIWireUsage(usage),
	}
	data, _ := json.Marshal(chunk)
	return protocols.SSEEvent{Data: string(data)}
}

// openAIWireUsage builds the OpenAI-spec usage object, shared by the streaming
// usage-only chunk and the non-streaming response so the two shapes cannot
// drift apart.
//
// Emits GROSS counts: OpenAI documents cached_tokens as a breakdown OF
// prompt_tokens, so the cached portion must be inside the prompt total.
// Forwarding the raw IR PromptTokens would be wrong for an Anthropic upstream,
// whose count is net — the whole cache portion would vanish and the response
// would claim cached_tokens > prompt_tokens.
func openAIWireUsage(u protocols.IRUsage) map[string]interface{} {
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
	usage := map[string]interface{}{
		"prompt_tokens":     u.GrossPromptTokens(),
		"completion_tokens": u.CompletionTokens,
		"total_tokens":      u.GrossTotalTokens(),
	}
	if u.CacheReadTokens > 0 || u.CacheWriteTokens > 0 {
		details := map[string]interface{}{}
		if u.CacheReadTokens > 0 {
			details["cached_tokens"] = u.CacheReadTokens
		}
		// Nested, not top-level; see protocols.CacheWriteDetailField.
		if u.CacheWriteTokens > 0 {
			details[protocols.CacheWriteDetailField] = u.CacheWriteTokens
		}
		usage["prompt_tokens_details"] = details
	}
	return usage
}

func (e *StreamEncoder) Usage() protocols.IRUsage {
	return e.usage
}

func openAIResponseID(id string) string {
	if id != "" && !strings.HasPrefix(id, "chatcmpl-") {
		return "chatcmpl-" + id
	}
	return id
}

// ResponseEncoder encodes IR responses into OpenAI Chat Completions JSON.
type ResponseEncoder struct{}

func (ResponseEncoder) EncodeResponse(resp *protocols.IRResponse) json.RawMessage {
	choice := map[string]interface{}{
		"index":         0,
		"finish_reason": mapFromOpenAIFinishReason(resp.StopReason, len(resp.ToolCalls) > 0),
	}
	message := map[string]interface{}{
		"role": "assistant",
	}
	if resp.ReasoningContent != "" {
		message["reasoning_content"] = resp.ReasoningContent
	}

	if len(resp.ToolCalls) > 0 {
		if resp.Content != "" {
			message["content"] = resp.Content
		}
		toolCalls := make([]interface{}, 0, len(resp.ToolCalls))
		for _, tc := range resp.ToolCalls {
			toolCalls = append(toolCalls, map[string]interface{}{
				"id":   tc.ID,
				"type": "function",
				"function": map[string]interface{}{
					"name":      tc.Name,
					"arguments": tc.Arguments,
				},
			})
		}
		message["tool_calls"] = toolCalls
	} else {
		message["content"] = resp.Content
	}
	choice["message"] = message

	result := map[string]interface{}{
		"id":      openAIResponseID(resp.ID),
		"object":  "chat.completion",
		"model":   resp.Model,
		"choices": []interface{}{choice},
		"usage":   openAIWireUsage(resp.Usage),
	}

	data, _ := json.Marshal(result)
	return data
}

// --- helpers ---

func openaiTextChunk(id, model, text string) protocols.SSEEvent {
	chunk := map[string]interface{}{
		"id":     id,
		"object": "chat.completion.chunk",
		"model":  model,
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"delta": map[string]interface{}{"content": text},
			},
		},
	}
	data, _ := json.Marshal(chunk)
	return protocols.SSEEvent{Data: string(data)}
}

func openaiReasoningChunk(id, model, text string) protocols.SSEEvent {
	chunk := map[string]interface{}{
		"id":     id,
		"object": "chat.completion.chunk",
		"model":  model,
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"delta": map[string]interface{}{"reasoning_content": text},
			},
		},
	}
	data, _ := json.Marshal(chunk)
	return protocols.SSEEvent{Data: string(data)}
}

func openaiToolCallStartChunk(id, model string, index int, callID, name string) protocols.SSEEvent {
	chunk := map[string]interface{}{
		"id":     id,
		"object": "chat.completion.chunk",
		"model":  model,
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"delta": map[string]interface{}{
					"tool_calls": []interface{}{
						map[string]interface{}{
							"index":    index,
							"id":       callID,
							"type":     "function",
							"function": map[string]interface{}{"name": name, "arguments": ""},
						},
					},
				},
			},
		},
	}
	data, _ := json.Marshal(chunk)
	return protocols.SSEEvent{Data: string(data)}
}

func openaiToolCallArgsChunk(id, model string, index int, arguments string) protocols.SSEEvent {
	chunk := map[string]interface{}{
		"id":     id,
		"object": "chat.completion.chunk",
		"model":  model,
		"choices": []interface{}{
			map[string]interface{}{
				"index": 0,
				"delta": map[string]interface{}{
					"tool_calls": []interface{}{
						map[string]interface{}{
							"index":    index,
							"function": map[string]interface{}{"arguments": arguments},
						},
					},
				},
			},
		},
	}
	data, _ := json.Marshal(chunk)
	return protocols.SSEEvent{Data: string(data)}
}

func openaiFinishChunk(id, model, reason string, hasToolCalls bool) protocols.SSEEvent {
	mapped := mapFromOpenAIFinishReason(reason, hasToolCalls)
	chunk := map[string]interface{}{
		"id":     id,
		"object": "chat.completion.chunk",
		"model":  model,
		"choices": []interface{}{
			map[string]interface{}{
				"index":         0,
				"delta":         map[string]interface{}{},
				"finish_reason": mapped,
			},
		},
	}
	data, _ := json.Marshal(chunk)
	return protocols.SSEEvent{Data: string(data)}
}

func mapFromOpenAIFinishReason(reason string, hasToolCalls bool) interface{} {
	if hasToolCalls {
		return "tool_calls"
	}
	if reason == "" || reason == "stop" {
		return "stop"
	}
	return reason
}
