# Loop 存续期控制：运行 / 暂停期改 LoopConfig + 优雅停止

## 1. 背景与目标

Loop 是一棵 `FlowNode` 树（`group` 带 `iterationCount`，`step` 带 `repeatCount` / prompt / agent / model / mode）。一旦 `Start` / `Continue`，`job.LoopConfig` 就成为只读快照：`runLoop` 拿到 `cfg := job.LoopConfig` 后按 index 递归遍历，结构在执行期不可变，想改只能丢弃任务重建。

**目标**：让用户在任务存续期间调整后续步骤的行为，分三档能力：

1. **暂停期全量编辑**：Stop 后改任意字段、增删 / 重排 node，Continue 生效。
2. **运行期实时改 prompt**：不停循环，改后续未执行 step 的 prompt，下一个 step 生效。
3. **运行期实时改 model / agent / mode**：不停循环，对后续「会新建 session」的 step 生效。

明确**不做**：运行期增删 / 重排 node（用「Stop → 全量编辑 → Continue」覆盖）。

并附带一个相关能力——**优雅停止**（让当前 step 跑完再停，见 §6）：现有 Stop 是硬停（cancel context、打断 in-flight step、Continue 重跑该 step），优雅停止则让当前 step 正常收尾、推进 resume，在干净的 step 边界停下。


## 2. 关键执行机制（设计依据）

- `executeRepeat` 在 `RunIteration` 前**现读** `node.Message / AgentType / StepModelID / ACPMode`；其中 model/agent/mode 真正起作用是在 `tryCreateSession → InitSession`，**仅 `beforeRound` / `eachRepeat` / 无 session 的 step** 会走到。
- `runFlowNodes` 里 `node` 是 `range nodes` 的**栈上拷贝**，直接改 in-flight slice 有数据竞争 → 不能依赖。
- `Variables` 已是「运行期带锁现读」的先例（`substituteVars` 持 `RLock` 读，`applyVarsToJob` 持 `Lock` 写）→ 运行期改字段复用同一并发模型。
- 暂停态 `Continue` 本就重读 `job.LoopConfig` 并基于 `Resume.NextPath` / `CurrentPath` 重算恢复点 → 档位 1 天然契合。

## 3. 设计：单一入口，按运行态分流

新增**一个**接口 `PUT /api/v1/job/:jobId/loop-config`，body 为完整 `{ loopConfig: { flow, variables? } }`（前端永远发全量，后端按 `job.Status` 决定怎么应用）：

- **非 running**（pending / stopped / failed / completed）→ `ReplaceLoopConfig`：**全量替换** + 进度 reconcile（档位 1）。
- **running** → `UpdateRunningStepFields`：与现有结构做 **ID 对齐的结构 diff**：
  - 结构一致 → 仅把每个 step 的 4 个可编辑字段（`message` / `agentType` / `modelId` / `acpMode`）就地写入存量 flow（档位 2 / 3）。
  - 结构不一致 → 返回 `ErrLoopStructureChanged`（HTTP 409）+ 文案：「运行中只能修改 prompt / model / agent / mode，结构变更请先停止循环」。

## 4. 后端实现

### 4.1 service（`services/job/executor_loopconfig.go`，新增）

- `ReplaceLoopConfig(ctx, jobID, cfg) (*JobProgress, error)`
  - 沿用 `updateJobField` 的「snapshot → save → mirror」骨架；拒绝 running（`ErrJobRunning`）。
  - `MigrateLoopConfig` + `ValidateFlow`；清掉 legacy `Rounds` / `IterationCount`。
  - `reconcileProgressToFlow`：当结构未变化（仅改字段 / label）时不动进度，保留已回填的分母与 early-stop 展示数据；当结构变化时才重算 `TotalSteps`，并按「路径仍存在 + 节点 ID 未变」判断 `Resume.NextPath` / `CurrentPath` / 历史结果是否仍有效，无效则剔除并重算计数。若旧恢复点失效但仍有存活的成功结果，则把恢复点重锚到「第一个尚无成功结果的步骤」，避免 Continue 从头重跑已完成的步骤。
  - ⚠️ **已知取舍**：删除 / 重排被记录过结果的 step 会丢弃其历史结果条目（早期工具可接受）。

