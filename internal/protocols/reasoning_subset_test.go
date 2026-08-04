package protocols

import "testing"

// These tests lock the reasoning-subset rule at the IR boundary. Every protocol
// decoded here reports reasoning as a breakdown OF the completion count, so a
// reasoning count exceeding its own completion is impossible in the same sense
// as a cache count exceeding the prompt it is a subset of.
//
// The rule matters because the contradiction is not inert downstream: the Gemini
// egress splits reasoning back out of the completion, clamps the remainder at 0,
// and still publishes the reasoning line against a total derived from the
// smaller completion — a usageMetadata whose parts do not add up to its own
// stated total. Billing reads the same undersized completion.

// TestIRUsage_ReasoningExceedingCompletionIsIncoherent locks the rule itself.
func TestIRUsage_ReasoningExceedingCompletionIsIncoherent(t *testing.T) {
	u := IRUsage{PromptTokens: 5, CompletionTokens: 10, TotalTokens: 15, ReasoningTokens: 100}
	if !u.IsIncoherent() {
		t.Errorf("reasoning (100) exceeding completion (10) must be incoherent, got coherent for %+v", u)
	}
}

// TestIRUsage_ReasoningEqualToCompletionIsCoherent guards against
// over-rejection: a response whose entire output was thinking reports the two
// counts equal, and that is the normal shape for a reasoning model that hit its
// output limit mid-thought.
func TestIRUsage_ReasoningEqualToCompletionIsCoherent(t *testing.T) {
	u := IRUsage{PromptTokens: 5, CompletionTokens: 100, TotalTokens: 105, ReasoningTokens: 100}
	if u.IsIncoherent() {
		t.Errorf("reasoning equal to completion is the pure-thinking shape and must stay coherent, got incoherent for %+v", u)
	}
}

// TestIRUsage_ReasoningWithoutCompletionIsNotJudged locks the gate. A record
// with no completion count has not yet said anything to contradict: on a
// streaming path a zero completion is indistinguishable from "the completion
// has not arrived yet". Both shapes are covered — with and without a stated
// total — because the total may legitimately arrive in an EARLIER frame than
// the completion, and gating on it would make the verdict frame-order
// dependent.
func TestIRUsage_ReasoningWithoutCompletionIsNotJudged(t *testing.T) {
	cases := []struct {
		name  string
		usage IRUsage
	}{
		{"no total yet", IRUsage{ReasoningTokens: 50}},
		{"total already collected from an earlier frame", IRUsage{PromptTokens: 100, TotalTokens: 100, ReasoningTokens: 50}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if tc.usage.IsIncoherent() {
				t.Errorf("a record with no completion count must not be judged, got incoherent for %+v", tc.usage)
			}
		})
	}
}

// TestIRUsage_Merge_AppliesOnlyTheMidStreamRules locks Merge's half of the
// contract. Merge cannot tell a raw frame from a running total — the Claude and
// Responses stream decoders accumulate internally and hand their accumulated
// record to every DeltaUsage they emit — so it applies only the rules that hold
// on a stitched record, and relies on the decoder to have settled the
// growth-sensitive one.
func TestIRUsage_Merge_AppliesOnlyTheMidStreamRules(t *testing.T) {
	t.Run("the stable rules still fire", func(t *testing.T) {
		var acc IRUsage
		acc.Merge(IRUsage{PromptTokens: 100, CompletionTokens: 1, TotalTokens: 101,
			CacheWriteTokens: 1000000, CacheIncludedInPrompt: true})
		if !acc.Invalid {
			t.Errorf("cache exceeding the prompt must be caught by Merge, got %+v", acc)
		}
	})

	t.Run("a decoder-settled frame propagates its verdict", func(t *testing.T) {
		frame := IRUsage{PromptTokens: 5, CompletionTokens: 10, TotalTokens: 15, ReasoningTokens: 100}
		frame.Invalid = frame.IsIncoherent() // what every decoder does at its exit
		if !frame.Invalid {
			t.Fatalf("the decoder must have condemned this snapshot, got %+v", frame)
		}
		var acc IRUsage
		acc.Merge(frame)
		if !acc.Invalid {
			t.Errorf("Merge must carry a decoder's verdict through, got %+v", acc)
		}
	})

	t.Run("the accumulation itself is never judged", func(t *testing.T) {
		var acc IRUsage
		acc.Merge(IRUsage{PromptTokens: 5, CompletionTokens: 10, TotalTokens: 15})
		acc.Merge(IRUsage{ReasoningTokens: 100})
		if acc.Invalid {
			t.Errorf("no frame asserted anything impossible, so the record must not be condemned; got %+v", acc)
		}
	})
}

// TestIRUsage_Merge_CumulativeReportingStaysBillable is the scenario the
// accumulated verdict used to break: an upstream reporting cumulative counts
// whose reasoning line runs ahead of the completion line it belongs to. The
// completion catches up in a later frame and the final record is coherent, so
// the request must remain billable — a verdict latched on the middle frame
// could never be undone.
func TestIRUsage_Merge_CumulativeReportingStaysBillable(t *testing.T) {
	var acc IRUsage
	acc.Merge(IRUsage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110})
	acc.Merge(IRUsage{ReasoningTokens: 15})
	acc.Merge(IRUsage{CompletionTokens: 20})

	if acc.Invalid {
		t.Errorf("reasoning briefly running ahead of a still-growing completion is not a contradiction; got %+v", acc)
	}
	if acc.CompletionTokens != 20 || acc.ReasoningTokens != 15 {
		t.Errorf("final counts wrong: got completion=%d reasoning=%d, want 20/15 (%+v)",
			acc.CompletionTokens, acc.ReasoningTokens, acc)
	}
	if acc.IsIncoherent() {
		t.Errorf("the settled record is coherent (15 reasoning inside 20 completion), got incoherent for %+v", acc)
	}
}

