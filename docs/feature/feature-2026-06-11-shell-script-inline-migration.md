# Shell 脚本内联化：移除 Shell 管理模块

日期：2026-06-11

## 背景

Loop 任务配置中的 shell 步骤目前通过 `scriptId` 引用 "Shell 管理" 模块中独立存储的脚本，执行时再按 ID 加载脚本内容。这带来一层间接性：脚本与 loop 配置分离存储，编辑 loop 时需要跳转到脚本管理，且脚本被删除后 loop 配置会执行失败。

本次改造将 shell 脚本内容直接内联存储在 loop 配置的步骤节点中，并整体移除 "Shell 管理" 模块。

## 决策

1. **shell 内容复用 `message` 字段**，不新增 `scriptContent` 字段。prompt 步骤与 shell 步骤统一使用 `message` 承载内容，前端编辑器可复用同一套文本编辑能力（变量插入、自动高度等）。后端执行链路本身已支持 `scriptId` 为空时直接执行 `message`，改造后脚本解析逻辑退化为 "取 message + 变量替换"。
2. **运行中的 job 允许编辑 shell 脚本内容**。原先 `scriptId`/`scriptName` 被视为结构字段，运行中不允许修改；内联到 `message` 后，shell 内容随 `message` 成为可编辑字段，运行中修改后下一轮生效，与运行中修改 prompt 的行为保持一致。

## 改造内容

### 数据结构

- 流程节点（FlowNode）及遗留的 LoopRound 中删除 `scriptId`、`scriptName` 字段。
- shell 步骤的脚本内容存放在 `message` 字段中。
- 运行中配置编辑的结构比较逻辑不再比较脚本绑定字段。

### 数据迁移（一次性脚本）

独立的一次性迁移程序，放在 `cmd/migrate-shell-script/`，迁移完成并验证后删除。逻辑：

1. 读取 `LOCAL_MEMORY` 环境变量，加载脚本目录（`agent/scripts/`）下所有脚本，建立 scriptId 到脚本内容的映射。
2. 扫描三类包含 LoopConfig 的数据文件：
   - Job：`workspaces/*/jobs/*/.meta/job.json`
   - Template：`agent/templates/*.json`
   - Schedule：`agent/schedules/*.json`
3. 对每个文件：先执行遗留 Rounds 格式到 Flow 树的迁移，再递归遍历 Flow 树，凡引用了 scriptId 的节点：
   - 将对应脚本内容写入 `message`；
   - 清空 `scriptId`、`scriptName`；
   - 若节点 `label` 为空，将原 `scriptName` 写入 `label` 保留可读性。
4. 写回前对每个被修改的文件做 `.bak` 备份，使用原子写入。
5. scriptId 找不到对应脚本的，全量打印错误（文件路径 + 节点 ID + scriptId），该文件跳过不写回。
6. 结束时输出汇总报告：扫描数 / 修改数 / 跳过数。
7. 迁移脚本不删除 `agent/scripts/` 目录，由用户验证通过后手动删除。

### 后端移除范围

- Script 领域模型及其 ID 生成。
- Script 的 repository 存储层。
- Script 业务服务（`services/script/`）。
- Script HTTP handler 及 `/api/v1/script/*` 全部路由。
- Job 执行器中对 script 服务的依赖注入，shell 脚本解析逻辑简化为变量替换。
- 路径定义中的脚本目录。
- 相关测试中对脚本字段的引用。

### 前端移除与改造范围

- 删除 Shell 管理面板组件及其样式，移除设置页中的入口。
- Loop 步骤编辑器：删除脚本选择器（picker、搜索、选择逻辑），shell 步骤改为直接编辑 `message` 的代码文本框，复用现有的变量插入与变量检测能力。
- Loop 配置面板：删除脚本列表拉取及 scripts 属性的层层透传。
- shell 步骤校验从 "已选择脚本" 改为 "message 非空"；步骤预览从脚本名改为 label 或 message 首行。
- 类型定义中删除 Script 接口与节点上的脚本引用字段。
- 清理相关 i18n 文案与 e2e 测试引用。

## 实施顺序

1. 编写迁移脚本，用户本地执行，验证 loop 配置正常。
2. 后端改造（字段删除 + 模块删除）。
3. 前端改造（编辑器改为内联文本框 + 删除管理面板）。
4. 构建与 lint 验证。
5. 确认无误后删除迁移脚本与 `agent/scripts/` 数据目录。

迁移必须先于代码合入执行：新代码不再识别 `scriptId` 字段，若先合入代码再迁移，旧数据中的脚本引用会在加载时被静默丢弃。

## 行为变化

- shell 脚本内容随 loop 配置一起存储与导出，不再依赖独立的脚本库。
- 运行中的 job 可以直接编辑 shell 脚本内容，下一轮执行生效。
- 不再存在 "脚本被删除导致 loop 执行失败" 的情况。
