package repository

import (
	"testing"
	"time"

	"github.com/yolorouter/yolorouter/internal/model"
	"github.com/yolorouter/yolorouter/internal/testutil"
)

func TestUpsertRequestLogBodyInsertThenUpdate(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	row := &model.RequestLogBody{
		RequestID:   "req_upsert_1",
		RequestBody: "hello", UpstreamRequestBody: "u1",
		ResponseBody: "resp1", UpstreamResponseBody: "raw1",
	}
	if err := UpsertRequestLogBody(db, row); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	// second call with the same request_id must DO UPDATE, not duplicate
	row2 := &model.RequestLogBody{
		RequestID:   "req_upsert_1",
		RequestBody: "hello2", ResponseBody: "resp2",
	}
	if err := UpsertRequestLogBody(db, row2); err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	got, err := GetRequestLogBodyByRequestID(db, "req_upsert_1")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got == nil {
		t.Fatalf("expected a row, got nil")
	}
	if got.RequestBody != "hello2" || got.ResponseBody != "resp2" {
		t.Fatalf("upsert did not overwrite: %+v", got)
	}

	var count int64
	if err := db.Model(&model.RequestLogBody{}).Where("request_id = ?", "req_upsert_1").Count(&count).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if count != 1 {
		t.Fatalf("expected exactly 1 row for request_id, got %d", count)
	}
}

func TestGetRequestLogBodyByRequestIDNotFound(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	got, err := GetRequestLogBodyByRequestID(db, "missing")
	if err != nil {
		t.Fatalf("not-found should return (nil,nil), got err: %v", err)
	}
	if got != nil {
		t.Fatalf("not-found should return nil body, got %+v", got)
	}
}

// TestPurgeRequestLogsOlderThan drives the retention path end-to-end: insert
// 3 old + 2 new summary rows (each with a body row), call
// PurgeRequestLogsOlderThan with a cutoff between old and new, and assert the
// counts before/after. The body table is verified separately because the
// "child first, parent second" order is the contract — a regression that
// reversed the order (or that skipped the body subselect) would leave orphan
// rows the next tick would re-encounter, and the assertion on body count
// would catch it.
func TestPurgeRequestLogsOlderThan(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	now := time.Now().UTC()
	old := now.Add(-48 * time.Hour)
	fresh := now.Add(-1 * time.Hour)

	// 3 old + 2 new summary rows, each with a body row.
	for i, created := range []time.Time{old, old.Add(time.Hour), old.Add(2 * time.Hour), fresh, fresh.Add(time.Minute)} {
		id := "purge_" + string(rune('a'+i))
		summary := &model.RequestLog{
			RequestID:  id,
			StatusCode: 200,
			CreatedAt:  created,
		}
		if err := CreateRequestLog(db, summary); err != nil {
			t.Fatalf("insert summary %s: %v", id, err)
		}
		body := &model.RequestLogBody{RequestID: id, RequestBody: "body-" + id}
		if err := UpsertRequestLogBody(db, body); err != nil {
			t.Fatalf("insert body %s: %v", id, err)
		}
	}

	// Cutoff sits between the 3 old rows and the 2 new ones (1h ago is
	// after the old cluster at 48h/47h/46h ago and before the new
	// cluster at 1h/59m ago).
	cutoff := now.Add(-2 * time.Hour)
	rowsDeleted, err := PurgeRequestLogsOlderThan(db, cutoff)
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if rowsDeleted != 3 {
		t.Fatalf("expected 3 summary rows deleted, got %d", rowsDeleted)
	}

	var summaryLeft int64
	if err := db.Model(&model.RequestLog{}).Count(&summaryLeft).Error; err != nil {
		t.Fatalf("count summary: %v", err)
	}
	if summaryLeft != 2 {
		t.Fatalf("expected 2 summary rows remaining, got %d", summaryLeft)
	}

	var bodyLeft int64
	if err := db.Model(&model.RequestLogBody{}).Count(&bodyLeft).Error; err != nil {
		t.Fatalf("count body: %v", err)
	}
	if bodyLeft != 2 {
		t.Fatalf("expected 2 body rows remaining, got %d", bodyLeft)
	}
}

// TestPurgeRequestLogsOlderThanNoRowsOlder exercises the empty-purge path: a
// database whose rows are all newer than the cutoff must report 0 deletions
// rather than erroring. Guards the WHERE-clause predicate: a regression that
// accidentally inverted the comparison (">" instead of "<") would delete
// everything, and this test would catch it via the surviving-count assertion.
func TestPurgeRequestLogsOlderThanNoRowsOlder(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	fresh := time.Now().UTC().Add(-1 * time.Hour)
	if err := CreateRequestLog(db, &model.RequestLog{RequestID: "fresh", StatusCode: 200, CreatedAt: fresh}); err != nil {
		t.Fatalf("insert: %v", err)
	}
	rowsDeleted, err := PurgeRequestLogsOlderThan(db, time.Now().UTC().Add(-48*time.Hour))
	if err != nil {
		t.Fatalf("purge: %v", err)
	}
	if rowsDeleted != 0 {
		t.Fatalf("expected 0 rows deleted, got %d", rowsDeleted)
	}
	var left int64
	if err := db.Model(&model.RequestLog{}).Count(&left).Error; err != nil {
		t.Fatalf("count: %v", err)
	}
	if left != 1 {
		t.Fatalf("expected the fresh row to survive, got count %d", left)
	}
}
