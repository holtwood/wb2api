# wb2api — Tencent WorkBuddy/CodeBuddy 反代（CPA 插件形态优先）

> 本文件是 Claude Code 的项目宪章。开始任何工作前必须先读完本文件，并严格遵守其中的参考目录规则与架构纪律。

## 1. 项目背景与目标

腾讯 WorkBuddy/CodeBuddy（上游端点 `copilot.tencent.com`）是订阅制 AI 编程服务，内部可调用混元系 hy3-preview / hy4-preview 以及 GLM、Kimi、DeepSeek 等模型。本项目将其包装成 OpenAI / Anthropic 兼容 API，让 Claude Code、OpenCode、Codex CLI 等 agent 客户端复用订阅额度。

- 首发形态：CLIProxyAPI（下称 CPA）插件，Go 语言，`-buildmode=c-shared` 动态库
- 架构上保留独立 HTTP 服务能力（见第 3 节），插件只是第一个发行形态
- 核心差异化目标：**修复现有实现 prompt cache 永不命中的问题**——通过 MITM 抓包找出官方客户端的会话/缓存标识字段并注入，让 `prompt_cache_hit_tokens` 真正产生命中

## 2. 生态上下文（为什么有这个项目）

- `router-for-me/CLIProxyAPI`（MIT，约 5 万 star）：宿主平台，把 Claude Code / Codex / Antigravity / Grok 等 CLI 订阅包装成标准 API，本项目以插件形式接入
- `Sliverkiss/cpa-plugin`（无许可证，已弃坑）：最早的 WorkBuddy CPA 插件，代码为 AI 生成、约 19k 行胶水代码，作者已公告停更并转向独立反代
- `lovingfish/workbuddy-cliproxy`（MIT）：对前者的 clean-room 重写，单 main.go，维护活跃。其 issue #4 实证了 prompt cache 问题：逐字节相同的请求也 `prompt_cache_hit_tokens=0`，原版二进制同样复现——基本确认上游对非官方流量不启用缓存，疑似需要官方客户端特有的会话字段激活
- `Sliverkiss/workbuddy2api`（无许可证）：原作者的独立反代继任者，包含最新 bugfix 的行为参考
- `origin652/cpa-plugin-key-policy`：CPA 插件能力上限参考（插件注册 HTTP 路由、管理 UI、下游 key 计费）

## 3. 架构纪律（不可妥协）

```
wb2api/
├── core/          # 协议转换、OAuth 登录与 token 刷新、多账号轮转、会话/缓存字段注入
│                  # 禁止 import 任何 CLIProxyAPI 的包；纯 Go package，可离线单测
├── cmd/
│   ├── plugin/    # CPA 插件入口薄壳，只做注册/适配/ABI 胶水，不得含协议逻辑（依赖 go1.26+CLIProxyAPI）
│   └── server/    # 独立 HTTP 服务（OpenAI 兼容 /v1），开发期验证用，纯标准库
├── tools/capture/ # 一键抓包工具包（mitmproxy addon + 脚本 + 说明）
├── specs/         # MITM 抓包记录与协议规格文档（协议事实的最高权威，Phase 1 产出）
└── notes/         # 读码笔记
```

### 构建与测试（Phase 2/3 现状）

```bash
go build ./... && go vet ./...     # 根模块（core + cmd/server）构建 + 静态检查
go test ./...                      # core 单测（httptest mock 上游，不依赖网络）
go run ./cmd/server                # 起本地 OpenAI 兼容服务，默认 127.0.0.1:8787
                                   # 环境变量: WB2API_ADDR / WB2API_AUTH_DIR(~/.wb2api)
                                   #           WB2API_STRATEGY(roundrobin|fillfirst)

# CPA 插件（cmd/plugin 为嵌套模块，需 go 1.26 + gcc + cgo）
GO=/home/shane/go1.26/bin/go
$GO build -buildmode=c-shared -o workbuddy.so .   # 在 cmd/plugin/ 下执行
```

- 环境 Go 1.22；`cmd/plugin` 需要 go 1.26+（宿主 SDK 要求），独立 go.mod（`replace wb2api => ../../`）
- 插件 ABI 自检：`/tmp/plugin-test/main.c` 模式（dlopen + cliproxy_plugin_init + 调用 register/identifier）
- 抓包：`tools/capture/capture.sh start` → 用代理跑官方 `codebuddy` CLI → `stop`，详见 `tools/capture/README.md`

