# SSE 消息流重构方案

> 方案日期：2026-05-13
> 说明：本文件已合并原 `feature-2026-05-13-sse-event-buffer-detail.md` 与 `feature-2026-05-13-sse-event-buffer-redesign.md`
> 关联：`docs/arch/message-to-sse-pipeline.md`（落地后需同步重写）

---

## 架构概览

### 背景与目标

Quartet 当前 workload 下，单 job 经常运行数十分钟到数小时、产生数万到十几万条事件。在该规模下对消息流的硬性要求是：

1. **服务端发出的消息尽量送达**——网络写入失败不等于消息丢失，必须有续传/恢复路径
2. **客户端最终能看到完整序列**——任何短暂的网络抖动、tab 切后台、重连，都不应造成事件丢失
3. **消息严格有序**——不允许出现乱序，事件流是单调时间线
4. **支持长任务大事件量**——单 job 几万到几十万事件量级下系统稳定不退化

当前链路已稳定暴露三个根问题：

- **终态事件 5s 超时被丢弃**（订阅者通道满 + 网络写阻塞）
- **重放缓冲区与订阅者通道容量都被真实负载击穿**（持续截断、重连重放时静默丢弃最旧部分）
- **非终态事件在慢消费者下静默丢失**（流式 token、tool 调用进度等实时事件）

这些问题的根因不在参数，而在架构假设本身。

### 现有架构的根本问题

现有事件总线同时承诺了两件互相冲突的事：

1. **不反压 producer**：job loop 不能被慢客户端拖住
2. **重连可恢复全部 live 中间态**：靠定长内存环形缓冲 + 快照兜底

在事件率持续高于消费率时，这两条承诺无法同时成立，系统会进入“接受丢失但不告诉前端丢了什么”的稳态。

当前物理结构本质上是三层独立有界容器串联：

```
[per-job 共享环形缓冲]  →  [每订阅者独立通道] × N  →  网络写入
       │                          │
       │                          ├── 通道满 → 静默丢非终态事件
       │                          └── 终态走 5s 超时 → 超时丢
       │
       └── 满了 → 截断最旧 1/4
                  → 重连重放撞通道容量 → 再次丢最旧
```

只要“为每个订阅者准备一个有限大小的队列”这个前提不变，“队列满了怎么办”就一定会落回丢弃决策。

### 新方案核心思路

新方案不是简单换数据结构，而是把 **state / 成品 / 增量** 三层职责彻底拆开：

| 层 | 数据 | 载体 | 谁读 |
|---|---|---|---|
| **state（当前是什么）** | job 状态、loop 进度、累计 token、迭代结果列表 | `job.json` | UI 首屏、Continue 续跑、列表页 |
| **成品（说了什么）** | 每轮 LLM 输入输出、工具最终结果 | `messages.jsonl` | UI 首屏、LLM context 装载、ACP drift 检测 |
| **增量（正在变什么）** | 流式 token / 工具流式输出 / 状态变更通知 | **per-job in-memory buffer** | SSE 订阅者 |

关键论断：**UI 首屏完全由 `job.json` + `messages.jsonl` 重建；buffer 只服务首屏之后的实时增量。**

buffer 内部再分三类事件：

- **A 类**：buffer 是唯一真源——进行中轮次的流式文本、思考、工具过程、`TOOL_CALL_STITCHED`
- **B 类**：state 真源在 `job.json`——`JOB_*`、`ITERATION_*`、`RUN_*`、`TOKEN_USAGE`
- **C 类**：前端不消费，直接删除——`ARTIFACT_*`、`STATE_SNAPSHOT`

对应 GC 原则：

- **A 类**：轮次结束事件已发布 + `messages.jsonl` 已写入该轮次 + 所有活跃订阅者 cursor 越过 round end → 整轮释放
- **B 类**：所有活跃订阅者 cursor 越过该事件 → 立即释放
- **无订阅者时**：B 类 publish 即释放，A 类在 round_end 到来且轮次已落盘后整轮释放

### 新旧方案的本质差异

| 维度 | 现有 | 新方案 |
|---|---|---|
| 事件存储 | 共享缓冲 + 每订阅者独立队列 | 共享 buffer，订阅者只持 cursor |
| 投递动作 | publisher 主动 push | publisher 只追加 + 唤醒；订阅者自己 pull |
| 丢弃决策点 | 三层（环形截断、通道满、重放超容量） | 零；GC 只回收已被 state/成品吸收的内容 |
| 慢订阅者影响 producer | 是 | 否 |
| 是否驱逐慢订阅者 | 是 | 否 |

因此三个线上问题的物理基础都会消失：

- 不再有“每订阅者有界通道”，所以不再有通道满导致的非终态静默丢弃
- 不再有终态 5s 特殊投递路径，所以不再有终态超时丢失
- 不再依赖固定重放窗口，而是依赖分类 GC，所以不会被真实负载持续击穿

