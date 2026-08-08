package gateway

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/yolorouter/yolorouter/internal/protocols"
)

func newClientResponse(t *testing.T) (*ginClientResponse, *httptest.ResponseRecorder) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	return &ginClientResponse{c: c, rc: &Exchange{requestID: "req-tools"}}, w
}

// TestCommitPutsTheStatusOnTheWire is the floor, and it is the assertion the
// first version of this type failed.
//
// gin's WriteHeader only REMEMBERS a status: nothing is sent, Written() stays
// false, and a second WriteHeader silently replaces the first. A Commit built
// on it left every guarantee downstream of "is this response committed" false
// at once -- Committed() denied a commit that had happened, and Rollback
// happily undid it.
func TestCommitPutsTheStatusOnTheWire(t *testing.T) {
	r, w := newClientResponse(t)

	if r.Committed() {
		t.Fatal("Committed() = true before anything happened")
	}
	if err := r.Commit(201); err != nil {
		t.Fatalf("Commit(201) = %v", err)
	}
	if !r.Committed() {
		t.Error("Committed() = false right after a successful Commit")
	}
	if w.Code != 201 {
		t.Errorf("the caller received status %d, want 201: the status never left the buffer", w.Code)
	}
	if !r.rc.firstByteSent {
		t.Error("firstByteSent = false after the status went out; the audit row would call this undelivered")
	}
}

// TestCommitIsIrreversible covers the three ways the old split let a committed
// response be taken back or quietly rewritten.
func TestCommitIsIrreversible(t *testing.T) {
	r, w := newClientResponse(t)
	if err := r.Commit(200); err != nil {
		t.Fatalf("Commit(200) = %v", err)
	}

	if err := r.Commit(500); err == nil {
		t.Error("a second Commit succeeded; the caller already has a 200")
	}
	if w.Code != 200 {
		t.Errorf("status is now %d, want 200: the second Commit rewrote what the caller was served", w.Code)
	}
	if err := r.Rollback(); err == nil {
		t.Error("Rollback succeeded after the response was committed")
	}
}

func TestCommitRefusesAStatusOfZero(t *testing.T) {
	r, _ := newClientResponse(t)
	if err := r.Commit(0); err == nil {
		t.Error("Commit(0) succeeded; gin treats it as a no-op and the response would hang uncommitted")
	}
}

// TestStagedHeadersDoNotTouchTheLiveResponse pins why staging exists at all.
// The modality's headers must be invisible until it commits, or a rollback has
// nothing to roll back to.
func TestStagedHeadersDoNotTouchTheLiveResponse(t *testing.T) {
	r, _ := newClientResponse(t)
	r.Inject(http.Header{"X-Compat": {"staged"}})

	if got := r.c.Writer.Header().Get("X-Compat"); got != "" {
		t.Fatalf("live header already carries %q before Commit", got)
	}
	if err := r.Commit(200); err != nil {
		t.Fatalf("Commit = %v", err)
	}
	if got := r.c.Writer.Header().Get("X-Compat"); got != "staged" {
		t.Errorf("X-Compat = %q after Commit, want %q", got, "staged")
	}
}

// TestRollbackLeavesTheKernelsOwnHeadersAlone is the case that made staging
// necessary. Writing straight to the live header overwrote whatever the kernel
// had set under the same name, and a rollback that deleted by name then took
// the kernel's value with it -- removing a header the modality never owned.
func TestRollbackLeavesTheKernelsOwnHeadersAlone(t *testing.T) {
	r, _ := newClientResponse(t)
	r.c.Writer.Header().Set("Content-Type", "application/json")

	r.Inject(http.Header{"Content-Type": {"audio/mpeg"}})
	if err := r.Rollback(); err != nil {
		t.Fatalf("Rollback = %v", err)
	}
	if err := r.Commit(200); err != nil {
		t.Fatalf("Commit = %v", err)
	}

	if got := r.c.Writer.Header().Get("Content-Type"); got != "application/json" {
		t.Errorf("Content-Type = %q, want %q: the rollback took out a header the kernel set", got, "application/json")
	}
}

