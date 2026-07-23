// Pure, side-effect-free helpers for the provider protocol_endpoints feature.
// Mirrors the backend's own logic (internal/service/provider_protocol.go's
// ValidateProviderType / ValidateProtocolEndpoints) so the form model and the
// wire format ({provider_type, protocol_endpoints}) never drift apart:
// - provider_type is the primary wire protocol a provider speaks (default "openai").
// - protocol_endpoints is a JSON object string of ADDITIONAL protocols the
//   provider also accepts, keyed by protocol id, valued by that protocol's
//   base URL — an empty-string value means "reuse the provider's base_url".
//   An empty overall string means "no additional protocols". The primary
//   protocol is never listed in protocol_endpoints.

export const ALL_PROTOCOLS = ['openai', 'anthropic', 'gemini', 'responses'] as const

export type ProtocolId = (typeof ALL_PROTOCOLS)[number]

export interface ProtocolEndpointEntry {
  enabled: boolean
  url: string
}

export interface ProtocolConfigModel {
  providerType: ProtocolId
  // Entries exist for all 4 protocols, including the primary one — the
  // primary's own entry is simply unused/ignored by the UI and by
  // serializeProtocolConfig (it is always excluded from the output).
  endpoints: Record<ProtocolId, ProtocolEndpointEntry>
}

function isProtocolId(value: string): value is ProtocolId {
  return (ALL_PROTOCOLS as readonly string[]).includes(value)
}

function normalizeProviderType(providerType: string): ProtocolId {
  return isProtocolId(providerType) ? providerType : 'openai'
}

function emptyEndpoints(): Record<ProtocolId, ProtocolEndpointEntry> {
  return Object.fromEntries(ALL_PROTOCOLS.map((p) => [p, { enabled: false, url: '' }])) as Record<
    ProtocolId,
    ProtocolEndpointEntry
  >
}

export function emptyProtocolConfig(providerType: ProtocolId = 'openai'): ProtocolConfigModel {
  return { providerType, endpoints: emptyEndpoints() }
}

// Tolerant parse: an empty or malformed protocolEndpointsJson yields "no
// additional endpoints" rather than throwing — this mirrors the backend's
// own lenient read-path parsing (SupportedProtocolSet/VerificationTargets),
// since validation already happened once at write time.
export function parseProtocolConfig(providerType: string, protocolEndpointsJson: string): ProtocolConfigModel {
  const model = emptyProtocolConfig(normalizeProviderType(providerType))

  if (!protocolEndpointsJson) return model

  let parsed: unknown
  try {
    parsed = JSON.parse(protocolEndpointsJson)
  } catch {
    return model
  }
  if (typeof parsed !== 'object' || parsed === null || Array.isArray(parsed)) return model

  for (const [key, value] of Object.entries(parsed as Record<string, unknown>)) {
    if (!isProtocolId(key) || key === model.providerType) continue
    if (typeof value !== 'string') continue
    model.endpoints[key] = { enabled: true, url: value }
  }

  return model
}

// Single-URL predicate mirroring the backend's own ValidateProtocolEndpoints
// (internal/service/provider_protocol.go): an empty string is valid (means
// "reuse the provider's base_url"), otherwise the value must parse as an
// absolute http(s) URL with a non-empty host.
export function isValidEndpointUrl(value: string): boolean {
  if (!value) return true
  let parsed: URL
  try {
    parsed = new URL(value)
  } catch {
    return false
  }
  return (parsed.protocol === 'http:' || parsed.protocol === 'https:') && !!parsed.host
}

// ProtocolConfigFields.vue's endpoint-url n-form-items have a `:rule` but no
// `path`, so they are NOT registered in the parent form's validation map and
// formRef.validate() silently skips them. This re-checks the same rule
// across all enabled additional-protocol endpoints so an invalid URL can
// never reach the backend — used by both provider modals before submit.
export function protocolEndpointsValid(model: ProtocolConfigModel): boolean {
  for (const protocol of ALL_PROTOCOLS) {
    const entry = model.endpoints[protocol]
    if (!entry.enabled || !entry.url) continue
    if (!isValidEndpointUrl(entry.url)) return false
  }
  return true
}

export function serializeProtocolConfig(model: ProtocolConfigModel): {
  provider_type: string
  protocol_endpoints: string
} {
  const extras: Record<string, string> = {}
  for (const proto of ALL_PROTOCOLS) {
    if (proto === model.providerType) continue
    const entry = model.endpoints[proto]
    if (!entry.enabled) continue
    extras[proto] = entry.url.trim()
  }

  const hasExtras = Object.keys(extras).length > 0
  return {
    provider_type: model.providerType,
    protocol_endpoints: hasExtras ? JSON.stringify(extras) : '',
  }
}
