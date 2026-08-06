package gateway

import (
	"context"
	"testing"

	"github.com/yolorouter/yolorouter/internal/fact"
)

// Fake money. Two capabilities a deployment can wire into the admission seam
// but none does here: a balance that is pre-deducted before the request runs
// and reconciled after, and a sub-request charge that has to be reversed before
// the balance it was taken from.
//
// Neither implements any pricing. What is under test is the shape: whether the
// admission seam can express a reservation that is sometimes not taken at all,
// a reversal that must not run twice, and an order between two reversals that
// is a real constraint rather than a comment.

// quotaState is the three-way answer a real quota gives, and the reason this
// capability is worth faking. Two of the three are easy and the third is where
// implementations go wrong: an account with no limit set must not be treated as
// an account with a limit of zero, and it must not reserve anything either —
// a reservation it never took is one the release path would give back.
type quotaState uint8

const (
	quotaUnlimited quotaState = iota
	quotaWithinLimit
	quotaExhausted
)

// prepaidTicket is this capability's OWN per-request state. It never lands on
// the Exchange: the kernel stores it opaquely and hands the same value back, so
// it stays typed from one end to the other.
type prepaidTicket struct {
	reservedMicros int64
}

// fakePrepaidBalance pre-deducts before the request runs and reconciles after.
type fakePrepaidBalance struct {
	state    quotaState
	balance  int64
	reserve  int64
	log      *[]string
	released int
}

func (*fakePrepaidBalance) Name() string { return "fake_prepaid_balance" }

func (f *fakePrepaidBalance) Admit(_ context.Context, _ fact.Attempt, sink fact.Sink) (prepaidTicket, bool) {
	switch f.state {
	case quotaExhausted:
		sink.Report(fact.Fact{Kind: fact.KindBalanceInsufficient, Detail: "no funds"})
		return prepaidTicket{}, false
	case quotaUnlimited:
		// Nothing taken, so nothing to give back. Returning held=true here is
		// the mistake this state exists to catch: the kernel would call Release
		// and this capability would credit a balance it never debited.
		*f.log = append(*f.log, "admit:unlimited")
		return prepaidTicket{}, false
	default:
		f.balance -= f.reserve
		*f.log = append(*f.log, "admit:reserved")
		return prepaidTicket{reservedMicros: f.reserve}, true
	}
}

func (f *fakePrepaidBalance) Release(_ context.Context, _ fact.Attempt, t prepaidTicket, out fact.Outcome, _ fact.Sink) {
	f.released++
	*f.log = append(*f.log, "release:balance")
	if out.Delivered {
		// Served: the reservation becomes the charge. A real one would settle
		// the difference against measured usage.
		return
	}
	f.balance += t.reservedMicros
}

// visionState is the three-way answer the sub-request capability gives, and the
// reason it is worth faking separately from the balance above.
//
// The balance's three states are about how much room an account has. These
// three are about something else: whether this exchange incurred a sub-request
// charge AT ALL, and if it did, whether it has already been settled. An
// implementation that collapses "no image in the request" into "charged zero"
// reverses a charge that was never made; one that collapses "already settled"
// into "still owed" reverses it twice.
type visionState uint8

const (
	visionNoImage visionState = iota
	visionCharged
	visionAlreadySettled
)

// subRequestTicket is the sub-call charge's own state, and it carries TWO
// figures rather than one.
//
// gross is what the second provider charged us; net is what the caller is
// charged for it. They differ by margin, and both have to survive to the
// reversal: refunding gross gives back money that was never taken from the
// caller, and refunding net leaves the margin booked against an exchange that
// did not happen. Nothing outside this capability can hold them — the kernel
// stores the ticket opaquely, which is what keeps the pair together and typed.
//
// They live on the ticket rather than on the capability because the capability
// is shared across concurrent exchanges: a figure kept there would be whichever
// request happened to run most recently.
type subRequestTicket struct {
	state visionState
	gross int64
	net   int64
}

// fakeSubRequestCharge stands in for a capability that spends money on a
// SECOND upstream before the main one runs — a vision model called to describe
// an image, whose cost is charged separately and must be reversed FIRST when
// the exchange fails, because it was taken out of the balance the other
// admission is holding.
type fakeSubRequestCharge struct {
	state visionState
	gross int64
	net   int64

	log      *[]string
	reversed []subRequestTicket
}

func (*fakeSubRequestCharge) Name() string { return "fake_sub_request_charge" }

