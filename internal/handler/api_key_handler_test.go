package handler

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/yolorouter/yolorouter/internal/model"
	"github.com/yolorouter/yolorouter/internal/service"
	"github.com/yolorouter/yolorouter/internal/testutil"
)

func newAPIKeyTestRouter(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	if err := RegisterValidators(); err != nil {
		t.Fatalf("RegisterValidators failed: %v", err)
	}
	db := testutil.NewSQLiteDB(t)
	svc := service.NewAPIKeyService(db)
	r := gin.New()
	r.POST("/api/admin/api-keys", PostAPIKey(svc))
	return r, db
}

func postAPIKey(t *testing.T, r *gin.Engine, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/admin/api-keys", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

// Model-scope create contract with no model seeding required: a custom key
// (allow_all_models=false) must name at least one model, while an all-models
// key legitimately carries no allowlist. gin's required_without only checks the
// slice is non-nil, so an explicit empty [] must be caught by
// validateCustomAllowlist instead.
func TestPostAPIKeyModelScopeContract(t *testing.T) {
	cases := []struct {
		name string
		body map[string]any
		want int
	}{
		{"empty custom allowlist", map[string]any{"allow_all_models": false, "model_ids": []uint{}}, http.StatusBadRequest},
		{"omitted allowlist on custom", map[string]any{"allow_all_models": false}, http.StatusBadRequest},
		{"all-models needs no ids", map[string]any{"allow_all_models": true, "model_ids": []uint{}}, http.StatusOK},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			r, _ := newAPIKeyTestRouter(t)
			w := postAPIKey(t, r, tc.body)
			if w.Code != tc.want {
				t.Fatalf("status = %d, want %d: %s", w.Code, tc.want, w.Body.String())
			}
		})
	}
}

