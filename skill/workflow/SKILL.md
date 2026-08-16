---
name: quartet-workflow
description: "管理 Quartet graph workflow（图工作流）：在「模型库（agent）」里新增、列出、查看、更新、删除、校验、手动运行（run）工作流。当用户要创建/编排一个 Quartet workflow、graph、DAG 流程，或增删改查、手动运行图工作流时使用。定时任务（cron/schedule）管理走 quartet-schedule skill，微信主动推送走 quartet-wechat skill。本 skill 通过 CLI 调 Quartet 后端 HTTP API，内置了编写合法 workflow config（节点/边/变量/条件）的完整指南，以及「多 Agent 选择 / Double Check / 对抗验证」等推荐编排模式。"
metadata:
  requires:
    bins: ["quartet-cli"]
  cliHelp: "quartet-cli --help"
---

# quartet-workflow

通过 CLI 管理 Quartet 的 graph workflow 与定时任务。CLI 是对后端 HTTP API 的薄封装，复用后端类型，能在本地构造请求、远端校验。

命令组一览（`quartet-cli <group> -h` 看子命令）：

| 组 | 作用 |
|---|---|
| `workflow` | workflow 的增删改查、校验、**手动运行**（`run`） |
| `schedule` | cron 定时任务的增删改查、启停、立即触发 |
| `workspace` | 只读列出 workspace（给 create --workspace 提供 ID） |
| `job` | 只读查看 job 列表/详情、停止运行中的 job |
| `agent` | 只读列出已安装的 ACP agent 及其模型（编排 workflow 选 agentType 用） |
| `wechat` | `send` 主动推送微信消息；`accounts` 列出已登录账号（发现 --user ID） |

## 库边界（重要）

工作流分两个库，用 `type` 区分：
- **`user`**：用户在 Web UI 手工编排的工作流。
- **`agent`**：模型（你）通过本 CLI 创建/管理的工作流。

**本 CLI 的权限边界（客户端强制）**：
- `create` 创建的工作流**一律标记为 `agent`**。
- `update` / `delete` 会先 `get` 目标，**只有 `type=agent` 才放行**；对 `user` 库工作流一律拒绝（防止误改/误删用户的工作流）。
- `get` / `list` 只读，两个库都能看。

## 准备

### 构建 CLI

```bash
bash <skill_dir>/build.sh   # 产出 <skill_dir>/bin/quartet-cli
```

> 也可以在仓库里 `make install-skill-cli`，把 `quartet-cli` 装到 `~/.local/bin`（在 PATH 上）。

### 连接配置（环境变量）

| 变量 | 说明 | 默认 |
|---|---|---|
| `QUARTET_BASE_URL` | 后端地址 | `http://127.0.0.1:8090` |
| `X_AGENT_AUTH` | 鉴权 token，非空时作为 `X-AGENT-AUTH` 头发送 | 空 |

鉴权规则：
- 后端进程的 `X_AGENT_AUTH` 可以是逗号分隔的 token 列表，例如 `tokenA,tokenB`；后端校验时会拆开，任意一个匹配即可。
- CLI 读取自己环境里的 `X_AGENT_AUTH` 后**自动取第一个 token**发送（含空白修剪），所以直接把整段逗号列表赋给它也能正常工作。

403 排查顺序：
1. 先确认后端是否要求鉴权：`curl -s "$QUARTET_BASE_URL/api/v1/health"`，看 `authRequired`。
2. 如果 `authRequired=true`，确认当前 shell 的 `X_AGENT_AUTH` 已设置且非空。
3. 如果浏览器已登录，可从 localStorage 取 `quartet.x_auth_token`，它就是可用于 CLI 的单个 token。
4. 仍然 403 时，看后端日志里的 `tokenPrefix` 和 `tokenLen` 比对发出去的是哪个 token。

所有错误（含后端校验错误）都会**全量打印**到 stderr。结果 JSON 打印到 stdout。

## 命令

> 下面用 `qwf` 代指 `quartet-cli workflow`（即 PATH 上的 `quartet-cli`，或 `<skill_dir>/bin/quartet-cli workflow`）。所有 workflow 操作都在 `quartet-cli workflow` 这个命令组下。

