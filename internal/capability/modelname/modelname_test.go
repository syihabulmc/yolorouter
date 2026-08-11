package modelname

import (
	"encoding/json"
	"testing"

	"github.com/yolorouter/yolorouter/internal/protocols"
)

// recordingStreamEncoder captures what it was handed, so a test can compare it
// against what the caller still holds.
type recordingStreamEncoder struct{ got []protocols.IRStreamDelta }

func (r *recordingStreamEncoder) EncodeDeltas(d []protocols.IRStreamDelta) []protocols.SSEEvent {
	r.got = d
	return nil
}
func (r *recordingStreamEncoder) EncodeDone() []protocols.SSEEvent { return nil }
func (r *recordingStreamEncoder) Usage() protocols.IRUsage         { return protocols.IRUsage{} }

// recordingResponseEncoder is recordingStreamEncoder's non-stream counterpart.
type recordingResponseEncoder struct{ got *protocols.IRResponse }

func (r *recordingResponseEncoder) EncodeResponse(resp *protocols.IRResponse) json.RawMessage {
	r.got = resp
	return nil
}

// TestRenamingTheModelDoesNotRenameTheCallersCopy pins that these two wrappers
// answer a question rather than change one.
//
// They exist to put the caller's name for the model into what goes out on the
// wire. Everything still holding the decoded response wants the OTHER name —
// the provider's — because that is what says which upstream actually served the
// request. Writing the substitution back into the shared value silently gives
// both readers the same answer, and the one who needed the provider's name has
// no way to tell they were handed the wrong one.
func TestRenamingTheModelDoesNotRenameTheCallersCopy(t *testing.T) {
	t.Run("stream deltas", func(t *testing.T) {
		mine := []protocols.IRStreamDelta{
			protocols.DeltaMessageStart{Model: "provider-model"},
		}
		inner := &recordingStreamEncoder{}
		streamEncoder{inner: inner, model: "caller-model"}.EncodeDeltas(mine)

		held, ok := mine[0].(protocols.DeltaMessageStart)
		if !ok {
			t.Fatalf("fixture no longer holds a message start: %T", mine[0])
		}
		if held.Model != "provider-model" {
			t.Errorf("the caller's own delta now says %q; the rename was supposed to go downstream only", held.Model)
		}
		sent, ok := inner.got[0].(protocols.DeltaMessageStart)
		if !ok {
			t.Fatalf("the encoder was handed a %T", inner.got[0])
		}
		if sent.Model != "caller-model" {
			t.Errorf("the frame going out says %q, want the caller's own name", sent.Model)
		}
	})

	t.Run("non-stream response", func(t *testing.T) {
		mine := &protocols.IRResponse{Model: "provider-model"}
		inner := &recordingResponseEncoder{}
		responseEncoder{inner: inner, model: "caller-model"}.EncodeResponse(mine)

		if mine.Model != "provider-model" {
			t.Errorf("the caller's own response now says %q; the rename was supposed to go downstream only", mine.Model)
		}
		if inner.got == nil || inner.got.Model != "caller-model" {
			t.Errorf("the response going out = %+v, want the caller's own name", inner.got)
		}
	})
}

// An exchange that never resolved a name has nothing to put back, and writing
// the empty string over the provider's name would leave the caller with no
// model name at all — worse than the wrong one, because a client reading the
// field gets nothing rather than something it can at least report.
func TestNothingIsRenamedWhenThereIsNoNameToPutBack(t *testing.T) {
	if New().Applies(stubView{}) {
		t.Fatal("a blank model name counts as something to put back; the rename would blank the field")
	}
	if !New().Applies(stubView{model: "gpt-4o"}) {
		t.Fatal("a real model name does not count as something to put back")
	}

	// Why the guard matters: the wrapper itself does not second-guess the name
	// it is handed, so an empty one goes straight into the response.
	inner := &recordingResponseEncoder{}
	responseEncoder{inner: inner, model: ""}.EncodeResponse(&protocols.IRResponse{Model: "provider-model"})
	if inner.got == nil || inner.got.Model != "" {
		t.Fatalf("encoder wrote %+v; the guard in Applies is the only thing keeping this off the wire", inner.got)
	}
}

type stubView struct{ model string }

func (v stubView) OriginalModel() string { return v.model }
