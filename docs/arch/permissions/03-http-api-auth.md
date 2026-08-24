# 03 · HTTP API 鉴权

> 范围：用户登录会话、RBAC、公开分享、CORS 与监听地址。

## 1. 用户会话

- 私有 `/api/v1/*` 由 `sessionAuthMiddleware` 校验 `quartet_session` Cookie。
- Cookie 是 host-only、HttpOnly、SameSite=Strict；HTTPS 请求增加 Secure。
- Cookie 只保存随机会话值，服务端仅保存摘要、用户 ID 和 CSRF 值。
- 登录会话没有时间过期限制，服务重启后继续有效；会话不存在或已被撤销返回 401，用户已登录但缺少路由权限返回 403。
- Web 和 iOS 收到 401 后返回登录页面。
- 用户停用、删除或重置密码时撤销其全部会话。
- `/api/v1/auth/me` 是轻量登录态探测接口，不触发 Agent 探测。

## 2. 初始化与登录

- `/api/v1/health` 匿名可访问，通过 `authState` 返回 `uninitialized`、`ready` 或 `recovery`。
- `uninitialized` 状态下，`POST /api/v1/auth/init` 创建首个管理员。
- `POST /api/v1/auth/login` 使用用户名和密码登录。同一账号或同一客户端 IP 连续失败会触发短时锁定，防止单 IP 扫描多个用户名以及多 IP 集中破解一个账号。
- 认证配置损坏或缺少有效管理员时进入 `recovery`，不重新开放匿名初始化。

## 3. 权限

私有业务路由在注册时显式声明稳定权限 ID。用户可绑定多个角色，有效权限为角色权限并集。未声明鉴权策略的私有路由不应放行。完整权限 ID 和路由矩阵见 [用户认证与权限方案](../../feature/feature-2026-08-23-user-auth-permission-management.md)。

这是共享实例权限模型：权限控制功能类别，不按资源创建人隔离数据。

## 4. CSRF

Cookie 会自动随请求发送，因此所有修改状态的私有请求必须携带当前会话对应的 `X-CSRF-Token`。该值由登录、初始化和 `/api/v1/auth/me` 返回。GET、HEAD、OPTIONS 和 SSE 握手不要求该请求头。

## 5. 公开分享

`/api/v1/public/*` 不创建用户登录态，继续通过 `shareToken` 或 `fileShareToken` 校验，只允许既有只读分享能力。分享凭证不能访问私有接口。

## 6. CORS 与监听地址

- 默认同源访问。跨域来源由 `QUARTET_CORS_ORIGINS` 显式配置。
- Cookie 登录的跨域部署还必须允许 credentials，且来源不能使用通配符。
- `QUARTET_LISTEN_ADDR` 可以覆盖监听地址。暴露到不可信网络时应使用 HTTPS，并结合反向代理限流。
- 客户端 IP 默认只信任来自本机反向代理的转发头；代理部署在其他地址时，通过 `QUARTET_TRUSTED_PROXIES` 显式配置代理 IP 或网段。
