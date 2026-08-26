# 首页发送消息 → ACP Agent 运行链路

> 范围：从首页点击发送一条消息，到该消息被交给 **ACP 类型 Agent**（子进程）执行、并把流式结果推回前端的完整链路。
> 关联：通用的「用户消息 → SSE 推送」时序（Eino / ACP 共用的事件 buffer 与 SSE 通道）见 [message-to-sse-pipeline.md](./message-to-sse-pipeline.md)。本文只展开 ACP 分支特有的部分：ACP session 初始化、子进程握手、prompt 发送、事件两跳翻译。

---

## 1. 整体时序

```
┌──────────────┐  POST /api/v1/job/create            创建 interactive Job
│   前端       │ ──────────────────────────────────► ← 返回 jobId，切到 JobChat 页
│  (ChatPage / │  POST /api/v1/job/:jobId/message     投递首条消息（带 agentType / acpMode / acpThoughtLevel）
│   JobChat)   │  GET  /api/v1/job/:jobId/events      SSE 订阅流式响应（带 Last-Event-ID）
└──────────────┘
       │
       ▼
  JobMessage ─► jobService.SendMessage ─► 异步 runInteractive ─► JobRunner.RunIteration
                                                                     │ switch session.Type
                                                            "eino" ──┤── 内置 eino runner
                                                            其它  ───┘── runACPInternal   ⭐ ACP 分支
                                                                          │
                              acpAgentService.GetOrCreate（缓存/新建）─────┤
                              NewACPAgent（初始化 acpSession）────────────┘
                              agent.Run（单轮 prompt）
                                 │
                          pkg/acp：NewConn(子进程+initialize) → session/new|load|resume
                                   AcquirePromptSlot → SendPrompt(session/prompt)
                                   子进程回发 session/update → StreamHandler
                                 │
                          round.Builder（聚合 → agui 事件）─► Publish ─► 事件 buffer ─► SSE
```

三条 HTTP 通道相互独立：创建接口拿 jobId，发送接口立即返回、Agent 在后台 goroutine 执行，事件接口负责实时推送。

---

## 2. 阶段拆解

### ① 前端发送（首页 = 新建 Job）

首页是 `ChatPage`（无活跃 job 时渲染，有 job 时切 `JobChat`）。点发送 / 回车触发 `handleSubmit`，转调 `App` 的 `handleStartChat`。

- **创建 Job**：`POST /api/v1/job/create`，`mode=interactive`，随请求带上 `agentType / modelId / workspaceId / workdir`，ACP agent 还带 `acpMode / acpThoughtLevel`。返回 jobId 后切到 `JobChat`，再投递首条消息。
- **投递消息**：`JobChat` 就绪后调 `sendMessage` → `POST /api/v1/job/:jobId/message`。
- **订阅 SSE**：`useJobChat` 用自研 `SSEClient`（fetch + ReadableStream，非原生 EventSource）连 `GET /api/v1/job/:jobId/events`，以便携带鉴权 token、支持 `Last-Event-ID` 续传。

**ACP agent 的识别与配置**：ACP agent 在 agent 列表里带 `modes`（session mode）与 `thoughtLevels`（思考等级）字段；前端切换 model / mode / thoughtLevel 走 `POST /api/v1/agent/config`（首页态无 session，按 agentType 生效），发送时把当前 mode / thoughtLevel 一并带出。

### ② 后端路由与 ACP / eino 判定 ⭐

`JobMessage`（`cmd/web/handler/job_message.go`）：校验 job、解析请求、`/command` 快速路径分流（命令直接执行、不落库不转发 Agent），`prepareJobSend → prepareInteractiveRun` 构建运行参数并做 `resolveSessionID`——**若已有 session 的 `Type` 与本次 `agentType` 不一致，则新建 session 让新类型生效**。随后落 `user_input`，调 `jobService.SendMessage`。

`jobService.SendMessage`（`services/job/executor_run.go`）置 `Status=Running`、持久化 job，异步 `runInteractive` → `executeAgentTurn` → `JobRunner.RunIteration`。

**判定 ACP 还是 eino 的唯一依据是 `session.Type`**（`cmd/web/handler/job_runner.go` `RunIteration`）：等于 `consts.AgentTypeEino`（`"eino"`）走内置 runner，其它一律走 `runACPInternal`。session 的 `Type` 就是创建时传入的 `agentType`；ACP agent 的 `agentType` 是它的 **serve 命令字符串**（如 `claude-agent-acp` / `codex-acp`，来自 agent 列表探测）。

