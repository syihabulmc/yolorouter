// Package gateway implements the OpenAI-compatible /v1/chat/completions
// relay. This is the second auth path — independent of the admin
// session — and routes caller requests through the model's candidate chain
// to an upstream provider, with Key rotation and candidate failover before
// the first streamed byte.
//
// v0.1 is OpenAI-in / OpenAI-out only, so there is no IR /
// cross-protocol layer: the request body is forwarded with only the model
// field swapped to the candidate's provider_model_name, and every model
// field in the response is rewritten back to the external name.
package gateway

import (
	"context"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yolorouter/yolorouter/internal/decision"
	"github.com/yolorouter/yolorouter/internal/fact"
	"github.com/yolorouter/yolorouter/internal/gateway/attempt"
	"github.com/yolorouter/yolorouter/internal/gateway/capture"
	"github.com/yolorouter/yolorouter/internal/protocols"
)

// Exchange holds per-request relay state for the gateway pass-through,
// key rotation, failover, and request logging.
type Exchange struct {
	requestID     string
	originalModel string // external model name; every response model field is rewritten to this
	isStream      bool
	// ingress is the wire protocol of the caller's request path, computed
	// once in Handle from c.Request.URL.Path. Every gateway call site that
	// already has rc in scope reads this instead of recomputing it.
	ingress protocols.ProtocolID
	// ingressPath is the caller's request path, captured at Handle entry. The
	// custom system prompt injection allowlist keys off it: Gemini's route is
	// a wildcard :modelaction and the ingress protocol falls back to
	// ProtocolOpenAI for non-generateContent actions, so the path alone
	// distinguishes countTokens / embedContent from real chat.
	ingressPath string
	// isChatEndpoint is computed once in Handle from IngressPath. Both the
	// compression gate and the CSP injection gate read this bool instead of
	// recomputing isChatEndpoint(path) independently.
	isChatEndpoint bool
	// customSystemPromptEnabled / CustomSystemPrompt are the two-level-resolved
	// prompt for this request. Empty or disabled means no injection.
	customSystemPromptEnabled bool
	customSystemPrompt        string
	// compressEnabled is the two-level-resolved input-compression switch for
	// this request. When true and the ingress path is a chat endpoint, the
	// caller's body is run through the compress engine before relay.
	compressEnabled bool
	// compressSkipReason records why compression was skipped (when it was
	// enabled but the engine returned Skipped=true). Empty when compression
	// was disabled, applied successfully, or never attempted.
	compressSkipReason string
	// compressEstimatedTokensSaved / CompressorsApplied record the outcome of
	// a successful compression pass; the compressed body itself lives in the
	// bodies capture (what admission is handed and the payload encodes from,
	// while the verbatim caller body stays for the audit row).
	compressEstimatedTokensSaved int
	compressorsApplied           []string
	apiKeyID                     uint
	// concurrencyLimit / rpmLimit are the caller's allowance, resolved once
	// from the key. Zero means unlimited, which is also what an absent limit
	// means — the distinction has no consumer, so it is not preserved.
	concurrencyLimit int
	rpmLimit         int

	// requestDeadline is the absolute cutoff for the whole request across all
	// failover candidates (the request_timeout budget). Set once at Handle
	// entry as now + gateway.RequestTimeout; each upstream attempt reads it
	// to derive its own per-attempt cap as min(attempt_timeout,
	// time.Until(requestDeadline)) so a request near its total budget can't
	// start a fresh full-length attempt. Zero before Handle assigns it.
	requestDeadline time.Time

	// attemptsSpent and probesSpent are the request's count-budget ledger,
	// the decision table's BudgetEffect made real: attempts count upstream
	// dispatches, probes count candidates abandoned before anything was
	// sent. The exchange records spending only — the limits live on the
	// service's gateway config, so a zero-valued Exchange starts with a full
	// budget rather than an exhausted one, and no reporter can refresh what
	// was spent. Spending happens in one place (spendBudget), driven by the
	// resolved decision's Budget effect, so what a judgement costs is the
	// table's call rather than each call site's.
	attemptsSpent int
	probesSpent   int

	// circuitGen is the health-record generation the current candidate's
	// provider was admitted under, set when the candidate loop consults the
	// breaker and handed back when a result is booked, so a result that
	// straddled a breaker transition cannot be booked against the wrong era.
	circuitGen uint64

	// requestCtx is the context carrying RequestDeadline, set once at Handle
	// entry. Candidate queries (model/candidate/key GORM reads) and each
	// per-attempt context derive from this, so a stalled DB cannot overrun
	// the total request budget. Without this, the GORM calls used s.db with
	// no deadline and a stuck query could block past RequestDeadline.
	requestCtx context.Context

	// attempt is the current attempt's identity and outcome: candidate,
	// provider, dispatch URL, and the verdict held for the terminal to quote.
	// The attempt package owns every write and the two lifetimes inside the
	// family (the verdict clears before a loop's early exits; the rest resets
	// only when a candidate is actually entered) — see its package comment.
	// The verdict slot names no capability: which verdicts are worth quoting
	// is a property of the decision table, and a second capability wanting
	// that treatment adds a table row, not a field here.
	attempt attempt.State

	statusCode int // set by finalize when the log row is written

	// attempts records every candidate try in order; recordAttempt is the one
	// place it grows. Usage itself is not held here — the successful
	// delivery's usage travels as a finalize parameter.
	attempts []AttemptRecord

	// timeline is the append-only log of everything capabilities reported
	// during this exchange. The kernel owns it: capabilities report through a
	// sink and never hold it, which is what keeps provenance stamping and
	// ordering in one place.
	timeline fact.Timeline

	// outcome is what finalize settled on, held until every release has run so
	// the recorders see a timeline nothing will be appended to.
	outcome        fact.Outcome
	outcomeSettled bool

	// firstByteSent flips true once any byte has been written to the client
	// (after this, no more Key/candidate switching is allowed).
	firstByteSent bool

	// logWritten guards finalize against double-write: Handle installs a
	// panic-recovery defer that calls finalize if no normal path did, and
	// finalize itself is idempotent via this flag (exactly one row
	// per request, even under panic).
	logWritten atomic.Bool

	mu sync.Mutex // protects FirstByteSent flips from racing the flusher

	// bodies is the audit record for the request_log_bodies row: the caller's
	// request, its compressed variant, what was sent upstream, what came
	// back, what the caller received, and the stream capture file. v0.1
	// stores them VERBATIM — body content is not scrubbed (only request
	// headers are masked; see RequestHeaders below). The capture package owns
	// every write, including the stream file's name (capture.StreamFileName,
	// reported back through StreamName); this struct only holds it.
	bodies capture.Bodies
	// requestHeaders is the caller's request headers as a JSON object, with
	// sensitive headers already masked (SanitizeHeaders). This header-name
	// masking is the ONLY redaction v0.1 does — body content is stored
	// verbatim. Captured once at Handle entry so it survives even an early
	// rejection.
	requestHeaders []byte
}

