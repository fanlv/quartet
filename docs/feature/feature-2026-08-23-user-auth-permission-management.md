# 共享实例的用户登录、用户管理与权限管理

> 创建日期：2026-08-23
> 状态：方案
> 本文是本次用户模块改造的实施依据；与 `docs/multi-user/` 中旧草案冲突时，以本文为准。

## 1. 背景与目标

Quartet 当前使用 `X_AGENT_AUTH` 环境变量和 `X-AGENT-AUTH` 请求头保护私有接口。该凭证只代表“知道共享密钥的调用方”，无法识别具体用户，也无法支持账号停用、角色分配和权限控制。

本功能将引入完整的本地用户模块：

- 首次启动且不存在用户数据时，进入初始化流程，引导创建第一个管理员账号。
- 用户使用用户名和密码登录，登录成功后统一通过 Cookie 访问私有接口。
- 管理员可以创建、编辑、停用、删除和重置其他用户。
- 管理员可以创建角色并为角色分配权限，再把一个或多个角色授予用户。
- 用户、角色和认证策略保存到 `LOCAL_MEMORY` 下受 Git 管理的位置。
- 彻底移除 `X_AGENT_AUTH`、`X-AGENT-AUTH` 以及 URL `token` 参数的私有接口鉴权能力。

## 2. 核心设计结论

1. **默认启用用户认证**：不提供旧鉴权与新鉴权并存的开关；升级后必须完成管理员初始化才能访问私有功能。
2. **Cookie 是唯一登录凭证**：浏览器、iOS 和 `quartet-cli` 均先登录，再携带服务端签发的会话 Cookie；不再接受共享 Token、Bearer Token 或 URL Token 作为私有接口凭证。
3. **使用服务端会话**：Cookie 只保存高强度随机会话标识，用户状态和权限以服务端当前数据为准，便于立即撤销。
4. **RBAC 权限模型**：用户绑定一个或多个角色，实际权限为所有角色权限的并集；首期不支持单用户例外授权和显式拒绝规则。
5. **共享实例，不做用户数据隔离**：所有账号共同使用同一套 workspace、job、session、workflow、schedule、文件、配置和统计数据。权限只控制用户能否查看或执行某类操作，不按创建人、所有者或用户目录限制资源范围。
6. **Git 管理持久配置，不管理登录会话**：用户、密码摘要、角色和权限配置进入 Git 可跟踪目录；Cookie 和服务端会话属于可撤销运行状态，不进入 Git。
7. **公开分享保持独立**：`/api/v1/public/*` 继续通过现有分享凭证提供匿名只读能力，不把分享凭证当作登录态。

这是“一个 Quartet 实例上的多账号与角色管理”，不是面向互不信任租户的数据隔离方案。具有某项读取权限的用户可以读取该类别下的全部实例数据；具有某项管理或执行权限的用户可以操作该类别下的全部实例数据。用户被停用或删除后，既有业务数据、定时任务和公开分享关系保持不变。

## 3. 系统状态与首次初始化

系统认证模块有三种状态：

| 状态 | 条件 | 可用能力 | 前端表现 |
| --- | --- | --- | --- |
| 待初始化 | 从未完成初始化，且没有用户数据 | 健康检查、初始化、公开分享 | 自动进入“创建管理员”页面 |
| 已就绪 | 配置完整，且至少有一个启用中的管理员 | 登录、公开分享及登录后的业务接口 | 未登录显示登录页，已登录进入应用 |
| 需要恢复 | 已有初始化标记，但用户或角色数据损坏，或不存在启用中的管理员 | 健康检查、公开分享和本机恢复 | 显示完整错误与恢复指引，不重新开放首个管理员注册 |

### 3.1 初始化流程

1. 后端启动时检查认证配置和初始化标记。
2. 若系统处于“待初始化”，健康检查返回明确的初始化状态，前端只展示管理员注册页，不发送其他私有业务请求。
3. 为避免远程部署在首次访问时被他人抢先注册，后端生成一次性初始化码并输出到后端日志；注册管理员时必须同时提交该初始化码。
4. 用户填写管理员用户名、显示名称、密码和确认密码。
5. 初始化成功后创建第一个用户并授予内置 `admin` 角色，同时建立登录会话。
6. 初始化只能成功一次；并发提交时只允许一个请求成功，其他请求得到明确的冲突错误。
7. 初始化标记与用户数量分开判断。系统曾初始化后，即使用户文件损坏、被误删或没有可用管理员，也只能进入恢复状态，不能重新开放匿名初始化。

### 3.2 恢复原则