## 4. 参考目录规则（../third_party/，全部只读，禁止复制进本仓库）

| 目录 | 许可证 | 允许 | 禁止 |
|---|---|---|---|
| third_party/CLIProxyAPI | MIT | 插件 ABI 的唯一权威来源，可自由参考仿写 | — |
| third_party/workbuddy-cliproxy | MIT | WorkBuddy 协议转换参考 | 大段引用需在 NOTICE 保留 MIT 署名 |
| third_party/workbuddy2api | 无许可证 | 理解其行为后用自己的代码重新实现 | 复制或逐行改写其代码 |
| third_party/cpa-plugin | 无许可证 | 只读 *.md、docs/、analysis/、*.json 等文档与数据文件 | **打开或仿写其 .go 源码** |
| third_party/cpa-plugin-key-policy | 未确认 | 仅学习"插件注册路由/管理面"的模式 | 复制代码 |

## 5. 协议事实优先级

1. specs/ 中我本人 MITM 抓包官方客户端的记录（最高优先级）
2. lovingfish/workbuddy-cliproxy 与 workbuddy2api 的行为观察
3. cpa-plugin 的文档文件

冲突时以抓包为准，并将差异记录到 specs/ 对应文档。

## 6. 已知协议基线（来自社区实证，需抓包复核）

- 上游端点：`https://copilot.tencent.com/v2/chat/completions`
- 客户端标识 headers：UA 形如 `CLI/2.143.0 CodeBuddy/2.143.0`（2026-09-02 实证，2.63.2 已过时），另有 X-Product、X-User-Id、X-Domain 等
- 已知 body 改写：强制 `stream:true`、`stream_options.include_usage`、Claude system 模板中性化、hy3 强制 `reasoning_effort=high`
- 凭据：OAuth 登录，本地 auth json 文件，支持多账号轮转
- 模型：hy4-preview / hy3 / hy3-x / glm-5.3* / kimi-k3-1 等（官方 --model 列表，见 specs）
- 缓存（2026-09-02 实证，见 specs/capture-2026-09-02.md）：`prompt_cache_hit_tokens=0` 的根因是**缺 request-id 头族**（X-Request-ID/X-Conversation-Request-ID/X-Conversation-Message-ID + traceparent/b3）。注入后共享内容可命中（0→320/512/3200）；缓存是**全局共享内容前缀缓存**，独有内容永不缓存，有 TTL；多账号轮询需按会话保持亲和

## 7. 开发路线图

- Phase 0 读码：通读 CLIProxyAPI 插件机制与 lovingfish 版 main.go → 产出 notes/abi-notes.md 与 notes/workbuddy-protocol-notes.md（不写实现代码）
- Phase 1 抓包：mitmproxy 抓官方客户端登录与多轮对话全流程 → specs/，重点寻找 session/conversation/cache 相关字段
- Phase 2 core：OAuth + 多账号轮转 + OpenAI/Anthropic 双向协议转换 + SSE 流式 + 工具调用
- Phase 3 插件薄壳：在 CPA 中注册 provider，跑通 Claude Code / OpenCode / Codex CLI
- Phase 4 缓存攻关：注入会话字段，用固定前缀连发脚本验证 `prompt_cache_hit_tokens > 0`
- Phase 5：cmd/server 骨架 + Dockerfile

## 8. 验收标准

- Claude Code 指向本插件端点可完成多轮对话与工具调用，流式输出正常，usage 字段完整透传
- 缓存测试：约 1200 token 固定前缀（system + 固定历史）连发 4 次，仅末条 user 消息变化，观察 usage 中 `prompt_cache_hit_tokens` / `prompt_cache_miss_tokens`；进一步发两次逐字节相同请求并确认落在同一账号
- core/ 单测不依赖网络与 CPA（mock 上游响应）
- 若最终证实上游不向第三方流量开放缓存：在 README 记录为已知限制，不得谎报

## 9. 合规与发布纪律

- 本项目以 MIT 发布；README 必须含 unofficial 声明、"需用户自有合法订阅"、"仅供学习研究"
- 任何情况下不提交 third_party/ 的文件到本仓库 git 历史
- 不复制无许可证仓库的代码；与其行为一致必须通过独立实现达成
