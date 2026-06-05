# 每步耗时统计与展示 —— 方案

> 所属项目：Quartet
> 状态：已全量实施（含时钟对齐、父组件实例保活、后续竞态/重连修复）
> 纯文字描述文档，不含实现代码

---

## 一、目标

为聊天界面上的每一个「步骤单元」增加耗时展示，把 Agent 工作过程的时间分布直观暴露给用户，方便回溯「慢在了哪一步」。

涉及的步骤单元：

- **深度思考块（Deep Thinking）**：从开始思考到思考结束的耗时
- **Assistant 气泡**：从正文开始（思考结束瞬间，或无思考时即气泡创建瞬间）到正文结束的耗时；思考段时长单独计入 Deep Thinking 徽章，不重复计入 Assistant 气泡
- **每一个 ToolCall**：从工具调用开始到返回结果的耗时
- **Shell 消息**：作为 Assistant 气泡的特殊形态（`isShellOutput`），与 Assistant 同口径
- **ChatInput 底部**：Tokens 显示右侧追加「本轮总耗时」
- **Loop Session 页面**：Sidebar 顶部显示整个 Loop Job 的累计耗时；每个 Session 行正在运行时实时滚动

---

## 二、时钟源约定

**以"后端事件携带的 `timestamp`"作为权威时间源**；仅在"需要实时滚动显示且终点未到达"时使用浏览器 `Date.now()` 作为临时终点。

- 起点 = 对应 `*_START` 事件的 `timestamp`
- 终点 = 对应 `*_END` 事件的 `timestamp`
- 正在运行的单元没有终点，用前端本地 `Date.now()` 做「实时滚动显示」的临时终点；终点一旦到达就锁定为后端时间，不再取本地时间

这样多个消息之间的耗时可以直接比较，也避免前后端时钟漂移把历史数据显示错。

**时钟偏差容错**：运行中用 `now - startTimestamp` 做滚动时，如果客户端时钟落后于服务端，这个差值可能暂时为负。此时徽章不得隐藏或展示异常值，应钳制为非负（先显示 `0ms`，随着本地时钟追上逐步变正）。这保证任何时钟状态下用户都能看到"正在计时"的反馈，而不是徽章整块消失。

**Live 实时滚动必须与后端戳对齐（关键）**：只做"钳制非负"只能兜住客户端时钟**落后**于服务端的方向。反向（客户端时钟超前于服务端，或 SSE 事件从产生到抵达客户端存在传输 / 批处理延迟、或断线重连后 ring buffer 把很久以前的 START 重放进来）同样会打破 live 与 finished 的参考系一致性：live 段用客户端墙钟减去后端戳，finished 段用后端戳减后端戳，两者参考系不同，会出现「运行中显示数分钟、结束瞬间跳回真实的几百毫秒」这种可见回跳，且回跳的方向恰好与单调钳制的保护方向相反。

要求：live 的临时终点不得直接取客户端 `Date.now()`，必须与后端戳同参考系。具体做法是在会话开始或首个携带 `timestamp` 的事件到达时记录一次客户端 → 服务端时钟偏移量（每次收到新事件可持续平滑校正，不要求高精度），live tick 以「客户端墙钟减去该偏移」作为临时终点。这样 live 与 finished 落在同一时间基上：

- 客户端超前：偏移为正，live 不再被放大。
- 客户端落后：偏移为负，live 不会再暂时为负，原本的非负钳制退化为二次保护。
- 事件传输 / ring buffer 重放：START 携带的是过去的后端戳，但临时终点也被校正为过去的后端"现在"，两者差值仍然近似真实已耗时。

终态事件到达瞬间把临时终点锁定为 `endedAt = *_END.timestamp`，即刻切出 live 模式；因为参考系从一开始就对齐，所以运行中与结束后的展示值天然连续，不会再出现「3 分钟 → 几百毫秒」的骤降。

偏移量维护的落地细节：

- **统一放在前端 Job 事件处理入口**：所有 SSE 事件（包括 delta、tool args、token usage 等高频事件）经过这里时都有机会刷新偏移量。高频事件比单纯的 start / end 更有价值，它们提供了持续的时钟校准点，避免偏移量在长时间空闲后失真。
- **按事件戳的"最大值"而非"最后到达"刷新**：`latest = max(latest, event.timestamp)` + 记录该事件被接收时的客户端墙钟。只保留最新的后端戳可避免乱序事件或重放的老事件把偏移量拉回过去——ring buffer 重放会携带一批老戳事件，之后紧接着的新事件应当把偏移量立即"拉回现在"。
- **暴露一个「服务端当前时间估计」接口给徽章消费**：返回 `latest_event_timestamp + (Date.now() - latest_receive_time)`。徽章 live tick 的临时终点、以及 `maxShown` 初始化里用到的"现在"，都从这个接口取值，不再直接调用 `Date.now()`。通过 context 注入可以避免每个调用点各自持有 offset。
- **退化情况**：一个极端场景是重连后只收到一批重放的老事件、之后没有任何新事件，且 tool 仍在跑（END 还未到达）。此时 `latest` 被锁在一个远早于现在的后端戳，live tick 显示的是"从接收 START 到现在的本地涨时"——并不等于"tool 实际已经跑了多久"（后端可能已经跑了数分钟），但相较直接用 `Date.now()` 减老戳，数字不会再冲到几分钟起步；一旦 END 或任何新事件到达，偏移量立刻被校正回正确值。该退化是可接受的边界退化。

**live 事件与落盘字段必须共享同一次时钟读数（关键）**：对同一语义边界（message/thought 的 start/end、tool 的 start/end、job/run 的 start/end）而言，「写入持久化字段」与「广播 SSE 事件」必须使用同一次 `time.Now()` 的结果，不能由两侧各自独立取时。否则即便两次取值只相差若干毫秒，也会导致刷新前（live 事件 timestamp）和刷新后（落盘 finished_at / started_at / job.finishedAt）对同一步展示出不同的耗时，破坏「以后端事件 timestamp 为权威」这一约定。落地要求：

- 负责生成边界语义的组件只复用一次边界时间：round Builder 对 message / thought / tool 边界只取一次时间戳，同时用于持久化字段与事件广播；job 生命周期对 `JOB_STARTED / JOB_*` 事件分别复用已持久化的 `job.StartedAt / job.FinishedAt`；**interactive send 的 `RUN_STARTED` 也必须复用同一次 `job.StartedAt`**（前端用它作为“本轮总耗时”的起点）。
- 事件 handler 需要能接收外部传入的边界时间戳，按「消费一次」语义使用（非边界事件例如 delta / tokenUsage / error 仍回退到各自的实时时间）。
- 该约定覆盖所有会出现 live vs reload 对照的路径：正文 chunk、思考 chunk、tool 开始与终态、superseded eager flush 为 pending tool 补发的终态、停止/取消时对挂起单元的兜底终态（`EmitPendingEnds`）。

