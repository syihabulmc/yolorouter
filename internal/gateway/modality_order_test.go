package gateway

import (
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/yolorouter/yolorouter/internal/fact"
)

// stubPayload answers every method with something inert. The tests here are
// about the ORDER calls arrive in, so what the calls return is deliberately
// uninteresting -- except for supports, which each test sets to whatever
// verdict the case is about.
type stubPayload struct {
	supports  func(Candidate) CandidateVerdict
	prepare   func(Candidate) (*UpstreamCall, error)
	onRouting func()
	// deliver is what Deliver reports. The zero value is deliberately an
	// undelivered, chain-continuing delivery so a test that does not care
	// stays inside the attempt cycle instead of silently ending the request.
	deliver fact.Delivery
}

func (s *stubPayload) Routing() RoutingIntent {
	if s.onRouting != nil {
		s.onRouting()
	}
	return RoutingIntent{Model: "m"}
}
func (s *stubPayload) EstimateCost(PricingView) CostEstimate { return CostEstimate{} }
func (s *stubPayload) Supports(c Candidate) CandidateVerdict {
	if s.supports != nil {
		return s.supports(c)
	}
	return CandidateVerdict{OK: true}
}
func (s *stubPayload) PrepareUpstream(c Candidate) (*UpstreamCall, error) {
	if s.prepare != nil {
		return s.prepare(c)
	}
	return &UpstreamCall{Path: "/v1/chat/completions", ContentType: "application/json"}, nil
}
func (s *stubPayload) Deliver(DeliveryTools, *http.Response) fact.Delivery {
	if s.deliver.Verdict != fact.VerdictUnset {
		return s.deliver
	}
	return fact.HandedOn(fact.FaultUpstream, "upstream_server_error", nil)
}
func (s *stubPayload) NormalizeUpstreamError(int, []byte, string) ErrorEnvelope {
	return ErrorEnvelope{}
}
func (s *stubPayload) FinalizeUsage(fact.Delivery) *fact.UsageReported { return nil }
func (s *stubPayload) LogPolicy() LogPolicy                            { return LogPolicy{} }
func (s *stubPayload) SanitizeForLog(BodyKind, string, []byte) string  { return "" }

// wantPanic runs fn and reports the panic message, failing if there was none.
func wantPanic(t *testing.T, mentioning string, fn func()) {
	t.Helper()
	defer func() {
		r := recover()
		if r == nil {
			t.Fatalf("no panic, want one mentioning %q", mentioning)
		}
		msg, ok := r.(string)
		if !ok {
			t.Fatalf("panicked with %T (%v), want a string", r, r)
		}
		if !strings.Contains(msg, mentioning) {
			t.Errorf("panicked with %q, want it to mention %q", msg, mentioning)
		}
	}()
	fn()
}

var candA = Candidate{ProviderModelName: "a", BaseURL: "https://a.invalid"}
var candB = Candidate{ProviderModelName: "b", BaseURL: "https://b.invalid"}

// errPrepareFailed stands in for whatever a modality reports when it cannot
// build a body for a candidate it had already accepted.
var errPrepareFailed = errors.New("prepare failed")

// TestTheDocumentedOrderIsAccepted is the floor. If the sequence the interface
// documents did not pass its own assertion, the assertion would be describing
// something other than how the kernel drives a payload.
func TestTheDocumentedOrderIsAccepted(t *testing.T) {
	p := newOrderedPayload(&stubPayload{}, "req-test")

	p.Routing()
	p.EstimateCost(PricingView{})
	// Two candidates in turn: the first reaches nobody and fails over, the
	// second delivers. The attempt cycle has to be re-enterable or a failover
	// chain would trip the assertion on its second candidate.
	//
	// The first delivery MUST be an undelivered one. Two settled deliveries in
	// a row is not the documented order at all -- a settled delivery ends the
	// request -- and a happy-path test written that way pins the very sequence
	// the assertion is supposed to refuse.
	deliveries := []fact.Delivery{
		fact.HandedOn(fact.FaultUpstream, "upstream_server_error", nil),
		fact.Succeeded(200),
	}
	stub := &stubPayload{}
	p = newOrderedPayload(stub, "req-test")
	p.Routing()
	p.EstimateCost(PricingView{})
	for i, cand := range []Candidate{candA, candB} {
		stub.deliver = deliveries[i]
		if v := p.Supports(cand); !v.OK {
			t.Fatalf("Supports(%v) = %+v, want OK", cand, v)
		}
		if _, err := p.PrepareUpstream(cand); err != nil {
			t.Fatalf("PrepareUpstream(%v) = %v", cand, err)
		}
		p.Deliver(DeliveryTools{}, nil)
	}
	p.FinalizeUsage(fact.Succeeded(200))
	p.LogPolicy()
	p.SanitizeForLog(BodyClientRequest, "application/json", nil)
}

