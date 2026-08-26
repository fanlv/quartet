import SwiftUI

/// 三个角色。标题生成与群回复走 `bin -p` 无会话路径，只能选支持该能力的 Agent，
/// 也没有会话模式；IM 会话角色创建的是真实交互 Job，所以多一个模式选择。
private enum AgentRole: String, CaseIterable, Identifiable {
    case title
    case groupReply
    case imSession

    var id: String { rawValue }

    var title: String {
        switch self {
        case .title: "标题生成 Agent"
        case .groupReply: "群聊回复 Agent"
        case .imSession: "IM 会话 Agent"
        }
    }

    var detail: String {
        switch self {
        case .title: "为新对话生成标题，走一次性无会话执行。"
        case .groupReply: "群聊里被 @ 时回复，走一次性无会话执行。"
        case .imSession: "IM 私聊创建或续接交互式 Job 时使用。"
        }
    }

    var icon: String {
        switch self {
        case .title: "text.badge.plus"
        case .groupReply: "bubble.left.and.bubble.right"
        case .imSession: "person.crop.circle.badge.checkmark"
        }
    }

    var supportsMode: Bool { self == .imSession }
    /// 只有支持 `bin -p` 的 Agent 能承担无会话角色。
    var requiresHeadlessPrint: Bool { self != .imSession }
}

private enum AgentRolePickerTarget: Identifiable {
    case agent(AgentRole)
    case model(AgentRole)
    case mode(AgentRole)
    case thoughtLevel(AgentRole)

    var id: String {
        switch self {
        case .agent(let role): "agent-\(role.rawValue)"
        case .model(let role): "model-\(role.rawValue)"
        case .mode(let role): "mode-\(role.rawValue)"
        case .thoughtLevel(let role): "thought-level-\(role.rawValue)"
        }
    }

    var role: AgentRole {
        switch self {
        case .agent(let role), .model(let role), .mode(let role), .thoughtLevel(let role): role
        }
    }
}

@MainActor
struct AgentRoleSettingsView: View {
    @EnvironmentObject private var model: AppModel
    @StateObject private var thoughtLevels = AgentThoughtLevelStore()

    @State private var agents: [AgentSummary] = []
    @State private var headlessAgentIDs: Set<String> = []
    @State private var configs: [String: AgentRoleConfig] = [:]
    @State private var migrationErrors: [String] = []
    @State private var isLoading = true
    @State private var loadError = ""
    @State private var isSaving = false
    @State private var message: AgentSettingsMessage?
    @State private var picker: AgentRolePickerTarget?

    private var canWrite: Bool { model.can("config.write") }

    var body: some View {
        Group {
            if isLoading {
                AgentSettingsLoadingView(title: "正在加载 Agent 角色…")
            } else if !loadError.isEmpty && agents.isEmpty {
                AgentSettingsLoadFailure(detail: loadError) { Task { await load() } }
            } else {
                editor
            }
        }
        .background(QuartetTheme.canvas)
        .task { await initialLoad() }
        .sheet(item: $picker) { target in
            pickerSheet(target)
        }
    }

    private var editor: some View {
        ScrollView {
            LazyVStack(spacing: 12) {
                AgentSettingsCard("Agent 角色", systemImage: "person.2.badge.gearshape") {
                    agentSettingsHint("这三个角色由后端自动触发，和手动新建对话时的选择互不影响。")
                    if !canWrite {
                        agentSettingsHint("当前账号没有 config.write 权限，只能查看角色配置。")
                    }
                }
                if !loadError.isEmpty {
                    AgentSettingsMessageView(kind: .failure, text: loadError)
                }
                ForEach(migrationErrors, id: \.self) { detail in
                    AgentSettingsMessageView(kind: .failure, text: detail)
                }
                ForEach(AgentRole.allCases) { role in
                    roleCard(role)
                }
                if let message {
                    AgentSettingsMessageView(message)
                }
            }
            .padding(.horizontal, 18)
            .padding(.vertical, 12)
        }
        .safeAreaInset(edge: .bottom, spacing: 0) {
            if canWrite {
                AgentSettingsSaveBar(
                    title: "保存角色配置",
                    savingTitle: "正在保存…",
                    isSaving: isSaving,
                    isEnabled: true,
                    identifier: "agent-role-save",
                    action: { save() }
                )
            }
        }
    }

