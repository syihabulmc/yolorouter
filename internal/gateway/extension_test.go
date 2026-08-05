package gateway

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"github.com/yolorouter/yolorouter/internal/fact"
	"github.com/yolorouter/yolorouter/internal/model"
	"github.com/yolorouter/yolorouter/internal/protocols"
	"github.com/yolorouter/yolorouter/internal/testutil"
)

// stubObserver reports one fact so a test can inspect what the sink stamped on
// it.
type stubObserver struct{}

func (stubObserver) Name() string { return "stub" }

func (stubObserver) ObserveUpstreamError(_ context.Context, _ fact.Attempt, up fact.Upstream, sink fact.Sink) {
	sink.Report(fact.Fact{Kind: fact.KindPayloadRefused, Status: up.StatusCode})
}

// TestObserveUpstreamStampsProvenance pins the provenance the sink writes onto
// every entry.
//
// Every provenance field has a meaningful zero, so getting one wrong produces
// audit rows that are wrong rather than obviously empty: an entry claiming
// candidate 0 refused the payload reads exactly like a real one. Nothing reads
// the timeline yet, which is precisely why this needs a test — a regression here
// would stay invisible until the audit trail is wired up, and by then the cause
// would be several changes back.
func TestObserveUpstreamStampsProvenance(t *testing.T) {
	svc := &Service{}
	RegisterUpstreamErrorObserver(svc, stubObserver{}, func(*Exchange) fact.Attempt { return fact.Attempt{} })

	rc := &Exchange{
		candidate: &model.ModelCandidate{ID: 77},
		provider:  &model.Provider{ID: 42},
		attempts:  make([]AttemptRecord, 2), // two attempts already recorded
	}

	got := svc.observeUpstreamError(context.Background(), rc, fact.Upstream{StatusCode: 400})
	if got.Loop != LoopNextCandidate {
		t.Fatalf("want the refusal to resolve to a failover, got loop %v", got.Loop)
	}

	entries := rc.timeline.All()
	if len(entries) != 1 {
		t.Fatalf("want 1 timeline entry, got %d", len(entries))
	}
	e := entries[0]
	if e.Candidate != 77 {
		t.Errorf("candidate = %d, want 77", e.Candidate)
	}
	if e.Provider != 42 {
		t.Errorf("provider = %d, want 42", e.Provider)
	}
	// The sink is built before the attempt record is appended, so the report
	// belongs to the attempt about to be recorded — index 2 here, not 1.
	if e.Attempt != 2 {
		t.Errorf("attempt = %d, want 2", e.Attempt)
	}
	if e.Reporter != "stub" {
		t.Errorf("reporter = %q, want %q", e.Reporter, "stub")
	}
}

// TestObserveUpstreamToleratesClearedTarget covers the relay's own habit of
// clearing the provider when a candidate turns out to be unusable. Attributing
// such a report to whichever provider happened to be set last would be worse
// than attributing it to none, so an absent target must stay absent rather than
// panic or borrow.
func TestObserveUpstreamToleratesClearedTarget(t *testing.T) {
	svc := &Service{}
	RegisterUpstreamErrorObserver(svc, stubObserver{}, func(*Exchange) fact.Attempt { return fact.Attempt{} })

	rc := &Exchange{} // no candidate, no provider

	svc.observeUpstreamError(context.Background(), rc, fact.Upstream{StatusCode: 400})

	entries := rc.timeline.All()
	if len(entries) != 1 {
		t.Fatalf("want 1 timeline entry, got %d", len(entries))
	}
	if entries[0].Candidate != 0 || entries[0].Provider != 0 {
		t.Errorf("want an unattributed entry, got candidate %d provider %d",
			entries[0].Candidate, entries[0].Provider)
	}
}

// TestObserveUpstreamWithNoObserversIsInert confirms a kernel with nothing
// registered reports nothing and steers nothing — the bare Service must not
// invent a verdict of its own.
func TestObserveUpstreamWithNoObserversIsInert(t *testing.T) {
	svc := &Service{}
	rc := &Exchange{}

	got := svc.observeUpstreamError(context.Background(), rc, fact.Upstream{StatusCode: 400})
	if got.Loop != LoopNone {
		t.Errorf("loop = %v, want LoopNone", got.Loop)
	}
	if len(rc.timeline.All()) != 0 {
		t.Errorf("want an empty timeline, got %d entries", len(rc.timeline.All()))
	}
}

