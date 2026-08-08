package gateway

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/yolorouter/yolorouter/internal/fact"
	"github.com/yolorouter/yolorouter/internal/protocols"
)

// deliveryOutcome is everything a delivery is answerable for: what it reported,
// what the caller received, and what was kept for the audit trail.
type deliveryOutcome struct {
	delivery      fact.Delivery
	clientStatus  int
	clientBody    string
	upstreamBody  string
	promptTokens  int
	firstByteSent bool
}

func upstreamResponse(t *testing.T, status int, body string) *http.Response {
	t.Helper()
	return &http.Response{
		StatusCode: status,
		Header:     http.Header{"Content-Type": {"application/json"}},
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}

// deliveryWant is that same account, written down.
type deliveryWant struct {
	clientStatus   int
	clientBody     string
	upstreamBody   string
	promptTokens   int
	firstByteSent  bool
	committed      bool
	deliveryStatus int
	verdict        fact.Verdict
	billingStatus  int
	complete       bool
	fault          fact.Fault
	failReason     string
	attemptOutcome string
}

// admitFor prepares a payload the way the kernel does before anything is
// delivered through it: admitted first, then told which candidate it is on its
// way to. Both steps are needed — the payload learns the caller's request from
// one and the provider's own name for the model from the other.
func admitFor(t *testing.T, ingress protocols.ProtocolID, path, body string, cand Candidate) admitted {
	t.Helper()
	m := NewTextModality()
	payload, rej := m.Admit(context.Background(), Ingress{
		Protocol: ingress, Path: path, ContentType: "application/json", Body: []byte(body),
	})
	if rej != nil {
		t.Fatalf("Admit refused a valid body: %+v", rej)
	}
	if _, err := payload.PrepareUpstream(cand); err != nil {
		t.Fatalf("PrepareUpstream = %v", err)
	}
	return admitted{payload: payload, limits: m.Limits()}
}

// runDelivery drives one upstream response through the modality.
func runDelivery(t *testing.T, ingress protocols.ProtocolID, egress protocols.ProtocolID, passthrough bool,
	providerModel string, callerBody string, resp *http.Response) deliveryOutcome {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).
		WithContext(context.Background())

	adm := admitFor(t, ingress, "/v1/chat/completions", callerBody, Candidate{
		ProviderModelName: providerModel, EgressProtocol: egress, Passthrough: passthrough,
	})

	rc := &Exchange{requestID: "under-test", ingress: ingress}
	tools, release := (&Service{}).newDeliveryTools(c, rc, TransferLimits{}, false)
	defer release()
	d := adm.payload.Deliver(tools, resp)

	out := deliveryOutcome{delivery: d, clientStatus: w.Code, clientBody: w.Body.String(),
		upstreamBody: string(rc.UpstreamResponseBody()), firstByteSent: rc.firstByteSent}
	if d.Usage != nil {
		out.promptTokens = d.Usage.Prompt
	}
	return out
}

// checkDelivery holds a delivery to the account written down for it.
func checkDelivery(t *testing.T, want deliveryWant, got deliveryOutcome) {
	t.Helper()
	if got.clientStatus != want.clientStatus {
		t.Errorf("caller received status %d, want %d", got.clientStatus, want.clientStatus)
	}
	if body := steady(t, got.clientBody); body != steady(t, want.clientBody) {
		t.Errorf("caller received body\n got: %s\nwant: %s", body, want.clientBody)
	}
	if got.upstreamBody != want.upstreamBody {
		t.Errorf("captured upstream body\n got: %s\nwant: %s", got.upstreamBody, want.upstreamBody)
	}
	if got.promptTokens != want.promptTokens {
		t.Errorf("prompt tokens = %d, want %d", got.promptTokens, want.promptTokens)
	}
	// The audit row calls a request undelivered when this is false, so a
	// response the caller received in full has to set it.
	if got.firstByteSent != want.firstByteSent {
		t.Errorf("first byte recorded as sent = %v, want %v", got.firstByteSent, want.firstByteSent)
	}
	d := got.delivery
	if d.Committed != want.committed {
		t.Errorf("committed = %v, want %v", d.Committed, want.committed)
	}
	if d.ClientStatus != want.deliveryStatus {
		t.Errorf("reported client status = %d, want %d", d.ClientStatus, want.deliveryStatus)
	}
	if d.Verdict != want.verdict {
		t.Errorf("verdict = %v, want %v", d.Verdict, want.verdict)
	}
	if d.BillingStatus != want.billingStatus {
		t.Errorf("billing status = %d, want %d", d.BillingStatus, want.billingStatus)
	}
	if d.Complete != want.complete {
		t.Errorf("complete = %v, want %v", d.Complete, want.complete)
	}
	if d.Fault != want.fault {
		t.Errorf("fault = %v, want %v", d.Fault, want.fault)
	}
	if d.FailReason != want.failReason {
		t.Errorf("fail reason = %q, want %q", d.FailReason, want.failReason)
	}
	if outcome := attemptOutcomeFor(d, false); outcome != want.attemptOutcome {
		t.Errorf("attempt outcome = %q, want %q", outcome, want.attemptOutcome)
	}
}

