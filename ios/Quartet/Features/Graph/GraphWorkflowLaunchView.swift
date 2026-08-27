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
    @State private var agentPreferences: [String: AgentPreferences] = [:]
    @State private var loading = true
    @State private var loadingWorkflow = false
    @State private var starting = false
    @State private var showsWorkflowPicker = false
    @State private var showsWorkspacePicker = false
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
                        .font(.quartet(.detail))
                        .foregroundStyle(QuartetTheme.secondaryText)
                        .frame(maxWidth: .infinity, alignment: .center)
                        .padding(.vertical, 32)
                    } else if workflow != nil, let config {
                        workspaceSection
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
        .sheet(isPresented: $showsWorkflowPicker) {
            GraphWorkflowTemplatePicker(
                workflows: workflows,
                selectedWorkflowID: selectedWorkflowID,
                onSelect: { selectedWorkflowID = $0 }
            )
            .presentationDetents([.medium, .large])
            .quartetSheetStyle()
        }
        .sheet(isPresented: $showsWorkspacePicker) {
            WorkspaceLaunchPicker(
                workspaces: appModel.workspaces,
                selectedWorkspaceID: workspaceID,
                accessibilityIdentifierPrefix: "graph-workspace-",
                onSelect: { id in
                    guard let id, let workspace = appModel.workspaces.first(where: { $0.id == id }) else { return }
                    selectWorkspace(workspace)
                }
            )
            .presentationDetents([.medium, .large])
            .quartetSheetStyle()
        }
        .sheet(isPresented: $showsGlobalEditor) {
            if let binding = configBinding {
                GraphGlobalConfigurationView(
                    config: binding,
                    workspaceRoot: selectedWorkspace?.workdir
                )
                    .quartetSheetStyle()
            }
        }
        .sheet(isPresented: Binding(
            get: { editingNodeID != nil },
            set: { if !$0 { editingNodeID = nil } }
        )) {
            if let nodeID = editingNodeID, let nodeBinding = binding(forNodeID: nodeID) {
                GraphNodeConfigurationView(
                    node: nodeBinding,
                    agents: agents,
                    agentPreferences: agentPreferences
                )
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
                .font(.quartet(.control, weight: .semibold))
            Text("正在读取工作流库与 Agent 配置…")
                .font(.quartet(.detail))
                .foregroundStyle(QuartetTheme.secondaryText)
        }
        .frame(maxWidth: .infinity)
        .padding(.top, 52)
    }

    private var emptyState: some View {
        ContentUnavailableView {
            Label("暂无工作流", systemImage: "point.3.connected.trianglepath.dotted")
                .font(.quartet(.control, weight: .semibold))
        } description: {
            Text("请先在 Web 端的 Graph Workflows 页面创建并保存一个工作流。")
                .font(.quartet(.detail))
        } actions: {
            Button("重新加载") { Task { await load() } }
                .font(.quartet(.control, weight: .semibold))
        }
        .frame(maxWidth: .infinity)
        .padding(.top, 36)
    }

    private var workflowSelector: some View {
        VStack(alignment: .leading, spacing: 10) {
            Text("工作流模板")
                .font(.quartet(.control, weight: .semibold))
                .foregroundStyle(QuartetTheme.primaryText)

            Button { showsWorkflowPicker = true } label: {
                HStack(spacing: 12) {
                    configurationIcon("point.3.connected.trianglepath.dotted")
                    VStack(alignment: .leading, spacing: 3) {
                        Text(selectedSummary?.name ?? "选择工作流")
                            .font(.quartet(.control, weight: .semibold))
                            .foregroundStyle(QuartetTheme.primaryText)
                            .lineLimit(2)
                        if let summary = selectedSummary {
                            Text("\(summary.nodeCount) 个节点 · \(summary.edgeCount) 条连线 · \((summary.type ?? "user").uppercased())")
                                .font(.quartet(.detail))
                                .foregroundStyle(QuartetTheme.secondaryText)
                            if let description = summary.description?.trimmingCharacters(in: .whitespacesAndNewlines),
                               !description.isEmpty {
                                Text(description)
                                    .font(.quartet(.detail))
                                    .foregroundStyle(QuartetTheme.secondaryText)
                                    .lineLimit(2)
                                    .multilineTextAlignment(.leading)
                            }
                        }
                    }
                    Spacer(minLength: 8)
                    Image(systemName: "chevron.right")
                        .font(.quartet(.detail, weight: .bold))
                        .foregroundStyle(QuartetTheme.secondaryText)
                }
                .padding(14)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: 18, style: .continuous).stroke(QuartetTheme.divider))
            .accessibilityLabel("工作流模板，当前为\(selectedSummary?.name ?? "未选择")")
            .accessibilityHint("点按弹出工作流模板列表")
            .accessibilityIdentifier("graph-workflow-picker")
        }
    }


    private var workspaceSection: some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .firstTextBaseline) {
                Text("运行空间")
                    .font(.quartet(.control, weight: .semibold))
                    .foregroundStyle(QuartetTheme.primaryText)
                Spacer()
                Text("本次运行")
                    .font(.quartet(.compact))
                    .foregroundStyle(QuartetTheme.secondaryText)
            }

            Button { showsWorkspacePicker = true } label: {
                HStack(spacing: 12) {
                    configurationIcon("square.stack.3d.up")
                    VStack(alignment: .leading, spacing: 2) {
                        Text("工作空间")
                            .font(.quartet(.detail, weight: .medium))
                            .foregroundStyle(QuartetTheme.secondaryText)
                        Text(selectedWorkspace?.displayName ?? "未选择工作空间")
                            .font(.quartet(.control, weight: .semibold))
                            .foregroundStyle(QuartetTheme.primaryText)
                            .lineLimit(1)
                        if let effectiveWorkdir {
                            Text(effectiveWorkdir)
                                .font(.quartet(.compact))
                                .foregroundStyle(QuartetTheme.secondaryText)
                                .lineLimit(1)
                                .truncationMode(.middle)
                        }
                    }
                    Spacer(minLength: 8)
                    Image(systemName: "chevron.right")
                        .font(.quartet(.detail, weight: .bold))
                        .foregroundStyle(QuartetTheme.secondaryText)
                }
                .padding(.horizontal, 14)
                .frame(minHeight: 68)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: 18, style: .continuous).stroke(QuartetTheme.divider))
            .accessibilityLabel("运行空间，当前为\(selectedWorkspace?.displayName ?? "未选择")")
            .accessibilityHint("点按弹出工作空间列表")
            .accessibilityIdentifier("graph-workspace-picker")

            if effectiveWorkdir == nil, !workspaceID.isEmpty {
                Label("工作空间 \(workspaceID) 不存在，请选择可用空间。", systemImage: "exclamationmark.triangle.fill")
                    .font(.quartet(.detail))
                    .foregroundStyle(QuartetTheme.failed)
            }
        }
    }

    private func globalSection(config: GraphConfig) -> some View {
        Button { showsGlobalEditor = true } label: {
            HStack(spacing: 12) {
                configurationIcon("slider.horizontal.3")
                VStack(alignment: .leading, spacing: 2) {
                    Text("全局配置")
                        .font(.quartet(.control, weight: .semibold))
                        .foregroundStyle(QuartetTheme.primaryText)
                    Text(globalConfigurationSummary(config))
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
            .frame(minHeight: 68)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 18, style: .continuous).stroke(QuartetTheme.divider))
        .accessibilityLabel("全局配置，\(globalConfigurationSummary(config))")
        .accessibilityHint("点按编辑初始变量与运行限制")
        .accessibilityIdentifier("graph-global-config")
    }

    private func nodeSection(config: GraphConfig) -> some View {
        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .firstTextBaseline) {
                Text("节点配置")
                    .font(.quartet(.control, weight: .semibold))
                    .foregroundStyle(QuartetTheme.primaryText)
                Spacer()
                Text("\(config.nodes.count) 个节点")
                    .font(.quartet(.compact))
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
                    .font(.quartet(.detail, weight: .semibold))
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
                .frame(minHeight: 54)
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
            agentPreferences = try await appModel.agentPreferences()
        } catch is CancellationError {
            return
        } catch {
            agentPreferences = [:]
            present(error)
        }
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
        // 上次在这里选过的空间优先，其次是运行台的当前筛选，最后才回落到工作流自带的配置。
        let candidates = [
            appModel.lastGraphWorkspaceID,
            appModel.selectedWorkspaceID,
            config.workspaceId,
            workflow.workspaceId
        ]
        for candidate in candidates {
            if let candidate, appModel.workspaces.contains(where: { $0.id == candidate }) {
                return candidate
            }
        }
        return appModel.workspaces.first?.id ?? ""
    }

    private func selectWorkspace(_ workspace: WorkspaceSummary) {
        workspaceID = workspace.id
        appModel.recordGraphWorkspace(workspace.id)
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
        return "并发 \(concurrency) · \(config.variables?.count ?? 0) 个变量"
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
        case "custom": "自定义结束脚本".localizedForApp
        case "off": "结束脚本关闭".localizedForApp
        default: "使用全局结束脚本".localizedForApp
        }
    }

    private func contextPill(_ value: String, icon: String) -> some View {
        Label(value, systemImage: icon)
            .font(.quartet(.compact, weight: .medium))
            .foregroundStyle(QuartetTheme.secondaryText)
            .lineLimit(1)
            .padding(.horizontal, 9)
            .frame(minHeight: 26)
            .background(QuartetTheme.elevated, in: Capsule())
    }

    private func configurationIcon(_ name: String) -> some View {
        Image(systemName: name)
            .font(.quartet(.control, weight: .semibold))
            .foregroundStyle(QuartetTheme.accent)
            .frame(width: 32, height: 32)
            .background(QuartetTheme.accent.opacity(0.11), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
    }
}

