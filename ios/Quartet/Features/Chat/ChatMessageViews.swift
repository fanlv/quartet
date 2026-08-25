import SwiftUI
import UIKit

private let chatCollapsibleHeaderMinHeight: CGFloat = 44

/// 每一行消息。
///
/// 这里刻意**不**持有 `@EnvironmentObject appModel`：`JobsView` 在有活跃 Job 时每 5 秒
/// 轮询一次 dashboard，而本视图是它的 `navigationDestination` 子树 —— 一旦订阅 `AppModel`，
/// 每次轮询都会重建全部气泡、把整条会话的 markdown 重新解析一遍。链接拦截所需的
/// `openURL` 由 `JobChatView` 在列表层统一注入。
///
/// 显式 `Equatable` 配合 `ForEach` 里的 `.equatable()`：流式输出只改动最后一条消息，
/// 其余行比较相等后直接跳过 body 求值。
struct ChatBubble: View, Equatable {
    let message: ChatMessage
    let fallbackAgentName: String
    let fallbackAgentIconUrl: String?

    var body: some View {
        Group {
            switch message.kind {
            case .user:
                UserMessageBubble(message: message)
            case .assistant:
                AssistantMessageCard(
                    message: message,
                    agentName: message.agentDisplayName ?? fallbackAgentName,
                    agentIconUrl: message.agentIconUrl ?? fallbackAgentIconUrl
                )
            case .thought:
                ThoughtMessageCard(message: message)
            case .tool:
                ToolCallCard(message: message)
            case .system:
                centeredEvent
            }
        }
    }

    private var centeredEvent: some View {
        HStack(spacing: 8) {
            Image(systemName: message.isFailed ? "exclamationmark.triangle.fill" : "info.circle.fill")
            Text(message.content.isEmpty ? "系统事件".localizedForApp : message.content)
                .textSelection(.enabled)
        }
        .font(.chat(.detail))
        .foregroundStyle(message.isFailed ? QuartetTheme.failed : QuartetTheme.secondaryText)
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
        .background(QuartetTheme.elevated.opacity(0.8), in: Capsule())
        .frame(maxWidth: .infinity)
    }
}

struct UserMessageBubble: View {
    let message: ChatMessage

    private var displayContent: String {
        if message.content == "[image]", !message.imagePaths.isEmpty { return "" }
        if message.content == "[file]", !message.fileAttachments.isEmpty { return "" }
        return message.content
    }

    var body: some View {
        HStack(alignment: .bottom, spacing: 7) {
            Spacer(minLength: 38)
            if !displayContent.isEmpty || message.timestamp != nil {
                VStack(alignment: .trailing, spacing: 4) {
                    if !displayContent.isEmpty {
                        CopyIconButton(text: displayContent, appearance: .plain)
                    }
                    if let timestamp = message.timestamp {
                        Text(chatTimeLabel(timestamp))
                            .font(.chat(.compact))
                            .foregroundStyle(QuartetTheme.secondaryText)
                    }
                }
                .padding(.bottom, 4)
            }
            VStack(alignment: .leading, spacing: 9) {
                ForEach(message.imagePaths, id: \.self) { path in
                    AuthenticatedImage(path: path)
                }
                ForEach(message.fileAttachments, id: \.path) { attachment in
                    AuthenticatedFile(attachment: attachment)
                }
                if !displayContent.isEmpty {
                    MarkdownMessageView(text: displayContent, tone: .user)
                }
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 12)
            .frame(maxWidth: 320, alignment: .leading)
            .background(QuartetTheme.accent, in: UnevenRoundedRectangle(
                topLeadingRadius: 17, bottomLeadingRadius: 17, bottomTrailingRadius: 5, topTrailingRadius: 17, style: .continuous
            ))
        }
        .frame(maxWidth: .infinity, alignment: .trailing)
        .accessibilityElement(children: .combine)
    }
}

