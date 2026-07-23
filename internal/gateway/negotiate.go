package gateway

import (
	"encoding/json"
	"errors"
	"strings"

	"github.com/yolorouter/yolorouter/internal/model"
	"github.com/yolorouter/yolorouter/internal/protocols"
)

// geminiIngressPathPrefix is the fixed prefix of a native Gemini ingress
// path; the remainder is the {model}:{action} segment parseGeminiPath
// splits apart. Shared with ingress.go's parseGeminiPath so the prefix the
// two functions match against can't drift apart.
const geminiIngressPathPrefix = "/v1beta/models/"

// IngressProtocol maps the client-facing request path to the wire protocol
// the caller is speaking. Structured as a switch so later versions can add
// more ingress routes without reshaping the call sites; everything unmapped
// falls back to ProtocolOpenAI.
//
// Gemini's native path is parameterized by model
// (/v1beta/models/{model}:generateContent or :streamGenerateContent), so it
// can't be matched by an exact-string switch case like the other ingress
// routes -- it's checked separately, before the switch, by prefix + one of
// the two recognized action suffixes.
func IngressProtocol(requestPath string) protocols.ProtocolID {
	if strings.HasPrefix(requestPath, geminiIngressPathPrefix) &&
		(strings.HasSuffix(requestPath, ":generateContent") || strings.HasSuffix(requestPath, ":streamGenerateContent")) {
		return protocols.ProtocolGemini
	}
	switch requestPath {
	case "/v1/messages":
		return protocols.ProtocolClaude
	case "/v1/chat/completions":
		return protocols.ProtocolOpenAI
	case "/v1/responses":
		return protocols.ProtocolResponses
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

// parseProtocolEndpoints decodes a provider's protocol_endpoints column into
// a map of protocol name to per-protocol base URL. This is deliberately
// lenient: an empty or malformed value decodes to an empty map instead of
// returning an error, mirroring the read-time leniency of
// internal/service.SupportedProtocolSet (validation happens once at write
// time via internal/service.ValidateProtocolEndpoints). Kept as a local copy
// rather than importing internal/service, which would create an import
// cycle (internal/service already imports internal/gateway).
func parseProtocolEndpoints(protocolEndpoints string) map[string]string {
	if protocolEndpoints == "" {
		return nil
	}
	var endpoints map[string]string
	if err := json.Unmarshal([]byte(protocolEndpoints), &endpoints); err != nil {
		return nil
	}
	return endpoints
}

// providerSupportedProtocols returns the set of wire protocols a provider
// accepts on egress: its primary protocol (from provider_type) plus
// whatever extra protocols its protocol_endpoints column declares. An empty
// or malformed protocol_endpoints degrades to just the primary protocol
// rather than erroring, so negotiation always has a fallback.
func providerSupportedProtocols(p *model.Provider) map[protocols.ProtocolID]bool {
	set := map[protocols.ProtocolID]bool{primaryProtocol(p): true}
	for protocol := range parseProtocolEndpoints(p.ProtocolEndpoints) {
		set[protocols.ProtocolID(protocol)] = true
	}
	return set
}

// egressBaseURL returns the base URL to use when speaking proto to
// provider p: the per-protocol URL from protocol_endpoints if one is
// declared and non-empty, otherwise the provider's default base_url. This
// lets a provider expose independent upstream URLs per supported protocol
// (e.g. an OpenAI-compatible gateway that proxies Claude requests to a
// different host).
func egressBaseURL(p *model.Provider, proto protocols.ProtocolID) string {
	if url, ok := parseProtocolEndpoints(p.ProtocolEndpoints)[string(proto)]; ok && url != "" {
		return url
	}
	return p.BaseURL
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
		return &EgressDecision{Protocol: ingress, BaseURL: egressBaseURL(p, ingress), Passthrough: true}, nil
	}
	primary := primaryProtocol(p)
	return &EgressDecision{Protocol: primary, BaseURL: egressBaseURL(p, primary), Passthrough: false}, nil
}
