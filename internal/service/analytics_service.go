// Package service additions: analytics composition.
// Strict 3-layer handler → service → repository, same shape as the
// sibling DashboardService / RequestLogService. This service owns:
//
//   - the analytics filter shape (AnalyticsFilter) — service-level mirror of
//     repository.RequestLogFilter, kept as a separate type so the handler
//     layer never imports repository
//   - the dimension / bucket vocabulary on the wire
//   - the overview row DTO that wraps MetricTotals + success_rate
//   - the CSV stream assembly (BOM + headers + records)
//
// All SQL lives in internal/repository/analytics_repository.go.
package service

import (
	"context"
	"errors"
	"io"
	"strconv"
	"time"

	"gorm.io/gorm"

	"github.com/yolorouter/yolorouter/internal/repository"
	"github.com/yolorouter/yolorouter/pkg/csvutil"
)

// === Wire-level vocab ====================================================
//
// Mirrored here (not imported from repository) so the handler layer stays
// repository-free — same convention request_log_handler.go uses for its
// validStatusClasses map.

const (
	// DimensionModel groups by request_logs.model_name.
	DimensionModel = repository.ReportDimensionModel
	// DimensionProvider groups by request_logs.provider_id.
	DimensionProvider = repository.ReportDimensionProvider
	// DimensionCaller groups by request_logs.api_key_id.
	DimensionCaller = repository.ReportDimensionCaller
	// DimensionTime groups by local-day (or local-hour) time bucket.
	DimensionTime = repository.ReportDimensionTime
)

const (
	// BucketDay emits one row per calendar day in the configured location.
	BucketDay = repository.TimeBucketDay
	// BucketHour emits one row per clock hour in the configured location.
	BucketHour = repository.TimeBucketHour
)

// ErrInvalidDimension is returned by GetReport / ExportCSV when dimension
// isn't one of model|provider|caller|time. The handler maps it to 400.
var ErrInvalidDimension = errors.New("invalid dimension; must be one of: model, provider, caller, time")

// === Filter / DTO types ==================================================

// AnalyticsFilter is the service-level filter shape. Identical to
// repository.RequestLogFilter (the shared query shape) but defined here so
// the handler doesn't take a repository import. The conversion is a flat
// field copy in toRepoFilter.
type AnalyticsFilter struct {
	RequestID   string
	APIKeyID    *uint
	ModelName   string
	ProviderID  *uint
	StatusClass string
	IsStream    *bool
	StartTime   *time.Time // inclusive
	EndTime     *time.Time // exclusive
	// Location is the client-supplied IANA timezone used for day-bucket
	// grouping so analytics "today" matches the admin's wall clock. nil
	// falls back to the server's local zone (location()).
	Location *time.Location
}

// location returns the effective timezone for day-bucket aggregation,
// falling back to time.Local when the caller didn't supply one. Centralized
// here so every repository call site picks up the same zone without each
// having to repeat the nil-check.
func (f AnalyticsFilter) location() *time.Location {
	if f.Location != nil {
		return f.Location
	}
	return time.Local
}

// OverviewRow is the GET /analytics/overview body. Mirrors
// repository.MetricTotals plus a pre-computed SuccessRate so the client
// doesn't have to do the divide-on-zero dance.
type OverviewRow struct {
	TotalCalls       int64   `json:"total_calls"`
	SuccessCalls     int64   `json:"success_calls"`
	EndedCalls       int64   `json:"ended_calls"`
	SuccessRate      float64 `json:"success_rate"`
	UnknownCostCalls int64   `json:"unknown_cost_calls"`
	InputTokens      int64   `json:"input_tokens"`
	OutputTokens     int64   `json:"output_tokens"`
	CostMicros       int64   `json:"cost_micros"`
	// Cache economics for the filtered window. Net saving is
	// CacheReadSavedMicros − CacheWriteExtraMicros; both are sent so the client
	// can show the read saving and the write premium as separate line items.
	CacheReadSavedMicros  int64 `json:"cache_read_saved_micros"`
	CacheWriteExtraMicros int64 `json:"cache_write_extra_micros"`
}

