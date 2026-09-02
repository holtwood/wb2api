# abi-notes.md — CLIProxyAPI 插件机制读码笔记

> Phase 0 产物。来源：`../third_party/CLIProxyAPI/`（MIT，module `github.com/router-for-me/CLIProxyAPI/v7`，go 1.26.0）。
> 标注约定：`[已确认]` = 直接读码核实（附 文件:行号）；`[待抓包验证]` = 代码不足以证实、需 Phase 1 抓包/实测确认的假设。
> 参考目录只读，禁止复制本仓库代码进 wb2api；本笔记仅记录事实与接口签名。

## 1. 项目结构速览

- `sdk/`：对外 SDK。`sdk/pluginapi`（插件能力接口）、`sdk/pluginabi`（C ABI 常量与 RPC 方法名）、`sdk/cliproxy`（宿主核心服务，含 auth/executor/translator）、`sdk/translator`（协议转换注册表）、`sdk/auth`（filestore/refresh registry）、`sdk/api/handlers`（HTTP 处理）
- `internal/`：实现。`internal/pluginhost`（插件加载与 RPC 适配）、`internal/runtime/executor`（内置 provider executor）、`internal/translator/openai/claude`（OpenAI↔Claude 转换）、`internal/watcher`（auth/config 热加载）、`internal/auth/claude`（Claude token 存储）
- `cmd/server`：宿主入口
- `examples/plugin/`：官方插件示例，Go/C/Rust 三语，`simple` 是全能骨架，`executor`/`auth`/`request-translator`/`response-translator`/`scheduler`/`management-api` 等按能力拆分

## 2. 插件 C ABI 与加载流程

### 2.1 ABI 常量

- `[已确认]` `ABIVersion uint32 = 1`、`SchemaVersion uint32 = 4` — `sdk/pluginabi/types.go:7,14`
- `[已确认]` Schema 演进：v3 起 stream chunk 省略 request body；v4 新增 WebSocket response observer — `sdk/pluginabi/types.go:15-20`

### 2.2 宿主加载（Unix 路径，cgo）

- `[已确认]` `internal/pluginhost/loader_unix.go:111` `dynamicLibraryLoader.Open`：`dlopen(path, RTLD_NOW|RTLD_LOCAL)`（`:43-45`）→ `dlsym(handle, "cliproxy_plugin_init")`（`:120-121`）→ 分配 `cliproxy_host_api` 函数表，调 `cliproxy_plugin_init`（`:128-153`）→ 校验 `plugin.abi_version == pluginHostABIVersion`（`:154-157`）
- `[已确认]` 卸载：先 `plugin.shutdown` 再 `dlclose`（`:202-224`）
- `[已确认]` 加载触发：`internal/pluginhost/host.go:292` `startPluginLoad`，由 `ApplyConfig` 扫描 `plugins/<GOOS>/<GOARCH>` 与 `plugins` 目录，扩展名 `.so/.dylib/.dll` — `internal/pluginhost/platform.go:54-71`

### 2.3 插件必须导出的符号（C ABI 函数表）

```
// 宿主 → 插件（唯一入口）
int  cliproxy_plugin_init(const cliproxy_host_api* host, cliproxy_plugin_api* plugin);
// 插件 → 宿主（由 init 填充）
int  call(char* method, uint8_t* request, size_t request_len, cliproxy_buffer* response);
void free_buffer(void* ptr, size_t len);
void shutdown(void);
// 宿主 → 插件反向回调
int  call(void* host_ctx, char* method, uint8_t* request, size_t request_len, cliproxy_buffer* response);
void free_buffer(void* ptr, size_t len);
```

- `[已确认]` C 类型定义：`internal/pluginhost/loader_unix.go:12-38`；Go 侧用法见 `examples/plugin/simple/README.md:9-32`
- `[已确认]` 插件抽象接口：`internal/pluginhost/abi.go:11-17`（`Call(ctx, method, request)` / `Shutdown()`）
- `[已确认]` Go 插件用 `-buildmode=c-shared` + `//export` 导出 `cliproxy_plugin_init`，示例构建：`examples/plugin/Makefile:31-32`

