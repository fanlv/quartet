# 02 — 存储层改造

> **已废弃：** 本文中的按用户数据隔离与 SQLite 方案不再实施。当前方案见 `docs/feature/feature-2026-08-23-user-auth-permission-management.md`。

## 1. 现有目录结构（单用户）

```text
$LOCAL_MEMORY/
├── agent/           ← 全局：settings, models, prompts, templates, scripts, schedules, usage_stats
├── workspaces/      ← 全局：所有 workspace/job/session 数据
├── user_input/      ← 全局：用户输入日志
├── im/              ← 全局：IM 消息映射与数据
├── uploads/         ← 全局：上传文件
└── wechat/          ← 全局：微信账号
```

## 2. 多用户目录结构

核心思路：在 `$LOCAL_MEMORY` 下按 user ID 隔离用户数据，全局共享数据提升到 `global/`。

```text
$LOCAL_MEMORY/
├── global/                          ← 全局共享（admin 管理）
│   ├── settings.json                ← 全局产品设置
│   ├── models.json                  ← 共享模型配置（所有用户可用）
│   ├── .jwt_secret                  ← JWT 签名密钥（仅多用户模式且未设置 QUARTET_JWT_SECRET 时生成）
│   ├── prompts/                     ← 共享 prompt 模板
│   ├── templates/                   ← 共享模板
│   └── scripts/                     ← 共享脚本
│
├── users/                           ← 用户数据根目录
│   ├── users.db                     ← SQLite：users 表、refresh_tokens 表、job_shares 表、public_shares 表、im_bindings 表（见 06-sharing.md）
│   └── {userID}/                    ← 每用户独立目录
│       ├── agent/
│       │   ├── settings.json        ← 用户个人设置
│       │   ├── models.json          ← 用户私有模型（叠加全局）
│       │   ├── recent_dirs.json
│       │   ├── prompts/
│       │   ├── templates/
│       │   ├── scripts/
│       │   ├── schedules/
│       │   └── usage_stats/
│       ├── workspaces/              ← 用户的 workspace
│       │   └── {ws-id}/
│       │       ├── .meta/
│       │       │   └── workspace.json
│       │       └── jobs/...
│       ├── user_input/              ← 用户输入日志
│       ├── im/                      ← IM 消息映射与数据
│       ├── uploads/                 ← 上传文件
│       └── wechat/                  ← 微信账号
│
├── agent/                           ← 单用户模式保留（兼容）
├── workspaces/                      ← 单用户模式保留（兼容）
├── user_input/                      ← 单用户模式保留（兼容）
├── im/                              ← 单用户模式保留（兼容）
├── uploads/                         ← 单用户模式保留（兼容）
└── wechat/                          ← 单用户模式保留（兼容）
```

## 3. 路径解析改造

### 改造方案：Context 透传

所有路径解析函数统一增加 `userID` 参数。在多用户模式下，路径解析到 `$LOCAL_MEMORY/users/{userID}/` 下对应子目录；单用户模式下，行为不变，继续使用 `$LOCAL_MEMORY/` 根目录。

需要改造的路径函数包括（**原则：所有基于 `$LOCAL_MEMORY` 的 path helper 都必须 user-aware**）：

