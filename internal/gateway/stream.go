package gateway

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"

	"github.com/gin-gonic/gin"
	"go.uber.org/zap"

	"github.com/yolorouter/yolorouter/internal/protocols"
	"github.com/yolorouter/yolorouter/pkg/logger"
)

// errClientDisconnected is returned by the stream pump when the caller's
// request context is cancelled. It is not a real upstream failure
// — the relay loop records it as a distinct outcome so the request log shows
// "caller cancelled", not "upstream failed".
var errClientDisconnected = errors.New("client disconnected")

// clientDisconnectOutcome maps a caller disconnect detected inside the stream
// pump to a pump result, keeping the post-[DONE] completion semantics in ONE
// place (both ctx-cancel exit paths — the in-loop select and the post-scan
// scanner.Err branch — call this, so they can never drift apart).
//
// A disconnect AFTER the `data: [DONE]` terminator was forwarded is NOT a real
// cancellation: the caller received the complete response and closed the
// connection. OpenAI SDKs close the moment they read [DONE], before the pump
// reaches the trailing blank line, so the very next ctx check fires — reporting
// 499 there would mislabel a fully-delivered stream as client_disconnected (the
// common case: most streams end this way). Only a disconnect BEFORE [DONE] is a
// genuine caller cancel (-> 499).
func clientDisconnectOutcome(usage *Usage, doneSeen bool) (*Usage, error) {
	if doneSeen {
		return usage, nil
	}
	return usage, errClientDisconnected
}

// errStreamNoDoneTerminator is returned when the upstream sent at least one
// data frame but closed the stream without the `data: [DONE]` terminator.
// The completion is silently truncated — the caller already received bytes
// (so the HTTP status stays 200), but the passthrough dispatch path logs a
// partial row, not clean success (stream-integrity fix).
var errStreamNoDoneTerminator = errors.New("upstream stream ended without [DONE] terminator")

// maxPreambleBytes caps the pre-first-data-frame preamble buffer in the
// passthrough stream pump — a malicious/buggy upstream could otherwise grow
// it without bound (the response body has no bodylimit guard the way the
// request body does).
const maxPreambleBytes = 64 * 1024

// maxStreamLineBytes caps a single SSE line. bufio.Reader.ReadBytes doesn't
// bound line length itself — without this a malicious/buggy upstream sending
// a very long line without a newline could grow the in-memory buffer without
// limit (the response body has no bodylimit guard the way the request does).
const maxStreamLineBytes = 1 * 1024 * 1024 // 1 MiB

// maxStreamBodyFileBytes is a HARD anti-OOM disk backstop, NOT a content
// truncation (the stream capture must record every SENT
// SSE fragment, never cut short for length). A normal chat stream never
// approaches this; only a hostile/buggy upstream maxing bandwidth within the
// 120s upstream timeout could. Hitting it sets stream_body_truncated=true
// (marked, never silent) and stops appending — the caller's own stream is
// unaffected either way. A package var (not const) so tests can inject a
// small value instead of actually writing 1GiB.
var maxStreamBodyFileBytes int64 = 1 << 30 // 1 GiB

// forwardStreamLine writes one SSE line and folds its outcome back onto rc
// (first-byte marker), the running usage pointer (final-frame tokens), and
// the [DONE] terminator flag. It returns the exact bytes written to the
// client (post model-rewrite, post usage-strip) so the caller can append
// the same caller-facing bytes to the stream capture file — never the raw
// pre-rewrite upstream line — and any Write error so the caller can stop the
// pump and classify the failure instead of silently dropping it (a swallowed
// downstream Write error left the sliding write-deadline protection
// unenforced on the production passthrough streaming path).
func forwardStreamLine(c *gin.Context, rc *RelayContext, line []byte, usage **Usage, doneSeen *bool) ([]byte, error) {
	wroteData, u, done, sent, writeErr := writeStreamLine(c.Writer, line, rc.OriginalModel, rc.WantsStreamUsage)
	if wroteData {
		rc.MarkFirstByteSent()
	}
	if u != nil {
		*usage = u
	}
	if done {
		*doneSeen = true
	}
	return sent, writeErr
}

func writeSSEHeader(c *gin.Context) {
	h := c.Writer.Header()
	h.Set("Content-Type", "text/event-stream")
	h.Set("Cache-Control", "no-cache")
	h.Set("Connection", "keep-alive")
	// Disable proxy buffering (nginx X-Accel-Buffering et al) so SSE chunks
	// reach the client token-by-token instead of in buffered batches.
	h.Set("X-Accel-Buffering", "no")
	c.Writer.WriteHeader(http.StatusOK)
}