    private func roleCard(_ role: AgentRole) -> some View {
        let config = configs[role.rawValue] ?? AgentRoleConfig()
        let pool = agentPool(for: role)
        let selected = pool.first { $0.agentId == config.agentId }
        return AgentSettingsCard(role.title, systemImage: role.icon) {
            agentSettingsHint(role.detail)
            AgentSettingsSelectionRow(
                title: "Agent",
                value: agentDisplayName(config.agentId, pool: pool),
                placeholder: config.agentId.isEmpty,
                identifier: "agent-role-\(role.rawValue)-agent"
            ) { picker = .agent(role) }
            if pool.isEmpty {
                agentSettingsHint(role.requiresHeadlessPrint
                    ? "当前没有支持 bin -p 单次执行的可用 Agent。"
                    : "当前没有可用的 Agent。")
            }
            if let selected, !(selected.models?.availableModels ?? []).isEmpty {
                AgentSettingsSelectionRow(
                    title: "模型",
                    value: modelName(effectiveModelID(role), agent: selected),
                    placeholder: effectiveModelID(role).isEmpty,
                    identifier: "agent-role-\(role.rawValue)-model"
                ) { picker = .model(role) }
            }
            if role.supportsMode, let selected, (selected.modes?.availableModes ?? []).count > 0 {
                AgentSettingsSelectionRow(
                    title: "模式",
                    value: config.acpMode.isEmpty
                        ? "跟随默认"
                        : optionName(config.acpMode, in: selected.modes?.availableModes ?? []),
                    placeholder: config.acpMode.isEmpty,
                    identifier: "agent-role-\(role.rawValue)-mode"
                ) { picker = .mode(role) }
            }
            if selected != nil, !thoughtLevelOptions(role).isEmpty {
                AgentSettingsSelectionRow(
                    title: "思考等级",
                    value: config.acpThoughtLevel.isEmpty
                        ? "跟随默认"
                        : optionName(config.acpThoughtLevel, in: thoughtLevelOptions(role)),
                    placeholder: config.acpThoughtLevel.isEmpty,
                    identifier: "agent-role-\(role.rawValue)-thought-level"
                ) { picker = .thoughtLevel(role) }
            }
            thoughtLevelStatus(role)
        }
    }

    @ViewBuilder
    private func thoughtLevelStatus(_ role: AgentRole) -> some View {
        let config = configs[role.rawValue] ?? AgentRoleConfig()
        if let agent = agentPool(for: role).first(where: { $0.agentId == config.agentId }) {
            let modelID = effectiveModelID(role)
            if !modelID.isEmpty {
                if thoughtLevels.isLoading(agentType: agent.type, modelID: modelID) {
                    HStack(spacing: 6) {
                        ProgressView().controlSize(.small).tint(QuartetTheme.secondaryText)
                        Text("正在读取该模型的思考等级…")
                            .font(.quartet(.detail))
                            .foregroundStyle(QuartetTheme.secondaryText)
                    }
                }
                if let detail = thoughtLevels.error(agentType: agent.type, modelID: modelID) {
                    AgentSettingsMessageView(kind: .failure, text: detail)
                }
            }
        }
    }

    @ViewBuilder
    private func pickerSheet(_ target: AgentRolePickerTarget) -> some View {
        let role = target.role
        let config = configs[role.rawValue] ?? AgentRoleConfig()
        let pool = agentPool(for: role)
        let selected = pool.first { $0.agentId == config.agentId }
        switch target {
        case .agent:
            QuartetChoiceSheet(
                title: "选择 Agent",
                choices: agentChoices(role),
                selection: Binding(
                    get: { config.agentId },
                    set: { newValue in selectAgent(role, agentId: newValue) }
                ),
                accessibilityPrefix: "agent-role-\(role.rawValue)-agent-option"
            )
            .presentationDetents([.medium, .large])
            .quartetSheetStyle()
        case .model:
            QuartetChoiceSheet(
                title: "选择模型",
                choices: (selected?.models?.availableModels ?? []).map {
                    QuartetChoice(id: $0.modelId, title: $0.name, detail: $0.description)
                },
                selection: Binding(
                    get: { effectiveModelID(role) },
                    set: { newValue in
                        updateConfig(role) { current in
                            current.modelId = newValue
                            current.acpThoughtLevel = ""
                        }
                    }
                ),
                accessibilityPrefix: "agent-role-\(role.rawValue)-model-option"
            )
            .presentationDetents([.medium, .large])
            .quartetSheetStyle()
        case .mode:
            QuartetChoiceSheet(
                title: "选择模式",
                choices: [QuartetChoice(id: "", title: "跟随默认")]
                    + (selected?.modes?.availableModes ?? []).map {
                        QuartetChoice(id: $0.id, title: $0.name, detail: $0.description)
                    },
                selection: Binding(
                    get: { config.acpMode },
                    set: { newValue in updateConfig(role) { $0.acpMode = newValue } }
                ),
                accessibilityPrefix: "agent-role-\(role.rawValue)-mode-option"
            )
            .presentationDetents([.medium, .large])
            .quartetSheetStyle()
        case .thoughtLevel:
            QuartetChoiceSheet(
                title: "选择思考等级",
                choices: [QuartetChoice(id: "", title: "跟随默认")]
                    + thoughtLevelOptions(role).map {
                        QuartetChoice(id: $0.id, title: $0.name, detail: $0.description)
                    },
                selection: Binding(
                    get: { config.acpThoughtLevel },
                    set: { newValue in updateConfig(role) { $0.acpThoughtLevel = newValue } }
                ),
                accessibilityPrefix: "agent-role-\(role.rawValue)-thought-level-option"
            )
            .presentationDetents([.medium, .large])
            .quartetSheetStyle()
        }
    }

