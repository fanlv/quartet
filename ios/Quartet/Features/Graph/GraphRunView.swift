import SwiftUI
import UIKit

struct GraphRunView: View {
    @EnvironmentObject private var appModel: AppModel
    @Environment(\.scenePhase) private var scenePhase
    let summary: JobSummary

    @State private var snapshot: GraphRunStatusResponse?
    @State private var loading = true
    @State private var pendingAction: GraphAction?
    @State private var confirmation: GraphAction?

    private var status: String { snapshot?.run?.status ?? summary.status }
    private var progress: GraphProgressSummary? { snapshot?.progress ?? snapshot?.run?.progress }
    private var run: GraphRunSummary? { snapshot?.run }
    private var instancesList: [GraphInstanceSummary] { snapshot?.instances ?? [] }
    private var currentInstance: GraphInstanceSummary? { GraphRunSelection.currentInstance(from: instancesList) }
    private var refreshPolicy: GraphRefreshPolicy { GraphRefreshPolicy(status: status) }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 20) {
                runHeader
                actions
                if loading && snapshot == nil {
                    HStack { Spacer(); ProgressView(); Spacer() }.padding(.top, 50)
                } else {
                    instances
                }
            }
            .padding(20)
        }
        .background(QuartetTheme.canvas)
        .navigationTitle(summary.displayTitle)
        .navigationBarTitleDisplayMode(.inline)
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                NavigationLink {
                    JobDetailView(summary: summary)
                } label: {
                    Image(systemName: "info.circle")
                }
                .accessibilityLabel("Job 详情")
            }
            .sharedBackgroundVisibility(.hidden)
        }
        .quartetPlainNavigationBackButton()
        .refreshable { await refresh() }
        .task {
            await refresh()
        }
        .task(id: refreshPolicy.id) {
            guard let interval = refreshPolicy.interval else { return }
            while !Task.isCancelled {
                try? await Task.sleep(for: interval)
                guard !Task.isCancelled else { return }
                await refresh(silent: true)
            }
        }
        .onChange(of: scenePhase) { _, phase in
            guard phase == .active else { return }
            Task { await refresh(silent: snapshot != nil) }
        }
        .alert(
            confirmation?.confirmationTitle ?? "控制工作流",
            isPresented: Binding(
                get: { confirmation != nil },
                set: { if !$0 { confirmation = nil } }
            )
        ) {
            if let confirmation {
                Button("关闭", role: .cancel) {}
                Button(confirmation.label, role: confirmation.isDestructive ? .destructive : nil) {
                    Task { await perform(confirmation) }
                }
            }
        } message: {
            if let confirmation {
                Text(confirmation.confirmationMessage(
                    runID: run?.id ?? "未知",
                    nodeName: currentInstance?.displayNameWithPath ?? "无当前节点"
                ))
            }
        }
    }

    private var runHeader: some View {
        VStack(alignment: .leading, spacing: 16) {
            HStack {
                VStack(alignment: .leading, spacing: 4) {
                    Text("GRAPH RUN")
                        .font(.system(.caption, design: .monospaced).weight(.bold))
                        .foregroundStyle(QuartetTheme.secondaryText)
                    Text(statusLabel(status))
                        .font(.title2.bold())
                        .foregroundStyle(QuartetTheme.statusColor(colorStatus(status)))
                }
                Spacer()
                if let progress {
                    Text("\(progress.completedCount)/\(progress.totalCount)")
                        .font(.system(size: 30, weight: .bold, design: .rounded))
                }
            }
            HStack(spacing: 10) {
                GraphInfoChip(title: "RUN ID", value: run?.id ?? "—")
                if let currentInstance {
                    GraphInfoChip(title: "当前节点", value: currentInstance.displayNameWithPath)
                }
            }
            ProgressView(value: completionFraction)
                .tint(QuartetTheme.statusColor(colorStatus(status)))
            HStack(spacing: 14) {
                metric("RUN", progress?.runningCount ?? 0, QuartetTheme.running)
                metric("FAIL", progress?.failedCount ?? 0, QuartetTheme.failed)
                metric("SKIP", progress?.skippedCount ?? 0, QuartetTheme.stopped)
            }
            GraphRunMetaGrid(run: run, currentInstance: currentInstance)
            if let error = currentInstance?.error ?? snapshot?.run?.lastError {
                Button {
                    appModel.present(APIError(summary: "Graph 节点错误", detail: error.fullDetail))
                } label: {
                    Label(error.message, systemImage: "exclamationmark.triangle.fill")
                        .font(.caption)
                        .foregroundStyle(QuartetTheme.failed)
                        .lineLimit(3)
                }
                .buttonStyle(.plain)
            } else if let lastError = progress?.lastError, !lastError.isEmpty {
                Button { appModel.present(APIError(summary: "Graph 运行错误", detail: lastError)) } label: {
                    Label(lastError, systemImage: "exclamationmark.triangle.fill")
                        .font(.caption)
                        .foregroundStyle(QuartetTheme.failed)
                        .lineLimit(3)
                }
                .buttonStyle(.plain)
            }
        }
        .padding(18)
        .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18))
        .overlay(RoundedRectangle(cornerRadius: 18).stroke(QuartetTheme.divider))
    }

    @ViewBuilder
    private var actions: some View {
        let available = GraphAction.available(for: status)
        if !available.isEmpty {
            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 10) {
                    ForEach(available) { action in
                        Button { confirmation = action } label: {
                            Label(action.label, systemImage: action.icon)
                                .font(.subheadline.weight(.semibold))
                                .padding(.horizontal, 14)
                                .frame(height: 42)
                                .foregroundStyle(action.isDestructive ? QuartetTheme.failed : QuartetTheme.primaryText)
                                .background(QuartetTheme.elevated, in: Capsule())
                        }
                        .disabled(pendingAction != nil)
                    }
                }
            }
        }
    }

    @ViewBuilder
    private var instances: some View {
        let rows = instancesList
        if rows.isEmpty {
            ContentUnavailableView("暂无节点状态", systemImage: "point.3.connected.trianglepath.dotted")
        } else {
            VStack(alignment: .leading, spacing: 0) {
                Text("EXECUTION TRACE")
                    .font(.system(.caption, design: .monospaced).weight(.bold))
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .padding(.bottom, 10)
                ForEach(rows) { instance in
                    GraphInstanceRow(summary: summary, instance: instance)
                        .environmentObject(appModel)
                    if instance.id != rows.last?.id { Divider().overlay(QuartetTheme.divider).padding(.leading, 32) }
                }
            }
            .padding(16)
            .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18))
            .overlay(RoundedRectangle(cornerRadius: 18).stroke(QuartetTheme.divider))
        }
    }

    private var completionFraction: Double {
        guard let progress, progress.totalCount > 0 else { return 0 }
        return min(1, Double(progress.completedCount) / Double(progress.totalCount))
    }

    private func metric(_ label: String, _ value: Int, _ color: Color) -> some View {
        HStack(spacing: 5) {
            Circle().fill(color).frame(width: 6, height: 6)
            Text("\(label) \(value)")
        }
        .font(.system(size: 10, weight: .bold, design: .monospaced))
        .foregroundStyle(QuartetTheme.secondaryText)
    }

    private func refresh(silent: Bool = false) async {
        if !silent { loading = true }
        defer {
            if !silent { loading = false }
        }
        do {
            let response = try await appModel.graphRunStatus(jobID: summary.id)
            snapshot = response
            appModel.observeGraphStatus(job: summary, response: response)
        } catch {
            if !silent { appModel.present(error) }
        }
    }

    private func perform(_ action: GraphAction) async {
        pendingAction = action
        defer { pendingAction = nil }
        do {
            let response = try await appModel.performGraphAction(jobID: summary.id, action: action.rawValue)
            if let run = response.run {
                snapshot = GraphRunStatusResponse(
                    run: run,
                    progress: run.progress ?? snapshot?.progress,
                    instances: snapshot?.instances,
                    edges: snapshot?.edges,
                    eventCount: snapshot?.eventCount,
                    agents: snapshot?.agents
                )
            }
            await refresh(silent: true)
            await appModel.reloadJobs()
        } catch { appModel.present(error) }
    }

    private func colorStatus(_ status: String) -> String {
        switch status {
        case "running", "pending", "stepStopping", "awaitingInput", "recovering": "running"
        case "completed": "completed"
        case "failed", "timedOut": "failed"
        default: "stopped"
        }
    }

    private func statusLabel(_ status: String) -> String {
        switch status {
        case "pending": "等待调度"
        case "running": "运行中"
        case "stepStopping": "当前步骤后停止中"
        case "stepStopped": "已在步骤后停止"
        case "stopped": "已停止"
        case "completed": "已完成"
        case "failed": "失败"
        case "timedOut": "已超时"
        case "recovering": "等待恢复"
        case "awaitingInput": "等待人工讨论"
        default: status
        }
    }
}