### create — 新建（强制 agent 库）

```bash
qwf create --name "我的流程" [--description "说明"] [--workspace <wsId>] --config-file config.json
# 或从 stdin 读 config：
cat config.json | qwf create --name "我的流程"
```
- `--config-file` 省略或为 `-` 时从 stdin 读。
- config JSON 可以是**裸 GraphConfig**（`{"nodes":...,"edges":...}`），也可以是 `{"config": {...}}` 包装对象。
- 成功后打印创建出的完整 workflow JSON（含 `id`、`type: "agent"`）。

### list — 列表（默认只看 agent 库）

```bash
qwf list                  # 只列 agent 库
qwf list --type all       # 列全部（user + agent）
qwf list --type user      # 只列 user 库
qwf list --json           # 输出原始 summary JSON
```
默认表格列：`id  type  name  nodes=N edges=M`。被后端跳过的损坏文件会以 `warning:` 打到 stderr。

### get — 查看完整 JSON（只读，任意库）

```bash
qwf get <workflowId>
```
输出完整 workflow（含 config）。**更新前先 get** 拿到当前 config 再改。

### update — 更新（仅 agent 库）

```bash
qwf update <workflowId> --name "新名字"
qwf update <workflowId> --config-file new-config.json
qwf update <workflowId> --description "新说明"
```
- 只改你传的字段；至少传一个 `--name` / `--description` / `--config-file`。
- CLI 自动先 `get`：校验 `type=agent` + 取 `updatedAt` 做乐观锁（无需你手动传 updatedAt）。
- 对 `user` 库工作流会直接报错拒绝。

### delete — 删除（仅 agent 库）

```bash
qwf delete <workflowId>
```
同样先 get 校验 `type=agent` + 乐观锁。软删除（后端保留文件供历史 run 解析，但不再出现在列表）。

### validate — 静态校验（不落库）

```bash
qwf validate --config-file config.json
cat config.json | qwf validate
```
合法打印 `valid`（exit 0）；非法打印全部定位错误（exit 1）。**建议 create/update 前先 validate。**

### run — 手动运行一次（任意库，不修改 workflow）

```bash
qwf run <workflowId> [--workspace <wsId>] [--workdir <dir>]
```
启动一个 graph run（等价于 UI 上的「运行」），创建一个新的 graph job 并开始执行。运行不改动 workflow 本身，所以 user / agent 库都能跑。`--workspace` / `--workdir` 省略时用 workflow 自己绑定的运行环境，再缺省落到默认 workspace。
stdout 打印 `{"runId","jobId","workflowId","status"}`；之后用 `quartet-cli job get <jobId>` 查状态、`quartet-cli job stop <jobId>` 停止。

---

## 相关 skill

- **定时任务**（cron 自动运行 workflow）：用 `quartet-cli schedule` 组管理（create/list/get/update/delete/toggle/run），详见 **quartet-schedule** skill。
- **微信主动推送**（把运行结果发到微信）：`quartet-cli wechat send / accounts`，详见 **quartet-wechat** skill。

## 其它只读/辅助命令

```bash
quartet-cli workspace list [--json]   # workspace ID/标题/工作目录；create workflow/schedule 的 --workspace 从这里取
quartet-cli job list [--workspace <id>] [--limit N] [--json]   # job 摘要表：id status mode 创建时间 标题
quartet-cli job get <jobId>           # 完整 job 快照 JSON（含 lastEventSeq）
quartet-cli job stop <jobId>          # 停止运行中的 job（非 running 时幂等成功）
quartet-cli agent list [--json]       # 已安装 ACP agent 及模型目录；编排 prompt 节点的 agentType 从这里取
```

---

# Workflow 编写指南

这是生成**合法 config** 的唯一依据，全部基于后端真实校验规则。保存时会做全量静态校验，任一不满足即失败。

## 1. 顶层结构

`create` 的请求体（`--config-file` 给的是其中的 config，或整个包装对象）：

```json
{
  "name": "工作流名称（必填，非空）",
  "description": "可选",
  "config": { "nodes": [...], "edges": [...], "variables": {...}, "runConfig": {...} }
}
```

`config`（GraphConfig）字段：

