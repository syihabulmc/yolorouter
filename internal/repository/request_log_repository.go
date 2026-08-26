// Package repository provides the RequestLog write path. Query/filter
// is a separate module — this file only writes rows.
package repository

import (
	"time"

	"gorm.io/gorm"
	"gorm.io/gorm/clause"

	"github.com/yolorouter/yolorouter/internal/model"
)

// CreateRequestLog inserts one gateway request audit row. The gateway always
// has a complete RequestLog to write (even on failure — status_code +
// fail_reason record what happened), so there is no sparse-update path here.
func CreateRequestLog(db *gorm.DB, log *model.RequestLog) error {
	if log.CreatedAt.IsZero() {
		log.CreatedAt = time.Now().UTC()
	}
	return db.Create(log).Error
}

// PurgeRequestLogsOlderThan deletes every request_logs row whose created_at
// is strictly before cutoff, and the matching request_log_bodies rows in
// the same transaction-shaped sequence. Returns the number of summary rows
// removed (bodies are deleted but not counted — they are a child of the
// summary, and the count operators care about is the row that owns the
// cost/usage figures).
//
// Bodies are deleted first via a subselect on the summary table so a crash
// between the two statements leaves at most orphan body rows; those are
// reaped by the next tick's same subselect, so the cleanup is eventually
// consistent without any extra machinery. The summary's own WHERE matches
// the same predicate the next tick will use, so an orphan left mid-cycle
// cannot be missed.
//
// SQLite: VACUUM runs after a successful purge so the .db file actually
// shrinks on disk (a plain DELETE leaves free pages that SQLite reuses but
// does not release back to the filesystem). VACUUM is best-effort: a
// failure is non-fatal because the next tick will re-try it once more
// rows accumulate. Postgres is skipped here because autovacuum is the
// platform's own reclaim path and a manual VACUUM would just contend
// with it.
//
// This is the only function that ever deletes audit rows; the only callers
// are the retention ticker (internal/service/requestlog/retention.go) and
// the operator's tests.
func PurgeRequestLogsOlderThan(db *gorm.DB, cutoff time.Time) (int64, error) {
	// Bodies first: they are the child of the summary. The subselect reads
	// the summary table directly (no JOIN) so the planner uses the
	// request_logs.created_at index whether it exists or not.
	if err := db.Where("request_id IN (SELECT request_id FROM request_logs WHERE created_at < ?)", cutoff).
		Delete(&model.RequestLogBody{}).Error; err != nil {
		return 0, err
	}
	res := db.Where("created_at < ?", cutoff).Delete(&model.RequestLog{})
	if err := res.Error; err != nil {
		return 0, err
	}
	// SQLite only: reclaim disk. gorm.Exec on a raw "VACUUM" statement
	// works; VACUUM has no parameters, so ?-binding is unnecessary.
	if db.Dialector.Name() != "postgres" {
		_ = db.Exec("VACUUM").Error //nolint:staticcheck // see QF1008 note elsewhere
	}
	return res.RowsAffected, nil
}

// IncrementAPIKeyBudgetSpent atomically adds micros to one key's cumulative
// spend. The gateway is the only writer. Used after a successful upstream
// response so budget exhaustion is visible to the next request's pre-check.
//
// UPDATE ... SET budget_spent_micros = budget_spent_micros + ? is a single
// statement, so concurrent gateway requests on the same key accumulate
// correctly without a read-then-write race.
func IncrementAPIKeyBudgetSpent(db *gorm.DB, apiKeyID uint, micros int64) error {
	return db.Model(&model.APIKey{}).Where("id = ?", apiKeyID).
		UpdateColumn("budget_spent_micros", gorm.Expr("budget_spent_micros + ?", micros)).Error
}

// UpsertRequestLogBody inserts or (on duplicate request_id) updates the 1:1
// body row for one gateway request. UNIQUE(request_id)
// + ON CONFLICT DO UPDATE makes finalize idempotent under retry/double-call
// and enforces true 1:1. Best-effort caller (gateway finalize): a failure is
// logged, not escalated — the request_logs billing row is already written.
//
// created_at is deliberately excluded from DoUpdates:
// it must keep recording when the row was FIRST created, not get bumped
// forward by a later conflicting write.
func UpsertRequestLogBody(db *gorm.DB, body *model.RequestLogBody) error {
	if body.CreatedAt.IsZero() {
		body.CreatedAt = time.Now().UTC()
	}
	return db.Clauses(clause.OnConflict{
		Columns: []clause.Column{{Name: "request_id"}},
		DoUpdates: clause.AssignmentColumns([]string{
			"request_headers", "request_body", "upstream_request_body",
			"response_body", "upstream_response_body",
			"stream_body_path", "stream_body_truncated",
			"compressed_request_body",
		}),
	}).Create(body).Error
}