### 与现有持久化的边界

新增 in-memory buffer **不取代任何现有文件**：

- `messages.jsonl`：仍然负责对话历史成品
- `job.json`：仍然负责 job 当前状态快照
- `user_input/*.jsonl`、session `meta.json`、`summary.json`：职责不变
- **in-memory event buffer**：只负责进行中过程与实时通知

同时新增一条隐性约束：**state 必须在对应事件发布之前或同时落盘。** 也就是：

- `ITERATION_STARTED` 前，`Progress.CurrentPath` 已落 `job.json`
- `ITERATION_COMPLETED / FAILED` 前，`Progress.Results[N]` 已落 `job.json`
- `JOB_*` 终态事件前，`status` / `finishedAt` 已落 `job.json`
- `TOKEN_USAGE` 前，累计 token 已落 `job.json`

这样刷新页面时，客户端读到的 `job.json` 不会落后于事件流。

---

## 1. 核心组件设计

### 1.1 Job Event Buffer（in-memory）

每个 job 一份内存 buffer。承担两件事：A 类——进行中轮次的过程时间线唯一真源；B 类——结构性 delta 的实时通知通道（state 在 `job.json`，buffer 不长期保留）。

**术语**

- **轮次（round）**：本文中"轮次"等价于一次 **ITERATION**——从 `ITERATION_STARTED` 开始，到 `ITERATION_COMPLETED / FAILED` 结束。这一段时间内的 thought、text、tool call、工具结果都视为同一轮次的产物，整段产物（含可能的多次 tool call 子循环）作为一个整体在轮次结束时落入 `messages.jsonl`。round_start / round_end 即指 `ITERATION_STARTED` 与 `ITERATION_COMPLETED / FAILED` 的事件序号。
- **轮次边界对所有运行入口生效**：Loop step、Shell step、Interactive (`SendMessage`) 三种入口都必须以 `ITERATION_STARTED` 打开轮次、以 `ITERATION_COMPLETED / FAILED` 关闭轮次。Interactive 不写入 `Progress.Results`，但仍要发出轮次结束事件——只有这样 buffer 才能把进行中标记翻回"无在飞轮次"，A 类 chunks 才能在 cursor 越过 round_end 后被回收，`SnapshotSeq` 才会回到"取 tail"分支不再让重连客户端重读已经写入 `messages.jsonl` 的内容。
- **中断路径同样要关闭轮次**：用户 Stop、ctx 超时、运行中的 panic 等所有非正常退出路径，都不能直接 abort 后让 round 悬挂。Stop / Cancel / DeadlineExceeded / panic recovery 触发的中断在丢出 `JOB_STOPPED / FAILED` 终态事件之前，必须先发出本轮的 `RUN_ERROR` + `ITERATION_FAILED`，让 buffer 把 `openRoundID` 翻回空。终态事件本身不关闭轮次（`JOB_*` 在 buffer 中按 B 类处理，`isRoundEnd=false`），如果中断路径不补这对结束事件，`MarkTerminal` 只是把 GC 暂时冻结起来掩盖问题——后续 `Continue` 调用 `ResumeGC` 重新启用 GC 时，旧轮次仍然 `closed=false`、`endSeq=0`，永远满足不了 `round.closed && minCursor >= round.endSeq` 的回收条件，A 类 chunks 永久滞留。更糟的是 `openRoundID` 在 `MarkTerminal` 后并不会被清空，新 run 启动时第一条 `JOB_STARTED` 会被错误归入旧轮次的 `roundID`，进一步污染回收边界。中断路径只发事件、不写 `Progress.Results`，让 Continue 仍然能在同一 path 重跑——panic recovery 也只发事件、不写 `Progress.Results`，否则下一次 Continue 会把这条凭空多出来的失败计入 `FailedCount`。

**职责**

- 接收所有发布调用，串行追加、分配单调递增的序号
- 提供"从某序号之后顺序读取"的能力
- 提供"被新事件唤醒"的信号
- 跟踪轮次边界（轮次开始序号、轮次结束序号），供 A 类 GC 使用

**关键约束**

- **单写者**：所有发布通过一个串行入口完成，保证全序与序号单调
- **共享存储**：buffer 是共享的 append-only 序列，所有订阅者读同一份，不为每订阅者另存一份
- **append-only**：写入后永不修改，读取无需加锁
- **错误透传**：极端情况下追加失败（内存分配失败）原样返给发布方，不吞错

**事件分类与差异化 GC**

