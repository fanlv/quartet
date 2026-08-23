# 05 — 配置与设置

> **已废弃：** 本文中的按用户数据隔离方案不再实施。当前方案见 `docs/feature/feature-2026-08-23-user-auth-permission-management.md`。

## 1. 配置分层

多用户模式下配置分为两类：

**启动/运行时配置**（由环境变量决定，不受 settings.json 影响）：

| 变量 | 说明 |
|---|---|
| `LOCAL_MEMORY` | 数据根目录 |
| `QUARTET_MULTI_USER` | 是否启用多用户 |
| `QUARTET_JWT_SECRET` | JWT 签名密钥 |
| `QUARTET_JWT_ACCESS_TTL` | access token 有效期 |
| `QUARTET_JWT_REFRESH_TTL` | refresh token 有效期 |

> **env 与 global settings 优先级规则**：`allow_registration` 和 `max_users` 同时出现在环境变量（`QUARTET_ALLOW_REGISTER`、`QUARTET_MAX_USERS`）和 global settings 中。优先级为：**环境变量 > global settings**。环境变量设置后为硬覆盖，只能重启修改；未设置环境变量时，以 global settings 的值为准，admin 可在运行时通过 UI 修改。

**产品设置**（用户级 > 全局级合并）：

| 层级 | 路径 | 谁管理 | 什么内容 |
|---|---|---|---|
| 用户级 | `$LOCAL_MEMORY/users/{userID}/agent/settings.json` | 用户自己 | 个人偏好、私有 API key、IM 配置 |
| 全局级 | `$LOCAL_MEMORY/global/settings.json` | admin | 系统名称、默认语言、注册开关、全局 agent 配置 |

## 2. settings.json 拆分

### 现有 settings.json（单用户）

包含以下字段（参考 `repository/settings.go` 的 `Settings` 结构体）：

| 字段 | 说明 |
|---|---|
| username | 展示用户名 |
| avatar_url | 头像 URL |
| title_agent | 标题 agent 配置 |
| message_agent | 消息 agent 配置 |
| lark_app_id | Lark 应用 ID |
| lark_app_secret | Lark 应用密钥 |
| lark_im_admin_sender_id | Lark IM 管理员发送者 ID |
| lark_im_sophia_sender_id | Lark IM Sophia 发送者 ID |
| im_workspace_id | IM 消息路由到的 workspace |
| wechat_admin_ids | WeChat 管理员 ID 列表 |
| acp_env_vars | ACP 环境变量配置 |

### 多用户拆分

**全局 settings（admin 管理）：**

| 字段 | 说明 |
|---|---|
| site_name | 站点名称，默认 "Quartet" |
| allow_registration | 是否开放注册 |
| default_language | 默认语言 |
| default_title_agent | 默认标题 agent 配置（对应现有 `TitleAgent`） |
| default_message_agent | 默认消息 agent 配置（对应现有 `MessageAgent`） |
| max_users | 最大用户数，0 = 不限 |
| shared_roots | 多用户模式下允许 workspace workdir 使用的宿主机路径白名单，格式为字符串数组，默认 `[]`（详见 [03-api.md](./03-api.md) 第 9 节文件访问模型） |
| global_acp_env_vars | 全局 ACP 环境变量（**安全约束见下方**） |

**`global_acp_env_vars` 安全边界：**
- `global_acp_env_vars` 中的变量**不注入到普通用户可触发的 ACP/Agent/Shell 进程**——因为用户可通过 `env` / `echo` 直接读取注入的环境变量，API 脱敏无法防止运行时泄露
- 只允许存放非敏感的全局配置（如 region、endpoint URL 等）；敏感凭据（API key、secret）不应放入此字段
- 若需要全局敏感凭据供 ACP 使用，应走后端代理模式：ACP 请求由后端代为签名/注入凭据，凭据不进入用户可控进程
- 普通用户的 settings API 不返回 `global_acp_env_vars` 的值

**用户 settings（每人独立）：**

| 字段 | 说明 |
|---|---|
| language | 语言偏好 |
| title_agent | 标题 agent 配置 |
| message_agent | 消息 agent 配置 |
| lark_app_id | Lark 应用 ID |
| lark_app_secret | Lark 应用密钥 |
| lark_im_admin_sender_id | Lark IM 管理员发送者 ID |
| lark_im_sophia_sender_id | Lark IM Sophia 发送者 ID |
| im_workspace_id | IM 消息路由到的 workspace |
| wechat_admin_ids | WeChat 管理员 ID 列表 |
| acp_env_vars | ACP 环境变量配置 |

> **注意**：`username` 和 `avatar_url` 不在用户 settings 中存储，统一归属 User 表（`users.db`），通过 `/api/v1/auth/me` 读取和更新。

