// frontend/src/utils/timeRange.ts
//
// Shared time-range helpers for dashboard pages that default to a "last 7
// days" window. The default window is computed against the browser's local
// midnight so "today" is meaningful to the user, not the server's UTC day.

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
