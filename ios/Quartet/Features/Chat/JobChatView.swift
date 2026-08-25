import AVFoundation
import PhotosUI
import SwiftUI
import UIKit

private enum ChatConfigurationPicker: String, Identifiable {
    case model
    case thoughtLevel

    var id: String { rawValue }
    var title: String {
        switch self {
        case .model: "选择模型".localizedForApp
        case .thoughtLevel: "选择思考等级".localizedForApp
        }
    }
}

private struct ChatConfigurationOption: Identifiable {
    let id: String
    let name: String
    let detail: String?
}

struct JobChatView: View {
    @EnvironmentObject private var appModel: AppModel
    @Environment(\.scenePhase) private var scenePhase
    @StateObject private var chat = ChatViewModel()
    let route: ChatRoute

    @State private var draft = ""
    @State private var selectedPhoto: PhotosPickerItem?
    @State private var showsPhotoPicker = false
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
    @State private var agentPreferences: [String: AgentPreferences] = [:]
    @State private var changingACPConfiguration = false
    @State private var configurationPicker: ChatConfigurationPicker?
    @State private var gitBranch = ""
    @State private var linkOpener = ChatLinkOpener()
    @State private var webDestination: ChatWebDestination?
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
        .task(id: route.summary.id) {
            do {
                agentPreferences = try await appModel.agentPreferences()
            } catch is CancellationError {
                return
            } catch {
                agentPreferences = [:]
                appModel.present(error)
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
        .photosPicker(isPresented: $showsPhotoPicker, selection: $selectedPhoto, matching: .images)
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
            Button("关闭", role: .cancel) {}
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
            Text((chat.serverQueue.items.isEmpty
                ? "正在执行的 Agent 将收到停止请求。"
                : "正在执行的 Agent 将收到停止请求，后续排队消息会保留并暂停，需手动继续。").localizedForApp)
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
        .sheet(item: $configurationPicker) { picker in
            ChatConfigurationSelectionSheet(
                title: picker.title,
                options: configurationOptions(for: picker),
                selectedID: configurationSelectionID(for: picker),
                favoriteIDs: picker == .model ? favoriteModelIDs : [],
                onSelect: { id in
                    configurationPicker = nil
                    Task {
                        switch picker {
                        case .model:
                            await selectModel(id)
                        case .thoughtLevel:
                            await selectThoughtLevel(id)
                        }
                    }
                }
            )
            .presentationDetents([.medium, .large])
            .quartetSheetStyle()
        }
        .fullScreenCover(item: $webDestination) { destination in
            NavigationStack {
                ChatWebViewPage(
                    destination: destination,
                    onError: { appModel.present($0) }
                )
            }
            .quartetSheetStyle()
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
                                .font(.chat(.detail))
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
                            .equatable()
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
                                .font(.chat(.control, weight: .medium))
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
            // 链接拦截统一在列表这一层注入，动作由 `linkOpener` 持有、全程同一个值。
            .environment(\.openURL, linkOpener.action)
            .onAppear {
                linkOpener.presentError = { [appModel] error in appModel.present(error) }
                linkOpener.presentDestination = { destination in webDestination = destination }
                configureLinkOpener()
            }
            .onChange(of: workspaceContextKey) { _, _ in configureLinkOpener() }
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
                // 流式输出时锚点会持续 bump，动画会一层层叠加成抖动；跟随滚动直接无动画。
                if chat.isRunning {
                    proxy.scrollTo("chat-bottom", anchor: .bottom)
                } else {
                    withAnimation(.easeOut(duration: 0.2)) {
                        proxy.scrollTo("chat-bottom", anchor: .bottom)
                    }
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
                    .font(.chat(.detail))
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
                                .font(.chat(.detail, weight: .semibold))
                                .foregroundStyle(QuartetTheme.secondaryText)
                            Spacer()
                            if chat.serverQueue.pauseReason != "blocked" {
                                Button("继续队列") { Task { await chat.continueQueue() } }
                                    .font(.chat(.detail, weight: .semibold))
                            }
                        }
                        .padding(.horizontal, 12)
                        .padding(.vertical, 9)
                    }
                    ScrollView {
                        VStack(spacing: 0) {
                            ForEach(Array(chat.serverQueue.items.enumerated()), id: \.element.id) { index, item in
                                ServerQueueRow(
                                    index: index + 1, item: item,
                                    showsDivider: index < chat.serverQueue.items.count - 1,
                                    deleting: chat.deletingQueueIDs.contains(item.id),
                                    onShowError: { chat.showQueueError(item) },
                                    onDelete: { Task { await chat.deleteQueuedMessage(id: item.id) } }
                                )
                            }
                        }
                    }
                    // 队列面板必须按行数收缩：ScrollView 在竖直方向是贪心的，只写 maxHeight
                    // 会让一条排队消息也撑满上限，在输入框上方留出一大片空白。fixedSize 让它
                    // 先取内容理想高度，再由 maxHeight 截断成可滚动列表。
                    .frame(maxHeight: 156)
                    .fixedSize(horizontal: false, vertical: true)
                    .scrollBounceBehavior(.basedOnSize)
                    // 面板底部 10pt 会被下面的输入框卡片盖住（见负 padding），补齐避免裁掉最后一行。
                    .padding(.bottom, 10)
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
                                    .font(.chat(.compact, weight: .bold))
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
                    .font(.chat(.reading))
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
                            .font(.chat(.compact, weight: .semibold))
                            .foregroundStyle(QuartetTheme.secondaryText)
                            .frame(width: 30, height: 30)
                            .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 8, style: .continuous))
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("预置消息与历史")
                    .accessibilityHint("打开后可从分组列表中选择")
                    .accessibilityIdentifier("chat-message-history")

