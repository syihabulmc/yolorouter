package responses

import (
	"testing"

	"github.com/yolorouter/yolorouter/internal/protocols"
)

// These tests lock the encoder side of the usage-coherence convergence. They
// drive a wire body through the REAL responses decoder and then call the REAL
// egress builder (responsesWireUsage), asserting the encoder publishes null
// (nil) for records the billing gate also refuses. Both bugs being guarded were
// wire/billing disagreements: both paths must return the same accept/reject
// result for every usage record.

// deltaUsageFrom returns the Usage carried in the first DeltaUsage of deltas,
// or false if none was emitted. Used by the streaming-path tests below.
func deltaUsageFrom(t *testing.T, deltas []protocols.IRStreamDelta) (protocols.IRUsage, bool) {
	t.Helper()
	for _, d := range deltas {
		if du, ok := d.(protocols.DeltaUsage); ok {
			return du.Usage, true
		}
	}
	return protocols.IRUsage{}, false
}

// --- Streaming path ---
//
// The non-streaming tests below cover DecodeResponse. These cover the STREAMING
// path (DecodeChunk -> appendUsageDelta), which has its own verdict-at-exit
// check (stream_decoder.go: `if d.usage.IsIncoherent() { d.usage.Invalid = true }`).
// That check was untested before this round; these guard it. The verdict there
// judges the ACCUMULATED record, not a single src frame, so it catches shapes
// that only emerge once frames combine — as well as the single-frame shape.

// TestStreamMarksAbsurdCacheCountAsInvalid locks the absurd-cache-count refusal on the streaming path: a
// single response.completed frame carrying an oversized cache_write must arrive
// in the DeltaUsage delta already marked Invalid.
func TestStreamMarksAbsurdCacheCountAsInvalid(t *testing.T) {
	dec := NewStreamDecoder()
	chunk := `{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":100,"output_tokens":1,"total_tokens":101,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":1000000}}}}`
	deltas, err := dec.DecodeChunk(chunk)
	if err != nil {
		t.Fatalf("DecodeChunk: %v", err)
	}
	u, ok := deltaUsageFrom(t, deltas)
	if !ok {
		t.Fatal("expected a DeltaUsage delta in the streaming output")
	}
	if !u.Invalid {
		t.Errorf("an absurd cache count must be marked Invalid at the streaming decoder exit, got %+v", u)
	}
}

// TestStreamMarksNegativeReasoningAsInvalid locks the negative-reasoning refusal on the streaming path.
func TestStreamMarksNegativeReasoningAsInvalid(t *testing.T) {
	dec := NewStreamDecoder()
	chunk := `{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":100,"output_tokens":50,"total_tokens":150,"output_tokens_details":{"reasoning_tokens":-10}}}}`
	deltas, err := dec.DecodeChunk(chunk)
	if err != nil {
		t.Fatalf("DecodeChunk: %v", err)
	}
	u, ok := deltaUsageFrom(t, deltas)
	if !ok {
		t.Fatal("expected a DeltaUsage delta in the streaming output")
	}
	if !u.Invalid {
		t.Errorf("a negative reasoning count must be marked Invalid at the streaming decoder exit, got %+v", u)
	}
}

// TestStreamDoesNotMarkPartialFramesIncoherent guards the gate: a frame carrying
// ONLY a cache count (no prompt in that frame) must NOT be marked incoherent,
// because partial frames routinely arrive before their prompt counterpart. The
// accumulated check at appendUsageDelta runs on the merged record, which here
// has no prompt to overflow, so it stays coherent.
func TestStreamDoesNotMarkPartialFramesIncoherent(t *testing.T) {
	dec := NewStreamDecoder()
	// A cache-only completed frame: prompt 0 in this frame, cache_read 500.
	chunk := `{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0,"input_tokens_details":{"cached_tokens":500,"cache_write_tokens":0}}}}`
	deltas, err := dec.DecodeChunk(chunk)
	if err != nil {
		t.Fatalf("DecodeChunk: %v", err)
	}
	u, ok := deltaUsageFrom(t, deltas)
	if !ok {
		t.Fatal("expected a DeltaUsage delta in the streaming output")
	}
	if u.Invalid {
		t.Errorf("a cache-only partial frame must not be marked incoherent (no prompt to overflow), got %+v", u)
	}
}

