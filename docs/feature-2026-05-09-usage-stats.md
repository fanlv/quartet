# 使用统计（Usage Stats）—— 总览

> 所属项目：Quartet
> 状态：设计
> 纯文字描述文档，不含代码细节。

---

## 目标

为 Quartet 增加一套使用统计能力，覆盖两类消费场景：

1. **Job List 内联统计**：每天分组 header 上展示当天的 **累计执行时长** 与 **累计 turn 次数**，让用户一眼感知"今天这台机器干了多久 / 跑了多少轮"。
2. **独立统计页面**：集中查看一段时间内：
   - 每个 **Workspace** 的使用时长 / 次数 / token 数
   - 每个 **Model** 的使用时长 / 次数 / token 数
   - 每个 **Tool（工具命令名）** 的调用次数 / 时长（shell 类工具按真实命令名拆开）
   - **每日趋势**（按 model 分层折线）
   - 维度细分到 **assistant 回复 / thought / toolcall** 三类活动，以及各自的 **本地估算 token 数**

## 范围与约束

- **每个 step / turn 完成时累加一次**，不区分成功 / 失败，统一口径。
- **不实时跳秒**：正在跑的 step 不计入；完成时 header 与统计页数字会"跳一次"。
- **不回填历史**：上线之前已经发生的活动不计；某天 / 某 workspace / 某 model 没有数据时不显示统计行。
- **不做兜底持久化**：进程崩溃时正在跑的 step 直接丢，符合"不要求很准"的取舍。
- **删除 Job 不回滚已计入的统计**：统计反映"历史活动事实"，不是"当前 Job 集合状态"。
- **本期不做 jobCount**：用 turnCount 表达活跃度。
- **Token 数为本地 tokenizer 估算值，不是 API 计费值**：与现有口径一致；展示侧需有提示文案，避免被当作账单依据。

## 文档结构

| 文件 | 内容 |
|---|---|
| `00-overview.md`（本文件） | 目标、范围、整体方案概要 |
| `01-data-model.md` | 度量维度、累加单元、Token 拆分口径、Tool 命令名取法、Model 归属规则 |
| `02-storage.md` | 存储格式（按月分片）、目录约定、读写策略、并发与崩溃 |
| `03-sdk-boundary.md` | SDK 抽象与边界、对外职责、与上层模块的解耦 |
| `04-frontend.md` | Job List 展示、统计页布局与交互、入口 |
| `05-api.md` | List 接口附带数据、统计接口形态 |
| `06-acceptance.md` | 验收要点 |

## 整体方案概要

### 累加链路

- 在 **三个 step 收尾位置** 各调一次"提交本步统计"：
  - Interactive 一轮对话结束
  - Loop 一次 iteration 结束
  - Shell 单步执行结束
- 每次提交携带：耗时、turn=1、所属 workspace、所属 model、本步内 assistant / thought / toolcall 的 **次数** 与 **token 估算**、以及按 **工具命令名** 分桶的 `{ count, totalMs }` 集合。

### 数据归属

- **日期**：按 step 完成时刻所在本地日期；跨夜 step 整段计入完成那天。
- **Workspace**：取 Job 当前 workspace。
- **Model**：取 **本步真实跑的 model**（per-step 解析），与 Job List 标签的 `Job.FirstModelID` **保持解耦**——后者代表"主模型"，前者代表"实际花了多少时间在哪个 model 上"。

### 抽象

- 引入新的内部包 `services/usagestats`，承担：
  - 累加协议（Recorder）
  - 持久化（按月分片 + 内存缓存 + 节流写）
  - 查询（Reader：日级总盘 / 范围聚合）
- 上层模块（job 执行器、HTTP handler）只通过 SDK 接口与之交互，不感知存储格式。

### 存储

- 目录：`${LOCAL_MEMORY}/agent/usage_stats/`
- 文件：按月分片 `YYYY-MM.json`
- 每文件结构：`workspaceId → date → { totalMs, turnCount, counts, tokens, tools, models }`；总盘与 model 子桶字段集对齐，`tools` 是 `工具命令名 → { count, totalMs }` map
- 内存层持有 **当月文件副本**；写采用节流合并（小窗口内多次写合并为一次落盘）
- 历史月份按需懒加载（统计页查询时载入），LRU 控量

### 前端展示

- **Job List**：day header 右侧 `2h 15m · 24 turns`（中文 `2h 15m · 24 次`），无数据时不显示。
- **统计页**：顶栏图标入口（与 Settings、File Browser 同级），独立页面、独立 URL；
  默认展示 7 天，提供 7d / 30d / 90d / All / 自定义；后端无参数接口仍保留 30 天缺省窗口作为兜底；
  顶部一组 segmented control 切换 **Duration / Counts / Tokens** 三种度量；
  四视图并列：**By Workspace**（donut）、**By Model**（横向条形图）、**By Tool**（横向条形图，Tokens 模式下空态文案）、**每日趋势（按 model 分层折线，不按 tool 分层）**。

### 接口

- `GET /api/v1/job/list`：响应附带当 page 涉及日期的 **dailyStats 总盘**（仅时长与 turn 数）。
- `GET /api/v1/stats/usage?from&to`：返回四视图所需的全部聚合数据，按所选度量切换。

---

# 数据模型

## 累加单元（一次 step / turn）

一个 step / turn = 一次累加。覆盖三类来源：

1. **Interactive**：用户的"发送消息 → 助手回复完成"算 1 turn。
2. **Loop**：每完成 1 次 iteration 算 1 turn。
3. **Shell**：单步执行完成算 1 turn。

