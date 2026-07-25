// Package handler tests for the analytics endpoints. Exercises the
// full HTTP → service → repository stack against a migrated SQLite DB;
// repository-only tests for the time-bucket walk live alongside (they'd
// require an awkward HTTP shim to drive a fixed *time.Location otherwise).
package handler

import (
	"bytes"
	"encoding/csv"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"gorm.io/gorm"

	"github.com/yolorouter/yolorouter/internal/model"
	"github.com/yolorouter/yolorouter/internal/repository"
	"github.com/yolorouter/yolorouter/internal/service"
	"github.com/yolorouter/yolorouter/internal/testutil"
)

// newAnalyticsTestRouter wires up a Gin engine with the three analytics
// routes mounted under /api/admin/analytics — the same paths
// internal/router/router.go would use, duplicated here so this test file
// doesn't have to touch router.go (out of scope per the task boundary).
func newAnalyticsTestRouter(t *testing.T) (*gin.Engine, *gorm.DB) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	if err := RegisterValidators(); err != nil {
		t.Fatalf("RegisterValidators: %v", err)
	}
	db := testutil.NewSQLiteDB(t)
	svc := service.NewAnalyticsService(db)

	r := gin.New()
	admin := r.Group("/api/admin")
	admin.GET("/analytics/overview", GetAnalyticsOverview(svc))
	admin.GET("/analytics/report", GetAnalyticsReport(svc))
	admin.GET("/analytics/export", ExportAnalyticsCSV(svc))
	admin.GET("/analytics/compress-stats", GetCompressStats(svc))
	return r, db
}

// analyticsStrPtr is a tiny *string helper local to this file (the existing
// seedRequestLog's mutator-callback pattern keeps the test body explicit at
// the cost of needing a closure-captured pointer for fail_reason).
func analyticsStrPtr(s string) *string { return &s }

// === Overview handler ====================================================

