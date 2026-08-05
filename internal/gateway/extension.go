package gateway

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"time"

	"go.uber.org/zap"

	"github.com/yolorouter/yolorouter/internal/fact"
	"github.com/yolorouter/yolorouter/internal/protocols"
	"github.com/yolorouter/yolorouter/pkg/logger"
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

// EgressRewriterOf rewrites the body about to be sent upstream, once per
// CANDIDATE, after the modality has encoded it and before credentials are
// attached. Key rotation within a candidate reuses the rewritten body — the
// body depends on where it is going, not on which credential sends it — so a
// rewriter must not assume it runs again per key.
//
// It returns the body to send. Returning the input unchanged — or nil, which
// the kernel reads the same way — is how a rewriter declines; there is no
// separate "skip" signal, because a rewriter that has nothing to do and a
// rewriter that decided against acting are the same thing from the kernel's
// side.
//
// An error means "this body is unusable", and it stops the attempt: nothing is
// sent upstream. That makes it the wrong tool for the common case. A rewriter
// that merely could not do its job — a body it cannot parse is a body some
// upstream may still accept — must return the ORIGINAL body and report what
// happened. Reserve the error for a body that must not be sent.
//
// Even then the rewriter does not choose the consequence: it says the body is
// unusable, and the kernel's table decides what that costs the request.
//
// The egress protocol is a parameter rather than something read off the view
// because it belongs to the attempt, not the exchange: the same request can be
// encoded for a different protocol on a later candidate.
type EgressRewriterOf[V any] interface {
	Name() string
	RewriteEgress(ctx context.Context, view V, egress protocols.ProtocolID, body []byte, sink fact.Sink) ([]byte, error)
}

// EgressStage fixes the order of egress rewriters.
//
// Order is supplied at registration rather than declared by the rewriter, and
// that placement is the point: where a rewriter sits relative to the others is
// a property of the pipeline being assembled, not of the rewriter itself. A
// rewriter that renamed a field would have no way to know which other rewriter
// reads the renamed name — whoever composes them does.
//
// It also keeps the constraint from leaking: were the stage part of the
// interface, every rewriter would have to name this type, and naming it means
// importing the kernel — exactly the dependency the split exists to prevent.
//
// The values are spaced so a rewriter can be inserted between two existing ones
// without renumbering, and a collision is a startup failure rather than a tie
// broken silently by registration order.
type EgressStage uint8

const (
	// StageCustomPrompt appends to the system text, so it runs late: it must
	// see the body every other rewriter has finished shaping.
	StageCustomPrompt EgressStage = 50
)

// egressRewriter is the kernel-side, view-erased form.
type egressRewriter interface {
	name() string
	stage() EgressStage
	rewrite(ctx context.Context, e *Exchange, egress protocols.ProtocolID, body []byte, sink fact.Sink) ([]byte, error)
}

type egressRewriterAdapter[V any] struct {
	inner   EgressRewriterOf[V]
	bind    func(*Exchange) V
	atStage EgressStage
}

func (a egressRewriterAdapter[V]) name() string       { return a.inner.Name() }
func (a egressRewriterAdapter[V]) stage() EgressStage { return a.atStage }

func (a egressRewriterAdapter[V]) rewrite(ctx context.Context, e *Exchange, egress protocols.ProtocolID, body []byte, sink fact.Sink) ([]byte, error) {
	return a.inner.RewriteEgress(ctx, a.bind(e), egress, body, sink)
}

// RegisterEgressRewriter wires a rewriter into the service, keeping the slice
// ordered by stage so the run order is settled at assembly rather than
// recomputed per request. Two rewriters claiming the same stage is a
// programming error and panics here, at startup, rather than resolving to
// whichever was registered first.
func RegisterEgressRewriter[V any](s *Service, r EgressRewriterOf[V], at EgressStage, bind func(*Exchange) V) {
	adapter := egressRewriterAdapter[V]{inner: r, bind: bind, atStage: at}
	for _, existing := range s.egressRewriters {
		if existing.stage() == adapter.stage() {
			panic(fmt.Sprintf("gateway: egress rewriters %q and %q both claim stage %d",
				existing.name(), adapter.name(), adapter.stage()))
		}
	}
	s.egressRewriters = append(s.egressRewriters, adapter)
	sort.Slice(s.egressRewriters, func(i, j int) bool {
		return s.egressRewriters[i].stage() < s.egressRewriters[j].stage()
	})
}

