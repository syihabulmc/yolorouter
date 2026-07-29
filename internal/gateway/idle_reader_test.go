package gateway

import (
	"bufio"
	"context"
	"errors"
	"io"
	"net/http"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// fakeEvent models one scheduled production of bytes/error from the fake reader.
// After delay elapses, data (if any) is returned alongside err.
type fakeEvent struct {
	delay time.Duration
	data  []byte
	err   error
}

// fakeReader is a controllable io.ReadCloser whose Read schedule is driven by
// a preset event list. Once all events are consumed, Read blocks forever
// until Close is called (mimicking an idle upstream that never EOFs).
//
// Close unblocks any in-flight Read immediately, which is essential for
// testing IdleReadCloser's Close path: body.Close must release the blocked
// reader goroutine.
type fakeReader struct {
	events []fakeEvent
	idx    int
	mu     sync.Mutex
	closed bool
	notify chan struct{}
}

// errFakeClosed is returned by fakeReader.Read after Close so the IdleReadCloser
// reader goroutine terminates cleanly on the Close path.
var errFakeClosed = errors.New("fake reader closed")

func newFakeReader(events []fakeEvent) *fakeReader {
	return &fakeReader{
		events: events,
		notify: make(chan struct{}),
	}
}

func (f *fakeReader) Read(p []byte) (int, error) {
	f.mu.Lock()
	if f.closed {
		f.mu.Unlock()
		return 0, errFakeClosed
	}
	if f.idx >= len(f.events) {
		f.mu.Unlock()
		// Block forever until Close.
		<-f.notify
		return 0, errFakeClosed
	}
	ev := f.events[f.idx]
	f.idx++
	f.mu.Unlock()

	if ev.delay > 0 {
		t := time.NewTimer(ev.delay)
		defer t.Stop()
		select {
		case <-t.C:
		case <-f.notify:
			return 0, errFakeClosed
		}
	}
	n := copy(p, ev.data)
	return n, ev.err
}

func (f *fakeReader) Close() error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.closed {
		return nil
	}
	f.closed = true
	close(f.notify)
	return nil
}

// oneByteFastReader returns one byte per Read with no delay. Used to fill
// chunkCh (buffered 1) and then block the reader goroutine on its second send,
// exercising the "Close unblocks a full chunkCh" path.
type oneByteFastReader struct {
	notify chan struct{}
}

func newOneByteFastReader() *oneByteFastReader {
	return &oneByteFastReader{notify: make(chan struct{})}
}

func (r *oneByteFastReader) Read(p []byte) (int, error) {
	select {
	case <-r.notify:
		return 0, errFakeClosed
	default:
	}
	if len(p) > 0 {
		p[0] = 'x'
		return 1, nil
	}
	return 0, nil
}

func (r *oneByteFastReader) Close() error {
	select {
	case <-r.notify:
	default:
		close(r.notify)
	}
	return nil
}

// assertExited waits up to 2s for the IdleReadCloser reader goroutine to
// terminate (signaled by the exited channel). Using the observable signal
// instead of runtime.NumGoroutine() deltas avoids flake under CI scheduling.
func assertExited(t *testing.T, r *IdleReadCloser) {
	t.Helper()
	select {
	case <-r.Exited():
	case <-time.After(2 * time.Second):
		t.Fatalf("reader goroutine did not exit within 2s (leak)")
	}
}

// closeIdle closes r ignoring the returned error. It exists purely so test
// cleanup reads as `defer closeIdle(r)` instead of the verbose
// `defer func() { _ = r.Close() }()` pattern, satisfying errcheck without
// noise. IdleReadCloser.Close is always nil.
func closeIdle(r *IdleReadCloser) { _ = r.Close() }

// -------- firstByte phase --------

// TestIdleReadCloser_FirstByteTimeout: open with no chunk within firstByteTimeout
// must surface ErrFirstByteTimeout.
func TestIdleReadCloser_FirstByteTimeout(t *testing.T) {
	fr := newFakeReader([]fakeEvent{
		{delay: 10 * time.Second, data: []byte("hello")},
	})
	r := NewIdleReadCloser(fr, 50*time.Millisecond, 30*time.Millisecond, context.Background())

	buf := make([]byte, 100)
	start := time.Now()
	n, err := r.Read(buf)
	elapsed := time.Since(start)

	assert.Zero(t, n)
	assert.ErrorIs(t, err, ErrFirstByteTimeout)
	// Sanity: fired around the firstByte budget, not the idle budget.
	assert.GreaterOrEqual(t, elapsed, 40*time.Millisecond)
	assert.Less(t, elapsed, 250*time.Millisecond)

	// First-byte timeout leaves the reader goroutine blocked inside body.Read
	// (no data has arrived). Close (io.ReadCloser contract) must release it.
	closeIdle(r)
	assertExited(t, r)
}

// TestIdleReadCloser_FirstBytePhaseIgnoresIdleBudget is the core two-phase
// test: an open-after silence that exceeds idle but stays under firstByteTimeout
// MUST NOT trip the idle timer.
//
// Setup: idle=30ms, firstByte=200ms, first chunk at 80ms. A naive single-phase
// implementation would fire idle at 30ms; the two-phase design correctly arms
// the firstByte timer (200ms) until the first chunk arrives.
func TestIdleReadCloser_FirstBytePhaseIgnoresIdleBudget(t *testing.T) {
	fr := newFakeReader([]fakeEvent{
		{delay: 80 * time.Millisecond, data: []byte("x")},
		{delay: 0, err: io.EOF},
	})
	r := NewIdleReadCloser(fr, 200*time.Millisecond, 30*time.Millisecond, context.Background())
	defer closeIdle(r)

	buf := make([]byte, 10)
	n, err := r.Read(buf)
	require.NoError(t, err, "firstByte phase must not be cut by idle timer")
	require.Equal(t, 1, n)
	assert.Equal(t, byte('x'), buf[0])
}

