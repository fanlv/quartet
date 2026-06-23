# LangChain: The Art of Loop Engineering 文章分析

> 原文链接：<https://www.langchain.com/blog/the-art-of-loop-engineering>  
> 文章发布日期：2026-06-16  
> 分析日期：2026-06-22  
> 发布方：LangChain Blog  
> 主题：Agent 工程、Loop Engineering、LangChain/LangSmith agent 原语

## 一句话概括

这篇文章的核心观点是：可靠的 Agent 不是只靠更强的模型，而是靠围绕模型构建多层循环系统。Agent 的价值来自它所在的 loop：先让模型反复调用工具完成任务，再用验证循环保证质量，再通过事件驱动把 Agent 嵌入真实业务系统，最后用生产 traces 反向改进 prompt、工具、grader、记忆甚至模型本身。

## 文章在讲什么

文章讨论的是 “loop engineering”，也就是为 Agent 设计、组合和优化多层循环。LangChain 认为，Agent 的基本形态虽然很简单：给 LLM 上下文，让它循环调用工具直到任务完成；但在生产环境里，仅有这个内层 loop 远远不够。

作者引用 swyx 的 “loopcraft: the art of stacking loops” 观点，强调可以通过堆叠不同层级的 loop 来构建更有效的 Agent。文章用一个内部文档 Agent 作为贯穿示例：这个 Agent 接收文档改进请求，读取代码库和文档，修改文件，提交 PR，并在后续 loop 中被检查、触发和持续优化。

文章最终把 Agent 系统拆成四层循环：

1. Agent loop：模型调用工具，完成任务。
2. Verification loop：检查输出，不合格就带反馈重试。
3. Event-driven loop：由事件、计划任务、webhook、消息频道等自动触发 Agent。
4. Hill climbing loop：分析生产运行 traces，自动提出或执行对 harness 的改进。

## 核心观点

### 1. 模型能力不是 Agent 可靠性的全部

文章开头明确指出，Agent 能自动化真实世界的工作，是因为它能采取行动。但让它稳定地产生价值，不只是选择一个强模型，还需要一个针对任务精心设计的 harness。

这里的 harness 可以理解为围绕模型的运行框架，包括：

- 给模型的上下文和 prompt。
- 可调用工具集合。
- 输出检查和评分机制。
- 触发方式。
- 运行 traces 的采集和分析。
- 人类审核点。
- 后续自动改进机制。

也就是说，Agent 工程的重点不是 “把模型接上工具” 就结束，而是要设计一套让 Agent 可运行、可验证、可集成、可迭代的系统。

### 2. Agent 的基本 loop 是工具调用循环

第一层 loop 是最基础的 Agent：模型拿到任务和上下文后，决定是否调用工具；工具返回结果后，模型继续思考和调用工具，直到认为任务完成。

LangChain 对应的原语是 `create_agent`。文章说，只要选择一个模型，接入一组工具，就能得到一个工作中的 agent loop。工具是 Agent 能对真实世界产生影响的关键，例如：

- clone 仓库。
- 读取文件。
- 修改文档。
- 打开 Pull Request。
- 查询外部系统。

这层 loop 解决的是 “能不能做事”。但它本身不保证做得对、做得一致，也不保证能嵌入真实业务流程。

### 3. Verification loop 把 Agent 从 “能做” 推向 “做得可靠”

第二层是验证循环。文章指出，Agent loop 能完成工作，但不一定第一次就正确或稳定。因此，当一致性和质量重要时，需要在 Agent 外面包一层验证机制。

Verification loop 的模式是：

1. Agent 执行任务并产出结果。
2. Grader 根据 rubric 检查结果。
3. 如果不合格，把错误和反馈传回模型。
4. Agent 基于反馈再次尝试。

Grader 可以是确定性的，例如测试、链接检查、CI、规则校验；也可以是 agentic 的，例如 LLM-as-a-judge。

文章提到 LangChain 的 `RubricMiddleware` 可以处理这种模式，也可以用 `create_agent` 的 `after_agent` hook 自己接。文档 Agent 的例子里，grader 会检查：

- 文档链接是否可访问。
- CI 是否通过。
- diff 是否只覆盖用户要求的范围。

这层 loop 的价值是减少人工检查那些可自动判断的问题。它的代价是增加延迟和调用成本。文章的判断是：当质量比速度更重要时，尤其是大部分生产用例里，这个成本是值得的。

### 4. Event-driven loop 让 Agent 成为系统的一部分

第三层是事件驱动循环。文章认为 Agent 开发里非常重要的一部分是 integration layer：把 Agent 连接到真实生态系统，让它在后台运行。

