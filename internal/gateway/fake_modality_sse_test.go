package gateway

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/yolorouter/yolorouter/internal/fact"
	"github.com/yolorouter/yolorouter/internal/protocols"
)

// An event-named SSE stream — the shape an image render sends — differs from
// the text modality's in the one way that matters to settlement: what it is
// worth is not decided when the status goes out.
//
// The status goes out on the first frame, before anything is known. Frames then
// arrive under event names rather than as a bare data channel, usage arrives at
// the end and can be restated, and an error can arrive after the caller has
// already been told 200 and shown part of an answer. That last case is the one
// the interface has to hold: the caller cannot be given a different status, but
// the request must not be billed as the success it looks like.

// fakeRenderPayload consumes an event-named stream and forwards each frame.
type fakeRenderPayload struct {
	fakePayloadBase

	framesSeen int
	usage      *fact.UsageReported
}

// Deliver forwards the render stream, committing on the first frame.
//
// The commit is early because it has to be: a progressive response cannot be
// held back until it is known to be good, which is the entire reason a delivery
// reports a client status and a billing status separately.
func (p *fakeRenderPayload) Deliver(tools DeliveryTools, resp *http.Response) fact.Delivery {
	defer func() { _ = resp.Body.Close() }()

	// Sized from the delivery's own budget, not from the Scanner's default.
	//
	// A render puts its image in the frame as base64, so one frame is megabytes
	// where a line of chat is bytes. At the default 64 KiB, Scan does not
	// truncate the frame — it stops, and the loop below would then report a
	// truncated 200 for a stream whose first frame never arrived. The cap is a
	// property of what this modality carries, which is why it is a number the
	// kernel resolved for this delivery rather than a constant in here.
	scanner := bufio.NewScanner(resp.Body)
	if limit := tools.Limits.MaxFrameBytes; limit > 0 {
		scanner.Buffer(make([]byte, 0, 64*1024), limit)
	}
	var pendingEvent string
	sawTerminal := false

	for scanner.Scan() {
		line := scanner.Text()
		switch {
		case strings.HasPrefix(line, "event:"):
			pendingEvent = strings.TrimSpace(strings.TrimPrefix(line, "event:"))
			continue
		case !strings.HasPrefix(line, "data:"):
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))

		if pendingEvent == "error" {
			// After the commit below, so the caller already holds a 200 and
			// part of a render. They cannot be given a different status now.
			// What can still be said is that this is not worth money.
			if !tools.Client.Committed() {
				return fact.HandedOn(fact.FaultUpstream, "render_error_before_commit", nil)
			}
			return fact.Truncated(http.StatusOK, http.StatusInternalServerError,
				fact.FaultUpstream, "render_failed: "+payload, errors.New(payload)).WithUsage(p.usage)
		}

		if !tools.Client.Committed() {
			tools.Client.Inject(http.Header{"Content-Type": {"text/event-stream"}})
			if cerr := tools.Client.Commit(http.StatusOK); cerr != nil {
				return fact.Undelivered(http.StatusInternalServerError, fact.VerdictSettled,
					fact.FaultGateway, "commit_failed: "+cerr.Error(), cerr)
			}
		}
		if _, werr := tools.Client.Write([]byte(line + "\n\n")); werr != nil {
			// A caller who stopped reading is not a provider fault, and must
			// never be recorded as one: the provider is still working, and a
			// disconnect filed against it counts towards taking it out of
			// rotation for an outage that was never theirs.
			return fact.Truncated(http.StatusOK, 499, fact.FaultClient, "client_write", werr).WithUsage(p.usage)
		}
		if ferr := tools.Client.Flush(); ferr != nil {
			return fact.Truncated(http.StatusOK, 499, fact.FaultClient, "client_flush", ferr).WithUsage(p.usage)
		}
		p.framesSeen++

		// Usage arrives on a frame like any other and the LAST statement wins:
		// a render restates its cost when it finishes at a different size than
		// it planned, and keeping the first would bill the estimate.
		var frame struct {
			Usage *struct {
				Images int `json:"images"`
			} `json:"usage"`
		}
		if err := json.Unmarshal([]byte(payload), &frame); err == nil && frame.Usage != nil {
			p.usage = &fact.UsageReported{Unit: fact.UnitToken, Completion: frame.Usage.Images}
		}
		if pendingEvent == "completed" {
			sawTerminal = true
		}
		pendingEvent = ""
	}

	if err := scanner.Err(); err != nil {
		if tools.Client.CallerGone() {
			return fact.Truncated(http.StatusOK, 499, fact.FaultClient, "client_disconnected", err).WithUsage(p.usage)
		}
		return fact.Truncated(http.StatusOK, http.StatusInternalServerError,
			fact.FaultUpstream, "render_stream_broke", err).WithUsage(p.usage)
	}
	if !tools.Client.Committed() {
		return fact.HandedOn(fact.FaultUpstream, "render_sent_nothing", nil)
	}
	if !sawTerminal {
		return fact.Truncated(http.StatusOK, http.StatusInternalServerError,
			fact.FaultUpstream, "render_unfinished", nil).WithUsage(p.usage)
	}
	return fact.Succeeded(http.StatusOK).WithUsage(p.usage)
}

