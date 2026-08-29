# 01 · 文件系统白名单

> 范围：所有"读 / 写 / 列 / 浏览 / 上传 / 服务"本地文件的 HTTP 入口。

## 1. 白名单的来源 —— `allowedRoots()`

入口：`cmd/web/handler/file_rw.go` 中的 `allowedRoots()`。

返回的根目录（按顺序）：

1. `$LOCAL_MEMORY` —— 启动时必须显式设置的本地记忆库根目录。
2. `typepath.UploadsDir()` —— 上传目录（一般在 `$LOCAL_MEMORY/uploads`）。
3. `workspaceRoots()` —— 当前所有未删除 workspace 的 `Workdir`（绝对路径），由 `services/workspace/service.go` 中的 `TrustedFileWorkspaceRoots()` 提供。
4. `$HOME` —— 由本地文件服务解析得到，**默认始终包含**。这一项是为了让"选目录"对话框可以从家目录起步浏览，并接受用户把 workspace 落在家目录任意子树上。

> `services/workspace/service.go` 还导出一个 `FileAccessBaseRoots()`，只返回 `LOCAL_MEMORY` + `$HOME`，用于 workspace 自身校验，不参与 HTTP 文件白名单。

## 2. 白名单的判定 —— `isPathInAllowedRegion`

入口：`cmd/web/handler/file_rw.go` 中的 `isPathInAllowedRegion(filePath)`。

逻辑：拿 `allowedRoots()`，逐个调用 `hasPathPrefix(filePath, root)`，**任意一个命中即放行**；空路径直接拒绝。

## 3. 路径包含判定 —— `hasPathPrefix`（Symlink-aware）

入口：`cmd/web/handler/file_rw.go` 中的 `hasPathPrefix(filePath, root)`。

行为分两种情况：

- **`filePath` 完整可解析**（叶子已存在）：`FileEvalSymlinks` 解析双方真实路径，**只信解析后的真实路径**。这是为了堵"白名单内的 symlink 指向白名单外目录"的口子。
- **`filePath` 叶子不存在**（写新文件场景）：从叶子向上找最深的"已存在祖先"，对它做 symlink 解析，再把剩余不存在的后缀拼回去比较。这样即便叶子不存在，也能保证祖先链上的任何 symlink 都被穿透检查。

### 已知 TOCTOU 局限

代码注释里直接写明：在"判定"和"实际 syscall"之间，攻击者如果对白名单内目录有写权限，可以把已校验过的目录换成指向白名单外的 symlink。Go 没暴露 `openat2(RESOLVE_BENEATH)`，所以无法做到 race-free。

缓解措施：

1. `ReadFile / WriteFile / ServeFile` 三个写/读关键路径在拿到 syscall 句柄前**再做一次** `isPathInAllowedRegion`（双重检查）。
2. 部署假设：宿主机 FS 只有受信本地用户可写。
3. 不能满足上述假设的部署，应该把白名单根目录放进 chroot / container 里，让 OS 层把外部树彻底隐藏。

## 4. 所有受白名单约束的入口

| 文件 | 入口 | 用途 |
|------|------|------|
| `cmd/web/handler/file_browse.go` | `ListDir` | 目录浏览 |
| `cmd/web/handler/file_browse.go` | `MkDir` | 创建子目录 |
| `cmd/web/handler/file_browse.go` | `FileExists` | 检查文件存在性（白名单外**静默返回 `exists:false`**，避免被穷举探测） |
| `cmd/web/handler/file_browse.go` | `SearchFiles` | 关键词搜索；带 3s 超时和 depth=5、maxResults=20 限制 |
| `cmd/web/handler/file_browse.go` | `AddRecentDir` | 写"最近使用目录"前必须落在白名单内 |
| `cmd/web/handler/file_rw.go` | `ReadFile` | 读取（含进 syscall 前的二次校验） |
| `cmd/web/handler/file_rw.go` | `WriteFile` | 写入（含进 syscall 前的二次校验） |
| `cmd/web/handler/file_rw.go` | `ServeFile` | 静态服务（含进 syscall 前的二次校验，外加 MIME 白名单，详见 06 篇） |
| `cmd/web/handler/file_rw.go` | `isPathAllowedForServe` | 仅是 `isPathInAllowedRegion` 的别名包装 |
| `cmd/web/handler/im_gateway.go` | IM 文件下发链路 | 也复用同一份白名单 |

## 5. 默认浏览根

`cmd/web/handler/file_browse.go` 的 `defaultBrowseRoot()`：客户端不传 `path` 时，挑 `allowedRoots()` 第一个非空项作为浏览起点（默认是 `$LOCAL_MEMORY`），而**不是** `$HOME`，避免误配置时把整个家目录暴露出去。

## 6. 错误响应

白名单拒绝时统一返回：

```json
{ "code": -1, "msg": "access denied: path is outside allowed directories" }
```

HTTP Status：`403 Forbidden`。

唯一例外：`FileExists` 故意返回 `200 + {"exists": false}`，避免给穷举攻击者反馈信号。
