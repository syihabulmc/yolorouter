package ratelimit

import (
	"context"
	"math"
	"sync"
	"time"

	"github.com/yolorouter/yolorouter/internal/fact"
)

// minuteWindow is a per-key fixed-minute counter used for RPM. A fixed
// window (not sliding) is deliberately coarse — RPM's job is to protect the
// upstream from a single hot key, not to bill precisely, and the boundary
// jitter is acceptable (the "today by system timezone" rule is about
// the dashboard, not the rate counter — the minute key is minuteOf).
type minuteWindow struct {
	minute int64
	count  int
}

// minuteOf is the shared fixed-minute key: unix epoch / 60, so it is
// timezone-independent and both windows agree on where a minute ends.
func minuteOf(now time.Time) int64 { return now.Unix() / 60 }

// satAdd sums token counts saturating at MaxInt, ignoring negatives. Counts
// arrive from third-party JSON, so an absurd value must slam the window shut,
// not wrap the tally negative and reopen an exhausted key — which is why this
// saturates instead of failing to zero the way the billing-side checked sum
// does: a zero here would be exactly that reopening.
func satAdd(a, b int) int {
	if b <= 0 {
		return a
	}
	if b > math.MaxInt-a {
		return math.MaxInt
	}
	return a + b
}

// allow increments the counter for the current minute if the limit has not
// been hit yet. Returns false without incrementing when over the limit.
func (w *minuteWindow) allow(limit int, now time.Time) bool {
	m := minuteOf(now)
	// Forward only: the timestamp is taken before the slot lock, so a caller
	// that waited across a minute boundary can arrive carrying the old
	// minute. Rolling backward would clear what the new minute has already
	// counted; a stale caller instead counts against the current window.
	if m > w.minute {
		w.minute = m
		w.count = 0
	}
	if w.count >= limit {
		return false
	}
	w.count++
	return true
}

// tokenWindow is the per-key fixed-minute token tally for TPM. Where RPM
// spends on admission, tokens are only known when the response has been
// served, so this window is debited after the fact: admission asks whether
// the current minute's tally is already at the limit, and settlement adds
// what the request actually used. A burst can therefore overshoot the limit
// once — by however much the in-flight requests use — and the window then
// rejects until it rolls. That is the honest trade for not estimating
// prompt tokens up front.
type tokenWindow struct {
	minute int64
	tokens int
}

// roll resets the tally when the minute has moved on. Forward only, for the
// same reason minuteWindow.allow is: a caller that waited out the slot lock
// across a minute boundary must not clear the new minute's tally.
func (w *tokenWindow) roll(now time.Time) {
	m := minuteOf(now)
	if m > w.minute {
		w.minute = m
		w.tokens = 0
	}
}

// keySlot is one API key's in-memory rate/concurrency state. All fields are
// guarded by the slot's own mutex, not a global one, so two keys never
// contend on each other's accounting.
type keySlot struct {
	mu   sync.Mutex
	conc int
	rpm  minuteWindow
	tpm  tokenWindow
}

// Limiter enforces per-API-key concurrency, RPM and TPM in memory. v0.1 is
// process-local: a single-binary deployment has one gateway, so per-process
// counters match per-instance state. TPM is enforced by debit-after-serve
// (see tokenWindow) rather than prompt-token estimation.
//
// Slots are stored in a sync.Map so a hot key's accounting never serializes
// against an unrelated key's slot lookup (a single shared mutex + map was
// the previous shape — three global-lock acquisitions per request).
type Limiter struct {
	keys sync.Map // map[uint]*keySlot
}

func NewLimiter() *Limiter {
	return &Limiter{}
}

func (l *Limiter) slot(apiKeyID uint) *keySlot {
	actual, _ := l.keys.LoadOrStore(apiKeyID, &keySlot{})
	return actual.(*keySlot)
}

