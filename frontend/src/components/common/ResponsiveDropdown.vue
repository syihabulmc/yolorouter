<!-- frontend/src/components/common/ResponsiveDropdown.vue
     A responsive action menu that adapts to viewport width.

       - Desktop: a naive NDropdown — the trigger lives in the default slot and
         the menu opens as the usual anchored popover.
       - Mobile: the same trigger raises a bottom sheet listing the options,
         reusing OptionSheet for the shell so every phone overlay reads the
         same.

     API mirrors NDropdown (options / placement / trigger / disabled + a
     `select` event) so the `h(NDropdown, …)` call sites swap over with almost
     no change. Unlike the OptionSheet value picker this is an action menu:
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

    <!-- OptionSheet owns the shell; the slot fills the action list. Rows reuse
         the shared `.option-sheet__option` style (with `:disabled` supported);
         dividers and danger styling are local to this menu. -->
    <OptionSheet v-model:show="sheetOpen" :title="title" :height="height">
      <div class="rd-sheet__list">
        <template v-for="(opt, i) in options" :key="opt.key ?? `d-${i}`">
          <div v-if="opt.type === 'divider'" class="rd-sheet__divider" />
          <button
            v-else
            type="button"
            class="option-sheet__option"
            :disabled="opt.disabled"
            :style="(opt.props?.style as any)"
            :title="(opt.props?.title as string | undefined)"
            @click="pick(opt)"
          >
            <span>{{ opt.label }}</span>
          </button>
        </template>
      </div>
    </OptionSheet>
  </template>
</template>

<script setup lang="ts">
import { ref } from 'vue'
import { NDropdown, NButton, type DropdownOption, type DropdownProps } from 'naive-ui'
import OptionSheet from './OptionSheet.vue'
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

<style scoped>
/* The trigger wrapper must not disturb the button's own layout. */
.rd-trigger {
  display: inline-flex;
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
</style>
