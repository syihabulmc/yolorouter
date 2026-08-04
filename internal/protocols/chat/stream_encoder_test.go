package chat

import (
	"testing"

	"github.com/yolorouter/yolorouter/internal/protocols"
)

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
