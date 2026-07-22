package protocols_test

import (
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yolorouter/yolorouter/internal/protocols"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestClearWriteDeadlineLetsStreamExceedServerWriteTimeout is a required
// integration regression: it starts a real http.Server with a short
// WriteTimeout (150ms); the handler uses ClearWriteDeadline to clear the
// write deadline for that request alone, then spends ≈400ms writing 5
// streaming chunks. It asserts the client receives all 5 chunks intact
// instead of being cut off by WriteTimeout.
//
// Anti-regression scenario: if ClearWriteDeadline fails silently (e.g.
// SetWriteDeadline returns ErrNotSupported but the error is swallowed), or a
// future Go/Gin/middleware change breaks ResponseController, this test fails
// immediately.
func TestClearWriteDeadlineLetsStreamExceedServerWriteTimeout(t *testing.T) {
	const (
		writeTimeout = 150 * time.Millisecond
		chunkGap     = 80 * time.Millisecond
		chunkCount   = 5
	)

	gin.SetMode(gin.TestMode)
	r := gin.New()
	var clearErrAtomic atomic.Pointer[error]
	r.GET("/stream", func(c *gin.Context) {
		if err := protocols.ClearWriteDeadline(c); err != nil {
			clearErrAtomic.Store(&err)
		}
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.Header().Set("X-Accel-Buffering", "no")
		c.Writer.WriteHeader(http.StatusOK)
		c.Writer.Flush()
		for range chunkCount {
			time.Sleep(chunkGap)
			_, _ = c.Writer.Write([]byte("data: chunk\n\n"))
			c.Writer.Flush()
		}
	})

	srv := &http.Server{
		Handler:      r,
		WriteTimeout: writeTimeout,
		ReadTimeout:  2 * time.Second,
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = srv.Serve(ln) }()
	defer func() {
		_ = srv.Close()
		_ = ln.Close()
	}()

	resp, err := http.Get("http://" + ln.Addr().String() + "/stream")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	require.Equal(t, http.StatusOK, resp.StatusCode)
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	// Total write time ≈ 5 * 80ms = 400ms > the 150ms WriteTimeout.
	// Without ClearWriteDeadline taking effect, the server would force-close
	// the connection around the 2nd chunk, and io.ReadAll would either
	// report an unexpected EOF or only receive a partial set of chunks.
	got := strings.Count(string(body), "data: chunk")
	assert.Equalf(t, chunkCount, got,
		"expected to receive %d chunks, got %d. This means ClearWriteDeadline had no effect and the stream was cut off by server.WriteTimeout. body=%q",
		chunkCount, got, string(body))

	if errPtr := clearErrAtomic.Load(); errPtr != nil {
		assert.Failf(t, "ClearWriteDeadline returned an error",
			"on the production path, *http.response should support SetWriteDeadline; getting %v means ResponseController is broken", *errPtr)
	}
}

// TestClearWriteDeadlineReturnsErrorOnUnsupportedWriter verifies that the
// caller gets a non-nil error back: *httptest.ResponseRecorder does not
// implement SetWriteDeadline, so ClearWriteDeadline should return a non-nil
// error rather than panicking (guards the unit test scenario so that if an
// upstream fix removes the ResponseController panic, this test starts
// failing instead of silently passing).
func TestClearWriteDeadlineReturnsErrorOnUnsupportedWriter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/x", nil)

	err := protocols.ClearWriteDeadline(c)
	require.Error(t, err, "a ResponseRecorder that doesn't support SetWriteDeadline must return an error, not swallow it")
}

// TestResponseControllerSetWriteDeadlineWorksThroughNestedWrappers verifies
// that when some middleware wraps c.Writer but **forgets to implement
// Unwrap()**, ResponseController.SetWriteDeadline returns ErrNotSupported,
// the sliding-idle protection silently fails, and the streaming response
// remains subject to server.WriteTimeout. A response-writer wrapper that
// forgot to implement Unwrap was found to trigger exactly this failure mode.
//
// This test wraps a real ResponseWriter in a "wrapper that lacks Unwrap" and
// asserts that ClearWriteDeadline is able to detect it (by returning an
// error), letting the caller know the sliding-idle protection didn't take
// effect.
func TestSetWriteDeadlineFailsWhenWrapperLacksUnwrap(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	// Middleware: wraps c.Writer but deliberately does not implement Unwrap().
	r.Use(func(c *gin.Context) {
		c.Writer = &wrapperWithoutUnwrap{ResponseWriter: c.Writer}
		c.Next()
	})
	var got atomic.Pointer[error]
	r.GET("/x", func(c *gin.Context) {
		err := protocols.ClearWriteDeadline(c)
		got.Store(&err)
		c.String(http.StatusOK, "ok")
	})
	srv := &http.Server{Handler: r}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close(); _ = ln.Close() }()

	resp, err := http.Get("http://" + ln.Addr().String() + "/x")
	require.NoError(t, err)
	_ = resp.Body.Close()
	gotErr := got.Load()
	require.NotNil(t, gotErr)
	require.Error(t, *gotErr,
		"wrapping c.Writer without implementing Unwrap() must make ClearWriteDeadline return an error so the caller can warn — "+
			"otherwise the sliding-idle protection silently fails, risking a repeat of the server.WriteTimeout hard-cancel issue")
}

// wrapperWithoutUnwrap deliberately does not implement Unwrap(), simulating
// middleware that forgets to do so.
type wrapperWithoutUnwrap struct {
	gin.ResponseWriter
}

// TestClearWriteDeadlineAllowsStreamPastShortWriteTimeout verifies that once
// ClearWriteDeadline zeroes out the deadline, server.WriteTimeout no longer
// constrains that streaming request. This is the most critical regression
// for the core hard-cancel fix: after the handler calls ClearWriteDeadline
// at entry, the global WriteTimeout must have no effect.
func TestClearWriteDeadlineAllowsStreamPastShortWriteTimeout(t *testing.T) {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.GET("/timing", func(c *gin.Context) {
		_ = protocols.ClearWriteDeadline(c)
		c.Writer.WriteHeader(http.StatusOK)
		c.Writer.Flush()
		// 6 chunks spaced 60ms apart, total ~360ms > the 100ms server.WriteTimeout.
		// Without ClearWriteDeadline, the 2nd chunk would already be cut off.
		for range 6 {
			time.Sleep(60 * time.Millisecond)
			_, _ = c.Writer.Write([]byte("data: chunk\n\n"))
			c.Writer.Flush()
		}
	})
	srv := &http.Server{
		Handler:      r,
		WriteTimeout: 100 * time.Millisecond,
		ReadTimeout:  2 * time.Second,
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close(); _ = ln.Close() }()

	resp, err := http.Get("http://" + ln.Addr().String() + "/timing")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, _ := io.ReadAll(resp.Body)
	count := strings.Count(string(body), "data: chunk")
	assert.Equalf(t, 6, count,
		"ClearWriteDeadline should let all 6 chunks arrive intact (total 360ms vs the 100ms WriteTimeout); got %d. body=%q",
		count, string(body))
}
