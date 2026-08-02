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

// TestIRUsage_NetAndGrossPromptTokens covers the two conversion directions an
// egress encoder depends on. The concrete numbers come from a real relayed
// request: an Anthropic upstream reporting 2 net input + 906 cache write +
// 36678 cache read, whose gross equivalent is 37586.
func TestIRUsage_NetAndGrossPromptTokens(t *testing.T) {
	tests := []struct {
		name      string
		usage     IRUsage
		wantNet   int
		wantGross int
	}{
		{
			// Anthropic upstream: PromptTokens is already net, so Net is a
			// pass-through and Gross must add both cache counts back.
			name: "anthropic upstream (net convention)",
			usage: IRUsage{
				PromptTokens:     2,
				CompletionTokens: 123,
				CacheWriteTokens: 906,
				CacheReadTokens:  36678,
			},
			wantNet:   2,
			wantGross: 37586,
		},
		{
			// OpenAI-shaped upstream: PromptTokens is gross, so Gross is a
			// pass-through and Net must subtract.
			name: "openai upstream (gross convention)",
			usage: IRUsage{
				PromptTokens:          37586,
				CompletionTokens:      123,
				CacheReadTokens:       36678,
				CacheIncludedInPrompt: true,
			},
			wantNet:   908,
			wantGross: 37586,
		},
		{
			// The cache_creation_input_tokens alias: a gross prompt count that
			// also carries a cache-write breakdown. Net must subtract BOTH, or
			// the write portion is counted twice as fresh input.
			name: "openai upstream carrying the cache-write alias",
			usage: IRUsage{
				PromptTokens:          37586,
				CompletionTokens:      123,
				CacheWriteTokens:      906,
				CacheReadTokens:       36678,
				CacheIncludedInPrompt: true,
			},
			wantNet:   2,
			wantGross: 37586,
		},
		{
			// Defensive: an upstream reporting more cache than prompt must not
			// produce a negative input count.
			name: "malformed upstream (cache exceeds prompt)",
			usage: IRUsage{
				PromptTokens:          10,
				CacheReadTokens:       500,
				CacheIncludedInPrompt: true,
			},
			wantNet:   0,
			wantGross: 10,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.usage.NetPromptTokens(); got != tt.wantNet {
				t.Errorf("NetPromptTokens() = %d, want %d", got, tt.wantNet)
			}
			if got := tt.usage.GrossPromptTokens(); got != tt.wantGross {
				t.Errorf("GrossPromptTokens() = %d, want %d", got, tt.wantGross)
			}
		})
	}
}

// TestIRUsage_GrossTotalTokens_IsStrictIdentity locks in that total is always
// GrossPromptTokens + CompletionTokens and never the upstream's own larger
// figure: an excess total is unattributed tokens, and emitting
// total > prompt + completion violates every protocol we encode to.
func TestIRUsage_GrossTotalTokens_IsStrictIdentity(t *testing.T) {
	u := IRUsage{
		PromptTokens:          37586,
		CompletionTokens:      123,
		TotalTokens:           99999, // upstream over-reports
		CacheReadTokens:       36678,
		CacheIncludedInPrompt: true,
	}
	if got := u.GrossTotalTokens(); got != 37709 {
		t.Errorf("GrossTotalTokens() = %d, want 37709 (strict identity, upstream's 99999 ignored)", got)
	}
}