// TestIdleReadCloser_FirstByteReceivedSwitchesToIdle verifies that after the
// first chunk arrives, the timer is reset to the idle budget (not the
// firstByte budget). A subsequent gap > idle but < firstByte must now trip
// ErrIdleTimeout.
func TestIdleReadCloser_FirstByteReceivedSwitchesToIdle(t *testing.T) {
	fr := newFakeReader([]fakeEvent{
		{delay: 5 * time.Millisecond, data: []byte("first")},
		{delay: 200 * time.Millisecond, data: []byte("second")},
	})
	r := NewIdleReadCloser(fr, 500*time.Millisecond, 40*time.Millisecond, context.Background())
	defer closeIdle(r)

	buf := make([]byte, 100)
	n, err := r.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, 5, n)
	assert.Equal(t, "first", string(buf[:n]))

	start := time.Now()
	_, err = r.Read(buf)
	elapsed := time.Since(start)

	assert.ErrorIs(t, err, ErrIdleTimeout)
	// Idle budget ~40ms, well under firstByte 500ms.
	assert.GreaterOrEqual(t, elapsed, 30*time.Millisecond)
	assert.Less(t, elapsed, 250*time.Millisecond)

	closeIdle(r)
	assertExited(t, r)
}

// -------- inter-chunk phase --------

// TestIdleReadCloser_InterChunkKeepAlive: continuous chunks at intervals
// shorter than idle must not time out; the stream ends cleanly on EOF.
func TestIdleReadCloser_InterChunkKeepAlive(t *testing.T) {
	events := make([]fakeEvent, 0, 6)
	for range 5 {
		events = append(events, fakeEvent{delay: 20 * time.Millisecond, data: []byte("a")})
	}
	events = append(events, fakeEvent{delay: 5 * time.Millisecond, err: io.EOF})

	fr := newFakeReader(events)
	r := NewIdleReadCloser(fr, 200*time.Millisecond, 50*time.Millisecond, context.Background())
	defer closeIdle(r)

	buf := make([]byte, 10)
	total := 0
	for {
		n, err := r.Read(buf)
		total += n
		if err != nil {
			assert.ErrorIs(t, err, io.EOF, "stream should end with io.EOF, not timeout")
			break
		}
		if total > 5 {
			t.Fatalf("read too many bytes: total=%d", total)
		}
	}
	assert.Equal(t, 5, total, "expected 5 keep-alive chunks to arrive")
}

// TestIdleReadCloser_InterChunkTimeout: a gap exceeding idle after the first
// chunk must surface ErrIdleTimeout.
func TestIdleReadCloser_InterChunkTimeout(t *testing.T) {
	fr := newFakeReader([]fakeEvent{
		{delay: 5 * time.Millisecond, data: []byte("first")},
		{delay: 500 * time.Millisecond, data: []byte("second")},
	})
	r := NewIdleReadCloser(fr, 500*time.Millisecond, 50*time.Millisecond, context.Background())
	defer closeIdle(r)

	buf := make([]byte, 100)
	_, err := r.Read(buf)
	require.NoError(t, err)

	start := time.Now()
	_, err = r.Read(buf)
	elapsed := time.Since(start)

	assert.ErrorIs(t, err, ErrIdleTimeout)
	assert.GreaterOrEqual(t, elapsed, 40*time.Millisecond)
	assert.Less(t, elapsed, 300*time.Millisecond)

	closeIdle(r)
	assertExited(t, r)
}

// -------- EOF / underlying error --------

// TestIdleReadCloser_EOFWithFinalData: body.Read returning (n>0, io.EOF) must
// deliver the bytes now and io.EOF on the next Read (io.Reader contract).
func TestIdleReadCloser_EOFWithFinalData(t *testing.T) {
	fr := newFakeReader([]fakeEvent{
		{delay: 5 * time.Millisecond, data: []byte("hello"), err: io.EOF},
	})
	r := NewIdleReadCloser(fr, 200*time.Millisecond, 50*time.Millisecond, context.Background())
	defer closeIdle(r)

	buf := make([]byte, 100)
	n, err := r.Read(buf)
	require.NoError(t, err, "first Read returns data, EOF cached for next call")
	assert.Equal(t, 5, n)
	assert.Equal(t, "hello", string(buf[:n]))

	n, err = r.Read(buf)
	assert.Zero(t, n)
	assert.ErrorIs(t, err, io.EOF)
}

// TestIdleReadCloser_BareEOF: body.Read returning (0, io.EOF) surfaces io.EOF
// immediately.
func TestIdleReadCloser_BareEOF(t *testing.T) {
	fr := newFakeReader([]fakeEvent{
		{delay: 5 * time.Millisecond, data: []byte("x")},
		{delay: 5 * time.Millisecond, err: io.EOF},
	})
	r := NewIdleReadCloser(fr, 200*time.Millisecond, 50*time.Millisecond, context.Background())
	defer closeIdle(r)

	buf := make([]byte, 10)
	_, err := r.Read(buf)
	require.NoError(t, err)

	n, err := r.Read(buf)
	assert.Zero(t, n)
	assert.ErrorIs(t, err, io.EOF)
}

// TestIdleReadCloser_UnderlyingError: non-EOF errors propagate verbatim.
func TestIdleReadCloser_UnderlyingError(t *testing.T) {
	customErr := errors.New("upstream boom")
	fr := newFakeReader([]fakeEvent{
		{delay: 5 * time.Millisecond, data: []byte("x")},
		{delay: 5 * time.Millisecond, err: customErr},
	})
	r := NewIdleReadCloser(fr, 200*time.Millisecond, 50*time.Millisecond, context.Background())
	defer closeIdle(r)

	buf := make([]byte, 10)
	_, _ = r.Read(buf)
	n, err := r.Read(buf)
	assert.Zero(t, n)
	assert.ErrorIs(t, err, customErr)
}

// -------- ctx cancellation --------

// TestIdleReadCloser_CtxDeadlineExceeded covers the attempt-timeout and
// request-timeout cases: ctx.WithTimeout firing returns DeadlineExceeded.
func TestIdleReadCloser_CtxDeadlineExceeded(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	fr := newFakeReader([]fakeEvent{
		{delay: 10 * time.Second, data: []byte("hello")},
	})
	r := NewIdleReadCloser(fr, 500*time.Millisecond, 500*time.Millisecond, ctx)
	defer closeIdle(r)

	buf := make([]byte, 100)
	start := time.Now()
	n, err := r.Read(buf)
	elapsed := time.Since(start)

	assert.Zero(t, n)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.GreaterOrEqual(t, elapsed, 40*time.Millisecond)
	assert.Less(t, elapsed, 300*time.Millisecond)

	// Subsequent Read returns the cached error without re-entering select.
	n2, err2 := r.Read(buf)
	assert.Zero(t, n2)
	assert.ErrorIs(t, err2, context.DeadlineExceeded)

	closeIdle(r)
	assertExited(t, r)
}