所有来源走同一份累加协议，归桶规则一致。

## 度量维度

每次 step 完成时一次性贡献以下字段到对应桶（天 / model）：

| 维度 | 含义 |
|---|---|
| `totalMs` | 本步耗时（墙钟）。Interactive 取本轮开始到结束；Loop / Shell 取该步执行时长 |
| `turnCount` | 固定 +1 |
| `assistantCount` | 本步内助手文本回复段数 |
| `thoughtCount` | 本步内 thought / Deep Think 段数 |
| `toolCallCount` | 本步内 tool call 触发次数 |
| `tokens.total` | 本步 round 的整体 token 估算（沿用现有 OnTokenUsage 口径） |
| `tokens.assistant` | 本步内 assistant 文本段的 token 估算之和 |
| `tokens.thought` | 本步内 thought 段的 token 估算之和 |
| `tokens.toolCall` | 本步内 tool call 段的 token 估算之和 |
| `tools` | 本步内按 **工具命令名** 分桶的 `{ count, totalMs }` map；详见下文 |

**关键一致性约定**：
- `tokens.total` 与 `tokens.assistant + tokens.thought + tokens.toolCall` **不强制相等**——前者覆盖整轮（含 user 消息、tool_result、历史等），后者只统计本步的输出段。两者口径不同，分别用于"整体规模"与"输出构成"分析。
- 文档与 UI 上需明示这一点，避免被理解为"账单值"。
- `sum(tools[*].count) == toolCallCount`、`sum(tools[*].totalMs)` 在不发生异常残留的前提下应等于本步内所有 tool 调用的耗时之和（异常残留另述）。
- `totalMs` 允许为 0：亚毫秒完成的合法 step 仍必须贡献 `turnCount`、counts、tokens 与 tools，不能因为毫秒截断为 0 而整条丢弃。

## Tool 维度（工具命令名分桶）

### 桶 key 的取法

为了让"shell 类工具"能按真实运行的命令分开统计（否则只会看到一行 `shell: 1000 次`），bucket key 的取法分两种：

- **shell-类工具**（识别规则：tool name 大小写无关地包含 `shell` 或 `bash`）：
  1. 解析本次 tool call 的 args，取 `command` 字段值（字符串）。
  2. 如果 args 解析失败、或没有 `command` 字段、或值不是字符串：回退用原始 tool name 走“第一个空白分隔 token + 大写”的规则。
  3. 取命令字符串的**第一个空白分隔 token** 作为命令名。
  4. 命令名规范化：
     - 去掉首尾的成对引号（单 / 双）。
     - 如果首 token 形如 `KEY=value`（环境变量赋值前缀），跳到下一个非赋值 token；连续多个赋值都跳过；赋值值里包含引号和空格时仍按一个 shell word 处理。
     - 取处理后的 token 后统一转大写作为 key（保留 `PYTHON3` 这种带版本号的形态）。
  5. 全为空白 / 处理后为空 → 回退用原始 tool name 走“第一个空白分隔 token + 大写”的规则。
- **非 shell 类工具**：取 `OnToolCallStart` 给的 tool name 的第一个空白分隔 token，再统一转大写作为 key；不做 friendly 名映射。这样可避免 ACP / Claude Code 传入 `Read path`、`Grep path` 这类 title 时按路径拆出大量脏桶。

注意：本期**不做**「sudo / time / env 等命令包装器」的特殊识别（如 `sudo ls` 仍归到 `sudo`），不做 pipe / 子 shell 的多命令拆分（`cat foo | grep bar` 只记到 `cat`）。这些是已知的简化，命中频率低，可后续按需增强。

### 时长的计算

- 在 Accumulator 内自计时：`OnToolCallStart` 时记下 `now()` 与 tool name；`OnToolCallEnd` 时计算 `now() - startedAt` 作为本次工具调用时长。
- 异常未收到 `OnToolCallEnd`（builder 中断 / step 异常）→ pending 残留**直接丢弃，不入账**，避免污染数据。
- 同一 step 内多次调用同一工具 → 各自独立计时，bucket 累加。

### 字段含义

- `tools[key].count`：本步内该工具被调用的次数。
- `tools[key].totalMs`：本步内该工具的累计耗时。

不区分成功 / 失败（与其它维度一致）；不做 per-tool 失败率（后续可扩展）。
不做 per-tool token 拆分（避免 schema 嵌套过深）；token 仍只在 day / model 级别提供。

## 归属规则

### 日期

- 以 step **完成时刻** 所在的服务器本地日期为准。
- 跨夜 step（23:55 起、00:10 完）整段计入完成那天，不拆分。

### Workspace

- 取 Job 所在 workspace。Workspace 缺失时不入账并 warn 日志（不应出现的兜底）。

### Model（per-step 解析）

为追求统计页准确度，按 **本步真实跑的 model** 入账，**不绑定 `Job.FirstModelID`**：

- **Interactive**：取本轮 session 的 model id（用户当时所选）。
- **Loop**：以本步执行时 session 实际绑定的 model 为准；step 节点的 model 覆盖只有在创建 / 切换 session 后才会自然体现在 session model 上。实现上不得把任务级默认值 backfill 到 step 后再当成“显式覆盖”做统计归因，避免 pause 后同 session 切 model 再 Continue 时仍记到旧默认 model。
- **Shell**：用 Job 所在 session 的当前 model（shell agent 仍是 LLM agent 的一种执行形态），同样不使用 backfilled step 默认 model 做统计归因。

