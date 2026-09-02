# workbuddy-protocol-notes.md — WorkBuddy/CodeBuddy 上游协议读码笔记

> Phase 0 产物。来源：`../third_party/workbuddy-cliproxy/main.go`（MIT，单文件 1162 行，module `github.com/lovingfish/workbuddy-cliproxy`，go 1.26.0，唯一依赖 `github.com/router-for-me/CLIProxyAPI/v7 v7.2.30`，实际只 import `sdk/pluginabi` 与 `sdk/pluginapi`）。
> 标注约定：`[已确认]` = 直接读码核实（附 main.go 行号，简写 `Lxx`）；`[待抓包验证]` = 代码不足以证实、需 Phase 1 抓官方客户端确认的假设。
> 本项目按 MIT 参考实现，行为借鉴需在 NOTICE 保留署名；无许可证的 workbuddy2api/cpa-plugin 不复制其代码。

## 1. 常量与端点（L77-91）

```go
upstreamBase  = "https://copilot.tencent.com"                    // L80
clientUA      = "CLI/2.63.2 CodeBuddy/2.63.2"                    // L81
originReferer = "https://www.codebuddy.cn"                       // L82
endpointAuthState    = upstreamBase + "/v2/plugin/auth/state?platform=CLI"   // L84
endpointLoginAcct    = upstreamBase + "/v2/plugin/login/account?state="      // L85
endpointAuthToken    = upstreamBase + "/v2/plugin/auth/token?state="         // L86
endpointTokenRefresh = upstreamBase + "/v2/plugin/auth/token/refresh"        // L87
endpointChat         = upstreamBase + "/v2/chat/completions"                 // L88
```

| 端点 | 方法 | 用途 | 调用处 |
|---|---|---|---|
| `/v2/plugin/auth/state?platform=CLI` | POST（body `{}`） | 发起登录，返回 `{state, authUrl}` | L546 |
| `/v2/plugin/auth/token?state=<state>` | GET | 登录轮询（权威状态源），code 0 拿 token bundle | L589 |
| `/v2/plugin/login/account?state=<state>` | GET（Bearer） | 账号信息；在 openresty 网关后，登录未完成 401 | L609 |
| `/v2/plugin/auth/token/refresh` | POST（无 body） | token 刷新 | L650 |
| `/v2/chat/completions` | POST | 对话（流式/非流式均强制流式上行） | L688/746/807 |

- `[已确认]` 无独立模型列表端点——模型表本地硬编码（见 §8）。
- `[已确认]` 所有请求走 `doJSON`（L483-510），统一响应信封 `apiEnvelope{code,msg,data}`（L372-376），`code==0` 才算成功（L506-508）。
- `[已确认]` 登录 TTL：`loginTTL = 5 * time.Minute`（L90）；`authFileName = "workbuddy.json"`（L79）。

## 2. 请求 Headers

### 2.1 通用头 `commonHeaders`（L442-449）

```go
Content-Type:      application/json
Accept:            application/json, text/plain, */*
X-Requested-With:  XMLHttpRequest
Origin:            https://www.codebuddy.cn        // originReferer
Referer:           https://www.codebuddy.cn/
User-Agent:        CLI/2.63.2 CodeBuddy/2.63.2
```

### 2.2 chat 认证头 `backendHeaders`（L453-479，在 commonHeaders 基础上）

| Header | 值 | 空值约定 |
|---|---|---|
| `Authorization` | `Bearer <accessToken>` | 空则 `X-No-Authorization: 1` |
| `X-User-Id` | `<account.uid>` | 空则 `X-No-User-Id: 1` |
| `X-Enterprise-Id` | `<account.enterpriseId>` | 空则 `X-No-Enterprise-Id: 1` |
| `X-Refresh-Token` | `<refreshToken>` | 仅非空时发送 |
| `X-Domain` | `<domain>` | 空则 `X-No-Department-Info: 1` |
| `X-Product` | `SaaS`（固定） | — |

