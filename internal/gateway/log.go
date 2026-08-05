package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"time"

	"github.com/yolorouter/yolorouter/internal/fact"
	"github.com/yolorouter/yolorouter/internal/model"
	"github.com/yolorouter/yolorouter/internal/protocols"
	"github.com/yolorouter/yolorouter/internal/repository"
	"github.com/yolorouter/yolorouter/pkg/logger"
	"go.uber.org/zap"
)

// generateRequestID returns a fresh 16-hex-char id for one gateway request
// (every failed response carries this so the admin can find the
// row). crypto/rand keeps it unguessable — it's surfaced to the caller, so a
// predictable counter would leak request volume / ordering.
func generateRequestID() string {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		// Fallback so a rand failure can never break request routing: epoch
		// nanos is still unique enough to find a log row.
		return fmt.Sprintf("%x", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

// microsPerUnit is the fixed-point scale for stored money: one CNY = 1e6
// integer micros, i.e. 6 decimal places. Mirrors the frontend's MICROS_PER_UNIT
// (utils/money.ts) — the two must agree.
const microsPerUnit = 1_000_000

// costBreakdown is the per-request cost result: the billed cost plus the two
// cache-economics figures the cost view surfaces. Known=false means usage or
// pricing was missing — every field is 0 but the row must NOT read as "free".
type costBreakdown struct {
	CostMicros int64
	// CacheReadSavedMicros: how much cheaper the cache-read tokens were than
	// reprocessing them at the input price. CacheWriteExtraMicros: the premium
	// paid to establish the cache versus billing those tokens at the input
	// price. Both non-negative; net cache saving = read saved − write extra.
	CacheReadSavedMicros  int64
	CacheWriteExtraMicros int64
	// CompressCostSavedMicros is the ESTIMATED input-cost reduction from
	// input compression — tokensSaved priced at the candidate's input unit
	// rate. It is an estimate for reporting only; it does NOT participate in
	// billing (CostMicros above is the billed amount for tokens actually
	// sent). Zero when usage/pricing is unknown or no tokens were saved.
	CompressCostSavedMicros int64
	Known                   bool
}

// computeCost returns the cost breakdown in integer micros (major-unit × 1e6,
// i.e. CNY to 6 decimal places) and whether the cost is "known".
// Unknown = usage missing — the row records cost_micros=0 with
// cost_known=false so the dashboard never shows it as a free request.
// compressTokensSaved is the input-compression estimate (chars-saved/4); the
// matching CompressCostSavedMicros line prices it at the candidate's input
// rate so the saving is reported on the same basis as the billed cost. It is
// forced to 0 whenever usage/pricing is unknown, matching CostKnown=false.
// Candidate prices are CNY per million tokens.
// netPromptTokens returns the billable/loggable net input token count: the
// prompt tokens with the cache-read portion removed. When the source protocol
// reports prompt tokens inclusive of cache reads (OpenAI/Gemini/Responses,
// CacheIncludedInPrompt=true), the cache-read count is subtracted; when it
// reports a net input already (Anthropic input_tokens, flag false),
// PromptTokens is returned unchanged. Floored at 0 so an upstream reporting
// cache > prompt can't produce a negative. This is the single source of truth
// for "net input" — both computeCost and the request_logs.input_tokens value
// go through it, so billing and the persisted count stay consistent across
// protocols.
//
// Cache WRITE is subtracted only under the inclusive convention, and only
// because this gateway now puts it there. It is NOT subtracted from a net
// (Anthropic) count, where cache_creation_input_tokens sits alongside
// input_tokens rather than inside it — deducting it there would remove tokens
// the prompt never contained.
//
// The inclusive case used to be unconditionally safe to ignore, since no
// standard protocol has a cache-write concept: OpenAI's prompt_tokens_details
// carries only cached_tokens and Gemini's promptTokenCount folds in only
// cachedContentTokenCount. That stopped being true once the gateway began
// emitting and accepting the cache_creation_input_tokens alias (see
// protocols.CacheWriteAliasField) on those wires, where the write portion
// genuinely IS part of the reported prompt total. Leaving it in would count
// those tokens twice: once as cache write, once as fresh input.
//
// Delegates to protocols.IRUsage.NetPromptTokens rather than repeating the
// subtraction, because the Claude egress encoder derives the same quantity
// through that method. Two implementations of one definition is how the
// input_tokens a client is shown and the input_tokens persisted to request_logs
// come to disagree about the same request.
func netPromptTokens(usage *Usage) int {
	if usage == nil {
		return 0
	}
	return usage.toIRUsage().NetPromptTokens()
}

// normalizeCacheConvention settles which cache-accounting convention a usage
// record actually follows, before anything prices or persists it.
//
// The decision itself lives in protocols.CacheSitsOutsidePrompt, which the
// egress encoders reach through IRUsage.PromptIncludesCache. Both sides MUST
// keep asking that one function: this gateway settles the question once per
// request against the persisted copy of the usage, while an encoder settles it
// per frame against the IR copy, long before this runs. A second implementation
// here would let the input_tokens a client is shown and the input_tokens
// written to request_logs disagree about the same request.
//
// Applied by mutating the flag rather than deferring to the predicate at every
// read, because the counts are consumed several times below (pricing, the
// persisted row, the compression denominator) and a partially-normalized record
// is exactly the inconsistency this exists to prevent.
func normalizeCacheConvention(u *Usage) {
	if u == nil {
		return
	}
	if protocols.CacheSitsOutsidePrompt(u.toIRUsage()) {
		u.CacheIncludedInPrompt = false
	}
}

// usageIsCoherent reports whether the upstream-reported counts describe a
// physically possible request. Counts arrive from third-party upstreams over
// the wire, so a buggy or hostile provider can send negatives (JSON numbers are
// signed) or a cache-read larger than the prompt it was supposedly read from.
// Either would flow straight into pricing: a negative cache count produces a
// negative line item, and an oversized one bills cache reads the request never
// performed. Incoherent usage is treated exactly like absent usage — unknown,
// never zero and never billed.
//
// Delegates to the single IR-level verdict (protocols.IRUsage.IsIncoherent) so
// the wire encoders and this billing gate read the SAME answer instead of each
// re-judging with its own predicate. The verdict was already computed at the
// decoder exit and carried on Invalid; round-tripping through toIRUsage lets
// this function evaluate the same predicate the encoder effectively used,
// catching the records where Merge erased the evidence before the mark was set.
// Both callers run normalizeCacheConvention first, so PromptIncludesCache (which
// IsIncoherent relies on) has the settled convention to read.
func usageIsCoherent(u *Usage) bool {
	if u == nil {
		return false
	}
	return !u.toIRUsage().IsIncoherent()
}

func computeCost(cand *model.ModelCandidate, usage *Usage, compressTokensSaved int) costBreakdown {
	if cand == nil || !usageIsCoherent(usage) {
		return costBreakdown{} // Known=false: no cost recorded, no budget consumed
	}
	// cost = net_input × input_price
	//      + cache_read × cache_read_price
	//      + cache_write × cache_write_price
	//      + completion × output_price
	// net_input is the non-cached input (netPromptTokens): cache tokens are
	// billed on their own lines below, so they must not also be charged at the
	// input price. Candidate prices are CNY per million tokens.
	cacheRead := usage.CacheReadTokens
	cacheWrite := usage.CacheWriteTokens
	nonCacheInput := netPromptTokens(usage)
	// Candidate without a configured cache price bills cache tokens at the
	// input price (when a candidate has no cache price configured, its cache tokens are billed at the input unit price).
	cacheReadPrice := cand.InputPrice
	if cand.CacheReadPrice != nil {
		cacheReadPrice = *cand.CacheReadPrice
	}
	cacheWritePrice := cand.InputPrice
	if cand.CacheWritePrice != nil {
		cacheWritePrice = *cand.CacheWritePrice
	}
	cost := float64(nonCacheInput)/1_000_000*cand.InputPrice +
		float64(cacheRead)/1_000_000*cacheReadPrice +
		float64(cacheWrite)/1_000_000*cacheWritePrice +
		float64(usage.CompletionTokens)/1_000_000*cand.OutputPrice
	// Cache economics, both against the input price as the "no cache" baseline.
	// Reads save when priced below input; writes cost a premium when priced
	// above it. Each is floored at 0 so a candidate whose cache price sits on
	// the wrong side of input never turns into a negative on the other line.
	cacheReadSaved := max(0, float64(cacheRead)/1_000_000*(cand.InputPrice-cacheReadPrice))
	cacheWriteExtra := max(0, float64(cacheWrite)/1_000_000*(cacheWritePrice-cand.InputPrice))
	// Scale CNY to integer micros so cumulative budget accounting stays
	// exact-integer (no float drift) while keeping 6-decimal cost precision.
	// (microsPerUnit is a distinct constant from the /1_000_000 above, which is
	// the "price per million tokens" divisor — same literal, different meaning.)
	// Compress savings use the same input rate: tokens × cand.InputPrice/M-tokens
	// — the /1e6 from per-M pricing and the ×1e6 to micros cancel, leaving
	// tokens × InputPrice (in micros). Reported only; not added to CostMicros.
	var compressSavedMicros int64
	if compressTokensSaved > 0 {
		compressSavedMicros = int64(float64(compressTokensSaved)*cand.InputPrice + 0.5)
	}
	// Converting an out-of-range float64 to int64 is undefined in Go — the
	// result is whatever the platform produces, which for a budget counter
	// means an arbitrary (possibly negative) charge. Coherent token counts can
	// still multiply into an out-of-range figure against an absurd unit price,
	// so the conversion is guarded rather than assumed safe.
	micros := cost*microsPerUnit + 0.5
	if micros < 0 || micros > math.MaxInt64 {
		return costBreakdown{}
	}
	return costBreakdown{
		CostMicros:              int64(micros),
		CacheReadSavedMicros:    int64(cacheReadSaved*microsPerUnit + 0.5),
		CacheWriteExtraMicros:   int64(cacheWriteExtra*microsPerUnit + 0.5),
		CompressCostSavedMicros: compressSavedMicros,
		Known:                   true,
	}
}

// safeUpstreamMessage produces the message shown to the caller for a 4xx
// non-auth upstream failure. The upstream body is NOT forwarded verbatim —
// it can echo back parts of the request (including the rewritten model) and,
// for some providers, fragments of credential detail. A bare
// "upstream returned status N" is enough for the caller to act on.
func safeUpstreamMessage(status int) string {
	return fmt.Sprintf("upstream returned status %d", status)
}

// finalize writes the request_logs row and, when cost is known and positive,
// accumulates the spend onto the API key's budget_spent_micros. Called on
// every exit path (success, every failure class) so each gateway request
// produces exactly one row. rc.candidate/Provider/Usage may be nil
// on early failures (before any candidate was tried); finalize is nil-safe
// for all of them.
//
// Budget accumulation uses the COST from this request, not a re-read of the
// row — two concurrent requests that each compute their own cost and add it
// atomically (repository.IncrementAPIKeyBudgetSpent is a single
// budget_spent_micros = budget_spent_micros + ? UPDATE) cannot lose updates to
// each other.
func (s *Service) finalize(rc *Exchange, statusCode int, failReason string, start time.Time) {
	if rc.logWritten.Swap(true) {
		return // already finalized (e.g. Handle's panic-recovery defer after a normal finalize)
	}
	rc.statusCode = statusCode
	// Stop the clock before any persistence runs. What follows writes to the
	// database, and a budget update waiting on a row lock would otherwise be
	// billed to the caller as request latency — inflating exactly the dashboard
	// someone consults to find out whether the gateway is slow.
	duration := time.Since(start)

	sink := newExchangeSink(rc)
	sink.reporter = "kernel"

	// Settle the cache convention once, here, before either consumer of the
	// usage reads it: pricing below and the persisted counts must not be able to
	// disagree about whether the prompt includes the cache. One normalization
	// point covers every ingress protocol and both the streaming and
	// non-streaming paths.
	//
	// A truncated stream can reach this with a partial record (the pumps keep
	// whatever usage arrived before the cut, deliberately), so the
	// reclassification must not depend on the record being complete — it
	// doesn't: a partial record whose stated total no longer corroborates the
	// net reading simply stays inclusive and is rejected as incoherent, exactly
	// as it was before.
	normalizeCacheConvention(rc.usage)
	cost := computeCost(rc.candidate, rc.usage, rc.compressEstimatedTokensSaved)

	s.reportUsage(rc, sink)
	sink.Note(fact.CostComputed{
		Known:                   cost.Known,
		Micros:                  cost.CostMicros,
		CacheReadSavedMicros:    cost.CacheReadSavedMicros,
		CacheWriteExtraMicros:   cost.CacheWriteExtraMicros,
		CompressCostSavedMicros: cost.CompressCostSavedMicros,
	})
	sink.Note(attemptsRecord(rc))
	if rc.compressEstimatedTokensSaved > 0 || len(rc.compressorsApplied) > 0 {
		sink.Note(fact.TokensSaved{
			Compressors:     rc.compressorsApplied,
			EstimatedTokens: rc.compressEstimatedTokensSaved,
		})
	}

	// Charging happens here rather than in a recorder: what the caller is billed
	// is not an audit concern, and a deployment that swapped out its audit trail
	// must not be able to stop collecting money by accident.
	if cost.Known && cost.CostMicros > 0 {
		if err := repository.IncrementAPIKeyBudgetSpent(s.db, rc.apiKeyID, cost.CostMicros); err != nil {
			logger.Error("gateway: increment budget spent failed",
				zap.String("request_id", rc.requestID), zap.Error(err))
		}
	}

	// The outcome is stored rather than handed straight to the recorders,
	// because recording must be the last thing that happens: admissions release
	// after this returns, and one that reports its own final accounting would
	// otherwise report it into a timeline nobody reads again. Handle arms the
	// recorder pass before it arms anything else, so it unwinds last.
	rc.outcome = fact.Outcome{
		StatusCode: statusCode,
		FailReason: failReason,
		Duration:   duration,
		Attempts:   len(rc.attempts),
		Delivered:  rc.firstByteSent,
	}
	rc.outcomeSettled = true
}

// recordTerminal hands the settled exchange to the recorders.
//
// It is separate from finalize, and runs later, because recording must be the
// last thing that happens: admissions release after finalize returns, and one
// that reports its own final accounting would otherwise report it into a
// timeline nobody reads again.
//
// A no-op when nothing settled the exchange, which means no work was ever done
// on its behalf and there is nothing to describe.
func (s *Service) recordTerminal(rc *Exchange) {
	if !rc.outcomeSettled {
		return
	}
	s.runRecorders(rc.requestCtx, rc, rc.outcome)
}

// reportUsage puts the billable counts on the timeline, or records that the
// upstream's own numbers contradicted themselves.
//
// An incoherent record is reported as such rather than persisted raw: a
// negative or impossible count poisons every SUM() the dashboard runs, and the
// same rejection is applied to pricing, so the stored counts can never disagree
// with the billing decision.
func (s *Service) reportUsage(rc *Exchange, sink fact.Sink) {
	if rc.usage == nil {
		return
	}
	if !usageIsCoherent(rc.usage) {
		logger.Warn("gateway: upstream reported incoherent token usage, counts dropped",
			zap.String("request_id", rc.requestID),
			zap.Int("prompt_tokens", rc.usage.PromptTokens),
			zap.Int("completion_tokens", rc.usage.CompletionTokens),
			zap.Int("cache_read_tokens", rc.usage.CacheReadTokens),
			zap.Int("cache_write_tokens", rc.usage.CacheWriteTokens))
		sink.Note(fact.UsageIncoherent{Reason: "upstream counts do not corroborate each other"})
		return
	}
	// The NET input (cache excluded) is what is persisted, so every
	// SUM(input_tokens) shares one convention across protocols and cache tokens
	// stay in their own columns.
	sink.Note(fact.UsageReported{
		Unit:       fact.UnitToken,
		Source:     fact.UsageFromUpstream,
		Prompt:     netPromptTokens(rc.usage),
		Completion: rc.usage.CompletionTokens,
		CacheRead:  rc.usage.CacheReadTokens,
		CacheWrite: rc.usage.CacheWriteTokens,
	})
}

// attemptsRecord carries the attempt count and, when anything ran, its detail.
// The detail is JSON so a query page can render it without a schema change.
func attemptsRecord(rc *Exchange) fact.AttemptsRecorded {
	rec := fact.AttemptsRecorded{Count: len(rc.attempts)}
	if len(rc.attempts) > 0 {
		if detail, err := json.Marshal(rc.attempts); err == nil {
			rec.Detail = string(detail)
		}
	}
	return rec
}
