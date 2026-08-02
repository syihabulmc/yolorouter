package protocols

import "math"

// IRUsage holds token usage information normalized across all protocols.
type IRUsage struct {
	PromptTokens          int
	CompletionTokens      int
	TotalTokens           int
	CacheWriteTokens      int
	CacheReadTokens       int
	CacheIncludedInPrompt bool
	ReasoningTokens       int
	WebSearchCount        int
	// Invalid marks a record whose counts cannot be trusted — an upstream
	// reported something impossible, such as a negative token count.
	//
	// It exists because dropping the offending frame is not enough on a
	// streaming path. Merge folds every frame into one accumulated record, so a
	// dropped frame silently leaves the PREVIOUS frame's counts in place, and a
	// finish_reason arriving in that same malformed chunk still completes the
	// stream — billing stale values that look perfectly coherent. Marking the
	// record instead lets the verdict travel with it: Merge propagates the flag
	// one-way, and every consumer that derives a count treats it as unknown.
	//
	// Unknown is not zero: a marked record must never be billed as free, it
	// must not be billed at all.
	Invalid bool
}

// Merge merges non-zero fields from src into dst.
//
// This is a field-level merge rather than a whole-struct last-wins assignment: an upstream
// provider may send partial usage chunks across multiple frames (e.g. prompt tokens in one
// frame, completion tokens in a later frame). A whole-struct assignment would let
// already-collected fields get clobbered back to zero; Merge only overwrites the numeric
// fields where src is non-zero, preserving previously collected values.
//
// CacheIncludedInPrompt is a pricing-accounting flag (the chat/gemini/responses decoders set
// it to true when constructing an IRUsage to indicate PromptTokens already includes cached
// tokens, so pricing subtracts the cached portion via netPromptTokens to avoid
// double-charging). **Only a false→true transition is allowed**: once any src marks it true
// it stays true; otherwise a later partial chunk defaulting to false would corrupt the
// pricing accounting and cause double-charging.
func (dst *IRUsage) Merge(src IRUsage) {
	// Recorded FIRST, because the copying below destroys the evidence: this
	// function only copies values greater than zero, so a negative count — the
	// very proof the record is impossible — is gone by the time any consumer
	// sees the accumulated result.
	//
	// This is deliberately the single place the check lives for streaming
	// paths. Every accumulator funnels through Merge, so a decoder cannot
	// forget it and cannot run it in the wrong order; both mistakes were made
	// when each decoder checked for itself.
	if HasNegativeCount(src) {
		dst.Invalid = true
	}
	if src.PromptTokens > 0 {
		dst.PromptTokens = src.PromptTokens
	}
	if src.CompletionTokens > 0 {
		dst.CompletionTokens = src.CompletionTokens
	}
	if src.TotalTokens > 0 {
		dst.TotalTokens = src.TotalTokens
	}
	if src.CacheWriteTokens > 0 {
		dst.CacheWriteTokens = src.CacheWriteTokens
	}
	if src.CacheReadTokens > 0 {
		dst.CacheReadTokens = src.CacheReadTokens
	}
	if src.ReasoningTokens > 0 {
		dst.ReasoningTokens = src.ReasoningTokens
	}
	if src.WebSearchCount > 0 {
		dst.WebSearchCount = src.WebSearchCount
	}
	if src.CacheIncludedInPrompt {
		dst.CacheIncludedInPrompt = true
	}
	// One-way, exactly like the flag above: once any frame in a stream is known
	// to be impossible, the accumulated record it merged into can never be
	// trusted again. Letting a later well-formed frame clear it would restore
	// the very "stale counts look coherent" outcome this flag exists to stop.
	if src.Invalid {
		dst.Invalid = true
	}
	if dst.TotalTokens == 0 || dst.TotalTokens < dst.PromptTokens+dst.CompletionTokens {
		dst.TotalTokens = dst.PromptTokens + dst.CompletionTokens
	}
}

