package protocols_test

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/yolorouter/yolorouter/internal/protocols"
)

// controlStreamDecoder turns each raw line into one DeltaText, or a DeltaDone
// when the line is the literal "DONE" — giving tests exact, deterministic
// control over how many deltas (and therefore how many emit()/Write calls)
// a stream produces, instead of depending on a real protocol codec's framing.
type controlStreamDecoder struct{}

func (controlStreamDecoder) DecodeChunk(line string) ([]protocols.IRStreamDelta, error) {
	line = strings.TrimSpace(line)
	if line == "" {
		return nil, nil
	}
	if line == "DONE" {
		return []protocols.IRStreamDelta{protocols.DeltaDone{StopReason: "stop"}}, nil
	}
	return []protocols.IRStreamDelta{protocols.DeltaText{Text: line}}, nil
}

func (controlStreamDecoder) Finish() ([]protocols.IRStreamDelta, error) { return nil, nil }

// controlStreamEncoder encodes each DeltaText into exactly one SSE data
// event carrying the text verbatim, so a test can predict precisely how many
// client-facing Write calls a given upstream body produces.
type controlStreamEncoder struct{}

func (controlStreamEncoder) EncodeDeltas(deltas []protocols.IRStreamDelta) []protocols.SSEEvent {
	var out []protocols.SSEEvent
	for _, d := range deltas {
		if t, ok := d.(protocols.DeltaText); ok {
			out = append(out, protocols.SSEEvent{Data: t.Text})
		}
	}
	return out
}

func (controlStreamEncoder) EncodeDone() []protocols.SSEEvent {
	return []protocols.SSEEvent{{Data: "[DONE]"}}
}

func (controlStreamEncoder) Usage() protocols.IRUsage { return protocols.IRUsage{} }

// recordingBuf is a minimal protocols.UpstreamBuffer that records every
// AppendResponse call, so tests can assert exactly which caller-facing bytes
// were captured — in particular, that a batch is captured only after BOTH
// the Write AND the Flush that carried it succeeded.
type recordingBuf struct {
	responses [][]byte
}

func (b *recordingBuf) AppendUpstream(data []byte)  {}
func (b *recordingBuf) SetBody(data []byte)         {}
func (b *recordingBuf) SetResponseBody(data []byte) {}
func (b *recordingBuf) AppendResponse(data []byte) {
	b.responses = append(b.responses, append([]byte(nil), data...))
}

// failAfterWriter is an http.ResponseWriter whose Write succeeds for the
// first (failAfter-1) calls, then fails on and after the failAfter'th call —
// simulating a client connection that dies partway through a stream (e.g.
// after the sliding write deadline expires).
type failAfterWriter struct {
	header    http.Header
	failAfter int
	calls     int
	written   [][]byte
}

func (w *failAfterWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *failAfterWriter) WriteHeader(int) {}

func (w *failAfterWriter) Write(b []byte) (int, error) {
	w.calls++
	if w.calls >= w.failAfter {
		return 0, errors.New("simulated downstream write failure")
	}
	w.written = append(w.written, append([]byte(nil), b...))
	return len(b), nil
}

// flushFailWriter is an http.ResponseWriter whose Write always succeeds
// (bytes are "buffered") but whose FlushError always fails — the net/http
// shape where a small Write returns nil before the real socket error
// surfaces on Flush. http.NewResponseController discovers FlushError through
// gin's Unwrap chain (see protocols.FlushAndCheckError).
type flushFailWriter struct {
	header  http.Header
	written [][]byte
}

func (w *flushFailWriter) Header() http.Header {
	if w.header == nil {
		w.header = make(http.Header)
	}
	return w.header
}

func (w *flushFailWriter) WriteHeader(int) {}

func (w *flushFailWriter) Write(b []byte) (int, error) {
	w.written = append(w.written, append([]byte(nil), b...))
	return len(b), nil
}

func (w *flushFailWriter) Flush() {}

