package gateway

import (
	"bytes"
	"errors"
	"github.com/yolorouter/yolorouter/internal/fact"
	"net/http"
	"testing"

	"github.com/yolorouter/yolorouter/internal/protocols"
)

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

// refusingCommitClient is a ClientResponse whose commit fails, which is what a
// caller that went away between the upstream answering and us replying looks
// like from here.
type refusingCommitClient struct{ ClientResponse }

func (refusingCommitClient) Commit(int) error { return errors.New("connection reset") }

// TestACallerWhoNeverGotAStatusIsNotFiledUnderTheProvidersOne pins what a
// failed commit is recorded as.
//
// Commit is the step that puts a status line on the wire. If it fails the
// caller received nothing at all — not a truncated answer, not a partial one.
// Recording the upstream's 2xx there files a request nobody was served under
// the code for a served one, and an operator counting failures never sees it.
// The streaming side of this same failure already records a 500.
func TestACallerWhoNeverGotAStatusIsNotFiledUnderTheProvidersOne(t *testing.T) {
	const openAIOK = `{"id":"c1","object":"chat.completion","model":"m","choices":[{"index":0,"message":{"role":"assistant","content":"hi"}}],"usage":{"prompt_tokens":11,"completion_tokens":3,"total_tokens":14}}`

	adm := admitFor(t, protocols.ProtocolOpenAI, "/v1/chat/completions",
		`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`,
		Candidate{ProviderModelName: "m", EgressProtocol: protocols.ProtocolOpenAI, Passthrough: true})

	tools, release, _ := fakeDelivery{path: "/v1/chat/completions", requestID: "commit-fails"}.tools(t)
	defer release()
	tools.Client = refusingCommitClient{ClientResponse: tools.Client}

	d := adm.payload.Deliver(tools, upstreamResponse(t, 200, openAIOK))

	if d.BillingStatus == 200 {
		t.Error("recorded as the provider's 200; the caller was never sent a status at all")
	}
	if d.BillingStatus != http.StatusInternalServerError {
		t.Errorf("billing status = %d, want 500 — matching what the streaming side records "+
			"for the same failure", d.BillingStatus)
	}
	if d.Fault != fact.FaultGateway {
		t.Errorf("fault = %v, want gateway: the provider answered correctly", d.Fault)
	}
	// The provider produced these tokens whatever became of them.
	if d.Usage == nil || d.Usage.Prompt != 11 {
		t.Errorf("usage = %+v, want the provider's counts kept", d.Usage)
	}
}
