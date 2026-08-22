import SwiftUI

struct JobsView: View {
    @EnvironmentObject private var model: AppModel
    @State private var path: [ChatRoute] = []
    @State private var presentsNewConversation = false
    @State private var renamingJob: JobSummary?
    @State private var renameDraft = ""
    @State private var deletingJob: JobSummary?
    @State private var presentsNotifications = false

    var body: some View {
        NavigationStack(path: $path) {
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 0) {
                    overview
                    connectionSummary
                    workspaceFilter
                    jobList
                }
            }
            .background(QuartetTheme.canvas)
            .navigationTitle("运行台")
            .navigationBarTitleDisplayMode(.large)
            .refreshable { await model.refreshDashboard() }
            .onAppear { Task { await model.reloadJobs() } }
            .navigationDestination(for: ChatRoute.self) { route in
                if route.summary.mode == "graph", route.targetSessionID == nil {
                    GraphRunView(summary: route.summary)
                } else {
                    JobChatView(route: route)
                }
            }
            .task(id: model.pendingNotificationDestination) {
                guard let destination = model.pendingNotificationDestination else { return }
                guard let summary = await model.notificationDestinationSummary() else {
                    model.present(APIError(
                        summary: "无法打开通知目标",
                        detail: "暂时无法读取通知对应的 Job，请恢复连接后重新点击该通知。"
                    ))
                    model.clearPendingNotificationDestination()
                    return
                }
                if let workspaceID = destination.workspaceID, workspaceID != model.selectedWorkspaceID {
                    await model.selectWorkspace(workspaceID)
                }
                path = [ChatRoute(summary: summary, targetSessionID: destination.graphSessionID)]
                model.clearPendingNotificationDestination()
            }
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    Button { presentsNotifications = true } label: {
                        ZStack(alignment: .topTrailing) {
                            Image(systemName: "bell")
                            if !model.notifications.filter(\.isUnread).isEmpty {
                                Circle()
                                    .fill(QuartetTheme.failed)
                                    .frame(width: 9, height: 9)
                                    .offset(x: 4, y: -4)
                            }
                        }
                    }
                    .accessibilityLabel("通知中心")
                }
                ToolbarItem(placement: .topBarTrailing) {
                    HStack(spacing: 14) {
                        ConnectionBadge(state: model.connectionState)
                        Button { presentsNewConversation = true } label: {
                            Image(systemName: "plus")
                        }
                        .accessibilityLabel("新建对话")
                    }
                }
            }
            .sheet(isPresented: $presentsNewConversation) {
                NewConversationView { route in
                    presentsNewConversation = false
                    path.append(route)
                }
            }
            .sheet(isPresented: $presentsNotifications) {
                NavigationStack {
                    NotificationsView()
                        .environmentObject(model)
                }
            }
            .alert("重命名 Job", isPresented: Binding(
                get: { renamingJob != nil },
                set: { if !$0 { renamingJob = nil } }
            )) {
                TextField("Job 标题", text: $renameDraft)
                Button("取消", role: .cancel) { renamingJob = nil }
                Button("保存") {
                    guard let job = renamingJob else { return }
                    let title = String(renameDraft.trimmingCharacters(in: .whitespacesAndNewlines).prefix(200))
                    Task {
                        do { try await model.renameJob(id: job.id, title: title) }
                        catch { model.present(error) }
                    }
                    renamingJob = nil
                }
                .disabled(renameDraft.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
            } message: {
                Text("最多 200 个字符。")
            }
            .confirmationDialog("删除这个 Job？", isPresented: Binding(
                get: { deletingJob != nil },
                set: { if !$0 { deletingJob = nil } }
            ), titleVisibility: .visible) {
                if let deletingJob {
                    Button("删除 \(deletingJob.displayTitle)", role: .destructive) {
                        Task {
                            do { try await model.deleteJob(id: deletingJob.id) }
                            catch { model.present(error) }
                        }
                    }
                }
                Button("取消", role: .cancel) {}
            } message: {
                Text("运行中的任务会先停止，随后删除相关会话。")
            }
        }
    }

    private var overview: some View {
        VStack(alignment: .leading, spacing: 16) {
            HStack(alignment: .lastTextBaseline) {
                VStack(alignment: .leading, spacing: 4) {
                    Text(String(model.activeJobCount))
                        .font(.system(size: 46, weight: .bold, design: .rounded))
                        .foregroundStyle(model.activeJobCount > 0 ? QuartetTheme.running : QuartetTheme.primaryText)
                    Text("正在运行")
                        .font(.system(.caption, design: .monospaced).weight(.semibold))
                        .foregroundStyle(QuartetTheme.secondaryText)
                }
                Spacer()
                VStack(alignment: .trailing, spacing: 4) {
                    Text(String(model.jobs.count))
                        .font(.system(size: 24, weight: .semibold, design: .rounded))
                    Text("当前列表")
                        .font(.caption)
                        .foregroundStyle(QuartetTheme.secondaryText)
                }
            }
            RunningPulseLine(active: model.activeJobCount > 0)
        }
        .padding(.horizontal, 20)
        .padding(.top, 12)
        .padding(.bottom, 22)
    }

    private var connectionSummary: some View {
        let state = model.connectionState
        return VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 8) {
                Circle()
                    .fill(QuartetTheme.statusColor(model.connectionState.isConnected ? "running" : "failed"))
                    .frame(width: 8, height: 8)
                Text(connectionHeadline(state))
                    .font(.system(.subheadline, design: .monospaced).weight(.semibold))
                    .foregroundStyle(QuartetTheme.primaryText)
                Spacer()
                if model.isRefreshing {
                    ProgressView()
                        .scaleEffect(0.8)
                }
            }
            Text(connectionDetail(state))
                .font(.footnote)
                .foregroundStyle(QuartetTheme.secondaryText)
                .lineSpacing(3)
            if state.isStale || state.hasPendingSync {
                Label(state.isStale ? "当前数据可能已过期，恢复连接后会自动刷新。" : "存在待同步状态，应用返回前台后会重新刷新。", systemImage: "arrow.triangle.2.circlepath")
                    .font(.caption)
                    .foregroundStyle(QuartetTheme.secondaryText)
            }
        }
        .padding(.horizontal, 20)
        .padding(.bottom, 16)
    }

    private var workspaceFilter: some View {
        ScrollView(.horizontal, showsIndicators: false) {
            HStack(spacing: 8) {
                workspaceButton(id: nil, title: "全部", color: QuartetTheme.accent)
                ForEach(model.workspaces) { workspace in
                    workspaceButton(
                        id: workspace.id,
                        title: workspace.displayName,
                        color: Color(hex: workspace.color) ?? QuartetTheme.accent
                    )
                }
            }
            .padding(.horizontal, 20)
        }
        .padding(.bottom, 16)
    }

    @ViewBuilder
    private var jobList: some View {
        if model.jobs.isEmpty && model.isRefreshing {
            HStack { Spacer(); ProgressView(); Spacer() }
                .padding(.top, 80)
        } else if model.jobs.isEmpty {
            ContentUnavailableView(
                "暂无 Job",
                systemImage: "waveform.path",
                description: Text(model.selectedWorkspace == nil ? "当前实例还没有任务。" : "这个工作空间还没有任务。")
            )
            .padding(.top, 54)
        } else {
            VStack(spacing: 0) {
                ForEach(model.jobs) { job in
                    HStack(spacing: 0) {
                        NavigationLink {
                            if job.mode == "graph" {
                                GraphRunView(summary: job)
                            } else {
                                JobChatView(route: ChatRoute(
                                    summary: job,
                                    agentType: job.agentId,
                                    modelID: job.modelId,
                                    modeID: job.acpMode,
                                    thoughtLevelID: job.acpThoughtLevel
                                ))
                            }
                        } label: {
                            JobRow(
                                job: job,
                                workspace: workspace(for: job),
                                displayedStatus: model.displayedStatus(for: job),
                                displayedStatusLabel: model.displayedStatusLabel(for: job)
                            )
                        }
                        .buttonStyle(.plain)

                        Menu {
                            Button { togglePinned(job) } label: {
                                Label((job.pinnedAt ?? 0) > 0 ? "取消置顶" : "置顶", systemImage: (job.pinnedAt ?? 0) > 0 ? "pin.slash" : "pin")
                            }
                            Button { renamingJob = job; renameDraft = job.displayTitle } label: {
                                Label("重命名", systemImage: "pencil")
                            }
                            Button(role: .destructive) { deletingJob = job } label: {
                                Label("删除", systemImage: "trash")
                            }
                        } label: {
                            Image(systemName: "ellipsis")
                                .foregroundStyle(QuartetTheme.secondaryText)
                                .frame(width: 44, height: 48)
                        }
                        .accessibilityLabel("\(job.displayTitle) 的更多操作")
                    }
                    Divider().overlay(QuartetTheme.divider).padding(.leading, 56)
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
                }
            }
            .background(QuartetTheme.surface)
            .task(id: model.jobs.map(\.id)) {
                for job in model.jobs where job.mode == "graph" {
                    await model.refreshGraphStatusIfNeeded(for: job)
                }
            }
        }
    }

    private func workspaceButton(id: String?, title: String, color: Color) -> some View {
        let selected = model.selectedWorkspaceID == id
        return Button { Task { await model.selectWorkspace(id) } } label: {
            HStack(spacing: 7) {
                Circle().fill(color).frame(width: 7, height: 7)
                Text(title).lineLimit(1)
            }
            .font(.subheadline.weight(.medium))
            .foregroundStyle(selected ? QuartetTheme.canvas : QuartetTheme.primaryText)
            .padding(.horizontal, 13)
            .frame(height: 36)
            .background(selected ? QuartetTheme.primaryText : QuartetTheme.elevated, in: Capsule())
        }
    }

    private func workspace(for job: JobSummary) -> WorkspaceSummary? {
        model.workspaces.first { $0.id == job.workspaceId }
    }

    private func togglePinned(_ job: JobSummary) {
        Task {
            do { try await model.setJobPinned(id: job.id, pinned: (job.pinnedAt ?? 0) == 0) }
            catch { model.present(error) }
        }
    }

    private func connectionHeadline(_ state: AppModel.ConnectionState) -> String {
        if state.phase == .connecting {
            return "正在同步服务器状态"
        }
        if state.isStale {
            return "当前展示的是缓存快照"
        }
        if state.isConnected {
            return "连接正常"
        }
        return "连接中断"
    }

    private func connectionDetail(_ state: AppModel.ConnectionState) -> String {
        if let lastSuccessfulSyncAt = state.lastSuccessfulSyncAt {
            if let failure = state.lastFailureMessage, state.isStale {
                return "上次成功同步 \(lastSuccessfulSyncAt.formatted(date: .omitted, time: .shortened))，最近失败：\(failure)"
            }
            return "上次成功同步 \(lastSuccessfulSyncAt.formatted(date: .omitted, time: .shortened))"
        }
        return state.lastFailureMessage ?? "尚未完成首次同步。"
    }
}