解析失败 / 拿不到 model id：写入时**只入当天总盘，不入原始 model 子桶**（避免脏 key）。读取聚合时用 `day total - sum(model buckets)` 推导出差额，并在 API / UI 中以 `(unknown model)` 暴露。这样存储层保持干净，同时统计页仍能解释 `sum(models[*]) ≤ total` 的差额来源。

**与 Job List 标签的关系**：
- `Job List` 显示的 model 标签来自 `Job.FirstModelID`，语义是"这个 Job 的主模型"，是反规范化字段、不变更。
- 统计的 model 桶代表"在这个 model 上实际花了多少时间和 token"，会包含 per-step 覆盖、用户中途切换等情况。
- **两者是两件事，刻意不合并**。

## Token 拆分口径

| 项 | 算法 |
|---|---|
| `tokens.total` | 沿用现有 `OnTokenUsage`：每轮结束时本地 tokenizer 对完整 history 估算所得 |
| `tokens.assistant` | 本步每个 assistant 文本段单独本地 tokenize 累加 |
| `tokens.thought` | 本步每个 thought 段单独本地 tokenize 累加 |
| `tokens.toolCall` | 本步每个 tool call 的 `name + args(JSON 字符串)` 单独本地 tokenize 累加 |

**统一说明**：
- 全部走本地 tiktoken-go 估算，**非 API 计费值**；UI 提示文案应明确告知。
- 复用现有 `Message.Extra` 的 token cache：已有缓存直接读，无缓存时 tokenize 一次并写回 Extra。
- ToolCall 取 `name + args` 而非仅 args：更接近模型真实输出 token 体感。
- 不区分 input / output token，本期只看输出段构成；input 维度作为后续可扩展项。

## 不区分成功 / 失败

step 的最终状态（Completed / Failed / Stopped）都计入。失败也消耗了时间与 token，与"使用量"语义一致。

---

# 存储

## 目录与分片

- 根目录：`${LOCAL_MEMORY}/agent/usage_stats/`
- 文件粒度：**按月分片**，文件名 `YYYY-MM.json`（服务器本地时区）。
- 选择按月而非单文件 / 按天的理由：
  - 单文件随时间增长，写热路径每步都要原子重写整文件，IO 延迟会持续恶化。
  - 按天文件数过多，统计页查询需要叠多个文件，碎 IO 反而更差。
  - 按月在容量与文件数之间取得平衡：单文件上限可控（~100 KB 量级），统计页查询最多读几十个文件。
- 与现有约定的关系：
  - 已有 `user_input/` 与 `im/{chatID}/` 走"按天 jsonl"（追加日志型，适合按天）。
  - 使用统计是"嵌套累加型"，按月才是自然粒度，**不破坏现有目录约定**。

## 文件结构

每个月文件内容：

- 顶层是 workspace 索引：`workspaceId → 该 workspace 当月的日索引`。
- 日索引：`YYYY-MM-DD → 当日总盘 + model 子桶`。
- 当日总盘字段：`totalMs / turnCount / counts / tokens / tools`。
- model 子桶：`modelId → 该 model 当日的 totalMs / turnCount / counts / tokens / tools`。
- 总盘与 model 子桶的字段名与含义保持一致，便于统一处理。
- `tools` 是 `工具命令名 → { count, totalMs }` 的 map（命令名取法见数据模型文档）；空 map 等价于本天没有 tool 调用。

## 内存层

- 进程持有 **当月文件的内存副本**（map 结构）；step 完成时直接改内存。
- 落盘走节流合并：内存变更后启一个短延迟（约 1 秒）的 timer，期间多次写合并为一次磁盘写；timer 到期或进程优雅停机时落盘。
- 历史月份按需懒加载（统计页范围查询触发），命中后保留在 LRU 中（容量按月数限制，例如 6-12 个月），淘汰时如有未刷脏数据先落盘再淘汰。
- 当月跨月切换：写入侦测到日期换月时自动切换到新文件；旧月文件如有未落盘数据，先同步尝试一次性落盘，再写入新月内存桶。

## 写策略

- 常规同月写顺序：**改内存 → 节流落盘**。跨月写是例外：**旧月脏数据同步 flush → 改新月内存 → 节流落盘**。落盘动作做原子写：
  - 写到同目录下的临时文件（`.tmp` 后缀），再 `rename` 覆盖目标文件。
  - 避免崩溃中残留半截 JSON。
- 失败处理：
  - 写失败（IO / rename 错误）**不打断主流程**，仅 warn 日志；脏月份会重新标记，后续 debounce / shutdown flush 带上累积变更重新尝试。
  - 解析失败（文件已损坏）：当月数据视为空，新写入按空底直接覆盖；不阻塞业务。
- 进程崩溃边界：
  - 崩溃丢失"timer 未到期那段时间内已累积但未刷盘"的变更。窗口约 1 秒，少量丢失符合"不要求很准"的取舍。

## 读策略

- **Job List dailyStats**：仅需当 page 涉及日期对应的总盘。
  - 同月：直接读内存（当月）/ 懒加载该月文件。
  - 跨月：分别从对应月份文件读取后合并。
- **统计页范围查询**：根据 `from / to` 计算覆盖的月份集合，逐月加载并按 workspace / model / day 聚合返回。
- **All Time 范围推导**：起始月份参考磁盘文件，以及进程内“有真实数据或脏写入”的月份。因为写入顺序是“改内存 → 节流落盘”，只看磁盘会在 flush 前漏掉同进程内刚写入但尚未落盘的数据；但读路径因 Job List 查询而懒加载出的空月份不能参与 All Time 起点，避免把范围拉到无数据的旧月份。
- **无法归桶的 model 差额**：存储文件不写特殊 model key；Reader 聚合时用日总盘减去 model 子桶和，差额在 `byModel` / `daily.models` 中作为 `(unknown model)` 暴露给前端。
- 读路径按月获取内存快照后在锁外聚合；cache miss 时磁盘 IO / JSON 解析也必须在锁外完成。锁内只做 cache 命中判断、短暂 clone 与 LRU/cache 更新，避免统计页或 Job List 查询长时间阻塞 step 收尾入账。

