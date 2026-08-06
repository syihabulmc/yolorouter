package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/yolorouter/yolorouter/internal/compress"
	"github.com/yolorouter/yolorouter/internal/config"
	"github.com/yolorouter/yolorouter/internal/fact"
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

	// secondaryFetch is the shared client for downloading responses an upstream
	// referred to rather than returned. Built once, on first use: a transport
	// per request would pool connections nobody ever reuses or closes.
	secondaryFetchOnce sync.Once
	secondaryFetch     *http.Client

	// upstreamErrorObservers are wired in by the assembly layer. They see
	// non-2xx upstream responses and report what they recognise; they never
	// decide what happens next. Order is irrelevant by construction — reported
	// judgements fold together by a rule that does not depend on who reported
	// first.
	upstreamErrorObservers []upstreamErrorObserver

	// deliveryObservers see how the exchange ended, successfully or not. This
	// is where an observation drawn from a SERVED response lands: the streaming
	// and non-streaming paths both settle here, so there is one call site
	// rather than one per response shape.
	deliveryObservers []deliveryObserver

	// egressRewriters rewrite the outbound body, ordered by stage at
	// registration so no per-request sort is needed.
	egressRewriters []egressRewriter

	// admissions gate the exchange before any upstream work. Registration
	// order is acquisition order; release runs in reverse.
	admissions []admission

	// recorders receive the settled exchange exactly once, on every exit path.
	recorders []recorder
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
		settingsProvider: sp,
		gateway:          gatewayCfg,
	}
}

