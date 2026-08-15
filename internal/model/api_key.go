// Package model defines APIKey / APIKeyModel.
// Schema lives in migrations/{sqlite,postgres}/00006_create_api_keys.sql —
// goose owns DDL, GORM here is query-only (no AutoMigrate).
package model

import "time"

const (
	APIKeyStatusActive  = 1
	APIKeyStatusRevoked = 2
)

// APIKey is one Yolorouter calling credential. The plaintext key is persisted
// as AES-GCM ciphertext in EncryptedKey (migration 00021) so it can be
// revealed again from the list page; the auth path does NOT use it — gateway
// auth still looks the key up by KeyHash (SHA-256, indexed). EncryptedKey is
// NULL for keys created before 00021 landed: their plaintext was never stored,
// so they cannot be recovered (the reveal endpoint returns a dedicated code).
// KeyPrefix is the first chars only (list-distinguishing — not enough to
// reconstruct the full key). Limits are pointer-typed so NULL means
// "no cap". Budget is integer micros (int64, major-unit × 1e6) to avoid float
// drift on a cumulative hard cap while keeping 6-decimal precision. KeyHash,
// EncryptedKey and RevokedAt are json:"-": the hash and ciphertext must
// never serialize back out, and revoked state surfaces via display_status
// rather than a raw timestamp.
type APIKey struct {
	ID      uint   `gorm:"column:id;primaryKey" json:"id"`
	KeyHash string `gorm:"column:key_hash" json:"-"`
	// UserID is the owning account (migration 00024). Every key belongs to
	// exactly one user; 0 is never a valid owner (see the migration's
	// rationale) and the create path refuses to persist it.
	UserID uint `gorm:"column:user_id" json:"user_id"`
	// EncryptedKey is the AES-GCM ciphertext (base64, nonce prepended) of the
	// full plaintext key, persisted so it can be revealed again. Empty for
	// pre-00021 keys. Read only on the reveal path; never used for auth.
	EncryptedKey      string     `gorm:"column:encrypted_key" json:"-"`
	KeyPrefix         string     `gorm:"column:key_prefix" json:"key_prefix"`
	Remark            string     `gorm:"column:remark" json:"remark"`
	Status            int        `gorm:"column:status" json:"status"`
	ExpiresAt         *time.Time `gorm:"column:expires_at" json:"expires_at"`
	RPMLimit          *int       `gorm:"column:rpm_limit" json:"rpm_limit"`
	TPMLimit          *int       `gorm:"column:tpm_limit" json:"tpm_limit"`
	ConcurrencyLimit  *int       `gorm:"column:concurrency_limit" json:"concurrency_limit"`
	BudgetLimitMicros *int64     `gorm:"column:budget_limit_micros" json:"budget_limit_micros"`
	BudgetSpentMicros int64      `gorm:"column:budget_spent_micros" json:"budget_spent_micros"`
	// AllowAllModels, when true, lets the key call any enabled model and the
	// api_key_models allowlist is ignored (see gateway enforcement). False keeps
	// the strict allowlist where an empty list permits no models.
	AllowAllModels bool `gorm:"column:allow_all_models" json:"allow_all_models"`
	// CustomSystemPromptEnabledOverride: true means this key explicitly
	// overrides the global default; false means inherit system_settings. When
	// override is true, CustomSystemPromptEnabled and CustomSystemPrompt take
	// effect; otherwise they are ignored.
	CustomSystemPromptEnabledOverride bool   `gorm:"column:custom_system_prompt_enabled_override" json:"custom_system_prompt_enabled_override"`
	CustomSystemPromptEnabled         bool   `gorm:"column:custom_system_prompt_enabled" json:"custom_system_prompt_enabled"`
	CustomSystemPrompt                string `gorm:"column:custom_system_prompt" json:"custom_system_prompt"`
	// CompressEnabledOverride: true means this key explicitly overrides the
	// global input-compression default; false means inherit system_settings.
	// When override is true, CompressEnabled takes effect; otherwise ignored.
	CompressEnabledOverride bool       `gorm:"column:compress_enabled_override" json:"compress_enabled_override"`
	CompressEnabled         bool       `gorm:"column:compress_enabled" json:"compress_enabled"`
	CreatedAt               time.Time  `gorm:"column:created_at" json:"created_at"`
	RevokedAt               *time.Time `gorm:"column:revoked_at" json:"-"`
	UpdatedAt               time.Time  `gorm:"column:updated_at" json:"updated_at"`
}

func (APIKey) TableName() string { return "api_keys" }

// APIKeyModel is one row of a key's model allowlist. Stored by
// model_id rather than model name, so renaming a model doesn't break the
// whitelists of keys that reference it; a stopped model stays in the list and
// is rejected at route time (handled by the gateway module).
type APIKeyModel struct {
	ID        uint      `gorm:"column:id;primaryKey" json:"id"`
	APIKeyID  uint      `gorm:"column:api_key_id" json:"api_key_id"`
	ModelID   uint      `gorm:"column:model_id" json:"model_id"`
	CreatedAt time.Time `gorm:"column:created_at" json:"created_at"`
}

func (APIKeyModel) TableName() string { return "api_key_models" }
