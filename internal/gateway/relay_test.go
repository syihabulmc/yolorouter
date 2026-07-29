package gateway

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	ycrypto "github.com/yolorouter/yolorouter/pkg/crypto"

	"github.com/yolorouter/yolorouter/internal/config"
	"github.com/yolorouter/yolorouter/internal/model"
	"github.com/yolorouter/yolorouter/internal/repository"
	"github.com/yolorouter/yolorouter/internal/settings"
	"github.com/yolorouter/yolorouter/internal/testutil"
)

// stubSettingsProvider implements SettingsProvider for tests (disabled by
// default). Tests that need a specific prompt can swap the value on the
// returned struct after construction.
type stubSettingsProvider struct {
	val             settings.CustomSystemPromptSetting
	compressEnabled bool
	compressErr     error
}

func (s stubSettingsProvider) CustomSystemPrompt(_ context.Context) (settings.CustomSystemPromptSetting, int64, error) {
	return s.val, 1, nil
}

func (s stubSettingsProvider) GetInputCompression(_ context.Context) (bool, int64, error) {
	return s.compressEnabled, 1, s.compressErr
}

// newRelaySvc builds a RelayService and swaps in a plain transport so the
// test can dial a loopback-bound httptest server — safehttp.NewTransport()
// (the production transport) deliberately refuses loopback (SSRF defense),
// which would block every test here. Same pattern as provider_client_test's
// newTestClient.
func newRelaySvc(t *testing.T, db *gorm.DB) *RelayService {
	t.Helper()
	return newRelaySvcWithSettings(t, db, stubSettingsProvider{})
}

// newRelaySvcWithSettingsAndGateway is the shared core behind
// newRelaySvcWithSettings / newRelaySvcWithGateway: it wires the RelayService
// against a caller-chosen settings provider AND gateway config, then swaps in
// a plain transport so the test can dial a loopback httptest server (the
// production safehttp transport deliberately refuses loopback for SSRF
// defense, which would block every test here).
func newRelaySvcWithSettingsAndGateway(t *testing.T, db *gorm.DB, sp stubSettingsProvider, gateway config.GatewayConfig) *RelayService {
	t.Helper()
	masterKey := bytes.Repeat([]byte{0x42}, 32)
	svc := NewRelayService(db, masterKey, false, sp, gateway)
	svc.client.httpClient.Transport = &http.Transport{}
	return svc
}

// newRelaySvcWithSettings is newRelaySvc with a caller-built stub, so a test
// can pre-seed the global compression / CSP state the gateway reads.
func newRelaySvcWithSettings(t *testing.T, db *gorm.DB, sp stubSettingsProvider) *RelayService {
	t.Helper()
	return newRelaySvcWithSettingsAndGateway(t, db, sp, testGatewayConfig())
}

// newRelaySvcWithGateway is newRelaySvc with a caller-built GatewayConfig, so
// a test can exercise a non-default request_timeout budget (e.g. asserting
// Handle stamps RequestDeadline = now + RequestTimeout).
func newRelaySvcWithGateway(t *testing.T, db *gorm.DB, gateway config.GatewayConfig) *RelayService {
	t.Helper()
	return newRelaySvcWithSettingsAndGateway(t, db, stubSettingsProvider{}, gateway)
}

func newCtx(body []byte) (*gin.Context, *httptest.ResponseRecorder) {
	return newCtxPath("/v1/chat/completions", body)
}

// newCtxPath is newCtx with a caller-chosen request path, so a test can
// exercise a non-OpenAI ingress (e.g. /v1/messages for Claude) through the
// same Handle entry point.
func newCtxPath(path string, body []byte) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, path, bytes.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	return c, w
}

func createProvider(t *testing.T, db *gorm.DB, name, baseURL string) *model.Provider {
	t.Helper()
	now := time.Now().UTC()
	p := &model.Provider{
		Name: name, ProviderType: "openai", BaseURL: baseURL,
		ManagementStatus: model.ProviderStatusEnabled, DestinationVersion: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(p).Error; err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	return p
}

func createProviderKey(t *testing.T, db *gorm.DB, masterKey []byte, providerID uint, plaintext, label string, order int, enabled bool) {
	t.Helper()
	now := time.Now().UTC()
	enc, err := ycrypto.Encrypt(masterKey, plaintext)
	if err != nil {
		t.Fatalf("encrypt upstream key: %v", err)
	}
	status := model.ProviderKeyStatusEnabled
	if !enabled {
		status = model.ProviderKeyStatusDisabled
	}
	pk := &model.ProviderKey{
		ProviderID: providerID, Label: label, EncryptedKey: enc, KeyPrefix: plaintext,
		SortOrder: order, TestModel: "m", ManagementStatus: status,
		VerificationStatus:           model.VerificationStatusPassed,
		AuthorizedDestinationVersion: 1, ConfigVersion: 1, TestGeneration: 1,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(pk).Error; err != nil {
		t.Fatalf("seed provider key: %v", err)
	}
}

func createModelAndCandidate(t *testing.T, db *gorm.DB, provider *model.Provider, externalName, providerModelName string, stream, fn bool, order int) *model.Model {
	t.Helper()
	now := time.Now().UTC()
	m := &model.Model{Name: externalName, ManagementStatus: model.ModelStatusEnabled, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(m).Error; err != nil {
		t.Fatalf("seed model: %v", err)
	}
	cand := &model.ModelCandidate{
		ModelID: m.ID, ProviderID: provider.ID, ProviderModelName: providerModelName,
		InputPrice: 1.0, OutputPrice: 2.0, MaxOutput: 4096,
		SupportsStreaming: stream, SupportsFunctionCalling: fn,
		ManagementStatus: model.ModelCandidateStatusEnabled, SortOrder: order,
		VerificationStatus: model.ModelVerificationStatusPassed,
		CreatedAt:          now, UpdatedAt: now,
	}
	if err := db.Create(cand).Error; err != nil {
		t.Fatalf("seed candidate: %v", err)
	}
	return m
}

func createAPIKey(t *testing.T, db *gorm.DB, status int, modelIDs []uint) *model.APIKey {
	t.Helper()
	now := time.Now().UTC()
	k := &model.APIKey{
		KeyHash: ycrypto.HashToken("sk-yr-test"), KeyPrefix: "sk-yr-test------",
		OwnerLabel: "tester", Status: status, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(k).Error; err != nil {
		t.Fatalf("seed api key: %v", err)
	}
	for _, mid := range modelIDs {
		if err := db.Create(&model.APIKeyModel{APIKeyID: k.ID, ModelID: mid, CreatedAt: now}).Error; err != nil {
			t.Fatalf("seed allowlist: %v", err)
		}
	}
	return k
}

// TestRelayNonStreamSuccess: a healthy upstream returns 200; the gateway
// rewrites the request model to the provider name, rewrites the response
// model back to the external name, extracts usage, and writes one log row.
func TestRelayNonStreamSuccess(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	var sawAuth, sawUpstreamModel bool
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sawAuth = r.Header.Get("Authorization") == "Bearer sk-upstream-1"
		body, _ := io.ReadAll(r.Body)
		sawUpstreamModel = bytes.Contains(body, []byte(`"model":"gpt-4o-real"`))
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-4o-real","choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`))
	}))
	defer upstream.Close()

	svc := newRelaySvc(t, db)
	p := createProvider(t, db, "p1", upstream.URL)
	createProviderKey(t, db, svc.masterKey, p.ID, "sk-upstream-1", "k1", 1, true)
	m := createModelAndCandidate(t, db, p, "gpt-4o", "gpt-4o-real", true, true, 1)
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, []uint{m.ID})

	c, w := newCtx([]byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`))
	svc.Handle(c, apiKey)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	if !sawAuth {
		t.Error("upstream did not receive the decrypted bearer key")
	}
	if !sawUpstreamModel {
		t.Error("upstream body model was not rewritten to the provider name")
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"model":"gpt-4o"`)) {
		t.Errorf("response model not rewritten back to external name: %s", w.Body.String())
	}
	var logCount int64
	db.Model(&model.RequestLog{}).Count(&logCount)
	if logCount != 1 {
		t.Fatalf("expected 1 request_log row, got %d", logCount)
	}
}

// TestRelayNonStreamScalarStopNotRejected is a regression test for the P1 fix
// to the OpenAI chat decoder's "stop" field: OpenAI documents "stop" as
// EITHER a single string OR an array of strings, but the decoder previously
// only accepted the array form, so a scalar "stop" failed the top-level JSON
// unmarshal inside validateIngressBody (relay.go) and the gateway rejected an
// otherwise-valid request with 400 before ever trying a candidate. This test
// pins that a scalar "stop" reaches the upstream successfully.
func TestRelayNonStreamScalarStopNotRejected(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-4o-real","choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`))
	}))
	defer upstream.Close()

	svc := newRelaySvc(t, db)
	p := createProvider(t, db, "p1", upstream.URL)
	createProviderKey(t, db, svc.masterKey, p.ID, "sk-upstream-1", "k1", 1, true)
	m := createModelAndCandidate(t, db, p, "gpt-4o", "gpt-4o-real", true, true, 1)
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, []uint{m.ID})

	reqBody := []byte(`{"model":"gpt-4o","stop":"END","messages":[{"role":"user","content":"hello"}]}`)
	c, w := newCtx(reqBody)
	svc.Handle(c, apiKey)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (a scalar \"stop\" must not be rejected by validateIngressBody); body = %s", w.Code, w.Body.String())
	}
}

