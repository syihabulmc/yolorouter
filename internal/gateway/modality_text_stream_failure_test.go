package gateway

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/yolorouter/yolorouter/internal/fact"
	"github.com/yolorouter/yolorouter/internal/protocols"
)

// stubClient is a ClientResponse a test can put into any state.
//
// The delivery paths worth the most attention are the ones that only exist when
// something goes wrong, and they turn entirely on what the caller's connection
// is doing: committed or not, still there or gone, accepting writes or not.
// Reaching those through a real socket means arranging a failure at exactly the
// right instant; setting a field does not.
//
// It mirrors the real object's semantics rather than simplifying them, because
// a stub that accepts what the real one refuses turns every test using it into
// a test of the stub. In particular a frame counts as received only once it has
// been flushed: a write that is never flushed reached net/http's buffer and
// nobody else.
type stubClient struct {
	committed bool
	status    int
	gone      bool
	flushErr  error
	// pending holds what was written, received what was flushed. The split is
	// the property most of these tests turn on.
	pending  bytes.Buffer
	received bytes.Buffer
	// writes counts attempts, which is the only way to see a frame that was
	// tried and never landed. Anything derived from what arrived cannot tell
	// "never sent" from "sent and lost".
	writes int
}

func (s *stubClient) Inject(http.Header) {}

func (s *stubClient) Rollback() error { return nil }

func (s *stubClient) Commit(status int) error {
	if status <= 0 {
		return fmt.Errorf("stub: cannot commit a response with status %d", status)
	}
	if s.committed {
		return fmt.Errorf("%w: %w", errClientCommitRefused, errAlreadyCommitted)
	}
	s.committed, s.status = true, status
	return nil
}

func (s *stubClient) Committed() bool { return s.committed }

func (s *stubClient) CommittedStatus() int {
	if !s.committed {
		return 0
	}
	return s.status
}

// CallerDone answers the same question as CallerGone, which is what the real
// one does too — a caller whose context is done is a caller who went. Never nil:
// a nil channel blocks forever rather than reporting "not done", so a select on
// it would behave differently here than in production.
func (s *stubClient) CallerDone() <-chan struct{} {
	ch := make(chan struct{})
	if s.gone {
		close(ch)
	}
	return ch
}

func (s *stubClient) CallerGone() bool { return s.gone }

func (s *stubClient) Write(p []byte) (int, error) {
	if !s.committed {
		return 0, errors.New("stub: write before the response was committed")
	}
	s.writes++
	return s.pending.Write(p)
}

func (s *stubClient) Flush() error {
	if !s.committed {
		return errors.New("stub: flush before the response was committed")
	}
	if s.flushErr != nil {
		// pending is deliberately left standing, exactly as the real one leaves
		// it: bytes that were written and not confirmed have not gone anywhere,
		// and discarding them here would make the stub forgive a case the
		// product does not.
		return fmt.Errorf("%w: flush to client: %w", protocols.ErrClientWrite, s.flushErr)
	}
	s.received.Write(s.pending.Bytes())
	s.pending.Reset()
	return nil
}

func (s *stubClient) WatchCaller(io.Closer) func() { return func() {} }

// streamingPayload is a text payload at the point a delivery reports back.
func streamingPayload(t *testing.T, ingress protocols.ProtocolID, caller string) *textPayload {
	t.Helper()
	payload, rej := NewTextModality().Admit(t.Context(), Ingress{
		Protocol: ingress, Path: "/v1/chat/completions", Body: []byte(caller),
	})
	if rej != nil {
		t.Fatalf("Admit refused a valid body: %+v", rej)
	}
	if _, err := payload.PrepareUpstream(Candidate{
		ProviderModelName: "provider-model", EgressProtocol: ingress, Passthrough: true,
	}); err != nil {
		t.Fatalf("PrepareUpstream = %v", err)
	}
	return payload.(*textPayload)
}

const streamCallerBody = `{"model":"gpt-4o","stream":true,"messages":[{"role":"user","content":"hi"}]}`

func settledStream(t *testing.T, client *stubClient, err error) fact.Delivery {
	t.Helper()
	p := streamingPayload(t, protocols.ProtocolOpenAI, streamCallerBody)
	tools := DeliveryTools{Client: client, RequestID: "req-under-test"}
	return p.settleStream(tools, &http.Response{StatusCode: http.StatusOK}, nil, err)
}

// TestAFailedWriteMidStreamIsBlamedOnTheCallerNotTheProvider pins the
// distinction a single "the stream broke" branch cannot make.
//
// The provider did everything asked of it; the bytes died on the way to a
// caller who stopped reading. Filing that as an upstream fault puts an outage
// on a record that had no part in it, and settles at 200 a response the caller
// never finished receiving.
func TestAFailedWriteMidStreamIsBlamedOnTheCallerNotTheProvider(t *testing.T) {
	client := &stubClient{committed: true, status: http.StatusOK}
	writeFailed := protocols.ErrClientWrite

	got := settledStream(t, client, writeFailed)

	if got.Fault != fact.FaultClient {
		t.Errorf("fault = %v, want client — the provider had no part in a write that died on our side", got.Fault)
	}
	if got.BillingStatus != 499 {
		t.Errorf("billing status = %d, want 499 — the bytes never landed", got.BillingStatus)
	}
	if got.ClientStatus != http.StatusOK {
		t.Errorf("client status = %d, want 200 — that is what they were served before it broke", got.ClientStatus)
	}
	if got.FailReason != "client_write_timeout" {
		t.Errorf("fail reason = %q, want client_write_timeout", got.FailReason)
	}
}