/// 工作流模板弹窗，样式与首页“任务操作”弹窗保持一致。
private struct GraphWorkflowTemplatePicker: View {
    @Environment(\.dismiss) private var dismiss

    let workflows: [GraphWorkflowSummary]
    let selectedWorkflowID: String
    let onSelect: (String) -> Void

    var body: some View {
        NavigationStack {
            ScrollView {
                LazyVStack(spacing: 0) {
                    ForEach(Array(workflows.enumerated()), id: \.element.id) { index, workflow in
                        if index > 0 {
                            Divider()
                                .overlay(QuartetTheme.divider)
                                .padding(.leading, 62)
                        }
                        workflowRow(workflow)
                    }
                }
                .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
                .overlay {
                    RoundedRectangle(cornerRadius: 18, style: .continuous)
                        .stroke(QuartetTheme.divider.opacity(0.8), lineWidth: 1)
                }
                .padding(.horizontal, 20)
                .padding(.top, 8)
                .padding(.bottom, 20)
            }
            .background(QuartetTheme.canvas)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .principal) {
                    Text("选择工作流模板")
                        .font(.quartet(.control, weight: .semibold))
                        .foregroundStyle(QuartetTheme.primaryText)
                        .accessibilityAddTraits(.isHeader)
                }
            }
        }
    }

    private func workflowRow(_ workflow: GraphWorkflowSummary) -> some View {
        let selected = workflow.id == selectedWorkflowID
        let detail = "\(workflow.nodeCount) 个节点 · \(workflow.edgeCount) 条连线 · \((workflow.type ?? "user").uppercased())"
        return Button {
            onSelect(workflow.id)
            dismiss()
        } label: {
            HStack(spacing: 12) {
                Image(systemName: "point.3.connected.trianglepath.dotted")
                    .font(.quartet(.control, weight: .semibold))
                    .foregroundStyle(QuartetTheme.accent)
                    .frame(width: 38, height: 38)
                    .background(QuartetTheme.accent.opacity(0.11), in: Circle())
                    .accessibilityHidden(true)

                VStack(alignment: .leading, spacing: 3) {
                    Text(workflow.name)
                        .font(.quartet(.control, weight: .semibold))
                        .foregroundStyle(QuartetTheme.primaryText)
                        .lineLimit(2)
                        .multilineTextAlignment(.leading)
                    Text(detail)
                        .font(.quartet(.detail))
                        .foregroundStyle(QuartetTheme.secondaryText)
                        .lineLimit(1)
                }

                Spacer(minLength: 8)

                if selected {
                    Image(systemName: "checkmark.circle.fill")
                        .font(.quartet(.regular, weight: .semibold))
                        .foregroundStyle(QuartetTheme.accent)
                        .accessibilityHidden(true)
                }
            }
            .padding(.horizontal, 13)
            .frame(minHeight: 64)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel("\(workflow.name)，\(detail)")
        .accessibilityValue(selected ? "已选择" : "")
        .accessibilityHint("选择此工作流模板并关闭弹窗")
        .accessibilityIdentifier("graph-workflow-\(workflow.id)")
    }
}

