// Package compressors implements the leaf compressors applied to live-zone
// text blocks. Every compressor in this package is lossless-for-signal and
// deterministic: it only removes noise that carries no semantic information
// (ANSI escapes, consecutive duplicate lines, passing-test boilerplate, git
// blob hashes) and never deletes a content-differing line or truncates a hunk.
package compressors

import "context"

// Compressor compresses a single text block. Returning an error is
// non-fatal: the caller falls back to the original text for that block.
type Compressor interface {
	Name() string
	Compress(ctx context.Context, content string) (string, error)
}
