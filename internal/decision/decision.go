// Package decision is the whole of the kernel's control-flow authority over
// reported facts. A fact turns into a decision here and nowhere else: no other
// package may branch on a fact, and a structural gate holds the kernel to
// that.
//
// The package imports only the standard library and the fact vocabulary. It
// holds no request state and touches no I/O: everything here is a pure fold
// from reported facts to one resolved verdict the kernel then acts on.
package decision

import (
	"net/http"

	"github.com/yolorouter/yolorouter/internal/fact"
)

// The table is a fixed-size array rather than a map so that a Kind added
// without a row is a detectable state (Defined stays false) instead of an
// all-zero Decision that silently reads as "continue, no status, no
// settlement". A test asserts every Kind has Defined set.
//
// WHAT IS ACTUALLY EXECUTED TODAY
//
// Every effect the table can express has an executor:
//
//   - Loop routes the chain wherever a decision is resolved: the admission
//     refusal path reads Status/Code/ErrType, the egress rewrite path skips a
//     candidate on LoopNextCandidate or stronger, and the non-2xx branch of
//     the attempt loop routes on the resolved Loop — rotate, fail over,
//     terminate, or re-send a repaired body to the same candidate. The kernel
//     is itself a reporter there: when no observer expressed a routing
//     opinion, its own reading of the status line is the baseline fact.
//     LoopRetrySameCandidate executes only when a failure rewriter produced
//     the body to re-send AND the status is one a payload repair can address;
//     otherwise it is logged and the baseline routes.
//   - Sticky is held by the attempt state and quoted by the exhausted-chain
//     terminal.
//   - Budget is a spend ledger on the exchange, charged at the two structural
//     events the rows price uniformly — a dispatch spends an attempt, a
//     candidate abandoned before dispatch spends a probe — and the relay's
//     loops stop when either budget is gone.
//   - Circuit is booked against the per-provider breaker: hard penalties
//     count toward opening it, soft ones count half, resets record health,
//     and the candidate loop consults it before spending work.
//   - Settle is operationalised through fact.Outcome rather than branched on:
//     the settlement seam puts the billed usage and priced cost on the one
//     outcome every Release reads, and usage present-vs-absent is the
//     settle-vs-reverse distinction the rows describe. No kernel code reads
//     the SettleEffect enum itself; the column documents, per row, what the
//     seam's contract produces.
//
// A verdict the relay cannot act on — a retry-same with nothing to re-send —
// is logged as unexecuted rather than dropped, so a gap is at least noisy.

// Caller-facing error "type" values (each failure class maps to one of
// these). They are the vocabulary the table's StatusFixed rows speak and the
// kernel's error envelopes render; kept as untyped string constants so both
// sides can use them wherever a plain string is expected.
const (
	ErrTypeAuthentication    = "authentication_error"
	ErrTypePermission        = "permission_error"
	ErrTypeRateLimit         = "rate_limit_error"
	ErrTypeInvalidRequest    = "invalid_request_error"
	ErrTypeNotFound          = "not_found_error"
	ErrTypeUpstream          = "upstream_error"
	ErrTypeServer            = "server_error"
	ErrTypeUnavailable       = "service_unavailable"
	ErrTypeInsufficientQuota = "insufficient_quota" // OpenAI's type for budget/quota exhaustion (distinct from rate_limit_error)
)

