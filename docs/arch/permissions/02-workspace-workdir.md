# 02 · Workspace / Job 的 workdir 校验

> 范围：workspace 创建/更新时的 workdir 准入；Job 创建/调度时的 workdir 与所属 workspace 的包含关系；防止 `$HOME` 走"老记录绕过"的纵深防御。

## 1. Workspace 自身的 workdir 校验

位置：`services/workspace/service.go`（`Create` / `Update` 分支）。

规则：

- workdir 不能为空。
- workdir 必须是绝对路径（`filepath.IsAbs`）。
- workdir 不做"必须在 LOCAL_MEMORY 之下"的硬约束 —— 这是有意的：用户可以把 workspace 落在任意业务仓库目录，但访问时要靠后续的白名单约束。

错误形态：

- `invalid workdir: workdir is empty`
- `invalid workdir: workdir must be absolute: <path>`

## 2. `TrustedFileWorkspaceRoots()`

位置：`services/workspace/service.go` 中的 `TrustedFileWorkspaceRoots()`。

行为：把当前所有 workspace 过滤一遍（非删除、`Workdir` 非空、绝对路径），排序后返回。专门给文件白名单用。**不会**自动加 `$HOME`。

## 3. `$HOME` 默认在白名单内

`cmd/web/handler/file_rw.go` 的 `allowedRoots()` 默认就把本地文件服务解析出的用户目录加进白名单，所以：

- 用户可以把 workspace 落在家目录任意子树上（`~/dev/myproject` 等），不需要任何环境变量开关。
- "Select Working Directory" 浏览框可以从家目录起步浏览整棵子树。
- 文件读 / 写 / 浏览 endpoint 也能直接读家目录文件。

因为本程序运行在用户自己的电脑上，本来就拥有家目录的全部访问权；白名单的目的不是"挡住用户自己"，而是挡住路径越界写入到 `/etc`、`/proc` 这类系统目录。

## 4. Workspace Provider 缓存

同一个 `SetWorkspaceRootsProvider` 实现里，使用 `atomic.Pointer[cachedWorkspaceRoots]` + `wss.Revision()` 做版本化缓存：

- 命中：revision 一致 → 直接返回上一次的列表，零开销。
- 未命中：调一次 `TrustedFileWorkspaceRoots()` 重建。
- 关键不变量：缓存 key 是"读取 List 之前的 revision"，所以最多落后一个 mutation，不会落后两个。

意义：每次文件请求都会过 `isPathInAllowedRegion`，缓存让稳态下零分配。

## 5. Job 的 workdir 校验

位置：`cmd/web/handler/job.go`。

两道关：

### 5.1 `validateWorkdir(workdir)`
- 空字符串：跳过（业务允许 Job 不指定 workdir，由所属 workspace 兜底）。
- 否则：调用文件服务检查路径，要求**存在 + 是目录**，否则报错。

### 5.2 `ensureWorkdirWithinWorkspace(workdir, wsWorkdir)`

防止 "Job 挂在 workspace A 名下、却跑在 workspace B 的目录里"。

- 双方均 `FileEvalSymlinks` 解析到真实路径（`resolveForContainment`），堵 symlink 旁路。
- 若两者解析后相等 → 通过。
- 否则 `filepath.Rel`，要求结果不能是 `..`、不能以 `../` 开头、不能是绝对路径，否则视为越界。
- `wsWorkdir == ""` 视为该 workspace 没有约束（历史/部分初始化情况），仅做 `validateWorkdir` 的存在性检查。

### 5.3 设计意图说明

注释里点明：前端 DirPicker 已经把选择范围锁在 workspace workdir 之内，但服务端**必须**再做一次校验，因为脚本/老客户端可以绕过 UI。

## 6. Scheduled Task 的 workdir 校验

位置：`cmd/web/handler/handler.go`（schedule 派发到 Job 的链路里）。

复用上述两个函数：先 `validateWorkdir`，再 `ensureWorkdirWithinWorkspace`，错误前缀加上 `schedule <id>:`。

## 7. 默认 workdir 解析 —— `resolveDefaultWorkdir`

位置：`services/workspace/service.go` 中的 `resolveDefaultWorkdir()`。

优先级：文件服务解析出的用户目录 → `$HOME` → 文件服务解析出的临时目录。

- 第二步会打 Warn，提醒用户目录解析不可用，已回退到宿主机 `$HOME`。
- 第三步还会打 Warn，提醒"`$HOME` 不可用，落到临时目录"。

它有两个用途：

1. `EnsureDefault()` 创建/修复默认 workspace `ws-1` 时，把 workdir 设为它的返回值；保证 `ws-1` 始终有一个可写 workdir，scheduled task / 新用户没建 workspace 也能跑。
2. **新建 workspace 的对话框预填**：service 接口上对应的方法是 `DefaultWorkdir()`，HTTP 入口是 `GET /api/v1/workspace/default-workdir`，前端 `WorkspaceFormModal` 在打开 create 模式时调用，把返回的 `workdir` 字符串预填到目录选择器里。用户可以再换路径，但默认就是家目录而非空字符串，避免每次都要手动输入。