// TestIdleReadCloser_CtxCanceled covers the client-disconnect case: manual
// cancel returns Canceled.
func TestIdleReadCloser_CtxCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fr := newFakeReader([]fakeEvent{
		{delay: 10 * time.Second, data: []byte("hello")},
	})
	r := NewIdleReadCloser(fr, 500*time.Millisecond, 500*time.Millisecond, ctx)
	defer closeIdle(r)

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()

	buf := make([]byte, 100)
	start := time.Now()
	n, err := r.Read(buf)
	elapsed := time.Since(start)

	assert.Zero(t, n)
	assert.ErrorIs(t, err, context.Canceled)
	assert.GreaterOrEqual(t, elapsed, 40*time.Millisecond)
	assert.Less(t, elapsed, 300*time.Millisecond)

	closeIdle(r)
	assertExited(t, r)
}

// TestIdleReadCloser_CtxAlreadyCanceled: passing an already-canceled ctx
// surfaces the error on the first Read without delay.
func TestIdleReadCloser_CtxAlreadyCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	fr := newFakeReader([]fakeEvent{
		{delay: 10 * time.Second, data: []byte("hello")},
	})
	r := NewIdleReadCloser(fr, 500*time.Millisecond, 500*time.Millisecond, ctx)

	buf := make([]byte, 100)
	start := time.Now()
	n, err := r.Read(buf)
	elapsed := time.Since(start)

	assert.Zero(t, n)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Less(t, elapsed, 50*time.Millisecond, "already-canceled ctx must return immediately")

	closeIdle(r)
	assertExited(t, r)
}

// -------- large chunk / leftover --------

// TestIdleReadCloser_PartialReadAcrossCalls verifies a chunk larger than the
// caller's buffer is served across multiple Reads and does not cause the idle
// timer to fire while draining already-received bytes.
func TestIdleReadCloser_PartialReadAcrossCalls(t *testing.T) {
	data := make([]byte, 1000)
	for i := range data {
		data[i] = 'x'
	}
	fr := newFakeReader([]fakeEvent{
		{delay: 5 * time.Millisecond, data: data},
		{delay: 5 * time.Millisecond, err: io.EOF},
	})
	r := NewIdleReadCloser(fr, 200*time.Millisecond, 50*time.Millisecond, context.Background())
	defer closeIdle(r)

	buf := make([]byte, 40) // much smaller than the 1000-byte chunk
	total := 0
	for total < 1000 {
		n, err := r.Read(buf)
		total += n
		if err != nil {
			t.Fatalf("unexpected err while draining leftover at total=%d: %v", total, err)
		}
	}
	assert.Equal(t, 1000, total)

	_, err := r.Read(buf)
	assert.ErrorIs(t, err, io.EOF)
}

// TestIdleReadCloser_SmallBufferFinalChunkWithEOF enforces the io.Reader
// contract when the upstream's terminal Read delivers more bytes than the
// caller's buffer: every byte must be delivered before the terminal error
// surfaces.
//
// Regression: a check-order bug (lastErr before leftover) would discard the
// leftover bytes the moment a terminal error accompanied the first partial
// delivery, silently truncating the tail of the stream.
func TestIdleReadCloser_SmallBufferFinalChunkWithEOF(t *testing.T) {
	const total = 100
	payload := make([]byte, total)
	for i := range payload {
		payload[i] = byte('A' + (i % 26))
	}
	// Single terminal Read: all bytes + io.EOF together.
	fr := newFakeReader([]fakeEvent{
		{delay: 5 * time.Millisecond, data: payload, err: io.EOF},
	})
	r := NewIdleReadCloser(fr, 5*time.Second, 5*time.Second, context.Background())
	defer closeIdle(r)

	got := make([]byte, 0, total)
	buf := make([]byte, 16) // much smaller than payload, forces leftover path
	for {
		n, err := r.Read(buf)
		got = append(got, buf[:n]...)
		if err != nil {
			require.ErrorIs(t, err, io.EOF)
			break
		}
	}
	require.Len(t, got, total, "leftover discarded by old check order")
	require.Equal(t, string(payload), string(got))
}

// -------- goroutine leak safety --------

// TestIdleReadCloser_GoroutineExitsOnEOF: after a clean EOF the reader
// goroutine must terminate on its own (no Close needed for this path).
func TestIdleReadCloser_GoroutineExitsOnEOF(t *testing.T) {
	fr := newFakeReader([]fakeEvent{
		{delay: 5 * time.Millisecond, data: []byte("x")},
		{delay: 5 * time.Millisecond, err: io.EOF},
	})
	r := NewIdleReadCloser(fr, 200*time.Millisecond, 50*time.Millisecond, context.Background())
	defer closeIdle(r)

	buf := make([]byte, 10)
	for {
		_, err := r.Read(buf)
		if err != nil {
			break
		}
	}
	assertExited(t, r)
}

// TestIdleReadCloser_GoroutineExitsOnClose: Close must unblock a reader
// goroutine currently stuck inside body.Read.
func TestIdleReadCloser_GoroutineExitsOnClose(t *testing.T) {
	fr := newFakeReader([]fakeEvent{
		{delay: 10 * time.Second, data: []byte("x")},
	})
	r := NewIdleReadCloser(fr, 10*time.Second, 10*time.Second, context.Background())

	closeIdle(r)
	assertExited(t, r)
}

// TestIdleReadCloser_GoroutineExitsOnTimeout: after a terminal timeout the
// reader goroutine is still blocked on body.Read; Close (the caller's
// responsibility per io.ReadCloser contract) must drain it.
func TestIdleReadCloser_GoroutineExitsOnTimeout(t *testing.T) {
	fr := newFakeReader([]fakeEvent{
		{delay: 10 * time.Second, data: []byte("x")},
	})
	r := NewIdleReadCloser(fr, 30*time.Millisecond, 30*time.Millisecond, context.Background())

	buf := make([]byte, 10)
	_, _ = r.Read(buf) // triggers ErrFirstByteTimeout

	// Reader goroutine is still blocked on body.Read because no data arrived.
	// Close must release it.
	closeIdle(r)
	assertExited(t, r)
}

// -------- chunkCh-full Close --------

// TestIdleReadCloser_CloseWithFullChunkCh: when the consumer stops reading,
// chunkCh (buffered 1) fills and the reader goroutine's next send blocks.
// Close must unblock it via done so it does not leak.
func TestIdleReadCloser_CloseWithFullChunkCh(t *testing.T) {
	fr := newOneByteFastReader()
	r := NewIdleReadCloser(fr, 10*time.Second, 10*time.Second, context.Background())

	// Give the reader goroutine time to fill chunkCh and block on the
	// second send.
	time.Sleep(50 * time.Millisecond)

	done := make(chan struct{})
	go func() {
		closeIdle(r)
		close(done)
	}()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Close blocked; reader goroutine likely stuck on full chunkCh")
	}
	assertExited(t, r)
}

