<!-- frontend/src/components/common/FilterSelectField.vue
     A single labelled filter select that adapts to viewport width.

       - Desktop: a labelled naive NSelect inline, the usual filter-bar control.
       - Mobile: the same label with a compact trigger button that raises a
         bottom sheet of the options (searchable when `filterable`).

     The mobile sheet reuses OptionSheet for the shell (handle / title /
     rounded surface) and the shared row style; only the searchable body is
     bespoke to this control.

     Controlled input: the parent owns the value; this component reads the
     `value` prop and emits `update:value`. Generic over string | number so it
     serves both id-valued (caller/provider) and name/enum-valued (model/status)
     filters. -->
<template>
  <div class="filter-item">
    <!-- Desktop: inline select. -->
    <NSelect
      v-if="!isMobile"
      :value="value"
      :options="options"
      :size="size"
      :multiple="multiple"
      :tag="tag"
      :loading="loading"
      :clearable="clearable"
      :filterable="filterable"
      :placeholder="placeholder"
      :style="{ width }"
      @update:value="(v: SelectValue) => emit('update:value', (v ?? null) as unknown as Value | null)"
    />

    <!-- Mobile: trigger + bottom sheet. -->
    <template v-else>
      <NButton :size="size" class="filter-select__trigger" @click="openSheet" icon-placement="right">
        <span class="filter-select__trigger-label" :class="{ 'is-placeholder': isEmpty }">
          {{ currentLabel }}
        </span>
        <template #icon><ChevronDown :size="14" /></template>
      </NButton>

      <!-- OptionSheet owns the shell + the shared `.option-sheet__option` row
           style; the slot only fills the searchable list body. -->
      <OptionSheet
        v-model:show="sheetOpen"
        :title="label || placeholder"
        :height="sheetHeight"
      >
        <NInput
          v-if="filterable"
          v-model:value="search"
          size="small"
          clearable
          :placeholder="placeholder"
          class="filter-sheet__search"
        />

        <div class="filter-sheet__list">
          <!-- Clear row: selecting it drops the constraint (placeholder is
               the "all X" label the parent passes). -->
          <button
            v-if="clearable"
            type="button"
            class="option-sheet__option"
            :class="{ 'option-sheet__option--active': isEmpty }"
            @click="select(null)"
          >
            <span>{{ placeholder }}</span>
            <NIcon v-if="isEmpty" :size="18"><Check /></NIcon>
          </button>

          <button
            v-for="opt in visibleOptions"
            :key="String(opt.value)"
            type="button"
            class="option-sheet__option"
            :class="{ 'option-sheet__option--active': isActive(opt.value as Value) }"
            @click="select(opt.value as Value)"
          >
            <span>{{ opt.label }}</span>
            <NIcon v-if="isActive(opt.value as Value)" :size="18"><Check /></NIcon>
          </button>

          <p v-if="!visibleOptions.length" class="filter-sheet__empty">{{ placeholder }}</p>
        </div>
      </OptionSheet>
    </template>
  </div>
</template>

<script setup lang="ts" generic="Value extends string | number">
import { computed, ref } from 'vue'
import { NButton, NIcon, NInput, NSelect, type SelectOption } from 'naive-ui'
import { Check, ChevronDown } from '@lucide/vue'
import OptionSheet from './OptionSheet.vue'
import { useIsMobile } from '../../composables/useIsMobile'

type SelectValue = Value | Value[] | null

const props = withDefaults(
  defineProps<{
    label?: string
    value: Value | Value[] | null
    options: SelectOption[]
    placeholder?: string
    clearable?: boolean
    filterable?: boolean
    multiple?: boolean
    tag?: boolean
    loading?: boolean
    size?: 'tiny' | 'small' | 'medium' | 'large'
    width?: string
    sheetHeight?: number
  }>(),
  {
    label: '',
    placeholder: '',
    clearable: true,
    filterable: false,
    multiple: false,
    tag: false,
    loading: false,
    size: 'small',
    width: '200px',
    sheetHeight: 350
  },
)
// Public emit stays single-valued so the many single-select callers keep their
// (Value | null) handlers; multiple-select callers pass their own cast handler
// and receive the array at runtime (emitted via a cast below).
const emit = defineEmits<{
  'update:value': [value: Value | null]
}>()

const search = ref('')

// Close the sheet if the viewport grows back to desktop, so it never strands
// an open overlay over the inline control (same guard as TimeRangeSelect).
const sheetOpen = ref(false)
const isMobile = useIsMobile(() => {
  sheetOpen.value = false
})

// Selected values normalized to an array so single- and multiple-select share
// one code path for the mobile sheet's active state and trigger label.
const selectedValues = computed<Value[]>(() => {
  const v = props.value
  if (v == null) return []
  return Array.isArray(v) ? v : [v]
})
const isEmpty = computed(() => selectedValues.value.length === 0)

function isActive(v: Value): boolean {
  return props.multiple ? selectedValues.value.includes(v) : v === props.value
}

// Trigger label: the selected option label(s), or the placeholder when unset.
// In multiple mode the chosen labels are joined so the collapsed trigger still
// shows what is selected.
const currentLabel = computed(() => {
  if (isEmpty.value) return props.placeholder
  if (props.multiple) {
    const labels = props.options
      .filter((o) => selectedValues.value.includes(o.value as Value))
      .map((o) => o.label as string)
    return labels.length ? labels.join(', ') : props.placeholder
  }
  const match = props.options.find((o) => o.value === props.value)
  return (match?.label as string) ?? props.placeholder
})

// Client-side search over the option labels — the option lists are small
// admin catalogs already held in memory, so filtering locally is instant and
// avoids a round trip.
const visibleOptions = computed(() => {
  const q = search.value.trim().toLowerCase()
  if (!q) return props.options
  return props.options.filter((o) => String(o.label ?? '').toLowerCase().includes(q))
})

function openSheet() {
  search.value = ''
  sheetOpen.value = true
}

function select(v: Value | null) {
  if (props.multiple) {
    // Toggle within the current selection and keep the sheet open so several
    // options can be picked in one pass; the clear row (v == null) drops all.
    if (v == null) {
      emit('update:value', [] as unknown as Value)
      return
    }
    const cur = selectedValues.value.slice()
    const i = cur.indexOf(v)
    if (i >= 0) cur.splice(i, 1)
    else cur.push(v)
    emit('update:value', cur as unknown as Value)
    return
  }
  emit('update:value', v)
  sheetOpen.value = false
}
</script>

<style scoped>
.filter-select__trigger {
  min-width: 140px;
  justify-content: space-between;
}

.filter-select__trigger-label {
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}

.filter-select__trigger-label.is-placeholder {
  color: var(--color-text-muted);
}

/* The slot body fills OptionSheet's drawer content; give the searchable list
   room to scroll beneath the search input. */
.filter-sheet__search {
  margin: 0 var(--space-3) var(--space-2);
  width: auto;
}

.filter-sheet__list {
  flex: 1;
  overflow-y: auto;
}

.filter-sheet__empty {
  padding: var(--space-4) var(--space-3);
  text-align: center;
  color: var(--color-text-muted);
  font-size: var(--text-sm);
}
</style>