// TestACallerWhoLeavesMidStreamIsNotAProviderFailure is the second way the same
// branch goes wrong: the write never failed, the caller simply went.
func TestACallerWhoLeavesMidStreamIsNotAProviderFailure(t *testing.T) {
	client := &stubClient{committed: true, status: http.StatusOK, gone: true}

	got := settledStream(t, client, errors.New("read upstream stream: connection reset"))

	if got.Fault != fact.FaultClient {
		t.Errorf("fault = %v, want client", got.Fault)
	}
	if got.BillingStatus != 499 {
		t.Errorf("billing status = %d, want 499", got.BillingStatus)
	}
	if got.FailReason != "client_disconnected" {
		t.Errorf("fail reason = %q, want client_disconnected", got.FailReason)
	}
	if client.received.Len() != 0 {
		t.Errorf("wrote %q to a caller who had already left", client.received.String())
	}
}

// TestABrokenUpstreamMidStreamClosesTheCallerSStreamProperly pins the frame a
// caller gets instead of a connection that simply stops.
//
// They cannot be given a different status now, and an SDK reading a stream that
// ends with no terminal event has no way to tell a finished response from an
// abandoned one. The request id travels in it so what they quote when they
// report the problem is the string the logs are indexed by.
func TestABrokenUpstreamMidStreamClosesTheCallerSStreamProperly(t *testing.T) {
	client := &stubClient{committed: true, status: http.StatusOK}

	got := settledStream(t, client, errors.New("read upstream stream: unexpected EOF"))

	if got.Fault != fact.FaultUpstream {
		t.Errorf("fault = %v, want upstream", got.Fault)
	}
	if !strings.HasPrefix(got.FailReason, "stream_partial") {
		t.Errorf("fail reason = %q, want a stream_partial…", got.FailReason)
	}
	frame := client.received.String()
	if !strings.Contains(frame, "upstream_error") {
		t.Errorf("caller received %q, want a closing error frame", frame)
	}
	if !strings.Contains(frame, "req-under-test") {
		t.Errorf("closing frame %q does not name the request; the caller has nothing to quote", frame)
	}
	if !strings.Contains(frame, "[DONE]") {
		t.Errorf("closing frame %q has no terminator; an OpenAI SDK blocks until its own read timeout", frame)
	}
}

// TestARefusedCommitDoesNotSendAnotherProviderAtTheSameCaller pins the branch
// that has to exist before the writer that triggers it does.
//
// Nothing of ours reached the caller, which is also what a dead upstream looks
// like from here — and that reading offers the chain another provider. It must
// not: the caller already holds somebody's response, so a second attempt could
// only spend a provider call to discover that.
func TestARefusedCommitDoesNotSendAnotherProviderAtTheSameCaller(t *testing.T) {
	client := &stubClient{committed: true, status: http.StatusTeapot}
	refused := errors.New("wrapped: " + errClientCommitRefused.Error())

	got := settledStream(t, client, errors.Join(errClientCommitRefused, refused))

	if got.Verdict != fact.VerdictSettled {
		t.Errorf("verdict = %v, want settled", got.Verdict)
	}
	if got.Fault != fact.FaultGateway {
		t.Errorf("fault = %v, want gateway — two writers on one response is ours", got.Fault)
	}
	if got.ClientStatus != http.StatusTeapot {
		t.Errorf("client status = %d, want the status actually on the wire (%d)", got.ClientStatus, http.StatusTeapot)
	}
	if got.FailReason != "client_commit_refused" {
		t.Errorf("fail reason = %q, want client_commit_refused", got.FailReason)
	}
}

// TestACallerWhoLeftBeforeAnyByteIsRecordedAsHavingReceivedNothing is the other
// half of the disconnect split.
//
// The same sentinel arrives whether the caller left before the first frame or
// long after, because the pump knows they went and not what they had by then.
// Recording both as a partial delivery would claim a status this one never saw.
func TestACallerWhoLeftBeforeAnyByteIsRecordedAsHavingReceivedNothing(t *testing.T) {
	client := &stubClient{}

	got := settledStream(t, client, errClientDisconnected)

	if got.Committed {
		t.Error("delivery reports committed; nothing had gone out when the caller left")
	}
	if got.ClientStatus != 0 {
		t.Errorf("client status = %d, want 0 — they were never served one", got.ClientStatus)
	}
	if got.BillingStatus != 499 {
		t.Errorf("billing status = %d, want 499", got.BillingStatus)
	}
	if got.Verdict != fact.VerdictSettled {
		t.Errorf("verdict = %v, want settled — a caller who left cannot be served by another provider", got.Verdict)
	}
}

