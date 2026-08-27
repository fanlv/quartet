import SwiftUI

private let defaultWorkspaceID = "ws-1"

private enum WorkspaceEditorPicker: String, Identifiable {
    case directory
    case agent
    case model

    var id: String { rawValue }
}

/// 与 Web 设置页共用同一套工作空间 API：维护工作空间资料、默认 Agent / 模型、
/// 收藏顺序与展示颜色。
@MainActor
struct WorkspaceSettingsView: View {
    @EnvironmentObject private var model: AppModel
    @Environment(\.scenePhase) private var scenePhase

    @State private var workspaces: [WorkspaceSummary] = []
    @State private var agents: [AgentSummary] = []
    @State private var defaultWorkdir = ""
    @State private var isLoading = true
    @State private var isWorking = false
    @State private var loadError = ""
    @State private var supportError = ""
    @State private var message: AgentSettingsMessage?
    @State private var isCreating = false
    @State private var editingWorkspace: WorkspaceSummary?
    @State private var deletingWorkspace: WorkspaceSummary?

    private var canWrite: Bool { model.can("workspace.write") }

    var body: some View {
        Group {
            if isLoading && workspaces.isEmpty {
                AgentSettingsLoadingView(title: "正在加载工作空间…")
            } else if workspaces.isEmpty && !loadError.isEmpty {
                AgentSettingsLoadFailure(detail: loadError) { Task { await load() } }
            } else {
                content
            }
        }
        .background(QuartetTheme.canvas)
        .task { await initialLoad() }
        .onChange(of: scenePhase) { _, phase in
            guard phase == .active, !isLoading, !isWorking, editingWorkspace == nil, !isCreating else { return }
            Task { await load() }
        }
        .toolbar {
            if canWrite {
                ToolbarItem(placement: .topBarTrailing) {
                    Button { isCreating = true } label: {
                        Image(systemName: "plus")
                    }
                    .disabled(isWorking)
                    .accessibilityLabel("新建工作空间")
                    .accessibilityIdentifier("workspace-settings-add")
                }
                .sharedBackgroundVisibility(.hidden)
            }
        }
        .sheet(isPresented: $isCreating) {
            WorkspaceEditorView(
                workspace: nil,
                agents: agents,
                suggestedWorkdir: defaultWorkdir,
                onSaved: applySavedWorkspace
            )
            .presentationDetents([.large])
            .quartetSheetStyle()
        }
        .sheet(item: $editingWorkspace) { workspace in
            WorkspaceEditorView(
                workspace: workspace,
                agents: agents,
                suggestedWorkdir: defaultWorkdir,
                onSaved: applySavedWorkspace
            )
            .presentationDetents([.large])
            .quartetSheetStyle()
        }
        .alert("删除工作空间？", isPresented: deleteAlertBinding) {
            Button("关闭", role: .cancel) { deletingWorkspace = nil }
            Button("删除", role: .destructive) { deleteSelectedWorkspace() }
        } message: {
            if let workspace = deletingWorkspace {
                Text(AppLanguage.localizedFormat(
                    "删除“%@”会同时删除其任务；预置消息会保留为未绑定配置。此操作无法恢复。",
                    workspace.displayName
                ))
            }
        }
    }

    private var content: some View {
        ScrollView {
            LazyVStack(spacing: 12) {
                overviewCard

                if !loadError.isEmpty {
                    AgentSettingsMessageView(kind: .failure, text: loadError)
                }
                if !supportError.isEmpty {
                    AgentSettingsMessageView(kind: .failure, text: supportError)
                }
                if let message {
                    AgentSettingsMessageView(message)
                        .accessibilityIdentifier("workspace-settings-feedback")
                }

                if workspaces.isEmpty {
                    emptyCard
                } else {
                    ForEach(Array(workspaces.enumerated()), id: \.element.id) { index, workspace in
                        workspaceCard(workspace, index: index)
                    }
                }
            }
            .padding(.horizontal, 18)
            .padding(.vertical, 12)
        }
        .refreshable { await load() }
    }

