package compressors

import (
	"context"
	"strings"
)

// Grep is a lossless compressor for grep/rg output: it folds runs of
// consecutive identical match lines into a single line plus a duplicate
// count, while preserving every distinct path:lineno:match entry.
type Grep struct{}

func (g *Grep) Name() string { return "grep" }

func (g *Grep) Compress(ctx context.Context, content string) (string, error) {
	lines := strings.Split(content, "\n")
	var b strings.Builder
	var prev string
	var dup int
	flush := func() {
		writeDupBlock(&b, prev, dup)
		dup = 0
	}
	for i, ln := range lines {
		if i%256 == 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			default:
			}
		}
		if ln == prev && dup > 0 {
			dup++
			continue
		}
		flush()
		prev = ln
		dup = 1
	}
	flush()
	return strings.TrimRight(b.String(), "\n"), nil
}