// ReportResult is the GET /analytics/report body. Dimension echoes the
// request so a single client code path can render any dimension; Rows is
// the dimension-specific row slice (typed by the handler as
// []repository.*ReportRow — kept as interface{} here to avoid a service →
// handler generic-type handshake, see GetReport).
type ReportResult struct {
	Dimension string      `json:"dimension"`
	Rows      interface{} `json:"rows"`
}

// === Input-compression stats DTO =========================================

// CompressStatsResult is the GET /analytics/compress-stats body. Each slice
// is normalized to empty (never nil) so the JSON never emits null — the
// frontend can call .map / .length unconditionally.
//
// TopAPIKeyLimit echoes the effective limit used for TopAPIKeys so the UI
// can render "showing top N" deterministically. Totals carries the
// platform-wide roll-up; SkipReasonBreakdown / CompressorHits / DailySeries
// are the dimension-specific breakouts the UI renders side-by-side.
type CompressStatsResult struct {
	Totals              repository.CompressTotals           `json:"totals"`
	SkipReasonBreakdown []repository.CompressSkipReasonRow  `json:"skip_reason_breakdown"`
	TopAPIKeys          []repository.CompressTopAPIKeyRow   `json:"top_api_keys"`
	TopModels           []repository.CompressTopModelRow    `json:"top_models"`
	TopProviders        []repository.CompressTopProviderRow `json:"top_providers"`
	CompressorHits      []repository.CompressorHitRow       `json:"compressor_hits"`
	DailySeries         []repository.CompressDailySeriesRow `json:"daily_series"`
}

// === Service =============================================================

// AnalyticsService is the stateless composition layer over
// analytics_repository. It has no caching, masking, or permission post-
// processing — those concerns will hang off this struct in later milestones
// (cost-masking per admin role is a likely add).
type AnalyticsService struct {
	db *gorm.DB
}

// NewAnalyticsService returns an AnalyticsService bound to db. db is
// captured by reference; callers must not close it before this service
// stops being used (same lifecycle convention as every other service in
// internal/).
func NewAnalyticsService(db *gorm.DB) *AnalyticsService {
	return &AnalyticsService{db: db}
}

// GetOverview returns the aggregate MetricTotals for the filter.
// Reuses AggregateRequestLogMetrics so the overview's success-rate
// definition stays identical to the dashboard's today-card.
func (s *AnalyticsService) GetOverview(filter AnalyticsFilter, bucket string, now time.Time) (*OverviewRow, error) {
	// Clamp the window on the same bucket cap the report uses, so the overview
	// cards and the time-dimension report aggregate the exact same range.
	filter = resolveEffectiveRange(filter, bucket, now)
	m, err := repository.AggregateRequestLogMetrics(s.db, toRepoFilter(filter))
	if err != nil {
		return nil, err
	}
	return &OverviewRow{
		TotalCalls:            m.TotalCalls,
		SuccessCalls:          m.SuccessCalls,
		EndedCalls:            m.EndedCalls,
		SuccessRate:           m.SuccessRate(),
		UnknownCostCalls:      m.UnknownCostCalls,
		InputTokens:           m.InputTokens,
		OutputTokens:          m.OutputTokens,
		CostMicros:            m.KnownCostMicros,
		CacheReadSavedMicros:  m.CacheReadSavedMicros,
		CacheWriteExtraMicros: m.CacheWriteExtraMicros,
	}, nil
}

// GetReport returns the dimension-grouped aggregates. dimension selects one
// of model|provider|caller|time; bucket is only consulted for dimension=time
// (empty bucket defaults to "day"). Returns ErrInvalidDimension for an
// unrecognized dimension and repository.ErrInvalidBucket for an unrecognized
// bucket; the handler maps both to 400.
func (s *AnalyticsService) GetReport(dimension, bucket string, filter AnalyticsFilter, now time.Time) (*ReportResult, error) {
	rows, err := s.runReport(dimension, bucket, filter, now)
	if err != nil {
		return nil, err
	}
	return &ReportResult{Dimension: dimension, Rows: rows}, nil
}

