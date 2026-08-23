import SwiftUI

struct GraphWorkflowLaunchView: View {
    @EnvironmentObject private var appModel: AppModel
    let onCreated: (ChatRoute) -> Void

    @State private var workflows: [GraphWorkflowSummary] = []
    @State private var warnings: [GraphWorkflowWarning] = []
    @State private var selectedWorkflowID = ""
    @State private var workflow: GraphWorkflow?
    @State private var config: GraphConfig?
    @State private var workspaceID = ""
    @State private var agents: [AgentSummary] = []
    @State private var loading = true
    @State private var loadingWorkflow = false
    @State private var starting = false
    @State private var showsGlobalEditor = false
    @State private var editingNodeID: String?
    @State private var localError: PresentedError?

    private var selectedSummary: GraphWorkflowSummary? {
        workflows.first { $0.id == selectedWorkflowID }
    }

    private var selectedWorkspace: WorkspaceSummary? {
        appModel.workspaces.first { $0.id == workspaceID }
    }

    private var effectiveWorkdir: String? {
        guard let selectedWorkspace else { return nil }
        if config?.workspaceId == selectedWorkspace.id,
           let configured = config?.workdir?.trimmingCharacters(in: .whitespacesAndNewlines),
           !configured.isEmpty {
            return configured
        }
        return selectedWorkspace.workdir
    }

    private var cannotStart: Bool {
        starting || loadingWorkflow || workflow?.id != selectedWorkflowID || config == nil || selectedWorkspace == nil
    }

    var body: some View {
        ScrollView {
            VStack(alignment: .leading, spacing: 20) {
                if loading {
                    loadingState
                } else if workflows.isEmpty {
                    emptyState
                } else {
                    workflowSelector
                    if loadingWorkflow {
                        HStack(spacing: 10) {
                            ProgressView()
                            Text("正在读取工作流配置…")
                        }
                        .font(.quartet(.control))
                        .foregroundStyle(QuartetTheme.secondaryText)
                        .frame(maxWidth: .infinity, alignment: .center)
                        .padding(.vertical, 32)
                    } else if let workflow, let config {
                        overview(workflow: workflow, config: config)
                        workspaceSection(config: config)
                        globalSection(config: config)
                        nodeSection(config: config)
                    }
                }

                warningSection
            }
            .padding(.horizontal, 18)
            .padding(.top, 12)
            .padding(.bottom, 110)
        }
        .scrollDismissesKeyboard(.interactively)
        .safeAreaInset(edge: .bottom, spacing: 0) {
            if !loading, !workflows.isEmpty { actionBar }
        }
        .task { await load() }
        .onChange(of: selectedWorkflowID) { _, id in
            guard !id.isEmpty, workflow?.id != id else { return }
            workflow = nil
            config = nil
            Task { await loadWorkflow(id: id) }
        }
        .sheet(isPresented: $showsGlobalEditor) {
            if let binding = configBinding {
                GraphGlobalConfigurationView(config: binding)
                    .quartetSheetStyle()
            }
        }
        .sheet(isPresented: Binding(
            get: { editingNodeID != nil },
            set: { if !$0 { editingNodeID = nil } }
        )) {
            if let nodeID = editingNodeID, let nodeBinding = binding(forNodeID: nodeID) {
                GraphNodeConfigurationView(node: nodeBinding, agents: agents)
                    .quartetSheetStyle()
            }
        }
        .sheet(item: $localError) {
            ErrorDetailView(error: $0)
        }
    }

    private var loadingState: some View {
        VStack(spacing: 14) {
            ProgressView().controlSize(.large).tint(QuartetTheme.accent)
            Text("正在加载工作流")
                .font(.quartet(.regular, weight: .semibold))
            Text("正在读取工作流库与 Agent 配置…")
                .font(.quartet(.control))
                .foregroundStyle(QuartetTheme.secondaryText)
        }
        .frame(maxWidth: .infinity)
        .padding(.top, 52)
    }

    private var emptyState: some View {
        ContentUnavailableView {
            Label("暂无工作流", systemImage: "point.3.connected.trianglepath.dotted")
        } description: {
            Text("请先在 Web 端的 Graph Workflows 页面创建并保存一个工作流。")
        } actions: {
            Button("重新加载") { Task { await load() } }
        }
        .frame(maxWidth: .infinity)
        .padding(.top, 36)
    }

    private var workflowSelector: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("工作流模板")
                .font(.quartet(.regular, weight: .semibold))
                .foregroundStyle(QuartetTheme.primaryText)