                    Button {
                        composerFocused = false
                        showsAttachmentMenu = true
                    } label: {
                        Image(systemName: "plus")
                            .font(.chat(.compact, weight: .bold))
                            .foregroundStyle(hasPendingAttachment ? QuartetTheme.accent : QuartetTheme.secondaryText)
                            .frame(width: 30, height: 30)
                            .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 8, style: .continuous))
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
                            onPhotoLibrary: {
                                showsAttachmentMenu = false
                                Task { @MainActor in
                                    await Task.yield()
                                    showsPhotoPicker = true
                                }
                            },
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
                                .font(.chat(.compact, weight: .bold))
                                .foregroundStyle(QuartetTheme.onAccent)
                                .frame(width: 30, height: 30)
                                .background(
                                    QuartetTheme.chatStop,
                                    in: RoundedRectangle(cornerRadius: 8, style: .continuous)
                                )
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
                            .font(.chat(.compact, weight: .bold))
                            .foregroundStyle(sendDisabled ? QuartetTheme.secondaryText : QuartetTheme.onAccent)
                            .frame(width: 30, height: 30)
                            .background(
                                sendDisabled ? QuartetTheme.elevated : QuartetTheme.accent,
                                in: RoundedRectangle(cornerRadius: 8, style: .continuous)
                            )
                    }
                    .buttonStyle(.plain)
                    .disabled(sendDisabled)
                    .opacity(chat.sending ? 0.55 : 1)
                    .accessibilityLabel("发送消息")
                    .accessibilityIdentifier("chat-send")
                }
                .frame(maxWidth: .infinity, alignment: .leading)
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
                    .font(.chat(.detail))
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
            Button {
                composerFocused = false
                configurationPicker = .model
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
                Button {
                    composerFocused = false
                    configurationPicker = .thoughtLevel
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
                // 只在运行中挂 TimelineView：运行结束后 `runFinishedAt` 已定，标签与时间无关，
                // 再让它每秒重算会连带整行胶囊（自定义 WrappingHStack Layout）每秒重排一次。
                if chat.isRunning {
                    TimelineView(.periodic(from: .now, by: 1)) { timeline in
                        durationChip(at: timeline.date)
                    }
                } else {
                    durationChip(at: .now)
                }
            }
        }
    }

    private func durationChip(at date: Date) -> some View {
        let label = chat.durationLabel(at: date)
        return ComposerMetadataChip(
            icon: "clock",
            text: label,
            accessibilityLabel: "耗时，\(label)"
        )
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
            Text("\(workspaceName ?? route.summary.workspaceId ?? "—")：")
                .font(.chat(.compact, weight: .semibold))
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
                    .font(.chat(.compact, weight: .semibold))
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
        ) ?? "未指定 Model".localizedForApp
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
        ) ?? "默认模式".localizedForApp
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

    private var selectedAgentPreferences: AgentPreferences? {
        if let selectedAgent {
            return agentPreferences[selectedAgent.agentId] ?? agentPreferences[selectedAgent.type]
        }
        guard let reference = chat.agentReferenceForDisplay else { return nil }
        return agentPreferences[reference]
    }

    private var favoriteModelIDs: Set<String> {
        Set(selectedAgentPreferences?.favoriteModelIDs ?? [])
    }

    private var orderedModels: [AgentModel] {
        let favoriteOrder = selectedAgentPreferences?.favoriteModelIDs ?? []
        guard !favoriteOrder.isEmpty else { return availableModels }

        let modelsByID = Dictionary(
            availableModels.map { ($0.modelId, $0) },
            uniquingKeysWith: { first, _ in first }
        )
        var appended = Set<String>()
        let favorites = favoriteOrder.compactMap { modelID -> AgentModel? in
            guard appended.insert(modelID).inserted else { return nil }
            return modelsByID[modelID]
        }
        return favorites + availableModels.filter { !appended.contains($0.modelId) }
    }

    private var availableThoughtLevels: [AgentOption] {
        configuredThoughtLevels?.availableThoughtLevels
            ?? selectedAgent?.thoughtLevels?.availableThoughtLevels
            ?? []
    }

    private func configurationOptions(for picker: ChatConfigurationPicker) -> [ChatConfigurationOption] {
        switch picker {
        case .model:
            return orderedModels.map {
                ChatConfigurationOption(id: $0.modelId, name: $0.name, detail: $0.description)
            }
        case .thoughtLevel:
            return availableThoughtLevels.map {
                ChatConfigurationOption(id: $0.id, name: $0.name, detail: $0.description)
            }
        }
    }

    private func configurationSelectionID(for picker: ChatConfigurationPicker) -> String? {
        switch picker {
        case .model:
            return chat.modelIDForDisplay
        case .thoughtLevel:
            return chat.thoughtLevelIDForDisplay
        }
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

    private func configureLinkOpener() {
        linkOpener.configure(
            baseURL: try? appModel.apiClient().baseURL,
            workdir: workspaceWorkdir,
            jobID: route.summary.id,
            canReadFiles: appModel.can("file.read")
        )
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

private struct ChatConfigurationSelectionSheet: View {
    @Environment(\.dismiss) private var dismiss

    let title: String
    let options: [ChatConfigurationOption]
    let selectedID: String?
    let favoriteIDs: Set<String>
    let onSelect: (String) -> Void

    private var favoriteOptions: [ChatConfigurationOption] {
        options.filter { favoriteIDs.contains($0.id) }
    }

    private var otherOptions: [ChatConfigurationOption] {
        options.filter { !favoriteIDs.contains($0.id) }
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 12) {
                    if favoriteOptions.isEmpty {
                        optionGroup(options)
                    } else {
                        optionGroup(favoriteOptions, title: "收藏".localizedForApp)
                        if !otherOptions.isEmpty {
                            optionGroup(otherOptions, title: "其他模型".localizedForApp)
                        }
                    }
                }
                .padding(.horizontal, 20)
                .padding(.top, 8)
                .padding(.bottom, 24)
            }
            .background(QuartetTheme.canvas)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .principal) {
                    Text(title)
                        .font(.chat(.regular, weight: .semibold))
                        .foregroundStyle(QuartetTheme.primaryText)
                        .accessibilityAddTraits(.isHeader)
                }
            }
        }
    }

    @ViewBuilder
    private func optionGroup(_ groupOptions: [ChatConfigurationOption], title: String? = nil) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            if let title {
                Text(title)
                    .font(.chat(.detail, weight: .semibold))
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .padding(.horizontal, 4)
            }

            VStack(spacing: 0) {
                ForEach(Array(groupOptions.enumerated()), id: \.element.id) { index, option in
                    optionRow(option)
                    if index < groupOptions.count - 1 {
                        Divider()
                            .overlay(QuartetTheme.divider)
                            .padding(.leading, 56)
                    }
                }
            }
            .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
            .overlay {
                RoundedRectangle(cornerRadius: 16, style: .continuous)
                    .stroke(QuartetTheme.divider.opacity(0.8), lineWidth: 1)
            }
        }
    }

    private func optionRow(_ option: ChatConfigurationOption) -> some View {
        Button {
            onSelect(option.id)
            dismiss()
        } label: {
            HStack(spacing: 12) {
                Image(systemName: optionIcon(for: option))
                    .font(.chat(.regular, weight: .semibold))
                    .foregroundStyle(
                        option.id == selectedID || favoriteIDs.contains(option.id)
                            ? QuartetTheme.accent
                            : QuartetTheme.secondaryText
                    )
                    .frame(width: 28)

                VStack(alignment: .leading, spacing: 3) {
                    Text(option.name)
                        .font(.chat(.control, weight: .semibold))
                        .foregroundStyle(QuartetTheme.primaryText)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    if let detail = option.detail?.trimmingCharacters(in: .whitespacesAndNewlines),
                       !detail.isEmpty {
                        Text(detail)
                            .font(.chat(.detail))
                            .foregroundStyle(QuartetTheme.secondaryText)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                }
            }
            .padding(.horizontal, 14)
            .padding(.vertical, 11)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel(option.name)
        .accessibilityAddTraits(option.id == selectedID ? .isSelected : [])
        .accessibilityIdentifier("chat-configuration-option-\(option.id)")
    }

    private func optionIcon(for option: ChatConfigurationOption) -> String {
        if option.id == selectedID { return "checkmark.circle.fill" }
        if favoriteIDs.contains(option.id) { return "star.fill" }
        return "circle"
    }
}

