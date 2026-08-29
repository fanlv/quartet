# AGENTS.md

This file documents the current architecture and development conventions for this repository.

## 代码规范

1. 目前项目还在开发中，然后重构的时候不用考虑兼容性问题.
2. 重构文档里面的话，不要有代码细节，只写功能描述。
3. 错误信息就要全量给用户显示，不要隐藏任何错误信息.
4. services 目录下的代码，尽量不要对外暴露全局的函数。比如 func ValidateWorkdir(workdir string) error 。
5. 当前 quartet 程序一般运行在用户的个人电脑或沙箱里的可信共享实例。账号之间不隔离工作区和业务数据，RBAC 只控制功能能力；不用考虑账号是否能访问宿主机上的资源。

## Build & Run Commands

```bash
make help                 # List Make targets and descriptions
make build-all            # Run go build ./...
make build-cli            # Build bin/quartet-cli
make build-eino-cli       # Build eino-cli and install it into INSTALL_BIN_DIR (must be on PATH)
make build-web            # Build bin/quartet-web
make build-frontend       # Type-check and build the SPA into static/; does not restart the backend
make pod-install          # Install iOS CocoaPods dependencies
make build-ios            # Build the iOS app on macOS with Xcode
make test-ios             # Build the iOS Simulator target without signing
make e2e-ios              # Run native iOS UI tests in Simulator
make test                 # Run Go build, frontend component tests, and Playwright E2E
make test-web             # Run frontend component tests (cd web && npm test)
make e2e                  # Run frontend Playwright E2E tests
make run-cli              # Run quartet-cli with go run
make run-backend          # Run web backend only (go run ./cmd/web)
make run-frontend         # Dev-only Vite server (5173, or 443 with certs); not used by make web
make web                  # Build frontend/backend, then start or restart the detached backend web service
make run                  # Alias for make web
make web-stop             # Stop the backend web service and orphan quartet-web processes
make backend-stop         # Stop backend only; watchdog untouched
make web-status           # Check backend and watchdog status
make web-logs             # Follow backend log (/tmp/quartet-backend.log)
make web-watch            # Start detached watchdog; revive the backend only after its port goes down
make web-watch-stop       # Stop the watchdog; leave a running backend untouched
make web-watch-logs       # Follow watchdog log (/tmp/quartet-watchdog.log)
make install-project-tools # Install quartet-cli and all three project skills
make install-skill        # Build/install quartet-cli, then register one skill selected by SKILL_NAME
make install-skill-cli    # Only build and install quartet-cli into INSTALL_BIN_DIR
make install-skill-run    # Only register the skill directory with the skills CLI
make install-skill-copy   # Install skill files by copying instead of symlinking
make install-skill-all    # Register the skill for every agent in SKILL_AGENTS
make install-skill-list   # List skills under SKILL_SOURCE without installing
make clean                # Remove bin/
```

> `make build` 和 `make build-acp` 目前仍残留在 Makefile 中（`make help` 里也还列着），但引用的 `cmd/acp` 已不存在，不能作为有效构建入口；使用 `make build-all` 或具体的 `build-cli`、`build-eino-cli`、`build-web` 目标。

> `stage-web` 和 `activate-web-stage` 是「设置页重启后端」功能的内部目标，由 `services/runtime` 调用：先把候选前端与候选二进制构建到一个独立的 stage 目录，候选进程启动自检通过后才提升为线上版本，失败会回滚到当前版本。不要手工执行这两个目标。

> agent 不要自己执行 `make web` 重启后端：当前 agent（ACP 子进程）跑在后端进程树之下，`make web` 会 kill 旧后端，旧后端一死，agent 这条 ACP 链会被 `Pdeathsig` 连带 SIGKILL，重启过程当场失去执行者。重启后端这一步交给用户在机器上手动执行。需要"挂掉自动拉起"时用 `make web-watch`：它 detached 在后端进程树之外，只在端口已空时才拉起服务、从不 kill 活着的进程，所以既不会误伤 agent，也能在后端宕掉后自动恢复。
> 修改前端后可以执行 `make build-frontend` 更新 `static/`；后端会直接提供新构建，刷新页面即可查看，无需重启后端。

Frontend (from `web/`):

```bash
npm run dev          # Vite dev server
npm run build        # TypeScript check + Vite build
npm run lint         # ESLint
npm test             # Vitest component/unit tests
npm run test:watch   # Vitest watch mode
npm run test:e2e     # Playwright E2E tests
npm run test:e2e:ui  # Playwright E2E UI mode
```

Go tests: `go test ./...`

## Development Conventions

