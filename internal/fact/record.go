package fact

import (
	"strconv"
	"time"
)

// Record is an accounting observation. It never moves the relay loop; it exists
// so settlement and the audit trail can describe what happened.
//
// Unlike Kind, this half of the vocabulary is open. A consumer switches on the
// record types it knows and persists everything else verbatim under
// RecordName(), so a build can report a record type another build's kernel has
// never heard of and still have it survive into the audit row. That property is
// what keeps deployment-specific capabilities from forcing kernel changes.
//
// Records must be typed structs, never a generic map. The struct is what keeps
// a renamed field a compile error rather than a value that silently stops being
// recorded.
type Record interface {
	// RecordName is the stable identifier used when a consumer does not
	// recognise the type. It is persisted, so it must not change once shipped.
	RecordName() string
	isRecord()
}

// Base is the opt-in marker every record type embeds.
//
// It is exported for one specific reason: the unexported isRecord method it
// carries is what makes this vocabulary opt-in, but an unexported marker on an
// unexported base would make it CLOSED — no type outside this package could
// ever satisfy Record, and the ability to add record types without touching the
// kernel is the entire reason this half of the vocabulary exists.
//
// Exporting the base keeps both properties: satisfying Record still requires
// deliberately embedding Base, so no type satisfies it by accident, and any
// package may do so. The routing half above is the one that is genuinely
// closed; do not copy its sealing here.
type Base struct{}

func (Base) isRecord() {}

// UsageReported carries the billable quantities for one exchange.
//
// Unit is what lets a modality redefine what is being counted without a
// parallel set of fields: a text exchange counts tokens, a speech exchange
// counts characters through the same Prompt field, and settlement reads Unit to
// know which price column applies.
type UsageReported struct {
	Base
	Unit                  Unit
	Source                UsageSource
	Prompt                int
	Completion            int
	Total                 int
	CacheRead             int
	CacheWrite            int
	CacheIncludedInPrompt bool
	// Reasoning is the share of the completion the model spent thinking.
	// Carried for the same reason Incoherent below is: settlement re-derives
	// its coherence verdict from this record, and one of the rules — reasoning
	// cannot exceed the completion — reads this count. A hop that drops it
	// quietly weakens that verdict on the copy that is actually priced.
	Reasoning int
	// Incoherent marks counts that contradict themselves — a negative token
	// count, a cache read larger than the prompt it was read from.
	//
	// It travels WITH the counts rather than being re-derived downstream,
	// because the evidence does not survive: a stream folds every frame into
	// one accumulated record, so the impossible frame that condemned it is
	// gone by the time anything could look again. A consumer that re-judges
	// what it receives sees only the plausible-looking remainder and prices it.
	//
	// Unknown is not zero. A marked record must not be billed at all — not
	// billed as free, which is a different claim and one a dashboard adds up.
	Incoherent bool
	// WebSearchCount is how many searches the provider performed on its own
	// initiative during the exchange. Not a token count and not priced here —
	// it is carried because it is the only evidence that they happened.
	//
	// Providers charge for these separately, and the number arrives once, in
	// the usage the response ends with. A capability that adds the surcharge
	// sees the exchange after it has been delivered; a quantity dropped on the
	// way to that point is one nobody downstream can re-derive, because the
	// frames it came from are gone.
	WebSearchCount int
}

func (UsageReported) RecordName() string { return "usage_reported" }

// UsageIncoherent reports that the upstream's own usage numbers contradict
// themselves. Settlement must not bill on them; the audit row keeps them so the
// contradiction stays visible instead of being silently normalised away.
type UsageIncoherent struct {
	Base
	Reason string
}

func (UsageIncoherent) RecordName() string { return "usage_incoherent" }

// Unit names what a usage count counts.
type Unit uint8

const (
	UnitToken Unit = iota
	UnitCharacter
	UnitImage
	UnitSecond
)

// String renders a unit for persistence and for logs.
//
// Next to the constants on purpose: a switch over them living in whichever
// package happened to need a name is a switch nobody updates when a unit is
// added here. The strings are stored, so they must not change once shipped —
// which is also why the numeric values are not stored, since inserting a unit
// renumbers every one after it.
//
// An unrecognised unit renders as its number rather than falling back to a
// plausible name. A new unit persisted as "token" is a wrong value that reads
// as a right one; "unit_4" is obviously something this build did not know.
func (u Unit) String() string {
	switch u {
	case UnitToken:
		return "token"
	case UnitCharacter:
		return "character"
	case UnitImage:
		return "image"
	case UnitSecond:
		return "second"
	default:
		return "unit_" + strconv.Itoa(int(u))
	}
}

