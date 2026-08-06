package gateway

import (
	"bytes"
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
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
	promptSeen     int
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
		promptSeen: func() int {
			if rc.usage == nil {
				return 0
			}
			return rc.usage.PromptTokens
		}(),
	}
}

// runNewStream drives the same response through the modality.
func runNewStream(t *testing.T, ingress, egress protocols.ProtocolID, passthrough bool, callerPath, callerBody string, resp *http.Response) streamOutcome {
	t.Helper()
	dir := t.TempDir()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).
		WithContext(context.Background())
	c.Set(BodiesDirContextKey, dir)

	payload, rej := NewTextModality().Admit(context.Background(), Ingress{
		Protocol: ingress, Path: callerPath, Body: []byte(callerBody),
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
		out.promptSeen = d.Usage.Prompt
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

// generatedID matches the identifier the gateway mints for a response whose
// provider did not supply one.
var generatedID = regexp.MustCompile(`gen-[A-Za-z0-9]+`)

// steady replaces what is freshly minted on every run with a fixed token, after
// checking the one property worth keeping about it: that a response uses ONE id
// throughout. Without that check the substitution would also hide chunks
// disagreeing about which response they belong to.
func steady(t *testing.T, body string) string {
	t.Helper()
	if ids := generatedID.FindAllString(body, -1); len(ids) > 0 {
		for _, id := range ids[1:] {
			if id != ids[0] {
				t.Errorf("one response carries two identifiers, %q and %q", ids[0], id)
				break
			}
		}
	}
	return generatedID.ReplaceAllString(body, "gen-*")
}

func compareStream(t *testing.T, old, got streamOutcome) {
	t.Helper()
	// A provider that supplies no identifier gets one minted per run, so the
	// two implementations cannot be compared on it — only on everything else.
	old.clientBody, got.clientBody = steady(t, old.clientBody), steady(t, got.clientBody)
	old.captured, got.captured = steady(t, old.captured), steady(t, got.captured)
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
	if got.promptSeen != old.promptSeen {
		t.Errorf("prompt tokens = %d, previously %d", got.promptSeen, old.promptSeen)
	}
}

// requireDelivered fails a comparison that compared two nothings.
//
// Two implementations agreeing is worth nothing when what they agree on is that
// the fixture produced no response — the assertions all pass on empty strings,
// and the case reads as covered while exercising none of the path it was added
// for.
func requireDelivered(t *testing.T, got streamOutcome) {
	t.Helper()
	if got.clientBody == "" {
		t.Fatalf("this case delivered nothing (verdict %v); it proves only that both sides fail alike", got.delivery.Verdict)
	}
	if !got.delivery.Complete {
		t.Fatalf("this case did not complete (fail reason %q)", got.delivery.FailReason)
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
		// callerPath matters for protocols that carry the model in the URL
		// rather than the body.
		callerPath string
		caller     string
		upstream   string
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
			// Gemini reaches the relay through a different entry point, the one
			// that splits on newlines rather than scanning SSE frames. Nothing
			// else in this table goes through it.
			name:        "openai caller, gemini provider",
			ingress:     protocols.ProtocolOpenAI,
			egress:      protocols.ProtocolGemini,
			callerModel: "gpt-4o",
			caller:      `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			upstream: `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]}}],"modelVersion":"gemini-2.0-flash-real"}` + "\n\n" +
				`data: {"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3,"totalTokenCount":10},"modelVersion":"gemini-2.0-flash-real"}` + "\n\n",
		},
		{
			// The completion signal is the LAST event and it has no trailing
			// blank line, so the decoder holds it until asked to finish. Any
			// earlier terminated signal would make that flush unnecessary and
			// the case would pass without it.
			name:        "claude caller, claude provider, completion held back",
			ingress:     protocols.ProtocolClaude,
			egress:      protocols.ProtocolClaude,
			passthrough: true,
			callerModel: "claude-3-5-sonnet",
			caller:      `{"model":"claude-3-5-sonnet","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`,
			upstream: "event: message_start\n" +
				`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-3-5-sonnet-20241022","usage":{"input_tokens":7,"output_tokens":0}}}` + "\n\n" +
				"event: content_block_delta\n" +
				`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n\n" +
				"event: message_delta\n" +
				`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":3}}`,
		},
		{
			// Gemini's decoder holds a frame until a blank line closes it, so a
			// stream whose last frame arrives without one is only completed by
			// the flush at the end. Claude's decodes per line and would pass
			// without it.
			name:        "gemini caller, gemini provider, completion held back",
			ingress:     protocols.ProtocolGemini,
			egress:      protocols.ProtocolGemini,
			passthrough: true,
			callerModel: "gemini-2.0-flash",
			callerPath:  "/v1beta/models/gemini-2.0-flash:streamGenerateContent",
			caller:      `{"contents":[{"role":"user","parts":[{"text":"hi"}]}]}`,
			upstream: `data: {"candidates":[{"content":{"role":"model","parts":[{"text":"hi"}]}}],"modelVersion":"gemini-2.0-flash-real"}` + "\n\n" +
				`data: {"candidates":[{"content":{"role":"model","parts":[]},"finishReason":"STOP"}],"usageMetadata":{"promptTokenCount":7,"candidatesTokenCount":3,"totalTokenCount":10},"modelVersion":"gemini-2.0-flash-real"}`,
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
			path := tc.callerPath
			if path == "" {
				path = "/v1/chat/completions"
			}
			got := runNewStream(t, tc.ingress, tc.egress, tc.passthrough, path, tc.caller, upstreamStream(t, tc.upstream))
			requireDelivered(t, got)
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
	got := runNewStream(t, protocols.ProtocolOpenAI, protocols.ProtocolOpenAI, true, "/v1/chat/completions", caller, upstreamStream(t, truncated))
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
	got := runNewStream(t, protocols.ProtocolOpenAI, protocols.ProtocolOpenAI, true, "/v1/chat/completions", caller, upstreamStream(t, ""))
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

// brokenAfter hands over its bytes and then fails, the way a provider that
// closes its connection non-standardly does. A buffer cannot do this, and a
// buffer is what every other fixture here is — which is why the branch below
// went unguarded: it is only read when the read itself failed.
type brokenAfter struct {
	data []byte
	err  error
}

func (r *brokenAfter) Read(p []byte) (int, error) {
	if len(r.data) == 0 {
		return 0, r.err
	}
	n := copy(p, r.data)
	r.data = r.data[n:]
	return n, nil
}

func upstreamStreamThatBreaks(t *testing.T, body string, err error) *http.Response {
	t.Helper()
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"text/event-stream"}},
		Body:       io.NopCloser(&brokenAfter{data: []byte(body), err: err}),
	}
}