func (w *flushFailWriter) FlushError() error {
	return errors.New("simulated flush failure")
}

// TestIRStreamRelay_WriteErrorClassifiedAsClientWrite verifies that a
// downstream Write failure inside IRStreamRelay's emit() is wrapped in
// protocols.ErrClientWrite (so the gateway's isClientWriteError classifier
// recognizes it as a client-side fault, not an upstream one) and that no
// bytes are captured for a write that never reached the client.
func TestIRStreamRelay_WriteErrorClassifiedAsClientWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fw := &failAfterWriter{failAfter: 1} // fails on the very first Write
	c, _ := gin.CreateTestContext(fw)
	c.Request, _ = http.NewRequest(http.MethodGet, "/x", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("hello\n")),
		Header:     http.Header{},
	}

	buf := &recordingBuf{}
	_, err := protocols.IRStreamRelay(protocols.NewGinClientWriter(c), resp, controlStreamDecoder{}, controlStreamEncoder{}, buf, nil, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, protocols.ErrClientWrite),
		"a downstream Write failure must be classified via protocols.ErrClientWrite, got %v", err)
	assert.Empty(t, buf.responses, "bytes that never reached the client must not be captured")
}

// TestIRStreamRelay_FlushErrorClassifiedAsClientWriteAndNotCaptured verifies
// Flush-failure handling: a Flush failure (Write succeeded, but the real
// delivery error only
// surfaces on Flush) is also classified via protocols.ErrClientWrite, AND —
// critically — the bytes are NOT captured to buf, since capture must only
// happen once both the Write and the Flush that carried it succeeded.
func TestIRStreamRelay_FlushErrorClassifiedAsClientWriteAndNotCaptured(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fw := &flushFailWriter{}
	c, _ := gin.CreateTestContext(fw)
	c.Request, _ = http.NewRequest(http.MethodGet, "/x", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("hello\n")),
		Header:     http.Header{},
	}

	buf := &recordingBuf{}
	_, err := protocols.IRStreamRelay(protocols.NewGinClientWriter(c), resp, controlStreamDecoder{}, controlStreamEncoder{}, buf, nil, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, protocols.ErrClientWrite),
		"a Flush failure must be classified via protocols.ErrClientWrite, got %v", err)
	assert.NotEmpty(t, fw.written, "the Write itself must have succeeded (buffered) for this scenario")
	assert.Empty(t, buf.responses,
		"bytes must not be captured when Flush failed — the client never actually received them")
}

// TestIRStreamRelay_CapturesOnlyAfterSuccessfulFlush is the positive control
// for the flush-failure fix: when both Write and Flush succeed, the bytes
// ARE captured, and
// in the same order they were written to the client.
func TestIRStreamRelay_CapturesOnlyAfterSuccessfulFlush(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/x", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("hello\nworld\nDONE\n")),
		Header:     http.Header{},
	}

	buf := &recordingBuf{}
	_, err := protocols.IRStreamRelay(protocols.NewGinClientWriter(c), resp, controlStreamDecoder{}, controlStreamEncoder{}, buf, nil, nil)
	require.NoError(t, err)
	require.NotEmpty(t, buf.responses)
	var captured strings.Builder
	for _, r := range buf.responses {
		captured.Write(r)
	}
	assert.Equal(t, w.Body.String(), captured.String(),
		"captured bytes must be byte-for-byte identical to what the client received")
}

