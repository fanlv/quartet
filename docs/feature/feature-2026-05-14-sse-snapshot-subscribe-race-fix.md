# SSE 推送延迟 / 卡死修复方案

> 方案日期：2026-05-14
> 关联：`docs/feature-2026-05-13-sse-event-buffer-detail.md`（事件 buffer 整体方案，本文是它的修补）

---

## 现象

页面打开后新事件不实时上屏，刷新页面才能看到最新消息；不刷新就要等很久才出现，看起来像"很久才到"。长任务刷新进入或终态后再发消息时，UI 偶发出现 `event buffer no longer contains seq=N` 的红条；并且 410 重试时**反复看到完全相同的 seq=N**（连续多次重试都说 `seq=1818`，即使后端实际状态已经在变）——这是浏览器缓存了第一次的 410 响应。

## 三条根因

| 失败模式 | 触发条件 | 根因 | 由谁解决 |
|---|---|---|---|
| 后端日志 `subscribe gone startSeq=0` | 客户端首次挂载发出空 `Last-Event-ID` | snapshot/SSE 两个 effect 并发，第一次发 SSE 时 `lastEventSeqRef` 还没填好 | 修复二 |
| 后端日志 `subscribe gone startSeq=N>0` | 客户端带着正确的 `Last-Event-ID: N` 但 buffer 已 GC 越过 | snapshot 接口不挂 reader，`minCursor=MaxUint64` 时 GC 在 snapshot/subscribe 网络往返期间自由推进 | 修复五 |
| 前端反复看到同一个 `seq=N` 文案、后端日志却没有对应的 410 入口 | RFC 7231 把 `410 Gone` 列为可缓存，handler 没设 `Cache-Control` | 浏览器把第一次的 410 响应钉在 disk cache 里，重试请求根本没出浏览器 | 修复八 |

修复二、五解决"真 410"的两条路径，修复八解决"假 410"（缓存命中）。三者必须一起上线：缓存放大器没清掉，前两条修复的退路都被 disk cache 拦在浏览器内。

## 修复二（基础）：串行化 snapshot 与 SSE 订阅

当前实现里，"取 history（含 `lastEventSeq`）"的 effect 与"建立 SSE 连接"的 effect 是两个独立的 `useEffect`，**并发跑**且都依赖 `lastEventSeqRef.current`。第一次挂载时 `lastEventSeqRef.current` 多半还是空字符串，于是 SSE 连接发出空的 `Last-Event-ID`，服务端按 0 处理→已 GC 的 buffer 必然 410。

修复方向：

- 进入页面后，**SSE 连接的发起必须等 `loadHistory` / `syncJobState` 完成、且 `lastEventSeqRef.current` 已被写入正确值**。
- 实现层面通过状态标志位（"history 已就绪"）串起两个 effect，不引入新协议步骤——后端 `/job/:id` 已经返回 `lastEventSeq`，足够使用。
- `parseLastEventID("")` 当前行为不变（仍然映射为 0），但**前端不再把空字符串送出去**。这样空 `Last-Event-ID` 还是合法，只是不会出现在正常路径上。

附带：`useJobChat.ts` 内已有的注释（"initialLastEventId 在首次 mount 时是空"）说明这个问题开发者本来就识别到了，本修复只是把它真正落地。

## 修复三（基础）：410 fallback 升级为可重入 + 透传错误

当前代码：第一次 410 走 fallback；如果 fallback 内部再 410，只 `console.error('giving up')`，SSE 流就此死掉。

修复方向：

- 把 410 fallback 改写成"再 410 就再拉一次 snapshot 再连"，最多重试 **3 次**（首次 + 2 次重试）。
- 退避按 **200ms / 1s / 3s**（量级，不必锱铢必较）。
- **任何一次失败必须把服务端 410 响应体里的原文一字不漏地透传到 UI**——按项目规范要求，错误信息全量给用户显示，不做任何替换或裁剪。
- 取消"giving up"分支：要么仍在重试，要么已经打用户可见的错误。
- 重试期间不重置 `idle-watchdog`，避免它和重试互相踩。

注意：基础修复（修复二）做对之后，这条重试路径预期极少触发，主要用于兜底"客户端长时间断线、buffer 已 GC 越过客户端 cursor"这一类合法异常。

## 修复四（基础）：cleanup 关闭"当前在用的"SSE 客户端

当前 `useEffect` 的 cleanup 调用的是闭包里捕获的 `client.disconnect()`——只能关掉首次创建的 client。一旦 410 fallback 创建过新的 `fresh` client，`fresh` 在 unmount 时**不会被关闭**，留下幽灵 reader 卡在 buffer 里继续压住 `minCursor`。

修复方向：

