package gateway

import (
	"context"
	"errors"
	"io"
	"sync"
	"time"
)

// ErrFirstByteTimeout is returned by IdleReadCloser.Read when no data arrives
// within firstByteTimeout since the wrapper was opened.
//
// This covers the reasoning-model pattern: a provider flushes a 200 response
// header, then silently thinks for minutes before emitting the first token.
// transport.ResponseHeaderTimeout cannot cover this gap because it stops
// ticking as soon as the header arrives.
var ErrFirstByteTimeout = errors.New("first byte timeout")

// ErrIdleTimeout is returned by IdleReadCloser.Read when the inter-chunk gap
// after the first byte exceeds the idle budget (analogous to nginx
// proxy_read_timeout).
var ErrIdleTimeout = errors.New("idle timeout between chunks")

// readResult carries the outcome of one underlying body.Read call from the
// reader goroutine to the consumer Read loop. When err is non-nil the chunk
// is terminal (io.EOF, IO error, or ctx-canceled read).
type readResult struct {
	buf []byte
	err error
}

// IdleReadCloser wraps an upstream response body with two-phase idle
// enforcement:
//
//   - firstByteTimeout: open -> first chunk. Reasoning models often flush a
//     200 header and then silently think for minutes before emitting the
//     first token; transport.ResponseHeaderTimeout cannot cover this gap
//     because it stops ticking once the header arrives.
//   - idle: inter-chunk gap after the first chunk (nginx proxy_read_timeout).
//
// ctx cancellation cuts the stream and Read returns ctx.Err() (Canceled on
// client disconnect, DeadlineExceeded on attempt/request timeout).
//
// Concurrency model:
//
//   - A background reader goroutine performs the blocking body.Read and sends
//     each result on chunkCh. Sends use select-with-done so Close can unblock
//     a pending send when the consumer has stopped reading.
//   - The main Read selects on chunkCh / timer.C / ctx.Done(). The timer
//     starts at firstByteTimeout and is Reset to idle after the first chunk,
//     then reset on every subsequent chunk.
//   - Close is idempotent (sync.Once): it closes done (unblocking the reader
//     goroutine's send) and also closes body (unblocking body.Read itself).
//     This double-release guarantees the reader goroutine always exits.
//
// Four exit paths all converge on Close() for cleanup:
//
//   - EOF / underlying error -> surfaced as-is (cached in lastErr)
//   - firstByteTimeout fired -> ErrFirstByteTimeout
//   - idle fired -> ErrIdleTimeout
//   - ctx canceled / deadline -> ctx.Err()
//
// All four are terminal: Read caches the error in lastErr and subsequent
// Reads return it without re-entering the select.
type IdleReadCloser struct {
	body             io.ReadCloser
	firstByteTimeout time.Duration
	idle             time.Duration
	ctx              context.Context

	// done is closed by Close to unblock the reader goroutine's send on
	// chunkCh when the consumer has stopped reading.
	done chan struct{}
	// chunkCh is buffered(1) so the reader goroutine can deliver one chunk
	// ahead of the consumer's Read without blocking.
	chunkCh chan readResult
	// exited is closed when the reader goroutine returns. It is the
	// observable signal for leak-safe tests: tests select on it instead of
	// comparing runtime.NumGoroutine() deltas.
	exited    chan struct{}
	closeOnce sync.Once

	// timer enforces both phases: firstByteTimeout until firstByteReceived,
	// then idle between subsequent chunks. Owned by Read (single-consumer).
	timer *time.Timer
	// firstByteReceived is flipped true after the first non-empty chunk
	// arrives; thereafter timer expiration means idle timeout, not first
	// byte timeout.
	firstByteReceived bool

	// leftover carries bytes from a chunk larger than the caller's buffer.
	// Drained on the next Read before re-entering the select. Owned by Read.
	leftover []byte
	// lastErr caches the terminal error once EOF / timeout / ctx-cancel
	// surfaces. Subsequent Reads return (0, lastErr).
	lastErr error
}

