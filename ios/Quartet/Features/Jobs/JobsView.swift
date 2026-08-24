import SwiftUI
import UIKit

struct JobsView: View {
    @EnvironmentObject private var model: AppModel
    @Environment(\.scenePhase) private var scenePhase
    @Environment(\.mainTabBarInset) private var mainTabBarInset
    @Binding private var showsMainTabBar: Bool
    @State private var path: [ChatRoute] = []
    @State private var presentsNewConversation = false
    /// Route the new-conversation sheet asked to open, parked until the sheet has actually gone away.
    /// Dismissing and pushing in the same state update is the classic way to lose the push.
    @State private var pendingRoute: ChatRoute?
    @State private var actionPresentation: JobActionPresentation?
    @State private var presentsConnectionStatus = false

    init(showsMainTabBar: Binding<Bool>) {
        _showsMainTabBar = showsMainTabBar
    }

    var body: some View {
        NavigationStack(path: $path) {
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 0) {
                    connectionNotice
                    sectionHeader
                    jobList
                }
            }
            .background(QuartetTheme.canvas)
            .mainTabBarBottomInset(mainTabBarInset)
            .navigationTitle("运行台")
            .navigationBarTitleDisplayMode(.inline)
            .refreshable { await model.refreshDashboard() }
            // Keyed on connectivity rather than a bare `.task`: launching offline with cached jobs used
            // to raise a modal error sheet for a secondary concern on top of the non-modal connection
            // notice, and it never retried afterwards, so agent/model names stayed as raw ids for the
            // rest of the session. The unreachable-server error itself stays visible in that notice.
            .task(id: model.connectionState.isConnected) {
                guard model.connectionState.isConnected else { return }
                await model.refreshAgentCatalog()
            }
            .task(id: dashboardPollingConfiguration) {
                let configuration = dashboardPollingConfiguration
                guard configuration.isActive, !model.isRunningUITests else { return }
                while !Task.isCancelled {
                    do {
                        try await Task.sleep(for: configuration.hasActiveJobs ? .seconds(5) : .seconds(60))
                    } catch {
                        return
                    }
                    guard !Task.isCancelled else { return }
                    await model.pollDashboard()
                }
            }
            .navigationDestination(for: ChatRoute.self) { route in
                if route.summary.mode == "graph", route.targetSessionID == nil {
                    GraphRunView(summary: route.summary)
                } else {
                    JobChatView(route: route)
                }
            }
            .toolbar {
                ToolbarItem(placement: .principal) {
                    workspaceSelector
                }
                ToolbarItem(placement: .topBarLeading) {
                    Button { presentsConnectionStatus = true } label: {
                        ConnectionBadge(
                            state: model.connectionState,
                            isRefreshing: model.isRefreshing
                        )
                    }
                    .accessibilityLabel(connectionStatusAccessibilityLabel)
                    .accessibilityIdentifier("connection-status-button")
                }
                .sharedBackgroundVisibility(.hidden)
                ToolbarItem(placement: .topBarTrailing) {
                    Button { presentsNewConversation = true } label: {
                        Image(systemName: "plus")
                    }
                    .accessibilityLabel("新建任务")
                    .accessibilityIdentifier("new-conversation-button")
                }
                .sharedBackgroundVisibility(.hidden)
            }
            .sheet(isPresented: $presentsNewConversation, onDismiss: {
                guard let route = pendingRoute else { return }
                pendingRoute = nil
                path.append(route)
            }) {
                NewConversationView { route in
                    pendingRoute = route
                    // Hidden here rather than in `onDismiss`: the bar is still covered by the sheet at
                    // this point, whereas hiding it after dismissal completes flashes it for a frame
                    // before the push begins.
                    setMainTabBarVisible(false)
                    presentsNewConversation = false
                }
                .quartetSheetStyle()
            }
            .sheet(isPresented: $presentsConnectionStatus) {
                DashboardConnectionView()
                    .environmentObject(model)
            }
            .sheet(item: $actionPresentation) { presentation in
                let job = presentation.job
                JobActionsSheet(
                    job: job,
                    initialContent: presentation.initialContent,
                    onTogglePinned: {
                        actionPresentation = nil
                        togglePinned(job)
                    },
                    onRename: { title in
                        actionPresentation = nil
                        Task {
                            do { try await model.renameJob(id: job.id, title: title) }
                            catch { model.present(error) }
                        }
                    },
                    onDelete: {
                        actionPresentation = nil
                        Task {
                            do { try await model.deleteJob(id: job.id) }
                            catch { model.present(error) }
                        }
                    }
                )
                .presentationDetents([.medium])
                .quartetSheetStyle()
            }
        }
        .toolbarBackground(QuartetTheme.canvas, for: .navigationBar)
        .toolbarBackground(.visible, for: .navigationBar)
        .onAppear { setMainTabBarVisible(path.isEmpty) }
        .onChange(of: path.isEmpty) { _, isAtRoot in
            setMainTabBarVisible(isAtRoot)
        }
    }

    @ViewBuilder
    private var connectionNotice: some View {
        let state = model.connectionState
        if !state.isConnected || state.isStale || state.hasPendingSync {
            Button { presentsConnectionStatus = true } label: {
                HStack(spacing: 12) {
                    Image(systemName: connectionNoticeIcon(state))
                        .font(.quartet(.control, weight: .semibold))
                        .foregroundStyle(connectionNoticeColor(state))
                        .frame(width: 34, height: 34)
                        .background(connectionNoticeColor(state).opacity(0.12), in: Circle())

                    VStack(alignment: .leading, spacing: 2) {
                        Text(connectionHeadline(state))
                            .font(.quartet(.control, weight: .semibold))
                            .foregroundStyle(QuartetTheme.primaryText)
                        Text(connectionNoticeDetail(state))
                            .font(.quartet(.detail))
                            .foregroundStyle(QuartetTheme.secondaryText)
                            .lineLimit(1)
                    }

                    Spacer(minLength: 8)
                    Image(systemName: "chevron.right")
                        .font(.quartet(.detail, weight: .bold))
                        .foregroundStyle(QuartetTheme.secondaryText)
                }
                .padding(.horizontal, 14)
                .frame(minHeight: 54)
                .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 16))
                .overlay(RoundedRectangle(cornerRadius: 16).stroke(connectionNoticeColor(state).opacity(0.24)))
            }
            .buttonStyle(.plain)
            .padding(.horizontal, 20)
            .padding(.top, 6)
            .accessibilityIdentifier("connection-notice")
        }
    }

    private var workspaceSelector: some View {
        Menu {
            workspaceMenuButton(id: nil, title: "ALL", color: QuartetTheme.accent)
            if !model.workspaces.isEmpty {
                Divider()
            }
            ForEach(model.workspaces) { workspace in
                workspaceMenuButton(
                    id: workspace.id,
                    title: workspace.displayName,
                    color: QuartetTheme.workspaceTint(workspace.id)
                )
            }
        } label: {
            HStack(spacing: 6) {
                Text(selectedWorkspaceTitle)
                    .font(.quartet(.regular, weight: .semibold))
                    .foregroundStyle(QuartetTheme.primaryText)
                    .lineLimit(1)
                Image(systemName: "chevron.down")
                    .font(.quartet(.compact, weight: .bold))
                    .foregroundStyle(QuartetTheme.secondaryText)
            }
            .frame(maxWidth: 190)
            .contentShape(Rectangle())
        }
        .accessibilityLabel("工作空间，当前为\(selectedWorkspaceTitle)")
        .accessibilityHint("点按选择其他工作空间")
        .accessibilityIdentifier("workspace-selector")
    }

    @ViewBuilder
    private var jobList: some View {
        if model.jobs.isEmpty && model.isRefreshing {
            HStack { Spacer(); ProgressView(); Spacer() }
                .padding(.top, 80)
        } else if model.jobs.isEmpty {
            ContentUnavailableView {
                Label("暂无 Job", systemImage: "waveform.path")
            } description: {
                Text(model.selectedWorkspace == nil ? "当前筛选下还没有任务。" : "这个工作空间在当前筛选下还没有任务。")
            } actions: {
                if model.selectedWorkspace != nil {
                    Button("查看全部工作空间") {
                        Task { await model.selectWorkspace(nil) }
                    }
                }
                if model.hideScheduledJobs {
                    Button("显示定时任务") {
                        Task { await model.setHideScheduledJobs(false) }
                    }
                }
            }
            // The enclosing LazyVStack is leading-aligned, so without this the empty state hugs the
            // left edge instead of centering the way `ContentUnavailableView` is meant to.
            .frame(maxWidth: .infinity)
            .padding(.top, 54)
        } else {
            ForEach(model.jobs) { job in
                VStack(spacing: 0) {
                    JobRow(
                        job: job,
                        workspace: workspace(for: job),
                        displayedStatus: model.displayedStatus(for: job),
                        agentName: agentName(for: job.agentId),
                        modelName: AgentConfigurationDisplay.modelName(
                            job.modelId,
                            agentReference: job.agentId,
                            agents: model.agentCatalogSnapshot
                        ),
                        onOpen: { openJob(job) },
                        onShowActions: { presentActions(for: job) }
                    )
                    // Replacing the swipe gesture with the timestamp button left VoiceOver users with
                    // no rotor entries for these three, only the sheet behind that button.
                    .accessibilityAction(named: (job.pinnedAt ?? 0) > 0 ? "取消置顶" : "置顶") {
                        togglePinned(job)
                    }
                    .accessibilityAction(named: "重命名") {
                        presentActions(for: job, initialContent: .rename)
                    }
                    .accessibilityAction(named: "删除任务") {
                        presentActions(for: job, initialContent: .deleteConfirmation)
                    }

                    // Every row but the last gets a separator — and the last one does too when the
                    // "load more" button follows it, which otherwise butts straight against the row.
                    if job.id != model.jobs.last?.id || model.hasMoreJobs {
                        Divider()
                            .overlay(QuartetTheme.divider)
                            .padding(.leading, 60)
                    }
                }
                .background(QuartetTheme.surface)
                .task(id: GraphStatusRefreshKey(job: job)) {
                    await model.refreshGraphStatusIfNeeded(for: job)
                }
            }
            if model.hasMoreJobs {
                Button { Task { await model.loadMoreJobs() } } label: {
                    HStack {
                        Spacer()
                        if model.isLoadingMore { ProgressView() } else { Text("加载更多") }
                        Spacer()
                    }
                    .padding(.vertical, 18)
                }
                .disabled(model.isLoadingMore)
                .background(QuartetTheme.surface)
            }
        }
    }

    private var sectionHeader: some View {
        HStack(spacing: 10) {
            Text("最近任务")
                .font(.quartet(.regular, weight: .semibold))
                .foregroundStyle(QuartetTheme.primaryText)

            Button {
                Task { await model.setHideScheduledJobs(!model.hideScheduledJobs) }
            } label: {
                Label(
                    model.hideScheduledJobs ? "显示定时任务" : "隐藏定时任务",
                    systemImage: model.hideScheduledJobs ? "eye" : "eye.slash"
                )
                .font(.quartet(.detail, weight: .semibold))
                .labelStyle(.titleAndIcon)
                .foregroundStyle(QuartetTheme.accent)
                .padding(.horizontal, 9)
                .padding(.vertical, 6)
                .background(QuartetTheme.accent.opacity(0.1), in: Capsule())
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("hide-scheduled-jobs-toggle")

            Spacer()
            if model.activeJobCount > 0 {
                HStack(spacing: 6) {
                    Circle()
                        // Matches the row tiles this counts rather than `QuartetTheme.running`, which is
                        // the theme green and would leave the dot a different colour from the very rows
                        // it is summarising.
                        .fill(JobStatusPalette.runningAccent)
                        .frame(width: 7, height: 7)
                    Text("\(model.activeJobCount) 个进行中")
                }
                .font(.quartet(.detail, weight: .medium))
                .foregroundStyle(QuartetTheme.secondaryText)
            }
        }
        .padding(.horizontal, 20)
        .padding(.top, 14)
        .padding(.bottom, 10)
    }

    private func workspaceMenuButton(id: String?, title: String, color: Color) -> some View {
        let selected = model.selectedWorkspaceID == id
        return Button { Task { await model.selectWorkspace(id) } } label: {
            HStack {
                Circle().fill(color).frame(width: 8, height: 8)
                Text(title)
                Spacer()
                if selected {
                    Image(systemName: "checkmark")
                }
            }
        }
        .accessibilityValue(selected ? "已选择" : "")
        .accessibilityIdentifier("workspace-filter-\(id ?? "all")")
    }

    private var selectedWorkspaceTitle: String {
        model.selectedWorkspace?.displayName ?? "ALL"
    }

    private func workspace(for job: JobSummary) -> WorkspaceSummary? {
        model.workspaces.first { $0.id == job.workspaceId }
    }

    private func openJob(_ job: JobSummary) {
        setMainTabBarVisible(false)
        path.append(ChatRoute(
            summary: job,
            agentType: job.agentId,
            modelID: job.modelId,
            modeID: job.acpMode,
            thoughtLevelID: job.acpThoughtLevel
        ))
    }

    private func setMainTabBarVisible(_ isVisible: Bool) {
        var transaction = Transaction()
        transaction.disablesAnimations = true
        withTransaction(transaction) {
            showsMainTabBar = isVisible
        }
    }

    private func agentName(for reference: String?) -> String? {
        guard let reference = displayValue(reference) else { return nil }
        if let agent = model.agentCatalogSnapshot.first(where: { $0.agentId == reference || $0.type == reference }) {
            return displayValue(agent.displayName) ?? displayValue(agent.type) ?? agent.agentId
        }
        return reference
    }

    private func displayValue(_ value: String?) -> String? {
        guard let trimmed = value?.trimmingCharacters(in: .whitespacesAndNewlines),
              !trimmed.isEmpty else { return nil }
        return trimmed
    }

    private func togglePinned(_ job: JobSummary) {
        Task {
            do { try await model.setJobPinned(id: job.id, pinned: (job.pinnedAt ?? 0) == 0) }
            catch { model.present(error) }
        }
    }

    private func presentActions(
        for job: JobSummary,
        initialContent: JobActionSheetContent = .actions
    ) {
        actionPresentation = JobActionPresentation(job: job, initialContent: initialContent)
    }

    private func connectionHeadline(_ state: AppModel.ConnectionState) -> String {
        if state.phase == .connecting {
            return "正在重新连接"
        }
        if state.isStale {
            return state.isConnected ? "数据可能已过期" : "连接已中断"
        }
        if state.hasPendingSync {
            return "等待同步"
        }
        return "连接中断"
    }

    private func connectionNoticeDetail(_ state: AppModel.ConnectionState) -> String {
        if state.isUsingCachedData || state.isStale {
            return "正在展示本地缓存，点按查看详情"
        }
        return "有状态等待刷新，点按立即同步"
    }

    private func connectionNoticeIcon(_ state: AppModel.ConnectionState) -> String {
        if state.phase == .connecting { return "arrow.triangle.2.circlepath" }
        if !state.isConnected { return "wifi.slash" }
        return "exclamationmark.arrow.triangle.2.circlepath"
    }

    private func connectionNoticeColor(_ state: AppModel.ConnectionState) -> Color {
        state.isConnected ? QuartetTheme.warning : QuartetTheme.failed
    }

    private var dashboardPollingConfiguration: DashboardPollingConfiguration {
        DashboardPollingConfiguration(
            // Inactive while a chat or graph run is pushed: those screens hold an SSE connection and
            // already call `reloadJobs()` when a round reaches a terminal state, so polling underneath
            // them is duplicate traffic competing for the same handful of HTTP/1.1 sockets as the stream.
            isActive: scenePhase == .active && path.isEmpty,
            hasActiveJobs: model.activeJobCount > 0,
            workspaceID: model.selectedWorkspaceID,
            hidesScheduledJobs: model.hideScheduledJobs
        )
    }

    private var connectionStatusAccessibilityLabel: String {
        let state = model.connectionState
        if model.isRefreshing || state.phase == .connecting { return "正在同步" }
        if !state.isConnected { return "连接中断，查看连接状态" }
        if state.isStale || state.hasPendingSync { return "数据待同步，查看连接状态" }
        return "连接正常，查看连接状态"
    }
}

