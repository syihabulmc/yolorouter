<!-- frontend/src/views/costs/CostStatsPage.vue
     Cost view — a money-first read of spend and budget standing, aimed at the
     "how much, is it worth it, who's spending, will anything blow its cap"
     questions. It composes existing analytics + api-key data rather than a
     dedicated endpoint.

     Two time calibers coexist by design: the overview cards, trend, breakdown
     and top-spenders all follow the header time range, while the budget table
     reads the LIFETIME budget counters (spent/limit never reset) and a fixed
     recent burn rate. The section labels keep "this period" and "cumulative"
     from reading as the same number. -->
<template>
  <div class="cost-stats-page">
    <PageHeader :eyebrow="t('costs.eyebrow')" :title="t('costs.pageTitle')" :description="t('costs.pageDescription')">
      <template #actions>
        <TimeRangeSelect v-model="timeRange" :preset="preset" @update:preset="onPresetChange" />
      </template>
    </PageHeader>

    <!-- Module: overview cards. Period spend + cache economics follow the
         header range; total/remaining budget are the cumulative counters. -->
    <div class="metric-row">
      <div class="metric">
        <div class="metric__label">
          <HelpLabel :tip="t('costs.overview.spend_tip')">{{ t('costs.overview.spend') }}</HelpLabel>
        </div>
        <div class="metric__value">¥{{ formatMicros(stats?.overview.cost_micros ?? 0) }}</div>
        <div v-if="(stats?.overview.unknown_cost_calls ?? 0) > 0" class="metric__sub">
          {{ t('costs.overview.unknownCostSub', { n: stats?.overview.unknown_cost_calls ?? 0 }) }}
        </div>
      </div>

      <div class="metric">
        <div class="metric__label">
          <HelpLabel :tip="t('costs.overview.cacheSaved_tip')">{{ t('costs.overview.cacheSaved') }}</HelpLabel>
        </div>
        <div class="metric__value" :class="{ 'metric__value--negative': netCacheSaved < 0 }">
          ¥{{ formatMicros(netCacheSaved) }}
        </div>
        <div class="metric__sub metric__sub--split">
          <span class="metric__chip metric__chip--up">
            {{ t('costs.overview.cacheReadSaved') }} ¥{{ formatMicros(stats?.overview.cache_read_saved_micros ?? 0) }}
          </span>
          <span class="metric__chip metric__chip--down">
            {{ t('costs.overview.cacheWriteExtra') }} ¥{{ formatMicros(stats?.overview.cache_write_extra_micros ?? 0) }}
          </span>
        </div>
      </div>

      <div class="metric">
        <div class="metric__label">
          <HelpLabel :tip="t('costs.overview.budgetTotal_tip')">{{ t('costs.overview.budgetTotal') }}</HelpLabel>
        </div>
        <div class="metric__value">¥{{ formatMicros(budgetTotalMicros) }}</div>
        <div class="metric__sub">{{ t('costs.overview.cappedKeysSub', { n: cappedKeyCount }) }}</div>
      </div>

      <div class="metric">
        <div class="metric__label">
          <HelpLabel :tip="t('costs.overview.budgetRemaining_tip')">{{ t('costs.overview.budgetRemaining') }}</HelpLabel>
        </div>
        <div class="metric__value">¥{{ formatMicros(budgetRemainingMicros) }}</div>
      </div>
    </div>

    <!-- Module: spend trend -->
    <div class="section-card">
      <div class="section-card__head">
        <HelpLabel :tip="t('costs.trend.title_tip')">{{ t('costs.trend.title') }}</HelpLabel>
      </div>
      <CostTrendChart :rows="stats?.timeRows ?? []" />
    </div>

    <!-- Module: cost breakdown + top spenders, side by side -->
    <div class="split-row">
      <div class="section-card">
        <div class="section-card__head">
          <HelpLabel :tip="t('costs.breakdown.title_tip')">{{ t('costs.breakdown.title') }}</HelpLabel>
        </div>
        <CostBreakdownChart :provider-rows="stats?.providerRows ?? []" :model-rows="stats?.modelRows ?? []" />
      </div>
      <div class="section-card">
        <div class="section-card__head">
          <HelpLabel :tip="t('costs.topSpenders.title_tip')">{{ t('costs.topSpenders.title') }}</HelpLabel>
        </div>
        <TopSpendersChart :rows="stats?.callerRows ?? []" />
      </div>
    </div>

    <!-- Module: budget consumption table -->
    <div class="section-card">
      <div class="section-card__head">
        <HelpLabel :tip="t('costs.budget.title_tip')">{{ t('costs.budget.title') }}</HelpLabel>
      </div>
      <BudgetConsumptionTable :rows="budgetRows" :loading="budgetLoading" />
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { useMessage } from 'naive-ui'
import PageHeader from '../../components/PageHeader.vue'
import HelpLabel from '../../components/HelpLabel.vue'
import TimeRangeSelect, {
  type RangePreset,
  type TimeRange,
} from '../../components/analytics/TimeRangeSelect.vue'
import CostTrendChart from '../../components/costs/CostTrendChart.vue'
import CostBreakdownChart from '../../components/costs/CostBreakdownChart.vue'
import TopSpendersChart from '../../components/costs/TopSpendersChart.vue'
import BudgetConsumptionTable from '../../components/costs/BudgetConsumptionTable.vue'
import { formatMicros } from '../../utils/money'
import { initialLast7DaysRange } from '../../utils/timeRange'
import { displayMessage } from '../../api/client'
import { getBudgetRows, getCostStats, type BudgetRow, type CostStats } from '../../api/costs'
import type { AnalyticsFilter } from '../../api/analytics'

