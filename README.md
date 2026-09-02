# wb2api — Tencent CodeBuddy/WorkBuddy 非官方反代

> **UNOFFICIAL / 非官方项目。** 本项目与腾讯无关，未获任何官方授权。
> 使用前提：**你必须拥有合法的 CodeBuddy / WorkBuddy 订阅账号**，且仅用于
> **个人学习研究**。本项目将你的订阅额度包装成 OpenAI 兼容 API，供
> OpenCode / Claude Code / Codex CLI 等 agent 客户端复用。
> 请遵守腾讯服务条款，勿用于商业用途或转售。

## 功能

- 包装 `copilot.tencent.com` 订阅额度为标准 OpenAI 兼容 API（流式 / 非流式）
- 登录（扫码/浏览器）+ token 自动刷新，多账号轮转
- **prompt cache 攻关**：注入官方客户端 request-id/trace 头族，让
  `prompt_cache_hit_tokens` 从恒 0 变为真实命中（实测 0 → 512/3200，见下）
- 官方模型表（hy4-preview / hy3 / hy3-x / glm-5.3 / kimi-k3-1 等 17 个）
- 两种形态：独立 HTTP 服务（`cmd/server`）与 CLIProxyAPI 插件（`cmd/plugin`）

## 架构

```
core/           协议转换、OAuth、多账号轮转、SSE、会话/缓存字段注入（纯标准库，可离线单测）
cmd/server      独立 OpenAI 兼容服务（开发期验证用）
cmd/plugin      CLIProxyAPI (CPA) 插件薄壳（-buildmode=c-shared）
tools/capture   一键抓包工具（mitmproxy + addon）
specs/          抓包与协议事实记录（最高权威）
notes/          读码笔记
```

## 快速开始（独立服务）

```bash
go run ./cmd/server                      # 默认 127.0.0.1:8787
# 环境变量: WB2API_ADDR / WB2API_AUTH_DIR(~/.wb2api) / WB2API_STRATEGY

# 登录（浏览器/扫码）
curl -X POST http://127.0.0.1:8787/v1/auth/login
# → {authUrl, state}，打开 authUrl 完成授权后：
curl "http://127.0.0.1:8787/v1/auth/poll?state=<state>"

# 对话
curl -N http://127.0.0.1:8787/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"hy3","stream":true,"messages":[{"role":"user","content":"hi"}]}'
```

## CPA 插件（推荐形态）

```bash
# 需 Go 1.26+ 与 gcc
GO=/path/to/go1.26/bin/go
cd cmd/plugin && $GO build -buildmode=c-shared -o workbuddy.so .

# 安装到宿主
mkdir -p <host>/plugins/linux/amd64 && cp workbuddy.so <host>/plugins/linux/amd64/
```

宿主 `config.yaml` 要点：

```yaml
plugins:
  enabled: true
  dir: "/abs/path/to/plugins"          # 绝对路径
  configs:
    workbuddy:
      enabled: true
auth-dir: "/abs/path/to/auth-dir"      # 放 workbuddy.json（OAuth 登录产出）
```

把 OpenCode / Claude Code / Codex CLI 的端点指向宿主即可（OpenAI 兼容 `/v1`）。

## prompt cache 攻关结论（2026-09-02 抓包实证）

- **根因**：`prompt_cache_hit_tokens=0` 是因为请求缺少官方客户端的
  request-id 头族（`X-Request-ID` / `X-Conversation-Request-ID` /
  `X-Conversation-Message-ID` + traceparent/b3），它们触发上游启用共享缓存。
- **实测**：同一请求体，无头族恒 0；注入后命中 320→512→3200。
- **机制**：上游是**全局共享内容前缀缓存**（官方系统提示词对全体用户相同 →
  全量命中；用户独有内容永不缓存），且有 TTL。
- 完整实验记录见 `specs/capture-2026-09-02.md`。

**已知限制（如实声明）**：缓存命中依赖内容在全局的共享度与 TTL，非每次请求
都命中；无法让"每个用户自己的长对话内容"进入缓存（上游对独有内容不缓存，
对官方客户端同样如此）。

## 开发

```bash
go build ./... && go vet ./...   # core + cmd/server
go test ./...                    # core 单测（mock 上游，不依赖网络）
```

插件构建需 go1.26（宿主 SDK 要求），见上文。参考目录规则与协议事实优先级见
`AGENTS.md`。

## 合规

- 本项目以 **MIT** 许可证发布，详见 `NOTICE.md`（第三方署名）。
- 仅供学习研究，需用户自有合法订阅；请勿违反腾讯服务条款。
- 任何情况下不提交 third_party/ 目录到本仓库。
