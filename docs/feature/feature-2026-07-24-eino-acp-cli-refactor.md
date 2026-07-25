# eino Agent 走 ACP 子进程化重构方案

> 目标一句话：把 quartet 内**所有 eino 相关功能**整体抽出，编译成独立二进制 `eino-cli`（入口 `cmd/eino-cli`，实现集中存放、与 quartet 后端零依赖，日后可整体抽到独立仓库）；quartet 与 agent 的对接**只保留 ACP 一条通道**——无私有协议、无 eino 运行时依赖——仓库后端**不再保留任何单独的 eino 路径**。

---

## 1. 背景与现状

当前 quartet 有两套 agent runtime，靠会话类型是否为 eino 做分流：

- **eino 分支**（in-process）：内置 ADK 推理循环，直接在同进程里跑，工具走本机 sandbox，历史直接读写本地 `messages.jsonl`（磁盘即事实来源，每轮重新装载）。其运行时、中间件、聊天上下文组装、会话管理等代码全部长在 quartet 后端内，且直接依赖 quartet 的存储、路径、沙箱等包。
- **acp 分支**（subprocess）：把外部 CLI（claude / gemini / codex …）当子进程，走 ACP 协议（stdio JSON-RPC）。子进程自管上下文，quartet 的 `messages.jsonl` 只是镜像，靠指纹漂移检测 + resume/load 对齐。

两条分支在协议中立事件层汇流，再经统一的事件构建与 SSE 通道推到前端，前端已经分不出是哪种 agent。

问题：eino 运行时和 quartet 的存储、模型配置、handler 分流深度耦合，既无法独立移植，又让 agent 列表、dispatch、多个 handler 里到处是 eino 特判。

## 2. 目标

- 把 quartet 内所有 eino 相关功能——推理循环、中间件链、聊天上下文组装、会话管理、多模态输入、历史压缩——整体抽出，编译成独立二进制 `eino-cli`：入口在 `cmd/eino-cli`，其余全部实现集中在统一目录；与 quartet 后端代码**零依赖**；结构按“日后可整体抽到独立仓库”设计。
- `eino-cli` **完整适配 ACP 公共协议**（建会话 / 加载 / 恢复 / 提问 / 取消 / 配置项等标准能力），可被任意 ACP client 驱动。
- quartet 与 agent 的对接**只保留 ACP 一条通道**：不扩展 `pkg/acp`、不引入私有协议、不再保留 eino 运行时（adk/编排）依赖——`eino/schema` 消息类型作为共享消息表示继续被存储与 ACP 链路使用；`eino-cli` 与 claude/gemini 走**完全相同**的接入、缓存、事件、存储链路。
- `eino-cli` 自管配置（模型目录、密钥、系统提示词），自存会话（`~/.eino/`）；quartet 设置页新增 eino 配置 tab，经后端 exec `eino-cli models` / `eino-cli systemprompt` 子命令（JSON）读写。
- quartet 后端**不再保留单独的 eino 路径**：in-process 运行时、eino 专属上下文组装、agent type 分流、handler 与 agent 列表特判、前端 eino 分支全部移除。quartet 对 eino 的唯一认知是“一个叫 eino-cli 的 ACP agent”。
- 多模态（图片）与切换**同阶段交付**：不改 quartet 发送通道，由 eino-cli 解析 prompt 中的图片标签、还原为 content block；**音视频暂不支持**。
- 工具执行全部发生在 eino-cli 进程内部：eino-cli 自带一套本地 sandbox（与 `pkg/sandbox` local 后端基于同一 sandbox SDK，不 fork `pkg/sandbox` 代码本身；工具经其 MCP tool server 提供），直接读写主机文件系统；container/compose 与回收恢复等能力不搬迁，quartet 后端保留 `pkg/sandbox` 供文件浏览器与 workspace 使用。

## 3. 关键取舍（已拍板，锁定）

1. **上下文归属：eino-cli 自管自存。** 会话上下文在 eino-cli 内存中持有；历史压缩 / summary 是 eino-cli 内部行为；会话持久化到 **`~/.eino/`**（位置可用 `EINO_HOME` 环境变量覆盖；只是借用 home 下独立目录的位置约定，内部存储结构由 eino-cli 自定义、不需要与 `~/.claude` 一致；quartet 不指定路径、不解读、不负责级联清理，残留累积与 claude 等 agent 一样由用户自理）；ACP 的建会话 / 加载 / 恢复等会话能力由 eino-cli **自己实现**。
   - quartet 侧后果：`messages.jsonl` 对 eino 变成“从线上 ACP 事件重建的镜像”，与 claude 一致；**删除 eino 专属的 summary 投影能力**；旧 eino 会话切换前由用户手动清除，无历史迁移负担（详见非目标）。
