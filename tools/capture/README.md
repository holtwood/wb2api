# wb2api 一键抓包工具包

抓官方 CodeBuddy CLI 与 `copilot.tencent.com` 之间的流量，用于确认
prompt cache 会话/缓存标识字段（Phase 1）。

## 环境

- 已在 WSL 中安装 mitmproxy（`~/.local/bin/mitmdump`）
- 官方客户端：`codebuddy`（Node CLI，`@tencent-ai/codebuddy-code` v2.143.0）
- 出网：复用当前 shell 的本地代理（`127.0.0.1:10808`），mitmproxy 以
  **upstream 模式** 经它出网（本机无直连外网）

## 用法（总共 3 步）

### 第 1 步：启动抓包

```bash
cd tools/capture
./capture.sh start
```

自动完成：安装检查 → 生成 CA 证书（`~/.wb2api-mitm/`）→ 启动 mitmdump（端口 8080）
→ 打印出网代理。抓包文件写入 `tools/capture/captures/`（每个 flow 一个可读 JSON）。

### 第 2 步：在另一个终端里用代理跑官方 CLI

按 `capture.sh start` 打印的提示执行（核心是下面三行）：

```bash
export NODE_EXTRA_CA_CERTS=$HOME/.wb2api-mitm/mitmproxy-ca-cert.pem
export HTTPS_PROXY=http://127.0.0.1:8080
unset NO_PROXY
codebuddy
```

操作要点（对应抓包清单 W1/W2/W3/W8/W9）：
1. 触发一次登录 / token 刷新（如果还没登录）
2. 发起 2~3 轮正常对话，其中一轮带工具调用
3. 对同一段固定前缀重复发 4 次（缓存测试），最后一轮只改 user 消息
4. 观察每轮响应的 usage 字段（`prompt_cache_hit_tokens` / `prompt_cache_miss_tokens`）

### 第 3 步：停止并交付

```bash
./capture.sh stop
```

把 `captures/` 目录下的 JSON 交给分析（我会逐条核对 W1-W14 清单）。

## 故障排查

| 现象 | 处理 |
|---|---|
| `codebuddy` 报证书错误 | 确认 `NODE_EXTRA_CA_CERTS` 指向 `~/.wb2api-mitm/mitmproxy-ca-cert.pem`；若仍失败可能是官方客户端 TLS 钉扎，改用别的方式（见下） |
| 抓包目录为空 | 确认 `codebuddy` 的流量确实走了 `HTTPS_PROXY=127.0.0.1:8080`（`env | grep -i proxy` 检查） |
| `mitmdump` 启动失败 | 看日志 `~/.wb2api-mitm/mitmdump.log`；多半是出网代理配置问题，用 `WB2API_UPSTREAM_PROXY=... ./capture.sh start` 覆盖 |
| 想改端口/输出目录 | `WB2API_MITM_PORT=8888` / `WB2API_CAPTURE_DIR=/path ./capture.sh start` |

## 若官方客户端 TLS 钉扎（暂未确认）

优先排查 Node 侧 `NODE_EXTRA_CA_CERTS` 是否生效。若确实钉扎，备选：
1. 让官方 CLI 走已存在的 `127.0.0.1:10808` 代理，在 10808 所在程序上做分流抓包（需代理软件支持）
2. 抓本机 HTTPS 出口流量（tcpdump + SSLKEYLOGFILE，若 Node 支持 `NODE_OPTIONS=--tls-keylog`）

## 已实测确认的事实（2026-09-02 探针）

- `GET /v2/plugin/auth/token?state=<任意>` 在登录未完成时返回
  `{"code":11217,"msg":"11217:login ing...","requestId":"<uuid>"}`，HTTP 200
  → 实证：code 11217 = login ing；信封含 `requestId`
- 上游响应头含 `traceid`、`x-request-id`、`x-waf-uuid`、`server: TencentEdgeOne`、
  `eo-cache-status`、`eo-log-uuid` → 缓存/会话标识字段候选
- 官方客户端 v2.143.0 **无 TLS 钉扎**：`NODE_EXTRA_CA_CERTS` 即可 MITM
- headless 驱动：`codebuddy -p "..." --model hy3 --tools ""`

## 完整抓包结论（2026-09-02，详见 specs/capture-2026-09-02.md）

缓存攻关核心：**request-id 头族（X-Request-ID + X-Conversation-Request-ID +
X-Conversation-Message-ID + traceparent/b3）是上游 prompt cache 的激活开关**。
无头恒 `prompt_cache_hit_tokens=0`，带头可命中共享内容（实测 0→320/512/3200）。
缓存是全局共享内容前缀缓存（TTL，独有内容永不缓存）。wb2api core 已内置该头族注入
（`core/trace.go`），实测端到端 hit=512 与官方客户端一致。

> ⚠️ 抓包文件含实时 access token，用完即删（已 gitignore）；本目录不保留原始流。
