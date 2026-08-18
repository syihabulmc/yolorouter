package testutil

import (
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/yolorouter/yolorouter/internal/model"
)

// SeedRequestLog inserts one request_logs row with the given shape. The
// mutator applies test-specific overrides (status, fail_reason, attempts,
// attempts_detail, FKs) after the sensible defaults are filled in.
func SeedRequestLog(t *testing.T, db *gorm.DB, requestID string, ts time.Time, mut func(*model.RequestLog)) {
	t.Helper()
	row := model.RequestLog{
		RequestID:    requestID,
		ModelName:    "gpt-4o-mini",
		IsStream:     false,
		StatusCode:   200,
		InputTokens:  10,
		OutputTokens: 20,
		CostMicros:   100,
		CostKnown:    true,
		Attempts:     1,
		DurationMs:   42,
		CreatedAt:    ts.UTC(),
	}
	if mut != nil {
		mut(&row)
	}
	// Seeded with a plain create: testutil is imported by the repository
	// package's own tests, so the repository helpers are unreachable from
	// here (import cycle). CreatedAt is always set above.
	if err := db.Create(&row).Error; err != nil {
		t.Fatalf("SeedRequestLog %q: %v", requestID, err)
	}
}

// seedUser inserts a users row with the given username and returns its ID,
// so the API key SeedAPIKey creates below has a real owner to reference.
func seedUser(t *testing.T, db *gorm.DB, username string) uint {
	t.Helper()
	now := time.Now().UTC()
	u := model.User{
		Username:  username,
		Role:      model.RoleMember,
		Status:    model.UserStatusEnabled,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.Create(&u).Error; err != nil {
		t.Fatalf("seed user %q: %v", username, err)
	}
	return u.ID
}

// SeedAPIKey inserts an account named owner plus a minimal api_keys row it
// owns (unique key_hash), returning the key ID and the owner's user ID.
func SeedAPIKey(t *testing.T, db *gorm.DB, owner string) (uint, uint) {
	t.Helper()
	now := time.Now().UTC()
	userID := seedUser(t, db, owner)
	ak := model.APIKey{
		KeyHash:   "test-hash-" + owner + "-" + now.Format("150405.000000000"),
		KeyPrefix: "sk-xx-",
		UserID:    userID,
		Status:    model.APIKeyStatusActive,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := db.Create(&ak).Error; err != nil {
		t.Fatalf("seed api_key %q: %v", owner, err)
	}
	return ak.ID, userID
}

// SeedProvider inserts a providers row and returns its ID, so provider_id
// on request_logs can JOIN to a real provider_name in list/detail tests.
func SeedProvider(t *testing.T, db *gorm.DB, name string) uint {
	t.Helper()
	p := model.Provider{
		Name: name, ProviderType: "openai", BaseURL: "https://example.com/v1",
		ManagementStatus: model.ProviderStatusEnabled,
	}
	if err := db.Create(&p).Error; err != nil {
		t.Fatalf("seed provider %q: %v", name, err)
	}
	return p.ID
}

// ProviderMasterKey returns the fixed 32-byte AES-256-GCM key the handler
// and router tests encrypt provider credentials with. One definition so a
// fixture that must decrypt what another fixture encrypted cannot drift.
func ProviderMasterKey() []byte {
	key := make([]byte, 32)
	for i := range key {
		key[i] = byte(i)
	}
	return key
}