2. **配置归属：eino-cli 自管配置。** 模型目录、密钥、系统提示词都归 eino-cli 存储与管理，quartet **不做任何注入**。quartet 设置页把 eino 相关配置（模型配置、系统提示词等）单独抽一个 tab：后端经 exec `eino-cli models {add|list|delete}` / `eino-cli systemprompt {get|set}`（JSON 输出）子命令读写 eino-cli 的配置；模型选择在会话内走 ACP config option，与现有 ACP agent 的呈现完全一致（下拉候选由 probe 探测 ACP 配置项得到）。配置变更在下次建会话时生效，不影响正在运行的会话子进程。
   - 后果：密钥不出 eino-cli 进程，“密钥出进程”风险消除。
3. **多模态保图片、暂弃音视频。** quartet 的 ACP prompt 通道维持现状（纯文本，图片降级为文本标签）**不变**；由 eino-cli 负责解析 prompt 中的图片标签、重新加载为图片 content block 输入模型，保证图片输入能力不回退。音视频输入本次不支持（接受回退，需要时后续补）。

## 4. 目标架构

### 4.1 eino-cli（独立二进制，源码集中、可整体抽离）

- 入口 `cmd/eino-cli`，其余全部实现集中在统一目录；**不 import quartet 后端的任何包**——现存对 quartet 存储 / 路径等包的依赖以 **fork（复制一份指向 `~/.eino/` 的独立实现）** 处理，quartet 后端保留各原件：`chatctx` + `ChatContextRepo` 的存储形状、中间件链、`round` 在 eino 侧各 fork 一份（`round` fork 用于 eino-cli 自持久化 `~/.eino/` 会话；quartet 侧 messages.jsonl 镜像仍由现有 ACP 层负责，见 §4.2）；local sandbox 不 fork `pkg/sandbox` 代码，直接基于同款 sandbox SDK。整体结构按“日后可原样抽到独立仓库”设计。
- **完整适配 ACP 公共协议**，无私有扩展；与 claude 等外部 agent 在 quartet 侧无任何差别待遇。
- **自管配置体系**：模型目录、密钥、系统提示词自读自管；提供 `eino-cli models {add|list|delete}` 与 `eino-cli systemprompt {get|set}` 子命令（JSON 输出）供 quartet 设置页 eino tab 经后端 exec 对接；另实现 `-p` headless 一次性输出，供 quartet 标题/IM 等文本生成的通用 headless 通道使用（与 claude/gemini 的 `-p` 一致，非 eino 专属通道）。
- **自存会话**：`~/.eino/` 持久化；自实现 ACP 的建会话 / 加载 / 恢复 / 提问 / 取消 / 配置项能力。
- **多模态还原**：解析 prompt 中的图片标签，加载为 content block 输入模型。
- **自带 local sandbox**：与 `pkg/sandbox` local 后端基于同一 sandbox SDK（不 fork `pkg/sandbox` 代码本身），工具经其 MCP tool server 在主机进程内执行、直接读写主机文件系统；container/compose 与回收恢复等能力不搬迁，未来需要时单独设计。

### 4.2 quartet 接入层（quartet 后端与 eino 相关的全部内容）

quartet 不再持有 eino 的任何实现，只保留对“一个普通 ACP agent”的接入配置：

- **构建安装**：新增 `make build-eino-cli`，构建仓库内 `cmd/eino-cli` 并安装到 `$PATH`（probe 按 PATH 查找探测）。
- **probe 注册**：`eino-cli` 进 known agents 白名单与探测列表。
- **agent 列表统一**：eino-cli 呈现为一个普通 agent 条目；模型选择与现有 ACP agent 一致（会话内 config option），删除“每个配置模型展开成一行”的专属逻辑。
- **设置页 eino tab**：模型配置、系统提示词等 eino 相关配置单独抽一个 tab；quartet 后端经 exec `eino-cli models {add|list|delete}` / `eino-cli systemprompt {get|set}`（JSON）读写，不落 quartet 存储。probe 只负责探测下拉候选，tab 子命令才做增删改，两条通道独立。