// markFirstByteSent flips firstByteSent true under the lock. Returns whether
// this call was the one that flipped it — the stream path uses that to decide
// whether a mid-stream upstream error can still switch (no) or must be
// surfaced inline (yes).
//
// Unexported because nothing outside this package calls it, and what it sets is
// worth more than most: `Delivered: rc.firstByteSent` is what the admissions are
// released against, so a caller that flipped it would turn off a refund.
//
// This narrows the surface; it does not close it. Capabilities reach an Exchange
// through a bind function they write themselves, and a bind that hands over the
// Exchange itself reaches every exported method on it — SetResponseBody and the
// other body mutators among them, which stay exported because the protocol layer
// calls them. Nothing here can prevent that. What keeps a capability honest is
// the narrow view it binds, which is a property of the assembly and not of this
// file.
// spendBudget books the count budget a resolved decision asks for. One spend
// point for every call site keeps the cost of a judgement the table's call: a
// path cannot decide its own price, and nothing ever books a refund.
func (rc *Exchange) spendBudget(b decision.BudgetEffect) {
	switch b {
	case decision.BudgetConsumeAttempt:
		rc.attemptsSpent++
	case decision.BudgetConsumeProbe:
		rc.probesSpent++
	}
}

func (rc *Exchange) markFirstByteSent() bool {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	if rc.firstByteSent {
		return false
	}
	rc.firstByteSent = true
	return true
}

// The getters below exist for capabilities, which read exchange state through
// the narrow view each one declares for itself rather than by reaching into
// the struct. They are added as capabilities ask for them, not pre-emptively:
// a getter with no caller is a field that has quietly stayed public.

// APIKeyID identifies the caller's key.
func (rc *Exchange) APIKeyID() uint { return rc.apiKeyID }

// ConcurrencyLimit is how many of this key's requests may be in flight at once.
// Zero means unlimited.
func (rc *Exchange) ConcurrencyLimit() int { return rc.concurrencyLimit }

// RPMLimit is how many requests this key may make per minute. Zero means
// unlimited.
func (rc *Exchange) RPMLimit() int { return rc.rpmLimit }

// CustomSystemPromptEnabled reports whether a prompt was resolved for this
// request, from either the global setting or a per-key override.
func (rc *Exchange) CustomSystemPromptEnabled() bool { return rc.customSystemPromptEnabled }

