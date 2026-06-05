# Bug Fix: Loop 模式用户消息重复显示

日期：2026-05-20

## 问题描述

Loop 模式下，用户发送的消息在聊天界面中重复显示。同一条用户消息会出现两次。

## 根因分析

Loop 模式的 SSE 事件 `ITERATION_STARTED` 触发时，前端会创建一条**合成用户消息**（ID 前缀为 `loop-user-`），用于在历史消息尚未加载时立即展示用户输入。

后续当以下任一路径执行时，会从 history API 加载同一条消息的**持久化版本**（ID 为 positional 格式 `session-X:msg_N`）：

1. `syncJobState`（SSE 重连 / idle-watchdog / job 结束时触发）
2. 初始化加载 Step 1（加载 active session 历史）
3. 初始化加载 Step 2（后台并行加载 remaining sessions 历史）

在消息合并阶段（`setMessages` 回调），`existingOnly` 过滤逻辑只通过以下条件去重：
- `historyIds.has(m.id)` — 按 ID 精确匹配
- `m.clientMessageId` — 过滤乐观 UI 的交互式用户消息

但合成消息：
- ID 为 `loop-user-*`，和 history 的 `session-X:msg_N` 不同 → 不命中 ID 去重
- 没有 `clientMessageId` → 不命中乐观消息过滤

结果：合成版本和历史版本同时存在于 messages 数组中，且都标记相同的 `sessionId`，因此 `filteredMessages` 过滤时两条都会展示。

## 修复方案

在所有消息合并路径的 `existingOnly` / `sseOnly` 过滤逻辑中，增加对合成 loop 用户消息的语义去重：

```typescript
// 构建 history 用户消息的 (sessionId, content) 集合
const historyUserKeys = new Set<string>();
for (const hm of historyMessages) {
  if (hm.role === 'user' && hm.sessionId) {
    historyUserKeys.add(`${hm.sessionId}\x00${hm.content}`);
  }
}

// 在 existingOnly 过滤中增加：
if (m.role === 'user' && m.id.startsWith('loop-user-') && m.sessionId) {
  if (historyUserKeys.has(`${m.sessionId}\x00${m.content}`)) return false;
}
```

逻辑：如果 history 中已存在相同 `sessionId + content` 的用户消息，说明该合成消息的持久化版本已到位，合成版本可以安全丢弃。

## 影响范围

修改文件：`web/src/hooks/useJobChat.ts`

修复位置（4 处）：
1. `syncJobState` 的消息合并回调
2. 初始化加载 path 1（有 progress.results）的 Step 1
3. 初始化加载 path 1 的 Step 2
4. 初始化加载 path 2（无 progress.results）的 Step 2

## 为什么后端每个 session 都有相同的用户消息

后端 loop 模式下，每次迭代将完整对话上下文（包括之前所有轮次的 user/assistant/tool 消息）存入当前 session 的 `messages.jsonl`，作为 LLM 的输入上下文。因此 history API 对每个 session 返回的消息中都包含之前积累的用户消息。前端通过 `sessionId` tag 区分不同 session 的消息，`filteredMessages` 按 `activeSessionId` 过滤确保只展示当前选中 session 的消息。