---

## 三、数据模型调整

消息实体上需要新增两个「结束时间戳」字段，用来承载各类单元的耗时终点：

| 消息类型 | 新增/已有字段 | 含义 |
|---|---|---|
| AssistantMessage | `finishedAt`（新增） | 本条 Assistant 回复结束时间 |
| AssistantMessage | `thinkingFinishedAt`（新增） | 深度思考阶段结束时间（首个正文 delta 到达或 `isThinking` 翻转时写入） |
| ToolMessage | `finishedAt`（已存在，补齐写入） | 工具调用结束时间 |

起点字段统一复用已有的 `createdAt`（`*_START` 事件时写入）。

**历史回放中拆分出的「独立 thought 条目」必须自带时间窗口**：当一条 Assistant 消息带有独立的思考气泡 id（由后端以独立 history 条目下发），这条 thought 条目本身必须携带思考的起止时间戳（对应 `thoughtStartedAt` / `thoughtFinishedAt`）。否则历史回放时深度思考块会因缺失时间而不显示耗时徽章。

Loop Session 列表项（`LoopSessionEntry`）追加：

- `startedAt`（新增）：`ITERATION_STARTED` 事件时写入，供 running 状态实时滚动使用。已有的 `durationMs` 为 `ITERATION_COMPLETED` 时后端给出的最终耗时，不变。

---

## 四、事件处理侧

在前端 Job 事件处理流里补齐写入与兜底逻辑：

1. **深度思考结束**：当收到第一个非思考正文 delta，或 `isThinking` 从 `true` 变 `false` 时，写入 `thinkingFinishedAt = event.timestamp`（若事件未提供则用当时的事件时间兜底）。
2. **Assistant 气泡结束**：`TEXT_MESSAGE_END` 时写入 `finishedAt = event.timestamp`。
3. **ToolCall 结束**：`TOOL_CALL_END` 时写入 `finishedAt = event.timestamp`。
4. **Iteration 开始**：`ITERATION_STARTED` 时写入 Loop Session entry 的 `startedAt`。对「刷新页面时该条目已经由 job 列表 API 预先创建但缺 `startedAt`」的情况，SSE 重连后的 `ITERATION_STARTED` 重放必须回填 `startedAt`，而不是因「条目已存在」直接跳过；否则 running session 行在刷新后永远无法实时滚动。

5. **终态事件兜底收尾（关键）**：
   - 当收到 `JOB_COMPLETED / JOB_STOPPED / JOB_FAILED` 时，统一将仍处于 in-flight 状态的消息收尾为 Finished，避免 UI 卡在 `Started/Processing`。
   - 其中 ToolCall 若仍为 `Processing`：
     - `JOB_COMPLETED`：收尾为 `Success`
     - `JOB_STOPPED / JOB_FAILED`：收尾为 `Placeholder`（并写入 `placeholderReason`，用于提示"被打断/未完成"）

**Interactive send 终态事件必须携带 run 级真实结果（关键）**：

Job 终态事件（`JOB_COMPLETED / JOB_STOPPED / JOB_FAILED`）天然带有歧义——对 loop run 它反映 loop 真实结果，但对发生在「已完成 / 已失败 / 已停止」job 上的 interactive send，后端会把 job 状态恢复为发送前的 prior 状态，这时事件类型反映的是**恢复后的 job 状态**，而不是**本次 send 的真实结果**。如果前端直接拿事件类型去 `finalizeInFlightMessages`，就会在「上次已 Completed 的 job 上发了一次被 stop 的 send」等场景把当前 in-flight tool/message 错误地标成 Success。

因此协议层必须区分「job 级别终态」和「当前 run 级别终态」：后端在这三个事件上一律补一个 `runOutcome: "completed" | "stopped" | "failed"` 字段，由后端根据**本次 run 的实际路径**决定（finish → completed / stop → stopped / fail → failed），与事件类型解耦。前端：

- 用 `event.type` 做「job 级 UI 状态」：只有在「当前认为还在 loop running」时才更新 loopStatus；否则保留原状态，避免 interactive send 把已完成 loop 的 sidebar 状态回写。
- 用 `runOutcome` 做「消息级兜底收尾」：
  - `completed` → 剩余 tool 收成 `Success`，其它 in-flight 消息收成 Finished；
  - `stopped` → 剩余 tool 收成 `Placeholder`（`placeholderReason = 'interrupted'`）；
  - `failed` → 剩余 tool 收成 `Placeholder`（`placeholderReason = 'job_failed'`）。

**后端落盘终态时间戳要求（关键）**：后端在翻转"正文/思考/工具 in-flight 状态为 end"的同一时刻必须写入对应的结束时间戳（不能只补发 UI END 事件），否则 stop/cancel/正文自然结束等路径会出现"落盘 finished_at = 思考结束瞬间（正文刚开始）"的偏差，导致 history reload 时 Assistant 气泡耗时趋近 0。要求覆盖：

- 正文自然结束（stream 耗尽但未切到 thought/tool）→ 写入 `finishedAt`
- 思考自然结束（同上，未切到 message/tool）→ 写入 `thinkingFinishedAt`
- 停止/取消时仍挂起的 ToolCall → 写入该 tool 的 `finishedAt`，并把 `startedAt/finishedAt` 一并落到其 placeholder 消息里（否则 stop/cancel 场景下 history 的 placeholder 徽章没有时间窗可展示）
- **Shell 持久化也必须写入 `startedAt / finishedAt`，并写入稳定的 `msgID`**：Shell 作为 Assistant 气泡的特殊形态落盘时，若只存输出内容而不存时间窗，history reload 会因 `finishedAt` 缺失而不显示 duration badge；若不存与 live SSE 对齐的 `msgID`，reconnect 后按 id 做的 history / live 消息合并会把同一条 Shell 输出显示成两条。要求 Shell 在 `persistShellMessages` 时一并写入 shell 进程的开始/结束时间与 handler 分配的 SSE 消息 id。
- **Superseded eager flush 也必须先给 pending tool 补 `finishedAt` 并发 live `TOOL_CALL_END`**：当 LLM 在前一个 tool 还没返回结果时就开始下一条正文/思考 chunk，builder 会触发 eager flush 为每个没收到结果的 tool 合成 `[placeholder]superseded`。这个路径同样属于「终态翻转」，必须在 flush 前给这些 tool 补 `finishedAt`（否则 placeholder 落盘缺时间窗），并在 flush 后给前端发 tool 终态事件（否则 live UI 会一直卡在 Processing，直到 run 级收尾才兜底）。语义上与上面 5 的"JOB_STOPPED 下的 Placeholder"一致，reason 使用 `superseded`。