// UsageSource records where the numbers came from. An upstream-reported count
// and one the gateway derived from the request are both legitimate, but an
// audit needs to tell them apart.
type UsageSource uint8

const (
	UsageAbsent UsageSource = iota
	UsageFromUpstream
	UsageFromRequest
)

// String renders a usage source for persistence, under the same rule as Unit:
// stored as a name, and an unrecognised one shows as its number rather than
// borrowing a name that would read as deliberate.
func (s UsageSource) String() string {
	switch s {
	case UsageAbsent:
		return "absent"
	case UsageFromUpstream:
		return "upstream"
	case UsageFromRequest:
		return "request"
	default:
		return "source_" + strconv.Itoa(int(s))
	}
}

// Several record types below have no producer in this build. They stay for
// the same reason the unread snapshot accessors do: this vocabulary is the
// contract capabilities report through, and the capabilities that produce
// these records are the ones not yet built against this kernel. A consumer
// meeting an unknown record already has a defined path (the overflow column),
// so an unproduced type costs nothing at run time.

// TokensSaved reports a successful input compression pass.
type TokensSaved struct {
	Base
	Compressors     []string
	EstimatedTokens int
}

func (TokensSaved) RecordName() string { return "tokens_saved" }

// CompressionSkipped reports that compression was enabled but declined to act.
// Deliberately a separate type from TokensSaved rather than a zero value of it:
// "compressed nothing" and "did not compress" are different events, and a
// consumer that has to tell them apart by inspecting a count will eventually
// get it wrong.
type CompressionSkipped struct {
	Base
	Reason string
}

func (CompressionSkipped) RecordName() string { return "compression_skipped" }

// SystemPromptInjected reports that a configured system prompt was added, and
// how much text it contributed — the pre-request cost estimate needs the size,
// not the content.
type SystemPromptInjected struct {
	Base
	Site       string // where it landed in the request body
	ExtraChars int
}

func (SystemPromptInjected) RecordName() string { return "system_prompt_injected" }

// ModelRewritten reports that a model name was substituted.
type ModelRewritten struct {
	Base
	From  string
	To    string
	Where string // request or response
}

func (ModelRewritten) RecordName() string { return "model_rewritten" }

// FirstTokenAt reports how long the caller waited for the first meaningful byte
// of a streamed response.
type FirstTokenAt struct {
	Base
	Elapsed time.Duration
}

func (FirstTokenAt) RecordName() string { return "first_token_at" }

// FinishReasonObserved carries the raw stop signals seen on the wire. The
// normalisation into a small set of buckets belongs to whoever consumes this,
// not here: the rule for ranking an abnormal stop above an inferred tool call is
// a policy, and policies do not belong in the vocabulary.
type FinishReasonObserved struct {
	Base
	Raw            string
	SawToolCall    bool
	SawSemanticEnd bool
}

func (FinishReasonObserved) RecordName() string { return "finish_reason_observed" }

// RateLimitSnapshot carries the caller's remaining allowance at the moment the
// response was produced, for the compatibility headers and the audit row.
type RateLimitSnapshot struct {
	Base
	Limit     int
	Remaining int
	Unlimited bool
}

func (RateLimitSnapshot) RecordName() string { return "rate_limit_snapshot" }

// CostComputed carries what an exchange cost, once the kernel has priced it.
//
// It is a record rather than something a consumer recomputes because pricing
// and persistence must not be able to disagree: the number written to the audit
// row has to be the same number that was charged, and the only way to guarantee
// that is for both to read one value.
//
// Known distinguishes "cost nothing" from "could not be priced" — a request
// that never reached a priced candidate has no cost, which is not the same as a
// free one, and a dashboard that sums them together reports revenue that never
// existed.
type CostComputed struct {
	Base
	Known                   bool
	Micros                  int64
	CacheReadSavedMicros    int64
	CacheWriteExtraMicros   int64
	CompressCostSavedMicros int64
}

func (CostComputed) RecordName() string { return "cost_computed" }

// AttemptsRecorded carries how many upstream attempts ran and their detail.
//
// The count is what separates a request that reached an upstream from one
// rejected before any candidate was tried, which several consumers need:
// compression savings from a request that never left the building would inflate
// every savings metric that counts it.
type AttemptsRecorded struct {
	Base
	Count  int
	Detail string // JSON, empty when nothing ran
}

func (AttemptsRecorded) RecordName() string { return "attempts_recorded" }
