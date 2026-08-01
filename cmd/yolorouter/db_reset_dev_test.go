//go:build !release

package main

import (
	"errors"
	"strings"
	"testing"
)

// TestConfirmDestructiveRequiresExactYes: the prompt is the only thing standing
// between a mistyped directory and a dropped schema, so anything short of the
// word itself aborts — including a closed stdin, which is what a command
// invoked from a script or a service unit sees.
func TestConfirmDestructiveRequiresExactYes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		answer  string
		wantErr bool
	}{
		{name: "yes", answer: "yes\n"},
		{name: "yes with carriage return", answer: "yes\r\n"},
		{name: "yes without newline at eof", answer: "yes"},
		{name: "no", answer: "no\n", wantErr: true},
		{name: "empty line", answer: "\n", wantErr: true},
		{name: "closed stdin", answer: "", wantErr: true},
		{name: "prefix only", answer: "yesplease\n", wantErr: true},
		{name: "capitalised", answer: "YES\n", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := confirmDestructive(strings.NewReader(tc.answer), "continue? ")
			if tc.wantErr && err == nil {
				t.Fatalf("answer %q should have aborted", tc.answer)
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("answer %q should have been accepted, got: %v", tc.answer, err)
			}
		})
	}
}

// TestConfirmDestructiveRejectsAReadFailure: bufio returns whatever it managed
// to read alongside the error, so bytes that happen to spell "yes" would sail
// through a check that only looks at the string. An answer that could not be
// read in full is not an answer.
func TestConfirmDestructiveRejectsAReadFailure(t *testing.T) {
	err := confirmDestructive(&failingReader{data: "yes", err: errors.New("terminal went away")}, "continue? ")
	if err == nil {
		t.Fatal("a failed read must abort even when the bytes read spell yes")
	}
	if !strings.Contains(err.Error(), "aborted") {
		t.Fatalf("error should say it aborted, got: %v", err)
	}
}

// failingReader hands back bytes and then a non-EOF error, standing in for a
// terminal that goes away mid-answer.
type failingReader struct {
	data string
	err  error
	done bool
}

func (r *failingReader) Read(p []byte) (int, error) {
	if r.done {
		return 0, r.err
	}
	r.done = true
	return copy(p, r.data), r.err
}
