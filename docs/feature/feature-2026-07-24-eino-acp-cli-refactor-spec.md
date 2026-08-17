# eino Agent 走 ACP 子进程化重构 — 需求规格（Spec）

> 配套设计文档：`docs/feature/feature-2026-07-24-eino-acp-cli-refactor.md`（背景、关键取舍、目标架构、分阶段落地的完整推演）。本 spec 是在其之上收敛出的、面向实现与验收的规格。

## 问题陈述（Problem Statement）

当前 quartet 内置两套 agent runtime，靠"会话是不是 eino"做分流。eino 这套推理循环、中间件、聊天上下文组装、会话管理全部长在 quartet 后端里，并直接依赖 quartet 的存储、模型配置、路径、沙箱等包。由此产生的痛点：

- **对维护者**：eino 无法独立移植；agent 列表、dispatch、多个 handler 里到处是 eino 特判；eino（磁盘即事实来源、每轮重载）与 ACP（子进程自管上下文、messages.jsonl 只是镜像）两种模型并存，认知与维护成本翻倍。
- **对使用者**：eino 的每个已配置模型在 agent 选择器里各占一行，把选择器撑爆，且行为与其他 ACP agent 不一致；模型密钥保存在 quartet 的配置文件里（明文、出进程），暴露面偏大。

## 解决方案（Solution）

把 quartet 内**所有 eino 相关功能**整体抽出，编译成独立二进制 `eino-cli`，它的**会话通道只有 ACP 一条**，被 quartet 当成"一个普通的 ACP agent"接入——与其他 ACP agent 走完全相同的接入、缓存、事件、存储镜像链路。会话之外另有一小组非会话 CLI 面（均与其他 agent CLI 的同款惯例一致，非私有协议）：配置管理子命令 `models` / `systemprompt`，以及 `-p` headless 一次性输出。

对使用者：eino 在选择器里收敛为**单个条目**，模型在条目内下拉切换（与其它 ACP agent 一致）；图片输入不回退；密钥只留在 eino-cli 进程内。对维护者：quartet 后端与前端**不再保留任何 eino 专属路径**，dispatch 收敛为单一 ACP 路径；eino-cli 结构按"日后可整体抽到独立仓库"设计。

## 用户故事（User Stories）

1. 作为 quartet 用户，我希望 eino 在 agent 选择器里只呈现为一个普通条目（而不是每个模型一行），这样选择器不再被撑爆、与其他 ACP agent 一致。
2. 作为 quartet 用户，我希望在 eino 条目内的下拉里切换模型，这样切换体验和其它 ACP agent 完全一致。
3. 作为 quartet 用户，我希望和 eino-cli 进行单轮对话并看到流式回复，这样基本聊天能力不回退。
4. 作为 quartet 用户，我希望和 eino-cli 进行多轮对话且上下文连续，这样它记得之前说过的话。
5. 作为 quartet 用户，我希望给 eino-cli 发图片并得到基于图片内容的回复，这样多模态输入不回退。
6. 作为 quartet 用户，当我发的图片文件已不可读时，我希望对话仍能继续（图片静默降级为文本、不报错阻断），这样不会因为一张图卡住整轮。
7. 作为 quartet 用户，我希望在 quartet 或 eino-cli 子进程重启后，eino 会话仍能续上上下文，这样重启不丢历史。
8. 作为 quartet 用户，我希望 eino 会话的历史在前端正常展示、不出现空档，即使内部做过历史压缩。
9. 作为 quartet 用户，我希望在设置页的 eino tab 里新增一个模型（含 provider、密钥、base url 等），这样我能自助配置 eino 可用的模型。
10. 作为 quartet 用户，我希望在 eino tab 里查看已配置的模型列表。
11. 作为 quartet 用户，我希望在 eino tab 里删除一个模型配置。
12. 作为 quartet 用户，我希望在 eino tab 里配置 eino 的系统提示词。
13. 作为 quartet 用户，我希望我新增的模型在下次打开 agent 选择器时出现在 eino 的模型下拉里。
14. 作为 quartet 用户，我希望我的模型密钥只保存在 eino-cli 内、不进入 quartet 的配置文件，这样降低密钥暴露面。
15. 作为 quartet 用户，我希望 eino-cli 的工具（读写文件、执行命令等）直接作用在我的主机工作目录上，这样它产出的文件我能在 quartet 文件浏览器里看到。
16. 作为 quartet 用户，我希望 eino-cli 读取工作目录下的 AGENTS.md / AGENTS.local.md 作为项目指令，这样项目约定继续生效。
17. 作为 quartet 用户，我希望 eino 长对话超过阈值时自动压缩历史，这样长会话不因超出上下文而失败。
18. 作为 quartet 用户，我希望即便 eino-cli 不上报官方用量，前端仍显示一个实时 token 估算，这样我对消耗有感知。
19. 作为 quartet 用户，我希望随时取消正在进行的 eino 生成，这样我能打断跑偏的回答。
20. 作为维护者，我希望所有 eino 功能集中在独立二进制 eino-cli 里、与 quartet 后端零 import 依赖，这样它日后可整体抽到独立仓库。
21. 作为维护者，我希望 quartet 后端和前端不再有任何 eino 专属分支/特判，这样 dispatch、agent 列表、handler 收敛为单一 ACP 路径。
22. 作为维护者，我希望 eino-cli 通过标准 ACP 协议接入、不扩展 `pkg/acp`、不引入私有协议、不再保留 eino 运行时依赖（`eino/schema` 消息类型仍作共享消息表示使用），这样接入面收敛为一条通道。
23. 作为维护者，我希望通过 `make build-eino-cli` 构建并安装 eino-cli 到 `$PATH`，这样 probe 能探测到它。
24. 作为维护者，我希望 eino-cli 作为一个 known ACP agent 被 probe 自动探测、并广播其模型候选，这样它免特判地出现在 agent 列表。
25. 作为维护者，我希望 quartet 保留 chatctx / round / repository / pkg/sandbox 等共享包（eino 侧按需持有 fork 副本），这样其他 ACP agent、文件浏览器、workspace 不受影响。
26. 作为维护者，我希望 messages.jsonl 对 eino 退化为"从 ACP 事件重建的镜像"，与 claude 走同一套镜像/漂移/resume 逻辑，这样无需为 eino 单独维护存储路径。
27. 作为维护者，我希望切换前手动清除旧 eino 会话，这样不会有残留会话 resume 时得到空上下文。
28. 作为集成方（任意 ACP client），我希望能独立驱动 eino-cli 跑通建会话/提问/取消/加载/恢复/配置项，这样 eino-cli 不依赖 quartet 也能被复用。
29. 作为维护者，我希望 eino-cli 自实现 ACP 的 load/resume，以 `~/.eino/` 作为其上下文事实来源，这样进程重启后能恢复。
30. 作为维护者，我希望配置变更（增删改模型/密钥）在下次建会话时生效、不影响正在运行的会话，这样行为可预期。

