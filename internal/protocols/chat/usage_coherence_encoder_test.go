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
// billing gate refused — the literal shape, relocated to chat. These tests
// guard that decoder-exit setting for the chat protocol.

// TestChatEncoderRefusesAbsurdCacheCount locks the absurd-cache-count refusal on the chat wire side.
func TestChatEncoderRefusesAbsurdCacheCount(t *testing.T) {
	body := `{"choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":100,"completion_tokens":1,"total_tokens":101,"prompt_tokens_details":{"cached_tokens":0,"cache_write_tokens":1000000}}}`
	resp, err := ResponseDecoder{}.DecodeResponse([]byte(body))
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if got := openAIWireUsage(resp.Usage); got != nil {
		t.Errorf("chat encoder must emit null for an absurd cache count, got %v for usage %+v", got, resp.Usage)
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

// TestChatEncoderRefusesReasoningExceedingCompletion locks the reasoning-subset
// rule on the chat non-streaming path. OpenAI counts
// completion_tokens_details.reasoning_tokens INSIDE completion_tokens, so 100
// reasoning tokens against a 10-token completion is impossible; the decoder must
// settle that at its exit and the encoder must publish null rather than a
// breakdown that contradicts its own total.
func TestChatEncoderRefusesReasoningExceedingCompletion(t *testing.T) {
	body := `{"choices":[{"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15,"completion_tokens_details":{"reasoning_tokens":100}}}`
	resp, err := ResponseDecoder{}.DecodeResponse([]byte(body))
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if !resp.Usage.Invalid {
		t.Errorf("reasoning exceeding completion must be marked Invalid at the decoder exit, got %+v", resp.Usage)
	}
	if got := openAIWireUsage(resp.Usage); got != nil {
		t.Errorf("chat encoder must emit null when reasoning exceeds completion, got %v for usage %+v", got, resp.Usage)
	}
}

// TestChatStreamRefusesSingleFrameReasoningExcess drives the REAL streaming
// decoder and encoder. The contradiction is asserted by ONE frame — one
// upstream snapshot claiming 100 reasoning tokens inside a 10-token
// completion — which is exactly the case the verdict is allowed to reach, so
// the encoder must publish nothing for it.
func TestChatStreamRefusesSingleFrameReasoningExcess(t *testing.T) {
	dec := NewStreamDecoder()
	enc := &StreamEncoder{}

	deltas, err := dec.DecodeChunk(`data: {"id":"c","model":"m","choices":[],` +
		`"usage":{"prompt_tokens":5,"completion_tokens":10,"total_tokens":15,"completion_tokens_details":{"reasoning_tokens":100}}}` + "\n\n")
	if err != nil {
		t.Fatalf("DecodeChunk: %v", err)
	}
	enc.EncodeDeltas(deltas)

	if !enc.Usage().Invalid {
		t.Errorf("a frame claiming 100 reasoning tokens inside a 10-token completion must be caught, got %+v", enc.Usage())
	}
	if got := openAIWireUsage(enc.Usage()); got != nil {
		t.Errorf("chat encoder must emit null for a contradiction the upstream asserted, got %v", got)
	}
}

// TestChatStreamCumulativeReasoningStaysBillable is the case that must NOT be
// refused: an upstream reporting cumulative counts whose reasoning line runs
// ahead of the completion line it belongs to, with the completion catching up
// in a later frame. No single frame ever asserted anything impossible, and the
// settled record is coherent, so the client must still get its usage and the
// request must still be billable.
func TestChatStreamCumulativeReasoningStaysBillable(t *testing.T) {
	dec := NewStreamDecoder()
	enc := &StreamEncoder{}

	frames := []string{
		`{"prompt_tokens":100,"completion_tokens":10,"total_tokens":110}`,
		`{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0,"completion_tokens_details":{"reasoning_tokens":15}}`,
		`{"prompt_tokens":100,"completion_tokens":20,"total_tokens":120}`,
	}
	for i, u := range frames {
		deltas, err := dec.DecodeChunk(`data: {"id":"c","model":"m","choices":[],"usage":` + u + "}\n\n")
		if err != nil {
			t.Fatalf("DecodeChunk frame%d: %v", i+1, err)
		}
		enc.EncodeDeltas(deltas)
		if enc.Usage().Invalid {
			t.Fatalf("record condemned after frame %d, but no frame asserted a contradiction: %+v", i+1, enc.Usage())
		}
	}

	u := enc.Usage()
	if u.CompletionTokens != 20 || u.ReasoningTokens != 15 {
		t.Errorf("final counts wrong: got completion=%d reasoning=%d, want 20/15 (%+v)",
			u.CompletionTokens, u.ReasoningTokens, u)
	}
	if got := openAIWireUsage(u); got == nil {
		t.Error("chat encoder must emit usage for a stream whose final counts are coherent, got nil")
	}
}

// TestChatStreamKeepsCoherentReasoningBillable is the negative case for the
// streaming path: reasoning arriving in its own frame, within the accumulated
// completion, must stay billable — otherwise the rule above would silently stop
// billing every reasoning model that splits its usage across frames.
func TestChatStreamKeepsCoherentReasoningBillable(t *testing.T) {
	dec := NewStreamDecoder()
	enc := &StreamEncoder{}

	deltas, err := dec.DecodeChunk(`data: {"id":"c","model":"m","choices":[],` +
		`"usage":{"prompt_tokens":5,"completion_tokens":60,"total_tokens":65}}` + "\n\n")
	if err != nil {
		t.Fatalf("DecodeChunk frame1: %v", err)
	}
	enc.EncodeDeltas(deltas)

	deltas, err = dec.DecodeChunk(`data: {"id":"c","model":"m","choices":[],` +
		`"usage":{"prompt_tokens":0,"completion_tokens":0,"total_tokens":0,"completion_tokens_details":{"reasoning_tokens":40}}}` + "\n\n")
	if err != nil {
		t.Fatalf("DecodeChunk frame2: %v", err)
	}
	enc.EncodeDeltas(deltas)

	u := enc.Usage()
	if u.Invalid {
		t.Fatalf("40 reasoning tokens inside a 60-token completion is coherent and must stay billable, got %+v", u)
	}
	if u.ReasoningTokens != 40 || u.CompletionTokens != 60 {
		t.Errorf("accumulated usage lost counts: got reasoning=%d completion=%d, want 40/60 (%+v)",
			u.ReasoningTokens, u.CompletionTokens, u)
	}
	if got := openAIWireUsage(u); got == nil {
		t.Error("chat encoder must emit usage for a coherent reasoning record, got nil")
	}
}
