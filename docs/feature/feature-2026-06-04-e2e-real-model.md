# Quartet Web E2E 改造方案：移除 E2E 专用代码，改用真实模型流程

> 状态：已执行（待真实凭证环境验证 `make e2e`） ｜ 日期：2026-06-04 ｜ 图例：未执行 / 进行中 / 已完成
> 范围：移除生产代码里所有 E2E 专用侵入（路由 / 字段 / 分支 / 回放 / 故障注入），E2E 改为直连真实模型；故障链路下沉到组件层与 Go 单测。

## 1. 背景与动机

当前 E2E（见 `docs/feature-2026-06-03-web-e2e.md`）采用「真实链路 + 替换模型输出」模型：建 Job、发消息、SSE、Loop、历史全走真实后端，只把「产出模型输出」一环换成确定性回放，并为故障链路（发送失败 / SSE 断开 / resume 过期 / snapshot 重拉失败）在服务层内置一次性故障注入。

代价是生产代码被 E2E 专用逻辑大量侵入：

- 生产二进制里**常驻注册** 11 个 `/api/v1/e2e/*` 路由。
- 生产数据模型混入 `Job.E2EScenario`、请求里混入 `X-QUARTET-E2E-SCENARIO`。
- 27 处运行期 `if QUARTET_E2E` 分支散落在 Handler / Service / Model / Agent 各层，包括替换 ChatModel、固定 Agent 列表、场景持久化、事件缓冲 GC 暂停钩子等。
- 这些钩子一旦判断写错，即是生产环境的真实故障注入后门。

**目标**：生产代码里不再保留任何 E2E 专用代码；E2E 直连真实模型跑真实主链路；不靠真实模型才能触发的故障链路，下沉到组件层 / Go 单测覆盖。

## 2. 决策（已确认）

| 决策点 | 选择 | 含义 |
|---|---|---|
| 模型输出来源 | **直连真实 API** | E2E 每次真打真实 provider（Claude / ARK 等），不再有回放模型 / 测试 Model / 固定 Agent 列表。E2E 为本地手动 / 带 key 的环境执行，不要求在无 key 的 CI 中确定性通过。 |
| 故障注入能力 | **下沉组件层 / Go 单测** | 发送失败、SSE 断开、resume 410 过期、snapshot 重拉失败等真实模型无法触发的链路，从 E2E 移到 Vitest 组件层（mock fetch / SSE）与 Go 单测（直接驱动事件缓冲），生产代码删除全部故障注入钩子。 |
| 场景 / 夹具侵入 | **全删** | 删除 `services/e2e/`、`cmd/web/handler/e2e.go`、`Job.E2EScenario`、`X-QUARTET-E2E-SCENARIO` 请求头、所有 `fixture/*` 接口与场景枚举。E2E 需要的数据改用真实业务接口创建。 |

## 3. 改造后的测试形态

### 3.1 E2E（Playwright，真实模型）

- 启动仍用专用隔离环境：临时 `LOCAL_MEMORY`、专用前后端端口、初始化测试管理员并注入 Cookie/CSRF、`workers: 1`、失败保留产物。这部分与 E2E 专用代码无关，保留。
- 不再设置 `QUARTET_E2E`；后端按生产模式运行，使用真实 provider 配置。
- E2E 运行依赖真实模型凭证（如 API key / base url），由环境变量提供；缺失凭证时 E2E 直接失败并完整展示缺失原因，不静默跳过、不回退假数据。
- 测试数据全部经真实业务接口创建：建 Job 走 `/job/create`，发消息走 `/job/message`，启动 Loop 走 `/job/start`，历史 / 侧边栏多 Job 经真实建 Job + 发消息预置。
- 断言对象调整为「真实模型流程下稳定可观察的结构信号」：消息节点出现、assistant 流式增量完成、工具块进终态、运行态 loading→idle、Loop 轮次推进、错误完整展示、列表排序 / 选中 / Rename / Delete / Pin 等纯前端行为。**不再断言固定自然语言文本片段**（真实模型输出不确定）。

### 3.2 组件层（Vitest + RTL，承接故障链路）

承接所有「真实模型无法触发」的故障与错误展示链路，靠 mock fetch / SSE / 存储制造：

- HTTP 发送失败：mock `/job/message` 返回错误，断言错误完整展示、用户消息保留、运行态恢复。
- SSE 鉴权失败：mock `/events` 返回 401 / 403，断言停止无意义重连、完整展示状态码 + 响应体。
- resume 410 恢复：mock `/events` 返回 410，断言重拉 snapshot 后重连；snapshot 再失败 / Job 缺失 / 鉴权失败时按固定格式同时展示原始 410 与恢复失败错误。
- 这部分前端错误展示链路（SSE 读响应体、`onError` 接通可见错误区、resume 双段错误格式）属于真实产品能力，**保留不动**，仅把触发方式从「后端故障注入」改为「组件层 mock」。

