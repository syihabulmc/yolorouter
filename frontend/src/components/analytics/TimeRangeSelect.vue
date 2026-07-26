<!-- frontend/src/components/analytics/TimeRangeSelect.vue
     Time range picker shared between the AnalyticsPage filter bar and any
     future dashboard that wants the same presets. Emits a {start,end} tuple
     in RFC3339 form (server's /analytics endpoints expect RFC3339).

     All date math is browser-local — the admin's wall clock. The backend
     receives the browser's IANA timezone via the ?timezone= query param and
     groups analytics by that zone, so the preset windows and the backend
     grouping stay aligned without the frontend needing to know the server's
     timezone. -->
<template>
  <div class="time-range">
    <NSelect
      :value="preset"
      :options="presetOptions"
      size="small"
      style="width: 160px"
      @update:value="onPresetChange"
    />
    <NDatePicker
      v-if="preset === 'custom'"
      :value="customRange"
      type="daterange"
      size="small"
      clearable
      :placeholder="t('analytics.customRangePlaceholder')"
      @update:value="onCustomChange"
    />
  </div>
</template>

<script setup lang="ts">
import { computed, ref, watch } from 'vue'
import { useI18n } from 'vue-i18n'
import { NDatePicker, NSelect, type SelectOption } from 'naive-ui'

export type RangePreset = 'today' | 'yesterday' | 'last7d' | 'last30d' | 'custom'

export interface TimeRange {
  start: string | null // RFC3339, inclusive
  end: string | null // RFC3339, exclusive
}

const props = defineProps<{
  modelValue: TimeRange
  preset: RangePreset
}>()
const emit = defineEmits<{
  'update:modelValue': [value: TimeRange]
  'update:preset': [value: RangePreset]
}>()

const { t } = useI18n()

const presetOptions = computed<SelectOption[]>(() => [
  { label: t('analytics.rangeToday'), value: 'today' },
  { label: t('analytics.rangeYesterday'), value: 'yesterday' },
  { label: t('analytics.rangeLast7d'), value: 'last7d' },
  { label: t('analytics.rangeLast30d'), value: 'last30d' },
  { label: t('analytics.rangeCustom'), value: 'custom' },
])

// Internal custom-range state as [startMs, endMs] (what NDatePicker daterange
// emits in the browser's local zone). Seeded from modelValue when mounting on
// "custom" (e.g. a detail page drilled from another view carries its window).
const customRange = ref<[number, number] | null>(
  props.preset === 'custom' ? modelValueToCustomRange(props.modelValue) : null,
)

function modelValueToCustomRange(v: TimeRange): [number, number] | null {
  if (!v.start || !v.end) return null
  // modelValue.end is exclusive (start of next day); the picker's last value
  // is the inclusive last day — step back one.
  const endInclusive = new Date(v.end)
  endInclusive.setDate(endInclusive.getDate() - 1)
  return [new Date(v.start).getTime(), endInclusive.getTime()]
}

// resolvePreset returns the [start, end) window for a named preset in the
// browser's local timezone. end is exclusive (start of tomorrow).
function resolvePreset(p: RangePreset): TimeRange {
  const today = new Date()
  today.setHours(0, 0, 0, 0)
  const tomorrow = new Date(today)
  tomorrow.setDate(tomorrow.getDate() + 1)
  switch (p) {
    case 'today':
      return { start: today.toISOString(), end: tomorrow.toISOString() }
    case 'yesterday': {
      const yesterday = new Date(today)
      yesterday.setDate(yesterday.getDate() - 1)
      return { start: yesterday.toISOString(), end: today.toISOString() }
    }
    case 'last7d': {
      const start = new Date(today)
      start.setDate(start.getDate() - 6) // 7 calendar days inclusive of today
      return { start: start.toISOString(), end: tomorrow.toISOString() }
    }
    case 'last30d': {
      const start = new Date(today)
      start.setDate(start.getDate() - 29) // 30 calendar days inclusive of today
      return { start: start.toISOString(), end: tomorrow.toISOString() }
    }
    default:
      return { start: null, end: null }
  }
}

function onPresetChange(v: RangePreset) {
  emit('update:preset', v)
  if (v === 'custom') return
  emit('update:modelValue', resolvePreset(v))
}

function onCustomChange(v: [number, number] | null) {
  customRange.value = v
  if (!v) {
    emit('update:modelValue', { start: null, end: null })
    return
  }
  const start = new Date(v[0])
  const end = new Date(v[1])
  end.setDate(end.getDate() + 1) // make end exclusive
  emit('update:modelValue', { start: start.toISOString(), end: end.toISOString() })
}

// Re-derive the window when the parent resets preset to a named window.
watch(
  () => props.preset,
  (p) => {
    if (p !== 'custom') emit('update:modelValue', resolvePreset(p))
  },
  { immediate: true },
)
</script>

<style scoped>
.time-range {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
}
</style>
