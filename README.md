# wb2api — Tencent CodeBuddy/WorkBuddy 非官方反代

将腾讯 CodeBuddy / WorkBuddy（`copilot.tencent.com`）订阅额度包装成 OpenAI 兼容
API，供 OpenCode、Codex CLI 等 agent 客户端复用，并注入官方客户端的 request-id /
trace 头族以激活上游共享 prompt cache。

> **非官方项目，与腾讯无关。** 需使用自有合法订阅，仅限个人学习研究，请遵守腾讯
> 服务条款。详见文末[合规](#合规)。

## 快速开始（独立服务）

```bash
go run ./cmd/server                  # 默认监听 127.0.0.1:8787
# 可选：WB2API_ADDR / WB2API_AUTH_DIR(默认 ~/.wb2api) / WB2API_STRATEGY(roundrobin|fillfirst)
```

登录（每个账号执行一次，浏览器扫码/授权）：

```bash
curl -X POST http://127.0.0.1:8787/v1/auth/login         # → {authUrl, state}
curl "http://127.0.0.1:8787/v1/auth/poll?state=<state>"  # 完成授权后轮询，成功即保存
```

凭据自动保存为 `~/.wb2api/workbuddy.json`，重启自动加载；重复登录即可添加多账号轮转。

对话（OpenAI 兼容，模型列表见 `GET /v1/models`）：

```bash
curl -N http://127.0.0.1:8787/v1/chat/completions \
  -H 'Content-Type: application/json' \
  -d '{"model":"hy3","stream":true,"messages":[{"role":"user","content":"hi"}]}'
```

接入客户端：baseURL 指向 `http://127.0.0.1:8787/v1` 即可（OpenCode / Codex CLI /
任意 OpenAI SDK）。Claude Code 等 Anthropic 原生客户端需经转换层——见下节插件形态。

## CPA 插件形态（CLIProxyAPI 宿主，含 Anthropic 转换）

```bash
cd cmd/plugin && $GO build -buildmode=c-shared -o workbuddy.so .  # 需 Go 1.26+ 与 gcc
cp workbuddy.so <host>/plugins/linux/amd64/
```

在宿主 `config.yaml` 中启用 workbuddy 插件并设置 `auth-dir`，登录走宿主自带流程，
之后把各 CLI 端点指向宿主即可。

## 合规

- 非官方项目：与腾讯无关，未获授权、无官方背书，请遵守腾讯服务条款与当地法律。
- 使用前提：必须拥有合法订阅；仅限个人学习研究，勿用于商业用途、转售或公开服务。
- 账号风控等风险由使用者自行承担。
- 以 **MIT** 发布，详见 `LICENSE` 与 `NOTICE.md`；仓库不包含第三方代码。