// TestWriteBeforeCommitIsRefused stops the framework from committing a status
// nobody chose. gin turns the first Write into an implicit 200, at a moment
// nothing recorded and with no chance to inject the headers meant to go with it.
func TestWriteBeforeCommitIsRefused(t *testing.T) {
	r, w := newClientResponse(t)

	if _, err := r.Write([]byte("hello")); err == nil {
		t.Error("Write succeeded before Commit")
	}
	if err := r.Flush(); err == nil {
		t.Error("Flush succeeded before Commit")
	}
	if w.Body.Len() != 0 {
		t.Errorf("the caller received %q, want nothing", w.Body.String())
	}
}

func TestWriteAfterCommitReachesTheCaller(t *testing.T) {
	r, w := newClientResponse(t)
	if err := r.Commit(200); err != nil {
		t.Fatalf("Commit = %v", err)
	}
	if _, err := r.Write([]byte("hello")); err != nil {
		t.Fatalf("Write = %v", err)
	}
	if w.Body.String() != "hello" {
		t.Errorf("body = %q, want %q", w.Body.String(), "hello")
	}
}

// TestUpstreamCaptureStopsAtItsLimit pins the cap. Without one, a progressive
// response appends into the heap for as long as the provider keeps sending --
// which is the unbounded growth the file-backed stream capture exists to avoid.
func TestUpstreamCaptureStopsAtItsLimit(t *testing.T) {
	rc := &Exchange{}
	cap4 := &exchangeCapture{rc: rc, limit: 4}

	if kept := cap4.Upstream([]byte("ab")); !kept {
		t.Error("Upstream reported a drop while there was still room")
	}
	if kept := cap4.Upstream([]byte("cdef")); kept {
		t.Error("Upstream reported everything kept while over the limit")
	}
	if got := string(rc.UpstreamResponseBody()); got != "abcd" {
		t.Errorf("captured %q, want %q: the capture ran past its limit", got, "abcd")
	}
	if kept := cap4.Upstream([]byte("gh")); kept {
		t.Error("Upstream kept bytes after the limit was already reached")
	}
	if got := len(rc.UpstreamResponseBody()); got != 4 {
		t.Errorf("captured %d bytes, want 4", got)
	}
}

// TestFetchWithoutASafeTransportRefuses pins fail-closed. A download whose
// destination a third party chose is exactly the one that must not fall back to
// net/http's default client, which resolves any address including this
// network's own.
func TestFetchWithoutASafeTransportRefuses(t *testing.T) {
	f := &safeFetcher{client: nil, limit: 1024}
	_, err := f.Fetch(context.Background(), "https://example.com/x", FetchPolicy{AllowedHosts: []string{"example.com"}})
	if err == nil {
		t.Fatal("Fetch succeeded with no safe transport")
	}
}

// refusingTransport answers every request with an error. The policy checks
// under test all run BEFORE any dial, so a transport that dials nothing is
// enough -- and using a client with no transport at all would trip the
// fail-closed guard first and leave these assertions untested.
type refusingTransport struct{}

func (refusingTransport) RoundTrip(*http.Request) (*http.Response, error) {
	return nil, errors.New("no network in tests")
}

func fetcherWithTransport(limit int64) *safeFetcher {
	return &safeFetcher{client: &http.Client{Transport: refusingTransport{}}, limit: limit}
}

func TestFetchRefusesWhatThePolicyDidNotName(t *testing.T) {
	f := fetcherWithTransport(1024)
	cases := []struct {
		name, url string
		hosts     []string
	}{
		{"empty allowlist permits nothing", "https://example.com/x", nil},
		{"host not on the list", "https://evil.example/x", []string{"example.com"}},
		{"plain http", "http://example.com/x", []string{"example.com"}},
		{"a port the list did not name", "https://example.com:8443/x", []string{"example.com"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := f.Fetch(context.Background(), tc.url, FetchPolicy{AllowedHosts: tc.hosts})
			if err == nil {
				t.Fatalf("Fetch(%q) succeeded", tc.url)
			}
			if strings.Contains(err.Error(), "evil.example") || strings.Contains(err.Error(), tc.url) {
				t.Errorf("the error repeats the url: %v", err)
			}
		})
	}
}