func TestEstimateCostBeforeRoutingIsRefused(t *testing.T) {
	p := newOrderedPayload(&stubPayload{}, "req-test")
	wantPanic(t, "EstimateCost", func() { p.EstimateCost(PricingView{}) })
}

func TestSupportsBeforeEstimateCostIsRefused(t *testing.T) {
	p := newOrderedPayload(&stubPayload{}, "req-test")
	p.Routing()
	wantPanic(t, "Supports", func() { p.Supports(candA) })
}

// TestPrepareUpstreamWithoutSupportsIsRefused matches the STAGE message, not
// just the method name. "No candidate was accepted" and "a different candidate
// was accepted" are two rules with two messages, and a test that would accept
// either cannot tell whether the rule it names is still there -- the other
// rule's panic would keep it green.
func TestPrepareUpstreamWithoutSupportsIsRefused(t *testing.T) {
	p := newOrderedPayload(&stubPayload{}, "req-test")
	p.Routing()
	p.EstimateCost(PricingView{})
	wantPanic(t, `needs at least "supported"`, func() { _, _ = p.PrepareUpstream(candA) })
}

// TestPrepareUpstreamForAnUnsupportedCandidateIsRefused is the case the whole
// assertion exists for: Supports may have left something on the payload that
// belongs to candidate A, and preparing B would build a body from it.
func TestPrepareUpstreamForAnotherCandidateIsRefused(t *testing.T) {
	p := newOrderedPayload(&stubPayload{}, "req-test")
	p.Routing()
	p.EstimateCost(PricingView{})
	p.Supports(candA)
	wantPanic(t, "a candidate that Supports did not accept", func() { _, _ = p.PrepareUpstream(candB) })
}

func TestPrepareUpstreamAfterARefusedCandidateIsRefused(t *testing.T) {
	p := newOrderedPayload(&stubPayload{
		supports: func(Candidate) CandidateVerdict {
			return CandidateVerdict{OK: false, Reason: "no streaming"}
		},
	}, "req-test")
	p.Routing()
	p.EstimateCost(PricingView{})
	p.Supports(candA)
	wantPanic(t, `needs at least "supported"`, func() { _, _ = p.PrepareUpstream(candA) })
}

// TestDeliverAfterAFailedPrepareIsRefused covers the gap a build failure would
// otherwise leave: the candidate was accepted, so the stage would still say
// "supported" and a Deliver would go out against a body that was never built.
func TestDeliverAfterAFailedPrepareIsRefused(t *testing.T) {
	p := newOrderedPayload(&stubPayload{
		prepare: func(Candidate) (*UpstreamCall, error) {
			return nil, errPrepareFailed
		},
	}, "req-test")
	p.Routing()
	p.EstimateCost(PricingView{})
	p.Supports(candA)
	if _, err := p.PrepareUpstream(candA); err == nil {
		t.Fatal("PrepareUpstream = nil error, want the stub's failure")
	}
	wantPanic(t, "Deliver", func() { p.Deliver(DeliveryTools{}, nil) })
}

func TestDeliverWithoutPrepareUpstreamIsRefused(t *testing.T) {
	p := newOrderedPayload(&stubPayload{}, "req-test")
	p.Routing()
	p.EstimateCost(PricingView{})
	p.Supports(candA)
	wantPanic(t, "Deliver", func() { p.Deliver(DeliveryTools{}, nil) })
}

