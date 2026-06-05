# 用户消息 → SSE 推送链路

> 范围：从前端发送一条用户消息，到 SSE 把 agent 产生的事件实时推回前端的完整调用链。
> 适用：Eino 与 ACP 两条 agent 分支共用同一套事件 buffer 与 SSE 通道。
> 关联：`docs/feature-2026-05-13-sse-event-buffer-detail.md`（事件 buffer 重构方案，本架构落地于该方案之上）。

---

## 1. 整体时序

```
┌──────────────┐  POST /api/v1/job/:jobId/message
│   前端       │ ─────────────────────────────────► 同步返回 200
│ (useJobChat) │  GET  /api/v1/job/:jobId         ◄───── 首屏：messages + job + lastEventSeq
│              │  GET  /api/v1/job/:jobId/events  ◄───── SSE 流式推送（带 Last-Event-ID）
└──────────────┘
       ▲                                                      │
       │                                                      ▼
[8] SSE Writer ◄── [7] EventBus / Buffer ◄── [6] loopEventHandler ◄── [5] eino/ACP runner
                                                  (agui.EventHandler   异步 goroutine
                                                   的实现)
```

发送消息接口、首屏快照接口、事件订阅接口是 **三条独立** 的 HTTP 通道：

- 发送接口立即返回 200，agent 真正的执行在后台 goroutine 中进行；
- 进入页面 / 刷新 / 410 fallback 时先调 `GET /api/v1/job/:jobId` 拿到 `(messages, job, lastEventSeq)`（响应是 `Job` 字段平铺 + `lastEventSeq` 一起的扁平 envelope，由 `JobGet` handler 经 `GetWithSnapshotSeq` 同时取出），UI 据此完整重建首屏；
- 客户端再以 `lastEventSeq` 作为 `Last-Event-ID` 起点订阅 SSE，得到此后的实时增量；
- 短暂断线重连不重拉快照，直接用客户端记住的最后一条事件 id 续传。

---

## 2. 阶段拆解

### ① HTTP 入口（同步、立即返回）

| 步骤 | 位置 | 职责 |
|---|---|---|
| 路由注册 | `cmd/web/api.go` | `POST /:jobId/message` → `Handler.JobMessage` |
| Handler | `cmd/web/handler/job_message.go` `JobMessage` | 解析请求、`/command` 快速路径分流、把消息落入 `user_input` 仓库 |
| 参数构造 | `job_message.go` `prepareJobSend → prepareJobMessage → prepareInteractiveRun` | 拼装发送参数：agentType / modelId / acpMode / sessionId / 多模态消息 |
| 构造 runner | `cmd/web/handler/job_runner.go` `newJobRunner` | 把 Handler 包装成 service 层依赖的 `JobRunner` 接口 |
| 触发执行 | `jobService.SendMessage` | **此时 HTTP 已可返回 200** |

### ② Service 异步调度

| 步骤 | 位置 | 职责 |
|---|---|---|
| 入口 | `services/job/executor_run.go` `SendMessage` | 校验 Job 状态、置 Running、按写入顺序契约先落 `job.json` 再发布 `JOB_STARTED` |
| 起 goroutine | `launchLoop → runLoop` | 真正的 agent 执行从这里开始 **异步运行** |
| 主循环 | `services/job/executor_loop.go` `runLoop / runFlowNodes` | 串起每一轮 `RunIteration`，按写入顺序契约维护 `job.json` 与事件 buffer |

### ③ Iteration 分流到 ACP / Eino

| 步骤 | 位置 | 职责 |
|---|---|---|
| 分流点 | `cmd/web/handler/job_runner.go` `RunIteration` | 取 Session（必要时从磁盘 reload），按 `Session.Type` 分流 |
| Eino 分支 | `cmd/web/handler/agent_run.go` `runEinoInternal` | 解析 ModelConfig → 取 agent lease → 执行 |
| ACP 分支 | `cmd/web/handler/agent_run.go` `runACPInternal` | 取 ACP agent lease → 按 modelId / acpMode / jobId 执行 |

**关键约定**：两条分支注入同一个 `loopEventHandler` 实例，保证产出的事件 schema 完全一致，前端无需区分 agent 类型。

### ④ Eino 内部跑（核心适配层）

主流程：`services/agent/eino/runner.go` `Quartet.Run`

