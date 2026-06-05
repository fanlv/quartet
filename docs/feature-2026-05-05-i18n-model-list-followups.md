# Web 国际化缺陷修复（Crash Risk + LoopConfigPanel 完整接入 i18n）

> 所属项目：Quartet Web 国际化
> 状态：已核实，修复已完成
> 更新时间：2026-05-05

## 一、核实结论（真实问题列表）

本次只保留经代码复查后 **确实成立** 的问题：

1. **潜在崩溃风险**：在 `i18n.resolvedLanguage / i18n.language` 为 `undefined` 时调用 `.startsWith()` 会导致首屏白屏。
2. **中英混编 / 未翻译**：`LoopConfigPanel` 变量提示中间 `<code>` 段落写死中文 `{{变量名}}`，且“有变量”分支整句未走 `t()`。
3. **国际化改造未完成**：`LoopConfigPanel` 文件夹下存在大量硬编码中文（含按钮、弹窗、提示语、内置变量描述等），导致英文界面体验不可用或不一致。

说明：本文覆盖的是本次变更范围内的 i18n 缺陷修复；模型列表刷新等其他 follow-up 不在本文范围内。

## 二、根因

### 2.1 `i18n` 语言字段存在 `undefined` 窗口期

在 `i18next` 初始化早期（未开启 Suspense 的典型场景），`i18n.resolvedLanguage` 与 `i18n.language` 可能同时为 `undefined`，此时直接调用字符串方法会抛异常。

### 2.2 变量示例与 UI 文案未统一进入翻译层

`LoopConfigPanel` 的变量示例 `{{变量名}}` 被写死在 JSX 中，且多处 UI 文案直接硬编码中文，未从 `locales/*.json` 提取。

## 三、修复方案

### 3.1 防崩溃兜底

将语言判断统一加 fallback：

- `(i18n.resolvedLanguage || i18n.language || 'en')...`

### 3.2 `LoopConfigPanel` 全量接入 i18n

- 变量提示使用 `react-i18next` 的 `<Trans />`，通过 `values={{ varTag }}` 渲染示例，避免 `{{变量名}}` 写死中文。
- 将 `LoopConfigPanel`（含 `FlowStepEditor` / `FlowNodeEditor`）可见中文文案全部提取到 `web/src/i18n/locales/en.json` 与 `web/src/i18n/locales/zh.json`。
- 默认生成的 group `label` 不再写死中文（使用空字符串），由 UI 根据深度/位置展示本地化默认名，避免配置文件被“创建时的语言”污染。

## 四、变更文件

- `web/src/components/ChatPage.tsx`
- `web/src/components/settings/ModelSettings.tsx`
- `web/src/components/LoopConfigPanel/index.tsx`
- `web/src/components/LoopConfigPanel/FlowStepEditor.tsx`
- `web/src/components/LoopConfigPanel/FlowNodeEditor.tsx`
- `web/src/components/LoopConfigPanel/utils.ts`
- `web/src/i18n/locales/en.json`
- `web/src/i18n/locales/zh.json`

## 五、验证要点

1. 首屏加载阶段不应再出现 `startsWith` 相关的运行时崩溃。
2. 切换语言后，`LoopConfigPanel` 的变量提示（含 `<code>` 示例）应完全一致地切换语言，不出现中英混编。
3. `LoopConfigPanel` 主要交互路径（新增变量、保存模板、更新模板、删除模板、导入配置、离开确认）在英文环境下不应再出现中文硬编码。

## 六、最终结论

- 上述 3 类问题均为真实缺陷；本次变更已将其修复并补齐对应翻译项。