    // MARK: 选项

    private func agentPool(for role: AgentRole) -> [AgentSummary] {
        guard role.requiresHeadlessPrint else { return agents }
        return agents.filter { headlessAgentIDs.contains($0.agentId) }
    }

    /// 保存过但当前不可用的 Agent 仍然列出来，避免用户看不到已生效的配置。
    private func agentChoices(_ role: AgentRole) -> [QuartetChoice] {
        let config = configs[role.rawValue] ?? AgentRoleConfig()
        let pool = agentPool(for: role)
        var choices = [QuartetChoice(id: "", title: "未设置")]
        if !config.agentId.isEmpty, !pool.contains(where: { $0.agentId == config.agentId }) {
            choices.append(QuartetChoice(id: config.agentId, title: config.agentId, detail: "当前不可用"))
        }
        choices.append(contentsOf: pool.map {
            QuartetChoice(
                id: $0.agentId,
                title: $0.displayName.isEmpty ? $0.agentId : $0.displayName,
                detail: $0.agentId
            )
        })
        return choices
    }

    private func agentDisplayName(_ agentId: String, pool: [AgentSummary]) -> String {
        guard !agentId.isEmpty else { return "未设置".localizedForApp }
        guard let agent = pool.first(where: { $0.agentId == agentId }) else { return agentId }
        return agent.displayName.isEmpty ? agent.agentId : agent.displayName
    }

    private func effectiveModelID(_ role: AgentRole) -> String {
        let config = configs[role.rawValue] ?? AgentRoleConfig()
        if !config.modelId.isEmpty { return config.modelId }
        let pool = agentPool(for: role)
        return pool.first { $0.agentId == config.agentId }?.models?.currentModelId ?? ""
    }

    private func modelName(_ modelID: String, agent: AgentSummary) -> String {
        guard !modelID.isEmpty else { return "未设置".localizedForApp }
        return agent.models?.availableModels.first { $0.modelId == modelID }?.name ?? modelID
    }

    private func optionName(_ optionID: String, in options: [AgentOption]) -> String {
        options.first { $0.id == optionID }?.name ?? optionID
    }

    private func thoughtLevelOptions(_ role: AgentRole) -> [AgentOption] {
        let config = configs[role.rawValue] ?? AgentRoleConfig()
        guard let agent = agentPool(for: role).first(where: { $0.agentId == config.agentId }) else { return [] }
        let modelID = effectiveModelID(role)
        guard !modelID.isEmpty else { return [] }
        return thoughtLevels.state(agentType: agent.type, modelID: modelID)?.availableThoughtLevels ?? []
    }

    // MARK: 编辑

    private func updateConfig(_ role: AgentRole, _ transform: (inout AgentRoleConfig) -> Void) {
        var current = configs[role.rawValue] ?? AgentRoleConfig()
        transform(&current)
        configs[role.rawValue] = current
        refreshThoughtLevels(role)
    }

    /// 换 Agent 时按新 Agent 的当前值重置模型 / 模式 / 思考等级，避免留下不匹配的组合。
    private func selectAgent(_ role: AgentRole, agentId: String) {
        let pool = agentPool(for: role)
        guard let agent = pool.first(where: { $0.agentId == agentId }) else {
            configs[role.rawValue] = AgentRoleConfig(agentId: agentId)
            return
        }
        configs[role.rawValue] = AgentRoleConfig(
            agentId: agentId,
            modelId: agent.models?.currentModelId ?? agent.modelId,
            acpMode: role.supportsMode ? (agent.modes?.currentModeId ?? "") : "",
            acpThoughtLevel: agent.thoughtLevels?.currentThoughtLevelId ?? ""
        )
        refreshThoughtLevels(role)
    }

