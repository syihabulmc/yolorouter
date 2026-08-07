// Package fact is the vocabulary capabilities use to report what they observed
// to the gateway kernel. It is a leaf package — standard library only — so the
// kernel, capabilities, admission, settlement and modality adapters can all
// import it without forming a cycle.
//
// The vocabulary has two halves, and which half a new observation belongs in is
// decided by one question: can it change where the retry loop goes, or the
// status the caller sees?
//
//   - Yes -> it is a Kind, a member of the closed routing enum below. Adding one
//     is a kernel change: the enum value plus a row in the kernel's decision
//     table. The kernel asserts every Kind has a row, so a forgotten row is a
//     failing test rather than a silently inert fact.
//   - No -> it is a Record (see record.go), a named type. Consumers switch on
//     the types they know and persist the rest verbatim, so a build whose kernel
//     has never heard of a record type still stores it instead of dropping it.
//
// The split exists so a deployment can add capabilities that report new
// accounting observations without touching the kernel, while the half that
// actually steers requests stays small, closed and exhaustively checked.
package fact

// Kind enumerates the judgements that may move the relay loop.
//
// Naming rule, and it is load-bearing: a Kind is named after WHAT THE KERNEL
// DOES, never after the domain of whichever capability reports it. It is
// KindPayloadRepairedRetrySame, not KindImagesStripped; KindPayloadRefused, not
// KindContentInspectionRejected. That is what lets a build carry a Kind no
// capability in it can report — the Kind describes a kernel mechanism that
// remains meaningful on its own, not a feature that is missing.
type Kind uint16

const (
	// KindNone is the zero value and is never reported. It exists so an
	// uninitialised Fact cannot accidentally name a real judgement.
	KindNone Kind = iota

	// --- Admission verdicts: the request cannot proceed at all. ---

	// KindQuotaExhausted: the caller's allowance is used up.
	KindQuotaExhausted
	// KindBalanceInsufficient: the caller cannot pay for this request.
	KindBalanceInsufficient
	// KindCallerRateLimited: the caller is asking faster, or more
	// concurrently, than their key allows. Distinct from
	// KindUpstreamRateLimited, which is a provider throttling us: this one is
	// about the caller's own allowance, and no other candidate or key would
	// help.
	KindCallerRateLimited

	// --- Candidate verdicts: this candidate cannot serve this request. ---

	// KindPricingUnavailableTerminal: pricing is missing in a way that no other
	// candidate can fix, so the chain ends here.
	KindPricingUnavailableTerminal
	// KindPricingUnavailableSkip: pricing lookup failed for this candidate
	// alone; another candidate may still be priced.
	KindPricingUnavailableSkip
	// KindCandidateUnsupported: this candidate structurally cannot serve the
	// request (no codec, cannot stream, wrong provider type). Not a failure —
	// it costs a probe, not an attempt.
	KindCandidateUnsupported

	// --- Upstream verdicts derived from a response. ---

	// KindPayloadRefused: the upstream judged the payload's content rather than
	// its shape. Another provider whose moderation differs may well accept it,
	// so this is a failover, and it must not count against the provider's
	// circuit breaker: refusing a payload is the provider working correctly.
	KindPayloadRefused
	// KindPayloadRepairedRetrySame: something produced a repaired body for a
	// request the upstream just rejected. The kernel decides whether to spend an
	// attempt re-sending it to the SAME candidate; the reporter never decides
	// that. Meaningful with no reporter present: it is the general "a repair was
	// offered" mechanism.
	KindPayloadRepairedRetrySame
	// KindUpstreamAuthRejected: credentials were refused; another key may work.
	KindUpstreamAuthRejected
	// KindUpstreamRateLimited: the upstream is throttling this key.
	KindUpstreamRateLimited
	// KindUpstreamServerError: the upstream failed on its own side.
	KindUpstreamServerError
	// KindUpstreamClientError: the request itself is at fault; no other
	// candidate would do better.
	KindUpstreamClientError
	// KindUpstreamTransportFailure: the connection failed or timed out.
	KindUpstreamTransportFailure
	// KindUpstreamPayloadUndecodable: the response arrived but could not be
	// decoded into something the caller's protocol can express.
	KindUpstreamPayloadUndecodable
	// KindUpstreamSucceeded: the upstream served the request.
	KindUpstreamSucceeded

	// --- Verdicts about a response already being delivered. ---

	// KindUpstreamStreamTruncated: the stream ended without its terminator. The
	// caller already holds the bytes that did arrive, so this settles in place.
	KindUpstreamStreamTruncated
	// KindClientGone: the caller disconnected.
	KindClientGone
	// KindClientWriteFailed: writing to the caller failed. Distinct from
	// KindClientGone only in how it was detected; both blame the caller's socket
	// and neither may penalise the provider.
	KindClientWriteFailed

	// --- Kernel-internal verdicts. ---

	// KindKeyUnusable: the key could not be prepared (decrypt failure, stale
	// version). Costs no attempt budget — nothing was sent.
	KindKeyUnusable
	// KindRequestBudgetExhausted: the whole-request deadline passed.
	KindRequestBudgetExhausted
	// KindIngressRewriteFailed: a pre-dispatch body rewrite could not produce a
	// usable body.
	KindIngressRewriteFailed
	// KindEgressRewriteFailed: a per-attempt body rewrite could not produce a
	// usable body for this candidate.
	KindEgressRewriteFailed

	// numKinds bounds the kernel's decision table. Keep it last.
	numKinds
)