func (f *fakeSubRequestCharge) Admit(_ context.Context, _ fact.Attempt, _ fact.Sink) (subRequestTicket, bool) {
	switch f.state {
	case visionNoImage:
		// No second upstream ran, so there is nothing to reverse. Returning
		// held=true here is the mistake: the kernel would call Release and this
		// capability would credit a charge it never made.
		*f.log = append(*f.log, "admit:vision-no-image")
		return subRequestTicket{state: visionNoImage}, false
	case visionAlreadySettled:
		// Charged and reconciled in the same breath — nothing is outstanding.
		*f.log = append(*f.log, "admit:vision-already-settled")
		return subRequestTicket{state: visionAlreadySettled}, false
	default:
		*f.log = append(*f.log, "admit:subrequest")
		return subRequestTicket{state: visionCharged, gross: f.gross, net: f.net}, true
	}
}

func (f *fakeSubRequestCharge) Release(_ context.Context, _ fact.Attempt, t subRequestTicket, _ fact.Outcome, _ fact.Sink) {
	// The whole ticket is kept, state included. The state is read from the
	// TICKET rather than from the capability because the capability is shared
	// across concurrent exchanges — its own field says what the most recent
	// request happened to be, and only the ticket says what THIS one was.
	//
	// Kept rather than checked here: a reversal handed a ticket for a state that
	// never charged anything means the held flag was wrong, and the test that
	// cares says so with an assertion. Refusing inside the capability would put
	// the complaint somewhere the kernel's own panic guard swallows.
	*f.log = append(*f.log, "release:subrequest")
	f.reversed = append(f.reversed, t)
}

func attemptView(*Exchange) fact.Attempt { return fact.Attempt{} }

// TestAQuotaWithNoLimitReservesNothingAndIsNotReleased is the three-state test.
//
// The failure it guards against is not a refusal that should have been an
// admission — that one shows up immediately. It is the account with no limit
// configured: an implementation that returns a ticket anyway gets Release
// called on a reservation it never made, and credits a balance for a debit that
// never happened. Nothing about the types prevents it; the held flag is the
// only thing that does.
func TestAQuotaWithNoLimitReservesNothingAndIsNotReleased(t *testing.T) {
	for _, tc := range []struct {
		name        string
		state       quotaState
		wantAdmit   bool
		wantRelease int
		wantBalance int64
		wantLog     []string
	}{
		// The balance is untouched here for a different reason than in the row
		// below it: nothing was ever debited, rather than a debit being undone.
		// wantLog is what tells the two apart — on its own, wantBalance: 500
		// passes for both, and for a capability that did nothing at all.
		{name: "no limit configured", state: quotaUnlimited, wantAdmit: true, wantRelease: 0,
			wantBalance: 500, wantLog: []string{"admit:unlimited"}},
		// Served, so the reservation stays spent — this row is what makes the
		// row above mean something: both admit, and only one owes a release.
		{name: "within the limit", state: quotaWithinLimit, wantAdmit: true, wantRelease: 1,
			wantBalance: 380, wantLog: []string{"admit:reserved", "release:balance"}},
		{name: "out of funds", state: quotaExhausted, wantAdmit: false, wantRelease: 0,
			wantBalance: 500, wantLog: nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var log []string
			svc := &Service{}
			money := &fakePrepaidBalance{state: tc.state, balance: 500, reserve: 120, log: &log}
			RegisterAdmission(svc, money, attemptView)

			rc := &Exchange{}
			var held []heldTicket
			verdict := svc.admit(context.Background(), rc, &held)

			admitted := verdict.Loop == LoopNone
			if admitted != tc.wantAdmit {
				t.Fatalf("admitted = %v (loop %v), want %v", admitted, verdict.Loop, tc.wantAdmit)
			}
			svc.releaseAdmissions(context.Background(), rc, held, fact.Outcome{Delivered: true})

			if money.released != tc.wantRelease {
				t.Errorf("Release ran %d times, want %d: a reservation that was never taken "+
					"must not be given back", money.released, tc.wantRelease)
			}
			if money.balance != tc.wantBalance {
				t.Errorf("balance = %d, want %d", money.balance, tc.wantBalance)
			}
			if len(log) != len(tc.wantLog) {
				t.Fatalf("log = %v, want %v", log, tc.wantLog)
			}
			for i := range tc.wantLog {
				if log[i] != tc.wantLog[i] {
					t.Fatalf("log = %v, want %v: an unchanged balance is not evidence of the "+
						"right decision — two of these rows leave it at 500", log, tc.wantLog)
				}
			}
		})
	}
}