### 2.3 其他位置

- 账号端点：`Authorization: Bearer <accessToken>`（L605-608）
- 刷新端点：`X-Refresh-Token`、`X-Enterprise-Id`（条件）、`X-Auth-Refresh-Source: workbuddy`（L642-649）
- 发给 CPA 的流式响应头：`Content-Type: text/event-stream`、`Cache-Control: no-cache`、`X-Accel-Buffering: no`（`streamHeaders`，L757-763）

## 3. 请求 body 改写规则

### 3.1 强制 `stream:true` — `forceStreamBody`（L912-927）

- `[已确认]` 上游拒绝非流式（code 11101），任何入站请求都强制 `obj["stream"] = true`（L921）。优先用 `req.Payload`，空则回退 `req.OriginalRequest`（L914-916）。
- `[已确认]` 入口：`handleExecExecute` 先 `forceStreamBody` 再 `rewriteSystemForUpstream`（L687）；`handleExecStream` 只做 `rewriteSystemForUpstream`（L730）。

### 3.2 Claude system 模板中性化 — `rewriteSystemForUpstream`（L935-965）+ `rewriteContentField`（L970-994）+ `sanitizeBlockedTemplates`（L996-1004）

- `[已确认]` 腾讯内容审核把 Claude Code 两句固定 system 模板逐字拉黑，命中即"敏感内容"拒答。逐字精确改写：
  ```
  "You are Claude Code, Anthropic's official CLI for Claude."  →  "...official CLI tool for Claude."
  "Main branch (you will usually use this for PRs)"            →  "Default branch (you will usually use this for PRs)"
  ```
  （L997-1002）
- `[已确认]` 遍历所有 `messages`（L943-953）；`content` 支持纯字符串（L972-976）与 OpenAI 多模态数组（遍历 `part["text"]`，L977-991）。
- `[待抓包验证]` 腾讯审核词表会随版本变动，这是 cat-and-mouse（README 明言），抓包时注意官方客户端当前是否仍需要此类改写。

### 3.3 hy3 系强制最大思考 — `forceMaxThinking`（L1010-1020）

- `[已确认]` model 以 `hy3` 为前缀 且 `reasoning_effort != "high"` → 强制 `obj["reasoning_effort"] = "high"`（L1011-1018）。CodeBuddy 只认 `high` 档真正开深度思考，其余档（medium/low/max/xhigh/ultra）被忽略回退无思考（注释 L1006-1010）。
- `[待抓包验证]` 官方客户端对 hy3 的思考档位是否确实只有 high 生效。

### 3.4 流式 chunk 清洗 — `cleanChunkJSON`（L869-894）+ `isEmptyValue`（L896-908）

- `[已确认]` 从 `choices[].delta` 删掉空值字段（`null`/`""`/`[]`/`{}`），避免严格客户端被 `{"function_call":null,"tool_calls":[]}` 卡住（L867-868）。

### 3.5 与 AGENTS.md 基线的差异（重要）

- `[已确认]` **本实现没有 tool_choice 归一化**，没有模型名→上游内部名映射（模型名原样透传，见 §8）。
- `[待抓包验证]` AGENTS.md 基线提到"tool_choice 归一化"——需抓官方客户端确认上游对 tool_choice 的期望形态，判断 wb2api 是否要做。

## 4. OAuth 流程（扫码/浏览器登录 + state 关联 cookie jar）

- `[已确认]` 类型：**非**设备码、**非** PKCE。无 `client_id`、无 `scopes`、无 `grant_type`（grep 无命中）。登录 = 扫码/浏览器打开 `authUrl` + 独立 cookie jar + 轮询。

### 时序

