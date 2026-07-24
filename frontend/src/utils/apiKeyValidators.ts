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
