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

/// 用户离开底部去看前面内容时冻结的时间线快照。
///
/// 只停掉跟随滚动不够：`LazyVStack` 的内容高度在流式输出期间一直在变（新气泡追加、
/// 工具卡在结束时从几千点塌成一行、离屏 cell 的估算高度被重新计算），只要视口上方的
/// 高度变了，contentOffset 不动的情况下画面里的内容也会被顶走 —— 用户看到的就是
/// “没有跳到底部，但在慢慢往下滑”。离开底部期间直接渲染这份快照，列表内容一个字都不
/// 变，阅读位置才真正钉住；回到底部时一次性解冻并补上这段时间的全部新内容。
private struct FrozenChatTimeline: Sendable {
    let messages: [ChatMessage]
    let outboxItems: [LocalOutboxItem]
    /// 冻结那一刻的滚动锚点，用来判断快照之后是否又有了新内容。
    let scrollAnchor: Int
}

/// 聊天列表每一帧只提取这些滚动数据，避免几个独立的几何观察者在同一帧读到不同状态。
private struct ChatScrollMetrics: Equatable {
    let offsetY: CGFloat
    let distanceToBottom: CGFloat
    let isScrollable: Bool

    init(_ geometry: ScrollGeometry) {
        offsetY = geometry.contentOffset.y

        // `contentSize` 不含 inset；UIScrollView 的合法偏移范围还要把上下 inset 算进去。
        // 直接用 contentSize - containerSize 会在键盘/安全区产生 inset 时把“底部”算偏。
        let minimumOffsetY = -geometry.contentInsets.top
        let maximumOffsetY = max(
            minimumOffsetY,
            geometry.contentSize.height - geometry.containerSize.height + geometry.contentInsets.bottom
        )
        distanceToBottom = maximumOffsetY - offsetY
        isScrollable = maximumOffsetY - minimumOffsetY > 1
    }

    var isNearBottom: Bool {
        abs(distanceToBottom) < ChatScrollThreshold.followCatchUp
    }

    var isAtBottom: Bool {
        abs(distanceToBottom) < ChatScrollThreshold.resumeFollow
    }

    var isOffContent: Bool {
        -distanceToBottom > ChatScrollThreshold.offContent
    }
}

private enum ChatScrollThreshold {
    /// 系统已经识别成滚动手势后，再向历史方向移动这点距离就视为明确的自由浏览意图。
    static let enterFreeBrowsing: CGFloat = 12
    /// 已处于自由浏览时，必须真正回到底边才恢复跟随，避免在底部附近反复切换模式。
    static let resumeFollow: CGFloat = 12
    /// 跟随模式下允许底边锚点自行消化的小幅布局误差。
    static let followCatchUp: CGFloat = 80
    /// 超过合法内容底部这么多时视为 LazyVStack 估算失配，而不是正常橡皮筋。
    static let offContent: CGFloat = 80
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
    @State private var userScrolledAwayFromBottom = false
    @State private var userIsScrollingMessages = false
    @State private var messagesAreNearBottom = true
    @State private var messagesScrolledOffContent = false
    @State private var frozenTimeline: FrozenChatTimeline?
    @State private var messageScrollStartOffsetY: CGFloat?
    @State private var messageScrollStartedWhileFollowing = false
    @State private var messageScrollRequestedFreeBrowsing = false
    @State private var followBottomRequests = 0
    @State private var configuredModels: AgentModelState?
    @State private var configuredThoughtLevels: AgentThoughtLevelState?
    @State private var configuredThoughtLevelSelection: ChatAgentModelSelection?
    @State private var thoughtLevelRequestID: UUID?
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

    /// 时间线渲染源：用户离开底部期间固定读冻结快照，回到底部才读实时数据。
    private var timelineMessages: [ChatMessage] {
        frozenTimeline?.messages ?? chat.messages
    }

    private var timelineOutboxItems: [LocalOutboxItem] {
        frozenTimeline?.outboxItems ?? chat.timelineOutboxItems
    }

    /// 冻结期间又产生了新内容：用来在“回到底部”按钮上提示有未读增量。
    private var timelineHasPendingUpdates: Bool {
        guard let frozenTimeline else { return false }
        return frozenTimeline.scrollAnchor != chat.scrollAnchor
    }

