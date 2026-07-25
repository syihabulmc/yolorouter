<!-- frontend/src/components/costs/CostBreakdownChart.vue
     Cost composition donut (module: cost breakdown). Two tabs — by provider,
     by model — each a share-of-spend donut. Slices past the top N collapse
     into one "Other" wedge so a long tail doesn't shatter the ring. -->
<template>
  <div class="cost-breakdown">
    <NTabs :value="tab" type="segment" size="small" @update:value="onTab">
      <NTabPane :name="'provider'" :tab="t('costs.breakdown.byProvider')" />
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
}>()

const { t } = useI18n()

type BreakdownTab = 'provider' | 'model'
const tab = ref<BreakdownTab>('provider')
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
}

const slices = computed<Slice[]>(() => {
  const raw =
    tab.value === 'provider'
      ? props.providerRows.map((r) => ({
          name: r.provider_name || t('costs.breakdown.unrouted'),
          micros: r.cost_micros,
        }))
      : props.modelRows.map((r) => ({
          name: r.model_name || t('costs.breakdown.unknownModel'),
          micros: r.cost_micros,
        }))
  // Only positive-cost rows form the ring; zero-cost buckets carry no share.
  const positive = raw.filter((r) => r.micros > 0).sort((a, b) => b.micros - a.micros)
  const head = positive.slice(0, TOP_N)
  const tail = positive.slice(TOP_N)
  const result: Slice[] = head.map((r) => ({
    name: r.name,
    micros: r.micros,
    value: fromMicros(r.micros),
  }))
  if (tail.length) {
    const otherMicros = tail.reduce((sum, r) => sum + r.micros, 0)
    result.push({
      name: t('costs.breakdown.other'),
      micros: otherMicros,
      value: fromMicros(otherMicros),
    })
  }
  return result
})

const option = computed(() => {
  const data = slices.value
  const colors = data.map((s, i) =>
    s.name === t('costs.breakdown.other') ? OTHER_COLOR : PALETTE[i % PALETTE.length],
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
