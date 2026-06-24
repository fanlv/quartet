# 技术方案：工作流图执行引擎（runloop 升级）

## 背景与目标

现有「Loop 任务」引擎（`services/job`）是 `FlowNode` 树（step/group），深度优先严格串行：`[]int` 路径作主键管 resume/progress，单会话靠 `roundMode` 决定新建/复用/重置，变量是全局 map、`{{变量}}` 单次替换、仅 Shell 可写回，循环靠 group iterationCount，评估输出 STOP 跳出 group。树形串行表达不了「一结点多后继/多前驱」，撑不起并行与分支。

**目标**：新增独立的**图（nodes+edges）执行引擎**（`graph` 模块），与 Loop 并存：

- 业务结点 Shell/Prompt/评估/If-Else/循环 + 控制结点 start/end。
- **并行**：一结点多出边、下游真并发跑 Agent。**汇总**：多入边等全部入边解析后，≥1 激活则执行、全剪枝则自身剪枝并向下传播。
- **条件分支**：If-Else 只走 yes 或 no。**循环**：容器内子图按固定次数/直到条件重复。
- **变量传递**：Shell/Prompt/评估可声明输出变量供下游 `{{变量}}` 引用；If-Else 只路由、start/end 不产出、循环产出为轮末快照。
- **对齐 Loop 体验**：Graph 配置覆盖 Loop 全部配置管理能力；Chat 页用独立 GraphLoop 组件承载运行展示与「编辑/暂停/步骤后停止」控制。

> **独立性约定**：旧 Loop 执行链路（`services/job`）与配置入口（`LoopConfigPanel`）保留，存量任务继续走旧逻辑。新能力落在独立 `graph` 模块与独立 React Flow 画布入口，两套引擎/数据模型/配置入口物理隔离，无共享可变状态、无分流、无灰度。GraphLoop 只消费 GraphRun 状态。

## 范围

**做**：图数据模型 + 就绪队列调度器；按现有分层做图配置/运行记录/实例状态/变量快照/会话血缘持久化；真并发（并发度可配）、拓扑并行汇总、剪枝传播的条件分支、子图重复的循环；结点级与 job 级超时兜底；可执行结点产出变量；React Flow 画布独立入口并适配手机端；Graph 配置管理对齐 Loop；Chat 页按 job 类型接入 GraphLoop。

**不做**：特殊汇聚策略（race/vote/N 选 K，只支持等全部入边解析）；循环内变量按轮隔离（确认不隔离，见 §3）；输出变量的多行/结构化值（只支持单行标量）；Graph 类型 Job 的公开分享（本方案明确不覆盖 `/api/v1/public/*`，仅旧 Loop/Job 保留分享）；旧 Loop 配置迁移到 Graph（用户手动在 Graph 画布重建）。

## 术语

- **GraphWorkflow**：可编辑/保存/导入导出的图工作流配置。
- **GraphRun**：一次运行记录，启动时持有基线快照；运行中允许对未开始部分做版本化编辑，后续调度按最新有效版本执行，已开始/已完成实例保持其执行时版本。
- **GraphLoop**：Chat 页运行区的 Graph 运行展示与控制组件，与旧 Loop 运行区并列。
- **Instance（实例）**：某结点在一次 run 中的执行实例，由「结点 ID + 循环迭代上下文」唯一标识。
- **业务结点**：Shell、Prompt、评估、If-Else、循环；**控制结点**：start、end。

**GraphRun 与 Job 关系**：GraphRun 是 Graph 类型 Job 的一次执行明细与快照；用户侧以 Job 作为运行入口、Chat 运行区与运行控制的承载对象。每次启动 GraphWorkflow 创建一个 Graph 类型 Job 并绑定一个 GraphRun；启动、停止、暂停、步骤后停止、续跑与查看状态都落到绑定 GraphRun。删除 workflow 不影响已绑定 Job 的 GraphRun 运行态。

## 用户流程

1. 进入 Graph 画布入口，新建或打开 GraphWorkflow。
2. 添加 start/end 与业务结点，编辑配置、连线、变量表、运行配置、会话策略。
3. 保存 workflow；后端一次返回全部校验错误并定位到结点/边/变量/全局配置，成功后更新快照。
4. 从 Graph 入口或 Chat 页启动；后端创建 GraphRun，固化 workflow/布局/变量/workdir/运行配置/会话策略/被引用 Agent 与模型配置的基线快照。
5. 运行中展示整体进度、结点状态、当前位置、已走分支、join 等待、循环轮次、日志和错误。
6. 运行中可编辑尚未开始部分：修改结点 Prompt、AgentType、模型配置等参数，也可新增/删除尚未调度到的结点和连线；保存后形成新的 run 图版本，后续未开始实例按新版本执行。
7. 可硬停止/暂停/步骤后停止，三者均保留状态供续跑；续跑基于已持久化实例/边状态、变量快照、循环上下文、会话血缘和最新有效图版本重建 ready 队列。
8. Graph 类型 Job 可打开其绑定 GraphRun 查看当前状态、进度、日志和错误；可显式删除非在飞 GraphRun/Job，删除时级联清理其全部持久化产物。

## 方案设计

### 1. 数据模型

**工作流图** = 结点列表 + 连线列表 + 初始变量表 + 画布视图状态。

- 结点：稳定 ID、类型、标题、类型专属配置、可选输出变量名、可选 `_last_assistant_msg` 别名变量名、可选父容器 ID、布局元数据。
- 连线：起点、终点、可选出端口 yes/no、连线元数据。
- 初始变量表：沿用 variables/disabledVars；所有变量统一按 `string` 处理。
- 布局元数据只用于展示、不参与执行语义；保存重开、运行回放都须稳定还原位置/连线/视图。

**工作目录与沙箱**：沿用 Loop——整个 run 绑定一个 workspace，所有 Shell/Agent 结点默认在该 workdir（及 sandbox）执行，快照固化 workspace 与 workdir。本引擎引入真并行，多结点可能同时读写同一 workdir；不做目录隔离（单用户、单机/沙箱前提），但 UI 须提示「并行结点共享同一工作目录，同一文件并发写由用户自行规避（串行化连线或写不同子路径）」。Shell 临时文件复用 Loop 清理机制。

**变量类型**：初始变量与输出变量只有 `string` 类型。下游文本替换、条件比较、循环直到条件均按字符串处理，不做 number/bool 解析，也不做隐式真假转换。`>`/`>=`/`<`/`<=` 按字符串 Unicode 码点字典序比较（Go 原生比较）。

**输出变量契约**：

- Shell 兼容现有 `QUARTET_CONTROL` 控制文件协议。执行 Shell 前继续注入内置 helper：
  - `quartet_set "变量名" "值"`：向控制文件写入 `B64:<变量名>=<base64值>`，Graph Shell 节点从控制文件解析并写入同名输出变量。
  - `quartet_break` / `quartet_stop`：向控制文件写入 `STOP_LOOP`。
  - `quartet_return`：向控制文件写入 `STOP_WORKFLOW`。
- Shell 仍可兼容控制文件中的明文 `key=value` 行；`B64:key=value` 优先用于安全承载空值、空白、等号等特殊字符。Graph 不要求 Shell 用户在 stdout 里打印 `QUARTET_OUTPUT`，推荐继续使用 `quartet_set`。
- Prompt/评估没有控制文件，使用模型原始输出中的 marker 协议：声明后须为每个变量给至少一行 `QUARTET_OUTPUT:<变量名>=<值>`，同名多行取最后一行。Graph 在 prompt 后追加固定协议后缀，引导模型逐行输出声明变量。
- `QUARTET_OUTPUT` **行内子串匹配**：在每行内任意位置查找 `QUARTET_OUTPUT:` 标记，标记前的内容忽略，无需出现在行首；即便标记被粘在前面文字之后（如 `2QUARTET_OUTPUT:answer=2`，例如模型分两条消息输出被拼接）也能识别。
- 变量名 `[A-Za-z_][A-Za-z0-9_]*`；`QUARTET_OUTPUT` 取标记之后到行尾的内容，按首个 `=` 切分，变量名 trim，值原样保留（可空、可含 `=`、不 trim）；只支持单行标量。`quartet_set` 的值由控制文件 base64 解码后原样保留。
- **保留命名空间**：`_last_assistant_msg` 及一切 `_` 开头名为内置保留名，输出变量与初始变量均不允许声明（保存失败并定位）；运行输出写保留名判结点失败全量报错。
- 结点配置内输出变量声明不允许同名重复（保存失败并定位）。
- 每个 Shell/Prompt/评估结点可选配置一个 `_last_assistant_msg` 别名变量名。该别名不需要 Shell `quartet_set`，也不需要 Prompt/评估模型输出 `QUARTET_OUTPUT`；结点执行完成后，调度器自动把本结点 `_last_assistant_msg` 的同一份内容写入该别名变量，供下游用 `{{别名}}` 引用。
- 声明了输出变量的节点必须产出所有已声明变量；产出未声明变量、缺任一已声明变量、变量名非法、值跨行均判结点失败全量报错。Shell 错误含 stdout/stderr/exit code/控制文件内容或解析错误；Prompt/评估错误含模型原始输出与解析错误详情。
- Shell 的 `STOP_LOOP` / `STOP_WORKFLOW` 是 Graph Shell 节点运行期控制信号，不属于旧 Loop 配置迁移：`STOP_WORKFLOW` 使当前 GraphRun 立即按成功提前结束并停止后续调度；`STOP_LOOP` 只允许在循环容器内使用，使当前循环容器结束并激活循环节点后继，容器内尚未执行的实例按剪枝处理；在非循环内触发 `STOP_LOOP` 判节点失败并完整报错。

**结点语义**：

- **Shell/Prompt**：复用现有单结点执行，输出变量可选，可选结点级超时。Prompt 属 Agent 类、参与会话策略。
- **评估**：跑 Agent 做判断的专用结点，属 Agent 类——强制 ≥1 输出变量、禁分支出边、UI 以判断/决策呈现、prompt 追加判断后缀；产具名变量供 If-Else/循环「直到条件」消费（弃用旧 `LOOP_DECISION:STOP`）。
- **If-Else**：配一个条件表达式，真激活 yes、假激活 no，另一条剪枝；只路由不产出。
- **循环**：容器，内部子图按父容器 ID 关联，按固定次数/直到条件重复，每轮是子图结点的全新实例。
- **start/end**：控制结点（均可多个），不执行/不产出/不计 progress 分母；start 无入边须有出边，end 无出边须有入边。

**结点配置清单**（只描述功能字段，结构由 `types/model` 承载）：

| 结点类型 | 用户可配置项 |
| --- | --- |
| Shell | 标题、Shell 脚本、输出变量声明、`_last_assistant_msg` 别名变量名、结点级超时、布局元数据。脚本在快照固化的 workdir/sandbox 下执行，并注入兼容旧 Loop 的 `quartet_set`/`quartet_break`/`quartet_return` helper。 |
| Prompt | 标题、提示词、Agent/模型配置引用、会话策略（新建/继承上游）、输出变量声明、`_last_assistant_msg` 别名变量名、结点级超时、布局元数据。 |
| 评估 | 标题、判断提示词、Agent/模型配置引用、会话策略、≥1 输出变量声明、`_last_assistant_msg` 别名变量名、结点级超时、布局元数据。 |
| If-Else | 标题、条件表达式、yes/no 两个出端口、布局元数据。 |
| 循环 | 标题、循环模式（固定次数/直到条件）、固定次数、直到条件表达式、最大次数兜底、内部子图父容器关系、布局元数据。 |
| start/end | 标题、布局元数据；不允许业务动作、输出变量、yes/no 端口。 |

