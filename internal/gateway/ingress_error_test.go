package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/yolorouter/yolorouter/internal/protocols"
)

// newIngressTestContext builds a gin test context wired to a real
// httptest.ResponseRecorder, mirroring the pattern used by
// TestWriteOpenAIErrorStashesResponseBody in error_test.go.
func newIngressTestContext(t *testing.T, path string) (*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, path, nil)
	return c, w
}

// TestWriteClaudeError_Envelope confirms WriteClaudeError writes the
// Anthropic-native shape: top-level type + request_id, and a nested
// error.type/error.message — not the OpenAI-shaped nested-only envelope.
func TestWriteClaudeError_Envelope(t *testing.T) {
	c, w := newIngressTestContext(t, "/v1/messages")

	WriteClaudeError(c, http.StatusTooManyRequests, "rate_limit_error", "too many requests", "req_abc")

	if w.Code != http.StatusTooManyRequests {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusTooManyRequests)
	}

	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body did not unmarshal: %v (body=%s)", err, w.Body.String())
	}
	if body["type"] != "error" {
		t.Errorf(`top-level "type" = %v, want "error"`, body["type"])
	}
	if body["request_id"] != "req_abc" {
		t.Errorf(`top-level "request_id" = %v, want "req_abc"`, body["request_id"])
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf(`"error" field is not an object: %v`, body["error"])
	}
	if errObj["type"] != "rate_limit_error" {
		t.Errorf(`error.type = %v, want "rate_limit_error"`, errObj["type"])
	}
	if errObj["message"] != "too many requests" {
		t.Errorf(`error.message = %v, want "too many requests"`, errObj["message"])
	}
	// The message must NOT have the request id appended (unlike the OpenAI
	// path) — the id travels only in the top-level request_id field.
	if strings.Contains(errObj["message"].(string), "req_abc") {
		t.Errorf("error.message = %v, must not contain the request id", errObj["message"])
	}
}

// TestWriteClaudeError_StashesResponseBody mirrors
// TestWriteOpenAIErrorStashesResponseBody: when a RelayContext is on the gin
// context, the Claude error JSON actually sent to the caller must also be
// stashed into rc.ResponseBody so the audit trail matches.
func TestWriteClaudeError_StashesResponseBody(t *testing.T) {
	c, w := newIngressTestContext(t, "/v1/messages")
	rc := &RelayContext{RequestID: "req_x"}
	c.Set(relayContextKey, rc)

	WriteClaudeError(c, http.StatusNotFound, "not_found_error", "model does not exist", "req_x")

	if rc.ResponseBody == nil {
		t.Fatal("rc.ResponseBody was not stashed")
	}
	if string(rc.ResponseBody) != w.Body.String() {
		t.Errorf("rc.ResponseBody = %s, want it to equal the sent body %s", rc.ResponseBody, w.Body.String())
	}
}

// TestWriteClaudeError_NoRelayContextIsNoop confirms the stash is a true
// no-op (no panic) when no RelayContext is on the context.
func TestWriteClaudeError_NoRelayContextIsNoop(t *testing.T) {
	c, w := newIngressTestContext(t, "/v1/messages")

	WriteClaudeError(c, http.StatusUnauthorized, "authentication_error", "missing API key", "req_y")

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", w.Code)
	}
}

// TestAnthropicErrorType covers every errType* constant's mapping to a valid
// Anthropic error type, per the internal->Anthropic table.
func TestAnthropicErrorType(t *testing.T) {
	tests := []struct {
		name    string
		errType string
		want    string
	}{
		{"authentication", errTypeAuthentication, "authentication_error"},
		{"permission", errTypePermission, "permission_error"},
		{"invalid_request", errTypeInvalidRequest, "invalid_request_error"},
		{"not_found", errTypeNotFound, "not_found_error"},
		{"rate_limit", errTypeRateLimit, "rate_limit_error"},
		{"insufficient_quota", errTypeInsufficientQuota, "invalid_request_error"},
		{"unavailable", errTypeUnavailable, "overloaded_error"},
		{"upstream", errTypeUpstream, "api_error"},
		{"server", errTypeServer, "api_error"},
		{"unknown_default", "some_unmapped_type", "api_error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := anthropicErrorType(tt.errType); got != tt.want {
				t.Errorf("anthropicErrorType(%q) = %q, want %q", tt.errType, got, tt.want)
			}
		})
	}
}

// TestWriteIngressError_Claude confirms WriteIngressError routes
// /v1/messages traffic to the Anthropic envelope, with errType mapped
// through anthropicErrorType and the message left untouched (id not
// appended).
func TestWriteIngressError_Claude(t *testing.T) {
	c, w := newIngressTestContext(t, "/v1/messages")

	WriteIngressError(c, protocols.ProtocolClaude, http.StatusForbidden, errTypePermission, "not allowed", "req_claude")

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusForbidden)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body did not unmarshal: %v", err)
	}
	if body["type"] != "error" || body["request_id"] != "req_claude" {
		t.Errorf("body = %v, want top-level type=error, request_id=req_claude", body)
	}
	errObj := body["error"].(map[string]any)
	if errObj["type"] != "permission_error" {
		t.Errorf("error.type = %v, want permission_error", errObj["type"])
	}
	if errObj["message"] != "not allowed" {
		t.Errorf("error.message = %v, want unchanged message %q", errObj["message"], "not allowed")
	}
}

