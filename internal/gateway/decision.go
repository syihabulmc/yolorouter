package gateway

import (
	"net/http"

	"github.com/yolorouter/yolorouter/internal/fact"
)

// This file is the whole of the kernel's control-flow authority over reported
// facts. A fact turns into a decision here and nowhere else: no other code in
// this package may branch on a fact.
//
// The table is a fixed-size array rather than a map so that a Kind added
// without a row is a detectable state (Defined stays false) instead of an
// all-zero Decision that silently reads as "continue, no status, no
// settlement". A test asserts every Kind has Defined set.
//
// WHAT IS ACTUALLY EXECUTED TODAY
//
// The table describes the intended behaviour of every Kind, but the relay does
// not yet act on all of it. Right now exactly one path consumes a decision —
// the non-2xx branch of attemptOne — and it acts only on a refusal verdict,
// mapping it onto the pre-existing failover classification. Everything else the
// table can express (retry against the same candidate, key rotation, explicit
// termination, status selection, circuit effects, budget accounting,
// stickiness, settlement) is described here and NOT executed.
//
// This is written down rather than left to be discovered because the gap is
// invisible from the table itself: a row exists, the completeness test passes,
// and a capability reporting that Kind gets nothing. Until the executor lands,
// a verdict the relay cannot act on is logged as unexecuted rather than
// dropped, so the gap is at least noisy. Do not add a capability that depends
// on an unexecuted effect before the executor exists — it will compile, pass
// its own tests, and do nothing.

// LoopEffect is what a decision does to the candidate loop.
//
// The values are ordered by STRENGTH, and that ordering is load-bearing:
// folding a batch takes the maximum, so two capabilities reporting at once can
// never produce an outcome weaker than either would alone.
type LoopEffect uint8

const (
	// LoopNone is for decisions that observe without steering.
	LoopNone LoopEffect = iota
	// LoopContinue proceeds with the current attempt.
	LoopContinue
	// LoopRetrySameCandidate rebuilds the upstream body and re-sends it to the
	// same candidate. It costs an attempt, so a repair loop cannot run forever:
	// the budget belongs to the kernel and no reporter can refresh it.
	LoopRetrySameCandidate
	// LoopRotateKey tries another key on the same provider.
	LoopRotateKey
	// LoopNextCandidate abandons this candidate.
	LoopNextCandidate
	// LoopTerminate ends the chain.
	LoopTerminate
	// LoopCommitted means bytes already reached the caller: nothing can move,
	// and the exchange settles wherever it stands.
	LoopCommitted
)

// StatusPolicy says where the caller-facing status comes from.
type StatusPolicy uint8

const (
	// StatusNone contributes no opinion about the status.
	StatusNone StatusPolicy = iota
	// StatusFromPeer defers to the strongest co-reported fact in the same
	// batch. This is what lets one reporter state "this ends the chain" while
	// another states "and the caller should see 503", without either having to
	// know about the other.
	StatusFromPeer
	// StatusFromUpstream forwards the upstream's own status verbatim.
	StatusFromUpstream
	// StatusFixed uses the Code and ErrType on the decision.
	StatusFixed
)

// CircuitEffect is what a decision does to the provider's health record.
type CircuitEffect uint8

const (
	CircuitNone CircuitEffect = iota
	// CircuitReset records a healthy interaction.
	CircuitReset
	// CircuitPenalizeSoft records a fault that says more about load than about
	// health.
	CircuitPenalizeSoft
	// CircuitPenalize records a fault against the provider.
	CircuitPenalize
)

// BudgetEffect is what a decision spends.
//
// The two budgets are deliberately separate. Anything that changes candidate
// without sending a request spends a probe, so a large pool cannot be walked
// end to end for free; anything that actually reached an upstream spends an
// attempt.
type BudgetEffect uint8

const (
	BudgetNone BudgetEffect = iota
	BudgetConsumeProbe
	BudgetConsumeAttempt
)

// StickyEffect records a verdict for the terminal to quote once the chain is
// exhausted, so "every candidate refused this payload" does not surface as a
// generic gateway failure.
type StickyEffect uint8