struct AssistantMessageCard: View {
    let message: ChatMessage
    let agentName: String
    let agentIconUrl: String?

    /// 与 `CodeBlockView` 同一档行距，让 shell 输出和代码块看起来是同一种“终端”文本。
    @ScaledMetric(relativeTo: .footnote) private var shellLineSpacing: CGFloat = 2.5

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            if let thought = message.thinkingContent, !thought.isEmpty {
                ThoughtPanel(text: thought, isStreaming: false, timestamp: message.timestamp)
            }
            if !message.content.isEmpty {
                VStack(alignment: .leading, spacing: 12) {
                    HStack(spacing: 8) {
                        if message.isShellOutput {
                            Text("💻")
                                .font(.chat(.control))
                                .accessibilityHidden(true)
                        } else {
                            AgentIdentityIcon(iconUrl: agentIconUrl)
                        }
                        Text(message.isShellOutput ? "Shell" : agentName)
                            .font(.chat(.detail, weight: .semibold))
                        if !message.isFinished {
                            StreamingDot(color: QuartetTheme.accent)
                        }
                        Spacer(minLength: 8)
                        if let timestamp = message.timestamp, message.isFinished {
                            Text(chatTimeLabel(timestamp))
                                .font(.chat(.compact))
                                .foregroundStyle(QuartetTheme.secondaryText)
                        }
                        if message.isFinished, !message.content.isEmpty {
                            CopyIconButton(text: message.content, appearance: .plain)
                        }
                    }
                    .foregroundStyle(QuartetTheme.accent)

                    Divider().overlay(QuartetTheme.divider.opacity(0.7))

                    if message.isShellOutput {
                        // 与代码块一致：软换行，不再内嵌横向 ScrollView。
                        Text(message.content)
                            .font(.chat(.detail, design: .monospaced))
                            .foregroundStyle(QuartetTheme.primaryText)
                            .lineSpacing(shellLineSpacing)
                            .textSelection(.enabled)
                            .fixedSize(horizontal: false, vertical: true)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    } else {
                        MarkdownMessageView(text: message.content, tone: .standard)
                    }
                }
                .padding(.horizontal, 16)
                .padding(.vertical, 14)
                .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
                .overlay(RoundedRectangle(cornerRadius: 16, style: .continuous).stroke(QuartetTheme.divider, lineWidth: 1))
                .shadow(color: Color.black.opacity(0.035), radius: 8, y: 2)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

struct AgentIdentityIcon: View {
    @EnvironmentObject private var appModel: AppModel
    let iconUrl: String?
    @State private var image: UIImage?

    var body: some View {
        Group {
            if let image {
                Image(uiImage: image)
                    .resizable()
                    .scaledToFill()
            } else if let icon = textIcon {
                Text(icon)
                    .font(.chat(.control))
            } else {
                Image(systemName: "sparkles")
                    .font(.chat(.detail, weight: .semibold))
            }
        }
        .frame(width: 20, height: 20)
        .clipShape(RoundedRectangle(cornerRadius: 5, style: .continuous))
        .accessibilityHidden(true)
        .task(id: iconUrl) {
            guard textIcon == nil, let iconUrl, !iconUrl.isEmpty else {
                image = nil
                return
            }
            guard let client = try? appModel.apiClient() else { return }
            // 同一个 Agent 的头像会出现在每条 assistant 消息上，交给共享缓存去重：
            // 命中直接同步返回，未命中时多条消息也只合并成一个请求。
            image = try? await ChatImageLoader.shared.image(
                path: iconUrl,
                namespace: appModel.serverAddress,
                maxPixelSize: 20
            ) {
                try await client.fileData(path: iconUrl)
            }
        }
    }

    private var textIcon: String? {
        guard let value = iconUrl?.trimmingCharacters(in: .whitespacesAndNewlines), !value.isEmpty else { return nil }
        if value.hasPrefix("http://") || value.hasPrefix("https://")
            || value.hasPrefix("data:image/") || value.hasPrefix("/api/v1/icon") {
            return nil
        }
        return value
    }
}

struct ThoughtMessageCard: View {
    let message: ChatMessage

    var body: some View {
        ThoughtPanel(
            text: message.content.isEmpty ? "正在思考…" : message.content,
            isStreaming: !message.isFinished,
            timestamp: message.timestamp
        )
    }
}

/// 深度思考面板。折叠策略跟 `ToolCallCard` 一致：流式输出时展开，方便边生成边读；
/// 思考一结束（`isStreaming` 由 true 变 false）自动收起，只留下标题栏。历史回放进来
/// 时 `isStreaming` 本来就是 false，所以直接以折叠态出现。
struct ThoughtPanel: View {
    let text: String
    let isStreaming: Bool
    let timestamp: Int64?
    @State private var isExpanded: Bool

    init(text: String, isStreaming: Bool, timestamp: Int64?) {
        self.text = text
        self.isStreaming = isStreaming
        self.timestamp = timestamp
        _isExpanded = State(initialValue: isStreaming)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            Button {
                withAnimation(.easeInOut(duration: 0.18)) { isExpanded.toggle() }
            } label: {
                HStack(spacing: 8) {
                    Image(systemName: "brain.head.profile")
                        .font(.chat(.detail, weight: .semibold))
                    Text("深度思考")
                        .font(.chat(.detail, weight: .semibold))
                    if isStreaming { StreamingDot(color: QuartetTheme.accent) }
                    Spacer(minLength: 8)
                    if let timestamp, !isStreaming {
                        Text(chatTimeLabel(timestamp))
                            .font(.chat(.compact))
                            .foregroundStyle(QuartetTheme.secondaryText)
                    }
                    Image(systemName: isExpanded ? "chevron.up" : "chevron.down")
                        .font(.chat(.detail, weight: .semibold))
                        .foregroundStyle(QuartetTheme.secondaryText)
                }
                .foregroundStyle(QuartetTheme.accent)
                .frame(minHeight: isExpanded ? 0 : chatCollapsibleHeaderMinHeight)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityLabel("深度思考")
            .accessibilityHint(isExpanded ? "轻点收起思考内容" : "轻点展开思考内容")

            if isExpanded {
                MarkdownMessageView(text: text, tone: .thought)
                    .padding(.top, 9)
                    .transition(.opacity.combined(with: .move(edge: .top)))
            }
        }
        .padding(.horizontal, 16)
        .padding(.vertical, isExpanded ? 13 : 0)
        .background(QuartetTheme.accent.opacity(0.075), in: RoundedRectangle(cornerRadius: 13, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 13, style: .continuous).stroke(QuartetTheme.accent.opacity(0.24), lineWidth: 1))
        .frame(maxWidth: .infinity, alignment: .leading)
        .onChange(of: isStreaming) { wasStreaming, streaming in
            if wasStreaming, !streaming {
                withAnimation(.easeOut(duration: 0.2)) { isExpanded = false }
            }
        }
    }
}

struct ToolCallCard: View {
    let message: ChatMessage
    @State private var isExpanded: Bool

    init(message: ChatMessage) {
        self.message = message
        _isExpanded = State(initialValue: message.toolStatus == .processing || !message.isFinished)
    }

    var body: some View {
        VStack(spacing: 0) {
            Button {
                withAnimation(.easeInOut(duration: 0.18)) { isExpanded.toggle() }
            } label: {
                HStack(spacing: 11) {
                    Text(toolIcon)
                        .font(.chat(.control))
                        .frame(width: 20)
                    Text(displayName)
                        .font(.chat(.control, weight: .medium))
                        .foregroundStyle(QuartetTheme.primaryText)
                        .lineLimit(1)
                    Spacer(minLength: 6)
                    toolStatusBadge
                    Image(systemName: isExpanded ? "chevron.up" : "chevron.down")
                        .font(.chat(.detail, weight: .semibold))
                        .foregroundStyle(QuartetTheme.secondaryText)
                }
                .padding(.horizontal, 15)
                .padding(.vertical, isExpanded ? 13 : 0)
                .frame(minHeight: isExpanded ? 0 : chatCollapsibleHeaderMinHeight)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityLabel("工具 \(displayName)，\(statusLabel)")
            .accessibilityHint(isExpanded ? "轻点收起详情" : "轻点展开参数和结果")

            if isExpanded {
                Divider().overlay(QuartetTheme.divider)
            }

            if isExpanded {
                VStack(alignment: .leading, spacing: 15) {
                    if let arguments = message.toolArguments, !arguments.isEmpty {
                        ToolPayloadSection(title: "PARAMETERS", text: arguments)
                    }
                    if !message.content.isEmpty {
                        ToolPayloadSection(title: "RESULT", text: message.content)
                    } else if status == .processing {
                        HStack(spacing: 8) {
                            ProgressView().controlSize(.small)
                            Text("工具正在执行，结果会实时显示在这里…")
                                .font(.chat(.detail))
                                .foregroundStyle(QuartetTheme.secondaryText)
                        }
                    }
                    if status == .placeholder, let reason = message.placeholderReason, !reason.isEmpty {
                        Label("未完成：\(reason)", systemImage: "minus.circle")
                            .font(.chat(.detail))
                            .foregroundStyle(QuartetTheme.secondaryText)
                            .textSelection(.enabled)
                    }
                }
                .padding(15)
                .transition(.opacity.combined(with: .move(edge: .top)))
            }
        }
        .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 10, style: .continuous).stroke(borderColor, lineWidth: message.isFailed ? 1.25 : 1))
        .clipShape(RoundedRectangle(cornerRadius: 10, style: .continuous))
        .onChange(of: message.toolStatus) { oldStatus, newStatus in
            if oldStatus == .processing, newStatus != .processing {
                withAnimation(.easeOut(duration: 0.2)) { isExpanded = false }
            }
        }
    }

    private var status: ChatMessage.ToolStatus {
        message.toolStatus ?? (message.isFinished ? (message.isFailed ? .error : .success) : .processing)
    }

    private var displayName: String {
        let value = message.toolName?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return value.isEmpty || value == "undefined" ? "Tool" : value
    }

    /// 精确匹配表与前缀表都是 `static let`：原实现每次渲染都会重建这 40 个元组并做两次线性扫描。
    private static let toolIcons: [String: String] = [
        "Agent": "🤖", "Read": "📖", "Edit": "✏️", "Write": "📝",
        "Glob": "🗂️", "Grep": "🔍", "WebSearch": "🌐", "WebFetch": "⬇️",
        "Bash": "💻", "Terminal": "💻", "Task": "📝", "TaskOutput": "📤",
        "TaskStop": "🛑", "TaskCreate": "📝", "TaskGet": "🔎", "TaskUpdate": "🛠️",
        "TaskList": "📋", "EnterPlanMode": "🗺️", "ExitPlanMode": "🚪",
        "NotebookEdit": "📓", "AskUserQuestion": "❓", "Skill": "🧠", "LSP": "🧩",
        "EnterWorktree": "🌿", "ExitWorktree": "🍂", "TeamCreate": "👥➕",
        "TeamDelete": "👥❌", "SendMessage": "✉️", "CronCreate": "⏰",
        "CronDelete": "⏰", "CronList": "⏰", "browser_click": "🖱️",
        "browser_evaluate": "⚙️", "browser_get_html": "📄", "browser_get_page_info": "ℹ️",
        "browser_get_title": "📑", "browser_get_url": "🔗", "browser_navigate": "🧭",
        "browser_pdf": "📋", "browser_screenshot": "📸", "browser_scroll": "📜",
        "browser_type": "⌨️", "browser_wait_visible": "👁️",
    ]

    /// 前缀回退保持原有的声明顺序语义（先来先匹配）。
    private static let toolIconPrefixes: [(String, String)] = [
        ("Agent", "🤖"), ("Read", "📖"), ("Edit", "✏️"), ("Write", "📝"),
        ("Glob", "🗂️"), ("Grep", "🔍"), ("WebSearch", "🌐"), ("WebFetch", "⬇️"),
        ("Bash", "💻"), ("Terminal", "💻"), ("Task", "📝"), ("TaskOutput", "📤"),
        ("TaskStop", "🛑"), ("TaskCreate", "📝"), ("TaskGet", "🔎"), ("TaskUpdate", "🛠️"),
        ("TaskList", "📋"), ("EnterPlanMode", "🗺️"), ("ExitPlanMode", "🚪"),
        ("NotebookEdit", "📓"), ("AskUserQuestion", "❓"), ("Skill", "🧠"), ("LSP", "🧩"),
        ("EnterWorktree", "🌿"), ("ExitWorktree", "🍂"), ("TeamCreate", "👥➕"),
        ("TeamDelete", "👥❌"), ("SendMessage", "✉️"), ("CronCreate", "⏰"),
        ("CronDelete", "⏰"), ("CronList", "⏰"), ("browser_click", "🖱️"),
        ("browser_evaluate", "⚙️"), ("browser_get_html", "📄"), ("browser_get_page_info", "ℹ️"),
        ("browser_get_title", "📑"), ("browser_get_url", "🔗"), ("browser_navigate", "🧭"),
        ("browser_pdf", "📋"), ("browser_screenshot", "📸"), ("browser_scroll", "📜"),
        ("browser_type", "⌨️"), ("browser_wait_visible", "👁️"),
    ]

    private var toolIcon: String {
        let name = displayName
        if let exact = Self.toolIcons[name] { return exact }
        return Self.toolIconPrefixes.first(where: { name.hasPrefix($0.0) })?.1 ?? "💻"
    }

    @ViewBuilder private var toolStatusBadge: some View {
        ZStack {
            Circle().fill(statusColor.opacity(0.11))
            if status == .processing {
                ProgressView().controlSize(.mini).tint(statusColor)
            } else {
                Image(systemName: statusIcon)
                    .font(.chat(.compact, weight: .bold))
                    .foregroundStyle(statusColor)
            }
        }
        .frame(width: 28, height: 28)
        .accessibilityLabel(statusLabel)
    }

    private var statusIcon: String {
        switch status {
        case .success: "checkmark"
        case .error: "xmark"
        case .placeholder: "minus"
        case .processing: "ellipsis"
        }
    }

    private var statusLabel: String {
        switch status {
        case .processing: "运行中".localizedForApp
        case .success: "已完成".localizedForApp
        case .error: "执行失败".localizedForApp
        case .placeholder: "未完成".localizedForApp
        }
    }

    private var statusColor: Color {
        switch status {
        case .processing: QuartetTheme.running
        case .success: QuartetTheme.accent
        case .error: QuartetTheme.failed
        case .placeholder: QuartetTheme.secondaryText
        }
    }

    private var borderColor: Color {
        status == .error ? QuartetTheme.failed.opacity(0.45) : QuartetTheme.divider
    }
}

struct ToolPayloadSection: View {
    let title: String
    let text: String

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 7) {
                Text(title)
                    .font(.chat(.compact, weight: .bold, design: .monospaced))
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .tracking(0.5)
                Spacer()
                CopyIconButton(text: text)
            }
            // 走缓存，且只算一次：原实现在 body 里调了两遍 prettyPrintedJSON，
            // 也就是每次渲染都要完整解析 + 序列化 JSON 两次。
            if let formatted = ChatTextCache.prettyPrintedJSON(from: text) {
                ScrollView(.horizontal, showsIndicators: false) {
                    Text(formatted)
                        .font(.chat(.detail, design: .monospaced))
                        .foregroundStyle(QuartetTheme.primaryText)
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                .padding(12)
                .background(QuartetTheme.canvas, in: RoundedRectangle(cornerRadius: 7, style: .continuous))
                .overlay(RoundedRectangle(cornerRadius: 7, style: .continuous).stroke(QuartetTheme.divider.opacity(0.8)))
            } else {
                MarkdownMessageView(text: text, tone: .tool)
                    .padding(12)
                    .background(QuartetTheme.canvas, in: RoundedRectangle(cornerRadius: 7, style: .continuous))
                    .overlay(RoundedRectangle(cornerRadius: 7, style: .continuous).stroke(QuartetTheme.divider.opacity(0.8)))
            }
        }
    }
}