// errorBodyBudget is the single 10s budget shared by both
// errorBodyFirstByteTimeout and errorBodyTotalBudget below, so the two never
// drift apart (one accidentally changed without the other) — they express
// the same "error bodies are diagnostic-only, small, and must not stall
// failover" rationale, just applied at two different points (first-byte vs.
// total read time). See errorBodyFirstByteTimeout's and errorBodyTotalBudget's
// own doc comments for what each individually bounds.
const errorBodyBudget = 10 * time.Second

// errorBodyFirstByteTimeout is the first-byte budget used when wrapping the
// body of a non-2xx upstream response (any status outside 200-299, including
// 3xx redirects and 101 protocol switches).
//
// A retryable 503/429 response that flushes the status header but then holds
// the chunked body open without emitting a single byte would otherwise block
// readErrorBody / readUpstreamErrorBody for the full firstByteTimeout (600s
// default) before the IdleReadCloser surfaces ErrFirstByteTimeout — burning
// 10 minutes per failed candidate and neutralizing the retry/failover loop's
// responsiveness. Error bodies are small by definition (a JSON error envelope
// or a short HTML page), so errorBodyBudget is generous for any healthy
// upstream while still bounding a stuck one. 2xx responses keep the
// configured firstByteTimeout so reasoning models that silently think for
// minutes before emitting the first token aren't killed early.
//
// 3xx and 101 are included because the gateway does not follow redirects or
// protocol switches, so they arrive as terminal error bodies at the relay
// layer and must not hang on a stuck body.
//
// Deliberately fixed, not exposed on config.GatewayConfig: unlike the other
// relay timeouts this is a diagnostic-only read bound, not a value with a
// legitimate deployment-specific setting — see the note on
// config.GatewayConfig.
//
// Declared as a var (not const) so tests can shrink it via t.Cleanup to
// keep the suite sub-second; production code never reassigns it.
var errorBodyFirstByteTimeout = errorBodyBudget

// errorBodyTotalBudget caps the TOTAL time spent reading a non-2xx upstream
// error body, independent of firstByteTimeout / idle. Without this, a
// hostile or buggy upstream that trickles one byte every interval shorter
// than the IdleReadCloser's idle budget could keep io.ReadAll alive for the
// full attempt_timeout (20m default) — a regression from the previous 120s
// request cap. Error bodies are diagnostic-only (a short JSON envelope or
// HTML page); errorBodyBudget total is ample for any healthy upstream while
// bounding a stuck one.
//
// Deliberately fixed, not exposed on config.GatewayConfig — same rationale
// as errorBodyFirstByteTimeout above.
//
// Declared as a var (not const) so tests can shrink it via t.Cleanup to
// keep the suite sub-second; production code never reassigns it.
var errorBodyTotalBudget = errorBodyBudget

// firstByteBudgetFor picks the IdleReadCloser first-byte timeout for a given
// upstream response. Any non-2xx status (including 3xx redirects, 4xx client
// errors, 5xx server errors, and 101 protocol switches) uses the short
// errorBodyFirstByteTimeout; 2xx responses keep the configured firstByteTimeout
// (reasoning-model coverage).
func firstByteBudgetFor(statusCode int, firstByteTimeout time.Duration) time.Duration {
	if statusCode < 200 || statusCode >= 300 {
		return errorBodyFirstByteTimeout
	}
	return firstByteTimeout
}

// NewIdleReadCloser wraps body with two-phase idle enforcement.
//
// Both firstByteTimeout and idle must be > 0 (the config layer enforces this
// via validateConfig). ctx carries cancellation / deadline that will surface
// as ctx.Err() from Read.
//
// The timer starts immediately so the firstByte budget measures wall-clock
// time from open (client.Do success) to first chunk, matching the
// "ResponseHeaderTimeout cannot cover this gap" requirement.
func NewIdleReadCloser(body io.ReadCloser, firstByteTimeout, idle time.Duration, ctx context.Context) *IdleReadCloser {
	r := &IdleReadCloser{
		body:             body,
		firstByteTimeout: firstByteTimeout,
		idle:             idle,
		ctx:              ctx,
		done:             make(chan struct{}),
		chunkCh:          make(chan readResult, 1),
		exited:           make(chan struct{}),
		timer:            time.NewTimer(firstByteTimeout),
	}
	go r.readLoop()
	return r
}

