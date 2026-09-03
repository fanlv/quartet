import SwiftUI

struct GraphRunView: View {
    @EnvironmentObject private var appModel: AppModel
    @Environment(\.scenePhase) private var scenePhase
    let summary: JobSummary

    @State private var snapshot: GraphRunStatusResponse?
    @State private var loading = true
    @State private var pendingAction: GraphAction?
    @State private var confirmation: GraphAction?
    @State private var configurationDraft: GraphConfig?

    private var status: String { snapshot?.run?.status ?? summary.status }
    private var progress: GraphProgressSummary? { snapshot?.progress ?? snapshot?.run?.progress }
    private var run: GraphRunSummary? { snapshot?.run }
    private var instancesList: [GraphInstanceSummary] { snapshot?.instances ?? [] }
    private var currentInstance: GraphInstanceSummary? { GraphRunSelection.currentInstance(from: instancesList) }
    private var sessionGroups: [GraphSessionGroup] {
        GraphSessionGroup.makeGroups(
            live: instancesList,
            archived: run?.archivedInstances ?? [:]
        )
    }
    private var refreshPolicy: GraphRefreshPolicy { GraphRefreshPolicy(status: status) }
    private var workspaceName: String? {
        appModel.workspaces.first(where: { $0.id == summary.workspaceId })?.displayName
    }
    private var workspaceWorkdir: String? {
        let value = summary.workdir
            ?? appModel.workspaces.first(where: { $0.id == summary.workspaceId })?.workdir
        guard let value = value?.trimmingCharacters(in: .whitespacesAndNewlines), !value.isEmpty else {
            return nil
        }
        return value
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 20) {
                runHeader
                actions
                runConfiguration
                if loading && snapshot == nil {
                    HStack { Spacer(); ProgressView(); Spacer() }.padding(.top, 50)
                } else {
                    sessions
                }
            }
            .padding(20)
        }
        .background(QuartetTheme.canvas)
        .quartetNavigationTitle(summary.displayTitle)
        .toolbar {
            ToolbarItem(placement: .topBarTrailing) {
                HStack(spacing: 0) {
                    NavigationLink {
                        WorkspaceDirectoryBrowserView(
                            workspaceTitle: workspaceName ?? summary.workspaceId ?? "工作空间".localizedForApp,
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
                    .accessibilityIdentifier("graph-workspace-files")

                    NavigationLink {
                        JobDetailView(summary: summary)
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
        .sheet(isPresented: Binding(
            get: { configurationDraft != nil },
            set: { if !$0 { configurationDraft = nil } }
        )) {
            if let configurationDraft, let run {
                GraphRunConfigurationEditorView(
                    jobID: summary.id,
                    runID: run.id,
                    version: run.currentVersion,
                    initialConfig: configurationDraft,
                    instances: instancesList
                ) { updatedRun in
                    apply(updatedRun: updatedRun)
                    self.configurationDraft = nil
                }
                .environmentObject(appModel)
                .quartetSheetStyle()
            }
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
                        .font(.quartet(.compact, weight: .bold, design: .monospaced))
                        .foregroundStyle(QuartetTheme.secondaryText)
                    Text(statusLabel(status))
                        .font(.quartet(.regular, weight: .bold))
                        .foregroundStyle(QuartetTheme.statusColor(colorStatus(status)))
                }
                Spacer()
                if let progress {
                    Text("\(progress.completedCount)/\(progress.totalCount)")
                        .font(.quartet(.large, weight: .bold, design: .rounded))
                }
            }
            HStack(spacing: 10) {
                GraphInfoChip(title: "RUN ID", value: run?.id ?? "—")
                GraphInfoChip(title: "VERSION", value: run.map { "V\($0.currentVersion)" } ?? "—")
            }
            ProgressView(value: completionFraction)
                .tint(QuartetTheme.statusColor(colorStatus(status)))
            HStack(spacing: 14) {
                metric("RUN", progress?.runningCount ?? 0, QuartetTheme.running)
                metric("FAIL", progress?.failedCount ?? 0, QuartetTheme.failed)
                metric("SKIP", progress?.skippedCount ?? 0, QuartetTheme.stopped)
            }
            GraphRunMetaGrid(run: run, sessionCount: sessionGroups.count)
            if let error = currentInstance?.error ?? snapshot?.run?.lastError {
                Button {
                    appModel.present(APIError(summary: "Graph 节点错误", detail: error.fullDetail))
                } label: {
                    Label(error.message, systemImage: "exclamationmark.triangle.fill")
                        .font(.quartet(.compact))
                        .foregroundStyle(QuartetTheme.failed)
                        .lineLimit(3)
                }
                .buttonStyle(.plain)
            } else if let lastError = progress?.lastError, !lastError.isEmpty {
                Button { appModel.present(APIError(summary: "Graph 运行错误", detail: lastError)) } label: {
                    Label(lastError, systemImage: "exclamationmark.triangle.fill")
                        .font(.quartet(.compact))
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
    private var runConfiguration: some View {
        if GraphRunEditing.isEditable(status), let config = run?.effectiveConfig, let run {
            Button { configurationDraft = config } label: {
                HStack(spacing: 13) {
                    Image(systemName: "slider.horizontal.3")
                        .font(.quartet(.regular, weight: .semibold))
                        .foregroundStyle(QuartetTheme.accent)
                        .frame(width: 40, height: 40)
                        .background(QuartetTheme.accent.opacity(0.11), in: RoundedRectangle(cornerRadius: 12))
                    VStack(alignment: .leading, spacing: 3) {
                        Text("编辑运行配置")
                            .font(.quartet(.control, weight: .semibold))
                            .foregroundStyle(QuartetTheme.primaryText)
                        Text("当前版本 V\(run.currentVersion) · \(config.nodes.count) 个节点 · 保存后作用于尚未执行的节点")
                            .font(.quartet(.detail))
                            .foregroundStyle(QuartetTheme.secondaryText)
                            .multilineTextAlignment(.leading)
                    }
                    Spacer(minLength: 8)
                    Image(systemName: "chevron.right")
                        .font(.quartet(.detail, weight: .bold))
                        .foregroundStyle(QuartetTheme.secondaryText)
                }
                .padding(15)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18))
            .overlay(RoundedRectangle(cornerRadius: 18).stroke(QuartetTheme.divider))
            .accessibilityHint("编辑 Prompt、Loop 和其他尚未冻结的节点配置")
            .accessibilityIdentifier("graph-run-edit-configuration")
        }
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
                                .font(.quartet(.detail, weight: .semibold))
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
    private var sessions: some View {
        let groups = sessionGroups
        if groups.isEmpty {
            ContentUnavailableView {
                Label("暂无 Session", systemImage: "bubble.left.and.text.bubble.right")
                    .font(.quartet(.control, weight: .semibold))
            } description: {
                Text(GraphSessionGroup.emptyMessage(for: status))
                    .font(.quartet(.detail))
            }
        } else {
            VStack(alignment: .leading, spacing: 0) {
                HStack(alignment: .firstTextBaseline) {
                    Text("SESSIONS")
                        .font(.quartet(.compact, weight: .bold, design: .monospaced))
                        .foregroundStyle(QuartetTheme.secondaryText)
                    Spacer()
                    Text("\(groups.count) 个")
                        .font(.quartet(.compact, weight: .medium))
                        .foregroundStyle(QuartetTheme.secondaryText)
                }
                .padding(.horizontal, 16)
                .padding(.top, 16)
                .padding(.bottom, 8)

                ForEach(Array(groups.enumerated()), id: \.element.id) { index, group in
                    NavigationLink {
                        JobChatView(route: ChatRoute(summary: summary, targetSessionID: group.id))
                    } label: {
                        GraphSessionRow(number: index + 1, group: group)
                    }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("graph-session-\(group.id)")
                    if group.id != groups.last?.id {
                        Divider().overlay(QuartetTheme.divider).padding(.leading, 66)
                    }
                }
            }
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
        .font(.quartet(.compact, weight: .bold, design: .monospaced))
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
                apply(updatedRun: run)
            }
            await refresh(silent: true)
            await appModel.reloadJobs()
        } catch { appModel.present(error) }
    }

    private func apply(updatedRun: GraphRunSummary) {
        snapshot = GraphRunStatusResponse(
            run: updatedRun,
            progress: updatedRun.progress ?? snapshot?.progress,
            instances: snapshot?.instances,
            edges: snapshot?.edges,
            eventCount: snapshot?.eventCount,
            agents: snapshot?.agents
        )
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
        case "pending": "等待调度".localizedForApp
        case "running": "运行中".localizedForApp
        case "stepStopping": "当前步骤后停止中".localizedForApp
        case "stepStopped": "已在步骤后停止".localizedForApp
        case "stopped": "已停止".localizedForApp
        case "completed": "已完成".localizedForApp
        case "failed": "失败".localizedForApp
        case "timedOut": "已超时".localizedForApp
        case "recovering": "等待恢复".localizedForApp
        case "awaitingInput": "等待人工讨论".localizedForApp
        default: status
        }
    }
}

private struct GraphRunMetaGrid: View {
    let run: GraphRunSummary?
    let sessionCount: Int

    var body: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .top, spacing: 12) {
                GraphMetaTile(title: "开始时间", value: GraphFormatters.dateTime(run?.startedAt))
                GraphMetaTile(title: "持续时长", value: runDurationValue)
            }
            GraphMetaTile(title: "SESSION", value: "\(sessionCount) 个")
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

private struct GraphRunConfigurationEditorView: View {
    @EnvironmentObject private var appModel: AppModel
    @Environment(\.dismiss) private var dismiss

    let jobID: String
    let runID: String
    let version: Int
    let initialConfig: GraphConfig
    let instances: [GraphInstanceSummary]
    let onSaved: (GraphRunSummary) -> Void

    @State private var config: GraphConfig
    @State private var agents: [AgentSummary] = []
    @State private var agentPreferences: [String: AgentPreferences] = [:]
    @State private var editingNodeID: String?
    @State private var showsGlobalEditor = false
    @State private var saving = false

    init(
        jobID: String,
        runID: String,
        version: Int,
        initialConfig: GraphConfig,
        instances: [GraphInstanceSummary],
        onSaved: @escaping (GraphRunSummary) -> Void
    ) {
        self.jobID = jobID
        self.runID = runID
        self.version = version
        self.initialConfig = initialConfig
        self.instances = instances
        self.onSaved = onSaved
        _config = State(initialValue: initialConfig)
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 16) {
                    editNotice
                    globalConfiguration
                    nodeList
                }
                .padding(.horizontal, 18)
                .padding(.top, 10)
                .padding(.bottom, 18)
            }
            .background(QuartetTheme.canvas)
            .quartetNavigationTitle("编辑运行配置")
            .safeAreaInset(edge: .bottom, spacing: 0) {
                Button { Task { await save() } } label: {
                    HStack(spacing: 9) {
                        if saving { ProgressView().tint(QuartetTheme.onAccent) }
                        Text(saving ? "正在保存…" : "保存为新版本")
                        Spacer()
                        Text("V\(version + 1)")
                            .font(.quartet(.compact, weight: .bold, design: .monospaced))
                    }
                    .font(.quartet(.control, weight: .semibold))
                    .foregroundStyle(QuartetTheme.onAccent)
                    .padding(.horizontal, 18)
                    .frame(minHeight: 52)
                    .background(QuartetTheme.accent, in: RoundedRectangle(cornerRadius: 14))
                }
                .buttonStyle(.plain)
                .disabled(saving || config == initialConfig)
                .opacity(saving || config == initialConfig ? 0.45 : 1)
                .accessibilityIdentifier("graph-run-save-version")
                .padding(.horizontal, 18)
                .padding(.vertical, 10)
                .background(.ultraThinMaterial)
            }
            .sheet(isPresented: Binding(
                get: { editingNodeID != nil },
                set: { if !$0 { editingNodeID = nil } }
            )) {
                if let nodeID = editingNodeID, let nodeBinding = binding(forNodeID: nodeID),
                   let node = config.nodes.first(where: { $0.id == nodeID }) {
                    GraphNodeConfigurationView(
                        node: nodeBinding,
                        agents: agents,
                        agentPreferences: agentPreferences,
                        editingRestriction: restriction(for: node)
                    )
                    .quartetSheetStyle()
                }
            }
            .sheet(isPresented: $showsGlobalEditor) {
                GraphGlobalConfigurationView(
                    config: $config,
                    locksExecutionLimits: true,
                    workspaceRoot: workspaceRoot
                )
                    .quartetSheetStyle()
            }
        }
        .task { await loadAgentConfiguration() }
    }

    private var workspaceRoot: String? {
        if let workspaceID = config.workspaceId,
           let workspace = appModel.workspaces.first(where: { $0.id == workspaceID }),
           !workspace.workdir.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
            return workspace.workdir
        }
        return config.workdir
    }

    private var globalConfiguration: some View {
        Button { showsGlobalEditor = true } label: {
            HStack(spacing: 12) {
                Image(systemName: "curlybraces")
                    .font(.quartet(.regular, weight: .semibold))
                    .foregroundStyle(QuartetTheme.accent)
                    .frame(width: 38, height: 38)
                    .background(QuartetTheme.accent.opacity(0.1), in: RoundedRectangle(cornerRadius: 11))
                VStack(alignment: .leading, spacing: 3) {
                    Text("全局变量")
                        .font(.quartet(.control, weight: .semibold))
                        .foregroundStyle(QuartetTheme.primaryText)
                    Text("\(config.variables?.count ?? 0) 个变量 · 执行限制已锁定")
                        .font(.quartet(.detail))
                        .foregroundStyle(QuartetTheme.secondaryText)
                }
                Spacer()
                Image(systemName: "chevron.right")
                    .font(.quartet(.detail, weight: .bold))
                    .foregroundStyle(QuartetTheme.secondaryText)
            }
            .padding(15)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18))
        .overlay(RoundedRectangle(cornerRadius: 18).stroke(QuartetTheme.divider))
        .accessibilityIdentifier("graph-run-global-configuration")
    }

    private var editNotice: some View {
        VStack(alignment: .leading, spacing: 9) {
            Label("RUN \(runID) · V\(version)", systemImage: "clock.arrow.circlepath")
                .font(.quartet(.compact, weight: .bold, design: .monospaced))
                .foregroundStyle(QuartetTheme.accentDeep)
            Text("保存会创建新的运行版本。尚未执行的节点会使用新配置；Loop 内节点从下一轮开始使用新配置。已经完成或正在执行的节点会锁定，Loop 容器只允许调整固定次数。")
                .font(.quartet(.detail))
                .foregroundStyle(QuartetTheme.secondaryText)
                .fixedSize(horizontal: false, vertical: true)
        }
        .padding(16)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(QuartetTheme.accent.opacity(0.08), in: RoundedRectangle(cornerRadius: 18))
        .overlay(RoundedRectangle(cornerRadius: 18).stroke(QuartetTheme.accent.opacity(0.2)))
    }

    private var nodeList: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(alignment: .firstTextBaseline) {
                Text("节点配置")
                    .font(.quartet(.control, weight: .semibold))
                Spacer()
                Text("\(config.nodes.count) 个节点")
                    .font(.quartet(.compact))
                    .foregroundStyle(QuartetTheme.secondaryText)
            }
            .padding(.horizontal, 16)
            .padding(.vertical, 14)

            ForEach(Array(config.nodes.enumerated()), id: \.element.id) { index, node in
                Button { editingNodeID = node.id } label: {
                    HStack(spacing: 12) {
                        GraphNodeBadge(type: node.type)
                        VStack(alignment: .leading, spacing: 4) {
                            HStack(spacing: 7) {
                                Text(node.displayName)
                                    .font(.quartet(.control, weight: .semibold))
                                    .foregroundStyle(QuartetTheme.primaryText)
                                    .lineLimit(1)
                                restrictionBadge(for: node)
                            }
                            Text(nodeSummary(node))
                                .font(.quartet(.detail))
                                .foregroundStyle(QuartetTheme.secondaryText)
                                .lineLimit(2)
                                .multilineTextAlignment(.leading)
                        }
                        Spacer(minLength: 8)
                        Image(systemName: "chevron.right")
                            .font(.quartet(.detail, weight: .bold))
                            .foregroundStyle(QuartetTheme.secondaryText)
                    }
                    .padding(.horizontal, 14)
                    .frame(minHeight: 72)
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("graph-run-node-\(node.id)")

                if index < config.nodes.count - 1 {
                    Divider().overlay(QuartetTheme.divider).padding(.leading, 62)
                }
            }
        }
        .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 20))
        .overlay(RoundedRectangle(cornerRadius: 20).stroke(QuartetTheme.divider))
    }

    @ViewBuilder
    private func restrictionBadge(for node: GraphNode) -> some View {
        switch restriction(for: node) {
        case .none:
            EmptyView()
        case .frozen:
            Label("已冻结", systemImage: "lock.fill")
                .font(.quartet(.tiny, weight: .semibold))
                .foregroundStyle(QuartetTheme.secondaryText)
        case .loopFixedCountOnly:
            Text("仅次数")
                .font(.quartet(.tiny, weight: .semibold))
                .foregroundStyle(QuartetTheme.running)
        }
    }

    private func binding(forNodeID id: String) -> Binding<GraphNode>? {
        guard config.nodes.contains(where: { $0.id == id }) else { return nil }
        return Binding(
            get: {
                config.nodes.first(where: { $0.id == id })
                    ?? GraphNode(id: id, type: "unknown", title: nil, parentId: nil, config: nil, layout: nil, metadata: nil)
            },
            set: { node in
                guard let index = config.nodes.firstIndex(where: { $0.id == id }) else { return }
                config.nodes[index] = node
            }
        )
    }

    private func restriction(for node: GraphNode) -> GraphNodeEditingRestriction {
        let frozen = instances.contains { instance in
            instance.nodeId == node.id && ["succeeded", "skipped", "running"].contains(instance.status)
        }
        guard frozen, !nodeIsInsideLoop(node) else { return .none }
        if node.type == "loop", node.config?.loopMode != "until" {
            return .loopFixedCountOnly
        }
        return .frozen
    }

    private func nodeIsInsideLoop(_ node: GraphNode) -> Bool {
        var parentID = node.parentId
        var visited = Set<String>()
        while let id = parentID, !id.isEmpty, visited.insert(id).inserted {
            guard let parent = config.nodes.first(where: { $0.id == id }) else { return false }
            if parent.type == "loop" { return true }
            parentID = parent.parentId
        }
        return false
    }

    private func nodeSummary(_ node: GraphNode) -> String {
        let value = node.config ?? GraphNodeConfiguration()
        switch node.type {
        case "shell": return concise(value.script, fallback: "未配置 Shell 脚本")
        case "prompt": return concise(value.prompt, fallback: "未配置 Prompt")
        case "clarify": return concise(value.prompt, fallback: "等待用户讨论")
        case "ifElse": return concise(value.condition, fallback: "未配置条件")
        case "loop":
            return value.loopMode == "until"
                ? "条件循环 · 最多 \(value.maxIterations ?? 0) 次"
                : "固定循环 · \(value.fixedCount ?? 0) 次"
        case "start": return "工作流入口"
        case "end": return "工作流出口"
        default: return node.type
        }
    }

    private func concise(_ text: String?, fallback: String) -> String {
        let trimmed = text?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return trimmed.isEmpty ? fallback : trimmed.replacingOccurrences(of: "\n", with: " ")
    }

    private func loadAgentConfiguration() async {
        do {
            async let loadedAgents = appModel.agentCatalog()
            async let loadedPreferences = appModel.agentPreferences()
            agents = try await loadedAgents
            agentPreferences = try await loadedPreferences
        } catch is CancellationError {
            return
        } catch {
            appModel.present(error)
        }
    }

    private func save() async {
        guard !saving, config != initialConfig else { return }
        saving = true
        defer { saving = false }
        do {
            let validation = try await appModel.apiClient().validateGraphWorkflow(config: config)
            guard validation.valid else {
                let detail = (validation.errors ?? []).enumerated().map { index, error in
                    let location = error.location.map { " [\($0)]" } ?? ""
                    return "\(index + 1). [\(error.type)]\(location) \(error.message)"
                }.joined(separator: "\n")
                throw APIError(
                    summary: "工作流配置校验失败",
                    detail: "POST /api/v1/graph/workflow/validate\nHTTP 200\n\n\(detail.isEmpty ? "服务端返回 valid=false，但没有错误详情。" : detail)",
                    requestWasRejected: true
                )
            }
            let response = try await appModel.updateGraphRunVersion(jobID: jobID, config: config)
            guard let updatedRun = response.run else {
                throw APIError(
                    summary: "运行配置保存失败",
                    detail: "PUT /api/v1/job/\(jobID)/graph-run/version\nHTTP 200\n\n响应中缺少 run。"
                )
            }
            onSaved(updatedRun)
            dismiss()
        } catch {
            appModel.present(error)
        }
    }
}

