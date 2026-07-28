<!-- frontend/src/views/costs/CostOptimizationPage.vue
     Data-first cost-optimization dashboard. Leads with savings metrics and
     breakdowns (by API key / model / provider / day), with the two global
     optimization switches (custom system prompt + input compression) behind
     a settings modal reachable from the page header and CTA banner.
     The CTA banner surfaces how many optimizations remain off; when both
     are on it shows a quiet all-clear.

     Uses the same reloadSeq stale-guard as AnalyticsPage so rapid filter
     changes never leave stale financial data on screen. The settings-enabled
     GETs run in parallel on mount and gate the CTA banner so it never flashes
     a false "off" state before the real values arrive. -->
<template>
  <div class="cost-optimization-page">
    <PageHeader
      :eyebrow="t('costOptimization.eyebrow')"
      :title="t('costOptimization.title')"
      :description="t('costOptimization.pageDescription')"
    >
      <template #actions>
        <TimeRangeSelect v-model="timeRange" :preset="preset" @update:preset="onPresetChange" />
        <NButton @click="settingsShow = true">
          <template #icon><Settings :size="16" /></template>
          {{ t('costOptimization.settingsAction') }}
        </NButton>
      </template>
    </PageHeader>

    <!-- CTA banner: surfaces how many optimizations remain off. -->
    <div v-if="settingsLoaded" class="cta-banner section-card">
      <div v-if="allOn" class="cta-banner__quiet">
        <CheckCircle2 :size="20" class="cta-banner__icon cta-banner__icon--ok" />
        <span>{{ t('costOptimization.ctaAllOn') }}</span>
      </div>
      <div v-else class="cta-banner__main">
        <div class="cta-banner__lead">
          <Sparkles :size="20" class="cta-banner__icon" />
          <span>{{ t('costOptimization.ctaNotAllOn', { n: offCount }) }}</span>
        </div>
        <div class="cta-banner__pills">
          <span class="status-pill" :class="cspEnabled ? 'status-pill--on' : 'status-pill--off'">
            {{ t('costOptimization.cspTitle') }} · {{ cspEnabled ? t('costOptimization.statusOn') : t('costOptimization.statusOff') }}
          </span>
          <span class="status-pill" :class="icEnabled ? 'status-pill--on' : 'status-pill--off'">
            {{ t('costOptimization.inputCompression.title') }} · {{ icEnabled ? t('costOptimization.statusOn') : t('costOptimization.statusOff') }}
          </span>
        </div>
        <NButton type="primary" size="small" @click="settingsShow = true">
          {{ t('costOptimization.ctaAction') }}
        </NButton>
      </div>
    </div>

    <!-- KPI metric tiles. All cost figures are ESTIMATED (see tips). -->
    <div class="metric-row">
      <div class="metric">
        <div class="metric__label">
          <HelpLabel :tip="t('costOptimization.metricTokensSaved_tip')">{{ t('costOptimization.metricTokensSaved') }}</HelpLabel>
        </div>
        <div class="metric__value">{{ formatNumber(totals.tokens_saved) }}</div>
      </div>
      <div class="metric">
        <div class="metric__label">
          <HelpLabel :tip="t('costOptimization.metricCostSaved_tip')">{{ t('costOptimization.metricCostSaved') }}</HelpLabel>
        </div>
        <div class="metric__value">¥{{ formatMicros(totals.cost_saved_micros,2) }}</div>
      </div>
      <div class="metric">
        <div class="metric__label">
          <HelpLabel :tip="t('costOptimization.metricCompressRate_tip')">{{ t('costOptimization.metricCompressRate') }}</HelpLabel>
        </div>
        <div class="metric__value">{{ formatRate(compressRate) }}</div>
      </div>
      <div class="metric">
        <div class="metric__label">
          <HelpLabel :tip="t('costOptimization.metricCompressedCalls_tip')">{{ t('costOptimization.metricCompressedCalls') }}</HelpLabel>
        </div>
        <div class="metric__value">{{ formatNumber(totals.compressed_calls) }}</div>
      </div>
      <div class="metric">
        <div class="metric__label">
          <HelpLabel :tip="t('costOptimization.metricAvgSaved_tip')">{{ t('costOptimization.metricAvgSaved') }}</HelpLabel>
        </div>
        <div class="metric__value">{{ formatNumber(avgSaved) }}</div>
      </div>
    </div>

    <!-- Dimension cards: four line charts (daily / API key / model / provider)
         in a 2x2 grid. All four breakdowns come from one stats call, so every
         card renders from the same stats ref without per-card reload. -->
    <div class="chart-grid">
      <div class="section-card">
        <div class="section-card__head">
          <HelpLabel :tip="t('costOptimization.dimDaily_tip')">{{ t('costOptimization.dimDaily') }}</HelpLabel>
        </div>
        <CompressLineChart
          :labels="dailyLabels"
          :values="dailyTokensSaved"
          :format-value="formatNumber"
          show-average
          :empty-text="t('costOptimization.noData')"
        />
      </div>
      <div class="section-card">
        <div class="section-card__head">
          <HelpLabel :tip="t('costOptimization.dimApiKey_tip')">{{ t('costOptimization.dimApiKey') }}</HelpLabel>
        </div>
        <CompressLineChart
          :labels="apiKeyLabels"
          :values="apiKeyTokensSaved"
          :format-value="formatNumber"
          :empty-text="t('costOptimization.noData')"
        />
      </div>
      <div class="section-card">
        <div class="section-card__head">
          <HelpLabel :tip="t('costOptimization.dimModel_tip')">{{ t('costOptimization.dimModel') }}</HelpLabel>
        </div>
        <CompressLineChart
          :labels="modelLabels"
          :values="modelTokensSaved"
          :format-value="formatNumber"
          :empty-text="t('costOptimization.noData')"
        />
      </div>
      <div class="section-card">
        <div class="section-card__head">
          <HelpLabel :tip="t('costOptimization.dimProvider_tip')">{{ t('costOptimization.dimProvider') }}</HelpLabel>
        </div>
        <CompressLineChart
          :labels="providerLabels"
          :values="providerTokensSaved"
          :format-value="formatNumber"
          :empty-text="t('costOptimization.noData')"
        />
      </div>
    </div>

    <!-- Breakdown: compressor hits + skip reasons side by side. -->
    <div class="breakdown-row">
      <div class="section-card">
        <div class="section-card__head">
          <HelpLabel :tip="t('costOptimization.compressorsTitle_tip')">{{ t('costOptimization.compressorsTitle') }}</HelpLabel>
        </div>
        <NDataTable
          :columns="compressorColumns"
          :data="compressorRows"
          :loading="loading"
          :bordered="false"
          :single-line="false"
          :row-key="(r: CompressorHitRow) => r.name"
          size="small"
        >
          <template #empty>
            <EmptyState :icon="BarChart3" :title="t('costOptimization.noData')" />
          </template>
        </NDataTable>
      </div>
      <div class="section-card">
        <div class="section-card__head">
          <HelpLabel :tip="t('costOptimization.skipReasonTitle_tip')">{{ t('costOptimization.skipReasonTitle') }}</HelpLabel>
        </div>
        <NDataTable
          :columns="skipReasonColumns"
          :data="skipReasonRows"
          :loading="loading"
          :bordered="false"
          :single-line="false"
          :row-key="(r: CompressSkipReasonRow) => r.skip_reason || '__ok__'"
          size="small"
        >
          <template #empty>
            <EmptyState :icon="BarChart3" :title="t('costOptimization.noData')" />
          </template>
        </NDataTable>
      </div>
    </div>

    <OptimizationSettingsModal v-model:show="settingsShow" @saved="onSettingsSaved" />
  </div>