struct CopyIconButton: View {
    enum Appearance {
        case plain
        case filled
    }

    let text: String
    var appearance: Appearance = .filled
    @State private var copied = false

    var body: some View {
        Button {
            UIPasteboard.general.string = text
            copied = true
            Task {
                try? await Task.sleep(for: .seconds(1.2))
                copied = false
            }
        } label: {
            Image(systemName: copied ? "checkmark" : "doc.on.doc")
                .font(.chat(.detail, weight: .semibold))
                .foregroundStyle(copied ? QuartetTheme.accent : QuartetTheme.secondaryText)
                .frame(width: 26, height: 26)
                .background {
                    if appearance == .filled {
                        RoundedRectangle(cornerRadius: 6)
                            .fill(QuartetTheme.elevated.opacity(0.75))
                    }
                }
        }
        .buttonStyle(.plain)
        .accessibilityLabel(copied ? "已复制" : "复制内容")
    }
}

struct StreamingDot: View {
    let color: Color
    @State private var active = false

    var body: some View {
        Circle()
            .fill(color)
            .frame(width: 6, height: 6)
            .scaleEffect(active ? 1.35 : 0.8)
            .opacity(active ? 0.45 : 1)
            .animation(.easeInOut(duration: 0.8).repeatForever(autoreverses: true), value: active)
            .onAppear { active = true }
            .accessibilityHidden(true)
    }
}

