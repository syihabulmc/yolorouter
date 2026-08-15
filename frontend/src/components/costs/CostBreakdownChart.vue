<!-- frontend/src/components/costs/CostBreakdownChart.vue
     Cost composition donut (module: cost breakdown). Two tabs — by provider,
     by model — each a share-of-spend donut. Slices past the top N collapse
     into one "Other" wedge so a long tail doesn't shatter the ring. -->
<template>
  <div class="cost-breakdown">
    <NTabs :value="tab" type="segment" size="small" @update:value="onTab">
      <NTabPane v-if="!hideProvider" :name="'provider'" :tab="t('costs.breakdown.byProvider')" />
      <NTabPane :name="'model'" :tab="t('costs.breakdown.byModel')" />
    </NTabs>
    <div v-if="!slices.length" class="cost-breakdown__empty">
      <EmptyState :icon="BarChart3" :title="t('costs.noData')" />
    </div>
    <VChart
      v-else
      class="cost-breakdown__chart"
      :option="option"
      :update-options="{ notMerge: true }"
      autoresize
      @click="onChartClick"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, ref } from 'vue'
import { useI18n } from 'vue-i18n'
import { NTabPane, NTabs } from 'naive-ui'
import { BarChart3 } from '@lucide/vue'
import EmptyState from '../EmptyState.vue'
import { VChart, CHART_ACCENT } from '../../utils/echarts'
import { formatMicros, fromMicros } from '../../utils/money'
import type { ModelReportRow, ProviderReportRow } from '../../api/analytics'

const props = defineProps<{
  providerRows: ProviderReportRow[]
  modelRows: ModelReportRow[]
  /**
   * Drops the by-provider tab entirely — set for member sessions, whose
   * backend scope has no provider dimension to slice by.
   */
  hideProvider?: boolean
}>()

const emit = defineEmits<{ select: [payload: { providerId?: number; model?: string }] }>()

const { t } = useI18n()

type BreakdownTab = 'provider' | 'model'
const tab = ref<BreakdownTab>(props.hideProvider ? 'model' : 'provider')
function onTab(v: string | number) {
  tab.value = v as BreakdownTab
}

// TOP_N slices are drawn individually; the remainder merges into "Other" so
// the ring stays readable when spend spreads across many providers/models.
const TOP_N = 6

// A qualitative palette (hex, since canvas can't resolve CSS tokens), led by
// the brand accent. The muted grey is reserved for the "Other" wedge.
const PALETTE = [CHART_ACCENT, '#8b5cf6', '#0ea5e9', '#10b981', '#f59e0b', '#ef4444']
const OTHER_COLOR = '#c0c4cc'

interface Slice {
  name: string
  value: number // yuan (major unit), for the chart
  micros: number // original, for the tooltip via the shared formatter
  // Provider-tab identity (null = unrouted bucket); undefined on model tab.
  providerId?: number | null
  // Model-tab identity — the raw model_name ('' = unknown-model bucket);
  // undefined on provider tab.
  modelName?: string
  // True for the merged "Other" wedge (no single entity behind it). Click
  // routing and color key off this flag instead of comparing display labels,
  // so a real entity whose name happens to match a localized fallback label
  // ("Other", "unrouted", "unknown model") is still clickable.
  synthetic?: boolean
}

const slices = computed<Slice[]>(() => {
  // Build Slice objects directly per tab — Slice already carries optional
  // providerId / modelName / synthetic fields, so a single annotated const
  // replaces the old intermediate raw[] + double-map.
  const all: Slice[] =
    tab.value === 'provider'
      ? props.providerRows.map((r) => ({
          name: r.provider_name || t('costs.breakdown.unrouted'),
          micros: r.cost_micros,
          value: fromMicros(r.cost_micros),
          providerId: r.provider_id ?? null,
        }))
      : props.modelRows.map((r) => ({
          name: r.model_name || t('costs.breakdown.unknownModel'),
          micros: r.cost_micros,
          value: fromMicros(r.cost_micros),
          modelName: r.model_name,
        }))
  // Only positive-cost rows form the ring; zero-cost buckets carry no share.
  const positive = all.filter((s) => s.micros > 0).sort((a, b) => b.micros - a.micros)
  const head = positive.slice(0, TOP_N)
  const tail = positive.slice(TOP_N)
  if (tail.length) {
    const otherMicros = tail.reduce((sum, s) => sum + s.micros, 0)
    head.push({
      name: t('costs.breakdown.other'),
      micros: otherMicros,
      value: fromMicros(otherMicros),
      synthetic: true,
    })
  }
  return head
})

const option = computed(() => {
  const data = slices.value
  const colors = data.map((s, i) =>
    s.synthetic ? OTHER_COLOR : PALETTE[i % PALETTE.length],
  )
  return {
    tooltip: {
      trigger: 'item',
      formatter: (p: { dataIndex: number; percent: number; marker: string; name: string }) => {
        const micros = data[p.dataIndex]?.micros ?? 0
        return `${p.marker} ${p.name}<br/>¥${formatMicros(micros)} (${p.percent}%)`
      },
    },
    legend: {
      type: 'scroll',
      orient: 'vertical',
      right: 0,
      top: 'middle',
      textStyle: { fontSize: 11, color: '#606266' },
    },
    color: colors,
    series: [
      {
        type: 'pie',
        radius: ['45%', '70%'],
        center: ['38%', '50%'],
        avoidLabelOverlap: true,
        itemStyle: { borderColor: '#fff', borderWidth: 2 },
        label: { show: false },
        labelLine: { show: false },
        data: data.map((s) => ({ name: s.name, value: s.value })),
      },
    ],
  }
})

// onChartClick routes a slice click to a detail page via the select event.
// Clickability is decided from the source identity captured at build time
// (providerId / modelName) and the synthetic flag — NEVER by comparing the
// display label — so a real entity whose name matches a localized fallback
// ("Other", "unrouted", "unknown model") still drills down.
function onChartClick(params: { dataIndex?: number; name?: string }) {
  if (params.dataIndex == null) return
  const slice = slices.value[params.dataIndex]
  if (!slice) return

  // The merged "Other" wedge has no single entity behind it.
  if (slice.synthetic) return

  if (tab.value === 'provider') {
    // Unrouted buckets have a null provider_id; real providers carry theirs.
    if (slice.providerId != null) emit('select', { providerId: slice.providerId })
    return
  }

  // model tab — the unknown-model bucket has an empty modelName.
  if (slice.modelName) emit('select', { model: slice.modelName })
}
</script>

<style scoped>
.cost-breakdown {
  width: 100%;
}
.cost-breakdown__chart {
  width: 100%;
  height: 260px;
}
.cost-breakdown__empty {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 260px;
  color: var(--color-text-muted);
  font-size: var(--text-sm);
}
</style>
