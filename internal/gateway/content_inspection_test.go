package gateway

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/yolorouter/yolorouter/internal/config"
	"github.com/yolorouter/yolorouter/internal/model"
	"github.com/yolorouter/yolorouter/internal/testutil"

	"gorm.io/gorm"
)

func TestIsContentInspectionRejection(t *testing.T) {
	cases := []struct {
		name   string
		status int
		body   string
		want   bool
	}{
		{
			name:   "vendor code data_inspection_failed",
			status: 400,
			body:   `{"error":{"message":"Input data may contain inappropriate content.","type":"invalid_request_error","code":"data_inspection_failed"}}`,
			want:   true,
		},
		{
			name:   "camelcase spelling of the same code",
			status: 400,
			body:   `{"code":"DataInspectionFailed","message":"Input data may contain inappropriate content."}`,
			want:   true,
		},
		{
			name:   "content_filter with nested filter result",
			status: 400,
			body:   `{"error":{"code":"content_filter","innererror":{"content_filter_result":{"hate":{"filtered":true}}}}}`,
			want:   true,
		},
		{
			name:   "content_policy_violation",
			status: 400,
			body:   `{"error":{"code":"content_policy_violation","message":"Your request was rejected as a result of our safety system."}}`,
			want:   true,
		},
		{
			name:   "sensitive content detected",
			status: 400,
			body:   `{"error":{"code":"SensitiveContentDetected","message":"the request was rejected"}}`,
			want:   true,
		},
		{
			name:   "prose-only refusal with an opaque numeric code",
			status: 400,
			body:   `{"error":{"code":"1301","message":"系统检测到输入或生成内容可能包含不安全或敏感内容"}}`,
			want:   true,
		},
		{
			name:   "content exists risk",
			status: 400,
			body:   `{"error":{"message":"Content Exists Risk","type":"invalid_request_error"}}`,
			want:   true,
		},
		{
			name:   "451 with moderation prose",
			status: 451,
			body:   `{"message":"prompt blocked by moderation"}`,
			want:   true,
		},

		// Ordinary request-shape 4xx: identical at every candidate, so they
		// must keep failing fast instead of walking the chain.
		{
			name:   "schema violation is not moderation",
			status: 400,
			body:   `{"error":{"message":"input violates the schema: messages[0].role must be one of user, assistant"}}`,
			want:   false,
		},
		{
			name:   "unknown parameter",
			status: 400,
			body:   `{"error":{"message":"Unrecognized request argument supplied: reasoning_effort","type":"invalid_request_error"}}`,
			want:   false,
		},
		{
			name:   "context length exceeded",
			status: 400,
			body:   `{"error":{"message":"This model's maximum context length is 65536 tokens. However, your input resulted in 70000 tokens.","code":"context_length_exceeded"}}`,
			want:   false,
		},
		{
			name:   "unsupported content type",
			status: 400,
			body:   `{"error":{"message":"Invalid content type, expected application/json"}}`,
			want:   false,
		},
		{
			name:   "auth failure",
			status: 401,
			body:   `{"error":{"message":"Incorrect API key provided","code":"invalid_api_key"}}`,
			want:   false,
		},

		// Status gating.
		{
			name:   "moderation prose on a 500 is an outage, not a refusal",
			status: 500,
			body:   `{"error":{"message":"Input data may contain inappropriate content."}}`,
			want:   false,
		},
		{
			name:   "moderation code on a 404",
			status: 404,
			body:   `{"error":{"code":"data_inspection_failed"}}`,
			want:   false,
		},
		{
			name:   "empty body",
			status: 400,
			body:   "",
			want:   false,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isContentInspectionRejection(tc.status, tc.body); got != tc.want {
				t.Fatalf("isContentInspectionRejection(%d, %q) = %v, want %v", tc.status, tc.body, got, tc.want)
			}
		})
	}
}

