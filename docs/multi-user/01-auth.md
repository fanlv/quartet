# 01 — 用户模型与认证

> **已废弃：** 本文中的 JWT、SQLite 和兼容模式不再实施。当前方案见 `docs/feature/feature-2026-08-23-user-auth-permission-management.md`。

## 1. User 模型

User 包含以下字段：

| 字段 | 类型 | 说明 |
|---|---|---|
| ID | string | `"user-{uuid}"` 或自增 |
| Username | string | 唯一，登录用 |
| Email | string | 可选，唯一 |
| Password | string | bcrypt hash |
| AvatarURL | string | 头像 |
| Role | string | `"admin"` 或 `"user"` |
| Status | string | `"active"`、`"disabled"` 或 `"deleted"`（软删除） |
| TokenVersion | int | 每次密码修改或 admin 禁用时自增，用于作废已签发的 token |
| CreatedAt | time.Time | 创建时间 |
| UpdatedAt | time.Time | 更新时间 |

- 首个注册用户自动成为 admin
  - **并发保护**：首个 admin 创建必须在 SQLite `BEGIN IMMEDIATE` 事务内完成——事务内检查当前用户数为 0 → 插入用户 → 设置 role 为 admin
  - 必须使用 `BEGIN IMMEDIATE`（而非默认的 deferred transaction），确保检查用户数和插入操作在同一个写锁下执行，防止两个并发请求都读到 count=0 的竞态
  - 并发失败方收到 `SQLITE_BUSY` 后应重试，重试时发现用户数 > 0，不再享受首个用户例外
  - 重试时必须重新执行完整注册流程——检查 `allow_registration` 和 `max_users`，仅在注册开关允许且未达到用户上限时创建为普通 user，否则返回 403
- admin 可以禁用/删除用户、管理全局模型配置
- 普通 user 只能管理自己的数据

**唯一约束与规范化：**
- `username`：入库前统一 trim + lowercase；SQLite 列使用 `COLLATE NOCASE`，确保 `Admin` 和 `admin` 视为同一用户名
- `email`：入库前统一 trim + lowercase；SQLite 列使用 `COLLATE NOCASE`；登录和 OAuth 绑定时也按 lowercase 匹配
- 注册 / 登录时的处理顺序：`trim → lowercase → 唯一性检查 → 写入`
- `username` 和 `email` 均有 UNIQUE 约束
- 软删除用户时，将 `username` 重命名为 `__deleted_{userID}_{原username}`，`email` 置为 `NULL`，释放原值供新用户使用
- 这样既保留了审计记录，又不阻塞新用户注册相同 username/email

## 2. 注册与登录

### 2.1 账密登录（Phase 1）

**注册流程：**
1. `POST /api/v1/auth/register` — 提交 username、password、email（可选）
2. **检查注册开关**：若 `allow_registration == false`，拒绝注册（403）。例外：当系统中 0 个用户时，首个注册不受此限制（用于初始化 admin）
3. **检查用户数上限**：若 `max_users > 0` 且当前 active 用户数已达上限，拒绝注册（403）
4. 后端校验 username 唯一性，password 强度（≥8 位，含字母+数字）
5. **Email 规范**：若提交了 email，trim + lowercase 后存储；若未提交或 trim + lowercase 后为空字符串，存为 `NULL`（SQLite UNIQUE 允许多个 NULL，不允许多个空字符串）
6. bcrypt hash 存储密码
7. 返回 JWT access token（response body）；refresh token 通过 `Set-Cookie` HttpOnly 下发

**登录流程：**
1. `POST /api/v1/auth/login` — 提交 username、password
2. bcrypt.CompareHashAndPassword 校验
3. **校验 `Status == "active"`**：若用户已被禁用或删除，返回 403 + "账号已被禁用"（即使密码正确也拒绝登录）
4. 成功：返回 JWT access token（body），refresh token 通过 HttpOnly cookie 下发；access token 有效期 24h，refresh token 有效期 7d
5. 失败：返回 401 + 统一错误信息"用户名或密码错误"（不区分"用户不存在"和"密码错误"，防止账号枚举；详细失败原因写服务端日志）

**Token 刷新：**
1. `POST /api/v1/auth/refresh` — 无需 body，refresh token 自动通过 cookie 发送
2. 服务端解析 refresh token 中的 `selector` 部分，定位 `refresh_tokens` 表中的记录；再用 `verifier` 部分的 SHA256 hash 与记录中存储的 `verifier_hash` 比对（token 格式见下方"Refresh Token 格式"）
3. 校验记录中的 `issued_token_version` 与用户当前 `TokenVersion` 是否一致，若不匹配则拒绝（401）
4. **校验 `Status == "active"`**：若用户已被禁用或删除，拒绝刷新（401）
5. 验证通过后：
   - 签发新的 access token
   - **签发新的 refresh token**（轮换），通过 `Set-Cookie` 下发
   - **旧 refresh token 立即失效**（从 `refresh_tokens` 表中删除旧记录，插入新记录）
