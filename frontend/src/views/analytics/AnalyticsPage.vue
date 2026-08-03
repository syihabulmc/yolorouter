<!-- frontend/src/views/analytics/AnalyticsPage.vue
     Usage report. Combines:
       - Filter bar (time range / api key / model / provider / status)
       - Dimension tabs (model / provider / time / caller)
       - Overview metric row (calls / success rate / tokens / cost)
       - Dimension-specific NDataTable (column tooltips via columnTitle)
       - CSV export button

     The page owns the filter + dimension state. Each change triggers a
     reload of both /overview and /report in parallel — they're independent
     given the same filter, so a single error message covers both. -->
<template>
  <div class="common-page">
    <PageHeader :eyebrow="t('analytics.eyebrow')" :title="t('analytics.pageTitle')" :description="t('analytics.pageDescription')">
      <template #actions>
        <NButton :loading="exporting" :disabled="!reportRows.length" @click="onExport">
          <template #icon><Download :size="16" /></template>
          {{ t('analytics.exportCSV') }}
        </NButton>
      </template>
    </PageHeader>

    <!-- Filter bar (inlined). The page owns filter/time-range/preset state, so
         the controls bind straight to it instead of routing through a wrapper
         component's events. -->
    <div class="filter-panel">
      <div class="filter-grid">
        <div class="filter-item w-auto">
          <TimeRangeSelect
            :model-value="timeRange"
            :preset="preset"
            @update:model-value="onTimeRange"
            @update:preset="onPreset"
          />
        </div>

        <FilterSelectField
          :label="t('analytics.apiKey')"
          :value="filter.api_key_id ?? null"
          :options="apiKeyOptions"
          :placeholder="t('analytics.allApiKey')"
          filterable
          width="100%"
          @update:value="(v) => update('api_key_id', v)"
        />

        <FilterSelectField
          :label="t('analytics.model')"
          :value="filter.model_name ?? null"
          :options="modelOptions"
          :placeholder="t('analytics.allModel')"
          filterable
          width="100%"
          @update:value="(v) => update('model_name', v)"
        />

        <FilterSelectField
          :label="t('analytics.provider')"
          :value="filter.provider_id ?? null"
          :options="providerOptions"
          :placeholder="t('analytics.allProvider')"
          filterable
          width="100%"
          @update:value="(v) => update('provider_id', v)"
        />

        <FilterSelectField
          :label="t('analytics.status')"
          :value="filter.status ?? null"
          :options="statusOptions"
          :placeholder="t('analytics.allStatus')"
          width="100%"
          @update:value="(v) => update('status', (v as string) || null)"
        />
      </div>
    </div>
    <div v-if="dimension === 'time'" class="bucket-bar">
      <span class="bucket-label">{{ t('analytics.bucketLabel') }}</span>
      <NSelect
        :value="bucket"
        :options="bucketOptions"
        size="small"
        style="width: 120px"
        @update:value="(v: AnalyticsBucket) => onBucketChange(v)"
      />
    </div>

    <!-- Overview metric row -->
    <div class="metric-row">
      <div class="metric">
        <div class="metric__label">
          <HelpLabel :tip="t('analytics.callsColumn_tip')">{{ t('analytics.totalCalls') }}</HelpLabel>
        </div>
        <div class="metric__value">{{ formatNumber(overview?.total_calls ?? 0) }}</div>
      </div>
      <div class="metric">
        <div class="metric__label">
          <HelpLabel :tip="t('analytics.successRate_tip')">{{ t('analytics.successRate') }}</HelpLabel>
        </div>
        <div class="metric__value">{{ formatRate(overview?.success_rate ?? 0) }}</div>
        <div class="metric__sub">{{ t('analytics.successRateSub', { success: overview?.success_calls ?? 0, ended: overview?.ended_calls ?? 0 }) }}</div>
      </div>
      <div class="metric">
        <div class="metric__label">
          <HelpLabel :tip="t('analytics.inputTokensColumn_tip')">{{ t('analytics.inputTokens') }}</HelpLabel>
        </div>
        <div class="metric__value">{{ formatNumber(overview?.input_tokens ?? 0) }}</div>
      </div>
      <div class="metric">
        <div class="metric__label">
          <HelpLabel :tip="t('analytics.outputTokensColumn_tip')">{{ t('analytics.outputTokens') }}</HelpLabel>
        </div>
        <div class="metric__value">{{ formatNumber(overview?.output_tokens ?? 0) }}</div>
      </div>
      <div class="metric">
        <div class="metric__label">
          <HelpLabel :tip="t('analytics.costColumn_tip')">{{ t('analytics.totalCost') }}</HelpLabel>
        </div>
        <div class="metric__value">¥{{ formatMicros(overview?.cost_micros ?? 0, 2) }}</div>
        <div v-if="(overview?.unknown_cost_calls ?? 0) > 0" class="metric__sub">
          {{ t('analytics.unknownCostSub', { n: overview?.unknown_cost_calls ?? 0 }) }}
        </div>
      </div>
    </div>

    <!-- Dimension tabs + report table -->
    <div class="section-card">
      <NTabs :value="dimension" type="line" @update:value="onDimensionChange">
        <NTabPane :name="'model'" :tab="t('analytics.dimensionModel')">
          <ResponsiveDataTable
            :columns="modelColumns"
            :data="modelRows"
            :loading="loading"
            :scroll-x="1330"
            :row-key="(r: ModelReportRow) => r.model_name"
          >
            <template #empty>
              <EmptyState :icon="BarChart3" :title="t('analytics.noData')" />
            </template>
          </ResponsiveDataTable>
        </NTabPane>
        <NTabPane :name="'provider'" :tab="t('analytics.dimensionProvider')">
          <ResponsiveDataTable
            :columns="providerColumns"
            :data="providerRows"
            :loading="loading"
            :scroll-x="920"
            :row-key="providerRowKey"
          >
            <template #empty>
              <EmptyState :icon="BarChart3" :title="t('analytics.noData')" />
            </template>
          </ResponsiveDataTable>
        </NTabPane>
        <NTabPane :name="'time'" :tab="t('analytics.dimensionTime')">
          <ResponsiveDataTable
            :columns="timeColumns"
            :data="timeRows"
            :loading="loading"
            :scroll-x="1330"
            :row-key="(r: TimeReportRow) => r.bucket"
          >
            <template #empty>
              <EmptyState :icon="BarChart3" :title="t('analytics.noData')" />
            </template>
          </ResponsiveDataTable>
        </NTabPane>
        <NTabPane :name="'caller'" :tab="t('analytics.dimensionCaller')">
          <ResponsiveDataTable
            :columns="callerColumns"
            :data="callerRows"
            :loading="loading"
            :scroll-x="1330"
            :row-key="callerRowKey"
          >
            <template #empty>
              <EmptyState :icon="BarChart3" :title="t('analytics.noData')" />
            </template>
          </ResponsiveDataTable>
        </NTabPane>
      </NTabs>
    </div>
  </div>
