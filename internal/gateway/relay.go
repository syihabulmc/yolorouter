package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/yolorouter/yolorouter/internal/compress"
	"github.com/yolorouter/yolorouter/internal/config"
	"github.com/yolorouter/yolorouter/internal/model"
	"github.com/yolorouter/yolorouter/internal/protocols"
	"github.com/yolorouter/yolorouter/internal/repository"
	"github.com/yolorouter/yolorouter/pkg/crypto"
	"github.com/yolorouter/yolorouter/pkg/logger"
)

// maxNonStreamResponseBytes caps a single non-stream upstream response body.
// A buggy or hostile provider can return an arbitrarily large body; without
// this cap io.ReadAll would grow the buffer until OOM before the request
// timeout fires (the response body has no bodylimit guard the way the
// request body does). Mirrors the provider-test client's bound
// (provider_client.go). Read up to N+1 so an overflow is detectable.
const maxNonStreamResponseBytes = 32 * 1024 * 1024 // 32 MiB

// BodyAuditCap bounds every early-rejection audit read of the caller's
// request body (captureRejectedBody here, and middleware.logAuthRejection's
// own read for the auth-gate rejection paths — the two
// packages each defined their own identical copy of this constant, which
// nothing enforced staying in sync). Exported so middleware can share this
// single definition instead of duplicating it. Mirrors the /v1 route group's
// middleware.BodySizeLimit(20<<20) (router.go) — this is a memory-safety cap
// on our read, not a re-enforcement of that limit (http.MaxBytesReader
// already enforces it upstream of us, before this code ever runs).
const BodyAuditCap = 20 << 20 // 20 MiB

// ReadAuditBody drains r (bounded by BodyAuditCap) — the shared bounded-read
// step the post-auth early-rejection audit paths need (captureRejectedBody
// below: revoked/expired/budget/RPM/concurrency, all for an ALREADY-
// authenticated caller who is rate-limited). Best-effort: nil on a read error
// or a nil/absent body. v0.1 stores the caller body verbatim — no content
// scrubbing (only request headers are masked, via SanitizeHeaders).
func ReadAuditBody(r io.Reader) []byte {
	return ReadAuditBodyCapped(r, BodyAuditCap+1)
}

// ReadAuditBodyCapped is ReadAuditBody with a caller-chosen byte ceiling, so
// the UNauthenticated auth-rejection path (middleware.logAuthRejection) can
// bound its capture far below BodyAuditCap: without a valid key, an attacker
// could otherwise make the gateway read + persist a 20 MiB body per rejected
// request and inflate request_log_bodies without ever authenticating.
// Best-effort: nil on a read error or a nil/absent
// body, never an error the caller must handle.
func ReadAuditBodyCapped(r io.Reader, limit int64) []byte {
	if r == nil {
		return nil
	}
	b, err := io.ReadAll(io.LimitReader(r, limit))
	if err != nil {
		return nil
	}
	return b
}

// captureRejectedBody drains the caller request body for the audit row, so
// records the request body even when the request is rejected before
// the normal body read (revoked/expired/budget/concurrency/RPM, all before
// io.ReadAll in Handle).
func captureRejectedBody(c *gin.Context, rc *Exchange) {
	if rc.requestBody != nil {
		return // already captured (e.g. body read succeeded then a later check failed)
	}
	if c.Request == nil {
		return
	}
	if body := ReadAuditBody(c.Request.Body); body != nil {
		rc.requestBody = body
	}
}

// testHookHandleDone, when non-nil, is invoked with the Exchange at the
// end of every Handle call (success or failure). Test-only wiring — Handle
// intentionally doesn't expose its internal Exchange in its public
// signature, so tests needing to inspect it (e.g. the captured request/
// response bodies) set this hook instead. Always nil in production.
var testHookHandleDone func(*Exchange)

// Service is the gateway orchestrator. One instance lives for the
// process lifetime (created in router.New); it owns the DB, the master key
// for decrypting provider keys, an upstream HTTP client, and the in-memory
// rate limiter.
type Service struct {
	db        *gorm.DB
	masterKey []byte
	client    *UpstreamClient
	limiter   *Limiter
	// settingsProvider is the read-only window into the cached global custom
	// system prompt. Nil when no provider is wired in (router passes nil until
	// the system settings service is registered); Handle nil-checks before
	// reading it, so a nil provider simply means no global prompt is applied.
	settingsProvider SettingsProvider
	// gateway carries the resolved gateway timeouts (connect/header/body-idle/
	// attempt/request) from config.GatewayConfig. The per-attempt timeout
	// orchestration (attemptOne, RequestDeadline) reads the individual fields
	// off this struct instead of re-deriving them per call.
	gateway config.GatewayConfig
}

// NewService wires the gateway with the already-decoded AES master key
// (the same one provider_service uses to decrypt the keys it now routes to).
// allowPrivate is forwarded to the upstream client's SSRF transport (config.
// SecurityConfig.AllowPrivateUpstreams) so LAN/localhost providers relay.
// sp is the read-only custom system prompt provider; nil is valid and
// disables global prompt injection (per-key overrides still apply).
// gatewayCfg is the resolved config.GatewayConfig; its ConnectTimeout seeds
// the upstream transport's TCP dial bound and its HeaderTimeout seeds the
// ResponseHeaderTimeout, while the remaining fields are read by the
// per-attempt timeout orchestration.
func NewService(db *gorm.DB, masterKey []byte, allowPrivate bool, sp SettingsProvider, gatewayCfg config.GatewayConfig) *Service {
	return &Service{
		db:               db,
		masterKey:        masterKey,
		client:           NewUpstreamClient(allowPrivate, gatewayCfg.HeaderTimeout, gatewayCfg.ConnectTimeout, gatewayCfg.TLSHandshakeTimeout),
		limiter:          NewLimiter(),
		settingsProvider: sp,
		gateway:          gatewayCfg,
	}
}

// requestIDFor returns the request id the RequestID middleware already
// generated (uuid, set on the gin context + X-Request-Id header), so the
// gateway's error messages and request_logs row share ONE id with the
// access log — not a second unrelated id. Falls back to a fresh hex id only
// if some route mounted Service without the RequestID middleware.
func requestIDFor(c *gin.Context) string {
	if id := c.GetString("request_id"); id != "" {
		return id
	}
	return generateRequestID()
}