| 类别 | 事件 | 真源 | GC 条件 |
|---|---|---|---|
| **A 类** | `TEXT_MESSAGE_*`（含 `external.isThinking=true` 的思考流，无独立 `THOUGHT_*` 类型）、`TOOL_CALL_*`（含 args/result 流）、`TOOL_CALL_STITCHED` | buffer | 对应轮次的 `*_END` 已发布 + `messages.jsonl` 已写入该轮次 + 所有活跃 cursor 越过 round end → 整轮 chunks 整段释放 |
| **B 类** | `JOB_*`、`ITERATION_*`、`RUN_*`、`TOKEN_USAGE` | `job.json` / 隐含在 `messages.jsonl` 末尾轮次 | 所有活跃 cursor 越过该事件序号 → 释放（轮次外立即物理回收，轮次内随所在轮次整轮回收）|
| **C 类** | `ARTIFACT_*`、`STATE_SNAPSHOT` | 无 | **彻底删除**：事件类型定义、publish 调用点、EventHandler 接口实现一并清掉，不入 buffer |
| **Transient 类** | `COMMAND_SYSTEM_MESSAGE`（slash command 反馈，如 `/ws`、`/help`、未知命令提示） | 无 | 不入 buffer、不分配序号、不参与续传——仅广播给当前活跃订阅者一次，刷新页面后不再重现 |

**Transient 类语义**：用于一次性、与 job state 无关、刷新页面**不应**重现的提示信息。典型场景是 slash command 的回显反馈：用户输入 `/ws workspace-foo` 后只在当时的会话里看到一条系统消息，刷新页面或断线重连后不需要、也不应该再看到。

实现上 transient 事件不进 append-only 序列、不占序号空间，对 buffer 内存与 GC 完全透明：

- 无活跃订阅者时 publish → 直接丢弃，零内存占用
- 有 N 个订阅者时 publish → 只挂到这 N 个 reader 的本地 transient 队列，由 reader 在下一次 Read 时透传给 SSE writer
- 不参与 cursor、不影响 `Last-Event-ID` 续传协议（事件 id 字段为空）

**B 类物理回收**

为保持 append-only 序列连续、避免 sparse 空槽，B 类释放按所在位置分两种：

- **轮次外的 B 类**（轮次间隙的 `ITERATION_*`、终态后的 `JOB_*`）：被所有 cursor 越过后立即物理回收
- **轮次内的 B 类**（如轮次进行中的 `TOKEN_USAGE`）：与所在轮次的 A 类 chunks 一起，在轮次关闭且所有 cursor 越过 round_end 后整轮回收

代码层面没有"标记可丢弃"中间态——轮次内事件统一在 `round.closed && minCursor >= round.endSeq` 时释放。

**无订阅者时的退化**：当活跃 cursor 集合为空时，"所有活跃 cursor 越过"空真成立——

- 轮次外的 B 类 publish 时若无订阅者 → 立即物理回收，零内存占用
- 轮次内的 B 类跟随所在轮次整轮回收
- A 类 chunks 仍按轮次累积，但 round_end 一到（叠加 `messages.jsonl` 写入条件后）立即整轮释放
- 后果：headless job 场景（Scheduled Task 后台跑、UI tab 已关）下 buffer 几乎不占内存
- 新客户端事后打开页面，走 snapshot 路径靠 `job.json` + `messages.jsonl` 就能拿到完整 state，不需要事件流回放

A 类 GC 安全性由 §2.4 写入顺序契约保证：buffer 看到 round end 时 `messages.jsonl` 已经写完该轮次，回收 chunks 不会让任何客户端"看不到这条 round"。

B 类 GC 不需等 `messages.jsonl`：state 已经同步落到 `job.json`（详见 §1.4），客户端任何时刻读 `job.json` 都能拿到等价或更新的状态。

GC 后 buffer 实际内容：

- 进行中那一轮的 A 类 chunks，以及夹在其中已被 cursor 越过、待整轮回收的 B 类
- 轮次外尚未被所有 cursor 越过的 B 类（追赶窗口）
- 轮次边界索引

**生命周期**

- 进行中 job：按 GC 规则维持 buffer，工作集稳定在 MB 量级
- 进入终态后：停止 GC，buffer 此刻剩余内容（最后一轮的 A 类 chunks + 轮次外 B 类追赶窗口）保留到 job 显式删除；早先轮次的过程已被 GC 回收，对话历史从 `messages.jsonl` 重建
- 终态 job 被再次激活（`Continue` 续跑、`SendMessage` 交互发送）：复用同一 buffer，把"已终态"标记翻回"GC active"，新一轮发布的事件按正常 GC 规则回收。慢订阅者仍由 minCursor 保护——已被订阅但未越过的事件不会被回收。`Start` 是唯一会重置序号空间的入口（`resetForRun` 创建新 buffer）
- job 显式删除：立即释放

**与磁盘方案对比省掉的东西**

- 文件格式与完整性标记
- fsync 节奏控制
- 启动时崩溃扫描截断
- 磁盘满反压

### 1.2 订阅者 Cursor Reader

每个 SSE 连接独占一个 reader。

**职责**

