package decision

import (
	"testing"

	"github.com/yolorouter/yolorouter/internal/fact"
)

// TestDecisionTableIsComplete is the guard that makes the fixed-size array
// worth having. A Kind added without a row leaves Defined false, and an
// all-zero Decision would otherwise read as a perfectly valid "continue, no
// status, no settlement" — a reported judgement silently doing nothing, which
// looks exactly like everything working.
func TestDecisionTableIsComplete(t *testing.T) {
	for k := range fact.NumKinds {
		if !decisionTable[k].Defined {
			t.Errorf("kind %d (%s) has no decision row", k, fact.Kind(k))
		}
	}
}

// TestDecisionTableStatusFieldsOnlyWithFixed checks that Code and ErrType are
// only set where they mean anything. A code sitting on a row whose policy
// ignores it is a trap: it reads as the answer while something else supplies
// the real status.
func TestDecisionTableStatusFieldsOnlyWithFixed(t *testing.T) {
	for k := range fact.NumKinds {
		d := decisionTable[k]
		if d.Status == StatusFixed {
			if d.Code == 0 {
				t.Errorf("kind %s uses StatusFixed but sets no code", fact.Kind(k))
			}
			continue
		}
		if d.Code != 0 || d.ErrType != "" {
			t.Errorf("kind %s sets code/errType but its policy is not StatusFixed", fact.Kind(k))
		}
	}
}

// allResolved enumerates one Resolved value per Kind, for the algebraic checks.
func allResolved() []Resolved {
	out := make([]Resolved, 0, fact.NumKinds*3)
	// Three variants per Kind, with distinct words. One per Kind cannot see the
	// tie-breaks at all: two facts of the SAME Kind is exactly the case the
	// ordinal cannot separate, and a fixture that never builds that pair leaves
	// the rule which settles it — and therefore whether the fold depends on
	// registration order — untested in both directions.
	//
	// loopFrom is filled for the same reason: left at the zero Kind for every
	// element, the loop tie-break compares a value against itself and any rule
	// at all passes.
	for k := range fact.NumKinds {
		for _, w := range []struct{ reason, detail string }{
			{"", ""},
			{"reason_a", "detail_x"},
			{"reason_b", "detail_y"},
		} {
			out = append(out, Resolved{
				Decision:     decisionTable[k],
				statusFrom:   fact.Kind(k),
				loopFrom:     fact.Kind(k),
				reason:       w.reason,
				rejectDetail: w.detail,
			})
		}
	}
	return out
}

// TestCombineIsCommutative is the property that keeps behaviour independent of
// the order capabilities were registered in. Without it, two capabilities
// reporting in one batch would resolve differently depending on assembly order
// — a defect no single-fact test can see, and one that only appears under the
// traffic where both happen to fire together.
func TestCombineIsCommutative(t *testing.T) {
	rs := allResolved()
	for _, a := range rs {
		for _, b := range rs {
			if Combine(a, b) != Combine(b, a) {
				t.Fatalf("Combine not commutative for %s and %s", a.statusFrom, b.statusFrom)
			}
		}
	}
}

// TestCombineIsAssociative keeps the fold independent of batch size: reporting
// three facts at once must equal reporting them in any grouping.
func TestCombineIsAssociative(t *testing.T) {
	rs := allResolved()
	// The full triple product is O(n^3); step through the middle operand to
	// keep the run bounded while still crossing every pair with every third.
	for i, a := range rs {
		for j, b := range rs {
			c := rs[(i+j)%len(rs)]
			if Combine(Combine(a, b), c) != Combine(a, Combine(b, c)) {
				t.Fatalf("Combine not associative for %s, %s, %s", a.statusFrom, b.statusFrom, c.statusFrom)
			}
		}
	}
}

// TestCombineNeverProducesUndefined checks the fold cannot manufacture a
// decision the table never sanctioned.
func TestCombineNeverProducesUndefined(t *testing.T) {
	rs := allResolved()
	for _, a := range rs {
		for _, b := range rs {
			if got := Combine(a, b); !got.Defined {
				t.Fatalf("Combine produced an undefined decision from %s and %s", a.statusFrom, b.statusFrom)
			}
		}
	}
}

