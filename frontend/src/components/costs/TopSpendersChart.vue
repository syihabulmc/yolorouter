<!-- frontend/src/components/costs/TopSpendersChart.vue
     Top spenders (module: top spenders). Horizontal bars rank the callers
     (api keys) that spent the most in the window, so the biggest cost drivers
     read at a glance. Precise per-caller figures live in the usage report. -->
<template>
  <div class="top-spenders">
    <div v-if="!bars.length" class="top-spenders__empty">
      <EmptyState :icon="BarChart3" :title="t('costs.noData')" />
    </div>
    <VChart
      v-else
      class="top-spenders__chart"
      :style="{ height: chartHeight }"
      :option="option"
      :update-options="{ notMerge: true }"
      autoresize
      @click="onChartClick"
    />
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { useI18n } from 'vue-i18n'
import { BarChart3 } from '@lucide/vue'
import EmptyState from '../EmptyState.vue'
import {
  VChart,
  CHART_ACCENT as ACCENT,
  CHART_TEXT_MUTED as TEXT_MUTED,
  CHART_GRID_LINE as GRID_LINE,
} from '../../utils/echarts'
import { formatMicros, fromMicros } from '../../utils/money'
import type { CallerReportRow } from '../../api/analytics'

const props = defineProps<{ rows: CallerReportRow[] }>()
const emit = defineEmits<{ select: [payload: { apiKeyId: number }] }>()
const { t } = useI18n()

// TOP_N caps the ranking; a boss view wants the handful of biggest drivers,
// not the full key roster.
const TOP_N = 8
// ROW_PX sizes the chart to its bar count so few callers don't leave a tall
// empty canvas and many don't cramp.
const ROW_PX = 34

interface Bar {
  label: string
  value: number // yuan
  micros: number
  // Preserved so a chart click can route to the per-key cost detail page.
  // Null when the source row had no api_key_id (unknown caller) — such bars
  // are NOT clickable.
  apiKeyId: number | null
}

const bars = computed<Bar[]>(() => {
  const positive = props.rows
    .filter((r) => r.cost_micros > 0)
    .sort((a, b) => b.cost_micros - a.cost_micros)
    .slice(0, TOP_N)
    .map((r) => ({
      label: r.owner_label || t('costs.topSpenders.unknownCaller'),
      micros: r.cost_micros,
      value: fromMicros(r.cost_micros),
      apiKeyId: r.api_key_id ?? null,
    }))
  // ECharts category axis stacks bottom-to-top, so reverse to put the largest
  // spender at the top of the visible list.
  return positive.reverse()
})

// onChartClick maps a bar click back to its source row and emits a select
// event so the parent page can navigate. Bars with a null apiKeyId (unknown
// caller bucket) are silently ignored — there is no detail page to drill into.
function onChartClick(params: { dataIndex?: number; name?: string }) {
  if (params.dataIndex == null) return
  const bar = bars.value[params.dataIndex]
  if (!bar || bar.apiKeyId == null) return
  emit('select', { apiKeyId: bar.apiKeyId })
}

const chartHeight = computed(() => `${Math.max(bars.value.length * ROW_PX + 24, 120)}px`)

const option = computed(() => ({
  tooltip: {
    trigger: 'axis',
    axisPointer: { type: 'shadow' },
    formatter: (params: unknown) => {
      if (!Array.isArray(params) || !params.length) return ''
      const p = params[0] as { dataIndex: number; name: string; marker: string }
      const micros = bars.value[p.dataIndex]?.micros ?? 0
      return `${p.marker} ${p.name}<br/>¥${formatMicros(micros)}`
    },
  },
  grid: { left: 8, right: 16, top: 8, bottom: 8, containLabel: true },
  xAxis: {
    type: 'value',
    axisLabel: { fontSize: 11, color: TEXT_MUTED, formatter: (v: number) => `¥${v}` },
    splitLine: { lineStyle: { color: GRID_LINE } },
  },
  yAxis: {
    type: 'category',
    data: bars.value.map((b) => b.label),
    axisTick: { show: false },
    axisLabel: { fontSize: 11, color: TEXT_MUTED },
  },
  series: [
    {
      type: 'bar',
      data: bars.value.map((b) => b.value),
      barMaxWidth: 18,
      itemStyle: { color: ACCENT, borderRadius: [0, 4, 4, 0] },
    },
  ],
}))
</script>

<style scoped>
.top-spenders {
  width: 100%;
}
.top-spenders__chart {
  width: 100%;
}
.top-spenders__empty {
  display: flex;
  align-items: center;
  justify-content: center;
  /* Match EmptyState's global min-height (220px in styles/global.less) so the
     empty block never overflows its fixed-height container. */
  min-height: 220px;
  color: var(--color-text-muted);
  font-size: var(--text-sm);
}
</style>
