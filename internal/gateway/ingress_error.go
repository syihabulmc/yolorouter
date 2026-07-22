package gateway

import (
	"encoding/json"

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
// existing OpenAI envelope otherwise. message must already reflect any
// ingress-specific transform (WriteIngressError appends the request id into
// message for the OpenAI ingress, not for Claude) so the returned bytes
// match what was actually sent byte-for-byte.
func LocalIngressErrorBody(ingress protocols.ProtocolID, errType, message, requestID string) []byte {
	if ingress == protocols.ProtocolClaude {
		return LocalClaudeErrorBody(anthropicErrorType(errType), message, requestID)
	}
	return LocalErrorBody(errType, message)
}

// stashLocalClaudeErrorBody mirrors stashLocalErrorBody (error.go) for the
// Claude envelope, so request_log_bodies.response_body matches what
// WriteClaudeError actually sent when a RelayContext is on the context.
func stashLocalClaudeErrorBody(c *gin.Context, anthropicType, message, requestID string) {
	rc := relayContextFrom(c)
	if rc == nil {
		return
	}
	rc.ResponseBody = LocalClaudeErrorBody(anthropicType, message, requestID)
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
// request_id, message unchanged) for /v1/messages traffic, and the existing
// OpenAI envelope (request id appended into message) for every other
// ingress. Both branches leave requestID on the response's X-Request-Id
// header. Exported so other packages generating a local error ahead of
// gateway.Handle (e.g. middleware.APIKeyAuth's own 401s) can pick the
// caller's actual wire envelope instead of always writing the OpenAI shape.
func WriteIngressError(c *gin.Context, ingress protocols.ProtocolID, status int, errType, message, requestID string) {
	if ingress == protocols.ProtocolClaude {
		WriteClaudeError(c, status, anthropicErrorType(errType), message, requestID)
		return
	}
	setRequestIDHeader(c, requestID)
	WriteOpenAIErrorWithRequestID(c, status, errType, message, requestID)
}