### 2.4 RPC 契约：JSON envelope

- `[已确认]` 所有 host↔plugin 调用封装为 JSON：
  ```json
  {"ok":true,"result":{...}}
  {"ok":false,"error":{"code":"...","message":"..."}}
  ```
  — `sdk/pluginabi/types.go:95-106`（`Envelope`/`Error`）

### 2.5 RPC 方法表（`sdk/pluginabi/types.go:24-93`）

| 分组 | 方法名 |
|---|---|
| 生命周期 | `plugin.register` `plugin.reconfigure` `plugin.quiesce` `plugin.shutdown` |
| 模型 | `model.register` `model.static` `model.for_auth` `model.route` |
| 认证 | `auth.identifier` `auth.parse` `auth.login.start` `auth.login.poll` `auth.refresh` |
| 前端认证 | `frontend_auth.identifier` `frontend_auth.authenticate` |
| 调度 | `scheduler.pick` |
| 执行 | `executor.identifier` `executor.execute` `executor.execute_stream` `executor.count_tokens` `executor.http_request` |
| 请求/响应转换 | `request.translate` `request.normalize` `request.intercept_before` `request.intercept_after` `request.complete` / `response.translate` `response.normalize_before` `response.normalize_after` `response.intercept_after` `response.intercept_stream_chunk` |
| 其他 | `websocket.response_event` `thinking.identifier` `thinking.apply` `usage.handle` `command_line.register` `command_line.execute` `management.register` `management.handle` |
| 宿主回调 | `host.http.do` `host.http.do_stream` `host.http.stream_read` `host.http.stream_close` `host.model.execute` `host.stream.emit` `host.stream.close` `host.log` `host.auth.list/get/get_runtime/save` |

- `[已确认]` workbuddy-cliproxy 实际只用到 12 个：register/reconfigure、model static/for-auth、auth identifier/parse/login start/login poll/refresh、executor identifier/execute/execute-stream — 见 workbuddy-protocol-notes.md。

## 3. 插件注册流程（plugin.register）

- `[已确认]` `plugin.register` 返回 `rpcRegistration`（`internal/pluginhost/rpc_schema.go:15-47`）：`Metadata`（id/name/version/author）+ `Capabilities`（bool 标志 + `executor_model_scope` + `executor_input_formats/output_formats`）
- `[已确认]` 宿主解析：`internal/pluginhost/rpc_client.go:58-152` `registerRPCPlugin`
- `[已确认]` 能力 → 宿主对象适配：
  - Executor → `coreauth.ProviderExecutor`：`internal/pluginhost/adapters_executors.go:34-129`（`RegisterExecutors`/`executorAdapter`）
  - Auth → `sdk/access`：`internal/pluginhost/adapters_auth.go:42-63`（exclusive 模式 `SetExclusiveProvider`）
  - Model → `internal/pluginhost/adapters.go:228` `RegisterModels`
  - Usage → `internal/pluginhost/adapters_usage_translation.go:18` `RegisterUsagePlugins`
  - Translator → `internal/pluginhost/adapters.go` + `sdk/translator/plugin_hooks.go`
  - 调度器 → `internal/pluginhost/scheduler.go`
- `[已确认]` 管理路由：插件声明的 `ManagementRoute`/`ResourceRoute` 挂到 `/v0/management/...`（`internal/pluginhost/management.go:17`）与 `/v0/resource/plugins/<pluginID>/...`（`:18`）；注册入口 `RegisterManagementRoutes`（`:37`）
- `[已确认]` 插件 ID 规则 `[A-Za-z0-9][A-Za-z0-9._-]{0,127}`，配置 `plugins.configs.<pluginID>` — `examples/plugin/simple/README.md:142-166`
- `[已确认]` 注册调用链：`host.go:944` `callRegister` → `host.go:199` `ApplyConfig`，宿主入口 `cmd/server/main.go:615` 与 `sdk/cliproxy/service_plugins.go:102`