// TestTheAssembledEnvIsUsable checks what the pieces add up to: a modality
// asking for a smaller budget gets it, and the fetcher it is handed has a
// transport of its own.
//
// The fetcher's transport is the assertion that matters. Sharing the upstream
// one would quietly extend "this operator allows private addresses, for the
// providers they configured" to a URL some provider picked -- and a nil
// transport would fall through to net/http's default, which allows everything.
func TestTheAssembledEnvIsUsable(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	svc := &Service{}
	rc := &Exchange{requestID: "req-env"}

	tools, release := svc.newDeliveryTools(c, rc, TransferLimits{MaxResponseBytes: 64}, false)
	defer release()

	if tools.Limits.MaxResponseBytes != 64 {
		t.Errorf("MaxResponseBytes = %d, want the modality's 64", tools.Limits.MaxResponseBytes)
	}
	if tools.Limits.MaxFrameBytes != maxStreamLineBytes {
		t.Errorf("MaxFrameBytes = %d, want the kernel's %d", tools.Limits.MaxFrameBytes, maxStreamLineBytes)
	}
	f, ok := tools.Fetch.(*safeFetcher)
	if !ok {
		t.Fatalf("Fetch is %T, want *safeFetcher", tools.Fetch)
	}
	if f.client == nil || f.client.Transport == nil {
		t.Fatal("the fetcher has no transport of its own, so it would fall back to net/http's default")
	}
	if f.client.Timeout != secondaryFetchTimeout {
		t.Errorf("fetch timeout = %v, want %v", f.client.Timeout, secondaryFetchTimeout)
	}
	if f.limit != 64 {
		t.Errorf("fetch limit = %d, want it narrowed to the delivery's 64", f.limit)
	}
	// The capture must be bounded by the same resolved number, or the modality
	// reads one limit while the buffer enforces another.
	if cap, ok := tools.Capture.(*exchangeCapture); !ok || cap.limit != 64 {
		t.Errorf("capture limit = %+v, want 64", tools.Capture)
	}
}

// TestLimitsOnlyNarrow pins that a modality's declaration cannot widen a
// kernel budget. A modality that could would be voting itself an unbounded
// buffer, which is the whole reason the kernel keeps a number of its own.
func TestLimitsOnlyNarrow(t *testing.T) {
	kernel := TransferLimits{
		MaxResponseBytes: 1000, MaxFrameBytes: 100,
		WriteWindow: time.Second, TotalBudget: time.Minute,
	}

	wider := kernel.resolveAgainst(TransferLimits{
		MaxResponseBytes: 1 << 40, MaxFrameBytes: 1 << 20,
		WriteWindow: time.Hour, TotalBudget: time.Hour,
	})
	if wider != kernel {
		t.Errorf("limits widened to %+v, want the kernel's %+v", wider, kernel)
	}

	narrower := kernel.resolveAgainst(TransferLimits{MaxResponseBytes: 10, TotalBudget: time.Second})
	if narrower.MaxResponseBytes != 10 || narrower.TotalBudget != time.Second {
		t.Errorf("limits = %+v, want the modality's smaller values honoured", narrower)
	}
	if narrower.MaxFrameBytes != 100 || narrower.WriteWindow != time.Second {
		t.Errorf("limits = %+v, want the unstated fields left at the kernel's values", narrower)
	}
}

// deadlineWriter records the write deadlines something sets on it, which is
// the only way to tell a deadline that was applied from one that was merely
// computed.
type deadlineWriter struct {
	gin.ResponseWriter
	deadlines []time.Time
}

func (d *deadlineWriter) SetWriteDeadline(t time.Time) error {
	d.deadlines = append(d.deadlines, t)
	return nil
}