## 并发

- Quartet 单进程单用户场景，**无需文件锁**；进程内串行化（mutex）即可。
- 写入路径只在改内存 / 标 dirty 的短临界区持锁；落盘与读侧聚合都不持这把锁。
- 不假设外部进程并发写本目录；如果未来出现，需要再引入文件锁机制（不在本期范围）。

## 没有兜底持久化

- 未完成的 step 不入账，因此进程崩溃时正在跑的 step 直接丢，不补救。
- 不做"step 进行中 checkpoint"——既贵又不准确。

---

# SDK 抽象与边界

## 为什么抽 SDK

- 累加发生在 **三个 step 收尾位置**，不抽必然散乱重复。
- 写（每步累加）与读（List dailyStats / 统计页聚合）是两类不同消费方，共用同一份数据模型，SDK 边界天然清晰。
- 持久化形态可能演化（按月分片 → SQLite / 其它），抽 SDK 后切换不影响调用方。
- 易测试：累加、合并、查询逻辑独立后可写单测，远比"散在 executor 里"轻松。

## 包位置与命名

- 新增 `services/usagestats`，沿用 `services/job` / `services/workspace` 的风格。
- 内部子结构（仅作组织参考，不约束实现）：
  - 数据模型与口径定义
  - 存储后端（按月分片 + 内存缓存 + 节流写）
  - Recorder（写）
  - Reader（读 / 聚合）
  - 内部小工具：本地 tokenizer 接入（复用 `pkg/tokenizer`）

## 对外职责

### Recorder（写）

让上层用一次调用提交"本步的全部统计"：

- 一次调用承载：
  - 所属 workspace
  - 所属 model（per-step 解析后的结果）
  - 完成时刻
  - 时长
  - 本步内 assistant / thought / toolcall 的次数
  - 本步内 assistant / thought / toolcall 的 token 估算
  - 本步整轮 token 估算（沿用现有 OnTokenUsage）
  - 本步内按 **工具命令名** 分桶的 `{ count, totalMs }` 集合
- SDK 内部完成：
  - 按完成时刻定位月文件 + 日键
  - 累加到日级总盘 与 model 子桶（含 `tools` map 合并）
  - 内存层标脏 + 节流落盘
- 提交失败不抛错，只 warn 日志，不打断业务主流程。

### Reader（读）

两类查询接口：

- **日级总盘查询**：传入 workspaceId + 一组日期，返回这些日期对应的 `{ totalMs, turnCount }`。供 Job List 接口附带 dailyStats 使用。
- **范围聚合查询**：传入时间范围 `from/to`，返回统计页四视图所需的全部聚合数据：按 workspace 聚合、按 model 聚合、按 tool 聚合、按日明细（含每日 model 分布）。度量类型由前端基于返回的全量字段本地切换。

### Recorder 辅助：本步累加器（Accumulator）

供调用方在 step 执行期间临时持有，挂在 `loop_event_handler` 上：

- 收到 `OnMessageEnd` → assistantCount +1，accumulator 把消息文本走本地 tokenizer 累加到 assistantTokens。
- 收到 `OnThoughtEnd` → 同理，落到 thoughtCount / thoughtTokens。
- 收到 `OnToolCallStart(id, name)` → accumulator 记下 `pendingTools[id] = { name, startedAt: now() }`。
- 收到 `OnToolCallArgs(id, delta)` → accumulator 把 delta 拼接到 `pendingTools[id].argsBuf`，便于在 `End` 时解析。
- 收到 `OnToolCallEnd(id, success)`：
  - 取出 `pendingTools[id]`，计算 `dur = now() - startedAt`。
  - 用 args 与原始 name 走"工具命令名解析"（详见数据模型文档），得到 bucket key。
  - `tools[key].count += 1`、`tools[key].totalMs += dur`；同时全局 `toolCallCount +=1`、`toolCallTokens` 沿用现有口径累加。
  - 删除 `pendingTools[id]` 释放瞬态。
- 收到 `OnTokenUsage(total)` → 暂存为 totalTokens（沿用现有口径）。
- step 收尾时把累加器（含 `tools` map）一次性交给 Recorder；尚未关闭的 `pendingTools` 残留**直接丢弃，不入账**。

## 与上层模块的解耦

- **`services/job/executor_*`**：只负责"step 完成时调用 Recorder"，不感知存储格式与文件位置。
- **`services/job/loop_event_handler`**：只负责"事件出现时调用 Accumulator"，不感知数据落地与口径定义。
- **HTTP handler**：
  - Job List handler 调 Reader 的日级查询，把结果塞到 `JobListResponse.dailyStats`。
  - Stats handler 调 Reader 的范围聚合查询，直接返回。
- **前端**：完全不感知存储分片；对外只看到聚合数据。

## 错误与降级

