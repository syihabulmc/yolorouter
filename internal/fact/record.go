package fact

import "time"

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

// UsageSource records where the numbers came from. An upstream-reported count
// and one the gateway derived from the request are both legitimate, but an
// audit needs to tell them apart.
type UsageSource uint8

const (
	UsageAbsent UsageSource = iota
	UsageFromUpstream
	UsageFromRequest
)

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
