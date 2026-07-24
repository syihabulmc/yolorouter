package service

import (
	"context"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/yolorouter/yolorouter/internal/repository"
	"github.com/yolorouter/yolorouter/internal/settings"
	"github.com/yolorouter/yolorouter/pkg/errcode"

	"golang.org/x/sync/singleflight"
	"gorm.io/gorm"
)

// MaxCustomSystemPromptLen bounds the prompt text by utf8 rune count. Enforced
// in the service layer (not the DDL) so PG's VARCHAR(2000) and SQLite's TEXT
// behave identically.
const MaxCustomSystemPromptLen = 2000

// refreshTimeout caps how long a cache refresh may block the request path.
const refreshTimeout = 500 * time.Millisecond

// cacheTTL is how long a successfully refreshed snapshot is served without
// re-querying the database.
const cacheTTL = 30 * time.Second

// refreshFailureTTL is the negative-TTL window applied after a refresh
// failure. During this window the read path serves last-known-good (or
// zero/disabled on cold start) without re-querying the DB, preventing a
// refresh storm and duplicate warning logs when the DB is down.
const refreshFailureTTL = 5 * time.Second

// SystemSettingsService caches the global custom system prompt and owns its
// read/write contract. It implements gateway.SettingsProvider (read path) and
// serves the handler's GET/PUT. CSP is best-effort behavior guidance, NOT a
// security boundary — on refresh failure it fails OPEN (returns last-known-good
// or disabled), never blocks the request path.
type SystemSettingsService struct {
	db *gorm.DB

	mu                  sync.RWMutex
	snapshot            settings.CustomSystemPromptSetting
	version             int64
	hasSnapshot         bool
	refreshExpiry       time.Time
	refreshFailureUntil time.Time
	refreshGroup        singleflight.Group
}

// NewSystemSettingsService constructs the service and primes the cache on the
// first read.
func NewSystemSettingsService(db *gorm.DB) *SystemSettingsService {
	return &SystemSettingsService{db: db}
}

// CustomSystemPrompt returns the cached snapshot (non-blocking). On cold cache
// or stale snapshot it triggers a singleflight refresh with a strict short
// timeout; on failure it returns last-known-good + error (fail-open).
func (s *SystemSettingsService) CustomSystemPrompt(ctx context.Context) (settings.CustomSystemPromptSetting, int64, error) {
	if cached, ver, ok := s.cached(); ok {
		return cached, ver, nil
	}
	// Negative cache on cold start (no snapshot yet): if a recent refresh
	// failed, serve zero/disabled silently instead of re-querying the DB on
	// every call. CSP is best-effort guidance, not a security gate.
	if s.inFailureWindow() {
		return settings.CustomSystemPromptSetting{}, 0, nil
	}
	// singleflight collapses concurrent refreshes into one DB query.
	v, err, _ := s.refreshGroup.Do("csp", func() (interface{}, error) {
		return s.refresh(ctx)
	})
	if err != nil {
		// fail-open: return last-known-good (or zero) + error
		s.mu.RLock()
		defer s.mu.RUnlock()
		return s.snapshot, s.version, err
	}
	snap := v.(systemSettingsSnapshot)
	return snap.Setting, snap.Version, nil
}

// systemSettingsSnapshot is the refresh result carried through singleflight.
type systemSettingsSnapshot struct {
	Setting settings.CustomSystemPromptSetting
	Version int64
}

// inFailureWindow reports whether a recent refresh failed and the
// negative-TTL cooldown is still active.
func (s *SystemSettingsService) inFailureWindow() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return time.Now().Before(s.refreshFailureUntil)
}

func (s *SystemSettingsService) cached() (settings.CustomSystemPromptSetting, int64, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if !s.hasSnapshot {
		return settings.CustomSystemPromptSetting{}, 0, false
	}
	now := time.Now()
	if now.Before(s.refreshExpiry) {
		return s.snapshot, s.version, true
	}
	// Negative TTL: a recent refresh failed — keep serving last-known-good
	// instead of re-querying the DB on every request.
	if now.Before(s.refreshFailureUntil) {
		return s.snapshot, s.version, true
	}
	return settings.CustomSystemPromptSetting{}, 0, false
}