// TestIRStreamRelayJSONLines_EmitFailureReturnsImmediately is a regression
// test for the emit-failure fix: on emit() failure, IRStreamRelayJSONLines
// used to fall
// through to the leftover-lineBuf and decoder.Finish() blocks, which would
// emit() AGAIN onto an already-dead connection. This test crafts two lines
// so the write for the second one fails, and verifies the writer sees
// EXACTLY two Write calls total (one success, one failure) — proving no
// further emit was attempted after the failure.
func TestIRStreamRelayJSONLines_EmitFailureReturnsImmediately(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fw := &failAfterWriter{failAfter: 2} // first line's Write succeeds, second fails
	c, _ := gin.CreateTestContext(fw)
	c.Request, _ = http.NewRequest(http.MethodGet, "/x", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("hello\nworld\n")),
		Header:     http.Header{},
	}

	buf := &recordingBuf{}
	_, err := protocols.IRStreamRelayJSONLines(protocols.NewGinClientWriter(c), resp, controlStreamDecoder{}, controlStreamEncoder{}, buf, nil, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, protocols.ErrClientWrite),
		"the write failure must be classified via protocols.ErrClientWrite, got %v", err)
	assert.Equal(t, 2, fw.calls,
		"expected exactly 2 Write attempts (1 success + 1 failure) — a fall-through to leftover/Finish "+
			"would have issued extra Write calls onto the already-failed connection")
	assert.Len(t, buf.responses, 1, "only the first (successfully written+flushed) line should be captured")
}

// TestIRStreamRelayJSONLines_FlushErrorNotCaptured mirrors
// TestIRStreamRelay_FlushErrorClassifiedAsClientWriteAndNotCaptured for the
// JSON-lines relay loop.
func TestIRStreamRelayJSONLines_FlushErrorNotCaptured(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fw := &flushFailWriter{}
	c, _ := gin.CreateTestContext(fw)
	c.Request, _ = http.NewRequest(http.MethodGet, "/x", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader("hello\n")),
		Header:     http.Header{},
	}

	buf := &recordingBuf{}
	_, err := protocols.IRStreamRelayJSONLines(protocols.NewGinClientWriter(c), resp, controlStreamDecoder{}, controlStreamEncoder{}, buf, nil, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, protocols.ErrClientWrite))
	assert.NotEmpty(t, fw.written)
	assert.Empty(t, buf.responses, "bytes must not be captured when Flush failed")
}

// stubResponseDecoder always decodes to a fixed IRResponse carrying the
// given usage, ignoring the input body — IRNonStreamRelay's write-error
// tests only need control over the DECODED result, not real protocol
// parsing.
type stubResponseDecoder struct{ usage protocols.IRUsage }

func (d stubResponseDecoder) DecodeResponse(json.RawMessage) (*protocols.IRResponse, error) {
	resp := protocols.NewIRResponse("resp-1", "real-model")
	resp.Content = "hi"
	resp.Usage = d.usage
	return resp, nil
}

// stubResponseEncoder encodes any IRResponse to a fixed JSON body — the
// write-error tests only care about how many bytes are written and whether
// the Write call fails, not the actual encoded shape.
type stubResponseEncoder struct{}

func (stubResponseEncoder) EncodeResponse(*protocols.IRResponse) json.RawMessage {
	return json.RawMessage(`{"ok":true}`)
}

