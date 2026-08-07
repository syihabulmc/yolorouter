package gateway

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

// allResolved enumerates one resolved value per Kind, for the algebraic checks.
func allResolved() []resolved {
	out := make([]resolved, 0, fact.NumKinds)
	for k := range fact.NumKinds {
		out = append(out, resolved{
			Decision:       decisionTable[k],
			statusFrom:     fact.Kind(k),
			upstreamStatus: 502,
		})
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
			if combine(a, b) != combine(b, a) {
				t.Fatalf("combine not commutative for %s and %s", a.statusFrom, b.statusFrom)
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
			if combine(combine(a, b), c) != combine(a, combine(b, c)) {
				t.Fatalf("combine not associative for %s, %s, %s", a.statusFrom, b.statusFrom, c.statusFrom)
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
			if got := combine(a, b); !got.Defined {
				t.Fatalf("combine produced an undefined decision from %s and %s", a.statusFrom, b.statusFrom)
			}
		}
	}
}

// TestCombineNeverWeakensAnyEffect pins the fold's direction on every ordered
// field individually.
//
// The algebra tests above prove combine is commutative and associative — which
// a fold that always took the WEAKER side would also be. Direction is a
// separate property, and per-field on purpose: reversing one field's fold
// while the others stay correct is invisible to any test that only inspects
// the overall strongest-loop behaviour.
func TestCombineNeverWeakensAnyEffect(t *testing.T) {
	weak := resolved{Decision: Decision{Defined: true}}
	strong := resolved{Decision: Decision{
		Defined: true,
		Loop:    LoopTerminate,
		Circuit: CircuitEffect(2),
		Budget:  BudgetEffect(1),
		Sticky:  StickyEffect(1),
		Settle:  SettleEffect(1),
	}}

	for name, got := range map[string]resolved{
		"strong on the left":  combine(strong, weak),
		"strong on the right": combine(weak, strong),
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
		if got.Sticky != strong.Sticky {
			t.Errorf("%s: Sticky folded to %v, want the stronger %v", name, got.Sticky, strong.Sticky)
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
	got := resolveBatch([]fact.Fact{
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
	got := resolveBatch([]fact.Fact{
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
	refused := resolved{
		Decision:   decisionTable[fact.KindPayloadRefused],
		loopFrom:   fact.KindPayloadRefused,
		statusFrom: fact.KindPayloadRefused,
	}
	terminal := resolved{
		Decision:   decisionTable[fact.KindRequestBudgetExhausted],
		loopFrom:   fact.KindRequestBudgetExhausted,
		statusFrom: fact.KindRequestBudgetExhausted,
	}

	got := combine(refused, terminal)
	if got.Loop != LoopTerminate {
		t.Fatalf("loop = %v, want LoopTerminate", got.Loop)
	}
	if got.loopFrom != fact.KindRequestBudgetExhausted {
		t.Errorf("loopFrom = %s, want the verdict that actually won", got.loopFrom)
	}
	// The weaker verdict must not claim the effect just because it was folded
	// in first.
	if rev := combine(terminal, refused); rev.loopFrom != got.loopFrom {
		t.Errorf("attribution depends on fold order: %s vs %s", rev.loopFrom, got.loopFrom)
	}
}

// TestResolveBatchAttributesRefusal is the case the relay keys on: a refusal
// must be identifiable as a refusal, not merely as "something wants the next
// candidate".
func TestResolveBatchAttributesRefusal(t *testing.T) {
	got := resolveBatch([]fact.Fact{{Kind: fact.KindPayloadRefused, Status: 400}})
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
		got := resolveBatch([]fact.Fact{{Kind: k}})
		if got.Loop != LoopNextCandidate {
			t.Fatalf("%s: loop = %v, want LoopNextCandidate", k, got.Loop)
		}
		if got.loopFrom == fact.KindPayloadRefused {
			t.Errorf("%s is attributed as a refusal", k)
		}
	}
}
