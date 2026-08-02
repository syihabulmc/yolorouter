package chat

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/yolorouter/yolorouter/internal/protocols"
)

// TestChatStreamEncoder_IncludeUsageEmitsUsageChunkBeforeDone is a
// regression test for FIX 6: for a cross-protocol OpenAI-ingress stream with
// stream_options.include_usage=true, the encoder merged DeltaUsage into
// e.usage but never emitted an SSE usage chunk before [DONE], so the caller
// got no usage. With IncludeUsage=true and a DeltaUsage carrying meaningful
// tokens, EncodeDone must emit a usage-bearing chunk before [DONE].
func TestChatStreamEncoder_IncludeUsageEmitsUsageChunkBeforeDone(t *testing.T) {
	enc := NewStreamEncoder()
	enc.IncludeUsage = true

	enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaMessageStart{ID: "msg1", Model: "gpt-4o"},
		protocols.DeltaText{Text: "hi"},
	})
	enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaUsage{Usage: protocols.IRUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}},
	})

	events := enc.EncodeDone()
	if len(events) != 2 {
		t.Fatalf("EncodeDone() returned %d events, want 2 (usage chunk + [DONE]): %+v", len(events), events)
	}
	usageEvent := events[0].Data
	if !strings.Contains(usageEvent, `"prompt_tokens":10`) ||
		!strings.Contains(usageEvent, `"completion_tokens":5`) ||
		!strings.Contains(usageEvent, `"total_tokens":15`) {
		t.Errorf("usage chunk missing expected token counts: %s", usageEvent)
	}
	if !strings.Contains(usageEvent, `"object":"chat.completion.chunk"`) {
		t.Errorf("usage chunk missing chat.completion.chunk envelope: %s", usageEvent)
	}
	if !strings.Contains(usageEvent, `"choices":[]`) {
		t.Errorf("usage chunk must have empty choices, matching OpenAI's own final usage frame: %s", usageEvent)
	}
	doneEvent := events[1].Data
	if doneEvent != "[DONE]" {
		t.Errorf("second event = %q, want the [DONE] terminator following the usage chunk", doneEvent)
	}
}

// TestChatStreamEncoder_NoIncludeUsageEmitsNoUsageChunk is the negative
// counterpart: when the caller did not request stream_options.include_usage
// (IncludeUsage=false, the default), EncodeDone must emit only [DONE], with
// no usage chunk — preserving the pre-fix behavior for every existing
// caller that doesn't set the field.
func TestChatStreamEncoder_NoIncludeUsageEmitsNoUsageChunk(t *testing.T) {
	enc := NewStreamEncoder()
	// IncludeUsage left at its zero value (false).

	enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaMessageStart{ID: "msg1", Model: "gpt-4o"},
		protocols.DeltaText{Text: "hi"},
	})
	enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaUsage{Usage: protocols.IRUsage{PromptTokens: 10, CompletionTokens: 5, TotalTokens: 15}},
	})

	events := enc.EncodeDone()
	if len(events) != 1 {
		t.Fatalf("EncodeDone() returned %d events, want 1 (just [DONE]): %+v", len(events), events)
	}
	if events[0].Data != "[DONE]" {
		t.Errorf("event = %q, want %q", events[0].Data, "[DONE]")
	}
}

// TestChatStreamEncoder_IncludeUsageWithoutUsageEmitsNoUsageChunk covers the
// guard against an empty (all-zero) usage chunk: IncludeUsage=true but the
// upstream never sent any DeltaUsage (or sent an all-zero one) must not
// produce a fabricated zero-usage chunk.
func TestChatStreamEncoder_IncludeUsageWithoutUsageEmitsNoUsageChunk(t *testing.T) {
	enc := NewStreamEncoder()
	enc.IncludeUsage = true

	enc.EncodeDeltas([]protocols.IRStreamDelta{
		protocols.DeltaMessageStart{ID: "msg1", Model: "gpt-4o"},
		protocols.DeltaText{Text: "hi"},
	})

	events := enc.EncodeDone()
	if len(events) != 1 {
		t.Fatalf("EncodeDone() returned %d events, want 1 (just [DONE], no usage ever collected): %+v", len(events), events)
	}
	if events[0].Data != "[DONE]" {
		t.Errorf("event = %q, want %q", events[0].Data, "[DONE]")
	}
}

