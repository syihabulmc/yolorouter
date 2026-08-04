<!-- frontend/src/components/common/ResponsiveDropdown.vue
     A responsive action menu that adapts to viewport width.

       - Desktop: a naive NDropdown — the trigger lives in the default slot and
         the menu opens as the usual anchored popover.
       - Mobile: the same trigger raises a bottom sheet listing the options,
         mirroring the OptionSheet / FilterSelectField interaction so action
         menus read consistently on a phone.

     API mirrors NDropdown (options / placement / trigger / disabled + a
     `select` event) so the `h(NDropdown, …)` call sites swap over with almost
     no change. Unlike OptionSheet this is an action menu, not a value picker:
     there is no selected value / checkmark, and it renders dividers, disabled
     rows and danger styling (via each option's `props.style`). -->
<template>
  <!-- Desktop: naive dropdown, trigger passed straight through. -->
  <NDropdown
    v-if="!isMobile"
    :options="options"
    :placement="placement"
    :trigger="trigger"
    :disabled="disabled"
    @select="(key: string | number) => emit('select', String(key))"
  >
    <slot />
  </NDropdown>

  <!-- Mobile: text-button trigger + bottom sheet. On a phone the icon-only
       "⋯" tap target is cramped, so we surface a labelled button instead
       (falling back to the slot trigger when no `triggerText` is given). -->
  <template v-else>
    <NButton
      v-if="triggerText"
      size="small"
      :disabled="disabled"
      :loading="loading"
      class="rd-trigger-btn"
      @click.stop="open"
    >
      {{ triggerText }}
    </NButton>
    <span v-else class="rd-trigger" @click.stop="open"><slot /></span>

    <NDrawer v-model:show="sheetOpen" placement="bottom" :height="height" class="rd-sheet">
      <NDrawerContent :native-scrollbar="false" body-content-style="padding: 0;">
        <div class="rd-sheet__body">
          <div class="rd-sheet__handle" />
          <div v-if="title" class="rd-sheet__title">{{ title }}</div>

          <div class="rd-sheet__list">
            <template v-for="(opt, i) in options" :key="opt.key ?? `d-${i}`">
              <div v-if="opt.type === 'divider'" class="rd-sheet__divider" />
              <button
                v-else
                type="button"
                class="rd-sheet__option"
                :disabled="opt.disabled"
                :style="(opt.props?.style as any)"
                :title="(opt.props?.title as string | undefined)"
                @click="pick(opt)"
              >
                <span>{{ opt.label }}</span>
              </button>
            </template>
          </div>
        </div>
      </NDrawerContent>
    </NDrawer>
  </template>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { NDropdown, NDrawer, NDrawerContent, NButton, type DropdownOption, type DropdownProps } from 'naive-ui'
import { useIsMobile } from '../../composables/useIsMobile'

const props = withDefaults(
  defineProps<{
    options: DropdownOption[]
    placement?: DropdownProps['placement']
    trigger?: 'click' | 'hover'
    disabled?: boolean
    loading?: boolean
    title?: string
    height?: number
    // Text shown on the mobile trigger button. When set, the phone view renders
    // a labelled button instead of wrapping the (icon) default slot.
    triggerText?: string
  }>(),
  {
    placement: 'bottom-end',
    trigger: 'click',
    disabled: false,
    loading: false,
    title: '',
    height: 300,
    triggerText: '',
  },
)

const emit = defineEmits<{
  select: [key: string]
}>()

// Close the sheet if the viewport grows back to desktop, so it never strands an
// open overlay over the anchored dropdown (same guard as FilterSelectField).
const sheetOpen = ref(false)
const isMobile = useIsMobile(() => {
  sheetOpen.value = false
})

function open() {
  if (props.disabled) return
  sheetOpen.value = true
}

// Picking a row fires the action and closes the sheet; disabled rows are inert.
function pick(opt: DropdownOption) {
  if (opt.disabled || opt.key == null) return
  emit('select', String(opt.key))
  sheetOpen.value = false
}
</script>
<style>
 .rd-sheet {
  --n-border-radius: 25px;
  }
</style>
<style scoped>
/* The trigger wrapper must not disturb the button's own layout. */
.rd-trigger {
  display: inline-flex;
}

/* Rounded top corners on the bottom sheet — naive's drawer is square by
   default. Mirrors OptionSheet / FilterSelectField. */
  
:deep(.rd-sheet.n-drawer) {
  overflow: hidden;
}

.rd-sheet__body {
  display: flex;
  flex-direction: column;
  min-height: 0;
  padding: var(--space-2) var(--space-3) var(--space-4);
}

/* A short grab handle centered at the top, the usual bottom-sheet affordance. */
.rd-sheet__handle {
  width: 36px;
  height: 4px;
  margin: var(--space-2) auto var(--space-2);
  border-radius: var(--radius-full);
  background: var(--color-border);
}

.rd-sheet__title {
  padding: 0 var(--space-3);
  margin-bottom: var(--space-2);
  font-size: var(--text-xs);
  font-weight: 700;
  letter-spacing: 0.05em;
  text-transform: uppercase;
  color: var(--color-text-muted);
}

.rd-sheet__list {
  flex: 1;
  min-height: 0;
  overflow-y: auto;
}

.rd-sheet__divider {
  height: 1px;
  margin: var(--space-1) var(--space-3);
  background: var(--color-border);
}

.rd-sheet__option {
  display: flex;
  align-items: center;
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

.rd-sheet__option > span {
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}

.rd-sheet__option:active {
  background: var(--color-surface-hover);
}

.rd-sheet__option:disabled {
  cursor: not-allowed;
  opacity: 0.5;
}
</style>