1. **startLogin**（`handleStartLogin`，L544-562）：新建独立 cookie jar 的 client（`newLoginClient`，L433-440）→ POST `endpointAuthState`（body `{}`）→ 得 `{state, authUrl}`（`authStateData`，L392-395）→ `loginStates` sync.Map 存 `state → loginCtx{client, expires}`，TTL 5 分钟 → 返回登录 URL + state 给 CPA 面板。
2. **pollLogin**（`handlePollLogin`，L564-631）：宿主驱动轮询节奏，单次 RPC 打一次。先 GET `endpointAuthToken+state`（L589）——登录未完成返回 code 11217（"login ing"，注释 L584-588），任何错误/空 token 均视为 Pending（L590-602）；完成 code 0 拿 token bundle。**拿到 Bearer 后才拉 account**（L604-611），因 `login/account` 在 openresty 网关后登录完成前 401。组装 `storedAuth` 后从 map 删 state，返回 Success。
3. **refreshAuth**（`handleRefreshAuth`，L633-670）：POST `endpointTokenRefresh`（无 body），带 `X-Refresh-Token` + `X-Auth-Refresh-Source`；成功更新 `AccessToken`（L661），`RefreshToken`/`Domain` 仅非空才覆盖（L662-667），重算 `ExpiresAt`（L668）。

### auth 文件格式（`workbuddy.json`）

```go
// storedAuth (L353-369)
{
  "auth": {
    "accessToken":  "...",
    "refreshToken": "...",
    "expiresAt":    0,      // unix 秒
    "domain":       "..."
  },
  "account": {
    "uid":          "...",
    "enterpriseId": "...",
    "nickname":     "..."
  }
}
```

- `[已确认]` token/account 的 wire 形态（`tokenData`/`accountData`，L378-390）与 stored 几乎一致，但 token 侧是 `expiresIn`/`refreshExpiresIn`（相对秒）。
- `[已确认]` 文件写入由 CPA 宿主完成（`FileName: authFileName`，L537），插件只返回 storage JSON。

## 5. SSE 流式处理

- `[已确认]` 逐行读取（`bufio.Scanner`，buffer 64KB/上限 4MB，L784/848）；每行 `stripDataPrefix` 去掉 `data:` 前缀（L1120-1126，支持多个嵌套 `data:`）。
- `[已确认]` 跳过空行与 `[DONE]`（L787-789、852-854）——`[DONE]` 被丢弃，由宿主追加自己的流终止符（注释 L843-845）。
- `[已确认]` 每个 data 事件即一个 OpenAI 格式 chunk：`id/model/created/choices[].delta{content,reasoning_content,role,tool_calls}/usage/finish_reason`（聚合读取字段见 L1041-1077）。
- `[已确认]` **没有显式处理 `event:` 行**——只按 `data:` 解析。`[待抓包验证]` 官方客户端是否发送 `event:` 类型行（如心跳/错误事件）。

### 5.1 增量拼接（非流式聚合 `aggregateCompletion`，L1024-1109）

- `[已确认]` `content += delta.content`（L1060-1062）、`reasoning += delta.reasoning_content`（L1063-1065）、`tool_calls` 直接 append（L1066-1072，粗粒度拼接非按 index 合并）、末尾 `finish_reason` 捕获（L1074-1076）。
- `[已确认]` 输出 `chat.completion` 对象：id 缺省 `chatcmpl-workbuddy`、object、created（缺省 now）、model、choices[0]，`role` 缺省 `assistant`，`finish_reason` 缺省 `stop`（L1080-1100）。
- `[已确认]` **usage 完全透传**：直接取上游 chunk 顶层 `usage` 原样放入结果（L1050-1052 → L1101-1103）。本实现**不做** `prompt_cache_hit_tokens`/`prompt_cache_miss_tokens` 的任何注入或加工。

### 5.2 错误处理

- `[已确认]` 状态码 ≥400：非流式读 body 前 200 字符报 `upstream %d: %s`（L698-701、817-820）；流式经 `streamEmitError` 推送错误 JSON `{"error":{"message":...}}`（L207-213、772-781）。
- `[已确认]` 客户端断连：`streamEmit` 返回错误即 break 停止读上游死流（L797-799）；结束 `streamClose(streamID)`（L215-221、801）。