// rewriteEgress runs the registered rewriters in stage order over one attempt's
// body, and reports the verdict the kernel should act on.
//
// A rewriter that errors has declared the body unusable, and this stops there.
// Carrying on with the last good body would send upstream exactly what a
// rewriter just refused to produce — the rewriter would have been better off
// never running. What the refusal costs the request is still not the
// rewriter's call: it reports a fact and the table decides, which is why the
// failure comes back as a verdict rather than as an error the caller must
// interpret.
func (s *Service) rewriteEgress(ctx context.Context, rc *Exchange, egress protocols.ProtocolID, body []byte) ([]byte, resolved) {
	if len(s.egressRewriters) == 0 {
		return body, resolved{}
	}
	if ctx == nil {
		// Reached only from a caller that never established a request context.
		// A rewriter that consults ctx should see an inert one rather than a
		// nil that panics on first use.
		ctx = context.Background()
	}
	// The chain describes the body being built now; steps from an earlier
	// candidate describe a body that no longer exists.
	rc.rewriteSteps = nil
	sink := newExchangeSink(rc)
	for _, r := range s.egressRewriters {
		sink.reporter = r.name()
		out, err := r.rewrite(ctx, rc, egress, body, sink)
		if err != nil {
			logger.Warn("gateway: egress rewrite refused the body",
				zap.String("request_id", rc.requestID),
				zap.String("rewriter", r.name()),
				zap.Error(err))
			sink.Report(fact.Fact{
				Kind:   fact.KindEgressRewriteFailed,
				Detail: r.name() + ": " + err.Error(),
			})
			return body, sink.resolve()
		}
		if out != nil && changedBytes(body, out) {
			// The chain is the answer to "who shaped this body": every applied
			// step, in order, with its size effect. One current body plus this
			// list replaces any scheme where rewriters write to separate fields
			// and a later reader has to arbitrate which one is in effect.
			rc.rewriteSteps = append(rc.rewriteSteps, rewriteStep{
				Name:      r.name(),
				ByteDelta: len(out) - len(body),
			})
			body = out
		}
	}
	return body, sink.resolve()
}

// rewriteStep records one applied rewrite for the audit trail.
type rewriteStep struct {
	Name      string
	ByteDelta int
}

