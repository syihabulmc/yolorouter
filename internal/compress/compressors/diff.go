package compressors

import (
	"context"
	"regexp"
	"strings"
)

// diffNoiseRe matches only git blob hash header lines (index abc..def).
// Mode, rename, and similarity lines carry semantic meaning and are kept.
var diffNoiseRe = regexp.MustCompile(`^index [0-9a-f]+\.\.[0-9a-f]+`)

// Diff is a lossless compressor for git diff output: it strips blob hash
// header lines and ANSI escape sequences. Hunks are never truncated (hunk
// truncation would lose content and is out of scope for this compressor).
type Diff struct{}

func (d *Diff) Name() string { return "diff" }

func (d *Diff) Compress(ctx context.Context, content string) (string, error) {
	lines := strings.Split(content, "\n")
	var b strings.Builder
	for i, raw := range lines {
		if i%256 == 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			default:
			}
		}
		line := raw
		if strings.IndexByte(raw, '\x1b') >= 0 {
			line = ansiRe.ReplaceAllString(raw, "")
		}
		if diffNoiseRe.MatchString(line) {
			continue
		}
		b.WriteString(line)
		b.WriteByte('\n')
	}
	return strings.TrimRight(b.String(), "\n"), nil
}
