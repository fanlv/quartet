# Quartet Web 测试方案：E2E（Playwright）+ 组件层（Vitest）

> ⚠️ 已被取代（2026-06-04）：本方案中"真实链路 + 替换模型输出 + E2E 专用控制/夹具 API"的部分已废弃，相关生产侵入代码（`/api/v1/e2e/*` 路由、`Job.E2EScenario`、`X-QUARTET-E2E-SCENARIO`、回放模型、故障注入钩子等）已全部移除。E2E 现改为直连真实模型、故障链路下沉组件层/Go 单测。最新方案见 `docs/feature-2026-06-04-e2e-real-model.md`。本文档保留作为历史记录，下方内容不再代表当前实现。

> 状态：已完成 ｜ 日期：2026-06-03 ｜ 更新：2026-06-04 ｜ 图例：未执行 / 部分完成 / 已完成
> 当前已完成：组件层基建、纯函数 utils 用例、AuthGate 分支用例、ChatInput 键盘用例、ChatInput 真实浏览器 IME composition 用例、i18n 切换组件用例、鉴权门 Playwright E2E 分支、设置页语言切换 Playwright E2E、Playwright E2E 依赖与基础配置、Vite E2E 启动参数化、E2E 专用启动与产物管理、console / network 摘要、控制 API 调用记录与失败响应体落盘、E2E-only API 关闭态结构化拒绝、E2E 模式固定 Eino Agent / 测试 Model 暴露、E2E 模式屏蔽 ACP 探测与 ACP Job / 消息 / Loop 拒绝入口、E2E 默认设置直写与仓储层测试 Model seed、后端场景绑定（`X-QUARTET-E2E-SCENARIO` 场景枚举 / 模式校验 / Job 持久化 / 后续发送与 Loop 启动校验）、`chat_success` / `tool_success` / `tool_failure` / `replay_failure` / `loop_success` / `loop_failure_midway` 回放与真实 Job UI 级 Playwright 断言、`http_send_failure` 场景绑定与发送失败 UI 断言、E2E-only 测试工具、ACP Loop Job E2E-only 夹具与 Loop 启动拒绝断言、ACP 类型 Session 夹具与 API / UI 历史消息加载断言、侧边栏多 Job 夹具与 Home Job History Rename / Delete / Pin 真实 API 交互断言、历史会话夹具与续问复用原 Job 断言、Job 真实删除 / 取消删除 / 二次确认删除 / 缺失 Job URL 清 stale 与提示、独立 `job_missing` fixture 与缺失 Job URL 场景绑定、稳定 `data-testid` 选择器、E2E 控制 API 与 resume / SSE 错误恢复链路、per-test reset / fixture 重建能力、Makefile 测试入口；组件层 `npm test` 已通过，前端 `npm run build` 已通过，`npm run test:e2e` 已通过，最终 `make test` 已通过。
> 当前部分完成：无。
> 当前剩余：无。
> 范围：`web/` 前端测试基建、Playwright E2E、E2E 所需的 `cmd/web` 测试联调能力

## 1. 背景与目标

前端无测试基建，高风险回归集中在聊天、流式事件、Loop 多轮、消息历史、侧边栏会话操作、语言切换、鉴权门。本方案建两层确定性回归，均不接真实外部模型；探索性手测仍用 `agent-browser`。

已修复一处存量缺陷（步骤 15，用例 5 / 6 前置）：SSE auth（401/403）分支现在会读取响应体并接通 `onError` 到聊天页可见错误区，SSE 鉴权失败已能向用户展示完整错误。

| 维度 | 组件层（Vitest + RTL） | E2E（Playwright，仅 chromium） |
|---|---|---|
| 运行环境 | Node + jsdom | chromium + Vite 前端 + `cmd/web` 后端 |
| 测试对象 | 单组件 / 单函数 | 完整用户链路 |
| 外部依赖 | 请求 / SSE / 存储按用例 mock | 真实后端 API + 事件回放，仅替换模型输出 |
| 入口 | `make test-web` | `make e2e` |

## 2. E2E 运行模型

### 2.1 确定性事件回放

建 Job、发消息、启动 Loop、读 Job / 历史、订阅 SSE 全走真实后端，不在浏览器侧伪造。后端新增事件回放：

- **开关**：环境变量 `QUARTET_E2E`，仅启动夹具设置。未开启时回放 Model、测试工具、固定 Agent 列表、`/api/v1/e2e/*` 一律不可执行（返回结构化完整错误、无副作用），行为同生产。
- **替换点**：只替换"产出模型输出"一环，须在真实 provider 初始化前完成——识别测试 Model 后直接绑定回放 ChatModel，不构建真实模型。Job 创建、事件分发、SSE 推送、归约 / 总结 / 工具包装、历史写盘全部保持真实。
- **覆盖范围**：只覆盖 Eino 线、只暴露测试专用 Eino Model；E2E 模式下服务端显式拒绝 ACP 类型 Job / Session 进入场景绑定、发送、Loop 链路，不靠前端固定列表。
- **场景契约**：建 Job 时用请求头 `X-QUARTET-E2E-SCENARIO` 携带场景名，校验模式后随 Job 持久化到测试 metadata，发送 / 启 Loop 沿用；缺失 / 未知 / 模式不匹配返回完整错误，不回退真实模型。
- **断言对象**：UI 行为、状态流转、固定结构化信号；不比对大段自然语言，但对固定短片段、工具名、参数、结果、错误文本做可见性断言。

### 2.2 固定场景契约