private struct GraphSessionGroup: Identifiable {
    let id: String
    let entries: [GraphInstanceSummary]

    var status: String {
        if entries.contains(where: { ["running", "pending"].contains($0.status) }) { return "running" }
        if entries.contains(where: { $0.status == "failed" }) { return "failed" }
        if entries.contains(where: { $0.status == "interrupted" }) { return "interrupted" }
        return "completed"
    }

    var statusLabel: String {
        switch status {
        case "running": return "运行中".localizedForApp
        case "failed": return "失败".localizedForApp
        case "interrupted": return "已中断".localizedForApp
        default: return "已完成".localizedForApp
        }
    }

    var title: String {
        let names = entries.map(\.displayName).reduce(into: [String]()) { result, name in
            if !result.contains(name) { result.append(name) }
        }
        return names.prefix(2).joined(separator: " · ")
    }

    var totalDurationMs: Int64 { entries.reduce(0) { $0 + ($1.durationMs ?? 0) } }
    var firstStartedAt: Int64? { entries.compactMap(\.startedAt).min() }

    static func makeGroups(
        live: [GraphInstanceSummary],
        archived: [String: GraphInstanceSummary]
    ) -> [GraphSessionGroup] {
        let liveKeys = Set(live.map { $0.key.backendKey })
        let restored = archived.compactMap { key, instance in liveKeys.contains(key) ? nil : instance }
        let ordered = (live + restored)
            .filter { ["prompt", "clarify", "shell"].contains($0.nodeType.lowercased()) && $0.preferredSessionID != nil }
            .sorted { lhs, rhs in
                let left = lhs.startedAt ?? Int64.max
                let right = rhs.startedAt ?? Int64.max
                return left == right ? lhs.id < rhs.id : left < right
            }

        var order: [String] = []
        var grouped: [String: [GraphInstanceSummary]] = [:]
        for instance in ordered {
            guard let sessionID = instance.preferredSessionID else { continue }
            if grouped[sessionID] == nil { order.append(sessionID) }
            grouped[sessionID, default: []].append(instance)
        }
        return order.compactMap { id in grouped[id].map { GraphSessionGroup(id: id, entries: $0) } }
    }

