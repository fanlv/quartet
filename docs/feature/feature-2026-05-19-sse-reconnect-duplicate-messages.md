# SSE 重连后重复推送旧消息

日期：2026-05-19

## 现象

Job `job-20260519-122050-343198-36e341ff`（workspace `ws-20260514-154037-531286-965e42d5`）在 14:02 发送新消息后，前端界面重新显示了 12:23 发送的旧消息内容。

## 日志时间线

```
12:20:50  SSE subscribe (startSeq=0, pending)
12:23:07  run starting (send_message, priorStatus=pending)
12:24:15  SSE subscribe (startSeq=1, running) — 页面可能刷新或重连
12:27:17  SSE subscribe (startSeq=1555, completed)
12:28:02  run starting (send_message, priorStatus=completed)
...
14:02:38  run starting (send_message, priorStatus=completed)
          ACP reconnect after idle reap (deadPid=1612701 → newPid=1653860)
14:02:48  SSE subscribe (startSeq=4464, running) — 旧 SSE 连接断开后重连
14:09:45  cross-path drift → resetACPSession → replay history
          run failed: "another session is currently executing Prompt"
14:09:52  SSE subscribe (startSeq=9021, completed)
14:10:03  run starting (send_message, retry)
```

关键观察：
- 旧 SSE 连接存活了约 1.5 小时（12:27 → 14:02），期间 job 处于 completed 状态。
- 14:02:38 发送新消息触发 run，SSE 连接随后断开并在 14:02:48 重连。
- 重连时 `startSeq=4464` 是正确的（SSE 侧不会重复推事件）。
- 但 `onReconnect` 触发了 `syncJobState`，导致全量消息重新加载。

## 根因

SSE `onReconnect` 回调中调用 `syncJobState(jobId)`，该函数在 `!isInitialSync` 分支会：

1. 调用 `GET /sessions/:sid/messages` 加载历史消息（summary + tail 投影，非原始全量）
2. 执行消息合并逻辑

合并过程中产生重复的路径：

### 路径 1：合并与 SSE 事件处理的竞态

`syncJobState` 是 fire-and-forget（`onReconnect` 中不 await），SSE 事件处理并行执行。两者同时调用 `setMessages`：

```
T1: SSE 推送 TEXT_MESSAGE_START (新消息) → setMessages append
T2: syncJobState 加载完历史 → setMessages replace with [...merged, ...existingOnly]
```

`setMessages` 的 updater 基于调用时的 prev state，当 T2 的 updater 执行时：
- `merged` 包含磁盘上所有历史消息（含 12:23 的旧消息）
- `existingOnly` 包含不在历史中的消息（如正在 streaming 的新消息）
- 最终列表 = 全量历史 + 正在 streaming 的新消息

这导致 React re-render 整个消息列表，用户视觉上看到旧消息"重新出现"。

### 路径 2：消息列表闪动

即使没有数据重复，React 对 342 条消息做一次全量 diff + re-render 也会导致：
- 滚动位置跳动
- 如果 MessageItem 组件没有正确 memoize，所有消息重新渲染
- 用户感知为"旧消息被重新推送"

## 解决方案（已实现）

采用方案 A，`syncJobState` 增加 `metadataOnly` 参数：

- `onReconnect`：调用 `syncJobState(jobId, true)` — SSE 从 lastEventId 恢复，事件不丢失，无需重载消息
- `post-connect`（`.then()`）：同上
- `idle-watchdog`：调用 `syncJobState(jobId, true)` — 但内部对 terminal 状态做了特殊处理（见下文）
- terminal event handler（`JOB_COMPLETED / STOPPED / FAILED`）：调用 `syncJobState(jobId)` — 不传 metadataOnly，做完整消息 reload
- 410 恢复路径：先调用 `syncJobState(jobId, true)` 获取 lastEventSeq，然后调用 `reloadMessagesFromDisk` 重建消息列表

### metadataOnly 的 terminal 回退

`syncJobState` 内部判断条件为：

```
const skipMessages = isInitialSync || (metadataOnly && !isTerminal);
```

即：当 `metadataOnly=true` 但 job 已 terminal 时，仍会 reload 消息。这覆盖了 idle-watchdog 的恢复场景——SSE 静默导致 terminal event 漏掉，watchdog 发现 `/job/:id` 已 terminal，此时 SSE 不再推送新事件，reload 不会产生竞态。

## 消息 reload 触发路径总结

