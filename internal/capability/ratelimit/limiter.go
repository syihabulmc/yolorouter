package ratelimit

import (
	"context"
	"sync"
	"time"

	"github.com/yolorouter/yolorouter/internal/fact"
)

// minuteWindow is a per-key fixed-minute counter used for RPM. A fixed
// window (not sliding) is deliberately coarse — RPM's job is to protect the
// upstream from a single hot key, not to bill precisely, and the boundary
// jitter is acceptable. The minute key is unix epoch / 60 so it's
// timezone-independent (the "today by system timezone" rule is about
// the dashboard, not the rate counter).
type minuteWindow struct {
	minute int64
	count  int
}

// allow increments the counter for the current minute if the limit has not
// been hit yet. Returns false without incrementing when over the limit.
func (w *minuteWindow) allow(limit int, now time.Time) bool {
	m := now.Unix() / 60
	if w.minute != m {
		w.minute = m
		w.count = 0
	}
	if w.count >= limit {
		return false
	}
	w.count++
	return true
}

// keySlot is one API key's in-memory rate/concurrency state. Both fields are
// guarded by the slot's own mutex, not a global one, so two keys never
// contend on each other's accounting.
type keySlot struct {
	mu   sync.Mutex
	conc int
	rpm  minuteWindow
}

// Limiter enforces per-API-key concurrency + RPM in memory. v0.1 is
// process-local: a single-binary deployment has one gateway, so per-process
// counters match per-instance state. TPM pre-enforcement needs prompt-token
// estimation and is deferred.
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

// The reason codes this capability persists. They are part of the contract with
// everything that reads a failure reason — the dashboard and the request log
// viewer map them to localised text — so they are declared as constants here
// and must not change without changing those readers too. Distinguishing the
// two matters: a concurrency wall and a per-minute wall call for different
// fixes.
const (
	ReasonConcurrencyLimit = "concurrency_limit"
	ReasonRPMExceeded      = "rpm_exceeded"
)

// View is what this capability needs to know about the exchange: which key is
// asking, and what that key is allowed. A nil limit and a zero limit both mean
// unlimited — the caller's configuration says so, and this package does not
// second-guess it.
type View interface {
	APIKeyID() uint
	ConcurrencyLimit() int
	RPMLimit() int
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

// Admit takes a concurrency slot and spends an RPM token, in that order.
//
// The order matters and is the reason these two live in one admission rather
// than two: a request rejected for concurrency must NOT also burn an RPM token.
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

// Release returns the concurrency slot. The outcome is not consulted: a slot is
// occupied for exactly as long as the request runs, however it ends.
func (l *Limiter) Release(_ context.Context, _ View, ticket Ticket, _ fact.Outcome, _ fact.Sink) {
	if ticket.heldSlot {
		l.ReleaseConcurrency(ticket.apiKeyID)
	}
}
