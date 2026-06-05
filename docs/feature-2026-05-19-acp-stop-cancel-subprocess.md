# ACP Stop 后子进程 Session 状态异常导致新消息无响应

## 问题现象

Interactive 模式下：用户发消息 → 点 Stop → 再发新消息 → 新消息 1 秒内完成，没有任何有意义的回复。

日志表现：

```
21:30:46 [job.lifecycle] run starting: jobId=... action=send_message
21:30:49 [acp] agent ready: acpSession=ef5bd284...
21:31:00 [acp] run context cancelled, notifying subprocess
21:31:01 [job.lifecycle] run stopped: jobId=... runOutcome=stopped
21:31:14 [job.lifecycle] run starting: jobId=... action=send_message
21:31:14 [job.lifecycle] run finished: jobId=... runOutcome=completed  ← 0秒完成，无输出
```

第二轮 run 复用了缓存的 ACPAgent，cancel 通知也发了，但子进程的 Prompt 秒回空内容。

## 根因分析

### 问题分两层

#### 第一层：cancel 通知未发送（已修复）

executor.Stop() 只取消 Go context，不直接通知子进程。旧 Run 退出后 `a.cancel = nil`，新 Run 的 prevCancel 路径和 CancelActivePrompt 路径都无法触发 cancel。

**修复**：在 Run() 的 cleanup defer 中，`runCtx.Err() != nil` 时同步发送 `cancelACPSubprocessSession`。

#### 第二层：子进程 session 被 cancel 后进入脏状态（本次修复）

Cancel 通知发出后，子进程（@agentclientprotocol/claude-agent-acp）确实停止了当前 turn。但该 session 进入了一种"已取消"状态——后续在同一 session 上发 Prompt，子进程立即返回空响应（nil error，无 stream events）。

Go 侧 `SendPrompt` 返回 nil → `Run()` 返回 nil → executor 认为 completed → 用户看到空消息。

### 关键时序

```
t0  旧 Run 开始，持有 promptSlot，acpSession=ef5bd284

t1  用户点 Stop → executor 取消 context
    → runCtx 被取消
    → SendPrompt() 返回 context.Canceled

t2  cleanup defer 触发：
    ① cancelACPSubprocessSession(ef5bd284) → 子进程停止当前 turn
    ② needSessionReset.Store(true)         → 标记 session 需重置
    ③ EmitPendingEnds / CollectMessages
    ④ slot.Release() / a.cancel = nil / 释放 runSem

    清理后：子进程空闲，但 session ef5bd284 处于脏状态

t3  用户发新消息，新 Run() 进入：
    ① reconnectIfNeeded → 子进程存活，跳过
    ② needSessionReset == true → 调用 resetACPSession
       → 创建新 session，设 needReplay=true
    ③ 在新 session 上正常 SendPrompt → 子进程正常回复 ✅
```

## 修复方案

两部分配合：

### 1. Cancel 时标记 session 脏（cleanup defer）

在 `cancelACPSubprocessSession` 之后设置 `a.needSessionReset.Store(true)`，标记当前 session 后续不可复用。

### 2. 下次 Run 时检测并重置

Run() 在 `reconnectIfNeeded` 之后、`snapshotTransport` 之前检查 `needSessionReset` 标志。若为 true，调用 `resetACPSession` 创建全新的子进程 session 并设置 history replay。

### 为什么不在 cancel defer 里直接 resetACPSession

- `resetACPSession` 需要网络 I/O（NewSession RPC、fingerprint 计算、持久化）
- 放在 cleanup defer 里会延长 runSem 持有时间，增加下一个 Run 的等待
- 放在下一次 Run 入口是"延迟修复"模式：只在真正需要时才做 I/O，不增加 cancel 路径开销

### 安全性

| 关注点 | 分析 |
|--------|------|
| 重复 reset | `CompareAndSwap(true, false)` 保证只触发一次 |
| 正常完成路径 | `needSessionReset` 只在 `runCtx.Err() != nil` 时设置，不影响正常流程 |
| Cancel 通知 | 仍然同步发送，有 2s 超时上界，保证子进程停止当前 turn |
| 与 slot 的顺序 | cancel 在 slot.Release() 之前（LIFO），不存在竞态 |
| resetACPSession 失败 | CAS 已清 flag → 恢复 `needSessionReset.Store(true)` → 返回 error → 下次 Run 重试 reset 而非静默打到脏 session |

## 影响范围

- 文件：`services/agent/acp/agent.go`
- 新增字段：`needSessionReset atomic.Bool`
- 仅影响 context 被取消（用户 Stop / 超时）后的下一次 Run 路径
- 不改变 executor / JobRunner 接口抽象