| 字段 | 类型 | 必填 | 说明 |
|---|---|---|---|
| `nodes` | 节点数组 | 是 | 见 §2 |
| `edges` | 边数组 | 是 | 见 §3 |
| `variables` | `{string:string}` | 否 | 初始变量表，键须合法且非保留名（§5） |
| `disabledVars` | `string[]` | 否 | 被禁用变量名（运行时按空串参与） |
| `runConfig` | 对象 | 否 | 运行配置，见 §7 |
| `workspaceId` / `workdir` / `sandboxId` | string | 否 | 运行环境绑定 |
| `canvas` | 对象 | 否 | 纯画布视图状态，不影响执行/校验，可省略 |

> `metadata` 等纯展示字段对校验无影响，可省略。**但模型新建 workflow 时必须给每个节点写 `layout`**，否则前端只能用兜底坐标，复杂图会挤在一条线或互相遮挡。

## 2. 节点 GraphNode

通用字段：`id`（必填、全局唯一）、`type`（必填）、`title`（可选）、`parentId`（仅 loop 子图用）、`config`（类型专属）、`layout`（见 §4）。

节点类型与 config 约束：

### start / end（控制节点）
- config 必须为空：**不得**设 `script`/`prompt`/`agentType`/`condition`/`loopMode`/`outputVariables` 等任何业务字段。
- `start`：无入边、有 ≥1 出边。`end`：有 ≥1 入边、无出边。
- 可以有多个 start、多个 end。

### shell（执行脚本）
- `config.script`：Shell 脚本（实践上必给）。
- `config.outputVariables`（可选）：输出变量名数组，每个须合法、非保留、本节点内不重名。
- `config.lastAssistantAlias`（可选）：给本节点输出起别名（规则同上）。
- `config.timeoutSeconds`（可选，整数 ≥0，0=不限）。
- Shell 用 `quartet_set "名" "值"` 写输出变量（§5）。

### prompt（Agent 节点）
- `config.prompt`：提示词（实践上必给）。
- `config.sessionStrategy`：`new`（默认）或 `inherit`。
- `config.agentType`：**当 sessionStrategy 为 new（或空）时必填**；inherit 时可省（继承上游 Agent 会话）。
- `config.modelId` / `acpMode` / `acpThoughtLevel`（可选）。
- `outputVariables` / `lastAssistantAlias` / `timeoutSeconds`：同 shell。
- 输出变量靠模型输出 `QUARTET_OUTPUT:名=值`（§5）。

### clarify（澄清 Agent 节点，与用户讨论后续跑）
- 与 prompt 相同的 Agent/输出契约。
- **额外约束**：`parentId` 必须为空 —— **clarify 不能放进 loop 子图**。

### ifElse（条件分支，只路由不产出）
- `config.condition`：**必填**，语法见 §6。
- **不得**声明 `outputVariables` / `lastAssistantAlias`。
- 出边：**必须恰好一条 `yes` + 一条 `no`**（见 §3）。

### loop（循环容器）
- `config.loopMode`：**必填**，`fixed` 或 `until`。
- fixed：`config.fixedCount` ≥0（0=跳过子图直接走出边；**别忘了填**，缺省为 0 会导致整个子图一轮都不跑）。
- until：`config.untilCondition` **必填**且语法合法（do-while，至少跑一轮，轮末求值）。
- `config.maxIterations`（可选，0..1000，0=用默认）。
- **两种模式的字段互斥且不混用**：`fixed` 模式下 `untilCondition` 被忽略；`until` 模式下 `fixedCount` 被忽略。不要两个都填以免误解语义。
- **不得**声明 `outputVariables` / `lastAssistantAlias`。
- **子图**：所有 `parentId == loop节点id` 的节点构成其子图，必须：
  - 非空；
  - **恰好一个** loop 内 start（`type=start, parentId=loopId`）作入口；
  - **至少一个** loop 内 end（`type=end, parentId=loopId`）作出口；
  - 内部从入口可达、且都能到达内部 end；
  - 边不跨容器（见 §3）。

## 3. 边 GraphEdge

字段：`id`（必填、唯一）、`sourceNodeId`、`targetNodeId`（必须指向存在节点）、`sourcePort`（见下）。

