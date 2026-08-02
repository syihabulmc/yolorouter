package responses

import (
	"testing"

	"github.com/yolorouter/yolorouter/internal/protocols"
)

// These tests lock the encoder side of the usage-coherence convergence. They
// drive a wire body through the REAL responses decoder and then call the REAL
// egress builder (responsesWireUsage), asserting the encoder publishes null
// (nil) for records the billing gate also refuses. Both bugs being guarded were
// wire/billing disagreements: see gateway/usage_coherence_regression_test.go
// for the billing-side counterpart and the full doc reference.

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
// The non-streaming tests above cover DecodeResponse. These cover the STREAMING
// path (DecodeChunk -> appendUsageDelta), which has its own verdict-at-exit
// check (stream_decoder.go: `if d.usage.IsIncoherent() { d.usage.Invalid = true }`).
// That check was untested before this round; these guard it. The verdict there
// judges the ACCUMULATED record, not a single src frame, so it catches shapes
// that only emerge once frames combine — as well as the single-frame shape.

// TestStreamMarksAbsurdCacheCountAsInvalid locks P1-1 on the streaming path: a
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
		t.Errorf("streaming P1-1: an absurd cache count must be marked Invalid at the streaming decoder exit, got %+v", u)
	}
}

// TestStreamMarksNegativeReasoningAsInvalid locks P2-4 on the streaming path.
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
		t.Errorf("streaming P2-4: a negative reasoning count must be marked Invalid at the streaming decoder exit, got %+v", u)
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

// --- Non-streaming path ---

// TestEncoderRefusesAbsurdCacheCount locks P1-1 on the wire side: under the
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
		t.Errorf("encoder must emit null for an absurd cache count (P1-1), got %v for usage %+v", got, resp.Usage)
	}
}

// TestEncoderRefusesNegativeReasoning locks P2-4 on the wire side: a negative
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
		t.Errorf("encoder must emit null for a negative reasoning count (P2-4), got %v for usage %+v", got, resp.Usage)
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
