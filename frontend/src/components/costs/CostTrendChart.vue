<!-- frontend/src/components/costs/CostTrendChart.vue
     Daily spend trend (module: spend trend). One line of usd-per-day plus a
     dashed average marker. A lifetime budget cap has no daily equivalent, so
     the reference line is the window's own mean daily spend ("you spend about
     $X/day") rather than a budget overlay that wouldn't map onto a daily axis. -->
<template>
  <div class="cost-trend">
    <div v-if="!rows.length" class="cost-trend__empty">
      <EmptyState :icon="BarChart3" :title="t('costs.noData')" />
    </div>
    <VChart
      v-else
      class="cost-trend__chart"
      :option="option"
      :update-options="{ notMerge: true }"
      autoresize
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
  CHART_AXIS_LINE as AXIS_LINE,
} from '../../utils/echarts'
import { formatMicros, fromMicros } from '../../utils/money'
import type { TimeReportRow } from '../../api/analytics'

const props = defineProps<{ rows: TimeReportRow[] }>()
const { t } = useI18n()

// A time bucket is "YYYY-MM-DD" (day) or "YYYY-MM-DD HH:00" (hour); trim the
// year for the day case so a 30-bucket axis stays legible.
function formatAxisBucket(s: string): string {
  return s.length === 10 ? s.slice(5) : s
}

const option = computed(() => {
  // The report returns time buckets newest-first, but a left-to-right axis has
  // to read oldest-to-newest or a rising trend looks like it's falling. Sort a
  // copy ascending by bucket — the zero-padded "YYYY-MM-DD"[" HH:00"] format
  // sorts chronologically as plain strings — and drive labels, values, and the
  // tooltip lookup from that same ordered array.
  const rows = [...props.rows].sort((a, b) => (a.bucket < b.bucket ? -1 : a.bucket > b.bucket ? 1 : 0))
  const labels = rows.map((r) => formatAxisBucket(r.bucket))
  // Yuan (major unit); micros are integers so this is exact, and the tooltip
  // still formats via formatMicros so ticks and card stay consistent.
  const costs = rows.map((r) => fromMicros(r.cost_micros))

  return {
    tooltip: {
      trigger: 'axis',
      axisPointer: { type: 'shadow' },
      formatter: (params: unknown) => {
        if (!Array.isArray(params) || !params.length) return ''
        const first = params[0] as { axisValueLabel?: string; name?: string; dataIndex: number }
        const header = first.axisValueLabel || first.name || ''
        const micros = rows[first.dataIndex]?.cost_micros ?? 0
        return `${header}<br/>${t('costs.trend.seriesName')}: $${formatMicros(micros)}`
      },
    },
    grid: { left: 8, right: 8, top: 16, bottom: 24, containLabel: true },
    xAxis: {
      type: 'category',
      data: labels,
      axisTick: { alignWithLabel: true },
      axisLine: { lineStyle: { color: AXIS_LINE } },
      axisLabel: { fontSize: 11, color: TEXT_MUTED, hideOverlap: true },
    },
    yAxis: {
      type: 'value',
      axisLabel: { fontSize: 11, color: TEXT_MUTED, formatter: (v: number) => `$${v}` },
      splitLine: { lineStyle: { color: GRID_LINE } },
    },
    series: [
      {
        name: t('costs.trend.seriesName'),
        type: 'line',
        data: costs,
        smooth: true,
        showSymbol: true,
        symbolSize: 6,
        lineStyle: { color: ACCENT, width: 2 },
        itemStyle: { color: ACCENT },
        areaStyle: { color: 'rgba(100, 103, 242, 0.08)' },
        // Dashed mean-daily-spend line, labelled with its own usd value.
        markLine: {
          symbol: 'none',
          silent: true,
          lineStyle: { color: TEXT_MUTED, type: 'dashed' },
          label: {
            formatter: (p: { value: number }) => `${t('costs.trend.avgLabel')} $${p.value}`,
            fontSize: 11,
            color: TEXT_MUTED,
          },
          data: [{ type: 'average' }],
        },
      },
    ],
  }
})
</script>

<style scoped>
.cost-trend {
  width: 100%;
  height: 260px;
}
.cost-trend__chart {
  width: 100%;
  height: 100%;
}
.cost-trend__empty {
  display: flex;
  align-items: center;
  justify-content: center;
  height: 100%;
  color: var(--color-text-muted);
  font-size: var(--text-sm);
}
</style>