**运行配置**（保存 workflow 与启动 run 时均校验，超出边界一律保存报错）：并发度（默认 4，1 退化串行，最大 16，>0）、默认结点超时（300s，0 不限，负数非法）、job 总超时（0 不限，负数非法）、循环最大次数兜底（100，最大 1000，>0）、单 run 实例总数上限（100000，可覆盖，>0）、快照累计体积上限（1 GiB，可覆盖，>0）、workspace/workdir/sandbox 引用。

**实例规模兜底**：嵌套循环 × 并行分支会让实例数与快照（含 `_last_assistant_msg` 等模型完整输出）乘积式膨胀，单机/沙箱下可能撑爆磁盘/内存。实例总数或快照体积任一上限被突破即按 job 失败终止，取消全部在飞结点并全量说明触顶项、当时数值与上限。

**条件表达式**（If-Else 与循环「直到条件」共用）：对当前结点可见变量求值的布尔表达式，不直接调 Agent（需 Agent 判断时由上游评估结点先产变量）。保存时静态语法校验，运行时校验变量存在性，错误全量返回。所有比较值都是字符串。

- **文法**：表达式 = `且`/`或`/`非` 与 `()` 组合的布尔项；布尔项 = 比较式 / 括号子表达式。不支持单独写 `{{变量}}` 作为真值判断，必须显式比较。
- **比较式**：左值 操作符 右值，可选比较选项。左值/右值可为变量引用或字符串字面量；操作符支持 `==`/`!=`/`>`/`>=`/`<`/`<=`/`StartWith`/`EndWith`。
- **比较选项**：每个比较式可独立配置「忽略大小写」「忽略空格」。忽略大小写时比较前对两侧做大小写折叠；忽略空格时比较前移除两侧所有 Unicode 空白字符；两项可同时开启，开启顺序固定为先去空白、再大小写折叠，保证可重放。
- **优先级**（高→低）：`()` > `非` > 比较 > `且` > `或`，同级左结合；保存校验括号配对。
- **字面量**：字符串用英文双引号（支持 `\"`/`\\` 转义、不跨行）。不支持 number/bool 字面量，用户需要判断数字或布尔时按字符串显式比较，例如 `{{flag}} == "true"`。
- **输入边界**：逻辑操作符只支持中文 `且`/`或`/`非`；变量引用必须写成 `{{变量名}}`；字符串外空白可忽略；出现未知 token、非法转义、未闭合字符串或未成对括号时保存失败并全量定位。

**变量引用语义**（替换与求值通用）：

- 文本替换（Prompt/Shell）：未知变量、被剪枝未产出变量保留 `{{变量}}` 字面量不报错；被禁用变量替换为空串。替换在结点开始执行那刻对其快照做，不对解析值二次替换。
- 条件求值：未知变量、被剪枝未产出变量按未知处理运行失败；被禁用变量参与条件时按空字符串参与字符串比较。
- **`_last_assistant_msg`**（内置）：默认写结点原始最终输出——Prompt/评估为模型原始输出，Shell 为 stdout 全量（不含 stderr，无输出为空串）。取值以 §3 实例快照血缘为准（非全局「时序最近」）：单入边继承上游快照值；多入边 join 按已激活入边「上游结点 ID 升序的末位」取值（不依赖到达顺序、被剪枝上游不参与），保证多次运行与续跑取值一致可重放；需稳定引用某上游应配明确输出变量名。条件中整体按字符串参与（多行/大文本不切分不截断），支持所有字符串比较操作符和比较选项。
- 配了输出变量名时，Shell 具名变量写控制文件解析值，Prompt/评估具名变量写 `QUARTET_OUTPUT` 解析值；配了 `_last_assistant_msg` 别名时，别名变量由调度器自动写入本结点原始最终输出。下游 `_last_assistant_msg`、别名变量与具名输出变量皆可读，值来源不同。

**图合法性规则**（保存时校验、全量提示违规项）：

- 结构：≥1 start；≥1 个从 start 可达的 end；≥1 个从 start 可达的业务结点或循环容器（禁纯 `start→end`，避免分母为 0）；start 以外不可达结点不允许保存。
- 控制结点：不允许配业务动作/输出变量/yes-no 端口；start 无入边须有出边，end 无出边须有入边。
- 普通可执行结点：须有入边（仅 start 与循环子图入口可无入边）与出边；所有路径须到达 end，无隐式终点。
- 环：非循环不允许成环，环只能由循环容器表达。
- 端口：If-Else 必须配 yes/no 两出端口，普通结点不允许用 yes/no 端口。
- 评估结点：强制声明 ≥1 输出变量，且不允许使用 yes/no 分支出端口（违反即保存失败并定位）。
- **循环子图边界**：边不得跨容器（容器外只连循环结点本身，内部子结点只互连或连容器内 end）；内部不放 start，内部入口是该容器内唯一一个无入边的业务结点或嵌套循环容器；内部 end 用带父容器 ID 的 end 结点（只终止当前容器内路径）；每个子图恰好一个入口、≥1 内部 end、所有内部路径须到达内部 end；嵌套循环按父容器 ID 分层、规则一致。
- **并行/汇总无专用结点**：并行=多出边，汇总=等全部入边解析后判定，跨分支信息靠变量传递。
- **输出变量写冲突**：同一容器作用域内，两结点若无祖先/后代关系且不在同一 If-Else 的 yes/no 互斥分支内即视为可能并行激活，不允许声明同名输出变量；`_last_assistant_msg` 别名变量按输出变量参与同名冲突校验，且不得与本结点显式输出变量重名；多 start 间默认可能并行；join 后共享下游只校验下游自身声明；循环同一结点跨轮写同名允许覆盖，同一轮内可能并行的不同结点仍禁止；同一 If-Else 分支内按祖先关系与嵌套分支递归校验。
- **输出变量与初始变量同名**：允许，运行时按血缘快照覆盖。
- **保留变量名**：输出变量、`_last_assistant_msg` 别名变量或初始变量声明命中保留命名空间（`_last_assistant_msg` 及 `_` 开头名）均保存失败定位。

### 2. 执行引擎：就绪队列调度器

从零实现，不复用 Loop 递归树 walk：

- **调度**：每结点维护「未满足入边计数」，入口先就绪；结点完成后把出边标记激活/剪枝，下游计数减一、归零入队。start 直接激活出边；end 入边解析完只记录路径终止、不激活后继。
- **并发**：就绪结点并发执行，并发度是**整个 run 的全局上界（单一信号量）**，主图与所有（含嵌套）循环子图共享同一额度。**循环容器/start/end 不占名额**——循环容器只驱动子图、子图运行期间不持信号量，否则并发度=1 或嵌套时会「父握名额等子图、子图拿不到名额」死锁。
- **分支与剪枝传播**（防 join 死锁）：边三态 = 待定/已激活/已剪枝；分支结点只激活一条出边、另一条剪枝；结点全入边解析后，≥1 激活则执行、全剪枝则自身剪枝并向下传播。
- **执行超时兜底**：结点级超时（默认 300s、可覆盖、0 不限）超时取消上下文、按失败处理并触发失败传播、全量说明时长；job 级总超时（默认 0 不限）超时按 job 失败、取消全部在飞结点。取消遵循 §4「在飞 Agent 取消」（ACP 只 cancel 不 reset）；超时不触发瞬态重试。
- **终止语义**：队列空即自然结束（到达某 end 只终止该路径，其余跑到队列空）。无失败→成功；任一失败→整个 job 失败并取消其它在飞结点，已入队未开始和未来不会执行的业务实例保持待定并记录阻断原因、不计分子，已终态实例不改写。**特例**：结束时无任何 end 被激活判 job 失败并说明「无终点到达」。主动终止信号三类：硬停止、暂停（优雅停止）、执行超时。
- **步骤后停止**（GraphLoop）：「步骤」= 当前 ready 批次。每次从 ready 队列取可调度实例时生成批次 ID，持久化批次 ID、成员、成员状态（未启动/运行中/终态）；触发后冻结当前批次、进入「步骤停止中」、不再生成后续批次，已在飞的跑到终态、同批次未启动成员按并发额度继续启动；本批次全部成员终态后进入「已步骤停止」，未进入本批次的实例保持待定。续跑清除批次冻结状态、从已解析入边重推队列。循环内触发停在当前批次边界、不强制等完整轮次。
- **瞬态错误重试**：沿用 Loop——网络重置/HTTP2 流错误重试 2 次（固定退避），限流/额度错误重试 3 次（指数退避）；重试在结点内部完成，不改边状态与 progress 终态。

**循环子图驱动**：

- 循环结点就绪时启动子图，子图结束后激活出边继续主图。
- 固定次数 ≥ 0，为 0 跳过子图直接激活出边；「直到条件」先跑一轮再轮末求值（**do-while，至少一轮**；需「不满足则一轮不跑」由用户在循环前用 If-Else 旁路）。
- 「最大次数兜底」始终生效（默认 100、最大 1000，非法值或固定次数大于兜底时保存失败）。固定次数跑满即正常结束；直到条件达最大次数仍未满足则 job 失败并说明「循环达最大次数但条件未满足」。
- 每轮从唯一内部入口启动，所有内部路径到达内部 end 或被剪枝后才判「直到条件」或进下一轮；全部内部路径被剪枝时本轮仍正常结束。
- **循环对外快照**：固定次数 0 继承循环入口快照；正常执行 ≥1 轮继承最后一轮轮末快照；直到条件首轮即满足继承首轮轮末快照；某轮无任何内部 end 被激活时该轮轮末快照沿用该轮入口快照；循环失败或 job 级取消不激活出边、不产新对外快照。
- 循环内任一结点失败触发失败传播；被剪枝结点按跳过计入 progress。

### 3. 三块基础设施

借鉴 Loop 思路，新模块独立实现。

**① 实例标识**：用「结点 ID + 循环迭代上下文」作执行实例稳定键（不再用 `[]int` 路径），Progress/Resume/回填均以实例键索引，天然支持并行、多前驱与嵌套循环。

**② 会话血缘（沿边传递）**：Agent 类结点指 Prompt 与评估；Shell/If-Else/循环/start/end 不参与会话策略。

- Agent 类结点声明会话策略——新建，或继承上游。继承上游对单入边与多入边均允许。
- 并行分叉时每条分支复制上游上下文后**新建独立会话**（不复用 session ID 避免污染）。
- **多入边 Agent 继承**：多入边 join 配继承时，复制**已激活入边中「上游结点 ID 升序末位」**的那条上游会话（与 `_last_assistant_msg` 同一可重放规则、不依赖到达顺序、被剪枝上游不参与），其余已激活上游的输出变量仍照常 join 合并进可见快照、只是对话上下文不进入新会话；对话历史天然不可多份合并，故只继承一条上游。
- **保存期约束**：「每条 start 链路首个可执行 Agent 必须新建会话」——首个 Agent 没有上游会话可继承，配继承则保存报错。该规则同时兜底多入边继承：若某 join Agent 的**任一**入边路径只穿过非 Agent 结点（该路径无上游会话），它即为某条 start 链路的首个 Agent，配继承即保存失败并定位。运行时不依赖 roundMode。
- **「复制上游上下文」** = 复制对话历史、模型配置、工作目录、系统提示词、可恢复所需 agent 元数据与工具可见上下文，据此建新后端 session；不复制运行中 turn 状态、不复用原 session ID、不复制进程内临时句柄。ACP 不支持直接 fork 时用持久化历史消息 replay 出等价 session，replay 失败作结点启动失败全量返回。

**③ 变量产出与可见性**：

