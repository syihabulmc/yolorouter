package gateway

import (
	"testing"

	"github.com/yolorouter/yolorouter/internal/model"
	"github.com/yolorouter/yolorouter/internal/protocols"
)

func TestIngressProtocol_ChatCompletions(t *testing.T) {
	got := IngressProtocol("/v1/chat/completions")
	if got != protocols.ProtocolOpenAI {
		t.Fatalf("IngressProtocol(/v1/chat/completions) = %v, want %v", got, protocols.ProtocolOpenAI)
	}
}

func TestIngressProtocol_UnknownPathFallsBackToOpenAI(t *testing.T) {
	got := IngressProtocol("/v1/unknown")
	if got != protocols.ProtocolOpenAI {
		t.Fatalf("IngressProtocol(/v1/unknown) = %v, want %v", got, protocols.ProtocolOpenAI)
	}
}

func TestNegotiate_OpenAIIngressOnOpenAIProvider_Passthrough(t *testing.T) {
	p := &model.Provider{ProviderType: "openai", BaseURL: "https://api.openai.com"}

	decision, err := Negotiate(protocols.ProtocolOpenAI, p)
	if err != nil {
		t.Fatalf("Negotiate returned error: %v", err)
	}
	if decision.Protocol != protocols.ProtocolOpenAI {
		t.Errorf("Protocol = %v, want %v", decision.Protocol, protocols.ProtocolOpenAI)
	}
	if decision.BaseURL != p.BaseURL {
		t.Errorf("BaseURL = %q, want %q", decision.BaseURL, p.BaseURL)
	}
}

func TestNegotiate_OpenAIIngressOnAnthropicProvider_FallsBackToPrimary(t *testing.T) {
	p := &model.Provider{ProviderType: "anthropic", BaseURL: "https://api.anthropic.com"}

	decision, err := Negotiate(protocols.ProtocolOpenAI, p)
	if err != nil {
		t.Fatalf("Negotiate returned error: %v", err)
	}
	if decision.Protocol != protocols.ProtocolClaude {
		t.Errorf("Protocol = %v, want %v", decision.Protocol, protocols.ProtocolClaude)
	}
	if decision.BaseURL != p.BaseURL {
		t.Errorf("BaseURL = %q, want %q", decision.BaseURL, p.BaseURL)
	}
}

func TestNegotiate_EmptyProviderTypeTreatedAsOpenAI(t *testing.T) {
	p := &model.Provider{ProviderType: "", BaseURL: "https://example.com"}

	decision, err := Negotiate(protocols.ProtocolOpenAI, p)
	if err != nil {
		t.Fatalf("Negotiate returned error: %v", err)
	}
	if decision.Protocol != protocols.ProtocolOpenAI {
		t.Errorf("Protocol = %v, want %v", decision.Protocol, protocols.ProtocolOpenAI)
	}
	if decision.BaseURL != p.BaseURL {
		t.Errorf("BaseURL = %q, want %q", decision.BaseURL, p.BaseURL)
	}
}

func TestNegotiate_NilProviderReturnsError(t *testing.T) {
	_, err := Negotiate(protocols.ProtocolOpenAI, nil)
	if err == nil {
		t.Fatal("Negotiate(nil provider) expected error, got nil")
	}
}
