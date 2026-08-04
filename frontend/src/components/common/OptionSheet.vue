<!-- frontend/src/components/common/OptionSheet.vue
     A reusable mobile bottom-sheet option picker. The parent owns the trigger
     and the open state (`v-model:show`); this component just renders the list
     and emits the chosen value. Extracted from the FilterSelectField /
     LocaleSwitcher sheet so option pickers read consistently on a phone.

     Generic over string | number so it serves both id-valued and
     name/enum-valued option lists. -->
<template>
  <NDrawer v-model:show="show" placement="bottom" :height="height" class="rd-sheet option-sheet">
    <NDrawerContent :native-scrollbar="false" body-content-style="padding: 0;">
      <div class="option-sheet__body">
        <div class="option-sheet__handle" />
        <div v-if="title" class="option-sheet__title">{{ title }}</div>

        <div class="option-sheet__list">
          <button
            v-for="opt in options"
            :key="String(opt.value)"
            type="button"
            class="option-sheet__option"
            :class="{ 'option-sheet__option--active': opt.value === value }"
            @click="onSelect(opt.value)"
          >
            <span>{{ opt.label }}</span>
            <NIcon v-if="opt.value === value" :size="18"><Check /></NIcon>
          </button>
        </div>
      </div>
    </NDrawerContent>
  </NDrawer>
</template>

<script setup lang="ts" generic="Value extends string | number">
import { NDrawer, NDrawerContent, NIcon } from 'naive-ui'
import { Check } from '@lucide/vue'

withDefaults(
  defineProps<{
    title?: string
    options: { label: string; value: Value }[]
    value: Value | null
    height?: number
  }>(),
  {
    title: '',
    height: 320,
  },
)

const emit = defineEmits<{
  select: [value: Value]
}>()

const show = defineModel<boolean>('show', { required: true })

// Selecting a row emits it and closes the sheet — the parent applies the value.
function onSelect(v: Value) {
  emit('select', v)
  show.value = false
}
</script>

<style scoped>
/* Rounded top corners on the bottom sheet — naive's drawer is square by
   default. Mirrors the FilterSelectField / TimeRangeSelect sheets. */
:deep(.option-sheet.n-drawer) {
  border-top-left-radius: var(--radius-xl);
  border-top-right-radius: var(--radius-xl);
  overflow: hidden;
}

.option-sheet__body {
  display: flex;
  flex-direction: column;
  min-height: 0;
  padding: var(--space-2) var(--space-3) var(--space-4);
}

/* A short grab handle centered at the top, the usual bottom-sheet affordance. */
.option-sheet__handle {
  width: 36px;
  height: 4px;
  margin: var(--space-2) auto var(--space-2);
  border-radius: var(--radius-full);
  background: var(--color-border);
}

.option-sheet__title {
  padding: 0 var(--space-3);
  margin-bottom: var(--space-2);
  font-size: var(--text-xs);
  font-weight: 700;
  letter-spacing: 0.05em;
  text-transform: uppercase;
  color: var(--color-text-muted);
}

.option-sheet__list {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
}

.option-sheet__option {
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

.option-sheet__option > span {
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}

.option-sheet__option:active {
  background: var(--color-surface-hover);
}

.option-sheet__option--active {
  color: var(--color-accent);
  font-weight: 600;
}
</style>