// TestAnUnservedRequestGetsItsReservationBack is the refund half.
//
// The reservation is taken before anybody knows whether an upstream will answer,
// so the path where none does is the one that has to give it back — and it is
// reached through the same Release as the served path, with the outcome as the
// only thing that differs. That is deliberate: a separate compensate method
// would be a second place for the reversal to be forgotten.
func TestAnUnservedRequestGetsItsReservationBack(t *testing.T) {
	var log []string
	svc := &Service{}
	money := &fakePrepaidBalance{state: quotaWithinLimit, balance: 500, reserve: 120, log: &log}
	RegisterAdmission(svc, money, attemptView)

	rc := &Exchange{}
	var held []heldTicket
	svc.admit(context.Background(), rc, &held)
	if money.balance != 380 {
		t.Fatalf("balance after reserving = %d, want 380; the fixture never debited anything", money.balance)
	}

	svc.releaseAdmissions(context.Background(), rc, held, fact.Outcome{Delivered: false, StatusCode: 502})

	if money.balance != 500 {
		t.Errorf("balance = %d, want 500: nobody was served, so nobody should have paid", money.balance)
	}
	if money.released != 1 {
		t.Errorf("Release ran %d times, want exactly 1 — a refund that runs twice pays the caller "+
			"back for money they were never charged", money.released)
	}
}

// TestTheSubRequestChargeIsReversedBeforeTheBalanceItCameOutOf pins an ordering
// that is a real constraint and has nowhere else to live.
//
// The sub-request was paid for out of the balance the other admission is
// holding. Reverse the balance first and the sub-request charge is credited
// against a reconciliation that has already happened. Stack discipline makes
// "last taken, first released" true by construction — but WHICH order that is
// remains a property of two lines in the assembly function, which is why the
// kernel exposes the order and this test reads it.
func TestTheSubRequestChargeIsReversedBeforeTheBalanceItCameOutOf(t *testing.T) {
	var log []string
	svc := &Service{}
	RegisterAdmission(svc, &fakePrepaidBalance{state: quotaWithinLimit, balance: 500, reserve: 120, log: &log}, attemptView)
	RegisterAdmission(svc, &fakeSubRequestCharge{state: visionCharged, gross: 40, net: 52, log: &log}, attemptView)

	if got := svc.RegisteredAdmissions(); len(got) != 2 ||
		got[0] != "fake_prepaid_balance" || got[1] != "fake_sub_request_charge" {
		t.Fatalf("acquisition order = %v, want the balance taken first: the order below follows from it", got)
	}

	rc := &Exchange{}
	var held []heldTicket
	svc.admit(context.Background(), rc, &held)
	svc.releaseAdmissions(context.Background(), rc, held, fact.Outcome{Delivered: false})

	want := []string{"admit:reserved", "admit:subrequest", "release:subrequest", "release:balance"}
	if len(log) != len(want) {
		t.Fatalf("log = %v, want %v", log, want)
	}
	for i := range want {
		if log[i] != want[i] {
			t.Fatalf("log = %v, want %v: the sub-request charge must be reversed before the balance it came out of", log, want)
		}
	}
}

// TestTheSubRequestReversalCarriesBothFiguresAndOnlyWhenSomethingWasCharged is
// the sub-request capability's own three-state test.
//
// The balance above is about how much room an account has. This is about
// whether a second upstream ran at all. Both of the states that did not charge
// must come back without a ticket the kernel will later reverse — and the one
// that did has to carry gross and net together, because reversing either one
// alone leaves the books wrong in a different direction.
func TestTheSubRequestReversalCarriesBothFiguresAndOnlyWhenSomethingWasCharged(t *testing.T) {
	for _, tc := range []struct {
		name        string
		state       visionState
		wantAdmit   string
		wantReverse bool
	}{
		{name: "no image in the request", state: visionNoImage, wantAdmit: "admit:vision-no-image"},
		{name: "charged and still outstanding", state: visionCharged, wantAdmit: "admit:subrequest", wantReverse: true},
		{name: "already settled", state: visionAlreadySettled, wantAdmit: "admit:vision-already-settled"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var log []string
			svc := &Service{}
			vision := &fakeSubRequestCharge{state: tc.state, gross: 40, net: 52, log: &log}
			RegisterAdmission(svc, vision, attemptView)

			rc := &Exchange{}
			var held []heldTicket
			svc.admit(context.Background(), rc, &held)
			svc.releaseAdmissions(context.Background(), rc, held, fact.Outcome{Delivered: false})

			if len(log) == 0 || log[0] != tc.wantAdmit {
				t.Fatalf("log = %v, want it to open with %q", log, tc.wantAdmit)
			}
			if !tc.wantReverse {
				if len(vision.reversed) != 0 {
					t.Fatalf("reversed %+v, want nothing: no second upstream was owed anything",
						vision.reversed)
				}
				return
			}
			if len(vision.reversed) != 1 {
				t.Fatalf("reversed %d times, want exactly 1", len(vision.reversed))
			}
			got := vision.reversed[0]
			// The ticket says which exchange this reversal belongs to. A ticket
			// arriving here for a state that never charged means the held flag
			// was wrong, and the reversal is crediting something that was never
			// taken.
			if got.state != visionCharged {
				t.Errorf("reversing a ticket in state %d; only a charged one is owed anything back",
					got.state)
			}
			if got.gross != 40 {
				t.Errorf("reversed gross = %d, want 40: this is what the second provider took, "+
					"and a reconciliation that forgets it leaves us paying for it", got.gross)
			}
			if got.net != 52 {
				t.Errorf("reversed net = %d, want 52: this is what the caller was charged, and "+
					"refunding gross instead gives back money they never paid", got.net)
			}
		})
	}
}

