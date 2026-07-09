# 技术方案：Graph 直播重连丢消息根治（持久化 agent 段落事件 + 统一 seq + 重连桥接）

## 背景

Graph 工作流的一轮 Agent 回答如果是「文字→长思考→文字→长思考→文字」交替产出（实测某 case 单轮跑了 11 分钟），直播期间用户会看到：最后一条气泡只显示了第一段「Let me verify a few more things before finalizing.」，后面那一大段主体正文没有实时刷出来；**刷新页面后又能看到完整内容**。

定位到的根因：

- 当前 Graph 的 agent 流式内容（message/thought/tool 的 delta）**只写进内存 ring buffer，从不落盘**。落盘的 `events.jsonl` 只保留结构事件（instance 生命周期、edge、progress）。这是 `feature-2026-06-23-graph-run-storage-by-session.md`（存储治理方案）「第二层」刻意为之的结果——为了不让事件文件随轮数膨胀。
- 内存 buffer 有 GC：读者离开后有 30s 宽限期，超过 `graphReplayGapLimit` 或命中硬上限就回收旧事件。
- 于是一轮 agent 回答里，只要 SSE 在长思考静默期断开/重连，并越过 buffer 的 GC 宽限，那些错过的 `AgentMessageStart` + 后续 delta 就被 buffer GC 掉、盘上又从来没写过——**这些段落在直播端永久丢失**，无法通过重连补回。
- 只有当这一轮在 round 末尾把合并后的完整消息 flush 到 session 的 `messages.jsonl` 后，用户刷新走「历史消息 API」才能读到完整内容。这就是「直播漏、刷新全」的体感来源。

已有的修复 `d19d0c62`（reconnect 时用 `TEXT_MESSAGE_CONTENT` 兜底建气泡）只能处理「delta 到达了但 START 丢了」，处理不了本场景的「整段（START + 全部 delta）在静默期被 GC、盘上又没有」。

放大器：这个 session 被两个节点复用，前一个节点在主体正文产出的同一刻完成，follow-latest 会把活动视图切到下一个 session，旧 session 的补全时机被错过，进一步加剧了「看不到最后一段」。

## 目标

让 Graph 直播重连能**自愈**恢复错过的 agent 内容，不再依赖用户手动刷新；同时保证事件文件体积**不随轮数无限增长**（沿用存储治理方案的体积约束目标）。

## 与存储治理方案的关系（关键：会不会把 307MB 问题带回来）

`feature-2026-06-23` 那份方案解决的是 `events.jsonl` 涨到 **307MB / 10 万行**导致打开卡顿的问题。那 307MB 由**两块**构成：

1. **逐字符流式事件**（`agentMessageDelta` / `agentThoughtDelta` / `agentTool*`）：约 **35MB**。
2. **生命周期事件内嵌的全量 progress 快照**（每个 instance 事件都塞一整份 progress，随轮数二次方膨胀）：约 **184MB**（是大头）。

本方案对这两块的处理：

- **第 2 块（184MB 的大头）完全不碰**。它由存储治理方案「第一层」解决（生命周期事件去掉全量 progress 快照），本方案不改这条，**已修复的部分保持修复**。
- **第 1 块（35MB 的逐字符流）本方案只做「段落级」持久化，不逐 token 落盘**：
  - 只在 message / thought / tool 的**段边界**（`OnMessageEnd` / `OnThoughtEnd` / 工具结果）落盘一条「段落整合事件」，携带该段的完整累积文本。
  - per-token 的 `*Delta` 仍然只进内存 buffer、**永不落盘**（逐字打字机动画不受影响）。
  - 因此落盘量 ≈ 每段一条，而不是每个 token 一条，比原来的逐字符 35MB **小 1～2 个数量级**。段落文本与 session `messages.jsonl` 里的权威对话是同一份内容的稀疏 checkpoint，只多存了「按 seq 定位、可增量重放」这一层能力。
- **本方案顺带实现存储治理方案里规划但从未落地的「第四层：事件文件滚动上限」**：`events.jsonl` 超过阈值时自动滚动、保留最近尾部。即使极端长 loop，文件也稳定在阈值附近而非无限增长。

**结论：不会重现 307MB 问题。** 大头（progress 快照 184MB）本方案不动、保持已修复；重新落盘的 agent 内容是「段落级稀疏 checkpoint」而非「逐字符全量」，且有滚动上限硬约束体积上界。代价是相比存储治理方案「agent 内容完全不落盘」的极致状态，本方案会把 agent 内容以段落粒度写回磁盘（有界、可控），换取直播重连的可自愈恢复。