// refusingRewriter declares the body unusable. Nothing in the tree does this
// yet, which is exactly why it needs a test: the branch that handles a refusal
// is otherwise unreachable, and unreachable code that guards a contract is code
// that has never been shown to honour it.
type refusingRewriter struct{}

func (refusingRewriter) Name() string { return "refuser" }

func (refusingRewriter) RewriteEgress(_ context.Context, _ fact.Attempt, _ protocols.ProtocolID, _ []byte, _ fact.Sink) ([]byte, error) {
	return nil, errors.New("cannot produce a usable body")
}

// mutatingRewriter rewrites successfully, so a test can prove a refusal stops
// the chain before later rewriters run.
type mutatingRewriter struct{ ran *bool }

func (mutatingRewriter) Name() string { return "mutator" }

func (m mutatingRewriter) RewriteEgress(_ context.Context, _ fact.Attempt, _ protocols.ProtocolID, body []byte, _ fact.Sink) ([]byte, error) {
	*m.ran = true
	return append(body, '!'), nil
}

// TestRewriteEgressRefusalStopsAndReportsAVerdict pins the contract the
// interface states: an error means the body must not be sent. Returning the
// last good body instead would put upstream exactly what a rewriter refused to
// produce.
func TestRewriteEgressRefusalStopsAndReportsAVerdict(t *testing.T) {
	svc := &Service{}
	laterRan := false
	RegisterEgressRewriter(svc, refusingRewriter{}, 10, func(*Exchange) fact.Attempt { return fact.Attempt{} })
	RegisterEgressRewriter(svc, mutatingRewriter{ran: &laterRan}, 20, func(*Exchange) fact.Attempt { return fact.Attempt{} })

	rc := &Exchange{}
	body := []byte(`{"a":1}`)
	out, verdict := svc.rewriteEgress(context.Background(), rc, protocols.ProtocolOpenAI, body)

	if verdict.Loop != LoopNextCandidate {
		t.Fatalf("loop = %v, want LoopNextCandidate so the candidate is skipped", verdict.Loop)
	}
	if verdict.loopFrom != fact.KindEgressRewriteFailed {
		t.Errorf("loopFrom = %s, want egress_rewrite_failed", verdict.loopFrom)
	}
	if string(out) != string(body) {
		t.Errorf("body = %q, want it left as it was; a refused body must not be handed on", out)
	}
	if laterRan {
		t.Error("a later rewriter ran after the body was refused")
	}
	if len(rc.timeline.All()) != 1 {
		t.Errorf("want the refusal on the timeline, got %d entries", len(rc.timeline.All()))
	}
}

// TestRewriteEgressRunsInStageOrder confirms order comes from the stage given
// at registration, not from the order the calls happened to be made in.
func TestRewriteEgressRunsInStageOrder(t *testing.T) {
	var order []string
	rec := func(name string) EgressRewriterOf[fact.Attempt] { return orderingRewriter{name: name, log: &order} }

	svc := &Service{}
	// Registered late-stage first, on purpose.
	RegisterEgressRewriter(svc, rec("second"), 90, func(*Exchange) fact.Attempt { return fact.Attempt{} })
	RegisterEgressRewriter(svc, rec("first"), 10, func(*Exchange) fact.Attempt { return fact.Attempt{} })

	svc.rewriteEgress(context.Background(), &Exchange{}, protocols.ProtocolOpenAI, []byte(`{}`))

	if len(order) != 2 || order[0] != "first" || order[1] != "second" {
		t.Fatalf("ran in %v, want stage order regardless of registration order", order)
	}
}

type orderingRewriter struct {
	name string
	log  *[]string
}

func (o orderingRewriter) Name() string { return o.name }

func (o orderingRewriter) RewriteEgress(_ context.Context, _ fact.Attempt, _ protocols.ProtocolID, body []byte, _ fact.Sink) ([]byte, error) {
	*o.log = append(*o.log, o.name)
	return body, nil
}

// TestDuplicateStagePanicsAtAssembly confirms a stage collision is caught where
// it can still be fixed, rather than resolving silently to whichever rewriter
// was registered first.
func TestDuplicateStagePanicsAtAssembly(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("registering two rewriters at the same stage must panic")
		}
	}()
	svc := &Service{}
	var log []string
	RegisterEgressRewriter(svc, orderingRewriter{name: "a", log: &log}, 10, func(*Exchange) fact.Attempt { return fact.Attempt{} })
	RegisterEgressRewriter(svc, orderingRewriter{name: "b", log: &log}, 10, func(*Exchange) fact.Attempt { return fact.Attempt{} })
}