</template>

<script setup lang="ts">
import { computed, h, onMounted, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NButton, NSelect, NTabPane, NTabs, useMessage, type DataTableColumns, type SelectOption } from 'naive-ui'
import { BarChart3, Download } from '@lucide/vue'
import PageHeader from '../../components/PageHeader.vue'
import EmptyState from '../../components/EmptyState.vue'
import HelpLabel from '../../components/HelpLabel.vue'
import ResponsiveDataTable from '../../components/common/ResponsiveDataTable.vue'
import FilterSelectField from '../../components/common/FilterSelectField.vue'
import TimeRangeSelect, { type RangePreset, type TimeRange } from '../../components/analytics/TimeRangeSelect.vue'
import { listProviders } from '../../api/providers'
import { listModels } from '../../api/models'
import { listAPIKeys, toAPIKeyOptions } from '../../api/apiKeys'
import { initialLast7DaysRange } from '../../utils/timeRange'
import { columnTitle } from '../../utils/columnTitle'
import { formatMicros } from '../../utils/money'
import { formatNumber, formatRate } from '../../utils/format'
import {
  avgDurationColumn,
  callsColumn,
  costColumn,
  successRateColumn,
  tokenColumn,
  unknownCostColumn,
} from '../../utils/analyticsColumns'
import { displayMessage } from '../../api/client'
import {
  exportAnalyticsCSV,
  getAnalyticsOverview,
  getAnalyticsReport,
  type AnalyticsBucket,
  type AnalyticsDimension,
  type AnalyticsFilter,
  type CallerReportRow,
  type ModelReportRow,
  type OverviewRow,
  type ProviderReportRow,
  type TimeReportRow,
} from '../../api/analytics'

const { t } = useI18n()
const message = useMessage()