### 3.3 Go 单测（承接事件缓冲故障）

- 事件缓冲 resume / GC 行为（序号 GC 后返回 410、水位推进令旧 resume 点失效）直接用 Go 单测驱动事件缓冲，不再经 E2E 控制 API。
- 事件缓冲里为 E2E 预留的 GC 暂停钩子删除，回归覆盖改由单测保证。

### 3.4 用例去向对照

| 原 E2E 用例 | 去向 |
|---|---|
| 鉴权门（无 Cookie、错误密码、正常登录、探测失败重试） | 保留为真实 E2E（与模型无关） |
| 聊天快捷键真实 IME composition | 保留为真实 E2E |
| 设置页语言切换 | 保留为真实 E2E |
| 聊天主链路（建 Job / 发消息 / 流式回复 / 工具块） | 保留为真实 E2E，断言改为结构信号、不比对固定文本 |
| Loop 主链路（启动 / 轮次推进 / session 列表 / 终态） | 保留为真实 E2E，断言改为结构信号 |
| 侧边栏多 Job / Rename / Delete / Pin | 保留为真实 E2E，数据改真实建 Job 预置 |
| 历史会话加载与续问复用原 Job | 保留为真实 E2E，数据改真实建 Job + 发消息预置 |
| Job 删除 / 缺失 URL 清 stale 与提示 | 保留为真实 E2E，缺失 Job 用真实删除制造 |
| HTTP 发送失败 | 下沉组件层 |
| SSE 鉴权失败 | 下沉组件层 |
| resume 恢复成功 / 失败（snapshot 失败 / Job 缺失 / 鉴权失败三子类） | 下沉组件层（前端恢复链路）+ Go 单测（事件缓冲 410 / GC） |
| `replay_failure` 模型错误回放 | 删除（依赖回放模型）；真实模型错误展示由组件层 mock 接口错误覆盖 |
| 工具成功 / 失败固定回放 | 删除固定回放断言；保留真实 E2E 触发真实工具、断言工具块进终态（不比对固定结果） |
| E2E 模式 ACP 拒绝（创建 / 发送 / Loop） | 删除（依赖 E2E 模式）；ACP 在生产模式正常可用，无需 E2E 模式拒绝 |
| E2E-only API 关闭态拒绝 | 删除（路由本身被删，无需断言其被拒绝） |
| 控制 API 自身边界（control-state / 冲突 / 消费清理断言） | 删除（控制 API 整体删除） |

## 4. 待清除的生产侵入清单

### 4.1 整文件删除

- `cmd/web/handler/e2e.go`（全部 E2E 路由与 fixture handler）
- `services/e2e/`（E2E 控制服务）
- `services/agent/eino/e2e_replay_model.go`（回放模型）
- `services/agent/eino/e2e_tool.go` 与 `e2e_tool_test.go`（E2E 测试工具）
- `services/agent/eino/manager_test.go` 中 E2E 指纹相关用例随指纹字段移除一并清理

### 4.2 路由清除

- `cmd/web/api.go`：删除整个 `/api/v1/e2e` 路由组（11 条路由）及其注释。

### 4.3 生产分支 / 字段清除（27 处侵入点）

- Handler 初始化：`handler.go` 的 `e2eService` 字段、`NewServiceFromEnv()` 装配、`e2eEnabled()` 辅助、`seedE2EDefaultConfig` 调用。
- Agent 列表：`agent_list.go` 的 E2E 固定 Agent / 测试 Model 分支。
- Agent 运行：`agent_run.go` 的测试 Model 配置解析、场景捕获与 `WithE2E` 传参。
- Job 创建 / 发送 / 生命周期：`job.go`、`job_message.go`、`job_lifecycle.go` 的场景绑定、场景校验、`consumeE2EFailNextMessage`、ACP E2E 拒绝分支。
- Agent 构造与指纹：`quartet.go` 的回放模型替换分支、指纹中的 E2E 参数；`manager.go` 指纹计算的 E2E 入参。
- 事件缓冲：`event_buffer.go` 的 GC 暂停钩子；`services/job/service.go` 的 `ExpireResumePoint` 接口。
- 数据模型：`types/model/job.go` 的 `E2EScenario` 字段、`types/model/request.go` 的 `E2EScenario` 字段。
- 仓储：`repository/model_config.go` 中为 E2E 测试 Model 预置的相关逻辑。

> 说明：项目处于开发阶段，按 `docs/CLAUDE.md` 不考虑历史 Job 记录里 `E2EScenario` 字段的兼容迁移，直接移除。

### 4.4 前端与测试基建清除