</template>

<script setup lang="ts">
import { computed, h, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NButton, NDataTable, useMessage, type DataTableColumns } from 'naive-ui'
import { BarChart3, CheckCircle2, Settings, Sparkles } from '@lucide/vue'
import PageHeader from '../../components/PageHeader.vue'
import EmptyState from '../../components/EmptyState.vue'
import HelpLabel from '../../components/HelpLabel.vue'
import TimeRangeSelect, { type RangePreset, type TimeRange } from '../../components/analytics/TimeRangeSelect.vue'
import { initialLast7DaysRange } from '../../utils/timeRange'
import { formatMicros } from '../../utils/money'
import { formatNumber, formatRate } from '../../utils/format'
import { displayMessage } from '../../api/client'
import OptimizationSettingsModal from '../../components/costs/OptimizationSettingsModal.vue'
import CompressLineChart from '../../components/costs/CompressLineChart.vue'
import { SKIP_REASON_KEYS } from '../../utils/compressSkipReason'
import { getCustomSystemPrompt, getInputCompression } from '../../api/systemSettings'
import {
  getCompressStats,
  type AnalyticsFilter,
  type CompressSkipReasonRow,
  type CompressStatsResult,
  type CompressorHitRow,
} from '../../api/analytics'

const { t } = useI18n()
const message = useMessage()