// TestFinalizeNonStreamCapturesBodies: a 2xx
// non-stream upstream response is captured into the RelayContext's 4 body
// fields — the caller's original request, the rewritten (provider model
// name) request actually sent upstream, the raw upstream response (provider
// model name), and the caller-facing rewritten response (external model
// name). response_body and upstream_response_body must differ (only the
// model field), proving both are recorded independently and neither is a
// copy of the other. It asserts the RelayContext fields directly via
// testHookHandleDone AND (now that finalize persists the row) that
// the same four values landed in request_log_bodies.
func TestFinalizeNonStreamCapturesBodies(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-4o-real","choices":[{"message":{"role":"assistant","content":"raw upstream resp"}}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`))
	}))
	defer upstream.Close()

	svc := newRelaySvc(t, db)
	p := createProvider(t, db, "p1", upstream.URL)
	createProviderKey(t, db, svc.masterKey, p.ID, "sk-upstream-1", "k1", 1, true)
	m := createModelAndCandidate(t, db, p, "gpt-4o", "gpt-4o-real", true, true, 1)
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, []uint{m.ID})

	var captured *RelayContext
	testHookHandleDone = func(rc *RelayContext) { captured = rc }
	defer func() { testHookHandleDone = nil }()

	reqBody := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`)
	c, w := newCtx(reqBody)
	svc.Handle(c, apiKey)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	if captured == nil {
		t.Fatal("testHookHandleDone was never invoked")
	}

	if !bytes.Contains(captured.RequestBody, []byte(`"model":"gpt-4o"`)) {
		t.Errorf("RequestBody = %s, want it to contain the caller's original request", captured.RequestBody)
	}
	if !bytes.Contains(captured.UpstreamRequestBody, []byte(`"model":"gpt-4o-real"`)) {
		t.Errorf("UpstreamRequestBody = %s, want the rewritten (provider model name) request", captured.UpstreamRequestBody)
	}
	if !bytes.Contains(captured.UpstreamResponseBody, []byte(`"model":"gpt-4o-real"`)) {
		t.Errorf("UpstreamResponseBody = %s, want the raw upstream response (provider model name)", captured.UpstreamResponseBody)
	}
	if !bytes.Contains(captured.ResponseBody, []byte(`"model":"gpt-4o"`)) {
		t.Errorf("ResponseBody = %s, want the caller-facing rewritten response (external model name)", captured.ResponseBody)
	}
	if bytes.Equal(captured.ResponseBody, captured.UpstreamResponseBody) {
		t.Error("ResponseBody and UpstreamResponseBody must differ (post- vs pre-rewrite model field)")
	}

	// finalize must persist the same four values into
	// request_log_bodies, keyed by request_id (UPSERT, 1:1 with request_logs).
	dbBody, err := repository.GetRequestLogBodyByRequestID(db, captured.RequestID)
	if err != nil {
		t.Fatalf("GetRequestLogBodyByRequestID: %v", err)
	}
	if dbBody == nil {
		t.Fatal("expected a request_log_bodies row to be persisted by finalize")
	}
	if dbBody.RequestBody != string(captured.RequestBody) {
		t.Errorf("persisted RequestBody = %q, want %q", dbBody.RequestBody, captured.RequestBody)
	}
	if dbBody.UpstreamRequestBody != string(captured.UpstreamRequestBody) {
		t.Errorf("persisted UpstreamRequestBody = %q, want %q", dbBody.UpstreamRequestBody, captured.UpstreamRequestBody)
	}
	if dbBody.ResponseBody != string(captured.ResponseBody) {
		t.Errorf("persisted ResponseBody = %q, want %q", dbBody.ResponseBody, captured.ResponseBody)
	}
	if dbBody.UpstreamResponseBody != string(captured.UpstreamResponseBody) {
		t.Errorf("persisted UpstreamResponseBody = %q, want %q", dbBody.UpstreamResponseBody, captured.UpstreamResponseBody)
	}
}

// TestHandleSetsRequestDeadline verifies that Handle stamps the per-request
// total-budget deadline (RequestDeadline = now + RequestTimeout) on entry, so
// attemptOne can derive remaining = time.Until(RequestDeadline) for
// its min(attempt_timeout, remaining) per-attempt cap. The deadline is read
// here via the testHookHandleDone capture path (the same pattern
// TestFinalizeNonStreamCapturesBodies uses); a non-default request_timeout
// (15m, distinct from testGatewayConfig's 30m) makes the assertion
// unambiguous — a zero-value or stale deadline would miss the (14m, 15m)
// window.
func TestHandleSetsRequestDeadline(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-4o-real","choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`))
	}))
	defer upstream.Close()

	// Use a non-default RequestTimeout (15m vs the 30m default) so the
	// assertion pins the deadline to THIS config, not to any default.
	svc := newRelaySvcWithGateway(t, db, config.GatewayConfig{
		ConnectTimeout:   5 * time.Second,
		HeaderTimeout:    600 * time.Second,
		FirstByteTimeout: 600 * time.Second,
		BodyIdleTimeout:  60 * time.Second,
		AttemptTimeout:   10 * time.Minute,
		RequestTimeout:   15 * time.Minute,
	})
	p := createProvider(t, db, "p1", upstream.URL)
	createProviderKey(t, db, svc.masterKey, p.ID, "sk-upstream-1", "k1", 1, true)
	m := createModelAndCandidate(t, db, p, "gpt-4o", "gpt-4o-real", true, true, 1)
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, []uint{m.ID})

	var captured *RelayContext
	testHookHandleDone = func(rc *RelayContext) { captured = rc }
	defer func() { testHookHandleDone = nil }()

	c, w := newCtx([]byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`))
	svc.Handle(c, apiKey)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	if captured == nil {
		t.Fatal("testHookHandleDone was never invoked")
	}
	if captured.RequestDeadline.IsZero() {
		t.Fatal("RequestDeadline not set on RelayContext")
	}
	// Deadline should be ~before + RequestTimeout (15m), allowing for Handle's
	// own runtime. The window is wide enough to absorb test-machine variance
	// but narrow enough that a zero-value (year 1) or 30m-default deadline
	// would fail.
	remaining := time.Until(captured.RequestDeadline)
	if remaining <= 14*time.Minute || remaining >= 15*time.Minute {
		t.Errorf("RequestDeadline remaining = %v, want within (14m, 15m) for a 15m RequestTimeout", remaining)
	}
	// Sanity: the deadline must be in the future, never in the past.
	if remaining <= 0 {
		t.Errorf("RequestDeadline is in the past: remaining = %v", remaining)
	}
}

// TestRelayKeyRotation: the first key gets a 401, so the gateway
// rotates to the second key on the same provider and succeeds.
func TestRelayKeyRotation(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	var calls []string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls = append(calls, r.Header.Get("Authorization"))
		if r.Header.Get("Authorization") == "Bearer sk-bad" {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"m","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	svc := newRelaySvc(t, db)
	p := createProvider(t, db, "p1", upstream.URL)
	createProviderKey(t, db, svc.masterKey, p.ID, "sk-bad", "bad", 1, true)
	createProviderKey(t, db, svc.masterKey, p.ID, "sk-good", "good", 2, true)
	m := createModelAndCandidate(t, db, p, "gpt-4o", "gpt-4o-real", true, true, 1)
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, []uint{m.ID})

	c, w := newCtx([]byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	svc.Handle(c, apiKey)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 after rotation; body = %s", w.Code, w.Body.String())
	}
	if len(calls) != 2 || calls[0] != "Bearer sk-bad" || calls[1] != "Bearer sk-good" {
		t.Fatalf("expected calls [sk-bad, sk-good], got %v", calls)
	}
}

// TestRelayCandidateFailover: the first candidate's provider
// returns 500; the gateway fails over to the second candidate and succeeds.
// Each attempt must use its own candidate's provider_model_name.
func TestRelayCandidateFailover(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	var (
		seenMu     sync.Mutex
		seenModels []string
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenMu.Lock()
		seenModels = append(seenModels, extractModelFromJSON(t, body))
		seenMu.Unlock()
		if bytes.Contains(body, []byte(`"model":"c1-model"`)) {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"c2-model","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	svc := newRelaySvc(t, db)
	p1 := createProvider(t, db, "p1", upstream.URL)
	createProviderKey(t, db, svc.masterKey, p1.ID, "sk-1", "k1", 1, true)
	p2 := createProvider(t, db, "p2", upstream.URL)
	createProviderKey(t, db, svc.masterKey, p2.ID, "sk-2", "k1", 1, true)
	// Both candidates back the same external model, different provider names.
	now := time.Now().UTC()
	m := &model.Model{Name: "gpt-4o", ManagementStatus: model.ModelStatusEnabled, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(m).Error; err != nil {
		t.Fatalf("seed model: %v", err)
	}
	for i, p := range []*model.Provider{p1, p2} {
		name := "c1-model"
		if i == 1 {
			name = "c2-model"
		}
		if err := db.Create(&model.ModelCandidate{
			ModelID: m.ID, ProviderID: p.ID, ProviderModelName: name,
			InputPrice: 0, OutputPrice: 0, MaxOutput: 4096,
			SupportsStreaming: true, SupportsFunctionCalling: true,
			ManagementStatus: model.ModelCandidateStatusEnabled, SortOrder: i + 1,
			VerificationStatus: model.ModelVerificationStatusPassed,
			CreatedAt:          now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatalf("seed candidate %d: %v", i, err)
		}
	}
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, []uint{m.ID})

	c, w := newCtx([]byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	svc.Handle(c, apiKey)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 after failover; body = %s", w.Code, w.Body.String())
	}
	seenMu.Lock()
	got := append([]string(nil), seenModels...)
	seenMu.Unlock()
	if len(got) != 2 || got[0] != "c1-model" || got[1] != "c2-model" {
		t.Fatalf("expected attempts with [c1-model, c2-model], got %v", got)
	}
	// Each attempt used the current candidate's provider name.
	if !bytes.Contains(w.Body.Bytes(), []byte(`"model":"gpt-4o"`)) {
		t.Errorf("final response model not rewritten back to external: %s", w.Body.String())
	}
}

// TestRelayClientErrorNoSwitch: a 400 from the upstream is the
// caller's problem — no rotation, no failover, surfaced as-is.
func TestRelayClientErrorNoSwitch(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	var calls int
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls++
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"bad request"}}`))
	}))
	defer upstream.Close()

	svc := newRelaySvc(t, db)
	p := createProvider(t, db, "p1", upstream.URL)
	createProviderKey(t, db, svc.masterKey, p.ID, "sk-1", "k1", 1, true)
	createProviderKey(t, db, svc.masterKey, p.ID, "sk-2", "k2", 2, true)
	m := createModelAndCandidate(t, db, p, "gpt-4o", "gpt-4o-real", true, true, 1)
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, []uint{m.ID})

	c, w := newCtx([]byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	svc.Handle(c, apiKey)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (no switch on client error)", w.Code)
	}
	if calls != 1 {
		t.Fatalf("expected exactly 1 upstream call (no switch), got %d", calls)
	}
}

