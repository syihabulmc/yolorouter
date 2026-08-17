package gateway

import "github.com/yolorouter/yolorouter/internal/protocols"

// clearResponseBodies drops UpstreamResponseBody/ResponseBody before this
// attempt commits to writing a 2xx response to the client. A prior failed
// candidate may have stashed a non-2xx error body in these fields
// (attemptOne's non-2xx path, "last attempt wins"); without this clear, a
// stale earlier-candidate error body would be persisted as this (successful)
// request's upstream/response body. Only the success path re-populates them
// afterward (or, for a stream request, leaves them empty — the sent SSE is
// captured to the stream capture file instead).
func (rc *Exchange) clearResponseBodies() {
	rc.bodies.ClearResponses()
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
// PromptTokens and the CacheIncludedInPrompt flag are passed through verbatim:
// each protocol's decoder reports PromptTokens in its own convention (OpenAI/
// Gemini/Responses include cache-read, so they set the flag true; Anthropic's
// input_tokens is already net, so the flag stays false). netPromptTokens
// (log.go) reads that flag to derive the net input for both billing and the
// persisted request_logs.input_tokens, so the conversion here must NOT
// pre-normalize by folding cache back into PromptTokens — doing so would make
// the persisted input count include cache tokens (gross), diverging from the
// net convention every downstream aggregate expects.
func irUsageToUsage(u *protocols.IRUsage) *Usage {
	if u == nil {
		return nil
	}
	// "Nothing was reported" is asked of every dimension the upstream can state
	// a quantity in, not only the three token counts. Searches the provider ran
	// on its own initiative and reasoning tokens are each their own line, and a
	// record carrying one of them and no tokens was being dropped whole: the
	// usage became nil, no observer was told anything, and the number could not
	// be re-derived afterwards because the body it was read from is gone.
	//
	// Deliberately NOT protocols.HasAnyUsage, which looks like the same
	// question and is not. That predicate answers "is there anything worth
	// putting back on the wire", so it folds in the cache counts and returns
	// false for a record judged impossible. Both of those belong to billing:
	// admitting cache-only records here would start pricing a shape this gate
	// used to drop, and treating an impossible record as absent would erase the
	// verdict instead of carrying it. Whether an upstream SAID something and
	// whether what it said can be BILLED are different questions, and this one
	// is the first.
	if u.PromptTokens == 0 && u.CompletionTokens == 0 && u.TotalTokens == 0 &&
		u.ReasoningTokens == 0 && u.WebSearchCount == 0 {
		return nil
	}
	return &Usage{
		PromptTokens:          u.PromptTokens,
		CompletionTokens:      u.CompletionTokens,
		TotalTokens:           u.TotalTokens,
		CacheWriteTokens:      u.CacheWriteTokens,
		CacheReadTokens:       u.CacheReadTokens,
		CacheIncludedInPrompt: u.CacheIncludedInPrompt,
		Invalid:               u.Invalid,
		ReasoningTokens:       u.ReasoningTokens,
		WebSearchCount:        u.WebSearchCount,
	}
}

// toIRUsage is the inverse of irUsageToUsage, letting this package reuse the
// cache-convention rules that live on protocols.IRUsage instead of restating
// them. A plain field copy: the two types model the same counts, and the whole
// point of borrowing the IR methods is that neither side gets its own reading
// of them.
func (u Usage) toIRUsage() protocols.IRUsage {
	return protocols.IRUsage{
		PromptTokens:          u.PromptTokens,
		CompletionTokens:      u.CompletionTokens,
		TotalTokens:           u.TotalTokens,
		CacheWriteTokens:      u.CacheWriteTokens,
		CacheReadTokens:       u.CacheReadTokens,
		CacheIncludedInPrompt: u.CacheIncludedInPrompt,
		Invalid:               u.Invalid,
		ReasoningTokens:       u.ReasoningTokens,
		WebSearchCount:        u.WebSearchCount,
	}
}
