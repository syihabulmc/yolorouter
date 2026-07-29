package protocols_test

import (
	"io"
	"net"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yolorouter/yolorouter/internal/protocols"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestSlidingWriteDeadlineCutsSlowClient verifies the core streaming
// protection: when streamWriteWindow is short and the client does not read
// the response body, the handler's Write call eventually blocks (TCP send
// buffer fills) and the sliding deadline cuts it within streamWriteWindow
// instead of letting it block indefinitely.
//
// The test starts a real http.Server (so SetWriteDeadline is honoured by the
// real *http.response writer). The handler writes 64 KiB chunks in a loop
// with ApplyStreamWriteDeadline before each batch. The client connects but
// never reads the body, so the TCP send buffer fills after a few chunks and
// the next Write blocks. The sliding deadline must fire and turn that Write
// into an error.
func TestSlidingWriteDeadlineCutsSlowClient(t *testing.T) {
	prev := protocols.StreamWriteWindow()
	protocols.SetStreamWriteWindow(150 * time.Millisecond)
	t.Cleanup(func() { protocols.SetStreamWriteWindow(prev) })

	gin.SetMode(gin.TestMode)
	r := gin.New()
	var handlerDone atomic.Bool
	var writeErr atomic.Pointer[error]
	r.GET("/stream", func(c *gin.Context) {
		defer func() { handlerDone.Store(true) }()
		c.Writer.Header().Set("Content-Type", "application/octet-stream")
		c.Writer.WriteHeader(http.StatusOK)
		chunk := make([]byte, 64*1024) // 64 KiB — fills TCP buffer quickly
		for i := 0; i < 100; i++ {     // up to 6.4 MiB
			if err := protocols.ApplyStreamWriteDeadline(c); err != nil {
				writeErr.Store(&err)
				return
			}
			if _, err := c.Writer.Write(chunk); err != nil {
				writeErr.Store(&err)
				return
			}
			c.Writer.Flush()
		}
	})

	srv := &http.Server{
		Handler:      r,
		ReadTimeout:  2 * time.Second,
		WriteTimeout: 30 * time.Second,
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close(); _ = ln.Close() }()

	// Connect but do NOT read the body — simulates a slow/stalled client.
	resp, err := http.Get("http://" + ln.Addr().String() + "/stream")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	// Wait for the handler to finish; it should return within a few x
	// streamWriteWindow because the sliding deadline cuts the blocked Write.
	waitStart := time.Now()
	for time.Since(waitStart) < 5*time.Second {
		if handlerDone.Load() {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}

	require.True(t, handlerDone.Load(),
		"handler should have returned after the sliding write deadline fired")

	errPtr := writeErr.Load()
	require.NotNil(t, errPtr,
		"a blocked Write should have returned a deadline-exceeded error")
	require.Error(t, *errPtr,
		"Write to a slow client must fail within streamWriteWindow")
}

// TestSlidingWriteDeadlineKeepsFastClientAlive verifies the complementary
// case: a healthy stream that actively writes resets the deadline on every
// chunk, so the connection stays alive well past any single window. The
// stream writes 5 chunks spaced less than streamWriteWindow apart; a fast
// client should receive all of them even when server.WriteTimeout is shorter
// than the total stream time.
func TestSlidingWriteDeadlineKeepsFastClientAlive(t *testing.T) {
	prev := protocols.StreamWriteWindow()
	protocols.SetStreamWriteWindow(200 * time.Millisecond)
	t.Cleanup(func() { protocols.SetStreamWriteWindow(prev) })

	gin.SetMode(gin.TestMode)
	r := gin.New()
	const chunkCount = 5
	r.GET("/stream", func(c *gin.Context) {
		c.Writer.Header().Set("Content-Type", "text/event-stream")
		c.Writer.WriteHeader(http.StatusOK)
		for range chunkCount {
			_ = protocols.ApplyStreamWriteDeadline(c)
			_, _ = c.Writer.Write([]byte("data: chunk\n\n"))
			c.Writer.Flush()
			// Gap between chunks is less than streamWriteWindow, so the
			// sliding deadline keeps the connection alive.
			time.Sleep(80 * time.Millisecond)
		}
	})

	// server.WriteTimeout (300 ms) < total stream time (5 x 80 ms = 400 ms).
	// Without the sliding deadline overriding it on each write, the server
	// would cut the stream around the 3rd chunk.
	srv := &http.Server{
		Handler:      r,
		ReadTimeout:  2 * time.Second,
		WriteTimeout: 300 * time.Millisecond,
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	require.NoError(t, err)
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close(); _ = ln.Close() }()

	resp, err := http.Get("http://" + ln.Addr().String() + "/stream")
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)

	got := strings.Count(string(body), "data: chunk")
	assert.Equal(t, chunkCount, got,
		"sliding deadline should keep the stream alive for all %d chunks; got %d. body=%q",
		chunkCount, got, string(body))
}
