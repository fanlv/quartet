# Quartet 权限与访问控制总览

最后更新：2026-05-14

Quartet 是共享实例：具有用户身份、Cookie 会话和 RBAC，但不按用户隔离业务数据。权限主要解决四件事：

1. **不要把整台机器的文件系统暴露给前端 / 三方调用方**——靠文件白名单。
2. **不要把宿主机敏感环境变量泄露给被执行的脚本**——靠 Shell env 过滤。
3. **识别登录用户并限制功能能力**——靠 Cookie 会话与 RBAC。
4. **保留匿名只读分享**——靠 Share Token。

本目录按模块组织，逐项拆开：

| 文件 | 范围 |
|------|------|
| [01-filesystem-whitelist.md](./01-filesystem-whitelist.md) | 文件系统白名单（`allowedRoots` / `isPathInAllowedRegion` / `hasPathPrefix` / TOCTOU / Symlink 处理） |
| [02-workspace-workdir.md](./02-workspace-workdir.md) | Workspace / Job 的 workdir 校验、`$HOME` 防御纵深、容器化检查 |
| [03-http-api-auth.md](./03-http-api-auth.md) | 用户 Cookie 会话、RBAC、Share Token、CORS、Listen Addr |
| [04-im-gateway.md](./04-im-gateway.md) | 飞书 / 企微 IM 网关的管理员白名单与 pending contact 限流 |
| [05-shell-env-sanitization.md](./05-shell-env-sanitization.md) | Shell 执行时的环境变量过滤、passthrough 白名单、`QUARTET_CONTROL` 注入 |
| [06-file-upload-serve.md](./06-file-upload-serve.md) | 文件上传、`ServeFile` 的 MIME 白名单、`Content-Disposition` 强制下载策略 |
| [07-job-sse-and-public.md](./07-job-sse-and-public.md) | Job SSE 订阅、`/api/v1/public/*` 只读分享路径的边界 |
| [08-env-vars.md](./08-env-vars.md) | 与权限相关的所有环境变量速查表 |
| [09-error-messages-and-tests.md](./09-error-messages-and-tests.md) | 拒绝/错误信息一览，以及覆盖这些边界的测试 |

## 一句话结论

- **首次使用**：从后端日志取得初始化码，在 Web 创建首个管理员。
- **登录访问**：所有私有 API 都要求登录 Cookie，并校验路由对应权限。
- **共享实例**：RBAC 控制功能，不按账号隔离 workspace、Job 和文件等业务数据。

## 关键设计假设（明确写出来，避免误用）

1. 宿主机文件系统只有受信本地用户可写。Symlink TOCTOU 检查是 best-effort，绝对防护需要容器化。
2. 同一个 quartet 进程可有多个账号，但所有账号共享实例数据；不在 quartet 内做租户级数据隔离。
3. Share Token 是**只读**临时凭证，撤回方式是删除/重置 Job 上的 token，没有签发记录、没有过期时间。
4. Shell 节点拿到的环境变量是"宿主机环境变量减去敏感名"，不是"白名单"。需要传敏感变量必须显式 `QUARTET_SHELL_ENV_PASSTHROUGH`。
