// frontend/src/utils/analyticsColumns.ts
//
// Shared metric column factories for analytics/report-style tables.
// Extracted so the analytics page and the per-entity cost detail breakdown
// tables share ONE definition of each metric column (label/width/format).
// Each factory takes the component's i18n translator `t` because module
// scope has no access to a component-local useI18n().

import type { DataTableColumns } from 'naive-ui'
import { columnTitle, STATUS_COL_WIDTH } from './columnTitle'
import { formatNumber, formatRate } from './format'
import { formatMicros } from './money'

type Translator = (key: string) => string

// Shape every dimension report row satisfies: the fields the shared metric
// columns render. Individual row types add their own dimension-key column.
export type MetricRow = {
  calls: number
  success_rate: number
  cost_micros: number
  unknown_cost_calls: number
}

export function callsColumn<T extends MetricRow>(t: Translator): DataTableColumns<T>[number] {
  return {
    title: columnTitle(t('analytics.callsColumn'), t('analytics.callsColumn_tip')),
    key: 'calls',
    width: 120,
    align: 'right',
    render: (r: T) => formatNumber(r.calls),
  }
}

export function successRateColumn<T extends MetricRow>(t: Translator): DataTableColumns<T>[number] {
  return {
    title: columnTitle(t('analytics.successRateColumn'), t('analytics.successRateColumn_tip')),
    key: 'success_rate',
    width: STATUS_COL_WIDTH,
    align: 'right',
    render: (r: T) => formatRate(r.success_rate),
  }
}

export function costColumn<T extends MetricRow>(t: Translator): DataTableColumns<T>[number] {
  return {
    title: columnTitle(t('analytics.costColumn'), t('analytics.costColumn_tip')),
    key: 'cost_micros',
    width: 140,
    align: 'right',
    render: (r: T) => `¥${formatMicros(r.cost_micros)}`,
  }
}

export function unknownCostColumn<T extends MetricRow>(t: Translator): DataTableColumns<T>[number] {
  return {
    title: columnTitle(t('analytics.unknownCostColumn'), t('analytics.unknownCostColumn_tip')),
    key: 'unknown_cost_calls',
    width: 140,
    align: 'right',
    render: (r: T) => formatNumber(r.unknown_cost_calls),
  }
}

// tokenColumn is the shared factory for every right-aligned token count
// (input / output / cache write / cache read). i18nKey names both the header
// (`analytics.<i18nKey>`) and its tooltip (`analytics.<i18nKey>_tip`); the
// column key is the row field to render.
export function tokenColumn<T extends MetricRow>(
  t: Translator,
  key: keyof T & string,
  i18nKey: string,
  width = 140,
): DataTableColumns<T>[number] {
  return {
    title: columnTitle(t(`analytics.${i18nKey}`), t(`analytics.${i18nKey}_tip`)),
    key,
    width,
    align: 'right',
    render: (r: T) => formatNumber(r[key] as number),
  }
}

// avgDurationColumn is provider-specific (ProviderReportRow.avg_duration_ms);
// not every dimension has an average latency, so it stays out of MetricRow.
export function avgDurationColumn<T extends { avg_duration_ms: number }>(
  t: Translator,
): DataTableColumns<T>[number] {
  return {
    title: columnTitle(t('analytics.avgDurationColumn'), t('analytics.avgDurationColumn_tip')),
    key: 'avg_duration_ms',
    width: 140,
    align: 'right',
    render: (r: T) => `${r.avg_duration_ms.toFixed(0)}ms`,
  }
}