// derefLimit reads an optional limit. An absent limit and a zero limit both
// mean unlimited, so they collapse to the same value rather than being carried
// as two states nothing downstream distinguishes.
func derefLimit(v *int) int {
	if v == nil || *v < 0 {
		return 0
	}
	return *v
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
		requestID:        requestIDFor(c),
		apiKeyID:         apiKey.ID,
		concurrencyLimit: derefLimit(apiKey.ConcurrencyLimit),
		rpmLimit:         derefLimit(apiKey.RPMLimit),
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
	// Armed before anything else that defers, so it unwinds LAST: recording has
	// to see a timeline that nothing will append to, and admissions release
	// after their own defer, which is registered later and therefore runs
	// first.
	defer s.recordTerminal(rc)
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
			d := fact.Undelivered(http.StatusInternalServerError, fact.VerdictSettled,
				fact.FaultGateway, "panic_recovered", nil)
			if c != nil && c.Writer != nil && c.Writer.Written() {
				// Something already went out, so the caller saw a status and
				// this cannot claim nothing was committed.
				d = fact.Truncated(c.Writer.Status(), http.StatusInternalServerError,
					fact.FaultGateway, "panic_recovered", nil)
			}
			s.settle(rc, d, start)
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
	// Admissions gate the exchange before any work is done on its behalf.
	// Whatever they take is released on every exit path below, including the
	// refusal path and a panic, which is why the release is deferred the moment
	// the tickets exist rather than at each return.
	// The release is armed before anything is acquired, not after: an admission
	// that panics must still give back whatever its predecessors took, and a
	// defer installed after the call would never run at all.
	var held []heldTicket
	defer func() {
		s.releaseAdmissions(rc.requestCtx, rc, held, fact.Outcome{
			StatusCode: rc.statusCode,
			Delivered:  rc.firstByteSent,
		})
	}()
	verdict := s.admit(rc.requestCtx, rc, &held)
	if verdict.Loop >= LoopNextCandidate {
		captureRejectedBody(c, rc)
		status, errType := admissionRejectionResponse(verdict)
		s.rejectRequest(c, rc, status, errType, verdict.rejectDetail, verdict.failReason(), fact.FaultClient, start)
		return
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		// Caller disconnect during body upload is terminal 499 (mirrors the
		// stream/non-stream response paths), not a malformed-request 400.
		if errors.Is(c.Request.Context().Err(), context.Canceled) {
			s.abandonRequest(rc, "client_disconnected", start)
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
		s.rejectRequest(c, rc, status, errTypeInvalidRequest, message, reason, fact.FaultClient, start)
		return
	}
	// Stash the caller-facing request body for the
	// request_log_bodies row, verbatim (v0.1 does not scrub body content).
	rc.requestBody = body

	// Two-level resolve for input compression: a per-key override wins
	// outright (short-circuit so a stalled settings read can't block an
	// override key); otherwise fall through to the global cached value. On
	// global read error leave compression disabled — fail-open, never block
	// the request on a settings hiccup.
	//
	// Resolved here rather than further down because the answer is needed
	// before the body is handed to a modality, and that is the last moment
	// anything may change those bytes.
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

	// Compression runs before the request is admitted, not after it is
	// validated, because the modality admits ONE body and everything
	// downstream builds from that one. Running it later would leave the
	// payload holding the uncompressed bytes while the exchange recorded the
	// compressed ones. Safe in this order: the engine leaves a body it cannot
	// parse untouched, so an invalid request is still rejected by the
	// validation below rather than slipping past a compressor that skipped it.
	// rc.requestBody keeps what the caller actually sent.
	admitBody := body
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
			admitBody = newBody
		}
	}

	// The modality that serves this ingress protocol decides whether the
	// request is one it can carry at all, and every refusal it can make is one
	// no candidate could have changed: a body that does not parse, a field the
	// protocol requires, a path that names no model.
	modality, ok := modalityFor(ingress)
	if !ok {
		// Nothing routes an unregistered protocol here today. Serving it with
		// whichever modality was nearest would answer the caller in a shape
		// they cannot read.
		logger.Error("gateway: no modality registered", zap.String("request_id", rc.requestID), zap.String("ingress", string(ingress)))
		s.rejectRequest(c, rc, http.StatusInternalServerError, errTypeServer, "internal error", "no_modality: "+string(ingress), fact.FaultGateway, start)
		return
	}
	payload, rej := modality.Admit(requestCtx, Ingress{
		Protocol:    ingress,
		Path:        c.Request.URL.Path,
		ContentType: c.GetHeader("Content-Type"),
		Body:        admitBody,
	})
	if rej != nil {
		s.rejectRequest(c, rc, rej.Status, rej.ErrorType, rej.Message, rej.FailReason, rej.Fault, start)
		return
	}
	// Wrapped before anything calls it: the wrapper is what holds the call
	// order and reconciles what a modality claims against what actually went
	// out to the caller.
	adm := admitted{payload: newOrderedPayload(payload, rc.requestID), limits: modality.Limits()}
	routing := adm.payload.Routing()
	rc.originalModel = routing.Model
	rc.isStream = routing.Stream

	// Every write to the caller slides a deadline forward, from the response
	// object the delivery was handed rather than from a package-wide default,
	// so a modality that asked for a shorter window gets the one it asked for.
	// This bounds a slow-reading client without clearing the server
	// WriteTimeout entirely.
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

	// Step 4: model exists and is enabled. A model disabled by an admin
	// must not route even if its candidates are still enabled.
	m, err := repository.FindModelByName(s.db.WithContext(requestCtx), rc.originalModel)
	if err != nil {
		if isClientDisconnected(c) {
			// The client hung up while this query was in flight — a
			// context.Canceled from the DB driver here is a disconnect, not
			// a server-side DB fault; nothing to write back to a gone caller.
			s.abandonRequest(rc, "client_disconnected", start)
			return
		}
		if errors.Is(err, gorm.ErrRecordNotFound) {
			s.rejectRequest(c, rc, http.StatusNotFound, errTypeNotFound, "model does not exist", "model_not_found", fact.FaultClient, start)
			return
		}
		logger.Error("gateway: find model", zap.String("request_id", rc.requestID), zap.Error(err))
		s.rejectRequest(c, rc, http.StatusInternalServerError, errTypeServer, "internal error", "db_model: "+err.Error(), fact.FaultGateway, start)
		return
	}
	if m.ManagementStatus != model.ModelStatusEnabled {
		s.rejectRequest(c, rc, http.StatusNotFound, errTypeNotFound, "model does not exist", "model_disabled", fact.FaultClient, start)
		return
	}

	// Step 5: allowlist. A key flagged allow_all_models skips the per-model
	// check and may call any enabled model.
	if !apiKey.AllowAllModels {
		allowed, err := repository.HasAPIKeyModelAccess(s.db.WithContext(requestCtx), apiKey.ID, m.ID)
		if err != nil {
			if isClientDisconnected(c) {
				s.abandonRequest(rc, "client_disconnected", start)
				return
			}
			logger.Error("gateway: allowlist", zap.String("request_id", rc.requestID), zap.Error(err))
			s.rejectRequest(c, rc, http.StatusInternalServerError, errTypeServer, "internal error", "db_allowlist: "+err.Error(), fact.FaultGateway, start)
			return
		}
		if !allowed {
			s.rejectRequest(c, rc, http.StatusForbidden, errTypePermission, "model is not in this API key's allowlist", "model_not_allowed", fact.FaultClient, start)
			return
		}
	}

	// Step 7: candidates filtered by requested capability.
	allCandidates, err := repository.ListModelCandidatesByModelID(s.db.WithContext(requestCtx), m.ID)
	if err != nil {
		if isClientDisconnected(c) {
			s.abandonRequest(rc, "client_disconnected", start)
			return
		}
		logger.Error("gateway: list candidates", zap.String("request_id", rc.requestID), zap.Error(err))
		s.rejectRequest(c, rc, http.StatusInternalServerError, errTypeServer, "internal error", "db_candidates: "+err.Error(), fact.FaultGateway, start)
		return
	}
	routable, anyEnabled := filterCandidates(allCandidates)
	if len(routable) == 0 {
		reason := "no_enabled_candidate"
		if anyEnabled {
			reason = "no_verified_candidate"
		}
		// No candidate was usable, so no provider was ever contacted. Blaming
		// upstream here would point an operator at a provider that had no part
		// in it; what is actually wrong is on our side of the wire.
		s.rejectRequest(c, rc, http.StatusServiceUnavailable, errTypeUnavailable, "model is not available", reason, fact.FaultGateway, start)
		return
	}

	// Asked before any candidate is tried, because that is the only window in
	// which the answer could still change what happens: an estimate produced
	// after the request was sent is a number nobody can act on.
	//
	// The prices come from the first routable candidate, and that is a seam
	// showing. Prices live on candidates, one per provider, while this question
	// is asked once for the request — so a modality that priced its work up
	// front would be pricing it against whichever provider happened to sort
	// first. Text answers that it cannot say and nothing acts on the estimate
	// yet, so nothing is wrong today; a modality that CAN answer is the reason
	// to settle whether this question is per-request or per-candidate.
	_ = adm.payload.EstimateCost(PricingView{
		InputPricePerMillion:  routable[0].InputPrice,
		OutputPricePerMillion: routable[0].OutputPrice,
	})

	// Steps 8–12.
	s.relayCandidates(c, rc, adm, routable, start)
}

