// frontend/src/utils/modelCostLocation.ts
//
// Builds the route location for a single-model cost detail page. Centralized
// so every entry point (breakdown row, chart click, management-page button)
// handles the dot-segment edge identically. A model name that is "." or ".."
// (the two names that normalize to URL dot-segments per RFC 3986) is routed
// via the bare query form so refresh/deep-link does not collapse to a parent
// path.

import type { RouteLocationRaw } from 'vue-router'

// "." and ".." are the two path segments RFC 3986 treats as dot-segments —
// browsers normalize them away during URL resolution, which would collapse
// the model path to its parent.
function isDotSegmentName(name: string): boolean {
  return name === '.' || name === '..'
}

export function modelCostDetailLocation(name: string): RouteLocationRaw {
  if (isDotSegmentName(name)) {
    return { path: '/costs/models', query: { name } }
  }
  return { path: `/costs/models/${encodeURIComponent(name)}` }
}
