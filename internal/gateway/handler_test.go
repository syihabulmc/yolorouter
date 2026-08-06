package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
)

// TestPostChatCompletionsMissingAuthContext_IngressAware guards the
// otherwise-unreachable auth-context guard in PostChatCompletions: if this
// handler somehow runs without APIKeyAuth having set gatewayAPIKeyKey first
// (a future route wired without that middleware, a context key typo), the
// response must still match the caller's actual wire protocol — a
// /v1/messages caller gets the Anthropic envelope, a /v1/chat/completions
// caller keeps the existing OpenAI shape.
func TestPostChatCompletionsMissingAuthContext_IngressAware(t *testing.T) {
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
			gin.SetMode(gin.TestMode)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)
			c.Request = httptest.NewRequest(http.MethodPost, tc.path, nil)
			c.Set("request_id", "req_missing_auth")

			// Deliberately do NOT set gatewayAPIKeyKey — the guard returns
			// before svc.Handle is ever called, so a nil *Service is
			// safe here.
			PostChatCompletions(nil)(c)

			if w.Code != http.StatusInternalServerError {
				t.Fatalf("status = %d, want 500, body: %s", w.Code, w.Body.String())
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
				if errObj["type"] != "server_error" {
					t.Errorf("openai ingress: error.type = %v, want server_error", errObj["type"])
				}
			}
		})
	}
}