const (
	StickyNone StickyEffect = iota
	// StickyCandidate is cleared at the start of each candidate: it describes
	// the last candidate only.
	StickyCandidate
	// StickyRequest survives the whole exchange.
	StickyRequest
)

// SettleEffect is what a decision does to outstanding reservations.
type SettleEffect uint8

const (
	SettleNone SettleEffect = iota
	// SettleWithUsage books the actual charge.
	SettleWithUsage
	// SettleSuppress books nothing but leaves reservations to be reversed
	// normally.
	SettleSuppress
	// SettleReverseAll reverses every outstanding reservation, in descending
	// rank order.
	SettleReverseAll
)

// Decision is what the kernel does when a fact is reported. Every field is an
// enum or a constant: there is no func field and no interface, so there is no
// channel through which a reporter could inject behaviour of its own.
type Decision struct {
	Loop    LoopEffect
	Status  StatusPolicy
	Code    int    // meaningful only when Status is StatusFixed
	ErrType string // meaningful only when Status is StatusFixed
	Circuit CircuitEffect
	Budget  BudgetEffect
	Sticky  StickyEffect
	Settle  SettleEffect
	// Defined distinguishes "this row says do nothing" from "nobody wrote this
	// row". Without it a forgotten Kind reads as a valid all-zero decision.
	Defined bool
}

