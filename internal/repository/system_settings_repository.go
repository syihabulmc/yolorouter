package repository

import (
	"errors"
	"fmt"

	"github.com/yolorouter/yolorouter/internal/settings"
	"github.com/yolorouter/yolorouter/pkg/errcode"

	"gorm.io/gorm"
)

// GetCustomSystemPrompt reads both CSP rows in a single query and validates
// atomicity: exactly 2 rows, equal version, and a strictly-parsed enabled.
// Two separate queries under READ COMMITTED could tear (enabled=N, text=N+1),
// and a wrong version could be accepted by the monotonic cache for a long time.
func GetCustomSystemPrompt(db *gorm.DB) (settings.CustomSystemPromptSetting, int64, error) {
	var rows []struct {
		Key     string
		Value   string
		Version int64
	}
	if err := db.Table("system_settings").
		Select("key, value, version").
		Where("key IN ?", []string{"custom_system_prompt_enabled", "custom_system_prompt"}).
		Find(&rows).Error; err != nil {
		return settings.CustomSystemPromptSetting{}, 0, err
	}
	if len(rows) != 2 {
		return settings.CustomSystemPromptSetting{}, 0, fmt.Errorf("system_settings: expected 2 rows, got %d", len(rows))
	}
	var s settings.CustomSystemPromptSetting
	ver := rows[0].Version
	for _, r := range rows {
		if r.Version != ver {
			return settings.CustomSystemPromptSetting{}, 0, errors.New("system_settings: version mismatch between rows")
		}
		switch r.Key {
		case "custom_system_prompt_enabled":
			switch r.Value {
			case "true":
				s.Enabled = true
			case "false":
				s.Enabled = false
			default:
				return settings.CustomSystemPromptSetting{}, 0, fmt.Errorf("system_settings: corrupt enabled value %q", r.Value)
			}
		case "custom_system_prompt":
			s.Text = r.Value
		}
	}
	return s, ver, nil
}

// UpdateCustomSystemPrompt CAS-upserts both rows in ONE statement:
//
//	UPDATE system_settings
//	SET value = CASE key WHEN 'custom_system_prompt_enabled' THEN ?
//	                     WHEN 'custom_system_prompt'        THEN ? END,
//	    version = version + 1
//	WHERE key IN (?, ?) AND version = ?
//
// Both rows share a version (the read path enforces this), so a single WHERE
// on version is correct and RowsAffected == 2 means the CAS held atomically —
// a concurrent writer that bumped only one row is impossible under the read
// invariant. Anything other than 2 rows affected => another writer committed
// first => conflict. Returns the committed snapshot + new version so the
// handler can hand the fresh version back to the caller; a second save with
// the stale version would otherwise always conflict.
func UpdateCustomSystemPrompt(db *gorm.DB, expectedVersion int64, enabled bool, text string) (settings.CustomSystemPromptSetting, int64, error) {
	enabledVal := "false"
	if enabled {
		enabledVal = "true"
	}
	const (
		keyEnabled = "custom_system_prompt_enabled"
		keyText    = "custom_system_prompt"
	)
	var newVersion int64
	err := db.Transaction(func(tx *gorm.DB) error {
		// CASE-driven SET so a single UPDATE writes the per-key value while
		// still hitting both rows with one WHERE clause. The two keys are the
		// only rows at this version, so RowsAffected == 2 is the CAS witness.
		res := tx.Table("system_settings").
			Where("key IN ? AND version = ?", []string{keyEnabled, keyText}, expectedVersion).
			Updates(map[string]interface{}{
				"value":   gorm.Expr("CASE key WHEN ? THEN ? WHEN ? THEN ? END", keyEnabled, enabledVal, keyText, text),
				"version": gorm.Expr("version + 1"),
			})
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 2 {
			return errcode.ErrCustomSystemPromptConflict
		}
		newVersion = expectedVersion + 1
		return nil
	})
	if err != nil {
		return settings.CustomSystemPromptSetting{}, 0, err
	}
	return settings.CustomSystemPromptSetting{Enabled: enabled, Text: text}, newVersion, nil
}