func TestPostAPIKeyAcceptsCustomAllowlist(t *testing.T) {
	r, db := newAPIKeyTestRouter(t)
	now := time.Now().UTC()
	m := &model.Model{Name: "gpt-4o", ManagementStatus: model.ModelStatusEnabled, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(m).Error; err != nil {
		t.Fatalf("seed model: %v", err)
	}
	w := postAPIKey(t, r, map[string]any{"allow_all_models": false, "model_ids": []uint{m.ID}})
	if w.Code != http.StatusOK {
		t.Fatalf("valid custom allowlist must succeed, got %d: %s", w.Code, w.Body.String())
	}
}

// newAPIKeyPatchTestRouter wires POST + PATCH + GET so the CAS 409 contract
// can be exercised end-to-end: create a key, read its authoritative
// updated_at via GET, then PATCH with that token.
func newAPIKeyPatchTestRouter(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	if err := RegisterValidators(); err != nil {
		t.Fatalf("RegisterValidators failed: %v", err)
	}
	db := testutil.NewSQLiteDB(t)
	svc := service.NewAPIKeyService(db)
	r := gin.New()
	r.POST("/api/admin/api-keys", PostAPIKey(svc))
	r.GET("/api/admin/api-keys/:id", GetAPIKey(svc))
	r.PATCH("/api/admin/api-keys/:id", PatchAPIKey(svc))
	return r, db
}

func patchAPIKey(t *testing.T, r *gin.Engine, id uint, body map[string]any) *httptest.ResponseRecorder {
	t.Helper()
	raw, err := json.Marshal(body)
	if err != nil {
		t.Fatalf("marshal body: %v", err)
	}
	req := httptest.NewRequest(http.MethodPatch, "/api/admin/api-keys/"+strconv.Itoa(int(id)), bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	return w
}

func getAPIKeyRaw(t *testing.T, r *gin.Engine, id uint) map[string]any {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/api/admin/api-keys/"+strconv.Itoa(int(id)), nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("GET api-key %d: status %d body %s", id, w.Code, w.Body.String())
	}
	var env struct {
		Data map[string]any `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("decode GET response: %v", err)
	}
	return env.Data
}

// TestPatchAPIKeyReturns409OnCASConflict exercises the full optimistic-lock
// contract: a PATCH carrying the row's authoritative updated_at must succeed
// on the first attempt, and a second PATCH reusing that stale token (after the
// first PATCH bumped updated_at) must return 409 with errcode 11013, instead
// of silently overwriting the committed state.
func TestPatchAPIKeyReturns409OnCASConflict(t *testing.T) {
	r, db := newAPIKeyPatchTestRouter(t)
	now := time.Now().UTC()
	m := &model.Model{Name: "cas-model", ManagementStatus: model.ModelStatusEnabled, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(m).Error; err != nil {
		t.Fatalf("seed model: %v", err)
	}
	w := postAPIKey(t, r, map[string]any{"allow_all_models": false, "model_ids": []uint{m.ID}})
	if w.Code != http.StatusOK {
		t.Fatalf("create key: status %d body %s", w.Code, w.Body.String())
	}
	var createEnv struct {
		Data struct {
			APIKey struct {
				ID uint `json:"id"`
			} `json:"api_key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &createEnv); err != nil {
		t.Fatalf("decode create response: %v", err)
	}
	keyID := createEnv.Data.APIKey.ID

	// Read the authoritative updated_at — this is the snapshot the modal would
	// capture on open and send back as expected_updated_at.
	data := getAPIKeyRaw(t, r, keyID)
	staleUpdatedAt, _ := data["updated_at"].(string)
	if staleUpdatedAt == "" {
		t.Fatalf("GET response missing updated_at: %v", data)
	}

	// First PATCH with the fresh token commits and bumps updated_at.
	w1 := patchAPIKey(t, r, keyID, map[string]any{
		"custom_system_prompt_enabled_override": true,
		"custom_system_prompt_enabled":          true,
		"custom_system_prompt":                  "first writer wins",
		"expected_updated_at":                   staleUpdatedAt,
	})
	if w1.Code != http.StatusOK {
		t.Fatalf("fresh CAS PATCH should succeed, got %d: %s", w1.Code, w1.Body.String())
	}

	// Second PATCH reusing the now-stale token must 409 (errcode 11013).
	w2 := patchAPIKey(t, r, keyID, map[string]any{
		"custom_system_prompt": "second writer stale",
		"expected_updated_at":  staleUpdatedAt,
	})
	if w2.Code != http.StatusConflict {
		t.Fatalf("stale CAS PATCH should return 409, got %d: %s", w2.Code, w2.Body.String())
	}
	var conflictEnv struct {
		Code int `json:"code"`
	}
	if err := json.Unmarshal(w2.Body.Bytes(), &conflictEnv); err != nil {
		t.Fatalf("decode conflict body: %v", err)
	}
	if conflictEnv.Code != 11013 {
		t.Fatalf("conflict body should carry errcode 11013, got %d", conflictEnv.Code)
	}
}

// TestPatchAPIKeyWithoutCASKeepsLegacyBehavior confirms that omitting
// expected_updated_at disables CAS entirely — the legacy path used by
// EditKeyModal and CreateKeyModal must keep working unchanged.
func TestPatchAPIKeyWithoutCASKeepsLegacyBehavior(t *testing.T) {
	r, db := newAPIKeyPatchTestRouter(t)
	now := time.Now().UTC()
	m := &model.Model{Name: "nocas-model", ManagementStatus: model.ModelStatusEnabled, CreatedAt: now, UpdatedAt: now}
	if err := db.Create(m).Error; err != nil {
		t.Fatalf("seed model: %v", err)
	}
	w := postAPIKey(t, r, map[string]any{"allow_all_models": false, "model_ids": []uint{m.ID}})
	if w.Code != http.StatusOK {
		t.Fatalf("create key: status %d body %s", w.Code, w.Body.String())
	}
	var createEnv struct {
		Data struct {
			APIKey struct {
				ID uint `json:"id"`
			} `json:"api_key"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &createEnv); err != nil {
		t.Fatalf("decode create response: %v", err)
	}

	// No expected_updated_at field — unconditional UPDATE, never 409.
	owner := "nocas-owner"
	wp := patchAPIKey(t, r, createEnv.Data.APIKey.ID, map[string]any{"owner_label": owner})
	if wp.Code != http.StatusOK {
		t.Fatalf("non-CAS PATCH should succeed, got %d: %s", wp.Code, wp.Body.String())
	}
}
