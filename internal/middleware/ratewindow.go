package middleware

import (
	"sync"
	"time"
)

// RateWindow is a fixed-window request counter: at most `limit` calls per
// `window`, globally. It complements Semaphore — the semaphore bounds how
// many requests run at once, this bounds how many run per unit time, which
// is what matters for endpoints whose per-call cost is a persisted row
// rather than CPU (a fast insert releases the semaphore immediately, so
// sequential spam sails through it).
//
// Fixed-window is deliberately the simplest correct shape here: the
// boundary burst it permits (2x limit across one boundary) is irrelevant
// at the generous limits this is used with, and it needs no per-caller
// state — these are anonymous endpoints, so per-IP budgets would be
// spoofable behind a proxy anyway.
type RateWindow struct {
	mu          sync.Mutex
	limit       int
	window      time.Duration
	windowStart time.Time
	count       int
}

// NewRateWindow returns a limiter allowing `limit` calls per `window`.
func NewRateWindow(limit int, window time.Duration) *RateWindow {
	return &RateWindow{limit: limit, window: window}
}

// Allow reports whether one more call fits into the current window,
// counting it if so.
func (r *RateWindow) Allow(now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if now.Sub(r.windowStart) >= r.window {
		r.windowStart = now
		r.count = 0
	}
	if r.count >= r.limit {
		return false
	}
	r.count++
	return true
}

// PerClientRateWindow is RateWindow keyed by client identity (an IP, as
// resolved by gin's ClientIP). It exists so one caller burning through
// its own budget cannot exhaust a shared global one and lock everyone
// else out — the global window stays as the absolute ceiling, this adds
// fairness under it. Client identity is spoofable behind a
// misconfigured proxy, which is why this is a fairness layer, never the
// only cap.
type PerClientRateWindow struct {
	mu      sync.Mutex
	limit   int
	window  time.Duration
	clients map[string]*clientWindow
}

type clientWindow struct {
	windowStart time.Time
	count       int
}

// perClientMapCap bounds the tracking map: when exceeded, expired entries
// are swept, so an attacker rotating spoofed identities grows memory only
// up to this bound plus one window's worth of live entries.
const perClientMapCap = 4096

// NewPerClientRateWindow returns a limiter allowing `limit` calls per
// `window` per client key.
func NewPerClientRateWindow(limit int, window time.Duration) *PerClientRateWindow {
	return &PerClientRateWindow{limit: limit, window: window, clients: make(map[string]*clientWindow)}
}

// Allow reports whether one more call from `key` fits its window,
// counting it if so. When the tracking map is at capacity and sweeping
// frees nothing (an attacker rotating spoofed identities inside one
// window), calls from NEW keys are rejected outright — the map can never
// exceed the cap plus one live window's churn, and established callers
// keep working.
func (r *PerClientRateWindow) Allow(key string, now time.Time) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	w := r.clients[key]
	if w == nil && len(r.clients) >= perClientMapCap {
		for k, cw := range r.clients {
			if now.Sub(cw.windowStart) >= r.window {
				delete(r.clients, k)
			}
		}
		if len(r.clients) >= perClientMapCap {
			return false
		}
	}
	if w == nil || now.Sub(w.windowStart) >= r.window {
		r.clients[key] = &clientWindow{windowStart: now, count: 1}
		return true
	}
	if w.count >= r.limit {
		return false
	}
	w.count++
	return true
}
