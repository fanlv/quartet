import AVFoundation
import PhotosUI
import SwiftUI
import UIKit

struct JobChatView: View {
    @EnvironmentObject private var appModel: AppModel
    @Environment(\.scenePhase) private var scenePhase
    @StateObject private var chat = ChatViewModel()
    let route: ChatRoute

    @State private var draft = ""
    @State private var selectedPhoto: PhotosPickerItem?
    @State private var pendingImage: PendingUpload?
    @State private var confirmsStop = false
    @State private var showsAttachmentMenu = false
    @State private var showsCameraPicker = false
    @State private var showsDocumentPicker = false

    var body: some View {
        VStack(spacing: 0) {
            statusStrip
            messageList
            composer
        }
        .background(QuartetTheme.canvas)
        .navigationTitle(chat.title.isEmpty ? route.summary.displayTitle : chat.title)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItemGroup(placement: .topBarTrailing) {
                NavigationLink {
                    JobDetailView(summary: route.summary)
                } label: {
                    Image(systemName: "info.circle")
                }
                .accessibilityLabel("Job 详情")
                if chat.isRunning {
                    Button(role: .destructive) { confirmsStop = true } label: {
                        Image(systemName: "stop.fill")
                    }
                    .accessibilityLabel("停止生成")
                }
            }
        }
        .task(id: route.summary.id) {
            do {
                let client = try appModel.apiClient()
                await chat.start(route: route, client: client)
            } catch {
                appModel.present(error)
            }
        }
        .onDisappear { chat.stopStreaming() }
        .onChange(of: scenePhase) { _, phase in
            if phase == .background {
                chat.stopStreaming()
            } else if phase == .active {
                Task {
                    do {
                        await chat.start(route: route, client: try appModel.apiClient())
                    } catch {
                        appModel.present(error)
                    }
                }
            }
        }
        .onChange(of: selectedPhoto) { _, item in
            guard let item else { return }
            Task { await loadPhoto(item) }
        }
        .onChange(of: chat.restoreDraftVersion) { _, _ in
            guard let restored = chat.restoreDraft else { return }
            draft = restored.text
            pendingImage = restored.attachment
            selectedPhoto = nil
        }
        .onChange(of: chat.terminalEventVersion) { _, _ in
            guard let terminal = chat.latestTerminalEvent else { return }
            guard route.summary.mode != "graph" else { return }
            Task {
                await appModel.recordInteractiveTerminalEvent(
                    jobID: route.summary.id,
                    outcome: terminal.outcome,
                    finalStatus: terminal.finalStatus,
                    occurredAt: terminal.occurredAt,
                    fallbackTitle: chat.title.isEmpty ? route.summary.displayTitle : chat.title,
                    fallbackWorkspaceID: route.summary.workspaceId
                )
                await appModel.reloadJobs()
            }
        }
        .confirmationDialog("更多图片来源", isPresented: $showsAttachmentMenu, titleVisibility: .visible) {
            Button("相机") { requestCameraAccess() }
            Button("文件") { showsDocumentPicker = true }
            Button("取消", role: .cancel) {}
        } message: {
            Text("照片可使用输入框左侧的照片按钮选择。")
        }
        .confirmationDialog("停止当前执行？", isPresented: $confirmsStop, titleVisibility: .visible) {
            Button("停止", role: .destructive) {
                Task {
                    do {
                        try await appModel.stopJob(id: route.summary.id)
                        chat.markStopped()
                    } catch {
                        appModel.present(error)
                    }
                }
            }
            Button("取消", role: .cancel) {}
        }
        .sheet(isPresented: $showsCameraPicker) {
            CameraImagePicker(
                onImagePicked: { image in
                    showsCameraPicker = false
                    Task { await setCameraImage(image) }
                },
                onCancel: {
                    showsCameraPicker = false
                }
            )
        }
        .sheet(isPresented: $showsDocumentPicker) {
            DocumentImagePicker(
                onDocumentPicked: { url in
                    showsDocumentPicker = false
                    Task { await loadDocument(url) }
                },
                onCancel: {
                    showsDocumentPicker = false
                }
            )
        }
    }

    private var statusStrip: some View {
        VStack(spacing: 8) {
            HStack {
                Circle()
                    .fill(QuartetTheme.statusColor(chat.status))
                    .frame(width: 8, height: 8)
                Text(chat.statusLabel)
                if let phase = chat.phaseLabel {
                    Text("· \(phase)")
                }
                if let session = chat.sessionIDDisplay {
                    Text("· \(session)")
                }
                Spacer()
                Text(chat.streamStateLabel)
                    .foregroundStyle(chat.streamStateColor)
            }
            .font(.system(size: 10, weight: .bold, design: .monospaced))
            .foregroundStyle(QuartetTheme.secondaryText)
            RunningPulseLine(active: chat.isRunning)
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 10)
        .background(QuartetTheme.surface)
    }

    private var messageList: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(spacing: 18) {
                    if chat.loading && chat.messages.isEmpty && chat.outbox.isEmpty {
                        ProgressView().padding(.top, 80)
                    }
                    ForEach(chat.messages) { message in
                        ChatBubble(message: message)
                            .id(message.id)
                    }
                    ForEach(chat.timelineOutboxItems) { item in
                        OutboxBubble(item: item)
                            .id(item.id)
                    }
                    Color.clear.frame(height: 1).id("chat-bottom")
                }
                .padding(16)
            }
            .onChange(of: chat.scrollAnchor) { _, _ in
                withAnimation(.easeOut(duration: 0.2)) {
                    proxy.scrollTo("chat-bottom", anchor: .bottom)
                }
            }
        }
    }

    private var composer: some View {
        VStack(spacing: 10) {
            if let error = chat.errorDetail {
                Button {
                    appModel.present(APIError(summary: "对话错误", detail: error))
                } label: {
                    HStack {
                        Image(systemName: "exclamationmark.triangle.fill")
                        Text(error).lineLimit(2)
                        Spacer()
                        Text("详情")
                    }
                    .font(.caption)
                    .foregroundStyle(QuartetTheme.failed)
                }
            }

            if let pendingImage {
                ChatAttachmentPreview(upload: pendingImage)
                HStack {
                    Button("移除图片") {
                        self.pendingImage = nil
                        selectedPhoto = nil
                    }
                    .font(.caption)
                    .foregroundStyle(QuartetTheme.failed)
                    Spacer()
                }
            }

            if !chat.outbox.isEmpty {
                VStack(spacing: 8) {
                    ForEach(chat.outbox) { item in
                        OutboxRow(
                            item: item,
                            onCancel: { chat.cancelOutboxItem(id: item.id) },
                            onRetry: { chat.retryOutboxItem(id: item.id) },
                            onRestore: { chat.restoreOutboxItem(id: item.id) }
                        )
                    }
                }
            }

            HStack(alignment: .bottom, spacing: 10) {
                PhotosPicker(selection: $selectedPhoto, matching: .images) {
                    Image(systemName: pendingImage == nil ? "photo" : "photo.fill")
                        .font(.headline)
                        .foregroundStyle(pendingImage == nil ? QuartetTheme.secondaryText : QuartetTheme.accent)
                        .frame(width: 36, height: 44)
                }
                .accessibilityLabel("从相册选择图片")

                Button {
                    showsAttachmentMenu = true
                } label: {
                    Image(systemName: "plus.viewfinder")
                        .font(.headline)
                        .foregroundStyle(QuartetTheme.secondaryText)
                        .frame(width: 36, height: 44)
                }
                .accessibilityLabel("更多图片来源")

                TextField("继续对话…", text: $draft, axis: .vertical)
                    .lineLimit(1...6)
                    .padding(.horizontal, 14)
                    .padding(.vertical, 11)
                    .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 18))

                Button { enqueueDraft() } label: {
                    Image(systemName: "arrow.up")
                        .font(.headline.weight(.bold))
                        .foregroundStyle(.black)
                        .frame(width: 44, height: 44)
                        .background(QuartetTheme.accent, in: Circle())
                }
                .disabled(chat.loading || (draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && pendingImage == nil))
                .opacity(chat.sending ? 0.55 : 1)
                .accessibilityLabel("发送消息")
            }

            if chat.isRunning {
                Text("当前轮次运行中，新消息会先进入本地队列，等本轮结束后自动按顺序发送。")
                    .font(.caption)
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .frame(maxWidth: .infinity, alignment: .leading)
            } else if chat.hasQueuedMessages {
                Text("队列中的消息会依次发送，可在发送前取消。")
                    .font(.caption)
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .padding(.horizontal, 12)
        .padding(.top, 10)
        .padding(.bottom, 8)
        .background(.ultraThinMaterial)
    }

    private func enqueueDraft() {
        let text = draft.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty || pendingImage != nil else { return }
        chat.enqueueDraft(text: text, attachment: pendingImage)
        draft = ""
        pendingImage = nil
        selectedPhoto = nil
    }

    private func loadPhoto(_ item: PhotosPickerItem) async {
        do {
            guard let data = try await item.loadTransferable(type: Data.self) else { return }
            let contentType = item.supportedContentTypes.first
            let upload = try ChatAttachmentProcessor.prepareImageUpload(
                data: data,
                suggestedFilename: "ios-\(UUID().uuidString).\(contentType?.preferredFilenameExtension ?? "jpg")",
                contentType: contentType
            )
            pendingImage = upload
        } catch {
            appModel.present(error)
            selectedPhoto = nil
        }
    }

    private func setCameraImage(_ image: UIImage) async {
        do {
            pendingImage = try ChatAttachmentProcessor.prepareImageUpload(
                image: image,
                suggestedFilename: "camera-\(UUID().uuidString).jpg"
            )
        } catch {
            appModel.present(error)
        }
    }

    private func loadDocument(_ url: URL) async {
        do {
            let upload = try await readDocumentUpload(url)
            pendingImage = upload
        } catch {
            appModel.present(error)
        }
    }

    private func requestCameraAccess() {
        guard UIImagePickerController.isSourceTypeAvailable(.camera) else {
            appModel.present(APIError(summary: "相机不可用", detail: "当前设备没有可用相机。"))
            return
        }
        switch AVCaptureDevice.authorizationStatus(for: .video) {
        case .authorized:
            showsCameraPicker = true
        case .notDetermined:
            AVCaptureDevice.requestAccess(for: .video) { granted in
                Task { @MainActor in
                    if granted {
                        showsCameraPicker = true
                    } else {
                        appModel.present(APIError(summary: "没有相机权限", detail: "请在系统设置中允许 Quartet 访问相机后重试。"))
                    }
                }
            }
        case .denied, .restricted:
            appModel.present(APIError(summary: "没有相机权限", detail: "请在系统设置中允许 Quartet 访问相机后重试。"))
        @unknown default:
            appModel.present(APIError(summary: "相机权限状态未知", detail: "系统返回了未知的相机权限状态。"))
        }
    }

    private func readDocumentUpload(_ url: URL) async throws -> PendingUpload {
        let didAccess = url.startAccessingSecurityScopedResource()
        defer {
            if didAccess {
                url.stopAccessingSecurityScopedResource()
            }
        }

        let data = try await Task.detached(priority: .userInitiated) {
            try Data(contentsOf: url)
        }.value

        return try await MainActor.run {
            try ChatAttachmentProcessor.prepareImageUpload(
                data: data,
                suggestedFilename: url.lastPathComponent,
                contentType: UTType(filenameExtension: url.pathExtension)
            )
        }
    }
}