// TestASecondDeliveryNeedsItsOwnCandidate pins that one accepted candidate
// buys one delivery, even when the first reached nobody and the chain is
// allowed to continue.
func TestASecondDeliveryNeedsItsOwnCandidate(t *testing.T) {
	p := newOrderedPayload(&stubPayload{}, "req-test")
	p.Routing()
	p.EstimateCost(PricingView{})
	p.Supports(candA)
	if _, err := p.PrepareUpstream(candA); err != nil {
		t.Fatalf("PrepareUpstream = %v", err)
	}
	p.Deliver(DeliveryTools{}, nil)
	wantPanic(t, `needs at least "prepared"`, func() { p.Deliver(DeliveryTools{}, nil) })
}

// TestASettledDeliveryEndsTheChain is the one the wrapper was silent about: a
// delivery that reached the caller must stop the cycle, not send it round
// again. A second candidate on top of a committed response is a second charge,
// and for a payload with side effects, a second execution.
func TestASettledDeliveryEndsTheChain(t *testing.T) {
	for _, tc := range []struct {
		name string
		d    fact.Delivery
	}{
		{"committed and settled", fact.Succeeded(200)},
		{"nothing reached the caller, but the request is over",
			fact.Undelivered(499, fact.VerdictSettled, fact.FaultClient, "client_disconnected", nil)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			stub := &stubPayload{deliver: tc.d}
			p := newOrderedPayload(stub, "req-test")
			p.Routing()
			p.EstimateCost(PricingView{})
			p.Supports(candA)
			if _, err := p.PrepareUpstream(candA); err != nil {
				t.Fatalf("PrepareUpstream = %v", err)
			}
			p.Deliver(DeliveryTools{}, nil)
			wantPanic(t, "after a delivery ended the request", func() { p.Supports(candB) })
		})
	}
}

// TestUsageTravelsWithTheDeliveryThatEarnedIt pins the reason Usage is a field
// on Delivery. An attempt can report what it charged and then fail; if those
// numbers outlive the attempt, they are charged under whatever ends the
// request instead.
func TestUsageTravelsWithTheDeliveryThatEarnedIt(t *testing.T) {
	u := &fact.UsageReported{Unit: fact.UnitToken, Source: fact.UsageFromUpstream, Prompt: 1234}
	failed := fact.HandedOn(fact.FaultUpstream, "upstream_server_error", nil).WithUsage(u)
	if failed.Usage == nil || failed.Usage.Prompt != 1234 {
		t.Fatalf("Usage = %+v, want the 1234 prompt tokens this attempt reported", failed.Usage)
	}
	// The next attempt's delivery is a separate value and carries nothing.
	if next := fact.Succeeded(200); next.Usage != nil {
		t.Errorf("Usage = %+v on a fresh delivery, want nil: it must not inherit the failed attempt's numbers", next.Usage)
	}

	// And the delivery must hold a snapshot, not a view. A payload that keeps
	// one accumulator across attempts hands out the same pointer every time, so
	// an alias would let a later attempt rewrite what this one reported -- which
	// is precisely the outliving this field exists to stop.
	u.Prompt = 9999
	if failed.Usage.Prompt != 1234 {
		t.Errorf("Usage.Prompt = %d after the source was changed, want 1234: the delivery aliases the caller's value",
			failed.Usage.Prompt)
	}
}

func TestFinalizeUsageTwiceIsRefused(t *testing.T) {
	p := newOrderedPayload(&stubPayload{}, "req-test")
	p.Routing()
	p.FinalizeUsage(fact.Succeeded(200))
	wantPanic(t, "billed against two answers", func() { p.FinalizeUsage(fact.Succeeded(200)) })
}

func TestDeliveryWorkAfterFinalizeIsRefused(t *testing.T) {
	p := newOrderedPayload(&stubPayload{}, "req-test")
	p.Routing()
	p.EstimateCost(PricingView{})
	p.FinalizeUsage(fact.Succeeded(200))
	wantPanic(t, "after usage was finalized", func() { p.Supports(candA) })
}

