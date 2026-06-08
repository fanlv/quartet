# 用户输入消息按天落盘 —— 重构方案

> 所属项目：Quartet 重构
> 状态：已实施（Web/IM 两侧均已接入 `user_input/` 落盘）
> 纯文字描述文档，不含实现代码

---

## 一、目标

新增一套**"真正的用户输入"**按天落盘机制，与现有的"IM 全量流水"并存，各司其职。便于后续按日期回溯真实对话、做离线分析。

范围约束：

- **只记用户输入**，不记 Agent 回复。回复仍按现状走 SSE 事件 + Job Progress 持久化。
- **口径是"被系统正式接纳的用户输入"**：
  - IM 侧：必须是**私聊**且发送者是配置的 **admin（owner）**。群聊、陌生人来访、pending 待批准都不写入。
  - Web 侧：**所有消息都写入**（登录用户默认视为 owner，Web 没有陌生人概念）。
- **双轨制，不取代现有 IM 流水**：原有 `{LOCAL_MEMORY}/im/{chatID}/YYYY-MM-DD.jsonl` 继续作为"IM 全量审计流"保留不动；新增的 `user_input/` 目录承担窄口径"真实输入"职责。两者用途不同、字段不同、实现不共用。
- **按天聚合、扁平目录**：不按 chatId / jobId 分子目录，所有来源的条目按时间写到同一天的文件里，靠字段筛。

---

## 二、现状梳理

### 两个入口的消息路径

| 维度 | IM（飞书 / 微信） | Web ChatPage |
|---|---|---|
| 入口 | IM Gateway 的 WebSocket 事件回调 | HTTP 接口 `POST /api/v1/job/:jobId/message` |
| 预处理 | 权限校验、Job 映射、命令解析 | 命令快速路径、Session 解析、附件处理 |
| 核心调用 | 同一个 `jobService.SendMessage` | 同左 |
| 原始消息落盘 | 已有：按 chatId + 按天分片到 `{LOCAL_MEMORY}/im/{chatID}/YYYY-MM-DD.jsonl` | 无独立落盘，只随 Job Progress 间接保留 |

两者在**核心执行层共用**，但在**入口层和存储层分裂**。IM 侧已经有按天的 jsonl，Web 侧完全没有独立的"用户消息"持久化。

### 现有 IM 消息模型

IM 侧已经落盘的字段：MessageID、Platform、ChatID、ChatType、SenderID、MessageType、Content、ReceivedAt。存储是追加式 jsonl，每天一个文件，按 chatId 分子目录。

---

## 三、目标方案

### 3.1 存储布局

在 `LOCAL_MEMORY` 下新增**扁平目录** `user_input`，不再按会话 / chatId / jobId 分子目录：

```
{LOCAL_MEMORY}/user_input/
  ├── 2026-05-02.jsonl
  ├── 2026-05-03.jsonl
  └── ...
```

- 一天一个 jsonl 文件，文件名用 `YYYY-MM-DD` 本地日期。
- 所有来源（IM、Web、未来接入的新平台）的条目**混写**到同一个日期文件。
- 按需要筛选来源 / 会话时，由消费者读文件后用字段过滤。

**不按 chatId / jobId 分子目录的理由**：用户诉求是"按天"这一个维度；多维度目录会让每天的条目散落在很多文件里，跨会话按时间检索反而更麻烦。保留单一维度（时间）+ 字段筛，是最简单的折中。

### 3.2 数据模型

新建统一的"用户输入"模型，替代原有的 IMMessage。条目包含以下字段：

**标识**
- `messageId`：平台侧的消息 ID；Web 侧使用 clientMessageId 或新生成的 UUID。
- `receivedAt`：服务端收到时的毫秒时间戳，用于排序和跨天边界判定。

**来源**
- `source`：`im` / `web`，粗粒度来源。
- `platform`：`lark` / `wechat` / `web`，细粒度平台。

**归属（IM 独有 / Web 独有按需填充）**
- `chatId`：IM 侧的会话 ID。Web 侧为空。
- `senderId`：IM 侧发送者 ID。Web 侧为空（或用登录用户 ID 占位）。
- `jobId`：Web 侧必填；IM 侧在完成 Job 映射后填充，新建 Job 映射前的第一条消息可为空。
- `workspaceId`：可选，Web 侧随请求自然带入，IM 侧映射后可填。

**内容**
- `content`：消息文本。
- `imageUrls`：附件 URL 列表（仅 Web 侧目前有，将来 IM 支持图片时沿用）。

**说明**：模型设计以"Web 和 IM 都能装下"为准，IM 独有字段（chatId、senderId）在 Web 条目中留空，反之亦然。不为每个来源设子模型，避免多态带来的读写复杂度。

### 3.3 路径工具

`types/path` 包里：

- 新增 `UserInputDir()`：返回 `{LOCAL_MEMORY}/user_input`。
- 新增 `UserInputFilePath(t time.Time)`：根据传入时间返回当日 jsonl 的绝对路径。

日期用**服务端本地时区**的 `YYYY-MM-DD` 格式。跨时区部署需要对齐时保持约定一致即可，不在本方案内深挖。

### 3.4 仓储层

新建统一仓储接口，替代现有的 `IMMessageRepo`：

- `Append(ctx, input)`：追加一条用户输入条目，内部负责按 `receivedAt` 解析当日路径、建目录、原子追加一行 JSON。
- 未来可按需加读接口（`ListByDay` / `ListRange`），本次先不做，读需求出现后再加。

