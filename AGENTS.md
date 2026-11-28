# AGENTS.md

## 开发约定

- 在 `main` 分支开发，Git 提交信息使用英文。项目仍在开发，重构时可直接调整接口与数据结构。
- 用 `pkg/logger` 记录日志，向用户展示完整错误。仅在明确要求时新增单元测试。
- 重构与功能方案只描述功能行为。编写文档、修改 iOS 或项目 Skill 时阅读对应目录的约定。[文档](docs/AGENTS.md) · [iOS](ios/AGENTS.md) · [Skill](skill/AGENTS.md)
- 进程启停、重启和 PATH 查找的跨平台差异按平台文件封装（如 `_unix.go`、`_windows.go`）。外部 CLI 统一用 `pkg/executil.LookPath` 查找，包含用户级安装目录的兜底。
- ACP 工具并发错误在原会话处理并发或租约问题，保留会话上下文。

## 构建与验证

Go 最低版本见 `go.mod`。Web 开发使用 `web/.nvmrc` 指定的 Node 和 `web/package.json` 的 `packageManager`。完整 Make 目标以 `make help` 为准。

| 任务 | 命令 |
| --- | --- |
| 构建 | `make build` 构建 `quartet-cli`、`eino-cli`、后端和前端。单独构建用 `make build-web` 或 `make build-frontend` |
| 检查 | `make test-go`、`make test-web`、`make lint-web`、`make e2e`。`make test` 汇总 Go、Web 组件和 Playwright 测试 |
| iOS | `make build-ios` 构建应用，`make test-ios` 无签名构建模拟器目标，`make e2e-ios` 运行 UI 测试 |
| 开发服务 | `make run-backend`、`make run-frontend`。Vite 服务用于开发，正式后端同源提供 SPA 构建 |
| 安装项目工具 | `make install-project-tools` 安装 `quartet-cli` 和项目 Skills，`make install-eino-cli` 安装独立的 `eino-cli` |

## 后端运行