| 场景（枚举） | 状态 | 模式 | 制造方式 | 前置 / 触发 | 功能契约与验收重点 |
|---|---|---|---|---|---|
| 聊天成功 `chat_success` | 已完成 | interactive | 回放 | 新建 Job 后发消息 | 场景枚举、模式校验、Job 绑定与持久化、确定性回放 ChatModel、真实 Job UI 级断言已完成 |
| 工具成功 `tool_success` | 已完成 | interactive | 回放 + 测试工具 | 发触发工具成功的消息 | 场景枚举、模式校验、Job 绑定与持久化、E2E-only 测试工具、工具成功事件序列与 UI 级断言已完成 |
| 工具失败 `tool_failure` | 已完成 | interactive | 回放 + 测试工具 | 发触发工具失败的消息 | 场景枚举、模式校验、Job 绑定与持久化、E2E-only 测试工具、失败事件序列与 UI 级断言已完成 |
| HTTP 发送失败 `http_send_failure` | 已完成 | interactive | 控制 API | 设一次性发送失败后发消息 | `fail-next-message` 控制能力、冲突与消费清理 API 断言、`http_send_failure` 场景绑定校验、UI 级错误展示、用户消息保留、运行态恢复与消费清理断言已完成 |
| 回放运行失败 `replay_failure` | 已完成 | interactive | 回放 | Job 绑定失败场景后发消息 | 场景枚举、模式校验、Job 绑定与持久化、模型错误回放、真实 Job UI 级错误展示与运行态恢复断言已完成 |
| Loop 成功 `loop_success` | 已完成 | loop | 回放 | 新建固定三轮 Loop 并启动 | 场景枚举、模式校验、Job 绑定、Loop 启动校验、三轮回放数据与真实 Job UI 级断言均已完成 |
| Loop 中途失败 `loop_failure_midway` | 已完成 | loop | 回放 | 新建固定三轮 Loop 并启动 | 场景枚举、模式校验、Job 绑定、Loop 启动校验、中途失败回放数据与真实 Job UI 级断言均已完成 |
| resume 恢复成功 `resume_expired_recover_success` | 已完成 | interactive / loop | 控制 API + 真实 `/events` | 预置 / 运行中 Job，令旧 resume 点失效 | `expire-resume` 已能携旧点重连触发真实 410 并消费清理；前端重拉 snapshot 后重连成功、无错误、运行态恢复的 UI 级断言已完成 |
| resume 恢复失败 `resume_expired_recover_failure` | 已完成 | interactive / loop | 控制 API + 真实 `/events` | 令旧点失效，并制造 snapshot 重拉失败 / Job 缺失 / 鉴权失败 | `expire-resume`、`fail-next-snapshot` 与 `disconnect-events` 控制能力已完成；snapshot 重拉失败、Job 缺失、鉴权失败三个子类的 UI 级 Playwright 断言均已完成 |
| 历史会话 `history_session` | 已完成 | interactive | 夹具 | 预置 Job、session、历史消息与恢复点 | 场景枚举、模式校验、Job 绑定与持久化、历史会话 Job 夹具、历史消息 API 与 UI 加载断言、续问复用原 Job Playwright 断言已完成 |
| Job 删除 / 不存在 `job_missing` | 已完成 | interactive / loop | 真实接口 / 夹具 | 真实删除或夹具造缺失 Job | 真实删除、取消删除不变、二次确认移除、URL 指向缺失 Job 时清 stale 并提示、独立 `job_missing` 夹具与关闭态拒绝断言均已完成；resume Job 缺失子类继续通过真实删除制造 |

关键约束：

- **resume 不伪造 410**：后端缓冲在序号 GC 后本就返回 410。控制能力只推进指定 Job 的 GC 水位令旧点失效，浏览器携旧点重连触发真实 410，前端 `onResumePointGone` 重拉 snapshot。
- **测试工具不伪造工具块**：走真实工具包装与事件分发；工具名、入参、成功 / 失败结果、耗时、错误文本由场景固定；不访问外网、不依赖机器状态、不读写可变工作区；仅开关开启时注册。
- **`resume_expired_recover_failure` 三子类**：① snapshot 重拉失败——控制能力 `fail-next-snapshot` 让 410 后的重拉返回一次性错误；② Job 缺失——当前通过真实删除制造，独立 `job_missing` 夹具也已覆盖缺失 URL 场景；③ 鉴权失败——清空 / 替换 token 后重拉携缺失 / 错误 token。三者均要求原始 410 与恢复失败错误同时完整可见（§2.6 格式）。
- **Job 缺失用户侧行为**：清 stale 指清理当前选中 Job、URL 中的 stale `jobId`、该 Job 的消息区 / 运行态 / resume 点与内存状态；页面回到 Home / 空态并展示 Job 不存在提示；sidebar 不再展示该 Job、不再重连其 SSE。真实删除、外部删除、夹具缺失三种制造方式的用户侧断言一致。

### 2.3 启动配置与隔离

不用 `make web`（会杀进程 / 固定端口 / 可能切 443 / 后台运行）。改用专用启动：临时 `LOCAL_MEMORY` 隔离数据目录、专用后端端口并固定 `X_AGENT_AUTH`、专用 Vite 端口；Playwright 管理前后端生命周期，端口占用直接失败并显示完整错误。固定 `workers: 1`，避免一次性控制 / 事件流断开 / resume 水位推进等进程级共享状态互相污染（这些状态无法按 Job 完全隔离，故不并行）。

全局能力由启动夹具负责（固定 Agent 列表、关 ACP 探测、注入测试 Eino Model、写默认设置与 fixture）。以下配置只由 `make e2e` / 启动夹具设置：

| 配置 | 用途 | 约束 |
|---|---|---|
| `QUARTET_E2E=1` | 启用 E2E-only 能力 | 见 §2.1 开关 |
| `X_AGENT_AUTH=<token>` | 强制后端鉴权 | 必须设置；storageState 写入同一 token，不只靠 Playwright 全局请求头 |
| `QUARTET_LISTEN_ADDR=127.0.0.1:<port>` | 专用后端监听地址 | 端口由夹具分配；占用直接失败并展示完整错误 |
| `VITE_E2E_BACKEND_URL` | Vite 代理目标 | 仅 E2E 使用，未传则人工默认代理不变 |
| `VITE_E2E_PORT` | 专用前端端口 | 仅 E2E 使用，强制 HTTP / 禁用证书自动切换 |

> 人工默认基线：`vite.config.ts` 现按本地证书自动切端口与协议（有证书 443+HTTPS，否则 5173+HTTP），代理目标硬编码 `localhost:8090`、不读任何 env。E2E 须由上述变量参数化绕开；未传则默认行为不变。

**`E2E-only API 关闭态` 用例**用独立关闭态后端夹具：不设 `QUARTET_E2E`，独立后端端口与独立临时 `LOCAL_MEMORY`，不写 fixture、不起前端，只用 Playwright API request 调 `/api/v1/e2e/*`，断言结构化完整错误、无副作用、不写控制状态、不影响普通 Job；跑完独立清理该后端进程与数据目录。

### 2.4 E2E 控制 API

运行期能力收敛到 `/api/v1/e2e/*`，仅服务夹具与脚本。统一要求：路由随服务注册但仅开关开启时可执行；鉴权用固定测试 token；按 Job 隔离、一次性控制生效后自动清除；错误响应含 HTTP 状态、错误码 / 文本、目标 Job、控制类型、失败原因并原样展示；handler 只做校验 / 鉴权 / 格式化，状态注入 / 事件流断开 / resume 水位推进 / 发送失败 / snapshot 重拉失败标记下沉服务层。

