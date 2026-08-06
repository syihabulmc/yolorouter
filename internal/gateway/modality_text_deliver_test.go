package gateway

import (
	"bytes"
	"context"
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

// deliveryOutcome is everything a delivery is answerable for: what it reported,
// what the caller received, and what was kept for the audit trail.
type deliveryOutcome struct {
	delivery     fact.Delivery
	clientStatus int
	clientBody   string
	upstreamBody string
	promptTokens int
}

func upstreamResponse(t *testing.T, status int, body string) *http.Response {
	t.Helper()
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}

// runOld drives the path as the service implements it today.
func runOld(t *testing.T, ingress protocols.ProtocolID, egress protocols.ProtocolID, passthrough bool,
	providerModel string, resp *http.Response) deliveryOutcome {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).
		WithContext(context.Background())

	rc := &Exchange{requestID: "old", ingress: ingress, originalModel: "gpt-4o"}
	cand := model.ModelCandidate{ProviderModelName: providerModel}
	svc := &Service{}

	var d fact.Delivery
	if passthrough {
		d = svc.dispatchPassthroughNonStream(c, rc, egress, cand, &model.Provider{}, model.ProviderKey{}, resp, time.Now())
	} else {
		d = svc.processDispatchResponseNonStream(c, rc, ingress,
			&EgressDecision{Protocol: egress, Passthrough: false}, cand, &model.Provider{}, model.ProviderKey{}, resp, time.Now())
	}
	out := deliveryOutcome{delivery: d, clientStatus: w.Code, clientBody: w.Body.String(),
		upstreamBody: string(rc.upstreamResponseBody)}
	if rc.usage != nil {
		out.promptTokens = rc.usage.PromptTokens
	}
	return out
}

// runNew drives the same response through the modality.
func runNew(t *testing.T, ingress protocols.ProtocolID, egress protocols.ProtocolID, passthrough bool,
	providerModel string, callerBody string, resp *http.Response) deliveryOutcome {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).
		WithContext(context.Background())

	payload, rej := NewTextModality().Admit(context.Background(), Ingress{
		Protocol: ingress, Path: "/v1/chat/completions", Body: []byte(callerBody),
	})
	if rej != nil {
		t.Fatalf("Admit refused a valid body: %+v", rej)
	}
	if _, err := payload.PrepareUpstream(Candidate{
		ProviderModelName: providerModel, EgressProtocol: egress, Passthrough: passthrough,
	}); err != nil {
		t.Fatalf("PrepareUpstream = %v", err)
	}

	rc := &Exchange{requestID: "new", ingress: ingress}
	tools, release := (&Service{}).newDeliveryTools(c, rc, TransferLimits{}, false)
	defer release()
	d := payload.Deliver(tools, resp)

	out := deliveryOutcome{delivery: d, clientStatus: w.Code, clientBody: w.Body.String(),
		upstreamBody: string(rc.upstreamResponseBody)}
	if d.Usage != nil {
		out.promptTokens = d.Usage.Prompt
	}
	return out
}

func compare(t *testing.T, old, got deliveryOutcome) {
	t.Helper()
	if got.clientStatus != old.clientStatus {
		t.Errorf("caller received status %d, previously %d", got.clientStatus, old.clientStatus)
	}
	if got.clientBody != old.clientBody {
		t.Errorf("caller received body\n got: %s\nwant: %s", got.clientBody, old.clientBody)
	}
	if got.upstreamBody != old.upstreamBody {
		t.Errorf("captured upstream body\n got: %s\nwant: %s", got.upstreamBody, old.upstreamBody)
	}
	if got.promptTokens != old.promptTokens {
		t.Errorf("prompt tokens = %d, previously %d", got.promptTokens, old.promptTokens)
	}
	o, n := old.delivery, got.delivery
	if n.Committed != o.Committed || n.ClientStatus != o.ClientStatus || n.Verdict != o.Verdict ||
		n.BillingStatus != o.BillingStatus || n.Complete != o.Complete || n.Fault != o.Fault {
		t.Errorf("delivery differs\n got: %+v\nwant: %+v", n, o)
	}
	if o.FailReason != "" && n.FailReason != o.FailReason {
		t.Errorf("fail reason = %q, previously %q", n.FailReason, o.FailReason)
	}
	if got := attemptOutcomeFor(n, false); got != attemptOutcomeFor(o, false) {
		t.Errorf("attempt outcome = %q, previously %q", got, attemptOutcomeFor(o, false))
	}
}

