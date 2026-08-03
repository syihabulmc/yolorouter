<!-- frontend/src/views/costs/ProviderCostDetailPage.vue
     Single-provider cost detail. Identity-first load: fetch the provider via
     getProvider(id) for the header name, then pin getCostStats on provider_id.
     Distinct loading / error / success / notfound states so an API failure
     never reads as zero spend. Mirrors KeyCostDetailPage minus the budget
     block (providers have no budget) and minus the provider breakdown table
     (that would be a single row — the entity itself). -->
<template>
  <div class="common-page">
    <PageHeader class="new-line" :title="title" :description="t('costs.detail.providerDesc')">
      <template #actions>
        <NButton size="small" @click="goLogs">{{ t('costs.detail.viewLogs') }}</NButton>
        <TimeRangeSelect v-model="timeRange" :preset="preset" @update:preset="onPreset" />
      </template>
    </PageHeader>

    <!-- notfound: identity lookup returned PROVIDER_NOT_FOUND — terminal, no retry. -->
    <EmptyState v-if="state === 'notfound'" :icon="AlertTriangle" :title="t('costs.detail.providerNotFound')" />
    <!-- error: identity or aggregate fetch failed for any other reason. Keep
         last stats if known but never render 0/[] as if it were real data;
         offer a retry instead. -->
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

      <div class="section-card">
        <div class="section-card__head">
          <HelpLabel :tip="t('costs.trend.title_tip')">{{ t('costs.trend.title') }}</HelpLabel>
        </div>
        <CostTrendChart :rows="trendRows" />
      </div>

      <!-- Model + caller splits. The provider split is intentionally absent:
           a single-provider window has only one provider row, so it carries no
           information beyond the overview cards already shown. -->
      <div class="section-card table-card">
        <div class="section-card__head">{{ t('costs.detail.byModel') }}</div>
        <BreakdownTable
          :rows="stats?.modelRows ?? []"
          dimension="model"
          @select="onSelect"
        />
      </div>
      <div class="section-card table-card">
        <div class="section-card__head">{{ t('costs.detail.byCaller') }}</div>
        <BreakdownTable
          :rows="stats?.callerRows ?? []"
          dimension="caller"
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
import { displayMessage, errorCodeOf } from '../../api/client'
import { getCostStats, type CostStats } from '../../api/costs'
import { getProvider } from '../../api/providers'
import { modelCostDetailLocation } from '../../utils/modelCostLocation'
import { PROVIDER_NOT_FOUND } from '../../api/errcodes'
import type { AnalyticsFilter } from '../../api/analytics'

const { t } = useI18n()
const route = useRoute()
const router = useRouter()
const message = useMessage()

const providerId = Number(route.params.id)
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
const providerName = ref('')

// reloadSeq guards against a stale window reload landing after a newer one and
// overwriting it with the previous range's figures. Applied to EVERY write
// point (identity success, identity fail, stats success, stats fail) so a slow
// identity fetch cannot stamp its result over a newer reload either.
let reloadSeq = 0

const title = computed(() =>
  t('costs.detail.providerTitle', { name: providerName.value || `#${providerId}` }),
)

// Trend passes [] when the window has no data so the chart shows its own
// empty state. The analytics backend gap-fills zero rows for missing days, so
// timeRows.length is unreliable for detecting "no traffic".
const trendRows = computed(() =>
  state.value === 'success' && (stats.value?.overview.total_calls ?? 0) === 0
    ? []
    : (stats.value?.timeRows ?? []),
)

// Identity comes from getProvider — providers have no budget counters, so the
// identity fetch only supplies the header name. identityOk guards so a
// TimeRange change does NOT re-fetch the provider — only the window-scoped stats.
let identityOk = false

async function reload() {
  const mySeq = ++reloadSeq
  if (!identityOk) {
    try {
      const provider = await getProvider(providerId)
      // Stale guard on the identity-success write point: a slower earlier
      // reload must not overwrite a newer providerName / identityOk.
      if (mySeq !== reloadSeq) return
      providerName.value = provider.name
      identityOk = true
    } catch (err) {
      // Stale guard on the identity-fail write point too — without it, a slow
      // 404 from an earlier reload could flip a newer successful reload to
      // notfound / error.
      if (mySeq !== reloadSeq) return
      // Distinguish "provider does not exist" (terminal notfound) from any
      // other failure (network / 5xx — show error with retry). errorCodeOf
      // returns undefined for NetworkError / RequestAbortedError, which
      // correctly fall through to the generic error branch.
      if (errorCodeOf(err) === PROVIDER_NOT_FOUND) {
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
    provider_id: providerId,
  }
  try {
    const result = await getCostStats(filter)
    // Stale guard on the stats-success write point.
    if (mySeq !== reloadSeq) return
    stats.value = result
    state.value = 'success'
  } catch (err) {
    // Stale guard on the stats-fail write point.
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
  } else if (p.kind === 'caller' && p.apiKeyId != null) {
    router.push(withRangeQuery(`/costs/keys/${p.apiKeyId}`, range.start, range.end))
  }
}

function goLogs() {
  router.push(logsRouteWithRange({ provider_id: String(providerId) }, timeRange.value))
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
