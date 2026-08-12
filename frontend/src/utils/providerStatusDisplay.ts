import type { ProviderKey } from '../api/providers'

// Shared by ProviderListPage.vue and CandidateEditModal.vue so the provider
// running_status → i18n key (and NTag color) mapping is defined once — the
// provider-side counterpart of modelStatusDisplay.ts.
export const PROVIDER_RUNNING_STATUS_DISPLAY: Record<
  string,
  { i18nKey: string; type: 'default' | 'success' | 'warning' | 'error' }
> = {
  not_configured: { i18nKey: 'NotConfigured', type: 'default' },
  pending_test: { i18nKey: 'Pending', type: 'default' },
  available: { i18nKey: 'Available', type: 'success' },
  partial: { i18nKey: 'Partial', type: 'warning' },
  unavailable: { i18nKey: 'Unavailable', type: 'error' },
}

export function providerRunningStatusDisplay(status: string) {
  return PROVIDER_RUNNING_STATUS_DISPLAY[status] ?? PROVIDER_RUNNING_STATUS_DISPLAY.unavailable
}

// isKeyUsable mirrors the backend's routable-key rule (model_service.go's
// providerHasAvailableKey): enabled + verification passed + authorized for the
// provider's current destination version — needs_reentry is the wire-level
// negation of that version match, so the three fields below reproduce the rule
// exactly.
export function isKeyUsable(k: ProviderKey): boolean {
  return k.management_status === 1 && k.verification_status === 1 && !k.needs_reentry
}

export function usableKeyCount(keys: ProviderKey[]): number {
  return keys.filter(isKeyUsable).length
}