            Menu {
                ForEach(workflows) { item in
                    Button { selectedWorkflowID = item.id } label: {
                        if item.id == selectedWorkflowID {
                            Label(item.name, systemImage: "checkmark")
                        } else {
                            Text(item.name)
                        }
                    }
                }
            } label: {
                HStack(spacing: 12) {
                    configurationIcon("point.3.connected.trianglepath.dotted")
                    VStack(alignment: .leading, spacing: 3) {
                        Text(selectedSummary?.name ?? "选择工作流")
                            .font(.quartet(.control, weight: .semibold))
                            .foregroundStyle(QuartetTheme.primaryText)
                        if let summary = selectedSummary {
                            Text("\(summary.nodeCount) 个节点 · \(summary.edgeCount) 条连线")
                                .font(.quartet(.detail))
                                .foregroundStyle(QuartetTheme.secondaryText)
                        }
                    }
                    Spacer()
                    Image(systemName: "chevron.up.chevron.down")
                        .font(.quartet(.compact, weight: .bold))
                        .foregroundStyle(QuartetTheme.secondaryText)
                }
                .padding(14)
                .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
                .overlay(RoundedRectangle(cornerRadius: 18, style: .continuous).stroke(QuartetTheme.divider))
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("graph-workflow-picker")
        }
    }

    private func overview(workflow: GraphWorkflow, config: GraphConfig) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(alignment: .firstTextBaseline) {
                Text(workflow.name)
                    .font(.quartet(.large, weight: .bold))
                    .foregroundStyle(QuartetTheme.primaryText)
                Spacer()
                Text((workflow.type ?? "user").uppercased())
                    .font(.quartet(.compact, weight: .bold, design: .monospaced))
                    .foregroundStyle(QuartetTheme.accent)
            }
            if let description = workflow.description, !description.isEmpty {
                Text(description)
                    .font(.quartet(.control))
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .fixedSize(horizontal: false, vertical: true)
            }
            HStack(spacing: 8) {
                metric("NODE", config.nodes.count)
                metric("EDGE", config.edges.count)
                metric("VAR", config.variables?.count ?? 0)
            }
        }
        .padding(16)
        .background(
            LinearGradient(
                colors: [QuartetTheme.accent.opacity(0.16), QuartetTheme.surface],
                startPoint: .topLeading,
                endPoint: .bottomTrailing
            ),
            in: RoundedRectangle(cornerRadius: 20, style: .continuous)
        )
        .overlay(RoundedRectangle(cornerRadius: 20, style: .continuous).stroke(QuartetTheme.accent.opacity(0.25)))
    }

    private func workspaceSection(config: GraphConfig) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .firstTextBaseline) {
                Text("运行空间")
                    .font(.quartet(.regular, weight: .semibold))
                    .foregroundStyle(QuartetTheme.primaryText)
                Spacer()
                Text("本次运行")
                    .font(.quartet(.detail))
                    .foregroundStyle(QuartetTheme.secondaryText)
            }

            ScrollView(.horizontal, showsIndicators: false) {
                HStack(spacing: 10) {
                    ForEach(appModel.workspaces) { item in
                        let selected = item.id == workspaceID
                        Button { selectWorkspace(item) } label: {
                            HStack(spacing: 9) {
                                Circle().fill(workspaceTint(item)).frame(width: 10, height: 10)
                                Text(item.displayName).font(.quartet(.control, weight: .semibold)).lineLimit(1)
                                if selected { Image(systemName: "checkmark").font(.quartet(.detail, weight: .bold)) }
                            }
                            .foregroundStyle(selected ? QuartetTheme.primaryText : QuartetTheme.secondaryText)
                            .padding(.horizontal, 14)
                            .frame(height: 44)
                            .background(selected ? QuartetTheme.accent.opacity(0.14) : QuartetTheme.surface, in: Capsule())
                            .overlay(Capsule().stroke(selected ? QuartetTheme.accent.opacity(0.7) : QuartetTheme.divider))
                        }
                        .buttonStyle(.plain)
                    }
                }
            }

            if let effectiveWorkdir {
                Label(effectiveWorkdir, systemImage: "folder")
                    .font(.quartet(.detail))
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .textSelection(.enabled)
            } else if !workspaceID.isEmpty {
                Label("工作空间 \(workspaceID) 不存在，请选择可用空间。", systemImage: "exclamationmark.triangle.fill")
                    .font(.quartet(.detail))
                    .foregroundStyle(QuartetTheme.failed)
            }
        }
    }

    private func globalSection(config: GraphConfig) -> some View {
        Button { showsGlobalEditor = true } label: {
            HStack(spacing: 14) {
                configurationIcon("slider.horizontal.3")
                VStack(alignment: .leading, spacing: 3) {
                    Text("全局配置")
                        .font(.quartet(.regular, weight: .semibold))
                        .foregroundStyle(QuartetTheme.primaryText)
                    Text(globalConfigurationSummary(config))
                        .font(.quartet(.detail))
                        .foregroundStyle(QuartetTheme.secondaryText)
                        .lineLimit(2)
                }
                Spacer()
                Image(systemName: "chevron.right")
                    .font(.quartet(.detail, weight: .bold))
                    .foregroundStyle(QuartetTheme.secondaryText)
            }
            .padding(16)
            .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: 18, style: .continuous).stroke(QuartetTheme.divider))
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier("graph-global-config")
    }

    private func nodeSection(config: GraphConfig) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .firstTextBaseline) {
                Text("节点配置")
                    .font(.quartet(.regular, weight: .semibold))
                    .foregroundStyle(QuartetTheme.primaryText)
                Spacer()
                Text("\(config.nodes.count) 个节点")
                    .font(.quartet(.detail))
                    .foregroundStyle(QuartetTheme.secondaryText)
            }

            VStack(spacing: 0) {
                ForEach(Array(config.nodes.enumerated()), id: \.element.id) { index, node in
                    Button { editingNodeID = node.id } label: {
                        HStack(spacing: 12) {
                            GraphNodeBadge(type: node.type)
                            VStack(alignment: .leading, spacing: 3) {
                                Text(node.displayName)
                                    .font(.quartet(.control, weight: .semibold))
                                    .foregroundStyle(QuartetTheme.primaryText)
                                Text(nodeSummary(node))
                                    .font(.quartet(.detail))
                                    .foregroundStyle(QuartetTheme.secondaryText)
                                    .lineLimit(2)
                            }
                            Spacer()
                            Image(systemName: "chevron.right")
                                .font(.quartet(.detail, weight: .bold))
                                .foregroundStyle(QuartetTheme.secondaryText)
                        }
                        .padding(.horizontal, 14)
                        .frame(minHeight: 68)
                        .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("graph-node-\(node.id)")

                    if index < config.nodes.count - 1 {
                        Divider().overlay(QuartetTheme.divider).padding(.leading, 62)
                    }
                }
            }
            .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 20, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: 20, style: .continuous).stroke(QuartetTheme.divider))
        }
    }

    @ViewBuilder
    private var warningSection: some View {
        if !warnings.isEmpty {
            VStack(alignment: .leading, spacing: 8) {
                Label("有 \(warnings.count) 个工作流文件加载失败", systemImage: "exclamationmark.triangle.fill")
                    .font(.quartet(.control, weight: .semibold))
                    .foregroundStyle(QuartetTheme.failed)
                ForEach(Array(warnings.enumerated()), id: \.offset) { _, warning in
                    Text("\(warning.file)\n\(warning.error)")
                        .font(.quartet(.detail, design: .monospaced))
                        .foregroundStyle(QuartetTheme.secondaryText)
                        .textSelection(.enabled)
                }
            }
            .padding(14)
            .background(QuartetTheme.failed.opacity(0.08), in: RoundedRectangle(cornerRadius: 14))
        }
    }

    private var actionBar: some View {
        VStack(spacing: 10) {
            HStack(spacing: 8) {
                contextPill(selectedSummary?.name ?? "未选择工作流", icon: "point.3.connected.trianglepath.dotted")
                contextPill(selectedWorkspace?.displayName ?? "未选择空间", icon: "square.stack.3d.up")
            }

            Button { Task { await start() } } label: {
                HStack(spacing: 10) {
                    if starting { ProgressView().tint(QuartetTheme.onAccent) }
                    else { Image(systemName: "play.fill") }
                    Text(starting ? "正在启动…" : "运行 Workflow")
                    Spacer()
                    Image(systemName: "chevron.right").font(.quartet(.detail, weight: .bold))
                }
                .font(.quartet(.regular, weight: .semibold))
                .foregroundStyle(QuartetTheme.onAccent)
                .padding(.horizontal, 18)
                .frame(height: 54)
                .background(QuartetTheme.accent, in: RoundedRectangle(cornerRadius: 16, style: .continuous))
            }
            .disabled(cannotStart)
            .opacity(cannotStart ? 0.45 : 1)
            .accessibilityIdentifier("graph-workflow-run")
        }
        .padding(.horizontal, 18)
        .padding(.top, 10)
        .padding(.bottom, 8)
        .background(.ultraThinMaterial)
        .overlay(alignment: .top) { Rectangle().fill(QuartetTheme.divider).frame(height: 0.5) }
    }

    private var configBinding: Binding<GraphConfig>? {
        guard config != nil else { return nil }
        return Binding(
            get: { config ?? GraphConfig(nodes: [], edges: []) },
            set: { config = $0 }
        )
    }

    private func binding(forNodeID id: String) -> Binding<GraphNode>? {
        guard config?.nodes.contains(where: { $0.id == id }) == true else { return nil }
        return Binding(
            get: {
                config?.nodes.first(where: { $0.id == id })
                    ?? GraphNode(id: id, type: "unknown", title: nil, parentId: nil, config: nil, layout: nil, metadata: nil)
            },
            set: { node in
                guard var next = config, let index = next.nodes.firstIndex(where: { $0.id == id }) else { return }
                next.nodes[index] = node
                config = next
            }
        )
    }

    private func load() async {
        loading = true
        defer { loading = false }
        do {
            let client = try appModel.apiClient()
            async let workflowResponse = client.graphWorkflows()
            async let agentResponse = appModel.agentCatalog()
            let (loadedWorkflows, loadedAgents) = try await (workflowResponse, agentResponse)
            workflows = loadedWorkflows.workflows.sorted { lhs, rhs in
                lhs.name.compare(
                    rhs.name,
                    options: [.caseInsensitive, .diacriticInsensitive, .numeric],
                    locale: .current
                ) == .orderedAscending
            }
            warnings = loadedWorkflows.warnings ?? []
            agents = loadedAgents
            guard let targetID = selectedWorkflowID.isEmpty || !workflows.contains(where: { $0.id == selectedWorkflowID })
                ? preferredWorkflowID(in: workflows)
                : selectedWorkflowID else {
                selectedWorkflowID = ""
                workflow = nil
                config = nil
                return
            }
            if selectedWorkflowID == targetID {
                if workflow?.id != targetID { await loadWorkflow(id: targetID) }
            } else {
                selectedWorkflowID = targetID
            }
        } catch is CancellationError {
            return
        } catch {
            present(error)
        }
    }

    private func loadWorkflow(id: String) async {
        loadingWorkflow = true
        defer {
            if selectedWorkflowID == id { loadingWorkflow = false }
        }
        do {
            let loaded = try await appModel.apiClient().graphWorkflow(id: id)
            guard selectedWorkflowID == id else { return }
            var snapshot = loaded.config
            if snapshot.workspaceId?.isEmpty != false { snapshot.workspaceId = loaded.workspaceId }
            var variables = snapshot.variables ?? [:]
            variables["Code"] = variables["Code"] ?? ""
            variables["Doc"] = variables["Doc"] ?? ""
            snapshot.variables = variables
            workflow = loaded
            config = snapshot
            workspaceID = preferredWorkspaceID(for: loaded, config: snapshot)
            editingNodeID = nil
            showsGlobalEditor = false
        } catch is CancellationError {
            return
        } catch {
            guard selectedWorkflowID == id else { return }
            workflow = nil
            config = nil
            present(error)
        }
    }

    private func start() async {
        guard !starting, let workflow, var executionConfig = config, let selectedWorkspace, let effectiveWorkdir else { return }
        starting = true
        defer { starting = false }

        executionConfig.workspaceId = selectedWorkspace.id
        executionConfig.workdir = effectiveWorkdir
        do {
            let validation = try await appModel.apiClient().validateGraphWorkflow(config: executionConfig)
            guard validation.valid else {
                throw validationError(validation.errors ?? [])
            }
            let run = try await appModel.apiClient().startGraphRun(StartGraphRunRequest(
                workflowId: workflow.id,
                workflowUpdatedAt: workflow.updatedAt,
                workspaceId: selectedWorkspace.id,
                workdir: effectiveWorkdir,
                config: executionConfig
            ))
            await appModel.reloadJobs()
            let now = Int64(Date().timeIntervalSince1970 * 1_000)
            onCreated(ChatRoute(summary: JobSummary(
                id: run.jobId,
                title: workflow.name,
                modelId: nil,
                status: run.status,
                mode: "graph",
                workspaceId: run.workspaceId ?? selectedWorkspace.id,
                workdir: effectiveWorkdir,
                createdAt: now,
                updatedAt: now,
                pinnedAt: nil,
                sessionCount: 0,
                scheduleId: nil,
                shareToken: nil
            )))
        } catch {
            present(error)
        }
    }

    private func preferredWorkflowID(in workflows: [GraphWorkflowSummary]) -> String? {
        if let currentWorkspaceID = appModel.selectedWorkspaceID,
           let match = workflows.first(where: { $0.workspaceId == currentWorkspaceID }) {
            return match.id
        }
        return workflows.first?.id
    }

    private func preferredWorkspaceID(for workflow: GraphWorkflow, config: GraphConfig) -> String {
        let candidates = [config.workspaceId, workflow.workspaceId, appModel.selectedWorkspaceID]
        for candidate in candidates {
            if let candidate, appModel.workspaces.contains(where: { $0.id == candidate }) {
                return candidate
            }
        }
        return appModel.workspaces.first?.id ?? ""
    }

    private func selectWorkspace(_ workspace: WorkspaceSummary) {
        workspaceID = workspace.id
        guard var next = config else { return }
        next.workspaceId = workspace.id
        next.workdir = workspace.workdir
        config = next
    }

    private func validationError(_ errors: [GraphValidationError]) -> APIError {
        let body = errors.enumerated().map { index, error in
            let location = error.location.map { " [\($0)]" } ?? ""
            return "\(index + 1). [\(error.type)]\(location) \(error.message)"
        }.joined(separator: "\n")
        return APIError(
            summary: "工作流配置校验失败",
            detail: "POST /api/v1/graph/workflow/validate\nHTTP 200\n\n\(body.isEmpty ? "服务端返回 valid=false，但没有错误详情。" : body)",
            requestWasRejected: true
        )
    }

    private func present(_ error: Error) {
        if let error = error as? APIError {
            localError = PresentedError(title: error.summary, detail: error.detail)
        } else {
            localError = PresentedError(title: "操作失败", detail: String(describing: error))
        }
    }

    private func globalConfigurationSummary(_ config: GraphConfig) -> String {
        let runConfig = config.runConfig ?? GraphRunConfiguration()
        let concurrency = runConfig.concurrencyLimit.map(String.init) ?? "默认"
        return "并发 \(concurrency) · \(config.variables?.count ?? 0) 个变量 · 点击编辑全部运行限制"
    }

    private func nodeSummary(_ node: GraphNode) -> String {
        let cfg = node.config ?? GraphNodeConfiguration()
        switch node.type {
        case "shell":
            return shortText(cfg.script, fallback: "未配置 Shell 脚本")
        case "prompt":
            return [agentDisplayName(cfg.agentType), shortText(cfg.prompt, fallback: "未配置 Prompt")].compactMap { $0 }.joined(separator: " · ")
        case "clarify":
            return [agentDisplayName(cfg.agentType), shortText(cfg.prompt, fallback: "等待用户讨论")].compactMap { $0 }.joined(separator: " · ")
        case "ifElse":
            return shortText(cfg.condition, fallback: "未配置条件")
        case "loop":
            return cfg.loopMode == "until" ? "条件循环 · 最多 \(cfg.maxIterations ?? 0) 次" : "固定循环 · \(cfg.fixedCount ?? 0) 次"
        case "start": return "工作流入口"
        case "end": return "工作流出口 · \(endHookLabel(cfg.endHookMode))"
        default: return node.type
        }
    }

    private func agentDisplayName(_ reference: String?) -> String? {
        guard let reference, !reference.isEmpty else { return nil }
        let match = agents.first { $0.type == reference || $0.agentId == reference }
        return match.map { $0.displayName.isEmpty ? $0.type : $0.displayName } ?? reference
    }

    private func shortText(_ text: String?, fallback: String) -> String {
        let value = text?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return value.isEmpty ? fallback : value.replacingOccurrences(of: "\n", with: " ")
    }

    private func endHookLabel(_ mode: String?) -> String {
        switch mode {
        case "custom": "自定义结束脚本"
        case "off": "结束脚本关闭"
        default: "使用全局结束脚本"
        }
    }

    private func metric(_ label: String, _ value: Int) -> some View {
        Text("\(label) \(value)")
            .font(.quartet(.compact, weight: .bold, design: .monospaced))
            .foregroundStyle(QuartetTheme.secondaryText)
            .padding(.horizontal, 9)
            .frame(height: 26)
            .background(QuartetTheme.elevated, in: Capsule())
    }

    private func contextPill(_ value: String, icon: String) -> some View {
        Label(value, systemImage: icon)
            .font(.quartet(.detail, weight: .medium))
            .foregroundStyle(QuartetTheme.secondaryText)
            .lineLimit(1)
            .padding(.horizontal, 9)
            .frame(height: 26)
            .background(QuartetTheme.elevated, in: Capsule())
    }

    private func configurationIcon(_ name: String) -> some View {
        Image(systemName: name)
            .font(.quartet(.control, weight: .semibold))
            .foregroundStyle(QuartetTheme.accent)
            .frame(width: 32, height: 32)
            .background(QuartetTheme.accent.opacity(0.11), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
    }

    private func workspaceTint(_ item: WorkspaceSummary) -> Color {
        QuartetTheme.workspaceTint(item.id)
    }
}