// CustomSystemPrompt returns the resolved prompt text, empty when none applies.
func (rc *Exchange) CustomSystemPrompt() string { return rc.customSystemPrompt }

// IsChatEndpoint reports whether the caller's route is one where a system
// prompt means anything. Computed once from the request path, because the
// answer cannot change mid-exchange and recomputing it invites two call sites
// to disagree.
func (rc *Exchange) IsChatEndpoint() bool { return rc.isChatEndpoint }

// The getters below serve the recorder, which needs the widest view of any
// capability — an audit row is by nature a summary of everything. That width is
// not a failure of the split: it is a list, written in the recorder's own
// package, of exactly what it reads, which is what the previous arrangement
// (any code in the package reaching for any field) could never produce.

// RequestID is the id shared with the access log and the caller's error
// messages, so one identifier locates a request everywhere it was recorded.
func (rc *Exchange) RequestID() string { return rc.requestID }

// OriginalModel is the model name the caller asked for.
func (rc *Exchange) OriginalModel() string { return rc.originalModel }

// IsStream reports whether the caller asked for a streamed response.
func (rc *Exchange) IsStream() bool { return rc.isStream }

// IngressPath is the caller's request path.
func (rc *Exchange) IngressPath() string { return rc.ingressPath }

// UpstreamURL is where the last attempt was dispatched, empty if none was.
func (rc *Exchange) UpstreamURL() string { return rc.attempt.UpstreamURL() }

// ProviderID identifies the provider of the last attempt, nil when no candidate
// was reached.
func (rc *Exchange) ProviderID() *uint {
	p := rc.attempt.Provider()
	if p == nil {
		return nil
	}
	id := p.ID
	return &id
}

// CompressSkipReason says why compression declined, empty when it did not.
func (rc *Exchange) CompressSkipReason() string { return rc.compressSkipReason }

// RequestHeaders is the masked header capture.
func (rc *Exchange) RequestHeaders() []byte { return rc.requestHeaders }

// RequestBody is the caller's body, verbatim.
func (rc *Exchange) RequestBody() []byte { return rc.bodies.Request() }

// CompressedRequestBody is the post-compression body, empty when none was made.
func (rc *Exchange) CompressedRequestBody() []byte { return rc.bodies.CompressedRequest() }

// UpstreamRequestBody is what the last attempt sent.
func (rc *Exchange) UpstreamRequestBody() []byte { return rc.bodies.UpstreamRequest() }

// ResponseBody is what the caller received.
func (rc *Exchange) ResponseBody() []byte { return rc.bodies.Response() }

// UpstreamResponseBody is what the upstream returned, unaltered.
func (rc *Exchange) UpstreamResponseBody() []byte { return rc.bodies.UpstreamResponse() }

// StreamBodyPath is where a streamed response was captured, empty when the
// response was not streamed or nothing was captured.
func (rc *Exchange) StreamBodyPath() string { return rc.bodies.StreamName() }

// StreamBodyTruncated reports whether the stream capture hit its cap.
func (rc *Exchange) StreamBodyTruncated() bool { return rc.bodies.StreamTruncated() }

// AttemptRecord is one candidate try (the log keeps every attempt,
// not just the final one). Outcome is one of the AttemptOutcome* constants.
type AttemptRecord struct {
	CandidateID       uint   `json:"candidate_id"`
	ProviderID        uint   `json:"provider_id"`
	ProviderName      string `json:"provider_name"`
	ProviderModelName string `json:"provider_model_name"`
	KeyID             uint   `json:"key_id"`
	KeyLabel          string `json:"key_label"`
	StatusCode        int    `json:"status_code"`
	Outcome           string `json:"outcome"`
	FailReason        string `json:"fail_reason"`
	// UpstreamURL is the full URL this attempt dispatched to. Empty for
	// attempts that failed before any request was sent (provider missing,
	// negotiate / build / decrypt failures) — they never reached an upstream.
	UpstreamURL string `json:"upstream_url"`
}

// Attempt outcomes — drive both the log's fail_reason text and the relay
// loop's switch decision.
const (
	AttemptSuccess     = "success"
	AttemptAuthFailed  = "auth_failed"  // 401 from upstream -> rotate Key
	AttemptRateLimited = "rate_limited" // 429 -> rotate Key
	AttemptConnError   = "conn_error"   // network/timeout -> failover candidate
	// AttemptServerError/AttemptBadStatus were coined for the two status classes
	// named below and have both outgrown those names: they are now also written
	// for stream cuts, decode failures, disabled providers and undecryptable
	// keys. What separates them depends on where the record comes from — see
	// attemptOutcomeFor for the delivery paths, classifyUpstreamStatus for the
	// status ones. Kept as they are because the attempts table has always shown
	// these two words.
	AttemptServerError = "server_error" // 5xx -> failover candidate
	AttemptClientError = "client_error" // 4xx (non-auth) -> do NOT switch
	AttemptBadStatus   = "bad_status"   // unmapped non-2xx -> do NOT switch
	// AttemptContentFiltered is the one 4xx that DOES switch: the upstream's
	// input inspection refused the payload, which another candidate may not.
	AttemptContentFiltered = "content_filtered"
)