// TestRelayModelNotAllowed: a model outside the key's allowlist is
// rejected with 403 and never reaches the upstream.
func TestRelayModelNotAllowed(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	upstreamHit := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHit = true
	}))
	defer upstream.Close()

	svc := newRelaySvc(t, db)
	p := createProvider(t, db, "p1", upstream.URL)
	createProviderKey(t, db, svc.masterKey, p.ID, "sk-1", "k1", 1, true)
	createModelAndCandidate(t, db, p, "gpt-4o", "gpt-4o-real", true, true, 1)
	// Key has an EMPTY allowlist — no model is permitted.
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, nil)

	c, w := newCtx([]byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	svc.Handle(c, apiKey)

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403 (model not in allowlist)", w.Code)
	}
	if upstreamHit {
		t.Error("upstream must not be called when the model is not in the allowlist")
	}
}

// TestRelayAllowAllModelsBypassesAllowlist: a key flagged allow_all_models
// reaches the upstream even with an empty allowlist and a model it never listed.
func TestRelayAllowAllModelsBypassesAllowlist(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	upstreamHit := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHit = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-4o-real","choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`))
	}))
	defer upstream.Close()

	svc := newRelaySvc(t, db)
	p := createProvider(t, db, "p1", upstream.URL)
	createProviderKey(t, db, svc.masterKey, p.ID, "sk-1", "k1", 1, true)
	createModelAndCandidate(t, db, p, "gpt-4o", "gpt-4o-real", true, true, 1)
	// Empty allowlist, but the key is flagged to permit any model.
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, nil)
	apiKey.AllowAllModels = true

	c, w := newCtx([]byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	svc.Handle(c, apiKey)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (allow_all_models bypasses allowlist); body = %s", w.Code, w.Body.String())
	}
	if !upstreamHit {
		t.Error("upstream must be called when allow_all_models permits the model")
	}
}

// TestRelayRevokedKey: a revoked key is rejected with 401 and
// never reaches the upstream.
func TestRelayRevokedKey(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	upstreamHit := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHit = true
	}))
	defer upstream.Close()

	svc := newRelaySvc(t, db)
	p := createProvider(t, db, "p1", upstream.URL)
	createProviderKey(t, db, svc.masterKey, p.ID, "sk-1", "k1", 1, true)
	m := createModelAndCandidate(t, db, p, "gpt-4o", "gpt-4o-real", true, true, 1)
	apiKey := createAPIKey(t, db, model.APIKeyStatusRevoked, []uint{m.ID})

	c, w := newCtx([]byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	svc.Handle(c, apiKey)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 (revoked key)", w.Code)
	}
	if upstreamHit {
		t.Error("upstream must not be called when the API key is revoked")
	}
}

// TestHandleEarlyRejectionCapturesRequestBody: every
// early-rejection branch that runs before Handle's normal io.ReadAll(body)
// call — revoked/expired/budget (checkKeyStateAndLimits), concurrency, RPM —
// still records the caller's request body (bounded read) and the local
// error JSON as response_body, with upstream_* left empty (never dispatched
// to any provider).
func TestHandleEarlyRejectionCapturesRequestBody(t *testing.T) {
	reqBody := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hello"}]}`)

	cases := []struct {
		name          string
		configureKey  func(k *model.APIKey)
		preRejectHook func(svc *RelayService, apiKeyID uint)
		wantStatus    int
		wantRespSub   string
	}{
		{
			name:         "revoked",
			configureKey: func(k *model.APIKey) { k.Status = model.APIKeyStatusRevoked },
			wantStatus:   http.StatusUnauthorized,
			wantRespSub:  "API key revoked",
		},
		{
			name: "expired",
			configureKey: func(k *model.APIKey) {
				past := time.Now().UTC().Add(-time.Hour)
				k.ExpiresAt = &past
			},
			wantStatus:  http.StatusUnauthorized,
			wantRespSub: "API key expired",
		},
		{
			name: "budget_exceeded",
			configureKey: func(k *model.APIKey) {
				limit := int64(100)
				k.BudgetLimitMicros = &limit
				k.BudgetSpentMicros = 100
			},
			wantStatus:  http.StatusTooManyRequests,
			wantRespSub: "budget limit exceeded",
		},
		{
			name: "concurrency_limit",
			configureKey: func(k *model.APIKey) {
				limit := 1
				k.ConcurrencyLimit = &limit
			},
			preRejectHook: func(svc *RelayService, apiKeyID uint) {
				svc.limiter.AcquireConcurrency(apiKeyID, 1) // exhaust the only slot
			},
			wantStatus:  http.StatusTooManyRequests,
			wantRespSub: "concurrency limit exceeded",
		},
		{
			name: "rpm_exceeded",
			configureKey: func(k *model.APIKey) {
				limit := 1
				k.RPMLimit = &limit
			},
			preRejectHook: func(svc *RelayService, apiKeyID uint) {
				svc.limiter.CheckRPM(apiKeyID, 1, time.Now()) // consume the only token
			},
			wantStatus:  http.StatusTooManyRequests,
			wantRespSub: "rate limit exceeded",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := testutil.NewSQLiteDB(t)
			svc := newRelaySvc(t, db)
			p := createProvider(t, db, "p1", "http://unused.invalid")
			m := createModelAndCandidate(t, db, p, "gpt-4o", "gpt-4o-real", true, true, 1)
			apiKey := createAPIKey(t, db, model.APIKeyStatusActive, []uint{m.ID})
			tc.configureKey(apiKey)
			if err := db.Save(apiKey).Error; err != nil {
				t.Fatalf("update api key: %v", err)
			}
			if tc.preRejectHook != nil {
				tc.preRejectHook(svc, apiKey.ID)
			}

			var captured *RelayContext
			testHookHandleDone = func(rc *RelayContext) { captured = rc }
			defer func() { testHookHandleDone = nil }()

			c, w := newCtx(reqBody)
			svc.Handle(c, apiKey)

			if w.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", w.Code, tc.wantStatus, w.Body.String())
			}
			if captured == nil {
				t.Fatal("testHookHandleDone was never invoked")
			}
			if !bytes.Contains(captured.RequestBody, []byte(`"model":"gpt-4o"`)) {
				t.Errorf("RequestBody = %s, want it to contain the caller's request", captured.RequestBody)
			}
			if len(captured.UpstreamRequestBody) != 0 || len(captured.UpstreamResponseBody) != 0 {
				t.Errorf("expected empty upstream_* for a pre-dispatch rejection, got request=%q response=%q",
					captured.UpstreamRequestBody, captured.UpstreamResponseBody)
			}

			dbBody, err := repository.GetRequestLogBodyByRequestID(db, captured.RequestID)
			if err != nil {
				t.Fatalf("GetRequestLogBodyByRequestID: %v", err)
			}
			if dbBody == nil {
				t.Fatalf("expected a request_log_bodies row for the %s rejection", tc.name)
			}
			if !bytes.Contains([]byte(dbBody.RequestBody), []byte(`"model":"gpt-4o"`)) {
				t.Errorf("persisted request_body = %q, want it to contain the caller's request", dbBody.RequestBody)
			}
			if !bytes.Contains([]byte(dbBody.ResponseBody), []byte(tc.wantRespSub)) {
				t.Errorf("persisted response_body = %q, want it to contain %q", dbBody.ResponseBody, tc.wantRespSub)
			}
			if dbBody.UpstreamRequestBody != "" || dbBody.UpstreamResponseBody != "" {
				t.Errorf("expected empty upstream_* columns, got request=%q response=%q",
					dbBody.UpstreamRequestBody, dbBody.UpstreamResponseBody)
			}
		})
	}
}