## 范围

**做**：
- 给 `GraphEvent` 增加持久化单调 `Seq`，由内存 buffer 统一分配（磁盘与 buffer 同一 seq 空间）。
- message/thought/tool 段边界落盘「段落整合事件」（含完整段落文本）；per-token delta 仍只进内存。
- 异步批量写盘（每批一次 fsync），buffer GC 用「已落盘水位」守卫，保证未落盘事件不被回收。
- 仓储层 seq 化读取（`ListEventsFromSeq` / `MaxEventSeq` / `FloorEventSeq`）+ 事件文件滚动上限。
- SSE 处理统一为「磁盘回放 `(startSeq, pivot]` → 无缝接内存 live-tail」的桥接；SSE `id` 用统一 seq。
- 前端 410 时重建 SSE 流、reconcile 覆盖「刚完成的会话」、段落整合事件的渲染。

**不做**：
- 不改 Graph 执行语义（调度、分支、循环、会话血缘、resume 业务规则不变）。
- 不改 Agent 节点对话的权威存储位置（继续存 session `messages.jsonl`）。
- 不改存储治理方案「第一层」的生命周期事件去 progress 快照逻辑（大头保持已修复）。
- 不动 Loop 引擎。
- 不做存量超大 run 的历史数据迁移（沿用手动清理入口）。

## 核心机制：统一 seq 空间

给 `GraphEvent` 增加持久化单调 `Seq`，由内存 buffer 的 `Publish` 在 `b.mu` 下统一分配（每个 publish 事件都 stamp，含 per-token delta）。buffer 创建时从磁盘最大 seq 播种，跨进程重启延续单调。

- **磁盘**只保存 seq 的稀疏子集（段落 checkpoint + 结构事件）。
- **内存 buffer** 是稠密全集（含 per-token delta）。
- 两边同一个 seq 空间，重连桥接因此可无缝拼接：**磁盘补 `(startSeq, pivot]` 的已落盘段落 → buffer 接 `> pivot` 的实时事件**，无缝、无重、无 410（除非早于磁盘滚动下限）。

## 方案设计（分区改动）

### A. 模型 —— `types/model/graph.go`

- `GraphEvent` 增加 `Seq uint64`（`json:"seq,omitempty"`）。`ID`（`NewGraphEventID` 随机串，用于身份/审计）不变，与 `Seq`（每 run 单调排序）正交。
- 前端 `web/src/types/graph.ts` 的 `GraphEvent` 增加 `seq?: number`（客户端实际靠 SSE `id:` 帧续传，此处仅完整性/健壮性）。

### B. 内存 buffer —— `services/graph/event_buffer.go`

- `newGraphEventBuffer(runID, seedSeq, persist)`：`nextSeq = headSeq = persistedSeq = seedSeq`，保存 persist 钩子。`SnapshotSeq()` 返回 `nextSeq`（重启后正确等于磁盘尾，新订阅从真实尾部开始）。
- `Publish`：在 `b.mu` 下 `nextSeq++`、**stamp `event.Seq`**、入 ring、并调用 `persist(event)`（钩子内部按类型决定是否落盘）。锁内入队保证「入队顺序 == seq 顺序 == 内存顺序」。
- **GC 守卫（正确性关键）**：`gcLocked` 里 `releaseFloor = min(minCursor, persistedSeq)`，用 `releaseFloor` 而非裸 `minCursor` 决定释放，确保**未落盘事件永不被 GC**——这样桥接才能保证 `(startSeq, pivot]` 全在磁盘。新增 `SetPersistedSeq(seq)`：异步写盘完成后回调，推进 `persistedSeq` 并触发一次 GC。硬上限 OOM 保护逻辑保留。
- 新增 `SubscribeBridged(startSeq) (reader, pivot, needReplay, err)`（全程 `b.mu` 下，headSeq 不漂移）：
  - `startSeq >= headSeq`：buffer 全包，订阅在 `startSeq`，`needReplay=false`（快路，等同现状）。
  - `startSeq < headSeq`：订阅在 `headSeq`，返回 `pivot=headSeq, needReplay=true`（磁盘补 `(startSeq, pivot]`）。
- 更新 `event_buffer_test.go` 全部构造点到新签名，新增播种/persistedSeq 卡 GC/SubscribeBridged pivot 用例。

### C. 段落整合事件 —— `services/graph/runtime.go`（graphEventHandler）

