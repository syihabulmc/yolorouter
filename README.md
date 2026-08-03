<div align="center">

# Yolorouter

**A free, self-hosted LLM gateway that speaks four wire protocols, fails over across providers, rotates upstream keys, and ships with an admin console in one binary.**

[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![CI](https://github.com/yolorouter/yolorouter/actions/workflows/ci.yml/badge.svg)](https://github.com/yolorouter/yolorouter/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/yolorouter/yolorouter)](https://goreportcard.com/report/github.com/yolorouter/yolorouter)
[![Release](https://img.shields.io/github/v/release/yolorouter/yolorouter?sort=semver)](https://github.com/yolorouter/yolorouter/releases)
[![Go](https://img.shields.io/badge/go-1.25.7+-00ADD8.svg)](go.mod)

English · [简体中文](README_zh.md)

[Quick start](#quick-start) · [Protocols](#protocols) · [Cost optimization](#cost-optimization) · [Documentation](#documentation) · [Contributing](#contributing)

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

Everything ships as a **single binary** with the web console embedded. No Node
runtime, no separate frontend deploy, no external services required — SQLite
works out of the box, PostgreSQL when you want it.

## Why Yolorouter

**Routing**

- **Multi-provider failover** — map one public model name (e.g. `smart`) to an ordered list of provider candidates. When one is down, requests fail over to the next — transparently, without the caller ever seeing a different model name.
- **Upstream key rotation** — give each provider a pool of upstream keys. Rate-limited, unauthorized, or quota-exhausted keys are skipped automatically.
- **Model aliasing** — callers request a stable public name; each provider candidate maps it to whatever model id that provider actually expects. Candidate mappings are probed against the real upstream when you save them, so a typo is caught at configuration time, not at 3 a.m.
- **Streaming done right** — key rotation and failover happen *before* the first byte reaches the client; once streaming starts, the provider is locked in. Content from two providers is never stitched into one response.
- **Timeouts tuned for reasoning models** — seven independent, configurable phases instead of one wall clock, so a model that thinks for eight minutes before emitting a token isn't killed mid-thought.

**Control & cost**

- **Per-key access control** — model allowlists, rate and concurrency limits, cumulative budget caps, optional expiry, instant revocation.
- **Cost optimization** — inject a custom system prompt globally or per key; compress bulky tool output before it reaches the upstream. The console reports what each actually saved.
- **Built-in observability** — token and cost KPIs, usage by model / provider / time / caller, and request logs with the full per-attempt routing chain. Any view exports to CSV.
- **Bilingual console** — English and 简体中文, switchable anywhere; timezone follows the browser.
- **Self-update** — the binary can check for and apply new releases.

## Screenshots

<div align="center">
  <img src="docs/screenshots/dashboard.png" alt="Dashboard" width="49%" />
  <img src="docs/screenshots/analytics.png" alt="Analytics" width="49%" />
</div>

## Quick start

Install as a background service that starts on boot — systemd on Linux, launchd on
macOS, a scheduled task on Windows:

```bash
# Linux / macOS
curl -fsSL https://get.yolorouter.com/install.sh | bash
```

```powershell
# Windows, PowerShell 5.1+
irm https://get.yolorouter.com/install.ps1 | iex
```

On Windows, an elevated PowerShell installs a system-wide service that starts at
boot; a normal one installs under your account and starts at logon.

> **🇨🇳 China mirror**: if GitHub is slow or unreachable from your network, swap
> `get.yolorouter.com` for `gh.yolorouter.com` — same installers, routed through a
> Cloudflare proxy, and auto-updates keep using the mirror afterwards.

Re-run the same command to upgrade; configuration and database are preserved and
the database is backed up first. Prefer a plain binary? Grab a
[release](https://github.com/yolorouter/yolorouter/releases) and run
`./yolorouter serve` (`.\yolorouter.exe serve` on Windows).

The first run generates `configs/config.yaml`, applies migrations and starts the
console on port 8080. Create the first admin account, then follow the guided flow:
add providers and upstream keys, create models with their provider candidates, and
issue API keys.

→ **Full installation guide for every platform, including building from source:**
[yolorouter.com/help?p=self-hosted/installation](https://yolorouter.com/help?p=self-hosted/installation&utm_source=oss-readme&utm_medium=repo)

## Protocols

Every ingress below authenticates with the **same** Yolorouter API key, supports
streaming, and can be served by **any** configured provider — no matter which
protocol that provider natively speaks.

| Ingress route | Protocol | Accepted auth headers |
| --- | --- | --- |
| `POST /v1/chat/completions` | OpenAI Chat Completions | `Authorization: Bearer`, `X-Api-Key` |
| `POST /v1/responses` | OpenAI Responses | `Authorization: Bearer`, `X-Api-Key` |
| `POST /v1/messages` | Anthropic Messages | `Authorization: Bearer`, `X-Api-Key` |
| `POST /v1beta/models/{model}:generateContent`<br>`POST /v1beta/models/{model}:streamGenerateContent` | Gemini | `x-goog-api-key`, `?key=`, `Authorization: Bearer`, `X-Api-Key` |
| `GET /v1/models`, `GET /v1/models/{model}` | Model discovery | `Authorization: Bearer`, `X-Api-Key` |

The `model` in every request is the **public name** you configured. Yolorouter picks
a provider candidate, swaps in the real upstream model id, and keeps your public
name in the response.

> **Known limitation**: `input_image` entries on the Responses ingress are dropped
> when the request has to be translated to a different egress protocol; only text is
> forwarded. Same-protocol passthrough is unaffected, and image content translates
> correctly on the other three ingresses.

### Point existing SDKs and tools at it

Because the ingresses are the real native protocols, official SDKs and agent tools
need two settings changed and no adapter layer:

```python
# OpenAI Python SDK
from openai import OpenAI

client = OpenAI(base_url="http://localhost:8080/v1", api_key="sk-yr-your-key")
print(client.chat.completions.create(
    model="smart",
    messages=[{"role": "user", "content": "Hello!"}],
).choices[0].message.content)
```

```bash
# Claude Code — routed through Yolorouter to whichever provider you configured
export ANTHROPIC_BASE_URL=http://localhost:8080
export ANTHROPIC_AUTH_TOKEN=sk-yr-your-key
claude
```

→ **Per-protocol request examples and setup guides for 19 agent tools**
(Claude Code, Cursor, Codex CLI, Cherry Studio, Gemini CLI, opencode …):
[yolorouter.com/help](https://yolorouter.com/help?utm_source=oss-readme&utm_medium=repo)

## Cost optimization

Both features are off by default, configured globally in the console, and
overridable per API key.

**Custom system prompt injection.** Append house rules to every request's system
prompt without touching client code. The injection follows the caller's own protocol
shape and is deterministic, so repeated requests produce byte-identical system
content and still hit upstream prompt caches.

**Input compression.** Coding agents send back huge, highly redundant tool output.
Yolorouter recognizes what each content block is — `go test` output, git diffs,
grep results, plain logs — and strips the noise while keeping every signal: failures,
stack traces, and each distinct match are preserved. It never touches the active edit
region at the tail of the conversation, and only replaces a block when the compressed
form is actually shorter.

Cache-read and cache-write tokens are metered and priced separately throughout the
dashboard, so prompt-cache savings are a number you can see rather than a feeling.

→ **Details and tuning:**
[yolorouter.com/help?p=self-hosted/configuration](https://yolorouter.com/help?p=self-hosted/configuration&utm_source=oss-readme&utm_medium=repo)

## Documentation

| Topic | Link |
| --- | --- |
| Installation (all platforms, from source) | [Installation](https://yolorouter.com/help?p=self-hosted/installation&utm_source=oss-readme&utm_medium=repo) |
| Every `config.yaml` field and the CLI | [Configuration](https://yolorouter.com/help?p=self-hosted/configuration&utm_source=oss-readme&utm_medium=repo) |
| Upgrading, rolling back, uninstalling | [Updating](https://yolorouter.com/help?p=self-hosted/updating&utm_source=oss-readme&utm_medium=repo) |
| Layering, protocol IR, storage | [Architecture](https://yolorouter.com/help?p=self-hosted/architecture&utm_source=oss-readme&utm_medium=repo) |
| API reference and model catalogue | [Docs home](https://yolorouter.com/help?utm_source=oss-readme&utm_medium=repo) |

Self-hosting means bringing your own upstream API keys. If you would rather not sign
up with every provider separately, **YoloRouter Cloud** ships in the console's provider
preset list as one more upstream you can select — see
[the hosted option](https://yolorouter.com/pricing?utm_source=oss-readme&utm_medium=repo).

## Build from source

Requires **Go 1.25.7+** and **Node.js 22.12+**.

```bash
make build          # backend only -> ./bin/yolorouter
make build-embed    # full binary with the console embedded
```

Cross-compilation targets, the test commands and the local development workflow
are documented in [CONTRIBUTING.md](CONTRIBUTING.md#building-and-testing).

## Contributing

Issues and pull requests are welcome. Please read [CONTRIBUTING.md](CONTRIBUTING.md)
and the [Code of Conduct](CODE_OF_CONDUCT.md) first. For security reports see
[SECURITY.md](SECURITY.md).

## License

Licensed under the [Apache License 2.0](LICENSE).
