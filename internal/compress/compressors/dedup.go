package compressors

import (
	"fmt"
	"strings"
)

// writeDupBlock writes text to b together with a duplicate-count annotation:
//   - n > 1 of a non-empty line emits the line once followed by
//     "[× N duplicate lines]".
//   - n > 1 of an empty line collapses the whole run to a single blank line
//     (no count annotation, to avoid injecting noise into the LLM context).
//   - n == 1 writes the line as-is.
//   - n == 0 is a no-op so the helper can be used as a flush callback.
func writeDupBlock(b *strings.Builder, text string, n int) {
	if n > 1 {
		if text == "" {
			b.WriteByte('\n') // collapse consecutive blank lines to one
		} else {
			fmt.Fprintf(b, "%s\n[× %d duplicate lines]\n", text, n)
		}
	} else if n == 1 {
		b.WriteString(text)
		b.WriteByte('\n')
	}
}