    private func refreshThoughtLevels(_ role: AgentRole) {
        let config = configs[role.rawValue] ?? AgentRoleConfig()
        guard let agent = agentPool(for: role).first(where: { $0.agentId == config.agentId }) else { return }
        let modelID = effectiveModelID(role)
        guard !modelID.isEmpty else { return }
        thoughtLevels.load(
            agentType: agent.type,
            modelID: modelID,
            fallback: modelID == agent.models?.currentModelId ? agent.thoughtLevels : nil,
            using: model
        )
    }

    // MARK: 数据

    private func initialLoad() async {
        guard agents.isEmpty else { return }
        await load()
    }

    private func load() async {
        isLoading = true
        loadError = ""
        message = nil
        do {
            if model.isRunningUITests {
                async let agentRequest = model.agentCatalog()
                async let catalogRequest = model.managedAgentCatalogItems()
                let (agentList, catalogItems) = try await (agentRequest, catalogRequest)
                agents = agentList.filter(\.available)
                headlessAgentIDs = Set(catalogItems.filter(\.supportsHeadlessPrint).map(\.agentId))
                let fixture = AgentRoleConfig(
                    agentId: agents.first?.agentId ?? "",
                    modelId: agents.first?.models?.currentModelId ?? "",
                    acpMode: "default",
                    acpThoughtLevel: "medium"
                )
                configs = Dictionary(uniqueKeysWithValues: AgentRole.allCases.map { ($0.rawValue, fixture) })
                migrationErrors = []
            } else {
                let client = try model.apiClient()
                async let agentRequest = client.agents()
                async let catalogRequest = client.agentCatalogItems()
                async let titleRequest = client.titleGenerationAgent()
                async let groupRequest = client.groupReplyAgent()
                async let imRequest = client.imSessionAgent()
                let (agentResponse, catalogResponse, titleResponse, groupResponse, imResponse) =
                    try await (agentRequest, catalogRequest, titleRequest, groupRequest, imRequest)

                agents = agentResponse.agentList.filter(\.available)
                headlessAgentIDs = Set(
                    (catalogResponse.agents ?? [])
                        .filter(\.supportsHeadlessPrint)
                        .map(\.agentId)
                )
                configs = [
                    AgentRole.title.rawValue: titleResponse.config ?? AgentRoleConfig(),
                    AgentRole.groupReply.rawValue: groupResponse.config ?? AgentRoleConfig(),
                    AgentRole.imSession.rawValue: imResponse.config ?? AgentRoleConfig(),
                ]
                migrationErrors = titleResponse.migrationErrors ?? []
            }
            for role in AgentRole.allCases { refreshThoughtLevels(role) }
        } catch {
            loadError = agentSettingsErrorDetail(error)
        }
        isLoading = false
    }

    private func save() {
        isSaving = true
        message = nil
        let titleConfig = configs[AgentRole.title.rawValue] ?? AgentRoleConfig()
        let groupConfig = configs[AgentRole.groupReply.rawValue] ?? AgentRoleConfig()
        let imConfig = configs[AgentRole.imSession.rawValue] ?? AgentRoleConfig()
        Task { @MainActor in
            var failures: [String] = []
            do {
                _ = try await model.apiClient().saveTitleGenerationAgent(titleConfig)
            } catch {
                failures.append("\(AgentRole.title.title.localizedForApp)：\(agentSettingsErrorDetail(error))")
            }
            do {
                _ = try await model.apiClient().saveGroupReplyAgent(groupConfig)
            } catch {
                failures.append("\(AgentRole.groupReply.title.localizedForApp)：\(agentSettingsErrorDetail(error))")
            }
            do {
                _ = try await model.apiClient().saveIMSessionAgent(imConfig)
            } catch {
                failures.append("\(AgentRole.imSession.title.localizedForApp)：\(agentSettingsErrorDetail(error))")
            }
            if failures.isEmpty {
                migrationErrors = []
                message = .success("已保存".localizedForApp)
            } else {
                message = .failure(failures.joined(separator: "\n"))
            }
            isSaving = false
        }
    }
}
