package service

import (
	"encoding/json"
	"fmt"
	"net/url"
	"sort"
	"strings"

	"github.com/yolorouter/yolorouter/internal/protocols"
)

// validProviderProtocols lists the wire protocols a provider can be
// configured to speak, either as its primary provider_type or as an extra
// entry in protocol_endpoints. Kept in sync with
// internal/gateway.primaryProtocol's switch cases.
var validProviderProtocols = map[string]bool{
	"openai":    true,
	"anthropic": true,
	"gemini":    true,
	"responses": true,
}

// ValidateProviderType normalizes and validates a provider's provider_type
// column. An empty string means "unset", normalized to "openai" for
// backward compatibility with providers created before this column existed.
func ValidateProviderType(t string) (string, error) {
	if t == "" {
		return "openai", nil
	}
	if !validProviderProtocols[t] {
		return "", fmt.Errorf("invalid provider_type: %s", t)
	}
	return t, nil
}

// ValidateProtocolEndpoints validates a provider's protocol_endpoints JSON
// text and returns it re-serialized with keys in sorted order, so equal
// configurations always produce byte-identical storage values (used for
// change detection). An empty string is valid and means "no extra
// protocols" — it round-trips to itself.
//
// raw must decode as a JSON object whose keys are wire protocol names
// (openai/anthropic/gemini/responses) and whose values are either an empty
// string (reuse the provider's base_url) or an absolute http(s) URL.
func ValidateProtocolEndpoints(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}

	var endpoints map[string]string
	if err := json.Unmarshal([]byte(raw), &endpoints); err != nil {
		return "", fmt.Errorf("invalid protocol_endpoints: %w", err)
	}

	for protocol, endpoint := range endpoints {
		if !validProviderProtocols[protocol] {
			return "", fmt.Errorf("invalid protocol_endpoints key: %s", protocol)
		}
		if endpoint == "" {
			continue
		}
		parsed, err := url.Parse(endpoint)
		if err != nil {
			return "", fmt.Errorf("invalid protocol_endpoints value for %s: %w", protocol, err)
		}
		if (parsed.Scheme != "http" && parsed.Scheme != "https") || parsed.Host == "" {
			return "", fmt.Errorf("invalid protocol_endpoints value for %s: %s is not an absolute http(s) URL", protocol, endpoint)
		}
	}

	return normalizeProtocolEndpoints(endpoints), nil
}

// normalizeProtocolEndpoints re-serializes a validated endpoints map with
// keys in sorted order, so callers can rely on the stored JSON text being
// stable for equal maps regardless of the order keys were supplied in.
//
// An empty map canonicalizes to "" (not "{}") so that "no additional
// protocols" has a single stored representation. Otherwise "" and "{}" would
// compare unequal, and a name-only edit whose UI re-submits the empty config
// as "" against a provider stored as "{}" (or vice versa) would spuriously
// bump destination_version and force every key to re-verify.
func normalizeProtocolEndpoints(endpoints map[string]string) string {
	if len(endpoints) == 0 {
		return ""
	}
	keys := make([]string, 0, len(endpoints))
	for k := range endpoints {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	var b strings.Builder
	b.WriteByte('{')
	for i, k := range keys {
		if i > 0 {
			b.WriteByte(',')
		}
		keyJSON, _ := json.Marshal(k)
		valueJSON, _ := json.Marshal(endpoints[k])
		b.Write(keyJSON)
		b.WriteByte(':')
		b.Write(valueJSON)
	}
	b.WriteByte('}')
	return b.String()
}

// SupportedProtocolSet is the single source of truth for which wire
// protocols a provider accepts on egress: its primary provider_type plus
// whatever extra protocols protocol_endpoints declares. Both negotiate
// (picking an egress protocol) and the provider read view (displaying
// supported protocols) must derive their answer from this function so they
// never drift apart.
//
// This is the read path, so it is deliberately lenient: an empty or
// malformed protocolEndpoints is treated as "no extra protocols" rather
// than an error — validation happens once at write time via
// ValidateProtocolEndpoints, not on every read.
func SupportedProtocolSet(providerType, protocolEndpoints string) map[string]bool {
	normalizedType := providerType
	if normalizedType == "" {
		normalizedType = "openai"
	}

	set := map[string]bool{normalizedType: true}

	if protocolEndpoints == "" {
		return set
	}
	var endpoints map[string]string
	if err := json.Unmarshal([]byte(protocolEndpoints), &endpoints); err != nil {
		return set
	}
	for protocol := range endpoints {
		set[protocol] = true
	}
	return set
}

// VerificationTarget is one (protocol, resolved-host) destination a provider
// key must be proven to work against before it can be authorized — mirrors
// gateway.egressBaseURL's resolution so verification covers exactly what
// negotiation will later route to.
type VerificationTarget struct {
	Proto protocols.ProtocolID
	URL   string
}

// VerificationTargets returns every routable destination for a provider: its
// primary protocol at baseURL, plus each protocol_endpoints entry at its
// resolved URL (the entry's non-empty value, else baseURL). Deterministic,
// stable order with the primary protocol first. Reuses the same lenient
// protocol_endpoints parse as SupportedProtocolSet (bad JSON -> just primary).
func VerificationTargets(providerType, protocolEndpoints, baseURL string) []VerificationTarget {
	normalizedType := providerType
	if normalizedType == "" {
		normalizedType = "openai"
	}

	var endpoints map[string]string
	if protocolEndpoints != "" {
		// Lenient on purpose, matching SupportedProtocolSet: malformed JSON
		// here just means "no extra protocols", not an error — validation
		// already happened once at write time via ValidateProtocolEndpoints.
		_ = json.Unmarshal([]byte(protocolEndpoints), &endpoints)
	}

	resolve := func(proto string) string {
		if url, ok := endpoints[proto]; ok && url != "" {
			return url
		}
		return baseURL
	}

	targets := []VerificationTarget{{Proto: protocols.ProtocolID(normalizedType), URL: resolve(normalizedType)}}

	extras := make([]string, 0, len(endpoints))
	for proto := range endpoints {
		if proto == normalizedType {
			continue
		}
		extras = append(extras, proto)
	}
	sort.Strings(extras)
	for _, proto := range extras {
		targets = append(targets, VerificationTarget{Proto: protocols.ProtocolID(proto), URL: resolve(proto)})
	}
	return targets
}
