# 06 — 共享与协作

> **已废弃：** 本文中的资源归属与组织内分享方案不在本期范围。当前共享实例方案见 `docs/feature/feature-2026-08-23-user-auth-permission-management.md`。

## 1. Job 分享（现有功能适配）

### 现状

- Job 通过 `shareToken` 生成公开链接，任何人可通过 `/api/v1/public/*` 只读访问
- share 路由走 `shareTokenMiddleware`，不需要用户认证

### 多用户下的变更

#### 公开分享（Public Share）

- 功能不变：shareToken 继续用于匿名只读访问
- **owner 定位机制**：多用户模式下 job 数据在 `$LOCAL_MEMORY/users/{userID}/workspaces/...` 下，匿名请求只有 `jobID + shareToken`，缺少 userID 无法定位数据目录。需要新增全局索引：
  - 在 SQLite 中新增 `public_shares` 表，记录 `shareToken → ownerID + workspaceID + jobID` 的映射
  - 或维护一个全局索引文件 `$LOCAL_MEMORY/global/share_index.json`
  - 推荐使用 SQLite 方案，与 `job_shares` 表一起存储
- **share 创建时**：在 job.json 中写入 shareToken 的同时，向 `public_shares` 表插入映射记录
- **public 请求处理**：`shareTokenMiddleware` 先通过 `public_shares` 表根据 shareToken 查到 ownerID 和 jobID，再定位数据目录读取 job。**必须校验 URL path 中的 `jobId` 与 `public_shares` 记录中的 `job_id` 一致**，不一致返回 404（防止 tokenA + jobB 的越权读取）

**public_shares 表结构**：见 [02-storage.md](./02-storage.md) 第 4 节"数据库表设计"中的 `public_shares` 表定义。

**public_shares 生命周期：**

| 事件 | 操作 |
|---|---|
| share 创建 | 插入或更新 `public_shares` 记录 |
| unshare（取消公开分享） | 删除对应 `public_shares` 记录 |
| job 删除 | 级联删除该 job 的 `public_shares` 记录 |
| workspace 删除 | 级联删除该 workspace 下所有 job 的 `public_shares` 记录 |
| 用户禁用 | 查询时必须检查 owner `Status == "active"`，禁用用户的 public share 立即不可访问（强约束）；同时可选批量删除索引作为优化，但不作为唯一安全保障 |
| 用户删除（软删除） | 批量删除该用户所有 `public_shares` 记录 |

#### 组织内分享（Org Share，Phase 2）

- 新增"组织内分享"：不生成公开链接，而是允许指定用户 ID 有读权限
- share 记录存到 job.json 里：顶层新增 `sharedWith` 字段（被分享用户 ID 列表，可选）。公开分享继续使用现有的顶层 `shareToken` 字段，不引入新的 `share` 嵌套对象
- **反向索引**：在 SQLite `job_shares` 表中同步写入分享记录（见 02-storage.md 第 4 节），支持被分享者高效查询

**Org Share API 契约（Phase 2）：**

| 路由 | 方法 | 认证 | 说明 |
|---|---|---|---|
| `/api/v1/job/:jobId/share/users` | POST | JWT | 新增 org share：将 job 分享给指定用户 |
| `/api/v1/job/:jobId/share/users/:userID` | DELETE | JWT | 取消 org share：取消对指定用户的分享 |
| `/api/v1/job/:jobId/share/users` | GET | JWT | 查询 org share：获取当前 job 的被分享用户列表 |

- `POST /api/v1/job/:jobId/share/users`：请求体 `{ "userIds": ["user-xxx", "user-yyy"] }`；只有 job owner 可操作；目标用户必须 `Status == "active"`，否则返回 400；重复分享幂等（upsert）；响应 `{ "code": 0, "sharedWith": [...] }`
- `DELETE /api/v1/job/:jobId/share/users/:userID`：只有 job owner 可操作；目标不存在返回 404；响应 `{ "code": 0 }`
- `GET /api/v1/job/:jobId/share/users`：只有 job owner 可查看完整列表；响应 `{ "code": 0, "users": [{ "id": "...", "username": "..." }] }`
- 被分享者通过 `GET /api/v1/job/list?scope=shared` 查看分享给自己的 job 列表（已在下方"被分享者的体验"中描述）
- 权限校验：非 owner 操作返回 404（不暴露 job 是否存在）

**job_shares 生命周期：**

