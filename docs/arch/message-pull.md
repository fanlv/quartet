# 消息拉取

最后更新：2026-09-04

本文描述前端如何通过 REST 拉取历史消息，以及如何把「拉取到的历史」和「SSE 实时推送」合并成一份有序、无重复的列表。落盘见 [message-storage.md](./message-storage.md)；实时推送见 [sse-event-buffer-design.md](./sse-event-buffer-design.md)。

## 一句话结论

- 拉取分两个接口：`GET /job/:jobId`（快照 + `lastEventSeq`，**不含正文**）+ `GET /sessions/:sid/messages`（该 session 的倒序游标分页历史）。
- 客户端分页读取磁盘镜像（`.meta/messages.jsonl`）；历史压缩只发生在 agent 子进程内部，不改写 quartet 侧的镜像。
- 历史结构（粗粒度整条消息）与 SSE 事件结构（细粒度流式）**不同**，靠后端打的**稳定 `msg_id` / `thought_msg_id`** 让两条链共享 ID，前端据此合并去重。
- Web 与 iOS 首屏展示最新一页，同时预取前一页；进入已预取页时继续预取更早一页。

## 拉取接口

| 接口 | Handler | 返回 |
|------|---------|------|
| `GET /api/v1/job/:jobId` | `JobGet`（`job.go:249`） | Job 元数据平铺 + `lastEventSeq`（SSE 续点），**无消息正文**，响应头 `Cache-Control: no-store` |
| `GET /api/v1/sessions/:sessionId/messages?paged=true&limit=80&before=...` | `GetSessionMessages` | 该 session 的倒序游标分页历史（`GetMessagesResponse`） |
| `GET /api/v1/sessions/:sessionId/token-usage` | `GetSessionTokenUsage` | 私有页面独立计算完整 session 的上下文 token，首屏之后异步刷新 |
| `GET /api/v1/job/:jobId`（列表 `JobList`） | `job.go:153` | Job 列表，`cursor` + `limit` 游标分页 + 弱 ETag 304 |

客户端流程：先打 `/job/:jobId` 拿 `sessionIds` 与 `lastEventSeq`，再拉当前 session 最新页，展示后以 `lastEventSeq` 订阅 SSE，并在后台预取前一页。

## 历史返回：倒序游标分页

客户端显式传 `paged=true` 使用分页；默认每页 80 条原始记录，上限 200 条。首次不传 `before`，从文件尾部读取最新页；响应中的 `page.beforeCursor` 用于继续向前读取。页内顺序仍是磁盘追加序。无分页参数保留旧的全量读取行为，供尚未迁移的调用使用。

分页正文不再同步扫描完整历史计算 token。客户端在最新页显示后异步请求 token 元数据，因此大历史的上下文数字可能稍后更新，但不会阻塞对话首屏。

游标冻结首次读取时的历史上界。运行期间在文件末尾追加新消息不会让旧游标失效；截断、占位结果缝合或外部改写若改变冻结边界，则返回冲突，让客户端重新同步，避免静默错页。服务端还会把分页起点扩展到 assistant/tool 组合的边界，防止工具调用和结果被拆到不同页。

ACP 时代 `messages.jsonl` 是从 agent 事件重建的**镜像**（与 claude 等一致），历史压缩在 agent 子进程内部完成、不回写镜像，因此接口不再做「summary 头 + 尾部」的投影，而是直接按镜像顺序分页。eino 时代的 session 级 `summary.json` 投影已随 eino 子进程化一并删除。

消息 ID 规则（`session.go` 内）：优先用 `Extra[msg_id]` 的稳定 ID；否则用 `sessionId:msg_<下标>`。带 `thought_msg_id` 且有 reasoning 的 assistant 消息，会在读时**额外拆出一条独立的思考条目**（用 `thought_msg_id` 作 ID），让思考气泡在历史里也有独立 ID，能与 SSE 的思考气泡对齐。

## 历史结构 vs 事件结构

两者结构完全不同，前端在 `loadHistory` 里做「粗 → 细」转换：

| | 历史消息（`HistoryMessage`） | SSE 事件（`model.Event`） |
|---|---|---|
| 粒度 | 一条完整消息（含 toolCalls[]、reasoning、时间戳） | 细粒度流式：`TEXT_MESSAGE_*` / `TOOL_CALL_*` / `ITERATION_*` … |
| 用途 | 刷新/首屏重建 | 实时逐字动画、状态更新 |
| 对齐键 | `msg_id` / `thought_msg_id`（存 `Extra`） | `messageId` / `toolCallId`（同一取值） |

黏合点是**稳定 ID**：后端发消息时给每条打稳定 `msg_id`（首条用前端乐观 ID `ClientMessageID`，其余 UUID，`job_message.go:293` 起），历史读时优先复用。于是同一条消息在历史里的 ID 和 SSE 里的 `event.messageId` 是同一个值，前端 `mergeMessages` 才能靠 ID 去重。