- **可见性**：唯一读语义来源是「每个执行实例的可见变量快照」，无并行可写全局 map。start 快照来自初始变量表；单入边继承上游完成后快照；多入边 join 在全入边解析后合并已激活上游快照（被剪枝入边不参与）。变量只沿连线向下可见，**无边约束的并行结点间不保证互相可见**，需传递的信息应经显式连线汇聚后再读。
- **join 合并**：不同上游写不同变量取并集；同名变量在同一可并行区域已被 §1 禁止；循环不同轮次取最新轮次；`_last_assistant_msg` 按已激活入边「上游结点 ID 升序的末位」取值。
- **并发写安全**：产出由调度器在完成回调里**串行落库（单写者）**，物理落库顺序不影响快照。
- **写冲突/剪枝**：写冲突规则见 §1；被剪枝分支输出变量永不写入，需默认值由用户在更上游显式初始化。
- **循环内变量沿轮次累积、不轮间隔离**：每轮唯一内部入口实例继承「上一轮累积快照」（首轮继承循环入口快照），轮末写回累积快照供下一轮与「直到条件」读；轮间清理由用户在循环末尾结点显式置空。
- **循环轮末快照**：由本轮所有已激活内部 end 的可见快照按 join 规则合并（被剪枝路径不参与）；同轮多路径写同名按 §1 保存时禁止。

### 4. 状态、持久化与可靠性

沿用 Loop「写顺序不变量、resume 恰好推进一次、跳过去重、回填仅用于展示」理念，主键改用实例键。

- **Progress**：按实例键记录状态（待定/运行中/成功/失败/跳过/中断）。业务结点与循环容器计入分母，start/end、循环内部 end 不计。已进入分母的实例被剪枝记跳过计分子、失败记失败计分子；尚未实例化、因分支剪枝或循环早停确定未来不会执行的实例不创建状态而从分母扣除；硬停止或失败传播取消的运行中实例记中断、不计分子、续跑可重调度。
- **终态分类（续跑/恢复唯一口径）**：**不可重置终态**＝成功、跳过（结果可靠，绝不重跑）；**可重置终态**＝失败、中断（产出不可靠或被打断，续跑/恢复时重置为「待定」、清空已写输出变量与下游边状态后重新调度）。「运行中」不是终态，恢复时一律按中断处理。
- **可重置状态的传播范围**：以失败实例、硬停止/超时/崩溃导致的中断实例、恢复时由运行中转中断的实例为重置起点，递归清空这些实例向下游发出的边状态、由这些边触发的待定/中断实例、join 入边计数派生状态、剪枝结果与相关分母修正；已成功/跳过的实例及其可靠输出不重跑、不清空。失败传播仅取消当时正在运行的实例并记中断；已入队未开始和未来不会执行的实例保持待定记阻断原因、不作重置起点。若 join 同时依赖已成功上游和被重置上游，保留已成功上游入边结果，只等被重置上游重新解析后再决定是否入队。循环内重置只影响对应迭代上下文及其下游迭代派生状态，不回滚其它已成功/跳过迭代。
- **分母回算**：以运行时实例状态为唯一口径（不取「全部可能路径之和」，否则分支只走一条、循环早停到不了 100%）。
  - 启动给静态合法上界：主图按可达业务结点去重计数（diamond 汇聚只计一次）；If-Else 只按条件可达一侧计入（静态判不出取互斥独占侧较大者，公共结点单独计一次）；循环按「容器 1 + 子图业务结点数 × 最大次数」，0 次只计容器自身。
  - 运行中每当边解析或轮次结束按实例状态修正：已纳入分母的实例被剪枝记跳过计分子；尚未纳入分母、且因剪枝确定未来不执行的实例直接从预估分母扣除不创建状态；直到条件早停扣剩余轮次；多 start/多 end/共享下游按实例键去重（共享下游只在全部入边解析后创建一个实例计一次）。
  - 失败/停止保留当时分子分母不强行回算 100%，续跑沿用已持久化分母继续修正；自然完成保证分子=分母。
- **运行配置与版本化编辑**：GraphRun 启动持久化基线快照（nodes/edges/初始变量/disabledVars/布局/workspace 与 workdir〔及 sandbox〕/运行配置〔并发度、结点级与 job 级超时、循环最大次数兜底、实例总数与快照体积上限〕/会话策略/被引用 Agent 与模型配置内容）。运行中保存编辑时，不直接覆盖基线快照，而是追加一个 GraphRun 图版本，版本内容包含完整 nodes/edges/布局、结点配置、会话策略，以及被引用 Agent/模型配置的内容快照。
  - **可编辑范围**：允许修改尚未开始结点的 Prompt、Shell 脚本、评估提示词、AgentType、模型配置、会话策略、输出变量声明、`_last_assistant_msg` 别名变量名、结点级超时与布局；允许新增结点/边；允许删除尚未开始、未完成、未产生可靠输出且没有已完成下游依赖的结点/边。
  - **不可编辑范围**：已运行中、成功、跳过的实例及其执行时配置不可改写；失败/中断实例若要修改配置后重跑，必须先触发“重置该实例及其下游可重置状态”，清空其不可靠输出和下游边状态后再保存新版本。已完成实例的输出变量、会话血缘、日志和错误永远按执行时版本展示。
  - **图结构编辑生效点**：调度器每次生成 ready 批次前读取最新有效图版本。已进入当前 ready 批次或正在运行的实例继续使用批次冻结时的版本；新增结点/边只有在其上游边状态能从当前持久化状态推导时才进入调度；删除未开始结点会同步重算相关入边计数、剪枝和 progress 分母。
  - **模型配置编辑**：运行中改结点 AgentType/model 等配置时，保存版本时按内容固化；后续未开始实例使用该版本内容。仅修改全局模型配置不会自动影响运行中 GraphRun，必须通过 GraphRun 编辑保存一次才生成新版本。
  - **校验要求**：每次运行中编辑保存都对“最新图版本 + 已持久化运行状态”做全量校验，返回所有错误。若编辑会断开已完成实例到未完成下游的必要路径、让已完成输出变量引用失效、删除已完成实例依赖的边，保存失败并定位。
  - **运行版本展示**：每个实例状态记录执行时的图版本号；查看绑定 GraphRun 时可按版本展示对应布局和配置。删除 workflow 不删既有 run 的基线快照、图版本和运行记录，除非用户显式删除该 run。
- **Resume/续跑**：持久化已完成实例集合、实例状态、边三态、剪枝结果、循环当前/已完成轮次、实例可见快照、已写输出变量、各血缘会话、当前分子分母；续跑据此重推就绪队列，不可重置终态不再调度，可重置终态先重置为待定再调度，只调度入边已解析的实例。
- **恢复语义**：续跑从盘上状态重建就绪队列，运行中实例与可重置终态按重置规则重跑，不可重置终态不重复执行。
- **停止/暂停**：硬停止取消上下文、运行中实例标记中断；暂停=优雅停止（不再调度新结点、等在飞实例结束、未调度实例保持待定）；步骤后停止是优雅停止的批次边界变体（§2）。三者均保留状态以续跑。
- **在飞 Agent 取消**（含失败传播/超时）：内置（eino）Agent 直接取消上下文；ACP Agent 仅 cancel/中断当前 turn、**绝不 reset/销毁 session**（reset 会丢全部上下文），中断后实例标记中断、子进程归还进程池，续跑在保留会话血缘上重跑。

**GraphRun 状态机**：

| 状态 | 进入条件 | 可续跑 | 用户可见说明 |
| --- | --- | --- | --- |
| 待运行 | 已创建但调度器未开始 | 是 | 等待启动或排队中。 |
| 运行中 | 调度器已启动且仍有待解析边/ready 队列/在飞实例 | 否 | 实时 progress、当前结点与日志。 |
| 成功 | 队列清空、无失败、≥1 个 end 被激活 | 否 | 完成态，分子=分母。 |
| 失败 | 任一结点失败、条件/变量/会话错误、无 end 被激活、直到条件达最大次数未满足 | 是 | 失败原因 + 可续跑入口；可重置终态重置后重跑。 |
| 暂停中 | 触发暂停，停调度等在飞结束 | 否 | 正在优雅暂停。 |
| 已暂停 | 暂停完成，未调度实例保持待定 | 是 | 可续跑，从已解析入边重推 ready 队列。 |
| 步骤停止中 | 触发步骤后停止，当前冻结批次继续 | 否 | 等待持久化批次全部成员终态。 |
| 已步骤停止 | 当前冻结批次结束、后续未调度 | 是 | 可续跑，清除批次冻结状态后从持久化状态继续。 |
| 已硬停止 | 触发硬停止并取消在飞实例 | 是 | 运行中实例标记中断；续跑可能重跑未落终态实例。 |
| 已超时 | job 总超时并取消全部在飞实例 | 是 | 超时时长与取消详情；续跑按中断实例规则处理。 |
| 恢复中 | 进程重启后从盘上状态重建 ready 队列 | 否 | 恢复中；未落终态运行中实例先标记中断。 |

失败、已暂停、已步骤停止、已硬停止、已超时都保留快照与实例状态；续跑在同一 run 上继续推进，不创建新 workflow。用户想用编辑后的 workflow 运行须启动新 GraphRun。

**错误展示规格**：

- 保存/导入校验错误：一次返回全部错误，含错误类型、可读信息、关联结点/边 ID、变量名或全局配置键；前端定位到对应元素。
- 结点执行错误：含 GraphRun ID、实例键、结点 ID/标题/类型、错误原因、是否可续跑；Shell 必含 stdout/stderr/exit code/重试次数；Prompt/评估必含模型原始输出与解析错误详情。
- 条件/变量错误：含表达式文本、变量名、实际值、未知/被剪枝/被禁用状态、比较操作符与比较选项。
- 会话错误：含会话策略、新建/继承/fork/replay 所在结点、上游实例键、ACP/eino 错误原文；ACP 取消不得 reset。
- API 错误不得只给摘要；前端可折叠长 stdout/stderr/model 输出，但必须能展开看完整内容。GraphRun 保留当时错误详情。

### 5. 持久化与接口分层

严格按现有分层，handler 不直接读写文件：

- **`types/model`**：GraphWorkflow、GraphNode、GraphEdge、GraphNodeConfig、GraphCanvasLayout、GraphRun、GraphRunSnapshot（含被引用 Agent/模型配置内容）、GraphInstanceState、GraphProgress、GraphResumeState、GraphValidationError、Graph API Request/Response。
- **`types/path`**：graph 配置/运行记录/实例状态/事件日志文件的路径拼接。
- **`repository`**：graph workflow CRUD、运行记录与不可变 run 快照读写，实例/边状态、变量快照、会话血缘、progress 原子持久化，事件追加，GraphRun 全部产物级联删除。
- **`services/graph`**：图合法性校验、条件表达式解析求值、变量快照合并、调度执行、循环驱动、resume、停止控制。
- **`cmd/web/handler`**：参数解析、鉴权后服务调用、错误全量返回、响应格式化；路由统一注册在 `cmd/web/api.go`。

### 6. 前端与响应式（手机端）

独立 React Flow 画布入口，桌面/窄屏双适配：

- **画布**：桌面拖拽编排；手机端触摸手势（单指平移、双指缩放）、保留缩放/适配按钮、窄屏隐藏小地图等次要控件。
- **结点编辑/变量表**：桌面侧边 inspector（各结点只展示自身配置项），手机端改底部抽屉/全屏面板、单列布局、触摸尺寸。
- **添加结点**：手机端无原生拖拽，改「点选结点类型 → 画布点击放置」，连线用点选起点/终点辅助。
- **运行查看**：结点状态高亮，运行日志窄屏单列纵向滚动。
- **配置管理**：支持复制、导入、导出、重置、未保存提示和校验错误定位，保存内容为 Graph 配置。
- **Chat 页兼容**：运行区按 job 类型选择旧 Loop 组件或 GraphLoop。GraphLoop 展示整体完成度与运行中/成功/失败/跳过/中断结点，并尽量在图视角标出当前结点、已走分支、循环轮次与 join 等待；无法展示完整画布时至少以列表/路径摘要表达当前位置。旧 Loop 任务仍用旧组件与旧语义。
- **运行控制**：GraphLoop 保留「编辑/暂停/步骤后停止」入口。暂停=后端优雅停止；步骤后停止冻结并持久化当前 ready 批次、进入「步骤停止中」、批次全部成员终态后进入「已步骤停止」；运行中编辑保存为 GraphRun 图版本，已完成/运行中/当前冻结批次实例不受影响，后续未开始实例按最新有效版本执行。
- 采用响应式断点（与现有约定一致），不依赖鼠标 hover。

