package protocols

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
)

// maxStreamBufSize caps how much upstream data a stream relay buffer retains,
// preventing an unbounded (or misbehaving) upstream stream from growing the
// buffer without limit.
const maxStreamBufSize = 2 * 1024 * 1024

// errStreamTruncated is returned by IRStreamRelay / IRStreamRelayJSONLines
// when the upstream stream ends (clean EOF, no transport error) without ever
// emitting a DeltaDone (sig.SawDone stays false) — e.g. an empty 200
// response or an upstream that closes mid-completion. Without this check the
// relay would emit the client's success terminator (EncodeDone) and call
// onFinish for a response the caller never actually finished receiving,
// wrongly recording success and billing. Every decoder's Finish() emits a
// DeltaDone fallback for a genuinely complete stream (claude
// response.go:314, gemini:383, responses:159), so this only fires for a
// truly incomplete stream.
var errStreamTruncated = errors.New("upstream stream ended before completion")

// maxIRResponseBytes caps a single non-stream cross-protocol upstream
// response body read by IRNonStreamRelay. A buggy or hostile provider can
// otherwise return an arbitrarily large body; without this cap
// io.ReadAll would grow the buffer until OOM before the request timeout
// fires (the response body has no bodylimit guard the way the request body
// does). Mirrors the gateway's same-protocol passthrough bound
// (maxNonStreamResponseBytes in internal/gateway/relay.go). A package var
// (not const) so tests can shrink it instead of buffering a real 32 MiB
// body.
var maxIRResponseBytes int64 = 32 * 1024 * 1024

// maxJSONLineBytes caps how large IRStreamRelayJSONLines's incomplete-line
// buffer (lineBuf) may grow while waiting for a newline. Without this bound,
// an upstream that sends bytes without ever emitting '\n' would grow lineBuf
// without limit. Comparable to the SSE scanner's per-line buffer cap
// (bufio.Scanner's 1 MiB max token size used elsewhere in this package). A
// package var (not const) so tests can shrink it instead of sending a real
// 1 MiB line.
var maxJSONLineBytes = 1 * 1024 * 1024

// allowedUpstreamHeaders is the allowlist of upstream response headers that the
// relay is permitted to forward to tenants. This uses an allowlist strategy:
// every unlisted header is dropped, preventing provider-internal details
// (organization IDs, server identifiers, diagnostic headers, rate-limit status,
// etc.) from leaking to tenants. Content-Type is deliberately excluded: each
// handler sets the correct Content-Type itself, so there's no need to copy it
// from upstream (this avoids duplicating or conflicting with the relay's own
// setting).
var allowedUpstreamHeaders = map[string]bool{
	"Cache-Control": true,
	"Retry-After":   true,
}

// hasMeaningfulUsage reports whether usage has been fully collected (any token
// field is non-zero). Combined with sawDone, this acts as a precondition for
// swallowing a ctx-canceled/EOF-race error as benign: under the OpenAI
// include_usage protocol, finish_reason can arrive before the standalone usage
// chunk does, and without this guard a request could be settled as successful
// in that window even though the usage chunk never arrived, undercharging the
// caller.
func hasMeaningfulUsage(u IRUsage) bool {
	return u.PromptTokens > 0 || u.CompletionTokens > 0 || u.TotalTokens > 0
}

// IsBenignPostDoneReadErr reports whether a read error is a benign trailing
// error that only appears after the upstream has already sent its terminal
// DONE signal. Callers must first verify sawDone=true before using this
// function — these same error codes indicate a genuine upstream interruption
// when DONE was never seen.
//
// Covers three cases observed in production:
//  1. context.Canceled — the client closes the connection before EOF,
//     canceling the request context; the HTTP transport returns ctx.Err()
//     from Read before it would otherwise have returned io.EOF.
//  2. io.ErrUnexpectedEOF — HTTP/2 transports often translate the tail of a
//     chunked stream into an unexpected EOF (rather than io.EOF) when the
//     client cancels; errors.Is can see through the transport's fmt.Errorf
//     wrapping.
//  3. "http2: response body closed" — some upstream providers close the
//     HTTP/2 stream non-standardly after sending [DONE] (RST_STREAM, or
//     dropping the connection outright), which causes the Go HTTP/2
//     transport to return this error on the next Read; the error is an
//     unexported net/http internal var, so it can only be matched by string.
//
// Exported so the relay layer's same-protocol passthrough path can reuse it,
// keeping the read-error allowlist consistent across all three streaming
// helpers.
func IsBenignPostDoneReadErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, io.ErrUnexpectedEOF) {
		return true
	}
	// "http2: response body closed" is an unexported net/http internal error,
	// so it cannot be matched with errors.Is.
	return strings.Contains(err.Error(), "http2: response body closed")
}