- 优先从 Memory 仓库的 Git 历史恢复认证配置。
- 恢复旧版本用户配置不会恢复旧登录会话，所有用户需要重新登录。
- 提供仅限本机操作者使用的离线恢复能力，用于重置管理员密码或恢复管理员角色；不提供匿名 HTTP 重置入口。
- 配置解析、唯一性或权限引用错误必须完整展示，不能静默忽略损坏记录后继续启动。

## 4. 用户模型与生命周期

每个用户包含以下信息：

| 信息 | 说明 |
| --- | --- |
| 用户 ID | 创建后不变的稳定标识，不使用用户名充当 ID |
| 用户名 | 登录名；去除首尾空白后按大小写不敏感规则保持唯一 |
| 显示名称 | 用于界面展示，可由用户本人或管理员修改 |
| 密码摘要 | 只保存慢速、带盐的密码摘要，永不保存或返回明文密码 |
| 角色列表 | 用户拥有的角色 ID，可绑定多个角色 |
| 状态 | `active`、`disabled` 或 `deleted` |
| 强制改密 | 管理员重置密码后标记，用户下次登录必须先修改密码 |
| 时间与操作者 | 创建、更新时间，以及创建人和最后修改人 |

用户生命周期规则：

- 首个用户固定为管理员。后续不开放自助注册，只能由有用户管理权限的管理员创建账号。
- 创建用户时至少分配一个角色；未显式选择时使用内置 `member` 角色。
- 停用用户后立即禁止新登录并撤销其全部已有会话，不删除其产生的共享业务数据。
- 删除采用软删除：账号不能再登录，所有会话立即失效，历史操作者信息继续可识别。
- 已删除用户名首期不允许重复使用，避免历史记录中的同名用户产生歧义。
- 管理员可以设置一次性临时密码；用户使用临时密码登录后，只能访问改密和退出接口，完成改密后才恢复其他权限。
- 用户修改自己的密码时必须验证当前密码，成功后撤销除当前会话外的其他会话，并轮换当前会话。
- 任意用户、角色或状态修改都不能导致系统不存在启用中的管理员。

## 5. 角色与权限模型

### 5.1 角色规则

- `admin`：内置系统角色，拥有当前及未来新增的全部权限，不允许编辑或删除。
- `member`：内置日常使用角色，可查看和操作现有共享业务数据，但不能管理用户、角色和系统级配置。
- `viewer`：内置只读角色，可查看业务数据、运行状态和统计，不可发起执行或修改内容。
- 自定义角色：管理员可以创建、复制、重命名、调整权限和删除。
- 删除仍被用户引用的角色前，必须先解除引用或为相关用户指定替代角色。
- 用户拥有多个角色时权限取并集；不提供“拒绝权限”覆盖允许权限的规则。
- 权限修改立即对该角色下所有用户生效，不要求重新登录。

### 5.2 稳定权限目录

权限项由产品内置，角色文件只能引用下表中的稳定权限 ID，不能创建任意字符串权限。权限 ID 发布后不得改名；废弃权限只能通过配置版本迁移处理。

| 权限 ID | 能力范围 |
| --- | --- |
| `workspace.read` | 查看工作空间、默认目录和最近目录 |
| `workspace.write` | 创建、编辑、排序、收藏和删除工作空间，更新最近目录 |
| `job.read` | 查看 Job、Session、运行状态、事件和 Hook |
| `job.execute` | 创建 Job、发送消息、停止或继续普通与 Graph 运行、调整运行版本 |
| `job.manage` | 重命名、置顶、删除 Job 和删除 Graph run |
| `job.share` | 创建和取消 Job 公开分享 |
| `workflow.read` | 查看和校验工作流 |
| `workflow.write` | 创建、编辑和删除工作流 |
| `workflow.execute` | 发起工作流运行 |
| `schedule.read` | 查看定时任务 |
| `schedule.write` | 创建、编辑、删除和启停定时任务 |
| `schedule.execute` | 立即运行定时任务 |
| `file.read` | 浏览、搜索、读取和下载文件，查看 Git 分支 |
| `file.write` | 创建目录、写入和上传文件 |
| `file.share` | 查看、创建和取消文件分享 |
| `agent.read` | 查看 Agent、目录、显示信息、用量、版本和安装候选项 |
| `agent.manage` | 安装、升级、卸载、自定义、恢复、删除和重新验证 Agent |
| `config.read` | 查看 Quartet、Prompt、Eino 和 Agent 配置 |
| `config.write` | 修改 Quartet、Prompt、Eino 和 Agent 配置 |
| `im.read` | 查看微信账号、登录状态、待处理项和 outbox 状态 |
| `im.manage` | 登录或退出微信、处理管理员与待处理项 |
| `im.send` | 发送微信主动消息 |
| `stats.read` | 查看实例用量统计 |
| `logs.read` | 查看后端日志 |
| `logs.manage` | 清理日志和修改日志级别 |
| `logs.report` | 上报当前客户端的前端日志 |
| `skills.read` | 查看、搜索和检查 Skill |
| `skills.manage` | 安装、添加、更新和删除 Skill |
| `system.manage` | 执行重启等实例级系统操作 |
| `users.read` | 查看用户列表与用户详情 |
| `users.manage` | 创建、编辑、停用、恢复、删除和重置用户密码 |
| `roles.read` | 查看权限目录、角色列表与角色详情 |
| `roles.manage` | 创建、复制、编辑和删除角色 |

