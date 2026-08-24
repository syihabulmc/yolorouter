package analytics

import (
	"context"
	"math"
	"testing"
	"time"

	"github.com/yolorouter/yolorouter/internal/model"
	"github.com/yolorouter/yolorouter/internal/repository"
	"github.com/yolorouter/yolorouter/internal/testutil"
)

// seedPricedOutputRow inserts a model with a priced candidate plus one
// request log carrying the given output volume, all inside any reasonable
// test window, and returns the service bound to the same db.
func seedPricedOutputRow(t *testing.T, requestID string, outputTokens int, at time.Time) *AnalyticsService {
	t.Helper()
	db := testutil.NewSQLiteDB(t)
	m := model.Model{Name: "m-priced", ManagementStatus: model.ModelStatusEnabled,
		SchedulingMode: model.ModelSchedulingModeFailover}
	if err := db.Create(&m).Error; err != nil {
		t.Fatalf("create model: %v", err)
	}
	p := model.Provider{ID: 1, Name: "provider-1", ProviderType: "openai"}
	if err := db.Create(&p).Error; err != nil {
		t.Fatalf("create provider: %v", err)
	}
	providerID := uint(1)
	c := model.ModelCandidate{ModelID: m.ID, ProviderID: providerID,
		ProviderModelName: "upstream/m-priced", OutputPrice: 1.0}
	if err := db.Create(&c).Error; err != nil {
		t.Fatalf("create candidate: %v", err)
	}
	log := model.RequestLog{RequestID: requestID, ModelName: "m-priced",
		ProviderID: &providerID, StatusCode: 200, OutputTokens: outputTokens, CreatedAt: at}
	if err := repository.CreateRequestLog(db, &log); err != nil {
		t.Fatalf("seed log %s: %v", requestID, err)
	}
	return NewAnalyticsService(db)
}

// TestConciseOutputProjectionPerMillionMath pins the per-million-token
// formula: projected = spend x coefficient x 1M / priced tokens — i.e. the
// traffic-weighted output price scaled by the coefficient. A dropped
// coefficient factor or a wrong divisor here changes the banner figure
// silently, so the expected value is computed from the formula itself.
func TestConciseOutputProjectionPerMillionMath(t *testing.T) {
	at := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	svc := seedPricedOutputRow(t, "p1", 700_000, at)

	start := time.Date(2026, 8, 15, 0, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)
	now := time.Date(2026, 8, 22, 0, 0, 0, 0, time.UTC)
	f := &repository.RequestLogFilter{StartTime: &start, EndTime: &end}

	p, err := svc.GetConciseOutputProjection(context.Background(), f, AnalyticsOptions{}, now)
	if err != nil {
		t.Fatalf("GetConciseOutputProjection: %v", err)
	}
	if p.Window.Start != start || p.Window.End != end {
		t.Errorf("window echo: got [%v, %v), want the resolved explicit range", p.Window.Start, p.Window.End)
	}
	if p.Window.Days != 7 {
		t.Errorf("Window.Days = %d, want 7", p.Window.Days)
	}
	if p.OutputSpendMicros != 700_000 || p.OutputRows != 1 || p.PricedRows != 1 || p.PricedOutputTokens != 700_000 {
		t.Fatalf("volume: got spend=%d rows=%d priced=%d tokens=%d, want 700000/1/1/700000",
			p.OutputSpendMicros, p.OutputRows, p.PricedRows, p.PricedOutputTokens)
	}
	// 700K tokens at 1 CNY/M = 700000 micros spend; per 1M tokens that is
	// coefficient x 1e6 micros = 0.089 CNY per million output tokens.
	want := int64(math.Round(700_000 * ConciseOutputCoefficient * 1e6 / 700_000))
	if p.ProjectedSavingsPerMillionTokensMicros != want {
		t.Errorf("per-million = %d, want %d (spend x coefficient x 1M / priced tokens)",
			p.ProjectedSavingsPerMillionTokensMicros, want)
	}
	if p.Coefficient != ConciseOutputCoefficient {
		t.Errorf("Coefficient echo = %v, want %v (the UI renders the rate's basis from it)",
			p.Coefficient, ConciseOutputCoefficient)
	}
}

// TestConciseOutputProjectionShortWindowFloor pins the sub-day floor: a
// window shorter than 24h still counts as one day in the echoed window (no
// divide-by-zero in the echo, and the per-million figure never depended on
// the day count anyway).
func TestConciseOutputProjectionShortWindowFloor(t *testing.T) {
	at := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	svc := seedPricedOutputRow(t, "p1", 100, at)

	start := time.Date(2026, 8, 20, 11, 0, 0, 0, time.UTC)
	end := time.Date(2026, 8, 20, 13, 0, 0, 0, time.UTC)
	f := &repository.RequestLogFilter{StartTime: &start, EndTime: &end}

	p, err := svc.GetConciseOutputProjection(context.Background(), f, AnalyticsOptions{}, start)
	if err != nil {
		t.Fatalf("GetConciseOutputProjection: %v", err)
	}
	if p.Window.Days != 1 {
		t.Errorf("Window.Days = %d, want 1 (sub-day window floors at one day)", p.Window.Days)
	}
	want := int64(math.Round(100 * ConciseOutputCoefficient * 1e6 / 100))
	if p.ProjectedSavingsPerMillionTokensMicros != want {
		t.Errorf("per-million = %d, want %d (unit rate independent of the day count)",
			p.ProjectedSavingsPerMillionTokensMicros, want)
	}
}

// TestConciseOutputWindowDaysDST pins the nearest-day rounding: a calendar
// week crossing a DST transition spans 167 or 169 actual hours, and the day
// count must stay 7 — ceiling 169h to 8 days would shave ~12% off that
// window's month-scale factor. A sub-day window still floors at one day.
func TestConciseOutputWindowDaysDST(t *testing.T) {
	base := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	if got := windowDays(base, base.Add(169*time.Hour)); got != 7 {
		t.Errorf("windowDays(169h) = %d, want 7", got)
	}
	if got := windowDays(base, base.Add(167*time.Hour)); got != 7 {
		t.Errorf("windowDays(167h) = %d, want 7", got)
	}
	if got := windowDays(base, base.Add(2*time.Hour)); got != 1 {
		t.Errorf("windowDays(2h) = %d, want 1 (sub-day floor)", got)
	}
}

// TestConciseOutputProjectionEmptyWindow pins the no-traffic shape: zero
// totals (never an error), and the default 7-day window resolved when the
// caller supplied none.
func TestConciseOutputProjectionEmptyWindow(t *testing.T) {
	db := testutil.NewSQLiteDB(t)
	svc := NewAnalyticsService(db)
	now := time.Date(2026, 8, 22, 9, 30, 0, 0, time.UTC)

	p, err := svc.GetConciseOutputProjection(context.Background(), &repository.RequestLogFilter{}, AnalyticsOptions{}, now)
	if err != nil {
		t.Fatalf("GetConciseOutputProjection on empty db: %v", err)
	}
	if p.OutputSpendMicros != 0 || p.OutputRows != 0 || p.PricedRows != 0 || p.PricedOutputTokens != 0 ||
		p.ProjectedSavingsPerMillionTokensMicros != 0 {
		t.Errorf("empty db: got %+v, want all-zero totals", p)
	}
	if p.Window.Days != 7 {
		t.Errorf("Window.Days = %d, want 7 (default lookback)", p.Window.Days)
	}
	if p.Window.End.Before(now.Add(-time.Hour)) {
		t.Errorf("default window end %v should sit at the current day boundary near %v", p.Window.End, now)
	}
}