// StatusClientClosedRequest is the non-standard status used to record that the
// caller went away before the response was delivered. It is never written to
// the wire — there is no caller left to receive it — but it must be
// distinguishable in the audit row from a gateway fault, because the two demand
// opposite responses from whoever reads it.
const StatusClientClosedRequest = 499

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
	// StickyAttempt is cleared at the start of each attempt: it describes the
	// one attempt that produced it, and nothing longer.
	//
	// A longer-lived scope was tried and removed. Every verdict worth quoting
	// describes ONE attempt — this candidate has no price, this candidate
	// cannot serve the request, this key is throttled — and letting one outlive
	// the attempt it describes gets the terminal wrong in the case that
	// matters: a key throttled and rotated past, followed by an attempt that
	// genuinely fell over, would report the throttle and send the caller to
	// wait out a rate limit while the provider is down.
	//
	// An attempt is the unit rather than a candidate because a candidate can
	// make several: the key loop clears this on every iteration, before the
	// checks that can pass a key over without ever dispatching it.
	StickyAttempt
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
		Code: http.StatusTooManyRequests, ErrType: ErrTypeInsufficientQuota,
		Settle: SettleReverseAll, Defined: true,
	},
	fact.KindBalanceInsufficient: {
		Loop: LoopTerminate, Status: StatusFixed,
		Code: http.StatusPaymentRequired, ErrType: ErrTypeInsufficientQuota,
		Settle: SettleReverseAll, Defined: true,
	},
	// Nothing was reserved downstream and no upstream was touched, so there is
	// nothing to reverse beyond the admissions already taken — which the kernel
	// releases on every exit path regardless of verdict.
	fact.KindCallerRateLimited: {
		Loop: LoopTerminate, Status: StatusFixed,
		Code: http.StatusTooManyRequests, ErrType: ErrTypeRateLimit,
		Defined: true,
	},
	fact.KindPricingUnavailableTerminal: {
		Loop: LoopTerminate, Status: StatusFromPeer,
		Settle: SettleReverseAll, Defined: true,
	},
	fact.KindPricingUnavailableSkip: {
		Loop: LoopNextCandidate, Status: StatusFixed,
		Code: http.StatusServiceUnavailable, ErrType: ErrTypeUnavailable,
		Budget: BudgetConsumeProbe, Sticky: StickyAttempt, Defined: true,
	},
	fact.KindCandidateUnsupported: {
		Loop: LoopNextCandidate, Status: StatusFixed,
		Code: http.StatusBadRequest, ErrType: ErrTypeInvalidRequest,
		Budget: BudgetConsumeProbe, Sticky: StickyAttempt, Defined: true,
	},

	// A refusal is a verdict on the payload, not an outage, so the provider is
	// not penalised for producing it and the caller is told what the upstream
	// actually said. The sticky slot is attempt-scoped: a chain that ends on a
	// transport failure is a fault on our side of the wire, whatever an earlier
	// attempt thought of the payload.
	fact.KindPayloadRefused: {
		Loop: LoopNextCandidate, Status: StatusFromUpstream,
		Circuit: CircuitNone, Budget: BudgetConsumeAttempt,
		Sticky: StickyAttempt, Defined: true,
	},
	fact.KindPayloadRepairedRetrySame: {
		Loop: LoopRetrySameCandidate, Budget: BudgetConsumeAttempt, Defined: true,
	},

	fact.KindUpstreamAuthRejected: {
		Loop: LoopRotateKey, Budget: BudgetConsumeAttempt, Defined: true,
	},
	fact.KindUpstreamRateLimited: {
		Loop: LoopRotateKey, Status: StatusFixed,
		Code: http.StatusTooManyRequests, ErrType: ErrTypeRateLimit,
		Circuit: CircuitPenalizeSoft, Budget: BudgetConsumeAttempt,
		Sticky: StickyAttempt, Defined: true,
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
		Code: http.StatusGatewayTimeout, ErrType: ErrTypeUpstream,
		Settle: SettleReverseAll, Defined: true,
	},
	fact.KindIngressRewriteFailed: {
		Loop: LoopTerminate, Status: StatusFixed,
		Code: http.StatusInternalServerError, ErrType: ErrTypeServer,
		Settle: SettleReverseAll, Defined: true,
	},
	fact.KindEgressRewriteFailed: {
		Loop: LoopNextCandidate, Budget: BudgetConsumeProbe, Defined: true,
	},

	// KindNone is never reported; the row exists so the completeness check can
	// require every index to be written.
	fact.KindNone: {Defined: true},
}

// For is the only lookup into the table.
func For(k fact.Kind) Decision {
	if int(k) >= len(decisionTable) {
		return Decision{}
	}
	return decisionTable[k]
}