// TestDeliverNonStreamRendersTheWholeAccount runs one upstream response per
// shape through the modality and holds it to everything a delivery is
// answerable for.
//
// The caller's bytes are the loudest of these but far from the only one that
// matters: what the audit trail keeps, what the request is billed for, and
// whether the chain may try another provider are all decided here, and a change
// that got the bytes right and any of those wrong would look correct from the
// caller's side and be wrong everywhere the request is later answered for.
func TestDeliverNonStreamRendersTheWholeAccount(t *testing.T) {
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
		want          deliveryWant
	}{
		{
			name: "passthrough success", ingress: protocols.ProtocolOpenAI, egress: protocols.ProtocolOpenAI,
			passthrough: true, providerModel: "gpt-4o-mini", upstream: openAIOK, status: 200,
			// Forwarded with one edit: the provider's name for the model is
			// replaced by the one the caller asked for, which re-serialises the
			// object and so sorts its keys.
			want: deliveryWant{
				clientStatus: 200,
				clientBody:   `{"choices":[{"index":0,"message":{"role":"assistant","content":"hello"},"finish_reason":"stop"}],"id":"c1","model":"gpt-4o","object":"chat.completion","usage":{"prompt_tokens":11,"completion_tokens":3,"total_tokens":14}}`,
				upstreamBody: openAIOK, promptTokens: 11, firstByteSent: true,
				committed: true, deliveryStatus: 200, verdict: fact.VerdictSettled,
				billingStatus: 200, complete: true, fault: fact.FaultNone, attemptOutcome: "success",
			},
		},
		{
			name: "passthrough, unparseable upstream body", ingress: protocols.ProtocolOpenAI, egress: protocols.ProtocolOpenAI,
			passthrough: true, providerModel: "gpt-4o-mini", upstream: `{not json`, status: 200,
			// Nothing is sent, so the chain may still try another provider. The
			// blamed party is us: the provider answered 2xx and it was the model
			// rewrite that could not proceed.
			//
			// clientStatus is 200 only because nothing was written and that is
			// what an untouched recorder reports; the delivery says the caller
			// got no status at all, which is the answer that counts.
			want: deliveryWant{
				clientStatus: 200,
				// No billing status: this delivery ends nothing, so the number
				// the caller is charged against belongs to whichever one does.
				verdict: fact.VerdictNextCandidate, fault: fact.FaultGateway,
				failReason:     "response_rewrite_failed: parse json object: invalid character 'n' looking for beginning of object key string",
				attemptOutcome: "bad_status",
			},
		},
		{
			name: "translated success", ingress: protocols.ProtocolOpenAI, egress: protocols.ProtocolClaude,
			providerModel: "claude-3-5-sonnet-20241022", upstream: claudeOK, status: 200,
			// Re-encoded into the caller's protocol: the id is prefixed the way
			// that protocol writes them, and fields it requires but claude does
			// not send (created, logprobs) are filled in.
			want: deliveryWant{
				clientStatus: 200,
				clientBody:   `{"choices":[{"finish_reason":"stop","index":0,"logprobs":null,"message":{"content":"hello","role":"assistant"}}],"created":*,"id":"chatcmpl-m1","model":"gpt-4o","object":"chat.completion","usage":{"completion_tokens":3,"prompt_tokens":11,"total_tokens":14}}`,
				upstreamBody: claudeOK, promptTokens: 11, firstByteSent: true,
				committed: true, deliveryStatus: 200, verdict: fact.VerdictSettled,
				billingStatus: 200, complete: true, fault: fact.FaultNone, attemptOutcome: "success",
			},
		},
		{
			name: "translated, upstream body fails to decode", ingress: protocols.ProtocolOpenAI, egress: protocols.ProtocolClaude,
			providerModel: "claude-3-5-sonnet-20241022", upstream: `{not json`, status: 200,
			// Same shape of failure as the passthrough case above but blamed
			// differently, and the body that caused it is kept: a provider whose
			// 2xx does not parse as its own protocol broke its side of the
			// contract, and the bytes are the only evidence of that.
			want: deliveryWant{
				clientStatus: 200, upstreamBody: `{not json`,
				verdict: fact.VerdictNextCandidate, fault: fact.FaultUpstream,
				failReason:     "ir_decode: invalid character 'n' looking for beginning of object key string",
				attemptOutcome: "bad_status",
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := runDelivery(t, tc.ingress, tc.egress, tc.passthrough, tc.providerModel, callerBody,
				upstreamResponse(t, tc.status, tc.upstream))
			checkDelivery(t, tc.want, got)
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
