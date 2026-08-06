package claude

import (
	"encoding/json"
	"testing"

	"github.com/yolorouter/yolorouter/internal/protocols"
)

// The provider runs web searches on its own initiative and charges for them
// separately from tokens. The count arrives exactly once, inside the usage
// object, and it is the only evidence the searches happened: by the time
// anything downstream could want it, the response body is gone.
//
// Both usage shapes carry it, so both decoders have to read it. The streaming
// one did from the start; the whole-response one did not, and nothing noticed
// because no test anywhere named the field. These do.

func TestAWholeResponseReportsTheSearchesTheProviderRan(t *testing.T) {
	body := json.RawMessage(`{
		"id": "msg_1",
		"model": "claude-sonnet-4-5",
		"stop_reason": "end_turn",
		"content": [{"type": "text", "text": "ok"}],
		"usage": {
			"input_tokens": 10,
			"output_tokens": 5,
			"server_tool_use": {"web_search_requests": 3, "web_fetch_requests": 9}
		}
	}`)

	irResp, err := ResponseDecoder{}.DecodeResponse(body)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if irResp.Usage.WebSearchCount != 3 {
		t.Errorf("WebSearchCount = %d, want 3: the provider billed for three searches and this "+
			"is the only place that number is ever stated", irResp.Usage.WebSearchCount)
	}
	// The sibling count is deliberately not carried: there is no IR field for
	// it. Asserting the tokens survived alongside is what keeps a future
	// implementer from "fixing" that by widening the wrong struct.
	if irResp.Usage.PromptTokens != 10 || irResp.Usage.CompletionTokens != 5 {
		t.Errorf("token counts = %d/%d, want 10/5", irResp.Usage.PromptTokens, irResp.Usage.CompletionTokens)
	}
}

// TestAResponseWithNoSearchesReportsNoneRatherThanFailing pins the absent case,
// which is the common one. A decoder that treats the missing key as an error,
// or that leaves a stale value behind, breaks every request that used no tools.
func TestAResponseWithNoSearchesReportsNoneRatherThanFailing(t *testing.T) {
	body := json.RawMessage(`{
		"id": "msg_2",
		"model": "claude-sonnet-4-5",
		"stop_reason": "end_turn",
		"content": [{"type": "text", "text": "ok"}],
		"usage": {"input_tokens": 10, "output_tokens": 5}
	}`)

	irResp, err := ResponseDecoder{}.DecodeResponse(body)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if irResp.Usage.WebSearchCount != 0 {
		t.Errorf("WebSearchCount = %d, want 0", irResp.Usage.WebSearchCount)
	}
}

// TestAStreamReportsTheSearchesItsFinalFrameStates is the streaming half of the
// same fact. It exists because the two decoders read the same JSON shape
// through the same type, and a change to that type has to be answered on both
// sides or one of them silently stops counting.
func TestAStreamReportsTheSearchesItsFinalFrameStates(t *testing.T) {
	d := NewStreamDecoder()

	if _, err := d.DecodeChunk(`data: {"type":"message_start","message":{"id":"msg_3","model":"claude-sonnet-4-5","usage":{"input_tokens":10,"output_tokens":0}}}`); err != nil {
		t.Fatalf("message_start: %v", err)
	}
	deltas, err := d.DecodeChunk(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5,"server_tool_use":{"web_search_requests":2}}}`)
	if err != nil {
		t.Fatalf("message_delta: %v", err)
	}

	found := false
	for _, delta := range deltas {
		if du, ok := delta.(protocols.DeltaUsage); ok {
			found = true
			if du.Usage.WebSearchCount != 2 {
				t.Errorf("WebSearchCount = %d, want 2", du.Usage.WebSearchCount)
			}
		}
	}
	if !found {
		t.Fatal("message_delta produced no usage delta to read the count from")
	}
}

// TestAMalformedSearchCountCostsOnlyTheSearchCount is the blast-radius test.
//
// The count is one optional extra inside the usage object. Reading it must not
// be able to take the usage down with it: typed onto the usage struct, a
// provider sending a string or an array there fails the enclosing Unmarshal,
// and the enclosing object holds the token counts and — on a stream — the stop
// reason. A frame lost that way settles the request with zero completion tokens
// and no finish reason, which is a billing loss caused entirely by trying to
// read a field nothing is required to send.
func TestAMalformedSearchCountCostsOnlyTheSearchCount(t *testing.T) {
	for _, shape := range []string{`"3"`, `[]`, `[1,2]`, `12.5`, `{"web_search_requests":"2"}`, `null`} {
		t.Run(shape, func(t *testing.T) {
			body := json.RawMessage(`{
				"id": "msg_x", "model": "claude-sonnet-4-5", "stop_reason": "end_turn",
				"content": [{"type": "text", "text": "ok"}],
				"usage": {"input_tokens": 11, "output_tokens": 7, "server_tool_use": ` + shape + `}
			}`)

			irResp, err := ResponseDecoder{}.DecodeResponse(body)
			if err != nil {
				t.Fatalf("the whole response failed to decode over one unreadable extra: %v", err)
			}
			if irResp.Usage.CompletionTokens != 7 || irResp.Usage.PromptTokens != 11 {
				t.Errorf("token counts = %d/%d, want 11/7: the counts the request is billed on "+
					"must survive a member that could not be read",
					irResp.Usage.PromptTokens, irResp.Usage.CompletionTokens)
			}
			if irResp.StopReason == "" {
				t.Error("the stop reason went down with the unreadable member")
			}
			if irResp.Usage.WebSearchCount != 0 {
				t.Errorf("WebSearchCount = %d, want 0: an unreadable shape is not a count",
					irResp.Usage.WebSearchCount)
			}
		})
	}
}

// TestAMalformedSearchCountDoesNotDropAStreamFrame is the streaming half, and
// the more expensive one: the frame that carries server_tool_use is the frame
// that carries the final token counts and the stop reason.
func TestAMalformedSearchCountDoesNotDropAStreamFrame(t *testing.T) {
	d := NewStreamDecoder()
	if _, err := d.DecodeChunk(`data: {"type":"message_start","message":{"id":"msg_y","model":"claude-sonnet-4-5","usage":{"input_tokens":11,"output_tokens":0}}}`); err != nil {
		t.Fatalf("message_start: %v", err)
	}
	deltas, err := d.DecodeChunk(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":100,"server_tool_use":{"web_search_requests":"2"}}}`)
	if err != nil {
		t.Fatalf("message_delta: %v", err)
	}

	var sawUsage, sawDone bool
	for _, delta := range deltas {
		switch v := delta.(type) {
		case protocols.DeltaUsage:
			sawUsage = true
			if v.Usage.CompletionTokens != 100 {
				t.Errorf("CompletionTokens = %d, want 100", v.Usage.CompletionTokens)
			}
			if v.Usage.WebSearchCount != 0 {
				t.Errorf("WebSearchCount = %d, want 0", v.Usage.WebSearchCount)
			}
		case protocols.DeltaDone:
			sawDone = true
		}
	}
	if !sawUsage {
		t.Error("the frame was dropped whole, so the request settles with no completion tokens")
	}
	if !sawDone {
		t.Error("the stop reason was dropped with the frame, so the request settles with no finish reason")
	}
}

