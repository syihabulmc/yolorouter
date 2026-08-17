package gateway

import (
	"testing"

	"github.com/yolorouter/yolorouter/internal/protocols"
)

func TestIRUsageToUsage(t *testing.T) {
	tests := []struct {
		name string
		in   *protocols.IRUsage
		want *Usage
	}{
		{
			name: "nil input",
			in:   nil,
			want: nil,
		},
		{
			name: "all-zero usage treated as unknown",
			in:   &protocols.IRUsage{},
			want: nil,
		},
		{
			// Every field, including CacheIncludedInPrompt, maps through
			// verbatim. netPromptTokens (log.go) — not this conversion —
			// derives the net input from PromptTokens and the flag.
			name: "full mapping preserves fields and flag",
			in: &protocols.IRUsage{
				PromptTokens:          100,
				CompletionTokens:      50,
				TotalTokens:           150,
				CacheWriteTokens:      10,
				CacheReadTokens:       20,
				CacheIncludedInPrompt: true,
			},
			want: &Usage{
				PromptTokens:          100,
				CompletionTokens:      50,
				TotalTokens:           150,
				CacheWriteTokens:      10,
				CacheReadTokens:       20,
				CacheIncludedInPrompt: true,
			},
		},
		{
			name: "only completion tokens known is not treated as empty",
			in:   &protocols.IRUsage{CompletionTokens: 50},
			want: &Usage{CompletionTokens: 50},
		},
		{
			// An exchange can spend money without spending tokens. The provider
			// runs searches on its own initiative and charges for them
			// separately; the count arrives once, in the usage the response
			// ends with. Judging emptiness by the token fields alone drops the
			// record whole, and nothing downstream can re-derive the count
			// because the body it was read from is gone by then.
			name: "searches with no tokens is usage, not an empty record",
			in:   &protocols.IRUsage{WebSearchCount: 3},
			want: &Usage{WebSearchCount: 3},
		},
		{
			// Same shape, different line: reasoning tokens are their own
			// dimension and were dropped by the same test.
			name: "reasoning with no other counts is usage too",
			in:   &protocols.IRUsage{ReasoningTokens: 12},
			want: &Usage{ReasoningTokens: 12},
		},
		{
			// An impossible record that states no quantity at all is still
			// nothing reported. The verdict has nothing to attach to: there are
			// no counts to withhold from pricing and none to show an operator,
			// so admitting it would only turn "could not be priced" into
			// "priced at zero" — a claim a dashboard adds up.
			//
			// The verdict matters when it arrives WITH counts, and that case is
			// covered where it can actually go wrong: the delivery round trip,
			// in the observer file.
			name: "an impossible record stating no quantity is still nothing reported",
			in:   &protocols.IRUsage{Invalid: true},
			want: nil,
		},
		// Regression for the cache-inclusion billing bug: computeCost
		// (log.go) does PromptTokens - CacheReadTokens, assuming OpenAI
		// semantics where PromptTokens already includes cache-read. The
		// Claude decoder reports input_tokens EXCLUDING cache-read and
		// leaves CacheIncludedInPrompt at its zero value (false); without
		// normalizing here, computeCost double-subtracts cache-read and
		// undercharges. false must add CacheReadTokens into PromptTokens;
		// true (chat/gemini/responses, which already include it) must be
		// passed through unchanged.
		{
			name: "claude semantics (flag false) passes prompt through as net input",
			in: &protocols.IRUsage{
				PromptTokens:          100,
				CacheReadTokens:       30,
				CacheIncludedInPrompt: false,
			},
			want: &Usage{
				PromptTokens:          100,
				CacheReadTokens:       30,
				CacheIncludedInPrompt: false,
			},
		},
		{
			name: "openai/gemini/responses semantics (flag true) passes prompt and flag through",
			in: &protocols.IRUsage{
				PromptTokens:          100,
				CacheReadTokens:       30,
				CacheIncludedInPrompt: true,
			},
			want: &Usage{
				PromptTokens:          100,
				CacheReadTokens:       30,
				CacheIncludedInPrompt: true,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := irUsageToUsage(tt.in)
			if tt.want == nil {
				if got != nil {
					t.Fatalf("irUsageToUsage() = %+v, want nil", got)
				}
				return
			}
			if got == nil {
				t.Fatalf("irUsageToUsage() = nil, want %+v", tt.want)
			}
			if *got != *tt.want {
				t.Fatalf("irUsageToUsage() = %+v, want %+v", *got, *tt.want)
			}
		})
	}
}