// checkKeyStateAndLimits runs the pre-call checks that don't need a paired
// release: status (revoked), expiry, budget (read-only here — the gateway
// writes the spend in finalize), and RPM. Concurrency is handled separately
// in Handle because it needs a deferred release. captureRejectedBody is
// called on each rejection (these three checks all run
// before Handle's normal body read, so the audit row would otherwise have an
// empty request_body).
func (s *Service) checkKeyStateAndLimits(c *gin.Context, rc *Exchange, apiKey *model.APIKey, start time.Time) bool {
	if apiKey.Status == model.APIKeyStatusRevoked {
		captureRejectedBody(c, rc)
		s.rejectRequest(c, rc, http.StatusUnauthorized, errTypeAuthentication, "API key revoked", "revoked", fact.FaultClient, start)
		return false
	}
	if apiKey.ExpiresAt != nil && apiKey.ExpiresAt.Before(time.Now().UTC()) {
		captureRejectedBody(c, rc)
		s.rejectRequest(c, rc, http.StatusUnauthorized, errTypeAuthentication, "API key expired", "expired", fact.FaultClient, start)
		return false
	}
	if apiKey.BudgetLimitMicros != nil && apiKey.BudgetSpentMicros >= *apiKey.BudgetLimitMicros {
		captureRejectedBody(c, rc)
		s.rejectRequest(c, rc, http.StatusTooManyRequests, errTypeInsufficientQuota, "budget limit exceeded", "budget_exceeded", fact.FaultClient, start)
		return false
	}
	return true
}