private struct GraphNodeBadge: View {
    let type: String

    var body: some View {
        Image(systemName: icon)
            .font(.quartet(.regular, weight: .semibold))
            .foregroundStyle(color)
            .frame(width: 36, height: 36)
            .background(color.opacity(0.12), in: RoundedRectangle(cornerRadius: 11, style: .continuous))
            .accessibilityLabel(label)
    }

    private var icon: String {
        switch type {
        case "start": "play.fill"
        case "end": "stop.fill"
        case "shell": "terminal"
        case "prompt": "sparkles"
        case "clarify": "person.crop.circle.badge.questionmark"
        case "ifElse": "arrow.triangle.branch"
        case "loop": "repeat"
        default: "circle.dotted"
        }
    }

    private var label: String {
        switch type {
        case "start": "开始节点"
        case "end": "结束节点"
        case "shell": "Shell 节点"
        case "prompt": "Prompt 节点"
        case "clarify": "澄清节点"
        case "ifElse": "条件节点"
        case "loop": "循环节点"
        default: type
        }
    }

    private var color: Color {
        switch type {
        case "start": QuartetTheme.accent
        case "end": QuartetTheme.failed
        case "shell": QuartetTheme.accentDeep
        case "prompt", "clarify": QuartetTheme.accent
        case "ifElse": QuartetTheme.accentSoft
        case "loop": QuartetTheme.terminalGreenMuted
        default: QuartetTheme.secondaryText
        }
    }
}