// TestOverlappingCallsAreSurvivable pins the deliberate exception. Re-entering
// a payload is a contract violation, but the version that matters happens on a
// goroutine the modality spawned -- and a panic there is uncatchable and takes
// the process with it. Reporting beats killing the gateway for every other
// caller, so this asserts the call comes back rather than that it explodes.
func TestOverlappingCallsAreSurvivable(t *testing.T) {
	var p *orderedPayload
	reentered := false
	p = newOrderedPayload(&stubPayload{
		onRouting: func() {
			if reentered {
				return
			}
			reentered = true
			// Re-entering from inside a call is the same violation as entering
			// from another goroutine, and is the version a test can make
			// happen deterministically.
			p.EstimateCost(PricingView{})
		},
	}, "req-test")
	p.Routing()
	if !reentered {
		t.Fatal("the stub never re-entered, so nothing was tested")
	}
}

// TestNormalizeUpstreamErrorIsAlwaysAvailable pins the deliberate exception. An
// upstream can fail before any candidate was accepted, and a kernel that could
// not translate that failure would be holding an error it cannot describe.
func TestNormalizeUpstreamErrorIsAlwaysAvailable(t *testing.T) {
	p := newOrderedPayload(&stubPayload{}, "req-test")
	p.NormalizeUpstreamError(500, nil, "application/json")

	p.Routing()
	p.FinalizeUsage(fact.Succeeded(200))
	p.NormalizeUpstreamError(500, nil, "application/json")
}

// fakeResponse is a ClientResponse whose account of itself the test controls,
// so a delivery can be checked against a wire state that disagrees with it.
type fakeResponse struct {
	status int
	ClientResponse
}

func (f *fakeResponse) Committed() bool      { return f.status != 0 }
func (f *fakeResponse) CommittedStatus() int { return f.status }

// TestADeliveryCannotDenyAResponseTheCallerReceived is the gap the delivered
// stage had when it trusted the modality's own account.
//
// A modality that commits a response and then reports "nothing reached the
// caller, try the next candidate" is self-consistent as a Delivery -- Validate
// passes it -- and would send the chain on to another candidate on top of a
// response already sent. Only the thing that wrote the bytes can catch it.
func TestADeliveryCannotDenyAResponseTheCallerReceived(t *testing.T) {
	stub := &stubPayload{
		deliver: fact.HandedOn(fact.FaultUpstream, "upstream_server_error", nil),
	}
	p := newOrderedPayload(stub, "req-dishonest")
	tools := DeliveryTools{Client: &fakeResponse{status: 200}}

	p.Routing()
	p.EstimateCost(PricingView{})
	p.Supports(candA)
	if _, err := p.PrepareUpstream(candA); err != nil {
		t.Fatalf("PrepareUpstream = %v", err)
	}

	d := p.Deliver(tools, nil)
	if !d.Committed || d.ClientStatus != 200 {
		t.Errorf("delivery = %+v, want it corrected to the 200 the caller actually got", d)
	}
	if d.Verdict != fact.VerdictSettled {
		t.Errorf("verdict = %v, want settled: the caller already has a response", d.Verdict)
	}
	if d.Fault != fact.FaultGateway {
		t.Errorf("fault = %v, want gateway: our own adapter misreported", d.Fault)
	}
	// And the chain must be over.
	wantPanic(t, "after a delivery ended the request", func() { p.Supports(candB) })
}

// TestADeliveryCannotClaimAResponseTheCallerNeverGot is the same check the
// other way round: an adapter that reports a committed 200 while nothing was
// written would freeze a status the caller never saw into the audit row.
func TestADeliveryCannotClaimAResponseTheCallerNeverGot(t *testing.T) {
	stub := &stubPayload{deliver: fact.Succeeded(200)}
	p := newOrderedPayload(stub, "req-dishonest-2")
	tools := DeliveryTools{Client: &fakeResponse{status: 0}}

	p.Routing()
	p.EstimateCost(PricingView{})
	p.Supports(candA)
	if _, err := p.PrepareUpstream(candA); err != nil {
		t.Fatalf("PrepareUpstream = %v", err)
	}

	d := p.Deliver(tools, nil)
	if d.Committed {
		t.Errorf("delivery = %+v, want Committed=false: nothing was written", d)
	}
	if d.Fault != fact.FaultGateway || d.FailReason != "delivery_contradicts_response" {
		t.Errorf("delivery = %+v, want it recorded as our own contradiction", d)
	}
}