## 3. 模型配置共享策略

| 类型 | 路径 | 可见范围 | 谁能改 |
|---|---|---|---|
| 全局模型 | `$LOCAL_MEMORY/global/models.json` | 所有用户 | admin |
| 用户私有模型 | `$LOCAL_MEMORY/users/{userID}/agent/models.json` | 仅该用户 | 用户自己 |

用户看到的模型列表 = 全局模型 + 私有模型（合并，私有优先覆盖同名全局模型）。

**敏感字段脱敏规则：**
- 普通用户可以"使用"全局模型发起请求，但**不能读取**全局模型的 API key / secret 明文
- `GET /api/v1/config/model/list` 返回的模型配置中，`api_key` 等敏感字段默认脱敏（如 `sk-****xxxx`，只保留末 4 位）
- admin 查看全局模型时同样默认脱敏；更新 secret 走单独的写入语义（提交新值覆盖，不需要先读旧值）
- `global_acp_env_vars` 不返回给普通用户（见上方安全边界说明）；普通用户的 settings API 只返回用户级 `acp_env_vars`
- 全局 settings GET API 仅 admin 可调用，且敏感字段（`shared_roots` 除外）默认脱敏

API 改造（对齐当前代码实际路由）：
- `GET /api/v1/config/model/list` — 返回合并后的列表，标注来源（global/user），敏感字段脱敏
- `POST /api/v1/config/model/create` — 默认保存到用户私有；admin 可指定 `scope: "global"`
- `POST /api/v1/config/model/delete` — 只能删自己的；admin 能删全局的

## 4. Prompt / Template / Script 共享

与模型类似的分层：

| 资源 | 全局路径 | 用户路径 | 合并策略 |
|---|---|---|---|
| Prompts | `global/prompts/` | `users/{userID}/agent/prompts/` | 用户 > 全局 |
| Templates | `global/templates/` | `users/{userID}/agent/templates/` | 用户 > 全局 |
| Scripts | `global/scripts/` | `users/{userID}/agent/scripts/` | 用户 > 全局 |

列表 API 返回合并结果；创建/编辑默认放用户目录；admin 可操作全局目录。

## 5. ScheduledTask 隔离

定时任务完全按用户隔离，不共享：
- 存储：`$LOCAL_MEMORY/users/{userID}/agent/schedules/`
- 执行：调度器遍历所有用户的 schedules，执行时注入对应用户的上下文
- 并发控制通过内存锁（per-schedule mutex + runningCount）实现，按用户隔离

### 调度器内部状态改造

当前调度器的内存状态（`runningCount`、`taskLocks`）以裸 `scheduleID` 为 key，`tick()` 只调用单一 `s.svc.List(ctx)` 加载当前（单用户）的全部任务。多用户模式下需要以下改造：

- **复合 key**：`runningCount` 和 `taskLocks` 的 key 统一改为 `userID:scheduleID` 格式，防止不同用户相同 scheduleID 的并发控制互相干扰
- **多用户遍历**：`tick()` 改为遍历所有 active 用户（`Status == "active"`），为每个用户调用 `s.svc.ListByUser(ctx, userID)` 加载其 schedules
- **UserContext 注入**：每次触发任务前，构造对应用户的 `UserContext` 并注入到 ctx 中，传递给 `TriggerFunc`
- **用户禁用/删除清理**：admin 禁用或删除用户时，按 `userID:*` 前缀清理 `runningCount` 和 `taskLocks` 中该用户的所有 entries，取消正在运行的 job

### 调度器生命周期

- **启动加载**：进程启动时，遍历所有 active 用户的 schedules 目录，加载并注册 cron entries；跳过 status 为 `disabled` 或 `deleted` 的用户
- **用户禁用/删除**：admin 禁用或删除用户时，移除该用户所有已注册的 cron entries，取消正在运行的 job
- **执行前二次检查**：每次 cron 触发执行前，先检查对应用户的 status 是否仍为 `active`，若不是则跳过执行并记录 warn 日志
- **用户上下文注入失败**：如果无法构造用户上下文（如用户已不存在），跳过执行并记录 error 日志
- **schedule CRUD**：用户创建/修改/删除定时任务时，实时更新对应的 cron entry 注册

## 6. 向后兼容

单用户模式（`QUARTET_MULTI_USER` 未设置）：
- `SettingsService.Get()` 读 `$LOCAL_MEMORY/agent/settings.json`（不变）
- `ModelService.List()` 读 `$LOCAL_MEMORY/agent/models.json`（不变）
- 不存在 `global/` 目录，不做分层合并
- 所有行为与改造前完全一致