6. **Loop Session 停止/失败兜底耗时（关键）**：
   - 当收到 `JOB_*` 终态事件但某一轮仍为 `running` 且缺失 `durationMs`（例如 stop/cancel 导致没有 `ITERATION_*` 结果事件），用 `max(0, endedAt - startedAt)` 近似补齐该轮 `durationMs`，避免 Sidebar 的耗时在 stop 瞬间"消失/回跳"。

7. **Loop Session 中断态必须与"完成态"区分（关键）**：
   job 被 stop 时，后端对仍在执行的 iteration 会走 `stepAborted`，**不会**记录 `IterationCompleted`，并且保留 `Resume.NextPath`，使 Continue 能从该 step 重跑。这意味着仍在 `running` 的 iteration 在数据模型上属于"被打断、待恢复"，而不是"完成"。前端不能把这类条目统一标为 `completed`，否则 sidebar 会把可重跑的半成品伪装成绿色完成。`LoopSessionEntry['status']` 必须支持 `interrupted` 态，`finalizeRunningLoopSessions` 在 stop 路径上要把 running 的 entry 改成 `interrupted`；sidebar 渲染对 `interrupted` 给一个与 completed / failed 视觉区分的图标/状态。reconnect 后 `syncJobState` 读取到的 job 终态是 `stopped` 时，对仍在 running 的历史条目同样映射成 `interrupted`，而不是 `completed`。

   **初始 hydration（刷新页面、首次打开已存在的 job）同样遵守该映射**：从 `GET /job/:id` 构造 `loopSessions` 时，对于「出现在 `sessionIds` 但没有对应 `progress.results` 条目」的 session（典型的就是被 stop 的那一轮），必须按 job 终态派生状态——job `stopped` → session `interrupted`，job `failed` → session `failed`，job `completed` / 其他 → session `completed`——不能把非 running / 非 failed 一律塞成 completed。否则刷新后 sidebar 展示的颜色和「live stop 瞬间」展示的颜色会不一致。

8. **Live Tool 终态与 History 终态必须一致（关键）**：
   被中断 / 被 superseded 的 tool 在历史里会以 `[placeholder]` 形式落盘，reload 时前端映射为 `Placeholder`。Live 路径若把同一个 tool 终态发成 `Error`，就会出现「刷新前是 Error，刷新后变 Placeholder」的状态漂移。Tool 终态事件要多提供一个 `Placeholder` 状态（含 `placeholderReason`），专供 run 因中断 / 取消 / superseded 而提前结束时使用；真实的 tool 失败仍然走 `Error`，真实的 tool 成功走 `Success`。前端的 `TOOL_CALL_END` 处理要识别 `Placeholder` 并把 `placeholderReason` 从事件上带到消息上，确保刷新前后同一条 tool 显示状态不漂移。

   **Live Placeholder 的 reason 必须与 History 落盘的 reason 同源**：run 结束时的收尾流程会同时做两件事——向前端广播仍在挂起的 tool 终态事件（live Placeholder），和把挂起的 tool 合成 `[placeholder]` 写到历史里（history Placeholder）。这两处使用的 reason 必须一致：用户取消走 `canceled`、异常中断走 `interrupted`、被下一轮提前覆盖走 `superseded`。如果 live 侧固定使用一个 reason 而历史侧按实际退出路径写另一个 reason，用户会看到 live tooltip 和刷新后 tooltip 不一致（例如 stop 后即时显示 `Incomplete (interrupted)`，刷新后变成 `Incomplete (canceled)`）。收尾协议必须把 reason 作为参数在「广播 live 终态」与「合成 history 终态」两步之间共享，不允许任意一侧内部写死。

9. **"本轮总耗时"起点必须绑在当前 run，不能跨 run 累积（关键）**：
   ChatInput 底部展示的「本轮总耗时」语义是**本次 run** 从 `JOB_STARTED` 到 `JOB_*` 终态的时长。Start / Continue 发起的是一次**全新 run**，前一次 run 的起点不得被新 run 继承；否则底部徽章会从一个停留在上一轮完成时刻的起点继续滚动，把 stop→continue 之间的闲置时间误算进来。前端需保证：
   - 点击 Start / Continue 前，先把 `jobStartedAt / jobFinishedAt` 清零，以当前 run 的下一个 `JOB_STARTED` 事件为权威起点。
   - `JOB_STARTED` 事件 handler 如果为了兼容 SSE 重连时的 replay 而保留了防覆盖保护，该保护不得阻挡「通过 Start / Continue 触发的显式新 run」将起点重置。清零逻辑放在发起端，handler 端保留 replay 幂等性，两者职责分明。

   **同一 hook 实例被复用于不同 Job 时也必须重置起点（关键）**：用户在会话列表里切换到另一个 Job 时，hook 组件不会卸载重挂，`jobStartedAt / jobFinishedAt` 的 state 会从上一个 Job 继承过来。紧接着的 Job 初次加载（`GET /job/:id`）与 SSE 重连后的 `syncJobState` 都只用 `prev ?? ...` 语义填充这两个字段，对已经存在的旧值不会覆盖；loop 模式的 `JOB_STARTED` 事件 handler 基于同样的 replay 幂等性用了 `prev ??`，同样救不回旧值。结果是切到一个已结束 Job 时，ChatInput 底部会持续显示上一个 Job 的「本轮总耗时」；切到一个正在跑的 loop Job 时，起点会沿用上一个 Job 的起点。必须在「切换 Job」的重置路径上显式把这两个字段清零，让新 Job 的回填和事件能够作为权威值写入。

   **Interactive 发消息同样是一次「新 run」**：`sendMessage` 走的是和 Start / Continue 同构的路径，上一轮的 `roundFinishedAt` 仍在时，ChatInput 底部徽章在本地 `isLoading = true` 之后、`RUN_STARTED` 事件到达之前，会显示上一轮已结束的总耗时。窗口虽短，但在事件缺失 / SSE 异常时会拉长。统一处理方式：发起端（和 Start / Continue 对齐）在 `setIsLoading(true)` 的同一瞬间清零 `jobStartedAt / jobFinishedAt`，由 `RUN_STARTED` 事件作为权威起点重新写入。