private struct ChatBubble: View {
    @EnvironmentObject private var appModel: AppModel
    let message: ChatMessage

    var body: some View {
        VStack(alignment: message.kind == .user ? .trailing : .leading, spacing: 7) {
            HStack(spacing: 7) {
                Text(label)
                if !message.isFinished { ProgressView().controlSize(.mini) }
            }
            .font(.system(size: 10, weight: .bold, design: .monospaced))
            .foregroundStyle(labelColor)

            if message.kind == .tool {
                Text(message.content.isEmpty ? "…" : message.content)
                    .font(.system(.footnote, design: .monospaced))
                    .foregroundStyle(QuartetTheme.primaryText)
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
            } else {
                MarkdownMessageView(text: message.content.isEmpty ? "…" : message.content)
            }

            ForEach(message.imagePaths, id: \.self) { path in
                AuthenticatedImage(path: path)
            }

            if let detail = message.detail, !detail.isEmpty {
                DisclosureGroup("调用详情") {
                    Text(detail)
                        .font(.system(.caption, design: .monospaced))
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(.top, 8)
                }
                .font(.caption)
                .foregroundStyle(QuartetTheme.secondaryText)
            }
        }
        .padding(14)
        .frame(maxWidth: message.kind == .user ? 310 : .infinity, alignment: .leading)
        .background(background, in: RoundedRectangle(cornerRadius: 16))
        .overlay(RoundedRectangle(cornerRadius: 16).stroke(border, lineWidth: 1))
        .frame(maxWidth: .infinity, alignment: message.kind == .user ? .trailing : .leading)
        .environment(\.openURL, OpenURLAction { url in
            openSafely(url)
        })
    }

