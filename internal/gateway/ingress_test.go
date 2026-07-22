package gateway

import (
	"testing"

	"github.com/yolorouter/yolorouter/internal/protocols"
)

func TestIngressProtocol(t *testing.T) {
	if got := IngressProtocol("/v1/chat/completions"); got != protocols.ProtocolOpenAI {
		t.Errorf("IngressProtocol(/v1/chat/completions) = %v, want %v", got, protocols.ProtocolOpenAI)
	}
	if got := IngressProtocol("/v1/messages"); got != protocols.ProtocolClaude {
		t.Errorf("IngressProtocol(/v1/messages) = %v, want %v", got, protocols.ProtocolClaude)
	}
}

func TestPeekIngressOpenAI(t *testing.T) {
	body := []byte(`{
		"model":"gpt-4o",
		"stream":true,
		"stream_options":{"include_usage":true},
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"type":"function"}]
	}`)
	meta, err := peekIngress(protocols.ProtocolOpenAI, body)
	if err != nil {
		t.Fatalf("peekIngress: %v", err)
	}
	if meta.Model != "gpt-4o" {
		t.Errorf("Model = %q, want gpt-4o", meta.Model)
	}
	if !meta.Stream {
		t.Errorf("Stream = false, want true")
	}
	if !meta.WantsStreamUsage {
		t.Errorf("WantsStreamUsage = false, want true (caller opted in via stream_options.include_usage)")
	}
	if !meta.HasTools {
		t.Errorf("HasTools = false, want true")
	}
	if err := meta.validate(); err != nil {
		t.Errorf("validate() = %v, want nil", err)
	}
}

func TestPeekIngressOpenAINoStreamUsageOptIn(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	meta, err := peekIngress(protocols.ProtocolOpenAI, body)
	if err != nil {
		t.Fatalf("peekIngress: %v", err)
	}
	if meta.WantsStreamUsage {
		t.Errorf("WantsStreamUsage = true, want false (caller did not set stream_options.include_usage)")
	}
	if meta.HasTools {
		t.Errorf("HasTools = true, want false (no tools in body)")
	}
}

func TestPeekIngressOpenAIValidateEmptyMessages(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[]}`)
	meta, err := peekIngress(protocols.ProtocolOpenAI, body)
	if err != nil {
		t.Fatalf("peekIngress: %v", err)
	}
	if err := meta.validate(); err == nil {
		t.Errorf("validate() = nil, want error for empty messages array")
	}
}

func TestPeekIngressOpenAIValidateNonFunctionTool(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}],"tools":[{"type":"retrieval"}]}`)
	meta, err := peekIngress(protocols.ProtocolOpenAI, body)
	if err != nil {
		t.Fatalf("peekIngress: %v", err)
	}
	if err := meta.validate(); err == nil {
		t.Errorf("validate() = nil, want error for a non-function tool type")
	}
}

func TestPeekIngressClaude(t *testing.T) {
	body := []byte(`{
		"model":"claude-3-5-sonnet",
		"max_tokens":1024,
		"messages":[{"role":"user","content":"hi"}],
		"tools":[{"name":"get_weather"}]
	}`)
	meta, err := peekIngress(protocols.ProtocolClaude, body)
	if err != nil {
		t.Fatalf("peekIngress: %v", err)
	}
	if meta.Model != "claude-3-5-sonnet" {
		t.Errorf("Model = %q, want claude-3-5-sonnet", meta.Model)
	}
	if meta.Stream {
		t.Errorf("Stream = true, want false")
	}
	if !meta.WantsStreamUsage {
		t.Errorf("WantsStreamUsage = false, want true (Claude always carries usage, even for a non-stream request)")
	}
	if !meta.HasTools {
		t.Errorf("HasTools = false, want true")
	}
	if err := meta.validate(); err != nil {
		t.Errorf("validate() = %v, want nil", err)
	}
}

func TestPeekIngressClaudeStreamAlwaysWantsUsage(t *testing.T) {
	body := []byte(`{"model":"claude-3-5-sonnet","max_tokens":1024,"stream":true,"messages":[{"role":"user","content":"hi"}]}`)
	meta, err := peekIngress(protocols.ProtocolClaude, body)
	if err != nil {
		t.Fatalf("peekIngress: %v", err)
	}
	if !meta.Stream {
		t.Errorf("Stream = false, want true")
	}
	// Claude streaming always carries usage in message_delta/message_stop; there is no
	// caller opt-in equivalent to OpenAI's stream_options.include_usage.
	if !meta.WantsStreamUsage {
		t.Errorf("WantsStreamUsage must always be true for Claude ingress")
	}
	if meta.HasTools {
		t.Errorf("HasTools = true, want false (no tools in body)")
	}
}

func TestPeekIngressClaudeValidateMissingMessages(t *testing.T) {
	body := []byte(`{"model":"claude-3-5-sonnet","max_tokens":1024}`)
	meta, err := peekIngress(protocols.ProtocolClaude, body)
	if err != nil {
		t.Fatalf("peekIngress: %v", err)
	}
	if err := meta.validate(); err == nil {
		t.Errorf("validate() = nil, want error for missing messages")
	}
}