// isClientDisconnected reports whether the CALLER's own connection was
// canceled, as opposed to a derived context (e.g. rc.requestCtx, which also
// expires when the request-level budget in RequestDeadline runs out).
// Checking c.Request.Context().Err() directly — rather than inspecting the
// error a failing DB call returns — is what makes this distinction
// possible: c.Request.Context() only ever becomes Canceled when the client
// itself hangs up, never when a context derived from it (with its own
// shorter deadline) simply times out on its own schedule. Mirrors the
// disconnect check already used around the body read and the upstream send
// (Handle, attemptOne) in this package.
func isClientDisconnected(c *gin.Context) bool {
	return errors.Is(c.Request.Context().Err(), context.Canceled)
}

// Handle is POST /v1/chat/completions. apiKey is the already-authenticated
// caller key (middleware.APIKeyAuth resolved and validated it). The handler
// runs the full pipeline: pre-checks → model lookup → allowlist →
// validate → candidate chain with Key rotation + failover → response rewrite
// → log. Every exit path writes exactly one request_logs row via finalize.
func (s *Service) Handle(c *gin.Context, apiKey *model.APIKey) {
	start := time.Now()
	rc := &Exchange{
		requestID: requestIDFor(c),
		apiKeyID:  apiKey.ID,
	}
	// Stamp the per-request total-budget deadline up front, before any
	// attempt logic, so every exit path (including early rejections below)
	// carries a consistent RequestDeadline. Each upstream attempt
	// derives its own cap as min(attempt_timeout, time.Until(RequestDeadline)).
	rc.requestDeadline = time.Now().Add(s.gateway.RequestTimeout)
	// Derive a context carrying the total-budget deadline so candidate
	// queries (model/candidate/key GORM reads) and each per-attempt context
	// are all bounded by RequestDeadline. Without this, the GORM calls used
	// s.db with no deadline and a stuck query could block past the request
	// cap. The per-attempt ctx in attemptOne derives from this, so
	// RequestCtx deadline => attempt ctx deadline too.
	requestCtx, requestCancel := context.WithDeadline(c.Request.Context(), rc.requestDeadline)
	defer requestCancel()
	rc.requestCtx = requestCtx
	// The ingress protocol is a property of the request path, computed once
	// up front so every error write in this function (and the pre-candidate
	// validation below) uses the wire envelope the caller actually expects
	// instead of always assuming OpenAI.
	ingress := IngressProtocol(c.Request.URL.Path)
	rc.ingress = ingress
	// Capture the raw path for the custom system prompt injection allowlist.
	// Gemini's route is a wildcard :modelaction, so the path (not the resolved
	// protocol) is the only thing that distinguishes generateContent from
	// countTokens / embedContent.
	rc.ingressPath = c.Request.URL.Path
	// Compute once so the compression gate and the CSP injection gate read
	// the bool instead of recomputing IsChatEndpoint(path) per call site.
	rc.isChatEndpoint = IsChatEndpoint(rc.ingressPath)
	// Put rc on the gin context so WriteOpenAIError*
	// (called from many exit paths below, and potentially from further down
	// the chain) can stash the local error JSON into rc.responseBody without
	// every call site threading an *Exchange parameter through.
	c.Set(relayContextKey, rc)
	// Capture the caller's request headers once at entry (masked
	// via SanitizeHeaders) so even an early rejection below still records
	// them. c.Request is always non-nil here (gin populates it), but guard
	// anyway for direct-call tests.
	if c.Request != nil {
		rc.requestHeaders = SanitizeHeaders(c.Request.Header)
	}
	// Panic-recovery safety net: if any sub-call panics (nil
	// deref, index OOB, type assertion), gin's Recovery middleware catches
	// it upstream, but finalize would otherwise never run and the request
	// would leave no audit/cost row. finalize is idempotent (logWritten
	// guard), so a normal-exit finalize first + this defer on panic writes
	// exactly one row either way.
	defer func() {
		if !rc.logWritten.Load() {
			s.finalize(rc, http.StatusInternalServerError, "panic_recovered", start)
		}
		// Test-only hook: Handle doesn't return its internal Exchange, so
		// tests that need to assert on the captured bodies (RequestBody/
		// UpstreamRequestBody/ResponseBody/UpstreamResponseBody)
		// hook in here instead of depending on DB persistence. Never
		// set outside _test.go.
		if testHookHandleDone != nil {
			testHookHandleDone(rc)
		}
	}()

	if !s.checkKeyStateAndLimits(c, rc, apiKey, start) {
		return
	}
	// Concurrency is the only limit that needs a paired release — acquire it
	// here and defer the release so every return path below frees the slot.
	if apiKey.ConcurrencyLimit != nil && *apiKey.ConcurrencyLimit > 0 {
		if !s.limiter.AcquireConcurrency(apiKey.ID, *apiKey.ConcurrencyLimit) {
			captureRejectedBody(c, rc)
			WriteIngressError(c, ingress, http.StatusTooManyRequests, errTypeRateLimit, "concurrency limit exceeded", rc.requestID)
			s.finalize(rc, http.StatusTooManyRequests, "concurrency_limit", start)
			return
		}
		defer s.limiter.ReleaseConcurrency(apiKey.ID)
	}
	// RPM is checked AFTER concurrency so a concurrency-rejected request does
	// NOT also burn an RPM token (the previous order — RPM in
	// checkKeyStateAndLimits before concurrency — let one served request
	// exhaust the whole minute's RPM under concurrent load).
	if apiKey.RPMLimit != nil && *apiKey.RPMLimit > 0 {
		if !s.limiter.CheckRPM(apiKey.ID, *apiKey.RPMLimit, time.Now()) {
			captureRejectedBody(c, rc)
			WriteIngressError(c, ingress, http.StatusTooManyRequests, errTypeRateLimit, "rate limit exceeded (requests per minute)", rc.requestID)
			s.finalize(rc, http.StatusTooManyRequests, "rpm_exceeded", start)
			return
		}
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		// Caller disconnect during body upload is terminal 499 (mirrors the
		// stream/non-stream response paths), not a malformed-request 400.
		if errors.Is(c.Request.Context().Err(), context.Canceled) {
			s.finalize(rc, 499, "client_disconnected", start)
			return // caller is gone; no response to write
		}
		// http.MaxBytesReader (BodySizeLimit middleware) rejects an oversized
		// body with *http.MaxBytesError — surface that as 413 (OpenAI
		// convention) so SDK clients can shrink and retry, instead of 400.
		status := http.StatusBadRequest
		message := "failed to read request body"
		reason := "read_body: " + err.Error()
		var maxErr *http.MaxBytesError
		if errors.As(err, &maxErr) {
			status = http.StatusRequestEntityTooLarge
			message = "request body exceeds the size limit"
			reason = "body_too_large"
		}
		WriteIngressError(c, ingress, status, errTypeInvalidRequest, message, rc.requestID)
		s.finalize(rc, status, reason, start)
		return
	}
	// Stash the caller-facing request body for the
	// request_log_bodies row, verbatim (v0.1 does not scrub body content).
	rc.requestBody = body

	// Gemini carries neither model nor stream in the body -- both are
	// encoded in the URL path
	// (/v1beta/models/{model}:generateContent|:streamGenerateContent) -- so
	// they must be pulled out here and threaded into peekIngress instead of
	// being read off the body like every other ingress protocol. A path
	// parseGeminiPath rejects (missing prefix, no recognized action, empty
	// model) is a structurally invalid request; reject it as a 400 the same
	// way an unparseable body is rejected just below, before the body is
	// even peeked.
	var pathModel string
	var pathStream bool
	if ingress == protocols.ProtocolGemini {
		gm, gs, ok := parseGeminiPath(c.Request.URL.Path)
		if !ok {
			WriteIngressError(c, ingress, http.StatusBadRequest, errTypeInvalidRequest, "invalid request path", rc.requestID)
			s.finalize(rc, http.StatusBadRequest, "invalid_gemini_path", start)
			return
		}
		pathModel, pathStream = gm, gs
	}

	// One lightweight per-ingress peek of the caller body — meta.Model/Stream
	// for routing, meta.validate() for the top-level structural checks each
	// protocol's decoder is lenient about, meta.HasTools for the capability
	// filter. The body itself (in `body`) is forwarded to buildUpstreamBody
	// untouched, which rewrites the model field (passthrough) or does the
	// full IR decode/encode (cross-protocol). The full protocol-specific
	// structural decode runs later, once, via validateIngressBody below.
	// pathModel/pathStream are only consumed by the Gemini branch (see
	// peekIngress); every other ingress protocol reads model/stream from the
	// body itself and ignores these two parameters.
	meta, err := peekIngress(ingress, body, pathModel, pathStream)
	if err != nil {
		WriteIngressError(c, ingress, http.StatusBadRequest, errTypeInvalidRequest, "invalid request body", rc.requestID)
		s.finalize(rc, http.StatusBadRequest, "parse: "+err.Error(), start)
		return
	}
	if meta.Model == "" {
		WriteIngressError(c, ingress, http.StatusBadRequest, errTypeInvalidRequest, "model is required", rc.requestID)
		s.finalize(rc, http.StatusBadRequest, "empty_model", start)
		return
	}
	rc.originalModel = meta.Model
	rc.isStream = meta.Stream
	rc.wantsStreamUsage = meta.WantsStreamUsage

	// Streaming relay loops (IRStreamRelay / IRStreamRelayJSONLines /
	// passthrough pumps) call ApplyStreamWriteDeadline before each
	// Write/Flush batch to slide the write deadline forward. This bounds a
	// slow-reading client without clearing the server WriteTimeout entirely.
	// The server WriteTimeout (RequestTimeout + 60s) covers the pre-first-
	// write gap (e.g. a long TTFT on a reasoning model); once the first
	// write lands, the sliding per-write deadline takes over. Non-streaming
	// endpoints keep the server WriteTimeout as a slow-read DoS guard.

	// Resolve the two-level custom system prompt: a per-key override wins
	// outright (short-circuit so a stalled settings read can't block an
	// override key); otherwise fall through to the global cached value.
	// On global read error leave the prompt disabled — fail-open behavior
	// guidance, never block the request on a settings hiccup.
	if apiKey.CustomSystemPromptEnabledOverride {
		rc.customSystemPromptEnabled = apiKey.CustomSystemPromptEnabled
		rc.customSystemPrompt = apiKey.CustomSystemPrompt
	} else if s.settingsProvider != nil {
		g, _, err := s.settingsProvider.CustomSystemPrompt(c.Request.Context())
		if err != nil {
			// Fail-open: the service returns last-known-good (or zero/disabled
			// on cold start) alongside the error; apply it so a transient
			// settings hiccup never downgrades behavior, and log for
			// observability. The negative-TTL in the service ensures this
			// log fires at most once per failure window, not per request.
			logger.Warn("gateway: custom system prompt read failed",
				zap.String("request_id", rc.requestID), zap.Error(err))
		}
		rc.customSystemPromptEnabled = g.Enabled
		rc.customSystemPrompt = g.Text
	}

	// Two-level resolve for input compression, mirroring the custom system
	// prompt resolve above: a per-key override wins outright (short-circuit
	// so a stalled settings read can't block an override key); otherwise fall
	// through to the global cached value. On global read error leave
	// compression disabled — fail-open, never block the request on a settings
	// hiccup.
	if apiKey.CompressEnabledOverride {
		rc.compressEnabled = apiKey.CompressEnabled
	} else if s.settingsProvider != nil {
		enabled, _, err := s.settingsProvider.GetInputCompression(c.Request.Context())
		if err != nil {
			logger.Warn("gateway: input compression read failed",
				zap.String("request_id", rc.requestID), zap.Error(err))
		}
		rc.compressEnabled = enabled
	}

	// Step 4: model exists and is enabled. A model disabled by an admin
	// must not route even if its candidates are still enabled.
	m, err := repository.FindModelByName(s.db.WithContext(requestCtx), meta.Model)
	if err != nil {
		if isClientDisconnected(c) {
			// The client hung up while this query was in flight — a
			// context.Canceled from the DB driver here is a disconnect, not
			// a server-side DB fault; nothing to write back to a gone caller.
			s.finalize(rc, 499, "client_disconnected", start)
			return
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			WriteIngressError(c, ingress, http.StatusNotFound, errTypeNotFound, "model does not exist", rc.requestID)
			s.finalize(rc, http.StatusNotFound, "model_not_found", start)
			return
		}
		logger.Error("gateway: find model", zap.String("request_id", rc.requestID), zap.Error(err))
		WriteIngressError(c, ingress, http.StatusInternalServerError, errTypeServer, "internal error", rc.requestID)
		s.finalize(rc, http.StatusInternalServerError, "db_model: "+err.Error(), start)
		return
	}
	if m.ManagementStatus != model.ModelStatusEnabled {
		WriteIngressError(c, ingress, http.StatusNotFound, errTypeNotFound, "model does not exist", rc.requestID)
		s.finalize(rc, http.StatusNotFound, "model_disabled", start)
		return
	}

	// Step 5: allowlist. A key flagged allow_all_models skips the per-model
	// check and may call any enabled model.
	if !apiKey.AllowAllModels {
		allowed, err := repository.HasAPIKeyModelAccess(s.db.WithContext(requestCtx), apiKey.ID, m.ID)
		if err != nil {
			if isClientDisconnected(c) {
				s.finalize(rc, 499, "client_disconnected", start)
				return
			}
			logger.Error("gateway: allowlist", zap.String("request_id", rc.requestID), zap.Error(err))
			WriteIngressError(c, ingress, http.StatusInternalServerError, errTypeServer, "internal error", rc.requestID)
			s.finalize(rc, http.StatusInternalServerError, "db_allowlist: "+err.Error(), start)
			return
		}
		if !allowed {
			WriteIngressError(c, ingress, http.StatusForbidden, errTypePermission, "model is not in this API key's allowlist", rc.requestID)
			s.finalize(rc, http.StatusForbidden, "model_not_allowed", start)
			return
		}
	}

	// Step 6: top-level structural validation (messages non-empty, Claude's
	// max_tokens invariant, OpenAI's parsedRequest.validate() rules).
	if err := meta.validate(); err != nil {
		WriteIngressError(c, ingress, http.StatusBadRequest, errTypeInvalidRequest, err.Error(), rc.requestID)
		s.finalize(rc, http.StatusBadRequest, "validate: "+err.Error(), start)
		return
	}

	// Step 6.5: full protocol-specific structural decode (message content
	// shapes, tool schemas, ...) — the same decode the request will
	// eventually go through on the hot path. Run once here, BEFORE any
	// candidate is picked, so a malformed body is rejected as a 400 client
	// error instead of surfacing later as a misleading "all upstream
	// candidates failed" 502 once relayCandidates has already started.
	if err := validateIngressBody(ingress, body, rc.originalModel, rc.isStream); err != nil {
		WriteIngressError(c, ingress, http.StatusBadRequest, errTypeInvalidRequest, "invalid request body: "+err.Error(), rc.requestID)
		s.finalize(rc, http.StatusBadRequest, "invalid_request: "+err.Error(), start)
		return
	}

	// Run input compression on the validated caller body when enabled and the
	// ingress path is a chat endpoint. Compression mutates only user/tool
	// content text — the system field CSP injection later appends to is
	// orthogonal, so running compress first is safe regardless of whether CSP
	// injection subsequently runs inside buildUpstreamBody. A skipped result
	// (no live zone, timeout, parse error, ...) leaves the body untouched; the
	// engine also recovers panics internally and returns the original body.
	if rc.compressEnabled && rc.isChatEndpoint {
		opts := compress.DefaultOptions()
		cctx, cancel := context.WithTimeout(c.Request.Context(), opts.Timeout)
		newBody, cres := compressByIngress(ingress, cctx, body, opts)
		cancel()
		if cres.Skipped {
			rc.compressSkipReason = string(cres.SkipReason)
		} else {
			rc.requestBodyCompressed = newBody
			rc.compressEstimatedTokensSaved = cres.EstimatedTokensSaved
			rc.compressorsApplied = cres.CompressorsApplied
		}
	}

	// Step 7: candidates filtered by requested capability.
	allCandidates, err := repository.ListModelCandidatesByModelID(s.db.WithContext(requestCtx), m.ID)
	if err != nil {
		if isClientDisconnected(c) {
			s.finalize(rc, 499, "client_disconnected", start)
			return
		}
		logger.Error("gateway: list candidates", zap.String("request_id", rc.requestID), zap.Error(err))
		WriteIngressError(c, ingress, http.StatusInternalServerError, errTypeServer, "internal error", rc.requestID)
		s.finalize(rc, http.StatusInternalServerError, "db_candidates: "+err.Error(), start)
		return
	}
	routable, anyEnabled := filterCandidates(allCandidates)
	if len(routable) == 0 {
		reason := "no_enabled_candidate"
		if anyEnabled {
			reason = "no_verified_candidate"
		}
		WriteIngressError(c, ingress, http.StatusServiceUnavailable, errTypeUnavailable, "model is not available", rc.requestID)
		s.finalize(rc, http.StatusServiceUnavailable, reason, start)
		return
	}

	// Steps 8–12.
	s.relayCandidates(c, rc, routable, start)
}