func isDataLine(line []byte) bool {
	// SSE allows "data:" with or without a space after the colon — accept
	// both so a provider that omits the space isn't misclassified as a
	// preamble line.
	return bytes.HasPrefix(bytes.TrimRight(line, "\r\n"), []byte("data:"))
}

// writeStreamLine writes one SSE line to the client, rewriting the model
// field when the line is a `data: {json}` chunk. Returns wroteData=true if
// the line was a data line (counts toward the first-byte decision), the
// usage extracted from this chunk (the final usage chunk carries
// prompt/completion tokens), done=true if the line was the [DONE]
// terminator, sent = the exact bytes written to w (caller-facing,
// post-rewrite/post-usage-strip) — the stream body capture appends
// this, never the raw pre-rewrite input line — and writeErr, the error (if
// any) from the underlying Write. Every branch below still returns wroteData
// unconditionally (matching the pre-error-propagation behavior of this
// function): whether a data line's write actually landed on the wire is
// captured in writeErr, not in wroteData.
func writeStreamLine(w io.Writer, line []byte, externalModel string, keepUsage bool) (wroteData bool, usage *Usage, done bool, sent []byte, writeErr error) {
	trimmed := bytes.TrimRight(line, "\r\n")
	if !bytes.HasPrefix(trimmed, []byte("data:")) {
		// Non-data line (blank separator, event:/id:/retry: headers) —
		// forward verbatim so the SSE framing stays intact.
		_, err := w.Write(line)
		return false, nil, false, line, err
	}
	// SSE allows "data:" or "data: " — the optional single space after the
	// colon is framing, not part of the value.
	payload := bytes.TrimSpace(trimmed[len("data:"):])
	if len(payload) == 0 {
		_, err := w.Write(line)
		return true, nil, false, line, err
	}
	if string(payload) == "[DONE]" {
		out := []byte("data: [DONE]\n")
		_, err := w.Write(out)
		return true, nil, true, out, err
	}
	rewritten, u := rewriteStreamChunk(payload, externalModel, keepUsage)
	out := make([]byte, 0, len(rewritten)+len("data: ")+1)
	out = append(out, "data: "...)
	out = append(out, rewritten...)
	out = append(out, '\n')
	_, err := w.Write(out)
	return true, u, false, out, err
}

// rewriteStreamChunk rewrites the model field in one SSE data payload. If
// the payload isn't valid JSON it's forwarded unchanged — breaking the
// stream over one malformed chunk would punish the caller for an upstream
// quirk. usage is pulled out of the SAME already-decoded map (not via a
// second json.Unmarshal of the whole payload), so the streaming hot path
// parses each frame once for the rewrite plus one tiny sub-decode for usage.
//
// When keepUsage is false (caller did not request stream_options.include_usage
// but the gateway injected it upstream), the usage field is stripped from the
// forwarded payload — the gateway still returns the extracted usage for its
// own cost accounting, but does not forward it to the caller.
func rewriteStreamChunk(payload []byte, externalModel string, keepUsage bool) ([]byte, *Usage) {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(payload, &m); err != nil {
		return payload, nil
	}
	if m == nil {
		// payload was literal "null" — json.Unmarshal returns nil error but
		// leaves m nil, and writing m["model"] would panic on a nil map.
		// Forward unchanged (mirrors rewriteModelField's guard).
		return payload, nil
	}
	// Only rewrite model when the chunk actually carries one — don't inject
	// it into usage-only / ping frames that never had a model field.
	if _, ok := m["model"]; ok {
		if modelJSON, err := json.Marshal(externalModel); err == nil {
			m["model"] = modelJSON
		}
	}
	usage := usageFromRawMap(m)
	// Strip the usage field from forwarded frames unless the caller asked
	// for it (usage the gateway injected for its own cost
	// accounting is internal-only and must not be forwarded to a caller
	// that did not request stream_options.include_usage). The extracted
	// usage above is still returned for internal cost/budget accounting.
	if !keepUsage {
		delete(m, "usage")
	}
	rewritten, err := json.Marshal(m)
	if err != nil {
		return payload, nil
	}
	return rewritten, usage
}

