# ACP 模型 / 模式 / 思考等级即时切换

日期：2026-07-02

## 背景

ACP Agent 的 model、mode、thoughtLevel 三类下拉选择，目前是"选择后只更新前端本地状态，等到发送消息时才随请求带给后端"。真正生效发生在 `ACPAgent.Run` 内部：每次 Run 都会依据传入的三个参数调用一次 Update，把选择推送到子进程 session。

这种方式有两个问题：

1. 选择与生效割裂，用户切换后无法立即得到"这个选择在当前 Agent 上真实可用、以及它联动出的新列表"的反馈。
2. ACP 协议里切换 model / thoughtLevel 的响应会返回**全量的可选项列表（ConfigOptions）**，也就是说切换一项可能引起其它项的列表变化（联动）。当前实现丢弃了这个响应，前端拿不到联动后的新列表。

## 目标

把三类下拉改成"选择后立即调用后端对应的设置接口"，后端在真实 session 上应用选择，并把 ACP 返回的最新可选项列表回传给前端刷新。区分两种场景：

- **ChatPage（已有 session）**：作用在该 session 对应的活跃 ACP Agent 上。
- **Home（尚无 session）**：像会话信息探测那样临时新建一个 ACP session，在其上应用并取回列表；最终发送消息时仍按现有逻辑把选择带进建 Job 请求。

同时移除 `ACPAgent.Run` 中"每次 Run 都 apply 三个参数"的逻辑，改为在 session 新建 / 重建时从持久化字段恢复一次。

## 关键约束（来自 ACP 协议本身）

1. **联动刷新是"部分刷新"**：
   - 切 model 或 thoughtLevel → 底层走通用的配置项设置接口，响应携带**全量 ConfigOptions**，可同时刷新 model 与 thoughtLevel 两份列表。
   - 切 mode → 底层走独立的 mode 设置接口，响应**不带** ConfigOptions，只能确认切换成功，无法回传联动列表。

   因此设置接口统一返回 `{models?, modes?, thoughtLevels?}` 三段可选结构，**谁能刷新就返回谁，为空的那几段前端保留旧值**。切 mode 时后端返回的三段可能都为空，前端自行把当前 mode 更新为所选值即可。

2. **Coco 后端已废弃**：本次一并移除仓库内所有 Coco 特化逻辑（见下"Coco 清理"章节）。移除后，model 切换在所有后端都能统一走"在活跃 session 上直接设置"这条路径，不再需要"构造时 pin model + model 变更触发 Agent 重建"的特殊处理。

## 决策记录

- **Home 临时 session 策略：无状态重放**。每次切换都新建一个临时 ACP session，按当前已选的 model / mode / thoughtLevel 依次重放后取回列表，用完即弃。不做 per-command 保活与 idle 回收。Home 属低频操作，每次切换新建一个子进程 session（数百毫秒）可接受。
- **接口形态：统一单端点**。新增一个设置端点，请求体带可选 `sessionId`：带则走 session 场景，不带则走 Home 预览场景。前端只需对接一套。
- **Run 参数：移除 + 持久化恢复**。从 `ACPAgent.Run` 签名移除三个 acp 参数，改由 session 新建 / 重建时从持久化字段应用。

## 数据流

### ChatPage（有 sessionId）

下拉切换 → 调用设置接口（带 sessionId + 目标项 + 值）→ 后端取该 session 的活跃 ACP Agent → 在其连接上应用切换 → 用 ACP 响应重建可选项列表 → 持久化到 session → 返回三段可选列表 → 前端用非空段刷新对应下拉。

### Home（无 sessionId）

下拉切换 → 调用设置接口（带 agentType + 当前全部已选 + 本次要改的项）→ 后端新建临时 session，按已选项重放 → 取回列表 → 返回三段可选列表 → 前端刷新。发送消息时仍走原有建 Job 流程，把最终选择带上。

## 分层改动

### 协议层 `pkg/acp`

三个设置方法从"只返回 error"改为"返回会话响应视图 + error"，把底层响应里的 ConfigOptions（若有）透出给上层：

