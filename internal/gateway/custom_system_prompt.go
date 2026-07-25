package gateway

import (
	"bytes"
	"context"
	"encoding/json"

	"github.com/yolorouter/yolorouter/internal/protocols"
	"github.com/yolorouter/yolorouter/internal/protocols/claude"
	"github.com/yolorouter/yolorouter/internal/settings"
)

// SettingsProvider is the read-only window the gateway has into the cached
// global custom system prompt and input-compression switch. Implemented by
// the system settings service and injected into RelayService by the router.
// The gateway imports only the neutral settings DTO (not the service
// package), so there is no import cycle.
type SettingsProvider interface {
	CustomSystemPrompt(ctx context.Context) (settings.CustomSystemPromptSetting, int64, error)
	GetInputCompression(ctx context.Context) (bool, int64, error)
}

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

// applyCustomSystemPrompt injects the resolved prompt into the egress body
// when enabled, non-empty, and the ingress path is in the allowlist. It picks
// the injection format by egress protocol (the body is already egress-encoded).
// Malformed bodies are returned unchanged: never panic, never silently rewrite
// a malformed request.
func applyCustomSystemPrompt(rc *RelayContext, egressProto protocols.ProtocolID, body []byte) []byte {
	if body == nil || !rc.CustomSystemPromptEnabled || rc.CustomSystemPrompt == "" {
		return body
	}
	if !rc.IsChatEndpoint {
		return body
	}
	switch egressProto {
	case protocols.ProtocolClaude:
		return claude.InjectCustomSystemPromptOnly(body, rc.CustomSystemPrompt)
	case protocols.ProtocolOpenAI:
		return injectOpenAI(body, rc.CustomSystemPrompt)
	case protocols.ProtocolResponses:
		return injectResponses(body, rc.CustomSystemPrompt)
	case protocols.ProtocolGemini:
		return injectGemini(body, rc.CustomSystemPrompt)
	default:
		return body
	}
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
		lastIdx := findLastSystemLikeIndex(msgs)
		if lastIdx == -1 {
			m["messages"] = append([]interface{}{map[string]interface{}{"role": "system", "content": custom}}, msgs...)
			return true
		}
		target := msgs[lastIdx].(map[string]interface{})
		if !appendSystemContent(target, custom, "text") {
			m["messages"] = append([]interface{}{map[string]interface{}{"role": "system", "content": custom}}, msgs...)
		}
		return true
	})
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
			lastIdx := findLastSystemLikeIndex(input)
			if lastIdx != -1 {
				target := input[lastIdx].(map[string]interface{})
				if !appendSystemContent(target, custom, "input_text") {
					m["input"] = append([]interface{}{map[string]interface{}{"role": "system", "content": custom}}, input...)
				}
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
