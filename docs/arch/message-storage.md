# 消息存储

最后更新：2026-07-25

本文描述一条消息（用户消息 / Agent 回复 / 工具结果）如何在磁盘上落地：目录布局、写入路径、原子性、并发锁、去重、损坏恢复与内存缓存。SSE 实时推送不落盘，见 [sse-event-buffer-design.md](./sse-event-buffer-design.md)；拉取见 [message-pull.md](./message-pull.md)。

## 一句话结论

- 会话 / Job 等**元数据**是单文件 JSON，走「临时文件 + fsync + rename」原子整写。
- **聊天消息**是 per-session 的 `.meta/messages.jsonl`，**追加为主**，截断/缝合时整文件原子重写。
- 并发写由**进程级 sessionDir 锁**串行化；读加速由**进程级 LRU 缓存**承担。
- **持久化是唯一事实源**：SSE 事件纯内存，页面任意时刻都能从磁盘重建。
- 历史压缩（summary）已下沉到 agent 子进程内部（如 eino-cli 的 `~/.eino/`），quartet 不再维护 session 级 summary 文件，历史按镜像原样返回。

## 磁盘布局

根目录由环境变量 `LOCAL_MEMORY` 决定（必须绝对路径），所有路径拼接集中在唯一文件 `types/path/path.go`。与消息相关的关键路径：

```
{LOCAL_MEMORY}/quartet/data/workspaces/{wsID}/jobs/{jobID}/
├── .meta/job.json                         Job 元数据（单文件 JSON）
├── graph_run/events.jsonl                 图运行结构化事件（追加 JSONL，仅 graph）
└── sessions/{sessionID}/
    ├── .meta/meta.json                     Session 元数据（单文件 JSON）
    └── .meta/messages.jsonl                聊天消息（追加为主 / 重写时整写）★核心
```

| 实体 | 路径 API（`types/path/path.go`） | 格式 | 写方式 |
|------|------|------|------|
| 聊天消息 | `MessagesFilePath` | JSONL，一行一条 `schema.Message` | 追加为主；截断/缝合整写 |
| Session 元数据 | Session `.meta/meta.json` | 单文件 JSON | 原子整写 |
| Job 元数据 | `JobMetaFile` | 单文件 JSON | 原子整写 |
| 图运行事件 | `GraphRunEventsFile` | JSONL | 追加，用行号做游标 |
| IM 消息 / user-input | `im/{chatID}/YYYY-MM-DD.jsonl` 等 | 按日 JSONL | `O_APPEND` 追加 |

外部来源 ID（platform/chatID）经 `safeExternalID` 净化（含 `..`/分隔符则回退 `sha256-<hex>`）；内部 ID 经 `validateID` 拒绝空、NUL、`..`、路径分隔符，防目录穿越。`LOCAL_MEMORY` 缺失时相关路径函数直接 panic，避免数据静默落到进程 CWD。

## 写入路径

消息单元直接复用 eino 的 `schema.Message`（存储层没有自定义消息结构体），每条序列化为一行 JSON。落盘经过统一编排层与仓库层：

```
Agent 执行器（acp / shell）
  → services/agent/chatctx.ChatContextManager        编排：截断孤儿尾、追加、缝合占位
    → repository/chat_context.go（chatContextRepo）   仓库：JSONL 追加 / 整文件重写
      → repository/atomic.go AtomicWriteFile          原子整写（元数据 / 重写路径）
      → JSONL 追加原语（O_APPEND + fsync）             追加路径
```

三种写形态：

1. **普通追加**（助手回复、shell 结果的主路径）：`ChatContextManager.AppendMessages`（`manager.go:121`）→ `chatContextRepo.AppendMessages`（`chat_context.go:347`）→ JSONL 追加。**只追加，不重写全文**，写后失效缓存。
2. **BeginRun**（新一轮用户消息入口）：`manager.go:200`，在一把写锁内先 `truncateOrphanedTailLocked`（`manager.go:287`，清掉「tool_use 无配对 tool_result」的孤儿尾），再追加。截断失败绝不追加，防止下一轮把新消息一起截掉。若上游已预写（graph 节点带 `KeyPrePersisted`）则只截断不追加，保证「恰好一次」。
3. **全量重写**：`ReplaceMessages`（`chat_context.go:388`，整块 join 后原子整写）；`ReplacePlaceholderToolResult`（`chat_context.go:440`，原地替换占位 tool_result 那一行，行数不变）。

Job / Session 元数据写：各自 `Save` 做 `json.Marshal` → `AtomicWriteFile` 整文件原子重写；Job 侧还有持久化重试与回滚（见「持久化失败」）。

## 原子性与并发锁

**原子整写**（`repository/atomic.go:27` `AtomicWriteFile`）：同目录建临时文件 → 写 → fsync → rename → 再 fsync 父目录。crash 后要么旧要么新，无中间态。JSONL 追加靠 `O_APPEND` + fsync，单进程内不会撕裂行（但不是「整批多行」原子，靠上层 session 锁串行化）。

**三层锁，粒度不同：**

