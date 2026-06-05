# 04 · IM 网关（飞书 / 企微）的接入控制

> 范围：`cmd/web/handler/im_gateway.go`。

## 1. 准入流程

收到一条 IM 消息后的处理顺序：

1. **群消息**优先派发：若 `msg.ChatType == ChatTypeGroup` 且能匹配到群命令路由，直接处理（按 bot 在群中的身份）。
2. **管理员配置探测**：调 `g.adminStatus(platform, senderID)`，得到 `(adminConfigured, isAdmin)`：
   - `adminConfigured == false` —— 该平台**根本没有**任何管理员配置 → drop（debug 日志）。
   - `isAdmin == false` —— 发送者不是管理员 → 走 pending contact 流程，不真正处理消息。
   - `isAdmin == true` —— 走真正的消息处理。

含义：**IM 通道只对管理员白名单开放**，不在白名单里的用户不会触发任何业务逻辑。

## 2. Pending Contact 限流

非管理员发送的消息会被记录到 pending contact 列表，便于管理员后续审批添加。为避免被刷爆 / 日志被刷屏，几个限制写在 `im_gateway.go` 文件顶部常量：

- `maxPendingContacts = 20` —— pending 队列上限。
- `pendingLogInterval = 1 minute` —— 同一个发送者多久内只打一次"新联系人 pending"的 Info 日志。
- `pendingLogRetention = 10 minutes` —— rate-limit map 里 entry 的 TTL。
- `pendingLogSweepInterval = 1 minute` —— 后台清理过期 entry 的频率。

## 3. 文件下发链路

`im_gateway.go` 的文件相关分支也接到了 `isPathInAllowedRegion`（详见 01 篇）。这意味着即便管理员通过 IM 命令请求"把 `/etc/shadow` 发我"，服务端依然会被白名单挡掉。

## 4. Bot 自身消息过滤

群消息派发里有 `botSenderID(msg.Platform)` 检查，避免 bot 自己发的消息触发自循环。

## 5. 没有的能力

- 没有"按命令权限分级"的概念（管理员要么能做所有事，要么进不来）。
- 没有审计日志（动作在普通业务日志里）。
- IM 收到的消息**默认会走完整 Job 链路**，意味着管理员可以通过 IM 控制 quartet 在本机执行任意操作 —— 这是设计意图，但部署者要意识到这等同于"拿到管理员 IM 身份就拿到本机 shell"。