- Logging: use `pkg/logger` (`logger.Infof/Warnf/Errorf/...`). Avoid `log.Printf`, `fmt.Printf`, etc.
- Tests: during development, do not add unit tests unless explicitly requested.
- Go requires `1.25` or newer.
- Default backend startup requires `LOCAL_MEMORY`; the web server binds to `0.0.0.0:8090` (plain HTTP) by default, or `0.0.0.0:443` (HTTPS, serving the built UI same-origin) when `certs/` holds `cert.pem`+`key.pem`. When TLS is active on :443, the backend additionally serves a loopback-only plaintext listener on `127.0.0.1:8090` so local tooling (quartet-cli, workflow shell scripts) can reach the API without TLS handling. `QUARTET_LISTEN_ADDR` overrides the backend address but not the TLS decision；证书存在但加载失败是硬错误，不会静默降级成 HTTP；Makefile 的 stop/status/watch 脚本仍按证书推导的 443/8090 端口工作。
- Frontend requires Node `>=22.18.0 <23` and npm `>=10.9.0 <11`.
- 跨平台：进程启停、PATH 查找、重启等有平台差异的实现按 `_unix.go` / `_windows.go` 拆分文件，不要在业务代码里写 `runtime.GOOS` 分支。查找外部 CLI 统一走 `pkg/executil.LookPath`，它在进程 PATH 之外还会兜底检查官方安装器使用的用户级目录。

### 环境变量

- 环境变量键统一在 `types/consts` 里声明并加注释，业务代码引用常量而不是裸字符串。
- 服务级：`LOCAL_MEMORY`（必需）、`QUARTET_LISTEN_ADDR`、`QUARTET_STATIC_DIR`（默认 `static`）、`QUARTET_CERTS_DIR`（默认 `certs`）、`QUARTET_CORS_ORIGINS`（默认仅同源；通配 `*` 会被忽略，Cookie 鉴权必须写明确 origin）、`QUARTET_TRUSTED_PROXIES`（默认只信回环）。
- 日志：`QUARTET_LOG_LEVEL`（debug|info|warn|error，默认 info）、`QUARTET_LOG_HTTP_BODY`（默认关闭，开启会打请求/响应正文，噪音大且可能泄漏密钥）。
- 运行时调优：`QUARTET_MAX_ACP_AGENTS`、`QUARTET_ACP_PROBE_CONCURRENCY`、`QUARTET_MESSAGES_CACHE_BYTES`、`QUARTET_SANDBOX_*`。
- 客户端：`QUARTET_BASE_URL`（quartet-cli 的后端地址，默认 `http://127.0.0.1:8090`）。
- `QUARTET_` 前缀在结束 Hook 脚本里是保留命名空间（注入上下文变量用），用户自定义变量不能占用该前缀。

### LOCAL_MEMORY Layout

- `$LOCAL_MEMORY/quartet/config/` — durable configuration such as settings, prompts, Agent catalog, Graph Workflows, schedules, message presets, and authentication users/roles.
- `$LOCAL_MEMORY/quartet/data/` — durable business data such as workspaces, Jobs, Sessions, uploads, IM records, file shares, and the WeChat outbox.
- `$LOCAL_MEMORY/quartet/usage-stats/` — persistent monthly usage statistics, month-sharded JSON written at the current schema version only; a file at any other version is rejected rather than upgraded in place. 同一 schema 版本内的内容迁移（模型 ID 归一、工作空间名回填）在服务里就地完成，并与写入/刷盘串行。
- `$LOCAL_MEMORY/var/quartet/state/` — durable runtime state such as authenticated sessions, schedule state, and sandbox compose state.
- `$LOCAL_MEMORY/var/quartet/cache/` and `$LOCAL_MEMORY/var/quartet/tmp/` — reconstructable cache and process-owned temporary files.
- Quartet 自有根目录和分类目录统一由 `types/path` 解析；handler 和 service 必须复用路径/仓储接口，不要硬编码 `LOCAL_MEMORY` 布局。

### Code Layering

新增接口时必须严格分层，禁止在 handler 中直接操作文件/数据库：

1. **`types/model/`** — 定义 Request/Response、认证、Agent、Job、Graph、Schedule、消息预设、IM 等共享结构体
2. **`types/path/`** — 统一维护数据目录和文件路径拼接逻辑
3. **`repository/`** — 数据持久化层，负责 auth、settings、Agent catalog、jobs、sessions、Graph、schedules、message presets、workspace、IM 与分享数据等读写
4. **`services/`** — 业务逻辑层，封装 auth、Agent、Job、Graph、schedule、workspace、prompt、config、skills、IM outbox 等能力
5. **`cmd/web/handler/`** — HTTP 层，只做参数校验、鉴权、响应格式化和服务编排

