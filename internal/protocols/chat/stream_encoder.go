package chat

import (
	"encoding/json"
	"github.com/yolorouter/yolorouter/internal/protocols"
	"strings"
	"time"
)

// StreamEncoder encodes IR deltas into OpenAI Chat Completions SSE format.
type StreamEncoder struct {
	usage        protocols.IRUsage
	model        string
	started      bool
	id           string
	hasToolCalls bool
	sawDone      bool

	// created is the Unix timestamp stamped on every chunk of this stream.
	// `created` is in the required list of both ChatCompletionChunk and
	// ChatCompletion in the official OpenAPI schema, so omitting it makes a
	// strict-validating downstream reject the whole response. Captured once at
	// construction so all frames of one stream agree, matching real OpenAI.
	created int64

	// IncludeUsage mirrors the caller's stream_options.include_usage=true
	// request: when true, EncodeDone emits one extra usage-only chunk (empty
	// choices, populated usage) before the [DONE] terminator, the same shape a
	// real OpenAI streaming response uses. Only meaningful on a cross-protocol
	// egress, where this encoder — not the upstream — produces the
	// client-facing SSE frames; the same-protocol passthrough path bypasses
	// the encoder entirely and forwards the upstream's own frames verbatim.
	// Wired from IRRequest.Stream.IncludeUsage by the dispatch layer.
	IncludeUsage bool
}

func NewStreamEncoder() *StreamEncoder {
	return &StreamEncoder{created: time.Now().UTC().Unix()}
}

// newChunk builds the shared chat.completion.chunk envelope. Centralising it
// keeps `created` (a required field) on every frame and applies the
// include_usage rule that non-final chunks carry an explicit `usage: null`.
func (e *StreamEncoder) newChunk(choices []interface{}) map[string]interface{} {
	chunk := map[string]interface{}{
		"id":      e.id,
		"object":  "chat.completion.chunk",
		"created": e.created,
		"model":   e.model,
		"choices": choices,
	}
	if e.IncludeUsage {
		// Per the OpenAI API docs, with stream_options.include_usage=true every
		// chunk except the final usage-only one reports usage as null.
		chunk["usage"] = nil
	}
	return chunk
}

// deltaChunk wraps a single choice delta in the standard envelope.
//
// finish_reason is emitted as an explicit null: the schema lists it alongside
// delta and index in choices[].required for every streaming chunk, and OpenAI's
// own examples carry `"finish_reason": null` on content frames. Omitting it is
// the same class of defect as the missing `created` — invisible to lenient
// clients, fatal to a schema-validating one, and on every frame rather than one.
func (e *StreamEncoder) deltaChunk(delta map[string]interface{}) protocols.SSEEvent {
	chunk := e.newChunk([]interface{}{
		map[string]interface{}{"index": 0, "delta": delta, "finish_reason": nil},
	})
	data, _ := json.Marshal(chunk)
	return protocols.SSEEvent{Data: string(data)}
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
			events = append(events, e.textChunk(d.Text))

		case protocols.DeltaThinking:
			events = append(events, e.reasoningChunk(d.Text))

		case protocols.DeltaToolCallStart:
			e.hasToolCalls = true
			events = append(events, e.toolCallStartChunk(d.Index, d.ID, d.Name))

		case protocols.DeltaToolCallArgs:
			events = append(events, e.toolCallArgsChunk(d.Index, d.Arguments))

		case protocols.DeltaUsage:
			// Field-level merge (via IRUsage.Merge), matching the claude /
			// responses / gemini encoders: when an upstream sends partial usage
			// chunks across multiple frames, a later completion-only frame must
			// not clobber already-collected PromptTokens / cache fields to 0
			// (which would under-bill and hand the client wrong usage).
			e.usage.Merge(d.Usage)

		case protocols.DeltaDone:
			e.sawDone = true
			events = append(events, e.finishChunk(d.StopReason, e.hasToolCalls))

		case protocols.DeltaUnknown:
			var value json.RawMessage
			if json.Unmarshal(d.Raw, &value) == nil {
				events = append(events, protocols.SSEEvent{Data: string(value)})
			}
		}
	}

	return events
}

// EncodeDone emits the OpenAI SSE stream terminator (`data: [DONE]`), preceded
// by a usage-only chunk when IncludeUsage is set and usage carries meaningful
// counts — mirroring a real OpenAI stream with stream_options.include_usage=true,
// which sends one final chunk with empty choices and populated usage right
// before [DONE].
//
// Called by the IR stream relay once, only on a clean end (finishErr == nil),
// after every delta has been encoded — so DeltaUsage has already been merged
// into e.usage and the counts are complete. On a failed stream it is not called
// at all, so no success terminator leaks out.
//
// The same-protocol passthrough path bypasses this encoder and forwards the
// upstream's own [DONE] verbatim, so there is no risk of a duplicate terminator.
//
// Gated on sawDone — mirroring the guards the other three ingress encoders
// already have (gemini: !e.hasStop, responses: e.completed || !e.hasStop,
// claude: e.stopped || e.pendingStopReason == ""). A truncated upstream that
// closes the socket cleanly mid-stream still reaches here with finishErr == nil
// (io.EOF is not a scanner error and the egress decoders' Finish() returns no
// error when they never saw a terminal frame). Emitting [DONE] there would hand
// the client an explicit success terminator for a half-written answer, which an
// OpenAI SDK would accept as a complete response instead of retrying.
func (e *StreamEncoder) EncodeDone() []protocols.SSEEvent {
	if !e.sawDone {
		return nil
	}
	var events []protocols.SSEEvent
	if e.IncludeUsage && protocols.HasAnyUsage(e.usage) {
		events = append(events, e.usageChunk())
	}
	events = append(events, protocols.SSEEvent{Data: "[DONE]"})
	return events
}