// seedTwoCandidateModel wires one external model onto two providers pointed at
// upstreamURL, so a failover walks from c1-model to c2-model in sort order.
func seedTwoCandidateModel(t *testing.T, svc *RelayService, db *gorm.DB, upstreamURL string) *model.APIKey {
	t.Helper()
	p1 := createProvider(t, db, "p1", upstreamURL)
	createProviderKey(t, db, svc.masterKey, p1.ID, "sk-1", "k1", 1, true)
	p2 := createProvider(t, db, "p2", upstreamURL)
	createProviderKey(t, db, svc.masterKey, p2.ID, "sk-2", "k1", 1, true)

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
			SupportsStreaming: boolPtr(true), SupportsFunctionCalling: boolPtr(true),
			ManagementStatus: model.ModelCandidateStatusEnabled, SortOrder: i + 1,
			VerificationStatus: model.ModelVerificationStatusPassed,
			CreatedAt:          now, UpdatedAt: now,
		}).Error; err != nil {
			t.Fatalf("seed candidate %d: %v", i, err)
		}
	}
	return createAPIKey(t, db, model.APIKeyStatusActive, []uint{m.ID})
}

// A content-inspection 400 must fail over even though status alone classifies
// every non-auth 4xx as terminal. The payload is fine; the first provider's
// moderation is what refused it, and the second serves the same bytes.
func TestContentInspectionRefusalFailsOver(t *testing.T) {
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
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"message":"Input data may contain inappropriate content.","type":"invalid_request_error","code":"data_inspection_failed"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"c2-model","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	svc := newRelaySvc(t, db)
	apiKey := seedTwoCandidateModel(t, svc, db, upstream.URL)

	c, w := newCtx([]byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	svc.Handle(c, apiKey)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 after content-inspection failover; body = %s", w.Code, w.Body.String())
	}
	seenMu.Lock()
	got := append([]string(nil), seenModels...)
	seenMu.Unlock()
	if len(got) != 2 || got[0] != "c1-model" || got[1] != "c2-model" {
		t.Fatalf("expected attempts with [c1-model, c2-model], got %v", got)
	}
}

// When every candidate moderates the payload the caller must still receive the
// upstream's own status and a reason naming content inspection — not the
// generic 502 that the exhausted-chain terminal reports for real outages.
func TestContentInspectionRefusalSurfacesAfterChainExhausted(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	var (
		mu    sync.Mutex
		calls int
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"data_inspection_failed","message":"Input data may contain inappropriate content."}}`))
	}))
	defer upstream.Close()

	svc := newRelaySvc(t, db)
	apiKey := seedTwoCandidateModel(t, svc, db, upstream.URL)

	c, w := newCtx([]byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	svc.Handle(c, apiKey)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want the upstream's own 400 to survive the exhausted chain; body = %s", w.Code, w.Body.String())
	}
	if !bytes.Contains(w.Body.Bytes(), []byte("content inspection")) {
		t.Errorf("error body should name content inspection, got %s", w.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 2 {
		t.Fatalf("both candidates should have been tried once each, got %d calls", calls)
	}
}

// A refusal that is followed by a genuine outage must NOT be reported as a
// refusal. The moderation verdict describes the candidate that issued it, so
// carrying it past a later 5xx would tell the caller to fix their prompt while
// the actual fault is an upstream that fell over.
func TestContentInspectionRefusalDoesNotOutliveALaterOutage(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		if bytes.Contains(body, []byte(`"model":"c1-model"`)) {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusBadRequest)
			_, _ = w.Write([]byte(`{"error":{"code":"data_inspection_failed","message":"Input data may contain inappropriate content."}}`))
			return
		}
		// Second candidate is simply down.
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer upstream.Close()

	svc := newRelaySvc(t, db)
	apiKey := seedTwoCandidateModel(t, svc, db, upstream.URL)

	c, w := newCtx([]byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	svc.Handle(c, apiKey)

	if w.Code != http.StatusBadGateway {
		t.Fatalf("status = %d, want 502: the chain ended on an outage, not a refusal; body = %s", w.Code, w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte("content inspection")) {
		t.Errorf("an outage must not be reported as a content-inspection refusal: %s", w.Body.String())
	}
}

// A refusal must not outlive a candidate that never reached an upstream at
// all. The second candidate here has no usable key, so it exits the loop before
// attemptOne runs — the exact path that resetting inside attemptOne would miss.
// Reporting "your prompt was refused" here would hide the misconfiguration the
// operator can actually fix.
func TestContentInspectionRefusalDoesNotOutliveASkippedCandidate(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	upstream := moderationOnlyUpstream(t, nil)
	defer upstream.Close()

	svc := newRelaySvc(t, db)
	p1 := createProvider(t, db, "p1", upstream.URL)
	createProviderKey(t, db, svc.masterKey, p1.ID, "sk-1", "k1", 1, true)
	// Second provider deliberately gets a DISABLED key, so the candidate is
	// dropped before any request is built or sent.
	p2 := createProvider(t, db, "p2", upstream.URL)
	createProviderKey(t, db, svc.masterKey, p2.ID, "sk-2", "k1", 1, false)

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
			SupportsStreaming: boolPtr(true), SupportsFunctionCalling: boolPtr(true),
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

	if w.Code == http.StatusBadRequest {
		t.Fatalf("a chain that ended on an undispatchable candidate must not be reported as a refusal; body = %s", w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte("content inspection")) {
		t.Errorf("stale refusal leaked into the terminal: %s", w.Body.String())
	}
}

// moderationOnlyUpstream refuses every request with a moderation 400.
func moderationOnlyUpstream(t *testing.T, hit *int32) *httptest.Server {
	t.Helper()
	_ = hit
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"data_inspection_failed","message":"Input data may contain inappropriate content."}}`))
	}))
}