// -------- idempotent Close --------

// TestIdleReadCloser_CloseIdempotent: multiple Close calls must not panic
// (double close of done/body) and must not block.
func TestIdleReadCloser_CloseIdempotent(t *testing.T) {
	fr := newFakeReader([]fakeEvent{
		{delay: 5 * time.Millisecond, data: []byte("x")},
	})
	r := NewIdleReadCloser(fr, 200*time.Millisecond, 50*time.Millisecond, context.Background())

	require.NotPanics(t, func() {
		closeIdle(r)
		closeIdle(r)
		closeIdle(r)
	})
	assertExited(t, r)
}

// -------- empty buffer --------

// TestIdleReadCloser_EmptyBuffer: Read with zero-length buffer returns
// immediately without consuming a chunk or advancing the timer.
func TestIdleReadCloser_EmptyBuffer(t *testing.T) {
	fr := newFakeReader([]fakeEvent{
		{delay: 5 * time.Millisecond, data: []byte("x")},
	})
	r := NewIdleReadCloser(fr, 200*time.Millisecond, 50*time.Millisecond, context.Background())
	defer closeIdle(r)

	n, err := r.Read(nil)
	require.NoError(t, err)
	assert.Zero(t, n)

	// Subsequent real Read still works and gets the chunk.
	buf := make([]byte, 10)
	n, err = r.Read(buf)
	require.NoError(t, err)
	assert.Equal(t, 1, n)
}

// -------- consumer backpressure must not trip idle timer --------

// TestIdleReadCloser_QueuedChunkSurvivesConsumerBackpressure covers the
// consumer-backpressure race: when the reader goroutine has already queued a
// chunk in chunkCh (buffered 1) but the consumer paused longer than the idle
// budget before calling Read again, the non-blocking pre-check must deliver
// the queued chunk instead of letting the idle timer fire ErrIdleTimeout.
//
// Without the fix, the blocking select would see both timer.C (already fired
// during the pause) and chunkCh (ready) and may pick timer.C — killing a
// healthy stream that had data already waiting.
func TestIdleReadCloser_QueuedChunkSurvivesConsumerBackpressure(t *testing.T) {
	fr := newFakeReader([]fakeEvent{
		{delay: 5 * time.Millisecond, data: []byte("first")},
		{delay: 5 * time.Millisecond, data: []byte("second")},
		{delay: 10 * time.Second, err: io.EOF},
	})
	r := NewIdleReadCloser(fr, 200*time.Millisecond, 30*time.Millisecond, context.Background())
	defer closeIdle(r)

	buf := make([]byte, 100)
	// Read the first chunk.
	n, err := r.Read(buf)
	require.NoError(t, err)
	require.Equal(t, 5, n)
	require.Equal(t, "first", string(buf[:n]))

	// Pause longer than the idle budget (30ms). During this pause the
	// reader goroutine reads "second" (~10ms in) and queues it in chunkCh,
	// and the idle timer fires (~35ms in) leaving a stale tick in timer.C.
	time.Sleep(100 * time.Millisecond)

	// The non-blocking pre-check must drain the queued "second" chunk
	// before the blocking select observes the stale timer tick.
	n, err = r.Read(buf)
	require.NoError(t, err, "queued chunk must be delivered despite idle timer having fired")
	require.Equal(t, 6, n)
	require.Equal(t, "second", string(buf[:n]))
}

// -------- chunkCh closure with cancelled ctx --------

// TestIdleReadCloser_ChunkChClosedWithCancelledCtxReturnsCanceled covers the
// chunkCh-closure race: when Close fires concurrently with ctx cancellation,
// the reader goroutine may exit (selecting <-r.done in readLoop) without
// delivering the terminal context.Canceled result. chunkCh then closes and
// Read hits the !ok path. The fix prefers ctx.Err() (Canceled) over the
// default io.EOF so client disconnect is not masked as a clean EOF.
//
// To deterministically trigger the !ok path (rather than having readLoop
// deliver errFakeClosed before exiting), this test fills chunkCh (buffered 1)
// with "second" and leaves readLoop blocked on the EOF send. When Close fires
// done, <-r.done wins the send select and readLoop exits without delivering
// the terminal error — leaving "second" buffered in a closed chunkCh.
//
// The pre-check ctx.Err() gate now runs BEFORE the chunkCh drain, so the first
// Read after cancel+close returns ctx.Err immediately — the queued "second"
// chunk is discarded because the client has disconnected and the data cannot
// be delivered. The closure-preference assertion still holds on subsequent
// Reads.
func TestIdleReadCloser_ChunkChClosedWithCancelledCtxReturnsCanceled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fr := newFakeReader([]fakeEvent{
		{delay: 5 * time.Millisecond, data: []byte("first")},
		{delay: 5 * time.Millisecond, data: []byte("second")},
		{delay: 5 * time.Millisecond, err: io.EOF},
	})
	r := NewIdleReadCloser(fr, 500*time.Millisecond, 500*time.Millisecond, ctx)
	defer closeIdle(r)

	buf := make([]byte, 100)
	// Read the first chunk successfully.
	n, err := r.Read(buf)
	require.NoError(t, err)
	require.Equal(t, 5, n)

	// Give the reader goroutine time to read "second" (queued in chunkCh,
	// buffered 1) and then block on the EOF send — chunkCh is full so the
	// send select blocks, waiting on either chunkCh drain or done.
	time.Sleep(50 * time.Millisecond)

	// Cancel ctx and Close while the reader goroutine is blocked on the
	// send. <-r.done wins and readLoop exits without delivering EOF.
	cancel()
	closeIdle(r)
	assertExited(t, r)

	// First Read after cancel: the pre-check ctx.Err() gate fires before
	// chunkCh is drained, so the queued "second" chunk is discarded and
	// ctx.Err is returned. Without the gate, the chunk would be delivered
	// and the cancellation deferred to the next Read.
	n, err = r.Read(buf)
	assert.Zero(t, n, "queued chunk must be discarded when ctx is canceled")
	assert.ErrorIs(t, err, context.Canceled,
		"ctx cancel must beat queued chunk even when chunkCh has buffered data")

	// Subsequent Read observes chunkCh drained-and-closed (!ok) and must
	// still prefer ctx.Err() (Cached) over the default io.EOF.
	n, err = r.Read(buf)
	assert.Zero(t, n)
	assert.ErrorIs(t, err, context.Canceled,
		"chunkCh closure with cancelled ctx must return Canceled, not io.EOF")

	// Further Reads return the cached error.
	n2, err2 := r.Read(buf)
	assert.Zero(t, n2)
	assert.ErrorIs(t, err2, context.Canceled)
}

