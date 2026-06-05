# 07 · Job SSE 与 `/api/v1/public/*` 边界

## 1. 私域 SSE —— `JobEvents`

位置：`cmd/web/handler/job_lifecycle.go` 中的 `JobEvents`。

挂在 `/api/v1/job/:jobId/events`，前置走 `agentAuthMiddleware`。

- 仅做"Job 是否存在"的存在性检查；不存在 → 404。
- 通过后调用 event buffer 把"快照 + tail"一并推给客户端。
- `Last-Event-ID` 头携带 seq，用来断线续传：
  - 缺失 / 解析失败：当作新订阅，从最新可用 seq 开始。
  - seq 比 buffer 已驱逐的最早条目还旧：返回 `410 Gone`，提示客户端重新拉取快照。

**没有 per-Job 的拥有者校验**：通过 token 鉴权的请求方可以订阅本机任何 Job 的事件流。这与"单机单用户"的设计前提一致。

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

quartet 的 SSE 在握手时校验 token / shareToken，连接持续期间不会再做 per-event 校验。token 轮换的影响：已建立的连接保持，新连接需要新 token；这是 Hertz 长连接 + 静态密钥的固有行为。

## 5. 含义说明（运维视角）

- 通过 token 拿到 `/api/v1/*` 的请求方就能订阅所有 Job、读所有受白名单保护的文件，没有进一步隔离。
- 部署到非单机环境前，请把 `X_AGENT_AUTH` 设成只在受信圈层流通的强密钥。
- 把 quartet 暴露到公网时，建议结合反向代理做：IP allowlist、TLS、限流，因为应用层没有这些能力。