/// 复用同一个 formatter：`Date.formatted(date:time:)` 每次调用都要走一遍格式解析，
/// 而这个标签会出现在每条消息上。`DateFormatter` 非 Sendable，靠 `@MainActor` 隔离
/// —— 三处调用点都在视图 body 里。
@MainActor
private let chatTimeFormatter: DateFormatter = {
    let formatter = DateFormatter()
    formatter.locale = .autoupdatingCurrent
    formatter.timeStyle = .short
    formatter.dateStyle = .none
    return formatter
}()

@MainActor
func chatTimeLabel(_ timestamp: Int64) -> String {
    chatTimeFormatter.string(from: timestamp.quartetDate)
}

func prettyPrintedJSON(_ text: String) -> String? {
    guard let data = text.data(using: .utf8),
          let object = try? JSONSerialization.jsonObject(with: data),
          JSONSerialization.isValidJSONObject(object),
          let formatted = try? JSONSerialization.data(withJSONObject: object, options: [.prettyPrinted, .sortedKeys]) else {
        return nil
    }
    return String(data: formatted, encoding: .utf8)
}

struct OutboxBubble: View {
    let item: LocalOutboxItem

    var body: some View {
        VStack(alignment: .trailing, spacing: 8) {
            HStack(spacing: 7) {
                Text("YOU")
                Text(item.statusTitle)
            }
            .font(.chat(.compact, weight: .bold, design: .monospaced))
            .foregroundStyle(item.isFailed ? QuartetTheme.failed : QuartetTheme.onAccent.opacity(0.76))

            MarkdownMessageView(text: item.displayText, tone: .user)

            if let attachment = item.attachment {
                ChatAttachmentPreview(upload: attachment)
            }

            if let detail = item.failureDetail {
                Text(detail)
                    .font(.chat(.detail, design: .monospaced))
                    .foregroundStyle(QuartetTheme.failed)
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .padding(14)
        .frame(maxWidth: 310, alignment: .leading)
        .background(QuartetTheme.accent, in: RoundedRectangle(cornerRadius: 16))
        .overlay(RoundedRectangle(cornerRadius: 16).stroke(item.isFailed ? QuartetTheme.failed.opacity(0.6) : QuartetTheme.onAccent.opacity(0.16), lineWidth: 1))
        .frame(maxWidth: .infinity, alignment: .trailing)
    }
}

struct OutboxRow: View {
    let item: LocalOutboxItem
    let onCancel: () -> Void
    let onRetry: () -> Void
    let onRestore: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(alignment: .firstTextBaseline) {
                Text(item.summaryLine)
                    .font(.chat(.detail, weight: .semibold))
                    .foregroundStyle(item.isFailed ? QuartetTheme.failed : QuartetTheme.primaryText)
                    .lineLimit(1)
                Spacer()
                Text(item.statusTitle)
                    .font(.chat(.compact, weight: .bold, design: .monospaced))
                    .foregroundStyle(item.isFailed ? QuartetTheme.failed : QuartetTheme.secondaryText)
            }

            if let detail = item.failureDetail {
                Text(detail)
                    .font(.chat(.compact, design: .monospaced))
                    .foregroundStyle(QuartetTheme.failed)
                    .lineLimit(3)
            }

            HStack {
                if item.isCancelable {
                    Button("取消待发送", action: onCancel)
                        .foregroundStyle(QuartetTheme.failed)
                }
                if item.isFailed {
                    Button("恢复到输入框", action: onRestore)
                    Button("重试发送", action: onRetry)
                }
                Spacer()
            }
            .font(.chat(.detail))
        }
        .padding(12)
        .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 14))
        .overlay(RoundedRectangle(cornerRadius: 14).stroke(item.isFailed ? QuartetTheme.failed.opacity(0.4) : QuartetTheme.divider, lineWidth: 1))
    }
}