struct GraphNodeBadge: View {
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
        case "start": "开始节点".localizedForApp
        case "end": "结束节点".localizedForApp
        case "shell": "Shell 节点".localizedForApp
        case "prompt": "Prompt 节点".localizedForApp
        case "clarify": "澄清节点".localizedForApp
        case "ifElse": "条件节点".localizedForApp
        case "loop": "循环节点".localizedForApp
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

/// Graph 表单的语义排版。字号仍全部来自 App 的统一排版入口，这里只固定表单内的层级关系。
/// 表单信息密度与“新任务”的运行配置保持一致：标题与输入内容用控件档，字段标签和说明
/// 再各收一档。以后调整全 App 刻度时，这里仍会随 `QuartetFontSize` 一起缩放。
private enum GraphTypography {
    static var cardTitle: Font { .quartet(.control, weight: .semibold) }
    static var fieldLabel: Font { .quartet(.detail, weight: .semibold) }
    static var fieldValue: Font { .quartet(.control) }
    static var emphasizedFieldValue: Font { .quartet(.control, weight: .medium) }
    static var hint: Font { .quartet(.compact) }

    static func fieldValue(monospaced: Bool) -> Font {
        monospaced ? .quartet(.control, design: .monospaced) : fieldValue
    }
}

/// Graph 编辑弹窗共用的卡片容器，样式跟随定时任务编辑器与首页弹窗。
private struct GraphEditorCard<Content: View>: View {
    let title: String
    let systemImage: String
    let content: Content

    init(_ title: String, systemImage: String, @ViewBuilder content: () -> Content) {
        self.title = title
        self.systemImage = systemImage
        self.content = content()
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack(spacing: 9) {
                Image(systemName: systemImage)
                    .font(.quartet(.control, weight: .semibold))
                    .foregroundStyle(QuartetTheme.accent)
                    .frame(width: 28, height: 28)
                    .background(QuartetTheme.accent.opacity(0.1), in: RoundedRectangle(cornerRadius: 8, style: .continuous))
                    .accessibilityHidden(true)
                Text(title.localizedForApp)
                    .font(GraphTypography.cardTitle)
                    .foregroundStyle(QuartetTheme.primaryText)
                    .accessibilityAddTraits(.isHeader)
            }
            content
        }
        .padding(16)
        .frame(maxWidth: .infinity, alignment: .leading)
        // 卡片底色自己接收点击：落在输入控件之外的位置都收起键盘，输入控件本身仍然优先命中。
        .background {
            RoundedRectangle(cornerRadius: 18, style: .continuous)
                .fill(QuartetTheme.surface)
                .onTapGesture { quartetDismissKeyboard() }
        }
        .overlay(RoundedRectangle(cornerRadius: 18, style: .continuous).stroke(QuartetTheme.divider.opacity(0.8)))
    }
}

@MainActor
private func graphFieldLabel(_ title: String) -> some View {
    Text(title.localizedForApp)
        .font(GraphTypography.fieldLabel)
        .foregroundStyle(QuartetTheme.primaryText)
        .frame(maxWidth: .infinity, alignment: .leading)
}

@MainActor
private func graphFieldHint(_ text: String) -> some View {
    Text(text.localizedForApp)
        .font(GraphTypography.hint)
        .foregroundStyle(QuartetTheme.secondaryText)
        .frame(maxWidth: .infinity, alignment: .leading)
        .fixedSize(horizontal: false, vertical: true)
        .lineSpacing(2)
}

@MainActor
private var graphFieldDivider: some View {
    Divider().overlay(QuartetTheme.divider)
}