// checkKeyStateAndLimits runs the pre-call checks that don't need a paired
// release: status (revoked), expiry, budget (read-only here — the gateway
// writes the spend in finalize), and RPM. Concurrency is handled separately
// in Handle because it needs a deferred release. captureRejectedBody is
// called on each rejection (these three checks all run
// before Handle's normal body read, so the audit row would otherwise have an
// empty request_body).
func (s *Service) checkKeyStateAndLimits(c *gin.Context, rc *Exchange, apiKey *model.APIKey, start time.Time) bool {
	ingress := rc.ingress
	if apiKey.Status == model.APIKeyStatusRevoked {
		captureRejectedBody(c, rc)
		WriteIngressError(c, ingress, http.StatusUnauthorized, errTypeAuthentication, "API key revoked", rc.requestID)
		s.finalize(rc, http.StatusUnauthorized, "revoked", start)
		return false
	}
	if apiKey.ExpiresAt != nil && apiKey.ExpiresAt.Before(time.Now().UTC()) {
		captureRejectedBody(c, rc)
		WriteIngressError(c, ingress, http.StatusUnauthorized, errTypeAuthentication, "API key expired", rc.requestID)
		s.finalize(rc, http.StatusUnauthorized, "expired", start)
		return false
	}
	if apiKey.BudgetLimitMicros != nil && apiKey.BudgetSpentMicros >= *apiKey.BudgetLimitMicros {
		captureRejectedBody(c, rc)
		WriteIngressError(c, ingress, http.StatusTooManyRequests, errTypeInsufficientQuota, "budget limit exceeded", rc.requestID)
		s.finalize(rc, http.StatusTooManyRequests, "budget_exceeded", start)
		return false
	}
	return true
}