// TestTheResolvedWriteWindowIsTheOneApplied pins the difference between a
// limit and a limit that does something.
//
// The first version resolved the modality's window into tools.Limits, read it
// back as a boolean, and then applied a package-global value to the socket. A
// modality asking for 5ms got the global window on the wire while every
// assertion about the struct passed.
func TestTheResolvedWriteWindowIsTheOneApplied(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	dw := &deadlineWriter{ResponseWriter: c.Writer}
	c.Writer = dw

	const window = 5 * time.Millisecond
	r := &ginClientResponse{c: c, rc: &Exchange{requestID: "req-deadline"}, window: window, limit: 1 << 20}

	before := time.Now()
	if err := r.Commit(200); err != nil {
		t.Fatalf("Commit = %v", err)
	}
	after := time.Now()

	if len(dw.deadlines) == 0 {
		t.Fatal("no write deadline was applied")
	}
	got := dw.deadlines[0]
	if got.Before(before.Add(window)) || got.After(after.Add(window)) {
		t.Errorf("deadline = %v, want about %v after now: the resolved window was not the one applied",
			got.Sub(before), window)
	}
	if global := protocols.StreamWriteWindow(); got.Sub(before) >= global {
		t.Errorf("deadline is %v out, which is the global window (%v), not this delivery's %v",
			got.Sub(before), global, window)
	}
}

// TestEachAttemptCapturesOnItsOwn pins that one attempt's upstream bytes never
// join another's.
//
// The audit column holds one body and a request may make several attempts, so
// a capture that appended would leave two providers' responses concatenated
// under one heading -- and, because room was measured against the shared
// field, would also spend the second attempt's budget on the first attempt's
// bytes.
func TestEachAttemptCapturesOnItsOwn(t *testing.T) {
	svc := &Service{}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	rc := &Exchange{requestID: "req-capture"}

	first, releaseFirst := svc.newDeliveryTools(c, rc, TransferLimits{MaxResponseBytes: 8}, false)
	defer releaseFirst()
	if kept := first.Capture.Upstream([]byte("AAAAAAAA")); !kept {
		t.Fatal("the first attempt could not capture its own body")
	}

	second, releaseSecond := svc.newDeliveryTools(c, rc, TransferLimits{MaxResponseBytes: 8}, false)
	defer releaseSecond()
	if kept := second.Capture.Upstream([]byte("BB")); !kept {
		t.Error("the second attempt was refused room the first attempt had used")
	}
	if got := string(rc.UpstreamResponseBody()); got != "BB" {
		t.Errorf("upstream body = %q, want %q: this row describes the attempt that ended the request", got, "BB")
	}
	if second.Capture.Truncated() {
		t.Error("the second attempt reports truncation, but nothing of its own was dropped")
	}
}

func TestCaptureReportsWhatItDropped(t *testing.T) {
	rc := &Exchange{}
	cap := &exchangeCapture{rc: rc, limit: 2}
	if kept := cap.Upstream([]byte("abcd")); kept {
		t.Error("Upstream reported everything kept while over the limit")
	}
	if !cap.Truncated() {
		t.Error("Truncated() = false after bytes were dropped: a cut-off body is indistinguishable from a short one")
	}
	if got := string(rc.UpstreamResponseBody()); got != "ab" {
		t.Errorf("captured %q, want %q", got, "ab")
	}
}

// TestClientBytesAreRecordedOnlyOnceFlushed pins where the caller-facing body
// comes from. A buffered Write returns nil without the caller having received
// anything, so recording at Write would file bytes that never landed.
func TestClientBytesAreRecordedOnlyOnceFlushed(t *testing.T) {
	r, _ := newClientResponse(t)
	r.limit = 1 << 20
	if err := r.Commit(200); err != nil {
		t.Fatalf("Commit = %v", err)
	}
	if _, err := r.Write([]byte("hello")); err != nil {
		t.Fatalf("Write = %v", err)
	}
	if got := string(r.rc.ResponseBody()); got != "" {
		t.Errorf("responseBody = %q before any flush, want empty", got)
	}
	if err := r.Flush(); err != nil {
		t.Fatalf("Flush = %v", err)
	}
	if got := string(r.rc.ResponseBody()); got != "hello" {
		t.Errorf("responseBody = %q after the flush, want %q", got, "hello")
	}
}