`pkg/` 放与 quartet 业务无关、可被多层复用的通用能力（协议客户端、IM 接入、沙箱、文件存储、日志、Hook 执行器等），不反向 import `services/`。跨 service 需要共享一段执行语义时（例如结束 Hook 同时被 graph 与 job 触发），抽到 `pkg/` 而不是让两个 service 互相依赖。

### API & Route Conventions

- 路由统一注册在 `cmd/web/api.go` 的 `registerRoutes`
- `/api/v1/health`、`POST /api/v1/auth/init`、`POST /api/v1/auth/login` 不走会话鉴权；health 只用于探测服务和认证初始化状态，返回 buildTime、实例 ID 和认证状态，供客户端选择「初始化 / 登录 / 恢复 / 就绪」流程。
- 其余私有 `/api/v1/*` 默认走 `quartet_session` Cookie 会话鉴权并按路由校验 RBAC 权限；除 `GET`、`HEAD`、`OPTIONS` 外还必须携带登录响应返回的 `X-CSRF-Token`。
- 新增私有路由必须挂权限：权限枚举、中文描述和权限之间的依赖关系集中在 `services/auth` 声明，路由注册时用 `permit(...)` 包一层；优先复用已有权限，确实是新能力再加新权限。
- `/api/v1/public/*` 只提供只读分享：Job 分享路由通过 `shareTokenMiddleware` 校验 `shareToken`，`/api/v1/public/file-preview/*` 是独立的分组，由 handler 校验独立的 `fileShareToken`。分享面只暴露 GET 读取与事件流，任何动作类、版本类路由都保持鉴权。
- 图标代理这类「服务端按调用方给的 URL 发请求」的能力必须留在鉴权后面，公共分享走不接受调用方 URL 的专用路由。
- 当前接口风格是混合型：
  - 查询/流式接口大量使用 `GET`（如 agent list、job list、SSE、public share）
  - 创建/动作类接口主要使用 `POST`
  - 更新类接口主要使用 `PUT`，局部更新可使用 `PATCH`（如 workspace）
  - 删除类接口使用 `DELETE`
- 文件类接口既有 JSON body，也有 multipart 上传，不要再假设"所有接口都走 POST + JSON"
- 响应压缩：JSON 响应体超过阈值时由中间件 gzip；SSE 和 body stream（文件下载）一律跳过，新增流式接口不要改成缓冲响应，否则会破坏 flush 语义。
- 未匹配路由回落：非 API 路径回落到前端静态构建（SPA index fallback），未知 `/api` 路径返回 JSON 404；该 fallback 注册在所有具体路由之后。
- SSE 连接模型：graph 工作流任务页面同一时刻只保留一条长连接——run live（pending/running/stepStopping）时只订阅 `/api/v1/job/:id/graph-run/events`，run 非 live（terminal/awaitingInput）时只订阅 `/api/v1/job/:id/events`；GraphRunProgress 等组件不得再开第二条流。站点当前全链路 HTTP/1.1，浏览器单域名仅约 6 条连接，SSE 占满会让 stop 等普通 POST 在 socket 池里饿死

### agent-browser 页面鉴权

- Quartet 账号记录保存在 `$LOCAL_MEMORY/quartet/config/auth/users/{userID}.json`（文件权限 `0600`）。文件内包含用户名和 bcrypt `passwordHash`，不保存明文密码；不要尝试从该文件恢复密码。
- 服务端登录会话保存在 `$LOCAL_MEMORY/var/quartet/state/auth/sessions/{tokenHash}.json`（文件权限 `0600`），浏览器持有对应的 HttpOnly `quartet_session` Cookie。服务端只保存 Cookie token 的哈希，不能根据 session 文件手工构造 Cookie。
- 使用 agent-browser 调试页面时，固定使用当前 worktree 派生的 session，并在每条命令中带上 `--restore --restore-save auto`，让首次登录产生的 Cookie 在后续调试及浏览器重启后继续复用：

  ```bash
  agent-browser --session "$(agent-browser session id --scope worktree --prefix quartet)" --restore --restore-save auto open --enable react-devtools 'https://devbox.fanlv.fun/?workspaceId=ws-1'
  agent-browser --session "$(agent-browser session id --scope worktree --prefix quartet)" --restore --restore-save auto snapshot -i
  ```

