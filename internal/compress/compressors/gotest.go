package compressors

import (
	"context"
	"fmt"
	"regexp"
	"strings"
)

var (
	runLineRe  = regexp.MustCompile(`^=== RUN\s`)
	passLineRe = regexp.MustCompile(`^\s*--- PASS:`)
	contLineRe = regexp.MustCompile(`^=== (CONT|PAUSE|NAME)\s`)
	jsonPassRe = regexp.MustCompile(`"Action":"(pass|run)"`)
	fenceRe    = regexp.MustCompile("^```")
)

// GoTest is a lossless-for-signal compressor for `go test` text output. It
// drops the boilerplate of passing tests (=== RUN / --- PASS / === CONT lines,
// counted but not emitted) while preserving every failure/skip indicator:
//   - --- FAIL / --- SKIP lines
//   - panic output and error stacks
//   - ok / FAIL summary lines
//   - the full contents of fenced code blocks (mixed-content safety)
type GoTest struct{}

func (g *GoTest) Name() string { return "gotest" }

func (g *GoTest) Compress(ctx context.Context, content string) (string, error) {
	lines := strings.Split(content, "\n")
	var b strings.Builder
	passCount := 0
	inFence := false
	for i, ln := range lines {
		if i%256 == 0 {
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			default:
			}
		}
		// Fenced code blocks are emitted verbatim, so adversarial mixed
		// content (e.g. a code block that looks like test output) is kept.
		if fenceRe.MatchString(ln) {
			inFence = !inFence
			b.WriteString(ln)
			b.WriteByte('\n')
			continue
		}
		if inFence {
			b.WriteString(ln)
			b.WriteByte('\n')
			continue
		}
		// Drop passing-test boilerplate (counted for the summary tail).
		if runLineRe.MatchString(ln) || contLineRe.MatchString(ln) {
			continue
		}
		if passLineRe.MatchString(ln) {
			passCount++
			continue
		}
		// Fold -json pass/run events; output events carry failure detail and
		// are never folded.
		if jsonPassRe.MatchString(ln) {
			passCount++
			continue
		}
		// Everything else (FAIL / SKIP / stacks / ok / FAIL summary) is kept.
		b.WriteString(ln)
		b.WriteByte('\n')
	}
	out := strings.TrimRight(b.String(), "\n")
	if passCount > 0 {
		out += fmt.Sprintf("\n[%d passed (collapsed)]", passCount)
	}
	return out, nil
}