// TestIdleReadCloser_ConcurrentCancelAndCloseWhileReadBlocked is the concurrent
// variant of the chunkCh-closure race: Read is blocked in the select when both
// ctx cancellation and Close fire simultaneously. Regardless of which select
// branch wins (ctx.Done or chunkCh closure via !ok), the result must be
// context.Canceled, not io.EOF.
func TestIdleReadCloser_ConcurrentCancelAndCloseWhileReadBlocked(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fr := newFakeReader([]fakeEvent{
		{delay: 10 * time.Second, data: []byte("hello")},
	})
	r := NewIdleReadCloser(fr, 500*time.Millisecond, 500*time.Millisecond, ctx)
	defer closeIdle(r)

	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
		closeIdle(r)
	}()

	buf := make([]byte, 100)
	start := time.Now()
	n, err := r.Read(buf)
	elapsed := time.Since(start)

	assert.Zero(t, n)
	assert.ErrorIs(t, err, context.Canceled,
		"concurrent cancel+Close during blocked Read must return Canceled, not EOF")
	assert.GreaterOrEqual(t, elapsed, 40*time.Millisecond)
	assert.Less(t, elapsed, 300*time.Millisecond)
	assertExited(t, r)
}

// -------- peek wrap must preserve IdleReadCloser.Close --------

// TestIdleReadCloser_PeekWrapClosePropagates verifies the relay.go wrap pattern:
// relay.go wraps resp.Body (an IdleReadCloser) with bufio.Reader + Peek(1)
// before deciding stream-vs-json. The wrapping MUST preserve the original
// IdleReadCloser's Closer so downstream defer resp.Body.Close() releases the
// reader goroutine.
//
// The fix uses struct{ io.Reader; io.Closer }{peekBuf, inner} instead of
// io.NopCloser(peekBuf). This test reproduces the post-fix pattern and asserts
// that Close propagates to inner.Close (reader goroutine exits within 2s).
//
// Counterfactual: with io.NopCloser the wrapped Close is a no-op; inner.Close
// is never called; the reader goroutine remains blocked inside body.Read
// (fakeReader blocks forever on its second event), and assertExited would time
// out after 2s — exactly the leak class the fix addresses.
func TestIdleReadCloser_PeekWrapClosePropagates(t *testing.T) {
	fr := newFakeReader([]fakeEvent{
		{delay: 5 * time.Millisecond, data: []byte("x")},
		// Block forever (until Close) to mimic the "Scanner tripped over a
		// >1MiB SSE line and the consumer bailed early" scenario where the
		// reader goroutine is stuck inside body.Read with chunkCh full.
		{delay: 10 * time.Second, data: []byte("y")},
	})
	inner := NewIdleReadCloser(fr, 200*time.Millisecond, 200*time.Millisecond, context.Background())

	// Apply the post-fix wrap pattern from relay.go.
	peekBuf := bufio.NewReaderSize(inner, 1)
	_, _ = peekBuf.Peek(1)
	wrapped := struct {
		io.Reader
		io.Closer
	}{peekBuf, inner}

	// Drain the peeked byte so the goroutine is not mid-send on chunkCh.
	buf := make([]byte, 1)
	n, err := wrapped.Read(buf)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	// The handler's defer resp.Body.Close() hits wrapped.Close. Under the
	// fix this delegates to inner.Close, which in turn unblocks the reader
	// goroutine currently parked inside fakeReader.Read on the 10s event.
	require.NoError(t, wrapped.Close())
	assertExited(t, inner)
}

// -------- terminal readResult must still prefer ctx.Err --------

// TestIdleReadCloser_TerminalReadResultWithCanceledCtx_PrefersCtx covers the
// hole left by the chunkCh-closure fix: handleChunk only prioritized ctx.Err()
// on chunkCh CLOSURE (ok=false), not when a terminal readResult (ok=true,
// res.err != nil) was already queued. This test closes that hole by checking
// ctx.Err() inside the res.err != nil branch too.
//
// Scenario: the underlying body.Read returns a closed-body error (e.g. the
// attempt ctx fired and transport tore down the connection). readLoop sends
// readResult{err: closedBodyErr} into chunkCh. The consumer, before calling
// Read again, observes ctx cancel (e.g. via ctx.Done in the handler). The next
// Read drains chunkCh and sees the terminal readResult — but ctx.Err() must
// win so the relay layer bills this as a client disconnect rather than a
// provider fault.
//
// Determinism: we (a) let readLoop deliver the terminal result into chunkCh
// (buffered 1) before cancelling, then (b) cancel, then (c) Read. The
// non-blocking pre-check in Read drains the buffered chunk and routes it
// through handleChunk.
func TestIdleReadCloser_TerminalReadResultWithCanceledCtx_PrefersCtx(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fr := newFakeReader([]fakeEvent{
		{delay: 5 * time.Millisecond, data: []byte("x")},
		// Terminal "body closed" error — NOT context.Canceled. Without the
		// fix, handleChunk would cache this as lastErr and the relay layer
		// would misclassify the disconnect as a provider fault.
		{delay: 5 * time.Millisecond, err: errClosedBody},
	})
	r := NewIdleReadCloser(fr, 500*time.Millisecond, 500*time.Millisecond, ctx)
	defer closeIdle(r)

	buf := make([]byte, 10)
	// Drain the first chunk so firstByteReceived is true and readLoop advances
	// to the second event.
	n, err := r.Read(buf)
	require.NoError(t, err)
	require.Equal(t, 1, n)

	// Give readLoop time to read the second event and deliver the terminal
	// readResult{err: errClosedBody} into chunkCh (buffered 1).
	time.Sleep(50 * time.Millisecond)

	// NOW cancel ctx. The terminal readResult is already queued; without the
	// fix, handleChunk would return errClosedBody.
	cancel()

	// Read observes the queued terminal readResult. handleChunk's res.err !=
	// nil branch must check ctx.Err() FIRST and prefer context.Canceled.
	n, err = r.Read(buf)
	assert.Zero(t, n)
	assert.ErrorIs(t, err, context.Canceled,
		"ctx cancellation must take priority over queued terminal err; got err=%v", err)
	assert.NotErrorIs(t, err, errClosedBody,
		"queued closedBodyErr must NOT win over ctx.Canceled")

	// Subsequent Read returns the cached Canceled.
	n2, err2 := r.Read(buf)
	assert.Zero(t, n2)
	assert.ErrorIs(t, err2, context.Canceled)
}