10. **Reconnect 必须用后端 `job.progress.results` 回填 Session 态（关键）**：
    后端在 job 发出终态事件后会清空事件 ring buffer。如果客户端恰好在 `ITERATION_STARTED` 之后、`ITERATION_COMPLETED` 之前断线，重连时 SSE 侧拿不到已清理的 iteration 结果事件，本地 session 条目会停留在 `running`。此时 `syncJobState` 通过 `GET /job/:id` 读到的 `progress.results` 是每条 iteration 的权威结果（包含 `success / durationMs / tokens / error`），terminal 分支必须把这些字段回填到 `loopSessions`，而不是只拿本地墙钟减 `startedAt` 做一次本地兜底——后者会把离线时间误算进耗时，且忽略 `tokens / error` 等后端字段。只有后端 **没有** 对应 result 且本地又仍为 `running` 的条目，才走「按 job 终态映射 + 本地 fallback durationMs」的兜底路径（映射遵守第 7 条的 stop → interrupted 规则）。

    回填时需要额外注意三点，否则回填本身会产生新偏差：

    - **按 `sessionId + path` 去重，不是按 `sessionId`**：round 可复用同一个 session（非 `eachRepeat` 模式下一个 sessionId 会承载多轮 step），`progress.results` 天然会出现多条 `sessionId` 相同但 `path` 不同的结果；同理本地 `loopSessions` 的唯一键也是 `sessionId + path`。按 `sessionId` 建 Map 会让最后一轮结果覆盖前面几轮，导致同 session 的所有 step 被写成同一条 `status / durationMs / tokens / error`。去重和回填都必须用 `sessionId|path` 作为复合键。
    - **缺失的 round 要 append，不只是 map**：如果一轮迭代的 `ITERATION_STARTED` 与 `ITERATION_COMPLETED/FAILED` 两个事件都落在断线窗口内（终态事件会 flush ring buffer，不再重放），前端 `loopSessions` 里从未出现过这个 entry。此时只走 `prev.map` 无从下手，这一轮会在 sidebar 里消失。`syncJobState` 的 terminal 分支必须以「`progress.results` 是权威清单」为前提，对本地缺失的 `(sessionId, path)` append 出对应 entry（`status / durationMs / tokens / error` 取自 result；`startedAt` 允许缺失），保持与首次 hydration 从 `progress.results` / `resume.nextPath` 重建 entries 的路径语义一致。
    - **本地兜底终点优先取 `job.finishedAt`，缺失时才退化到服务端时钟投影**：对既没有 authoritative result、本地又仍为 `running` 的条目，fallback 计算 `durationMs` 时若 `GET /job/:id` 已返回终态 `finishedAt`，必须优先用它作为终点；它代表 job 真实停止时刻，能避免把 stop 到 reconnect 之间的离线空窗误算进时长。只有响应里确实没有 `finishedAt` 时，才退化到第二章的 `getServerNow()` 投影，而不是直接 `Date.now()`。`startedAt` 来自 SSE 事件时间戳（服务端参考系），因此退化路径也必须继续停留在服务端参考系，避免客户端时钟漂移再引入额外误差。

同时，在 `useJobChat` 层导出整个会话的起点时间戳与结束状态，供 ChatInput 展示「本轮总耗时」使用；当还在跑时前端实时滚动，跑完锁定到后端事件给出的最后时间。

---

## 五、展示组件

### 5.1 通用耗时徽章

统一抽象一个「耗时徽章」UI 组件，负责把 `(startedAt, endedAt?)` 渲染成字符串：

- running 徽章首次挂载时必须以当前本地时间初始化临时终点，避免首帧闪现 `0ms`
- `endedAt` 已确定 → 静态显示，例如 `0.8s`、`1m 23s`
- `endedAt` 未确定 → 按固定频率（250ms 量级）刷新，实时滚动
- 统一格式化规则：
  - `< 1 秒` → `234ms`
  - `< 60 秒` → `1.2s`
  - `< 60 分钟` → `1m 23s`
  - 否则 → `1h 2m`

**组件实例保活（关键）**：徽章承担「单调不回跳」的保证，实现上依赖一个只升不降的已展示上限（见第六节）。这个上限必须跨 running → finished 保活，一旦徽章组件被卸载重挂，这个上限就会被清零，新实例会直接按后端给出的最终耗时重新初始化——在 live 段由于参考系偏差被放大时，"重挂"就会表现为数字在终态瞬间骤降。

该约束不仅覆盖徽章组件自身，还覆盖它的所有父级包装组件（例如 ToolCall 卡片、Assistant 气泡、Shell 气泡、Loop Session 行）：这些父组件的 React key 不得随步骤状态（`Processing → Success / Error / Placeholder` 等）变化，否则父组件的卸载重挂同样会连带把徽章销毁。状态切换必须通过 props 驱动重渲染，而不是通过 key 驱动重挂。

去掉 key 里的状态位之后，原本"借 key 变化顺便重置某些 UI 状态"的行为也会一并消失，需要显式补回来。例如 ToolCall 卡片原先靠 key 切换让"Processing 默认展开、完成后默认折叠"自然发生，改造后 `isExpanded` 只在首次挂载初始化，不会再跟随状态翻转；必须用监听 `toolCallStatus` 的 effect 在 Processing → 非 Processing 的边界显式折叠。这个"配套迁移"适用于所有因该约束被去除 key 的父组件——凡依赖 key 重置的初始值，都改为 effect 驱动。

### 5.2 接入点

| 位置 | 起点 | 终点 |
|---|---|---|
| Deep Thinking 头部（💭 深度思考 右侧） | 消息 `createdAt` | `thinkingFinishedAt` |
| Assistant 气泡头部（Agent 名右侧、Copy 按钮之前） | `thinkingFinishedAt ?? createdAt` | `finishedAt` |
| Shell 气泡（`isShellOutput`，套用 Assistant 模板） | `createdAt` | `finishedAt` |
| Tool call 卡片头部（状态徽章右侧） | `createdAt` | `finishedAt` |
| ChatInput 底部 Tokens 右侧 | 当前 run / job 的开始事件 `timestamp` | 当前 run / job 的结束事件 `timestamp`（运行中为空） |
| Loop Session Sidebar 顶部标题右侧 | 已完成 session 的 `durationMs` 聚合值 + running session 的实时执行耗时 | — |
| Loop Session Sidebar 每个 Session 行 | 已完成 rounds 的 `durationMs` 聚合值 + 当前 running round 的实时执行耗时 | — |