// usageChunk builds the final usage-only SSE frame that
// stream_options.include_usage=true produces: the standard chunk envelope with
// an empty choices array and populated usage. Reuses newChunk so it carries the
// same id / created / model as every content frame — a mismatched id would make
// clients that group chunks by id discard it as a separate completion.
func (e *StreamEncoder) usageChunk() protocols.SSEEvent {
	chunk := e.newChunk([]interface{}{})
	// Overwrite the null placeholder newChunk sets for non-final frames.
	chunk["usage"] = openAIWireUsage(e.usage)
	data, _ := json.Marshal(chunk)
	return protocols.SSEEvent{Data: string(data)}
}

// openAIWireUsage builds the OpenAI usage object shared by the
// non-streaming response and the streaming usage-only chunk.
//
// prompt_tokens is the GROSS input (cache included). cached_tokens is a
// breakdown of prompt_tokens and must therefore remain within that total.
// Emitting the IR's raw PromptTokens would drop the whole cache portion for
// Anthropic upstreams and produce the impossible cached_tokens > prompt_tokens.
//
// OpenAI's usage has no cache-WRITE field of its own, so CacheWriteTokens is
// carried in the non-standard protocols.CacheWriteAliasField below; without it
// a downstream gateway could not tell those tokens from fresh input and would
// under-bill that portion.
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
	// Reasoning tokens are decoded into the IR by the responses/claude decoders
	// but had no egress here, so clients doing reasoning cost breakdowns saw 0.
	if u.ReasoningTokens > 0 {
		usage["completion_tokens_details"] = map[string]interface{}{
			"reasoning_tokens": u.ReasoningTokens,
		}
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
		// logprobs is in choices[].required for the non-streaming response
		// (finish_reason / index / message / logprobs); it is nullable, so an
		// explicit null satisfies the schema without inventing data.
		"logprobs": nil,
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
		"id":     openAIResponseID(resp.ID),
		"object": "chat.completion",
		// `created` is in ChatCompletion's required list; omitting it makes a
		// strict-validating downstream reject the response.
		"created": time.Now().UTC().Unix(),
		"model":   resp.Model,
		"choices": []interface{}{choice},
		"usage":   openAIWireUsage(resp.Usage),
	}

	data, _ := json.Marshal(result)
	return data
}

// --- helpers ---

func (e *StreamEncoder) textChunk(text string) protocols.SSEEvent {
	return e.deltaChunk(map[string]interface{}{"content": text})
}

func (e *StreamEncoder) reasoningChunk(text string) protocols.SSEEvent {
	return e.deltaChunk(map[string]interface{}{"reasoning_content": text})
}

func (e *StreamEncoder) toolCallStartChunk(index int, callID, name string) protocols.SSEEvent {
	return e.deltaChunk(map[string]interface{}{
		"tool_calls": []interface{}{
			map[string]interface{}{
				"index":    index,
				"id":       callID,
				"type":     "function",
				"function": map[string]interface{}{"name": name, "arguments": ""},
			},
		},
	})
}

func (e *StreamEncoder) toolCallArgsChunk(index int, arguments string) protocols.SSEEvent {
	return e.deltaChunk(map[string]interface{}{
		"tool_calls": []interface{}{
			map[string]interface{}{
				"index":    index,
				"function": map[string]interface{}{"arguments": arguments},
			},
		},
	})
}

func (e *StreamEncoder) finishChunk(reason string, hasToolCalls bool) protocols.SSEEvent {
	chunk := e.newChunk([]interface{}{
		map[string]interface{}{
			"index":         0,
			"delta":         map[string]interface{}{},
			"finish_reason": mapFromOpenAIFinishReason(reason, hasToolCalls),
		},
	})
	data, _ := json.Marshal(chunk)
	return protocols.SSEEvent{Data: string(data)}
}

// mapFromOpenAIFinishReason maps an IR stop reason onto OpenAI's finish_reason
// enum (stop | length | tool_calls | content_filter).
//
// An explicit abnormal termination OUTRANKS the tool-call inference. A run cut
// off by max tokens can already have emitted partial tool-call argument deltas;
// reporting "tool_calls" would tell the client those arguments are complete and
// safe to execute when they may not even be valid JSON. "length" /
// "content_filter" must survive so the client knows the call is unusable.
func mapFromOpenAIFinishReason(reason string, hasToolCalls bool) interface{} {
	if protocols.IRStopReasonIsAbnormal(reason) {
		return reason
	}
	if hasToolCalls {
		return "tool_calls"
	}
	if reason == "" || reason == "stop" {
		return "stop"
	}
	return reason
}