struct ServerQueueRow: View {
    let index: Int
    let item: QueuedJobMessage
    let showsDivider: Bool
    let deleting: Bool
    let onShowError: () -> Void
    let onDelete: () -> Void

    var body: some View {
        HStack(spacing: 9) {
            Text("\(index)")
                .font(.chat(.compact, design: .monospaced))
                .foregroundStyle(QuartetTheme.secondaryText)
            Text(item.summaryLine)
                .font(.chat(.detail))
                .foregroundStyle(item.state == "blocked" ? QuartetTheme.failed : QuartetTheme.primaryText)
                .lineLimit(1)
            Spacer(minLength: 6)
            if !item.imagePaths.isEmpty {
                Image(systemName: "photo")
                    .font(.chat(.compact))
                    .foregroundStyle(QuartetTheme.secondaryText)
            }
            if !item.fileAttachments.isEmpty {
                Image(systemName: "paperclip")
                    .font(.chat(.compact))
                    .foregroundStyle(QuartetTheme.secondaryText)
            }
            if item.error?.isEmpty == false {
                Button(action: onShowError) {
                    Image(systemName: "exclamationmark.triangle.fill")
                        .foregroundStyle(QuartetTheme.failed)
                        .frame(width: 30, height: 30)
                }
                .buttonStyle(.plain)
                .accessibilityLabel("查看排队错误")
            }
            Button(role: .destructive, action: onDelete) {
                Group {
                    if deleting { ProgressView() }
                    else { Image(systemName: "xmark").font(.chat(.compact, weight: .bold)) }
                }.frame(width: 30, height: 30)
            }
            .buttonStyle(.plain)
            .disabled(deleting)
            .accessibilityLabel("删除排队消息")
        }
        .padding(.leading, 12)
        .padding(.trailing, 8)
        .padding(.vertical, 7)
        .overlay(alignment: .bottom) {
            if showsDivider { Divider().overlay(QuartetTheme.divider) }
        }
        .accessibilityHint(item.error ?? "等待发送")
    }
}

