# Agents.md

This file documents the current architecture and development conventions for this repository.

## 代码规范

1. 目前项目还在开发中，然后重构的时候不用考虑兼容性问题.
2. 重构文档里面的话，不要有代码细节，只写功能描述。
3. 错误信息就要全量给用户显示，不要隐藏任何错误信息.
4. services 目录下的代码，尽量不要对外暴露全局的函数。比如 func ValidateWorkdir(workdir string) error 。
5. 当前 quartet 程序一般是运行在 用户的个人电脑上，或者沙箱里面。使用者只有 用户一个人。所以不用考虑 用户是否能够访问电脑上所有资源这种安全问题。

## Build & Run Commands

```bash
make build-all     # go build ./...
make build-web     # Build web backend binary to bin/quartet-web
make build-eino-cli# Build the standalone eino-cli ACP agent and install it onto $PATH
                   #   (required for agent probe and the settings eino tab to work)
make test          # Run Go build, frontend component tests, and Playwright E2E
make test-web      # Run frontend component tests (cd web && npm test)
make e2e           # Run frontend Playwright E2E tests
make run-backend   # Run web backend only (go run ./cmd/web)
make run-frontend  # Dev-only vite dev server (npm run dev; 5173 or 443 with certs). NOT used by `make web`.
make web           # Build frontend into static/, build backend, then start/restart the backend as the SINGLE
                   #   web service: it serves the built UI and, when certs/ holds cert.pem+key.pem, terminates
                   #   HTTPS on :443 plus a loopback-only plaintext companion on 127.0.0.1:8090
                   #   (else plain HTTP on 127.0.0.1:8090 only). Detached; survives caller exit.
make run           # Alias for make web
make web-stop      # Stop the backend web service and orphan quartet-web processes
make backend-stop  # Stop backend only; watchdog untouched
make web-status    # Check backend + watchdog status
make web-logs      # Follow backend log (/tmp/quartet-backend.log)
make web-watch     # Start detached watchdog: revives the backend if its port goes down
make web-watch-stop# Stop the watchdog (running services are left untouched)
make web-watch-logs# Follow watchdog log (/tmp/quartet-watchdog.log)
make clean         # Remove bin/
```

> agent 不要自己执行 `make web` 重启后端：当前 agent（ACP 子进程）跑在后端进程树之下，`make web` 会 kill 旧后端，旧后端一死，agent 这条 ACP 链会被 `Pdeathsig` 连带 SIGKILL，重启过程当场失去执行者。重启后端这一步交给用户在机器上手动执行。需要”挂掉自动拉起”时用 `make web-watch`：它 detached 在后端进程树之外，只在端口已空时才拉起服务、从不 kill 活着的进程，所以既不会误伤 agent，也能在后端宕掉后自动恢复。
> 修改前端完以后可以 make build-frontend 更页面到 static 目录下。然后后端可以刷新后查看更新

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
- Default backend startup requires `LOCAL_MEMORY`; the web server binds to `127.0.0.1:8090` (plain HTTP) by default, or `0.0.0.0:443` (HTTPS, serving the built UI same-origin) when `certs/` holds `cert.pem`+`key.pem`. When TLS is active on :443, the backend additionally serves a loopback-only plaintext listener on `127.0.0.1:8090` so local tooling (quartet-cli, workflow shell scripts) can reach the API without TLS handling. `QUARTET_LISTEN_ADDR` overrides the address but not the TLS decision; the served UI dir defaults to `static/` (`QUARTET_STATIC_DIR`) and the cert dir to `certs/` (`QUARTET_CERTS_DIR`).
- Frontend requires Node `>=22.18.0 <23` and npm `>=10.9.0 <11`.

### Code Layering

新增接口时必须严格分层，禁止在 handler 中直接操作文件/数据库：

1. **`types/model/`** — 定义 Request/Response、Job、LoopConfig、Schedule、Script 等共享结构体
2. **`types/path/`** — 统一维护数据目录和文件路径拼接逻辑
3. **`repository/`** — 数据持久化层，负责 settings、jobs、sessions、schedules、workspace、IM 映射等读写
4. **`services/`** — 业务逻辑层，封装 job、schedule、workspace、prompt、config 等能力
5. **`cmd/web/handler/`** — HTTP 层，只做参数校验、鉴权、响应格式化和服务编排

### API & Route Conventions

- 路由统一注册在 `cmd/web/api.go` 的 `registerRoutes`
- `/api/v1/health` 不走鉴权，用于前端探测服务状态和是否需要 token。
- `/api/v1/*` 默认走 `agentAuthMiddleware`，`/api/v1/public/*` 走 `shareTokenMiddleware`。
- 当前接口风格是混合型：
  - 查询/流式接口大量使用 `GET`（如 agent list、job list、SSE、public share）
  - 创建/动作类接口主要使用 `POST`
  - 更新类接口使用 `PUT`
  - 删除类接口使用 `DELETE`