| 能力 | 状态 | 方法与路径 | 输入 | 成功结果 | 失败边界 |
|---|---|---|---|---|---|
| 设置发送失败 | 已完成 | `POST .../job/:jobId/fail-next-message` | 一次性失败原因 | 下次发送被拒，随后自动清除 | Job 不存在、非 interactive、已有未消费控制 |
| 设置 resume 过期 | 已完成 | `POST .../job/:jobId/expire-resume` | 旧恢复点 / 目标序号 | 后续携旧点连 `/events` 返回 410，随后自动清除 | Job 不存在、已有未消费控制 |
| 设置 snapshot 重拉失败 | 已完成 | `POST .../job/:jobId/fail-next-snapshot` | 一次性失败原因 / 状态码 | 下次 snapshot 重拉被拒，随后自动清除 | Job 不存在、已有未消费控制 |
| 断开事件流 | 已完成 | `POST .../job/:jobId/disconnect-events` | 断开次数、原因 | 指定次数事件流被服务端主动断开 | 无活动流时记为待生效；次数耗尽自动清除 |
| 查询控制状态 | 已完成 | `GET .../job/:jobId/control-state` | 无 | 返回已设置未消费的控制项 | 仅供断言 / 排障，不改状态 |

- **模式校验**：仅"发送失败"绑定单轮发送、需校验 interactive；其余 interactive / loop 通用；所有控制 API 一律校验 Job 存在性。"设置→生效"时序由 `workers: 1` + 按 Job 隔离保证。
- **自身边界验收**：`disconnect-events` 的"待生效 / 次数耗尽自动清除"、各 `fail-next-*` 的"消费后清除"由用例 5 / 6 调用前后用 `control-state` 断言，保证服务层逻辑有回归覆盖。
- **当前状态**：路由注册、关闭态结构化拒绝、开启态 `control-state` 空状态、开启态 Job 存在性校验、目标 Job 不存在结构化 404 均已完成并有 Playwright API 断言；`fail-next-message` 发送失败一次性控制、重复设置冲突、消费后清理已完成并有 Playwright API 断言；`fail-next-snapshot` snapshot 重拉失败一次性控制、重复设置冲突、消费后清理已完成并有 Playwright API 断言；`disconnect-events` 事件流断开控制、重复设置冲突、消费后清理已完成并有 Playwright API 断言；`expire-resume` resume 过期控制、重复设置冲突及其一次性消费清理已完成并有 Playwright API 断言。

### 2.5 鉴权门

后端以 `X_AGENT_AUTH` 开启鉴权；AuthGate 先查健康检查（`/api/v1/health` 的 `authRequired`）判断是否需要 token，再读本地 token 验证。

- 常规用例用固定测试 token，经 storageState 预置前端本地 token——不能只靠 Playwright 全局请求头（AuthGate 先检查本地 token 是否存在）。
- 鉴权门覆盖 无 token / 错误 token / 正确 token；组件层另 mock 接口覆盖 probing / ready / needToken / invalidToken / probeFailed。
- SSE 鉴权失败：先用正确 token 建流，再清空 / 替换本地 token 并用 `disconnect-events` 断开一次；下次 `/events` 重连携缺失 / 错误 token，断言 401/403 后停止无意义重连并展示完整错误（依赖步骤 15）。

### 2.6 错误展示契约

所有错误用例以用户可见错误为验收对象：

- HTTP 错误展示状态码 + 响应体；JSON 体取 `msg` / `error` / `message`，并补充展示 `code` / `reason`，文本体取原始文本。
- SSE 401 / 403 / 410 展示 HTTP 状态 + 服务端正文（auth 分支须先按步骤 15 改造读响应体）。
- resume 410 恢复失败按固定格式同时展示两段，避免后续错误覆盖原始 410：`Resume point expired: <410 状态与正文>; Snapshot reload failed: <恢复失败状态与正文>`。
- 测试运行错误（启动、端口占用、夹具准备、浏览器启动）在命令输出完整展示，不吞 stdout / stderr。

### 2.7 用例级隔离

> 状态：已完成。当前已完成整轮 E2E run 级临时 `LOCAL_MEMORY`、固定 `workers: 1`、每 test 独立浏览器 context、每 test 独立 Job、每 test 前重建默认设置 / 默认 workspace / fixture 基线，并清理未消费控制项。

`workers: 1` 只解决并发污染，不依赖串行顺序共享状态；每个 test 从干净状态开始：

- **浏览器上下文**：每 test 全新 context；常规用例从固定 storageState 注入正确 token，鉴权失败用例用专属 context 允许清空 / 替换 token。
- **夹具数据**：每 test 前经 E2E-only reset 重建默认设置、默认 workspace、agent / model、控制状态与 fixture 基线；侧边栏 Job、历史 Job 等业务 fixture 由用例按需重新创建。
- **Job 隔离**：每 test 独立 Job；Pin / Rename / Delete 等改列表状态的用例用专属 Job 集合，不复用聊天 / Loop / 历史数据。
- **控制能力隔离**：状态按 Job 隔离；每 test 前清未消费控制项；一次性控制消费后清除并可经 `control-state` 断言为空。
- **语言与设置隔离**：断言不得依赖上一用例留下的语言或配置，由下次 fixture 重建覆盖。

### 2.8 夹具数据

> 状态：已完成。启动级隔离数据、默认 workspace、固定测试 Agent / Model、默认设置、ACP 拒绝夹具、ACP 类型 Session 夹具、侧边栏多 Job 夹具、历史会话夹具、失败数据、独立 `job_missing` fixture、per-test reset / fixture 重建均已完成。

E2E 不依赖开发者机器上的模型配置、ACP 安装状态或历史数据；启动夹具经仓储 / E2E-only 服务写入隔离数据目录：