// readLoop bridges blocking body.Read to the select-friendly chunkCh.
//
// Exits when:
//   - body.Read returns a terminal error (io.EOF, IO error, ctx-canceled
//     read). If the final read also produced bytes they are delivered with
//     the error in a single result; otherwise the error is delivered alone.
//   - Close fires done. Either the pending send unblocks via select, or the
//     loop notices done on the next iteration. body.Close() (called by Close)
//     also unblocks body.Read itself.
//
// On exit it closes chunkCh (so Read observes reader termination) and exited
// (for leak-safe tests).
func (r *IdleReadCloser) readLoop() {
	defer close(r.exited)
	defer close(r.chunkCh)
	// Local buffer; bytes are copied before send so the next iteration can
	// reuse the underlying array without aliasing.
	buf := make([]byte, 32*1024)
	for {
		n, err := r.body.Read(buf)
		if n > 0 {
			dup := make([]byte, n)
			copy(dup, buf[:n])
			res := readResult{buf: dup, err: err}
			select {
			case r.chunkCh <- res:
			case <-r.done:
				return
			}
		}
		if err != nil {
			// Deliver a data-less terminal error separately so the
			// consumer Read loop observes the cause (io.EOF, etc.).
			if n == 0 {
				select {
				case r.chunkCh <- readResult{err: err}:
				case <-r.done:
				}
			}
			return
		}
		// n == 0 && err == nil: skip (rare; io.Reader contract discourages
		// this but does not forbid it). Loop and read again.
	}
}

// Exited returns a channel that is closed when the background reader goroutine
// has terminated. Tests select on it to assert the goroutine does not leak
// without relying on runtime.NumGoroutine() deltas.
func (r *IdleReadCloser) Exited() <-chan struct{} { return r.exited }