| 事件 | job.json 操作 | job_shares 操作 |
|---|---|---|
| 新增 org share | 向 `sharedWith` 追加用户 ID | 插入 `job_shares` 记录（upsert，见下方唯一约束） |
| 取消 org share（unshare） | 从 `sharedWith` 移除用户 ID | 删除对应 `job_shares` 记录 |
| job 删除 | 删除 job 目录 | 级联删除该 job 的所有 `job_shares` 记录 |
| workspace 删除 | 删除 workspace 目录 | 级联删除该 workspace 下所有 job 的 `job_shares` 记录 |
| owner 禁用 | — | 保留记录，查询时校验 owner `Status == "active"`，禁用 owner 的分享对被分享者不可见 |
| owner 删除（软删除） | — | 批量删除该 owner 的所有 `job_shares` 记录 |
| 被分享者禁用/删除 | — | 批量删除 `shared_with_id` 为该用户的所有 `job_shares` 记录 |

**一致性策略：`job.json` 是真源，`job_shares` 是派生索引。** 写入顺序：**先写 `job_shares` 索引，再写 `job.json`**。`job.json` 的写入成功作为整个操作的 commit 点：
- `job_shares` 写入失败：API 返回 500，`job.json` 未变更，权限未生效——语义一致
- `job_shares` 写入成功但 `job.json` 写入失败：API 返回 500，`job_shares` 中存在多余索引——但因为读取时会二次校验 `job.json`（见下方），多余索引不会导致越权，后续 reconciliation 会清理
- 两步均成功：API 返回 200，权限生效

查询 "Shared with me" 时通过 `job_shares` 高效定位，读取 job 详情时**二次校验** `job.json` 中 `sharedWith` 是否包含当前用户（防止索引与文件不一致时越权）。若发现不一致，以 `job.json` 为准并异步修正索引。

**索引修复机制：** 启动时或定时扫描所有 job 的 `sharedWith` 字段，以 `job.json` 为准重建/补齐 `job_shares`——删除多余索引、补齐缺失索引。

### 被分享者的体验

- 在"Shared with me"视图里看到别人分享给自己的 Job
- 只读：能看消息历史，不能发消息、不能修改 job
- API 侧：在 `ListJobs` 里增加 `scope=shared` 参数，通过 `job_shares` 表查询 `shared_with_id = 当前 userID` 的所有 job，再根据 `owner_id` 定位数据目录读取 job 详情

**Org Share 资源（图片/附件/文件）访问契约：**

被分享者查看消息历史时，消息中可能引用图片、附件等文件资源。访问流程：

1. 被分享者请求签名 URL：`POST /api/v1/files/sign-url`，body 中携带 `jobId`（被分享的 job）和 `path`（资源相对路径）
2. 后端校验：通过 `job_shares` 表确认当前 userID 对该 job 有只读权限
3. 后端定位资源：用 `owner_id + workspace_id + job_id` 定位 owner 的 session 目录（`$LOCAL_MEMORY/users/{ownerID}/workspaces/{wsID}/jobs/{jobID}/sessions/`）
4. 路径限制：签名 URL **仅允许**访问 job 的 session 目录内的文件，不允许访问 owner 的 workspace workdir 或其他目录
5. 签名 URL 的 `purpose` 字段设为 `"shared_file_access"`，payload 额外包含 `owner_id` 和 `job_id`
6. `/api/v1/serve-file` 校验签名 URL 时，验证 `purpose`、path 范围、owner 归属和 job_shares 权限

## 2. Workspace 协作（Phase 2）

Phase 1 不做 workspace 级协作。每个 workspace 严格归属一个用户。

Phase 2 可考虑：
- workspace 增加 `members` 字段，支持邀请其他用户
- 成员角色：owner（完全控制）、editor（可创建 job）、viewer（只读）
- 协作的原子性由文件锁保证（现有机制已有 per-entity 锁）

## 3. IM 消息路由

### 现状

- IM 消息（Lark 通过 WebSocket 长连接、WeChat 通过 iLink 长连接）进入，路由到 `im_workspace_id` 指定的 workspace

### 多用户下

- 每个用户有自己的 IM 配置（Lark App ID/Secret、Lark IM sender ID、WeChat 绑定）
- IM 消息入口需要区分消息属于哪个用户：
  - Lark：每个用户独立的 `lark.Manager` / WebSocket 连接，通过 app_id 对应到用户
  - WeChat：每个用户独立的 `wechat.Listener`，通过 bot 账号对应到用户
- 路由逻辑：IM message → lookup user by app/bot → route to user's im_workspace_id
- 不同用户的 IM 数据完全隔离

### IM 运行时设计

