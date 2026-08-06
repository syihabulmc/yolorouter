package gateway

import (
	"bytes"
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
		modelOverrideStreamEncoder{inner: inner, model: "caller-model"}.EncodeDeltas(mine)

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
		modelOverrideResponseEncoder{inner: inner, model: "caller-model"}.EncodeResponse(mine)

		if mine.Model != "provider-model" {
			t.Errorf("the caller's own response now says %q; the rename was supposed to go downstream only", mine.Model)
		}
		if inner.got == nil || inner.got.Model != "caller-model" {
			t.Errorf("the response going out = %+v, want the caller's own name", inner.got)
		}
	})
}

// TestAProviderThatOmitsTheSpaceStillGetsItsModelRenamed pins the framing the
// rewriters must accept.
//
// SSE allows `data:` with or without a space, and isDataLine accepts both — so
// a frame that omits it counts as data and commits the response. If the
// rewriters disagreed and skipped it, the provider's own name for the model
// would reach a caller who asked for a different one and will quote it back on
// the next turn.
func TestAProviderThatOmitsTheSpaceStillGetsItsModelRenamed(t *testing.T) {
	cases := []struct {
		name    string
		egress  protocols.ProtocolID
		payload string
		// leaked is the provider's own name, which must not survive.
		leaked string
	}{
		{
			name: "claude message_start", egress: protocols.ProtocolClaude,
			payload: `{"type":"message_start","message":{"id":"m1","model":"provider-secret"}}`,
			leaked:  "provider-secret",
		},
		{
			name: "gemini modelVersion", egress: protocols.ProtocolGemini,
			payload: `{"candidates":[{"content":{"parts":[{"text":"hi"}]}}],"modelVersion":"provider-secret"}`,
			leaked:  "provider-secret",
		},
		{
			name: "responses envelope", egress: protocols.ProtocolResponses,
			payload: `{"type":"response.created","response":{"id":"r1","model":"provider-secret"}}`,
			leaked:  "provider-secret",
		},
	}

	for _, tc := range cases {
		for _, framing := range []struct{ name, prefix string }{
			{"with space", "data: "},
			{"without space", "data:"},
		} {
			t.Run(tc.name+"/"+framing.name, func(t *testing.T) {
				line := []byte(framing.prefix + tc.payload + "\n\n")
				var claudeOnce bool
				out := rewritePassthroughStreamModel(tc.egress, line, "caller-model", &claudeOnce)

				if bytes.Contains(out, []byte(tc.leaked)) {
					t.Errorf("the provider's own name reached the caller: %s", out)
				}
				if !bytes.Contains(out, []byte("caller-model")) {
					t.Errorf("the caller's name is not in the frame: %s", out)
				}
				// The framing itself is the provider's, not ours to normalise.
				if !bytes.HasPrefix(out, []byte(framing.prefix)) {
					t.Errorf("framing changed to %q, want it left as %q", out[:8], framing.prefix)
				}
			})
		}
	}
}