private struct DashboardPollingConfiguration: Equatable {
    let isActive: Bool
    let hasActiveJobs: Bool
    let workspaceID: String?
    let hidesScheduledJobs: Bool
}

/// Identity of a row's Graph status fetch. Keyed on the Job revision as well as the id so a row
/// refetches whenever the poll brings a newer Job record — every Graph transition rewrites the Job,
/// and a `job.id`-only key would leave the first fetch cached for the lifetime of the row.
/// Non-graph rows carry a constant revision so their (no-op) task is not restarted on every poll.
private struct GraphStatusRefreshKey: Equatable {
    let jobID: String
    let revision: Int64

    init(job: JobSummary) {
        jobID = job.id
        revision = job.mode == "graph" ? job.updatedAt : 0
    }
}

private struct JobActionPresentation: Identifiable {
    let job: JobSummary
    let initialContent: JobActionSheetContent

    var id: String {
        "\(job.id)-\(initialContent.id)"
    }
}

private enum JobActionSheetContent: Equatable {
    case actions
    case rename
    case deleteConfirmation

    var id: String {
        switch self {
        case .actions: "actions"
        case .rename: "rename"
        case .deleteConfirmation: "delete"
        }
    }
}

private struct JobActionsSheet: View {
    @FocusState private var renameFieldFocused: Bool

