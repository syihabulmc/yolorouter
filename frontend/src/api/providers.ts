import { apiFetch } from './client'

export interface ProviderKey {
  id: number
  label: string
  key_prefix: string
  sort_order: number
  test_model: string
  management_status: number
  verification_status: number
  needs_reentry: boolean
  last_test_result: number | null
  last_test_model: string
  last_test_duration_ms: number | null
  last_tested_at: string | null
}

export interface Provider {
  id: number
  name: string
  provider_type: string
  base_url: string
  note: string
  management_status: number
  running_status: 'not_configured' | 'pending_test' | 'available' | 'partial' | 'unavailable'
  keys: ProviderKey[]
  created_at: string
  protocol_endpoints: string
}

export interface BatchTestResult {
  key_id: number
  label: string
  needs_reentry: boolean
  skipped: boolean
  outcome: number | null
  duration_ms: number
}

export interface CreateProviderInput {
  name: string
  base_url: string
  note?: string
  key_label: string
  key_plaintext: string
  test_model: string
  management_status?: number
  provider_type?: string
  protocol_endpoints?: string
}

export interface UpdateProviderInput {
  name: string
  base_url: string
  note?: string
  provider_type?: string
  protocol_endpoints?: string
}

export interface CreateKeyInput {
  label: string
  plaintext: string
  test_model: string
  management_status?: number
}

export interface UpdateKeyInput {
  label: string
  plaintext?: string
  test_model: string
  management_status?: number
}

export interface TestKeyResult {
  outcome: number
  duration_ms: number
  // detail is a concise, admin-facing diagnostic string for a failed test
  // (HTTP status + the upstream's own error message when present). Empty on
  // success. Shown expandable in the setup UI to help tell a bad key from a
  // wrong model from a blocked address.
  detail: string
}

export interface ListModelsResult {
  models: string[]
  outcome: number
}

export function listProviders(): Promise<{ list: Provider[] }> {
  return apiFetch('/api/admin/providers')
}

export function getProvider(id: number): Promise<Provider> {
  return apiFetch(`/api/admin/providers/${id}`)
}

export function createProvider(input: CreateProviderInput): Promise<Provider> {
  return apiFetch('/api/admin/providers', { method: 'POST', body: JSON.stringify(input) })
}

export function updateProvider(id: number, input: UpdateProviderInput): Promise<Provider> {
  return apiFetch(`/api/admin/providers/${id}`, { method: 'PATCH', body: JSON.stringify(input) })
}

export function setProviderStatus(id: number, enabled: boolean): Promise<void> {
  return apiFetch(`/api/admin/providers/${id}/status`, { method: 'PATCH', body: JSON.stringify({ enabled }) })
}

// testKeyPreview checks a key against a single protocol — the selected
// primary (providerType) — before the provider exists. It does NOT cover
// any additional protocol_endpoints the create form may also configure;
// the authoritative multi-endpoint verification happens on actual create
// via each destination's own server-side test (verifyKeyAllDestinations).
export function testKeyPreview(baseUrl: string, apiKey: string, model: string, providerType: string): Promise<TestKeyResult> {
  return apiFetch('/api/admin/providers/test-key', {
    method: 'POST',
    body: JSON.stringify({ base_url: baseUrl, api_key: apiKey, model, provider_type: providerType }),
  })
}

// listModelsPreview fetches the upstream model catalogue for a candidate
// credential so the setup UI can offer a picker instead of a free-text model
// field. Stateless preview — nothing is persisted. A non-success outcome
// (with an optional detail) means the catalogue could not be retrieved (bad
// key, unreachable, or the upstream simply doesn't expose /v1/models), in
// which case the caller falls back to manual model entry.
export function listModelsPreview(baseUrl: string, apiKey: string, providerType: string): Promise<ListModelsResult> {
  return apiFetch('/api/admin/providers/list-models', {
    method: 'POST',
    body: JSON.stringify({ base_url: baseUrl, api_key: apiKey, provider_type: providerType }),
  })
}

// listModelsForProvider fetches the upstream model catalogue for an already-
// stored provider, using one of its server-side keys — the by-id counterpart
// to listModelsPreview (which needs a plaintext key the candidate UI doesn't
// hold). A non-success outcome (including "no usable key") returns an empty
// list; the caller falls back to manual model entry.
export function listModelsForProvider(id: number): Promise<ListModelsResult> {
  return apiFetch(`/api/admin/providers/${id}/models`)
}

export function createProviderKey(providerId: number, input: CreateKeyInput): Promise<ProviderKey> {
  return apiFetch(`/api/admin/providers/${providerId}/keys`, { method: 'POST', body: JSON.stringify(input) })
}

export function updateProviderKey(providerId: number, keyId: number, input: UpdateKeyInput): Promise<ProviderKey> {
  return apiFetch(`/api/admin/providers/${providerId}/keys/${keyId}`, { method: 'PATCH', body: JSON.stringify(input) })
}

export function reorderProviderKey(providerId: number, keyId: number, direction: 'up' | 'down'): Promise<void> {
  return apiFetch(`/api/admin/providers/${providerId}/keys/${keyId}/order`, {
    method: 'PATCH',
    body: JSON.stringify({ direction }),
  })
}

export function setProviderKeyStatus(providerId: number, keyId: number, enabled: boolean): Promise<void> {
  return apiFetch(`/api/admin/providers/${providerId}/keys/${keyId}/status`, {
    method: 'PATCH',
    body: JSON.stringify({ enabled }),
  })
}

export function testProviderKey(providerId: number, keyId: number): Promise<ProviderKey> {
  return apiFetch(`/api/admin/providers/${providerId}/keys/${keyId}/test`, { method: 'POST' })
}

// Batch test can legitimately exceed apiFetch's default
// 30s timeout — passes timeoutMs (apiFetch itself honors this
// override; see client.ts). An earlier version tried to work around this
// with an extra AbortController instead — that only added a SECOND, later abort signal
// on top of apiFetch's own hardcoded 30s internal timer, which still fired
// first regardless, so slow multi-key batches kept failing at 30s anyway.
export function testAllProviderKeys(
  providerId: number,
  enabledKeyCount: number,
): Promise<{ results: BatchTestResult[] }> {
  const timeoutMs = 60_000 + enabledKeyCount * 16_000
  return apiFetch<{ results: BatchTestResult[] }>(`/api/admin/providers/${providerId}/keys/test-all`, {
    method: 'POST',
    timeoutMs,
  })
}
