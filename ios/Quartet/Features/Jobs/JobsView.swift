import SwiftUI
import UIKit

struct JobsView: View {
    @EnvironmentObject private var model: AppModel
    @Environment(\.scenePhase) private var scenePhase
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
                    sectionHeader
                    jobList
                }
            }
            .background(QuartetTheme.canvas)
            .navigationTitle("运行台")
            .navigationBarTitleDisplayMode(.inline)
            .refreshable { await model.refreshDashboard() }
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
                ToolbarItem(placement: .topBarTrailing) {
                    Button { presentsNewConversation = true } label: {
                        Image(systemName: "plus")
                    }
                    .accessibilityLabel("新建任务")
                    .accessibilityIdentifier("new-conversation-button")
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
            .alert("删除这个 Job？", isPresented: Binding(
                get: { deletingJob != nil },
                set: { if !$0 { deletingJob = nil } }
            )) {
                Button("取消", role: .cancel) {}
                if let deletingJob {
                    Button("删除 \(deletingJob.displayTitle)", role: .destructive) {
                        Task {
                            do { try await model.deleteJob(id: deletingJob.id) }
                            catch { model.present(error) }
                        }
                    }
                }
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
            .padding(.top, 54)
        } else {
            ForEach(Array(model.jobs.enumerated()), id: \.element.id) { index, job in
                VStack(spacing: 0) {
                    HStack(alignment: .center, spacing: 0) {
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
                                displayedStatus: model.displayedStatus(for: job)
                            )
                        }
                        .buttonStyle(.plain)
                        .frame(maxWidth: .infinity, alignment: .leading)
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
                                .frame(width: 44, height: 44)
                                .contentShape(Rectangle())
                        }
                        .accessibilityLabel("\(job.displayTitle) 的更多操作")
                    }

                    if index < model.jobs.count - 1 {
                        Divider()
                            .overlay(QuartetTheme.divider)
                            .padding(.leading, 60)
                    }
                }
                .background(QuartetTheme.surface)
                .task(id: job.id) {
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
                        .fill(QuartetTheme.running)
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
        model.selectedWorkspace?.displayName ?? "全部工作空间"
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

    private var dashboardPollingConfiguration: DashboardPollingConfiguration {
        DashboardPollingConfiguration(
            isActive: scenePhase == .active,
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

private struct JobRow: View {
    let job: JobSummary
    let workspace: WorkspaceSummary?
    let displayedStatus: String

    var body: some View {
        HStack(spacing: 12) {
            JobModeIcon(mode: job.mode, color: modeColor)
                .frame(width: 32, height: 32)
                .accessibilityElement(children: .ignore)
                .accessibilityLabel(modeText)

            VStack(alignment: .leading, spacing: 8) {
                HStack(spacing: 7) {
                    Text(job.displayTitle)
                        .font(.quartet(.control, weight: .semibold))
                        .foregroundStyle(QuartetTheme.primaryText)
                        .lineLimit(1)
                        .frame(maxWidth: .infinity, alignment: .leading)

                    if (job.pinnedAt ?? 0) > 0 {
                        Image(systemName: "pin.fill")
                            .font(.quartet(.compact, weight: .bold))
                            .foregroundStyle(QuartetTheme.accent)
                            .accessibilityHidden(true)
                    }

                    JobStatusIcon(status: displayedStatus)
                        .frame(width: 20, height: 20)
                        .accessibilityElement(children: .ignore)
                        .accessibilityLabel(statusText)
                }

                HStack(spacing: 6) {
                    HStack(spacing: 4) {
                        Circle()
                            .fill(workspaceColor)
                            .frame(width: 6, height: 6)
                        Text(workspaceName)
                            .lineLimit(1)
                    }
                    .layoutPriority(1)

                    Text("·")

                    Text(modelName)
                        .lineLimit(1)

                    Spacer(minLength: 2)

                    if job.scheduleId != nil {
                        Image(systemName: "calendar.badge.clock")
                            .accessibilityLabel("定时任务")
                    }

                    JobSentTime(timestamp: job.updatedAt)
                }
                .font(.quartet(.detail))
                .foregroundStyle(QuartetTheme.secondaryText)
                .lineLimit(1)
            }
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(.leading, 16)
        .padding(.trailing, 6)
        .padding(.vertical, 16)
        .contentShape(Rectangle())
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

    private var modelName: String {
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

    private var workspaceColor: Color {
        Color(hex: workspace?.color) ?? modeColor
    }

    private var modeColor: Color {
        switch job.mode {
        case "graph": QuartetTheme.chartViolet
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
}

private struct JobModeIcon: View {
    let mode: String?
    let color: Color

    var body: some View {
        ZStack {
            RoundedRectangle(cornerRadius: 8, style: .continuous)
                .fill(color.opacity(0.11))

            if mode == "loop" {
                Image(systemName: "arrow.triangle.2.circlepath")
                    .font(.quartet(.control, weight: .semibold))
                    .foregroundStyle(color)
            } else {
                JobModeGlyph(mode: mode)
                    .stroke(
                        color,
                        style: StrokeStyle(lineWidth: 2, lineCap: .round, lineJoin: .round)
                    )
                    .frame(width: 18, height: 18)
            }
        }
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

private struct JobStatusIcon: View {
    let status: String

    var body: some View {
        Group {
            if isLive {
                ProgressView()
                    .controlSize(.small)
                    .tint(statusColor)
            } else {
                ZStack {
                    Circle()
                        .fill(statusColor.opacity(0.12))
                    Circle()
                        .stroke(statusColor.opacity(0.7), lineWidth: 1.2)
                    Image(systemName: statusSymbol)
                        .font(.quartet(.compact, weight: .bold))
                        .foregroundStyle(statusColor)
                }
            }
        }
    }

    private var isLive: Bool {
        status == "pending" || status == "running" || status == "stepStopping"
    }

    private var statusColor: Color {
        QuartetTheme.statusColor(status)
    }

    private var statusSymbol: String {
        switch status {
        case "completed": "checkmark"
        case "failed": "xmark"
        case "timedOut": "clock"
        case "pending": "clock.fill"
        case "awaitingInput": "pause.fill"
        case "stepStopped", "stopped": "stop.fill"
        default: "circle.fill"
        }
    }
}

private struct JobSentTime: View {
    let timestamp: Int64

    var body: some View {
        TimelineView(.periodic(from: .now, by: refreshInterval)) { context in
            Text(formattedTime(relativeTo: context.date))
                .font(.quartet(.detail).monospacedDigit())
                .lineLimit(1)
                .accessibilityLabel("发送时间，\(formattedTime(relativeTo: context.date))")
        }
    }

    private var sentAt: Date {
        timestamp.quartetDate
    }

    private var refreshInterval: TimeInterval {
        let elapsed = Date.now.timeIntervalSince(sentAt)
        if elapsed < 60 { return 1 }
        if elapsed < 3_600 { return 30 }
        return 60
    }

    private func formattedTime(relativeTo now: Date) -> String {
        let elapsed = max(0, now.timeIntervalSince(sentAt))
        if elapsed < 10 {
            return "刚刚"
        }
        if elapsed < 60 {
            return "\(Int(elapsed)) s 前"
        }
        if elapsed < 3_600 {
            return "\(Int(elapsed / 60)) m 前"
        }

        let calendar = Calendar.autoupdatingCurrent
        let components = calendar.dateComponents([.year, .month, .day, .hour, .minute], from: sentAt)
        let time = String(format: "%02d:%02d", components.hour ?? 0, components.minute ?? 0)
        if calendar.isDate(sentAt, inSameDayAs: now) {
            return time
        }

        let monthAndDay = "\(components.month ?? 0)月\(components.day ?? 0)日"
        if calendar.component(.year, from: sentAt) == calendar.component(.year, from: now) {
            return "\(monthAndDay) \(time)"
        }
        return "\(components.year ?? 0)年\(monthAndDay) \(time)"
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
