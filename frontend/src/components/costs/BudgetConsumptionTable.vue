<!-- frontend/src/components/costs/BudgetConsumptionTable.vue
     Per-key budget standing (module: budget consumption). Each row pairs a
     key's lifetime spent/limit counters with a consumption bar and a
     days-to-exhaust estimate derived from its recent daily burn rate.

     Two calibers meet here on purpose: spent/limit are LIFETIME cumulative
     (the budget counter never resets), while the burn rate behind
     "days to exhaust" comes from a fixed recent window. The column headers
     label them so the two don't read as the same number. -->
<template>
  <ResponsiveDataTable
    :columns="columns"
    :data="sortedRows"
    :loading="loading"
    :scroll-x="820"
    :row-key="(r: BudgetRow) => r.id"
    :row-props="rowProps"
  >
    <template #empty>
      <EmptyState :icon="Wallet" :title="t('costs.budget.empty')" />
    </template>
  </ResponsiveDataTable>
</template>

<script setup lang="ts">
import { computed, h } from 'vue'
import { callerDisplay } from '../../utils/format'
import { useI18n } from 'vue-i18n'
import { useRouter } from 'vue-router'
import { type DataTableColumns } from 'naive-ui'
import { Wallet } from '@lucide/vue'
import EmptyState from '../EmptyState.vue'
import ResponsiveDataTable from '../common/ResponsiveDataTable.vue'
import { columnTitle, STATUS_COL_WIDTH } from '../../utils/columnTitle'
import { formatMicros } from '../../utils/money'
import {
  computeDaysToExhaust,
  fillPercentOf,
  formatDaysToExhaustLabel,
  levelOf,
  ratioOf,
} from '../../utils/budget'
import type { BudgetRow } from '../../api/costs'

const props = defineProps<{
  rows: BudgetRow[]
  loading: boolean
}>()

const { t } = useI18n()
const router = useRouter()

// Capped keys sort by consumption descending (closest to the cap first, where
// attention is needed); uncapped keys sink to the bottom since they have no
// ratio to rank on.
const sortedRows = computed<BudgetRow[]>(() =>
  [...props.rows].sort((a, b) => {
    const ra = ratioOf(a)
    const rb = ratioOf(b)
    if (ra == null && rb == null) return b.budget_spent_micros - a.budget_spent_micros
    if (ra == null) return 1
    if (rb == null) return -1
    return rb - ra
  }),
)

function rowProps(r: BudgetRow): Record<string, unknown> {
  const ratio = ratioOf(r)
  if (ratio == null) return {}
  const level = levelOf(ratio)
  return level === 'ok' ? {} : { class: `budget-row--${level}` }
}

// renderConsumption draws the fill bar + percentage, or an em dash for
// uncapped keys (nothing to fill against).
function renderConsumption(r: BudgetRow) {
  const ratio = ratioOf(r)
  if (ratio == null) return h('span', { class: 'budget-uncapped' }, t('costs.budget.uncapped'))
  const pct = ratio * 100
  const level = levelOf(ratio)
  return h('div', { class: 'budget-bar' }, [
    h('div', { class: 'budget-bar__track' }, [
      // Clamp the fill at 100% width so an overspent key doesn't overflow the
      // track; the percentage label still shows the true >100% value.
      h('div', {
        class: `budget-bar__fill budget-bar__fill--${level}`,
        style: { width: `${fillPercentOf(ratio)}%` },
      }),
    ]),
    h('span', { class: 'budget-bar__pct' }, `${pct.toFixed(0)}%`),
  ])
}

// renderDaysToExhaust renders the structured DaysToExhaust result. The label
// text comes from the shared formatDaysToExhaustLabel helper; this function
// keeps the per-kind styling (over / muted / soon tint) that the table owns.
function renderDaysToExhaust(r: BudgetRow) {
  const result = computeDaysToExhaust(r)
  if (result.kind === 'overspent') {
    return h('span', { class: 'budget-days--over' }, formatDaysToExhaustLabel(result, t))
  }
  if (result.kind === 'unestimable' || result.kind === 'uncapped') {
    return h('span', { class: 'budget-muted' }, formatDaysToExhaustLabel(result, t))
  }
  // kind === 'days': the "soon" tint is keyed off the raw (unfloored)
  // projection, so a value like 7.5 (label '7') does NOT tint.
  return h('span', { class: result.soon ? 'budget-days--soon' : '' }, formatDaysToExhaustLabel(result, t))
}