新增私有接口时必须在路由注册处显式声明一个权限 ID，或明确声明为“仅需登录态”的用户自助接口。未声明鉴权策略的私有接口必须拒绝注册或默认拒绝访问。读取与修改权限分开，不能只按 HTTP 方法推断权限，因为当前存在使用 `POST` 的查询接口。

管理类与执行类权限依赖完成其操作所需的读取权限。例如 `job.execute` 依赖 `job.read`、`agent.read` 和 `workspace.read`，`users.manage` 依赖 `users.read` 和 `roles.read`，其他模块的 `write`、`manage`、`execute`、`share` 权限至少依赖同资源的 `read` 权限。保存自定义角色时若缺少依赖权限，系统必须拒绝并完整列出缺失项，不做静默补全。

### 5.3 私有路由权限矩阵

以下矩阵覆盖当前私有路由以及本功能新增路由，是本期鉴权改造的基线。一个单元格列出多个路径时，它们使用相同权限。

| 路由 | 方法 | 所需权限 |
| --- | --- | --- |
| `/api/v1/auth/me` | GET、PUT | 有效登录会话 |
| `/api/v1/auth/password`、`/api/v1/auth/logout` | PUT、POST | 有效登录会话 |
| `/api/v1/users`、`/api/v1/users/:userId` | GET | `users.read` |
| `/api/v1/users` | POST | `users.manage` |
| `/api/v1/users/:userId` | PUT、DELETE | `users.manage` |
| `/api/v1/users/:userId/reset-password` | POST | `users.manage` |
| `/api/v1/permissions`、`/api/v1/roles`、`/api/v1/roles/:roleId` | GET | `roles.read` |
| `/api/v1/roles` | POST | `roles.manage` |
| `/api/v1/roles/:roleId` | PUT、DELETE | `roles.manage` |
| `/api/v1/icon` | GET | `agent.read` |
| `/api/v1/agent/list`、`/api/v1/agent/catalog`、`/api/v1/agent/catalog/deleted`、`/api/v1/agent/catalog/:agentId` | GET | `agent.read` |
| `/api/v1/agent/display-info/resolve` | POST | `agent.read` |
| `/api/v1/agent/usage`、`/api/v1/agent/version`、`/api/v1/agent/versions`、`/api/v1/agent/install/candidates` | GET | `agent.read` |
| `/api/v1/agent/config` | POST | `job.execute` |
| `/api/v1/agent/install`、`/api/v1/agent/:agentId/upgrade`、`/api/v1/agent/:agentId/uninstall`、`/api/v1/agent/:agentId/revalidate` | POST | `agent.manage` |
| `/api/v1/agent/custom`、`/api/v1/agent/custom/:agentId/restore`、`/api/v1/agent/custom/:agentId/delete` | POST | `agent.manage` |
| `/api/v1/agent/custom/:agentId` | PUT | `agent.manage` |
| `/api/v1/agent/custom/:agentId/delete-impact` | GET | `agent.manage` |
| `/api/v1/list-dir`、`/api/v1/file-exists`、`/api/v1/search-files`、`/api/v1/git-branch`、`/api/v1/serve-file` | GET | `file.read` |
| `/api/v1/read-file` | POST | `file.read` |
| `/api/v1/mkdir`、`/api/v1/write-file`、`/api/v1/upload-file` | POST | `file.write` |
| `/api/v1/recent-dirs` | GET | `workspace.read` |
| `/api/v1/recent-dirs` | POST | `workspace.write` |
| `/api/v1/sessions/:sessionId/messages` | GET | `job.read` |
| `/api/v1/prompt/get` | POST | `config.read` |
| `/api/v1/prompt/save` | POST | `config.write` |
| `/api/v1/config/eino/model/list`、`/api/v1/config/eino/system-prompt` | GET | `config.read` |
| `/api/v1/config/eino/model/create`、`/api/v1/config/eino/system-prompt` | POST | `config.write` |
| `/api/v1/config/eino/model/:modelId` | DELETE | `config.write` |
| `/api/v1/config/settings/get`、`/api/v1/config/settings/title-generation-agent`、`/api/v1/config/settings/group-reply-agent`、`/api/v1/config/settings/im-session-agent` | GET | `config.read` |
| `/api/v1/config/settings/save` | POST | `config.write` |
| `/api/v1/config/settings/title-generation-agent`、`/api/v1/config/settings/group-reply-agent`、`/api/v1/config/settings/im-session-agent`、`/api/v1/config/settings/agent/:agentId/env`、`/api/v1/config/settings/agent/:agentId/prefs` | PUT | `config.write` |
| `/api/v1/wechat/login/status`、`/api/v1/wechat/accounts`、`/api/v1/wechat/pending`、`/api/v1/wechat/outbox/status`、`/api/v1/wechat/outbox/:taskId` | GET | `im.read` |
| `/api/v1/wechat/login/start`、`/api/v1/wechat/logout`、`/api/v1/wechat/pending/dismiss`、`/api/v1/wechat/admin/add`、`/api/v1/wechat/admin/remove` | POST | `im.manage` |
| `/api/v1/wechat/send` | POST | `im.send` |
| `/api/v1/logs/list` | GET | `logs.read` |
| `/api/v1/logs/clear`、`/api/v1/logs/level` | POST | `logs.manage` |
| `/api/v1/logs/frontend` | POST | `logs.report` |
| `/api/v1/skills/list`、`/api/v1/skills/check`、`/api/v1/skills/find` | GET | `skills.read` |
| `/api/v1/skills/install-project-tools`、`/api/v1/skills/add`、`/api/v1/skills/remove`、`/api/v1/skills/update` | POST | `skills.manage` |
| `/api/v1/graph/workflow/list`、`/api/v1/graph/workflow/:workflowId` | GET | `workflow.read` |
| `/api/v1/graph/workflow/validate` | POST | `workflow.read` |
| `/api/v1/graph/workflow` | POST | `workflow.write` |
| `/api/v1/graph/workflow/:workflowId` | PUT、DELETE | `workflow.write` |
| `/api/v1/graph/run/start` | POST | `workflow.execute` |
| `/api/v1/job/list`、`/api/v1/job/observations`、`/api/v1/job/:jobId`、`/api/v1/job/:jobId/events`、`/api/v1/job/:jobId/graph-run`、`/api/v1/job/:jobId/graph-run/events`、`/api/v1/job/:jobId/graph-run/hooks` | GET | `job.read` |
| `/api/v1/job/create`、`/api/v1/job/:jobId/message`、`/api/v1/job/:jobId/stop`、`/api/v1/job/:jobId/graph-run/stop`、`/api/v1/job/:jobId/graph-run/step-stop`、`/api/v1/job/:jobId/graph-run/cancel-stop`、`/api/v1/job/:jobId/graph-run/resume`、`/api/v1/job/:jobId/graph-run/continue` | POST | `job.execute` |
| `/api/v1/job/:jobId/graph-run/version` | PUT | `job.execute` |
| `/api/v1/job/:jobId/title`、`/api/v1/job/:jobId/pin` | PUT | `job.manage` |
| `/api/v1/job/:jobId`、`/api/v1/job/:jobId/graph-run` | DELETE | `job.manage` |
| `/api/v1/job/:jobId/share`、`/api/v1/job/:jobId/unshare` | POST | `job.share` |
| `/api/v1/workspace/list`、`/api/v1/workspace/default-workdir`、`/api/v1/workspace/:id` | GET | `workspace.read` |
| `/api/v1/workspace/create`、`/api/v1/workspace/regenerate-colors` | POST | `workspace.write` |
| `/api/v1/workspace/order`、`/api/v1/workspace/:id/favorite` | PUT | `workspace.write` |
| `/api/v1/workspace/:id` | PATCH、DELETE | `workspace.write` |
| `/api/v1/stats/usage` | GET | `stats.read` |
| `/api/v1/system/restart-web` | POST | `system.manage` |
| `/api/v1/schedule/list`、`/api/v1/schedule/:scheduleId` | GET | `schedule.read` |
| `/api/v1/schedule/create` | POST | `schedule.write` |
| `/api/v1/schedule/:scheduleId` | PUT、DELETE | `schedule.write` |
| `/api/v1/schedule/:scheduleId/toggle` | POST | `schedule.write` |
| `/api/v1/schedule/:scheduleId/run` | POST | `schedule.execute` |
| `/api/v1/file-share/get` | GET | `file.share` |
| `/api/v1/file-share/create`、`/api/v1/file-share/delete` | POST | `file.share` |

