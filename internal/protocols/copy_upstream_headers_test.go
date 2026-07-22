package protocols_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/yolorouter/yolorouter/internal/protocols"
)

// TestCopyUpstreamHeaders_AllowlistPassthrough verifies that only headers in the
// allowlist (Cache-Control, Retry-After) are forwarded to clients.
// Content-Type is excluded from the allowlist because the relay layer sets the
// correct Content-Type for every path.
func TestCopyUpstreamHeaders_AllowlistPassthrough(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := http.Header{
		"Cache-Control": {"no-cache"},
		"Retry-After":   {"30"},
		"Content-Type":  {"application/json"}, // NOT in allowlist — relay sets this itself
	}

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	protocols.CopyUpstreamHeaders(c, upstream)
	got := c.Writer.Header()

	if got.Get("Cache-Control") != "no-cache" {
		t.Errorf("Cache-Control = %q, want %q", got.Get("Cache-Control"), "no-cache")
	}
	if got.Get("Retry-After") != "30" {
		t.Errorf("Retry-After = %q, want %q", got.Get("Retry-After"), "30")
	}
	// Content-Type must NOT be copied — relay handles it independently
	if got.Get("Content-Type") != "" {
		t.Errorf("Content-Type must be blocked by allowlist, got %q", got.Get("Content-Type"))
	}
}

// TestCopyUpstreamHeaders_MultiValueCacheControl verifies that multiple Cache-Control
// directives from upstream are preserved (using Add, not Set).
func TestCopyUpstreamHeaders_MultiValueCacheControl(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := http.Header{
		"Cache-Control": {"no-store", "private"},
	}

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	protocols.CopyUpstreamHeaders(c, upstream)
	got := c.Writer.Header()["Cache-Control"]

	if len(got) != 2 {
		t.Errorf("expected 2 Cache-Control values, got %d: %v", len(got), got)
	}
}

// TestCopyUpstreamHeaders_BlocksProviderInternalHeaders verifies that provider-internal
// and relay-injected headers are NOT forwarded, preventing tenant boundary violations.
func TestCopyUpstreamHeaders_BlocksProviderInternalHeaders(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := http.Header{
		// relay-injected compat headers
		"X-Generation-Id":      {"abc123"},
		"X-Model":              {"gpt-4"},
		"X-Provider":           {"openai"},
		"X-Response-Time":      {"42"},
		"Openai-Processing-Ms": {"99"},
		"X-Request-Id":         {"upstream-req-xyz"},
		// provider internal headers that could leak org/account details
		"Openai-Organization": {"org-abc123"},
		"Openai-Version":      {"2020-10-01"},
		// server infrastructure headers
		"Server": {"uvicorn"},
		"Via":    {"1.1 proxy"},
		// non-listed custom header
		"X-Upstream-Custom": {"should-be-dropped"},
	}

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	protocols.CopyUpstreamHeaders(c, upstream)
	got := c.Writer.Header()

	blocked := []string{
		"X-Generation-Id", "X-Model", "X-Provider", "X-Response-Time", "Openai-Processing-Ms",
		"X-Request-Id", "Openai-Organization", "Openai-Version", "Server", "Via", "X-Upstream-Custom",
	}
	for _, key := range blocked {
		if got.Get(key) != "" {
			t.Errorf("header %q must be blocked by allowlist, got %q", key, got.Get(key))
		}
	}
}

// TestCopyUpstreamHeaders_BlocksRatelimitFamily verifies that the entire X-Ratelimit-*
// family (requests, tokens, reset variants) is blocked by the allowlist.
func TestCopyUpstreamHeaders_BlocksRatelimitFamily(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := http.Header{
		"X-Ratelimit-Limit-Requests":     {"1000"},
		"X-Ratelimit-Remaining-Requests": {"500"},
		"X-Ratelimit-Limit-Tokens":       {"80000"},
		"X-Ratelimit-Remaining-Tokens":   {"79000"},
		"X-Ratelimit-Reset-Requests":     {"1ms"},
		"X-Ratelimit-Reset-Tokens":       {"1ms"},
		"Content-Type":                   {"application/json"},
	}

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	protocols.CopyUpstreamHeaders(c, upstream)
	got := c.Writer.Header()

	for _, key := range []string{
		"X-Ratelimit-Limit-Requests", "X-Ratelimit-Remaining-Requests",
		"X-Ratelimit-Limit-Tokens", "X-Ratelimit-Remaining-Tokens",
		"X-Ratelimit-Reset-Requests", "X-Ratelimit-Reset-Tokens",
		"Content-Type", // not in allowlist — relay sets this independently
	} {
		if got.Get(key) != "" {
			t.Errorf("header %q must be blocked by allowlist, got %q", key, got.Get(key))
		}
	}
}

// TestCopyUpstreamHeaders_EmptyUpstream verifies that an empty upstream header map
// is handled without panic.
func TestCopyUpstreamHeaders_EmptyUpstream(t *testing.T) {
	gin.SetMode(gin.TestMode)

	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	protocols.CopyUpstreamHeaders(c, http.Header{}) // must not panic
}
