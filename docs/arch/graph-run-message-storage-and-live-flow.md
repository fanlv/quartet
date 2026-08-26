# Graph 工作流：消息的存储、推送与刷新重载（现状梳理）

> 最后校验：2026-07-08
>
> 本文只描述**当前实现**（baseline），用于理解「用户发消息 → 大模型回复」在 Graph 工作流里如何存储、直播推送、刷新后重载，以及「直播漏、刷新全」的丢消息根因。对应的根治方案见 `docs/feature/feature-2026-07-08-graph-live-reconnect-agent-event-persist.md`。

## 总览：两份东西并行工作

理解这两者的不对称，是理解全部流程的钥匙：

| | 权威存储（磁盘、完整） | 直播通道（内存为主、部分落盘） |
|---|---|---|
| 内容 | 每个节点 session 的 `messages.jsonl` | graph run 的 `events.jsonl` + 内存 ring buffer |
| 谁写 | round.Builder 每轮 flush | graphEventHandler / appendEvent |
| 用途 | **刷新后能看到完整对话**的来源 | 直播时逐字动画、节点状态 |
| 粒度 | 合并后的完整消息（带 msg_id） | 结构事件落盘 + agent 逐 token 只进内存 |

「直播漏、刷新全」的体感正来自这两者的不对称：agent 对话内容只在内存直播通道里跑，从不落盘到事件流；只有权威存储持有完整副本。

## 一、发消息 → 大模型回复：怎么存

### 1. 权威对话存到 session 的 `messages.jsonl`

一个 agent 节点跑起来后，一轮回答结束时由 round.Builder 把合并后的完整消息 flush 出去：

- `services/agent/round/flush.go:29` `FlushPending` → 经 onFlush 回调
- → `repository/chat_context.go:346` `AppendMessages` 写入 `{sessionDir}/.meta/messages.jsonl`（`types/path/path.go:28` `messagesFile`）
- 消息带 `msg_id` / `thought_msg_id`（见 `services/graph/runtime.go:1137-1149` 关于 `lastStartedID` 的说明），保证直播气泡与刷新后历史条目**同一个 id**，可去重。

> 这是唯一权威、完整的对话副本。刷新页面看到的全部内容都来自它。

### 2. 直播事件：结构事件落盘，agent 逐 token 只进内存

Graph 事件分两类，分界点是 `isPersistableGraphEvent`（`services/graph/runtime.go:980`）：

**结构事件**（instance 生命周期、edge、progress、loop、hook、error）
- `appendEvent`（`services/graph/runtime.go:830`）走**双路径**：既 `runRepo.AppendEvent` 落盘 `events.jsonl`，又 `buf.Publish` 进内存 buffer。

**agent 流式内容**（`agentMessage*` / `agentThought*` / `agentTool*` / `agentTokenUsage`）
- 由 graphEventHandler 钩子 `OnMessageStart/Delta/End`、`OnThought*`、`OnToolCall*` 产生（`services/graph/runtime.go:1215-1369`）
- 全部走 `appendEventLocked`（`services/graph/runtime.go:1194`）→ **只 `buf.Publish`，从不落盘**
- `isPersistableGraphEvent` 对这些类型返回 `false`，刻意不写 `events.jsonl`，避免事件文件随轮数无限膨胀。

所以 `events.jsonl` 里**只有结构事件，没有任何 agent 对话内容**。

### 3. 内存 ring buffer（`services/graph/event_buffer.go`）

- `Publish` 分配单调 `seq`（从 1 开始），入 ring，唤醒 reader。
- reader 有 cursor，`Ack` 才前移——「至少一次 + 可回退」语义。
- **GC**（`gcLocked`）：所有 reader 都跨过的事件被回收，`headSeq` 前移。安全网：
  - `terminal` 状态停 GC（保尾巴给晚到的刷新）
  - 最后一个 reader 离开后 30s 宽限（`graphGCGracePeriod`）
  - resume 后无 reader 时挂起 GC，最多 5min（`graphGCSuspensionTimeout`）
  - 硬上限 50000 条强制回收（`graphBufferHardcap`）

## 二、怎么推送（直播）

前端有**两条 SSE**（`web/src/hooks/useJobChat.ts`）：

1. `/job/:id/events` —— interactive Job 事件流
2. `/job/:id/graph-run/events` —— **graph 专用**（后端 `cmd/web/handler/graph.go:392` `JobGraphRunEvents`）

Graph SSE 前端消费逻辑（`web/src/hooks/useJobChat.ts:2415-2443`）：