- **[已完成] 工作区**：确认默认工作区存在且可写；当前启动夹具 / workspace 服务已保证隔离 `LOCAL_MEMORY`、默认 workspace 与每测试独立 Job。
- **[已完成] 测试 Agent / Model 固定暴露**：固定 Eino Agent / 测试 Model 暴露已完成，测试 Model 已在 E2E 模式下绑定确定性回放 ChatModel，不触发真实 provider 调用。
- **[已完成] 默认设置直写与仓储层测试 model seed**：E2E 启动时已写入默认设置、Title / Message Agent 与固定测试 Model，并通过模型列表与 settings API 断言。
- **[已完成] 屏蔽 ACP 探测**：E2E 模式下 `agent/list` 只返回固定 Eino Agent / 测试 Model，不触发真实 ACP 同步探测与后台异步缓存刷新。
- **[已完成] 侧边栏列表**：侧边栏多 Job E2E-only 夹具、API 排序断言、UI 渲染与选中态断言已完成；Home Job History Rename / Delete / Pin 真实 API 交互断言、Rename 空标题保持原标题边界、Pin 后置顶排序与取消置顶持久化均已完成。
- **[已完成] 历史会话**：场景枚举、模式校验、Job 绑定与持久化、预置 Job / session / 历史消息夹具、历史消息 API 与 UI 加载断言、续问复用原 Job Playwright 断言已完成。
- **[已完成] ACP 拒绝夹具**：ACP Job 创建拒绝、ACP 消息发送拒绝、ACP Loop Job 夹具直写与 Loop 启动拒绝已完成。
- **[已完成] ACP 类型 Session 夹具**：ACP 类型 Session 夹具已落地，支持直接写入 ACP 类型 Session 元数据、ACP mode 与历史消息，并通过 API / UI 断言验证。
- **[已完成] 失败数据已落地项**：HTTP 发送失败、snapshot 重拉失败、事件流断开、resume 过期已可由控制 API 制造；Job 删除 / 不存在的真实删除、取消删除、二次确认移除、独立 `job_missing` 夹具、缺失 Job URL 清 stale 与提示已完成。
- **[已完成] 交互失败场景初始数据**：HTTP 发送失败、snapshot 重拉失败、事件流断开、resume 过期已可由控制 API 制造，历史会话 Job / session / 历史消息夹具与续问复用原 Job 断言、回放运行失败模型错误回放与 UI 级断言已完成。
- **[已完成] Loop 场景初始数据**：Loop 场景枚举、模式校验、Job 绑定与 Loop 启动入口校验已完成；Loop 成功三步回放数据与 UI 级断言已完成，Loop 中途失败场景夹具、回放数据与 UI 级断言已完成。

### 2.9 SSE 断言

不用固定 sleep，统一等可观察状态（`expect.poll` / locator expectation / `waitForFunction`）：消息节点出现、assistant 增量拼接完成、工具块进终态、运行态 loading→idle、Loop 进度达预期、错误完整展示。

### 2.10 失败产物与清理

> 状态：已完成。已完成临时 `LOCAL_MEMORY`、前后端 stdout / stderr、Playwright trace / 截图 / 录屏、失败保留与成功清理、console / network 摘要、控制 API 调用记录与失败响应体落盘；最近一次 `npm run test:e2e` 28 个用例通过，`npm run build` 通过。

成功自动清理临时进程与数据目录；失败默认保留排障产物并在命令输出打印路径，含临时 `LOCAL_MEMORY` 数据目录、前后端 / Playwright 的 stdout / stderr、Playwright trace / 截图 / 录屏 / 失败页 HTML / console / network 摘要、控制 API 调用记录与失败响应体。产物放固定 run 目录、按时间 + 随机后缀区分，可经清理参数删历史。成功清理与失败保留都须先停前后端进程。

## 3. 测试范围

### 3.1 E2E 用例（Playwright）

> 状态：已完成。Playwright 依赖、基础配置、E2E 启动夹具、鉴权门、聊天快捷键真实浏览器 IME composition、设置页语言切换与关闭态用例、聊天 / 工具 / 回放失败 / Loop 成功 / Loop 中途失败 / 发送失败 / SSE 鉴权失败 / resume 恢复成功与失败 / 历史会话 / 侧边栏 Job 操作 / ACP 拒绝 / E2E-only API 关闭态、Job 删除 / 不存在、用例级隔离等 UI 与 API 断言均已完成。

1. **[已完成] 鉴权门**：组件层已覆盖无鉴权、无 token、错误 token、探测失败重试、正确 token 进入应用；Playwright 已覆盖固定正确 token 通过 AuthGate 并进入 Home、无 token 展示鉴权门并输入正确 token 恢复、错误 token 被拒后输入正确 token 恢复、健康探测失败展示错误并重试恢复。
2. **[已完成] 聊天主链路**：输入框 / 消息 / 工具块稳定选择器、建 Job 场景绑定、发送入口校验、发送失败 UI 回归、`chat_success`、工具成功 / 失败与回放运行失败确定性回放、真实 Job UI 级断言均已完成。
3. **[已完成] 聊天快捷键**：组件层已覆盖 Enter 发送、Cmd/Ctrl+Enter / Shift+Enter 换行、IME 期间 Enter 不发送、命令补全导航、历史导航；Playwright E2E 已覆盖真实浏览器 composition 期间 Enter 不发送，composition 结束后 Enter 恢复发送触发。
4. **[已完成] 发送失败**：`fail-next-message` HTTP 拒绝控制能力与 API 断言已完成，覆盖错误完整、重复设置冲突、一次性消费后清理且不进入真实发送；UI 级用例已绑定 `http_send_failure` 场景，并覆盖错误展示、用户消息保留、运行态恢复与消费清理断言。
5. **[已完成] SSE 鉴权失败**：前端已支持 401/403 停止无意义重连并展示真实响应体错误；`disconnect-events` 控制能力与 API 级断言已完成，清 token 后触发断开并断言 401/403 的 UI 级 Playwright 用例已完成。
6. **[已完成] SSE resume 恢复**：前端已具备 410 后重拉 snapshot、恢复失败时同时展示原始 410 与恢复失败错误；`expire-resume` resume 过期控制与 `fail-next-snapshot` snapshot 失败控制的 API 级断言已完成，resume 恢复成功与 snapshot 重拉失败 / Job 缺失 / 鉴权失败三个恢复失败子类的 UI 级 Playwright 用例均已完成。
7. **[已完成] 消息历史**：`history_session` 场景枚举、模式校验、Job 绑定与持久化、预置历史会话夹具、历史消息 API 与 UI 加载断言、续问复用原 Job Playwright 断言已完成。
8. **[已完成] Job 删除 / 不存在**：Home Job History 删除取消不变、二次确认后移除、后端真实删除、独立 `job_missing` 夹具、URL 指向已删除 / 不存在 Job 时清 stale 并提示均已完成。
9. **[已完成] Loop 链路**：Loop 场景枚举、模式校验、Job 绑定、Loop 启动入口校验、Loop 进度 / session 选择器与 ACP Loop 拒绝断言已完成；Loop 成功三步回放数据、启动 UI 脚本、轮次递增、session 列表与终态 UI 断言已完成；Loop 中途失败回放数据、失败状态、错误展示、结果列表、session 列表、resume 指针与 Job 明细断言已完成。
10. **[已完成] 侧边栏 Job 操作**：侧边栏 / Home Job History 多 Job 夹具、列表渲染、排序、选中 / 跳转断言已完成；Home Job History Rename 持久化、Rename 空标题保持原标题、Delete 取消后不变、Delete 二次确认后移除、Pin 后置顶排序与取消置顶持久化均已完成。
11. **[已完成] 设置页语言切换**：组件层已覆盖 GeneralSettings 语言切换、文案更新、`document.lang` 与语言缓存更新；Playwright E2E 已覆盖真实浏览器打开设置、切换语言、校验设置文案、`document.lang` 与语言缓存更新。
12. **[已完成] E2E 模式 ACP 拒绝**：E2E 模式下固定 Agent 列表已屏蔽真实 ACP 探测；已覆盖 ACP Job 创建拒绝、ACP 消息发送拒绝、ACP Loop Job E2E-only 夹具与 Loop 启动拒绝 Playwright 断言；关闭态也已覆盖 fixture API 结构化拒绝。ACP 类型 Session 夹具已按步骤 7 的夹具扩展完成。
13. **[已完成] E2E-only API 关闭态**：已完成未设 `QUARTET_E2E` 的独立关闭态后端夹具、健康检查冒烟，并断言 `/api/v1/e2e/*` 返回结构化完整错误、无副作用、不写控制状态、不影响普通 Job；ACP 类型 Session 夹具已归入步骤 7 的夹具扩展完成。