// === Filter / dimension state =============================================

// Default window = last 7 days (matches the backend's default for
// dimension=time and feels like a reasonable default for "show me recent
// usage" without over-querying). Shared with the other dashboard pages via
// utils/timeRange.ts so every dashboard opens on the same window.
const preset = ref<RangePreset>('last7d')
const timeRange = ref<TimeRange>(initialLast7DaysRange())
const filter = ref<AnalyticsFilter>({ start: timeRange.value.start, end: timeRange.value.end })
const dimension = ref<AnalyticsDimension>('model')
const bucket = ref<AnalyticsBucket>('day')

const bucketOptions = computed<SelectOption[]>(() => [
  { label: t('analytics.bucketDay'), value: 'day' },
  { label: t('analytics.bucketHour'), value: 'hour' },
])

// === Filter option lists ==================================================
//
// Inlined from the former AnalyticsFilterBar. These are admin-configured
// catalogs (not request-derived), so the lists are small and change
// infrequently — fetched once on mount, in parallel.
const apiKeyOptions = ref<SelectOption[]>([])
const providerOptions = ref<SelectOption[]>([])
const modelOptions = ref<SelectOption[]>([])

const statusOptions = computed<SelectOption[]>(() => [
  { label: t('analytics.statusSuccess'), value: 'success' },
  { label: t('analytics.statusFailed'), value: 'failed' },
  { label: t('analytics.statusPartial'), value: 'partial' },
  { label: t('analytics.statusCancelled'), value: 'cancelled' },
  { label: t('analytics.statusRejected'), value: 'rejected' },
])

// === Result state =========================================================
//
// Four dimension-typed refs instead of one `rows: unknown[]` because
// vue-tsc can't narrow a union through a single ref across renders — typed
// refs let the per-dimension DataTable bindings stay strict.

const overview = ref<OverviewRow | null>(null)
const modelRows = ref<ModelReportRow[]>([])
const providerRows = ref<ProviderReportRow[]>([])
const callerRows = ref<CallerReportRow[]>([])
const timeRows = ref<TimeReportRow[]>([])
const loading = ref(false)
const exporting = ref(false)

// reportRows is the dimension-agnostic accessor used by the export button's
// disabled state ("no rows to export" regardless of which tab is active).
const reportRows = computed<unknown[]>(() => {
  switch (dimension.value) {
    case 'model':
      return modelRows.value
    case 'provider':
      return providerRows.value
    case 'caller':
      return callerRows.value
    case 'time':
      return timeRows.value
  }
})

// === Reload ===============================================================

// reloadSeq is a monotonic token guarding against stale reloads: a rapid
// filter/tab change starts a newer reload before the older one resolves, and
// without this guard the older response could land last and overwrite the
// newer overview/rows with stale data. Each reload captures its own seq and
// bails (without writing state) if a newer one has started.
let reloadSeq = 0

async function reload() {
  const mySeq = ++reloadSeq
  loading.value = true
  // Clear previous results IMMEDIATELY so a failed reload under new filters
  // can't leave stale financial data on screen.
  // The user sees a brief loading state rather than the previous filter's
  // numbers; on error the results stay cleared (not the stale values).
  overview.value = null
  modelRows.value = []
  providerRows.value = []
  callerRows.value = []
  timeRows.value = []
  // Effective bucket: the time dimension honors the caller's bucket; every
  // other dimension uses 'day' for range resolution, so overview and non-time
  // reports clamp to the SAME cap (switching hour→model left overview
  // on the 30d hour cap while model used the 90d day cap).
  const effectiveBucket = dimension.value === 'time' ? bucket.value : 'day'
  // Two parallel round trips — overview and report are independent given
  // the same filter. Promise.all lets a single .catch surface either error.
  try {
    const [ov, report] = await Promise.all([
      getAnalyticsOverview(effectiveBucket, filter.value),
      getAnalyticsReport(dimension.value, bucket.value, filter.value),
    ])
    if (mySeq !== reloadSeq) return // a newer reload started; discard this one
    overview.value = ov
    // Narrow the untyped `rows: unknown` per dimension. The case set must
    // stay in sync with AnalyticsDimension for exhaustiveness — TS would
    // catch a missing case at compile time via the function's return type.
    switch (report.dimension) {
      case 'model':
        modelRows.value = (report.rows as ModelReportRow[]) ?? []
        break
      case 'provider':
        providerRows.value = (report.rows as ProviderReportRow[]) ?? []
        break
      case 'caller':
        callerRows.value = (report.rows as CallerReportRow[]) ?? []
        break
      case 'time':
        timeRows.value = (report.rows as TimeReportRow[]) ?? []
        break
    }
  } catch (err) {
    if (mySeq !== reloadSeq) return
    message.error(displayMessage(err, t))
    // overview/rows stay cleared (set above) — no stale data under new filter.
  } finally {
    // Only clear loading when the latest reload finishes — otherwise a stale
    // finally could flip it to false while the newer reload is still in flight.
    if (mySeq === reloadSeq) loading.value = false
  }
}

