package gateway

import (
	"errors"
	"testing"

	"github.com/yolorouter/yolorouter/internal/fact"
)

// TestAttemptOutcomeMatchesEveryExistingSite walks every place the dispatch
// paths write an attempt record today and checks the derived label is the one
// that site already writes.
//
// This is what makes moving the delivery paths behind the modality seam a
// refactor rather than a rewrite of what the attempts table says. Each case is
// named after the situation, and carries the Delivery that situation returns —
// so a row going red means the words in that table would change for real
// traffic, not that an abstraction moved.
//
// The rows are one per CALL SITE, not one per rule, and the two do not line up:
// most rows are decided by the fault before the streaming split is ever read,
// and several are byte-identical to each other because different sites end the
// same way. Coverage of the rules themselves is the three tests below, which is
// where to look for whether a rule is pinned.
func TestAttemptOutcomeMatchesEveryExistingSite(t *testing.T) {
	boom := errors.New("boom")

	cases := []struct {
		site   string
		stream bool
		d      fact.Delivery
		want   string
	}{
		// ── cross-protocol streaming ─────────────────────────────────────────
		{"ir stream delivered the whole response", true,
			fact.Succeeded(200), AttemptSuccess},
		{"ir stream failed before the first byte", true,
			fact.Undelivered(200, fact.VerdictNextCandidate, fact.FaultUpstream, "stream start: "+boom.Error(), boom),
			AttemptServerError},
		{"caller stopped reading mid-stream", true,
			fact.Truncated(200, 499, fact.FaultClient, "client_write_timeout", boom), AttemptConnError},
		{"caller's connection went away mid-stream", true,
			fact.Truncated(200, 499, fact.FaultClient, "client_disconnected", boom), AttemptConnError},
		{"upstream cut the stream mid-flight", true,
			fact.Truncated(200, 200, fact.FaultUpstream, "stream_partial: "+boom.Error(), boom),
			AttemptServerError},

		// ── cross-protocol non-streaming ─────────────────────────────────────
		{"ir decode failed after bytes had gone out", false,
			fact.Truncated(200, 200, fact.FaultUpstream, "ir_decode_partial: "+boom.Error(), boom),
			AttemptBadStatus},
		{"ir decode failed with nothing written", false,
			fact.Undelivered(200, fact.VerdictNextCandidate, fact.FaultUpstream, "ir_decode: "+boom.Error(), boom),
			AttemptBadStatus},
		{"ir non-stream delivered the whole response", false,
			fact.Succeeded(200), AttemptSuccess},
		// Shared by the cross-protocol write failure and both passthrough
		// non-stream write/flush failures — one combination, three sites.
		{"non-stream write to the caller failed", false,
			fact.Truncated(200, 499, fact.FaultClient, "client_write_timeout", boom), AttemptConnError},

		// ── same-protocol passthrough, non-streaming ─────────────────────────
		{"caller was gone before the body was read", false,
			fact.Undelivered(499, fact.VerdictSettled, fact.FaultClient, "client_disconnected", boom),
			AttemptConnError},
		{"reading the upstream body failed", false,
			fact.Undelivered(200, fact.VerdictNextCandidate, fact.FaultUpstream, "read_body: "+boom.Error(), boom),
			AttemptBadStatus},
		{"upstream response was too large", false,
			fact.Undelivered(200, fact.VerdictNextCandidate, fact.FaultUpstream, "response_too_large", nil),
			AttemptBadStatus},
		{"our own rewrite of the response failed", false,
			fact.Undelivered(200, fact.VerdictNextCandidate, fact.FaultGateway, "response_rewrite_failed: "+boom.Error(), boom),
			AttemptBadStatus},
		{"passthrough non-stream delivered the whole response", false,
			fact.Succeeded(200), AttemptSuccess},

		// ── same-protocol passthrough, streaming ─────────────────────────────
		{"passthrough stream delivered the whole response", true,
			fact.Succeeded(200), AttemptSuccess},
		{"caller left after the stream had started", true,
			fact.Truncated(200, 499, fact.FaultClient, "client_disconnected", boom), AttemptConnError},
		{"caller left before the stream had started", true,
			fact.Undelivered(499, fact.VerdictSettled, fact.FaultClient, "client_disconnected", boom),
			AttemptConnError},
		{"upstream ended the stream without its terminator", true,
			fact.Truncated(200, 200, fact.FaultUpstream, "stream_no_done", boom), AttemptServerError},
		{"passthrough stream failed before the first byte", true,
			fact.Undelivered(200, fact.VerdictNextCandidate, fact.FaultUpstream, "stream start: "+boom.Error(), boom),
			AttemptServerError},

		// ── not a call site: the conjunct no site happens to exercise ────────
		// Nothing in dispatch returns an uncommitted delivery that blames
		// nobody, so without this row the "did anything reach the caller" half
		// of the success test is unpinned -- and an attempt that delivered
		// nothing at all would be free to record itself as a success.
		{"nothing was delivered and nobody is to blame", false,
			fact.Undelivered(200, fact.VerdictNextCandidate, fact.FaultNone, "no_delivery", nil),
			AttemptBadStatus},
		{"nothing was delivered and nobody is to blame, streaming", true,
			fact.Undelivered(200, fact.VerdictNextCandidate, fact.FaultNone, "no_delivery", nil),
			AttemptServerError},
	}

	for _, tc := range cases {
		t.Run(tc.site, func(t *testing.T) {
			if got := attemptOutcomeFor(tc.d, tc.stream); got != tc.want {
				t.Errorf("outcome = %q, want %q — the attempts table would start saying something else here",
					got, tc.want)
			}
		})
	}
}