private struct GraphGlobalConfigurationView: View {
    @Environment(\.dismiss) private var dismiss
    @Binding var config: GraphConfig

    @State private var variables: [GraphVariableDraft]
    @State private var concurrencyLimit: String
    @State private var defaultNodeTimeoutSec: String
    @State private var jobTimeoutSec: String
    @State private var defaultLoopMaxIters: String
    @State private var instanceLimit: String
    @State private var snapshotByteLimit: String
    @State private var validationMessage: String?

    init(config: Binding<GraphConfig>) {
        _config = config
        let value = config.wrappedValue
        let disabled = Set(value.disabledVars ?? [])
        _variables = State(initialValue: (value.variables ?? [:])
            .sorted { $0.key.localizedStandardCompare($1.key) == .orderedAscending }
            .map { GraphVariableDraft(name: $0.key, value: $0.value, disabled: disabled.contains($0.key)) })
        let run = value.runConfig ?? GraphRunConfiguration()
        _concurrencyLimit = State(initialValue: Self.text(run.concurrencyLimit))
        _defaultNodeTimeoutSec = State(initialValue: Self.text(run.defaultNodeTimeoutSec))
        _jobTimeoutSec = State(initialValue: Self.text(run.jobTimeoutSec))
        _defaultLoopMaxIters = State(initialValue: Self.text(run.defaultLoopMaxIters))
        _instanceLimit = State(initialValue: Self.text(run.instanceLimit))
        _snapshotByteLimit = State(initialValue: run.snapshotByteLimit.map(String.init) ?? "")
    }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    if variables.isEmpty {
                        Text("暂无初始变量")
                            .foregroundStyle(QuartetTheme.secondaryText)
                    }
                    ForEach($variables) { $variable in
                        let isBuiltIn = variable.name == "Code" || variable.name == "Doc"
                        VStack(alignment: .leading, spacing: 8) {
                            HStack {
                                if isBuiltIn {
                                    Label(variable.name, systemImage: "lock.fill")
                                        .font(.quartet(.control, weight: .semibold))
                                        .foregroundStyle(QuartetTheme.primaryText)
                                } else {
                                    TextField("变量名", text: $variable.name)
                                        .textInputAutocapitalization(.never)
                                        .autocorrectionDisabled()
                                    Button(role: .destructive) {
                                        variables.removeAll { $0.id == variable.id }
                                    } label: {
                                        Image(systemName: "trash")
                                    }
                                    .accessibilityLabel("删除变量 \(variable.name)")
                                }
                            }
                            TextField("变量值", text: $variable.value, axis: .vertical)
                                .lineLimit(2...5)
                            Toggle("禁用此变量", isOn: $variable.disabled)
                        }
                    }
                    Button { variables.append(GraphVariableDraft()) } label: {
                        Label("添加变量", systemImage: "plus.circle.fill")
                    }
                } header: {
                    Text("初始变量")
                } footer: {
                    Text("变量名需匹配 [A-Za-z_][A-Za-z0-9_]*；以下划线或 QUARTET_ 开头的名称由系统保留。")
                }

                Section("执行限制") {
                    integerField("并发数", text: $concurrencyLimit, hint: "0 = 默认，最大 16")
                    integerField("默认节点超时（秒）", text: $defaultNodeTimeoutSec, hint: "0 = 不限")
                    integerField("Job 超时（秒）", text: $jobTimeoutSec, hint: "0 = 不限")
                    integerField("默认循环上限", text: $defaultLoopMaxIters, hint: "0 = 默认，最大 1000")
                    integerField("实例数量上限", text: $instanceLimit, hint: "0 = 默认")
                    integerField("快照字节上限", text: $snapshotByteLimit, hint: "0 = 默认")
                }

                if let validationMessage {
                    Section {
                        Text(validationMessage)
                            .foregroundStyle(QuartetTheme.failed)
                            .textSelection(.enabled)
                    }
                }
            }
            .scrollContentBackground(.hidden)
            .background(QuartetTheme.canvas)
            .navigationTitle("全局配置")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("取消") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) { Button("完成") { save() } }
            }
        }
    }

    private func integerField(_ title: String, text: Binding<String>, hint: String) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            TextField(title, text: text)
                .keyboardType(.numberPad)
            Text(hint).font(.quartet(.detail)).foregroundStyle(QuartetTheme.secondaryText)
        }
    }

    private func save() {
        let normalizedNames = variables.map { $0.name.trimmingCharacters(in: .whitespacesAndNewlines) }
        let duplicates = Dictionary(grouping: normalizedNames.filter { !$0.isEmpty }, by: { $0 }).filter { $0.value.count > 1 }.keys.sorted()
        guard !normalizedNames.contains("") else {
            validationMessage = "变量名不能为空。"
            return
        }
        guard duplicates.isEmpty else {
            validationMessage = "变量名重复：\(duplicates.joined(separator: ", "))"
            return
        }
        do {
            let concurrency = try parseInt(concurrencyLimit, field: "并发数", range: 0...16)
            let defaultNodeTimeout = try parseInt(defaultNodeTimeoutSec, field: "默认节点超时", minimum: 0)
            let jobTimeout = try parseInt(jobTimeoutSec, field: "Job 超时", minimum: 0)
            let loopLimit = try parseInt(defaultLoopMaxIters, field: "默认循环上限", range: 0...1000)
            let instances = try parseInt(instanceLimit, field: "实例数量上限", minimum: 0)
            let snapshotBytes = try parseInt64(snapshotByteLimit, field: "快照字节上限", minimum: 0)

            var next = config
            next.variables = Dictionary(uniqueKeysWithValues: zip(normalizedNames, variables.map(\.value)))
            next.disabledVars = zip(normalizedNames, variables).compactMap { name, variable in variable.disabled ? name : nil }
            next.runConfig = GraphRunConfiguration(
                concurrencyLimit: concurrency,
                defaultNodeTimeoutSec: defaultNodeTimeout,
                jobTimeoutSec: jobTimeout,
                defaultLoopMaxIters: loopLimit,
                instanceLimit: instances,
                snapshotByteLimit: snapshotBytes
            )
            config = next
            dismiss()
        } catch {
            validationMessage = String(describing: error)
        }
    }

    private static func text(_ value: Int?) -> String { value.map(String.init) ?? "" }
}

