package ratelimit

import (
	"context"
	"math"
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

// stubView answers the questions this capability asks of an exchange.
type stubView struct {
	keyID uint
	conc  int
	rpm   int
	tpm   int
}

func (v stubView) APIKeyID() uint        { return v.keyID }
func (v stubView) ConcurrencyLimit() int { return v.conc }
func (v stubView) RPMLimit() int         { return v.rpm }
func (v stubView) TPMLimit() int         { return v.tpm }

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

// TPM is debited after serving, so the sequence is: admit freely while the
// minute's tally is under the limit, debit what was actually used, and
// reject once the tally reaches it.
func TestTPMRejectsOnceTheMinuteTallyReachesTheLimit(t *testing.T) {
	l := NewLimiter()
	view := stubView{keyID: 1, tpm: 100}

	ticket, ok := l.Admit(context.Background(), view, &collectSink{})
	if !ok {
		t.Fatal("first request must be admitted — nothing has been spent yet")
	}
	l.Release(context.Background(), view, ticket, fact.Outcome{Usage: &fact.UsageReported{Total: 100}}, &collectSink{})

	sink := &collectSink{}
	if _, ok := l.Admit(context.Background(), view, sink); ok {
		t.Fatal("the minute's tally is at the limit — the next request must be rejected")
	}
	if len(sink.facts) != 1 || sink.facts[0].Reason != ReasonTPMExceeded {
		t.Fatalf("facts = %+v, want one %s", sink.facts, ReasonTPMExceeded)
	}
}

// A failed or unbilled request settles no usage and must not debit the
// window: rejecting callers for tokens nobody was billed for is a phantom
// limit.
func TestTPMDoesNotDebitWhenNothingSettled(t *testing.T) {
	l := NewLimiter()
	view := stubView{keyID: 1, tpm: 1}

	ticket, ok := l.Admit(context.Background(), view, &collectSink{})
	if !ok {
		t.Fatal("admit failed on a fresh window")
	}
	l.Release(context.Background(), view, ticket, fact.Outcome{Usage: nil}, &collectSink{})

	if _, ok := l.Admit(context.Background(), view, &collectSink{}); !ok {
		t.Fatal("nothing settled, so the window must still be empty")
	}
}

// The window is a fixed minute: a tally that blocks now admits again once
// the minute rolls.
func TestTPMWindowRolls(t *testing.T) {
	l := NewLimiter()
	const key uint = 1
	base := time.Unix(0, 0)
	l.AddTokens(key, 100, base)
	if l.CheckTPM(key, 100, base) {
		t.Fatal("tally at the limit must block within the same minute")
	}
	if !l.CheckTPM(key, 100, base.Add(time.Minute)) {
		t.Fatal("a new minute must admit again")
	}
}

// A TPM rejection must not burn an RPM token — TPM's check is read-only and
// runs before RPM's spend. With one RPM token and an exhausted TPM window,
// the rejected request leaves the RPM token intact for when TPM clears.
func TestTPMRejectionDoesNotBurnAnRPMToken(t *testing.T) {
	l := NewLimiter()
	blocked := stubView{keyID: 1, rpm: 1, tpm: 10}
	seed, ok := l.Admit(context.Background(), blocked, &collectSink{})
	if !ok {
		t.Fatal("seed admit failed")
	}
	l.Release(context.Background(), blocked, seed, fact.Outcome{Usage: &fact.UsageReported{Total: 10}}, &collectSink{})

	// The counters are limit-agnostic: the slot tracks what was spent, and
	// each Admit judges that spend against whatever allowance its view
	// declares. Declaring an allowance of 2 against the 1 token the seed
	// spent leaves exactly one token — the one a TPM rejection must not
	// burn — which is what makes the burn observable at all.
	view := stubView{keyID: 1, rpm: 2, tpm: 10}
	if _, ok := l.Admit(context.Background(), view, &collectSink{}); ok {
		t.Fatal("TPM exhausted — must reject")
	}
	// Lift TPM: the remaining RPM token must still be there. If the TPM
	// rejection had burnt it, this admit fails.
	cleared := stubView{keyID: 1, rpm: 2}
	if _, ok := l.Admit(context.Background(), cleared, &collectSink{}); !ok {
		t.Fatal("the TPM rejection burnt the RPM token it must not spend")
	}
}

// A TPM rejection gives back the concurrency slot it took a moment earlier,
// exactly as an RPM rejection does.
func TestTPMRejectionReleasesTheConcurrencySlot(t *testing.T) {
	l := NewLimiter()
	exhausted := stubView{keyID: 1, conc: 1, tpm: 5}
	seed, ok := l.Admit(context.Background(), exhausted, &collectSink{})
	if !ok {
		t.Fatal("seed admit failed")
	}
	l.Release(context.Background(), exhausted, seed, fact.Outcome{Usage: &fact.UsageReported{Total: 5}}, &collectSink{})

	if _, ok := l.Admit(context.Background(), exhausted, &collectSink{}); ok {
		t.Fatal("TPM exhausted — must reject")
	}
	// The slot must have been returned: with TPM lifted, the only slot is free.
	cleared := stubView{keyID: 1, conc: 1}
	if _, ok := l.Admit(context.Background(), cleared, &collectSink{}); !ok {
		t.Fatal("the TPM rejection kept the concurrency slot it must return")
	}
}

// An upstream that reports prompt and completion but omits total must still
// debit the window. Taken verbatim, the zero total would silently switch TPM
// off for exactly that provider.
func TestTPMDebitsFromPromptPlusCompletionWhenTotalIsAbsent(t *testing.T) {
	l := NewLimiter()
	view := stubView{keyID: 1, tpm: 100}

	ticket, ok := l.Admit(context.Background(), view, &collectSink{})
	if !ok {
		t.Fatal("admit failed on a fresh window")
	}
	l.Release(context.Background(), view, ticket,
		fact.Outcome{Usage: &fact.UsageReported{Prompt: 60, Completion: 40, Total: 0}}, &collectSink{})

	if _, ok := l.Admit(context.Background(), view, &collectSink{}); ok {
		t.Fatal("prompt+completion reached the limit — the absent total must not erase the debit")
	}
}

// Cache reads and writes are real served volume — prompt is stored net of
// them, so a fallback that ignored the cache fields would exempt exactly the
// cache-heavy callers the limit is for.
func TestTPMDebitCountsCachedTokensWhenTotalIsAbsent(t *testing.T) {
	l := NewLimiter()
	view := stubView{keyID: 1, tpm: 100}
	ticket, ok := l.Admit(context.Background(), view, &collectSink{})
	if !ok {
		t.Fatal("admit failed on a fresh window")
	}
	l.Release(context.Background(), view, ticket,
		fact.Outcome{Usage: &fact.UsageReported{Prompt: 50, Completion: 20, CacheRead: 20, CacheWrite: 10, Total: 0}}, &collectSink{})

	if _, ok := l.Admit(context.Background(), view, &collectSink{}); ok {
		t.Fatal("net 70 plus 30 cached tokens reached the limit — cached volume must count")
	}
}

// The windows only roll forward. The timestamp is taken before the slot
// lock, so a caller that waited across a minute boundary arrives carrying
// the old minute — it must not clear what the new minute already counted.
func TestWindowsDoNotRollBackwardForStaleTimestamps(t *testing.T) {
	l := NewLimiter()
	const key uint = 1
	base := time.Unix(0, 0)
	later := base.Add(time.Minute)

	l.AddTokens(key, 100, later)
	if l.CheckTPM(key, 100, base) {
		t.Fatal("a stale check cleared the newer minute's token tally")
	}

	if !l.CheckRPM(key, 1, later) {
		t.Fatal("seed RPM spend failed")
	}
	if l.CheckRPM(key, 1, base) {
		t.Fatal("a stale check cleared the newer minute's request count")
	}
}

// Token counts arrive from third-party JSON. Absurd values must slam the
// window shut, not wrap the tally negative and reopen an exhausted key.
func TestTPMTallySaturatesInsteadOfWrapping(t *testing.T) {
	l := NewLimiter()
	const key uint = 1
	now := time.Unix(0, 0)
	l.AddTokens(key, math.MaxInt, now)
	l.AddTokens(key, math.MaxInt, now)
	if l.CheckTPM(key, 1_000_000, now) {
		t.Fatal("the tally wrapped negative and reopened the window")
	}
}
