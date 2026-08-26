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

// Bulk-add parser for the "Add bulk key" flow. Each non-blank line is either
// `label:key` (label may be empty so the server fills a random one) or just
// `key` (no colon, no label). The same shape the backend accepts, surfaced
// client-side so the operator sees line-numbered errors before submit instead
// of after a 400 round trip.
export interface ParsedBulkKey {
  label: string
  plaintext: string
}

export interface ParsedBulkError {
  line: number
  reason: 'empty_key' | 'key_too_short' | 'label_too_long'
}

export interface ParsedBulkDuplicate {
  line: number
  plaintext: string
}

export interface ParsedBulkResult {
  valid: ParsedBulkKey[]
  duplicates: ParsedBulkDuplicate[]
  errors: ParsedBulkError[]
}

const BULK_KEY_MIN_LENGTH = 8
const BULK_LABEL_MAX_LENGTH = 30

export function parseBulkKeyLines(input: string): ParsedBulkResult {
  const result: ParsedBulkResult = { valid: [], duplicates: [], errors: [] }
  const seen = new Set<string>()
  const lines = input.split('\n')
  for (let i = 0; i < lines.length; i++) {
    const lineNumber = i + 1
    const trimmed = lines[i].trim()
    if (trimmed === '') continue
    const colonIdx = trimmed.indexOf(':')
    let label: string
    let plaintext: string
    if (colonIdx === -1) {
      label = ''
      plaintext = trimmed
    } else {
      label = trimmed.slice(0, colonIdx).trim()
      plaintext = trimmed.slice(colonIdx + 1)
    }
    if (plaintext === '') {
      result.errors.push({ line: lineNumber, reason: 'empty_key' })
      continue
    }
    if (plaintext.length < BULK_KEY_MIN_LENGTH) {
      result.errors.push({ line: lineNumber, reason: 'key_too_short' })
      continue
    }
    if (label.length > BULK_LABEL_MAX_LENGTH) {
      result.errors.push({ line: lineNumber, reason: 'label_too_long' })
      continue
    }
    if (seen.has(plaintext)) {
      result.duplicates.push({ line: lineNumber, plaintext })
      continue
    }
    seen.add(plaintext)
    result.valid.push({ label, plaintext })
  }
  return result
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
