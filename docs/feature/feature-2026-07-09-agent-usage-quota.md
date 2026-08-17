# Home 页 Codex / Claude 套餐额度展示

## 背景与目标

在 home 页选择 Codex 或 Claude 两种 Agent type 后，展示对应账号的套餐 / 额度信息，帮助用户直观了解当前用量。页面加载与切换 Agent type 时采用「先旧后新」的异步刷新：先展示浏览器本地缓存里上一次已拉到的信息（若有），再异步请求最新数据；请求成功后无感替换并更新缓存，请求失败则保留本地缓存的旧信息（无缓存则不展示任何数据），避免加载态闪烁。

其余 Agent type（eino、TraeCLI 等）不展示该卡片。

## 数据来源

### Codex

- 从 `~/.codex/auth.json` 读取 `tokens.access_token` 与 `tokens.account_id`。
- 请求 ChatGPT usage 接口（`https://chatgpt.com/backend-api/wham/usage`），携带 Bearer token 与账号 id。
- 该接口从本机直连不可达，必须走代理；代理配置直接复用用户已在「ACP 环境变量」中为 Codex 配好的 `http_proxy / https_proxy / no_proxy`（后端已有按 agentType 读取 ACP 环境变量的能力）。
- 返回信息中提取：套餐类型（plan_type）、邮箱、5 小时窗口用量、7 天窗口用量（各含已用百分比与重置时间）、额度重置券余量。

### Claude

- 从 `~/.claude/settings.json` 读取当前使用的鉴权 token（`env.ANTHROPIC_AUTH_TOKEN`）与接口基址（`env.ANTHROPIC_BASE_URL`）。
- 以 token 后 8 位作为 key 标识，请求基址下的用量接口，分别取「今日用量」与「累计用量」。
- 该接口直连可达（在 no_proxy 列表内，不走代理），偶发超时，需带重试。
- 按 key 标识匹配到当前 key，提取其今日与累计消费金额（美元）。

## 展示内容

- **Codex**：套餐类型徽标 + acp 版本号 + 5 小时窗口用量 + 7 天窗口用量。两个窗口用量以小型环形进度图标展示，环形按已用百分比实时填充并分档着色（低/中/高），鼠标悬浮显示「5h X%」「7d X%」及重置时间。不展示额度重置券余量。
- **Claude**：acp 版本号 + 今日消费金额 + 累计消费金额（美元）。
- 版本号通过执行对应 acp 命令的 `--cli --version` 获取（与用量请求并行执行，不增加串行耗时），显示为 `v<版本>`。
- 用量请求失败不展示错误信息：有本地缓存则继续展示缓存，无缓存则不展示任何数据。

展示位置：home 页输入框底部工具栏，以及进入会话后的会话输入框底部工具栏（图片上传按钮之后的一条紧凑行内信息条），不占用额外的垂直空间。Agent 选择器就在同一行，切换后异步刷新。

## 分层设计

严格遵循项目分层，禁止在 handler 中直接读文件 / 发请求。

1. **types/model** — 新增额度相关的响应结构体（Codex 额度、Claude 额度、统一响应）。
2. **services** — 新增 Agent 用量服务，负责读取本地鉴权文件、按 Agent 类型构造请求（Codex 走 ACP 代理、Claude 直连并重试）、解析结果。依赖已有的 Settings 服务读取 ACP 环境变量。
3. **cmd/web/handler** — 新增用量查询接口 `GET /api/v1/agent/usage`，按 `type` 参数（codex / claude）编排服务调用，不做缓存以保证每次切换取最新。

## 前端设计

1. 新增用量请求工具与类型定义。
2. 新增用量展示卡片组件：按传入的 Agent type，在挂载及 type 变化时先读本地缓存渲染、再异步拉取刷新；仅在无缓存且首次加载时展示加载态。
3. home 页组件在输入区上方按选中的 Agent type 条件渲染该卡片。
4. 补齐中英文文案。

## 约束

- 页面加载与切换 Agent type 时先展示浏览器本地缓存（localStorage）里的旧信息（若有），再异步刷新为最新信息；成功后更新缓存，失败则保留缓存旧信息（无缓存则不展示）。缓存跨页面刷新持久化。
- 用量请求失败静默处理，不向用户透出错误信息。
- 仅在选中 Codex / Claude 时展示，home 页与会话页输入框均展示。
