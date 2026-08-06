package protocols_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

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

// TestInjectReplacesButCopyUpstreamHeadersMerges pins a difference that lives
// two functions apart in one file and reads like an inconsistency.
//
// It is not. A relay setting Content-Type is stating what the body is, and a
// second value would make the response malformed — so Inject replaces. Copying
// the upstream's allowed headers is a merge on top of what the relay already
// set: a stream sets Cache-Control: no-cache before this runs, and the
// upstream's own directives join it rather than evict it. Unifying the two
// would silently drop one side or duplicate the other.
func TestInjectReplacesButCopyUpstreamHeadersMerges(t *testing.T) {
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

	t.Run("CopyUpstreamHeaders adds to what the relay already set", func(t *testing.T) {
		c, _ := gin.CreateTestContext(httptest.NewRecorder())
		c.Writer.Header().Set("Cache-Control", "no-cache")

		protocols.CopyUpstreamHeaders(c, http.Header{"Cache-Control": {"max-age=60"}})

		got := c.Writer.Header()["Cache-Control"]
		if len(got) != 2 || got[0] != "no-cache" || got[1] != "max-age=60" {
			t.Errorf("Cache-Control = %v, want the relay's own directive kept and the upstream's added", got)
		}
	})
}