// A refusal must not outlive the request budget either. The gate that stops
// walking candidates once the total budget is gone exits the loop
// mid-iteration, so a reset placed after it is skipped exactly when the
// previous candidate was refused. Running out of time is not a verdict on the
// caller's payload and must not be reported as one.
//
// Reaching that window takes some care: an attempt can never outlast the budget
// and still return a status, but the error-body read afterwards can. The
// upstream below flushes the moderation marker immediately and then stalls, so
// the read is cut short by the deadline while still yielding enough bytes to be
// classified as a refusal — leaving the budget spent by the next iteration.
func TestContentInspectionRefusalDoesNotOutliveTheRequestBudget(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"code":"data_inspection_failed","message":"`))
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		// Stall well past the request budget so the body read is what runs out
		// of time, not the attempt itself.
		select {
		case <-r.Context().Done():
		case <-time.After(2 * time.Second):
		}
	}))
	defer upstream.Close()

	svc := newRelaySvcWithGateway(t, db, config.GatewayConfig{
		AttemptTimeout: time.Second,
		RequestTimeout: 120 * time.Millisecond,
	})
	apiKey := seedTwoCandidateModel(t, svc, db, upstream.URL)

	c, w := newCtx([]byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	svc.Handle(c, apiKey)

	if w.Code == http.StatusBadRequest {
		t.Fatalf("a request that ran out of budget must not be reported as a content refusal; body = %s", w.Body.String())
	}
	if bytes.Contains(w.Body.Bytes(), []byte("content inspection")) {
		t.Errorf("stale refusal leaked past the budget gate: %s", w.Body.String())
	}
}

// An ordinary 400 must stay terminal on the first candidate. Walking the chain
// for a malformed request spends every candidate on a rejection that is
// identical everywhere and delays the error the caller needs.
func TestOrdinaryClientErrorStillTerminal(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	var (
		mu    sync.Mutex
		calls int
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		mu.Lock()
		calls++
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_, _ = w.Write([]byte(`{"error":{"message":"Unrecognized request argument supplied: reasoning_effort","type":"invalid_request_error"}}`))
	}))
	defer upstream.Close()

	svc := newRelaySvc(t, db)
	apiKey := seedTwoCandidateModel(t, svc, db, upstream.URL)

	c, w := newCtx([]byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	svc.Handle(c, apiKey)

	if w.Code != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 surfaced as-is; body = %s", w.Code, w.Body.String())
	}
	mu.Lock()
	defer mu.Unlock()
	if calls != 1 {
		t.Fatalf("a schema 400 must not fail over, got %d upstream calls", calls)
	}
}
