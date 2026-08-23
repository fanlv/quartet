---
name: quartet-wechat
description: "通过 Quartet 后端发送微信（iLink）主动消息：quartet-cli wechat send 把文本推送给微信用户（默认发管理员白名单），wechat accounts 查询已登录账号。当用户要把消息、运行结果或通知发到微信，给微信用户推送内容，或定时任务/脚本/workflow 里需要微信通知时使用。"
metadata:
  requires:
    bins: ["quartet-cli"]
  cliHelp: "quartet-cli wechat --help"
---

# quartet-wechat

通过 CLI 让 Quartet 后端代为发送微信主动消息（走已登录的 iLink 通道）。这是**主动推送**而不是回复，典型用法：定时任务的 workflow 节点把运行结果发给运营者。

## 准备

CLI 是对后端 HTTP API 的薄封装：

| 变量 | 说明 | 默认 |
|---|---|---|
| `QUARTET_BASE_URL` | 后端地址 | `http://127.0.0.1:8090` |

- 构建/安装 CLI：在仓库根目录 `make install-skill-cli`（装到 `~/.local/bin`，须在 PATH 上）。
- 首次使用先运行 `quartet-cli auth login --username <用户名>`；当前用户需要 `im.send` 权限。
- 所有错误**全量打印**到 stderr；结果打印到 stdout。

## 命令

### send — 发送文本消息

```bash
echo "消息内容" | quartet-cli wechat send                     # 内容走 stdin，发给管理员白名单
quartet-cli wechat send --file report.md                     # 内容从文件读
quartet-cli wechat send --user <ilinkUserId> --file x.txt    # 指定接收人（--user 可重复）
```

- 内容来自 `--file`；省略或为 `-` 时从 stdin 读。内容为空会报错。
- 不传 `--user` 时发给后端配置的**微信管理员白名单**——「通知运营者」场景直接省略即可。
- 长消息后端自动分片发送（durable outbox），stdout 逐行打印进度：`user  status  task=xx (n/m chunk(s))`。
- `--idempotency-key <key>`：幂等键；相同 key 复用已有 outbox 任务，脚本重跑时防重发。
- 默认阻塞等待所有分片送达；`--wait=false` 只入队不等待，立即打印 task ID。
- 报错「backend does not expose the durable WeChat outbox」说明后端二进制过旧，需要重启更新后的 quartet-web。

### accounts — 查询已登录账号

```bash
quartet-cli wechat accounts [--json]
```

列出已登录 iLink 账号的 `ilink_user_id`（即 `send --user` 的取值）、`ilink_bot_id`、在线状态（online/expired）。没有账号时先在 Web UI 完成扫码登录。
