package middleware

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/yolorouter/yolorouter/internal/model"
	"github.com/yolorouter/yolorouter/internal/repository"
	"github.com/yolorouter/yolorouter/internal/testutil"
	"github.com/yolorouter/yolorouter/pkg/crypto"
)

func newAuthContext(t *testing.T, authHeader string) *gin.Context {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	if authHeader != "" {
		c.Request.Header.Set("Authorization", authHeader)
	}
	return c
}

// TestExtractBearerKey locks in the RFC 7235 scheme handling: case-
// insensitive scheme, whitespace/tab separator tolerated, and the
// "bearerXYZ" typo rejected as malformed (not parsed as token "XYZ").
func TestExtractBearerKey(t *testing.T) {
	tests := []struct {
		name  string
		input string
		want  string
	}{
		{"standard", "Bearer sk-yr-abc", "sk-yr-abc"},
		{"lowercase scheme", "bearer sk-yr-abc", "sk-yr-abc"},
		{"uppercase scheme", "BEARER sk-yr-abc", "sk-yr-abc"},
		{"mixed case", "BeArEr sk-yr-abc", "sk-yr-abc"},
		{"tab separator", "Bearer\tsk-yr-abc", "sk-yr-abc"},
		{"extra spaces collapsed", "Bearer   sk-yr-abc", "sk-yr-abc"},
		{"missing header", "", ""},
		{"wrong scheme", "Basic sk-yr-abc", ""},
		{"bearerXYZ typo rejected", "bearerXYZ", ""},
		{"bearerX-with-token rejected", "bearerXYZ sk-yr-abc", ""},
		{"just bearer", "Bearer", ""},
		{"bearer trailing space", "Bearer ", ""},
		{"token with internal spaces preserved", "Bearer sk yr abc", "sk yr abc"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := newAuthContext(t, tt.input)
			if got := extractBearerKey(c); got != tt.want {
				t.Errorf("extractBearerKey(%q) = %q, want %q", tt.input, got, tt.want)
			}
		})
	}
}

// TestAuthRejectionWritesBodyRow: a request rejected at
// the auth gate (missing / unknown key) never reaches gateway.Handle, so its
// finalize() never runs — without logAuthRejection also writing the
// request_log_bodies row, both bodies would be permanently unrecorded for
// this whole failure class. Covers both the audit row (request_logs, already
// tested indirectly elsewhere) and the new body row.
func TestAuthRejectionWritesBodyRow(t *testing.T) {
	reqBody := []byte(`{"model":"gpt-4o","messages":[{"role":"user","content":"hi"}]}`)

	cases := []struct {
		name       string
		authHeader string
		wantReason string
	}{
		{"missing key", "", "missing API key"},
		{"unknown key", "Bearer sk-yr-does-not-exist", "invalid API key"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			db := testutil.NewSQLiteDB(t)
			r := gin.New()
			r.Use(RequestID())
			r.POST("/v1/chat/completions", APIKeyAuth(db), func(c *gin.Context) {
				c.Status(http.StatusOK) // never reached — auth always rejects here
			})

			req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", bytes.NewReader(reqBody))
			if tc.authHeader != "" {
				req.Header.Set("Authorization", tc.authHeader)
			}
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401; body=%s", w.Code, w.Body.String())
			}
			requestID := w.Header().Get("X-Request-Id")
			if requestID == "" {
				t.Fatal("X-Request-Id header not set by RequestID middleware")
			}

			var auditRow model.RequestLog
			if err := db.Where("request_id = ?", requestID).First(&auditRow).Error; err != nil {
				t.Fatalf("expected a request_logs audit row: %v", err)
			}
			if auditRow.APIKeyID != nil {
				t.Errorf("audit row api_key_id = %v, want nil for an auth rejection", *auditRow.APIKeyID)
			}
			if auditRow.StatusCode != http.StatusUnauthorized {
				t.Errorf("audit row status_code = %d, want 401", auditRow.StatusCode)
			}

			bodyRow, err := repository.GetRequestLogBodyByRequestID(db, requestID)
			if err != nil {
				t.Fatalf("GetRequestLogBodyByRequestID: %v", err)
			}
			if bodyRow == nil {
				t.Fatal("expected a request_log_bodies row for the auth rejection")
			}
			if !bytes.Contains([]byte(bodyRow.RequestBody), []byte(`"model":"gpt-4o"`)) {
				t.Errorf("body.request_body = %q, want the caller's request body", bodyRow.RequestBody)
			}
			if !bytes.Contains([]byte(bodyRow.ResponseBody), []byte(tc.wantReason)) {
				t.Errorf("body.response_body = %q, want it to contain %q", bodyRow.ResponseBody, tc.wantReason)
			}
			if !bytes.Contains([]byte(bodyRow.ResponseBody), []byte("authentication_error")) {
				t.Errorf("body.response_body = %q, want the authentication_error type", bodyRow.ResponseBody)
			}
		})
	}
}