- 如果恢复后仍显示登录页，先查看 `agent-browser auth list`。已有对应 auth profile 时，在同一个 session 中执行 `agent-browser --session "$(agent-browser session id --scope worktree --prefix quartet)" --restore --restore-save auto auth login <profile>`；没有 profile 时由用户使用 `agent-browser auth save <profile> --url 'https://devbox.fanlv.fun/' --username '<username>' --password-stdin` 一次性录入，禁止把密码写在命令行、AGENTS.md、日志或代码中。登录完成后继续复用上述 session，不要每次新建 session 或重复登录。

## Architecture

### 顶层入口

- `cmd/web` — Web 后端入口，承担 HTTP 路由注册、中间件、请求编排和服务装配。
- `cmd/eino-cli` — 独立 eino-cli 二进制的入口；eino 能力全部抽出为该 ACP agent，quartet 只经 ACP 接入。
- `cmd/quartet-cli` — quartet-cli 命令行入口，通过后端 HTTP API 登录并按 base URL 保存 Cookie/CSRF 会话。命令分组：`workflow`（图工作流增删改查、校验、手动运行）、`schedule`（cron 定时任务）、`workspace`（只读）、`job`（查询与停止）、`agent`（只读列出已安装 ACP agent）、`wechat`（发送主动消息、查询账号）、`auth`（登录/查看/清除会话）。
- `einocli/` — eino-cli 的全部实现（`app` 命令层：serve/headless/prompt/replay/sessions；`runtime` 推理循环；`middlewares` 中间件链：AGENTS.md 加载、计划任务、上下文削减与总结、工具包装、sandbox 后端；`chatctx` 上下文组装；`round` 轮次管理；`store` 会话与上下文持久化；`modelbuilder` 多供应商模型构建（openai/claude/gemini/deepseek/qwen/ark/ollama）；`config` 自管配置，以及自带的 `types`/`json`/`logger`/`tokenizer`），与 quartet 后端零 import 依赖，按"日后可整体抽到独立仓库"设计。
- `web/` — React/Vite 单页应用，提供聊天、工作区、文件浏览、Graph 编排、设置、统计、调度、Agent 管理和 IM 配置等界面。
- `ios/` — 原生 SwiftUI 客户端，提供局域网连接与登录、Job/对话、附件、Graph 运行、定时任务和统计能力；目录内还有更具体的 `ios/AGENTS.md`（含 UI/主题/排版/弹窗等规范，改 iOS 代码前必读）。

### 类型与路径

- `types/model` — Auth、Agent、Job、Session、Graph、Schedule、MessagePreset、IM、Workspace、Skill、用户输入与事件等共享 Request/Response 和领域结构体。
- `types/path` — 数据目录与文件路径的统一拼接规则。
- `types/agentstream`、`types/agui` — Agent 流式与 AGUI 协议的事件结构定义。
- `types/msgextra`、`types/consts` — 消息扩展字段，以及默认值、环境变量键、错误码等全局常量。

### 数据持久化（repository）

- `repository/` — 基于本地文件的存储层，负责 auth、settings、Agent catalog、ACP 探测缓存、jobs、sessions、Graph Workflows/Runs、schedules、message presets、prompts、workspace、文件分享、IM/WeChat outbox、IM-Job 映射、用户输入、聊天上下文缓存与最近目录等数据的读写，以及原子写入、损坏处理、ID 生成和并发锁。

### 业务服务（services）