func (p *fakeRenderPayload) FinalizeUsage(fact.Delivery) *fact.UsageReported { return p.usage }

type fakeRenderModality struct{}

func (fakeRenderModality) ID() ModalityID         { return ModalityID("fake_render") }
func (fakeRenderModality) Limits() TransferLimits { return TransferLimits{} }

func (fakeRenderModality) Admit(context.Context, Ingress) (Payload, *Rejection) {
	return &fakeRenderPayload{}, nil
}

// deliverFakeRender runs one render stream and reports what the caller got.
func deliverFakeRender(t *testing.T, upstreamBody string) (fact.Delivery, *httptest.ResponseRecorder, *fakeRenderPayload) {
	t.Helper()
	payload, rej := fakeRenderModality{}.Admit(context.Background(), Ingress{
		Protocol: protocols.ProtocolOpenAI, Path: "/v1/images/generations",
	})
	if rej != nil {
		t.Fatalf("Admit refused: %+v", rej)
	}
	render, ok := payload.(*fakeRenderPayload)
	if !ok {
		t.Fatalf("Admit returned %T, want the render payload", payload)
	}

	tools, release, w := fakeDelivery{
		path: "/v1/images/generations", requestID: "req-fake-render", isStream: true,
	}.tools(t)
	defer release()
	d := render.Deliver(tools, &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"text/event-stream"}},
		Body:       io.NopCloser(strings.NewReader(upstreamBody)),
	})
	return d, w, render
}

// TestARenderThatFailsAfterItsFirstFrameIsNotBilledAsASuccess is the case this
// fake exists for.
//
// The caller has a 200 and part of a render. Nothing can change that. But every
// column an operator or an invoice reads would say this request succeeded, and
// the only reason it does not is that the delivery is allowed to report two
// statuses: the one the caller saw, and the one settlement reasons about. A
// single status code cannot express this, and the modalities that ran before
// this seam existed each decided for themselves which of the two to report.
func TestARenderThatFailsAfterItsFirstFrameIsNotBilledAsASuccess(t *testing.T) {
	const stream = "event: partial\n" +
		`data: {"b64":"first-tile"}` + "\n\n" +
		"event: error\n" +
		`data: {"message":"renderer ran out of memory"}` + "\n\n"

	d, w, _ := deliverFakeRender(t, stream)

	if w.Code != http.StatusOK {
		t.Fatalf("caller received %d, want 200: the status went out on the first frame and cannot be changed", w.Code)
	}
	if d.ClientStatus != http.StatusOK {
		t.Errorf("ClientStatus = %d, want 200 — the record has to agree with what the caller saw", d.ClientStatus)
	}
	if d.BillingStatus != http.StatusInternalServerError {
		t.Errorf("BillingStatus = %d, want 500: a render that failed halfway is not a sale", d.BillingStatus)
	}
	if d.Complete {
		t.Error("a render that stopped on an error was reported complete")
	}
	if d.Fault != fact.FaultUpstream {
		t.Errorf("fault = %v, want upstream", d.Fault)
	}
	if !strings.Contains(w.Body.String(), "first-tile") {
		t.Error("the frames that did arrive were not forwarded; the caller paid the latency for nothing")
	}
}

// TestARenderErrorBeforeAnyFrameLeavesTheChainOpen is the other side of the
// same branch, and the reason the commit is checked rather than assumed.
//
// Before the first frame nothing has reached the caller, so this candidate can
// still be replaced. Reporting it the same way as the case above would turn a
// recoverable provider failure into a failed request.
func TestARenderErrorBeforeAnyFrameLeavesTheChainOpen(t *testing.T) {
	const stream = "event: error\n" + `data: {"message":"no capacity"}` + "\n\n"

	d, w, _ := deliverFakeRender(t, stream)

	if d.Committed {
		t.Fatal("something reached the caller before the first frame")
	}
	if d.Verdict != fact.VerdictNextCandidate {
		t.Errorf("verdict = %v, want the chain to continue", d.Verdict)
	}
	if w.Body.Len() != 0 {
		t.Errorf("caller received %d bytes from a render that never started", w.Body.Len())
	}
}

// TestARenderRestatesItsUsageAndTheLastStatementWins pins the frame-carried
// accounting.
//
// A render announces what it expects to produce and corrects itself when it
// finishes. Keeping the first figure bills the estimate; keeping a sum bills
// twice.
func TestARenderRestatesItsUsageAndTheLastStatementWins(t *testing.T) {
	const stream = "event: partial\n" +
		`data: {"b64":"tile","usage":{"images":4}}` + "\n\n" +
		"event: completed\n" +
		`data: {"b64":"final","usage":{"images":2}}` + "\n\n"

	d, _, render := deliverFakeRender(t, stream)

	if d.Usage == nil {
		t.Fatal("a completed render reported no usage")
	}
	if d.Usage.Completion != 2 {
		t.Errorf("billed %d images, want 2: the render's own final count is the one that is true — "+
			"4 is the estimate it opened with, 6 is both of them added up", d.Usage.Completion)
	}
	if !d.Complete {
		t.Error("a render that reached its terminal event was not reported complete")
	}
	// Both frames were forwarded. Restating the cost is not the same as dropping
	// the frame that carried the earlier figure, and a modality that swallowed
	// the partial would still report the right usage while showing the caller
	// half a render.
	if render.framesSeen != 2 {
		t.Errorf("forwarded %d frames, want 2: the restated cost is about what the render "+
			"charged, not about which frames the caller was shown", render.framesSeen)
	}
}