// errClosedBody stands in for any "body was closed under us" read error that
// is NOT itself a context error. Typical causes: transport teardown after the
// attempt ctx fires, or an HTTP/2 stream reset. Crucially it does NOT wrap
// context.Canceled — the test relies on errors.Is(err, context.Canceled)
// returning false for this sentinel.
var errClosedBody = errors.New("upstream body closed")

// -------- ctx > chunk > timer priority in blocking select --------

// TestIdleReadCloser_CtxBeatsAlreadyFiredTimer covers the priority invariant:
// when the idle timer has ALREADY fired (pending tick in timer.C) and ctx is
// then canceled before the next Read, the blocking select sees both timer.C
// and ctx.Done ready. Without the fix, Go's random select pick could return
// ErrIdleTimeout (→ 502 + circuit failure) instead of ctx.Canceled (→ 499).
//
// The fix adds an explicit ctx.Err() check at the top of the timer.C case so
// ctx always wins regardless of which case the runtime selects.
//
// Determinism: we cancel ctx AFTER the idle timer fires and BEFORE calling
// Read. Both channels are ready when Read enters the blocking select; there is
// no timing race — the assertion holds every run.
func TestIdleReadCloser_CtxBeatsAlreadyFiredTimer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	fr := newFakeReader([]fakeEvent{
		{delay: 5 * time.Millisecond, data: []byte("first")},
		// Long delay: second chunk never arrives within the test window.
		{delay: 10 * time.Second, data: []byte("second")},
	})
	r := NewIdleReadCloser(fr, 500*time.Millisecond, 30*time.Millisecond, ctx)
	defer closeIdle(r)

	buf := make([]byte, 100)
	// Read the first chunk: firstByteReceived=true, timer reset to idle=30ms.
	n, err := r.Read(buf)
	require.NoError(t, err)
	require.Equal(t, 5, n)

	// Wait until the idle timer fires (30ms budget). After this, timer.C has
	// a pending tick buffered.
	time.Sleep(80 * time.Millisecond)

	// NOW cancel ctx. ctx.Done becomes ready while timer.C is already ready.
	// The next Read's blocking select will see both cases simultaneously.
	cancel()

	n, err = r.Read(buf)
	assert.Zero(t, n, "ctx cancellation must produce zero bytes")
	assert.ErrorIs(t, err, context.Canceled,
		"ctx must take priority over already-fired idle timer; got err=%v", err)
	assert.NotErrorIs(t, err, ErrIdleTimeout,
		"ErrIdleTimeout must NOT win over ctx.Canceled when both are ready")
	assert.NotErrorIs(t, err, ErrFirstByteTimeout,
		"ErrFirstByteTimeout must NOT win over ctx.Canceled when both are ready")
}

// TestIdleReadCloser_CtxBeatsAlreadyFiredFirstByteTimer covers the first-byte
// phase variant: timer.C fires during the first-byte window (no chunk yet)
// and ctx is canceled before Read observes either. The fix must still prefer
// ctx.Canceled over ErrFirstByteTimeout.
func TestIdleReadCloser_CtxBeatsAlreadyFiredFirstByteTimer(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())

	fr := newFakeReader([]fakeEvent{
		{delay: 10 * time.Second, data: []byte("late")},
	})
	r := NewIdleReadCloser(fr, 30*time.Millisecond, 30*time.Millisecond, ctx)
	defer closeIdle(r)

	// Wait until the firstByte timer fires (30ms). timer.C has a pending tick.
	time.Sleep(80 * time.Millisecond)

	// Cancel ctx while timer.C is already ready.
	cancel()

	buf := make([]byte, 100)
	n, err := r.Read(buf)
	assert.Zero(t, n)
	assert.ErrorIs(t, err, context.Canceled,
		"ctx must beat already-fired firstByte timer; got err=%v", err)
	assert.NotErrorIs(t, err, ErrFirstByteTimeout)
}

// blockingSignalReader delivers exactly one chunk immediately, then blocks on
// a signal channel for the second chunk. This lets tests synchronize the
// moment the second chunk lands in chunkCh relative to other events (e.g. ctx
// cancellation) — used to exercise the blocking select's chunkCh case under
// the priority fix.
type blockingSignalReader struct {
	firstChunk   []byte
	signalSecond chan struct{}
	notify       chan struct{}
}

func newBlockingSignalReader(first []byte) *blockingSignalReader {
	return &blockingSignalReader{
		firstChunk:   first,
		signalSecond: make(chan struct{}),
		notify:       make(chan struct{}),
	}
}

func (r *blockingSignalReader) Read(p []byte) (int, error) {
	select {
	case <-r.notify:
		return 0, errFakeClosed
	default:
	}

	if r.firstChunk != nil {
		n := copy(p, r.firstChunk)
		r.firstChunk = nil
		return n, nil
	}

	// Block until signaled to deliver the second chunk, or until Close.
	select {
	case <-r.signalSecond:
		return copy(p, []byte("second")), nil
	case <-r.notify:
		return 0, errFakeClosed
	}
}

func (r *blockingSignalReader) Close() error {
	select {
	case <-r.notify:
	default:
		close(r.notify)
	}
	return nil
}