- cleanup 改用 `eventSseRef.current?.disconnect()`，关闭"当前在用的"那个 client。
- 在替换 client 的那一刻**立即**把旧的 client `disconnect()`，不依赖 cleanup。
- 同样的引用收敛规则适用于所有未来的替换路径（不只是 410 fallback）。

## 修复五（基础）：410 时从 disk 重建消息列表

### 现象

修复二/三/四上线后，仍然能看到稳态的 `[sse] subscribe gone`，但这一次 `startSeq>0`（例如 `seq=1818`）。也就是说前端**已经按设计 seed 了正确的 `Last-Event-ID`**，但服务端 buffer 已经把 `seq=1818` GC 掉了。

### 根因

Per-job event buffer 的 GC 闸门由 `minCursor` 决定：当当前没有任何 active reader 时，`minCursor` 取无穷大，gcLocked 可以释放任何符合 round-end 条件的事件。snapshot 接口本身**不挂 reader**——它只读 `nextSeq` 和 job 状态。所以 snapshot 返回 → 网络往回走 ~50–500ms → 这一段时间里 buffer 没有 reader → 后续每一次 Publish 都会触发 GC，并发地推进 `headSeq`。等到前端 SSE 请求带着 `Last-Event-ID: 1818` 到达时，`headSeq` 可能已经越过 1818，命中 `startSeq < headSeq` → 410。

长任务、用户切换 tab、之前的 SSE client 已断开等场景下，buffer 长期没有 reader，这条 race 会被稳态触发。

### 修复方向（不动后端）

回到 buffer 的设计契约——**A 类 chunk 在 round 闭合后写入 `messages.jsonl`、B 类 state delta 的真相在 `job.json`/session meta 里**。换句话说：buffer 只在"事件已经持久化"之后才 GC 它。所以 `410 Gone` 的真正含义不是"数据丢了"，而是"数据已经落盘了，去 disk 拿"。

修复就只在前端做：

- **410 fallback 在重连之前先 reload history**——重新拉 `/sessions/:sid/messages`（jsonl 路径）把消息列表整体替换成 disk 上的最新版本。
- 已经在 buffer 里的 in-flight round chunk 不会丢：`SnapshotSeq()` 在有未闭合 round 时返回 `round_start - 1`，新的 SSE subscribe 从 round 起点重播这一轮的 A 类 chunk，message 处理器按 ID 去重把它们 merge 回重建后的消息列表。
- B 类 state（status/progress/title 等）由 `syncJobState`（已经在 410 fallback 里调）从 `/job/:id` 拿到。
- `pendingEventsRef`、`historyLoadedRef` 这些过渡状态由 reload 期间统一清空/重置，避免旧 SSE 事件落到刚 reload 的消息列表上。
- 多次 410 → 多次 reload，幂等。Backoff 200ms / 1s / 3s 兜底。

### 不再需要的设计选项

之前文档提过两条更重的方案，本期都不做，结论如下：

- **buffer 加 placeholder cursor + token**——给 buffer 增加额外状态机来"为 snapshot 调用保留 seq 不被 GC"。架构上把 buffer 拉成"gap 期间的 source of truth"，偏离原设计；token 机制本来是为了不可信客户端，Quartet 是单机自用工具用不上。
- **public / shareToken 路径的 placeholder 衔接**——上面那条不做，这条也跟着不做。

修复五的当前版本只在**前端**改 50 行，**后端零改动**，复用现有的 `loadHistory` 与 `syncJobState`。

### 与其它修复的关系

- 修复二把"首次挂载 seq=0 race"消除，留下来的就是"snapshot/subscribe 之间 buffer GC 越过 seq race"。
- 修复三的 410 重试预算（200ms / 1s / 3s × 3 次）继续生效；修复五在每一次重试里追加一步"reload history"，让重试不只是"换个 seq 重连"，而是"把可能丢的事件从 disk 补齐"。
- 修复七的 `[DONE]` 重订阅复用同一条 `attemptConnect` 路径，自动享受 reload 行为。
- 修复八（缓存禁用）是修复五的前提：没有 no-store，重试请求被浏览器拦在 disk cache 里直接返回旧 410 响应，reload 拿到的 fresh seq 也无从生效。

## 修复八（基础）：禁用 SSE / snapshot 端点的浏览器缓存

### 现象

修复五落地后，后端日志里的 `[sse] subscribe gone` 应该看到不同 seq 值随重试推进；但前端 console 里反复看到**完全相同的 seq=N**，且后端日志里其实没有对应那一连串重试请求的接入记录——请求根本没出浏览器。

### 根因

RFC 7231 把 `410 Gone` 列为可缓存的状态码，浏览器在没有显式 `Cache-Control` 时会按默认策略缓存它。SSE 端点 `/api/v1/job/:id/events` 的 410 响应、以及 snapshot 端点 `/api/v1/job/:id` 的成功响应，都没设 cache header。结果：