// TestAnOverlappingCallRunsNothing is the difference between a detector and a
// guard. Logging and carrying on still ran the ordering assertions -- which
// panic, on whichever goroutine made the second call, where nothing recovers
// them -- and still touched the unguarded state the check exists to protect.
func TestAnOverlappingCallRunsNothing(t *testing.T) {
	var p *orderedPayload
	var inner CandidateVerdict
	supportsRan := false
	p = newOrderedPayload(&stubPayload{
		supports: func(Candidate) CandidateVerdict {
			supportsRan = true
			return CandidateVerdict{OK: true}
		},
		onRouting: func() {
			// Re-entering mid-call is the deterministic stand-in for a second
			// goroutine. At this point the stage is below what Supports needs,
			// so the old version would have panicked here.
			inner = p.Supports(candA)
		},
	}, "req-overlap")

	p.Routing()

	if supportsRan {
		t.Error("the inner Supports body ran during an overlapping call")
	}
	if inner.OK {
		t.Errorf("overlapping Supports returned %+v, want a refusal", inner)
	}
	if p.stage != stageRouted {
		t.Errorf("stage = %v after the overlap, want routed: the refused call still moved the state machine", p.stage)
	}
}

// TestUpstreamPathCannotLeaveTheCandidatesOrigin pins the constraint that the
// URL-to-Path rename did not by itself create.
//
// The kernel joins this onto a base by concatenation and then attaches the
// provider's credentials, so a path that can name a host is a path that can
// post somebody's API key somewhere else.
func TestUpstreamPathCannotLeaveTheCandidatesOrigin(t *testing.T) {
	cases := []struct {
		name, path string
	}{
		{"userinfo swallows the real host", "@evil.example/v1/chat"},
		{"protocol-relative names its own host", "//evil.example/v1/chat"},
		{"an absolute url outright", "https://evil.example/v1/chat"},
		{"relative, so it lands wherever the base ends", "v1/chat"},
		{"empty", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := newOrderedPayload(&stubPayload{
				prepare: func(Candidate) (*UpstreamCall, error) {
					return &UpstreamCall{Path: tc.path, ContentType: "application/json"}, nil
				},
			}, "req-path")
			p.Routing()
			p.EstimateCost(PricingView{})
			p.Supports(candA)

			call, err := p.PrepareUpstream(candA)
			if err == nil {
				t.Fatalf("PrepareUpstream accepted %q (call=%+v)", tc.path, call)
			}
			// A refused build must also leave the payload unable to deliver.
			wantPanic(t, `needs at least "prepared"`, func() { p.Deliver(DeliveryTools{}, nil) })
		})
	}
}

func TestAnOrdinaryPathIsAccepted(t *testing.T) {
	p := newOrderedPayload(&stubPayload{
		prepare: func(Candidate) (*UpstreamCall, error) {
			return &UpstreamCall{Path: "/v1/messages?alt=sse", ContentType: "application/json"}, nil
		},
	}, "req-path-ok")
	p.Routing()
	p.EstimateCost(PricingView{})
	p.Supports(candA)
	if _, err := p.PrepareUpstream(candA); err != nil {
		t.Fatalf("PrepareUpstream = %v, want an ordinary path accepted", err)
	}
}

// bareWriteResponse committed without going through Commit, which is what
// every writer outside this type looks like -- the error writers, and all the
// dispatch code that has not moved behind the interface yet.
type bareWriteResponse struct {
	ClientResponse
	written bool
	status  int
}

func (b *bareWriteResponse) Committed() bool      { return b.written }
func (b *bareWriteResponse) CommittedStatus() int { return b.status }

