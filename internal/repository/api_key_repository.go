// Package repository provides APIKey / APIKeyModel data access. Most functions
// are pure storage; UpdateAPIKey additionally enforces a couple of
// resulting-state invariants that the DB can't express and that need the
// transaction's row lock to check atomically (see its doc comment).
package repository

import (
	"errors"
	"strings"
	"time"

	"gorm.io/gorm"

	"github.com/yolorouter/yolorouter/internal/model"
)

// ErrEmptyCustomAllowlist is returned by UpdateAPIKey when an update would
// leave a custom-scope key (allow_all_models=false) with an explicitly-empty
// allowlist — such a key could call nothing. The service translates it to the
// caller-facing errcode; this package stays storage-only.
var ErrEmptyCustomAllowlist = errors.New("custom scope requires at least one model")

func FindAPIKeyByID(db *gorm.DB, id uint) (*model.APIKey, error) {
	var k model.APIKey
	if err := db.Where("id = ?", id).First(&k).Error; err != nil {
		return nil, err
	}
	return &k, nil
}

// APIKeyFilter is the set of list filters applied together (AND). All fields
// are optional — an empty field is a no-op. Now anchors the status filter's
// expiry comparison and must be supplied by the caller (same clock the display
// status is computed against).
type APIKeyFilter struct {
	Query  string
	Owner  string
	Status string
	Now    time.Time
}

// likeContainsPattern wraps q in a LIKE "contains" pattern, escaping the LIKE
// metacharacters so a search for "100%" or "a_b" matches literally rather than
// as wildcards. Backslash is the escape char on both SQLite and Postgres.
func likeContainsPattern(q string) string {
	escaped := strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(q)
	return "%" + escaped + "%"
}

// applyAPIKeyFilters ANDs together the free-text search, the owner filter and
// the display-status filter. LOWER() on both sides keeps SQLite's and
// Postgres's case-sensitive LIKE behaving identically — search must not depend
// on the driver.
func applyAPIKeyFilters(tx *gorm.DB, f APIKeyFilter) *gorm.DB {
	// Free-text search matches the key prefix or remark (owner has its own
	// dedicated filter below).
	if f.Query != "" {
		like := likeContainsPattern(f.Query)
		tx = tx.Where("LOWER(key_prefix) LIKE LOWER(?) ESCAPE '\\' OR LOWER(remark) LIKE LOWER(?) ESCAPE '\\'", like, like)
	}
	if f.Owner != "" {
		tx = tx.Where("LOWER(owner_label) LIKE LOWER(?) ESCAPE '\\'", likeContainsPattern(f.Owner))
	}
	// Status filter mirrors computeAPIKeyDisplayStatus exactly, including its
	// precedence: revoked > expired > budget-exhausted > active.
	switch f.Status {
	case "revoked":
		tx = tx.Where("status = ?", model.APIKeyStatusRevoked)
	case "expired":
		tx = tx.Where("status = ? AND expires_at IS NOT NULL AND expires_at < ?", model.APIKeyStatusActive, f.Now)
	case "budget_exhausted":
		tx = tx.Where(
			"status = ? AND (expires_at IS NULL OR expires_at >= ?) AND budget_limit_micros IS NOT NULL AND budget_spent_micros >= budget_limit_micros",
			model.APIKeyStatusActive, f.Now,
		)
	case "active":
		tx = tx.Where(
			"status = ? AND (expires_at IS NULL OR expires_at >= ?) AND (budget_limit_micros IS NULL OR budget_spent_micros < budget_limit_micros)",
			model.APIKeyStatusActive, f.Now,
		)
	}
	return tx
}

// CountAPIKeys returns the total row count matching the filter.
func CountAPIKeys(db *gorm.DB, f APIKeyFilter) (int64, error) {
	var total int64
	if err := applyAPIKeyFilters(db.Model(&model.APIKey{}), f).Count(&total).Error; err != nil {
		return 0, err
	}
	return total, nil
}