覆盖对照（验收时核对全覆盖；数字指 §3.1 用例 / §5 步骤）：

| 场景 / 边界 | 状态 | 用例 | 步骤 |
|---|---|---|---|
| 聊天成功 | 已完成 | 2 | 10、13.1 |
| 工具成功 / 失败 | 已完成 | 2 | 11、13.2、13.3 |
| HTTP 发送失败 | 已完成 | 4 | 12、13.4 |
| 回放运行失败 | 已完成 | 2 | 10、13.5 |
| Loop 成功 | 已完成 | 9 | 10、13.6 |
| Loop 中途失败 | 已完成 | 9 | 10、13.7 |
| resume 恢复成功 | 已完成 | 6 | 12、13.8 |
| resume 恢复失败 | 已完成 | 6 | 12、13.9 |
| 历史会话 | 已完成 | 7 | 7、13.10 |
| Job 删除 / 不存在 | 已完成 | 8、10 | 7、13.11、13.12 |
| E2E 模式 ACP 拒绝 | 已完成 | 12 | 9、13.13 |
| E2E-only API 关闭态 | 已完成 | 13 | 8、12、13.14 |
| 鉴权门 / 快捷键 / SSE 鉴权失败 / 语言切换 | 已完成 | 1、3、5、11 | 3（组件层）、15（用例 5）、16（鉴权门、快捷键真实 composition 与语言切换 E2E）、§2.5 / §2.6 |

### 3.2 组件层用例（Vitest + RTL）

> 状态：已完成（组件层基建、纯函数 utils、AuthGate 分支、ChatInput 键盘用例、i18n 切换用例均已完成）。

- **[已完成] ChatInput 键盘**：Enter 发送、Cmd/Ctrl+Enter / Shift+Enter 换行、IME 忽略 Enter、命令补全导航、历史导航。
- **[已完成] AuthGate 分支**：mock 健康检查与受保护接口，断言 probing / ready / needToken / invalidToken / probeFailed。
- **[已完成] i18n 切换**：切换语言后断言文案变化，并校验 `document.lang` 与语言缓存更新。
- **[已完成] 纯函数 utils**：`time.ts`、`commands.ts`、`statsFormat.ts`、`url.ts`、`workspace.ts`、`mergeMessages.ts`。

## 4. 稳定选择器

> 状态：对应步骤 14（已完成）；当前前端已补首批 `data-testid` 与重复实体稳定业务标识 / 状态属性，并已补齐 Loop 启动入口、Home 空态 / 加载 / 离线状态、全局连接状态等剩余选择器。后续如新增业务场景脚本出现新断言，可继续按需小步补充。

前端缺稳定选择器，需补 `data-testid`，只加测试需要的节点，命名 `区域-元素`（如 `chat-input`、`message-item`、`sidebar-job-item`、`loop-progress`）。重复实体（Job、message、tool call、session、Loop 轮次）须暴露稳定业务标识或状态属性（各类 ID、role、Loop path、运行状态），优先用这些定位而非 `nth()`。最小集状态：

- **[已完成] 鉴权门**：token 输入框、提交按钮、错误提示、重试按钮。
- **[已完成] 聊天输入 / 消息**：输入框、发送按钮、停止按钮、队列、消息列表、单条消息、user / assistant / tool / system 消息、耗时相关节点。
- **[已完成] 工具调用**：调用块、工具名、参数区、结果区、成功 / 失败 / 运行中状态。
- **[已完成] Job 导航**：侧边栏、Job item、选中态、Rename 输入框、Delete 按钮与删除确认弹窗、Home Job History Pin 按钮与置顶状态属性已完成。
- **[已完成] Loop**：Loop 进度区、结果项、session sidebar、session item、终态状态、Loop 配置入口、配置面板、workspace 选择、启动按钮已完成。
- **[已完成] 设置 / 状态**：设置入口、弹窗、语言控件、保存 / 关闭按钮、Job 错误提示、Job 不存在专用提示、全局连接状态、Home 空态 / 加载 / 离线状态已完成。

## 5. 工程接入与落地步骤

```
web/
  src/**/*.test.ts(x)
  e2e/{tests,fixtures}/  playwright.config.ts
  vitest.config.ts  vitest.setup.ts
```