## 实现决策（Implementation Decisions）

1. **独立二进制 eino-cli**：承载全部 eino 能力（推理循环、中间件链、聊天上下文组装、会话管理、多模态还原、历史压缩），作为 ACP server，可被任意 ACP client 驱动；与 quartet 后端零 import 依赖；实现集中在统一目录，结构按"整体抽到独立仓库"设计。
2. **接入通道唯一为 ACP**：不扩展/不修改 `pkg/acp`，不引入私有协议，不保留 eino 运行时依赖（`eino/schema` 消息类型保留为共享消息表示）；eino-cli 与其他 ACP agent 走完全相同的接入、缓存、事件、存储镜像链路，quartet 侧无差别待遇。会话之外的非会话 CLI 面仅两个：配置子命令（决策 5）与 `-p` headless 一次性输出（决策 9），均为各 agent CLI 的同款惯例，不构成第二条会话通道。
3. **共享包处理（fork，不搬走）**：`chatctx` + 会话存储形状、eino 中间件链、`round` 在 eino 侧**各 fork 一份并指向 `~/.eino/`**（`round` fork 供 eino-cli 自持久化会话；quartet 侧 messages.jsonl 镜像仍由现有 ACP 层负责）；local sandbox 不 fork `pkg/sandbox` 代码，直接基于同款 sandbox SDK；quartet 保留全部原件供 ACP 分支、文件浏览器、workspace 使用。fork 副本与原件从此各自演进（可接受代价）。
4. **上下文归属：eino-cli 自管自存**。上下文在 eino-cli 内存持有；历史压缩/summary 是其内部行为；持久化到 `~/.eino/`（内部结构自定义，不要求与 `~/.claude` 一致）；自实现 ACP 建会话/加载/恢复。quartet 对 eino 的 messages.jsonl 退化为"从 ACP 事件重建的镜像"，复用现有指纹漂移检测 + resume/load 对齐；**删除 eino 专属 summary 投影**。
5. **配置归属：eino-cli 自管**。模型目录/密钥/系统提示词归 eino-cli 存储管理；quartet 设置页新增 eino tab，后端经 **exec `eino-cli models {add|list|delete}`（JSON 输出）子命令**读写；密钥不出 eino-cli 进程；配置变更下次建会话生效、不影响运行中的会话。
6. **模型选择走 ACP config option**（keyed by `model`）：下拉候选由 probe 探测会话 `ConfigOptions` 得到；删除"每个配置模型展开成一行"逻辑，eino-cli 呈现为单个 agent 条目。**probe 探测候选**与 **tab 子命令增删改**是两条独立通道。
7. **多模态保图片、弃音视频**：quartet 的 ACP prompt 通道维持现状（图片降级为带**绝对路径**的文本标签）；eino-cli 解析标签、按绝对路径直接读文件还原为图片 content block，**不需知道 `LOCAL_MEMORY`**；文件读不到则**静默降级**（标签原样留在文本里交给模型），不报错、不阻断；音视频不支持。
8. **sandbox 只保 local**：eino-cli 直接基于 sandbox SDK 的 local 后端（不 fork `pkg/sandbox` 代码），工具经其 MCP tool server 在主机进程内执行、直接读写主机文件系统，产出对 quartet 文件浏览器可见；container/compose/回收恢复不搬；quartet 保留 `pkg/sandbox` 供文件浏览器/workspace。
9. **接入装配**：`make build-eino-cli` 构建并安装到 `$PATH`；eino-cli 进 known ACP agents 白名单（自动获得执行 allowlist、`$PATH` 探测、probe 能力发现）；RUN 分发收敛为单一 ACP 路径。标题/IM 等一次性文本生成沿用 quartet 既有 headless 分发（probe 记录各 agent 的 plain CLI bin，统一以 `-p` 一次性调用）；eino-cli 实现同款 `-p`，接替被删除的 in-process 文本生成能力，与其他 ACP agent 无差别。
10. **quartet 内删除项**：in-process 运行时与会话管理、eino 专属上下文组装与本地历史耦合、agent type 分流常量与 RUN 的 eino 分支、handler 与 agent 列表的 eino 特判（含模型展开逻辑）、eino summary 投影、eino 中间件链（搬入 eino-cli）、旧 eino 模型配置入口、前端全部 eino 分支。
11. **落地方式：硬切**，不保留 in-process 中间态；Phase 1 产出并在本地独立验证 eino-cli，Phase 2 一次性接入并删除旧路径；切换前用户手动清旧 eino 会话；中途无回退到 in-process 的检查点。

