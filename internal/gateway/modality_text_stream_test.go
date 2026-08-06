package gateway

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/yolorouter/yolorouter/internal/fact"
	"github.com/yolorouter/yolorouter/internal/model"
	"github.com/yolorouter/yolorouter/internal/protocols"
)

// streamRequestID is shared by both sides of every comparison below.
const streamRequestID = "stream-under-test"

// streamOutcome is what a streaming delivery is answerable for. It differs from
// deliveryOutcome in where the audit bytes live: a stream's account is the
// capture file, not a field on the exchange.
type streamOutcome struct {
	delivery       fact.Delivery
	clientStatus   int
	clientBody     string
	captured       string
	captureExists  bool
	firstByteSent  bool
	completionSeen int
}

func upstreamStream(t *testing.T, body string) *http.Response {
	t.Helper()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"text/event-stream"}},
		Body:       io.NopCloser(bytes.NewBufferString(body)),
	}
}

// runOldStream drives a cross-protocol stream the way the service does today.
func runOldStream(t *testing.T, ingress, egress protocols.ProtocolID, passthrough bool, callerModel string, resp *http.Response) streamOutcome {
	t.Helper()
	dir := t.TempDir()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).
		WithContext(context.Background())
	c.Set(BodiesDirContextKey, dir)

	// Both sides carry the same request id. It reaches caller bytes only
	// through an error frame, so an asymmetry here is invisible today and
	// would silently defeat the first mid-stream-error case anyone adds.
	rc := &Exchange{requestID: streamRequestID, ingress: ingress, originalModel: callerModel, isStream: true}
	d := (&Service{}).processDispatchResponseStream(c, rc, ingress,
		&EgressDecision{Protocol: egress, Passthrough: passthrough},
		model.ModelCandidate{ProviderModelName: "provider-model"},
		&model.Provider{}, model.ProviderKey{}, resp, time.Now())

	captured, exists := readCapture(t, dir, rc.requestID)
	return streamOutcome{
		delivery: d, clientStatus: w.Code, clientBody: w.Body.String(),
		captured:      captured,
		captureExists: exists,
		firstByteSent: rc.firstByteSent,
		completionSeen: func() int {
			if rc.usage == nil {
				return 0
			}
			return rc.usage.CompletionTokens
		}(),
	}
}

// runNewStream drives the same response through the modality.
func runNewStream(t *testing.T, ingress, egress protocols.ProtocolID, passthrough bool, callerBody string, resp *http.Response) streamOutcome {
	t.Helper()
	dir := t.TempDir()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).
		WithContext(context.Background())
	c.Set(BodiesDirContextKey, dir)

	payload, rej := NewTextModality().Admit(context.Background(), Ingress{
		Protocol: ingress, Path: "/v1/chat/completions", Body: []byte(callerBody),
	})
	if rej != nil {
		t.Fatalf("Admit refused a valid body: %+v", rej)
	}
	if _, err := payload.PrepareUpstream(Candidate{
		ProviderModelName: "provider-model", EgressProtocol: egress, Passthrough: passthrough,
	}); err != nil {
		t.Fatalf("PrepareUpstream = %v", err)
	}

	rc := &Exchange{requestID: streamRequestID, ingress: ingress, isStream: true}
	tools, release := (&Service{}).newDeliveryTools(c, rc, TransferLimits{}, true)
	d := payload.Deliver(tools, resp)
	// The kernel takes the toolbox back before anything reads the audit trail:
	// that is when an empty capture is removed and the file is closed, and a test
	// that read the file first would be reading a record nobody had finished.
	release()

	captured, exists := readCapture(t, dir, rc.requestID)
	out := streamOutcome{
		delivery: d, clientStatus: w.Code, clientBody: w.Body.String(),
		captured:      captured,
		captureExists: exists,
		firstByteSent: rc.firstByteSent,
	}
	if d.Usage != nil {
		out.completionSeen = d.Usage.Completion
	}
	return out
}

// readCapture returns the capture's content and whether the file is there at
// all.
//
// The two are different answers. A stream that sent nothing should leave NO
// file, and reading both as "" makes that assertion pass just as happily when
// an empty one was left behind.
func readCapture(t *testing.T, dir, requestID string) (content string, exists bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(dir, requestID+".stream"))
	if os.IsNotExist(err) {
		return "", false
	}
	if err != nil {
		t.Fatalf("read capture file: %v", err)
	}
	return string(b), true
}

