// frontend/src/composables/useIsMobile.ts
import { onUnmounted, ref, type Ref } from 'vue'

// Single source of truth for the mobile breakpoint. This JS constant must stay
// in sync with the LESS variable `@mobile-breakpoint`, injected via
// `additionalData` in vite.config.ts (css.preprocessorOptions.less) — CSS media
// queries cannot read a JS or CSS variable, so the number is duplicated in
// those two places by design. Use `@mobile-breakpoint` in .less /
// <style lang="less"> blocks rather than hard-coding 768px.
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