// SearchAPIKeys returns one page (newest first) of API keys matching the filter.
func SearchAPIKeys(db *gorm.DB, f APIKeyFilter, offset, limit int) ([]model.APIKey, error) {
	var keys []model.APIKey
	if err := applyAPIKeyFilters(db.Order("id DESC"), f).Offset(offset).Limit(limit).Find(&keys).Error; err != nil {
		return nil, err
	}
	return keys, nil
}

// CreateAPIKey inserts the key row then its allowlist rows in one transaction,
// so a partial write can never leave a key with fewer whitelisted models than
// requested (at least one is required at create time).
func CreateAPIKey(db *gorm.DB, key *model.APIKey, modelIDs []uint, now time.Time) error {
	return db.Transaction(func(tx *gorm.DB) error {
		key.CreatedAt = now
		key.UpdatedAt = now
		if err := tx.Create(key).Error; err != nil {
			return err
		}
		return insertAPIKeyModels(tx, key.ID, modelIDs, now)
	})
}

// insertAPIKeyModels bulk-inserts the allowlist rows for one key. Empty slice
// is a no-op (UpdateAPIKey uses this when clearing a whitelist).
func insertAPIKeyModels(tx *gorm.DB, apiKeyID uint, modelIDs []uint, now time.Time) error {
	if len(modelIDs) == 0 {
		return nil
	}
	rows := make([]model.APIKeyModel, 0, len(modelIDs))
	for _, mid := range modelIDs {
		rows = append(rows, model.APIKeyModel{APIKeyID: apiKeyID, ModelID: mid, CreatedAt: now})
	}
	return tx.Create(&rows).Error
}

// FindAPIKeyModelIDs returns the model_id allowlist for one key.
func FindAPIKeyModelIDs(db *gorm.DB, apiKeyID uint) ([]uint, error) {
	var ids []uint
	if err := db.Model(&model.APIKeyModel{}).Where("api_key_id = ?", apiKeyID).
		Order("model_id ASC").Pluck("model_id", &ids).Error; err != nil {
		return nil, err
	}
	return ids, nil
}

