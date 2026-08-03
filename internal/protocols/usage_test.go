package protocols

import (
	"math"
	"testing"
)

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

// TestNetPromptTokens covers the net input token calculation: when
// CacheIncludedInPrompt=true, PromptTokens includes cached tokens and must
// be reduced by CacheRead+CacheWrite; when false, PromptTokens is returned
// as-is. Clamps to 0 on underflow.
func TestNetPromptTokens(t *testing.T) {
	tests := []struct {
		name string
		u    IRUsage
		want int
	}{
		{"no cache flag returns raw", IRUsage{PromptTokens: 100}, 100},
		{"cache included subtracts read", IRUsage{PromptTokens: 17075, CacheReadTokens: 256, CacheIncludedInPrompt: true}, 16819},
		{"cache included subtracts read+write", IRUsage{PromptTokens: 200, CacheReadTokens: 50, CacheWriteTokens: 30, CacheIncludedInPrompt: true}, 120},
		{"underflow clamps to zero", IRUsage{PromptTokens: 10, CacheReadTokens: 50, CacheIncludedInPrompt: true}, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.u.NetPromptTokens(); got != tt.want {
				t.Errorf("NetPromptTokens() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestGrossPromptTokens covers the mirror of NetPromptTokens: OpenAI Chat /
// Responses / Gemini all define their input count as INCLUDING cached tokens,
// so an Anthropic upstream (CacheIncludedInPrompt=false, net PromptTokens) must
// have the cache portion added back before being emitted on those protocols.
// Emitting the raw net value would produce the invalid relation
// cached_tokens > prompt_tokens.
func TestGrossPromptTokens(t *testing.T) {
	tests := []struct {
		name string
		u    IRUsage
		want int
	}{
		// Anthropic upstream: net prompt, cache must be added back.
		{"net prompt adds read+write", IRUsage{PromptTokens: 0, CacheWriteTokens: 344, CacheReadTokens: 6521}, 6865},
		{"net prompt with real input", IRUsage{PromptTokens: 100, CacheWriteTokens: 20, CacheReadTokens: 50}, 170},
		{"net prompt without cache is unchanged", IRUsage{PromptTokens: 1000}, 1000},
		// OpenAI/Gemini upstream: already gross, must pass through untouched
		// (adding again would double-count the cache).
		{"already gross passes through", IRUsage{PromptTokens: 6865, CacheReadTokens: 6521, CacheIncludedInPrompt: true}, 6865},
		{"already gross without cache", IRUsage{PromptTokens: 500, CacheIncludedInPrompt: true}, 500},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := tt.u.GrossPromptTokens(); got != tt.want {
				t.Errorf("GrossPromptTokens() = %v, want %v", got, tt.want)
			}
		})
	}
}

// TestGrossTotalTokens locks total_tokens as an exact identity
// (gross prompt + completion). Every protocol we emit defines it that way, and
// passing through a larger upstream-reported total would emit
// total > prompt + completion, an invalid relation that also hides whichever
// tokens we failed to attribute to either side (Gemini thoughts used to land
// there before the decoder folded them into CompletionTokens).
func TestGrossTotalTokens(t *testing.T) {
	tests := []struct {
		name string
		u    IRUsage
		want int
	}{
		{
			"anthropic sample stays self-consistent",
			IRUsage{PromptTokens: 0, CompletionTokens: 38, CacheWriteTokens: 344, CacheReadTokens: 6521, TotalTokens: 6903},
			6903,
		},
		{
			"raises total that contradicts gross prompt",
			IRUsage{PromptTokens: 0, CompletionTokens: 38, CacheWriteTokens: 344, CacheReadTokens: 6521, TotalTokens: 38},
			6903,
		},
		{
			"discards unattributable upstream excess",
			IRUsage{PromptTokens: 100, CompletionTokens: 50, TotalTokens: 900, CacheIncludedInPrompt: true},
			150,
		},
		{
			"gemini thoughts folded into completion keep the identity",
			// promptTokenCount=100, candidates=30, thoughts=20 →
			// decoder yields CompletionTokens=50, ReasoningTokens=20, total=150.
			IRUsage{PromptTokens: 100, CompletionTokens: 50, ReasoningTokens: 20, TotalTokens: 150, CacheIncludedInPrompt: true},
			150,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.u.GrossTotalTokens()
			if got != tt.want {
				t.Errorf("GrossTotalTokens() = %v, want %v", got, tt.want)
			}
			if got != tt.u.GrossPromptTokens()+tt.u.CompletionTokens {
				t.Errorf("identity broken: total %d != gross prompt %d + completion %d",
					got, tt.u.GrossPromptTokens(), tt.u.CompletionTokens)
			}
		})
	}
}

// TestIRUsage_NetDialectIsReclassified covers the OpenAI-compatible upstreams
// that front an Anthropic model: they copy Anthropic's already-net input_tokens
// into prompt_tokens and report the cached portion beside it, while the decoder
// — reading only the wire shape — marks the record inclusive.
//
// Read at face value, the cache is subtracted from a prompt that never
// contained it: the net input collapses to 0 and the gross total shrinks from
// the upstream's own 17399 to 247. The stated total is what gives the dialect
// away — prompt + completion + cache == total exactly, which is impossible
// under the inclusive convention where the cache already sits inside prompt.
func TestIRUsage_NetDialectIsReclassified(t *testing.T) {
	u := IRUsage{
		PromptTokens:          237, // actually NET, despite the flag below
		CompletionTokens:      10,
		TotalTokens:           17399, // 237 + 10 + 17152
		CacheReadTokens:       17152,
		CacheIncludedInPrompt: true, // the decoder's guess, and it is wrong
	}

	if !CacheSitsOutsidePrompt(u) {
		t.Fatal("net dialect not detected: prompt+completion+cache == total can only hold under the net convention")
	}
	if got := u.NetPromptTokens(); got != 237 {
		t.Errorf("NetPromptTokens() = %d, want 237 (subtracting a cache the prompt never held drives this to 0)", got)
	}
	if got := u.GrossPromptTokens(); got != 17389 {
		t.Errorf("GrossPromptTokens() = %d, want 17389 (237 + 17152)", got)
	}
	if got := u.GrossTotalTokens(); got != 17399 {
		t.Errorf("GrossTotalTokens() = %d, want 17399", got)
	}
}

// TestIRUsage_GenuineInclusiveIsNotReclassified is the negative case: a real
// OpenAI upstream, where the cache genuinely is a subset of the prompt. Its
// stated total leaves no room for the cache on a line of its own, so the record
// must keep the inclusive reading and keep subtracting.
func TestIRUsage_GenuineInclusiveIsNotReclassified(t *testing.T) {
	u := IRUsage{
		PromptTokens:          17389, // gross: 237 fresh + 17152 cached
		CompletionTokens:      10,
		TotalTokens:           17399,
		CacheReadTokens:       17152,
		CacheIncludedInPrompt: true,
	}

	if CacheSitsOutsidePrompt(u) {
		t.Fatal("a genuinely inclusive record must not be reclassified — doing so bills the cached tokens twice")
	}
	if got := u.NetPromptTokens(); got != 237 {
		t.Errorf("NetPromptTokens() = %d, want 237", got)
	}
	if got := u.GrossPromptTokens(); got != 17389 {
		t.Errorf("GrossPromptTokens() = %d, want 17389 unchanged", got)
	}
}

// TestIRUsage_OverflowIsRejectedNotWrapped guards the conversion arithmetic
// against upstream-supplied extremes. Token counts arrive as signed JSON
// integers with nothing bounding their magnitude, and a wrapped sum would be
// marshalled to the client as a negative total.
func TestIRUsage_OverflowIsRejectedNotWrapped(t *testing.T) {
	u := IRUsage{PromptTokens: math.MaxInt, CompletionTokens: 1}
	if got := u.GrossTotalTokens(); got < 0 {
		t.Errorf("GrossTotalTokens() = %d, must never be negative", got)
	}

	if _, ok := AddTokenCounts(math.MaxInt, 1); ok {
		t.Error("AddTokenCounts must report overflow rather than wrapping")
	}
	if _, ok := AddTokenCounts(-1); ok {
		t.Error("AddTokenCounts must reject negative counts")
	}
}
