package gateway

import (
	"net/http"
	"time"

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
//
// headerTimeout bounds the wait for the response headers (the reasoning-model
// "thinking" phase before the first byte) and is applied to the transport so
// it stays independent of the per-attempt context that later bounds the body.
// A zero headerTimeout disables the header-phase bound (kept explicit rather
// than silently defaulting, so the caller stays in control of the value).
//
// connectTimeout bounds each TCP dial to the upstream and is forwarded to the
// SSRF-safe transport's net.Dialer; it tracks config.GatewayConfig.
// ConnectTimeout so the admin-tuned value, not a hard-coded constant, governs
// the gateway path.
//
// tlsHandshake bounds the TLS handshake after the TCP connect and is forwarded
// to transport.TLSHandshakeTimeout; it tracks config.GatewayConfig.
// TLSHandshakeTimeout so the admin-tuned value governs the gateway path.
func NewUpstreamClient(allowPrivate bool, headerTimeout, connectTimeout, tlsHandshake time.Duration) *UpstreamClient {
	transport := safehttp.NewTransport(allowPrivate, connectTimeout, tlsHandshake)
	transport.ResponseHeaderTimeout = headerTimeout
	return &UpstreamClient{
		httpClient: &http.Client{
			Transport: transport,
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
			Timeout: 0, // per-attempt context in the relay loop bounds each call
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