    /// 暂停跟随，并把当前时间线冻结成快照。
    ///
    /// 冻结必须发生在滚动视图进入 tracking 的那一刻：这样快照就是手指按下时看到的画面，
    /// 流式增量也没有机会在系统正式识别拖动前先把视口带回底部。此时只是“暂时挂起”，
    /// 还不能断言用户真的想看历史；一次点击或底部橡皮筋会在手势结束时自动解冻。
    private func freezeTimelineForUserScroll() {
        guard frozenTimeline == nil else { return }
        frozenTimeline = FrozenChatTimeline(
            messages: chat.messages,
            outboxItems: chat.timelineOutboxItems,
            scrollAnchor: chat.scrollAnchor
        )
    }

    private func beginMessageScroll(with metrics: ChatScrollMetrics) {
        guard messageScrollStartOffsetY == nil else { return }
        messageScrollStartOffsetY = metrics.offsetY
        messageScrollStartedWhileFollowing = frozenTimeline == nil && !userScrolledAwayFromBottom
        messageScrollRequestedFreeBrowsing = false
        freezeTimelineForUserScroll()
    }

    /// 记录用户“向历史方向”滚动的意图，并且一旦成立就在本次手势内锁存。
    ///
    /// 不能只看松手时是否还在底部附近：流式布局可能在同一帧改变 contentSize，旧实现因此
    /// 经常把一次真实的上滑误判成“仍在底部”。快照冻结后 contentSize 基本稳定，直接比较
    /// contentOffset 的方向既不受底部阈值影响，也不会把向下橡皮筋当成自由浏览。
    private func recordFreeBrowsingIntent(with metrics: ChatScrollMetrics) {
        guard messageScrollStartedWhileFollowing,
              !messageScrollRequestedFreeBrowsing,
              metrics.isScrollable,
              let startOffsetY = messageScrollStartOffsetY,
              startOffsetY - metrics.offsetY >= ChatScrollThreshold.enterFreeBrowsing else { return }
        messageScrollRequestedFreeBrowsing = true
        userScrolledAwayFromBottom = true
    }

    private func resetMessageScrollGesture() {
        messageScrollStartOffsetY = nil
        messageScrollStartedWhileFollowing = false
        messageScrollRequestedFreeBrowsing = false
    }

    /// 用手势结束同一帧的几何值收敛跟随状态。
    private func finishMessageScroll(_ proxy: ScrollViewProxy, with metrics: ChatScrollMetrics) {
        recordFreeBrowsingIntent(with: metrics)
        let startedWhileFollowing = messageScrollStartedWhileFollowing
        let requestedFreeBrowsing = messageScrollRequestedFreeBrowsing
        resetMessageScrollGesture()

        if startedWhileFollowing {
            if requestedFreeBrowsing {
                // 用户意图优先于这一帧的底部位置。即使流式布局恰好把偏移拉回底部，仍保留
                // 冻结快照；用户可通过悬浮按钮明确恢复实时跟随。
                userScrolledAwayFromBottom = true
            } else {
                resumeTimelineFollow(proxy)
            }
            return
        }

        // 已经在自由浏览时，只有用户确实把列表带回底边才自动恢复；停在“差不多到底”仍
        // 保持冻结，避免 80pt 的宽阈值让模式在底部附近来回翻转。
        if metrics.isAtBottom {
            resumeTimelineFollow(proxy)
        } else {
            userScrolledAwayFromBottom = true
        }
    }

    /// 恢复跟随：解冻快照、补上这段时间的新内容，并回到底部。
    ///
    /// 只重置偏移不够：`userScrolledAwayFromBottom` 留在 `true` 的话，后续内容照样不跟随，
    /// 用户看到的是一条卡住的时间线。视口既然已经回到底部，跟随状态必须跟着对齐。
    ///
    /// 这里刻意不加动画：解冻要补上的可能是几千点的内容，动画滚过这么长的距离会让
    /// `LazyVStack` 一路来不及物化 cell，中途整屏空白，落点也更容易算飘。
    private func resumeTimelineFollow(_ proxy: ScrollViewProxy) {
        resetMessageScrollGesture()
        frozenTimeline = nil
        messagesAreNearBottom = true
        userScrolledAwayFromBottom = false
        // `messagesScrolledOffContent` 刻意不在这里清掉：它由几何观察独占，只有偏移真的回到
        // 内容范围内才翻回 false。手工清成 false 会让越界状态在纠正失败后无声消失 ——
        // `onScrollGeometryChange` 只在布尔值翻转时回调，之后再没有第二次纠正机会。
        //
        // 解冻和滚动请求在同一次更新里提交：ScrollView 会先按补齐后的内容重新布局，
        // 再消费这个待处理的滚动目标，所以一次 scrollTo 就落在真正的底部。
        proxy.scrollTo("chat-bottom", anchor: .bottom)
    }

