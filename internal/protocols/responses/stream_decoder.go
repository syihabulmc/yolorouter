package responses

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/yolorouter/yolorouter/internal/protocols"
)

// Sentinel errors for an explicit failure signaled inside the upstream Responses SSE stream.
// Deliberately carry no raw payload: this avoids leaking prompt fragments, tool arguments,
// or other sensitive data into application logs. The handler only needs to see the error to
// decide to route into the failure billing path; the raw SSE content is retained separately
// in rc.UpstreamResponseBuf for debugging.
var (
	errResponsesStreamFailed     = errors.New("upstream responses stream failed (response.failed event)")
	errResponsesStreamErrorEvent = errors.New("upstream responses stream error event")
)

// StreamDecoder parses an OpenAI Responses API SSE event stream into IR Stream Deltas.
// See the ResponsesEvent* constants (or the OpenAI Responses API spec) for the upstream
// SSE event types.
//
// State machine: tracks the currently active message item / reasoning item / function_call
// item, and emits the corresponding IR delta for each event type.
// upstreamErr records an error the upstream explicitly signaled mid-stream (response.failed
// or a top-level error event); it is returned from Finish() so the IR pipeline can propagate
// the in-stream failure back to the handler instead of settling/billing it as a success.
type StreamDecoder struct {
	// Whether message_start has already been emitted.
	startEmitted bool
	// call_id of the currently active function_call item, indexed by output_index.
	activeFunctionCalls map[int]functionCallState
	usage               protocols.IRUsage
	upstreamErr         error
}

type functionCallState struct {
	CallID string
	Name   string
}

func NewStreamDecoder() *StreamDecoder {
	return &StreamDecoder{
		activeFunctionCalls: make(map[int]functionCallState),
	}
}

// responsesStreamEventWire parses the JSON of a single SSE data frame.
type responsesStreamEventWire struct {
	Type     string            `json:"type"`
	Response *responsesRespMin `json:"response,omitempty"`
	Item     *responsesItemMin `json:"item,omitempty"`
	Delta    string            `json:"delta,omitempty"`
	Text     string            `json:"text,omitempty"`
	// Index fields, used by events such as function_call_arguments.delta.
	OutputIndex *int   `json:"output_index,omitempty"`
	ItemID      string `json:"item_id,omitempty"`
	CallID      string `json:"call_id,omitempty"`
	Name        string `json:"name,omitempty"`
	Arguments   string `json:"arguments,omitempty"`
}

type responsesRespMin struct {
	ID    string          `json:"id,omitempty"`
	Model string          `json:"model,omitempty"`
	Usage *responsesUsage `json:"usage,omitempty"`
}

type responsesItemMin struct {
	Type   string `json:"type,omitempty"`
	ID     string `json:"id,omitempty"`
	CallID string `json:"call_id,omitempty"`
	Name   string `json:"name,omitempty"`
}

