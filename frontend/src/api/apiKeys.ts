import type { SelectOption } from 'naive-ui'
import { apiFetch } from './client'

export interface APIKey {
  id: number
  key_prefix: string
  // Owning account (user id + username), plus the legacy free-text label.
  user_id: number
  owner_username: string
  owner_label: string
  remark: string
  status: number
  display_status: string
  expires_at: string | null
  rpm_limit: number | null
  tpm_limit: number | null
  concurrency_limit: number | null
  budget_limit_micros: number | null
  budget_spent_micros: number
  allow_all_models: boolean
  model_ids: number[]
  // Per-key custom-system-prompt override. When the override flag is false,
  // the enabled/text pair is ignored and the key inherits the global
  // custom-system-prompt setting; when true, the key uses its own values.
  custom_system_prompt_enabled_override: boolean
  custom_system_prompt_enabled: boolean
  custom_system_prompt: string
  // Per-key input-compression override. Same shape as CSP: override=false
  // means inherit the global setting; override=true means the key uses its
  // own compress_enabled flag.
  compress_enabled_override: boolean
  compress_enabled: boolean
  created_at: string
  updated_at: string
}

export interface APIKeyPage {
  total: number
  page: number
  page_size: number
  list: APIKey[]
}

// toAPIKeyOptions maps API keys to naive-ui <select> options: owner_label
// disambiguated by key_prefix (keys can share an owner label or have none).
// Kept here — next to the APIKey type — so every api-key <select> (analytics
// filter bar, request-log caller filter) maps the same way and can't drift on
// label formatting.
export function toAPIKeyOptions(keys: APIKey[]): SelectOption[] {
  return keys.map((k) => ({
    label: k.owner_label ? `${k.owner_label} (${k.key_prefix}…)` : `${k.key_prefix}…`,
    value: k.id,
  }))
}

export interface CreateAPIKeyInput {
  owner_label?: string
  remark?: string
  allow_all_models: boolean
  model_ids: number[]
  expires_at?: string
  rpm_limit?: number
  tpm_limit?: number
  concurrency_limit?: number
  budget_limit_micros?: number
  // CSP fields default to inherit (override=false) when omitted — the create
  // form no longer collects them; they're configured post-creation via the
  // dedicated optimization modal.
  custom_system_prompt_enabled_override?: boolean
  custom_system_prompt_enabled?: boolean
  custom_system_prompt?: string
}

export interface CreateAPIKeyResult {
  plaintext_key: string
  api_key: APIKey
}

// UpdateAPIKeyInput is a sparse PATCH. Numeric limits: undefined = leave
// unchanged; 0 = clear sentinel; positive = set. model_ids: undefined =
// unchanged; an array (including empty) replaces the whitelist. owner_label /
// remark: undefined = unchanged. expected_updated_at: when set, the backend
// qualifies the UPDATE with `AND updated_at = ?` and returns 11013 (409) if
// another writer committed first — the optimistic-lock CAS token captured by
// the optimization modal's authoritative GET on open. Omitted by legacy callers
// (EditKeyModal/CreateKeyModal) to keep their non-CAS behavior.
export interface UpdateAPIKeyInput {
  owner_label?: string
  remark?: string
  allow_all_models?: boolean
  model_ids?: number[]
  expires_at?: string
  rpm_limit?: number
  tpm_limit?: number
  concurrency_limit?: number
  budget_limit_micros?: number
  custom_system_prompt_enabled_override?: boolean
  custom_system_prompt_enabled?: boolean
  custom_system_prompt?: string
  // Per-key input-compression override PATCH fields. Same CAS / override
  // semantics as CSP — see APIKey above.
  compress_enabled_override?: boolean
  compress_enabled?: boolean
  expected_updated_at?: string
}

export interface APIKeyListParams {
  q: string
  owner: string
  status: string
  // Narrow to keys owned by one account; omit/null = all.
  userId?: number | null
  page: number
  pageSize: number
}

export function listAPIKeys(p: APIKeyListParams): Promise<APIKeyPage> {
  const params = new URLSearchParams({
    q: p.q,
    owner: p.owner,
    status: p.status,
    page: String(p.page),
    page_size: String(p.pageSize),
  })
  if (p.userId != null) params.set('user_id', String(p.userId))
  return apiFetch(`/api/admin/api-keys?${params.toString()}`)
}

export function createAPIKey(input: CreateAPIKeyInput): Promise<CreateAPIKeyResult> {
  return apiFetch('/api/admin/api-keys', { method: 'POST', body: JSON.stringify(input) })
}

export function getAPIKey(id: number): Promise<APIKey> {
  return apiFetch(`/api/admin/api-keys/${id}`)
}

// Reveal the full plaintext key for the list-page copy button. Returns the
// same plaintext_key field name as createAPIKey so the caller can reuse one
// code path. The backend returns 11016 when the key predates the
// encrypted_key column (its plaintext was never stored); that surfaces as an
// APIError the UI displays via displayMessage.
export function getAPIKeyPlaintext(id: number): Promise<{ plaintext_key: string }> {
  return apiFetch(`/api/admin/api-keys/${id}/plaintext`)
}

export function updateAPIKey(id: number, input: UpdateAPIKeyInput): Promise<APIKey> {
  return apiFetch(`/api/admin/api-keys/${id}`, { method: 'PATCH', body: JSON.stringify(input) })
}

export function revokeAPIKey(id: number): Promise<void> {
  return apiFetch(`/api/admin/api-keys/${id}/revoke`, { method: 'PATCH', body: JSON.stringify({}) })
}
