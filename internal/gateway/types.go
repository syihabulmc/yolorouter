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
	"os"
	"sync"
	"sync/atomic"
	"time"

	"github.com/yolorouter/yolorouter/internal/fact"
	"github.com/yolorouter/yolorouter/internal/model"
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
	// upstreamURL is the full URL the gateway dispatched to for the current
	// candidate's attempt. Reset to "" at the start of each candidate in
	// relayCandidates so a build-failed candidate never inherits the previous
	// candidate's URL; set in attemptOne right before the request is sent.
	// makeAttempt stamps each AttemptRecord with it, and finalize copies it to
	// the request_logs.upstream_url column under the same "last attempt wins"
	// rule as UpstreamRequestBody.
	upstreamURL string
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
	// compressEstimatedTokensSaved / CompressorsApplied / RequestBodyCompressed
	// record the outcome of a successful compression pass. RequestBodyCompressed
	// is the compressed body that upstream encoding (buildUpstreamBody) uses as
	// its input via EffectiveRequestBody; RequestBody stays the verbatim caller
	// body for the audit row.
	compressEstimatedTokensSaved int
	compressorsApplied           []string
	requestBodyCompressed        []byte
	// wantsStreamUsage is true when the caller set
	// stream_options.include_usage=true. Controls whether usage frames
	// collected upstream are forwarded to the caller (the gateway always
	// requests usage upstream for its own cost accounting, but only
	// forwards it when the caller asked).
	wantsStreamUsage bool
	apiKeyID         uint

	// requestDeadline is the absolute cutoff for the whole request across all
	// failover candidates (the request_timeout budget). Set once at Handle
	// entry as now + gateway.RequestTimeout; each upstream attempt reads it
	// to derive its own per-attempt cap as min(attempt_timeout,
	// time.Until(requestDeadline)) so a request near its total budget can't
	// start a fresh full-length attempt. Zero before Handle assigns it.
	requestDeadline time.Time

	// requestCtx is the context carrying RequestDeadline, set once at Handle
	// entry. Candidate queries (model/candidate/key GORM reads) and each
	// per-attempt context derive from this, so a stalled DB cannot overrun
	// the total request budget. Without this, the GORM calls used s.db with
	// no deadline and a stuck query could block past RequestDeadline.
	requestCtx context.Context

	// Current-attempt target (overwritten on each candidate switch).
	candidate *model.ModelCandidate
	provider  *model.Provider

	statusCode int // set by finalize when the log row is written

	// contentInspectionStatus / ContentInspectionErrType describe the MOST
	// RECENT candidate only — relayCandidates clears them at the top of each
	// iteration, alongside Provider/UpstreamURL and for the same reason, so
	// they are set if and only if the candidate that just ran was refused by
	// the upstream's content inspection. Every other failover reason is an
	// upstream fault
	// whose generic 502 is the honest answer once the chain is exhausted; a
	// moderation refusal is a verdict on the request, so reporting it as "all
	// upstream candidates failed" would hide the one thing the caller can act
	// on. Zero means the last attempt was not such a refusal and the ordinary
	// terminal applies.
	contentInspectionStatus  int
	contentInspectionErrType string

	// usage from the successful attempt, if any — drives cost + the log row.
	usage *Usage

	// attempts records every candidate try in order.
	attempts []AttemptRecord

	// timeline is the append-only log of everything capabilities reported
	// during this exchange. The kernel owns it: capabilities report through a
	// sink and never hold it, which is what keeps provenance stamping and
	// ordering in one place.
	timeline fact.Timeline

	// rewriteSteps records, in order, every egress rewrite that actually
	// changed the body for the current candidate. Reset alongside the body
	// it describes.
	rewriteSteps []rewriteStep

	// firstByteSent flips true once any byte has been written to the client
	// (after this, no more Key/candidate switching is allowed).
	firstByteSent bool

	// logWritten guards finalize against double-write: Handle installs a
	// panic-recovery defer that calls finalize if no normal path did, and
	// finalize itself is idempotent via this flag (exactly one row
	// per request, even under panic).
	logWritten atomic.Bool

	mu sync.Mutex // protects FirstByteSent flips from racing the flusher

	// Bodies captured for the request_log_bodies row.
	// v0.1 stores them VERBATIM — body content is not scrubbed (only request
	// headers are masked; see RequestHeaders below). requestBody is set as
	// soon as the caller body is read. UpstreamRequestBody is overwritten on
	// each attempt (success => successful attempt; total failure => last
	// attempt). ResponseBody is the caller-FACING response (post-rewrite,
	// post-usage-strip, including local error JSON); UpstreamResponseBody is
	// the raw upstream response (non-stream full / non-2xx error body
	// bounded-read). For stream, the sent SSE is appended to streamBodyFile
	// instead and dispatchPassthroughStream clears these two so they stay empty.
	// Nil/empty on early failure or body-read failure.
	requestBody          []byte
	upstreamRequestBody  []byte
	responseBody         []byte
	upstreamResponseBody []byte
	// requestHeaders is the caller's request headers as a JSON object, with
	// sensitive headers already masked (SanitizeHeaders). This header-name
	// masking is the ONLY redaction v0.1 does — body content above is stored
	// verbatim. Captured once at Handle entry so it survives even an early
	// rejection.
	requestHeaders []byte

	// streamBodyFile/streamBodyCaptured/streamBodyTruncated are the
	// stream-only counterpart of the four body fields above: the sent SSE
	// lines are appended to streamBodyFile as
	// they go out instead of being buffered in memory. streamBodyCaptured is
	// true once a capture file was successfully opened for this request —
	// finalize derives the persisted stream_body_path from RequestID
	// (simplification: the path is always exactly "<request_id>.stream", so
	// this field only ever needs to answer "was a file captured?", not carry
	// the string itself). streamBodyTruncated flips true only if the 1GiB
	// anti-OOM backstop was hit (never a silent content cut). Unexported —
	// accessed only from within this package (stream.go/relay.go).
	streamBodyFile      *os.File
	streamBodyCaptured  bool
	streamBodyTruncated bool
	// streamBodyBytesWritten mirrors the capture file's current size so
	// appendStreamBodyLine can check the 1GiB backstop with a plain integer
	// comparison instead of an os.File.Stat() syscall per appended line
	// (a chat stream can append hundreds of lines, each otherwise costing
	// its own Stat() call).
	streamBodyBytesWritten int64
}

// MarkFirstByteSent flips FirstByteSent true under the lock. Returns whether
// this call was the one that flipped it — the stream path uses that to decide
// whether a mid-stream upstream error can still switch (no) or must be
// surfaced inline (yes).
func (rc *Exchange) MarkFirstByteSent() bool {
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

// EffectiveRequestBody returns the body that upstream encoding should use as
// its input: the compressed body when a successful compression pass produced
// one, otherwise the verbatim caller body. buildUpstreamBody reads this
// instead of RequestBody directly so that both the passthrough (model-field
// rewrite) and cross-protocol (IR decode/encode) paths consume the compressed
// body without every call site branching.
func (rc *Exchange) EffectiveRequestBody() []byte {
	if rc.requestBodyCompressed != nil {
		return rc.requestBodyCompressed
	}
	return rc.requestBody
}

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
	// verdict. Not serialized — internal accounting only.
	ReasoningTokens int `json:"-"`
}
