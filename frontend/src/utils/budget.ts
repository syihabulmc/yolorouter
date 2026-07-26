// frontend/src/utils/budget.ts
//
// Budget standing math shared by the cost-stats budget table and the
// per-API-key cost detail page. Extracted from BudgetConsumptionTable.vue
// so the two pages cannot drift on threshold / clamp / rounding rules.

// The burn-rate lookback (days) behind "days to exhaust". This is the single
// source; api/costs.ts imports it for its budget helpers.
export const BURN_RATE_WINDOW_DAYS = 7

// Thresholds as fractions of the limit. Approaching the cap flags "warn";
// at/above 100% the key has overspent.
export const WARN_RATIO = 0.8
export const OVER_RATIO = 1.0

// BudgetRatio is spent/limit, or null when the key is uncapped (no limit to
// measure consumption against).
export type BudgetRatio = number | null

export type BudgetLevel = 'ok' | 'warn' | 'over' | 'uncapped'

// ratioOf returns spent/limit, or null when the key is uncapped (no limit to
// measure consumption against).
export function ratioOf(r: {
  budget_limit_micros: number | null
  budget_spent_micros: number
}): BudgetRatio {
  if (r.budget_limit_micros == null || r.budget_limit_micros <= 0) return null
  return r.budget_spent_micros / r.budget_limit_micros
}

// levelOf maps a consumption ratio to its severity tier — the single place the
// WARN/OVER thresholds live, so the row tint, bar fill, and days-to-exhaust
// label can't drift apart. Uncapped (null) maps to its own tier.
export function levelOf(ratio: BudgetRatio): BudgetLevel {
  if (ratio == null) return 'uncapped'
  if (ratio >= OVER_RATIO) return 'over'
  if (ratio >= WARN_RATIO) return 'warn'
  return 'ok'
}

// fillPercentOf returns the consumption bar width clamped to [0,100] so an
// overspent key does not overflow the track; the percentage label still shows
// the true >100% value. Uncapped keys get width 0 (the track is hidden).
export function fillPercentOf(ratio: BudgetRatio): number {
  if (ratio == null) return 0
  return Math.min(Math.max(ratio * 100, 0), 100)
}

// DaysToExhaust is the structured projection of remaining budget ÷ recent
// daily spend. The page renders the localized label from `kind`; `days` and
// `belowOne` carry the formatted day count for the `days` kind.
export interface DaysToExhaust {
  kind: 'days' | 'overspent' | 'unestimable' | 'uncapped'
  // For kind === 'days': Math.floor(remaining / daily_avg) when that is >= 1.
  days?: number
  // For kind === 'days' with a sub-1 remainder; the page renders "<1".
  belowOne?: boolean
  // True when the RAW projection is within the "soon" warning window
  // (days <= 7 before flooring), so callers can tint the cell without
  // redoing the raw-float math. Keyed off the unfloored value to match
  // the original table behavior exactly.
  soon?: boolean
}

// computeDaysToExhaust projects remaining budget ÷ recent daily spend into a
// structured result. The order of branches matches the original table render:
// uncapped → overspent → zero burn → day count (with sub-1 collapsed to a
// belowOne flag).
export function computeDaysToExhaust(r: {
  budget_limit_micros: number | null
  budget_spent_micros: number
  daily_avg_micros: number
}): DaysToExhaust {
  const ratio = ratioOf(r)
  if (ratio == null) return { kind: 'uncapped' }
  if (levelOf(ratio) === 'over') return { kind: 'overspent' }
  if (r.daily_avg_micros <= 0) return { kind: 'unestimable' }
  const remaining = (r.budget_limit_micros ?? 0) - r.budget_spent_micros
  const days = remaining / r.daily_avg_micros
  if (days >= 1) return { kind: 'days', days: Math.floor(days), soon: days <= 7 }
  return { kind: 'days', belowOne: true, soon: true }
}

// formatDaysToExhaustLabel renders the localized days-to-exhaust text shared
// by the key cost detail page and the budget consumption table. Callers keep
// their own styling (muted / soon-tint / plain); only the label text is shared.
export function formatDaysToExhaustLabel(
  r: DaysToExhaust,
  t: (key: string, args?: Record<string, unknown>) => string,
): string {
  if (r.kind === 'overspent') return t('costs.budget.overspent')
  if (r.kind === 'uncapped' || r.kind === 'unestimable') return '—'
  return t('costs.budget.daysValue', { n: r.belowOne ? '<1' : String(r.days ?? 0) })
}
