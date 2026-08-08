package gateway

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// errSentinelFlush is returned by the innermost test writer's FlushError,
// proving whether a caller can discover FlushError through gin's
// responseWriter wrapper.
var errSentinelFlush = errors.New("sentinel flush error from inner writer")

// innerFlushErrorWriter simulates net/http's *http.response: it implements
// http.Flusher (Flush) AND the FlushError() error method that
// http.NewResponseController prefers when it can reach it. It does NOT
// implement Unwrap (it is the innermost writer).
type innerFlushErrorWriter struct {
	headers http.Header
	flushed bool
}

func (w *innerFlushErrorWriter) Header() http.Header {
	if w.headers == nil {
		w.headers = make(http.Header)
	}
	return w.headers
}
func (w *innerFlushErrorWriter) Write(b []byte) (int, error) { return len(b), nil }
func (w *innerFlushErrorWriter) WriteHeader(code int)        {}
func (w *innerFlushErrorWriter) Flush()                      { w.flushed = true }
func (w *innerFlushErrorWriter) FlushError() error           { return errSentinelFlush }

// TestResponseController_FlushErrorReachableThroughGin verifies whether
// http.NewResponseController(c.Writer).Flush() can discover the innermost
// writer's FlushError() method when gin's responseWriter sits in between.
//
// gin.responseWriter implements http.Flusher (Flush) but NOT FlushError(), and
// also implements Unwrap() http.ResponseWriter. The Go stdlib
// ResponseController.Flush switch checks, at EACH level of the chain, in order:
//  1. interface{ FlushError() error }  -> call and return
//  2. http.Flusher                     -> call Flush, return nil (STOP)
//  3. rwUnwrapper (Unwrap)             -> descend
//
// Because gin.responseWriter matches case 2 (Flusher) BEFORE case 3 (Unwrap),
// ResponseController.Flush returns nil without ever descending to the inner
// writer's FlushError. This test documents that behavior so the manual
// unwrap in flushAndCheckError is NOT redundant.
func TestResponseController_FlushErrorReachableThroughGin(t *testing.T) {
	inner := &innerFlushErrorWriter{}
	c, _ := gin.CreateTestContext(inner)
	c.Request = httptest.NewRequest(http.MethodPost, "/", nil)

	// Sanity: gin wrapped the inner writer (Unwrap returns it).
	ginRw := c.Writer
	uw, ok := ginRw.(interface{ Unwrap() http.ResponseWriter })
	if !ok {
		t.Fatalf("gin responseWriter does not implement Unwrap (unexpected for gin v1.12+)")
	}
	if uw.Unwrap() != http.ResponseWriter(inner) {
		t.Fatalf("Unwrap did not return the inner writer")
	}

	// (A) ResponseController.Flush: expected to match gin's Flusher and return
	// nil, NEVER reaching inner.FlushError.
	rcErr := http.NewResponseController(c.Writer).Flush()
	if rcErr != nil {
		// If we end up here, ResponseController DID reach FlushError through
		// gin — the simplification would be safe after all.
		if !errors.Is(rcErr, errSentinelFlush) {
			t.Fatalf("ResponseController.Flush returned unexpected error: %v", rcErr)
		}
		t.Logf("NOTE: ResponseController.Flush reached inner FlushError (returned %v) — unwrap chain worked", rcErr)
	}

	// (B) The current manual-unwrap flushAndCheckError MUST reach the inner
	// FlushError — this is the behavior the production code relies on.
	manualErr := flushAndCheckError(c)
	if !errors.Is(manualErr, errSentinelFlush) {
		t.Fatalf("flushAndCheckError did not surface inner FlushError; got %v, want %v",
			manualErr, errSentinelFlush)
	}

	// Document the divergence explicitly: if ResponseController returned nil
	// while the manual unwrap returned the sentinel, ResponseController cannot
	// replace the manual unwrap.
	if rcErr == nil && manualErr != nil {
		t.Logf("ResponseController.Flush returned nil (gin Flush shadowed inner FlushError); " +
			"manual unwrap returned the sentinel. Manual unwrap is NOT redundant — do not simplify.")
	}

	// (C) Control: with a plain inner writer that only implements Flusher (no
	// FlushError), both paths must return nil — this is the normal success path.
	rec := httptest.NewRecorder()
	c2, _ := gin.CreateTestContext(rec)
	c2.Request = httptest.NewRequest(http.MethodPost, "/", nil)
	if err := http.NewResponseController(c2.Writer).Flush(); err != nil {
		t.Errorf("ResponseController.Flush on plain Flusher returned error: %v", err)
	}
	if err := flushAndCheckError(c2); err != nil {
		t.Errorf("flushAndCheckError on plain Flusher returned error: %v", err)
	}
}