**sourcePort 规则**：
- **ifElse 出边**：必须是 `yes` 或 `no`，且恰好各一条。
- **其它所有节点出边**：用 `default`（或省略），**不得**用 `yes`/`no`。

**scope（容器）规则**：边的 source 和 target 必须**同 scope**（`parentId` 相同）。
- 主图节点（parentId 空）之间互连，或连到 loop **容器节点本身**。
- loop 子图内节点之间互连。
- **外部不能直接连进 loop 子图内部**，子图内部也不能直接连到外部 —— 只能经由 loop 容器节点进出。

## 4. 画布布局 layout（新建时必写）

`layout` 只影响前端展示，不参与后端执行校验；但为了让用户打开模型生成的 workflow 时能直接看懂结构，**create/update config 时要为每个 node 提供非重叠坐标**。

基础规则：
- 主图从左到右排：start 在左，end 在右；同一执行顺序每列水平间距建议 `260`，普通节点纵向间距建议 `140`。
- 普通节点可按约 `200x92` 的视觉占位规划；不要让两个普通节点的 `x/y` 完全相同，分支节点要上下错开。
- `ifElse` 的 `yes` / `no` 分支放到下一列的上下两行，汇合节点放到再下一列的中间。
- loop 容器节点必须写 `layout.width` / `layout.height`，默认用 `560x320` 起步；子图复杂时按内容加大，保证内部业务节点不贴边。
- loop 子图节点的 `layout.x/y` 是**相对父 loop 容器**的坐标，不是全局坐标。
- loop 内的 start/end 是入口/出口端口，前端会按容器尺寸钉在边界上；仍建议写入合理坐标：start `{x:0,y:height/2-13}`，end `{x:width-62,y:height/2-13}`。
- 嵌套 loop 时，内层 loop 也是父 loop 内的一个普通大节点：内层 `layout.x/y` 用父容器相对坐标，内层子节点再用内层相对坐标。
- 可加 `canvas.viewport`，推荐 `{ "x": 0, "y": 0, "zoom": 0.8 }`；不确定时省略。

推荐坐标模板：
- 线性主图：`start(40,160) -> node1(300,160) -> node2(560,160) -> end(820,160)`。
- 二分支：`if(300,160) -> yes(560,80), no(560,240) -> join/end(820,160)`。
- loop 容器：主图 `loop(300,40,width=820,height=360)`；内部 `loop_start(0,167) -> work(140,134) -> loop_end(758,167)`。
- 复杂图先按 DAG 分层：第 `col` 列 `x = 40 + col*260`，同列第 `row` 个 `y = 80 + row*140`；主图和每个 loop 子图分别计算。

## 5. 变量系统

**初始变量** `config.variables`：`{名:值}`，键须 `[A-Za-z_][A-Za-z0-9_]*` 且非保留名。

**输出变量**：
- Shell 脚本里：
  - `quartet_set "名" "值"` → 写一个输出变量（安全承载空值/空格/等号）。
  - `quartet_break` / `quartet_stop` → 提前结束当前循环（仅 loop 内）。
  - `quartet_return` → 提前成功结束整个 run。
- Prompt/Clarify 模型输出里：行内出现 `QUARTET_OUTPUT:名=值`（按首个 `=` 切分，值可空、可含 `=`；同名多行取最后一行）。
  - 声明在 `outputVariables` 的变量**必须被产出**，否则节点失败；未声明的 `QUARTET_OUTPUT` 也会流向下游。
  - 系统会自动在 prompt 末尾追加输出协议引导，**你写 config 时不用自己加这段后缀**。

**`_last_assistant_msg`（内置保留变量）**：本节点的原始最终输出（Prompt/Clarify=模型原始输出，Shell=stdout 全量）。可用 `config.lastAssistantAlias` 起个别名给下游 `{{别名}}` 引用。

**引擎注入的只读变量**（不可声明，可在 `{{}}`/条件/Shell env 读）：
- `_current_time`：当前时间 RFC3339（所有节点可读）。
- 仅 loop 子图内：`QUARTET_LOOP_INDEX`（当前轮 0-based）、`QUARTET_LOOP_FIXED_COUNT`（fixed 模式次数，until 为空）、`QUARTET_LOOP_MAX_ITERS`（最大轮数兜底）。

