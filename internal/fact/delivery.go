package fact

import "fmt"

// Delivery is what a modality adapter reports about one attempt to get a
// response to the caller.
//
// It exists to separate two things that are one variable today. The relay
// currently settles an attempt by passing a status code to a single function,
// and that code has to mean both "what the caller saw" and "what to settle on".
// Those genuinely differ: a stream that opened with 200 and then died still
// showed the caller a 200, while settlement has to reason about it as a
// failure. Expressing that with one number means every entry point re-invents
// the same convention, and no two of them agree for long.
//
// The adapter states facts about the delivery. It does not settle, bill, or
// write the log row, and the verdict it offers is a floor rather than an
// instruction: it says what is still POSSIBLE — once bytes are on the wire,
// nothing is — and the kernel decides what actually happens.
type Delivery struct {
	// Committed is true once anything at all reached the caller. After that,
	// no other candidate can serve this request.
	Committed bool
	// ClientStatus is the status the caller observed. Meaningful only when
	// Committed: it is frozen at the moment the response headers went out.
	ClientStatus int
	// Verdict is what remains possible after this delivery.
	Verdict Verdict
	// BillingStatus is what settlement reasons about. It may differ from
	// ClientStatus, and that difference is the whole point of this type.
	BillingStatus int
	// Complete says the caller actually received the whole payload.
	//
	// It is an AUDIT signal and is deliberately not wired into billing: the
	// current charge condition contains no completeness test, so making one
	// would be a revenue change rather than a refactor. Whether a truncated
	// response should be refunded is a separate decision that needs data this
	// field is meant to start collecting.
	Complete bool
	// Fault attributes the failure. Deliberately independent of the statuses:
	// a 200 that the caller never finished reading is the caller's fault, and
	// a 200 whose stream died mid-flight is the upstream's.
	Fault Fault
	// Usage is what this delivery's own attempt reported as billable.
	//
	// It belongs to the Delivery rather than to the exchange because an attempt
	// can report what it charged before producing anything a caller can see,
	// and then fail. Left on the exchange, those numbers outlive the attempt
	// that earned them and are charged under whatever the request eventually
	// settles as — a later candidate's success, or the 502 that ends the chain.
	// Carried here, they can only ever be read together with the attempt they
	// describe. Nil when nothing was reported.
	Usage *UsageReported
	// Err is the underlying failure. Logged when the request settles; never
	// persisted — FailReason is the code downstream reads.
	Err error
	// FailReason is the stable, persisted code. Same contract as Fact.Reason:
	// it is read by dashboards, so it must not be derived from a constant name.
	FailReason string
}

// Verdict is what remains possible after a delivery attempt.
type Verdict uint8

const (
	// VerdictUnset is the zero value and is never valid. It exists so an
	// uninitialised Delivery fails the kernel's check instead of silently
	// claiming the request is over.
	VerdictUnset Verdict = iota
	// VerdictSettled: this delivery ends the request, whatever its outcome.
	VerdictSettled
	// VerdictNextCandidate: nothing reached the caller, so the chain may
	// continue.
	//
	// There is deliberately no separate "rotate to another key" verdict. Only
	// a delivery attempt produces a Delivery, and by then a key has already
	// been accepted; the credential-level retries are decided before anything
	// is delivered. Adding one for symmetry would be a value nothing can
	// construct and a branch nothing can reach.
	VerdictNextCandidate
)

func (v Verdict) String() string {
	switch v {
	case VerdictSettled:
		return "settled"
	case VerdictNextCandidate:
		return "next_candidate"
	default:
		return "unset"
	}
}

// Fault attributes a failed delivery to whichever side caused it.
type Fault uint8

const (
	// FaultNone: nothing failed.
	FaultNone Fault = iota
	// FaultClient: the caller's socket. Must never penalise the provider.
	FaultClient
	// FaultUpstream: the provider failed on its own side.
	FaultUpstream
	// FaultGateway: we did. Kept distinct from FaultUpstream so that our own
	// failures cannot later be counted against a provider that had no part in
	// them, once anything starts acting on this field.
	FaultGateway
)

func (f Fault) String() string {
	switch f {
	case FaultClient:
		return "client"
	case FaultUpstream:
		return "upstream"
	case FaultGateway:
		return "gateway"
	default:
		return "none"
	}
}

// Succeeded reports a delivery that reached the caller whole.
func Succeeded(status int) Delivery {
	return Delivery{
		Committed:     true,
		ClientStatus:  status,
		Verdict:       VerdictSettled,
		BillingStatus: status,
		Complete:      true,
		Fault:         FaultNone,
	}
}

