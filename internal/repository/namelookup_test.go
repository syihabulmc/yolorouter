package repository

import (
	"testing"
	"time"

	"github.com/yolorouter/yolorouter/internal/model"
	"github.com/yolorouter/yolorouter/internal/testutil"
)

// TestCollectPtrIDs: nil ids are skipped and duplicates collapse — the
// whole point of the collector is that callers stop hand-rolling both
// guards at every enrichment site.
func TestCollectPtrIDs(t *testing.T) {
	one, two := uint(1), uint(2)
	rows := []struct{ ID *uint }{{&one}, {nil}, {&two}, {&one}, {&two}}
	got := CollectPtrIDs(rows, func(r *struct{ ID *uint }) *uint { return r.ID })
	if len(got) != 2 || got[0] != 1 || got[1] != 2 {
		t.Fatalf("expected deduped [1 2], got %v", got)
	}
	if empty := CollectPtrIDs([]struct{ ID *uint }{}, func(r *struct{ ID *uint }) *uint { return r.ID }); len(empty) != 0 {
		t.Fatalf("empty input must collect nothing, got %v", empty)
	}
}

// TestFindOwnerIdentitiesByKeyIDs: resolves key id → owner username + prefix in
// one query; missing keys are simply absent so callers render zero values.
func TestFindOwnerIdentitiesByKeyIDs(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	now := time.Now().UTC()
	u := &model.User{Username: "dave", Role: model.RoleMember, Status: model.UserStatusEnabled,
		CreatedAt: now, UpdatedAt: now}
	if err := CreateUser(db, u); err != nil {
		t.Fatalf("seed user: %v", err)
	}
	k := &model.APIKey{KeyHash: "h-dave", KeyPrefix: "sk-yr-dv", UserID: u.ID,
		Status: model.APIKeyStatusActive, AllowAllModels: true, CreatedAt: now, UpdatedAt: now}
	if err := CreateAPIKey(db, k, nil, now); err != nil {
		t.Fatalf("seed key: %v", err)
	}

	got, err := FindOwnerIdentitiesByKeyIDs(db, []uint{k.ID, 999999})
	if err != nil {
		t.Fatalf("FindOwnerIdentitiesByKeyIDs: %v", err)
	}
	if id := got[k.ID]; id.Username != "dave" || id.KeyPrefix != "sk-yr-dv" {
		t.Fatalf("identity mismatch: %+v", id)
	}
	if _, ok := got[999999]; ok {
		t.Fatalf("missing key must be absent from the map")
	}
	if empty, err := FindOwnerIdentitiesByKeyIDs(db, nil); err != nil || len(empty) != 0 {
		t.Fatalf("empty ids must return empty map without querying, got %v %v", empty, err)
	}
}
