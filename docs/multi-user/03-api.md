# 03 — API 层改造

## 1. 中间件链变更

### 现有中间件

| 路由 | 中间件 |
|---|---|
| `/api/v1/*` | `agentAuthMiddleware` → handler |
| `/api/v1/public/*` | `shareTokenMiddleware` → handler |
| `/api/v1/health` | 无中间件 |

### 多用户中间件

| 路由 | 中间件 |
|---|---|
| `/api/v1/auth/register`、`/api/v1/auth/login`、`/api/v1/auth/refresh` | 无认证（公开 auth 路由） |
| `/api/v1/auth/logout`、`/api/v1/auth/me`、`/api/v1/auth/change-password` | `jwtAuthMiddleware`（受保护 auth 路由） |
| `/api/v1/public/*` | `shareTokenMiddleware`（需改造，详见下方说明） |
| `/api/v1/admin/*` | `jwtAuthMiddleware` → `adminRoleMiddleware` → handler |
| `/api/v1/health` | 无认证 |
| `/api/v1/*` | `jwtAuthMiddleware` → handler |

> **logout 双重认证例外**：`/api/v1/auth/logout` 支持双重认证——优先 JWT header，若 JWT 缺失或过期则回退到 refresh token cookie 校验，确保 access token 过期但 refresh token 仍有效时用户仍能完成服务端 logout。

> **shareTokenMiddleware 改造**：通过 `public_shares` 表根据 shareToken 查到 ownerID 再定位数据目录，详见 [06-sharing.md](./06-sharing.md) 第 1 节 Public Share。

### `/api/v1/health` 响应契约

多用户模式下，`/api/v1/health` 的响应需要新增字段供前端 AuthGate 和注册页使用：

| 字段 | 类型 | 说明 |
|---|---|---|
| `status` | string | 服务状态（现有字段） |
| `time` | string | 服务时间（现有字段） |
| `authRequired` | bool | 是否需要认证（现有字段） |
| `multiUser` | bool | 是否多用户模式（`QUARTET_MULTI_USER=true` 时为 true） |
| `hasUsers` | bool | 是否已有注册用户（用于注册页判断"首个用户将成为管理员"） |
| `allowRegistration` | bool | 是否允许注册（对应全局设置 `allow_registration`；无用户时强制为 true） |
| `migrationStatus` | string | 迁移状态：`"none"`（无需迁移）、`"pending"`（等待首个用户注册后触发）、`"running"`（迁移进行中）、`"failed"`（迁移失败）、`"completed"`（迁移完成）。单用户模式不返回此字段 |

## 2. UserContext 注入

`jwtAuthMiddleware` 的职责：
1. 多用户模式下：从 Authorization header 提取 JWT → 验证签名和过期 → 检查 `token_version` claim 与数据库中用户的 `TokenVersion` 一致性 → **校验 `Status == "active"`** → 构造 UserContext（含 UserID、Username、Role）→ 放入 ctx
2. 单用户模式下：走现有 `X-AGENT-AUTH` 校验逻辑，注入固定 UserContext（`UserID = "default"`）

`adminRoleMiddleware`：从 ctx 取 UserContext，校验 `Role == "admin"`，否则 403。

## 3. Handler 层变更

每个 handler 从 ctx 取 UserContext，将 ctx 直接传给 service。service 内部从 ctx 获取 userID。

单用户模式下 `GetUserContext(ctx)` 返回固定的默认 UserContext（`UserID = "default"`），使所有代码路径统一。

## 4. Service 接口改造

所有 service 接口的方法统一增加 ctx 作为第一个参数，service 内部从 ctx 获取当前用户 ID 再定位数据目录。URL path 或 query 中的 workspace ID / job ID 等资源标识不变。

## 5. 内存索引多用户隔离

当前多个 service 使用内存 map 缓存数据，key 为裸资源 ID（如 jobID、wsID）。多用户模式下不同用户可能生成相同的资源 ID（尤其是默认 workspace `ws-1`），必须解决冲突。

### 隔离策略

采用**复合 key** 方案：所有内存 map 的 key 统一改为 `{userID}:{resourceID}` 格式。

需要改造的内存索引：

