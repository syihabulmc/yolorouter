package gateway

import (
	"net/http"

	"github.com/yolorouter/yolorouter/internal/service/safehttp"
)

// UpstreamClient sends the rewritten body to a provider. It reuses safehttp's
// SSRF-safe transport (provider_client.go's contract: every outbound dial is
// SSRF-checked) and disables redirect following so the decrypted upstream
// key cannot leak to a host the admin never confirmed.
type UpstreamClient struct {
	httpClient *http.Client
}

// NewUpstreamClient builds a gateway upstream client. The Transport is the
// same SSRF-safe one provider_client.go uses for connection tests, so a
// provider that tests green also serves real traffic through the identical
// network path — including the allowPrivate relaxation, so a self-hosted
// LAN/localhost provider that tests green also relays (config.SecurityConfig.
// AllowPrivateUpstreams).
func NewUpstreamClient(allowPrivate bool) *UpstreamClient {
	return &UpstreamClient{
		httpClient: &http.Client{
			Transport: safehttp.NewTransport(allowPrivate),
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
			Timeout: 0, // see upstreamDialTimeout comment + relay loop's per-call ctx
		},
	}
}

// SendUpstreamRequest sends a fully-built upstream *http.Request (already
// carrying its context, URL, body, and codec-specific headers from
// buildUpstreamBody/attemptOne) and returns the raw response. A non-nil
// error means a transport-level failure (network/timeout/SSRF-block) — HTTP
// status codes, including 5xx, come back as a non-nil response with nil
// error.
func (c *UpstreamClient) SendUpstreamRequest(req *http.Request) (*http.Response, error) {
	return c.httpClient.Do(req)
}
