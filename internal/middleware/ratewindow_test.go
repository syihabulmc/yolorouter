package middleware

import (
	"fmt"
	"testing"
	"time"
)

func TestRateWindowEnforcesLimitAndResets(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := NewRateWindow(3, time.Minute)

	for i := range 3 {
		if !r.Allow(now) {
			t.Fatalf("call %d within limit must be allowed", i+1)
		}
	}
	if r.Allow(now) {
		t.Fatalf("call over the limit must be rejected")
	}
	// Still inside the same window a second later.
	if r.Allow(now.Add(time.Second)) {
		t.Fatalf("call over the limit must stay rejected within the window")
	}
	// A fresh window admits calls again.
	if !r.Allow(now.Add(time.Minute)) {
		t.Fatalf("call in the next window must be allowed")
	}
}

func TestPerClientRateWindowIsolatesClients(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := NewPerClientRateWindow(2, time.Minute)

	for i := range 2 {
		if !r.Allow("a", now) {
			t.Fatalf("client a's call %d within budget must be allowed", i+1)
		}
	}
	if r.Allow("a", now) {
		t.Fatalf("client a over budget must be rejected")
	}
	// Client b's budget is untouched by a's exhaustion — the whole point
	// of the per-client layer.
	if !r.Allow("b", now) {
		t.Fatalf("client b must not be starved by client a")
	}
	// a's next window admits again.
	if !r.Allow("a", now.Add(time.Minute)) {
		t.Fatalf("client a must be admitted in the next window")
	}
}

// TestPerClientRateWindowBoundsTrackingMap pins the memory-bound promise:
// once the map is at capacity with live (unexpired) entries, calls from
// brand-new keys are rejected instead of growing the map, while
// established keys keep their budgets; after the window expires, the
// sweep frees room and new keys are admitted again.
func TestPerClientRateWindowBoundsTrackingMap(t *testing.T) {
	now := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	r := NewPerClientRateWindow(5, time.Minute)

	for i := range perClientMapCap {
		if !r.Allow(fmt.Sprintf("client-%d", i), now) {
			t.Fatalf("filling call %d must be allowed", i)
		}
	}
	if len(r.clients) != perClientMapCap {
		t.Fatalf("expected map at cap %d, got %d", perClientMapCap, len(r.clients))
	}

	if r.Allow("fresh-key", now.Add(time.Second)) {
		t.Fatalf("a new key at capacity with live entries must be rejected")
	}
	if len(r.clients) != perClientMapCap {
		t.Fatalf("rejected new key must not grow the map, got %d", len(r.clients))
	}
	if !r.Allow("client-0", now.Add(time.Second)) {
		t.Fatalf("an established key must keep working at capacity")
	}

	// Once the old window expires, the sweep clears room.
	if !r.Allow("fresh-key", now.Add(2*time.Minute)) {
		t.Fatalf("a new key must be admitted after expired entries are swept")
	}
	if len(r.clients) > perClientMapCap {
		t.Fatalf("map exceeded its bound after sweep: %d", len(r.clients))
	}
}