func compareStream(t *testing.T, old, got streamOutcome) {
	t.Helper()
	if got.clientStatus != old.clientStatus {
		t.Errorf("caller received status %d, previously %d", got.clientStatus, old.clientStatus)
	}
	if got.clientBody != old.clientBody {
		t.Errorf("caller received\n got: %q\nwant: %q", got.clientBody, old.clientBody)
	}
	if got.captured != old.captured {
		t.Errorf("capture file holds\n got: %q\nwant: %q", got.captured, old.captured)
	}
	if got.captureExists != old.captureExists {
		t.Errorf("capture file exists = %v, previously %v", got.captureExists, old.captureExists)
	}
	if got.captured != got.clientBody {
		t.Errorf("capture file and caller disagree; the file promises exactly what the caller received\n file: %q\ncaller: %q", got.captured, got.clientBody)
	}
	if got.firstByteSent != old.firstByteSent {
		t.Errorf("first byte recorded as sent = %v, previously %v", got.firstByteSent, old.firstByteSent)
	}
	if got.completionSeen != old.completionSeen {
		t.Errorf("completion tokens = %d, previously %d", got.completionSeen, old.completionSeen)
	}
	if got.delivery.Verdict != old.delivery.Verdict {
		t.Errorf("verdict = %v, previously %v", got.delivery.Verdict, old.delivery.Verdict)
	}
	if got.delivery.Committed != old.delivery.Committed {
		t.Errorf("committed = %v, previously %v", got.delivery.Committed, old.delivery.Committed)
	}
	if got.delivery.Complete != old.delivery.Complete {
		t.Errorf("complete = %v, previously %v", got.delivery.Complete, old.delivery.Complete)
	}
}

// TestTheModalityStreamsWhatTheServiceStreamed runs one upstream stream through
// both implementations and holds them to the same account.
//
// Equal caller bytes is the loudest of these but not the only one that matters:
// the capture file has to hold the same thing, the usage has to survive the
// move, and a failure has to leave the same verdict behind. A refactor that got
// the bytes right and any of those wrong would look correct from the caller's
// side and be wrong everywhere the request is later answered for.
func TestTheModalityStreamsWhatTheServiceStreamed(t *testing.T) {
	// The provider's own name for the model, deliberately not the caller's: a
	// rewrite to the same string proves nothing, and that is exactly the fixture
	// that let a dropped rewrite pass.
	const claudeUpstream = "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-3-5-sonnet-20241022","usage":{"input_tokens":7,"output_tokens":0}}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	cases := []struct {
		name        string
		ingress     protocols.ProtocolID
		egress      protocols.ProtocolID
		passthrough bool
		callerModel string
		caller      string
		upstream    string
	}{
		{
			name:        "openai caller, claude provider",
			ingress:     protocols.ProtocolOpenAI,
			egress:      protocols.ProtocolClaude,
			callerModel: "gpt-4o",
			caller:      `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			upstream:    claudeUpstream,
		},
		{
			name:        "claude caller, openai provider",
			ingress:     protocols.ProtocolClaude,
			egress:      protocols.ProtocolOpenAI,
			callerModel: "claude-3-5-sonnet",
			caller:      `{"model":"claude-3-5-sonnet","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			upstream: `data: {"id":"c1","model":"gpt-4o-real","choices":[{"delta":{"role":"assistant","content":"hi"}}]}` + "\n\n" +
				`data: {"id":"c1","model":"gpt-4o-real","choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}` + "\n\n" +
				"data: [DONE]\n\n",
		},
		{
			name:        "openai caller, openai provider, forwarded as-is",
			ingress:     protocols.ProtocolOpenAI,
			egress:      protocols.ProtocolOpenAI,
			passthrough: true,
			callerModel: "gpt-4o",
			caller:      `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			// Opens with a comment heartbeat and a retry directive: lines that
			// arrive before any data and must reach the caller in order once the
			// first data frame commits the response, not be dropped.
			upstream: ": ping\n\nretry: 1000\n\n" +
				`data: {"id":"c1","model":"gpt-4o-real","choices":[{"delta":{"role":"assistant","content":"hi"}}]}` + "\n\n" +
				`data: {"id":"c1","model":"gpt-4o-real","choices":[{"delta":{},"finish_reason":"stop"}],"usage":{"prompt_tokens":7,"completion_tokens":3,"total_tokens":10}}` + "\n\n" +
				"data: [DONE]\n\n",
		},
		{
			// Gemini's stream is newline-delimited JSON, not SSE, and the relay
			// reaches it through a different entry point. Nothing else in this
			// table goes through that one.
			name:        "openai caller, gemini provider",
			ingress:     protocols.ProtocolOpenAI,
			egress:      protocols.ProtocolGemini,
			callerModel: "gpt-4o",
			caller:      `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			upstream:    `{"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3,"totalTokenCount":10},"modelVersion":"gemini-2.0-flash-real"}` + "\n",
		},
		{
			// The last event has no trailing blank line, so its terminal delta
			// only appears once the decoder is asked to finish. A stream that
			// ends this way is complete; one whose decoder was never flushed
			// reads as having stopped short.
			name:        "claude caller, claude provider, no trailing blank line",
			ingress:     protocols.ProtocolClaude,
			egress:      protocols.ProtocolClaude,
			passthrough: true,
			callerModel: "claude-3-5-sonnet",
			caller:      `{"model":"claude-3-5-sonnet","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			upstream:    strings.TrimSuffix(claudeUpstream, "\n"),
		},
		{
			name:        "claude caller, claude provider, forwarded as-is",
			ingress:     protocols.ProtocolClaude,
			egress:      protocols.ProtocolClaude,
			passthrough: true,
			callerModel: "claude-3-5-sonnet",
			caller:      `{"model":"claude-3-5-sonnet","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			upstream:    claudeUpstream,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			old := runOldStream(t, tc.ingress, tc.egress, tc.passthrough, tc.callerModel, upstreamStream(t, tc.upstream))
			got := runNewStream(t, tc.ingress, tc.egress, tc.passthrough, tc.caller, upstreamStream(t, tc.upstream))
			compareStream(t, old, got)
		})
	}
}

