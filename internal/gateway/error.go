package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/yolorouter/yolorouter/internal/decision"
	"github.com/yolorouter/yolorouter/internal/fact"
)

// relayContextKey is the gin.Context key Handle stores the in-flight
// Exchange under (relay.go: c.Set(relayContextKey, rc)), so
// WriteOpenAIError* can stash the local error JSON it is about to return
// into the response-body capture without threading an
// *Exchange parameter through every call site. Absent on paths that
// never call Handle (e.g. unit tests, or middleware.APIKeyAuth's own 401s
// before Handle ever runs) — stashLocalErrorBody is then a no-op.
const relayContextKey = "relay_context"

// openaiErrorBody is the OpenAI-compatible error envelope. Gateway traffic
// uses upstream's native wire format, NOT pkg/response — so
// these responses intentionally do not carry the admin API's Code/Message
// envelope.
type openaiErrorBody struct {
	Error openaiError `json:"error"`
}

type openaiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
}

// LocalErrorBody serializes the OpenAI-compatible error envelope used by
// WriteOpenAIError/WriteOpenAIErrorWithRequestID. Exported so
// middleware.logAuthRejection (a different package, rejecting requests
// before Handle — and any Exchange — ever exists) can build the exact
// same response_body JSON for its own request_log_bodies row instead of
// duplicating the envelope shape (single source of truth for
// shared logic).
func LocalErrorBody(errType, message string) []byte {
	b, _ := json.Marshal(openaiErrorBody{Error: openaiError{Message: message, Type: errType}})
	return b
}

// relayContextFrom retrieves the in-flight Exchange Handle stashed on c
// (see relayContextKey), or nil when none is present — e.g. a path that
// never called Handle (unit tests, or an early 401 in middleware.APIKeyAuth
// before Handle ever runs). The single lookup + two-step type assertion both
// stashLocalErrorBody (here) and stashLocalClaudeErrorBody (ingress_error.go)
// need.
func relayContextFrom(c *gin.Context) *Exchange {
	v, ok := c.Get(relayContextKey)
	if !ok {
		return nil
	}
	rc, _ := v.(*Exchange)
	return rc
}

// stashLocalErrorBody records the local error JSON WriteOpenAIError is about
// to return, as response_body for this request's request_log_bodies row
// No-op when no Exchange is on the context. The
// body is a gateway-generated error envelope (no caller/upstream content), so
// it is stored verbatim — v0.1 does not scrub body content.
func stashLocalErrorBody(c *gin.Context, errType, message string) {
	rc := relayContextFrom(c)
	if rc == nil {
		return
	}
	rc.bodies.SetResponse(LocalErrorBody(errType, message))
}

// Caller-facing error "type" values. The canonical constants live in the
// decision package — its table speaks them — and are aliased here so the many
// envelope call sites in this package stay short.
const (
	errTypeAuthentication    = decision.ErrTypeAuthentication
	errTypePermission        = decision.ErrTypePermission
	errTypeRateLimit         = decision.ErrTypeRateLimit
	errTypeInvalidRequest    = decision.ErrTypeInvalidRequest
	errTypeNotFound          = decision.ErrTypeNotFound
	errTypeUpstream          = decision.ErrTypeUpstream
	errTypeServer            = decision.ErrTypeServer
	errTypeUnavailable       = decision.ErrTypeUnavailable
	errTypeInsufficientQuota = decision.ErrTypeInsufficientQuota
)

// StatusClientClosedRequest is the non-standard status used to record that the
// caller went away before the response was delivered. It is never written to
// the wire — there is no caller left to receive it — but it must be
// distinguishable in the audit row from a gateway fault, because the two demand
// opposite responses from whoever reads it.
const StatusClientClosedRequest = decision.StatusClientClosedRequest

// WriteOpenAIError writes one OpenAI-compatible error response and aborts
// the chain. status is the HTTP status; errType is the error.type string;
// message is shown verbatim to the caller.
func WriteOpenAIError(c *gin.Context, status int, errType, message string) {
	stashLocalErrorBody(c, errType, message)
	c.AbortWithStatusJSON(status, openaiErrorBody{
		Error: openaiError{Message: message, Type: errType},
	})
}

// AppendRequestID appends " (request: <id>)" to message so a caller
// reporting an error can quote the id and the admin can find the row. A no-op
// (returns message unchanged) when requestID is empty. Shared by every call
// site that builds this exact suffix, so they can't drift out of sync.
func AppendRequestID(message, requestID string) string {
	if requestID == "" {
		return message
	}
	return message + " (request: " + requestID + ")"
}

// WriteOpenAIErrorWithRequestID is WriteOpenAIError with the request id
// appended to the message, so a caller reporting an error can quote the id
// and the admin can find the row.
func WriteOpenAIErrorWithRequestID(c *gin.Context, status int, errType, message, requestID string) {
	message = AppendRequestID(message, requestID)
	WriteOpenAIError(c, status, errType, message)
}