- SDK 内部 IO / 解析失败 → Reader 返回空 / 部分结果，同时携带原始错误；Job List 读统计失败时仅在完全无可用结果时降级为不带 dailyStats，若 Reader 已返回部分成功结果则继续带上这部分 dailyStats；统计页接口返回空 / 部分数据 + failed/error 标识而非 5xx。
- 外部传入的非法参数（日期颠倒、workspace 为空等）→ Reader 返回空、Recorder 跳过并 warn。
- HTTP 读接口对空结果做归一化：`byWorkspace` / `byModel` / `byTool` / `daily` 始终返回数组（空时为 `[]`），不返回 `null`，前端可稳定按数组处理。
- 健康度监控不在本期范围；若后续要看 SDK 自身可观测性，再加内部计数器与 expvar。

## 测试边界

- SDK 单测覆盖：累加正确性、跨月切换、并发写、解析容错、聚合口径。
- 上层 executor 测可注入 mock Recorder，验证收尾位置确实调用了一次。
- 不测真实 tokenizer 数值，只测"被调用过且累加进入对应桶"。

---

# 前端展示

## Job List 内联统计

### 位置与文案

- 在每天分组 header 右侧加一行小字辅助信息：
  - 有数据：`2h 15m · 24 turns`（中文 `2h 15m · 24 次`）
  - 无数据：**不显示统计行**，不出现 `0 turns` 占位
- 与日期同行右对齐；颜色比主文案稍弱，避免抢戏。
- 时长格式沿用 Scheduled Tasks 短格式：`30s` / `18m` / `1h` / `2h 15m`。
- 不含 model 维度信息（model 维度集中放到统计页）。
- 不可点击、不展开；纯只读展示。

### 切换 Workspace

- header 数字立刻切到对应 workspace 的总盘。
- 当前 workspace 数据由 List 接口附带 dailyStats 提供（无需额外请求）。
- 每次 list 响应只代表当前 page 涉及日期的最新统计状态：若某个 page 涉及的日期未出现在 `dailyStats` 中，前端必须把这些日期视为“当前无统计”并清掉旧缓存，不能沿用上一轮 polling / focus refresh 的旧值。

## 统计页

### 入口

- Web 顶栏新增图标按钮，在 Workspace 首页与具体 JobChat 页面都可见；与 Settings、File Browser、Share 等同级，沿用同一套尺寸 / 色板 / hover 风格。
- 图标走"统计 / 图表"系列；hover 提示文案 `Statistics` / `统计`。
- 点击进入独立页面（不是弹窗、不是侧栏），带独立 URL，便于刷新与书签。
- 顶栏宽度紧张时把次要按钮收进溢出菜单（具体在实施时确认）。

### 顶部控制区

- **时间范围快捷按钮**：`7 days` / `30 days` / `90 days` / `All`。
  - 选择 `All` 时明确请求 All Time 语义，不走接口缺省 30 天窗口。
- **自定义日期 picker**：起始 + 结束。
- **默认选中 7 天**——前端入口默认关注最近一周；用户可手动切到 30 / 90 天或 All。后端无参数接口仍保留 30 天缺省窗口作为直接调用时的兜底。
- **度量切换器（segmented control）**：`Duration` / `Counts` / `Tokens` 三选一。
  - 切换后四视图（By Workspace / By Model / By Tool / 趋势）同步换口径；图表形态保持各自既定形态。
  - Counts 模式下默认显示 turnCount，可下拉细分为 assistant / thought / toolcall。
  - Tokens 模式下默认显示总 token，可下拉细分为 assistant / thought / toolcall。
- 时间范围 / 度量 / 子项变更 → 四视图同步重算；接口请求必须防竞态，快速切换范围时只允许最后一次请求结果落到页面，旧请求需 abort 或被 request id 丢弃。
- 正常成功加载后展示当前筛选条件下的新结果；新请求进行中不得继续展示旧筛选条件下的结果，必须先进入 loading / skeleton 状态。若本次加载失败，可保留上一次成功结果作为降级展示，但必须同时展示错误提示和重试入口，避免把旧数据误认为新结果。
- 范围内全空 → 整页空态文案，不显示空表 / 空图。
- 页面顶部有一行小字 banner 提示："Token 数为本地估算，非 API 计费值"（中英文两份）。

### 视图 1：By Workspace

- 使用 donut 图展示各 workspace 在当前度量下的占比，中心展示当前度量总量。
- 右侧 legend 展示 workspace 名称、当前度量值与占比；按当前度量主项降序排序。
- Workspace 显示**名称**；当前活跃 workspace 加视觉强调（badge / 高亮）。
- 已删除 / 找不到的 workspace：仍显示统计，名称回退为"已删除/Deleted"+ 原 id 后缀（跟随当前语言；统计是历史事实，不能因为 workspace 被删就丢）。
- legend 项可点击 → 跳到对应 workspace 的 Job List。
- hover 展示 tooltip，包含 duration / counts / tokens 的完整明细。

### 视图 2：By Model

- 使用横向条形图展示各 model 在当前度量下的绝对值，按当前度量主项降序排序。
- 跨 workspace 汇总（不再按 workspace 切分），每个 model 一行。
- Model 显示**名称**（从已有 model registry 解析）；找不到名称的 model id 直接显示原 id，不报错。
- "无法归桶"的部分以一个特殊行展示（英文 `Unknown model`，中文 `未知模型`），便于排查；展示文案必须走 i18n，不在组件内硬编码英文。
- 默认只展示 Top N；超出部分以“隐藏了 N 项”的方式提示，避免图表过长。
- hover 展示 tooltip，包含 duration / counts / tokens 的完整明细。

### 视图 3：By Tool