func TestPeekIngressClaudeValidateEmptyMessages(t *testing.T) {
	body := []byte(`{"model":"claude-3-5-sonnet","max_tokens":1024,"messages":[]}`)
	meta, err := peekIngress(protocols.ProtocolClaude, body)
	if err != nil {
		t.Fatalf("peekIngress: %v", err)
	}
	if err := meta.validate(); err == nil {
		t.Errorf("validate() = nil, want error for an empty messages array")
	}
}

func TestPeekIngressClaudeValidateMissingMaxTokens(t *testing.T) {
	body := []byte(`{"model":"claude-3-5-sonnet","messages":[{"role":"user","content":"hi"}]}`)
	meta, err := peekIngress(protocols.ProtocolClaude, body)
	if err != nil {
		t.Fatalf("peekIngress: %v", err)
	}
	if err := meta.validate(); err == nil {
		t.Errorf("validate() = nil, want error for missing max_tokens (Claude requires it)")
	}
}

func TestPeekIngressClaudeValidateNonPositiveMaxTokens(t *testing.T) {
	cases := map[string]string{
		"zero":     `{"model":"claude-3-5-sonnet","max_tokens":0,"messages":[{"role":"user","content":"hi"}]}`,
		"negative": `{"model":"claude-3-5-sonnet","max_tokens":-1,"messages":[{"role":"user","content":"hi"}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			meta, err := peekIngress(protocols.ProtocolClaude, []byte(body))
			if err != nil {
				t.Fatalf("peekIngress: %v", err)
			}
			if err := meta.validate(); err == nil {
				t.Errorf("validate() = nil, want error for max_tokens <= 0 (Anthropic requires max_tokens >= 1)")
			}
		})
	}
}

func TestValidateIngressBodyClaudeValid(t *testing.T) {
	body := []byte(`{"model":"claude-3-5-sonnet","max_tokens":1024,"messages":[{"role":"user","content":"hi"}]}`)
	if err := validateIngressBody(protocols.ProtocolClaude, body, "placeholder", false); err != nil {
		t.Errorf("validateIngressBody() = %v, want nil", err)
	}
}

func TestValidateIngressBodyClaudeContentObject(t *testing.T) {
	// content is neither a string nor an array of content blocks -- the
	// claude.RequestDecoder rejects this while decoding messages.
	body := []byte(`{"model":"claude-3-5-sonnet","max_tokens":1024,"messages":[{"role":"user","content":{"foo":"bar"}}]}`)
	if err := validateIngressBody(protocols.ProtocolClaude, body, "placeholder", false); err == nil {
		t.Errorf("validateIngressBody() = nil, want error for object-typed message content")
	}
}

func TestValidateIngressBodyClaudeMessagesNotArray(t *testing.T) {
	body := []byte(`{"model":"claude-3-5-sonnet","max_tokens":1024,"messages":{"role":"user"}}`)
	if err := validateIngressBody(protocols.ProtocolClaude, body, "placeholder", false); err == nil {
		t.Errorf("validateIngressBody() = nil, want error when messages is not an array")
	}
}

func TestValidateIngressBodyClaudeMalformedTools(t *testing.T) {
	cases := map[string]string{
		"tools not an array": `{"model":"m","max_tokens":100,"messages":[{"role":"user","content":"hi"}],"tools":"nope"}`,
		"tool item wrong type": `{"model":"m","max_tokens":100,"messages":[{"role":"user","content":"hi"}],
			"tools":[{"name":123}]}`,
	}
	for name, body := range cases {
		t.Run(name, func(t *testing.T) {
			if err := validateIngressBody(protocols.ProtocolClaude, []byte(body), "placeholder", false); err == nil {
				t.Errorf("validateIngressBody(%s) = nil, want error", name)
			}
		})
	}
}

// The claude.RequestDecoder itself does not require messages to be non-empty
// and does not require max_tokens to be present or positive: an empty
// messages array decodes to zero IR messages with no error, and a
// zero/negative max_tokens is silently left unset on the IR (no error
// either). These gaps are covered earlier by ingressMeta.validate() in
// peekIngress, which the gateway runs before it ever reaches
// validateIngressBody. This test documents the decoder's leniency (not a
// bug) so a future decoder change that starts rejecting these is visible.
func TestValidateIngressBodyClaudeLenientOnEmptyMessagesAndMaxTokens(t *testing.T) {
	emptyMessages := []byte(`{"model":"m","max_tokens":100,"messages":[]}`)
	if err := validateIngressBody(protocols.ProtocolClaude, emptyMessages, "placeholder", false); err != nil {
		t.Errorf("validateIngressBody() = %v, want nil (decoder does not reject empty messages)", err)
	}
	zeroMaxTokens := []byte(`{"model":"m","max_tokens":0,"messages":[{"role":"user","content":"hi"}]}`)
	if err := validateIngressBody(protocols.ProtocolClaude, zeroMaxTokens, "placeholder", false); err != nil {
		t.Errorf("validateIngressBody() = %v, want nil (decoder does not reject max_tokens<=0)", err)
	}
}

func TestValidateIngressBodyOpenAIValid(t *testing.T) {
	body := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)
	if err := validateIngressBody(protocols.ProtocolOpenAI, body, "placeholder", false); err != nil {
		t.Errorf("validateIngressBody() = %v, want nil", err)
	}
}
