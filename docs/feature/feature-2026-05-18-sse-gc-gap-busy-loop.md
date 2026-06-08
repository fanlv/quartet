# SSE GC Gap 紧循环：Stop 后发消息收不到回复

created: 2026-05-18

## 现象

Job 暂停（Stop）后，用户发送消息，UI 卡在 loading，看不到 AI 回复。必须再发一条新消息才能收到推送（两条一起出现）。

## 复现条件

- Interactive job（ACP agent 等快速回复场景）
- 用户 Stop 后立即 send_message
- Agent 回复极快（同秒完成）
- SSE 连接在 Stop 之后因网络/代理原因发生过断开重连

## 根因

`event_buffer.go` 的 `Read` 函数在特定 GC 状态下进入紧循环（busy loop），导致 SSE handler 永远不发 keepalive，客户端认为连接死了。

### 触发时序

1. SSE reader A 正常连接，收到 Stop 事件 → `MarkTerminal()` → GC 禁止
2. 用户发消息 → `SendMessage` → `resumeGC()` → GC 重新激活
3. 快速 run 产生事件，期间 GC 持续清除已 ack 的旧事件
4. Run 完成 → terminal event(seq=N) 发布 → GC 在 `MarkTerminal()` 前最后跑一轮
5. Reader A 读到最后事件 ack → cursor=N，nextSeq=N+1
6. SSE 连接因网络/代理原因断开
7. 客户端重连 Subscribe(N) → 新 reader cursor=N
8. `Read` 等待条件 `cursor(N) < nextSeq(N+1)` 为 true → 跳出等待循环
9. `indexAfter(events, N)` 找不到 seq > N 的事件（buffer 中仅剩 seq=N 或已全部 GC）
10. 返回空 entries + ok=true → SSE handler 立刻再调 Read → **紧循环**
11. 永不写 keepalive → 客户端收不到数据 → 断开重连 → 同样问题重复

### 本质缺陷

`Read` 的等待条件（`cursor < nextSeq`）与实际可交付事件不一致。当 GC 清除了 cursor 到 nextSeq 之间的所有事件后，等待条件成立但无事件可返回，导致 Read 返回空 slice（ok=true），调用方无限重试。

## 修复方案

将 `Read` 函数的事件收集逻辑包裹在外层 for 循环中。当收集结果为空（GC gap）时，将 cursor 推进到 `nextSeq` 并重新进入内层等待循环，正确阻塞直到新事件到达。

修复后行为：
- GC gap 时 cursor 追上 nextSeq → 内层等待条件 `cursor < nextSeq` 不再成立 → 正確阻塞
- 新事件发布时 nextSeq 递增 → 等待条件重新成立 → 正常交付

## 涉及文件

| 文件 | 说明 |
|------|------|
| `services/job/event_buffer.go` | Read 函数增加 GC gap 防护，外层 for + cursor 推进 |
| `services/job/event_buffer_test.go` | 新增 `TestBuffer_GCGapDoesNotBusyLoop` 覆盖此场景 |

## 与前次修复的关系

docs/feature-2026-05-17-sse-reconnect-race-on-fast-run.md 修复了"terminal 事件主动断开 SSE 导致空窗期"的问题（去掉 `[DONE]`，SSE 不再因 terminal 事件关闭）。本次修复解决的是另一个层面：即使 SSE 因外部原因（网络/代理）断开后重连，buffer 的 GC gap 也不会导致紧循环。两个修复互补。

## 验证方法

1. 打开一个 interactive job（ACP agent）
2. 发送一条消息，等回复完成
3. 点 Stop
4. 立即发送一条新消息
5. 验证 AI 回复正常展示，不会卡 loading
6. 观察后端日志不出现反复快速的 `[sse][TRACE-SEQ0] subscribe req` (相同 startSeq)
