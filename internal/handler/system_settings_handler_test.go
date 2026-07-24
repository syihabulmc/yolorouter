package handler

import (
	"bytes"
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/yolorouter/yolorouter/internal/service"
)

func setupSettingsRouter(t *testing.T) (*gin.Engine, *service.SystemSettingsService) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.Exec(`CREATE TABLE system_settings (key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '', version INTEGER NOT NULL DEFAULT 1, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`)
	db.Exec(`INSERT INTO system_settings (key, value) VALUES ('custom_system_prompt_enabled','false'),('custom_system_prompt','')`)
	svc := service.NewSystemSettingsService(db)
	r := gin.New()
	r.GET("/api/admin/system-settings/custom-system-prompt", GetCustomSystemPrompt(svc))
	r.PUT("/api/admin/system-settings/custom-system-prompt", PutCustomSystemPrompt(svc))
	return r, svc
}

func TestGetCustomSystemPromptReturnsSeeded(t *testing.T) {
	r, _ := setupSettingsRouter(t)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("GET", "/api/admin/system-settings/custom-system-prompt", nil))
	if w.Code != 200 {
		t.Fatalf("status = %d", w.Code)
	}
	var resp struct {
		Code int `json:"code"`
		Data struct {
			Enabled bool   `json:"enabled"`
			Text    string `json:"text"`
			Version int64  `json:"version"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Data.Enabled || resp.Data.Text != "" || resp.Data.Version != 1 {
		t.Fatalf("unexpected payload: %+v", resp.Data)
	}
}

func TestPutCustomSystemPromptMissingFields400(t *testing.T) {
	r, _ := setupSettingsRouter(t)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, httptest.NewRequest("PUT", "/api/admin/system-settings/custom-system-prompt", bytes.NewBufferString(`{}`)))
	if w.Code != 400 {
		t.Fatalf("status = %d, want 400", w.Code)
	}
}

func TestPutCustomSystemPromptSuccessReturnsNewVersion(t *testing.T) {
	r, _ := setupSettingsRouter(t)
	body, _ := json.Marshal(map[string]interface{}{"enabled": true, "text": "hi", "version": int64(1)})
	req := httptest.NewRequest("PUT", "/api/admin/system-settings/custom-system-prompt", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d body=%s", w.Code, w.Body.String())
	}
	var resp struct {
		Data struct {
			Version int64 `json:"version"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("unmarshal response: %v", err)
	}
	if resp.Data.Version != 2 {
		t.Fatalf("new version = %d, want 2", resp.Data.Version)
	}
}

func TestPutCustomSystemPromptStaleVersion409(t *testing.T) {
	r, _ := setupSettingsRouter(t)
	body, _ := json.Marshal(map[string]interface{}{"enabled": false, "text": "", "version": int64(99)})
	req := httptest.NewRequest("PUT", "/api/admin/system-settings/custom-system-prompt", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != 409 {
		t.Fatalf("status = %d, want 409", w.Code)
	}
}