// TestOneAdmissionPanickingStillReversesTheOthers is why the release loop
// guards each capability the way the observation loops do.
//
// Releases run from a defer on the way out, after the caller has been served,
// and they run in reverse order because each one may be undoing something the
// next still holds. An unguarded panic in the middle escapes into the HTTP
// framework's own recovery, which knows nothing about the tickets still
// outstanding: every reversal that had not run yet is skipped, and the money
// one of them was holding stays reserved against a request that is over.
func TestOneAdmissionPanickingStillReversesTheOthers(t *testing.T) {
	var log []string
	svc := &Service{}
	money := &fakePrepaidBalance{state: quotaWithinLimit, balance: 500, reserve: 120, log: &log}
	RegisterAdmission(svc, money, attemptView)
	RegisterAdmission(svc, &explodingAdmission{log: &log}, attemptView)

	rc := &Exchange{requestID: "req-exploding-release"}
	var held []heldTicket
	svc.admit(context.Background(), rc, &held)

	svc.releaseAdmissions(context.Background(), rc, held, fact.Outcome{Delivered: false})

	// Taken last, so it is released first — and it is the one that blows up.
	if money.released != 1 {
		t.Fatalf("the balance was released %d times, want 1: a panic in the reversal above it "+
			"took the whole unwind with it", money.released)
	}
	if money.balance != 500 {
		t.Errorf("balance = %d, want 500: nobody was served, and the refund was lost to "+
			"somebody else's panic", money.balance)
	}
}

// explodingAdmission is a capability whose reversal fails the way real code
// fails: not by returning an error, but by dereferencing something that turned
// out to be nil.
type explodingAdmission struct{ log *[]string }

func (*explodingAdmission) Name() string { return "exploding_admission" }

func (f *explodingAdmission) Admit(_ context.Context, _ fact.Attempt, _ fact.Sink) (struct{}, bool) {
	*f.log = append(*f.log, "admit:exploding")
	return struct{}{}, true
}

func (*explodingAdmission) Release(_ context.Context, _ fact.Attempt, _ struct{}, _ fact.Outcome, _ fact.Sink) {
	// A nil pointer whose field is read — the shape a capability reaches for
	// state it assumed some earlier step had populated.
	var reservation *prepaidTicket
	_ = reservation.reservedMicros
}

// TestARefusedBalanceStopsTheSubRequestFromBeingCharged is the reason the
// admission chain stops at the first refusal rather than running them all and
// collecting the verdicts.
//
// A sub-request that runs after the balance has already refused spends real
// money on a request nobody will serve. There is no reversal for that: the
// second upstream was called and it charged.
func TestARefusedBalanceStopsTheSubRequestFromBeingCharged(t *testing.T) {
	var log []string
	svc := &Service{}
	RegisterAdmission(svc, &fakePrepaidBalance{state: quotaExhausted, log: &log}, attemptView)
	RegisterAdmission(svc, &fakeSubRequestCharge{state: visionCharged, gross: 40, net: 52, log: &log}, attemptView)

	rc := &Exchange{}
	var held []heldTicket
	verdict := svc.admit(context.Background(), rc, &held)

	if verdict.Loop == LoopNone {
		t.Fatal("an exhausted balance admitted the request")
	}
	for _, e := range log {
		if e == "admit:subrequest" {
			t.Fatal("the sub-request was charged after the balance had already refused")
		}
	}
}