// TestRelayAllCandidatesFailed: every candidate fails with 5xx; the gateway
// returns 502 and the log records the exhaustion.
func TestRelayAllCandidatesFailed(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer upstream.Close()

	svc := newRelaySvc(t, db)
	p := createProvider(t, db, "p1", upstream.URL)
	createProviderKey(t, db, svc.masterKey, p.ID, "sk-1", "k1", 1, true)
	m := createModelAndCandidate(t, db, p, "gpt-4o", "gpt-4o-real", true, true, 1)
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, []uint{m.ID})

	c, w := newCtx([]byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	svc.Handle(c, apiKey)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (all candidates failed)", w.Code)
	}
	var log model.RequestLog
	if err := db.First(&log).Error; err != nil {
		t.Fatalf("no request_log row: %v", err)
	}
	if log.StatusCode != http.StatusBadGateway {
		t.Errorf("log status_code = %d, want 502", log.StatusCode)
	}
	if log.FailReason == nil || *log.FailReason == "" {
		t.Error("expected non-empty fail_reason for an all-candidates-failed request")
	}
	// attempts_detail records every candidate try as JSON.
	if log.AttemptsDetail == nil {
		t.Error("expected attempts_detail to be populated")
	}
}

// TestRelayBudgetExceededReturnsInsufficientQuota: budget exhaustion maps
// to OpenAI's insufficient_quota type (distinct from rate_limit_error), and
// never reaches the upstream.
func TestRelayBudgetExceededReturnsInsufficientQuota(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	upstreamHit := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHit = true
	}))
	defer upstream.Close()

	svc := newRelaySvc(t, db)
	p := createProvider(t, db, "p1", upstream.URL)
	createProviderKey(t, db, svc.masterKey, p.ID, "sk-1", "k1", 1, true)
	m := createModelAndCandidate(t, db, p, "gpt-4o", "gpt-4o-real", true, true, 1)

	// Key whose budget is already fully spent (>= limit).
	now := time.Now().UTC()
	limit := int64(100)
	apiKey := &model.APIKey{
		KeyHash: ycrypto.HashToken("sk-yr-test"), KeyPrefix: "sk-yr-test------",
		OwnerLabel: "tester", Status: model.APIKeyStatusActive,
		BudgetLimitMicros: &limit, BudgetSpentMicros: 100,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(apiKey).Error; err != nil {
		t.Fatalf("seed api key: %v", err)
	}
	if err := db.Create(&model.APIKeyModel{APIKeyID: apiKey.ID, ModelID: m.ID, CreatedAt: now}).Error; err != nil {
		t.Fatalf("seed allowlist: %v", err)
	}

	c, w := newCtx([]byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	svc.Handle(c, apiKey)

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want 429 (budget exceeded)", w.Code)
	}
	if !bytes.Contains(w.Body.Bytes(), []byte(`"insufficient_quota"`)) {
		t.Errorf("expected insufficient_quota error type, got %s", w.Body.String())
	}
	if upstreamHit {
		t.Error("upstream must not be called when the key's budget is exhausted")
	}
}

// TestRelayStreamSuccess: a healthy streaming upstream forwards SSE chunks
// with the model field rewritten to the external name in EVERY chunk
// (provider name never leaks), terminates with [DONE], and records the
// final-usage tokens for cost (cost_known=true). Covers
// dispatchPassthroughStream + passthroughStreamToClient end-to-end.
func TestRelayStreamSuccess(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		write := func(s string) {
			_, _ = w.Write([]byte(s))
			if flusher != nil {
				flusher.Flush()
			}
		}
		write("data: {\"model\":\"gpt-4o-real\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		write("data: {\"model\":\"gpt-4o-real\",\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2,\"total_tokens\":5}}\n\n")
		write("data: [DONE]\n\n")
	}))
	defer upstream.Close()

	svc := newRelaySvc(t, db)
	p := createProvider(t, db, "p1", upstream.URL)
	createProviderKey(t, db, svc.masterKey, p.ID, "sk-1", "k1", 1, true)
	m := createModelAndCandidate(t, db, p, "gpt-4o", "gpt-4o-real", true, true, 1)
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, []uint{m.ID})

	c, w := newCtx([]byte(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	svc.Handle(c, apiKey)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	body := w.Body.Bytes()
	if bytes.Contains(body, []byte("gpt-4o-real")) {
		t.Errorf("upstream provider model name leaked into the stream: %s", body)
	}
	if !bytes.Contains(body, []byte(`"model":"gpt-4o"`)) {
		t.Errorf("external model name not present in every chunk: %s", body)
	}
	if !bytes.Contains(body, []byte("data: [DONE]")) {
		t.Errorf("[DONE] terminator not forwarded: %s", body)
	}
	var log model.RequestLog
	if err := db.First(&log).Error; err != nil {
		t.Fatalf("no request_log row: %v", err)
	}
	if log.InputTokens != 3 || log.OutputTokens != 2 {
		t.Errorf("stream usage not recorded: input=%d output=%d", log.InputTokens, log.OutputTokens)
	}
	if !log.CostKnown {
		t.Error("expected cost_known=true (usage was received from the final chunk)")
	}
}

// TestRelayClaudeMalformedBodyRejectedBeforeCandidateLoop: a /v1/messages
// request whose top-level shape passes ingressMeta.validate() (non-empty
// messages, positive max_tokens) but whose message content is structurally
// invalid (an object, not a string or content-block array) must be rejected
// by validateIngressBody as a 400 Claude error envelope BEFORE any candidate
// is ever tried — proving the pre-loop full-body validation (added in this
// task) actually gates the candidate loop, not just ingressMeta.validate().
// Without it, this malformed body would instead reach buildUpstreamBody
// (relay.go's decode step) once inside relayCandidates, fail there per
// candidate, and get misreported as a 502 "all upstream candidates failed"
// instead of the correct 400 client error.
func TestRelayClaudeMalformedBodyRejectedBeforeCandidateLoop(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	upstreamHit := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHit = true
	}))
	defer upstream.Close()

	svc := newRelaySvc(t, db)
	p := createProvider(t, db, "p1", upstream.URL)
	createProviderKey(t, db, svc.masterKey, p.ID, "sk-1", "k1", 1, true)
	m := createModelAndCandidate(t, db, p, "claude-3-5-sonnet", "claude-3-5-sonnet-real", true, true, 1)
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, []uint{m.ID})

	var captured *RelayContext
	testHookHandleDone = func(rc *RelayContext) { captured = rc }
	defer func() { testHookHandleDone = nil }()

	// messages is non-empty and max_tokens is positive (passes
	// ingressMeta.validate()), but content is an object -- neither a string
	// nor a content-block array -- which only the full claude.RequestDecoder
	// (validateIngressBody) rejects.
	body := []byte(`{"model":"claude-3-5-sonnet","max_tokens":1024,"messages":[{"role":"user","content":{"foo":"bar"}}]}`)
	c, w := newCtxPath("/v1/messages", body)
	svc.Handle(c, apiKey)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	if upstreamHit {
		t.Error("upstream must not be called for a body that fails the pre-loop structural validation")
	}

	var respBody map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &respBody); err != nil {
		t.Fatalf("response body did not unmarshal: %v (body=%s)", err, w.Body.String())
	}
	if respBody["type"] != "error" {
		t.Errorf(`top-level "type" = %v, want "error" (Anthropic-native envelope)`, respBody["type"])
	}
	if _, hasRequestID := respBody["request_id"]; !hasRequestID {
		t.Errorf("response body missing top-level request_id: %v", respBody)
	}
	errObj, ok := respBody["error"].(map[string]any)
	if !ok {
		t.Fatalf(`"error" field is not an object: %v`, respBody["error"])
	}
	if errObj["type"] != "invalid_request_error" {
		t.Errorf("error.type = %v, want invalid_request_error", errObj["type"])
	}

	if captured == nil {
		t.Fatal("testHookHandleDone was never invoked")
	}
	// The audit trail must record the Claude envelope actually sent, not the
	// OpenAI-shaped one.
	if !bytes.Contains(captured.ResponseBody, []byte(`"type":"error"`)) {
		t.Errorf("rc.ResponseBody = %s, want the Claude error envelope stashed for audit", captured.ResponseBody)
	}
	if len(captured.UpstreamRequestBody) != 0 || len(captured.UpstreamResponseBody) != 0 {
		t.Errorf("expected empty upstream_* (candidate loop never entered), got request=%q response=%q",
			captured.UpstreamRequestBody, captured.UpstreamResponseBody)
	}
}

// extractModelFromJSON pulls just the "model" string out of a JSON body for
// the failover test's assertion — keeping it inline avoids pulling
// encoding/json into the assertion line for what is a one-field check.
func extractModelFromJSON(t *testing.T, body []byte) string {
	t.Helper()
	var p struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &p); err != nil {
		t.Fatalf("parse upstream body: %v", err)
	}
	return p.Model
}