写入路径和实现沿用现有 IM 消息仓储的 sandbox 追加能力，不引入新依赖。

### 3.5 两个入口的接入点

**IM 入口**
- 位置：IM Gateway 的消息派发阶段，**以下所有检查都通过之后**才落盘：
  1. 非群聊（`ChatType == P2P`）
  2. admin 已配置
  3. 发送者是 admin
  4. 不是命令消息（命令会在 admin command 分支提前 return）
- 动作：构造 UserInput（source=im、platform、chatId、senderId、content），调 `userInputRepo.Append`。
- **jobId / workspaceId 回填**：落盘点在 `routeToJob` 之前，但会同步查询现有的 IM → Job mapping，如果该聊天已绑定过 Job，就把 jobId 和 workspaceId 一起写入；首次给一个新 chat 发消息（mapping 尚未建立）时，这两个字段留空。
- **不替代现有 `imMessageRepo.Append`**：全量 IM 审计流保持不动。user_input 是叠加的窄口径流。

**Web 入口**
- 位置：HTTP JobMessage 处理函数内，**前置校验全部通过之后**、实际 `SendMessage` 之前：
  1. 命令快速路径未命中（BypassCommand 或多消息 / 带图等绕过快速路径的情况会走到这里，下一步会再做一次命令过滤）
  2. 请求参数、sessionId 归属、agentType 与 session 匹配等前置校验均通过
- 动作：按 `messages` 数组顺序遍历，每条非空消息对应一条 JSONL 条目，构造 UserInput（source=web、platform=web、jobId、workspaceId、content、imageUrls）并 Append。
- **Web 侧无 admin 概念**：登录用户即视为 owner，所有非命令消息全部落盘。
- **空消息 / 参数错误不落盘**：前置校验失败直接返回错误，不进入 Append；落盘时也会跳过 content 为空且无附件的条目。

### 3.6 命令消息一律不落

无论 IM 还是 Web，所有命令消息（`/help`、`/status`、`/workspace`、`/new` 等）都**不**写入 user_input。原因：命令是 UI / 交互控制指令，不是对话内容，放进"用户输入"流会污染数据。

"命令"的判定以 `/` 前缀为准，**不区分已知 / 未知命令**：例如用户拼错的 `/hlep`、或任何以 `/` 开头的未定义文本，两端都按命令处理、一律不落 user_input。这样 Web 和 IM 两端在"什么算命令"上完全对齐，便于后续离线对比。

- IM 侧：admin command 分支在落盘之前 return，天然不落。
- Web 侧：命令快速路径通常会在落盘之前 return；对于快速路径不覆盖的边缘情况（BypassCommand、多消息、命令带图片），落盘前会再对每条消息按 `/` 前缀单独做一次命令识别，命中即跳过。

### 3.7 追加失败的处理

仓储写入如果失败：

- 记日志（error 级），不阻塞主链路。
- 不向上游返回错误，避免因为"日志功能"把"对话功能"带挂。
- 这与现有 IM 消息落盘的容错策略一致。

---

## 四、与现有 IM 流水的关系（双轨制）

本次不删除现有的 `repository/im_message.go` / `IMMessageDir` / `IMMessageFilePath` / `NewIMMessage`。两套存储各司其职：

| 存储 | 路径 | 口径 | 用途 |
|---|---|---|---|
| **旧：IM 流水** | `{LOCAL_MEMORY}/im/{chatID}/YYYY-MM-DD.jsonl` | 全量（群聊、陌生人、pending 全落） | IM 侧审计、追查来源 |
| **新：user_input** | `{LOCAL_MEMORY}/user_input/YYYY-MM-DD.jsonl` | 窄口径（IM admin 私聊 + Web 全部） | "真实用户输入"回溯 / 离线分析 |

两者互不替代，接入点、字段、调用位置都独立。IM Gateway 里会**同时**调两个 repo：`HandleMessage` 里保留现有的 `imMessageRepo.Append`，`dispatchMessage` 过滤后新增 `userInputRepo.Append`。

不做旧数据迁移，也不清理 `{LOCAL_MEMORY}/im/` 下的历史文件。

---

## 五、验收要点

- **IM 群聊消息**（无论是否被 @）→ 只落旧 IM 流水，不落 user_input。
- **IM 私聊 + 陌生人（非 admin）** → 只落旧 IM 流水 + pendingContact，不落 user_input。
- **IM 私聊 + admin 普通消息** → 同时落旧 IM 流水和 user_input（source=im）。
- **IM 私聊 + admin 命令**（如 `/workspace`）→ 只落旧 IM 流水，**不落** user_input。
- **Web ChatPage 普通消息** → 落 user_input（source=web、带 jobId）。
- **Web ChatPage 命令**（如 `/help`）→ 不落 user_input。
- **跨零点发送**：前后两条分别落到对应日期文件。
- **user_input 写入失败**：服务不中断，错误进日志，对话流程继续，旧 IM 流水不受影响。

---

## 六、后续可扩展项（不在本期范围）

- **读接口 + CLI**：根据日期 / 来源 / jobId 做检索。
- **Agent 回复侧落盘**：若后续需要完整对话回放，可加 assistant 方向的条目，direction 字段区分。
- **归档 / 压缩**：冷数据按月打包为 `.jsonl.gz`，`user_input/` 下只留近 N 天热文件。
- **跨时区对齐**：如果未来有跨时区部署诉求，统一约定 UTC 或带时区偏移的日期格式。