// TestCombineNeverWeakensAnyEffect pins the fold's direction on every ordered
// field individually.
//
// The algebra tests above prove Combine is commutative and associative — which
// a fold that always took the WEAKER side would also be. Direction is a
// separate property, and per-field on purpose: reversing one field's fold
// while the others stay correct is invisible to any test that only inspects
// the overall strongest-loop behaviour.
// Sticky is deliberately absent from the fields checked here. It is not folded
// by strength at all: it is part of the caller-facing verdict, adopted whole
// from whichever fact wins the status, because every part of that verdict —
// including whether to remember it — has to come from the same reporter.
// TestAVerdictIsRememberedOnlyByTheFactThatSuppliedIt holds that behaviourally
// and TestTheVerdictIsAdoptedWholeOrNotAtAll holds it structurally.
func TestCombineNeverWeakensAnyEffect(t *testing.T) {
	weak := Resolved{Decision: Decision{Defined: true}}
	strong := Resolved{Decision: Decision{
		Defined: true,
		Loop:    LoopTerminate,
		Circuit: CircuitEffect(2),
		Budget:  BudgetEffect(1),
		Settle:  SettleEffect(1),
	}}

	for name, got := range map[string]Resolved{
		"strong on the left":  Combine(strong, weak),
		"strong on the right": Combine(weak, strong),
	} {
		if got.Loop != strong.Loop {
			t.Errorf("%s: Loop folded to %v, want the stronger %v", name, got.Loop, strong.Loop)
		}
		if got.Circuit != strong.Circuit {
			t.Errorf("%s: Circuit folded to %v, want the stronger %v", name, got.Circuit, strong.Circuit)
		}
		if got.Budget != strong.Budget {
			t.Errorf("%s: Budget folded to %v, want the stronger %v", name, got.Budget, strong.Budget)
		}
		if got.Settle != strong.Settle {
			t.Errorf("%s: Settle folded to %v, want the stronger %v", name, got.Settle, strong.Settle)
		}
	}
}

// TestResolveBatchTakesStrongestLoop confirms a batch resolves to the strongest
// steer, so a capability reporting a weak verdict alongside a strong one cannot
// weaken it.
func TestResolveBatchTakesStrongestLoop(t *testing.T) {
	got := ResolveBatch([]fact.Fact{
		{Kind: fact.KindUpstreamServerError},
		{Kind: fact.KindRequestBudgetExhausted},
	})
	if got.Loop != LoopTerminate {
		t.Fatalf("want LoopTerminate, got %v", got.Loop)
	}
	if got.Settle != SettleReverseAll {
		t.Fatalf("want SettleReverseAll, got %v", got.Settle)
	}
}

// TestResolveBatchStatusFromPeer is the orthogonal-dimensions case: one fact
// decides the chain ends, another decides what the caller is told, and neither
// has to know about the other.
func TestResolveBatchStatusFromPeer(t *testing.T) {
	got := ResolveBatch([]fact.Fact{
		{Kind: fact.KindPricingUnavailableTerminal},
		{Kind: fact.KindPricingUnavailableSkip},
	})
	if got.Loop != LoopTerminate {
		t.Fatalf("want LoopTerminate, got %v", got.Loop)
	}
	if got.Status != StatusFixed || got.Code != 503 {
		t.Fatalf("want the peer's fixed 503, got policy %v code %d", got.Status, got.Code)
	}
}

// TestCombineAttributesTheWinningLoop checks that a folded decision can say
// WHICH verdict moved the chain.
//
// Without attribution every verdict resolving to the same effect is
// indistinguishable, and the relay has to guess — which is how a repair offer
// and a content refusal both ended up logged as content inspection.
func TestCombineAttributesTheWinningLoop(t *testing.T) {
	refused := Resolved{
		Decision:   decisionTable[fact.KindPayloadRefused],
		loopFrom:   fact.KindPayloadRefused,
		statusFrom: fact.KindPayloadRefused,
	}
	terminal := Resolved{
		Decision:   decisionTable[fact.KindRequestBudgetExhausted],
		loopFrom:   fact.KindRequestBudgetExhausted,
		statusFrom: fact.KindRequestBudgetExhausted,
	}

	got := Combine(refused, terminal)
	if got.Loop != LoopTerminate {
		t.Fatalf("loop = %v, want LoopTerminate", got.Loop)
	}
	if got.loopFrom != fact.KindRequestBudgetExhausted {
		t.Errorf("loopFrom = %s, want the verdict that actually won", got.loopFrom)
	}
	// The weaker verdict must not claim the effect just because it was folded
	// in first.
	if rev := Combine(terminal, refused); rev.loopFrom != got.loopFrom {
		t.Errorf("attribution depends on fold order: %s vs %s", rev.loopFrom, got.loopFrom)
	}
}

// TestResolveBatchAttributesRefusal is the case the relay keys on: a refusal
// must be identifiable as a refusal, not merely as "something wants the next
// candidate".
func TestResolveBatchAttributesRefusal(t *testing.T) {
	got := ResolveBatch([]fact.Fact{{Kind: fact.KindPayloadRefused, Status: 400}})
	if got.Loop != LoopNextCandidate {
		t.Fatalf("loop = %v, want LoopNextCandidate", got.Loop)
	}
	if got.loopFrom != fact.KindPayloadRefused {
		t.Errorf("loopFrom = %s, want payload_refused", got.loopFrom)
	}
}

