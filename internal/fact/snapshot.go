package fact

import (
	"net/http"
	"time"
)

// The types below are immutable snapshots the kernel hands to an extension
// point. They are values, not views onto live state: a capability that holds
// one from a previous attempt sees what was true then, never torn state from an
// attempt in flight.
//
// A capability that needs less than a whole snapshot declares its own narrow
// interface locally and takes that instead; these types are the ready-made
// option for capabilities that do not care.
//
// Several accessors here have no caller in this build. That is deliberate and
// they stay: these snapshots are the read-only surface capabilities are given
// instead of the exchange itself, and the capabilities that read the rest of
// it are the ones not yet built against this kernel. Removing an accessor
// because this build's capabilities happen not to need it would make the
// surface a mirror of today's consumers rather than a contract for the next
// ones.

// Request describes the caller's request for the whole exchange.
type Request struct {
	requestID   string
	ingress     string
	ingressPath string
	model       string
	isStream    bool
	isChat      bool
	deadline    time.Time
	startedAt   time.Time
}

// NewRequest builds a Request snapshot. Only the kernel calls it; the fields
// stay unexported so a capability cannot fabricate a snapshot that claims to
// describe an exchange it is not part of.
func NewRequest(requestID, ingress, ingressPath, model string, isStream, isChat bool, deadline, startedAt time.Time) Request {
	return Request{
		requestID:   requestID,
		ingress:     ingress,
		ingressPath: ingressPath,
		model:       model,
		isStream:    isStream,
		isChat:      isChat,
		deadline:    deadline,
		startedAt:   startedAt,
	}
}

func (r Request) RequestID() string    { return r.requestID }
func (r Request) Ingress() string      { return r.ingress }
func (r Request) IngressPath() string  { return r.ingressPath }
func (r Request) Model() string        { return r.model }
func (r Request) IsStream() bool       { return r.isStream }
func (r Request) IsChatEndpoint() bool { return r.isChat }
func (r Request) Deadline() time.Time  { return r.deadline }
func (r Request) StartedAt() time.Time { return r.startedAt }

// Attempt is a Request plus the target of one attempt.
type Attempt struct {
	Request
	attemptNo     int
	candidate     Candidate
	egress        string
	passthrough   bool
	upstreamModel string
}

// NewAttempt builds an Attempt snapshot. Kernel only, for the same reason as
// NewRequest.
func NewAttempt(r Request, attemptNo int, cand Candidate, egress, upstreamModel string, passthrough bool) Attempt {
	return Attempt{
		Request:       r,
		attemptNo:     attemptNo,
		candidate:     cand,
		egress:        egress,
		upstreamModel: upstreamModel,
		passthrough:   passthrough,
	}
}

func (a Attempt) AttemptNo() int        { return a.attemptNo }
func (a Attempt) Candidate() Candidate  { return a.candidate }
func (a Attempt) Egress() string        { return a.egress }
func (a Attempt) UpstreamModel() string { return a.upstreamModel }
func (a Attempt) Passthrough() bool     { return a.passthrough }

// Candidate identifies one routable destination.
type Candidate struct {
	ProviderID   uint
	CandidateID  uint
	ProviderType string
	ProviderName string
}

// Upstream is one complete upstream response. Body has already been read under
// a bound, so an observer never decides how much to read.
type Upstream struct {
	StatusCode int
	Header     http.Header
	Body       []byte
	Elapsed    time.Duration
}

// Frame is one forwarded chunk of a streamed response.
type Frame struct {
	Line       []byte
	Index      int
	SinceStart time.Duration
}

// StreamEnd describes how a stream finished.
type StreamEnd struct {
	Complete bool
	Err      error
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

// Compensation tells a settler why its reservation is being reversed. Cause is
// the routing fact that ended the exchange, so a reversal can be explained in
// the audit row without the settler having to guess.
type Compensation struct {
	Reason string
	Cause  Kind
}