// TestTheSecondaryFetchClientIsSharedPinsTheLeak. A transport per delivery
// pools connections nothing ever reuses and nothing ever closes, so every
// fetching request leaves its own idle conns and their goroutines behind.
func TestTheSecondaryFetchClientIsShared(t *testing.T) {
	svc := &Service{}
	if a, b := svc.secondaryFetchClient(), svc.secondaryFetchClient(); a != b {
		t.Error("a second call built another client; each delivery would pool its own connections")
	}
}

func TestFetchRefusesAClientWithNoTransport(t *testing.T) {
	// The guard that matters is the transport, not the client: a client with a
	// nil transport is the one that silently falls back to net/http's default.
	f := &safeFetcher{client: &http.Client{}, limit: 1024}
	_, err := f.Fetch(context.Background(), "https://example.com/x",
		FetchPolicy{AllowedHosts: []string{"example.com"}})
	if err == nil {
		t.Fatal("Fetch succeeded on a client with no transport")
	}
	// The MESSAGE is the assertion, not merely that something failed. Without
	// the guard this call falls through to net/http's default transport and
	// makes a real request -- which also errors, in a sandbox, and would leave
	// this test green while the protection was gone.
	if !strings.Contains(err.Error(), "no safe transport") {
		t.Errorf("Fetch = %v, want it refused before any request was attempted", err)
	}
}

// TestAllowlistAcceptsBothSpellingsOfTheDefaultPort covers both directions of
// the same equivalence: a url and an allowlist entry may each spell the default
// port or leave it out, and they mean the same destination either way. Matching
// only literal strings would let an allowlist that looks right deny everything.
func TestAllowlistAcceptsBothSpellingsOfTheDefaultPort(t *testing.T) {
	f := fetcherWithTransport(1024)
	cases := []struct{ url, entry string }{
		{"https://example.com/x", "example.com"},
		{"https://example.com/x", "example.com:443"},
		{"https://example.com:443/x", "example.com"},
		{"https://example.com:443/x", "example.com:443"},
		{"https://EXAMPLE.com/x", "example.com"},
	}
	for _, tc := range cases {
		_, err := f.Fetch(context.Background(), tc.url, FetchPolicy{AllowedHosts: []string{tc.entry}})
		if err != nil && strings.Contains(err.Error(), "host is not allowed") {
			t.Errorf("url %q with allowlist entry %q was denied; they name the same destination", tc.url, tc.entry)
		}
	}
}

// TestAZeroKernelLimitIsNoLimit covers the direction the narrowing rule got
// backwards. Zero on the kernel's side means unbounded, so any finite number a
// modality asks for IS narrower and has to win -- reading zero as "smallest"
// let an unset kernel budget override every modality that had asked for one.
func TestAZeroKernelLimitIsNoLimit(t *testing.T) {
	unset := TransferLimits{}
	got := unset.resolveAgainst(TransferLimits{
		MaxResponseBytes: 100, MaxFrameBytes: 10,
		WriteWindow: time.Second, TotalBudget: time.Minute,
	})
	if got.MaxResponseBytes != 100 || got.MaxFrameBytes != 10 ||
		got.WriteWindow != time.Second || got.TotalBudget != time.Minute {
		t.Errorf("limits = %+v, want the modality's numbers: an unset kernel limit is no limit at all", got)
	}
}