// seedAPIKey inserts one active APIKey row keyed to rawKey and returns it,
// mirroring the pattern gateway/relay_test.go uses for its own fixtures.
func seedAPIKey(t *testing.T, db *gorm.DB, rawKey string) *model.APIKey {
	t.Helper()
	now := time.Now().UTC()
	prefixLen := len(rawKey)
	if prefixLen > 12 {
		prefixLen = 12
	}
	k := &model.APIKey{
		KeyHash: crypto.HashToken(rawKey), KeyPrefix: rawKey[:prefixLen],
		OwnerLabel: "tester", Status: model.APIKeyStatusActive, CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(k).Error; err != nil {
		t.Fatalf("seed api key: %v", err)
	}
	return k
}

// newAuthRouter builds a gin engine with RequestID + APIKeyAuth mounted on
// path, its next handler responding 200 so a passing request is
// distinguishable from a rejected one.
func newAuthRouter(db *gorm.DB, path string) *gin.Engine {
	gin.SetMode(gin.TestMode)
	r := gin.New()
	r.Use(RequestID())
	r.POST(path, APIKeyAuth(db), func(c *gin.Context) {
		c.Status(http.StatusOK)
	})
	return r
}

// TestAPIKeyAuth_XAPIKeyHeader confirms the Anthropic SDK's X-Api-Key header
// authenticates a caller on /v1/messages, without any Authorization header —
// this is the whole point of the task: Claude Code / the Anthropic SDK sends
// exactly this header and nothing else.
func TestAPIKeyAuth_XAPIKeyHeader(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	seedAPIKey(t, db, "sk-yr-anthropic-sdk")
	r := newAuthRouter(db, "/v1/messages")

	req := httptest.NewRequest(http.MethodPost, "/v1/messages",
		bytes.NewReader([]byte(`{"model":"claude-3-opus","max_tokens":16,"messages":[{"role":"user","content":"hi"}]}`)))
	req.Header.Set("X-Api-Key", "sk-yr-anthropic-sdk")
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (authenticated); body=%s", w.Code, w.Body.String())
	}
}

// TestAPIKeyAuth_BearerStillWorks locks in that adding X-Api-Key support did
// not regress the original Authorization: Bearer path.
func TestAPIKeyAuth_BearerStillWorks(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	seedAPIKey(t, db, "sk-yr-bearer-ok")
	r := newAuthRouter(db, "/v1/messages")

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer sk-yr-bearer-ok")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (authenticated); body=%s", w.Code, w.Body.String())
	}
}

// TestAPIKeyAuth_ConflictingHeaders confirms that presenting both headers
// with different values is rejected outright rather than silently preferring
// one — a mismatch almost certainly means the caller sent a stale credential
// in one of the two headers, and picking a winner would hide that.
func TestAPIKeyAuth_ConflictingHeaders(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	seedAPIKey(t, db, "sk-yr-bearer-value")
	seedAPIKey(t, db, "sk-yr-header-value")
	r := newAuthRouter(db, "/v1/messages")

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer sk-yr-bearer-value")
	req.Header.Set("X-Api-Key", "sk-yr-header-value")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401 for conflicting headers; body=%s", w.Code, w.Body.String())
	}
}