- 使用横向条形图展示各工具命令名在当前度量下的绝对值；Duration 展示总时长，Counts 展示调用次数。
- Tokens：本视图**不支持** token 维度，展示空态文案"按工具维度暂不区分 token";segmented control 在统计页保持一致，不隐藏。
- 跨 workspace、跨 model 汇总，每个工具命令名一行。
- 工具命令名为数据模型文档约定的命令名取法：shell 类工具用 `command` 第一个有效 shell word；其它工具用原始 tool name 的第一个空白分隔 token；最终统一转大写。
- 不做友好别名映射；脏 / 拼错的命令名也照样显示，便于发现异常调用。
- 排序默认按当前度量主项降序。
- **不**支持按 workspace / model 交叉筛选（保持平铺，避免 UI 复杂度爆）。
- hover 展示 tooltip，包含总时长与调用次数。

### 视图 4：每日趋势

- **按日折线图**，Y 轴随当前度量变化（Duration / Counts / Tokens）。
- X 轴：日期，按时间范围铺满；缺数据的日期占 0 高度空槽。
- 每条线按 **model 分层颜色** 展示；分层和显隐状态使用稳定 model id，顶部图例颜色 ↔ model 展示名，可点击切换显隐。若多个 model 的展示名相同，也不能把它们合并成一个分层。
- 如果当日总盘大于已知 model 分层之和，后端 Reader 会把差额补成稳定未知模型 key；前端仍保留 residual 兜底，确保趋势总量与当日 total 一致；图例与 tooltip 同样使用 i18n 文案。
- 趋势图**不按 tool 维度分层**——tool 名通常数十种以上，叠加颜色不可读；tool 维度只通过 By Tool 图表展示。
- 鼠标 hover：tooltip 显示当日明细：当前度量主项、各 model 的贡献、turn 数。
- 时间范围 ≤ 31 天时按"日"为粒度；> 31 天本期仍按日（让用户接受密一些），后续再加"按周"聚合。
- 不做点击穿透（点某天趋势点跳当天 Job List）——本期纯展示，避免和 List 状态联动出问题。

### 度量切换的展示策略

- Duration 模式：各图表突出"时长"值，趋势图 Y 轴单位为分钟 / 小时自适应。
- Counts 模式：各图表突出当前选择的次数子项，趋势图 Y 轴单位为"次"。
- Tokens 模式：各图表突出当前选择的 token 子项，附带说明性 tooltip（解释 total 与三段之和的口径差异）；趋势图 Y 轴单位为"tokens"，>1k 时自动 K/M。
- 趋势图轴标签必须明确展示当前单位；数值大数缩写使用大写 `K / M`，中英文保持一致。

### 加载与错误

- 加载中：图表显示 skeleton；即使之前已有成功结果，切换时间范围发起新请求后也先隐藏旧结果，避免用户把旧范围数据误认为当前筛选结果。
- 接口失败：整页错误提示带原始错误信息（沿用"错误全量展示"的原则），并保留重试按钮；如果已有上一次成功结果，则失败后恢复展示旧结果作为降级参考，用户可继续参考旧结果并手动重试。
- 当接口返回 `failed/error` 且同时携带部分聚合数组时，前端不得把这批 partial data 当作当前筛选条件下的完整结果渲染；没有上一次成功结果时只显示错误，不显示残缺图表。
- 本期统计页使用单个汇总接口加载四个视图，因此接口级失败按整页错误处理；如果后续拆分为多接口 / 多 section 数据源，再按单视图失败做局部错误提示，其它视图继续渲染。

## 国际化

- 所有文案（"turns" / "次"、"Duration" / "时长"、"Tokens" / "Token 数"、"Unknown model" / "未知模型"、空态、提示 banner、表格列头、按钮 aria-label 等可访问性文案）走现有 i18n 体系。
- 默认英文，跟随系统语言切换。
- 时长格式中英文一致（`30s / 18m / 1h 5m`）。

## 视觉一致性

- 与现有顶栏按钮、Job List、Settings 页风格保持一致。
- 图表使用轻量 SVG 实现，样式与现有页面保持一致；不额外引入重量级图表库。

---

# 接口设计

## 总体原则

- 写路径只在 step 收尾时通过 SDK 内部完成，**不暴露任何 HTTP 写接口**。
- 读路径分两类：
  - **Job List 顺带读**：附带在现有 list 接口响应里。
  - **统计页专用读**：独立接口。
- 接口失败不应让前端无法显示主页面：
  - List 接口若统计读取失败，dailyStats 字段缺失但 jobs 仍正常返回。
  - 统计接口失败时返回空数据 + 标识，并附错误原文（不隐藏）。

## Job List 接口附带数据

### 路径

复用现有 `GET /api/v1/job/list`。

### 响应扩展字段

- 新增字段：`dailyStats`，按当前 page 涉及的日期投影返回。
- 字段含义：以 `YYYY-MM-DD` 为 key，每个 key 对应一个 `{ totalMs, turnCount }`。
- 缺失日期不出现在 map 里（前端按"无统计"处理）。
- 对当前 page 涉及的日期而言，字段缺失或 map 中缺少某个日期都表示服务端本次没有该日期统计；前端必须用它覆盖本地旧状态，而不是把缺失解释为“保留旧值”。

### 与 ETag / Version 的关系

- 现有 list 的 ETag / Version 由 jobs 数据决定，**不为统计变更而 bump**。
- 这意味着用户下次正常刷新 list 时统计数据自然带上；不引入额外的高频版本失效。

### 性能边界

- 当前 page 涉及日期数量有限（一页 jobs 最多 50 条，分布到几天），SDK 层一次内存 / 文件读即可。
- 跨页加载时新 page 拿到该 page 范围内的统计；老日期已在前页带过，不重复传。