// TestGeminiIngressResolvesModelFromPath drives Handle with a native Gemini
// generateContent request against an openai-type provider (forcing the
// cross-protocol IR round trip: gemini.RequestDecoder -> IR ->
// chat.RequestEncoder, since the provider only supports openai on the
// wire). It pins the structural change this task adds: the external model
// name and the stream flag come from the URL path
// (/v1beta/models/{model}:generateContent), NOT the request body -- the
// gemini body here carries neither field.
func TestGeminiIngressResolvesModelFromPath(t *testing.T) {
	db := testutil.NewSQLiteDB(t)

	var sawUpstreamModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sawUpstreamModel = extractModelFromJSON(t, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-1","model":"gpt-4o-real","choices":[{"message":{"role":"assistant","content":"hi there"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`))
	}))
	defer upstream.Close()

	svc := newRelaySvc(t, db)
	p := createProvider(t, db, "openai-provider", upstream.URL)
	createProviderKey(t, db, svc.masterKey, p.ID, "sk-openai-upstream", "k1", 1, true)
	m := createModelAndCandidate(t, db, p, "gemini-2.0-flash", "gpt-4o-real", true, true, 1)
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, []uint{m.ID})

	// The body deliberately carries neither "model" nor "stream" -- Gemini's
	// wire format has no such top-level fields; both must come from the URL.
	reqBody := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	c, w := newCtxPath("/v1beta/models/gemini-2.0-flash:generateContent", reqBody)
	svc.Handle(c, apiKey)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	if sawUpstreamModel != "gpt-4o-real" {
		t.Errorf("upstream body model = %q, want the provider model name %q (external model %q resolved via the URL path)", sawUpstreamModel, "gpt-4o-real", "gemini-2.0-flash")
	}
}

// TestGeminiIngressStreamActionSetsStreamFromPath covers the
// :streamGenerateContent action -- the stream flag, like the model name,
// must come from the URL suffix, not from any body field.
func TestGeminiIngressStreamActionSetsStreamFromPath(t *testing.T) {
	db := testutil.NewSQLiteDB(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		flusher, _ := w.(http.Flusher)
		write := func(s string) {
			_, _ = w.Write([]byte(s))
			if flusher != nil {
				flusher.Flush()
			}
		}
		write(`data: {"id":"chatcmpl-2","model":"gpt-4o-real","choices":[{"delta":{"role":"assistant","content":"hi"}}]}` + "\n\n")
		write(`data: {"id":"chatcmpl-2","choices":[{"delta":{},"finish_reason":"stop"}]}` + "\n\n")
		write("data: [DONE]\n\n")
	}))
	defer upstream.Close()

	svc := newRelaySvc(t, db)
	p := createProvider(t, db, "openai-provider", upstream.URL)
	createProviderKey(t, db, svc.masterKey, p.ID, "sk-openai-upstream", "k1", 1, true)
	m := createModelAndCandidate(t, db, p, "gemini-2.0-flash", "gpt-4o-real", true, true, 1)
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, []uint{m.ID})

	var captured *RelayContext
	testHookHandleDone = func(rc *RelayContext) { captured = rc }
	defer func() { testHookHandleDone = nil }()

	reqBody := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	c, w := newCtxPath("/v1beta/models/gemini-2.0-flash:streamGenerateContent", reqBody)
	svc.Handle(c, apiKey)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	if captured == nil {
		t.Fatal("testHookHandleDone was never invoked")
	}
	if !captured.IsStream {
		t.Errorf("rc.IsStream = false, want true (from the :streamGenerateContent path suffix)")
	}
}

// TestGeminiIngressMalformedPathRejected covers an ingress path that
// IngressProtocol still routes to Gemini (the prefix + suffix match) but
// parseGeminiPath rejects for a different reason -- an empty model segment.
// Handle must return a 400 before ever peeking the body or touching a
// candidate, mirroring how a malformed body is rejected.
func TestGeminiIngressMalformedPathRejected(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	upstreamHit := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHit = true
	}))
	defer upstream.Close()

	svc := newRelaySvc(t, db)
	p := createProvider(t, db, "openai-provider", upstream.URL)
	createProviderKey(t, db, svc.masterKey, p.ID, "sk-openai-upstream", "k1", 1, true)
	m := createModelAndCandidate(t, db, p, "gemini-2.0-flash", "gpt-4o-real", true, true, 1)
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, []uint{m.ID})

	reqBody := []byte(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`)
	c, w := newCtxPath("/v1beta/models/:generateContent", reqBody)
	svc.Handle(c, apiKey)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	if upstreamHit {
		t.Error("upstream must not be called for a malformed Gemini ingress path")
	}
}

// TestResponsesIngressResolvesModelAndStreamFromBody covers the Responses
// ingress path against an openai-type provider (forcing the cross-protocol
// IR round trip). Unlike Gemini, Responses carries model and stream in the
// body itself, same as OpenAI Chat and Claude.
func TestResponsesIngressResolvesModelAndStreamFromBody(t *testing.T) {
	db := testutil.NewSQLiteDB(t)

	var sawUpstreamModel string
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		sawUpstreamModel = extractModelFromJSON(t, body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"chatcmpl-3","model":"gpt-4o-real","choices":[{"message":{"role":"assistant","content":"hi there"},"finish_reason":"stop"}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`))
	}))
	defer upstream.Close()

	svc := newRelaySvc(t, db)
	p := createProvider(t, db, "openai-provider", upstream.URL)
	createProviderKey(t, db, svc.masterKey, p.ID, "sk-openai-upstream", "k1", 1, true)
	m := createModelAndCandidate(t, db, p, "responses-model", "gpt-4o-real", true, true, 1)
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, []uint{m.ID})

	reqBody := []byte(`{"model":"responses-model","input":"hi"}`)
	c, w := newCtxPath("/v1/responses", reqBody)
	svc.Handle(c, apiKey)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	if sawUpstreamModel != "gpt-4o-real" {
		t.Errorf("upstream body model = %q, want the provider model name %q (external model %q resolved from the body)", sawUpstreamModel, "gpt-4o-real", "responses-model")
	}
}

// TestResponsesIngressMissingInputRejected covers the responses-specific
// top-level invariant peekResponsesIngress/validate() enforce that the
// responses.RequestDecoder itself is lenient about: a missing "input" field
// must be rejected as a 400 before any candidate is picked.
func TestResponsesIngressMissingInputRejected(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	upstreamHit := false
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamHit = true
	}))
	defer upstream.Close()

	svc := newRelaySvc(t, db)
	p := createProvider(t, db, "openai-provider", upstream.URL)
	createProviderKey(t, db, svc.masterKey, p.ID, "sk-openai-upstream", "k1", 1, true)
	m := createModelAndCandidate(t, db, p, "responses-model", "gpt-4o-real", true, true, 1)
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, []uint{m.ID})

	reqBody := []byte(`{"model":"responses-model"}`)
	c, w := newCtxPath("/v1/responses", reqBody)
	svc.Handle(c, apiKey)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400; body = %s", w.Code, w.Body.String())
	}
	if upstreamHit {
		t.Error("upstream must not be called for a responses request missing input")
	}
}

// --- Input compression tests -------------------------------------------------

// buildOutputContent returns a >512-byte string of go-test-style output that
// the compress engine's GoTest/Log compressors will shrink (drops === RUN and
// --- PASS: boilerplate lines, retaining only the summary tail).
func buildOutputContent() string {
	return strings.Repeat("=== RUN   TestFoo\n--- PASS: TestFoo (0.00s)\n", 30) + "PASS\nok  \tpkg\t0.1s\n"
}

// jsonStr JSON-encodes s as a string literal (including surrounding quotes),
// safe to concatenate into a JSON body template.
func jsonStr(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

// TestCompressTriggersAcrossProtocols: for each of the four ingress protocols,
// compression enabled globally shrinks a build-output body before relay.
// The compressed body reaches the upstream (captured via the RelayContext)
// and is strictly shorter than the caller's original.
func TestCompressTriggersAcrossProtocols(t *testing.T) {
	cases := []struct {
		name       string
		path       string
		externalNm string
		providerMn string
		body       func() []byte
	}{
		{
			name:       "openai_chat",
			path:       "/v1/chat/completions",
			externalNm: "gpt-4o",
			providerMn: "gpt-4o-real",
			body: func() []byte {
				return []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":` + jsonStr(buildOutputContent()) + `}]}`)
			},
		},
		{
			name:       "claude",
			path:       "/v1/messages",
			externalNm: "claude-3-5-sonnet",
			providerMn: "claude-3-5-sonnet-real",
			body: func() []byte {
				return []byte(`{"model":"claude-3-5-sonnet","max_tokens":1024,"messages":[{"role":"user","content":[{"type":"tool_result","content":` + jsonStr(buildOutputContent()) + `}]}]}`)
			},
		},
		{
			name:       "responses",
			path:       "/v1/responses",
			externalNm: "responses-model",
			providerMn: "gpt-4o-real",
			body: func() []byte {
				return []byte(`{"model":"responses-model","input":[{"role":"user","content":[{"type":"input_text","text":` + jsonStr(buildOutputContent()) + `}]}]}`)
			},
		},
		{
			name:       "gemini",
			path:       "/v1beta/models/gemini-2.0-flash:generateContent",
			externalNm: "gemini-2.0-flash",
			providerMn: "gpt-4o-real",
			body: func() []byte {
				return []byte(`{"contents":[{"role":"user","parts":[{"text":` + jsonStr(buildOutputContent()) + `}]}]}`)
			},
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := testutil.NewSQLiteDB(t)
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`{"model":"` + tc.providerMn + `","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`))
			}))
			defer upstream.Close()

			sp := stubSettingsProvider{compressEnabled: true}
			svc := newRelaySvcWithSettings(t, db, sp)
			p := createProvider(t, db, "p1", upstream.URL)
			createProviderKey(t, db, svc.masterKey, p.ID, "sk-1", "k1", 1, true)
			m := createModelAndCandidate(t, db, p, tc.externalNm, tc.providerMn, true, true, 1)
			apiKey := createAPIKey(t, db, model.APIKeyStatusActive, []uint{m.ID})

			var captured *RelayContext
			testHookHandleDone = func(rc *RelayContext) { captured = rc }
			defer func() { testHookHandleDone = nil }()

			origBody := tc.body()
			c, w := newCtxPath(tc.path, origBody)
			svc.Handle(c, apiKey)

			if w.Code != http.StatusOK {
				t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
			}
			if captured == nil {
				t.Fatal("testHookHandleDone was never invoked")
			}
			if !captured.CompressEnabled {
				t.Error("rc.CompressEnabled = false, want true")
			}
			if captured.RequestBodyCompressed == nil {
				t.Fatal("rc.RequestBodyCompressed is nil, compression did not produce a body")
			}
			if len(captured.RequestBodyCompressed) >= len(origBody) {
				t.Errorf("compressed body (%d bytes) is not shorter than original (%d bytes)",
					len(captured.RequestBodyCompressed), len(origBody))
			}
			if captured.CompressEstimatedTokensSaved <= 0 {
				t.Error("CompressEstimatedTokensSaved should be positive")
			}
			if len(captured.CompressorsApplied) == 0 {
				t.Error("CompressorsApplied should not be empty")
			}
			if captured.CompressSkipReason != "" {
				t.Errorf("CompressSkipReason = %q, want empty (compression applied)", captured.CompressSkipReason)
			}
		})
	}
}