    private var overviewCard: some View {
        AgentSettingsCard("工作空间", systemImage: "folder.badge.gearshape") {
            agentSettingsHint("配置 Agent 的工作目录和默认模型。这里的修改会与 Web 端设置同步。")
            if canWrite {
                Button { regenerateColors() } label: {
                    HStack(spacing: 8) {
                        if isWorking { ProgressView().tint(QuartetTheme.accent) }
                        Label("重新生成颜色", systemImage: "arrow.triangle.2.circlepath")
                    }
                    .font(.quartet(.control, weight: .semibold))
                    .foregroundStyle(QuartetTheme.accentDeep)
                    .frame(maxWidth: .infinity)
                    .frame(minHeight: 46)
                    .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                }
                .buttonStyle(.plain)
                .disabled(isWorking || workspaces.isEmpty)
                .opacity(isWorking || workspaces.isEmpty ? 0.45 : 1)
                .accessibilityIdentifier("workspace-settings-regenerate-colors")
            } else {
                agentSettingsHint("当前账号没有 workspace.write 权限，只能查看工作空间。")
            }
        }
    }

    private var emptyCard: some View {
        AgentSettingsCard {
            ContentUnavailableView {
                Label("暂无工作空间", systemImage: "folder")
                    .font(.quartet(.control, weight: .semibold))
            } description: {
                Text("默认工作空间 ws-1 应由后端自动创建，请检查后端状态。")
                    .font(.quartet(.detail))
            } actions: {
                if canWrite {
                    Button("新建工作空间") { isCreating = true }
                        .buttonStyle(.borderedProminent)
                        .tint(QuartetTheme.accent)
                }
            }
        }
    }