在这一层，Agent 不再是一个人手动点击或调用的工具，而是被外部事件触发的系统组件。触发源可以包括：

- 新文档出现。
- 定时任务触发。
- webhook 到达。
- Slack 频道收到消息。
- 其他业务系统事件。

LangChain 对应提到的能力是 LangSmith Deployment 的 cron 和 webhook，以及 Fleet 的 channels 和 schedules。文档 Agent 的例子中，LangChain 团队用 Fleet 构建 Agent，并配置一个 Slack channel：只要 `#docs-plz` 频道里有消息，就触发文档 Agent 运行。

这一层 loop 的意义是规模化。它把 Agent 从 “被人调用的工具” 变成 “持续运行的业务自动化单元”。

### 5. Hill climbing loop 是最关键的复利层

第四层是 hill climbing loop，文章认为它可能是最重要的一层。前三层主要是在自动化工作，第四层则是在自动化改进。

每次 Agent 运行都会产生 trace，其中包含：

- 模型做了什么决策。
- 调用了哪些工具。
- 工具返回了什么。
- Grader 给了什么反馈。
- 哪些地方失败或表现不好。

这些 traces 是高价值信号，能反映 Agent harness 哪里有问题。Hill climbing loop 会让一个分析 Agent 读取这些 traces，总结问题，并提出或执行 harness 配置改进，例如：

- 改 prompt。
- 调整工具描述或工具集合。
- 改 grader/rubric。
- 改 memory 或检索到的 skills。
- 对 open-weight model 使用 trace/eval 结果做 RL fine-tuning。

LangSmith 对应的产品是 Engine，也就是 trace analysis agent。文章的文档 Agent 示例里，Engine 会分析文档 Agent 的 traces；当多个 traces 指向某个潜在问题时，它会创建 issue，要求修改有问题的 prompt 或工具。

这层的关键不是简单地 “失败后重跑”，而是外层 loop 会回写和更新内层 agent loop。每次循环都让底层 Agent 变得更好，因此具备复利效应。

## 四层 Loop 对照表

| 层级 | 作用 | 直接影响 | LangChain/LangSmith 原语 |
| --- | --- | --- | --- |
| 1. Agent loop | 模型反复调用工具直到任务完成 | 自动化单次工作 | `create_agent`、LangChain 支持的任意模型 |
| 2. Verification loop | 用 rubric/grader 检查输出，不合格则带反馈重试 | 提升正确性和一致性 | `RubricMiddleware`、`after_agent` hook |
| 3. Event-driven loop | 由事件、cron、webhook、频道消息触发 Agent | 在真实系统中规模化自动化 | LangSmith Deployment、cron、webhooks、Fleet channels |
| 4. Hill climbing loop | 分析生产 traces，改进 Agent harness | 持续优化 Agent 能力 | LangSmith Engine |

## Human-in-the-loop 的位置

文章特别强调，自动化不等于移除人。人类判断在每一层都有自然的位置。

作者举了一个很典型的区别：自动 grader 可以检查链接是否可访问，但判断一段文档的表达是否适合目标读者，仍然需要人类的上下文、经验和审美。

文章建议在以下位置加入人类审核：

- Agent loop：敏感工具调用前要求人工确认。
- Verification loop：对敏感流程由人类担任 grader。
- Application/event loop：Agent 输出返回最终用户前由人类批准。
- Hill climbing loop：harness 改进在部署前经过人工 review。

这给出的工程启发是：Human-in-the-loop 不应该只是最后一道兜底，而应该被作为系统原语设计进每层 loop。

## 文章背后的产品叙事

这篇文章也是一篇 LangChain/LangSmith 产品叙事文章。它不是单纯讨论抽象架构，而是把四层 loop 映射到 LangChain 生态里的具体原语：

- 基础 Agent：LangChain `create_agent`。
- 验证：`RubricMiddleware`。
- 事件触发和部署：LangSmith Deployment、cron、webhooks。
- 无代码/频道化 Agent：Fleet。
- 自动改进：LangSmith Engine。

因此文章想传达的不只是 “Agent 需要 loop”，还包括 “LangChain 正在把这些 loop 产品化和平台化”。它把 Agent 工程从框架层推进到平台层：不只帮你写 Agent，也帮你部署、触发、观测、评估和改进 Agent。

## 我的分析：这篇文章真正重要的地方

### 1. 它把 Agent 工程的关注点从 prompt 转向系统设计

很多 Agent 失败案例不是模型完全不会，而是系统没有给它足够的结构：

- 没有可检查的完成标准。
- 没有失败反馈。
- 没有自动重试。
- 没有生产运行数据。
- 没有把运行数据变成改进信号。