// relayCandidates walks the candidate chain in sort_order. For each
// candidate it loads the provider's enabled keys, decrypts them one at a
// time, and sends the upstream request; Key rotation and candidate failover
// decisions come back from tryKeys.
func (s *Service) relayCandidates(c *gin.Context, rc *Exchange, candidates []model.ModelCandidate, start time.Time) {
	// The ingress protocol is a property of the request path, not of any
	// individual candidate — threaded through every candidate/key attempt
	// below.
	ingress := rc.ingress
	for i := range candidates {
		cand := candidates[i]
		// Cleared ABOVE the budget gate below, not with the other per-candidate
		// fields further down: the gate exits the loop mid-iteration, so a reset
		// placed after it would be skipped exactly when the previous candidate
		// had just been refused — and the request would be reported as a content
		// refusal when what actually ended it was running out of time. Normal
		// exhaustion is unaffected: the last candidate clears these on entry and
		// sets them again itself if it is refused.
		rc.contentInspectionStatus = 0
		rc.contentInspectionErrType = ""
		// Per-request total-budget gate: RequestDeadline is the hard cap that
		// spans every candidate and key rotation. Checking it only at the
		// first attempt left later candidates reachable after the budget had
		// already elapsed, burning wall-clock on a chain that could never
		// succeed. Stop walking as soon as the budget is gone and fall
		// through to allCandidatesFailed so the caller sees the same 502
		// without the extra latency.
		if !rc.requestDeadline.IsZero() && time.Until(rc.requestDeadline) <= 0 {
			break
		}
		rc.candidate = &cand
		// Reset per iteration: rc.provider is set only when this candidate's
		// provider is usable, so a `continue` path (provider missing/disabled,
		// load-keys failed, no enabled key, rewrite failed) doesn't leave a
		// stale provider from a previous iteration on rc — which finalize
		// would otherwise record as the "final hit provider" of an all-failed
		// request. rc.upstreamURL is reset the same way so a candidate that
		// fails before sending (negotiate / build) never inherits the previous
		// candidate's URL in its AttemptRecord or the upstream_url column. The
		// content-inspection fields belong to the same family — see their reset
		// above the budget gate, which is why they are not repeated here.
		rc.provider = nil
		rc.upstreamURL = ""

		provider := cand.Provider
		if provider == nil {
			rc.attempts = append(rc.attempts, rc.makeAttempt(cand, nil, nil, 0, AttemptBadStatus, "provider missing (preload)"))
			continue
		}
		if provider.ManagementStatus != model.ProviderStatusEnabled {
			rc.attempts = append(rc.attempts, rc.makeAttempt(cand, provider, nil, 0, AttemptBadStatus, "provider disabled"))
			continue
		}
		rc.provider = provider

		keys, err := repository.ListProviderKeysByProvider(s.db.WithContext(rc.requestCtx), provider.ID)
		if err != nil {
			if isClientDisconnected(c) {
				// The client is gone — stop walking the candidate chain
				// entirely rather than burning the remaining candidates
				// only to land on allCandidatesFailed's 502; record 499
				// instead, mirroring attemptOne's disconnect handling.
				rc.attempts = append(rc.attempts, rc.makeAttempt(cand, provider, nil, 0, AttemptConnError, "client disconnected"))
				s.finalize(rc, 499, "client_disconnected", start)
				return
			}
			logger.Error("gateway: list provider keys", zap.String("request_id", rc.requestID), zap.Error(err))
			rc.attempts = append(rc.attempts, rc.makeAttempt(cand, provider, nil, 0, AttemptBadStatus, "load keys failed"))
			continue
		}
		enabled, anyEnabledKey := filterEnabledKeys(keys)
		if len(enabled) == 0 {
			reason := "no enabled key"
			if anyEnabledKey {
				reason = "no verified key"
			}
			rc.attempts = append(rc.attempts, rc.makeAttempt(cand, provider, nil, 0, AttemptBadStatus, reason))
			continue
		}

		// Step 9: negotiate the wire protocol to speak to this candidate's
		// provider — the ingress protocol when the provider accepts it
		// directly (passthrough, no IR round trip), otherwise the
		// provider's own primary protocol (buildDispatchRequest/
		// processDispatchResponseNonStream do the IR decode/encode).
		egress, err := Negotiate(ingress, provider)
		if err != nil {
			rc.attempts = append(rc.attempts, rc.makeAttempt(cand, provider, nil, 0, AttemptBadStatus, "negotiate egress: "+err.Error()))
			continue // mapping failure -> skip candidate
		}

		// Build the upstream body/URL once per candidate — it only depends
		// on the candidate/egress choice (model rewrite OR IR decode/encode
		// + stream-usage injection + URL), not on which key ends up sending
		// it, so every key attempt below reuses the same bytes instead of
		// rebuilding them.
		outBody, url, err := s.buildUpstreamBody(rc, ingress, egress)
		if err != nil {
			rc.attempts = append(rc.attempts, rc.makeAttempt(cand, provider, nil, 0, AttemptBadStatus, "build request: "+err.Error()))
			continue // build failure -> skip candidate, nothing sent yet
		}

		if s.tryKeys(c, rc, &cand, provider, enabled, egress, outBody, url, start) == outcomeDone {
			return
		}
		// outcomeNextCandidate: fall through to the next candidate.
	}
	s.allCandidatesFailed(c, rc, start)
}

