<!-- Sidebar nav renderer. Supports four entry kinds in a single flat, ordered
     list so grouped sections and standalone items can interleave:
       - group  : a static, non-interactive section header (hidden text when
                  the sidebar is collapsed, replaced by a thin divider)
       - link   : a RouterLink destination (the default), with optional badge
       - action : a button that runs onClick (e.g. opens a modal) instead of
                  navigating
       - disabled: a placeholder entry for a feature that isn't built yet —
                  muted, non-interactive, with a "coming soon" tooltip -->
<script setup lang="ts">
import { computed, type Component } from 'vue'
import { useRoute } from 'vue-router'

export interface NavItem {
  key: string
  label: string
  // Absent for group headers; present for every interactive entry.
  icon?: Component
  // link entry: navigation target. Mutually exclusive with onClick.
  to?: string
  // action entry: runs on click instead of navigating.
  onClick?: () => void
  // Renders a static section header rather than an interactive row.
  group?: boolean
  // Renders a muted, non-interactive placeholder (feature not built yet).
  disabled?: boolean
  // badge lights a small indicator dot (e.g. "new version available") at the
  // item's top-right. Optional; absent means no badge.
  badge?: boolean
  // A short green "money-saving" chip pinned to the row's right edge (e.g.
  // "省钱" / "Save"), marking the entry as a cost-related feature. Optional.
  tag?: string
  // When true the entry is kept in the source list (route and code intact) but
  // never rendered in the sidebar — used to hide a menu item without deleting
  // its wiring.
  hidden?: boolean
}

const props = defineProps<{
  items: NavItem[]
  collapsed: boolean
  // Chip label + hover tooltip for disabled placeholder rows (e.g. "Soon" /
  // "Coming soon"). Passed in once rather than per item — every disabled row
  // shares them — and kept out of the item data so this component stays
  // i18n-free.
  soonLabel?: string
  soonTooltip?: string
}>()

const route = useRoute()

function isActive(to: string): boolean {
  return route.path === to || route.path.startsWith(to + '/')
}

const resolvedItems = computed(() =>
  props.items
    .filter((item) => !item.hidden)
    .map((item) => ({
      ...item,
      active: item.to ? isActive(item.to) : false,
    })),
)
</script>

