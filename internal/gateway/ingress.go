package gateway

import (
	"encoding/json"
	"fmt"

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
}

// validate checks the structural invariants the gateway cares about before
// picking a candidate: messages must be non-empty for every protocol, plus
// whatever else that protocol's decoder is lenient about at the top level
// (for Claude, max_tokens must be present). This is a lightweight pre-check;
// the full body structure (message content shape, tool schema, ...) is
// checked once by validateIngressBody after a candidate model is resolved.
func (m *ingressMeta) validate() error {
	if m.openai != nil {
		return m.openai.validate()
	}
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
}

// peekIngress extracts routing/filtering metadata from a caller body without
// doing the full IR decode. OpenAI reuses parseRequest (the same parse the
// OpenAI hot path already does); Claude does its own lightweight top-level
// parse of the Messages API request shape (model/stream/messages/tools/
// max_tokens) since the Claude wire format differs from OpenAI's.
func peekIngress(ingress protocols.ProtocolID, body []byte) (*ingressMeta, error) {
	if ingress == protocols.ProtocolClaude {
		return peekClaudeIngress(body)
	}
	return peekOpenAIIngress(body)
}

func peekOpenAIIngress(body []byte) (*ingressMeta, error) {
	p, err := parseRequest(body)
	if err != nil {
		return nil, err
	}
	return &ingressMeta{
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
		Model:  raw.Model,
		Stream: raw.Stream,
		// Claude streaming always carries usage in message_delta/message_stop;
		// unlike OpenAI there is no caller opt-in equivalent to
		// stream_options.include_usage, so this is unconditionally true.
		WantsStreamUsage:   true,
		HasTools:           hasTools,
		claudeMessageCount: len(messages),
		claudeMaxTokens:    raw.MaxTokens,
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
