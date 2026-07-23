package gateway

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strings"

	"github.com/yolorouter/yolorouter/internal/protocols"
)

// ingressMeta is the gateway's one-pass view of a caller request, extracted
// by peekIngress before any candidate is chosen: just enough to route
// (Model, Stream) and to filter/validate (WantsStreamUsage, HasTools). It
// carries a small amount of unexported per-protocol bookkeeping so validate()
// can enforce protocol-specific invariants without a second body parse.
type ingressMeta struct {
	Model            string
	Stream           bool
	WantsStreamUsage bool
	HasTools         bool

	// protocol records which peek* function built this meta, so validate()
	// knows which protocol-specific invariants to check without re-deriving
	// it from which of the fields below happen to be non-zero (several of
	// them, e.g. claudeMessageCount and geminiContentCount, are legitimately
	// 0 for a protocol they don't apply to).
	protocol protocols.ProtocolID

	// openai retains the full parsed OpenAI request so validate() can reuse
	// parsedRequest.validate() verbatim instead of duplicating its rules.
	openai *parsedRequest

	// Claude-only bookkeeping: the decoder itself (claude.RequestDecoder) does
	// not reject an empty messages array, a missing max_tokens, or a
	// present-but-non-positive max_tokens, so validate() checks these
	// top-level invariants itself using the fields below. claudeMaxTokens is
	// the parsed value (nil = the field was absent) so validate() can tell
	// "absent" apart from "present but <= 0".
	claudeMessageCount int
	claudeMaxTokens    *int

	// Gemini-only bookkeeping: gemini.RequestDecoder does not reject an
	// empty/absent contents array, so validate() checks it itself using the
	// count peekGeminiIngress recorded from the top-level parse.
	geminiContentCount int

	// Responses-only bookkeeping: responses.RequestDecoder treats an
	// empty/absent input as "zero messages" rather than an error, so
	// validate() checks presence itself using the flag peekResponsesIngress
	// recorded from the top-level parse.
	responsesHasInput bool
}

// validate checks the structural invariants the gateway cares about before
// picking a candidate: messages must be non-empty for every protocol, plus
// whatever else that protocol's decoder is lenient about at the top level
// (for Claude, max_tokens must be present). This is a lightweight pre-check;
// the full body structure (message content shape, tool schema, ...) is
// checked once by validateIngressBody after a candidate model is resolved.
func (m *ingressMeta) validate() error {
	switch m.protocol {
	case protocols.ProtocolClaude:
		if m.claudeMessageCount == 0 {
			return fmt.Errorf("messages must be a non-empty array")
		}
		if m.claudeMaxTokens == nil {
			return fmt.Errorf("max_tokens is required")
		}
		if *m.claudeMaxTokens <= 0 {
			return fmt.Errorf("max_tokens must be a positive integer")
		}
		return nil
	case protocols.ProtocolGemini:
		if m.geminiContentCount == 0 {
			return fmt.Errorf("contents must be a non-empty array")
		}
		return nil
	case protocols.ProtocolResponses:
		if !m.responsesHasInput {
			return fmt.Errorf("input is required")
		}
		return nil
	default:
		return m.openai.validate()
	}
}

// peekIngress extracts routing/filtering metadata from a caller body without
// doing the full IR decode. OpenAI reuses parseRequest (the same parse the
// OpenAI hot path already does); Claude and Responses do their own
// lightweight top-level parse of their own request shape (model/stream/
// messages-or-input/tools/...) since their wire formats differ from
// OpenAI's. Gemini carries neither model nor stream in the body at all --
// both come from the URL path (see parseGeminiPath) -- so pathModel/
// pathStream supply them; every other protocol ignores these two
// parameters and reads model/stream from the body instead.
func peekIngress(ingress protocols.ProtocolID, body []byte, pathModel string, pathStream bool) (*ingressMeta, error) {
	switch ingress {
	case protocols.ProtocolClaude:
		return peekClaudeIngress(body)
	case protocols.ProtocolGemini:
		return peekGeminiIngress(body, pathModel, pathStream)
	case protocols.ProtocolResponses:
		return peekResponsesIngress(body)
	default:
		return peekOpenAIIngress(body)
	}
}

func peekOpenAIIngress(body []byte) (*ingressMeta, error) {
	p, err := parseRequest(body)
	if err != nil {
		return nil, err
	}
	return &ingressMeta{
		protocol:         protocols.ProtocolOpenAI,
		Model:            p.Model,
		Stream:           p.Stream,
		WantsStreamUsage: p.WantsStreamUsage,
		HasTools:         p.hasTools(),
		openai:           p,
	}, nil
}

