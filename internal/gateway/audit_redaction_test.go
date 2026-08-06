package gateway

import (
	"errors"
	"net/url"
	"strings"
	"testing"

	"github.com/yolorouter/yolorouter/internal/protocols"
)

// TestATransportFailureDoesNotCarryTheCredentialItFailedToSend pins the one
// place the dispatch-time redaction can be undone.
//
// A provider's base URL is operator-configured and may carry the key as a query
// parameter. Every URL written to the audit trail goes through RedactURL for
// that reason — except that a transport failure arrives as a *url.Error holding
// the URL net/http was given, and that error's own text becomes the attempt
// note. url.Error hides the userinfo password and stops there.
func TestATransportFailureDoesNotCarryTheCredentialItFailedToSend(t *testing.T) {
	const secret = "SECRET-KEY-123"
	raw := "https://user:hunter2@gw.example.invalid/v1/chat/completions?key=" + secret
	redacted := protocols.RedactURL(raw)

	// The shape net/http hands back, with the redaction it does apply already
	// in place — so this fixture cannot pass by accident.
	failure := &url.Error{
		Op:  "Post",
		URL: strings.Replace(raw, "hunter2", "***", 1),
		Err: errors.New("dial tcp 127.0.0.1:1: connect: connection refused"),
	}

	// The premise: left alone, the error names the key.
	if !strings.Contains(failure.Error(), secret) {
		t.Fatalf("the fixture no longer carries the credential, so it proves nothing: %v", failure)
	}

	note := redactedFailure(failure, redacted)

	if strings.Contains(note, secret) {
		t.Errorf("attempt note = %q, want the key gone: this string is persisted", note)
	}
	if !strings.Contains(note, "connection refused") {
		t.Errorf("attempt note = %q, want it to still say why the send failed", note)
	}
	if !strings.Contains(note, "gw.example.invalid") {
		t.Errorf("attempt note = %q, want it to still say which upstream was tried", note)
	}
}

// TestAnUnparseableURLIsNotPassedThroughToTheAuditRow pins the case the
// redaction used to hand straight back.
//
// A base URL that will not parse is exactly the one that fails to build a
// request — so this is not a hypothetical corner, it is the branch the attempt
// note is written from. Passing the string through unchanged there means the
// sanitizer returns its own input on the one path where the input is what has
// to be hidden.
func TestAnUnparseableURLIsNotPassedThroughToTheAuditRow(t *testing.T) {
	const secret = "SECRET-KEY-123"
	// A control character makes url.Parse fail; the credential rides in the
	// query, where url.Error would not have hidden it either.
	raw := withControlCharacter("https://gw.example.invalid/v1", "?key="+secret)

	if _, err := url.Parse(raw); err == nil {
		t.Fatal("the fixture parses now, so it no longer exercises the branch it was written for")
	}
	if got := protocols.RedactURL(raw); strings.Contains(got, secret) {
		t.Errorf("RedactURL returned %q, want the credential gone: this string is persisted "+
			"as upstream_url and reused in the attempt note", got)
	}

	// And the note built from it, which is where the fix has to hold end to end.
	failure := &url.Error{Op: "parse", URL: raw, Err: errors.New("net/url: invalid control character in URL")}
	if note := redactedFailure(failure, protocols.RedactURL(raw)); strings.Contains(note, secret) {
		t.Errorf("attempt note = %q, want the credential gone", note)
	}
}

// TestAFailureThatNamesNoURLIsPassedThroughWhole guards the other direction:
// only a *url.Error has a URL to redact, and rewriting anything else would
// throw away the only description of the failure there is.
func TestAFailureThatNamesNoURLIsPassedThroughWhole(t *testing.T) {
	err := errors.New("provider key decrypt failed")
	if got := redactedFailure(err, "https://redacted.invalid"); got != err.Error() {
		t.Errorf("note = %q, want the error unchanged (%q)", got, err.Error())
	}
}

// withControlCharacter joins two halves of a URL around a byte no URL may
// contain, producing a string url.Parse rejects.
//
// Built here rather than written as one literal because a literal invalid URL
// is itself a lint finding — and the right answer to that is not to silence the
// check, which is correct everywhere else, but to stop handing it a constant.
func withControlCharacter(head, tail string) string {
	return string(append(append([]byte(head), 0x7f), tail...))
}