**保留命名空间**：变量名以 `_` 开头**或** `QUARTET_` 开头的，均为保留名，**初始变量/输出变量/别名都不得声明**。

**文本替换 `{{名}}`**（用于 shell script、prompt）：已知变量→替换为值；禁用变量→空串；**未知变量→保留 `{{名}}` 字面量不报错**。单趟替换。

## 6. 条件表达式语法（ifElse 的 condition、loop 的 untilCondition 通用）

**最易写错，务必精确**：

- **逻辑运算符只能用中文**：`且`、`或`、`非`（不支持 `&&`/`||`/`!`/and/or）。优先级 `()` > `非` > 比较 > `且` > `或`。
- **变量必须写 `{{名}}`**；不支持裸变量当真值（`{{x}}` 单独出现非法，必须显式比较如 `{{x}} == "true"`）。
- **字符串字面量用英文双引号** `"..."`，仅支持 `\"` 和 `\\` 转义。
- **无 number/bool 字面量**，一律按字符串比较（如 `{{n}} == "0"`、`{{ok}} == "true"`）。
- 比较运算符：`==` `!=` `>` `>=` `<` `<=` `StartWith` `EndWith`。
  - ⚠️ `>` `>=` `<` `<=` 是**字符串字典序**比较：`"10" > "9"` 为 **false**（'1' < '9'）。数字大小比较会踩坑。
- 一元后缀：`{{n}} 是偶数`（能解析为整数且偶数为真，否则 false 不报错）。
- 比较选项（跟在比较式后，可叠加）：`忽略大小写`、`忽略空格`。
- **未知/不存在的变量在条件里解析为空字符串，不报错**（`{{未定义}} == ""` 为 true）。

合法示例：`{{status}} == "done"`、`{{count}} != "0" 且 {{ready}} == "true"`、`非 ({{name}} StartWith "tmp_")`、`{{i}} 是偶数`。

## 7. runConfig 边界

| 字段 | 范围 | 默认 |
|---|---|---|
| `concurrencyLimit` | 0..16（0=默认 8，1=串行） | 8 |
| `defaultNodeTimeoutSec` | ≥0（0=不限） | 0（不限） |
| `jobTimeoutSec` | ≥0（0=不限） | 0 |
| `defaultLoopMaxIters` | 0..1000（0=100） | 100 |
| `instanceLimit` | ≥0 | 100000 |

可整段省略，用默认值。

## 8. 示例

### 8.1 最简：start → shell → end

```json
{
  "name": "minimal-shell",
  "config": {
    "nodes": [
      { "id": "start_1", "type": "start", "layout": { "x": 40, "y": 160 } },
      { "id": "shell_1", "type": "shell", "title": "打印", "config": { "script": "echo hello" }, "layout": { "x": 300, "y": 160 } },
      { "id": "end_1", "type": "end", "layout": { "x": 560, "y": 160 } }
    ],
    "edges": [
      { "id": "e1", "sourceNodeId": "start_1", "targetNodeId": "shell_1", "sourcePort": "default" },
      { "id": "e2", "sourceNodeId": "shell_1", "targetNodeId": "end_1", "sourcePort": "default" }
    ]
  }
}
```

### 8.2 Agent：start → prompt → end

```json
{
  "name": "minimal-prompt",
  "config": {
    "nodes": [
      { "id": "start_1", "type": "start", "layout": { "x": 40, "y": 160 } },
      { "id": "p1", "type": "prompt", "title": "问答",
        "config": { "prompt": "请回答用户问题", "sessionStrategy": "new", "agentType": "<你的 agentType>", "outputVariables": ["answer"] },
        "layout": { "x": 300, "y": 160 } },
      { "id": "end_1", "type": "end", "layout": { "x": 560, "y": 160 } }
    ],
    "edges": [
      { "id": "e1", "sourceNodeId": "start_1", "targetNodeId": "p1", "sourcePort": "default" },
      { "id": "e2", "sourceNodeId": "p1", "targetNodeId": "end_1", "sourcePort": "default" }
    ]
  }
}
```
> `agentType` 必填（因 sessionStrategy=new）。第一个 Agent 不能 inherit。