补充规则（避免旧历史数据误判为 running）：

- **仅在“确定仍在运行”的状态下允许 `endedAt` 缺失并实时滚动**（例如 streaming / processing / loop running）。
- 若是**历史回放且状态已结束**，但缺失 `finishedAt` / `thinkingFinishedAt`，则按“字段缺失 = 不展示”处理。
- **「有思考内容但缺 `thinkingFinishedAt`」的历史条目，Assistant 气泡徽章必须不展示**。原因：Assistant 气泡的起点口径是「思考结束瞬间」，如果这一字段缺失而用消息起点做回退，就会把思考段时长折算进气泡显示里，违反「thought 段时长单独计入 Deep Thinking 徽章，不重复计入 Assistant 气泡」的口径。对这类边界数据，宁可不显示徽章，也不能展示一个把思考段双算进去的误导数值。Deep Thinking 徽章侧按同样的「字段缺失 = 不展示」规则处理。

### 5.3 样式要点

- 徽章：透明底（无背景块、无 pill 轮廓），等宽数字、小字号，紧贴在标题文本右侧作为次级信息
- 颜色口径（不再按 thinking / assistant / tool 分色）：
  - **运行中**：所有徽章统一使用一种高亮色 + pulse 呼吸动画，提示「还在跑」
  - **已结束**：所有徽章统一回落为灰色，避免结束态徽章抢占视觉焦点
- 运行 → 完成的切换只改颜色与动画，不改字号/位置/格式化规则，避免尺寸抖动

---

## 六、Loop Session 页面补强

当前 Loop Sidebar 只给**已完成** session 显示最终 `durationMs`。本次改动要做两件事：

1. **Sidebar 顶部加总耗时**：`Sessions / count` 行旁显示统一口径的累计执行耗时。具体为：`Σ 已完成 session.durationMs + Σ 正在运行 session 的实时执行耗时`；全部跑完后锁定为最终 `Σ durationMs`，避免完成瞬间数字回跳。
2. **Running session 行的耗时**：running 状态展示 `已完成 rounds 的 durationMs 之和 + 当前 running round 的实时执行耗时`，完成后自动切换回后端返回的最终 `durationMs`，保证 running / completed 两态口径一致。

**单调性保证（实现约束）**：完成瞬间客户端的实时估计 `(now_client - startTimestamp)` 可能略超过服务端最终给出的 `durationMs`，直接切换会出现数字回跳。耗时徽章必须保留"当前已展示的最大值"作为下限，后续每次重新计算都不得低于这个下限；同时 running → completed 的过渡必须复用同一个徽章实例（不能换成普通文本节点），否则这个下限会因组件重新挂载而丢失。

已完成 session 行的耗时显示使用与徽章一致的格式化规则（如 `1m 23s`），避免出现 `125.3s` 这类不一致格式。

`LoopProgress` 组件（可折叠的每轮结果列表）已经显示 `durationMs`，本次不动。

---

## 七、风险与权衡

1. **时钟源一致性**：采用后端时间戳后，只要后端所有事件来自同一进程就能保证单调性。跨进程（如 ACP sandbox 通过 IM gateway 透传）需要确认上游时钟；如果发现倒挂，退化为前端 `Date.now()` 兜底，但不做混用。前端侧 live tick 的临时终点同样不能直接取客户端墙钟——必须先按第二章「Live 实时滚动必须与后端戳对齐」记录一次客户端 ↔ 服务端偏移量，让 live 段与 finished 段落在同一时间基上。
2. **大量定时器**：历史滚动条很长时理论上会有 N 个 running 徽章同时存在，但因为「running」在时序上至多几个并存，单页最多个位数，直接 `setInterval` 不做全局调度。
3. **历史会话加载**：从存储回放历史消息时，若旧数据没有 `finishedAt` / `thinkingFinishedAt`，徽章不显示（而非显示 `0s` 或误判为 running），避免误导。对旧版 ToolCall 历史若缺失 per-tool `startedAt`，前端用 assistant/thinking 的结束边界近似回填起点，优先保证历史耗时可见且非负；新老数据共存期通过「字段缺失 = 不展示」优雅降级。
4. **深度思考与正文边界模糊**：部分模型不会显式翻转 `isThinking`，以「首个非思考 delta」作为边界；极少数场景可能把思考阶段一直拉到 `TEXT_MESSAGE_END`，此时 `thinkingFinishedAt` 与 `finishedAt` 相等，界面上 Assistant 气泡耗时为 0，属于可接受的显示结果。

5. **终态兜底的语义选择**：停止/失败时对仍处于 `Processing` 的 ToolCall 标为 `Placeholder`（而不是 `Success`），确保 UI 不把"被打断的工具调用"误渲染为绿色完成。为避免同一条 tool 在 live 显示为 `Error`、在 history 显示为 `Placeholder` 的状态漂移，tool 终态协议需要引入独立的 `Placeholder` 状态；真实失败仍走 `Error`，真实成功走 `Success`，中断 / 取消 / superseded 统一走 `Placeholder`（携带 `placeholderReason`）。

6. **SSE 事件重放的覆盖范围**：刷新后 running session 实时滚动依赖 SSE ring buffer 重放 `ITERATION_STARTED` 来补齐 `startedAt`。极端场景下若 iteration 累积的事件数量超过 ring buffer 容量，最早的 `ITERATION_STARTED` 会被挤出，此时 running 行仍可能不滚动；这是 ring buffer 容量与内存占用的权衡结果，属于可接受的边界退化（徽章不显示 ≠ 显示错乱）。

7. **Interactive send 的终态歧义**：interactive send 完成后，job 状态会被恢复为发送前的 prior 状态，`JOB_*` 事件反映的是这个恢复后的 job 状态，而不是这次 send 的真实结果。如果前端直接用事件类型做消息级兜底会出现误判（例如一次被 stop 的 send 依然落到 `JOB_COMPLETED` 分支）。因此协议层引入 `runOutcome` 字段把「job 级终态」和「run 级终态」解耦：前端消息级兜底一律看 `runOutcome`，loopStatus 更新仅在当前仍被认为是 loop running 时才触发。

---

## 八、不做的事