- `services/agent/acp` — 通过 ACP 协议接入的外部 Agent 进程（含 eino-cli）的会话、运行与回放管理；是 quartet 唯一的 agent 接入路径。
- `services/agent/catalog` — 内置与自定义 Agent 目录、稳定 AgentID、运行定义修订及历史引用解析。
- `services/agent/install`、`services/agent/versioncheck` — Agent 安装状态检查、受控安装/升级/卸载和版本检查。
- `services/agent/chatctx` — Agent 聊天上下文的组装与维护。
- `services/agent/round` — 单轮 Agent 交互的构建、刷新与生命周期管理。
- `services/agent/probe` — Agent 安装与 ACP 能力探测、并发/冷却控制、持久化缓存及 npx 自愈。
- `services/agent/usage` — 已支持 Agent 的订阅配额与 CLI 版本读取。
- `services/agent/internal/acpstate`、`services/agent/internal/sessioncache` — ACP 状态转换与带租约的 Agent 会话缓存。
- `services/auth` — 初始化、登录、Cookie/CSRF 会话、用户/角色与 RBAC 权限管理（权限枚举、描述和依赖关系的唯一来源）。
- `services/einocli` — 设置页 eino tab 的后端编排：exec `eino-cli models` / `eino-cli systemprompt` 子命令读写 eino-cli 自管配置（密钥不出 eino-cli 进程）。
- `services/graph` — Graph Workflow 校验、DAG/Loop 调度、变量替换与条件求值、运行版本、恢复、控制、Shell 结点控制、事件缓冲，以及 Prompt/终点结点的 Hook 触发（执行器复用 `pkg/shellhook`）。
- `services/job` — Job 执行核心，覆盖交互式消息运行、持久消息队列、状态恢复、观察记录、用量归集、事件分发，以及每轮交互结束后的结束 Hook 触发。
- `services/messagepreset` — 全局与工作区消息预设的合并、校验、乐观并发和孤儿配置处理。
- `services/schedule` — 定时任务的注册、调度与执行。
- `services/prompt` — Prompt 模板的组装与渲染。
- `services/session` — 会话级业务逻辑封装。
- `services/workspace` — 工作目录与最近目录管理。
- `services/config` — 全局 settings 的业务接口。
- `services/command` — 系统命令执行的统一封装。
- `services/runtime` — 运行时控制：后端重启走「构建候选版本 → 候选进程自检 → 提升 → 失败回滚」流程。
- `services/skills` — 项目/全局 Skill 清单缓存以及受控安装入口。
- `services/usagestats` — Token 与工具调用等用量统计的记录、累积、读取和同版本内的内容迁移。
- `services/wechatoutbox` — 微信主动消息的持久队列、分片、限速与重试。

### 基础包（pkg）

- `pkg/acp` — ACP 协议客户端、子进程池、会话 IO、事件缓冲与跨平台进程属性/终止处理。
- `pkg/shellhook` — 结束 Hook（graph Prompt/终点结点、对话每轮结束）共用的脚本执行器：统一超时、环境变量注入规则和「纯副作用」语义——输出只用于展示/日志，失败只告警，绝不改变调用方状态。
- `pkg/executil` — 外部 CLI 的跨平台解析与启动，PATH 之外兜底检查官方安装器使用的用户级目录。
- `pkg/messaging` — IM 通用类型与文件名处理，下辖 `lark`（消息监听、回复、图片处理与 WS 运行时）、`wechat`（ilink 客户端、CDN、登录、媒体、回复与主动推送：`Replier.SendText`，经 `POST /api/v1/wechat/send` 暴露给定时任务/脚本，ContextToken 持久化在 `$LOCAL_MEMORY/quartet/data/wechat/accounts/user_tokens.json`）和 `media`（IM 媒体缓存）。
- `pkg/sandbox` — 沙箱（compose、模板、管理、回收与恢复）能力。
- `pkg/fileserver` — repository 与文件 API 共用的本地/沙箱文件存储抽象。
- `pkg/tokenizer` — Token 计数。
- `pkg/logger` — 项目统一日志（必须使用）。
- `pkg/httputil`、`pkg/json`、`pkg/strutil`、`pkg/safe` — HTTP 响应、JSON、字符串、协程安全等通用工具。

### 前端结构（web/）

- `src/components/` — 页面级与业务组件，按域再分 `graph/`、`settings/`、`stats/`、`FileViewer/` 子目录。
- `src/hooks/`、`src/contexts/` — 聊天/任务列表/文件预览/消息预设等数据 hook，以及连接状态、服务端时钟等全局上下文。
- `src/utils/` — SSE 客户端（普通流与 graph 流各一套）、消息合并、API 响应解析、命令与斜杠补全、时间与统计格式化等纯逻辑，单测就近放在同目录。
- `src/i18n/` — 文案集中在 `locales/zh.json` 与 `locales/en.json`，界面文本一律走 i18n，不在组件里写死中英文。
- `src/types/` — 与后端 `types/model` 对应的协议、消息与 graph 类型。
- `e2e/` — Playwright 用例与 fixtures。

### 其他

- `scripts/` — Make 目标调用的 shell 脚本：前端环境检查与依赖安装、后端启停与重启、进程树终止、看门狗、候选版本提升，以及微信 ilink 移植代码与上游的差异对比。
- `static/`、`bin/`、`certs/` — 分别是前端构建产物、Go 构建产物和 TLS 证书目录，都不入库；`certs/` 里是否存在 `cert.pem`+`key.pem` 决定后端是否走 HTTPS。
- `docs/` — 架构与功能设计文档，命名与写法见 `docs/AGENTS.md`。
- `skill/` — 随项目发布的 `quartet-workflow`、`quartet-schedule`、`quartet-wechat` Skills。
- 就在 main 分支开发，不要在其他分支开发。
- ACP 工具并发错误后不要 reset session；reset 会丢失全部上下文，应保留 session 并处理并发/租约问题。
