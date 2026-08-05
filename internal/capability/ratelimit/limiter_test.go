package ratelimit

import (
	"context"
	"testing"

	"github.com/yolorouter/yolorouter/internal/fact"
	"time"
)

func TestLimiterConcurrency(t *testing.T) {
	l := NewLimiter()
	const key uint = 1
	if !l.AcquireConcurrency(key, 2) {
		t.Fatal("first acquire should succeed")
	}
	if !l.AcquireConcurrency(key, 2) {
		t.Fatal("second acquire should succeed")
	}
	if l.AcquireConcurrency(key, 2) {
		t.Fatal("third acquire should fail (limit 2)")
	}
	l.ReleaseConcurrency(key)
	if !l.AcquireConcurrency(key, 2) {
		t.Fatal("acquire after release should succeed")
	}
	// Release one more time than acquired past this point is a guarded no-op,
	// not an underflow — verify it doesn't panic or go negative.
	l.ReleaseConcurrency(key)
	l.ReleaseConcurrency(key)
}

func TestLimiterConcurrencyUnlimited(t *testing.T) {
	l := NewLimiter()
	for i := 0; i < 200; i++ {
		if !l.AcquireConcurrency(1, 0) {
			t.Fatalf("acquire %d failed under unlimited (limit 0)", i)
		}
	}
}

func TestLimiterConcurrencyPerKey(t *testing.T) {
	l := NewLimiter()
	// Two different keys do not share a concurrency budget.
	if !l.AcquireConcurrency(1, 1) {
		t.Fatal("key 1 first acquire failed")
	}
	if !l.AcquireConcurrency(2, 1) {
		t.Fatal("key 2 acquire should be independent of key 1")
	}
	if l.AcquireConcurrency(1, 1) {
		t.Fatal("key 1 second acquire should fail (limit 1)")
	}
}

func TestLimiterRPM(t *testing.T) {
	l := NewLimiter()
	const key uint = 1
	now := time.Unix(0, 0)
	for i := 0; i < 3; i++ {
		if !l.CheckRPM(key, 3, now) {
			t.Fatalf("check %d should succeed under limit 3", i)
		}
	}
	if l.CheckRPM(key, 3, now) {
		t.Fatal("4th check in the same minute should fail")
	}
	// A new minute window resets the counter.
	nextMinute := time.Unix(60, 0)
	if !l.CheckRPM(key, 3, nextMinute) {
		t.Fatal("first check of the next minute should succeed")
	}
}

func TestLimiterRPMUnlimited(t *testing.T) {
	l := NewLimiter()
	for i := 0; i < 100; i++ {
		if !l.CheckRPM(1, 0, time.Now()) {
			t.Fatalf("check %d failed under unlimited RPM", i)
		}
	}
}

// stubView answers the three questions this capability asks.
type stubView struct {
	keyID uint
	conc  int
	rpm   int
}

func (v stubView) APIKeyID() uint        { return v.keyID }
func (v stubView) ConcurrencyLimit() int { return v.conc }
func (v stubView) RPMLimit() int         { return v.rpm }

type collectSink struct{ facts []fact.Fact }

func (s *collectSink) Report(f ...fact.Fact) { s.facts = append(s.facts, f...) }
func (s *collectSink) Note(...fact.Record)   {}

// TestConcurrencyRejectionDoesNotBurnAnRPMToken is the ordering rule these two
// limits share, and the reason they are one admission rather than two.
//
// Checked the other way round, a burst rejected on concurrency still spends the
// minute's allowance, and a single served request can exhaust it for everyone.
func TestConcurrencyRejectionDoesNotBurnAnRPMToken(t *testing.T) {
	l := NewLimiter()
	view := stubView{keyID: 1, conc: 1, rpm: 2}

	// Fill the only concurrency slot.
	first, held := l.Admit(context.Background(), view, &collectSink{})
	if !held {
		t.Fatal("the first request should have been admitted")
	}

	sink := &collectSink{}
	if _, held := l.Admit(context.Background(), view, sink); held {
		t.Fatal("the second request should have been refused on concurrency")
	}
	if len(sink.facts) != 1 || sink.facts[0].Kind != fact.KindCallerRateLimited {
		t.Fatalf("want one caller_rate_limited fact, got %v", sink.facts)
	}

	// The admitted request spent one of the two tokens; the refused one must
	// have spent none. So exactly one more request fits in this minute — if the
	// refusal had also cost a token, none would.
	l.Release(context.Background(), view, first, fact.Outcome{}, &collectSink{})

	second, held := l.Admit(context.Background(), view, &collectSink{})
	if !held {
		t.Fatal("the second token was gone: the refused request spent one")
	}
	l.Release(context.Background(), view, second, fact.Outcome{}, &collectSink{})

	if _, held := l.Admit(context.Background(), view, &collectSink{}); held {
		t.Fatal("a third request was admitted: the minute allows only two")
	}
}

// TestRPMRejectionGivesBackTheConcurrencySlot: the slot was taken moments
// earlier for a request that will not be served, so it goes back immediately
// rather than waiting for a release pass that only runs for admitted requests.
func TestRPMRejectionGivesBackTheConcurrencySlot(t *testing.T) {
	l := NewLimiter()
	view := stubView{keyID: 7, conc: 1, rpm: 1}

	first, held := l.Admit(context.Background(), view, &collectSink{})
	if !held {
		t.Fatal("the first request should have been admitted")
	}
	l.Release(context.Background(), view, first, fact.Outcome{}, &collectSink{})

	// RPM is now exhausted; this refusal must not leave the slot occupied.
	sink := &collectSink{}
	if _, held := l.Admit(context.Background(), view, sink); held {
		t.Fatal("the second request should have been refused on RPM")
	}
	if len(sink.facts) != 1 {
		t.Fatalf("want one fact, got %v", sink.facts)
	}
	if !l.AcquireConcurrency(7, 1) {
		t.Fatal("the concurrency slot was left occupied by an RPM refusal")
	}
}

// TestUnlimitedKeyHoldsNothing: releasing a slot that was never taken would
// give back a resource the request never had.
func TestUnlimitedKeyHoldsNothing(t *testing.T) {
	l := NewLimiter()
	ticket, held := l.Admit(context.Background(), stubView{keyID: 9}, &collectSink{})
	if !held {
		t.Fatal("an unlimited key must be admitted")
	}
	if ticket.heldSlot {
		t.Error("an unlimited key must not report holding a concurrency slot")
	}
}
