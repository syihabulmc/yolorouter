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

	"github.com/yolorouter/yolorouter/internal/model"
	"github.com/yolorouter/yolorouter/internal/protocols"
	"github.com/yolorouter/yolorouter/internal/repository"
	"github.com/yolorouter/yolorouter/pkg/crypto"
	"github.com/yolorouter/yolorouter/pkg/logger"
)

// upstreamRequestTimeout caps a single upstream attempt's full duration
// (connect + headers + body read). Applied via context.WithTimeout on the
// per-attempt request context. A streaming response longer than this is cut
// — v0.1's documented limit; a per-stage timeout split (dial/header vs body)
// is deferred. Named for what it actually bounds, not "dial only".
const upstreamRequestTimeout = 120 * time.Second

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
func captureRejectedBody(c *gin.Context, rc *RelayContext) {
	if rc.RequestBody != nil {
		return // already captured (e.g. body read succeeded then a later check failed)
	}
	if c.Request == nil {
		return
	}
	if body := ReadAuditBody(c.Request.Body); body != nil {
		rc.RequestBody = body
	}
}

// testHookHandleDone, when non-nil, is invoked with the RelayContext at the
// end of every Handle call (success or failure). Test-only wiring — Handle
// intentionally doesn't expose its internal RelayContext in its public
// signature, so tests needing to inspect it (e.g. the captured request/
// response bodies) set this hook instead. Always nil in production.
var testHookHandleDone func(*RelayContext)

// RelayService is the gateway orchestrator. One instance lives for the
// process lifetime (created in router.New); it owns the DB, the master key
// for decrypting provider keys, an upstream HTTP client, and the in-memory
// rate limiter.
type RelayService struct {
	db        *gorm.DB
	masterKey []byte
	client    *UpstreamClient
	limiter   *Limiter
}

// NewRelayService wires the gateway with the already-decoded AES master key
// (the same one provider_service uses to decrypt the keys it now routes to).
// allowPrivate is forwarded to the upstream client's SSRF transport (config.
// SecurityConfig.AllowPrivateUpstreams) so LAN/localhost providers relay.
func NewRelayService(db *gorm.DB, masterKey []byte, allowPrivate bool) *RelayService {
	return &RelayService{
		db:        db,
		masterKey: masterKey,
		client:    NewUpstreamClient(allowPrivate),
		limiter:   NewLimiter(),
	}
}

// requestIDFor returns the request id the RequestID middleware already
// generated (uuid, set on the gin context + X-Request-Id header), so the
// gateway's error messages and request_logs row share ONE id with the
// access log — not a second unrelated id. Falls back to a fresh hex id only
// if some route mounted RelayService without the RequestID middleware.
func requestIDFor(c *gin.Context) string {
	if id := c.GetString("request_id"); id != "" {
		return id
	}
	return generateRequestID()
}