// TestOpenAIWireUsage_AnthropicUpstreamEmitsGross covers the cross-protocol
// combination that used to be wrong: an Anthropic upstream reports a NET input
// (cache excluded), but OpenAI defines cached_tokens as a breakdown OF
// prompt_tokens. Forwarding the raw count dropped the whole cache portion and
// produced the spec violation cached_tokens > prompt_tokens.
func TestOpenAIWireUsage_AnthropicUpstreamEmitsGross(t *testing.T) {
	// 2 net input + 906 cache write + 36678 cache read, 123 output.
	usage := openAIWireUsage(protocols.IRUsage{
		PromptTokens:     2,
		CompletionTokens: 123,
		CacheWriteTokens: 906,
		CacheReadTokens:  36678,
		// CacheIncludedInPrompt false: the Anthropic convention.
	})

	if usage["prompt_tokens"] != 37586 {
		t.Errorf("prompt_tokens = %v, want 37586 (net 2 + write 906 + read 36678)", usage["prompt_tokens"])
	}
	if usage["total_tokens"] != 37709 {
		t.Errorf("total_tokens = %v, want 37709 (gross prompt + completion)", usage["total_tokens"])
	}
	details := usage["prompt_tokens_details"].(map[string]interface{})
	if details["cached_tokens"] != 36678 {
		t.Errorf("cached_tokens = %v, want 36678", details["cached_tokens"])
	}
	if details["cached_tokens"].(int) > usage["prompt_tokens"].(int) {
		t.Error("cached_tokens must never exceed prompt_tokens — OpenAI defines it as a subset")
	}
}

// TestOpenAIWireUsage_OpenAIUpstreamPassesThrough is the negative case: an
// upstream that already reports the gross convention must not have its counts
// inflated a second time.
func TestOpenAIWireUsage_OpenAIUpstreamPassesThrough(t *testing.T) {
	usage := openAIWireUsage(protocols.IRUsage{
		PromptTokens:          37586,
		CompletionTokens:      123,
		CacheReadTokens:       36678,
		CacheIncludedInPrompt: true,
	})
	if usage["prompt_tokens"] != 37586 {
		t.Errorf("prompt_tokens = %v, want 37586 unchanged", usage["prompt_tokens"])
	}
}