6. **Refresh token reuse 检测**：若 `selector` 在 `refresh_tokens` 表中存在但 `verifier_hash` 不匹配，视为 token 泄露——通过记录中的 `user_id` 清除该用户所有 refresh token 记录，强制全端重新登录。若 `selector` 不存在，直接返回 401

### 2.2 OAuth（Phase 2，可选）

- 支持 GitHub OAuth / Lark OAuth / 飞书扫码
- 回调 `/api/v1/auth/callback/:provider`
- 首次 OAuth 登录自动创建用户，**同样受 `allow_registration` 和 `max_users` 控制**：注册关闭或用户数已达上限时，拒绝自动创建新用户（返回 403 并提示联系管理员）
- 绑定已有账号不消耗新用户名额

**OAuth 身份映射存储（Phase 2 实现时新增）：**

在 SQLite 中新增 `oauth_identities` 表，存储用户与第三方 OAuth 身份的映射关系：

| 列 | 类型 | 约束 | 说明 |
|---|---|---|---|
| id | TEXT | PRIMARY KEY | 记录 ID |
| user_id | TEXT | NOT NULL, FK → users(id) | 本地用户 ID |
| provider | TEXT | NOT NULL | OAuth 提供方（`github`、`lark`、`feishu`） |
| provider_user_id | TEXT | NOT NULL | 第三方用户唯一标识 |
| email | TEXT | | 第三方账号邮箱（可选） |
| created_at | DATETIME | DEFAULT CURRENT_TIMESTAMP | |
| updated_at | DATETIME | DEFAULT CURRENT_TIMESTAMP | |

唯一约束：`UNIQUE(provider, provider_user_id)`，确保同一个第三方身份不会重复绑定。一个本地用户可绑定多个 provider，一个 provider_user_id 只能绑定一个本地用户。

**绑定已有账号策略：** 用户必须先登录本地账号，再通过 OAuth 回调完成绑定；不支持邮箱自动匹配绑定（避免邮箱冲突导致的安全问题）。

绑定流程使用 `state` 参数关联本地用户——因为 OAuth callback 是从第三方域名跨站跳转回来，`SameSite=Strict` cookie 不会被浏览器发送，不能依赖 cookie 识别当前用户：

1. 用户在已登录状态下点击"绑定 GitHub / Lark"
2. 前端调用 `POST /api/v1/auth/oauth/bind-init`（携带 JWT），后端生成随机 `state`，在服务端缓存 `state → userID`（TTL 10 分钟），返回 OAuth authorize URL（含 `state`）
3. 前端跳转到第三方 OAuth 授权页
4. 用户授权后，第三方 302 回调到 `/api/v1/auth/callback/{provider}?code=xxx&state=xxx`
5. 后端通过 `state` 查找 pending binding 中的 `userID`，校验 `state` 有效性和 TTL
6. 用 `code` 换取 OAuth access token，获取第三方用户信息
7. 将 OAuth 身份写入 `oauth_identities` 表，绑定到 `state` 对应的本地用户
8. `state` 使用后立即删除（一次性，防重放）

## 3. Token 设计

### Access Token（JWT）

- 算法：HS256
- Payload 包含：`sub`（user ID）、`name`（username）、`role`、`token_version`、`exp`、`iat`
- 签名密钥：启动时从环境变量 `QUARTET_JWT_SECRET` 读取；未设置则随机生成并持久化到 `$LOCAL_MEMORY/global/.jwt_secret`（仅多用户模式使用 JWT；单用户模式继续使用 `X_AGENT_AUTH`，不需要 JWT secret）
- 传输方式：`Authorization: Bearer <token>` 请求头

### Refresh Token（Selector + Verifier）