// usageFromRawMap decodes just the "usage" sub-value out of an already-parsed
// SSE/JSON object map. Returns nil when there's no usage field — the relay
// loop treats nil as "unknown", never zero.
func usageFromRawMap(m map[string]json.RawMessage) *Usage {
	raw, ok := m["usage"]
	if !ok || len(raw) == 0 || string(raw) == "null" {
		return nil
	}
	var w wireUsage
	if err := json.Unmarshal(raw, &w); err != nil {
		return nil
	}
	// toUsage returns nil when prompt/completion counts are missing — a
	// partial usage frame must NOT be treated as known-zero.
	return w.toUsage()
}

// writeStreamErrorEvent writes one inline SSE error frame carrying an error,
// used when the upstream stream breaks AFTER the first byte has already gone
// to the client (can't switch, can't change status — only emit an inline
// error event and close). The caller has already verified the response is
// mid-stream.
//
// The wire shape is protocol-aware:
//   - Claude (/v1/messages): the Anthropic streaming error shape (event:
//     error + a {"type":"error",...} envelope), no [DONE] terminator
//     convention at all.
//   - Gemini (/v1beta/...): a bare `data: {"error":{code,message,status}}`
//     frame — Gemini's streamGenerateContent SSE has no [DONE] terminator
//     convention either (see gemini.StreamEncoder.EncodeDeltas/EncodeDone,
//     which only ever emit plain data frames), so none is appended.
//   - Responses (/v1/responses): a bare `data: {"type":"error",...}` frame —
//     the Responses SSE stream is framed as typed response.* events
//     (response.created/.../response.completed/response.failed), never
//     OpenAI Chat's chat.completion.chunk + `data: [DONE]` (see
//     responses.StreamDecoder, which recognizes "[DONE]" only as a tolerated
//     no-op, not the completion signal), so no [DONE] is appended here
//     either.
//   - Every other ingress (OpenAI Chat) keeps the shape this function has
//     always produced: a bare `data: {"error":...}` frame followed by
//     `data: [DONE]` so OpenAI SDKs blocked on [DONE] to finalize their
//     completion unblock promptly instead of hanging until their own read
//     timeout.
//
// Sending the OpenAI [DONE] convention to a Claude, Gemini, or Responses
// client would be a protocol violation none of those SDKs expect mid-stream.
//
// rc's stream capture file is still open at this point (the passthrough
// stream pump deliberately leaves it open past its own return, see
// openStreamBodyFile's call site) — every frame written here is also
// appended to it, or the persisted "sent stream chunks" capture would end
// one frame short of what the real client actually received. The caller
// closes the file once this function returns.
// This applies the sliding streamWriteWindow deadline BEFORE the first write
// (mirroring normal chunk forwarding in the stream pumps) so that a
// BodyIdleTimeout larger than streamWriteWindow cannot leave the write
// deadline already expired by the time the terminal error frame is written.
// Returns the first Write error encountered so callers can react (e.g. skip
// a subsequent [DONE]); callers that have already committed to finalization
// may simply discard it.
func writeStreamErrorEvent(c *gin.Context, rc *RelayContext) error {
	// Slide the per-write deadline before writing the terminal error frame,
	// exactly as normal chunk forwarding does. Without this, when
	// BodyIdleTimeout > streamWriteWindow, the last healthy chunk may have
	// set a deadline that already expired by the time the upstream breaks
	// and this function writes the error frame — the client would see a
	// bare EOF with no protocol error / [DONE], while the capture file
	// (which appended unconditionally in the old code) recorded bytes that
	// were never delivered.
	applyStreamWriteDeadline(c, rc.RequestID)
	msg := streamErrorMessage(rc.RequestID)
	switch rc.Ingress {
	case protocols.ProtocolClaude:
		return writeClaudeStreamErrorEvent(c, rc, msg)
	case protocols.ProtocolGemini:
		return writeGeminiStreamErrorEvent(c, rc, msg)
	case protocols.ProtocolResponses:
		return writeResponsesStreamErrorEvent(c, rc, msg)
	default:
		return writeOpenAIStreamErrorEvent(c, rc, msg)
	}
}

// streamWriteDeadlineWarnedKey gates the sliding-deadline warning to once per
// request: ApplyStreamWriteDeadline runs before every forwarded chunk, and on
// a writer without SetWriteDeadline support the error would otherwise spam the
// log. Production *http.response always supports it, so a warning means some
// wrapper disabled the slow-client protection.
const streamWriteDeadlineWarnedKey = "gateway_stream_write_deadline_warned"