| 触发者 | metadataOnly | 是否 reload 消息 | 原因 |
|---|---|---|---|
| initial sync | — | 否 | SSE replay 负责 |
| pre-connect（attemptConnect） | true | 否（running）/ 是（terminal） | 水合已加载消息，只需 lastEventSeq |
| onReconnect | true | 否（running）/ 是（terminal） | running 时 SSE 继续推送；terminal 时需补齐 |
| post-connect | true + forceSkipMessages | 否 | hydration 或 reloadMessagesFromDisk 已负责 |
| idle-watchdog | true | 否（running）/ 是（terminal） | watchdog 专为 terminal event 丢失设计 |
| terminal event handler | false | 是 | 正常终结，确保最终一致 |
| 410 recovery | true + forceSkipMessages + reloadFromDisk | 是（仅 reloadMessagesFromDisk） | syncJobState 只取 seq，消息重建由 reloadMessagesFromDisk 负责 |

## 附加修复（2026-05-19 追加）

### 1. syncJobState 并发竞态保护

**问题**：多个路径（terminal handler / watchdog / onReconnect / post-connect）并发 fire-and-forget 调用 `syncJobState`，旧响应晚到会覆盖新状态并回退 `lastEventSeqRef`。

**修复**：引入 `syncGenerationRef` 计数器。每次调用 `syncJobState` 递增 generation，响应返回后对比当前 generation，stale 响应在 snapshot 返回和 history 加载完成两个时间点分别检查并丢弃。

### 2. terminal recovery 路径补充 finalizeInFlightMessages

**问题**：如果 terminal SSE event（JOB_COMPLETED / STOPPED / FAILED）丢失，页面靠 watchdog / syncJobState snapshot 恢复，但不会 finalize 本地 in-flight 的 assistant / tool 消息，导致它们残留"思考中 / processing"状态。

**修复**：将 `finalizeInFlightMessages` 提升为独立的 `useCallback`（之前是 `handleEvent` 内部闭包），在 `syncJobState` 检测到 terminal 状态时也调用它，按 job status 映射正确的 toolProcessingStatus 和 placeholderReason。

### 3. non-loop / interactive 场景恢复 error banner

**问题**：后端把失败原因持久化到 `job.progress.lastError`，但 `syncJobState` 恢复时不会写回前端 `error` state。loop 模式有 `LoopProgress` 组件兜底展示 `progress.lastError`，但 interactive 模式没有这层兜底，导致失败原因丢失。

**修复**：`syncJobState` 在 terminal 且 `runOutcome === 'failed'` 时，从 `job.progress.lastError` 恢复 `setError`（仅当当前无 error 时）。

### 4. syncJobState terminal recovery 使用 lastRunOutcome 替代 job.status

**问题**：interactive send 在已 terminal 的 job 上执行时，后端会恢复 prior terminal status（如把 job.status 恢复为 completed），而实际的执行结果通过 SSE 事件的 `runOutcome` 字段传递。如果 terminal SSE 事件丢失，`syncJobState` snapshot 恢复路径仅依据 `job.status` 来 finalize in-flight 消息，会把一次实际失败的 interactive send 错误地标记为 success。

**修复**：
- 后端：在 Job 结构体新增 `lastRunOutcome` 字段，在 `persistAndPublishTerminal` 中持久化每次 run 的实际结果。
- 前端：`syncJobState` 的 terminal recovery 路径优先使用 `job.lastRunOutcome`（落回 `job.status` 作为兼容）来决定 `toolProcessingStatus` 和 `placeholderReason`，以及是否恢复 error banner。

### 5. assistant 长消息时间戳重复渲染

**问题**：assistant 消息的 header 区域已渲染时间戳；当消息高度超过视口时触发 footer copy button 展示，footer 又重复渲染了一遍相同的时间戳。

**修复**：移除 footer 中的时间戳渲染，仅保留 header 一处展示。

### 6. 410 恢复路径 loop job 判断字段错误

**问题**：`reloadMessagesFromDisk` 中通过 `loopConfig.steps` 判断是否为 loop job，但 `LoopConfig` 的 canonical 字段是 `flow`，不存在 `steps` 字段。导致 410 恢复时 loop job 只 reload 第一个 session 的消息，其余 session 历史丢失。

**修复**：将判断条件改为 `loopConfig.flow`，与类型定义对齐。

### 7. syncJobState stale-response 保护未绑定 job 切换

**问题**：`syncGenerationRef` 仅在 `syncJobState` 调用时递增，用户切换 job 时的 reset effect 未 invalidate 旧 generation。存在窗口使旧 job 的 in-flight 响应通过 stale check，短暂将旧 job 数据写入新页面。

**修复**：在 `existingJobId` 变化的 reset effect 中递增 `syncGenerationRef`，确保切换 job 后任何旧响应都会被丢弃。

### 8. syncJobState stale-response 保护未覆盖 history 加载阶段

