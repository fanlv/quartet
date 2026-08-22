# iOS 开发约定

- 使用 SwiftUI、Swift Concurrency 和 Apple 系统框架，不引入第三方依赖，除非需求明确要求。
- 最低系统版本跟随项目文件中的 deployment target。
- 优先复用 Quartet 现有 HTTP API 与 SSE；新增接口前先确认现有接口无法表达该能力。
- Token 只存储在 Keychain，不写入 UserDefaults 或日志。
- API 错误必须保留请求方法、URL、HTTP 状态和完整响应正文，并允许用户复制。
- 应用从后台返回时重新读取服务端快照，不假设 SSE 在后台持续存活。
- UI 状态不能只依赖颜色表达，所有操作需支持 VoiceOver 和动态字体。
- 本目录的 Xcode 构建只能在 macOS 环境验证；Linux 上仅进行静态工程检查。
