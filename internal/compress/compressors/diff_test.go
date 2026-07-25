package compressors

import (
	"context"
	"strings"
	"testing"
)

func TestDiffStripsHeaderNoiseKeepsAllHunkLines(t *testing.T) {
	in := "diff --git a/x.go b/x.go\nindex 1a2b3c4..5d6e7f8 100644\n--- a/x.go\n+++ b/x.go\n@@ -1,2 +1,3 @@\n func f() {\n-\tx := 1\n+\tx := 2\n+\ty := 3\n }\n"
	out, err := (&Diff{}).Compress(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	// The index noise line must be stripped.
	if strings.Contains(out, "index 1a2b3c4") {
		t.Fatal("index header line should be stripped")
	}
	// Hunk change lines, file names, and the hunk header must be preserved
	// verbatim (lossless invariant).
	for _, w := range []string{"diff --git a/x.go", "@@ -1,2 +1,3 @@", "-\tx := 1", "+\tx := 2", "+\ty := 3"} {
		if !strings.Contains(out, w) {
			t.Fatalf("must be preserved: %q\ngot:\n%s", w, out)
		}
	}
}

func TestDiffStripsAnsi(t *testing.T) {
	in := "diff --git a/x.go b/x.go\n\x1b[32m+added line\x1b[0m\n@@ -1 +1,2 @@\n"
	out, _ := (&Diff{}).Compress(context.Background(), in)
	if strings.Contains(out, "\x1b[") {
		t.Fatal("ANSI escapes should be stripped")
	}
	if !strings.Contains(out, "+added line") {
		t.Fatal("content must be preserved after ANSI removal")
	}
}

func TestDiffPreservesModeAndRenameLines(t *testing.T) {
	// mode/rename lines carry semantic meaning (chmod affects deployment
	// permissions) and must not be deleted.
	in := "diff --git a/x.sh b/x.sh\nold mode 100644\nnew mode 100755\n--- a/x.sh\n+++ b/x.sh\n@@ -1 +1 @@\n-old\n+new\n"
	out, _ := (&Diff{}).Compress(context.Background(), in)
	for _, w := range []string{"old mode 100644", "new mode 100755", "-old", "+new"} {
		if !strings.Contains(out, w) {
			t.Fatalf("semantic line must be preserved: %q\ngot:\n%s", w, out)
		}
	}
}

func TestDiffPreservesRenameFromTo(t *testing.T) {
	in := "diff --git a/old.go b/new.go\nsimilarity index 90%\nrename from old.go\nrename to new.go\nindex abc1234..def5678\n"
	out, _ := (&Diff{}).Compress(context.Background(), in)
	for _, w := range []string{"rename from old.go", "rename to new.go", "similarity index 90%"} {
		if !strings.Contains(out, w) {
			t.Fatalf("semantic line must be preserved: %q\ngot:\n%s", w, out)
		}
	}
	if strings.Contains(out, "index abc1234") {
		t.Fatal("blob hash index line should be stripped")
	}
}
