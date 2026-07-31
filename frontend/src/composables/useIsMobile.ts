// frontend/src/composables/useIsMobile.ts
import { onUnmounted, ref, type Ref } from 'vue'

// Single source of truth for the mobile breakpoint. Keep this in sync with the
// `@media (max-width: 768px)` queries in the .less files — one number governs
// both the JS matchMedia checks and the CSS layout switches.
export const MOBILE_BREAKPOINT = 768
const MOBILE_MEDIA_QUERY = `(max-width: ${MOBILE_BREAKPOINT}px)`

/**
 * Reactive `isMobile` flag driven by `window.matchMedia`. Encapsulates the
 * listener wiring (add on setup, remove on unmount) so call sites just read the
 * returned ref. Pass an `onLeave` callback to run cleanup (e.g. close a drawer)
 * when the viewport grows back past the breakpoint.
 */
export function useIsMobile(onLeave?: () => void): Ref<boolean> {
  const query = window.matchMedia(MOBILE_MEDIA_QUERY)
  const isMobile = ref(query.matches)

  function onChange(e: MediaQueryListEvent) {
    isMobile.value = e.matches
    if (!e.matches) onLeave?.()
  }

  query.addEventListener('change', onChange)
  onUnmounted(() => query.removeEventListener('change', onChange))

  return isMobile
}