// AcquireConcurrency tries to take one in-flight slot for this key. limit
// <= 0 means unlimited (always succeeds, caller still MUST pair with
// ReleaseConcurrency — Release is a no-op when nothing was counted).
func (l *Limiter) AcquireConcurrency(apiKeyID uint, limit int) bool {
	if limit <= 0 {
		return true
	}
	s := l.slot(apiKeyID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conc >= limit {
		return false
	}
	s.conc++
	return true
}

// ReleaseConcurrency returns one in-flight slot. Safe to call when the key
// had no limit (AcquireConcurrency returned true via the unlimited path) —
// the counter stays at 0 and the decrement is a guarded no-op.
func (l *Limiter) ReleaseConcurrency(apiKeyID uint) {
	s := l.slot(apiKeyID)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.conc > 0 {
		s.conc--
	}
}

// CheckRPM increments the key's per-minute counter. limit <= 0 = unlimited.
// Returns false without incrementing when the limit is already hit this
// minute.
func (l *Limiter) CheckRPM(apiKeyID uint, limit int, now time.Time) bool {
	if limit <= 0 {
		return true
	}
	s := l.slot(apiKeyID)
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rpm.allow(limit, now)
}

// CheckTPM reports whether the key's current minute still has token
// allowance. Read-only — nothing is spent here; the debit happens in
// Release with the usage the request actually settled on. limit <= 0 =
// unlimited.
func (l *Limiter) CheckTPM(apiKeyID uint, limit int, now time.Time) bool {
	if limit <= 0 {
		return true
	}
	s := l.slot(apiKeyID)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tpm.roll(now)
	return s.tpm.tokens < limit
}

// AddTokens debits served usage into the key's current minute.
func (l *Limiter) AddTokens(apiKeyID uint, tokens int, now time.Time) {
	if tokens <= 0 {
		return
	}
	s := l.slot(apiKeyID)
	s.mu.Lock()
	defer s.mu.Unlock()
	s.tpm.roll(now)
	s.tpm.tokens = satAdd(s.tpm.tokens, tokens)
}

// The reason codes this capability persists. They are part of the contract with
// everything that reads a failure reason — the dashboard and the request log
// viewer map them to localised text — so they are declared as constants here
// and must not change without changing those readers too. Distinguishing them
// matters: a concurrency wall, a request-rate wall and a token-volume wall
// each call for a different fix.
const (
	ReasonConcurrencyLimit = "concurrency_limit"
	ReasonRPMExceeded      = "rpm_exceeded"
	ReasonTPMExceeded      = "tpm_exceeded"
)

// View is what this capability needs to know about the exchange: which key is
// asking, and what that key is allowed. A nil limit and a zero limit both mean
// unlimited — the caller's configuration says so, and this package does not
// second-guess it.
type View interface {
	APIKeyID() uint
	ConcurrencyLimit() int
	RPMLimit() int
	TPMLimit() int
}

// Ticket records what an admitted request took, so releasing it gives back
// exactly that and nothing else. A request that was admitted without holding a
// concurrency slot — an unlimited key — must not decrement a counter it never
// incremented.
type Ticket struct {
	apiKeyID uint
	heldSlot bool
}

// Name identifies this admission in the audit trail.
func (l *Limiter) Name() string { return "ratelimit" }

// Admit takes a concurrency slot, checks the TPM window, and spends an RPM
// token, in that order.
//
// The order matters and is the reason these live in one admission rather
// than several: a request rejected for concurrency must NOT also burn an RPM token.
// Checked the other way round, a burst that is rejected on concurrency still
// consumes the minute's allowance, and a single served request can exhaust it
// for everyone. Splitting them into separately registered admissions would make
// that ordering a property of the assembly instead of a property of the rule,
// and the rule is not the assembler's to get wrong.
func (l *Limiter) Admit(_ context.Context, view View, sink fact.Sink) (Ticket, bool) {
	keyID := view.APIKeyID()
	ticket := Ticket{apiKeyID: keyID}

	if limit := view.ConcurrencyLimit(); limit > 0 {
		if !l.AcquireConcurrency(keyID, limit) {
			sink.Report(fact.Fact{
				Kind:   fact.KindCallerRateLimited,
				Reason: ReasonConcurrencyLimit,
				Detail: "concurrency limit exceeded",
			})
			return ticket, false
		}
		ticket.heldSlot = true
	}

	// TPM sits between the two spends because its check is read-only: a
	// request it rejects has taken nothing that needs refunding, and RPM's
	// spend — which cannot be un-spent — must come last so a TPM rejection
	// never burns a request token.
	if limit := view.TPMLimit(); limit > 0 {
		if !l.CheckTPM(keyID, limit, time.Now()) {
			sink.Report(fact.Fact{
				Kind:   fact.KindCallerRateLimited,
				Reason: ReasonTPMExceeded,
				Detail: "rate limit exceeded (tokens per minute)",
			})
			if ticket.heldSlot {
				l.ReleaseConcurrency(keyID)
				ticket.heldSlot = false
			}
			return ticket, false
		}
	}

	if limit := view.RPMLimit(); limit > 0 {
		if !l.CheckRPM(keyID, limit, time.Now()) {
			sink.Report(fact.Fact{
				Kind:   fact.KindCallerRateLimited,
				Reason: ReasonRPMExceeded,
				Detail: "rate limit exceeded (requests per minute)",
			})
			// The slot was taken a moment ago and this request will not be
			// served, so it goes back here rather than waiting for a release
			// pass that only runs for admitted requests.
			if ticket.heldSlot {
				l.ReleaseConcurrency(keyID)
				ticket.heldSlot = false
			}
			return ticket, false
		}
	}
	return ticket, true
}

// debitBasis is what a served request costs the window: the upstream's total
// when it stated one, otherwise the same gross identity billing uses —
// prompt (stored net of cache) plus completion plus both cache sides. Some
// OpenAI-compatible upstreams omit total_tokens, and taking the verbatim
// zero would switch TPM off for exactly those providers with no sign
// anything is wrong; summing without the cache fields would exempt exactly
// the cache-heavy callers, whose real volume is the point of the limit.
func debitBasis(u *fact.UsageReported) int {
	if u == nil {
		return 0
	}
	gross := 0
	for _, v := range []int{u.Prompt, u.Completion, u.CacheRead, u.CacheWrite} {
		gross = satAdd(gross, v)
	}
	if gross > u.Total {
		return gross
	}
	return u.Total
}

// Release returns the concurrency slot and debits the usage the exchange
// settled on into the TPM window. The debit reads the settled copy — the
// same counts the audit row persists — so the window cannot disagree with
// the books. Nothing settled (a failed or unbilled request) debits nothing.
func (l *Limiter) Release(_ context.Context, view View, ticket Ticket, outcome fact.Outcome, _ fact.Sink) {
	if ticket.heldSlot {
		l.ReleaseConcurrency(ticket.apiKeyID)
	}
	if view.TPMLimit() > 0 {
		l.AddTokens(ticket.apiKeyID, debitBasis(outcome.Usage), time.Now())
	}
}