// TestIRNonStreamRelay_2xxWriteErrorClassifiedAsClientWrite is the fix
// pin for IRNonStreamRelay's success path: before the fix, the final
// c.Writer.Write(encoded) call discarded its error entirely, so a downstream
// write failure after a fully-decoded 2xx response was indistinguishable
// from genuine delivery — the gateway caller would finalize it
// as a plain 2xx success. The Write error must instead be wrapped in
// protocols.ErrClientWrite, and the already-decoded usage must still be
// returned (the upstream was fully consumed regardless of delivery outcome,
// so billing must not silently drop to zero).
func TestIRNonStreamRelay_2xxWriteErrorClassifiedAsClientWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fw := &failAfterWriter{failAfter: 1} // fails on the one and only Write
	c, _ := gin.CreateTestContext(fw)
	c.Request, _ = http.NewRequest(http.MethodGet, "/x", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"anything":true}`)),
		Header:     http.Header{},
	}

	buf := &recordingBuf{}
	usage, err := protocols.IRNonStreamRelay(protocols.NewGinClientWriter(c), resp,
		stubResponseDecoder{usage: protocols.IRUsage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8}},
		stubResponseEncoder{}, buf, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, protocols.ErrClientWrite),
		"a downstream Write failure must be classified via protocols.ErrClientWrite, got %v", err)
	require.NotNil(t, usage, "usage must still be returned for billing even though delivery failed")
	assert.Equal(t, 5, usage.PromptTokens)
	assert.Equal(t, 3, usage.CompletionTokens)
}

// TestIRNonStreamRelay_NonStreamSuccessWriteSucceeds is the positive control
// for TestIRNonStreamRelay_2xxWriteErrorClassifiedAsClientWrite: when the
// write succeeds, IRNonStreamRelay returns a nil error and the usage
// unconditionally.
func TestIRNonStreamRelay_NonStreamSuccessWriteSucceeds(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/x", nil)

	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(`{"anything":true}`)),
		Header:     http.Header{},
	}

	buf := &recordingBuf{}
	usage, err := protocols.IRNonStreamRelay(protocols.NewGinClientWriter(c), resp,
		stubResponseDecoder{usage: protocols.IRUsage{PromptTokens: 5, CompletionTokens: 3, TotalTokens: 8}},
		stubResponseEncoder{}, buf, nil)
	require.NoError(t, err)
	require.NotNil(t, usage)
	assert.Equal(t, 5, usage.PromptTokens)
	assert.Equal(t, `{"ok":true}`, w.Body.String())
}

// TestIRNonStreamRelay_NonSuccessErrorBodyWriteErrorClassifiedAsClientWrite
// covers IRNonStreamRelay's non-2xx passthrough branch: forwarding an
// upstream error body to the client also has a raw c.Writer.Write call that
// previously discarded its error. A write failure there must likewise be
// wrapped in protocols.ErrClientWrite rather than silently reported as a
// clean pass-through (nil, nil).
func TestIRNonStreamRelay_NonSuccessErrorBodyWriteErrorClassifiedAsClientWrite(t *testing.T) {
	gin.SetMode(gin.TestMode)
	fw := &failAfterWriter{failAfter: 1}
	c, _ := gin.CreateTestContext(fw)
	c.Request, _ = http.NewRequest(http.MethodGet, "/x", nil)

	resp := &http.Response{
		StatusCode: http.StatusBadGateway,
		Body:       io.NopCloser(strings.NewReader(`{"error":"upstream failed"}`)),
		Header:     http.Header{},
	}

	buf := &recordingBuf{}
	usage, err := protocols.IRNonStreamRelay(protocols.NewGinClientWriter(c), resp, stubResponseDecoder{}, stubResponseEncoder{}, buf, nil)
	require.Error(t, err)
	assert.True(t, errors.Is(err, protocols.ErrClientWrite),
		"a downstream Write failure while forwarding an error body must be classified via protocols.ErrClientWrite, got %v", err)
	assert.Nil(t, usage, "no usage is expected on the error-body passthrough path")
}

// TestApplyStreamWriteDeadline_ErrorsOnUnsupportedWriter guards the surviving
// behaviour of ApplyStreamWriteDeadline after the protocol-layer cleanup
// removed its logging: the error from an unsupported writer (e.g.
// httptest.ResponseRecorder, which lacks SetWriteDeadline) must still be
// returned to the caller so tests and callers can observe the failure, even
// though the warn-once log that used to accompany it is gone (the protocol
// layer no longer depends on pkg/logger; the caller owns observability).
func TestApplyStreamWriteDeadline_ErrorsOnUnsupportedWriter(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder() // does not support SetWriteDeadline
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/x", nil)

	// Repeated per-chunk calls must each surface the failure identically —
	// that is what a real SSE stream's per-chunk ApplyStreamWriteDeadline
	// calls look like.
	for range 5 {
		err := protocols.ApplyStreamWriteDeadline(c)
		require.Error(t, err)
	}
}