// UpstreamBuffer is a minimal interface for recording upstream response data.
type UpstreamBuffer interface {
	AppendUpstream(data []byte)
	SetBody(data []byte)
	// SetResponseBody records the caller-facing (post-IR-encode) bytes
	// actually written to the client — the cross-protocol counterpart of
	// SetBody, which only captures the raw pre-decode upstream body.
	// Without this, the caller-facing audit body (request_log_bodies.
	// response_body) stays empty for every cross-protocol non-stream
	// success, unlike the same-protocol passthrough path.
	SetResponseBody(data []byte)
}

// ClearWriteDeadline resets the current request's write deadline to zero
// (time.Time{}), lifting the global http.Server.WriteTimeout limit for
// **this streaming request** only. Non-streaming endpoints remain protected
// by the global WriteTimeout (non-streaming handlers never call this
// function).
//
// Known trade-off: once cleared, a slow-reading client has no write-idle
// ceiling and can in theory hold the connection open indefinitely, until the
// relay's overall context timeout kicks in as a backstop (default 600s for
// streaming, 180s for non-streaming). A complete fix — sliding idle timeout,
// Write error propagation, and failure settlement — would require touching
// every Write/Flush call across all four streaming helpers, which is
// billing-pipeline-level work and is left for a future iteration.
//
// In unit tests, *httptest.ResponseRecorder does not support
// SetWriteDeadline and returns ErrNotSupported; the production path
// (*http.response) always supports it. Requires Go 1.20+. **Callers must log
// a warning that includes the request ID** — silently swallowing this error
// would let the streaming WriteTimeout protection degrade into a no-op,
// making a recurrence of the same hard-cancel-after-N-seconds issue
// invisible.
func ClearWriteDeadline(c *gin.Context) error {
	rc := http.NewResponseController(c.Writer)
	return rc.SetWriteDeadline(time.Time{})
}

// WatchClientClose starts a watcher that immediately closes the upstream
// connection when the client disconnects or c.Request.Context() is canceled,
// letting a blocked scanner.Scan / Body.Read return right away. This avoids
// reading the entire upstream response to completion after the client has
// already left (wasting tokens and billing the user for content the client
// never receives).
//
// Key detail: this must call c.Writer.CloseNotify() — a Go HTTP/1.x server
// does not actively detect a half-closed client connection while a handler
// is running by default; calling CloseNotify() has the side effect of
// activating net/http's backgroundRead goroutine, which is what actually
// propagates a client FIN/RST to c.Request.Context().
//
// Returns stop(); callers should defer stop() so the watcher exits cleanly
// on normal completion without closing the upstream connection.
func WatchClientClose(c *gin.Context, upstream io.Closer) (stop func()) {
	done := make(chan struct{})
	var closeNotify <-chan bool
	// CloseNotify is deprecated, but **calling it** is itself the necessary
	// side effect: it activates net/http's backgroundRead goroutine, which is
	// what propagates a client FIN to c.Request.Context(). Watching
	// ctx.Done() alone without calling CloseNotify does not work under
	// HTTP/1.x.
	//
	// Gin's ResponseWriter interface explicitly declares that it implements
	// http.CloseNotifier, so the type assertion always succeeds; but it
	// delegates to the underlying http.ResponseWriter (in production,
	// *http.response; in unit tests, often *httptest.ResponseRecorder). The
	// latter doesn't necessarily implement CloseNotifier, in which case
	// calling CloseNotify() panics. The recover here guards against that in
	// unit tests without affecting the production path.
	func() {
		defer func() { _ = recover() }()
		//nolint:staticcheck // SA1019: CloseNotify is deprecated, but calling it is
		// the necessary side effect described above; there is no ctx.Done()-only
		// replacement under HTTP/1.x.
		if cn, ok := c.Writer.(http.CloseNotifier); ok {
			closeNotify = cn.CloseNotify()
		}
	}()
	go func() {
		select {
		case <-c.Request.Context().Done():
		case <-closeNotify:
		case <-done:
			return
		}
		_ = upstream.Close()
	}()
	var once sync.Once
	return func() { once.Do(func() { close(done) }) }
}

