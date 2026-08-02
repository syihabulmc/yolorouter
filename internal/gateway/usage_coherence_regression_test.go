package gateway

import (
	"testing"

	"github.com/yolorouter/yolorouter/internal/protocols"
	"github.com/yolorouter/yolorouter/internal/protocols/responses"
)

// These tests guard the two real bugs fixed by the verdict-convergence work
// (P1-1 and P2-4 in docs/protocols/usage-coherence-open-issues.md). Both were
// symptoms of one root cause: the wire encoder and the billing gate judged
// "is this usage impossible?" with DIFFERENT predicates, so a record could be
// emitted on the wire while billing refused it (P1-1), or refused on the wire
// while billing accepted it (P2-4).
//
// The fix routes both through a single IR-level verdict (IRUsage.IsIncoherent),
// set once at the decoder exit and carried on Invalid. These tests exercise the
// REAL path — a wire JSON body through the actual responses decoder, then the
// actual bridge into gateway.Usage and the actual coherence gate — and assert
// the two sides AGREE. They do NOT construct a Usage struct directly to assert
// gate behaviour: that is the false-positive pattern documented as lesson 1 in
// the open-issues doc, and it is exactly what let these bugs ship green.

// encodeRefuses reports what the wire encoder would publish: null when the
// verdict is impossible (every egress builder now guards on Invalid alone), a
// non-nil map otherwise. It is the encoder side of the agreement.
func encodeRefuses(u protocols.IRUsage) bool {
	return u.Invalid
}

// billRefuses reports what the billing gate decides, driven through the real
// IR→gateway.Usage bridge and the real coherence predicate.
func billRefuses(u protocols.IRUsage) bool {
	gu := irUsageToUsage(&u)
	return gu == nil || !usageIsCoherent(gu)
}

// decodeResponses drives a wire body through the actual responses decoder,
// returning the IR usage it produces (with Invalid set at the decoder exit).
func decodeResponses(t *testing.T, body string) protocols.IRUsage {
	t.Helper()
	resp, err := responses.ResponseDecoder{}.DecodeResponse([]byte(body))
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	return resp.Usage
}

// TestP1_1_WireAndBillingAgreeOnAbsurdCacheCount: an upstream reporting
// prompt=100 alongside cache_write=1000000 under the inclusive convention used
// to be EMITTED by the encoder (its gate checked only negatives) while billing
// refused it (cache exceeds the prompt it is supposedly a subset of). The client
// got an authoritative-looking million-token cache write and a downstream gateway
// billing from it would have over-charged. After convergence both sides refuse.
func TestP1_1_WireAndBillingAgreeOnAbsurdCacheCount(t *testing.T) {
	body := `{
		"status": "completed",
		"usage": {
			"input_tokens": 100,
			"output_tokens": 1,
			"total_tokens": 101,
			"input_tokens_details": {"cached_tokens": 0, "cache_write_tokens": 1000000}
		}
	}`
	u := decodeResponses(t, body)

	if !u.CacheIncludedInPrompt {
		t.Fatalf("precondition: responses decoder must mark usage inclusive, got %+v", u)
	}
	// The two sides must agree — both refuse — instead of one emitting and the
	// other rejecting. Agreement is the regression; the direction (refuse) is the
	// fix for this shape.
	if encodeRefuses(u) != billRefuses(u) {
		t.Errorf("P1-1 disagreement: encoder refuses=%v, billing refuses=%v — wire and billing must agree on %+v",
			encodeRefuses(u), billRefuses(u), u)
	}
	if !encodeRefuses(u) {
		t.Errorf("P1-1: an absurd cache count (cache_write=1000000 on a 100-token prompt) must be refused on the wire, got %+v", u)
	}
}

// TestP2_4_WireAndBillingAgreeOnNegativeReasoning: a record with a positive
// output count but a NEGATIVE reasoning count used to make the encoder emit null
// (HasNegativeCount sees the negative reasoning count) while billing accepted it
// (relay.Usage/gateway.Usage had no ReasoningTokens field, so the billing gate
// could not see the negative and re-derive the verdict). After convergence the
// reasoning count is carried across the bridge and both sides refuse.
func TestP2_4_WireAndBillingAgreeOnNegativeReasoning(t *testing.T) {
	body := `{
		"status": "completed",
		"usage": {
			"input_tokens": 100,
			"output_tokens": 50,
			"total_tokens": 150,
			"output_tokens_details": {"reasoning_tokens": -10}
		}
	}`
	u := decodeResponses(t, body)

	if u.ReasoningTokens != -10 {
		t.Fatalf("precondition: decoder must surface the negative reasoning count, got ReasoningTokens=%d", u.ReasoningTokens)
	}
	if encodeRefuses(u) != billRefuses(u) {
		t.Errorf("P2-4 disagreement: encoder refuses=%v, billing refuses=%v — wire and billing must agree on %+v",
			encodeRefuses(u), billRefuses(u), u)
	}
	if !billRefuses(u) {
		t.Errorf("P2-4: a negative reasoning count must reach billing and be refused; the bridge must carry ReasoningTokens. got %+v", u)
	}
}

// TestCoherentRecordsStayBillable: the convergence must not over-reject. A plain
// healthy record and a full-cache-hit record both stay billable on both sides.
// This is the "the guard cannot silently zero the bill" sanity check.
func TestCoherentRecordsStayBillable(t *testing.T) {
	healthy := `{
		"status": "completed",
		"usage": {"input_tokens": 1000, "output_tokens": 10, "total_tokens": 1010,
		          "input_tokens_details": {"cached_tokens": 0, "cache_write_tokens": 0}}
	}`
	u := decodeResponses(t, healthy)
	if encodeRefuses(u) || billRefuses(u) {
		t.Errorf("a healthy record must stay billable on both sides, got encoder refuses=%v billing refuses=%v for %+v",
			encodeRefuses(u), billRefuses(u), u)
	}

	// A full cache hit where the whole prompt was served from cache, plus a real
	// completion. cache_read equals the prompt here (500 == 500), which is the
	// legitimate "fully cached" shape — both sides must accept it. (A record with
	// zero prompt AND zero completion AND zero total maps to nil at the bridge by
	// design — "nothing billable arrived" — so this case keeps a completion.)
	fullCache := `{
		"status": "completed",
		"usage": {"input_tokens": 500, "output_tokens": 20, "total_tokens": 520,
		          "input_tokens_details": {"cached_tokens": 500, "cache_write_tokens": 0}}
	}`
	c := decodeResponses(t, fullCache)
	if encodeRefuses(c) || billRefuses(c) {
		t.Errorf("a full-cache-hit record must stay billable on both sides, got encoder refuses=%v billing refuses=%v for %+v",
			encodeRefuses(c), billRefuses(c), c)
	}
}