## 风险

- **并发竞态**：变量单写者串行落库，progress 按实例键独立更新。
- **剪枝遗漏致 join 永久等待**：边三态机 + 全入边剪枝则自身剪枝，单测覆盖 diamond/嵌套分支。
- **循环+并行嵌套致实例键膨胀**：实例键设计覆盖嵌套并加单测保证唯一可恢复；另设 run 级实例总数与快照体积兜底（100000 / 1 GiB），触顶按 job 失败终止并全量说明。
- **Agent 会话 fork/cancel 行为不一致**：会话血缘持久化复制内容，ACP 不 reset；fork/replay/cancel 失败均完整报错并以续跑用例覆盖。最高风险假设（复制上下文新建独立 session、ACP replay 等价 session）由步骤 0 spike 前置验证，失败按步骤 0 回退顺序确定正式会话血缘方案。
- **重复副作用**：不变量是「已落不可重置终态不重跑、可重置终态/运行中实例可能重跑」，故 Shell 等带副作用结点在「执行完但状态未落库即被取消/崩溃」时可能重复一次；Shell 完整展示次数与 stdout/stderr/exit code，UI 提示带副作用命令需自行保证幂等。
- **模型输出协议不稳定**：Prompt/评估用统一 marker 协议，缺失/重复/未声明/变量名非法均失败并展示原始输出。

## 可观测性

- 统一用 `pkg/logger` 打点：结点就绪/开始/完成、边激活与剪枝、循环每轮开始与终止判定、join 等待与触发、会话新建与继承、变量写入、并发入队出队、结点/job 超时；失败全量输出错误。
- SSE 按实例键上报结点状态变化，前端画布据此高亮运行中/完成/失败结点。

**状态：已完成**。后端运行日志已覆盖 GraphRun 生命周期、调度、并发、结点执行、边解析、join、循环、变量写入、会话、运行控制、版本更新、超时与失败；SSE 事件流已覆盖运行态与运行查看所需的实例状态、边状态、变量、循环、进度、日志与错误。Prompt/评估结点执行真实 Agent 时，Graph 现在会把 Agent 消息、思考、工具调用参数/结果/结束状态与 token usage 追加为 GraphRun 事件并通过 SSE 续传；Graph 类型 Job 的 Agent 结点用量也已接入 `usagestats`，统计页可看到 token、工具调用次数与工具耗时。

## 执行步骤

> 步骤 0 是全局硬前置；后续按依赖自然推进，允许在不冲突的前后端子任务间并行。状态取值：未执行、部分完成、已完成。每步满足「完成判定」后才可标记已完成。

**当前状态复核（2026-06-18 代码核对）**：执行步骤 0-12、18-20、22 为「已完成」；步骤 13-17、21 为「部分完成」；无「未执行」步骤。本次状态按实际功能落地与验收表缺口综合判定：核心 Graph 工作流图执行引擎的模型、存储、校验、调度、变量、分支、join、并发、循环、超时与瞬态重试、画布、布局回放、配置管理和手机端基础适配已完成；仍标为「部分完成」的模块主要是声明能力或自动化验收仍有缺口，包括嵌套循环 resume/recover 专项覆盖、真实 ACP replay/cancel 专项验证、运行中新增/删除图结构专项覆盖、运行控制/SSE/Chat 前端 E2E 等。本次补齐并复核了步骤 12「超时与瞬态重试」：Shell 结点瞬态/限流重试已接入统一重试驱动 `runWithRetries`，与 Prompt/评估共用同一两阶段策略，Shell 失败完整展示 retryCount 与 stdout/stderr/exit code，新增 `TestShellTransientRetryRecovers`/`TestShellRateLimitRetriesExhausted`/`TestShellDeterministicFailureNotRetried`，`go build ./...`、`go test -race ./services/graph`、`go vet ./services/graph` 通过。此前已补齐并复核步骤 20「Graph 配置管理」：复制、导入、导出、重置、未保存提示、校验错误定位均已由 `graph-canvas.spec.ts` 覆盖。

**前端「运行中编辑」状态（已完成，覆盖步骤 16/18/21）**：`PUT /api/v1/graph/run/:runId/version` 后端接口与前端调用链均已闭环。`GraphWorkflowPage` 在查看态之上叠加「编辑运行版本」子态：对可编辑（在飞/可续跑，非自然 `completed`）的 run 把当前有效版本快照载入可编辑画布，复用既有画布/Inspector/校验定位链路，保存调用 `PUT /run/:id/version`，400 校验错误走 `setErrors`+`focusError` 定位、409 提示运行已不可编辑；已有 succeeded/skipped/running 实例的结点在 Inspector 中按 `validateVersionEdit` 的 frozen 规则置灰（后端仍兜底校验）。GraphLoop「Edit」跳转携带 `?graphEditRun=<runId>`，Graph 页启动消费一次后直接打开该 run 的编辑态并清除参数。`GraphInspector` 全局视图已新增常驻 workdir 写冲突提示。`graph-canvas.spec.ts` 已覆盖失败 run 进入编辑→保存→`currentVersion`+1，以及改已成功结点配置→后端 400 并定位到该结点。自动化（前端 build/test、go build、graph-canvas E2E 8/8、新文件 eslint 无新增告警）全绿。

