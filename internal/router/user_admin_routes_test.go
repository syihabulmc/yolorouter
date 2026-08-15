package router

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/yolorouter/yolorouter/internal/model"
	"github.com/yolorouter/yolorouter/internal/repository"
	"github.com/yolorouter/yolorouter/pkg/crypto"
	"github.com/yolorouter/yolorouter/pkg/errcode"
)

// TestDisableCascadesToKeysAndSessions walks the acceptance chain over
// real HTTP: a member's gateway key works, the admin disables the
// account, the key answers 401 on the very next request and the member's
// admin session dies; re-enabling restores the key with no repair step.
func TestDisableCascadesToKeysAndSessions(t *testing.T) {
	f := newMemberScopeFixture(t)

	// The fixture stores hashes; mint a presentable raw credential for
	// alice by inserting one more key whose hash we control.
	const rawKey = "sk-yr-alice-live-credential"
	now := time.Now().UTC()
	k := &model.APIKey{KeyHash: crypto.HashToken(rawKey), KeyPrefix: rawKey[:8], UserID: f.aliceID,
		Status: model.APIKeyStatusActive, AllowAllModels: true, CreatedAt: now, UpdatedAt: now}
	if err := repository.CreateAPIKey(f.db, k, nil, now); err != nil {
		t.Fatalf("seed raw key: %v", err)
	}
	gatewayModels := func() (int, string) {
		req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
		req.Header.Set("Authorization", "Bearer "+rawKey)
		w := httptest.NewRecorder()
		f.r.ServeHTTP(w, req)
		return w.Code, w.Body.String()
	}

	if code, body := gatewayModels(); code != http.StatusOK {
		t.Fatalf("enabled owner: expected 200 from /v1/models, got %d %s", code, body)
	}

	// Admin disables alice.
	w := f.do(t, http.MethodPatch, "/api/admin/users/"+uintToString(f.aliceID)+"/status", `{"status":"disabled"}`, f.adminCk)
	if w.Code != http.StatusOK {
		t.Fatalf("disable alice: %d %s", w.Code, w.Body.String())
	}

	// Her key dies immediately, with the disabled-account message.
	if code, body := gatewayModels(); code != http.StatusUnauthorized || !strings.Contains(body, "account disabled") {
		t.Fatalf("disabled owner: expected 401 account disabled, got %d %s", code, body)
	}
	// The rejection's audit row stays attributed to both the key AND the
	// owning account — account-scoped log views filter on
	// request_logs.user_id, and an unattributed 401 would vanish from them.
	var auditRow model.RequestLog
	if err := f.db.Where("status_code = 401 AND api_key_id = ?", k.ID).
		Order("id DESC").First(&auditRow).Error; err != nil {
		t.Fatalf("load disabled-owner audit row: %v", err)
	}
	if auditRow.UserID == nil || *auditRow.UserID != f.aliceID {
		t.Fatalf("disabled-owner audit row must carry the owner's user_id, got %+v", auditRow.UserID)
	}
	// Her admin session is gone too — not merely rejected, deleted.
	w = f.do(t, http.MethodGet, "/api/admin/auth/me", "", f.aliceCk)
	if w.Code == http.StatusOK {
		t.Fatalf("disabled member session must not answer 200, got %s", w.Body.String())
	}

	// Re-enable: the same key works again without touching key rows.
	w = f.do(t, http.MethodPatch, "/api/admin/users/"+uintToString(f.aliceID)+"/status", `{"status":"enabled"}`, f.adminCk)
	if w.Code != http.StatusOK {
		t.Fatalf("re-enable alice: %d %s", w.Code, w.Body.String())
	}
	if code, body := gatewayModels(); code != http.StatusOK {
		t.Fatalf("re-enabled owner: expected 200, got %d %s", code, body)
	}
}

// TestUserAdminEndpointGuards: the self-operation refusal surfaces as a
// 409 with its own code; member sessions cannot reach the endpoints at
// all; role changes flow through and take effect on the next request
// (RequireSession re-reads the role per request).
func TestUserAdminEndpointGuards(t *testing.T) {
	f := newMemberScopeFixture(t)

	// Admin acting on their own account: refused.
	var adminID uint
	w := f.do(t, http.MethodGet, "/api/admin/users", "", f.adminCk)
	if w.Code != http.StatusOK {
		t.Fatalf("list users: %d", w.Code)
	}
	var env struct {
		Data struct {
			Users []struct {
				ID       uint   `json:"id"`
				Username string `json:"username"`
			} `json:"users"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	for _, u := range env.Data.Users {
		if u.Username == "boss" {
			adminID = u.ID
		}
	}
	if adminID == 0 {
		t.Fatalf("admin missing from directory")
	}
	w = f.do(t, http.MethodPatch, "/api/admin/users/"+uintToString(adminID)+"/status", `{"status":"disabled"}`, f.adminCk)
	if w.Code != http.StatusConflict {
		t.Fatalf("self-disable over http: expected 409, got %d %s", w.Code, w.Body.String())
	}
	var errEnv struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &errEnv); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if errEnv.Code != errcode.AccountSelfOperation {
		t.Fatalf("self-disable code: expected %d, got %d", errcode.AccountSelfOperation, errEnv.Code)
	}

	// Members cannot reach the management endpoints.
	w = f.do(t, http.MethodPatch, "/api/admin/users/"+uintToString(adminID)+"/status", `{"status":"disabled"}`, f.aliceCk)
	if w.Code != http.StatusForbidden {
		t.Fatalf("member reaching user management: expected 403, got %d", w.Code)
	}

	// Promote alice to admin. Her existing session dies with the role
	// change (a live SPA only learns its role at login, so the stale
	// session would keep her locked in the member shell); after a fresh
	// login the admin surface opens up.
	w = f.do(t, http.MethodPatch, "/api/admin/users/"+uintToString(f.aliceID)+"/role", `{"role":"admin"}`, f.adminCk)
	if w.Code != http.StatusOK {
		t.Fatalf("promote alice: %d %s", w.Code, w.Body.String())
	}
	w = f.do(t, http.MethodGet, "/api/admin/users", "", f.aliceCk)
	if w.Code != http.StatusUnauthorized {
		t.Fatalf("promoted alice's old session: expected 401 (forced re-login), got %d", w.Code)
	}
	now := time.Now().UTC()
	if err := repository.CreateSession(f.db, "tok-alice-admin", f.aliceID, now.Add(time.Hour), now); err != nil {
		t.Fatalf("relogin alice: %v", err)
	}
	w = f.do(t, http.MethodGet, "/api/admin/users", "", &http.Cookie{Name: "session_id", Value: "tok-alice-admin"})
	if w.Code != http.StatusOK {
		t.Fatalf("re-logged-in admin alice: expected 200, got %d", w.Code)
	}

	// Invalid payloads are 400s, not silent no-ops.
	w = f.do(t, http.MethodPatch, "/api/admin/users/"+uintToString(f.aliceID)+"/role", `{"role":"superuser"}`, f.adminCk)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("invalid role: expected 400, got %d", w.Code)
	}
}
