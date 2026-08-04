<!-- frontend/src/views/costs/KeyCostDetailPage.vue
     Single API-key cost detail. Identity-first load: fetch the key (via
     getKeyBudgetRow, which also returns the budget standing), then pin
     getCostStats on api_key_id. Distinct loading / error / success / notfound
     states so an API failure never reads as zero spend. -->
<template>
  <div class="common-page">
    <PageHeader class="new-line"  :title="title" :description="t('costs.detail.keyDesc')">
      <template #actions>
        <NButton size="small" @click="goLogs">{{ t('costs.detail.viewLogs') }}</NButton>
        <TimeRangeSelect v-model="timeRange" :preset="preset" @update:preset="onPreset" />
      </template>
    </PageHeader>

    <!-- notfound: identity lookup returned API_KEY_NOT_FOUND — terminal, no retry. -->
    <EmptyState v-if="state === 'notfound'" :icon="AlertTriangle" :title="t('costs.detail.notFound')" />
    <!-- error: identity or aggregate fetch failed for any other reason. Keep
         last identity/budget if known but never render 0/[] as if it were real
         data; offer a retry instead. -->
    <EmptyState v-else-if="state === 'error'" :icon="AlertTriangle" :title="t('costs.detail.loadFailed')">
      <template #action>
        <NButton size="small" @click="reload">{{ t('costs.detail.retry') }}</NButton>
      </template>
    </EmptyState>

    <!-- loading: initial load or a window change. Never render 0 (initial) or
         stale previous-window data (range change) as if it were current. -->
    <div v-else-if="state === 'loading'" class="detail-loading">
      <NSpin size="medium" />
    </div>

    <!-- success: fresh window data only. -->
    <template v-else>
      <CostOverviewCards :overview="stats?.overview ?? null" />

      <!-- Budget block (key-only). Lifetime counters plus a fixed-window burn
           rate, independent of the header time range. -->
      <div class="section-card">
        <div class="section-card__head">
          <HelpLabel :tip="t('costs.detail.budgetTip')">{{ t('costs.detail.budgetTitle') }}</HelpLabel>
        </div>
        <div class="budget-block">
          <span>
            {{ t('costs.budget.limitColumn') }}:
            <template v-if="budget?.budget_limit_micros == null">{{ t('costs.detail.noLimit') }}</template>
            <template v-else>¥{{ formatMicros(budget.budget_limit_micros) }}</template>
          </span>
          <span>{{ t('costs.budget.spentColumn') }}: ¥{{ formatMicros(budget?.budget_spent_micros ?? 0) }}</span>
          <span>{{ t('costs.detail.exhaust') }}: {{ exhaustLabel }}</span>
        </div>
      </div>

      <div class="section-card">
        <div class="section-card__head">
          <HelpLabel :tip="t('costs.trend.title_tip')">{{ t('costs.trend.title') }}</HelpLabel>
        </div>
        <CostTrendChart :rows="trendRows" />
      </div>

      <div class="section-card table-card">
        <div class="section-card__head">{{ t('costs.detail.byModel') }}</div>
        <BreakdownTable
          :rows="stats?.modelRows ?? []"
          dimension="model"
          @select="onSelect"
        />
      </div>
      <div class="section-card table-card">
        <div class="section-card__head">{{ t('costs.detail.byProvider') }}</div>
        <BreakdownTable
          :rows="stats?.providerRows ?? []"
          dimension="provider"
          @select="onSelect"
        />
      </div>
    </template>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useRoute, useRouter } from 'vue-router'
import { useI18n } from 'vue-i18n'
import { useMessage, NButton, NSpin } from 'naive-ui'
import { AlertTriangle } from '@lucide/vue'
import PageHeader from '../../components/PageHeader.vue'
import HelpLabel from '../../components/HelpLabel.vue'
import EmptyState from '../../components/EmptyState.vue'
import TimeRangeSelect, { type RangePreset, type TimeRange } from '../../components/analytics/TimeRangeSelect.vue'
import CostOverviewCards from '../../components/costs/CostOverviewCards.vue'
import CostTrendChart from '../../components/costs/CostTrendChart.vue'
import BreakdownTable from '../../components/costs/BreakdownTable.vue'
import { formatMicros } from '../../utils/money'
import { computeDaysToExhaust, formatDaysToExhaustLabel } from '../../utils/budget'
import { initialLast7DaysRange, logsRouteWithRange, rangeFromQuery, withRangeQuery } from '../../utils/timeRange'
import { displayMessage, errorCodeOf } from '../../api/client'
import { getCostStats, getKeyBudgetRow, type BudgetRow, type CostStats } from '../../api/costs'
import { modelCostDetailLocation } from '../../utils/modelCostLocation'
import { API_KEY_NOT_FOUND } from '../../api/errcodes'
import type { AnalyticsFilter } from '../../api/analytics'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const message = useMessage()

const keyId = Number(route.params.id)
// Inherit the reporting window when drilled from another analytics view
// (chart/bar/breakdown row); otherwise default to the last 7 days.
const carried = rangeFromQuery(route.query)
const preset = ref<RangePreset>(carried ? 'custom' : 'last7d')
const timeRange = ref<TimeRange>(carried ?? initialLast7DaysRange())
function onPreset(v: RangePreset) {
  preset.value = v
}

