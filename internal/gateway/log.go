package gateway

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"
	"time"

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
func usageIsCoherent(u *Usage) bool {
	if u == nil {
		return false
	}
	// A decoder already ruled this record impossible — typically a streaming
	// frame whose bad counts would otherwise have been erased by IRUsage.Merge
	// before reaching the checks below.
	if u.Invalid {
		return false
	}
	if u.PromptTokens < 0 || u.CompletionTokens < 0 || u.CacheReadTokens < 0 ||
		u.CacheWriteTokens < 0 || u.TotalTokens < 0 {
		return false
	}
	// Only meaningful when the prompt total is supposed to contain the cache
	// lines; under Anthropic's convention they are independent counts. This is
	// also the sole bound on them, so it has to stay: a record that
	// normalizeCacheConvention declined to reclassify still claims the cache sits
	// inside the prompt, and an unbounded cache line prices straight into the
	// bill and into the key's budget.
	//
	// Both lines are bounded together rather than the read alone, because the
	// inclusive convention can also carry a cache write (see
	// protocols.CacheWriteAliasField). Bounding only the read would leave the
	// write free to exceed the prompt, which drives netPromptTokens to its zero
	// floor — the whole input billed as cache write, at a cache-write count the
	// request never performed.
	//
	// Must run AFTER normalizeCacheConvention, and both callers do. The same
	// inequality is rule 1 of the reclassification, so on a record carrying a
	// stated total this is unreachable: an oversized cache proves the record is
	// net, the flag has already been cleared, and the counts are kept. What
	// survives to be rejected here is the narrow remainder — an oversized cache
	// with no stated total to corroborate it, i.e. no evidence either way. That
	// stays a rejection on purpose: dropping the counts records the request at
	// cost_known=false, which is visible, whereas guessing net would bill a
	// cache the request may never have performed.
	// Gated on a stated total: without one normalizeCacheConvention cannot
	// adjudicate the convention, so "cache exceeds prompt" is ambiguous rather
	// than impossible. A full cache hit legitimately reports prompt 0 / total 0
	// alongside a real cache read.
	// The exception is narrow on purpose: only when PromptTokens is 0. There the
	// two readings agree — net input is 0 either way — so accepting costs
	// nothing, and it is exactly the legitimate full-cache-hit shape
	// (prompt 0 / total 0 / cache_read N). With a NON-zero prompt the readings
	// disagree about the bill (net says PromptTokens, inclusive says 0), so the
	// record is genuinely ambiguous and refusing to bill it is the safe answer.
	if cacheTotal, ok := protocols.AddTokenCounts(u.CacheReadTokens, u.CacheWriteTokens); u.CacheIncludedInPrompt &&
		(u.TotalTokens > 0 || u.PromptTokens != 0) && (!ok || cacheTotal > u.PromptTokens) {
		return false
	}
	// When the upstream states a total, the parts have to fit inside it. This
	// is the only bound on the completion count — nothing else can tell an
	// inflated output figure from a genuinely long generation. It bites on the
	// passthrough path, where the upstream's own total survives; on the IR path
	// IRUsage.Merge has already raised a too-small total to prompt+completion.
	parts, ok := protocols.AddTokenCounts(u.PromptTokens, u.CompletionTokens)
	if !ok || (u.TotalTokens > 0 && parts > u.TotalTokens) {
		return false
	}
	return true
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
// produces exactly one row. rc.Candidate/Provider/Usage may be nil
// on early failures (before any candidate was tried); finalize is nil-safe
// for all of them.
//
// Budget accumulation uses the COST from this request, not a re-read of the
// row — two concurrent requests that each compute their own cost and add it
// atomically (repository.IncrementAPIKeyBudgetSpent is a single
// budget_spent_micros = budget_spent_micros + ? UPDATE) cannot lose updates to
// each other.
func (s *RelayService) finalize(rc *RelayContext, statusCode int, failReason string, start time.Time) {
	if rc.logWritten.Swap(true) {
		return // already finalized (e.g. Handle's panic-recovery defer after a normal finalize)
	}
	rc.StatusCode = statusCode
	durationMs := time.Since(start).Milliseconds()
	// Settle the cache convention once, here, before either consumer of the
	// usage reads it: pricing below and the persisted counts further down must
	// not be able to disagree about whether the prompt includes the cache. One
	// normalization point covers every ingress protocol and both the streaming
	// and non-streaming paths.
	//
	// A truncated stream can reach this with a partial record (the pumps keep
	// whatever usage arrived before the cut, deliberately), so the reclassification
	// must not depend on the record being complete — it doesn't: a partial record
	// whose stated total no longer corroborates the net reading simply stays
	// inclusive and is rejected as incoherent, exactly as it was before.
	normalizeCacheConvention(rc.Usage)
	cost := computeCost(rc.Candidate, rc.Usage, rc.CompressEstimatedTokensSaved)

	var providerID *uint
	if rc.Provider != nil {
		id := rc.Provider.ID
		providerID = &id
	}
	var failPtr *string
	if failReason != "" {
		fr := failReason
		failPtr = &fr
	}
	apiKeyID := rc.APIKeyID
	var inputTokens, outputTokens, cacheWriteTokens, cacheReadTokens int
	switch {
	case rc.Usage == nil:
		// Leave every count at 0; CostKnown is already false.
	case !usageIsCoherent(rc.Usage):
		// Same rejection computeCost applies, so the persisted counts can never
		// disagree with the billing decision. Storing the raw values would let a
		// negative or impossible count poison every SUM() the dashboard and
		// analytics pages run.
		logger.Warn("gateway: upstream reported incoherent token usage, counts dropped",
			zap.String("request_id", rc.RequestID),
			zap.Int("prompt_tokens", rc.Usage.PromptTokens),
			zap.Int("completion_tokens", rc.Usage.CompletionTokens),
			zap.Int("cache_read_tokens", rc.Usage.CacheReadTokens),
			zap.Int("cache_write_tokens", rc.Usage.CacheWriteTokens))
	default:
		// Persist the NET input (cache excluded) so request_logs.input_tokens
		// and every SUM(input_tokens) aggregate share one convention across
		// protocols — cache tokens live in their own columns. Same source of
		// truth as billing (computeCost also goes through netPromptTokens).
		inputTokens = netPromptTokens(rc.Usage)
		outputTokens = rc.Usage.CompletionTokens
		cacheWriteTokens = rc.Usage.CacheWriteTokens
		cacheReadTokens = rc.Usage.CacheReadTokens
	}

	// Compression savings are only meaningful when the request actually reached
	// upstream (len(rc.Attempts) > 0 — the relay loop appends at least one
	// attempt before sending). A request that compressed the body but was then
	// rejected pre-relay (no routable candidate, model not found, allowlist
	// denied) must NOT inflate compress-rate / savings metrics: zero the
	// savings fields and clear the compressors list. CompressSkipReason is
	// kept unconditionally — it is audit-only and useful even on pre-relay
	// failures to diagnose why compression was bypassed.
	compressTokensSaved := rc.CompressEstimatedTokensSaved
	compressCostSavedMicros := cost.CompressCostSavedMicros
	compressorsApplied := joinCompressors(rc.CompressorsApplied)
	if len(rc.Attempts) == 0 {
		compressTokensSaved = 0
		compressCostSavedMicros = 0
		compressorsApplied = ""
	}

	logRow := &model.RequestLog{
		RequestID:                        rc.RequestID,
		APIKeyID:                         &apiKeyID,
		ModelName:                        rc.OriginalModel,
		ProviderID:                       providerID,
		IsStream:                         rc.IsStream,
		StatusCode:                       statusCode,
		InputTokens:                      inputTokens,
		OutputTokens:                     outputTokens,
		CacheWriteTokens:                 cacheWriteTokens,
		CacheReadTokens:                  cacheReadTokens,
		CostMicros:                       cost.CostMicros,
		CostKnown:                        cost.Known,
		CacheReadSavedMicros:             cost.CacheReadSavedMicros,
		CacheWriteExtraMicros:            cost.CacheWriteExtraMicros,
		CompressEstimatedTokensSaved:     compressTokensSaved,
		CompressEstimatedCostSavedMicros: compressCostSavedMicros,
		CompressSkipReason:               rc.CompressSkipReason,
		CompressorsApplied:               compressorsApplied,
		RequestPath:                      rc.IngressPath,
		UpstreamURL:                      rc.UpstreamURL,
		FailReason:                       failPtr,
		Attempts:                         len(rc.Attempts),
		DurationMs:                       durationMs,
	}
	// Keep every attempt's order / key label / failure cause, not
	// just the count. Stored as JSON so the query page can render it
	// later without a schema change; empty when no attempt ran (pre-check
	// failure before any candidate was tried).
	if len(rc.Attempts) > 0 {
		if detail, mErr := json.Marshal(rc.Attempts); mErr == nil {
			s := string(detail)
			logRow.AttemptsDetail = &s // *string so empty stays SQL NULL, not ''
		}
	}
	if err := repository.CreateRequestLog(s.db, logRow); err != nil {
		logger.Error("gateway: write request log failed",
			zap.String("request_id", rc.RequestID), zap.Error(err))
	}
	if cost.Known && cost.CostMicros > 0 {
		if err := repository.IncrementAPIKeyBudgetSpent(s.db, rc.APIKeyID, cost.CostMicros); err != nil {
			logger.Error("gateway: increment budget spent failed",
				zap.String("request_id", rc.RequestID), zap.Error(err))
		}
	}

	// Record obtainable request/response bodies
	// (stored verbatim; v0.1 does not scrub body content — only request
	// headers are masked, via SanitizeHeaders). Idempotent UPSERT (UNIQUE
	// request_id) so retry/double-call never duplicates. Best-effort: a body-
	// write failure is logged only — the billing row (above) is authoritative
	// and must not roll back on a body failure.
	//
	// streamBodyPath is derived here (not stored on rc) rather than kept as a
	// second string field alongside streamBodyCaptured (simplification: the
	// path is always exactly "<request_id>.stream" — see rc.streamBodyCaptured's
	// doc comment in types.go).
	streamBodyPath := ""
	if rc.streamBodyCaptured {
		streamBodyPath = rc.RequestID + ".stream"
	}
	bodyRow := &model.RequestLogBody{
		RequestID:             rc.RequestID,
		RequestHeaders:        string(rc.RequestHeaders),
		RequestBody:           string(rc.RequestBody),
		UpstreamRequestBody:   string(rc.UpstreamRequestBody),
		ResponseBody:          string(rc.ResponseBody),
		UpstreamResponseBody:  string(rc.UpstreamResponseBody),
		StreamBodyPath:        streamBodyPath,
		StreamBodyTruncated:   rc.streamBodyTruncated,
		CompressedRequestBody: string(rc.RequestBodyCompressed),
	}
	if err := repository.UpsertRequestLogBody(s.db, bodyRow); err != nil {
		logger.Error("gateway: write request log body failed",
			zap.String("request_id", rc.RequestID), zap.Error(err))
	}
}

// joinCompressors collapses the per-block list of compressors that fired into
// the comma-joined string stored in request_logs.compressors_applied. The
// compress engine appends one entry per modified block, so the same compressor
// can appear multiple times when it shrank several blocks; per-block
// occurrences are preserved (NOT deduped) so downstream stats can count how
// many times each compressor actually fired. Empty input yields "" (SQL
// default).
func joinCompressors(applied []string) string {
	if len(applied) == 0 {
		return ""
	}
	parts := make([]string, 0, len(applied))
	for _, name := range applied {
		if name == "" {
			continue
		}
		parts = append(parts, name)
	}
	return strings.Join(parts, ",")
}