| 函数 | 单用户路径 | 多用户路径 |
|---|---|---|
| `AgentDir` | `$LOCAL_MEMORY/agent/` | `$LOCAL_MEMORY/users/{userID}/agent/` |
| `ModelsConfigFile` | `$LOCAL_MEMORY/agent/models.json` | `$LOCAL_MEMORY/users/{userID}/agent/models.json` |
| `SettingsConfigFile` | `$LOCAL_MEMORY/agent/settings.json` | `$LOCAL_MEMORY/users/{userID}/agent/settings.json` |
| `RecentDirsFile` | `$LOCAL_MEMORY/agent/recent_dirs.json` | `$LOCAL_MEMORY/users/{userID}/agent/recent_dirs.json` |
| `PromptsDir` | `$LOCAL_MEMORY/agent/prompts/` | `$LOCAL_MEMORY/users/{userID}/agent/prompts/` |
| `TemplatesDir` | `$LOCAL_MEMORY/agent/templates/` | `$LOCAL_MEMORY/users/{userID}/agent/templates/` |
| `ScriptsDir` | `$LOCAL_MEMORY/agent/scripts/` | `$LOCAL_MEMORY/users/{userID}/agent/scripts/` |
| `ShellTempDir` | `$LOCAL_MEMORY/agent/tmp/shell/` | `$LOCAL_MEMORY/users/{userID}/agent/tmp/shell/` |
| `SchedulesDir` | `$LOCAL_MEMORY/agent/schedules/` | `$LOCAL_MEMORY/users/{userID}/agent/schedules/` |
| `UsageStatsDir` | `$LOCAL_MEMORY/agent/usage_stats/` | `$LOCAL_MEMORY/users/{userID}/agent/usage_stats/` |
| `LocalWorkspacesDir` | `$LOCAL_MEMORY/workspaces/` | `$LOCAL_MEMORY/users/{userID}/workspaces/` |
| `UserInputDir` | `$LOCAL_MEMORY/user_input/` | `$LOCAL_MEMORY/users/{userID}/user_input/` |
| `IMJobMappingDir` | `$LOCAL_MEMORY/im/mappings/` | `$LOCAL_MEMORY/users/{userID}/im/mappings/` |
| `IMMessageDir` | `$LOCAL_MEMORY/im/{chatID}/` | `$LOCAL_MEMORY/users/{userID}/im/{chatID}/` |
| `UploadsDir` | `$LOCAL_MEMORY/uploads/` | `$LOCAL_MEMORY/users/{userID}/uploads/` |
| `WeChatAccountsDir` | `$LOCAL_MEMORY/wechat/accounts/` | `$LOCAL_MEMORY/users/{userID}/wechat/accounts/` |
| `WeChatSyncBufFile` | `$LOCAL_MEMORY/wechat/accounts/{botID}.sync.json` | `$LOCAL_MEMORY/users/{userID}/wechat/accounts/{botID}.sync.json` |

新增 `GlobalDir` 函数返回 `$LOCAL_MEMORY/global/`，不接受 userID 参数。

### 对 service 层的影响

所有 repository 和 service 的方法统一增加 `ctx` 作为第一个参数。userID 在中间件中放入 ctx，service 内部从 ctx 取 userID 后调用路径解析函数。单用户模式下 userID 固定为 `"default"`。

## 4. 数据库选型

| 需求 | 方案 |
|---|---|
| 用户表（注册、登录、CRUD） | SQLite（`users.db`） |
| refresh token | SQLite 同一个库 |
| job 分享索引（shared_with 反向查询） | SQLite 同一个库 |
| workspace/job/session 数据 | 继续文件存储（不迁 DB） |
| usage stats | 继续文件存储 |

SQLite 选型理由：
- 零运维，嵌入式，与现有"单进程 + 文件存储"一致
- Go 生态有 `modernc.org/sqlite`（纯 Go，无 CGO）或 `mattn/go-sqlite3`
- 只存用户表、token 表和分享索引，数据量极小
- 未来需要扩展到 PostgreSQL 时，SQL 层可通过 interface 替换

### 数据库表设计

**users 表：**

| 列 | 类型 | 约束 | 说明 |
|---|---|---|---|
| id | TEXT | PRIMARY KEY | user ID |
| username | TEXT | NOT NULL COLLATE NOCASE, UNIQUE | 登录名（大小写不敏感） |
| email | TEXT | COLLATE NOCASE, UNIQUE | 可选邮箱（大小写不敏感） |
| password | TEXT | NOT NULL | bcrypt hash |
| avatar_url | TEXT | DEFAULT '' | 头像 URL |
| role | TEXT | DEFAULT 'user' | 'admin' 或 'user' |
| status | TEXT | DEFAULT 'active' | 'active'、'disabled'、'deleted' |
| token_version | INTEGER | DEFAULT 0 | 每次密码修改或禁用时自增 |
| created_at | DATETIME | DEFAULT CURRENT_TIMESTAMP | |
| updated_at | DATETIME | DEFAULT CURRENT_TIMESTAMP | |

**refresh_tokens 表：**

| 列 | 类型 | 约束 | 说明 |
|---|---|---|---|
| selector | TEXT | PRIMARY KEY | refresh token 的 selector 部分（明文，用于定位记录） |
| user_id | TEXT | NOT NULL, FK → users(id) | 所属用户 |
| verifier_hash | TEXT | NOT NULL | verifier 部分的 SHA256 hash |
| issued_token_version | INTEGER | NOT NULL | 签发时 user 的 TokenVersion，用于刷新时比对 |
| expires_at | DATETIME | NOT NULL | 过期时间 |
| created_at | DATETIME | DEFAULT CURRENT_TIMESTAMP | |

**job_shares 表（组织内分享反向索引）：**

