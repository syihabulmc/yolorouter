package requestlog

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"time"

	"go.uber.org/zap"
	"gorm.io/gorm"

	"github.com/yolorouter/yolorouter/internal/gateway/capture"
	"github.com/yolorouter/yolorouter/internal/repository"
	"github.com/yolorouter/yolorouter/pkg/logger"
)

// StartRetention launches a background goroutine that purges
// request_logs / request_log_bodies rows older than days, plus the
// orphaned stream-body capture files in bodiesDir. It warms immediately
// rather than waiting for the first tick, so a freshly started instance
// is not behind a full interval's worth of accumulated rows before its
// first cleanup — and so a restart on a backlog of stale data begins
// reaping without the operator waiting up to `interval` for a tick.
//
// days<=0 or interval<=0 is the explicit-disable signal: nothing is
// spawned, nil is returned, the operator's zero config (or the no-op
// default) is honoured without burning a goroutine. Each tick's failures
// are logged at Warn and the loop continues — the next tick re-tries
// from whatever state the prior one reached, so a transient database
// hiccup degrades to "purge ran but deleted 0 rows" rather than halting
// the schedule.
//
// The returned stop function cancels the loop and blocks until the
// goroutine has actually exited, so the caller (serve.go) can defer it
// alongside the HTTP-server shutdown. It is safe to call any number of
// times including concurrently with a purge in flight; the in-flight
// purge is allowed to finish rather than aborted, so the database never
// sees a half-finished DELETE wrapped in a forced shutdown.
//
// bodiesDir is the absolute data/bodies/ directory serve.go creates at
// boot. An empty string is allowed (e.g. an embedder that has no stream
// capture) — the function still spawns the row-purge goroutine, just
// without the filesystem-cleanup step.
func StartRetention(ctx context.Context, db *gorm.DB, bodiesDir string, days int, interval time.Duration) (stop func()) {
	if days <= 0 || interval <= 0 {
		return nil
	}

	ctx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})

	run := func() {
		defer close(done)
		// First cleanup before the first tick so a restart does not have
		// to wait a full interval to begin reaping — same rationale as
		// pricecatalog.StartRefresh warming its index immediately.
		purgeOnce(ctx, db, bodiesDir, days)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				purgeOnce(ctx, db, bodiesDir, days)
			}
		}
	}
	go run()

	var stopOnce sync.Once
	return func() {
		stopOnce.Do(cancel)
		<-done
	}
}

// purgeOnce is one cleanup pass. The DB purge and the filesystem purge
// are independent — a failure in one does not stop the other, and
// neither's error escalates to a fatal: this is a background maintenance
// task and the next tick is the natural retry boundary.
func purgeOnce(ctx context.Context, db *gorm.DB, bodiesDir string, days int) {
	cutoff := time.Now().UTC().AddDate(0, 0, -days)
	rowsDeleted, err := repository.PurgeRequestLogsOlderThan(db, cutoff)
	if err != nil {
		logger.Warn("retention: purge request logs failed",
			zap.Time("cutoff", cutoff), zap.Error(err))
	} else {
		logger.Info("retention: purged request logs",
			zap.Int64("rows_deleted", rowsDeleted),
			zap.Time("cutoff", cutoff),
			zap.Int("retention_days", days))
	}

	if bodiesDir == "" {
		return
	}
	filesDeleted, err := PurgeOrphanedStreamBodies(db, bodiesDir, cutoff)
	if err != nil {
		logger.Warn("retention: purge stream body files failed",
			zap.Time("cutoff", cutoff), zap.Error(err))
	} else if filesDeleted > 0 {
		logger.Info("retention: purged stream body files",
			zap.Int("files_deleted", filesDeleted),
			zap.Time("cutoff", cutoff))
	}
}

// PurgeOrphanedStreamBodies removes .stream capture files in bodiesDir
// whose mtime is older than cutoff AND whose request_id is not in
// request_logs. A file referenced by a live row stays — the parent
// purge cycle is responsible for deleting that row, and until the row
// is gone the file is the diagnostic evidence the operator might still
// be looking at via the request-log detail page.
//
// Cross-checking the database is the difference between deleting every
// stale file (which would destroy a stream a recently-arrived request
// is still being captured into — its row's created_at is "now", not
// "old") and only deleting files whose owner is gone. The list of live
// request_ids is small relative to the number of files on a busy
// instance, but it is also bounded by the same retention policy so a
// runaway table cannot make this O(n^2) — the query is a single indexed
// range scan on the same created_at column the row-purge already uses.
//
// Empty bodiesDir is a no-op (returns 0, nil) so a caller without
// stream capture (a unit test, an embedder that disabled it) does not
// need to special-case the directory.
func PurgeOrphanedStreamBodies(db *gorm.DB, bodiesDir string, cutoff time.Time) (int, error) {
	if bodiesDir == "" {
		return 0, nil
	}
	entries, err := os.ReadDir(bodiesDir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0, nil // directory was never created — no files, no error
		}
		return 0, err
	}

	// One query for the entire live set, in memory. request_id is the
	// primary lookup key for the stream file name, so a set membership
	// check is the right data structure.
	liveIDs, err := loadLiveRequestIDsSince(db, cutoff)
	if err != nil {
		return 0, err
	}

	deleted := 0
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		// StreamFileName is the single source of truth for the
		// naming convention (requestID + ".stream"); reach in to it
		// rather than re-implementing the suffix check, so a future
		// rename of the capture file does not silently orphan a
		// cleanup pass.
		if filepath.Ext(name) != filepath.Ext(capture.StreamFileName("x")) {
			continue
		}
		requestID := name[:len(name)-len(filepath.Ext(name))]
		if _, live := liveIDs[requestID]; live {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue // unreadable entry — leave it; stat will succeed next tick
		}
		if !info.ModTime().Before(cutoff) {
			continue // file is newer than cutoff; possibly mid-write by a fresh request
		}
		path := filepath.Join(bodiesDir, name)
		if err := os.Remove(path); err != nil {
			// One bad file must not stop the rest. The next tick will
			// re-encounter it; an operator who needs immediate cleanup
			// can delete the file by hand.
			logger.Warn("retention: remove stream body file failed",
				zap.String("path", path), zap.Error(err))
			continue
		}
		deleted++
	}
	return deleted, nil
}

// loadLiveRequestIDsSince returns the set of request_ids whose
// created_at >= cutoff. These are the rows whose stream capture file
// must not be deleted even if the file's own mtime is older than cutoff
// (a request that took longer than `days` to complete would otherwise
// see its evidence nuked mid-archive).
func loadLiveRequestIDsSince(db *gorm.DB, cutoff time.Time) (map[string]struct{}, error) {
	var ids []string
	// Pluck runs a SELECT request_id FROM request_logs WHERE created_at >= ?;
	// gorm infers the table from the column name on the *gorm.DB
	// session if we use a typed model, but going through Table() here
	// keeps the query self-describing and avoids the implicit model
	// binding that would tie this function to model.RequestLog.
	if err := db.Table("request_logs").
		Where("created_at >= ?", cutoff).
		Pluck("request_id", &ids).Error; err != nil {
		return nil, err
	}
	out := make(map[string]struct{}, len(ids))
	for _, id := range ids {
		out[id] = struct{}{}
	}
	return out, nil
}
