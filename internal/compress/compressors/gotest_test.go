package compressors

import (
	"context"
	_ "embed"
	"strings"
	"testing"
)

//go:embed testdata/gotest_pass.txt
var gotestRaw string

func TestGoTestKeepsFailuresDropsPassNames(t *testing.T) {
	out, err := (&GoTest{}).Compress(context.Background(), gotestRaw)
	if err != nil {
		t.Fatal(err)
	}
	// Failure signal lines must be preserved verbatim.
	if !strings.Contains(out, "--- FAIL") {
		t.Fatal("failure case lines must be preserved")
	}
	// Per-line PASS boilerplate should be collapsed, so the output is
	// substantially shorter than the input.
	if len(out) >= len(gotestRaw) {
		t.Fatalf("output should be much shorter: out=%d raw=%d", len(out), len(gotestRaw))
	}
	// The total pass count must still be represented (summary or count).
	if !strings.Contains(out, "passed") && !strings.Contains(out, "PASS") {
		t.Fatal("pass summary/count must be preserved")
	}
}

func TestGoTestPreservesSkipXfailNames(t *testing.T) {
	in := "=== RUN   TestA\n--- PASS: TestA (0.00s)\n=== RUN   TestB\n--- SKIP: TestB (0.00s)\n    b_test.go:9: needs net\nPASS\nok  \tpkg\t0.1s\n"
	out, _ := (&GoTest{}).Compress(context.Background(), in)
	if !strings.Contains(out, "TestB") {
		t.Fatal("SKIP case name must be preserved")
	}
}

func TestGoTestJSONOutputEventPreservedOnFail(t *testing.T) {
	// go test -json output events carry failure-assertion detail and must
	// not be folded.
	in := `{"Action":"run","Test":"TestFoo"}
{"Action":"output","Test":"TestFoo","Output":"=== RUN   TestFoo\n"}
{"Action":"output","Test":"TestFoo","Output":"    foo_test.go:42: expected 1 got 0\n"}
{"Action":"output","Test":"TestFoo","Output":"--- FAIL: TestFoo (0.00s)\n"}
{"Action":"fail","Test":"TestFoo","Elapsed":0.001}
{"Action":"pass","Test":"TestBar","Elapsed":0.001}
`
	out, err := (&GoTest{}).Compress(context.Background(), in)
	if err != nil {
		t.Fatal(err)
	}
	// The failure detail line (an output event) must be preserved.
	if !strings.Contains(out, "foo_test.go:42") {
		t.Fatal("go test -json output event with failure detail must not be folded")
	}
	// run/pass events should be folded.
	if strings.Contains(out, `"Action":"run"`) || strings.Contains(out, `"Action":"pass"`) {
		t.Fatal("run/pass events should be folded")
	}
}

func TestGoTestAdversarialKeepsCodeBlock(t *testing.T) {
	// Adversarial mixed content: a fenced code block must not be mistaken
	// for go test output and deleted.
	in := "ok  \tpkg\t0.1s\n```go\nfunc Foo() { panic(\"x\") }\n```\n"
	out, _ := (&GoTest{}).Compress(context.Background(), in)
	if !strings.Contains(out, "func Foo()") {
		t.Fatal("fenced code block content must not be deleted (mixed-content adversarial)")
	}
}
