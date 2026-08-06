package gateway

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/yolorouter/yolorouter/internal/fact"
	"github.com/yolorouter/yolorouter/internal/model"
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
	const openAIOK = `{"id":"c1","object":"chat.completion","model":"gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":3,"total_tokens":14}}`

	for _, tc := range []struct {
		name        string
		passthrough bool
		egress      protocols.ProtocolID
		provider    string
		body        string
	}{
		{"passthrough", true, protocols.ProtocolOpenAI, "gpt-4o-mini", openAIOK},
		{"translated", false, protocols.ProtocolOpenAI, "gpt-4o-mini", openAIOK},
	} {
		t.Run(tc.name, func(t *testing.T) {
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
			rc := &Exchange{requestID: "delivered", ingress: protocols.ProtocolOpenAI, originalModel: "gpt-4o"}
			cand := model.ModelCandidate{ProviderModelName: tc.provider}
			svc := &Service{}

			var d fact.Delivery
			if tc.passthrough {
				d = svc.dispatchPassthroughNonStream(c, rc, tc.egress, cand, &model.Provider{}, model.ProviderKey{},
					&http.Response{
						StatusCode: 200,
						Header:     http.Header{"Content-Type": {"application/json"}},
						Body:       io.NopCloser(bytes.NewBufferString(tc.body)),
					}, time.Now())
			} else {
				d = svc.processDispatchResponseNonStream(c, rc, protocols.ProtocolOpenAI,
					&EgressDecision{Protocol: tc.egress, Passthrough: false}, cand, &model.Provider{},
					model.ProviderKey{}, &http.Response{
						StatusCode: 200,
						Header:     http.Header{"Content-Type": {"application/json"}},
						Body:       io.NopCloser(bytes.NewBufferString(tc.body)),
					}, time.Now())
			}

			if !d.Committed {
				t.Fatalf("delivery = %+v, want a committed success", d)
			}
			if !rc.firstByteSent {
				t.Error("firstByteSent = false after a fully delivered response; the audit row calls that undelivered")
			}
		})
	}
}