// TestReconcileCatchesAResponseWrittenOutsideTheSink is the hole the first
// version of the check left open, and the one that matters most during the
// migration: dispatch still writes straight to the response, so a status this
// type never recorded is the normal case, not the exotic one.
func TestReconcileCatchesAResponseWrittenOutsideTheSink(t *testing.T) {
	client := &bareWriteResponse{written: true, status: 200}
	d := fact.HandedOn(fact.FaultUpstream, "upstream_server_error", nil)

	got := reconcileWithResponse(client, d, "req-bare")

	if !got.Committed || got.ClientStatus != 200 {
		t.Errorf("delivery = %+v, want it corrected to the 200 already on the wire", got)
	}
	if got.Verdict != fact.VerdictSettled {
		t.Errorf("verdict = %v, want settled: another candidate would write over a served response", got.Verdict)
	}
}

// TestReconcileCorrectsTheStatusToo covers the case where both sides agree
// something was served and disagree about what. The audit row has to carry the
// number the caller actually got.
func TestReconcileCorrectsTheStatusToo(t *testing.T) {
	client := &bareWriteResponse{written: true, status: 200}
	d := fact.Rejected(502, fact.FaultUpstream, "upstream_server_error", nil)

	got := reconcileWithResponse(client, d, "req-status")

	if got.ClientStatus != 200 {
		t.Errorf("ClientStatus = %d, want 200: the caller never saw a 502", got.ClientStatus)
	}
	if got.Fault != fact.FaultGateway {
		t.Errorf("fault = %v, want gateway: our adapter reported a status it did not serve", got.Fault)
	}
}

// TestReconcileKeepsTheUsageItRefuses pins that being wrong about the response
// does not make an attempt free. The tokens were spent upstream either way, and
// dropping them would hand the caller a free request out of our own bug.
func TestReconcileKeepsTheUsageItRefuses(t *testing.T) {
	u := &fact.UsageReported{Unit: fact.UnitToken, Source: fact.UsageFromUpstream, Prompt: 777}
	client := &bareWriteResponse{written: true, status: 200}
	d := fact.HandedOn(fact.FaultUpstream, "upstream_server_error", nil).WithUsage(u)

	got := reconcileWithResponse(client, d, "req-usage")

	if got.Usage == nil || got.Usage.Prompt != 777 {
		t.Errorf("Usage = %+v, want the 777 tokens the attempt actually reported", got.Usage)
	}
}

// TestPrepareUpstreamRefusesACallThatIsNotThere covers the gap the path check
// left: it tested for nil and then did nothing about it, so a build that
// reported success and produced nothing advanced to prepared -- unlocking
// Deliver and handing the kernel a request that does not exist.
func TestPrepareUpstreamRefusesACallThatIsNotThere(t *testing.T) {
	p := newOrderedPayload(&stubPayload{
		prepare: func(Candidate) (*UpstreamCall, error) { return nil, nil },
	}, "req-nilcall")
	p.Routing()
	p.EstimateCost(PricingView{})
	p.Supports(candA)

	call, err := p.PrepareUpstream(candA)
	if err == nil {
		t.Fatalf("PrepareUpstream accepted a nil call (call=%v)", call)
	}
	wantPanic(t, `needs at least "prepared"`, func() { p.Deliver(DeliveryTools{}, nil) })
}

func TestUpstreamPathCannotWalkOutOfTheBasePath(t *testing.T) {
	for _, bad := range []string{"/../admin", "/v1/../../admin", "/v1/./../admin"} {
		p := newOrderedPayload(&stubPayload{
			prepare: func(Candidate) (*UpstreamCall, error) {
				return &UpstreamCall{Path: bad, ContentType: "application/json"}, nil
			},
		}, "req-traversal")
		p.Routing()
		p.EstimateCost(PricingView{})
		p.Supports(candA)
		if _, err := p.PrepareUpstream(candA); err == nil {
			t.Errorf("PrepareUpstream accepted %q, which leaves the configured base path", bad)
		}
	}
}
