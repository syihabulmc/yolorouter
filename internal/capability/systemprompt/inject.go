package systemprompt

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/yolorouter/yolorouter/internal/fact"
	"github.com/yolorouter/yolorouter/internal/protocols"
	"github.com/yolorouter/yolorouter/internal/protocols/claude"
)

const systemPromptSep = "\n\n"

// joinSystemText appends the custom prompt to the original system text: when
// the original is empty the custom text is used directly (avoiding a leading
// blank line).
func joinSystemText(orig, custom string) string {
	if orig == "" {
		return custom
	}
	return orig + systemPromptSep + custom
}

// View is what this capability needs to know about the exchange: whether a
// prompt was resolved for this request, and whether the caller's route is one
// where injecting into the system text means anything. Four getters, declared
// here rather than handed down, so the coupling is visible in the package that
// actually has it.
type View interface {
	CustomSystemPromptEnabled() bool
	CustomSystemPrompt() string
	IsChatEndpoint() bool
}

// Injector adds a configured system prompt to the outbound body.
type Injector struct{}

// New returns an injector. It is stateless; one instance serves every request.
func New() Injector { return Injector{} }

// Name identifies the injector in the audit trail.
func (Injector) Name() string { return "custom_system_prompt" }

// RewriteEgress injects the resolved prompt when one applies. It picks the
// format by egress protocol because the body has already been encoded for that
// protocol by the time it arrives here.
//
// A malformed body is returned unchanged rather than reported as an error: the
// request may still be valid for an upstream that is more lenient than our
// parser, and rewriting a body we could not parse is the one outcome worse than
// not injecting at all.
func (Injector) RewriteEgress(_ context.Context, view View, egressProto protocols.ProtocolID, body []byte, sink fact.Sink) ([]byte, error) {
	if len(body) == 0 || !view.CustomSystemPromptEnabled() || view.CustomSystemPrompt() == "" {
		return body, nil
	}
	if !view.IsChatEndpoint() {
		return body, nil
	}
	prompt := view.CustomSystemPrompt()
	var out []byte
	switch egressProto {
	case protocols.ProtocolClaude:
		out = claude.InjectCustomSystemPromptOnly(body, prompt)
	case protocols.ProtocolOpenAI:
		out = injectOpenAI(body, prompt)
	case protocols.ProtocolResponses:
		out = injectResponses(body, prompt)
	case protocols.ProtocolGemini:
		out = injectGemini(body, prompt)
	default:
		return body, nil
	}
	if !changed(body, out) {
		return body, nil
	}
	sink.Note(fact.SystemPromptInjected{
		Site:       string(egressProto),
		ExtraChars: len(prompt),
	})
	return out, nil
}

// decodeObjectStrict decodes one JSON object, rejecting null / non-object /
// trailing content. Returns a nil map (and the caller returns the body
// unchanged) when the body is malformed, so a malformed request is preserved
// instead of silently rewritten.
func decodeObjectStrict(body []byte) (map[string]interface{}, bool) {
	if len(body) == 0 {
		return nil, false
	}
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.UseNumber()
	var m map[string]interface{}
	if err := dec.Decode(&m); err != nil || m == nil {
		return nil, false
	}
	if claude.JSONHasTrailingContent(dec) {
		return nil, false
	}
	return m, true
}

