package protocols

// IRUsage holds token usage information normalized across all protocols.
type IRUsage struct {
	PromptTokens          int
	CompletionTokens      int
	TotalTokens           int
	CacheWriteTokens      int
	CacheReadTokens       int
	CacheIncludedInPrompt bool
	ReasoningTokens       int
	WebSearchCount        int
}

// Merge merges non-zero fields from src into dst.
//
// This is a field-level merge rather than a whole-struct last-wins assignment: an upstream
// provider may send partial usage chunks across multiple frames (e.g. prompt tokens in one
// frame, completion tokens in a later frame). A whole-struct assignment would let
// already-collected fields get clobbered back to zero; Merge only overwrites the numeric
// fields where src is non-zero, preserving previously collected values.
//
// CacheIncludedInPrompt is a pricing-accounting flag (the chat/gemini/responses decoders set
// it to true when constructing an IRUsage to indicate PromptTokens already includes cached
// tokens, so pricing subtracts the cached portion via netPromptTokens to avoid
// double-charging). **Only a false→true transition is allowed**: once any src marks it true
// it stays true; otherwise a later partial chunk defaulting to false would corrupt the
// pricing accounting and cause double-charging.
func (dst *IRUsage) Merge(src IRUsage) {
	if src.PromptTokens > 0 {
		dst.PromptTokens = src.PromptTokens
	}
	if src.CompletionTokens > 0 {
		dst.CompletionTokens = src.CompletionTokens
	}
	if src.TotalTokens > 0 {
		dst.TotalTokens = src.TotalTokens
	}
	if src.CacheWriteTokens > 0 {
		dst.CacheWriteTokens = src.CacheWriteTokens
	}
	if src.CacheReadTokens > 0 {
		dst.CacheReadTokens = src.CacheReadTokens
	}
	if src.ReasoningTokens > 0 {
		dst.ReasoningTokens = src.ReasoningTokens
	}
	if src.WebSearchCount > 0 {
		dst.WebSearchCount = src.WebSearchCount
	}
	if src.CacheIncludedInPrompt {
		dst.CacheIncludedInPrompt = true
	}
	if dst.TotalTokens == 0 || dst.TotalTokens < dst.PromptTokens+dst.CompletionTokens {
		dst.TotalTokens = dst.PromptTokens + dst.CompletionTokens
	}
}