- **[已完成] 依赖状态**：组件层 `vitest`、`@testing-library/{react,user-event,jest-dom}`、`jsdom` 已完成；E2E `@playwright/test`、基础配置与本机 chromium 安装已完成。
- **[已完成] npm scripts 状态**：组件层 `test`、`test:watch` 已完成，并已通过 `npm test` 验证（9 个文件 / 33 个用例通过）；E2E `test:e2e`、`test:e2e:ui` 已完成，启动夹具 / 鉴权门 / E2E Agent 冒烟 / E2E 默认设置与测试 Model seed / ACP 拒绝 / ACP 类型 Session 夹具 / 侧边栏多 Job 夹具 / Home Job History Rename/Delete/Pin / 历史会话加载与续问复用原 Job / 独立 `job_missing` fixture 与缺失 Job URL 清 stale 提示 / 开启态 `control-state` 断言 / 开启态 `fail-next-message` 发送失败控制断言 / `http_send_failure` HTTP 发送失败 UI 级断言 / `chat_success` UI 级断言 / 工具成功 / 失败 UI 级断言 / `replay_failure` UI 级断言 / `loop_success` UI 级断言 / `loop_failure_midway` UI 级断言 / SSE 鉴权失败 UI 级断言 / resume 成功恢复 UI 级断言 / resume 410 + snapshot 重拉失败 / Job 缺失 / 鉴权失败 UI 级断言 / 开启态 `fail-next-snapshot` snapshot 重拉失败控制断言 / 开启态 `disconnect-events` 事件流断开控制断言 / 开启态 `expire-resume` resume 过期控制断言 / 目标 Job 不存在 404 / 聊天快捷键真实 composition / 设置页语言切换 / 关闭态用例已覆盖；最近一次完整 `npm run test:e2e` 通过 28 个用例，独立 `job_missing` fixture、`http_send_failure`、`loop_success` 与 `loop_failure_midway` 定向 Playwright 用例已通过。
- **[已完成] Makefile 入口接入**：`make test-web`、`make e2e`、`make test` 已接入；其中 `make test-web` 已通过。`make e2e` 入口复用当前 Playwright 套件，可覆盖启动夹具 / 鉴权门 / E2E Agent 冒烟 / E2E 默认设置与测试 Model seed / ACP 拒绝 / ACP 类型 Session 夹具 / 侧边栏多 Job 夹具 / Home Job History Rename/Delete/Pin / 历史会话加载与续问复用原 Job / 开启态控制 API / `http_send_failure` HTTP 发送失败 UI / `chat_success` UI / 工具成功 / 失败 UI / `replay_failure` UI / `loop_success` UI / `loop_failure_midway` UI / SSE 鉴权失败 UI / resume 成功恢复 UI / resume 410 + snapshot 重拉失败 / Job 缺失 / 鉴权失败 UI / 独立 `job_missing` fixture 与缺失 Job URL 清 stale 提示 / 目标 Job 不存在 404 / 聊天快捷键真实 composition / 设置页语言切换 / 关闭态用例；`make test` 已通过。
- **步骤依赖**（数字指本节步骤，引用用例显式写"用例 N"）：组件层 1→2→3 已完成；E2E 基建 4→6 已完成；后端确定性能力 8→9→10→12 已完成，步骤 13 已完成；步骤 7 已完成；步骤 11 / 14 / 15 / 16 / 17 已完成；`chat_success`、`http_send_failure`、工具成功 / 失败、`replay_failure`、`loop_success`、`loop_failure_midway`、`job_missing` 均已有回放、控制或夹具能力与 UI 脚本覆盖；当前 `npm run test:e2e`、`npm run build` 已通过，既有 `make e2e` 与 `make test` 记录已通过，步骤 18 的测试执行已完成。

1. **[已完成]** 安装组件层依赖，新增 Vitest 配置、setup、npm scripts。
2. **[已完成]** 组件层夹具：fetch mock、localStorage / i18n reset、必要 SSE / 存储 mock。
3. **[已完成]** 组件层用例：纯函数 utils、AuthGate 分支、ChatInput 键盘、i18n 切换已完成。
4. **[已完成]** 安装 Playwright，新增 E2E 配置与 npm scripts：`@playwright/test` 已加入 web devDependencies；新增 `web/e2e/playwright.config.ts`，固定 chromium、`workers: 1`、失败 trace / 截图 / 录屏产物目录；新增 `test:e2e` / `test:e2e:ui` scripts。
5. **[已完成]** 参数化 Vite E2E 启动：`vite.config.ts` 已支持读取 `VITE_E2E_BACKEND_URL` 与 `VITE_E2E_PORT`；传入 E2E env 时使用专用代理目标 / 前端端口并禁用 HTTPS 证书自动切换，未传 env 时默认行为不变。
6. **[已完成]** E2E 专用启动与产物管理：已新增 Playwright global setup，支持临时 `LOCAL_MEMORY`、固定 token、专用前后端端口、独立关闭态后端夹具、关闭态后端动态避让占用端口、进程清理、成功清理、失败保留 trace / 截图 / 录屏 / 日志 / 数据目录并打印路径；已补 console / network 摘要、控制 API 调用记录与失败响应体落盘；已补启动冒烟 / 鉴权门 / 聊天快捷键真实 composition / 设置页语言切换 / 关闭态夹具用例，并通过 `npm run test:e2e` 验证。
7. **[已完成]** E2E 夹具数据（仓储 / E2E-only 服务写入）：主体夹具、独立 `job_missing` fixture、per-test reset / fixture 重建均已完成。
   - **[已完成]** 隔离 `LOCAL_MEMORY` 与默认 workspace，由启动夹具 / workspace 服务保证。
   - **[已完成]** 测试 Agent / Model 固定暴露，避免真实 provider 调用。
   - **[已完成]** 默认设置直写与仓储层测试 model seed：E2E 启动时写入默认 settings、Title / Message Agent 与固定测试 Model，并补 Playwright API 断言。
   - **[已完成]** ACP Loop Job 夹具直写，并用于 Loop 启动拒绝断言。
   - **[已完成]** 失败数据已落地项：HTTP 发送失败、snapshot 重拉失败、事件流断开、resume 过期已由控制 API 覆盖；Job 删除 / 不存在的真实删除、取消删除、二次确认移除、独立 `job_missing` fixture、缺失 Job URL 清 stale 与提示已由 UI 用例覆盖。
   - **[已完成]** 交互失败场景初始数据：HTTP 发送失败、snapshot 重拉失败、事件流断开、resume 过期已由控制 API 覆盖，历史会话 Job / session / 历史消息夹具与续问复用原 Job 断言、回放运行失败模型错误回放与 UI 级断言已完成。
   - **[已完成]** Loop 场景初始数据：Loop 场景枚举、模式校验、Job 绑定与 Loop 启动入口校验已完成；Loop 成功三步回放数据与 UI 级断言已完成，Loop 中途失败场景夹具、回放数据与 UI 级断言已完成。
   - **[已完成]** 侧边栏多 Job 夹具：已覆盖 API 排序、Home Job History UI 渲染与跳转断言、Rename 持久化、Rename 空标题保持原标题、Delete 取消不变与二次确认移除、Pin 后置顶排序与取消置顶持久化。
   - **[已完成]** ACP 类型 Session 夹具：已覆盖 ACP 类型 Session 元数据、ACP mode、历史消息写入、消息 API 返回与 UI 历史消息加载断言。