// Read implements io.Reader. It returns data from the underlying body but
// enforces firstByteTimeout (open -> first chunk) and idle (inter-chunk)
// budgets.
//
//   - On firstByteTimeout (before any data arrived) it returns ErrFirstByteTimeout.
//   - On idle timeout (after the first chunk) it returns ErrIdleTimeout.
//   - On ctx cancellation it returns ctx.Err() (Canceled / DeadlineExceeded).
//   - On underlying EOF / read error it returns the original error.
//
// Any of the above is terminal: lastErr caches it and subsequent Reads return
// (0, lastErr) without re-entering the select.
//
// Bytes from a chunk larger than the caller's buffer are buffered in leftover
// and served on subsequent Reads before re-entering the select. This keeps
// the idle timer counting wall-clock inter-chunk gaps rather than
// consumer-side drain time.
//
// The blocking select is preceded by a non-blocking drain of chunkCh: if a
// chunk is already queued (buffered 1), it is taken immediately without
// waiting on timer.C. This prevents the idle timer from firing while data is
// already buffered but the consumer was slow to call Read (healthy stream
// killed by consumer-backpressure time charged to the idle budget).
//
// On chunkCh closure (reader goroutine exited without delivering a terminal
// chunk), Read prefers ctx.Err() over the default io.EOF so a concurrent
// Close-while-ctx-cancelled surfaces as Canceled rather than masking as a
// clean EOF (client disconnect would otherwise be billed as success).
func (r *IdleReadCloser) Read(p []byte) (int, error) {
	if len(p) == 0 {
		return 0, nil
	}
	for {
		// ctx > leftover: honor ctx cancellation even when buffered bytes
		// remain. Without this check, a healthy stream canceled mid-drain
		// would deliver the leftover bytes on this Read and defer the
		// cancellation error to the next Read, masking a client disconnect
		// long enough for the relay layer to emit a success terminal frame
		// and bill a disconnected request.
		if len(r.leftover) > 0 {
			if cerr := r.ctx.Err(); cerr != nil {
				r.leftover = nil
				r.lastErr = cerr
				r.timer.Stop()
				return 0, cerr
			}
			n := copy(p, r.leftover)
			r.leftover = r.leftover[n:]
			return n, nil
		}
		// After a terminal error, Read keeps returning the cached error.
		if r.lastErr != nil {
			return 0, r.lastErr
		}

		// ctx > pre-check: before the non-blocking chunkCh drain, honor a
		// canceled ctx. Without this, a canceled ctx racing with a queued
		// chunk would land in the chunkCh case below and (because handleChunk
		// only prefers ctx.Err on res.err != nil) deliver the healthy chunk
		// instead of the cancellation — same masking class as the leftover
		// case above.
		if cerr := r.ctx.Err(); cerr != nil {
			r.lastErr = cerr
			r.timer.Stop()
			return 0, cerr
		}

		// Non-blocking pre-check: drain an already-queued chunk before
		// entering the blocking select. Without this, a chunk buffered in
		// chunkCh while the consumer was slow to call Read would race
		// against timer.C in the blocking select; when timer.C wins the
		// stream is killed with ErrIdleTimeout even though data is already
		// waiting — a false-positive idle kill.
		select {
		case res, ok := <-r.chunkCh:
			n, err, done := r.handleChunk(p, res, ok)
			if done {
				return n, err
			}
			continue
		default:
		}

		// Blocking select: chunkCh is empty; wait for ctx cancel, timer
		// expiration, or the next chunk from the reader goroutine.
		//
		// Priority order is ctx > chunk > timer. When the select observes
		// multiple cases ready simultaneously, Go picks one at random; the
		// explicit ctx.Err() checks inside the timer.C and chunkCh cases
		// ensure a client disconnect always surfaces as ctx.Err() (Canceled
		// / DeadlineExceeded) regardless of which case the runtime chooses.
		// Without this, a client disconnect racing with an idle tick could
		// return ErrIdleTimeout (502 + circuit failure) instead of the
		// correct 499 / canceled classification.
		select {
		case <-r.ctx.Done():
			r.lastErr = r.ctx.Err()
			r.timer.Stop()
			return 0, r.lastErr
		case <-r.timer.C:
			// ctx > timer: if ctx is already canceled, return ctx.Err instead
			// of ErrFirstByteTimeout / ErrIdleTimeout. This covers the race
			// where ctx.Done and timer.C are both ready at select time.
			if cerr := r.ctx.Err(); cerr != nil {
				r.lastErr = cerr
				return 0, r.lastErr
			}
			// timer fired but ctx still alive: re-check chunkCh non-blocking
			// before declaring a timeout. A chunk may have landed in chunkCh
			// between the pre-check above and the blocking select landing on
			// timer.C (Go's random pick may choose timer.C over chunkCh when
			// both are ready). Without this re-check a healthy stream with
			// data already queued would be misclassified as idle timeout.
			select {
			case res, ok := <-r.chunkCh:
				if cerr := r.ctx.Err(); cerr != nil {
					r.lastErr = cerr
					return 0, r.lastErr
				}
				n, err, done := r.handleChunk(p, res, ok)
				if done {
					return n, err
				}
				continue
			default:
			}
			if !r.firstByteReceived {
				r.lastErr = ErrFirstByteTimeout
			} else {
				r.lastErr = ErrIdleTimeout
			}
			return 0, r.lastErr
		case res, ok := <-r.chunkCh:
			// ctx > chunk: if ctx is already canceled, discard the queued
			// chunk and return ctx.Err. The client has disconnected; data
			// cannot be delivered and the request must be classified as
			// canceled rather than a successful chunk delivery.
			if cerr := r.ctx.Err(); cerr != nil {
				r.lastErr = cerr
				r.timer.Stop()
				return 0, r.lastErr
			}
			n, err, done := r.handleChunk(p, res, ok)
			if done {
				return n, err
			}
		}
	}
}