struct AuthenticatedImage: View {
    @EnvironmentObject private var appModel: AppModel
    let path: String
    @State private var image: UIImage?
    @State private var error: String?

    var body: some View {
        Group {
            if let image {
                Image(uiImage: image)
                    .resizable()
                    .scaledToFit()
                    .frame(maxHeight: 280)
                    .clipShape(RoundedRectangle(cornerRadius: 12))
            } else if let error {
                Button { appModel.present(APIError(summary: "图片加载失败", detail: error)) } label: {
                    Label("图片加载失败，查看详情", systemImage: "photo.badge.exclamationmark")
                        .font(.chat(.detail))
                        .foregroundStyle(QuartetTheme.failed)
                }
            } else {
                ProgressView().frame(maxWidth: .infinity).frame(height: 80)
            }
        }
        .task(id: path) {
            do {
                let client = try appModel.apiClient()
                // 按 280pt 展示上限降采样，别把原图整张位图留在内存里。
                image = try await ChatImageLoader.shared.image(
                    path: path,
                    namespace: appModel.serverAddress,
                    maxPixelSize: 280
                ) {
                    try await client.fileData(path: path)
                }
            } catch let apiError as APIError {
                error = apiError.detail
            } catch {
                self.error = String(describing: error)
            }
        }
    }
}