// Resolved is a folded batch: the decision the kernel will act on, plus the
// status it resolved to.
type Resolved struct {
	Decision
	// statusFrom is the Kind that supplied the status, used to break ties
	// deterministically when two facts in a batch both have an opinion.
	statusFrom fact.Kind
	// loopFrom is the Kind that supplied the winning Loop effect. The kernel
	// needs it to describe WHY the chain moved: without it, every verdict that
	// resolves to the same effect is indistinguishable, and a log line has to
	// guess which of them actually happened.
	loopFrom fact.Kind
	// rejectDetail is the reporter's own words for a refusal, so the caller is
	// told which limit they hit rather than a status code alone.
	rejectDetail string
	// reason is the stable code persisted as the failure reason. Empty when the
	// reporter gave none, in which case the Kind name is the fallback.
	reason string
}

// LoopFrom names the fact that supplied the winning Loop effect.
func (v Resolved) LoopFrom() fact.Kind { return v.loopFrom }

// RejectDetail is the winning reporter's own words for a refusal, empty when
// it gave none.
func (v Resolved) RejectDetail() string { return v.rejectDetail }

// Combine folds two decisions from the same batch.
//
// It must be commutative and associative. If it were not, the outcome would
// depend on the order capabilities happen to be registered in — which is the
// class of bug this whole design exists to prevent, and one that unit tests on
// individual facts cannot see. Every field folds by maximum over its own
// strength ordering; Status carries a payload and so cannot, and breaks ties by
// Kind ordinal instead, which is a total order and therefore keeps the fold
// commutative.
func Combine(a, b Resolved) Resolved {
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
	if b.Settle > out.Settle {
		out.Settle = b.Settle
	}
	out.Defined = a.Defined || b.Defined

	// Status cannot fold by maximum because it carries a payload, so it folds
	// by an explicit strength and then by Kind ordinal. Both steps are total
	// orders, which is what keeps the fold commutative — including in the case
	// where NEITHER side has a real opinion, where an "otherwise keep the left
	// operand" rule would quietly make the result depend on argument order.
	// The caller-facing verdict is adopted whole, from one fact, in one
	// statement. Everything the caller ends up seeing or that gets persisted
	// travels together: the status, the error type, the words, the reason, and
	// whether the verdict is worth remembering.
	//
	// Folding those apart is a mistake this package has now made twice. Each
	// time the shape was the same — one fact supplied part of the answer and
	// another supplied the rest, and the pair was reported as though a single
	// reporter had said both. A caller was told their quota ran out when their
	// payload had been refused; a row was filed under a reason belonging to
	// neither. Splitting the assignment is what made those possible, so the
	// assignment is not split.
	//
	// Which fact wins is decided by statusWins: first by how strong a claim the
	// row makes about the status, then — and this is what settles the quota
	// case, since a fixed status and a forwarded one are equally strong — by
	// Kind, lowest first.
	if statusWins(b, a) {
		out.adoptVerdict(b)
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
func statusWins(challenger, holder Resolved) bool {
	cs, hs := statusStrength(challenger.Status), statusStrength(holder.Status)
	if cs != hs {
		return cs > hs
	}
	if challenger.statusFrom != holder.statusFrom {
		return challenger.statusFrom < holder.statusFrom
	}
	// Two facts of the SAME Kind, which the Kind ordinal cannot separate. Left
	// unbroken the comparison answers false both ways, the fold keeps whichever
	// operand came first, and two capabilities reporting the same Kind with
	// different words would hand the caller a different message depending on
	// which was registered first — the exact dependence on registration order
	// this whole design exists to remove.
	//
	// Broken on the words themselves, the answer stops depending on anything
	// but the facts. Which of two same-Kind reports wins is arbitrary; that it
	// is the SAME one every time is not.
	if challenger.reason != holder.reason {
		return challenger.reason < holder.reason
	}
	return challenger.rejectDetail < holder.rejectDetail
}

// ResolveBatch folds one Report call into a single decision.
func ResolveBatch(facts []fact.Fact) Resolved {
	var out Resolved
	for _, f := range facts {
		r := Resolved{
			Decision:     For(f.Kind),
			statusFrom:   f.Kind,
			loopFrom:     f.Kind,
			rejectDetail: f.Detail,
			reason:       f.Reason,
		}
		out = Combine(out, r)
	}
	return out
}

// StickyVerdict is a verdict held for the terminal to quote once the chain is
// exhausted, so "every candidate refused this payload" reaches the caller as
// what it is rather than as a generic gateway failure.
//
// The kernel holds it without knowing which capability produced it. That is the
// whole point of the slot: the terminal used to carry a pair of fields named
// after one capability's concern, which meant a second capability wanting the
// same treatment had to add its own pair and its own branch there.
type StickyVerdict struct {
	Status  int
	ErrType string
	Detail  string
	Reason  string
}

// Held reports whether anything was recorded.
func (s StickyVerdict) Held() bool { return s.Status != 0 }

// CallerFacing renders the status and error type this verdict asks the caller
// to be shown, or a zero status when it has no opinion.
//
// The upstream's own status and classification are passed in rather than read
// off the decision, which does not carry them: StatusFromUpstream means
// "whatever the peer actually answered with", and the response is held by the
// call site.
func (v Resolved) CallerFacing(upstreamStatus int, upstreamErrType string) (int, string) {
	switch v.Status {
	case StatusFixed:
		return v.Code, v.ErrType
	case StatusFromUpstream:
		return upstreamStatus, upstreamErrType
	default:
		return 0, ""
	}
}

// AdmissionRejectionResponse turns a refusal verdict into what the caller sees.
//
// Only StatusFixed is honoured here: an admission runs before any upstream
// exists, so there is no upstream status to forward and no peer fact to defer
// to. A verdict that expressed neither falls back to the generic refusal rather
// than inventing a specific one, because guessing a status is how a rate limit
// ends up reported as a server fault.
func AdmissionRejectionResponse(v Resolved) (status int, errType string) {
	return PreDispatchRejectionResponse(v, http.StatusTooManyRequests, ErrTypeRateLimit)
}

// PreDispatchRejectionResponse is the same reading for any refusal raised
// before an upstream exists, with the fallback supplied by the caller.
//
// The fallback is a parameter because it is the one part that cannot be shared:
// what a verdict without a fixed status should degrade to depends entirely on
// what refused. An admission that expressed nothing is a rate limit; a rewriter
// that could not produce a sendable body is a fault on this side of the wire,
// and reporting that as a rate limit would tell the caller to retry something
// no amount of waiting will fix.
func PreDispatchRejectionResponse(v Resolved, fallbackStatus int, fallbackErrType string) (status int, errType string) {
	if v.Status == StatusFixed && v.Code != 0 {
		return v.Code, v.ErrType
	}
	return fallbackStatus, fallbackErrType
}

// adoptVerdict takes the whole caller-facing verdict from one fact.
//
// It exists so there is exactly one place the verdict can be assigned, which is
// what makes "every part of it came from the same fact" true by construction
// rather than by everyone remembering to keep the fields together. A gate holds
// Combine to assigning these fields only through here.
func (v *Resolved) adoptVerdict(from Resolved) {
	v.Status, v.Code, v.ErrType = from.Status, from.Code, from.ErrType
	v.Sticky = from.Sticky
	v.statusFrom = from.statusFrom
	v.rejectDetail, v.reason = from.rejectDetail, from.reason
}

// FailReason is the code persisted for a verdict.
//
// A reporter-supplied Reason wins because it is the stable one: Kind names are
// internal and get renamed as the vocabulary is refined, while this string is
// read by dashboards and log viewers that were written against the old value.
//
// The fallback names the fact that supplied the verdict, not the one that
// supplied the loop effect. Those can be different facts in the same batch, and
// naming the loop's would file the row under something that contributed neither
// the status the caller saw nor the words they were given — an audit trail
// pointing at a fact that had nothing to do with the answer.
func (v Resolved) FailReason() string {
	if v.reason != "" {
		return v.reason
	}
	return v.statusFrom.String()
}