// relayOutcome is what tryKeys reports back to relayCandidates.
type relayOutcome int

const (
	outcomeDone          relayOutcome = iota // response written, relay finished
	outcomeNextCandidate                     // this candidate's keys are exhausted, try next
)

// tryKeys walks one provider's enabled keys, sending the same pre-built
// upstream body/URL (outBody/url — built once per candidate by
// relayCandidates via buildUpstreamBody) with each key's own auth header.
// Returns outcomeDone once a response (success OR a non-switchable failure)
// has been written to the client, or outcomeNextCandidate when every key on
// this provider failed with a key-rotation error and the chain should move
// to the next candidate (same-provider no usable key, THEN failover).
func (s *Service) tryKeys(c *gin.Context, rc *Exchange, cand *model.ModelCandidate, provider *model.Provider, keys []model.ProviderKey, egress *EgressDecision, outBody []byte, url string, start time.Time) relayOutcome {
	for i := range keys {
		pk := keys[i]
		// Destination-version guard (credential-scope mechanism): a key
		// is only authorized for the provider destination it was verified
		// against. When an admin changes BaseURL, DestinationVersion bumps
		// while existing keys keep their old AuthorizedDestinationVersion —
		// decrypting and sending such a key would exfiltrate the credential
		// to an unapproved destination. Skip and rotate to the next key,
		// matching the destination-matched select in provider_repository.go.
		if pk.AuthorizedDestinationVersion != provider.DestinationVersion {
			rc.attempts = append(rc.attempts, rc.makeAttempt(*cand, provider, &pk, 0, AttemptAuthFailed, "destination version mismatch"))
			continue
		}
		plaintext, derr := crypto.Decrypt(s.masterKey, pk.EncryptedKey)
		if derr != nil {
			logger.Warn("gateway: decrypt provider key failed",
				zap.Uint("key_id", pk.ID), zap.String("request_id", rc.requestID), zap.Error(derr))
			rc.attempts = append(rc.attempts, rc.makeAttempt(*cand, provider, &pk, 0, AttemptBadStatus, "decrypt failed"))
			continue
		}
		switch s.attemptOne(c, rc, *cand, provider, pk, plaintext, egress, outBody, url, start) {
		case attemptSuccess, attemptTerminal:
			return outcomeDone
		case attemptRotateKey:
			continue // next key on the same provider
		case attemptNextCandidate:
			return outcomeNextCandidate
		}
	}
	// Every key failed with a key-rotation error → failover.
	return outcomeNextCandidate
}

