import { testOutcomeI18nKey } from './testOutcomeDisplay'

export type RunningStatusTagType = 'default' | 'success' | 'warning' | 'error'

// A capability flag records whether the last probe CONFIRMED the capability. It
// is informational: routing ignores it entirely, so an unconfirmed capability is
// not a reason to avoid the candidate — the remedy, if the operator cares, is to
// retest. 'unsupported' exists only because the column can still hold a false
// written by an older build; nothing writes one now.
export type CapabilityState = 'confirmed' | 'unsupported' | 'unconfirmed'

export function capabilityState(flag: boolean | null | undefined): CapabilityState {
  if (flag === true) return 'confirmed'
  if (flag === false) return 'unsupported'
  return 'unconfirmed'
}

// Localized result text for a candidate test: "passed", or "failed: <reason>"
// when the outcome is known, else a plain "failed". Reused by the row-test
// toast and the modal's result alert so both name a failure identically.
export function candidateTestResultText(
  t: (key: string) => string,
  passed: boolean,
  outcome: number | null | undefined,
): string {
  if (passed) return t('models.testPassed')
  if (outcome !== null && outcome !== undefined) {
    return `${t('models.testFailed')}: ${t(`providers.${testOutcomeI18nKey(outcome)}`)}`
  }
  return t('models.testFailed')
}

// Shared by ModelListPage.vue and ModelDetailPage.vue so the
// running_status → i18n key (and, where needed, NTag color) mapping is
// defined once.
export const MODEL_RUNNING_STATUS_DISPLAY: Record<string, { i18nKey: string; tagType: RunningStatusTagType }> = {
  not_configured: { i18nKey: 'NotConfigured', tagType: 'default' },
  pending_test: { i18nKey: 'Pending', tagType: 'default' },
  available: { i18nKey: 'Available', tagType: 'success' },
  degraded: { i18nKey: 'Degraded', tagType: 'warning' },
  unavailable: { i18nKey: 'Unavailable', tagType: 'error' },
}

export function modelRunningStatusDisplay(status: string) {
  return MODEL_RUNNING_STATUS_DISPLAY[status] ?? MODEL_RUNNING_STATUS_DISPLAY.unavailable
}
