# wb2api — Tencent CodeBuddy/WorkBuddy 非官方反代

简单说：登录你自己的 CodeBuddy / WorkBuddy 订阅后，wb2api 会把它包装成一个
OpenAI 兼容的本地 API 服务，让 OpenCode、Codex CLI 等 AI 工具直接使用这个订阅。

> **非官方项目，与腾讯无关。** 需使用自有合法订阅，仅限个人学习研究，请遵守腾讯
> 服务条款。详见文末[合规](#合规)。

## 快速开始（独立服务）

**1. 启动服务**

```bash
go run ./cmd/server        # 默认监听 http://127.0.0.1:8787
```

可选环境变量：`WB2API_ADDR` 监听地址；`WB2API_AUTH_DIR` 凭据目录（默认 `~/.wb2api`）；
`WB2API_STRATEGY` 多账号轮转策略 `roundrobin` / `fillfirst`。

**2. 登录 CodeBuddy 账号**（每个账号执行一次）

```bash
# ① 发起登录，返回一个授权链接
curl -X POST http://127.0.0.1:8787/v1/auth/login
# → {"authUrl":"https://...","state":"..."}

# ② 浏览器打开上面的 authUrl，扫码或网页授权完成后：
curl "http://127.0.0.1:8787/v1/auth/poll?state=<state>"
# → {"pending":false,"nickname":"你的账号名"} 即为登录成功
```

凭据自动保存到 `~/.wb2api/workbuddy.json`，重启服务后自动加载；重复登录不同账号，
请求会按策略轮转。

**3. 开始对话**

```bash
curl -N http://127.0.0.1:8787/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"hy3","stream":true,"messages":[{"role":"user","content":"hi"}]}'
```

模型名用上游模型 id（`hy3`、`glm-5.3` 等，完整列表见 `GET /v1/models`）。

**4. 接入你的 AI 工具**

- **OpenCode / Codex CLI / 任意 OpenAI SDK**：把 baseURL / API 端点指向
  `http://127.0.0.1:8787/v1` 即可。
- **Claude Code**：它只支持 Anthropic 协议，不能直连本服务，请使用下方
  插件形态（CLIProxyAPI 宿主负责转换）。

## CPA 插件形态（让 Claude Code 等也能用）

CLIProxyAPI 宿主负责 OpenAI ↔ Anthropic 等协议转换，本插件在其中把 WorkBuddy
注册为一个可用模型源。

```bash
cd cmd/plugin && $GO build -buildmode=c-shared -o workbuddy.so .   # 需 Go 1.26+ 与 gcc
cp workbuddy.so <宿主目录>/plugins/linux/amd64/
```

在宿主 `config.yaml` 中启用 workbuddy 插件并设置 `auth-dir`，登录走宿主自带流程，
然后把 Claude Code / OpenCode / Codex CLI 的端点指向宿主即可。

## 合规

- 非官方项目：与腾讯无关，未获授权、无官方背书；请遵守腾讯服务条款与当地法律。
- 使用前提：必须拥有合法订阅；仅限个人学习研究，勿用于商业用途、转售或公开服务。
- 账号风控等风险由使用者自行承担。
- 以 **MIT** 发布，详见 `LICENSE` 与 `NOTICE.md`；仓库不包含第三方代码。