// IRStreamRelay proxies an upstream SSE stream through IR: decode -> encode.
// Returns partial usage even on upstream read errors.
//
// The client-facing SSE response headers (200 + text/event-stream) are
// DEFERRED until the first encoded event is actually about to be written —
// mirroring the same-protocol passthrough pump's (passthroughStreamToClient)
// deferred-header behavior. If the upstream ends (clean EOF) or errors
// before any event is ever emitted, this function returns an error WITHOUT
// having written anything to the client, so the caller can still fail over
// to a healthy candidate instead of being stuck with an already-committed
// empty 200.
//
// onFirstChunk fires once, at that same first-event point (pass nil to
// skip); used for TTFT (time-to-first-token) measurement and — via the
// caller — for marking the response as committed (no more failover).
// onFinish is called when the stream ends normally, with the raw finish
// reason, whether a tool call was seen, and whether any content was produced
// (pass nil to skip); used for finish_reason collection.
func IRStreamRelay(
	c *gin.Context,
	resp *http.Response,
	decoder StreamDecoder,
	encoder StreamEncoder,
	buf UpstreamBuffer,
	onFirstChunk func(),
	onFinish func(rawReason string, sawToolCall, produced bool),
) (*IRUsage, error) {
	defer func() { _ = resp.Body.Close() }()
	defer WatchClientClose(c, resp.Body)()
	// Note: ClearWriteDeadline is called by the caller (the relay layer),
	// which has logger context and can log a warning with the request ID on
	// failure, avoiding a silent error swallow that would degrade the
	// streaming WriteTimeout protection.

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)

	var sig FinishSignals
	// headerWritten gates both the deferred header write and onFirstChunk:
	// neither happens until there is at least one encoded event actually
	// about to be sent to the client. Calling c.Writer.Flush() (or Write)
	// before this point would implicitly commit a bare 200 with none of the
	// SSE headers set, so emit() must be the only place that touches the
	// writer before headerWritten flips true.
	headerWritten := false
	emit := func(events []SSEEvent) {
		if len(events) == 0 {
			return
		}
		if !headerWritten {
			c.Header("Content-Type", "text/event-stream")
			c.Header("Cache-Control", "no-cache")
			c.Header("Connection", "keep-alive")
			c.Header("X-Accel-Buffering", "no")
			c.Writer.WriteHeader(http.StatusOK)
			headerWritten = true
			if onFirstChunk != nil {
				onFirstChunk()
				onFirstChunk = nil
			}
		}
		for _, event := range events {
			_, _ = fmt.Fprint(c.Writer, event.String())
		}
		c.Writer.Flush()
	}

	for scanner.Scan() {
		line := scanner.Text()

		if buf != nil {
			buf.AppendUpstream(append([]byte(line), '\n'))
		}

		deltas, err := decoder.DecodeChunk(line + "\n")
		if err != nil {
			continue
		}

		sig.Accumulate(deltas)
		emit(encoder.EncodeDeltas(deltas))
	}

	// The decoder buffer must be flushed before checking scanner.Err: if the
	// upstream's last line ([DONE] / finish_reason) lacks a trailing blank
	// line (the OpenAI chat decoder only emits deltas from its buffer on
	// \n\n), sig.SawDone is still false at this point in the main loop. If
	// the scanner then errors with a ctx-canceled error and we return
	// immediately, a terminal frame that fully arrived would be
	// misclassified as a failed stream. Finish flushes the remaining buffer;
	// on the normal path, receiving DeltaDone sets sig.SawDone=true, which
	// lets a subsequent read error be recognized as "client closed after
	// receiving everything".
	deltas, finishErr := decoder.Finish()
	sig.Accumulate(deltas)
	if len(deltas) > 0 {
		emit(encoder.EncodeDeltas(deltas))
	}

	var scanErr error
	if err := scanner.Err(); err != nil {
		// Tightened exemption: settle as success only when sawDone
		// (DeltaDone was emitted) AND usage has been fully collected AND the
		// error belongs to the benign-trailing family. Under the OpenAI
		// include_usage protocol, finish_reason and the final usage chunk
		// arrive as **two separate SSE frames** — if the client disconnects
		// after finish_reason but before usage, relying on sawDone alone
		// would let a request with encoder.Usage() still zero be swallowed
		// and settled as success, undercharging the user and leaving
		// provider_cost missing. Only once usage is fully collected can we
		// be sure the billing fields have actually all arrived.
		u := encoder.Usage()
		//nolint:staticcheck // QF1001: kept as a positive "all three exemption
		// conditions hold" grouping to match the doc comment above; a De
		// Morgan'd form would obscure the exemption logic being described.
		if !(sig.SawDone && hasMeaningfulUsage(u) && IsBenignPostDoneReadErr(err)) {
			scanErr = fmt.Errorf("upstream stream read error: %w", err)
			return &u, scanErr
		}
	}

	// finishErr != nil means the upstream stream reported an explicit
	// failure inline (e.g. a response.failed / error event). In that case we
	// must **not** call EncodeDone() — that would write the client
	// protocol's "successful termination frame" (Claude message_stop / Chat
	// finish_reason=stop / Gemini STOP), and the client would treat a failed
	// request as a complete response. Leaving out the termination frame lets
	// the client SDK treat the stream as truncated, consistent with the
	// server settling this as a 502.
	//
	// sig.SawDone is required too: a clean EOF before any DeltaDone (an
	// empty 200 response, or an upstream that closes mid-completion without
	// an inline error) must not be synthesized into a success terminator
	// either — see errStreamTruncated's doc comment.
	terminalErr := finishErr
	if finishErr == nil && sig.SawDone {
		emit(encoder.EncodeDone())
		// Only notify the collection point when the stream ended normally
		// (no finishErr); a failed stream leaves finish_reason unset.
		if onFinish != nil {
			onFinish(sig.Raw, sig.SawToolCall, sig.Produced)
		}
	} else if finishErr == nil {
		terminalErr = errStreamTruncated
	}

	usage := encoder.Usage()
	return &usage, terminalErr
}