### ③ ACP session 初始化 ⭐

`runACPInternal`（`cmd/web/handler/agent_run.go`）→ `acpAgentService.GetOrCreate(...)`（`services/agent/acp/manager.go`）：

- **缓存 key = `wsID/jobID/sessionID`**，基于带 lease + singleflight 的泛型 LRU（`services/agent/internal/sessioncache`），容量由 `QUARTET_MAX_ACP_AGENTS`（默认 64）控制。
- **命中且无需重建 → 直接复用**已有 `ACPAgent` 实例（同一 session 后续消息都复用）。
- `RequiresRebuild(agentType, workdir)` 为真（agentType / workdir 变了，二者烘焙进子进程不能热切换）→ 丢弃重建；旧 agent 正在运行则拒绝并返回「busy, rebuild required」错误。

缓存未命中时调 `NewACPAgent`（`services/agent/acp/agent.go`）真正初始化，关键步骤：

1. **启动子进程连接**（`pkg/acp` `NewConn` / `NewTrackedConn`）：校验 agentType 在白名单 → 启动子进程、注入子进程标记与配置的额外 env → 建 stdio 传输（缓冲上限提高以容纳大 tool result）→ 发 `initialize` 握手（ClientInfo="quartet"），读取子进程能力 `LoadSession` / `Resume`。连接注册进全局池，有创建超时与空闲回收（默认 30 分钟）。
2. **读持久化状态**：从 SessionStore 取上次的 ACP session id 与消息指纹 `{Count, Hash}`。
3. **冷启动漂移检测**：磁盘 `messages.jsonl` 当前指纹与持久化指纹不一致（说明 eino Run / 摘要压缩 / 外部编辑改过磁盘）时记录告警，随后照常恢复持久化的 ACP session（子进程 session 是上下文的事实来源），恢复成功后把同步基线重对齐到当前磁盘快照。
4. **恢复 / 新建 acpSession**：有旧 id 优先 `ResumeSession`，不支持 resume 则 `LoadSession`，失败则报错；无旧 id 才 `NewSession` 并持久化新 id + 指纹。恢复只依赖 ACP 原生的 session/resume 与 session/load，不再新建替代 session、也不把磁盘历史作为文本前缀重放。
5. 组装 `ACPAgent`（持有 conn、acpSession、chatctx 上下文管理器、round.Builder、串行运行信号量）。

> 两个「session id」不同层：quartet 级 `sessionID`（缓存 key）与子进程级 `acpSession`（ACP 协议 id，可被 restore / reconnect 重新恢复）。

### ④ 单轮 prompt 运行

`agent.Run`（`services/agent/acp/agent.go`）：

1. 抢占并取消上一轮 Run → 用信号量串行化 → `reconnectIfNeeded`（子进程死亡时透明重连：优先 session/resume，不支持则 session/load）。上一轮被取消过（session 可能被 cancel 污染）时，先经 session/resume 或 session/load 重恢复 session 再发 prompt。
2. 提取 prompt 文本（ACP 目前只支持纯文本，带附件报错）。
3. **每轮都做漂移检测**（因实例复用，不能只在初始化查）：磁盘指纹与同步基线不一致时记录告警并把基线对齐到当前磁盘——活着的子进程 session 自身是自洽的上下文事实来源，无需重置。
4. `BeginRun` 持久化用户消息；若改写 messages.jsonl 丢弃了孤儿尾，则经 `restoreACPSession` 用 session/resume 或 session/load 重恢复子进程 session；应用 model / mode / thoughtLevel。
5. **接线顺序（关键）**：`AcquirePromptSlot`（先取消连接上正在进行的 prompt 并等信号量）→ `SetStreamHandler`（把 round.Builder 注册为该 session 的流处理器）→ `SendPrompt`，即调 ACP 的 `session/prompt` 方法，阻塞到本轮结束。这个先取槽后装 handler 的顺序，避免旧 prompt 的残余事件路由到新 handler。

### ⑤ 事件两跳翻译 → agui