0. **会话 fork/replay 可行性验证**（已完成，依赖：无）：动工前 spike 验证「复制上游上下文新建独立 session」与「ACP replay 出等价 session」在现有 eino/ACP 链路可行（全方案最高风险假设）。**失败回退**按序取首个可行项作为正式会话血缘方案——①仅 ACP replay 不可行：ACP 链路改「新建空 session + 重放持久化历史消息」逼近等价，eino 链路保持 fork；②上下文重放整体不可行：「继承上游」降级为「新建独立 session 但不继承上游对话历史」（仅继承模型配置/工作目录/系统提示词），并在画布与运行态标注「上游对话上下文不可继承」；③记录所选回退对 §3 会话血缘语义的影响，连带评审受阻步骤 14 及下游 15/16/21。完成判定：形成覆盖 eino 与 ACP 两条链路成功或失败证据的可运行 spike 结论，失败时已据回退顺序确定并记录正式方案。（结论：采用统一的「复制上游会话持久化历史到新建独立 session」方案，对 eino 与 ACP 两条链路均可行且无需 ACP 原生 fork——新建独立 session 后把上游 session 的历史消息复制进新 session 的历史，eino 在运行时按历史加载、ACP 新建子进程检测到非空历史后自动 replay，二者达到等价上游上下文且不复用上游 session ID，正是 §3 主假设的首选形态，未触发任何回退分支。取消「绝不 reset」由现有 ACP 链路按 turn 取消天然满足。）
1. **图数据模型**（已完成，依赖：无）：在 `types/model` 定义 §5 所列全部结构体与 Graph API Request/Response。完成判定：后端能编译，结构可被 handler/service/repository 引用。
2. **路径与存储模型**（已完成，依赖：1）：在 `types/path` 与 `repository` 落 graph workflow、run 快照、实例/边状态、变量快照、会话血缘、事件日志的路径与原子读写，以及 GraphRun 全部产物的级联删除入口。完成判定：所有 Graph 数据都有统一路径函数和 repository 读写入口，删除 GraphRun 能级联清理全部产物且无残留，handler 不直接读写文件。（进度：GraphWorkflow 配置、GraphRun 元数据与基线快照、实例/边状态、变量快照、会话血缘、progress、resume、事件日志均已有统一路径与 repository 读写入口；GraphRun 删除会清理该 run 的全部运行产物，供后续执行引擎和运行控制接口复用。）
3. **服务与 handler 骨架**（已完成，依赖：1、2）：新增 `services/graph` 与 graph handler，接入 `cmd/web/api.go` 路由和鉴权；handler 只做参数解析、服务调用、错误全量返回、响应格式化。完成判定：Graph 基础 API 可被前端调用并返回统一响应，路由均走既定鉴权。
4. **图合法性校验**（已完成，依赖：1、3）：按 §1 全量校验 start/end、纯控制图、控制结点配置、不可达结点、入/出边、非法环、If-Else 端口、循环子图边界、条件表达式语法、变量写冲突、初始变量与输出变量同名覆盖、保留变量名、每条 start 链路首个可执行 Agent 禁继承（兜底多入边继承的无上游会话路径）、评估结点强制 ≥1 输出变量且禁分支出边，错误全量返回带定位。完成判定：非法图保存/导入时一次返回全部定位错误，合法图可保存。（进度：`services/graph` 的 `validateConfig` 全量校验 + 条件表达式 tokenizer/递归下降 parser 已实现，仅做保存期静态语法校验〔运行时变量存在性与求值留给执行引擎〕；附 `validate_test.go`/`condition_test.go` 单测全绿。**本次更新**：放开「多入边 Agent 禁继承」限制——多入边 join 现允许继承上游（运行时复制升序末位上游会话），保存期仅保留首个 Agent 禁继承兜底；`TestValidate_MultiInEdgeAgentInheritAllowed`/`TestValidate_MultiInEdgeFirstAgentInheritRejected` 覆盖放开与兜底两侧。）
5. **线性图调度闭环**（已完成，依赖：1、3）：实现就绪队列、入边计数、边三态、拓扑串行执行，先跑通 start → Shell/Prompt → end（可用最小内存 run 驱动，完整快照在步骤 6 接入）。完成判定：最小线性 Graph 类型 Job 可启动、执行、结束并得到成功/失败状态。（进度：`services/graph` 已实现第一阶段线性运行器，限定单 start、单 end、每个中间结点单入单出，支持 Shell/Prompt/评估结点顺序执行；GraphRun 元数据、实例状态、边状态、变量快照、progress、resume 与事件日志落到 `GraphRunRepo`，并通过窄接口同步绑定 Job 的 GraphRunID 与运行状态。该阶段主动拒绝分支、join、循环、并发等非线性图，后续步骤继续扩展。**步骤 9/10/11 落地后，线性运行器已被 `services/graph/scheduler.go` 的 DAG 就绪队列调度器取代**，分支/join/并发已支持；**步骤 13 落地后调度器进一步支持循环容器与嵌套循环子图，已不再拒绝任何合法图。**）
6. **GraphRun 创建、基线快照与图版本**（已完成，依赖：2、5）：启动 run 固化 nodes/edges/初始变量/disabledVars/布局/workspace/workdir/sandbox/运行配置/会话策略/被引用 Agent 与模型配置内容作为基线快照；运行中编辑追加 GraphRun 图版本，实例记录执行时版本。完成判定：已完成/运行中实例按执行时版本展示与续跑，后续未开始实例可按最新有效版本执行。（进度：run 启动时已创建 GraphRun，固化 workflow/config〔含 workspace/workdir/sandbox/会话策略字段〕基线快照，写入 baseline 版本，并在实例状态中记录执行版本。**本次新增**：①被引用 Agent/模型配置内容快照——`services/graph/snapshot_content.go` 的 `buildSnapshotContent` 遍历 Prompt/评估结点，按 string modelID 去重固化 `ModelInstance` 内容到 `BaseSnapshot.ModelSnapshots`，按结点 ID 固化 AgentType/ModelID/ACPMode/ACPThoughtLevel + 解析后系统提示词到 `AgentSnapshots`，模型缺失降级不阻塞启动；内容来源经扩展后的 `Runner`（新增 `ResolveModelSnapshot`/`ResolveSystemPrompt`，由 `jobRunnerImpl` 复用 `resolveModelCfg`/`promptService.ResolvePrompt` 实现）；baseline `GraphRunVersion` 同步携带该内容快照。②运行中编辑追加图版本——`services/graph/version.go` 的 `UpdateRunVersion` 对「最新有效版本 + 已持久化实例状态」做全量校验后追加 `GraphRunVersion`（含内容快照）、递增 `CurrentVersion`，`validateVersionEdit` 拒绝删除/改写已成功·跳过·运行中结点的执行配置、拒绝删除已完成实例依赖的边，错误全量带 NodeID/EdgeID 定位；新增 `PUT /api/v1/graph/run/:runId/version`。③后续未开始实例按最新有效版本执行——非在飞 run 继续由 `effectiveConfig` 在续跑时取 `CurrentVersion` 对应 config；在飞 run 的版本更新改由调度器控制通道进入单写者 goroutine，追加版本后刷新 `nodes/edges/disabledVars` 索引，尚未决定/启动的后续实例按最新版本执行，已在飞 worker 与已完成/跳过实例保留执行时版本不重跑。配套 `snapshot_content_test.go`/`version_test.go` 单测全绿，新增 `TestUpdateRunVersionInFlightAppliesToFutureNode` 覆盖运行中编辑实时生效，`go test -race ./services/graph` 通过。）
7. **Graph workflow CRUD 与列表**（已完成，依赖：2、3）：创建/读取/更新/删除/列表接口与基础前端入口，workflow 删除不影响已绑定 Graph 类型 Job 的运行态。完成判定：GraphWorkflow 可完整增删改查。（进度：后端 GraphWorkflow 增删改查、列表与 validate 接口已实现，删除为软删；主应用已新增 Graph Workflows 基础入口，支持列表、读取、新建、更新、删除和保存前校验。）
8. **变量基础能力**（已完成，依赖：4、5）：初始变量、输出变量声明、`_last_assistant_msg` 别名变量、Shell `QUARTET_CONTROL`/`quartet_set` 解析、Prompt/评估 `QUARTET_OUTPUT` 解析、实例可见快照、执行时替换、单写者串行落库、写同名变量保存校验、保留变量名保存校验、字符串比较选项处理。完成判定：变量声明、解析、替换、快照继承和冲突校验按 §1/§3 语义运行。（进度：模型层已承载初始变量、禁用变量、输出变量声明与 `_last_assistant_msg` 别名；保存期已校验输出变量重名、保留变量名、潜在并行写冲突；GraphRun repository 已提供变量快照读写入口。运行时纯逻辑已落地 `services/graph`：`{{变量}}` 单趟替换〔未知/剪枝保留字面量、禁用替空串、不二次替换〕、Prompt/评估 `QUARTET_OUTPUT` 行内子串匹配解析与声明集校验及协议后缀构造、Shell 控制文件解析〔`B64:`/明文/空值/`STOP_LOOP`/`STOP_WORKFLOW`、独立 helper 常量、声明集与保留名校验〕、join 可见快照合并〔并集 + `_last_assistant_msg` 按上游结点 ID 升序末位取值〕、字符串比较选项处理，均配套单测。DAG 调度器〔`services/graph/scheduler.go`〕已把初始变量、禁用变量、执行时替换、Shell/Prompt/评估输出解析、`_last_assistant_msg` 与别名变量接入到完整 DAG 链路：单入边继承上游快照、多入边 join 合并已激活上游、被剪枝上游不参与、剪枝结点不写输出变量，且所有变量快照由单一调度 goroutine 串行落库〔单写者〕。**步骤 13 落地后，循环内变量已按轮次累积〔不轮间隔离〕、轮末快照按已激活内部 end 的 join 合并并供下一轮与「直到条件」读取。**）
9. **条件表达式与 If-Else 剪枝**（已完成，依赖：5、8）：统一条件求值，实现 yes/no 激活/剪枝、全入边剪枝则自身剪枝、未知/被剪枝变量运行失败、被禁用变量按 §1 规则。完成判定：If-Else 可稳定只走一边，被剪枝路径不会导致下游 join 卡住。（进度：条件表达式 tokenizer/parser 与运行时求值已落地；DAG 调度器〔`services/graph/scheduler.go`〕已驱动 If-Else 运行时——对结点可见快照求条件、只激活 yes 或 no 出边并剪枝另一条、全入边剪枝则自身记跳过并向下游传播剪枝；条件求值失败按结点失败全量报错；配套 diamond/全剪枝/If-Else 路由单测。）
10. **汇总 join**（已完成，依赖：8、9）：多入边等全部入边解析，≥1 激活则执行、全剪枝则跳过并传播剪枝；合并已激活上游快照、不合并被剪枝上游。完成判定：diamond、多分支汇聚、全剪枝汇聚均能自然结束且变量快照正确。（进度：join 合并纯函数与 DAG 调度器的边三态机/入边计数已联通——多入边结点等全部入边解析后裁决，≥1 激活则按 `MergeVisibleSnapshots` 合并已激活上游快照后执行、全剪枝则跳过并继续传播；`_last_assistant_msg` 按上游结点 ID 升序末位取值；diamond join 单测覆盖。）
11. **并发执行**（已完成，依赖：5、10）：全局并发度配置（默认 4/最大 16/1 串行/非法保存失败），主图与循环子图共享单一信号量，循环容器/start/end 不占名额，结果串行落库。完成判定：并发度限制对主图和循环子图共同生效，并发度为 1 时含循环图不死锁。（进度：DAG 调度器以单一计数信号量作整个 run 的全局并发上界〔`concurrencyLimit` 解析默认 4、上限 16〕，就绪业务结点并发跑、start/end/If-Else 不占名额；所有状态变更与落库都在单一调度 goroutine 单写者完成、worker 只跑纯执行；失败时取消在飞 worker 并 drain 防泄漏；配套并发上界单测〔gatedRunner 验证峰值在飞 ≤ 限额〕与 `-race` 验证。**步骤 13 落地后，循环子图与主图共享同一信号量已验证——并发度=1 含循环、嵌套循环不死锁正常跑完。**）
12. **超时与瞬态重试**（已完成，依赖：5、11）：结点级与 job 级超时、取消上下文、失败传播、全量错误说明；网络重置/HTTP2 流/限流按 Loop 策略在结点内部重试。完成判定：结点超时、job 超时和瞬态错误重试都有完整状态与错误输出。（进度：DAG 调度器已接入 job 级总超时与结点级有效超时〔结点覆盖、默认 300s、0 不限〕，超时通过 context 取消在飞执行；结点超时按实例失败并保留完整错误，job 超时进入 `timedOut`、在飞实例落为 `interrupted` 并同步绑定 Job 失败状态。瞬态重试已抽出统一驱动 `services/graph/retry.go` 的 `runWithRetries`〔两阶段：transient 网络错误固定退避 2 次、rate-limit/429 指数退避 3 次含 Retry-After hint〕，Prompt/评估经 `runPromptWithRetries`、Shell 经 `runShellWithRetries`〔`runtime.go`〕共用同一驱动；重试在结点内部完成、不改边状态与 progress 终态，失败错误经 `withGraphRetryCount` 携带 retryCount 并由 `runtimeError` 透出到 GraphRuntimeError，Shell 失败错误完整含 stdout/stderr/exit code/控制文件内容；`STOP_LOOP`/`STOP_WORKFLOW` 与解析/声明类确定性失败不被分类为瞬态，故不重试。失败传播会取消其他在飞 worker、等待退出后再写终态，避免实例状态悬挂。配套单测覆盖结点超时、job 超时、Prompt transient retry 恢复、Prompt rate-limit retry 耗尽、Shell transient retry 恢复〔`TestShellTransientRetryRecovers`〕、Shell rate-limit retry 耗尽并展示 retryCount+stdout/stderr/exit code〔`TestShellRateLimitRetriesExhausted`〕、Shell 确定性失败不重试〔`TestShellDeterministicFailureNotRetried`〕与并发失败传播，`go test -race ./services/graph` 通过。）
13. **循环子图驱动**（部分完成，依赖：9、10、11）：固定次数、直到条件 do-while、最大次数兜底、0 次跳过、内部唯一入口、内部 end、全部剪枝结束本轮、轮末快照合并、嵌套循环实例键。完成判定：固定次数、0 次、直到条件、全部剪枝、嵌套循环都能结束或按规则失败，并产出正确对外快照。（进度：DAG 调度器〔`services/graph/scheduler.go`〕由「单作用域」泛化为「作用域实例集合」，所有运行态〔入边计数、边三态、剪枝、join 贡献、变量快照〕改用「结点 ID + 迭代上下文」实例键索引，主图无循环时实例键退化为结点 ID、行为与原 DAG 一致；新增 `services/graph/loop.go` 驱动循环容器——固定次数 N 轮、0 次跳过子图直接走主图、直到条件 do-while 轮末求值、最大次数兜底〔结点/全局/默认 100〕未满足则整 run 失败、唯一内部入口按累积快照启动每轮、内部 end 轮末按 join 合并〔全部剪枝则沿用入口快照〕、跨轮累积不隔离、嵌套循环按父容器 ID 分层且实例键逐层叠加；循环子图与主图共享单一并发信号量、循环容器/start/end 不占名额；循环内结点失败复用失败传播。Shell 控制信号接入——`STOP_WORKFLOW` 使 run 提前成功结束并停后续调度、循环内 `STOP_LOOP` 结束当前容器并剪枝剩余、非循环内 `STOP_LOOP` 判结点失败完整报错；progress 分母按嵌套循环静态上界初始化〔运行时剪枝/早停精修留步骤 15〕。配套 `services/graph/loop_test.go` 覆盖固定/0 次/until/累积/全剪枝失败传播/嵌套/STOP_LOOP/STOP_WORKFLOW/并发=1 不死锁，`-race` 通过。剩余缺口：嵌套循环 resume/recover 后重建迭代上下文缺专门单测。）
14. **会话血缘**（部分完成，依赖：0、6、10）：Agent 类结点新建/继承策略、并行分叉复制上下文后新建独立会话、多入边继承复制升序末位上游会话、每条 start 链路首个 Agent 必须新建；取消在飞 ACP Agent 只 cancel 不 reset。完成判定：保存校验能拦截非法继承，运行时会话 fork/replay/cancel 行为和错误展示符合 §3/§4。（进度：模型层承载会话策略，GraphRun repository 提供会话血缘读写入口；步骤 0 spike 已确定统一「复制上游历史新建独立 session」方案。本步骤新增：①运行时会话流——会话沿边作为「入流会话」传递，Agent 类结点按策略产出「出流会话」（新建策略创建全新 session，继承策略从入流会话 fork 出复制了上游历史的新独立 session），Shell/If-Else/循环等非 Agent 结点把入流会话原样透传为出流；多入边 join 处入流会话按「已激活上游结点 ID 升序末位」确定，与 `_last_assistant_msg` 同一可重放规则；并行分叉的多个继承 Agent 各自从同一上游独立 fork、不复用 session ID。②运行时守卫——继承策略但无可用上游会话时按结点失败完整报错。③保存期校验——「每条 start 链路首个可执行 Agent（穿过非 Agent 结点后遇到的第一个 Agent）不得声明继承」，非首个 Agent 的继承（含多入边 join）均合法。④会话血缘持久化——每个建立会话的实例把策略、出流会话、父会话、replay 消息数记入 run 的 resume 状态，续跑时重建上游会话来源。循环子图把入流会话带入每轮入口、按轮末快照规则累积、并在容器结束时作为出流会话向主图传递。配套 `session_test.go` 覆盖新建/继承 fork、并行 fork 独立、Shell 透传、多入边 join 新建、多入边 join 继承复制升序末位上游、继承无父运行失败，及保存期首个 Agent 继承拒绝/下游继承放行单测。**本次更新**：放开多入边 Agent 继承——校验层移除「多入边禁继承」、仅保留首个 Agent 兜底；运行时 fork 链路无需改动。剩余缺口：真实 ACP 子进程 replay 等价 session 与 graph 取消路径不触发 ACP reset 仍缺专项自动化验证。)
15. **GraphRun 状态与可靠性**（部分完成，依赖：6、12、13、14）：运行级状态机、实例 progress、分子分母回算、失败传播、未执行结点中断状态、resume、终态分类（成功/跳过 不重跑、失败/中断 重置后重跑）。完成判定：失败、暂停、步骤停止、硬停止、超时、进程重启后均可按状态机续跑或禁止续跑，且不重跑不可重置终态。（进度：DAG 调度器〔`services/graph/scheduler.go`〕已覆盖 GraphRun pending/running/completed/failed/timedOut 基础状态、实例 running/succeeded/failed/skipped/interrupted、按实例状态统计的 progress 分子分母、节点失败时 GraphRun/绑定 Job 失败同步、job 超时时在飞实例中断与绑定 Job 失败同步、实例/边/变量/progress/resume 落库；失败传播——任一结点失败即整 run 失败并取消在飞 worker〔context 取消 + drain 防泄漏〕，剪枝结点记跳过计入 progress。本步骤新增：硬停止/暂停/步骤后停止通过单一控制 channel 注入到单写者调度 goroutine〔`services/graph/control.go`〕——硬停止取消在飞实例记中断、status=stopped；暂停=优雅停止〔不派发新结点、在飞跑完〕、status=paused；步骤后停止冻结当前 ready 批次〔`Resume.FrozenBatch`〕、批次成员全终态后 status=stepStopped、循环内停在批次边界不强制整轮；运行中版本编辑也通过同一控制通道进入单写者，避免 HTTP 旁路写 run 文件被调度器后续 persist 覆盖，并把 worker 所需 run/config/disabled/outEdges 固化到 ready item，`-race` 验证无并发读写；续跑〔`ResumeRun`，`services/graph/resume.go`〕按终态分类——成功/跳过保留不重跑、失败/中断/进程崩溃残留的 running 作为重置起点边驱动传播重置〔丢弃其出边由重跑重发，diamond join 保留成功上游入边〕，循环内重置整迭代回退、活循环 scope 树由持久化 `Resume.LoopState` + 边状态重建；分母运行时回算〔`services/graph/denominator.go`〕——剪枝循环容器/until 早停/STOP_LOOP/0 次循环按已运行轮次回收未来实例估值、已建被剪枝实例记 skipped 计分子，自然完成 completed==total。**本次补齐**：运行期实例规模兜底已完成，单 run 实例总数或快照累计体积触顶时按 GraphRun 失败终止，取消在飞结点，并在错误中完整展示触顶项、当前值与上限；运行中编辑实时生效已完成，后续未开始实例按最新有效版本执行，成功/跳过/在飞实例保留执行时版本不重跑。配套 `control_test.go`/`resume_test.go`/`denominator_test.go`、规模兜底单测与运行中版本编辑单测全绿，`go test -race ./services/graph` 通过。剩余缺口：运行中新增/删除结点和边的调度推导与 progress 重算缺专项运行期单测。）
16. **运行控制接口**（部分完成，依赖：3、15）：运行、运行中编辑保存图版本、硬停止、暂停、步骤后停止、续跑、查看状态，以及删除 GraphRun/Graph 类型 Job（仅非在飞 run，级联清理快照/图版本/实例·边状态/变量快照/会话血缘/事件日志），错误按错误展示规格完整返回。完成判定：所有控制动作都能驱动绑定 GraphRun 状态变化；运行中编辑只影响后续未开始实例；删除能级联清理全部产物且不影响其它 run，并返回完整错误详情。（进度：GraphRun 启动、状态查询接口已有；本步骤新增 `POST /api/v1/graph/run/:runId/stop`、`/pause`、`/step-stop`、`/resume`、`DELETE /api/v1/graph/run/:runId`，均经统一鉴权与 `httputil.MapError` 完整错误返回〔非运行态控制→409、非可续跑续跑→409、in-flight 删除→409〕；删除非在飞 run 经 `runRepo.DeleteRun` 级联清理整 run 目录并经新增的 `JobStateSink.ClearGraphRunLinkage`〔`services/job/executor_mutators.go`〕清除绑定 Job 的 GraphRunID〔仅当仍绑定该 run〕；续跑接口解析绑定 Job + `newJobRunner` 后驱动 `ResumeRun`。**本次新增**：运行中编辑保存图版本接口 `PUT /api/v1/graph/run/:runId/version`（`UpdateGraphRunVersion` handler 经 `validationErrors` 全量返回 400、`ErrGraphRunNotEditable`→409），在飞 run 通过调度器控制通道热应用版本，追加版本只影响后续未开始实例，已在飞/已完成实例不受影响。本步骤后端运行控制接口已闭环；前端运行中编辑入口已于步骤 18/21 补齐〔见下〕。剩余缺口：硬停止、暂停、续跑、删除等控制动作缺前端 E2E 直接驱动。）
17. **SSE 事件流**（部分完成，依赖：15、16）：按实例键上报结点状态、边激活/剪枝、变量写入、循环轮次、progress、日志与错误；刷新页面后可恢复当前运行状态和日志。完成判定：运行中刷新页面可恢复画布状态、日志和 progress，GraphRun 可查看关键事件。（进度：GraphRun repository 已支持事件追加与事件列表读取，调度器已在实例开始/完成/失败/跳过、边解析、变量写入、循环轮次、progress 更新和错误时追加事件；`GET /api/v1/graph/run/:runId` 已返回持久化 events，可用于刷新后恢复状态和运行查看。`GET /api/v1/graph/run/:runId/events` 独立 SSE 流式接口已落地，使用持久化事件日志行号作为 `Last-Event-ID` 续传点，运行中按增量事件实时推送，终态空闲后自动关闭；本次补齐 Agent 级流式可观测性和用量统计：`services/graph/runtime.go` 的 `graphEventHandler` 已把 Agent 消息、思考、工具调用开始/参数/结果/结束、token usage 转成 GraphRun 事件追加到事件日志并经现有 SSE 续传；`services/graph` 通过 `SetUsageRecorder` 接入 `usagestats`，Agent 结点完成时记录 workspace/model 归因、assistant/thought/tool 调用计数、token 与工具耗时；前端 GraphLoop 与 Graph 运行事件面板已识别这些 Agent 事件并展示关键 payload。新增 `TestPromptNodeStreamsAgentEventsAndRecordsUsage` 覆盖事件持久化与 usage snapshot。`go test ./services/graph ./cmd/web/handler`、`go build ./...`、`web npm run build`、`web npm test -- --run` 均通过。剩余缺口：运行中刷新页面恢复画布、日志和 progress 缺专项 E2E。）
18. **独立 React Flow 画布**（已完成，依赖：4、7）：独立 Graph 画布入口，支持新增/编辑结点、连线、配置、变量表、运行配置、会话策略、保存校验定位、运行高亮。完成判定：用户可在新入口完成一个可运行 GraphWorkflow 的创建、保存、运行与错误定位。（进度：`web/src/components/graph` 已落地正式画布——集成 `@xyflow/react` 依赖；`graphFlowAdapter` 负责 GraphConfig↔React Flow 双向映射〔结点位置、循环容器 parentId/extent 与尺寸、yes/no 端口、布局往返稳定、保留未编辑字段〕并配套单测；自定义结点 `QuartetNode`/`LoopGroupNode` + `kinds` 移植 demo 视觉语言，渲染输入/输出变量 pill 与运行高亮；`GraphInspector` 按结点类型只展示自身配置项〔Shell/Prompt/评估/If-Else/循环/start/end〕，Prompt/评估含 Agent/模型/会话策略表单，未选中时展示全局变量表与运行配置表；`GraphCanvas` 提供调色板拖拽/点选添加结点、yes/no 自动连线、删除〔start/end 禁删〕、运行态高亮注入与校验错误元素红描边 + 点击错误定位 fitView。`GraphWorkflowPage` 改造为画布 + Inspector，保留全部 CRUD/validate/run/SSE 数据流并保留 JSON 高级视图，新增 Run/Import/Export 入口。**端到端验收已闭环**：新增 `web/e2e/tests/graph-canvas.spec.ts` 用纯 Shell 图〔start→Shell〔echo〕→end，无需模型/ACP 凭证〕在真实后端上验证创建/保存〔dirty 清除、入库列表出现〕、运行至 `completed`〔分子=分母〕、非法图保存返回定位错误并点击错误定位到结点；E2E 全绿。**验收期间修复三处真实缺陷**：①运行路径不可用——前端 `startRun` 不带 `jobId`，后端硬要求 `jobId` 返回 400，按设计「每次启动创建一个 Graph 类型 Job」在 `cmd/web/handler/graph.go` `StartGraphRun` 缺 `jobId` 时创建并绑定 `JobModeGraph` Job〔复用 `model.NewJob`/默认 workspace 回退/`validateWorkdir`/`ensureWorkdirWithinWorkspace`〕；②未保存提示永久亮起——`stableStringify` 未对齐 Go `omitempty`，live config 的 `variables:{}`/`disabledVars:[]`/`canvas:undefined` 与入库 config 永不相等，已让指纹忽略空集合/undefined 且 `dirty` 指纹排除纯视图态 `canvas` 视口；③React Flow 触发的良性 `ResizeObserver loop` 浏览器告警被 `main.tsx` boot-error 覆盖层当作致命错误绘制整屏遮罩、阻断画布交互，已在覆盖层错误处理中过滤该已知噪声。`go build ./...`、`go test ./services/graph/... ./services/job/... ./cmd/web/...`、`web npm run build`、`web npm test -- --run`、`graph-canvas` E2E 均通过。**本次补齐前端「运行中编辑」**：`GraphWorkflowPage` 在查看 run 态之上叠加「编辑运行版本」子态——对可编辑〔在飞/可续跑，非自然 `completed`〕的 run 把当前有效版本快照载入可编辑画布〔复用既有 `GraphCanvas`/`GraphInspector`/`focusError` 校验定位链路〕，保存调 `PUT /api/v1/graph/run/:runId/version`，400 校验错误走 `setErrors`+`focusError` 定位、409 提示运行已不可编辑；已有 succeeded/skipped/running 实例的结点在 `GraphInspector` 中按后端 `validateVersionEdit` 的 frozen 规则置灰〔`fieldset disabled` + banner，后端仍兜底校验〕；并按 §1 强制要求在 Inspector 全局视图新增常驻 workdir 写冲突提示。**本次补齐前端状态一致性**：workflow/run 详情加载使用最新请求保护，避免快速切换时旧响应覆盖当前画布；JSON 高级模式应用后清理旧校验错误并重新执行静态校验，非法草稿保留在画布但完整展示错误；任意画布/Inspector/全局配置编辑会清理旧校验结果，避免 stale 高亮误导；Reset 复用打开时的 canonical config，保证旧工作流的 workspace fallback 不丢失；workspace 切换纳入 undo/redo；ConditionBuilder 随外部 value 变化同步，保证 undo/redo 后 Inspector 显示真实配置；Agent/Workspace 列表加载失败在页面中完整展示；Description 字段恢复为可编辑；Graph 新增 UI 文案补齐 i18n，workspace 下拉项改为可键盘操作。`graph-canvas.spec.ts` 新增 2 个端到端用例：失败 run 进入编辑→保存→`currentVersion`+1、`versions` 增长；改已成功结点配置→后端 400 且定位到该结点。`web npm run build`、`web npm test -- --run`、`graph-canvas` E2E 8/8 全绿，新增前端文件 eslint 无新增告警。）
19. **布局保存与运行态回放**（已完成，依赖：6、18）：保存并恢复结点位置/连线/视图；运行态按 GraphRun 快照展示布局。完成判定：保存重开、运行查看时布局稳定且不受后续编辑影响。（进度：画布拖拽位置经 `flowToConfig` 回写结点 `layout.x/y`〔循环容器含 width/height〕，画布视图 `onMoveEnd` 回写 `canvas.viewport`，保存重开按持久化布局稳定还原；`flowToConfig` 按 prevConfig 顺序稳定排序保证保存 diff 稳定、不受后续编辑影响〔单测覆盖位置变更后顺序不变〕。打开绑定 run 进入只读查看态：按 `currentVersion` 对应版本快照〔缺失回退 baseSnapshot〕的 config 渲染布局，运行态由 run 实例状态注入结点高亮，运行中经 SSE 实时刷新。**端到端验收已闭环**：`graph-canvas.spec.ts` 验证 JSON 应用特定 `layout.x/y` 后保存、经 `/api/v1/graph/workflow/:id` 读回结点坐标精确往返〔444/222〕、UI 重选 workflow 行恢复画布且回到「Saved state」；打开已完成 run 进入只读查看态，结点带 `run-succeeded` 运行态类重渲染、编辑控件被「返回编辑」替代〔`Run` 按钮消失〕。注：运行态 `canvas.viewport` 属纯视图态，已从 dirty 指纹排除以避免 fitView 后误报未保存。）
20. **Graph 配置管理**（已完成，依赖：7、18）：复制、导入、导出、重置、未保存提示、校验错误定位，保存内容为 Graph 配置。完成判定：Graph 配置管理操作可用，且不含运行记录或当前 run 状态。（进度：基础入口已支持当前配置另存为新 workflow〔复制〕、从默认图新建草稿、保存前校验并展示完整错误列表；导出当前 config 为 `.json`〔不含运行记录与 run 状态〕、导入 `.json` 文件还原到画布并全量报错、面向画布元素的校验错误定位。Graph 模板库能力已移除，工作流库作为唯一的 Graph 配置保存入口。配置管理 E2E 覆盖 Save as New 副本独立、导出内容边界、合法导入保存、非法图结构导入定位、非法 JSON 错误展示、dirty 提示与 Reset 恢复保存状态。）
21. **Chat 页 GraphLoop 组件**（部分完成，依赖：16、17、18）：按 job 类型选择旧 Loop 组件或 GraphLoop；GraphLoop 展示 progress、当前位置、已走分支、循环轮次、join 等待，支持编辑/暂停/步骤后停止，不改旧 Loop 任务语义。完成判定：Chat 页能正确区分旧 Loop Job 与 Graph 类型 Job，GraphLoop 控制只影响绑定 GraphRun。（进度：Chat 页 hook 已识别 `mode=graph` 与绑定 `graphRunId`，旧 Loop 仍走 `LoopProgress`/`LoopSessionSidebar`；新增独立 `GraphLoopProgress` 组件，加载 `/api/v1/graph/run/:runId` 并订阅 `/events`，展示总进度、运行中实例、已激活/剪枝边、join 等待、循环轮次、最近事件与完整错误；支持编辑跳转到 Graph 入口、暂停、步骤后停止、硬停止与续跑，所有控制动作只调用绑定 GraphRun 的后端接口；Chat 输入在 Graph Job 中禁用，避免混入交互消息；历史/侧边栏/Chat 下拉列表已区分 graph job 图标。**本次补齐**：GraphLoop「Edit」跳转携带 `?graphEditRun=<runId>`，Graph 页启动消费一次后直接打开该 run 的「编辑运行版本」态〔见步骤 18〕并清除该 URL 参数，使 Chat 页编辑入口真正落到在飞 run 的版本化编辑而非仅 workflow 配置编辑。剩余缺口：Chat 页 graph job 分流、GraphLoop 控制和暂停/步骤后停止等前端链路缺专项 E2E/组件测试。）
22. **手机端适配**（已完成，依赖：18、21）：窄屏触摸平移/缩放、底部抽屉或全屏编辑、点选放置结点、点选起终点连线、变量表单列布局、运行日志单列滚动。完成判定：窄屏下可完成基础浏览、编辑、保存、运行查看和运行控制。（进度：`GraphCanvas` 已支持点击调色板在当前视口中心添加结点，窄屏隐藏小地图并将调色板改为横向可换行区域；新增点选连线工具，用户可先点起点、再点画布终点，也可在起点选中后直接用工具条终点按钮完成连线，If-Else 起点支持 YES/NO 端口切换，避免手机端拖拽连线和画布边缘裁切带来的不可用；`GraphInspector` 在窄屏变为真正的底部抽屉，支持显式展开/收起与滑入动效，选中结点时自动展开，收起后只保留抽屉把手以让出画布空间；变量表单列/紧凑输入可用，Graph 页面主体窄屏纵向布局，运行事件列表窄屏单列滚动。已通过 390×844 视口端到端验证手机端抽屉可收起、选中结点后自动展开；自动化验证 `web npm run build`、`web npm test -- --run` 与 `graph-canvas.spec.ts`（8 用例）通过。）