    private func openSafely(_ url: URL) -> OpenURLAction.Result {
        guard let scheme = url.scheme?.lowercased(), ["http", "https"].contains(scheme) else {
            appModel.present(APIError(summary: "链接已拦截", detail: "仅允许打开 http/https 链接。\n当前链接：\(url.absoluteString)"))
            return .handled
        }
        UIApplication.shared.open(url)
        return .handled
    }

    private var label: String {
        switch message.kind {
        case .user: "YOU"
        case .assistant: "AGENT"
        case .thought: "THINKING"
        case .tool: "TOOL"
        case .system: "SYSTEM"
        }
    }

    private var labelColor: Color {
        message.isFailed ? QuartetTheme.failed : (message.kind == .thought ? QuartetTheme.running : QuartetTheme.accent)
    }

    private var background: Color {
        message.kind == .user ? QuartetTheme.accent.opacity(0.14) : QuartetTheme.surface
    }

    private var border: Color {
        message.isFailed ? QuartetTheme.failed.opacity(0.6) : QuartetTheme.divider
    }
}

private struct OutboxBubble: View {
    let item: LocalOutboxItem

    var body: some View {
        VStack(alignment: .trailing, spacing: 8) {
            HStack(spacing: 7) {
                Text("YOU")
                Text(item.statusTitle)
            }
            .font(.system(size: 10, weight: .bold, design: .monospaced))
            .foregroundStyle(item.isFailed ? QuartetTheme.failed : QuartetTheme.secondaryText)

            MarkdownMessageView(text: item.displayText)

            if let attachment = item.attachment {
                ChatAttachmentPreview(upload: attachment)
            }

            if let detail = item.failureDetail {
                Text(detail)
                    .font(.system(.caption, design: .monospaced))
                    .foregroundStyle(QuartetTheme.failed)
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .padding(14)
        .frame(maxWidth: 310, alignment: .leading)
        .background(QuartetTheme.accent.opacity(0.14), in: RoundedRectangle(cornerRadius: 16))
        .overlay(RoundedRectangle(cornerRadius: 16).stroke(item.isFailed ? QuartetTheme.failed.opacity(0.6) : QuartetTheme.divider, lineWidth: 1))
        .frame(maxWidth: .infinity, alignment: .trailing)
    }
}

private struct OutboxRow: View {
    let item: LocalOutboxItem
    let onCancel: () -> Void
    let onRetry: () -> Void
    let onRestore: () -> Void

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(alignment: .firstTextBaseline) {
                Text(item.summaryLine)
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(item.isFailed ? QuartetTheme.failed : QuartetTheme.primaryText)
                    .lineLimit(1)
                Spacer()
                Text(item.statusTitle)
                    .font(.system(size: 10, weight: .bold, design: .monospaced))
                    .foregroundStyle(item.isFailed ? QuartetTheme.failed : QuartetTheme.secondaryText)
            }

            if let detail = item.failureDetail {
                Text(detail)
                    .font(.system(.caption2, design: .monospaced))
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
            .font(.caption)
        }
        .padding(12)
        .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 14))
        .overlay(RoundedRectangle(cornerRadius: 14).stroke(item.isFailed ? QuartetTheme.failed.opacity(0.4) : QuartetTheme.divider, lineWidth: 1))
    }
}

private struct MarkdownMessageView: View {
    let text: String

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            ForEach(MarkdownRenderer.blocks(from: text)) { block in
                switch block.kind {
                case .markdown(let content):
                    MarkdownTextBlock(text: content)
                case .code(let language, let content):
                    CodeBlockView(language: language, code: content)
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

private struct MarkdownTextBlock: View {
    let text: String

    var body: some View {
        if let attributed = MarkdownRenderer.attributedString(from: text) {
            Text(attributed)
                .foregroundStyle(QuartetTheme.primaryText)
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
        } else {
            Text(text)
                .foregroundStyle(QuartetTheme.primaryText)
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
    }
}

private struct CodeBlockView: View {
    let language: String?
    let code: String

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Text((language?.isEmpty == false ? language! : "code").uppercased())
                    .font(.system(size: 10, weight: .bold, design: .monospaced))
                    .foregroundStyle(QuartetTheme.secondaryText)
                Spacer()
                Button("复制代码") {
                    UIPasteboard.general.string = code
                }
                .font(.caption)
            }

            ScrollView(.horizontal, showsIndicators: false) {
                Text(code)
                    .font(.system(.footnote, design: .monospaced))
                    .foregroundStyle(QuartetTheme.primaryText)
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .padding(12)
        .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 12))
        .overlay(RoundedRectangle(cornerRadius: 12).stroke(QuartetTheme.divider, lineWidth: 1))
    }
}

private struct AuthenticatedImage: View {
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
                        .font(.caption)
                        .foregroundStyle(QuartetTheme.failed)
                }
            } else {
                ProgressView().frame(maxWidth: .infinity).frame(height: 80)
            }
        }
        .task(id: path) {
            do {
                let data = try await appModel.apiClient().fileData(path: path)
                guard let decoded = UIImage(data: data) else {
                    throw APIError(summary: "图片数据无效", detail: "无法将 \(path) 解码为图片。")
                }
                image = decoded
            } catch let apiError as APIError {
                error = apiError.detail
            } catch {
                self.error = String(describing: error)
            }
        }
    }
}

private enum MarkdownRenderer {
    struct Block: Identifiable {
        enum Kind {
            case markdown(String)
            case code(language: String?, content: String)
        }

        let id = UUID()
        let kind: Kind
    }

    static func blocks(from text: String) -> [Block] {
        guard text.contains("```") else { return [.init(kind: .markdown(text))] }
        var result: [Block] = []
        var remaining = text[...]

        while let opening = remaining.range(of: "```") {
            let leading = String(remaining[..<opening.lowerBound])
            if !leading.isEmpty {
                result.append(.init(kind: .markdown(leading)))
            }
            remaining = remaining[opening.upperBound...]

            let headerEnd = remaining.firstIndex(of: "\n") ?? remaining.endIndex
            let language = String(remaining[..<headerEnd]).trimmingCharacters(in: .whitespacesAndNewlines)
            if headerEnd < remaining.endIndex {
                remaining = remaining[remaining.index(after: headerEnd)...]
            } else {
                remaining = remaining[headerEnd...]
            }

            guard let closing = remaining.range(of: "```") else {
                result.append(.init(kind: .markdown("```\(language)\n\(remaining)")))
                remaining = "".suffix(0)
                break
            }

            let code = String(remaining[..<closing.lowerBound]).trimmingCharacters(in: .newlines)
            result.append(.init(kind: .code(language: language.isEmpty ? nil : language, content: code)))
            remaining = remaining[closing.upperBound...]
        }

        if !remaining.isEmpty {
            result.append(.init(kind: .markdown(String(remaining))))
        }
        return result.isEmpty ? [.init(kind: .markdown(text))] : result
    }

