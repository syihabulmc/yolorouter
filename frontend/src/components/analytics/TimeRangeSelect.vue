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
  <template v-if="isMobile">
    <NButton size="small" class="filter-select__trigger" @click="sheetOpen = true" icon-placement="right">
      <span class="filter-select__trigger-label">{{ currentPresetLabel }}</span>
      <template #icon><ChevronDown :size="14" /></template>
    </NButton>
    <NDrawer v-model:show="sheetOpen" placement="bottom" :height="sheetHeight" class="filter-select-sheet">
      <NDrawerContent :native-scrollbar="false" body-content-style="padding: 0;">
        <div class="filter-sheet">
          <div class="filter-sheet__handle" />
            <div class="filter-sheet__title">{{ t('analytics.timeRange') }}</div>

          <button
            v-for="opt in mobilePresetOptions"
            :key="opt.value"
            type="button"
            class="filter-sheet__option"
            :class="{ 'filter-sheet__option--active': opt.value === preset }"
            @click="onSheetSelect(opt.value)"
          >
            <span>{{ opt.label }}</span>
            <NIcon v-if="opt.value === preset" :size="18"><Check /></NIcon>
          </button>
        </div>
      </NDrawerContent>
    </NDrawer>
  </template>
  <!-- Desktop: the full select + inline custom daterange picker. -->
  <div v-else class="time-range">
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
import { NButton, NDatePicker, NDrawer, NDrawerContent, NIcon, NSelect, type SelectOption } from 'naive-ui'
import { Check, ChevronDown } from '@lucide/vue'
import { useIsMobile } from '../../composables/useIsMobile'

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

// Bottom-sheet open state + a height that fits the four preset rows plus the
// drag handle without leaving a tall empty gap.
const sheetOpen = ref(false)
const sheetHeight = 300

// Below the mobile breakpoint the select+picker pair is replaced by a single
// dropdown button (see template). Leaving mobile with the sheet still open
// would strand an invisible overlay over the desktop controls — close it on
// the way out.
const isMobile = useIsMobile(() => {
  sheetOpen.value = false
})

// The mobile sheet drops "custom" — the inline daterange picker has no room on
// a phone filter bar. Typed as {label, value} so the template can key/compare
// on `value` directly (matching RangePreset).
const mobilePresetOptions = computed(() =>
  presetOptions.value
    .filter((o) => o.value !== 'custom')
    .map((o) => ({ label: o.label as string, value: o.value as RangePreset })),
)

function onSheetSelect(v: RangePreset) {
  sheetOpen.value = false
  onPresetChange(v)
}

// Label shown on the mobile trigger button. A custom range (drilled in from a
// detail page) has no named preset, so fall back to the custom label.
const currentPresetLabel = computed(
  () => presetOptions.value.find((o) => o.value === props.preset)?.label ?? t('analytics.rangeCustom'),
)

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

// Tracks the preset the component itself emitted via onPresetChange, so the
// props.preset watcher can skip the duplicate emit that would otherwise fire
// when the parent round-trips the update:preset back into props.
let selfEmittedPreset: RangePreset | null = null

function onPresetChange(v: RangePreset) {
  selfEmittedPreset = v
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
// Skips the self-emitted round-trip from onPresetChange to avoid a duplicate
// reload (the parent updates preset → this watcher fires → emits again →
// parent watch fires again → double request).
watch(
  () => props.preset,
  (p) => {
    if (p === selfEmittedPreset) {
      selfEmittedPreset = null
      return
    }
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

.filter-select__trigger {
  min-width: 120px;
  justify-content: space-between;
}

.filter-select__trigger-label {
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}

/* Rounded top corners on the bottom sheet — naive's drawer is square by
   default. The class is set on <NDrawer> but the rounded surface is the
   drawer container, so target it via :deep. */
:deep(.filter-select-sheet.n-drawer) {
  border-top-left-radius: var(--radius-xl);
  border-top-right-radius: var(--radius-xl);
  overflow: hidden;
}

.filter-sheet {
  display: flex;
  flex-direction: column;
  min-height: 0;
  padding: var(--space-2) var(--space-3) var(--space-4);
}

.filter-sheet__handle {
  width: 36px;
  height: 4px;
  margin: var(--space-2) auto var(--space-2);
  border-radius: var(--radius-full);
  background: var(--color-border);
}

.filter-sheet__title {
  font-size: var(--text-xs);
  font-weight: 700;
  letter-spacing: 0.05em;
  text-transform: uppercase;
  color: var(--color-text-muted);
  padding: 0 var(--space-3);
  margin-bottom: var(--space-2);
}

.filter-sheet__option {
  display: flex;
  align-items: center;
  justify-content: space-between;
  height: 48px;
  padding: 0 var(--space-3);
  border: none;
  border-radius: var(--radius-md);
  background: transparent;
  color: var(--color-text);
  font: inherit;
  font-size: var(--text-base);
  text-align: left;
  cursor: pointer;
  transition: background var(--duration-fast) var(--ease-out);
}

.filter-sheet__option:active {
  background: var(--color-surface-hover);
}

.filter-sheet__option--active {
  color: var(--color-accent);
  font-weight: 600;
}

</style>