private struct GraphRunMetaGrid: View {
    let run: GraphRunSummary?
    let currentInstance: GraphInstanceSummary?

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .top, spacing: 12) {
                GraphMetaTile(title: "开始时间", value: GraphFormatters.dateTime(run?.startedAt))
                GraphMetaTile(title: "持续时长", value: runDurationValue)
            }
            HStack(alignment: .top, spacing: 12) {
                GraphMetaTile(title: "当前节点类型", value: currentInstance?.nodeType.uppercased() ?? "—")
                GraphMetaTile(title: "当前节点状态", value: currentInstance.map { GraphRunSelection.statusLabel(for: $0.status) } ?? "—")
            }
        }
    }

    private var runDurationValue: String {
        guard let run else { return "—" }
        return GraphFormatters.durationLabel(
            startedAt: run.startedAt,
            finishedAt: run.finishedAt,
            fallbackDurationMs: nil
        )
    }
}

private struct GraphInstanceRow: View {
    @EnvironmentObject private var appModel: AppModel
    let summary: JobSummary
    let instance: GraphInstanceSummary

    var body: some View {
        VStack(alignment: .leading, spacing: 9) {
            HStack {
                Circle().fill(QuartetTheme.statusColor(mappedStatus)).frame(width: 8, height: 8)
                VStack(alignment: .leading, spacing: 3) {
                    Text(instance.displayName)
                        .font(.headline)
                        .lineLimit(2)
                    if !instance.pathSummary.isEmpty {
                        Text(instance.pathSummary)
                            .font(.system(size: 10, weight: .medium, design: .monospaced))
                            .foregroundStyle(QuartetTheme.secondaryText)
                    }
                }
                Spacer()
                VStack(alignment: .trailing, spacing: 6) {
                    Text(GraphRunSelection.statusLabel(for: instance.status).uppercased())
                        .font(.system(size: 9, weight: .bold, design: .monospaced))
                        .foregroundStyle(QuartetTheme.statusColor(mappedStatus))
                    if let sessionID = instance.preferredSessionID {
                        NavigationLink {
                            if instance.nodeType.lowercased() == "shell" {
                                GraphNodeSessionView(
                                    sessionID: sessionID,
                                    nodeTitle: instance.displayName,
                                    nodeType: instance.nodeType,
                                    displayPath: instance.pathSummary
                                )
                            } else {
                                JobChatView(route: ChatRoute(
                                    summary: summary,
                                    targetSessionID: sessionID
                                ))
                            }
                        } label: {
                            Label(instance.sessionEntryLabel, systemImage: instance.sessionEntryIcon)
                                .font(.caption.weight(.semibold))
                                .foregroundStyle(QuartetTheme.accent)
                        }
                    }
                }
            }
            HStack(spacing: 10) {
                Text(instance.nodeType.uppercased())
                Text("V\(instance.version)")
                if let startedAt = instance.startedAt {
                    Text(GraphFormatters.dateTime(startedAt))
                }
                Text(instanceDurationText)
            }
            .font(.system(size: 10, design: .monospaced))
            .foregroundStyle(QuartetTheme.secondaryText)
            if let sessionID = instance.preferredSessionID {
                Text("SESSION \(sessionID)")
                    .font(.system(size: 10, design: .monospaced))
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .textSelection(.enabled)
            }
            if let error = instance.error {
                Button {
                    appModel.present(APIError(summary: "节点执行错误", detail: error.fullDetail))
                } label: {
                    HStack(alignment: .top, spacing: 8) {
                        Image(systemName: "exclamationmark.triangle.fill")
                        Text(error.message)
                            .font(.caption)
                            .foregroundStyle(QuartetTheme.failed)
                            .multilineTextAlignment(.leading)
                        Spacer()
                        Text("完整错误")
                            .font(.caption2.weight(.semibold))
                            .foregroundStyle(QuartetTheme.failed)
                    }
                }
                .buttonStyle(.plain)
            } else if let reason = instance.blockedReason, !reason.isEmpty {
                Text(reason).font(.caption).foregroundStyle(QuartetTheme.secondaryText)
            }
        }
        .padding(.vertical, 13)
    }

