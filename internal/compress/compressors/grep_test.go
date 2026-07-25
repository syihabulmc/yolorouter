package compressors

import (
	"context"
	"strings"
	"testing"
)

func TestGrepFoldsConsecutiveIdentical(t *testing.T) {
	in := "a.go:1:hit\n" + strings.Repeat("b.go:2:same\n", 4) + "c.go:3:hit\n"
	out, err := (&Grep{}).Compress(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(out, "b.go:2:same") != 1 {
		t.Fatalf("consecutive identical match lines should fold to 1 + count, got:\n%s", out)
	}
	for _, w := range []string{"a.go:1:hit", "c.go:3:hit"} {
		if !strings.Contains(out, w) {
			t.Fatalf("distinct match lines must be preserved: %s\ngot:\n%s", w, out)
		}
	}
}

func TestGrepNoFoldNonConsecutive(t *testing.T) {
	in := "a.go:1:same\nb.go:2:other\na.go:3:same\n"
	out, _ := (&Grep{}).Compress(context.Background(), in)
	if strings.Count(out, "a.go") != 2 {
		t.Fatalf("non-consecutive identical lines should not fold, got:\n%s", out)
	}
}

func TestGrepSingleLine(t *testing.T) {
	in := "a.go:1:hit\n"
	out, _ := (&Grep{}).Compress(context.Background(), in)
	if !strings.Contains(out, "a.go:1:hit") {
		t.Fatalf("single line should be preserved as-is, got: %s", out)
	}
}
