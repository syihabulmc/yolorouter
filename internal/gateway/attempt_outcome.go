package gateway

import "github.com/yolorouter/yolorouter/internal/fact"

// attemptOutcomeFor picks the label an attempt record carries, from what a
// delivery reported and whether the caller was being streamed to.
//
// The label is the kernel's vocabulary, not a modality's. Letting each modality
// choose its own would put the same question — "which of these five words is
// mine?" — in front of every future adapter author, and they would answer it
// differently; one entry point per opinion is the arrangement the modality seam
// exists to end. So a modality states what happened in its own terms and this
// translates, once.
//
// # Scope
//
// This is for the outcome of a DELIVERY, and nothing else: a 2xx upstream
// response that a modality then got, or failed to get, to the caller. Two other
// families of attempt record exist and must keep writing their own labels:
//
//   - Everything that fails before an upstream responds — a disabled provider,
//     a key that will not decrypt, a request that cannot be built. Those record
//     a status of 0 and never produce a Delivery at all.
//   - Non-2xx responses, whose label comes from classifying the status itself.
//
// Both write the same two words this function chooses between, and neither
// follows the streaming rule below. Routing them through here would relabel
// them, silently and in production.
//
// # Why streaming is an input
//
// The existing delivery records need it: every streaming failure that was
// neither the caller's doing nor a success is recorded as a server error, and
// every non-streaming one as a bad status. No Go code branches on the
// difference, but it is not invisible — the attempts table renders the two as
// different words to whoever is reading it ("Upstream error" against "Unknown
// status"), and that is what those rows have always said. A refactor is not the
// place to start saying something else.
//
// # Order
//
// Pass a delivery that has already been through the kernel's check and any
// reconciliation with the response. One that has not is a delivery whose own
// account may be about to be refused, and calling it a success would leave a
// successful-looking attempt behind a request the kernel then settles as a
// failure — hiding exactly the modality bug the check exists to surface. The
// guard below is a second line, not a substitute: only a delivery that could
// describe something real is allowed to be a success.
//
// AttemptContentFiltered is deliberately absent: it is decided from an
// upstream's error body before any delivery happens, and never from one.
func attemptOutcomeFor(d fact.Delivery, stream bool) string {
	switch {
	case d.Fault == fact.FaultNone && d.Committed && d.Validate() == nil:
		return AttemptSuccess
	case d.Fault == fact.FaultClient:
		return AttemptConnError
	case stream:
		return AttemptServerError
	default:
		return AttemptBadStatus
	}
}