- `UpdateRunningStepFields(ctx, jobID, newFlow) error`
  - `flowStructureEqual(a, b)`：递归比对 `id` / `type` / `roundMode` / `roundType` / `repeatCount` / `iterationCount` / `scriptId` / `scriptName` / children 顺序；忽略 message / agent / model / mode / label。比对前先把空 `roundType` / 空 `roundMode` / 小于 1 的 count 归一到语义默认值（prompt / none / 1），与校验和执行层对零值的处理保持一致，避免存量配置因默认值差异被误判为结构变更（既不会误拒纯字段编辑，也不会让暂停期保存误重算进度）。不一致返回 `ErrLoopStructureChanged`。
  - 一致则采用 save-then-commit：先把字段写到深拷贝并落盘，落盘成功后才就地更新 live flow 的 4 个字段 + label。落盘失败直接返回错误，live flow 与运行中的循环保持不变（不会出现「报错但改动已生效」）。

### 4.2 执行链路（`services/job/executor_loop.go`）

- `runFlowNodes` 的 step 分支：在算出 `stepPath`、构建 `stepOverrides` **之前**，用 `liveStepFields(job, stepPath)` 从 live `job.LoopConfig` 按 path（`stepNodeForPath`，由原 `roundModeForStepPath` 泛化而来）**带锁现读** 4 个可编辑字段覆盖到局部 `node` 拷贝。这样 `tryCreateSession`（overrides）与 `executeRepeat`（`node.Message`）都拿到最新值。
- 结构字段仍取快照 `node`（运行期禁止改结构，二者本就一致），blast radius 受控。

### 4.3 约束（运行期）

- 改动只对**尚未开始**的 step 生效；正在执行的 step 改不动。
- model / agent / mode 仅对**会新建 session** 的 step（`beforeRound` / `eachRepeat` / 无 session 兜底）生效；复用已有 session 的 `none` step 中途换不了模型 / agent（物理约束）。

### 4.4 HTTP / 路由

- `cmd/web/handler/job_lifecycle.go` `JobUpdateLoopConfig`：校验 body，按 `job.Status` 分流；running 改结构返回 409 + 文案。错误统一走 `jobErrMappings`（新增 `ErrLoopStructureChanged → 409`）。
- `cmd/web/api.go`：`jobGroup.PUT("/:jobId/loop-config", h.JobUpdateLoopConfig)`。
- `types/model/request.go`：`UpdateLoopConfigRequest{ LoopConfig }`。
- `types/model/flow_node.go`：提供 flow 路径有效性与节点 ID 映射的校验能力（枚举所有合法叶子步骤路径的 path set，配合「路径 → 节点 ID」映射，用于 reconcile 判断某条历史路径在新 flow 中是否仍指向同一步骤）。

## 5. 前端实现

- **复用 `LoopConfigPanel`**（新增可选 props，不影响新建流）：
  - `initialConfig`（seed flow / variables）、`onSave`（edit 模式保存）、`saveError`（页脚错误展示）、`runningLock`（结构锁）。
  - edit 模式：标题 → 「编辑循环配置」，主按钮 Start → Save / Saving，隐藏 workspace 选择器。
  - `runningLock`：禁用结构控件（add / duplicate / move / delete node、repeat / iteration 步进器、round-type 切换、session-mode 下拉、脚本选择器），保留 message / agent / model / mode；页头展示运行锁提示。结构变更 handler 自身也在 `structureLocked` 下提前返回（不只是隐藏入口）：包括 round-type 切换、session-mode 切换、脚本选择、repeat / iteration 步进器的提交（含失焦 / 回车触发的提交），以及父层的迭代 / 重复次数更新；锁生效时还会主动关闭已打开的结构类弹层（session-mode 下拉、脚本选择器），结构类弹窗（删除确认、导入对话框）在 `structureLocked` 下不渲染。这样即便任务在另一个 tab / SSE 中途变为 running、控件已处于打开 / 聚焦状态，也无法再产出本地结构草稿污染后续保存（否则会触发后端结构变更校验失败）。
  - 锁通过 `structureLocked` 透传到 `FlowOutline`（隐藏 action 行 + 禁用并阻止 `NumberStepper` 提交）和 `FlowStepEditor`（禁用并阻止 round-type 切换 / session-mode 下拉 / 脚本选择）。
