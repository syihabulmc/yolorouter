package compress

import "testing"

func TestDetectBuildOutput(t *testing.T) {
	gotest := "=== RUN   TestFoo\n--- PASS: TestFoo (0.00s)\nPASS\nok  \tpkg/foo\t0.1s\n"
	if ct := detectContentType(gotest); ct != ContentBuildOutput {
		t.Fatalf("go test output should be detected as BuildOutput, got %v", ct)
	}
}

func TestDetectPlainTextFallback(t *testing.T) {
	if ct := detectContentType("just some prose without log markers"); ct != ContentPlainText {
		t.Fatalf("plain prose should fall back to PlainText, got %v", ct)
	}
	if ct := detectContentType(""); ct != ContentPlainText {
		t.Fatalf("empty string should be PlainText, got %v", ct)
	}
}

func TestDetectGitDiff(t *testing.T) {
	d := "diff --git a/x.go b/x.go\n--- a/x.go\n+++ b/x.go\n@@ -1,3 +1,4 @@\n func f() {\n-\tx := 1\n+\tx := 2\n+\ty := 3\n }\n"
	if detectContentType(d) != ContentGitDiff {
		t.Fatal("expected GitDiff detection")
	}
}

func TestDetectSearchResults(t *testing.T) {
	s := "src/main.go:42:func process() {\nsrc/util.go:13:\treturn nil\nlib/x.go:7:type X struct{}\n"
	if detectContentType(s) != ContentSearchResults {
		t.Fatal("expected SearchResults detection")
	}
}

func TestDetectSearchResultsNoFalsePositive(t *testing.T) {
	// The label:N: form (without a path separator like / or .) must not be
	// misclassified as SearchResults.
	plain := "case:1: first\ncase:2: second\nitem:3: third\nitem:4: fourth\n"
	if ct := detectContentType(plain); ct == ContentSearchResults {
		t.Fatal("label:N: text without a path separator should not be flagged as SearchResults")
	}
}