- 不改变后端事件协议（`BaseEvent.timestamp` 已足够，不新增 `endTimestamp` 字段）
- 不把每步耗时持久化到 Job 存档里；它们可以在加载历史时从已有的消息 `createdAt` / `finishedAt` 推导（注：Job 级别的 `StartedAt` / `FinishedAt` 已持久化到 Job 模型上，用于刷新后恢复"本轮总耗时"badge）
- 不做跨 Job / 跨 Session 的耗时统计页
- 不引入全局实时滚动调度器，就用本地 `setInterval`

---

## 九、当前实施状态盘点

本章盘点方案的落地情况，用于跟踪还剩哪些项待办。描述按"现状"口径写，不固化到具体文件 / 行号以避免腐化，各字段与事件名以代码中实际命名为准。

### 9.1 数据流主干

**后端落盘（已实施）**

- 每条 Assistant 消息的 Extra 上写入四个 Unix 毫秒时间戳 key：消息起止、思考段起止。其中 thought 时间戳仅在该消息确实带思考段时存在。
- 每条 Tool 消息的 Extra 上写入 start / finish 两个时间戳。Placeholder（superseded / interrupted / canceled）落盘时同样带齐这两个戳，并携带 `placeholderReason`。
- Shell 持久化时连带写入 shell 进程的 start / finish 以及 SSE handler 分配的稳定 msgID，保证 live 与 reload 合并时不重复。
- **Shell 在 stop / cancel 的 interrupted 路径上也必须持久化已产生的输出**：`stepAborted` 只表示该 iteration 不写 `IterationCompleted`、Continue 会从当前 step 重跑；不代表已经通过 live SSE 展示给用户的 shell output 可以丢弃。否则用户 stop 时看到的 shell 气泡会在 refresh 后从 history 消失，破坏 live / reload 一致性。
- Message / thought / tool 的时间戳由 round Builder 在各自边界回调内只取一次 `time.Now()`，同一读数同时用于 Extra 落盘和对应 SSE 事件的 `BaseEvent.timestamp`；Job 级 `JOB_STARTED / JOB_*` 事件则分别复用已持久化的 `job.StartedAt / job.FinishedAt`（方案第二章「live 事件与落盘字段必须共享同一次时钟读数」的落地）。
- Loop Job 的每轮结果（`IterationCompleted` 的 `durationMs` 等）落在 Job 的 progress 结果列表里。

**历史 API 下发（已实施）**

- 会话历史 handler 把 Extra 上的四个时间戳按 camelCase（`startedAt / finishedAt / thoughtStartedAt / thoughtFinishedAt`）放进 `HistoryMessage` 一并返回，全部为 int64 Unix 毫秒。
- Tool 历史条目同样带 `startedAt / finishedAt`，并在 Placeholder 情况下带 `placeholderReason`。
- 带独立思考气泡 id 的 Assistant 历史条目会自带 thought 窗口（方案第三章的要求）。

**前端数据模型（已实施）**

- `AssistantMessage` 在 `createdAt` 之外新增 `finishedAt` 和 `thinkingFinishedAt`。
- `ToolMessage` 在 `createdAt` 之外新增 `finishedAt` 和 `placeholderReason`。
- `LoopSessionEntry` 新增 `startedAt`，跟既有 `durationMs` 并存，用于 running 状态滚动。

**前端事件写入（已实施）**

- `TEXT_MESSAGE_START` → 消息 `createdAt` 从 `event.timestamp` 初始化。
- `TEXT_MESSAGE_CONTENT` 首个非思考 delta（或 `isThinking` 翻转）→ 写入 `thinkingFinishedAt`。
- `TEXT_MESSAGE_END` → 写入 `finishedAt`，兼顾"思考段从未翻转"的边界。
- `TOOL_CALL_START` → Tool 消息 `createdAt` 初始化，同时若与已存在的 Finished 历史条目冲突则不回滚。
- `TOOL_CALL_END` → 写入 `finishedAt`，识别并透传 `placeholderReason`。
- `ITERATION_STARTED` → Loop session entry 的 `startedAt` 被写入或回填（覆盖 SSE 重连重放场景）。
- 终态事件（`JOB_COMPLETED / JOB_STOPPED / JOB_FAILED`）按 `runOutcome` 决定兜底语义，把仍 in-flight 的 tool 收尾为 Success / Placeholder。

**Job 级耗时（已实施）**

- `useJobChat` 导出 `roundStartedAt / roundFinishedAt` 给 ChatInput 消费。起点在发起新 run（Start / Continue / SendMessage）时清零：loop 模式由下一个 `JOB_STARTED` 事件设置；interactive 模式由下一个 `RUN_STARTED` 事件设置；终点由终态事件（loop 为 `JOB_*`，interactive 为 `RUN_FINISHED / RUN_ERROR`）设置。针对 Continue 的 HTTP/SSE 并发时序，前端不再让晚到的 `/continue` 响应无条件把终态改回 `running`。
- **后端持久化**：Job 模型新增 `StartedAt` / `FinishedAt`（int64 Unix 毫秒，RunLoop-owned），分别在 Start / Continue / SendMessage 入口写入 `StartedAt` 并清零 `FinishedAt`，在 `finishJob` / `stopJob` / `failJob` 写入 `FinishedAt`。`publishJobStarted` 的 SSE 事件 timestamp 复用已持久化的 `job.StartedAt`，`publishTerminalEvent` 复用同一轮刚写入的 `job.FinishedAt`；interactive send 的 `RUN_FINISHED / RUN_ERROR` 现在也复用这一轮预先 pin 的 `job.FinishedAt`，保证 run / job 两层边界时间同源。
- **前端水合**：`loadHistory` 和 `syncJobState` 均从 `GET /job/:id` 响应中读取 `job.startedAt` / `job.finishedAt`，用 `prev ?? ...` 语义回填 `jobStartedAt` / `jobFinishedAt`，确保终态后（ring buffer 已清空）刷新页面仍能展示"本轮总耗时"badge。

**历史回放兼容（已实施）**

- 历史加载路径把 HistoryMessage 的时间戳字段映射到前端消息对象的对应字段；legacy 缺失 per-tool `startedAt` 时用 assistant / thinking 的结束边界近似回填。
- 首次 hydration 从 `GET /job/:id` 重建 `loopSessions` 时，缺失 entry 的补齐同样按 `sessionId + path` 复合键处理：不仅补「整个 session 从未出现过」的条目，也要补「同一 session 已有 earlier results，但 `resume.sessionId + resume.nextPath` 指向的当前 round 尚未落入 `progress.results`」这一条，状态按 job 终态映射（`stopped → interrupted` 等），避免刷新后把半成品 round 吞掉。
- Reconnect 时 `syncJobState` 会在 **running 与 terminal 两种状态** 下都按 `job.progress.results` 回填每轮结果，失败 / 停止 / 完成的状态映射遵循第 7 条的 stop → interrupted 规则。回填按 `sessionId + path` 复合键去重（避免 session 跨 step 复用时同 session 多条结果互相覆盖）；本地未出现过的 `(sessionId, path)` 会基于 `progress.results` append 出缺失的 entry（覆盖「两端事件都落在断线窗口内」的极端场景）；对 terminal 下既无 authoritative result 又仍为 `running` 的条目，fallback `durationMs` 的终点取第二章的服务端墙钟投影（非 `Date.now()`），保持与 `startedAt` 同参考系。

