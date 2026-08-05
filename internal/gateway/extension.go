package gateway

import (
	"context"
	"time"

	"github.com/yolorouter/yolorouter/internal/fact"
)

// Extension points are declared here; implementations live in their own
// packages and never import this one.
//
// Each shape is generic over V, the view the implementation wants of the
// exchange. That is not decoration: Go interface satisfaction requires
// signatures to match type for type, so an interface written against one fixed
// view type could only ever be satisfied by implementations that name that same
// type — which would force every implementation to import this package and undo
// the separation. With V as a parameter, an implementation declares the narrow
// view it actually needs, and the binding function supplied at assembly is the
// compile-time proof that an Exchange satisfies it.
//
// An implementation that needs nothing beyond the snapshot uses fact.Attempt or
// fact.Request as its V; the generic degenerates and no second calling
// convention is needed.

// UpstreamErrorObserverOf sees one complete NON-2xx upstream response and
// reports what it recognises in it. It cannot alter the response, write to the
// caller, or say what should happen next — its only output is what it reports.
//
// The name is narrow on purpose. A successful response never reaches this
// shape: the relay dispatches 2xx straight into the response pipeline, so an
// observer registered here would never see one. Observations drawn from a
// successful exchange — usage, stop reasons, first-token latency — arrive
// through the streaming and codec shapes instead, and calling this one
// "upstream observer" would promise a reach it does not have.
type UpstreamErrorObserverOf[V any] interface {
	Name() string
	ObserveUpstreamError(ctx context.Context, view V, up fact.Upstream, sink fact.Sink)
}

// upstreamErrorObserver is the kernel-side, view-erased form. One per shape.
type upstreamErrorObserver interface {
	name() string
	observe(ctx context.Context, e *Exchange, up fact.Upstream, sink fact.Sink)
}

type upstreamErrorObserverAdapter[V any] struct {
	inner UpstreamErrorObserverOf[V]
	bind  func(*Exchange) V
}

func (a upstreamErrorObserverAdapter[V]) name() string { return a.inner.Name() }

func (a upstreamErrorObserverAdapter[V]) observe(ctx context.Context, e *Exchange, up fact.Upstream, sink fact.Sink) {
	a.inner.ObserveUpstreamError(ctx, a.bind(e), up, sink)
}

// RegisterUpstreamErrorObserver wires an observer into the service. The bind
// function is where an Exchange is checked against the observer's own view: a
// getter the observer needs and the Exchange lacks fails to compile at this
// call, not at run time.
func RegisterUpstreamErrorObserver[V any](s *Service, o UpstreamErrorObserverOf[V], bind func(*Exchange) V) {
	s.upstreamErrorObservers = append(s.upstreamErrorObservers, upstreamErrorObserverAdapter[V]{inner: o, bind: bind})
}

// exchangeSink collects what capabilities report during one exchange.
//
// It stamps provenance as reports arrive rather than asking reporters to supply
// it, so the attempt a report belongs to cannot be misattributed by a capability
// that held on to a stale value.
//
// Build one with newExchangeSink and never with a struct literal: every
// provenance field has a meaningful zero, so a literal that omits one produces
// entries that are wrong rather than obviously incomplete — an audit row
// claiming candidate 0 refused the payload reads exactly like a real one.
type exchangeSink struct {
	timeline  *fact.Timeline
	reporter  string
	attempt   int
	candidate uint
	provider  uint
	now       func() time.Time

	// batches records each Report call separately. Facts reported together are
	// resolved together, so the grouping has to survive until resolution.
	batches [][]fact.Fact
}

// newExchangeSink builds a sink whose provenance describes where the exchange
// currently stands.
//
// The attempt number is taken as the count of records appended so far, which is
// the index the attempt about to be recorded will occupy: the sink is always
// built before that append, so reports land on the attempt that produced them
// rather than the one after it.
//
// candidate and provider are read through nil checks because the relay clears
// them: a candidate whose provider turned out to be unusable leaves provider
// nil on purpose, and attributing that report to whichever provider happened to
// be set last would be worse than attributing it to none.
func newExchangeSink(rc *Exchange) *exchangeSink {
	s := &exchangeSink{
		timeline: &rc.timeline,
		attempt:  len(rc.attempts),
		now:      time.Now,
	}
	if rc.candidate != nil {
		s.candidate = rc.candidate.ID
	}
	if rc.provider != nil {
		s.provider = rc.provider.ID
	}
	return s
}

func (s *exchangeSink) Report(facts ...fact.Fact) {
	if len(facts) == 0 {
		return
	}
	batch := make([]fact.Fact, 0, len(facts))
	for _, f := range facts {
		if f.Reporter == "" {
			f.Reporter = s.reporter
		}
		batch = append(batch, f)
		s.timeline.Append(fact.Entry{
			Attempt:   s.attempt,
			Candidate: s.candidate,
			Provider:  s.provider,
			At:        s.now(),
			Reporter:  f.Reporter,
			Fact:      &f,
		})
	}
	s.batches = append(s.batches, batch)
}

func (s *exchangeSink) Note(records ...fact.Record) {
	for _, r := range records {
		s.timeline.Append(fact.Entry{
			Attempt:   s.attempt,
			Candidate: s.candidate,
			Provider:  s.provider,
			At:        s.now(),
			Reporter:  s.reporter,
			Record:    r,
		})
	}
}

// resolve folds every batch reported through this sink into one decision.
// Batches fold into each other by the same rule as facts within a batch, so a
// capability reporting twice is indistinguishable from two capabilities
// reporting once — which is the point: the outcome depends on what was said,
// not on who said it or when.
func (s *exchangeSink) resolve() resolved {
	var out resolved
	for _, b := range s.batches {
		out = combine(out, resolveBatch(b))
	}
	return out
}

// observeUpstreamError runs every registered observer over one upstream response and
// folds what they reported into a single verdict.
//
// Observers are run unconditionally rather than short-circuiting on the first
// report: each one sees the whole response and none of them knows what the
// others recognised, so stopping early would make the verdict depend on
// registration order — the one thing the fold is built to rule out.
func (s *Service) observeUpstreamError(ctx context.Context, rc *Exchange, up fact.Upstream) resolved {
	if len(s.upstreamErrorObservers) == 0 {
		return resolved{}
	}
	sink := newExchangeSink(rc)
	for _, o := range s.upstreamErrorObservers {
		sink.reporter = o.name()
		o.observe(ctx, rc, up, sink)
	}
	return sink.resolve()
}