// Usage is the token usage pulled from an OpenAI-compatible response or
// final SSE chunk. Prompt/Completion/Total are the
// always-present totals; CacheWrite/CacheRead are the prompt-cache counts
// some upstreams report, driving the cache line items in computeCost.
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
	// CacheWriteTokens / CacheReadTokens are the prompt-cache counts some
	// upstreams report (OpenAI exposes cache READ via
	// prompt_tokens_details.cached_tokens; Anthropic splits cache writes via
	// cache_creation_input_tokens). They drive the cache line items in
	// computeCost. Zero when the upstream didn't report them.
	CacheWriteTokens int `json:"cache_write_tokens"`
	CacheReadTokens  int `json:"cache_read_tokens"`
	// CacheIncludedInPrompt marks whether PromptTokens already counts the cache
	// tokens. OpenAI-shaped upstreams report prompt_tokens inclusive of cache
	// reads (true); Anthropic's input_tokens is the net non-cached count
	// (false). It covers the cache WRITE too, which is not the free-standing
	// count it once was: this gateway both emits and accepts
	// protocols.CacheWriteAliasField on OpenAI-shaped wires, where the write is
	// part of the reported prompt.
	//
	// As decoded this is only a claim, taken from the wire shape alone — the
	// OpenAI-compatible upstreams that front an Anthropic model report a net
	// prompt under an inclusive-looking schema. normalizeCacheConvention
	// (log.go) settles it once per request, before anything reads a count from
	// it. netPromptTokens then derives the billable/logged net input, so the
	// value persisted to request_logs.input_tokens is always the net count
	// regardless of origin protocol. Not serialized — internal accounting only.
	CacheIncludedInPrompt bool `json:"-"`
	// Invalid carries protocols.IRUsage.Invalid across the bridge: an upstream
	// reported something impossible and no count here may be billed or
	// persisted. Not serialized — internal accounting only.
	Invalid bool `json:"-"`
	// ReasoningTokens carries the IR reasoning-token count across the bridge so
	// the coherence verdict (run via toIRUsage) can see a negative one. Without
	// it a record the wire encoder refused (HasNegativeCount sees the negative
	// reasoning count and emits null) would still bill here, since the bridge
	// used to drop the field and the billing gate could not re-derive the
	// verdict.
	//
	// It must ALSO survive the delivery round trip (usageReportOf and back):
	// settlement re-runs the coherence verdict on the copy that travelled with
	// the delivery, and a hop that drops this field silently un-condemns a
	// record on its way to being priced. Not serialized — internal accounting
	// only.
	ReasoningTokens int `json:"-"`
	// WebSearchCount carries protocols.IRUsage.WebSearchCount across the bridge.
	// It is not a token count and nothing prices it, but it is the only record
	// that the provider ran searches it charges for, and the frames it was read
	// from do not survive the delivery. Not serialized — internal accounting
	// only.
	WebSearchCount int `json:"-"`
}

// beginUpstreamAttempt drops whatever the previous send left on the exchange,
// so nothing this attempt did not produce is read as belonging to it.
//
// The boundary is the send, not the candidate: a single candidate rotates
// through its provider's keys, and each rotation is a real request with its own
// response. Tying invalidation to the candidate would leave one key's leftovers
// standing while the next key runs.
//
// The captured bodies move on this boundary. The
// audit row has one field for them and a request may make several attempts, so
// whatever ends up stored is read as belonging to the attempt the row describes.
// Keeping the last body that happened to exist meant a chain whose final
// attempt never reached an upstream still stored an EARLIER provider's error
// response, with nothing in the row saying it came from somewhere else. The
// per-attempt records carry each attempt's own status and error text, so
// storing nothing here loses the raw payload of an already-recorded failure —
// while storing the wrong one invites a diagnosis of the wrong provider.
//
// Keeping every attempt's body would lose nothing at all, and is the better
// answer. It is not this change: bodies live in their own table precisely
// because they are large, so attributing them per attempt is a schema change,
// not a boundary fix.
func (rc *Exchange) beginUpstreamAttempt() {
	rc.bodies.BeginUpstreamAttempt()
	rc.attempt.BeginUpstreamAttempt()
}