    private var mappedStatus: String {
        switch instance.status {
        case "succeeded": "completed"
        case "failed": "failed"
        case "running", "awaitingInput", "pending": "running"
        default: "stopped"
        }
    }

    private var instanceDurationText: String {
        GraphFormatters.durationLabel(
            startedAt: instance.startedAt,
            finishedAt: instance.finishedAt,
            fallbackDurationMs: instance.durationMs
        )
    }
}

private enum GraphAction: String, Identifiable, Equatable {
    case stepStop = "step-stop"
    case cancelStop = "cancel-stop"
    case stop
    case resume
    case completeDiscussion = "continue"

    var id: String { rawValue }
    var label: String {
        switch self {
        case .stepStop: "步骤后停止"
        case .cancelStop: "继续运行"
        case .stop: "立即停止"
        case .resume: "恢复运行"
        case .completeDiscussion: "讨论完成"
        }
    }
    var icon: String {
        switch self {
        case .stepStop: "pause.circle"
        case .cancelStop: "play.circle"
        case .stop: "stop.fill"
        case .resume, .completeDiscussion: "play.fill"
        }
    }
    var isDestructive: Bool { self == .stop }
    var confirmationTitle: String { "确认\(label)？" }
    func confirmationMessage(runID: String, nodeName: String) -> String {
        "Run ID: \(runID)\n当前节点: \(nodeName)"
    }