- 维护一个 cursor，表示"已确认成功投递的最后序号"
- 从 cursor + 1 开始按序读取
- 投递失败 → cursor 不前进，下一轮重读这一条
- 向 buffer 暴露当前 cursor，供 GC 计算"所有活跃 cursor 的最小值"

**两种状态**

- **追赶**：cursor 落后较远，连续读取并下发，直到追上当前 tail
- **实时跟随**：cursor 已对齐到 head，等待新事件信号唤醒，唤醒后再读"上次到新 tail"段

两种状态透明切换。

**关键约束**

- **慢订阅者天然背压**：慢只是 cursor 推进慢，对 producer 完全无影响
- **不再有"驱逐慢订阅者"概念**：仅在客户端真正断开时清理 reader，事件本体不丢
- **读多少推多少**：无窗口截断、无非阻塞丢弃
- **GC 等所有 cursor 越过**：慢订阅者自动让 GC 推迟，不会读到一半"事件被回收"

### 1.3 SSE Handler 契约重写

**写超时**

每次网络写都套写截止时间（30s 量级），超过即判定为僵尸连接。当前网络写没有超时，是终态事件丢失的真正根因。

**写失败的两种语义必须区分**

- **客户端真正断开**（broken pipe / connection reset / EOF）：清理 reader、关闭连接，cursor 不推进；客户端重连时按事件序号续传
- **写超时但连接未断**：直接判定为僵尸连接、关闭连接，由客户端通过 Last-Event-ID 续传。不再原地重试同一事件——TCP 写不可取消，超时后底层 goroutine 仍持有 SSE Writer，再启第二次写会在原写最终排出时把同一事件投递两遍

**核心是 cursor 不推进、事件不丢。**

**SSE 事件 id 字段携带序号**

每条事件的 SSE 事件 id 字段写入 buffer 序号，作为续传锚点。

**重连超出 buffer 范围 → 410 Gone**

客户端持有的上次事件序号不在当前 buffer 的有效区间 `[headSeq, nextSeq]` 时：

- **典型场景 1**：客户端离线时间过长，序号 < headSeq（已被 GC）
- **典型场景 2**：Job 被 `Start` 重启过，buffer 被 reset 为新的序号空间，旧客户端持有的序号 > 新 buffer 的 nextSeq —— 属于**不同的 buffer epoch**

两种情况服务端处理一致：

1. 服务端发现请求序号不在 `[headSeq, nextSeq]` 区间
2. 返回 410 Gone + 错误信息（按规范错误全量给用户显示）
3. 客户端走"重新拉首屏"路径——`messages.jsonl` + `job.json` + 当前 tail 序号 → 重新订阅

这条路径不丢消息：`messages.jsonl` + `job.json` 已覆盖完整 state 与成品，进行中那段从当前 tail 续上。

### 1.4 对 job.json 的写入要求

新方案对 `job.json` 提出**写入早于事件发布**的约束。这是 B 类 GC 安全性的前提：

| 事件 | 必须先落到 `job.json` 的字段 |
|---|---|
| `ITERATION_STARTED` | `Progress.CurrentPath` |
| `ITERATION_COMPLETED / FAILED` | Loop / Shell 路径：`Progress.Results[N]`（含耗时、tokens、error）。Interactive 路径例外：不写入 `Progress.Results`，轮次结束事件仅用于关闭 buffer round 与翻转 `openRoundID`（详见 §1.1 与 §6.1 #11） |
| `JOB_STARTED` | `Status = running` + `StartedAt` |
| `JOB_COMPLETED / STOPPED / FAILED` | `Status = 终态` + `FinishedAt` + `Error` |
| `TOKEN_USAGE` | 实现层放宽：流式期间不强制同步落盘，由 `ITERATION_COMPLETED / FAILED` 在 iteration 边界一起持久化 `Progress.Results[N].TokenUsage` 与累计值。理由见下方 |

`RUN_ERROR` 当前仅作为 SSE 事件下发，没有对应的 session 级持久化字段——run 失败的语义已被随后的 `ITERATION_FAILED` / `JOB_FAILED` 写入 `job.json` 覆盖，这里不再单独维护一份 last-error。

**原则**：客户端任何时刻拉 `job.json` 都看到不晚于事件流的状态。这样刷新页面是安全的——不会出现"事件已发但 state 还没落"导致 UI 与 state 错位。

**`TOKEN_USAGE` 例外说明**：流式过程中 `TOKEN_USAGE` 可能每秒多次发出，每次都同步落 `job.json` 会带来不可接受的 IO 开销，且 token 计数本身不影响 GC 安全（B 类 GC 只看 cursor 推进，不读 state）。代价是流式中刷新页面，token 显示会回退到上一个 iteration 边界的值；下一个 SSE 事件到达后立即追上。这是显式的可观察延迟、非语义错误。

