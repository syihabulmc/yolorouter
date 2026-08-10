package fact

import (
	"net/http"
	"time"
)

// The types below are immutable snapshots the kernel hands to an extension
// point. They are values, not views onto live state: a capability that holds
// one from a previous attempt sees what was true then, never torn state from
// an attempt in flight. A capability that needs anything else declares its own
// narrow view locally and binds it at assembly — the snapshot surface grows
// one consumer at a time, never ahead of them.

// Upstream is one complete upstream response. Body has already been read under
// a bound, so an observer never decides how much to read.
type Upstream struct {
	StatusCode int
	Header     http.Header
	Body       []byte
	Elapsed    time.Duration
}

// Outcome is the kernel's own terminal record — the part of the audit row no
// capability produced.
type Outcome struct {
	StatusCode  int
	FailReason  string
	Duration    time.Duration
	Attempts    int
	Delivered   bool
	UpstreamURL string
	// Usage is the billed usage the exchange settled on, nil when nothing
	// billable was delivered. This is the field a Release acts on: usage
	// present means the reservation settles into the actual charge, absent
	// means there is nothing to book and the reservation is reversed. It is
	// the settled copy — the same counts the audit row persists — so a
	// capability settling from it cannot disagree with the books.
	Usage *UsageReported
	// CostMicros is the priced cost of that usage in millionths of the
	// account currency, meaningful only when CostKnown. CostKnown false
	// means the exchange could not be priced — no usage, or no price on the
	// candidate — which is a different claim from a known zero-cost
	// exchange, and one a settlement must not bill as free.
	CostMicros int64
	CostKnown  bool
}
