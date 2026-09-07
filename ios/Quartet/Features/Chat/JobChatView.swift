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
    case browsing(anchor: Int)

    var isFollowing: Bool {
        if case .following = self { return true }
        return false
    }

    var browsingAnchor: Int? {
        if case .browsing(let anchor) = self { return anchor }
        return nil
    }
}

private enum ChatTimelineWindow {
    /// 默认非懒加载窗口有明确上限，避免长会话首次创建几百个 Markdown 视图。
    static let initialMessageCount = 80
    static let earlierPageSize = 80
    /// 顶部翻页哨兵的固定高度。加载指示器和空占位共用同一高度，哨兵才能一直存在
    /// 且不因加载状态切换而在视口上方凭空增减内容高度。
    static let earlierSentinelHeight: CGFloat = 34
}

struct JobChatView: View {
    @EnvironmentObject private var appModel: AppModel
    @Environment(\.scenePhase) private var scenePhase
    @StateObject private var chat = ChatViewModel()
    let route: ChatRoute

    @State private var draft = ""
    @State private var restoredPersistentDraftJobID: String?
    @State private var selectedPhotos: [PhotosPickerItem] = []
    @State private var showsPhotoPicker = false
    @State private var pendingAttachments: [PendingUpload] = []
    @State private var attachmentImportCount = 0
    @State private var activeImageEdit: ImageAttachmentEditRequest?
    @State private var editingAttachmentIndex: Int?
    @State private var deferredImageEditError: APIError?
    @State private var confirmsStop = false
    @State private var showsAttachmentMenu = false
    @State private var showsCameraPicker = false
    @State private var showsDocumentPicker = false
    @State private var showsMessageLibrary = false
    @State private var sentMessageHistory: [SentMessageHistoryItem] = []
    @State private var projectMessagePresets: [MessagePreset] = []
    @State private var globalMessagePresets: [MessagePreset] = []
    @State private var messagePresetLoadErrors: [String] = []
    @State private var loadingMessagePresets = false
    @State private var timelineMode: ChatTimelineMode = .following
    @State private var userIsScrollingTimeline = false
    @State private var timelineTopIsVisible = false
    @State private var timelineBottomIsVisible = true
    /// 渲染窗口的起点：body 开头要跳过多少条更早历史。
    ///
    /// `nil` 表示尾部对齐——跟随最新时只渲染最近一页，保证非懒加载容器的工作量有界。
    /// 用户一旦开始向上读就把它固化下来，此后流式追加、历史折叠、翻页 prepend 都不再
    /// 移动窗口起点，用户正在看的那条消息不会被窗口挤出渲染。
    @State private var hiddenEarlierMessageCount: Int?
    /// 翻页期间的还原锚点：翻页前视口顶部那条消息，新内容进入视图树后把它重新对齐回顶部。
    @State private var pendingTimelineAnchorID: String?
    @State private var timelineAnchorRestoreRequests = 0
    @State private var earlierPageLoadInFlight = false
    @State private var earlierPageTask: Task<Void, Never>?
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
        .onAppear {
            restorePersistentDraft()
        }
        .onChange(of: route.summary.id) { _, _ in
            restorePersistentDraft()
        }
        .onChange(of: draft) { _, content in
            guard restoredPersistentDraftJobID == route.summary.id else { return }
            appModel.saveJobConversationDraft(content, jobID: route.summary.id)
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
        .onDisappear {
            cancelEarlierPageLoad()
            chat.stopStreaming()
        }
        .onChange(of: scenePhase) { _, phase in
            if phase != .active {
                cancelEarlierPageLoad()
                chat.stopStreaming()
            } else {
                Task {
                    do {
                        await chat.start(route: route, client: try appModel.apiClient())
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
        .fullScreenCover(item: $activeImageEdit, onDismiss: imageEditorDidDismiss) { request in
            ImageAttachmentEditor(
                request: request,
                onCancel: cancelImageEditing,
                onComplete: completeImageEditing
            )
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
        .sheet(isPresented: $showsMessageLibrary) {
            MessagePresetHistorySheet(
                currentMessage: $draft,
                projectPresets: projectMessagePresets,
                globalPresets: globalMessagePresets,
                history: sentMessageHistory,
                errors: messagePresetLoadErrors,
                loading: loadingMessagePresets,
                onApplyHistory: applyHistory
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
    /// 而窗口的跳过量是相对它之后那段算的，所以它总是被单独放回列表最前面。
    private var timelineMessages: [ChatMessage] {
        let pinnedCount = pinnedRoundHeadCount
        let body = chat.messages.dropFirst(pinnedCount + hiddenTimelineMessageCount)
        guard pinnedCount > 0 else { return Array(body) }
        return Array(chat.messages.prefix(pinnedCount)) + Array(body)
    }

    private var pinnedRoundHeadCount: Int {
        chat.messages.prefix(while: { $0.isRoundHeadPinned }).count
    }

    /// 轮首占位始终渲染，不参与窗口配额，所以窗口只在它之后的这段里挪动。
    private var timelineBodyCount: Int {
        max(0, chat.messages.count - pinnedRoundHeadCount)
    }

    /// 窗口之上、还没渲染出来的更早历史条数。
    ///
    /// 描述窗口的是「跳过多少条」而不是「渲染多少条」，这一点是位置稳定的关键：
    /// 尾部追加、历史折叠、翻页 prepend 都不改这个数，窗口起点对应的那条消息就不会变，
    /// 视口上方的内容高度也就不会凭空增减。
    private var hiddenTimelineMessageCount: Int {
        // 固化值不小于当前可用条数说明列表被整体换过（切会话 / 切 Graph 结点），
        // 这时固化的起点已经没有意义，回落到尾部对齐，免得渲染出一个空窗口。
        guard let fixed = hiddenEarlierMessageCount, fixed < timelineBodyCount else {
            return max(0, timelineBodyCount - ChatTimelineWindow.initialMessageCount)
        }
        return max(0, fixed)
    }

    private var timelineHasPendingUpdates: Bool {
        guard let anchor = timelineMode.browsingAnchor else { return false }
        return anchor != chat.scrollAnchor
    }

    private func beginTimelineBrowsing() {
        guard timelineMode.isFollowing else { return }
        // 跟随态的窗口是尾部对齐的，流式每追加一条就从窗口顶部挤掉一条。用户开始向上读
        // 的这一刻必须把窗口起点固定住，否则他正在看的内容会被这种挤动抽走。
        hiddenEarlierMessageCount = hiddenTimelineMessageCount
        timelineMode = .browsing(anchor: chat.scrollAnchor)
    }

    /// 非懒加载窗口里的底部位置是完整布局后的真实位置，不再经过离屏 cell 高度估算。
    private func scrollTimelineToBottom(_ proxy: ScrollViewProxy) {
        withTransaction(Transaction(animation: nil)) {
            proxy.scrollTo("chat-bottom", anchor: .bottom)
        }
    }

    /// 回到跟随最新。窗口重新尾部对齐，把浏览期间为了稳住阅读位置而扩开的渲染量收回去；
    /// 视口此刻就在底部，`.sizeChanges` 的底部锚定会吸收这次收缩。
    ///
    /// 底部可见性回调在流式输出期间会反复触发，所以这里先判一次「已经是跟随态且窗口
    /// 已经尾部对齐」，避免每次都白写一遍状态、多跑一轮 body 求值。
    private func enterTimelineFollow() {
        guard !timelineMode.isFollowing
            || hiddenEarlierMessageCount != nil
            || earlierPageLoadInFlight
            || earlierPageTask != nil else { return }
        cancelEarlierPageLoad()
        timelineMode = .following
        hiddenEarlierMessageCount = nil
    }

    private func resumeTimelineFollow(_ proxy: ScrollViewProxy) {
        enterTimelineFollow()
        scrollTimelineToBottom(proxy)
    }

    private func cancelEarlierPageLoad() {
        earlierPageTask?.cancel()
        earlierPageTask = nil
        earlierPageLoadInFlight = false
        pendingTimelineAnchorID = nil
    }

    /// 取更早的历史。
    ///
    /// 只在用户已经滚到列表顶部（顶部哨兵可见）时触发，所以渲染列表的第一条消息就是
    /// 视口顶部那条，用它当还原锚点是精确的——不必去猜视口里正显示着哪一条。这也是
    /// 为什么这里不再提前一页取：提前取的话锚点就不在视口里，还原就成了盲对齐。
    private func loadEarlierTimelineMessages() {
        guard !earlierPageLoadInFlight, pendingTimelineAnchorID == nil else { return }
        guard let anchorID = timelineMessages.first(where: { !$0.isRoundHeadPinned })?.id else { return }

        // 已经加载进内存、只是被窗口挡住的那部分先揭示出来，这一步不用等网络。
        let hidden = hiddenTimelineMessageCount
        if hidden > 0 {
            earlierPageLoadInFlight = true
            hiddenEarlierMessageCount = max(0, hidden - ChatTimelineWindow.earlierPageSize)
            requestTimelineAnchorRestore(anchorID)
            return
        }

        guard chat.hasMoreEarlierMessages else { return }
        earlierPageLoadInFlight = true
        earlierPageTask = Task {
            let loadedCount = await chat.loadEarlierMessages()
            guard !Task.isCancelled else { return }
            earlierPageTask = nil
            guard loadedCount > 0 else {
                // 这一页拉回来全是已有内容（游标重叠）。窗口不动，闸门放开，
                // 让用户下一次上滑接着往前取。
                earlierPageLoadInFlight = false
                return
            }
            // 窗口起点是「跳过多少条」，这里 hidden 已经是 0，prepend 进来的一页
            // 自然全部落在渲染区内，不需要改窗口。
            requestTimelineAnchorRestore(anchorID)
        }
    }

    /// 内容变更和位置还原必须落在同一次视图更新里：先让新内容进入视图树，再把锚点
    /// 对齐回视口顶部。用专用的请求计数当触发信号，流式追加不会误触发它。
    private func requestTimelineAnchorRestore(_ anchorID: String) {
        pendingTimelineAnchorID = anchorID
        timelineAnchorRestoreRequests &+= 1
    }

    /// 显式把锚点滚回视口顶部。
    ///
    /// 翻页在视口上方插进来的整页内容全靠这一次还原抵掉，缺了它滚动条就会瞬间跑到
    /// 顶部附近。位置维持必须是这样一条显式命令：滚动容器只保证「内容尺寸变化时按
    /// `.sizeChanges` 锚点处理」，从不保证「插入内容时替你认住某一行」。
    private func restorePendingTimelineAnchor(_ proxy: ScrollViewProxy) {
        guard let anchorID = pendingTimelineAnchorID else { return }
        pendingTimelineAnchorID = nil
        earlierPageLoadInFlight = false
        // 锚点必须真的在渲染列表里，`scrollTo` 才有可对齐的目标。
        guard timelineMessages.contains(where: { $0.id == anchorID }) else { return }
        withTransaction(Transaction(animation: nil)) {
            proxy.scrollTo(anchorID, anchor: .top)
        }
    }

    private var messageList: some View {
        // 位置维持全程走单向命令：`proxy.scrollTo` 只在本文件明确调用时滚动。
        // 双向绑定的 `ScrollPosition` 会把滚动结果写回状态，容器何时拿这个值去重新
        // 定位内容并无契约保证，正是「滚动条偶发瞬间跳到顶部」这类问题的来源。
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
                    // 顶部翻页哨兵：无条件渲染，高度恒定。
                    //
                    // 条件渲染过的哨兵被移除时收不到可见性回调，`timelineTopIsVisible` 会
                    // 一直停在旧值；高度随加载状态变化又会在视口上方凭空增减内容高度，
                    // 把用户正在读的位置顶走。所以这里只在固定高度的容器里换内容，
                    // 而且只有真的在等网络时才转圈——「还有更早但没加载」是常态，
                    // 挂一个常驻的离屏动画没有意义。
                    Group {
                        if earlierPageLoadInFlight {
                            ProgressView()
                                .controlSize(.small)
                                .tint(QuartetTheme.accent)
                                .accessibilityLabel("加载更多".localizedForApp)
                                .accessibilityIdentifier("chat-load-earlier")
                        } else {
                            Color.clear
                        }
                    }
                    .frame(maxWidth: .infinity)
                    .frame(height: ChatTimelineWindow.earlierSentinelHeight)
                    .onScrollVisibilityChange { isVisible in
                        timelineTopIsVisible = isVisible
                        guard isVisible, !timelineMode.isFollowing else { return }
                        loadEarlierTimelineMessages()
                    }
                    ForEach(timelineMessages, id: \.id) { message in
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
                                enterTimelineFollow()
                            }
                        }
                }
                .onGeometryChange(for: CGFloat.self) { geometry in
                    geometry.size.width
                } action: { width in
                    timelineContentWidth = width
                }
                .padding(.horizontal, 14)
                .padding(.vertical, 18)
            }
            .scrollDismissesKeyboard(.interactively)
            .defaultScrollAnchor(.bottom, for: .initialOffset)
            // 跟随态由容器把内容尺寸变化锚到底部，流式输出不必每个 delta 都下滚动命令；
            // 浏览态交回默认行为（保持内容偏移），新内容不会把视口从阅读位置拽走。
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
                    // 可见性回调只在「变化」时触发，用户停在顶部再次起滑时不会重放，
                    // 所以这里按当前状态补一次。
                    if timelineTopIsVisible {
                        loadEarlierTimelineMessages()
                    }
                    return
                }
                if newPhase == .idle, wasUserScrolling, timelineBottomIsVisible {
                    enterTimelineFollow()
                }
            }
            .onChange(of: followBottomRequests) { _, _ in
                resumeTimelineFollow(proxy)
            }
            // 翻页的新内容已经进入这次视图树，把翻页前视口顶部那条重新对齐回顶部。
            .onChange(of: timelineAnchorRestoreRequests) { _, _ in
                restorePendingTimelineAnchor(proxy)
            }
            .onChange(of: route.summary.id) { _, _ in
                cancelEarlierPageLoad()
                timelineMode = .following
                hiddenEarlierMessageCount = nil
                userIsScrollingTimeline = false
                timelineTopIsVisible = false
                timelineBottomIsVisible = true
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
                    ChatPendingAttachmentStrip(
                        uploads: pendingAttachments,
                        onEdit: { index in editImageAttachment(at: index) }
                    ) { index in
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

                WrappingHStack(spacing: 7, rowAlignment: .center) {
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
            try appModel.recordSentMessage(
                text,
                attachments: pendingAttachments,
                workspaceID: route.summary.workspaceId
            )
        } catch {
            appModel.present(error)
        }
        guard chat.enqueueDraft(text: text, attachments: pendingAttachments) != nil else { return }
        appModel.beginOptimisticJobExecution(id: route.summary.id, fallback: route.summary)
        appModel.clearJobConversationDraft(jobID: route.summary.id)
        // 发送就是“我不看历史了”，恢复跟随并回到底部。
        followBottomRequests &+= 1
        draft = ""
        pendingAttachments = []
        selectedPhotos = []
    }

    private func restorePersistentDraft() {
        let jobID = route.summary.id
        draft = appModel.jobConversationDraft(jobID: jobID)
        restoredPersistentDraftJobID = jobID
    }

    private func loadSentMessageHistory() {
        do {
            sentMessageHistory = try appModel.sentMessageHistory(workspaceID: route.summary.workspaceId)
        } catch {
            appModel.present(error)
        }
    }

    private func applyHistory(_ item: SentMessageHistoryItem) -> Bool {
        do {
            let attachments = try appModel.sentMessageHistoryAttachments(for: item)
            draft = item.composerContent
            pendingAttachments = attachments
            selectedPhotos = []
            return true
        } catch {
            appModel.present(error)
            return false
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

    private func editImageAttachment(at index: Int) {
        guard pendingAttachments.indices.contains(index) else { return }
        let upload = pendingAttachments[index]
        guard upload.isImage, let image = UIImage(data: upload.data) else {
            appModel.present(APIError(
                summary: "图片数据无效".localizedForApp,
                detail: AppLanguage.localizedFormat("无法打开 %@ 进行编辑。", upload.filename)
            ))
            return
        }
        composerFocused = false
        editingAttachmentIndex = index
        activeImageEdit = ImageAttachmentEditRequest(
            image: image,
            suggestedFilename: upload.filename
        )
    }

    private func completeImageEditing(_ image: UIImage, suggestedFilename: String) {
        do {
            let upload = try ChatAttachmentProcessor.prepareImageUpload(
                image: image,
                suggestedFilename: suggestedFilename
            )
            guard let index = editingAttachmentIndex, pendingAttachments.indices.contains(index) else {
                throw APIError(
                    summary: "无法保存图片".localizedForApp,
                    detail: "原附件已不存在，请重新选择图片。".localizedForApp
                )
            }
            pendingAttachments[index] = upload
        } catch let error as APIError {
            deferredImageEditError = error
        } catch {
            deferredImageEditError = APIError(
                summary: "图片编辑失败".localizedForApp,
                detail: String(describing: error)
            )
        }
        activeImageEdit = nil
        editingAttachmentIndex = nil
    }

    private func cancelImageEditing() {
        activeImageEdit = nil
        editingAttachmentIndex = nil
    }

    private func imageEditorDidDismiss() {
        editingAttachmentIndex = nil
        if let deferredImageEditError {
            self.deferredImageEditError = nil
            appModel.present(deferredImageEditError)
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
