# Changelog

All notable changes to this project are documented here.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Fixed

- `stop` reported `no running instance` and exited 0 while the server was running, whenever it was invoked from a directory other than the one the service runs in. It generated a config in the working directory and probed that empty deployment's lock instead of the real one. It no longer generates anything: it reports the path it looked at, and — when the binary belongs to an installation — the `--config` line to use instead. It also prints the config it resolved alongside every result, so an answer about a deployment is never shown without the deployment it is about.
- `db:rollback` generated a config when none was there, which on a deployment whose config had been lost pointed it straight back at the database still on disk. It now requires the config to exist, and prints the config and database it is about to act on.
- `db:rollback` ran its down migrations against a live server. It now takes the same instance lock `serve` holds for its lifetime, which `db:reset` has always required, instead of dropping tables and columns out from under a running process.
- A relative `sqlite_path` now resolves to one spelling however the config was reached. The lock file and, on Windows, the name of the shutdown event `stop` signals are both derived from that string, so two spellings of one deployment could leave `stop` signalling an event nobody listens on and timing out against a healthy server.

## [0.1.2] - 2026-07-31

A maintenance release on top of 0.1.1: native Windows install and development
scripts, the hosted service as a provider preset, and fixes to usage
accounting, the documentation links and the dashboard trend chart.

### Added

- Windows PowerShell scripts: `install.ps1`, runnable as an `irm ... | iex` one-liner, which registers the server as a scheduled task and installs machine-wide or per-user depending on elevation; and `dev.ps1` for local development. Both are documented in the READMEs and CONTRIBUTING.
- The hosted service as a provider preset. It runs this same gateway, so all four protocols are declared against a single base URL through a new optional `extraProtocols` preset field, giving Anthropic, Gemini and Responses callers native passthrough instead of a cross-protocol translation. No key ships with it.
- Preset cards link to their own provider console once picked, since by then a key is the only thing left to supply.
- Actions menu on the provider list with a direct link to that provider's cost detail page.

### Changed

- The custom system prompt is now a single "Concise Output" toggle. The free-form prompt editor is gone from both the global cost-optimization modal and the per-key one; the text comes from the built-in concise and minimal-code presets instead.
- The create-provider dialog opens with the hosted preset applied, so the common path is paste a key and save. Picking "custom" now clears the preset-owned fields (name, base URL, test model, protocol config) rather than only moving the highlight, which previously let a custom provider inherit the preselected preset's values.
- The READMEs are a user-facing overview again: what the project is, how to run it, and where the full documentation lives. The build, test and local development material moved into CONTRIBUTING.md.

### Fixed

- Usage was dropped whole for OpenAI-compatible upstreams that front an Anthropic model and copy the net input count into `prompt_tokens` while reporting the cached portion only under `prompt_tokens_details`. Read under the OpenAI convention, where the cached count is a subset of the prompt, such a record had its cache subtracted from a prompt that never contained it, and went negative when the cache exceeded the prompt — at which point it was rejected entirely, zeroing the completion and cache counts along with the input and logging a successful request as costing nothing. The convention is now settled once, before pricing or persistence read the usage, and only when the inclusive reading is positively ruled out.
- The released binaries can update themselves. The repository that `update` and the update-check API look for releases in is injected at build time, and the variable holding it was never set once the repository went public, so 0.1.0 and 0.1.1 both shipped with it empty — visible only to a user who runs `update` and is told it is disabled. A release now fails outright rather than publishing artifacts that quietly cannot update.
- Documentation links in the READMEs. The documentation is a separate app the main site embeds, so every hardcoded `/docs/...` link resolved through the SPA fallback and bounced back to the homepage; all 16 now go through the embedding route, which keeps the site chrome and language preference around the page.
- Dashboard trend chart: the two Y axes picked their own tick counts, leaving the cost labels between grid lines with no reference to read the line against. Both axes now share a tick count and a pinned zero. Line smoothing is also gone — daily buckets are discrete, and the spline overshot around a zero-to-peak jump, drawing cost on days that had none.
- Both PowerShell scripts were saved in GBK. PowerShell 7 assumes UTF-8 without a byte-order mark, so every localized string decoded to replacement characters; 5.1 falls back to the system code page, which only lines up on a Chinese-locale host. They are now UTF-8 with a BOM, the one form both read correctly.

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

[Unreleased]: https://github.com/yolorouter/yolorouter/compare/v0.1.2...HEAD
[0.1.2]: https://github.com/yolorouter/yolorouter/compare/v0.1.1...v0.1.2
[0.1.1]: https://github.com/yolorouter/yolorouter/compare/v0.1.0...v0.1.1
[0.1.0]: https://github.com/yolorouter/yolorouter/releases/tag/v0.1.0