// TestOpenAIWireUsage_CacheWriteRoundTrips is the contract test for the
// non-standard cache-write alias: what this encoder emits must be exactly what
// this decoder reads back. Without the alias the 906 cache-write tokens are
// indistinguishable from fresh input on the far side of an OpenAI hop, and a
// downstream gateway bills them at the plain input rate.
//
// The round trip must also not double-count: prompt_tokens is gross (it
// already contains the write), so the decoded net input has to come back as 2,
// not 908.
func TestOpenAIWireUsage_CacheWriteRoundTrips(t *testing.T) {
	origin := protocols.IRUsage{
		PromptTokens:     2,
		CompletionTokens: 123,
		CacheWriteTokens: 906,
		CacheReadTokens:  36678,
	}

	body, err := json.Marshal(map[string]interface{}{
		"id":    "chatcmpl-roundtrip",
		"model": "claude-opus-4-8",
		"choices": []interface{}{map[string]interface{}{
			"index":         0,
			"message":       map[string]interface{}{"role": "assistant", "content": "hi"},
			"finish_reason": "stop",
		}},
		"usage": openAIWireUsage(origin),
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}

	decoded, err := ResponseDecoder{}.DecodeResponse(body)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	if got := decoded.Usage.CacheWriteTokens; got != 906 {
		t.Errorf("CacheWriteTokens = %d, want 906 — the alias did not survive the hop", got)
	}
	if got := decoded.Usage.CacheReadTokens; got != 36678 {
		t.Errorf("CacheReadTokens = %d, want 36678", got)
	}
	if got := decoded.Usage.NetPromptTokens(); got != origin.NetPromptTokens() {
		t.Errorf("NetPromptTokens after round trip = %d, want %d — the write portion must be subtracted, not counted as fresh input", got, origin.NetPromptTokens())
	}
}

// TestChatDecoder_OpenRouterNestedCacheWrite covers the spelling a real
// upstream uses. OpenRouter documents a cache-WRITE count nested inside
// prompt_tokens_details beside cached_tokens; reading only the top-level
// Anthropic-style alias left CacheWriteTokens at 0 for every OpenRouter
// request, so those tokens were billed at the plain input rate.
func TestChatDecoder_OpenRouterNestedCacheWrite(t *testing.T) {
	body := json.RawMessage(`{
		"id": "chatcmpl-or", "model": "anthropic/claude-opus-4.8",
		"choices": [{"index": 0, "message": {"role": "assistant", "content": "hi"}, "finish_reason": "stop"}],
		"usage": {
			"prompt_tokens": 194, "completion_tokens": 2, "total_tokens": 196,
			"prompt_tokens_details": {"cached_tokens": 0, "cache_write_tokens": 100, "audio_tokens": 0}
		}
	}`)

	resp, err := ResponseDecoder{}.DecodeResponse(body)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Usage.CacheWriteTokens != 100 {
		t.Errorf("CacheWriteTokens = %d, want 100 from prompt_tokens_details.cache_write_tokens",
			resp.Usage.CacheWriteTokens)
	}
	if got := resp.Usage.NetPromptTokens(); got != 94 {
		t.Errorf("NetPromptTokens() = %d, want 94 (194 gross - 100 write)", got)
	}
}

// TestChatDecoder_NestedCacheWriteWinsOverAlias pins the precedence rule. Both
// spellings name the same breakdown of prompt_tokens, so a decoder that added
// them would double-count; the documented nested field is the one that wins.
func TestChatDecoder_NestedCacheWriteWinsOverAlias(t *testing.T) {
	body := json.RawMessage(`{
		"id": "chatcmpl-both", "model": "m",
		"choices": [{"index": 0, "message": {"role": "assistant", "content": "hi"}, "finish_reason": "stop"}],
		"usage": {
			"prompt_tokens": 1000, "completion_tokens": 5, "total_tokens": 1005,
			"prompt_tokens_details": {"cache_write_tokens": 300},
			"cache_creation_input_tokens": 700
		}
	}`)

	resp, err := ResponseDecoder{}.DecodeResponse(body)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Usage.CacheWriteTokens != 300 {
		t.Errorf("CacheWriteTokens = %d, want 300 (nested wins; 1000 would mean the two were summed)",
			resp.Usage.CacheWriteTokens)
	}
}

// TestOpenAIWireUsage_EmitsNestedCacheWriteOnly locks the emission policy:
// strict out, liberal in. Sending both spellings would put the same number in
// the payload twice and invite a downstream to add them.
func TestOpenAIWireUsage_EmitsNestedCacheWriteOnly(t *testing.T) {
	usage := openAIWireUsage(protocols.IRUsage{
		PromptTokens:     2,
		CompletionTokens: 123,
		CacheWriteTokens: 906,
		CacheReadTokens:  36678,
	})

	if _, present := usage["cache_creation_input_tokens"]; present {
		t.Error("top-level alias must not be emitted alongside the nested field")
	}
	details, ok := usage["prompt_tokens_details"].(map[string]interface{})
	if !ok {
		t.Fatal("prompt_tokens_details missing")
	}
	if details["cache_write_tokens"] != 906 {
		t.Errorf("prompt_tokens_details.cache_write_tokens = %v, want 906", details["cache_write_tokens"])
	}
}

// TestChatStreamDecoder_NegativeUsageIsMarkedInvalid pins the streaming half of
// the coherence contract.
//
// The frame is deliberately still emitted, but flagged. Dropping it was the
// first attempt and it was not enough: IRUsage.Merge folds frames into one
// accumulated record, so a dropped frame leaves the PREVIOUS frame's counts
// standing, and a finish_reason arriving in the same malformed chunk still
// completes the stream — billing stale values that look perfectly coherent.
// Marking lets the verdict travel with the record instead.
func TestChatStreamDecoder_NegativeUsageIsMarkedInvalid(t *testing.T) {
	d := NewStreamDecoder()
	deltas, err := d.DecodeChunk(`data: {"id":"c","model":"m","choices":[],` +
		`"usage":{"prompt_tokens":100,"completion_tokens":5,"total_tokens":105,` +
		`"prompt_tokens_details":{"cache_write_tokens":-50}}}` + "\n\n")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	var found bool
	for _, delta := range deltas {
		du, ok := delta.(protocols.DeltaUsage)
		if !ok {
			continue
		}
		found = true
		if !du.Usage.Invalid {
			t.Error("a usage frame carrying a negative count must be marked Invalid")
		}
		// The mark must survive the accumulation every relay performs, or the
		// stale-counts problem it exists to solve comes straight back.
		var acc protocols.IRUsage
		acc.Merge(protocols.IRUsage{PromptTokens: 999, CompletionTokens: 9})
		acc.Merge(du.Usage)
		if !acc.Invalid {
			t.Error("Invalid must propagate through Merge, otherwise earlier counts stay billable")
		}
	}
	if !found {
		t.Fatal("expected the usage frame to be emitted (marked), not dropped")
	}
}

// TestChatStreamDecoder_CacheOnlyUsageChunkIsEmitted covers an upstream that
// splits usage across frames and sends one carrying only cache counts. Gating
// emission on prompt/completion being non-zero dropped that frame entirely, so
// the cache write never reached the IR and its tokens were billed as fresh
// input at the plain input rate.
func TestChatStreamDecoder_CacheOnlyUsageChunkIsEmitted(t *testing.T) {
	d := NewStreamDecoder()
	deltas, err := d.DecodeChunk(`data: {"id":"c","model":"m","choices":[],` +
		`"usage":{"prompt_tokens":0,"completion_tokens":0,` +
		`"prompt_tokens_details":{"cache_write_tokens":900}}}` + "\n\n")
	if err != nil {
		t.Fatalf("decode: %v", err)
	}

	for _, delta := range deltas {
		if du, ok := delta.(protocols.DeltaUsage); ok {
			if du.Usage.CacheWriteTokens != 900 {
				t.Errorf("CacheWriteTokens = %d, want 900", du.Usage.CacheWriteTokens)
			}
			return
		}
	}
	t.Fatal("a usage chunk carrying only cache counts must still be emitted")
}