| 组件 | 当前 key | 改造后 key |
|---|---|---|
| Job executor 的 job map | `jobID` | `userID:jobID` |
| Job executor 的 repo map | `jobID` | `userID:jobID` |
| Job executor 的 cancels map | `jobID` | `userID:jobID` |
| Job executor 的 dones map | `jobID` | `userID:jobID` |
| Job executor 的 interactivePriorStatus map | `jobID` | `userID:jobID` |
| Job executor 的 notifiedJobs map | `jobID` | `userID:jobID` |
| Job executor 的 wsListVersion map | `wsID` | `userID:wsID` |
| SSE event buffer registry | `jobID` | `userID:jobID` |
| Workspace service 的 workspace map | `wsID` | `userID:wsID` |
| Handler 的 session service 缓存 | `jobID` | `userID:jobID` |
| **Scheduler 的 runningCount** | `scheduleID` | `userID:scheduleID` |
| **Scheduler 的 taskLocks** | `scheduleID` | `userID:scheduleID` |
| **Eino Agent cache** | `wsID/jobID/sessionID` | `userID:wsID/jobID/sessionID` |
| **ACP Agent cache** | `wsID/jobID/sessionID` | `userID:wsID/jobID/sessionID` |
| **Sandbox Manager entries / bringUps / singleflight** | `workspaceID` | `userID:workspaceID` |
| **Sandbox compose project name** | 由 `workspaceID` 派生 | 由 `userID:workspaceID` 派生（内存 map key 可直接使用 `userID:workspaceID`，但 Docker Compose project name 需 sanitize，如 `quartet-${sha256(userID + ":" + workspaceID)[:12]}`，因为冒号不是合法 project name 字符） |

单用户模式下 userID 固定为 `"default"`，与多用户逻辑统一。

### 默认 Workspace ID

当前默认 workspace ID 为固定值 `ws-1`。多用户模式下，每个用户仍保留各自的 `ws-1` 作为默认 workspace ID，通过用户目录物理隔离（`$LOCAL_MEMORY/users/{userID}/workspaces/ws-1/`）和内存复合 key（`userID:ws-1`）避免跨用户冲突。单用户模式下行为不变。

## 6. 路由变更汇总

### 新增路由

| 路由 | 方法 | 认证 | 说明 |
|---|---|---|---|
| `/api/v1/auth/register` | POST | 无 | 注册 |
| `/api/v1/auth/login` | POST | 无 | 登录 |
| `/api/v1/auth/logout` | POST | JWT 或 refresh token cookie | 登出 |
| `/api/v1/auth/refresh` | POST | 无（cookie） | 刷新 token |
| `/api/v1/auth/me` | GET | JWT | 当前用户信息 |
| `/api/v1/auth/me` | PUT | JWT | 更新个人资料（username、avatar） |
| `/api/v1/auth/change-password` | POST | JWT | 修改密码 |
| `/api/v1/admin/users` | GET | JWT+admin | 用户列表 |
| `/api/v1/admin/users/:userID` | PUT | JWT+admin | 编辑用户 |
| `/api/v1/admin/users/:userID` | DELETE | JWT+admin | 删除用户 |
| `/api/v1/admin/users/:userID/reset-password` | POST | JWT+admin | 重置用户密码（生成随机密码返回，自增 TokenVersion，清除该用户全部 refresh token） |
| `/api/v1/files/sign-url` | POST | JWT | 生成短期签名 URL（用于浏览器原生资源请求鉴权，详见下方） |

**`/api/v1/files/sign-url` 接口契约：**

- 请求：`POST /api/v1/files/sign-url`，body: `{ "path": "/path/to/file" }`
- 响应：`{ "url": "/api/v1/serve-file?path=%2Fpath%2Fto%2Ffile&token=xxx", "expiresAt": "2026-05-17T12:05:00Z" }`
- file access token 为 HMAC 签名的短期凭据，payload 至少包含：
  - `user_id`：签发用户
  - `path`：canonical file path（或 path hash）
  - `exp`：过期时间（默认 5 分钟）
  - `purpose`：固定值 `"file_access"`（防止与其他 token 混用）
- token 仅授权访问指定文件路径，不可复用为通用认证凭据
- 后端 `/api/v1/serve-file` 对 `token` query 参数校验：验证签名、过期时间、path 匹配、用户归属

### 现有路由的变更

所有现有 `/api/v1/*` 路由在多用户模式下：
- 中间件从 `agentAuthMiddleware` 切换到 `jwtAuthMiddleware`
- handler 内部通过 `GetUserContext(ctx)` 获取用户身份
- service 层根据 ctx 中的 userID 访问对应用户的数据目录

## 7. SSE 事件流隔离

现有 SSE 路由 `/api/v1/job/:jobId/events`，通过 URL 中的 jobID 订阅事件。多用户模式下：

1. 连接时从 JWT 提取 userID
2. 校验 jobID 归属该用户
3. 事件 buffer 的 key 从 `jobID` 改为 `userID:jobID`（防止跨用户订阅）

公开分享的 SSE 路由 `/api/v1/public/job/:jobId/events` 走 shareToken 校验（通过 `public_shares` 表定位 owner，详见 [06-sharing.md](./06-sharing.md) 第 1 节 Public Share）。

## 8. 数据越权防护

