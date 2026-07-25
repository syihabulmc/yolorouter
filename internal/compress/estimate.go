package compress

// estimateTokensSaved approximates saved tokens as (origChars-compChars)/4.
// Negative savings are clamped to zero (no local tokenizer is loaded).
func estimateTokensSaved(origChars, compChars int) int {
	d := origChars - compChars
	if d <= 0 {
		return 0
	}
	return d / 4
}

// shouldAttempt reports whether a block meets the byte threshold for compression.
func shouldAttempt(blockBytes, minBlockBytes int) bool {
	return blockBytes >= minBlockBytes
}

// acceptCompressed reports whether the compressed form is strictly shorter
// than the original. Equal-length output is rejected (no benefit).
func acceptCompressed(original, compressed string) bool {
	return len(compressed) < len(original)
}
