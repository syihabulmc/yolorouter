<!-- frontend/src/components/analytics/TimeRangeSelect.vue
     Time range picker shared between the AnalyticsPage filter bar and any
     future dashboard that wants the same presets. Emits a {start,end} tuple
     in RFC3339 form (server's /analytics endpoints expect RFC3339).

     Preset list: Today / Yesterday / Last 7d / Last 30d / Custom. "Custom"
     reveals an NDatePicker range panel; NDatePicker is not in main.ts's
     create() components list, so it's imported explicitly. -->
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
import { useAuthStore } from '../../store/auth'

// RangePreset enumerates the named windows the picker offers. The string
// values are internal (not sent to the backend) — the backend receives
// resolved start/end timestamps only.
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
const authStore = useAuthStore()

// startOfTodayInZone returns the UTC instant of local-midnight "today" in a
// timezone given by its UTC offset (minutes east of UTC). When offset is null
// (server offset not yet known — e.g. before /auth/me resolves), it falls
// back to the browser's own timezone, preserving the pre-fix behavior for the
// dev case where server and browser share a zone. Offset-based computation
// avoids pulling a timezone library; DST is correct for "today" because the
// server's CURRENT offset is what's in effect right now (today is by the
// system's current timezone).
function startOfTodayInZone(offsetMinutes: number | null): Date {
  const offset = offsetMinutes ?? -new Date().getTimezoneOffset()
  const serverNowMs = Date.now() + offset * 60_000
  const serverNow = new Date(serverNowMs)
  // Server wall-clock midnight expressed as a UTC instant of that Y-M-D, then
  // shifted back by the offset to the true UTC time the server would store.
  const serverMidnightMs = Date.UTC(
    serverNow.getUTCFullYear(),
    serverNow.getUTCMonth(),
    serverNow.getUTCDate(),
    0,
    0,
    0,
    0,
  )
  return new Date(serverMidnightMs - offset * 60_000)
}

// calendarDateInZone returns the calendar {year, month, day} the given UTC
// instant falls on in a timezone specified by its UTC offset (minutes east of
// UTC); null offset falls back to the browser zone. midnightInstantInZone is
// its inverse — the UTC instant of local midnight (00:00) on a calendar date
// in the target zone. Together they move dates across the browser/server
// timezone boundary without pulling a timezone library.
function calendarDateInZone(ms: number, offsetMinutes: number | null): { y: number; m: number; d: number } {
  const offset = offsetMinutes ?? -new Date().getTimezoneOffset()
  const zoned = new Date(ms + offset * 60_000)
  return { y: zoned.getUTCFullYear(), m: zoned.getUTCMonth(), d: zoned.getUTCDate() }
}
function midnightInstantInZone(y: number, m: number, d: number, offsetMinutes: number | null): number {
  const offset = offsetMinutes ?? -new Date().getTimezoneOffset()
  return Date.UTC(y, m, d, 0, 0, 0, 0) - offset * 60_000
}

const presetOptions = computed<SelectOption[]>(() => [
  { label: t('analytics.rangeToday'), value: 'today' },
  { label: t('analytics.rangeYesterday'), value: 'yesterday' },
  { label: t('analytics.rangeLast7d'), value: 'last7d' },
  { label: t('analytics.rangeLast30d'), value: 'last30d' },
  { label: t('analytics.rangeCustom'), value: 'custom' },
])

// Internal custom-range state — kept as a tuple [startMs, endMs] because
// that's what NDatePicker daterange emits. We localize the boundaries to
// the user's timezone via toISOString() at emit time so the server's UTC
// storage gets compared correctly.
//
// Seeded from modelValue when the component mounts already on the "custom"
// preset (a detail page drilled from another analytics view carries its
// window in the URL); otherwise the picker renders blank despite an active
// custom range. modelValue carries SERVER-local-midnight instants; convert
// each server calendar day to a browser-local-midnight timestamp so the
// (browser-local) picker displays the same dates. modelValue.end is exclusive
// (start of next day), so the picker's inclusive last-day value steps back one
// server day before converting.
function modelValueToCustomRange(v: TimeRange): [number, number] | null {
  if (!v.start || !v.end) return null
  const off = authStore.serverTimezoneOffset
  const sd = calendarDateInZone(new Date(v.start).getTime(), off)
  const startMs = midnightInstantInZone(sd.y, sd.m, sd.d, null)
  const ed = calendarDateInZone(new Date(v.end).getTime() - 24 * 60 * 60 * 1000, off)
  const endMs = midnightInstantInZone(ed.y, ed.m, ed.d, null)
  return [startMs, endMs]
}
const customRange = ref<[number, number] | null>(
  props.preset === 'custom' ? modelValueToCustomRange(props.modelValue) : null,
)

// resolvePreset returns the [start, end) window for a named preset, in the
// user's local timezone. end is exclusive (the start of tomorrow / the day
// after the range); start is inclusive (local midnight of the appropriate
// day). Mirrors the Go side's TodayBounds logic — both sides use local
// midnight so "today" means the same thing on both sides of the wire.
function resolvePreset(p: RangePreset): TimeRange {
  const startOfToday = startOfTodayInZone(authStore.serverTimezoneOffset)
  const endOfToday = new Date(startOfToday)
  endOfToday.setDate(endOfToday.getDate() + 1)
  switch (p) {
    case 'today':
      return { start: startOfToday.toISOString(), end: endOfToday.toISOString() }
    case 'yesterday': {
      const start = new Date(startOfToday)
      start.setDate(start.getDate() - 1)
      return { start: start.toISOString(), end: startOfToday.toISOString() }
    }
    case 'last7d': {
      const start = new Date(startOfToday)
      start.setDate(start.getDate() - 6) // 7 calendar days inclusive of today
      return { start: start.toISOString(), end: endOfToday.toISOString() }
    }
    case 'last30d': {
      const start = new Date(startOfToday)
      start.setDate(start.getDate() - 29) // 30 calendar days inclusive of today
      return { start: start.toISOString(), end: endOfToday.toISOString() }
    }
    default:
      // custom is handled by onCustomChange — no preset window here.
      return { start: null, end: null }
  }
}

function onPresetChange(v: RangePreset) {
  emit('update:preset', v)
  if (v === 'custom') {
    // Don't emit a new range — leave the existing customRange as the source
    // of truth. The user opens the picker and selects the next window.
    return
  }
  emit('update:modelValue', resolvePreset(v))
}

function onCustomChange(v: [number, number] | null) {
  customRange.value = v
  if (!v) {
    emit('update:modelValue', { start: null, end: null })
    return
  }
  const [startMs, endMs] = v
  // The picker emits browser-local-midnight timestamps for the calendar days
  // the user selected. Resolve those days to SERVER-local midnights so custom
  // ranges line up with the named presets (and the backend's server-timezone
  // grouping) regardless of the browser zone. When browser and server share a
  // zone the emitted instants are identical to a direct toISOString() of the
  // picker timestamps — no behavior change for the common same-zone case.
  const off = authStore.serverTimezoneOffset
  const sd = calendarDateInZone(startMs, null)
  const ed = calendarDateInZone(endMs, null)
  const start = midnightInstantInZone(sd.y, sd.m, sd.d, off)
  const end = midnightInstantInZone(ed.y, ed.m, ed.d, off) + 24 * 60 * 60 * 1000
  emit('update:modelValue', { start: new Date(start).toISOString(), end: new Date(end).toISOString() })
}

// When the parent resets preset back to a named window (e.g. another tab's
// defaults load), re-derive the window. Without this, switching from
// "custom" back to "today" leaves the stale custom range on the wire.
watch(
  () => props.preset,
  (p) => {
    if (p !== 'custom') {
      emit('update:modelValue', resolvePreset(p))
    }
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