**问题**：`syncGenerationRef` 检查仅在 `/job/:id` snapshot 返回后执行一次。通过该检查后，函数继续异步执行 `parallelLimit(loadHistory...)` 加载所有 session 消息。如果在此期间有新的 `syncJobState` 调用（generation 递增），旧调用仍会完成 history 加载并执行 `setMessages`，将过期的消息列表回写到 UI。

**修复**：在 `parallelLimit` 返回后、`setMessages` 前增加二次 generation 检查。若 generation 已过期则直接 return，不再执行消息合并和状态更新。

### 9. 410 恢复路径在 terminal job 上双重 reload 消息

**问题**：`attemptConnect` 在每次重试前调用 `syncJobState(jobId, true)` 获取 lastEventSeq。但 `skipMessages` 条件为 `isInitialSync || (metadataOnly && !isTerminal)`，terminal job 下 `metadataOnly=true` 不会跳过消息加载。随后 `attempt > 0` 分支又执行 `reloadMessagesFromDisk`，导致同一轮重试内消息被加载两次。

**修复**：给 `syncJobState` 增加 `forceSkipMessages` 参数。410 恢复路径（`attempt > 0`）传入 `forceSkipMessages=true`，确保 `syncJobState` 只负责获取 metadata 和 lastEventSeq，消息重建完全由 `reloadMessagesFromDisk` 负责。同时 post-connect 回调也传入 `forceSkipMessages=true`，因为无论 attempt 0 还是 retry，连接成功时消息已经由 hydration 或 `reloadMessagesFromDisk` 加载完毕，不应再重复 reload。

### 10. syncJobState HTTP 失败被静默吞掉，调用方 try/catch 契约不成立

**问题**：`syncJobState` 对 `/job/:id` 的 HTTP non-OK 响应直接 `return`（不抛异常），外层 catch 也只 warn。但调用方 `attemptConnect` 用 try/catch 包裹 `await syncJobState(...)` 并依赖 catch 分支做 retry 或 error surface。HTTP 4xx/5xx 时不会进入 catch，导致恢复流程在 snapshot 没刷新时仍继续往后执行。

**修复**：`syncJobState` 对 HTTP non-OK 改为 `throw new Error(...)`，同时函数末尾的 catch 在 warn 后必须 rethrow（`throw err`），确保异常能传播到调用方 `attemptConnect` 的 try/catch，正确走 retry 或 error surface 路径。

### 11. loop 模式 history merge 未过滤 optimistic user message，可能出现重复气泡

**问题**：非 loop 路径的消息合并逻辑显式过滤了带 `clientMessageId` 的 optimistic user message（因为 history 里已有确认版本）。但 loop 模式的初始 hydration merge（active session 和 background session 两处）没有这个过滤逻辑。如果 hydration 执行期间用户已发送消息（optimistic msg 存在于 state），合并后会同时保留 optimistic 和 history 版本。

**修复**：loop 模式的 4 处 hydration merge 的 `prev.filter(...)` 中，增加与非 loop 路径一致的 `m.role === USER && m.clientMessageId` 过滤条件。

### 12. loop 模式下 COMMAND_SYSTEM_MESSAGE 被 session 过滤隐藏

**问题**：slash command 的 inline 回复以 `sessionId: ''` 插入消息列表。loop 模式最终返回给 UI 的消息经过 `filter((m) => m.sessionId === activeSessionId)` 过滤，`sessionId=''` 永远不匹配任何真实 session UUID，导致 inline command response 在 loop 模式下不可见。

**修复**：`COMMAND_SYSTEM_MESSAGE` 插入时使用 `activeSessionIdRef.current || ''` 作为 sessionId，确保在 loop 模式下消息归属于当前活跃 session，能正确通过过滤展示。

### 13. session history 的 multimodal 文本投影只保留第一段

**问题**：`GetSessionMessages` 遍历 `UserInputMultiContent` 时，对文本 part 使用 `if content == "" { content = part.Text }` 条件赋值，等价于只保留第一段文本。但项目其他链路（LLM 上下文构造、tokenizer 计数、ACP replay）都是拼接所有 text part。导致 history API 返回的内容可能与用户实际输入不一致。

**修复**：改为收集所有 text part 到 slice，最后用 `strings.Join(textParts, "\n")` 拼接，与其他链路行为对齐。

### 14. terminal event handler 对 syncJobState 的 fire-and-forget 调用未处理 rejection

**问题**：`syncJobState` 在 HTTP 非 2xx 时会抛错并 rethrow，但 terminal event handler（JOB_COMPLETED / JOB_STOPPED / JOB_FAILED）以 fire-and-forget 方式调用 `syncJobStateRef.current(event.jobId)` 时没有 `.catch()`。未被 catch 的 Promise rejection 会打到全局 `unhandledrejection` 监听，触发用户可见的 error overlay 并污染前端错误日志。（注：idle-watchdog、onReconnect、post-connect 三处已有 `.catch()` 保护，仅 terminal handler 遗漏。）

