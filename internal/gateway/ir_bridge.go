package gateway

import "github.com/yolorouter/yolorouter/internal/protocols"

// var _ protocols.UpstreamBuffer = (*RelayContext)(nil) is a compile-time
// assertion that RelayContext satisfies the IR relay layer's minimal
// upstream-recording interface, so the IR relay helpers
// (protocols.IRStreamRelay / IRNonStreamRelay / IRStreamRelayJSONLines) can
// take a *RelayContext directly as their buf argument.
var _ protocols.UpstreamBuffer = (*RelayContext)(nil)

// AppendUpstream implements protocols.UpstreamBuffer for the streaming IR
// relay path: it appends one raw upstream line to the per-request stream
// capture file. Delegates to the existing appendStreamBodyLine helper (the
// same one passthroughStreamToClient uses) rather than reimplementing the
// capture-file bookkeeping (nil-file no-op, 1GiB anti-OOM backstop) here.
func (rc *RelayContext) AppendUpstream(data []byte) {
	appendStreamBodyLine(rc, data)
}

// SetBody implements protocols.UpstreamBuffer for the non-streaming IR relay
// path: it records the raw (pre-IR-decode) upstream response body for the
// request_log_bodies row, mirroring the non-IR non-stream path's population
// of UpstreamResponseBody.
func (rc *RelayContext) SetBody(data []byte) {
	rc.UpstreamResponseBody = data
}

// SetResponseBody implements protocols.UpstreamBuffer for the non-streaming
// IR relay path: it records the caller-facing (post-IR-encode) response
// bytes actually written to the client, mirroring dispatchPassthroughNonStream's
// population of rc.ResponseBody for the same-protocol path.
func (rc *RelayContext) SetResponseBody(data []byte) {
	rc.ResponseBody = data
}

// clearResponseBodies drops UpstreamResponseBody/ResponseBody before this
// attempt commits to writing a 2xx response to the client. A prior failed
// candidate may have stashed a non-2xx error body in these fields
// (attemptOne's non-2xx path, "last attempt wins"); without this clear, a
// stale earlier-candidate error body would be persisted as this (successful)
// request's upstream/response body. Only the success path re-populates them
// afterward (or, for a stream request, leaves them empty — the sent SSE is
// captured to streamBodyFile instead).
func (rc *RelayContext) clearResponseBodies() {
	rc.UpstreamResponseBody = nil
	rc.ResponseBody = nil
}

// irUsageToUsage converts a protocols.IRUsage into the gateway's own Usage
// type. IRUsage represents "field missing" and "field present but zero"
// identically (plain int fields, no pointer/omitempty tracking upstream of
// this conversion) — unlike wireUsage.toUsage, which distinguishes a missing
// prompt_tokens/completion_tokens member via pointer fields. To still avoid
// recording a fabricated zero-cost usage row when the upstream never sent
// usage at all, this applies the same "unknown, not zero" rule at the
// coarsest level available: an IRUsage where prompt, completion, and total
// tokens are ALL zero is treated as absent and mapped to nil (mirrors
// protocols.hasMeaningfulUsage's precondition for "usage has been
// collected"). Any other IRUsage, including one with only a single non-zero
// field, is mapped through. The emptiness check runs against the ORIGINAL
// fields, before the cache normalization below.
//
// gateway.computeCost (log.go) subtracts CacheReadTokens from PromptTokens
// to avoid double-billing cached input, which assumes OpenAI semantics where
// prompt_tokens already INCLUDES cache-read tokens. The Claude decoder does
// not follow that convention: Anthropic's input_tokens excludes
// cache_read_input_tokens, so IRUsage.PromptTokens is already the
// non-cached count and IRUsage.CacheIncludedInPrompt stays false. Passing
// that through unchanged would make computeCost double-subtract cache-read
// tokens and undercharge the tenant. Normalize to OpenAI semantics here:
// when the source protocol didn't include cache-read in PromptTokens, add
// it back in so computeCost's subtraction is correct regardless of origin
// protocol.
func irUsageToUsage(u *protocols.IRUsage) *Usage {
	if u == nil {
		return nil
	}
	if u.PromptTokens == 0 && u.CompletionTokens == 0 && u.TotalTokens == 0 {
		return nil
	}
	promptTokens := u.PromptTokens
	if !u.CacheIncludedInPrompt {
		promptTokens += u.CacheReadTokens
	}
	return &Usage{
		PromptTokens:     promptTokens,
		CompletionTokens: u.CompletionTokens,
		TotalTokens:      u.TotalTokens,
		CacheWriteTokens: u.CacheWriteTokens,
		CacheReadTokens:  u.CacheReadTokens,
	}
}
