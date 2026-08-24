// Concise-output projection: the priced output-volume roll-up and
// per-million-token projection behind the cost-optimization banner's
// estimated-savings figure. Companion to the compress stats in
// analytics_service.go — same filter shape, same resolveEffectiveRange
// windowing; the arithmetic that turns the volume into a projected
// per-million-token figure lives here.
package analytics

import (
	"context"
	"math"
	"time"

	"github.com/yolorouter/yolorouter/internal/repository"
)

// ConciseOutputCoefficient is the single global factory coefficient behind
// the projected-savings figure: the median output-token reduction of the
// concise-output switch. One number for every model and instance — per-model
// tables age quickly and invite "why is my model in that band" questions an
// estimate cannot settle. The projection assumes the custom system prompt
// actually asks for concise output; an unrelated prompt text voids the figure
// (surfaced as help text in the UI).
//
// Measured 2026-08-24: 10 fixed questions x 3 rounds, paired on/off through
// a live gateway with the canonical concise prompt, across 5 models
// (claude-opus-4-7, deepseek-v4-flash, deepseek-v4-pro, glm-5.1,
// qwen3.5-flash; default sampling parameters). Median of the 150 pairs =
// 8.9%. The spread is wide and real: quartiles [-9.4%, +25.1%], 37% of the
// pairs negative — reasoning models can spend MORE thinking tokens when
// asked to be concise, so per-model medians range from -12% to +47%. The
// median is the honest single-number summary of that distribution. The
// full methodology and the complete 150-pair raw data are published at
// https://github.com/yolorouter/yolorouter/blob/main/docs/concise-output-benchmark.md
const ConciseOutputCoefficient = 0.089

// ConciseOutputWindow echoes the resolved [start, end) window the
// projection aggregated, so the UI can caption the figure with the actual
// span it was extrapolated from.
type ConciseOutputWindow struct {
	Start time.Time `json:"start"`
	End   time.Time `json:"end"`
	Days  int       `json:"days"`
}

// ConciseOutputProjection is the GET /analytics/concise-output-projection
// body. OutputSpendMicros / OutputRows / PricedRows / PricedOutputTokens
// come straight from repository.AggregatePricedOutputVolume (current-price
// recomputation, see that function); ProjectedSavingsPerMillionTokensMicros
// is the banner figure: spend x coefficient normalized to one million
// output tokens. The per-million basis is traffic-independent — a
// month-scale extrapolation reads as cents on a lightly-used instance,
// while a unit rate stays meaningful at any volume. PricedRows <
// OutputRows means some output traffic was unpriced and is not in the
// figure.
type ConciseOutputProjection struct {
	Window             ConciseOutputWindow `json:"window"`
	OutputSpendMicros  int64               `json:"output_spend_micros"`
	OutputRows         int64               `json:"output_rows"`
	PricedRows         int64               `json:"priced_rows"`
	PricedOutputTokens int64               `json:"priced_output_tokens"`
	// ProjectedSavingsPerMillionTokensMicros = spend x coefficient over
	// the priced token total, scaled to 1M tokens, in micros.
	ProjectedSavingsPerMillionTokensMicros int64 `json:"projected_savings_per_million_tokens_micros"`
	// Coefficient echoes the factory coefficient behind the figure so the
	// UI renders the rate's basis from the single backend source of truth
	// instead of hard-coding a copy that could drift.
	Coefficient float64 `json:"coefficient"`
}

// GetConciseOutputProjection aggregates the priced output volume for the
// filter and projects it to a per-million-token saving. The window resolves
// on the day-bucket cap like the compress stats, so the banner figure and
// the dashboard below it aggregate the same range for a given filter (the
// window's traffic sets the price weighting). The same figure serves both
// switch states — enabled reads as "with the switch on", disabled as "if
// enabled" — the UI picks the wording.
func (s *AnalyticsService) GetConciseOutputProjection(ctx context.Context, filter *repository.RequestLogFilter, opts AnalyticsOptions, now time.Time) (*ConciseOutputProjection, error) {
	resolveEffectiveRange(filter, opts, BucketDay, now)
	volume, err := repository.AggregatePricedOutputVolume(ctx, s.db, filter)
	if err != nil {
		return nil, err
	}
	perMillion := int64(0)
	if volume.PricedOutputTokens > 0 {
		perMillion = int64(math.Round(
			float64(volume.OutputSpendMicros) * ConciseOutputCoefficient * 1e6 / float64(volume.PricedOutputTokens)))
	}
	return &ConciseOutputProjection{
		Window: ConciseOutputWindow{
			Start: *filter.StartTime,
			End:   *filter.EndTime,
			Days:  windowDays(*filter.StartTime, *filter.EndTime),
		},
		OutputSpendMicros:                      volume.OutputSpendMicros,
		OutputRows:                             volume.OutputRows,
		PricedRows:                             volume.PricedRows,
		PricedOutputTokens:                     volume.PricedOutputTokens,
		ProjectedSavingsPerMillionTokensMicros: perMillion,
		Coefficient:                            ConciseOutputCoefficient,
	}, nil
}

// windowDays renders the span of [start, end) in whole 24h days, floored at
// 1 so a sub-day window still counts as one day of traffic (and a zero-length
// window never divides the projection by zero). Rounded to the NEAREST day
// rather than rounded up: ResolveTimeRange builds calendar windows in the
// client's timezone, and a 7-day window that crosses a DST transition spans
// 167 or 169 actual hours — ceiling that to 8 would shave ~12% off the
// month-scale factor for that window, while nearest keeps it at 7.
func windowDays(start, end time.Time) int {
	days := int(math.Round(end.Sub(start).Hours() / 24))
	if days < 1 {
		return 1
	}
	return days
}
