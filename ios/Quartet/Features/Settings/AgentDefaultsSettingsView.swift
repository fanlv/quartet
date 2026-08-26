import SwiftUI

/// 可编辑的偏好草稿。`AgentPreferences` 用于收发，草稿负责界面上的增删改。
private struct AgentPrefsDraft: Equatable {
    var favoriteModelIDs: [String] = []
    var defaultModelID: String = ""
    var defaultMode: String = ""
    var defaultThoughtLevel: String = ""

    init() {}

    init(_ preferences: AgentPreferences?) {
        favoriteModelIDs = preferences?.favoriteModelIDs ?? []
        defaultModelID = preferences?.defaultModelID ?? ""
        defaultMode = preferences?.defaultMode ?? ""
        defaultThoughtLevel = preferences?.defaultThoughtLevel ?? ""
    }

    /// 空值一律不落库，交给运行时按 Agent 的实时列表回退。
    var payload: AgentPreferences {
        AgentPreferences(
            favoriteModelIDs: favoriteModelIDs,
            defaultModelID: defaultModelID.isEmpty ? nil : defaultModelID,
            defaultMode: defaultMode.isEmpty ? nil : defaultMode,
            defaultThoughtLevel: defaultThoughtLevel.isEmpty ? nil : defaultThoughtLevel
        )
    }
}

private enum AgentDefaultsPicker: String, Identifiable {
    case model
    case mode
    case thoughtLevel

    var id: String { rawValue }

    var title: String {
        switch self {
        case .model: "选择默认模型"
        case .mode: "选择默认模式"
        case .thoughtLevel: "选择默认思考等级"
        }
    }
}

@MainActor
struct AgentDefaultsSettingsView: View {
    @EnvironmentObject private var model: AppModel
    @StateObject private var thoughtLevels = AgentThoughtLevelStore()

    /// “选择 Agent”弹窗副标题要显示的版本号与用量。全局单例，跨弹窗复用缓存与节流。
    @ObservedObject private var agentUsageSummaries = AgentUsageSummaryStore.shared

    @State private var agents: [AgentSummary] = []
    @State private var drafts: [String: AgentPrefsDraft] = [:]
    @State private var dirtyAgentIDs: Set<String> = []
    @State private var activeAgentID = ""
    @State private var isLoading = true
    @State private var loadError = ""
    @State private var isSaving = false
    @State private var message: AgentSettingsMessage?
    @State private var showsAgentPicker = false
    @State private var showsFavoritePicker = false
    @State private var picker: AgentDefaultsPicker?

    private var canWrite: Bool { model.can("config.write") }
    private var activeAgent: AgentSummary? { agents.first { $0.agentId == activeAgentID } }
    private var draft: AgentPrefsDraft { drafts[activeAgentID] ?? AgentPrefsDraft() }
    private var availableModels: [AgentModel] { activeAgent?.models?.availableModels ?? [] }
    private var availableModes: [AgentOption] { activeAgent?.modes?.availableModes ?? [] }

    /// 思考等级挂在具体的“Agent + 模型”上，所以先把当前实际生效的模型定下来：
    /// 用户选过就用它，否则用 Agent 当前模型，最后退到第一个可用模型。
    private var effectiveModelID: String {
        let agentModelID = activeAgent?.models?.currentModelId ?? ""
        if availableModels.contains(where: { $0.modelId == draft.defaultModelID }) {
            return draft.defaultModelID
        }
        if availableModels.contains(where: { $0.modelId == agentModelID }) {
            return agentModelID
        }
        return availableModels.first?.modelId ?? ""
    }

    private var availableThoughtLevels: [AgentOption] {
        guard let agent = activeAgent else { return [] }
        return thoughtLevels
            .state(agentType: agent.type, modelID: effectiveModelID)?
            .availableThoughtLevels ?? []
    }

