<!-- frontend/src/components/common/FilterSelectField.vue
     A single labelled filter select that adapts to viewport width.

       - Desktop: a labelled naive NSelect inline, the usual filter-bar control.
       - Mobile: the same label with a compact trigger button that raises a
         bottom sheet of the options (searchable when `filterable`), mirroring
         the TimeRangeSelect interaction so the whole filter bar reads
         consistently on a phone.

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
      :clearable="clearable"
      :filterable="filterable"
      :placeholder="placeholder"
      :style="{ width }"
      @update:value="(v: SelectValue) => emit('update:value', (v ?? null) as Value)"
    />

    <!-- Mobile: trigger + bottom sheet. -->
    <template v-else>
      <NButton :size="size" class="filter-select__trigger" @click="openSheet" icon-placement="right">
        <span class="filter-select__trigger-label" :class="{ 'is-placeholder': value == null }">
          {{ currentLabel }}
        </span>
        <template #icon><ChevronDown :size="14" /></template>
      </NButton>

      <NDrawer v-model:show="sheetOpen" placement="bottom" :height="sheetHeight" class="filter-select-sheet">
        <NDrawerContent :native-scrollbar="false" body-content-style="padding: 0;">
          <div class="filter-sheet">
            <div class="filter-sheet__handle" />
            <div class="filter-sheet__title">{{ label }}</div>

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
                class="filter-sheet__option"
                :class="{ 'filter-sheet__option--active': value == null }"
                @click="select(null)"
              >
                <span>{{ placeholder }}</span>
                <NIcon v-if="value == null" :size="18"><Check /></NIcon>
              </button>

              <button
                v-for="opt in visibleOptions"
                :key="String(opt.value)"
                type="button"
                class="filter-sheet__option"
                :class="{ 'filter-sheet__option--active': opt.value === value }"
                @click="select(opt.value as Value)"
              >
                <span>{{ opt.label }}</span>
                <NIcon v-if="opt.value === value" :size="18"><Check /></NIcon>
              </button>

              <p v-if="!visibleOptions.length" class="filter-sheet__empty">{{ placeholder }}</p>
            </div>
          </div>
        </NDrawerContent>
      </NDrawer>
    </template>
  </div>
</template>

<script setup lang="ts" generic="Value extends string | number">
import { computed, ref } from 'vue'
import { NButton, NDrawer, NDrawerContent, NIcon, NInput, NSelect, type SelectOption } from 'naive-ui'
import { Check, ChevronDown } from '@lucide/vue'
import { useIsMobile } from '../../composables/useIsMobile'

type SelectValue = string | number | null

const props = withDefaults(
  defineProps<{
    label: string
    value: Value | null
    options: SelectOption[]
    placeholder?: string
    clearable?: boolean
    filterable?: boolean
    size?: 'tiny' | 'small' | 'medium' | 'large'
    width?: string
  }>(),
  {
    placeholder: '',
    clearable: true,
    filterable: false,
    size: 'small',
    width: '200px',
  },
)
const emit = defineEmits<{
  'update:value': [value: Value | null]
}>()

const search = ref('')

// Close the sheet if the viewport grows back to desktop, so it never strands
// an open overlay over the inline control (same guard as TimeRangeSelect).
const sheetOpen = ref(false)
const sheetHeight = 420
const isMobile = useIsMobile(() => {
  sheetOpen.value = false
})

// Trigger label: the selected option's label, or the placeholder when unset.
const currentLabel = computed(() => {
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

/* Rounded top corners on the bottom sheet — naive's drawer is square by
   default. Mirrors the TimeRangeSelect sheet. */
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

.filter-sheet__search {
  margin: 0 var(--space-3) var(--space-2);
  width: auto;
}

.filter-sheet__list {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
}

.filter-sheet__option {
  display: flex;
  align-items: center;
  justify-content: space-between;
  width: 100%;
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

.filter-sheet__option > span {
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}

.filter-sheet__option:active {
  background: var(--color-surface-hover);
}

.filter-sheet__option--active {
  color: var(--color-accent);
  font-weight: 600;
}

.filter-sheet__empty {
  padding: var(--space-4) var(--space-3);
  text-align: center;
  color: var(--color-text-muted);
  font-size: var(--text-sm);
}
</style>