    private func workspaceCard(_ workspace: WorkspaceSummary, index: Int) -> some View {
        AgentSettingsCard {
            Button {
                guard canWrite, !isWorking else { return }
                editingWorkspace = workspace
            } label: {
                HStack(alignment: .top, spacing: 12) {
                    Circle()
                        .fill(QuartetTheme.workspaceTint(workspace))
                        .frame(width: 12, height: 12)
                        .padding(.top, 5)
                        .accessibilityHidden(true)

                    VStack(alignment: .leading, spacing: 5) {
                        HStack(spacing: 8) {
                            Text(workspace.displayName)
                                .font(.quartet(.control, weight: .semibold))
                                .foregroundStyle(QuartetTheme.primaryText)
                                .multilineTextAlignment(.leading)
                            if workspace.id == defaultWorkspaceID {
                                Text("默认")
                                    .font(.quartet(.compact, weight: .bold))
                                    .foregroundStyle(QuartetTheme.accentDeep)
                                    .padding(.horizontal, 7)
                                    .padding(.vertical, 3)
                                    .background(QuartetTheme.accent.opacity(0.1), in: Capsule())
                            }
                        }

                        Text(workspace.workdir)
                            .font(.quartet(.detail, design: .monospaced))
                            .foregroundStyle(QuartetTheme.secondaryText)
                            .lineLimit(2)
                            .truncationMode(.middle)
                            .multilineTextAlignment(.leading)

                        if !workspace.description.isEmpty {
                            Text(workspace.description)
                                .font(.quartet(.detail))
                                .foregroundStyle(QuartetTheme.secondaryText)
                                .lineLimit(2)
                                .multilineTextAlignment(.leading)
                        }

                        if !workspaceDefaultSummary(workspace).isEmpty {
                            Text(workspaceDefaultSummary(workspace))
                                .font(.quartet(.compact))
                                .foregroundStyle(QuartetTheme.secondaryText)
                                .lineLimit(2)
                                .multilineTextAlignment(.leading)
                        }
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)

                    if canWrite {
                        Image(systemName: "chevron.right")
                            .font(.quartet(.compact, weight: .bold))
                            .foregroundStyle(QuartetTheme.secondaryText)
                            .padding(.top, 3)
                    }
                }
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .disabled(!canWrite || isWorking)
            .accessibilityIdentifier("workspace-settings-row-\(workspace.id)")

            if canWrite {
                agentSettingsDivider()
                HStack(spacing: 8) {
                    workspaceActionButton(
                        title: workspace.favorite ? "取消收藏" : "收藏",
                        systemImage: workspace.favorite ? "star.fill" : "star",
                        tint: workspace.favorite ? QuartetTheme.warning : QuartetTheme.secondaryText,
                        identifier: "workspace-settings-favorite-\(workspace.id)",
                        disabled: isWorking
                    ) { toggleFavorite(workspace) }

                    Spacer(minLength: 4)

                    workspaceActionButton(
                        title: "上移",
                        systemImage: "arrow.up",
                        identifier: "workspace-settings-move-up-\(workspace.id)",
                        disabled: isWorking || !canMove(workspace, at: index, offset: -1)
                    ) { moveWorkspace(at: index, offset: -1) }

                    workspaceActionButton(
                        title: "下移",
                        systemImage: "arrow.down",
                        identifier: "workspace-settings-move-down-\(workspace.id)",
                        disabled: isWorking || !canMove(workspace, at: index, offset: 1)
                    ) { moveWorkspace(at: index, offset: 1) }

                    Menu {
                        Button { editingWorkspace = workspace } label: {
                            Label("编辑", systemImage: "pencil")
                        }
                        if workspace.id != defaultWorkspaceID {
                            Button(role: .destructive) { deletingWorkspace = workspace } label: {
                                Label("删除", systemImage: "trash")
                            }
                        }
                    } label: {
                        Image(systemName: "ellipsis")
                            .font(.quartet(.control, weight: .semibold))
                            .foregroundStyle(QuartetTheme.primaryText)
                            .frame(width: 40, height: 40)
                            .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                    }
                    .disabled(isWorking)
                    .accessibilityLabel("更多操作")
                    .accessibilityIdentifier("workspace-settings-more-\(workspace.id)")
                }
            }
        }
    }

    private func workspaceActionButton(
        title: String,
        systemImage: String,
        tint: Color = QuartetTheme.primaryText,
        identifier: String,
        disabled: Bool,
        action: @escaping () -> Void
    ) -> some View {
        Button(action: action) {
            Image(systemName: systemImage)
                .font(.quartet(.control, weight: .semibold))
                .foregroundStyle(tint)
                .frame(width: 40, height: 40)
                .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
        }
        .buttonStyle(.plain)
        .disabled(disabled)
        .opacity(disabled ? 0.4 : 1)
        .accessibilityLabel(title.localizedForApp)
        .accessibilityIdentifier(identifier)
    }

    private var deleteAlertBinding: Binding<Bool> {
        Binding(
            get: { deletingWorkspace != nil },
            set: { if !$0 { deletingWorkspace = nil } }
        )
    }

    private func initialLoad() async {
        guard workspaces.isEmpty else { return }
        await load()
    }

    private func load() async {
        guard !isWorking else { return }
        isLoading = true
        loadError = ""
        supportError = ""
        message = nil
        do {
            let client = try model.apiClient()
            let response = try await client.workspaces()
            applyWorkspaceList(response.workspaces)
            isLoading = false

            var supportFailures: [String] = []
            do {
                let response = try await client.defaultWorkspaceWorkdir()
                defaultWorkdir = response.workdir
            } catch {
                supportFailures.append(agentSettingsErrorDetail(error))
            }
            if model.can("agent.read") {
                do {
                    agents = try await client.agents().agentList.filter(\.available)
                } catch {
                    agents = []
                    supportFailures.append(agentSettingsErrorDetail(error))
                }
            }
            supportError = supportFailures.joined(separator: "\n\n")
        } catch is CancellationError {
            return
        } catch {
            loadError = agentSettingsErrorDetail(error)
            isLoading = false
        }
    }

    private func applyWorkspaceList(_ list: [WorkspaceSummary]) {
        workspaces = list
        model.applyWorkspaceSnapshot(list)
    }

    private func applySavedWorkspace(_ saved: WorkspaceSummary) {
        if let index = workspaces.firstIndex(where: { $0.id == saved.id }) {
            workspaces[index] = saved
        } else {
            workspaces.append(saved)
        }
        workspaces = sorted(workspaces)
        model.applyWorkspaceSnapshot(workspaces)
        message = .success("工作空间已保存".localizedForApp)
    }

    private func toggleFavorite(_ workspace: WorkspaceSummary) {
        performMutation { client in
            let response = try await client.setWorkspaceFavorite(id: workspace.id, favorite: !workspace.favorite)
            self.applyWorkspaceList(response.workspaces)
            self.message = .success((workspace.favorite ? "已取消收藏" : "已收藏").localizedForApp)
        }
    }

    private func canMove(_ workspace: WorkspaceSummary, at index: Int, offset: Int) -> Bool {
        let target = index + offset
        guard workspaces.indices.contains(target) else { return false }
        return workspaces[target].favorite == workspace.favorite
    }

    private func moveWorkspace(at index: Int, offset: Int) {
        let target = index + offset
        guard workspaces.indices.contains(index), workspaces.indices.contains(target),
              workspaces[index].favorite == workspaces[target].favorite else { return }
        var reordered = workspaces
        reordered.swapAt(index, target)
        performMutation { client in
            let response = try await client.reorderWorkspaces(reordered.map(\.id))
            self.applyWorkspaceList(response.workspaces)
            self.message = .success("工作空间顺序已更新".localizedForApp)
        }
    }

    private func regenerateColors() {
        performMutation { client in
            let response = try await client.regenerateWorkspaceColors()
            self.applyWorkspaceList(response.workspaces)
            self.message = .success("工作空间颜色已重新生成".localizedForApp)
        }
    }

    private func deleteSelectedWorkspace() {
        guard let workspace = deletingWorkspace, workspace.id != defaultWorkspaceID else { return }
        deletingWorkspace = nil
        performMutation { client in
            try await client.deleteWorkspace(id: workspace.id)
            let response = try await client.workspaces()
            self.applyWorkspaceList(response.workspaces)
            self.message = .success("工作空间已删除".localizedForApp)
            await self.model.refreshDashboard(
                userInitiated: false,
                presentFailure: false,
                disconnectOnFailure: false
            )
        }
    }

    private func performMutation(_ operation: @escaping @MainActor (APIClient) async throws -> Void) {
        guard !isWorking else { return }
        isWorking = true
        message = nil
        Task { @MainActor in
            defer { isWorking = false }
            do {
                try await operation(model.apiClient())
            } catch is CancellationError {
                return
            } catch {
                message = .failure(agentSettingsErrorDetail(error))
            }
        }
    }

    private func workspaceDefaultSummary(_ workspace: WorkspaceSummary) -> String {
        let agent = workspace.defaultAgent?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        let model = workspace.defaultModel?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        guard !agent.isEmpty || !model.isEmpty else { return "" }
        let agentName = agents.first { $0.agentId == agent || $0.type == agent }?.displayName
        return AppLanguage.localizedFormat(
            "默认：%@ / %@",
            agentName?.isEmpty == false ? agentName! : (agent.isEmpty ? "—" : agent),
            model.isEmpty ? "—" : model
        )
    }

    private func sorted(_ list: [WorkspaceSummary]) -> [WorkspaceSummary] {
        list.sorted { lhs, rhs in
            if lhs.favorite != rhs.favorite { return lhs.favorite }
            if lhs.sortOrder != rhs.sortOrder { return lhs.sortOrder < rhs.sortOrder }
            if lhs.createdAt != rhs.createdAt { return lhs.createdAt < rhs.createdAt }
            return lhs.id < rhs.id
        }
    }
}

@MainActor
private struct WorkspaceEditorView: View {
    @EnvironmentObject private var model: AppModel
    @Environment(\.dismiss) private var dismiss