### 4.3 quartet 内被删除的 eino 路径

- in-process eino 运行时与会话管理。
- eino 专属聊天上下文组装与本地历史存储耦合。
- agent type 分流常量与 RUN 分发里的 eino 分支（分发收敛为单一 ACP 路径）。
- handler 与 agent 列表中的 eino 特判（含“每个配置模型展开成一行”的专属逻辑）。
- eino 专属 summary 投影（历史接口的 summary+tail 投影、repository 的 summary 读写 API、前端 is_summary 渲染标记）——历史展示与 claude 一致，按 ACP 事件镜像原样投影。
- Shell 节点 transcript 会话的类型标记不再复用 eino agent 类型，改为独立的非 agent 标记；历史渲染走标准会话路径，存量会话不受影响。
- eino 链路上的中间件链（含 sandbox backend 中间件）——整体搬到 eino-cli，quartet 后端不再保留；但 `pkg/sandbox` 本身**不删除**（文件浏览器与 workspace 仍在用）。
- eino 模型构建器原件 `pkg/modelbuilder`（fork 副本归 eino-cli）。
- 设置页中旧的 eino 模型配置入口（由 eino tab 取代）。
- 前端所有 eino 专属分支。

## 5. 分阶段落地（一步到位，不保留 in-process 中间态）

按用户决定，不做“先抽核心仍 in-process 挂着”的安全垫阶段；抽离与 ACP server 壳合并成一步，直接切子进程。

- **Phase 1 — 产出 eino-cli**：建 `cmd/eino-cli` 入口与统一实现目录；先产出两份清单——eino **能力清单**（推理、中间件、多模态、压缩、会话恢复等）与**依赖清单**（eino 链路对 quartet 后端各包的引用）——作为逐项对齐依据；把 eino 功能整体搬入并逐一 fork（chatctx/存储/中间件/round 各复制一份指向 `~/.eino/`），直至与 quartet 后端零依赖；落地 `~/.eino/` 会话持久化与自实现 load/resume、自管配置与 `eino-cli models` / `eino-cli systemprompt` 子命令（JSON）、prompt 图片标签解析还原（读不到则静默降级）；eino-cli 基于 sandbox SDK 接入 local sandbox（container/compose 不搬）；quartet 侧新增 `make build-eino-cli` 并安装到 `$PATH`。
  - **完成判据**：eino-cli 不引用 quartet 后端任何代码；任意 ACP client（含本地脚本）可独立驱动 eino-cli 跑通单轮/多轮对话、带图片标签 prompt 的图片输入、进程重启后的会话 resume。
- **Phase 2 — quartet 接入并删除 eino 路径（同阶段完成）**：`make build-eino-cli` 安装 + probe 注册 + agent 列表统一 + dispatch 收敛为单一 ACP 路径 + 设置页 eino tab；同时移除 quartet 后端与前端全部 eino 专属实现与特判。此阶段结束后 quartet 后端不再存在 eino 路径。
  - **完成判据**：全仓搜索无 eino 专属残留（`cmd/eino-cli` 及其实现目录、接入配置、文档除外）；前端发图 → eino-cli 回复的端到端链路跑通。
- **后续项（不在本方案排期，需要时单独立项）**：usage/token 统计上报（当前按模型归因空档，见非目标）；container/compose sandbox；音视频输入。

> 取舍提示：省掉 in-process 中间态意味着切换是“硬切”——Phase 1 的 eino-cli 必须在本地被独立驱动、验证充分后，Phase 2 再一次性接入并删除旧路径，中途没有可回退到 in-process 的检查点。

## 6. 非目标

