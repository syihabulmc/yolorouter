package systemprompt

import (
	"context"
	"strings"
	"testing"

	"github.com/yolorouter/yolorouter/internal/fact"
	"github.com/yolorouter/yolorouter/internal/protocols"
)

// stubView stands in for the exchange.
//
// The capability declares the view it needs, so a test answers exactly those
// three questions — no exchange to construct, no kernel to start, and no way
// for a test to accidentally depend on state the capability never reads.
type stubView struct {
	enabled bool
	prompt  string
	chat    bool
}

func (v stubView) CustomSystemPromptEnabled() bool { return v.enabled }
func (v stubView) CustomSystemPrompt() string      { return v.prompt }
func (v stubView) IsChatEndpoint() bool            { return v.chat }

// recordingSink captures what was reported, so a test can check the capability
// does not claim an injection that did not happen.
type recordingSink struct {
	facts   []fact.Fact
	records []fact.Record
}

func (s *recordingSink) Report(f ...fact.Fact) { s.facts = append(s.facts, f...) }
func (s *recordingSink) Note(r ...fact.Record) { s.records = append(s.records, r...) }

func apply(t *testing.T, v stubView, proto protocols.ProtocolID, body []byte) ([]byte, *recordingSink) {
	t.Helper()
	sink := &recordingSink{}
	out, err := New().RewriteEgress(context.Background(), v, proto, body, sink)
	if err != nil {
		t.Fatalf("RewriteEgress returned an error: %v", err)
	}
	return out, sink
}

func TestDisabledPromptLeavesBodyAlone(t *testing.T) {
	body := []byte(`{"messages":[{"role":"user","content":"hi"}]}`)
	out, sink := apply(t, stubView{chat: true}, protocols.ProtocolOpenAI, body)
	if string(out) != string(body) {
		t.Fatal("a disabled prompt must return the body unchanged")
	}
	if len(sink.records) != 0 {
		t.Errorf("nothing was injected, so nothing should be recorded: %v", sink.records)
	}
}

func TestPromptAppendsToExistingSystemText(t *testing.T) {
	v := stubView{chat: true, enabled: true, prompt: "BE CONCISE"}
	body := []byte(`{"messages":[{"role":"system","content":"You are helpful."},{"role":"user","content":"hi"}]}`)
	out, sink := apply(t, v, protocols.ProtocolOpenAI, body)
	if !strings.Contains(string(out), "BE CONCISE") || !strings.Contains(string(out), "You are helpful.") {
		t.Fatalf("expected both the original and the custom text, got %s", out)
	}
	if len(sink.records) != 1 {
		t.Fatalf("want exactly one record for one injection, got %d", len(sink.records))
	}
	rec, ok := sink.records[0].(fact.SystemPromptInjected)
	if !ok {
		t.Fatalf("recorded the wrong type: %T", sink.records[0])
	}
	if rec.ExtraChars != len("BE CONCISE") {
		t.Errorf("ExtraChars = %d, want %d", rec.ExtraChars, len("BE CONCISE"))
	}
	if len(sink.facts) != 0 {
		t.Errorf("injection must not steer the relay, but facts were reported: %v", sink.facts)
	}
}

// A route outside the chat allowlist has no system text to speak of; injecting
// there would corrupt a request the caller meant literally.
func TestNonChatRouteIsSkipped(t *testing.T) {
	v := stubView{chat: false, enabled: true, prompt: "X"}
	body := []byte(`{"models":["x"]}`)
	out, sink := apply(t, v, protocols.ProtocolOpenAI, body)
	if string(out) != string(body) {
		t.Fatal("a non-chat route must not be injected")
	}
	if len(sink.records) != 0 {
		t.Errorf("nothing was injected, so nothing should be recorded: %v", sink.records)
	}
}

// A body this capability cannot parse may still be one an upstream accepts.
// Rewriting it blind is the one outcome worse than not injecting at all, so the
// body is returned untouched — and, just as importantly, nothing is recorded,
// because a record claiming an injection the body contradicts is worse than no
// record.
func TestMalformedBodyIsReturnedUntouchedAndUnrecorded(t *testing.T) {
	v := stubView{chat: true, enabled: true, prompt: "X"}
	for _, b := range [][]byte{nil, []byte(``), []byte(`null`), []byte(`not json`), []byte(`{}`)} {
		out, sink := apply(t, v, protocols.ProtocolOpenAI, b)
		if string(out) != string(b) {
			t.Fatalf("malformed body must be unchanged: in=%q out=%q", b, out)
		}
		if len(sink.records) != 0 {
			t.Errorf("in=%q: nothing was injected, so nothing should be recorded: %v", b, sink.records)
		}
	}
}

// An unrecognised egress protocol is a route this capability has no injection
// format for. Declining is correct; guessing is not.
func TestUnknownEgressProtocolDeclines(t *testing.T) {
	v := stubView{chat: true, enabled: true, prompt: "X"}
	body := []byte(`{"messages":[]}`)
	out, sink := apply(t, v, protocols.ProtocolID("something-else"), body)
	if string(out) != string(body) {
		t.Fatal("an unknown egress protocol must leave the body unchanged")
	}
	if len(sink.records) != 0 {
		t.Errorf("nothing was injected, so nothing should be recorded: %v", sink.records)
	}
}
