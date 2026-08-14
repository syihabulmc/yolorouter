package repository

import (
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/yolorouter/yolorouter/internal/model"
	"github.com/yolorouter/yolorouter/internal/testutil"
	"github.com/yolorouter/yolorouter/pkg/errcode"
)

// seedCustomScopeKeyForCSPTest inserts a custom-scope key (one model in its
// allowlist) and returns the DB + key so CSP PATCH tests have a row that
// reaches the final-state CSP check in UpdateAPIKey.
func seedCustomScopeKeyForCSPTest(t *testing.T) (*gorm.DB, *model.APIKey) {
	t.Helper()
	now := time.Now().UTC().Truncate(time.Second)
	db := testutil.NewSQLiteDB(t)
	m := &model.Model{
		Name: "csp-test-model", ManagementStatus: model.ModelStatusEnabled,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(m).Error; err != nil {
		t.Fatalf("seed model: %v", err)
	}
	owner := seedUser(t, db, "csp-key-owner")
	key := &model.APIKey{
		KeyHash:   "test-hash-csp",
		KeyPrefix: "sk-yr-csptest000",
		UserID:    owner.ID,
		Status:    model.APIKeyStatusActive,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := CreateAPIKey(db, key, []uint{m.ID}, now); err != nil {
		t.Fatalf("CreateAPIKey: %v", err)
	}
	return db, key
}

// TestUpdateAPIKeyRejectsOverrideEnabledEmptyText checks the core CSP guard:
// override=true && enabled=true && text="" must be rejected under the tx row
// lock, so a sparse PATCH that flips enabled on (without supplying text) can't
// leave the key in an "enabled but empty" state.
func TestUpdateAPIKeyRejectsOverrideEnabledEmptyText(t *testing.T) {
	db, key := seedCustomScopeKeyForCSPTest(t)
	now := time.Now().UTC().Truncate(time.Second)
	updates := map[string]interface{}{
		"custom_system_prompt_enabled_override": true,
		"custom_system_prompt_enabled":          true,
		"custom_system_prompt":                  "",
	}
	err := UpdateAPIKey(db, key.ID, updates, nil, false, now, nil)
	if !errors.Is(err, errcode.ErrCustomSystemPromptEmpty) {
		t.Fatalf("want ErrCustomSystemPromptEmpty, got %v", err)
	}
}

// TestUpdateAPIKeyOverrideDisabledAllowsEmptyText checks that empty text is
// fine when CSP is not enabled — override=true but enabled=false doesn't use
// the key's own text, so the empty-text rule must not fire.
func TestUpdateAPIKeyOverrideDisabledAllowsEmptyText(t *testing.T) {
	db, key := seedCustomScopeKeyForCSPTest(t)
	now := time.Now().UTC().Truncate(time.Second)
	updates := map[string]interface{}{
		"custom_system_prompt_enabled_override": true,
		"custom_system_prompt_enabled":          false,
		"custom_system_prompt":                  "",
	}
	if err := UpdateAPIKey(db, key.ID, updates, nil, false, now, nil); err != nil {
		t.Fatalf("override+disabled+empty should be allowed: %v", err)
	}
}

// TestUpdateAPIKeyAllowsOverrideEnabledWithText checks the happy path: all
// three CSP fields set together with non-empty text must succeed.
func TestUpdateAPIKeyAllowsOverrideEnabledWithText(t *testing.T) {
	db, key := seedCustomScopeKeyForCSPTest(t)
	now := time.Now().UTC().Truncate(time.Second)
	updates := map[string]interface{}{
		"custom_system_prompt_enabled_override": true,
		"custom_system_prompt_enabled":          true,
		"custom_system_prompt":                  "you are a helpful router",
	}
	if err := UpdateAPIKey(db, key.ID, updates, nil, false, now, nil); err != nil {
		t.Fatalf("override+enabled+text should be allowed: %v", err)
	}
}

// TestUpdateAPIKeyRejectsCSPViolationOnAllModelsPath checks the CSP guard
// runs on the all-models exit path of UpdateAPIKey: a PATCH that flips
// allow_all_models on in the same call as override+enabled with empty text
// must still be rejected, rather than early-returning after the allowlist
// clear and skipping the CSP re-read.
func TestUpdateAPIKeyRejectsCSPViolationOnAllModelsPath(t *testing.T) {
	db, key := seedCustomScopeKeyForCSPTest(t)
	now := time.Now().UTC().Truncate(time.Second)
	updates := map[string]interface{}{
		"allow_all_models":                      true,
		"custom_system_prompt_enabled_override": true,
		"custom_system_prompt_enabled":          true,
		"custom_system_prompt":                  "",
	}
	err := UpdateAPIKey(db, key.ID, updates, nil, true, now, nil)
	if !errors.Is(err, errcode.ErrCustomSystemPromptEmpty) {
		t.Fatalf("want ErrCustomSystemPromptEmpty, got %v", err)
	}
}

// TestUpdateAPIKeyRejectsCSPViolationOnReplaceAllowlistPath checks the CSP
// guard runs on the explicit-allowlist-replacement exit path: a PATCH that
// supplies model_ids together with override+enabled and empty text must be
// rejected, rather than early-returning after the allowlist rewrite.
func TestUpdateAPIKeyRejectsCSPViolationOnReplaceAllowlistPath(t *testing.T) {
	db, key := seedCustomScopeKeyForCSPTest(t)
	now := time.Now().UTC().Truncate(time.Second)
	// Seed a second model so the replacement slice is a valid non-empty one.
	m2 := &model.Model{
		Name: "csp-replace-path-model", ManagementStatus: model.ModelStatusEnabled,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(m2).Error; err != nil {
		t.Fatalf("seed model: %v", err)
	}
	updates := map[string]interface{}{
		"custom_system_prompt_enabled_override": true,
		"custom_system_prompt_enabled":          true,
		"custom_system_prompt":                  "",
	}
	err := UpdateAPIKey(db, key.ID, updates, []uint{m2.ID}, false, now, nil)
	if !errors.Is(err, errcode.ErrCustomSystemPromptEmpty) {
		t.Fatalf("want ErrCustomSystemPromptEmpty, got %v", err)
	}
}

// TestCreateAPIKeyRejectsOverrideEnabledEmptyText checks the same guard at
// create time — a brand-new key with override && enabled but no text is
// rejected before the row is inserted.
func TestCreateAPIKeyRejectsOverrideEnabledEmptyText(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	m := &model.Model{
		Name: "csp-create-model", ManagementStatus: model.ModelStatusEnabled,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := db.Create(m).Error; err != nil {
		t.Fatalf("seed model: %v", err)
	}
	owner := seedUser(t, db, "csp-create-owner")
	key := &model.APIKey{
		KeyHash:                           "test-hash-csp-create",
		KeyPrefix:                         "sk-yr-cspcreate0",
		UserID:                            owner.ID,
		Status:                            model.APIKeyStatusActive,
		CustomSystemPromptEnabledOverride: true,
		CustomSystemPromptEnabled:         true,
		CustomSystemPrompt:                "",
		CreatedAt:                         now,
		UpdatedAt:                         now,
	}
	err := CreateAPIKey(db, key, []uint{m.ID}, now)
	if !errors.Is(err, errcode.ErrCustomSystemPromptEmpty) {
		t.Fatalf("want ErrCustomSystemPromptEmpty, got %v", err)
	}
}

// TestCreateAPIKeyRejectsMissingOwner pins the ownerless-key guard: GORM
// writes every mapped column, so a caller that forgot to set UserID would
// otherwise insert 0 silently and the key would belong to no one's view.
func TestCreateAPIKeyRejectsMissingOwner(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	now := time.Now().UTC().Truncate(time.Second)
	key := &model.APIKey{
		KeyHash:   "test-hash-no-owner",
		KeyPrefix: "sk-yr-noowner000",
		Status:    model.APIKeyStatusActive,
		CreatedAt: now,
		UpdatedAt: now,
	}
	err := CreateAPIKey(db, key, nil, now)
	if !errors.Is(err, ErrAPIKeyOwnerMissing) {
		t.Fatalf("want ErrAPIKeyOwnerMissing, got %v", err)
	}
}

// TestUpdateAPIKeyCASConflictsOnStaleUpdatedAt exercises the optimistic-lock
// CAS path: with expectedUpdatedAt set to the row's current updated_at, a
// concurrent writer that bumps updated_at in between must cause the second
// UpdateAPIKey to return ErrAPIKeyConflict (0 rows matched the predicate)
// rather than silently overwriting the newer state.
func TestUpdateAPIKeyCASConflictsOnStaleUpdatedAt(t *testing.T) {
	db, key := seedCustomScopeKeyForCSPTest(t)
	t0 := key.UpdatedAt // snapshot before any concurrent write

	// First writer commits — bumps updated_at to t1.
	t1 := t0.Add(time.Second)
	updates := map[string]interface{}{"remark": "writer-1"}
	if err := UpdateAPIKey(db, key.ID, updates, nil, false, t1, &t0); err != nil {
		t.Fatalf("CAS update against fresh snapshot should succeed: %v", err)
	}

	// Second writer reuses the stale t0 snapshot — should conflict because the
	// row's updated_at is now t1, not t0.
	updates2 := map[string]interface{}{"remark": "writer-2-stale"}
	err := UpdateAPIKey(db, key.ID, updates2, nil, false, t1.Add(time.Second), &t0)
	if !errors.Is(err, errcode.ErrAPIKeyConflict) {
		t.Fatalf("want ErrAPIKeyConflict on stale CAS token, got %v", err)
	}

	// The conflicting update must roll back — the row still carries writer-1's
	// remark and t1's updated_at, not writer-2's.
	var stored model.APIKey
	if err := db.First(&stored, key.ID).Error; err != nil {
		t.Fatalf("load stored: %v", err)
	}
	if stored.Remark != "writer-1" {
		t.Fatalf("stale CAS must not overwrite; remark = %q", stored.Remark)
	}
	if !stored.UpdatedAt.Equal(t1) {
		t.Fatalf("updated_at should still be t1 after rejected CAS, got %v want %v", stored.UpdatedAt, t1)
	}
}

// TestUpdateAPIKeyCASSucceedsWithFreshSnapshot confirms the happy path: when
// expectedUpdatedAt equals the row's current updated_at, the UPDATE commits
// and bumps updated_at to the new value. Paired with the conflict case so the
// predicate isn't so strict it rejects legitimate saves.
func TestUpdateAPIKeyCASSucceedsWithFreshSnapshot(t *testing.T) {
	db, key := seedCustomScopeKeyForCSPTest(t)
	fresh := key.UpdatedAt
	newNow := fresh.Add(time.Second)
	updates := map[string]interface{}{"remark": "cas-ok"}
	if err := UpdateAPIKey(db, key.ID, updates, nil, false, newNow, &fresh); err != nil {
		t.Fatalf("CAS with fresh snapshot should succeed: %v", err)
	}

	var stored model.APIKey
	if err := db.First(&stored, key.ID).Error; err != nil {
		t.Fatalf("load stored: %v", err)
	}
	if stored.Remark != "cas-ok" {
		t.Fatalf("CAS update should persist remark, got %q", stored.Remark)
	}
	if !stored.UpdatedAt.Equal(newNow) {
		t.Fatalf("updated_at should bump to newNow, got %v want %v", stored.UpdatedAt, newNow)
	}
}

// --- Input-compression per-key override tests --------------------------------

// TestUpdateAPIKeyWritesCompressCols checks that compress columns placed in
// the updates map are persisted by UpdateAPIKey -- the repository writes
// whatever the service layer puts into the map, so this confirms the plumbing
// end-to-end (map -> SQL -> stored row).
func TestUpdateAPIKeyWritesCompressCols(t *testing.T) {
	db, key := seedCustomScopeKeyForCSPTest(t)
	now := time.Now().UTC().Truncate(time.Second)
	updates := map[string]interface{}{
		"compress_enabled_override": true,
		"compress_enabled":          true,
	}
	if err := UpdateAPIKey(db, key.ID, updates, nil, false, now, nil); err != nil {
		t.Fatalf("UpdateAPIKey: %v", err)
	}

	var stored model.APIKey
	if err := db.First(&stored, key.ID).Error; err != nil {
		t.Fatalf("load stored: %v", err)
	}
	if !stored.CompressEnabledOverride || !stored.CompressEnabled {
		t.Fatalf("compress cols not persisted: override=%v enabled=%v",
			stored.CompressEnabledOverride, stored.CompressEnabled)
	}
}

// TestUpdateAPIKeyCompressCASConflict verifies that a stale expectedUpdatedAt
// returns ErrAPIKeyConflict (11013) even when the updates map contains only
// compress columns -- the CAS mechanism is column-agnostic.
func TestUpdateAPIKeyCompressCASConflict(t *testing.T) {
	db, key := seedCustomScopeKeyForCSPTest(t)
	t0 := key.UpdatedAt

	// First writer bumps updated_at.
	t1 := t0.Add(time.Second)
	updates := map[string]interface{}{
		"compress_enabled_override": true,
		"compress_enabled":          true,
	}
	if err := UpdateAPIKey(db, key.ID, updates, nil, false, t1, &t0); err != nil {
		t.Fatalf("CAS update against fresh snapshot should succeed: %v", err)
	}

	// Second writer reuses the stale t0 snapshot -- must conflict.
	updates2 := map[string]interface{}{
		"compress_enabled": false,
	}
	err := UpdateAPIKey(db, key.ID, updates2, nil, false, t1.Add(time.Second), &t0)
	if !errors.Is(err, errcode.ErrAPIKeyConflict) {
		t.Fatalf("want ErrAPIKeyConflict on stale CAS token, got %v", err)
	}

	// The conflicting update must roll back -- compress_enabled stays true.
	var stored model.APIKey
	if err := db.First(&stored, key.ID).Error; err != nil {
		t.Fatalf("load stored: %v", err)
	}
	if !stored.CompressEnabled {
		t.Fatalf("stale CAS must not overwrite; compress_enabled should still be true")
	}
}
