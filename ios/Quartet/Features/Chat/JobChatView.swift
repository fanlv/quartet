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
            messageList
            composer
        }
        .background(QuartetTheme.canvas)
        .navigationTitle(chat.title.isEmpty ? route.summary.displayTitle : chat.title)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar(.hidden, for: .tabBar)
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
        .alert("停止当前执行？", isPresented: $confirmsStop) {
            Button("取消", role: .cancel) {}
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
        } message: {
            Text("正在执行的 Agent 将收到停止请求。")
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

    private var messageList: some View {
        ScrollViewReader { proxy in
            ScrollView {
                LazyVStack(spacing: 14) {
                    if chat.loading && chat.messages.isEmpty && chat.outbox.isEmpty {
                        VStack(spacing: 12) {
                            ProgressView()
                            Text("正在同步对话…")
                                .font(.quartet(.detail))
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
                    .font(.quartet(.detail))
                    .foregroundStyle(QuartetTheme.failed)
                }
            }

            if !chat.composerOutboxItems.isEmpty {
                VStack(spacing: 8) {
                    ForEach(chat.composerOutboxItems) { item in
                        OutboxRow(
                            item: item,
                            onCancel: { chat.cancelOutboxItem(id: item.id) },
                            onRetry: { chat.retryOutboxItem(id: item.id) },
                            onRestore: { chat.restoreOutboxItem(id: item.id) }
                        )
                    }
                }
            }

            VStack(spacing: 0) {
                if let pendingImage {
                    ChatAttachmentPreview(upload: pendingImage)
                        .padding(.horizontal, 12)
                        .padding(.top, 12)
                        .overlay(alignment: .topTrailing) {
                            Button {
                                self.pendingImage = nil
                                selectedPhoto = nil
                            } label: {
                                Image(systemName: "xmark")
                                    .font(.quartet(.compact, weight: .bold))
                                    .foregroundStyle(QuartetTheme.primaryText)
                                    .frame(width: 28, height: 28)
                                    .background(.thinMaterial, in: Circle())
                            }
                            .buttonStyle(.plain)
                            .accessibilityLabel("移除图片")
                            .padding(18)
                        }
                }

                TextField("继续对话…", text: $draft, axis: .vertical)
                    .font(.quartet(.regular))
                    .lineLimit(1...6)
                    .padding(.horizontal, 15)
                    .padding(.vertical, 14)
                    .frame(minHeight: 54, alignment: .topLeading)
                    .accessibilityIdentifier("chat-composer")

                Divider()
                    .overlay(QuartetTheme.divider.opacity(0.7))

                HStack(alignment: .center, spacing: 7) {
                    composerContext

                    PhotosPicker(selection: $selectedPhoto, matching: .images) {
                        Image(systemName: hasPendingImage ? "photo.fill" : "photo")
                            .font(.quartet(.control, weight: .semibold))
                            .foregroundStyle(hasPendingImage ? QuartetTheme.accent : QuartetTheme.secondaryText)
                            .frame(width: 36, height: 36)
                            .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                    }
                    .accessibilityLabel("从相册选择图片")

                    Button {
                        showsAttachmentMenu = true
                    } label: {
                        Image(systemName: "plus")
                            .font(.quartet(.control, weight: .bold))
                            .foregroundStyle(QuartetTheme.secondaryText)
                            .frame(width: 36, height: 36)
                            .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("更多图片来源")

                    Button { enqueueDraft() } label: {
                        Image(systemName: "arrow.up")
                            .font(.quartet(.control, weight: .bold))
                            .foregroundStyle(sendDisabled ? QuartetTheme.secondaryText : Color.black)
                            .frame(width: 38, height: 38)
                            .background(sendDisabled ? QuartetTheme.elevated : QuartetTheme.accent, in: Circle())
                    }
                    .buttonStyle(.plain)
                    .disabled(sendDisabled)
                    .opacity(chat.sending ? 0.55 : 1)
                    .accessibilityLabel("发送消息")
                    .accessibilityIdentifier("chat-send")
                }
                .padding(.horizontal, 10)
                .padding(.vertical, 9)
            }
            .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 20, style: .continuous))
            .overlay(
                RoundedRectangle(cornerRadius: 20, style: .continuous)
                    .stroke(QuartetTheme.divider, lineWidth: 1)
            )
            .shadow(color: Color.black.opacity(0.05), radius: 16, y: 7)

            if chat.isRunning {
                Text("当前轮次运行中，新消息会先进入本地队列，等本轮结束后自动按顺序发送。")
                    .font(.quartet(.detail))
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .frame(maxWidth: .infinity, alignment: .leading)
            } else if chat.hasQueuedMessages {
                Text("队列中的消息会依次发送，可在发送前取消。")
                    .font(.quartet(.detail))
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .padding(.horizontal, 12)
        .padding(.top, 10)
        .padding(.bottom, 8)
        .background(.thinMaterial)
    }

    private var composerContext: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 6) {
                ComposerMetadataChip(
                    icon: "sparkles",
                    text: chat.agentDisplayLabel,
                    accessibilityLabel: "Agent，\(chat.agentDisplayLabel)"
                )
                ComposerMetadataChip(
                    icon: "cpu",
                    text: chat.modelDisplayLabel,
                    accessibilityLabel: "Model ID，\(chat.modelDisplayLabel)"
                )
                ComposerMetadataChip(
                    icon: "slider.horizontal.3",
                    text: chat.modeDisplayLabel,
                    accessibilityLabel: "Mode，\(chat.modeDisplayLabel)"
                )
                if let thoughtLevel = chat.thoughtLevelDisplayLabel {
                    ComposerMetadataChip(
                        icon: "brain.head.profile",
                        text: thoughtLevel,
                        accessibilityLabel: "Thought level，\(thoughtLevel)"
                    )
                }
                ComposerMetadataChip(
                    icon: "text.word.spacing",
                    text: chat.tokenCountLabel,
                    accessibilityLabel: chat.tokenCountAccessibilityLabel
                )
                if chat.showsDuration {
                    TimelineView(.periodic(from: .now, by: 1)) { timeline in
                        ComposerMetadataChip(
                            icon: "clock",
                            text: chat.durationLabel(at: timeline.date),
                            accessibilityLabel: "耗时，\(chat.durationLabel(at: timeline.date))"
                        )
                    }
                }
                if let workspace = appModel.workspaces.first(where: { $0.id == route.summary.workspaceId }) {
                    ComposerMetadataChip(
                        icon: "folder",
                        text: workspace.displayName,
                        accessibilityLabel: "工作空间，\(workspace.displayName)"
                    )
                }
            }
            .padding(.vertical, 1)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private var sendDisabled: Bool {
        chat.loading || (draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && pendingImage == nil)
    }

    private func enqueueDraft() {
        let text = draft.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty || pendingImage != nil else { return }
        do {
            try appModel.recordSentMessage(text, workspaceID: route.summary.workspaceId)
        } catch {
            appModel.present(error)
        }
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
                        appModel.present(APIError(summary: "没有相机权限", detail: "请在系统设置中允许 Sophia 访问相机后重试。"))
                    }
                }
            }
        case .denied, .restricted:
            appModel.present(APIError(summary: "没有相机权限", detail: "请在系统设置中允许 Sophia 访问相机后重试。"))
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

private struct ComposerMetadataChip: View {
    let icon: String
    let text: String
    let accessibilityLabel: String

    var body: some View {
        HStack(spacing: 5) {
            Image(systemName: icon)
                .font(.quartet(.compact, weight: .semibold))
            Text(text)
                .font(.quartet(.compact, weight: .medium))
                .lineLimit(1)
        }
        .foregroundStyle(QuartetTheme.secondaryText)
        .padding(.horizontal, 9)
        .frame(height: 30)
        .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 8, style: .continuous))
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(accessibilityLabel)
    }
}