// TestStreamCrossFrameAccumulatedIncoherence exercises the ACCUMULATED verdict at
// appendUsageDelta in isolation — the check Merge alone cannot make. Each frame
// is coherent on its own (frame 1: prompt only; frame 2: cache only, gated out
// because its prompt is 0), but once merged the record has prompt=100 alongside
// cache_read=500, which is impossible under the inclusive convention. Only the
// terminal `if d.usage.IsIncoherent()` check catches this; Merge judged each src
// frame separately and could not see the cross-frame shape. The single-frame
// tests above cannot reach this path because Merge sets Invalid itself there.
func TestStreamCrossFrameAccumulatedIncoherence(t *testing.T) {
	dec := NewStreamDecoder()
	// Frame 1: response.completed with prompt=100, no cache.
	if _, err := dec.DecodeChunk(`{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":100,"output_tokens":0,"total_tokens":100,"input_tokens_details":{"cached_tokens":0,"cache_write_tokens":0}}}}`); err != nil {
		t.Fatalf("DecodeChunk frame1: %v", err)
	}
	// Frame 2: response.done carrying cache_read=500 (oversized vs the prompt
	// collected in frame 1). This frame alone is gated out (prompt 0), so Merge
	// leaves Invalid alone — the accumulated check must catch it.
	deltas, err := dec.DecodeChunk(`{"type":"response.done","response":{"usage":{"input_tokens":0,"output_tokens":0,"total_tokens":0,"input_tokens_details":{"cached_tokens":500,"cache_write_tokens":0}}}}`)
	if err != nil {
		t.Fatalf("DecodeChunk frame2: %v", err)
	}
	u, ok := deltaUsageFrom(t, deltas)
	if !ok {
		t.Fatal("expected a DeltaUsage delta after the second (response.done) frame")
	}
	if !u.Invalid {
		t.Errorf("cross-frame accumulated incoherence (prompt=100 + cache_read=500) must be caught by the terminal accumulated check, got %+v", u)
	}
}

// --- Non-streaming path ---

// TestEncoderRefusesAbsurdCacheCount locks the absurd-cache-count refusal on the wire side: under the
// inclusive convention a cache count cannot exceed the prompt it is supposedly a
// subset of. The encoder used to emit this (its gate checked only negatives);
// after convergence the decoder sets Invalid and the encoder emits null.
func TestEncoderRefusesAbsurdCacheCount(t *testing.T) {
	body := `{
		"status": "completed",
		"usage": {
			"input_tokens": 100,
			"output_tokens": 1,
			"total_tokens": 101,
			"input_tokens_details": {"cached_tokens": 0, "cache_write_tokens": 1000000}
		}
	}`
	resp, err := ResponseDecoder{}.DecodeResponse([]byte(body))
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if got := responsesWireUsage(resp.Usage); got != nil {
		t.Errorf("encoder must emit null for an absurd cache count, got %v for usage %+v", got, resp.Usage)
	}
}

// TestEncoderRefusesNegativeReasoning locks the negative-reasoning refusal on the wire side: a negative
// reasoning count must reach the encoder's verdict (carried on Invalid) and
// produce null, matching what the billing gate now also decides.
func TestEncoderRefusesNegativeReasoning(t *testing.T) {
	body := `{
		"status": "completed",
		"usage": {
			"input_tokens": 100,
			"output_tokens": 50,
			"total_tokens": 150,
			"output_tokens_details": {"reasoning_tokens": -10}
		}
	}`
	resp, err := ResponseDecoder{}.DecodeResponse([]byte(body))
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if got := responsesWireUsage(resp.Usage); got != nil {
		t.Errorf("encoder must emit null for a negative reasoning count, got %v for usage %+v", got, resp.Usage)
	}
}

// TestEncoderEmitsHealthyUsage guards against over-rejection: a normal record
// must still produce a non-nil wire usage object.
func TestEncoderEmitsHealthyUsage(t *testing.T) {
	body := `{
		"status": "completed",
		"usage": {"input_tokens": 1000, "output_tokens": 10, "total_tokens": 1010,
		          "input_tokens_details": {"cached_tokens": 0, "cache_write_tokens": 0}}
	}`
	resp, err := ResponseDecoder{}.DecodeResponse([]byte(body))
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if got := responsesWireUsage(resp.Usage); got == nil {
		t.Errorf("encoder must emit usage for a healthy record, got nil for %+v", resp.Usage)
	}
}