    static func emptyMessage(for status: String) -> String {
        switch status {
        case "running", "pending": return "工作流正在等待第一个可查看的 Session。"
        case "failed": return "工作流在创建 Session 前失败。"
        case "stopped", "stepStopped": return "工作流在创建 Session 前停止。"
        default: return "本次运行没有记录可查看的 Session。"
        }
    }
}

private struct GraphSessionRow: View {
    let number: Int
    let group: GraphSessionGroup

    var body: some View {
        HStack(spacing: 13) {
            Image(systemName: icon)
                .font(.quartet(.control, weight: .bold))
                .foregroundStyle(statusColor)
                .frame(width: 38, height: 38)
                .background(statusColor.opacity(0.1), in: RoundedRectangle(cornerRadius: 12))
            VStack(alignment: .leading, spacing: 4) {
                HStack(spacing: 7) {
                    Text("Session #\(number)")
                        .font(.quartet(.control, weight: .semibold))
                        .foregroundStyle(QuartetTheme.primaryText)
                    Text("\(group.entries.count) \(group.entries.count == 1 ? "Job" : "Jobs")")
                        .font(.quartet(.tiny, weight: .semibold))
                        .foregroundStyle(QuartetTheme.secondaryText)
                }
                Text(group.title)
                    .font(.quartet(.detail))
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .lineLimit(1)
                HStack(spacing: 8) {
                    Text(group.statusLabel)
                    if let startedAt = group.firstStartedAt {
                        Text(GraphFormatters.dateTime(startedAt))
                    }
                    if group.totalDurationMs > 0 {
                        Text(GraphFormatters.formattedDuration(milliseconds: group.totalDurationMs))
                    }
                }
                .font(.quartet(.compact, weight: .medium, design: .monospaced))
                .foregroundStyle(statusColor)
            }
            Spacer(minLength: 8)
            Image(systemName: "chevron.right")
                .font(.quartet(.detail, weight: .bold))
                .foregroundStyle(QuartetTheme.secondaryText)
        }
        .padding(.horizontal, 14)
        .padding(.vertical, 12)
        .contentShape(Rectangle())
    }