**修复**：三处 terminal handler 的 fire-and-forget 调用统一改为 `.catch(err => console.warn(...))` 模式，确保 rejection 被本地消化。

### 15. syncJobState 消息恢复后未同步 loadedSessionIds，导致 loop session 卡在"未加载"

**问题**：`syncJobState` 的 full reload 路径（watchdog / onReconnect 在 terminal job 下触发）会加载所有 session 消息并 `setMessages()`，但没有调用 `setLoadedSessionIds()` 同步标记。而 UI 严格依赖 `loadedSessionIds.has(activeSessionId)` 决定是否展示 "Loading session messages..."。如果断线期间创建了新 session，syncJobState 恢复其消息后，切过去时仍会显示 loading 死等。

**修复**：在 `syncJobState` 的 full reload 路径中，消息恢复完成后同步调用 `setLoadedSessionIds`，将所有处理过的 sessionIds 加入已加载集合。

### 16. loadHistory 吞掉非 404 错误并返回空数组，导致 silent empty / partial UI

**问题**：`loadHistory()` 对所有非 404 错误统一 catch 后返回 `[]`，调用方无法区分"合法的空历史"和"请求失败"。多条加载路径（初始 hydration、syncJobState、410 恢复的 reloadMessagesFromDisk）会把这个 `[]` 当成功处理：直接 `setMessages([])`、标记 loaded、结束 loading。导致页面在 session 历史加载失败时出现 silent empty UI 或 silent partial UI，无错误提示。

**修复**：
- `loadHistory()` 移除外层 try/catch，只保留 404 → `[]` 的特殊处理，其余错误正常抛出。
- `reloadMessagesFromDisk` 路径：已被 `attemptConnect` 的 try/catch 包裹，错误会走 `scheduleNextOrSurface` 展示给用户。
- `syncJobState` 路径：已被自身 catch + rethrow 覆盖，fire-and-forget 调用方的 `.catch()` 会消化。
- hydration 路径：整体有 `.catch()` 兜底并 `setError()`；后台加载剩余 session 的 `parallelLimit` 内每个 session 单独 try/catch，失败时记录到 failedSessionIdsRef 供切换时重试，不影响其他 session。

### 17. user message 缺少稳定 ID，history reload 后 summary 投影偏移导致重复气泡

**问题**：session history handler 为消息生成 fallback ID 时使用投影后的数组下标 (`sessionID:msg_N`)。assistant 消息有持久化的 `msg_id` Extra 字段会覆盖 fallback，但 user message 没有。当 summary 压缩发生后，同一条 user message 在不同次 history reload 中下标不同，产生不同 ID，导致前端 merge 逻辑将其视为两条不同消息，出现真实重复气泡。

**修复**：在 `prepareInteractiveRun` 中给 user message 的 Extra 写入 `msg_id`：第一条消息使用前端传入的 `ClientMessageID`（与 optimistic message 的 ID 保持一致），其余消息生成 UUID。history handler 已有的 "prefer stable SSE message ID" 逻辑会自动读取并使用它。

### 18. loop 后台 session 加载失败被标记为"已加载"，切换时不会重试

**问题**：loop hydration 的后台并行加载（Step 2）如果某个 session 的 `loadHistory` 失败，会直接标记为 loaded。后续用户切到该 session 时，UI 由于已在 `loadedSessionIds` 中而不显示 loading，也不触发重新加载，导致用户看到空白会话无任何提示。

**修复**：加载失败时不再标记为 loaded，而是记录到 `failedSessionIdsRef`。新增 effect 监听 `activeSessionId` 变化，当目标 session 在失败集合中时自动重试 `loadHistory`，成功后正常 merge 消息并标记 loaded，失败时保留在集合中供下次切换重试。

### 19. non-loop job 多 session 恢复不完整，刷新/410 会漏消息

**问题**：interactive / non-loop job 在切换 agentType 等情况下可能产生多个 session（后端会将新 session append 到 `job.SessionIDs`，且后续默认使用最后一个 session 继续对话）。但前端在 non-loop 的两条恢复路径——初始 hydration 和 410 reload——都只加载 `sessionIds[0]`（第一个 session）的历史。non-loop UI 最终展示的是全量消息列表，不按 session 过滤，因此多 session 的第二个及之后 session 的历史消息在刷新后丢失。

**修复**：non-loop 的 hydration 和 410 reload 路径改为遍历所有 sessionIds 依次调用 `loadHistory`，将所有 session 的消息合并后再设置到 state，与 loop 模式的 reload 行为保持一致。
