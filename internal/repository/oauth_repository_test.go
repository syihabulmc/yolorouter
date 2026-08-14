package repository

import (
	"errors"
	"testing"
	"time"

	"gorm.io/gorm"

	"github.com/yolorouter/yolorouter/internal/model"
	"github.com/yolorouter/yolorouter/internal/testutil"
)

// TestDeleteOAuthProviderClearsDependentRows pins the delete semantics:
// a provider with linked identities and pending login states must delete
// cleanly (both reference tables carry foreign keys to it — leaving the
// rows behind would make the DELETE itself fail), while the accounts
// those identities pointed at survive untouched.
func TestDeleteOAuthProviderClearsDependentRows(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	now := time.Now().UTC()

	p := &model.OAuthProvider{
		Slug: "doomed", Name: "Doomed", Enabled: true,
		ClientID: "cid", EncryptedClientSecret: "ct",
		AuthorizationEndpoint: "https://idp/authorize",
		TokenEndpoint:         "https://idp/token",
		UserinfoEndpoint:      "https://idp/userinfo",
		Scopes:                "openid", UserIDField: "sub", UsernameField: "preferred_username",
		DisplayNameField: "name", EmailField: "email", AuthStyle: model.OAuthAuthStylePost,
		CreatedAt: now, UpdatedAt: now,
	}
	if err := CreateOAuthProvider(db, p); err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	member := newMemberUser("oauth-member", now)
	if err := CreateUser(db, member); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	if err := CreateIdentity(db, &model.UserIdentity{
		UserID: member.ID, OAuthProviderID: p.ID, ProviderUserID: "ext-1", CreatedAt: now,
	}); err != nil {
		t.Fatalf("seed identity: %v", err)
	}
	if err := CreateAuthState(db, "pending-state", p.ID, "verifier", "http://cb", now.Add(10*time.Minute), now); err != nil {
		t.Fatalf("seed auth state: %v", err)
	}

	if err := DeleteOAuthProvider(db, p.ID); err != nil {
		t.Fatalf("DeleteOAuthProvider with dependents must succeed, got: %v", err)
	}

	if _, err := FindOAuthProviderByID(db, p.ID); !errors.Is(err, gorm.ErrRecordNotFound) {
		t.Fatalf("provider row should be gone, got: %v", err)
	}
	var identCount, stateCount int64
	if err := db.Model(&model.UserIdentity{}).Where("oauth_provider_id = ?", p.ID).Count(&identCount).Error; err != nil {
		t.Fatalf("count identities: %v", err)
	}
	if err := db.Model(&model.AuthState{}).Where("oauth_provider_id = ?", p.ID).Count(&stateCount).Error; err != nil {
		t.Fatalf("count states: %v", err)
	}
	if identCount != 0 || stateCount != 0 {
		t.Fatalf("dependent rows must be cleared, got identities=%d states=%d", identCount, stateCount)
	}
	// The account itself survives — only its sign-in path through this
	// provider is gone.
	if _, err := FindUserByID(db, member.ID); err != nil {
		t.Fatalf("account must survive provider deletion: %v", err)
	}
}