    private var icon: String {
        switch group.status {
        case "running": "hourglass"
        case "failed": "xmark"
        case "interrupted": "pause.fill"
        default: "checkmark"
        }
    }

    private var statusColor: Color {
        switch group.status {
        case "running": QuartetTheme.running
        case "failed": QuartetTheme.failed
        case "interrupted": QuartetTheme.stopped
        default: QuartetTheme.success
        }
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
        case .stepStop: "步骤后停止".localizedForApp
        case .cancelStop: "继续运行".localizedForApp
        case .stop: "立即停止".localizedForApp
        case .resume: "恢复运行".localizedForApp
        case .completeDiscussion: "讨论完成".localizedForApp
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

private enum GraphRunEditing {
    static func isEditable(_ status: String) -> Bool {
        [
            "running", "stepStopping", "recovering", "stepStopped",
            "stopped", "failed", "timedOut", "awaitingInput"
        ].contains(status)
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
        case "pending": "等待中".localizedForApp
        case "running": "运行中".localizedForApp
        case "succeeded": "已完成".localizedForApp
        case "failed": "失败".localizedForApp
        case "skipped": "已跳过".localizedForApp
        case "interrupted": "已中断".localizedForApp
        case "awaitingInput": "等待讨论".localizedForApp
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
            Text(title.localizedForApp)
                .font(.quartet(.compact, weight: .bold, design: .monospaced))
                .foregroundStyle(QuartetTheme.secondaryText)
            Text(value)
                .font(.quartet(.detail, weight: .semibold))
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
            Text(title.localizedForApp)
                .font(.quartet(.tiny, weight: .bold, design: .monospaced))
                .foregroundStyle(QuartetTheme.secondaryText)
            Text(value)
                .font(.quartet(.compact, weight: .semibold))
                .foregroundStyle(QuartetTheme.primaryText)
                .lineLimit(1)
                .textSelection(.enabled)
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 10)
        .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 14))
    }
}

extension GraphInstanceSummary {
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
}

extension GraphInstanceKeySummary {
    var backendKey: String {
        let scope = (iterations ?? []).map { "\($0.loopNodeId)#\($0.index)" }
        return (scope + [nodeId]).joined(separator: "/")
    }
}
