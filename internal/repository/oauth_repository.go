// Package repository: OAuth provider / identity / auth-state data access.
package repository

import (
	"time"

	"gorm.io/gorm"

	"github.com/yolorouter/yolorouter/internal/model"
	"github.com/yolorouter/yolorouter/pkg/crypto"
)

// === oauth_providers ======================================================

// ListOAuthProviders returns every configured provider in creation order.
func ListOAuthProviders(db *gorm.DB) ([]model.OAuthProvider, error) {
	var rows []model.OAuthProvider
	if err := db.Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// ListEnabledOAuthProviders returns only the providers the login page may
// offer.
func ListEnabledOAuthProviders(db *gorm.DB) ([]model.OAuthProvider, error) {
	var rows []model.OAuthProvider
	if err := db.Where("enabled = ?", true).Order("id ASC").Find(&rows).Error; err != nil {
		return nil, err
	}
	return rows, nil
}

// FindOAuthProviderByID returns gorm.ErrRecordNotFound when absent.
func FindOAuthProviderByID(db *gorm.DB, id uint) (*model.OAuthProvider, error) {
	var p model.OAuthProvider
	if err := db.Where("id = ?", id).First(&p).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

// FindOAuthProviderBySlug returns gorm.ErrRecordNotFound when absent.
func FindOAuthProviderBySlug(db *gorm.DB, slug string) (*model.OAuthProvider, error) {
	var p model.OAuthProvider
	if err := db.Where("slug = ?", slug).First(&p).Error; err != nil {
		return nil, err
	}
	return &p, nil
}

// CreateOAuthProvider inserts a new provider row.
func CreateOAuthProvider(db *gorm.DB, p *model.OAuthProvider) error {
	return db.Create(p).Error
}

// UpdateOAuthProvider applies a sparse column update (only keys present in
// updates), bumping updated_at.
func UpdateOAuthProvider(db *gorm.DB, id uint, updates map[string]any, now time.Time) error {
	updates["updated_at"] = now
	return db.Model(&model.OAuthProvider{}).Where("id = ?", id).Updates(updates).Error
}

// DeleteOAuthProvider removes a provider together with its dependent
// rows: pending login states (one-time credentials, worthless without
// the provider) and identity links (unreachable without the provider,
// and their foreign keys would otherwise reject the provider DELETE
// outright). The accounts the identities pointed at survive — deleting a
// provider only removes that sign-in path, never the users.
func DeleteOAuthProvider(db *gorm.DB, id uint) error {
	return db.Transaction(func(tx *gorm.DB) error {
		if err := tx.Where("oauth_provider_id = ?", id).Delete(&model.AuthState{}).Error; err != nil {
			return err
		}
		if err := tx.Where("oauth_provider_id = ?", id).Delete(&model.UserIdentity{}).Error; err != nil {
			return err
		}
		return tx.Where("id = ?", id).Delete(&model.OAuthProvider{}).Error
	})
}

// CountIdentitiesForProvider reports how many accounts sign in through the
// provider — surfaced before deletion so the admin knows the blast radius.
func CountIdentitiesForProvider(db *gorm.DB, providerID uint) (int64, error) {
	var n int64
	err := db.Model(&model.UserIdentity{}).Where("oauth_provider_id = ?", providerID).Count(&n).Error
	return n, err
}

// === user_identities ======================================================

// FindIdentity resolves (provider, provider_user_id) to its identity row,
// or gorm.ErrRecordNotFound.
func FindIdentity(db *gorm.DB, providerID uint, providerUserID string) (*model.UserIdentity, error) {
	var ident model.UserIdentity
	if err := db.Where("oauth_provider_id = ? AND provider_user_id = ?", providerID, providerUserID).
		First(&ident).Error; err != nil {
		return nil, err
	}
	return &ident, nil
}

// CreateIdentity inserts a new identity link.
func CreateIdentity(db *gorm.DB, ident *model.UserIdentity) error {
	return db.Create(ident).Error
}

// === auth_states ==========================================================

// CreateAuthState persists a new one-time state for rawToken. Exactly like
// CreateSession, this is the only constructor of the model — the
// raw-token-to-hash transform happens in one place.
func CreateAuthState(db *gorm.DB, rawToken string, providerID uint, codeVerifier, redirectURI string, expiresAt, createdAt time.Time) error {
	row := &model.AuthState{
		TokenHash:       crypto.HashToken(rawToken),
		OAuthProviderID: providerID,
		CodeVerifier:    codeVerifier,
		RedirectURI:     redirectURI,
		ExpiresAt:       expiresAt,
		CreatedAt:       createdAt,
	}
	return db.Create(row).Error
}

// DeleteExpiredAuthStates removes every expired one-time state. Called
// from BeginLogin so the anonymous state endpoint cannot grow the table
// unboundedly through abandoned attempts — the same amortized-cleanup
// pattern session login uses.
func DeleteExpiredAuthStates(db *gorm.DB, now time.Time) error {
	return db.Where("expires_at <= ?", now).Delete(&model.AuthState{}).Error
}

// ConsumeAuthState atomically spends the state identified by rawToken,
// returning its row. The conditional UPDATE (consumed_at IS NULL, not
// expired, same provider) is the replay defense: of any number of
// concurrent callbacks carrying the same state, exactly one can flip
// consumed_at — every other caller sees 0 rows affected and gets
// gorm.ErrRecordNotFound, indistinguishable from a state that never
// existed. Provider is part of the predicate so a state issued for one
// provider can never authorize a callback from another.
//
// DeleteExpiredAuthStates piggybacks on the same call: like session
// cleanup amortized across logins, abandoned login attempts (the common
// case for expired states) are swept on the next successful consume
// instead of needing a cron.
func ConsumeAuthState(db *gorm.DB, rawToken string, providerID uint, now time.Time) (*model.AuthState, error) {
	hash := crypto.HashToken(rawToken)
	var row model.AuthState
	err := db.Transaction(func(tx *gorm.DB) error {
		res := tx.Model(&model.AuthState{}).
			Where("id = ? AND oauth_provider_id = ? AND consumed_at IS NULL AND expires_at > ?", hash, providerID, now).
			Update("consumed_at", now)
		if res.Error != nil {
			return res.Error
		}
		if res.RowsAffected != 1 {
			return gorm.ErrRecordNotFound
		}
		if err := tx.Where("id = ?", hash).First(&row).Error; err != nil {
			return err
		}
		return tx.Where("expires_at <= ? AND id != ?", now, hash).Delete(&model.AuthState{}).Error
	})
	if err != nil {
		return nil, err
	}
	return &row, nil
}