`/api/v1/health`、`/api/v1/auth/init`、`/api/v1/auth/login` 和 `/api/v1/public/*` 不属于私有权限矩阵，分别按健康检查、初始化、登录和分享凭证规则处理。`/api/v1/auth/verify` 由轻量的 `/api/v1/auth/me` 替代；`/auth/me` 只读取用户与权限信息，不得触发 Agent 探测或其他重型业务加载。

### 5.4 默认角色权限

| 角色 | 默认权限 |
| --- | --- |
| `admin` | 所有当前及未来权限 |
| `member` | `workspace.read`、`workspace.write`、`job.read`、`job.execute`、`job.manage`、`job.share`、`workflow.read`、`workflow.write`、`workflow.execute`、`schedule.read`、`schedule.write`、`schedule.execute`、`file.read`、`file.write`、`file.share`、`agent.read`、`config.read`、`stats.read`、`logs.report`、`skills.read` |
| `viewer` | `workspace.read`、`job.read`、`workflow.read`、`schedule.read`、`file.read`、`agent.read`、`config.read`、`stats.read`、`logs.report`、`skills.read` |

## 6. Cookie 会话与接口鉴权

### 6.1 登录会话

- 登录成功后由服务端创建可撤销会话，并通过 host-only、`HttpOnly`、`SameSite=Strict` Cookie 返回。
- HTTPS 访问时 Cookie 必须带 `Secure`；明确使用本机或可信局域网 HTTP 时允许非 Secure Cookie，并在客户端持续展示明文传输风险。
- 会话同时具备空闲过期时间和最长存活时间。每次有效访问可以延长空闲时间，但不能突破最长存活时间。
- 服务端只保存会话标识的摘要；Cookie 原值不写入文件日志、业务响应、Git 或可读前端存储。
- 每次请求都校验会话、用户状态和用户当前角色，不把权限固化在 Cookie 中。
- 退出登录删除当前服务端会话并清除 Cookie。用户停用、删除、管理员重置密码时撤销该用户全部会话。
- 会话过期或失效返回 `401`；已登录但权限不足返回 `403`，并返回完整、可定位的错误信息。