## 验收标准

> **单测范围**：调度、剪枝、循环、实例键、变量快照、条件表达式等高风险纯逻辑按下表写单测；前端流程、真实 Agent/ACP 链路、配置管理入口按 E2E/手工/代码 review 验收。
>
> **验收状态图例**（2026-06-18 按实际代码与测试核对）：✅ 已完成（功能落地且对应验收手段已覆盖）；⚠️ 部分完成（功能已实现，但声明的验收手段尚有缺口，下表在该行注明缺口）。状态标注追加在每行「测试方式」列尾。

### 调度与并发

| 功能 | 测试方式 |
| --- | --- |
| 线性图顺序执行 | 单测 ✅ |
| 多出边并行执行（真并发跑 Agent） | 单测 + 手工 ✅ |
| 并发度：默认 4 / 最大 16 / 为 1 串行 / 非法值保存失败全量报错 | 单测 + 手工 ✅ |
| 并发度为整个 run 全局上界：主图 + 循环内（含嵌套）并行同时在飞不超上限 | 单测 ✅ |
| 循环容器/start/end 不占名额：并发度=1 含循环、嵌套循环不死锁正常跑完 | 单测 ✅ |
| 结点级/job 级超时：缺省默认（结点 300s/job 不限）生效、结点可覆盖、非法值保存报错；取消按失败处理触发失败传播并全量说明；超时不触发瞬态重试 | 单测 + 手工 ✅ |
| 瞬态错误（网络重置/HTTP2 流/限流）按 Loop 策略重试后再判失败；Shell 重试完整展示次数与 stdout/stderr/exit code | 单测 ✅（Prompt/评估与 Shell 均经统一重试驱动 `runWithRetries` 包装瞬态 2 次+限流 3 次重试；Shell 失败错误含 retryCount 与 stdout/stderr/exit code/控制文件内容，确定性失败不重试。覆盖 `TestShellTransientRetryRecovers`/`TestShellRateLimitRetriesExhausted`/`TestShellDeterministicFailureNotRetried`） |
| 并发写变量/progress 无竞态 | 单测 ✅ |
| 实例规模兜底：单 run 实例总数（默认 100000）或快照体积（默认 1 GiB）触顶按 job 失败终止、取消在飞结点并全量说明；非法上限值保存报错 | 单测 + 手工 ✅ |