### 8.3 分支：ifElse（一 yes 一 no，各自汇到 end）

```json
{
  "name": "ifelse-demo",
  "config": {
    "variables": { "mode": "fast" },
    "nodes": [
      { "id": "start_1", "type": "start", "layout": { "x": 40, "y": 160 } },
      { "id": "if_1", "type": "ifElse", "title": "判断", "config": { "condition": "{{mode}} == \"fast\"" }, "layout": { "x": 300, "y": 160 } },
      { "id": "s_fast", "type": "shell", "config": { "script": "echo fast" }, "layout": { "x": 560, "y": 80 } },
      { "id": "s_slow", "type": "shell", "config": { "script": "echo slow" }, "layout": { "x": 560, "y": 240 } },
      { "id": "end_1", "type": "end", "layout": { "x": 820, "y": 160 } }
    ],
    "edges": [
      { "id": "e1", "sourceNodeId": "start_1", "targetNodeId": "if_1", "sourcePort": "default" },
      { "id": "e2", "sourceNodeId": "if_1", "targetNodeId": "s_fast", "sourcePort": "yes" },
      { "id": "e3", "sourceNodeId": "if_1", "targetNodeId": "s_slow", "sourcePort": "no" },
      { "id": "e4", "sourceNodeId": "s_fast", "targetNodeId": "end_1", "sourcePort": "default" },
      { "id": "e5", "sourceNodeId": "s_slow", "targetNodeId": "end_1", "sourcePort": "default" }
    ]
  }
}
```

### 8.4 循环：loop 子图（固定 3 次）

```json
{
  "name": "loop-demo",
  "config": {
    "nodes": [
      { "id": "start_1", "type": "start", "layout": { "x": 40, "y": 160 } },
      { "id": "loop_1", "type": "loop", "title": "重复3次", "config": { "loopMode": "fixed", "fixedCount": 3, "maxIterations": 10 }, "layout": { "x": 300, "y": 40, "width": 820, "height": 360 } },
      { "id": "loop_start", "type": "start", "parentId": "loop_1", "layout": { "x": 0, "y": 167 } },
      { "id": "loop_body", "type": "shell", "parentId": "loop_1", "config": { "script": "echo round $QUARTET_LOOP_INDEX" }, "layout": { "x": 140, "y": 134 } },
      { "id": "loop_end", "type": "end", "parentId": "loop_1", "layout": { "x": 758, "y": 167 } },
      { "id": "end_1", "type": "end", "layout": { "x": 1180, "y": 160 } }
    ],
    "edges": [
      { "id": "e1", "sourceNodeId": "start_1", "targetNodeId": "loop_1", "sourcePort": "default" },
      { "id": "e2", "sourceNodeId": "loop_1", "targetNodeId": "end_1", "sourcePort": "default" },
      { "id": "e3", "sourceNodeId": "loop_start", "targetNodeId": "loop_body", "sourcePort": "default" },
      { "id": "e4", "sourceNodeId": "loop_body", "targetNodeId": "loop_end", "sourcePort": "default" }
    ]
  }
}
```
> 主图边（e1/e2）连的是 loop **容器** `loop_1`；子图三节点 `parentId` 都是 `"loop_1"`，子图边（e3/e4）只在子图内部。until 模式则改 `loopMode:"until"` + `untilCondition:"{{done}} == \"true\""`。

## 9. 避坑清单

1. **纯 start→end**：没有业务节点 → 报错（进度分母为 0）。
2. **端口错配**：ifElse 出边用了 `default` 或缺一条；普通节点出边用了 `yes`/`no`。
3. **非法环**：两个业务节点互连成环（没用 loop 容器）。
4. **跨容器边**：主图节点直连 loop 子图内部，或子图内连到外部。
5. **loop 子图缺入口/出口**：没有 loop 内 start、或没有 loop 内 end、或有多个入口 start。
6. **parentId 指向非 loop 节点**（或不存在/自指）。
7. **首个 Agent 用 inherit**：从 start 出发遇到的第一个 prompt/clarify 不能 inherit（没有上游会话）。
8. **new session 缺 agentType**。
9. **保留变量名**：声明了 `_xxx` 或 `QUARTET_xxx`。
10. **并行节点声明同名输出变量**（如 diamond 两条并行分支都写 `x`）。
11. **悬空节点**：某业务节点没入边或没出边，或从 start 不可达，或到不了任何 end。
12. **clarify 放进了 loop 子图**（parentId 非空）。
13. **条件语法错**：用了 `&&`/`and`、裸变量当条件、字符串用单引号、数字比较踩字典序坑。
14. **loop 缺 loopMode / until 缺 untilCondition / fixedCount 为负 / maxIterations > 1000**。
15. **重复的节点 id 或边 id**。
16. **新建 workflow 没写 layout，或多个节点 layout 坐标相同**：前端打开后会堆在一起；按 §4 分层排布。