### 6.2 CSRF 防护

Cookie 会被浏览器自动携带，因此所有会改变状态的私有请求必须通过 CSRF 校验。CSRF 凭证只用于证明请求来自当前客户端，不承担用户身份认证。浏览器、iOS 和 CLI 均应按统一契约发送；读取接口和 SSE 不要求 CSRF 凭证。

### 6.3 路由分类

| 路由类型 | 认证要求 |
| --- | --- |
| 健康检查 | 匿名可访问，仅返回服务与认证状态，不返回用户数据 |
| 初始化 | 仅“待初始化”状态可访问，并校验一次性初始化码 |
| 登录 | 匿名可访问；成功后签发 Cookie |
| 公开分享 | 保留现有分享凭证校验，只读，不创建用户登录态 |
| 私有业务接口 | 必须有有效登录 Cookie，并通过对应权限校验 |
| 退出与当前用户信息 | 必须有有效登录 Cookie |
| 用户与角色管理 | 必须有有效登录 Cookie，并具有对应管理权限 |

所有原先受 `agentAuthMiddleware` 保护的接口都迁移到“会话认证 + 权限校验”。以下旧能力同步删除：

- `X_AGENT_AUTH` 环境变量对 API 的鉴权作用。
- `X-AGENT-AUTH` 请求头。
- 私有资源通过 `?token=` 传递登录凭证的兼容路径。
- Web localStorage 中的共享 Token 和全局请求头注入逻辑。
- iOS Keychain 中的旧共享 Token。
- `quartet-cli` 读取共享 Token 环境变量并附加请求头的行为。

## 7. 数据保存与 Git 管理

### 7.1 目录边界

持久认证配置保存到以下 Git 可跟踪区域：

```text
$LOCAL_MEMORY/quartet/config/auth/
├── system.json       # 初始化状态与认证策略
├── users/            # 每个用户一份独立记录
└── roles/            # 内置角色快照与自定义角色
```

登录会话保存到运行状态区域：

```text
$LOCAL_MEMORY/var/quartet/state/auth/sessions/
```

`quartet/config/auth/` 不得加入 Memory 仓库的忽略规则；`var/` 继续不受 Git 跟踪。

### 7.2 Git 语义

- 用户和角色变更以原子方式落盘，写入成功后即对当前实例生效，并表现为 Memory 仓库工作区中的普通 Git 变更。
- Quartet 首期不自动执行 `git add`、`git commit`、`git pull` 或 `git push`，提交和同步仍由现有 Memory 管理流程负责。
- 启动时完整校验认证配置。存在重复用户名、未知角色、未知权限、缺失管理员或不可解析文件时进入恢复状态，不静默修复或跳过。
- 外部 Git 操作修改认证文件后，需要显式重新加载或重启后端才生效；加载失败时保留上一份有效内存快照并展示完整错误。
- Git 回滚用户配置可能恢复旧密码摘要、账号状态或权限。执行回滚后应主动撤销全部会话，并要求用户重新登录。
- 密码摘要按产品要求与用户记录一起进入 Git 管理。这意味着摘要会保留在 Git 历史中，密码修改无法抹除旧提交中的摘要；该风险作为本方案的明确取舍被接受，Memory 仓库必须保持私有。