private struct GraphVariableDraft: Identifiable {
    let id = UUID()
    var name = ""
    var value = ""
    var disabled = false

    init(name: String = "", value: String = "", disabled: Bool = false) {
        self.name = name
        self.value = value
        self.disabled = disabled
    }
}

private struct GraphAgentModelSelection: Hashable {
    let agentID: String
    let agentType: String
    let modelID: String
}

private struct GraphNodeConfigurationView: View {
    @Environment(\.dismiss) private var dismiss
    @EnvironmentObject private var appModel: AppModel
    @Binding var node: GraphNode
    let agents: [AgentSummary]

    @State private var draft: GraphNode
    @State private var timeoutSeconds: String
    @State private var fixedCount: String
    @State private var maxIterations: String
    @State private var outputVariables: String
    @State private var validationMessage: String?
    @State private var linkedThoughtLevels: AgentThoughtLevelState?
    @State private var linkedThoughtLevelSelection: GraphAgentModelSelection?
    @State private var thoughtLevelRequestID: UUID?
    @State private var thoughtLevelLinkError: String?

    init(node: Binding<GraphNode>, agents: [AgentSummary]) {
        _node = node
        self.agents = agents
        let value = node.wrappedValue
        _draft = State(initialValue: value)
        let config = value.config ?? GraphNodeConfiguration()
        _timeoutSeconds = State(initialValue: config.timeoutSeconds.map(String.init) ?? "")
        _fixedCount = State(initialValue: config.fixedCount.map(String.init) ?? "")
        _maxIterations = State(initialValue: config.maxIterations.map(String.init) ?? "")
        _outputVariables = State(initialValue: (config.outputVariables ?? []).joined(separator: ", "))
    }