    let workspace: WorkspaceSummary?
    let agents: [AgentSummary]
    let suggestedWorkdir: String
    let onSaved: (WorkspaceSummary) -> Void

    @State private var title: String
    @State private var workspaceDescription: String
    @State private var workdir: String
    @State private var defaultAgentID: String
    @State private var defaultModelID: String
    @State private var picker: WorkspaceEditorPicker?
    @State private var isSaving = false
    @State private var message: AgentSettingsMessage?

    init(
        workspace: WorkspaceSummary?,
        agents: [AgentSummary],
        suggestedWorkdir: String,
        onSaved: @escaping (WorkspaceSummary) -> Void
    ) {
        self.workspace = workspace
        self.agents = agents
        self.suggestedWorkdir = suggestedWorkdir
        self.onSaved = onSaved
        _title = State(initialValue: workspace?.title ?? "")
        _workspaceDescription = State(initialValue: workspace?.description ?? "")
        _workdir = State(initialValue: workspace?.workdir ?? suggestedWorkdir)
        let savedAgent = workspace?.defaultAgent ?? ""
        let canonicalAgent = agents.first { $0.agentId == savedAgent || $0.type == savedAgent }?.agentId ?? savedAgent
        _defaultAgentID = State(initialValue: canonicalAgent)
        _defaultModelID = State(initialValue: workspace?.defaultModel ?? "")
    }