onMounted(() => {
  void reload()
  void loadFilterOptions()
})

// Fetch the filter selectors' option lists once. Failure is degraded, not
// broken — the user can still type a model name; show the error inline but
// don't block the page.
async function loadFilterOptions() {
  try {
    const [providerPage, modelPage, apiKeyPage] = await Promise.all([
      listProviders(),
      listModels(),
      listAPIKeys({ q: '', owner: '', status: '', page: 1, pageSize: 200 }),
    ])
    providerOptions.value = providerPage.list.map((p) => ({ label: p.name, value: p.id }))
    modelOptions.value = modelPage.list.map((m) => ({ label: m.name, value: m.name }))
    apiKeyOptions.value = toAPIKeyOptions(apiKeyPage.list)
  } catch (err) {
    message.error(displayMessage(err, t))
  }
}

// Reload whenever the dimension / bucket / filter changes. The watch is
// deep on `filter` because filter changes always emit a new object (see
// update()).
watch([dimension, bucket, filter], () => {
  void reload()
}, { deep: true })

// === Event handlers =======================================================

// Merge a single filter field, always emitting a new object so the deep
// watch fires and reloads.
function update<K extends keyof AnalyticsFilter>(key: K, value: AnalyticsFilter[K]) {
  filter.value = { ...filter.value, [key]: value }
}

function onTimeRange(v: TimeRange) {
  timeRange.value = v
  filter.value = { ...filter.value, start: v.start, end: v.end }
}

function onPreset(v: RangePreset) {
  preset.value = v
}

function onDimensionChange(v: string | number) {
  // NTabs emits string | number; we know our tab names are the dimension
  // strings. The cast is safe because the tabs are statically defined.
  dimension.value = v as AnalyticsDimension
}

function onBucketChange(v: AnalyticsBucket) {
  bucket.value = v
}

function onExport() {
  exporting.value = true
  try {
    exportAnalyticsCSV(dimension.value, bucket.value, filter.value)
  } finally {
    // The export is a navigation click, not a promise — there's nothing to
    // await. The toggle just covers the brief moment between mousedown and
    // the browser's download dialog.
    setTimeout(() => {
      exporting.value = false
    }, 600)
  }
}

// === Row keys for NULL-id buckets =========================================
//
// The provider/caller dimensions include a synthetic bucket for rows with
// NULL provider_id / api_key_id (auth failed before routing, etc.). naive-ui
// needs a unique string row-key; fall back to a fixed sentinel for those
// NULL rows so they're still selectable / paginated correctly.

function providerRowKey(r: ProviderReportRow): string {
  return r.provider_id == null ? '__null_provider__' : `p-${r.provider_id}`
}

function callerRowKey(r: CallerReportRow): string {
  return r.api_key_id == null ? '__null_caller__' : `k-${r.api_key_id}`
}

// === Column definitions ===================================================
//
// Dimension (label) columns stay inline here because each is specific to a
// dimension (model name / provider name / caller / bucket). The shared metric
// columns (calls / successRate / cost / unknownCost / tokens / avgDuration)
// come from utils/analyticsColumns.ts so a metric column change lands in every
// dimension at once instead of being copy-pasted across four column arrays.

const modelColumns = computed<DataTableColumns<ModelReportRow>>(() => [
  {
    title: columnTitle(t('analytics.modelNameColumn'), t('analytics.modelNameColumn_tip')),
    key: 'model_name',
    minWidth: 200,
    render: (r) => h('span', { class: 'mono-cell' }, r.model_name || '—'),
  },
  callsColumn<ModelReportRow>(t),
  successRateColumn<ModelReportRow>(t),
  tokenColumn<ModelReportRow>(t, 'input_tokens', 'inputTokensColumn'),
  tokenColumn<ModelReportRow>(t, 'output_tokens', 'outputTokensColumn'),
  tokenColumn<ModelReportRow>(t, 'cache_write_tokens', 'cacheWriteTokensColumn', 150),
  tokenColumn<ModelReportRow>(t, 'cache_read_tokens', 'cacheReadTokensColumn', 150),
  costColumn<ModelReportRow>(t),
  unknownCostColumn<ModelReportRow>(t),
])

