# eino Agent 走 ACP 子进程化重构方案

> 目标一句话：把 quartet 内**所有 eino 相关功能**整体抽出，编译成独立二进制 `eino-cli`（入口 `cmd/eino-cli`，实现集中存放、与 quartet 后端零依赖，日后可整体抽到独立仓库）；quartet 与 agent 的对接**只保留 ACP 一条通道**——无私有协议、无 eino sdk——仓库后端**不再保留任何单独的 eino 路径**。

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
- quartet 与 agent 的对接**只保留 ACP 一条通道**：不扩展 `pkg/acp`、不引入私有协议、不再有 eino sdk；`eino-cli` 与 claude/gemini 走**完全相同**的接入、缓存、事件、存储链路。
- `eino-cli` 自管配置（模型目录、密钥、系统提示词），自存会话（`~/.eino/`）；quartet 设置页新增 eino 配置 tab，经 eino-cli 的配置接口（JSON 交换）读写。
- quartet 后端**不再保留单独的 eino 路径**：in-process 运行时、eino 专属上下文组装、agent type 分流、handler 与 agent 列表特判、前端 eino 分支全部移除。quartet 对 eino 的唯一认知是“一个叫 eino-cli 的 ACP agent”。
- 多模态（图片）与切换**同阶段交付**：不改 quartet 发送通道，由 eino-cli 解析 prompt 中的图片/文件标签、还原为 content block；**音视频暂不支持**。
- 工具执行全部发生在 eino-cli 进程内部（直接读写主机文件系统）；sandbox 相关能力整体移除，不在本方案范围。

## 3. 关键取舍（已拍板，锁定）

1. **上下文归属：eino-cli 自管自存。** 会话上下文在 eino-cli 内存中持有；历史压缩 / summary 是 eino-cli 内部行为；会话持久化到 **`~/.eino/`**（与 `~/.claude` 同构，格式自定义——quartet 不指定路径、不解读、不负责级联清理，残留累积与 claude 等 agent 一致，用户自理）；ACP 的建会话 / 加载 / 恢复等会话能力由 eino-cli **自己实现**。
   - quartet 侧后果：`messages.jsonl` 对 eino 变成“从线上 ACP 事件重建的镜像”，与 claude 一致；**删除 eino 专属的 summary 投影能力**；旧 eino 会话已清除，无历史迁移负担。
2. **配置归属：eino-cli 自管配置。** 模型目录、密钥、系统提示词都归 eino-cli 存储与管理，quartet **不做任何注入**。quartet 设置页把 eino 相关配置（模型配置、系统提示词等）单独抽一个 tab，通过 eino-cli 的配置接口（JSON 交换）读写；模型选择在会话内走 ACP config option，与现有 ACP agent 的呈现完全一致。
   - 后果：密钥不出 eino-cli 进程，“密钥出进程”风险消除。
3. **多模态保图片、暂弃音视频。** quartet 的 ACP prompt 通道维持现状（纯文本，图片降级为文本标签）**不变**；由 eino-cli 负责解析 prompt 中的图片/文件标签、重新加载为图片 content block 输入模型，保证图片输入能力不回退。音视频输入本次不支持（接受回退，需要时后续补）。

## 4. 目标架构

### 4.1 eino-cli（独立二进制，源码集中、可整体抽离）

- 入口 `cmd/eino-cli`，其余全部实现集中在统一目录；**不 import quartet 后端的任何包**——现存对 quartet 存储 / 路径 / 沙箱等包的依赖逐一 port 化或随功能搬迁；整体结构按“日后可原样抽到独立仓库”设计。
- **完整适配 ACP 公共协议**，无私有扩展；与 claude 等外部 agent 在 quartet 侧无任何差别待遇。
- **自管配置体系**：模型目录、密钥、系统提示词自读自管；提供配置接口（JSON 交换）供 quartet 设置页 eino tab 对接。
- **自存会话**：`~/.eino/` 持久化；自实现 ACP 的建会话 / 加载 / 恢复 / 提问 / 取消 / 配置项能力。
- **多模态还原**：解析 prompt 中的图片/文件标签，加载为 content block 输入模型。
- **不含 sandbox**：工具直接在主机进程内执行、直接读写主机文件系统；sandbox 相关能力整体移除，未来需要时单独设计。

### 4.2 quartet 接入层（quartet 后端与 eino 相关的全部内容）

quartet 不再持有 eino 的任何实现，只保留对“一个普通 ACP agent”的接入配置：

- **构建安装**：新增 `make build-eino-cli`，构建仓库内 `cmd/eino-cli` 并安装到 quartet 可探测的位置。
- **probe 注册**：`eino-cli` 进 known agents 白名单与探测列表。
- **agent 列表统一**：eino-cli 呈现为一个普通 agent 条目；模型选择与现有 ACP agent 一致（会话内 config option），删除“每个配置模型展开成一行”的专属逻辑。
- **设置页 eino tab**：模型配置、系统提示词等 eino 相关配置单独抽一个 tab，对接 eino-cli 的配置接口。

### 4.3 quartet 内被删除的 eino 路径

