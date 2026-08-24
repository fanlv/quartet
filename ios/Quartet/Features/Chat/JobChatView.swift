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
    @State private var showsMessageLibrary = false
    @State private var focusesComposerAfterMessageLibrary = false
    @State private var sentMessageHistory: [SentMessageHistoryItem] = []
    @State private var projectMessagePresets: [MessagePreset] = []
    @State private var globalMessagePresets: [MessagePreset] = []
    @State private var messagePresetLoadErrors: [String] = []
    @State private var loadingMessagePresets = false
    @State private var userScrolledAwayFromBottom = false
    @State private var userIsScrollingMessages = false
    @State private var configuredModels: AgentModelState?
    @State private var configuredThoughtLevels: AgentThoughtLevelState?
    @State private var changingACPConfiguration = false
    @State private var gitBranch = ""
    @FocusState private var composerFocused: Bool

    var body: some View {
        VStack(spacing: 0) {
            messageList
            composer
        }
        .ignoresSafeArea(.container, edges: .bottom)
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
            }
            .sharedBackgroundVisibility(.hidden)
        }
        .quartetPlainNavigationBackButton()
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
        .task(id: route.summary.id) {
            if appModel.agentCatalogSnapshot.isEmpty {
                await appModel.refreshAgentCatalog()
            }
        }
        .task(id: workspaceContextKey) {
            await loadGitBranch()
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
        .onChange(of: chat.expectsExecution) { wasExpected, isExpected in
            guard wasExpected, !isExpected else { return }
            appModel.cancelOptimisticJobExecution(id: route.summary.id)
            Task { await appModel.reloadJobs() }
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
            Text(chat.serverQueue.items.isEmpty
                ? "正在执行的 Agent 将收到停止请求。"
                : "正在执行的 Agent 将收到停止请求，后续排队消息会保留并暂停，需手动继续。")
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
            .quartetSheetStyle()
        }
        .sheet(isPresented: $showsDocumentPicker) {
            DocumentAttachmentPicker(
                onDocumentPicked: { url in
                    showsDocumentPicker = false
                    Task { await loadDocument(url) }
                },
                onCancel: {
                    showsDocumentPicker = false
                }
            )
            .quartetSheetStyle()
        }
        .sheet(isPresented: $showsMessageLibrary, onDismiss: {
            if focusesComposerAfterMessageLibrary {
                focusesComposerAfterMessageLibrary = false
                composerFocused = true
            }
        }) {
            MessagePresetHistorySheet(
                currentMessage: $draft,
                projectPresets: projectMessagePresets,
                globalPresets: globalMessagePresets,
                history: sentMessageHistory,
                errors: messagePresetLoadErrors,
                loading: loadingMessagePresets,
                onApplied: { focusesComposerAfterMessageLibrary = true }
            )
            .presentationDetents([.medium, .large])
            .quartetSheetStyle()
            .task(id: route.summary.workspaceId) { await loadMessagePresets() }
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
                        ChatBubble(
                            message: message,
                            fallbackAgentName: chat.agentDisplayLabel,
                            fallbackAgentIconUrl: chat.agentDisplayIconUrl
                        )
                            .id(message.id)
                    }
                    ForEach(chat.timelineOutboxItems) { item in
                        OutboxBubble(item: item)
                            .id(item.id)
                    }
                    if chat.isRunning {
                        HStack(spacing: 9) {
                            Spacer(minLength: 0)
                            ProgressView()
                                .controlSize(.small)
                                .tint(QuartetTheme.accent)
                            Text("AI 正在思考...")
                                .font(.quartet(.control, weight: .medium))
                                .foregroundStyle(QuartetTheme.secondaryText)
                            Spacer(minLength: 0)
                        }
                        .padding(.horizontal, 4)
                        .padding(.vertical, 6)
                        .accessibilityElement(children: .combine)
                        .accessibilityLabel("AI 正在思考")
                    }
                    Color.clear.frame(height: 1).id("chat-bottom")
                }
                .padding(.horizontal, 14)
                .padding(.vertical, 18)
            }
            .scrollDismissesKeyboard(.interactively)
            .simultaneousGesture(TapGesture().onEnded { composerFocused = false })
            .onScrollPhaseChange { _, newPhase in
                userIsScrollingMessages = newPhase.isScrolling && newPhase != .animating
            }
            .onScrollGeometryChange(for: Bool.self) { geometry in
                let distanceToBottom = geometry.contentSize.height
                    - geometry.contentOffset.y
                    - geometry.containerSize.height
                return distanceToBottom < 80
            } action: { _, isNearBottom in
                guard userIsScrollingMessages else { return }
                userScrolledAwayFromBottom = !isNearBottom
            }
            .onChange(of: chat.scrollAnchor) { _, _ in
                guard !userScrolledAwayFromBottom else { return }
                withAnimation(.easeOut(duration: 0.2)) {
                    proxy.scrollTo("chat-bottom", anchor: .bottom)
                }
            }
            .onChange(of: chat.isRunning) { wasRunning, isRunning in
                guard !wasRunning, isRunning else { return }
                userScrolledAwayFromBottom = false
                withAnimation(.easeOut(duration: 0.2)) {
                    proxy.scrollTo("chat-bottom", anchor: .bottom)
                }
            }
        }
    }

    private var composer: some View {
        let hasPendingAttachment = pendingImage != nil
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

            if !chat.serverQueue.items.isEmpty || chat.serverQueue.paused {
                VStack(spacing: 0) {
                    if chat.serverQueue.paused {
                        HStack {
                            Text(chat.serverQueue.pauseReason == "blocked" ? "队列已阻塞，请删除失败消息" : "队列已暂停")
                                .font(.quartet(.detail, weight: .semibold))
                                .foregroundStyle(QuartetTheme.secondaryText)
                            Spacer()
                            if chat.serverQueue.pauseReason != "blocked" {
                                Button("继续队列") { Task { await chat.continueQueue() } }
                                    .font(.quartet(.detail, weight: .semibold))
                            }
                        }
                        .padding(.horizontal, 12)
                        .padding(.vertical, 9)
                    }
                    ScrollView {
                        LazyVStack(spacing: 0) {
                            ForEach(Array(chat.serverQueue.items.enumerated()), id: \.element.id) { index, item in
                                ServerQueueRow(
                                    index: index + 1, item: item,
                                    deleting: chat.deletingQueueIDs.contains(item.id),
                                    onShowError: { chat.showQueueError(item) },
                                    onDelete: { Task { await chat.deleteQueuedMessage(id: item.id) } }
                                )
                            }
                        }
                    }
                    .frame(maxHeight: 156)
                }
                .background(QuartetTheme.surface)
                .clipShape(UnevenRoundedRectangle(topLeadingRadius: 16, bottomLeadingRadius: 0, bottomTrailingRadius: 0, topTrailingRadius: 16))
                .overlay(
                    UnevenRoundedRectangle(topLeadingRadius: 16, bottomLeadingRadius: 0, bottomTrailingRadius: 0, topTrailingRadius: 16)
                        .stroke(QuartetTheme.divider, lineWidth: 1)
                )
                .padding(.bottom, -10)
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
                            .accessibilityLabel("移除附件")
                            .padding(18)
                        }
                }

                TextField("继续对话…", text: $draft, axis: .vertical)
                    .font(.quartet(.regular))
                    .lineLimit(1...6)
                    .focused($composerFocused)
                    .padding(.horizontal, 15)
                    .padding(.vertical, 14)
                    .frame(minHeight: 54, alignment: .topLeading)
                    .accessibilityIdentifier("chat-composer")

                Divider()
                    .overlay(QuartetTheme.divider.opacity(0.7))

                WrappingHStack(spacing: 7) {
                    composerContext

                    Button {
                        composerFocused = false
                        loadSentMessageHistory()
                        loadingMessagePresets = true
                        showsMessageLibrary = true
                    } label: {
                        Image(systemName: "clock.arrow.circlepath")
                            .font(.quartet(.compact, weight: .semibold))
                            .foregroundStyle(QuartetTheme.secondaryText)
                            .frame(width: 36, height: 36)
                            .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("预置消息与历史")
                    .accessibilityHint("打开后可从分组列表中选择")
                    .accessibilityIdentifier("chat-message-history")

                    PhotosPicker(selection: $selectedPhoto, matching: .images) {
                        Image(systemName: hasPendingAttachment ? "paperclip.circle.fill" : "photo")
                            .font(.quartet(.control, weight: .semibold))
                            .foregroundStyle(hasPendingAttachment ? QuartetTheme.accent : QuartetTheme.secondaryText)
                            .frame(width: 36, height: 36)
                            .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                    }
                    .simultaneousGesture(TapGesture().onEnded { composerFocused = false })
                    .accessibilityLabel("从相册选择图片")

                    Button {
                        composerFocused = false
                        showsAttachmentMenu = true
                    } label: {
                        Image(systemName: "plus")
                            .font(.quartet(.control, weight: .bold))
                            .foregroundStyle(QuartetTheme.secondaryText)
                            .frame(width: 36, height: 36)
                            .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("更多附件来源")
                    .accessibilityIdentifier("chat-attachment-menu")
                    .popover(
                        isPresented: $showsAttachmentMenu,
                        attachmentAnchor: .rect(.bounds),
                        arrowEdge: .bottom
                    ) {
                        AttachmentSourcePopover(
                            onCamera: {
                                showsAttachmentMenu = false
                                Task { @MainActor in
                                    await Task.yield()
                                    requestCameraAccess()
                                }
                            },
                            onFile: {
                                showsAttachmentMenu = false
                                Task { @MainActor in
                                    await Task.yield()
                                    showsDocumentPicker = true
                                }
                            }
                        )
                    }

                    if chat.isRunning {
                        Button(role: .destructive) {
                            composerFocused = false
                            confirmsStop = true
                        } label: {
                            Image(systemName: "stop.fill")
                                .font(.quartet(.control, weight: .bold))
                                .foregroundStyle(QuartetTheme.onAccent)
                                .frame(width: 38, height: 38)
                                .background(QuartetTheme.failed, in: Circle())
                        }
                        .buttonStyle(.plain)
                        .accessibilityLabel("停止生成")
                        .accessibilityIdentifier("chat-stop")
                    }

                    Button {
                        composerFocused = false
                        enqueueDraft()
                    } label: {
                        Image(systemName: "arrow.up")
                            .font(.quartet(.control, weight: .bold))
                            .foregroundStyle(sendDisabled ? QuartetTheme.secondaryText : QuartetTheme.onAccent)
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
                .simultaneousGesture(TapGesture().onEnded { composerFocused = false })

                if workspaceName != nil || workspaceWorkdir != nil {
                    Divider()
                        .overlay(QuartetTheme.divider.opacity(0.7))
                    workspaceFooter
                }
            }
            .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 20, style: .continuous))
            .overlay(
                RoundedRectangle(cornerRadius: 20, style: .continuous)
                    .stroke(QuartetTheme.divider, lineWidth: 1)
            )
            .shadow(color: Color.black.opacity(0.05), radius: 16, y: 7)

            if !chat.isRunning && (!chat.serverQueue.items.isEmpty || chat.hasQueuedMessages) {
                Text(chat.serverQueue.paused ? "服务端队列已暂停，继续后会按顺序发送。" : "队列中的消息会由服务端依次发送，可在执行前删除。")
                    .font(.quartet(.detail))
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .padding(.horizontal, 12)
        .padding(.top, 10)
        .padding(.bottom, 20)
        .background(.thinMaterial)
    }

    private var composerContext: some View {
        Group {
            ComposerMetadataChip(
                agentIconUrl: chat.agentDisplayIconUrl,
                text: chat.agentDisplayLabel,
                accessibilityLabel: "Agent，\(chat.agentDisplayLabel)"
            )
            Menu {
                ForEach(availableModels) { model in
                    Button {
                        Task { await selectModel(model.modelId) }
                    } label: {
                        if model.modelId == chat.modelIDForDisplay {
                            Label(model.name, systemImage: "checkmark")
                        } else {
                            Text(model.name)
                        }
                    }
                }
            } label: {
                ComposerMetadataChip(
                    icon: changingACPConfiguration ? "arrow.trianglehead.2.clockwise.rotate.90" : "cpu",
                    text: modelDisplayLabel,
                    accessibilityLabel: "模型，\(modelDisplayLabel)"
                )
            }
            .buttonStyle(.plain)
            .disabled(availableModels.isEmpty || changingACPConfiguration)
            .accessibilityIdentifier("chat-model-selector")
            ComposerMetadataChip(
                icon: "slider.horizontal.3",
                text: modeDisplayLabel,
                accessibilityLabel: "模式，\(modeDisplayLabel)"
            )
            if !availableThoughtLevels.isEmpty || thoughtLevelDisplayLabel != nil {
                let thoughtLevel = thoughtLevelDisplayLabel ?? "思考等级"
                Menu {
                    ForEach(availableThoughtLevels) { level in
                        Button {
                            Task { await selectThoughtLevel(level.id) }
                        } label: {
                            if level.id == chat.thoughtLevelIDForDisplay {
                                Label(level.name, systemImage: "checkmark")
                            } else {
                                Text(level.name)
                            }
                        }
                    }
                } label: {
                    ComposerMetadataChip(
                        icon: changingACPConfiguration ? "arrow.trianglehead.2.clockwise.rotate.90" : "brain.head.profile",
                        text: thoughtLevel,
                        accessibilityLabel: "思考等级，\(thoughtLevel)"
                    )
                }
                .buttonStyle(.plain)
                .disabled(availableThoughtLevels.isEmpty || changingACPConfiguration)
                .accessibilityIdentifier("chat-thought-level-selector")
            }
            ComposerMetadataChip(
                icon: "text.word.spacing",
                text: chat.tokenCountLabel,
                accessibilityLabel: chat.tokenCountAccessibilityLabel
            )
            if let agentType = agentRuntimeType {
                AgentUsageStrip(
                    command: agentType,
                    displayName: chat.agentDisplayLabel
                )
            }
            if chat.showsDuration {
                TimelineView(.periodic(from: .now, by: 1)) { timeline in
                    ComposerMetadataChip(
                        icon: "clock",
                        text: chat.durationLabel(at: timeline.date),
                        accessibilityLabel: "耗时，\(chat.durationLabel(at: timeline.date))"
                    )
                }
            }
        }
    }

    private var workspaceFooter: some View {
        ViewThatFits(in: .horizontal) {
            workspaceFooterLine(path: workspaceWorkdir ?? "—")
                .fixedSize(horizontal: true, vertical: false)
            workspaceFooterLine(path: abbreviatedWorkspacePath)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 12)
        .padding(.vertical, 7)
        .accessibilityElement(children: .combine)
        .accessibilityLabel(
            "工作空间，\(workspaceName ?? route.summary.workspaceId ?? "未指定")，"
                + "目录，\(workspaceWorkdir ?? "未指定")"
                + (gitBranch.isEmpty ? "" : "，Git 分支，\(gitBranch)")
        )
        .accessibilityIdentifier("workspace-footer")
    }

    private func workspaceFooterLine(path: String) -> some View {
        HStack(spacing: 6) {
            Image(systemName: "square.stack.3d.up")
                .foregroundStyle(QuartetTheme.accent)
            Text("Workspace(\(workspaceName ?? route.summary.workspaceId ?? "—")) :")
                .font(.quartet(.compact, weight: .semibold))
                .foregroundStyle(QuartetTheme.primaryText)
                .lineLimit(1)
            Text(path)
                .font(.system(size: 11.5, weight: .medium, design: .monospaced))
                .foregroundStyle(QuartetTheme.secondaryText)
                .lineLimit(1)
                .truncationMode(.middle)
            Spacer(minLength: 0)
            if !gitBranch.isEmpty {
                Label(gitBranch, systemImage: "point.3.connected.trianglepath.dotted")
                    .font(.quartet(.compact, weight: .semibold))
                    .foregroundStyle(QuartetTheme.accent)
                    .lineLimit(1)
                    .padding(.horizontal, 8)
                    .frame(height: 22)
                    .background(QuartetTheme.accent.opacity(0.1), in: Capsule())
            }
        }
    }

    private var sendDisabled: Bool {
        chat.loading || (draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && pendingImage == nil)
    }

    private var modelDisplayLabel: String {
        if let modelID = chat.modelIDForDisplay,
           let name = configuredModels?.availableModels.first(where: { $0.modelId == modelID })?.name,
           !name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            return name
        }
        return AgentConfigurationDisplay.modelName(
            chat.modelIDForDisplay,
            agentReference: chat.agentReferenceForDisplay,
            agents: appModel.agentCatalogSnapshot
        ) ?? "未指定 Model"
    }

    private var agentRuntimeType: String? {
        guard let reference = chat.agentRuntimeType else { return nil }
        return appModel.agentCatalogSnapshot.first {
            $0.agentId == reference || $0.type == reference
        }?.type ?? reference
    }

    private var modeDisplayLabel: String {
        AgentConfigurationDisplay.modeName(
            chat.modeIDForDisplay,
            agentReference: chat.agentReferenceForDisplay,
            agents: appModel.agentCatalogSnapshot
        ) ?? "默认模式"
    }

    private var thoughtLevelDisplayLabel: String? {
        if let thoughtLevelID = chat.thoughtLevelIDForDisplay,
           let name = configuredThoughtLevels?.availableThoughtLevels
            .first(where: { $0.id == thoughtLevelID })?.name,
           !name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            return name
        }
        return AgentConfigurationDisplay.thoughtLevelName(
            chat.thoughtLevelIDForDisplay,
            agentReference: chat.agentReferenceForDisplay,
            agents: appModel.agentCatalogSnapshot
        )
    }

    private var selectedAgent: AgentSummary? {
        guard let reference = chat.agentReferenceForDisplay else { return nil }
        return appModel.agentCatalogSnapshot.first { agent in
            agent.agentId == reference || agent.type == reference
        }
    }

    private var availableModels: [AgentModel] {
        configuredModels?.availableModels ?? selectedAgent?.models?.availableModels ?? []
    }

    private var availableThoughtLevels: [AgentOption] {
        configuredThoughtLevels?.availableThoughtLevels
            ?? selectedAgent?.thoughtLevels?.availableThoughtLevels
            ?? []
    }

    private var workspaceName: String? {
        appModel.workspaces.first(where: { $0.id == route.summary.workspaceId })?.displayName
    }

    private var workspaceWorkdir: String? {
        let value = route.summary.workdir
            ?? appModel.workspaces.first(where: { $0.id == route.summary.workspaceId })?.workdir
        guard let value = value?.trimmingCharacters(in: .whitespacesAndNewlines), !value.isEmpty else {
            return nil
        }
        return value
    }

    private var abbreviatedWorkspacePath: String {
        guard let path = workspaceWorkdir else { return "—" }
        let components = path.split(separator: "/", omittingEmptySubsequences: true)
        guard components.count > 2, let first = components.first, let last = components.last else {
            return path
        }
        let leadingSlash = path.hasPrefix("/") ? "/" : ""
        return "\(leadingSlash)\(first)/.../\(last)"
    }

    private var workspaceContextKey: String {
        [
            route.summary.workspaceId,
            workspaceWorkdir,
            appModel.can("file.read") ? "file.read" : "no-file.read"
        ]
        .compactMap { $0 }
        .joined(separator: "::")
    }

    private func selectModel(_ modelID: String) async {
        guard modelID != chat.modelIDForDisplay else { return }
        await applyACPConfiguration(target: .model, modelID: modelID, thoughtLevelID: nil)
    }

    private func selectThoughtLevel(_ thoughtLevelID: String) async {
        guard thoughtLevelID != chat.thoughtLevelIDForDisplay else { return }
        await applyACPConfiguration(
            target: .thoughtLevel,
            modelID: chat.modelIDForDisplay,
            thoughtLevelID: thoughtLevelID
        )
    }

    private func applyACPConfiguration(
        target: ACPConfigTarget,
        modelID: String?,
        thoughtLevelID: String?
    ) async {
        guard !changingACPConfiguration else { return }
        let sessionID = chat.configurationSessionID
        guard sessionID != nil || selectedAgent != nil else {
            appModel.present(APIError(
                summary: "无法切换 Agent 配置",
                detail: "当前会话没有可用的 sessionId，也无法从 Agent 列表解析 \(chat.agentReferenceForDisplay ?? "<empty>")。"
            ))
            return
        }

        changingACPConfiguration = true
        defer { changingACPConfiguration = false }
        do {
            let response = try await appModel.setACPConfig(SetACPConfigRequest(
                target: target,
                sessionId: sessionID,
                agentType: sessionID == nil ? selectedAgent?.type : nil,
                model: modelID,
                mode: target == .model ? nil : chat.modeIDForDisplay,
                thoughtLevel: target == .model ? nil : thoughtLevelID
            ))
            if let models = response.models { configuredModels = models }
            if target == .model {
                configuredThoughtLevels = response.thoughtLevels ?? AgentThoughtLevelState(
                    availableThoughtLevels: [],
                    currentThoughtLevelId: ""
                )
            } else if let thoughtLevels = response.thoughtLevels {
                configuredThoughtLevels = thoughtLevels
            }
            chat.applyACPConfiguration(
                response,
                target: target,
                selectedModelID: modelID,
                selectedThoughtLevelID: thoughtLevelID
            )
        } catch is CancellationError {
            return
        } catch {
            appModel.present(error)
        }
    }

    private func loadGitBranch() async {
        gitBranch = ""
        guard let workspaceWorkdir else { return }
        guard appModel.can("file.read") else { return }
        if appModel.isRunningUITests {
            gitBranch = "main"
            return
        }
        do {
            let response = try await appModel.apiClient().gitBranch(path: workspaceWorkdir)
            guard response.code == 0 else {
                throw APIError(
                    summary: "无法读取 Git 分支",
                    detail: "GET /api/v1/git-branch?path=\(workspaceWorkdir) 返回 code=\(response.code)。"
                )
            }
            guard !Task.isCancelled else { return }
            gitBranch = response.branch.trimmingCharacters(in: .whitespacesAndNewlines)
        } catch is CancellationError {
            return
        } catch {
            appModel.present(error)
        }
    }

    private func enqueueDraft() {
        let text = draft.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty || pendingImage != nil else { return }
        do {
            try appModel.recordSentMessage(text, workspaceID: route.summary.workspaceId)
        } catch {
            appModel.present(error)
        }
        if chat.enqueueDraft(text: text, attachment: pendingImage) != nil {
            appModel.beginOptimisticJobExecution(id: route.summary.id, fallback: route.summary)
        }
        draft = ""
        pendingImage = nil
        selectedPhoto = nil
    }

    private func loadSentMessageHistory() {
        do {
            sentMessageHistory = try appModel.sentMessageHistory(workspaceID: route.summary.workspaceId)
        } catch {
            appModel.present(error)
        }
    }

    private func loadMessagePresets() async {
        guard let workspaceID = route.summary.workspaceId, !workspaceID.isEmpty else {
            projectMessagePresets = []
            globalMessagePresets = []
            messagePresetLoadErrors = []
            loadingMessagePresets = false
            return
        }
        loadingMessagePresets = true
        projectMessagePresets = []
        globalMessagePresets = []
        messagePresetLoadErrors = []
        defer { loadingMessagePresets = false }
        do {
            let response = try await appModel.effectiveMessagePresets(workspaceID: workspaceID)
            projectMessagePresets = response.project
            globalMessagePresets = response.global
            messagePresetLoadErrors = (response.errors ?? []).map { error in
                [error.scope, error.file, error.error]
                    .filter { !$0.isEmpty }
                    .joined(separator: "\n")
            }
        } catch is CancellationError {
            return
        } catch let error as APIError {
            messagePresetLoadErrors = ["\(error.summary)\n\n\(error.detail)"]
        } catch {
            messagePresetLoadErrors = [String(reflecting: error)]
        }
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
            try ChatAttachmentProcessor.prepareFileUpload(
                data: data,
                suggestedFilename: url.lastPathComponent,
                contentType: UTType(filenameExtension: url.pathExtension)
            )
        }
    }
}

