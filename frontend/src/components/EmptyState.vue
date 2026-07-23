<!-- Shared inline empty-state block. The mark is a tinted rounded icon tile
     matching the dashboard KPI icon treatment, so an empty list reads as a
     finished state rather than a blank placeholder box. Callers may pass a
     contextual icon; otherwise a neutral "inbox" glyph is used. -->
<script setup lang="ts">
import type { Component } from 'vue'
import { Inbox } from '@lucide/vue'

withDefaults(
  defineProps<{
    title?: string
    description?: string
    icon?: Component
  }>(),
  // A lucide icon is itself a function component; return it from a factory so
  // Vue does not mistake the default for a factory it should invoke as a render.
  { icon: () => Inbox },
)
</script>

<template>
  <div class="empty-state">
    <div class="empty-state__mark icon-tile">
      <component :is="icon" :size="22" :stroke-width="1.75" />
    </div>
    <h3 v-if="title">{{ title }}</h3>
    <p v-if="description">{{ description }}</p>
    <div v-if="$slots.action" class="empty-state__action">
      <slot name="action" />
    </div>
  </div>
</template>