执行步骤：
1. `BeginRun` 持久化用户消息
2. 装载历史上下文供 LLM 使用
3. 配置 round 构建器的回调（flush / stitch）
4. 创建 round 适配器
5. 调用 eino adk runner，得到流式 iterator
6. 在迭代循环里把每个 eino event 喂给适配器

适配层：`services/agent/eino/round_adapter.go`
- 流式 token / reasoning chunk 转发
- 非流式整段消息转发
- tool call 起止与流式输出转发
- 所有适配结果统一通过 `agui.EventHandler` 接口往外吐：消息起止、思考起止、工具调用起止、token usage

### ⑤ EventHandler → model.Event

`services/job/loop_event_handler.go` 实现 `agui.EventHandler` 接口，把回调转换成统一的 `model.Event`：

| EventHandler 回调 | 对应 model.Event 类型 |
|---|---|
| `OnMessageStart / Delta / End` | `TEXT_MESSAGE_START / CONTENT / END` |
| `OnThoughtStart / Delta / End` | 同上，但 `external.isThinking = true` |
| `OnToolCallStart / Args / Result / End` | `TOOL_CALL_START / ARGS / RESULT / END` |
| `OnTokenUsage` | `TOKEN_USAGE` |

每个事件都套上公共字段（`sessionId / runId / jobId / path / timestamp`），最终交给事件 buffer 入队。

### ⑥ Event Buffer（per-job in-memory，append-only）

`services/job/event_buffer.go` 与 `services/job/executor_pubsub.go`：

1. **入 buffer**：`Publish` 单写者串行追加，分配单调递增的序号，唤醒所有阻塞中的 reader；写入后永不修改。
2. **订阅者**：`Subscribe(jobID, startSeq)` 注册一个独占 reader，仅持有一个 cursor；不再为每个订阅者准备独立队列。`startSeq` 不在 `[headSeq, nextSeq]` 区间时返回 `ErrSeqGone`，handler 映射为 410。
3. **事件分类**：
   - **A 类**（流式 token / 思考 / 工具流）：buffer 是唯一真源，整轮 GC——轮次结束 + `messages.jsonl` 该轮已落盘 + 所有活跃 cursor 越过 round end → 整轮释放
   - **B 类**（`JOB_*` / `ITERATION_*` / `RUN_*` / `TOKEN_USAGE`）：state 真源在 `job.json`，所有活跃 cursor 越过即释放
   - **Transient 类**（`COMMAND_SYSTEM_MESSAGE`）：通过 `PublishTransient` 仅广播给当前活跃订阅者一次，不入 append-only 序列、不占序号、刷新页面不重现
4. **无终端特殊路径**：终态事件（`JOB_COMPLETED / STOPPED / FAILED`）走与其他事件相同的发布 / 投递 / cursor 推进流程；不再有 5s 超时强制交付与"超时丢弃"。
5. **无慢订阅者驱逐**：慢订阅者只让自己的 cursor 推进慢，不影响其他订阅者、不反压 producer；GC 等所有 cursor 越过即可。

### ⑦ SSE Handler 写回

`cmd/web/handler/job_lifecycle.go` `JobEvents`：

1. 解析请求头 `Last-Event-ID`（缺省 / 非数字 / `0` 都解析为 0），调 `Subscribe(jobID, startSeq)` 拿到 reader。
2. 序号超出 buffer 区间时 buffer 返回 `ErrSeqGone`，handler 直接以 **410 Gone** 响应，错误信息全量返回给客户端，触发前端走"重新拉首屏"路径。
3. 创建 SSE writer，先发一帧 keep-alive 撬开代理 / CDN buffer。
4. **单写者主循环**：用 `ReadWithTimeout(ctx, keepAliveInterval, batchSize)` 阻塞读 reader，按返回状态分支：
   - **ReadOK**：`event.Seq` 写入 SSE `id` 字段、`data` 写序列化后的 `model.Event`、写成功后 `Ack(seq)` 推进 cursor；写失败一律退出 handler、cursor 不推进，客户端可凭 `Last-Event-ID` 续传。
   - **ReadTimeout**：在同一 goroutine 里发一帧 keep-alive，再次进入读循环；keep-alive 写失败直接退出。
   - **ReadClosed**：父 ctx 已取消或 buffer/reader 已关，退出。
5. 收到终态事件后写 `[DONE]` 并关闭连接。
6. 每次网络写都套写超时（30s 量级），超时归类为僵尸连接、立即拆掉。