## 8. API 能力规划

下表描述产品能力和接口边界，具体请求字段沿用统一的错误响应与版本冲突规范。

| 能力 | 方法与路径 | 访问条件 |
| --- | --- | --- |
| 获取系统状态 | `GET /api/v1/health` | 匿名 |
| 初始化管理员 | `POST /api/v1/auth/init` | 待初始化 + 一次性初始化码 |
| 登录 | `POST /api/v1/auth/login` | 匿名 |
| 退出 | `POST /api/v1/auth/logout` | 已登录 |
| 获取当前用户和有效权限 | `GET /api/v1/auth/me` | 已登录 |
| 更新个人资料 | `PUT /api/v1/auth/me` | 已登录 |
| 修改自己的密码 | `PUT /api/v1/auth/password` | 已登录 |
| 列出用户、查看用户详情 | `GET /api/v1/users`、`GET /api/v1/users/:userId` | `users.read` |
| 创建、修改、删除用户 | `POST /api/v1/users`、`PUT/DELETE /api/v1/users/:userId` | `users.manage` |
| 重置用户密码 | `POST /api/v1/users/:userId/reset-password` | `users.manage` |
| 获取权限目录 | `GET /api/v1/permissions` | `roles.read` |
| 列出角色、查看角色详情 | `GET /api/v1/roles`、`GET /api/v1/roles/:roleId` | `roles.read` |
| 创建、修改、删除角色 | `POST /api/v1/roles`、`PUT/DELETE /api/v1/roles/:roleId` | `roles.manage` |

接口行为约束：

- 用户和角色修改携带当前版本，陈旧版本写入返回 `409`，避免两个管理员互相覆盖。
- 用户列表和详情永不返回密码摘要、会话标识或 CSRF 凭证。
- 用户名或密码错误返回统一的凭证错误；其他校验、存储和权限错误按仓库约定完整返回。
- 登录接口需要限制连续失败频率，但不能因为单个来源的失败长期锁死所有用户。
- 所有用户与角色变更记录操作者、目标、动作和结果；日志中不得出现密码、Cookie 或会话摘要。

## 9. 前端与其他客户端

### 9.1 Web

- `AuthGate` 改为按健康检查结果在初始化页、登录页、恢复页和主应用之间切换。
- Web 不再展示、保存或粘贴共享 Token；所有同源请求、图片和 SSE 自动携带 Cookie。
- 收到 `401` 时清理前端用户态并回到登录页；收到 `403` 时保留登录态，展示完整权限错误。
- 导航和操作入口根据 `/auth/me` 返回的有效权限隐藏或禁用，但后端仍必须执行最终权限校验。
- 设置区域新增“用户管理”和“角色与权限”页面；只有具备相应权限的用户可见。
- 管理员在编辑角色时可以看到该角色影响的用户数量，并在删除或收回关键权限前确认影响。

### 9.2 iOS

- 连接页从“服务地址 + Token”调整为“服务地址 + 用户名 + 密码”。
- 登录成功后使用系统 Cookie 存储；不再把 `X-AGENT-AUTH` Token 写入 Keychain。
- Cookie 失效时回到登录页，保留服务地址和非敏感离线缓存，但不得继续展示为已连接。
- iOS 暂不提供用户和角色管理界面；已登录用户仍受服务端权限控制，权限不足时展示完整错误。

### 9.3 quartet-cli

- 新增登录、查看当前用户和退出命令。登录时交互式读取密码，避免密码进入 shell 历史。
- 登录后按服务地址保存 Cookie 会话，文件权限仅允许当前系统用户读取。
- 后续命令自动携带 Cookie 和需要的 CSRF 凭证。会话失效时明确提示重新登录。
- 删除 `X_AGENT_AUTH` 的读取、帮助文案和请求头逻辑。非交互自动化首期不提供长期 API Key，需使用专门登录会话。

## 10. 旧鉴权迁移清单

本功能发布时必须同步清理所有仍会指导用户或客户端发送旧 Token 的入口。不能只替换后端中间件。