// applyStreamWriteDeadline slides the per-write deadline and logs a warning
// (at most once per request) when the writer does not support SetWriteDeadline.
func applyStreamWriteDeadline(c *gin.Context, requestID string) {
	if err := protocols.ApplyStreamWriteDeadline(c); err != nil && !c.GetBool(streamWriteDeadlineWarnedKey) {
		c.Set(streamWriteDeadlineWarnedKey, true)
		logger.Warn("gateway: apply stream write deadline failed", zap.String("request_id", requestID), zap.Error(err))
	}
}

// streamErrorMessage builds the generic mid-stream failure message shared by
// every ingress protocol — no upstream detail is leaked, only the request id
// so the caller can quote it to support.
func streamErrorMessage(requestID string) string {
	return AppendRequestID("upstream stream interrupted", requestID)
}

// writeAndCaptureSSE writes one SSE frame to the client and appends the same
// caller-facing bytes to rc's stream capture file, keeping the two writes
// (live response, audit capture) from drifting apart at any call site that
// needs both.
//
// The capture file append runs ONLY after a successful Write AND a
// successful Flush — net/http may buffer a small Write and return nil
// without having pushed bytes onto the socket; the real delivery error
// surfaces only on Flush. flushAndCheckError unwraps through gin's
// responseWriter (which implements http.Flusher and would shadow the
// underlying writer's FlushError method) to call FlushError on the
// innermost writer. If either Write or Flush fails, the bytes are NOT
// appended to the capture file (the client never received them) and the
// error is returned so the caller can short-circuit subsequent frames.
func writeAndCaptureSSE(c *gin.Context, rc *RelayContext, b []byte) error {
	if _, err := c.Writer.Write(b); err != nil {
		return err
	}
	if err := flushAndCheckError(c); err != nil {
		return err
	}
	appendStreamBodyLine(rc, b)
	return nil
}

// flushAndCheckError flushes the response writer and returns any deferred
// write error. Thin wrapper around protocols.FlushAndCheckError: the
// canonical unwrap-and-flush implementation lives in internal/protocols
// (this package already depends on it, and protocols cannot depend back on
// this package), so the IR streaming relay loops and this package's
// same-protocol passthrough stream pumps share one implementation instead of
// drifting apart.
func flushAndCheckError(c *gin.Context) error {
	return protocols.FlushAndCheckError(c)
}

// writeOpenAIStreamErrorEvent writes the OpenAI-shaped mid-stream error: an
// inline `data: {"error":...}` frame followed by `data: [DONE]`. Returns the
// first Write error so the caller knows whether the terminal frame landed.
func writeOpenAIStreamErrorEvent(c *gin.Context, rc *RelayContext, msg string) error {
	evt := fmt.Sprintf(`data: {"error":{"message":%q,"type":"upstream_error"}}`+"\n\n", msg)
	if err := writeAndCaptureSSE(c, rc, []byte(evt)); err != nil {
		return err
	}
	// Terminate the stream so OpenAI SDK clients that block on [DONE] to
	// finalize their completion unblock promptly instead of hanging until
	// their own read timeout. Skip once the error frame itself failed —
	// the client is already gone.
	if err := writeAndCaptureSSE(c, rc, []byte("data: [DONE]\n\n")); err != nil {
		return err
	}
	return nil
}

// writeClaudeStreamErrorEvent writes the Anthropic-shaped mid-stream error:
// a single `event: error` SSE event carrying the Messages API error
// envelope. Claude has no [DONE] terminator convention — the Anthropic SDK
// treats the connection close right after this event as the end of the
// stream, so nothing further is written.
func writeClaudeStreamErrorEvent(c *gin.Context, rc *RelayContext, msg string) error {
	evt := protocols.SSEEvent{
		Event: "error",
		Data:  fmt.Sprintf(`{"type":"error","error":{"type":"api_error","message":%q}}`, msg),
	}
	if err := writeAndCaptureSSE(c, rc, []byte(evt.String())); err != nil {
		return err
	}
	return nil
}

