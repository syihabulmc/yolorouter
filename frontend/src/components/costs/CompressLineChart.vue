<!-- frontend/src/components/costs/CompressLineChart.vue
     A single-series line chart for one compress metric, reused across the
     four dimension cards on CostOptimizationPage (daily / API key / model /
     provider). Mirrors CostTrendChart's structure: smooth line + areaStyle,
     optional dashed average markLine, empty state when no data, and a tooltip
     whose axis label and y-axis ticks share the caller-supplied formatValue
     so the units stay consistent (tokens, usd, etc.) between chart and
     metric tiles. The x axis renders arbitrary category labels (date strings,
     key/model/provider names) with rotate+hideOverlap so long labels can't
     collide. -->
<template>
  <div class="compress-line">
    <div v-if="!effectiveContent" class="compress-line__empty">
      <EmptyState :icon="BarChart3" :title="emptyText" />
    </div>
    <VChart
      v-else
      class="compress-line__chart"
      :option="option"
      :update-options="{ notMerge: true }"
      autoresize
    />
  </div>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { BarChart3 } from '@lucide/vue'
import EmptyState from '../EmptyState.vue'
import {
  VChart,
  CHART_ACCENT as ACCENT,
  CHART_TEXT_MUTED as TEXT_MUTED,
  CHART_GRID_LINE as GRID_LINE,
  CHART_AXIS_LINE as AXIS_LINE,
} from '../../utils/echarts'

const props = withDefaults(
  defineProps<{
    labels: string[]
    values: number[]
    formatValue: (n: number) => string
    showAverage?: boolean
    emptyText?: string
  }>(),
  {
    showAverage: false,
    emptyText: '',
  },
)

const effectiveContent = computed(() => {
  return props.values.filter(e => !!e).length
})

const option = computed(() => {
  // Pair labels and values defensively; if a caller hands in mismatched
  // lengths we trim to the shorter one so the chart never reads undefined.
  const len = Math.min(props.labels.length, props.values.length)
  const labels = props.labels.slice(0, len)
  const values = props.values.slice(0, len)

  return {
    tooltip: {
      trigger: 'axis',
      axisPointer: { type: 'shadow' },
      formatter: (params: unknown) => {
        if (!Array.isArray(params) || !params.length) return ''
        const first = params[0] as { axisValueLabel?: string; name?: string; dataIndex: number }
        const header = first.axisValueLabel || first.name || ''
        const v = values[first.dataIndex] ?? 0
        return `${header}<br/>${props.formatValue(v)}`
      },
    },
    grid: { left: 8, right: 8, top: 16, bottom: 24, containLabel: true },
    xAxis: {
      type: 'category',
      data: labels,
      boundaryGap: false,
      axisTick: { alignWithLabel: true },
      axisLine: { lineStyle: { color: AXIS_LINE } },
      // rotate+hideOverlap keeps long key/model/provider names from colliding
      // while still showing as many as the axis has room for.
      axisLabel: { fontSize: 11, color: TEXT_MUTED, rotate: 30, hideOverlap: true },
    },
    yAxis: {
      type: 'value',
      axisLabel: {
        fontSize: 11,
        color: TEXT_MUTED,
        formatter: (v: number) => props.formatValue(v),
      },
      splitLine: { lineStyle: { color: GRID_LINE } },
    },
    series: [
      {
        type: 'line',
        data: values,
        smooth: true,
        showSymbol: true,
        symbolSize: 6,
        lineStyle: { color: ACCENT, width: 2 },
        itemStyle: { color: ACCENT },
        areaStyle: { color: 'rgba(100, 103, 242, 0.08)' },
        // Dashed average marker; only attached when showAverage is set so the
        // per-key/model/provider cards (which aren't time series) skip it.
        markLine: props.showAverage
          ? {
              symbol: 'none',
              silent: true,
              lineStyle: { color: TEXT_MUTED, type: 'dashed' },
              label: {
                formatter: (p: { value: number }) => props.formatValue(p.value),
                fontSize: 11,
                color: TEXT_MUTED,
              },
              data: [{ type: 'average' }],
            }
          : undefined,
      },
    ],
  }
})
</script>

<style scoped>
.compress-line {
  width: 100%;
  height: 260px;
}
.compress-line__chart {
  width: 100%;
  height: 100%;
}
.compress-line__empty {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 100%;
  color: var(--color-text-muted);
  font-size: var(--text-sm);
}
</style>