// changedBytes reports whether a rewriter actually produced a different body.
// Identity of the backing array is the test: rewriters return their input
// (possibly re-sliced) when they decline. Lengths are compared first so the
// address check never indexes an empty slice.
func changedBytes(before, after []byte) bool {
	if len(after) != len(before) {
		return true
	}
	if len(after) == 0 {
		return false
	}
	return &after[0] != &before[0]
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
//
// Each observer gets its own copy of the response for the same reason. Header
// and Body are a map and a slice, so handing every observer the same value
// would let one that normalises either in place change what the next one sees,
// reintroducing order dependence through a second door. The Body is also the
// bytes already captured for the audit row, so a stray write would rewrite the
// record of what the upstream actually said.
func (s *Service) observeUpstreamError(ctx context.Context, rc *Exchange, up fact.Upstream) resolved {
	if len(s.upstreamErrorObservers) == 0 {
		return resolved{}
	}
	sink := newExchangeSink(rc)
	for _, o := range s.upstreamErrorObservers {
		sink.reporter = o.name()
		o.observe(ctx, rc, isolate(up), sink)
	}
	return sink.resolve()
}

// isolate returns a copy an observer cannot use to reach anything outside
// itself.
//
// The cost is one header clone and one body copy per observer. Error bodies are
// already read under a 1 MiB bound and observers number in the single digits,
// so this buys a structural guarantee for a bounded price — and the alternative,
// trusting every present and future observer to treat its input as read-only,
// is the kind of guarantee that holds until exactly one of them does not.
func isolate(up fact.Upstream) fact.Upstream {
	out := up
	if up.Header != nil {
		out.Header = up.Header.Clone()
	}
	if up.Body != nil {
		out.Body = bytes.Clone(up.Body)
	}
	return out
}

// AdmissionOf gates one exchange before any upstream work, and releases
// whatever it took once the exchange is over.
//
// Admit either takes what the request needs and returns a ticket, or reports
// why the request cannot proceed. It does not decide what a refusal costs: it
// says what it found and the table decides, same as every other shape.
//
// Release is called exactly once for every ticket Admit handed back, on every
// exit path including a panic. There is no separate settle-versus-compensate
// pair: the two differ only in what an implementation does with the outcome it
// is given, and the outcome is a parameter here, so an implementation that
// needs to distinguish them can, and the many that do not are not forced to
// split logic they share.
type AdmissionOf[V, T any] interface {
	Name() string
	Admit(ctx context.Context, view V, sink fact.Sink) (ticket T, held bool)
	Release(ctx context.Context, view V, ticket T, out fact.Outcome, sink fact.Sink)
}

// admission is the kernel-side, view-erased form. The ticket travels as an any:
// the kernel never inspects it, it only hands the same value back.
type admission interface {
	name() string
	admit(ctx context.Context, e *Exchange, sink fact.Sink) (any, bool)
	release(ctx context.Context, e *Exchange, ticket any, out fact.Outcome, sink fact.Sink)
}

type admissionAdapter[V, T any] struct {
	inner AdmissionOf[V, T]
	bind  func(*Exchange) V
}

func (a admissionAdapter[V, T]) name() string { return a.inner.Name() }

func (a admissionAdapter[V, T]) admit(ctx context.Context, e *Exchange, sink fact.Sink) (any, bool) {
	ticket, held := a.inner.Admit(ctx, a.bind(e), sink)
	return ticket, held
}

func (a admissionAdapter[V, T]) release(ctx context.Context, e *Exchange, ticket any, out fact.Outcome, sink fact.Sink) {
	typed, ok := ticket.(T)
	if !ok {
		// Unreachable: the kernel hands back the same value it received from
		// this same adapter. Guarded anyway because a wrong ticket would
		// otherwise release something another admission is holding.
		logger.Error("gateway: admission ticket type mismatch on release",
			zap.String("admission", a.inner.Name()))
		return
	}
	a.inner.Release(ctx, a.bind(e), typed, out, sink)
}

// RegisterAdmission wires an admission into the service.
//
// Registration order is acquisition order, and release runs in reverse — plain
// stack discipline, which is what makes "release what was taken last, first"
// true by construction rather than by everyone agreeing on a set of ordinal
// constants nobody can get wrong only if they are all correct.
func RegisterAdmission[V, T any](s *Service, a AdmissionOf[V, T], bind func(*Exchange) V) {
	s.admissions = append(s.admissions, admissionAdapter[V, T]{inner: a, bind: bind})
}

// heldTicket pairs a ticket with the admission that issued it.
type heldTicket struct {
	by     admission
	ticket any
}

// admit runs the registered admissions in order and stops at the first refusal.
//
// Stopping is not an optimisation: an admission that runs after a refusal would
// take a resource for a request that is already over, and for a rate limiter
// that means charging the caller for a request nobody serves.
//
// Tickets are appended to *held as they are acquired rather than returned at
// the end, so the caller can arm its release BEFORE calling this. If a later
// admission panics, everything taken so far is already recorded where the
// caller's deferred release can see it; had the tickets only appeared in a
// return value, that release would never have been installed and the resources
// would be held until the process restarts.
func (s *Service) admit(ctx context.Context, rc *Exchange, held *[]heldTicket) resolved {
	if len(s.admissions) == 0 {
		return resolved{}
	}
	sink := newExchangeSink(rc)
	for _, a := range s.admissions {
		sink.reporter = a.name()
		ticket, ok := a.admit(ctx, rc, sink)
		if ok {
			*held = append(*held, heldTicket{by: a, ticket: ticket})
		}
		if verdict := sink.resolve(); verdict.Loop >= LoopNextCandidate {
			return verdict
		}
	}
	return sink.resolve()
}

// releaseAdmissions returns everything that was taken, most recent first.
func (s *Service) releaseAdmissions(ctx context.Context, rc *Exchange, held []heldTicket, out fact.Outcome) {
	if len(held) == 0 {
		return
	}
	sink := newExchangeSink(rc)
	for i := len(held) - 1; i >= 0; i-- {
		sink.reporter = held[i].by.name()
		held[i].by.release(ctx, rc, held[i].ticket, out, sink)
	}
}