// TestIRUsage_MidStreamVerdictKeepsTheStableRules is the counterpart to the
// split: dropping the reasoning rule mid-stream must not drop the rules that
// were always safe there. Negative counts and cache-exceeds-prompt do not
// depend on how far the response has progressed, so both must still fire.
func TestIRUsage_MidStreamVerdictKeepsTheStableRules(t *testing.T) {
	negative := IRUsage{PromptTokens: 10, CompletionTokens: -1}
	if !negative.IsIncoherentMidStream() {
		t.Errorf("a negative count is impossible at any point in a stream, got coherent for %+v", negative)
	}

	absurdCache := IRUsage{PromptTokens: 100, CompletionTokens: 1, TotalTokens: 101,
		CacheWriteTokens: 1000000, CacheIncludedInPrompt: true}
	if !absurdCache.IsIncoherentMidStream() {
		t.Errorf("cache exceeding the prompt is impossible at any point in a stream, got coherent for %+v", absurdCache)
	}

	growing := IRUsage{PromptTokens: 100, CompletionTokens: 10, TotalTokens: 110, ReasoningTokens: 15}
	if growing.IsIncoherentMidStream() {
		t.Errorf("reasoning ahead of a growing completion must NOT be judged mid-stream, got incoherent for %+v", growing)
	}
	if !growing.IsIncoherent() {
		t.Errorf("the same shape asserted by ONE snapshot is still a contradiction, got coherent for %+v", growing)
	}
}

// TestIRUsage_Merge_CoherentFramesStayBillableInAnyOrder is the negative case
// for the terminal check, and the reason its gate keys on the completion count
// alone.
//
// A stream may report its counts in any order. The reasoning-before-completion
// order is the dangerous one: between the reasoning frame and the completion
// frame the accumulated record reads as 40 reasoning tokens against a zero
// completion, next to a total collected two frames earlier. A gate that opened
// on that total would mark the record invalid then and there — and Invalid is
// one-way, so the completion arriving afterwards could not clear it. The very
// same three frames in the other order would sail through, which means the
// decision would rest entirely on how the upstream chose to split its frames.
func TestIRUsage_Merge_CoherentFramesStayBillableInAnyOrder(t *testing.T) {
	orders := map[string][]IRUsage{
		"completion before reasoning": {
			{PromptTokens: 100, TotalTokens: 100},
			{CompletionTokens: 60},
			{ReasoningTokens: 40},
		},
		"reasoning before completion": {
			{PromptTokens: 100, TotalTokens: 100},
			{ReasoningTokens: 40},
			{CompletionTokens: 60},
		},
	}
	for name, frames := range orders {
		t.Run(name, func(t *testing.T) {
			var acc IRUsage
			for i, f := range frames {
				acc.Merge(f)
				if acc.Invalid {
					t.Fatalf("record marked invalid after frame %d (%+v); accumulated %+v", i+1, f, acc)
				}
			}
			if acc.CompletionTokens != 60 || acc.ReasoningTokens != 40 {
				t.Errorf("accumulated counts lost: got completion=%d reasoning=%d, want 60/40 (%+v)",
					acc.CompletionTokens, acc.ReasoningTokens, acc)
			}
		})
	}
}

// TestIRUsage_Merge_DecoderVerdictSurvivesLaterFrames is the positive
// counterpart: conceding the cross-frame case must not have loosened the rule
// into uselessness. Once a decoder condemns a snapshot, no amount of later
// well-formed usage may rehabilitate the record — that one-way property is what
// stops a malformed frame's counts from being billed as if they were fine.
//
// The frames are settled the way a decoder settles them, since that is the only
// path a real record takes; Merge's own contract is covered above.
func TestIRUsage_Merge_DecoderVerdictSurvivesLaterFrames(t *testing.T) {
	settled := func(u IRUsage) IRUsage {
		u.Invalid = u.IsIncoherent()
		return u
	}
	orders := map[string][]IRUsage{
		"asserted by the only frame":  {settled(IRUsage{CompletionTokens: 10, TotalTokens: 10, ReasoningTokens: 100})},
		"asserted by a later frame":   {settled(IRUsage{PromptTokens: 5}), settled(IRUsage{CompletionTokens: 10, TotalTokens: 10, ReasoningTokens: 100})},
		"verdict survives new frames": {settled(IRUsage{CompletionTokens: 10, TotalTokens: 10, ReasoningTokens: 100}), settled(IRUsage{PromptTokens: 5})},
	}
	for name, frames := range orders {
		t.Run(name, func(t *testing.T) {
			var acc IRUsage
			for _, f := range frames {
				acc.Merge(f)
			}
			if !acc.Invalid {
				t.Errorf("a frame claiming 100 reasoning tokens inside a 10-token completion must condemn the record, got %+v", acc)
			}
		})
	}
}