// ExportCSV streams the report as CSV (UTF-8 BOM + headers + records). Same
// query path as GetReport; the caller (handler) has already set
// Content-Type / Content-Disposition headers and is responsible for the BOM
// bytes (so a mid-stream error can still truncate cleanly without a stray
// BOM appearing in an error envelope). The service writes headers + rows
// only and flushes once at the end — analytics exports are bounded by the
// lookback cap (90d day / 30d hour) so buffering the whole thing is fine.
// BuildCSVRecords runs the report and renders headers + records. Split from
// ExportCSV so the analytics handler can fail BEFORE committing the HTTP 200
// (same pattern as request_log's BuildExportRows + WriteCSVRows): a build
// failure here returns a JSON envelope, not a truncated CSV reported as
// success.
func (s *AnalyticsService) BuildCSVRecords(dimension, bucket string, filter AnalyticsFilter, now time.Time) ([]string, [][]string, error) {
	rows, err := s.runReport(dimension, bucket, filter, now)
	if err != nil {
		return nil, nil, err
	}
	return buildCSV(dimension, rows)
}

// ExportCSV is a thin wrapper retained for tests / direct callers; production
// handlers use BuildCSVRecords + csvutil.WriteCSV so a build failure never
// reaches the wire.
func (s *AnalyticsService) ExportCSV(dimension, bucket string, filter AnalyticsFilter, w io.Writer, now time.Time) error {
	headers, records, err := s.BuildCSVRecords(dimension, bucket, filter, now)
	if err != nil {
		return err
	}
	return csvutil.WriteCSV(w, headers, records)
}

// runReport dispatches to the right repository function and returns the
// typed row slice (caller-facing type chosen by dimension). The switch is
// the single point that maps dimension → row type.
func (s *AnalyticsService) runReport(dimension, bucket string, filter AnalyticsFilter, now time.Time) (interface{}, error) {
	// Resolve the window once for every dimension (day cap by default; the
	// time dimension uses the caller's bucket cap) so overview, model/
	// provider/caller, and time reports all see the same [start, end).
	filter = resolveEffectiveRange(filter, bucketForDimension(dimension, bucket), now)
	rf := toRepoFilter(filter)
	switch dimension {
	case DimensionModel:
		return repository.AggregateByModel(s.db, rf)
	case DimensionProvider:
		return repository.AggregateByProvider(s.db, rf)
	case DimensionCaller:
		return repository.AggregateByCaller(s.db, rf)
	case DimensionTime:
		return repository.AggregateByTime(s.db, rf, filter.location(), bucket, now)
	}
	return nil, ErrInvalidDimension
}

// toRepoFilter maps the service-level filter to repository's pointer-typed
// filter. Same layering convention as request_log_service.go's toRepoFilter.
func toRepoFilter(f AnalyticsFilter) *repository.RequestLogFilter {
	return &repository.RequestLogFilter{
		RequestID:   f.RequestID,
		APIKeyID:    f.APIKeyID,
		ModelName:   f.ModelName,
		ProviderID:  f.ProviderID,
		StatusClass: f.StatusClass,
		IsStream:    f.IsStream,
		StartTime:   f.StartTime,
		EndTime:     f.EndTime,
	}
}

// resolveEffectiveRange clamps the filter's window to the bucket lookback cap
// and writes the resolved [start, end) back into the filter. Every dimension
// and the overview go through this so they aggregate the SAME window —
// otherwise the time report would silently truncate an oversized custom range
// while the overview / model / provider / caller dimensions kept the full
// range. bucket="" uses the day-bucket cap.
func resolveEffectiveRange(filter AnalyticsFilter, bucket string, now time.Time) AnalyticsFilter {
	rf := toRepoFilter(filter)
	end, start := repository.ResolveTimeRange(rf, filter.location(), bucket, now)
	filter.StartTime = &start
	filter.EndTime = &end
	return filter
}

// bucketForDimension returns the bucket to use for range resolution: the time
// dimension honors the caller's bucket; every other dimension uses the day
// default (they don't bucket, so the day cap is the relevant upper bound).
func bucketForDimension(dimension, bucket string) string {
	if dimension == DimensionTime {
		return bucket
	}
	return BucketDay
}

// === CSV assembly ========================================================

