// Package repository is the pure data-access layer for the user/session
// tables — no business judgment here (that's internal/service's job), just
// reads and writes against internal/model structs.
package repository

import (
	"time"

	"gorm.io/gorm"

	"github.com/yolorouter/yolorouter/internal/model"
)

// CountLocalUsers reports how many local (password-login) accounts exist
// — 0 or 1 by schema (partial unique index on is_local), used to decide
// whether first-run setup is still available. Counting all users would
// be wrong here: externally-provisioned accounts must not make setup
// look "already done" on an instance whose local admin was never created.
func CountLocalUsers(db *gorm.DB) (int64, error) {
	var count int64
	err := db.Model(&model.User{}).Where("is_local = ?", true).Count(&count).Error
	return count, err
}

// CreateUser inserts a new user row, populating user.ID on success.
func CreateUser(db *gorm.DB, user *model.User) error {
	return db.Create(user).Error
}

// FindLocalUserByUsername returns the local (password-login) account with
// that username, or gorm.ErrRecordNotFound. Password login deliberately
// only ever matches the local account: externally-provisioned users have
// an empty password hash and must never be reachable through the
// password form, even by username collision. Callers must not
// distinguish not-found from a wrong password: never reveal whether an
// account exists, only "invalid username or password".
func FindLocalUserByUsername(db *gorm.DB, username string) (*model.User, error) {
	var user model.User
	if err := db.Where("username = ? AND is_local = ?", username, true).First(&user).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

// FindUserByID returns gorm.ErrRecordNotFound if id doesn't exist.
func FindUserByID(db *gorm.DB, id uint) (*model.User, error) {
	var user model.User
	if err := db.Where("id = ?", id).First(&user).Error; err != nil {
		return nil, err
	}
	return &user, nil
}

// UpdateUserPasswordHash overwrites the stored password hash.
func UpdateUserPasswordHash(db *gorm.DB, id uint, passwordHash string, now time.Time) error {
	return db.Model(&model.User{}).Where("id = ?", id).
		Updates(map[string]interface{}{"password_hash": passwordHash, "updated_at": now}).Error
}

// RecordLoginFailure atomically increments the user's consecutive
// failed-login counter and applies a lock once it reaches lockThreshold.
// If the user's
// previous lock has already expired (locked_until <= now), this failure
// starts a fresh count of 1 instead of continuing the old count: otherwise
// the very first retry after a lock expires would immediately re-trigger
// it, which in effect never lets the lock actually expire.
//
// now and the resulting unlock time are both computed in Go and passed in
// as bound parameters rather than using SQL date-arithmetic functions
// (SQLite's datetime(...) vs Postgres's `+ interval` have incompatible
// syntax) — this keeps the statement identical across both drivers.
//
// Returns the resulting locked_until (nil if this failure didn't lock the
// account) via `RETURNING`, so the caller doesn't need a second SELECT
// just to learn whether this exact call crossed the lock threshold —
// Postgres and SQLite 3.35+ (this project's minimum, via
// modernc.org/sqlite) both support the same RETURNING syntax.
func RecordLoginFailure(db *gorm.DB, userID uint, now time.Time, lockThreshold int, lockDuration time.Duration) (*time.Time, error) {
	lockedUntil := now.Add(lockDuration)
	var result struct {
		LockedUntil *time.Time `gorm:"column:locked_until"`
	}
	err := db.Raw(`
		UPDATE users
		SET failed_login_count = CASE
				WHEN locked_until IS NOT NULL AND locked_until <= ? THEN 1
				ELSE failed_login_count + 1
			END,
			locked_until = CASE
				-- A fresh count of 1 is never >= lockThreshold (a threshold
				-- of 1 would mean "lock on the very first failure ever",
				-- not a real configuration this feature supports), so a
				-- just-expired lock's reset case never needs to check the
				-- new count against the threshold — it's always NULL.
				WHEN locked_until IS NOT NULL AND locked_until <= ? THEN NULL
				WHEN failed_login_count + 1 >= ? THEN ?
				ELSE NULL
			END,
			updated_at = ?
		WHERE id = ?
		RETURNING locked_until
	`, now, now, lockThreshold, lockedUntil, now, userID).Scan(&result).Error
	if err != nil {
		return nil, err
	}
	return result.LockedUntil, nil
}

// RecordLoginSuccess atomically clears the failed-login counter and lock,
// and stamps last_login_at.
func RecordLoginSuccess(db *gorm.DB, userID uint, now time.Time) error {
	return db.Exec(`
		UPDATE users SET failed_login_count = 0, locked_until = NULL, last_login_at = ?, updated_at = ?
		WHERE id = ?
	`, now, now, userID).Error
}
