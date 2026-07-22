package middleware

import (
	"net/http"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/yolorouter/yolorouter/internal/gateway"
	"github.com/yolorouter/yolorouter/internal/protocols"
	"github.com/yolorouter/yolorouter/pkg/errcode"
	"github.com/yolorouter/yolorouter/pkg/response"
)

const RequestEntityTooLargeCode = errcode.RequestEntityTooLarge

// WriteAdminError writes the /api/* namespace's error response using
// pkg/response's actual envelope (response.Response{Code, Message, Data,
// Timestamp} via ErrorStatus) rather than a separate hand-rolled JSON shape
// — every other admin handler will use pkg/response, so error paths must
// match that same contract instead of diverging from it. The request ID is
// already present on every response via the X-Request-Id header (see
// RequestID middleware), so it isn't duplicated into the JSON body here.
// Gateway (/v1/*) routes must NOT use this — see WriteGatewayError.
func WriteAdminError(c *gin.Context, httpStatus int, code int) {
	c.Abort()
	response.ErrorStatus(c, httpStatus, code, errcode.ErrorMessages[code])
}

// WriteAdminErrorWithData is WriteAdminError plus a Data payload —
// pkg/response has no error-with-data helper of its own (it's kept a
// verbatim copy of the reference project's package, so the fix belongs
// here instead of there). AccountLoginLocked's
// `locked_until` field is the first caller; any future admin error that
// also needs to carry structured data (e.g. a 429's retry_after) should
// go through this one place rather than hand-rolling another
// response.Response{} literal.
func WriteAdminErrorWithData(c *gin.Context, httpStatus int, code int, data any) {
	c.Abort()
	c.JSON(httpStatus, response.Response{
		Code:      code,
		Message:   errcode.ErrorMessages[code],
		Data:      data,
		Timestamp: time.Now().Unix(),
	})
}

// gatewayError is an OpenAI-compatible error body — the shape every
// OpenAI-API-compatible client already knows how to parse. The router's
// 404/405 fallback for unmatched /v1 paths commits to this shape so future
// handlers land on an already-consistent convention instead of two
// incompatible ones existing side by side.
type gatewayError struct {
	Error gatewayErrorBody `json:"error"`
}

type gatewayErrorBody struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    string `json:"code"`
}

// WriteGatewayError writes the /v1/* namespace's error response using the
// OpenAI-compatible {"error": {message, type, code}} shape instead of
// pkg/response's admin envelope — see WriteAdminError's doc comment.
func WriteGatewayError(c *gin.Context, httpStatus int, errType, code, message string) {
	c.Abort()
	c.JSON(httpStatus, gatewayError{Error: gatewayErrorBody{Message: message, Type: errType, Code: code}})
}

// IsAdminNamespace reports whether path falls under the /api/* admin
// namespace.
func IsAdminNamespace(path string) bool {
	return path == "/api" || strings.HasPrefix(path, "/api/")
}

// IsGatewayNamespace reports whether path falls under the /v1/* gateway
// namespace (OpenAI-compatible surface).
func IsGatewayNamespace(path string) bool {
	return path == "/v1" || strings.HasPrefix(path, "/v1/")
}

// WriteNamespacedError dispatches a route-level error (404/405/500) to the
// error shape its namespace owns: /api* gets pkg/response's admin envelope,
// /v1* gets the wire envelope its ingress protocol expects — the
// OpenAI-compatible shape for every /v1/* path except /v1/messages, which
// gets the Anthropic-native envelope instead (gateway.WriteIngressError). The
// OpenAI branch is untouched by that split — same shape, same content, byte
// for byte — since every existing /v1/* caller besides /v1/messages still
// expects it. This is the single place NoRoute, NoMethod, and Recovery all
// dispatch through, so a future panic under /v1/* doesn't leak an envelope
// the caller's SDK can't parse.
func WriteNamespacedError(c *gin.Context, path string, httpStatus int, adminCode int) {
	if !IsGatewayNamespace(path) {
		WriteAdminError(c, httpStatus, adminCode)
		return
	}
	status, errType, message, openAICode := gatewayErrorFor(adminCode, httpStatus)
	if gateway.IngressProtocol(path) == protocols.ProtocolClaude {
		requestID := c.GetString(RequestIDKey)
		gateway.WriteIngressError(c, protocols.ProtocolClaude, status, errType, message, requestID)
		return
	}
	WriteGatewayError(c, status, errType, openAICode, message)
}

// gatewayErrorFor maps a route-level admin error code (404/405/500) to the
// (status, error type, message, OpenAI-style code) tuple both /v1/* ingress
// branches of WriteNamespacedError need — computed once so the OpenAI and
// Claude wire shapes can't drift apart on which case maps to which text.
func gatewayErrorFor(adminCode int, httpStatus int) (status int, errType, message, openAICode string) {
	switch adminCode {
	case errcode.MethodNotAllowed:
		return httpStatus, "invalid_request_error", "method not allowed", "method_not_allowed"
	case errcode.RouteNotFound:
		return httpStatus, "invalid_request_error", "route not found", "route_not_found"
	default:
		return http.StatusInternalServerError, "server_error", "internal server error", "internal_error"
	}
}