    static func available(for status: String) -> [GraphAction] {
        switch status {
        case "pending": [.stop]
        case "running": [.stepStop, .stop]
        case "stepStopping": [.cancelStop, .stop]
        case "failed", "stepStopped", "stopped", "timedOut", "recovering": [.resume]
        case "awaitingInput": [.completeDiscussion]
        default: []
        }
    }
}

private struct GraphRefreshPolicy: Equatable {
    let id: String
    let interval: Duration?

    init(status: String) {
        switch status {
        case "pending", "running", "stepStopping":
            id = "live"
            interval = .seconds(2)
        case "awaitingInput", "recovering", "stepStopped":
            id = "parked"
            interval = .seconds(4)
        default:
            id = "idle"
            interval = nil
        }
    }
}

private enum GraphRunSelection {
    static func currentInstance(from instances: [GraphInstanceSummary]) -> GraphInstanceSummary? {
        if let active = newest(
            in: instances.filter { ["running", "awaitingInput", "pending"].contains($0.status) }
        ) {
            return active
        }
        if let failed = newest(in: instances.filter { $0.status == "failed" }) {
            return failed
        }
        return newest(in: instances)
    }

    static func newest(in instances: [GraphInstanceSummary]) -> GraphInstanceSummary? {
        instances.max { lhs, rhs in
            sortKey(lhs) < sortKey(rhs)
        }
    }

