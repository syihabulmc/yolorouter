package service

import (
	"testing"

	"github.com/yolorouter/yolorouter/internal/protocols"
	"github.com/yolorouter/yolorouter/internal/protocols/chat"
)

// TestUnknownProtocolFallsBackToOpenAI pins the fallback the table's own
// comment promises: a protocol nobody registered probes exactly like OpenAI —
// same encoder, same body shape — mirroring protocolForProviderType's
// normalization of unknown provider_type values. Without this, an
// unrecognized ProtocolID would silently probe with whatever spec the
// fallback happens to hand out, and a credential test could pass against an
// endpoint production never dispatches to that way.
func TestUnknownProtocolFallsBackToOpenAI(t *testing.T) {
	const bogus = protocols.ProtocolID("bogus-protocol")

	if _, ok := requestEncoderFor(bogus).(chat.RequestEncoder); !ok {
		t.Errorf("requestEncoderFor(unknown) = %T, want the OpenAI chat encoder", requestEncoderFor(bogus))
	}

	payload := chatCompletionPayload(bogus, "m1")
	if _, ok := payload["messages"]; !ok {
		t.Errorf("chatCompletionPayload(unknown) = %v, want the OpenAI messages shape", payload)
	}
	if payload["model"] != "m1" {
		t.Errorf("chatCompletionPayload(unknown) model = %v, want the tested model", payload["model"])
	}

	if got := protocolForProviderType("bogus-protocol"); got != protocols.ProtocolOpenAI {
		t.Errorf("protocolForProviderType(unknown) = %v, want OpenAI", got)
	}
}
