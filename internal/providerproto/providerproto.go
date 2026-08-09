// Package providerproto owns the vocabulary a provider row uses to declare
// which wire protocols it speaks: the provider_type column and the
// protocol_endpoints JSON column.
//
// It is the single home for three facts that were once restated per caller
// and kept aligned by comments alone:
//   - the set of protocol names a provider may declare;
//   - the ""-means-openai default for rows that predate the provider_type
//     column;
//   - how a protocol_endpoints value resolves to the URL an attempt uses.
//
// The write path is strict (Validate* returns errors and canonicalizes) and
// the read path is lenient (TypeOf / ParseEndpoints degrade to the primary
// protocol on malformed input) — validation happens once when the row is
// written, never again on the hot path that reads it.
package providerproto

import (
	"encoding/json"
	"fmt"
	"net/url"
	"slices"
	"sort"
	"strings"

	"github.com/yolorouter/yolorouter/internal/protocols"
)

// supported is the set of wire protocols a provider can be configured to
// speak, either as its primary provider_type or as an extra entry in
// protocol_endpoints. The provider_type column stores the ProtocolID's own
// string value, so declaring the set in terms of the constants keeps the two
// vocabularies from drifting.
var supported = map[protocols.ProtocolID]bool{
	protocols.ProtocolOpenAI:    true,
	protocols.ProtocolClaude:    true,
	protocols.ProtocolGemini:    true,
	protocols.ProtocolResponses: true,
}

// All returns the supported protocols in stable sorted order, for callers
// that enumerate the vocabulary (registry-completeness checks, UI lists).
func All() []protocols.ProtocolID {
	out := make([]protocols.ProtocolID, 0, len(supported))
	for p := range supported {
		out = append(out, p)
	}
	slices.Sort(out)
	return out
}

// TypeOf is the read-path mapping from the provider_type column to the wire
// protocol it names. An empty value (rows that predate the column) and an
// unrecognized one both normalize to OpenAI — reads never fail, they fall
// back to the protocol every provider historically spoke.
func TypeOf(providerType string) protocols.ProtocolID {
	if supported[protocols.ProtocolID(providerType)] {
		return protocols.ProtocolID(providerType)
	}
	return protocols.ProtocolOpenAI
}

// ValidateType is the write-path mapping: "" normalizes to OpenAI for
// backward compatibility, anything else must name a supported protocol.
func ValidateType(providerType string) (protocols.ProtocolID, error) {
	if providerType == "" {
		return protocols.ProtocolOpenAI, nil
	}
	if !supported[protocols.ProtocolID(providerType)] {
		return "", fmt.Errorf("invalid provider_type: %s", providerType)
	}
	return protocols.ProtocolID(providerType), nil
}

// ParseEndpoints decodes a protocol_endpoints column into a map of protocol
// name to per-protocol base URL. Deliberately lenient: an empty or malformed
// value decodes to nil instead of an error, because validation happened once
// at write time via ValidateEndpoints and a read must always have a usable
// answer.
func ParseEndpoints(raw string) map[string]string {
	if raw == "" {
		return nil
	}
	var endpoints map[string]string
	if err := json.Unmarshal([]byte(raw), &endpoints); err != nil {
		return nil
	}
	return endpoints
}

// ValidateEndpoints validates a protocol_endpoints JSON text and returns it
// re-serialized with keys in sorted order, so equal configurations always
// produce byte-identical storage values (used for change detection). An
// empty string is valid and means "no extra protocols" — it round-trips to
// itself.
//
// raw must decode as a JSON object whose keys are supported protocol names
// and whose values are either an empty string (reuse the provider's
// base_url) or an absolute http(s) URL.
func ValidateEndpoints(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}

	var endpoints map[string]string
	if err := json.Unmarshal([]byte(raw), &endpoints); err != nil {
		return "", fmt.Errorf("invalid protocol_endpoints: %w", err)
	}

	for protocol, endpoint := range endpoints {
		if !supported[protocols.ProtocolID(protocol)] {
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

	return normalizeEndpoints(endpoints), nil
}

// normalizeEndpoints re-serializes a validated endpoints map with keys in
// sorted order, so callers can rely on the stored JSON text being stable for
// equal maps regardless of the order keys were supplied in.
//
// An empty map canonicalizes to "" (not "{}") so that "no additional
// protocols" has a single stored representation. Otherwise "" and "{}" would
// compare unequal, and a name-only edit whose UI re-submits the empty config
// as "" against a provider stored as "{}" (or vice versa) would spuriously
// bump destination_version and force every key to re-verify.
func normalizeEndpoints(endpoints map[string]string) string {
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

// SupportedSet returns every wire protocol a provider accepts on egress: its
// primary protocol (from provider_type) plus whatever extra protocols its
// protocol_endpoints column declares. Lenient like every read here — a
// malformed protocol_endpoints degrades to just the primary protocol, so a
// caller always has a routable answer.
func SupportedSet(providerType, protocolEndpoints string) map[protocols.ProtocolID]bool {
	set := map[protocols.ProtocolID]bool{TypeOf(providerType): true}
	for protocol := range ParseEndpoints(protocolEndpoints) {
		set[protocols.ProtocolID(protocol)] = true
	}
	return set
}

// ResolveURL returns the base URL to use when speaking proto against a
// provider whose protocol_endpoints decoded to endpoints: the per-protocol
// URL when one is declared and non-empty, otherwise baseURL. This is the one
// rule that lets a provider expose independent upstream URLs per supported
// protocol.
func ResolveURL(endpoints map[string]string, proto protocols.ProtocolID, baseURL string) string {
	if u, ok := endpoints[string(proto)]; ok && u != "" {
		return u
	}
	return baseURL
}

// VerificationTarget is one (protocol, resolved-host) destination a provider
// key must be proven to work against before it can be authorized — the same
// resolution ResolveURL applies at egress time, so verification covers
// exactly what negotiation will later route to.
type VerificationTarget struct {
	Proto protocols.ProtocolID
	URL   string
}

// VerificationTargets returns every routable destination for a provider: its
// primary protocol at baseURL, plus each protocol_endpoints entry at its
// resolved URL. Deterministic, stable order with the primary protocol first.
func VerificationTargets(providerType, protocolEndpoints, baseURL string) []VerificationTarget {
	primary := TypeOf(providerType)
	endpoints := ParseEndpoints(protocolEndpoints)

	targets := []VerificationTarget{{Proto: primary, URL: ResolveURL(endpoints, primary, baseURL)}}

	extras := make([]string, 0, len(endpoints))
	for proto := range endpoints {
		if protocols.ProtocolID(proto) == primary {
			continue
		}
		extras = append(extras, proto)
	}
	sort.Strings(extras)
	for _, proto := range extras {
		id := protocols.ProtocolID(proto)
		targets = append(targets, VerificationTarget{Proto: id, URL: ResolveURL(endpoints, id, baseURL)})
	}
	return targets
}
