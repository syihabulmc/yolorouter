package capture

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestOpeningTheStreamTwiceKeepsOneDescriptor pins what happens while two
// callers both believe they open this file.
//
// The kernel decides where an attempt's bytes are kept and opens the file
// when the answer is "on disk"; the streaming pumps still open it themselves
// too. Overwriting the handle on the second call would drop the only
// reference to the first descriptor, and nothing would ever close it — one
// leaked descriptor per stream, invisible until a busy process runs out.
func TestOpeningTheStreamTwiceKeepsOneDescriptor(t *testing.T) {
	dir := t.TempDir()
	var b Bodies
	if err := b.OpenStream(dir, "twice"); err != nil {
		t.Fatalf("first open: %v", err)
	}
	defer b.CloseStream()
	first := b.streamFile
	if first == nil {
		t.Fatal("first open produced no capture file")
	}
	if err := b.OpenStream(dir, "twice"); err != nil {
		t.Fatalf("second open: %v", err)
	}
	if b.streamFile != first {
		t.Fatalf("second open replaced the capture file (%v -> %v); the first descriptor is now unreachable and will never be closed", first, b.streamFile)
	}
}

// TestReopeningAfterCloseAppends pins the case the guard above must not
// break: a pre-first-byte failover closes the file on its way out and opens
// it again for the next attempt. That second open has to happen — and has to
// keep what the first attempt wrote, since the capture is the whole exchange,
// not the last try at it.
func TestReopeningAfterCloseAppends(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, StreamFileName("reopen"))
	var b Bodies
	if err := b.OpenStream(dir, "reopen"); err != nil {
		t.Fatalf("first open: %v", err)
	}
	if err := b.AppendStream([]byte("data: first\n\n")); err != nil {
		t.Fatalf("first append: %v", err)
	}
	b.CloseStream()

	if err := b.OpenStream(dir, "reopen"); err != nil {
		t.Fatalf("reopen: %v", err)
	}
	if err := b.AppendStream([]byte("data: second\n\n")); err != nil {
		t.Fatalf("second append: %v", err)
	}
	b.CloseStream()

	captured, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read capture file: %v", err)
	}
	if !strings.Contains(string(captured), "data: first") || !strings.Contains(string(captured), "data: second") {
		t.Fatalf("capture after a reopen holds %q, want both attempts' lines", captured)
	}
}

// TestAppendStopsAtTheBackstop: once the anti-OOM cap fires, the capture is
// marked truncated — never silently cut — and later appends are dropped
// without growing the file.
func TestAppendStopsAtTheBackstop(t *testing.T) {
	orig := MaxStreamFileBytes
	MaxStreamFileBytes = 20
	defer func() { MaxStreamFileBytes = orig }()

	dir := t.TempDir()
	path := filepath.Join(dir, StreamFileName("backstop"))
	var b Bodies
	if err := b.OpenStream(dir, "backstop"); err != nil {
		t.Fatalf("open: %v", err)
	}
	defer b.CloseStream()

	line := []byte("data: 0123456789\n") // 17 bytes: first fits, second would cross the cap
	if err := b.AppendStream(line); err != nil {
		t.Fatalf("append: %v", err)
	}
	if b.StreamTruncated() {
		t.Fatal("truncated after the first line, which fits the cap")
	}
	if err := b.AppendStream(line); err != nil {
		t.Fatalf("append past cap: %v", err)
	}
	if !b.StreamTruncated() {
		t.Fatal("crossing the cap did not mark the capture truncated")
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() != int64(len(line)) {
		t.Fatalf("file holds %d bytes, want exactly the one line (%d) that fit before the cap", info.Size(), len(line))
	}
}

// TestDiscardEmptyStreamDropsOnlyEmptyFiles: an empty capture is removed (it
// would render as a useless link), a capture with content survives, and a
// discarded capture no longer reports itself captured.
func TestDiscardEmptyStreamDropsOnlyEmptyFiles(t *testing.T) {
	dir := t.TempDir()

	empty := filepath.Join(dir, StreamFileName("empty"))
	var b Bodies
	if err := b.OpenStream(dir, "empty"); err != nil {
		t.Fatalf("open: %v", err)
	}
	b.DiscardEmptyStream()
	if _, err := os.Stat(empty); !os.IsNotExist(err) {
		t.Fatal("an empty capture file was left behind")
	}
	if b.StreamCaptured() {
		t.Fatal("a discarded capture still reports itself captured")
	}
	if got := b.StreamName(); got != "" {
		t.Fatalf("StreamName() = %q after the capture was discarded; persisting it would point the audit row at a deleted file", got)
	}

	full := filepath.Join(dir, StreamFileName("full"))
	var c Bodies
	if err := c.OpenStream(dir, "full"); err != nil {
		t.Fatalf("open: %v", err)
	}
	if err := c.AppendStream([]byte("data: kept\n")); err != nil {
		t.Fatalf("append: %v", err)
	}
	c.DiscardEmptyStream()
	c.CloseStream()
	if _, err := os.Stat(full); err != nil {
		t.Fatalf("a capture with content was discarded: %v", err)
	}
	if !c.StreamCaptured() {
		t.Fatal("a kept capture must still report itself captured")
	}
}

// TestBeginUpstreamAttemptClearsThePair: the request and the response it
// produced are one pair — a new attempt clears both, so the audit row can
// never show an earlier attempt's request beside a later attempt's response.
func TestBeginUpstreamAttemptClearsThePair(t *testing.T) {
	var b Bodies
	b.SetUpstreamRequest([]byte("req"))
	b.SetUpstreamResponse([]byte("resp"))
	b.SetResponse([]byte("client"))
	b.BeginUpstreamAttempt()
	if b.UpstreamRequest() != nil || b.UpstreamResponse() != nil || b.Response() != nil {
		t.Fatalf("a new attempt inherited the previous one's bodies: req=%q resp=%q client=%q",
			b.UpstreamRequest(), b.UpstreamResponse(), b.Response())
	}
}

// TestBeginDeliveryCaptureStartsClean: a fresh delivery capture starts with a
// clean upstream slate, or an attempt that never produced a body would stand
// under the previous provider's heading.
func TestBeginDeliveryCaptureStartsClean(t *testing.T) {
	var b Bodies
	b.SetUpstreamResponse([]byte("stale"))
	b.BeginDeliveryCapture()
	if b.UpstreamResponse() != nil {
		t.Fatalf("delivery capture started dirty: body=%q", b.UpstreamResponse())
	}
}