    static func sortKey(_ instance: GraphInstanceSummary) -> (Int64, Int64) {
        let started = instance.startedAt ?? 0
        let finished = instance.finishedAt ?? 0
        return (max(started, finished), started)
    }

    static func statusLabel(for status: String) -> String {
        switch status {
        case "pending": "等待中"
        case "running": "运行中"
        case "succeeded": "已完成"
        case "failed": "失败"
        case "skipped": "已跳过"
        case "interrupted": "已中断"
        case "awaitingInput": "等待讨论"
        default: status
        }
    }
}

private enum GraphFormatters {
    static func dateTime(_ timestamp: Int64?) -> String {
        guard let timestamp else { return "—" }
        let formatter = DateFormatter()
        formatter.locale = Locale(identifier: "zh_CN")
        formatter.dateFormat = "yyyy-MM-dd HH:mm:ss"
        return formatter.string(from: timestamp.quartetDate)
    }

    static func durationLabel(startedAt: Int64?, finishedAt: Int64?, fallbackDurationMs: Int64?) -> String {
        if let startedAt {
            let endDate = finishedAt.map { Date(timeIntervalSince1970: TimeInterval($0) / 1_000) } ?? Date()
            let startedDate = startedAt.quartetDate
            let milliseconds = max(0, Int64(endDate.timeIntervalSince(startedDate) * 1_000))
            return formattedDuration(milliseconds: milliseconds)
        }
        if let fallbackDurationMs {
            return formattedDuration(milliseconds: fallbackDurationMs)
        }
        return "—"
    }

    static func formattedDuration(milliseconds: Int64) -> String {
        let totalSeconds = max(0, milliseconds / 1_000)
        let hours = totalSeconds / 3_600
        let minutes = (totalSeconds % 3_600) / 60
        let seconds = totalSeconds % 60
        let remainderMs = milliseconds % 1_000

        if hours > 0 {
            return String(format: "%02lld:%02lld:%02lld", hours, minutes, seconds)
        }
        if minutes > 0 || seconds > 0 {
            return String(format: "%02lld:%02lld", minutes, seconds)
        }
        return "\(remainderMs) ms"
    }
}

private struct GraphMetaTile: View {
    let title: String
    let value: String

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            Text(title)
                .font(.system(size: 10, weight: .bold, design: .monospaced))
                .foregroundStyle(QuartetTheme.secondaryText)
            Text(value)
                .font(.subheadline.weight(.semibold))
                .foregroundStyle(QuartetTheme.primaryText)
                .lineLimit(2)
                .textSelection(.enabled)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .padding(12)
        .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 14))
    }
}

private struct GraphInfoChip: View {
    let title: String
    let value: String

    var body: some View {
        VStack(alignment: .leading, spacing: 3) {
            Text(title)
                .font(.system(size: 9, weight: .bold, design: .monospaced))
                .foregroundStyle(QuartetTheme.secondaryText)
            Text(value)
                .font(.caption.weight(.semibold))
                .foregroundStyle(QuartetTheme.primaryText)
                .lineLimit(1)
                .textSelection(.enabled)
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 10)
        .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 14))
    }
}

private struct GraphNodeSessionView: View {
    @EnvironmentObject private var appModel: AppModel

    let sessionID: String
    let nodeTitle: String
    let nodeType: String
    let displayPath: String

