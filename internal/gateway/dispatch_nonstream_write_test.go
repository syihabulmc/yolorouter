package gateway

import (
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/yolorouter/yolorouter/internal/model"
	"github.com/yolorouter/yolorouter/internal/protocols"
	"github.com/yolorouter/yolorouter/internal/testutil"
)

// TestDispatchPassthroughNonStream_WriteErrorClassifiedAsClientWrite drives
// the production same-protocol non-stream passthrough path
// (dispatchPassthroughNonStream) directly with a ResponseWriter whose Write
// always fails (failingPassthroughWriter, already used by the streaming
// write-error tests in passthrough_write_deadline_test.go) — a fast,
// deterministic stand-in for a slow/disconnected client, instead of relying
// on real network timing (non-stream responses have no per-write sliding
// deadline the way streaming does, so a wall-clock-based reproduction would
// race the whole test process's scheduling under load).
//
// Before the P2 fix, the final c.Writer.Write(rewritten) call discarded its
// error entirely and the request was finalized as a delivered 2xx success
// even though the client received nothing. It must instead settle as a 499
// client_write_timeout, must NOT record the undelivered body as the
// caller-facing response, and must still preserve the already-known usage
// for billing (the upstream itself was fully consumed regardless of
// delivery outcome).
func TestDispatchPassthroughNonStream_WriteErrorClassifiedAsClientWrite(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	svc := newRelaySvc(t, db)
	p := createProvider(t, db, "p1", "http://upstream.invalid")
	m := createModelAndCandidate(t, db, p, "gpt-4o", "gpt-4o-real", false, false, 1)
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, []uint{m.ID})

	var cand model.ModelCandidate
	if err := db.Where("model_id = ?", m.ID).First(&cand).Error; err != nil {
		t.Fatalf("load seeded candidate: %v", err)
	}

	gin.SetMode(gin.TestMode)
	fw := &failingPassthroughWriter{}
	c, _ := gin.CreateTestContext(fw)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	rc := &RelayContext{RequestID: "req-nonstream-write-fail", OriginalModel: "gpt-4o", APIKeyID: apiKey.ID}

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"model":"gpt-4o-real","choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`)),
		Header:     make(http.Header),
	}

	result := svc.dispatchPassthroughNonStream(c, rc, protocols.ProtocolOpenAI, cand, p, model.ProviderKey{}, resp, time.Now())

	if result != attemptSuccess {
		t.Errorf("result = %v, want attemptSuccess (status+headers already committed, cannot fail over)", result)
	}
	if rc.StatusCode != 499 {
		t.Errorf("rc.StatusCode = %d, want 499 (a discarded write failure must not settle as a delivered 2xx success)", rc.StatusCode)
	}
	if len(rc.ResponseBody) != 0 {
		t.Errorf("ResponseBody must stay empty when the write to the client failed (never delivered), got %d bytes", len(rc.ResponseBody))
	}
	if len(rc.UpstreamResponseBody) == 0 {
		t.Error("UpstreamResponseBody should still be recorded (what the gateway actually consumed from upstream) even though delivery to the client failed")
	}
	if rc.Usage == nil || rc.Usage.PromptTokens != 5 {
		t.Errorf("Usage = %+v, want the already-decoded usage preserved for billing despite the write failure", rc.Usage)
	}
	if len(rc.Attempts) != 1 {
		t.Fatalf("Attempts = %+v, want exactly one entry", rc.Attempts)
	}
	if rc.Attempts[0].Outcome != AttemptConnError {
		t.Errorf("attempt outcome = %q, want %q (client write timeout must not be classified as an upstream server fault)",
			rc.Attempts[0].Outcome, AttemptConnError)
	}
	if !strings.Contains(rc.Attempts[0].FailReason, "client write timeout") {
		t.Errorf("fail_reason = %q, want it to contain 'client write timeout'", rc.Attempts[0].FailReason)
	}
}

// TestDispatchPassthroughNonStream_WriteSucceedsRecordsDeliveredResponse is
// the positive control: when the write succeeds, the response IS recorded
// as delivered (rc.ResponseBody set) and the request finalizes with the
// upstream's own status code, not 499.
func TestDispatchPassthroughNonStream_WriteSucceedsRecordsDeliveredResponse(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	svc := newRelaySvc(t, db)
	p := createProvider(t, db, "p1", "http://upstream.invalid")
	m := createModelAndCandidate(t, db, p, "gpt-4o", "gpt-4o-real", false, false, 1)
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, []uint{m.ID})

	var cand model.ModelCandidate
	if err := db.Where("model_id = ?", m.ID).First(&cand).Error; err != nil {
		t.Fatalf("load seeded candidate: %v", err)
	}

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	rc := &RelayContext{RequestID: "req-nonstream-write-ok", OriginalModel: "gpt-4o", APIKeyID: apiKey.ID}

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"model":"gpt-4o-real","choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":5,"completion_tokens":3,"total_tokens":8}}`)),
		Header:     make(http.Header),
	}

	result := svc.dispatchPassthroughNonStream(c, rc, protocols.ProtocolOpenAI, cand, p, model.ProviderKey{}, resp, time.Now())

	if result != attemptSuccess {
		t.Errorf("result = %v, want attemptSuccess", result)
	}
	if rc.StatusCode != http.StatusOK {
		t.Errorf("rc.StatusCode = %d, want 200", rc.StatusCode)
	}
	if len(rc.ResponseBody) == 0 {
		t.Error("ResponseBody should be recorded as delivered once the write succeeds")
	}
	if len(rc.Attempts) != 1 || rc.Attempts[0].Outcome != AttemptSuccess {
		t.Errorf("Attempts = %+v, want exactly one AttemptSuccess entry", rc.Attempts)
	}
}