8. **[已完成]** E2E 开关与场景绑定：
   - **[已完成]** E2E 开关与控制 API 开启 / 关闭态：启动夹具已设置 `QUARTET_E2E=1`，关闭态后端夹具已显式不设置该开关；关闭时 `/api/v1/e2e/*` 控制能力已返回结构化完整错误且不可执行；开启态 `control-state` 空状态查询、控制 action 的 Job 存在性校验、目标 Job 不存在结构化 404、`fail-next-message`、`fail-next-snapshot`、`disconnect-events` 与 `expire-resume` 真实控制已完成。
   - **[已完成]** 后端场景绑定：`X-QUARTET-E2E-SCENARIO` 请求头、场景枚举与模式匹配校验、随 Job 持久化、后续消息发送与 Loop 启动场景校验均已完成，并补充 Playwright API 级断言。
9. **[已完成]** 测试 Agent 能力：E2E 模式下返回固定 Eino Agent 列表并暴露测试 Model；E2E 启动时已写入默认 settings 与仓储层测试 Model；Agent 列表接口不再触发真实 ACP 同步探测与后台异步缓存刷新；E2E 测试 Model 现在绑定确定性回放模型，不再触发真实 provider 调用；ACP 类型 Job 创建、消息发送与 Loop 启动入口会被服务端拒绝，不进入真实 ACP 链路，且上述拒绝路径已有 Playwright 断言。
10. **[已完成]** 模型回放能力：
    - **[已完成]** 交互场景回放：场景枚举、模式匹配校验、Job 绑定持久化、发送消息时沿用场景契约、测试 Model 在真实 provider 初始化前绑定回放 ChatModel、`chat_success`、工具成功 / 失败固定事件序列与 `replay_failure` 模型错误回放均已完成。
    - **[已完成]** Loop 场景回放：Loop 启动时沿用场景契约已完成；`loop_success` 三步固定事件序列已完成，`loop_failure_midway` 中途失败固定事件序列已完成。
11. **[已完成]** E2E-only 测试工具：已落地固定工具名 / 入参 / 成功 / 失败结果，仅 `QUARTET_E2E` 开启且场景为 `tool_success` / `tool_failure` 时注册到真实 Eino `ToolsConfig`；失败返回普通可恢复错误，由现有工具包装链路标记失败。
12. **[已完成]** E2E 控制 API 与服务层封装：
    - **[已完成]** 控制 API 路由、统一服务入口、关闭态结构化拒绝、控制状态查询的关闭态响应与开启态空状态查询。
    - **[已完成]** 开启态 Job 存在性校验、目标 Job 不存在结构化 404，并已补 Playwright API 断言。
    - **[已完成]** `fail-next-message` 发送失败控制、重复设置冲突、一次性消费后清理，并已补 Playwright API 断言。
    - **[已完成]** `fail-next-snapshot` snapshot 重拉失败控制、重复设置冲突、一次性消费后清理，并已补 Playwright API 断言。
    - **[已完成]** `disconnect-events` 事件流断开控制、重复设置冲突及其一次性消费清理，并已补 Playwright API 断言。
    - **[已完成]** `expire-resume` resume 过期控制、重复设置冲突及其一次性消费清理，并已补 Playwright API 断言。
13. **[已完成]** 各场景后端回放数据与控制能力（依赖 8~12，含内置事件序列与契约校验）：E2E-only API 关闭态的独立关闭态后端夹具、健康检查冒烟、结构化拒绝与普通 Job 不受影响断言已完成；各场景枚举、模式匹配、Job 绑定持久化与后续入口校验已完成；`chat_success`、`http_send_failure`、工具成功 / 失败回放数据、`replay_failure` 模型错误回放、`loop_success` 三步 Loop 回放、`loop_failure_midway` 中途失败回放、独立 `job_missing` fixture 均已完成。
    - 13.1 **[已完成]** 聊天成功（场景枚举 / 模式校验 / Job 绑定持久化、确定性回放 ChatModel、聊天成功回放事件序列与真实 Job UI 级断言已完成）
    - 13.2 **[已完成]** 工具成功（场景枚举 / 模式校验 / Job 绑定持久化 / E2E-only 测试工具 / 工具成功事件序列与真实 Job UI 级断言已完成）
    - 13.3 **[已完成]** 工具失败（场景枚举 / 模式校验 / Job 绑定持久化 / E2E-only 测试工具 / 工具失败事件序列与真实 Job UI 级断言已完成）
    - 13.4 **[已完成]** HTTP 发送失败（`fail-next-message` 控制能力、API 级断言、`http_send_failure` 场景绑定、UI 级错误展示、用户消息保留、运行态恢复与消费清理断言已完成）
    - 13.5 **[已完成]** 回放运行失败（场景枚举 / 模式校验 / Job 绑定持久化 / 模型错误回放、真实 Job UI 级错误展示与运行态恢复断言已完成）
    - 13.6 **[已完成]** Loop 成功（场景枚举 / 模式校验 / Job 绑定持久化 / Loop 启动入口校验 / 三轮 Loop 回放数据 / 真实 Job UI 级断言均已完成）
    - 13.7 **[已完成]** Loop 中途失败（场景枚举 / 模式校验 / Job 绑定持久化 / Loop 启动入口校验 / 中途失败回放数据 / 真实 Job UI 级断言均已完成）
    - 13.8 **[已完成]** resume 恢复成功（`expire-resume` 已可制造真实 410；已覆盖前端重拉 snapshot、重新连接事件流、无错误展示、运行态恢复与控制状态清理的 UI 级 Playwright 断言）
    - 13.9 **[已完成]** resume 恢复失败（`expire-resume` 已可制造真实 410，`fail-next-snapshot` 已可制造 snapshot 重拉失败，`disconnect-events` 已可制造事件流断开；snapshot 重拉失败、Job 缺失、鉴权失败三个子类的 UI 级 Playwright 断言均已完成）
    - 13.10 **[已完成]** 历史会话（场景枚举 / 模式校验 / Job 绑定持久化、历史会话 Job 夹具、历史消息 API 与 UI 加载断言、续问复用原 Job Playwright 断言已完成）
    - 13.11 **[已完成]** Job 删除 / 不存在（真实删除、取消删除不变、二次确认移除已由 Home Job History E2E 覆盖；独立 `job_missing` fixture、关闭态拒绝断言、URL 缺失 Job 清 stale 与提示均已完成）
    - 13.12 **[已完成]** 侧边栏多 Job（E2E-only 夹具、API 排序断言、Home Job History UI 渲染与跳转断言、Rename 持久化、Rename 空标题保持原标题、Delete 取消不变与二次确认移除、Pin 后置顶排序与取消置顶持久化已完成）
    - 13.13 **[已完成]** E2E 模式 ACP 拒绝（固定 Agent / 测试 Model、ACP 探测屏蔽、ACP 创建 / 发送入口拒绝、ACP Loop Job 夹具直写与 Loop 启动拒绝 Playwright 断言均已完成；ACP 类型 Session 夹具已按步骤 7 扩展完成）
    - 13.14 **[已完成]** E2E-only API 关闭态（独立关闭态后端夹具、健康检查冒烟、`/api/v1/e2e/*` 结构化拒绝与普通 Job 不受影响断言已完成）