// relayCandidates walks the candidate chain in sort_order. For each
// candidate it loads the provider's enabled keys, decrypts them one at a
// time, and sends the upstream request; Key rotation and candidate failover
// decisions come back from tryKeys.
func (s *Service) relayCandidates(c *gin.Context, rc *Exchange, adm admitted, candidates []model.ModelCandidate, start time.Time) {
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
				s.abandonRequestAfterAttempt(rc, "client_disconnected", start)
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
		// provider's own primary protocol, which the payload decodes to and
		// encodes back from.
		egress, err := Negotiate(ingress, provider)
		if err != nil {
			rc.attempts = append(rc.attempts, rc.makeAttempt(cand, provider, nil, 0, AttemptBadStatus, "negotiate egress: "+err.Error()))
			continue // mapping failure -> skip candidate
		}

		// The modality answers for this candidate before anything is built for
		// it. A refusal costs one candidate rather than the request, which is
		// the difference between this and the refusals Admit makes.
		offer := Candidate{
			ProviderModelName: cand.ProviderModelName,
			EgressProtocol:    egress.Protocol,
			Passthrough:       egress.Passthrough,
			BaseURL:           egress.BaseURL,
			// Unprobed reads as unsupported rather than supported: a capability
			// nobody has confirmed is one a modality must not be told it has.
			SupportsStreaming:       cand.SupportsStreaming != nil && *cand.SupportsStreaming,
			SupportsFunctionCalling: cand.SupportsFunctionCalling != nil && *cand.SupportsFunctionCalling,
			MaxOutput:               cand.MaxOutput,
		}
		if v := adm.payload.Supports(offer); !v.OK {
			rc.attempts = append(rc.attempts, rc.makeAttempt(cand, provider, nil, 0, AttemptBadStatus, v.Reason))
			continue
		}
		// Built once per candidate: it depends on the candidate and the
		// negotiated protocol, not on which key ends up sending it, so every
		// key attempt below reuses the same bytes.
		call, err := adm.payload.PrepareUpstream(offer)
		if err != nil {
			rc.attempts = append(rc.attempts, rc.makeAttempt(cand, provider, nil, 0, AttemptBadStatus, "build request: "+err.Error()))
			continue // build failure -> skip candidate, nothing sent yet
		}
		// The origin and the credentials stay on this side of the interface:
		// the modality states a path within a provider it was already talking
		// to, and the kernel decides which host that provider is.
		url := protocols.JoinUpstreamURL(egress.BaseURL, call.Path, egress.Protocol)
		// Rewriters run over the finished egress body, after the modality
		// built it and before anything is sent. A rewriter that refuses comes
		// back as a verdict for this loop to act on, not as an error: what a
		// refusal costs the request is the table's call.
		outBody, verdict := s.rewriteEgress(rc.requestCtx, rc, egress.Protocol, call.Body)
		// Anything as strong as "abandon this candidate" stops the send. The
		// full effect may be more than this path can execute — a terminate
		// verdict also wants a specific status, which the exhausted-chain
		// terminal below cannot always reproduce — but the floor is absolute:
		// once a fact resolved to a verdict this strong, dispatching the body
		// anyway would mean a reported judgement was overridden by omission.
		// Under-executing a verdict is recoverable; ignoring it is not.
		if verdict.Loop >= LoopNextCandidate {
			rc.attempts = append(rc.attempts, rc.makeAttempt(cand, provider, nil, 0, AttemptBadStatus,
				"egress rewrite verdict "+verdict.loopFrom.String()))
			continue
		}
		// Retry-same and rotate-key have no meaning before anything was sent;
		// they are logged so the reporting capability's misunderstanding is
		// visible rather than silently absorbed.
		if verdict.Loop > LoopContinue {
			logger.Warn("gateway: reported verdict is not executable before dispatch",
				zap.String("request_id", rc.requestID),
				zap.String("verdict", verdict.loopFrom.String()))
		}

		if s.tryKeys(c, rc, adm, &cand, provider, enabled, egress, outBody, url, start) == outcomeDone {
			return
		}
		// outcomeNextCandidate: fall through to the next candidate.
	}
	s.allCandidatesFailed(c, rc, start)
}

// admitted is what a modality handed back for one request: the payload it built
// and the budgets its modality asked for.
//
// They travel together because a delivery needs both and neither can be derived
// from the other — the payload is per-request and the limits belong to the
// modality that made it.
type admitted struct {
	payload Payload
	limits  TransferLimits
}

// attemptNoteFor is what one attempt's row says happened, in words.
//
// The stable code and the error read differently and both are wanted: a
// dashboard groups by the first, and whoever opens the row needs the second.
// Built here rather than by each delivery path, which is what let four paths
// spell the same failure four ways.
func attemptNoteFor(d fact.Delivery) string {
	if d.FailReason == "" {
		return ""
	}
	if d.Err == nil {
		return d.FailReason
	}
	return d.FailReason + ": " + d.Err.Error()
}