| 范围 | 必须完成的迁移 |
| --- | --- |
| 后端路由与配置 | 用登录会话和权限校验替换旧鉴权；删除 `X_AGENT_AUTH`、`X-AGENT-AUTH`、私有 URL `token`、旧鉴权探测字段和旧 CORS 请求头配置 |
| Web | 删除 localStorage Token、全局请求头注入和 Token 输入页；改为初始化、登录、当前用户和 Cookie 流程 |
| iOS | 删除 Token 输入、Keychain Token 和请求头注入；改为用户名密码登录及系统 Cookie 会话 |
| `quartet-cli` | 删除共享 Token 环境变量和请求头逻辑；增加按服务地址保存和使用登录 Cookie 的命令 |
| 项目内 Skills | 更新 `skill/workflow`、`skill/schedule`、`skill/wechat` 的说明、脚本和排障步骤，禁止再建议设置或读取 `X_AGENT_AUTH` |
| 自动化脚本 | 检查所有调用 `quartet-cli` 或直接访问 `/api/v1/*` 的脚本，改用新的 CLI 登录会话；失效时完整报告登录错误 |
| 测试 | 单元测试、前端组件测试和 E2E fixture 改为先初始化管理员或加载测试认证配置，再使用 Cookie；补齐匿名、登录、角色权限和旧 Token 拒绝场景 |
| 使用文档 | 更新 `README.md`、`README.zh-CN.md`、`ios/README.md`、`AGENTS.md` 中的启动、调试和浏览器访问说明 |
| 架构文档 | 重写 `docs/arch/permissions/` 中 HTTP 鉴权、SSE、环境变量、错误码和测试章节 |
| 历史测试方案 | 更新 `docs/feature/feature-2026-06-03-web-e2e.md` 和 `docs/feature/feature-2026-06-04-e2e-real-model.md` 中仍要求固定共享 Token 的测试环境约束 |
| 旧多用户草案 | `docs/multi-user/` 的 JWT、SQLite、兼容模式和用户数据隔离方案与本文冲突，发布前删除或整体标记为已废弃，不得继续作为实施依据 |

迁移完成的静态检查标准：除历史迁移说明和“旧能力已删除”的说明外，活跃源码、测试、Skills、README 和架构文档中不得再出现 `X_AGENT_AUTH`、`X-AGENT-AUTH`、`agentAuthMiddleware` 或通过私有 URL `token` 鉴权的用法。`shareToken` 和 `fileShareToken` 属于公开分享能力，不在删除范围内。

## 11. 用户故事与验收标准

### US-001：首次初始化管理员

**用户故事：** 作为首次启动 Quartet 的操作者，我希望创建管理员账号，以便进入系统并继续配置其他用户。

**验收标准：**

- [ ] 无认证数据时，Web 自动展示管理员初始化页，且不会请求 workspace、job、agent 等私有接口。
- [ ] 初始化必须校验一次性初始化码、用户名、密码和确认密码。
- [ ] 初始化成功后用户拥有 `admin` 角色并直接进入应用。
- [ ] 重复或并发初始化不能创建第二个首任管理员。
- [ ] 已初始化但认证数据异常时进入恢复页，不重新开放初始化。
- [ ] 在浏览器中验证初始化成功、初始化码错误、并发提交和异常恢复页面。

### US-002：用户登录与退出

**用户故事：** 作为已创建的用户，我希望使用账号密码登录，并在退出后立即失去访问权限。

**验收标准：**

- [ ] 登录成功后，Cookie 能用于普通请求、文件请求和 SSE，前端不保存共享 Token。
- [ ] 未登录、Cookie 无效或已过期时，所有私有接口统一返回 `401`。
- [ ] 退出后原 Cookie 不能再次访问私有接口。
- [ ] 禁用、删除或重置密码后，该用户的所有已有会话立即失效。
- [ ] 页面展示完整的连接、校验和服务端错误。
- [ ] 在浏览器中验证刷新页面、长连接、退出、过期和多标签页状态。

### US-003：管理用户

**用户故事：** 作为管理员，我希望维护用户及其状态和角色，以便控制谁能使用 Quartet。

**验收标准：**

- [ ] 管理员可以查看、创建、编辑、停用、软删除用户并重置密码。
- [ ] 普通用户不能访问用户管理接口或页面。
- [ ] 用户名按大小写不敏感规则保持唯一。
- [ ] 系统始终至少保留一个启用中的管理员。
- [ ] 管理员重置密码后，用户下次登录必须修改密码。
- [ ] 并发编辑不会静默覆盖较新的用户数据。
- [ ] 在浏览器中验证用户的创建、编辑、停用、恢复、删除和密码重置。

### US-004：管理角色与权限

**用户故事：** 作为管理员，我希望用角色组合权限并授予用户，以便控制各类操作能力。

**验收标准：**

