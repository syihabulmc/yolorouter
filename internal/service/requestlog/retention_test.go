package requestlog

import (
	"context"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/yolorouter/yolorouter/internal/gateway/capture"
	"github.com/yolorouter/yolorouter/internal/model"
	"github.com/yolorouter/yolorouter/internal/repository"
	"github.com/yolorouter/yolorouter/internal/testutil"
)

// TestStartRetentionDisabledReturnsNil exercises the explicit-disable contract
// for StartRetention: a zero day count (or zero interval) must return a nil
// stop function and spawn no goroutine. Without this guard, a config that
// intended "no retention" would still tick once a day forever, executing an
// empty DELETE and writing a "retention: purged 0 rows" line to the log
// every 24h — wasted work the operator's "0" was supposed to prevent.
func TestStartRetentionDisabledReturnsNil(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if stop := StartRetention(ctx, db, "", 0, time.Hour); stop != nil {
		t.Fatalf("days=0: expected nil stop, got non-nil")
	}
	if stop := StartRetention(ctx, db, "", 30, 0); stop != nil {
		t.Fatalf("interval=0: expected nil stop, got non-nil")
	}
}

// TestStartRetentionRunsOnce is the positive-path smoke test: with a tiny
// interval and a database holding one row older than `days`, StartRetention
// must delete it before the first tick fires (the warm-immediate behaviour
// StartRetention documents). The assertion is "row gone after Stop() returns"
// rather than "row gone at time T" — a 5ms interval would otherwise be
// fragile to a slow CI box, while the stop-then-check sequence is bounded
// only by Stop's own <done> sync.
func TestStartRetentionRunsOnce(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	old := time.Now().UTC().Add(-48 * time.Hour)
	if err := repository.CreateRequestLog(db, &model.RequestLog{
		RequestID:  "ret_old",
		StatusCode: 200,
		CreatedAt:  old,
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stop := StartRetention(ctx, db, "", 1, 24*time.Hour)
	if stop == nil {
		t.Fatalf("expected non-nil stop with days=1 interval=24h")
	}
	stop() // cancels + waits for the goroutine to exit

	var left int64
	if err := db.Model(&model.RequestLog{}).Count(&left).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if left != 0 {
		t.Fatalf("expected the 48h-old row to be purged, %d rows remain", left)
	}
}

// TestStartRetentionKeepsFreshRows is the inverse guarantee: a row newer
// than the cutoff must NOT be deleted, even though the ticker is running.
// Without it, a "1 day" retention config would happily drop rows the
// operator expects to keep — the same regression mode as an inverted
// WHERE-clause comparison, caught at the service level instead of the
// repository level.
func TestStartRetentionKeepsFreshRows(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	fresh := time.Now().UTC().Add(-1 * time.Hour)
	if err := repository.CreateRequestLog(db, &model.RequestLog{
		RequestID:  "ret_fresh",
		StatusCode: 200,
		CreatedAt:  fresh,
	}); err != nil {
		t.Fatalf("insert: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	stop := StartRetention(ctx, db, "", 1, 24*time.Hour)
	if stop == nil {
		t.Fatalf("expected non-nil stop")
	}
	stop()

	var left int64
	if err := db.Model(&model.RequestLog{}).Count(&left).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if left != 1 {
		t.Fatalf("expected the 1h-old row to survive, %d rows remain", left)
	}
}

// TestPurgeOrphanedStreamBodiesRemovesOldFiles exercises the filesystem half
// of the retention path: a .stream file in bodiesDir whose mtime is older
// than cutoff AND whose request_id has no live row must be removed, while a
// file whose request_id IS live must be kept. The dual cross-check (mtime +
// DB membership) is the contract; without it a recently-arrived request
// whose stream is still being captured could lose its file mid-write.
func TestPurgeOrphanedStreamBodiesRemovesOldFiles(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	dir := t.TempDir()
	cutoff := time.Now().UTC().Add(-24 * time.Hour)

	// Orphan: file exists, mtime old, no row in DB.
	orphanID := "orphan_req"
	orphanPath := filepath.Join(dir, capture.StreamFileName(orphanID))
	if err := os.WriteFile(orphanPath, []byte("old stream"), 0o600); err != nil {
		t.Fatalf("write orphan: %v", err)
	}
	mustSetMtime(t, orphanPath, cutoff.Add(-time.Hour))

	// Live: file exists, mtime old, but the DB has a row newer than cutoff.
	liveID := "live_req"
	livePath := filepath.Join(dir, capture.StreamFileName(liveID))
	if err := os.WriteFile(livePath, []byte("still relevant"), 0o600); err != nil {
		t.Fatalf("write live: %v", err)
	}
	mustSetMtime(t, livePath, cutoff.Add(-time.Hour))
	if err := repository.CreateRequestLog(db, &model.RequestLog{
		RequestID:  liveID,
		StatusCode: 200,
		CreatedAt:  time.Now().UTC().Add(-1 * time.Hour), // newer than cutoff
	}); err != nil {
		t.Fatalf("insert live row: %v", err)
	}

	deleted, err := PurgeOrphanedStreamBodies(db, dir, cutoff)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if deleted != 1 {
		t.Fatalf("expected 1 file deleted, got %d", deleted)
	}
	if _, err := os.Stat(orphanPath); !os.IsNotExist(err) {
		t.Fatalf("orphan file should have been removed: stat err = %v", err)
	}
	if _, err := os.Stat(livePath); err != nil {
		t.Fatalf("live file must survive: stat err = %v", err)
	}
}

// TestPurgeOrphanedStreamBodiesEmptyDir is the no-op path: a missing or
// empty bodiesDir must return (0, nil) so a unit test or an embedder
// without stream capture does not need to special-case the directory.
func TestPurgeOrphanedStreamBodiesEmptyDir(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	deleted, err := PurgeOrphanedStreamBodies(db, "", time.Now().UTC())
	if err != nil {
		t.Fatalf("empty bodiesDir: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("expected 0 deletions for empty bodiesDir, got %d", deleted)
	}
	deleted, err = PurgeOrphanedStreamBodies(db, filepath.Join(t.TempDir(), "does-not-exist"), time.Now().UTC())
	if err != nil {
		t.Fatalf("missing bodiesDir: %v", err)
	}
	if deleted != 0 {
		t.Fatalf("expected 0 deletions for missing bodiesDir, got %d", deleted)
	}
}

// mustSetMtime sets the file's modification time, fataling the test on
// failure. The retention path uses mtime as the age signal (the .stream
// filename encodes request_id but not creation time), so a deterministic
// mtime is the only way to assert "older than cutoff" in a unit test.
func mustSetMtime(t *testing.T, path string, t0 time.Time) {
	t.Helper()
	if err := os.Chtimes(path, t0, t0); err != nil {
		t.Fatalf("chtimes %s: %v", path, err)
	}
}