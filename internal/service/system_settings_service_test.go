package service

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/yolorouter/yolorouter/pkg/errcode"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"
)

func newSvcTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		t.Fatalf("open sqlite: %v", err)
	}
	// Pin the pool to a single connection so :memory: is shared across all
	// queries (otherwise modernc/sqlite gives each connection its own private
	// in-memory DB) and concurrent transactions serialize through it — this
	// is what makes the CAS conflict deterministic in the concurrent test.
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("underlying *sql.DB: %v", err)
	}
	sqlDB.SetMaxOpenConns(1)
	db.Exec(`CREATE TABLE system_settings (key TEXT PRIMARY KEY, value TEXT NOT NULL DEFAULT '', version INTEGER NOT NULL DEFAULT 1, updated_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP)`)
	db.Exec(`INSERT INTO system_settings (key, value) VALUES ('custom_system_prompt_enabled','false'),('custom_system_prompt','')`)
	return db
}

func TestSystemSettingsServiceReadReturnsSeededDisabled(t *testing.T) {
	svc := NewSystemSettingsService(newSvcTestDB(t))
	s, ver, err := svc.CustomSystemPrompt(context.Background())
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if s.Enabled || s.Text != "" || ver != 1 {
		t.Fatalf("want disabled/empty/v1, got %+v v%d", s, ver)
	}
}

func TestSystemSettingsServiceUpdatePublishesImmediately(t *testing.T) {
	svc := NewSystemSettingsService(newSvcTestDB(t))
	if _, _, err := svc.UpdateCustomSystemPrompt(context.Background(), 1, true, "hi"); err != nil {
		t.Fatalf("update: %v", err)
	}
	// read path sees the new value without any invalidate
	s, _, err := svc.CustomSystemPrompt(context.Background())
	if err != nil || !s.Enabled || s.Text != "hi" {
		t.Fatalf("read after update mismatch: %+v err=%v", s, err)
	}
}

func TestSystemSettingsServiceUpdateConflict(t *testing.T) {
	svc := NewSystemSettingsService(newSvcTestDB(t))
	if _, _, err := svc.UpdateCustomSystemPrompt(context.Background(), 1, true, "a"); err != nil {
		t.Fatalf("first: %v", err)
	}
	_, _, err := svc.UpdateCustomSystemPrompt(context.Background(), 1, false, "")
	if !errors.Is(err, errcode.ErrCustomSystemPromptConflict) {
		t.Fatalf("want conflict, got %v", err)
	}
}

func TestSystemSettingsServiceRejectsTooLong(t *testing.T) {
	svc := NewSystemSettingsService(newSvcTestDB(t))
	long := make([]rune, MaxCustomSystemPromptLen+1)
	for i := range long {
		long[i] = 'x'
	}
	_, _, err := svc.UpdateCustomSystemPrompt(context.Background(), 1, true, string(long))
	if !errors.Is(err, errcode.ErrCustomSystemPromptTooLong) {
		t.Fatalf("want too-long, got %v", err)
	}
}

func TestSystemSettingsServiceRejectsEnabledEmpty(t *testing.T) {
	svc := NewSystemSettingsService(newSvcTestDB(t))
	_, _, err := svc.UpdateCustomSystemPrompt(context.Background(), 1, true, "")
	if !errors.Is(err, errcode.ErrCustomSystemPromptEmpty) {
		t.Fatalf("want empty, got %v", err)
	}
}

// Two concurrent PUTs: the loser must see Conflict, and the cache must end on
// the winner's version (monotonic publish — a late-publishing loser can't
// roll the cache back to an older version).
func TestSystemSettingsServiceConcurrentPUTsMonotonic(t *testing.T) {
	svc := NewSystemSettingsService(newSvcTestDB(t))
	var wg sync.WaitGroup
	var conflicts atomic.Int32
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _, err := svc.UpdateCustomSystemPrompt(context.Background(), 1, true, "race")
			if errors.Is(err, errcode.ErrCustomSystemPromptConflict) {
				conflicts.Add(1)
			}
		}()
	}
	wg.Wait()
	if conflicts.Load() != 1 {
		t.Fatalf("want exactly 1 conflict, got %d", conflicts.Load())
	}
	s, ver, err := svc.CustomSystemPrompt(context.Background())
	if err != nil || !s.Enabled || s.Text != "race" || ver != 2 {
		t.Fatalf("cache not at winner v2: %+v v%d err=%v", s, ver, err)
	}
}

