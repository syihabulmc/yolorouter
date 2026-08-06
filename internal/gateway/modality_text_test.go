package gateway

import (
	"context"
	"strings"
	"testing"

	"github.com/yolorouter/yolorouter/internal/fact"
	"github.com/yolorouter/yolorouter/internal/protocols"
)

// TestPrepareUpstreamBuildsTheRequestForEachEgress pins the request a provider
// actually receives — body, path and content type — for every combination of
// what the caller spoke and what the provider speaks.
//
// The bytes are spelled out rather than derived, because the request is the one
// thing here nobody downstream can correct. A provider sent a body missing a
// field its protocol requires answers with a 4xx that reads as our caller's
// fault, and a request sent to the wrong path fails over to the next candidate
// as if the provider were down.
func TestPrepareUpstreamBuildsTheRequestForEachEgress(t *testing.T) {
	const openAIBody = `{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`
	const streamBody = `{"model":"gpt-4o","stream":true,"stream_options":{"include_usage":true},"messages":[{"role":"user","content":"hi"}]}`
	// A streaming request that did NOT ask for usage is the one that proves the
	// injection ran: with stream_options already present there is nothing to
	// add, and a test built only on that case would pass with the step removed.
	const streamNoOptsBody = `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	const claudeBody = `{"model":"claude-3-5-sonnet-20241022","max_tokens":64,"messages":[{"role":"user","content":"hi"}]}`

	cases := []struct {
		name     string
		ingress  protocols.ProtocolID
		body     string
		egress   protocols.ProtocolID
		through  bool
		provider string
		wantBody string
		wantURL  string
	}{
		{
			name: "openai passthrough", ingress: protocols.ProtocolOpenAI, body: openAIBody,
			egress: protocols.ProtocolOpenAI, through: true, provider: "gpt-4o-mini",
			// Forwarded as sent, with the provider's own name for the model
			// substituted in.
			wantBody: `{"messages":[{"role":"user","content":"hi"}],"model":"gpt-4o-mini"}`,
			wantURL:  "https://upstream.invalid/v1/chat/completions",
		},
		{
			name: "openai streaming passthrough", ingress: protocols.ProtocolOpenAI, body: streamBody,
			egress: protocols.ProtocolOpenAI, through: true, provider: "gpt-4o-mini",
			wantBody: `{"messages":[{"role":"user","content":"hi"}],"model":"gpt-4o-mini","stream":true,"stream_options":{"include_usage":true}}`,
			wantURL:  "https://upstream.invalid/v1/chat/completions",
		},
		{
			name: "openai streaming without stream_options", ingress: protocols.ProtocolOpenAI, body: streamNoOptsBody,
			egress: protocols.ProtocolOpenAI, through: true, provider: "gpt-4o-mini",
			// stream_options is added even though the caller did not ask for it:
			// without it this provider reports no usage at all and the request
			// cannot be billed. It is stripped back out of the caller's own
			// frames on the way home.
			wantBody: `{"messages":[{"role":"user","content":"hi"}],"model":"gpt-4o-mini","stream":true,"stream_options":{"include_usage":true}}`,
			wantURL:  "https://upstream.invalid/v1/chat/completions",
		},
		{
			name: "openai to claude, full re-encode", ingress: protocols.ProtocolOpenAI, body: openAIBody,
			egress: protocols.ProtocolClaude, provider: "claude-3-5-sonnet-20241022",
			// max_tokens is required by this protocol and absent from the
			// caller's request, so a default is supplied rather than sending a
			// body the provider will refuse.
			//
			// No system message in the fixture, deliberately. A leading
			// system-role message decodes into an ordinary message entry rather
			// than into the request's own System field, and this encoder emits
			// only the latter — so a system prompt does not survive this
			// crossing today. Pinning one here would record that loss as
			// intended behaviour.
			wantBody: `{"max_tokens":4096,"messages":[{"content":"hi","role":"user"}],"model":"claude-3-5-sonnet-20241022"}`,
			wantURL:  "https://upstream.invalid/v1/messages",
		},
		{
			name: "openai to gemini, full re-encode", ingress: protocols.ProtocolOpenAI, body: openAIBody,
			egress: protocols.ProtocolGemini, provider: "gemini-2.0-flash",
			// This protocol names the model in the path, not the body.
			wantBody: `{"contents":[{"parts":[{"text":"hi"}],"role":"user"}]}`,
			wantURL:  "https://upstream.invalid/v1beta/models/gemini-2.0-flash:generateContent",
		},
		{
			name: "openai stream to gemini", ingress: protocols.ProtocolOpenAI, body: streamBody,
			egress: protocols.ProtocolGemini, provider: "gemini-2.0-flash",
			// Streaming is a different method here, and the SSE framing the
			// decoder expects has to be asked for explicitly.
			wantBody: `{"contents":[{"parts":[{"text":"hi"}],"role":"user"}]}`,
			wantURL:  "https://upstream.invalid/v1beta/models/gemini-2.0-flash:streamGenerateContent?alt=sse",
		},
		{
			name: "claude passthrough", ingress: protocols.ProtocolClaude, body: claudeBody,
			egress: protocols.ProtocolClaude, through: true, provider: "claude-3-5-sonnet-20241022",
			wantBody: `{"max_tokens":64,"messages":[{"role":"user","content":"hi"}],"model":"claude-3-5-sonnet-20241022"}`,
			wantURL:  "https://upstream.invalid/v1/messages",
		},
		{
			name: "claude to openai, full re-encode", ingress: protocols.ProtocolClaude, body: claudeBody,
			egress: protocols.ProtocolOpenAI, provider: "gpt-4o-mini",
			// stream is written out as false rather than left off: this caller
			// did not ask for a stream, and the field carries that decision
			// explicitly to a protocol that defaults it.
			wantBody: `{"max_tokens":64,"messages":[{"content":"hi","role":"user"}],"model":"gpt-4o-mini","stream":false}`,
			wantURL:  "https://upstream.invalid/v1/chat/completions",
		},
	}

	const baseURL = "https://upstream.invalid/v1"

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload, rej := NewTextModality().Admit(context.Background(), Ingress{
				Protocol: tc.ingress, Path: "/v1/chat/completions",
				ContentType: "application/json", Body: []byte(tc.body),
			})
			if rej != nil {
				t.Fatalf("Admit refused a valid body: %+v", rej)
			}

			call, err := payload.PrepareUpstream(Candidate{
				ProviderModelName: tc.provider,
				EgressProtocol:    tc.egress,
				Passthrough:       tc.through,
				BaseURL:           baseURL,
			})
			if err != nil {
				t.Fatalf("PrepareUpstream = %v", err)
			}

			if string(call.Body) != tc.wantBody {
				t.Errorf("body sent upstream\n got: %s\nwant: %s", call.Body, tc.wantBody)
			}
			if got := protocols.JoinUpstreamURL(baseURL, call.Path, tc.egress); got != tc.wantURL {
				t.Errorf("url = %q, want %q (path was %q)", got, tc.wantURL, call.Path)
			}
			if call.ContentType != "application/json" {
				t.Errorf("content type = %q, want application/json", call.ContentType)
			}
		})
	}
}

// TestAdmitRefusesWhatNoCandidateCouldFix pins every way a body can be turned
// away before a provider is picked, and pins WHICH way each one is.
//
// Each of these is a separate exit with its own status, message and reason
// code. The reason code is what anything querying these rows groups by, so a
// row that reaches the wrong exit while still reporting 400 is a silent
// misfiling — which is exactly what asserting only on the shared prefix would
// let through: with "validate: " alone, the no-messages row and the
// no-max_tokens row are interchangeable and neither pins the rule it names.
func TestAdmitRefusesWhatNoCandidateCouldFix(t *testing.T) {
	cases := []struct {
		name       string
		protocol   protocols.ProtocolID
		path, body string
		// reason is matched as a prefix only where the tail is an underlying
		// decoder's wording, which is not ours to pin.
		reason string
		// mustSay is a fragment of the reason that identifies WHICH rule fired,
		// so two rows sharing a prefix cannot stand in for each other.
		mustSay string
	}{
		{"unparseable body", protocols.ProtocolOpenAI, "/v1/chat/completions", `{`, "parse: ", "unexpected end of JSON input"},
		{"no model", protocols.ProtocolOpenAI, "/v1/chat/completions", `{"messages":[{"role":"user","content":"hi"}]}`, "empty_model", "empty_model"},
		{"no messages", protocols.ProtocolOpenAI, "/v1/chat/completions", `{"model":"gpt-4o"}`, "validate: ", "messages"},
		{"claude without max_tokens", protocols.ProtocolClaude, "/v1/messages", `{"model":"c","messages":[{"role":"user","content":"hi"}]}`, "validate: ", "max_tokens"},
		{"gemini path that does not parse", protocols.ProtocolGemini, "/v1beta/nonsense", `{}`, "invalid_gemini_path", "invalid_gemini_path"},
		// The fifth exit: the body parses and names a model, but the full
		// structural decode refuses it. Nothing reached this exit from a table
		// whose own doc claimed to cover the refusals.
		{"body that parses but does not decode", protocols.ProtocolClaude, "/v1/messages",
			`{"model":"c","max_tokens":64,"messages":[{"role":"user","content":{"not":"a content block"}}]}`,
			"invalid_request: ", "invalid_request: "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload, rej := NewTextModality().Admit(context.Background(), Ingress{
				Protocol: tc.protocol, Path: tc.path,
				ContentType: "application/json", Body: []byte(tc.body),
			})
			if rej == nil {
				t.Fatalf("Admit accepted %s (payload=%+v)", tc.body, payload)
			}
			if rej.Status != 400 {
				t.Errorf("status = %d, want 400", rej.Status)
			}
			if rej.Fault != fact.FaultClient {
				t.Errorf("fault = %v, want client: the caller's own request is what is wrong", rej.Fault)
			}
			if !strings.HasPrefix(rej.FailReason, tc.reason) {
				t.Errorf("fail reason = %q, want it to start with %q", rej.FailReason, tc.reason)
			}
			if !strings.Contains(rej.FailReason, tc.mustSay) {
				t.Errorf("fail reason = %q, want it to name %q — without that this row is "+
					"indistinguishable from any other sharing its prefix", rej.FailReason, tc.mustSay)
			}
			if rej.Message == "" {
				t.Error("message is empty; the caller would get an error body with nothing in it")
			}
		})
	}
}

// TestRoutingReportsWhatTheCallerAsked keeps Routing honest about the two
// fields the kernel routes on.
func TestRoutingReportsWhatTheCallerAsked(t *testing.T) {
	payload, rej := NewTextModality().Admit(context.Background(), Ingress{
		Protocol: protocols.ProtocolOpenAI, Path: "/v1/chat/completions",
		Body: []byte(`{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`),
	})
	if rej != nil {
		t.Fatalf("Admit refused: %+v", rej)
	}
	got := payload.Routing()
	if got.Model != "gpt-4o" || !got.Stream {
		t.Errorf("Routing() = %+v, want model gpt-4o and streaming", got)
	}
}