// TestCompressSkipsNoLiveZone: compression is enabled but the body has no
// live-zone blocks (last message is assistant — no user/tool text after it).
// The engine returns Skipped=NoLiveZone, rc.RequestBodyCompressed stays nil,
// and the request proceeds with the original body.
func TestCompressSkipsNoLiveZone(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-4o-real","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`))
	}))
	defer upstream.Close()

	sp := stubSettingsProvider{compressEnabled: true}
	svc := newRelaySvcWithSettings(t, db, sp)
	p := createProvider(t, db, "p1", upstream.URL)
	createProviderKey(t, db, svc.masterKey, p.ID, "sk-1", "k1", 1, true)
	m := createModelAndCandidate(t, db, p, "gpt-4o", "gpt-4o-real", true, true, 1)
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, []uint{m.ID})

	var captured *RelayContext
	testHookHandleDone = func(rc *RelayContext) { captured = rc }
	defer func() { testHookHandleDone = nil }()

	// Last message is assistant — no user/tool content follows, so locate
	// returns zero live blocks.
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"},{"role":"assistant","content":"ok"}]}`)
	c, w := newCtx(body)
	svc.Handle(c, apiKey)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	if captured == nil {
		t.Fatal("testHookHandleDone was never invoked")
	}
	if captured.RequestBodyCompressed != nil {
		t.Error("RequestBodyCompressed should be nil when the engine skips")
	}
	if captured.CompressSkipReason != "no_live_zone" {
		t.Errorf("CompressSkipReason = %q, want %q", captured.CompressSkipReason, "no_live_zone")
	}
}

// TestCompressDisabledBySwitch: CompressEnabled=false leaves the body
// untouched — no compression attempt, no skip reason recorded.
func TestCompressDisabledBySwitch(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-4o-real","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`))
	}))
	defer upstream.Close()

	// Global switch off.
	sp := stubSettingsProvider{compressEnabled: false}
	svc := newRelaySvcWithSettings(t, db, sp)
	p := createProvider(t, db, "p1", upstream.URL)
	createProviderKey(t, db, svc.masterKey, p.ID, "sk-1", "k1", 1, true)
	m := createModelAndCandidate(t, db, p, "gpt-4o", "gpt-4o-real", true, true, 1)
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, []uint{m.ID})

	var captured *RelayContext
	testHookHandleDone = func(rc *RelayContext) { captured = rc }
	defer func() { testHookHandleDone = nil }()

	origBody := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":` + jsonStr(buildOutputContent()) + `}]}`)
	c, w := newCtx(origBody)
	svc.Handle(c, apiKey)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	if captured == nil {
		t.Fatal("testHookHandleDone was never invoked")
	}
	if captured.CompressEnabled {
		t.Error("rc.CompressEnabled should be false")
	}
	if captured.RequestBodyCompressed != nil {
		t.Error("RequestBodyCompressed should be nil when compression is off")
	}
	if captured.CompressSkipReason != "" {
		t.Errorf("CompressSkipReason = %q, want empty (compression never attempted)", captured.CompressSkipReason)
	}
}

// TestCompressNonChatEndpointNotCompressed: a path whose IsChatEndpoint is
// false (Gemini countTokens) is not compressed even when CompressEnabled is
// true. rc.RequestBodyCompressed stays nil and no skip reason is recorded
// (the gate was never entered).
func TestCompressNonChatEndpointNotCompressed(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-4o-real","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`))
	}))
	defer upstream.Close()

	sp := stubSettingsProvider{compressEnabled: true}
	svc := newRelaySvcWithSettings(t, db, sp)
	p := createProvider(t, db, "p1", upstream.URL)
	createProviderKey(t, db, svc.masterKey, p.ID, "sk-1", "k1", 1, true)
	m := createModelAndCandidate(t, db, p, "gpt-4o", "gpt-4o-real", true, true, 1)
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, []uint{m.ID})

	var captured *RelayContext
	testHookHandleDone = func(rc *RelayContext) { captured = rc }
	defer func() { testHookHandleDone = nil }()

	// countTokens is under the Gemini prefix but is NOT a generateContent/
	// streamGenerateContent action — IsChatEndpoint returns false, and the
	// compression gate is skipped entirely.
	origBody := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":` + jsonStr(buildOutputContent()) + `}]}`)
	c, w := newCtxPath("/v1beta/models/gemini-2.0-flash:countTokens", origBody)
	svc.Handle(c, apiKey)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	if captured == nil {
		t.Fatal("testHookHandleDone was never invoked")
	}
	if !captured.CompressEnabled {
		t.Error("rc.CompressEnabled should be true (global switch on)")
	}
	if captured.RequestBodyCompressed != nil {
		t.Error("RequestBodyCompressed should be nil — non-chat endpoints are not compressed")
	}
	if captured.CompressSkipReason != "" {
		t.Errorf("CompressSkipReason = %q, want empty (gate never entered)", captured.CompressSkipReason)
	}
}

// TestCompressFailOpenProceeds: compression enabled, engine returns Skipped
// (no live zone in a body whose only user content is a short string with no
// compressible anchors). The request must still succeed and the original
// body is what reaches upstream.
func TestCompressFailOpenProceeds(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	var upstreamBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-4o-real","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`))
	}))
	defer upstream.Close()

	sp := stubSettingsProvider{compressEnabled: true}
	svc := newRelaySvcWithSettings(t, db, sp)
	p := createProvider(t, db, "p1", upstream.URL)
	createProviderKey(t, db, svc.masterKey, p.ID, "sk-1", "k1", 1, true)
	m := createModelAndCandidate(t, db, p, "gpt-4o", "gpt-4o-real", true, true, 1)
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, []uint{m.ID})

	var captured *RelayContext
	testHookHandleDone = func(rc *RelayContext) { captured = rc }
	defer func() { testHookHandleDone = nil }()

	// Short prose content with no build-output/diff/grep anchors: the engine
	// detects PlainText, compressorsFor returns nil, and the pass surfaces
	// NoMatchingCompressor (skipped). The request must still proceed.
	origBody := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":` + jsonStr(strings.Repeat("hello world ", 60)) + `}]}`)
	c, w := newCtx(origBody)
	svc.Handle(c, apiKey)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	if captured == nil {
		t.Fatal("testHookHandleDone was never invoked")
	}
	if captured.RequestBodyCompressed != nil {
		t.Error("RequestBodyCompressed should be nil when the engine skips")
	}
	if captured.CompressSkipReason == "" {
		t.Error("CompressSkipReason should record why the engine skipped")
	}
	// The original body content reaches upstream unchanged.
	if !bytes.Contains(upstreamBody, []byte("hello world")) {
		t.Errorf("upstream body should contain the original (uncompressed) content: %s", upstreamBody)
	}
}

// TestCompressOverrideShortCircuitsGlobal: a per-key override (enabled=false)
// wins over a globally-enabled switch. rc.CompressEnabled must be false and
// no compression attempt is made.
func TestCompressOverrideShortCircuitsGlobal(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-4o-real","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`))
	}))
	defer upstream.Close()

	// Global switch ON, but per-key override says OFF.
	sp := stubSettingsProvider{compressEnabled: true}
	svc := newRelaySvcWithSettings(t, db, sp)
	p := createProvider(t, db, "p1", upstream.URL)
	createProviderKey(t, db, svc.masterKey, p.ID, "sk-1", "k1", 1, true)
	m := createModelAndCandidate(t, db, p, "gpt-4o", "gpt-4o-real", true, true, 1)
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, []uint{m.ID})
	apiKey.CompressEnabledOverride = true
	apiKey.CompressEnabled = false

	var captured *RelayContext
	testHookHandleDone = func(rc *RelayContext) { captured = rc }
	defer func() { testHookHandleDone = nil }()

	origBody := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":` + jsonStr(buildOutputContent()) + `}]}`)
	c, w := newCtx(origBody)
	svc.Handle(c, apiKey)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	if captured == nil {
		t.Fatal("testHookHandleDone was never invoked")
	}
	if captured.CompressEnabled {
		t.Error("rc.CompressEnabled should be false (per-key override wins)")
	}
	if captured.RequestBodyCompressed != nil {
		t.Error("RequestBodyCompressed should be nil when override disables compression")
	}
}