// TestSystemSettingsServiceRefreshFailureDoesNotHammer verifies that after a
// refresh failure the negative-TTL window prevents repeated DB queries: the
// first stale read triggers one refresh (which fails and returns
// last-known-good + error), and subsequent reads within the failure window
// return last-known-good silently (nil error, no refresh).
func TestSystemSettingsServiceRefreshFailureDoesNotHammer(t *testing.T) {
	db := newSvcTestDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("underlying *sql.DB: %v", err)
	}
	svc := NewSystemSettingsService(db)

	// Warm the cache with a known value.
	if _, _, err := svc.UpdateCustomSystemPrompt(context.Background(), 1, true, "warm"); err != nil {
		t.Fatalf("seed: %v", err)
	}
	s, _, err := svc.CustomSystemPrompt(context.Background())
	if err != nil || !s.Enabled || s.Text != "warm" {
		t.Fatalf("warm read: %+v err=%v", s, err)
	}

	// Force the cache stale so the next read triggers a refresh.
	svc.mu.Lock()
	svc.refreshExpiry = time.Now().Add(-time.Second)
	svc.mu.Unlock()

	// Break the DB so the refresh will fail.
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	// First call: triggers refresh, fails, sets failure window, returns
	// last-known-good + error.
	s1, _, err1 := svc.CustomSystemPrompt(context.Background())
	if err1 == nil {
		t.Fatalf("first stale read: want error, got nil")
	}
	if !s1.Enabled || s1.Text != "warm" {
		t.Fatalf("first stale read: want last-known-good warm, got %+v", s1)
	}

	// Verify failure window is active.
	svc.mu.RLock()
	failureUntil := svc.refreshFailureUntil
	svc.mu.RUnlock()
	if !time.Now().Before(failureUntil) {
		t.Fatalf("failure window not set or already expired: %v", failureUntil)
	}

	// Subsequent calls within the failure window: no refresh, no error,
	// return last-known-good. If these calls hit the DB they would error
	// (the DB is closed), so a nil error proves no DB query happened.
	for i := 0; i < 5; i++ {
		s2, _, err2 := svc.CustomSystemPrompt(context.Background())
		if err2 != nil {
			t.Fatalf("call %d: want nil error in failure window, got %v", i, err2)
		}
		if !s2.Enabled || s2.Text != "warm" {
			t.Fatalf("call %d: want last-known-good warm, got %+v", i, s2)
		}
	}
}

// TestSystemSettingsServiceRefreshFailureColdStartDoesNotHammer verifies the
// cold-start failure path: the first call triggers one refresh (fails, returns
// zero/disabled + error), and subsequent calls within the failure window return
// zero/disabled silently without re-querying the DB.
func TestSystemSettingsServiceRefreshFailureColdStartDoesNotHammer(t *testing.T) {
	db := newSvcTestDB(t)
	sqlDB, err := db.DB()
	if err != nil {
		t.Fatalf("underlying *sql.DB: %v", err)
	}
	svc := NewSystemSettingsService(db)

	// Close the DB before the first read — cold start with a broken DB.
	if err := sqlDB.Close(); err != nil {
		t.Fatalf("close db: %v", err)
	}

	// First call: triggers refresh, fails, sets failure window, returns
	// zero/disabled + error.
	s1, _, err1 := svc.CustomSystemPrompt(context.Background())
	if err1 == nil {
		t.Fatalf("first cold read: want error, got nil")
	}
	if s1.Enabled || s1.Text != "" {
		t.Fatalf("first cold read: want zero/disabled, got %+v", s1)
	}

	// Verify failure window is active.
	svc.mu.RLock()
	failureUntil := svc.refreshFailureUntil
	svc.mu.RUnlock()
	if !time.Now().Before(failureUntil) {
		t.Fatalf("failure window not set or already expired: %v", failureUntil)
	}

	// Subsequent calls within the failure window: no refresh, no error,
	// zero/disabled. If these calls hit the DB they would error (the DB is
	// closed), so a nil error proves no DB query happened.
	for i := 0; i < 5; i++ {
		s2, _, err2 := svc.CustomSystemPrompt(context.Background())
		if err2 != nil {
			t.Fatalf("call %d: want nil error in failure window, got %v", i, err2)
		}
		if s2.Enabled || s2.Text != "" {
			t.Fatalf("call %d: want zero/disabled, got %+v", i, s2)
		}
	}
}
