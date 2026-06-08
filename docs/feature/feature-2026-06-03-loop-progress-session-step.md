# Loop 进度展示：从「总 Step」改为「Session 进度 + 当前 Session 内 Step 进度」

- 创建日期：2026-06-03
- 作者：Sophie
- 相关文件：`web/src/components/LoopProgress.tsx`、`web/src/components/LoopConfigPanel/utils.ts`、`web/src/hooks/useJobChat.ts`、`web/src/components/JobChat.tsx`、i18n 资源

## 背景

Loop 模式的进度条原来只显示一个全局维度 `done / totalSteps`（已完成步数 / 总步数）。
对带「主循环 group + 多个 step + 按 roundMode 新建会话」的调研类模板，用户真正关心的是：

1. 当前跑到第几个会话 / 一共多少个会话；
2. 当前会话内跑到第几步 / 这个会话一共多少步。

全局 step 把这两层语义压成了一个数字，无法直观判断「现在第几轮调研、这一轮走到哪」。

## 方案

进度区域改为展示两段信息：`会话 X / Y` 与 `步骤 M / N`（当前会话内）。

- **会话边界推导**：前端按与后端一致的执行顺序遍历 flow（group 按 iterationCount 展开、
  step 按 repeatCount 展开），按 roundMode 规则把每个叶子 step 归属到一个会话：
  - 首个执行的 step 无条件开新会话；
  - `beforeRound`：每次进入该 step 节点开新会话，节点内多个 repeat 复用；
  - `eachRepeat`：每个 repeat 都开新会话；
  - `none` / 空：复用上一个会话。
  规则与 `docs/feature-2026-05-08-loop-eachrepeat-session-reuse-fix.md` 保持一致。
- **当前位置定位**：用 `progress.currentPath`（后端在每步开跑前写入、结束后停留在最后完成步）
  在叶子序列里精确匹配，得出当前会话序号、会话内步序与该会话总步数。
- **数据透传**：job 的 flow 经 `useJobChat`（拉取 / 刷新 job 时 hydrate）暴露为 `loopFlow`，
  由 `JobChat` 传入 `LoopProgress`；新建 loop 时回退用 `initialLoopConfig.flow`。
- **兜底**：拿不到 flow 时回退显示原 `done / totalSteps` 文本，整体进度条仍按全局完成度绘制。
- **国际化**：新增 `loop.progress.session` / `loop.progress.step`，中文显示「会话 / 步骤」。

## 验证

- 纯函数语义已用回归用例核对：group(10)×eachRepeat → 10 会话；group(3)×eachRepeat(rc=4) → 12 会话；
  beforeRound rc=4 × group(2) → 2 会话 ×4 步；单 none → 1 会话 1 步；beforeRound+none 混排 → 会话内 2 步。
- `npm run build` 未引入新的类型错误（既有 pre-existing 报错与本次改动无关）。

## 待办

- [ ] 用真实模板 `make web` 跑一次，确认 live 与刷新后两组数值一致。