- **格式**：`{selector}.{verifier}`，其中 `selector` 为 16 字节随机 hex（32 字符），`verifier` 为 32 字节随机 hex（64 字符），两部分用 `.` 拼接
- **存储**：`selector` 明文存入 `refresh_tokens` 表作为定位键；`verifier` 仅存 SHA256 hash（`verifier_hash`），不存明文
- **查找**：刷新时解析 token 的 `selector` 部分定位记录，再用 `verifier` 的 SHA256 与记录中 `verifier_hash` 比对
- **Reuse 检测**：`selector` 命中但 `verifier_hash` 不匹配 → token 泄露，通过记录中 `user_id` 清除该用户所有 refresh token
- 客户端存储：**仅通过 HttpOnly + Secure + SameSite=Strict cookie** 传输和存储
- **禁止放入 localStorage**——refresh token 有效期长达 7 天，localStorage 对 XSS 无防护，公网多用户部署下风险过高

## 4. 与现有 X_AGENT_AUTH 的关系

| 模式 | 行为 |
|---|---|
| 单用户（`QUARTET_MULTI_USER` 未设置） | 沿用环境变量 `X_AGENT_AUTH` + 请求头 `X-AGENT-AUTH`，行为完全不变 |
| 多用户（`QUARTET_MULTI_USER=true`） | 受保护的 `/api/v1/*` 路由不再接受 `X-AGENT-AUTH` 作为认证凭据，改为 `Authorization: Bearer <JWT>`；public share 路由仍按 shareToken 走独立校验 |

认证中间件行为：
- 多用户模式：从 Authorization header 提取 JWT → 验证签名和过期 → 检查 claims 中的 `token_version` 与数据库中用户的 `TokenVersion` 是否一致 → **校验 `Status == "active"`** → 注入 UserContext
- 单用户模式：走现有 `X-AGENT-AUTH` 校验逻辑

## 5. 会话管理

- access token 是无状态 JWT，服务端不存储
- refresh token 的 hash 存储在 SQLite `refresh_tokens` 表中
- **Logout 语义**：logout 仅清除 refresh token cookie 和数据库记录，不维护 access token 黑名单。已签发的 access token 在 TTL 到期前仍然有效（最长 24h）。这是有状态/无状态 token 的固有权衡，可接受。如需立即踢出用户（如 admin 禁用），通过自增 `TokenVersion` 实现（见下方）
- 用户修改密码时：自增 `TokenVersion`，同时清除该用户所有 refresh token 记录
- **admin 禁用用户时**：
  1. 将用户 `Status` 设为 `"disabled"` 并持久化到 SQLite
  2. 自增该用户的 `TokenVersion`（使所有已签发的 access token 和 refresh token 失效）
  3. 清除该用户所有 refresh token 记录
  4. 认证中间件在验证 JWT 时，检查 `token_version` claim 与数据库中的 `TokenVersion` 一致性；不一致则拒绝（401）
  5. 不依赖内存黑名单，进程重启后行为一致

## 6. 用户删除策略

admin 通过 `DELETE /api/v1/admin/users/:userID` 删除用户时：

1. **软删除**：将 `Status` 设为 `"deleted"`，保留数据库记录，自增 `TokenVersion`
2. **清除 token**：删除该用户所有 refresh token 记录
3. **停止运行中的任务**：取消该用户所有运行中的 job 和定时任务
4. **数据保留策略**：
   - `$LOCAL_MEMORY/users/{userID}/` 目录默认保留 30 天，不立即删除
   - 30 天后由 admin 手动确认删除，或通过定时清理任务自动清理
   - 公开分享链接（public share）在用户删除后立即失效
   - 组织内分享（shared_with）在用户删除后对被分享者不再可见
5. **IM / WeChat 绑定**：解除该用户的 IM app_id 和 WeChat bot 映射关系

## 7. API 变更汇总

| 路由 | 方法 | 说明 |
|---|---|---|
| `/api/v1/auth/register` | POST | 注册 |
| `/api/v1/auth/login` | POST | 登录 |
| `/api/v1/auth/logout` | POST | 登出（清 refresh token cookie + 数据库记录；支持 JWT 或 refresh token cookie 认证，确保 access token 过期时仍可 logout） |
| `/api/v1/auth/refresh` | POST | 刷新 access token（refresh token 通过 cookie 传递） |
| `/api/v1/auth/me` | GET | 获取当前用户信息 |
| `/api/v1/auth/me` | PUT | 更新个人资料（username、avatar） |
| `/api/v1/auth/change-password` | POST | 修改密码（自增 TokenVersion） |
| `/api/v1/admin/users` | GET | 管理员：用户列表 |
| `/api/v1/admin/users/:userID` | PUT | 管理员：编辑用户（角色、状态） |
| `/api/v1/admin/users/:userID` | DELETE | 管理员：删除用户（软删除） |
| `/api/v1/admin/users/:userID/reset-password` | POST | 管理员：重置用户密码（生成随机密码返回，自增 TokenVersion，清除该用户所有 refresh token） |
