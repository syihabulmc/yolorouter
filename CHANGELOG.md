# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [0.1.1] - 2026-07-31

Yolorouter becomes multi-protocol: it now accepts four wire protocols and can
translate any of them to any other on the way to the provider. Adds
cost-optimization features, deeper cost reporting, and Windows support.

### Added

- Protocol-agnostic intermediate representation with a full codec per protocol (request decode/encode, response decode/encode, and a streaming decoder/encoder pair each).
- Anthropic Messages ingress: `POST /v1/messages`, authenticated with `X-Api-Key` or `Authorization: Bearer`.
- OpenAI Responses ingress: `POST /v1/responses`.
- Native Gemini ingress: `POST /v1beta/models/{model}:generateContent` and `:streamGenerateContent`, additionally accepting `x-goog-api-key` and `?key=` auth.
- Protocol negotiation per request: the body passes through with only the model name rewritten when the caller's protocol matches the provider's, and round-trips through the IR when it does not.
- Per-provider protocol set with per-protocol endpoint configuration and verification.
- Model discovery: `GET /v1/models` and `GET /v1/models/{model}`, returning the OpenAI or the Anthropic envelope depending on the client. Read-only — no relay, no spend.
- Custom system prompt injection, set globally and overridable per API key, applied in the caller's own protocol shape.
- Input compression for bulky tool output (`go test` / build logs, git diffs, grep results, generic logs), with a savings dashboard and per-request skip reasons. Global setting, overridable per API key.
- Cost statistics page plus per-model, per-provider, and per-key cost detail pages.
- Dashboard time-range selector with range-aware KPIs and trend, and separate input / output / cache token cards.
- API keys: all-models scope as an alternative to an explicit allowlist.
- Provider preset catalogue, live upstream model-list fetch, and per-candidate capability probing with specific failure reasons.
- Preset batch model creation, provider model-name picker, and inline editing on model detail pages.
- Unified list filters across admin pages, plus owner and status filters for API keys.
- Configurable gateway timeouts covering seven independent phases (connect, TLS handshake, headers, first byte, inter-chunk idle, per-attempt, whole-request), validated for ordering at startup.
- `stop` command, and Windows and macOS cross-compilation targets.
- Optional GitHub mirror for both install and self-update (`update.github_proxy`).
- Startup log prints the bound listen address, a clickable localhost URL, and the primary LAN IPv4 URL.
- One-click hand-off of a model or API key to the CC Switch desktop app via a `ccswitch://` deep link.

### Changed

- Replaced the single 120s upstream wall clock with the staged idle-keepalive timeouts described above, so reasoning models that pause for minutes before the first token are no longer cut off mid-request.
- Candidate mappings are verified against the real upstream when saved, replacing the manual test buttons.
- Timezone is taken from a browser-supplied IANA identifier via the `X-Timezone` header instead of being inferred server-side.
- Redesigned login and first-run setup shell with a shared language switcher; the dashboard empty state is now a setup-progress funnel banner.
- Restructured the admin sidebar into grouped navigation; empty states and the setup banner gained contextual icon tiles.
- All create and edit panels unified on a modal layout, dropping the previous drawer.
- Reworked the API key form's custom-prompt and expiry layout.
- `make build-windows` now cross-compiles runnable Windows binaries for amd64 and arm64 with the frontend embedded. The previous compile-only check, which produced no artifact, is now `make build-windows-check`.
- CI runs the test suite on a Windows host in addition to Linux; cross-compiling alone cannot catch platform behavior that only differs at runtime.

### Fixed

- Windows builds could not start at all: the config file's permission check rejected every file on Windows, where Go synthesizes `0666` for any writable file and `os.Chmod` only toggles the read-only attribute. Since first run generates the config and then reads it back, the server failed before serving anything. The check is now platform-split; Windows logs a warning that permissions cannot be enforced there and continues.
- Net (non-cached) input tokens no longer deduct cache-write tokens from the prompt total. No protocol counts cache writes inside the prompt, so the deduction understated both the input line of the bill and the stored input token count. Net input is now persisted consistently across the streaming and non-streaming paths.
- DeepSeek usage mapping: `prompt_cache_miss_tokens` is the non-cached prompt remainder, not a cache write, and was being priced at the cache-write rate while driving net input to zero. `prompt_cache_hit_tokens` was never read on the passthrough path, leaving cache reads billed at the full input price.
- Upstream token counts are checked for coherence before pricing — negatives, a cache read larger than an inclusive prompt, and parts exceeding a stated total now mark usage unknown instead of producing a fabricated charge. The micros conversion is range-guarded so an out-of-range value cannot corrupt budget accounting.
- Claude streaming `input_tokens` is adopted from the terminal `message_delta` when `message_start` reports zero, which is what a translating upstream does. Total tokens are recomputed from the per-field values on each delta rather than held at a high-water mark.
- Gemini thinking and tool-use tokens are accounted for correctly.
- The provider streaming probe now requires both a non-empty content delta and a clean termination, so an endpoint that emits one delta and then hangs is no longer certified as streaming-capable. Provider URLs and transport error strings are redacted in probe logs so credentials embedded in a base URL cannot leak.
- Installer error handling, uninstall scope detection, and upgrade safety.
- Frontend: TypeScript type errors, auth layout, chart empty state, and import toast behavior.

## [0.1.0]

Initial release. The core loop is complete: configure providers, route with
failover, and observe usage and cost.

### Added

- OpenAI-compatible gateway: `POST /v1/chat/completions` with streaming (SSE) and function calling (`tools` / `tool_choice` / `parallel_tool_calls`).
- Multi-provider routing with ordered failover, keeping the public model name stable to the caller.
- Upstream API key pools with automatic rotation on rate-limit / auth-failure / quota-exhaustion.
- Model aliasing (public model name → per-provider model id) with per-candidate capability flags (streaming, function calling).
- API key management: model allowlist, request-rate and concurrency limits, cumulative budget cap, expiry, and instant revocation. Full key shown once on creation.
- Admin console: dashboard, usage & cost analytics (by model / provider / time / caller), and request logs with the full per-attempt routing trace. CSV export.
- First-run setup: create the initial admin, guided provider / model / key configuration.
- Bilingual admin UI (English / 简体中文).
- Single binary with the web console embedded via `go:embed`; SQLite or PostgreSQL storage; upstream keys encrypted at rest (AES-256).
- Self-update via the `update` command and update-check API.

[Unreleased]: https://github.com/yolorouter/yolorouter/compare/v0.1.1...HEAD
[0.1.1]: https://github.com/yolorouter/yolorouter/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/yolorouter/yolorouter/releases/tag/v0.1.0