    var body: some View {
        Group {
            if isLoading && agents.isEmpty {
                AgentSettingsLoadingView(title: "正在加载 Agent 默认参数…")
            } else if !loadError.isEmpty {
                AgentSettingsLoadFailure(detail: loadError) { Task { await load() } }
            } else if agents.isEmpty {
                emptyState
            } else {
                editor
            }
        }
        .background(QuartetTheme.canvas)
        .task { await initialLoad() }
        .onChange(of: activeAgentID) { _, _ in
            message = nil
            refreshThoughtLevels()
        }
        .onChange(of: effectiveModelID) { _, _ in refreshThoughtLevels() }
        .onChange(of: availableThoughtLevels) { _, levels in dropUnavailableThoughtLevel(levels) }
        .sheet(isPresented: $showsAgentPicker) {
            QuartetChoiceSheet(
                title: "选择 Agent",
                choices: agents.map { agent in
                    .agent(
                        id: agent.agentId,
                        title: agent.displayName.isEmpty ? agent.agentId : agent.displayName,
                        command: agent.agentId,
                        usage: agentUsageSummaries.summary(agent: agent),
                        retry: { Task { await loadAgentUsageSummaries() } }
                    )
                },
                selection: $activeAgentID,
                accessibilityPrefix: "agent-defaults-agent"
            )
            .presentationDetents([.medium, .large])
            .quartetSheetStyle()
            .task { await loadAgentUsageSummaries() }
        }
        .sheet(isPresented: $showsFavoritePicker) {
            AgentFavoriteModelsSheet(
                models: availableModels,
                selection: Binding(
                    get: { draft.favoriteModelIDs },
                    set: { newValue in applyDraft { $0.favoriteModelIDs = newValue } }
                )
            )
            .presentationDetents([.medium, .large])
            .quartetSheetStyle()
        }
        .sheet(item: $picker) { target in
            pickerSheet(target)
        }
    }

    private var emptyState: some View {
        ScrollView {
            AgentSettingsCard("Agent 默认参数", systemImage: "slider.horizontal.3") {
                agentSettingsHint("当前没有可用的 Agent。先在“Agent 目录”里安装并通过可用性检查。")
            }
            .padding(18)
        }
    }

    @ViewBuilder
    private func pickerSheet(_ target: AgentDefaultsPicker) -> some View {
        switch target {
        case .model:
            QuartetChoiceSheet(
                title: target.title,
                choices: [QuartetChoice(id: "", title: "未设置")]
                    + availableModels.map { QuartetChoice(id: $0.modelId, title: $0.name, detail: $0.description) },
                selection: Binding(
                    get: { draft.defaultModelID },
                    set: { newValue in
                        applyDraft { current in
                            current.defaultModelID = newValue
                            current.defaultThoughtLevel = ""
                        }
                    }
                ),
                accessibilityPrefix: "agent-defaults-model",
                favoriteIDs: Set(draft.favoriteModelIDs)
            )
            .presentationDetents([.medium, .large])
            .quartetSheetStyle()
        case .mode:
            QuartetChoiceSheet(
                title: target.title,
                choices: [QuartetChoice(id: "", title: "未设置")]
                    + availableModes.map { QuartetChoice(id: $0.id, title: $0.name, detail: $0.description) },
                selection: Binding(
                    get: { draft.defaultMode },
                    set: { newValue in applyDraft { $0.defaultMode = newValue } }
                ),
                accessibilityPrefix: "agent-defaults-mode"
            )
            .presentationDetents([.medium, .large])
            .quartetSheetStyle()
        case .thoughtLevel:
            QuartetChoiceSheet(
                title: target.title,
                choices: [QuartetChoice(id: "", title: "未设置")]
                    + availableThoughtLevels.map { QuartetChoice(id: $0.id, title: $0.name, detail: $0.description) },
                selection: Binding(
                    get: { draft.defaultThoughtLevel },
                    set: { newValue in applyDraft { $0.defaultThoughtLevel = newValue } }
                ),
                accessibilityPrefix: "agent-defaults-thought-level"
            )
            .presentationDetents([.medium, .large])
            .quartetSheetStyle()
        }
    }