- 第一次真实 410 返回（含特定 `seq=1818` 文案）→ 浏览器 disk cache 命中
- 重试再发 GET 同 URL → 浏览器从 cache 直接取，不发出网络请求
- 用户看到 410 反复出现，文案永远是 `seq=1818`，即使后端实际状态早已变化

snapshot 端点同理：缓存里的 `lastEventSeq=N` 会被当成新值喂回 SSE subscribe，制造"幻觉" 410。

### 修复方向（不动协议）

- SSE 端点 `/api/v1/job/:id/events` 在 handler 入口处统一设 `Cache-Control: no-store` 与 `X-Accel-Buffering: no`，覆盖所有响应路径（200 流、404、410、5xx）。
- snapshot 端点 `/api/v1/job/:id` 同样设 `Cache-Control: no-store`——它返回的 `lastEventSeq` 是个时刻在变的 SSE resume 点，缓存哪怕 1 秒都会让 SSE subscribe 拿到陈旧 seq。
- 不影响 ETag / If-None-Match 这类显式缓存协商路径（Job 列表等仍按既有方式做）。

### 与其它修复的关系

- 修复八是修复五的"前置条件"：禁用缓存才能让 reload-from-disk 真正拿到服务端的最新状态。两者必须一起上线。
- 修复二/三/四/七 同样依赖请求能真实到达后端。不缓存能让所有这些恢复路径不被悄悄掉链子。

## 修复七（基础）：终态事件后 SSE 不重订阅，导致暂停后再发送消息收不到回包

### 现象

用户在 Job 运行过程中点了「暂停」（Stop），随后在输入框继续发送一条消息。后端确实收到了消息、把 Job 状态从 Stopped 切回 Running、并发出新的 Started/输出事件；但前端 UI 没有任何反应——既看不到流式输出，也看不到状态切换。刷新页面之后所有内容才补齐。

实际链路：

1. 服务端在 Job 终态事件（`JOB_COMPLETED` / `JOB_STOPPED` / `JOB_FAILED`）后立即写入 `[DONE]` 并关闭 SSE 流——这是事件 buffer 设计里的"终结点"语义。
2. 前端 SSE Client 把 `[DONE]` 视为「正常结束」，不会触发自动重连；外层 `useEffect` 的依赖项也不包含任何"Job 终态"信号，所以它不会重新发起一次新的订阅。
3. 用户后续通过 `SendMessage` 把 Job 拉回 Running 时，新的事件被 publish 进 buffer 但**没有任何前端订阅者**在听。事件留在 buffer 里直到被 GC 回收。

也就是说：服务端语义把 `[DONE]` 当作"本轮跑结束"，但前端把它误解成了"这条流彻底关闭"。这一差异会一直生效，直到用户手动刷新页面、把 SSE 重新建立起来。

### 修复方向

把"终态后是否重订阅"这件事的语义统一到服务端：`[DONE]` 只意味着**当前这一轮跑**结束，并不代表**当前页面对这个 Job 的关注**结束。

具体做法：

- 前端 SSE 的 `onComplete` 触发时（即收到 `[DONE]`），不再仅仅 resync snapshot 就完事，而是顺势重新发起一次 SSE 订阅。
- 重订阅复用既有的"先 snapshot 再 subscribe"恢复路径：
  - 先打一次 snapshot，把 `Last-Event-ID` 推到刚才那条终态事件之后——再订阅时正好从"未来事件"开始等。
  - 把当前的 SSE Client 关闭，再创建一个新的 Client，避免新旧两条流并行。
  - 保留原来的 410 / 认证拒绝 / 重试预算等所有兜底分支，重订阅后这些路径自动生效。
- 重订阅的副作用是：即使 Job 真的完成了（用户也不再发消息），页面会留一条空闲的 SSE 连接，靠 keepalive 维持。代价是一条空连接，回报是「暂停 → 发送 → 立刻看到回包」的体验闭环——值得。
- 边界情况：
  - 用户在 `[DONE]` 之后立即关闭页面 / 切换到其他 Job：`useEffect` cleanup 会把当前 client 关闭；重订阅前需要有"已被替换 / 已取消"的闸门，保证不会再复活一条流。
  - 同一时间多次终态（例如 stop 之后立刻又 fail）：每次 `[DONE]` 都触发一次重订阅，但因为有"先关闭旧 client、再创建新 client"的固定顺序，最终只会留下最后一条流。
  - 重订阅本身失败（snapshot 410、网络断）：直接落入既有的 410 兜底重试 + 错误透传路径，行为与首次订阅完全一致。

### 与其它修复的关系