// TestWhatCountsAsAWholeStreamDiffersByProtocol pins the one thing the two
// pumps deliberately disagree about.
//
// Both forgive a transport error that arrives after the response was already
// whole — an upstream closing badly on its way out interrupts nothing. They do
// not agree on what whole means. OpenAI reports usage in a frame of its own, so
// a stream that reached the terminator without one stopped short; the other
// protocols carry usage inside their terminal event, and demanding it
// separately would file finished streams as broken.
//
// Both fixtures deliberately omit usage. That is the case where one condition
// for both would quietly change an answer.
func TestWhatCountsAsAWholeStreamDiffersByProtocol(t *testing.T) {
	const openAIStream = `data: {"id":"c1","model":"gpt-4o-real","choices":[{"delta":{"content":"hi"}}]}` + "\n\n" +
		"data: [DONE]\n\n"
	const openAICaller = `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`
	const claudeCaller = `{"model":"claude-3-5-sonnet","max_tokens":64,"stream":true,"messages":[{"role":"user","content":"hi"}]}`
	claudeStream := "event: message_start\n" +
		`data: {"type":"message_start","message":{"id":"msg_1","model":"claude-3-5-sonnet-20241022"}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","index":0,"delta":{"type":"text_delta","text":"hi"}}` + "\n\n" +
		// The completion signal, carrying no usage: that omission is the whole
		// point of this fixture.
		"event: message_delta\n" +
		`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"}}` + "\n\n" +
		"event: message_stop\n" +
		`data: {"type":"message_stop"}` + "\n\n"

	t.Run("openai wants its usage frame", func(t *testing.T) {
		old := runOldStream(t, protocols.ProtocolOpenAI, protocols.ProtocolOpenAI, true, "gpt-4o",
			upstreamStreamThatBreaks(t, openAIStream, io.ErrUnexpectedEOF))
		got := runNewStream(t, protocols.ProtocolOpenAI, protocols.ProtocolOpenAI, true, "/v1/chat/completions", openAICaller,
			upstreamStreamThatBreaks(t, openAIStream, io.ErrUnexpectedEOF))
		compareStream(t, old, got)

		if got.delivery.Complete {
			t.Error("reported complete; the terminator arrived but the usage frame never did, so the stream stopped short")
		}
		if got.delivery.Fault != fact.FaultUpstream {
			t.Errorf("fault = %v, want upstream", got.delivery.Fault)
		}
	})

	t.Run("claude does not", func(t *testing.T) {
		old := runOldStream(t, protocols.ProtocolClaude, protocols.ProtocolClaude, true, "claude-3-5-sonnet",
			upstreamStreamThatBreaks(t, claudeStream, io.ErrUnexpectedEOF))
		got := runNewStream(t, protocols.ProtocolClaude, protocols.ProtocolClaude, true, "/v1/messages", claudeCaller,
			upstreamStreamThatBreaks(t, claudeStream, io.ErrUnexpectedEOF))
		compareStream(t, old, got)

		if !got.delivery.Complete {
			t.Errorf("reported incomplete (%q); this stream reached its terminal event, and usage is not what ends it", got.delivery.FailReason)
		}
	})
}