// The four protocols this gateway speaks split into two camps over whether the
// input count includes cached tokens, and that split is the root of every
// cross-protocol usage bug:
//
//	protocol            input convention   cache read field                    cache write field
//	Anthropic Messages  NET                cache_read_input_tokens             cache_creation_input_tokens
//	OpenAI Chat         GROSS              prompt_tokens_details.cached_tokens (none)
//	OpenAI Responses    GROSS              input_tokens_details.cached_tokens  (none)
//	Google Gemini       GROSS              cachedContentTokenCount             (none)
//
// Anthropic is the sole NET protocol and the sole one counting cache writes.
// IRUsage stores whichever convention the upstream decoder BELIEVED it saw,
// flagged by CacheIncludedInPrompt, and the methods below convert to whichever
// convention an egress encoder needs. Two rules follow from that, and every
// usage bug found so far has been a violation of one of them:
//
//   - An encoder must never emit the raw PromptTokens. Whether that value
//     happens to be correct depends entirely on which upstream produced it.
//   - Nothing may read CacheIncludedInPrompt directly to derive a count. It
//     records a guess made from the wire shape alone; PromptIncludesCache is
//     the settled answer.

// CacheWriteAliasField is a deliberate extension to the OpenAI Chat, OpenAI
// Responses and Gemini usage schemas, none of which has a cache-WRITE field —
// Anthropic is the only protocol that counts cache creation.
//
// Without it, relaying an Anthropic upstream to a client speaking any other
// protocol silently loses the distinction: the tokens remain inside the gross
// prompt total, but a downstream gateway can no longer tell them from fresh
// input, so it bills them at the plain input rate rather than the cache-write
// rate. That is a systematic under-charge on every cached Anthropic request
// that crosses a protocol boundary.
//
// Anthropic's own field name is reused rather than minting a per-protocol one:
// a single name means a single thing to document, grep and decode, and it is
// what new-api emits. It is deliberately left in snake_case even on the Gemini
// wire, where the surrounding fields are camelCase — looking foreign is
// correct for a field that is not part of that spec.
//
// It is a BREAKDOWN, not an addition: GrossPromptTokens already counts these
// tokens inside the prompt total, exactly as cached_tokens is. A decoder
// reading it must subtract, never add — see NetPromptTokens.
//
// Only emitted when non-zero, so clients of cache-less upstreams never see an
// unfamiliar key.
const CacheWriteAliasField = "cache_creation_input_tokens"

// CacheWriteDetailField is where a cache-WRITE count belongs on an OpenAI-shaped
// wire: nested in the prompt/input token details object, beside cached_tokens.
//
// Unlike CacheWriteAliasField this is not invented here. OpenAI's own OpenAPI
// spec declares it in BOTH shapes — CompletionUsage.prompt_tokens_details
// .cache_write_tokens and ResponseUsage.input_tokens_details.cache_write_tokens
// (required there) — and OpenRouter documents the same Chat field. Emitting it
// is therefore speaking the protocol, not extending it.
//
// The nesting is also what makes the count unambiguous. prompt_tokens_details
// is defined as "a breakdown of tokens used in the prompt", so placing the
// write inside it states structurally that it is PART OF prompt_tokens. A
// top-level sibling of prompt_tokens suggests the opposite, and that ambiguity
// is precisely why an inbound third-party cache-write count cannot be told
// apart from the hybrid dialect (see CacheSitsOutsidePrompt's KNOWN LIMITATION).
//
// Emission policy — strict out, liberal in:
//   - Chat and Responses egress emit ONLY this field, never both spellings: the
//     same number under two names invites a downstream to add them.
//   - Gemini egress keeps CacheWriteAliasField, because usageMetadata is flat
//     and has no details object to nest into.
//   - Every decoder accepts BOTH, with this one taking precedence.
const CacheWriteDetailField = "cache_write_tokens"

// AddTokenCounts sums upstream-reported token counts, reporting false if any
// count is negative or the sum would overflow. Both are reachable from the
// wire: JSON numbers are signed, and nothing bounds their magnitude.
//
// A wrapped sum compares as though it fits, which would turn an absurd count
// into a billable one, so callers must treat false as "these counts cannot be
// trusted" rather than falling back to raw arithmetic.
func AddTokenCounts(counts ...int) (int, bool) {
	sum := 0
	for _, n := range counts {
		if n < 0 || n > math.MaxInt-sum {
			return 0, false
		}
		sum += n
	}
	return sum, true
}