// mutateThenEncode decodes one JSON object, hands it to mutate, and re-encodes
// the result. If decode fails or mutate returns false, the original body is
// returned unchanged so a malformed request is never silently rewritten. If
// re-encoding fails the original body is returned too.
func mutateThenEncode(body []byte, mutate func(map[string]interface{}) bool) []byte {
	m, ok := decodeObjectStrict(body)
	if !ok {
		return body
	}
	if !mutate(m) {
		return body
	}
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

// findLastSystemLikeIndex returns the index of the last item whose role is
// "system" or "developer", or -1 when none exists. Used by both the OpenAI
// messages[] and Responses input[] shapes. Scans backward because system
// messages conventionally sit near the front of the list, so the last
// occurrence is found faster from the end.
func findLastSystemLikeIndex(items []interface{}) int {
	for i := len(items) - 1; i >= 0; i-- {
		mi, ok := items[i].(map[string]interface{})
		if !ok {
			continue
		}
		if role, _ := mi["role"].(string); role == "system" || role == "developer" {
			return i
		}
	}
	return -1
}

// appendSystemContent appends custom to an existing system/developer message's
// content in place. partType is the block type used for the appended text
// ("text" for OpenAI chat, "input_text" for Responses). It returns true when
// target was mutated; a false return tells the caller the content had an
// unexpected type and the caller should instead prepend a fresh system message
// (the default branch of the OpenAI/Responses content-type switch).
func appendSystemContent(target map[string]interface{}, custom, partType string) bool {
	switch content := target["content"].(type) {
	case string:
		target["content"] = joinSystemText(content, custom)
		return true
	case []interface{}:
		target["content"] = append(content, map[string]interface{}{"type": partType, "text": custom})
		return true
	}
	return false
}

// injectOpenAI appends to the last system/developer message, or prepends a new
// system message when none exists.
func injectOpenAI(body []byte, custom string) []byte {
	return mutateThenEncode(body, func(m map[string]interface{}) bool {
		msgs, ok := m["messages"].([]interface{})
		if !ok {
			return false
		}
		m["messages"] = injectIntoItemList(msgs, custom, "text")
		return true
	})
}

// injectIntoItemList adds the custom text to a role-tagged message list: append
// to the last system-like item when one exists and its content shape is
// appendable, otherwise prepend a fresh system message. Shared by every
// protocol whose request carries such a list — the lists differ only in field
// name and content-part type, which the caller supplies.
func injectIntoItemList(items []interface{}, custom, partType string) []interface{} {
	prepended := func() []interface{} {
		return append([]interface{}{newSystemMessage(custom)}, items...)
	}
	lastIdx := findLastSystemLikeIndex(items)
	if lastIdx == -1 {
		return prepended()
	}
	target := items[lastIdx].(map[string]interface{})
	if !appendSystemContent(target, custom, partType) {
		return prepended()
	}
	return items
}

// newSystemMessage is the one shape a fresh system entry takes, defined once so
// the three call sites cannot drift apart.
func newSystemMessage(custom string) map[string]interface{} {
	return map[string]interface{}{"role": "system", "content": custom}
}

// injectResponses prefers a top-level instructions field; otherwise appends to
// the last system/developer item in input[]. A JSON null instructions value is
// treated the same as an absent one (fall through to the input[] path) rather
// than failing injection.
func injectResponses(body []byte, custom string) []byte {
	return mutateThenEncode(body, func(m map[string]interface{}) bool {
		if instrVal, exists := m["instructions"]; exists && instrVal != nil {
			instr, ok := instrVal.(string)
			if !ok {
				return false
			}
			m["instructions"] = joinSystemText(instr, custom)
			return true
		}
		if input, ok := m["input"].([]interface{}); ok {
			if findLastSystemLikeIndex(input) != -1 {
				m["input"] = injectIntoItemList(input, custom, "input_text")
				return true
			}
		}
		m["instructions"] = custom
		return true
	})
}

// injectGemini appends to systemInstruction.parts, creating it when absent.
// A malformed systemInstruction/system_instruction that is present but not a
// JSON object, or whose parts is present but not a JSON array, is returned
// unchanged — never silently rewrite a malformed request. A JSON null
// systemInstruction (or null parts) is treated the same as absent and creates
// a fresh parts array.
func injectGemini(body []byte, custom string) []byte {
	return mutateThenEncode(body, func(m map[string]interface{}) bool {
		key := "systemInstruction"
		if _, has := m["system_instruction"]; has {
			key = "system_instruction"
		}
		raw, exists := m[key]
		if !exists || raw == nil {
			m[key] = map[string]interface{}{"parts": []interface{}{map[string]interface{}{"text": custom}}}
			return true
		}
		si, ok := raw.(map[string]interface{})
		if !ok {
			return false
		}
		partsRaw, hasParts := si["parts"]
		if !hasParts || partsRaw == nil {
			si["parts"] = []interface{}{map[string]interface{}{"text": custom}}
			return true
		}
		parts, ok := partsRaw.([]interface{})
		if !ok {
			return false
		}
		si["parts"] = append(parts, map[string]interface{}{"text": custom})
		return true
	})
}

// changed reports whether an injector actually produced a new body.
//
// The injectors return the input slice itself when they could not parse it, so
// identity is the precise test — but it has to be spelled carefully: indexing
// element zero to compare addresses panics on an empty slice, which is exactly
// what a malformed-body case hands us. Length is checked first because a real
// injection always adds text, making the address comparison a guard for the
// pathological equal-length case rather than the main test.
func changed(before, after []byte) bool {
	if len(after) != len(before) {
		return true
	}
	if len(after) == 0 {
		return false
	}
	return &after[0] != &before[0]
}