### 分支、汇总与条件

| 功能 | 测试方式 |
| --- | --- |
| 多入边汇总等全部入边解析，≥1 激活则执行、全剪枝则跳过并传播剪枝 | 单测 ✅ |
| join 合并已激活上游、不合并被剪枝上游、不同上游变量取并集 | 单测 ✅ |
| 分支剪枝向下游正确传播（diamond/嵌套分支不死锁） | 单测 ✅ |
| 分支任一路失败触发失败传播、取消其它路 | 单测 ✅ |
| If-Else 条件分支只走一条路 | 单测 ✅ |
| 条件表达式：字符串比较、`StartWith`、`EndWith`、忽略大小写、忽略空格、且或非、`{{变量}}`、优先级与括号、字面量；非法表达式与括号不配对保存失败全量报错 | 单测 ✅ |
| 条件运行时覆盖空串、未知、被禁用、被剪枝、大小写差异、空白差异，错误完整展示 | 单测 ✅ |
| 无任何 end 被激活时判 job 失败并全量说明「无终点到达」 | 单测 ⚠️（调度器已实现「无终点到达」失败分支〔scheduler.go〕；目前仅校验层 `validate_test.go` 涉及该规则，缺一条直接驱动运行到「无 end 激活→run 失败」的调度单测） |

### 变量

| 功能 | 测试方式 |
| --- | --- |
| Shell 控制文件协议：注入 `QUARTET_CONTROL` 与 `quartet_set`/`quartet_break`/`quartet_return` helper；解析 `B64:key=value`、明文 `key=value`、空值、值含空白/等号、同名多行取最后；未声明变量/缺已声明变量/变量名非法/保留名/控制文件解码失败时结点失败完整报错（含 stdout/stderr/exit code/控制文件内容或解析错误） | 单测 ✅ |
| Shell 控制信号：`quartet_return` / `STOP_WORKFLOW` 使 GraphRun 成功提前结束并停止后续调度；循环内 `quartet_break` / `STOP_LOOP` 结束当前循环容器并剪枝容器内剩余实例；非循环内 `STOP_LOOP` 判节点失败完整报错 | 单测 + 手工 ✅ |
| Prompt/评估 `QUARTET_OUTPUT` 协议：行内子串匹配、变量名字符集、首个 `=` 切分、空串、值含 `=`、同名多行取最后、单行限制；配置中重复声明保存失败；模型输出未声明变量/缺已声明变量/变量名非法/值跨行时结点失败完整报错（含模型原始输出） | 单测 ✅ |
| 输出变量统一按字符串写入与比较，`"10" > "9"` 按字符串 Unicode 码点字典序比较；`StartWith`/`EndWith` 与比较选项按表达式配置生效 | 单测 ✅ |
| 可执行结点产出变量、下游引用 | 单测 ✅ |
| `_last_assistant_msg` 默认写原始最终输出（Prompt/评估为模型原始输出、Shell 为 stdout 全量不含 stderr）；结点配置别名变量时，调度器自动把同一内容写入别名变量，不要求 Shell `quartet_set` 或模型 `QUARTET_OUTPUT`；多上游 join 按已激活入边「上游结点 ID 升序末位」取值（不依赖到达顺序、续跑一致）；条件中固定按 string 整体比较（不截断不切分） | 单测 + 手工 ✅ |
| 保留变量名：声明 `_last_assistant_msg` 或任意 `_` 开头名保存失败全量定位；`_last_assistant_msg` 别名变量命中保留名、与显式输出变量重名或在可并行区域冲突时保存失败；Shell 控制文件或 Prompt/评估 `QUARTET_OUTPUT` 写保留名时结点失败完整报错 | 单测 ✅ |
| 被剪枝变量文本替换保留 `{{变量}}`、条件引用按未知变量运行失败完整报错 | 单测 ✅ |
| 未知变量（从未定义）文本替换保留 `{{变量}}` 不报错；条件引用按未知变量运行失败完整报错 | 单测 ✅ |
| 条件不支持裸变量真值判断，`{{变量}}` 必须出现在显式比较式中；被禁用变量按空字符串参与比较 | 单测 ✅ |
| 输出变量与初始变量同名允许、按血缘快照覆盖 | 单测 + 手工 ✅ |
| 并行分支间无边约束时不通过变量隐式通信 | 单测 ✅ |
| 同一可并行区域写同名变量保存失败并全量展示冲突结点，跨轮按最新覆盖 | 单测 + 手工 ✅ |