// TestCompressOverrideEnablesWhenGlobalOff: the reverse — per-key override
// (enabled=true) takes effect when the global switch is off.
func TestCompressOverrideEnablesWhenGlobalOff(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-4o-real","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`))
	}))
	defer upstream.Close()

	// Global OFF, per-key override ON.
	sp := stubSettingsProvider{compressEnabled: false}
	svc := newRelaySvcWithSettings(t, db, sp)
	p := createProvider(t, db, "p1", upstream.URL)
	createProviderKey(t, db, svc.masterKey, p.ID, "sk-1", "k1", 1, true)
	m := createModelAndCandidate(t, db, p, "gpt-4o", "gpt-4o-real", true, true, 1)
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, []uint{m.ID})
	apiKey.CompressEnabledOverride = true
	apiKey.CompressEnabled = true

	var captured *RelayContext
	testHookHandleDone = func(rc *RelayContext) { captured = rc }
	defer func() { testHookHandleDone = nil }()

	origBody := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":` + jsonStr(buildOutputContent()) + `}]}`)
	c, w := newCtx(origBody)
	svc.Handle(c, apiKey)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	if captured == nil {
		t.Fatal("testHookHandleDone was never invoked")
	}
	if !captured.CompressEnabled {
		t.Error("rc.CompressEnabled should be true (per-key override enables)")
	}
	if captured.RequestBodyCompressed == nil {
		t.Error("RequestBodyCompressed should be non-nil (override enabled compression)")
	}
}

// TestCompressGlobalFailOpenOnError: when the settings read returns an error,
// the gateway leaves compression disabled (fail-open) and the request
// proceeds normally.
func TestCompressGlobalFailOpenOnError(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-4o-real","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`))
	}))
	defer upstream.Close()

	sp := stubSettingsProvider{compressErr: errors.New("settings db unavailable")}
	svc := newRelaySvcWithSettings(t, db, sp)
	p := createProvider(t, db, "p1", upstream.URL)
	createProviderKey(t, db, svc.masterKey, p.ID, "sk-1", "k1", 1, true)
	m := createModelAndCandidate(t, db, p, "gpt-4o", "gpt-4o-real", true, true, 1)
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, []uint{m.ID})

	var captured *RelayContext
	testHookHandleDone = func(rc *RelayContext) { captured = rc }
	defer func() { testHookHandleDone = nil }()

	origBody := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":` + jsonStr(buildOutputContent()) + `}]}`)
	c, w := newCtx(origBody)
	svc.Handle(c, apiKey)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	if captured == nil {
		t.Fatal("testHookHandleDone was never invoked")
	}
	if captured.CompressEnabled {
		t.Error("rc.CompressEnabled should be false (fail-open on settings error)")
	}
	if captured.RequestBodyCompressed != nil {
		t.Error("RequestBodyCompressed should be nil when settings read fails")
	}
}

// TestCompressAndCSPCoexist: compression and custom system prompt are both
// enabled. Both apply: the compressed body reaches upstream AND the injected
// system prompt is present in the upstream body. The two are orthogonal —
// compress mutates user/tool text, CSP appends to the system field.
func TestCompressAndCSPCoexist(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	var upstreamBody []byte
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		upstreamBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"gpt-4o-real","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`))
	}))
	defer upstream.Close()

	// Both globally enabled.
	sp := stubSettingsProvider{
		val: settings.CustomSystemPromptSetting{
			Enabled: true,
			Text:    "BE CONCISE AND HELPFUL",
		},
		compressEnabled: true,
	}
	svc := newRelaySvcWithSettings(t, db, sp)
	p := createProvider(t, db, "p1", upstream.URL)
	createProviderKey(t, db, svc.masterKey, p.ID, "sk-1", "k1", 1, true)
	m := createModelAndCandidate(t, db, p, "gpt-4o", "gpt-4o-real", true, true, 1)
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, []uint{m.ID})

	var captured *RelayContext
	testHookHandleDone = func(rc *RelayContext) { captured = rc }
	defer func() { testHookHandleDone = nil }()

	origBody := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":` + jsonStr(buildOutputContent()) + `}]}`)
	c, w := newCtx(origBody)
	svc.Handle(c, apiKey)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body = %s", w.Code, w.Body.String())
	}
	if captured == nil {
		t.Fatal("testHookHandleDone was never invoked")
	}
	// Compression applied.
	if captured.RequestBodyCompressed == nil {
		t.Fatal("RequestBodyCompressed should be non-nil (compression enabled and ran)")
	}
	if len(captured.RequestBodyCompressed) >= len(origBody) {
		t.Errorf("compressed body (%d) should be shorter than original (%d)",
			len(captured.RequestBodyCompressed), len(origBody))
	}
	// CSP resolved.
	if !captured.CustomSystemPromptEnabled {
		t.Error("CustomSystemPromptEnabled should be true")
	}
	// The upstream body must carry BOTH the injected system prompt AND the
	// compressed content (=== RUN lines dropped).
	if !bytes.Contains(upstreamBody, []byte("BE CONCISE AND HELPFUL")) {
		t.Errorf("upstream body missing the injected system prompt: %s", upstreamBody)
	}
	if bytes.Contains(upstreamBody, []byte("=== RUN")) {
		t.Errorf("upstream body still contains === RUN lines (compression did not shrink the content): %s", upstreamBody)
	}
}

