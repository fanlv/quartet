# 07 — 部署与运维

> **已废弃：** 本文中的 JWT、SQLite 和兼容模式不再实施。当前方案见 `docs/feature/feature-2026-08-23-user-auth-permission-management.md`。

## 1. 部署模式对比

| 维度 | 单用户（现状） | 多用户 |
|---|---|---|
| 监听地址 | `0.0.0.0:8090` | `127.0.0.1:8090`（反代后端绑定本地；由 Nginx/Caddy 监听 HTTPS 对外） |
| 认证 | 环境变量 `X_AGENT_AUTH` + 请求头 `X-AGENT-AUTH` | JWT（`QUARTET_JWT_SECRET`） |
| 存储 | 纯文件 | 文件 + SQLite |
| 进程数 | 1 | 1（同一进程服务所有用户） |
| 反向代理 | 不需要 | 建议 Nginx/Caddy 前置（HTTPS、限流） |

## 2. 新增环境变量

| 变量 | 默认值 | 说明 |
|---|---|---|
| `QUARTET_MULTI_USER` | 未设置（false） | 设为 `true` 启用多用户 |
| `QUARTET_JWT_SECRET` | 随机生成并持久化 | JWT 签名密钥 |
| `QUARTET_JWT_ACCESS_TTL` | `24h` | access token 有效期 |
| `QUARTET_JWT_REFRESH_TTL` | `7d` | refresh token 有效期 |
| `QUARTET_ALLOW_REGISTER` | `true` | 是否开放注册 |
| `QUARTET_MAX_USERS` | `0`（不限） | 最大用户数 |

## 3. HTTPS 与安全

多用户部署 **必须** 使用 HTTPS：
- 通过 HTTP 传输 Bearer/JWT 凭据会被明文暴露，必须用 HTTPS 保护
- HttpOnly cookie 中的 refresh token 同样需要 HTTPS（`Secure` flag）
- 推荐 Caddy（自动获取和续期证书）或 Nginx 作为反向代理
- 反向代理配置要点：开启 SSL/TLS、关闭 proxy buffering（SSE 支持）、设置足够长的 read timeout（SSE 长连接）

## 4. 容量估算

| 项目 | 单用户 | 10 用户 | 100 用户 |
|---|---|---|---|
| SQLite 大小 | N/A | < 1MB | < 10MB |
| 内存（进程） | ~50MB | ~80MB | ~200MB |
| 磁盘（数据） | 依赖使用 | ×10 | ×100 |
| 并发连接 | 1-3 | 10-30 | 50-200 |

瓶颈在磁盘 I/O（文件存储）和 AI 模型 API 调用，不在 Quartet 进程本身。

## 5. 备份与恢复

- **冷备**（推荐）：停止 Quartet 进程后，`$LOCAL_MEMORY` 整体 rsync/tar 即为完整一致备份
- **热备**：进程运行中备份需要分两步：
  1. SQLite 使用 `.backup` 命令或 Go `sqlite3` backup API 生成一致的数据库快照（不能直接 cp/tar 运行中的 `.db` + `.db-wal` + `.db-shm`，可能出现不一致）
  2. 文件目录（workspaces/uploads/...）使用 rsync
- 建议 cron 定期备份到外部存储

## 6. 监控与日志

- 项目使用 `pkg/logger`（基于 Go 标准库 `log/slog`）
- 多用户下日志 message 中包含 user_id（当前 logger 为纯文本格式，不支持结构化字段；user_id 通过字符串拼接写入 message，如 `"job created, user_id=xxx"`）
- 如需按 user_id 检索或接入 log pipeline，后续可扩展 logger 支持 slog attrs 结构化输出
- 可接入 Prometheus metrics（可选 Phase 2）：
  - `quartet_active_users` gauge
  - `quartet_requests_total` counter（按 user_id label）
  - `quartet_job_duration_seconds` histogram

## 7. 启动时初始化流程

1. 读取环境变量
2. 判断 `QUARTET_MULTI_USER`：
   - **多用户模式**：
     1. 初始化 SQLite（auto-migrate 表结构：users、refresh_tokens、job_shares、public_shares、im_bindings；Phase 2 启用 OAuth 时同时 auto-migrate `oauth_identities`）
     2. 检查是否存在 `.migration_pending` 或需要迁移的旧数据（参见 02-storage.md 第 5 节迁移状态机）
     3. 若存在旧数据且未迁移：写 `.migration_pending` 标记，启动时在日志中提示
     4. 初始化 JWT 密钥（从环境变量或持久化文件加载）
     5. 注册多用户中间件和路由（auth 路由、admin 路由）
     6. 若 0 users exist：在日志和首次访问页面提示 "First run: create admin via register"；首个注册用户自动成为 admin，注册成功后自动触发旧数据迁移
     7. **迁移期间系统处于受限模式**：仅允许 auth、health、migration status/retry API；业务 API 返回 503；前端展示迁移进度页（详见 02-storage.md "迁移中/迁移失败的系统行为"）
   - **单用户模式**：
     1. 走现有初始化逻辑，行为不变
3. 注册通用路由（workspace/job/session/...）
4. 启动调度器（**多用户模式下有条件启动**：若 `.migration_pending` 存在或当前 active 用户数为 0，则暂不启动调度器——此时旧 schedules 尚未迁移，也没有可注入的 UserContext。待首个用户注册成功后，无论是否存在旧数据迁移，注册流程统一调用 `EnsureSchedulerStarted` 启动或 reload 调度器。有旧数据时在迁移完成后触发；无旧数据时在注册成功后立即触发）
5. 监听端口

## 8. 实施优先级

| 阶段 | 内容 |
|---|---|
| **Phase 1** | User 模型 + 账密登录 + JWT + 数据隔离 + 前端登录页 + 配置分层（全局/用户）+ 模型共享 |
| **Phase 2** | OAuth 登录（GitHub/Lark）+ 组织内 Job 分享 |
| **Phase 3** | Workspace 协作 + 细粒度权限 |