private struct ChatBubble: View {
    @EnvironmentObject private var appModel: AppModel
    let message: ChatMessage

    var body: some View {
        Group {
            switch message.kind {
            case .user:
                UserMessageBubble(message: message)
            case .assistant:
                AssistantMessageCard(message: message)
            case .thought:
                ThoughtMessageCard(message: message)
            case .tool:
                ToolCallCard(message: message)
            case .system:
                centeredEvent
            }
        }
        .environment(\.openURL, OpenURLAction { url in
            openSafely(url)
        })
    }

    private var centeredEvent: some View {
        HStack(spacing: 8) {
            Image(systemName: message.isFailed ? "exclamationmark.triangle.fill" : "info.circle.fill")
            Text(message.content.isEmpty ? "系统事件" : message.content)
                .textSelection(.enabled)
        }
        .font(.quartet(.detail))
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

}

private struct UserMessageBubble: View {
    let message: ChatMessage

    var body: some View {
        HStack(alignment: .bottom, spacing: 7) {
            Spacer(minLength: 38)
            if let timestamp = message.timestamp {
                Text(chatTimeLabel(timestamp))
                    .font(.quartet(.compact))
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .padding(.bottom, 4)
            }
            VStack(alignment: .leading, spacing: 9) {
                ForEach(message.imagePaths, id: \.self) { path in
                    AuthenticatedImage(path: path)
                }
                if !message.content.isEmpty {
                    MarkdownMessageView(text: message.content, tone: .user)
                }
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 12)
            .frame(maxWidth: 320, alignment: .leading)
            .background(Color(red: 0.10, green: 0.10, blue: 0.10), in: UnevenRoundedRectangle(
                topLeadingRadius: 17, bottomLeadingRadius: 17, bottomTrailingRadius: 5, topTrailingRadius: 17, style: .continuous
            ))
        }
        .frame(maxWidth: .infinity, alignment: .trailing)
        .accessibilityElement(children: .combine)
    }
}

private struct AssistantMessageCard: View {
    let message: ChatMessage

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            if let thought = message.thinkingContent, !thought.isEmpty {
                ThoughtPanel(text: thought, isStreaming: false, timestamp: message.timestamp)
            }
            if !message.content.isEmpty || !message.isFinished {
                VStack(alignment: .leading, spacing: 12) {
                    HStack(spacing: 8) {
                        Image(systemName: message.isShellOutput ? "terminal.fill" : "sparkles")
                            .font(.quartet(.detail, weight: .semibold))
                        Text(message.isShellOutput ? "SHELL" : "ASSISTANT")
                            .font(.quartet(.detail, weight: .semibold))
                        if !message.isFinished {
                            StreamingDot(color: QuartetTheme.accent)
                        }
                        Spacer(minLength: 8)
                        if let timestamp = message.timestamp, message.isFinished {
                            Text(chatTimeLabel(timestamp))
                                .font(.quartet(.compact))
                                .foregroundStyle(QuartetTheme.secondaryText)
                        }
                        if message.isFinished, !message.content.isEmpty {
                            CopyIconButton(text: message.content)
                        }
                    }
                    .foregroundStyle(QuartetTheme.accent)

                    Divider().overlay(QuartetTheme.divider.opacity(0.7))

                    if message.isShellOutput {
                        ScrollView(.horizontal, showsIndicators: false) {
                            Text(message.content)
                                .font(.quartet(.detail, design: .monospaced))
                                .foregroundStyle(QuartetTheme.primaryText)
                                .textSelection(.enabled)
                        }
                    } else if message.content.isEmpty {
                        HStack(spacing: 8) {
                            TypingDots()
                            Text("正在组织回复…")
                                .font(.quartet(.control))
                                .foregroundStyle(QuartetTheme.secondaryText)
                        }
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

private struct ThoughtMessageCard: View {
    let message: ChatMessage

    var body: some View {
        ThoughtPanel(
            text: message.content.isEmpty ? "正在思考…" : message.content,
            isStreaming: !message.isFinished,
            timestamp: message.timestamp
        )
    }
}

private struct ThoughtPanel: View {
    let text: String
    let isStreaming: Bool
    let timestamp: Int64?

    var body: some View {
        VStack(alignment: .leading, spacing: 9) {
            HStack(spacing: 8) {
                Image(systemName: "brain.head.profile")
                    .font(.quartet(.detail, weight: .semibold))
                Text("深度思考")
                    .font(.quartet(.detail, weight: .semibold))
                if isStreaming { StreamingDot(color: .blue) }
                Spacer(minLength: 8)
                if let timestamp, !isStreaming {
                    Text(chatTimeLabel(timestamp))
                        .font(.quartet(.compact))
                        .foregroundStyle(QuartetTheme.secondaryText)
                }
            }
            .foregroundStyle(Color.blue)

            MarkdownMessageView(text: text, tone: .thought)
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 13)
        .background(Color.blue.opacity(0.075), in: RoundedRectangle(cornerRadius: 13, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 13, style: .continuous).stroke(Color.blue.opacity(0.20), lineWidth: 1))
        .frame(maxWidth: .infinity, alignment: .leading)
    }
}

private struct ToolCallCard: View {
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
                    Image(systemName: toolIcon)
                        .font(.quartet(.control, weight: .semibold))
                        .foregroundStyle(QuartetTheme.secondaryText)
                        .frame(width: 20)
                    Text(displayName)
                        .font(.quartet(.control, weight: .medium))
                        .foregroundStyle(QuartetTheme.primaryText)
                        .lineLimit(1)
                    Spacer(minLength: 6)
                    toolStatusBadge
                    Image(systemName: isExpanded ? "chevron.up" : "chevron.down")
                        .font(.quartet(.detail, weight: .semibold))
                        .foregroundStyle(QuartetTheme.secondaryText)
                }
                .padding(.horizontal, 15)
                .padding(.vertical, 13)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityLabel("工具 \(displayName)，\(statusLabel)")
            .accessibilityHint(isExpanded ? "轻点收起详情" : "轻点展开参数和结果")

            if status == .processing {
                RunningPulseLine(active: true)
            } else if isExpanded {
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
                                .font(.quartet(.detail))
                                .foregroundStyle(QuartetTheme.secondaryText)
                        }
                    }
                    if status == .placeholder, let reason = message.placeholderReason, !reason.isEmpty {
                        Label("未完成：\(reason)", systemImage: "minus.circle")
                            .font(.quartet(.detail))
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

    private var toolIcon: String {
        let name = displayName.lowercased()
        if name.contains("read") || name.contains("file") { return "doc.text.magnifyingglass" }
        if name.contains("write") || name.contains("edit") || name.contains("patch") { return "square.and.pencil" }
        if name.contains("search") || name.contains("grep") { return "magnifyingglass" }
        if name.contains("terminal") || name.contains("exec") || name.contains("command") { return "terminal" }
        if name.contains("web") || name.contains("browser") { return "globe" }
        return "wrench.and.screwdriver"
    }

    @ViewBuilder private var toolStatusBadge: some View {
        ZStack {
            Circle().fill(statusColor.opacity(0.11))
            if status == .processing {
                ProgressView().controlSize(.mini).tint(statusColor)
            } else {
                Image(systemName: statusIcon)
                    .font(.quartet(.compact, weight: .bold))
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
        case .processing: "运行中"
        case .success: "已完成"
        case .error: "执行失败"
        case .placeholder: "未完成"
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

private struct ToolPayloadSection: View {
    let title: String
    let text: String

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 7) {
                Text(title)
                    .font(.quartet(.compact, weight: .bold, design: .monospaced))
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .tracking(0.5)
                Spacer()
                CopyIconButton(text: text)
            }
            if prettyPrintedJSON(text) == nil {
                MarkdownMessageView(text: text, tone: .tool)
                    .padding(12)
                    .background(QuartetTheme.canvas, in: RoundedRectangle(cornerRadius: 7, style: .continuous))
                    .overlay(RoundedRectangle(cornerRadius: 7, style: .continuous).stroke(QuartetTheme.divider.opacity(0.8)))
            } else {
                ScrollView(.horizontal, showsIndicators: false) {
                    Text(prettyPrintedJSON(text) ?? text)
                        .font(.quartet(.detail, design: .monospaced))
                        .foregroundStyle(QuartetTheme.primaryText)
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                .padding(12)
                .background(QuartetTheme.canvas, in: RoundedRectangle(cornerRadius: 7, style: .continuous))
                .overlay(RoundedRectangle(cornerRadius: 7, style: .continuous).stroke(QuartetTheme.divider.opacity(0.8)))
            }
        }
    }
}

private struct CopyIconButton: View {
    let text: String
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
                .font(.quartet(.detail, weight: .semibold))
                .foregroundStyle(copied ? QuartetTheme.accent : QuartetTheme.secondaryText)
                .frame(width: 26, height: 26)
                .background(QuartetTheme.elevated.opacity(0.75), in: RoundedRectangle(cornerRadius: 6))
        }
        .buttonStyle(.plain)
        .accessibilityLabel(copied ? "已复制" : "复制内容")
    }
}

private struct StreamingDot: View {
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

private func chatTimeLabel(_ timestamp: Int64) -> String {
    timestamp.quartetDate.formatted(date: .omitted, time: .shortened)
}

private func prettyPrintedJSON(_ text: String) -> String? {
    guard let data = text.data(using: .utf8),
          let object = try? JSONSerialization.jsonObject(with: data),
          JSONSerialization.isValidJSONObject(object),
          let formatted = try? JSONSerialization.data(withJSONObject: object, options: [.prettyPrinted, .sortedKeys]) else {
        return nil
    }
    return String(data: formatted, encoding: .utf8)
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
            .font(.quartet(.compact, weight: .bold, design: .monospaced))
            .foregroundStyle(item.isFailed ? QuartetTheme.failed : QuartetTheme.secondaryText)

            MarkdownMessageView(text: item.displayText, tone: .user)

            if let attachment = item.attachment {
                ChatAttachmentPreview(upload: attachment)
            }

            if let detail = item.failureDetail {
                Text(detail)
                    .font(.quartet(.detail, design: .monospaced))
                    .foregroundStyle(QuartetTheme.failed)
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .padding(14)
        .frame(maxWidth: 310, alignment: .leading)
        .background(Color(red: 0.10, green: 0.10, blue: 0.10), in: RoundedRectangle(cornerRadius: 16))
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
                    .font(.quartet(.detail, weight: .semibold))
                    .foregroundStyle(item.isFailed ? QuartetTheme.failed : QuartetTheme.primaryText)
                    .lineLimit(1)
                Spacer()
                Text(item.statusTitle)
                    .font(.quartet(.compact, weight: .bold, design: .monospaced))
                    .foregroundStyle(item.isFailed ? QuartetTheme.failed : QuartetTheme.secondaryText)
            }

            if let detail = item.failureDetail {
                Text(detail)
                    .font(.quartet(.compact, design: .monospaced))
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
            .font(.quartet(.detail))
        }
        .padding(12)
        .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 14))
        .overlay(RoundedRectangle(cornerRadius: 14).stroke(item.isFailed ? QuartetTheme.failed.opacity(0.4) : QuartetTheme.divider, lineWidth: 1))
    }
}

private struct MarkdownMessageView: View {
    let text: String
    var tone: MarkdownTone = .standard

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            ForEach(MarkdownRenderer.blocks(from: text)) { block in
                switch block.kind {
                case .markdown(let content):
                    MarkdownTextBlock(text: content, tone: tone)
                case .code(let language, let content):
                    CodeBlockView(language: language, code: content, tone: tone)
                case .table(let headers, let rows):
                    MarkdownTableView(headers: headers, rows: rows, tone: tone)
                case .heading(let level, let content):
                    MarkdownTextBlock(text: content, tone: tone)
                        .font(headingFont(level))
                        .fontWeight(.bold)
                case .quote(let content):
                    HStack(alignment: .top, spacing: 10) {
                        Capsule()
                            .fill(tone == .user ? Color.white.opacity(0.45) : QuartetTheme.secondaryText.opacity(0.5))
                            .frame(width: 3)
                        MarkdownTextBlock(text: content, tone: tone)
                    }
                    .padding(.vertical, 4)
                    .padding(.horizontal, 10)
                    .background(tone.codeBackground.opacity(0.7), in: RoundedRectangle(cornerRadius: 7))
                case .list(let ordered, let items):
                    VStack(alignment: .leading, spacing: 7) {
                        ForEach(Array(items.enumerated()), id: \.offset) { index, item in
                            HStack(alignment: .firstTextBaseline, spacing: 8) {
                                Text(ordered ? "\(index + 1)." : "•")
                                    .font(.quartet(tone.contentFontSize, weight: .semibold))
                                    .foregroundStyle(tone.secondaryForeground)
                                    .frame(minWidth: ordered ? 20 : 10, alignment: .trailing)
                                MarkdownTextBlock(text: item, tone: tone)
                            }
                        }
                    }
                case .divider:
                    Divider().overlay(tone.codeBorder)
                }
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
    }

