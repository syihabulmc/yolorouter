package repository

import (
	"errors"
	"testing"

	"github.com/yolorouter/yolorouter/internal/settings"
	"github.com/yolorouter/yolorouter/pkg/errcode"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newSettingsTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	db.Exec(`CREATE TABLE system_settings (key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '', version INTEGER NOT NULL DEFAULT 1, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`)
	db.Exec(`INSERT INTO system_settings (key, value) VALUES ('custom_system_prompt_enabled','false'),('custom_system_prompt','')`)
	return db
}

func TestGetCustomSystemPromptReadsBothRows(t *testing.T) {
	db := newSettingsTestDB(t)
	s, ver, err := GetCustomSystemPrompt(db)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if s.Enabled || s.Text != "" {
		t.Fatalf("want disabled/empty, got %+v", s)
	}
	if ver != 1 {
		t.Fatalf("version = %d, want 1", ver)
	}
}

func TestGetCustomSystemPromptRejectsCorruptEnabled(t *testing.T) {
	db := newSettingsTestDB(t)
	db.Exec(`UPDATE system_settings SET value='maybe' WHERE key='custom_system_prompt_enabled'`)
	if _, _, err := GetCustomSystemPrompt(db); err == nil {
		t.Fatal("expected error for corrupt enabled value, got nil")
	}
}

func TestUpdateCustomSystemPromptCASConflict(t *testing.T) {
	db := newSettingsTestDB(t)
	// first successful update bumps version 1 -> 2
	if _, _, err := UpdateCustomSystemPrompt(db, 1, true, "hello"); err != nil {
		t.Fatalf("first update: %v", err)
	}
	// stale expectedVersion=1 must conflict
	_, _, err := UpdateCustomSystemPrompt(db, 1, false, "")
	if !errors.Is(err, errcode.ErrCustomSystemPromptConflict) {
		t.Fatalf("want ErrCustomSystemPromptConflict, got %v", err)
	}
}

func TestUpdateCustomSystemPromptReturnsNewSnapshot(t *testing.T) {
	db := newSettingsTestDB(t)
	s, ver, err := UpdateCustomSystemPrompt(db, 1, true, "hi")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !s.Enabled || s.Text != "hi" || ver != 2 {
		t.Fatalf("want enabled/hi/v2, got %+v v%d", s, ver)
	}
	// persisted?
	got, gver, err := GetCustomSystemPrompt(db)
	if err != nil || !got.Enabled || got.Text != "hi" || gver != 2 {
		t.Fatalf("read-back mismatch: %+v v%d err=%v", got, gver, err)
	}
	_ = settings.CustomSystemPromptSetting{} // keep import if assertions above evolve
}