    private var editor: some View {
        ScrollView {
            LazyVStack(spacing: 12) {
                agentCard
                favoritesCard
                defaultsCard
            }
            .padding(.horizontal, 18)
            .padding(.vertical, 12)
        }
        .safeAreaInset(edge: .bottom, spacing: 0) {
            if canWrite {
                AgentSettingsSaveBar(
                    title: "保存默认参数",
                    savingTitle: "正在保存…",
                    isSaving: isSaving,
                    isEnabled: !dirtyAgentIDs.isEmpty,
                    message: message,
                    identifier: "agent-defaults-save",
                    action: { save() }
                )
            }
        }
    }

    private var agentCard: some View {
        AgentSettingsCard("Agent 默认参数", systemImage: "slider.horizontal.3") {
            agentSettingsHint("新建对话选中这个 Agent 时会套用这里的默认值；保存的值如果已经不在实时列表里，运行时会自动回退。")
            AgentSettingsSelectionRow(
                title: "Agent",
                value: activeAgent.map { $0.displayName.isEmpty ? $0.agentId : $0.displayName } ?? "请选择",
                placeholder: activeAgent == nil,
                identifier: "agent-defaults-agent-picker"
            ) { showsAgentPicker = true }
            if !dirtyAgentIDs.isEmpty {
                agentSettingsHint(AppLanguage.localizedFormat("有 %d 个 Agent 的改动尚未保存。", dirtyAgentIDs.count))
            }
            if !canWrite {
                agentSettingsHint("当前账号没有 config.write 权限，只能查看默认参数。")
            }
        }
    }