即使用户提供了正确的 workspace ID 或 job ID，也要验证该资源归属当前用户。因为数据是按用户目录物理隔离的，路径本身就包含 userID，天然降低越权风险。但 API 层仍需在 service 内部做归属校验，防止路径拼接攻击。不暴露"资源存在但不属于你"的信息，统一返回 404。

**Session-only 路由的 user-scoped lookup：**

`/api/v1/sessions/:sessionId/messages` 等只有 `sessionId`、不含 workspaceID/jobID 的路由，当前实现通过遍历全局 session service 缓存或全局 job list 反查 session 归属。多用户模式下必须限制查找范围：

- `getSessionByID` / `reloadSessionByID` / `lookupSessionService` 必须接受 `userID` 参数，只在当前用户 scope 内查找
- 内存缓存 key 已改为 `userID:jobID`（见第 5 节），lookup 时必须按 `userID:*` 前缀过滤，不得跨用户扫描
- 或者将 session message API 路由改为 `/api/v1/jobs/:jobId/sessions/:sessionId/messages`，通过 jobID 先校验归属再读取 session

## 9. 文件操作 API 的访问控制

`/api/v1/list-dir`、`/api/v1/read-file`、`/api/v1/write-file` 等文件操作 API：

- 现有逻辑：允许访问 `$HOME` 和 `$LOCAL_MEMORY` 下的路径
- 多用户改造：每个用户只能访问自己 workspace 的 workdir 和自己的 uploads 目录；管理员可配置额外的共享目录；文件访问 roots 函数改为接受 userID，返回该用户的合法路径列表
- **workdir 安全边界**：多用户模式下，workspace 的 workdir 必须位于管理员预授权的目录范围内（如用户 home 子目录、指定项目目录、sandbox 路径）；创建或更新 workspace 时校验 workdir 是否在允许列表中，拒绝任意宿主机路径

### 文件访问模型

多用户模式下，每个用户的合法访问范围由以下部分组成：

| 来源 | 路径 | 说明 |
|---|---|---|
| 用户 uploads | `$LOCAL_MEMORY/users/{userID}/uploads/` | 用户上传文件目录 |
| 用户 workspace workdirs | 各 workspace 配置的 workdir | 见下方 workdir 授权规则 |
| 用户 LOCAL_MEMORY | `$LOCAL_MEMORY/users/{userID}/` | 用户自己的数据目录 |
| admin shared roots | 由管理员在全局 settings 中配置 | 宿主机外部目录白名单 |
| sandbox 路径 | 用户当前 sandbox 的挂载路径 | 仅当 sandbox 存活时有效 |

**Workdir 授权规则（明确优先级）：**
1. workdir 位于 `$LOCAL_MEMORY/users/{userID}/` 下（含默认 `workspace-files/`）：**允许**，不需要 shared roots 授权——这是用户自己的数据目录
2. workdir 位于用户私有目录之外的宿主机路径：**必须命中 admin shared roots**，否则拒绝创建/更新 workspace

**关键变更**：
- 多用户模式下**不再默认允许访问整个 `$HOME`**。单用户模式不变
- admin shared roots 存储在 `$LOCAL_MEMORY/global/settings.json` 的 `shared_roots` 字段，格式为字符串数组
- 未配置 shared roots 时默认为空，用户只能访问自己的 uploads 和 LOCAL_MEMORY 子目录

### 多用户默认 Workspace 策略

当前单用户模式下 `resolveDefaultWorkdir()` 优先使用 sandbox home / `$HOME`。多用户模式下需要额外处理：

- **未配置 shared roots 时**：默认 workspace 的 workdir 改为 `$LOCAL_MEMORY/users/{userID}/workspace-files/`（位于用户自身数据目录内，不需要 shared roots 授权）
- **已配置 shared roots 时**：默认 workdir 可沿用 `resolveDefaultWorkdir()` 逻辑，但创建时需校验路径是否在 shared roots 范围内
- **旧数据迁移**：迁移后 admin 的 workspace 可能包含 `$HOME` 作为 workdir；若 admin 未配置 shared roots，这些 workspace 的 workdir 应标记为 "unverified"，在 UI 上提示 admin 配置 shared roots 或修改 workdir
- **public/shared job 的文件读取**：
  - 通过 owner 的数据目录定位
  - 访问范围**仅限** job 的 session 目录（即 `$LOCAL_MEMORY/[users/{userID}/]workspaces/{wsID}/jobs/{jobID}/sessions/`），与现有 `PublicServeFile` 的 `allowedDir` 一致
  - 不暴露 owner 的 workspace workdir 或其他目录
  - 若消息中引用了 uploads 中的图片/附件，需在 session 目录下维护引用副本或符号链接，public 访问时仅通过 session 目录读取
