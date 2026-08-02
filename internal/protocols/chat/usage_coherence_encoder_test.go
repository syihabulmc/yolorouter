package chat

import "testing"

// These tests lock the encoder side of the usage-coherence convergence on the
// CHAT (OpenAI) path specifically. They drive a wire body through the REAL chat
// decoder and then call the REAL egress builder (openAIWireUsage), asserting the
// encoder publishes null (nil) for records the billing gate also refuses.
//
// This file exists because the convergence was originally tested only on the
// responses path, which let a real gap slip through: the chat non-streaming
// DecodeResponse once forgot to set Invalid, and since openAIWireUsage now guards
// on Invalid alone (not HasNegativeCount), it emitted absurd-cache records the
// billing gate refused — the literal P1-1 shape, relocated to chat. These tests
// guard that decoder-exit setting for the chat protocol.

// TestChatEncoderRefusesAbsurdCacheCount locks P1-1 on the chat wire side.
func TestChatEncoderRefusesAbsurdCacheCount(t *testing.T) {
	body := `{"choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":100,"completion_tokens":1,"total_tokens":101,"prompt_tokens_details":{"cached_tokens":0,"cache_write_tokens":1000000}}}`
	resp, err := ResponseDecoder{}.DecodeResponse([]byte(body))
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if got := openAIWireUsage(resp.Usage); got != nil {
		t.Errorf("chat encoder must emit null for an absurd cache count (P1-1), got %v for usage %+v", got, resp.Usage)
	}
}

// TestChatEncoderEmitsHealthyUsage guards against over-rejection on the chat path.
func TestChatEncoderEmitsHealthyUsage(t *testing.T) {
	body := `{"choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":1000,"completion_tokens":10,"total_tokens":1010}}`
	resp, err := ResponseDecoder{}.DecodeResponse([]byte(body))
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if got := openAIWireUsage(resp.Usage); got == nil {
		t.Errorf("chat encoder must emit usage for a healthy record, got nil for %+v", resp.Usage)
	}
}
