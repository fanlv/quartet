# 消息拉取

最后更新：2026-09-05

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
- 首屏完成后后台预取更早 80 条；首帧只渲染一页，随后立刻把预取的这一页补进可见时间线，稳定维持两页缓冲。
- 取下一页的触发点是「距已加载内容顶部一页」，不是滚到最顶：用户进入最上面那一页时下一页就已在路上，不会到顶干等一次网络往返。
- 每次 prepend 都必须按新增条数扩大渲染窗口。窗口是尾部对齐的，没扩窗时整页新数据会落进窗口之外的隐藏区，而隐藏区就在列表顶部——用户刚才还在看的内容会当场从渲染里消失。
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

## 重连 / 恢复时的「最新页回灌」

重连恢复、idle 看门狗重同步、410 重建都会重新拉一次**最新页**再合回内存列表，入口是 `mergeLatestHistoryPage`（`web/src/utils/mergeMessages.ts`）。最新页只描述时间线的尾部，因此它做的是**拼接**而不是重建：

```
[ 内存中比该页更早的消息 ] [ 最新页 ] [ 实时尾巴 ]
```

- 以「内存列表里第一条同时出现在该页中的消息」为切点：切点之前的消息该页管不着（用户往上翻出来的更早页、以及在超长轮次里已经被挤出最新页的那条用户消息），原位保留。
- 切点之后、该页覆盖范围内的已结束消息以磁盘版本为准：一轮里多个流式气泡会被折叠成一条落盘 assistant 记录、只保留最后一个流式 ID，保留折叠前的气泡会让同一段正文渲染两次。
- 该页覆盖范围内的**临时消息**（system/命令气泡、还没确认的乐观用户消息、仍在流式中的气泡）尚未落盘，保留并排在页后面。
- 该页与内存列表完全没有交集时，已结束的前缀视为更早历史留在页前，只有末尾连续的临时消息排到页后。

回灌同时**不回退向前翻页的游标**：内存里已经保留了更早的页，把游标重置回最新页会让下一次上滚重复拉取已经渲染出来的历史。

Web 与 iOS 的回灌走同一套拼接语义。iOS 只在「这一页属于当前列表正在展示的 session」时拼接；页属于别的 session（Graph 换节点、显式指定 targetSession）时才整体替换。

## 正在执行的那条消息落在窗口之外

消息队列快照把后端正在执行的那条消息标为 `active`，而 run 在产出任何内容之前就已经把它写进 transcript。前端据此**不能**把「这条消息不在内存列表里」读成「它还没发出去」——列表只是最新页，超长轮次会把开启这一轮的用户消息挤到窗口之上。按「不在列表里就追加」处理，用户自己的提问就会排到回答它的那些气泡下面。

判定改成三档：

- 已在列表里 → 什么都不做。
- 窗口已经覆盖到会话开头，或这条消息比窗口里最新的一条还新（刚开始执行的新消息）→ 追加到末尾。
- 其余情况（已落盘、位于窗口之上）→ 作为「轮首占位」**钉在已加载窗口的顶部**。

占位气泡与真实记录共用同一个 ID。任何合并路径都不按页序摆放它：上滚翻页拿到真实记录时占位让位，真实记录回到它在页里的正确位置；页里还没有它时占位继续浮在最前面。不给它做后端侧的「把页起点回退到轮首」，因为一轮的字节数没有上限（现网单个 transcript 已有 30MB 量级），回退整轮会让单次响应爆掉，而加了上限又恰好在最大的那些轮次上失效。

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
- **回灌最新页吃掉消息**：分页后回灌只拿最新页，若按「只保留内存里的临时消息」重建列表，超长轮次里已被挤出最新页的用户消息、以及用户往上翻出来的更早页都会消失；最新页恰好为空时整个列表会被清空。修复：改成按切点拼接（见上一节），空页直接返回原列表。
- **上滚翻页误删工具气泡**：向前翻页时按 `toolCallId` 去重方向是反的——agent 复用 `call_1`/`call_2` 这类 ID 时，会用更早的同名调用删掉尾部正在显示的工具气泡。修复：向前翻页只按消息 ID 去重（工具气泡的 ID 本身就是 `toolCallId`，真重叠已被覆盖）。
- **新消息滚不到底**：列表只比视口高一点点时「贴顶」和「贴底」同时成立，先判贴顶会把浏览态锁死，之后自动跟随底部永久失效，用户刚发出的消息落在视口下方看不见。修复：滚动处理先判贴底，贴顶只用来触发向前翻页。
- **Agent 解析请求风暴**：会话引用了本机未安装的 Agent 时，对话页会以「渲染 → 强制重新解析 → 解析完成通知 → 再渲染」的方式无限循环请求 `agent/display-info/resolve`（实测约 12 次/秒），在 HTTP/1.1 下抢占同域连接。修复：`getSessionMeta` 保持稳定引用，可见引用的解析走缓存、不再强制刷新（目录变更仍走失效 + 强制刷新那条路径）。
- **正在执行的消息被追加到页尾**：队列快照里的 `active` 已经落盘，但超长轮次会把它挤出最新页；按「不在内存列表里就追加」处理，用户的提问会渲染在回答它的气泡下面（Web 与 iOS 同源缺陷）。修复：见上一节的三档判定，页外的那条钉在窗口顶部而不是追加。
- **prepend 的一页被停在窗口之上**：渲染窗口尾部对齐，翻页分支里的链式取页忘了按新增条数扩窗，于是整页新数据落进列表顶部的隐藏区。表现是用户正看着的那条（尤其是代表窗口之上那条消息的轮首占位）在上一页加载完成的瞬间消失。修复：所有 prepend 路径统一扩窗，另加「距顶部一页」的预取触发点，让用户根本不必滚到顶。
- **iOS 回灌吃掉更早的页**：iOS 的最新页回灌一直是整体替换 `messages`，分页之后等于每次重连/回前台都把用户上滚翻出来的更早页丢掉。修复：改成与 Web 相同的拼接语义，只有「页属于另一个 session」时才替换。

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
| iOS 分页 / 合并 / 队列气泡定位 | `ios/Quartet/Features/Chat/ChatViewModel.swift` |
| 渲染窗口与翻页触发 | `web/src/components/MessageList.tsx`、`ios/Quartet/Features/Chat/JobChatView.swift` |