// usageFromReport turns what a modality reported into what the kernel bills on.
//
// The two are separate types on purpose: a modality states quantities in the
// unit it counts, and this is where the kernel decides what to do with them.
// Nil in, nil out — an attempt that reported nothing is not an attempt that
// reported zeros, and billing the difference is real money.
func usageFromReport(u *fact.UsageReported) *Usage {
	if u == nil {
		return nil
	}
	return &Usage{
		PromptTokens:          u.Prompt,
		CompletionTokens:      u.Completion,
		TotalTokens:           u.Total,
		CacheReadTokens:       u.CacheRead,
		CacheWriteTokens:      u.CacheWrite,
		CacheIncludedInPrompt: u.CacheIncludedInPrompt,
		Invalid:               u.Incoherent,
		WebSearchCount:        u.WebSearchCount,
	}
}

// relayOutcome is what tryKeys reports back to relayCandidates.
type relayOutcome int

const (
	outcomeDone          relayOutcome = iota // response written, relay finished
	outcomeNextCandidate                     // this candidate's keys are exhausted, try next
)

// tryKeys walks one provider's enabled keys, sending the same pre-built
// upstream body/URL (outBody/url — built once per candidate, by asking the
// payload to prepare it) with each key's own auth header.
// Returns outcomeDone once a response (success OR a non-switchable failure)
// has been written to the client, or outcomeNextCandidate when every key on
// this provider failed with a key-rotation error and the chain should move
// to the next candidate (same-provider no usable key, THEN failover).
func (s *Service) tryKeys(c *gin.Context, rc *Exchange, adm admitted, cand *model.ModelCandidate, provider *model.Provider, keys []model.ProviderKey, egress *EgressDecision, outBody []byte, url string, start time.Time) relayOutcome {
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
		result := s.attemptOne(c, rc, adm, *cand, provider, pk, plaintext, egress, outBody, url, start)
		if result == attemptSuccess || result == attemptTerminal {
			return outcomeDone
		}
		if result == attemptRotateKey {
			continue // next key on the same provider
		}
		return outcomeNextCandidate
	}
	// Every key failed with a key-rotation error → failover.
	return outcomeNextCandidate
}

// deliverAndSettle hands a 2xx upstream response to the modality and settles
// whatever comes back.
//
// The modality delivers, this side settles. Keeping those apart is what stops
// "how a response is delivered" and "what the request cost and how it is
// recorded" from having to be known in one place.
func (s *Service) deliverAndSettle(c *gin.Context, rc *Exchange, adm admitted, cand model.ModelCandidate, provider *model.Provider, pk model.ProviderKey, resp *http.Response, start time.Time) attemptResult {
	tools, release := s.newDeliveryTools(c, rc, adm.limits, rc.isStream)
	defer release()
	return s.recordAndSettle(c, rc, adm, adm.payload.Deliver(tools, resp), cand, provider, pk, resp.StatusCode, start)
}

// recordAndSettle turns what a delivery reported into the request's record.
//
// Separate from the delivery itself because the two answer to different things:
// a modality states what happened, and this decides what the request is
// therefore billed, logged and answered as. Nothing here reads the response
// body — by this point the only evidence is the Delivery.
func (s *Service) recordAndSettle(c *gin.Context, rc *Exchange, adm admitted, d fact.Delivery, cand model.ModelCandidate, provider *model.Provider, pk model.ProviderKey, upstreamStatus int, start time.Time) attemptResult {
	// The order below is not arrangement. A delivery is checked before it is
	// labelled, because checkAndNote replaces an impossible Delivery with one
	// that says so, and the attempt row is built from whatever it is handed.
	// Label first and the row is built from the original: the outcome still
	// comes out wrong-ish either way, but the fail reason comes out EMPTY,
	// because the reason only exists on the substitute. An operator opening
	// that row to find out what happened is told nothing at all, which is
	// worse than being told the wrong thing.
	sink := newExchangeSink(rc)
	s.checkAndNote(rc, &d, sink)
	rc.attempts = append(rc.attempts, rc.makeAttempt(cand, provider, &pk,
		upstreamStatus, attemptOutcomeFor(d, rc.isStream), attemptNoteFor(d)))
	if d.Verdict == fact.VerdictSettled {
		// The one delivery that ended the request, which is the only one this
		// question is asked about. Asking per attempt would tell the payload the
		// request was over while the chain was still walking.
		//
		// Folded back into the Delivery rather than stashed on the exchange:
		// what a request is billed for belongs to the delivery that ended it,
		// and an attempt that reported tokens and then failed has no way to
		// leave them lying around for a later settlement to pick up.
		d = d.WithUsage(adm.payload.FinalizeUsage(d))
	}
	return s.settleCheckedDelivery(c, rc, d, sink, start)
}