// TestDeliverNonStreamMatchesTheServicePaths runs the same upstream response
// through both implementations and compares everything a delivery is
// answerable for.
//
// Written against the code being replaced rather than against recorded
// fixtures: a fixture pins what the bytes were the day it was captured, this
// pins that the two agree — which is the property the switch has to preserve.
func TestDeliverNonStreamMatchesTheServicePaths(t *testing.T) {
	const callerBody = `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	const openAIOK = `{"id":"c1","object":"chat.completion","model":"gpt-4o-mini","choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"usage":{"prompt_tokens":11,"completion_tokens":3,"total_tokens":14}}`
	const claudeOK = `{"id":"m1","type":"message","role":"assistant","model":"claude-3-5-sonnet-20241022","content":[{"type":"text","text":"hello"}],"stop_reason":"end_turn","usage":{"input_tokens":11,"output_tokens":3}}`

	cases := []struct {
		name          string
		ingress       protocols.ProtocolID
		egress        protocols.ProtocolID
		passthrough   bool
		providerModel string
		upstream      string
		status        int
	}{
		{"passthrough success", protocols.ProtocolOpenAI, protocols.ProtocolOpenAI, true, "gpt-4o-mini", openAIOK, 200},
		{"passthrough, unparseable upstream body", protocols.ProtocolOpenAI, protocols.ProtocolOpenAI, true, "gpt-4o-mini", `{not json`, 200},
		{"translated success", protocols.ProtocolOpenAI, protocols.ProtocolClaude, false, "claude-3-5-sonnet-20241022", claudeOK, 200},
		{"translated, upstream body fails to decode", protocols.ProtocolOpenAI, protocols.ProtocolClaude, false, "claude-3-5-sonnet-20241022", `{not json`, 200},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			old := runOld(t, tc.ingress, tc.egress, tc.passthrough, tc.providerModel,
				upstreamResponse(t, tc.status, tc.upstream))
			got := runNew(t, tc.ingress, tc.egress, tc.passthrough, tc.providerModel, callerBody,
				upstreamResponse(t, tc.status, tc.upstream))
			compare(t, old, got)
		})
	}
}

// TestDeliverRefusesABodyOverTheLimit pins the cap on its own: the old path
// reads one byte past it and gives up, and so must this one.
func TestDeliverRefusesABodyOverTheLimit(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)

	payload, rej := NewTextModality().Admit(context.Background(), Ingress{
		Protocol: protocols.ProtocolOpenAI, Path: "/v1/chat/completions",
		Body: []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`),
	})
	if rej != nil {
		t.Fatalf("Admit refused: %+v", rej)
	}
	if _, err := payload.PrepareUpstream(Candidate{
		ProviderModelName: "gpt-4o-mini", EgressProtocol: protocols.ProtocolOpenAI, Passthrough: true,
	}); err != nil {
		t.Fatalf("PrepareUpstream = %v", err)
	}

	rc := &Exchange{requestID: "too-big"}
	tools, release := (&Service{}).newDeliveryTools(c, rc, TransferLimits{MaxResponseBytes: 8}, false)
	defer release()
	d := payload.Deliver(tools, upstreamResponse(t, 200, `{"aaaaaaaaaaaaaaaaaaaa":1}`))

	if d.Committed || d.Verdict != fact.VerdictNextCandidate {
		t.Errorf("delivery = %+v, want an uncommitted failover", d)
	}
	if d.FailReason != "response_too_large" {
		t.Errorf("fail reason = %q, want %q", d.FailReason, "response_too_large")
	}
	if w.Body.Len() != 0 {
		t.Errorf("the caller received %q, want nothing", w.Body.String())
	}
}

// failingBody fails partway through, the way a read does when the connection
// underneath it goes.
type failingBody struct{ err error }

func (f failingBody) Read([]byte) (int, error) { return 0, f.err }
func (failingBody) Close() error               { return nil }

// TestAFailedReadIsBlamedOnWhoeverCausedIt is the distinction CallerGone exists
// for, and the reason it had to be asked rather than inferred.
//
// The upstream request runs on the caller's context, so a caller who hangs up
// makes the read from the provider fail — with the same error a broken provider
// produces. Recording them the same way would put a caller's impatience on a
// provider's health record, and would spend another candidate on a request
// nobody is waiting for.
func TestAFailedReadIsBlamedOnWhoeverCausedIt(t *testing.T) {
	deliver := func(t *testing.T, callerGone bool) fact.Delivery {
		t.Helper()
		w := httptest.NewRecorder()
		c, _ := gin.CreateTestContext(w)
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		if callerGone {
			cancel()
		}
		c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)

		payload, rej := NewTextModality().Admit(context.Background(), Ingress{
			Protocol: protocols.ProtocolOpenAI, Path: "/v1/chat/completions",
			Body: []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`),
		})
		if rej != nil {
			t.Fatalf("Admit refused: %+v", rej)
		}
		if _, err := payload.PrepareUpstream(Candidate{
			ProviderModelName: "gpt-4o-mini", EgressProtocol: protocols.ProtocolOpenAI, Passthrough: true,
		}); err != nil {
			t.Fatalf("PrepareUpstream = %v", err)
		}

		tools, release := (&Service{}).newDeliveryTools(c, &Exchange{requestID: "read-fail"}, TransferLimits{}, false)
		defer release()
		return payload.Deliver(tools, &http.Response{
			StatusCode: 200,
			Header:     http.Header{"Content-Type": {"application/json"}},
			Body:       failingBody{err: io.ErrUnexpectedEOF},
		})
	}

	t.Run("the caller left", func(t *testing.T) {
		d := deliver(t, true)
		if d.Fault != fact.FaultClient {
			t.Errorf("fault = %v, want client: the provider had nothing to do with it", d.Fault)
		}
		if d.Verdict != fact.VerdictSettled {
			t.Errorf("verdict = %v, want settled: nobody is waiting for another candidate", d.Verdict)
		}
		if d.BillingStatus != 499 || d.FailReason != "client_disconnected" {
			t.Errorf("delivery = %+v, want a 499 client_disconnected", d)
		}
	})

	t.Run("the provider broke", func(t *testing.T) {
		d := deliver(t, false)
		if d.Fault != fact.FaultUpstream {
			t.Errorf("fault = %v, want upstream", d.Fault)
		}
		if d.Verdict != fact.VerdictNextCandidate {
			t.Errorf("verdict = %v, want another candidate to be tried", d.Verdict)
		}
	})
}