    private var config: GraphNodeConfiguration {
        get { draft.config ?? GraphNodeConfiguration() }
        nonmutating set { draft.config = newValue }
    }

    private var isAgentNode: Bool { draft.type == "prompt" || draft.type == "clarify" }
    private var inheritsSession: Bool { config.sessionStrategy == "inherit" }
    private var thoughtLevelSelection: GraphAgentModelSelection? {
        guard isAgentNode, !inheritsSession, let agent = selectedAgent, agent.available,
              agent.models != nil, let modelID = config.modelId, !modelID.isEmpty else { return nil }
        return GraphAgentModelSelection(agentID: agent.agentId, agentType: agent.type, modelID: modelID)
    }
    private var isLinkingThoughtLevels: Bool {
        guard let selection = thoughtLevelSelection else { return false }
        return linkedThoughtLevelSelection != selection
    }

    var body: some View {
        NavigationStack {
            Form {
                Section {
                    HStack(spacing: 12) {
                        GraphNodeBadge(type: draft.type)
                        VStack(alignment: .leading, spacing: 2) {
                            Text(nodeTypeLabel(draft.type)).font(.quartet(.regular, weight: .semibold))
                            Text(draft.id).font(.quartet(.detail, design: .monospaced)).foregroundStyle(QuartetTheme.secondaryText)
                        }
                    }
                    TextField("节点名称", text: binding(\.title, default: ""))
                }

                nodeSpecificFields

                if let validationMessage {
                    Section {
                        Text(validationMessage)
                            .foregroundStyle(QuartetTheme.failed)
                            .textSelection(.enabled)
                    }
                }
            }
            .scrollContentBackground(.hidden)
            .background(QuartetTheme.canvas)
            .navigationTitle(draft.displayName)
            .navigationBarTitleDisplayMode(.inline)
            .task(id: thoughtLevelSelection) {
                await refreshThoughtLevels(for: thoughtLevelSelection)
            }
            .toolbar {
                ToolbarItem(placement: .cancellationAction) { Button("取消") { dismiss() } }
                ToolbarItem(placement: .confirmationAction) {
                    Button("完成") { save() }
                        .disabled(isLinkingThoughtLevels)
                }
            }
        }
    }

    @ViewBuilder
    private var nodeSpecificFields: some View {
        switch draft.type {
        case "shell":
            Section("Shell") {
                multiline("脚本", text: configBinding(\.script), prompt: "输入 Shell 脚本")
                outputFields
                integerField("节点超时（秒）", text: $timeoutSeconds, hint: "留空则使用全局配置，0 = 不限")
            }
        case "prompt":
            agentSection
            Section("Prompt") {
                multiline("提示词", text: configBinding(\.prompt), prompt: "输入节点提示词")
                outputFields
                integerField("节点超时（秒）", text: $timeoutSeconds, hint: "留空则使用全局配置，0 = 不限")
                multiline("完成后 Hook", text: configBinding(\.hookScript), prompt: "可选 Shell 脚本")
            }
        case "clarify":
            agentSection
            Section("澄清") {
                multiline("提示词", text: configBinding(\.prompt), prompt: "可选，描述需要用户确认的内容")
                outputFields
                integerField("节点超时（秒）", text: $timeoutSeconds, hint: "留空则使用全局配置，0 = 不限")
            }
        case "ifElse":
            Section {
                multiline("条件表达式", text: configBinding(\.condition), prompt: "例如：status == \"ready\"")
            } footer: {
                Text("条件为真走 yes 分支，为假走 no 分支。")
            }
        case "loop":
            loopSection
        case "end":
            endSection
        case "start":
            Section { Text("开始节点只作为工作流入口，没有业务执行配置。") }
        default:
            Section { Text("未知节点类型：\(draft.type)") }
        }
    }