// TestAttemptOne_StreamBodyIdleIsTerminal pins the per-attempt idle-timeout
// behavior for streaming: when an upstream emits one SSE chunk and then goes
// silent longer than BodyIdleTimeout, the IdleReadCloser wrapping the response
// body cuts the read, the stream pump surfaces a mid-stream error, and the
// gateway finalizes the attempt as stream_partial terminal (no failover once
// the first byte has been written). The test pins three things at once: the
// first chunk IS delivered to the caller, the attempt's failure reason
// carries the idle-timeout signal (not the legacy "stream ended without
// [DONE]" / 120s absolute-wall behaviour), and no failover is attempted.
//
// Timeout budget: body_idle=100ms, upstream stall=500ms (5x the idle window,
// so the idle timer deterministically fires before the handler's body write).
// The whole test runs in well under one second.
func TestAttemptOne_StreamBodyIdleIsTerminal(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		if flusher != nil {
			flusher.Flush()
		}
		_, _ = w.Write([]byte("data: {\"model\":\"gpt-4o-real\",\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n"))
		if flusher != nil {
			flusher.Flush()
		}
		// Stall much longer than BodyIdleTimeout so the idle timer fires
		// before any further bytes could arrive. The handler then returns,
		// closing the connection.
		time.Sleep(500 * time.Millisecond)
	}))
	defer upstream.Close()

	gw := testGatewayConfig()
	gw.BodyIdleTimeout = 100 * time.Millisecond
	gw.AttemptTimeout = 2 * time.Second
	gw.RequestTimeout = 5 * time.Second
	svc := newRelaySvcWithGateway(t, db, gw)
	p := createProvider(t, db, "p1", upstream.URL)
	createProviderKey(t, db, svc.masterKey, p.ID, "sk-1", "k1", 1, true)
	m := createModelAndCandidate(t, db, p, "gpt-4o", "gpt-4o-real", true, true, 1)
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, []uint{m.ID})

	var captured *RelayContext
	testHookHandleDone = func(rc *RelayContext) { captured = rc }
	defer func() { testHookHandleDone = nil }()

	c, w := newCtx([]byte(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`))
	svc.Handle(c, apiKey)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (mid-stream truncation keeps the already-sent 200); body = %s", w.Code, w.Body.String())
	}
	if captured == nil {
		t.Fatal("testHookHandleDone was never invoked")
	}
	// The first chunk was forwarded before the idle timer fired.
	if !strings.Contains(w.Body.String(), "hi") {
		t.Errorf("response body missing the first streamed chunk: %s", w.Body.String())
	}
	// Exactly one attempt — no failover after the first byte went out.
	if len(captured.Attempts) != 1 {
		t.Fatalf("expected exactly 1 attempt (no failover on a mid-stream cut), got %d: %+v", len(captured.Attempts), captured.Attempts)
	}
	last := captured.Attempts[0]
	if last.Outcome != AttemptServerError {
		t.Errorf("attempt outcome = %q, want %q", last.Outcome, AttemptServerError)
	}
	if !strings.Contains(last.FailReason, "idle") {
		t.Errorf("attempt FailReason = %q, want it to contain \"idle\" (the inter-chunk idle timeout)", last.FailReason)
	}
	// The finalize reason on the persisted log row carries the same signal.
	var log model.RequestLog
	if err := db.First(&log).Error; err != nil {
		t.Fatalf("no request_log row: %v", err)
	}
	if log.FailReason == nil || !strings.Contains(*log.FailReason, "stream_partial") {
		t.Errorf("log fail_reason = %v, want it to contain \"stream_partial\"", log.FailReason)
	}
}

// TestAttemptOne_NonStreamBodyIdleFailovers pins the per-attempt first-byte
// timeout behavior for non-streaming: when an upstream returns 200 headers but
// then stalls sending the first body chunk longer than FirstByteTimeout, the
// IdleReadCloser wrapping the response body cuts the read during io.ReadAll,
// the dispatch path records a read-body failure for this candidate, and the
// gateway fails over to the next candidate. The test pins: candidate A's body
// read is cut by the first-byte timeout, candidate B is tried next and
// succeeds, and the final caller-facing response carries candidate B's content.
//
// Timeout budget: first_byte=100ms, upstream A body delay=500ms (5x the
// first-byte window, so the firstByte timer deterministically fires before the
// body write).
func TestAttemptOne_NonStreamBodyIdleFailovers(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	var (
		callsMu sync.Mutex
		calls   []string
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		requested := extractModelFromJSON(t, body)
		callsMu.Lock()
		calls = append(calls, requested)
		callsMu.Unlock()
		flusher, _ := w.(http.Flusher)
		if requested == "c1-model" {
			// Candidate A: commit 200 headers immediately, then stall the
			// body well past FirstByteTimeout so the IdleReadCloser fires
			// during the dispatch layer's io.ReadAll.
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			if flusher != nil {
				flusher.Flush()
			}
			time.Sleep(500 * time.Millisecond)
			_, _ = w.Write([]byte(`{"model":"c1-model","choices":[{"message":{"role":"assistant","content":"A"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
			return
		}
		// Candidate B: fast, clean success.
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"c2-model","choices":[{"message":{"role":"assistant","content":"B-ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	gw := testGatewayConfig()
	gw.FirstByteTimeout = 100 * time.Millisecond
	gw.BodyIdleTimeout = 100 * time.Millisecond
	gw.AttemptTimeout = 2 * time.Second
	gw.RequestTimeout = 5 * time.Second
	svc := newRelaySvcWithGateway(t, db, gw)
	p1 := createProvider(t, db, "p1", upstream.URL)
	createProviderKey(t, db, svc.masterKey, p1.ID, "sk-1", "k1", 1, true)
	p2 := createProvider(t, db, "p2", upstream.URL)
	createProviderKey(t, db, svc.masterKey, p2.ID, "sk-2", "k2", 1, true)

	// Two candidates backing the same external model, distinguished by their
	// provider_model_name so the test upstream can route each one.
	now := time.Now().UTC()
	m := &model.Model{Name: "gpt-4o", ManagementStatus: model.ModelStatusEnabled, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(m).Error; err != nil {
		t.Fatalf("seed model: %v", err)
	}
	for i, p := range []*model.Provider{p1, p2} {
		name := "c1-model"
		if i == 1 {
			name = "c2-model"
		}
		if err := db.Create(&model.ModelCandidate{
			ModelID: m.ID, ProviderID: p.ID, ProviderModelName: name,
			InputPrice: 0, OutputPrice: 0, MaxOutput: 4096,
			SupportsStreaming: true, SupportsFunctionCalling: true,
			ManagementStatus: model.ModelCandidateStatusEnabled, SortOrder: i + 1,
			VerificationStatus: model.ModelVerificationStatusPassed,
			CreatedAt:          now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatalf("seed candidate %d: %v", i, err)
		}
	}
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, []uint{m.ID})

	var captured *RelayContext
	testHookHandleDone = func(rc *RelayContext) { captured = rc }
	defer func() { testHookHandleDone = nil }()

	c, w := newCtx([]byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	svc.Handle(c, apiKey)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (failover to candidate B); body = %s", w.Code, w.Body.String())
	}
	if captured == nil {
		t.Fatal("testHookHandleDone was never invoked")
	}
	callsMu.Lock()
	gotCalls := append([]string(nil), calls...)
	callsMu.Unlock()
	// Two upstream calls: A then B.
	if len(gotCalls) != 2 || gotCalls[0] != "c1-model" || gotCalls[1] != "c2-model" {
		t.Fatalf("expected upstream calls [c1-model, c2-model], got %v", gotCalls)
	}
	// The caller-facing response is candidate B's, rewritten to the external name.
	if !bytes.Contains(w.Body.Bytes(), []byte(`"model":"gpt-4o"`)) {
		t.Errorf("response body missing external model name (candidate B should win): %s", w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("B-ok")) {
		t.Errorf("response body missing candidate B content: %s", w.Body.String())
	}
	// Two attempts recorded: A failed with a first-byte-timeout read-body error, B succeeded.
	if len(captured.Attempts) != 2 {
		t.Fatalf("expected 2 attempts (A failed, B succeeded), got %d: %+v", len(captured.Attempts), captured.Attempts)
	}
	if captured.Attempts[0].Outcome != AttemptBadStatus {
		t.Errorf("attempt A outcome = %q, want %q (read-body failover)", captured.Attempts[0].Outcome, AttemptBadStatus)
	}
	// The firstByte/inter-chunk timeout error messages both contain "timeout"
	// ("first byte timeout" / "idle timeout between chunks"); either is a
	// legitimate read-body cut. The assertion accepts both so the test stays
	// valid regardless of which phase fired.
	if !strings.Contains(captured.Attempts[0].FailReason, "timeout") {
		t.Errorf("attempt A FailReason = %q, want it to contain \"timeout\" (first-byte or inter-chunk)", captured.Attempts[0].FailReason)
	}
	if captured.Attempts[1].Outcome != AttemptSuccess {
		t.Errorf("attempt B outcome = %q, want %q", captured.Attempts[1].Outcome, AttemptSuccess)
	}
}

// TestRelayCandidateLoopRespectsRequestBudget verifies that the candidate
// walk stops as soon as the per-request total budget (RequestDeadline) has
// been exhausted. Before the per-iteration budget gate was added,
// relayCandidates kept walking the chain after the budget had elapsed -
// every subsequent candidate entered attemptOne just to be rejected by its
// own remaining-check, burning wall-clock on a chain that could never
// succeed. With the gate, the second candidate is never reached once the
// first attempt consumes the entire budget.
func TestRelayCandidateLoopRespectsRequestBudget(t *testing.T) {
	db := testutil.NewSQLiteDB(t)

	// Slow upstream for candidate 1: sleeps well past RequestTimeout so the
	// first attempt's context (capped at the remaining request budget) elapses
	// before the handler returns.
	var slowHit atomic.Bool
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		slowHit.Store(true)
		time.Sleep(2 * time.Second)
		w.WriteHeader(http.StatusBadGateway)
	}))
	defer slow.Close()

	// Fast upstream for candidate 2: should never be reached once the budget
	// gate stops the walk.
	var fastHit atomic.Bool
	fast := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fastHit.Store(true)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"c2","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer fast.Close()

	// Short RequestTimeout (150ms) + long AttemptTimeout (30s) so the first
	// attempt is bounded by the request budget, not the per-attempt budget -
	// exercising the candidate-loop gate rather than attemptOne's own check.
	svc := newRelaySvcWithGateway(t, db, config.GatewayConfig{
		ConnectTimeout:   5 * time.Second,
		HeaderTimeout:    600 * time.Second,
		FirstByteTimeout: 600 * time.Second,
		BodyIdleTimeout:  60 * time.Second,
		AttemptTimeout:   30 * time.Second,
		RequestTimeout:   150 * time.Millisecond,
	})
	p1 := createProvider(t, db, "slow", slow.URL)
	createProviderKey(t, db, svc.masterKey, p1.ID, "sk-1", "k1", 1, true)
	p2 := createProvider(t, db, "fast", fast.URL)
	createProviderKey(t, db, svc.masterKey, p2.ID, "sk-2", "k2", 1, true)

	now := time.Now().UTC()
	m := &model.Model{Name: "gpt-4o", ManagementStatus: model.ModelStatusEnabled, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(m).Error; err != nil {
		t.Fatalf("seed model: %v", err)
	}
	for i, p := range []*model.Provider{p1, p2} {
		if err := db.Create(&model.ModelCandidate{
			ModelID: m.ID, ProviderID: p.ID, ProviderModelName: "c" + string(rune('1'+i)),
			InputPrice: 0, OutputPrice: 0, MaxOutput: 4096,
			SupportsStreaming: true, SupportsFunctionCalling: true,
			ManagementStatus: model.ModelCandidateStatusEnabled, SortOrder: i + 1,
			VerificationStatus: model.ModelVerificationStatusPassed,
			CreatedAt:          now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatalf("seed candidate %d: %v", i, err)
		}
	}
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, []uint{m.ID})

	var captured *RelayContext
	testHookHandleDone = func(rc *RelayContext) { captured = rc }
	defer func() { testHookHandleDone = nil }()

	c, w := newCtx([]byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	svc.Handle(c, apiKey)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502 (all candidates failed after budget exhausted); body = %s", w.Code, w.Body.String())
	}
	if !slowHit.Load() {
		t.Error("slow upstream was never called - candidate 1 should have been attempted")
	}
	if fastHit.Load() {
		t.Error("fast upstream was called - candidate 2 must NOT be reached once the request budget elapsed")
	}
	if captured == nil {
		t.Fatal("testHookHandleDone was never invoked")
	}
	// Exactly one attempt recorded: candidate 1 timed out consuming the entire
	// request budget, so the candidate-loop gate stopped the walk before
	// candidate 2 could register its own budget-exhausted attempt.
	if len(captured.Attempts) != 1 {
		t.Errorf("expected exactly 1 attempt (budget gate stops the walk), got %d: %+v", len(captured.Attempts), captured.Attempts)
	}
}