// TestIdleReadCloser_CtxBeatsQueuedChunkInBlockingSelect covers the chunkCh
// case of the priority invariant: when ctx is canceled while the consumer is
// parked in the blocking select and a chunk then arrives in chunkCh, both
// ctx.Done and chunkCh may be ready. The fix adds a ctx.Err() pre-check
// inside the chunkCh case so ctx always wins, regardless of which case the
// runtime selects.
//
// Synchronization barrier: we start Read in a goroutine, wait for it to be
// parked in the blocking select, then cancel ctx and IMMEDIATELY release the
// signal that lets the reader goroutine deliver the second chunk. The two
// operations happen back-to-back in the same test goroutine so the consumer
// (parked in select) observes both channels ready when the runtime eventually
// schedules it.
//
// Run sub-tests × iterations because Go's multi-ready select is randomized;
// the assertion must hold on every iteration with the fix in place.
func TestIdleReadCloser_CtxBeatsQueuedChunkInBlockingSelect(t *testing.T) {
	const iterations = 30
	for i := range iterations {
		ctx, cancel := context.WithCancel(context.Background())

		fr := newBlockingSignalReader([]byte("first"))
		r := NewIdleReadCloser(fr, 500*time.Millisecond, 500*time.Millisecond, ctx)

		buf := make([]byte, 100)
		// Read the first chunk so firstByteReceived=true and the reader
		// goroutine parks on the signal-second Read.
		n, err := r.Read(buf)
		require.NoError(t, err, "iter %d", i)
		require.Equal(t, 5, n, "iter %d", i)

		// Start the second Read in a goroutine. It enters the blocking
		// select (chunkCh empty, reader goroutine parked on signalSecond).
		type readOutcome struct {
			n   int
			err error
		}
		outCh := make(chan readOutcome, 1)
		go func() {
			n, err := r.Read(buf)
			outCh <- readOutcome{n: n, err: err}
		}()

		// Let the goroutine enter the blocking select.
		time.Sleep(20 * time.Millisecond)

		// Cancel ctx FIRST, then immediately release the second chunk.
		// Both operations run back-to-back in this goroutine so the
		// consumer observes both channels ready when scheduled.
		cancel()
		close(fr.signalSecond)

		select {
		case out := <-outCh:
			assert.Zero(t, out.n, "iter %d: ctx canceled must discard queued chunk", i)
			assert.ErrorIs(t, out.err, context.Canceled,
				"iter %d: ctx must beat queued chunk in blocking select; got err=%v", i, out.err)
		case <-time.After(1 * time.Second):
			t.Fatalf("iter %d: Read did not return within 1s", i)
		}

		closeIdle(r)
		assertExited(t, r)
	}
}

// -------- complete ctx > chunk > timer priority --------

// TestIdleReadCloser_CtxBeatsQueuedChunkInPreCheck covers the pre-check gap:
// when ctx is canceled AFTER a chunk is already queued in chunkCh but BEFORE
// Read is called, the non-blocking pre-check would naively drain and deliver
// the healthy chunk, masking the cancellation. The fix adds a ctx.Err() check
// before the pre-check so ctx always wins.
//
// Determinism: we let readLoop queue "second" in chunkCh (buffered 1), then
// cancel ctx, then call Read. Both the leftover/pre-check ctx checks and the
// blocking-select chunkCh case ctx check must honor the cancellation.
func TestIdleReadCloser_CtxBeatsQueuedChunkInPreCheck(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	fr := newFakeReader([]fakeEvent{
		{delay: 5 * time.Millisecond, data: []byte("first")},
		{delay: 5 * time.Millisecond, data: []byte("second")},
		{delay: 10 * time.Second, err: io.EOF},
	})
	r := NewIdleReadCloser(fr, 500*time.Millisecond, 500*time.Millisecond, ctx)
	defer closeIdle(r)

	buf := make([]byte, 100)
	// Read the first chunk so readLoop advances to the second event.
	n, err := r.Read(buf)
	require.NoError(t, err)
	require.Equal(t, 5, n)

	// Give readLoop time to read "second" and queue it in chunkCh (buffered 1).
	time.Sleep(50 * time.Millisecond)

	// NOW cancel ctx. "second" is already queued; without the fix the
	// pre-check would drain and deliver it, masking the cancellation.
	cancel()

	// Read must return ctx.Err, NOT the queued chunk.
	n, err = r.Read(buf)
	assert.Zero(t, n, "queued chunk must be discarded when ctx is canceled")
	assert.ErrorIs(t, err, context.Canceled,
		"ctx must beat queued chunk in pre-check; got err=%v", err)
}

// TestIdleReadCloser_CtxBeatsLeftover covers the leftover gap: when ctx is
// canceled while leftover bytes remain from a previous large-chunk drain, the
// next Read must return ctx.Err instead of delivering the buffered bytes.
// Without the fix, a healthy stream canceled mid-drain would emit leftover
// data and defer the cancellation, letting the relay layer write a success
// terminal frame for a disconnected client.
func TestIdleReadCloser_CtxBeatsLeftover(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	data := make([]byte, 1000)
	for i := range data {
		data[i] = 'x'
	}
	fr := newFakeReader([]fakeEvent{
		{delay: 5 * time.Millisecond, data: data},
		{delay: 10 * time.Second, err: io.EOF},
	})
	r := NewIdleReadCloser(fr, 500*time.Millisecond, 500*time.Millisecond, ctx)
	defer closeIdle(r)

	smallBuf := make([]byte, 40)
	// Partial Read populates leftover (960 bytes remain).
	n, err := r.Read(smallBuf)
	require.NoError(t, err)
	require.Equal(t, 40, n)

	// Cancel ctx while leftover is non-empty.
	cancel()

	// Next Read must return ctx.Err, NOT leftover bytes.
	n, err = r.Read(smallBuf)
	assert.Zero(t, n, "leftover must be discarded when ctx is canceled")
	assert.ErrorIs(t, err, context.Canceled,
		"ctx must beat leftover drain; got err=%v", err)
}

// TestIdleReadCloser_TimerFiredWithQueuedChunkReturnsChunk covers the timer.C
// re-check: when the idle timer has fired (pending tick in timer.C) and a
// chunk is queued in chunkCh, Read must deliver the chunk rather than declaring
// an idle timeout. The fix adds a non-blocking chunkCh re-check inside the
// timer.C case so a healthy stream is not misclassified as 502 when Go's
// random select picks timer.C over chunkCh.
//
// Using blockingSignalReader we queue the second chunk after the timer has
// already fired, then call Read. The pre-check should drain the chunk; even if
// it didn't and the blocking select ran, the timer.C re-check would find it.
// Either way the result is the chunk, not ErrIdleTimeout.
func TestIdleReadCloser_TimerFiredWithQueuedChunkReturnsChunk(t *testing.T) {
	const iterations = 30
	for i := range iterations {
		ctx, cancel := context.WithCancel(context.Background())
		fr := newBlockingSignalReader([]byte("first"))
		// Short idle (30ms) so the timer fires quickly after the first chunk.
		r := NewIdleReadCloser(fr, 500*time.Millisecond, 30*time.Millisecond, ctx)

		buf := make([]byte, 100)
		// Read first chunk: firstByteReceived=true, timer reset to idle=30ms.
		n, err := r.Read(buf)
		require.NoError(t, err, "iter %d", i)
		require.Equal(t, 5, n, "iter %d", i)

		// Wait until idle timer fires (30ms budget) → timer.C has a tick.
		time.Sleep(80 * time.Millisecond)

		// Release the second chunk so the reader goroutine sends it to
		// chunkCh. After this both timer.C (pending tick) and chunkCh are ready.
		close(fr.signalSecond)
		// Give the reader goroutine time to complete the send.
		time.Sleep(10 * time.Millisecond)

		// Read must deliver the chunk (via pre-check or timer.C re-check),
		// NOT declare ErrIdleTimeout despite the already-fired timer.
		start := time.Now()
		n, err = r.Read(buf)
		elapsed := time.Since(start)

		require.NoError(t, err, "iter %d: queued chunk must be delivered, not timeout", i)
		require.Equal(t, 6, n, "iter %d: second chunk payload", i)
		require.Equal(t, "second", string(buf[:n]), "iter %d", i)
		require.Less(t, elapsed, 200*time.Millisecond, "iter %d: should return promptly", i)

		closeIdle(r)
		assertExited(t, r)
		cancel()
	}
}