### 9.2 前端展示点

| 位置 | 组件 | 起点 | 终点 |
|---|---|---|---|
| Deep Thinking 头部 | Assistant 消息渲染组件 | 消息 `createdAt` | `thinkingFinishedAt` |
| Assistant 气泡头部 | 同上 | `thinkingFinishedAt ?? createdAt` | `finishedAt` |
| Shell 气泡 | 同上（Shell 形态） | `createdAt` | `finishedAt` |
| Tool call 卡片头部 | Tool 消息渲染组件 | `createdAt` | `finishedAt` |
| ChatInput 底部 | ChatInput | `roundStartedAt` | `roundFinishedAt` |
| Loop Sidebar 顶部 | LoopSessionSidebar | 已完成 sessions 累加 + running 实时 | — |
| Loop Sidebar 每行 | 同上 | 同上，范围缩到单个 session | — |

徽章本身的格式化规则、running / finished 态样式、单调钳制内部 state、250ms tick、旧数据字段缺失时不展示等规则均已实施。

### 9.3 关键修复落地说明

本章早期版本在此列出若干项"尚未落地"的关键项。现均已完成实施，记录如下，便于后续回顾修复手法。

**修复 A：Live 实时滚动与后端戳对齐**（方案第二章要求）

- 在前端 Job hook 中新增「服务端墙钟估计」状态：维护两个字段——到目前为止观察到的最大 `event.timestamp` 以及该事件被前端接收时的客户端墙钟时间。
- 所有 SSE 事件（包括 delta、tool args 这类高频事件）进入 Job 事件分发器时，第一步就刷新这个状态，按 "max" 语义更新以防乱序 / ring buffer 重放老事件把估计值拉回过去。此动作必须发生在「history 尚未加载时的事件 buffer」之前，否则 running job 在首页数据到达前的 SSE 流量就无法参与时钟校准。
- hook 暴露一个 `getServerNow()` 接口，返回 `latestServerTs + (Date.now() - latestReceiveTime)` —— 在客户端墙钟基础上投影出服务端当前时间的估计；若还没收到任何事件，回退到 `Date.now()`。
- 新增独立的 React Context（独立模块，默认值 = `Date.now`）承载 `getServerNow`；在消费 Job hook 的高层组件（当前是 JobChat）用 Provider 包住整个聊天子树。徽章组件通过 hook 订阅，无需逐级 props 传递。
- 徽章组件内部所有原先调用 `Date.now()` 的地方（mount 初始化 `now` / `maxShown`、props-change 的 rAF 对齐、running 态的 250ms tick）一律切换到注入的 `getServerNow()`。为避免该函数被列入 `useEffect` 依赖数组导致 interval 在父组件每次 render 时重置，hook 内部用 ref 缓存最新引用，tick 从 ref 读取。
- 原来仅应对"客户端落后"方向的非负钳制保留作二次保护——在极端退化场景（重连后只收到重放 START、后续无新事件）中继续兜底。

- **切换 Job 必须清空该校准状态**：`serverClockRef` 基于“当前 job 的事件流”校准，若 hook 实例在 Job 切换时复用而不清零，从“正在运行的 job（最新戳）”切到“历史 job（旧戳）”会导致 `max()` 保护拒绝历史戳校准，live tick 继续投影上一个 job 的“服务端现在”，从而让耗时滚动失真。Job 切换的 reset effect 需显式 `serverClockRef.current = null`。

**修复 B：父组件实例保活**（方案第 5.1 节要求）

- 摘掉 Tool 卡片渲染分支里 React key 中拼接的 `toolCallStatus`，该分支不再提供 key（外层 MessageList 已按 `message.id` 分配 key）。这使得 `Processing → Success / Error / Placeholder` 的状态翻转不再卸载重挂子树，徽章的 `maxShown` 单调钳制 state 得以跨状态保活。
- 原先依赖 key 变化"顺便重置"的 `isExpanded`（Processing 默认展开、完成后默认折叠）改用 React 19 推荐的「render 阶段检测 prop 变化」模式：用一个 `prevStatus` useState 存上一轮 status，在每次 render 里比较并在 Processing → 非 Processing 的翻转瞬间调用 `setIsExpanded(false)`。没有使用 `useEffect + setState`，既符合 React 19 新的 `react-hooks/set-state-in-effect` 规则，也避免了多一次 commit。

**修复 C：JOB 终态事件与 `job.FinishedAt` 同源**（方案第二章要求）

- `finishJob / stopJob / failJob` 在写入 `job.FinishedAt` 后，会把这一次边界时间显式传给 `publishTerminalEvent`。
- `publishTerminalEvent` 不再自行额外取一次 `nowMillis()`，而是直接把传入的 `terminalAt` 写进 `JOB_COMPLETED / JOB_STOPPED / JOB_FAILED` 的 `BaseEvent.timestamp`。
- 这样前端 live 路径消费到的 `event.timestamp` 与 reload 路径从 `GET /job/:id` 水合出的 `job.finishedAt` 保持同源，ChatInput 底部「本轮总耗时」不会再因为 `saveJob` 与发事件之间的间隔，在刷新前后出现几十到几百毫秒的偏差。

**修复 D：Shell 的 `TEXT_MESSAGE_END.timestamp` 与落盘 `finishedAt` 同源**（方案第二章“同一次时钟读数”要求）

- Shell 不是通过 round Builder 触发 message boundary，因此需要在 `cmd.Wait()` 返回后人为 pin 一次终态时间戳：只取一次 `nowMillis()` 作为 `shellFinishedAt`，同时用于 `handler.SetNextBoundaryTimestamp(shellFinishedAt)` + `handler.OnMessageEnd()` 的 SSE 事件戳，以及 `persistShellMessages(... finishedAt=shellFinishedAt ...)` 的落盘字段，避免 live vs reload 的毫秒级不一致。

