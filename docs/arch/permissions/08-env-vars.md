# 08 · 与权限相关的环境变量

> 速查表，含默认行为、影响面、推荐用法。

| 环境变量 | 常量定义 | 默认 | 作用 |
|----------|----------|------|------|
| `X_AGENT_AUTH` | `consts.EnvKeyAgentAuth` | 未设 | HTTP API Bearer Token；逗号分隔多个；未设即"开放访问"。默认监听已可 LAN 访问，实际使用建议始终设置。 |
| `LOCAL_MEMORY` | 直接读字符串 | 未设 | 本地记忆库根目录，是文件白名单的第一根；同时也是 default browse root。 |
| `HOME` | 系统 env | 用户家目录 | 由 `sandbox.UserHomeDir()` 解析，默认始终在文件白名单里（无需任何额外开关）。 |
| `QUARTET_LISTEN_ADDR` | `consts.EnvKeyListenAddr` | 默认 `0.0.0.0:8090` | 修改 HTTP 监听地址；默认已可 LAN 访问，**必须配合 `X_AGENT_AUTH` 一起用**。 |
| `QUARTET_CORS_ORIGINS` | `consts.EnvKeyCORSOrigins` | 未设（same-origin） | 跨域白名单；逗号分隔多个 origin；不设则不放行任何跨域请求（早期版本默认 `*`，已收紧）。 |
| `QUARTET_LOG_HTTP_BODY` | `consts.EnvKeyLogHTTPBody` | 未设（关闭） | 是否把 HTTP req/resp body 写日志；默认关，因为有泄漏 token / 聊天内容的风险，仅本地开发用。 |
| `QUARTET_LOG_LEVEL` | `consts.EnvKeyLogLevel` | `info` | 日志级别（debug/info/warn/error）。 |
| `QUARTET_SHELL_ENV_PASSTHROUGH` | 直接读字符串（`envShellPassthrough`） | 未设 | 把指定 env key 显式放行给 Shell 节点，覆盖默认敏感名单；逗号分隔，大小写无关。 |

## 与 Header 命名相关

| 名称 | 常量 | 说明 |
|------|------|------|
| `X-AGENT-AUTH` | `consts.HeaderAgentAuth` | HTTP API Bearer Token 的请求头名；统一用这个，反代不会丢。 |

## 默认值对单机使用的含义

- **`X_AGENT_AUTH` 未设** = API 完全开放给任何能访问端口的人。默认 `0.0.0.0:8090` 已可 LAN 访问，因此实际使用前必须打开。
- **`HOME` 已读取** = 文件白名单默认包含家目录，用户可以从家目录任意起步浏览，把 workspace 落在 `~/dev/...` 这类位置都不需要额外开关。
- **`QUARTET_CORS_ORIGINS` 未设** = 浏览器跨域请求被拒；如果你想做反向代理 / Web 集成，需要显式配置。

## 配置建议（按部署场景）

### 1. 单机本地开发（推荐默认）
```bash
export LOCAL_MEMORY=/path/to/local
export X_AGENT_AUTH=$(openssl rand -hex 32)
# 其它都不设；$HOME 默认就在白名单里
```

### 2. 内网共用一台 quartet
```bash
export LOCAL_MEMORY=/path/to/local
export X_AGENT_AUTH=$(openssl rand -hex 32)
export QUARTET_CORS_ORIGINS=https://your-frontend.example.com
```
另外建议在前面套 nginx + TLS + IP allowlist。

### 3. Shell 需要用 LLM key
```bash
export QUARTET_SHELL_ENV_PASSTHROUGH=OPENAI_API_KEY,ANTHROPIC_API_KEY
```
然后正常 export 这两个 key，quartet 会把它们透传给 Shell 节点。