    private func headingFont(_ level: Int) -> Font {
        switch level {
        case 1, 2: .quartet(.large)
        case 3: .quartet(.regular)
        default: .quartet(.control)
        }
    }
}

private struct MarkdownTextBlock: View {
    let text: String
    let tone: MarkdownTone

    var body: some View {
        if let attributed = MarkdownRenderer.attributedString(from: text) {
            Text(attributed)
                .font(.quartet(tone.contentFontSize))
                .foregroundStyle(tone.foreground)
                .lineSpacing(4)
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
        } else {
            Text(text)
                .font(.quartet(tone.contentFontSize))
                .foregroundStyle(tone.foreground)
                .lineSpacing(4)
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
    }
}

private struct CodeBlockView: View {
    let language: String?
    let code: String
    let tone: MarkdownTone

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack {
                Text((language?.isEmpty == false ? language! : "code").uppercased())
                    .font(.quartet(.compact, weight: .bold, design: .monospaced))
                    .foregroundStyle(tone.secondaryForeground)
                Spacer()
                Button("复制代码") {
                    UIPasteboard.general.string = code
                }
                .font(.quartet(.detail))
            }

            ScrollView(.horizontal, showsIndicators: false) {
                Text(code)
                    .font(.quartet(.detail, design: .monospaced))
                    .foregroundStyle(tone.foreground)
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .padding(12)
        .background(tone.codeBackground, in: RoundedRectangle(cornerRadius: 10))
        .overlay(RoundedRectangle(cornerRadius: 10).stroke(tone.codeBorder, lineWidth: 1))
    }
}

private enum MarkdownTone: Equatable {
    case standard
    case user
    case thought
    case tool