- 设置 model：返回携带全量 ConfigOptions 的会话响应视图。
- 设置 thoughtLevel：同上。
- 设置 mode：底层无 ConfigOptions，返回空列表的会话响应视图（表示"无联动列表可回传"）。

### 探测层 `services/agent/probe`

- 复用现有的"建连 + 新建 session"能力（会话信息探测已有此逻辑），对外提供 Home 预览设置能力：给定 agent 命令与"当前已选项集合"，新建临时 session、重放已选项、取回三段列表。
- 现有的三个"从会话响应提取 model / mode / thoughtLevel 列表"的转换逻辑保持在本包内，供预览路径和会话路径共同复用；按分层规范不对外暴露裸函数，以包内内聚入口的形式提供。

### 会话 Agent 层 `services/agent/acp`

- `ACPAgent` 新增三个即时设置方法（设置 model / mode / thoughtLevel），职责：在当前连接上调用协议层设置方法 → 把响应转换为三段列表 → 更新自身 current 缓存字段 → 返回三段列表。
- `acpService` 新增对应的设置入口，内部按缓存键取/建 Agent、调用上述方法、释放租约。
- `ACPAgent.Run`：
  - 从签名移除 model / mode / thoughtLevel 三个参数以及对应的三段 apply 逻辑。
  - 在 session 新建 / 重建的收口处（构造、reset、reconnect 的重建分支），从 session 持久化字段应用一次，保证 Home 首次发送、以及 reset / reconnect 后不丢选择。

### 会话持久化 `services/session`

- 复用已有的 `UpdateModelID` / `UpdateACPMode` / `UpdateACPThoughtLevel`。ChatPage 设置成功后，按切换的项持久化对应字段。

### HTTP 层 `cmd/web/handler` 与路由

- `cmd/web/api.go` 注册新的设置端点（走默认鉴权中间件）。
- 新增 handler：解析目标项（model / mode / thoughtLevel）与值，按有无 sessionId 分流到会话 Agent 层或探测层预览；成功后（会话场景）持久化到 session；统一返回三段可选列表。

### 前端 `web/`

- api client 新增设置调用。
- Home（`ChatPage.tsx`）与会话（`ChatInput.tsx`）的三个下拉 `onChange`：改为异步调用设置接口 → 用返回的非空段刷新对应列表，空段保留旧值；加入加载态与失败回滚。
- 发送逻辑保留：建 Job 仍需带最终选择。

## Coco 清理

移除以下 Coco 特化逻辑，使 model 切换在所有后端统一走"活跃 session 直接设置"：

- `services/agent/acp/agent.go`：`isCoco`、`constructorModelIDFor`、`presetCocoModel`、构造函数里的 preset 分支、`constructorModelID` 字段，以及 `RequiresRebuild` 中基于 model 的重建判定（保留 agentType / workdir 变化触发重建）。相关注释同步清理。
- `services/agent/acp/manager.go`：`GetOrCreate` 中围绕 Coco preset model 的重建说明与判定相应简化。
- `services/agent/probe/probe.go`：内置 agent 列表中的 coco 条目。
- `pkg/acp`、`cmd/web/handler/text_generator.go` 中以 coco 为例的注释与关键字（进程识别关键字、示例注释）按实际使用情况清理，不影响其它 agent 的识别。

> 注：清理需通过全量编译与实际运行验证，确认没有残留引用。

## 边界与风险

- **联动只对 model / thoughtLevel 生效**：这是协议限制，切 mode 无法回传联动列表，前端需接受"切 mode 后其它两项列表不刷新"。
- **Home 每次切换新建子进程 session**：低频可接受；若后续 Home 切换过于频繁再考虑保活方案。
- **mid-Run 切换**：遵循既有约定，切换 / 并发出错**不 reset session**（reset 会丢全部上下文）；Agent 尚未创建时先取/建再设置。
- **Run 签名变更**：唯一调用方在 `cmd/web/handler/agent_run.go`，随改；graph / schedule 等路径若经由同一入口则不受影响，需在实现时确认无其它直接调用方。