var decisionTable = [fact.NumKinds]Decision{
	fact.KindQuotaExhausted: {
		Loop: LoopTerminate, Status: StatusFixed,
		Code: http.StatusTooManyRequests, ErrType: errTypeInsufficientQuota,
		Settle: SettleReverseAll, Defined: true,
	},
	fact.KindBalanceInsufficient: {
		Loop: LoopTerminate, Status: StatusFixed,
		Code: http.StatusPaymentRequired, ErrType: errTypeInsufficientQuota,
		Settle: SettleReverseAll, Defined: true,
	},
	// Nothing was reserved downstream and no upstream was touched, so there is
	// nothing to reverse beyond the admissions already taken — which the kernel
	// releases on every exit path regardless of verdict.
	fact.KindCallerRateLimited: {
		Loop: LoopTerminate, Status: StatusFixed,
		Code: http.StatusTooManyRequests, ErrType: errTypeRateLimit,
		Defined: true,
	},
	fact.KindPricingUnavailableTerminal: {
		Loop: LoopTerminate, Status: StatusFromPeer,
		Settle: SettleReverseAll, Defined: true,
	},
	fact.KindPricingUnavailableSkip: {
		Loop: LoopNextCandidate, Status: StatusFixed,
		Code: http.StatusServiceUnavailable, ErrType: errTypeUnavailable,
		Budget: BudgetConsumeProbe, Sticky: StickyRequest, Defined: true,
	},
	fact.KindCandidateUnsupported: {
		Loop: LoopNextCandidate, Status: StatusFixed,
		Code: http.StatusBadRequest, ErrType: errTypeInvalidRequest,
		Budget: BudgetConsumeProbe, Sticky: StickyRequest, Defined: true,
	},

	// A refusal is a verdict on the payload, not an outage, so the provider is
	// not penalised for producing it and the caller is told what the upstream
	// actually said. The sticky slot is candidate-scoped: a chain that ends on
	// a transport failure is a fault on our side of the wire, whatever an
	// earlier candidate thought of the payload.
	fact.KindPayloadRefused: {
		Loop: LoopNextCandidate, Status: StatusFromUpstream,
		Circuit: CircuitNone, Budget: BudgetConsumeAttempt,
		Sticky: StickyCandidate, Defined: true,
	},
	fact.KindPayloadRepairedRetrySame: {
		Loop: LoopRetrySameCandidate, Budget: BudgetConsumeAttempt, Defined: true,
	},

	fact.KindUpstreamAuthRejected: {
		Loop: LoopRotateKey, Budget: BudgetConsumeAttempt, Defined: true,
	},
	fact.KindUpstreamRateLimited: {
		Loop: LoopRotateKey, Status: StatusFixed,
		Code: http.StatusTooManyRequests, ErrType: errTypeRateLimit,
		Circuit: CircuitPenalizeSoft, Budget: BudgetConsumeAttempt,
		Sticky: StickyRequest, Defined: true,
	},
	fact.KindUpstreamServerError: {
		Loop: LoopNextCandidate, Circuit: CircuitPenalize,
		Budget: BudgetConsumeAttempt, Defined: true,
	},
	fact.KindUpstreamClientError: {
		Loop: LoopTerminate, Status: StatusFromUpstream,
		Budget: BudgetConsumeAttempt, Settle: SettleWithUsage, Defined: true,
	},
	fact.KindUpstreamTransportFailure: {
		Loop: LoopNextCandidate, Circuit: CircuitPenalize,
		Budget: BudgetConsumeAttempt, Defined: true,
	},
	fact.KindUpstreamPayloadUndecodable: {
		Loop: LoopNextCandidate, Circuit: CircuitPenalize,
		Budget: BudgetConsumeAttempt, Defined: true,
	},
	fact.KindUpstreamSucceeded: {
		Loop: LoopCommitted, Status: StatusFromUpstream,
		Circuit: CircuitReset, Budget: BudgetConsumeAttempt,
		Settle: SettleWithUsage, Defined: true,
	},

	fact.KindUpstreamStreamTruncated: {
		Loop: LoopCommitted, Status: StatusFixed, Code: http.StatusOK,
		Circuit: CircuitPenalizeSoft, Settle: SettleWithUsage, Defined: true,
	},
	// The caller's socket is at fault, so the provider is blameless: it served
	// what it was asked for and the bytes simply had nowhere to go.
	fact.KindClientGone: {
		Loop: LoopCommitted, Status: StatusFixed, Code: StatusClientClosedRequest,
		Circuit: CircuitNone, Settle: SettleWithUsage, Defined: true,
	},
	fact.KindClientWriteFailed: {
		Loop: LoopCommitted, Status: StatusFixed, Code: StatusClientClosedRequest,
		Circuit: CircuitNone, Settle: SettleWithUsage, Defined: true,
	},

	// Nothing was sent, so no attempt is spent — otherwise a provider with
	// several unusable keys would eat the whole budget without one request
	// reaching an upstream.
	fact.KindKeyUnusable: {
		Loop: LoopRotateKey, Defined: true,
	},
	fact.KindRequestBudgetExhausted: {
		Loop: LoopTerminate, Status: StatusFixed,
		Code: http.StatusGatewayTimeout, ErrType: errTypeUpstream,
		Settle: SettleReverseAll, Defined: true,
	},
	fact.KindIngressRewriteFailed: {
		Loop: LoopTerminate, Status: StatusFixed,
		Code: http.StatusInternalServerError, ErrType: errTypeServer,
		Settle: SettleReverseAll, Defined: true,
	},
	fact.KindEgressRewriteFailed: {
		Loop: LoopNextCandidate, Budget: BudgetConsumeProbe, Defined: true,
	},

	// KindNone is never reported; the row exists so the completeness check can
	// require every index to be written.
	fact.KindNone: {Defined: true},
}

// decisionFor is the only lookup into the table.
func decisionFor(k fact.Kind) Decision {
	if int(k) >= len(decisionTable) {
		return Decision{}
	}
	return decisionTable[k]
}

// resolved is a folded batch: the decision the kernel will act on, plus the
// status it resolved to.
type resolved struct {
	Decision
	// statusFrom is the Kind that supplied the status, used to break ties
	// deterministically when two facts in a batch both have an opinion.
	statusFrom fact.Kind
	// loopFrom is the Kind that supplied the winning Loop effect. The kernel
	// needs it to describe WHY the chain moved: without it, every verdict that
	// resolves to the same effect is indistinguishable, and a log line has to
	// guess which of them actually happened.
	loopFrom fact.Kind
	// upstreamStatus carries the verbatim status for StatusFromUpstream.
	upstreamStatus int
	// rejectDetail is the reporter's own words for a refusal, so the caller is
	// told which limit they hit rather than a status code alone.
	rejectDetail string
	// reason is the stable code persisted as the failure reason. Empty when the
	// reporter gave none, in which case the Kind name is the fallback.
	reason string
}