// buildCSV returns the headers and record rows for a dimension's typed row
// slice. The type assertions encode the row type → column order mapping per
// dimension; a future column change only needs to update this function.
func buildCSV(dimension string, rows interface{}) ([]string, [][]string, error) {
	switch dimension {
	case DimensionModel:
		typed, ok := rows.([]repository.ModelReportRow)
		if !ok {
			return nil, nil, errCSVTypeMismatch("ModelReportRow")
		}
		headers := []string{"model_name", "calls", "success_rate", "input_tokens", "output_tokens", "cache_write_tokens", "cache_read_tokens", "cost_micros", "unknown_cost_calls"}
		records := make([][]string, len(typed))
		for i, r := range typed {
			records[i] = []string{
				r.ModelName,
				strconv.FormatInt(r.Calls, 10),
				formatRate(r.SuccessRate),
				strconv.FormatInt(r.InputTokens, 10),
				strconv.FormatInt(r.OutputTokens, 10),
				strconv.FormatInt(r.CacheWriteTokens, 10),
				strconv.FormatInt(r.CacheReadTokens, 10),
				strconv.FormatInt(r.CostMicros, 10),
				strconv.FormatInt(r.UnknownCostCalls, 10),
			}
		}
		return headers, records, nil
	case DimensionProvider:
		typed, ok := rows.([]repository.ProviderReportRow)
		if !ok {
			return nil, nil, errCSVTypeMismatch("ProviderReportRow")
		}
		headers := []string{"provider_id", "provider_name", "calls", "success_rate", "avg_duration_ms", "cost_micros", "unknown_cost_calls"}
		records := make([][]string, len(typed))
		for i, r := range typed {
			records[i] = []string{
				formatUintPtr(r.ProviderID),
				r.ProviderName,
				strconv.FormatInt(r.Calls, 10),
				formatRate(r.SuccessRate),
				strconv.FormatFloat(r.AvgDurationMs, 'f', 2, 64),
				strconv.FormatInt(r.CostMicros, 10),
				strconv.FormatInt(r.UnknownCostCalls, 10),
			}
		}
		return headers, records, nil
	case DimensionCaller:
		typed, ok := rows.([]repository.CallerReportRow)
		if !ok {
			return nil, nil, errCSVTypeMismatch("CallerReportRow")
		}
		headers := []string{"api_key_id", "owner_label", "calls", "success_rate", "input_tokens", "output_tokens", "cache_write_tokens", "cache_read_tokens", "cost_micros", "unknown_cost_calls"}
		records := make([][]string, len(typed))
		for i, r := range typed {
			records[i] = []string{
				formatUintPtr(r.APIKeyID),
				r.OwnerLabel,
				strconv.FormatInt(r.Calls, 10),
				formatRate(r.SuccessRate),
				strconv.FormatInt(r.InputTokens, 10),
				strconv.FormatInt(r.OutputTokens, 10),
				strconv.FormatInt(r.CacheWriteTokens, 10),
				strconv.FormatInt(r.CacheReadTokens, 10),
				strconv.FormatInt(r.CostMicros, 10),
				strconv.FormatInt(r.UnknownCostCalls, 10),
			}
		}
		return headers, records, nil
	case DimensionTime:
		typed, ok := rows.([]repository.TimeReportRow)
		if !ok {
			return nil, nil, errCSVTypeMismatch("TimeReportRow")
		}
		headers := []string{"bucket", "calls", "success_rate", "input_tokens", "output_tokens", "cache_write_tokens", "cache_read_tokens", "cost_micros", "unknown_cost_calls"}
		records := make([][]string, len(typed))
		for i, r := range typed {
			records[i] = []string{
				r.Bucket,
				strconv.FormatInt(r.Calls, 10),
				formatRate(r.SuccessRate),
				strconv.FormatInt(r.InputTokens, 10),
				strconv.FormatInt(r.OutputTokens, 10),
				strconv.FormatInt(r.CacheWriteTokens, 10),
				strconv.FormatInt(r.CacheReadTokens, 10),
				strconv.FormatInt(r.CostMicros, 10),
				strconv.FormatInt(r.UnknownCostCalls, 10),
			}
		}
		return headers, records, nil
	}
	// Unreachable: runReport already validated dimension before handing the
	// result to ExportCSV. Returning an error (not panicking) so a future
	// dimension added to runReport without a buildCSV branch fails loudly
	// instead of crashing the process.
	return nil, nil, ErrInvalidDimension
}