### 5.3 两种流式路径

- `[已确认]` 异步真流式（有 `stream_id`）：`handleExecStream` 立即返回空 chunks + headers，goroutine 后台 `pumpUpstreamStream` 逐个 `host.stream.emit`（L717-755、769-802）。
- `[已确认]` 同步回退（无 `stream_id`）：`collectUpstreamStream` 收齐返回 chunks 切片（L806-822）。
- `[已确认]` SSE 帧化判定 `clientNeedsSSEFrame`（L830-838）：依据 metadata 的 `request_path`，非 `/v1/chat/completions`、`/v1/completions`（即跨格式翻译器 claude/gemini/codex）时给每个 chunk 加 `"data: "` 前缀（L794-796、859-861）。

## 6. 非流式请求处理

- `[已确认]` `handleExecExecute`（L676-707）：上游拒非流式（code 11101，注释 L685-686）→ 先 `forceStreamBody` 强制转流式上行 → `aggregateCompletion` 把 SSE 流折叠回单个 `chat.completion` 对象返回。README.md:64 有明确记载。

## 7. 多账号 / 轮转

- `[已确认]` **不存在**。整个插件围绕单个 `storedAuth` 工作：`sharedHTTPClient()`（L415-429）全局单例（`httpClientOnce`），`backendHeaders` 用一份 access token。
- `[已确认]` 唯一"多实例"痕迹是 `loginStates` sync.Map（L103）——只是并发多个登录 state 的隔离（各用独立 cookie jar，L431-440），**非**账号轮转。
- `[待抓包验证]` AGENTS.md 基线的"多账号轮询"需 wb2api core 自己实现（宿主侧有轮转能力，见 abi-notes.md §6.3）。

## 8. 模型列表（硬编码，无上游映射）

- `[已确认]` `wbModels`（L313-346）：`UserDefined: true`、`maxCompletionTokens = 8192`（L314）、`SupportedGenerationMethods: ["chat"]`。
- `[已确认]` 模型 id **原样透传**给上游；聚合时 `model` 用上游回传的 `respModel`，缺省回退请求模型名 `firstNonEmpty(respModel, model)`（L1094）。

| id | DisplayName | contextLength |
|---|---|---|
| `glm-5.2` | GLM-5.2 | 1000000 |
| `glm-5.1` | GLM-5.1 | 131072 |
| `glm-5v-turbo` | GLM-5V Turbo | 131072 |
| `kimi-k2.7` | Kimi K2.7 | 262144 |
| `minimax-m3-pay` | MiniMax M3 | 204800 |
| `hy3` | Hy3 | 262144 |
| `hy3-preview` | Hy3 Preview | 262144 |
| `hy3-preview-agent` | Hy3 Preview Agent | 262144 |
| `deepseek-v4-pro` | DeepSeek V4 Pro | 1000000 |
| `deepseek-v4-flash` | DeepSeek V4 Flash | 1000000 |

- `[待抓包验证]` 上游可用模型全集（官方客户端模型选择面板 / 是否有模型列表接口）；AGENTS.md 基线提到 `hy4-preview`，本列表**无 hy4**——需实测确认。

## 9. 已知问题与限制（README + 读码）

- `[已确认]` 腾讯内容审核逐字拉黑 Claude Code 固定 system 模板 → `sanitizeBlockedTemplates` 最小改写绕过（README.md:66-75，cat-and-mouse）。
- `[已确认]` 模型可用性以 CodeBuddy 账号权限为准（README.md:15）。
- `[已确认]` `workbuddy.json` 含 access/refresh token，.gitignore 明令勿提交。
- `[已确认]` README 未提 prompt cache；本实现不处理缓存字段（与 issue #4 的 `prompt_cache_hit_tokens=0` 实证不冲突）。
- `[已确认]` 文件无任何日志语句（连 fmt.Println 都无），错误全走返回 error。
- `[已确认]` 注册元数据：Name=workbuddy、Version=0.1.0、Author=lovingfish（注明 clean-room rebuild，original 归属 Sliverkiss）（L296-301）。

