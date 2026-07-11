# Antigravity (agy) 额度与版本展示方案

## 背景

系统通过 `AgentUsageCard` 在输入框底部展示各 ACP agent 的额度（Codex 的限额环、Claude 的花费）或版本号。本方案扩展该卡片：当用户选择 `antigravity-acp` 时，展示

1. Antigravity 内置套餐的余量（Gemini 与 Claude/GPT 两个模型组，各含「7 天」和「5 小时」两档），以限额环形式呈现；
2. 底层 `agy` CLI 的版本号。

## 运行时事实（已实测，作为设计前提）

- Antigravity 由两部分组成：`antigravity-acp`（ACP wrapper，通过 bun 运行）与 `agy`（真正承载额度/会话的本地 language server CLI）。
- 版本以 `agy --version`（如 `1.1.1`）为准。`antigravity-acp` 自身版本（如 `1.0.0`）只是 wrapper 版本，不是要展示的目标。
- 每个 `agy` 进程会在 `127.0.0.1` 上监听**两个端口**：一个**明文 HTTP** 端口对外提供额度用的 Connect-RPC 接口，另一个是需要客户端证书（mTLS）的 HTTPS 端口，无法直接访问。
- 额度接口 `RetrieveUserQuotaSummary` **无需 csrf token、无需任何鉴权**，直接向明文 HTTP 端口 POST 即可拿到数据（进程命令行中并不存在 `--csrf_token` / `--extension_server_port` 参数）。
- 返回结构为 `response.groups[].buckets[]`，每个 bucket 含 `bucketId`、`remainingFraction`（剩余比例 0~1）、`resetTime`（RFC3339）。`bucketId` 取值固定为四种：

  | bucketId | 含义 |
  |---|---|
  | `3p-weekly` | Claude/GPT 组 · 7 天限额 |
  | `3p-5h` | Claude/GPT 组 · 5 小时限额 |
  | `gemini-weekly` | Gemini 组 · 7 天限额 |
  | `gemini-5h` | Gemini 组 · 5 小时限额 |

## 方案设计

### 后端

**模型定义**：新增 `AntigravityUsage`，含 `version` 与四个复用现有 `UsageWindow` 的字段，按语义命名：`claude_weekly` / `claude_5h` / `gemini_weekly` / `gemini_5h`（比 primary/secondary 更清晰，避免映射歧义）。`AgentUsageResponse` 增加 `Antigravity` 字段，并把 `type` 允许值扩展为 `codex | claude | antigravity`。

**服务方法**：在 `services/agent/usage` 中新增 `AntigravityUsage(ctx)`，与现有 Codex/Claude 逻辑并列。执行步骤：

1. **版本**（可与额度并发）：直接运行 `agy --version`，取首个 semver。注意：不能复用现有的 `acpVersionAsync`——它按 `antigravity-acp` 的 serve 命令解析，拿到的是 wrapper 版本而非 `agy` 版本；这里需要独立探测 `agy`。版本探测失败不应阻断额度（版本是补充信息）。
2. **端口发现**：用 `ps` 找出所有 `agy` 进程，再用 `lsof` 取得它们在 `127.0.0.1` 上的监听端口。无需解析任何进程参数。
3. **拉取额度**：对候选端口用**明文 HTTP** POST：
   - URL：`http://127.0.0.1:<port>/exa.language_server_pb.LanguageServerService/RetrieveUserQuotaSummary`
   - Header：`Content-Type: application/json`、`Connect-Protocol-Version: 1`
   - Body：`{"metadata":{"ideName":"antigravity","extensionName":"antigravity","ideVersion":"unknown","locale":"en"}}`
   - 取第一个返回合法额度 JSON 的端口即可（HTTPS 端口会因 mTLS 握手失败，自然被跳过）。
4. **解析组装**：遍历 `response.groups[].buckets[]`，按 `bucketId` 落到对应字段，并做两处换算：
   - `used_percent = (1 - remainingFraction) * 100`（API 给的是**剩余**比例，UI 环填充的是**使用**率，必须取反，否则显示颠倒）；
   - `reset_at = resetTime`(RFC3339) 转 unix 秒，供 UI tooltip 显示重置时刻。
5. 失败即返回完整错误（遵循「错误全量展示」约定）。

**Handler**：在现有 `AgentUsage` 的 `type` 分支中新增 `antigravity` 分支，调用服务方法并以 `AgentUsageResponse{Type:"antigravity", Antigravity: ...}` 返回，沿用现有 `no-store` 头。

### 前端（`web/src/utils/agentUsage.ts` & `AgentUsageCard.tsx`）

- **类型**：新增 `AntigravityUsage`（`version?` + `claude_weekly?` / `claude_5h?` / `gemini_weekly?` / `gemini_5h?`，均为 `UsageWindow`）；`AgentUsageProvider` 增加 `'antigravity'`。
- **provider 判定**：`agentUsageProvider` 中当 `agentType`/`displayName` 含 `antigravity` 时返回 `'antigravity'`。
- **拉取与缓存**：`fetchAgentUsage` 读取并返回 `data.antigravity`；`getCachedUsage` / `setCachedUsage` 增加 `antigravity` 分支（localStorage 键 `agentUsage_antigravity`）。
- **渲染**：`AgentQuotaCard` 增加 `antigravity` 状态与渲染分支，展示版本号 + 四个 `UsageRing`。因界面窄，用紧凑标签区分模型系列与档位（如 `C7d`/`C5h`/`G7d`/`G5h`），完整含义放到悬浮 tooltip（复用 `ringTitle`，依赖上面填入的 `reset_at`）。

## 依赖条件

- 目标机器需有 `ps` 与 `lsof`（macOS/Linux 自带）以定位 `agy` 的监听端口。
- 额度随时间变化，接口保持每次刷新实时拉取、不做结果缓存（与 Codex/Claude 一致）。如需降低 `ps`/`lsof` 频率，可对「进程→端口」映射加很短的 TTL 内存缓存，但必须在探测失败时立即失效（`agy` 重启后端口会变）。

## 开放问题

- 旧版本 `agy` 若不支持 `RetrieveUserQuotaSummary`，是否需要回退到 `GetUserStatus` / `GetCommandModelConfigs`。当前版本该接口可用，回退可作为后续可选增强，非首版必需。