// errCSVTypeMismatch is the defensive fallback when runReport's switch and
// buildCSV's switch disagree on the row type for a dimension — should be
// unreachable but a regression here would otherwise panic on the type
// assertion and take down the request.
func errCSVTypeMismatch(expected string) error {
	return errors.New("analytics: internal type mismatch, expected " + expected)
}

// formatRate renders a float success_rate as a fixed-point string with 4
// decimal digits (e.g. 0.6667). Spreadsheet apps handle floats either way,
// but fixed-point keeps the CSV diff-stable across exports.
func formatRate(r float64) string {
	return strconv.FormatFloat(r, 'f', 4, 64)
}

// formatUintPtr renders *uint as decimal or "" for nil — matches the JSON
// encoding of the row fields (provider_id / api_key_id with null for the
// NULL-FK bucket).
func formatUintPtr(v *uint) string {
	if v == nil {
		return ""
	}
	return strconv.FormatUint(uint64(*v), 10)
}

// === Input-compression stats ============================================

// DefaultCompressTopN is the per-api-key Top-N row count when the caller
// doesn't supply one. Mirrors the dashboard's "top 5 callers" default
// (rendering 5 rows fits the admin UI's card height).
const DefaultCompressTopN = 5

// MaxCompressTopN caps the per-api-key Top-N row count. A higher value
// wastes screen real estate without adding signal — the dashboard's other
// lists also cap at 20.
const MaxCompressTopN = 20

// GetCompressStats aggregates the compress_* columns into one response DTO.
// Clamps the filter window on the day-bucket cap so the daily series and
// totals aggregate the same range — same reason ResolveTimeRange is applied
// in runReport. topN is clamped to [1, MaxCompressTopN]; DefaultCompressTopN
// is used when the caller passes zero.
//
// ctx is threaded through to every repository query so a client disconnect
// cancels in-flight SQL. Every slice in the result is non-nil (empty [] on
// the wire, never JSON null) so the frontend can call .map / .length without
// null-checking.
func (s *AnalyticsService) GetCompressStats(ctx context.Context, filter AnalyticsFilter, topN int, now time.Time) (*CompressStatsResult, error) {
	if topN <= 0 {
		topN = DefaultCompressTopN
	}
	if topN > MaxCompressTopN {
		topN = MaxCompressTopN
	}
	// Clamp on the day-bucket cap so the daily series walk and the totals
	// share the exact same [start, end) window.
	filter = resolveEffectiveRange(filter, BucketDay, now)
	rf := toRepoFilter(filter)

	totals, err := repository.AggregateCompressTotals(ctx, s.db, rf)
	if err != nil {
		return nil, err
	}
	skipReasons, err := repository.AggregateCompressSkipReasons(ctx, s.db, rf)
	if err != nil {
		return nil, err
	}
	topKeys, err := repository.AggregateCompressTopAPIKeys(ctx, s.db, rf, topN)
	if err != nil {
		return nil, err
	}
	topModels, err := repository.AggregateCompressTopModels(ctx, s.db, rf, topN)
	if err != nil {
		return nil, err
	}
	topProviders, err := repository.AggregateCompressTopProviders(ctx, s.db, rf, topN)
	if err != nil {
		return nil, err
	}
	compressorHits, err := repository.AggregateCompressorHits(ctx, s.db, rf)
	if err != nil {
		return nil, err
	}
	daily, err := repository.AggregateCompressDailySeries(ctx, s.db, rf, filter.location(), now)
	if err != nil {
		return nil, err
	}
	return &CompressStatsResult{
		Totals:              *totals,
		SkipReasonBreakdown: skipReasons,
		TopAPIKeys:          topKeys,
		TopModels:           topModels,
		TopProviders:        topProviders,
		CompressorHits:      compressorHits,
		DailySeries:         daily,
	}, nil
}