// HasNegativeCount reports whether any token count is negative, i.e. the record
// is impossible and must not be trusted.
//
// Streaming decoders use this to drop a bad usage frame rather than emit it.
// They cannot rely on the gateway's coherence check the way the non-streaming
// path does: every relay accumulator merges frames through IRUsage.Merge, which
// copies only values greater than zero, so a negative would be quietly rounded
// up to 0 and the surrounding counts billed as though nothing were wrong.
// Dropping the frame leaves usage unknown, which is the outcome the coherence
// check would have produced.
func HasNegativeCount(u IRUsage) bool {
	return u.PromptTokens < 0 || u.CompletionTokens < 0 || u.TotalTokens < 0 ||
		u.CacheReadTokens < 0 || u.CacheWriteTokens < 0 ||
		u.ReasoningTokens < 0 || u.WebSearchCount < 0
}

// CacheSitsOutsidePrompt reports whether a record that CLAIMS the inclusive
// convention actually follows the net one, stating its cache counts alongside
// the prompt rather than inside it.
//
// Every OpenAI-shaped decoder marks its usage CacheIncludedInPrompt=true,
// because the OpenAI schema defines cached_tokens as a subset of prompt_tokens.
// Plenty of OpenAI-compatible upstreams break that rule — anything fronting an
// Anthropic model especially, since Anthropic's input_tokens is already net of
// cache. They copy that net figure into prompt_tokens and report the cached
// portion only under prompt_tokens_details. Read as inclusive, such a record
// has its cache subtracted from a prompt the cache was never part of, so every
// consumer of the net input loses min(prompt, cache) tokens on EVERY request.
//
// Reclassifying requires the inclusive reading to be positively ruled out, and
// there are exactly two ways to do that. Either is enough on its own; slack in
// the stated total is not, because slack alone cannot tell the two conventions
// apart — a net-convention upstream leaves room for the cache, an inclusive one
// can leave room for anything this shape does not model, and the two look
// identical in the numbers.
//
//  1. The cache counts exceed the prompt. The inclusive convention defines both
//     cache lines as non-overlapping subsets OF the prompt (the read is the
//     part served from cache, the write the part newly stored), and subsets
//     cannot outgrow the set containing them, so the inclusive reading is not
//     merely unlikely here but impossible.
//  2. The stated total accounts for the cache on lines of its own, exactly:
//     prompt + completion + write + read == total. Under the inclusive
//     convention the cache already sits inside the prompt, so that sum would
//     overshoot the total by the whole cache. Requiring equality rather than
//     "fits inside" is what keeps unexplained slack from passing as evidence.
//
// Both are gated on the net reading fitting inside the stated total at all. A
// net sum that overshoots the upstream's own total is self-contradictory, and
// without the gate an absurd or over-reported cache count could talk its way
// into being billed through rule 1.
//
// The cache WRITE is weighed alongside the read rather than ignored. That was
// safe only while no inclusive-convention protocol had a cache-write concept;
// it stopped being true once this gateway began emitting and accepting the
// CacheWriteAliasField on those wires, where the write genuinely IS part of the
// reported prompt. Leaving it out would strand the write-only first cache fill
// — the case with no cache read at all — permanently unjudgeable.
//
// Every early return errs toward NOT reclassifying, deliberately: a missed
// reclassification undercounts the input line, while a wrong one bills the
// gross prompt at the input price AND the cache at the cache price, charging
// the same tokens twice.
//
// Left alone by design: a record with no cache at all (nothing to decide), one
// with no stated total (no evidence available), and negative or absurd counts
// (genuinely corrupt — the gateway rejects those separately).
//
// KNOWN LIMITATION, deliberately not addressed: read and write are weighed
// together, so an upstream using the hybrid dialect — cache read INSIDE the
// prompt but cache write beside it — is read as fully inclusive and its write
// portion is wrongly subtracted from the net input. Telling that dialect apart
// needs a separate total-based test for the write alone; that was evaluated and
// declined, since it would change the billing convention in both gateways for a
// dialect no current upstream is known to use. Only inbound third-party aliases
// are affected: what this gateway emits always puts the write inside the gross
// prompt. See the usage-cross-protocol-matrix doc.
func CacheSitsOutsidePrompt(u IRUsage) bool {
	if !u.CacheIncludedInPrompt {
		return false // already net; nothing to reclassify
	}
	cacheTotal, ok := AddTokenCounts(u.CacheReadTokens, u.CacheWriteTokens)
	if !ok || cacheTotal <= 0 || u.TotalTokens <= 0 {
		return false
	}
	netTotal, ok := AddTokenCounts(u.PromptTokens, u.CompletionTokens, u.CacheWriteTokens, u.CacheReadTokens)
	if !ok || netTotal > u.TotalTokens {
		return false
	}
	cacheCannotBeASubsetOfPrompt := cacheTotal > u.PromptTokens
	totalCountsCacheSeparately := netTotal == u.TotalTokens
	return cacheCannotBeASubsetOfPrompt || totalCountsCacheSeparately
}