- `OnMessageEnd` / `OnThoughtEnd`：在 payload 带上该段**完整累积文本**（buffer reset 前的内容），标记为可落盘。`OnToolCallResult` 已带 content。
- per-token `OnMessageDelta` / `OnThoughtDelta` 仍只进 buffer、不落盘（保持逐字动画）。
- `isPersistableGraphEvent`：`AgentMessageEnd` / `AgentThoughtEnd` / `AgentToolStart` / `Result` / `End` + 全部结构事件 → 落盘；per-token `*Start` / `*Delta` 和 `AgentTokenUsage` → 不落盘。
- `appendEvent` / `appendHookEvent` **移除直接 `runRepo.AppendEvent`**，改由 buffer 的 persist 钩子统一落盘（保证 seq == 磁盘顺序）。

### D. 异步批量写盘 —— 新增 `services/graph/event_writer.go`

- 每 run 一个单消费者 writer：从 channel 攒批（≤256 条或 50ms），一次 `AppendEvents`（一次 fsync），再 `buf.SetPersistedSeq(lastSeq)`。
- `eventBuffer(runID)` 创建时先 `MaxEventSeq` 播种、再建 writer + buffer；`removeBuffer` / `MarkTerminal` 时 flush 剩余。

### E. 仓储 seq 化 + 滚动上限 —— `repository/graph_run.go`

- `AppendEvents(runID, []*GraphEvent)`：一次 `JSONLAppendLine` 多行 = 一次 fsync（复用现有模式，持 per-run 锁）。`AppendEvent` 变为单元素包装。
- `ListEventsFromSeq(runID, afterSeq, uptoSeq, limit)`：按 seq 过滤（磁盘严格 seq 升序，可提前 break）；legacy 无 seq 行按行号赋合成 seq。
- `MaxEventSeq` / `FloorEventSeq`：读末行 / 首行 seq（floor 可缓存到内存 map，避免每次连接一次读）。
- **滚动上限**：`AppendEvents` 后 `FileStat`，超阈值（如 32MiB）则在锁内用 `AtomicWriteFile`（rename 语义、读侧不撕裂）重写、保留最近尾部（如 16MiB / 最近 N 行）、更新 floor。seq 化读取对行号漂移免疫，触发时打日志（run、触发体积、丢弃行数、新 floor）。

### F. SSE 处理统一 —— `cmd/web/handler/graph.go`（`JobGraphRunEvents`）

用一个桥接替换 live/disk 分叉（删 `graphRunEventsReplayFromDisk` 的行号逻辑，保留 idle keep-alive 尾部为共享 helper）。`startSeq` 全程语义 = seq：

- **新连（`startSeq==0`）**：有 live buffer → remap 到 `SnapshotSeq`（tail）直接实时；无 buffer → **结构-only** 磁盘回放（会话由历史 API 补），再 replay-then-idle。
- **重连（`startSeq>0`）**：`SubscribeBridged`：
  - `needReplay=false` → 直接 live-tail。
  - `needReplay=true` → 先分页回放磁盘 `(startSeq, pivot]`（**含段落 checkpoint**，`structuralOnly=false`），再接 live-tail（`>pivot`）。无缝（盘止于 pivot，buffer 起于 pivot）、无重（区间不交）。
  - 无 buffer → 回放 `(startSeq, max]` 后 idle keep-alive（terminal / 重启后恢复）。
- **410 仅当** `startSeq < FloorEventSeq` 且 buffer 也无法服务（真不可恢复）；前端转历史 reconcile。
- SSE `id = event.Seq`。`graphReplayMaxEvents=2000` 由「硬截断」改为「分页页大小」，桥接分页直到 pivot。
- 边界精确性：磁盘上界含 `<= pivot`，buffer 严格 `> pivot`，pivot 那条恰好由磁盘发一次；`persistedSeq` GC 守卫保证 `pivot = headSeq <= persistedSeq <= diskMax`，桥接区间必可覆盖。

### G. 前端 —— `web/src/hooks/useJobChat.ts` + `sse-client.ts` + `translateGraphEvent.ts`

- `onResumePointGone`（410 回调）：`reconcile()` **后重建 SSE client**（`initialLastEventId:'0'` 从 tail 续），不再让流「死掉」。`connectUntilReady` 已会重置状态，caller 直接重连即可。
- `reconcile`：在 follow-latest 切换前记录 `prevActive`，切换后对 `prevActive` 与新 active **都** reload，避���刚完成的会话被 follow-latest 切走后没补全。
- `translateGraphEvent`：`AgentMessageEnd` / `AgentThoughtEnd` 若 payload 带 `content`（回放整段场景），先合成一条 `TEXT_MESSAGE_CONTENT`（**set 全量**，缺气泡则建，沿用 `d19d0c62` 思路）再 `TEXT_MESSAGE_END`；直播场景（无 content、delta 已流）只发 END。同 messageId 去重由现有 `mergeMessages` / status 守卫覆盖。

