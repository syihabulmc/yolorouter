package gateway

import (
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/yolorouter/yolorouter/internal/model"
	"github.com/yolorouter/yolorouter/internal/protocols"
	"github.com/yolorouter/yolorouter/internal/testutil"
)

// This file covers the gap the earlier sliding-write-deadline regression
// suite (dispatch_response_classification_test.go and its _extra counterpart)
// left open: those tests only drove the cross-protocol IR streaming path
// (protocols.IRStreamRelay, reached via a
// Claude/Anthropic-typed provider). In production every configured provider
// is provider_type=openai, so every real stream goes through the
// same-protocol byte-passthrough pumps in dispatch.go
// (passthroughStreamToClient / passthroughStreamToClientDecoded) instead —
// which, before this fix, silently discarded every downstream Write/Flush
// error and never enforced the sliding write deadline at all. The tests
// below drive those two pumps directly through the full production dispatch
// path (svc.Handle over a real *http.Server, exactly like a real client).

// TestPassthroughStreamToClient_ClientWriteTimeoutClassification drives the
// production OpenAI same-protocol passthrough stream pump
// (passthroughStreamToClient) with a client that connects but never reads
// the response body. Once the kernel's TCP send buffer fills, the gateway's
// Write blocks; the sliding write deadline (streamWriteWindow, shrunk here)
// must cut it, and the resulting error must be classified as a client-side
// write timeout (AttemptConnError + "client write timeout"), not an upstream
// server fault — and the handler must actually return (concurrency slot
// released), not hang.
func TestPassthroughStreamToClient_ClientWriteTimeoutClassification(t *testing.T) {
	prevWindow := protocols.StreamWriteWindow()
	protocols.SetStreamWriteWindow(150 * time.Millisecond)
	t.Cleanup(func() { protocols.SetStreamWriteWindow(prevWindow) })

	db := testutil.NewSQLiteDB(t)

	// Upstream speaks plain OpenAI chat.completion.chunk SSE — the
	// production shape, since every provider is provider_type=openai. Large
	// per-chunk padding fills the client's TCP + kernel buffers quickly,
	// which is what triggers the gateway-side sliding write deadline.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		pad := strings.Repeat("x", 8192)
		for {
			select {
			case <-r.Context().Done():
				return
			default:
			}
			_, _ = w.Write([]byte(`data: {"id":"x","object":"chat.completion.chunk","model":"real-model","choices":[{"index":0,"delta":{"content":"` + pad + `"}}]}` + "\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer upstream.Close()

	svc := newRelaySvc(t, db)
	p := createProvider(t, db, "p-passthrough-slow", upstream.URL)
	createProviderKey(t, db, svc.masterKey, p.ID, "sk-passthrough-slow", "k1", 1, true)
	m := createModelAndCandidate(t, db, p, "gpt-4o-slow", "gpt-4o-real", true, true, 1)
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, []uint{m.ID})

	gin.SetMode(gin.TestMode)
	r := gin.New()
	var capturedRC atomic.Pointer[RelayContext]
	prevHook := testHookHandleDone
	testHookHandleDone = func(rc *RelayContext) { capturedRC.Store(rc) }
	t.Cleanup(func() { testHookHandleDone = prevHook })

	r.POST("/v1/chat/completions", func(c *gin.Context) {
		c.Set("request_id", "req-passthrough-slow-client")
		svc.Handle(c, apiKey)
	})

	srv := &http.Server{Handler: r, ReadTimeout: 5 * time.Second, WriteTimeout: 30 * time.Second}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close(); _ = ln.Close() }()

	// Connect but do NOT read the body — the handler's Writes eventually
	// block in the TCP buffer, and the sliding write deadline cuts it.
	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Post("http://"+ln.Addr().String()+"/v1/chat/completions", "application/json",
		strings.NewReader(`{"model":"gpt-4o-slow","messages":[{"role":"user","content":"hi"}],"stream":true}`))
	if err != nil {
		t.Fatalf("connect to gateway: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	waitStart := time.Now()
	for time.Since(waitStart) < 10*time.Second {
		if capturedRC.Load() != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	rc := capturedRC.Load()
	if rc == nil {
		t.Fatal("handler did not complete within 10s — the sliding write deadline should have released the concurrency slot")
	}

	if len(rc.Attempts) == 0 {
		t.Fatal("expected at least one attempt record")
	}
	last := rc.Attempts[len(rc.Attempts)-1]
	if last.Outcome != AttemptConnError {
		t.Errorf("attempt outcome = %q, want %q (client write timeout must not be classified as an upstream server fault)",
			last.Outcome, AttemptConnError)
	}
	if !strings.Contains(last.FailReason, "client write timeout") {
		t.Errorf("fail_reason = %q, want it to contain 'client write timeout'", last.FailReason)
	}
}

// TestPassthroughStreamToClientDecoded_ClientWriteTimeoutClassification is
// TestPassthroughStreamToClient_ClientWriteTimeoutClassification's
// counterpart for the non-OpenAI same-protocol passthrough pump
// (passthroughStreamToClientDecoded), reached by a native Gemini ingress
// request against a provider_type=gemini provider (createGeminiProvider —
// same protocol both sides, so no IR round trip).
func TestPassthroughStreamToClientDecoded_ClientWriteTimeoutClassification(t *testing.T) {
	prevWindow := protocols.StreamWriteWindow()
	protocols.SetStreamWriteWindow(150 * time.Millisecond)
	t.Cleanup(func() { protocols.SetStreamWriteWindow(prevWindow) })

	db := testutil.NewSQLiteDB(t)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		flusher, _ := w.(http.Flusher)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		pad := strings.Repeat("x", 8192)
		for {
			select {
			case <-r.Context().Done():
				return
			default:
			}
			_, _ = w.Write([]byte(`data: {"candidates":[{"content":{"role":"model","parts":[{"text":"` + pad + `"}]}}],"modelVersion":"gemini-real"}` + "\n\n"))
			if flusher != nil {
				flusher.Flush()
			}
		}
	}))
	defer upstream.Close()

	svc := newRelaySvc(t, db)
	p := createGeminiProvider(t, db, "gemini-passthrough-slow", upstream.URL)
	createProviderKey(t, db, svc.masterKey, p.ID, "sk-gemini-slow", "k1", 1, true)
	m := createModelAndCandidate(t, db, p, "gemini-2.0-flash-slow", "gemini-real", true, true, 1)
	apiKey := createAPIKey(t, db, model.APIKeyStatusActive, []uint{m.ID})

	gin.SetMode(gin.TestMode)
	r := gin.New()
	var capturedRC atomic.Pointer[RelayContext]
	prevHook := testHookHandleDone
	testHookHandleDone = func(rc *RelayContext) { capturedRC.Store(rc) }
	t.Cleanup(func() { testHookHandleDone = prevHook })

	r.POST("/v1beta/models/:modelaction", func(c *gin.Context) {
		c.Set("request_id", "req-gemini-passthrough-slow-client")
		svc.Handle(c, apiKey)
	})

	srv := &http.Server{Handler: r, ReadTimeout: 5 * time.Second, WriteTimeout: 30 * time.Second}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close(); _ = ln.Close() }()

	client := &http.Client{Timeout: 15 * time.Second}
	resp, err := client.Post(
		"http://"+ln.Addr().String()+"/v1beta/models/gemini-2.0-flash-slow:streamGenerateContent",
		"application/json",
		strings.NewReader(`{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`))
	if err != nil {
		t.Fatalf("connect to gateway: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()

	waitStart := time.Now()
	for time.Since(waitStart) < 10*time.Second {
		if capturedRC.Load() != nil {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	rc := capturedRC.Load()
	if rc == nil {
		t.Fatal("handler did not complete within 10s — the sliding write deadline should have released the concurrency slot")
	}

	if len(rc.Attempts) == 0 {
		t.Fatal("expected at least one attempt record")
	}
	last := rc.Attempts[len(rc.Attempts)-1]
	if last.Outcome != AttemptConnError {
		t.Errorf("attempt outcome = %q, want %q (client write timeout must not be classified as an upstream server fault)",
			last.Outcome, AttemptConnError)
	}
	if !strings.Contains(last.FailReason, "client write timeout") {
		t.Errorf("fail_reason = %q, want it to contain 'client write timeout'", last.FailReason)
	}
}

// failingPassthroughWriter is an http.ResponseWriter whose Write always
// fails, used to unit-test the passthrough pumps' error propagation without
// needing a real network connection.
type failingPassthroughWriter struct {
	headers http.Header
	status  int
}

func (w *failingPassthroughWriter) Header() http.Header {
	if w.headers == nil {
		w.headers = make(http.Header)
	}
	return w.headers
}
func (w *failingPassthroughWriter) Write(b []byte) (int, error) {
	return 0, errors.New("simulated downstream write failure")
}
func (w *failingPassthroughWriter) WriteHeader(code int) { w.status = code }

// TestPassthroughStreamToClient_WriteErrorPropagatedAsClientWrite is a fast,
// deterministic unit test for FIX 1 + FIX 2: a downstream Write failure
// inside passthroughStreamToClient's forward loop must be wrapped in
// protocols.ErrClientWrite and returned (not silently swallowed), and the
// undelivered bytes must not land in the stream capture file.
func TestPassthroughStreamToClient_WriteErrorPropagatedAsClientWrite(t *testing.T) {
	dir := t.TempDir()
	fw := &failingPassthroughWriter{}
	c, _ := gin.CreateTestContext(fw)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Set(BodiesDirContextKey, dir)
	rc := &RelayContext{RequestID: "req-passthrough-write-fail", OriginalModel: "ext", IsStream: true}

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`data: {"model":"real","choices":[{"delta":{"content":"hi"}}]}` + "\n\n")),
		Header:     make(http.Header),
	}
	_, err := passthroughStreamToClient(c, resp, rc)
	if err == nil {
		t.Fatal("expected a write error, got nil")
	}
	if !errors.Is(err, protocols.ErrClientWrite) {
		t.Errorf("error must be classified via protocols.ErrClientWrite, got %v", err)
	}
	if !isClientWriteError(err) {
		t.Errorf("isClientWriteError(%v) = false, want true", err)
	}
	captured, readErr := os.ReadFile(filepath.Join(dir, rc.RequestID+".stream"))
	if readErr != nil {
		t.Fatalf("read capture file: %v", readErr)
	}
	if len(captured) > 0 {
		t.Errorf("capture file must be empty when the write never reached the client, got %q", captured)
	}
}

// TestPassthroughStreamToClientDecoded_WriteErrorPropagatedAsClientWrite is
// TestPassthroughStreamToClient_WriteErrorPropagatedAsClientWrite's
// counterpart for passthroughStreamToClientDecoded (the non-OpenAI
// same-protocol pump), using a Claude-shaped upstream body.
func TestPassthroughStreamToClientDecoded_WriteErrorPropagatedAsClientWrite(t *testing.T) {
	dir := t.TempDir()
	fw := &failingPassthroughWriter{}
	c, _ := gin.CreateTestContext(fw)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	c.Set(BodiesDirContextKey, dir)
	rc := &RelayContext{RequestID: "req-passthrough-decoded-write-fail", OriginalModel: "ext", IsStream: true}

	body := `event: message_start` + "\n" +
		`data: {"type":"message_start","message":{"id":"msg_1","type":"message","role":"assistant","model":"real-model","content":[],"usage":{"input_tokens":10,"output_tokens":0}}}` + "\n\n"
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
	_, err := passthroughStreamToClientDecoded(c, resp, rc, protocols.ProtocolClaude)
	if err == nil {
		t.Fatal("expected a write error, got nil")
	}
	if !errors.Is(err, protocols.ErrClientWrite) {
		t.Errorf("error must be classified via protocols.ErrClientWrite, got %v", err)
	}
	captured, readErr := os.ReadFile(filepath.Join(dir, rc.RequestID+".stream"))
	if readErr != nil {
		t.Fatalf("read capture file: %v", readErr)
	}
	if len(captured) > 0 {
		t.Errorf("capture file must be empty when the write never reached the client, got %q", captured)
	}
}