// combine folds two decisions from the same batch.
//
// It must be commutative and associative. If it were not, the outcome would
// depend on the order capabilities happen to be registered in — which is the
// class of bug this whole design exists to prevent, and one that unit tests on
// individual facts cannot see. Every field folds by maximum over its own
// strength ordering; Status carries a payload and so cannot, and breaks ties by
// Kind ordinal instead, which is a total order and therefore keeps the fold
// commutative.
func combine(a, b resolved) resolved {
	out := a
	switch {
	case b.Loop > out.Loop:
		out.Loop, out.loopFrom = b.Loop, b.loopFrom
	case b.Loop == out.Loop && b.loopFrom < out.loopFrom:
		// Equal strength: the lower Kind ordinal attributes the effect, for the
		// same reason the status fold breaks ties that way — an arbitrary rule
		// is fine, an order-dependent one is not.
		out.loopFrom = b.loopFrom
	}
	if b.Circuit > out.Circuit {
		out.Circuit = b.Circuit
	}
	if b.Budget > out.Budget {
		out.Budget = b.Budget
	}
	if b.Sticky > out.Sticky {
		out.Sticky = b.Sticky
	}
	if b.Settle > out.Settle {
		out.Settle = b.Settle
	}
	out.Defined = a.Defined || b.Defined

	// Status cannot fold by maximum because it carries a payload, so it folds
	// by an explicit strength and then by Kind ordinal. Both steps are total
	// orders, which is what keeps the fold commutative — including in the case
	// where NEITHER side has a real opinion, where an "otherwise keep the left
	// operand" rule would quietly make the result depend on argument order.
	if statusWins(b, a) {
		out.Status, out.Code, out.ErrType = b.Status, b.Code, b.ErrType
		out.statusFrom, out.upstreamStatus = b.statusFrom, b.upstreamStatus
		out.rejectDetail, out.reason = b.rejectDetail, b.reason
	}
	return out
}

// statusStrength ranks how much of an opinion a policy expresses.
// StatusFromPeer is deliberately above StatusNone but below a real answer: it
// says "somebody else decides", which is more than saying nothing but is never
// itself the answer.
func statusStrength(p StatusPolicy) int {
	switch p {
	case StatusNone:
		return 0
	case StatusFromPeer:
		return 1
	default: // StatusFromUpstream, StatusFixed
		return 2
	}
}

// statusWins reports whether challenger's status opinion should replace
// holder's. Ties break on the lower Kind ordinal: arbitrary, but total, and
// total is the only property that matters here.
func statusWins(challenger, holder resolved) bool {
	cs, hs := statusStrength(challenger.Status), statusStrength(holder.Status)
	if cs != hs {
		return cs > hs
	}
	return challenger.statusFrom < holder.statusFrom
}

// resolveBatch folds one Report call into a single decision.
func resolveBatch(facts []fact.Fact) resolved {
	var out resolved
	for _, f := range facts {
		r := resolved{
			Decision:       decisionFor(f.Kind),
			statusFrom:     f.Kind,
			loopFrom:       f.Kind,
			upstreamStatus: f.Status,
			rejectDetail:   f.Detail,
			reason:         f.Reason,
		}
		out = combine(out, r)
	}
	return out
}

// admissionRejectionResponse turns a refusal verdict into what the caller sees.
//
// Only StatusFixed is honoured here: an admission runs before any upstream
// exists, so there is no upstream status to forward and no peer fact to defer
// to. A verdict that expressed neither falls back to the generic refusal rather
// than inventing a specific one, because guessing a status is how a rate limit
// ends up reported as a server fault.
func admissionRejectionResponse(v resolved) (status int, errType string) {
	if v.Status == StatusFixed && v.Code != 0 {
		return v.Code, v.ErrType
	}
	return http.StatusTooManyRequests, errTypeRateLimit
}

// failReason is the code persisted for a verdict.
//
// A reporter-supplied Reason wins because it is the stable one: Kind names are
// internal and get renamed as the vocabulary is refined, while this string is
// read by dashboards and log viewers that were written against the old value.
// The Kind name is only the fallback for verdicts nobody gave a code.
func (v resolved) failReason() string {
	if v.reason != "" {
		return v.reason
	}
	return v.loopFrom.String()
}
