package gateway

import (
	"errors"

	"github.com/yolorouter/yolorouter/internal/model"
	"github.com/yolorouter/yolorouter/internal/protocols"
)

// IngressProtocol maps the client-facing request path to the wire protocol
// the caller is speaking. Structured as a switch so later versions can add
// more ingress routes (e.g. "/v1/messages" -> ProtocolClaude) without
// reshaping the call sites; everything unmapped falls back to
// ProtocolOpenAI, which is the only ingress route this version serves.
func IngressProtocol(requestPath string) protocols.ProtocolID {
	switch requestPath {
	case "/v1/chat/completions":
		return protocols.ProtocolOpenAI
	default:
		return protocols.ProtocolOpenAI
	}
}

// primaryProtocol returns the protocol a provider natively speaks, derived
// from its provider_type column. An empty or unrecognized provider_type is
// treated as openai.
func primaryProtocol(p *model.Provider) protocols.ProtocolID {
	switch p.ProviderType {
	case "openai":
		return protocols.ProtocolOpenAI
	case "anthropic":
		return protocols.ProtocolClaude
	case "gemini":
		return protocols.ProtocolGemini
	case "responses":
		return protocols.ProtocolResponses
	default:
		return protocols.ProtocolOpenAI
	}
}

// providerSupportedProtocols returns the set of wire protocols a provider
// accepts on egress. This version has no protocol_endpoints table, so a
// provider only supports its own primary protocol; multi-protocol egress
// (e.g. an OpenAI-compatible endpoint that also accepts Claude requests) is
// deferred to a later version.
func providerSupportedProtocols(p *model.Provider) map[protocols.ProtocolID]bool {
	return map[protocols.ProtocolID]bool{primaryProtocol(p): true}
}

// EgressDecision is the outcome of negotiating between the ingress protocol
// and a provider's supported protocols: which wire protocol to speak to the
// provider, and where to send it.
type EgressDecision struct {
	Protocol protocols.ProtocolID
	BaseURL  string
	// Passthrough is true when Protocol equals the ingress protocol (the
	// caller's body is forwarded with only the model field rewritten, no IR
	// round-trip), and false when the request falls back to the provider's
	// primary protocol (IR decode/encode required).
	Passthrough bool
}

// Negotiate picks the egress protocol for a request against a provider. If
// the provider supports the ingress protocol directly, the request passes
// through unchanged (no IR round-trip). Otherwise it falls back to the
// provider's primary protocol, which is always in its supported set, so
// this never fails to find a route.
func Negotiate(ingress protocols.ProtocolID, p *model.Provider) (*EgressDecision, error) {
	if p == nil {
		return nil, errors.New("gateway: negotiate requires a non-nil provider")
	}

	if providerSupportedProtocols(p)[ingress] {
		return &EgressDecision{Protocol: ingress, BaseURL: p.BaseURL, Passthrough: true}, nil
	}
	return &EgressDecision{Protocol: primaryProtocol(p), BaseURL: p.BaseURL, Passthrough: false}, nil
}