// TestCommittedStatusReportsWhatTheWireHas is the assertion the reconciliation
// rests on. A response committed without going through Commit -- which is what
// every writer outside this type does, including all of dispatch today -- must
// not report "nothing was served".
func TestCommittedStatusReportsWhatTheWireHas(t *testing.T) {
	r, _ := newClientResponse(t)
	if got := r.CommittedStatus(); got != 0 {
		t.Fatalf("CommittedStatus() = %d before anything was written, want 0", got)
	}

	// Bypass the sink entirely, the way the error writers still do.
	r.c.Writer.WriteHeader(503)
	r.c.Writer.WriteHeaderNow()

	if !r.Committed() {
		t.Fatal("Committed() = false after a bare write")
	}
	if got := r.CommittedStatus(); got != 503 {
		t.Errorf("CommittedStatus() = %d, want 503: the caller has a status this type never recorded", got)
	}
}

// TestAnAttemptWithNoBodyClearsThePreviousOne covers the case the first
// cross-attempt test missed. Publishing only on the first write meant an
// attempt that produced nothing left the previous provider's response standing
// under its own heading.
func TestAnAttemptWithNoBodyClearsThePreviousOne(t *testing.T) {
	svc := &Service{}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	rc := &Exchange{requestID: "req-empty-attempt"}

	first, releaseFirst := svc.newDeliveryTools(c, rc, TransferLimits{}, false)
	defer releaseFirst()
	first.Capture.Upstream([]byte("first provider said this"))

	// The second attempt never captures anything at all.
	_, releaseSecond := svc.newDeliveryTools(c, rc, TransferLimits{}, false)
	defer releaseSecond()

	if got := string(rc.UpstreamResponseBody()); got != "" {
		t.Errorf("upstream body = %q, want empty: this row describes an attempt that produced nothing", got)
	}
}

// TestCaptureDoesNotResurrectADeliberateClear pins that the publish-every-write
// rule defers to anything else that touches the field. The dispatch paths clear
// it on purpose when they commit to a candidate, and republishing the whole
// accumulated buffer over one would bring back exactly the bytes it dropped.
func TestCaptureDoesNotResurrectADeliberateClear(t *testing.T) {
	rc := &Exchange{}
	cap := newExchangeCapture(rc, 1024)
	cap.Upstream([]byte("stale error body"))

	rc.clearResponseBodies()
	cap.Upstream([]byte("fresh"))

	if got := string(rc.UpstreamResponseBody()); got != "fresh" {
		t.Errorf("upstream body = %q, want %q: the clear was undone", got, "fresh")
	}
}

func TestTruncationIsRecordedOnTheExchange(t *testing.T) {
	rc := &Exchange{}
	cap := newExchangeCapture(rc, 2)
	cap.Upstream([]byte("abcd"))
	if !rc.UpstreamBodyTruncated() {
		t.Error("UpstreamBodyTruncated() = false after the cap was hit")
	}

	r, _ := newClientResponse(t)
	r.limit = 2
	if err := r.Commit(200); err != nil {
		t.Fatalf("Commit = %v", err)
	}
	if _, err := r.Write([]byte("abcd")); err != nil {
		t.Fatalf("Write = %v", err)
	}
	if err := r.Flush(); err != nil {
		t.Fatalf("Flush = %v", err)
	}
	if !r.rc.ClientBodyTruncated() {
		t.Error("ClientBodyTruncated() = false after the client capture was cut off")
	}
}

// TestAssemblyNeverHandsOutAZeroLimit closes the gap between the resolver, for
// which zero means "no limit", and the buffers, for which it means "keep
// nothing". One struct cannot carry both readings.
func TestAssemblyNeverHandsOutAZeroLimit(t *testing.T) {
	svc := &Service{}
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	rc := &Exchange{requestID: "req-zero"}

	tools, release := svc.newDeliveryTools(c, rc, TransferLimits{}, false)
	defer release()

	if tools.Limits.MaxResponseBytes <= 0 || tools.Limits.MaxFrameBytes <= 0 {
		t.Fatalf("limits = %+v, want positive caps", tools.Limits)
	}
	if kept := tools.Capture.Upstream([]byte("x")); !kept {
		t.Error("the capture dropped a byte, so it was handed a zero limit")
	}
}