struct AuthenticatedFile: View {
    @EnvironmentObject private var appModel: AppModel
    let attachment: FileAttachment
    @State private var downloading = false
    @State private var previewDocument: PreviewDocument?

    private struct PreviewDocument: Identifiable {
        let id = UUID()
        let url: URL
    }

    var body: some View {
        Button { Task { await openFile() } } label: {
            HStack(spacing: 10) {
                ZStack {
                    RoundedRectangle(cornerRadius: 8).fill(QuartetTheme.onAccent.opacity(0.12))
                    Image(systemName: "doc.fill").font(.chat(.control, weight: .semibold))
                }
                .frame(width: 40, height: 44)
                VStack(alignment: .leading, spacing: 3) {
                    Text(attachment.name).font(.chat(.control, weight: .semibold)).lineLimit(1)
                    if !fileMeta.isEmpty {
                        Text(fileMeta).font(.chat(.compact)).foregroundStyle(QuartetTheme.onAccent.opacity(0.68)).lineLimit(1)
                    }
                }
                Spacer(minLength: 6)
                if downloading { ProgressView().controlSize(.small).tint(QuartetTheme.onAccent) }
                else { Image(systemName: "arrow.down.circle") }
            }
            .padding(10)
            .background(QuartetTheme.onAccent.opacity(0.08), in: RoundedRectangle(cornerRadius: 11))
            .overlay(RoundedRectangle(cornerRadius: 11).stroke(QuartetTheme.onAccent.opacity(0.18), lineWidth: 1))
        }
        .buttonStyle(.plain)
        .accessibilityLabel("打开文件 \(attachment.name)")
        .disabled(downloading)
        .sheet(item: $previewDocument) { document in
            LocalFilePreview(url: document.url)
                .quartetSheetStyle()
        }
    }

    private var fileMeta: String {
        let size = attachment.size.map { ByteCountFormatter.string(fromByteCount: $0, countStyle: .file) }
        return [attachment.mimeType, size].compactMap { $0 }.filter { !$0.isEmpty }.joined(separator: " · ")
    }

    @MainActor private func openFile() async {
        downloading = true
        defer { downloading = false }
        do {
            let data = try await appModel.apiClient().fileData(path: attachment.path, downloadName: attachment.name)
            let safeName = attachment.name.replacingOccurrences(of: "/", with: "-")
            let url = FileManager.default.temporaryDirectory.appendingPathComponent(safeName.isEmpty ? UUID().uuidString : safeName)
            try data.write(to: url, options: .atomic)
            previewDocument = PreviewDocument(url: url)
        } catch {
            appModel.present(error)
        }
    }
}

