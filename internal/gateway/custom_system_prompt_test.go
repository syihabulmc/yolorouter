package gateway

import (
	"strings"
	"testing"

	"github.com/yolorouter/yolorouter/internal/protocols"
)

func TestIsChatEndpointAllowlist(t *testing.T) {
	cases := []struct {
		path string
		want bool
	}{
		{"/v1/chat/completions", true},
		{"/v1/messages", true},
		{"/v1/responses", true},
		{"/v1beta/models/gemini-pro:generateContent", true},
		{"/v1beta/models/gemini-pro:streamGenerateContent", true},
		{"/v1beta/models/gemini-pro:countTokens", false},
		{"/v1beta/models/gemini-pro:embedContent", false},
		{"/v1/embeddings", false},
		{"/v1/completions", false},
	}
	for _, c := range cases {
		if got := IsChatEndpoint(c.path); got != c.want {
			t.Errorf("IsChatEndpoint(%q) = %v, want %v", c.path, got, c.want)
		}
	}
}

func TestApplyCustomSystemPromptDisabledNoOp(t *testing.T) {
	rc := &RelayContext{IngressPath: "/v1/chat/completions"}
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	out := applyCustomSystemPrompt(rc, protocols.ProtocolOpenAI, body)
	if string(out) != string(body) {
		t.Fatal("disabled prompt must return body unchanged")
	}
}

func TestApplyCustomSystemPromptChatAppendsToSystem(t *testing.T) {
	rc := &RelayContext{
		IngressPath:               "/v1/chat/completions",
		CustomSystemPromptEnabled: true,
		CustomSystemPrompt:        "BE CONCISE",
	}
	body := []byte(`{"messages":[{"role":"system","content":"You are helpful."},{"role":"user","content":"hi"}]}`)
	out := applyCustomSystemPrompt(rc, protocols.ProtocolOpenAI, body)
	if !contains(out, "BE CONCISE") || !contains(out, "You are helpful.") {
		t.Fatalf("expected both original and custom text, got %s", out)
	}
}

func TestApplyCustomSystemPromptCountTokensSkipped(t *testing.T) {
	rc := &RelayContext{
		IngressPath:               "/v1beta/models/gemini-pro:countTokens",
		CustomSystemPromptEnabled: true,
		CustomSystemPrompt:        "X",
	}
	body := []byte(`{"models":["x"]}`)
	out := applyCustomSystemPrompt(rc, protocols.ProtocolOpenAI, body)
	if string(out) != string(body) {
		t.Fatal("countTokens path must not be injected")
	}
}

func TestApplyCustomSystemPromptMalformedBodyUnchanged(t *testing.T) {
	rc := &RelayContext{
		IngressPath:               "/v1/chat/completions",
		CustomSystemPromptEnabled: true,
		CustomSystemPrompt:        "X",
	}
	for _, b := range [][]byte{nil, []byte(``), []byte(`null`), []byte(`not json`), []byte(`{}`)} {
		out := applyCustomSystemPrompt(rc, protocols.ProtocolOpenAI, b)
		if string(out) != string(b) {
			t.Fatalf("malformed body must be unchanged: in=%q out=%q", b, out)
		}
	}
}

func contains(haystack []byte, needle string) bool {
	return strings.Contains(string(haystack), needle)
}