// writeGeminiStreamErrorEvent writes the Gemini-shaped mid-stream error: a
// bare `data: {"error":{code,message,status}}` frame, with no [DONE]
// terminator (Gemini's SSE framing has no such convention at all — see this
// function's doc comment on writeStreamErrorEvent). The nested error uses
// http.StatusInternalServerError as its representative status: by this point
// the response's real HTTP status is already committed as 200 (headers went
// out with the first upstream chunk), so there is no meaningful per-request
// status left to report — 500/INTERNAL is the same "opaque upstream failure"
// choice writeClaudeStreamErrorEvent makes with its fixed api_error type.
func writeGeminiStreamErrorEvent(c *gin.Context, rc *RelayContext, msg string) error {
	const midStreamStatus = http.StatusInternalServerError
	evt := fmt.Sprintf(`data: {"error":{"code":%d,"message":%q,"status":%q}}`+"\n\n",
		midStreamStatus, msg, geminiErrorStatus(midStreamStatus))
	if err := writeAndCaptureSSE(c, rc, []byte(evt)); err != nil {
		return err
	}
	return nil
}

// writeResponsesStreamErrorEvent writes the Responses-shaped mid-stream
// error: a bare `data: {"type":"error",...}` frame matching the top-level
// error event shape responses.StreamDecoder itself recognizes on the decode
// side (see its "case \"error\":" branch), with no [DONE] terminator — the
// Responses SSE stream is framed as typed response.* events, never OpenAI
// Chat's [DONE] sentinel (see this function's doc comment on
// writeStreamErrorEvent).
func writeResponsesStreamErrorEvent(c *gin.Context, rc *RelayContext, msg string) error {
	evt := fmt.Sprintf(`data: {"type":"error","message":%q,"code":null,"param":null}`+"\n\n", msg)
	if err := writeAndCaptureSSE(c, rc, []byte(evt)); err != nil {
		return err
	}
	return nil
}

// isClientWriteError reports whether err represents a downstream (client-side)
// write failure rather than an upstream server fault. The streaming relay
// loops (IRStreamRelay / IRStreamRelayJSONLines) and the same-protocol
// passthrough pumps wrap every downstream Write/Flush error with
// protocols.ErrClientWrite; this classifier matches ONLY that sentinel, so a
// slow or gone client is not misclassified as AttemptServerError.
//
// Only the dedicated sentinel is used for classification — neither a broad
// net.Error.Timeout() check nor the bare syscalls (EPIPE / ECONNRESET /
// io.ErrClosedPipe) are matched here. Both are ambiguous on the READ side:
// an upstream RST while reading the response body surfaces through the exact
// same syscalls, and context.DeadlineExceeded (upstream attempt timeout)
// also satisfies net.Error.Timeout(). Only a write-site error explicitly
// wrapped in ErrClientWrite is unambiguous.
//
// The error reaches here wrapped (e.g. "stream write failed: ErrClientWrite:
// write to client: <root>") via fmt.Errorf("...: %w", ...) chains; errors.As
// / errors.Is unwrap through all layers.
func isClientWriteError(err error) bool {
	if err == nil {
		return false
	}
	return errors.Is(err, protocols.ErrClientWrite)
}

// openStreamBodyFile opens (create+append) the per-request stream capture
// file under the configured bodies directory (data/bodies/<request_id>.stream).
// Failure to resolve the directory or open the file is
// NOT fatal to the request — the caller's own SSE stream is completely
// unaffected either way; only the audit capture is skipped (and, for a real
// OS-level open error, logged so an unwritable data/bodies dir is visible
// in ops logs instead of silently dropping every stream body forever).
func openStreamBodyFile(c *gin.Context, rc *RelayContext) {
	if rc.RequestID == "" {
		return
	}
	dir := streamBodiesDir(c)
	if dir == "" {
		return
	}
	path := filepath.Join(dir, rc.RequestID+".stream")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		logger.Warn("gateway: open stream body file failed", zap.String("request_id", rc.RequestID), zap.Error(err))
		return
	}
	rc.streamBodyFile = f
	rc.streamBodyCaptured = true
	// A pre-first-byte failover can re-enter this function for the SAME
	// RequestID (same file, O_APPEND) after an earlier attempt already wrote
	// some preamble bytes to it — one Stat() here (not per appended line,
	// see appendStreamBodyLine) seeds the in-memory backstop counter with
	// the file's real starting size so it never drifts out of sync with what
	// prior attempts already wrote.
	if info, statErr := f.Stat(); statErr == nil {
		rc.streamBodyBytesWritten = info.Size()
	}
}

