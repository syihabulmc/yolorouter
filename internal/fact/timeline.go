package fact

import "time"

// Entry wraps one reported observation with its provenance.
//
// Provenance lives here, once, rather than as three repeated fields on every
// observation type. That also means an observation is not self-locating: a
// reporter cannot ask which attempt its own earlier report belonged to. A
// capability that needs to correlate across attempts keeps that state itself.
type Entry struct {
	Attempt   int
	Candidate uint
	Provider  uint
	At        time.Time
	Reporter  string

	// Exactly one of these is set.
	Fact   *Fact
	Record Record
}

// Timeline is the append-only log of everything reported during one exchange.
// The kernel owns it and hands it to the terminal consumer, which is how the
// audit trail is assembled without that consumer knowing which capabilities
// exist.
//
// Append takes a pointer receiver and every reader takes a value one, which is
// what makes "append-only" true for anyone outside this package rather than
// merely intended: consumers are handed the Timeline by value, so the only
// method they can reach that would grow it grows their own copy.
type Timeline struct {
	entries []Entry
}

// Append adds an entry. Callers are the kernel only: capabilities report
// through a Sink, which is what keeps the ordering and provenance stamping in
// one place.
func (t *Timeline) Append(e Entry) {
	t.entries = append(t.entries, e)
}

// All returns every entry in report order.
//
// The slice, and the Fact each entry points at, are copies. Consumers run after
// the kernel has stopped deciding, so a stray write could not change routing —
// but it could change what the NEXT consumer reads, which would make the audit
// trail depend on the order consumers happen to be registered in. That is the
// same order dependence the fold is built to rule out, arriving through a later
// door.
//
// Records are handed over as stored. They are interface values, so this cannot
// copy them without knowing every implementation — and it does not need to: a
// Record belongs to the capability that reported it, so a consumer that mutates
// one corrupts only data it produced itself.
func (t Timeline) All() []Entry {
	out := make([]Entry, len(t.entries))
	copy(out, t.entries)
	for i := range out {
		if out[i].Fact != nil {
			f := *out[i].Fact
			out[i].Fact = &f
		}
	}
	return out
}

// OfAttempt returns the entries reported during one attempt.
func (t Timeline) OfAttempt(n int) []Entry {
	var out []Entry
	for _, e := range t.All() {
		if e.Attempt == n {
			out = append(out, e)
		}
	}
	return out
}

// Last returns the entries from the highest-numbered attempt that reported
// anything.
//
// Terminal decisions read this rather than scanning the whole timeline for "did
// any attempt ever see X". A chain that ends on a transport failure really is a
// fault on our side of the wire, whatever an earlier candidate thought of the
// payload — so only the final attempt's verdict may shape what the caller is
// told.
func (t Timeline) Last() []Entry {
	if len(t.entries) == 0 {
		return nil
	}
	last := t.entries[len(t.entries)-1].Attempt
	return t.OfAttempt(last)
}