## 测试决策（Testing Decisions）

**好测试的定义**：只验证外部可观察行为（协议输入/输出、UI 表现），不绑定内部实现细节；重构内部结构不应导致测试红。遵循项目约定——开发期不额外加单元测试，除非明确要求；eino-cli 内部模块不逐一写单测，验证集中在下面两个最高层、行为级 seam。

- **Seam 1（首选，最高层）— ACP 协议边界**：以任意 ACP client / 本地脚本把 eino-cli 当子进程通过 stdio JSON-RPC 驱动，覆盖建会话、单轮/多轮对话、带图片标签 prompt 的图片输入、取消、进程重启后的 load/resume、config option 选模型、`eino-cli models` 增删改列、`-p` 一次性输出。此 seam **完全不涉及 quartet**，即 Phase 1 完成判据。
  - 覆盖模块：eino-cli 全链路。
  - 先验（prior art）：probe 已有"程序化 spawn 一个临时 ACP 会话、读 `ConfigOptions`"的能力发现做法，可复用同类 ACP client 能力来驱动 eino-cli。
- **Seam 2（复用现有）— 前端 E2E（Playwright）**：覆盖 quartet 集成——eino-cli 呈现为单个 agent 条目、模型下拉切换、发图 → eino-cli 回复渲染、历史展示无空档、实时 token 计数出现。即 Phase 2 完成判据。
  - 覆盖模块：quartet 接入层 + 前端。
  - 先验（prior art）：`web/` 下现有的 Playwright E2E 用例。

**清理面校验**（非测试用例，但属验收）：Phase 2 结束后全仓搜索确认后端与前端无 eino 专属残留（`cmd/eino-cli` 及其实现目录、接入配置、文档除外）。

## 范围之外（Out of Scope）

- 不扩展/不修改 `pkg/acp` 公共通道；不改 SSE / 事件 buffer / 事件构建 / 协议中立事件层的契约（全部复用）。
- 不引入任何私有协议；quartet 与 agent 的对接只有 ACP 一条通道。
- 音视频输入（暂）。
- container/compose sandbox 及沙箱回收恢复能力（未来单独立项）。
- `~/.eino/` 的级联清理（与 `~/.claude` 一致，用户自理）。
- eino-cli 上报官方 usage/token 统计；接受**按模型的用量归因**空档（实时 token 计数由 quartet ACP 侧本地 tokenizer 兜底、不会空）。
- 旧 eino 会话数据迁移（切换前手动清）。
- in-process 与子进程并存（末态只保留子进程一条路径）。

## 补充说明（Further Notes）

- **搬迁前必须先产出两份清单**：eino 能力清单（推理、中间件、多模态、压缩、会话恢复等）+ 依赖清单（eino 链路对 quartet 后端各包的引用），Phase 1 逐项对齐，避免漏搬造成功能静默丢失。
- **eino-cli 内部推理事件 → ACP 标准事件的翻译适配**是 Phase 1 的核心实现活，相当于现有 ACP client 侧的反向映射；已含在"完整适配 ACP 公共协议"内。
- **thinking 档位**可选择性映射到 ACP `thought_level` 配置项呈现；不做也不阻断主链路，可在 Phase 1 顺带交付。
- **硬切无回退检查点**：Phase 1 的 eino-cli 必须本地独立验证充分（单轮/多轮、带图、重启 resume）后，Phase 2 再一次性接入并删除旧路径。
- **性能**：相对 in-process，多了子进程 spawn + JSON 序列化开销，需确认对首轮延迟可接受。
- 关联文档：`docs/arch/acp-agent-message-flow.md`（ACP 现有链路，复用）、`docs/arch/message-to-sse-pipeline.md`（事件与 SSE 通道，不动）。