const providerColumns = computed<DataTableColumns<ProviderReportRow>>(() => [
  {
    title: columnTitle(t('analytics.providerNameColumn'), t('analytics.providerNameColumn_tip')),
    key: 'provider_name',
    minWidth: 200,
    render: (r) => r.provider_name || t('analytics.unroutedBucket'),
  },
  callsColumn<ProviderReportRow>(t),
  successRateColumn<ProviderReportRow>(t),
  avgDurationColumn<ProviderReportRow>(t),
  costColumn<ProviderReportRow>(t),
  unknownCostColumn<ProviderReportRow>(t),
])

const callerColumns = computed<DataTableColumns<CallerReportRow>>(() => [
  {
    title: columnTitle(t('analytics.callerColumn'), t('analytics.callerColumn_tip')),
    key: 'owner_label',
    minWidth: 200,
    render: (r) => r.owner_label || t('analytics.unknownCallerBucket'),
  },
  callsColumn<CallerReportRow>(t),
  successRateColumn<CallerReportRow>(t),
  tokenColumn<CallerReportRow>(t, 'input_tokens', 'inputTokensColumn'),
  tokenColumn<CallerReportRow>(t, 'output_tokens', 'outputTokensColumn'),
  tokenColumn<CallerReportRow>(t, 'cache_write_tokens', 'cacheWriteTokensColumn', 150),
  tokenColumn<CallerReportRow>(t, 'cache_read_tokens', 'cacheReadTokensColumn', 150),
  costColumn<CallerReportRow>(t),
  unknownCostColumn<CallerReportRow>(t),
])

const timeColumns = computed<DataTableColumns<TimeReportRow>>(() => [
  {
    title: columnTitle(t('analytics.bucketColumn'), t('analytics.bucketColumn_tip')),
    key: 'bucket',
    minWidth: 180,
    render: (r) => h('span', { class: 'mono-cell' }, r.bucket),
  },
  callsColumn<TimeReportRow>(t),
  successRateColumn<TimeReportRow>(t),
  tokenColumn<TimeReportRow>(t, 'input_tokens', 'inputTokensColumn'),
  tokenColumn<TimeReportRow>(t, 'output_tokens', 'outputTokensColumn'),
  tokenColumn<TimeReportRow>(t, 'cache_write_tokens', 'cacheWriteTokensColumn', 150),
  tokenColumn<TimeReportRow>(t, 'cache_read_tokens', 'cacheReadTokensColumn', 150),
  costColumn<TimeReportRow>(t),
  unknownCostColumn<TimeReportRow>(t),
])
</script>

<style scoped>
.bucket-bar {
  display: flex;
  align-items: center;
  gap: var(--space-2);
}

.bucket-label {
  font-size: var(--text-xs);
  font-weight: 700;
  letter-spacing: 0.05em;
  text-transform: uppercase;
  color: var(--color-text-muted);
}

.metric-row {
  display: grid;
  grid-template-columns: repeat(5, 1fr);
  gap: var(--space-4);
}

.metric {
  display: flex;
  flex-direction: column;
  gap: 4px;
  padding: var(--space-4);
  background: var(--color-surface);
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-lg);
}

.metric__label {
  font-size: var(--text-xs);
  font-weight: 700;
  letter-spacing: 0.05em;
  text-transform: uppercase;
  color: var(--color-text-muted);
}

.metric__value {
  font-size: 1.5rem;
  font-weight: 800;
  line-height: 1;
  font-variant-numeric: tabular-nums;
  color: var(--color-text);
}

.metric__sub {
  font-size: var(--text-xs);
  color: var(--color-text-muted);
}

.section-card {
  padding: var(--space-5);
  background: var(--color-surface);
  border: 1px solid var(--color-border-subtle);
  border-radius: var(--radius-lg);
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
}
@media (max-width: 1100px) {
  .section-card {
    padding: 0;
  }
  :deep(.n-tabs-nav-scroll-wrapper) {
    padding: 0 20px;
  }
}
</style>