    static func attributedString(from text: String) -> AttributedString? {
        do {
            return try AttributedString(
                markdown: text,
                options: AttributedString.MarkdownParsingOptions(interpretedSyntax: .full)
            )
        } catch {
            return nil
        }
    }
}

private struct ComposerDraft: Hashable, Sendable {
    let text: String
    let attachment: PendingUpload?
}

private struct ChatTerminalEvent: Equatable, Sendable {
    let outcome: String
    let finalStatus: String
    let occurredAt: Int64?
}

private struct OutboxRequestContext: Hashable, Sendable {
    let targetSessionID: String?
    let modelID: String?
    let agentType: String?
    let modeID: String?
    let thoughtLevelID: String?
    let bypassCommand: Bool
}

private struct LocalOutboxItem: Identifiable, Hashable, Sendable {
    enum State: Hashable, Sendable {
        case queued
        case uploading
        case sending
        case awaitingEcho
        case failed(detail: String, requiresNewMessageID: Bool)
    }

    let id: String
    let draft: ComposerDraft
    let createdAt: Int64
    let isInitialDraft: Bool
    let requestContext: OutboxRequestContext
    var remoteImagePaths: [String]
    var state: State

    var displayText: String {
        let trimmed = draft.text.trimmingCharacters(in: .whitespacesAndNewlines)
        if trimmed.isEmpty, draft.attachment != nil { return "[image]" }
        return trimmed.isEmpty ? "…" : trimmed
    }

    var attachment: PendingUpload? {
        draft.attachment
    }

    var statusTitle: String {
        switch state {
        case .queued: "QUEUED"
        case .uploading: "UPLOADING"
        case .sending: "SENDING"
        case .awaitingEcho: "SYNCING"
        case .failed: "FAILED"
        }
    }

    var failureDetail: String? {
        if case let .failed(detail, _) = state { return detail }
        return nil
    }

    var isFailed: Bool {
        if case .failed = state { return true }
        return false
    }

    var isCancelable: Bool {
        if case .queued = state { return true }
        return false
    }

    var retryRequiresNewMessageID: Bool {
        if case let .failed(_, requiresNewMessageID) = state { return requiresNewMessageID }
        return false
    }

    var isVisibleInTimeline: Bool {
        true
    }

    var summaryLine: String {
        let trimmed = draft.text.trimmingCharacters(in: .whitespacesAndNewlines)
        if !trimmed.isEmpty { return trimmed }
        if draft.attachment != nil { return "[image]" }
        return "空消息"
    }
}

private enum StreamConnectionState: Equatable {
    case offline
    case connecting
    case live
    case reconnecting

    var label: String {
        switch self {
        case .offline: "OFFLINE"
        case .connecting: "CONNECTING"
        case .live: "LIVE"
        case .reconnecting: "RECONNECTING"
        }
    }
}

@MainActor
private final class ChatViewModel: ObservableObject {
    @Published var messages: [ChatMessage] = []
    @Published var outbox: [LocalOutboxItem] = []
    @Published var title = ""
    @Published var status = "pending"
    @Published var loading = true
    @Published var sending = false
    @Published private var streamState: StreamConnectionState = .offline
    @Published var phaseLabel: String?
    @Published var errorDetail: String?
    @Published var restoreDraft: ComposerDraft?
    @Published var restoreDraftVersion = 0
    @Published var scrollAnchor = 0
    @Published var latestTerminalEvent: ChatTerminalEvent?
    @Published var terminalEventVersion = 0

    private var client: APIClient?
    private var jobID = ""
    private(set) var sessionID: String?
    private var preferredSessionID: String?
    private var modelID: String?
    private var agentType: String?
    private var modeID: String?
    private var thoughtLevelID: String?
    private var lastEventID: UInt64 = 0
    private var streamTask: Task<Void, Never>?
    private var didSeedInitialDraft = false
    private var isTurnRunning = false
    private var isProcessingOutbox = false

    var isRunning: Bool { isTurnRunning }
    var hasQueuedMessages: Bool {
        outbox.contains { if case .queued = $0.state { return true } else { return false } }
    }
    var timelineOutboxItems: [LocalOutboxItem] {
        outbox.filter(\.isVisibleInTimeline)
    }
    var sessionIDDisplay: String? {
        guard let sessionID, !sessionID.isEmpty else { return nil }
        return "SESSION \(sessionID.suffix(8))"
    }
    var streamStateLabel: String {
        streamState.label
    }
    var streamStateColor: Color {
        switch streamState {
        case .live: QuartetTheme.accent
        case .connecting, .reconnecting: QuartetTheme.running
        case .offline: QuartetTheme.secondaryText
        }
    }

    var statusLabel: String {
        switch status {
        case "pending": "等待中"
        case "running": "运行中"
        case "completed": "已完成"
        case "failed": "失败"
        case "stopped": "已停止"
        case "awaitingInput": "等待输入"
        default: status
        }
    }