// FindAPIKeyModelsByAPIKeyIDs batches the N+1 of per-key allowlist lookup when
// listing keys (the same fix shape used elsewhere, e.g. ListModelCandidatesByModelIDs).
func FindAPIKeyModelsByAPIKeyIDs(db *gorm.DB, apiKeyIDs []uint) ([]model.APIKeyModel, error) {
	if len(apiKeyIDs) == 0 {
		return nil, nil
	}
	var rows []model.APIKeyModel
	if err := db.Where("api_key_id IN ?", apiKeyIDs).
		Order("api_key_id ASC, model_id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// UpdateAPIKey applies a sparse column update (only keys present in updates)
// and reconciles the allowlist — all in one transaction. modelIDs == nil
// leaves the allowlist unchanged; modelIDs == [] clears it; a non-nil slice
// replaces it. Two invariants are enforced here rather than in the service,
// because only this layer sees the *resulting* state atomically: the column
// update takes a row lock, the flag is re-read under that lock, and the
// decision is made on what the key will actually end up as, not on the request
// shape. (1) An all-models key owns no allowlist rows. (2) A custom key must
// end up with a non-empty allowlist — otherwise it could call nothing; this
// covers every path that lands there, including a sparse PATCH that flips
// allow_all_models off without supplying ids. Returns ErrEmptyCustomAllowlist
// when the second invariant would be violated (rolls back the whole update).
// scopeChanged tells the second check whether this update touched
// allow_all_models (the caller knows; the repo doesn't probe the updates map
// for it) — a scope flip to custom with ids omitted must re-validate the
// inherited allowlist, an unrelated field edit must not.
func UpdateAPIKey(db *gorm.DB, id uint, updates map[string]interface{}, modelIDs []uint, scopeChanged bool, now time.Time) error {
	return db.Transaction(func(tx *gorm.DB) error {
		// updated_at is always bumped — even a whitelist-only change is a real
		// edit and should refresh the row's last-modified timestamp. This UPDATE
		// also locks the row for the rest of the transaction.
		updates["updated_at"] = now
		if err := tx.Model(&model.APIKey{}).Where("id = ?", id).Updates(updates).Error; err != nil {
			return err
		}
		// Re-read the flag under the lock the UPDATE just took, so the effective
		// value includes any allow_all_models change we just wrote and can't race
		// a concurrent scope change.
		var k model.APIKey
		if err := tx.Select("allow_all_models").Where("id = ?", id).First(&k).Error; err != nil {
			return err
		}
		if k.AllowAllModels {
			// All-models keys own no allowlist rows: clear any that exist and
			// ignore whatever the caller supplied.
			return tx.Where("api_key_id = ?", id).Delete(&model.APIKeyModel{}).Error
		}
		// Custom scope. Enforce a non-empty *resulting* allowlist.
		if modelIDs != nil {
			// Explicit replacement.
			if len(modelIDs) == 0 {
				return ErrEmptyCustomAllowlist
			}
			if err := tx.Where("api_key_id = ?", id).Delete(&model.APIKeyModel{}).Error; err != nil {
				return err
			}
			return insertAPIKeyModels(tx, id, modelIDs, now)
		}
		// Allowlist left unchanged. If this update just switched scope to custom
		// (the flag is in updates), the rows it inherits must be non-empty — a
		// true->false flip supplying no ids would otherwise leave an empty custom
		// key. An update that doesn't touch scope skips this, so a sparse PATCH
		// (e.g. remark-only) never re-validates rows it isn't changing.
		if scopeChanged {
			var cnt int64
			if err := tx.Model(&model.APIKeyModel{}).Where("api_key_id = ?", id).Count(&cnt).Error; err != nil {
				return err
			}
			if cnt == 0 {
				return ErrEmptyCustomAllowlist
			}
		}
		return nil
	})
}

// RevokeAPIKey marks a single active key revoked. The WHERE status = active
// clause makes the UPDATE itself idempotent (0 rows if already revoked) —
// deliberate defense-in-depth alongside service.RevokeAPIKey's pre-check
// short-circuit, not redundant: the pre-check avoids the write on the common
// "revoke an already-revoked key" path, this clause keeps the write correct
// even if that pre-check read was stale.
func RevokeAPIKey(db *gorm.DB, id uint, now time.Time) error {
	return db.Model(&model.APIKey{}).
		Where("id = ? AND status = ?", id, model.APIKeyStatusActive).
		Updates(map[string]interface{}{
			"status":     model.APIKeyStatusRevoked,
			"revoked_at": now,
			"updated_at": now,
		}).Error
}

// FindAPIKeyByHash looks up a key by its SHA-256 hash — the gateway auth path.
// The plaintext is never stored or indexed; the caller
// hashes the bearer token and looks the row up by hash. Returns
// gorm.ErrRecordNotFound for an unknown key (the service layer maps that to
// ErrAPIKeyInvalid — never "not found", to avoid leaking which keys exist).
func FindAPIKeyByHash(db *gorm.DB, hash string) (*model.APIKey, error) {
	var k model.APIKey
	if err := db.Where("key_hash = ?", hash).First(&k).Error; err != nil {
		return nil, err
	}
	return &k, nil
}

// HasAPIKeyModelAccess reports whether modelID is in the key's allowlist.
// Stored by id, so renaming a model does not break allowlists. An empty
// allowlist matches nothing; the gateway only consults this for custom-scope
// keys (all-models keys bypass it upstream), and UpdateAPIKey/CreateAPIKey keep
// a custom key's allowlist non-empty — so a false result here is the
// defense-in-depth floor, not an expected steady state.
func HasAPIKeyModelAccess(db *gorm.DB, apiKeyID, modelID uint) (bool, error) {
	var cnt int64
	if err := db.Model(&model.APIKeyModel{}).
		Where("api_key_id = ? AND model_id = ?", apiKeyID, modelID).Count(&cnt).Error; err != nil {
		return false, err
	}
	return cnt > 0, nil
}