// IRNonStreamRelay proxies a non-streaming response through IR: decode -> encode.
//
// Behavior on errors:
//   - Upstream non-2xx: the raw body is passed through to the client
//     (standard error passthrough, no IR round trip), returning a nil error.
//   - Upstream 2xx but IR decode fails (including a status=failed / error
//     field): **nothing is written to the client**; decErr is returned so
//     the caller can decide to write a client-protocol-native error body,
//     rewrite the status code, and skip billing.
//   - Reading the body fails: an IO error is returned.
//
// This lets the caller correctly write a 502 with a client-protocol-formatted
// error body on a decode error, instead of the client ending up with both an
// upstream 200 OK and an unrecognized Responses JSON failure body (which
// client SDKs would treat as a 200).
// onFinish is called after a successful decode and response send, with the
// raw finish reason, whether a tool call occurred, and whether any content
// was produced (pass nil to skip).
func IRNonStreamRelay(
	c *gin.Context,
	resp *http.Response,
	decoder ResponseDecoder,
	encoder ResponseEncoder,
	buf UpstreamBuffer,
	onFinish func(rawReason string, sawToolCall, produced bool),
) (*IRUsage, error) {
	defer func() { _ = resp.Body.Close() }()

	// Read up to N+1 bytes so an overflow is detectable, then fail instead
	// of buffering an unbounded upstream body (see maxIRResponseBytes).
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxIRResponseBytes+1))
	if err != nil {
		return nil, fmt.Errorf("read upstream body: %w", err)
	}
	if int64(len(body)) > maxIRResponseBytes {
		return nil, fmt.Errorf("upstream response exceeds %d bytes", maxIRResponseBytes)
	}

	if buf != nil && resp.StatusCode >= 200 && resp.StatusCode < 300 {
		buf.SetBody(body)
	}

	// Non-2xx: the upstream error body is passed through (bypassing IR); the
	// caller routes billing decisions by status code alone.
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Preserve upstream Content-Type for error bodies so clients can parse them.
		// This is an explicit single-header copy, not subject to the whitelist policy
		// which targets success responses where the relay sets its own Content-Type.
		if ct := resp.Header.Get("Content-Type"); ct != "" {
			c.Header("Content-Type", ct)
		}
		CopyUpstreamHeaders(c, resp.Header)
		c.Writer.WriteHeader(resp.StatusCode)
		_, _ = c.Writer.Write(body)
		return nil, nil
	}

	// 2xx: must be validated through the IR decoder.
	irResp, decErr := decoder.DecodeResponse(json.RawMessage(body))
	if decErr != nil {
		// **Write nothing to the client** here: let the caller take the
		// "client-protocol-native 502 error" path. The raw body has already
		// been saved to buf (via buf.SetBody above), so operators can still
		// debug it. Even on failure, the decoder may return a partial
		// IRResponse (preserving wire.Usage), so provider cost / dispatch
		// analysis doesn't lose the information of how many tokens the
		// upstream consumed before failing.
		var partialUsage *IRUsage
		if irResp != nil {
			partialUsage = &irResp.Usage
		}
		return partialUsage, decErr
	}

	encoded := encoder.EncodeResponse(irResp)
	if buf != nil {
		// Caller-facing (post-IR-encode) bytes, mirroring SetBody's capture
		// of the raw pre-decode upstream body above.
		buf.SetResponseBody(encoded)
	}
	// The relay encodes the success body as JSON; set Content-Type explicitly because
	// Content-Type is excluded from the upstream header allowlist.
	c.Header("Content-Type", "application/json")
	CopyUpstreamHeaders(c, resp.Header)
	c.Writer.WriteHeader(resp.StatusCode)
	_, _ = c.Writer.Write(encoded)

	if irResp != nil {
		// Non-streaming success path: extract the finish_reason signal from
		// irResp. produced also accounts for ReasoningContent: a reasoning
		// model may only produce thinking with Content empty, and without
		// this check a normally completed thinking-only response would be
		// misclassified as empty.
		if onFinish != nil {
			sawToolCall := len(irResp.ToolCalls) > 0
			produced := irResp.Content != "" || irResp.ReasoningContent != "" || len(irResp.ToolCalls) > 0
			onFinish(irResp.StopReason, sawToolCall, produced)
		}
		return &irResp.Usage, nil
	}
	return nil, nil
}

