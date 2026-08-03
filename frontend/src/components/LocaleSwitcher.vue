<!-- Shared language switcher — used by both DefaultLayout.vue (inside the
     protected app shell) and AuthCard.vue (setup/login pages, which render
     outside DefaultLayout — see router/index.ts), so adding a language or
     changing the option list only needs one edit instead of two kept in
     lockstep by hand. Borderless ghost trigger + dropdown menu; the active
     language carries a check mark. -->
<template>
  <!-- Desktop: borderless trigger + naive dropdown menu. -->
  <n-dropdown
    v-if="!isMobile"
    trigger="click"
    placement="bottom-end"
    :options="options"
    @select="onLocaleChange"
  >
    <button class="locale-switch" type="button" aria-haspopup="menu">
      <NIcon :size="16" class="locale-switch__globe"><Globe /></NIcon>
      <span class="locale-switch__label">{{ currentLabel }}</span>
      <NIcon :size="14" class="locale-switch__chevron"><ChevronDown /></NIcon>
    </button>
  </n-dropdown>

  <!-- Mobile: the same trigger raises a bottom sheet of languages, mirroring
       the TimeRangeSelect interaction so the app shell reads consistently on a
       phone. -->
  <template v-else>
    <button class="locale-switch" type="button" aria-haspopup="menu" @click="sheetOpen = true">
      <NIcon :size="16" class="locale-switch__globe"><Globe /></NIcon>
      <span class="locale-switch__label">{{ currentLabel }}</span>
      <NIcon :size="14" class="locale-switch__chevron"><ChevronDown /></NIcon>
    </button>

    <NDrawer v-model:show="sheetOpen" placement="bottom" :height="sheetHeight" class="locale-sheet">
      <NDrawerContent :native-scrollbar="false" body-content-style="padding: 0;">
        <div class="locale-sheet__body">
          <div class="locale-sheet__handle" />
          <button
            v-for="l in LOCALES"
            :key="l.value"
            type="button"
            class="locale-sheet__option"
            :class="{ 'locale-sheet__option--active': l.value === locale.locale }"
            @click="onSheetSelect(l.value)"
          >
            <span>{{ l.label }}</span>
            <NIcon v-if="l.value === locale.locale" :size="18"><Check /></NIcon>
          </button>
        </div>
      </NDrawerContent>
    </NDrawer>
  </template>
</template>

<script setup lang="ts">
import { computed, h, ref } from 'vue'
import { NDrawer, NDrawerContent, NIcon, type DropdownOption } from 'naive-ui'
import { Globe, ChevronDown, Check } from '@lucide/vue'
import { useLocaleStore } from '../store/locale'
import { useIsMobile } from '../composables/useIsMobile'
import { LOCALES } from '../i18n'

const locale = useLocaleStore()

const currentLabel = computed(
  () => LOCALES.find((l) => l.value === locale.locale)?.label ?? LOCALES[0].label,
)

// Close the sheet if the viewport grows back to desktop, so it never strands
// an open overlay over the dropdown trigger (same guard as TimeRangeSelect).
const sheetOpen = ref(false)
const sheetHeight = 200
const isMobile = useIsMobile(() => {
  sheetOpen.value = false
})

// A leading check mark marks the active language; inactive rows reserve the
// same slot (a transparent placeholder) so labels stay left-aligned.
const options = computed<DropdownOption[]>(() =>
  LOCALES.map((l) => ({
    label: l.label,
    key: l.value,
    icon: () =>
      h(
        NIcon,
        { style: l.value === locale.locale ? undefined : { opacity: 0 } },
        { default: () => h(Check) },
      ),
  })),
)

function onLocaleChange(value: 'zh-CN' | 'en') {
  locale.setLocale(value)
}

function onSheetSelect(value: 'zh-CN' | 'en') {
  sheetOpen.value = false
  locale.setLocale(value)
}
</script>

<style scoped>
.locale-switch {
  display: inline-flex;
  align-items: center;
  gap: var(--space-2);
  height: 34px;
  padding: 0 10px;
  border: none;
  border-radius: var(--radius-md);
  background: transparent;
  color: var(--color-text-secondary);
  font: inherit;
  font-size: var(--text-sm);
  cursor: pointer;
  transition: background var(--duration-fast) var(--ease-out), color var(--duration-fast) var(--ease-out);
}

.locale-switch:hover {
  background: var(--color-surface-hover);
  color: var(--color-text);
}

.locale-switch__globe {
  color: var(--color-text-muted);
}

.locale-switch__chevron {
  color: var(--color-text-muted);
}

/* Rounded top corners on the bottom sheet — naive's drawer is square by
   default. Mirrors the TimeRangeSelect sheet. */
:deep(.locale-sheet.n-drawer) {
  border-top-left-radius: var(--radius-xl);
  border-top-right-radius: var(--radius-xl);
  overflow: hidden;
}

.locale-sheet__body {
  display: flex;
  flex-direction: column;
  padding: var(--space-2) var(--space-3) var(--space-4);
}

.locale-sheet__handle {
  width: 36px;
  height: 4px;
  margin: var(--space-2) auto var(--space-3);
  border-radius: var(--radius-full);
  background: var(--color-border);
}

.locale-sheet__option {
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

.locale-sheet__option:active {
  background: var(--color-surface-hover);
}

.locale-sheet__option--active {
  color: var(--color-accent);
  font-weight: 600;
}
</style>
