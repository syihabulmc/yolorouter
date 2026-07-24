import type { FormItemRule } from 'naive-ui'

// Mirrors the backend binding (createAPIKeyRequest.model_ids in
// internal/handler/api_key_handler.go) so CreateKeyModal.vue and EditKeyModal.vue
// can't drift apart from each other or from the backend — the same reason
// providerValidators.ts / authValidators.ts exist (this rule was previously
// duplicated inline in both forms).

// required is parameterized the same way providerValidators' keyPlaintextRule is:
// a custom allowlist needs at least one model, but an all-models key needs none.
export function modelIdsRule(t: (key: string) => string, required: boolean): FormItemRule[] {
  return [
    { required, type: 'array', trigger: ['change', 'blur'], message: t('apiKeys.modelAllowlistRequired') },
  ]
}

// Custom system prompt rule: when a key overrides the global setting AND
// enables the override, the prompt text must be non-empty (the backend
// rejects enabled+empty as errcode 11011). The 2000-rune cap mirrors the
// service layer's MaxCustomSystemPromptLen so the client rejects oversized
// input before the round-trip.
export function customSystemPromptRule(
  t: (key: string) => string,
  override: boolean,
  enabled: boolean,
): FormItemRule[] {
  const required = override && enabled
  return [
    { required, trigger: ['blur', 'input'], message: t('apiKeys.cspRequiredWhenEnabled') },
    { max: 2000, type: 'string', trigger: ['blur', 'input'], message: t('apiKeys.cspTooLong') },
  ]
}