## 4. Provider 接口定义

### 4.1 插件侧（宿主对插件能力的抽象，`sdk/pluginapi/types.go`）

- `[已确认]` `AuthProvider`（`:268-274`）：
  ```go
  type AuthProvider interface {
      Identifier() string
      ParseAuth(context.Context, AuthParseRequest) (AuthParseResponse, error)
      StartLogin(context.Context, AuthLoginStartRequest) (AuthLoginStartResponse, error)
      PollLogin(context.Context, AuthLoginPollRequest) (AuthLoginPollResponse, error)
      RefreshAuth(context.Context, AuthRefreshRequest) (AuthRefreshResponse, error)
  }
  ```
- `[已确认]` `ProviderExecutor`（`:588-594`）：
  ```go
  type ProviderExecutor interface {
      Identifier() string
      Execute(context.Context, ExecutorRequest) (ExecutorResponse, error)
      ExecuteStream(context.Context, ExecutorRequest) (ExecutorStreamResponse, error)
      CountTokens(context.Context, ExecutorRequest) (ExecutorResponse, error)
      HttpRequest(context.Context, ExecutorHTTPRequest) (ExecutorHTTPResponse, error)
  }
  ```
- `[已确认]` 其他单方法接口（同文件）：`ModelRegistrar`/`ModelProvider`/`FrontendAuthProvider`/`Scheduler`/`ModelRouter`/`RequestTranslator`/`RequestNormalizer`/`ResponseTranslator`/`ResponseNormalizer`/`RequestInterceptor`/`RequestLifecyclePlugin`/`ResponseInterceptor`/`StreamChunkInterceptor`/`WebSocketResponseObserver`/`ThinkingApplier`/`UsagePlugin`/`CommandLinePlugin`/`ManagementAPI`

### 4.2 宿主内部 ProviderExecutor（`sdk/cliproxy/auth/conductor.go:16-31`）

```go
type ProviderExecutor interface {
    Identifier() string
    Execute(ctx, auth *Auth, req cliproxyexecutor.Request, opts cliproxyexecutor.Options) (cliproxyexecutor.Response, error)
    ExecuteStream(ctx, auth *Auth, req, opts) (*cliproxyexecutor.StreamResult, error)
    Refresh(ctx, auth *Auth) (*Auth, error)
    CountTokens(ctx, auth, req, opts) (cliproxyexecutor.Response, error)
    HttpRequest(ctx, auth, req *http.Request) (*http.Response, error)
}
```

- `[已确认]` executor 请求/响应类型：`sdk/cliproxy/executor/types.go`（`Request:75`、`Response:225`、`StreamChunk:235`、`StreamResult:244`、`Options:182`）
- `[已确认]` 账号选择：`Selector`（`conductor.go:72-74` `SelectAuth`）、`Hook`（`:92-99`）

### 4.3 新 provider 接入路径

1. `[已确认]` 实现 `ProviderExecutor`（宿主内置范例：`internal/runtime/executor/claude_executor.go:139-141` `NewClaudeExecutor` + `Identifier() = "claude"`）
2. `[已确认]` 注册：内置走 `sdk/cliproxy/service_executors.go:228-321` `registerExecutorForAuth`（按 `auth.Provider` switch）；插件走 `internal/pluginhost/adapters_executors.go:34` `RegisterExecutors`
3. `[已确认]` 启动链：`Service.Run`（`sdk/cliproxy/service_lifecycle.go:32`）→ `syncPluginRuntimeConfig`/`syncPluginModelRuntime`（`service_plugins.go:89/129`）→ `registerAvailableExecutors`（`service_executors.go:176`）
4. `[已确认]` 协议转换注册：`internal/translator/*/init.go` 调 `translator.Register(from, to, request, response)`（范例 `internal/translator/openai/claude/init.go:9-20`），底层 `sdk/translator/registry.go:30`