// === Settings modal visibility ============================================
const settingsShow = ref(false)

// === Settings-enabled state (drives the CTA banner) =======================
// Both default to false; settingsLoaded stays false until both GETs resolve
// so the CTA banner doesn't flash a false "not all on" before the real
// values arrive. On error we still flip settingsLoaded so the CTA shows —
// the safe fallback is to prompt the user to check settings.
const cspEnabled = ref(false)
const icEnabled = ref(false)
const settingsLoaded = ref(false)

async function loadSettingsEnabled() {
  try {
    const [csp, ic] = await Promise.all([getCustomSystemPrompt(), getInputCompression()])
    cspEnabled.value = csp.enabled
    icEnabled.value = ic.enabled
  } catch {
    // Leave defaults (both false). The CTA will prompt the user to open
    // settings, which is the safest fallback when we can't read state.
  } finally {
    settingsLoaded.value = true
  }
}

const allOn = computed(() => cspEnabled.value && icEnabled.value)
const offCount = computed(() => Number(!cspEnabled.value) + Number(!icEnabled.value))

// === Time range state =====================================================
// Default window = last 7 days, shared with the other dashboards via
// utils/timeRange.ts so every page opens on the same window.
const preset = ref<RangePreset>('last7d')
const timeRange = ref<TimeRange>(initialLast7DaysRange())

// === Stats state ==========================================================
const stats = ref<CompressStatsResult | null>(null)
const loading = ref(false)

// Totals accessor: returns zeros when stats hasn't loaded yet so the metric
// tiles render "0" rather than nothing during the initial fetch.
const totals = computed(() => ({
  tokens_saved: stats.value?.totals.tokens_saved ?? 0,
  cost_saved_micros: stats.value?.totals.cost_saved_micros ?? 0,
  compressed_calls: stats.value?.totals.compressed_calls ?? 0,
  total_estimated_tokens: stats.value?.totals.total_estimated_tokens ?? 0,
}))

// compressRate = tokens_saved / (sent + saved) over the filtered window.
// total_estimated_tokens sums the COMPRESSED (post-compression) input volume
// the upstream actually saw; tokens_saved is the char-delta estimate of what
// compression removed. The denominator must therefore be the estimated
// pre-compression volume = sent + saved, otherwise a high-ratio request
// (saved > sent) pushes the rate past 100%. Clamped to [0, 1] defensively.
const compressRate = computed(() => {
  const sent = totals.value.total_estimated_tokens
  const saved = totals.value.tokens_saved
  const denom = sent + saved
  if (!denom) return 0
  const rate = saved / denom
  if (rate < 0) return 0
  if (rate > 1) return 1
  return rate
})

// avgSaved = mean tokens saved per compressed request.
const avgSaved = computed(() => {
  const calls = totals.value.compressed_calls
  if (!calls) return 0
  return Math.round(totals.value.tokens_saved / calls)
})

