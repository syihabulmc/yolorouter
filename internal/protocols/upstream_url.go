package protocols

import (
	"net/url"
	"strings"
)

// EndsWithVersionSegment reports whether the **last segment** of rawURL's
// path is a real version token (v1 / v1beta / v2 / v4 / ..., i.e. "v"
// followed immediately by a digit).
//
// Important: this must NOT be approximated by "path length > 1 implies a
// version is present" — that weak check misclassifies a path prefix like
// https://gateway.example.com/openai (not a version) as versioned, which
// then causes URL joining to skip appending /v1 and produces a 404.
func EndsWithVersionSegment(rawURL string) bool {
	_, ok := trailingVersionSegment(rawURL)
	return ok
}

// trailingVersionSegment returns the last path segment of rawURL (with any
// trailing slash trimmed). ok is true when that segment is a version token
// (a "v" immediately followed by a digit, e.g. v1 / v1beta / v4); otherwise
// it returns "", false. Both EndsWithVersionSegment and the Gemini /v1beta
// check share this helper so the two never drift apart.
func trailingVersionSegment(rawURL string) (string, bool) {
	u, err := url.Parse(rawURL)
	if err != nil {
		return "", false
	}
	path := strings.TrimRight(u.Path, "/")
	if path == "" {
		return "", false
	}
	last := path[strings.LastIndex(path, "/")+1:]
	if len(last) >= 2 && last[0] == 'v' && last[1] >= '0' && last[1] <= '9' {
		return last, true
	}
	return "", false
}

// JoinUpstreamURL joins a base URL with the egressPath produced by a
// codec/handler, automatically handling whether the base's last path
// segment already carries a version token (avoiding a duplicated or
// missing /v1). This is the single authoritative place in the codebase
// that performs this join.
//
// Per-protocol EgressPath conventions:
//   - chat (ProtocolOpenAI):             "/v1/chat/completions"
//   - responses (ProtocolResponses):     "/responses" (no /v1 prefix)
//   - claude (ProtocolClaude):           "/v1/messages"
//   - gemini (ProtocolGemini):           "/models/X:generateContent" (no /v1beta prefix)
//
// Rules:
//   - claude egress: if the base's last segment is a version token, append
//     /messages directly; otherwise append /v1/messages.
//   - openai egress: if the base's last segment is a version token, strip
//     the /v1 prefix from egressPath before appending; otherwise append /v1.
//   - responses egress: cross-protocol callers pass the bare /responses;
//     same-protocol passthrough (e.g. a Codex CLI client hitting
//     /v1/responses directly) forwards the client's original path, which
//     still carries /v1. Strip that /v1 prefix first, then apply the same
//     policy as openai: append directly if the base already has a version
//     token, otherwise prepend /v1.
//   - gemini egress: last segment /v1beta -> append /models/... directly;
//     /v1 -> replaced with /v1beta; any other explicit version token
//     (v2/v4/...) -> appended as-is, respecting the explicit configuration;
//     no version token -> prepend /v1beta (generateContent is only served
//     under /v1beta).
func JoinUpstreamURL(base, egressPath string, proto ProtocolID) string {
	// A base URL ending in "/" is an allowed config shape (the key-test path
	// trims it the same way); trim it uniformly here so every branch below
	// joins on a clean base instead of producing a doubled "//".
	base = strings.TrimRight(base, "/")
	hasVer := EndsWithVersionSegment(base)
	switch proto {
	case ProtocolClaude:
		if hasVer {
			return base + strings.TrimPrefix(egressPath, "/v1")
		}
		return base + egressPath
	case ProtocolOpenAI:
		tail := strings.TrimPrefix(egressPath, "/v1")
		if hasVer {
			return base + tail
		}
		return base + "/v1" + tail
	case ProtocolResponses:
		// Cross-protocol egress supplies the bare /responses path; same-protocol
		// passthrough (e.g. a Codex CLI client hitting /v1/responses directly)
		// forwards the client's original path unchanged, including the /v1
		// prefix. Strip it uniformly so it doesn't stack with the base's own
		// /v1 segment and produce /v1/v1/responses.
		tail := strings.TrimPrefix(egressPath, "/v1")
		if hasVer {
			return base + tail
		}
		return base + "/v1" + tail
	case ProtocolGemini:
		// Native Gemini generateContent is only served under /v1beta. Rules
		// (TrimRight first so a trailing slash on base doesn't produce "//"):
		//   - last segment /v1beta -> append directly (standard Google endpoint)
		//   - last segment /v1     -> replaced with /v1beta (typical case: the
		//     provider has no dedicated Gemini endpoint and falls back to the
		//     chat endpoint's /v1; appending as-is would yield /v1/models/X,
		//     which hits a non-Gemini route and resolves to an empty model name)
		//   - any other explicit version segment (v2/v4/v10/...) -> appended
		//     as-is, respecting the user's explicit configuration; never
		//     silently rewrite the route
		//   - no version segment -> prepend /v1beta
		if seg, ok := trailingVersionSegment(base); ok {
			if seg == "v1" {
				return strings.TrimSuffix(base, "/v1") + "/v1beta" + egressPath
			}
			return base + egressPath
		}
		return base + "/v1beta" + egressPath
	}
	return base + egressPath
}