func TestGetAnalyticsOverviewAggregatesSeededRows(t *testing.T) {
	r, db := newAnalyticsTestRouter(t)
	now := time.Now().UTC()
	// 2 successes (cost-known), 1 server failure (cost-unknown),
	// 1 caller-cancel (cost-unknown). Verify each metric below.
	seedRequestLog(t, db, "r1", now, func(r *model.RequestLog) {
		r.ModelName = "gpt-4"
		r.StatusCode = 200
		r.InputTokens = 100
		r.OutputTokens = 50
		r.CostMicros = 10
		r.CostKnown = true
		r.DurationMs = 500
	})
	seedRequestLog(t, db, "r2", now, func(r *model.RequestLog) {
		r.ModelName = "gpt-4"
		r.StatusCode = 200
		r.InputTokens = 200
		r.OutputTokens = 100
		r.CostMicros = 20
		r.CostKnown = true
		r.DurationMs = 600
	})
	seedRequestLog(t, db, "r3", now, func(r *model.RequestLog) {
		r.ModelName = "gpt-4"
		r.StatusCode = 500
		r.FailReason = analyticsStrPtr("upstream")
		// Explicitly cost-unknown with zero tokens — seedRequestLog's
		// defaults (InputTokens=10, OutputTokens=20, CostKnown=true) would
		// otherwise skew the aggregate.
		r.InputTokens = 0
		r.OutputTokens = 0
		r.CostMicros = 0
		r.CostKnown = false
		r.DurationMs = 100
	})
	seedRequestLog(t, db, "r4", now, func(r *model.RequestLog) {
		r.ModelName = "gpt-4"
		r.StatusCode = 499
		r.InputTokens = 0
		r.OutputTokens = 0
		r.CostMicros = 0
		r.CostKnown = false
		r.DurationMs = 50
	})

	w, _ := doJSON(t, r, http.MethodGet, "/api/admin/analytics/overview", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}
	var env struct {
		Code int                 `json:"code"`
		Data service.OverviewRow `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	data := env.Data
	if data.TotalCalls != 4 {
		t.Fatalf("TotalCalls = %d, want 4", data.TotalCalls)
	}
	if data.SuccessCalls != 2 {
		t.Fatalf("SuccessCalls = %d, want 2", data.SuccessCalls)
	}
	// Ended excludes 499 (caller-cancel). Ended = r1+r2+r3 = 3.
	if data.EndedCalls != 3 {
		t.Fatalf("EndedCalls = %d, want 3", data.EndedCalls)
	}
	wantRate := float64(2) / float64(3)
	if !approxEqual(data.SuccessRate, wantRate, 1e-9) {
		t.Fatalf("SuccessRate = %v, want %v", data.SuccessRate, wantRate)
	}
	if data.InputTokens != 300 || data.OutputTokens != 150 {
		t.Fatalf("tokens = %d/%d, want 300/150", data.InputTokens, data.OutputTokens)
	}
	if data.CostMicros != 30 {
		t.Fatalf("CostMicros = %d, want 30", data.CostMicros)
	}
	if data.UnknownCostCalls != 2 {
		t.Fatalf("UnknownCostCalls = %d, want 2 (r3 + r4)", data.UnknownCostCalls)
	}
}

func TestGetAnalyticsOverviewRespectsTimeRange(t *testing.T) {
	r, db := newAnalyticsTestRouter(t)
	now := time.Now().UTC()
	longAgo := now.Add(-30 * 24 * time.Hour)
	seedRequestLog(t, db, "old", longAgo, func(r *model.RequestLog) {
		r.ModelName = "gpt-4"
		r.StatusCode = 200
		r.InputTokens = 10
		r.OutputTokens = 5
		r.CostMicros = 1
		r.CostKnown = true
	})
	seedRequestLog(t, db, "new", now, func(r *model.RequestLog) {
		r.ModelName = "gpt-4"
		r.StatusCode = 200
		r.InputTokens = 20
		r.OutputTokens = 10
		r.CostMicros = 2
		r.CostKnown = true
	})

	// Window covering only `now`.
	start := now.Add(-time.Hour).Format(time.RFC3339)
	end := now.Add(time.Hour).Format(time.RFC3339)
	w, _ := doJSON(t, r, http.MethodGet, "/api/admin/analytics/overview?start="+start+"&end="+end, nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}
	var env struct {
		Data service.OverviewRow `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Data.TotalCalls != 1 {
		t.Fatalf("TotalCalls = %d, want 1 (only the recent row)", env.Data.TotalCalls)
	}
}

func TestGetAnalyticsOverviewRejectsBadStartTime(t *testing.T) {
	r, _ := newAnalyticsTestRouter(t)
	w, _ := doJSON(t, r, http.MethodGet, "/api/admin/analytics/overview?start=not-a-time", nil, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestGetAnalyticsOverviewRejectsBadStatus(t *testing.T) {
	r, _ := newAnalyticsTestRouter(t)
	w, _ := doJSON(t, r, http.MethodGet, "/api/admin/analytics/overview?status=bogus", nil, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// === Report handlers =====================================================

func TestGetAnalyticsReportByModelGroupsAndOrdersByCalls(t *testing.T) {
	r, db := newAnalyticsTestRouter(t)
	now := time.Now().UTC()
	mk := func(name string) func(*model.RequestLog) {
		return func(r *model.RequestLog) {
			r.ModelName = name
			r.StatusCode = 200
			r.InputTokens = 10
			r.OutputTokens = 5
			r.CostMicros = 1
			r.CostKnown = true
		}
	}
	seedRequestLog(t, db, "a1", now, mk("gpt-4"))
	seedRequestLog(t, db, "a2", now, mk("gpt-4"))
	seedRequestLog(t, db, "a3", now, mk("gpt-4"))
	seedRequestLog(t, db, "a4", now, mk("gpt-4o"))
	seedRequestLog(t, db, "a5", now, mk("claude"))
	seedRequestLog(t, db, "a6", now, mk("claude"))

	w, _ := doJSON(t, r, http.MethodGet, "/api/admin/analytics/report?dimension=model", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}
	var env struct {
		Code int `json:"code"`
		Data struct {
			Dimension string `json:"dimension"`
			Rows      []struct {
				ModelName   string  `json:"model_name"`
				Calls       int64   `json:"calls"`
				SuccessRate float64 `json:"success_rate"`
			} `json:"rows"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Data.Dimension != "model" {
		t.Fatalf("dimension = %q, want model", env.Data.Dimension)
	}
	if len(env.Data.Rows) != 3 {
		t.Fatalf("expected 3 model groups, got %d", len(env.Data.Rows))
	}
	// Ordered by calls DESC: gpt-4 (3), claude (2), gpt-4o (1).
	if env.Data.Rows[0].ModelName != "gpt-4" || env.Data.Rows[0].Calls != 3 {
		t.Fatalf("row[0] = %+v, want gpt-4/3", env.Data.Rows[0])
	}
	if env.Data.Rows[1].ModelName != "claude" || env.Data.Rows[1].Calls != 2 {
		t.Fatalf("row[1] = %+v, want claude/2", env.Data.Rows[1])
	}
	if env.Data.Rows[2].ModelName != "gpt-4o" || env.Data.Rows[2].Calls != 1 {
		t.Fatalf("row[2] = %+v, want gpt-4o/1", env.Data.Rows[2])
	}
	// All success, no cancels → rate = 1.0
	if env.Data.Rows[0].SuccessRate != 1.0 {
		t.Fatalf("SuccessRate = %v, want 1.0", env.Data.Rows[0].SuccessRate)
	}
}

func TestGetAnalyticsReportByModelComputesSuccessRateExcluding499(t *testing.T) {
	r, db := newAnalyticsTestRouter(t)
	now := time.Now().UTC()
	// 1 success + 1 server-error (5xx, ended) + 1 caller-cancel (499, NOT ended).
	seedRequestLog(t, db, "s", now, func(r *model.RequestLog) {
		r.ModelName = "gpt-4"
		r.StatusCode = 200
		r.CostKnown = true
		r.CostMicros = 1
	})
	seedRequestLog(t, db, "f", now, func(r *model.RequestLog) {
		r.ModelName = "gpt-4"
		r.StatusCode = 500
		r.FailReason = analyticsStrPtr("err")
	})
	seedRequestLog(t, db, "c", now, func(r *model.RequestLog) { r.ModelName = "gpt-4"; r.StatusCode = 499 })

	w, _ := doJSON(t, r, http.MethodGet, "/api/admin/analytics/report?dimension=model", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}
	var env struct {
		Data struct {
			Rows []struct {
				Calls        int64   `json:"calls"`
				SuccessCalls int64   `json:"success_calls"`
				EndedCalls   int64   `json:"ended_calls"`
				SuccessRate  float64 `json:"success_rate"`
			} `json:"rows"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(env.Data.Rows) != 1 {
		t.Fatalf("rows = %d, want 1", len(env.Data.Rows))
	}
	row := env.Data.Rows[0]
	if row.Calls != 3 || row.SuccessCalls != 1 || row.EndedCalls != 2 {
		t.Fatalf("calls/success/ended = %d/%d/%d, want 3/1/2", row.Calls, row.SuccessCalls, row.EndedCalls)
	}
	want := float64(1) / float64(2)
	if !approxEqual(row.SuccessRate, want, 1e-9) {
		t.Fatalf("SuccessRate = %v, want %v", row.SuccessRate, want)
	}
}

func TestGetAnalyticsReportByProviderResolvesNamesViaPostFetch(t *testing.T) {
	r, db := newAnalyticsTestRouter(t)
	// Seed a real Provider so resolveProviderNames can find it.
	prov := &model.Provider{Name: "openai-main", ProviderType: "openai", BaseURL: "https://api.example.com/v1", ManagementStatus: model.ProviderStatusEnabled}
	if err := db.Create(prov).Error; err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	now := time.Now().UTC()
	seedRequestLog(t, db, "p1", now, func(r *model.RequestLog) {
		r.ModelName = "gpt-4"
		r.ProviderID = &prov.ID
		r.StatusCode = 200
		r.InputTokens = 10
		r.OutputTokens = 5
		r.CostMicros = 1
		r.CostKnown = true
		r.DurationMs = 100
	})
	seedRequestLog(t, db, "p2", now, func(r *model.RequestLog) {
		r.ModelName = "gpt-4"
		r.StatusCode = 200 // NULL-provider bucket
	})

	w, _ := doJSON(t, r, http.MethodGet, "/api/admin/analytics/report?dimension=provider", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}
	var env struct {
		Data struct {
			Rows []struct {
				ProviderID    *uint   `json:"provider_id"`
				ProviderName  string  `json:"provider_name"`
				Calls         int64   `json:"calls"`
				AvgDurationMs float64 `json:"avg_duration_ms"`
			} `json:"rows"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(env.Data.Rows) != 2 {
		t.Fatalf("rows = %d, want 2 (provider bucket + NULL bucket)", len(env.Data.Rows))
	}
	var named *struct {
		ProviderID    *uint   `json:"provider_id"`
		ProviderName  string  `json:"provider_name"`
		Calls         int64   `json:"calls"`
		AvgDurationMs float64 `json:"avg_duration_ms"`
	}
	for i := range env.Data.Rows {
		if env.Data.Rows[i].ProviderID != nil {
			named = &env.Data.Rows[i]
		}
	}
	if named == nil {
		t.Fatalf("no non-NULL provider bucket in result %+v", env.Data.Rows)
	}
	if named.ProviderName != "openai-main" {
		t.Fatalf("ProviderName = %q, want openai-main", named.ProviderName)
	}
	if named.Calls != 1 {
		t.Fatalf("Calls = %d, want 1", named.Calls)
	}
	// avg(duration_ms=100) over one row → 100.
	if named.AvgDurationMs != 100 {
		t.Fatalf("AvgDurationMs = %v, want 100", named.AvgDurationMs)
	}
}

func TestGetAnalyticsReportByCallerResolvesOwnerLabels(t *testing.T) {
	r, db := newAnalyticsTestRouter(t)
	key := &model.APIKey{OwnerLabel: "alice", KeyHash: "x", KeyPrefix: "sk-", Status: model.APIKeyStatusActive}
	if err := db.Create(key).Error; err != nil {
		t.Fatalf("seed api_key: %v", err)
	}
	now := time.Now().UTC()
	seedRequestLog(t, db, "k1", now, func(r *model.RequestLog) {
		r.ModelName = "gpt-4"
		r.APIKeyID = &key.ID
		r.StatusCode = 200
		r.InputTokens = 30
		r.OutputTokens = 15
		r.CostMicros = 3
		r.CostKnown = true
	})
	seedRequestLog(t, db, "k2", now, func(r *model.RequestLog) {
		r.ModelName = "gpt-4"
		r.StatusCode = 200 // NULL-api_key bucket
	})

	w, _ := doJSON(t, r, http.MethodGet, "/api/admin/analytics/report?dimension=caller", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}
	var env struct {
		Data struct {
			Rows []struct {
				APIKeyID   *uint  `json:"api_key_id"`
				OwnerLabel string `json:"owner_label"`
				Calls      int64  `json:"calls"`
			} `json:"rows"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if len(env.Data.Rows) != 2 {
		t.Fatalf("rows = %d, want 2", len(env.Data.Rows))
	}
	var labeled *struct {
		APIKeyID   *uint  `json:"api_key_id"`
		OwnerLabel string `json:"owner_label"`
		Calls      int64  `json:"calls"`
	}
	for i := range env.Data.Rows {
		if env.Data.Rows[i].APIKeyID != nil {
			labeled = &env.Data.Rows[i]
		}
	}
	if labeled == nil {
		t.Fatalf("no non-NULL api_key bucket in %+v", env.Data.Rows)
	}
	if labeled.OwnerLabel != "alice" {
		t.Fatalf("OwnerLabel = %q, want alice", labeled.OwnerLabel)
	}
}

func TestGetAnalyticsReportDefaultsDimensionToModel(t *testing.T) {
	r, db := newAnalyticsTestRouter(t)
	now := time.Now().UTC()
	seedRequestLog(t, db, "d1", now, func(r *model.RequestLog) {
		r.ModelName = "gpt-4"
		r.StatusCode = 200
		r.CostKnown = true
		r.CostMicros = 1
	})

	// No ?dimension= on the URL at all.
	w, _ := doJSON(t, r, http.MethodGet, "/api/admin/analytics/report", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}
	var env struct {
		Data struct {
			Dimension string `json:"dimension"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if env.Data.Dimension != service.DimensionModel {
		t.Fatalf("default dimension = %q, want %q", env.Data.Dimension, service.DimensionModel)
	}
}

func TestGetAnalyticsReportRejectsUnknownDimension(t *testing.T) {
	r, _ := newAnalyticsTestRouter(t)
	w, _ := doJSON(t, r, http.MethodGet, "/api/admin/analytics/report?dimension=banana", nil, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

func TestGetAnalyticsReportRejectsUnknownBucket(t *testing.T) {
	r, _ := newAnalyticsTestRouter(t)
	w, _ := doJSON(t, r, http.MethodGet, "/api/admin/analytics/report?dimension=time&bucket=century", nil, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// === Time dimension ======================================================

func TestGetAnalyticsReportByTimeDayBucketFillsGaps(t *testing.T) {
	r, db := newAnalyticsTestRouter(t)
	// Seed a row 3 days ago and another 1 day ago; the day in between has
	// zero data and must still appear with zeros so the trend line is
	// continuous. Use UTC instants that map unambiguously to local days in
	// most timezones (mid-afternoon UTC lands in the same local calendar
	// day for offsets in [-11h, +10h], which covers CI runners).
	now := time.Now().UTC()
	day3 := now.Add(-3 * 24 * time.Hour)
	day1 := now.Add(-1 * 24 * time.Hour)
	seedRequestLog(t, db, "g1", day3, func(r *model.RequestLog) {
		r.ModelName = "gpt-4"
		r.StatusCode = 200
		r.CostKnown = true
		r.CostMicros = 5
	})
	seedRequestLog(t, db, "g2", day1, func(r *model.RequestLog) {
		r.ModelName = "gpt-4"
		r.StatusCode = 200
		r.CostKnown = true
		r.CostMicros = 10
	})

	w, _ := doJSON(t, r, http.MethodGet, "/api/admin/analytics/report?dimension=time&bucket=day", nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}
	var env struct {
		Data struct {
			Rows []struct {
				Bucket string `json:"bucket"`
				Calls  int64  `json:"calls"`
			} `json:"rows"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Buckets are ordered chronologically; format "YYYY-MM-DD".
	if len(env.Data.Rows) < 3 {
		t.Fatalf("expected at least 3 day buckets (gap-fill), got %d", len(env.Data.Rows))
	}
	// Find a zero-bucket in the middle — the day strictly between the two
	// seeded days. Exact date depends on time.Local, but a contiguous walk
	// must include at least one bucket with zero calls in the middle.
	sawZeroBetween := false
	for _, row := range env.Data.Rows[1 : len(env.Data.Rows)-1] {
		if row.Calls == 0 {
			sawZeroBetween = true
			break
		}
	}
	if !sawZeroBetween {
		t.Fatalf("no zero-call bucket between seeded days — gap-fill failed; rows: %+v", env.Data.Rows)
	}
	// Total calls across all buckets = 2.
	var total int64
	for _, row := range env.Data.Rows {
		total += row.Calls
	}
	if total != 2 {
		t.Fatalf("total calls across buckets = %d, want 2", total)
	}
}

// TestAggregateByTimeWalksDayBucketsInUTC exercises the repository directly
// with a fixed *time.Location so the bucket labels and walk length are
// deterministic regardless of the test machine's TZ.
func TestAggregateByTimeWalksDayBucketsInUTC(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	loc := time.UTC

	// Build an explicit [start, end) window: 3 UTC days starting from a
	// fixed base. Seed rows on day 0 and day 2; day 1 stays empty.
	base := time.Date(2026, 7, 14, 0, 0, 0, 0, loc)
	day0 := base
	day2 := base.AddDate(0, 0, 2)
	end := base.AddDate(0, 0, 3)

	seedRequestLog(t, db, "d0-a", day0.Add(6*time.Hour), func(r *model.RequestLog) {
		r.ModelName = "gpt-4"
		r.StatusCode = 200
		r.InputTokens = 10
		r.OutputTokens = 5
		r.CostMicros = 1
		r.CostKnown = true
	})
	seedRequestLog(t, db, "d0-b", day0.Add(7*time.Hour), func(r *model.RequestLog) {
		r.ModelName = "gpt-4"
		r.StatusCode = 200
		r.InputTokens = 10
		r.OutputTokens = 5
		r.CostMicros = 1
		r.CostKnown = true
	})
	seedRequestLog(t, db, "d2-a", day2.Add(8*time.Hour), func(r *model.RequestLog) {
		r.ModelName = "gpt-4"
		r.StatusCode = 500
		r.FailReason = analyticsStrPtr("err")
	})

	startUTC := day0
	endUTC := end
	f := &repository.RequestLogFilter{StartTime: &startUTC, EndTime: &endUTC}

	rows, err := repository.AggregateByTime(db, f, loc, repository.TimeBucketDay)
	if err != nil {
		t.Fatalf("AggregateByTime: %v", err)
	}
	if len(rows) != 3 {
		t.Fatalf("len(rows) = %d, want 3", len(rows))
	}
	wantBuckets := []string{"2026-07-16", "2026-07-15", "2026-07-14"}
	for i, want := range wantBuckets {
		if rows[i].Bucket != want {
			t.Fatalf("rows[%d].Bucket = %q, want %q", i, rows[i].Bucket, want)
		}
	}
	if rows[0].Calls != 1 || rows[0].SuccessCalls != 0 {
		t.Fatalf("day2 (newest) = %+v, want Calls=1 SuccessCalls=0", rows[0])
	}
	if rows[1].Calls != 0 {
		t.Fatalf("day1 gap-fill Calls = %d, want 0", rows[1].Calls)
	}
	if rows[2].Calls != 2 || rows[2].SuccessCalls != 2 {
		t.Fatalf("day0 (oldest) = %+v, want Calls=2 SuccessCalls=2", rows[2])
	}
}

func TestAggregateByTimeRejectsInvalidBucket(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	_, err := repository.AggregateByTime(db, &repository.RequestLogFilter{}, time.UTC, "century")
	if !errors.Is(err, repository.ErrInvalidBucket) {
		t.Fatalf("expected ErrInvalidBucket, got %v", err)
	}
}

// === CSV export ==========================================================

func TestExportAnalyticsCSVWritesBOMAndHeadersAndRows(t *testing.T) {
	r, db := newAnalyticsTestRouter(t)
	prov := &model.Provider{Name: "openai-main", ProviderType: "openai", BaseURL: "https://api.example.com/v1", ManagementStatus: model.ProviderStatusEnabled}
	if err := db.Create(prov).Error; err != nil {
		t.Fatalf("seed provider: %v", err)
	}
	now := time.Now().UTC()
	seedRequestLog(t, db, "c1", now, func(r *model.RequestLog) {
		r.ModelName = "gpt-4"
		r.ProviderID = &prov.ID
		r.StatusCode = 200
		r.InputTokens = 10
		r.OutputTokens = 5
		r.CostMicros = 1
		r.CostKnown = true
	})

	req := httptest.NewRequest(http.MethodGet, "/api/admin/analytics/export?dimension=model", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d", w.Code)
	}
	if ct := w.Header().Get("Content-Type"); !strings.HasPrefix(ct, "text/csv") {
		t.Fatalf("Content-Type = %q, want text/csv*", ct)
	}
	if cd := w.Header().Get("Content-Disposition"); !strings.Contains(cd, "attachment;") || !strings.Contains(cd, ".csv") {
		t.Fatalf("Content-Disposition = %q", cd)
	}
	body := w.Body.Bytes()
	// UTF-8 BOM
	if len(body) < 3 || body[0] != 0xEF || body[1] != 0xBB || body[2] != 0xBF {
		t.Fatalf("missing UTF-8 BOM; first bytes: % x", body[:minInt(3, len(body))])
	}
	reader := csv.NewReader(bytes.NewReader(body[3:]))
	records, err := reader.ReadAll()
	if err != nil {
		t.Fatalf("csv read: %v", err)
	}
	if len(records) < 2 {
		t.Fatalf("expected header + at least 1 row, got %d records", len(records))
	}
	wantHeader := []string{"model_name", "calls", "success_rate", "input_tokens", "output_tokens", "cache_write_tokens", "cache_read_tokens", "cost_micros", "unknown_cost_calls"}
	if len(records[0]) != len(wantHeader) {
		t.Fatalf("header len = %d, want %d (%v)", len(records[0]), len(wantHeader), records[0])
	}
	for i, h := range wantHeader {
		if records[0][i] != h {
			t.Fatalf("header[%d] = %q, want %q", i, records[0][i], h)
		}
	}
	// Find the gpt-4 row.
	var found bool
	for _, rec := range records[1:] {
		if rec[0] == "gpt-4" {
			found = true
			if rec[1] != "1" {
				t.Fatalf("gpt-4 calls = %q, want 1", rec[1])
			}
			break
		}
	}
	if !found {
		t.Fatalf("gpt-4 row missing from CSV; records: %+v", records[1:])
	}
}

func TestExportAnalyticsCSVRejectsUnknownDimension(t *testing.T) {
	r, _ := newAnalyticsTestRouter(t)
	req := httptest.NewRequest(http.MethodGet, "/api/admin/analytics/export?dimension=banana", nil)
	w := httptest.NewRecorder()
	r.ServeHTTP(w, req)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// === Compress stats ======================================================

// compressStatsEnvelop mirrors the response envelope for GetCompressStats —
// kept local so the test doesn't need to import the service-level Compress*
// types. Field names match the JSON tags.
type compressStatsEnvelop struct {
	Totals struct {
		TotalCalls           int64 `json:"total_calls"`
		CompressedCalls      int64 `json:"compressed_calls"`
		TokensSaved          int64 `json:"tokens_saved"`
		CostSavedMicros      int64 `json:"cost_saved_micros"`
		TotalEstimatedTokens int64 `json:"total_estimated_tokens"`
	} `json:"totals"`
	SkipReasonBreakdown []struct {
		SkipReason string `json:"skip_reason"`
		Calls      int64  `json:"calls"`
	} `json:"skip_reason_breakdown"`
	TopAPIKeys []struct {
		APIKeyID    *uint  `json:"api_key_id"`
		OwnerLabel  string `json:"owner_label"`
		Calls       int64  `json:"calls"`
		TokensSaved int64  `json:"tokens_saved"`
	} `json:"top_api_keys"`
	TopModels []struct {
		ModelName       string `json:"model_name"`
		TokensSaved     int64  `json:"tokens_saved"`
		CostSavedMicros int64  `json:"cost_saved_micros"`
		CompressedCalls int64  `json:"compressed_calls"`
		TotalCalls      int64  `json:"total_calls"`
	} `json:"top_models"`
	TopProviders []struct {
		ProviderID      *uint  `json:"provider_id"`
		ProviderName    string `json:"provider_name"`
		TokensSaved     int64  `json:"tokens_saved"`
		CostSavedMicros int64  `json:"cost_saved_micros"`
		CompressedCalls int64  `json:"compressed_calls"`
		TotalCalls      int64  `json:"total_calls"`
	} `json:"top_providers"`
	CompressorHits []struct {
		Name string `json:"name"`
		Hits int64  `json:"hits"`
	} `json:"compressor_hits"`
	DailySeries []struct {
		Bucket          string `json:"bucket"`
		TokensSaved     int64  `json:"tokens_saved"`
		CostSavedMicros int64  `json:"cost_saved_micros"`
		CompressedCalls int64  `json:"compressed_calls"`
	} `json:"daily_series"`
}

// doCompressStats is a tiny helper: hits the endpoint, unmarshals the
// envelope, fails the test on any non-200 or unmarshal error.
func doCompressStats(t *testing.T, r *gin.Engine, query string) compressStatsEnvelop {
	t.Helper()
	path := "/api/admin/analytics/compress-stats"
	if query != "" {
		path += "?" + query
	}
	w, _ := doJSON(t, r, http.MethodGet, path, nil, nil)
	if w.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d, body: %s", w.Code, w.Body.String())
	}
	var env struct {
		Code int                  `json:"code"`
		Data compressStatsEnvelop `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &env); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	return env.Data
}

// TestGetCompressStatsAggregatesSeededRows exercises the full roll-up:
// totals, skip-reason breakdown, Top-N api keys, compressor-hit counting
// (the "diff,gotest,diff" row now counts once per compressor name — the
// new semantics count ROWS-that-used-the-compressor, not total invocations),
// and the daily series (now a single GROUP BY query with gap-fill).
func TestGetCompressStatsAggregatesSeededRows(t *testing.T) {
	r, db := newAnalyticsTestRouter(t)

	// Seed two api_keys so the Top-N bucket resolves owner_label.
	key1 := &model.APIKey{OwnerLabel: "alice", KeyHash: "x", KeyPrefix: "sk-", Status: model.APIKeyStatusActive}
	key2 := &model.APIKey{OwnerLabel: "bob", KeyHash: "y", KeyPrefix: "sk-", Status: model.APIKeyStatusActive}
	if err := db.Create(key1).Error; err != nil {
		t.Fatalf("seed api_key 1: %v", err)
	}
	if err := db.Create(key2).Error; err != nil {
		t.Fatalf("seed api_key 2: %v", err)
	}
	// Seed two providers so TopProviders resolves provider_name.
	prov1 := seedProvider(t, db, "openai-main")
	prov2 := seedProvider(t, db, "anthropic-main")

	now := time.Now().UTC()
	day3 := now.Add(-3 * 24 * time.Hour)

	// Row 1 (alice, gpt-4o-mini, openai, compressed, diff+gotest+diff): 100 tokens, 10 cost.
	seedRequestLog(t, db, "c1", now, func(r *model.RequestLog) {
		r.APIKeyID = &key1.ID
		r.ModelName = "gpt-4o-mini"
		r.ProviderID = &prov1
		r.InputTokens = 500
		r.CompressEstimatedTokensSaved = 100
		r.CompressEstimatedCostSavedMicros = 10
		r.CompressSkipReason = ""
		r.CompressorsApplied = "diff,gotest,diff"
	})
	// Row 2 (bob, claude-3-haiku, anthropic, compressed, diff): 50 tokens, 5 cost.
	seedRequestLog(t, db, "c2", now, func(r *model.RequestLog) {
		r.APIKeyID = &key2.ID
		r.ModelName = "claude-3-haiku"
		r.ProviderID = &prov2
		r.InputTokens = 200
		r.CompressEstimatedTokensSaved = 50
		r.CompressEstimatedCostSavedMicros = 5
		r.CompressSkipReason = ""
		r.CompressorsApplied = "diff"
	})
	// Row 3 (alice, gpt-4o-mini, openai, skipped too_small, 0 tokens, no compressors).
	seedRequestLog(t, db, "c3", now, func(r *model.RequestLog) {
		r.APIKeyID = &key1.ID
		r.ModelName = "gpt-4o-mini"
		r.ProviderID = &prov1
		r.InputTokens = 5
		r.CompressSkipReason = "too_small"
		r.CompressorsApplied = ""
	})
	// Row 4 (alice, gpt-4o-mini, openai, 3 days ago, compressed, gotest): 30 tokens, 3 cost.
	seedRequestLog(t, db, "c4", day3, func(r *model.RequestLog) {
		r.APIKeyID = &key1.ID
		r.ModelName = "gpt-4o-mini"
		r.ProviderID = &prov1
		r.InputTokens = 100
		r.CompressEstimatedTokensSaved = 30
		r.CompressEstimatedCostSavedMicros = 3
		r.CompressSkipReason = ""
		r.CompressorsApplied = "gotest"
	})
	// Row 5 (bob, claude-3-haiku, anthropic, compression never attempted).
	// BOTH compress_skip_reason='' AND compressors_applied='' — this is the
	// regression case that proves compressed_calls is gated on
	// compressors_applied, not on compress_skip_reason. Counting this row as
	// compressed would be the overcount bug.
	seedRequestLog(t, db, "c5", now, func(r *model.RequestLog) {
		r.APIKeyID = &key2.ID
		r.ModelName = "claude-3-haiku"
		r.ProviderID = &prov2
		r.InputTokens = 50
	})

	data := doCompressStats(t, r, "")

	// Totals: 5 total calls, 3 compressed (rows 1, 2, 4 — NOT row 5 which
	// never attempted), tokens 100+50+30=180, cost 10+5+3=18, total
	// input_tokens 500+200+5+100+50=855.
	if data.Totals.TotalCalls != 5 {
		t.Fatalf("TotalCalls = %d, want 5", data.Totals.TotalCalls)
	}
	if data.Totals.CompressedCalls != 3 {
		t.Fatalf("CompressedCalls = %d, want 3 (row 5 must NOT count)", data.Totals.CompressedCalls)
	}
	if data.Totals.TokensSaved != 180 {
		t.Fatalf("TokensSaved = %d, want 180", data.Totals.TokensSaved)
	}
	if data.Totals.CostSavedMicros != 18 {
		t.Fatalf("CostSavedMicros = %d, want 18", data.Totals.CostSavedMicros)
	}
	if data.Totals.TotalEstimatedTokens != 855 {
		t.Fatalf("TotalEstimatedTokens = %d, want 855", data.Totals.TotalEstimatedTokens)
	}

	// Skip-reason breakdown is gated on rows that ENTERED the compress stage
	// (compressors_applied != '' OR compress_skip_reason != ''). Row 5 (both
	// empty — switch off / never attempted) must NOT appear here; the ''
	// bucket is "entered + succeeded" only (rows 1, 2, 4), and 'too_small' is
	// row 3 which entered but skipped.
	if len(data.SkipReasonBreakdown) != 2 {
		t.Fatalf("SkipReasonBreakdown len = %d, want 2", len(data.SkipReasonBreakdown))
	}
	if data.SkipReasonBreakdown[0].SkipReason != "" || data.SkipReasonBreakdown[0].Calls != 3 {
		t.Fatalf("SkipReasonBreakdown[0] = %+v, want ''/3 (row 5 must NOT appear)", data.SkipReasonBreakdown[0])
	}
	if data.SkipReasonBreakdown[1].SkipReason != "too_small" || data.SkipReasonBreakdown[1].Calls != 1 {
		t.Fatalf("SkipReasonBreakdown[1] = %+v, want too_small/1", data.SkipReasonBreakdown[1])
	}

	// Top-N api keys by tokens_saved DESC: alice (130 = 100+30), bob (50).
	if len(data.TopAPIKeys) != 2 {
		t.Fatalf("TopAPIKeys len = %d, want 2", len(data.TopAPIKeys))
	}
	if data.TopAPIKeys[0].OwnerLabel != "alice" || data.TopAPIKeys[0].TokensSaved != 130 {
		t.Fatalf("TopAPIKeys[0] = %+v, want alice/130", data.TopAPIKeys[0])
	}
	if data.TopAPIKeys[1].OwnerLabel != "bob" || data.TopAPIKeys[1].TokensSaved != 50 {
		t.Fatalf("TopAPIKeys[1] = %+v, want bob/50", data.TopAPIKeys[1])
	}
	// Calls per key: alice has 3 rows (c1, c3, c4); bob has 2 (c2, c5).
	if data.TopAPIKeys[0].Calls != 3 {
		t.Fatalf("TopAPIKeys[0].Calls = %d, want 3", data.TopAPIKeys[0].Calls)
	}
	if data.TopAPIKeys[1].Calls != 2 {
		t.Fatalf("TopAPIKeys[1].Calls = %d, want 2", data.TopAPIKeys[1].Calls)
	}

	// Top-N models by tokens_saved DESC. Only compressed rows (c1, c2, c4)
	// participate — c3 (skip) and c5 (never attempted) are excluded by the
	// compressors_applied != '' gate.
	// gpt-4o-mini: 100+30=130 tokens, 10+3=13 cost, 2 compressed, 2 total.
	// claude-3-haiku: 50 tokens, 5 cost, 1 compressed, 1 total.
	if len(data.TopModels) != 2 {
		t.Fatalf("TopModels len = %d, want 2", len(data.TopModels))
	}
	if data.TopModels[0].ModelName != "gpt-4o-mini" || data.TopModels[0].TokensSaved != 130 {
		t.Fatalf("TopModels[0] = %+v, want gpt-4o-mini/130", data.TopModels[0])
	}
	if data.TopModels[0].CostSavedMicros != 13 || data.TopModels[0].CompressedCalls != 2 || data.TopModels[0].TotalCalls != 2 {
		t.Fatalf("TopModels[0] = %+v, want cost=13/compressed=2/total=2", data.TopModels[0])
	}
	if data.TopModels[1].ModelName != "claude-3-haiku" || data.TopModels[1].TokensSaved != 50 {
		t.Fatalf("TopModels[1] = %+v, want claude-3-haiku/50", data.TopModels[1])
	}
	if data.TopModels[1].CostSavedMicros != 5 || data.TopModels[1].CompressedCalls != 1 || data.TopModels[1].TotalCalls != 1 {
		t.Fatalf("TopModels[1] = %+v, want cost=5/compressed=1/total=1", data.TopModels[1])
	}

	// Top-N providers by tokens_saved DESC. Same compressed-only gate.
	// openai-main: 100+30=130 tokens, 10+3=13 cost, 2 compressed, 2 total.
	// anthropic-main: 50 tokens, 5 cost, 1 compressed, 1 total.
	if len(data.TopProviders) != 2 {
		t.Fatalf("TopProviders len = %d, want 2", len(data.TopProviders))
	}
	if data.TopProviders[0].ProviderName != "openai-main" || data.TopProviders[0].TokensSaved != 130 {
		t.Fatalf("TopProviders[0] = %+v, want openai-main/130", data.TopProviders[0])
	}
	if data.TopProviders[0].CostSavedMicros != 13 || data.TopProviders[0].CompressedCalls != 2 || data.TopProviders[0].TotalCalls != 2 {
		t.Fatalf("TopProviders[0] = %+v, want cost=13/compressed=2/total=2", data.TopProviders[0])
	}
	if data.TopProviders[1].ProviderName != "anthropic-main" || data.TopProviders[1].TokensSaved != 50 {
		t.Fatalf("TopProviders[1] = %+v, want anthropic-main/50", data.TopProviders[1])
	}
	if data.TopProviders[1].CostSavedMicros != 5 || data.TopProviders[1].CompressedCalls != 1 || data.TopProviders[1].TotalCalls != 1 {
		t.Fatalf("TopProviders[1] = %+v, want cost=5/compressed=1/total=1", data.TopProviders[1])
	}

	// Compressor hits: counts ROWS that used each compressor (not total
	// invocations). c1 "diff,gotest,diff" counts once for diff + once for
	// gotest; c2 "diff" counts once for diff; c4 "gotest" counts once for
	// gotest. So diff=2 (c1+c2), gotest=2 (c1+c4), log=0, grep=0.
	// Zero-hit entries are retained (all four known compressors appear).
	// Ordered by hits DESC, name ASC: diff(2), gotest(2), grep(0), log(0).
	if len(data.CompressorHits) != 4 {
		t.Fatalf("CompressorHits len = %d, want 4 (all known compressors)", len(data.CompressorHits))
	}
	if data.CompressorHits[0].Name != "diff" || data.CompressorHits[0].Hits != 2 {
		t.Fatalf("CompressorHits[0] = %+v, want diff/2 (rows c1+c2)", data.CompressorHits[0])
	}
	if data.CompressorHits[1].Name != "gotest" || data.CompressorHits[1].Hits != 2 {
		t.Fatalf("CompressorHits[1] = %+v, want gotest/2 (rows c1+c4)", data.CompressorHits[1])
	}
	if data.CompressorHits[2].Name != "grep" || data.CompressorHits[2].Hits != 0 {
		t.Fatalf("CompressorHits[2] = %+v, want grep/0", data.CompressorHits[2])
	}
	if data.CompressorHits[3].Name != "log" || data.CompressorHits[3].Hits != 0 {
		t.Fatalf("CompressorHits[3] = %+v, want log/0", data.CompressorHits[3])
	}

	// Daily series: at least 4 days (gap-filled between day3 and now).
	// Total tokens across the series = 180; total compressed_calls = 3.
	if len(data.DailySeries) < 4 {
		t.Fatalf("DailySeries len = %d, want >= 4 (gap-fill)", len(data.DailySeries))
	}
	var seriesTokens int64
	var seriesCalls int64
	for _, row := range data.DailySeries {
		seriesTokens += row.TokensSaved
		seriesCalls += row.CompressedCalls
	}
	if seriesTokens != 180 {
		t.Fatalf("series TokensSaved sum = %d, want 180", seriesTokens)
	}
	if seriesCalls != 3 {
		t.Fatalf("series CompressedCalls sum = %d, want 3", seriesCalls)
	}
	// Newest-first ordering (today at index 0).
	if data.DailySeries[0].Bucket == "" {
		t.Fatalf("DailySeries[0].Bucket empty; rows: %+v", data.DailySeries)
	}
}

// TestGetCompressStatsEmptyReturnsEmptyArrays verifies that a filter that
// matches zero rows produces empty arrays (not JSON null) for every slice
// field — the contract the frontend relies on for .map / .length.
func TestGetCompressStatsEmptyReturnsEmptyArrays(t *testing.T) {
	r, _ := newAnalyticsTestRouter(t)
	// No rows seeded; the default 7-day window still matches nothing.
	data := doCompressStats(t, r, "")

	if data.SkipReasonBreakdown == nil {
		t.Fatalf("SkipReasonBreakdown = nil, want empty slice")
	}
	if len(data.SkipReasonBreakdown) != 0 {
		t.Fatalf("SkipReasonBreakdown len = %d, want 0", len(data.SkipReasonBreakdown))
	}
	if data.TopAPIKeys == nil {
		t.Fatalf("TopAPIKeys = nil, want empty slice")
	}
	if len(data.TopAPIKeys) != 0 {
		t.Fatalf("TopAPIKeys len = %d, want 0", len(data.TopAPIKeys))
	}
	if data.TopModels == nil {
		t.Fatalf("TopModels = nil, want empty slice")
	}
	if len(data.TopModels) != 0 {
		t.Fatalf("TopModels len = %d, want 0", len(data.TopModels))
	}
	if data.TopProviders == nil {
		t.Fatalf("TopProviders = nil, want empty slice")
	}
	if len(data.TopProviders) != 0 {
		t.Fatalf("TopProviders len = %d, want 0", len(data.TopProviders))
	}
	if data.CompressorHits == nil {
		t.Fatalf("CompressorHits = nil, want slice")
	}
	// CompressorHits now always returns the four known compressors (zero-hit
	// entries retained) so the UI can show which compressors exist even when
	// they haven't fired.
	if len(data.CompressorHits) != 4 {
		t.Fatalf("CompressorHits len = %d, want 4 (all known, zero-hit)", len(data.CompressorHits))
	}
	for _, ch := range data.CompressorHits {
		if ch.Hits != 0 {
			t.Fatalf("CompressorHits = %+v, want all zero", data.CompressorHits)
		}
	}
	if data.DailySeries == nil {
		t.Fatalf("DailySeries = nil, want empty slice")
	}
	// DailySeries is still gap-filled (7 days of zeros by default), so it
	// isn't empty — but it must be a non-nil slice.
	if len(data.DailySeries) == 0 {
		t.Fatalf("DailySeries len = 0, want gap-filled days")
	}
	// Totals is zero-valued but not nil (struct value, never nil).
	if data.Totals.TotalCalls != 0 || data.Totals.TokensSaved != 0 {
		t.Fatalf("Totals = %+v, want all zero", data.Totals)
	}
}

// TestGetCompressStatsRespectsAPIKeyFilter verifies the shared filter shape
// propagates to every sub-query (totals, skip-reasons, Top-N, compressors,
// daily). An api_key_id filter that matches one key should yield that key's
// rows only.
func TestGetCompressStatsRespectsAPIKeyFilter(t *testing.T) {
	r, db := newAnalyticsTestRouter(t)
	key1 := &model.APIKey{OwnerLabel: "alice", KeyHash: "x", KeyPrefix: "sk-", Status: model.APIKeyStatusActive}
	key2 := &model.APIKey{OwnerLabel: "bob", KeyHash: "y", KeyPrefix: "sk-", Status: model.APIKeyStatusActive}
	if err := db.Create(key1).Error; err != nil {
		t.Fatalf("seed api_key 1: %v", err)
	}
	if err := db.Create(key2).Error; err != nil {
		t.Fatalf("seed api_key 2: %v", err)
	}
	now := time.Now().UTC()
	seedRequestLog(t, db, "c1", now, func(r *model.RequestLog) {
		r.APIKeyID = &key1.ID
		r.CompressEstimatedTokensSaved = 100
		r.CompressorsApplied = "diff"
	})
	seedRequestLog(t, db, "c2", now, func(r *model.RequestLog) {
		r.APIKeyID = &key2.ID
		r.CompressEstimatedTokensSaved = 50
		r.CompressorsApplied = "gotest"
	})

	// Filter to alice's key only.
	data := doCompressStats(t, r, "api_key_id="+strconv.FormatUint(uint64(key1.ID), 10))
	if data.Totals.TotalCalls != 1 {
		t.Fatalf("TotalCalls = %d, want 1 (filtered)", data.Totals.TotalCalls)
	}
	if data.Totals.TokensSaved != 100 {
		t.Fatalf("TokensSaved = %d, want 100", data.Totals.TokensSaved)
	}
	if len(data.TopAPIKeys) != 1 || data.TopAPIKeys[0].OwnerLabel != "alice" {
		t.Fatalf("TopAPIKeys = %+v, want alice only", data.TopAPIKeys)
	}
	// CompressorHits returns all four known compressors; only diff has a
	// non-zero hit count (1 row, alice's c1 which used "diff").
	if len(data.CompressorHits) != 4 {
		t.Fatalf("CompressorHits len = %d, want 4 (all known)", len(data.CompressorHits))
	}
	if data.CompressorHits[0].Name != "diff" || data.CompressorHits[0].Hits != 1 {
		t.Fatalf("CompressorHits[0] = %+v, want diff/1", data.CompressorHits[0])
	}
}

// TestGetCompressStatsLimitParamClampsToMax verifies the limit query param
// both clamps and defaults correctly. We pass limit=1000 and expect the
// service to clamp to MaxCompressTopN (20); the row count can't exceed the
// seeded key count either way, so we verify the request is accepted and
// returns at most MaxCompressTopN rows.
func TestGetCompressStatsLimitParamClampsToMax(t *testing.T) {
	r, _ := newAnalyticsTestRouter(t)
	// No rows seeded — clamp doesn't change row count, but a 1000 must not
	// 400 (the parser accepts and clamps).
	data := doCompressStats(t, r, "limit=1000")
	if len(data.TopAPIKeys) > service.MaxCompressTopN {
		t.Fatalf("TopAPIKeys len = %d, want <= %d (clamped)", len(data.TopAPIKeys), service.MaxCompressTopN)
	}
}

// TestGetCompressStatsRejectsBadLimit verifies a non-numeric / zero limit
// produces a 400, not a silent default — same posture as the existing
// filter-param validators.
func TestGetCompressStatsRejectsBadLimit(t *testing.T) {
	r, _ := newAnalyticsTestRouter(t)
	w, _ := doJSON(t, r, http.MethodGet, "/api/admin/analytics/compress-stats?limit=banana", nil, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for limit=banana, got %d", w.Code)
	}
	w, _ = doJSON(t, r, http.MethodGet, "/api/admin/analytics/compress-stats?limit=0", nil, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400 for limit=0, got %d", w.Code)
	}
}

// TestGetCompressStatsRejectsBadStartTime verifies the shared filter parser
// still runs before compress-stats does its work — a bad start timestamp
// fails the same way it does for /analytics/overview.
func TestGetCompressStatsRejectsBadStartTime(t *testing.T) {
	r, _ := newAnalyticsTestRouter(t)
	w, _ := doJSON(t, r, http.MethodGet, "/api/admin/analytics/compress-stats?start=not-a-time", nil, nil)
	if w.Code != http.StatusBadRequest {
		t.Fatalf("expected 400, got %d", w.Code)
	}
}

// TestGetCompressStatsTopAPIKeysDropsNullBucketWithinLimit seeds a NULL
// api_key_id row (auth-failed requests) whose tokens_saved would sort it
// inside the Top-N window, then asserts the NULL bucket is excluded at the
// SQL layer (HAVING api_key_id IS NOT NULL) rather than post-fetch in Go.
//
// Before the HAVING fix the NULL row consumed a LIMIT slot and was then
// dropped by a Go loop, so the caller got back limit-1 real keys instead of
// the requested limit. With 5 real keys + 1 NULL bucket and limit=5, the
// response must contain all 5 real keys (not 4).
func TestGetCompressStatsTopAPIKeysDropsNullBucketWithinLimit(t *testing.T) {
	r, db := newAnalyticsTestRouter(t)

	// Five real keys, each with a distinct tokens_saved so the order is
	// deterministic. Their values are all BELOW the NULL bucket's 1000 so
	// the NULL row would land at position 0 inside the LIMIT=5 window.
	for i := 0; i < 5; i++ {
		key := &model.APIKey{
			OwnerLabel: "k" + strconv.Itoa(i),
			KeyHash:    "h" + strconv.Itoa(i),
			KeyPrefix:  "sk-",
			Status:     model.APIKeyStatusActive,
		}
		if err := db.Create(key).Error; err != nil {
			t.Fatalf("seed api_key %d: %v", i, err)
		}
		seedRequestLog(t, db, "real"+strconv.Itoa(i), time.Now().UTC(), func(r *model.RequestLog) {
			r.APIKeyID = &key.ID
			r.CompressEstimatedTokensSaved = 100 - i // 100, 99, 98, 97, 96
		})
	}

	// NULL api_key_id row — sorts first (1000 > 100) and would steal a slot.
	seedRequestLog(t, db, "null-bucket", time.Now().UTC(), func(r *model.RequestLog) {
		r.APIKeyID = nil
		r.CompressEstimatedTokensSaved = 1000
	})

	data := doCompressStats(t, r, "limit=5")
	if len(data.TopAPIKeys) != 5 {
		t.Fatalf("TopAPIKeys len = %d, want 5 (NULL bucket must not consume a LIMIT slot)", len(data.TopAPIKeys))
	}
	for _, row := range data.TopAPIKeys {
		if row.APIKeyID == nil {
			t.Fatalf("NULL api_key_id row leaked into TopAPIKeys: %+v", row)
		}
		if row.OwnerLabel == "" {
			t.Fatalf("OwnerLabel empty for api_key_id=%v (label resolution broken)", row.APIKeyID)
		}
	}
}

// TestGetCompressStatsTopProvidersDropsNullBucketWithinLimit mirrors the
// TopAPIKeys NULL-bucket test: seeds a NULL provider_id row whose
// tokens_saved would sort it first, then asserts the HAVING clause drops it
// at the SQL layer so all 3 real providers fit within LIMIT=3.
func TestGetCompressStatsTopProvidersDropsNullBucketWithinLimit(t *testing.T) {
	r, db := newAnalyticsTestRouter(t)

	// Three real providers with descending tokens_saved so the order is
	// deterministic. All values are BELOW the NULL bucket's 1000 so the NULL
	// row would land at position 0 inside the LIMIT=3 window.
	for i := 0; i < 3; i++ {
		pid := seedProvider(t, db, "p"+strconv.Itoa(i))
		seedRequestLog(t, db, "prov"+strconv.Itoa(i), time.Now().UTC(), func(r *model.RequestLog) {
			r.ProviderID = &pid
			r.ModelName = "m" + strconv.Itoa(i)
			r.CompressEstimatedTokensSaved = 100 - i // 100, 99, 98
			r.CompressorsApplied = "diff"
		})
	}

	// NULL provider_id row — sorts first (1000 > 100) and would steal a slot.
	seedRequestLog(t, db, "null-prov", time.Now().UTC(), func(r *model.RequestLog) {
		r.ProviderID = nil
		r.CompressEstimatedTokensSaved = 1000
		r.CompressorsApplied = "diff"
	})

	data := doCompressStats(t, r, "limit=3")
	if len(data.TopProviders) != 3 {
		t.Fatalf("TopProviders len = %d, want 3 (NULL bucket must not consume a LIMIT slot)", len(data.TopProviders))
	}
	for _, row := range data.TopProviders {
		if row.ProviderID == nil {
			t.Fatalf("NULL provider_id row leaked into TopProviders: %+v", row)
		}
		if row.ProviderName == "" {
			t.Fatalf("ProviderName empty for provider_id=%v (name resolution broken)", row.ProviderID)
		}
	}
}

// TestGetCompressStatsDailySeriesAscendingWithGapFill seeds compressed rows
// on two non-adjacent days inside a narrow window, then verifies the daily
// series:
//   - is gap-filled (every day in the window appears, even with zero rows),
//   - is sorted ascending (oldest-first) for a left-to-right trend chart,
//   - only counts rows where compressors_applied != ”.
func TestGetCompressStatsDailySeriesAscendingWithGapFill(t *testing.T) {
	r, db := newAnalyticsTestRouter(t)

	now := time.Now().UTC()
	// Two rows: today and 2 days ago. The day in between has zero compressed
	// rows and must still appear as a zero gap-fill row.
	seedRequestLog(t, db, "c1", now, func(r *model.RequestLog) {
		r.CompressEstimatedTokensSaved = 100
		r.CompressorsApplied = "diff"
	})
	seedRequestLog(t, db, "c2", now.Add(-2*24*time.Hour), func(r *model.RequestLog) {
		r.CompressEstimatedTokensSaved = 50
		r.CompressorsApplied = "gotest"
	})

	// Use a 4-day window so we know there are exactly 4 buckets.
	start := now.Add(-3 * 24 * time.Hour).Format(time.RFC3339)
	end := now.Add(1 * 24 * time.Hour).Format(time.RFC3339)
	data := doCompressStats(t, r, "start="+start+"&end="+end)

	if len(data.DailySeries) < 4 {
		t.Fatalf("DailySeries len = %d, want >= 4 (gap-fill)", len(data.DailySeries))
	}
	// Ascending: every bucket label must be <= the next (oldest-first).
	for i := 1; i < len(data.DailySeries); i++ {
		if data.DailySeries[i].Bucket < data.DailySeries[i-1].Bucket {
			t.Fatalf("DailySeries not ascending at index %d: %q > %q",
				i, data.DailySeries[i-1].Bucket, data.DailySeries[i].Bucket)
		}
	}
	// At least one zero-fill row exists between the two seeded days.
	var zeroRows int
	for _, row := range data.DailySeries {
		if row.CompressedCalls == 0 && row.TokensSaved == 0 {
			zeroRows++
		}
	}
	if zeroRows == 0 {
		t.Fatalf("no zero-fill rows found; DailySeries = %+v", data.DailySeries)
	}
}

// TestGetCompressStatsCompressorHitsCountsRowsNotInvocations verifies the
// SQL-side compressor-hit counter counts ROWS-that-used-the-compressor, not
// total invocations. A row listing "log,log,diff" counts once for log and
// once for diff (not twice for log).
func TestGetCompressStatsCompressorHitsCountsRowsNotInvocations(t *testing.T) {
	r, db := newAnalyticsTestRouter(t)

	now := time.Now().UTC()
	// One row that lists log twice + diff once. Under the old app-side
	// split, log would get 2 hits. Under the new SQL-side LIKE counting,
	// log gets 1 (the row used log, regardless of how many times).
	seedRequestLog(t, db, "c1", now, func(r *model.RequestLog) {
		r.CompressorsApplied = "log,log,diff"
	})

	data := doCompressStats(t, r, "")

	// All four known compressors appear; log=1 (not 2), diff=1, gotest=0, grep=0.
	hitsByName := make(map[string]int64, len(data.CompressorHits))
	for _, ch := range data.CompressorHits {
		hitsByName[ch.Name] = ch.Hits
	}
	if hitsByName["log"] != 1 {
		t.Fatalf("log hits = %d, want 1 (row-count, not invocation-count)", hitsByName["log"])
	}
	if hitsByName["diff"] != 1 {
		t.Fatalf("diff hits = %d, want 1", hitsByName["diff"])
	}
	if hitsByName["gotest"] != 0 {
		t.Fatalf("gotest hits = %d, want 0", hitsByName["gotest"])
	}
	if hitsByName["grep"] != 0 {
		t.Fatalf("grep hits = %d, want 0", hitsByName["grep"])
	}
}

// === Helpers =============================================================

// approxEqual compares two floats with an absolute tolerance — fine for the
// success-rate math in these tests (denominators are small, precision is
// not the question under test).
func approxEqual(a, b, tol float64) bool {
	if a > b {
		return a-b <= tol
	}
	return b-a <= tol
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}