// attemptResult is what one upstream attempt reports back to tryKeys.
type attemptResult int

const (
	attemptSuccess       attemptResult = iota
	attemptTerminal                    // 4xx client error — surfaced to caller, no switch
	attemptRotateKey                   // 401/429 — try next key
	attemptNextCandidate               // 5xx / conn / timeout — try next candidate
)

// attemptOne sends one upstream request with one decrypted key and routes
// the response. outBody/url are the pre-built upstream body/URL for this
// candidate (relayCandidates' buildUpstreamBody call) — this key's only
// contribution is the auth header (SetupRequest). Transport failures, 5xx,
// and pre-first-byte stream failures are candidate-level (failover); 401/429
// are key-level (rotate); 2xx is success; other 4xx is terminal (caller's
// problem).
func (s *Service) attemptOne(c *gin.Context, rc *Exchange, cand model.ModelCandidate, provider *model.Provider, pk model.ProviderKey, plaintext string, egress *EgressDecision, outBody []byte, url string, start time.Time) attemptResult {
	// Per-attempt deadline = min(attempt_timeout, remaining request budget).
	// The request-level budget (RequestDeadline, set at Handle entry) spans
	// all failover candidates; each attempt gets at most its own attempt_timeout,
	// capped by whatever budget remains so a long chain of slow candidates
	// cannot overrun the request cap.
	remaining := time.Until(rc.requestDeadline)
	if remaining <= 0 {
		rc.attempts = append(rc.attempts, rc.makeAttempt(cand, provider, &pk, 0, AttemptConnError, "request budget exhausted"))
		return attemptNextCandidate
	}
	attemptBudget := min(s.gateway.AttemptTimeout, remaining)
	// Derive from rc.requestCtx (carries RequestDeadline) rather than
	// c.Request.Context() directly, so the request-level deadline
	// propagates: when RequestCtx expires, the attempt ctx expires too,
	// cutting the upstream request even mid-stream.
	ctx, cancel := context.WithTimeout(rc.requestCtx, attemptBudget)
	defer cancel()

	// Record the rewritten (provider_model_name) request
	// actually sent upstream, verbatim. Overwritten on every attempt — the
	// last write wins, matching the "successful attempt, else the last
	// attempt" rule.
	rc.upstreamRequestBody = outBody
	// Record the dispatched URL for the log row and each AttemptRecord.
	// Redacted (userinfo/query/fragment stripped) so a base URL that embeds
	// credentials never reaches the audit log or UI; the raw url is used
	// only for the actual HTTP request below.
	rc.upstreamURL = protocols.RedactURL(url)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(outBody))
	if err != nil {
		// A build failure here is a candidate-level problem, mirroring the
		// old rewrite-model-failed skip: nothing has been sent, so fail
		// over. Every key on this candidate would fail identically (url is
		// candidate-invariant), so the first key attempt already exhausts
		// this candidate via tryKeys' immediate return on attemptNextCandidate.
		rc.attempts = append(rc.attempts, rc.makeAttempt(cand, provider, &pk, 0, AttemptBadStatus, "build request: "+err.Error()))
		return attemptNextCandidate
	}
	codecsFor(egress.Protocol).RequestEncoder.SetupRequest(req, plaintext)

	resp, err := s.client.SendUpstreamRequest(req)
	if err != nil {
		// Caller disconnected mid-request is terminal (can't switch — the
		// caller is gone). Distinguish context.Canceled (client gone) from
		// context.DeadlineExceeded (server-side/per-attempt timeout, which
		// is candidate-level, not a disconnect) so the log labels the right
		// failure class. Any other transport failure is candidate-level.
		if errors.Is(c.Request.Context().Err(), context.Canceled) {
			rc.attempts = append(rc.attempts, rc.makeAttempt(cand, provider, &pk, 0, AttemptConnError, "client disconnected"))
			s.finalize(rc, 499, "client_disconnected", start) // nginx-style 499
			return attemptTerminal
		}
		rc.attempts = append(rc.attempts, rc.makeAttempt(cand, provider, &pk, 0, AttemptConnError, err.Error()))
		return attemptNextCandidate
	}

	// Wrap the body with two-phase idle enforcement:
	//   - firstByteTimeout (open -> first chunk): covers the reasoning-model
	//     "flush 200 header then think for minutes" gap that
	//     transport.ResponseHeaderTimeout cannot reach.
	//   - idle (inter-chunk): nginx proxy_read_timeout — a steady reasoning
	//     stream resets the timer on every chunk and stays alive indefinitely,
	//     while a stalled stream is cut.
	// This single wrap point covers the stream relay, the non-stream ReadAll,
	// and the upstream error-body read below — all of which consume resp.Body.
	// The per-attempt ctx carries both the attempt budget and the caller's
	// disconnect, either of which cuts the stream with ctx.Err().
	//
	// Non-2xx error bodies get a short firstByte budget: a stalled retryable
	// 503/429 failover would otherwise burn the full 600s default before
	// ErrFirstByteTimeout surfaces; error bodies are small, so 10s is ample
	// for a healthy upstream while bounding a stuck one.
	if resp.Body != nil {
		// firstByteBudgetFor picks the short errorBodyFirstByteTimeout for
		// any non-2xx status and the full configured firstByteTimeout for
		// 2xx (reasoning models may silently think for minutes before the
		// first token).
		firstByte := firstByteBudgetFor(resp.StatusCode, s.gateway.FirstByteTimeout)
		if resp.StatusCode >= 200 && resp.StatusCode < 300 {
			// 2xx: a steady stream should stay alive indefinitely (up to
			// the request-level deadline), so idle uses the configured
			// BodyIdleTimeout rather than the short error-body budget.
			resp.Body = NewIdleReadCloser(resp.Body, firstByte, s.gateway.BodyIdleTimeout, ctx)
		} else {
			// Non-2xx error body: use a short total read budget so a
			// slow-trickle upstream cannot burn the full attempt_timeout.
			// The idle timeout is also tightened to the same short budget
			// so inter-byte gaps longer than that cut the read short,
			// preventing the "one byte every <idle gap" trickle attack.
			errBodyCtx, errBodyCancel := context.WithTimeout(ctx, errorBodyTotalBudget)
			resp.Body = NewIdleReadCloser(resp.Body, firstByte, firstByte, errBodyCtx)
			// Re-wrap cancel so the deferred cancel below frees it. The
			// original ctx cancel is deferred at the top of attemptOne;
			// errBodyCancel is a nested timeout that must also be released.
			defer errBodyCancel()
		}
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		// 2xx — dispatch directly instead of through a one-line trampoline.
		ingress := rc.ingress
		if rc.isStream {
			return s.processDispatchResponseStream(c, rc, ingress, egress, cand, provider, pk, resp, start)
		}
		return s.processDispatchResponseNonStream(c, rc, ingress, egress, cand, provider, pk, resp, start)
	}

	statusCode := resp.StatusCode
	class := classifyUpstreamStatus(statusCode)
	note := fmt.Sprintf("upstream %d", statusCode)

	// For 401, persist the key verification failure BEFORE reading the
	// error body. The CAS uses a context deliberately detached from the
	// attempt/request context so that a client disconnect or attempt-deadline
	// expiry arriving between the 401 response headers and this DB write
	// cannot cancel the UPDATE — a cancelled CAS would leave a dead key
	// marked as valid, causing every subsequent request to burn a full
	// upstream timeout on it. WithoutCancel decouples from request
	// cancellation; the 5s budget bounds a stuck DB so it cannot hang the
	// goroutine indefinitely. The CAS's own version guard
	// (expectedDestinationVersion) already protects against concurrent
	// edits, so the detached context is safe.
	if statusCode == http.StatusUnauthorized {
		casCtx, casCancel := context.WithTimeout(context.WithoutCancel(ctx), 5*time.Second)
		defer casCancel()
		if applied, mErr := repository.MarkProviderKeyVerificationFailedIfCurrent(s.db.WithContext(casCtx), pk.ID, provider.DestinationVersion, time.Now()); mErr != nil {
			logger.Warn("gateway: mark provider key failed",
				zap.Uint("key_id", pk.ID), zap.String("request_id", rc.requestID), zap.Error(mErr))
		} else if !applied {
			logger.Debug("gateway: provider key invalidation CAS lost race",
				zap.Uint("key_id", pk.ID), zap.String("request_id", rc.requestID))
		}
	}

	// Capture the obtainable upstream error body before close, verbatim.
	// Error bodies are small; cap at 1MiB — beyond that is
	// truncation of an error diagnostic, not a response body, and 1MiB is
	// ample for debugging. Unconditionally overwritten (even when empty) so
	// this matches rc.upstreamRequestBody's "last attempt wins" rule above —
	// an empty errBody from THIS attempt must clear out a stale non-empty body
	// left by an earlier failed candidate, not leave it looking current.
	// A subsequent SUCCESSFUL stream candidate clears
	// it entirely — see dispatchPassthroughStream (dispatch.go).
	//
	// The body is already wrapped with a short total budget
	// (errorBodyTotalBudget) so a slow-trickle upstream cannot hold this read
	// open beyond that window.
	errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	rc.upstreamResponseBody = errBody
	_ = resp.Body.Close()

	// A content-inspection refusal is reclassified from terminal to failover.
	// Status alone cannot tell it apart from a malformed request — only the
	// body can — so the upgrade happens here, after the read above, and before
	// the attempt record is appended so the log shows why the chain continued.
	// The refusal is remembered for allCandidatesFailed: if every candidate
	// moderates the payload, the caller must still get that verdict rather than
	// a generic 502 that reads like an outage.
	if class.Category == statusTerminalClient && isContentInspectionRejection(statusCode, string(errBody)) {
		class.Category = statusFailover
		class.Outcome = AttemptContentFiltered
		note = fmt.Sprintf("upstream %d content inspection", statusCode)
		rc.contentInspectionStatus = statusCode
		rc.contentInspectionErrType = class.ErrorType
	}

	rc.attempts = append(rc.attempts, rc.makeAttempt(cand, provider, &pk, statusCode, class.Outcome, note))
	switch class.Category {
	case statusRotateKey:
		// 401 CAS was already performed above, before the error body read.
		return attemptRotateKey
	case statusFailover:
		return attemptNextCandidate
	default: // statusTerminalClient — caller's request is the problem, no switch.
		if !c.Writer.Written() {
			WriteIngressError(c, rc.ingress, statusCode, class.ErrorType, safeUpstreamMessage(statusCode), rc.requestID)
		}
		s.finalize(rc, statusCode, fmt.Sprintf("upstream_client_error_%d", statusCode), start)
		return attemptTerminal
	}
}

