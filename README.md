# wb2api — Tencent CodeBuddy/WorkBuddy 非官方反代

> **UNOFFICIAL / 非官方项目**：本项目与腾讯无关，未经腾讯授权。使用本项目即代表你
> 同意：① 拥有合法的 CodeBuddy / WorkBuddy 订阅账号；② 仅将其用于**个人学习研究**，
> 不用于商业用途、转售或任何形式的公开服务；③ 自行遵守腾讯服务条款。详细说明见
> 文末[合规声明](#合规声明)。

## 这是什么

把腾讯 CodeBuddy / WorkBuddy（`copilot.tencent.com`）订阅额度包装成 OpenAI 兼容 API，
让 Claude Code / OpenCode / Codex CLI 等 agent 客户端直接复用你的订阅。

- 包装为标准 OpenAI 兼容 API（流式 / 非流式，SSE）
- OAuth 登录（扫码 / 浏览器）+ token 自动刷新 + 多账号轮转
- 注入官方客户端的 request-id / trace 头族，激活上游共享 prompt cache
- 官方模型表：hy4-preview / hy3 / hy3-x / glm-5.3* / kimi-k3* / deepseek-v4* 等
- 两种形态：独立 HTTP 服务（`cmd/server`）与 CLIProxyAPI 插件（`cmd/plugin`）

## 目录结构

```
core/          协议转换、OAuth、多账号轮转、SSE、缓存字段注入（纯标准库，可离线单测）
cmd/server     独立 OpenAI 兼容服务（HTTP，适合先跑通验证）
cmd/plugin     CLIProxyAPI (CPA) 插件（-buildmode=c-shared，首发发行形态）
tools/capture  一键抓包工具（mitmproxy + addon，仅供协议研究）
specs/         抓包与协议事实记录
notes/         读码笔记
```

## 快速开始（独立服务）

### 1. 启动

```bash
# 环境 Go 1.22 即可
go run ./cmd/server
# 默认监听 127.0.0.1:8787
```

可用环境变量：

| 变量 | 默认值 | 说明 |
|---|---|---|
| `WB2API_ADDR` | `127.0.0.1:8787` | 监听地址 |
| `WB2API_AUTH_DIR` | `~/.wb2api` | 登录凭据存放目录 |
| `WB2API_STRATEGY` | `roundrobin` | 多账号轮转策略：`roundrobin` / `fillfirst` |

### 2. 登录（每个账号执行一次）

```bash
# 发起登录，得到授权链接与 state
curl -X POST http://127.0.0.1:8787/v1/auth/login
# → {"authUrl":"https://...","state":"..."}

# 浏览器打开 authUrl，扫码 / 网页授权完成后：
curl "http://127.0.0.1:8787/v1/auth/poll?state=<state>"
# → {"pending":false,"nickname":"xxx"} 即登录成功
# 凭据自动保存为 ~/.wb2api/workbuddy.json
```

> 提示：换浏览器重复登录即可添加多账号；重启服务后凭据从 `WB2API_AUTH_DIR`
> 自动加载，无需重新登录。

### 3. 对话

```bash
# 流式
curl -N http://127.0.0.1:8787/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"hy3","stream":true,"messages":[{"role":"user","content":"hi"}]}'

# 非流式
curl http://127.0.0.1:8787/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"hy3","messages":[{"role":"user","content":"hi"}]}'

# 可用模型
curl http://127.0.0.1:8787/v1/models
```

### 4. 让 agent 客户端指向本服务

服务本身只暴露 **OpenAI 兼容**的 `/v1` 接口，因此：

- **原生 OpenAI 客户端**（OpenCode、Codex CLI、任意 OpenAI SDK / curl）：
  把 baseURL / 端点指向 `http://127.0.0.1:8787/v1` 即可，模型名用上方列表中的
  上游模型 id（如 `hy3`、`glm-5.3`）。
- **Claude 系客户端（如 Claude Code）**：其原生协议是 Anthropic 格式，不能直接
  指向本服务；请在两者之间加一层 OpenAI ↔ Anthropic 转换（如
  `router-for-me/CLIProxyAPI` 宿主——即下方的插件形态——或 claude-code-router
  等第三方转换器）。

> 各客户端 provider 配置字段随版本略有差异，请以各客户端文档为准；核心只需一个
> OpenAI 兼容 baseURL。

## CPA 插件（CLIProxyAPI 形态，推荐）

> CLIProxyAPI（`router-for-me/CLIProxyAPI`，MIT）是把 Claude Code / Codex /
> Antigravity / Grok 等 CLI 订阅包装成标准 API 的宿主平台；本插件在其中注册
> WorkBuddy 为 provider。

### 构建

```bash
# 需 Go 1.26+（宿主 SDK 要求）与 gcc
GO=/home/shane/go1.26/bin/go        # 替换为你的 go1.26 路径
cd cmd/plugin && $GO build -buildmode=c-shared -o workbuddy.so .
```

### 安装与配置

```bash
mkdir -p <host>/plugins/linux/amd64 && cp workbuddy.so <host>/plugins/linux/amd64/
```

宿主 `config.yaml`：

```yaml
plugins:
  enabled: true
  dir: "/abs/path/to/plugins"      # 绝对路径
  configs:
    workbuddy:
      enabled: true
auth-dir: "/abs/path/to/auth-dir"  # 放 workbuddy.json
```

首次使用通过宿主自带的登录流程（`/v1/auth/login` 等）完成 OAuth 登录，凭据由宿主
写入 `auth-dir`。之后把 Claude Code / OpenCode / Codex CLI 的端点指向宿主即可。

## prompt cache 说明（2026-09-02 抓包实证）

- **根因**：第三方流量 `prompt_cache_hit_tokens=0`，是因为请求缺少官方客户端的
  request-id 头族（`X-Request-ID` / `X-Conversation-Request-ID` /
  `X-Conversation-Message-ID` + traceparent/b3），这些字段触发上游启用共享缓存。
- **实测**：同一请求体，无头族恒 0；注入后命中 320 → 512 → 3200。
- **机制**：上游是**全局共享内容前缀缓存**（官方系统提示词对全体用户相同 → 全量
  命中；用户独有内容不缓存），且有 TTL。
- 完整实验记录见 `specs/capture-2026-09-02.md`。

**已知限制（如实声明）**：缓存命中取决于内容在全局的共享度与 TTL，不是每次请求
都命中；"每个用户自己的长对话"无法进入上游缓存（上游对独有内容不缓存，对官方
客户端同样如此）。

## 开发

```bash
go build ./... && go vet ./...   # core + cmd/server
go test ./...                    # core 单测（mock 上游，不依赖网络）
```

插件构建需 go1.26，见上文。参考目录规则与协议事实优先级见 `AGENTS.md`。

## 合规声明

- 本项目是**非官方**的独立实现：与腾讯、CodeBuddy / WorkBuddy 官方无任何关联，
  未获授权、无官方背书。
- 使用前提：**必须拥有合法的 CodeBuddy / WorkBuddy 订阅**；本项目不提供、不绕过
  任何付费墙或鉴权，仅将你自有订阅的额度暴露为 API。
- 仅供**学习研究**（协议逆向、互操作实验）使用；请勿用于商业用途、转售额度、
  或提供给组织外 / 公众使用。
- 使用本项目产生的账号风险（如风控、封禁）由使用者自行承担；请遵守腾讯服务条款
  与当地法律。
- 本项目以 **MIT** 许可证发布，详见 `LICENSE` 与 `NOTICE.md`（第三方署名）。
- 仓库不包含任何第三方代码；`third_party/` 参考目录（见 `AGENTS.md`）永不入库。
