package gateway

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"github.com/yolorouter/yolorouter/internal/model"
	"github.com/yolorouter/yolorouter/internal/protocols"
)

func TestBuildUpstreamBody_PassthroughOpenAIToOpenAI(t *testing.T) {
	s := &Service{}
	rc := &Exchange{
		originalModel: "gpt-4o",
		isStream:      false,
		candidate:     &model.ModelCandidate{ProviderModelName: "gpt-4o-2024-08-06"},
		requestBody:   []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`),
	}
	egress := &EgressDecision{Protocol: protocols.ProtocolOpenAI, BaseURL: "https://api.openai.com/v1", Passthrough: true}

	outBody, url, _, err := s.buildUpstreamBody(rc, protocols.ProtocolOpenAI, egress)
	if err != nil {
		t.Fatalf("buildUpstreamBody returned error: %v", err)
	}

	var parsed struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(outBody, &parsed); err != nil {
		t.Fatalf("outBody not valid JSON: %v", err)
	}
	if parsed.Model != "gpt-4o-2024-08-06" {
		t.Errorf("outBody model = %q, want %q", parsed.Model, "gpt-4o-2024-08-06")
	}

	if !strings.HasSuffix(url, "/chat/completions") {
		t.Errorf("url = %q, want suffix /chat/completions", url)
	}

	// SetupRequest (the per-key auth step attemptOne performs after
	// buildUpstreamBody) is exercised separately here, since it now happens
	// outside buildUpstreamBody.
	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	codecsFor(egress.Protocol).RequestEncoder.SetupRequest(req, "test-key")
	if got := req.Header.Get("Authorization"); got != "Bearer test-key" {
		t.Errorf("Authorization header = %q, want %q", got, "Bearer test-key")
	}
}

func TestBuildUpstreamBody_CrossProtocolOpenAIToAnthropic(t *testing.T) {
	s := &Service{}
	// NOTE: intentionally no "role":"system" message here. The P1 chat
	// RequestDecoder (internal/protocols/chat/request_decoder.go) decodes a
	// leading system-role message into a RoleSystem entry inside
	// IRRequest.Messages rather than into IRRequest.System, and the Claude
	// RequestEncoder's encodeClaudeMessages skips RoleSystem entries and only
	// emits the top-level "system" field from IRRequest.System. So a system
	// message currently does not survive an OpenAI-ingress -> Claude-egress
	// IR round-trip; asserting on it here would pin a known cross-protocol
	// data-loss gap in the codec layer (out of scope for this dispatch-layer
	// change) as if it were correct behavior.
	rc := &Exchange{
		originalModel: "gpt-4o",
		isStream:      false,
		candidate:     &model.ModelCandidate{ProviderModelName: "claude-3-5-sonnet-20241022"},
		requestBody:   []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`),
	}
	egress := &EgressDecision{Protocol: protocols.ProtocolClaude, BaseURL: "https://api.anthropic.com", Passthrough: false}

	outBody, url, _, err := s.buildUpstreamBody(rc, protocols.ProtocolOpenAI, egress)
	if err != nil {
		t.Fatalf("buildUpstreamBody returned error: %v", err)
	}

	var parsed struct {
		Messages  []json.RawMessage `json:"messages"`
		MaxTokens *int              `json:"max_tokens"`
	}
	if err := json.Unmarshal(outBody, &parsed); err != nil {
		t.Fatalf("outBody not valid Claude Messages JSON: %v", err)
	}
	if len(parsed.Messages) == 0 {
		t.Errorf("outBody has no messages, want at least one")
	}
	if parsed.MaxTokens == nil {
		t.Errorf("outBody missing max_tokens")
	}

	if !strings.HasSuffix(url, "/v1/messages") {
		t.Errorf("url = %q, want suffix /v1/messages", url)
	}

	req, err := http.NewRequest(http.MethodPost, url, nil)
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	codecsFor(egress.Protocol).RequestEncoder.SetupRequest(req, "test-key")
	if got := req.Header.Get("x-api-key"); got != "test-key" {
		t.Errorf("x-api-key header = %q, want %q", got, "test-key")
	}
	if got := req.Header.Get("anthropic-version"); got == "" {
		t.Errorf("anthropic-version header missing")
	}
}