### H. Legacy 兼容

无 seq 的旧 `events.jsonl` 行：读取时按行号赋合成 seq；升级后首次 append 从 `MaxEventSeq`（= 旧行数）续号，连续无歧义（append-only 决定文件不会「新在前、旧在后」）。无需数据迁移。

## 落地顺序

1. 模型 `Seq` + `Publish` stamp（无行为变化，验证 seq 流到 SSE id）。
2. 仓储 seq 层（`AppendEvents` / `ListEventsFromSeq` / `Max`·`FloorEventSeq` + legacy 合成 seq）。
3. buffer 播种 + persist 钩子 + `persistedSeq` GC 守卫 + 异步 writer。
4. 段落整合事件 + `isPersistableGraphEvent` 翻转 + 移除生产者直写。
5. 滚动上限。
6. `SubscribeBridged` + 回放 API seq 化 + `structuralOnly` 模式。
7. SSE 处理统一（用户可见修复落地）。
8. 前端重建-on-410 + reconcile-just-finished + translate 整段。

1～4 一起做（deltas 以段落粒度持久化、统一 seq），6～7 作为第二个内聚改动（重连桥接），5 / 8 收尾硬化。

## 风险

- **写盘量 / fsync**：段落级 + 异步攒批（每批一次 fsync）+ 滚动上限，压力远低于逐 token；极端突发 writer 落后由 `persistedSeq` GC 守卫兜底（buffer 不会跑到磁盘前面），最坏对生产者短暂背压（单用户可接受，优于丢持久化）。
- **滚动上限调参**：过小 → 410 变多 → 更多走历史 reconcile（正确但略卡）；过大 → 磁盘增长。需保证磁盘 floor 常老于 buffer headSeq，使盘 + buffer 有重叠区。
- **顺序 / 锁**：seq 必须在 `Publish` 的 `b.mu` 内分配并入队 writer，禁止在此顺序外落盘（故移除生产者直写）；`persistedSeq` GC 守卫不可省，省掉即重现「buffer GC 了但盘上没有 → 桥接出现空洞」。
- **重写并发读**：依赖 `AtomicWriteFile` rename + seq（非行号）读取；审计确认唯一按行号读的 `graphRunEventsReplayFromDisk` 已改为 seq。
- **前端 410 重建循环**：重建从 tail 续，tail 恒可订阅，不会立即二次 410（除非 run 被删，此时 effect cleanup 拆除）。

## 可观测性

- 段落整合事件落盘处按类别记录写入量与 seq，确认逐 token delta 确实不落盘。
- SSE 桥接触发时记录 `startSeq` / `pivot` / 是否 needReplay / 磁盘回放条数，便于排查重连一致性。
- 滚动上限触发打日志（run、触发体积、丢弃行数、新 floor seq）。
- 410 发生时记录（run、startSeq、floor），观测是否滚动阈值过小。

## 验收标准

- **直播重连自愈**（用户手工测试 + E2E）：起一个含长思考 Agent 节点的 graph run；流式中断开 SSE 并等过 30s GC 宽限；重连（带 Last-Event-ID）→ 走桥接、整段补出错过内容、随后接实时，**无需刷新**。
- **410 下限恢复**（单测 / 手工）：测试构建里调小滚动阈值，用过期 seq 重连 → 410 → 前端 reconcile + 重建、实时续上。
- **体积不重现 307MB**（用户手工测试）：连续跑上百轮 loop，`events.jsonl` 稳定在滚动阈值附近；生命周期事件仍不含全量 progress 快照（大头保持已修复）。
- **对话完整 / 节点状态正确**（E2E）：多轮循环结束后逐个打开各节点 session，历史完整；运行中与刷新后节点状态色、进度、轮次正确。
- **resume 正常**（E2E）：失败 / 暂停 / 步骤后停止的多轮 run 能正确续跑（resume 读状态快照而非事件流）。
- **seq 单调**（单测）：持久化 `Seq` 与 SSE 交付 seq 严格单调一致；`event_buffer_test.go` 播种 / GC 守卫 / 桥接 pivot；`repository` 批量一次 fsync / `ListEventsFromSeq` 含 legacy / 滚动重写后 seq 查询仍对；`runtime_test.go` 原「delta 不落盘」断言**反转**为「段落 checkpoint 落盘 + seq 单调」。