// TestEncoderRefusesReasoningExceedingOutput locks the reasoning-subset rule on
// the Responses path: output_tokens_details.reasoning_tokens is a breakdown OF
// output_tokens, so a reasoning count larger than the output it sits inside is
// impossible and must reach the wire as null rather than as a self-contradicting
// breakdown.
func TestEncoderRefusesReasoningExceedingOutput(t *testing.T) {
	body := `{
		"status": "completed",
		"usage": {
			"input_tokens": 5,
			"output_tokens": 10,
			"total_tokens": 15,
			"output_tokens_details": {"reasoning_tokens": 100}
		}
	}`
	resp, err := ResponseDecoder{}.DecodeResponse([]byte(body))
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if !resp.Usage.Invalid {
		t.Errorf("reasoning exceeding output must be marked Invalid at the decoder exit, got %+v", resp.Usage)
	}
	if got := responsesWireUsage(resp.Usage); got != nil {
		t.Errorf("encoder must emit null when reasoning exceeds output, got %v for usage %+v", got, resp.Usage)
	}
}

// TestStreamCumulativeReasoningSurvivesDownstreamMerge covers the full path a
// Responses stream takes, not just the decoder's own state. The decoder hands
// its RUNNING TOTAL to every DeltaUsage it emits, so a downstream accumulator
// re-merges an already-stitched record; if that re-merge applied the
// growth-sensitive rule, the intermediate 10/15 snapshot would condemn a stream
// whose final 20/15 counts are perfectly coherent, and the usage would be
// dropped and left unbilled.
func TestStreamCumulativeReasoningSurvivesDownstreamMerge(t *testing.T) {
	dec := NewStreamDecoder()
	// Frame 2 carries the reasoning line WITHOUT restating the output it belongs
	// to — the shape that makes the accumulated record read 15 reasoning tokens
	// against a 10-token output. A frame that restated output=10 alongside
	// reasoning=15 would be self-contradictory on its own and is condemned by
	// the single-snapshot rule (see the test below); this one asserts nothing
	// impossible.
	frames := []string{
		`{"input_tokens":100,"output_tokens":10,"total_tokens":110}`,
		`{"input_tokens":100,"output_tokens_details":{"reasoning_tokens":15}}`,
		`{"input_tokens":100,"output_tokens":20,"total_tokens":120,"output_tokens_details":{"reasoning_tokens":15}}`,
	}

	// Mirrors what every egress encoder does with the deltas it receives.
	var downstream protocols.IRUsage
	for i, u := range frames {
		deltas, err := dec.DecodeChunk(`{"type":"response.completed","response":{"status":"completed","usage":` + u + `}}`)
		if err != nil {
			t.Fatalf("DecodeChunk frame%d: %v", i+1, err)
		}
		if du, ok := deltaUsageFrom(t, deltas); ok {
			downstream.Merge(du)
		}
	}

	if downstream.Invalid {
		t.Errorf("reasoning briefly ahead of a still-growing output must not condemn the stream, got %+v", downstream)
	}
	if downstream.CompletionTokens != 20 || downstream.ReasoningTokens != 15 {
		t.Errorf("final counts wrong: got output=%d reasoning=%d, want 20/15 (%+v)",
			downstream.CompletionTokens, downstream.ReasoningTokens, downstream)
	}
	if got := responsesWireUsage(downstream); got == nil {
		t.Error("encoder must publish usage for a stream whose final counts are coherent, got nil")
	}
}

// TestStreamMarksReasoningExceedingOutputAsInvalid is the streaming counterpart:
// the same contradiction arriving on a completed frame must be settled at the
// streaming decoder exit too, not only on the non-streaming path.
func TestStreamMarksReasoningExceedingOutputAsInvalid(t *testing.T) {
	dec := NewStreamDecoder()
	chunk := `{"type":"response.completed","response":{"status":"completed","usage":{"input_tokens":5,"output_tokens":10,"total_tokens":15,"output_tokens_details":{"reasoning_tokens":100}}}}`
	deltas, err := dec.DecodeChunk(chunk)
	if err != nil {
		t.Fatalf("DecodeChunk: %v", err)
	}
	u, ok := deltaUsageFrom(t, deltas)
	if !ok {
		t.Fatal("expected a DeltaUsage delta in the streaming output")
	}
	if !u.Invalid {
		t.Errorf("reasoning exceeding output must be marked Invalid at the streaming decoder exit, got %+v", u)
	}
}
