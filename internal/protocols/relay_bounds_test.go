package protocols

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
)

// fakeResponseDecoder/fakeResponseEncoder are minimal stand-ins for
// TestIRNonStreamRelay_BoundsResponseBody below — the oversize-body bound is
// checked before the decoder/encoder are ever invoked, so their behavior is
// irrelevant to the test; they only need to satisfy the interfaces.
type fakeResponseDecoder struct{}

func (fakeResponseDecoder) DecodeResponse(json.RawMessage) (*IRResponse, error) {
	return &IRResponse{}, nil
}

type fakeResponseEncoder struct{}

func (fakeResponseEncoder) EncodeResponse(*IRResponse) json.RawMessage {
	return json.RawMessage(`{}`)
}

// TestIRNonStreamRelay_BoundsResponseBody is a regression test for the
// bounded-body fix: an
// unbounded io.ReadAll(resp.Body) let a hostile/buggy upstream exhaust
// gateway memory before the request timeout fires. maxIRResponseBytes is a
// package var precisely so a test can shrink it instead of buffering a real
// 32 MiB body. With the cap shrunk to a small value, an oversize upstream
// body must make IRNonStreamRelay return an error rather than buffering it
// all.
func TestIRNonStreamRelay_BoundsResponseBody(t *testing.T) {
	orig := maxIRResponseBytes
	maxIRResponseBytes = 16
	defer func() { maxIRResponseBytes = orig }()

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/x", nil)

	oversizeBody := strings.Repeat("a", int(maxIRResponseBytes)*4)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(oversizeBody)),
		Header:     http.Header{},
	}

	_, err := IRNonStreamRelay(NewGinClientWriter(c), resp, fakeResponseDecoder{}, fakeResponseEncoder{}, nil, nil)
	if err == nil {
		t.Fatal("expected an error for an upstream body exceeding maxIRResponseBytes, got nil")
	}
}

// fakeStreamDecoder/fakeStreamEncoder are minimal stand-ins for
// TestIRStreamRelayJSONLines_CapsIncompleteLineBuffer below — the line-buffer
// bound is enforced purely on the raw byte count before any line is ever
// decoded, so decoder/encoder behavior is irrelevant; they only need to
// satisfy the interfaces.
type fakeStreamDecoder struct{}

func (fakeStreamDecoder) DecodeChunk(string) ([]IRStreamDelta, error) { return nil, nil }
func (fakeStreamDecoder) Finish() ([]IRStreamDelta, error)            { return nil, nil }

type fakeStreamEncoder struct{}

func (fakeStreamEncoder) EncodeDeltas([]IRStreamDelta) []SSEEvent { return nil }
func (fakeStreamEncoder) EncodeDone() []SSEEvent                  { return nil }
func (fakeStreamEncoder) Usage() IRUsage                          { return IRUsage{} }

// TestIRStreamRelayJSONLines_CapsIncompleteLineBuffer is a regression test
// for the unbounded-lineBuf fix: lineBuf grew unboundedly when the upstream
// sent bytes without
// ever emitting a newline. maxJSONLineBytes is a package var precisely so a
// test can shrink it instead of sending a real 1 MiB line. With the cap
// shrunk to a small value, an upstream that never emits a newline must make
// the function return an error instead of growing memory without bound.
func TestIRStreamRelayJSONLines_CapsIncompleteLineBuffer(t *testing.T) {
	orig := maxJSONLineBytes
	maxJSONLineBytes = 64
	defer func() { maxJSONLineBytes = orig }()

	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request, _ = http.NewRequest(http.MethodGet, "/x", nil)

	// A body well past the (shrunk) cap that never contains a newline.
	huge := strings.Repeat("x", maxJSONLineBytes*4)
	resp := &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(huge)),
		Header:     http.Header{},
	}

	_, err := IRStreamRelayJSONLines(NewGinClientWriter(c), resp, fakeStreamDecoder{}, fakeStreamEncoder{}, nil, nil, nil)
	if err == nil {
		t.Fatal("expected a read error when the upstream never emits a newline past maxJSONLineBytes, got nil")
	}
}
