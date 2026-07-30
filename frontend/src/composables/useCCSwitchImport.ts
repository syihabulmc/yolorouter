// useCCSwitchImport centralizes the CC-Switch deep-link import used by both the
// models list and the API-keys list. Deep links can't report back whether the
// desktop app actually handled them, so we heuristically detect a successful
// hand-off: if the tab loses focus / is hidden shortly after we navigate to the
// ccswitch:// URL, the OS most likely opened the app; if nothing happens within
// a few seconds while the tab is still visible, the protocol handler is
// probably not registered and we surface an install hint.
import { onUnmounted } from 'vue'
import { useI18n } from 'vue-i18n'
import { useMessage } from 'naive-ui'

// The full API key secret is never returned by the list endpoints (only a
// truncated prefix), so imports carry a placeholder key that the user fills in
// inside CC-Switch after the provider is created.
const PLACEHOLDER_API_KEY = 'sk-'

// Milliseconds to wait for a focus/visibility change before deciding the deep
// link was not handled by any installed app.
const OPEN_DETECT_MS = 5000

export interface CCSwitchImportParams {
  // Provider display name shown in CC-Switch.
  name: string
  // Optional model name to preselect; omitted for provider-only imports.
  model?: string
}

export function useCCSwitchImport() {
  const { t } = useI18n()
  const message = useMessage()

  let openTimer: ReturnType<typeof setTimeout> | null = null
  let openCleanup: (() => void) | null = null

  function buildUrl(p: CCSwitchImportParams): string {
    const params = new URLSearchParams({
      resource: 'provider',
      app: 'claude',
      name: p.name,
      endpoint: location.origin,
      apiKey: PLACEHOLDER_API_KEY,
      homepage: location.origin,
    })
    if (p.model) params.set('model', p.model)
    return `ccswitch://v1/import?${params.toString()}`
  }

  function importToCCS(p: CCSwitchImportParams) {
    let maybeOpened = false

    const cleanup = () => {
      window.removeEventListener('blur', markOpened)
      window.removeEventListener('pagehide', markOpened)
      document.removeEventListener('visibilitychange', handleVisibilityChange)
    }
    const markOpened = () => {
      maybeOpened = true
      cleanup()
    }
    const handleVisibilityChange = () => {
      if (document.hidden) markOpened()
    }

    // Cancel any in-flight detection from a previous click so overlapping
    // imports don't race their timers/listeners.
    if (openTimer) {
      clearTimeout(openTimer)
      openTimer = null
    }
    if (openCleanup) {
      openCleanup()
      openCleanup = null
    }

    window.addEventListener('blur', markOpened, { once: true })
    window.addEventListener('pagehide', markOpened, { once: true })
    document.addEventListener('visibilitychange', handleVisibilityChange)
    openCleanup = cleanup

    message.info(t('ccswitch.opening'))
    window.location.href = buildUrl(p)

    openTimer = setTimeout(() => {
      cleanup()
      openTimer = null
      openCleanup = null
      if (!maybeOpened && document.visibilityState === 'visible') {
        message.error(t('ccswitch.openFailed'))
      }
    }, OPEN_DETECT_MS)
  }

  // A detection started right before the component unmounts (e.g. the user
  // clicks Import then navigates away) would otherwise leave a dangling timer
  // and listeners that fire a misleading toast on an unrelated page.
  onUnmounted(() => {
    if (openTimer) {
      clearTimeout(openTimer)
      openTimer = null
    }
    if (openCleanup) {
      openCleanup()
      openCleanup = null
    }
  })

  return { importToCCS }
}
