package gateway

import (
	"encoding/json"
	"net/http"

	"github.com/gin-gonic/gin"

	"github.com/yolorouter/yolorouter/internal/protocols"
)

// requestIDHeader is the response header name the RequestID middleware
// already sets on every response (see internal/middleware/request_id.go).
// Ingress error writers reuse this exact name instead of introducing a
// second id header, and set it defensively so it is present even on paths
// that bypass that middleware (unit tests, or a handler mounted without it).
const requestIDHeader = "X-Request-Id"

// anthropicErrorBody is the Anthropic-native error envelope: unlike
// openaiErrorBody (nested-only), the request id sits at the top level
// alongside a top-level "type":"error" discriminator, and the nested
// error.type uses Anthropic's own vocabulary (see anthropicErrorType)
// rather than the OpenAI errType* constants.
type anthropicErrorBody struct {
	Type      string         `json:"type"`
	RequestID string         `json:"request_id"`
	Error     anthropicError `json:"error"`
}

type anthropicError struct {
	Type    string `json:"type"`
	Message string `json:"message"`
}

// anthropicErrorType maps an internal OpenAI-shaped errType* constant (the
// single error classification produced by classifyUpstreamStatus and the
// gateway's own local-rejection call sites) to the closest valid Anthropic
// error type, so one internal classification can drive either ingress's
// wire envelope without every call site knowing both vocabularies.
func anthropicErrorType(errType string) string {
	switch errType {
	case errTypeAuthentication:
		return "authentication_error"
	case errTypePermission:
		return "permission_error"
	case errTypeInvalidRequest:
		return "invalid_request_error"
	case errTypeNotFound:
		return "not_found_error"
	case errTypeRateLimit:
		return "rate_limit_error"
	case errTypeInsufficientQuota:
		// Anthropic has no quota-specific error type; invalid_request_error
		// is the closest fit.
		return "invalid_request_error"
	case errTypeUnavailable:
		return "overloaded_error"
	case errTypeUpstream, errTypeServer:
		return "api_error"
	default:
		return "api_error"
	}
}

// LocalClaudeErrorBody serializes the Anthropic-native error envelope used
// by WriteClaudeError. Exported for the same reason LocalErrorBody is:
// callers building request_log_bodies.response_body outside of a
// WriteClaudeError call (or in a different package) need the exact same
// bytes instead of duplicating the envelope shape.
func LocalClaudeErrorBody(anthropicType, message, requestID string) []byte {
	b, _ := json.Marshal(anthropicErrorBody{
		Type:      "error",
		RequestID: requestID,
		Error:     anthropicError{Type: anthropicType, Message: message},
	})
	return b
}

// LocalIngressErrorBody returns the audit-log body for a locally-generated
// error on the given ingress: the Anthropic envelope for Claude traffic, the
// Gemini envelope for Gemini traffic, the existing OpenAI envelope otherwise.
// message must already reflect any ingress-specific transform
// (WriteIngressError appends the request id into message for the OpenAI
// ingress, not for Claude or Gemini) so the returned bytes match what was
// actually sent byte-for-byte. status is the HTTP status the error was (or
// will be) written with — only the Gemini envelope needs it (its "code" and
// "status" fields both derive from the HTTP status, not from errType); the
// Claude and OpenAI branches ignore it.
func LocalIngressErrorBody(ingress protocols.ProtocolID, status int, errType, message, requestID string) []byte {
	switch ingress {
	case protocols.ProtocolClaude:
		return LocalClaudeErrorBody(anthropicErrorType(errType), message, requestID)
	case protocols.ProtocolGemini:
		return LocalGeminiErrorBody(status, message, requestID)
	default:
		return LocalErrorBody(errType, message)
	}
}

// stashLocalClaudeErrorBody mirrors stashLocalErrorBody (error.go) for the
// Claude envelope, so request_log_bodies.response_body matches what
// WriteClaudeError actually sent when a Exchange is on the context.
func stashLocalClaudeErrorBody(c *gin.Context, anthropicType, message, requestID string) {
	rc := relayContextFrom(c)
	if rc == nil {
		return
	}
	rc.responseBody = LocalClaudeErrorBody(anthropicType, message, requestID)
}

// geminiErrorBody is the Google API error envelope: a single nested "error"
// object carrying code/message/status. Unlike anthropicErrorBody, there is no
// top-level request_id slot on the wire — the request id is only ever
// surfaced via the X-Request-Id response header (setRequestIDHeader), same as
// every other ingress.
type geminiErrorBody struct {
	Error geminiError `json:"error"`
}

type geminiError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Status  string `json:"status"`
}