**`ITERATION_STARTED` 例外说明**：`Progress.CurrentPath` 的落盘是 best-effort——如果 `job.json` 写入失败，实现只在内存 `Progress.LastError` 留一条 breadcrumb 并继续 publish `ITERATION_STARTED`，不阻断整个 loop。理由是磁盘 IO 异常时让 loop 整体停转的代价远高于"刷新页面看到上一轮 path"的代价；下一次成功 save 时（通常是同一 iteration 末尾的 `record_iteration_result`）会自愈。代价是磁盘异常窗口内刷新页面，`Progress.CurrentPath` 可能落后一轮，UI 进度条短暂回退。这是显式的可观察延迟、非语义错误。

---

## 2. 加载与续传

### 2.1 加载场景对照

| 场景 | 数据源 | 订阅起点 |
|---|---|---|
| 首次打开 job 页 | `messages.jsonl` + `job.json` 快照 | 快照接口返回的 `lastEventSeq`（有进行中轮次时为 `round_start - 1`，否则为 buffer tail K） |
| 刷新页面 | 同上，重新拉快照 | 同上 |
| SSE 短暂断线重连 | **不重拉**快照 | 客户端记住的上次事件序号 |
| 重连超出 buffer 范围（410） | 走"重新拉首屏"路径 | 同首次打开 |
| 进入终态后回看 | 同首次打开 | 不订阅 SSE |

### 2.2 首屏的完整性

UI 首屏由 `job.json` + `messages.jsonl` 完整重建，不依赖任何事件回放：

- 对话历史 ← `messages.jsonl`
- loop 状态、进度、当前迭代、累计 token、迭代结果列表 ← `job.json`
- session 列表与元数据 ← `job.json` + 各 session 的 `meta.json`
- 进行中那一轮还没完成的内容 ← SSE 订阅之后的事件流

即使 SSE 一直连不上，UI 也是对的，只是不会自动更新。

### 2.3 cut-over 一致性

快照接口必须返回一组值：消息列表 + 订阅起点序号 `lastEventSeq`。

- 消息列表来自 `messages.jsonl` 与 `job.json`
- `lastEventSeq` 的取值规则：
  - **若 buffer 中存在进行中轮次** → `lastEventSeq = round_start - 1`，让客户端从该轮次第一条事件（含 `*_START`）开始订阅，能完整续接进行中那一轮的打字过程
  - **若没有进行中轮次**（job 在轮次间隙或终态）→ `lastEventSeq = K`，K 是该次读取时 buffer 的 tail 序号
- 这一组值由"快照原子方法"在同一个调用里取出：先取 buffer 的当前序号，再读 jsonl/job.json。配合 §1.4 的"state 早于 publish"契约，这样能保证返回的 state 不老于 `lastEventSeq`（state 已包含了所有 ≤ lastEventSeq 的事件对应的更新，可能还包含 lastEventSeq+1 的更新——这种情况下事件会再触发一次 idempotent 状态更新，不影响正确性）；如果反过来"先读 state 再取 seq"，则可能在两步之间发生发布，导致客户端从 seq+1 订阅但永远收不到那条事件，UI 与 state 错位。

客户端拿到快照后以 `lastEventSeq` 为 Last-Event-ID 起点订阅，服务端从 `lastEventSeq + 1` 开始推。

**不丢**：
- 已落盘的轮次 → 客户端从 `messages.jsonl` 拿到完整成品
- 进行中那一轮 → 客户端从 `round_start` 开始拿到完整 chunks（含 `*_START`、所有已发出的 token、思考、工具流式输出），UI 能正确续接动画，不会出现"半句话冒出来"的视觉断裂
- `lastEventSeq + 1` 之后产生的新事件持续推送

**不重**：
- 没有进行中轮次时 `lastEventSeq = K`，K 及之前的事件已被 `messages.jsonl + job.json` 覆盖，不会与快照重复
- 有进行中轮次时 `lastEventSeq = round_start - 1`，进行中轮次的 chunks 此刻**只**在 buffer 流中存在（`messages.jsonl` 该轮次尚未写入），不会与消息列表重复

### 2.4 写入顺序契约

新 buffer 与 `messages.jsonl` 在每个轮次完成时表达同一事实，写入顺序契约同时约束 cut-over 一致性与 A 类 GC 安全性：

> **先写 `messages.jsonl`（轮次成品落盘），再向 buffer 发轮次结束事件。**

顺序反过来会破坏两件事：

- **cut-over 不一致**：客户端可能在快照看不到这个轮次（`messages.jsonl` 还没写），但订阅起点 K 又跳过了结束事件，UI 会"卡在打字一半"
- **GC 不安全**：buffer 看到 round end 后会回收该轮次的 chunks；如果 `messages.jsonl` 还没写完，就会出现"buffer 已无、`messages.jsonl` 也无"的窗口，重连客户端两边都拿不到

类似地，B 类事件的写入顺序契约：

> **先更新 `job.json` 的对应字段，再向 buffer 发事件。**