// TestACallerWhoLeftPartWayThroughKeepsTheStatusTheyWereServed is the
// committed half of the disconnect split, and the reason it is a split.
//
// This caller did get a status and some of an answer, so the record has to show
// both: the 200 they were served, and the 499 that says they never received the
// rest. One number cannot answer both, and the branch that tries picks whichever
// question it was written for.
func TestACallerWhoLeftPartWayThroughKeepsTheStatusTheyWereServed(t *testing.T) {
	client := &stubClient{committed: true, status: http.StatusOK}

	got := settledStream(t, client, errClientDisconnected)

	if !got.Committed {
		t.Error("delivery reports nothing committed; this caller had a status and bytes")
	}
	if got.ClientStatus != http.StatusOK {
		t.Errorf("client status = %d, want 200 — that is what went out before they left", got.ClientStatus)
	}
	if got.BillingStatus != 499 {
		t.Errorf("billing status = %d, want 499 — they never received the rest", got.BillingStatus)
	}
	if got.Complete {
		t.Error("delivery reports complete; the caller left before the stream ended")
	}
}

// TestAClosingFrameThatNeverLandsDoesNotDragTheTerminatorAfterIt pins what the
// short-circuit in the OpenAI closing sequence is for.
//
// That sequence is two frames: the error, then the terminator an SDK waits for.
// If the first one cannot reach the caller, neither can the second — the
// connection is what failed, not the frame — and writing on regardless records
// a caller as having been told something they never received.
func TestAClosingFrameThatNeverLandsDoesNotDragTheTerminatorAfterIt(t *testing.T) {
	client := &stubClient{
		committed: true, status: http.StatusOK,
		flushErr: errors.New("broken pipe"),
	}

	got := settledStream(t, client, errors.New("read upstream stream: unexpected EOF"))

	if client.received.Len() != 0 {
		t.Errorf("recorded %q as received; the flush that would have sent it failed", client.received.String())
	}
	if client.writes != 1 {
		t.Errorf("%d frames were attempted, want 1 — the terminator followed a frame that never reached the caller", client.writes)
	}
	// The upstream is still what broke first, and a caller who then went away
	// does not turn the provider's failure into their own.
	if got.Fault != fact.FaultUpstream {
		t.Errorf("fault = %v, want upstream", got.Fault)
	}
}

// TestAFrameOnlyCountsAsReceivedOnceItHasBeenFlushed pins the property the
// closing sequence is written around, on the real object that decides it.
//
// net/http can take a small write into its own buffer and report success, so a
// frame counted at the write is one the audit trail claims was delivered while
// it sat in memory. Asserted against the capture file rather than a recorder:
// the recorder takes bytes immediately, so it cannot tell the two moments
// apart, and the file is where a stream's account actually lands.
func TestAFrameOnlyCountsAsReceivedOnceItHasBeenFlushed(t *testing.T) {
	dir := t.TempDir()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Set(BodiesDirContextKey, dir)
	rc := &Exchange{requestID: "flush-decides"}
	openStreamBodyFile(c, rc)
	defer closeStreamBodyFile(rc)

	client := (&Service{}).streamResponse(c, rc)
	if err := client.Commit(http.StatusOK); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := client.Write([]byte("data: one\n\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if got, _ := readCapture(t, dir, rc.requestID); got != "" {
		t.Fatalf("a written-but-unflushed frame is already in the record: %q", got)
	}

	if err := client.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	if got, _ := readCapture(t, dir, rc.requestID); got != "data: one\n\n" {
		t.Errorf("record holds %q after the flush, want the frame", got)
	}
}

// TestATranslatedStreamNamesADoubleCommitForWhatItIs pins the one thing two
// representations of the same refusal cost.
//
// A relay wraps whatever Commit returns in its own client-write error, so a
// refusal that did not say what it was would reach settlement looking like a
// caller who stopped reading — and a double-commit of ours would be filed as
// theirs, at 499. The sentinel travels inside the refusal so both the pumps and
// the relays arrive at the same answer.
func TestATranslatedStreamNamesADoubleCommitForWhatItIs(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rc := &Exchange{requestID: "double-commit"}

	client := (&Service{}).streamResponse(c, rc)
	if err := client.Commit(http.StatusOK); err != nil {
		t.Fatalf("first commit: %v", err)
	}
	refusal := client.Commit(http.StatusOK)
	if refusal == nil {
		t.Fatal("a second commit was accepted; the response was already on the wire")
	}
	// Shaped the way a relay hands it on.
	relayed := fmt.Errorf("%w: commit stream to client: %w", protocols.ErrClientWrite, refusal)

	got := settledStream(t, &stubClient{committed: true, status: http.StatusOK}, relayed)

	if got.FailReason != "client_commit_refused" {
		t.Errorf("fail reason = %q, want client_commit_refused — this is ours, not a caller who left", got.FailReason)
	}
	if got.Fault != fact.FaultGateway {
		t.Errorf("fault = %v, want gateway", got.Fault)
	}
	if got.BillingStatus != http.StatusInternalServerError {
		t.Errorf("billing status = %d, want 500", got.BillingStatus)
	}
}