### 循环

| 功能 | 测试方式 |
| --- | --- |
| 固定次数重复，次数为 0 时跳过子图并继续主图 | 单测 ✅ |
| 「直到条件」do-while 轮末判断，达最大次数兜底（默认 100/最大 1000，非法值保存失败）仍未满足时 job 失败并全量说明 | 单测 ✅ |
| 下一轮入口及出边读到上一轮变量；一轮内多激活内部 end 按 join 合并；同轮可并行路径写同名保存失败；跨轮同结点写同名按最新覆盖 | 单测 ✅ |
| 子图按唯一入口启动，所有内部路径到达内部 end 或被剪枝后结束本轮；全部剪枝时本轮正常结束 | 单测 ✅ |
| 循环内失败触发失败传播，剪枝结点按跳过计入 progress | 单测 ✅ |
| 嵌套循环：按父容器 ID 分层、实例键唯一可恢复、嵌套续跑正确重建 | 单测 ⚠️（`TestNestedLoops` 已覆盖嵌套分层与实例键唯一执行；缺一条专门验证「嵌套循环续跑后正确重建迭代上下文」的 resume 单测） |

### 图结构校验

| 功能 | 测试方式 |
| --- | --- |
| 缺 start/end、start/end 边界违规、纯 `start→end`、控制结点配业务动作/输出变量/yes-no 端口、不可达结点、普通结点缺入/出边、隐式终点、非法环、If-Else 缺 yes/no、普通结点非法端口、评估结点缺输出变量或配分支出边、循环跨界连线、子图非唯一入口、缺内部 end、内部路径不到 end | 单测 + 手工 ✅ |

### 会话与可靠性

| 功能 | 测试方式 |
| --- | --- |
| 会话沿边继承/新建/并行分叉复制上下文后新建独立会话；多入边 join 配继承时复制升序末位上游会话、配新建时铸新会话；首个可执行 Agent 配继承时保存失败 | 单测 ✅ |
| 会话 fork/replay：ACP 不支持直接 fork 时用持久化历史 replay 出等价 session；失败按结点启动失败全量返回（含会话策略、所在结点、上游实例键、ACP/eino 原文），中断实例可续跑 | 单测 + 手工 ⚠️（统一「复制上游历史新建独立 session」方案已落地，`session_test.go` 覆盖新建/继承 fork/并行独立 fork/继承无父失败；ACP 真实子进程 replay 等价 session 的端到端验证仍属手工项，未在自动化中固化） |
| 取消在飞 ACP Agent 只 cancel 不 reset session，中断实例可续跑 | 单测 + 手工 ⚠️（取消走现有 ACP 按 turn 取消、不 reset，由复用链路保证；缺一条专门断言「graph 取消路径不触发 ACP reset」的自动化，列为手工验收） |
| 硬停止/优雅停止保留状态可续跑 | 单测 + 手工 ✅ |
| 续跑基于实例/边状态/变量快照/会话血缘/循环上下文重建就绪队列，成功/跳过不重跑、失败/中断重置后重跑 | 单测 ✅ |
| 失败传播后已完成终态实例不改写、在飞实例取消并记中断、已入队未开始和未来不会执行的业务实例保持待定记阻断原因、progress 保留失败时分子分母 | 单测 + 手工 ✅ |
| 分母回算：剪枝/早停/0 次循环/多 start/多 end/共享下游/复杂 DAG 按实例状态正确；start/end 与内部 end 不计分母、容器计入；已纳入分母的剪枝实例记跳过并计分子，未实例化且未来不执行的实例从分母扣除，失败计分子、中断不计；完成时分子=分母，失败/停止保留当时值 | 单测 ✅ |
| GraphRun 启动保存基线快照（含被引用 Agent/模型配置内容）；运行中编辑 Prompt/AgentType/model/结点配置后追加图版本，已完成/运行中实例不改写，后续未开始实例按最新有效版本执行；仅改全局模型配置不自动影响运行中 run | 单测 + 手工 ✅ |
| 运行中新增/删除结点和边：新增部分只在可从当前状态推导入边时调度；删除尚未开始且无已完成下游依赖的结点/边可保存并重算 progress；删除或断开已完成实例依赖路径时保存失败并定位 | 单测 + 手工 ⚠️（运行中编辑「改结点配置 / 冻结结点拒改 / 在飞实时生效」已有单测；缺专门覆盖「运行中新增结点·新增边只在入边可推导时调度」与「删除未开始结点重算 progress 分母」的运行期单测，目前以手工验收为主） |
| 删除 GraphRun/Graph 类型 Job：仅允许删除非在飞 run，级联原子清理快照/图版本/实例·边状态/变量快照/会话血缘/事件日志，无残留且不影响其它 run；删除在飞 run 被拒绝并报错 | 单测 + 手工 ✅ |
| 工作目录/沙箱：run 绑定 workspace 与 workdir 固化进快照，所有 Shell/Agent 在该 workdir（及 sandbox）执行；并行结点共享同一工作目录且 UI 有写冲突提示 | 手工 ⚠️（workdir 固化进快照、Shell/Agent 在 workdir 执行、UI 常驻写冲突提示均已落地；sandbox 约束沿用 Loop——经 workspace 路径解析与 Agent 工具中间件 `sandbox_backend` 生效，Shell 结点本身仅设 `cmd.Dir=workdir` 不做容器包裹，与 Loop 一致，「Shell 进 sandbox 执行」的强形态未单独实现，待手工确认是否符合预期） |

### 分层与接入

| 功能 | 测试方式 |
| --- | --- |
| graph 配置/运行记录/实例状态/变量快照/会话血缘按分层落地，handler 不直接读写文件 | 代码 review ✅ |
| graph 工作流 CRUD/运行/硬停止/优雅停止/续跑/查看状态/删除，入口与 Loop 并列 | E2E + 手工 ⚠️（CRUD、启动、运行至完成、运行中编辑已有 E2E；硬停止、暂停、续跑、删除的前端链路尚无 E2E 用例直接驱动，列为手工验收） |
| graph 接口接入统一路由与鉴权，错误完整返回前端 | E2E + 手工 ✅ |
| SSE 按实例键上报，刷新页面后恢复当前运行状态和日志 | E2E + 手工 ⚠️（SSE 接口与事件回放已实现并在运行查看链路使用；缺一条专门「运行中刷新页面恢复画布/日志/progress」的 E2E 用例，目前以手工验收） |
| 可观测性：结点/边/循环/join/会话/变量/并发/超时均经 `pkg/logger` 打点，失败全量输出错误；Agent 级消息/思考/工具调用/token usage 经 GraphRun 事件和 SSE 上报；Graph Agent 结点用量接入 `usagestats` | 代码 review + 单测 + 手工 ✅ |
| 错误展示规格：API 返回完整 stdout/stderr/model 原始输出与定位字段（不只给摘要）；前端可折叠但必须能展开看完整内容；GraphRun 保留错误详情 | E2E + 手工 ✅ |

### 配置管理（对齐 Loop）

| 功能 | 测试方式 |
| --- | --- |
| 复制：生成独立副本（结点/边/变量/disabledVars/运行配置/会话策略/布局），改副本不影响原配置 | E2E + 手工 ✅（`graph-canvas.spec.ts` 已覆盖 Save as New 后原 workflow 与副本配置互不影响） |
| 导出：文件含完整配置与布局，不含运行记录与当前 run 状态 | E2E + 手工 ✅（`graph-canvas.spec.ts` 已覆盖导出 JSON 含变量、运行配置与布局，且不含 run/events/instances） |
| 导入：合法文件还原为新配置；非法 JSON/缺字段/非法图结构/变量冲突/非法条件表达式均导入失败全量展示错误 | E2E + 手工 ✅（`graph-canvas.spec.ts` 已覆盖合法导入保存、非法图结构定位与非法 JSON 完整错误；导入校验直接作用于导入内容） |
| 重置：回到默认空图或最近保存状态，清未保存变更且不影响已有 run 快照 | E2E + 手工 ✅（`graph-canvas.spec.ts` 已覆盖保存后编辑变量/运行配置/节点配置触发 dirty，Reset 后恢复最近保存状态） |
| 未保存提示：结点/边/变量/运行配置/会话策略/布局任一修改触发，保存后消失 | E2E + 手工 ✅（`graph-canvas.spec.ts` 已覆盖保存后清 dirty、复制/模板/导入/重置相关修改触发或清除未保存提示） |
| 校验错误定位：保存或导入失败时定位到相关结点/边/变量/全局配置，多错误全量展示 | E2E + 手工 ✅（`graph-canvas.spec.ts` 已覆盖非法图保存与非法导入返回定位错误并点击定位到结点） |

### 前端画布与 Chat 兼容

| 功能 | 测试方式 |
| --- | --- |
| 画布编排（添加 start/end/Shell/Prompt/评估/If-Else/循环、普通连线与 yes/no 连线、拖拽、配置、运行高亮）作独立入口 | E2E + 手工 ✅ |
| 节点配置面板：各结点只展示自身配置项，控制结点不可配业务动作，Prompt/评估可配会话策略，Shell/Prompt/评估可配置输出变量声明与 `_last_assistant_msg` 别名变量名 | E2E + 手工 ✅ |
| 保存布局：结点位置/连线/视图保存后重开一致；运行态按 run 快照展示位置 | E2E + 手工 ✅ |
| Chat 页按 job 类型选择旧 Loop 组件或 GraphLoop；GraphLoop 显示总进度/结点状态/当前位置/已走分支/循环轮次/join 等待，支持编辑/暂停/步骤后停止，旧 Loop 语义不变 | E2E + 手工 ⚠️（`JobChat` 已按 `isGraph` 分流渲染 `GraphLoopProgress` 并实现编辑/暂停/步骤后停止/续跑控制；缺 Chat 页 graph job 的 E2E/组件测试，目前以手工验收） |
| 运行中编辑保存为 GraphRun 图版本：当前运行中/已完成/冻结批次实例不受影响，后续未开始实例使用最新有效版本；历史查看能按实例执行时版本回放 | E2E + 手工 ✅ |
| 暂停=优雅停止：不再调度新结点、等在飞结束、未调度实例保持待定、可续跑 | E2E + 手工 ⚠️（后端暂停/续跑已实现且有 Go 单测；缺前端 E2E 直接驱动暂停→续跑，列为手工） |
| 步骤后停止=当前 ready 批次边界：批次 ID、成员与成员状态持久化；触发后进入「步骤停止中」，本批次完成后进入「已步骤停止」，不调度后续批次；续跑清除冻结状态并从持久化状态继续 | E2E + 手工 ⚠️（后端批次冻结/恢复已实现且有 Go 单测；缺前端 E2E，列为手工） |
| 手机端（窄屏）画布触摸缩放/平移、点选添加结点、点选连线、结点编辑、变量表、运行查看可用 | 手工 ✅（`graph-canvas.spec.ts` 已含 390×844 窄屏抽屉收起/选中展开用例，其余触摸交互手工验收） |

### 独立性

| 功能 | 测试方式 |
| --- | --- |
| `graph` 模块与现有 Loop 不共享可变运行状态，存量 Loop 任务继续走旧执行链路且不受影响 | 手工 ✅ |
| Graph 画布不提供旧 Loop 配置迁移入口；需要复用旧配置时由用户手动在 Graph 画布重建 | 手工 ✅ |