- `web/e2e/fixtures/e2e-environment.ts`：删除 `QUARTET_E2E` 注入与关闭态后端夹具；保留隔离启动 / 端口 / 测试用户会话 / 产物管理。
- `web/e2e/fixtures/test.ts`、`web/vite.config.ts`、`web/src/components/ChatPage.tsx`：移除 E2E 专用分支 / 场景头注入，保留真实链路所需的 `data-testid` 选择器。
- `web/e2e/tests/startup.spec.ts`：按 §3.4 重写 / 拆分，删除依赖 E2E 专用能力的用例，下沉故障用例到组件层。

## 5. 风险与处理

- **CI 无凭证**：真实模型 E2E 不在无 key 环境确定性通过。`make e2e` 在缺凭证时直接失败并完整展示原因；CI 是否纳入 E2E 由后续单独决定，组件层 + Go 单测仍是无 key 可跑的回归主力。
- **断言稳定性**：真实模型输出不确定，所有断言改为结构 / 状态信号，禁止比对固定自然语言；工具与 Loop 用例断言「进入终态 / 轮次推进」而非具体内容。
- **指纹逻辑变更**：移除 Agent 指纹中的 E2E 参数后，需确认指纹仍能正确反映 workdir / model / systemPrompt 变更，避免缓存污染。
- **事件缓冲 GC**：移除 GC 暂停钩子后，resume / 410 行为以 Go 单测兜底回归，确认高并发 SSE 行为不受影响。
- **故障链路覆盖度**：下沉后须确认组件层用例完整覆盖原 E2E 故障断言点（错误完整展示、用户消息保留、运行态恢复、resume 双段错误格式、停止无意义重连）。

## 6. 执行步骤

> 步骤状态随推进更新（未执行 / 进行中 / 已完成）。

1. **[已完成]** 删除整文件级 E2E 代码（§4.1）与 `/api/v1/e2e` 路由组（§4.2）。
2. **[已完成]** 清除 Handler / Agent / Job / 事件缓冲 / 数据模型 / 仓储里的 27 处生产侵入分支与字段（§4.3），并修复编译；连带清理因侵入删除而失去调用方的 Seed / SeedModel 死代码链。
3. **[已完成]** Go 单测：事件缓冲 resume / GC / 410 行为已由现有 `event_buffer_test.go` 充分覆盖（`TestBuffer_SubscribeStaleSeqReturnsErrSeqGone` / `TestBuffer_ResumeGCNoReaderRace` 等）。删除的 `ExpireResumePoint` 仅是测试钩子，生产 410 路径由 `gcLocked` 产生且已被覆盖，无需新增（§3.3）。
4. **[已完成]** 组件层：新增 `web/src/utils/sse-client.test.ts`（8 用例），覆盖 401/403 鉴权拒绝→可见错误+停止重连、410→`onResumePointGone`(body) 一次+停止重连、错误体解析（§3.2）。resume 双段错误格式拼接仍由 `useJobChat` 持有，留给真实 E2E 真实 410 覆盖。
5. **[已完成]** E2E 基建：启动夹具移除 `QUARTET_E2E` 与关闭态后端夹具，新增从 `QUARTET_E2E_MODEL_*` env 写入隔离 `models.json` / `settings.json`，缺凭证即抛错（§3.1、§4.4）。
6. **[已完成]** E2E 用例：`startup.spec.ts` 按 §3.4 重写为 10 个用例（5 模型无关 + 真实 API 数据的列表/导航/Rename/Delete + 1 真实模型流式冒烟），删除全部 replay / scenario / 控制 API / closed-mode / ACP 拒绝用例。
7. **[已完成]** `docs/feature-2026-06-03-web-e2e.md` 已加顶部"已被取代"提示。
8. **[部分完成]** `go build ./...` 通过；改动包 Go 单测通过；组件层 `npm test` 41 用例通过；`npx playwright test --list` 列出 10 用例无编译错误。`services/job` 包内 3 个失败为本次改造前既有失败（与本方案无关，已比对基线确认）。**剩余**：在带真实模型凭证的环境跑 `make e2e` 跑通真实模型主链路。

## 7. 真实凭证环境变量

`make e2e` 缺凭证会直接失败并打印缺失原因（§5）。需提供：

| 变量 | 必填 | 说明 |
|---|---|---|
| `QUARTET_E2E_MODEL_API_KEY` | 是 | 真实 provider API key |
| `QUARTET_E2E_MODEL_NAME` | 是 | 上游模型标识（provider 的 model id） |
| `QUARTET_E2E_MODEL_CLASS` | 否 | provider 类型，默认 `ark`（可选 `claude` / `openai` 等） |
| `QUARTET_E2E_MODEL_BASE_URL` | 否 | 自定义 base url |
| `QUARTET_E2E_MODEL_ID` | 否 | 隔离 models.json 的本地数字 id，默认 `1000001` |
| `QUARTET_E2E_MODEL_DISPLAY_NAME` | 否 | 展示名 |
