# 09 · 错误信息一览 & 测试覆盖

## 1. 拒绝 / 错误信息一览

| HTTP Status | Body | 触发条件 |
|-------------|------|----------|
| 403 | `{"code":-1,"msg":"access denied: path is outside allowed directories"}` | 文件白名单（读、写、列、建目录、上传、最近目录、搜索全部用同一句） |
| 403 | `{"error":"permission denied"}` | `agentAuthMiddleware` 鉴权失败 |
| 403 | `{"error":"shareToken is required"}` | `/api/v1/public/*` 缺 shareToken |
| 403 | `{"error":"invalid share token"}` | shareToken 不匹配 |
| 403 | `{"error":"access denied: path is outside job directory"}` | `PublicServeFile` 路径越出 Job session 目录 |
| 400 | `{"error":"jobId is required"}` | shareTokenMiddleware 缺 jobId |
| 400 | `{"error":"path must be absolute"}` / `"parent must be absolute path"` / `"dir must be absolute path"` | 文件浏览相关入口收到相对路径 |
| 400 | `{"error":"invalid directory name"}` | `MkDir` 收到含 `/`、`\`、`.`、`..` 的目录名 |
| 404 | `{"error":"job not found"}` | shareTokenMiddleware 找不到 Job |
| 410 | `{"error":"event buffer no longer contains seq=..."}` | SSE 续传 seq 已被驱逐 |

业务错误用 `httputil.ErrResponse{Code:-1,Msg:...}` 的 envelope；Auth / Share / Public 中间件用 `{"error":"..."}` 的扁平 map。两种 shape 都被 `extractErrMsg` 接住，会出现在 access log 里方便排障。

### 1.1 静默处理的特例

| 入口 | 行为 | 原因 |
|------|------|------|
| `FileExists` 命中白名单外 | 返回 `200 + {"exists":false}` | 避免 403 信号被用作"文件是否存在的探测器" |
| 群聊 IM 消息且发送者非管理员 | 不回响应、写 pending contact | 避免对群成员暴露 bot 内部状态 |
| IM 平台无管理员配置 | 直接 drop（debug 日志） | 半成品状态下不做反应是更合理的默认 |

## 2. 日志策略

- 鉴权失败：empty token → Info；non-empty mismatch → Warn。token 只打前 4 位 + `***`。
- 文件白名单失败：HTTP 中间件按状态码统一打日志（`5xx` Warn / `3xx`-`4xx` Info / `2xx` Debug）。
- Shell env 过滤：debug 级别打"被滤掉的 key 名 + passthrough 提示"，**绝不**打 value。
- HTTP body 默认不打，由 `QUARTET_LOG_HTTP_BODY` 控制。

## 3. 测试覆盖

| 测试文件 | 覆盖内容 |
|----------|---------|
| `cmd/web/handler/file_rw_test.go` | `hasPathPrefix` 的 symlink 解析、不存在叶子的祖先穿透；`isPathInAllowedRegion` 的多根白名单、空路径拒绝；`ServeFile` 的 MIME 白名单（SVG/HTML 拦截、图片/PDF 放行）；`sanitizeAttachmentFilename` 的响应头注入防御；`buildAttachmentDisposition` 的 RFC 5987 编码；workspace provider 缓存按 revision 失效。 |
| `services/workspace/service_test.go` | `FileAccessBaseRoots` 的 `LOCAL_MEMORY` + `$HOME` 组合；`TrustedFileWorkspaceRoots` 过滤掉非绝对、空路径、删除中的 workspace。 |
| `services/job/executor_shell_test.go` | `TestShellEnvFiltering` 覆盖默认敏感 key 拦截、passthrough 显式放行、`QUARTET_CONTROL` 永不透传；以及 Shell 节点自身的 workdir / 临时目录隔离与清理。 |
| `cmd/web/handler/job_test.go`（若存在） | `validateWorkdir` / `ensureWorkdirWithinWorkspace` 的边界（绝对/相对、不存在、不是目录、symlink 越界）。 |

## 4. 排障小抄

1. 前端报 `access denied: path is outside allowed directories`：
   - 看 `LOCAL_MEMORY` 是否设了。
   - 路径不在 `LOCAL_MEMORY` / 任意 workspace workdir / `$HOME` 之下都会被拦——`$HOME` 默认始终在白名单里，所以一般只有跑到 `/etc`、`/proc`、其它磁盘挂载点这类系统目录才会触发。
   - 真要让 quartet 接触家目录之外的路径，先建一个落在那里的 workspace。
2. 前端报 `permission denied`：
   - 大概率 `X_AGENT_AUTH` 设过、客户端没带头或带错了；前端会从 `localStorage.quartet.x_auth_token` 取 token。
   - 看 web 后端日志的 `[auth] reject` 行；空 token 的话 Info 级别，需要把日志级别开到 Info 才能看到。
3. Shell 节点拿不到 `OPENAI_API_KEY`：
   - 默认就是被剥离的；显式 `export QUARTET_SHELL_ENV_PASSTHROUGH=OPENAI_API_KEY` 后重启 quartet。
4. 跨域请求被拒：
   - 看 `QUARTET_CORS_ORIGINS` 配的是不是包含当前前端 origin。