    @State private var loading = true
    @State private var errorDetail: String?
    @State private var messages: [HistoryMessage] = []
    @State private var modelID = ""
    @State private var agentType: String?
    @State private var workdir: String?
    @State private var acpMode: String?
    @State private var thoughtLevel: String?

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 16) {
                sessionHeader
                if loading && messages.isEmpty {
                    HStack { Spacer(); ProgressView(); Spacer() }
                        .padding(.top, 50)
                } else if messages.isEmpty {
                    ContentUnavailableView("暂无节点会话内容", systemImage: "text.bubble")
                } else {
                    LazyVStack(spacing: 14) {
                        ForEach(messages) { message in
                            GraphSessionBubble(message: message)
                        }
                    }
                }
            }
            .padding(16)
        }
        .background(QuartetTheme.canvas)
        .navigationTitle(sessionTitle)
        .navigationBarTitleDisplayMode(.inline)
        .quartetPlainNavigationBackButton()
        .refreshable { await load() }
        .task(id: sessionID) { await load() }
    }

    private var sessionTitle: String {
        switch nodeType.lowercased() {
        case "shell": return "节点输出"
        case "clarify": return "澄清会话"
        default: return "节点会话"
        }
    }

    private var sessionHeader: some View {
        VStack(alignment: .leading, spacing: 12) {
            Text(nodeTitle)
                .font(.title3.bold())
                .foregroundStyle(QuartetTheme.primaryText)
            HStack(spacing: 10) {
                GraphInfoChip(title: "节点类型", value: nodeType.uppercased())
                if !displayPath.isEmpty {
                    GraphInfoChip(title: "执行路径", value: displayPath)
                }
            }
            VStack(alignment: .leading, spacing: 8) {
                GraphMetaTile(title: "SESSION ID", value: sessionID)
                HStack(alignment: .top, spacing: 12) {
                    GraphMetaTile(title: "模型", value: AgentConfigurationDisplay.modelName(
                        modelID,
                        agentReference: agentType,
                        agents: appModel.agentCatalogSnapshot
                    ) ?? "—")
                    GraphMetaTile(title: "Agent", value: agentType ?? "—")
                }
                HStack(alignment: .top, spacing: 12) {
                    GraphMetaTile(title: "模式", value: AgentConfigurationDisplay.modeName(
                        acpMode,
                        agentReference: agentType,
                        agents: appModel.agentCatalogSnapshot
                    ) ?? "—")
                    GraphMetaTile(title: "思考等级", value: AgentConfigurationDisplay.thoughtLevelName(
                        thoughtLevel,
                        agentReference: agentType,
                        agents: appModel.agentCatalogSnapshot
                    ) ?? "—")
                }
                if let workdir, !workdir.isEmpty {
                    GraphMetaTile(title: "Workdir", value: workdir)
                }
                if let errorDetail, !errorDetail.isEmpty {
                    Button {
                        appModel.present(APIError(summary: "节点会话加载失败", detail: errorDetail))
                    } label: {
                        Label("节点会话加载失败，查看详情", systemImage: "exclamationmark.triangle.fill")
                            .font(.caption)
                            .foregroundStyle(QuartetTheme.failed)
                    }
                    .buttonStyle(.plain)
                }
            }
        }
        .padding(18)
        .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18))
        .overlay(RoundedRectangle(cornerRadius: 18).stroke(QuartetTheme.divider))
    }

    private func load() async {
        loading = true
        defer { loading = false }
        if appModel.agentCatalogSnapshot.isEmpty {
            await appModel.refreshAgentCatalog()
        }
        do {
            let response = try await appModel.apiClient().sessionMessages(id: sessionID)
            messages = response.messages
            modelID = response.modelId
            agentType = response.type
            workdir = response.workdir
            acpMode = response.acpMode
            thoughtLevel = response.acpThoughtLevel
            errorDetail = nil
        } catch let apiError as APIError {
            errorDetail = apiError.detail
        } catch {
            errorDetail = String(describing: error)
        }
    }
}

