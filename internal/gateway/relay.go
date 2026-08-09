package gateway

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/yolorouter/yolorouter/internal/compress"
	"github.com/yolorouter/yolorouter/internal/config"
	"github.com/yolorouter/yolorouter/internal/decision"
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
	if rc.bodies.Request() != nil {
		return // already captured (e.g. body read succeeded then a later check failed)
	}
	if c.Request == nil {
		return
	}
	if body := ReadAuditBody(c.Request.Body); body != nil {
		rc.bodies.SetRequest(body)
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

	// failureRewriters see a non-2xx upstream response together with the body
	// that provoked it, and may offer a repaired body. They never decide what
	// happens next: whether the repair is worth an attempt is the decision
	// table's call, made from the facts they report.
	failureRewriters []failureRewriter

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
	// Normalise the count budgets here rather than at every read: a config
	// assembled without them (unit tests, older config files) means the
	// defaults, not a request that stops before its first dispatch.
	if gatewayCfg.MaxUpstreamAttempts <= 0 {
		gatewayCfg.MaxUpstreamAttempts = config.DefaultMaxUpstreamAttempts
	}
	if gatewayCfg.MaxCandidateProbes <= 0 {
		gatewayCfg.MaxCandidateProbes = config.DefaultMaxCandidateProbes
	}
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
	// Armed before every other defer except the context cancel, so of the work
	// that matters it unwinds LAST: recording has
	// to see a timeline that nothing will append to, and admissions release
	// after their own defer, which is registered later and therefore runs
	// first.
	//
	// The test hook fires here, after recording, because "done" must mean done:
	// a hook that fired with releases and recording still pending handed tests a
	// signal to tear down their temp directories while this goroutine was still
	// writing capture files into them.
	defer func() {
		s.recordTerminal(rc)
		if testHookHandleDone != nil {
			testHookHandleDone(rc)
		}
	}()
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
	// the chain) can stash the local error JSON into the response-body capture without
	// every call site threading an *Exchange parameter through.
	c.Set(relayContextKey, rc)
	// Capture the caller's request headers once at entry (masked
	// via SanitizeHeaders) so even an early rejection below still records
	// them. c.Request is always non-nil here (gin populates it), but guard
	// anyway for direct-call tests.
	if c.Request != nil {
		rc.requestHeaders = SanitizeHeaders(c.Request.Header)
	}
	// Admissions gate the exchange before any work is done on its behalf.
	// Whatever they take is released on every exit path below, including the
	// refusal path and a panic, which is why the release is deferred the moment
	// the tickets exist rather than at each return.
	//
	// Two ordering rules are load-bearing here:
	//   - The release is armed before anything is acquired, not after: an
	//     admission that panics must still give back whatever its predecessors
	//     took, and a defer installed after the call would never run at all.
	//   - The release is armed BEFORE the panic-settlement defer below, so on
	//     an unwind it runs AFTER settlement. A release is a reconciliation
	//     against how the request ended; run first, it would read a status of
	//     zero and a blank reason on exactly the requests that ended worst.
	//
	// The outcome is read at release time rather than captured when the defer
	// was armed, and it is finalize's own record rather than a second literal
	// built here: a capability deciding whether a reservation became a charge
	// needs what the request SETTLED as, and two sites each assembling their own
	// account of that would drift — the recorders would reconcile one ending and
	// the releases another.
	var held []heldTicket
	defer func() {
		s.releaseAdmissions(rc.requestCtx, rc, held, rc.outcome)
	}()

	// Panic-recovery safety net: if any sub-call panics (nil
	// deref, index OOB, type assertion), gin's Recovery middleware catches
	// it upstream, but finalize would otherwise never run and the request
	// would leave no audit/cost row. finalize is idempotent (logWritten
	// guard), so a normal-exit finalize first + this defer on panic writes
	// exactly one row either way.
	//
	// Registered after the release defer above so it runs BEFORE it on an
	// unwind: settlement first, then release reads the settled outcome.
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
	}()

	if !s.checkKeyStateAndLimits(c, rc, apiKey, start) {
		return
	}
	verdict := s.admit(rc.requestCtx, rc, &held)
	if verdict.Loop >= decision.LoopNextCandidate {
		captureRejectedBody(c, rc)
		status, errType := decision.AdmissionRejectionResponse(verdict)
		s.rejectRequest(c, rc, status, errType, verdict.RejectDetail(), verdict.FailReason(), fact.FaultClient, start)
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
	rc.bodies.SetRequest(body)

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
	// the captured request body keeps what the caller actually sent.
	admitBody := body
	if rc.compressEnabled && rc.isChatEndpoint {
		opts := compress.DefaultOptions()
		cctx, cancel := context.WithTimeout(c.Request.Context(), opts.Timeout)
		newBody, cres := compress.ByProtocol(ingress, cctx, body, opts)
		cancel()
		if cres.Skipped {
			rc.compressSkipReason = string(cres.SkipReason)
		} else {
			rc.bodies.SetCompressedRequest(newBody)
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

// exhaustedBudget reports whether any of the request's budgets — wall-clock,
// attempts, probes — is spent. One predicate shared by the candidate-loop
// gate and the exhausted-chain terminal, so the walk stops and the answer is
// chosen by the same rule: a budget spent by the final candidate must produce
// the same verdict as one spent with candidates still waiting.
func (s *Service) exhaustedBudget(rc *Exchange) bool {
	return (!rc.requestDeadline.IsZero() && time.Until(rc.requestDeadline) <= 0) ||
		rc.attemptsSpent >= s.gateway.MaxUpstreamAttempts ||
		rc.probesSpent >= s.gateway.MaxCandidateProbes
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
	// skipCandidate books the probe a pre-dispatch abandonment costs and
	// records the row for it, in one place so a new skip path cannot forget
	// the charge. Every candidate walked past without a dispatch spends a
	// probe — the table prices all such judgements identically — which is
	// what keeps a large pool from being walked end to end for free. Key
	// rotations inside a candidate deliberately spend nothing (the
	// key-unusable row's call): a provider with several unusable keys must
	// not eat the walk budget without the candidate itself being abandoned.
	skipCandidate := func(cand model.ModelCandidate, provider *model.Provider, outcome, note string) {
		rc.spendBudget(decision.BudgetConsumeProbe)
		rc.recordAttempt(cand, provider, nil, 0, outcome, note)
	}
	for i := range candidates {
		// Cleared here as well as on entry to each attempt, because the two
		// clears cover paths the other cannot reach. A candidate can be dropped
		// before any attempt is built — no usable key, nothing to send — and
		// the budget gate below exits the loop mid-iteration; neither reaches
		// attemptOne, and a verdict left over from the previous candidate would
		// be reported as what ended the request when what ended it was running
		// out of candidates or out of time.
		//
		// Placed ABOVE the budget gate for that second reason: a reset after it
		// is skipped exactly when the previous attempt had just left a verdict
		// behind.
		//
		// A known asymmetry follows and is accepted: a budget spent by the
		// previous candidate's last attempt answers with that attempt's
		// verdict when no further candidate exists, but with the budget
		// verdict when one does — entering the next candidate is what retires
		// the old verdict. The alternative (clearing after the gate) would
		// let a chain that ran out of time quote a stale refusal from a
		// candidate it had already moved past, sending the caller to edit a
		// payload that was never the problem.
		rc.attempt.ClearVerdict()
		cand := candidates[i]
		// Per-request budget gate: the wall-clock and count caps span every
		// candidate and key rotation. Checking only at the first attempt left
		// later candidates reachable after a budget had already run out,
		// burning work on a chain that could never succeed. Stop walking as
		// soon as any budget is gone and fall through to the exhausted-chain
		// terminal, which recomputes the same predicate to pick its answer.
		if s.exhaustedBudget(rc) {
			break
		}
		// Entering the candidate replaces the whole attempt state: provider is
		// re-bound only when this candidate's provider proves usable, so a
		// `continue` path (provider missing/disabled, load-keys failed, no
		// enabled key, rewrite failed) doesn't leave a stale provider from a
		// previous iteration on rc — which finalize would otherwise record as
		// the "final hit provider" of an all-failed request — and the same for
		// the dispatch URL. Deliberately AFTER the budget gate above: an
		// iteration that exits there keeps the previous attempt's identity,
		// which is what the audit row is supposed to show.
		rc.attempt.BeginCandidate(&cand)

		provider := cand.Provider
		if provider == nil {
			skipCandidate(cand, nil, AttemptBadStatus, "provider missing (preload)")
			continue
		}
		if provider.ManagementStatus != model.ProviderStatusEnabled {
			skipCandidate(cand, provider, AttemptBadStatus, "provider disabled")
			continue
		}
		rc.attempt.BindProvider(provider)

		keys, err := repository.ListProviderKeysByProvider(s.db.WithContext(rc.requestCtx), provider.ID)
		if err != nil {
			if isClientDisconnected(c) {
				// The client is gone — stop walking the candidate chain
				// entirely rather than burning the remaining candidates
				// only to land on allCandidatesFailed's 502; record 499
				// instead, mirroring attemptOne's disconnect handling.
				rc.recordAttempt(cand, provider, nil, 0, AttemptConnError, "client disconnected")
				s.abandonRequestAfterAttempt(rc, "client_disconnected", start)
				return
			}
			logger.Error("gateway: list provider keys", zap.String("request_id", rc.requestID), zap.Error(err))
			skipCandidate(cand, provider, AttemptBadStatus, "load keys failed")
			continue
		}
		enabled, anyEnabledKey := filterEnabledKeys(keys)
		if len(enabled) == 0 {
			reason := "no enabled key"
			if anyEnabledKey {
				reason = "no verified key"
			}
			skipCandidate(cand, provider, AttemptBadStatus, reason)
			continue
		}

		// Step 9: negotiate the wire protocol to speak to this candidate's
		// provider — the ingress protocol when the provider accepts it
		// directly (passthrough, no IR round trip), otherwise the
		// provider's own primary protocol, which the payload decodes to and
		// encodes back from.
		egress, err := Negotiate(ingress, provider)
		if err != nil {
			skipCandidate(cand, provider, AttemptBadStatus, "negotiate egress: "+err.Error())
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
			skipCandidate(cand, provider, AttemptBadStatus, v.Reason)
			continue
		}
		// Built once per candidate: it depends on the candidate and the
		// negotiated protocol, not on which key ends up sending it, so every
		// key attempt below reuses the same bytes.
		call, err := adm.payload.PrepareUpstream(offer)
		if err != nil {
			skipCandidate(cand, provider, AttemptBadStatus, "build request: "+err.Error())
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
		if verdict.Loop >= decision.LoopNextCandidate {
			skipCandidate(cand, provider, AttemptBadStatus,
				"egress rewrite verdict "+verdict.LoopFrom().String())
			continue
		}
		// Retry-same and rotate-key have no meaning before anything was sent;
		// they are logged so the reporting capability's misunderstanding is
		// visible rather than silently absorbed.
		if verdict.Loop > decision.LoopContinue {
			logger.Warn("gateway: reported verdict is not executable before dispatch",
				zap.String("request_id", rc.requestID),
				zap.String("verdict", verdict.LoopFrom().String()))
		}

		// A candidate can also die INSIDE the key loop without a single
		// dispatch — every key stale or undecryptable, or the request
		// impossible to build. Attempts are charged at the wire, so "nothing
		// was dispatched" is visible as an unchanged attempt ledger, and such
		// a candidate is charged the same one probe as any other pre-dispatch
		// abandonment. Without this, a pool full of stale keys would repeat
		// database and decryption work bounded by nothing but the wall clock.
		attemptsBefore := rc.attemptsSpent
		if s.tryKeys(c, rc, adm, enabled, egress, outBody, url, start) == outcomeDone {
			return
		}
		if rc.attemptsSpent == attemptsBefore {
			rc.spendBudget(decision.BudgetConsumeProbe)
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
	// Several construction sites already fold the error's text into the reason
	// they build — "read_body: EOF" carrying the same EOF as its Err. Appending
	// it again writes "read_body: EOF: EOF" into a persisted column, so the
	// suffix is only added when it brings something the reason does not already
	// say. The check matches the folded shape (": " + error, or the error alone)
	// rather than any suffix, so a reason that merely ENDS with the error's
	// words — "client_write_timeout" against a bare "timeout" — keeps its
	// suffix instead of being mistaken for a fold.
	if d.Err == nil || d.FailReason == d.Err.Error() ||
		strings.HasSuffix(d.FailReason, ": "+d.Err.Error()) {
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
		ReasoningTokens:       u.Reasoning,
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
func (s *Service) tryKeys(c *gin.Context, rc *Exchange, adm admitted, keys []model.ProviderKey, egress *EgressDecision, outBody []byte, url string, start time.Time) relayOutcome {
	provider := rc.attempt.Provider()
	// Indexed by hand because one iteration can legitimately not advance: a
	// repaired-body retry re-enters the same key with the new body. One
	// repair per candidate: a capability that kept producing
	// fresh-but-ineffective repairs would otherwise burn the whole attempt
	// budget on one candidate — the kernel does not rely on rewriters being
	// well-behaved. The allowance is passed INTO attemptOne so that an offer
	// it cannot honour is judged unexecutable there, where the failure still
	// gets its full baseline handling — surfacing the upstream's own status —
	// rather than being discarded after the attempt already closed as a
	// retry.
	repairsUsed := 0
	for i := 0; i < len(keys); {
		// The attempt budget spans key rotations too: a rotation that spent
		// the last attempt must not dispatch the next key. Surfacing as a
		// candidate switch lands on the loop gate above, which recognises the
		// exhaustion and ends the walk.
		if rc.attemptsSpent >= s.gateway.MaxUpstreamAttempts {
			return outcomeNextCandidate
		}
		pk := keys[i]
		// Cleared before the skip checks below, not inside attemptOne, because
		// a key can be passed over without ever getting there — an unauthorized
		// destination version, a key that will not decrypt. Left to attemptOne,
		// the previous key's verdict would survive those paths and, if the
		// chain then ran out, be reported as what ended the request: a rate
		// limit one key hit, quoted for a request that actually died with no
		// usable key at all.
		rc.attempt.ClearVerdict()
		// Bound before the skip checks for the same reason the verdict is
		// cleared before them: a skipped key's audit row must name the key
		// that was passed over, not the previous one.
		rc.attempt.BindKey(&pk)
		// Destination-version guard (credential-scope mechanism): a key
		// is only authorized for the provider destination it was verified
		// against. When an admin changes BaseURL, DestinationVersion bumps
		// while existing keys keep their old AuthorizedDestinationVersion —
		// decrypting and sending such a key would exfiltrate the credential
		// to an unapproved destination. Skip and rotate to the next key,
		// matching the destination-matched select in provider_repository.go.
		if pk.AuthorizedDestinationVersion != provider.DestinationVersion {
			rc.recordCurrentAttempt(0, AttemptAuthFailed, "destination version mismatch")
			i++
			continue
		}
		plaintext, derr := crypto.Decrypt(s.masterKey, pk.EncryptedKey)
		if derr != nil {
			logger.Warn("gateway: decrypt provider key failed",
				zap.Uint("key_id", pk.ID), zap.String("request_id", rc.requestID), zap.Error(derr))
			rc.recordCurrentAttempt(0, AttemptBadStatus, "decrypt failed")
			i++
			continue
		}
		result, repaired := s.attemptOne(c, rc, adm, plaintext, egress, outBody, url, start, repairsUsed == 0)
		if result == attemptSuccess || result == attemptTerminal {
			return outcomeDone
		}
		if result == attemptRotateKey {
			i++ // next key on the same provider
			continue
		}
		if result == attemptRetrySame {
			// The table judged the repaired body worth another attempt against
			// THIS candidate. The body is candidate-level, exactly like the
			// egress-rewritten one it replaces: the same key retries first,
			// and a later rotation reuses it too.
			repairsUsed++
			outBody = repaired
			continue
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
func (s *Service) deliverAndSettle(c *gin.Context, rc *Exchange, adm admitted, resp *http.Response, start time.Time) attemptResult {
	// The payloads close the body themselves, from a defer INSIDE Deliver — but
	// a panic can fire before that defer is registered: the call-order wrapper
	// asserts before it forwards, and an assertion tripping there unwinds with
	// the body still open, pinning the upstream connection until the attempt
	// context expires. Closing twice is safe; leaking on the one path that
	// panics before anyone armed a close is not.
	defer func() { _ = resp.Body.Close() }()
	tools, release := s.newDeliveryTools(c, rc, adm.limits, rc.isStream)
	defer release()
	return s.recordAndSettle(c, rc, adm, adm.payload.Deliver(tools, resp), resp.StatusCode, start)
}

// recordAndSettle turns what a delivery reported into the request's record.
//
// Separate from the delivery itself because the two answer to different things:
// a modality states what happened, and this decides what the request is
// therefore billed, logged and answered as. Nothing here reads the response
// body — by this point the only evidence is the Delivery.
func (s *Service) recordAndSettle(c *gin.Context, rc *Exchange, adm admitted, d fact.Delivery, upstreamStatus int, start time.Time) attemptResult {
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
	rc.recordCurrentAttempt(upstreamStatus, attemptOutcomeFor(d, rc.isStream), attemptNoteFor(d))
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
	// attemptRetrySame re-sends a repaired body to the same candidate. Only
	// attemptOne's failure-rewrite path produces it, and only together with
	// the body to re-send; the attempt budget bounds how often it can recur.
	attemptRetrySame
)

// attemptOne sends one upstream request with one decrypted key and routes
// the response. outBody/url are the pre-built upstream body/URL for this
// candidate — this key's only contribution is the auth header
// (SetupRequest). Transport failures, 5xx,
// and pre-first-byte stream failures are candidate-level (failover); 401/429
// are key-level (rotate); 2xx is success; other 4xx is terminal (caller's
// problem).
func (s *Service) attemptOne(c *gin.Context, rc *Exchange, adm admitted, plaintext string, egress *EgressDecision, outBody []byte, url string, start time.Time, repairAllowed bool) (attemptResult, []byte) {
	// The attempt's identity — candidate, provider, key — lives on rc.attempt,
	// staged by the loops above; plaintext alone stays a parameter, because a
	// credential parked on the exchange would outlive the one call that needs
	// it. Whatever the previous send left behind describes that send, not
	// this one.
	pk := rc.attempt.Key()
	provider := rc.attempt.Provider()
	rc.beginUpstreamAttempt()

	// Per-attempt deadline = min(attempt_timeout, remaining request budget).
	// The request-level budget (RequestDeadline, set at Handle entry) spans
	// all failover candidates; each attempt gets at most its own attempt_timeout,
	// capped by whatever budget remains so a long chain of slow candidates
	// cannot overrun the request cap.
	remaining := time.Until(rc.requestDeadline)
	if remaining <= 0 {
		rc.recordCurrentAttempt(0, AttemptConnError, "request budget exhausted")
		return attemptNextCandidate, nil
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
	rc.bodies.SetUpstreamRequest(outBody)
	// Record the dispatched URL for the log row and each AttemptRecord.
	// Redacted (userinfo/query/fragment stripped) so a base URL that embeds
	// credentials never reaches the audit log or UI; the raw url is used
	// only for the actual HTTP request below.
	rc.attempt.SetUpstreamURL(protocols.RedactURL(url))

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(outBody))
	if err != nil {
		// A build failure here is a candidate-level problem, mirroring the
		// old rewrite-model-failed skip: nothing has been sent, so fail
		// over. Every key on this candidate would fail identically (url is
		// candidate-invariant), so the first key attempt already exhausts
		// this candidate via tryKeys' immediate return on attemptNextCandidate.
		rc.recordCurrentAttempt(0, AttemptBadStatus,
			"build request: "+redactedFailure(err, rc.attempt.UpstreamURL()))
		return attemptNextCandidate, nil
	}
	codecsFor(egress.Protocol).RequestEncoder.SetupRequest(req, plaintext)

	resp, err := s.client.SendUpstreamRequest(req)
	// The request reached for the wire, and that is what the attempt budget
	// counts. Every row the table prices for a dispatched upstream —
	// transport failure, any status, a 2xx whose delivery later fails —
	// says one attempt, so the charge lives at the dispatch itself rather
	// than on each outcome branch, where the delivery failures that return
	// through the 2xx path used to escape it.
	rc.spendBudget(decision.BudgetConsumeAttempt)
	if err != nil {
		// Caller disconnected mid-request is terminal (can't switch — the
		// caller is gone). Distinguish context.Canceled (client gone) from
		// context.DeadlineExceeded (server-side/per-attempt timeout, which
		// is candidate-level, not a disconnect) so the log labels the right
		// failure class. Any other transport failure is candidate-level.
		if errors.Is(c.Request.Context().Err(), context.Canceled) {
			rc.recordCurrentAttempt(0, AttemptConnError, "client disconnected")
			s.abandonRequestAfterAttempt(rc, "client_disconnected", start) // nginx-style 499
			return attemptTerminal, nil
		}
		// Reported as the kernel's own fact so the timeline shows the
		// judgement; the attempt was already charged at the dispatch above,
		// and the routing (fail over) matches the row while staying explicit
		// here because the disconnect branch above must keep precedence.
		sink := newExchangeSink(rc)
		sink.reporter = kernelReporter
		sink.Report(fact.Fact{Kind: fact.KindUpstreamTransportFailure})
		rc.recordCurrentAttempt(0, AttemptConnError,
			redactedFailure(err, rc.attempt.UpstreamURL()))
		return attemptNextCandidate, nil
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
		return s.deliverAndSettle(c, rc, adm, resp, start), nil
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
	// this matches the upstream request capture's "last attempt wins" rule above —
	// an empty errBody from THIS attempt must clear out a stale non-empty body
	// left by an earlier failed candidate, not leave it looking current.
	// A subsequent SUCCESSFUL stream candidate clears it entirely.
	//
	// The body is already wrapped with a short total budget
	// (errorBodyTotalBudget) so a slow-trickle upstream cannot hold this read
	// open beyond that window.
	errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	rc.bodies.SetUpstreamResponse(errBody)
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
	up := fact.Upstream{
		StatusCode: statusCode,
		Header:     resp.Header,
		Body:       errBody,
		Elapsed:    time.Since(start),
	}
	observed := s.observeUpstreamError(ctx, rc, up)
	// Failure rewriters see the same failed response and may offer a
	// repaired body. What they report folds with what the observers
	// reported: one verdict, however many mouths spoke.
	repaired, offered := s.rewriteAfterFailure(ctx, rc, egress.Protocol, outBody, up)
	observed = decision.Combine(observed, offered)
	// A repair addresses the payload, so it executes only when the status
	// says the payload is what failed AND the candidate's repair allowance
	// remains. A rejected credential or a throttled key is not a payload
	// problem — no body change can address it, and re-sending on the same
	// key would burn the budget against a cause the repair cannot touch (a
	// 401 has also just marked the key failed, so retrying it would dispatch
	// on a credential already known bad). A provider fault is not one
	// either, and neither are the 4xx that judge the caller, the account, or
	// the route rather than the bytes. And a repair already spent is an
	// answer already given. In all those cases the verdict is logged as
	// unexecuted and the baseline joins the fold, so the failure keeps its
	// full normal handling — rotation, failover, or surfacing the upstream's
	// own status — instead of being discarded after a half-executed retry.
	retryExecutable := observed.Loop == decision.LoopRetrySameCandidate &&
		repaired != nil &&
		repairAllowed &&
		payloadRepairableUpstreamStatus(statusCode)
	// A retry-same verdict that cannot be executed — no repaired body behind
	// it, or a failure no payload repair can address — is logged loudly, and
	// the fold below then treats it as no routing opinion, so the kernel's
	// baseline decides: routing, sticky and status all together, exactly as
	// if the repair had never been offered. Substituting the routing alone
	// was tried and is not enough: it left the baseline's sticky behind, and
	// a chain exhausted on rate limits then answered with a generic 502
	// instead of the 429 the caller should back off on.
	if observed.Loop == decision.LoopRetrySameCandidate && !retryExecutable {
		logger.Warn("gateway: reported verdict is not executed on this path",
			zap.String("request_id", rc.requestID),
			zap.String("verdict", observed.LoopFrom().String()),
			zap.Int("upstream_status", statusCode))
	}
	// The kernel is a reporter too. When the observers expressed no
	// executable routing opinion, the status line is the only evidence there
	// is, and the kernel files its own reading of it through the same
	// vocabulary, so the routing below comes out of one table however the
	// judgement was reached.
	//
	// The baseline is folded in ONLY on that condition, and the asymmetry is
	// deliberate: an observer that steered has read the body, the kernel has
	// read three digits, and a body-informed verdict must beat a
	// status-informed one outright. Folding the two unconditionally would let
	// the baseline's "terminate" (any unrecognised 4xx) out-rank a moderation
	// refusal's "try the next candidate" — the exact upgrade the observation
	// point exists to make possible.
	if observed.Loop <= decision.LoopContinue ||
		(observed.Loop == decision.LoopRetrySameCandidate && !retryExecutable) {
		sink := newExchangeSink(rc)
		sink.reporter = kernelReporter
		sink.Report(kernelUpstreamFact(statusCode))
		observed = decision.Combine(observed, sink.resolve())
	}
	// The retry-same executor. The table judged a repaired body worth
	// another attempt against this candidate, the failure was one a repair
	// can address, and the body to re-send exists: the attempt is recorded
	// and the pair goes back to the key loop, whose budget gate bounds how
	// often a repair can recur.
	if retryExecutable {
		rc.recordCurrentAttempt(statusCode, class.Outcome, note)
		return attemptRetrySame, repaired
	}
	// A body-informed refusal relabels the attempt record: the payload was
	// judged, not the provider, and the row should say which happened.
	if observed.LoopFrom() == fact.KindPayloadRefused {
		class.Outcome = AttemptContentFiltered
		note = fmt.Sprintf("upstream %d content inspection", statusCode)
	}

	// Recorded from the table rather than from the routing below, which is what
	// makes the slot general: the routing decides where the chain goes next, the
	// table decides whether this verdict is worth quoting if the chain then
	// runs out. A verdict with no status to offer records nothing — quoting a
	// zero would tell the caller the request ended without ever being answered.
	if observed.Sticky != decision.StickyNone {
		if status, errType := observed.CallerFacing(statusCode, class.ErrorType); status != 0 {
			rc.attempt.HoldVerdict(decision.StickyVerdict{
				Status:  status,
				ErrType: errType,
				Detail:  observed.RejectDetail(),
				Reason:  observed.FailReason(),
			})
		}
	}

	rc.recordCurrentAttempt(statusCode, class.Outcome, note)

	// The resolved Loop routes the chain from here.
	switch {
	case observed.Loop >= decision.LoopTerminate:
		// The caller's request is the problem, or a reporter ended the chain:
		// surfaced as-is, no rotation, no failover.
		status, errType := observed.CallerFacing(statusCode, class.ErrorType)
		if status == 0 {
			status, errType = statusCode, class.ErrorType
		}
		if !c.Writer.Written() {
			detail := observed.RejectDetail()
			if detail == "" {
				detail = safeUpstreamMessage(status)
			}
			WriteIngressError(c, rc.ingress, status, errType, detail, rc.requestID)
		}
		s.settleAfterAttempt(rc, fact.Rejected(status, fact.FaultUpstream,
			observed.FailReason(), nil), start)
		return attemptTerminal, nil
	case observed.Loop == decision.LoopNextCandidate:
		return attemptNextCandidate, nil
	case observed.Loop == decision.LoopRotateKey:
		// 401 CAS was already performed above, before the error body read.
		return attemptRotateKey, nil
	default:
		// Unreachable while the baseline fold above holds: every kernel row
		// steers at least as strongly as a key rotation. Routed rather than
		// panicking so a future weakening fails toward failover, the least
		// damaging wrong answer.
		return attemptNextCandidate, nil
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
	// The chain ended on a verdict somebody thought worth quoting — a payload
	// refusal is the motivating case. That is a judgement on the request, not
	// an outage, so the caller is shown what was actually said: a 502 "all
	// upstream candidates failed" would send them looking for a broken provider
	// instead of at their own prompt.
	//
	// One slot, holding the last attempt's verdict: whoever ends the chain
	// leaves theirs behind, and everyone before them has already had theirs
	// overwritten or cleared.
	if v := rc.attempt.Verdict(); v.Held() {
		detail := v.Detail
		if detail == "" {
			detail = "upstream refused this request"
		}
		s.rejectRequest(c, rc, v.Status, v.ErrType, detail, v.Reason, fact.FaultUpstream, start)
		return
	}
	// The chain ended with a budget — wall-clock or count — spent, and the
	// table's request-budget row supplies the verdict for that. Recomputed
	// here rather than flagged at the loop gate, because the gate only sees
	// exhaustion when ANOTHER candidate iteration reaches it: a budget spent
	// by the final candidate would otherwise change the answer depending on
	// whether an unused candidate happened to remain. The sticky check above
	// deliberately still wins: a held verdict names what actually went wrong
	// before the allowance ran dry, and that is the answer the caller can
	// act on.
	if s.exhaustedBudget(rc) {
		sink := newExchangeSink(rc)
		sink.reporter = kernelReporter
		// Reason is an explicit stable code (persisted, mapped in the
		// frontend), never derived from the Kind's internal name.
		sink.Report(fact.Fact{Kind: fact.KindRequestBudgetExhausted, Reason: "request_budget_exhausted"})
		v := sink.resolve()
		status, errType := v.CallerFacing(0, "")
		s.rejectRequest(c, rc, status, errType,
			"request budget exhausted", v.FailReason(), fact.FaultUpstream, start)
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

// recordAttempt builds one AttemptRecord and appends it to the exchange's
// attempt log — the one place that log grows, so every recorded try passes
// through the same construction. provider and key are nil-able: nil provider
// marks a candidate whose provider was missing/disabled; nil key marks a
// candidate-level failure before any key was tried (load failed, no enabled
// key, rewrite failed).
//
// It is an Exchange method so it can stamp the attempt with
// rc.attempt.UpstreamURL() — the URL the gateway dispatched to for this
// attempt — without every caller threading the URL through. That URL is reset
// per candidate in relayCandidates and set in attemptOne, so it reflects the
// current attempt: empty for attempts that failed before any request was
// sent.
// recordCurrentAttempt records the attempt rc.attempt currently describes —
// the form every post-BeginCandidate path uses, so the identity on the row
// and the identity on the state cannot disagree. The explicit recordAttempt
// below stays for the loop's pre-bind skip rows, which deliberately name a
// provider the state never bound (a disabled provider is on the row so the
// operator sees who was skipped, and off the state so finalize never reports
// it as the final hit).
func (rc *Exchange) recordCurrentAttempt(status int, outcome, failReason string) {
	rc.recordAttempt(*rc.attempt.Candidate(), rc.attempt.Provider(), rc.attempt.Key(), status, outcome, failReason)
}

func (rc *Exchange) recordAttempt(cand model.ModelCandidate, provider *model.Provider, key *model.ProviderKey, status int, outcome, failReason string) {
	rec := AttemptRecord{
		CandidateID:       cand.ID,
		ProviderModelName: cand.ProviderModelName,
		StatusCode:        status,
		Outcome:           outcome,
		FailReason:        failReason,
		UpstreamURL:       rc.attempt.UpstreamURL(),
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
	rc.attempts = append(rc.attempts, rec)
}