// Dimension card data. Each card consumes one breakdown array from the
// single stats call: labels are the x categories (date / owner / model /
// provider name), values are the tokens_saved series plotted on the y axis.
// The daily series is newest-first from the backend; sort ascending by bucket
// so the line reads left-to-right (older -> newer), matching CostTrendChart.
const dailyRowsAsc = computed(() => {
  const rows = stats.value?.daily_series ?? []
  return [...rows].sort((a, b) => (a.bucket < b.bucket ? -1 : a.bucket > b.bucket ? 1 : 0))
})
// "YYYY-MM-DD" -> "MM-DD" so a 30-bucket axis stays legible.
const dailyLabels = computed(() => dailyRowsAsc.value.map((r) => r.bucket.slice(5)))
const dailyTokensSaved = computed(() => dailyRowsAsc.value.map((r) => r.tokens_saved))

const apiKeyLabels = computed(() => (stats.value?.top_api_keys ?? []).map((r) => r.owner_label))
const apiKeyTokensSaved = computed(() => (stats.value?.top_api_keys ?? []).map((r) => r.tokens_saved))

const modelLabels = computed(() => (stats.value?.top_models ?? []).map((r) => r.model_name))
const modelTokensSaved = computed(() => (stats.value?.top_models ?? []).map((r) => r.tokens_saved))

const providerLabels = computed(() => (stats.value?.top_providers ?? []).map((r) => r.provider_name))
const providerTokensSaved = computed(() => (stats.value?.top_providers ?? []).map((r) => r.tokens_saved))

const compressorRows = computed(() => stats.value?.compressor_hits ?? [])
const skipReasonRows = computed(() => stats.value?.skip_reason_breakdown ?? [])

// === Reload (stale-guarded, mirrors AnalyticsPage) ========================
// reloadSeq is a monotonic token: each reload captures its own seq and bails
// (without writing state) if a newer reload has started. Without this guard,
// a rapid filter change could let the older response land last and overwrite
// the newer stats with stale data.
let reloadSeq = 0

async function reload() {
  const mySeq = ++reloadSeq
  loading.value = true
  // Clear immediately so a failed reload under new filters can't leave stale
  // financial data on screen. The user sees a brief loading state rather
  // than the previous filter's numbers; on error the results stay cleared.
  stats.value = null
  try {
    const filter: AnalyticsFilter = { start: timeRange.value.start, end: timeRange.value.end }
    const result = await getCompressStats(filter)
    if (mySeq !== reloadSeq) return // a newer reload started; discard this one
    stats.value = result
  } catch (err) {
    if (mySeq !== reloadSeq) return
    message.error(displayMessage(err, t))
    // stats stays null — no stale data under the new filter.
  } finally {
    // Only clear loading when the latest reload finishes — otherwise a stale
    // finally could flip it to false while the newer reload is still in flight.
    if (mySeq === reloadSeq) loading.value = false
  }
}

// Settings-enabled GETs run on mount to drive the CTA banner. Stats are
// deliberately NOT loaded here: TimeRangeSelect resolves and emits its range
// synchronously on mount, which sets timeRange and fires the watch below —
// loading stats from onMounted too would double every request on first paint.
// loadSettingsEnabled handles its own errors, so no .catch is needed.
onMounted(() => {
  void loadSettingsEnabled()
})

// Stats reload whenever the time range changes — including the initial range
// TimeRangeSelect emits on mount, which is what performs the first load.
// reload handles its own errors, so no .catch is needed.
watch(
  timeRange,
  () => {
    void reload()
  },
  { deep: true },
)

function onPresetChange(v: RangePreset) {
  preset.value = v
}

// After a settings save, re-read the enabled flags (for the CTA banner)
// and reload stats (the toggle may affect new data going forward).
function onSettingsSaved() {
  void loadSettingsEnabled()
  void reload()
}

// === Column definitions ===================================================
//
// Two breakdown tables (compressor hits + skip reasons). The four dimension
// breakdowns now render as line charts (see chart-grid above), so their
// column definitions have been removed.

// --- Compressor hits breakdown ---
const compressorColumns = computed<DataTableColumns<CompressorHitRow>>(() => [
  {
    title: () => t('costOptimization.colCompressor'),
    key: 'name',
    minWidth: 200,
    render: (r) => h('span', { class: 'mono-cell' }, r.name),
  },
  {
    title: () => h(HelpLabel, { tip: t('costOptimization.colHits_tip') }, { default: () => t('costOptimization.colHits') }),
    key: 'hits',
    width: 140,
    align: 'right',
    render: (r) => formatNumber(r.hits),
  },
])