> 拿不准就先 `qwf validate`：它返回每条错误的定位（节点/边/变量/配置项）和原因。
>
> 另外提醒：§8 的最简示例（start→shell→end）是为讲语法而**没有**用 loop 包裹；真正新建任务时请按 §10 模式 D，把执行节点放进 loop 子图里。

## 10. 推荐模式（新建 workflow 时尽量用上）

**用户偏好**：新建工作流（尤其是「Review / 审查 / 多 Agent 校验」类任务）时，应尽量组合下面四种模式。它们的目的是用「多源 + 复核 + 对抗 + 重复」把单个 Agent 的幻觉/误报压下去、并让任务能反复迭代直到收敛。完整可运行示例见 `references/code-review-demo.json`（已通过 `validate`），下面只讲模式本身。

### 模式 A：多 Agent 选择（按条件分流到不同 Agent）
用一个 `ifElse` 在循环里按条件把任务分给**不同的 Agent**（不同模型/不同 agentType），让多个模型轮流或并行参与，避免单一模型偏见。
- 典型条件：`{{MultiWorker}} == "1" 且 {{QUARTET_LOOP_INDEX}} 是偶数` —— 开了多 worker 时，偶数轮走 A 模型、奇数轮走 B 模型。
- 结构：`ifElse` 的 `yes`/`no` 各连一个 `prompt`（不同 `agentType`/`modelId`，都用 `sessionStrategy: new`），两条分支再汇合到下一个节点。
- 注意：两个分支是互斥的（同一轮只走一条），所以它们汇合到同一个下游节点是合法的，不构成「并行写冲突」。

### 模式 B：Double Check（同一会话内自我复核）
在出 review 结论的 Agent 后面，紧跟一个 `sessionStrategy: inherit` 的 `prompt`，让**同一个会话**重新审视自己刚才的输出，剔除不成立的问题。
- prompt 形如：「你重新 double check 一下上面输出的每一个问题，确认是不是真的是问题。重新输出真的是问题的 issue list。」
- 用 `lastAssistantAlias`（如 `issues`）把复核后的结论存成变量，供下游引用。
- 因为 inherit，它能看到上一个节点的完整上下文 —— 这是「自我复核」，成本低。

### 模式 C：对抗验证（换一个全新 Agent 来挑刺）
用一个 `ifElse`（如 `{{AgentCheck}} == "1"`）决定是否开启对抗验证：开启时，把上一步的结论（`{{issues}}`）喂给一个**全新会话的不同 Agent**（`sessionStrategy: new` + 不同 `agentType`），让它独立判断「这些问题是不是真的问题」。
- 这是「换个脑子复审」，比 Double Check 更强：新 Agent 没有前序上下文包袱，更容易发现误报。
- `yes` 分支 → 对抗验证 Agent → 汇合；`no` 分支 → 直接跳过，进入下一步。两分支汇合到同一下游节点（如「修复确认问题」）。

### 模式 D：执行节点用循环包裹（强烈推荐，默认就这么做）
**所有执行节点（prompt / clarify / shell）都尽量放进 `loop` 子图里，而不是直接挂在主图上。** 这是用户的强偏好：让每个执行步骤都具备「可重复 / 可重试 / 可迭代」的能力。
- **为什么**：单次 Agent 执行常常不收敛（review 没查全、修复没修干净、生成没达标）。用 loop 包住，就能多跑几轮直到满意，而不是一锤子买卖。
- **怎么选模式**：
  - **想固定跑 N 轮**（如「review→修复」循环 10 次）：`loopMode: "fixed"`, `fixedCount: N`。
  - **想跑到满足条件为止**（如「直到没有问题」）：`loopMode: "until"`, `untilCondition: "{{has_issues}} != \"1\""`（do-while，至少一轮，轮末求值），配 `maxIterations` 兜底。
