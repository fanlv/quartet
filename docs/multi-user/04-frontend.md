# 04 — 前端改造

> **已废弃：** 本文中的 JWT 与旧 Token 兼容方案不再实施。当前方案见 `docs/feature/feature-2026-08-23-user-auth-permission-management.md`。

## 1. 新增页面

### 1.1 登录页 (`LoginPage`)

- 路由条件：多用户模式 + 未登录
- 内容：username + password 表单，"Register" 链接
- 登录成功后把 JWT access token 存 localStorage（key: `quartet.access_token`）；refresh token 由服务端通过 `Set-Cookie` 自动设置为 HttpOnly cookie，前端不直接操作
- 替代现有 AuthGate 的 token 手动输入流程

### 1.2 注册页 (`RegisterPage`)

- username + password + confirm password + email(可选)
- 首次注册（无用户时）展示"你将成为管理员"提示
- 注册成功自动登录

### 1.3 用户管理页 (`AdminUsersPage`)

- 仅 admin 可见（Settings 里新增 tab 或独立页面）
- 用户列表、禁用/启用、删除、重置密码

## 2. AuthGate 改造

现有 AuthGate 行为：探测 `/api/v1/health` 判断是否需要认证 → 有 token 则尝试访问 → 成功渲染 app / 失败显示 token 输入表单。

多用户模式下：
1. 探测 `/api/v1/health` 获取 `{ authRequired, multiUser }` 信息
2. `multiUser=false`：走现有逻辑，不变
3. `multiUser=true`：
   - localStorage 有 `quartet.access_token` → 调用 `/api/v1/auth/me` 验证 → 有效则渲染 app
   - access token 过期 → 调用 `/api/v1/auth/refresh`（refresh token 通过 cookie 自动发送）→ 成功则更新 access token 并渲染 → 失败跳转 LoginPage
   - 无 access token → 跳转 LoginPage

## 3. Auth Token 管理

### 前端存储

| 模式 | Key | 值 | 存储位置 |
|---|---|---|---|
| 单用户 | `quartet.x_auth_token` | 原始 shared secret | localStorage |
| 多用户 | `quartet.access_token` | JWT access token | localStorage |
| 多用户 | (refresh token) | opaque token | HttpOnly cookie（服务端设置，前端不可访问） |

### fetch 拦截器改造

现有 `main.tsx` 的 fetch monkey-patch：
- 单用户模式：在 header 中添加 `X-AGENT-AUTH` token
- 多用户模式：在 header 中添加 `Authorization: Bearer <accessToken>`

增加 401 自动刷新逻辑：
- 收到 401 响应且不是 refresh 请求本身 → 调用 `/api/v1/auth/refresh` → 成功则用新 token 重试原请求 → 刷新失败则跳转登录页

### 浏览器原生资源请求的鉴权

`<img src>`、`<a href>`、Markdown 渲染的图片等浏览器原生请求不经过 fetch monkey-patch，无法注入 `Authorization` header。多用户模式下 `/api/v1/serve-file` 等资源接口需要鉴权，必须单独处理：

- **方案：短期签名 URL**。前端在需要加载受保护资源时，先通过 fetch 请求后端生成带签名的临时 URL（含 `token` query 参数和过期时间），再将该 URL 赋给 `<img src>` 等属性
- 后端对 `/api/v1/serve-file` 增加 `token` query 参数校验：若请求携带有效的短期 file access token 则放行，否则回退到 `Authorization` header 校验
- file access token 有效期短（如 5 分钟），仅授权访问指定文件路径，不可复用为通用认证凭据
- **禁止将长期 JWT access token 放入 URL query 参数**——URL 会出现在浏览器历史、Referer header、服务端日志中，泄露风险高

## 4. 用户状态管理

在 App 顶层维护当前用户状态，包含 id、username、avatarUrl、role 字段：
- 登录成功时设置
- 从 `/api/v1/auth/me` 响应初始化
- 传给 Sidebar（显示用户名、头像）和 Settings（判断 admin tab 可见性）

### localStorage 缓存隔离

多用户模式下，所有 localStorage key 必须按用户 ID 隔离，防止同一浏览器切换账号后复用前一用户的缓存：

| 现有 key | 多用户隔离后 key |
|---|---|
| `workspace_${wsId}` | `quartet:${userID}:workspace:${wsId}` |
| `last_used_workspace_id` | `quartet:${userID}:last_used_workspace_id` |
| `quartet:sent_history:${wsId}` | `quartet:${userID}:sent_history:${wsId}` |
| `workspacePrefs_${wsId}` | `quartet:${userID}:workspacePrefs:${wsId}` |
| `home_hide_scheduled` | `quartet:${userID}:home_hide_scheduled` |
| `home_filter_workspace_id` | `quartet:${userID}:home_filter_workspace_id` |
| `last_agent_type` | `quartet:${userID}:last_agent_type` |
| `quartet-language` | `quartet:${userID}:language`（登录后）；未登录时保留 `quartet-language` 作为匿名阶段语言偏好，登录后若用户级 key 不存在则从匿名 key 继承 |

- 登出（logout）时清除当前用户的所有 `quartet:${userID}:*` 缓存
- 切换用户时重新从新用户的 key 读取状态
- `quartet.access_token` 不按用户分（登出时直接清除）

## 5. Settings 面板变更

| Tab | 变更 |
|---|---|
| `general` | username/avatar 改为只读展示（从 `/api/v1/auth/me` 获取），编辑走 `PUT /api/v1/auth/me` 更新接口 |
| `token` | 多用户模式下隐藏此 tab（token 由系统管理，不再手动粘贴） |
| `model` | 区分"全局模型"（admin 配置）和"我的模型"（用户私有） |
| 新增 `account` | 修改密码、退出登录 |
| 新增 `admin` | 仅 admin 可见：用户管理、全局配置 |

## 6. Sidebar 变更

- 底部用户信息区：显示 username + avatar + "Logout" 按钮
- 多用户模式下点击用户名展开下拉：Account Settings、Logout
- 单用户模式下保持不变

## 7. URL 与路由

不引入 React Router，继续用 query params：

| 参数 | 说明 |
|---|---|
| `?view=login` | 登录页 |
| `?view=register` | 注册页 |
| `?view=admin` | 管理页（admin only） |

App 渲染逻辑新增（按优先级从高到低）：`view=register` → 渲染 RegisterPage；`view=admin` 且当前用户为 admin → 渲染 AdminUsersPage；多用户且未登录 → 渲染 LoginPage；其他走现有 view 逻辑。

## 8. i18n 新增 Key

新增 auth 和 admin 相关的国际化 key，包括：login、register、username、password、confirmPassword、logout、changePassword、userManagement、disableUser、enableUser 等。中英文双语。