// vandalObserver mutates everything it is handed. A well-behaved observer never
// does this; the point is that the guarantee must not depend on that.
type vandalObserver struct{}

func (vandalObserver) Name() string { return "vandal" }

func (vandalObserver) ObserveUpstreamError(_ context.Context, _ fact.Attempt, up fact.Upstream, _ fact.Sink) {
	if len(up.Body) > 0 {
		up.Body[0] = 'X'
	}
	up.Header.Set("X-Tampered", "yes")
}

// witnessObserver records what it was actually handed.
type witnessObserver struct {
	body   []byte
	header string
}

func (w *witnessObserver) Name() string { return "witness" }

func (w *witnessObserver) ObserveUpstreamError(_ context.Context, _ fact.Attempt, up fact.Upstream, _ fact.Sink) {
	w.body = bytes.Clone(up.Body)
	w.header = up.Header.Get("X-Tampered")
}

// TestObserversCannotReachEachOtherOrTheAuditBody is the isolation guarantee
// the interface states. Without per-observer copies the verdict would depend on
// registration order through a second door — one observer editing the input the
// next one reads — and a stray write would also rewrite the bytes already
// captured as the record of what the upstream said.
func TestObserversCannotReachEachOtherOrTheAuditBody(t *testing.T) {
	svc := &Service{}
	witness := &witnessObserver{}
	RegisterUpstreamErrorObserver(svc, vandalObserver{}, func(*Exchange) fact.Attempt { return fact.Attempt{} })
	RegisterUpstreamErrorObserver(svc, witness, func(*Exchange) fact.Attempt { return fact.Attempt{} })

	captured := []byte(`{"error":"real"}`)
	header := http.Header{}
	header.Set("Content-Type", "application/json")

	svc.observeUpstreamError(context.Background(), &Exchange{}, fact.Upstream{
		StatusCode: 400,
		Header:     header,
		Body:       captured,
	})

	if witness.body[0] == 'X' {
		t.Error("the second observer saw the first one's edit to the body")
	}
	if witness.header != "" {
		t.Error("the second observer saw the first one's edit to the headers")
	}
	if string(captured) != `{"error":"real"}` {
		t.Errorf("the captured upstream body was rewritten to %q", captured)
	}
	if header.Get("X-Tampered") != "" {
		t.Error("the caller's header map was mutated")
	}
}

// candidateRefusingRewriter refuses the body only when it is bound for the
// first candidate, so a test can watch the chain walk past the refusal.
type candidateRefusingRewriter struct{}

func (candidateRefusingRewriter) Name() string { return "candidate_refuser" }

func (candidateRefusingRewriter) RewriteEgress(_ context.Context, _ fact.Attempt, _ protocols.ProtocolID, body []byte, _ fact.Sink) ([]byte, error) {
	if bytes.Contains(body, []byte(`"model":"c1-model"`)) {
		return nil, errors.New("refusing the body bound for c1")
	}
	return body, nil
}

// TestEgressRefusalFailsOverToNextCandidate is the end-to-end proof for the
// refusal path: the verdict must cost exactly one candidate. The refused
// candidate's upstream must never see the request — a refused body reaching the
// wire is the failure this path exists to prevent — and the chain must then
// serve from the next candidate rather than giving up.
func TestEgressRefusalFailsOverToNextCandidate(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	var (
		seenMu     sync.Mutex
		seenModels []string
	)
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenMu.Lock()
		seenModels = append(seenModels, extractModelFromJSON(t, body))
		seenMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"model":"c2-model","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
	}))
	defer upstream.Close()

	svc := newSvc(t, db)
	RegisterEgressRewriter(svc, candidateRefusingRewriter{}, 10,
		func(*Exchange) fact.Attempt { return fact.Attempt{} })
	apiKey := seedTwoCandidateModel(t, svc, db, upstream.URL)

	c, w := newCtx([]byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`))
	svc.Handle(c, apiKey)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 served by the second candidate; body = %s", w.Code, w.Body.String())
	}
	seenMu.Lock()
	got := append([]string(nil), seenModels...)
	seenMu.Unlock()
	if len(got) != 1 || got[0] != "c2-model" {
		t.Fatalf("upstream saw %v, want only [c2-model]: the refused body must never reach the wire", got)
	}
}