- **用 `make web` 重启当前后端，由用户手动执行。** 重启会中断正在运行的 ACP 会话。纯前端改动可用 `make build-frontend` 更新 `static/` 后刷新页面。
- `stage-web` 和 `activate-web-stage` 仅供 `services/runtime` 的设置页重启流程调用。候选版本通过启动自检后才会提升，失败时回滚。
- `make web-watch` 启动独立 watchdog，只在后端端口空闲时拉起服务。`make web-stop` 停止后端和 watchdog，`make backend-stop` 只停止后端，`make web-watch-stop` 只停止 watchdog。状态和日志命令见服务命令表。[服务命令](README.zh-CN.md#服务命令)
- `LOCAL_MEMORY` 必须是绝对路径。默认 HTTP 监听 `0.0.0.0:8090`。证书目录默认是 `certs/`，同时存在 `cert.pem` 和 `key.pem` 时启用 HTTPS，默认监听 `0.0.0.0:443`，ALPN 优先 `h2` 并支持 HTTP/1.1。TLS 启用时另起 `127.0.0.1:8090` 的明文监听供本地 CLI 和脚本使用。证书加载失败会报错退出。
- `QUARTET_LISTEN_ADDR` 只覆盖后端监听地址。Make 的启停、状态和 watchdog 命令按仓库 `certs/` 推导端口，使用自定义地址时注意两者的差异。
- macOS `make autostart-install` 通过 launchd 监督 watchdog。`AUTOSTART_SCOPE=agent` 在登录时启动，`daemon` 在开机时启动且装卸需 `sudo`，后端以安装用户身份运行。两种模式互斥。安装时记录 `LOCAL_MEMORY` 和 `PATH`，变更后重装。watchdog 正常停止后，launchd 会在下次登录或开机时再启动它。卸载时使用与安装相同的 scope 执行 `make autostart-uninstall`。 [自启脚本](scripts/autostart.sh)
- macOS 构建优先使用稳定签名，可用 `QUARTET_SIGN_IDENTITY` 指定身份。缺少可用身份时构建会告警。首次部署或更换身份后，按需为 `bin/quartet-web` 重新授予目录访问权限。 [签名脚本](scripts/codesign-darwin.sh)

## 代码入口与分层

- `cmd/web` 是 Web 后端入口，负责装配服务、路由与中间件。`web/` 是 React/Vite SPA，`ios/` 是 SwiftUI 客户端。
- `cmd/quartet-cli` 通过 HTTP API 驱动项目 Skills，按后端地址保存 Cookie 和 CSRF 会话。`skill/` 包含 `quartet-workflow`、`quartet-schedule`、`quartet-wechat`。
- `cmd/eino-cli` 和 `einocli/` 构成独立 CLI，自管配置和运行时。Quartet 对话经 ACP 使用 Eino，设置页经 `services/einocli` 调用它的配置子命令。
- `types/model` 放共享模型和 API 类型，`types/path` 统一解析 Quartet 数据路径。`repository/` 负责持久化、原子写入和并发控制。`services/` 负责业务逻辑并优先封装为服务方法，`cmd/web/handler` 仅做参数校验、鉴权、服务编排和响应。
- `pkg/` 放可复用的基础能力。多个服务共用的执行语义放到 `pkg/`，依赖方向从服务指向基础包。

### 常用业务入口

- `services/agent/acp` 管理对话、Agent 会话和回放。标题与群聊回复等单次文本生成由 `services/agent/textgen` 调用 CLI。
- `services/agent/catalog` 管理稳定的 Agent ID 与历史运行定义。`probe`、`authentication`、`install`、`versioncheck` 和 `usage` 分别负责探测、登录、安装、版本检查及订阅配额。`codexaccounts` 管理 Codex 账号切换与凭据协调，对客户端仅返回元信息。
- `services/job` 管理对话执行、队列与恢复，`services/session` 管理会话。`services/graph` 管理图工作流，`services/schedule` 管理 Prompt 与工作流定时器、执行记录和对应会话。 [定时任务设计](docs/feature/feature-2026-11-27-timer-management.md)
- `services/usagestats` 归集 Token 和工具调用。IM 接入由 `pkg/messaging` 和 `services/im` 承担，主动微信消息的持久队列在 `services/wechatoutbox`。

## 数据与配置

通过 `types/path` 和仓储接口访问数据。以下路径相对于 `$LOCAL_MEMORY`。

| 路径 | 内容 |
| --- | --- |
| `quartet/config/` | 认证、Prompt、工作流、定时器和消息预设 |
| `quartet/data/` | 设置、Agent 目录、工作区、Job、Session、上传、IM 和分享数据 |
| `quartet/usage-stats/` | 按月分片的统计，仅支持当前 schema。同版本的内容迁移与写入、刷盘串行 |
| `var/quartet/state/` | 会话、定时器审批和执行记录等持久运行状态 |
| `var/quartet/cache/` | 可重建缓存 |
| `var/quartet/tmp/` | 进程临时文件 |

- 新增 Quartet 业务环境变量时，在 `types/consts` 声明常量并写明含义。`QUARTET_CORS_ORIGINS` 默认仅同源，跨域 Cookie 需要明确来源。`QUARTET_TRUSTED_PROXIES` 默认只信任回环。`QUARTET_LOG_HTTP_BODY` 默认关闭，开启可能记录密钥。 [配置表](README.zh-CN.md#配置)
- Job 对话及 Graph 的 Prompt、终点结束 Hook 共用 `pkg/shellhook`。`QUARTET_` 保留给 Hook 上下文。临时脚本写入 `$LOCAL_MEMORY/var/quartet/tmp/shell/`，无法解析该根目录时使用系统临时目录。Job 工作目录仅用于执行。Hook 输出用于展示与日志，失败只记录告警。

## HTTP API 与事件流

- Quartet 面向个人电脑或可信环境。账号共享工作区和业务数据，RBAC 控制功能权限。
- 在 `cmd/web/api.go` 的 `registerRoutes` 注册路由。`GET /api/v1/health`、`POST /api/v1/auth/init`、`POST /api/v1/auth/login` 无需会话。health 返回服务状态、构建时间、实例 ID 与认证状态。
- 私有 API 使用 `quartet_session` Cookie。除 `GET`、`HEAD`、`OPTIONS` 外还需登录响应中的 `X-CSRF-Token`。新增私有路由使用 `permit(...)`，优先复用已有权限。权限定义、中文说明和依赖在 `services/auth`。
- 公共分享只开放 `GET` 读取及 SSE。Job 分享用 `shareTokenMiddleware` 校验 `shareToken`，`/api/v1/public/file-preview/*` 由 handler 校验独立的 `fileShareToken`。动作、版本及接受调用方 URL 的服务端代理走私有鉴权。
- 查询和流式接口通常用 `GET`，创建与动作通常用 `POST`，更新用 `PUT` 或 `PATCH`，删除用 `DELETE`。文件接口按需使用 JSON 或 multipart。
- gzip 只处理满足阈值的缓冲 JSON。SSE 和文件下载保持流式输出。静态回落在具体路由之后注册，非 API 路径可回落到 SPA，未知 `/api` 路径返回 JSON 404。
- **Graph 页面只维持一条 SSE。** `pending`、`running`、`stepStopping`、`sessionStopping` 订阅 `/api/v1/job/:jobId/graph-run/events`，其余状态使用 `/api/v1/job/:jobId/events`。子组件复用页面的事件流。
- 新增 SSE 用 handler 的 `newSSEWriter`，保留 flush 语义。出错时只结束当前流，保留 HTTP/2 共享连接。
- 对话结束 Hook 默认在没有可见实时查看者时运行。Web 的普通与 Graph SSE 使用稳定的 `viewerId`，在可见性变化和重连时同步状态。公共分享流不计入查看者。iOS 对话页只在 scene 为 active 时保持 SSE。

## Web 前端与页面调试

- 组件在 `web/src/components/`，状态与数据逻辑在 `hooks/`、`contexts/` 和 `utils/`。`src/types/` 对应后端共享类型，Playwright 用例和 fixtures 在 `web/e2e/`。
- 界面文本走 `web/src/i18n/locales/zh.json` 和 `en.json`。Web 单测就近放置。
- `AgentUsageCard` 先显示 CLI 版本，再显示套餐、额度或金额。用量响应应并行获取版本，缺失时使用版本探测结果。Codex 工作区可显示个人支出额度、余额和重置时间。 [Magpie 项目](https://github.com/yetone/magpie) · [Codex 工作区用量案例](https://github.com/yetone/magpie/issues/4259)
- `agent-browser` 的每条页面调试命令都复用当前 worktree 的 session，并带 `--restore --restore-save auto`。

  ```bash
  agent-browser --session "$(agent-browser session id --scope worktree --prefix quartet)" \
    --restore --restore-save auto open '<站点地址>'
  ```

- 登录态失效时在同一 session 使用 `auth login <profile>`。新 profile 由用户通过 `auth save` 的 `--password-stdin` 录入，也可直接通过浏览器登录。密码与会话凭据不得写入命令行、文档、代码或日志。本地自动化可复用 Playwright 的隔离后端与初始化流程。 [E2E 环境](web/e2e/fixtures/e2e-environment.ts)