// TestOtherNextCandidateVerdictsAreNotRefusals guards the mislabel directly: a
// different verdict that happens to resolve to the same loop effect must remain
// distinguishable from a refusal, so the relay cannot record one as the other.
func TestOtherNextCandidateVerdictsAreNotRefusals(t *testing.T) {
	for _, k := range []fact.Kind{
		fact.KindUpstreamServerError,
		fact.KindUpstreamTransportFailure,
		fact.KindCandidateUnsupported,
	} {
		got := ResolveBatch([]fact.Fact{{Kind: k}})
		if got.Loop != LoopNextCandidate {
			t.Fatalf("%s: loop = %v, want LoopNextCandidate", k, got.Loop)
		}
		if got.loopFrom == fact.KindPayloadRefused {
			t.Errorf("%s is attributed as a refusal", k)
		}
	}
}

// TestAVerdictWithNoStatusOpinionAsksForNothing pins the way a decision says
// "I have no view on what the caller should be told".
//
// Sticky and status are separate columns: a row can ask for its verdict to be
// remembered without stating what status that verdict deserves. The caller-
// facing render answers that with a zero status, and the terminal reads the
// zero as "record nothing" rather than as "tell them zero". Without this the
// pair reads like defensive filler, and the first person to tidy it would
// remove the only thing standing between a caller and an HTTP 0.
func TestAVerdictWithNoStatusOpinionAsksForNothing(t *testing.T) {
	var v Resolved // Status is StatusNone
	status, errType := v.CallerFacing(500, "upstream_error")
	if status != 0 || errType != "" {
		t.Errorf("CallerFacing = (%d, %q), want (0, \"\"): a decision that states no status "+
			"must not borrow the response's, or every verdict would look like it had an "+
			"opinion about what the caller sees", status, errType)
	}
}

// TestAVerdictIsRememberedOnlyByTheFactThatSuppliedIt is the fold property that
// a count of scopes cannot express.
//
// The terminal stores ONE verdict — status, error type, words, persisted reason
// — and asks whether to remember it. Every part of that answer has to come from
// the same fact. Folded independently, stickiness can be supplied by one fact
// while the status and the words come from another, and the pair is stored as
// though a single reporter had said both.
//
// The case below is the cheapest one that shows it: an exhausted quota carries
// no stickiness, a payload refusal carries it, and the quota wins the status
// not by making a stronger claim — a fixed status and a forwarded one are
// equally strong — but by having the lower Kind, which is how statusWins
// settles a tie. Fold them apart and the
// refusal's stickiness files the quota's 429 and the quota's wording — telling
// a caller their quota ran out when what happened was their payload was
// refused, and persisting that under the quota's reason code where no dashboard
// grouping by refusals will ever find it.
func TestAVerdictIsRememberedOnlyByTheFactThatSuppliedIt(t *testing.T) {
	for _, tc := range []struct {
		name  string
		facts []fact.Fact
	}{
		{"refusal first", []fact.Fact{
			{Kind: fact.KindPayloadRefused, Status: 400, Detail: "refused", Reason: "content_inspection_refused"},
			{Kind: fact.KindQuotaExhausted, Detail: "quota", Reason: "quota_exhausted"},
		}},
		{"quota first", []fact.Fact{
			{Kind: fact.KindQuotaExhausted, Detail: "quota", Reason: "quota_exhausted"},
			{Kind: fact.KindPayloadRefused, Status: 400, Detail: "refused", Reason: "content_inspection_refused"},
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			v := ResolveBatch(tc.facts)
			// Whoever won the status also decides whether it is remembered.
			want := For(v.statusFrom).Sticky
			if v.Sticky != want {
				t.Errorf("folded Sticky = %v, but the status came from %v whose row says %v: "+
					"the verdict would be remembered at a scope its own reporter never asked "+
					"for, and the caller told a story neither fact tells",
					v.Sticky, v.statusFrom, want)
			}
		})
	}
}

// TestTheFallbackReasonNamesTheFactThatSuppliedTheVerdict covers a provenance
// break that only shows up when two facts split the answer.
//
// A fact may carry no reason of its own, and then the Kind's own name is
// persisted instead. The question is WHICH Kind: a batch can have one fact win
// the status and the words while another wins the loop effect, and naming the
// loop's would file the row under something that contributed neither the status
// the caller saw nor the sentence they read — an audit trail pointing at a fact
// with nothing to do with the answer.
//
// A single-fact batch cannot see this: there, the two winners are the same
// fact by construction.
func TestTheFallbackReasonNamesTheFactThatSuppliedTheVerdict(t *testing.T) {
	// PricingUnavailableSkip states a status and no reason; RequestBudgetExhausted
	// terminates, so it wins the loop. Neither carries words.
	v := ResolveBatch([]fact.Fact{
		{Kind: fact.KindPricingUnavailableSkip},
		{Kind: fact.KindRequestBudgetExhausted},
	})
	if v.statusFrom == v.loopFrom {
		t.Fatalf("fixture no longer splits the winners (both %v); pick two Kinds where one wins "+
			"the status and another wins the loop, or this test proves nothing", v.statusFrom)
	}
	if got, want := v.FailReason(), v.statusFrom.String(); got != want {
		t.Errorf("FailReason = %q, want %q: the persisted reason has to name the fact that "+
			"supplied the verdict, not the one that supplied the loop effect", got, want)
	}
}
