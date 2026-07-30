<div align="center">

# Yolorouter

**免费、自托管的 LLM 网关：同时支持四种协议入口、多供应商 failover、上游 Key 自动切换，管理后台内嵌于单个二进制。**

[![License](https://img.shields.io/badge/license-Apache--2.0-blue.svg)](LICENSE)
[![CI](https://github.com/yolorouter/yolorouter/actions/workflows/ci.yml/badge.svg)](https://github.com/yolorouter/yolorouter/actions/workflows/ci.yml)
[![Go Report Card](https://goreportcard.com/badge/github.com/yolorouter/yolorouter)](https://goreportcard.com/report/github.com/yolorouter/yolorouter)
[![Release](https://img.shields.io/github/v/release/yolorouter/yolorouter?sort=semver)](https://github.com/yolorouter/yolorouter/releases)
[![Go](https://img.shields.io/badge/go-1.25.7+-00ADD8.svg)](go.mod)

[English](README.md) · 简体中文

[快速开始](#快速开始) · [协议](#协议) · [成本优化](#成本优化) · [配置](#配置) · [架构](#架构) · [贡献](#贡献)

⚡ **低开销流式代理** · 🔀 **任意协议进、任意协议出** · 🆓 **免费开源** · 📦 **单二进制 · 零外部依赖** · 🔁 **自动 failover + Key 轮换** · 💰 **成本分析与优化**

</div>

---

让你的应用只对接**一个**入口、**一个** API Key。Yolorouter 位于应用和上游供应商之间，
把那些繁琐的事——管理多个供应商账号、切换被限流的 Key、账号失效时自动 failover、
按 Key 控制预算、看清成本——都收拢到一处，而不是散落在每个代码库里。

它同时接受**四种协议**入口——OpenAI Chat Completions、OpenAI Responses、
Anthropic Messages、Gemini `generateContent`——并且能在出口把任意一种翻译成另一种。
只有 OpenAI 接口的供应商可以拿来跑 Claude Code；只有 Anthropic 接口的供应商也能
直接服务 OpenAI SDK。流式、工具调用、推理（thinking）块全程保留；图片内容除
Responses 入口外也全程保留（见[协议](#协议)）。

一切都打包成**单个二进制**，管理后台已内嵌。无需 Node 运行时、无需单独部署前端、
无需外部依赖——SQLite 开箱即用，需要时可切 PostgreSQL。

## 为什么用 Yolorouter

**路由**

- **多供应商 failover** — 把一个对外模型名（如 `smart`）映射到有序的供应商候选列表。某个不可用时自动切到下一个，调用方全程只看到同一个对外模型名。
- **上游 Key 自动切换** — 每个供应商配一个 Key 池。限流、认证失败、额度不可用的 Key 会被自动跳过，请求先尝试下一个 Key，再决定是否 failover。
- **模型别名** — 调用方用稳定的对外名；每个供应商候选把它映射到该供应商实际接受的模型 id。保存候选映射时会真实探测一次上游，配错的模型名在配置阶段就暴露，而不是等到半夜出故障。
- **流式做对了** — Key 切换与 failover 都发生在首字节抵达客户端**之前**；一旦开始流式，供应商即被锁定，绝不把两个供应商的内容拼进同一个响应。
- **为推理模型调过的超时** — 七个互相独立、可配置的阶段（连接、TLS 握手、响应头、首字节、块间空闲、单次尝试、整请求），而不是一刀切的总超时，所以"想了八分钟才吐第一个 token"的模型不会被中途掐断。

**协议翻译**

- **四个入口、四种出口协议** — 见[协议](#协议)。调用方协议与供应商协议一致时，请求体直接透传，只改写模型名；不一致时先解码成协议无关的中间表示（IR），再按供应商协议重新编码——流式事件语法也一起翻译。
- **模型发现** — `GET /v1/models` 返回当前 Key 可调用的模型；按客户端类型返回 OpenAI 或 Anthropic 格式。

**管控与成本**

- **按 Key 访问控制** — 每个签发的 Key 要么带显式模型白名单，要么是全模型范围；此外还有请求速率 / 并发限制、累计预算上限、可选过期时间，支持即时吊销。
- **成本优化** — 可全局或按 Key 注入自定义系统提示词；把体积大的工具输出（构建日志、git diff、grep 结果）在发往上游前压缩。后台会显示每项功能实际省下了多少。
- **内置可观测性** — 支持任意时间范围的 token / 成本 KPI 仪表盘、用量与成本分析（按模型 / 供应商 / 时间 / 使用人）、按模型 / 供应商 / Key 的成本详情页，以及含完整逐次尝试路由链与请求体留存的请求日志。任意视图可导出 CSV。
- **双语后台** — 简体中文与 English，登录前后随处可切；时区跟随浏览器。
- **自更新** — 二进制可检查并应用新版本。

## 截图

<p align="center">
  <img src="docs/screenshots/dashboard.png" width="49%" alt="仪表盘" />
  <img src="docs/screenshots/analytics.png" width="49%" alt="用量与成本分析" />
</p>

## 快速开始

### 一键安装为服务

把 yolorouter 安装成开机自启的后台服务（Linux 用 systemd，macOS 用 launchd）：

```bash
curl -fsSL https://get.yolorouter.com/install.sh | bash
# 或直接走 GitHub：
# curl -fsSL https://raw.githubusercontent.com/yolorouter/yolorouter/main/scripts/install.sh | bash
```

> **🇨🇳 国内加速安装**：如果你在国内、直连 GitHub 慢或不通，用下面这条加速命令
> （同一个安装器，经 Cloudflare 代理下载，装完后的自动升级也会一直走加速通道，
> 无需任何额外配置）：
> ```bash
> curl -fsSL https://gh.yolorouter.com/install.sh | bash
> ```
> 若要把一台**已装好的普通机器**切换到加速通道，编辑 `config.yaml` 在 `update`
> 段加一行 `github_proxy: https://gh.yolorouter.com/` 后重启服务即可。

脚本第一步让你选界面语言，随后自动探测系统架构、下载并做 sha256 校验、建立一个
自包含的 app-home 目录，最后启动服务并做健康检查。重跑同一条命令即可升级（配置和
数据库原样保留，升级前会先自动备份数据库）。卸载时把结尾的 `bash` 换成
`bash -s -- --uninstall`：

```bash
curl -fsSL https://get.yolorouter.com/install.sh | bash -s -- --uninstall
# 国内加速：
# curl -fsSL https://gh.yolorouter.com/install.sh | bash -s -- --uninstall
```

可选环境变量覆盖：`YOLO_LANG=zh|en`、`YOLO_SCOPE=system|user`、
`YOLO_VERSION=vX.Y.Z`、`YOLO_REPO=owner/repo`、`YOLO_MIRROR=https://host/`。系统级
安装需要 root/sudo；没有时脚本会自动退回用户级服务。

### 运行发布二进制

从[最新发布](https://github.com/yolorouter/yolorouter/releases)下载对应平台的压缩包，解压后：

```bash
./yolorouter serve
```

发布包覆盖 Linux、macOS、Windows 三个平台的 amd64 与 arm64（Windows 为 `.zip`，
其余为 `.tar.gz`）。上面的一键安装脚本**只支持 Linux 和 macOS**——Windows 下解压后
在你希望生成 `configs\` 和 `data\` 的目录里运行 `yolorouter.exe serve`（这两个目录
按**工作目录**解析，不是按 exe 所在位置）。

> Windows 上无法强制配置文件权限——那里的访问由 ACL 控制，而 Go 报告的权限位是合成
> 的、不反映真实权限。服务启动时会打一条 warning（其中附带一条针对当前账户、可直接
> 复制运行的 `icacls` 命令）然后继续运行。若要手动限制：
>
> ```powershell
> icacls "configs\config.yaml" /inheritance:r `
>   /remove:g *S-1-1-0 /remove:g *S-1-5-32-545 /remove:g *S-1-5-11 `
>   /grant:r "${env:USERNAME}:F"
> icacls "configs\config.yaml"   # 确认列出的只有你自己的账户
> ```
>
> `/inheritance:r` 只删继承来的条目，`/grant:r` 只替换它指名的那个账户的授权，所以真正
> 清掉过宽主体（Everyone、Users、Authenticated Users）靠的是那几个 `/remove:g`——按 SID
> 而不是按名字删，因为这些名字是本地化的（德文 Windows 上 Everyone 显示为 Jeder）。这
> 覆盖了现实中的绝大多数情况，但无法保证对任意主体都清空，所以第二条核验命令值得跑一下。
> 若想彻底重建 ACL，用 PowerShell 的 `Set-Acl` 配合 `SetAccessRuleProtection($true, $false)`。
>
> 在 `cmd.exe` 里要改用 `"%USERNAME%:F"`，且续行符是 `^` 而非反引号——`${env:...}` 是
> PowerShell 专有语法、`%...%` 是 cmd 专有语法，两者不能互换。

首次运行会生成 `configs/config.yaml`（含用于加密上游 Key 的随机 AES-256 主密钥）、
执行数据库迁移，并在 <http://localhost:8080> 启动后台——启动日志会同时打印 localhost
地址和局域网地址，方便从另一台机器打开。创建首个管理员账号后按引导操作：添加供应商
和上游 Key，创建模型及其供应商候选，然后签发 API Key。

供应商配置是预置驱动的：从内置目录里挑一个已知供应商，base URL 和协议会自动填好，
粘贴 Key 之后可以直接拉取该供应商的线上模型列表，不用手敲模型 id。

## 协议

下面每个入口都用**同一个** Yolorouter API Key 认证、都支持流式，并且都可以由**任意**
已配置的供应商来承接——不管那个供应商原生说哪种协议。

| 入口路由 | 协议 | 可用的认证头 |
| --- | --- | --- |
| `POST /v1/chat/completions` | OpenAI Chat Completions | `Authorization: Bearer`、`X-Api-Key` |
| `POST /v1/responses` | OpenAI Responses | `Authorization: Bearer`、`X-Api-Key` |
| `POST /v1/messages` | Anthropic Messages | `Authorization: Bearer`、`X-Api-Key` |
| `POST /v1beta/models/{model}:generateContent`<br>`POST /v1beta/models/{model}:streamGenerateContent` | Gemini | `x-goog-api-key`、`?key=`、`Authorization: Bearer`、`X-Api-Key` |
| `GET /v1/models`、`GET /v1/models/{model}` | 模型发现 | `Authorization: Bearer`、`X-Api-Key` |

下面所有示例里的 `model` 都是你在后台配置的**对外名**。Yolorouter 会挑选供应商候选、
替换成真实的上游模型 id，并在返回时保持你的对外名不变。

> **已知限制**：Responses 入口的 `input_image` 条目，在请求需要翻译成另一种出口协议时
> 会被丢弃，只有文本被传递。同协议透传（Responses 调用方打到 Responses 供应商）不受
> 影响。另外三个入口的图片内容翻译正常。

### OpenAI Chat Completions

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-yr-your-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "smart",
    "messages": [{"role": "user", "content": "你好！"}],
    "stream": true
  }'
```

带工具调用：

```bash
curl http://localhost:8080/v1/chat/completions \
  -H "Authorization: Bearer sk-yr-your-key" \
  -H "Content-Type: application/json" \
  -d '{
    "model": "smart",
    "messages": [{"role": "user", "content": "上海天气怎么样？"}],
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
    "system": "回答尽量简洁。",
    "messages": [{"role": "user", "content": "你好！"}],
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
    "instructions": "回答尽量简洁。",
    "input": "你好！"
  }'
```

### Gemini

```bash
curl "http://localhost:8080/v1beta/models/smart:generateContent" \
  -H "x-goog-api-key: sk-yr-your-key" \
  -H "Content-Type: application/json" \
  -d '{
    "contents": [{"role": "user", "parts": [{"text": "你好！"}]}]
  }'

# 流式，Key 放在 query 里（Google 官方 SDK 就是这么发的）：
curl "http://localhost:8080/v1beta/models/smart:streamGenerateContent?key=sk-yr-your-key" \
  -H "Content-Type: application/json" \
  -d '{"contents": [{"role": "user", "parts": [{"text": "你好！"}]}]}'
```

### 模型发现

```bash
# OpenAI 格式
curl http://localhost:8080/v1/models -H "Authorization: Bearer sk-yr-your-key"

# Anthropic 格式——由 anthropic-version 头决定
curl http://localhost:8080/v1/models \
  -H "x-api-key: sk-yr-your-key" \
  -H "anthropic-version: 2023-06-01"
```

### 让现有 SDK 和工具直接指过来

因为入口就是真正的原生协议，官方 SDK 和 agent 工具只需改两个设置即可接入，
不需要任何适配层。

```python
# OpenAI Python SDK
from openai import OpenAI

client = OpenAI(base_url="http://localhost:8080/v1", api_key="sk-yr-your-key")
print(client.chat.completions.create(
    model="smart",
    messages=[{"role": "user", "content": "你好！"}],
).choices[0].message.content)
```

```python
# Anthropic Python SDK——同一个网关、同一个 Key
from anthropic import Anthropic

client = Anthropic(base_url="http://localhost:8080", api_key="sk-yr-your-key")
print(client.messages.create(
    model="smart",
    max_tokens=1024,
    messages=[{"role": "user", "content": "你好！"}],
).content[0].text)
```

```bash
# Claude Code——经 Yolorouter 转发到你配置的任意供应商
export ANTHROPIC_BASE_URL=http://localhost:8080
export ANTHROPIC_AUTH_TOKEN=sk-yr-your-key
claude
```

## 成本优化

两项功能默认关闭，在后台全局设置，并且可以按 API Key 单独覆盖。

**自定义系统提示词注入。** 不改客户端代码，就能给每个请求的系统提示追加统一规则。
注入按调用方自己的协议形态追加到已有的系统块上（OpenAI 的 `messages`、Anthropic 的
`system`、Responses 的 `instructions`、Gemini 的 `systemInstruction`），请求里原本没有
系统块时则新建一个。注入过程是确定性的，因此多次请求得到的系统内容字节一致，仍然能
命中上游的 prompt 缓存。请求体格式异常时会原样透传，而不是被静默改写。

**输入压缩。** 编码类 agent 会回传大量高度冗余的工具输出。Yolorouter 会识别请求中每个
内容块的类型，只去掉噪声、保留信号：

| 识别到的内容 | 去掉的部分 |
| --- | --- |
| `go test` / 构建日志 | 通过用例的模板行（`=== RUN`、`--- PASS`、`=== CONT`）——失败、跳过、panic、堆栈和汇总行全部保留，围栏代码块原样输出 |
| git diff | `index abc..def` blob 哈希头和 ANSI 转义——hunk 绝不截断 |
| grep / ripgrep 结果 | 连续重复的匹配行折叠成一行加重复次数；每一条不同的 `path:line:match` 都保留 |
| 普通日志 | ANSI 转义、连续重复行、连续空行 |

压缩不会碰对话尾部的活跃编辑区，并且只有压完确实更短时才替换。后台会展示按时间的
token 节省曲线，并在每条请求日志里标明哪些块被压缩了、没压的原因是什么。

缓存读 / 缓存写 token 在仪表盘、分析和成本页里全程单独计量和计价，所以 prompt 缓存
省下多少是一个能看到的数字，不是感觉。

## 配置

配置位于 `configs/config.yaml`，首次运行自动生成，通常无需手改。

```yaml
server:
  port: 8080
database:
  driver: sqlite            # sqlite | postgres
  sqlite_path: ../data/yolorouter.db
  # driver 为 postgres 时需要 host/port/user/password/dbname/sslmode
log:
  level: info
security:
  provider_master_key: ""   # base64 AES-256 密钥；留空时自动生成
  allow_private_upstreams: false  # 允许回环/内网上游（本地 Ollama、vLLM 等）
update:
  enabled: true             # 设为 false 可关闭更新检查 API 与 CLI
  github_repo: ""           # 更新检查的 "owner/repo" 覆盖项
  github_proxy: ""          # 例如 https://gh.yolorouter.com/ ，让更新走加速通道
gateway:                    # 上游转发超时；整段可省略
  connect_timeout: 5s       # TCP 建连
  header_timeout: 600s      # 请求发出 -> 响应头
  first_byte_timeout: 600s  # 响应头 -> 首个响应块（"思考"阶段的空档）
  body_idle_timeout: 60s    # 两个流式块之间的最大间隔
  attempt_timeout: 20m      # 单个候选上单个 Key 的硬上限
  request_timeout: 30m      # 跨所有 failover 候选的总预算
  tls_handshake_timeout: 10s
```

几点值得注意：

- `sqlite_path` 的相对路径按**配置文件自身所在目录**解析，不是进程 cwd。
- 若配置文件已存在，`provider_master_key` 必须是真实密钥——只有在"首次生成"路径下才会自动填充。
- 手工复制来的配置文件必须 `chmod 600`，否则会被拒绝加载。
- `allow_private_upstreams` 是为了让你把供应商指向本地 Ollama / vLLM / LM Studio。默认关闭是 SSRF 防护——公网暴露或多租户部署下绝对不要打开。
- 超时次序在启动时校验：`header_timeout` 和 `first_byte_timeout` 必须 ≤ `attempt_timeout`，且 `attempt_timeout` < `request_timeout`。

完整带注释的参考见 [`configs/config.example.yaml`](configs/config.example.yaml)。

### CLI

所有子命令都支持 `--config <path>`。

```bash
./yolorouter serve            # 启动 HTTP 服务与后台任务管理器
./yolorouter stop             # 停止正在运行的服务
./yolorouter update           # 自更新到最新的 GitHub 发布版本
./yolorouter db:migrate       # 执行待应用的迁移
./yolorouter db:status        # 查看当前迁移版本
./yolorouter db:rollback [v]  # 回滚一个迁移，或回滚到版本 v
./yolorouter db:backup --output-dir backups
./yolorouter db:reset         # 删除所有表并重新迁移（危险）；仅开发构建可用，
                              # 发布二进制中已禁用
./yolorouter --version
./yolorouter --help
```

## 从源码构建

依赖：**Go 1.25.7+** 与 **Node.js 22.12+**。

```bash
# 仅后端——提供占位页而非后台
make build          # -> ./bin/yolorouter

# 内嵌后台的完整二进制
make build-embed    # -> ./bin/yolorouter（构建并嵌入前端）

# 交叉编译（含内嵌前端）
make build-macos          # -> ./bin/yolorouter-darwin-{amd64,arm64}
make build-windows        # -> ./bin/yolorouter-windows-{amd64,arm64}.exe

# 快速编译检查（仅 windows，不构建前端、不产出二进制）
make build-windows-check
```

## 开发

```bash
./scripts/dev.sh              # 重建前后端、迁移、（重）启动
./scripts/dev.sh --backend    # 仅后端
./scripts/dev.sh --frontend   # 仅前端
./scripts/dev.sh --help       # 全部模式 + 环境变量（YOLO_LANG、NO_COLOR）

make test                     # go test ./...
make test-embed               # 带内嵌前端标签的测试
make vet                      # go vet（plain + -tags release）
```

完整流程与代码规范见 [CONTRIBUTING.md](CONTRIBUTING.md)。

## 架构

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

图中从上到下：四种协议入口 → 认证/限流/预算 → 协议协商与 IR 翻译 → 模型别名与供应商
候选 → Key 轮换与 failover → 输入压缩与日志；管理后台为内嵌 Vue 应用，数据落在
SQLite 或 PostgreSQL。

- **后端** — Go（[Gin](https://gin-gonic.com/) + [GORM](https://gorm.io/)），迁移用 [goose](https://github.com/pressly/goose)。分层 handler → service → repository，网关转发与协议编解码各自独立成包。
- **协议层** — 一套中间表示（IR）加每种协议一组编解码器（请求解码、请求编码、响应解码、响应编码，外加各自一对流式解码/编码器）。同协议请求完全绕过 IR。
- **前端** — Vue 3 + TypeScript + [naive-ui](https://www.naiveui.com/)，Vite 构建，经 `go:embed` 嵌入二进制。
- **存储** — SQLite（纯 Go、零配置）或 PostgreSQL。上游 Key 以 AES-256 静态加密存储。

## 贡献

欢迎提 Issue 和 PR。请先阅读 [CONTRIBUTING.md](CONTRIBUTING.md) 与
[行为准则](CODE_OF_CONDUCT.md)。报告安全问题见 [SECURITY.md](SECURITY.md)。

## 许可证

基于 [Apache License 2.0](LICENSE) 授权。