// allCandidatesFailed is reached only when every candidate was tried without
// a response being written — the writer is guaranteed not yet written, but
// the guard is kept defensively in case a future caller changes that.
func (s *Service) allCandidatesFailed(c *gin.Context, rc *Exchange, start time.Time) {
	if c.Writer.Written() {
		status := rc.statusCode
		if status == 0 {
			status = http.StatusBadGateway
		}
		s.finalize(rc, status, "partial_then_exhausted", start)
		return
	}
	// The chain ended on a content-inspection refusal. That is a verdict on the
	// request, not an outage, so surface the upstream's own status and say why
	// — a 502 "all upstream candidates failed" would send the caller looking
	// for a broken provider instead of at their own prompt. Keyed on the LAST
	// candidate (the loop clears these on every iteration) rather than "any
	// candidate was refused": a chain that ends on a 5xx, or on a candidate
	// that could not even be dispatched to, really is a fault on our side of
	// the wire, whatever an earlier candidate thought of the payload.
	if rc.contentInspectionStatus != 0 {
		errType := rc.contentInspectionErrType
		if errType == "" {
			errType = errTypeInvalidRequest
		}
		WriteIngressError(c, rc.ingress, rc.contentInspectionStatus, errType,
			"request was refused by upstream content inspection", rc.requestID)
		s.finalize(rc, rc.contentInspectionStatus, "content_inspection_refused", start)
		return
	}
	status := http.StatusBadGateway
	WriteIngressError(c, rc.ingress, status, errTypeUpstream, "all upstream candidates failed", rc.requestID)
	s.finalize(rc, status, "all_candidates_failed", start)
}