    let job: JobSummary
    let onTogglePinned: () -> Void
    let onRename: (String) -> Void
    let onDelete: () -> Void

    @State private var content: JobActionSheetContent
    @State private var renameDraft: String

    init(
        job: JobSummary,
        initialContent: JobActionSheetContent = .actions,
        onTogglePinned: @escaping () -> Void,
        onRename: @escaping (String) -> Void,
        onDelete: @escaping () -> Void
    ) {
        self.job = job
        self.onTogglePinned = onTogglePinned
        self.onRename = onRename
        self.onDelete = onDelete
        _content = State(initialValue: initialContent)
        _renameDraft = State(initialValue: job.displayTitle)
    }

    var body: some View {
        NavigationStack {
            VStack(spacing: 20) {
                Group {
                    switch content {
                    case .actions:
                        actions
                    case .rename:
                        renameForm
                    case .deleteConfirmation:
                        deleteConfirmation
                    }
                }
                .transition(.opacity.combined(with: .move(edge: .bottom)))

                Spacer(minLength: 0)
            }
            .padding(.horizontal, 20)
            .padding(.top, 8)
            .background(QuartetTheme.canvas)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .principal) {
                    Text(job.displayTitle)
                        .font(.quartet(.regular, weight: .semibold))
                        .foregroundStyle(QuartetTheme.primaryText)
                        .lineLimit(1)
                        .truncationMode(.tail)
                        .accessibilityAddTraits(.isHeader)
                }
            }
        }
        .animation(.snappy(duration: 0.28), value: content)
    }

    private var actions: some View {
        VStack(spacing: 12) {
            VStack(spacing: 0) {
                actionRow(
                    title: isPinned ? "取消置顶" : "置顶任务",
                    detail: isPinned ? "恢复按最近更新时间排序" : "固定显示在最近任务顶部",
                    systemImage: isPinned ? "pin.slash.fill" : "pin.fill",
                    tint: QuartetTheme.accent,
                    accessibilityIdentifier: "job-action-pin",
                    showsDisclosure: false
                ) {
                    onTogglePinned()
                }

                Divider()
                    .overlay(QuartetTheme.divider)
                    .padding(.leading, 62)

                actionRow(
                    title: "重命名",
                    detail: "使用更容易识别的任务名称",
                    systemImage: "pencil",
                    tint: QuartetTheme.accentDeep,
                    accessibilityIdentifier: "job-action-rename"
                ) {
                    content = .rename
                }
            }
            .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
            .overlay {
                RoundedRectangle(cornerRadius: 18, style: .continuous)
                    .stroke(QuartetTheme.divider.opacity(0.8), lineWidth: 1)
            }

            actionRow(
                title: "删除任务",
                detail: "同时删除与任务关联的会话",
                systemImage: "trash.fill",
                tint: QuartetTheme.failed,
                isDestructive: true,
                accessibilityIdentifier: "job-action-delete"
            ) {
                content = .deleteConfirmation
            }
            .background(QuartetTheme.failed.opacity(0.07), in: RoundedRectangle(cornerRadius: 18, style: .continuous))
            .overlay {
                RoundedRectangle(cornerRadius: 18, style: .continuous)
                    .stroke(QuartetTheme.failed.opacity(0.18), lineWidth: 1)
            }
        }
    }

    private var renameForm: some View {
        VStack(alignment: .leading, spacing: 14) {
            VStack(alignment: .leading, spacing: 8) {
                Text("任务名称")
                    .font(.quartet(.detail, weight: .semibold))
                    .foregroundStyle(QuartetTheme.secondaryText)

                TextField("输入任务名称", text: $renameDraft)
                    .font(.quartet(.regular, weight: .medium))
                    .foregroundStyle(QuartetTheme.primaryText)
                    .textInputAutocapitalization(.sentences)
                    .submitLabel(.done)
                    .focused($renameFieldFocused)
                    .onSubmit(saveRename)
                    .padding(.horizontal, 15)
                    .frame(minHeight: 52)
                    .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
                    .overlay {
                        RoundedRectangle(cornerRadius: 14, style: .continuous)
                            .stroke(renameFieldFocused ? QuartetTheme.accent : QuartetTheme.divider, lineWidth: 1)
                    }
                    .accessibilityIdentifier("job-rename-field")
                    .task { renameFieldFocused = true }
                    .onChange(of: renameDraft) { _, value in
                        if value.count > 200 {
                            renameDraft = String(value.prefix(200))
                        }
                    }

                Text("\(renameDraft.count) / 200")
                    .font(.quartet(.compact, design: .monospaced))
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .frame(maxWidth: .infinity, alignment: .trailing)
            }

            HStack(spacing: 10) {
                secondaryButton("返回") {
                    renameFieldFocused = false
                    content = .actions
                }

                Button("保存名称", action: saveRename)
                    .font(.quartet(.control, weight: .semibold))
                    .foregroundStyle(QuartetTheme.onAccent)
                    .frame(maxWidth: .infinity)
                    .frame(height: 50)
                    .background(QuartetTheme.accent, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
                    .disabled(preparedTitle.isEmpty || preparedTitle == job.displayTitle)
                    .opacity(preparedTitle.isEmpty || preparedTitle == job.displayTitle ? 0.45 : 1)
                    .accessibilityIdentifier("job-rename-save")
            }
        }
    }

    private var deleteConfirmation: some View {
        VStack(spacing: 18) {
            HStack(alignment: .top, spacing: 14) {
                Image(systemName: "trash.fill")
                    .font(.quartet(.regular, weight: .semibold))
                    .foregroundStyle(QuartetTheme.failed)
                    .frame(width: 44, height: 44)
                    .background(QuartetTheme.failed.opacity(0.12), in: Circle())

                VStack(alignment: .leading, spacing: 5) {
                    Text("确定删除这个任务？")
                        .font(.quartet(.regular, weight: .semibold))
                        .foregroundStyle(QuartetTheme.primaryText)
                    Text("运行中的任务会先停止，随后删除相关会话。此操作无法撤销。")
                        .font(.quartet(.detail))
                        .foregroundStyle(QuartetTheme.secondaryText)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            .padding(16)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(QuartetTheme.failed.opacity(0.07), in: RoundedRectangle(cornerRadius: 18, style: .continuous))
            .overlay {
                RoundedRectangle(cornerRadius: 18, style: .continuous)
                    .stroke(QuartetTheme.failed.opacity(0.2), lineWidth: 1)
            }

            HStack(spacing: 10) {
                secondaryButton("保留任务") {
                    content = .actions
                }

                Button(role: .destructive) {
                    onDelete()
                } label: {
                    Text("确认删除")
                        .font(.quartet(.control, weight: .semibold))
                        .foregroundStyle(QuartetTheme.onDanger)
                        .frame(maxWidth: .infinity)
                        .frame(height: 50)
                        .background(QuartetTheme.failed, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("job-delete-confirm")
            }
        }
    }

    private func actionRow(
        title: String,
        detail: String,
        systemImage: String,
        tint: Color,
        isDestructive: Bool = false,
        accessibilityIdentifier: String,
        showsDisclosure: Bool = true,
        action: @escaping () -> Void
    ) -> some View {
        Button(role: isDestructive ? .destructive : nil, action: action) {
            HStack(spacing: 12) {
                Image(systemName: systemImage)
                    .font(.quartet(.control, weight: .semibold))
                    .foregroundStyle(tint)
                    .frame(width: 38, height: 38)
                    .background(tint.opacity(0.11), in: Circle())

                VStack(alignment: .leading, spacing: 3) {
                    Text(title)
                        .font(.quartet(.control, weight: .semibold))
                        .foregroundStyle(isDestructive ? QuartetTheme.failed : QuartetTheme.primaryText)
                    Text(detail)
                        .font(.quartet(.detail))
                        .foregroundStyle(QuartetTheme.secondaryText)
                        .lineLimit(1)
                }

                Spacer(minLength: 8)

                if showsDisclosure {
                    Image(systemName: "chevron.right")
                        .font(.quartet(.compact, weight: .bold))
                        .foregroundStyle(QuartetTheme.secondaryText.opacity(0.7))
                }
            }
            .padding(.horizontal, 13)
            .frame(minHeight: 64)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier(accessibilityIdentifier)
    }

    private func secondaryButton(_ title: String, action: @escaping () -> Void) -> some View {
        Button(title, action: action)
            .font(.quartet(.control, weight: .semibold))
            .foregroundStyle(QuartetTheme.primaryText)
            .frame(maxWidth: .infinity)
            .frame(height: 50)
            .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
    }

    private func saveRename() {
        guard !preparedTitle.isEmpty, preparedTitle != job.displayTitle else { return }
        onRename(preparedTitle)
    }

    private var preparedTitle: String {
        String(renameDraft.trimmingCharacters(in: .whitespacesAndNewlines).prefix(200))
    }

    private var isPinned: Bool {
        (job.pinnedAt ?? 0) > 0
    }
}

private struct JobRow: View {
    let job: JobSummary
    let workspace: WorkspaceSummary?
    let displayedStatus: String
    let agentName: String?
    let modelName: String?
    let onOpen: () -> Void
    let onShowActions: () -> Void

    var body: some View {
        HStack(alignment: .top, spacing: 8) {
            Button(action: handleOpen) {
                HStack(alignment: .top, spacing: 12) {
                    JobModeIcon(mode: job.mode, status: displayedStatus)
                        .frame(width: 34, height: 34)
                        .accessibilityElement(children: .ignore)
                        .accessibilityLabel("\(modeText)，\(statusText)")
                        .padding(.top, 1)

                    VStack(alignment: .leading, spacing: 7) {
                        HStack(alignment: .firstTextBaseline, spacing: 7) {
                            Text(job.displayTitle)
                                .font(.quartet(.control, weight: .semibold))
                                .foregroundStyle(QuartetTheme.primaryText)
                                .lineLimit(1)
                                .frame(maxWidth: .infinity, alignment: .leading)

                            if isPinned {
                                Image(systemName: "pin.fill")
                                    .font(.quartet(.compact, weight: .bold))
                                    .foregroundStyle(QuartetTheme.accent)
                                    .accessibilityHidden(true)
                            }
                        }

                        HStack(alignment: .center, spacing: 6) {
                            metadataTopLine
                            metadataSeparator
                            metadataBottomLine
                        }
                        .font(.quartet(.detail))
                        .lineLimit(1)
                        .minimumScaleFactor(0.82)
                        .foregroundStyle(QuartetTheme.secondaryText)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .frame(maxWidth: .infinity, alignment: .leading)
            .accessibilityIdentifier("job-\(job.id)")

            timeButton
        }
        .padding(.leading, 16)
        .padding(.trailing, 12)
        .padding(.vertical, 12)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(QuartetTheme.surface)
    }

    private var timeButton: some View {
        Button(action: onShowActions) {
            JobSentTime(timestamp: job.updatedAt)
                .foregroundStyle(QuartetTheme.secondaryText)
                .fixedSize(horizontal: true, vertical: true)
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel("\(job.displayTitle) 的任务操作")
        // The timestamp this button renders is the row's only copy of the update time, and the button's
        // own label would otherwise swallow it, leaving VoiceOver no way to hear when the Job changed.
        .accessibilityValue("更新于 \(FormattedJobTime.make(timestamp: job.updatedAt, relativeTo: .now).accessibility)")
        .accessibilityHint("点按打开任务操作")
        .accessibilityIdentifier("job-time-\(job.id)")
    }

    private func handleOpen() {
        onOpen()
    }

    private var metadataTopLine: some View {
        HStack(spacing: 6) {
            JobMetadataLabel(
                text: workspaceName,
                systemImage: nil,
                color: workspaceColor,
                accessibilityLabel: "工作空间，\(workspaceName)"
            )
            .layoutPriority(1)

            if let agentDisplayName {
                metadataSeparator

                JobMetadataLabel(
                    text: agentDisplayName,
                    systemImage: "command",
                    color: QuartetTheme.secondaryText,
                    accessibilityLabel: "Agent，\(agentDisplayName)"
                )
                .layoutPriority(1)
            }
        }
    }

    private var metadataBottomLine: some View {
        HStack(spacing: 6) {
            JobMetadataLabel(
                text: modelName ?? fallbackModelName,
                systemImage: "cpu",
                color: QuartetTheme.secondaryText,
                accessibilityLabel: "模型，\(modelName ?? fallbackModelName)"
            )
            .layoutPriority(1)

            if job.scheduleId != nil {
                Image(systemName: "calendar.badge.clock")
                    .font(.quartet(.detail, weight: .semibold))
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .accessibilityLabel("定时任务")
            }
        }
    }

    private var metadataSeparator: some View {
        Text("·")
            .foregroundStyle(QuartetTheme.secondaryText.opacity(0.72))
            .accessibilityHidden(true)
    }

    private var modeText: String {
        switch job.mode {
        case "graph": "Graph 工作流"
        case "loop": "循环任务"
        default: "交互任务"
        }
    }

    private var workspaceName: String {
        if let workspace {
            return workspace.displayName
        }
        if let workspaceID = job.workspaceId, !workspaceID.isEmpty {
            return workspaceID
        }
        if let workdir = job.workdir, !workdir.isEmpty {
            return URL(fileURLWithPath: workdir).lastPathComponent
        }
        return "未关联工作空间"
    }

    private var fallbackModelName: String {
        if let modelID = job.modelId?.trimmingCharacters(in: .whitespacesAndNewlines),
           !modelID.isEmpty {
            return modelID
        }
        if let agentID = job.agentId?.trimmingCharacters(in: .whitespacesAndNewlines),
           !agentID.isEmpty {
            return agentID
        }
        return job.mode == "graph" ? "按节点配置" : "未记录模型"
    }

    private var agentDisplayName: String? {
        if let agentName = agentName?.trimmingCharacters(in: .whitespacesAndNewlines),
           !agentName.isEmpty {
            return agentName
        }
        if let agentID = job.agentId?.trimmingCharacters(in: .whitespacesAndNewlines),
           !agentID.isEmpty {
            return agentID
        }
        return nil
    }

    private var workspaceColor: Color {
        workspace.map(QuartetTheme.workspaceTint) ?? modeColor
    }

    private var modeColor: Color {
        switch job.mode {
        case "graph": QuartetTheme.terminalGreen
        case "loop": QuartetTheme.running
        default: QuartetTheme.accent
        }
    }

    private var statusText: String {
        switch displayedStatus {
        case "pending": "等待运行"
        case "running": "运行中"
        case "awaitingInput": "等待人工"
        case "stepStopping": "停止中"
        case "stepStopped", "stopped": "已停止"
        case "completed": "运行成功"
        case "failed": "运行失败"
        case "timedOut": "运行超时"
        default: displayedStatus
        }
    }

    private var isPinned: Bool {
        (job.pinnedAt ?? 0) > 0
    }
}

private struct JobMetadataLabel: View {
    let text: String
    let systemImage: String?
    let color: Color
    let accessibilityLabel: String

    var body: some View {
        HStack(spacing: 4) {
            if let systemImage {
                Image(systemName: systemImage)
                    .font(.quartet(.compact, weight: .semibold))
                    .foregroundStyle(color)
                    .frame(width: 12)
            } else {
                Circle()
                    .fill(color)
                    .frame(width: 6, height: 6)
            }
            Text(text)
                .lineLimit(1)
                .truncationMode(.tail)
                .minimumScaleFactor(0.82)
        }
        .frame(minWidth: 0, alignment: .leading)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(accessibilityLabel)
    }
}

private struct JobModeIcon: View {
    let mode: String?
    let status: String

    var body: some View {
        ZStack {
            RoundedRectangle(cornerRadius: 8, style: .continuous)
                .fill(palette.fill)
                .overlay {
                    RoundedRectangle(cornerRadius: 8, style: .continuous)
                        .stroke(palette.border, lineWidth: 1)
                }

            if isLive {
                JobRunningIndicator(color: palette.primary, track: palette.border)
                    .frame(width: 25, height: 25)
            }

            modeSymbol
                .frame(width: 18, height: 18)
                .foregroundStyle(palette.primary)
        }
        .overlay(alignment: .bottomTrailing) {
            if !isLive, let statusSymbol {
                ZStack {
                    Circle()
                        .fill(QuartetTheme.surface)
                    Circle()
                        .fill(palette.primary)
                        .padding(1.5)
                    Image(systemName: statusSymbol)
                        .font(.system(size: 6.5, weight: .bold))
                        .foregroundStyle(palette.badgeForeground)
                }
                .frame(width: 13, height: 13)
            }
        }
    }

    @ViewBuilder
    private var modeSymbol: some View {
        if mode == "loop" {
            Image(systemName: "arrow.triangle.2.circlepath")
                .font(.quartet(.control, weight: .semibold))
        } else {
            JobModeGlyph(mode: mode)
                .stroke(
                    palette.primary,
                    style: StrokeStyle(lineWidth: 2, lineCap: .round, lineJoin: .round)
                )
        }
    }

    private var isLive: Bool {
        normalizedStatus == "running" || normalizedStatus == "stepstopping"
    }

    private var statusSymbol: String? {
        switch normalizedStatus {
        case "completed": "checkmark"
        case "failed": "xmark"
        case "timedout", "pending": "clock"
        case "awaitinginput": "pause.fill"
        case "stepstopped", "stopped": "stop.fill"
        default: nil
        }
    }

    private var palette: JobStatusPalette {
        JobStatusPalette(status: normalizedStatus)
    }

    private var normalizedStatus: String {
        status.lowercased()
    }
}

private struct JobRunningIndicator: View {
    let color: Color
    let track: Color
    @State private var rotates = false

    var body: some View {
        ZStack {
            Circle()
                .stroke(track.opacity(0.48), lineWidth: 2.4)
            Circle()
                .trim(from: 0.08, to: 0.36)
                .stroke(color, style: StrokeStyle(lineWidth: 2.4, lineCap: .round))
                .rotationEffect(.degrees(rotates ? 360 : 0))
        }
        .onAppear { rotates = true }
        .animation(.linear(duration: 0.9).repeatForever(autoreverses: false), value: rotates)
    }
}

/// Job status hues for the dashboard rows. These deliberately do NOT come from
/// `QuartetTheme.statusColor`: the theme's `running` is the ambient app green, and a green tile reads as
/// "nothing to see here" for the one status that most needs to stand out — worse, it would collide with
/// `completed`, which is also green, and cost the list its at-a-glance running/finished distinction.
/// `completed` and `pending` already match `QuartetTheme.success`/`warning` exactly; only `running` and
/// the two neutrals diverge, and that divergence is the point.
private struct JobStatusPalette {
    let fill: Color
    let border: Color
    let primary: Color
    let badgeForeground: Color

    /// The "a run is in flight" hue, exposed so chrome outside the tiles can match them.
    static var runningAccent: Color { running.primary }

    init(status: String) {
        switch status {
        case "running", "stepstopping":
            self = .running
        case "completed":
            self = .completed
        case "failed", "timedout":
            self = .failed
        case "pending", "awaitinginput":
            self = .pending
        case "stepstopped", "stopped":
            self = .stopped
        default:
            self = .unknown
        }
    }

    private init(
        fillLight: UInt32, fillDark: UInt32,
        borderLight: UInt32, borderDark: UInt32,
        primaryLight: UInt32, primaryDark: UInt32
    ) {
        fill = Self.dynamic(light: fillLight, dark: fillDark)
        border = Self.dynamic(light: borderLight, dark: borderDark)
        primary = Self.dynamic(light: primaryLight, dark: primaryDark)
        // The status glyph sits on a `primary` disc, which is a deep hue in light mode and a bright one
        // in dark mode, so the glyph has to flip with it to stay readable.
        badgeForeground = Self.dynamic(light: 0xFFFFFF, dark: 0x07120B)
    }

    // Light values are the original palette, unchanged. The dark values were missing entirely, which
    // left these tiles rendering as pastel blocks on a near-black row; they keep the same hue at the
    // depth the rest of the theme uses.
    private static let running = JobStatusPalette(
        fillLight: 0xDBEAFE, fillDark: 0x12253F,
        borderLight: 0x93C5FD, borderDark: 0x1E4E8C,
        primaryLight: 0x2563EB, primaryDark: 0x60A5FA
    )
    private static let completed = JobStatusPalette(
        fillLight: 0xDCFCE7, fillDark: 0x0E2A19,
        borderLight: 0x86EFAC, borderDark: 0x1E5B34,
        primaryLight: 0x16A34A, primaryDark: 0x4ADE80
    )
    private static let failed = JobStatusPalette(
        fillLight: 0xFEE2E2, fillDark: 0x341315,
        borderLight: 0xFCA5A5, borderDark: 0x7F2A2E,
        primaryLight: 0xDC2626, primaryDark: 0xFF6B6B
    )
    private static let pending = JobStatusPalette(
        fillLight: 0xFEF9C3, fillDark: 0x2E2708,
        borderLight: 0xFDE68A, borderDark: 0x6B5410,
        primaryLight: 0xA16207, primaryDark: 0xFACC15
    )
    private static let stopped = JobStatusPalette(
        fillLight: 0xF3F4F6, fillDark: 0x1C211E,
        borderLight: 0xD1D5DB, borderDark: 0x39413B,
        primaryLight: 0x6B7280, primaryDark: 0x9AA3A0
    )
    private static let unknown = JobStatusPalette(
        fillLight: 0xF1F5F9, fillDark: 0x1A1F24,
        borderLight: 0xCBD5E1, borderDark: 0x39424B,
        primaryLight: 0x64748B, primaryDark: 0x94A3B8
    )

    private static func dynamic(light: UInt32, dark: UInt32) -> Color {
        Color(uiColor: UIColor { traits in
            let rgb = traits.userInterfaceStyle == .dark ? dark : light
            return UIColor(
                red: CGFloat((rgb >> 16) & 0xff) / 255,
                green: CGFloat((rgb >> 8) & 0xff) / 255,
                blue: CGFloat(rgb & 0xff) / 255,
                alpha: 1
            )
        })
    }
}

private struct JobModeGlyph: Shape {
    let mode: String?

    func path(in rect: CGRect) -> Path {
        let scale = min(rect.width, rect.height) / 24
        let origin = CGPoint(
            x: rect.midX - 12 * scale,
            y: rect.midY - 12 * scale
        )
        func point(_ x: CGFloat, _ y: CGFloat) -> CGPoint {
            CGPoint(x: origin.x + x * scale, y: origin.y + y * scale)
        }

        var path = Path()
        if mode == "graph" {
            let radius = 2.5 * scale
            for center in [point(6, 6), point(18, 6), point(12, 18)] {
                path.addEllipse(in: CGRect(
                    x: center.x - radius,
                    y: center.y - radius,
                    width: radius * 2,
                    height: radius * 2
                ))
            }
            path.move(to: point(8.2, 7.5))
            path.addLine(to: point(11, 15.7))
            path.move(to: point(15.8, 7.5))
            path.addLine(to: point(13, 15.7))
            path.move(to: point(8.5, 6))
            path.addLine(to: point(15.5, 6))
        } else {
            path.move(to: point(21, 15))
            path.addCurve(
                to: point(19, 17),
                control1: point(21, 16.1),
                control2: point(20.1, 17)
            )
            path.addLine(to: point(7, 17))
            path.addLine(to: point(3, 21))
            path.addLine(to: point(3, 5))
            path.addCurve(
                to: point(5, 3),
                control1: point(3, 3.9),
                control2: point(3.9, 3)
            )
            path.addLine(to: point(19, 3))
            path.addCurve(
                to: point(21, 5),
                control1: point(20.1, 3),
                control2: point(21, 3.9)
            )
            path.closeSubpath()
        }
        return path
    }
}

private struct JobSentTime: View {
    let timestamp: Int64
    /// Lifts the clock down onto the row's metadata line. `@ScaledMetric` because the line it has to
    /// meet is laid out from the title and metadata fonts, so the offset has to grow with Dynamic Type
    /// too — a fixed 25pt collides with the title at the larger accessibility text sizes.
    @ScaledMetric(relativeTo: .subheadline) private var clockTopPadding: CGFloat = 25

    var body: some View {
        // `.everyMinute` rather than `.periodic(from: .now, by: 60)`: the rendered text can only change
        // when the day rolls over, and a `.now` anchor restarts the schedule on every re-render — which
        // is every 5 seconds while the dashboard is polling active jobs.
        TimelineView(.everyMinute) { context in
            let time = FormattedJobTime.make(timestamp: timestamp, relativeTo: context.date)
            ZStack(alignment: .topTrailing) {
                if let date = time.date {
                    Text(date)
                }

                Text(time.clock)
                    .padding(.top, clockTopPadding)
            }
                .font(.quartet(.detail).monospacedDigit())
        }
        // The enclosing button owns the accessibility element, so an inner label here would be dropped.
        .accessibilityHidden(true)
    }
}

private struct FormattedJobTime {
    let date: String?
    let clock: String

    var accessibility: String {
        if let date {
            return "\(date) \(clock)"
        }
        return clock
    }

    static func make(timestamp: Int64, relativeTo now: Date) -> FormattedJobTime {
        let sentAt = timestamp.quartetDate
        let calendar = Calendar.autoupdatingCurrent
        let components = calendar.dateComponents([.month, .day, .hour, .minute], from: sentAt)
        let clock = String(format: "%02d:%02d", components.hour ?? 0, components.minute ?? 0)
        if calendar.isDate(sentAt, inSameDayAs: now) {
            return FormattedJobTime(date: nil, clock: clock)
        }

        let date = String(format: "%02d-%02d", components.month ?? 0, components.day ?? 0)
        return FormattedJobTime(date: date, clock: clock)
    }
}

private struct ConnectionBadge: View {
    let state: AppModel.ConnectionState
    let isRefreshing: Bool

    var body: some View {
        ZStack {
            Circle()
                .fill(statusColor.opacity(0.12))
                .frame(width: 30, height: 30)

            if isRefreshing || state.phase == .connecting {
                ProgressView()
                    .controlSize(.mini)
                    .tint(statusColor)
            } else {
                Circle()
                    .fill(statusColor)
                    .frame(width: 9, height: 9)

                if state.isStale || state.hasPendingSync {
                    Circle()
                        .fill(QuartetTheme.canvas)
                        .frame(width: 7, height: 7)
                        .overlay(
                            Circle()
                                .fill(QuartetTheme.running)
                                .frame(width: 5, height: 5)
                        )
                        .offset(x: 7, y: -7)
                }
            }
        }
    }

    private var statusColor: Color {
        if !state.isConnected { return QuartetTheme.failed }
        if state.isStale || state.hasPendingSync { return QuartetTheme.warning }
        return QuartetTheme.accent
    }
}

private struct DashboardConnectionView: View {
    @EnvironmentObject private var model: AppModel

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 18) {
                    statusCard

                    VStack(alignment: .leading, spacing: 7) {
                        Text("服务地址")
                            .font(.quartet(.detail, weight: .semibold))
                            .foregroundStyle(QuartetTheme.secondaryText)
                        Text(model.serverAddress)
                            .font(.quartet(.detail, design: .monospaced))
                            .foregroundStyle(QuartetTheme.primaryText)
                            .textSelection(.enabled)
                    }
                    .padding(16)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 16))
                    .overlay(RoundedRectangle(cornerRadius: 16).stroke(QuartetTheme.divider))

                    if let failure = model.connectionState.lastFailureMessage, !failure.isEmpty {
                        VStack(alignment: .leading, spacing: 10) {
                            HStack {
                                Label("最近一次错误", systemImage: "exclamationmark.triangle.fill")
                                    .font(.quartet(.control, weight: .semibold))
                                    .foregroundStyle(QuartetTheme.failed)
                                Spacer()
                                Button("复制") { UIPasteboard.general.string = failure }
                                    .font(.quartet(.detail, weight: .semibold))
                            }
                            Text(failure)
                                .font(.quartet(.detail, design: .monospaced))
                                .foregroundStyle(QuartetTheme.primaryText)
                                .textSelection(.enabled)
                        }
                        .padding(16)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(QuartetTheme.failed.opacity(0.07), in: RoundedRectangle(cornerRadius: 16))
                        .overlay(RoundedRectangle(cornerRadius: 16).stroke(QuartetTheme.failed.opacity(0.24)))
                    }

                    Button {
                        Task { await synchronize() }
                    } label: {
                        HStack {
                            if model.isRefreshing || model.connectionState.phase == .connecting {
                                ProgressView()
                                    .tint(QuartetTheme.canvas)
                            } else {
                                Image(systemName: model.connectionState.isConnected ? "arrow.clockwise" : "network")
                            }
                            Text(model.connectionState.isConnected ? "立即同步" : "重新连接")
                        }
                        .font(.quartet(.regular, weight: .semibold))
                        .foregroundStyle(QuartetTheme.canvas)
                        .frame(maxWidth: .infinity)
                        .frame(height: 50)
                        .background(QuartetTheme.primaryText, in: RoundedRectangle(cornerRadius: 15))
                    }
                    .disabled(model.isRefreshing || model.connectionState.phase == .connecting)
                    .accessibilityIdentifier("connection-sync-button")
                }
                .padding(20)
            }
            .background(QuartetTheme.canvas)
            .navigationTitle("连接状态")
            .navigationBarTitleDisplayMode(.inline)
        }
        .presentationDetents([.medium, .large])
        .quartetSheetStyle()
    }

    private var statusCard: some View {
        let state = model.connectionState
        return HStack(spacing: 14) {
            Image(systemName: statusIcon)
                .font(.quartet(.large, weight: .semibold))
                .foregroundStyle(statusColor)
                .frame(width: 46, height: 46)
                .background(statusColor.opacity(0.12), in: Circle())

            VStack(alignment: .leading, spacing: 4) {
                Text(statusTitle)
                    .font(.quartet(.regular, weight: .semibold))
                    .foregroundStyle(QuartetTheme.primaryText)
                Text(syncDescription(state))
                    .font(.quartet(.detail))
                    .foregroundStyle(QuartetTheme.secondaryText)
            }
        }
        .padding(16)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18))
        .overlay(RoundedRectangle(cornerRadius: 18).stroke(QuartetTheme.divider))
    }

    private var statusTitle: String {
        let state = model.connectionState
        if model.isRefreshing || state.phase == .connecting { return "正在同步" }
        if !state.isConnected { return "连接中断" }
        if state.isStale { return "当前数据可能已过期" }
        if state.hasPendingSync { return "有状态等待同步" }
        return "连接正常"
    }

    private var statusIcon: String {
        let state = model.connectionState
        if model.isRefreshing || state.phase == .connecting { return "arrow.triangle.2.circlepath" }
        if !state.isConnected { return "wifi.slash" }
        if state.isStale || state.hasPendingSync { return "exclamationmark.arrow.triangle.2.circlepath" }
        return "checkmark"
    }

    private var statusColor: Color {
        let state = model.connectionState
        if !state.isConnected { return QuartetTheme.failed }
        if state.isStale || state.hasPendingSync { return QuartetTheme.warning }
        return QuartetTheme.accent
    }

    private func syncDescription(_ state: AppModel.ConnectionState) -> String {
        guard let date = state.lastSuccessfulSyncAt else {
            return "尚未完成首次同步"
        }
        return "上次成功同步于 \(date.formatted(date: .abbreviated, time: .shortened))"
    }

    private func synchronize() async {
        if model.connectionState.isConnected {
            await model.refreshDashboard()
        } else {
            await model.connect()
        }
    }
}
