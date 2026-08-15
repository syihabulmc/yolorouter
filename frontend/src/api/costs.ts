// frontend/src/api/costs.ts
//
// Cost view data layer. The cost view is a money-first re-slice of data the
// analytics and api-key endpoints already expose, so this module composes
// those existing endpoints rather than adding a dedicated backend route. The
// only backend addition it depends on is the cache-economics pair on the
// analytics overview (cache_read_saved_micros / cache_write_extra_micros).

import {
  getAnalyticsOverview,
  getAnalyticsReport,
  type AnalyticsFilter,
  type CallerReportRow,
  type ModelReportRow,
  type OverviewRow,
  type ProviderReportRow,
  type TimeReportRow,
} from './analytics'
import { getAPIKey, listAPIKeys, type APIKey } from './apiKeys'
import { BURN_RATE_WINDOW_DAYS } from '../utils/budget'

// CostStats bundles every window-scoped figure the page renders from the
// user-selected time range: the spend/savings overview (module: overview
// cards), the daily trend (trend chart), the provider/model split (breakdown
// donut) and the per-caller spend (top spenders). All four come from the same
// filter, so they're fetched together.
export interface CostStats {
  overview: OverviewRow
  timeRows: TimeReportRow[]
  providerRows: ProviderReportRow[]
  modelRows: ModelReportRow[]
  callerRows: CallerReportRow[]
}

// getCostStats fetches every window-scoped figure in parallel. bucket is
// forwarded to the daily trend (dimension=time) and, per the analytics
// contract, also caps the overview's range to match.
// includeProvider controls the provider split: the provider dimension is
// admin-only on the backend (it exposes upstream topology), so a member
// page must skip that request instead of letting the whole Promise.all
// reject on its 403.
export async function getCostStats(filter: AnalyticsFilter, includeProvider = true): Promise<CostStats> {
  const [overview, timeReport, providerReport, modelReport, callerReport] = await Promise.all([
    getAnalyticsOverview('day', filter),
    getAnalyticsReport('time', 'day', filter),
    includeProvider ? getAnalyticsReport('provider', 'day', filter) : Promise.resolve(null),
    getAnalyticsReport('model', 'day', filter),
    getAnalyticsReport('caller', 'day', filter),
  ])
  return {
    overview,
    timeRows: (timeReport.rows as TimeReportRow[]) ?? [],
    providerRows: (providerReport?.rows as ProviderReportRow[]) ?? [],
    modelRows: (modelReport.rows as ModelReportRow[]) ?? [],
    callerRows: (callerReport.rows as CallerReportRow[]) ?? [],
  }
}

// BudgetRow is one api key's budget standing for the consumption table. spent
// / limit are the lifetime cumulative counters (limit null = uncapped);
// daily_avg_micros is this key's mean daily spend over a FIXED recent window
// (independent of the page's time filter) so "days to exhaust" reflects the
// current burn rate rather than the selected reporting period.
export interface BudgetRow {
  id: number
  owner_username: string
  key_prefix: string
  status: number
  display_status: string
  budget_limit_micros: number | null
  budget_spent_micros: number
  daily_avg_micros: number
}

// BUDGET_KEYS_PAGE_SIZE is the api-keys list page-size ceiling. getAllAPIKeys
// walks every page at this size, so the budget overview aggregates (total /
// remaining / capped count) reflect the FULL key population — a subset would
// silently understate those money figures once a deployment passes one page.
const BUDGET_KEYS_PAGE_SIZE = 200

// getAllAPIKeys returns every api key, not just the first page. It reads page 1
// to learn the total, then fetches the remaining pages concurrently. Keeping
// the summary complete matters more than the request count here — the overview
// cards present these as authoritative totals.
async function getAllAPIKeys(userId?: number | null): Promise<APIKey[]> {
  const first = await listAPIKeys({ q: '', status: '', userId, page: 1, pageSize: BUDGET_KEYS_PAGE_SIZE })
  if (first.total <= first.list.length) return first.list
  const pageCount = Math.ceil(first.total / BUDGET_KEYS_PAGE_SIZE)
  const rest = await Promise.all(
    Array.from({ length: pageCount - 1 }, (_, i) =>
      listAPIKeys({ q: '', status: '', userId, page: i + 2, pageSize: BUDGET_KEYS_PAGE_SIZE }),
    ),
  )
  return rest.reduce((acc, page) => acc.concat(page.list), first.list)
}