详见 §1.4。

**Panic recovery 例外**：`runLoop` / `runInteractive` 顶层 `recover()` 触发的轮次关闭路径不满足"先写 `messages.jsonl`"前提——panic 意味着 `RunIteration` 没有正常返回，jsonl 大概率没有完整写入。这条路径仍然必须发出 `RUN_ERROR` + `ITERATION_FAILED` 把 round 关掉（否则 §6.1 第 12 条所述的 `openRoundID` 滞留 + A 类 chunks 永久泄漏问题会重新出现），属于 §2.4 契约的合法例外。安全性由 §4.1 崩溃等价语义保证：进行中那段事件即使保住也无意义，业务状态会被 Continue 在同一 path 重跑时重新生成；无订阅者时 GC 立即整轮回收也不影响任何客户端。

### 2.5 短暂重连路径

短暂断线重连不走快照，直接用客户端记住的上次事件序号续传：

- 重连流量极小（只补缺口）
- 不会出现"重新渲染整个对话"的视觉跳动
- 只要客户端落后没有跨过一个轮次的 GC 回收点，buffer 就有它需要的事件

由于浏览器重连延迟一般在秒级、写超时阈值在 30s 量级，**正常断线重连基本不会跨过轮次回收点**。

---

## 3. SSE 协议层与前端客户端实现细节

### 3.1 服务端协议

URL：`GET /api/v1/job/:jobId/events`（公开链接版：`GET /api/v1/public/job/:jobId/events?shareToken=...`）

每条事件输出格式：

```
id: <buffer 序号>
event: message
data: <JSON 序列化的 model.Event>

```

- `id:` 字段必填，写入 buffer 序号
- `event: message` 保持现有统一类型
- `data:` JSON 结构与现有 `model.Event` 一致，不变

心跳：每 30s 发 SSE 注释行（`:keepalive`）防代理超时。

终态：job 进入终态后服务端发 `[DONE]` 标记后关闭连接，前端识别 `[DONE]` 不触发重连。

### 3.2 前端客户端实现实情

当前前端 SSE 客户端**不是浏览器原生 EventSource**，而是用 fetch + ReadableStream 自己拼。这意味着：

- 续传协议（`Last-Event-ID` 头）需要前端**手动实现**——记录最后一条成功收到的事件 id，重连时放进请求头
- 410 状态码处理需要前端识别——清空已记 id、退回到"重新拉首屏"路径
- 心跳监测、指数退避重连、连接生命周期管理都已经在自实现里

新方案下需要新增的客户端逻辑：

1. 首次打开 / 刷新 / 410 fallback：先调快照接口拿到 `(messages, job, lastEventSeq)`，把 `lastEventSeq` 作为初始 `Last-Event-ID` 发起 SSE 订阅
2. 收到事件后用事件的 `id` 字段更新本地保存的 `Last-Event-ID`
3. 短暂断线重连时把当前保存的 `Last-Event-ID` 放进请求头
4. 收到 410 时清空已记 id、清空 SSE 连接状态，回到第 1 步
5. 收到 `[DONE]` 不重连

是否切换到浏览器原生 EventSource 是一个独立的待决策点（见 §6）：原生支持自动续传，但不支持自定义请求头，会限制现有的 share token 类鉴权方式。

---

## 4. 故障与边界条件

### 4.1 进程崩溃

- 进程崩溃 = 所有 in-memory buffer 全部丢失
- **可接受**：进程崩溃同时意味着 job 协程死亡，job 在重启后从 `messages.jsonl` + `job.json` + Continue 续跑
- 进行中那段事件即使保住也无意义——它们对应的业务状态会被 Continue 重新生成
- 与磁盘方案功能等价

### 4.2 内存占用

- 进行中一轮 A 类 chunks（30s–5min 流式）：~30KB–1MB
- B 类 cursor 追赶窗口：KB 级
- **单 job 稳态**：< 1MB，与 job 时长解耦
- 并发 3–5 job：几 MB
- 极端长任务（百万事件量级，跑数十小时）：仍稳定在 MB 量级，因为已完成轮次的 chunks 都会被 GC

### 4.3 并发模型

- **单写者**：job loop 串行发布，追加天然有序
- **多读者**：每个 SSE 连接一个独立 reader，互不影响
- **写锁只保护追加索引这一个动作**，唤醒是无锁广播
- **读不需要锁**：append-only 共享存储，已写入部分永不修改
- **GC 在写者上下文执行**（事件追加之后立即触发 GC 检查），与读者只通过 cursor 协调

### 4.4 慢消费者

- 慢只是慢，不丢事件、不影响其他订阅者、不反压 producer
- 慢订阅者会让 GC 暂时无法回收对应区间，但其他订阅者不受影响
- 唯一上限是连接的写超时（避免无限挂着占进程资源 + 让 GC 长期阻塞）
- 写超时也不丢事件——cursor 不推进，重连可续传