    private var favoritesCard: some View {
        AgentSettingsCard("收藏模型", systemImage: "star") {
            agentSettingsHint("收藏的模型会排在模型选择器最前面。")
            if availableModels.isEmpty {
                agentSettingsHint("这个 Agent 没有上报可用模型。")
            } else {
                if draft.favoriteModelIDs.isEmpty {
                    agentSettingsHint("还没有收藏任何模型。")
                } else {
                    favoriteList
                }
                if canWrite {
                    Button {
                        showsFavoritePicker = true
                    } label: {
                        Label("管理收藏模型", systemImage: "star.fill")
                            .font(.quartet(.control, weight: .semibold))
                            .foregroundStyle(QuartetTheme.accent)
                            .frame(maxWidth: .infinity)
                            .frame(height: 46)
                            .background(QuartetTheme.accent.opacity(0.1), in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                    }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("agent-defaults-manage-favorites")
                }
            }
        }
    }

    private var favoriteList: some View {
        VStack(spacing: 0) {
            ForEach(Array(draft.favoriteModelIDs.enumerated()), id: \.element) { index, modelID in
                if index > 0 {
                    Divider().overlay(QuartetTheme.divider)
                }
                HStack(spacing: 10) {
                    Image(systemName: "star.fill")
                        .font(.quartet(.detail, weight: .semibold))
                        .foregroundStyle(QuartetTheme.accent)
                        .accessibilityHidden(true)
                    Text(modelName(modelID))
                        .font(.quartet(.control, weight: .medium))
                        .foregroundStyle(QuartetTheme.primaryText)
                    Spacer(minLength: 8)
                    if canWrite {
                        Button {
                            applyDraft { $0.favoriteModelIDs.removeAll { $0 == modelID } }
                        } label: {
                            Image(systemName: "minus.circle")
                                .font(.quartet(.control, weight: .semibold))
                                .foregroundStyle(QuartetTheme.failed)
                                .frame(width: 40, height: 40)
                        }
                        .buttonStyle(.plain)
                        .accessibilityLabel(AppLanguage.localizedFormat("取消收藏 %@", modelName(modelID)))
                    }
                }
                .frame(minHeight: 48)
            }
        }
        .padding(.horizontal, 12)
        .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
    }

    private var defaultsCard: some View {
        AgentSettingsCard("默认选择", systemImage: "checklist") {
            if availableModels.isEmpty && availableModes.count <= 1 && availableThoughtLevels.count <= 1 {
                agentSettingsHint("这个 Agent 没有可设置的默认项。")
            }
            if !availableModels.isEmpty {
                AgentSettingsSelectionRow(
                    title: "默认模型",
                    value: draft.defaultModelID.isEmpty ? "未设置" : modelName(draft.defaultModelID),
                    placeholder: draft.defaultModelID.isEmpty,
                    identifier: "agent-defaults-model-picker"
                ) { picker = .model }
            }
            if availableModes.count > 1 {
                AgentSettingsSelectionRow(
                    title: "默认模式",
                    value: draft.defaultMode.isEmpty ? "未设置" : optionName(draft.defaultMode, in: availableModes),
                    placeholder: draft.defaultMode.isEmpty,
                    identifier: "agent-defaults-mode-picker"
                ) { picker = .mode }
            }
            if availableThoughtLevels.count > 1 {
                AgentSettingsSelectionRow(
                    title: "默认思考等级",
                    value: draft.defaultThoughtLevel.isEmpty
                        ? "未设置"
                        : optionName(draft.defaultThoughtLevel, in: availableThoughtLevels),
                    placeholder: draft.defaultThoughtLevel.isEmpty,
                    identifier: "agent-defaults-thought-level-picker"
                ) { picker = .thoughtLevel }
            }
            thoughtLevelStatus
        }
    }

    @ViewBuilder
    private var thoughtLevelStatus: some View {
        if let agent = activeAgent, !effectiveModelID.isEmpty {
            if thoughtLevels.isLoading(agentType: agent.type, modelID: effectiveModelID) {
                HStack(spacing: 6) {
                    ProgressView().controlSize(.small).tint(QuartetTheme.secondaryText)
                    Text("正在读取该模型的思考等级…")
                        .font(.quartet(.detail))
                        .foregroundStyle(QuartetTheme.secondaryText)
                }
            }
            if let detail = thoughtLevels.error(agentType: agent.type, modelID: effectiveModelID) {
                AgentSettingsMessageView(kind: .failure, text: detail)
            }
        }
    }

    // MARK: 草稿

    /// 只有真正改了值才标记为待保存，避免打开选择器就把所有 Agent 都标脏。
    private func applyDraft(_ transform: (inout AgentPrefsDraft) -> Void) {
        guard !activeAgentID.isEmpty else { return }
        var current = drafts[activeAgentID] ?? AgentPrefsDraft()
        let before = current
        transform(&current)
        guard current != before else { return }
        drafts[activeAgentID] = current
        dirtyAgentIDs.insert(activeAgentID)
        message = nil
    }

    private func modelName(_ modelID: String) -> String {
        availableModels.first { $0.modelId == modelID }?.name ?? modelID
    }

    private func optionName(_ optionID: String, in options: [AgentOption]) -> String {
        options.first { $0.id == optionID }?.name ?? optionID
    }

    private func refreshThoughtLevels() {
        guard let agent = activeAgent, !effectiveModelID.isEmpty else { return }
        thoughtLevels.load(
            agentType: agent.type,
            modelID: effectiveModelID,
            fallback: effectiveModelID == agent.models?.currentModelId ? agent.thoughtLevels : nil,
            using: model
        )
    }

    /// 换模型后原来的思考等级可能已经不存在，直接清掉并标脏，与 Web 端一致。
    private func dropUnavailableThoughtLevel(_ levels: [AgentOption]) {
        guard !draft.defaultThoughtLevel.isEmpty, !levels.isEmpty else { return }
        guard !levels.contains(where: { $0.id == draft.defaultThoughtLevel }) else { return }
        applyDraft { $0.defaultThoughtLevel = "" }
    }

    // MARK: 数据

    private func initialLoad() async {
        guard agents.isEmpty else { return }
        await load()
    }

    /// 打开“选择 Agent”弹窗时读取每个 Agent 的版本号与用量：先出缓存，再后台刷新。
    /// 失败不占用节流窗口，所以行内“重试”按钮直接再调一次。
    private func loadAgentUsageSummaries() async {
        await agentUsageSummaries.load(agents: agents, model: model)
    }

    private func load() async {
        isLoading = true
        loadError = ""
        message = nil
        do {
            async let agentRequest = model.agentCatalog()
            async let settingsRequest = model.agentPreferences()
            let (agentList, saved) = try await (agentRequest, settingsRequest)
            let usable = agentList.filter(\.available)
            agents = usable
            drafts = Dictionary(
                usable.map { ($0.agentId, AgentPrefsDraft(saved[$0.agentId] ?? saved[$0.type])) },
                uniquingKeysWith: { first, _ in first }
            )
            dirtyAgentIDs = []
            if activeAgentID.isEmpty || !usable.contains(where: { $0.agentId == activeAgentID }) {
                activeAgentID = usable.first?.agentId ?? ""
            }
            refreshThoughtLevels()
        } catch {
            agents = []
            drafts = [:]
            dirtyAgentIDs = []
            activeAgentID = ""
            loadError = agentSettingsErrorDetail(error)
        }
        isLoading = false
    }

    private func save() {
        guard !dirtyAgentIDs.isEmpty else { return }
        isSaving = true
        message = nil
        let pending = agents
            .filter { dirtyAgentIDs.contains($0.agentId) }
            .map { ($0.agentId, $0.displayName.isEmpty ? $0.agentId : $0.displayName, (drafts[$0.agentId] ?? AgentPrefsDraft()).payload) }
        Task { @MainActor in
            var failures: [String] = []
            var saved: Set<String> = []
            for (agentID, displayName, payload) in pending {
                do {
                    let client = try model.apiClient()
                    _ = try await client.saveAgentPrefs(agentID: agentID, prefs: payload)
                    saved.insert(agentID)
                } catch {
                    failures.append("\(displayName)：\(agentSettingsErrorDetail(error))")
                }
            }
            dirtyAgentIDs.subtract(saved)
            message = failures.isEmpty
                ? .success("已保存".localizedForApp)
                : .failure(failures.joined(separator: "\n"))
            isSaving = false
        }
    }
}

// MARK: - 收藏模型弹窗

private struct AgentFavoriteModelsSheet: View {
    let models: [AgentModel]
    @Binding var selection: [String]

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(spacing: 0) {
                    ForEach(Array(models.enumerated()), id: \.element.id) { index, item in
                        if index > 0 {
                            Divider().overlay(QuartetTheme.divider).padding(.leading, 54)
                        }
                        row(item)
                    }
                }
                .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
                .overlay(RoundedRectangle(cornerRadius: 18, style: .continuous).stroke(QuartetTheme.divider.opacity(0.8)))
                .padding(.horizontal, 20)
                .padding(.top, 8)
                .padding(.bottom, 24)
            }
            .background(QuartetTheme.canvas)
            .quartetNavigationTitle("管理收藏模型")
        }
    }

    private func row(_ item: AgentModel) -> some View {
        let selected = selection.contains(item.modelId)
        return Button {
            if selected {
                selection = selection.filter { $0 != item.modelId }
            } else {
                selection = selection + [item.modelId]
            }
        } label: {
            HStack(spacing: 12) {
                Image(systemName: selected ? "star.fill" : "star")
                    .font(.quartet(.regular, weight: .semibold))
                    .foregroundStyle(selected ? QuartetTheme.accent : QuartetTheme.secondaryText)
                    .frame(width: 28)
                    .accessibilityHidden(true)
                VStack(alignment: .leading, spacing: 3) {
                    Text(item.name)
                        .font(.quartet(.control, weight: .semibold))
                        .foregroundStyle(QuartetTheme.primaryText)
                        .frame(maxWidth: .infinity, alignment: .leading)
                    if let detail = item.description, !detail.isEmpty {
                        Text(detail)
                            .font(.quartet(.detail))
                            .foregroundStyle(QuartetTheme.secondaryText)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                }
                Spacer(minLength: 8)
            }
            .padding(.horizontal, 14)
            .frame(minHeight: 60)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(selected ? .isSelected : [])
        .accessibilityIdentifier("agent-defaults-favorite-\(item.modelId)")
    }
}
