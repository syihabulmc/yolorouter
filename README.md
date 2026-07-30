<div align="center">

# Yolorouter

**A free, self-hosted LLM gateway that speaks four wire protocols, fails over across providers, rotates upstream keys, and ships with an admin console in one binary.**

[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![CI](https://github.com/yolorouter/yolorouter/actions/workflows/ci.yml/badge.svg)](https://github.com/yolorouter/yolorouter/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/yolorouter/yolorouter)](https://goreportcard.com/report/github.com/yolorouter/yolorouter)
[![Release](https://img.shields.io/github/v/release/yolorouter/yolorouter?sort=semver)](https://github.com/yolorouter/yolorouter/releases)
[![Go](https://img.shields.io/badge/go-1.25.7+-00ADD8.svg)](go.mod)

English · [简体中文](README_zh.md)

[Quick start](#quick-start) · [Protocols](#protocols) · [Cost optimization](#cost-optimization) · [Configuration](#configuration) · [Architecture](#architecture) · [Contributing](#contributing)

⚡ **Low-overhead streaming proxy** · 🔀 **Any protocol in, any protocol out** · 🆓 **Free & open-source** · 📦 **Single binary, zero external deps** · 🔁 **Automatic failover + key rotation** · 💰 **Cost analytics & optimization**

</div>

---

Point your application at **one** endpoint and **one** API key. Yolorouter sits
between your apps and your upstream providers, so the messy parts — juggling
provider accounts, rotating rate-limited keys, failing over when an account
breaks, enforcing per-key budgets, and knowing what everything costs — live in
one place instead of scattered across every codebase.

It accepts **four wire protocols** — OpenAI Chat Completions, OpenAI Responses,
Anthropic Messages, and Gemini `generateContent` — and can translate any of
them to any other on the way out. An OpenAI-only provider can serve Claude
Code; an Anthropic-only provider can serve the OpenAI SDK. Streaming, tool
calling, and reasoning/thinking blocks all survive the trip, as does image
content on every ingress except Responses (see [Protocols](#protocols)).

Everything ships as a **single binary** with the web console embedded. No
Node runtime, no separate frontend deploy, no external services required —
SQLite works out of the box, PostgreSQL when you want it.

## Why Yolorouter

**Routing**

- **Multi-provider failover** — map one public model name (e.g. `smart`) to an ordered list of provider candidates. When one is down, requests fail over to the next — transparently, without the caller ever seeing a different model name.
- **Upstream key rotation** — give each provider a pool of upstream keys. Rate-limited, unauthorized, or quota-exhausted keys are skipped automatically; the request retries the next key before failing over.
- **Model aliasing** — callers request a stable public name; each provider candidate maps it to whatever model id that provider actually expects. Candidate mappings are probed against the real upstream when you save them, so a typo is caught at configuration time, not at 3 a.m.
- **Streaming done right** — key rotation and failover happen *before* the first byte reaches the client; once streaming starts, the provider is locked in. Content from two providers is never stitched into one response.
- **Timeouts tuned for reasoning models** — seven independent, configurable phases (connect, TLS handshake, headers, first byte, inter-chunk idle, per-attempt, whole-request) instead of one wall clock, so a model that thinks for eight minutes before emitting a token isn't killed mid-thought.

**Protocol translation**

- **Four ingress endpoints, four egress protocols** — see [Protocols](#protocols). When the caller's protocol matches the provider's, the body passes through with only the model name rewritten. When it doesn't, the request is decoded into a protocol-agnostic intermediate representation and re-encoded for the provider — including the streaming event grammar of both sides.
- **Model discovery** — `GET /v1/models` returns the models the presenting key may call, in the OpenAI shape or the Anthropic shape depending on the client.

**Control & cost**

- **Per-key access control** — every issued key carries either an explicit model allowlist or an all-models scope, plus request-rate / concurrency limits, a cumulative budget cap, and an optional expiry. Revoke instantly.
- **Cost optimization** — inject a custom system prompt globally or per key, and compress bulky tool output (build logs, git diffs, grep results) before it reaches the upstream. The console reports what each feature actually saved.
- **Observability built in** — a dashboard with token and cost KPIs over any time range, usage & cost analytics (by model / provider / time / caller), per-model / per-provider / per-key cost detail pages, and full request logs with the complete per-attempt routing trace and captured bodies. Export any view to CSV.
- **Bilingual admin console** — English and 简体中文, switchable anywhere, before or after login. Timezone follows the browser.
- **Self-updating** — the binary can check for and apply new releases.

## Screenshots

<p align="center">
  <img src="docs/screenshots/dashboard.png" width="49%" alt="Dashboard" />
  <img src="docs/screenshots/analytics.png" width="49%" alt="Usage & cost analytics" />
</p>

## Quick start

### Install as a service (one command)

Install yolorouter as a boot-persistent background service (systemd on Linux,
launchd on macOS):

```bash
curl -fsSL https://get.yolorouter.com/install.sh | bash
# or straight from GitHub:
# curl -fsSL https://raw.githubusercontent.com/yolorouter/yolorouter/main/scripts/install.sh | bash
```

> **In mainland China** (or any network where GitHub is slow or blocked), use
> the accelerated mirror — same installer, routed through a Cloudflare proxy,
> and self-update stays on the mirror automatically:
> ```bash
> curl -fsSL https://gh.yolorouter.com/install.sh | bash
> ```

The installer picks a UI language, detects your OS/arch, downloads and
sha256-verifies the matching release, sets up a self-contained app-home
directory, then starts and health-checks the service. Re-run the same command
to upgrade (config and database are preserved, and the database is backed up
first). Uninstall by replacing the trailing `bash` with `bash -s -- --uninstall`:

```bash
curl -fsSL https://get.yolorouter.com/install.sh | bash -s -- --uninstall
# China mirror:
# curl -fsSL https://gh.yolorouter.com/install.sh | bash -s -- --uninstall
```

Optional environment overrides: `YOLO_LANG=zh|en`, `YOLO_SCOPE=system|user`,
`YOLO_VERSION=vX.Y.Z`, `YOLO_REPO=owner/repo`, `YOLO_MIRROR=https://host/`. A
system install needs root/sudo; without them the installer falls back to a
user-level service.

### Run a release binary

Download the archive for your platform from the
[latest release](https://github.com/yolorouter/yolorouter/releases), extract it, then:

```bash
./yolorouter serve
```

Release archives cover Linux, macOS, and Windows on both amd64 and arm64
(Windows as a `.zip`, the rest as `.tar.gz`). The one-command installer above
is Linux and macOS only — on Windows, extract the archive and run
`yolorouter.exe serve` from the directory you want `configs\` and `data\`
created in (both are resolved relative to the working directory, not the
executable).

> On Windows, config file permissions cannot be enforced — access there is
> governed by ACLs, and the permission bits Go reports are synthesized, not
> real. The server logs a warning at startup with a ready-to-run `icacls`
> command for the current account, then continues. To restrict the file
> manually:
>
> ```powershell
> icacls "configs\config.yaml" /inheritance:r `
>   /remove:g *S-1-1-0 /remove:g *S-1-5-32-545 /remove:g *S-1-5-11 `
>   /grant:r "${env:USERNAME}:F"
> icacls "configs\config.yaml"   # confirm only your account is listed
> ```
>
> `/inheritance:r` drops inherited entries and `/grant:r` replaces the grant
> only for the account it names, so the `/remove:g` entries are what clear the
> broad principals (Everyone, Users, Authenticated Users — removed by SID
> because their display names are localized). That covers the realistic cases
> but cannot guarantee an empty ACL for an arbitrary principal, which is why
> the second command is worth running. To rebuild the ACL from scratch instead,
> use PowerShell's `Set-Acl` with `SetAccessRuleProtection($true, $false)`.
>
> In `cmd.exe`, use `"%USERNAME%:F"` and `^` for line continuation instead of
> the backtick — `${env:...}` is PowerShell-only syntax and `%...%` is
> cmd-only, so the two are not interchangeable.

On first run it generates `configs/config.yaml` (including a random AES-256
master key used to encrypt stored upstream keys), applies database migrations,
and serves the console on <http://localhost:8080> — the startup log prints both
the localhost URL and the LAN URL so you can open it from another machine.
Create the first admin account, then follow the setup flow: add a provider and
an upstream key, create a model with its provider candidates, and issue an API
key.

Provider setup is preset-driven: pick a known provider from the catalogue to
get its base URL and protocol filled in, paste a key, and let Yolorouter fetch
the provider's live model list instead of typing model ids by hand.

## Protocols

Every ingress route below is authenticated with the **same** Yolorouter API
key, accepts streaming, and can be served by **any** configured provider
regardless of the protocol that provider speaks natively.

| Ingress route | Protocol | Accepted auth headers |
| --- | --- | --- |
| `POST /v1/chat/completions` | OpenAI Chat Completions | `Authorization: Bearer`, `X-Api-Key` |
| `POST /v1/responses` | OpenAI Responses | `Authorization: Bearer`, `X-Api-Key` |
| `POST /v1/messages` | Anthropic Messages | `Authorization: Bearer`, `X-Api-Key` |
| `POST /v1beta/models/{model}:generateContent`<br>`POST /v1beta/models/{model}:streamGenerateContent` | Gemini | `x-goog-api-key`, `?key=`, `Authorization: Bearer`, `X-Api-Key` |
| `GET /v1/models`, `GET /v1/models/{model}` | model discovery | `Authorization: Bearer`, `X-Api-Key` |

In every example below, `model` is the **public name you configured** in the
console. Yolorouter picks a provider candidate, substitutes the real upstream
model id, and returns a response with your public name preserved.

> **Known limitation.** On the Responses ingress, `input_image` items are
> dropped when the request has to be translated to a different egress protocol
> — only text is carried across. Same-protocol passthrough (a Responses caller
> on a Responses provider) preserves them. The other three ingresses translate
> image content correctly.

### OpenAI Chat Completions

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-yr-your-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "smart",
    "messages": [{"role": "user", "content": "Hello!"}],
    "stream": true
  }'
```

With tool calling:

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-yr-your-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "smart",
    "messages": [{"role": "user", "content": "What is the weather in Shanghai?"}],
    "tools": [{
      "type": "function",
      "function": {
        "name": "get_weather",
        "description": "Get the current weather for a city",
        "parameters": {
          "type": "object",
          "properties": {"city": {"type": "string"}},
          "required": ["city"]
        }
      }
    }],
    "tool_choice": "auto"
  }'
```

### Anthropic Messages

```bash
curl http://localhost:8080/v1/messages \
  -H "x-api-key: sk-yr-your-key" \
  -H "anthropic-version: 2023-06-01" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "smart",
    "max_tokens": 1024,
    "system": "You are concise.",
    "messages": [{"role": "user", "content": "Hello!"}],
    "stream": true
  }'
```

### OpenAI Responses

```bash
curl http://localhost:8080/v1/responses \
  -H "Authorization: Bearer sk-yr-your-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "smart",
    "instructions": "You are concise.",
    "input": "Hello!"
  }'
```

### Gemini

```bash
curl "http://localhost:8080/v1beta/models/smart:generateContent" \
  -H "x-goog-api-key: sk-yr-your-key" \
  -H "Content-Type: application/json" \
  -d '{
    "contents": [{"role": "user", "parts": [{"text": "Hello!"}]}]
  }'

# Streaming, key in the query string (what the Google SDKs do):
curl "http://localhost:8080/v1beta/models/smart:streamGenerateContent?key=sk-yr-your-key" \
  -H "Content-Type: application/json" \
  -d '{"contents": [{"role": "user", "parts": [{"text": "Hello!"}]}]}'
```

### Model discovery

```bash
# OpenAI shape
curl http://localhost:8080/v1/models -H "Authorization: Bearer sk-yr-your-key"

# Anthropic shape — selected by the anthropic-version header
curl http://localhost:8080/v1/models \
  -H "x-api-key: sk-yr-your-key" \
  -H "anthropic-version: 2023-06-01"
```

### Point existing SDKs and tools at it

Because the ingress protocols are the real thing, official SDKs and
agent tools work by changing two settings — no shims.

```python
# OpenAI Python SDK
from openai import OpenAI

client = OpenAI(base_url="http://localhost:8080/v1", api_key="sk-yr-your-key")
print(client.chat.completions.create(
    model="smart",
    messages=[{"role": "user", "content": "Hello!"}],
).choices[0].message.content)
```

```python
# Anthropic Python SDK — same gateway, same key
from anthropic import Anthropic

client = Anthropic(base_url="http://localhost:8080", api_key="sk-yr-your-key")
print(client.messages.create(
    model="smart",
    max_tokens=1024,
    messages=[{"role": "user", "content": "Hello!"}],
).content[0].text)
```

```bash
# Claude Code — routes through Yolorouter to whatever provider you configured
export ANTHROPIC_BASE_URL=http://localhost:8080
export ANTHROPIC_AUTH_TOKEN=sk-yr-your-key
claude
```

## Cost optimization

Both features are off by default, set globally in the console, and can be
overridden per API key.

**Custom system prompt injection.** Append house rules to every request's
system prompt without touching client code. The text is appended to the
existing system block in the caller's own protocol shape (OpenAI `messages`,
Anthropic `system`, Responses `instructions`, Gemini `systemInstruction`), and
a new system block is created when the request has none. Injection is
deterministic, so the resulting system content is byte-identical across
requests and stays eligible for upstream prompt caching. Malformed request
bodies are forwarded untouched rather than silently rewritten.

**Input compression.** Coding agents send back huge, highly redundant tool
output. Yolorouter detects the content type of each block in the request and
rewrites the noise out of it, keeping the signal intact:

| Detected content | What gets removed |
| --- | --- |
| `go test` / build logs | passing-test boilerplate (`=== RUN`, `--- PASS`, `=== CONT`) — failures, skips, panics, stacks and summary lines are all kept, and fenced code blocks are emitted verbatim |
| git diffs | `index abc..def` blob-hash headers and ANSI escapes — hunks are never truncated |
| grep / ripgrep results | runs of identical match lines folded into one line plus a repeat count; every distinct `path:line:match` survives |
| generic logs | ANSI escapes, runs of identical lines, runs of blank lines |

Compression never touches the live edit zone at the tail of the conversation,
and a block is only rewritten when the result is actually shorter. The console
shows tokens saved over time and, per request log, which blocks were compressed
or why they were skipped.

Cache-read and cache-write tokens are tracked and priced separately throughout
the dashboard, analytics, and cost pages, so prompt-caching savings show up as
a number rather than a vibe.

## Configuration

Configuration lives in `configs/config.yaml`, auto-generated on first run. You
rarely need to edit it by hand.

```yaml
server:
  port: 8080
database:
  driver: sqlite            # sqlite | postgres
  sqlite_path: ../data/yolorouter.db
  # host/port/user/password/dbname/sslmode apply when driver: postgres
log:
  level: info
security:
  provider_master_key: ""   # base64 AES-256 key; auto-generated when blank
  allow_private_upstreams: false  # allow loopback/private upstreams (local Ollama, vLLM, ...)
update:
  enabled: true             # set false to disable the update-check API and CLI
  github_repo: ""           # "owner/repo" override for update checks
  github_proxy: ""          # e.g. https://gh.yolorouter.com/ to route updates via a mirror
gateway:                    # upstream relay timeouts; the whole block is optional
  connect_timeout: 5s       # TCP dial
  header_timeout: 600s      # request sent -> response headers
  first_byte_timeout: 600s  # headers -> first body chunk (the "thinking" gap)
  body_idle_timeout: 60s    # max gap between two streamed chunks
  attempt_timeout: 20m      # hard wall for one key on one candidate
  request_timeout: 30m      # total budget across all failover candidates
  tls_handshake_timeout: 10s
```

Notes worth knowing:

- Relative `sqlite_path` resolves against the config file's directory, not the process CWD.
- If the config file already exists, `provider_master_key` must be a real key — it is only auto-filled on the initial generate path.
- A hand-copied config must be `chmod 600` or it is refused.
- `allow_private_upstreams` exists so you can point a provider at a local Ollama / vLLM / LM Studio. It is off by default as SSRF defense — never enable it on an internet-exposed or multi-tenant deployment.
- Timeout ordering is validated at startup: `header_timeout` and `first_byte_timeout` must be ≤ `attempt_timeout`, and `attempt_timeout` < `request_timeout`.

See [`configs/config.example.yaml`](configs/config.example.yaml) for the full
annotated reference.

### CLI

Every subcommand accepts `--config <path>`.

```bash
./yolorouter serve            # start the HTTP server and background supervisor
./yolorouter stop             # stop the running server
./yolorouter update           # self-update to the latest GitHub release
./yolorouter db:migrate       # apply pending migrations
./yolorouter db:status        # show the current migration version
./yolorouter db:rollback [v]  # roll back one migration, or down to version v
./yolorouter db:backup --output-dir backups
./yolorouter db:reset         # drop all tables and re-migrate; development
                              # builds only, disabled in release binaries
./yolorouter --version
./yolorouter --help
```

## Build from source

Requirements: **Go 1.25.7+** and **Node.js 22.12+**.

```bash
# Backend only — serves a placeholder page instead of the console
make build          # -> ./bin/yolorouter

# Full binary with the web console embedded
make build-embed    # -> ./bin/yolorouter (frontend built + embedded)

# Cross-compile (frontend embedded)
make build-macos          # -> ./bin/yolorouter-darwin-{amd64,arm64}
make build-windows        # -> ./bin/yolorouter-windows-{amd64,arm64}.exe

# Fast compile-only check for windows — no frontend build, no artifact
make build-windows-check
```

## Development

```bash
./scripts/dev.sh              # rebuild frontend + backend, migrate, (re)start
./scripts/dev.sh --backend    # backend only
./scripts/dev.sh --frontend   # frontend only
./scripts/dev.sh --help       # all modes + env vars (YOLO_LANG, NO_COLOR)

make test                     # go test ./...
make test-embed               # tests with the embedded frontend build tag
make vet                      # go vet (plain + -tags release)
```

See [CONTRIBUTING.md](CONTRIBUTING.md) for the full workflow and coding standards.

## Architecture

```
  OpenAI Chat Completions ─┐                                        ┌─ OpenAI-native provider
  OpenAI Responses ────────┤                                        ├─ Anthropic-native provider
  Anthropic Messages ──────┼──▶ ┌──────────────────────────────┐ ──▶├─ Gemini-native provider
  Gemini generateContent ──┘    │          Yolorouter          │    └─ local Ollama / vLLM / ...
                                │                              │
                                │  auth · limits · budget      │
  ┌────────────┐   admin UI     │  protocol negotiation + IR   │
  │  operator  │ ─────────────▶ │  model alias · candidates    │
  └────────────┘  embedded Vue  │  key rotation · failover     │
                                │  compression · logging       │
                                └──────────────┬───────────────┘
                                               │
                                        SQLite / PostgreSQL
```

- **Backend** — Go ([Gin](https://gin-gonic.com/) + [GORM](https://gorm.io/)), migrations via [goose](https://github.com/pressly/goose). Layered handler → service → repository, with the gateway relay and protocol codecs as separate packages.
- **Protocol layer** — one intermediate representation plus a codec per protocol (decode request, encode request, decode response, encode response, and a streaming decoder/encoder pair each). Same-protocol requests skip the IR entirely.
- **Frontend** — Vue 3 + TypeScript + [naive-ui](https://www.naiveui.com/), built with Vite and embedded into the binary via `go:embed`.
- **Storage** — SQLite (pure-Go, zero-config) or PostgreSQL. Upstream keys are encrypted at rest with AES-256.

## Contributing

Issues and pull requests are welcome. Please read
[CONTRIBUTING.md](CONTRIBUTING.md) and our
[Code of Conduct](CODE_OF_CONDUCT.md) first. To report a security issue, see
[SECURITY.md](SECURITY.md).

## License

Licensed under the [Apache License 2.0](LICENSE).
