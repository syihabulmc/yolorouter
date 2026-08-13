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

// --- Input compression repository -------------------------------------------

// newSettingsTestDBWithIC returns a settings test DB with the
// input_compression_enabled row also seeded at v1 disabled.
func newSettingsTestDBWithIC(t *testing.T) *gorm.DB {
	t.Helper()
	db := newSettingsTestDB(t)
	db.Exec(`INSERT INTO system_settings (key, value) VALUES ('input_compression_enabled','false')`)
	return db
}

func TestGetInputCompressionReadsSeededRow(t *testing.T) {
	db := newSettingsTestDBWithIC(t)
	// Bump to v3 + enabled to confirm version + value are both read.
	db.Exec(`UPDATE system_settings SET value='true', version=3 WHERE key='input_compression_enabled'`)
	enabled, ver, err := GetInputCompression(db)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !enabled || ver != 3 {
		t.Fatalf("want enabled=true/v3, got enabled=%v v%d", enabled, ver)
	}
}

func TestGetInputCompressionMissingRowReturnsDefault(t *testing.T) {
	// newSettingsTestDB seeds only the CSP rows; the IC row is absent.
	db := newSettingsTestDB(t)
	enabled, ver, err := GetInputCompression(db)
	if err != nil {
		t.Fatalf("missing row: want (false,0,nil), got err=%v", err)
	}
	if enabled || ver != 0 {
		t.Fatalf("want disabled/v0, got enabled=%v v%d", enabled, ver)
	}
}

func TestGetInputCompressionRejectsCorruptValue(t *testing.T) {
	db := newSettingsTestDBWithIC(t)
	db.Exec(`UPDATE system_settings SET value='maybe' WHERE key='input_compression_enabled'`)
	if _, _, err := GetInputCompression(db); err == nil {
		t.Fatal("expected error for corrupt input_compression_enabled value, got nil")
	}
}

func TestUpdateInputCompressionSuccess(t *testing.T) {
	db := newSettingsTestDBWithIC(t)
	enabled, ver, err := UpdateInputCompression(db, 1, true)
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if !enabled || ver != 2 {
		t.Fatalf("want enabled=true/v2, got enabled=%v v%d", enabled, ver)
	}
	// Persisted?
	got, gver, err := GetInputCompression(db)
	if err != nil || !got || gver != 2 {
		t.Fatalf("read-back mismatch: enabled=%v v%d err=%v", got, gver, err)
	}
}

func TestUpdateInputCompressionCASConflict(t *testing.T) {
	db := newSettingsTestDBWithIC(t)
	// First successful update bumps version 1 -> 2.
	if _, _, err := UpdateInputCompression(db, 1, true); err != nil {
		t.Fatalf("first update: %v", err)
	}
	// Stale expectedVersion=1 must conflict.
	_, _, err := UpdateInputCompression(db, 1, false)
	if !errors.Is(err, errcode.ErrInputCompressionConflict) {
		t.Fatalf("want ErrInputCompressionConflict, got %v", err)
	}
}

func newVisionFallbackTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db := newSettingsTestDB(t)
	db.Exec(`INSERT INTO system_settings (key, value) VALUES ('vision_fallback_model',''),('vision_fallback_prompt','')`)
	return db
}

func TestGetVisionFallbackReadsBothRows(t *testing.T) {
	db := newVisionFallbackTestDB(t)
	db.Exec(`UPDATE system_settings SET value='glm-4v' WHERE key='vision_fallback_model'`)
	s, ver, err := GetVisionFallback(db)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if s.Model != "glm-4v" || s.Prompt != "" {
		t.Fatalf("snapshot = %+v, want model glm-4v with empty prompt", s)
	}
	if ver != 1 {
		t.Fatalf("version = %d, want 1", ver)
	}
}

// Missing rows (a pre-00022 database) must read as the disabled default, not
// an error — the gateway read path fails open on configuration.
func TestGetVisionFallbackMissingRowsReturnsDefault(t *testing.T) {
	db := newSettingsTestDB(t)
	s, ver, err := GetVisionFallback(db)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if s.Model != "" || s.Prompt != "" || ver != 0 {
		t.Fatalf("want disabled default, got %+v ver %d", s, ver)
	}
}

func TestUpdateVisionFallbackCASConflict(t *testing.T) {
	db := newVisionFallbackTestDB(t)
	if _, _, err := UpdateVisionFallback(db, 1, "glm-4v", "describe it"); err != nil {
		t.Fatalf("first update: %v", err)
	}
	_, _, err := UpdateVisionFallback(db, 1, "", "")
	if !errors.Is(err, errcode.ErrVisionFallbackConflict) {
		t.Fatalf("want ErrVisionFallbackConflict, got %v", err)
	}
}

func TestUpdateVisionFallbackReturnsNewSnapshot(t *testing.T) {
	db := newVisionFallbackTestDB(t)
	s, ver, err := UpdateVisionFallback(db, 1, "glm-4v", "custom prompt")
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if s.Model != "glm-4v" || s.Prompt != "custom prompt" || ver != 2 {
		t.Fatalf("got %+v ver %d, want committed snapshot at version 2", s, ver)
	}
	// The DB agrees with the returned snapshot.
	got, gotVer, err := GetVisionFallback(db)
	if err != nil || got != s || gotVer != ver {
		t.Fatalf("reread = %+v ver %d err %v, want %+v ver %d", got, gotVer, err, s, ver)
	}
}