// TestAPIKeyAuth_ConflictingHeaders_SameValue confirms both headers present
// with the SAME value is accepted (not treated as a conflict).
func TestAPIKeyAuth_ConflictingHeaders_SameValue(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	seedAPIKey(t, db, "sk-yr-same-value")
	r := newAuthRouter(db, "/v1/messages")

	req := httptest.NewRequest(http.MethodPost, "/v1/messages", nil)
	req.Header.Set("Authorization", "Bearer sk-yr-same-value")
	req.Header.Set("X-Api-Key", "sk-yr-same-value")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 for identical headers; body=%s", w.Code, w.Body.String())
	}
}

// TestAPIKeyAuth_MissingKey_IngressAwareEnvelope confirms the auth-rejection
// error envelope matches the ingress: /v1/messages gets the Anthropic
// envelope (top-level "type":"error" + "request_id"), /v1/chat/completions
// keeps the existing OpenAI nested-only envelope.
func TestAPIKeyAuth_MissingKey_IngressAwareEnvelope(t *testing.T) {
	cases := []struct {
		name   string
		path   string
		claude bool
	}{
		{"claude ingress", "/v1/messages", true},
		{"openai ingress", "/v1/chat/completions", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := testutil.NewSQLiteDB(t)
			r := newAuthRouter(db, tc.path)

			req := httptest.NewRequest(http.MethodPost, tc.path, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401; body=%s", w.Code, w.Body.String())
			}

			var body map[string]any
			if err := json.Unmarshal(w.Body.Bytes(), &body); err != nil {
				t.Fatalf("body did not unmarshal: %v (body=%s)", err, w.Body.String())
			}
			_, hasTopType := body["type"]
			_, hasTopRequestID := body["request_id"]
			if tc.claude {
				if !hasTopType || !hasTopRequestID {
					t.Errorf("claude ingress: body = %v, want top-level type + request_id", body)
				}
			} else {
				if hasTopType || hasTopRequestID {
					t.Errorf("openai ingress: body = %v, want no top-level type/request_id", body)
				}
				errObj, ok := body["error"].(map[string]any)
				if !ok {
					t.Fatalf("openai ingress: body missing nested error object: %v", body)
				}
				if errObj["type"] != "authentication_error" {
					t.Errorf("openai ingress: error.type = %v, want authentication_error", errObj["type"])
				}
			}
		})
	}
}

// TestAPIKeyAuth_AuditBodyMatchesSentBody is the audit-parity requirement:
// the request_log_bodies.response_body row logAuthRejection writes for a
// rejected request must equal, byte for byte, what the caller actually
// received on the wire — for both ingresses.
func TestAPIKeyAuth_AuditBodyMatchesSentBody(t *testing.T) {
	cases := []struct {
		name string
		path string
	}{
		{"claude ingress", "/v1/messages"},
		{"openai ingress", "/v1/chat/completions"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			db := testutil.NewSQLiteDB(t)
			r := newAuthRouter(db, tc.path)

			req := httptest.NewRequest(http.MethodPost, tc.path, nil)
			w := httptest.NewRecorder()
			r.ServeHTTP(w, req)

			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401; body=%s", w.Code, w.Body.String())
			}
			requestID := w.Header().Get("X-Request-Id")
			if requestID == "" {
				t.Fatal("X-Request-Id header not set")
			}

			bodyRow, err := repository.GetRequestLogBodyByRequestID(db, requestID)
			if err != nil {
				t.Fatalf("GetRequestLogBodyByRequestID: %v", err)
			}
			if bodyRow == nil {
				t.Fatal("expected a request_log_bodies row for the auth rejection")
			}
			if bodyRow.ResponseBody != w.Body.String() {
				t.Errorf("audit response_body = %q, want it to equal the sent body %q", bodyRow.ResponseBody, w.Body.String())
			}
		})
	}
}
