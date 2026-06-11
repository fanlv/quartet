# 05 · Shell 执行的环境变量净化

> 范围：`services/job/shell_env.go` 与 `services/job/executor_shell.go`。

quartet 在执行 Shell 节点时，**不会**把宿主机环境一股脑透传给子进程，而是按一组规则过滤后再注入。这是为了避免 LLM / 用户脚本意外把 `AWS_*`、`GOOGLE_*` 等敏感凭证带到任意命令里执行；同时也会默认放行一小部分本地开发常用 token，避免单用户场景下脚本无法调用用户已配置好的开发服务。

## 1. 强制移除项（reserved）

无论用户怎么配置、都会被剥离的 key：

- `QUARTET_CONTROL` —— 控制文件路径，**每条命令独立注入**。如果继承宿主机里的旧值，可能被定向到攻击者控制的文件。

## 2. 默认敏感名单（除非显式 passthrough，否则移除）

`shell_env.go` 中维护四类匹配规则，命中任意一类即过滤：

- **精确名**：`API_KEY`, `ACCESS_KEY`, `SECRET`, `KEY`, `TOKEN`, `PASSWORD`, `COHERE_API_KEY`, `HF_TOKEN`, `HUGGINGFACEHUB_API_TOKEN`。
- **前缀**：`AWS_`, `OPENAI_`, `ANTHROPIC_`, `COHERE_`, `AZURE_OPENAI_`, `GOOGLE_`, `GEMINI_`, `MISTRAL_`, `DEEPSEEK_`, `DASHSCOPE_`, `ARK_`, `VOLC_`, `BYTEPLUS_`。
- **片段**：`_TOKEN`, `_SECRET`, `_PASSWORD`, `_API_KEY`, `_ACCESS_KEY`, `PRIVATE_KEY`, `CREDENTIAL`。
- **后缀**：`_KEY`（兜底捕获 `DEEPSEEK_KEY` / `GEMINI_KEY` / `MISTRAL_KEY` 这类）。

匹配时先 `strings.ToUpper` 标准化，所以 `aws_secret_access_key` 也会命中。

## 3. 默认放行名单

一些常用开发工具 token / 模型服务配置虽然会命中上述敏感规则，但在 quartet 的单用户本地 / 沙箱运行场景里通常需要被 Shell 脚本继承，因此默认放行：

- VCS：`GITHUB_TOKEN`, `GITLAB_TOKEN`。
- ARK / 火山引擎：`ARK_API_KEY`, `ARK_MODEL`, `ARK_BASE_URL`。
- Anthropic：`ANTHROPIC_API_KEY`, `ANTHROPIC_AUTH_TOKEN`。
- Firecrawl：`FIRECRAWL_API_KEY`。
- OpenAI-compatible：`OPENAI_API_KEY`, `OPENAI_BASE_URL`。

## 4. 显式放行 —— `QUARTET_SHELL_ENV_PASSTHROUGH`

逗号分隔，列出"必须透传给 Shell 节点"的环境变量名。常见用法：

- 业务必须但未在默认放行名单中的 key（例如内部模型服务 token）。
- 公司内部的 metrics / tracing 凭证。

注意：

- `QUARTET_CONTROL` 被硬编码忽略，不会因为出现在这里就被透传。
- 解析时大小写无关、自动 trim 空白。
- 当前实现是**按 `Environ()` 调用**重新读环境变量，不是按"每条命令"——也就是改了环境变量后下一次执行才生效（启动后 `export` 的需要重启程序，或确保生效后再触发执行）。

## 5. 过滤函数 —— `isAllowedShellEnvKey`

优先级：

1. Reserved → 直接拒绝（即便在 passthrough 列表里也拒绝）。
2. 默认放行名单 → 直接放行（覆盖默认敏感名单）。
3. 显式 passthrough → 直接放行（覆盖默认敏感名单）。
4. 命中默认敏感名单（精确 / 前缀 / 片段 / 后缀任意一类）→ 拒绝。
5. 其它 → 放行。

## 6. 注入逻辑 —— `executor_shell.go`

- 启动 Shell 子进程前调用 `sanitizedShellEnvWithFiltered()` 拿到过滤后的环境。
- 然后**每次执行**都额外注入一条 `QUARTET_CONTROL=<本次命令的控制文件路径>`，覆盖任何继承下来的同名变量。
- 调试日志只打"被过滤掉的 key 名 + passthrough 提示"，**不打 value**。

## 7. 其它边界

- HTTP 创建 / 更新入口会先做 workdir 请求级校验；Shell 启动前还会再次校验 workdir 存在且是目录，校验通过后才会启动子进程。
- stderr 抓取上限 10MB（`maxStderrSize`），单行扫描缓冲 1MB；尾部 1024 字节会随错误日志、迭代消息一起带出，方便排错。

## 8. 测试

`services/job/executor_shell_test.go` 中的 Shell 环境变量测试覆盖：

- 非敏感 key 透传。
- 默认敏感 key 被剥离。
- passthrough 显式放行能覆盖默认敏感名单。
- `QUARTET_CONTROL` 即便在 passthrough 名单中也不会透传。