- **入口**：`LoopProgress` 头部新增「Edit」按钮（`onEdit`，`isReadonly` 时不显示）；`JobChat` 渲染编辑面板，`runningLock = loopStatus === 'running'`。
- **接线**：`useJobChat.updateLoopConfig(cfg)` → `PUT .../loop-config`；成功 `setLoopFlow` /（暂停态）`setLoopProgress`，失败 rethrow 给面板显示 `saveError`。

## 6. 优雅停止（当前 step 跑完再停）

### 6.1 与硬 Stop 的区别

| | 硬 Stop（现有） | 优雅 Stop（新） |
|---|---|---|
| 触发 | cancel context | 设标志位，不动 context |
| 当前 step | 立刻打断 LLM 调用，不记结果 | 正常跑完、记结果、推进 resume |
| 停在哪 | 当前 step 中途 | 下一个 step 边界 |
| Continue 后 | 重跑被打断的 step | 从下一个 step 干净继续 |
| 终态 | Stopped（保留 resume） | Stopped（保留 resume） |

### 6.2 行为

- 优雅停止只在 **step 间隙**生效：当前 step 跑到自然结束（结果已落、resume 已指向下一个 step）后，循环不再进入下一个 step，直接以 **Stopped** 收尾，resume 保留 → Continue 从下一个 step 干净继续，不重跑。
- 进度条语义：**分母不变**。剩余 step 算「待续」，进度条停在当前完成度（如 3/8），不强制满格 —— 和硬 Stop 一致，符合「还能 Continue」语义；这点和 `STOP_WORKFLOW`（剩余 step 算「跳过」，需重算分母满格）不同。
- 标志位是 **best-effort**：若当前 step 卡死（LLM 不返回），优雅停止不会打断它，用户可再点硬 Stop 升级。
- 与硬 Stop 竞争：两者都触发时，硬 Stop 的 context 取消在 step 内优先（当前 step 会被打断）。
- 标志位在 run 启动（`Start` / `Continue`）时清除，避免上一轮的停止请求误停新的一轮；命中后即消费清除。此外硬 Stop 以及 run 收尾（完成 / 失败 / 超时 / panic / cancel 任意路径）也兜底清除标志，避免「优雅停止后升级硬停」等不经过 step 边界的路径残留陈旧的待停标志，让 `Get` 快照对已停止任务持续合成出错误的待停态。

### 6.3 实现

- **service**：`RequestGracefulStop(jobID)` 置位（per-job 运行态），仅当该 job 有可消费请求的活跃 loop run 时才生效（否则 no-op，不留陈旧标志）；`consumeGracefulStop` 在每个 step 边界检查并清除；run 启动时清除上一轮残留请求。「检查是否有活跃 run」与「置位待停标志」在同一把锁下完成，是不可分割的一步：避免 run 恰好退出后请求仍把标志写到一个已无活跃 run 的 job 上，留下永远无人消费的陈旧待停状态。run 以任何方式退出（完成 / 失败 / 超时 / panic / 硬停）时统一清除运行态，若仍有待停标志一并广播清除，保证非活跃 job 不可能携带待停标志。
- **执行链路**：`runFlowNodes` 在 step 成功完成、推进完 resume、进入下一个 step **之前**检查标志，命中返回新的 `stepResult`——`stepStopGraceful`，像 `stepStopWorkflow` 一样一路 bubble up（group 立即透传，不当作 `stepStopLoop`）。`runLoop` 收到后调用 `stopJob`（Stopped + 保留 resume），且**不** backfill 分母。
- **HTTP**：复用 `POST /api/v1/job/:jobId/stop`，body 加可选 `{ graceful: true }` —— graceful 调 `RequestGracefulStop` 立即返回 `status: "stopping"`，不 `StopAndWait`；缺省仍是硬停。
- **pending 状态可见性**：优雅停止的「待停」状态是运行期临时状态，不落盘（进程重启后请求本身也失效）。后端把它作为只读视图字段 `JobProgress.GracefulStopPending` 在 `GET /job/:id` 快照里按内存态合成，并在 请求 / 取消 / 消费 时广播一个 transient 的 `graceful_stop_pending` 自定义事件。前端 hydrate 时据此恢复「待停」状态，其他已连接的 tab 通过该事件实时同步，刷新或多端都能看到并取消「Stop after step」。
- **前端**：`LoopProgress` running 态把 Stop 拆成「Stop after step」（优雅，主）+「Stop now」（硬，次）两个动作；`useJobChat.stopLoop(graceful?)` 透传 `graceful` 标志到 stop 接口。硬停以及 `JOB_STOPPED` / `JOB_COMPLETED` / `JOB_FAILED` 终态事件统一清除本地待停标志（`stopPending`），与后端硬停清除标志对齐，避免硬停升级后「Keep running」按钮残留。`graceful_stop_pending` 是 transient 事件，断线重连后不保证回放，因此所有基于快照的恢复路径（hydrate、SSE 重连、watchdog、pre-connect snapshot）都以快照里的 `GracefulStopPending` 为权威来源同步本地待停标志，避免某条 transient 事件丢失后 UI 与后端长期不一致。loop 模式下 hook 层的操作错误（stop / continue / cancel / SSE 连接失败）通过 `LoopProgress` 的 `error` 展示，避免被顶部 banner 的 `!isLoop` 条件隐藏。
- **运行资源原子可见**：Start / Continue / SendMessage 把 job 状态翻转为 running 与运行期资源（取消句柄、完成信号、活跃 loop run 标记）的注册放在同一临界区，保证外部一旦观察到 running 状态，硬停一定能找到取消句柄、优雅停止一定能看到活跃 loop run，不存在「状态已 running 但资源尚未就绪」的窗口导致停止请求被误判为成功或不支持。

