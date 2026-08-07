package protocols_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/yolorouter/yolorouter/internal/protocols"
)

// TestUpstreamHeadersToCopyIsAnAllowlist checks the policy on its own.
//
// It could previously only be reached by running a whole relay, so what the
// filter did was pinned by tests that were really about something else. A
// provider's headers can name the provider, the account, or the model behind an
// alias, which is why the default is to drop.
func TestUpstreamHeadersToCopyIsAnAllowlist(t *testing.T) {
	got := protocols.UpstreamHeadersToCopy(http.Header{
		"Cache-Control":         {"no-store", "no-cache"},
		"X-Ratelimit-Remaining": {"42"},
		"X-Request-Id":          {"upstream-internal"},
		"Openai-Organization":   {"org-secret"},
		"Server":                {"provider-gateway/1.2"},
	})

	if v := got["Cache-Control"]; len(v) != 2 || v[0] != "no-store" || v[1] != "no-cache" {
		t.Errorf("Cache-Control = %v, want both values kept in order", v)
	}
	for _, dropped := range []string{"X-Ratelimit-Remaining", "X-Request-Id", "Openai-Organization", "Server"} {
		if _, ok := got[dropped]; ok {
			t.Errorf("%s survived the filter; it describes the provider, not the response", dropped)
		}
	}
}

// TestUpstreamHeadersToCopyKeepsNothingByDefault pins the direction the filter
// fails in. A header nobody thought about must be dropped, not forwarded.
func TestUpstreamHeadersToCopyKeepsNothingByDefault(t *testing.T) {
	if got := protocols.UpstreamHeadersToCopy(http.Header{"X-Some-New-Thing": {"v"}}); len(got) != 0 {
		t.Errorf("filter returned %v for a header it has never heard of, want nothing", got)
	}
	if got := protocols.UpstreamHeadersToCopy(http.Header{}); len(got) != 0 {
		t.Errorf("filter returned %v for empty input", got)
	}
	if got := protocols.UpstreamHeadersToCopy(nil); len(got) != 0 {
		t.Errorf("filter returned %v for nil input", got)
	}
}

// TestInjectReplaces pins Inject's replacement semantics: a relay setting
// Content-Type is stating what the body is, and a second value would make the
// response malformed — so Inject replaces rather than accumulates, while still
// keeping every value of a single multi-valued set it is handed.
func TestInjectReplaces(t *testing.T) {
	gin.SetMode(gin.TestMode)

	t.Run("Inject replaces what is already there", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Writer.Header().Set("Content-Type", "text/plain")

		protocols.NewGinClientWriter(c).Inject(http.Header{"Content-Type": {"application/json"}})

		if got := c.Writer.Header()["Content-Type"]; len(got) != 1 || got[0] != "application/json" {
			t.Errorf("Content-Type = %v, want exactly [application/json]; two of them is a malformed response", got)
		}
	})

	t.Run("Inject keeps every value it is given", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())

		protocols.NewGinClientWriter(c).Inject(http.Header{"Cache-Control": {"no-store", "no-cache"}})

		if got := c.Writer.Header()["Cache-Control"]; len(got) != 2 {
			t.Errorf("Cache-Control = %v, want both values: replacing by name must not collapse a multi-value header", got)
		}
	})

}

// deadlineRecorder records the write deadlines set on it. httptest's own
// recorder does not support them, so a writer that quietly stopped setting one
// would look identical to a writer that never could.
type deadlineRecorder struct {
	gin.ResponseWriter
	deadlines []time.Time
}

func (d *deadlineRecorder) SetWriteDeadline(t time.Time) error {
	d.deadlines = append(d.deadlines, t)
	return nil
}

// TestEveryWriteToTheCallerIsBounded pins the promise both implementations of
// ClientWriter now make.
//
// A caller that stops reading holds the handler open for as long as it likes —
// a connection and a concurrency slot spent on somebody who left. The streaming
// loops used to set this themselves, so the protection depended on which loop
// you were in; a non-streaming response had none at all.
func TestEveryWriteToTheCallerIsBounded(t *testing.T) {
	gin.SetMode(gin.TestMode)
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	rec := &deadlineRecorder{ResponseWriter: c.Writer}
	c.Writer = rec

	w := protocols.NewGinClientWriter(c)
	if err := w.Commit(200); err != nil {
		t.Fatalf("Commit = %v", err)
	}
	if _, err := w.Write([]byte("hello")); err != nil {
		t.Fatalf("Write = %v", err)
	}
	_ = w.Flush()

	if len(rec.deadlines) < 2 {
		t.Fatalf("write deadlines set: %d, want one per write and one per flush", len(rec.deadlines))
	}
	for i, d := range rec.deadlines {
		if !d.After(time.Now()) {
			t.Errorf("deadline %d is %v, already in the past", i, d)
		}
	}
}