    @ViewBuilder
    private var agentSection: some View {
        Section("Agent 会话") {
            Picker("会话策略", selection: configBinding(\.sessionStrategy, default: "new")) {
                Text("新建会话").tag("new")
                Text("继承上游").tag("inherit")
            }
            if !inheritsSession {
                Picker("Agent", selection: configBinding(\.agentType, default: "")) {
                    Text("请选择").tag("")
                    ForEach(agents) { agent in
                        Text(agent.displayName.isEmpty ? agent.type : agent.displayName)
                            .tag(agent.type)
                            .disabled(!agent.available && agent.type != config.agentType && agent.agentId != config.agentType)
                    }
                    if let reference = config.agentType, !reference.isEmpty,
                       !agents.contains(where: { $0.type == reference || $0.agentId == reference }) {
                        Text("\(reference)（未解析）").tag(reference)
                    }
                }
                .onChange(of: config.agentType) { _, reference in
                    selectAgent(reference ?? "")
                }

                if let agent = selectedAgent {
                    Picker("模型", selection: configBinding(\.modelId, default: "")) {
                        ForEach(agent.models?.availableModels ?? []) { model in
                            Text(model.name.isEmpty ? model.modelId : model.name).tag(model.modelId)
                        }
                        if let modelID = config.modelId, !modelID.isEmpty,
                           agent.models?.availableModels.contains(where: { $0.modelId == modelID }) != true {
                            Text("\(modelID)（当前）").tag(modelID)
                        }
                    }
                    .onChange(of: config.modelId) { _, _ in
                        clearThoughtLevelForNewSelection()
                    }
                    if !(agent.modes?.availableModes ?? []).isEmpty {
                        Picker("模式", selection: configBinding(\.acpMode, default: "")) {
                            Text("跟随 Agent").tag("")
                            ForEach(agent.modes?.availableModes ?? []) { option in
                                Text(option.name).tag(option.id)
                            }
                            if let mode = config.acpMode, !mode.isEmpty,
                               agent.modes?.availableModes.contains(where: { $0.id == mode }) != true {
                                Text("\(mode)（当前）").tag(mode)
                            }
                        }
                    }
                    if isLinkingThoughtLevels {
                        HStack {
                            Text("思考等级")
                            Spacer()
                            ProgressView()
                            Text("正在刷新…")
                                .foregroundStyle(.secondary)
                        }
                    } else if !(linkedThoughtLevels?.availableThoughtLevels ?? []).isEmpty {
                        Picker("思考等级", selection: configBinding(\.acpThoughtLevel, default: "")) {
                            Text("跟随 Agent").tag("")
                            ForEach(linkedThoughtLevels?.availableThoughtLevels ?? []) { option in
                                Text(option.name).tag(option.id)
                            }
                            if let level = config.acpThoughtLevel, !level.isEmpty,
                               linkedThoughtLevels?.availableThoughtLevels.contains(where: { $0.id == level }) != true {
                                Text("\(level)（当前）").tag(level)
                            }
                        }
                    }
                    if let thoughtLevelLinkError {
                        Text(thoughtLevelLinkError)
                            .font(.quartet(.detail))
                            .foregroundStyle(QuartetTheme.failed)
                            .textSelection(.enabled)
                    }
                }
            }
        }
    }

    @ViewBuilder
    private var outputFields: some View {
        TextField("输出变量（逗号分隔）", text: $outputVariables)
            .textInputAutocapitalization(.never)
            .autocorrectionDisabled()
        TextField("最后回复别名", text: configBinding(\.lastAssistantAlias))
            .textInputAutocapitalization(.never)
            .autocorrectionDisabled()
    }

    private var loopSection: some View {
        Section("循环") {
            Picker("循环模式", selection: configBinding(\.loopMode, default: "fixed")) {
                Text("固定次数").tag("fixed")
                Text("满足条件前持续").tag("until")
            }
            .pickerStyle(.segmented)
            if config.loopMode == "until" {
                multiline("终止条件", text: configBinding(\.untilCondition), prompt: "输入条件表达式")
                integerField("最大迭代次数", text: $maxIterations, hint: "0 = 使用全局默认，最大 1000")
            } else {
                integerField("固定次数", text: $fixedCount, hint: "0 = 跳过子图")
            }
        }
    }

    private var endSection: some View {
        Section {
            Picker("结束 Hook", selection: configBinding(\.endHookMode, default: "default")) {
                Text("使用全局默认脚本").tag("default")
                Text("使用自定义脚本").tag("custom")
                Text("关闭").tag("off")
            }
            if config.endHookMode == "custom" {
                multiline("Hook 脚本", text: configBinding(\.hookScript), prompt: "输入 Shell 脚本")
            }
        } footer: {
            Text("结束 Hook 的输出会被忽略，失败只记录日志，不改变工作流结果。")
        }
    }

    private var selectedAgent: AgentSummary? {
        guard let reference = config.agentType else { return nil }
        return agents.first { $0.type == reference || $0.agentId == reference }
    }

    private func selectAgent(_ reference: String) {
        guard let agent = agents.first(where: { $0.type == reference || $0.agentId == reference }) else { return }
        var next = config
        next.agentType = agent.type
        next.modelId = agent.models?.currentModelId ?? agent.models?.availableModels.first?.modelId ?? agent.modelId
        next.acpMode = nil
        next.acpThoughtLevel = nil
        config = next
        invalidateThoughtLevelsIfNeeded()
    }

    private func clearThoughtLevelForNewSelection() {
        var next = config
        next.acpThoughtLevel = nil
        config = next
        invalidateThoughtLevelsIfNeeded()
    }

    private func invalidateThoughtLevelsIfNeeded() {
        guard linkedThoughtLevelSelection != thoughtLevelSelection else { return }
        linkedThoughtLevels = nil
        linkedThoughtLevelSelection = nil
        thoughtLevelLinkError = nil
    }

