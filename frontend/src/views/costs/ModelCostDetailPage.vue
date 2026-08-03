<!-- frontend/src/views/costs/ModelCostDetailPage.vue
     Single-model cost detail. The identity is the model name read synchronously
     from the route (no async identity fetch), so the state machine is just
     loading / success / error — an empty name redirects away before any state.
     getCostStats is pinned on model_name, and the breakdown tables split the
     spend across callers and providers (a model split would be a single row,
     the entity itself). -->
<template>
  <div class="common-page">
    <PageHeader class="new-line" :title="title" :description="t('costs.detail.modelDesc')">
      <template #actions>
        <NButton size="small" @click="goLogs">{{ t('costs.detail.viewLogs') }}</NButton>
        <TimeRangeSelect v-model="timeRange" :preset="preset" @update:preset="onPreset" />
      </template>
    </PageHeader>

    <!-- error: aggregate fetch failed. Keep last stats if known but never
         render 0/[] as if it were real data; offer a retry instead. -->
    <EmptyState v-if="state === 'error'" :icon="AlertTriangle" :title="t('costs.detail.loadFailed')">
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

      <div class="section-card">
        <div class="section-card__head">
          <HelpLabel :tip="t('costs.trend.title_tip')">{{ t('costs.trend.title') }}</HelpLabel>
        </div>
        <CostTrendChart :rows="trendRows" />
      </div>

      <div class="section-card  table-card">
        <div class="section-card__head">{{ t('costs.detail.byCaller') }}</div>
        <BreakdownTable
          :rows="stats?.callerRows ?? []"
          dimension="caller"
          @select="onSelect"
        />
      </div>
      <div class="section-card  table-card">
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
import { initialLast7DaysRange, logsRouteWithRange, rangeFromQuery, withRangeQuery } from '../../utils/timeRange'
import { displayMessage } from '../../api/client'
import { getCostStats, type CostStats } from '../../api/costs'
import type { AnalyticsFilter } from '../../api/analytics'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const message = useMessage()

// Identity comes straight from the route — Vue Router already decodes
// params/query once, so a second decodeURIComponent here would mangle any name
// that legitimately contains '%'. Empty / missing name is a terminal no-op:
// bounce to the cost index; we never enter the loading state.
const name = (route.params.name as string) ?? (route.query.name as string)
if (!name) {
  void router.replace('/costs')
}

// Inherit the reporting window when drilled from another analytics view
// (chart/bar/breakdown row); otherwise default to the last 7 days.
const carried = rangeFromQuery(route.query)
const preset = ref<RangePreset>(carried ? 'custom' : 'last7d')
const timeRange = ref<TimeRange>(carried ?? initialLast7DaysRange())
function onPreset(v: RangePreset) {
  preset.value = v
}

// Explicit state machine: loading -> success | error. There is no notfound
// state (no async identity fetch that can 404); error never renders 0/[] as
// data, only the retry affordance.
type State = 'loading' | 'success' | 'error'
const state = ref<State>('loading')
const stats = ref<CostStats | null>(null)

// reloadSeq guards against a stale window reload landing after a newer one and
// overwriting it with the previous range's figures (same pattern the cost
// stats page and the key detail page use).
let reloadSeq = 0

const title = computed(() => t('costs.detail.modelTitle', { name }))

// Trend passes [] when the window has no data so the chart shows its own
// empty state. The analytics backend gap-fills zero rows for missing days, so
// timeRows.length is unreliable for detecting "no traffic".
const trendRows = computed(() =>
  state.value === 'success' && (stats.value?.overview.total_calls ?? 0) === 0
    ? []
    : (stats.value?.timeRows ?? []),
)

async function reload() {
  // Empty name means router.replace('/costs') is already in flight; skip the
  // stats fetch so we never enter the loading state on the way out.
  if (!name) return
  const mySeq = ++reloadSeq
  state.value = 'loading'
  // model_name is the exact identifier — pass it verbatim. Trimming would
  // break names that intentionally carry leading/trailing whitespace, and the
  // list page already treats this as an exact-match query.
  const filter: AnalyticsFilter = {
    start: timeRange.value.start,
    end: timeRange.value.end,
    model_name: name,
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
  if (p.kind === 'caller' && p.apiKeyId != null) {
    router.push(withRangeQuery(`/costs/keys/${p.apiKeyId}`, range.start, range.end))
  } else if (p.kind === 'provider' && p.providerId != null) {
    router.push(withRangeQuery(`/costs/providers/${p.providerId}`, range.start, range.end))
  }
}

function goLogs() {
  // model_name is passed verbatim (exact match); logsRouteWithRange normalizes
  // a cleared range and clamps to the analytics cap.
  router.push(logsRouteWithRange({ model_name: name }, timeRange.value))
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

<style scoped>
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
@media (max-width: 768px) {
  .section-card {
    padding: var(--space-3);
  }
  .section-card.table-card {
    padding: 0;
  }
  .section-card.table-card .section-card__head{
    padding: var(--space-3);
  }
  
}
</style>