- in-process eino 运行时与会话管理。
- eino 专属聊天上下文组装与本地历史存储耦合。
- agent type 分流常量与 RUN 分发里的 eino 分支（分发收敛为单一 ACP 路径）。
- handler 与 agent 列表中的 eino 特判（含“每个配置模型展开成一行”的专属逻辑）。
- eino 专属 summary 投影。
- eino 链路上的 sandbox backend 中间件（随 sandbox 一并移除）。
- 设置页中旧的 eino 模型配置入口（由 eino tab 取代）。
- 前端所有 eino 专属分支。

## 5. 分阶段落地（一步到位，不保留 in-process 中间态）

按用户决定，不做“先抽核心仍 in-process 挂着”的安全垫阶段；抽离与 ACP server 壳合并成一步，直接切子进程。

- **Phase 1 — 产出 eino-cli**：建 `cmd/eino-cli` 入口与统一实现目录；先产出两份清单——eino **能力清单**（推理、中间件、多模态、压缩、会话恢复等）与**依赖清单**（eino 链路对 quartet 后端各包的引用）——作为逐项对齐依据；把 eino 功能整体搬入并逐一 port 化，直至与 quartet 后端零依赖；落地 `~/.eino/` 会话持久化与自实现 load/resume、自管配置与配置接口（JSON）、prompt 图片/文件标签解析还原；移除 sandbox 相关代码；quartet 侧新增 `make build-eino-cli`。
  - **完成判据**：eino-cli 不引用 quartet 后端任何代码；任意 ACP client（含本地脚本）可独立驱动 eino-cli 跑通单轮/多轮对话、带图片标签 prompt 的图片输入、进程重启后的会话 resume。
- **Phase 2 — quartet 接入并删除 eino 路径（同阶段完成）**：`make build-eino-cli` 安装 + probe 注册 + agent 列表统一 + dispatch 收敛为单一 ACP 路径 + 设置页 eino tab；同时移除 quartet 后端与前端全部 eino 专属实现与特判。此阶段结束后 quartet 后端不再存在 eino 路径。
  - **完成判据**：全仓搜索无 eino 专属残留（`cmd/eino-cli` 及其实现目录、接入配置、文档除外）；前端发图 → eino-cli 回复的端到端链路跑通。
- **后续项（不在本方案排期，需要时单独立项）**：usage/token 统计上报（当前接受空档，见非目标）；sandbox 能力重新设计；音视频输入。

> 取舍提示：省掉 in-process 中间态意味着切换是“硬切”——Phase 1 的 eino-cli 必须在本地被独立驱动、验证充分后，Phase 2 再一次性接入并删除旧路径，中途没有可回退到 in-process 的检查点。

## 6. 非目标

- 不改 SSE / 事件 buffer / 事件构建 / 协议中立事件层的契约——全部复用。
- 不动其它 ACP agent 的行为；**不扩展、不修改 `pkg/acp` 公共通道**——多模态等能力由 eino-cli 侧适配标准协议。
- 不引入任何私有协议；quartet 与 agent 的对接只有 ACP 一条通道。
- 不支持音视频输入（暂）。
- 不做 `~/.eino/` 的级联清理（与 claude 等 agent 的 `~/.claude` 一致，用户自理）。
- eino-cli 暂不上报 usage/token 统计（接受前端 token 展示与用量统计对 eino 会话的空档）。
- 不做 sandbox：相关能力整体移除，未来需要时单独设计。
- 不做旧 eino 会话数据迁移（旧会话已清除，无历史包袱）。
- 不追求 in-process 与子进程并存（末态只保留子进程一条路径）。

## 7. 风险与需验证项

- **功能完整性**：搬迁前必须产出 eino 能力清单与依赖清单，Phase 1 逐项对齐，避免漏搬造成功能静默丢失。
- **上下文自持久化**是 eino-cli 的新增职责：`~/.eino/` 写入、子进程死亡 / quartet 重启后的 resume 语义要跑通，避免上下文丢失。
- **配置接口契约**：eino tab 与 eino-cli 之间的 JSON 契约需稳定；配置读写的通道形态（优先 ACP 标准能力，不足时以 eino-cli 子命令输出 JSON）实现时确定，避免滑向私有协议。
- **图片标签解析健壮性**：多模态还原依赖 quartet 现有“图片降级为文本标签”的格式约定；标签格式变化或文件不可读时必须有明确错误反馈，不能静默丢图。
- **summary 能力迁移**：确认删掉 quartet 的 eino summary 投影后，前端历史展示不出现空档（token 统计空档为已知接受项，见非目标）。
- **无沙箱隔离**：sandbox 移除后工具直接读写主机文件系统；按 quartet 个人电脑、单用户的安全假设可接受，沙箱化后续重做。
- **性能**：相对 in-process，多了子进程 spawn + JSON 序列化开销；需确认对首轮延迟可接受。
- **清理面**：Phase 2 结束后需全仓搜索确认 quartet 后端与前端无 eino 残留分支（`cmd/eino-cli` 及其实现目录除外）。

## 8. 关联文档

- `docs/arch/acp-agent-message-flow.md` — ACP 分支现有链路（本方案复用）。
- `docs/arch/message-to-sse-pipeline.md` — eino / acp 共用的事件与 SSE 通道（本方案不动）。