<template>
  <nav class="sidebar-nav" :class="{ 'sidebar-nav--collapsed': collapsed }">
    <template v-for="item in resolvedItems" :key="item.key">
      <!-- Group header: uppercase label when expanded, a bare divider when
           collapsed (where the text wouldn't fit). -->
      <div v-if="item.group" class="sidebar-nav__group">
        <span v-if="!collapsed" class="sidebar-nav__group-label">{{ item.label }}</span>
        <span v-else class="sidebar-nav__group-divider" />
      </div>

      <!-- Disabled placeholder: an inert row for a not-yet-built feature. A
           "soon" chip marks it as intentional roadmap rather than a broken
           link. -->
      <div
        v-else-if="item.disabled"
        class="sidebar-nav-item sidebar-nav-item--disabled"
        :title="soonTooltip ?? item.label"
      >
        <span class="sidebar-nav-item__icon">
          <component :is="item.icon" :size="18" :stroke-width="1.8" />
        </span>
        <span v-if="!collapsed" class="sidebar-nav-item__label">{{ item.label }}</span>
        <span v-if="!collapsed && soonLabel" class="sidebar-nav-item__soon">{{ soonLabel }}</span>
      </div>

      <!-- Action row: opens a modal / runs a handler instead of navigating. -->
      <button
        v-else-if="item.onClick"
        type="button"
        class="sidebar-nav-item"
        :title="collapsed ? item.label : undefined"
        @click="item.onClick"
      >
        <span class="sidebar-nav-item__icon">
          <component :is="item.icon" :size="18" :stroke-width="1.8" />
        </span>
        <span v-if="!collapsed" class="sidebar-nav-item__label">{{ item.label }}</span>
      </button>

      <!-- Link row (default). -->
      <RouterLink
        v-else
        :to="item.to!"
        class="sidebar-nav-item"
        :class="{ 'sidebar-nav-item--active': item.active }"
        :title="collapsed ? item.label : undefined"
      >
        <span class="sidebar-nav-item__icon">
          <component :is="item.icon" :size="18" :stroke-width="1.8" />
        </span>
        <span v-if="!collapsed" class="sidebar-nav-item__label">{{ item.label }}</span>
        <span style="position: relative;">
          <span v-if="!collapsed && item.tag" class="sidebar-nav-item__save">{{ item.tag }}</span>
        </span>
        <span v-if="item.badge" class="sidebar-nav-item__dot" :title="item.label" />
      </RouterLink>
    </template>
  </nav>
</template>

<style scoped>
.sidebar-nav {
  display: flex;
  flex-direction: column;
  gap: 1px;
  padding: 0 12px;
}

.sidebar-nav--collapsed {
  padding: 0 6px;
}

/* Group header: a quiet section label. Kept tight against its items (small top
   margin, minimal bottom) so groups read as clusters instead of punching empty
   gaps into the list. The first group needs no extra top margin — the item
   above it already provides the separation. */
.sidebar-nav__group {
  margin: 14px 0 3px;
  padding: 0 10px;
}

.sidebar-nav__group-label {
  color: var(--color-text-muted);
  font-size: 10.5px;
  font-weight: 600;
  letter-spacing: 0.05em;
  text-transform: uppercase;
}

/* Collapsed sidebar can't show the label text, so a hairline stands in to keep
   the groups visually separated. */
.sidebar-nav__group-divider {
  display: block;
  height: 1px;
  margin: 7px 4px;
  background: var(--color-border);
}

/* Interactive rows share this base whether they are RouterLink, button, or the
   inert disabled variant. Reset button-native styles so the <button> action
   row matches the anchors pixel-for-pixel. */
.sidebar-nav-item {
  position: relative;
  display: flex;
  align-items: center;
  gap: 10px;
  width: 100%;
  height: 34px;
  padding: 0 10px;
  border: none;
  border-radius: var(--radius-md);
  background: transparent;
  color: var(--color-text-secondary);
  font: inherit;
  font-size: var(--text-sm);
  font-weight: 500;
  text-align: left;
  cursor: pointer;
  transition:
    color var(--duration-fast) var(--ease-out),
    background var(--duration-fast) var(--ease-out);
}

.sidebar-nav--collapsed .sidebar-nav-item {
  justify-content: center;
  padding: 0;
}

/* Hover: a neutral surface lift, kept visually distinct from the accent-tinted
   active state so "where I can go" never reads the same as "where I am". */
.sidebar-nav-item:hover {
  background: var(--color-surface-hover);
  color: var(--color-text);
}

/* Active: an on-brand indigo-tint pill (the same accent wash used elsewhere in
   the app) with an accent-colored icon and a stronger label. Reads clearly as
   "selected" on the near-white sidebar, where the old white-on-white pill did
   not. :hover repeated so hovering the current page doesn't drop the tint. */
.sidebar-nav-item--active,
.sidebar-nav-item--active:hover {
  background: var(--color-accent-subtle);
  color: var(--color-text);
  font-weight: 600;
}

/* Two-tone by default (icon a shade lighter than its label), both sharpening
   together on hover; the active row overrides the icon to the accent. */
.sidebar-nav-item__icon {
  display: flex;
  align-items: center;
  justify-content: center;
  width: 16px;
  height: 16px;
  flex-shrink: 0;
  color: var(--color-text-muted);
  transition: color var(--duration-fast) var(--ease-out);
}

.sidebar-nav-item:hover .sidebar-nav-item__icon {
  color: var(--color-text);
}

.sidebar-nav-item--active .sidebar-nav-item__icon {
  color: var(--color-accent);
}

/* Disabled placeholder: muted but not faded (a flat opacity knock-back reads as
   "failed to load"). The "soon" chip carries the intent, so the row stays inert
   with a plain cursor rather than a hostile not-allowed. */
.sidebar-nav-item--disabled {
  color: var(--color-text-muted);
  cursor: default;
}

/* Inert: a disabled row reacts to nothing. Cancel the base hover surface + text
   lift, and keep the icon muted too — otherwise the shared :hover icon rule
   above would brighten it, out of step with the label that stays muted. */
.sidebar-nav-item--disabled:hover {
  background: transparent;
  color: var(--color-text-muted);
}

.sidebar-nav-item--disabled:hover .sidebar-nav-item__icon {
  color: var(--color-text-muted);
}

.sidebar-nav-item__label {
  overflow: hidden;
  white-space: nowrap;
  text-overflow: ellipsis;
}

/* "Soon" chip on placeholder rows: pushed to the row's right edge, wearing the
   same accent wash as the active pill so it reads as a deliberate roadmap
   marker rather than a broken link. */
.sidebar-nav-item__soon {
  flex-shrink: 0;
  margin-left: auto;
  padding: 1px 6px;
  border-radius: var(--radius-full);
  background: var(--color-accent-subtle);
  color: var(--color-accent);
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.02em;
  line-height: 1.4;
}

/* "Save" chip: a small red superscript badge pinned to the top-right corner of
   the label text (not the row edge), so it reads as an annotation on the menu
   name itself. align-self:flex-start + the negative top margin lift it above
   the label baseline into a superscript position. */
.sidebar-nav-item__save {
  position: absolute;
  top: -16px;
  left: -6px;
  flex-shrink: 0;
  align-self: flex-start;
  margin-top: 2px;
  margin-left: 2px;
  padding: 0 5px;
  border-radius: var(--radius-full);
  background: var(--color-danger-subtle, rgba(208, 48, 80, 0.14));
  color: var(--color-danger, #d03050);
  font-size: 10px;
  font-weight: 600;
  letter-spacing: 0.02em;
  line-height: 1.5;
}

/* The update-available indicator dot. .sidebar-nav-item is position:relative,
   so this absolute dot anchors to the item's top-right corner — visible in
   both expanded and collapsed sidebar states. */
.sidebar-nav-item__dot {
  position: absolute;
  top: 7px;
  right: 10px;
  width: 8px;
  height: 8px;
  border-radius: var(--radius-full);
  background: var(--color-danger, #d03050);
  pointer-events: none;
}
</style>
