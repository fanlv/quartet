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

private struct ChatAgentModelSelection: Hashable {
    let jobID: String
    let agentType: String
    let modelID: String
}

/// 聊天时间线只保留“跟随最新”和“浏览历史”两个互斥状态。
/// 浏览态记录进入时的内容版本，用于提示这期间是否又收到了新内容。
private enum ChatTimelineMode: Equatable {
    case following
    case browsing(anchor: Int, messageCount: Int)

    var isFollowing: Bool {
        if case .following = self { return true }
        return false
    }

    var browsingAnchor: Int? {
        if case .browsing(let anchor, _) = self { return anchor }
        return nil
    }

    var browsingMessageCount: Int? {
        if case .browsing(_, let messageCount) = self { return messageCount }
        return nil
    }
}

private enum ChatTimelineWindow {
    /// 默认非懒加载窗口有明确上限，避免长会话首次创建几百个 Markdown 视图。
    static let initialMessageCount = 80
    static let earlierPageSize = 80
}

struct JobChatView: View {
    @EnvironmentObject private var appModel: AppModel
    @Environment(\.scenePhase) private var scenePhase
    @StateObject private var chat = ChatViewModel()
    let route: ChatRoute

    @State private var draft = ""
    @State private var selectedPhotos: [PhotosPickerItem] = []
    @State private var showsPhotoPicker = false
    @State private var pendingAttachments: [PendingUpload] = []
    @State private var attachmentImportCount = 0
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
    @State private var timelineMode: ChatTimelineMode = .following
    @State private var userIsScrollingTimeline = false
    @State private var timelineTopIsVisible = false
    @State private var timelineBottomIsVisible = true
    @State private var visibleTimelineMessageCount = ChatTimelineWindow.initialMessageCount
    @State private var pendingTimelinePrependAnchor: String?
    @State private var earlierPageRequestInFlight = false
    @State private var followBottomRequests = 0
    /// 时间线内容区的实际宽度（已扣掉列表的水平内边距），气泡按它算宽度上限。
    @State private var timelineContentWidth: CGFloat = 0
    @State private var configuredModels: AgentModelState?
    @State private var configuredThoughtLevels: AgentThoughtLevelState?
    @State private var configuredThoughtLevelSelection: ChatAgentModelSelection?
    @State private var thoughtLevelRequestID: UUID?
    @State private var agentPreferences: [String: AgentPreferences] = [:]
    @State private var changingACPConfiguration = false
    @State private var configurationPicker: ChatConfigurationPicker?
    @State private var gitBranch = ""
    @State private var showsWorkspacePathTip = false
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
        .font(.chat(.regular))
        .quartetNavigationTitle(chat.title.isEmpty ? route.summary.displayTitle : chat.title)
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                // 两颗按钮都走 plain style：工具栏默认按钮样式会在每个标签外再补一圈内边距，两颗挨在
                // 一起时中间被撑出很大的空隙，同时把居中标题的可用宽度挤掉。plain 之后横向尺寸完全由
                // 下面的 frame 决定，44pt 高的触摸区域照旧保留。图标字号取全局刻度而不是聊天页那档
                // 缩小刻度，否则会比同一条导航栏上的返回箭头和标题小一圈。
                HStack(spacing: 0) {
                    NavigationLink {
                        WorkspaceDirectoryBrowserView(
                            workspaceTitle: workspaceName ?? route.summary.workspaceId ?? "工作空间".localizedForApp,
                            workspaceRoot: workspaceWorkdir ?? ""
                        )
                    } label: {
                        Image(systemName: "folder")
                            .font(.quartet(.regular, weight: .semibold))
                            .foregroundStyle(QuartetTheme.accent)
                            .frame(width: 30, height: 44)
                            .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("查看当前工作空间目录".localizedForApp)
                    .accessibilityHint(workspaceWorkdir ?? "当前工作空间没有可浏览的目录。".localizedForApp)
                    .accessibilityIdentifier("chat-workspace-files")

                    NavigationLink {
                        JobDetailView(summary: currentJobSummary)
                    } label: {
                        Image(systemName: "info.circle")
                            .font(.quartet(.regular, weight: .semibold))
                            .foregroundStyle(QuartetTheme.accent)
                            .frame(width: 30, height: 44)
                            .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("Job 详情")
                }
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
                primeEarlierTimelineBuffer()
            } catch {
                appModel.present(error)
            }
        }
        .task(id: route.summary.id) {
            if appModel.agentCatalogSnapshot.isEmpty {
                await appModel.refreshAgentCatalog()
            }
        }
        .task(id: thoughtLevelSelection) {
            await refreshThoughtLevels(for: thoughtLevelSelection)
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
            if phase != .active {
                chat.stopStreaming()
            } else {
                Task {
                    do {
                        await chat.start(route: route, client: try appModel.apiClient())
                        primeEarlierTimelineBuffer()
                    } catch {
                        appModel.present(error)
                    }
                }
            }
        }
        .onChange(of: selectedPhotos) { _, items in
            guard !items.isEmpty else { return }
            Task { await loadPhotos(items) }
        }
        .photosPicker(isPresented: $showsPhotoPicker, selection: $selectedPhotos, maxSelectionCount: nil, matching: .images)
        .onChange(of: chat.restoreDraftVersion) { _, _ in
            guard let restored = chat.restoreDraft else { return }
            draft = restored.text
            pendingAttachments = restored.attachments
            selectedPhotos = []
        }
        .onChange(of: chat.authoritativeTitleVersion) { _, _ in
            appModel.synchronizeJobTitle(
                id: route.summary.id,
                title: chat.title,
                fallback: route.summary
            )
            Task { await appModel.reloadJobs() }
        }
        .onChange(of: chat.terminalStateVersion) { _, _ in
            guard route.summary.mode != "graph" else { return }
            Task { await appModel.reloadJobs() }
        }
        .onChange(of: chat.expectsExecution) { wasExpected, isExpected in
            if isExpected {
                // The dashboard stays mounted underneath this NavigationStack. Mirror every
                // execution-start signal back into its shared snapshot so a late JOB_STARTED /
                // RUN_STARTED can repair an earlier transient idle state while the POST, SSE and
                // history reconciliation race each other.
                appModel.beginOptimisticJobExecution(id: route.summary.id, fallback: route.summary)
                return
            }
            guard wasExpected else { return }
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
                onDocumentsPicked: { urls in
                    showsDocumentPicker = false
                    Task { await loadDocuments(urls) }
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

    private var currentJobSummary: JobSummary {
        appModel.jobSummary(id: route.summary.id) ?? route.summary
    }

    /// 只渲染最近一段历史，确保非懒加载容器的工作量有界。
    ///
    /// 钉在最前面的轮首占位不占窗口配额：它代表的就是「已加载窗口之上那条用户消息」，
    /// 而尾部对齐的窗口恰好最先把它切掉，钉住也就白钉了。
    private var timelineMessages: [ChatMessage] {
        let pinnedCount = pinnedRoundHeadCount
        guard pinnedCount > 0 else {
            return Array(chat.messages.suffix(effectiveTimelineMessageCount))
        }
        return Array(chat.messages.prefix(pinnedCount))
            + Array(chat.messages.dropFirst(pinnedCount).suffix(effectiveTimelineMessageCount))
    }

    private var pinnedRoundHeadCount: Int {
        chat.messages.prefix(while: { $0.isRoundHeadPinned }).count
    }

    /// 翻页后用来还原阅读位置的锚点：跳过轮首占位。占位在 prepend 前后都停在最前面，
    /// 拿它当锚点等于没有位移，新插入的一页会把用户正在看的位置整段顶下去。
    private var timelinePrependAnchorID: String? {
        timelineMessages.first(where: { !$0.isRoundHeadPinned })?.id
    }

    /// 渲染列表里距顶部一页的位置。用户滚到这里就该取下一页；只有不足一页可度量时
    /// 返回 nil，那种情况由顶部哨兵兜住。
    private var earlierBufferSentinelIndex: Int? {
        let index = pinnedRoundHeadCount + ChatTimelineWindow.earlierPageSize
        let renderedCount = pinnedRoundHeadCount
            + min(effectiveTimelineMessageCount, max(0, chat.messages.count - pinnedRoundHeadCount))
        return index < renderedCount ? index : nil
    }

    /// 浏览期间追加到尾部的新消息不占用原窗口配额，否则 `suffix` 会同时从顶部移除一条，
    /// 让用户正在看的位置跳动。把增量加入有效窗口后，窗口起点保持不变。
    private var effectiveTimelineMessageCount: Int {
        guard let browsingMessageCount = timelineMode.browsingMessageCount else {
            return visibleTimelineMessageCount
        }
        return visibleTimelineMessageCount + max(0, chat.messages.count - browsingMessageCount)
    }

    private var hiddenTimelineMessageCount: Int {
        // 轮首占位始终渲染，不算「被窗口挡住的更早历史」。
        max(0, chat.messages.count - pinnedRoundHeadCount - effectiveTimelineMessageCount)
    }

    private var timelineHasPendingUpdates: Bool {
        guard let anchor = timelineMode.browsingAnchor else { return false }
        return anchor != chat.scrollAnchor
    }

    private func beginTimelineBrowsing() {
        guard timelineMode.isFollowing else { return }
        timelineMode = .browsing(anchor: chat.scrollAnchor, messageCount: chat.messages.count)
    }

    /// 非懒加载窗口里的底部位置是完整布局后的真实位置，不再经过离屏 cell 高度估算。
    private func scrollTimelineToBottom(_ proxy: ScrollViewProxy) {
        withTransaction(Transaction(animation: nil)) {
            proxy.scrollTo("chat-bottom", anchor: .bottom)
        }
    }

    private func resumeTimelineFollow(_ proxy: ScrollViewProxy) {
        pendingTimelinePrependAnchor = nil
        timelineMode = .following
        visibleTimelineMessageCount = ChatTimelineWindow.initialMessageCount
        scrollTimelineToBottom(proxy)
    }

    /// 首屏之后把缓冲补到两页。
    ///
    /// 首帧仍只渲染一页（非懒加载容器的工作量上限就是为此设的），但只有一页时窗口
    /// 之上没有任何东西可度量，「到顶前一页就取下一页」在第一次上滚时无从触发。这里
    /// 消费的是模型已经在后台预取好的那一页，不额外发请求；且不设 prepend 锚点，
    /// 让跟随态的底部锚定继续生效，补页不会把视口从底部拽走。
    private func primeEarlierTimelineBuffer() {
        guard timelineMode.isFollowing, pendingTimelinePrependAnchor == nil, !earlierPageRequestInFlight else { return }
        if hiddenTimelineMessageCount > 0 {
            visibleTimelineMessageCount = chat.messages.count
        }
        guard chat.hasMoreEarlierMessages else { return }
        earlierPageRequestInFlight = true
        Task {
            let loadedCount = await chat.loadEarlierMessages()
            earlierPageRequestInFlight = false
            guard loadedCount > 0 else { return }
            visibleTimelineMessageCount += loadedCount
        }
    }

    private func loadEarlierTimelineMessages() {
        guard pendingTimelinePrependAnchor == nil, !earlierPageRequestInFlight else { return }
        if hiddenTimelineMessageCount > 0 {
            let anchor = timelinePrependAnchorID
            let revealedCount = min(hiddenTimelineMessageCount, ChatTimelineWindow.earlierPageSize)
            pendingTimelinePrependAnchor = anchor
            visibleTimelineMessageCount = min(
                chat.messages.count,
                visibleTimelineMessageCount + ChatTimelineWindow.earlierPageSize
            )
            if revealedCount == hiddenTimelineMessageCount, chat.hasMoreEarlierMessages {
                earlierPageRequestInFlight = true
                Task {
                    let loadedCount = await chat.loadEarlierMessages()
                    earlierPageRequestInFlight = false
                    if loadedCount > 0, case .browsing(let anchor, let messageCount) = timelineMode {
                        timelineMode = .browsing(anchor: anchor, messageCount: messageCount + loadedCount)
                    }
                    // 必须按新增条数扩窗，和另一条取页分支一致。否则整页新数据落进
                    // 窗口之外的隐藏区，而隐藏区就在列表顶部——用户刚刚还在看的那条
                    // （代表窗口之上那条消息的轮首占位）会当场从渲染里消失。
                    visibleTimelineMessageCount += loadedCount
                }
            }
            return
        }

        guard chat.hasMoreEarlierMessages else { return }
        let anchor = timelinePrependAnchorID
        pendingTimelinePrependAnchor = anchor
        earlierPageRequestInFlight = true
        Task {
            let loadedCount = await chat.loadEarlierMessages()
            earlierPageRequestInFlight = false
            guard loadedCount > 0 else {
                pendingTimelinePrependAnchor = nil
                return
            }
            if case .browsing(let anchor, let messageCount) = timelineMode {
                timelineMode = .browsing(anchor: anchor, messageCount: messageCount + loadedCount)
            }
            visibleTimelineMessageCount += loadedCount
        }
    }

    private var messageList: some View {
        ScrollViewReader { proxy in
            ScrollView {
                // 聊天气泡高度会在流式输出时持续变化。这里必须使用完整测量的 VStack；
                // LazyVStack 会估算离屏高度，工具/思考卡收起时可能把视口留在没有 cell 的空白区。
                VStack(spacing: 14) {
                    if chat.loading && chat.messages.isEmpty && chat.outbox.isEmpty {
                        VStack(spacing: 12) {
                            ProgressView()
                            Text("正在同步对话…")
                                .font(.chat(.detail))
                                .foregroundStyle(QuartetTheme.secondaryText)
                        }
                        .padding(.top, 80)
                    }
                    if hiddenTimelineMessageCount > 0 || chat.hasMoreEarlierMessages {
                        Group {
                            if hiddenTimelineMessageCount > 0 {
                                ProgressView()
                                    .controlSize(.small)
                                    .tint(QuartetTheme.accent)
                                    .frame(maxWidth: .infinity)
                                    .padding(.vertical, 8)
                                    .accessibilityLabel("加载更多".localizedForApp)
                                    .accessibilityIdentifier("chat-load-earlier")
                            } else {
                                Color.clear.frame(height: 1)
                            }
                        }
                        .onScrollVisibilityChange { isVisible in
                            timelineTopIsVisible = isVisible
                            guard isVisible, !timelineMode.isFollowing else { return }
                            loadEarlierTimelineMessages()
                        }
                    }
                    ForEach(Array(timelineMessages.enumerated()), id: \.element.id) { index, message in
                        if index == earlierBufferSentinelIndex {
                            // 距顶部一页的哨兵：滚到这里就说明用户已经进入最上面那一页，
                            // 此时取下一页，而不是等他滚到最顶再干等一次网络往返。
                            Color.clear
                                .frame(height: 1)
                                .onScrollVisibilityChange { isVisible in
                                    guard isVisible, !timelineMode.isFollowing else { return }
                                    loadEarlierTimelineMessages()
                                }
                        }
                        ChatBubble(
                            message: message,
                            fallbackAgentName: chat.agentDisplayLabel,
                            fallbackAgentIconUrl: chat.agentDisplayIconUrl,
                            contentWidth: timelineContentWidth
                        )
                            .equatable()
                            .id(message.id)
                    }
                    ForEach(chat.timelineOutboxItems) { item in
                        OutboxBubble(item: item, contentWidth: timelineContentWidth)
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
                    Color.clear
                        .frame(height: 1)
                        .id("chat-bottom")
                        .onScrollVisibilityChange { isVisible in
                            timelineBottomIsVisible = isVisible
                            guard !userIsScrollingTimeline else { return }
                            if isVisible {
                                timelineMode = .following
                            }
                        }
                }
                .onGeometryChange(for: CGFloat.self) { proxy in
                    proxy.size.width
                } action: { width in
                    timelineContentWidth = width
                }
                .padding(.horizontal, 14)
                .padding(.vertical, 18)
            }
            .scrollDismissesKeyboard(.interactively)
            .defaultScrollAnchor(.bottom, for: .initialOffset)
            .defaultScrollAnchor(timelineMode.isFollowing ? .bottom : nil, for: .sizeChanges)
            .overlay(alignment: .bottom) { backToBottomButton(proxy) }
            // 链接拦截统一在列表这一层注入，动作由 `linkOpener` 持有、全程同一个值。
            .environment(\.openURL, linkOpener.action)
            .onAppear {
                linkOpener.presentError = { [appModel] error in appModel.present(error) }
                linkOpener.presentDestination = { destination in webDestination = destination }
                configureLinkOpener()
            }
            .onChange(of: workspaceContextKey) { _, _ in configureLinkOpener() }
            .simultaneousGesture(TapGesture().onEnded { composerFocused = false })
            .onScrollPhaseChange { oldPhase, newPhase in
                let wasUserScrolling = oldPhase.isScrolling && oldPhase != .animating
                let isUserScrolling = newPhase.isScrolling && newPhase != .animating
                userIsScrollingTimeline = isUserScrolling
                if isUserScrolling {
                    beginTimelineBrowsing()
                    if timelineTopIsVisible {
                        loadEarlierTimelineMessages()
                    }
                    return
                }
                if newPhase == .idle, wasUserScrolling, timelineBottomIsVisible {
                    timelineMode = .following
                }
            }
            .onChange(of: followBottomRequests) { _, _ in
                resumeTimelineFollow(proxy)
            }
            .onChange(of: visibleTimelineMessageCount) { _, _ in
                guard let anchor = pendingTimelinePrependAnchor else { return }
                // 新页已经进入这次视图树，将加载前的第一条固定在顶部，阅读位置不跳。
                withTransaction(Transaction(animation: nil)) {
                    proxy.scrollTo(anchor, anchor: .top)
                }
                pendingTimelinePrependAnchor = nil
            }
            .onChange(of: route.summary.id) { _, _ in
                pendingTimelinePrependAnchor = nil
                earlierPageRequestInFlight = false
                timelineMode = .following
                userIsScrollingTimeline = false
                timelineTopIsVisible = false
                timelineBottomIsVisible = true
                visibleTimelineMessageCount = ChatTimelineWindow.initialMessageCount
                scrollTimelineToBottom(proxy)
            }
        }
    }

    @ViewBuilder
    private func backToBottomButton(_ proxy: ScrollViewProxy) -> some View {
        if !timelineMode.isFollowing, !timelineBottomIsVisible {
            Button {
                composerFocused = false
                resumeTimelineFollow(proxy)
            } label: {
                HStack(spacing: 6) {
                    Image(systemName: "arrow.down")
                        .font(.chat(.detail, weight: .bold))
                    Text(timelineHasPendingUpdates ? "有新内容".localizedForApp : "回到底部".localizedForApp)
                        .font(.chat(.detail, weight: .medium))
                }
                .foregroundStyle(QuartetTheme.onAccent)
                .padding(.horizontal, 13)
                .padding(.vertical, 8)
                .background(QuartetTheme.accent, in: Capsule())
                .shadow(color: Color.black.opacity(0.18), radius: 6, y: 2)
            }
            .buttonStyle(.plain)
            .padding(.bottom, 12)
            .accessibilityLabel(
                timelineHasPendingUpdates ? "有新内容，回到底部".localizedForApp : "回到底部".localizedForApp
            )
            .accessibilityHint("恢复自动跟随最新内容".localizedForApp)
            .accessibilityIdentifier("chat-back-to-bottom")
        }
    }

    private var composer: some View {
        let hasPendingAttachment = !pendingAttachments.isEmpty
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
                }
                .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
                .overlay(
                    RoundedRectangle(cornerRadius: 14, style: .continuous)
                        .stroke(QuartetTheme.divider, lineWidth: 1)
                )
            }

            VStack(spacing: 0) {
                if !pendingAttachments.isEmpty {
                    ChatPendingAttachmentStrip(uploads: pendingAttachments) { index in
                        pendingAttachments.remove(at: index)
                    }
                        .padding(.horizontal, 12)
                        .padding(.top, 12)
                }

                if attachmentImportCount > 0 {
                    HStack(spacing: 8) {
                        ProgressView()
                        Text("正在处理附件…")
                    }
                    .font(.chat(.detail))
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(.horizontal, 15)
                    .padding(.top, 10)
                    .accessibilityElement(children: .combine)
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

                    ComposerMetadataChip(
                        icon: chat.tokenCountSourceIcon,
                        text: chat.tokenCountLabel,
                        accessibilityLabel: chat.tokenCountAccessibilityLabel
                    )

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

                    composerAgentUsage
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
            if !availableThoughtLevels.isEmpty || thoughtLevelDisplayLabel != nil {
                let thoughtLevel = isRefreshingThoughtLevels
                    ? "正在刷新思考等级…".localizedForApp
                    : thoughtLevelDisplayLabel ?? "思考等级"
                Button {
                    composerFocused = false
                    configurationPicker = .thoughtLevel
                } label: {
                    ComposerMetadataChip(
                        icon: changingACPConfiguration ? "arrow.trianglehead.2.clockwise.rotate.90" : thoughtLevelIcon,
                        text: thoughtLevel,
                        accessibilityLabel: thoughtLevelAccessibilityLabel
                    )
                }
                .buttonStyle(.plain)
                .disabled(!canSelectThoughtLevel)
                .accessibilityIdentifier("chat-thought-level-selector")
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

    private var composerAgentUsage: some View {
        Group {
            if let agentType = agentRuntimeType {
                AgentUsageStrip(
                    command: agentType,
                    displayName: chat.agentDisplayLabel
                )
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
        Button {
            composerFocused = false
            showsWorkspacePathTip = true
        } label: {
            ViewThatFits(in: .horizontal) {
                workspaceFooterLine(path: workspaceWorkdir ?? "—")
                    .fixedSize(horizontal: true, vertical: false)
                workspaceFooterLine(path: abbreviatedWorkspacePath)
            }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(workspaceWorkdir == nil)
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.horizontal, 12)
        .padding(.vertical, 7)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(
            "工作空间，\(workspaceName ?? route.summary.workspaceId ?? "未指定")，"
                + "目录，\(workspaceWorkdir ?? "未指定")"
                + (gitBranch.isEmpty ? "" : "，Git 分支，\(gitBranch)")
        )
        .accessibilityHint("轻点查看完整路径".localizedForApp)
        .accessibilityIdentifier("workspace-footer")
        .popover(
            isPresented: $showsWorkspacePathTip,
            attachmentAnchor: .rect(.bounds),
            arrowEdge: .bottom
        ) {
            VStack(alignment: .leading, spacing: 6) {
                Text("完整路径".localizedForApp)
                    .font(.chat(.detail, weight: .semibold))
                    .foregroundStyle(QuartetTheme.primaryText)
                Text(workspaceWorkdir ?? "—")
                    .font(.chat(.compact, design: .monospaced))
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .fixedSize(horizontal: false, vertical: true)
                    .textSelection(.enabled)
                    .accessibilityIdentifier("workspace-full-path")
            }
            .padding(12)
            .frame(width: 300, alignment: .leading)
            .background(QuartetTheme.surface)
            .presentationCompactAdaptation(.popover)
            .presentationBackground(QuartetTheme.surface)
        }
    }

    private func workspaceFooterLine(path: String) -> some View {
        HStack(spacing: 6) {
            Image(systemName: "square.stack.3d.up")
                .foregroundStyle(QuartetTheme.accent)
            Text("\(workspaceName ?? route.summary.workspaceId ?? "—")：")
                .font(.chat(.detail, weight: .semibold))
                .foregroundStyle(QuartetTheme.primaryText)
                .lineLimit(1)
            Text(path)
                .font(.chat(.detail, weight: .medium, design: .monospaced))
                .foregroundStyle(QuartetTheme.secondaryText)
                .lineLimit(1)
                .truncationMode(.middle)
            Spacer(minLength: 0)
            if !gitBranch.isEmpty {
                Label(gitBranch, systemImage: "point.3.connected.trianglepath.dotted")
                    .font(.chat(.detail, weight: .semibold))
                    .foregroundStyle(QuartetTheme.accent)
                    .lineLimit(1)
                    .padding(.horizontal, 8)
                    .frame(height: 22)
                    .background(QuartetTheme.accent.opacity(0.1), in: Capsule())
            }
        }
    }

    private var sendDisabled: Bool {
        chat.loading
            || attachmentImportCount > 0
            || (draft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty && pendingAttachments.isEmpty)
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

    private var thoughtLevelDisplayLabel: String? {
        if let thoughtLevelID = chat.thoughtLevelIDForDisplay,
           configuredThoughtLevelSelection == thoughtLevelSelection,
           let name = configuredThoughtLevels?.availableThoughtLevels
            .first(where: { $0.id == thoughtLevelID })?.name,
           !name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            return name
        }
        if catalogThoughtLevelsMatchCurrentModel {
            return AgentConfigurationDisplay.thoughtLevelName(
                chat.thoughtLevelIDForDisplay,
                agentReference: chat.agentReferenceForDisplay,
                agents: appModel.agentCatalogSnapshot
            )
        }
        return chat.thoughtLevelIDForDisplay
    }

    private var thoughtLevelSelection: ChatAgentModelSelection? {
        guard !chat.loading,
              let agent = selectedAgent,
              let modelID = chat.modelIDForDisplay,
              agent.models != nil else {
            return nil
        }
        return ChatAgentModelSelection(
            jobID: route.summary.id,
            agentType: agent.type,
            modelID: modelID
        )
    }

    private var catalogThoughtLevelsMatchCurrentModel: Bool {
        guard !chat.loading, let modelID = chat.modelIDForDisplay else { return false }
        return selectedAgent?.models?.currentModelId == modelID
    }

    private var currentConfiguredThoughtLevels: AgentThoughtLevelState? {
        guard configuredThoughtLevelSelection == thoughtLevelSelection else { return nil }
        return configuredThoughtLevels
    }

    private var fallbackThoughtLevels: AgentThoughtLevelState? {
        guard catalogThoughtLevelsMatchCurrentModel else { return nil }
        return selectedAgent?.thoughtLevels
    }

    private var displayedThoughtLevels: AgentThoughtLevelState? {
        currentConfiguredThoughtLevels ?? fallbackThoughtLevels
    }

    private var isRefreshingThoughtLevels: Bool {
        thoughtLevelSelection != nil && configuredThoughtLevelSelection != thoughtLevelSelection
    }

    private var thoughtLevelIcon: String {
        isRefreshingThoughtLevels ? "arrow.trianglehead.2.clockwise.rotate.90" : "brain.head.profile"
    }

    private var canSelectThoughtLevel: Bool {
        !availableThoughtLevels.isEmpty && !changingACPConfiguration && !isRefreshingThoughtLevels
    }

    private var thoughtLevelAccessibilityLabel: String {
        if isRefreshingThoughtLevels {
            return "正在刷新思考等级"
        }
        return thoughtLevelDisplayLabel.map { "思考等级，\($0)" } ?? "思考等级"
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
        displayedThoughtLevels?.availableThoughtLevels ?? []
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

    private func refreshThoughtLevels(for selection: ChatAgentModelSelection?) async {
        guard let selection else {
            configuredThoughtLevels = nil
            configuredThoughtLevelSelection = nil
            thoughtLevelRequestID = nil
            return
        }
        guard configuredThoughtLevelSelection != selection else { return }

        configuredThoughtLevels = nil
        configuredThoughtLevelSelection = nil
        let requestID = UUID()
        thoughtLevelRequestID = requestID
        do {
            // Agent 目录只保存最近一次探测的模型联动结果；用无 session 的预览链路
            // 按当前聊天恢复出的 Agent + 模型重新关联，避免改动正在使用的 ACP 会话。
            let state = try await appModel.relinkACPThoughtLevels(
                agentType: selection.agentType,
                modelID: selection.modelID
            )
            try Task.checkCancellation()
            guard thoughtLevelSelection == selection, thoughtLevelRequestID == requestID else { return }

            let availableIDs = Set(state.availableThoughtLevels.map(\.id))
            let persistedThoughtLevelID = chat.thoughtLevelIDForDisplay
            let currentThoughtLevelID = persistedThoughtLevelID.flatMap {
                availableIDs.contains($0) ? $0 : nil
            } ?? state.currentThoughtLevelId
            let refreshed = AgentThoughtLevelState(
                availableThoughtLevels: state.availableThoughtLevels,
                currentThoughtLevelId: currentThoughtLevelID
            )
            configuredThoughtLevels = refreshed
            configuredThoughtLevelSelection = selection
            thoughtLevelRequestID = nil
            chat.reconcileThoughtLevelID(currentThoughtLevelID)
        } catch is CancellationError {
            return
        } catch {
            guard thoughtLevelSelection == selection, thoughtLevelRequestID == requestID else { return }
            configuredThoughtLevels = AgentThoughtLevelState(
                availableThoughtLevels: [],
                currentThoughtLevelId: ""
            )
            configuredThoughtLevelSelection = selection
            thoughtLevelRequestID = nil
            appModel.present(error)
        }
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
            chat.applyACPConfiguration(
                response,
                target: target,
                selectedModelID: modelID,
                selectedThoughtLevelID: thoughtLevelID
            )
            if target == .model {
                configuredThoughtLevels = response.thoughtLevels ?? AgentThoughtLevelState(
                    availableThoughtLevels: [],
                    currentThoughtLevelId: ""
                )
            } else if let thoughtLevels = response.thoughtLevels {
                configuredThoughtLevels = thoughtLevels
            }
            configuredThoughtLevelSelection = thoughtLevelSelection
            thoughtLevelRequestID = nil
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
        guard !text.isEmpty || !pendingAttachments.isEmpty else { return }
        do {
            try appModel.recordSentMessage(text, workspaceID: route.summary.workspaceId)
        } catch {
            appModel.present(error)
        }
        if chat.enqueueDraft(text: text, attachments: pendingAttachments) != nil {
            appModel.beginOptimisticJobExecution(id: route.summary.id, fallback: route.summary)
        }
        // 发送就是“我不看历史了”，恢复跟随并回到底部。
        followBottomRequests &+= 1
        draft = ""
        pendingAttachments = []
        selectedPhotos = []
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

    private func loadPhotos(_ items: [PhotosPickerItem]) async {
        attachmentImportCount += 1
        defer { attachmentImportCount -= 1 }
        var uploads: [PendingUpload] = []
        var failures: [String] = []
        for (index, item) in items.enumerated() {
            do {
                guard let data = try await item.loadTransferable(type: Data.self) else {
                    throw APIError(summary: "图片为空", detail: "照片选择器没有返回图片数据。")
                }
                let contentType = item.supportedContentTypes.first
                uploads.append(try ChatAttachmentProcessor.prepareImageUpload(
                    data: data,
                    suggestedFilename: "ios-\(UUID().uuidString).\(contentType?.preferredFilenameExtension ?? "jpg")",
                    contentType: contentType
                ))
            } catch {
                failures.append("第 \(index + 1) 张图片：\(attachmentErrorDetail(error))")
            }
        }
        pendingAttachments.append(contentsOf: uploads)
        selectedPhotos = []
        presentAttachmentFailures(failures)
    }

    private func setCameraImage(_ image: UIImage) async {
        do {
            pendingAttachments.append(try ChatAttachmentProcessor.prepareImageUpload(
                image: image,
                suggestedFilename: "camera-\(UUID().uuidString).jpg"
            ))
        } catch {
            appModel.present(error)
        }
    }

    private func loadDocuments(_ urls: [URL]) async {
        attachmentImportCount += 1
        defer { attachmentImportCount -= 1 }
        var uploads: [PendingUpload] = []
        var failures: [String] = []
        for url in urls {
            do {
                uploads.append(try await readDocumentUpload(url))
            } catch {
                failures.append("\(url.lastPathComponent)：\(attachmentErrorDetail(error))")
            }
        }
        pendingAttachments.append(contentsOf: uploads)
        presentAttachmentFailures(failures)
    }

    private func attachmentErrorDetail(_ error: Error) -> String {
        if let error = error as? APIError { return "\(error.summary)\n\(error.detail)" }
        return String(describing: error)
    }

    private func presentAttachmentFailures(_ failures: [String]) {
        guard !failures.isEmpty else { return }
        appModel.present(APIError(summary: "部分附件读取失败", detail: failures.joined(separator: "\n\n")))
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
                        .font(.quartet(.regular, weight: .semibold))
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
                    .font(.quartet(.detail, weight: .semibold))
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
                    .font(.quartet(.regular, weight: .semibold))
                    .foregroundStyle(
                        option.id == selectedID || favoriteIDs.contains(option.id)
                            ? QuartetTheme.accent
                            : QuartetTheme.secondaryText
                    )
                    .frame(width: 28)

                VStack(alignment: .leading, spacing: 3) {
                    Text(option.name)
                        .font(.quartet(.control, weight: .semibold))
                        .foregroundStyle(QuartetTheme.primaryText)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    if let detail = option.detail?.trimmingCharacters(in: .whitespacesAndNewlines),
                       !detail.isEmpty {
                        Text(detail)
                            .font(.quartet(.detail))
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