// toBudgetRow assembles a BudgetRow from an APIKey and its pre-computed daily
// average micros. Shared by getBudgetRows (full-key walk) and getKeyBudgetRow
// (single-key) so the column mapping lives in exactly one place.
function toBudgetRow(key: APIKey, dailyAvgMicros: number): BudgetRow {
  return {
    id: key.id,
    owner_username: key.owner_username,
    key_prefix: key.key_prefix,
    status: key.status,
    display_status: key.display_status,
    budget_limit_micros: key.budget_limit_micros,
    budget_spent_micros: key.budget_spent_micros,
    daily_avg_micros: dailyAvgMicros,
  }
}

// getBudgetRows merges each api key's lifetime budget counters with its recent
// daily burn rate. The burn rate comes from a caller report over a fixed
// BURN_RATE_WINDOW_DAYS window ending now — deliberately NOT the page filter,
// so the "days to exhaust" figure tracks current spend velocity.
export async function getBudgetRows(userId?: number | null): Promise<BudgetRow[]> {
  // A now-anchored rolling window: exactly BURN_RATE_WINDOW_DAYS × 24h ending
  // at the current instant, so dividing the window's spend by the day count
  // gives a true daily rate. Anchoring the end at tomorrow's midnight would put
  // it in the future for most of the day, leaving the window with one partial
  // day while still dividing by the full count — understating a steady spend
  // and inflating "days to exhaust".
  const end = new Date()
  const start = new Date(end)
  start.setDate(start.getDate() - BURN_RATE_WINDOW_DAYS)
  const burnFilter: AnalyticsFilter = { start: start.toISOString(), end: end.toISOString(), user_id: userId ?? null }

  const [keys, callerReport] = await Promise.all([
    getAllAPIKeys(userId),
    getAnalyticsReport('caller', 'day', burnFilter),
  ])

  // Per-key recent spend, keyed by api_key_id. The NULL-caller bucket (auth
  // failed before routing) has no key to attribute to, so it's skipped.
  const recentSpend = new Map<number, number>()
  for (const row of (callerReport.rows as CallerReportRow[]) ?? []) {
    if (row.api_key_id != null) recentSpend.set(row.api_key_id, row.cost_micros)
  }

  return keys.map((k: APIKey) =>
    toBudgetRow(k, (recentSpend.get(k.id) ?? 0) / BURN_RATE_WINDOW_DAYS),
  )
}

// getKeyBudgetRow returns one api key's budget standing for the detail page:
// lifetime limit/spent (from the key) merged with a fixed
// BURN_RATE_WINDOW_DAYS burn rate (a scoped caller report), identical in
// spirit to getBudgetRows but for a single key — no full-key walk. The filter
// scopes the caller report to this key so the burn rate reflects only its own
// recent spend.
export async function getKeyBudgetRow(id: number): Promise<BudgetRow> {
  const end = new Date()
  const start = new Date(end)
  start.setDate(start.getDate() - BURN_RATE_WINDOW_DAYS)
  const burnFilter: AnalyticsFilter = {
    start: start.toISOString(),
    end: end.toISOString(),
    api_key_id: id,
  }

  const [key, callerReport] = await Promise.all([
    getAPIKey(id),
    getAnalyticsReport('caller', 'day', burnFilter),
  ])
  const row = ((callerReport.rows as CallerReportRow[]) ?? []).find((r) => r.api_key_id === id)
  const daily_avg_micros = (row?.cost_micros ?? 0) / BURN_RATE_WINDOW_DAYS
  return toBudgetRow(key, daily_avg_micros)
}