// TestAStreamThatEndsWithoutItsTerminatorIsNotCalledComplete pins the case an
// equal-bytes comparison alone would let through.
//
// The caller may well hold the whole answer — some upstreams simply never send
// the terminator — so nothing is injected into their stream and the status
// stays 200. What cannot be said is that the end of the response was seen, and
// a delivery reported complete here is one the audit trail would later claim
// finished when nobody knows that it did.
func TestAStreamThatEndsWithoutItsTerminatorIsNotCalledComplete(t *testing.T) {
	const truncated = `data: {"id":"c1","model":"gpt-4o-real","choices":[{"delta":{"content":"hi"}}]}` + "\n\n"
	const caller = `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`

	old := runOldStream(t, protocols.ProtocolOpenAI, protocols.ProtocolOpenAI, true, "gpt-4o", upstreamStream(t, truncated))
	got := runNewStream(t, protocols.ProtocolOpenAI, protocols.ProtocolOpenAI, true, caller, upstreamStream(t, truncated))
	compareStream(t, old, got)

	if got.delivery.Complete {
		t.Error("delivery reports complete; the stream ended without its terminator, so nobody saw it finish")
	}
	if got.delivery.Verdict != fact.VerdictSettled {
		t.Errorf("verdict = %v, want settled; the caller already has these bytes and no other provider can replace them", got.delivery.Verdict)
	}
	if got.clientStatus != http.StatusOK {
		t.Errorf("caller received status %d, want 200 — the headers went out with the first frame", got.clientStatus)
	}
}

// TestAStreamThatNeverSendsDataLeavesTheChainOpen pins the failover window.
//
// An upstream can answer 2xx and then die before its first data frame. Nothing
// has reached the caller at that point, which is the whole reason the response
// is not committed up front — and it means this candidate can still be replaced
// by a healthy one. Reporting it as settled would turn a recoverable provider
// failure into a failed request.
func TestAStreamThatNeverSendsDataLeavesTheChainOpen(t *testing.T) {
	const caller = `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`

	old := runOldStream(t, protocols.ProtocolOpenAI, protocols.ProtocolOpenAI, true, "gpt-4o", upstreamStream(t, ""))
	got := runNewStream(t, protocols.ProtocolOpenAI, protocols.ProtocolOpenAI, true, caller, upstreamStream(t, ""))
	compareStream(t, old, got)

	if got.delivery.Verdict != fact.VerdictNextCandidate {
		t.Errorf("verdict = %v, want next_candidate; nothing reached the caller, so another provider can still serve them", got.delivery.Verdict)
	}
	if got.delivery.Committed {
		t.Error("delivery reports committed; no data frame ever arrived, so no status went out")
	}
	if got.captureExists {
		t.Errorf("a capture file was left behind holding %q; nothing was ever sent, and an empty one renders as a capture worth opening", got.captured)
	}
}