## 消息分页与预取

Web 与 iOS 使用相同的两屏窗口：

- 当前 session 同步加载最新 80 条并立即渲染；若遇到 assistant/tool 组合边界，会向前扩展少量记录保证组合完整。
- 首屏完成后后台预取更早 80 条，但不立刻插入可见时间线。
- 用户滚入上一屏时提交已缓存页并保持滚动锚点，同时开始预取再前一页。
- 当前 session 读完后，以同样方式跨到前一个 session；Graph 只为当前选中的 session 保持分页窗口。

## 前端合并去重

入口 `mergeMessages`（`web/src/utils/mergeMessages.ts:56`），`existing` = 内存中 SSE 累积的实时消息，`incoming` = 拉取到的历史：

1. **主键 = 消息 `id`**：incoming 命中 existing 同 ID 时，若 existing 内容更长（流式还在累积）则保留 existing，避免历史短内容覆盖实时长内容。
2. **语义 key 兜底**（处理短暂 ID 不一致）：
   - 乐观用户消息：existing 的 `clientMessageId` 出现在 incoming 即丢弃。
   - **纯思考气泡**：按 `sessionId + thinkingContent`（`mergeMessages.ts:23`）——live 思考气泡 ID 与持久化 `thought_msg_id` 短暂不一致时的兜底。
   - tool 消息：按 `toolCallId`。
3. **最终 `dedupeById`**：保证结果按 ID 唯一，连 incoming 内部重复 ID（reconnect 重放的 `call_*`）也清掉。

**有序性**：不做时间戳全局重排。顺序 = 数组插入序（历史在前、实时追加在后）。SSE 实时事件不走 mergeMessages，而是按 `messageId`/`toolCallId` 在列表里 `findIndex` **就地更新**；已 `Finished` 的气泡不被 replay 的空事件回退（`TOOL_CALL_STITCHED` 是特例，即使 Finished 也就地重写，把占位改成真结果）。

## 公开分享（public share）

只读子集挂在 `/api/v1/public/*`（`api.go:177`），走 `shareTokenMiddleware`：

- 鉴权走 `?shareToken=` query（常量时间比对 Job 上的 `ShareToken`），**不是** header token。
- `PublicGetSessionMessages` 先校验 `sessionId` 属于该 share job，再复用 `GetSessionMessages`。
- 前端 `apiUrl` helper 在 public 模式给每个 URL 换前缀并追加 `?shareToken=`，SSE 也走同一鉴权。

## 已知坑

- **首连 seq=0 → 410**：SSE effect 若抢在 hydration 前发出，`Last-Event-ID` 为空 → 服务端解析成 `startSeq=0` → buffer 已 GC 则立刻 410。修复：`snapshotReady` 门控，必须等快照把续点种好才连。
- **thought 消息重复**：历史把思考拆成独立条目，SSE 也建独立气泡，两者 ID 短暂不一致时按 ID 去重会漏。兜底 = `sessionId + thinkingContent` 语义去重。
- **命令气泡重复**：slash 命令结果同时走 POST 响应 inline 和 SSE transient 两条路，SSE 连接的 tab 收到两份。修复：`command\0present\0text` 签名 + 10s 窗口去重；命令气泡是 transient，刷新即消失。
- **乐观消息在 410 恢复时丢失**：往长空闲 job 发消息触发 410 时消息还没落盘，直接用磁盘历史 replace 会让它消失。修复：`reloadMessagesFromDisk` 用 `mergeMessages` 保留 `clientMessageId` 不在历史中的乐观消息。
- **placeholder tool 误画成绿色完成**：中断/取消时 round builder 合成 placeholder 结果，刷新时若强扫成 Success 会与用户所见矛盾。后端用 `Placeholder`/`PlaceholderReason` 结构化标志，前端按 `placeholder > failed > success` 优先级渲染。
- **时钟偏移**：DurationBadge 用 `Date.now() - event.timestamp` 会混用客户端/服务端时钟。修复：用 HTTP `Date` 头种服务端时钟，并对 replay 的老时间戳设容忍窗口。
- **重连重复消息**：`onReconnect` 历史 reload 与 SSE 事件处理竞态。修复：`metadataOnly` + `syncGenerationRef` stale 保护。

## 关键文件

| 角色 | 文件 |
|------|------|
| 路由 | `cmd/web/api.go` |
| 历史消息 Handler | `cmd/web/handler/session.go` |
| 快照 / 列表 Handler | `cmd/web/handler/job.go` |
| 发消息 / 打稳定 ID | `cmd/web/handler/job_message.go` |
| 公开分享 Handler | `cmd/web/handler/job_public.go` |
| 前端集成 Hook | `web/src/hooks/useJobChat.ts` |
| 合并去重 | `web/src/utils/mergeMessages.ts` |
| 前端 SSE 客户端 | `web/src/utils/sse-client.ts` |