// IRStreamRelayJSONLines proxies a Gemini-style JSON Lines stream through IR.
// Consistent with IRStreamRelay: an explicit in-stream upstream failure
// (decoder.Finish returns an error) or a non-EOF read error is propagated as
// an error, letting the caller rewrite the status code / skip
// RecordSuccess.
//
// Like IRStreamRelay, the client-facing SSE response headers are DEFERRED
// until the first encoded event is actually about to be written — see
// IRStreamRelay's doc comment for the full rationale (pre-first-event
// failover).
//
// onFirstChunk fires once, at that same first-event point (pass nil to
// skip); used for TTFT measurement.
// onFinish is called when the stream ends normally, with the raw finish
// reason, whether a tool call was seen, and whether any content was produced
// (pass nil to skip); used for finish_reason collection.
func IRStreamRelayJSONLines(
	c *gin.Context,
	resp *http.Response,
	decoder StreamDecoder,
	encoder StreamEncoder,
	buf UpstreamBuffer,
	onFirstChunk func(),
	onFinish func(rawReason string, sawToolCall, produced bool),
) (*IRUsage, error) {
	defer func() { _ = resp.Body.Close() }()
	defer WatchClientClose(c, resp.Body)()
	// Note: ClearWriteDeadline is called by the caller (the relay layer),
	// which has logger context and can log a warning with the request ID on
	// failure, avoiding a silent error swallow that would degrade the
	// streaming WriteTimeout protection.

	buf2 := make([]byte, 4096)
	var lineBuf []byte
	var rawReadErr error
	var sig FinishSignals

	// See IRStreamRelay's emit() for the full rationale: headers (and
	// onFirstChunk) are deferred until the first non-empty encoded events,
	// and emit() must be the only place that touches the writer before that
	// point (an unconditional Flush() before any Write/WriteHeader call
	// would implicitly commit a bare 200 with none of the SSE headers set).
	headerWritten := false
	emit := func(events []SSEEvent) {
		if len(events) == 0 {
			return
		}
		if !headerWritten {
			c.Header("Content-Type", "text/event-stream")
			c.Header("Cache-Control", "no-cache")
			c.Header("Connection", "keep-alive")
			c.Header("X-Accel-Buffering", "no")
			c.Writer.WriteHeader(http.StatusOK)
			headerWritten = true
			if onFirstChunk != nil {
				onFirstChunk()
				onFirstChunk = nil
			}
		}
		for _, event := range events {
			_, _ = c.Writer.Write([]byte(event.String()))
		}
		c.Writer.Flush()
	}

	for {
		n, err := resp.Body.Read(buf2)
		if n > 0 {
			lineBuf = append(lineBuf, buf2[:n]...)
			for {
				idx := bytes.IndexByte(lineBuf, '\n')
				if idx < 0 {
					break
				}
				line := string(lineBuf[:idx])
				lineBuf = lineBuf[idx+1:]

				if buf != nil {
					buf.AppendUpstream(append([]byte(line), '\n'))
				}

				deltas, decErr := decoder.DecodeChunk(line + "\n")
				if decErr != nil {
					continue
				}

				sig.Accumulate(deltas)
				emit(encoder.EncodeDeltas(deltas))
			}
			// The loop above drains every complete line out of lineBuf;
			// whatever remains is an incomplete tail still waiting for a
			// newline. Cap it so an upstream that sends bytes without ever
			// emitting '\n' can't grow lineBuf without bound.
			if len(lineBuf) > maxJSONLineBytes {
				rawReadErr = fmt.Errorf("upstream JSON-lines line exceeds %d bytes", maxJSONLineBytes)
				break
			}
		}
		if err != nil {
			rawReadErr = err
			break
		}
	}

	// Leftover lineBuf: the upstream's last line may lack a trailing newline
	// (observed with some Gemini streams that EOF directly); it must be
	// decoded first so sig.SawDone can pick up finishReason before deciding
	// whether the read error triggers a failure settlement.
	if len(lineBuf) > 0 {
		if buf != nil {
			buf.AppendUpstream(lineBuf)
		}
		deltas, _ := decoder.DecodeChunk(string(lineBuf) + "\n")
		sig.Accumulate(deltas)
		emit(encoder.EncodeDeltas(deltas))
	}

	// Finish is called before judging the read error: the decoder may still
	// have an internal buffer holding a termination signal.
	deltas, finishErr := decoder.Finish()
	sig.Accumulate(deltas)
	if len(deltas) > 0 {
		emit(encoder.EncodeDeltas(deltas))
	}

	// EOF is a normal end. When the client closes the connection only after
	// the upstream SSE has finished, resp.Body.Read gets
	// ctx.Err()=context.Canceled instead of io.EOF; as long as sawDone=true,
	// the upstream has already finished emitting finish_reason normally, so
	// this should settle as success. Any other non-EOF error is a genuine
	// upstream interruption. The loop only breaks when Read returns an
	// error (including io.EOF), so rawReadErr must be non-nil here. The
	// exemption condition mirrors IRStreamRelay: sawDone + usage fully
	// collected + error in the benign-trailing family.
	var readErr error
	//nolint:staticcheck // QF1001: kept as a positive "all three exemption
	// conditions hold" grouping to match the doc comment above; a De Morgan'd
	// form would obscure the exemption logic being described.
	if !errors.Is(rawReadErr, io.EOF) && !(sig.SawDone && hasMeaningfulUsage(encoder.Usage()) && IsBenignPostDoneReadErr(rawReadErr)) {
		readErr = fmt.Errorf("upstream JSON-lines read error: %w", rawReadErr)
	}

	// Mirrors IRStreamRelay: finishErr or readErr means the stream failed,
	// so EncodeDone must not be called — otherwise the client would see a
	// seemingly-complete termination frame, inconsistent with the server
	// settling this as a 502. sig.SawDone is also required: a clean EOF
	// before any DeltaDone must not be synthesized into a success
	// terminator either — see errStreamTruncated's doc comment.
	var terminalErr error
	switch {
	case readErr == nil && finishErr == nil && sig.SawDone:
		emit(encoder.EncodeDone())
		// Only notify the collection point when the stream ended normally
		// (no finishErr / readErr); a failed stream leaves finish_reason
		// unset.
		if onFinish != nil {
			onFinish(sig.Raw, sig.SawToolCall, sig.Produced)
		}
	case readErr != nil:
		terminalErr = readErr
	case finishErr != nil:
		terminalErr = finishErr
	default:
		// readErr == nil && finishErr == nil && !sig.SawDone
		terminalErr = errStreamTruncated
	}

	usage := encoder.Usage()
	return &usage, terminalErr
}

// CopyUpstreamHeaders copies the headers in the allowlist from the upstream
// response into the gin response. This uses an allowlist strategy to prevent
// provider-internal information (organization IDs, server version,
// diagnostic headers, rate-limit status, Set-Cookie, etc.) from leaking to
// tenants. Relay-layer compatibility headers (X-Provider, X-Request-Id,
// x-ratelimit-*, etc.) are injected by each handler itself and are never
// forwarded from upstream. Uses Add rather than Set to preserve the full
// semantics of multi-value headers (such as multiple Cache-Control
// directives).
func CopyUpstreamHeaders(c *gin.Context, header http.Header) {
	for k, vv := range header {
		if !allowedUpstreamHeaders[k] {
			continue
		}
		for _, v := range vv {
			c.Writer.Header().Add(k, v)
		}
	}
}