// TestBufferCapsAreAlwaysPositive tests the rule directly, because the
// assembly path cannot currently produce a zero -- the kernel's own caps are
// constants. Testing it through the assembly would assert nothing.
func TestBufferCapsAreAlwaysPositive(t *testing.T) {
	got := TransferLimits{}.withPositiveBuffers()
	if got.MaxResponseBytes != maxNonStreamResponseBytes || got.MaxFrameBytes != maxStreamLineBytes {
		t.Errorf("limits = %+v, want the kernel's caps: a zero reaches the buffers as \"keep nothing\"", got)
	}
	kept := TransferLimits{MaxResponseBytes: 5, MaxFrameBytes: 6}.withPositiveBuffers()
	if kept.MaxResponseBytes != 5 || kept.MaxFrameBytes != 6 {
		t.Errorf("limits = %+v, want the positive values left alone", kept)
	}
}

// TestCallerGoneTracksTheCallersContext pins the one question a modality
// cannot answer from an error alone.
//
// The upstream request runs on the caller's context, so when the caller hangs
// up the read from the provider fails too. The two produce the same error and
// must not produce the same record: one is a caller who left, the other is a
// provider that broke, and only the second belongs on that provider's health.
func TestCallerGoneTracksTheCallersContext(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	ctx, cancel := context.WithCancel(context.Background())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)

	r := &ginClientResponse{c: c, rc: &Exchange{requestID: "req-gone"}}

	if r.CallerGone() {
		t.Fatal("CallerGone() = true while the caller is still connected")
	}
	cancel()
	if !r.CallerGone() {
		t.Error("CallerGone() = false after the caller's context was cancelled")
	}
}

// TestAProgressiveDeliveryKeepsNoUpstreamBytes pins the choice the kernel makes
// on a modality's behalf, and it is a choice to keep nothing.
//
// The capture file promises one thing: exactly what the caller received. A
// stream's raw upstream lines are not that — they are pre-rewrite, and the
// rewrite is the reason the two differ — so writing them into that file would
// interleave two accounts of the response and leave neither readable. There is
// nowhere else sized for a stream either, which is why they are dropped rather
// than kept smaller. A second file of their own is the way to keep them, and
// that is a thing to add, not a thing this quietly does.
//
// The modality calls the same method either way and is told nothing about which
// capture it got; this is the assertion that it never needs to know.
func TestAProgressiveDeliveryKeepsNoUpstreamBytes(t *testing.T) {
	dir := t.TempDir()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Set(BodiesDirContextKey, dir)
	svc := &Service{}
	rc := &Exchange{requestID: "stream"}

	progressive, releaseProgressive := svc.newDeliveryTools(c, rc, TransferLimits{}, true)
	defer releaseProgressive()
	defer closeStreamBodyFile(rc)
	progressive.Capture.Upstream([]byte("data: raw upstream line\n\n"))
	closeStreamBodyFile(rc)

	captured, err := os.ReadFile(filepath.Join(dir, rc.requestID+".stream"))
	if err != nil {
		t.Fatalf("read capture file: %v", err)
	}
	if len(captured) != 0 {
		t.Errorf("capture file holds %q; upstream bytes in there are not what the caller received", captured)
	}
	// Nowhere else either. Asserting only on the file let a swap to the
	// in-memory capture pass: the bytes moved somewhere nothing was looking,
	// which is the same leak wearing a different hat.
	if rc.ResponseBody() != nil || rc.UpstreamResponseBody() != nil {
		t.Errorf("upstream bytes were kept on the exchange instead (response=%q upstream=%q)", rc.ResponseBody(), rc.UpstreamResponseBody())
	}

	buffered, releaseBuffered := svc.newDeliveryTools(c, &Exchange{requestID: "whole"}, TransferLimits{}, false)
	defer releaseBuffered()
	if _, ok := buffered.Capture.(*exchangeCapture); !ok {
		t.Errorf("non-progressive capture is %T, want the bounded in-memory one", buffered.Capture)
	}
}