这篇文章把这些问题统一放到 loop engineering 里解释，比较有启发性。它提醒我们：prompt 和工具只是第一层，真正的 Agent 产品需要闭环。

### 2. 第三、第四层是区分 demo 和生产系统的关键

文章最后明确说，大家已经讨论 loop 1 和 loop 2 很久了，现在更应该关注 loop 3 和 loop 4。

这点很关键。很多 Agent demo 停留在 “输入任务 -> Agent 跑一下 -> 输出结果”。这最多覆盖了第一层，最多再加一点第二层验证。生产系统的差别在于：

- 它是否能被业务事件自动触发。
- 它是否在用户真实工作流里运行。
- 它是否持续收集 traces。
- 它是否能基于 traces 改进自身。

如果没有第三层，Agent 只是工具；如果没有第四层，Agent 不会形成持续改进的组织能力。

### 3. Hill climbing loop 的挑战会在治理和变更管理

文章对第四层的描述很吸引人，但真正落地时也最难。因为让分析 Agent 自动修改 prompt、工具或 grader，本质上是在改生产系统行为。这会带来几个问题：

- 如何确认 trace 中的问题是真问题，而不是偶发样本？
- 自动生成的 harness 改动如何评估？
- 是否需要灰度发布？
- 如何回滚？
- 谁批准这些改动？
- 如果 grader 本身错了，会不会优化错方向？

所以 hill climbing loop 不能只靠 “自动分析 + 自动修改”，还需要评估、版本控制、审批、灰度、回滚和审计。文章也通过 human review 提到这一点，但没有展开治理细节。

### 4. Grader/rubric 会成为 Agent 工程的核心资产

第二层和第四层都依赖 grader。Verification loop 用 grader 判断结果是否合格；Hill climbing loop 用 traces 和 eval 结果判断哪里要改。

这意味着 rubric 不只是测试用例，而是组织把经验固化成机器可执行判断的方式。谁能定义更好的 rubric，谁就能更稳定地改进 Agent。

对于真实业务来说，rubric 可能比 prompt 更难写，因为它要求团队明确 “什么叫好”。例如文档 Agent 的 “diff 是否 scoped” 可以自动判断，但 “解释是否适合目标读者” 就更依赖领域经验。

### 5. Loop engineering 和组织学习有关

文章结尾引用 Satya 的观点：早期构建 learning loops 的公司，会把人类判断和 token capital 结合起来，形成难以复制的优势。

这里的重点是组织学习。Agent 每次运行都不只是完成一次任务，还应该为系统留下可复用信号。如果这些信号能持续进入 prompt、工具、memory、eval、模型训练，那么组织会积累一种新的资产：自动化经验。

## 对我们构建 Agent 系统的启发

如果把这篇文章用于指导实际工程，可以转成下面几个设计问题：

1. 当前 Agent 有没有明确的工具调用 loop？
2. 每个任务有没有可执行的完成标准？
3. 哪些错误可以由 deterministic grader 检查，哪些必须由 LLM judge 或人类检查？
4. Agent 是手动触发，还是已经接入 cron、webhook、IM、工作流事件？
5. 每次运行是否保存足够完整的 trace？
6. traces 是否被定期分析，能否转化成 prompt、工具、grader、memory 的改进？
7. 改进是否有 review、评估、灰度和回滚机制？
8. 哪些动作必须有人类审批？

这套问题比 “模型选哪个” 更接近生产 Agent 的关键。

## 可能的局限和未展开部分

文章更偏框架和产品叙事，没有深入展开下面这些问题：

- 多层 loop 的成本模型如何计算。
- Verification loop 失败多次后如何停止。
- Grader 质量如何验证。
- Hill climbing 的自动改动如何做实验设计。
- 自动改进和人工审批之间的具体流程。
- 在高风险业务里如何做审计和合规。
- 多 Agent、多团队共享 traces 时如何避免优化目标冲突。

这些不是文章的缺点，但说明真正落地 loop engineering 时，需要补充工程治理和平台能力。

## 结论

《The Art of Loop Engineering》想表达的是：Agent 的潜力不在单次模型调用，而在围绕模型构建的多层循环。第一层让 Agent 能做事，第二层让它做得更可靠，第三层让它嵌入真实系统持续运行，第四层让它基于生产经验不断自我改进。

这篇文章对 Agent 工程的最大启发是：不要把 Agent 当成一个 prompt 或一个函数，而要把它当成一个有反馈、有触发、有观测、有改进机制的长期运行系统。真正的竞争优势来自 learning loops，而不是一次性的模型包装。