// TestAnImpossibleSearchCountReachesTheCoherenceRulesOnBothPaths pins the one
// asymmetry that would let a stream launder a number a whole response rejects.
//
// A negative count cannot be true, and the coherence rules downstream exist to
// catch it — but only if it gets there. A decoder that quietly discards it
// hands on a record that reads as perfectly ordinary, and the request is billed
// on counts that arrived alongside an impossibility nobody can see any more.
// The two decoders must agree, and they must agree on keeping it.
func TestAnImpossibleSearchCountReachesTheCoherenceRulesOnBothPaths(t *testing.T) {
	body := json.RawMessage(`{
		"id": "msg_neg", "model": "claude-sonnet-4-5", "stop_reason": "end_turn",
		"content": [{"type": "text", "text": "ok"}],
		"usage": {"input_tokens": 10, "output_tokens": 5, "server_tool_use": {"web_search_requests": -1}}
	}`)
	whole, err := ResponseDecoder{}.DecodeResponse(body)
	if err != nil {
		t.Fatalf("DecodeResponse: %v", err)
	}
	if whole.Usage.WebSearchCount != -1 {
		t.Errorf("whole response kept %d, want -1: discarding it hides the contradiction",
			whole.Usage.WebSearchCount)
	}
	if !whole.Usage.Invalid {
		t.Error("a negative search count did not mark the record impossible")
	}

	d := NewStreamDecoder()
	if _, err := d.DecodeChunk(`data: {"type":"message_start","message":{"id":"msg_neg","model":"claude-sonnet-4-5","usage":{"input_tokens":10,"output_tokens":0}}}`); err != nil {
		t.Fatalf("message_start: %v", err)
	}
	deltas, err := d.DecodeChunk(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5,"server_tool_use":{"web_search_requests":-1}}}`)
	if err != nil {
		t.Fatalf("message_delta: %v", err)
	}
	for _, delta := range deltas {
		if du, ok := delta.(protocols.DeltaUsage); ok {
			if du.Usage.WebSearchCount != -1 {
				t.Errorf("stream kept %d, want -1: the two decoders disagree, and this is the "+
					"side that lets an impossible count through as ordinary",
					du.Usage.WebSearchCount)
			}
			return
		}
	}
	t.Fatal("the stream produced no usage delta")
}

// TestALaterFrameWithoutSearchesDoesNotEraseTheCountAlreadyReported pins the
// non-regression rule. The count is cumulative for the whole stream, so a frame
// that omits server_tool_use is saying nothing new — not saying zero. A decoder
// that overwrites unconditionally drops searches the provider already charged
// for whenever a stream carries more than one usage-bearing frame.
func TestALaterFrameWithoutSearchesDoesNotEraseTheCountAlreadyReported(t *testing.T) {
	d := NewStreamDecoder()

	if _, err := d.DecodeChunk(`data: {"type":"message_start","message":{"id":"msg_4","model":"claude-sonnet-4-5","usage":{"input_tokens":10,"output_tokens":0}}}`); err != nil {
		t.Fatalf("message_start: %v", err)
	}
	if _, err := d.DecodeChunk(`data: {"type":"message_delta","delta":{},"usage":{"output_tokens":3,"server_tool_use":{"web_search_requests":2}}}`); err != nil {
		t.Fatalf("first message_delta: %v", err)
	}
	deltas, err := d.DecodeChunk(`data: {"type":"message_delta","delta":{"stop_reason":"end_turn"},"usage":{"output_tokens":5}}`)
	if err != nil {
		t.Fatalf("second message_delta: %v", err)
	}

	for _, delta := range deltas {
		if du, ok := delta.(protocols.DeltaUsage); ok {
			if du.Usage.WebSearchCount != 2 {
				t.Errorf("WebSearchCount = %d, want 2: the second frame omitted the count, "+
					"which does not mean the searches stopped having happened", du.Usage.WebSearchCount)
			}
			return
		}
	}
	t.Fatal("second message_delta produced no usage delta")
}
