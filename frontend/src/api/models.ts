import { apiFetch } from './client'

export interface ModelCandidate {
  id: number
  provider_id: number
  provider_name: string
  provider_model_name: string
  input_price: number
  output_price: number
  cache_write_price: number | null
  cache_read_price: number | null
  max_output: number
  // Whether the last probe confirmed the capability: true when it did, null when
  // it did not. Informational only — routing ignores these. A false can still
  // arrive from a row written by an older build.
  supports_streaming: boolean | null
  supports_function_calling: boolean | null
  management_status: number
  sort_order: number
  verification_status: number
  routable: boolean
  last_test_result: number | null
  last_test_duration_ms: number | null
  last_tested_at: string | null
}

export interface Model {
  id: number
  name: string
  management_status: number
  running_status: string
  candidates: ModelCandidate[]
  created_at: string
}

export interface CreateCandidateInput {
  provider_id: number
  provider_model_name: string
  input_price: number
  output_price: number
  cache_write_price?: number
  cache_read_price?: number
  max_output: number
  management_status?: number
}

export interface UpdateCandidateInput {
  provider_model_name: string
  input_price: number
  output_price: number
  cache_write_price?: number
  cache_read_price?: number
  max_output: number
  management_status?: number
}

// ProbeReport is one probe's result. `ran: false` means the probe was skipped
// (the basic probe failed first), which must read as "not tested" rather than as
// a verdict. `supported` is tri-state for the capability probes.
export interface ProbeReport {
  ran: boolean
  supported: boolean | null
  outcome: number | null
  duration_ms: number
}

export interface CandidateTestReport {
  basic: ProbeReport
  streaming: ProbeReport
  function_calling: ProbeReport
}

// `created: false` means enablement was requested but the basic probe failed, so
// nothing was stored and the operator can fix the config and retry.
export interface TestAndCreateResult {
  report: CandidateTestReport
  created: boolean
  candidate: ModelCandidate | null
}

export interface UpdateCandidateResult {
  candidate: ModelCandidate
  report: CandidateTestReport | null
}

export function listModels(): Promise<{ list: Model[] }> {
  return apiFetch('/api/admin/models')
}

export function createModel(name: string): Promise<Model> {
  return apiFetch('/api/admin/models', { method: 'POST', body: JSON.stringify({ name }) })
}

export interface BatchSkippedModel {
  name: string
  reason: 'exists' | 'invalid'
}

export interface BatchCreateModelsResult {
  created: Model[]
  skipped: BatchSkippedModel[]
}

export function createModelsBatch(names: string[]): Promise<BatchCreateModelsResult> {
  return apiFetch('/api/admin/models/batch', { method: 'POST', body: JSON.stringify({ names }) })
}

export function getModel(id: number): Promise<Model> {
  return apiFetch(`/api/admin/models/${id}`)
}

export function updateModel(id: number, name: string): Promise<Model> {
  return apiFetch(`/api/admin/models/${id}`, { method: 'PATCH', body: JSON.stringify({ name }) })
}

export function setModelStatus(id: number, enabled: boolean): Promise<void> {
  return apiFetch(`/api/admin/models/${id}/status`, { method: 'PATCH', body: JSON.stringify({ enabled }) })
}


export function createCandidate(modelId: number, input: CreateCandidateInput): Promise<ModelCandidate> {
  return apiFetch(`/api/admin/models/${modelId}/candidates`, { method: 'POST', body: JSON.stringify(input) })
}

// PROBE_TIMEOUT_MS covers a save or retest that probes upstream. The server
// budget is two sequential rounds — the basic probe, then the two capability
// probes concurrently — each capped at its own 15s upstream timeout, so a slow
// but perfectly valid run lands right on apiFetch's 30s default. Aborting there
// would be the worst possible outcome: the candidate is already stored by then,
// so the operator sees a timeout and their retry fails on the duplicate-provider
// constraint. The margin is for that, not for expected latency.
const PROBE_TIMEOUT_MS = 90_000

// testAndCreateCandidate probes the mapping server-side and stores it only if
// the result allows what was asked for. Slower than createCandidate (up to two
// upstream round trips) but it is what removes the manual test step.
export function testAndCreateCandidate(modelId: number, input: CreateCandidateInput): Promise<TestAndCreateResult> {
  return apiFetch(`/api/admin/models/${modelId}/candidates/test-and-create`, {
    method: 'POST',
    body: JSON.stringify(input),
    timeoutMs: PROBE_TIMEOUT_MS,
  })
}

// May probe: renaming the target, or enabling a candidate that is not verified,
// re-verifies it server-side.
export function updateCandidate(modelId: number, candidateId: number, input: UpdateCandidateInput): Promise<UpdateCandidateResult> {
  return apiFetch(`/api/admin/models/${modelId}/candidates/${candidateId}`, {
    method: 'PATCH',
    body: JSON.stringify(input),
    timeoutMs: PROBE_TIMEOUT_MS,
  })
}

export function reorderCandidate(modelId: number, candidateId: number, direction: 'up' | 'down'): Promise<void> {
  return apiFetch(`/api/admin/models/${modelId}/candidates/${candidateId}/order`, {
    method: 'PATCH',
    body: JSON.stringify({ direction }),
  })
}

export function setCandidateStatus(modelId: number, candidateId: number, enabled: boolean): Promise<void> {
  return apiFetch(`/api/admin/models/${modelId}/candidates/${candidateId}/status`, {
    method: 'PATCH',
    body: JSON.stringify({ enabled }),
  })
}

// retestCandidate re-probes a stored candidate. One retest covers the basic
// mapping and both capabilities, so there is no test type to choose.
export function retestCandidate(modelId: number, candidateId: number): Promise<ModelCandidate> {
  return apiFetch(`/api/admin/models/${modelId}/candidates/${candidateId}/test`, {
    method: 'POST',
    timeoutMs: PROBE_TIMEOUT_MS,
  })
}

export function deleteCandidate(modelId: number, candidateId: number): Promise<void> {
  return apiFetch(`/api/admin/models/${modelId}/candidates/${candidateId}`, { method: 'DELETE' })
}