## 5. 内置 provider 的协议转换与流式响应（以 Claude executor 为范例）

### 5.1 入站请求 → 上游请求

- `[已确认]` Claude executor 上游端点：`%s/v1/messages?beta=true`，默认 `https://api.anthropic.com`（`internal/runtime/executor/claude_executor_stream.go:28-32`、`claude_executor_execute.go:26-30`）
- `[已确认]` 转换主入口 `TranslateRequestWithAPIKeyModelCompatibility`（`claude_executor_execute.go:66`）：按 `opts.SourceFormat`（openai/openai-response/claude）→ `sdktranslator.FromString("claude")` 调注册表转换（`sdk/translator/registry.go:66`）
- `[已确认]` 转换后重写：model 改写（`SetStringIfDifferent(body,"model",upstreamModel)`，`:67`）、thinking 注入（`ApplyRequestThinking`，`:69`）、cloaking/system 中性化（`applyCloaking`，`:81`）、`cache_control` 注入（`ensureCacheControl`，`:126-152`）、headers 处理（`applyClaudeHeadersWithNativeProfile`，`:201`）
- `[已确认]` 注册的转换函数（`internal/translator/openai/claude/init.go:10-19`）：`ConvertClaudeRequestToOpenAI`、`ConvertOpenAIResponseToClaude`（流式）、`ConvertOpenAIResponseToClaudeNonStream`、`ClaudeTokenCount`

### 5.2 SSE 流式响应解析与回写

- `[已确认]` 上游读取：`bufio.NewScanner`（50MB buffer）逐行扫 SSE（`claude_executor_stream.go:303/304`）
- `[已确认]` 目标格式即 claude：直接转发完整 SSE 帧（`stream.go:302-362`）；需翻译：每行 `sdktranslator.TranslateStream(...)` 产出 `[][]byte` chunks 发出（`stream.go:364-406`，`:383-392`）
- `[已确认]` usage 透传：`helps.ParseClaudeStreamUsage(line)`（`:374`）→ `reporter.Publish`
- `[已确认]` model 还原：`restoreResponseModel(restoredLine, req.Model)`（`:382`）
- `[已确认]` 非流式：`claude_executor_execute.go:283` `io.ReadAll` → `TranslateNonStream`（`:323-332`）

### 5.3 请求管线调用链（HTTP handler → 上游）

```
sdk/api/handlers
  └─ ExecuteWithAuthManager / ExecuteStreamWithAuthManager     handlers_execution.go:32 / handlers_stream.go:21
       └─ executeWithAuthManagerFormats / ...Stream...Formats   handlers_execution.go:45 / handlers_stream.go:293
            ├─ applyModelRouter (model.route)                    handlers_execution.go:47
            ├─ providersForExecution                             handlers_execution.go:55
            ├─ applyRequestInterceptorsBeforeAuth                handlers_execution.go:90
            ├─ h.AuthManager.Execute / ExecuteStream             handlers_execution.go:95 / handlers_stream.go:355
            │    └─ sdk/cliproxy/auth/conductor*
            │         ├─ SelectAuth / pickNext → scheduler.pickSingle/pickMixed   scheduler.go:288/332
            │         ├─ RegisterExecutor 选定 provider executor
            │         └─ exec.Execute / ExecuteStream (internal/runtime/executor/claude_executor_*)
            │              ├─ TranslateRequestWithAPIKeyModelCompatibility
            │              ├─ doClaudeUpstreamRequest → 上游 HTTP
            │              └─ TranslateStream / TranslateNonStream + usage reporter
            └─ applyResponseInterceptors / stream interceptors   handlers_execution.go:105 / handlers_stream.go:432
```

- `[已确认]` host 侧 SSE 校验：`sdk/api/handlers/handlers_stream.go:733-836` `sseJSONValidationState`（按 `\n\n` 分帧校验 `data:` JSON，用于 openai-response 协议）