private struct JobRow: View {
    let job: JobSummary
    let workspace: WorkspaceSummary?
    let displayedStatus: String
    let displayedStatusLabel: String

    var body: some View {
        HStack(alignment: .top, spacing: 14) {
            VStack(spacing: 5) {
                Circle()
                    .fill(QuartetTheme.statusColor(colorKey))
                    .frame(width: 10, height: 10)
                Rectangle()
                    .fill(QuartetTheme.divider)
                    .frame(width: 1, height: 42)
            }
            .padding(.top, 5)

            VStack(alignment: .leading, spacing: 8) {
                HStack {
                    Text(job.displayTitle)
                        .font(.headline)
                        .foregroundStyle(QuartetTheme.primaryText)
                        .lineLimit(2)
                    Spacer(minLength: 8)
                }
                HStack(spacing: 8) {
                    Text(job.modeLabel)
                    Text(displayedStatusLabel)
                        .foregroundStyle(QuartetTheme.statusColor(colorKey))
                    if let workspace { Text(workspace.displayName) }
                    if job.scheduleId != nil { Text("SCHEDULE") }
                }
                .font(.system(.caption, design: .monospaced).weight(.medium))
                .foregroundStyle(QuartetTheme.secondaryText)
                .lineLimit(1)

                HStack {
                    Text(job.updatedAt.quartetDate, style: .relative)
                    if let agentId = job.agentId, !agentId.isEmpty {
                        Text("·")
                        Text(agentId).lineLimit(1)
                    } else if let modelId = job.modelId, !modelId.isEmpty {
                        Text("·")
                        Text(modelId).lineLimit(1)
                    }
                    Spacer()
                    if (job.pinnedAt ?? 0) > 0 { Image(systemName: "pin.fill") }
                }
                .font(.caption)
                .foregroundStyle(QuartetTheme.secondaryText)
            }
        }
        .padding(.horizontal, 20)
        .padding(.vertical, 16)
    }