// Truncated reports a delivery that started reaching the caller and then did
// not finish. billingStatus is free to differ from clientStatus — that is the
// case this constructor exists for.
func Truncated(clientStatus, billingStatus int, at Fault, failReason string, err error) Delivery {
	return Delivery{
		Committed:     true,
		ClientStatus:  clientStatus,
		Verdict:       VerdictSettled,
		BillingStatus: billingStatus,
		Complete:      false,
		Fault:         at,
		Err:           err,
		FailReason:    failReason,
	}
}

// Rejected reports a request answered with an error instead of a response.
//
// The caller received that error whole, so this is committed and settled. It is
// still not Complete: Complete asks whether the caller got the payload they
// asked for, and a rejection is the case where there was never going to be one.
func Rejected(status int, at Fault, failReason string, err error) Delivery {
	return Delivery{
		Committed:     true,
		ClientStatus:  status,
		Verdict:       VerdictSettled,
		BillingStatus: status,
		Complete:      false,
		Fault:         at,
		Err:           err,
		FailReason:    failReason,
	}
}

// Undelivered reports that nothing reached the caller, so the chain may still
// continue — next says how far.
func Undelivered(billingStatus int, next Verdict, at Fault, failReason string, err error) Delivery {
	return Delivery{
		Committed:     false,
		Verdict:       next,
		BillingStatus: billingStatus,
		Complete:      false,
		Fault:         at,
		Err:           err,
		FailReason:    failReason,
	}
}

// WithUsage attaches what this attempt reported as billable.
//
// A method rather than a parameter on every constructor: most deliveries have
// nothing to report, and threading a nil through six signatures would put the
// field in front of every call site that has no use for it while saying
// nothing about the one invariant that matters — that the numbers travel with
// the attempt that produced them.
func (d Delivery) WithUsage(u *UsageReported) Delivery {
	if u == nil {
		d.Usage = nil
		return d
	}
	// Copied, not aliased. A payload that accumulates usage across attempts
	// hands out the same pointer every time, and a later attempt writing into
	// it would retroactively change what an earlier delivery reported — which
	// is the exact invariant this field exists to hold.
	snapshot := *u
	d.Usage = &snapshot
	return d
}

// Validate reports why a Delivery cannot describe anything that could actually
// have happened.
//
// The type has enough independent fields to express states with no physical
// meaning, and the compiler cannot tell them apart from real ones. Without this
// check the type would be exactly what it replaced — a tuple of loosely related
// values, with the rules living in whichever call site last remembered them.
// The kernel calls this on every Delivery it receives and refuses the ones that
// fail.
//
// One rule is deliberately NOT here: "a client-side fault must not count
// against a provider's health record" constrains what the KERNEL does with a
// Delivery, not whether the Delivery is coherent. It belongs wherever Fault
// starts driving that decision.
func (d Delivery) Validate() error {
	if d.Verdict == VerdictUnset {
		return fmt.Errorf("delivery: verdict is unset")
	}
	if d.BillingStatus == 0 {
		return fmt.Errorf("delivery: billing status is unset, settlement has nothing to reason about")
	}
	if d.Committed && d.Verdict != VerdictSettled {
		return fmt.Errorf("delivery: committed but verdict is %s: bytes reached the caller, so no other candidate can serve this request", d.Verdict)
	}
	if d.Committed && d.ClientStatus == 0 {
		return fmt.Errorf("delivery: committed but no client status: the caller received something, so they saw a status")
	}
	if !d.Committed && d.ClientStatus != 0 {
		return fmt.Errorf("delivery: client status %d but nothing was committed: the caller saw no status at all", d.ClientStatus)
	}
	if d.Complete && !d.Committed {
		return fmt.Errorf("delivery: complete but nothing was committed")
	}
	if d.Complete && d.BillingStatus >= 400 {
		return fmt.Errorf("delivery: complete but billing status is %d", d.BillingStatus)
	}
	if d.Complete && d.Fault != FaultNone {
		return fmt.Errorf("delivery: complete but blamed on %s", d.Fault)
	}
	return nil
}

// DeliveryObserved carries the delivery's shape into the audit trail.
//
// Complete has no column of its own and is not supposed to get one yet: it is
// being collected to find out how often each kind of incomplete delivery
// happens, which is a question about data we do not have rather than a schema
// we already need. Reporting it as a record puts it in the overflow column
// today, and a column can follow if the numbers justify one.
type DeliveryObserved struct {
	Base
	Committed     bool
	Complete      bool
	ClientStatus  int
	BillingStatus int
	Fault         string
}

// RecordName implements Record.
func (DeliveryObserved) RecordName() string { return "delivery_observed" }

// Observed summarises this delivery for the audit trail.
func (d Delivery) Observed() DeliveryObserved {
	return DeliveryObserved{
		Committed:     d.Committed,
		Complete:      d.Complete,
		ClientStatus:  d.ClientStatus,
		BillingStatus: d.BillingStatus,
		Fault:         d.Fault.String(),
	}
}
