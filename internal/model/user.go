// Package model defines the GORM-mapped structs for the account/session
// tables. Schema itself lives in migrations/{sqlite,postgres}/
// 00023_users_multi_account.sql — goose owns DDL, GORM here is
// query-only (no AutoMigrate).
package model

import "time"

// User roles. Stored as strings rather than numeric levels: there are
// exactly two roles, and "admin"/"member" in a DB row or a log line
// needs no decoder ring.
const (
	RoleAdmin  = "admin"
	RoleMember = "member"
)

// User statuses. 1/2 rather than 0/1 so the Go zero value can never be
// mistaken for a valid status — same convention as api_keys.status.
const (
	UserStatusEnabled  = 1
	UserStatusDisabled = 2
)

// User is an account that can sign in to the console. Exactly one user
// may have IsLocal=true (enforced by a partial unique index): that is
// the password-login account created by first-run setup, which also
// serves as the escape hatch when external login is misconfigured. All
// other accounts are provisioned through external identity providers
// and carry an empty PasswordHash.
//
// PasswordHash and the lockout-state fields are explicitly json:"-":
// handlers today only ever expose a hand-picked gin.H{...}, never this
// struct directly, but without the tag a future response.Success(c, user)
// (or any other generic serialization) would silently leak the bcrypt
// hash and account-lockout internals.
type User struct {
	ID               uint       `gorm:"column:id;primaryKey" json:"id"`
	Username         string     `gorm:"column:username" json:"username"`
	PasswordHash     string     `gorm:"column:password_hash" json:"-"`
	DisplayName      string     `gorm:"column:display_name" json:"display_name"`
	Email            string     `gorm:"column:email" json:"email"`
	Role             string     `gorm:"column:role" json:"role"`
	Status           int        `gorm:"column:status" json:"status"`
	IsLocal          bool       `gorm:"column:is_local" json:"is_local"`
	FailedLoginCount int        `gorm:"column:failed_login_count" json:"-"`
	LockedUntil      *time.Time `gorm:"column:locked_until" json:"-"`
	LastLoginAt      *time.Time `gorm:"column:last_login_at" json:"last_login_at"`
	CreatedAt        time.Time  `gorm:"column:created_at" json:"created_at"`
	UpdatedAt        time.Time  `gorm:"column:updated_at" json:"-"`
}

// TableName pins the table name explicitly rather than relying on GORM's
// default pluralization — the migration created "users", and an implicit
// mismatch here would only surface as a runtime "no such table" error.
func (User) TableName() string { return "users" }