    private func refreshThoughtLevels(for selection: GraphAgentModelSelection?) async {
        guard let selection else {
            linkedThoughtLevels = nil
            linkedThoughtLevelSelection = nil
            thoughtLevelRequestID = nil
            thoughtLevelLinkError = nil
            return
        }
        invalidateThoughtLevelsIfNeeded()
        let requestID = UUID()
        thoughtLevelRequestID = requestID
        do {
            let state = try await appModel.relinkACPThoughtLevels(
                agentType: selection.agentType,
                modelID: selection.modelID
            )
            try Task.checkCancellation()
            guard thoughtLevelSelection == selection, thoughtLevelRequestID == requestID else { return }
            linkedThoughtLevels = state
            linkedThoughtLevelSelection = selection
            thoughtLevelLinkError = nil
            if let level = config.acpThoughtLevel,
               !level.isEmpty,
               !state.availableThoughtLevels.contains(where: { $0.id == level }) {
                var next = config
                next.acpThoughtLevel = nil
                config = next
            }
        } catch is CancellationError {
            return
        } catch {
            guard thoughtLevelSelection == selection, thoughtLevelRequestID == requestID else { return }
            linkedThoughtLevels = AgentThoughtLevelState(
                availableThoughtLevels: [],
                currentThoughtLevelId: ""
            )
            linkedThoughtLevelSelection = selection
            var next = config
            next.acpThoughtLevel = nil
            config = next
            thoughtLevelLinkError = errorDetail(error)
        }
    }

    private func errorDetail(_ error: Error) -> String {
        if let apiError = error as? APIError {
            return "\(apiError.summary)\n\(apiError.detail)"
        }
        return String(describing: error)
    }

    private func save() {
        do {
            var next = config
            switch draft.type {
            case "shell", "prompt", "clarify":
                next.timeoutSeconds = try parseInt(timeoutSeconds, field: "节点超时", minimum: 0)
                next.outputVariables = outputVariables
                    .split(separator: ",", omittingEmptySubsequences: true)
                    .map { $0.trimmingCharacters(in: .whitespacesAndNewlines) }
                    .filter { !$0.isEmpty }
            default:
                break
            }
            if draft.type == "loop" {
                if next.loopMode == "until" {
                    next.maxIterations = try parseInt(maxIterations, field: "最大迭代次数", range: 0...1000)
                } else {
                    next.fixedCount = try parseInt(fixedCount, field: "固定次数", minimum: 0)
                }
            }
            if isAgentNode, next.sessionStrategy != "inherit", (next.agentType ?? "").isEmpty {
                validationMessage = "新建会话的 Agent 节点必须选择 Agent。"
                return
            }
            draft.config = next
            node = draft
            dismiss()
        } catch {
            validationMessage = String(describing: error)
        }
    }

    private func binding(_ keyPath: WritableKeyPath<GraphNode, String?>, default defaultValue: String) -> Binding<String> {
        Binding(
            get: { draft[keyPath: keyPath] ?? defaultValue },
            set: { draft[keyPath: keyPath] = $0 }
        )
    }

    private func configBinding(_ keyPath: WritableKeyPath<GraphNodeConfiguration, String?>, default defaultValue: String = "") -> Binding<String> {
        Binding(
            get: { config[keyPath: keyPath] ?? defaultValue },
            set: { value in
                var next = config
                next[keyPath: keyPath] = value.isEmpty ? nil : value
                config = next
            }
        )
    }

    private func multiline(_ title: String, text: Binding<String>, prompt: String) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(title).font(.quartet(.detail)).foregroundStyle(QuartetTheme.secondaryText)
            TextField(prompt, text: text, axis: .vertical)
                .lineLimit(4...12)
                .font(.quartet(.regular, design: .monospaced))
        }
    }

    private func integerField(_ title: String, text: Binding<String>, hint: String) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            TextField(title, text: text)
                .keyboardType(.numberPad)
            Text(hint).font(.quartet(.detail)).foregroundStyle(QuartetTheme.secondaryText)
        }
    }

    private func nodeTypeLabel(_ type: String) -> String {
        switch type {
        case "start": "开始节点"
        case "end": "结束节点"
        case "shell": "Shell 节点"
        case "prompt": "Prompt 节点"
        case "clarify": "澄清节点"
        case "ifElse": "条件节点"
        case "loop": "循环节点"
        default: type
        }
    }
}

private enum GraphDraftError: Error, CustomStringConvertible {
    case invalidInteger(field: String, value: String)
    case outOfRange(field: String, range: String)

    var description: String {
        switch self {
        case let .invalidInteger(field, value): "\(field) 必须是整数，当前值：\(value)"
        case let .outOfRange(field, range): "\(field) 必须在 \(range) 范围内。"
        }
    }
}

private func parseInt(_ text: String, field: String, minimum: Int) throws -> Int? {
    let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
    guard !trimmed.isEmpty else { return nil }
    guard let value = Int(trimmed) else { throw GraphDraftError.invalidInteger(field: field, value: text) }
    guard value >= minimum else { throw GraphDraftError.outOfRange(field: field, range: ">= \(minimum)") }
    return value
}

private func parseInt(_ text: String, field: String, range: ClosedRange<Int>) throws -> Int? {
    let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
    guard !trimmed.isEmpty else { return nil }
    guard let value = Int(trimmed) else { throw GraphDraftError.invalidInteger(field: field, value: text) }
    guard range.contains(value) else { throw GraphDraftError.outOfRange(field: field, range: "\(range.lowerBound)...\(range.upperBound)") }
    return value
}

private func parseInt64(_ text: String, field: String, minimum: Int64) throws -> Int64? {
    let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
    guard !trimmed.isEmpty else { return nil }
    guard let value = Int64(trimmed) else { throw GraphDraftError.invalidInteger(field: field, value: text) }
    guard value >= minimum else { throw GraphDraftError.outOfRange(field: field, range: ">= \(minimum)") }
    return value
}
