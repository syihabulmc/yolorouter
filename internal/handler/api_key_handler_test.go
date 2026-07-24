package handler

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