    var contentFontSize: QuartetFontSize {
        switch self {
        case .standard, .thought: .control
        case .user, .tool: .regular
        }
    }

    var foreground: Color {
        switch self {
        case .user: Color.white
        case .thought: QuartetTheme.primaryText.opacity(0.82)
        case .standard, .tool: QuartetTheme.primaryText
        }
    }

    var secondaryForeground: Color {
        self == .user ? Color.white.opacity(0.7) : QuartetTheme.secondaryText
    }

    var codeBackground: Color {
        switch self {
        case .user: Color.white.opacity(0.09)
        case .thought: Color.blue.opacity(0.07)
        case .standard, .tool: QuartetTheme.elevated.opacity(0.72)
        }
    }

    var codeBorder: Color {
        self == .user ? Color.white.opacity(0.18) : QuartetTheme.divider
    }
}

private struct MarkdownTableView: View {
    let headers: [String]
    let rows: [[String]]
    let tone: MarkdownTone

    var body: some View {
        ScrollView(.horizontal, showsIndicators: true) {
            Grid(horizontalSpacing: 0, verticalSpacing: 0) {
                GridRow {
                    ForEach(Array(headers.enumerated()), id: \.offset) { _, value in
                        MarkdownTextBlock(text: value, tone: tone)
                            .fontWeight(.semibold)
                            .padding(.horizontal, 10)
                            .padding(.vertical, 8)
                            .background(tone.codeBackground)
                    }
                }
                ForEach(Array(rows.enumerated()), id: \.offset) { _, row in
                    GridRow {
                        ForEach(0..<headers.count, id: \.self) { index in
                            MarkdownTextBlock(text: index < row.count ? row[index] : "", tone: tone)
                                .padding(.horizontal, 10)
                                .padding(.vertical, 8)
                        }
                    }
                }
            }
        }
        .background(tone.codeBackground.opacity(0.45), in: RoundedRectangle(cornerRadius: 9))
        .overlay(RoundedRectangle(cornerRadius: 9).stroke(tone.codeBorder, lineWidth: 1))
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
                        .font(.quartet(.detail))
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
            case table(headers: [String], rows: [[String]])
            case heading(level: Int, content: String)
            case quote(String)
            case list(ordered: Bool, items: [String])
            case divider
        }

        let id: Int
        let kind: Kind
    }

    static func blocks(from text: String) -> [Block] {
        var kinds: [Block.Kind] = []
        var remaining = text[...]

        while let opening = remaining.range(of: "```") {
            let leading = String(remaining[..<opening.lowerBound])
            if !leading.isEmpty {
                appendMarkdownAndTables(leading, to: &kinds)
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
                // An unfinished fence is common while SSE is still streaming.
                // Render the available payload as code immediately instead of
                // flashing raw backticks until the closing fence arrives.
                kinds.append(.code(language: language.isEmpty ? nil : language, content: String(remaining)))
                remaining = "".suffix(0)
                break
            }

            let code = String(remaining[..<closing.lowerBound]).trimmingCharacters(in: .newlines)
            kinds.append(.code(language: language.isEmpty ? nil : language, content: code))
            remaining = remaining[closing.upperBound...]
        }

        if !remaining.isEmpty {
            appendMarkdownAndTables(String(remaining), to: &kinds)
        }
        if kinds.isEmpty { appendMarkdownAndTables(text, to: &kinds) }
        return kinds.enumerated().map { Block(id: $0.offset, kind: $0.element) }
    }

    private static func appendMarkdownAndTables(_ text: String, to result: inout [Block.Kind]) {
        let lines = text.components(separatedBy: "\n")
        var markdownLines: [String] = []
        var index = 0

        func flushMarkdown() {
            guard !markdownLines.isEmpty else { return }
            result.append(.markdown(markdownLines.joined(separator: "\n")))
            markdownLines.removeAll(keepingCapacity: true)
        }

        while index < lines.count {
            if index + 1 < lines.count,
               isTableRow(lines[index]),
               isTableDivider(lines[index + 1]) {
                flushMarkdown()
                let headers = tableCells(lines[index])
                index += 2
                var rows: [[String]] = []
                while index < lines.count, isTableRow(lines[index]), !lines[index].trimmingCharacters(in: .whitespaces).isEmpty {
                    rows.append(tableCells(lines[index]))
                    index += 1
                }
                result.append(.table(headers: headers, rows: rows))
                continue
            }

            let trimmed = lines[index].trimmingCharacters(in: .whitespaces)
            if let heading = heading(in: trimmed) {
                flushMarkdown()
                result.append(.heading(level: heading.level, content: heading.content))
                index += 1
                continue
            }
            if isDivider(trimmed) {
                flushMarkdown()
                result.append(.divider)
                index += 1
                continue
            }
            if trimmed.hasPrefix("> ") || trimmed == ">" {
                flushMarkdown()
                var quoted: [String] = []
                while index < lines.count {
                    let candidate = lines[index].trimmingCharacters(in: .whitespaces)
                    guard candidate.hasPrefix(">") else { break }
                    quoted.append(String(candidate.dropFirst()).trimmingCharacters(in: .whitespaces))
                    index += 1
                }
                result.append(.quote(quoted.joined(separator: "\n")))
                continue
            }
            if let firstItem = listItem(in: trimmed) {
                flushMarkdown()
                var items = [firstItem.content]
                let ordered = firstItem.ordered
                index += 1
                while index < lines.count, let next = listItem(in: lines[index].trimmingCharacters(in: .whitespaces)), next.ordered == ordered {
                    items.append(next.content)
                    index += 1
                }
                result.append(.list(ordered: ordered, items: items))
                continue
            }
            markdownLines.append(lines[index])
            index += 1
        }
        flushMarkdown()
    }