## 统计页接口

### 路径

`GET /api/v1/stats/usage`

### 入参

- `from`、`to`：日期，闭区间，`YYYY-MM-DD` 字符串。缺省策略：缺 `from` 默认 30 天前，缺 `to` 默认今天。
- `all`（可选）：`1` / `true` 表示 All Time。选择 All 时前端传该参数，服务端跳过“缺 `from` 默认 30 天前”的策略，从已有统计数据的最早月份开始聚合。
- 校验失败（日期格式错误、默认值补齐后 `from > to` 等）返回 400，错误原文透传。例如只传未来 `from` 且缺省 `to` 为今天时，也必须返回 400。

### 响应结构

聚合后的四视图数据：

- **byWorkspace**：数组，每项含 workspaceId、workspaceName（已删除回退"已删除 + id"）、`{ totalMs, turnCount, counts, tokens }`。
- **byModel**：数组，每项含 modelId、modelName（无法解析时与 modelId 同值）、同上字段集；不再按 workspace 切分；无法归桶的 residual 使用稳定内部 key 返回，前端展示为 i18n 文案 `Unknown model` / `未知模型`。
- **byTool**：数组，每项含 toolKey（工具命令名）、`{ count, totalMs }`；跨 workspace、跨 model 汇总；本数组**不含 token 字段**（per-tool token 不在本期范围）。
- **daily**：数组，按日期升序，每项含日期、当日总盘 `{ totalMs, turnCount, counts, tokens }`、当日 model 分布 `models` map，以及可选的 `modelNames` map。`models` 必须使用稳定 model id（无法归桶差额使用稳定未知模型 key）作为 key，不得使用 display name 做聚合 key，避免同名模型被合并；`modelNames` 仅用于展示，解析不到时前端回退 model id。前端展示未知模型时使用 i18n 文案，并保证分层图可还原当日 total。
- **range**：实际生效的 from / to（便于前端默认值回显）。
- **note**：固定提示文案 token 为本地估算（i18n key，前端解析）；前端优先使用响应中的 `note` 做 `t(note)`，缺失时再回退默认 key。
- **failed/error**：当统计文件 IO / 解析失败时返回；数组字段仍保持 `[]` 或已聚合的部分结果，`error` 保留原始错误文本。前端必须把这类响应视为失败，不得把随响应返回的部分数组当作完整当前结果渲染。

### 范围内为空

- 三个数组各为空，`range` 字段仍如实回显，不返回 404。
- 所有数组字段都返回 `[]`，不返回 `null`。
- 前端据此渲染空态。

### 性能边界

- SDK 内部根据 from/to 计算覆盖月份集合，逐月加载；范围 ≤ 1 年内单次响应数据量在百 KB 级以内。
- 不做服务端分页；范围太大（例如 All Time）的极端情况由前端负责分块渲染（图表降采样 / 表格虚拟滚动），本期实施时再视实际数据量决定是否需要。

## 错误透传

- 沿用项目"错误信息全量展示"的原则：SDK 层错误向上抛时不做隐藏，handler 层把 message 原样写到响应。
- 统计页接口遇到统计读失败时仍返回 200 + `failed=true` + `error`，避免主页面不可用；前端把 `error` 原文展示出来。
- 前端展示错误时同样保留原文，不替换为模糊文案。

## 鉴权与多用户

- 当前是单用户本机场景，不引入额外鉴权。
- 多用户化后再加 user 维度桶（不在本期范围）。

---

# 验收要点

## 累加与持久化

- Interactive Job 完成一轮 → 当天 day header 出现 / 更新对应耗时与 +1 turn；统计页对应 model 桶按本步真实 model 增加；assistant / thought / toolcall 三类计数与 token 各自累加到对应字段。
- Loop 任务完成一次 iteration → 同样累加；若该 step 节点指定了 model 覆盖，则统计落到该覆盖 model 桶。
- Shell 单步完成 → 同样累加。
- Shell setup 阶段失败（临时脚本、workdir、pipe、start 等失败）→ 也按失败 step 计入当天 total/turn，保证“失败也消耗使用量”的口径一致。
- 一天混合多种来源（chat / loop / shell）→ 都汇到当天总盘；model 维度按真实 model 分桶。
- 跨夜 step（23:55 起、00:10 完）→ 整段计入完成那天，不拆分。
- Loop 内 per-step 切换 model（不同节点设不同 model）→ 各自的耗时与 token 入到自己的 model 桶；总盘等于各 model 桶之和（除"无法归桶"差额）。
- 重启进程 → 之前数据仍在，不被清空。
- 删除 / 损坏当月文件 → list 接口与统计接口不报错；返回空 / 部分缺失，主流程不阻塞。
- 跨月切换 → 旧月文件如有未刷脏，写入新月前先同步尝试落盘；落盘失败不阻塞主流程，但必须保留 dirty 状态等待后续重试，新月文件正常生成与写入。

## Tool 维度

