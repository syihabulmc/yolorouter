package requestlog

import (
	"strings"
	"testing"

	"github.com/yolorouter/yolorouter/internal/fact"
)

func TestJoinCompressors(t *testing.T) {
	cases := []struct {
		name string
		in   []string
		want string
	}{
		{"nil", nil, ""},
		{"empty", []string{}, ""},
		{"single", []string{"whitespace"}, "whitespace"},
		{"distinct", []string{"whitespace", "contractions"}, "whitespace,contractions"},
		{"duplicates preserved", []string{"whitespace", "contractions", "whitespace", "whitespace"}, "whitespace,contractions,whitespace,whitespace"},
		{"blanks filtered", []string{"", "whitespace", "", ""}, "whitespace"},
		{"repeats counted", []string{"log", "log", "diff"}, "log,log,diff"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := joinCompressors(tc.in); got != tc.want {
				t.Errorf("joinCompressors(%v) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

// unfamiliarRecord is what a capability this build has no column for would
// report. Declared in this package's test rather than in fact, because the
// point is that the recorder handles a type it was never taught about.
type unfamiliarRecord struct {
	fact.Base
	Detail string
}

func (unfamiliarRecord) RecordName() string { return "unfamiliar" }

// TestUnrecognisedRecordsSurviveIntoOverflow is the contract that makes the
// recording half of the vocabulary open.
//
// Dropping an unrecognised record is silent: the row still writes, just without
// the number, and nothing afterwards can tell an observation that never
// happened from one nobody made room for. Keeping it under its stable name
// turns a missing column into something an operator can find.
func TestUnrecognisedRecordsSurviveIntoOverflow(t *testing.T) {
	var tl fact.Timeline
	tl.Append(fact.Entry{Attempt: 1, Record: fact.UsageReported{Prompt: 7, Completion: 3}})
	tl.Append(fact.Entry{Attempt: 1, Record: unfamiliarRecord{Detail: "kept"}})
	tl.Append(fact.Entry{Attempt: 1, Record: fact.UsageIncoherent{Reason: "contradictory"}})

	s := summarise(tl)

	// The recognised one still lands in its column.
	if s.inputTokens != 7 || s.outputTokens != 3 {
		t.Errorf("recognised record lost: input=%d output=%d", s.inputTokens, s.outputTokens)
	}
	if len(s.overflow) != 2 {
		t.Fatalf("want both unrecognised records collected, got %d: %+v", len(s.overflow), s.overflow)
	}
	names := map[string]bool{}
	for _, e := range s.overflow {
		names[e.Name] = true
	}
	// UsageIncoherent has no column of its own, so it belongs here too: an
	// audit row that simply shows zero tokens cannot say whether the upstream
	// reported nothing or reported something impossible.
	for _, want := range []string{"unfamiliar", "usage_incoherent"} {
		if !names[want] {
			t.Errorf("record %q was dropped", want)
		}
	}

	encoded := encodeOverflow(s.overflow, "req-1")
	if encoded == "" {
		t.Fatal("overflow encoded to nothing")
	}
	if !strings.Contains(encoded, "unfamiliar") || !strings.Contains(encoded, "kept") {
		t.Errorf("encoded overflow lost the record's content: %s", encoded)
	}
}

// TestRoutingFactsAreNotRecords guards the split: a fact that steers the
// request is not an observation with a column, and must not end up in the
// overflow pretending to be one.
func TestRoutingFactsAreNotRecords(t *testing.T) {
	var tl fact.Timeline
	tl.Append(fact.Entry{Attempt: 1, Fact: &fact.Fact{Kind: fact.KindPayloadRefused}})

	if s := summarise(tl); len(s.overflow) != 0 {
		t.Errorf("a routing fact leaked into the overflow: %+v", s.overflow)
	}
}

// TestNoOverflowEncodesToEmpty keeps the common case out of the column: a row
// where everything was recognised should store nothing, not "[]".
func TestNoOverflowEncodesToEmpty(t *testing.T) {
	if got := encodeOverflow(nil, "req-1"); got != "" {
		t.Errorf("encodeOverflow(nil) = %q, want empty", got)
	}
}