    private static func isTableRow(_ line: String) -> Bool {
        tableCells(line).count > 1
    }

    private static func isTableDivider(_ line: String) -> Bool {
        let cells = tableCells(line)
        guard cells.count > 1 else { return false }
        return cells.allSatisfy { cell in
            let core = cell.trimmingCharacters(in: CharacterSet(charactersIn: " :-"))
            return core.isEmpty && cell.filter { $0 == "-" }.count >= 3
        }
    }

    private static func heading(in line: String) -> (level: Int, content: String)? {
        let markerCount = line.prefix { $0 == "#" }.count
        guard (1...6).contains(markerCount) else { return nil }
        let boundary = line.index(line.startIndex, offsetBy: markerCount)
        guard boundary < line.endIndex, line[boundary] == " " else { return nil }
        return (markerCount, String(line[line.index(after: boundary)...]))
    }

    private static func isDivider(_ line: String) -> Bool {
        let compact = line.filter { !$0.isWhitespace }
        guard compact.count >= 3, let first = compact.first, ["-", "*", "_"].contains(first) else { return false }
        return compact.allSatisfy { $0 == first }
    }

    private static func listItem(in line: String) -> (ordered: Bool, content: String)? {
        if line.hasPrefix("- ") || line.hasPrefix("* ") || line.hasPrefix("+ ") {
            return (false, String(line.dropFirst(2)))
        }
        guard let dot = line.firstIndex(of: "."), dot < line.endIndex else { return nil }
        let number = line[..<dot]
        let afterDot = line.index(after: dot)
        guard !number.isEmpty, number.allSatisfy(\.isNumber), afterDot < line.endIndex, line[afterDot] == " " else { return nil }
        return (true, String(line[line.index(after: afterDot)...]))
    }