14. **[已完成]** 补稳定选择器：已覆盖鉴权门、聊天输入 / 消息、工具调用、Job 导航、Home Job History Pin、Home Job History、Loop 进度 / session、Loop 配置入口 / 配置面板 / 启动按钮、Home 设置入口、设置弹窗 / 语言控件、全局错误提示、全局连接状态、Home 空态 / 加载 / 离线状态等 `data-testid`；重复实体已暴露 `data-job-id`、`data-message-id`、`data-session-id`、`data-tool-status`、`data-loop-status`、`data-pinned`、`data-home-state`、`data-connection-state` 等稳定业务标识 / 状态属性。后续随步骤 16 新增业务场景脚本如有新断言，可继续按需小步回补。
15. **[已完成]** 改造前端错误展示链路（修复存量缺陷，用例 5 / 6 前置）：SSE 401/403 会读取响应体正文并停止无意义重连；`onError` 已接通聊天页可见错误区；resume 410 恢复失败按固定格式同时展示原始 410 与 snapshot 重拉失败、Job 缺失、鉴权失败等恢复错误。
16. **[已完成]** 编写 E2E 脚本（依赖 4~15）：已新增启动夹具 / 关闭态脚本，覆盖鉴权门无 token / 错误 token / 正确 token / 探测失败重试分支、聊天快捷键真实浏览器 IME composition 分支、真实浏览器设置页语言切换、独立关闭态后端健康检查、`/api/v1/e2e/*` 关闭态结构化拒绝与普通 Job 不受影响、E2E 模式固定 Eino Agent / 测试 Model 暴露、E2E 默认设置与测试 Model seed、侧边栏多 Job 夹具的 API 排序 / Home Job History UI 渲染 / 跳转断言 / Rename 持久化 / Rename 空标题保持原标题 / Delete 取消不变 / Delete 二次确认移除 / Pin 后置顶排序与取消置顶持久化、历史会话夹具的 API 与 UI 历史消息加载断言、ACP 类型 Session 夹具的 API 与 UI 历史消息加载断言、历史会话续问复用原 Job 断言、独立 `job_missing` fixture、缺失 Job URL 清 stale 与提示、开启态 `control-state` 空状态查询、开启态 `fail-next-message` 发送失败控制、HTTP 发送失败 UI 级错误展示与运行态断言、`chat_success` 真实 Job UI 级断言、工具成功 / 失败真实 Job UI 级断言、`replay_failure` 真实 Job UI 级错误展示与运行态恢复断言、`loop_success` 三步 Loop 回放 / 轮次递增 / session 列表 / 终态真实 Job UI 级断言、`loop_failure_midway` 中途失败状态 / 错误展示 / 结果列表 / session 列表 / resume 指针 / Job 明细真实 UI 级断言、清 token 后触发 `disconnect-events` 并展示 SSE 鉴权失败的 UI 级断言、resume 过期后重拉 snapshot 并恢复事件流的成功恢复 UI 级断言、resume 410 + snapshot 重拉失败 / Job 缺失 / 鉴权失败三个恢复失败子类 UI 级错误展示、开启态 `fail-next-snapshot` snapshot 重拉失败控制、开启态 `disconnect-events` 事件流断开控制、开启态 `expire-resume` resume 过期控制、目标 Job 不存在的结构化 404、ACP 创建 / 发送入口拒绝、ACP Loop 启动拒绝以及 E2E fixture 关闭态拒绝断言。
17. **[已完成]** 接入 Makefile：`make test-web`、`make e2e`、`make test`。
18. **[已完成]** 跑 `make test-web` / `make e2e` / `make test` 按完整错误修复。`make test-web` 已通过；前端 `npm test` 与 `npm run build` 已通过；`npm run test:e2e` 已通过当前启动 / 鉴权门 / E2E Agent 冒烟 / E2E 默认设置与测试 Model seed / ACP 拒绝 / ACP 类型 Session 夹具 / 侧边栏多 Job 夹具 / Home Job History Rename/Delete/Pin / 历史会话加载与续问复用原 Job / 开启态 `control-state` 断言 / 开启态 `fail-next-message` 发送失败控制 / `http_send_failure` HTTP 发送失败 UI 级断言 / `chat_success` UI 级断言 / 工具成功 / 失败 UI 级断言 / `loop_success` UI 级断言 / `loop_failure_midway` UI 级断言 / SSE 鉴权失败 UI 级断言 / resume 成功恢复 UI 级断言 / resume 410 + snapshot 重拉失败 / Job 缺失 / 鉴权失败 UI 级断言 / 开启态 `fail-next-snapshot` snapshot 重拉失败控制 / 开启态 `disconnect-events` 事件流断开控制 / 开启态 `expire-resume` resume 过期控制 / 目标 Job 不存在 404 / 聊天快捷键真实 composition / 设置页语言切换 / 关闭态用例（28 个用例）；`chat_success` UI 定向 Playwright 用例、工具成功 / 失败 UI 定向 Playwright 用例、`replay_failure` UI 定向 Playwright 用例、`loop_success` UI 定向 Playwright 用例、`loop_failure_midway` UI 定向 Playwright 用例、`http_send_failure` UI 定向 Playwright 用例、Rename 空标题保持原标题、缺失 Job URL 清 stale 与提示、历史会话夹具与续问复用原 Job、ACP 类型 Session 夹具、关闭态 fixture 拒绝的定向 Playwright 用例与前端 build 已通过；E2E 默认设置与测试 Model seed 的定向 Playwright 用例、相关 Go package test 与前端 build 已通过；resume 成功恢复 UI 与 resume snapshot 失败 / Job 缺失 / 鉴权失败 UI 定向 Playwright 用例已通过；E2E-only 测试工具后端定向 Go 用例已通过；独立 `job_missing` fixture 与关闭态拒绝定向 Playwright 用例已通过；per-test reset / fixture 重建已接入并通过完整 `npm run test:e2e` 28 个用例验证；最终 `make test` 已通过。