// TestWriteIngressError_OpenAI confirms WriteIngressError keeps the existing
// OpenAI-compatible behavior for any non-Claude ingress: nested-only
// envelope, id appended into message.
func TestWriteIngressError_OpenAI(t *testing.T) {
	c, w := newIngressTestContext(t, "/v1/chat/completions")

	WriteIngressError(c, protocols.ProtocolOpenAI, http.StatusNotFound, errTypeNotFound, "model does not exist", "req_openai")

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want %d", w.Code, http.StatusNotFound)
	}
	var body map[string]any
	if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
		t.Fatalf("body did not unmarshal: %v", err)
	}
	if _, hasTopLevelType := body["type"]; hasTopLevelType {
		t.Errorf("body has an OpenAI-illegal top-level %q field: %v", "type", body)
	}
	if _, hasTopLevelRequestID := body["request_id"]; hasTopLevelRequestID {
		t.Errorf("body has an OpenAI-illegal top-level %q field: %v", "request_id", body)
	}
	errObj, ok := body["error"].(map[string]any)
	if !ok {
		t.Fatalf(`"error" field is not an object: %v`, body["error"])
	}
	if errObj["type"] != errTypeNotFound {
		t.Errorf("error.type = %v, want %q", errObj["type"], errTypeNotFound)
	}
	msg, _ := errObj["message"].(string)
	if !strings.Contains(msg, "model does not exist") || !strings.Contains(msg, "req_openai") {
		t.Errorf("error.message = %q, want it to contain both the message and the request id", msg)
	}
}

// TestWriteIngressError_SetsRequestIDHeader confirms both ingresses leave
// the caller-visible request id on the response header the RequestID
// middleware already establishes (X-Request-Id), so SDKs can surface it
// even from a locally-generated error.
func TestWriteIngressError_SetsRequestIDHeader(t *testing.T) {
	cases := []struct {
		name    string
		ingress protocols.ProtocolID
		path    string
	}{
		{"claude", protocols.ProtocolClaude, "/v1/messages"},
		{"openai", protocols.ProtocolOpenAI, "/v1/chat/completions"},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			c, w := newIngressTestContext(t, tt.path)
			WriteIngressError(c, tt.ingress, http.StatusInternalServerError, errTypeServer, "boom", "req_hdr_"+tt.name)

			got := w.Header().Get("X-Request-Id")
			if got != "req_hdr_"+tt.name {
				t.Errorf("X-Request-Id header = %q, want %q", got, "req_hdr_"+tt.name)
			}
		})
	}
}

// TestLocalIngressErrorBody_MatchesSentBody is the audit-parity
// requirement: LocalIngressErrorBody must byte-for-byte equal what
// WriteIngressError actually put on the wire, for both ingresses.
func TestLocalIngressErrorBody_MatchesSentBody(t *testing.T) {
	t.Run("claude", func(t *testing.T) {
		c, w := newIngressTestContext(t, "/v1/messages")
		WriteIngressError(c, protocols.ProtocolClaude, http.StatusBadRequest, errTypeInvalidRequest, "bad body", "req_1")

		got := LocalIngressErrorBody(protocols.ProtocolClaude, errTypeInvalidRequest, "bad body", "req_1")
		if string(got) != w.Body.String() {
			t.Errorf("LocalIngressErrorBody = %s, want it to equal sent body %s", got, w.Body.String())
		}
	})

	t.Run("openai", func(t *testing.T) {
		c, w := newIngressTestContext(t, "/v1/chat/completions")
		WriteIngressError(c, protocols.ProtocolOpenAI, http.StatusBadRequest, errTypeInvalidRequest, "bad body", "req_2")

		// WriteIngressError appends the request id into the message for the
		// OpenAI ingress, so the audit body must be built with that same
		// message to match — LocalErrorBody does not append it itself.
		got := LocalIngressErrorBody(protocols.ProtocolOpenAI, errTypeInvalidRequest, "bad body (request: req_2)", "req_2")
		if string(got) != w.Body.String() {
			t.Errorf("LocalIngressErrorBody = %s, want it to equal sent body %s", got, w.Body.String())
		}
	})
}

// TestLocalClaudeErrorBody_Shape confirms LocalClaudeErrorBody produces the
// exact same shape WriteClaudeError sends, independent of WriteIngressError.
func TestLocalClaudeErrorBody_Shape(t *testing.T) {
	c, w := newIngressTestContext(t, "/v1/messages")
	WriteClaudeError(c, http.StatusServiceUnavailable, "overloaded_error", "overloaded", "req_3")

	got := LocalClaudeErrorBody("overloaded_error", "overloaded", "req_3")
	if string(got) != w.Body.String() {
		t.Errorf("LocalClaudeErrorBody = %s, want it to equal sent body %s", got, w.Body.String())
	}
}