// geminiErrorStatus maps an HTTP status to Google's canonical UPPER_SNAKE
// status string. Unlike anthropicErrorType (which maps from the internal
// errType* classification), this maps directly from the HTTP status: Gemini's
// wire "status" field is defined in terms of the response's HTTP status, not
// the gateway's own error classification, so deriving it from errType would
// require a second, redundant mapping that could drift from the status code
// actually written.
func geminiErrorStatus(httpStatus int) string {
	switch httpStatus {
	case http.StatusBadRequest:
		return "INVALID_ARGUMENT"
	case http.StatusUnauthorized:
		return "UNAUTHENTICATED"
	case http.StatusForbidden:
		return "PERMISSION_DENIED"
	case http.StatusNotFound:
		return "NOT_FOUND"
	case http.StatusTooManyRequests:
		return "RESOURCE_EXHAUSTED"
	case http.StatusInternalServerError:
		return "INTERNAL"
	case http.StatusServiceUnavailable:
		return "UNAVAILABLE"
	case http.StatusGatewayTimeout:
		return "DEADLINE_EXCEEDED"
	default:
		switch {
		case httpStatus >= 400 && httpStatus < 500:
			return "INVALID_ARGUMENT"
		case httpStatus >= 500:
			return "INTERNAL"
		default:
			return "UNKNOWN"
		}
	}
}

// LocalGeminiErrorBody serializes the Gemini-native error envelope used by
// WriteGeminiError. Exported for the same reason LocalClaudeErrorBody is:
// callers building request_log_bodies.response_body outside of a
// WriteGeminiError call need the exact same bytes instead of duplicating the
// envelope shape. requestID is accepted (mirroring LocalClaudeErrorBody's
// signature) but does not appear in the body itself — Gemini's envelope
// carries no request-id field; the id only ever reaches the caller via the
// X-Request-Id header.
func LocalGeminiErrorBody(status int, message, requestID string) []byte {
	b, _ := json.Marshal(geminiErrorBody{
		Error: geminiError{Code: status, Message: message, Status: geminiErrorStatus(status)},
	})
	return b
}

// stashLocalGeminiErrorBody mirrors stashLocalClaudeErrorBody for the Gemini
// envelope, so request_log_bodies.response_body matches what WriteGeminiError
// actually sent when a Exchange is on the context.
func stashLocalGeminiErrorBody(c *gin.Context, status int, message, requestID string) {
	rc := relayContextFrom(c)
	if rc == nil {
		return
	}
	rc.responseBody = LocalGeminiErrorBody(status, message, requestID)
}

// WriteGeminiError writes one Gemini-native error response and aborts the
// chain. Like WriteClaudeError, message reaches the caller unmodified — the
// request id is never appended into it, only set on the X-Request-Id header
// (Gemini's envelope has no field to carry it).
func WriteGeminiError(c *gin.Context, status int, message, requestID string) {
	setRequestIDHeader(c, requestID)
	stashLocalGeminiErrorBody(c, status, message, requestID)
	c.AbortWithStatusJSON(status, geminiErrorBody{
		Error: geminiError{Code: status, Message: message, Status: geminiErrorStatus(status)},
	})
}

// setRequestIDHeader puts requestID on the response under requestIDHeader.
// Idempotent with the RequestID middleware (same header, same value), so
// calling it here does not produce a second/duplicate id header — it only
// guarantees the header is present even when that middleware never ran.
func setRequestIDHeader(c *gin.Context, requestID string) {
	if requestID == "" {
		return
	}
	c.Writer.Header().Set(requestIDHeader, requestID)
}

// WriteClaudeError writes one Anthropic-native /v1/messages error response
// and aborts the chain. Unlike WriteOpenAIErrorWithRequestID, the request id
// is NOT appended into message: Anthropic's envelope carries it as a
// top-level request_id field, so message reaches the caller unmodified.
func WriteClaudeError(c *gin.Context, status int, anthropicType, message, requestID string) {
	setRequestIDHeader(c, requestID)
	stashLocalClaudeErrorBody(c, anthropicType, message, requestID)
	c.AbortWithStatusJSON(status, anthropicErrorBody{
		Type:      "error",
		RequestID: requestID,
		Error:     anthropicError{Type: anthropicType, Message: message},
	})
}

// WriteIngressError dispatches a locally-generated error to the wire
// envelope its ingress protocol expects: the Anthropic envelope (top-level
// request_id, message unchanged) for /v1/messages traffic, the Gemini
// envelope (nested code/message/status, message unchanged, request id
// header-only) for /v1beta native Gemini traffic, and the existing OpenAI
// envelope (request id appended into message) for every other ingress
// (including Responses, which reuses OpenAI's error shape verbatim). Every
// branch leaves requestID on the response's X-Request-Id header. Exported so
// other packages generating a local error ahead of gateway.Handle (e.g.
// middleware.APIKeyAuth's own 401s) can pick the caller's actual wire
// envelope instead of always writing the OpenAI shape.
func WriteIngressError(c *gin.Context, ingress protocols.ProtocolID, status int, errType, message, requestID string) {
	switch ingress {
	case protocols.ProtocolClaude:
		WriteClaudeError(c, status, anthropicErrorType(errType), message, requestID)
	case protocols.ProtocolGemini:
		WriteGeminiError(c, status, message, requestID)
	default:
		setRequestIDHeader(c, requestID)
		WriteOpenAIErrorWithRequestID(c, status, errType, message, requestID)
	}
}