    func start(route: ChatRoute, client: APIClient) async {
        stopStreaming()
        self.client = client
        jobID = route.summary.id
        title = route.summary.displayTitle
        status = route.summary.status
        preferredSessionID = route.targetSessionID
        modelID = route.modelID ?? route.summary.modelId
        agentType = route.agentType
        modeID = route.modeID
        thoughtLevelID = route.thoughtLevelID
        loading = true
        errorDetail = nil

        do {
            let detail = try await client.job(id: jobID)
            title = detail.title
            status = detail.status
            lastEventID = detail.lastEventSeq
            isTurnRunning = detail.status == "running" || (detail.status == "pending" && hasPriorConversation(detail))

            let interactiveSessions = detail.sessionIds ?? []
            let fallbackSession = interactiveSessions.last ?? detail.graphSessionIds?.last
            let requestedSession = preferredSessionID?.trimmingCharacters(in: .whitespacesAndNewlines)
            sessionID = (requestedSession?.isEmpty == false ? requestedSession : nil) ?? fallbackSession
            if interactiveSessions.isEmpty && requestedSession?.isEmpty != false {
                agentType = detail.initialAgentId ?? agentType
                modelID = detail.firstModelId ?? modelID
                modeID = detail.initialAcpMode ?? modeID
                thoughtLevelID = detail.initialAcpThoughtLevel ?? thoughtLevelID
            }

            if requestedSession?.isEmpty == false, let sessionID {
                try await loadHistory(sessionID: sessionID)
            } else if !interactiveSessions.isEmpty {
                try await loadInteractiveHistory(sessionIDs: interactiveSessions)
            } else if let sessionID {
                try await loadHistory(sessionID: sessionID)
            } else {
                messages = []
            }

            loading = false
            if isTurnRunning || sessionID != nil || route.initialMessage != nil || route.initialAttachment != nil || route.initialImagePaths != nil {
                startStreaming()
            }

            if !didSeedInitialDraft, route.initialMessage != nil || route.initialAttachment != nil || route.initialImagePaths != nil {
                didSeedInitialDraft = true
                enqueueDraft(
                    text: route.initialMessage?.trimmingCharacters(in: .whitespacesAndNewlines) ?? "",
                    attachment: route.initialAttachment,
                    remoteImagePaths: route.initialImagePaths ?? [],
                    isInitialDraft: true
                )
            } else {
                scheduleOutboxProcessing()
            }
        } catch {
            loading = false
            let detail = errorText(error)
            errorDetail = detail
            restoreInitialDraftIfNeeded(route: route, detail: detail)
        }
    }

    func enqueueDraft(
        text: String,
        attachment: PendingUpload?,
        remoteImagePaths: [String] = [],
        isInitialDraft: Bool = false
    ) {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty || attachment != nil || !remoteImagePaths.isEmpty else { return }
        let item = LocalOutboxItem(
            id: UUID().uuidString.lowercased(),
            draft: ComposerDraft(text: trimmed, attachment: attachment),
            createdAt: Int64(Date().timeIntervalSince1970 * 1_000),
            isInitialDraft: isInitialDraft,
            requestContext: currentRequestContext(bypassCommand: isInitialDraft),
            remoteImagePaths: remoteImagePaths,
            state: .queued
        )
        outbox.append(item)
        bumpScrollAnchor()
        scheduleOutboxProcessing()
    }

    func cancelOutboxItem(id: String) {
        outbox.removeAll { $0.id == id && $0.isCancelable }
        bumpScrollAnchor()
    }

    func retryOutboxItem(id: String) {
        guard let index = outbox.firstIndex(where: { $0.id == id }) else { return }
        let item = outbox[index]
        let startsNewExecution = item.retryRequiresNewMessageID
        outbox[index] = LocalOutboxItem(
            id: startsNewExecution ? UUID().uuidString.lowercased() : item.id,
            draft: item.draft,
            createdAt: startsNewExecution ? Int64(Date().timeIntervalSince1970 * 1_000) : item.createdAt,
            isInitialDraft: item.isInitialDraft,
            requestContext: startsNewExecution
                ? currentRequestContext(bypassCommand: item.isInitialDraft)
                : item.requestContext,
            remoteImagePaths: item.remoteImagePaths,
            state: .queued
        )
        scheduleOutboxProcessing()
    }

    private func currentRequestContext(bypassCommand: Bool) -> OutboxRequestContext {
        OutboxRequestContext(
            targetSessionID: preferredSessionID ?? sessionID,
            modelID: modelID,
            agentType: agentType,
            modeID: modeID,
            thoughtLevelID: thoughtLevelID,
            bypassCommand: bypassCommand
        )
    }

    func restoreOutboxItem(id: String) {
        guard let index = outbox.firstIndex(where: { $0.id == id }) else { return }
        let item = outbox.remove(at: index)
        publishRestore(item.draft)
        bumpScrollAnchor()
    }

    func stopStreaming() {
        streamTask?.cancel()
        streamTask = nil
        streamState = .offline
    }

    func markStopped() {
        status = "stopped"
        isTurnRunning = false
        stopStreaming()
        scheduleOutboxProcessing()
    }

    private func restoreInitialDraftIfNeeded(route: ChatRoute, detail: String) {
        let hasInitialContent = (route.initialMessage?.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty == false)
            || route.initialAttachment != nil
            || !(route.initialImagePaths ?? []).isEmpty
        guard hasInitialContent else { return }
        publishRestore(ComposerDraft(text: route.initialMessage ?? "", attachment: route.initialAttachment))
        errorDetail = detail
    }

    private func loadHistory(sessionID: String) async throws {
        guard let client else { return }
        let response = try await client.sessionMessages(id: sessionID)
        modelID = response.modelId
        agentType = response.type
        modeID = response.acpMode
        thoughtLevelID = response.acpThoughtLevel
        messages = response.messages.map { ChatMessage(history: $0) }
        removeEchoedOutboxItems()
        bumpScrollAnchor()
    }

    private func loadInteractiveHistory(sessionIDs: [String]) async throws {
        guard let client else { return }
        var combined: [ChatMessage] = []
        var latest: SessionMessagesResponse?
        let nonEmptySessionIDs = sessionIDs.filter { !$0.isEmpty }
        for (index, currentSessionID) in nonEmptySessionIDs.enumerated() {
            let response = try await client.sessionMessages(id: currentSessionID)
            latest = response
            let isLatestSession = index == nonEmptySessionIDs.count - 1
            combined.append(contentsOf: response.messages.map {
                ChatMessage(history: $0, idPrefix: isLatestSession ? nil : currentSessionID)
            })
        }
        if let latest {
            modelID = latest.modelId
            agentType = latest.type
            modeID = latest.acpMode
            thoughtLevelID = latest.acpThoughtLevel
        }
        messages = combined
        removeEchoedOutboxItems()
        bumpScrollAnchor()
    }