### 4.5 buffer 保留与 GC

| 触发点 | 行为 |
|---|---|
| 任意 A 类轮次结束事件发布 | 检查所有活跃 cursor，若都已越过 round_end + `messages.jsonl` 已写入 → 回收该轮次 chunks（含轮次内已被标记可丢弃的 B 类） |
| 任意 B 类事件发布 | 检查所有活跃 cursor，若都已越过 → 轮次外的 B 类立即物理回收，轮次内的 B 类随所在轮次整轮回收 |
| **无订阅者时事件发布** | "所有 cursor 越过"空真成立 → 轮次外 B 类立即物理回收、轮次内 B 类随轮次走、A 类等本轮 round_end + `messages.jsonl` 写入后整轮释放 |
| 慢订阅者卡住 GC | 等订阅者 cursor 推进；若订阅者写超时被踢，cursor 退出活跃集合，GC 立即放行 |
| 客户端真正断开连接 | reader 清理，cursor 退出活跃集合，GC 立即放行 |
| job 进入终态 | 停止 GC，buffer 此刻剩余内容保留至 job 显式删除（最后一轮 chunks + 轮次外 B 类追赶窗口） |
| **终态 job 被 `Continue` / `SendMessage` 再次激活** | 复用同一 buffer，把"已终态"标记翻回"GC active"，新一轮发布的事件按正常规则回收。`Start` 不走这条路径——`Start` 通过 `resetForRun` 直接换新 buffer |
| job 显式删除 | 立即释放整份 buffer |

---

## 5. "不丢"的精确语义

| 场景 | 是否不丢 | 说明 |
|---|---|---|
| 客户端 tab 切后台、网络抖动、SSE 断线重连 | 是 | 上次事件序号续传 |
| 客户端长时间慢消费 | 是 | cursor 落后即可，GC 自动等它 |
| 客户端离线时间过长，重连超出 buffer 范围 | 是（语义不变，但路径不同） | 服务端返回 410，客户端重新拉首屏；进行中那段从当前 tail 续上 |
| 服务端进程被 kill 后重启 | buffer 全丢，但功能等价 | 重启后 job 重跑，事件重新生成 |
| 整个 job 删除 | buffer 一并释放 | 符合预期 |
| 客户端真正关闭连接 | reader 清理 | 事件留在 buffer 直到被 GC 或 job 删除 |
| 单进程内极端内存压力 | 上抛错误、发布失败可见 | 错误全量给用户显示 |

---

## 6. 落地

不分阶段，一次性改到位。

### 6.1 后端改动清单