func (s *SystemSettingsService) refresh(ctx context.Context) (systemSettingsSnapshot, error) {
	// WithoutCancel detaches the caller's cancellation signal so a single client
	// disconnect does NOT abort the shared singleflight refresh other waiters
	// depend on. Only refreshTimeout bounds the query.
	rctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), refreshTimeout)
	defer cancel()
	// Bind the query to rctx so the refreshTimeout actually interrupts the DB
	// call (modernc/sqlite honors ctx cancellation). This keeps the read path
	// non-blocking even on a slow/hung DB.
	setting, ver, err := repository.GetCustomSystemPrompt(s.db.WithContext(rctx))
	if err != nil {
		// Negative TTL: record the failure so subsequent reads serve
		// last-known-good (or zero) without re-querying the DB.
		s.mu.Lock()
		s.refreshFailureUntil = time.Now().Add(refreshFailureTTL)
		s.mu.Unlock()
		return systemSettingsSnapshot{}, err
	}
	s.publishIfNewer(setting, ver)
	// Return the cache's current snapshot, not the value we just read: if a
	// concurrent PUT committed and published a newer version between our DB
	// read and here, publishIfNewer rejected ours and the cache holds the
	// newer one. Returning the stale read to singleflight waiters would serve
	// a superseded prompt despite a completed update.
	s.mu.RLock()
	cur := systemSettingsSnapshot{Setting: s.snapshot, Version: s.version}
	s.mu.RUnlock()
	return cur, nil
}

// publishIfNewer writes the cache only when ver >= current version (monotonic).
// This defeats the "PUT A committed N+1, paused; PUT B published N+2; A then
// published N+1" rollback.
func (s *SystemSettingsService) publishIfNewer(setting settings.CustomSystemPromptSetting, ver int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	// A successful DB read means the DB is healthy — clear the failure
	// window regardless of the version-monotonicity check below.
	s.refreshFailureUntil = time.Time{}
	if s.hasSnapshot && ver < s.version {
		return
	}
	s.snapshot = setting
	s.version = ver
	s.hasSnapshot = true
	s.refreshExpiry = time.Now().Add(cacheTTL)
}

// GetCustomSystemPrompt is the authoritative read for the handler GET: it
// bypasses the cache and reads straight from the DB so the admin always sees
// authoritative state. The query is bound to the request ctx so a client
// disconnect/timeout cancels the in-flight DB call rather than running to
// completion against a dead request.
func (s *SystemSettingsService) GetCustomSystemPrompt(ctx context.Context) (settings.CustomSystemPromptSetting, int64, error) {
	return repository.GetCustomSystemPrompt(s.db.WithContext(ctx))
}

// UpdateCustomSystemPrompt validates, CAS-upserts both rows in one tx, then
// atomically publishes the committed snapshot. Returns the new snapshot +
// version so the PUT response can hand the fresh version to the caller.
func (s *SystemSettingsService) UpdateCustomSystemPrompt(ctx context.Context, expectedVersion int64, enabled bool, text string) (settings.CustomSystemPromptSetting, int64, error) {
	if utf8.RuneCountInString(text) > MaxCustomSystemPromptLen {
		return settings.CustomSystemPromptSetting{}, 0, errcode.ErrCustomSystemPromptTooLong
	}
	if enabled && text == "" {
		return settings.CustomSystemPromptSetting{}, 0, errcode.ErrCustomSystemPromptEmpty
	}
	// Bind the CAS write to the request ctx so a client disconnect/timeout
	// aborts the transaction mid-flight rather than committing after the caller
	// is gone. The cache publish below is in-process and stays unbounded.
	setting, ver, err := repository.UpdateCustomSystemPrompt(s.db.WithContext(ctx), expectedVersion, enabled, text)
	if err != nil {
		return settings.CustomSystemPromptSetting{}, 0, err
	}
	s.publishIfNewer(setting, ver)
	return setting, ver, nil
}