## 10. 待抓包验证的假设（Phase 1 抓包清单，核心是 prompt cache 攻关）

### 10.1 会话 / 缓存标识（最高优先级）

| # | 假设 | 验证方法 |
|---|---|---|
| W1 | 官方客户端会在 header 或 body 中发送 session/conversation/request-id 类字段（如 `X-Session-Id`、`x-request-id`、`traceparent`、conversation id、`prompt_cache_key` 等），正是这些字段激活上游 prompt cache | mitmproxy 抓官方客户端多轮对话，比对 wb2api 插件请求与官方请求的 header/body 差异 |
| W2 | 上游对未携带会话标识的流量不启用缓存（解释 `prompt_cache_hit_tokens=0`） | 用官方抓包确认的字段注入后重测缓存 |
| W3 | 上游 SSE 末帧/usage 中 `prompt_cache_hit_tokens`/`prompt_cache_miss_tokens` 字段的真实形态与出现条件 | 官方客户端长会话抓包，观察 usage 字段变化 |

### 10.2 header 完整性与时效

| # | 假设 | 验证方法 |
|---|---|---|
| W4 | workbuddy-cliproxy 的 header 集合（§2）是否有遗漏（设备指纹、客户端版本、cookie 等） | 对比官方请求全量 header |
| W5 | `User-Agent: CLI/2.63.2 CodeBuddy/2.63.2` 是写死的，上游是否校验版本、过期后是否拒绝 | 抓官方当前 UA；换 UA 实测 |
| W6 | `Origin`/`Referer: https://www.codebuddy.cn` 是否被服务端校验（去掉/改错是否 4xx） | 抓包实测变体 |
| W7 | `X-No-*` 空值约定（§2.2）是否真实反映官方行为，还是本实现的发明 | 官方请求中对应字段为空时怎么发 |

### 10.3 body 与 SSE

| # | 假设 | 验证方法 |
|---|---|---|
| W8 | 官方 chat 请求 body 的完整字段集（温度/采样约束、thinking 配置的官方形态、metadata、tool_choice 是否需归一化） | 官方多轮对话 body 抓包 |
| W9 | 上游 SSE 是否含 `event:` 行或心跳/keepalive 事件；`[DONE]` 之外有无其他终止事件 | 官方长响应抓包 |
| W10 | hy3 思考档位是否只有 `high` 生效（§3.3）；思考内容是否走 `delta.reasoning_content` | 官方 thinking 模式抓包 |

### 10.4 OAuth 与账号

| # | 假设 | 验证方法 |
|---|---|---|
| W11 | `authUrl` 打开后的完整交互（扫码/账号密码/企业 SSO？）；cookie jar 中哪些 cookie 关联登录态 | 官方登录流程抓包 |
| W12 | token 刷新响应字段（`expiresIn`/`refreshExpiresIn` 语义）与刷新周期；`X-Auth-Refresh-Source: workbuddy` 是否必需 | 官方 + 插件刷新抓包对比 |
| W13 | 错误码全集（11101 非流式、11217 登录中之外还有哪些） | 异常路径抓包 |
| W14 | 多账号能否共存（同一账号多会话缓存亲和 vs 换账号打散缓存）；AGENTS.md 建议缓存攻关阶段单账号 | 双账号实测 |

## 11. 抓包环境准备备忘（Phase 1 用）