// NumKinds is the size a decision table must have. Exported so the kernel can
// declare a fixed-size array and prove at test time that every Kind has a row.
const NumKinds = int(numKinds)

// String names the Kind for logs. Deliberately not derived from the constant
// name: these strings are persisted, so they must not change when the constants
// are reordered.
func (k Kind) String() string {
	switch k {
	case KindQuotaExhausted:
		return "quota_exhausted"
	case KindBalanceInsufficient:
		return "balance_insufficient"
	case KindCallerRateLimited:
		return "caller_rate_limited"
	case KindPricingUnavailableTerminal:
		return "pricing_unavailable_terminal"
	case KindPricingUnavailableSkip:
		return "pricing_unavailable_skip"
	case KindCandidateUnsupported:
		return "candidate_unsupported"
	case KindPayloadRefused:
		return "payload_refused"
	case KindPayloadRepairedRetrySame:
		return "payload_repaired_retry_same"
	case KindUpstreamAuthRejected:
		return "upstream_auth_rejected"
	case KindUpstreamRateLimited:
		return "upstream_rate_limited"
	case KindUpstreamServerError:
		return "upstream_server_error"
	case KindUpstreamClientError:
		return "upstream_client_error"
	case KindUpstreamTransportFailure:
		return "upstream_transport_failure"
	case KindUpstreamPayloadUndecodable:
		return "upstream_payload_undecodable"
	case KindUpstreamSucceeded:
		return "upstream_succeeded"
	case KindUpstreamStreamTruncated:
		return "upstream_stream_truncated"
	case KindClientGone:
		return "client_gone"
	case KindClientWriteFailed:
		return "client_write_failed"
	case KindKeyUnusable:
		return "key_unusable"
	case KindRequestBudgetExhausted:
		return "request_budget_exhausted"
	case KindIngressRewriteFailed:
		return "ingress_rewrite_failed"
	case KindEgressRewriteFailed:
		return "egress_rewrite_failed"
	default:
		return "none"
	}
}

// Fact is one routing judgement.
//
// Kind is the only field the kernel's decision table is allowed to read. Reason
// is persisted, Detail is shown to the caller, and Reporter is for logs.
// Keeping the table keyed on Kind alone is what makes it enumerable: if a
// decision could also depend on Detail, the table would no longer be the
// complete description of the kernel's control flow.
type Fact struct {
	Kind Kind
	// Reporter names who observed it. Logs and metrics only.
	Reporter string
	// Status is the upstream HTTP status that produced this judgement, or 0 when
	// it did not come from a response.
	//
	// Nothing reads it today, and that is deliberate rather than an oversight:
	// the status a caller is shown comes from the response the kernel is
	// holding, not from what a reporter says about it. The two agree in
	// practice and the response is the one that cannot be wrong.
	//
	// It is kept as the reporter's own record of what it judged. A timeline
	// entry that says "this payload was refused" without saying what it saw is
	// an entry nobody can check against the upstream's own logs.
	Status int
	// Reason is the stable, persisted code for why this happened, when the Kind
	// alone is too coarse. A rate limit refusal is the motivating case: the Kind
	// says the caller was limited, but "which limit" is what an operator acts
	// on, and the two answers demand different fixes.
	//
	// It exists because the Kind name must NOT be used for this. Kind names are
	// internal and get renamed as the vocabulary is refined; a persisted column
	// read by dashboards and log viewers is a contract with everything
	// downstream. Tying one to the other means a rename silently breaks a
	// display nobody was looking at.
	Reason string
	// Detail is the reporter's own words for what happened, in prose. Never
	// parsed.
	//
	// It can reach the CALLER, not just the log: when this verdict is the one a
	// request ends on — an admission refusal, or a sticky verdict the chain ran
	// out on — the kernel puts it in the error body as the explanation. Other
	// endings substitute a generic message and it stays in the timeline only.
	// So write it for the person who made the request — say what was refused
	// and why they might fix it — and keep out of it anything they should not
	// see. An upstream's raw error text, a prompt excerpt, or an internal
	// identifier pasted in here is pasted into a response.
	Detail string
}

// Sink accepts reported observations.
//
// Facts passed to a single Report call form one BATCH. The kernel folds their
// decisions together rather than applying them in arrival order, so a reporter
// can state two orthogonal judgements at once — "this ends the chain" and "the
// caller should see 503" — without inventing a precedence rule of its own.
type Sink interface {
	Report(...Fact)
	// Note records an accounting observation. It can never move the relay loop,
	// which is why it is a separate method: the split is visible at the call
	// site, not buried in the type of the argument.
	Note(...Record)
}