| 列 | 类型 | 约束 | 说明 |
|---|---|---|---|
| id | TEXT | PRIMARY KEY | 记录 ID |
| owner_id | TEXT | NOT NULL | job 所有者的 user ID |
| workspace_id | TEXT | NOT NULL | job 所在 workspace |
| job_id | TEXT | NOT NULL | 被分享的 job ID |
| shared_with_id | TEXT | NOT NULL, FK → users(id) | 被分享给的 user ID |
| created_at | DATETIME | DEFAULT CURRENT_TIMESTAMP | |

`job_shares` 表在 `shared_with_id` 上建索引，支持高效查询"分享给我的 job"。同时增加唯一约束 `UNIQUE(owner_id, workspace_id, job_id, shared_with_id)`，防止重复分享产生重复记录；写入时使用 `INSERT OR IGNORE` 或 upsert 语义。

**public_shares 表（公开分享全局索引，详见 [06-sharing.md](./06-sharing.md) 第 1 节）：**

| 列 | 类型 | 约束 | 说明 |
|---|---|---|---|
| share_token | TEXT | PRIMARY KEY | 公开分享 token |
| owner_id | TEXT | NOT NULL | job 所有者的 user ID |
| workspace_id | TEXT | NOT NULL | workspace ID |
| job_id | TEXT | NOT NULL | job ID |
| created_at | DATETIME | DEFAULT CURRENT_TIMESTAMP | |

`public_shares` 表用于匿名 public share 请求根据 shareToken 定位 ownerID 和数据目录。唯一约束：`UNIQUE(owner_id, workspace_id, job_id)`，确保同一个 job 同时只有一个有效 share token；share 创建时按 `(owner_id, workspace_id, job_id)` 做 upsert，自动替换旧 token。

**im_bindings 表（IM 消息路由全局索引，详见 [06-sharing.md](./06-sharing.md) 第 3 节）：**

| 列 | 类型 | 约束 | 说明 |
|---|---|---|---|
| id | TEXT | PRIMARY KEY | 记录 ID |
| platform | TEXT | NOT NULL | 平台标识（`lark`、`wechat`） |
| app_or_bot_id | TEXT | NOT NULL | Lark app_id 或 WeChat bot ID |
| user_id | TEXT | NOT NULL, FK → users(id) | 绑定的用户 ID |
| status | TEXT | DEFAULT 'active' | `'active'` 或 `'inactive'`（用户禁用时设为 inactive） |
| created_at | DATETIME | DEFAULT CURRENT_TIMESTAMP | |

唯一约束：`UNIQUE(platform, app_or_bot_id)`，确保同一平台内 app/bot ID 不重复绑定，但不同平台允许相同字符串 ID 并存。

## 5. 迁移策略（单用户 → 多用户）

### 迁移状态机

首次启用 `QUARTET_MULTI_USER=true` 时，按以下顺序执行：

1. **初始化 SQLite**：创建 `$LOCAL_MEMORY/users/users.db`，执行表结构初始化
2. **检测旧数据**：检查以下任一旧单用户目录是否存在（任一存在即判定为有旧数据需要迁移）：
   - `$LOCAL_MEMORY/agent/`
   - `$LOCAL_MEMORY/workspaces/`
   - `$LOCAL_MEMORY/user_input/`
   - `$LOCAL_MEMORY/im/`
   - `$LOCAL_MEMORY/uploads/`
   - `$LOCAL_MEMORY/wechat/`
3. **若存在旧数据**：
   - 写入 `$LOCAL_MEMORY/.migration_pending` 标记文件，记录待迁移状态（含阶段字段 `stage`，初始值 `"pending"`）
   - 程序正常启动，但在日志和首次注册页面提示"检测到旧数据，首个注册用户将继承现有数据"
4. **首个用户注册**：
   - 用户通过 `/api/v1/auth/register` 注册，自动获得 admin 角色
   - 注册成功后，立即触发数据迁移
5. **备份**：自动将 `$LOCAL_MEMORY` 整体 tar 备份到 `$LOCAL_MEMORY/../quartet-migration-backup-<timestamp>.tar.gz`
   - 输出文件必须在 `$LOCAL_MEMORY` 外部，避免 tar 自包含导致 `file changed as we read it` warning 和非零退出码
