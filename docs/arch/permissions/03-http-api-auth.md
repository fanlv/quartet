# 03 · HTTP API 鉴权

> 范围：`/api/v1/*` 的 Token 认证、`/api/v1/public/*` 的 Share Token、CORS、Listen Addr。

## 1. Bearer Token 中间件 —— `agentAuthMiddleware`

位置：`cmd/web/middleware.go` 中的 `agentAuthMiddleware()`。

- Token 来源（按优先级）：
  1. 请求头 `X-AGENT-AUTH`（推荐）。
  2. URL Query `?token=...`（给 `<img src>` 这类无法加自定义头的浏览器原生请求兜底）。
- 对比逻辑在 `cmd/web/handler/auth.go` 的 `CheckAgentAuth`：
  - 配置来源：`X_AGENT_AUTH` 环境变量，逗号分隔可配多个。
  - 未配置 → "开放访问"，任何 token 都返回通过。这是单机默认体验，这里不用考虑安全问题，因为是运行在用户个人电脑上。
  - 已配置 → 用 `subtle.ConstantTimeCompare` 做常量时间对比，逐个匹配。
- `IsAuthRequired()` 提供给 `/api/v1/health` 等公开接口探测：返回是否至少有一个非空 token 配置。

### 1.1 拒绝时的日志策略

注释里写得很清楚：很多被鉴权的路径在访问日志里默认会被跳过，所以中间件这条 reject 行往往是排障唯一线索。日志级别区分：

- 空 token → `Info`（首次启动 UI、token 轮转等正常情况）。
- 非空 token 但不匹配 → `Warn`（误配 / 攻击探测）。

`tokenPrefix(token)` 只输出前 4 位 + `***`，绝不会把完整 token 写日志。

### 1.2 拒绝响应

```json
{ "error": "permission denied" }
```

HTTP `403 Forbidden`，并 `c.Abort()`。

## 2. Share Token 中间件 —— `shareTokenMiddleware`

位置：`cmd/web/middleware.go` 中的 `shareTokenMiddleware(jobSvc)`。

挂在 `/api/v1/public/*` 路由组下，给 Job 的"分享只读视图"用。

流程：

1. 取 query `?shareToken=...`，缺失 → `403 {"error":"shareToken is required"}`。
2. 取 jobId（`:jobId` 路径参数 / 否则 `?jobId=`），缺失 → `400 {"error":"jobId is required"}`。
3. `jobSvc.Get(jobID)`，找不到 → `404 {"error":"job not found"}`。
4. 把 `j.ShareToken` 与请求 token 用 `subtle.ConstantTimeCompare` 比对，不一致 → `403 {"error":"invalid share token"}`。
5. 通过后把 Job 写入 `c.Set("publicJob", j)` 给下游 handler 使用。

### 2.1 Share Token 生命周期

位置：`cmd/web/handler/job.go` 中的 `JobShare` / `JobShareClear`。

- `EnsureShareToken`（在 job service 层）：拿到锁后读现值，空则用 32 字节 `crypto/rand` 生成并持久化；并发请求不会签出多个不同 token。
- `ClearShareToken`：原子清空，避免并发覆盖。
- 没有过期机制；撤回 = 清空或删除 Job。

## 3. `/api/v1/public/*` 路由

位置：`cmd/web/api.go`。

公共路径只有少数几条且全部"读"：
- `GET /api/v1/public/job/:jobId` —— 读 Job 元信息。
- `GET /api/v1/public/job/:jobId/events` —— SSE 订阅 Job 事件。
- `GET /api/v1/public/sessions/:sessionId/messages` —— 读会话消息。
- `GET /api/v1/public/serve-file?path=...` —— 受限的文件服务，详见下一节。

### 3.1 `PublicServeFile` 的额外约束

位置：`cmd/web/handler/job_public.go` 中的 `PublicServeFile`。

- 取出 `c.Get("publicJob")` 拿到经过 share token 校验的 Job。
- 把 `typepath.LocalSessionsDirInWorkspaceJob(job.WorkspaceID, job.ID)` 当作"允许的根"。
- 双方做 `FileEvalSymlinks`，校验请求路径必须落在该 Job 的 sessions 目录之下。
- 拒绝时返回 `403 {"error":"access denied: path is outside job directory"}`。

含义：分享出去的链接**不能**通过 `path=...` 跳读到 Job 范围之外的文件。

## 4. `/api/v1/health` 的特殊性

它**不**走 `agentAuthMiddleware`，是给前端做"是否需要登录"探测用的。返回里包含 `authRequired`，前端据此决定要不要在请求里挂 token。

## 5. CORS

位置：`cmd/web/main.go` 中的 `corsOrigins()` / `cors.Config`。

- 配置来源：`QUARTET_CORS_ORIGINS`（逗号分隔多个 origin）。
- 未配置：默认 same-origin。Boot 时打 Info 日志，提醒"以前的版本默认 `*`，现在已改为 same-origin，要跨域必须显式配置"。
- `AllowHeaders` 包含 `X-AGENT-AUTH`，确保鉴权头能在跨域预检里通过。

## 6. Listen Addr

位置：`cmd/web/main.go`，配置项 `QUARTET_LISTEN_ADDR`（如 `0.0.0.0:8090` 暴露到 LAN）。

注意：暴露到 LAN/WAN **必须**同时设置 `X_AGENT_AUTH`，否则任何能连到端口的人都能调所有 API。Boot 时如果 `X_AGENT_AUTH` 未设会打 Info 日志提示。

## 7. 没有 Session / Cookie / Refresh Token

quartet 的 HTTP 鉴权是无状态的、单个静态共享密钥。对 IM 网关、SSE 长连接同样如此（连接建立时校验一次，长连本身不再做 per-message 鉴权）。