- **结构事件**（`instanceStarted/Completed/Failed/Skipped/progressUpdated`）→ 触发 `reconcile()`。
- **agent token 事件** → `translateGraphEvent`（`web/src/utils/translateGraphEvent.ts:33`）翻译成共享的 `TEXT_MESSAGE_START/CONTENT/END`、`TOOL_CALL_*`，再喂给 `handleEvent` → **逐字更新气泡**，复用 Agent 消息流式渲染管线。

后端 `JobGraphRunEvents`：
- 单 writer 循环从 buffer reader 读事件，`WriteEvent(id, ...)`，**SSE `id` = buffer 的 seq**。
- 超时写 keep-alive；终态空闲超时才关连接。

## 三、刷新页面后，重新加载流程

刷新后**完整内容靠历史 REST API，不靠 SSE 回放**。三步接力：

### 1. 加载历史（完整对话来源）
`loadHistory(sid)`（`web/src/hooks/useJobChat.ts:1523`）→ `GET /sessions/:sid/messages`（路由 `cmd/web/api.go:42` → `GetSessionMessages`）→ 从 `messages.jsonl` 读该 session 完整对话。thought 条目在有 `thought_msg_id` 时被单独拆出。

### 2. reconcile：重建节点/session 列表
`reconcile`（`web/src/hooks/useJobChat.ts:2315`）→ `GET /job/:id/graph-run` 拉运行快照 → 重建 session 侧边栏、按 `startedAt` 排序、**follow-latest** 选中最新启动节点的 session → 重载当前 active session 历史并 `mergeMessages` 去重。

### 3. 建立 graph SSE（`initialLastEventId: '0'`）
后端 `startSeq=0` 的处理（`cmd/web/handler/graph.go:416-420`）：
- **有 live buffer**：remap 到 `SnapshotSeq`（buffer 尾部）→ 直接接实时 tail。
- **无 buffer**（重启后 / 已终态且 buffer 已释放）：走 `graphRunEventsReplayFromDisk`（`cmd/web/handler/graph.go:542`）→ **只回放 `events.jsonl` 的结构事件**，然后 keep-alive 挂着。对话内容不在这条路上补——因为 agent 内容压根没落盘。

> 脆弱点：`startSeq` 在 live 路径里是 **seq**，在 disk-replay 路径里被当成**行号**（`cmd/web/handler/graph.go:543` `startLine := int(startSeq)`）。同一参数两种语义，这是根治方案要统一成纯 seq 的原因之一。

## 四、重连 / 漏消息根因

SSE 断开重连会带 `Last-Event-ID`（= seq）：

- buffer 能服务（seq 在 `[headSeq, nextSeq]` 且 gap ≤ 1000）→ 从该 seq 续，无缝。
- 否则返回 **410 Gone**（`cmd/web/handler/graph.go:437-443`）→ 前端 `onResumePointGone`。

**关键差异**：
- 主 job SSE 的 410 处理会**重建流 + 从磁盘重载消息**，能恢复。
- **graph SSE 的 410 只调 `reconcile()`，不重建流**（`web/src/hooks/useJobChat.ts:2445`）——流就此死掉，之后不再有实时。

**丢消息的完整链路**：
1. 某节点长思考静默期，SSE 断开；
2. 超过 30s GC 宽限，buffer 把错过的 `AgentMessageStart` + 后续 delta 回收掉；
3. 这些 delta 盘上从来没写过（`isPersistableGraphEvent=false`）→ 直播端**永久丢失**，重连补不回；
4. 唯一能补的是 reconcile 重载 **active session** 历史，但此时 follow-latest 可能已把 active 切到下一个 session（session 被两节点复用的放大器），旧 session 补全时机被错过；
5. 于是「直播只看到第一小段，刷新（重走历史 API）才看到全部」。

已有修复 `d19d0c62`（`translateGraphEvent` 里 reconnect 用 `TEXT_MESSAGE_CONTENT` 兜底建气泡）只能处理「delta 到了但 START 丢了」，处理不了「整段 START + delta 都被 GC 且盘上没有」这个场景。

## 一句话总结

- **存**：完整对话 = 各节点 `messages.jsonl`（round flush，权威）；直播 = `events.jsonl` 只存结构事件 + 内存 buffer 存全量（含 agent 逐 token，但 agent 内容永不落盘）。
- **推**：graph 专用 SSE，结构事件触发 reconcile 拉快照，agent token 翻译成气泡逐字流。
- **刷新**：完整内容走 `/sessions/:sid/messages` 历史 API + `/job/:id/graph-run` 快照重建，SSE 从 tail 接实时。
- **漏消息根因**：agent 内容只在内存 buffer、会被 GC、盘上没有备份，长静默期断线重连补不回；且 graph SSE 410 后不重建流。
