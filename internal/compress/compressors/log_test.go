package compressors

import (
	"context"
	"strings"
	"testing"
)

func TestLogStripsANSIAndFoldsDupes(t *testing.T) {
	in := "building...\x1b[32mok\x1b[0m\n" +
		strings.Repeat("retrying connection timeout\n", 5) +
		"done\n"
	out, err := (&Log{}).Compress(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(out, "\x1b[") {
		t.Fatal("ANSI escapes should be stripped")
	}
	if strings.Count(out, "retrying connection timeout") != 1 {
		t.Fatalf("consecutive duplicate lines should fold to 1 + count, got:\n%s", out)
	}
	if !strings.Contains(out, "done") {
		t.Fatal("non-duplicate lines must be preserved")
	}
}

func TestLogPreservesDistinctLines(t *testing.T) {
	in := "line a\nline b\nline c\n"
	out, _ := (&Log{}).Compress(context.Background(), in)
	for _, w := range []string{"line a", "line b", "line c"} {
		if !strings.Contains(out, w) {
			t.Fatalf("distinct lines must not be dropped: %s", w)
		}
	}
}