- [ ] 管理员可以查看权限目录，创建、复制、编辑和删除自定义角色。
- [ ] 内置 `admin` 角色不能被编辑或删除。
- [ ] 用户绑定多个角色时，权限按并集生效。
- [ ] 角色权限修改后，对已有会话立即生效。
- [ ] 仍被用户引用的角色不能直接删除。
- [ ] 角色配置只接受权限目录中的稳定权限 ID，未知权限 ID 必须明确拒绝。
- [ ] 当前全部私有路由都具有明确权限映射，新增但未声明鉴权策略的私有路由默认不可访问。
- [ ] 前端入口可见性与后端授权结果一致，直接调用接口也不能绕过权限。
- [ ] 在浏览器中使用 admin、member、viewer 和自定义角色分别验证允许与拒绝场景。

### US-005：Git 管理认证配置

**用户故事：** 作为实例维护者，我希望用户与权限配置出现在 Memory 仓库中，以便审阅、备份和恢复。

**验收标准：**

- [ ] 初始化和后续用户、角色变更只修改 `$LOCAL_MEMORY/quartet/config/auth/` 下的持久配置。
- [ ] 这些文件能被 Memory 根仓库检测为 Git 变更，且不被 `.gitignore` 排除。
- [ ] 登录、刷新页面和退出不会产生 Git 工作区变更。
- [ ] Cookie、会话标识和 CSRF 凭证不会进入 Git 跟踪文件。
- [ ] 从 Git 恢复有效配置并重启后，用户与角色恢复，但旧会话不可复用。
- [ ] 损坏、冲突或不完整配置会进入恢复状态并展示完整错误。

### US-006：迁移所有客户端和私有接口

**用户故事：** 作为实例维护者，我希望所有客户端只使用用户会话，以便彻底停止共享 Token 鉴权。

**验收标准：**

- [ ] Web、iOS 和 `quartet-cli` 均可登录并通过 Cookie 调用其有权访问的接口。
- [ ] `X_AGENT_AUTH`、`X-AGENT-AUTH` 和私有 URL `token` 均不能再授权任何请求。
- [ ] 浏览器原生图片请求和 SSE 不再依赖查询参数中的登录凭证。
- [ ] 公开 Job 与文件分享仍可通过各自分享凭证只读访问。
- [ ] 现有私有接口均已归属权限项，未归属的接口默认拒绝。
- [ ] README、架构文档、旧多用户草案、项目内 Skills、自动化脚本和浏览器调试说明已按迁移清单更新。
- [ ] 活跃源码、测试与使用文档中不存在仍可使用旧共享 Token 的入口或操作指引。
- [ ] Web 组件测试、前端构建、Go 构建和关键端到端场景通过。

## 12. 实施顺序

1. 建立认证配置、用户、角色、会话和初始化状态能力。
2. 建立登录、退出、当前用户、Cookie 会话和 CSRF 契约。
3. 将现有私有接口切换到会话认证，并完成权限目录映射。
4. 完成用户管理与角色权限管理接口及 Web 页面。
5. 替换 Web 的 AuthGate 和共享 Token 注入逻辑。
6. 迁移 iOS 与 `quartet-cli` 登录流程。
7. 删除旧环境变量、请求头、查询参数，并迁移测试、Skills、自动化脚本和使用文档。
8. 删除或整体废弃 `docs/multi-user/` 旧方案，避免存在两套冲突的实施依据。
9. 完成初始化、登录、权限矩阵、Cookie/SSE、Git 恢复和多客户端回归验收。

这次改造应作为一次原子版本升级发布：服务端开始拒绝 `X-AGENT-AUTH` 时，Web、iOS 和 `quartet-cli` 必须同时具备 Cookie 登录能力，避免出现部分客户端永久无法访问的中间状态。

## 13. 非目标

- 不支持匿名自助注册、邀请注册、邮箱验证、找回密码或第三方 OAuth。
- 不支持长期 API Key、Personal Access Token、Bearer Token 或服务账号。
- 不做 workspace、job、session、workflow、schedule、文件、模型、配置和用量数据的用户归属、按用户过滤或物理隔离。
- 不做团队、组织、资源级 ACL、单用户直接授权和显式拒绝规则。
- 不改变现有公开 Job 分享和文件分享的业务模型。
- 不自动提交或推送 Memory 仓库中的 Git 变更。

## 14. 发布与回滚要求

- 发布说明必须明确这是破坏性鉴权升级，旧共享 Token 在升级后立即失效。
- 升级前备份 Memory，并确认操作者可以读取后端日志中的一次性初始化码。
- 第一次升级启动后，先完成管理员初始化，再恢复其他用户访问。
- 回滚到旧版本前必须评估旧版本会重新接受 `X_AGENT_AUTH` 的风险；新版本生成的 Cookie 不应被旧版本识别。
- 不由 agent 自动执行 `make web`；实现完成后的服务重启由用户在机器上手动执行。