// --- Skip reasons breakdown ---
// skip_reason = '' means compression actually ran (OK bucket); any other
// value is one of the short stable skip codes the compress stage emits when
// it bypasses a request. The codes mirror internal/compress/result.go's
// SkipReason enum; render them via i18n so users see a localized label
// instead of the raw code. Unknown codes fall back to a generic "other"
// label with the raw code in parentheses for debuggability.

function formatSkipReason(code: string): string {
  if (code === '') return t('costOptimization.skipReasonOk')
  const key = SKIP_REASON_KEYS[code]
  if (key) return t(key)
  return t('costOptimization.skipReasonUnknown', { code })
}

const skipReasonColumns = computed<DataTableColumns<CompressSkipReasonRow>>(() => [
  {
    title: () => t('costOptimization.colSkipReason'),
    key: 'skip_reason',
    minWidth: 200,
    render: (r) => formatSkipReason(r.skip_reason),
  },
  {
    title: () => t('costOptimization.colCalls'),
    key: 'calls',
    width: 140,
    align: 'right',
    render: (r) => formatNumber(r.calls),
  },
])
</script>

<style scoped>
.cost-optimization-page {
  display: flex;
  flex-direction: column;
  gap: var(--space-6);
}

/* Metric row: 5 KPI tiles. The base .metric-row/.metric/.metric__*
   classes live in global.less; each page only sets its own column count. */
.metric-row {
  grid-template-columns: repeat(5, 1fr);
}

/* CTA banner — a flat card sitting right under the header. */
.cta-banner {
  display: flex;
  align-items: center;
  padding: var(--space-4) var(--space-5);
}

.cta-banner__quiet {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  color: var(--color-success, #18a058);
  font-weight: 600;
}

.cta-banner__main {
  display: flex;
  align-items: center;
  flex-wrap: wrap;
  gap: var(--space-3) var(--space-4);
  width: 100%;
}

.cta-banner__lead {
  display: flex;
  align-items: center;
  gap: var(--space-2);
  font-weight: 600;
  color: var(--color-text);
}

.cta-banner__icon {
  flex-shrink: 0;
  color: var(--color-text-secondary);
}

.cta-banner__icon--ok {
  color: var(--color-success, #18a058);
}

.cta-banner__pills {
  display: flex;
  flex-wrap: wrap;
  gap: var(--space-2);
}

.status-pill {
  display: inline-flex;
  align-items: center;
  padding: 2px 10px;
  border-radius: 999px;
  font-size: var(--text-xs);
  font-weight: 600;
  white-space: nowrap;
}

.status-pill--on {
  background: rgba(24, 160, 88, 0.12);
  color: var(--color-success, #18a058);
}

.status-pill--off {
  background: rgba(208, 48, 80, 0.10);
  color: var(--color-danger, #d03050);
}

/* Section heading inside a section-card (for the breakdown tables + chart cards). */
.section-card__head {
  display: flex;
  align-items: center;
  gap: var(--space-3);
  margin-bottom: var(--space-4);
  font-weight: 700;
  color: var(--color-text);
}

/* Chart grid: 2x2 layout for the four dimension line charts. Collapses to a
   single column under 1100px, matching CostStatsPage's .split-row rule. */
.chart-grid {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: var(--space-6);
}

/* Breakdown row: two tables side by side, collapses on narrow screens. */
.breakdown-row {
  display: grid;
  grid-template-columns: 1fr 1fr;
  gap: var(--space-6);
}

:deep(.mono-cell) {
  font-family: var(--font-mono, monospace);
  font-weight: 600;
  color: var(--color-text);
}

@media (max-width: 1100px) {
  .metric-row {
    grid-template-columns: repeat(2, 1fr);
  }
  .chart-grid {
    grid-template-columns: 1fr;
  }
  .breakdown-row {
    grid-template-columns: 1fr;
  }
}

@media (max-width: 640px) {
  .metric-row {
    grid-template-columns: 1fr;
  }
}
</style>
