package gateway

import (
	"context"
	"testing"

	"github.com/yolorouter/yolorouter/internal/fact"
	"github.com/yolorouter/yolorouter/internal/model"
	"github.com/yolorouter/yolorouter/internal/protocols"
)

// TestPrepareUpstreamMatchesTheExistingBuilder is the assertion that has to
// hold before the kernel can switch over: the modality builds the same request
// the service builds today, byte for byte.
//
// It is written against buildUpstreamBody rather than against a recorded
// fixture on purpose. A fixture pins what the bytes were on the day it was
// captured; this pins that two implementations agree, which is the property
// that has to survive the switch.
func TestPrepareUpstreamMatchesTheExistingBuilder(t *testing.T) {
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
	}{
		{"openai passthrough", protocols.ProtocolOpenAI, openAIBody, protocols.ProtocolOpenAI, true, "gpt-4o-mini"},
		{"openai streaming passthrough", protocols.ProtocolOpenAI, streamBody, protocols.ProtocolOpenAI, true, "gpt-4o-mini"},
		{"openai streaming without stream_options", protocols.ProtocolOpenAI, streamNoOptsBody, protocols.ProtocolOpenAI, true, "gpt-4o-mini"},
		{"openai to claude, full re-encode", protocols.ProtocolOpenAI, openAIBody, protocols.ProtocolClaude, false, "claude-3-5-sonnet-20241022"},
		{"openai to gemini, full re-encode", protocols.ProtocolOpenAI, openAIBody, protocols.ProtocolGemini, false, "gemini-2.0-flash"},
		{"openai stream to gemini", protocols.ProtocolOpenAI, streamBody, protocols.ProtocolGemini, false, "gemini-2.0-flash"},
		{"claude passthrough", protocols.ProtocolClaude, claudeBody, protocols.ProtocolClaude, true, "claude-3-5-sonnet-20241022"},
		{"claude to openai, full re-encode", protocols.ProtocolClaude, claudeBody, protocols.ProtocolOpenAI, false, "gpt-4o-mini"},
	}

	const baseURL = "https://upstream.invalid/v1"
	svc := &Service{}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			payload, rej := NewTextModality().Admit(context.Background(), Ingress{
				Protocol: tc.ingress, Path: "/v1/chat/completions",
				ContentType: "application/json", Body: []byte(tc.body),
			})
			if rej != nil {
				t.Fatalf("Admit refused a valid body: %+v", rej)
			}

			// The service's builder reads everything off the exchange, so the
			// exchange has to be set up to describe the same request.
			cand := model.ModelCandidate{ProviderModelName: tc.provider}
			rc := &Exchange{
				ingress:          tc.ingress,
				requestBody:      []byte(tc.body),
				candidate:        &cand,
				isStream:         payload.meta.Stream,
				wantsStreamUsage: payload.meta.WantsStreamUsage,
			}
			egress := &EgressDecision{Protocol: tc.egress, BaseURL: baseURL, Passthrough: tc.through}

			wantBody, wantURL, _, err := svc.buildUpstreamBody(rc, tc.ingress, egress)
			if err != nil {
				t.Fatalf("buildUpstreamBody = %v", err)
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

			if string(call.Body) != string(wantBody) {
				t.Errorf("body differs from the existing builder\n got: %s\nwant: %s", call.Body, wantBody)
			}
			if got := protocols.JoinUpstreamURL(baseURL, call.Path, tc.egress); got != wantURL {
				t.Errorf("url = %q, want %q (path was %q)", got, wantURL, call.Path)
			}
			if call.ContentType == "" {
				t.Error("ContentType is empty; the kernel would have to guess one")
			}
		})
	}
}

// TestAdmitRefusesWhatNoCandidateCouldFix pins that the refusals moved intact.
// Each of these used to be a separate exit in the handler, with its own status,
// its own message and its own reason code; the point of moving them is that
// they keep those three unchanged.
func TestAdmitRefusesWhatNoCandidateCouldFix(t *testing.T) {
	cases := []struct {
		name       string
		protocol   protocols.ProtocolID
		path, body string
		reason     string
	}{
		{"unparseable body", protocols.ProtocolOpenAI, "/v1/chat/completions", `{`, "parse: "},
		{"no model", protocols.ProtocolOpenAI, "/v1/chat/completions", `{"messages":[{"role":"user","content":"hi"}]}`, "empty_model"},
		{"no messages", protocols.ProtocolOpenAI, "/v1/chat/completions", `{"model":"gpt-4o"}`, "validate: "},
		{"claude without max_tokens", protocols.ProtocolClaude, "/v1/messages", `{"model":"c","messages":[{"role":"user","content":"hi"}]}`, "validate: "},
		{"gemini path that does not parse", protocols.ProtocolGemini, "/v1beta/nonsense", `{}`, "invalid_gemini_path"},
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
			if len(rej.FailReason) < len(tc.reason) || rej.FailReason[:len(tc.reason)] != tc.reason {
				t.Errorf("fail reason = %q, want it to start with %q", rej.FailReason, tc.reason)
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