    private var colorKey: String {
        switch displayedStatus {
        case "running", "pending", "awaitingInput":
            return "running"
        case "completed":
            return "completed"
        case "failed", "timedOut":
            return "failed"
        default:
            return "stopped"
        }
    }
}

private struct ConnectionBadge: View {
    let state: AppModel.ConnectionState

    var body: some View {
        HStack(spacing: 6) {
            Circle().fill(state.isConnected ? QuartetTheme.accent : QuartetTheme.failed).frame(width: 7, height: 7)
            Text(state.isConnected ? (state.isStale ? "STALE" : "ONLINE") : "OFFLINE")
        }
        .font(.system(size: 10, weight: .bold, design: .monospaced))
        .foregroundStyle(QuartetTheme.secondaryText)
        .accessibilityLabel(state.isConnected ? (state.isStale ? "缓存数据" : "已连接") : "未连接")
    }
}

private extension Color {
    init?(hex: String?) {
        guard let hex else { return nil }
        let cleaned = hex.trimmingCharacters(in: CharacterSet.alphanumerics.inverted)
        guard cleaned.count == 6, let value = UInt64(cleaned, radix: 16) else { return nil }
        self.init(
            red: Double((value >> 16) & 0xff) / 255,
            green: Double((value >> 8) & 0xff) / 255,
            blue: Double(value & 0xff) / 255
        )
    }
}