// statusCategory classifies a non-2xx upstream HTTP status into the relay
// loop's three branches: rotate to another Key on the same
// provider, failover to the next candidate, or surface as terminal (no
// switch).
type statusCategory int

const (
	statusRotateKey      statusCategory = iota // 401/429: Key-scoped, try next key
	statusFailover                             // 5xx: provider-scoped, try next candidate
	statusTerminalClient                       // other 4xx: caller's problem, no switch
)

// ErrorType is stated on EVERY classification, including the two that used to
// leave it empty because their callers never read it. A verdict a capability
// reports can now carry any of these statuses to the terminal, and an empty
// error type reaches the caller as `"type": ""` — a field their client library
// will branch on and find nothing. What used to protect this was a caller that
// only ever asked on the one path that filled it in; that protection is not
// something the type can state, so the type states the value instead.
//
// upstreamStatusClass is the full classification attemptOne needs from one
// upstream HTTP status: which branch to take, what outcome label to log,
// and (for terminal 4xx) which OpenAI error type to surface.
type upstreamStatusClass struct {
	Category  statusCategory
	Outcome   string
	ErrorType string
}

// kindForUpstreamStatus is the kernel's own reading of a non-2xx upstream
// status, spoken in the fact vocabulary so the decision table can resolve it
// like any other report. It exists because the kernel is itself a reporter:
// when no observer recognised anything in the response body, the status line
// is the only evidence there is, and the routing it implies must come out of
// the same table as everything else rather than out of a parallel switch.
//
// The mapping mirrors classifyUpstreamStatus, which keeps the label-side
// vocabulary (attempt outcome, caller-facing error type); the two must agree
// on which statuses mean what, and a test holds them together.
func kindForUpstreamStatus(status int) fact.Kind {
	switch {
	case status == http.StatusUnauthorized:
		return fact.KindUpstreamAuthRejected
	case status == http.StatusTooManyRequests:
		return fact.KindUpstreamRateLimited
	case status >= 500:
		return fact.KindUpstreamServerError
	default:
		return fact.KindUpstreamClientError
	}
}

// kernelUpstreamFact is the kernel's baseline report for a non-2xx response,
// built whole in one place so the fact and its persisted audit code cannot
// drift apart.
//
// Reason is set explicitly for every reading that can end up persisted as a
// fail_reason — a client error surfaced to the caller, a rate limit quoted
// when the chain exhausts. The persisted column is a contract with dashboards
// and log viewers, so the code must never be derived from the Kind's name:
// Kind names are internal and get renamed as the vocabulary is refined, and a
// rename must not silently change a column somebody queries. Readings that
// are never persisted leave Reason empty.
func kernelUpstreamFact(status int) fact.Fact {
	kind := kindForUpstreamStatus(status)
	switch kind {
	case fact.KindUpstreamRateLimited:
		return fact.Fact{Kind: kind, Status: status, Reason: "upstream_rate_limited"}
	case fact.KindUpstreamClientError:
		return fact.Fact{
			Kind:   kind,
			Status: status,
			Reason: fmt.Sprintf("upstream_client_error_%d", status),
		}
	default:
		return fact.Fact{Kind: kind, Status: status}
	}
}

// classifyUpstreamStatus maps a non-2xx upstream status to its relay
// classification. One call site (attemptOne), one source of truth —
// replaces the former statusIsKeyRotation / statusIsCandidateFailover /
// clientErrorTypeFor / keyOutcome quartet that was spread across two files
// and had already drifted (the candidate/client branches hardcoded outcome
// labels while the rotate branch used a separate keyOutcome helper).
//
// 403 is intentionally NOT a rotate-Key status: a 403 from an
// OpenAI-compatible provider is usually account/permission scoped (the whole
// provider is forbidden), so rotating Keys within it is futile and we fall
// through to terminal.
func classifyUpstreamStatus(status int) upstreamStatusClass {
	switch {
	case status == http.StatusUnauthorized, status == http.StatusTooManyRequests:
		outcome, errType := AttemptAuthFailed, errTypeAuthentication
		if status == http.StatusTooManyRequests {
			outcome, errType = AttemptRateLimited, errTypeRateLimit
		}
		return upstreamStatusClass{Category: statusRotateKey, Outcome: outcome, ErrorType: errType}
	case status >= 500:
		return upstreamStatusClass{Category: statusFailover, Outcome: AttemptServerError, ErrorType: errTypeUpstream}
	default:
		errType := errTypeInvalidRequest
		switch status {
		case http.StatusNotFound:
			errType = errTypeNotFound
		case http.StatusForbidden:
			errType = errTypePermission
		}
		return upstreamStatusClass{Category: statusTerminalClient, Outcome: AttemptClientError, ErrorType: errType}
	}
}