// DecodeChunk parses a single line of SSE data (the "data: {...}" prefix already
// stripped) into IR deltas.
//
// Upstream Responses SSE frame format: "event: <type>\ndata: <json>\n\n".
// The caller is responsible for handing us the data line; the event type is already
// present in the JSON's type field, so it doesn't need to be passed separately.
func (d *StreamDecoder) DecodeChunk(raw string) ([]protocols.IRStreamDelta, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	// Tolerate an upstream that still has the "data: " prefix attached.
	if strings.HasPrefix(raw, "data:") {
		raw = strings.TrimSpace(raw[len("data:"):])
	}
	if raw == "[DONE]" || raw == "" {
		return nil, nil
	}

	var evt responsesStreamEventWire
	if err := json.Unmarshal([]byte(raw), &evt); err != nil {
		// Single-frame parse failure: return an Unknown delta and let the caller decide
		// whether to pass it through or drop it.
		return []protocols.IRStreamDelta{protocols.DeltaUnknown{Raw: json.RawMessage(raw)}}, nil
	}

	var out []protocols.IRStreamDelta

	// Once the upstream has explicitly failed (response.failed / error event), stop
	// emitting any further deltas — this prevents a later "success terminator" frame
	// such as response.done from being sent, which would make the client think the
	// response completed successfully.
	if d.upstreamErr != nil {
		return nil, nil
	}

	switch evt.Type {
	case "response.created", "response.in_progress":
		if !d.startEmitted && evt.Response != nil {
			out = append(out, protocols.DeltaMessageStart{ID: evt.Response.ID, Model: evt.Response.Model})
			d.startEmitted = true
		}
	case "response.output_item.added":
		// A function_call item is starting: record its call_id and name.
		if evt.Item != nil && evt.Item.Type == "function_call" {
			idx := -1
			if evt.OutputIndex != nil {
				idx = *evt.OutputIndex
			}
			d.activeFunctionCalls[idx] = functionCallState{
				CallID: evt.Item.CallID,
				Name:   evt.Item.Name,
			}
			out = append(out, protocols.DeltaToolCallStart{
				ID:    evt.Item.CallID,
				Name:  evt.Item.Name,
				Index: idx,
			})
		}
	case "response.output_text.delta":
		if evt.Delta != "" {
			out = append(out, protocols.DeltaText{Text: evt.Delta})
		}
	case "response.reasoning_summary_text.delta":
		if evt.Delta != "" {
			out = append(out, protocols.DeltaThinking{Text: evt.Delta})
		}
	case "response.function_call_arguments.delta":
		if evt.Delta != "" {
			idx := -1
			if evt.OutputIndex != nil {
				idx = *evt.OutputIndex
			}
			out = append(out, protocols.DeltaToolCallArgs{Index: idx, Arguments: evt.Delta})
		}
	case "response.completed":
		if evt.Response != nil && evt.Response.Usage != nil {
			d.collectUsage(evt.Response.Usage)
			// Marked rather than dropped: a dropped frame would leave whatever
			// an earlier frame merged in place, and the DeltaDone appended just
			// below would still complete the stream and bill those stale counts.
			// Merge propagates Invalid one-way, so the verdict survives.
			//
			// IsIncoherent, not HasNegativeCount: Merge judges each incoming
			// src frame on its own, but the ACCUMULATED record can still be
			// impossible once frames combine (one frame's prompt, another's
			// oversized cache). Re-weighing the merged result here is what
			// catches that — and the cache-exceeds-prompt shape alongside it.
			if d.usage.IsIncoherent() {
				d.usage.Invalid = true
			}
			out = append(out, protocols.DeltaUsage{Usage: d.usage})
		}
		out = append(out, protocols.DeltaDone{StopReason: "stop"})
	case "response.failed":
		// Explicit upstream failure: only record the type, not the raw content (to avoid
		// leaking sensitive data).
		// **Do not emit protocols.DeltaDone here**: emitting Done would make the ingress
		// encoder write a "looks successful" terminator frame (Claude message_stop / Chat
		// finish_reason=stop / Gemini finishReason=STOP), so the client would treat a failed
		// request as a complete response, inconsistent with the server settling it as a 502.
		// upstreamErr is surfaced via Finish() so IRStreamRelay skips EncodeDone().
		d.upstreamErr = errResponsesStreamFailed
	case "response.incomplete":
		out = append(out, protocols.DeltaDone{StopReason: "max_tokens"})
	case "response.done":
		// Fallback DONE; don't re-send usage.
		if !d.hasEmittedDone(out) {
			out = append(out, protocols.DeltaDone{StopReason: "stop"})
		}
	case "error":
		// Top-level error event: also treated as a failure, do not emit Done.
		d.upstreamErr = errResponsesStreamErrorEvent
		out = append(out, protocols.DeltaUnknown{Raw: json.RawMessage(raw)})
	}

	return out, nil
}

// Finish is called when the stream ends. If an upstream failure/error event was captured
// mid-stream, it returns that error; the IR pipeline propagates it back to the handler so
// the handler skips RecordSuccess and settles the request as a failure.
func (d *StreamDecoder) Finish() ([]protocols.IRStreamDelta, error) {
	return nil, d.upstreamErr
}

func (d *StreamDecoder) collectUsage(u *responsesUsage) {
	if u == nil {
		return
	}
	d.usage.PromptTokens = u.InputTokens
	d.usage.CompletionTokens = u.OutputTokens
	d.usage.TotalTokens = u.TotalTokens
	if u.InputTokensDetails != nil && u.InputTokensDetails.CachedTokens > 0 {
		d.usage.CacheReadTokens = u.InputTokensDetails.CachedTokens
		d.usage.CacheIncludedInPrompt = true
	}
	// Assigned unconditionally, matching the chat and gemini decoders: a
	// negative count from a buggy or hostile upstream has to reach the
	// gateway's coherence check, which rejects the whole record as unknown.
	// Gating the assignment on > 0 instead swallows the negative and lets the
	// remaining counts be billed as though nothing were wrong.
	// Exactly one cache-write spelling is taken, never summed: the nested
	// breakdown is the standard one and wins, the top-level alias is the
	// fallback for new-api-style peers.
	d.usage.CacheWriteTokens = u.CacheCreationInputTokens
	if u.InputTokensDetails != nil && u.InputTokensDetails.CacheWriteTokens != nil {
		d.usage.CacheWriteTokens = *u.InputTokensDetails.CacheWriteTokens
	}
	// The flag, by contrast, is only meaningful for a real count. The write
	// sits inside input_tokens, so without it NetPromptTokens would skip the
	// subtraction on a write-only request and over-report fresh input.
	if d.usage.CacheWriteTokens > 0 {
		d.usage.CacheIncludedInPrompt = true
	}
	if u.OutputTokensDetails != nil {
		d.usage.ReasoningTokens = u.OutputTokensDetails.ReasoningTokens
	}
}

func (d *StreamDecoder) hasEmittedDone(deltas []protocols.IRStreamDelta) bool {
	for _, d := range deltas {
		if _, ok := d.(protocols.DeltaDone); ok {
			return true
		}
	}
	return false
}

var _ = fmt.Sprintf // placeholder for future error messages