// claudeIngressWire is the top-level shape peekIngress reads from a Claude
// Messages API request. MaxTokens is a pointer purely to distinguish
// "absent" from "present with value 0" for the max_tokens-required check;
// the actual numeric value (and everything else) is re-decoded in full by
// validateIngressBody via claude.RequestDecoder.
type claudeIngressWire struct {
	Model     string          `json:"model"`
	Stream    bool            `json:"stream"`
	Messages  json.RawMessage `json:"messages"`
	Tools     json.RawMessage `json:"tools"`
	MaxTokens *int            `json:"max_tokens"`
}

func peekClaudeIngress(body []byte) (*ingressMeta, error) {
	var raw claudeIngressWire
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse claude request: %w", err)
	}

	var messages []json.RawMessage
	if len(raw.Messages) > 0 && string(raw.Messages) != "null" {
		if err := json.Unmarshal(raw.Messages, &messages); err != nil {
			return nil, fmt.Errorf("messages must be an array: %w", err)
		}
	}

	hasTools := false
	if len(raw.Tools) > 0 && string(raw.Tools) != "null" {
		var tools []json.RawMessage
		if err := json.Unmarshal(raw.Tools, &tools); err != nil {
			return nil, fmt.Errorf("tools must be an array: %w", err)
		}
		hasTools = len(tools) > 0
	}

	return &ingressMeta{
		protocol: protocols.ProtocolClaude,
		Model:    raw.Model,
		Stream:   raw.Stream,
		// Claude streaming always carries usage in message_delta/message_stop;
		// unlike OpenAI there is no caller opt-in equivalent to
		// stream_options.include_usage, so this is unconditionally true.
		WantsStreamUsage:   true,
		HasTools:           hasTools,
		claudeMessageCount: len(messages),
		claudeMaxTokens:    raw.MaxTokens,
	}, nil
}

// geminiIngressWire is the top-level shape peekGeminiIngress reads from a
// native Gemini generateContent/streamGenerateContent request body. Unlike
// every other ingress protocol, Gemini carries neither model nor stream in
// the body -- both are encoded in the URL path (see parseGeminiPath) -- so
// this only needs the fields the path can't supply.
type geminiIngressWire struct {
	Contents json.RawMessage `json:"contents"`
	Tools    json.RawMessage `json:"tools"`
}

// peekGeminiIngress parses the top-level shape of a Gemini request body.
// model and isStream are NOT read from the body (Gemini has no such fields
// on the wire) -- they come from pathModel/pathStream, extracted from the
// URL by parseGeminiPath in Handle before this is called.
func peekGeminiIngress(body []byte, pathModel string, pathStream bool) (*ingressMeta, error) {
	var raw geminiIngressWire
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse gemini request: %w", err)
	}

	var contents []json.RawMessage
	if len(raw.Contents) > 0 && string(raw.Contents) != "null" {
		if err := json.Unmarshal(raw.Contents, &contents); err != nil {
			return nil, fmt.Errorf("contents must be an array: %w", err)
		}
	}

	hasTools := false
	if len(raw.Tools) > 0 && string(raw.Tools) != "null" {
		var tools []json.RawMessage
		if err := json.Unmarshal(raw.Tools, &tools); err != nil {
			return nil, fmt.Errorf("tools must be an array: %w", err)
		}
		hasTools = len(tools) > 0
	}

	return &ingressMeta{
		protocol: protocols.ProtocolGemini,
		Model:    pathModel,
		Stream:   pathStream,
		// Gemini's usageMetadata is unconditionally attached to the final SSE
		// chunk of a streamGenerateContent response (and to every
		// non-streaming response); there is no caller opt-in field on the
		// wire (unlike OpenAI's stream_options.include_usage) that gates it,
		// so this is unconditionally true, mirroring gemini.RequestDecoder's
		// own IRStreamConfig.IncludeUsage:true.
		WantsStreamUsage:   true,
		HasTools:           hasTools,
		geminiContentCount: len(contents),
	}, nil
}

// responsesIngressWire is the top-level shape peekResponsesIngress reads
// from an OpenAI Responses API request body. Unlike Gemini, Responses
// carries model and stream in the body itself, same as OpenAI Chat and
// Claude.
type responsesIngressWire struct {
	Model  string          `json:"model"`
	Stream bool            `json:"stream"`
	Input  json.RawMessage `json:"input"`
	Tools  json.RawMessage `json:"tools"`
}

