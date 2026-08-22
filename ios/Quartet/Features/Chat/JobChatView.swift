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
            if appModel.isRunningUITests {
                chat.startUITestPreview(route: route)
                return
            }
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
        .onChange(of: chat.terminalStateVersion) { _, _ in
            guard route.summary.mode != "graph" else { return }
            Task { await appModel.reloadJobs() }
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
        HStack(spacing: 11) {
            ZStack {
                Circle()
                    .fill(chat.statusColor.opacity(0.15))
                Image(systemName: chat.isRunning ? "waveform" : "bubble.left.and.bubble.right.fill")
                    .font(.system(size: 13, weight: .bold))
                    .foregroundStyle(chat.statusColor)
            }
            .frame(width: 34, height: 34)

            VStack(alignment: .leading, spacing: 3) {
                HStack(spacing: 5) {
                    Circle()
                        .fill(chat.statusColor)
                        .frame(width: 7, height: 7)
                    Text(chat.statusLabel)
                    if let phase = chat.phaseLabel {
                        Text("· \(phase)")
                            .lineLimit(1)
                    }
                }
                .font(.caption.weight(.semibold))
                .foregroundStyle(QuartetTheme.primaryText)

                if let session = chat.sessionIDDisplay {
                    Text(session)
                        .font(.system(size: 9, weight: .medium, design: .monospaced))
                        .foregroundStyle(QuartetTheme.secondaryText)
                        .lineLimit(1)
                }
            }

            Spacer(minLength: 8)

            HStack(spacing: 6) {
                if chat.streamStateLabel != "OFFLINE" {
                    Circle()
                        .fill(chat.streamStateColor)
                        .frame(width: 6, height: 6)
                }
                Text(chat.streamStateLabel)
                    .font(.system(size: 9, weight: .bold, design: .monospaced))
            }
            .foregroundStyle(chat.streamStateColor)
            .padding(.horizontal, 9)
            .frame(height: 26)
            .background(QuartetTheme.elevated, in: Capsule())
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 9)
        .background(.thinMaterial)
        .overlay(alignment: .bottom) {
            RunningPulseLine(active: chat.isRunning)
        }
    }

    private var messageList: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(spacing: 14) {
                    if chat.loading && chat.messages.isEmpty && chat.outbox.isEmpty {
                        VStack(spacing: 12) {
                            ProgressView()
                            Text("正在同步对话…")
                                .font(.caption)
                                .foregroundStyle(QuartetTheme.secondaryText)
                        }
                        .padding(.top, 80)
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
                .padding(.horizontal, 14)
                .padding(.vertical, 18)
            }
            .onChange(of: chat.scrollAnchor) { _, _ in
                withAnimation(.easeOut(duration: 0.2)) {
                    proxy.scrollTo("chat-bottom", anchor: .bottom)
                }
            }
        }
    }

    private var composer: some View {
        let hasPendingImage = pendingImage != nil
        return VStack(spacing: 10) {
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
                    Image(systemName: hasPendingImage ? "photo.fill" : "photo")
                        .font(.headline)
                        .foregroundStyle(hasPendingImage ? QuartetTheme.accent : QuartetTheme.secondaryText)
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
                    .accessibilityIdentifier("chat-composer")

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
                .accessibilityIdentifier("chat-send")
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
        Group {
            if isCenteredEvent {
                centeredEvent
            } else {
                HStack(alignment: .bottom, spacing: 8) {
                    if message.kind == .user { Spacer(minLength: 54) }

                    if message.kind != .user { avatar }

                    VStack(alignment: message.kind == .user ? .trailing : .leading, spacing: 5) {
                        messageLabel
                        bubbleContent
                    }

                    if message.kind == .user { avatar }
                    if message.kind != .user { Spacer(minLength: 32) }
                }
            }
        }
        .environment(\.openURL, OpenURLAction { url in
            openSafely(url)
        })
    }

    private var messageLabel: some View {
        HStack(spacing: 6) {
            Text(label)
            if let timestamp = message.timestamp {
                Text(timeLabel(timestamp))
                    .fontWeight(.regular)
            }
            if !message.isFinished {
                Text("正在输入")
                    .fontWeight(.semibold)
                TypingDots()
            }
        }
        .font(.system(size: 9, weight: .bold, design: .monospaced))
        .foregroundStyle(labelColor)
        .padding(.horizontal, 4)
    }

    private var bubbleContent: some View {
        VStack(alignment: .leading, spacing: 9) {
            if message.kind == .tool {
                if let detail = message.detail, !detail.isEmpty {
                    Label(detail, systemImage: "wrench.and.screwdriver.fill")
                        .font(.caption.weight(.semibold))
                        .foregroundStyle(labelColor)
                        .lineLimit(2)
                }
                Text(message.content.isEmpty ? "工具正在执行…" : message.content)
                    .font(.system(.footnote, design: .monospaced))
                    .foregroundStyle(QuartetTheme.primaryText)
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
            } else {
                MarkdownMessageView(text: message.content.isEmpty ? "正在组织回复…" : message.content)
            }

            ForEach(message.imagePaths, id: \.self) { path in
                AuthenticatedImage(path: path)
            }

            if message.kind != .tool, let detail = message.detail, !detail.isEmpty {
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
        .padding(.horizontal, 14)
        .padding(.vertical, 11)
        .frame(maxWidth: message.kind == .user ? 300 : .infinity, alignment: .leading)
        .background(background, in: bubbleShape)
        .overlay(bubbleShape.stroke(border, lineWidth: message.isFailed ? 1.5 : 0.75))
        .shadow(color: Color.black.opacity(0.045), radius: 10, y: 3)
    }

    private var avatar: some View {
        ZStack {
            Circle().fill(avatarBackground)
            Image(systemName: avatarIcon)
                .font(.system(size: 11, weight: .bold))
                .foregroundStyle(avatarForeground)
        }
        .frame(width: 30, height: 30)
        .overlay(Circle().stroke(QuartetTheme.divider.opacity(0.8), lineWidth: 0.75))
        .accessibilityHidden(true)
    }

    private var centeredEvent: some View {
        HStack(spacing: 8) {
            Image(systemName: message.isFailed ? "exclamationmark.triangle.fill" : "info.circle.fill")
            Text(message.content.isEmpty ? "系统事件" : message.content)
                .textSelection(.enabled)
        }
        .font(.caption)
        .foregroundStyle(message.isFailed ? QuartetTheme.failed : QuartetTheme.secondaryText)
        .padding(.horizontal, 12)
        .padding(.vertical, 8)
        .background(QuartetTheme.elevated.opacity(0.8), in: Capsule())
        .frame(maxWidth: .infinity)
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
        switch message.kind {
        case .user: QuartetTheme.accent.opacity(0.18)
        case .thought: QuartetTheme.running.opacity(0.09)
        case .tool: QuartetTheme.elevated
        default: QuartetTheme.surface
        }
    }

    private var border: Color {
        message.isFailed ? QuartetTheme.failed.opacity(0.6) : QuartetTheme.divider
    }

    private var bubbleShape: UnevenRoundedRectangle {
        UnevenRoundedRectangle(
            topLeadingRadius: message.kind == .user ? 20 : 7,
            bottomLeadingRadius: 20,
            bottomTrailingRadius: 20,
            topTrailingRadius: message.kind == .user ? 7 : 20,
            style: .continuous
        )
    }

    private var isCenteredEvent: Bool { message.kind == .system }

    private var avatarIcon: String {
        switch message.kind {
        case .user: "person.fill"
        case .assistant: "sparkles"
        case .thought: "brain.head.profile"
        case .tool: "wrench.and.screwdriver.fill"
        case .system: "info.circle.fill"
        }
    }

    private var avatarBackground: Color {
        message.kind == .user ? QuartetTheme.accent : QuartetTheme.elevated
    }

    private var avatarForeground: Color {
        message.kind == .user ? Color.black.opacity(0.75) : labelColor
    }

    private func timeLabel(_ timestamp: Int64) -> String {
        timestamp.quartetDate.formatted(date: .omitted, time: .shortened)
    }
}

private struct TypingDots: View {
    @State private var active = false

    var body: some View {
        HStack(spacing: 2) {
            ForEach(0..<3, id: \.self) { index in
                Circle()
                    .fill(QuartetTheme.accent)
                    .frame(width: 3, height: 3)
                    .offset(y: active ? -2 : 2)
                    .animation(
                        .easeInOut(duration: 0.55)
                            .repeatForever(autoreverses: true)
                            .delay(Double(index) * 0.12),
                        value: active
                    )
            }
        }
        .onAppear { active = true }
        .accessibilityHidden(true)
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
    @Published var terminalStateVersion = 0

    private var client: APIClient?
    private var jobID = ""
    private(set) var sessionID: String?
    private var preferredSessionID: String?
    private var modelID: String?
    private var agentType: String?
    private var modeID: String?
    private var thoughtLevelID: String?
    private var lastEventID: UInt64 = 0
    private var lastGraphEventID: UInt64 = 0
    private var streamTask: Task<Void, Never>?
    private var graphReconcileTask: Task<Void, Never>?
    private var graphMonitorTask: Task<Void, Never>?
    private var didSeedInitialDraft = false
    private var isTurnRunning = false
    private var isProcessingOutbox = false
    private var isGraph = false
    private var graphRunLive = false

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
    var statusColor: Color {
        switch status {
        case "running", "pending", "awaitingInput", "stepStopping": QuartetTheme.running
        case "completed": QuartetTheme.accent
        case "failed", "timedOut": QuartetTheme.failed
        default: QuartetTheme.stopped
        }
    }

    var statusLabel: String {
        switch status {
        case "pending": "等待中"
        case "running": "运行中"
        case "completed": "已完成"
        case "failed": "失败"
        case "timedOut": "已超时"
        case "stopped": "已停止"
        case "awaitingInput": "等待输入"
        case "stepStopping": "步骤后停止中"
        case "stepStopped": "已在步骤后停止"
        default: status
        }
    }

    func start(route: ChatRoute, client: APIClient) async {
        stopStreaming()
        let changesJob = !jobID.isEmpty && jobID != route.summary.id
        if changesJob {
            messages = []
            outbox = []
            sessionID = nil
        }
        self.client = client
        jobID = route.summary.id
        isGraph = route.summary.mode == "graph"
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
            lastGraphEventID = 0
            var graphDefaultSessionID: String?
            if isGraph {
                let graphSnapshot = try await client.graphRunStatus(jobID: jobID)
                let graphStatus = graphSnapshot.run?.status
                graphRunLive = graphStatus.map(isLiveGraphStatus) ?? false
                graphDefaultSessionID = latestGraphSessionID(in: graphSnapshot)
                if detail.status != "running", let graphStatus {
                    status = graphStatus
                }
            } else {
                graphRunLive = false
            }
            isTurnRunning = graphRunLive
                || detail.status == "running"
                || (detail.status == "pending" && hasPriorConversation(detail))

            let interactiveSessions = detail.sessionIds ?? []
            let fallbackSession = interactiveSessions.last ?? detail.graphSessionIds?.last
            let requestedSession = preferredSessionID?.trimmingCharacters(in: .whitespacesAndNewlines)
            sessionID = (requestedSession?.isEmpty == false ? requestedSession : nil) ?? graphDefaultSessionID ?? fallbackSession
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

    func startUITestPreview(route: ChatRoute) {
        stopStreaming()
        jobID = route.summary.id
        isGraph = route.summary.mode == "graph"
        graphRunLive = isGraph && ["pending", "running", "stepStopping"].contains(route.summary.status)
        title = route.summary.displayTitle
        status = route.summary.status
        sessionID = "session-preview"
        modelID = route.modelID ?? route.summary.modelId
        agentType = route.agentType ?? route.summary.agentId
        modeID = route.modeID ?? route.summary.acpMode
        thoughtLevelID = route.thoughtLevelID ?? route.summary.acpThoughtLevel
        var previewMessages = [
            ChatMessage(
                id: "preview-user", kind: .user,
                content: "请检查 iOS 端的核心交互和状态展示。", detail: nil,
                isFinished: true, isFailed: false, timestamp: nil
            ),
            ChatMessage(
                id: "preview-assistant", kind: .assistant,
                content: "已完成第一轮检查。运行状态和操作反馈都已同步。", detail: nil,
                isFinished: true, isFailed: false, timestamp: nil
            )
        ]
        if let initialMessage = route.initialMessage, !initialMessage.isEmpty {
            previewMessages.insert(
                ChatMessage(
                    id: "preview-initial", kind: .user, content: initialMessage, detail: nil,
                    isFinished: true, isFailed: false, timestamp: nil
                ),
                at: 0
            )
        }
        messages = previewMessages
        isTurnRunning = route.summary.status == "running"
        phaseLabel = isTurnRunning ? "正在处理" : nil
        loading = false
        bumpScrollAnchor()
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
        graphReconcileTask?.cancel()
        graphReconcileTask = nil
        graphMonitorTask?.cancel()
        graphMonitorTask = nil
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

    private func loadHistory(sessionID: String, preservesLiveMessages: Bool = true) async throws {
        guard let client else { return }
        let response = try await client.sessionMessages(id: sessionID)
        modelID = response.modelId
        agentType = response.type
        modeID = response.acpMode
        thoughtLevelID = response.acpThoughtLevel
        let historyMessages = response.messages.map { ChatMessage(history: $0) }
        if isGraph && graphRunLive && preservesLiveMessages {
            mergeGraphHistory(historyMessages)
        } else {
            messages = historyMessages
        }
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
        if isGraph && graphRunLive {
            startGraphMonitor()
        } else {
            graphMonitorTask?.cancel()
            graphMonitorTask = nil
        }
        streamState = .connecting
        streamTask = Task { @MainActor [weak self] in
            var attempts = 0
            while !Task.isCancelled {
                guard let self else { return }
                let resumeID = self.lastEventID
                do {
                    if self.isGraph && self.graphRunLive {
                        try await client.streamGraphEvents(
                            jobID: jobID,
                            lastEventID: self.lastGraphEventID,
                            onOpen: {
                                self.streamState = .live
                                self.errorDetail = nil
                            },
                            onEvent: { event, id in
                                self.applyGraph(event, id: id)
                            }
                        )
                    } else {
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
                    }
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
                        self.lastGraphEventID = 0
                        try await self.refreshSnapshotAndHistory()
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
        if isGraph {
            let graphSnapshot = try await client.graphRunStatus(jobID: jobID)
            let graphStatus = graphSnapshot.run?.status
            graphRunLive = graphStatus.map(isLiveGraphStatus) ?? false
            if snapshot.status != "running", let graphStatus {
                status = graphStatus
            }
        }
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
        isTurnRunning = graphRunLive
            || snapshot.status == "running"
            || (snapshot.status == "pending" && hasPriorConversation(snapshot))
    }

    private func applyGraph(_ event: GraphStreamEvent, id: UInt64?) {
        if let id { lastGraphEventID = id }

        let payload = event.payload ?? [:]
        let eventSessionID = payload["sessionId"]
        let belongsToVisibleSession = eventSessionID == nil || sessionID == nil || eventSessionID == sessionID

        switch event.type {
        case "agentMessageStart":
            guard belongsToVisibleSession, let messageID = payload["messageId"], !messageID.isEmpty else { return }
            phaseLabel = "Agent 正在回复"
            upsert(
                id: messageID, kind: .assistant, content: "", detail: nil,
                finished: false, failed: false, timestamp: event.createdAt
            )
        case "agentMessageDelta":
            guard belongsToVisibleSession, let messageID = payload["messageId"], !messageID.isEmpty else { return }
            phaseLabel = "Agent 正在回复"
            append(
                id: messageID, kind: .assistant,
                text: payload["delta"] ?? event.message ?? "", timestamp: event.createdAt
            )
        case "agentMessageEnd":
            guard belongsToVisibleSession else { return }
            finish(id: payload["messageId"])
            phaseLabel = nil
        case "agentThoughtStart":
            guard belongsToVisibleSession, let messageID = payload["messageId"], !messageID.isEmpty else { return }
            phaseLabel = "正在思考"
            upsert(
                id: messageID, kind: .thought, content: "", detail: nil,
                finished: false, failed: false, timestamp: event.createdAt
            )
        case "agentThoughtDelta":
            guard belongsToVisibleSession, let messageID = payload["messageId"], !messageID.isEmpty else { return }
            phaseLabel = "正在思考"
            append(
                id: messageID, kind: .thought,
                text: payload["delta"] ?? event.message ?? "", timestamp: event.createdAt
            )
        case "agentThoughtEnd":
            guard belongsToVisibleSession else { return }
            finish(id: payload["messageId"])
            phaseLabel = nil
        case "agentToolStart":
            guard belongsToVisibleSession, let toolID = payload["toolCallId"], !toolID.isEmpty else { return }
            phaseLabel = "正在调用工具"
            upsert(
                id: toolID, kind: .tool, content: "", detail: payload["toolName"],
                finished: false, failed: false, timestamp: event.createdAt
            )
        case "agentToolArgs":
            guard belongsToVisibleSession, let toolID = payload["toolCallId"], !toolID.isEmpty else { return }
            appendDetail(id: toolID, text: payload["delta"] ?? "", replace: payload["replace"] == "true")
        case "agentToolResult":
            guard belongsToVisibleSession, let toolID = payload["toolCallId"], !toolID.isEmpty else { return }
            if payload["stitched"] == "true", let index = messages.firstIndex(where: { $0.id == toolID }) {
                messages[index].content = payload["delta"] ?? event.message ?? messages[index].content
                messages[index].isFinished = true
                messages[index].isFailed = payload["status"] == "Error"
                bumpScrollAnchor()
            } else {
                append(
                    id: toolID, kind: .tool,
                    text: payload["delta"] ?? event.message ?? "", timestamp: event.createdAt
                )
                if let index = messages.firstIndex(where: { $0.id == toolID }) {
                    messages[index].isFailed = payload["status"] == "Error"
                }
            }
        case "agentToolEnd":
            guard belongsToVisibleSession else { return }
            if let toolID = payload["toolCallId"], let index = messages.firstIndex(where: { $0.id == toolID }) {
                messages[index].isFailed = payload["status"] == "Error"
                if let reason = payload["placeholderReason"], !reason.isEmpty {
                    messages[index].detail = [messages[index].detail, reason].compactMap { $0 }.joined(separator: "\n")
                }
            }
            finish(id: payload["toolCallId"])
            phaseLabel = nil
        case "error":
            let detail = event.error?.fullDetail
            errorDetail = detail?.isEmpty == false ? detail : (event.message ?? "Graph run stream reported an error")
            scheduleGraphReconcile(immediate: true)
        case "instanceStarted", "instanceCompleted", "instanceFailed", "instanceSkipped",
             "edgeResolved", "loopIteration", "progressUpdated":
            scheduleGraphReconcile(immediate: event.type == "progressUpdated" || event.message == "session opened")
        default:
            break
        }
    }

    private func scheduleGraphReconcile(immediate: Bool) {
        guard graphReconcileTask == nil, client != nil else { return }
        graphReconcileTask = Task { @MainActor [weak self] in
            guard let self else { return }
            defer { self.graphReconcileTask = nil }
            if !immediate {
                do {
                    try await Task.sleep(for: .milliseconds(400))
                } catch {
                    return
                }
            }
            let wasLive = self.graphRunLive
            do {
                try await self.refreshGraphRunState()
            } catch {
                self.errorDetail = self.errorText(error)
                return
            }
            guard wasLive, !self.graphRunLive else { return }
            if let sessionID = self.sessionID {
                do {
                    try await self.loadHistory(sessionID: sessionID)
                } catch {
                    self.errorDetail = self.errorText(error)
                }
            }
            self.finishOpenMessages()
            self.phaseLabel = nil
            self.streamTask?.cancel()
            self.streamTask = nil
            self.graphMonitorTask?.cancel()
            self.graphMonitorTask = nil
            self.streamState = .offline
            self.startStreaming()
        }
    }

    private func refreshGraphRunState() async throws {
        guard let client else { return }
        let snapshot = try await client.graphRunStatus(jobID: jobID)
        guard let run = snapshot.run else { return }
        status = run.status
        graphRunLive = isLiveGraphStatus(run.status)
        isTurnRunning = graphRunLive
        if preferredSessionID == nil, let latestSession = latestGraphSessionID(in: snapshot), latestSession != sessionID {
            sessionID = latestSession
            try await loadHistory(sessionID: latestSession, preservesLiveMessages: false)
        }
        if let error = run.lastError?.fullDetail, !error.isEmpty {
            errorDetail = error
        } else if let error = snapshot.progress?.lastError, !error.isEmpty {
            errorDetail = error
        } else {
            errorDetail = nil
        }
    }

    private func startGraphMonitor() {
        guard graphMonitorTask == nil, client != nil else { return }
        graphMonitorTask = Task { @MainActor [weak self] in
            while !Task.isCancelled {
                do {
                    try await Task.sleep(for: .seconds(2))
                } catch {
                    return
                }
                guard let self, self.graphRunLive else { return }
                self.scheduleGraphReconcile(immediate: true)
            }
        }
    }

    private func mergeGraphHistory(_ persisted: [ChatMessage]) {
        let persistedIDs = Set(persisted.map(\.id))
        let inFlight = messages.filter { !persistedIDs.contains($0.id) }
        messages = persisted + inFlight
    }

    private func latestGraphSessionID(in snapshot: GraphRunStatusResponse) -> String? {
        snapshot.instances?
            .compactMap { instance -> (String, Int64)? in
                guard let candidate = instance.displaySessionId ?? instance.sessionId, !candidate.isEmpty else { return nil }
                return (candidate, instance.startedAt ?? 0)
            }
            .max(by: { $0.1 < $1.1 })?
            .0
    }

    private func isLiveGraphStatus(_ status: String) -> Bool {
        ["pending", "running", "stepStopping"].contains(status)
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
            publishTerminalStateChange()
            applyRunOutcome(outcome)
            finishOpenMessages()
            scheduleSnapshotRefresh()
            stopStreaming()
        case "JOB_FAILED":
            status = "failed"
            phaseLabel = nil
            isTurnRunning = false
            let outcome = event.runOutcome ?? "failed"
            publishTerminalStateChange()
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
            publishTerminalStateChange()
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

    private func publishTerminalStateChange() {
        terminalStateVersion &+= 1
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