**全局唯一索引：**
- `lark_app_id` 平台内唯一：一个 Lark app 只能绑定一个用户。用户保存 IM 配置时，校验 `lark_app_id` 是否已被其他用户占用；若冲突则拒绝保存
- WeChat bot ID 平台内唯一：同理，一个 WeChat bot 只能绑定一个用户
- 索引存储在 SQLite `im_bindings` 表，唯一约束为 `UNIQUE(platform, app_or_bot_id)`（见 02-storage.md 第 4 节），或维护在全局内存 map 中并在启动时从各用户 settings 扫描重建

**Listener 拓扑：**
- **Lark**：当前实现为 WebSocket 架构（每个 `Manager` 持有单组 `appID/secret`，通过 Lark SDK 的 `larkws.NewClient` 建立 WebSocket 连接）。多用户下需要 per-user Manager 实例，每个 Manager 独立管理各自的 WebSocket 连接。推荐方案 A：
  - **方案 A（保留 WebSocket，推荐）**：为每个绑定用户创建独立的 `lark.Manager` 实例，通过 `im_bindings` 表管理 app_id 到 Manager 的映射；用户配置变更时重启对应 Manager 的 WebSocket 连接
  - **方案 B（迁移 HTTP webhook）**：改为共享一个 HTTP webhook 入口，收到事件后从 event payload 中提取 `app_id`，查 `im_bindings` 定位用户。需要 Lark 开放平台配置 webhook URL 且 HTTPS 公网可达
- WeChat：当前实现仅支持单账号（`Listener.Start` 只处理 `creds[0]`，多余账号直接忽略并输出 warn 日志）。多用户下需要重构为多实例架构：
  - 每个绑定用户启动独立的 `wechat.Listener` 实例，各自持有独立的 credentials、iLink Monitor、Replier cache
  - Manager 层按 `userID` / `botID` 维护 Listener 实例的生命周期（创建、重启、停止）
  - 用户禁用/删除时精确停止对应 Listener，释放 iLink 长连接资源

**配置变更处理：**
- 用户保存/修改 IM 配置时：更新 `im_bindings` 索引；Lark 需停止旧 Manager 的 WebSocket 连接并创建/重启新连接（app_id/secret 变更必须重启）；WeChat 同样需重启或创建对应的 listener
- 用户清除 IM 配置时：停止对应 Lark Manager / WeChat listener，再删除 `im_bindings` 索引，释放 app_id / bot ID

**用户禁用/删除/重新启用清理：**
- 禁用用户时：停止该用户的所有 IM listener（Lark Manager / WeChat Listener），将对应 `im_bindings` 记录的 `status` 设为 `'inactive'`
- 重新启用用户时：将 `im_bindings` 记录恢复为 `'active'`，按当前配置重建对应的 Lark Manager / WeChat Listener
- 删除用户时：停止对应 Lark Manager / WeChat Listener，删除 `im_bindings` 记录，释放 app_id / bot ID 供其他用户绑定

**IM listener 生命周期状态机：**

| 事件 | Lark | WeChat | im_bindings |
|---|---|---|---|
| 首次绑定 | 创建 Manager，启动 WebSocket | 创建 Listener，启动长连接 | 插入记录 |
| 修改 app_id/secret 或 botID | 停止旧 Manager → 创建新 Manager | 停止旧 Listener → 创建新 Listener | 更新记录（释放旧 ID，占用新 ID） |
| 仅修改路由配置（sender ID、workspace） | 更新内存路由表，无需重启 | 更新内存路由表，无需重启 | 不变 |
| 清除配置 | 停止 Manager | 停止 Listener | 删除记录 |
| 禁用用户 | 停止 Manager | 停止 Listener | status → inactive |
| 重新启用用户 | 重建 Manager | 重建 Listener | status → active |
| 删除用户 | 停止 Manager | 停止 Listener | 删除记录 |

## 4. 上传文件隔离

- 上传目录从 `$LOCAL_MEMORY/uploads/` 改为 `$LOCAL_MEMORY/users/{userID}/uploads/`
- `/api/v1/serve-file` 校验文件归属，支持两种鉴权方式：`Authorization: Bearer` header（fetch 请求）或短期签名 URL 的 `token` query 参数（浏览器原生请求如 `<img src>`），详见 [04-frontend.md](./04-frontend.md) 第 3 节"浏览器原生资源请求的鉴权"

## 5. 暂不支持的协作场景

以下场景 Phase 1 明确不支持，后续按需迭代：

- 多人同时编辑同一个 Job/Session
- 跨用户的模型共享计费
- 用户组/团队概念
- RBAC 细粒度权限（只有 admin 和 user 两个角色）