- shell 类工具（tool name 含 `shell` / `bash`）→ 用 args.command 第一个 shell word 作为 bucket key（去引号、跳过 `KEY=value` 前缀；`FOO="a b" git status` 归到 `GIT`；最终统一转大写）。
- 同一步内多次调用 `bash`：`tools["LS"]`、`tools["GREP"]`、`tools["GIT"]` 各自独立累加 count 与 totalMs。
- 非 shell 类工具（例如 `file_read`、`web_search`、`Read /path`）→ 取原始 tool name 的第一个空白分隔 token 后统一转大写，分别归到 `FILE_READ` / `WEB_SEARCH` / `READ`。
- args 解析失败 / 没有 `command` 字段：回退用原始 tool name 的第一个空白分隔 token 并统一转大写，不丢统计。
- pipe / 子 shell 命令（`cat foo | grep bar`）→ 只计第一个命令 `CAT`，是已知的简化。
- 命令包装器（`sudo ls` / `time ls`）→ 仍按 `SUDO` / `TIME` 计入，不做特殊解包。
- pending 中的 tool call 异常未收到 `OnToolCallEnd`（builder 中断 / step 异常）→ 直接丢弃，不入账。
- 拼错 / LLM 幻觉的命令名（`bashh`、`grepp`）→ 照样入桶，便于排查异常调用。
- `sum(tools[*].count) == toolCallCount`（在不发生 pending 残留的常态下）。

## Job List

- 切换 workspace → header 数字立即切到对应 workspace 的总盘。
- 该日期没有完成 step → day header 不显示统计行。
- polling / focus refresh 后，如果本次 List 响应没有返回某个当前 page 日期的 dailyStats → day header 必须清掉该日期旧统计，不继续显示上一轮值。
- 当天有跑了未完成的 step → 当天数字尚未包含该 step；step 完成后下一次拉到 list 可见数字跳一次。
- 多标签页打开 List：A 端发完一轮消息，B 端刷新可见统计已更新。
- 删除 Job → 已记入的统计不变；新 step 仍按正常累加。

## 统计页

- Workspace 首页与具体 JobChat 顶栏图标点击 → 都能进入独立统计页，URL 可独立访问。
- 默认范围 7 天；切到 30d / 90d / All / 自定义 → 四视图与图表同步重算；All 展示 all-time 数据而不是默认 30 天窗口。
- 快速连续切换范围（例如 7d → 30d → 90d）→ 只有最后一次请求结果可更新页面，旧请求晚返回也不能覆盖新结果。
- All Time 起点只受真实统计文件或内存中有数据/脏写入月份影响；Job List 读过但无数据的空月份不会把范围拉早。
- 度量切换器（Duration / Counts / Tokens）→ 四视图同步切换主度量；可下拉细分 assistant / thought / toolcall。
- 顶部 banner 提示 token 为本地估算（中英文）。
- By Workspace donut legend → 点击跳到对应 workspace 的 Job List；从 JobChat 打开统计页后点击也必须离开旧 JobChat；workspace 已删除显示"已删除/Deleted + id"。
- By Model 横向条形图 → model 名能正常解析；解析不到时显示原 id；"无法归桶"用 `Unknown model` / `未知模型` 单列出来，且跟随语言切换。
- By Tool 横向条形图 → 命令名按数据模型规则取得；shell 类按命令名拆开；非 shell 取原 tool name 第一个空白分隔 token 后统一转大写。
- By Tool 在 Counts 模式下展示工具名与调用次数。
- By Tool 在 Tokens 模式下显示空态文案"按工具维度暂不区分 token"，segmented control 不隐藏。
- 趋势图 → 选中范围每一天都有 X 轴占位；hover 显示当天明细；图例可切换显隐；颜色与 model 一致；不按 tool 分层；两个不同 model id 即使 display name 相同，也必须保持独立分层，不得合并统计。
- 范围内全空 → 显示空态文案，不显示空表 / 空图。
- 统计接口空结果 → `byWorkspace` / `byModel` / `byTool` / `daily` 返回 `[]` 而不是 `null`，前端不崩溃。
- 同一时段加载多次 → 数据一致。
- 统计接口失败 → 响应包含 `failed/error` 原始错误信息；前端展示原始错误信息与重试按钮；如果页面已有上一次成功结果，失败时不清空旧结果。
- 统计接口返回 `failed/error + partial data` 且页面没有上一次成功结果 → 只显示错误与重试，不渲染残缺图表。
- `GET /api/v1/stats/usage?from=未来日期` 且未传 `to` → 服务端补齐 `to=今天` 后返回 400，不返回 200 空结果。
- By Workspace / By Model / By Tool 按当前度量主项降序排序，hover tooltip 展示完整明细。
- 本期单个统计汇总接口加载失败 → 按整页错误处理并可重试；如果后续拆分为多 section 数据源，单视图加载失败时其它视图仍渲染，失败视图局部错误并展示原始错误信息。

## 数据口径与一致性

- `tokens.assistant + tokens.thought + tokens.toolCall` 与 `tokens.total` **不强制相等**——在文档与 UI 提示文案中明确说明。
- `sum(byModel[*].totalMs) == sum(byWorkspace[*].totalMs)`：无法归桶的部分以 `Unknown model` / `未知模型` residual 行补齐，避免和总盘 / 趋势图口径不一致。
- 不同时间范围的同一天数据一致；切换范围不应改变历史日期上的数字。

## 国际化

- 中英文切换后所有文案（"turns" / "次"、"Duration" / "时长"、提示 banner、列头、空态）正确切换。
- 时长 / token 大数（K / M）格式中英文一致。

## 性能边界

- 单步 step 收尾位置增加的累加调用对主流程的影响 < 10 ms（节流写不阻塞主路径）。
- 统计页范围查询 / Job List dailyStats 查询不得在磁盘 IO、JSON 解析或大范围聚合期间持有写入互斥锁；并发查询最多短暂阻塞 `Record` 的内存 clone/cache 更新临界区。
- 30 天范围统计接口响应数据量在百 KB 级以内。
- All Time 范围接口响应不报错；前端能展示（必要时降采样 / 虚拟滚动）。
