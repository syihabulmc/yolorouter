// OAuthProvider / UserIdentity / AuthState back external login.
// Schema lives in migrations/{sqlite,postgres}/00025_oauth_login.sql —
// goose owns DDL, GORM here is query-only (no AutoMigrate).
package model

import "time"

// OAuth token-endpoint client authentication styles.
const (
	OAuthAuthStyleBasic = "basic" // client_secret_basic: Authorization header
	OAuthAuthStylePost  = "post"  // client_secret_post: form body parameters
)

// OAuthProvider is one admin-configured identity source. A single generic
// shape covers every standard OAuth2/OIDC provider: three endpoints,
// credentials, scopes, and JSON field paths into the userinfo response.
// EncryptedClientSecret is AES-GCM ciphertext under the provider master
// key and is json:"-" — the secret is write-only through the API.
type OAuthProvider struct {
	ID                    uint   `gorm:"column:id;primaryKey" json:"id"`
	Slug                  string `gorm:"column:slug" json:"slug"`
	Name                  string `gorm:"column:name" json:"name"`
	Icon                  string `gorm:"column:icon" json:"icon"`
	Enabled               bool   `gorm:"column:enabled" json:"enabled"`
	ClientID              string `gorm:"column:client_id" json:"client_id"`
	EncryptedClientSecret string `gorm:"column:encrypted_client_secret" json:"-"`
	AuthorizationEndpoint string `gorm:"column:authorization_endpoint" json:"authorization_endpoint"`
	TokenEndpoint         string `gorm:"column:token_endpoint" json:"token_endpoint"`
	UserinfoEndpoint      string `gorm:"column:userinfo_endpoint" json:"userinfo_endpoint"`
	Scopes                string `gorm:"column:scopes" json:"scopes"`
	// Field paths into the userinfo JSON (dot-separated for nesting, e.g.
	// "data.user.id"), so non-OIDC providers with custom shapes still map.
	UserIDField      string    `gorm:"column:user_id_field" json:"user_id_field"`
	UsernameField    string    `gorm:"column:username_field" json:"username_field"`
	DisplayNameField string    `gorm:"column:display_name_field" json:"display_name_field"`
	EmailField       string    `gorm:"column:email_field" json:"email_field"`
	AuthStyle        string    `gorm:"column:auth_style" json:"auth_style"`
	CreatedAt        time.Time `gorm:"column:created_at" json:"created_at"`
	UpdatedAt        time.Time `gorm:"column:updated_at" json:"updated_at"`
}

func (OAuthProvider) TableName() string { return "oauth_providers" }

// UserIdentity links one provider identity to one account. The provider's
// stable user id is the identity — never the username or email, both of
// which can change or be reassigned upstream. There is no bind/merge flow:
// each (provider, provider_user_id) pair owns exactly one account.
type UserIdentity struct {
	ID              uint      `gorm:"column:id;primaryKey" json:"id"`
	UserID          uint      `gorm:"column:user_id" json:"user_id"`
	OAuthProviderID uint      `gorm:"column:oauth_provider_id" json:"oauth_provider_id"`
	ProviderUserID  string    `gorm:"column:provider_user_id" json:"provider_user_id"`
	CreatedAt       time.Time `gorm:"column:created_at" json:"created_at"`
}

func (UserIdentity) TableName() string { return "user_identities" }

// AuthState is a one-time login-flow credential. TokenHash is the SHA-256
// of the raw state token (the value round-tripped through the provider's
// `state` parameter) — same recipe and reasoning as UserSession.TokenHash.
// CodeVerifier is this flow's PKCE secret; RedirectURI pins the exact
// redirect_uri the authorization request used so the token exchange
// repeats it verbatim.
type AuthState struct {
	TokenHash       string     `gorm:"column:id;primaryKey" json:"-"`
	OAuthProviderID uint       `gorm:"column:oauth_provider_id" json:"oauth_provider_id"`
	CodeVerifier    string     `gorm:"column:code_verifier" json:"-"`
	RedirectURI     string     `gorm:"column:redirect_uri" json:"redirect_uri"`
	ConsumedAt      *time.Time `gorm:"column:consumed_at" json:"consumed_at"`
	ExpiresAt       time.Time  `gorm:"column:expires_at" json:"expires_at"`
	CreatedAt       time.Time  `gorm:"column:created_at" json:"created_at"`
}

func (AuthState) TableName() string { return "auth_states" }