    private static func tableCells(_ line: String) -> [String] {
        var value = line.trimmingCharacters(in: .whitespaces)
        if value.hasPrefix("|") { value.removeFirst() }
        if value.hasSuffix("|") { value.removeLast() }
        return value.split(separator: "|", omittingEmptySubsequences: false)
            .map { $0.trimmingCharacters(in: .whitespaces) }
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
    @Published var errorDetail: String?
    @Published var restoreDraft: ComposerDraft?
    @Published var restoreDraftVersion = 0
    @Published var scrollAnchor = 0
    @Published var terminalStateVersion = 0
    @Published private(set) var totalTokens = 0
    @Published private(set) var runStartedAt: Int64?
    @Published private(set) var runFinishedAt: Int64?
    @Published private(set) var accumulatedDurationMs: Int64 = 0

    private var client: APIClient?
    private var jobID = ""
    private(set) var sessionID: String?
    private var preferredSessionID: String?
    @Published private var modelID: String?
    @Published private var agentType: String?
    @Published private var agentDisplayName: String?
    @Published private var modeID: String?
    @Published private var thoughtLevelID: String?
    private var lastEventID: UInt64 = 0
    private var lastGraphEventID: UInt64 = 0
    private var streamTask: Task<Void, Never>?
    private var graphReconcileTask: Task<Void, Never>?
    private var graphMonitorTask: Task<Void, Never>?
    private var didSeedInitialDraft = false
    private var accumulatedRoundBoundaries: Set<String> = []
    private var isTurnRunning = false
    private var isProcessingOutbox = false
    private var isGraph = false
    private var graphRunLive = false

    var isRunning: Bool { isTurnRunning }
    var hasQueuedMessages: Bool {
        outbox.contains { if case .queued = $0.state { return true } else { return false } }
    }
    var timelineOutboxItems: [LocalOutboxItem] {
        let optimisticMessageIDs = Set(messages.map(\.id))
        return outbox.filter { $0.isVisibleInTimeline && !optimisticMessageIDs.contains($0.id) }
    }
    var composerOutboxItems: [LocalOutboxItem] {
        let optimisticMessageIDs = Set(messages.map(\.id))
        return outbox.filter { item in
            switch item.state {
            case .queued:
                return !optimisticMessageIDs.contains(item.id)
            case .failed:
                return true
            case .uploading, .sending, .awaitingEcho:
                return false
            }
        }
    }
    var agentDisplayLabel: String {
        displayValue(agentDisplayName) ?? displayValue(agentType) ?? "未指定 Agent"
    }
    var modelDisplayLabel: String { displayValue(modelID) ?? "未指定 Model" }
    var modeDisplayLabel: String { displayValue(modeID) ?? "默认模式" }
    var thoughtLevelDisplayLabel: String? { displayValue(thoughtLevelID) }
    var tokenCountLabel: String { "Tokens: \(Self.compactCount(totalTokens))" }
    var tokenCountAccessibilityLabel: String { "Token 数量，\(totalTokens)" }
    var showsDuration: Bool {
        accumulatedDurationMs > 0 || (runStartedAt != nil && (isTurnRunning || runFinishedAt != nil))
    }

    func durationLabel(at date: Date) -> String {
        let currentDuration: Int64
        if let runStartedAt {
            let end = runFinishedAt ?? Int64(date.timeIntervalSince1970 * 1_000)
            currentDuration = max(0, end - runStartedAt)
        } else {
            currentDuration = 0
        }
        return Self.formatDuration(accumulatedDurationMs + currentDuration)
    }

    func start(route: ChatRoute, client: APIClient) async {
        stopStreaming()
        let changesJob = !jobID.isEmpty && jobID != route.summary.id
        if changesJob {
            messages = []
            outbox = []
            sessionID = nil
            agentDisplayName = nil
            totalTokens = 0
            runStartedAt = nil
            runFinishedAt = nil
            accumulatedDurationMs = 0
            accumulatedRoundBoundaries = []
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
        let seededInitialDraftID = seedInitialDraftIfNeeded(route: route)

        do {
            let detail = try await client.job(id: jobID)
            title = detail.title
            status = detail.status
            runStartedAt = detail.startedAt
            runFinishedAt = detail.finishedAt
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
            scheduleOutboxProcessing()
        } catch {
            loading = false
            let detail = errorText(error)
            errorDetail = detail
            restoreInitialDraftIfNeeded(id: seededInitialDraftID, detail: detail)
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
        agentDisplayName = "TraeCode"
        totalTokens = 12_480
        runStartedAt = Int64(Date().addingTimeInterval(-83).timeIntervalSince1970 * 1_000)
        runFinishedAt = isGraph || route.summary.status == "running" ? nil : Int64(Date().timeIntervalSince1970 * 1_000)
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
        loading = false
        bumpScrollAnchor()
    }

    @discardableResult
    func enqueueDraft(
        text: String,
        attachment: PendingUpload?,
        remoteImagePaths: [String] = [],
        isInitialDraft: Bool = false
    ) -> String? {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty || attachment != nil || !remoteImagePaths.isEmpty else { return nil }
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
        if isInitialDraft && (attachment == nil || !remoteImagePaths.isEmpty) {
            upsertOptimisticUserMessage(for: item)
        }
        bumpScrollAnchor()
        scheduleOutboxProcessing()
        return item.id
    }

    func cancelOutboxItem(id: String) {
        outbox.removeAll { $0.id == id && $0.isCancelable }
        bumpScrollAnchor()
    }

    func retryOutboxItem(id: String) {
        guard let index = outbox.firstIndex(where: { $0.id == id }) else { return }
        let item = outbox[index]
        let startsNewExecution = item.retryRequiresNewMessageID
        if startsNewExecution {
            messages.removeAll { $0.id == item.id && $0.isOptimistic }
        }
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
        messages.removeAll { $0.id == id && $0.isOptimistic }
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
        let now = Int64(Date().timeIntervalSince1970 * 1_000)
        runFinishedAt = now
        finishOpenMessages(outcome: "stopped", timestamp: now)
        isTurnRunning = false
        stopStreaming()
        scheduleOutboxProcessing()
    }

    private func seedInitialDraftIfNeeded(route: ChatRoute) -> String? {
        guard !didSeedInitialDraft else { return nil }
        let hasInitialContent = (route.initialMessage?.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty == false)
            || route.initialAttachment != nil
            || !(route.initialImagePaths ?? []).isEmpty
        guard hasInitialContent else { return nil }

        didSeedInitialDraft = true
        return enqueueDraft(
            text: route.initialMessage?.trimmingCharacters(in: .whitespacesAndNewlines) ?? "",
            attachment: route.initialAttachment,
            remoteImagePaths: route.initialImagePaths ?? [],
            isInitialDraft: true
        )
    }

    private func restoreInitialDraftIfNeeded(id: String?, detail: String) {
        guard let id, let index = outbox.firstIndex(where: { $0.id == id }) else { return }
        let item = outbox.remove(at: index)
        messages.removeAll { $0.id == id && $0.isOptimistic }
        publishRestore(item.draft)
        errorDetail = detail
        bumpScrollAnchor()
    }

    private func loadHistory(sessionID: String, preservesLiveMessages: Bool = true) async throws {
        guard let client else { return }
        let response = try await client.sessionMessages(id: sessionID)
        applySessionMetadata(response)
        let historyMessages = convertHistoryMessages(response.messages)
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
            combined.append(contentsOf: convertHistoryMessages(
                response.messages,
                idPrefix: isLatestSession ? nil : currentSessionID
            ))
        }
        if let latest {
            applySessionMetadata(latest)
        }
        messages = combined
        removeEchoedOutboxItems()
        bumpScrollAnchor()
    }

    // Match the Web client's history projection: assistant tool-call metadata
    // creates the card, then the later role=tool row fills its result/status.
    // Keeping this pairing structured is important because a single free-form
    // detail string cannot distinguish a tool name from streamed arguments.
    private func convertHistoryMessages(_ history: [HistoryMessage], idPrefix: String? = nil) -> [ChatMessage] {
        func scopedID(_ id: String) -> String {
            idPrefix.map { "\($0):\(id)" } ?? id
        }

        // Keep one canonical base projection per history row, then expand the
        // assistant/tool relationship into the richer timeline below.
        let isLatestSession = idPrefix == nil
        let currentSessionID = idPrefix ?? ""
        let baseMessages = history.map {
            ChatMessage(history: $0, idPrefix: isLatestSession ? nil : currentSessionID)
        }
        var converted: [ChatMessage] = []
        var toolIndexByCallID: [String: Int] = [:]

        for (offset, item) in history.enumerated() {
            if item.role == "assistant" {
                var assistant = baseMessages[offset]
                assistant.detail = nil
                if item.isThinking == true || !item.content.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
                    || !(item.reasoningContent ?? "").isEmpty || !(item.imageUrls ?? []).isEmpty {
                    converted.append(assistant)
                }

                for call in item.toolCalls ?? [] {
                    let callID = scopedID(call.id)
                    let toolName = call.name == "undefined" ? "" : call.name
                    toolIndexByCallID[callID] = converted.count
                    converted.append(ChatMessage(
                        id: callID,
                        kind: .tool,
                        content: "",
                        detail: nil,
                        isFinished: false,
                        isFailed: false,
                        timestamp: item.finishedAt ?? item.thoughtFinishedAt ?? item.startedAt,
                        toolCallID: call.id,
                        toolName: toolName,
                        toolArguments: call.arguments,
                        toolStatus: .processing
                    ))
                }
                continue
            }

            if item.role == "tool" {
                let rawCallID = item.toolCallId ?? item.id
                let callID = scopedID(rawCallID)
                let finalStatus: ChatMessage.ToolStatus = item.placeholder == true
                    ? .placeholder
                    : (item.failed == true ? .error : .success)
                if let index = toolIndexByCallID[callID] {
                    converted[index].content = item.content
                    converted[index].isFinished = true
                    converted[index].isFailed = item.failed == true
                    converted[index].toolStatus = finalStatus
                    converted[index].placeholderReason = item.placeholderReason
                    converted[index].finishedAt = item.finishedAt
                    if let startedAt = item.startedAt { converted[index].timestamp = startedAt }
                } else {
                    var orphan = baseMessages[offset]
                    orphan.toolCallID = rawCallID
                    orphan.toolStatus = finalStatus
                    converted.append(orphan)
                    toolIndexByCallID[callID] = converted.count - 1
                }
                continue
            }

            converted.append(baseMessages[offset])
        }

        // A call without a persisted result was interrupted. Do not paint it
        // as successful after reload.
        for index in converted.indices where converted[index].kind == .tool && !converted[index].isFinished {
            converted[index].isFinished = true
            converted[index].toolStatus = .placeholder
            converted[index].placeholderReason = "unknown"
        }
        return converted
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
        archiveFinishedRoundIfNeeded()
        sending = true
        defer { sending = false }

        do {
            outbox[index].state = .sending
            if outbox[index].attachment == nil || !outbox[index].remoteImagePaths.isEmpty {
                upsertOptimisticUserMessage(for: outbox[index])
            }
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
                upsertOptimisticUserMessage(for: outbox[refreshed])
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
        upsertOptimisticUserMessage(for: outbox[index])
        isTurnRunning = true
        status = "running"
        bumpScrollAnchor()
    }

    private func upsertOptimisticUserMessage(for item: LocalOutboxItem) {
        if let index = messages.firstIndex(where: { $0.id == item.id }) {
            messages[index].content = item.draft.text
            messages[index].imagePaths = item.remoteImagePaths
            messages[index].isFailed = false
            return
        }
        messages.append(ChatMessage(
            id: item.id,
            kind: .user,
            content: item.draft.text,
            detail: nil,
            isFinished: true,
            isFailed: false,
            timestamp: item.createdAt,
            imagePaths: item.remoteImagePaths,
            isOptimistic: true
        ))
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
            if let messageIndex = messages.firstIndex(where: { $0.id == itemID }) {
                messages[messageIndex].isFailed = true
            }
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
        runStartedAt = snapshot.startedAt
        runFinishedAt = snapshot.finishedAt
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
            upsert(
                id: messageID, kind: .assistant, content: "", detail: nil,
                finished: false, failed: false, timestamp: event.createdAt
            )
        case "agentMessageDelta":
            guard belongsToVisibleSession, let messageID = payload["messageId"], !messageID.isEmpty else { return }
            append(
                id: messageID, kind: .assistant,
                text: payload["delta"] ?? event.message ?? "", timestamp: event.createdAt
            )
        case "agentMessageEnd":
            guard belongsToVisibleSession else { return }
            finish(id: payload["messageId"], timestamp: event.createdAt)
        case "agentThoughtStart":
            guard belongsToVisibleSession, let messageID = payload["messageId"], !messageID.isEmpty else { return }
            upsert(
                id: messageID, kind: .thought, content: "", detail: nil,
                finished: false, failed: false, timestamp: event.createdAt
            )
        case "agentThoughtDelta":
            guard belongsToVisibleSession, let messageID = payload["messageId"], !messageID.isEmpty else { return }
            append(
                id: messageID, kind: .thought,
                text: payload["delta"] ?? event.message ?? "", timestamp: event.createdAt
            )
        case "agentThoughtEnd":
            guard belongsToVisibleSession else { return }
            finish(id: payload["messageId"], timestamp: event.createdAt)
        case "agentToolStart":
            guard belongsToVisibleSession, let toolID = payload["toolCallId"], !toolID.isEmpty else { return }
            upsert(
                id: toolID, kind: .tool, content: "", detail: nil,
                finished: false, failed: false, timestamp: event.createdAt
            )
            configureTool(id: toolID, name: payload["toolName"], status: payload["status"])
        case "agentToolArgs":
            guard belongsToVisibleSession, let toolID = payload["toolCallId"], !toolID.isEmpty else { return }
            appendToolArguments(id: toolID, text: payload["delta"] ?? "", replace: payload["replace"] == "true")
        case "agentToolResult":
            guard belongsToVisibleSession, let toolID = payload["toolCallId"], !toolID.isEmpty else { return }
            if payload["stitched"] == "true", let index = messages.firstIndex(where: { $0.id == toolID }) {
                messages[index].content = payload["delta"] ?? event.message ?? messages[index].content
                messages[index].isFinished = true
                messages[index].isFailed = payload["status"] == "Error"
                messages[index].toolStatus = ChatMessage.ToolStatus(serverValue: payload["status"])
                messages[index].finishedAt = event.createdAt
                bumpScrollAnchor()
            } else {
                append(
                    id: toolID, kind: .tool,
                    text: payload["delta"] ?? event.message ?? "", timestamp: event.createdAt
                )
                if let index = messages.firstIndex(where: { $0.id == toolID }) {
                    messages[index].isFailed = payload["status"] == "Error"
                    messages[index].toolStatus = ChatMessage.ToolStatus(serverValue: payload["status"])
                }
            }
        case "agentToolEnd":
            guard belongsToVisibleSession else { return }
            if let toolID = payload["toolCallId"], let index = messages.firstIndex(where: { $0.id == toolID }) {
                messages[index].isFailed = payload["status"] == "Error"
                messages[index].toolStatus = ChatMessage.ToolStatus(serverValue: payload["status"])
                if let reason = payload["placeholderReason"], !reason.isEmpty {
                    messages[index].placeholderReason = reason
                    messages[index].toolStatus = .placeholder
                }
            }
            finish(id: payload["toolCallId"], timestamp: event.createdAt)
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
            self.finishOpenMessages(outcome: self.status, timestamp: Int64(Date().timeIntervalSince1970 * 1_000))
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
            runStartedAt = event.timestamp ?? runStartedAt
            runFinishedAt = nil
        case "RUN_STARTED":
            isTurnRunning = true
            runStartedAt = event.timestamp ?? runStartedAt
            runFinishedAt = nil
            if let clientMessageID = event.clientMessageId {
                setAwaitingEcho(itemID: clientMessageID)
            }
        case "RUN_FINISHED":
            runFinishedAt = event.timestamp ?? runFinishedAt
            finishOpenMessages(outcome: "completed", timestamp: event.timestamp)
            // RUN_FINISHED closes the Agent round, but the backend publishes the
            // authoritative JOB_* terminal event only after it has persisted the
            // Job transition. Keep the local queue paused until that event arrives;
            // sending here races the backend's still-running gate and returns 409.
        case "JOB_COMPLETED":
            status = "completed"
            isTurnRunning = false
            runFinishedAt = event.timestamp ?? runFinishedAt
            let outcome = event.runOutcome ?? "completed"
            publishTerminalStateChange()
            applyRunOutcome(outcome)
            finishOpenMessages(outcome: outcome, timestamp: event.timestamp)
            scheduleSnapshotRefresh()
            stopStreaming()
        case "JOB_FAILED":
            status = "failed"
            isTurnRunning = false
            runFinishedAt = event.timestamp ?? runFinishedAt
            let outcome = event.runOutcome ?? "failed"
            publishTerminalStateChange()
            applyRunOutcome(outcome)
            if let message = event.message, !message.isEmpty { errorDetail = message }
            finishOpenMessages(outcome: outcome, timestamp: event.timestamp)
            scheduleSnapshotRefresh()
            stopStreaming()
        case "JOB_STOPPED":
            status = "stopped"
            isTurnRunning = false
            runFinishedAt = event.timestamp ?? runFinishedAt
            let outcome = event.runOutcome ?? "stopped"
            publishTerminalStateChange()
            applyRunOutcome(outcome)
            finishOpenMessages(outcome: outcome, timestamp: event.timestamp)
            scheduleSnapshotRefresh()
            stopStreaming()
        case "RUN_ERROR":
            errorDetail = [event.code, event.message].compactMap { $0 }.joined(separator: "\n")
        case "COMMAND_SYSTEM_MESSAGE":
            applyCommandEvent(event)
        case "CUSTOM":
            applyCustomEvent(event)
        case "TEXT_MESSAGE_START":
            guard let messageID = event.messageId else { return }
            let kind: ChatMessage.Kind = event.external?.isThinking == true ? .thought : .assistant
            upsert(id: messageID, kind: kind, content: "", detail: nil, finished: false, failed: false, timestamp: event.timestamp)
        case "TEXT_MESSAGE_CONTENT":
            guard let messageID = event.messageId else { return }
            let kind: ChatMessage.Kind = event.external?.isThinking == true ? .thought : .assistant
            append(id: messageID, kind: kind, text: event.delta ?? "", timestamp: event.timestamp)
        case "TEXT_MESSAGE_END":
            finish(id: event.messageId, timestamp: event.timestamp)
        case "TOOL_CALL_START":
            guard let toolID = event.toolCallId else { return }
            upsert(id: toolID, kind: .tool, content: "", detail: nil, finished: false, failed: false, timestamp: event.timestamp)
            configureTool(id: toolID, name: event.toolCallName, status: event.toolCallStatus)
        case "TOOL_CALL_ARGS":
            guard let toolID = event.toolCallId else { return }
            appendToolArguments(id: toolID, text: event.delta ?? "", replace: event.replace == true)
        case "TOOL_CALL_RESULT":
            guard let toolID = event.toolCallId else { return }
            append(id: toolID, kind: .tool, text: event.delta ?? "", timestamp: event.timestamp)
            if let index = messages.firstIndex(where: { $0.id == toolID }) {
                messages[index].isFailed = event.toolCallStatus == "Error"
                messages[index].toolStatus = ChatMessage.ToolStatus(serverValue: event.toolCallStatus)
            }
        case "TOOL_CALL_END":
            if let toolID = event.toolCallId, let index = messages.firstIndex(where: { $0.id == toolID }) {
                messages[index].toolStatus = ChatMessage.ToolStatus(serverValue: event.toolCallStatus ?? (messages[index].isFailed ? "Error" : "Success"))
            }
            finish(id: event.toolCallId, timestamp: event.timestamp)
        case "TOOL_CALL_STITCHED":
            guard let toolID = event.toolCallId else { return }
            if let index = messages.firstIndex(where: { $0.id == toolID }) {
                messages[index].content = event.delta ?? messages[index].content
                messages[index].isFinished = true
                messages[index].isFailed = event.toolCallStatus == "Error"
                messages[index].toolStatus = ChatMessage.ToolStatus(serverValue: event.toolCallStatus)
                messages[index].finishedAt = event.timestamp
            }
        default:
            break
        }
    }

    private func applyCustomEvent(_ event: ServerEvent) {
        switch event.name {
        case "token_usage":
            if let tokens = event.value?.totalTokens {
                totalTokens = tokens
            }
        case "job_title_updated":
            if let updatedTitle = event.value?.title, !updatedTitle.isEmpty {
                title = updatedTitle
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

    private func configureTool(id: String, name: String?, status: String?) {
        guard let index = messages.firstIndex(where: { $0.id == id }) else { return }
        messages[index].toolCallID = id
        if let name, !name.isEmpty { messages[index].toolName = name }
        messages[index].toolStatus = ChatMessage.ToolStatus(serverValue: status)
    }

    private func appendToolArguments(id: String, text: String, replace: Bool) {
        guard let index = messages.firstIndex(where: { $0.id == id }) else { return }
        messages[index].toolArguments = replace ? text : (messages[index].toolArguments ?? "") + text
        bumpScrollAnchor()
    }

    private func finish(id: String?, timestamp: Int64? = nil) {
        guard let id, let index = messages.firstIndex(where: { $0.id == id }) else { return }
        messages[index].isFinished = true
        messages[index].finishedAt = timestamp ?? messages[index].finishedAt
        if messages[index].kind == .tool, messages[index].toolStatus == .processing {
            messages[index].toolStatus = messages[index].isFailed ? .error : .success
        }
        bumpScrollAnchor()
    }

    private func finishOpenMessages(outcome: String = "completed", timestamp: Int64? = nil) {
        for index in messages.indices {
            guard !messages[index].isFinished else { continue }
            messages[index].isFinished = true
            messages[index].finishedAt = timestamp ?? messages[index].finishedAt
            guard messages[index].kind == .tool, messages[index].toolStatus == .processing else { continue }
            if outcome == "completed" {
                messages[index].toolStatus = .success
            } else {
                messages[index].toolStatus = .placeholder
                messages[index].placeholderReason = outcome == "failed" ? "job_failed" : "interrupted"
            }
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
        let serverIDs = Set(messages.filter { !$0.isOptimistic }.map(\.id))
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
        let serverIDs = Set(messages.filter { !$0.isOptimistic }.map(\.id))
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
        if let messageIndex = messages.firstIndex(where: { $0.id == failedItem.id && $0.isOptimistic }) {
            messages[messageIndex].isFailed = true
        }
        publishRestore(failedItem.draft)
        removeEchoedOutboxItems()
        bumpScrollAnchor()
    }

    private func bumpScrollAnchor() {
        scrollAnchor &+= 1
    }

    private func hasPriorConversation(_ detail: JobDetail) -> Bool {
        detail.sessionCount > 0 || messages.contains { !$0.isOptimistic }
    }

    private func applySessionMetadata(_ response: SessionMessagesResponse) {
        modelID = response.modelId
        agentType = response.type
        modeID = response.acpMode
        thoughtLevelID = response.acpThoughtLevel
        totalTokens = response.tokenUsage?.totalTokens ?? totalTokens
        if let agentType = response.type, let display = response.agents?[agentType] {
            agentDisplayName = display.displayName
        } else {
            agentDisplayName = nil
        }
    }

    private func archiveFinishedRoundIfNeeded() {
        guard let start = runStartedAt, let end = runFinishedAt, end >= start else { return }
        let boundary = "\(start):\(end)"
        if accumulatedRoundBoundaries.insert(boundary).inserted {
            accumulatedDurationMs += end - start
        }
        runStartedAt = nil
        runFinishedAt = nil
    }

    private func displayValue(_ value: String?) -> String? {
        guard let trimmed = value?.trimmingCharacters(in: .whitespacesAndNewlines), !trimmed.isEmpty else { return nil }
        return trimmed
    }

    private static func compactCount(_ value: Int) -> String {
        guard value >= 1_000 else { return String(value) }
        return String(format: "%.2fK", Double(value) / 1_000)
    }

    private static func formatDuration(_ milliseconds: Int64) -> String {
        guard milliseconds >= 1_000 else { return "\(milliseconds)ms" }
        if milliseconds < 60_000 {
            return String(format: "%.1fs", Double(milliseconds) / 1_000)
        }
        if milliseconds < 3_600_000 {
            return "\(milliseconds / 60_000)m \((milliseconds % 60_000) / 1_000)s"
        }
        return "\(milliseconds / 3_600_000)h \((milliseconds % 3_600_000) / 60_000)m"
    }

    private func errorText(_ error: Error) -> String {
        if let error = error as? APIError { return error.detail }
        return String(describing: error)
    }
}