    private var isEditing: Bool { workspace != nil }
    private var canBrowseDirectories: Bool { model.can("file.read") }
    private var selectedAgent: AgentSummary? {
        agents.first { $0.agentId == defaultAgentID || $0.type == defaultAgentID }
    }
    private var canSelectAgentDefaults: Bool { model.can("agent.read") && !agents.isEmpty }
    private var availableModels: [AgentModel] { selectedAgent?.models?.availableModels ?? [] }
    private var canSave: Bool {
        !title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
            && !workdir.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
            && !isSaving
    }
    private var browseStartPath: String {
        let current = workdir.trimmingCharacters(in: .whitespacesAndNewlines)
        return current.isEmpty ? suggestedWorkdir : current
    }
    private var directoryBrowseRoot: String {
        let suggested = suggestedWorkdir.trimmingCharacters(in: .whitespacesAndNewlines)
        let current = browseStartPath
        guard !suggested.isEmpty else { return current }
        return current == suggested || current.hasPrefix(suggested + "/") ? suggested : current
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(spacing: 12) {
                    detailsCard
                    defaultsCard
                }
                .padding(.horizontal, 18)
                .padding(.vertical, 12)
            }
            .scrollDismissesKeyboard(.interactively)
            .background(QuartetTheme.canvas)
            .quartetNavigationTitle(isEditing ? "编辑工作空间" : "新建工作空间")
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    Button("关闭") { dismiss() }
                        .disabled(isSaving)
                }
                .sharedBackgroundVisibility(.hidden)
            }
            .safeAreaInset(edge: .bottom, spacing: 0) {
                AgentSettingsSaveBar(
                    title: isEditing ? "保存工作空间" : "创建工作空间",
                    savingTitle: "正在保存…",
                    isSaving: isSaving,
                    isEnabled: canSave,
                    message: message,
                    identifier: "workspace-editor-save",
                    action: save
                )
            }
        }
        .sheet(item: $picker) { target in
            pickerSheet(target)
        }
        .task { await loadSuggestedWorkdirIfNeeded() }
    }

    private var detailsCard: some View {
        AgentSettingsCard("基本信息", systemImage: "folder") {
            if workspace?.id == defaultWorkspaceID {
                agentSettingsHint("默认工作空间 ID 为 ws-1，可以改名和修改路径，但不能删除。")
            }

            AgentSettingsTextField(
                title: "名称",
                text: $title,
                identifier: "workspace-editor-title",
                placeholder: "工作空间名称"
            )

            VStack(alignment: .leading, spacing: 6) {
                agentSettingsFieldLabel("描述")
                TextField("可选描述".localizedForApp, text: $workspaceDescription, axis: .vertical)
                    .font(.quartet(.control))
                    .lineLimit(2...5)
                    .padding(.horizontal, 14)
                    .padding(.vertical, 12)
                    .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                    .accessibilityIdentifier("workspace-editor-description")
            }

            VStack(alignment: .leading, spacing: 6) {
                agentSettingsFieldLabel("工作目录")
                HStack(spacing: 8) {
                    TextField("/path/to/workdir", text: $workdir)
                        .font(.quartet(.control, design: .monospaced))
                        .textInputAutocapitalization(.never)
                        .autocorrectionDisabled()
                        .padding(.horizontal, 14)
                        .frame(height: 48)
                        .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                        .accessibilityIdentifier("workspace-editor-workdir")
                    Button {
                        quartetDismissKeyboard()
                        picker = .directory
                    } label: {
                        Image(systemName: "folder")
                            .font(.quartet(.control, weight: .semibold))
                            .foregroundStyle(QuartetTheme.accentDeep)
                            .frame(width: 48, height: 48)
                            .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                    }
                    .buttonStyle(.plain)
                    .disabled(browseStartPath.isEmpty || !canBrowseDirectories)
                    .opacity(browseStartPath.isEmpty || !canBrowseDirectories ? 0.45 : 1)
                    .accessibilityLabel("浏览目录")
                    .accessibilityIdentifier("workspace-editor-browse")
                }
                agentSettingsHint(
                    canBrowseDirectories
                        ? "目录选择器浏览的是 Quartet 服务所在设备，而不是当前 iPhone。"
                        : "当前账号缺少 file.read 权限，请手动输入 Quartet 服务所在设备上的绝对目录。"
                )
            }
        }
    }

    private var defaultsCard: some View {
        AgentSettingsCard("默认运行参数", systemImage: "slider.horizontal.3") {
            agentSettingsHint("新建对话选择这个工作空间时，默认使用这里的 Agent 和模型。Web 与 iOS 共用此配置。")

            AgentSettingsSelectionRow(
                title: "默认 Agent",
                value: selectedAgent.map(agentName) ?? (defaultAgentID.isEmpty ? "使用系统默认" : defaultAgentID),
                placeholder: defaultAgentID.isEmpty,
                identifier: "workspace-editor-agent"
            ) { picker = .agent }
            .disabled(!canSelectAgentDefaults)
            .opacity(canSelectAgentDefaults ? 1 : 0.55)

            if !defaultAgentID.isEmpty, !availableModels.isEmpty || selectedAgent == nil {
                AgentSettingsSelectionRow(
                    title: "默认模型",
                    value: modelName(defaultModelID),
                    placeholder: defaultModelID.isEmpty,
                    identifier: "workspace-editor-model"
                ) { picker = .model }
                .disabled(!canSelectAgentDefaults)
                .opacity(canSelectAgentDefaults ? 1 : 0.55)
            }

            if !model.can("agent.read") {
                agentSettingsHint("当前账号缺少 agent.read 权限，无法选择默认 Agent；已有值会保持不变。")
            } else if agents.isEmpty {
                agentSettingsHint("当前没有可用的 Agent，仍可保存工作空间并使用系统默认值。")
            }
        }
    }

    @ViewBuilder
    private func pickerSheet(_ target: WorkspaceEditorPicker) -> some View {
        switch target {
        case .directory:
            WorkspacePathPickerView(
                variableName: "工作目录",
                workspaceRoot: directoryBrowseRoot,
                initialPath: browseStartPath,
                allowsFileSelection: false,
                navigationTitle: "选择工作目录",
                rootLabel: "浏览根目录",
                accessibilityIdentifierPrefix: "workspace-directory-picker"
            ) { path in
                workdir = path
            }
            .presentationDetents([.large])
            .quartetSheetStyle()
        case .agent:
            QuartetChoiceSheet(
                title: "选择默认 Agent",
                choices: [QuartetChoice(id: "", title: "使用系统默认")]
                    + agents.map { QuartetChoice(id: $0.agentId, title: agentName($0), detail: $0.agentId) },
                selection: Binding(
                    get: { defaultAgentID },
                    set: { selection in
                        if selection != defaultAgentID { defaultModelID = "" }
                        defaultAgentID = selection
                    }
                ),
                accessibilityPrefix: "workspace-editor-agent-choice"
            )
            .presentationDetents([.medium, .large])
            .quartetSheetStyle()
        case .model:
            QuartetChoiceSheet(
                title: "选择默认模型",
                choices: [QuartetChoice(id: "", title: "使用 Agent 默认")]
                    + availableModels.map { QuartetChoice(id: $0.modelId, title: $0.name, detail: $0.description) },
                selection: $defaultModelID,
                accessibilityPrefix: "workspace-editor-model-choice"
            )
            .presentationDetents([.medium, .large])
            .quartetSheetStyle()
        }
    }

    private func save() {
        guard canSave else { return }
        isSaving = true
        message = nil
        let cleanTitle = title.trimmingCharacters(in: .whitespacesAndNewlines)
        let cleanDescription = workspaceDescription.trimmingCharacters(in: .whitespacesAndNewlines)
        let cleanWorkdir = workdir.trimmingCharacters(in: .whitespacesAndNewlines)
        let cleanAgent = selectedAgent?.agentId
            ?? defaultAgentID.trimmingCharacters(in: .whitespacesAndNewlines)
        let cleanModel: String
        if cleanAgent.isEmpty || defaultModelID.isEmpty {
            cleanModel = ""
        } else if selectedAgent == nil {
            cleanModel = defaultModelID
        } else {
            cleanModel = availableModels.contains(where: { $0.modelId == defaultModelID })
                ? defaultModelID
                : ""
        }

        Task { @MainActor in
            do {
                let client = try model.apiClient()
                let saved: WorkspaceSummary
                if let workspace {
                    saved = try await client.updateWorkspace(
                        id: workspace.id,
                        body: WorkspacePatchRequest(
                            expectedVersion: workspace.version,
                            title: cleanTitle,
                            description: cleanDescription,
                            workdir: cleanWorkdir,
                            defaultAgent: cleanAgent,
                            defaultModel: cleanModel
                        )
                    )
                } else {
                    saved = try await client.createWorkspace(CreateWorkspaceRequest(
                        title: cleanTitle,
                        description: cleanDescription,
                        workdir: cleanWorkdir,
                        defaultAgent: cleanAgent,
                        defaultModel: cleanModel
                    ))
                }
                onSaved(saved)
                dismiss()
            } catch is CancellationError {
                return
            } catch {
                message = .failure(agentSettingsErrorDetail(error))
                isSaving = false
            }
        }
    }

    private func loadSuggestedWorkdirIfNeeded() async {
        guard workspace == nil, workdir.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return }
        do {
            let response = try await model.apiClient().defaultWorkspaceWorkdir()
            guard workdir.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty else { return }
            workdir = response.workdir
        } catch is CancellationError {
            return
        } catch {
            message = .failure(agentSettingsErrorDetail(error))
        }
    }

    private func agentName(_ agent: AgentSummary) -> String {
        agent.displayName.isEmpty ? agent.agentId : agent.displayName
    }

    private func modelName(_ id: String) -> String {
        guard !id.isEmpty else { return "使用 Agent 默认" }
        return availableModels.first { $0.modelId == id }?.name ?? id
    }
}