const columns = computed<DataTableColumns<BudgetRow>>(() => [
  {
    title: columnTitle(t('costs.budget.callerColumn'), t('costs.budget.callerColumn_tip')),
    key: 'owner_username',
    minWidth: 180,
    render: (r) =>
      h('div', { class: 'budget-caller' }, [
        h(
          'span',
          {
            class: 'budget-caller__label budget-caller__label--link',
            onClick: () => router.push(`/costs/keys/${r.id}`),
          },
          callerDisplay(r.owner_username, r.key_prefix) || t('costs.budget.unnamedKey'),
        ),
      ]),
  },
  {
    title: columnTitle(t('costs.budget.spentColumn'), t('costs.budget.spentColumn_tip')),
    key: 'budget_spent_micros',
    width: 150,
    align: 'right',
    render: (r) => `$${formatMicros(r.budget_spent_micros)}`,
  },
  {
    title: columnTitle(t('costs.budget.limitColumn'), t('costs.budget.limitColumn_tip')),
    key: 'budget_limit_micros',
    width: 150,
    align: 'right',
    render: (r) =>
      r.budget_limit_micros == null || r.budget_limit_micros <= 0
        ? h('span', { class: 'budget-uncapped' }, t('costs.budget.uncapped'))
        : `$${formatMicros(r.budget_limit_micros)}`,
  },
  {
    title: columnTitle(t('costs.budget.consumptionColumn'), t('costs.budget.consumptionColumn_tip')),
    key: 'consumption',
    width: 180,
    render: renderConsumption,
  },
  {
    title: columnTitle(t('costs.budget.exhaustColumn'), t('costs.budget.exhaustColumn_tip')),
    key: 'exhaust',
    width: STATUS_COL_WIDTH,
    align: 'right',
    render: renderDaysToExhaust,
  },
])
</script>

<style scoped>
.budget-caller {
  display: flex;
  flex-direction: column;
  gap: 1px;
}
.budget-caller__label {
  font-weight: 600;
  color: var(--color-text);
}
:deep(.budget-caller__label--link) {
  cursor: pointer;
  color: var(--color-accent);
}
:deep(.budget-caller__label--link:hover) {
  text-decoration: underline;
}
.budget-caller__prefix {
  font-family: var(--font-mono, monospace);
  font-size: var(--text-xs);
  color: var(--color-text-muted);
}

.budget-bar {
  display: flex;
  align-items: center;
  gap: 8px;
}
.budget-bar__track {
  flex: 1;
  height: 6px;
  border-radius: var(--radius-full);
  background: var(--color-border-subtle);
  overflow: hidden;
}
.budget-bar__fill {
  height: 100%;
  border-radius: var(--radius-full);
  transition: width var(--duration-fast) var(--ease-out);
}
.budget-bar__fill--ok {
  background: var(--color-accent);
}
.budget-bar__fill--warn {
  background: var(--color-warning, #e6a23c);
}
.budget-bar__fill--over {
  background: var(--color-danger, #d03050);
}
.budget-bar__pct {
  min-width: 34px;
  text-align: right;
  font-variant-numeric: tabular-nums;
  font-size: var(--text-xs);
  color: var(--color-text-secondary);
}

.budget-uncapped,
.budget-muted {
  color: var(--color-text-muted);
}

.budget-days--soon {
  color: var(--color-warning, #e6a23c);
  font-weight: 600;
}
.budget-days--over {
  color: var(--color-danger, #d03050);
  font-weight: 600;
}

/* Row tints echo the consumption thresholds so a near-cap or overspent key
   stands out even before the eye reaches the bar. On desktop the tint lands on
   the row's cells; on mobile the same class rides on the stacked card. */
:deep(.budget-row--warn td),
:deep(.rdt-card.budget-row--warn) {
  background: color-mix(in srgb, var(--color-warning, #e6a23c) 8%, transparent);
}
:deep(.budget-row--over td),
:deep(.rdt-card.budget-row--over) {
  background: color-mix(in srgb, var(--color-danger, #d03050) 9%, transparent);
}
</style>