**单写者契约**：SSE writer 不是 goroutine-safe，因此事件写、keep-alive 写、`[DONE]` 写都从同一个 goroutine 发出，避免并发写竞争。

### ⑧ 前端消费

- `web/src/utils/sse-client.ts` —— 用 fetch + ReadableStream 自实现 SSE：手动维护 `Last-Event-ID`、指数退避重连、心跳监测、`[DONE]` 终止识别、`410` 触发 fallback。
- `web/src/hooks/useJobChat.ts` —— `onEvent` 按 `event.type` 分发：更新 message 列表、loop 进度、token 计数、tool call 状态等。
- 进入页面 / 刷新 / 410 fallback：先调 `GET /api/v1/job/:jobId` 拿 `(messages, job, lastEventSeq)`，UI 据此完整重建；以 `lastEventSeq` 作为初始 `Last-Event-ID` 订阅 SSE。
- 短暂断线重连：直接用本地保存的最后一条事件 id 重连，不再拉快照。

---

## 3. 关键约束 & 易踩坑点

1. **HTTP 200 与执行解耦**：发送消息接口立即返回，agent 在后台 goroutine 中跑；前端必须先订阅好 SSE 才能看到推送，否则只能靠下次刷新走快照路径补回首屏 state。
2. **handler 共用**：ACP 与 Eino 共用同一个 `loopEventHandler`，前端拿到的事件 schema 完全一致，不区分 agent 类型。
3. **eino 流式来源**：真正吐 token 的是 eino adk iterator，`round_adapter` 只负责把底层 chunk 翻译成 UI 语义。
4. **thought 没有独立事件类型**：复用 `TEXT_MESSAGE_*`，靠 `external.isThinking` 区分；前端渲染时必须读这个字段。
5. **写入顺序契约**（关键）：
   - **B 类事件先落 `job.json` 再 publish**——保证客户端任意时刻读 `job.json` 都不老于事件流，刷新页面不会出现"事件已发但 state 未落"的错位；GC 不需要等 `messages.jsonl`。
   - **A 类轮次先落 `messages.jsonl` 再发 round end 事件**——保证 GC 看到 round end 时该轮成品已在 jsonl，整轮回收 chunks 不会让任何重连客户端"两边都看不到"。
6. **续传与首屏的分工**：
   - 短暂断连 → 用 `Last-Event-ID` 续传，buffer 还在区间内即可，零拉取消耗。
   - 离线过久 / job 被 Start 重启过（buffer epoch 切换）→ 序号超出区间，服务端 410，前端走快照重建路径。
   - 进入终态后回看 → 直接走快照，不再订阅 SSE。
7. **慢订阅者天然背压**：慢只是 cursor 推进慢，对 producer 完全无影响；GC 等所有活跃 cursor 越过事件即可。事件不丢、不驱逐订阅者。
8. **Transient 事件刷新即消失**：`/command` 类系统反馈通过 `PublishTransient` 投递，仅一次性可见，重连或刷新不会重现，避免 slash command 的副作用被反复触发。
9. **错误全量透传**：buffer / handler 任何写入或订阅失败，错误信息原样回给客户端，不在中间层吞错。

---

## 4. 关键文件速查表

| 角色 | 文件 |
|---|---|
| 路由 | `cmd/web/api.go` |
| 发送消息 Handler | `cmd/web/handler/job_message.go` |
| 首屏快照 Handler | `cmd/web/handler/job_lifecycle.go` |
| Job Runner 适配 | `cmd/web/handler/job_runner.go` |
| Agent 分流 | `cmd/web/handler/agent_run.go` |
| Job Service - 异步调度 | `services/job/executor_run.go` |
| Job Service - 主循环 | `services/job/executor_loop.go` |
| Eino Runner | `services/agent/eino/runner.go` |
| Eino 事件适配 | `services/agent/eino/round_adapter.go` |
| EventHandler 实现 | `services/job/loop_event_handler.go` |
| Event Buffer / 分类 GC | `services/job/event_buffer.go` |
| EventBus / Pub/Sub | `services/job/executor_pubsub.go` |
| SSE Handler | `cmd/web/handler/job_lifecycle.go` |
| 事件类型定义 | `types/model/event.go` |
| 前端 SSE 客户端 | `web/src/utils/sse-client.ts` |
| 前端集成 Hook | `web/src/hooks/useJobChat.ts` |
