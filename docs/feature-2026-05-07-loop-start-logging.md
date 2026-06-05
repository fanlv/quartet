# Loop Start 事件按天落盘 —— 重构方案

> 所属项目：Quartet 重构
> 状态：设计
> 延续 2026-05-03 《用户输入消息按天落盘》的双轨窄口径思路，不新增独立目录。
> 纯文字描述文档，不含实现代码。

---

## 一、目标

把 **"用户点击 Start Loop"** 这个事件（含当时的 `LoopConfig` 快照）作为一类**用户输入**，与现有 Web/IM 聊天消息**混写到同一个 `user_input/YYYY-MM-DD.jsonl`**，用字段区分。

为什么算"用户输入"：

- 是用户主动触发的一次"意图表达"，跟"发一条消息"是同一层级，只是载体不是文本而是结构化配置。
- 事后回溯 / 离线分析需要能答出："那天主人启动了哪些 Loop？配置是什么？" —— 现有的 `user_input/` 流是答这类问题最自然的地方。
- Job Progress / Step 日志记录的是**执行过程**；user_input 记录的是**启动动作**。两者层级不同，互不替代。

范围约束：

- **只记 Start 动作**，不记 Loop 执行过程、不记 Loop 结束；过程走现有 Job Progress。
- **Start 成功才落**；前置校验失败或 service 报错的 Start 不算"被系统正式接纳的用户输入"，不写入。
- **Web 侧**接入；IM 侧目前没有 Start Loop 入口，本期不接。

---

## 二、现状梳理

| 维度 | 现状 |
|---|---|
| 启动入口 | Web：`POST /api/v1/job/:jobId/start`；IM：无 |
| `LoopConfig` 的生命周期 | 创建 Job 时随 `CreateJobRequest` 落到 `Job.LoopConfig` → Start 时由 runLoop 取走执行 |
| 已有持久化 | Job meta 里存一份（含 LoopConfig），Job Progress 记录执行事件；没有一条"启动动作"意义上的记录 |
| 现有 user_input 条目 | 仅"消息"一类：`content + imageUrls`，source=web/im |

---

## 三、方案

### 3.1 数据模型扩展

给 `UserInput` 条目加一个判别字段 + 载荷字段（保持所有条目仍是同一张表）：

- **`kind`（新增）**：`message`（默认，兼容现有所有条目）/ `loop_start`（本次新增）。未来如果要记 loop_end / loop_update，沿用此字段继续扩枚举。
- **`loopConfig`（新增，可选）**：仅 `kind=loop_start` 时填充，直接内嵌当时的 `LoopConfig` 快照（完整 `flow / variables`，已废弃的兼容字段也一并带上），原样存储。
- **`content`（复用）**：写一条**人类可读摘要**，形如 `Start Loop: <模板/自定义标题> (<N> steps, <M> vars)`；纯粹服务于"打开 jsonl 一眼能看懂"，不承担结构化信息的角色。
- **`imageUrls`（复用）**：loop_start 一律空。

其他现有字段（`messageId / receivedAt / source / platform / jobId / workspaceId / chatId / senderId`）沿用原语义。老条目（`kind` 缺省）视为 `message`，无需迁移。新字段都 `omitempty`，文件里不会把 message 条目撑胖。

**不做模板溯源字段**：不引入 `templateId / templateName`。原因是模板是否编辑过无法在后端准确判断，引入会带来"是不是忠实反映"的疑问；`loopConfig` 快照本身就是事实的唯一真相，够用。未来真有诉求再加。

### 3.2 接入点

Web 侧：`JobStart` handler 内，**以下条件全部通过之后**才落盘：

1. `jobId` 解析成功、Job 存在、归属当前 workspace。
2. `Job.LoopConfig != nil`（非 Loop 模式的 Start 不落 loop_start）。
3. `jobService.Start` **返回成功**之后再落盘。

落盘动作：按当时的 `Job` 状态构造 `UserInput(kind=loop_start, source=web, platform=web, jobId, workspaceId, loopConfig=<job.LoopConfig 快照>, content=<摘要>)` → 调 `userInputRepo.Append`。

`receivedAt` 在 handler 入口处取一次本地毫秒时间戳，与现有 Web 消息落盘的采样时机保持一致，便于两类条目混排时的时间顺序正确。

### 3.3 Start 失败时不落

- 前置校验失败（Job 不存在、LoopConfig 为空、workspace 不匹配等）→ 不落 user_input。
- `jobService.Start` 返回错误 → 不落 user_input。
- 原因：user_input 的总基调是"被系统正式接纳的输入"，失败的 Start 不满足。把噪音混进去会污染"真实启动动作"的统计口径。
- 错误信息本身仍按 `CLAUDE.local.md` 要求**原样返回前端**，不隐藏。
- 落盘失败（磁盘错误等）与 Start 失败是两件事，不要互相吞：Start 成功但 Append 失败时，只打 error 日志、不阻塞主链路、不向前端报错。

### 3.4 快照的大小与完整性

- **整份 LoopConfig 原样落**：6 ~ 10 个节点量级下也就几 KB，jsonl 完全装得下。
- 不压缩、不截断、不按字段白名单过滤：这份落盘就是给人看、给后续回放用的，残缺的快照没价值。
- `variables` 里如果有敏感值：当前是单用户本机环境（`CLAUDE.local.md` 明确说了不考虑权限），不做脱敏；未来多用户化再加策略。

### 3.5 重启 / 重跑

- 同一 Job 多次成功 Start → 每次各落一条 `loop_start`，靠 `receivedAt` 区分先后。
- 不做去重、不合并。

---

## 四、与现有 user_input 的关系

继续遵守 2026-05-03 方案的三条原则：

- **扁平目录**：仍写到 `{LOCAL_MEMORY}/user_input/YYYY-MM-DD.jsonl`，不按 kind 拆子目录。
- **按天聚合**：按 `receivedAt` 本地日期分片。
- **消费者字段筛选**：未来做回溯 UI / CLI 时，按 `kind` 过滤出 `loop_start` 得到"启动动作时间线"；按 `jobId` 串起"一个 Job 的所有相关用户输入"。

命令消息一律不落的约定对 loop_start 不适用 —— Start Loop 不是命令，本身就是合法的用户动作。

---

## 五、验收要点

- 点击 **Start Loop** 并成功启动 → `user_input/今日.jsonl` 追加一条 `kind=loop_start`，`loopConfig` 完整且可反序列化回结构。
- Start 失败（前置校验不过 / service 报错）→ **不落** loop_start 条目；错误信息如实返回前端。
- 同一天多次成功 Start → 多条独立条目。
- 同一 Job 多次 Start → 多条独立条目。
- 跨零点 Start → 按 `receivedAt` 落到对应日期文件。
- 纯自定义 Loop / 基于模板的 Loop → 条目形态一致，都只靠 `loopConfig` 快照还原。
- 落盘失败 → 对话 / Loop 主流程不受影响，error 级日志能排查。
- 既有 `kind=message` 条目不受影响（字段缺省兼容）。

---

## 六、后续可扩展项（不在本期范围）

- **loop_end**：Loop 结束时再落一条，带最终状态 / 总耗时，形成完整 lifecycle 回放。
- **loop_config_updated**：Loop 运行中如果支持热改配置，变更点也落一条，diff 追溯。
- **读接口 / CLI**：按 `kind + 日期 + jobId` 检索，支撑"今日启动了哪些 Loop"之类的报表。
- **Variables 脱敏策略**：多用户化后按字段名 / 前缀做掩码。
- **IM 侧 Start Loop**：未来 admin 命令支持 `start_loop` 时，同模型直接复用（`source=im`）。