// TestStreamingIsWhatSeparatesTheTwoFailureLabels states the rule on its own,
// because the table above could stay green with the distinction hard-coded per
// case. Within the delivery paths the two labels differ by nothing except this.
//
// The shape is a delivery-path one on purpose. Written against the non-2xx
// failover shape it would look identical and mean something dangerous: that
// path records a server error whether or not the caller is streaming, so
// asserting bad_status for it here would hand a passing test to a change that
// routed status classification through this function.
func TestStreamingIsWhatSeparatesTheTwoFailureLabels(t *testing.T) {
	failed := fact.Undelivered(200, fact.VerdictNextCandidate, fact.FaultUpstream, "read_body: eof", nil)

	if got := attemptOutcomeFor(failed, true); got != AttemptServerError {
		t.Errorf("streaming failure = %q, want %q", got, AttemptServerError)
	}
	if got := attemptOutcomeFor(failed, false); got != AttemptBadStatus {
		t.Errorf("non-streaming failure = %q, want %q", got, AttemptBadStatus)
	}
}

// TestAClientFaultIsNeverBlamedOnTheProvider pins the one rule that outranks
// the streaming split. A caller who hangs up must not leave a record that reads
// like the provider broke: these rows are what a provider's health is judged
// from.
func TestAClientFaultIsNeverBlamedOnTheProvider(t *testing.T) {
	for _, stream := range []bool{true, false} {
		gone := fact.Truncated(200, 499, fact.FaultClient, "client_disconnected", nil)
		if got := attemptOutcomeFor(gone, stream); got != AttemptConnError {
			t.Errorf("stream=%v: outcome = %q, want %q", stream, got, AttemptConnError)
		}
	}
}

// TestAnImpossibleDeliveryIsNeverASuccess covers the order this function has to
// be called in.
//
// An attempt record is appended before the kernel refuses a delivery it cannot
// read. A delivery claiming bytes reached the caller AND that another candidate
// may still try is one of those — self-contradictory, and about to be replaced
// with a gateway failure. Calling it a success would leave a successful-looking
// attempt behind a request that settles as a 500, which hides the modality bug
// the check exists to surface.
func TestAnImpossibleDeliveryIsNeverASuccess(t *testing.T) {
	impossible := fact.Delivery{
		Committed: true, ClientStatus: 200, BillingStatus: 200,
		Verdict: fact.VerdictNextCandidate, Fault: fact.FaultNone,
	}
	if err := impossible.Validate(); err == nil {
		t.Fatal("this delivery is supposed to be one Validate refuses; the test no longer tests what it claims")
	}
	// The value is pinned, not merely "anything but success": an assertion that
	// only denies leaves the label free to move, and these two words mean
	// different things to whoever reads that table.
	for _, tc := range []struct {
		stream bool
		want   string
	}{
		{true, AttemptServerError},
		{false, AttemptBadStatus},
	} {
		if got := attemptOutcomeFor(impossible, tc.stream); got != tc.want {
			t.Errorf("stream=%v: outcome = %q, want %q for a delivery the kernel is about to refuse",
				tc.stream, got, tc.want)
		}
	}
}
