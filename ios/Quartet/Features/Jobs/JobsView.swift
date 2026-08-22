import SwiftUI
import UIKit

struct JobsView: View {
    @EnvironmentObject private var model: AppModel
    @State private var path: [ChatRoute] = []
    @State private var presentsNewConversation = false
    @State private var renamingJob: JobSummary?
    @State private var renameDraft = ""
    @State private var deletingJob: JobSummary?
    @State private var presentsConnectionStatus = false

    var body: some View {
        NavigationStack(path: $path) {
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 0) {
                    connectionNotice
                    dashboardControls
                    sectionHeader
                    jobList
                }
            }
            .background(QuartetTheme.canvas)
            .navigationTitle("运行台")
            .navigationBarTitleDisplayMode(.large)
            .refreshable { await model.refreshDashboard() }
            .navigationDestination(for: ChatRoute.self) { route in
                if route.summary.mode == "graph", route.targetSessionID == nil {
                    GraphRunView(summary: route.summary)
                } else {
                    JobChatView(route: route)
                }
            }
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    HStack(spacing: 14) {
                        Button { presentsConnectionStatus = true } label: {
                            ConnectionBadge(
                                state: model.connectionState,
                                isRefreshing: model.isRefreshing
                            )
                        }
                        .accessibilityLabel(connectionStatusAccessibilityLabel)
                        .accessibilityIdentifier("connection-status-button")
                        Button { presentsNewConversation = true } label: {
                            Image(systemName: "plus")
                        }
                        .accessibilityLabel("新建对话")
                        .accessibilityIdentifier("new-conversation-button")
                    }
                }
            }
            .sheet(isPresented: $presentsNewConversation) {
                NewConversationView { route in
                    presentsNewConversation = false
                    path.append(route)
                }
            }
            .sheet(isPresented: $presentsConnectionStatus) {
                DashboardConnectionView()
                    .environmentObject(model)
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

    @ViewBuilder
    private var connectionNotice: some View {
        let state = model.connectionState
        if !state.isConnected || state.isStale || state.hasPendingSync {
            Button { presentsConnectionStatus = true } label: {
                HStack(spacing: 12) {
                    Image(systemName: connectionNoticeIcon(state))
                        .font(.system(size: 15, weight: .semibold))
                        .foregroundStyle(connectionNoticeColor(state))
                        .frame(width: 34, height: 34)
                        .background(connectionNoticeColor(state).opacity(0.12), in: Circle())

                    VStack(alignment: .leading, spacing: 2) {
                        Text(connectionHeadline(state))
                            .font(.subheadline.weight(.semibold))
                            .foregroundStyle(QuartetTheme.primaryText)
                        Text(connectionNoticeDetail(state))
                            .font(.caption)
                            .foregroundStyle(QuartetTheme.secondaryText)
                            .lineLimit(1)
                    }

                    Spacer(minLength: 8)
                    Image(systemName: "chevron.right")
                        .font(.caption.weight(.bold))
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

    private var dashboardControls: some View {
        HStack(spacing: 10) {
            Menu {
                workspaceMenuButton(id: nil, title: "全部工作空间", color: QuartetTheme.accent)
                if !model.workspaces.isEmpty {
                    Divider()
                }
                ForEach(model.workspaces) { workspace in
                    workspaceMenuButton(
                        id: workspace.id,
                        title: workspace.displayName,
                        color: Color(hex: workspace.color) ?? QuartetTheme.accent
                    )
                }
            } label: {
                HStack(spacing: 11) {
                    Circle()
                        .fill(selectedWorkspaceColor)
                        .frame(width: 10, height: 10)

                    VStack(alignment: .leading, spacing: 2) {
                        Text(selectedWorkspaceTitle)
                            .font(.subheadline.weight(.semibold))
                            .foregroundStyle(QuartetTheme.primaryText)
                            .lineLimit(1)
                        Text(workspaceSummary)
                            .font(.caption)
                            .foregroundStyle(QuartetTheme.secondaryText)
                            .lineLimit(1)
                    }

                    Spacer(minLength: 8)
                    Image(systemName: "chevron.up.chevron.down")
                        .font(.caption2.weight(.bold))
                        .foregroundStyle(QuartetTheme.secondaryText)
                }
                .padding(.horizontal, 15)
                .frame(maxWidth: .infinity, minHeight: 58, alignment: .leading)
                .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 16))
                .overlay(RoundedRectangle(cornerRadius: 16).stroke(QuartetTheme.divider))
            }
            .accessibilityLabel("工作空间，当前为\(selectedWorkspaceTitle)")
            .accessibilityIdentifier("workspace-selector")

            Menu {
                Button {
                    Task { await model.setHideScheduledJobs(!model.hideScheduledJobs) }
                } label: {
                    Label(
                        model.hideScheduledJobs ? "显示定时任务" : "隐藏定时任务",
                        systemImage: model.hideScheduledJobs ? "calendar.badge.plus" : "calendar.badge.minus"
                    )
                }
                .accessibilityIdentifier("hide-scheduled-jobs-toggle")

                Button { Task { await model.refreshDashboard() } } label: {
                    Label("刷新任务", systemImage: "arrow.clockwise")
                }
                .disabled(model.isRefreshing)
            } label: {
                ZStack(alignment: .topTrailing) {
                    Image(systemName: model.hideScheduledJobs
                          ? "line.3.horizontal.decrease.circle.fill"
                          : "line.3.horizontal.decrease.circle")
                        .font(.system(size: 20, weight: .medium))
                        .foregroundStyle(model.hideScheduledJobs ? QuartetTheme.accent : QuartetTheme.primaryText)
                        .frame(width: 56, height: 58)
                        .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 16))
                        .overlay(RoundedRectangle(cornerRadius: 16).stroke(QuartetTheme.divider))
                }
            }
            .accessibilityLabel(model.hideScheduledJobs ? "任务筛选，已隐藏定时任务" : "任务筛选，显示全部任务")
            .accessibilityIdentifier("job-filter-menu")
        }
        .padding(.horizontal, 20)
        .padding(.top, 10)
        .padding(.bottom, 4)
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
            .padding(.top, 54)
        } else {
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
                    .accessibilityIdentifier("job-\(job.id)")

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
                .background(QuartetTheme.surface)
                .task(id: job.id) {
                    await model.refreshGraphStatusIfNeeded(for: job)
                }
                Divider()
                    .overlay(QuartetTheme.divider)
                    .padding(.leading, 56)
                    .background(QuartetTheme.surface)
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
        HStack {
            Text("最近任务")
                .font(.headline)
                .foregroundStyle(QuartetTheme.primaryText)
            Spacer()
            if model.activeJobCount > 0 {
                HStack(spacing: 6) {
                    Circle()
                        .fill(QuartetTheme.running)
                        .frame(width: 7, height: 7)
                    Text("\(model.activeJobCount) 个进行中")
                }
                .font(.caption.weight(.medium))
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
        model.selectedWorkspace?.displayName ?? "全部工作空间"
    }

    private var selectedWorkspaceColor: Color {
        Color(hex: model.selectedWorkspace?.color) ?? QuartetTheme.accent
    }

    private var workspaceSummary: String {
        let count = "\(model.jobs.count) 个任务"
        return model.hideScheduledJobs ? "\(count) · 已隐藏定时任务" : count
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
        state.isConnected ? QuartetTheme.running : QuartetTheme.failed
    }

    private var connectionStatusAccessibilityLabel: String {
        let state = model.connectionState
        if model.isRefreshing || state.phase == .connecting { return "正在同步" }
        if !state.isConnected { return "连接中断，查看连接状态" }
        if state.isStale || state.hasPendingSync { return "数据待同步，查看连接状态" }
        return "连接正常，查看连接状态"
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
        if state.isStale || state.hasPendingSync { return QuartetTheme.running }
        return QuartetTheme.accent
    }
}

private struct DashboardConnectionView: View {
    @EnvironmentObject private var model: AppModel
    @Environment(\.dismiss) private var dismiss

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 18) {
                    statusCard

                    VStack(alignment: .leading, spacing: 7) {
                        Text("服务地址")
                            .font(.caption.weight(.semibold))
                            .foregroundStyle(QuartetTheme.secondaryText)
                        Text(model.serverAddress)
                            .font(.system(.footnote, design: .monospaced))
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
                                    .font(.subheadline.weight(.semibold))
                                    .foregroundStyle(QuartetTheme.failed)
                                Spacer()
                                Button("复制") { UIPasteboard.general.string = failure }
                                    .font(.caption.weight(.semibold))
                            }
                            Text(failure)
                                .font(.system(.caption, design: .monospaced))
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
                        .font(.headline)
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
            .toolbar {
                ToolbarItem(placement: .confirmationAction) {
                    Button("完成") { dismiss() }
                        .accessibilityIdentifier("connection-status-close")
                }
            }
        }
        .presentationDetents([.medium, .large])
    }

    private var statusCard: some View {
        let state = model.connectionState
        return HStack(spacing: 14) {
            Image(systemName: statusIcon)
                .font(.system(size: 20, weight: .semibold))
                .foregroundStyle(statusColor)
                .frame(width: 46, height: 46)
                .background(statusColor.opacity(0.12), in: Circle())

            VStack(alignment: .leading, spacing: 4) {
                Text(statusTitle)
                    .font(.headline)
                    .foregroundStyle(QuartetTheme.primaryText)
                Text(syncDescription(state))
                    .font(.footnote)
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
        if state.isStale || state.hasPendingSync { return QuartetTheme.running }
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