- 修复二解决的是「页面打开时 snapshot 与 SSE 并发跑、Last-Event-ID 没填好」的首次挂载问题。
- 修复七解决的是「页面已经在跑、Job 进入终态后 SSE 死掉」的运行期问题。
- 两者用的是同一条「先 snapshot 再 subscribe」的恢复路径，只是触发点不同：修复二在初始 snapshot 就绪时触发，修复七在收到终态 `[DONE]` 时触发。
- 修复三 / 四 提供的 410 兜底与 client 替换关闭语义在修复七的重订阅里完全复用，无需重复实现。

## 验收标准（量化）

修复二/三/四上线后：

- 单 mount 周期内 `[sse] subscribe gone startSeq=0` 日志条数 **= 0**（即使是首次挂载）。
- 持续推送中（如 LLM 流式输出），从服务端 `Publish` 到前端 `onEvent` 的 P95 延迟 **≤ 1s**。
- 反复进出页面 100 次后，单 job buffer `Stats().Readers` 与 `Events` 不出现单调上涨（无 client / reader 泄漏）。
- 410 fallback 触发后，UI 可见的错误信息 **必须等于** 服务端响应体内容（不替换、不裁剪、不沉默）。

修复五上线后：

- 长任务（≥ 4 小时、上千事件量级）刷新页面时，即使 `[sse] subscribe gone startSeq=N>0` 触发 410，前端在 retry 预算内自动恢复并补齐 disk 上的消息，**用户视角无错误条**。
- 410 → reload history → 重订阅这条路径必须保证：reload 完成前 SSE 事件被正确缓冲或丢弃，不会把"旧 SSE 事件"应用到"刚从 disk 重建的消息列表"上。
- in-flight round 触发的 410 reload：reload 后的新 SSE subscribe 从 `round_start-1` 重播这一轮所有 A 类 chunk，message 处理器按 ID 去重把它们 merge 回消息列表，最终一致。

修复七上线后：

- 任意 Job 在 Stop / Complete / Fail 后，紧接着发送一条新消息，前端 UI 能在不刷新的前提下看到新一轮的 Started 事件、流式输出与终态事件，端到端延迟与首次发送一致。
- 同一页面多次「暂停 → 发送 → 暂停 → 发送」往返 50 次，单 job buffer `Stats().Readers` 不出现单调上涨——重订阅必须严格遵守「关闭旧 client → 创建新 client」的顺序。
- Job 进入终态后页面保持不操作 5 分钟，`/sse keepalive` 路径仍持续工作（用以保护这条空闲订阅），不会被超时中断。

## 落地清单

基础修复（二/三/四）：

- 前端串起 history-load 与 SSE-connect 两个 effect，确保 SSE 连接发出时 `lastEventSeqRef.current` 不为空。
- 410 fallback 改为最多 3 次、退避 200ms / 1s / 3s；最终失败展示服务端 410 响应体原文。
- `useEffect` cleanup 改用 `eventSseRef.current?.disconnect()`；任何替换 client 的路径都在替换瞬间关闭旧 client。
- 后端无改动。

基础修复（七）：

- 前端 SSE 收到 `[DONE]` 时，复用既有的「先 snapshot 再 subscribe」恢复路径重新订阅，使 Stop / Complete / Fail 之后再发送的消息也能实时回流。
- 重订阅必须保留「已取消 / client 已被替换」两道闸门，避免 cleanup 后还复活流。
- 后端无改动；语义上由前端把 `[DONE]` 严格定义为"本轮结束"而非"流彻底关闭"。

基础修复（五）：

- 410 fallback 在重连之前调用 `loadHistory` 重建当前 active session（loop 模式下重建所有 session）的消息列表，把可能被 buffer GC 掉的事件从 `messages.jsonl` 补回。
- reload 期间 `pendingEventsRef` 清空、`historyLoadedRef` 重置；reload 完成后再 flip 回 true 并继续往下走 SSE subscribe。
- 后端无改动；`Subscribe` 的 410 行为本身就是对的——"数据已落盘，请去 disk 拉"。

基础修复（八）：

- SSE 端点 `/api/v1/job/:id/events` 在 handler 入口处设 `Cache-Control: no-store` 与 `X-Accel-Buffering: no`，覆盖所有响应路径。
- snapshot 端点 `/api/v1/job/:id` 同样设 `Cache-Control: no-store`，避免 `lastEventSeq` 被浏览器或中间代理缓存。
- 与修复五配套上线，否则 reload-from-disk 取回的 fresh seq 会被浏览器 cache 拦住。

文档：

- 落地完成后回写 `docs/feature-2026-05-13-sse-event-buffer-detail.md` §2.3 / §2.5：现实现是"前端必须先完成 snapshot 拿到 `lastEventSeq` 再发起订阅；如果 buffer 在 snapshot 与 subscribe 之间被 GC 越过，前端从 disk 重建消息列表后再续订"。
- `docs/arch/message-to-sse-pipeline.md` 中受影响的描述同步更新。
