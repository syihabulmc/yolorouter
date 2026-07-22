package protocols

import "testing"

// TestIRUsage_Merge_PreservesCacheIncludedInPrompt guards a correctness property of Merge:
// CacheIncludedInPrompt is a pricing-accounting flag (the chat/gemini/responses decoders set
// PromptTokens to the full value including cached tokens, and set CacheIncludedInPrompt=true
// so pricing subtracts the cached portion via netPromptTokens to avoid double-charging). If
// Merge failed to propagate this boolean, the field would stay false after repeated Merge
// calls in a cross-protocol streaming encoder, causing cache-bearing OpenAI/Gemini requests
// to be double-charged.
func TestIRUsage_Merge_PreservesCacheIncludedInPrompt(t *testing.T) {
	// Simulate the IRUsage emitted by the chat decoder on the first DeltaUsage (carrying the
	// cache-accounting flag).
	dst := IRUsage{}
	dst.Merge(IRUsage{
		PromptTokens:          100,
		CompletionTokens:      0,
		CacheReadTokens:       30,
		CacheIncludedInPrompt: true,
	})
	if !dst.CacheIncludedInPrompt {
		t.Fatalf("first Merge must set CacheIncludedInPrompt=true — otherwise pricing would count cached tokens twice")
	}

	// Simulate a subsequent partial usage chunk (e.g. a completion-only delta), where
	// CacheIncludedInPrompt defaults to false.
	dst.Merge(IRUsage{CompletionTokens: 50})
	if !dst.CacheIncludedInPrompt {
		t.Fatalf("CacheIncludedInPrompt must not be reverted to false by a later partial chunk once true, otherwise cross-protocol streaming cache accounting would be double-charged")
	}
	if dst.PromptTokens != 100 {
		t.Errorf("PromptTokens = %d, want 100 (field-level merge protection)", dst.PromptTokens)
	}
	if dst.CompletionTokens != 50 {
		t.Errorf("CompletionTokens = %d, want 50", dst.CompletionTokens)
	}
	if dst.CacheReadTokens != 30 {
		t.Errorf("CacheReadTokens = %d, want 30 (field-level merge protection)", dst.CacheReadTokens)
	}
}

// TestIRUsage_Merge_FieldLevelMergesAcrossPartialChunks locks in the field-level merge
// semantics: when upstream sends partial usage chunks across multiple frames (prompt tokens
// in an earlier frame, completion tokens in a later one), each field accumulates correctly
// and TotalTokens is recomputed automatically.
func TestIRUsage_Merge_FieldLevelMergesAcrossPartialChunks(t *testing.T) {
	dst := IRUsage{}
	dst.Merge(IRUsage{PromptTokens: 100})
	dst.Merge(IRUsage{CompletionTokens: 50})

	if dst.PromptTokens != 100 || dst.CompletionTokens != 50 {
		t.Errorf("partial merge failed: prompt=%d completion=%d, want 100/50", dst.PromptTokens, dst.CompletionTokens)
	}
	if dst.TotalTokens != 150 {
		t.Errorf("TotalTokens = %d, want 150 (auto-recomputed)", dst.TotalTokens)
	}
}