    private func startStreaming() {
        guard streamTask == nil, let client else { return }
        let jobID = self.jobID
        streamState = .connecting
        streamTask = Task { @MainActor [weak self] in
            var attempts = 0
            while !Task.isCancelled {
                guard let self else { return }
                let resumeID = self.lastEventID
                do {
                    try await client.streamEvents(
                        jobID: jobID,
                        lastEventID: resumeID,
                        onOpen: {
                            self.streamState = .live
                            self.errorDetail = nil
                            self.scheduleOutboxProcessing()
                        },
                        onEvent: { event, id in
                            self.apply(event, id: id)
                        }
                    )
                    attempts = 0
                    self.streamState = .reconnecting
                    try await Task.sleep(for: .seconds(1))
                } catch is CancellationError {
                    return
                } catch {
                    attempts += 1
                    self.streamState = .reconnecting
                    self.errorDetail = self.errorText(error)
                    do {
                        try await Task.sleep(for: .seconds(min(8, attempts * 2)))
                        let snapshot = try await client.job(id: jobID)
                        self.status = snapshot.status
                        self.lastEventID = snapshot.lastEventSeq
                        let interactiveSessions = snapshot.sessionIds ?? []
                        let fallbackSession = interactiveSessions.last ?? snapshot.graphSessionIds?.last
                        self.sessionID = self.preferredSessionID ?? fallbackSession
                        if interactiveSessions.isEmpty && self.preferredSessionID == nil {
                            self.agentType = snapshot.initialAgentId ?? self.agentType
                            self.modelID = snapshot.firstModelId ?? self.modelID
                            self.modeID = snapshot.initialAcpMode ?? self.modeID
                            self.thoughtLevelID = snapshot.initialAcpThoughtLevel ?? self.thoughtLevelID
                        }
                        if self.preferredSessionID != nil, let session = self.sessionID {
                            try await self.loadHistory(sessionID: session)
                        } else if !interactiveSessions.isEmpty {
                            try await self.loadInteractiveHistory(sessionIDs: interactiveSessions)
                        } else if let session = self.sessionID {
                            try await self.loadHistory(sessionID: session)
                        }
                        self.isTurnRunning = snapshot.status == "running" || (snapshot.status == "pending" && self.hasPriorConversation(snapshot))
                    } catch is CancellationError {
                        return
                    } catch {
                        self.errorDetail = self.errorText(error)
                    }
                }
            }
        }
    }

    private func waitForStreamReady() async throws {
        for _ in 0..<150 {
            try Task.checkCancellation()
            if streamState == .live { return }
            try await Task.sleep(for: .milliseconds(100))
        }
        throw APIError(
            summary: "实时连接超时",
            detail: "GET /api/v1/job/\(jobID)/events 在 15 秒内未建立连接，消息尚未发送。"
        )
    }

    private func scheduleOutboxProcessing() {
        guard !isProcessingOutbox else { return }
        Task { @MainActor [weak self] in
            await self?.processOutboxIfPossible()
        }
    }

    private func processOutboxIfPossible() async {
        guard !isProcessingOutbox else { return }
        guard !loading, !sending, !isTurnRunning else { return }
        guard let index = outbox.firstIndex(where: {
            if case .queued = $0.state { return true }
            return false
        }) else { return }

        isProcessingOutbox = true
        defer { isProcessingOutbox = false }
        await dispatchOutboxItem(at: index)

        if outbox.contains(where: {
            if case .queued = $0.state { return true }
            return false
        }) && !isTurnRunning {
            scheduleOutboxProcessing()
        }
    }

    private func dispatchOutboxItem(at index: Int) async {
        guard outbox.indices.contains(index), let client else { return }
        let itemID = outbox[index].id
        sending = true
        defer { sending = false }

        do {
            outbox[index].state = .sending
            startStreaming()
            try await waitForStreamReady()

            if let attachment = outbox[index].attachment, outbox[index].remoteImagePaths.isEmpty {
                outbox[index].state = .uploading
                let path = try await client.uploadImage(
                    data: attachment.data,
                    filename: attachment.filename,
                    mimeType: attachment.mimeType
                )
                guard let refreshed = outbox.firstIndex(where: { $0.id == itemID }) else { return }
                outbox[refreshed].remoteImagePaths = [path]
            }

            guard let refreshed = outbox.firstIndex(where: { $0.id == itemID }) else { return }
            let item = outbox[refreshed]
            outbox[refreshed].state = .sending
            let content = item.displayText
            let response = try await client.sendMessage(
                jobID: jobID,
                body: SendMessageRequest(
                    messages: [.init(
                        id: item.id,
                        content: content,
                        timestamp: item.createdAt,
                        imageUrls: item.remoteImagePaths.isEmpty ? nil : item.remoteImagePaths
                    )],
                    modelId: item.requestContext.modelID,
                    agentType: item.requestContext.agentType,
                    sessionId: item.requestContext.targetSessionID,
                    clientMessageId: item.id,
                    acpMode: item.requestContext.modeID,
                    acpThoughtLevel: item.requestContext.thoughtLevelID,
                    bypassCommand: item.requestContext.bypassCommand
                )
            )
            await reconcileSendResponse(response, itemID: itemID)
        } catch {
            let detail = errorText(error)
            if let failedIndex = outbox.firstIndex(where: { $0.id == itemID }),
               case .failed = outbox[failedIndex].state {
                errorDetail = outbox[failedIndex].failureDetail ?? detail
            } else if let failedIndex = outbox.firstIndex(where: { $0.id == itemID }) {
                outbox[failedIndex].state = .failed(detail: detail, requiresNewMessageID: false)
                publishRestore(outbox[failedIndex].draft)
                errorDetail = detail
            }
            // The POST result may be unknown while JOB_STARTED already arrived
            // over SSE. Do not reopen the queue/composer as idle in that case.
            isTurnRunning = status == "running"
            bumpScrollAnchor()
        }
    }

