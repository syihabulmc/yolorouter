<!-- frontend/src/components/common/OptionSheet.vue
     The single mobile bottom-sheet this app draws. Every responsive control
     that needs a phone overlay — a value picker (FilterSelectField,
     TimeRangeSelect, the locale switch), an action menu (ResponsiveDropdown),
     or a bespoke layout — renders through this component so there is one
     handle, one title, one rounded surface, and one set of row styles.

     Two usage shapes share the same shell:

       1. Direct value picker: pass `options` + `value`, listen on `select`.
          OptionSheet renders and highlights the rows. Use this for plain
          {label,value} lists (the locale switch, TimeRangeSelect).

       2. Custom body: drop a default slot. OptionSheet renders only the shell
          (handle + optional title) and the caller fills the list — e.g. a
          searchable filter (FilterSelectField) or a divider/danger action menu
          (ResponsiveDropdown). Use the `.option-sheet__option` / `--active`
          classes so those rows stay visually identical to the direct rows.

     The two shapes are deliberately kept in one component rather than split
     into a ShellSheet + a separate picker: every consumer needs the same
     handle/title/surface, and only the list body differs, so one shell with a
     slot plus a built-in picker fallback is less moving parts than two types
     callers must choose between. In slot mode `options`/`value`/`select` are
     unused (left optional precisely to permit that).

     The parent owns the open state via `v-model:show`. Generic over
     string | number so it serves both id-valued and name/enum-valued lists.

     Note: CLAUDE.md bans NDrawer for interactive overlays in favour of
     NModal(preset="card"), but the mobile bottom-sheet is the same responsive
     exception ModalDrawer already makes (desktop NModal, mobile NDrawer). -->
<template>
  <NDrawer v-model:show="show" placement="bottom" :height="height" class="option-sheet">
    <NDrawerContent :native-scrollbar="false" body-content-style="padding: 0;">
      <div class="option-sheet__body">
        <div class="option-sheet__handle" />
        <div v-if="title" class="option-sheet__title">{{ title }}</div>

        <!-- Slot callers own the list; render nothing here so their content is
             the only body. -->
        <slot>
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
        </slot>
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
    options?: { label: string; value: Value }[]
    value?: Value | null
    height?: number
  }>(),
  {
    title: '',
    options: () => [],
    value: null,
    height: 320,
  },
)

const emit = defineEmits<{
  select: [value: Value]
}>()

const show = defineModel<boolean>('show', { required: true })

// Selecting a row emits it and closes the sheet — the parent applies the value.
// Slot callers wire their own row clicks, so this only fires in the direct
// value-picker shape.
function onSelect(v: Value) {
  emit('select', v)
  show.value = false
}
</script>

<style scoped>
/* Rounded top corners on the bottom sheet — naive's drawer is square by
   default. This is the only place that rule lives; every sheet inherits it by
   going through OptionSheet. */
:deep(.option-sheet.n-drawer) {
  border-top-left-radius: var(--radius-xl);
  border-top-right-radius: var(--radius-xl);
  overflow: hidden;
}

/* The drawer's scroll-content wrapper must pass the drawer's height down to
   `.option-sheet__body` (height: 100%), otherwise the slot callers' `flex:1`
   lists have nothing to fill and overflow the drawer instead of scrolling. */
:deep(.n-drawer-body-content-wrapper) {
  height: 100%;
}

.option-sheet__body {
  display: flex;
  flex-direction: column;
  /* Fill the drawer content area so this is a height-constrained flex parent:
     the direct `.option-sheet__list` and slot callers' `flex:1` lists can then
     take the remaining height and scroll instead of overflowing the drawer. */
  height: 100%;
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
</style>

<!-- Shared row styles, kept scoped via :slotted() instead of a global block.
     :slotted() reaches content a caller renders into the default slot
     (FilterSelectField / ResponsiveDropdown rows); the same rules are repeated
     on the plain selector for this component's own fallback rows (the direct
     value-picker list, which is OptionSheet's own scoped content, not slot
     content, so :slotted() does not cover it). -->
<style scoped>
.option-sheet__option,
:slotted(.option-sheet__option) {
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

.option-sheet__option > span,
:slotted(.option-sheet__option > span) {
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}

.option-sheet__option:active,
:slotted(.option-sheet__option:active) {
  background: var(--color-surface-hover);
}

.option-sheet__option:disabled,
:slotted(.option-sheet__option:disabled) {
  cursor: not-allowed;
  opacity: 0.5;
}

.option-sheet__option--active,
:slotted(.option-sheet__option--active) {
  color: var(--color-accent);
  font-weight: 600;
}
</style>