// redactedFailure renders a transport-layer failure for the audit trail with
// the upstream URL redacted.
//
// net/http wraps its failures in a *url.Error carrying the URL it was handed.
// url.Error hides the userinfo password and nothing else — a base URL
// configured with the key in a query parameter comes through intact. That
// string goes into the attempt record and is persisted, so the credential that
// RedactURL strips at dispatch time walks straight back in through the error
// text. Rebuilding the message around the already-redacted URL is what keeps
// the two in step.
func redactedFailure(err error, redactedURL string) string {
	var uerr *url.Error
	if errors.As(err, &uerr) {
		return uerr.Op + " " + redactedURL + ": " + uerr.Err.Error()
	}
	return err.Error()
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
// candidate — this key's only contribution is the auth header
// (SetupRequest). Transport failures, 5xx,
// and pre-first-byte stream failures are candidate-level (failover); 401/429
// are key-level (rotate); 2xx is success; other 4xx is terminal (caller's
// problem).
func (s *Service) attemptOne(c *gin.Context, rc *Exchange, adm admitted, cand model.ModelCandidate, provider *model.Provider, pk model.ProviderKey, plaintext string, egress *EgressDecision, outBody []byte, url string, start time.Time) attemptResult {
	// Whatever the previous send left behind describes that send, not this one.
	rc.beginUpstreamAttempt()

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
		rc.attempts = append(rc.attempts, rc.makeAttempt(cand, provider, &pk, 0, AttemptBadStatus,
			"build request: "+redactedFailure(err, rc.upstreamURL)))
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
			s.abandonRequestAfterAttempt(rc, "client_disconnected", start) // nginx-style 499
			return attemptTerminal
		}
		rc.attempts = append(rc.attempts, rc.makeAttempt(cand, provider, &pk, 0, AttemptConnError,
			redactedFailure(err, rc.upstreamURL)))
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
		return s.deliverAndSettle(c, rc, adm, cand, provider, pk, resp, start)
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
	// A subsequent SUCCESSFUL stream candidate clears it entirely.
	//
	// The body is already wrapped with a short total budget
	// (errorBodyTotalBudget) so a slow-trickle upstream cannot hold this read
	// open beyond that window.
	errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	rc.upstreamResponseBody = errBody
	_ = resp.Body.Close()

	// Observers get the response and report what they recognise in it; the
	// decision table turns those reports into a verdict. A terminal
	// classification can be upgraded to a failover this way — status alone
	// cannot tell a moderated payload apart from a malformed one, only the body
	// can — so the upgrade happens here, after the read above, and before the
	// attempt record is appended, so the log shows why the chain continued.
	// The verdict is remembered for allCandidatesFailed: if every candidate
	// moderates the payload, the caller must still get that verdict rather than
	// a generic 502 that reads like an outage.
	observed := s.observeUpstreamError(ctx, rc, fact.Upstream{
		StatusCode: statusCode,
		Header:     resp.Header,
		Body:       errBody,
		Elapsed:    time.Since(start),
	})
	// Only the refusal verdict is executed here. The table describes more than
	// this call site acts on, so a verdict that is understood but not yet
	// wired is logged rather than silently dropped: a reported judgement that
	// vanishes without trace is the one failure mode that looks exactly like
	// everything working.
	switch {
	case observed.loopFrom == fact.KindPayloadRefused && class.Category == statusTerminalClient:
		class.Category = statusFailover
		class.Outcome = AttemptContentFiltered
		note = fmt.Sprintf("upstream %d content inspection", statusCode)
		rc.contentInspectionStatus = statusCode
		rc.contentInspectionErrType = class.ErrorType
	case observed.Loop > LoopContinue:
		logger.Warn("gateway: reported verdict is not executed on this path",
			zap.String("request_id", rc.requestID),
			zap.String("verdict", observed.loopFrom.String()),
			zap.Int("upstream_status", statusCode))
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
		s.settleAfterAttempt(rc, fact.Rejected(statusCode, fact.FaultUpstream,
			fmt.Sprintf("upstream_client_error_%d", statusCode), nil), start)
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
		s.settle(rc, fact.Truncated(status, status, fact.FaultUpstream,
			"partial_then_exhausted", nil), start)
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
		s.rejectRequest(c, rc, rc.contentInspectionStatus, errType,
			"request was refused by upstream content inspection",
			"content_inspection_refused", fact.FaultUpstream, start)
		return
	}
	s.rejectRequest(c, rc, http.StatusBadGateway, errTypeUpstream,
		"all upstream candidates failed", "all_candidates_failed", fact.FaultUpstream, start)
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
