# Agents.md

This file documents the current architecture and development conventions for this repository.

## 代码规范
1. 目前项目还在开发中，然后重构的时候不用考虑兼容性问题.
2. 重构文档里面的话，不要有代码细节。存是功能的描述文呢.
3. 错误信息就要全量给用户显示，不要隐藏任何错误信息.
4. services 目录下的代码，尽量不要对外暴露全局的函数。比如 func ValidateWorkdir(workdir string) error 。
5. 当前 quartet 程序一般是运行在 用户的个人电脑上，或者沙箱里面。使用者只有 用户一个人。所以不用考虑 用户是否能够访问电脑上所有资源这种安全问题。

## Build & Run Commands

```bash
make build          # Build all three binaries (acp, cli, web) to bin/
make build-all      # go build ./...
make build-acp      # Build only ACP binary
make build-cli      # Build only CLI binary
make build-web      # Build only web binary
make dev            # Start backend (port 8090) + frontend (port 5173) in parallel
make run-cli        # Run CLI: go run ./cmd/cli
make run-backend    # Run web backend only (port 8090)
make run-frontend   # Run frontend only (npm run dev, port 5173)
make web            # Full setup: kill existing, build, run backend + frontend (requires LOCAL_MEMORY env)
make web-backend    # Restart web backend only
make web-stop       # Stop all services
make web-status     # Check web services status
make clean          # Remove bin/
```

Docker:

```bash
make docker-build       # Multi-platform build (linux/amd64 + linux/arm64)
make docker-build-load  # Build and load locally
make docker-run         # Run container with port mappings
```

Frontend (from `web/`):

```bash
npm run dev         # Vite dev server
npm run build       # TypeScript check + Vite build
npm run lint        # ESLint
```

Go tests: `go test ./...`

## Development Conventions

- Logging: use `pkg/logger` (`logger.Infof/Warnf/Errorf/...`). Avoid `log.Printf`, `fmt.Printf`, etc.
- Tests: during development, do not add unit tests unless explicitly requested.
- Default backend startup requires `LOCAL_MEMORY`; web server binds to `127.0.0.1:8090` by default and can be overridden with `QUARTET_LISTEN_ADDR`.

### Code Layering

新增接口时必须严格分层，禁止在 handler 中直接操作文件/数据库：

1. **`types/model/`** — 定义 Request/Response、Job、LoopConfig、Schedule、Script 等共享结构体
2. **`types/path/`** — 统一维护数据目录和文件路径拼接逻辑
3. **`repository/`** — 数据持久化层，负责 settings、jobs、sessions、templates、scripts、schedules、workspace、IM 映射等读写
4. **`services/`** — 业务逻辑层，封装 job、schedule、script、workspace、prompt、template、config 等能力
5. **`cmd/web/handler/`** — HTTP 层，只做参数校验、鉴权、响应格式化和服务编排

### API & Route Conventions

- 路由统一注册在 `cmd/web/api.go` 的 `registerRoutes`
- 当前接口风格是混合型：
  - 查询/流式接口大量使用 `GET`（如 agent list、job list、SSE、public share）
  - 创建/动作类接口主要使用 `POST`
  - 更新类接口使用 `PUT`
  - 删除类接口使用 `DELETE`
- 文件类接口既有 JSON body，也有 multipart 上传，不要再假设“所有接口都走 POST + JSON”
- 公开分享能力统一放在 `/api/v1/public/*`，通过 `shareToken` 校验

## Architecture

### 顶层入口

- `cmd/web` — Web 后端入口，承担 HTTP 路由注册、请求编排和服务装配。
- `web/` — 前端单页应用，提供聊天、设置、统计、调度、脚本等用户界面。

### 类型与路径

- `types/model` — Job、Session、Schedule、Script、ModelConfig、IM、Workspace 等共享 Request/Response 与领域结构体。
- `types/path` — 数据目录与文件路径的统一拼接规则。
- `types/agentstream`、`types/agui` — Agent 流式与 AGUI 协议的事件结构定义。
- `types/msgextra`、`types/consts` — 消息扩展字段与全局常量。

### 数据持久化（repository）

- `repository/` — 基于本地文件的存储层，负责 settings、jobs、sessions、templates、scripts、schedules、prompts、workspace、IM 映射、用户输入、模型配置、聊天上下文等数据的读写，以及原子写入、损坏处理、ID 生成等基础能力。

### 业务服务（services）

- `services/agent/acp` — 通过 ACP 协议接入的外部 Agent 进程的会话、运行与回放管理。
- `services/agent/eino` — 基于 Eino 框架的内置 Quartet 运行器与会话管理。
- `services/agent/chatctx` — Agent 聊天上下文的组装与维护。
- `services/agent/middlewares` — Agent 调用链路上的中间件，覆盖 AGENTS.md 加载、计划任务、上下文归约/总结、工具包装、Sandbox 后端等能力。
- `services/agent/round` — 单轮 Agent 交互的构建、刷新与生命周期管理。
- `services/agent/probe` — Agent 运行环境探测与 npx 自愈。
- `services/job` — Job 执行核心，覆盖普通运行、循环、步骤、Shell、状态、存储、事件分发等执行模式。
- `services/schedule` — 定时任务的注册、调度与执行。
- `services/script` — 用户自定义脚本的管理与运行。
- `services/prompt` — Prompt 模板的组装与渲染。
- `services/template` — Job/会话模板的业务封装。
- `services/session` — 会话级业务逻辑封装。
- `services/workspace` — 工作目录与最近目录管理。
- `services/config` — 全局 settings 与模型配置的业务接口。
- `services/command` — 系统命令执行的统一封装。
- `services/runtime` — 运行时控制（如重启）。
- `services/usagestats` — Token 与工具调用等用量统计的记录、累积与读取。

### 基础包（pkg）

- `pkg/acp` — ACP 协议客户端、子进程池与会话 IO 处理。
- `pkg/auth` — 后端鉴权能力。
- `pkg/messaging/lark` — 飞书 IM 接入，包括消息监听、回复、图片处理与 WS 运行时。
- `pkg/messaging/wechat` — 微信 IM 接入，包括 ilink 客户端、CDN、登录、媒体与回复。
- `pkg/modelbuilder` — 各家 LLM（OpenAI、Claude、Gemini、DeepSeek、Qwen、Ark、Ollama）的统一构建器。
- `pkg/sandbox` — 沙箱（compose、模板、管理、回收与恢复）能力。
- `pkg/fileserver` — 工作区与静态资源的文件服务。
- `pkg/llmgen` — 通用 LLM 生成调用工具。
- `pkg/tokenizer` — Token 计数。
- `pkg/logger` — 项目统一日志（必须使用）。
- `pkg/httputil`、`pkg/json`、`pkg/strutil`、`pkg/safe` — HTTP 响应、JSON、字符串、协程安全等通用工具。

### 其他

- `scripts/` — 一次性运维与数据迁移脚本。
- `docs/` — 架构与功能设计文档。

