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
}
