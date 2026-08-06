package gateway

import (
	"testing"

	"github.com/yolorouter/yolorouter/internal/protocols"
)

// TestASuccessfulNonStreamResponseIsRecordedAsDelivered fixes a record that was
// wrong for every non-streaming request.
//
// The audit row's "delivered" field reads the exchange's first-byte-sent flag,
// and only the streaming paths ever set it — so a response the caller received
// in full was filed as never delivered. Nothing branched on it, which is why it
// survived; it is read by people looking at a request that supposedly failed.
func TestASuccessfulNonStreamResponseIsRecordedAsDelivered(t *testing.T) {
	const callerBody = `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	const openAIOK = `{"id":"c1","object":"chat.completion","model":"gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":3,"total_tokens":14}}`

	// Both ways of producing the caller's bytes have to set it. They write
	// through the same client, but they arrive at what to write differently, and
	// only one of them was ever exercised by the table this came from.
	for _, tc := range []struct {
		name        string
		passthrough bool
	}{
		{"passthrough", true},
		{"translated", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := runDelivery(t, protocols.ProtocolOpenAI, protocols.ProtocolOpenAI, tc.passthrough,
				"gpt-4o-mini", callerBody, upstreamResponse(t, 200, openAIOK))

			if !got.delivery.Committed {
				t.Fatalf("delivery = %+v, want a committed success", got.delivery)
			}
			if !got.firstByteSent {
				t.Error("firstByteSent = false after a fully delivered response; the audit row calls that undelivered")
			}
		})
	}
}