// Explicit state machine: loading -> success | error | notfound. error and
// notfound are terminal-ish (retry reloads); they never render 0/[] as data.
type State = 'loading' | 'success' | 'error' | 'notfound'
const state = ref<State>('loading')
const stats = ref<CostStats | null>(null)
const budget = ref<BudgetRow | null>(null)
const ownerLabel = ref('')

// reloadSeq guards against a stale window reload landing after a newer one and
// overwriting it with the previous range's figures (same pattern the cost
// stats page uses).
let reloadSeq = 0

const title = computed(() =>
  t('costs.detail.keyTitle', { name: ownerLabel.value || `#${keyId}` }),
)

// Trend passes [] when the window has no data so the chart shows its own
// empty state. The analytics backend gap-fills zero rows for missing days, so
// timeRows.length is unreliable for detecting "no traffic".
const trendRows = computed(() =>
  state.value === 'success' && (stats.value?.overview.total_calls ?? 0) === 0
    ? []
    : (stats.value?.timeRows ?? []),
)

// exhaustLabel renders the shared days-to-exhaust text (or '—' before the
// budget row has loaded / when the key is uncapped). Label derivation lives in
// formatDaysToExhaustLabel so it cannot drift from the budget table.
const exhaustLabel = computed(() =>
  budget.value ? formatDaysToExhaustLabel(computeDaysToExhaust(budget.value), t) : '—',
)

// Identity + budget come from ONE getKeyBudgetRow call (it fetches the key
// internally and returns owner_label for the header). identityOk guards so a
// TimeRange change does NOT re-fetch the key — only the window-scoped stats.
let identityOk = false

async function reload() {
  const mySeq = ++reloadSeq
  if (!identityOk) {
    try {
      budget.value = await getKeyBudgetRow(keyId)
      // Stale guard on the identity-success write point: a slower earlier
      // reload must not overwrite a newer ownerLabel / identityOk.
      if (mySeq !== reloadSeq) return
      ownerLabel.value = budget.value.owner_label
      identityOk = true
    } catch (err) {
      // Distinguish "key does not exist" (terminal notfound) from any other
      // failure (network / 5xx — show error with retry). errorCodeOf returns
      // undefined for NetworkError / RequestAbortedError, which correctly
      // fall through to the generic error branch.
      if (mySeq !== reloadSeq) return
      if (errorCodeOf(err) === API_KEY_NOT_FOUND) {
        state.value = 'notfound'
      } else {
        message.error(displayMessage(err, t))
        state.value = 'error'
      }
      return
    }
  }
  state.value = 'loading'
  const filter: AnalyticsFilter = {
    start: timeRange.value.start,
    end: timeRange.value.end,
    api_key_id: keyId,
  }
  try {
    const result = await getCostStats(filter)
    if (mySeq !== reloadSeq) return
    stats.value = result
    state.value = 'success'
  } catch (err) {
    if (mySeq !== reloadSeq) return
    // On aggregate failure, drop into the persistent error state — do NOT
    // fall back to 0/[], which would disguise the failure as zero spend.
    stats.value = null
    message.error(displayMessage(err, t))
    state.value = 'error'
  }
}

function onSelect(p: { kind: string; model?: string; providerId?: number; apiKeyId?: number }) {
  const range = { start: timeRange.value.start, end: timeRange.value.end }
  if (p.kind === 'model' && p.model != null) {
    router.push(withRangeQuery(modelCostDetailLocation(p.model), range.start, range.end))
  } else if (p.kind === 'provider' && p.providerId != null) {
    router.push(withRangeQuery(`/costs/providers/${p.providerId}`, range.start, range.end))
  }
}

function goLogs() {
  router.push(logsRouteWithRange({ api_key_id: String(keyId) }, timeRange.value))
}

// First load: for a carried custom range (drilled from another analytics
// view) preset is 'custom' and TimeRangeSelect does NOT emit an initial
// window (it only resolves named presets), so trigger reload explicitly.
// For the default last-7d preset, TimeRangeSelect's mount emit sets timeRange
// and fires the watch below (calling reload here too would double it).
onMounted(() => {
  if (carried) void reload()
})

watch(timeRange, () => { void reload() }, { deep: true })
</script>

<style scoped lang="less">
/* Only padding differs from the global .section-card (these pages use a
   tighter --space-5); bg / border / radius / __head come from global.less. */
.section-card {
  padding: var(--space-5);
}

.detail-loading {
  display: flex;
  align-items: center;
  justify-content: center;
  min-height: 240px;
}

/* Budget block: three lifetime counters laid out as a wrapping row so the
   labels never collide on narrow viewports. */
.budget-block {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-4);
  color: var(--color-text);
}
@media (max-width: @mobile-breakpoint) {
  .section-card {
    padding: var(--space-3);
  }
  .section-card.table-card {
    padding: 0;
    border: 0;
  }
  .section-card.table-card .section-card__head::before{
    content: "";
    display: inline-block;
    width: 4px;
    height: 1.5em;
    border-radius: 2px;
    background: var(--color-primary, #6467f2);
    flex: 0 0 auto;
  }
}
</style>