**修复 E：Shell stop/cancel 语义识别**（终态兜底语义一致性）

- Shell 使用“手动 kill 进程组”而非 `exec.CommandContext`，因此 stop/cancel 时 `cmd.Wait()` 多数返回 `*exec.ExitError`（signal killed/terminated），不会包装成 `context.Canceled`。需要在 `cmd.Wait()` 后以 `ctx.Err() != nil` 作为兜底判据，把这类退出归类为 interrupted run，从而走与 AI 路径一致的“中断”语义分支（而不是误判为脚本自身失败）。
- 进入 interrupted 分支前，shell executor 仍需把已累计的 stdout/stderr 连同 `startedAt / finishedAt / msgID` 落盘；否则 `TEXT_MESSAGE_END` 虽然已经发给 live 前端，但 refresh 后历史中没有对应 shell 消息，用户会看到“刚才 stop 前还存在的 shell 输出消失了”。

**修复 F：`RUN_FINISHED / RUN_ERROR` 与 `job.FinishedAt` 同源**（方案第二章要求）

- interactive send 在 iteration 返回后会先 pin 一次终态时间戳，并预写到 `job.FinishedAt`。
- `publishRunOutcome` 不再为 `RUN_FINISHED / RUN_ERROR` 单独额外取时，而是直接复用这次 pin 下来的终态时间。
- 随后的 `finishJob / stopJob / failJob` 若发现 `job.FinishedAt` 已经被当前 interactive run 预写，则继续复用该值，不再覆盖。这样 live 路径上的 `RUN_FINISHED.timestamp` 与 reload / reconnect 路径从 `GET /job/:id` 读到的 `job.finishedAt` 完全同源。

**修复 G：Continue 不再被晚到 HTTP 响应回写成 running**（前端状态竞态）

- `continueLoop` 仍会在点击后进入 loading，但 `/continue` 成功回调不再无条件执行 `setLoopStatus('running')`。
- 只有当前 run 已经通过 SSE `JOB_STARTED` 确认进入 loop running（`loopRunningRef.current === true`）时，HTTP 成功回调才允许把 sidebar 状态补成 `running`；若期间已经收到 `JOB_STOPPED / JOB_FAILED / JOB_COMPLETED`，则保持终态不被晚到响应覆盖。

**修复 H：running job reconnect 也用 `progress.results` 回填 Loop Session**（Sidebar 累计口径一致性）

- 之前 `syncJobState` 仅在 terminal 分支用 `job.progress.results` 回填 `loopSessions`；若 running job 断线期间漏掉 `ITERATION_COMPLETED / FAILED`，本地旧条目会一直停在 `running`。
- 现改为 running 分支也按 `sessionId + path` 复合键消费 authoritative results：已有条目会被修正为 `completed / failed` 并补齐 `durationMs / tokens / error`，本地缺失的 round 会 append。
- 这样 Sidebar 顶部与每个 Session 行都不会再把 stale running round 的 `startedAt` 继续叠加进实时耗时，避免出现断线重连后时长被放大的问题。

### 9.4 修复前 vs 修复后

修复前观察到的现象（作为历史记录保留）：

- Tool 卡片：live 数分钟 → 完成瞬间跳回几百毫秒（同时命中 A 和 B）。
- Assistant 气泡 / Deep Thinking 块：live 段被放大，但由于单调钳制，完成后停在被放大的值上（命中 A，不命中 B）。
- Loop Session Sidebar 顶部与每行的 running 滚动、ChatInput 底部本轮总耗时：live 段同样被放大（命中 A）。

修复后预期表现：

- Live tick 从服务端墙钟估计取临时终点，与 finished 段的 `endedAt - startedAt` 处在同一参考系，客户端时钟超前 / SSE 延迟 / ring buffer 重放都不再放大 live 数值。
- Tool 卡片徽章在 Processing → 终态翻转时复用同一实例，单调钳制生效，不再出现向下骤降。
- Assistant / Thinking / ChatInput / Loop Sidebar 的 live 滚动也回到真实量级，不再"3 分钟起步"。

### 9.5 常见误判澄清（非问题）

本节记录几类在 code review / 静态扫描中容易被误判为 bug 的点，明确它们在本方案语义下为何成立，避免后续反复讨论。

1) **`JOB_STARTED` 重放会不会把终态 job 误标为 running（含 `loopRunningRef` / `jobFinishedAt` 被清空的担忧）？**

- 终态 job 刷新/重连后，后端会在发出终态事件后清空 ring buffer，导致 SSE 无法重放旧的 `JOB_STARTED`（方案第 10 条已明确该语义）。因此「终态 job + 重放 `JOB_STARTED`」的前提不成立。
- 对于真正仍在 running 的 job，收到 `JOB_STARTED` 清空 `jobFinishedAt` 属于正确行为（该 run 尚未结束）。终态时间的权威来源是 `JOB_*` 终态事件或 `GET /job/:id` 的落盘字段；两者在方案第二章已要求同源（见 9.3 修复 C）。

2) **`publishIterationEvent` 的 `BaseEvent.timestamp` 为什么不要求与落盘字段同一次取时？**

- 方案第二章「同一次时钟读数」约束只覆盖会出现 *live vs reload* 对照的“语义边界”（message/thought/tool/job/run 的 start/end）。Iteration 事件的耗时权威值来自 `IterationResult.durationMs`（并随结果落盘），前端也以该字段作为 completed/failed 的最终展示口径；`BaseEvent.timestamp` 仅作为事件发生时刻的元数据，以及用于服务端墙钟估计的校准样本。

3) **`DurationBadge` 依赖项里包含 `startedAtList`（每次 render 新引用）是否是性能问题？**

- 该 effect 只做一次 ref 写入 + 一次 `requestAnimationFrame` 的延迟对齐（用于更新单调钳制上限），属于轻量操作；running 状态下本就存在 250ms tick，额外触发不会成为性能瓶颈。

4) **单调钳制会不会让 finished 值“偏大”，与后端权威值不一致？**

- 会存在极小概率：running 的 live 估计可能比后端最终 `endedAt - startedAt` 略大（通常是 tick 对齐与估计误差导致的 < 1s 级差异）。为了满足“绝不回跳”的硬约束，徽章在切到 finished 后仍以已展示过的最大值作为下限（`displayed = max(elapsed, maxShown)`）。
- 若产品口径要求 finished 必须严格等于后端权威值，则需要放弃“绝不回跳”或引入双值展示（例如同时展示 `server` 与 `displayed`）；这属于需求权衡，而非实现 bug。