// peekResponsesIngress parses the top-level shape of a Responses API
// request body.
func peekResponsesIngress(body []byte) (*ingressMeta, error) {
	var raw responsesIngressWire
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("parse responses request: %w", err)
	}

	hasTools := false
	if len(raw.Tools) > 0 && string(raw.Tools) != "null" {
		var tools []json.RawMessage
		if err := json.Unmarshal(raw.Tools, &tools); err != nil {
			return nil, fmt.Errorf("tools must be an array: %w", err)
		}
		hasTools = len(tools) > 0
	}

	return &ingressMeta{
		protocol: protocols.ProtocolResponses,
		Model:    raw.Model,
		Stream:   raw.Stream,
		// responses.StreamEncoder (internal/protocols/responses/encoder.go)
		// unconditionally attaches usage to the response.completed event via
		// its own accumulated e.usage -- there is no caller opt-in request
		// field (no Responses equivalent of OpenAI Chat's
		// stream_options.include_usage) gating it, so this is
		// unconditionally true, same as Claude and Gemini.
		WantsStreamUsage:  true,
		HasTools:          hasTools,
		responsesHasInput: len(raw.Input) > 0 && string(raw.Input) != "null",
	}, nil
}

// validateIngressBody runs the full protocol-specific structural decode on a
// caller body -- the same decode the request will eventually go through on
// the hot path -- and returns its error, if any. Call this once (after model
// lookup, before the candidate loop) with a placeholder externalModel; a
// successful decode means the body is structurally valid for its ingress
// protocol (message content shapes, tool schemas, max_tokens>0, etc.), so a
// malformed body is rejected here as a client error instead of surfacing
// later as an upstream failure once a candidate has already been picked.
func validateIngressBody(ingress protocols.ProtocolID, body []byte, externalModel string, isStream bool) error {
	_, err := codecsFor(ingress).RequestDecoder.DecodeRequest(json.RawMessage(body), externalModel, isStream)
	return err
}

// parseGeminiPath extracts the model name and streaming flag from a native
// Gemini ingress path, e.g.
// "/v1beta/models/gemini-2.0-flash:generateContent". The model is split
// from the action on the LAST colon (a model name itself never contains a
// colon, but splitting on the last one is the robust choice regardless) and
// URL-decoded, since a model name may itself be percent-encoded. ok is false
// for anything that isn't exactly /v1beta/models/{model}:{action} with a
// recognized action and a non-empty model: a path missing the prefix, a
// segment with no colon, an unknown action, or an empty (or
// empty-after-decoding) model.
//
// Limitation: a model name containing a "/" (e.g. a tuned model's
// "tunedModels/xyz" resource name, which the real Gemini API would expect
// percent-encoded as "tunedModels%2Fxyz" in the path segment) is NOT
// supported end to end, even though this function itself would happily
// url.PathUnescape one if handed it directly. In practice it never is: this
// is only ever called with c.Request.URL.Path (relay.go), and net/http
// percent-decodes "%2F" into a literal "/" in URL.Path before gin ever
// routes the request (gin's default matches against the decoded Path, not
// RawPath). The router's ":modelaction" param matches exactly one
// "/"-delimited segment (internal/router/router.go), so a request for a
// slash-containing model 404s at the routing layer and never reaches this
// function at all. Standard (non-tuned) Gemini model names never contain a
// slash, so this is not a practical gap for this version; see
// TestGeminiRouteWithSlashInModelSegmentDoesNotRoute
// (internal/router/router_test.go) for the routing-layer proof.
func parseGeminiPath(path string) (model string, stream bool, ok bool) {
	rest, matched := strings.CutPrefix(path, geminiIngressPathPrefix)
	if !matched {
		return "", false, false
	}

	idx := strings.LastIndex(rest, ":")
	if idx < 0 {
		return "", false, false
	}
	encodedModel, action := rest[:idx], rest[idx+1:]

	switch action {
	case "generateContent":
		stream = false
	case "streamGenerateContent":
		stream = true
	default:
		return "", false, false
	}

	if encodedModel == "" {
		return "", false, false
	}
	decodedModel, err := url.PathUnescape(encodedModel)
	if err != nil || decodedModel == "" {
		return "", false, false
	}

	return decodedModel, stream, true
}
