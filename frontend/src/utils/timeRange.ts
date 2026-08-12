// frontend/src/utils/timeRange.ts
//
// Shared time-range helpers for dashboard pages that default to a "last 7
// days" window. The default window is computed against the browser's local
// midnight so "today" is meaningful to the user, not the server's UTC day.

import type { RouteLocationRaw } from 'vue-router'
import type { TimeRange } from '../components/analytics/TimeRangeSelect.vue'

/**
 * initialLast7DaysRange returns a [start, end) window covering the last 7
 * calendar days in the browser's timezone: start = local midnight 7 days
 * ago, end = local-midnight "tomorrow" (exclusive). Used as the default
 * filter on the analytics, cost-stats, and cost-optimization dashboards so
 * every page feels consistent on first paint.
 */
export function initialLast7DaysRange(): TimeRange {
  const now = new Date()
  const end = new Date(now.getFullYear(), now.getMonth(), now.getDate() + 1, 0, 0, 0, 0)
  const start = new Date(end)
  start.setDate(start.getDate() - 7)
  return { start: start.toISOString(), end: end.toISOString() }
}

/**
 * rangeFromQuery returns the reporting window carried in a route query (when
 * drilling from another analytics view), or null when the query carries none
 * so the caller can fall back to the default last-7-day preset.
 */
export function rangeFromQuery(
  query: Record<string, unknown>,
): { start: string; end: string } | null {
  const { start, end } = query as { start?: unknown; end?: unknown }
  if (typeof start === 'string' && typeof end === 'string' && start && end) {
    return { start, end }
  }
  return null
}

/**
 * withRangeQuery merges the current reporting window into a drill-down route
 * location so the destination's totals match the row the user clicked. A
 * string location is lifted to an object form; an object location keeps its
 * existing query (e.g. the model dot-segment `?name=`) and adds start/end.
 */
export function withRangeQuery(
  loc: RouteLocationRaw,
  start?: string | null,
  end?: string | null,
): RouteLocationRaw {
  if (!start || !end) return loc
  if (typeof loc === 'string') return { path: loc, query: { start, end } }
  const obj = loc as { path?: string; query?: Record<string, unknown> }
  return { ...obj, query: { ...(obj.query ?? {}), start, end } }
}

/**
 * ANALYTICS_DAY_CAP_DAYS mirrors the analytics backend's day-bucket lookback
 * cap (maxDayLookbackDays): a window wider than this is clamped to [end - cap,
 * end] server-side. clampedRangeStart reproduces that clamp client-side so a
 * request-log deep link matches the (already-clamped) totals the user sees —
 * without it, a >90-day custom range would surface logs outside the window.
 */
export const ANALYTICS_DAY_CAP_DAYS = 90
// DASHBOARD_RANGE_CAP_DAYS mirrors the dashboard service's
// maxDashboardRangeDays: KPI and top-caller aggregation clamp to the final
// 365 days of a wider custom range, and drill-down links must match.
export const DASHBOARD_RANGE_CAP_DAYS = 365
export function clampedRangeStart(start: string, end: string, capDays: number = ANALYTICS_DAY_CAP_DAYS): string {
  const s = new Date(start).getTime()
  const e = new Date(end).getTime()
  if (Number.isNaN(s) || Number.isNaN(e)) return start
  const capMs = capDays * 24 * 60 * 60 * 1000
  if (e - s <= capMs) return start
  // The analytics backend clamps with end.AddDate(0, 0, -cap). Frontend
  // ranges serialize via toISOString (UTC "Z"), so Go parses end in UTC and
  // AddDate subtracts exactly cap UTC days (UTC has no DST). Mirror that with
  // fixed UTC-hour subtraction so the request-log window matches the totals.
  return new Date(e - capMs).toISOString()
}

/**
 * logsRouteWithRange builds the /request-logs route for a "view logs" deep
 * link from a cost detail page. It normalizes a null/cleared range to the
 * server-timezone last-7-days default (matching the analytics backend's own
 * fallback) and clamps the start to the analytics day-bucket cap, so the log
 * list's window always matches the totals the user sees. `filter` carries the
 * entity-specific query key(s), e.g. { api_key_id: '1' } or { model_name }.
 */
export function logsRouteWithRange(
  filter: Record<string, string>,
  range: TimeRange,
): RouteLocationRaw {
  const r = range.start && range.end ? range : initialLast7DaysRange()
  return {
    path: '/request-logs',
    query: { ...filter, start: clampedRangeStart(r.start ?? '', r.end ?? ''), end: r.end ?? '' },
  }
}