- **嵌套**：外层 loop 控制「总迭代轮数」，内层 loop 控制「一轮内的重试/校验」，执行节点放在**最内层**子图里。demo 就是 `loop-1`(fixed 10) 包 `loop-6`(一轮的 review-修复) 包所有 prompt/ifElse。
- **读轮次**：子图内可用 `{{QUARTET_LOOP_INDEX}}`（0-based 当前轮，也用于模式 A 的「偶数轮」分流）。
- **提前跳出**：shell 里 `quartet_break`/`quartet_stop` 跳出当前循环、`quartet_return` 提前成功结束整个 run；until 循环则靠 `untilCondition` 自然收敛。
- **结构要点**（见 §2 loop / §3 scope）：loop 子图必须有恰一个 loop 内 start、≥1 个 loop 内 end，子图内的边不跨容器；主图只连 loop **容器节点本身**。

最小示例（执行节点被 loop 包裹，until 重试直到成功）：
```json
{
  "nodes": [
    { "id": "start", "type": "start", "layout": { "x": 40, "y": 160 } },
    { "id": "loop", "type": "loop", "config": { "loopMode": "until", "untilCondition": "{{ok}} == \"true\"", "maxIterations": 5 }, "layout": { "x": 300, "y": 40, "width": 820, "height": 360 } },
    { "id": "l_start", "type": "start", "parentId": "loop", "layout": { "x": 0, "y": 167 } },
    { "id": "work", "type": "shell", "parentId": "loop", "config": { "script": "echo work; quartet_set ok true", "outputVariables": ["ok"] }, "layout": { "x": 140, "y": 134 } },
    { "id": "l_end", "type": "end", "parentId": "loop", "layout": { "x": 758, "y": 167 } },
    { "id": "end", "type": "end", "layout": { "x": 1180, "y": 160 } }
  ],
  "edges": [
    { "id": "e1", "sourceNodeId": "start", "targetNodeId": "loop", "sourcePort": "default" },
    { "id": "e2", "sourceNodeId": "loop", "targetNodeId": "end", "sourcePort": "default" },
    { "id": "e3", "sourceNodeId": "l_start", "targetNodeId": "work", "sourcePort": "default" },
    { "id": "e4", "sourceNodeId": "work", "targetNodeId": "l_end", "sourcePort": "default" }
  ]
}
```
> 例外：`clarify` 节点不能放进 loop（必须在主图，见 §2）；纯路由的 `ifElse` 本身无所谓包不包，但它串联的执行节点应在 loop 内。

### 组合范式（这几者怎么串起来）
外层 `loop`（控制总轮数）→ 内层 `loop`（一轮内可重试）→ 内层子图里：
```
模式A(多Agent选择) → review 出结论 → 模式B(Double Check, inherit, 存 {{issues}})
  → 模式C(对抗验证 ifElse) ──yes──> 全新Agent复审 ──┐
                          └──no──────────────────────┴─> 修复确认问题(inherit) → end
```
（整段都跑在模式 D 的 loop 包裹里 —— 这正是 demo 的形态。）
配套变量（按需放进 `variables`，可用 `disabledVars` 临时关掉）：
- `MultiWorker`（`"0"/"1"`）：是否启用多 Agent 分流（模式 A）。
- `AgentCheck`（`"0"/"1"`）：是否启用对抗验证（模式 C）。
- `PR` / `Doc` / `Code`：被 review 的 PR 地址、技术文档目录、代码目录。
- `Notice` / `Info`：给 Agent 的额外提示位（常放 `disabledVars` 里默认关闭）。

> 想直接复用：`qwf create --name "代码 Review" --config-file references/code-review-demo.json`（记得先按需改 `PR`/`Doc`/`Code`/`workdir`/`workspaceId`）。也可以先 `qwf validate --config-file references/code-review-demo.json` 看一眼。