// handleChunk processes one readResult received from chunkCh (or the closure
// signal ok=false when the reader goroutine has exited). It mutates timer /
// leftover / lastErr / firstByteReceived state and returns (n, err, done):
//   - done=true: Read returns (n, err) immediately.
//   - done=false: zero-length non-terminal chunk; Read should loop and select
//     again (rare; io.Reader contract discourages (0, nil) but does not forbid it).
//
// Shared by the non-blocking pre-check and the blocking select in Read so the
// chunk handling logic (timer reset, leftover, terminal-error caching) stays
// in one place.
//
// On chunkCh closure (ok=false), Read prefers ctx.Err() over the default
// io.EOF. Without this, a concurrent Close-while-ctx-cancelled would mask a
// client disconnect as a clean EOF (causing the relay layer to emit a success
// terminal frame and bill for a disconnected request). ctx.Err() returns
// Canceled when the client disconnected and nil when Close was called without
// cancellation (handler-driven cleanup), preserving the EOF behavior for the
// non-race path.
func (r *IdleReadCloser) handleChunk(p []byte, res readResult, ok bool) (int, error, bool) {
	if !ok {
		// chunkCh closed: reader goroutine exited without delivering a
		// terminal chunk. Prefer ctx.Err() (Canceled on client disconnect)
		// over io.EOF so the failure is classified correctly.
		if cerr := r.ctx.Err(); cerr != nil {
			r.lastErr = cerr
		} else if r.lastErr == nil {
			r.lastErr = io.EOF
		}
		r.timer.Stop()
		return 0, r.lastErr, true
	}
	// Non-empty chunk: transition firstByte -> idle, or reset the idle
	// budget for the next inter-chunk gap.
	if len(res.buf) > 0 {
		if !r.firstByteReceived {
			r.firstByteReceived = true
		}
		// Go 1.23+ Reset semantics: Reset now drains any pending value
		// internally and rearms the timer, so the pre-1.23 Stop+drain
		// idiom is no longer needed. This guarantee requires Go >= 1.23;
		// go.mod pins 1.26.2, well above that floor.
		r.timer.Reset(r.idle)
	}
	n := copy(p, res.buf)
	r.leftover = res.buf[n:]
	if res.err != nil {
		// ctx cancellation > terminal chunk error: client disconnect / attempt
		// deadline may fire concurrently with body.Read, and the underlying
		// Read may unblock with a generic "closed body" error that surfaces
		// here as res.err before ctx.Done is observed in the select. Without
		// this priority a client disconnect would be cached as a benign
		// closed-body error and the relay layer would bill it as a provider
		// fault instead of 499 StatusClientClosedRequest — the same masking
		// class, now covered for the terminal-readResult path in addition
		// to the chunkCh-closure path.
		if cerr := r.ctx.Err(); cerr != nil {
			res.err = cerr
		}
		r.lastErr = res.err
		r.timer.Stop()
	}
	if n > 0 {
		return n, nil, true
	}
	if res.err != nil {
		// n == 0 with terminal err (e.g. bare io.EOF).
		return 0, res.err, true
	}
	// n == 0 && err == nil: loop again (rare).
	return 0, nil, false
}

// Close implements io.Closer. It is idempotent (sync.Once) and performs the
// "double-release":
//
//   - close(done) unblocks a reader goroutine waiting in select on the send.
//   - body.Close() unblocks a reader goroutine currently inside body.Read.
//
// The double-release guarantees the goroutine always exits even when chunkCh
// is full (consumer stopped reading) or body.Read never returns voluntarily.
//
// Read and Close must not be called concurrently (io.ReadCloser contract);
// the timer is owned by Read and not touched here so Close stays race-free.
func (r *IdleReadCloser) Close() error {
	r.closeOnce.Do(func() {
		close(r.done)
		_ = r.body.Close()
	})
	return nil
}