## 7. 涉及文件

后端：`services/job/{executor_loopconfig.go(新),executor_loop.go,executor_run.go,executor_store.go,executor_step.go,service.go,errors.go}`、`cmd/web/handler/job_lifecycle.go`、`cmd/web/api.go`、`types/model/{flow_node.go,request.go}`。
前端：`web/src/components/LoopConfigPanel/{index.tsx,FlowOutline.tsx,FlowStepEditor.tsx,NumberStepper.tsx}`、`LoopProgress.tsx`、`JobChat.tsx`、`hooks/useJobChat.ts`、`i18n/locales/{en,zh}.json`、相关 CSS。

## 8. 任务状态

- [x] flow 路径有效性 / 节点 ID 映射校验能力（`flow_node.go`）
- [x] `ReplaceLoopConfig` / `UpdateRunningStepFields` / `flowStructureEqual` / reconcile（service）
- [x] `liveStepFields` / `stepNodeForPath` + step 执行前现读（executor_loop）
- [x] HTTP handler + 路由 + request model + error 映射
- [x] 前端编辑面板（edit / runningLock 模式）+ 入口 + 接线 + i18n
- [x] 优雅停止：`RequestGracefulStop` / `stepStopGraceful` / `stop?graceful=true` + 前端双停止动作
- [x] Go 单测（结构 diff、reconcile、resume 失效 / 保留、running 改结构被拒、优雅停止边界）+ 前端组件测试
- [ ] 真实页面 E2E（运行 / 暂停两态编辑链路 + 两种停止）——待用户在真实环境验收

## 9. 验证

1. **Go**：`go build ./...`；`go test ./services/job/ ./types/model/ ./cmd/...` 全绿（含本特性单测）。
2. **前端**：`npm run build`、`npx vitest run`（43 tests）全绿。
3. **运行期改 prompt**：多 group/step loop 跑起来后改后续 step prompt → 下个 step 的 IterationStarted 用新 prompt。
4. **运行期改 model/agent**：改 `beforeRound` step 的 model → 下个该 step 新建 session 用新模型。
5. **运行期改结构被拒**：running 时增删 node 保存 → 前端显示后端 409 文案，循环不受影响。
6. **档位 1**：Stop → 增删 / 改 node → Continue → 进度分母正确重算、从合理恢复点继续、被删 step 的结果已清理。
7. **优雅停止**：running 时点「Stop after step」→ 当前 step 跑完后停（Stopped），下个 step 没起跑；Continue 从下个 step 干净继续、不重跑当前 step。
