# Quartet 权限与访问控制总览

最后更新：2026-05-14

Quartet 的设计前提是 **单机 / 单用户 / 受信用户**，没有多租户、RBAC、用户身份模型。所谓"权限"主要解决三件事：

1. **不要把整台机器的文件系统暴露给前端 / 三方调用方**——靠文件白名单。
2. **不要把宿主机敏感环境变量泄露给被执行的脚本**——靠 Shell env 过滤。
3. **当 quartet 暴露在网络上时，要能挡住未授权访问**——靠 HTTP API Token、Share Token、CORS。

本目录按模块组织，逐项拆开：

| 文件 | 范围 |
|------|------|
| [01-filesystem-whitelist.md](./01-filesystem-whitelist.md) | 文件系统白名单（`allowedRoots` / `isPathInAllowedRegion` / `hasPathPrefix` / TOCTOU / Symlink 处理） |
| [02-workspace-workdir.md](./02-workspace-workdir.md) | Workspace / Job 的 workdir 校验、`$HOME` 防御纵深、容器化检查 |
| [03-http-api-auth.md](./03-http-api-auth.md) | HTTP API Bearer Token、Share Token、CORS、Listen Addr |
| [04-im-gateway.md](./04-im-gateway.md) | 飞书 / 企微 IM 网关的管理员白名单与 pending contact 限流 |
| [05-shell-env-sanitization.md](./05-shell-env-sanitization.md) | Shell 执行时的环境变量过滤、passthrough 白名单、`QUARTET_CONTROL` 注入 |
| [06-file-upload-serve.md](./06-file-upload-serve.md) | 文件上传、`ServeFile` 的 MIME 白名单、`Content-Disposition` 强制下载策略 |
| [07-job-sse-and-public.md](./07-job-sse-and-public.md) | Job SSE 订阅、`/api/v1/public/*` 只读分享路径的边界 |
| [08-env-vars.md](./08-env-vars.md) | 与权限相关的所有环境变量速查表 |
| [09-error-messages-and-tests.md](./09-error-messages-and-tests.md) | 拒绝/错误信息一览，以及覆盖这些边界的测试 |

## 一句话结论

- **本机使用**：默认就够用，`X_AGENT_AUTH` 不设，CORS 不设，文件 RW 包含 `LOCAL_MEMORY` + uploads + 已建 workspace 的 workdir + `$HOME`。
- **暴露到内网/公网**：必须设 `X_AGENT_AUTH`、设 `QUARTET_CORS_ORIGINS` 为具体 origin、`QUARTET_LISTEN_ADDR` 谨慎绑定，最好再套一层 chroot / container 把 TOCTOU 风险压住。
- **没有 RBAC**：通过 Token 的请求方拥有所有 workspace、所有 Job 的全部读写能力。

## 关键设计假设（明确写出来，避免误用）

1. 宿主机文件系统只有受信本地用户可写。Symlink TOCTOU 检查是 best-effort，绝对防护需要容器化。
2. 同一个 quartet 进程的所有 API 调用方都是同一个用户；不在 quartet 内做"用户隔离"。
3. Share Token 是**只读**临时凭证，撤回方式是删除/重置 Job 上的 token，没有签发记录、没有过期时间。
4. Shell 节点拿到的环境变量是"宿主机环境变量减去敏感名"，不是"白名单"。需要传敏感变量必须显式 `QUARTET_SHELL_ENV_PASSTHROUGH`。