// PromptIncludesCache reports the convention this record ACTUALLY follows,
// rather than the one its decoder claimed.
//
// The raw CacheIncludedInPrompt flag must never be read directly by anything
// deriving a token count. Decoders set it from the shape of the wire they
// parsed, before any of the evidence above has been weighed, so on the
// OpenAI-compatible upstreams that front an Anthropic model it is simply wrong.
// The gateway settles the same question once per request before billing and
// persisting; egress encoders run earlier than that and on a separate copy of
// the usage, so they have to settle it themselves. Routing both through this
// one predicate is what keeps the counts a client is shown and the counts
// written to the request log from disagreeing about the same request.
func (u IRUsage) PromptIncludesCache() bool {
	return u.CacheIncludedInPrompt && !CacheSitsOutsidePrompt(u)
}

// NetPromptTokens returns the input count in Anthropic's convention: cache
// read and cache write excluded, so that
// input_tokens + cache_creation_input_tokens + cache_read_input_tokens is the
// total input. Used by the Claude egress encoder.
//
// Cache WRITE is subtracted alongside cache read because a GROSS prompt count
// includes both. Standard OpenAI/Gemini upstreams have no cache-write concept
// so CacheWriteTokens is 0 there and the term is inert; it matters when the
// upstream reported the CacheWriteAliasField, where the write portion genuinely
// sits inside prompt_tokens.
//
// Floored at 0: a malformed upstream count must not yield a negative input.
func (u IRUsage) NetPromptTokens() int {
	if !u.PromptIncludesCache() {
		if u.PromptTokens < 0 {
			return 0
		}
		return u.PromptTokens
	}
	cacheTotal, ok := AddTokenCounts(u.CacheReadTokens, u.CacheWriteTokens)
	if !ok {
		return 0
	}
	n := u.PromptTokens - cacheTotal
	if n < 0 {
		return 0
	}
	return n
}

// GrossPromptTokens returns the input count in the OpenAI/Gemini convention —
// cached tokens included — and is the exact mirror of NetPromptTokens.
//
// Those protocols document their cache fields as a breakdown OF the prompt,
// i.e. a strict subset. So emitting a raw PromptTokens that came from an
// Anthropic upstream (CacheIncludedInPrompt=false, hence a NET count) drops
// the entire cache portion and produces the spec violation
// cached_tokens > prompt_tokens.
func (u IRUsage) GrossPromptTokens() int {
	if u.PromptIncludesCache() {
		if u.PromptTokens < 0 {
			return 0
		}
		return u.PromptTokens
	}
	g, ok := AddTokenCounts(u.PromptTokens, u.CacheWriteTokens, u.CacheReadTokens)
	if !ok {
		return 0
	}
	return g
}

// GrossTotalTokens returns total as a strict identity:
// GrossPromptTokens() + CompletionTokens.
//
// Every protocol emitted here defines total that way. A larger
// upstream-reported TotalTokens is deliberately NOT preferred: the excess is
// tokens that could not be attributed to either side, and passing it through
// emits total > prompt + completion, contradicting the spec and hiding the
// attribution bug rather than surfacing it.
func (u IRUsage) GrossTotalTokens() int {
	t, ok := AddTokenCounts(u.GrossPromptTokens(), u.CompletionTokens)
	if !ok {
		return 0
	}
	return t
}