    /// 跟随期间的兜底滚动：只在真的飘离底部时才补一次显式滚动。
    ///
    /// 常态下什么都不做 —— 内容长高由 `.defaultScrollAnchor(.bottom, for: .sizeChanges)` 在
    /// ScrollView 内部接管。这里刻意不像以前那样每次锚点变化都请求一次 `scrollTo`：流式输出
    /// 时锚点每几百毫秒就 bump 一次，每一次都是一次“按 `LazyVStack` 估算高度反算滚到底”的
    /// 机会，撞上工具卡塌缩那一帧就把偏移甩到内容之外，整屏空白。
    ///
    /// 保留这条路径是为了三件事：底边锚点没能把视口带上时（内容已经长高、视口还留在原处）
    /// 补一次追赶；偏移已经越界时每次调用都重试一次纠正 —— `onScrollGeometryChange` 只在
    /// 布尔值翻转时回调，纠正失败就没有第二次机会了；以及历史一次性加载完这类只 bump 一次
    /// 锚点的场景。
    ///
    /// `isNearBottom` 必须由调用方显式传入：几何回调里刚算出的新值和 `messagesAreNearBottom`
    /// 这个状态不是同一时刻的东西，而锚点变化那条路径跑在布局之前，读到的是上一帧的旧值。
    ///
    /// 这里同样不加动画：需要补滚动时距离本来就远，动画滚过几千点会让 `LazyVStack` 一路来不及
    /// 物化 cell，中途整屏空白。
    private func followBottomIfNeeded(_ proxy: ScrollViewProxy, isNearBottom: Bool) {
        guard frozenTimeline == nil,
              !userScrolledAwayFromBottom,
              !userIsScrollingMessages else { return }
        guard messagesScrolledOffContent || !isNearBottom else { return }
        proxy.scrollTo("chat-bottom", anchor: .bottom)
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
                    ForEach(timelineMessages) { message in
                        ChatBubble(
                            message: message,
                            fallbackAgentName: chat.agentDisplayLabel,
                            fallbackAgentIconUrl: chat.agentDisplayIconUrl
                        )
                            .equatable()
                            .id(message.id)
                    }
                    ForEach(timelineOutboxItems) { item in
                        OutboxBubble(item: item)
                            .id(item.id)
                    }
                    // 运行指示器不进快照：它永远排在最后一条气泡之后，冻结期间用户视口
                    // 一定在它上面，出现或消失都只改视口下方的高度，不会顶动正在看的内容。
                    // 反过来，冻结它会让快照在运行早已结束后还挂着一个“正在思考”。
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
            // 跟随底部交给 ScrollView 自己完成。
            //
            // 内容长高（新 delta）或塌缩（长工具输出收起）时，`.sizeChanges` 锚点会按内容
            // **底边**重算偏移，落点天然在合法范围内。这是和外部 `scrollTo` 的本质区别：
            // `scrollTo` 要先在 `LazyVStack` 里解析目标 cell 的位置，而离屏 cell 只有估算
            // 高度，高度剧烈变化的那一帧估算失配得足够狠，反算出的偏移就落到内容之外，
            // 一个 cell 都不物化 —— 这就是流式输出时看到的白屏。
            //
            // 冻结期间必须换回顶部锚点：那时视口在内容中段，用底边锚点的话下方“AI 正在思考”
            // 指示器一出现/消失，用户正在读的内容就会被推着走。
            .defaultScrollAnchor(.bottom, for: .initialOffset)
            .defaultScrollAnchor(frozenTimeline == nil ? .bottom : .top, for: .sizeChanges)
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
            .onScrollPhaseChange { oldPhase, newPhase, context in
                let wasUserScrolling = oldPhase.isScrolling && oldPhase != .animating
                let isUserScrolling = newPhase.isScrolling && newPhase != .animating
                let metrics = ChatScrollMetrics(context.geometry)

                if isUserScrolling, !wasUserScrolling {
                    beginMessageScroll(with: metrics)
                }
                if wasUserScrolling {
                    // phase 回调自带的 geometry 与阶段变化属于同一帧；尤其 idle 时不能再读取
                    // `messagesAreNearBottom` 这种由另一条回调异步写入的上一帧状态。
                    recordFreeBrowsingIntent(with: metrics)
                }
                userIsScrollingMessages = isUserScrolling

                // 用户一开始拖动就暂停跟随并冻结时间线，避免流式增量在手势过程中抢走滚动
                // 位置、也避免内容高度继续变化把阅读位置一点点顶走。
                // 松手后按锁存的滚动意图决定是否恢复，而不是用宽泛的“靠近底部”状态猜测。
                if isUserScrolling {
                    return
                }
                if newPhase == .idle, wasUserScrolling {
                    finishMessageScroll(proxy, with: metrics)
                    return
                }

                // 越界是在滚动静止后才看得见的白屏，手势结束的这一刻补一次判断，
                // 免得越界标记翻转时刚好卡在手势里、之后再没有几何变化来触发纠正。
                guard newPhase == .idle,
                      messagesScrolledOffContent,
                      frozenTimeline == nil,
                      !userScrolledAwayFromBottom else { return }
                resumeTimelineFollow(proxy)
            }
            .onScrollGeometryChange(for: ChatScrollMetrics.self) { geometry in
                ChatScrollMetrics(geometry)
            } action: { oldMetrics, newMetrics in
                // 几何变化在手势阶段会高频发生；只在布尔状态真正翻转或自由浏览意图首次成立
                // 时写 SwiftUI State，避免每个像素都让整页重算 body。
                recordFreeBrowsingIntent(with: newMetrics)

                let nearBottomChanged = messagesAreNearBottom != newMetrics.isNearBottom
                if nearBottomChanged {
                    messagesAreNearBottom = newMetrics.isNearBottom
                    // 布局之后才知道底边锚点有没有真的把视口带上，所以追赶要在这里判，
                    // 不能只靠锚点变化那条路径 —— 它跑在布局之前，读到的是上一帧的位置。
                    followBottomIfNeeded(proxy, isNearBottom: newMetrics.isNearBottom)
                }

                let offContentChanged = messagesScrolledOffContent != newMetrics.isOffContent
                if offContentChanged {
                    messagesScrolledOffContent = newMetrics.isOffContent
                }
                guard newMetrics.isOffContent,
                      !oldMetrics.isOffContent,
                      !userIsScrollingMessages,
                      frozenTimeline == nil,
                      !userScrolledAwayFromBottom else { return }
                resumeTimelineFollow(proxy)
            }
            // 白屏自愈：静止时的偏移必须落在内容范围内，超出就说明 `LazyVStack` 的估算高度
            // 和实际布局失配（长工具输出收起、离屏 cell 重新物化），偏移停在没有任何 cell 的
            // 区域，于是整个列表是空白的 —— 连非 lazy 的“AI 正在思考”一起看不见。
            //
            // 阈值刻意取得和 `messagesAreNearBottom` 同一档（80pt）：静止的 ScrollView 不可能
            // 合法越界这么多，橡皮筋只发生在拖拽/减速阶段，而下面会先排除“用户正在滚动”。
            // 此前要求越界整整一屏才纠正，等于放过了绝大多数白屏 —— 只差半屏也照样一个 cell
            // 都不物化，用户看到的一样是空白。
            //
            // 纠正动作就是“回到底部”，而它只在跟随状态下执行，所以误判是无害的：本来就该在底部。
            .onChange(of: chat.scrollAnchor) { _, _ in
                followBottomIfNeeded(proxy, isNearBottom: messagesAreNearBottom)
            }
            .onChange(of: chat.isRunning) { wasRunning, isRunning in
                guard !wasRunning, isRunning else { return }
                followBottomIfNeeded(proxy, isNearBottom: messagesAreNearBottom)
            }
            // 发送新消息意味着用户不再看历史，必须无条件解冻并回到底部，
            // 否则自己刚发出的消息会被挡在快照之后看不见。
            .onChange(of: followBottomRequests) { _, _ in
                resumeTimelineFollow(proxy)
            }
            // 同一个视图被复用到另一个 Job 时，冻结的快照属于上一段对话，必须丢掉。
            .onChange(of: route.summary.id) { _, _ in
                frozenTimeline = nil
                userScrolledAwayFromBottom = false
                messagesAreNearBottom = true
                messagesScrolledOffContent = false
                resetMessageScrollGesture()
            }
        }
    }

    /// 离开底部期间的“回到底部”悬浮按钮：既是回到实时内容的入口，也是时间线已被冻结的提示。
    ///
    /// 手指按下就会冻结（快照必须早于任何位移），所以按钮的显示还要叠一个“确实不在底部”的
    /// 条件，否则在底部随手一点都会闪一下按钮。
    @ViewBuilder
    private func backToBottomButton(_ proxy: ScrollViewProxy) -> some View {
        if frozenTimeline != nil, userScrolledAwayFromBottom {
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
                        icon: "text.word.spacing",
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
        // 发送就是“我不看历史了”，让时间线解冻并回到底部，免得自己发出的消息被冻结的快照挡住。
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