6. **执行迁移**（在备份完成后自动触发，每完成一步更新 `.migration_pending` 中的 `stage` 字段；启动时若发现 `.migration_pending` 存在则基于 `stage` 继续未完成的步骤）：
   - `stage: "backup_done"` — 备份完成
   - 创建 `$LOCAL_MEMORY/users/{adminUserID}/` 目录
   - **先复制** `$LOCAL_MEMORY/agent/models.json` 到 `$LOCAL_MEMORY/global/models.json`（须在移动 agent/ 之前完成）
   - `stage: "models_copied"` — 全局模型已复制
   - 移动 `$LOCAL_MEMORY/agent/` → `$LOCAL_MEMORY/users/{adminUserID}/agent/`
   - **删除** `$LOCAL_MEMORY/users/{adminUserID}/agent/models.json`（旧模型只保留在 global，避免 admin 私有模型覆盖全局，详见 05-settings.md 模型合并策略）
   - `stage: "agent_moved"` — agent 目录已迁移
   - 移动 `$LOCAL_MEMORY/workspaces/` → `$LOCAL_MEMORY/users/{adminUserID}/workspaces/`
   - `stage: "workspaces_moved"` — workspaces 已迁移
   - 移动 `$LOCAL_MEMORY/user_input/` → `$LOCAL_MEMORY/users/{adminUserID}/user_input/`
   - 移动 `$LOCAL_MEMORY/im/` → `$LOCAL_MEMORY/users/{adminUserID}/im/`
   - 移动 `$LOCAL_MEMORY/uploads/` → `$LOCAL_MEMORY/users/{adminUserID}/uploads/`
   - 移动 `$LOCAL_MEMORY/wechat/` → `$LOCAL_MEMORY/users/{adminUserID}/wechat/`
   - `stage: "files_moved"` — 所有文件目录已迁移
   - **回填 public_shares 索引**：扫描 `$LOCAL_MEMORY/users/{adminUserID}/workspaces/**/jobs/**/.meta/job.json`，对存在 `shareToken` 的 job 向 `public_shares` 表插入映射记录（`shareToken → adminUserID + workspaceID + jobID`）。若出现 shareToken 冲突，中断迁移并输出冲突报告
   - `stage: "shares_indexed"` — 索引回填完成
7. **标记完成**：删除 `.migration_pending`，写入 `$LOCAL_MEMORY/.migrated` 标记文件
8. 若不存在旧数据：跳过迁移，直接写 `.migrated`

迁移是一次性的，不可逆（步骤 5 已自动备份，可手动回滚）。

### 迁移中/迁移失败的系统行为

`.migration_pending` 存在期间（`migrationStatus` 为 `running` 或 `failed`），系统处于受限模式：

- **admin token 已签发**：注册成功即返回 token，但系统能力受限
- **允许的 API**：`/api/v1/auth/*`（认证相关）、`/api/v1/health`（含 `migrationStatus` 字段）、`/api/v1/admin/migration/status`（查看迁移详情）、`/api/v1/admin/migration/retry`（迁移失败后重试）
- **禁止的 API**：其他所有业务 API 返回 `503 Service Unavailable`，body: `{ "code": "MIGRATION_IN_PROGRESS", "message": "Data migration is in progress, please wait" }`
- **UI 行为**：前端 AuthGate 检查 `health.migrationStatus`，若为 `running` 或 `pending` 则展示迁移进度页（含阶段信息），若为 `failed` 则展示错误详情和"重试"按钮
- **迁移失败**：`.migration_pending` 中写入 `status: "failed"` 和 `error` 字段；admin 通过 `POST /api/v1/admin/migration/retry` 触发从失败 stage 继续
- **scheduler 不启动**：直到 `.migration_pending` 删除（迁移完成）后才启动

## 6. 文件权限

- `$LOCAL_MEMORY/users/{userID}/` 目录 mode `0700`
- `settings.json` mode `0600`（含 API key 等敏感信息）
- SQLite 文件 mode `0600`
- `.jwt_secret` mode `0600`（JWT 签名密钥，认证根凭据；启动时若发现权限过宽应自动修正为 `0600` 并输出 warn 日志）
- 全局配置目录 mode `0755`（admin 写，user 读），但敏感文件单独设置：
  - `$LOCAL_MEMORY/global/settings.json` mode `0600`（含 `shared_roots`、`global_acp_env_vars` 等敏感配置）
  - `$LOCAL_MEMORY/global/models.json` mode `0600`（可能含模型 API key）
  - `$LOCAL_MEMORY/global/.jwt_secret` mode `0600`
  - 非敏感的 prompts/templates/scripts 目录保持 `0755`，文件保持 `0644`