// filterCandidates returns the subset of candidates eligible for this request:
// management-enabled and verification-passed. Order is preserved (sort_order was
// applied by the repository) so failover still walks the chain in the admin's
// configured order.
//
// It deliberately does NOT consult the streaming / function-calling capability
// flags. Those are recorded for the admin UI only. Filtering on them looks
// appealing but cannot be made to pay off here: a request this gate rejects
// produces "model is not available" with no attempt at all, while a capability
// the upstream genuinely lacks is reported as a 4xx, which attemptOne classifies
// as terminal — so excluding a candidate cannot be recovered by failover either
// way. Meanwhile a probe that merely failed to confirm a capability would take a
// working candidate out of rotation. Letting the upstream be the authority costs
// one failed request in the rare genuine case and removes a whole class of
// self-inflicted outages.
//
// anyEnabled is reported in the same pass so the caller can tell "all disabled"
// apart from "enabled but unverified" without walking the slice twice.
func filterCandidates(all []model.ModelCandidate) (routable []model.ModelCandidate, anyEnabled bool) {
	for _, c := range all {
		if c.ManagementStatus != model.ModelCandidateStatusEnabled {
			continue
		}
		anyEnabled = true
		// An enabled-but-unverified candidate is NOT routable. The two states can
		// coexist — a candidate is stored before its first probe, and a probe can
		// reset verification without touching enablement — and ModelService's own
		// routability check (isCandidateRoutable) already rejects these, so the
		// gateway must match that gate or it routes a mapping known to be
		// unverified.
		if c.VerificationStatus != model.ModelVerificationStatusPassed {
			continue
		}
		routable = append(routable, c)
	}
	return routable, anyEnabled
}

// filterEnabledKeys returns keys that are both management-enabled AND
// verification-passed (the gateway must match ModelService's routability
// gate). anyEnabled lets the caller distinguish "all keys disabled" from
// "enabled but none verified" for an accurate log reason.
func filterEnabledKeys(keys []model.ProviderKey) (out []model.ProviderKey, anyEnabled bool) {
	out = make([]model.ProviderKey, 0, len(keys))
	for _, k := range keys {
		if k.ManagementStatus != model.ProviderKeyStatusEnabled {
			continue
		}
		anyEnabled = true
		// Match ModelService routability: a key whose verification_status
		// is not Passed (never tested, or failed a retest) must not be
		// sent to the upstream — the gateway would otherwise keep using a
		// credential already known to be invalid.
		if k.VerificationStatus != model.VerificationStatusPassed {
			continue
		}
		out = append(out, k)
	}
	return out, anyEnabled
}

// makeAttempt builds one AttemptRecord. provider and key are nil-able: nil
// provider marks a candidate whose provider was missing/disabled; nil key
// marks a candidate-level failure before any key was tried (load failed, no
// enabled key, rewrite failed).
//
// It is a Exchange method so it can stamp the attempt with rc.upstreamURL
// — the URL the gateway dispatched to for this attempt — without every caller
// threading the URL through. rc.upstreamURL is reset per candidate in
// relayCandidates and set in attemptOne, so it reflects the current attempt:
// empty for attempts that failed before any request was sent.
func (rc *Exchange) makeAttempt(cand model.ModelCandidate, provider *model.Provider, key *model.ProviderKey, status int, outcome, failReason string) AttemptRecord {
	rec := AttemptRecord{
		CandidateID:       cand.ID,
		ProviderModelName: cand.ProviderModelName,
		StatusCode:        status,
		Outcome:           outcome,
		FailReason:        failReason,
		UpstreamURL:       rc.upstreamURL,
	}
	if provider != nil {
		rec.ProviderID = provider.ID
		rec.ProviderName = provider.Name
	} else {
		rec.ProviderID = cand.ProviderID
	}
	if key != nil {
		rec.KeyID = key.ID
		rec.KeyLabel = key.Label
	}
	return rec
}

// compressByIngress dispatches the body to the protocol-specific compress entry
// point. An unrecognized protocol returns the body unchanged with a no-op
// result (Skipped=true) so an unknown ingress never breaks the relay.
func compressByIngress(ingress protocols.ProtocolID, ctx context.Context, body []byte, opts compress.CompressOptions) ([]byte, compress.CompressResult) {
	switch ingress {
	case protocols.ProtocolClaude:
		return compress.CompressClaude(ctx, body, opts)
	case protocols.ProtocolOpenAI:
		return compress.CompressChat(ctx, body, opts)
	case protocols.ProtocolResponses:
		return compress.CompressResponses(ctx, body, opts)
	case protocols.ProtocolGemini:
		return compress.CompressGemini(ctx, body, opts)
	default:
		return body, compress.CompressResult{Skipped: true, SkipReason: compress.SkipReasonNoLiveZone}
	}
}