// Handle is POST /v1/chat/completions. apiKey is the already-authenticated
// caller key (middleware.APIKeyAuth resolved and validated it). The handler
// runs the full pipeline: pre-checks → model lookup → allowlist →
// validate → candidate chain with Key rotation + failover → response rewrite
// → log. Every exit path writes exactly one request_logs row via finalize.
func (s *RelayService) Handle(c *gin.Context, apiKey *model.APIKey) {
	start := time.Now()
	rc := &RelayContext{
		RequestID: requestIDFor(c),
		APIKeyID:  apiKey.ID,
	}
	// The ingress protocol is a property of the request path, computed once
	// up front so every error write in this function (and the pre-candidate
	// validation below) uses the wire envelope the caller actually expects
	// instead of always assuming OpenAI.
	ingress := IngressProtocol(c.Request.URL.Path)
	rc.Ingress = ingress
	// Put rc on the gin context so WriteOpenAIError*
	// (called from many exit paths below, and potentially from further down
	// the chain) can stash the local error JSON into rc.ResponseBody without
	// every call site threading an *RelayContext parameter through.
	c.Set(relayContextKey, rc)
	// Capture the caller's request headers once at entry (masked
	// via SanitizeHeaders) so even an early rejection below still records
	// them. c.Request is always non-nil here (gin populates it), but guard
	// anyway for direct-call tests.
	if c.Request != nil {
		rc.RequestHeaders = SanitizeHeaders(c.Request.Header)
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
		// Test-only hook: Handle doesn't return its internal RelayContext, so
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
			WriteIngressError(c, ingress, http.StatusTooManyRequests, errTypeRateLimit, "concurrency limit exceeded", rc.RequestID)
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
			WriteIngressError(c, ingress, http.StatusTooManyRequests, errTypeRateLimit, "rate limit exceeded (requests per minute)", rc.RequestID)
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
		WriteIngressError(c, ingress, status, errTypeInvalidRequest, message, rc.RequestID)
		s.finalize(rc, status, reason, start)
		return
	}
	// Stash the caller-facing request body for the
	// request_log_bodies row, verbatim (v0.1 does not scrub body content).
	rc.RequestBody = body

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
			WriteIngressError(c, ingress, http.StatusBadRequest, errTypeInvalidRequest, "invalid request path", rc.RequestID)
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
		WriteIngressError(c, ingress, http.StatusBadRequest, errTypeInvalidRequest, "invalid request body", rc.RequestID)
		s.finalize(rc, http.StatusBadRequest, "parse: "+err.Error(), start)
		return
	}
	if meta.Model == "" {
		WriteIngressError(c, ingress, http.StatusBadRequest, errTypeInvalidRequest, "model is required", rc.RequestID)
		s.finalize(rc, http.StatusBadRequest, "empty_model", start)
		return
	}
	rc.OriginalModel = meta.Model
	rc.IsStream = meta.Stream
	rc.WantsStreamUsage = meta.WantsStreamUsage

	// Step 4: model exists and is enabled. A model disabled by an admin
	// must not route even if its candidates are still enabled.
	m, err := repository.FindModelByName(s.db, meta.Model)
	if err != nil {
		if errors.Is(err, gorm.ErrRecordNotFound) {
			WriteIngressError(c, ingress, http.StatusNotFound, errTypeNotFound, "model does not exist", rc.RequestID)
			s.finalize(rc, http.StatusNotFound, "model_not_found", start)
			return
		}
		logger.Error("gateway: find model", zap.String("request_id", rc.RequestID), zap.Error(err))
		WriteIngressError(c, ingress, http.StatusInternalServerError, errTypeServer, "internal error", rc.RequestID)
		s.finalize(rc, http.StatusInternalServerError, "db_model: "+err.Error(), start)
		return
	}
	if m.ManagementStatus != model.ModelStatusEnabled {
		WriteIngressError(c, ingress, http.StatusNotFound, errTypeNotFound, "model does not exist", rc.RequestID)
		s.finalize(rc, http.StatusNotFound, "model_disabled", start)
		return
	}

	// Step 5: allowlist.
	allowed, err := repository.HasAPIKeyModelAccess(s.db, apiKey.ID, m.ID)
	if err != nil {
		logger.Error("gateway: allowlist", zap.String("request_id", rc.RequestID), zap.Error(err))
		WriteIngressError(c, ingress, http.StatusInternalServerError, errTypeServer, "internal error", rc.RequestID)
		s.finalize(rc, http.StatusInternalServerError, "db_allowlist: "+err.Error(), start)
		return
	}
	if !allowed {
		WriteIngressError(c, ingress, http.StatusForbidden, errTypePermission, "model is not in this API key's allowlist", rc.RequestID)
		s.finalize(rc, http.StatusForbidden, "model_not_allowed", start)
		return
	}

	// Step 6: top-level structural validation (messages non-empty, Claude's
	// max_tokens invariant, OpenAI's parsedRequest.validate() rules).
	if err := meta.validate(); err != nil {
		WriteIngressError(c, ingress, http.StatusBadRequest, errTypeInvalidRequest, err.Error(), rc.RequestID)
		s.finalize(rc, http.StatusBadRequest, "validate: "+err.Error(), start)
		return
	}

	// Step 6.5: full protocol-specific structural decode (message content
	// shapes, tool schemas, ...) — the same decode the request will
	// eventually go through on the hot path. Run once here, BEFORE any
	// candidate is picked, so a malformed body is rejected as a 400 client
	// error instead of surfacing later as a misleading "all upstream
	// candidates failed" 502 once relayCandidates has already started.
	if err := validateIngressBody(ingress, body, rc.OriginalModel, rc.IsStream); err != nil {
		WriteIngressError(c, ingress, http.StatusBadRequest, errTypeInvalidRequest, "invalid request body: "+err.Error(), rc.RequestID)
		s.finalize(rc, http.StatusBadRequest, "invalid_request: "+err.Error(), start)
		return
	}

	// Step 7: candidates filtered by requested capability.
	allCandidates, err := repository.ListModelCandidatesByModelID(s.db, m.ID)
	if err != nil {
		logger.Error("gateway: list candidates", zap.String("request_id", rc.RequestID), zap.Error(err))
		WriteIngressError(c, ingress, http.StatusInternalServerError, errTypeServer, "internal error", rc.RequestID)
		s.finalize(rc, http.StatusInternalServerError, "db_candidates: "+err.Error(), start)
		return
	}
	routable, anyEnabled, anyVerified := filterCandidates(allCandidates, meta.Stream, meta.HasTools)
	if len(routable) == 0 {
		reason := "no_enabled_candidate"
		if anyVerified {
			reason = "no_capability_candidate"
		} else if anyEnabled {
			reason = "no_verified_candidate"
		}
		WriteIngressError(c, ingress, http.StatusServiceUnavailable, errTypeUnavailable, "model is not available", rc.RequestID)
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
func (s *RelayService) checkKeyStateAndLimits(c *gin.Context, rc *RelayContext, apiKey *model.APIKey, start time.Time) bool {
	ingress := rc.Ingress
	if apiKey.Status == model.APIKeyStatusRevoked {
		captureRejectedBody(c, rc)
		WriteIngressError(c, ingress, http.StatusUnauthorized, errTypeAuthentication, "API key revoked", rc.RequestID)
		s.finalize(rc, http.StatusUnauthorized, "revoked", start)
		return false
	}
	if apiKey.ExpiresAt != nil && apiKey.ExpiresAt.Before(time.Now().UTC()) {
		captureRejectedBody(c, rc)
		WriteIngressError(c, ingress, http.StatusUnauthorized, errTypeAuthentication, "API key expired", rc.RequestID)
		s.finalize(rc, http.StatusUnauthorized, "expired", start)
		return false
	}
	if apiKey.BudgetLimitMicros != nil && apiKey.BudgetSpentMicros >= *apiKey.BudgetLimitMicros {
		captureRejectedBody(c, rc)
		WriteIngressError(c, ingress, http.StatusTooManyRequests, errTypeInsufficientQuota, "budget limit exceeded", rc.RequestID)
		s.finalize(rc, http.StatusTooManyRequests, "budget_exceeded", start)
		return false
	}
	return true
}

// relayCandidates walks the candidate chain in sort_order. For each
// candidate it loads the provider's enabled keys, decrypts them one at a
// time, and sends the upstream request; Key rotation and candidate failover
// decisions come back from tryKeys.
func (s *RelayService) relayCandidates(c *gin.Context, rc *RelayContext, candidates []model.ModelCandidate, start time.Time) {
	// The ingress protocol is a property of the request path, not of any
	// individual candidate — threaded through every candidate/key attempt
	// below.
	ingress := rc.Ingress
	for i := range candidates {
		cand := candidates[i]
		rc.Candidate = &cand
		// Reset per iteration: rc.Provider is set only when this candidate's
		// provider is usable, so a `continue` path (provider missing/disabled,
		// load-keys failed, no enabled key, rewrite failed) doesn't leave a
		// stale provider from a previous iteration on rc — which finalize
		// would otherwise record as the "final hit provider" of an all-failed
		// request.
		rc.Provider = nil

		provider := cand.Provider
		if provider == nil {
			rc.Attempts = append(rc.Attempts, makeAttempt(cand, nil, nil, 0, AttemptBadStatus, "provider missing (preload)"))
			continue
		}
		if provider.ManagementStatus != model.ProviderStatusEnabled {
			rc.Attempts = append(rc.Attempts, makeAttempt(cand, provider, nil, 0, AttemptBadStatus, "provider disabled"))
			continue
		}
		rc.Provider = provider

		keys, err := repository.ListProviderKeysByProvider(s.db, provider.ID)
		if err != nil {
			logger.Error("gateway: list provider keys", zap.String("request_id", rc.RequestID), zap.Error(err))
			rc.Attempts = append(rc.Attempts, makeAttempt(cand, provider, nil, 0, AttemptBadStatus, "load keys failed"))
			continue
		}
		enabled, anyEnabledKey := filterEnabledKeys(keys)
		if len(enabled) == 0 {
			reason := "no enabled key"
			if anyEnabledKey {
				reason = "no verified key"
			}
			rc.Attempts = append(rc.Attempts, makeAttempt(cand, provider, nil, 0, AttemptBadStatus, reason))
			continue
		}

		// Step 9: negotiate the wire protocol to speak to this candidate's
		// provider — the ingress protocol when the provider accepts it
		// directly (passthrough, no IR round trip), otherwise the
		// provider's own primary protocol (buildDispatchRequest/
		// processDispatchResponseNonStream do the IR decode/encode).
		egress, err := Negotiate(ingress, provider)
		if err != nil {
			rc.Attempts = append(rc.Attempts, makeAttempt(cand, provider, nil, 0, AttemptBadStatus, "negotiate egress: "+err.Error()))
			continue // mapping failure -> skip candidate
		}

		// Build the upstream body/URL once per candidate — it only depends
		// on the candidate/egress choice (model rewrite OR IR decode/encode
		// + stream-usage injection + URL), not on which key ends up sending
		// it, so every key attempt below reuses the same bytes instead of
		// rebuilding them.
		outBody, url, err := s.buildUpstreamBody(rc, ingress, egress)
		if err != nil {
			rc.Attempts = append(rc.Attempts, makeAttempt(cand, provider, nil, 0, AttemptBadStatus, "build request: "+err.Error()))
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
func (s *RelayService) tryKeys(c *gin.Context, rc *RelayContext, cand *model.ModelCandidate, provider *model.Provider, keys []model.ProviderKey, egress *EgressDecision, outBody []byte, url string, start time.Time) relayOutcome {
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
			rc.Attempts = append(rc.Attempts, makeAttempt(*cand, provider, &pk, 0, AttemptAuthFailed, "destination version mismatch"))
			continue
		}
		plaintext, derr := crypto.Decrypt(s.masterKey, pk.EncryptedKey)
		if derr != nil {
			logger.Warn("gateway: decrypt provider key failed",
				zap.Uint("key_id", pk.ID), zap.String("request_id", rc.RequestID), zap.Error(derr))
			rc.Attempts = append(rc.Attempts, makeAttempt(*cand, provider, &pk, 0, AttemptBadStatus, "decrypt failed"))
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
func (s *RelayService) attemptOne(c *gin.Context, rc *RelayContext, cand model.ModelCandidate, provider *model.Provider, pk model.ProviderKey, plaintext string, egress *EgressDecision, outBody []byte, url string, start time.Time) attemptResult {
	ctx, cancel := context.WithTimeout(c.Request.Context(), upstreamRequestTimeout)
	defer cancel()

	// Record the rewritten (provider_model_name) request
	// actually sent upstream, verbatim. Overwritten on every attempt — the
	// last write wins, matching the "successful attempt, else the last
	// attempt" rule.
	rc.UpstreamRequestBody = outBody

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(outBody))
	if err != nil {
		// A build failure here is a candidate-level problem, mirroring the
		// old rewrite-model-failed skip: nothing has been sent, so fail
		// over. Every key on this candidate would fail identically (url is
		// candidate-invariant), so the first key attempt already exhausts
		// this candidate via tryKeys' immediate return on attemptNextCandidate.
		rc.Attempts = append(rc.Attempts, makeAttempt(cand, provider, &pk, 0, AttemptBadStatus, "build request: "+err.Error()))
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
			rc.Attempts = append(rc.Attempts, makeAttempt(cand, provider, &pk, 0, AttemptConnError, "client disconnected"))
			s.finalize(rc, 499, "client_disconnected", start) // nginx-style 499
			return attemptTerminal
		}
		rc.Attempts = append(rc.Attempts, makeAttempt(cand, provider, &pk, 0, AttemptConnError, err.Error()))
		return attemptNextCandidate
	}

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		// 2xx — dispatch directly instead of through a one-line trampoline.
		ingress := rc.Ingress
		if rc.IsStream {
			return s.processDispatchResponseStream(c, rc, ingress, egress, cand, provider, pk, resp, start)
		}
		return s.processDispatchResponseNonStream(c, rc, ingress, egress, cand, provider, pk, resp, start)
	}

	statusCode := resp.StatusCode
	// Capture the obtainable upstream error body before close, verbatim.
	// Error bodies are small; cap at 1MiB — beyond that is
	// truncation of an error diagnostic, not a response body, and 1MiB is
	// ample for debugging. Unconditionally overwritten (even when empty) so
	// this matches rc.UpstreamRequestBody's "last attempt wins" rule above —
	// an empty errBody from THIS attempt must clear out a stale non-empty body
	// left by an earlier failed candidate, not leave it looking current.
	// A subsequent SUCCESSFUL stream candidate clears
	// it entirely — see dispatchPassthroughStream (dispatch.go).
	errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	rc.UpstreamResponseBody = errBody
	_ = resp.Body.Close()

	class := classifyUpstreamStatus(statusCode)
	note := fmt.Sprintf("upstream %d", statusCode)
	rc.Attempts = append(rc.Attempts, makeAttempt(cand, provider, &pk, statusCode, class.Outcome, note))
	switch class.Category {
	case statusRotateKey:
		// A 401 means the credential itself was rejected —
		// persist verification_status=Failed so subsequent requests skip
		// this key (filterEnabledKeys checks verification_status) instead
		// of retrying the dead credential first. 429 (rate limit) is NOT
		// persisted: it's transient and the key is still valid. CAS on
		// (Passed, destination) so a concurrent edit or destination change
		// can't be clobbered; applied=false is a benign lost race.
		if statusCode == http.StatusUnauthorized {
			if applied, mErr := repository.MarkProviderKeyVerificationFailedIfCurrent(s.db, pk.ID, provider.DestinationVersion, time.Now()); mErr != nil {
				logger.Warn("gateway: mark provider key failed",
					zap.Uint("key_id", pk.ID), zap.String("request_id", rc.RequestID), zap.Error(mErr))
			} else if !applied {
				logger.Debug("gateway: provider key invalidation CAS lost race",
					zap.Uint("key_id", pk.ID), zap.String("request_id", rc.RequestID))
			}
		}
		return attemptRotateKey
	case statusFailover:
		return attemptNextCandidate
	default: // statusTerminalClient — caller's request is the problem, no switch.
		if !c.Writer.Written() {
			WriteIngressError(c, rc.Ingress, statusCode, class.ErrorType, safeUpstreamMessage(statusCode), rc.RequestID)
		}
		s.finalize(rc, statusCode, fmt.Sprintf("upstream_client_error_%d", statusCode), start)
		return attemptTerminal
	}
}

// allCandidatesFailed is reached only when every candidate was tried without
// a response being written — the writer is guaranteed not yet written, but
// the guard is kept defensively in case a future caller changes that.
func (s *RelayService) allCandidatesFailed(c *gin.Context, rc *RelayContext, start time.Time) {
	if c.Writer.Written() {
		status := rc.StatusCode
		if status == 0 {
			status = http.StatusBadGateway
		}
		s.finalize(rc, status, "partial_then_exhausted", start)
		return
	}
	status := http.StatusBadGateway
	WriteIngressError(c, rc.Ingress, status, errTypeUpstream, "all upstream candidates failed", rc.RequestID)
	s.finalize(rc, status, "all_candidates_failed", start)
}

// filterCandidates returns the subset of candidates eligible for this
// request: enabled, and supporting the stream / function-calling capability
// the caller asked for. anyEnabled is reported in the
// same pass so the caller can distinguish "no candidate at all" from "no
// candidate matched the capability" without walking the slice twice. Order
// is preserved (sort_order was applied by the repository) so failover still
// walks the chain in the admin's configured order.
// filterCandidates returns routable candidates plus two diagnostic flags so
// the caller can pick the right "why empty" reason: anyEnabled = at least one
// management-enabled candidate (distinguishes "all disabled" from anything
// else); anyVerified = at least one enabled AND verification-passed
// (distinguishes "enabled but unverified/failed" from "capability mismatch").
func filterCandidates(all []model.ModelCandidate, isStream, hasTools bool) (routable []model.ModelCandidate, anyEnabled, anyVerified bool) {
	for _, c := range all {
		if c.ManagementStatus != model.ModelCandidateStatusEnabled {
			continue
		}
		anyEnabled = true
		// An enabled-but-verification-failed candidate is NOT routable:
		// TestModelCandidate flips verification_status to failed while
		// leaving management status enabled, and ModelService's own
		// routability check (model_service.go) already rejects these —
		// the gateway must match that gate or it routes a known-broken
		// mapping.
		if c.VerificationStatus != model.ModelVerificationStatusPassed {
			continue
		}
		anyVerified = true
		if isStream && !c.SupportsStreaming {
			continue
		}
		if hasTools && !c.SupportsFunctionCalling {
			continue
		}
		routable = append(routable, c)
	}
	return routable, anyEnabled, anyVerified
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
// enabled key, rewrite failed). One constructor replaces the former
// makeAttempt/makeAttemptWithKey pair.
func makeAttempt(cand model.ModelCandidate, provider *model.Provider, key *model.ProviderKey, status int, outcome, failReason string) AttemptRecord {
	rec := AttemptRecord{
		CandidateID:       cand.ID,
		ProviderModelName: cand.ProviderModelName,
		StatusCode:        status,
		Outcome:           outcome,
		FailReason:        failReason,
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
