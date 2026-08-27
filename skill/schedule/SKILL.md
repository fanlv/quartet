---
name: quartet-schedule
description: "管理 Quartet 定时任务（schedule）：创建、列出、查看、更新、删除 cron 定时任务，启用/停用（toggle），立即触发一次（run）。当用户要设置、调整、暂停或删除定时任务，让某个 graph workflow 按 cron 周期自动运行（如「每天 9 点跑 XX」「每小时同步一次」），或要立即手动触发一次定时任务时使用。本 skill 通过 quartet-cli 调 Quartet 后端 HTTP API。"
metadata:
  requires:
    bins: ["quartet-cli"]
  cliHelp: "quartet-cli schedule --help"
---

# quartet-schedule

通过 CLI 管理 Quartet 的定时任务。定时任务 = cron 表达式 + 一个 graph workflow：到点后端自动运行该 workflow，生成一个 graph job。

## 准备

CLI 是对后端 HTTP API 的薄封装，复用后端类型：

| 变量 | 说明 | 默认 |
|---|---|---|
| `QUARTET_BASE_URL` | 后端地址 | `http://127.0.0.1:8090` |

- 构建/安装 CLI：在仓库根目录 `make install-skill-cli`（装到 `~/.local/bin`，须在 PATH 上）。
- 首次使用先运行 `quartet-cli auth login --username <用户名>`；CLI 按后端地址保存登录 Cookie。`401` 时重新登录，`403` 时检查账号的 schedule 权限。
- 所有错误（含后端校验错误）**全量打印**到 stderr；结果 JSON 打印到 stdout。

## 命令

### create — 新建定时任务

```bash
quartet-cli schedule create --name "每日复盘" --cron "0 9 * * *" --workflow <gwfId> \
    [--workspace <wsId>] [--workdir <dir>] [--max-concurrent N] [--timeout 分钟] [--disabled]
```

- `--name` / `--cron` / `--workflow` 必填。
- cron 是 **5 段式**「分 时 日 月 周」，按**后端本地时间**解析（`0 9 * * *` = 每天 09:00）。
- `--workflow` 是 graph workflow ID：用 `quartet-cli workflow list --type all` 查找；还没有 workflow 时先用 quartet-workflow skill 创建。
- `--workspace` 决定运行环境（工作目录）：用 `quartet-cli workspace list` 查找 ID。
- 默认在创建任务的当前机器启用；`--disabled` 先建好但不跑。开关保存在本机运行态，不随 Git 同步到其他机器。
- 成功打印完整任务 JSON（含 `id`、`nextRunAt`）。

### list / get — 查询

```bash
quartet-cli schedule list [--workspace <wsId>] [--json]
quartet-cli schedule get <scheduleId>
```

list 表格列：`id  on/off  cron  name  next=下次运行时间`。`lastRunJobID` / `lastStatus` / `lastTriggerError` / `runCount` 等字段在 get 的完整 JSON 里。

### update — 更新（只改传了的字段）

```bash
quartet-cli schedule update <scheduleId> [--name ...] [--cron ...] [--workflow ...] \
    [--workspace ...] [--workdir ...] [--max-concurrent N] [--timeout M] [--enable | --disable]
```

指针语义：没传的字段保持不变。`--enable` / `--disable` 互斥，且只修改当前机器的开关。

### toggle / delete — 启停 / 删除

```bash
quartet-cli schedule toggle <scheduleId>   # 翻转本机启用状态，打印新状态
quartet-cli schedule delete <scheduleId>
```

同一任务同步到其他机器后默认关闭；每台机器可独立启停，互不影响。
从旧版本升级时，旧的本机状态没有开关字段，也会按关闭处理，需要在各机器上重新启用所需任务。

### run — 立即触发一次

```bash
quartet-cli schedule run <scheduleId>
```

立刻按任务配置运行一次（仍受 max-concurrent 限制），打印 `{"status":"triggered","jobId":"..."}`。和 cron 触发走同一条执行路径，适合建好任务后先手动验一次。

## 触发后怎么跟踪

任务每次触发生成一个 graph job：

```bash
quartet-cli job get <jobId>     # 看 status（pending/running/completed/failed）
quartet-cli job list            # 找最近的运行记录
quartet-cli job stop <jobId>    # 停止运行中的 job
```

## 典型闭环：定时跑 workflow + 微信通知

1. （没有则先建）workflow：`quartet-cli workflow create ...`，编写指南见 quartet-workflow skill。
2. 建定时任务：`quartet-cli schedule create --name ... --cron ... --workflow <gwfId>`。
3. workflow 的 shell/prompt 节点里用 `quartet-cli wechat send` 把结果推到微信（见 quartet-wechat skill）。
4. 事后用 `schedule get` 看 `lastStatus` / `lastTriggerError`，用 `job get` 看单次运行详情。