private struct GraphSessionBubble: View {
    let message: HistoryMessage

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 7) {
                Text(label)
                Spacer()
                if let startedAt = message.startedAt {
                    Text(GraphFormatters.dateTime(startedAt))
                }
            }
            .font(.system(size: 10, weight: .bold, design: .monospaced))
            .foregroundStyle(labelColor)

            Text(primaryContent.isEmpty ? "…" : primaryContent)
                .font(message.role == "tool" || message.isShellOutput == true ? .system(.footnote, design: .monospaced) : .body)
                .foregroundStyle(QuartetTheme.primaryText)
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)

            ForEach(message.imageUrls ?? [], id: \.self) { path in
                GraphAuthenticatedImage(path: path)
            }

            if let detail = detailText, !detail.isEmpty {
                DisclosureGroup("调用详情") {
                    Text(detail)
                        .font(.system(.caption, design: .monospaced))
                        .textSelection(.enabled)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .padding(.top, 8)
                }
                .font(.caption)
                .foregroundStyle(QuartetTheme.secondaryText)
            }
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(background, in: RoundedRectangle(cornerRadius: 16))
        .overlay(RoundedRectangle(cornerRadius: 16).stroke(border, lineWidth: 1))
    }

    private var label: String {
        if message.isShellOutput == true { return "SHELL OUTPUT" }
        if message.role == "user" { return "YOU" }
        if message.role == "tool" { return "TOOL" }
        if message.isThinking == true { return "THINKING" }
        if message.role == "system" { return "SYSTEM" }
        return "AGENT"
    }

    private var labelColor: Color {
        message.failed == true ? QuartetTheme.failed : (message.isThinking == true ? QuartetTheme.running : QuartetTheme.accent)
    }

    private var background: Color {
        if message.isShellOutput == true {
            return QuartetTheme.elevated
        }
        return message.role == "user" ? QuartetTheme.accent.opacity(0.14) : QuartetTheme.surface
    }

    private var border: Color {
        message.failed == true ? QuartetTheme.failed.opacity(0.6) : QuartetTheme.divider
    }

    private var primaryContent: String {
        if message.isThinking == true {
            return message.reasoningContent ?? message.content
        }
        return message.content
    }

    private var detailText: String? {
        if message.role == "tool" {
            return message.placeholderReason
        }
        if let toolCalls = message.toolCalls, !toolCalls.isEmpty {
            return toolCalls.map { "\($0.name)\n\($0.arguments)" }.joined(separator: "\n\n")
        }
        return nil
    }
}

private struct GraphAuthenticatedImage: View {
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
                Button {
                    appModel.present(APIError(summary: "图片加载失败", detail: error))
                } label: {
                    Label("图片加载失败，查看详情", systemImage: "photo.badge.exclamationmark")
                        .font(.caption)
                        .foregroundStyle(QuartetTheme.failed)
                }
                .buttonStyle(.plain)
            } else {
                ProgressView()
                    .frame(maxWidth: .infinity)
                    .frame(height: 80)
            }
        }
        .task(id: path) {
            do {
                let data = try await appModel.apiClient().fileData(path: path)
                guard let decoded = UIImage(data: data) else {
                    throw APIError(summary: "图片数据无效", detail: "无法将 \(path) 解码为图片。")
                }
                image = decoded
                error = nil
            } catch let apiError as APIError {
                error = apiError.detail
            } catch {
                self.error = String(describing: error)
            }
        }
    }
}

private extension GraphInstanceSummary {
    var preferredSessionID: String? {
        let candidate = displaySessionId ?? sessionId
        guard let candidate, !candidate.isEmpty else { return nil }
        return candidate
    }

    var pathSummary: String {
        let parts = key.iterations?.map { "\($0.loopNodeId)#\($0.index)" } ?? []
        return parts.joined(separator: " / ")
    }

    var displayNameWithPath: String {
        pathSummary.isEmpty ? displayName : "\(displayName) · \(pathSummary)"
    }

    var sessionEntryLabel: String {
        switch nodeType.lowercased() {
        case "shell": "查看输出"
        default: "查看会话"
        }
    }

    var sessionEntryIcon: String {
        switch nodeType.lowercased() {
        case "shell": "terminal"
        case "clarify": "text.bubble"
        default: "bubble.left.and.text.bubble.right"
        }
    }
}