- **第一跳**（`pkg/acp` `client.go`）：子进程回发的 `session/update` 按 SessionID 找到注册的 `StreamHandler`，分派为 chunk 级回调——message chunk、thought chunk、tool call / tool call update、usage。
- **第二跳**（`services/agent/round` `builder.go`）：`round.Builder` 既实现 `agentstream.StreamHandler`，又持有 `agui.EventHandler`；把 chunk 聚合成「消息轮次」并转成 UI 事件（消息起止 / 思考起止 / 工具调用起止与结果 / token usage），含迟到 terminal 缝合逻辑保证 tool_use 与 tool_result 成对，同时增量落盘到 `messages.jsonl`。

此后 agui 事件经 `Publish` 进入每 job 的事件 buffer，再由 SSE handler 推回前端——这段与 Eino 分支完全一致，细节见 [message-to-sse-pipeline.md](./message-to-sse-pipeline.md)。

---

## 3. session 缓存与复用

- **复用时机**：`GetOrCreate` 命中缓存（key=`wsID/jobID/sessionID`）且不需重建时，直接返回已有 `ACPAgent`；同一 session 的后续 Run 共用同一实例。
- **新建 / 重建时机**：缓存未命中 → 新建；agentType 或 workdir 变化 → 重建（运行中则拒绝）。
- **lease 保护**：只要有活跃 lease，淘汰与删除都不会关闭底层 agent，堵住「取出 agent 到调用 Run 之间被并发关闭」的窗口。
- **LRU 淘汰跳过运行中条目**，容量满且无空闲可淘汰时返回容量超限错误。

---

## 4. 关键约束 & 易踩坑点

1. **判定依据只有 `session.Type`**：非 `eino` 即走 ACP；ACP 的 agentType 是 serve 命令字符串，不是普通标签。
2. **agentType / workdir 不可热切换**：二者烘焙进子进程连接，变更必须重建 agent（运行中会被拒绝）。
3. **两个 session id 分层**：缓存 key 用 quartet 级 `sessionID`；ACP 协议级 `acpSession` 可被 reset / reconnect 更换，漂移检测与重连都围绕它。
4. **漂移检测是每轮做的**：因为实例复用，磁盘被其它路径（eino Run / 摘要压缩 / 外部编辑）改动后，靠指纹比对触发 ACP session 重置，避免子进程上下文与磁盘错位。
5. **接线顺序**：必须先 `AcquirePromptSlot` 再 `SetStreamHandler` 再 `SendPrompt`，否则上一轮残余的 `session/update` 会错误路由到本轮 handler。
6. **仅支持纯文本 prompt**：ACP 分支当前不支持多模态附件（带附件直接报错），图片等由前端在文本里以 markdown 形式表达。
7. **子进程生命周期**：连接进全局池，有创建超时、空闲回收、进程存活监控与透明重连；运行中通过占用标记防止被空闲回收误杀。

---

## 5. 关键文件速查表

| 角色 | 文件 |
|---|---|
| 首页 / 发送触发 | `web/src/components/ChatPage.tsx`、`web/src/App.tsx` |
| 首条消息自动发送 | `web/src/components/JobChat.tsx` |
| 发送消息 / SSE 订阅（前端） | `web/src/hooks/useJobChat.ts`、`web/src/utils/sse-client.ts` |
| ACP 配置切换（前端） | `web/src/utils/acpConfig.ts` |
| 路由注册 | `cmd/web/api.go` |
| 发送消息 Handler | `cmd/web/handler/job_message.go` |
| Job Runner 适配 / ACP·eino 分流 | `cmd/web/handler/job_runner.go` |
| Agent 分流（ACP / eino 执行） | `cmd/web/handler/agent_run.go` |
| Job Service - 异步调度 | `services/job/executor_run.go` |
| ACP Service - 缓存 / 取建 | `services/agent/acp/manager.go` |
| ACP Agent - 初始化 / 单轮运行 | `services/agent/acp/agent.go` |
| ACP session 缓存（泛型 LRU + lease） | `services/agent/internal/sessioncache` |
| ACP 子进程连接 / initialize 握手 | `pkg/acp/conn.go` |
| ACP 连接池 / 空闲回收 | `pkg/acp/pool.go` |
| ACP session 方法（new / load / resume / prompt） | `pkg/acp/session.go` |
| ACP session/update 分派 | `pkg/acp/client.go` |
| 事件聚合 → agui | `services/agent/round/builder.go` |
| 事件契约 | `types/agentstream/handler.go`、`types/agui/handler.go` |