private struct ComposerMetadataChip: View {
    let icon: String?
    let agentIconUrl: String?
    let text: String
    let accessibilityLabel: String

    init(icon: String, text: String, accessibilityLabel: String) {
        self.icon = icon
        agentIconUrl = nil
        self.text = text
        self.accessibilityLabel = accessibilityLabel
    }

    init(agentIconUrl: String?, text: String, accessibilityLabel: String) {
        icon = nil
        self.agentIconUrl = agentIconUrl
        self.text = text
        self.accessibilityLabel = accessibilityLabel
    }

    var body: some View {
        HStack(spacing: 5) {
            if let icon {
                Image(systemName: icon)
                    .font(.quartet(.compact, weight: .semibold))
            } else {
                AgentIdentityIcon(iconUrl: agentIconUrl)
            }
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

private enum AgentUsageProvider: String {
    case codex
    case claude
    case antigravity
    case kimi
    case qoder

    static func resolve(command: String, displayName: String) -> Self? {
        let candidate = "\(command) \(displayName)".lowercased()
        if candidate.contains("antigravity") { return .antigravity }
        if candidate.contains("codex") { return .codex }
        if candidate.contains("claude") { return .claude }
        if candidate.contains("qoder") || candidate.contains("qcode") { return .qoder }
        if candidate.contains("kimi") { return .kimi }
        return nil
    }
}

private enum AgentUsageCache {
    static func usage(provider: AgentUsageProvider, namespace: String) -> AgentUsageResponse? {
        guard let data = UserDefaults.standard.data(forKey: key("agentUsage_\(provider.rawValue)", namespace: namespace)) else { return nil }
        return try? JSONDecoder().decode(AgentUsageResponse.self, from: data)
    }

    static func setUsage(_ usage: AgentUsageResponse, provider: AgentUsageProvider, namespace: String) {
        guard let data = try? JSONEncoder().encode(usage) else { return }
        UserDefaults.standard.set(data, forKey: key("agentUsage_\(provider.rawValue)", namespace: namespace))
    }

    static func version(command: String, namespace: String) -> String {
        UserDefaults.standard.string(forKey: key("agentVersion_\(command)", namespace: namespace)) ?? ""
    }

    static func setVersion(_ version: String, command: String, namespace: String) {
        let key = key("agentVersion_\(command)", namespace: namespace)
        if version.isEmpty {
            UserDefaults.standard.removeObject(forKey: key)
        } else {
            UserDefaults.standard.set(version, forKey: key)
        }
    }

    private static func key(_ value: String, namespace: String) -> String {
        "\(namespace)|\(value)"
    }
}

private struct AgentUsageDetail: Identifiable {
    let id = UUID()
    let title: String
    let lines: [String]
}

private struct AgentUsageStrip: View {
    @EnvironmentObject private var appModel: AppModel

    let command: String
    let displayName: String

    @State private var usage: AgentUsageResponse?
    @State private var version = ""
    @State private var loading = false
    @State private var requestError: APIError?
    @State private var detail: AgentUsageDetail?

    private var provider: AgentUsageProvider? {
        AgentUsageProvider.resolve(command: command, displayName: displayName)
    }

    private var identity: String {
        "\(appModel.serverAddress):\(provider?.rawValue ?? "version"):\(command)"
    }

    var body: some View {
        Group {
            if provider != nil || !version.isEmpty || requestError != nil {
                HStack(spacing: 5) {
                    usageContent

                    if loading {
                        ProgressView()
                            .controlSize(.mini)
                            .tint(QuartetTheme.secondaryText)
                            .accessibilityLabel("正在获取 Agent 用量")
                    } else {
                        Button {
                            Task { await refresh() }
                        } label: {
                            Image(systemName: "arrow.clockwise")
                                .font(.system(size: 10, weight: .semibold))
                                .foregroundStyle(QuartetTheme.secondaryText.opacity(0.72))
                                .frame(width: 22, height: 22)
                        }
                        .buttonStyle(.plain)
                        .accessibilityLabel("刷新 Agent 用量")
                    }

                    if let requestError {
                        Button {
                            appModel.present(requestError)
                        } label: {
                            Image(systemName: "exclamationmark.triangle.fill")
                                .font(.system(size: 10, weight: .semibold))
                                .foregroundStyle(QuartetTheme.failed)
                                .frame(width: 22, height: 22)
                        }
                        .buttonStyle(.plain)
                        .accessibilityLabel("查看 Agent 用量错误")
                    }
                }
                .padding(.leading, 4)
                .accessibilityElement(children: .contain)
            }
        }
        .task(id: identity) {
            restoreCache()
            if appModel.isRunningUITests {
                version = "v1.0.0"
                return
            }
            await refresh()
        }
        .popover(item: $detail, attachmentAnchor: .rect(.bounds), arrowEdge: .bottom) { value in
            VStack(alignment: .leading, spacing: 6) {
                Text(value.title)
                    .font(.quartet(.detail, weight: .semibold))
                    .foregroundStyle(QuartetTheme.primaryText)
                ForEach(Array(value.lines.enumerated()), id: \.offset) { _, line in
                    Text(line)
                        .font(.quartet(.compact, design: .monospaced))
                        .foregroundStyle(QuartetTheme.secondaryText)
                }
            }
            .padding(12)
            .background(QuartetTheme.surface)
            .presentationCompactAdaptation(.popover)
            .presentationBackground(QuartetTheme.surface)
        }
    }

    @ViewBuilder
    private var usageContent: some View {
        if let provider, let usage {
            switch provider {
            case .codex:
                if let value = usage.codex { codexContent(value) }
            case .claude:
                if let value = usage.claude { claudeContent(value) }
            case .antigravity:
                if let value = usage.antigravity { antigravityContent(value) }
            case .kimi:
                if let value = usage.kimi { kimiContent(value) }
            case .qoder:
                if let value = usage.qoder { qoderContent(value) }
            }
        } else if !version.isEmpty {
            versionLabel(version)
        }
    }

    private func codexContent(_ value: CodexAgentUsage) -> some View {
        Group {
            if let version = displayValue(value.version) { versionLabel(version) }
            if let window = value.primaryWindow { usageRing(label: windowLabel(window), window: window) }
            if let window = value.secondaryWindow { usageRing(label: windowLabel(window), window: window) }
            usageRing(
                label: String(value.resetCredits),
                percent: value.resetCredits > 0 ? 100 : 0,
                color: value.resetCredits > 0 ? QuartetTheme.accentDeep : QuartetTheme.secondaryText.opacity(0.55),
                detail: AgentUsageDetail(
                    title: "剩余 \(value.resetCredits) 次重置额度",
                    lines: (value.resetCreditExpiries ?? []).map { "\(formatDate($0, includesDate: true)) 到期" }
                )
            )
        }
    }

    private func claudeContent(_ value: ClaudeAgentUsage) -> some View {
        Group {
            if let version = displayValue(value.version) { versionLabel(version) }
            usageMetric(icon: "calendar", text: money(value.todayCost), emphasis: QuartetTheme.running, label: "今日花费 \(money(value.todayCost))")
            usageMetric(icon: "sum", text: money(value.totalCost), emphasis: QuartetTheme.primaryText, label: "累计花费 \(money(value.totalCost))")
        }
    }

    private func antigravityContent(_ value: AntigravityAgentUsage) -> some View {
        Group {
            if let version = displayValue(value.version) { versionLabel(version) }
            if value.claude5h != nil || value.claudeWeekly != nil {
                usageGroup(mark: "C", windows: [("5h", value.claude5h), ("7d", value.claudeWeekly)])
            }
            if value.gemini5h != nil || value.geminiWeekly != nil {
                usageGroup(mark: "G", windows: [("5h", value.gemini5h), ("7d", value.geminiWeekly)])
            }
        }
    }

    private func kimiContent(_ value: KimiAgentUsage) -> some View {
        Group {
            if let version = displayValue(value.version) { versionLabel(version) }
            if let window = value.weekly { usageRing(label: windowLabel(window), window: window) }
            if let window = value.fiveHour { usageRing(label: windowLabel(window), window: window) }
            if let window = value.total {
                usageRing(
                    label: "Σ",
                    percent: window.usedPercent,
                    color: usageColor(window.usedPercent),
                    detail: AgentUsageDetail(
                        title: "累计额度 \(Int(window.usedPercent.rounded()))%",
                        lines: value.parallelLimit.map { ["并发上限 \($0)"] } ?? []
                    )
                )
            }
        }
    }

    private func qoderContent(_ value: QoderAgentUsage) -> some View {
        Group {
            if let version = displayValue(value.version) { versionLabel(version) }
            Button {
                var lines: [String] = []
                if let plan = displayValue(value.planType) {
                    lines.append(plan.split(separator: "_").map { $0.capitalized }.joined(separator: " "))
                }
                if let expiresAt = value.expiresAt, expiresAt > 0 {
                    lines.append("\(formatDate(expiresAt, includesDate: true)) 到期")
                }
                if value.quotaExceeded { lines.append("额度已用尽") }
                detail = AgentUsageDetail(
                    title: "已用 \(credits(value.used)) / \(credits(value.total))",
                    lines: lines
                )
            } label: {
                HStack(spacing: 4) {
                    Image(systemName: "dollarsign.circle")
                        .font(.system(size: 11, weight: .medium))
                    Text(credits(value.used))
                        .fontWeight(.bold)
                        .foregroundStyle(usageColor(value.usedPercent))
                    Text("/ \(credits(value.total))")
                        .foregroundStyle(QuartetTheme.secondaryText.opacity(0.75))
                    GeometryReader { proxy in
                        ZStack(alignment: .leading) {
                            Capsule().fill(QuartetTheme.divider)
                            Capsule()
                                .fill(usageColor(value.usedPercent))
                                .frame(width: proxy.size.width * min(max(value.usedPercent, 0), 100) / 100)
                        }
                    }
                    .frame(width: 38, height: 4)
                }
                .font(.quartet(.compact))
                .foregroundStyle(QuartetTheme.secondaryText)
            }
            .buttonStyle(.plain)
            .accessibilityLabel("Qoder 已用 \(credits(value.used))，总额度 \(credits(value.total))")
        }
    }

    private func versionLabel(_ value: String) -> some View {
        Text(value)
            .font(.quartet(.compact, weight: .medium, design: .monospaced))
            .foregroundStyle(QuartetTheme.secondaryText.opacity(0.74))
            .lineLimit(1)
            .accessibilityLabel("Agent 版本 \(value)")
    }

    private func usageMetric(icon: String, text: String, emphasis: Color, label: String) -> some View {
        HStack(spacing: 3) {
            Image(systemName: icon)
                .font(.system(size: 10, weight: .medium))
            Text(text)
                .fontWeight(.bold)
                .foregroundStyle(emphasis)
        }
        .font(.quartet(.compact, design: .monospaced))
        .foregroundStyle(QuartetTheme.secondaryText)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(label)
    }

    private func usageGroup(mark: String, windows: [(String, AgentUsageWindow?)]) -> some View {
        HStack(spacing: 2) {
            Text(mark)
                .font(.system(size: 9, weight: .bold, design: .monospaced))
                .foregroundStyle(QuartetTheme.secondaryText)
                .padding(.trailing, 2)
            ForEach(Array(windows.enumerated()), id: \.offset) { _, item in
                if let window = item.1 { usageRing(label: item.0, window: window) }
            }
        }
        .padding(2)
        .background(QuartetTheme.elevated.opacity(0.8), in: RoundedRectangle(cornerRadius: 6, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 6, style: .continuous).stroke(QuartetTheme.divider, lineWidth: 1))
        .accessibilityElement(children: .contain)
    }

    private func usageRing(label: String, window: AgentUsageWindow) -> some View {
        usageRing(
            label: label,
            percent: window.usedPercent,
            color: usageColor(window.usedPercent),
            detail: AgentUsageDetail(
                title: "\(label) \(Int(window.usedPercent.rounded()))%",
                lines: ["\(formatReset(window)) 重置"]
            )
        )
    }

    private func usageRing(label: String, percent: Double, color: Color, detail value: AgentUsageDetail) -> some View {
        Button { detail = value } label: {
            ZStack {
                Circle()
                    .stroke(QuartetTheme.divider, lineWidth: 3)
                Circle()
                    .trim(from: 0, to: min(max(percent, 0), 100) / 100)
                    .stroke(color, style: StrokeStyle(lineWidth: 3, lineCap: .round))
                    .rotationEffect(.degrees(-90))
                Text(label)
                    .font(.system(size: 8, weight: .semibold, design: .monospaced))
                    .foregroundStyle(color)
                    .minimumScaleFactor(0.7)
            }
            .frame(width: 22, height: 22)
        }
        .buttonStyle(.plain)
        .accessibilityLabel("\(value.title)，\(value.lines.joined(separator: "，"))")
    }

    private func restoreCache() {
        requestError = nil
        usage = provider.flatMap {
            AgentUsageCache.usage(provider: $0, namespace: appModel.serverAddress)
        }
        version = provider == nil
            ? AgentUsageCache.version(command: command, namespace: appModel.serverAddress)
            : ""
    }

    private func refresh() async {
        loading = true
        requestError = nil
        defer { loading = false }

        do {
            if let provider {
                let response = try await appModel.apiClient().agentUsage(provider: provider.rawValue)
                try Task.checkCancellation()
                usage = response
                AgentUsageCache.setUsage(response, provider: provider, namespace: appModel.serverAddress)
            } else {
                let response = try await appModel.apiClient().agentVersion(command: command)
                try Task.checkCancellation()
                version = response.version?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
                AgentUsageCache.setVersion(version, command: command, namespace: appModel.serverAddress)
            }
        } catch is CancellationError {
            return
        } catch let error as APIError {
            requestError = error
        } catch {
            requestError = APIError(summary: "Agent 用量加载失败", detail: String(describing: error))
        }
    }

    private func usageColor(_ percent: Double) -> Color {
        if percent >= 80 { return QuartetTheme.failed }
        if percent >= 50 { return QuartetTheme.warning }
        return QuartetTheme.success
    }

    private func windowLabel(_ window: AgentUsageWindow) -> String {
        let seconds = window.limitWindowSeconds
        if seconds > 0, seconds % 86_400 == 0 { return "\(seconds / 86_400)d" }
        if seconds > 0, seconds % 3_600 == 0 { return "\(seconds / 3_600)h" }
        if seconds > 0, seconds % 60 == 0 { return "\(seconds / 60)m" }
        return "\(max(0, seconds))s"
    }

    private func formatReset(_ window: AgentUsageWindow) -> String {
        let seconds = window.resetAt > 0
            ? TimeInterval(window.resetAt)
            : Date().timeIntervalSince1970 + TimeInterval(window.resetAfterSeconds)
        return formatDate(Int64(seconds), includesDate: window.limitWindowSeconds >= 86_400)
    }

    private func formatDate(_ unixSeconds: Int64, includesDate: Bool) -> String {
        let formatter = DateFormatter()
        formatter.locale = Locale(identifier: "zh_CN")
        formatter.dateFormat = includesDate ? "MM-dd HH:mm" : "HH:mm"
        return formatter.string(from: Date(timeIntervalSince1970: TimeInterval(unixSeconds)))
    }

    private func money(_ value: Double) -> String { String(format: "$%.2f", value) }

    private func credits(_ value: Double) -> String {
        value.rounded() == value ? String(Int64(value)) : String(format: "%.1f", value)
    }

    private func displayValue(_ value: String?) -> String? {
        guard let value = value?.trimmingCharacters(in: .whitespacesAndNewlines), !value.isEmpty else { return nil }
        return value
    }
}

private struct WrappingHStack: Layout {
    let spacing: CGFloat

    func sizeThatFits(
        proposal: ProposedViewSize,
        subviews: Subviews,
        cache: inout ()
    ) -> CGSize {
        let maxWidth = proposal.width ?? .greatestFiniteMagnitude
        let result = layout(subviews: subviews, maxWidth: maxWidth)
        return CGSize(width: result.width, height: result.height)
    }

    func placeSubviews(
        in bounds: CGRect,
        proposal: ProposedViewSize,
        subviews: Subviews,
        cache: inout ()
    ) {
        let result = layout(subviews: subviews, maxWidth: bounds.width)
        for item in result.items {
            subviews[item.index].place(
                at: CGPoint(x: bounds.minX + item.origin.x, y: bounds.minY + item.origin.y),
                anchor: .topLeading,
                proposal: ProposedViewSize(item.size)
            )
        }
    }

    private func layout(subviews: Subviews, maxWidth: CGFloat) -> (items: [Item], width: CGFloat, height: CGFloat) {
        var items: [Item] = []
        var x: CGFloat = 0
        var y: CGFloat = 0
        var rowHeight: CGFloat = 0
        var usedWidth: CGFloat = 0

        for index in subviews.indices {
            var size = subviews[index].sizeThatFits(ProposedViewSize(width: maxWidth, height: nil))
            size.width = min(size.width, maxWidth)
            if x > 0, x + size.width > maxWidth {
                x = 0
                y += rowHeight + spacing
                rowHeight = 0
            }
            items.append(Item(index: index, origin: CGPoint(x: x, y: y), size: size))
            usedWidth = max(usedWidth, x + size.width)
            rowHeight = max(rowHeight, size.height)
            x += size.width + spacing
        }
        return (items, usedWidth, items.isEmpty ? 0 : y + rowHeight)
    }

    private struct Item {
        let index: Int
        let origin: CGPoint
        let size: CGSize
    }
}

private struct ChatBubble: View {
    @EnvironmentObject private var appModel: AppModel
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

    private var displayContent: String {
        if message.content == "[image]", !message.imagePaths.isEmpty { return "" }
        if message.content == "[file]", !message.fileAttachments.isEmpty { return "" }
        return message.content
    }

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

private struct AssistantMessageCard: View {
    let message: ChatMessage
    let agentName: String
    let agentIconUrl: String?

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
                                .font(.quartet(.control))
                                .accessibilityHidden(true)
                        } else {
                            AgentIdentityIcon(iconUrl: agentIconUrl)
                        }
                        Text(message.isShellOutput ? "Shell" : agentName)
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

private struct AgentIdentityIcon: View {
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
                    .font(.quartet(.control))
            } else {
                Image(systemName: "sparkles")
                    .font(.quartet(.detail, weight: .semibold))
            }
        }
        .frame(width: 20, height: 20)
        .clipShape(RoundedRectangle(cornerRadius: 5, style: .continuous))
        .accessibilityHidden(true)
        .task(id: iconUrl) {
            image = nil
            guard textIcon == nil, let iconUrl, !iconUrl.isEmpty else { return }
            guard let data = try? await appModel.apiClient().fileData(path: iconUrl) else { return }
            image = UIImage(data: data)
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
                if isStreaming { StreamingDot(color: QuartetTheme.accent) }
                Spacer(minLength: 8)
                if let timestamp, !isStreaming {
                    Text(chatTimeLabel(timestamp))
                        .font(.quartet(.compact))
                        .foregroundStyle(QuartetTheme.secondaryText)
                }
            }
            .foregroundStyle(QuartetTheme.accent)

            MarkdownMessageView(text: text, tone: .thought)
        }
        .padding(.horizontal, 16)
        .padding(.vertical, 13)
        .background(QuartetTheme.accent.opacity(0.075), in: RoundedRectangle(cornerRadius: 13, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 13, style: .continuous).stroke(QuartetTheme.accent.opacity(0.24), lineWidth: 1))
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
                    Text(toolIcon)
                        .font(.quartet(.control))
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
        let mappings = [
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
            ("browser_type", "⌨️"), ("browser_wait_visible", "👁️")
        ]
        if let exact = mappings.first(where: { $0.0 == displayName }) { return exact.1 }
        return mappings.first(where: { displayName.hasPrefix($0.0) })?.1 ?? "💻"
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

private struct OutboxBubble: View {
    let item: LocalOutboxItem

    var body: some View {
        VStack(alignment: .trailing, spacing: 8) {
            HStack(spacing: 7) {
                Text("YOU")
                Text(item.statusTitle)
            }
            .font(.quartet(.compact, weight: .bold, design: .monospaced))
            .foregroundStyle(item.isFailed ? QuartetTheme.failed : QuartetTheme.onAccent.opacity(0.76))

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
        .background(QuartetTheme.accent, in: RoundedRectangle(cornerRadius: 16))
        .overlay(RoundedRectangle(cornerRadius: 16).stroke(item.isFailed ? QuartetTheme.failed.opacity(0.6) : QuartetTheme.onAccent.opacity(0.16), lineWidth: 1))
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

private struct ServerQueueRow: View {
    let index: Int
    let item: QueuedJobMessage
    let deleting: Bool
    let onShowError: () -> Void
    let onDelete: () -> Void

    var body: some View {
        HStack(spacing: 9) {
            Text("\(index)")
                .font(.quartet(.compact, design: .monospaced))
                .foregroundStyle(QuartetTheme.secondaryText)
            Text(item.summaryLine)
                .font(.quartet(.detail))
                .foregroundStyle(item.state == "blocked" ? QuartetTheme.failed : QuartetTheme.primaryText)
                .lineLimit(1)
            Spacer(minLength: 6)
            if !item.imagePaths.isEmpty {
                Image(systemName: "photo")
                    .font(.quartet(.compact))
                    .foregroundStyle(QuartetTheme.secondaryText)
            }
            if !item.fileAttachments.isEmpty {
                Image(systemName: "paperclip")
                    .font(.quartet(.compact))
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
                    else { Image(systemName: "xmark").font(.quartet(.compact, weight: .bold)) }
                }.frame(width: 30, height: 30)
            }
            .buttonStyle(.plain)
            .disabled(deleting)
            .accessibilityLabel("删除排队消息")
        }
        .padding(.leading, 12)
        .padding(.trailing, 8)
        .padding(.vertical, 7)
        .overlay(alignment: .bottom) { Divider().overlay(QuartetTheme.divider) }
        .accessibilityHint(item.error ?? "等待发送")
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
                            .fill(tone == .user ? QuartetTheme.onAccent.opacity(0.42) : QuartetTheme.secondaryText.opacity(0.5))
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
                    .foregroundStyle(codeSecondaryForeground)
                Spacer()
                Button("复制代码") {
                    UIPasteboard.general.string = code
                }
                .font(.quartet(.detail))
            }

            ScrollView(.horizontal, showsIndicators: false) {
                Text(code)
                    .font(.quartet(.detail, design: .monospaced))
                    .foregroundStyle(codeForeground)
                    .textSelection(.enabled)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
        .padding(12)
        .background(codeBackground, in: RoundedRectangle(cornerRadius: 10))
        .overlay(RoundedRectangle(cornerRadius: 10).stroke(codeBorder, lineWidth: 1))
    }

    private var codeForeground: Color {
        tone == .user ? tone.foreground : QuartetTheme.terminalText
    }

    private var codeSecondaryForeground: Color {
        tone == .user ? tone.secondaryForeground : QuartetTheme.terminalGreenMuted
    }

    private var codeBackground: Color {
        tone == .user ? tone.codeBackground : QuartetTheme.terminalBackground
    }

    private var codeBorder: Color {
        tone == .user ? tone.codeBorder : QuartetTheme.terminalBorder
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
        case .user: QuartetTheme.onAccent
        case .thought: QuartetTheme.primaryText.opacity(0.82)
        case .standard, .tool: QuartetTheme.primaryText
        }
    }

    var secondaryForeground: Color {
        self == .user ? QuartetTheme.onAccent.opacity(0.68) : QuartetTheme.secondaryText
    }

    var codeBackground: Color {
        switch self {
        case .user: QuartetTheme.onAccent.opacity(0.08)
        case .thought: QuartetTheme.accent.opacity(0.07)
        case .standard, .tool: QuartetTheme.elevated.opacity(0.72)
        }
    }

    var codeBorder: Color {
        self == .user ? QuartetTheme.onAccent.opacity(0.16) : QuartetTheme.divider
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

private struct AuthenticatedFile: View {
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
                    Image(systemName: "doc.fill").font(.quartet(.control, weight: .semibold))
                }
                .frame(width: 40, height: 44)
                VStack(alignment: .leading, spacing: 3) {
                    Text(attachment.name).font(.quartet(.control, weight: .semibold)).lineLimit(1)
                    if !fileMeta.isEmpty {
                        Text(fileMeta).font(.quartet(.compact)).foregroundStyle(QuartetTheme.onAccent.opacity(0.68)).lineLimit(1)
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
    var remoteFileAttachments: [FileAttachment]
    var state: State

    var displayText: String {
        let trimmed = draft.text.trimmingCharacters(in: .whitespacesAndNewlines)
        if trimmed.isEmpty, let attachment = draft.attachment { return attachment.isImage ? "[image]" : "[file]" }
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
        if case .awaitingEcho = state { return true }
        return false
    }

    var summaryLine: String {
        let trimmed = draft.text.trimmingCharacters(in: .whitespacesAndNewlines)
        if !trimmed.isEmpty { return trimmed }
        if let attachment = draft.attachment { return attachment.isImage ? "[image]" : "[file]" }
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
    @Published var serverQueue = MessageQueueSnapshot(jobId: "", version: 0, paused: false, pauseReason: nil, willContinue: false, active: nil, items: [])
    @Published var deletingQueueIDs: Set<String> = []
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
    @Published private var agentIconUrl: String?
    @Published private var modeID: String?
    @Published private var thoughtLevelID: String?
    private var lastEventID: UInt64 = 0
    private var lastGraphEventID: UInt64 = 0
    private var streamTask: Task<Void, Never>?
    private var graphReconcileTask: Task<Void, Never>?
    private var graphMonitorTask: Task<Void, Never>?
    private var didSeedInitialDraft = false
    private var knownQueuedItems: [String: QueuedJobMessage] = [:]
    private var agentDisplayInfoByReference: [String: AgentDisplayInfo] = [:]
    private var accumulatedRoundBoundaries: Set<String> = []
    private var isTurnRunning = false
    private var isProcessingOutbox = false
    private var isGraph = false
    private var graphRunLive = false

    var isRunning: Bool { isTurnRunning }
    var expectsExecution: Bool {
        if isTurnRunning { return true }
        return outbox.contains { item in
            switch item.state {
            case .queued, .uploading, .sending, .awaitingEcho:
                return true
            case .failed:
                return false
            }
        }
    }
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
            case .queued, .uploading, .sending:
                return !optimisticMessageIDs.contains(item.id)
            case .failed:
                return true
            case .awaitingEcho:
                return false
            }
        }
    }
    var agentDisplayLabel: String {
        displayValue(agentDisplayName) ?? displayValue(agentType) ?? "未指定 Agent"
    }
    var agentDisplayIconUrl: String? { displayValue(agentIconUrl) }
    var agentRuntimeType: String? { displayValue(agentType) }
    var configurationSessionID: String? { displayValue(preferredSessionID) ?? displayValue(sessionID) }
    var modelIDForDisplay: String? { displayValue(modelID) }
    var agentReferenceForDisplay: String? { displayValue(agentType) }
    var modeIDForDisplay: String? { displayValue(modeID) }
    var thoughtLevelIDForDisplay: String? { displayValue(thoughtLevelID) }
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

    func applyACPConfiguration(
        _ response: SetACPConfigResponse,
        target: ACPConfigTarget,
        selectedModelID: String?,
        selectedThoughtLevelID: String?
    ) {
        if let models = response.models {
            modelID = displayValue(models.currentModelId) ?? modelID
        } else if target == .model {
            modelID = displayValue(selectedModelID) ?? modelID
        }

        if let thoughtLevels = response.thoughtLevels {
            thoughtLevelID = displayValue(thoughtLevels.currentThoughtLevelId)
        } else if target == .model {
            thoughtLevelID = nil
        } else if target == .thoughtLevel {
            thoughtLevelID = displayValue(selectedThoughtLevelID) ?? thoughtLevelID
        }
    }

    func start(route: ChatRoute, client: APIClient) async {
        stopStreaming()
        let changesJob = !jobID.isEmpty && jobID != route.summary.id
        if changesJob {
            messages = []
            outbox = []
            serverQueue = MessageQueueSnapshot(jobId: route.summary.id, version: 0, paused: false, pauseReason: nil, willContinue: false, active: nil, items: [])
            sessionID = nil
            agentDisplayName = nil
            agentIconUrl = nil
            agentDisplayInfoByReference = [:]
            totalTokens = 0
            runStartedAt = nil
            runFinishedAt = nil
            accumulatedDurationMs = 0
            accumulatedRoundBoundaries = []
            knownQueuedItems = [:]
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
            if !isGraph {
                do {
                    applyServerQueue(try await client.messageQueue(jobID: jobID))
                } catch {
                    errorDetail = errorText(error)
                }
            }
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
                || serverQueue.willContinue

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
            if let reference = displayValue(agentType),
               let agentInfo = await resolveAgentDisplayInfo(reference: reference) {
                agentDisplayName = resolvedAgentName(agentInfo)
                agentIconUrl = displayValue(agentInfo.iconUrl)
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
            if !isGraph { applyServerQueue(serverQueue) }

            loading = false
            if isTurnRunning || sessionID != nil || route.initialMessage != nil || route.initialAttachment != nil || route.initialImagePaths != nil || route.initialFileAttachments != nil {
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
        agentIconUrl = "✨"
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
        remoteFileAttachments: [FileAttachment] = [],
        isInitialDraft: Bool = false
    ) -> String? {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty || attachment != nil || !remoteImagePaths.isEmpty || !remoteFileAttachments.isEmpty else { return nil }
        let item = LocalOutboxItem(
            id: UUID().uuidString.lowercased(),
            draft: ComposerDraft(text: trimmed, attachment: attachment),
            createdAt: Int64(Date().timeIntervalSince1970 * 1_000),
            isInitialDraft: isInitialDraft,
            requestContext: currentRequestContext(bypassCommand: isInitialDraft),
            remoteImagePaths: remoteImagePaths,
            remoteFileAttachments: remoteFileAttachments,
            state: .queued
        )
        outbox.append(item)
        if isInitialDraft && (attachment == nil || !remoteImagePaths.isEmpty || !remoteFileAttachments.isEmpty) {
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

    func deleteQueuedMessage(id: String) async {
        guard let client else { return }
        guard deletingQueueIDs.insert(id).inserted else { return }
        defer { deletingQueueIDs.remove(id) }
        do {
            applyServerQueue(try await client.deleteQueuedMessage(jobID: jobID, messageID: id))
        } catch {
            errorDetail = errorText(error)
        }
    }

    func showQueueError(_ item: QueuedJobMessage) {
        guard let detail = item.error, !detail.isEmpty else { return }
        errorDetail = detail
    }

    func continueQueue() async {
        guard let client else { return }
        do {
            applyServerQueue(try await client.continueMessageQueue(jobID: jobID))
            if serverQueue.willContinue {
                isTurnRunning = true
                startStreaming()
            }
        } catch {
            errorDetail = errorText(error)
        }
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
            remoteFileAttachments: item.remoteFileAttachments,
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
        Task { [weak self] in
            guard let self, let client = self.client else { return }
            do { self.applyServerQueue(try await client.messageQueue(jobID: self.jobID)) }
            catch { self.errorDetail = self.errorText(error) }
            self.scheduleOutboxProcessing()
        }
    }

    private func seedInitialDraftIfNeeded(route: ChatRoute) -> String? {
        guard !didSeedInitialDraft else { return nil }
        let hasInitialContent = (route.initialMessage?.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty == false)
            || route.initialAttachment != nil
            || !(route.initialImagePaths ?? []).isEmpty
            || !(route.initialFileAttachments ?? []).isEmpty
        guard hasInitialContent else { return nil }

        didSeedInitialDraft = true
        return enqueueDraft(
            text: route.initialMessage?.trimmingCharacters(in: .whitespacesAndNewlines) ?? "",
            attachment: route.initialAttachment,
            remoteImagePaths: route.initialImagePaths ?? [],
            remoteFileAttachments: route.initialFileAttachments ?? [],
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
        let agentInfo = await resolveAgentDisplayInfo(for: response)
        applySessionMetadata(response, agentInfo: agentInfo)
        let historyMessages = convertHistoryMessages(response.messages, agentInfo: agentInfo)
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
        let nonEmptySessionIDs = sessionIDs.filter { !$0.isEmpty }
        for (index, currentSessionID) in nonEmptySessionIDs.enumerated() {
            let response = try await client.sessionMessages(id: currentSessionID)
            let agentInfo = await resolveAgentDisplayInfo(for: response)
            let isLatestSession = index == nonEmptySessionIDs.count - 1
            combined.append(contentsOf: convertHistoryMessages(
                response.messages,
                idPrefix: isLatestSession ? nil : currentSessionID,
                agentInfo: agentInfo
            ))
            if isLatestSession {
                applySessionMetadata(response, agentInfo: agentInfo)
            }
        }
        messages = combined
        removeEchoedOutboxItems()
        bumpScrollAnchor()
    }

    // Match the Web client's history projection: assistant tool-call metadata
    // creates the card, then the later role=tool row fills its result/status.
    // Keeping this pairing structured is important because a single free-form
    // detail string cannot distinguish a tool name from streamed arguments.
    private func convertHistoryMessages(
        _ history: [HistoryMessage],
        idPrefix: String? = nil,
        agentInfo: AgentDisplayInfo? = nil
    ) -> [ChatMessage] {
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
        for index in converted.indices where converted[index].kind == .assistant {
            converted[index].agentDisplayName = resolvedAgentName(agentInfo)
            converted[index].agentIconUrl = displayValue(agentInfo?.iconUrl)
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
        guard !loading, !sending else { return }
        // Graph messages retain their existing client-side serialization. The
        // durable server queue only applies to ordinary interactive Jobs.
        if isGraph && isTurnRunning { return }
        guard let index = outbox.firstIndex(where: {
            if case .queued = $0.state { return true }
            return false
        }) else { return }

        isProcessingOutbox = true
        defer {
            isProcessingOutbox = false
            if outbox.contains(where: { if case .queued = $0.state { return true }; return false }) {
                scheduleOutboxProcessing()
            }
        }
        await dispatchOutboxItem(at: index)
    }

    private func dispatchOutboxItem(at index: Int) async {
        guard outbox.indices.contains(index), let client else { return }
        let itemID = outbox[index].id
        archiveFinishedRoundIfNeeded()
        sending = true
        defer { sending = false }

        do {
            outbox[index].state = .sending
            if outbox[index].attachment == nil || !outbox[index].remoteImagePaths.isEmpty || !outbox[index].remoteFileAttachments.isEmpty {
                upsertOptimisticUserMessage(for: outbox[index])
            }
            startStreaming()
            try await waitForStreamReady()

            if let attachment = outbox[index].attachment, outbox[index].remoteImagePaths.isEmpty, outbox[index].remoteFileAttachments.isEmpty {
                outbox[index].state = .uploading
                let uploaded = try await client.uploadFile(
                    data: attachment.data,
                    filename: attachment.filename,
                    mimeType: attachment.mimeType
                )
                guard let refreshed = outbox.firstIndex(where: { $0.id == itemID }) else { return }
                if attachment.isImage {
                    outbox[refreshed].remoteImagePaths = [uploaded.path]
                } else {
                    outbox[refreshed].remoteFileAttachments = [uploaded]
                }
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
                        imageUrls: item.remoteImagePaths.isEmpty ? nil : item.remoteImagePaths,
                        fileAttachments: item.remoteFileAttachments.isEmpty ? nil : item.remoteFileAttachments
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
        if response.status == "queued" {
            if let current = outbox.first(where: { $0.id == itemID }), case .awaitingEcho = current.state {
                return
            }
            if let queue = response.queue { applyServerQueue(queue) }
            messages.removeAll { $0.id == itemID && $0.isOptimistic }
            outbox.removeAll { $0.id == itemID }
            isTurnRunning = serverQueue.willContinue
            bumpScrollAnchor()
            return
        }
        if response.status == "deleted" {
            if let queue = response.queue { applyServerQueue(queue) }
            messages.removeAll { $0.id == itemID && $0.isOptimistic }
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
        if response.messageState == "queued" || response.messageState == "blocked" || response.messageState == "deleted" {
            if let current = outbox.first(where: { $0.id == itemID }), case .awaitingEcho = current.state {
                return
            }
            if let queue = response.queue { applyServerQueue(queue) }
            messages.removeAll { $0.id == itemID && $0.isOptimistic }
            outbox.removeAll { $0.id == itemID }
            isTurnRunning = serverQueue.willContinue
            bumpScrollAnchor()
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
            isTurnRunning = serverQueue.willContinue
            if isGraph { stopStreaming() }
            scheduleOutboxProcessing()
            bumpScrollAnchor()
        case "queued", "blocked":
            if let queue = response.queue { applyServerQueue(queue) }
            messages.removeAll { $0.id == itemID && $0.isOptimistic }
            outbox.removeAll { $0.id == itemID }
            isTurnRunning = serverQueue.willContinue
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
            isTurnRunning = serverQueue.willContinue
            if isGraph { stopStreaming() }
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
            messages[index].fileAttachments = item.remoteFileAttachments
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
            fileAttachments: item.remoteFileAttachments,
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
        if !isGraph {
            do { applyServerQueue(try await client.messageQueue(jobID: jobID)) }
            catch { errorDetail = errorText(error) }
        }
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
        if !isGraph { applyServerQueue(serverQueue) }
        isTurnRunning = graphRunLive
            || snapshot.status == "running"
            || (snapshot.status == "pending" && hasPriorConversation(snapshot))
            || serverQueue.willContinue
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
                let queued = knownQueuedItems[clientMessageID]
                if let queued,
                   !messages.contains(where: { $0.id == clientMessageID }) {
                    messages.append(ChatMessage(
                        id: queued.id, kind: .user, content: queued.summaryLine, detail: nil,
                        isFinished: true, isFailed: false, timestamp: event.timestamp,
                        imagePaths: queued.imagePaths, fileAttachments: queued.fileAttachments
                    ))
                }
                if serverQueue.items.contains(where: { $0.id == clientMessageID }) {
                    serverQueue = MessageQueueSnapshot(
                        jobId: serverQueue.jobId, version: serverQueue.version, paused: serverQueue.paused,
                        pauseReason: serverQueue.pauseReason, willContinue: true,
                        active: queued,
                        items: serverQueue.items.filter { $0.id != clientMessageID }
                    )
                }
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
        case "JOB_STOPPED":
            status = "stopped"
            isTurnRunning = false
            runFinishedAt = event.timestamp ?? runFinishedAt
            let outcome = event.runOutcome ?? "stopped"
            publishTerminalStateChange()
            applyRunOutcome(outcome)
            finishOpenMessages(outcome: outcome, timestamp: event.timestamp)
            scheduleSnapshotRefresh()
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
        case "message_queue_changed":
            if let version = event.value?.version, version > serverQueue.version, let client {
                Task { [weak self] in
                    guard let self else { return }
                    do { self.applyServerQueue(try await client.messageQueue(jobID: self.jobID)) }
                    catch { self.errorDetail = self.errorText(error) }
                }
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

    private func applyServerQueue(_ snapshot: MessageQueueSnapshot) {
        guard snapshot.jobId == jobID else { return }
        if serverQueue.jobId == jobID, snapshot.version < serverQueue.version { return }
        for item in snapshot.items { knownQueuedItems[item.id] = item }
        if let active = snapshot.active {
            knownQueuedItems[active.id] = active
            if !messages.contains(where: { $0.id == active.id }) {
                messages.append(ChatMessage(
                    id: active.id, kind: .user, content: active.summaryLine, detail: nil,
                    isFinished: true, isFailed: false, timestamp: active.createdAt,
                    imagePaths: active.imagePaths, fileAttachments: active.fileAttachments, isOptimistic: true
                ))
            }
        }
        serverQueue = snapshot
        isTurnRunning = snapshot.willContinue || status == "running" || graphRunLive
    }

    private func scheduleSnapshotRefresh() {
        guard client != nil else { return }
        Task { [weak self] in
            guard let self else { return }
            do {
                try await self.refreshSnapshotAndHistory()
                self.reconcileAwaitingEchoIfNeeded()
                self.scheduleOutboxProcessing()
                if self.isGraph && !self.serverQueue.willContinue { self.stopStreaming() }
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

    private func applySessionMetadata(_ response: SessionMessagesResponse, agentInfo: AgentDisplayInfo?) {
        modelID = response.modelId
        agentType = response.type
        modeID = response.acpMode
        thoughtLevelID = response.acpThoughtLevel
        totalTokens = response.tokenUsage?.totalTokens ?? totalTokens
        agentDisplayName = resolvedAgentName(agentInfo)
        agentIconUrl = displayValue(agentInfo?.iconUrl)
    }

    private func resolveAgentDisplayInfo(for response: SessionMessagesResponse) async -> AgentDisplayInfo? {
        guard let reference = displayValue(response.type) else { return nil }
        if let embedded = response.agents?[reference] {
            agentDisplayInfoByReference[reference] = embedded
            return embedded
        }
        return await resolveAgentDisplayInfo(reference: reference)
    }

    private func resolveAgentDisplayInfo(reference: String) async -> AgentDisplayInfo? {
        if let cached = agentDisplayInfoByReference[reference] { return cached }
        guard let client else { return nil }
        do {
            let info = try await client.resolveAgentDisplayInfo(ids: [reference]).agents[reference]
            if let info { agentDisplayInfoByReference[reference] = info }
            return info
        } catch {
            errorDetail = errorText(error)
            return nil
        }
    }

    private func resolvedAgentName(_ info: AgentDisplayInfo?) -> String? {
        guard let info, let name = displayValue(info.displayName) else { return nil }
        return info.deleted ? "\(name)（已删除）" : name
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