1. **聊天上下文文件锁（最关键）** — `repository/session_locks.go`。进程级全局注册表 `sessionFileLocks`，**按 sessionDir 分键**（`session_locks.go:33`）。原因：acp / shell / web-reload 会各建不同的 `chatContextRepo` 实例但指向同一目录，per-instance 锁无法串行化跨实例的「读-改-写」，会丢消息。锁体是 **ctx 感知的读写锁** `ctxRWMutex`（`session_locks.go:72`）：读用 `RLock`，追加/重写/缝合用 `Lock`；`acquireLock`（`session_locks.go:98`）让加锁与 `ctx.Done()` 竞争，persist 超时能及时解绑而不是无限阻塞在慢盘上。`WithLock`（`chat_context.go:153`）暴露免重复加锁的句柄，让「截断→追加」在一把写锁内原子完成。锁永不回收（单用户，泄漏可忽略）。
2. **每实体 Save 分片锁** — Session / Job 仓库各用 64 路 `sync.Mutex`（FNV(id) 取模），让不同实体 Save 互不阻塞、同一实体 Save 串行。
3. **Job service 持久化分片锁** — `services/job` 用 64 路分片锁串行化「DeepCopy + Save」，并有严格锁序（persist 分片锁 → `s.mu` → run-resource 锁，禁止反向嵌套）。

## 去重与幂等

存储层**没有消息级 dedup key**（`messages.jsonl` 每行是裸 `schema.Message`）。`msgextra.KeyMsgID`（`msg_id`）只用于前端事件关联，不参与落盘去重。幂等靠上层三机制：

- **`KeyPrePersisted`**：graph 节点入队时已写盘，BeginRun 检测到全部预写即跳过追加。
- **占位符缝合**（`ReplacePlaceholderToolResult`）：dedup key = `ToolCallID` + 占位标记；真结果晚到时原地替换而非新增一行，避免占位与真结果并存。
- **孤儿尾截断**：新一轮前清掉 SIGKILL/panic/写失败残留的未配对尾段。

## 损坏与恢复

- **`messages.jsonl` 逐行容错**：读时（`loadAllMessagesLocked` `chat_context.go:203`）遇到无法解析的行 Warn 后跳过；`ReplacePlaceholderToolResult` 对坏行原样保留、绝不重写丢弃（否则一处局部损坏会被整文件重写永久放大成历史丢失）。
- **单文件 JSON**：settings / recent-dirs 等解析失败时 `backupCorruptFile` 改名备份为 `<path>.corrupt.<ns>` 再重建；但 `job.json` / `meta.json` 解析失败是直接返回 error（`LoadAll` 对单个坏 job 记 Error 后跳过，不阻塞其它）。
- **进程重启恢复**：启动时把仍是 `Running` 的 job 重置为 `Failed` 并写 `LastError="interrupted: process restarted while running"`，只保留最后一条迭代结果的内容以省内存。

## 内存缓存

`repository/chat_context_cache.go` 是进程级、字节受限的 LRU（`globalMessagesCache`，`chat_context_cache.go:64`），缓存已解析的 `[]*schema.Message`：

- 键 = 文件绝对路径；有效性用 `(size, mtime)` 校验，不匹配即 miss，绝不返回过期内容。
- 默认预算 64MiB（`QUARTET_MESSAGES_CACHE_BYTES` 可覆盖，设 0 关闭）；单会话超预算不缓存。
- 写路径（append/replace/stitch）写完显式 invalidate，作为 mtime 亚秒粒度别名的兜底。
- **持久化是唯一事实源，缓存只是读加速**：动机是长 loop 的 jsonl 可达数 MB，web reload / SSE reconcile 每次都读整段历史，反复 Unmarshal 主导繁忙 job 的加载耗时。

## 已知坑

- **多实例同 session 丢消息**：必须用进程级 sessionDir 锁而非实例锁（`session_locks.go:8` 注释）。
- **ACP 漂移误判**：仅靠 message count 不够——占位缝合原地重写行数不变，两个不同磁盘态可能同 count。故引入 `MessagesFingerprint`（count + 内容哈希，`chat_context.go:287`），把 `ACPLastSyncedMessageCount/Hash` 持久化到 session meta，漂移才换新 ACP session。坏行曾导致 count 分歧触发 spurious reset——**不要因并发错误 reset session，会丢全部上下文**（见 `AGENTS.md`）。
- **JSONL 追加非「多行原子」**：靠 session 写锁 + 单进程 `O_APPEND` 串行保证。
- **持久化失败分级**：recovery-critical 写失败回滚 run 态；best-effort 写失败只记降级警告、不重试（防磁盘满时放大 IO）。

## 关键文件

| 角色 | 文件 |
|------|------|
| 路径规则 | `types/path/path.go` |
| 原子写 | `repository/atomic.go` |
| 聊天上下文仓库 | `repository/chat_context.go` |
| Session 锁 | `repository/session_locks.go` |
| 消息缓存 | `repository/chat_context_cache.go` |
| 编排层 | `services/agent/chatctx/manager.go` |
| Session / Job 仓库 | `repository/session.go`、`repository/job.go` |
| Job service 持久化 | `services/job/executor_store.go`、`executor_persist.go` |