- 不改 SSE / 事件 buffer / 事件构建 / 协议中立事件层的契约——全部复用。
- 不动其它 ACP agent 的行为；**不扩展、不修改 `pkg/acp` 公共通道**——多模态等能力由 eino-cli 侧适配标准协议。
- 不引入任何私有协议；quartet 与 agent 的对接只有 ACP 一条通道。
- 不支持音视频输入（暂）。
- 不做 `~/.eino/` 的级联清理（与 claude 等 agent 的 `~/.claude` 一致，用户自理）。
- eino-cli 暂不上报 usage/token 统计（实时 token 计数由 quartet ACP 侧本地 tokenizer 兜底、不会空档；真正空档在**按模型的用量归因**——会话内经 ACP config option 选的模型可能对应不回旧的数字 ID 统计口径，统计页可能落到未知模型桶）。
- 不搬 container/compose sandbox：eino-cli 只 fork local 后端；容器化 / compose / 回收恢复等未来需要时单独设计。
- 不做旧 eino 会话数据迁移：切换前由用户**手动清除**旧 eino 会话（否则残留会话 resume 时 `~/.eino/` 无对应上下文，等同上下文丢失）。
- 不追求 in-process 与子进程并存（末态只保留子进程一条路径）。

## 7. 风险与需验证项

- **功能完整性**：搬迁前必须产出 eino 能力清单与依赖清单，Phase 1 逐项对齐，避免漏搬造成功能静默丢失。
- **上下文自持久化**是 eino-cli 的新增职责：`~/.eino/` 写入、子进程死亡 / quartet 重启后的 resume 语义要跑通，避免上下文丢失。
- **配置接口契约**：配置 CRUD 走 quartet 后端 exec `eino-cli models {add|list|delete}`（JSON 输出）——ACP config option 只做“从已广播候选里选模型”、不含增删改，故 CRUD 必然走子命令；JSON 契约需稳定。probe 探测候选、tab 子命令写配置是两条独立通道。
- **图片标签解析健壮性**：多模态还原依赖 quartet 现有“图片降级为文本标签”的格式约定；标签内已带**绝对路径**，eino-cli 直接按路径读文件、不需知道 `LOCAL_MEMORY`。文件读不到时**静默降级**（标签原样留在文本里交给模型），不报错、不阻断。
- **summary 能力迁移**：确认删掉 quartet 的 eino summary 投影后，前端历史展示不出现空档（token 统计空档为已知接受项，见非目标）。
- **sandbox 双份维护**：eino-cli 自带的 local sandbox 封装与 quartet 保留的 `pkg/sandbox` 从此各自演进（底层同为 sandbox SDK local 实现），一边修 bug 另一边不自动同步——这是“日后抽独立仓库”目标下可接受的代价。工具直接读写主机文件系统，按 quartet 个人电脑、单用户的安全假设可接受。
- **性能**：相对 in-process，多了子进程 spawn + JSON 序列化开销；需确认对首轮延迟可接受。
- **清理面**：Phase 2 结束后需全仓搜索确认 quartet 后端与前端无 eino 残留分支（`cmd/eino-cli` 及其实现目录除外）。
  - 落地后复查（2026-07-25）：全仓无 eino 专属分支残留，唯有两处非功能性陈旧引用未随删除同步——访问日志跳过名单仍指向已删的旧模型配置路由、澄清结点取末条回复的函数注释仍描述已删的 summary 跳过行为；均已修正，不影响运行行为。
  - 二次复查（2026-07-25，code-review 双轴 + 逐条核实后）：又发现同类残留——多处注释仍以已删除的 eino summary 机制（summary.json、summary 压缩/投影）描述指纹漂移、会话锁保护范围与消息去重理由，涉及 chat_context / session_locks / acp agent / job_message 共 7 处注释；已改为按当前 ACP 镜像语义重述，纯注释、不改行为。另修正一处并发缺陷：eino 配置服务惰性解析 eino-cli 路径时对缓存字段无锁读写，多个配置接口并发调用会触发数据竞争，已加锁串行化（`go test -race` 口径）。


## 8. 实现备注（非阻塞，实现时定）

以下不影响方案定稿，留给实现阶段决定：

- **`~/.eino/` 内部存储结构** 由 eino-cli 自定义，不要求与 `~/.claude` 一致；只需支撑自实现的 load/resume。
- **推理事件 → ACP 标准事件的翻译适配** 是 Phase 1 的核心实现活（相当于现有 ACP client 侧的反向映射），已含在“完整适配 ACP 公共协议”内。
- **thinking 档位** 可选择性映射到 ACP 思考档位配置项呈现；不做也不阻断主链路，可在 Phase 1 顺带交付。

## 9. 关联文档

- `docs/arch/acp-agent-message-flow.md` — ACP 分支现有链路（本方案复用）。
- `docs/arch/message-to-sse-pipeline.md` — eino / acp 共用的事件与 SSE 通道（本方案不动）。