private extension View {
    @ViewBuilder
    func graphInputChrome(
        background: Color = QuartetTheme.elevated,
        multiline: Bool = false
    ) -> some View {
        if multiline {
            self
                .foregroundStyle(QuartetTheme.primaryText)
                .tint(QuartetTheme.accent)
                .padding(.horizontal, 14)
                .padding(.vertical, 12)
                .background(background, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                .overlay {
                    RoundedRectangle(cornerRadius: 12, style: .continuous)
                        .stroke(QuartetTheme.divider.opacity(0.8))
                }
        } else {
            self
                .foregroundStyle(QuartetTheme.primaryText)
                .tint(QuartetTheme.accent)
                .padding(.horizontal, 14)
                .frame(minHeight: 50)
                .background(background, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                .overlay {
                    RoundedRectangle(cornerRadius: 12, style: .continuous)
                        .stroke(QuartetTheme.divider.opacity(0.8))
                }
        }
    }
}

@MainActor
private func graphSingleLineField(
    _ title: String,
    text: Binding<String>,
    prompt: String = "",
    monospaced: Bool = false,
    numeric: Bool = false,
    hint: String? = nil,
    identifier: String? = nil
) -> some View {
    VStack(alignment: .leading, spacing: 8) {
        graphFieldLabel(title)
        TextField(prompt.isEmpty ? title : prompt, text: text)
            .font(GraphTypography.fieldValue(monospaced: monospaced))
            .keyboardType(numeric ? .numberPad : .default)
            .textInputAutocapitalization(.never)
            .autocorrectionDisabled()
            .graphInputChrome()
            .accessibilityLabel(title.localizedForApp)
            .accessibilityIdentifier(identifier ?? "")
        if let hint { graphFieldHint(hint) }
    }
}

@MainActor
private func graphMultilineField(
    _ title: String,
    text: Binding<String>,
    prompt: String,
    monospaced: Bool = false,
    hint: String? = nil,
    identifier: String? = nil
) -> some View {
    VStack(alignment: .leading, spacing: 8) {
        graphFieldLabel(title)
        TextField(prompt, text: text, axis: .vertical)
            .lineLimit(4...12)
            .font(GraphTypography.fieldValue(monospaced: monospaced))
            .lineSpacing(3)
            .textInputAutocapitalization(.never)
            .autocorrectionDisabled()
            .graphInputChrome(multiline: true)
            .accessibilityLabel(title.localizedForApp)
            .accessibilityIdentifier(identifier ?? "")
        if let hint { graphFieldHint(hint) }
    }
}

@MainActor
private func graphSelectionField(
    _ title: String,
    value: String,
    placeholder: Bool = false,
    identifier: String,
    action: @escaping () -> Void
) -> some View {
    VStack(alignment: .leading, spacing: 8) {
        graphFieldLabel(title)
        Button(action: action) {
            HStack(spacing: 8) {
                Text(value.localizedForApp)
                    .font(GraphTypography.emphasizedFieldValue)
                    .foregroundStyle(placeholder ? QuartetTheme.secondaryText : QuartetTheme.primaryText)
                    .lineLimit(2)
                    .multilineTextAlignment(.leading)
                Spacer(minLength: 8)
                Image(systemName: "chevron.up.chevron.down")
                    .font(.quartet(.compact, weight: .bold))
                    .foregroundStyle(QuartetTheme.secondaryText)
            }
            .graphInputChrome()
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityLabel("\(title)，当前为\(value)")
        .accessibilityHint("点按弹出可选项列表")
        .accessibilityIdentifier(identifier)
    }
}

@MainActor
private func graphValidationCard(_ message: String) -> some View {
    HStack(alignment: .top, spacing: 10) {
        Image(systemName: "exclamationmark.triangle.fill")
            .font(.quartet(.control, weight: .semibold))
            .foregroundStyle(QuartetTheme.failed)
        Text(message)
            .font(.quartet(.detail))
            .foregroundStyle(QuartetTheme.failed)
            .textSelection(.enabled)
            .frame(maxWidth: .infinity, alignment: .leading)
    }
    .padding(14)
    .background(QuartetTheme.failed.opacity(0.08), in: RoundedRectangle(cornerRadius: 16, style: .continuous))
    .overlay(RoundedRectangle(cornerRadius: 16, style: .continuous).stroke(QuartetTheme.failed.opacity(0.2)))
}

@MainActor
private func graphSaveBar(
    title: String,
    disabled: Bool,
    identifier: String,
    action: @escaping () -> Void
) -> some View {
    Button(action: action) {
        Text(title.localizedForApp)
            .font(.quartet(.control, weight: .semibold))
            .foregroundStyle(QuartetTheme.onAccent)
            .frame(maxWidth: .infinity)
            .frame(minHeight: 50)
            .background(QuartetTheme.accent, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
    }
    .buttonStyle(.plain)
    .disabled(disabled)
    .opacity(disabled ? 0.45 : 1)
    .accessibilityIdentifier(identifier)
    .padding(.horizontal, 18)
    .padding(.vertical, 10)
    .background(.ultraThinMaterial)
}

private enum GraphBuiltInVariable: String, Identifiable {
    case code = "Code"
    case doc = "Doc"

    var id: String { rawValue }
}

struct GraphGlobalConfigurationView: View {
    @EnvironmentObject private var appModel: AppModel
    @Environment(\.dismiss) private var dismiss
    @Binding var config: GraphConfig
    let locksExecutionLimits: Bool
    let workspaceRoot: String?

    @State private var variables: [GraphVariableDraft]
    @State private var concurrencyLimit: String
    @State private var defaultNodeTimeoutSec: String
    @State private var jobTimeoutSec: String
    @State private var defaultLoopMaxIters: String
    @State private var instanceLimit: String
    @State private var snapshotByteLimit: String
    @State private var validationMessage: String?
    @State private var pathPickerVariable: GraphBuiltInVariable?

    init(
        config: Binding<GraphConfig>,
        locksExecutionLimits: Bool = false,
        workspaceRoot: String? = nil
    ) {
        _config = config
        self.locksExecutionLimits = locksExecutionLimits
        self.workspaceRoot = workspaceRoot
        let value = config.wrappedValue
        let disabled = Set(value.disabledVars ?? [])
        var initialVariables = value.variables ?? [:]
        initialVariables[GraphBuiltInVariable.code.rawValue] = initialVariables[GraphBuiltInVariable.code.rawValue] ?? ""
        initialVariables[GraphBuiltInVariable.doc.rawValue] = initialVariables[GraphBuiltInVariable.doc.rawValue] ?? ""
        _variables = State(initialValue: initialVariables
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
            ScrollView {
                VStack(spacing: 14) {
                    variablesCard
                    if locksExecutionLimits {
                        HStack(alignment: .top, spacing: 10) {
                            Image(systemName: "lock.fill")
                                .font(.quartet(.control, weight: .semibold))
                                .foregroundStyle(QuartetTheme.running)
                            Text("运行已经开始，执行限制保持锁定；仍可调整提供给后续节点的全局变量。")
                                .font(.quartet(.detail))
                                .foregroundStyle(QuartetTheme.secondaryText)
                                .fixedSize(horizontal: false, vertical: true)
                        }
                        .padding(14)
                        .frame(maxWidth: .infinity, alignment: .leading)
                        .background(QuartetTheme.running.opacity(0.09), in: RoundedRectangle(cornerRadius: 16))
                        .overlay(RoundedRectangle(cornerRadius: 16).stroke(QuartetTheme.running.opacity(0.22)))
                    } else {
                        limitsCard
                    }
                    if let validationMessage { graphValidationCard(validationMessage) }
                }
                .padding(.horizontal, 18)
                .padding(.top, 10)
                .padding(.bottom, 18)
            }
            .scrollDismissesKeyboard(.interactively)
            .background(
                QuartetTheme.canvas
                    .contentShape(Rectangle())
                    .onTapGesture { quartetDismissKeyboard() }
            )
            .quartetNavigationTitle("全局配置")
            .safeAreaInset(edge: .bottom, spacing: 0) {
                graphSaveBar(
                    title: "保存全局配置",
                    disabled: false,
                    identifier: "graph-global-config-save"
                ) {
                    save()
                }
            }
            .sheet(item: $pathPickerVariable) { variable in
                WorkspacePathPickerView(
                    variableName: variable.rawValue,
                    workspaceRoot: resolvedWorkspaceRoot ?? "",
                    initialPath: variableValue(named: variable.rawValue)
                ) { path in
                    setVariableValue(path, named: variable.rawValue)
                }
                .presentationDetents([.large])
                .quartetSheetStyle()
            }
        }
    }

    private var resolvedWorkspaceRoot: String? {
        if let workspaceRoot = nonEmpty(workspaceRoot) { return workspaceRoot }
        if let workspaceID = nonEmpty(config.workspaceId),
           let workspace = appModel.workspaces.first(where: { $0.id == workspaceID }),
           let workdir = nonEmpty(workspace.workdir) {
            return workdir
        }
        return nonEmpty(config.workdir)
    }

    private var variablesCard: some View {
        GraphEditorCard("初始变量", systemImage: "curlybraces") {
            if variables.isEmpty {
                Text("暂无初始变量")
                    .font(.quartet(.detail))
                    .foregroundStyle(QuartetTheme.secondaryText)
            }

            ForEach(variables) { variable in
                variableBlock(binding(for: variable.id))
            }

            Button { variables.append(GraphVariableDraft()) } label: {
                Label("添加变量", systemImage: "plus.circle.fill")
                    .font(.quartet(.control, weight: .semibold))
                    .foregroundStyle(QuartetTheme.accent)
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("graph-global-config-add-variable")

            graphFieldHint("变量名需匹配 [A-Za-z_][A-Za-z0-9_]*；以下划线或 QUARTET_ 开头的名称由系统保留。")
        }
    }

    private func binding(for id: UUID) -> Binding<GraphVariableDraft> {
        let list = $variables
        return Binding(
            get: { list.wrappedValue.first { $0.id == id } ?? GraphVariableDraft() },
            set: { newValue in
                guard let index = list.wrappedValue.firstIndex(where: { $0.id == id }) else { return }
                list.wrappedValue[index] = newValue
            }
        )
    }

    private func variableBlock(_ variable: Binding<GraphVariableDraft>) -> some View {
        let builtIn = GraphBuiltInVariable(rawValue: variable.wrappedValue.name)
        let isBuiltIn = builtIn != nil
        return VStack(alignment: .leading, spacing: 12) {
            HStack(alignment: .bottom, spacing: 10) {
                VStack(alignment: .leading, spacing: 8) {
                    graphFieldLabel("变量名")
                    if isBuiltIn {
                        Label(variable.wrappedValue.name, systemImage: "lock.fill")
                            .font(GraphTypography.emphasizedFieldValue)
                            .graphInputChrome(background: QuartetTheme.surface)
                    } else {
                        TextField("变量名", text: variable.name)
                            .font(.quartet(.control, weight: .medium, design: .monospaced))
                            .textInputAutocapitalization(.never)
                            .autocorrectionDisabled()
                            .graphInputChrome(background: QuartetTheme.surface)
                            .accessibilityLabel("变量名")
                    }
                }

                if !isBuiltIn {
                    Button {
                        let id = variable.wrappedValue.id
                        quartetDismissKeyboard()
                        variables.removeAll { $0.id == id }
                    } label: {
                        Image(systemName: "trash")
                            .font(.quartet(.control, weight: .semibold))
                            .foregroundStyle(QuartetTheme.failed)
                            .frame(width: 50, height: 50)
                            .background(QuartetTheme.failed.opacity(0.1), in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel("删除变量 \(variable.wrappedValue.name)")
                }
            }

            VStack(alignment: .leading, spacing: 8) {
                graphFieldLabel("变量值")
                HStack(alignment: .top, spacing: 9) {
                    TextField("变量值", text: variable.value, axis: .vertical)
                        .lineLimit(2...5)
                        .font(GraphTypography.fieldValue)
                        .lineSpacing(3)
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .graphInputChrome(background: QuartetTheme.surface, multiline: true)
                        .accessibilityLabel("变量值")

                    if let builtIn {
                        Button {
                            quartetDismissKeyboard()
                            pathPickerVariable = builtIn
                        } label: {
                            Image(systemName: "folder")
                                .font(.quartet(.control, weight: .semibold))
                                .foregroundStyle(QuartetTheme.accent)
                                .frame(width: 50, height: 50)
                                .background(QuartetTheme.accent.opacity(0.1), in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                        }
                        .buttonStyle(.plain)
                        .disabled(resolvedWorkspaceRoot == nil)
                        .opacity(resolvedWorkspaceRoot == nil ? 0.45 : 1)
                        .accessibilityLabel(AppLanguage.localizedFormat("浏览 %@ 的目录或文件", builtIn.rawValue))
                        .accessibilityHint("从当前工作空间选择目录或文件".localizedForApp)
                        .accessibilityIdentifier("graph-global-\(builtIn.rawValue.lowercased())-path-picker")
                    }
                }

                if isBuiltIn {
                    graphFieldHint(resolvedWorkspaceRoot == nil
                        ? "当前工作空间没有可浏览的目录。"
                        : "可输入任意文本，或从当前工作空间选择目录或文件。")
                }
            }

            Toggle("禁用此变量", isOn: variable.disabled)
                .font(.quartet(.control, weight: .medium))
                .foregroundStyle(QuartetTheme.primaryText)
                .tint(QuartetTheme.accent)
        }
        .padding(12)
        .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
    }

    private var limitsCard: some View {
        GraphEditorCard("执行限制", systemImage: "slider.horizontal.3") {
            VStack(alignment: .leading, spacing: 12) {
                graphSingleLineField("并发数", text: $concurrencyLimit, prompt: "留空使用默认", numeric: true, hint: "0 = 默认，最大 16")
                graphFieldDivider
                graphSingleLineField("默认节点超时（秒）", text: $defaultNodeTimeoutSec, prompt: "留空使用默认", numeric: true, hint: "0 = 不限")
                graphFieldDivider
                graphSingleLineField("Job 超时（秒）", text: $jobTimeoutSec, prompt: "留空使用默认", numeric: true, hint: "0 = 不限")
                graphFieldDivider
                graphSingleLineField("默认循环上限", text: $defaultLoopMaxIters, prompt: "留空使用默认", numeric: true, hint: "0 = 默认，最大 1000")
            }
            VStack(alignment: .leading, spacing: 12) {
                graphFieldDivider
                graphSingleLineField("实例数量上限", text: $instanceLimit, prompt: "留空使用默认", numeric: true, hint: "0 = 默认")
                graphFieldDivider
                graphSingleLineField("快照字节上限", text: $snapshotByteLimit, prompt: "留空使用默认", numeric: true, hint: "0 = 默认")
            }
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

    private func variableValue(named name: String) -> String {
        variables.first(where: { $0.name == name })?.value ?? ""
    }

    private func setVariableValue(_ value: String, named name: String) {
        guard let index = variables.firstIndex(where: { $0.name == name }) else { return }
        variables[index].value = value
    }

    private func nonEmpty(_ value: String?) -> String? {
        guard let trimmed = value?.trimmingCharacters(in: .whitespacesAndNewlines),
              !trimmed.isEmpty else { return nil }
        return trimmed
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

enum GraphNodeEditingRestriction: Equatable {
    case none
    case frozen
    case loopFixedCountOnly
}

/// 节点编辑弹窗里的“选一个”入口，统一走 `QuartetChoiceSheet`。
private enum GraphNodePicker: String, Identifiable {
    case sessionStrategy
    case agent
    case model
    case mode
    case thoughtLevel
    case endHook

    var id: String { rawValue }

    var title: String {
        switch self {
        case .sessionStrategy: "会话策略".localizedForApp
        case .agent: "选择 Agent".localizedForApp
        case .model: "选择模型".localizedForApp
        case .mode: "选择模式".localizedForApp
        case .thoughtLevel: "选择思考等级".localizedForApp
        case .endHook: "结束 Hook".localizedForApp
        }
    }
}

struct GraphNodeConfigurationView: View {
    @Environment(\.dismiss) private var dismiss
    @EnvironmentObject private var appModel: AppModel
    @Binding var node: GraphNode
    let agents: [AgentSummary]
    let agentPreferences: [String: AgentPreferences]
    let editingRestriction: GraphNodeEditingRestriction

    /// “选择 Agent”弹窗副标题要显示的版本号与用量。全局单例，跨弹窗复用缓存与节流。
    @ObservedObject private var agentUsageSummaries = AgentUsageSummaryStore.shared

    @State private var draft: GraphNode
    @State private var timeoutSeconds: String
    @State private var fixedCount: String
    @State private var maxIterations: String
    @State private var outputVariables: String
    @State private var validationMessage: String?
    @State private var picker: GraphNodePicker?
    @State private var linkedThoughtLevels: AgentThoughtLevelState?
    @State private var linkedThoughtLevelSelection: GraphAgentModelSelection?
    @State private var thoughtLevelRequestID: UUID?
    @State private var thoughtLevelLinkError: String?

    init(
        node: Binding<GraphNode>,
        agents: [AgentSummary],
        agentPreferences: [String: AgentPreferences],
        editingRestriction: GraphNodeEditingRestriction = .none
    ) {
        _node = node
        self.agents = agents
        self.agentPreferences = agentPreferences
        self.editingRestriction = editingRestriction
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
        guard editingRestriction != .frozen, isAgentNode, !inheritsSession, let agent = selectedAgent, agent.available,
              agent.models != nil, let modelID = config.modelId, !modelID.isEmpty else { return nil }
        return GraphAgentModelSelection(agentID: agent.agentId, agentType: agent.type, modelID: modelID)
    }
    private var isLinkingThoughtLevels: Bool {
        guard let selection = thoughtLevelSelection else { return false }
        return linkedThoughtLevelSelection != selection
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(spacing: 14) {
                    identityCard
                    if editingRestriction != .none {
                        frozenConfigurationNotice
                    }
                    nodeSpecificCards
                        .disabled(editingRestriction == .frozen)
                        .opacity(editingRestriction == .frozen ? 0.58 : 1)
                    if let validationMessage { graphValidationCard(validationMessage) }
                }
                .padding(.horizontal, 18)
                .padding(.top, 10)
                .padding(.bottom, 18)
            }
            .scrollDismissesKeyboard(.interactively)
            .background(
                QuartetTheme.canvas
                    .contentShape(Rectangle())
                    .onTapGesture { quartetDismissKeyboard() }
            )
            .quartetNavigationTitle(draft.displayName)
            .task(id: thoughtLevelSelection) {
                await refreshThoughtLevels(for: thoughtLevelSelection)
            }
            .onChange(of: config.agentType) { _, reference in
                guard editingRestriction != .frozen else { return }
                selectAgent(reference ?? "")
            }
            .onChange(of: config.modelId) { _, _ in
                guard editingRestriction != .frozen else { return }
                clearThoughtLevelForNewSelection()
            }
            .safeAreaInset(edge: .bottom, spacing: 0) {
                graphSaveBar(
                    title: isLinkingThoughtLevels ? "正在刷新思考等级…" : "保存节点配置",
                    disabled: isLinkingThoughtLevels,
                    identifier: "graph-node-config-save"
                ) {
                    save()
                }
            }
            .sheet(item: $picker) { picker in
                QuartetChoiceSheet(
                    title: picker.title,
                    choices: choices(for: picker),
                    selection: selectionBinding(for: picker),
                    accessibilityPrefix: "graph-node-\(picker.rawValue)-option",
                    favoriteIDs: picker == .model ? favoriteModelIDs : []
                )
                .presentationDetents([.medium, .large])
                .quartetSheetStyle()
                .task {
                    guard picker == .agent else { return }
                    await loadAgentUsageSummaries()
                }
            }
        }
    }

    private var frozenConfigurationNotice: some View {
        HStack(alignment: .top, spacing: 10) {
            Image(systemName: "lock.fill")
                .font(.quartet(.control, weight: .semibold))
                .foregroundStyle(QuartetTheme.running)
            Text(editingRestriction == .loopFixedCountOnly
                ? "这个 Loop 已经开始执行，只能修改固定次数；新值会在下一轮边界生效。"
                : "这个节点已执行或正在执行，运行配置已经冻结；仍可修改仅用于显示的节点名称。")
                .font(.quartet(.detail))
                .foregroundStyle(QuartetTheme.secondaryText)
                .fixedSize(horizontal: false, vertical: true)
        }
        .padding(14)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(QuartetTheme.running.opacity(0.09), in: RoundedRectangle(cornerRadius: 16))
        .overlay(RoundedRectangle(cornerRadius: 16).stroke(QuartetTheme.running.opacity(0.22)))
    }

    private var identityCard: some View {
        GraphEditorCard("节点", systemImage: "square.on.square") {
            HStack(spacing: 12) {
                GraphNodeBadge(type: draft.type)
                VStack(alignment: .leading, spacing: 2) {
                    Text(nodeTypeLabel(draft.type))
                        .font(.quartet(.control, weight: .semibold))
                        .foregroundStyle(QuartetTheme.primaryText)
                    Text(draft.id)
                        .font(.quartet(.compact, design: .monospaced))
                        .foregroundStyle(QuartetTheme.secondaryText)
                        .textSelection(.enabled)
                }
                Spacer(minLength: 8)
            }
            graphFieldDivider
            graphSingleLineField(
                "节点名称",
                text: binding(\.title, default: ""),
                prompt: "输入节点名称",
                identifier: "graph-node-title"
            )
        }
    }

    @ViewBuilder
    private var nodeSpecificCards: some View {
        switch draft.type {
        case "shell":
            GraphEditorCard("Shell", systemImage: "terminal") {
                graphMultilineField("脚本", text: configBinding(\.script), prompt: "输入 Shell 脚本", monospaced: true)
                graphFieldDivider
                outputFields
                graphFieldDivider
                timeoutField
            }
        case "prompt":
            agentCard
            GraphEditorCard("Prompt", systemImage: "text.bubble") {
                graphMultilineField("提示词", text: configBinding(\.prompt), prompt: "输入节点提示词")
                graphFieldDivider
                outputFields
                graphFieldDivider
                timeoutField
                graphFieldDivider
                graphMultilineField("完成后 Hook", text: configBinding(\.hookScript), prompt: "可选 Shell 脚本", monospaced: true)
            }
        case "clarify":
            agentCard
            GraphEditorCard("澄清", systemImage: "questionmark.bubble") {
                graphMultilineField("提示词", text: configBinding(\.prompt), prompt: "可选，描述需要用户确认的内容")
                graphFieldDivider
                outputFields
                graphFieldDivider
                timeoutField
            }
        case "ifElse":
            GraphEditorCard("条件", systemImage: "arrow.triangle.branch") {
                graphMultilineField(
                    "条件表达式",
                    text: configBinding(\.condition),
                    prompt: "例如：status == \"ready\"",
                    monospaced: true,
                    hint: "条件为真走 yes 分支，为假走 no 分支。"
                )
            }
        case "loop":
            if editingRestriction == .loopFixedCountOnly {
                GraphEditorCard("循环", systemImage: "arrow.triangle.2.circlepath") {
                    graphSingleLineField(
                        "固定次数",
                        text: $fixedCount,
                        prompt: "留空使用全局默认",
                        numeric: true,
                        hint: "0 = 跳过子图；保存后从下一轮边界生效"
                    )
                }
            } else {
                loopCard
            }
        case "end":
            endCard
        case "start":
            GraphEditorCard("说明", systemImage: "info.circle") {
                Text("开始节点只作为工作流入口，没有业务执行配置。")
                    .font(.quartet(.detail))
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        default:
            GraphEditorCard("说明", systemImage: "questionmark.circle") {
                Text("未知节点类型：\(draft.type)")
                    .font(.quartet(.detail))
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
    }

    private var agentCard: some View {
        GraphEditorCard("Agent 会话", systemImage: "person.crop.square") {
            graphSelectionField(
                "会话策略",
                value: displayTitle(for: .sessionStrategy),
                identifier: "graph-node-sessionStrategy"
            ) {
                picker = .sessionStrategy
            }

            if !inheritsSession {
                graphFieldDivider
                graphSelectionField(
                    "Agent",
                    value: displayTitle(for: .agent),
                    placeholder: (config.agentType ?? "").isEmpty,
                    identifier: "graph-node-agent"
                ) {
                    picker = .agent
                }

                if let agent = selectedAgent {
                    graphFieldDivider
                    graphSelectionField(
                        "模型",
                        value: displayTitle(for: .model),
                        placeholder: (config.modelId ?? "").isEmpty,
                        identifier: "graph-node-model"
                    ) {
                        picker = .model
                    }

                    if !(agent.modes?.availableModes ?? []).isEmpty {
                        graphFieldDivider
                        graphSelectionField(
                            "模式",
                            value: displayTitle(for: .mode),
                            identifier: "graph-node-mode"
                        ) {
                            picker = .mode
                        }
                    }

                    if isLinkingThoughtLevels {
                        graphFieldDivider
                        VStack(alignment: .leading, spacing: 8) {
                            graphFieldLabel("思考等级")
                            HStack(spacing: 10) {
                                ProgressView().controlSize(.small)
                                Text("正在刷新…")
                                    .font(GraphTypography.fieldValue)
                                    .foregroundStyle(QuartetTheme.secondaryText)
                                Spacer(minLength: 0)
                            }
                            .graphInputChrome()
                        }
                    } else if !(linkedThoughtLevels?.availableThoughtLevels ?? []).isEmpty {
                        graphFieldDivider
                        graphSelectionField(
                            "思考等级",
                            value: displayTitle(for: .thoughtLevel),
                            identifier: "graph-node-thoughtLevel"
                        ) {
                            picker = .thoughtLevel
                        }
                    }

                    if let thoughtLevelLinkError {
                        Text(thoughtLevelLinkError)
                            .font(.quartet(.detail))
                            .foregroundStyle(QuartetTheme.failed)
                            .textSelection(.enabled)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                }
            }
        }
    }

    @ViewBuilder
    private var outputFields: some View {
        graphSingleLineField(
            "输出变量",
            text: $outputVariables,
            prompt: "逗号分隔，例如：result, summary",
            monospaced: true,
            identifier: "graph-node-output-variables"
        )
        graphFieldDivider
        graphSingleLineField(
            "最后回复别名",
            text: configBinding(\.lastAssistantAlias),
            prompt: "可选，为最后一条回复命名"
        )
    }

    private var timeoutField: some View {
        graphSingleLineField(
            "节点超时（秒）",
            text: $timeoutSeconds,
            prompt: "留空使用全局配置",
            numeric: true,
            hint: "0 = 不限"
        )
    }

    private var loopCard: some View {
        GraphEditorCard("循环", systemImage: "arrow.triangle.2.circlepath") {
            VStack(alignment: .leading, spacing: 6) {
                graphFieldLabel("循环模式")
                Picker("循环模式", selection: configBinding(\.loopMode, default: "fixed")) {
                    Text("固定次数").tag("fixed")
                    Text("满足条件前持续").tag("until")
                }
                .font(.quartet(.detail, weight: .medium))
                .pickerStyle(.segmented)
                .labelsHidden()
            }
            graphFieldDivider
            if config.loopMode == "until" {
                graphMultilineField(
                    "终止条件",
                    text: configBinding(\.untilCondition),
                    prompt: "输入条件表达式",
                    monospaced: true
                )
                graphFieldDivider
                graphSingleLineField(
                    "最大迭代次数",
                    text: $maxIterations,
                    prompt: "留空使用全局默认",
                    numeric: true,
                    hint: "0 = 使用全局默认，最大 1000"
                )
            } else {
                graphSingleLineField(
                    "固定次数",
                    text: $fixedCount,
                    prompt: "留空使用全局默认",
                    numeric: true,
                    hint: "0 = 跳过子图"
                )
            }
        }
    }

    private var endCard: some View {
        GraphEditorCard("结束 Hook", systemImage: "flag.checkered") {
            graphSelectionField(
                "Hook 模式",
                value: displayTitle(for: .endHook),
                identifier: "graph-node-endHook"
            ) {
                picker = .endHook
            }
            if config.endHookMode == "custom" {
                graphFieldDivider
                graphMultilineField("Hook 脚本", text: configBinding(\.hookScript), prompt: "输入 Shell 脚本", monospaced: true)
            }
            graphFieldHint("结束 Hook 的输出会被忽略，失败只记录日志，不改变工作流结果。")
        }
    }

    private func selectionBinding(for picker: GraphNodePicker) -> Binding<String> {
        switch picker {
        case .sessionStrategy: configBinding(\.sessionStrategy, default: "new")
        case .agent: configBinding(\.agentType, default: "")
        case .model: configBinding(\.modelId, default: "")
        case .mode: configBinding(\.acpMode, default: "")
        case .thoughtLevel: configBinding(\.acpThoughtLevel, default: "")
        case .endHook: configBinding(\.endHookMode, default: "default")
        }
    }

    /// 打开“选择 Agent”弹窗时读取每个 Agent 的版本号与用量：先出缓存，再后台刷新。
    /// 失败不占用节流窗口，所以行内“重试”按钮直接再调一次。
    private func loadAgentUsageSummaries() async {
        await agentUsageSummaries.load(agents: agents, model: appModel)
    }

    private func choices(for picker: GraphNodePicker) -> [QuartetChoice] {
        switch picker {
        case .sessionStrategy:
            return [
                QuartetChoice(id: "new", title: "新建会话", detail: "为该节点单独创建 Agent 会话"),
                QuartetChoice(id: "inherit", title: "继承上游", detail: "复用上游节点的 Agent 会话与上下文")
            ]
        case .agent:
            var items = agents.map { agent in
                QuartetChoice.agent(
                    id: agent.type,
                    title: agent.displayName.isEmpty ? agent.type : agent.displayName,
                    command: agent.type,
                    note: agent.available ? nil : "未安装".localizedForApp,
                    disabled: !agent.available && agent.type != config.agentType && agent.agentId != config.agentType,
                    usage: agentUsageSummaries.summary(agent: agent),
                    retry: { Task { await loadAgentUsageSummaries() } }
                )
            }
            if let reference = config.agentType, !reference.isEmpty,
               !agents.contains(where: { $0.type == reference || $0.agentId == reference }) {
                items.append(QuartetChoice(id: reference, title: reference, detail: "未解析"))
            }
            return items
        case .model:
            guard let agent = selectedAgent else { return [] }
            var items = orderedModels(for: agent).map { model in
                QuartetChoice(id: model.modelId, title: model.name.isEmpty ? model.modelId : model.name, detail: model.modelId)
            }
            if let modelID = config.modelId, !modelID.isEmpty,
               agent.models?.availableModels.contains(where: { $0.modelId == modelID }) != true {
                items.append(QuartetChoice(id: modelID, title: modelID, detail: "当前"))
            }
            return items
        case .mode:
            guard let agent = selectedAgent else { return [] }
            var items = [QuartetChoice(id: "", title: "跟随 Agent", detail: "使用 Agent 自身的默认模式")]
            items += (agent.modes?.availableModes ?? []).map { QuartetChoice(id: $0.id, title: $0.name) }
            if let mode = config.acpMode, !mode.isEmpty,
               agent.modes?.availableModes.contains(where: { $0.id == mode }) != true {
                items.append(QuartetChoice(id: mode, title: mode, detail: "当前"))
            }
            return items
        case .thoughtLevel:
            var items = [QuartetChoice(id: "", title: "跟随 Agent", detail: "使用 Agent 自身的默认思考等级")]
            items += (linkedThoughtLevels?.availableThoughtLevels ?? []).map { QuartetChoice(id: $0.id, title: $0.name) }
            if let level = config.acpThoughtLevel, !level.isEmpty,
               linkedThoughtLevels?.availableThoughtLevels.contains(where: { $0.id == level }) != true {
                items.append(QuartetChoice(id: level, title: level, detail: "当前"))
            }
            return items
        case .endHook:
            return [
                QuartetChoice(id: "default", title: "使用全局默认脚本", detail: "跟随工作流的默认结束 Hook"),
                QuartetChoice(id: "custom", title: "使用自定义脚本", detail: "只在该节点执行自定义 Shell 脚本"),
                QuartetChoice(id: "off", title: "关闭", detail: "结束时不执行任何 Hook")
            ]
        }
    }

    private func displayTitle(for picker: GraphNodePicker) -> String {
        let current = selectionBinding(for: picker).wrappedValue
        if let match = choices(for: picker).first(where: { $0.id == current }) {
            return match.title
        }
        return current.isEmpty ? "请选择" : current
    }

    private var selectedAgent: AgentSummary? {
        guard let reference = config.agentType else { return nil }
        return agents.first { $0.type == reference || $0.agentId == reference }
    }

    private var selectedAgentPreferences: AgentPreferences? {
        guard let selectedAgent else { return nil }
        return agentPreferences[selectedAgent.agentId] ?? agentPreferences[selectedAgent.type]
    }

    private var favoriteModelIDs: Set<String> {
        Set(selectedAgentPreferences?.favoriteModelIDs ?? [])
    }

    private func orderedModels(for agent: AgentSummary) -> [AgentModel] {
        let availableModels = agent.models?.availableModels ?? []
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

    private func nodeTypeLabel(_ type: String) -> String {
        switch type {
        case "start": "开始节点".localizedForApp
        case "end": "结束节点".localizedForApp
        case "shell": "Shell 节点".localizedForApp
        case "prompt": "Prompt 节点".localizedForApp
        case "clarify": "澄清节点".localizedForApp
        case "ifElse": "条件节点".localizedForApp
        case "loop": "循环节点".localizedForApp
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
