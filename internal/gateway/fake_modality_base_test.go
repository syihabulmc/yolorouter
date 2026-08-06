package gateway

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/yolorouter/yolorouter/internal/fact"
	"github.com/yolorouter/yolorouter/internal/protocols"
)

// The fake modalities in the neighbouring files stand in for the three kinds of
// payload this kernel does not carry yet: a multipart upload, a binary audio
// response, and a stream whose frames are not the text modality's. None of them
// ships — they are compiled only under `go test` — and none implements anything
// a real one would call business logic. What they are for is weight: each one
// leans on the part of the interface its kind of payload is known to strain, so
// that "the interface can hold it" is a build result rather than a belief.
//
// fakePayloadBase answers the questions none of them are about, so each fake
// contains only the methods it is actually testing. That is a deliberate line:
// a base that answered everything would let a fake pass by not implementing the
// method under test, which is the failure mode the fakes exist to prevent.
type fakePayloadBase struct{}

func (fakePayloadBase) Routing() RoutingIntent                { return RoutingIntent{Model: "fake"} }
func (fakePayloadBase) EstimateCost(PricingView) CostEstimate { return CostEstimate{} }
func (fakePayloadBase) Supports(Candidate) CandidateVerdict   { return CandidateVerdict{OK: true} }

func (fakePayloadBase) NormalizeUpstreamError(status int, _ []byte, _ string) ErrorEnvelope {
	return ErrorEnvelope{Status: status, ErrorType: "upstream_error", Message: "upstream failed"}
}

func (fakePayloadBase) FinalizeUsage(fact.Delivery) *fact.UsageReported { return nil }
func (fakePayloadBase) LogPolicy() LogPolicy                            { return LogPolicy{} }

func (fakePayloadBase) SanitizeForLog(BodyKind, string, []byte) string { return "" }

func (fakePayloadBase) PrepareUpstream(Candidate) (*UpstreamCall, error) {
	return &UpstreamCall{Path: "/v1/fake", Body: []byte("{}"), ContentType: "application/json"}, nil
}

func (fakePayloadBase) Deliver(DeliveryTools, *http.Response) fact.Delivery {
	return fact.Succeeded(http.StatusOK)
}

// fakeDelivery is the scaffolding a Deliver test needs before it can call one:
// a recorder to read the caller's side from, a gin context with somewhere to
// put captured bodies, and an exchange for the kernel to build tools against.
//
// Shared across the fakes because it is the same six lines each time and the
// differences are data. Written out per test, the one that mattered — whether
// the delivery is a streaming one — read as an incidental argument rather than
// as the thing that changes what the tools do.
type fakeDelivery struct {
	path      string
	requestID string
	isStream  bool
	// onContext runs before the tools are built, so a test can reach the
	// request from inside a read that happens later.
	onContext func(*gin.Context)
}

func (f fakeDelivery) tools(t *testing.T) (DeliveryTools, func(), *httptest.ResponseRecorder) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, f.path, nil)
	c.Set(BodiesDirContextKey, t.TempDir())
	if f.onContext != nil {
		f.onContext(c)
	}

	rc := &Exchange{requestID: f.requestID, ingress: protocols.ProtocolOpenAI, isStream: f.isStream}
	tools, release := (&Service{}).newDeliveryTools(c, rc, TransferLimits{}, f.isStream)
	return tools, release, w
}