    private func reconcileSendResponse(_ response: SendMessageResponse, itemID: String) async {
        if response.status == "command_dispatched" || response.status == "command_duplicate" {
            if let event = response.event {
                applyCommandEvent(event, fallbackClientMessageID: itemID)
            }
            outbox.removeAll { $0.id == itemID }
            bumpScrollAnchor()
            return
        }
        guard response.status == "started" || response.isDuplicate else {
            markOutboxFailed(
                itemID: itemID,
                detail: "POST /api/v1/job/\(jobID)/message 返回未知状态：\(response.status)"
            )
            return
        }
        if let responseID = response.clientMessageId, responseID != itemID {
            markOutboxFailed(
                itemID: itemID,
                detail: "服务端返回 clientMessageId=\(responseID)，但当前消息是 \(itemID)。"
            )
            return
        }
        guard response.isDuplicate else {
            setAwaitingEcho(itemID: itemID)
            return
        }

        switch response.messageState {
        case "processing":
            setAwaitingEcho(itemID: itemID)
            do {
                try await refreshSnapshotAndHistory()
                if let index = outbox.firstIndex(where: { $0.id == itemID }) {
                    outbox[index].state = .awaitingEcho
                }
                if outbox.contains(where: { $0.id == itemID }) {
                    isTurnRunning = true
                    status = "running"
                } else {
                    // The history already contains the stable clientMessageId.
                    // Treat the durable history as newer than a stale processing
                    // receipt and keep the authoritative snapshot status.
                    isTurnRunning = status == "running" || status == "pending"
                }
            } catch {
                // The durable receipt already proved the original request is in
                // progress. Keep following its SSE stream; a transient snapshot
                // failure must not turn it into a locally retryable send.
                errorDetail = errorText(error)
                isTurnRunning = true
                status = "running"
            }
        case "completed":
            do {
                try await refreshSnapshotAndHistory()
            } catch {
                errorDetail = errorText(error)
            }
            outbox.removeAll { $0.id == itemID }
            isTurnRunning = false
            stopStreaming()
            scheduleOutboxProcessing()
            bumpScrollAnchor()
        case "failed", "stopped", "interrupted":
            let failedItem = outbox.first(where: { $0.id == itemID })
            do {
                try await refreshSnapshotAndHistory()
            } catch {
                errorDetail = errorText(error)
            }
            let state = response.messageState ?? "unknown"
            let detail = "服务端确认消息 \(itemID) 已被处理，但最终状态为 \(state)。草稿和附件已恢复，可使用新的消息 ID 重试。"
            markOutboxFailed(itemID: itemID, detail: detail, requiresNewMessageID: true, fallback: failedItem)
            isTurnRunning = false
            stopStreaming()
        case let state?:
            markOutboxFailed(
                itemID: itemID,
                detail: "POST /api/v1/job/\(jobID)/message 返回未知 messageState：\(state)"
            )
        case nil:
            markOutboxFailed(
                itemID: itemID,
                detail: "POST /api/v1/job/\(jobID)/message 返回 duplicate，但缺少 messageState。"
            )
        }
    }

    private func setAwaitingEcho(itemID: String) {
        guard let index = outbox.firstIndex(where: { $0.id == itemID }) else { return }
        outbox[index].state = .awaitingEcho
        isTurnRunning = true
        status = "running"
        bumpScrollAnchor()
    }

    private func markOutboxFailed(
        itemID: String,
        detail: String,
        requiresNewMessageID: Bool = false,
        fallback: LocalOutboxItem? = nil
    ) {
        if let index = outbox.firstIndex(where: { $0.id == itemID }) {
            outbox[index].state = .failed(detail: detail, requiresNewMessageID: requiresNewMessageID)
            publishRestore(outbox[index].draft)
        } else if let fallback {
            // A history refresh may already have replaced the optimistic item
            // with its persisted user message. Restore the draft without adding
            // a second copy of that same message to the timeline.
            publishRestore(fallback.draft)
        } else {
            errorDetail = detail
            bumpScrollAnchor()
            return
        }
        removeEchoedOutboxItems()
        errorDetail = detail
        bumpScrollAnchor()
    }

    private func refreshSnapshotAndHistory() async throws {
        guard let client else { return }
        let snapshot = try await client.job(id: jobID)
        title = snapshot.title
        status = snapshot.status
        lastEventID = snapshot.lastEventSeq
        let interactiveSessions = snapshot.sessionIds ?? []
        let fallbackSession = interactiveSessions.last ?? snapshot.graphSessionIds?.last
        sessionID = preferredSessionID ?? fallbackSession
        if interactiveSessions.isEmpty && preferredSessionID == nil {
            agentType = snapshot.initialAgentId ?? agentType
            modelID = snapshot.firstModelId ?? modelID
            modeID = snapshot.initialAcpMode ?? modeID
            thoughtLevelID = snapshot.initialAcpThoughtLevel ?? thoughtLevelID
        }
        if preferredSessionID != nil, let sessionID {
            try await loadHistory(sessionID: sessionID)
        } else if !interactiveSessions.isEmpty {
            try await loadInteractiveHistory(sessionIDs: interactiveSessions)
        } else if let sessionID {
            try await loadHistory(sessionID: sessionID)
        }
    }

    private func apply(_ event: ServerEvent, id: UInt64?) {
        if let id { lastEventID = id }
        if let sessionId = event.sessionId, !sessionId.isEmpty {
            sessionID = sessionId
        }
        switch event.type {
        case "JOB_STARTED":
            status = "running"
            isTurnRunning = true
            phaseLabel = "Agent 已启动"
        case "RUN_STARTED":
            isTurnRunning = true
            phaseLabel = "Agent 已启动"
        case "RUN_FINISHED":
            phaseLabel = nil
            finishOpenMessages()
            // RUN_FINISHED closes the Agent round, but the backend publishes the
            // authoritative JOB_* terminal event only after it has persisted the
            // Job transition. Keep the local queue paused until that event arrives;
            // sending here races the backend's still-running gate and returns 409.
        case "JOB_COMPLETED":
            status = "completed"
            phaseLabel = nil
            isTurnRunning = false
            let outcome = event.runOutcome ?? "completed"
            publishTerminalEvent(outcome: outcome, finalStatus: "completed", occurredAt: event.timestamp)
            applyRunOutcome(outcome)
            finishOpenMessages()
            scheduleSnapshotRefresh()
            stopStreaming()
        case "JOB_FAILED":
            status = "failed"
            phaseLabel = nil
            isTurnRunning = false
            let outcome = event.runOutcome ?? "failed"
            publishTerminalEvent(outcome: outcome, finalStatus: "failed", occurredAt: event.timestamp)
            applyRunOutcome(outcome)
            if let message = event.message, !message.isEmpty { errorDetail = message }
            finishOpenMessages()
            scheduleSnapshotRefresh()
            stopStreaming()
        case "JOB_STOPPED":
            status = "stopped"
            phaseLabel = nil
            isTurnRunning = false
            let outcome = event.runOutcome ?? "stopped"
            publishTerminalEvent(outcome: outcome, finalStatus: "stopped", occurredAt: event.timestamp)
            applyRunOutcome(outcome)
            finishOpenMessages()
            scheduleSnapshotRefresh()
            stopStreaming()
        case "RUN_ERROR":
            errorDetail = [event.code, event.message].compactMap { $0 }.joined(separator: "\n")
        case "COMMAND_SYSTEM_MESSAGE":
            applyCommandEvent(event)
        case "TEXT_MESSAGE_START":
            guard let messageID = event.messageId else { return }
            let kind: ChatMessage.Kind = event.external?.isThinking == true ? .thought : .assistant
            upsert(id: messageID, kind: kind, content: "", detail: nil, finished: false, failed: false, timestamp: event.timestamp)
            removeEchoedOutboxItems()
        case "TEXT_MESSAGE_CONTENT":
            guard let messageID = event.messageId else { return }
            let kind: ChatMessage.Kind = event.external?.isThinking == true ? .thought : .assistant
            append(id: messageID, kind: kind, text: event.delta ?? "", timestamp: event.timestamp)
        case "TEXT_MESSAGE_END":
            finish(id: event.messageId)
        case "TOOL_CALL_START":
            guard let toolID = event.toolCallId else { return }
            upsert(id: toolID, kind: .tool, content: "", detail: event.toolCallName, finished: false, failed: false, timestamp: event.timestamp)
        case "TOOL_CALL_ARGS":
            guard let toolID = event.toolCallId else { return }
            appendDetail(id: toolID, text: event.delta ?? "", replace: event.replace == true)
        case "TOOL_CALL_RESULT":
            guard let toolID = event.toolCallId else { return }
            append(id: toolID, kind: .tool, text: event.delta ?? "", timestamp: event.timestamp)
            if let index = messages.firstIndex(where: { $0.id == toolID }) {
                messages[index].isFailed = event.toolCallStatus == "Error"
            }
        case "TOOL_CALL_END":
            finish(id: event.toolCallId)
        case "TOOL_CALL_STITCHED":
            guard let toolID = event.toolCallId else { return }
            if let index = messages.firstIndex(where: { $0.id == toolID }) {
                messages[index].content = event.delta ?? messages[index].content
                messages[index].isFinished = true
                messages[index].isFailed = event.toolCallStatus == "Error"
            }
        default:
            break
        }
    }

