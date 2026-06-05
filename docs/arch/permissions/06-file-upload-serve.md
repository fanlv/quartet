# 06 · 文件上传与 ServeFile

> 范围：`cmd/web/handler/file_rw.go` 中的 `UploadFile` 与 `ServeFile`。

## 1. 上传 —— `UploadFile`

- 目标目录：`typepath.UploadsDir()`，一般是 `$LOCAL_MEMORY/uploads`，本身在文件白名单内。
- 大小上限：单文件 10MB，超过直接拒。
- 命名策略：纳秒时间戳 + 8 字节 `crypto/rand` 后缀 + 原始扩展名。仅靠时间戳在低精度时钟系统上有碰撞风险，所以追加随机后缀。
- 实际写入走 sandbox `FileUpload`，统一由 fileserver 层落盘。

上传路径不会暴露给前端用户去"指定"，所以不存在"上传到任意目录"的攻击面。

## 2. 服务 —— `ServeFile`

`ServeFile` 是 quartet 把本地文件回吐给浏览器的入口。两层防御：

### 2.1 路径白名单

入口和读写一样：先 `isPathAllowedForServe`（实际就是 `isPathInAllowedRegion` 的别名），再在 syscall 之前再校验一次（缩窄 TOCTOU 窗口）。

### 2.2 MIME 白名单 + 强制下载

`serveFileContentType`：

- 内联放行的 MIME（在 `serveFileInlineMIMEs` 集合里）：常见图片（PNG/JPG/GIF/WebP/BMP/AVIF/HEIC/HEIF）、音频（MP3/WAV/OGG/WebM/FLAC/AAC）、视频（MP4 等）、PDF。这些可以直接 `Content-Type: <真实类型>` 返回，让浏览器内联渲染。
- **强制阻挡内联**：SVG（可嵌 `<script>`）、HTML / XHTML / XML、JS、未知 MIME。这些一律降级为 `application/octet-stream` 并加 `Content-Disposition: attachment`，强制浏览器下载，不在应用 origin 里执行。
- 加 `X-Content-Type-Options: nosniff` 防止浏览器嗅探回 HTML。

### 2.3 文件名安全

`Content-Disposition` 里的文件名经过：

- `sanitizeAttachmentFilename` —— 剥离 CR/LF/双引号/反斜杠，避免响应头注入。
- `buildAttachmentDisposition` —— 走 RFC 5987 编码（`filename*`），同时给老浏览器保留 ASCII fallback 的 `filename`。

## 3. `PublicServeFile`（分享链路）

详见 03 篇 §3.1。在普通 ServeFile 基础上多一层：要求请求路径必须落在该 Job 的 `LocalSessionsDirInWorkspaceJob` 之下，不允许跳到 Job 外部，即便那条路径在白名单内也不行。

## 4. 设计意图

quartet 的工作目录里可能存着 LLM / Agent 写出来的 HTML、SVG、JS（思考过程、调试产物、用户上传等），如果浏览器把它们当成"应用 origin 里的脚本"渲染，就能读 `localStorage.quartet.x_auth_token` 然后转发到外网。这一层 MIME 白名单 + nosniff + 强制下载是把这条链路堵住。