- 文件类接口既有 JSON body，也有 multipart 上传，不要再假设“所有接口都走 POST + JSON”
- 公开分享能力统一放在 `/api/v1/public/*`，通过 `shareToken` 校验
- SSE 连接模型：graph 工作流任务页面同一时刻只保留一条长连接——run live（pending/running/stepStopping）时只订阅 `/api/v1/job/:id/graph-run/events`，run 非 live（terminal/awaitingInput）时只订阅 `/api/v1/job/:id/events`；GraphLoopProgress 等组件不得再开第二条流。站点当前全链路 HTTP/1.1，浏览器单域名仅约 6 条连接，SSE 占满会让 stop 等普通 POST 在 socket 池里饿死

## Architecture

### 顶层入口

- `cmd/web` — Web 后端入口，承担 HTTP 路由注册、中间件、请求编排和服务装配。
- `cmd/eino-cli` — 独立 eino-cli 二进制的入口；eino 能力全部抽出为该 ACP agent，quartet 只经 ACP 接入。
- `cmd/quartet-cli` — quartet-cli 命令行工具的入口，通过后端 HTTP API 管理 graph workflow（agent 库；含手动运行 run）、cron 定时任务（schedule 增删改查/启停/立即触发）、只读查询 workspace/job/agent，以及发送微信主动消息（wechat send / accounts）。
- `einocli/` — eino-cli 的全部实现（推理循环、中间件链、上下文组装、会话管理、多模态还原、local sandbox fork、自管配置），与 quartet 后端零 import 依赖，按"日后可整体抽到独立仓库"设计。
- `web/` — 前端单页应用，提供聊天、工作区、文件浏览、设置、统计、调度、脚本、IM 配置等用户界面。

### 类型与路径

- `types/model` — Job、Session、Schedule、Script、IM、Workspace、FlowNode、事件等共享 Request/Response 与领域结构体。
- `types/path` — 数据目录与文件路径的统一拼接规则。
- `types/agentstream`、`types/agui` — Agent 流式与 AGUI 协议的事件结构定义。
- `types/msgextra`、`types/consts` — 消息扩展字段与全局常量。

### 数据持久化（repository）

- `repository/` — 基于本地文件的存储层，负责 settings、jobs、sessions、schedules、prompts、workspace、IM 映射、IM 消息、用户输入、聊天上下文、最近目录等数据的读写，以及原子写入、损坏处理、ID 生成等基础能力。

### 业务服务（services）

- `services/agent/acp` — 通过 ACP 协议接入的外部 Agent 进程（含 eino-cli）的会话、运行与回放管理；是 quartet 唯一的 agent 接入路径。
- `services/agent/chatctx` — Agent 聊天上下文的组装与维护。
- `services/agent/round` — 单轮 Agent 交互的构建、刷新与生命周期管理。
- `services/agent/probe` — Agent 运行环境探测与 npx 自愈。
- `services/agent/internal/sessioncache` — Agent 会话缓存等内部复用能力。
- `services/einocli` — 设置页 eino tab 的后端编排：exec `eino-cli models` / `eino-cli systemprompt` 子命令读写 eino-cli 自管配置（密钥不出 eino-cli 进程）。
- `services/job` — Job 执行核心，覆盖交互式消息运行、状态、存储、事件分发等执行模式。
- `services/schedule` — 定时任务的注册、调度与执行。
- `services/prompt` — Prompt 模板的组装与渲染。
- `services/session` — 会话级业务逻辑封装。
- `services/workspace` — 工作目录与最近目录管理。
- `services/config` — 全局 settings 的业务接口。
- `services/command` — 系统命令执行的统一封装。
- `services/runtime` — 运行时控制（如重启）。
- `services/usagestats` — Token 与工具调用等用量统计的记录、累积与读取。

### 基础包（pkg）

- `pkg/acp` — ACP 协议客户端、子进程池与会话 IO 处理。
- `pkg/messaging/lark` — 飞书 IM 接入，包括消息监听、回复、图片处理与 WS 运行时。
- `pkg/messaging/wechat` — 微信 IM 接入，包括 ilink 客户端、CDN、登录、媒体、回复与主动推送（`Replier.SendText`，经 `POST /api/v1/wechat/send` 暴露给定时任务/脚本，ContextToken 持久化在 `data/wechat/accounts/user_tokens.json`）。
- `pkg/messaging/media` — IM 媒体缓存等通用能力。
- `pkg/sandbox` — 沙箱（compose、模板、管理、回收与恢复）能力。
- `pkg/fileserver` — 工作区与静态资源的文件服务。
- `pkg/tokenizer` — Token 计数。
- `pkg/logger` — 项目统一日志（必须使用）。
- `pkg/httputil`、`pkg/json`、`pkg/strutil`、`pkg/safe` — HTTP 响应、JSON、字符串、协程安全等通用工具。

### 其他

- `docs/` — 架构与功能设计文档。
- 就在 main 分支开发，不要在其他分支开发。
- agent-browser --session deepagent-bubble --headers "{\"x-agent-auth\":\"${X_AGENT_AUTH%%,*}\"}" open --enable react-devtools 'https://devbox.fanlv.fun/?workspaceId=ws-1' 可以查看页面。`X_AGENT_AUTH` 使用逗号分隔多个值，此命令取第一个。
- ACP 工具并发错误后未 reset session 这个不要 reset session ，reset session 会丢失所有上下文。
