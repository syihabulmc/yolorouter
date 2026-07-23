import type { FormItemRule } from 'naive-ui'
import { isValidEndpointUrl } from './providerProtocol'

// Mirrors the backend's own binding tags (createProviderRequest / createKeyRequest
// / updateKeyRequest in internal/handler/provider_handler.go) so NewProviderModal.vue
// and KeyEditModal.vue can't drift apart from each other or from the backend —
// the same reason authValidators.ts exists for the auth forms (these rules were
// previously duplicated inline in both forms).

export function providerNameRule(t: (key: string) => string): FormItemRule[] {
  return [
    { required: true, message: t('providers.fieldRequired'), trigger: ['blur', 'input'] },
    { min: 2, max: 50, message: t('providers.nameLengthHint'), trigger: ['blur', 'input'] },
  ]
}

export function baseUrlRule(t: (key: string) => string): FormItemRule[] {
  return [
    { required: true, message: t('providers.fieldRequired'), trigger: ['blur', 'input'] },
    { type: 'url', max: 255, message: t('providers.baseUrlInvalid'), trigger: ['blur', 'input'] },
  ]
}

export function noteRule(t: (key: string) => string): FormItemRule[] {
  return [{ max: 200, message: t('providers.noteTooLong'), trigger: ['blur', 'input'] }]
}

export function keyLabelRule(t: (key: string) => string): FormItemRule[] {
  return [
    { required: true, message: t('providers.fieldRequired'), trigger: ['blur', 'input'] },
    { min: 2, max: 30, message: t('providers.labelLengthHint'), trigger: ['blur', 'input'] },
  ]
}

// required is parameterized the same way confirmPasswordRule's getOriginal is:
// a brand-new key's plaintext is mandatory, but an existing key's edit form
// leaves it blank to mean "keep the current key".
export function keyPlaintextRule(t: (key: string) => string, required: boolean): FormItemRule[] {
  return [
    { required, message: t('providers.fieldRequired'), trigger: ['blur', 'input'] },
    { min: 8, message: t('providers.keyPlaintextTooShort'), trigger: ['blur', 'input'] },
  ]
}

export function testModelRule(t: (key: string) => string): FormItemRule[] {
  return [
    { required: true, message: t('providers.fieldRequired'), trigger: ['blur', 'input'] },
    { max: 100, message: t('providers.testModelTooLong'), trigger: ['blur', 'input'] },
  ]
}

// Mirrors the backend's ValidateProtocolEndpoints (internal/service/provider_protocol.go):
// an empty string is valid (means "reuse the provider's base_url"), otherwise the
// value must parse as an absolute http(s) URL with a non-empty host.
export function protocolEndpointUrlRule(t: (key: string) => string): FormItemRule {
  return {
    trigger: ['blur', 'input'],
    validator: (_rule, value: string) => {
      return isValidEndpointUrl(value) ? true : new Error(t('providers.protocolEndpointUrlInvalid'))
    },
  }
}