// TestCallerDoneAndCallerGoneAnswerDifferentQuestions pins a distinction that
// looks like duplication.
//
// Done closes for our own deadline too, which is a timeout we caused. Blaming
// the caller for it would put a 499 on a request they were still waiting for,
// and spare the provider a failure that was ours.
func TestCallerDoneAndCallerGoneAnswerDifferentQuestions(t *testing.T) {
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil).WithContext(ctx)

	r := &ginClientResponse{c: c, rc: &Exchange{requestID: "deadline"}}

	select {
	case <-r.CallerDone():
	default:
		t.Fatal("CallerDone() has not closed after the deadline passed")
	}
	if r.CallerGone() {
		t.Error("CallerGone() = true after OUR deadline expired; the caller never left")
	}
}

// TestAProgressiveDeliveryRecordsWhatWasSentToTheCaptureFile pins the other
// half of the choice newCapture makes.
//
// The upstream's bytes and the caller's bytes are recorded separately, and both
// answers depend on the same fact: a stream has no size the in-memory record was
// built for. Putting a stream there would truncate it at the non-stream cap and
// flag every stream as truncated, while the file that exists for exactly this
// stayed empty. The modality writes the same way either way and is told
// nothing about where the record lands.
func TestAProgressiveDeliveryRecordsWhatWasSentToTheCaptureFile(t *testing.T) {
	dir := t.TempDir()
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	c.Set(BodiesDirContextKey, dir)
	rc := &Exchange{requestID: "progressive-sent"}
	svc := &Service{}

	tools, release := svc.newDeliveryTools(c, rc, TransferLimits{}, true)
	defer release()
	defer closeStreamBodyFile(rc)
	if err := tools.Client.Commit(http.StatusOK); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := tools.Client.Write([]byte("data: one\n\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := tools.Client.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}
	closeStreamBodyFile(rc)

	captured, err := os.ReadFile(filepath.Join(dir, rc.requestID+".stream"))
	if err != nil {
		t.Fatalf("read capture file: %v", err)
	}
	if string(captured) != "data: one\n\n" {
		t.Errorf("capture file holds %q, want the bytes the caller received", captured)
	}
	if rc.ResponseBody() != nil {
		t.Errorf("in-memory response body holds %q; a stream's bytes belong in the file, and the cap here would truncate them", rc.ResponseBody())
	}
}

// TestAWholeResponseRecordsWhatWasSentInMemory is the case above's counterpart,
// and the reason the choice cannot simply become "always the file": a
// non-stream response has no capture file, and its bytes are what the audit row
// itself carries.
func TestAWholeResponseRecordsWhatWasSentInMemory(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rc := &Exchange{requestID: "whole-sent"}
	svc := &Service{}

	tools, release := svc.newDeliveryTools(c, rc, TransferLimits{}, false)
	defer release()
	if err := tools.Client.Commit(http.StatusOK); err != nil {
		t.Fatalf("commit: %v", err)
	}
	if _, err := tools.Client.Write([]byte(`{"ok":true}`)); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := tools.Client.Flush(); err != nil {
		t.Fatalf("flush: %v", err)
	}

	if string(rc.ResponseBody()) != `{"ok":true}` {
		t.Errorf("response body holds %q, want the bytes the caller received", rc.ResponseBody())
	}
}

// TestTheToolboxCarriesTheKernelSNameForTheRequest pins the one field a
// modality cannot supply for itself.
//
// It reaches the caller in the text of a mid-stream error, so that what they
// quote when they report a problem is the string the logs are indexed by. An
// empty one still renders a perfectly well-formed frame — naming nothing.
func TestTheToolboxCarriesTheKernelSNameForTheRequest(t *testing.T) {
	rec := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(rec)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	rc := &Exchange{requestID: "req-named"}

	tools, release := (&Service{}).newDeliveryTools(c, rc, TransferLimits{}, false)
	defer release()

	if tools.RequestID != rc.requestID {
		t.Errorf("toolbox reports request id %q, want %q — a modality has no other way to name this request", tools.RequestID, rc.requestID)
	}
}
