# Loop eachRepeat 在外层 group 多次迭代下被错误复用会话的修复

- 创建日期：2026-05-08
- 作者：Sophie
- 相关文件：`services/job/executor_loop.go`
- 背景：用户使用 Cowork 调研模板，外层主循环 `iterationCount=10`、内层 `Round 1` 的 `repeatCount=1`、`roundMode=eachRepeat`，期望得到 10 个相互独立的会话；实际只创建了 1 个会话，后续 9 次迭代都在同一个会话里继续追加消息。

## 用户场景（现象）

- 流程配置：
  - 主循环 `group`：`iterationCount=10`
  - 子节点 `step Round 1`：`repeatCount=1`、`roundMode=eachRepeat`（"Round 每次循环重复新建"）
- 期望行为：主循环每迭代一次，Round 1 都在一个全新的会话里从头执行，总共 10 个 session。
- 实际行为：只有第一次迭代创建了新会话，后续迭代的 Round 1 都在这个首轮会话里继续执行，语义被错误地变成了"同一个会话里连续发送 10 条相同提示"。

## 根因定位

问题出在 Loop 执行引擎里"如何判断 step 处于真正的『恢复』状态"的逻辑。核心机制包含三个要素：

1. **当前会话指针**：Loop 主循环里有一个贯穿整个执行过程的"当前会话 ID"引用，随每一步的会话创建 / 复用而变化。
2. **Resume（恢复点）**：每结束一个 step，引擎会把"下一步应该跑的节点路径 + 下一步应该携带的会话 ID"写回 Job 的 Resume，用于 Stop/Continue 等场景。`eachRepeat` 的 step 结束时会把 Resume 里的 SessionID 清空，表示"下一个 step 应当重新起 session"。
3. **eachRepeat 新建会话的判定**：进入带 `eachRepeat` 的 step 时，为了兼容"步骤执行到一半被暂停，后来 Continue 恢复"的场景，引擎会做一次"是否正处于 resume 上"的判断：如果恢复指针正好停在这一步，并且"当前会话指针"还有值，则跳过新建、沿用旧会话。

三者叠加后出现漏洞：

- 外层 group 每次迭代都会递归进入子节点，重新读取 Job 的 Resume 作为本次递归的 `resumePath`。
- 上一轮迭代的 `advance_resume` 已经把 Resume 的 NextPath 指到"下一个会被执行到的 step 路径"——对于只有一个 step 的 group，这个"下一个 step"恰好就是**同一个 step 在下一次迭代中的路径**。
- 进入这个 step 时，引擎发现"路径和 resume 完全匹配"，于是把当前这次普通前进也误判成"从暂停恢复"。
- 而"当前会话指针"在上一次迭代结束时**没有被同步清空**（只有 Resume 里的 SessionID 被清了），所以判定条件里的"当前会话非空"仍然成立。
- 最终 eachRepeat 的新建分支被跳过，后续 9 次迭代全部复用第一次创建出来的会话。

换句话说，这里把两件完全不同的事情搞混了：

| 场景 | Resume 指向当前 step | 当前会话指针 | 期望行为 |
| --- | --- | --- | --- |
| 真·恢复（step 执行途中被暂停后 Continue） | 是 | 非空（来自 Resume.SessionID） | 复用旧会话 |
| 普通前进（前一轮 eachRepeat 刚结束，指到下一轮） | 是 | 非空（上一轮留下的残影） | 必须新建 |

引擎只看了指针是否非空，没看"这个非空是不是 eachRepeat 清场后遗留下来的脏值"，所以两个场景被合并处理。

## 修复方案

在"一个 step 结束、推进 Resume"这一步同时收紧两处状态：

1. **让当前会话指针与 Resume 的 SessionID 保持一致**：当结束的 step 是 `eachRepeat` 时，`advance_resume` 除了把 Resume 里的 SessionID 清空，还要把贯穿主循环的"当前会话指针"一起清空。这样下一轮迭代进入同一个 step 时，"当前会话非空" 这一前置条件不再成立，eachRepeat 的新建分支会正确触发。
2. **收窄"真·恢复"的判定口径**：把"是否处于 resume 上"的判断从"当前会话指针非空"改为"Job 的 Resume 里记录了非空的 SessionID"。Resume.SessionID 代表的是持久化下来的"恢复意图"，不会被前一轮执行的副作用污染；而"当前会话指针"本质上是内存态的中间变量，不适合作为语义判定依据。

两处改动是互补的：第 1 条保证状态机的不变式（当前会话指针在 eachRepeat 结束后必须为空）；第 2 条保证即使未来有其他路径把指针污染了，判定逻辑本身也不会误伤普通前进。

配套调整：

- **failure / stopLoop / stopWorkflow 三条提前返回路径的一致性**：只要在"分支离开 step 之前"发生，`advance_resume` 不会执行；此时的"当前会话指针"保持原状（用于 Continue 恢复该 step）并无影响，因为真正的恢复逻辑会从 Resume.SessionID 重新灌给指针。本修复不需要动这几条路径。
- **beforeRound 与 none**：二者都不经过上面出问题的判定分支，`beforeRound` 每次进入 step 都无条件新建，`none` 则始终复用，不受影响。
- **rc > 1 的情况**：同一个 step 的多个 repeat 仍发生在同一次递归调用里，内部的 `resumePath` 已经在第一次 r=0 执行后被清掉，因此 r>=1 的 repeat 本来就会进入新建分支，不需要额外处理。

## 回归与自测用例

建议补一组单测 / 集成验证，覆盖以下组合：

1. **本次 bug 场景**：group(ic=10) × step(rc=1, eachRepeat)，期望 10 个会话、每个会话只跑一次。
2. **rc>1 的 eachRepeat**：group(ic=3) × step(rc=4, eachRepeat)，期望 12 个会话。
3. **真·恢复不能被破坏**：手动把 Job 的 Resume 设置成 `{NextPath=某 step, SessionID=已有会话}`，然后 Continue，期望沿用这个会话而不是新建。
4. **eachRepeat 和 none 混排**：group 内先后包含一个 eachRepeat step 和一个 none step，期望前一个 step 结束后，下一个 none step 通过 fallback 自己起一个新会话，而不是继承上一个。
5. **beforeRound 不回归**：确认 beforeRound 在同样的嵌套配置下仍然按现行行为执行。

## 影响面

- 对用户已有的历史 Job / 已完成的历史 Run 没有影响，只影响修复之后新启动的 Loop。
- 前端、Schedule、ChatCtx 等模块没有改动。
- 日志与可观测性保持不变：`tryCreateSession` 的 source 仍然会打印 `eachRepeat`，便于在日志里直接数出每次迭代的会话创建事件。

## 待办

- [ ] 在 `executor_loop.go` 的 eachRepeat 前置判定与 advance_resume 处落地上述两处收紧。
- [ ] 补齐上述 5 条回归用例的单测。
- [ ] 使用用户的 Cowork 调研模板在本地 `make web` 实际跑一次，确认 `job.SessionIDs` 长度为 10 且每个会话只包含一轮对话。
