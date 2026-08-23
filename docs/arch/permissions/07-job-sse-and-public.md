# 07 · Job SSE 与 `/api/v1/public/*` 边界

## 1. 私域 SSE —— `JobEvents`

位置：`cmd/web/handler/job_lifecycle.go` 中的 `JobEvents`。

挂在 `/api/v1/job/:jobId/events`，前置校验登录 Cookie 和 `job.read` 权限。

- 仅做"Job 是否存在"的存在性检查；不存在 → 404。
- 通过后调用 event buffer 把"快照 + tail"一并推给客户端。
- `Last-Event-ID` 头携带 seq，用来断线续传：
  - 缺失 / 解析失败：当作新订阅，从最新可用 seq 开始。
  - seq 比 buffer 已驱逐的最早条目还旧：返回 `410 Gone`，提示客户端重新拉取快照。

**没有 per-Job 的拥有者校验**：具有 `job.read` 权限的用户可以订阅实例中的任何 Job 事件流。这与共享实例模型一致。

## 2. 公域 SSE —— `Public Job Events`

位置：`cmd/web/api.go` 中的 `pub.GET("/job/:jobId/events", h.JobEvents)`。

挂在 `shareTokenMiddleware` 之下：

- 必须带 `?shareToken=...`，且与 Job 上的 `ShareToken` 一致才能进。
- 进入后流的内容与私域一致 —— Job 的 ShareToken 一旦签发就等于把"该 Job 的事件流和文件视图"都对外开放，撤回方式是清空或删除。
- ShareToken 是 32 字节 `crypto/rand`，对比走 `subtle.ConstantTimeCompare`。

## 3. `/api/v1/public/*` 一览

| Path | 方法 | 说明 |
|------|------|------|
| `/api/v1/public/job/:jobId` | GET | 读 Job 元信息（标题、workdir、状态等） |
| `/api/v1/public/job/:jobId/events` | GET | SSE 订阅事件 |
| `/api/v1/public/sessions/:sessionId/messages` | GET | 拉取分页消息 |
| `/api/v1/public/serve-file?path=...` | GET | 分享只读文件，限定在 Job session 目录内 |

全部走 `shareTokenMiddleware`，**全部只读**。

## 4. SSE 鉴权一次性

quartet 的私有 SSE 在握手时校验登录 Cookie 和 `job.read`；公开 SSE 校验 shareToken。连接持续期间不做逐事件重新校验。

## 5. 含义说明（运维视角）

- 具有对应权限的登录用户可访问实例中的全部同类资源，没有按用户归属隔离。
- 部署到不可信网络时必须使用 HTTPS，保护用户名、密码与 Cookie。
- 把 quartet 暴露到公网时，建议结合反向代理做：IP allowlist、TLS、限流，因为应用层没有这些能力。