// TestARenderThatStopsWithoutFinishingIsNotASuccessEither covers the ending
// with no error frame at all: the stream simply stops.
func TestARenderThatStopsWithoutFinishingIsNotASuccessEither(t *testing.T) {
	const stream = "event: partial\n" + `data: {"b64":"tile","usage":{"images":4}}` + "\n\n"

	d, _, render := deliverFakeRender(t, stream)

	if d.Complete {
		t.Error("a render that never reached its terminal event was reported complete")
	}
	if d.BillingStatus != http.StatusInternalServerError {
		t.Errorf("BillingStatus = %d, want 500", d.BillingStatus)
	}
	if d.ClientStatus != http.StatusOK {
		t.Errorf("ClientStatus = %d, want 200", d.ClientStatus)
	}
	// The caller was shown the one frame that did arrive. That is what makes
	// this an unfinished success rather than a failure the chain could retry:
	// something reached them, so no other candidate can serve this request.
	if render.framesSeen != 1 {
		t.Errorf("forwarded %d frames, want 1", render.framesSeen)
	}
}

// TestARenderFrameBiggerThanAScannerDefaultStillArrives is why the line cap is
// a delivery budget rather than a constant in the modality.
//
// A render embeds its image in the frame as base64. One frame is megabytes; the
// Scanner's own default is 64 KiB, and past it Scan does not truncate — it
// stops with ErrTooLong. A modality that took the default would report a
// truncated 200 for a stream whose first frame simply never got through, and
// the failure would look like the provider's.
func TestARenderFrameBiggerThanAScannerDefaultStillArrives(t *testing.T) {
	big := strings.Repeat("A", 200*1024) // comfortably past bufio's 64 KiB default
	stream := "event: completed\n" + `data: {"b64":"` + big + `","usage":{"images":1}}` + "\n\n"

	d, w, _ := deliverFakeRender(t, stream)

	if !d.Complete {
		t.Fatalf("delivery = %+v, want a complete one: the frame was inside the delivery's own "+
			"budget and only outside the Scanner's default", d)
	}
	if !strings.Contains(w.Body.String(), big) {
		t.Error("the large frame did not reach the caller intact")
	}
	if d.Usage == nil || d.Usage.Completion != 1 {
		t.Errorf("usage = %+v, want the count the terminal frame carried", d.Usage)
	}
}

// TestACallerWhoLeavesMidRenderIsNotBlamedOnTheProvider pins the blame split.
//
// Both endings look the same from inside the write: bytes stopped moving. Only
// one of them is a provider problem, and recording the other against the
// provider counts towards taking a working one out of rotation.
func TestACallerWhoLeavesMidRenderIsNotBlamedOnTheProvider(t *testing.T) {
	payload := &fakeRenderPayload{}

	// A body whose second read fails, with the caller already gone by then. The
	// request has to be reachable from inside the read, which is why the scaffold
	// hands it over rather than keeping it.
	body := &goneAfterFirstFrame{
		frames: []string{"event: partial\n" + `data: {"b64":"tile"}` + "\n\n"},
	}
	tools, release, _ := fakeDelivery{
		path: "/v1/images/generations", requestID: "req-render-gone", isStream: true,
		onContext: func(c *gin.Context) {
			body.onRead = func() { c.Request = c.Request.WithContext(cancelledContext()) }
		},
	}.tools(t)
	defer release()
	d := payload.Deliver(tools, &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": {"text/event-stream"}},
		Body:       body,
	})

	if d.Fault != fact.FaultClient {
		t.Errorf("fault = %v, want the caller's: they left, and the provider was still working", d.Fault)
	}
	if d.BillingStatus != 499 {
		t.Errorf("BillingStatus = %d, want 499", d.BillingStatus)
	}
}

// goneAfterFirstFrame yields its frames and then fails, running onRead once the
// frames are exhausted so the failure and the caller's departure coincide the
// way they do on a real connection.
type goneAfterFirstFrame struct {
	frames []string
	onRead func()
	sent   int
}

func (b *goneAfterFirstFrame) Read(p []byte) (int, error) {
	if b.sent < len(b.frames) {
		n := copy(p, b.frames[b.sent])
		b.sent++
		return n, nil
	}
	if b.onRead != nil {
		b.onRead()
		b.onRead = nil
	}
	return 0, errors.New("read tcp: connection reset by peer")
}

func (b *goneAfterFirstFrame) Close() error { return nil }

func cancelledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	return ctx
}