- 工具：mitmproxy；需处理官方客户端的 TLS 指纹校验与证书信任（CLI 客户端可能用系统证书库）。
- 目标流量：登录全流程（state/account/token/refresh）+ 多轮对话（含工具调用、thinking 开关、长会话 4 连发缓存测试）。
- 记录格式：`specs/` 下按端点/主题拆分（登录、auth、chat-headers、chat-body、sse、usage-cache、errors）。
- 缓存验证脚本（Phase 4 验收标准）：约 1200 token 固定前缀（system + 固定历史）连发 4 次，仅末条 user 变化，观察 `prompt_cache_hit_tokens`/`prompt_cache_miss_tokens`；再发两次逐字节相同请求并确认落在同一账号。

## 12. 已实测确认的事实（2026-09-02 探针，非完整登录流程）

以下为通过 tools/capture 对真实上游发出的无凭据探针请求的实证结果，
已从"假设"升级为"已确认"：

- `[已确认]` `GET /v2/plugin/auth/token?state=<任意>` 在登录未完成时返回 HTTP 200 +
  `{"code":11217,"msg":"11217:login ing...","requestId":"<uuid>"}` —— 与 L584-588 注释一致，`CodeLoginInProgress=11217` 确认。
- `[已确认]` 上游信封除 `code/msg/data` 外还有 `requestId` 字段（apiEnvelope 应补充该字段）。
- `[已确认]` 上游响应头含 `traceid`、`x-request-id`（值与 body requestId 相同）、`x-waf-uuid`、`server: TencentEdgeOne`、`eo-cache-status`（本次 MISS）、`eo-log-uuid`。
  - **缓存攻关候选字段**：`x-request-id`/`traceid`/`eo-cache-status` 可能与上游请求追踪/缓存判定相关，正式抓包时重点记录官方客户端发出的请求头中是否有对应值。
- `[待抓包验证]` 官方客户端 chat 请求是否携带上述 trace/request-id 类字段（作为请求头或 body 字段），以及它们与 prompt cache 的关系。

## 13. 官方客户端完整抓包与缓存机制（2026-09-02，已确认）

详见 `specs/capture-2026-09-02.md`（协议事实最高权威）。要点：

- `[已确认]` 官方客户端请求头含前所未知的 `X-Conversation-ID` / `X-Conversation-Request-ID` / `X-Conversation-Message-ID` / `X-Request-ID` / `X-Root-Request-ID` / `X-Trace-ID` / `traceparent` / `b3` / `X-B3-*` / `x-stainless-*` / `X-Agent-*` / `X-IDE-*` / `X-Private-Data` / `x-codebuddy-request` / `Content-Encoding: gzip` 头族（§2）
- `[已确认]` 官方客户端 body 含 `stream_options:{"include_usage":true}` / `verbosity` / `reasoning_summary` / `agent` 字段；`max_tokens=64000`、`temperature=0.9`（§3）
- `[已确认]` **缓存激活机制**：request-id 头族 = 官方身份开关。无头恒 `prompt_cache_hit_tokens=0`；带头可命中。缓存是**全局共享内容前缀缓存**（官方系统提示词全局命中 3200；独有内容永不缓存），有 TTL（§5）
- `[已确认]` 官方客户端 UA 为 `CLI/2.143.0 CodeBuddy/2.143.0`（非 2.63.2）；官方**不发** Origin/Referer/X-No-*（§2）
- `[已确认]` 官方当前模型列表（§7）：含 hy4-preview / hy3-x / glm-5.3* / minimax-m2.7 / kimi-k3-1 / kimi-k2.6；**无** hy3-preview / minimax-m3-pay
- `[已确认]` 新端点：`GET /v2/plugin/accounts`、`GET /v3/config?repos[]=`、`POST /v2/report`、`POST /v1/traces`（§6）
- `[已确认]` wb2api 端到端实测：注入 trace 头后打真实上游，官方系统提示词 → `hit=512`，与官方客户端一致
- W1-W4/W5/W8/W9/W10/W12/W13 已核销（见 specs §8）；W6/W7/W11/W14 待后续