## 6. auth 文件加载、多账号轮转、token 刷新

### 6.1 auth JSON 结构

- `[已确认]` auth 文件是**自由 JSON 对象**（`map[string]any`），非单一结构体；由 `coreauth.Auth`（`sdk/cliproxy/auth/types.go:47-105`）+ provider 专属 token storage 共同解释
- `[已确认]` `Auth` 关键字段：`ID/Provider/Prefix/Label/Status/Disabled/Unavailable/ProxyURL/Attributes/Metadata`；metadata 关键键：`type`（provider 名）、`access_token`、`refresh_token`、`expired`/`expires_at`/`expires_in`、`email`、`account_uuid`、`organization_uuid`、`priority`、`note`、`disabled`、`proxy_url`、`prefix`、`model_aliases`/`excluded_models`、`refresh_interval_seconds`
- `[已确认]` 过期解析：`Auth.ExpirationTime()`（`types.go:610`）；账号信息 `Auth.AccountInfo()`（`:582`）
- `[已确认]` Claude token 文件结构范例：`internal/auth/claude/token.go:29-60` `ClaudeTokenStorage{ id_token, access_token, refresh_token, last_refresh, email, account_uuid, organization_uuid, organization_name, claude_device_ids, type, expired, ... }`
- `[已确认]` 文件 → Auth 映射：`internal/watcher/synthesizer/file.go:74-240` `synthesizeFileAuths`

### 6.2 扫描与热加载

- `[已确认]` Watcher（fsnotify）：`internal/watcher/watcher.go:93` `NewWatcher(configPath, authDir, reloadCallback)`；`events.go:29-46` watch config + authDir，`events.go:67-128` 只处理 config 与 `authDir/*.json`（Create/Write/Remove/Rename，带 hash 去重）
- `[已确认]` 扫描：`internal/watcher/clients.go` `reloadClients`（`:25`）→ `BuildAPIKeyClients`（`:386`）+ `loadFileClients`（`:349` 遍历 `authDir/*.json`）+ `SynthesizeAuthFile`（`:122`）；增量 `addOrUpdateClientLocked`（`:171`）/`removeClientLocked`（`:284`）
- `[已确认]` 持久化：`sdk/auth/filestore.go:56` `FileTokenStore`（`List` `:170` `filepath.WalkDir` 找 `*.json`，`Save` `:76`）；`RegisterPluginAuthParser`（`:39`）可让插件接管解析
- `[已确认]` 默认 auth 目录：`config.example.yaml:36` `auth-dir: "~/.cli-proxy-api"`；`Service.Run` 里 `ensureAuthDir()`（`service_lifecycle.go:68`）
- `[已确认]` 变更同步：watcher 把 AuthUpdate 队列接给 service（`service_lifecycle.go:184-195`）→ `handleAuthUpdate`（`service_auth.go:89`）→ Manager

### 6.3 多账号轮转策略

- `[已确认]` 策略常量：`sdk/cliproxy/auth/scheduler.go:18-24`（roundRobin / fillFirst / weightedRoundRobin）
- `[已确认]` 实现：
  - `RoundRobinSelector`（`selector.go:34`，`Pick` `:589`，`successorIndex` 续转 `:618-627`）
  - `WeightedRoundRobinSelector`（`:41`，平滑加权 `pickSmoothWeightedAuth` `:753`）
  - `FillFirstSelector`（`:73`，取第一个可用账号 `Pick` `:787`）
  - 现代路径 `authScheduler.pickSingleWithStrategy`（`scheduler.go:292`）→ `modelScheduler.pickReadyAtPriorityLocked`（`scheduler.go:981`，按 priority 桶 + 策略 `:994-1001`）
- `[已确认]` **Session 亲和（缓存攻关关键）**：`SessionAffinitySelector`（`selector.go:873`）按 `X-Claude-Code-Session-Id`/`Session-Id`/`X-Session-Affinity`/`prompt_cache_key` 等提取 session 并绑定到 auth（`:927-1033`）；`prompt_cache_key` 也以 conversation/conversation.id 为别名（`:1345`，读取在 `:1674-1679`）；LCP Merkle 前缀匹配 `pickLCP`（`:1035`）