    private func applyCommandEvent(_ event: ServerEvent, fallbackClientMessageID: String? = nil) {
        guard let text = event.text, !text.isEmpty else { return }
        let messageID = event.clientMessageId ?? fallbackClientMessageID ?? UUID().uuidString
        upsert(
            id: messageID,
            kind: .system,
            content: text,
            detail: nil,
            finished: true,
            failed: false,
            timestamp: event.timestamp
        )
        // Commands are transient and never enter session history. Remove the
        // optimistic outbox copy as soon as either delivery path arrives; the
        // matching inline/SSE copy then upserts the same stable system message.
        if event.clientMessageId != nil || fallbackClientMessageID != nil {
            outbox.removeAll { $0.id == messageID }
        }
    }

    private func upsert(id: String, kind: ChatMessage.Kind, content: String, detail: String?, finished: Bool, failed: Bool, timestamp: Int64?) {
        if let index = messages.firstIndex(where: { $0.id == id }) {
            guard !messages[index].isFinished else { return }
            messages[index].kind = kind
            messages[index].detail = detail ?? messages[index].detail
            return
        }
        messages.append(ChatMessage(
            id: id,
            kind: kind,
            content: content,
            detail: detail,
            isFinished: finished,
            isFailed: failed,
            timestamp: timestamp
        ))
        bumpScrollAnchor()
    }

    private func append(id: String, kind: ChatMessage.Kind, text: String, timestamp: Int64?) {
        if let index = messages.firstIndex(where: { $0.id == id }) {
            guard !messages[index].isFinished else { return }
            messages[index].content += text
        } else {
            messages.append(ChatMessage(
                id: id,
                kind: kind,
                content: text,
                detail: nil,
                isFinished: false,
                isFailed: false,
                timestamp: timestamp
            ))
        }
        bumpScrollAnchor()
    }

    private func appendDetail(id: String, text: String, replace: Bool) {
        guard let index = messages.firstIndex(where: { $0.id == id }) else { return }
        messages[index].detail = replace ? text : (messages[index].detail ?? "") + text
    }

    private func finish(id: String?) {
        guard let id, let index = messages.firstIndex(where: { $0.id == id }) else { return }
        messages[index].isFinished = true
    }

    private func finishOpenMessages() {
        for index in messages.indices {
            messages[index].isFinished = true
        }
        bumpScrollAnchor()
    }

    private func scheduleSnapshotRefresh() {
        guard client != nil else { return }
        Task { [weak self] in
            guard let self else { return }
            do {
                try await self.refreshSnapshotAndHistory()
                self.reconcileAwaitingEchoIfNeeded()
                self.scheduleOutboxProcessing()
            } catch {
                self.errorDetail = self.errorText(error)
            }
        }
    }

    private func removeEchoedOutboxItems() {
        let serverIDs = Set(messages.map(\.id))
        outbox.removeAll { item in
            switch item.state {
            case .awaitingEcho, .failed:
                return serverIDs.contains(item.id)
            default:
                return false
            }
        }
    }

    private func reconcileAwaitingEchoIfNeeded() {
        let serverIDs = Set(messages.map(\.id))
        for index in outbox.indices {
            guard case .awaitingEcho = outbox[index].state else { continue }
            if serverIDs.contains(outbox[index].id) {
                continue
            }
            outbox[index].state = .failed(
                detail: "消息已提交，但当前会话历史未出现该条消息。草稿和附件已恢复，可重试。",
                requiresNewMessageID: true
            )
            publishRestore(outbox[index].draft)
        }
        removeEchoedOutboxItems()
    }

    private func publishRestore(_ draft: ComposerDraft) {
        restoreDraft = draft
        restoreDraftVersion &+= 1
    }

    private func publishTerminalEvent(outcome: String, finalStatus: String, occurredAt: Int64?) {
        latestTerminalEvent = ChatTerminalEvent(outcome: outcome, finalStatus: finalStatus, occurredAt: occurredAt)
        terminalEventVersion &+= 1
    }

    private func applyRunOutcome(_ outcome: String) {
        guard outcome != "completed" else { return }
        let detail = outcome == "stopped"
            ? "消息对应的运行已停止。草稿和附件已恢复，可按需重试。"
            : "消息对应的运行失败。草稿和附件已恢复，可按需重试。"
        guard let index = outbox.firstIndex(where: { $0.state == .awaitingEcho }) else { return }
        let failedItem = outbox[index]
        outbox[index].state = .failed(detail: detail, requiresNewMessageID: true)
        publishRestore(failedItem.draft)
        removeEchoedOutboxItems()
        bumpScrollAnchor()
    }

    private func bumpScrollAnchor() {
        scrollAnchor &+= 1
    }

    private func hasPriorConversation(_ detail: JobDetail) -> Bool {
        detail.sessionCount > 0 || !messages.isEmpty
    }

    private func errorText(_ error: Error) -> String {
        if let error = error as? APIError { return error.detail }
        return String(describing: error)
    }
}

