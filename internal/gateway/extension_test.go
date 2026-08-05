package gateway

import (
	"context"
	"testing"

	"github.com/yolorouter/yolorouter/internal/fact"
	"github.com/yolorouter/yolorouter/internal/model"
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