const { t } = useI18n()
const message = useMessage()

// === Time range (drives the window-scoped modules only) ===================

const preset = ref<RangePreset>('last7d')
const timeRange = ref<TimeRange>(initialLast7DaysRange())

function onPresetChange(v: RangePreset) {
  preset.value = v
}

// === Window-scoped stats ==================================================

const stats = ref<CostStats | null>(null)

// reloadSeq guards against a stale window reload landing after a newer one and
// overwriting it with the previous range's figures (same pattern the usage
// report uses).
let reloadSeq = 0

async function loadStats() {
  const mySeq = ++reloadSeq
  const filter: AnalyticsFilter = { start: timeRange.value.start, end: timeRange.value.end }
  // Clear immediately so a failed reload under a new range can't leave stale
  // money on screen.
  stats.value = null
  try {
    const result = await getCostStats(filter)
    if (mySeq !== reloadSeq) return
    stats.value = result
  } catch (err) {
    if (mySeq !== reloadSeq) return
    message.error(displayMessage(err, t))
  }
}

const netCacheSaved = computed(
  () => (stats.value?.overview.cache_read_saved_micros ?? 0) - (stats.value?.overview.cache_write_extra_micros ?? 0),
)

// === Budget rows (filter-independent: lifetime counters + fixed burn rate) =

const budgetRows = ref<BudgetRow[]>([])
const budgetLoading = ref(false)

async function loadBudget() {
  budgetLoading.value = true
  try {
    budgetRows.value = await getBudgetRows()
  } catch (err) {
    message.error(displayMessage(err, t))
  } finally {
    budgetLoading.value = false
  }
}

// Total configured budget = sum of capped keys' limits (uncapped keys have no
// ceiling to add). Remaining subtracts each capped key's lifetime spend,
// floored at 0 so an overspent key doesn't drag the pool negative.
const budgetTotalMicros = computed(() =>
  budgetRows.value.reduce((sum, r) => sum + (r.budget_limit_micros ?? 0), 0),
)
const budgetRemainingMicros = computed(() =>
  budgetRows.value.reduce((sum, r) => {
    if (r.budget_limit_micros == null || r.budget_limit_micros <= 0) return sum
    return sum + Math.max(r.budget_limit_micros - r.budget_spent_micros, 0)
  }, 0),
)
const cappedKeyCount = computed(
  () => budgetRows.value.filter((r) => r.budget_limit_micros != null && r.budget_limit_micros > 0).length,
)

// === Lifecycle ============================================================

// Budget rows are filter-independent, so they load once on mount. Window-scoped
// stats are deliberately NOT loaded here: TimeRangeSelect resolves and emits its
// range synchronously on mount, which sets timeRange and fires the watch below —
// loading stats from onMounted too would double every request on first paint.
// loadBudget handles its own errors, so no outer .catch is needed.
onMounted(() => {
  void loadBudget()
})

// The window-scoped stats load whenever the range changes — including the
// initial range TimeRangeSelect emits on mount, which is what performs the
// first load. loadStats handles its own errors, so no .catch is needed.
watch(
  timeRange,
  () => {
    void loadStats()
  },
  { deep: true },
)
</script>

<style scoped>
.cost-stats-page {
  display: flex;
  flex-direction: column;
  gap: var(--space-6);
}

.metric-row {
  grid-template-columns: repeat(4, 1fr);
}

.metric__value--negative {
  color: var(--color-danger, #d03050);
}

.metric__sub--split {
  display: flex;
  flex-wrap: wrap;
  gap: 4px 10px;
}
.metric__chip--up {
  color: var(--color-success, #18a058);
}
.metric__chip--down {
  color: var(--color-text-secondary);
}

.section-card {
  padding: var(--space-5);
  background: var(--color-surface);
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-lg);
}

.section-card__head {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  margin-bottom: var(--space-4);
  font-weight: 700;
  color: var(--color-text);
}

.split-row {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: var(--space-6);
}

@media (max-width: 1100px) {
  .metric-row {
    grid-template-columns: repeat(2, 1fr);
  }
  .split-row {
    grid-template-columns: 1fr;
  }
}

@media (max-width: 640px) {
  .metric-row {
    grid-template-columns: 1fr;
  }
}
</style>
