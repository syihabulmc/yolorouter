package gateway

import (
	"testing"

	"github.com/yolorouter/yolorouter/internal/fact"
)

// billedUsage returns the token counts the request was actually settled on, as
// they reached the audit trail.
//
// Read off the timeline rather than off a field on the exchange. What is billed
// travels with the delivery that ended the request and is reported to the sink
// on the way past; a copy parked on the exchange would let a test pass while
// the number that reaches the row is a different one. Returns nil when nothing
// was reported, which is what an unbilled request looks like.
func billedUsage(t *testing.T, rc *Exchange) *fact.UsageReported {
	t.Helper()
	var found *fact.UsageReported
	for _, e := range rc.timeline.All() {
		if u, ok := e.Record.(fact.UsageReported); ok {
			v := u
			found = &v
		}
	}
	return found
}

// requireBilled is billedUsage for the tests that only care that something was
// billed and what the counts were.
func requireBilled(t *testing.T, rc *Exchange) fact.UsageReported {
	t.Helper()
	u := billedUsage(t, rc)
	if u == nil {
		t.Fatal("nothing was billed: no usage reached the audit trail")
	}
	return *u
}