### 6.4 token 过期刷新

- `[已确认]` 后台循环：`StartAutoRefresh`（`sdk/cliproxy/auth/conductor_refresh.go:41`），检查间隔 `refreshCheckInterval = 5s`（`:24`），`Service.Run` 15 分钟启动（`service_lifecycle.go:203-207`）
- `[已确认]` 调度：`auto_refresh_loop.go`（最小堆 + worker，`run` `:62`、`handleDueAuth` `:221`、`nextRefreshCheckAt` `:338`）
- `[已确认]` 实际刷新：`refreshAuthForRequest`（`conductor_refresh.go:433`）→ `exec.Refresh(ctx, auth)`；401 触发 `tryRefreshAfterUnauthorized`（`:405`）；失败退避 `refreshFailureBackoff = 5m`（`:27`）
- `[已确认]` 刷新 lead 注册表：`sdk/auth/refresh_registry.go:9-15`

## 7. 对 wb2api 的接入建议（Phase 2/3 参考）

1. **最短接入路径**：实现插件 `ProviderExecutor`（`Identifier/Execute/ExecuteStream/Refresh/CountTokens/HttpRequest`），在 `plugin.register` 声明 `executor` 能力 + `executor_input_formats/output_formats`（如 `chat-completions`），协议转换交给宿主 translator（`internal/pluginhost/adapters_executors.go:399` `prepareExecutorCall`）
   - 若走 `ExecuteStream`（流式），宿主自动完成跨格式翻译与 SSE 帧校验
   - 参考 workbuddy-cliproxy：它只实现 `Execute`+`ExecuteStream`，非流式内部转流式聚合
2. **缓存亲和**：上游若开放缓存，可直接复用 `SessionAffinitySelector` 的 session 提取（`ExtractSessionID`，`selector.go:1350`）与 `prompt_cache_key` 读取逻辑的思路——但这是宿主内置能力，wb2api 作为插件可依赖宿主，或在自己的 core 里独立实现等价逻辑
3. **auth 文件**：按 `auth-dir/*.json` 自由 JSON + `metadata["type"]` 定 provider 的约定设计；wb2api 需要独立实现（core/ 禁止 import 宿主包），格式只需与宿主 watcher 的解析约定兼容
4. **轮转**：宿主提供 RoundRobin/FillFirst/Weighted/SessionAffinity 四策略，wb2api 若以插件形态运行可声明 `scheduler.pick` 能力自行控制；`[待抓包验证]` 缓存攻关阶段默认单账号测试（AGENTS.md 约定）

## 8. 待抓包验证的假设（Phase 1 抓包清单来源）

| # | 假设 | 背景 |
|---|---|---|
| A1 | 宿主的 `SessionAffinitySelector` 提取逻辑（header/body 中的 session 字段）对 WorkBuddy 上游是否同样适用 | 该逻辑为 Claude/Codex 等设计，WorkBuddy 可能用完全不同的 session 标识 |
| A2 | 插件声明 `scheduler.pick` 能力后宿主的轮转调用序列与参数 | 决定 wb2api 是否接管轮转以保缓存亲和 |
| A3 | `ExecutorRequest`/`Options` 在宿主→插件 RPC 中实际传输的字段全集（尤其 metadata 中可带的自定义键） | 插件能拿到的上下文边界，影响自定义字段注入 |
| A4 | 非流式请求在宿主侧是否总是先被转成流式（插件 `Execute` 的入站 `stream` 字段值） | 决定 wb2api 是否只需实现 `ExecuteStream` |
| A5 | `host.http.do`/`do_stream` 回调的可用性（插件是否能用宿主 HTTP 栈做上游请求） | workbuddy-cliproxy 用的是标准库 `http.NewRequest`，未走 host HTTP 回调 |