1. **新增 Job Event Buffer 组件**：单写者、共享 append-only 序列、唤醒信号、轮次边界跟踪、A/B 类差异化 GC
2. **publish 阶段彻底删除 C 类事件**（`ARTIFACT_*`、`STATE_SNAPSHOT`）：删事件类型定义、删 publish 调用点、删 EventHandler 接口实现、确认前端无相关分支
3. **替换原事件总线的订阅路径为 cursor reader**
4. **落实 `job.json` 写入早于 B 类事件发布的约束**——见 §1.4 字段对照表
5. **落实 `messages.jsonl` 写入早于轮次结束事件发布的约束**——见 §2.4
6. **SSE Handler 改用 reader 模式**：写超时 + 事件 id 字段携带序号 + 重连超出范围返 410
7. **快照接口附带订阅起点**：`GET /api/v1/job/:jobId` 响应中追加 `lastEventSeq` 字段——若 buffer 中存在进行中轮次取 `round_start - 1`，否则取 buffer tail K；客户端用该值作为 Last-Event-ID 起点订阅，确保进行中轮次能从第一条事件开始接收，不会丢失前半段。字段为 job 级（buffer/SSE/序号空间都是 per-job）；session 的 messages 接口不变
8. **删除现有终态特殊投递路径**：终态 5s 超时、并发投递、慢订阅者驱逐相关全部清掉
9. **删除现有环形缓冲与重放概念**：相关常量、重放窗口、replay 函数一并清理
10. **加可观测性**：buffer 暴露每 job 的事件数、活跃订阅者数、head/next 序号、最大 cursor 滞后量、GC 触发次数与累计释放量。本期落到 buffer/Service 层 API（供后续接日志或 metrics），不强制接入展示层
11. **轮次边界对所有运行入口生效**：Loop step、Shell step、Interactive (`SendMessage`) 都要发出 `ITERATION_STARTED` / `ITERATION_COMPLETED` / `ITERATION_FAILED`。Interactive 不写 `Progress.Results`，但仍要发轮次结束事件——否则 buffer round 不闭合、`SnapshotSeq` 退化到 `round_start - 1`、刷新页面会重看进行中轮次（与 `messages.jsonl` 重复）。Interactive 入口前置阶段（如 session 初始化）失败也不能借机往 `Progress.Results` 写错记，否则会把交互失败计入 loop 的 `FailedCount`、并污染 `[0,0]` 这条与 loop 第一步重叠的路径——直接走 `JOB_FAILED` 终态即可，前置阶段尚未发 `IterationStarted`，也不应该补不配对的 `IterationFailed`。Loop 入口的每步前置 session 初始化（`tryCreateSession`）若失败，则按"该步迭代失败"处理：发出配对的 `ITERATION_STARTED` + `ITERATION_FAILED`（保持 §1.1 round-boundary 契约），同时把失败计入 `Progress.Results` 并把 resume 指针推到下一步——这与 Interactive 不同，因为 loop 的 session 是按 step 归属的、`tryCreateSession` 已经处在迭代边界上，给该步开一个空轮次再立即关掉是正确语义；订阅者也能看到一个明确归属于该 step 的失败事件而非孤儿 `ITERATION_FAILED`。`runInteractive` 自身则要在进入 loop body 之前先发 `JOB_STARTED`（与 `runLoop` 对称），让其他订阅者通过事件流感知 job 重新进入 Running。
12. **中断路径必须补轮次结束事件**：`isInterruptedRun(err)`（`context.Canceled` / `context.DeadlineExceeded`）、shell 进程被 kill 的中断分支，以及 `runLoop` / `runInteractive` 顶层 `recover()` 捕获的 panic 路径，在 `return stepAborted` 或 `failJob` 之前必须先发 `RUN_ERROR` + `ITERATION_FAILED` 把本轮 round 关掉，再让外层去发 `JOB_STOPPED / FAILED`。终态事件本身在 buffer 里是 B 类、不闭合 round；漏掉这对事件的话，`MarkTerminal` 只是把 GC 冻起来掩盖泄漏，`Continue` 走 `ResumeGC` 复活 GC 时，旧 round 因为 `closed=false` 永远满足不了回收条件、A 类 chunks 永久滞留；同时 `openRoundID` 不会被清，下个 run 的 `JOB_STARTED` 还会被错误归入旧轮次。中断/panic 分支只 publish 事件，不写 `Progress.Results`——这样 Continue 在同一 path 重跑时不会双计 result。panic recovery 没有完整的 runID/path/sessionID 上下文，使用 `Progress.CurrentPath` 与最近一次 `SessionIDs` 作为最优近似即可
13. **终态后再激活的 buffer GC 复活**：`Continue` / `SendMessage` 复用同一 buffer，必须在新 run 启动前把"已终态"标记翻回"GC active"。`Start` 是唯一通过 `resetForRun` 直接换新 buffer 的入口；其余两条入口若不复活 GC，旧的终态标记会让 gcLocked 整段新 run 都短路

### 6.2 前端改动清单

1. **SSE 客户端补 Last-Event-ID 续传**：收到事件后保存 `id` 字段；重连时放进请求头
2. **410 fallback**：收到 410 时清空 id、重新调首屏接口拉 `(messages, job, lastEventSeq)` 后再订阅
3. **首屏渲染从 `job.json` 完整重建 loop UI**：明确不再依赖事件回放，确保即使 SSE 断也能正确显示

### 6.3 验收标准

- 日志中不再出现 `terminal event dropped`、`event buffer truncated`、`replay: buffer exceeds channel capacity`、`evicting slow subscriber` 这四类 ERROR/WARN
- 长任务（≥ 4 小时、≥ 100k 事件）下单 job buffer 内存稳态 < 1MB
- 客户端 tab 切后台 90 分钟后回到前台，对话内容完整可见
- 客户端断线 30s 内重连，不出现"重新渲染整个对话"的视觉跳动
- 进入终态后刷新页面，对话历史从 `messages.jsonl` 完整重建；最后一轮的过程时间线（chunks）可继续回看，更早轮次的过程已被 GC 回收（buffer 保留至 job 显式删除）

---

## 7. 已决策参数

落地阶段已确认下述参数与取舍：

1. **写超时阈值**：30s。短于 5s 会误伤暂时网络慢的客户端；长于 60s 会让僵尸连接占进程资源太久、阻塞 GC 推进。落地点：`job_lifecycle.go` `sseWriteTimeout`。
2. **写超时后重试次数**：0 次。一次写超时即关连接让前端经 Last-Event-ID 重连，避免被时序排出后的旧 goroutine 与新写入路径并发投递同一事件。落地点：`job_lifecycle.go` SSE 写路径。
3. **GC 触发时机**：事件入 buffer 后立即检查（即时触发，时延更可控）。落地点：`event_buffer.go` `Publish` 末尾调用 `gcLocked`。
4. **前端 SSE 客户端**：保留 fetch + ReadableStream，手动管理 Last-Event-ID。原生 EventSource 不支持自定义请求头会冲击 share token 鉴权。落地点：`web/src/utils/sse-client.ts`。