// TestFirstByteBudgetFor_StatusSplit covers the policy: any non-2xx status
// (including 3xx redirects, 101 protocol switches, 4xx client errors, and
// 5xx server errors) uses the short errorBodyFirstByteTimeout (10s) instead
// of the configured firstByteTimeout (600s default) so a stalled retryable
// 503/429 failover or a dangling 3xx/101 body does not burn 10 minutes per
// candidate. 2xx responses keep the configured firstByteTimeout.
func TestFirstByteBudgetFor_StatusSplit(t *testing.T) {
	configured := 600 * time.Second
	tests := []struct {
		name       string
		statusCode int
		want       time.Duration
	}{
		{"200_keeps_configured", 200, configured},
		{"201_keeps_configured", 201, configured},
		{"299_keeps_configured", 299, configured},
		{"101_uses_short_error_budget", 101, errorBodyFirstByteTimeout},
		{"301_uses_short_error_budget", 301, errorBodyFirstByteTimeout},
		{"302_uses_short_error_budget", 302, errorBodyFirstByteTimeout},
		{"307_uses_short_error_budget", 307, errorBodyFirstByteTimeout},
		{"400_uses_short_error_budget", 400, errorBodyFirstByteTimeout},
		{"404_uses_short_error_budget", 404, errorBodyFirstByteTimeout},
		{"429_uses_short_error_budget", 429, errorBodyFirstByteTimeout},
		{"500_uses_short_error_budget", 500, errorBodyFirstByteTimeout},
		{"502_uses_short_error_budget", 502, errorBodyFirstByteTimeout},
		{"503_uses_short_error_budget", 503, errorBodyFirstByteTimeout},
		{"504_uses_short_error_budget", 504, errorBodyFirstByteTimeout},
		{"599_uses_short_error_budget", 599, errorBodyFirstByteTimeout},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := firstByteBudgetFor(tt.statusCode, configured)
			assert.Equal(t, tt.want, got,
				"firstByteBudgetFor(%d, configured=%s) = %s; want %s",
				tt.statusCode, configured, got, tt.want)
		})
	}
	// Sanity: the short budget is materially shorter than the configured one,
	// otherwise the short-error-budget fix would have no effect.
	assert.Less(t, errorBodyFirstByteTimeout, configured,
		"errorBodyFirstByteTimeout (%s) must be < the configured firstByteTimeout (%s); "+
			"otherwise the short-error-budget fix is a no-op",
		errorBodyFirstByteTimeout, configured)
}

// TestIdleReadCloser_FirstByteTimeout_Non2xxUsesShortBudget exercises the
// short first-byte budget end-to-end at the IdleReadCloser level. Production
// errorBodyFirstByteTimeout is 10s; tests shrink it to 50ms via t.Cleanup
// override so the suite stays sub-second. The IdleReadCloser MUST trip
// ErrFirstByteTimeout in ~the short budget, NOT the configured 600s — proving
// the helper is actually consulted at construction time.
func TestIdleReadCloser_FirstByteTimeout_Non2xxUsesShortBudget(t *testing.T) {
	prev := errorBodyFirstByteTimeout
	errorBodyFirstByteTimeout = 50 * time.Millisecond
	t.Cleanup(func() { errorBodyFirstByteTimeout = prev })

	// Mirror the production wiring: firstByteBudgetFor(503, 600s) = 50ms.
	budget := firstByteBudgetFor(http.StatusServiceUnavailable, 600*time.Second)
	require.Equal(t, 50*time.Millisecond, budget,
		"firstByteBudgetFor(503, 600s) must return the overridden short budget")

	body := newFakeReader(nil)
	r := NewIdleReadCloser(body, budget, 60*time.Second, context.Background())
	defer closeIdle(r)

	start := time.Now()
	_, err := r.Read(make([]byte, 16))
	elapsed := time.Since(start)

	require.ErrorIs(t, err, ErrFirstByteTimeout,
		"non-2xx wrapped body with the short first-byte budget must surface ErrFirstByteTimeout")
	// Elapsed must be on the order of the (test-shrunk) short budget, NOT the
	// configured 600s. Generous 3x upper bound absorbs CI scheduling jitter.
	assert.Less(t, elapsed, 3*budget,
		"non-2xx first-byte timeout must fire in ~%s (the short error budget); got %s; "+
			"if this approaches the configured 600s the short-error-budget fix is not wired",
		budget, elapsed)
}

// TestIdleReadCloser_ReadAllDrainsAndEOF verifies the end-to-end ReadAll path:
// a short multi-chunk stream concatenated by io.ReadAll produces the expected
// payload and terminates with io.EOF, and the reader goroutine exits cleanly.
func TestIdleReadCloser_ReadAllDrainsAndEOF(t *testing.T) {
	fr := newFakeReader([]fakeEvent{
		{delay: 0, data: []byte("aaa")},
		{delay: 5 * time.Millisecond, data: []byte("bbb")},
		{delay: 5 * time.Millisecond, data: []byte("ccc")},
		{delay: 0, err: io.EOF},
	})
	r := NewIdleReadCloser(fr, 500*time.Millisecond, 200*time.Millisecond, context.Background())
	defer closeIdle(r)

	out, err := io.ReadAll(r)
	require.NoError(t, err)
	assert.Equal(t, "aaabbbccc", string(out))
	assertExited(t, r)
}