// closeStreamBodyFile closes the stream capture file opened by
// openStreamBodyFile, if any. Safe to call even when open never succeeded.
func closeStreamBodyFile(rc *RelayContext) {
	if rc.streamBodyFile != nil {
		_ = rc.streamBodyFile.Close()
		rc.streamBodyFile = nil
	}
}

// appendStreamBodyLine appends one already-caller-facing SSE line to the
// request's stream capture file, verbatim (v0.1 does not scrub body content).
// A no-op once the file was never opened (bodies dir unresolved / open failed)
// or the 1GiB backstop already fired for this request.
//
// maxStreamBodyFileBytes is a HARD anti-OOM disk backstop, NOT a content
// truncation (the capture must record every sent SSE
// fragment, never cut short for length) — a normal chat stream never comes
// close to it; only a hostile/buggy upstream maxing bandwidth within the
// 120s upstream timeout could. Hitting it sets rc.streamBodyTruncated=true
// (marked, never silent) and stops appending; the caller's own stream is
// entirely unaffected.
func appendStreamBodyLine(rc *RelayContext, line []byte) {
	if rc.streamBodyFile == nil || rc.streamBodyTruncated || len(line) == 0 {
		return
	}
	// rc.streamBodyBytesWritten tracks the file's size purely in memory
	// (initialized once from a real Stat() in openStreamBodyFile) — checking
	// the backstop this way, instead of Stat()-ing the file on every single
	// appended line, turns what could be hundreds of syscalls per stream
	// into zero.
	if rc.streamBodyBytesWritten+int64(len(line)) > maxStreamBodyFileBytes {
		rc.streamBodyTruncated = true
		return
	}
	n, err := rc.streamBodyFile.Write(line)
	rc.streamBodyBytesWritten += int64(n)
	if err != nil {
		logger.Warn("gateway: write stream body failed", zap.String("request_id", rc.RequestID), zap.Error(err))
	}
}

// removeEmptyStreamBodyFile deletes the stream capture file for rc if it
// ended up completely empty — the stream failed before any data ever
// reached the caller (a pre-first-byte candidate failover, or a caller
// disconnect before the first byte forwarded). Left in place, an empty
// stream_body_path would show as an empty, useless "stream body" link on
// the request-log detail page. A no-op when nothing was ever captured
// (streamBodyCaptured == false, e.g. bodies_dir was never resolved) or the
// file legitimately has content — streamBodyTruncated in particular guards
// against ever discarding a backstop-marked file, even though in practice
// that flag never fires on an empty file (it only fires after content was
// already appended).
func removeEmptyStreamBodyFile(c *gin.Context, rc *RelayContext) {
	if !rc.streamBodyCaptured || rc.streamBodyTruncated {
		return
	}
	dir := streamBodiesDir(c)
	if dir == "" {
		return
	}
	path := filepath.Join(dir, rc.RequestID+".stream")
	info, err := os.Stat(path)
	if err != nil || info.Size() != 0 {
		return
	}
	// Close the fd BEFORE unlinking: leaving it open
	// past os.Remove would let any later appendStreamBodyLine write to an
	// unlinked inode (bytes silently lost), and on Windows os.Remove of an
	// open file fails outright, leaving a stale empty stream_body_path. This
	// also nils rc.streamBodyFile so the caller's deferred closeStreamBodyFile
	// is a no-op.
	closeStreamBodyFile(rc)
	_ = os.Remove(path)
	rc.streamBodyCaptured = false
}

// BodiesDirContextKey is the gin.Context key under which a router-level
// middleware stashes the absolute data/bodies/ directory for every gateway
// request. Exported and shared so the setter (internal/router/router.go) and
// this reader can't silently drift apart into two mismatched string literals
// that would disable stream capture with no error.
const BodiesDirContextKey = "bodies_dir"

// streamBodiesDir resolves the absolute data/bodies/ directory for this
// request. The gateway package has no direct access to app config (importing
// it would create a cycle back through cmd/config's dependents); instead
// serve.go resolves the absolute path once at boot, and a router-level
// middleware stashes it on every request's gin.Context under
// BodiesDirContextKey (internal/router/router.go). Empty means unresolved
// (e.g. a test gin.Context built without that middleware) — stream capture
// is silently skipped in that case; the caller's stream itself is never
// affected.
func streamBodiesDir(c *gin.Context) string {
	return c.GetString(BodiesDirContextKey)
}
