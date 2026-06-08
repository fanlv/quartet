# SSE 重连竞态：快速完成的 interactive run 事件丢失

created: 2026-05-17

## 现象

1. **Stop 后发消息收不到 SSE 推送**：Job 暂停（stop）后，用户发送消息，UI 卡在 loading，看不到 AI 回复。刷新页面也无法恢复，必须再发一条新消息才能收到推送。
2. **重复消息**：客户端偶尔收到两条内容完全相同的 assistant 回复，刷新后变为一条。

## 复现条件

- interactive job（非 loop）
- 用户 stop 后立即 send_message
- agent 回复极快（同秒完成，如 ACP agent 的短回复）

## 时序分析

以 `job-20260517-095623-830313-5b9b1d10` 的后端日志为例：

```
10:09:50  run stopped
          → 后端 publish JOB_STOPPED + buffer MarkTerminal
          → SSE handler 写 [DONE]，关闭 stream
10:09:50  [DONE] → 前端 onComplete → attemptConnect(0) 启动 (async)
          ↓
          ↓  attemptConnect 内部第一步：await syncJobState()
          ↓  → HTTP GET /job/:id（网络 IO，耗时若干毫秒到数百毫秒）
          ↓  → 此时旧 SSE reader 已从 readStream return，新 client 还没建
          ↓  → **无活跃 SSE subscriber 的空窗期**
          ↓
10:10:03  run starting (send_message)   ← 用户在空窗期发了消息
10:10:03  run finished                  ← agent 同秒完成，事件 seq 1145-1151
          ↓
          ↓  syncJobState 返回
          ↓  → lastEventSeqRef = SnapshotSeq() = 1151（buffer nextSeq）
          ↓  → disconnect 旧 client → 新 SSE subscribe(startSeq=1151)
          ↓  → 新 reader cursor=1151，从 1152 开始读
          ↓  → **seq 1145-1151 的全部事件（含 AI 回复和 JOB_COMPLETED）被跳过**
          ↓
10:10:03  SSE subscribe startSeq=1151   ← 新 client 建好，但事件已全部错过
10:10:21  SSE subscribe startSeq=1151   ← 用户刷新，还是 1151
10:10:49  SSE subscribe startSeq=1151   ← 再刷新
10:10:52  run starting (send_message)   ← 用户再发一条，这次 SSE 正常收到
```

## 根因

### Bug 1：快速 run 事件丢失

旧的设计中，每次 terminal 事件（JOB_COMPLETED / STOPPED / FAILED）到达后，后端写 `[DONE]` 并关闭 SSE 连接，前端收到 `[DONE]` 后异步重连（`onComplete` → `attemptConnect(0)` → `await syncJobState()` → 新建 SSE client）。

问题出在 `readStream` 收到 `[DONE]` 时立即 return，旧 SSE reader 已释放；而新 SSE client 需要先完成一次 HTTP GET（syncJobState）才能建立。这个异步空窗期内没有活跃的 SSE subscriber。如果用户此时发消息且 run 极快完成，所有事件被 publish 到 buffer 但无人消费。新 client 建好后，从 SnapshotSeq（= buffer nextSeq）开始读，整个 run 的事件被跳过。

### Bug 2：重复消息

用户因 Bug 1 看不到回复，手动再发了一条相同意图的消息。两次 run 产生相同内容但不同 message ID 的回复，前端去重按 ID，无法合并，UI 上显示两条。刷新后从 history 加载合并为一条。Bug 1 修复后此问题自然消失。

## 修复方案

### 最终采用：SSE 连接不因 terminal 事件关闭（消除空窗期）

**核心思路**：一条 SSE 连接覆盖整个 job 生命周期，不因 terminal 事件断开重连。SendMessage 和 Continue 复用同一个 buffer，新事件自然推送到已有 reader。只有 Start（resetForRun 关闭旧 buffer）和 Delete 才触发 SSE 断开。

**后端改动**：
- SSE handler 的 read loop 不再检测 terminal 事件类型来退出循环
- 不再发送 `[DONE]` 标记
- SSE 连接仅在 buffer 被 close/reset（Start、Delete）或写失败（客户端断开、TCP 超时）时退出

**前端改动**：
- 删除 `[DONE]` 的处理逻辑和 `onComplete` 回调
- 删除 `onComplete` 触发的断开→重连机制（`attemptConnect(0)` 调用）
- 原来由 `onComplete` → `attemptConnect` → `syncJobState` 做的元数据同步，改为在 JOB_COMPLETED / JOB_STOPPED / JOB_FAILED 事件的 handler 中直接调用 `syncJobState`

**为什么这个方案最好**：
1. 消除整类问题——不存在"无 subscriber 空窗期"，任何 Publish 的事件都有活跃 reader 消费
2. 删代码多于加代码——去掉 `[DONE]` 写入、`onComplete` 回调、重连重试逻辑
3. 无性能差异——旧设计 `[DONE]` 后立刻重连等于一直有连接，改后同理
4. resetForRun 自然兼容——Start 调用 resetForRun 关旧 buffer → reader 收到 ReadClosed → SSE 正常退出 → 前端 onDisconnect → reconnect

### 曾考虑但未采用的方案

- **方案 A（保持旧 reader 存活）**：在 `attemptConnect` 的 `syncJobState` 完成前不释放旧 reader。缺点是旧 reader cursor 不再推进会阻碍 GC，且仍在修补 `[DONE]` 断开重连的设计漏洞。
- **方案 B（复用旧 client 的 lastEventId）**：新 SSE client 用旧 client 的 lastEventId 而非 SnapshotSeq 作为 startSeq。改动最小但没有消除根因。
- **方案 C（SendMessage 返回 startSeq）**：后端 response 带 SnapshotSeq，前端立即建 SSE。改动涉及前后端接口，且与现有 "event 通过已有 SSE 推送" 的架构不一致。

## 涉及文件

| 文件 | 说明 |
|------|------|
| `cmd/web/handler/job_lifecycle.go` | SSE handler read loop 去掉 terminal flag 和 `[DONE]` 写入 |
| `web/src/utils/sse-client.ts` | 删除 `[DONE]` 处理和 `onComplete` 接口 |
| `web/src/hooks/useJobChat.ts` | 删除 `onComplete` 回调；terminal event handler 加 `syncJobState` |

## 验证方法

1. 打开一个 interactive job，发消息等回复完成
2. 点 Stop
3. 立即发送一条新消息（不等 SSE 重连完成）
4. 验证 AI 回复正常展示，不会卡 loading
5. 验证不会出现重复消息
6. 刷新页面后消息列表与刷新前一致
7. Start（loop job）→ SSE 自动重连（resetForRun 关闭旧 buffer）
8. 网络断开 → SSE 自动重连
