import SwiftUI
import UIKit

/// 技能作用域。全局技能对所有运行生效；项目技能装在某个工作区目录下，
/// 只有以该目录为 cwd 启动的 Agent 才加载得到，所以项目作用域必须绑定工作区。
private enum SkillScope: Equatable {
    case global
    case workspace(String)

    static let workspacePrefix = "workspace:"

    var key: String {
        switch self {
        case .global: "global"
        case .workspace(let id): Self.workspacePrefix + id
        }
    }

    init(key: String) {
        if key.hasPrefix(Self.workspacePrefix) {
            self = .workspace(String(key.dropFirst(Self.workspacePrefix.count)))
        } else {
            self = .global
        }
    }

    var isGlobal: Bool {
        if case .global = self { return true }
        return false
    }

    var workspaceID: String {
        if case .workspace(let id) = self { return id }
        return ""
    }
}

/// 安装弹窗里可选的 Agent 目标。`slug` 是 `skills add -a` 接受的值，`label` 与
/// 技能列表里回报的展示名一致；`preselected` 是本项目默认安装的那几个。
private struct SkillAgentTarget {
    let slug: String
    let label: String
    let preselected: Bool

    init(_ slug: String, _ label: String, preselected: Bool = false) {
        self.slug = slug
        self.label = label
        self.preselected = preselected
    }
}

/// 需要弹窗承载的三件事：切作用域、安装技能、查看命令输出。
private enum SkillSheet: String, Identifiable {
    case scope
    case install
    case output

    var id: String { rawValue }
}

/// 与 Web 端“技能列表”设置一致：按作用域查看已安装技能，安装、卸载、整体更新，
/// 以及一键安装 quartet-cli 与项目自带技能。
@MainActor
struct SkillSettingsView: View {
    private static let agentTargets: [SkillAgentTarget] = [
        SkillAgentTarget("codex", "Codex", preselected: true),
        SkillAgentTarget("claude-code", "Claude Code", preselected: true),
        SkillAgentTarget("trae-cn", "Trae CN", preselected: true),
        SkillAgentTarget("opencode", "OpenCode", preselected: true),
        SkillAgentTarget("cursor", "Cursor"),
        SkillAgentTarget("gemini-cli", "Gemini CLI"),
        SkillAgentTarget("kimi-code-cli", "Kimi Code CLI"),
        SkillAgentTarget("antigravity-cli", "Antigravity CLI"),
        SkillAgentTarget("trae", "Trae")
    ]

    /// `skills add --all` 会装到 CLI 认识的全部 Agent（50+），标签全铺开会把卡片
    /// 撑爆，默认只展示前几个。
    private static let agentTagPreview = 6
    /// 后端首次 `skills ls` 还没跑完时会回 ready=false，这里的轮询上限比后端自身
    /// 的命令超时更长，只作为兜底。
    private static let readyPollAttempts = 180
    private static let readyPollDelay = Duration.milliseconds(500)

    @EnvironmentObject private var model: AppModel

    @State private var scopeKey = SkillScope.global.key
    @State private var skills: [SkillInfo] = []
    @State private var isLoading = true
    @State private var loadError = ""
    @State private var filterText = ""
    @State private var message: AgentSettingsMessage?
    @State private var sheet: SkillSheet?
    @State private var loadSequence = 0

    @State private var isMutating = false
    @State private var pendingRemoval: SkillInfo?
    @State private var confirmsUpdateAll = false
    @State private var confirmsProjectTools = false

    @State private var commandOutput = ""
    @State private var commandOutputTitle = ""

    @State private var installPackage = ""
    @State private var installAgents: [String] = Self.agentTargets.filter(\.preselected).map(\.slug)
    @State private var searchQuery = ""
    @State private var searchResults: [SkillFindResult] = []
    @State private var isSearching = false

    private var canManage: Bool { model.can("skills.manage") }
    private var activeScope: SkillScope { SkillScope(key: scopeKey) }

    var body: some View {
        ScrollView {
            LazyVStack(spacing: 12) {
                scopeCard
                actionsCard
                if !loadError.isEmpty {
                    AgentSettingsCard("读取失败", systemImage: "exclamationmark.triangle") {
                        AgentSettingsMessageView(kind: .failure, text: loadError)
                        HStack(spacing: 10) {
                            secondaryButton("复制错误", systemImage: "doc.on.doc", identifier: "skill-copy-error") {
                                UIPasteboard.general.string = loadError
                            }
                            secondaryButton("重试", systemImage: "arrow.clockwise", identifier: "skill-retry") {
                                Task { await load() }
                            }
                        }
                    }
                }
                if isLoading {
                    AgentSettingsCard {
                        agentSettingsHint("正在读取技能列表…")
                    }
                } else {
                    skillList
                }
            }
            .padding(.horizontal, 18)
            .padding(.vertical, 12)
        }
        .scrollDismissesKeyboard(.interactively)
        .background(QuartetTheme.canvas)
        .task { await load() }
        .onChange(of: scopeKey) { _, _ in
            message = nil
            Task { await load() }
        }
        // 首页还没拉到工作空间列表时进设置页，列表到达后补上作用域选项。
        .onChange(of: model.workspaces.map(\.id)) { _, _ in
            guard case .workspace(let id) = activeScope,
                  !model.workspaces.contains(where: { $0.id == id }) else { return }
            scopeKey = SkillScope.global.key
        }
        .safeAreaInset(edge: .bottom, spacing: 0) {
            if let message {
                AgentSettingsMessageView(message)
                    .padding(.horizontal, 18)
                    .padding(.vertical, 10)
                    .background(.ultraThinMaterial)
                    .accessibilityIdentifier("skill-feedback")
            }
        }
        .sheet(item: $sheet) { sheet in
            sheetContent(sheet)
        }
        .alert("卸载这个技能？", isPresented: removalAlertBinding) {
            Button("关闭", role: .cancel) { pendingRemoval = nil }
            Button("卸载", role: .destructive) {
                guard let skill = pendingRemoval else { return }
                pendingRemoval = nil
                remove(skill)
            }
        } message: {
            Text(removalAlertMessage)
        }
        .alert("更新全部技能？", isPresented: $confirmsUpdateAll) {
            Button("关闭", role: .cancel) {}
            Button("更新全部技能", role: .destructive) { updateAll() }
        } message: {
            Text(AppLanguage.localizedFormat(
                "将把「%@」作用域下所有已安装技能更新到最新版本。skills CLI 没有只读的检查模式，这一步会直接覆盖已安装的技能。",
                activeScopeTitle
            ))
        }
        .alert("安装 CLI 与项目技能？", isPresented: $confirmsProjectTools) {
            Button("关闭", role: .cancel) {}
            Button("开始安装", role: .destructive) { installProjectTools() }
        } message: {
            Text("将构建并安装当前项目的 quartet-cli，并把项目自带技能装到全局作用域。过程可能持续几分钟。")
        }
    }

    // MARK: - 卡片

    private var scopeCard: some View {
        AgentSettingsCard("技能列表", systemImage: "puzzlepiece.extension") {
            agentSettingsHint("技能是 Agent 可以按名字调用的能力包。全局技能对所有运行生效；项目技能装在工作区目录下，只有在该工作区运行的 Agent 才加载得到。")
            AgentSettingsSelectionRow(
                title: "作用域",
                value: activeScopeTitle,
                identifier: "skill-scope-picker"
            ) { sheet = .scope }
            if !activeScopeWorkdir.isEmpty {
                AgentSettingsMonoRow(label: "工作目录", value: activeScopeWorkdir)
            }
            if !canManage {
                agentSettingsHint("当前账号没有 skills.manage 权限，只能查看技能列表。")
            }
        }
    }

    private var actionsCard: some View {
        AgentSettingsCard {
            if canManage {
                primaryButton("安装技能", systemImage: "plus", identifier: "skill-open-install") {
                    message = nil
                    searchQuery = ""
                    searchResults = []
                    sheet = .install
                }
                secondaryButton("更新全部技能", systemImage: "arrow.up.circle", identifier: "skill-update-all") {
                    confirmsUpdateAll = true
                }
                agentSettingsHint("skills CLI 没有只读的“检查更新”命令，点击后会直接把当前作用域下的技能更新到最新版本。")
                secondaryButton("安装 CLI + 项目技能", systemImage: "shippingbox", identifier: "skill-install-project-tools") {
                    confirmsProjectTools = true
                }
                agentSettingsHint("构建并安装当前项目的 quartet-cli，同时把项目自带技能装到全局作用域。")
            }
            if !commandOutput.isEmpty {
                secondaryButton("查看上次命令输出", systemImage: "text.alignleft", identifier: "skill-show-output") {
                    sheet = .output
                }
            }
            searchField
        }
    }

    private var searchField: some View {
        HStack(spacing: 8) {
            Image(systemName: "magnifyingglass")
                .font(.quartet(.control))
                .foregroundStyle(QuartetTheme.secondaryText)
                .accessibilityHidden(true)
            TextField("筛选已安装的技能", text: $filterText)
                .font(.quartet(.control))
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .accessibilityIdentifier("skill-filter-field")
            if !filterText.isEmpty {
                Button {
                    quartetDismissKeyboard()
                    filterText = ""
                } label: {
                    Image(systemName: "xmark.circle.fill")
                        .font(.quartet(.control))
                        .foregroundStyle(QuartetTheme.secondaryText)
                }
                .buttonStyle(.plain)
                .accessibilityLabel("清除筛选".localizedForApp)
                .accessibilityIdentifier("skill-filter-clear")
            }
        }
        .padding(.horizontal, 14)
        .frame(height: 48)
        .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
    }

    @ViewBuilder
    private var skillList: some View {
        if filteredSkills.isEmpty {
            AgentSettingsCard {
                if !filterText.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty {
                    agentSettingsHint("没有匹配的技能")
                } else {
                    agentSettingsHint("当前作用域暂无已安装的技能")
                    if canManage {
                        agentSettingsHint("点“安装技能”可以按包名安装，或先搜索再安装。")
                    }
                }
            }
        } else {
            ForEach(filteredSkills) { skill in
                skillCard(skill)
            }
            AgentSettingsCard {
                agentSettingsHint(AppLanguage.localizedFormat("共 %d 个技能", filteredSkills.count))
            }
        }
    }

    private func skillCard(_ skill: SkillInfo) -> some View {
        AgentSettingsCard {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Text(skill.name)
                    .font(.quartet(.control, weight: .semibold))
                    .foregroundStyle(QuartetTheme.primaryText)
                    .textSelection(.enabled)
                    .fixedSize(horizontal: false, vertical: true)
                Spacer(minLength: 8)
                Text(LocalizedStringKey(skill.isGlobal ? "全局级" : "项目级"))
                    .font(.quartet(.compact, weight: .semibold))
                    .foregroundStyle(skill.isGlobal ? QuartetTheme.accent : QuartetTheme.softwareUpdate)
                    .padding(.horizontal, 8)
                    .padding(.vertical, 3)
                    .background(
                        (skill.isGlobal ? QuartetTheme.accent : QuartetTheme.softwareUpdate).opacity(0.12),
                        in: Capsule()
                    )
            }
            AgentSettingsMonoRow(label: "安装路径", value: skill.path)
            if let source = skill.source, !source.isEmpty {
                skillSourceRow(source: source, url: skill.sourceUrl)
            }
            if let agents = skill.agents, !agents.isEmpty {
                SkillAgentTagRow(agents: agents, previewCount: Self.agentTagPreview)
            }
            if canManage {
                destructiveButton("卸载", systemImage: "trash", identifier: "skill-remove") {
                    pendingRemoval = skill
                }
                .accessibilityLabel(AppLanguage.localizedFormat("卸载技能 %@", skill.name))
            }
        }
    }

    @ViewBuilder
    private func skillSourceRow(source: String, url: String?) -> some View {
        VStack(alignment: .leading, spacing: 4) {
            agentSettingsFieldLabel("来源")
            if let url, let sourceURL = URL(string: url) {
                Link(destination: sourceURL) {
                    Text(source)
                        .font(.quartet(.detail, design: .monospaced))
                        .foregroundStyle(QuartetTheme.accent)
                        .fixedSize(horizontal: false, vertical: true)
                        .frame(maxWidth: .infinity, alignment: .leading)
                }
                .accessibilityHint("点按在浏览器中打开来源仓库")
            } else {
                Text(source)
                    .font(.quartet(.detail, design: .monospaced))
                    .foregroundStyle(QuartetTheme.primaryText)
                    .textSelection(.enabled)
                    .fixedSize(horizontal: false, vertical: true)
                    .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
    }

    // MARK: - 按钮

    private func primaryButton(
        _ title: String,
        systemImage: String,
        identifier: String,
        action: @escaping () -> Void
    ) -> some View {
        Button {
            quartetDismissKeyboard()
            action()
        } label: {
            Label(title.localizedForApp, systemImage: systemImage)
                .font(.quartet(.control, weight: .semibold))
                .foregroundStyle(QuartetTheme.onAccent)
                .frame(maxWidth: .infinity)
                .frame(height: 48)
                .background(QuartetTheme.accent, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
        }
        .buttonStyle(.plain)
        .disabled(isMutating)
        .opacity(isMutating ? 0.45 : 1)
        .accessibilityIdentifier(identifier)
    }

    private func secondaryButton(
        _ title: String,
        systemImage: String,
        identifier: String,
        action: @escaping () -> Void
    ) -> some View {
        Button {
            quartetDismissKeyboard()
            action()
        } label: {
            Label(title.localizedForApp, systemImage: systemImage)
                .font(.quartet(.control, weight: .semibold))
                .foregroundStyle(QuartetTheme.accent)
                .frame(maxWidth: .infinity)
                .frame(height: 46)
                .background(
                    QuartetTheme.accent.opacity(0.1),
                    in: RoundedRectangle(cornerRadius: 12, style: .continuous)
                )
        }
        .buttonStyle(.plain)
        .disabled(isMutating)
        .opacity(isMutating ? 0.45 : 1)
        .accessibilityIdentifier(identifier)
    }

    private func destructiveButton(
        _ title: String,
        systemImage: String,
        identifier: String,
        action: @escaping () -> Void
    ) -> some View {
        Button {
            quartetDismissKeyboard()
            action()
        } label: {
            Label(title.localizedForApp, systemImage: systemImage)
                .font(.quartet(.control, weight: .semibold))
                .foregroundStyle(QuartetTheme.failed)
                .frame(maxWidth: .infinity)
                .frame(height: 46)
                .background(
                    QuartetTheme.failed.opacity(0.1),
                    in: RoundedRectangle(cornerRadius: 12, style: .continuous)
                )
        }
        .buttonStyle(.plain)
        .disabled(isMutating)
        .opacity(isMutating ? 0.45 : 1)
        .accessibilityIdentifier(identifier)
    }

    // MARK: - 弹窗

    @ViewBuilder
    private func sheetContent(_ sheet: SkillSheet) -> some View {
        switch sheet {
        case .scope:
            QuartetChoiceSheet(
                title: "作用域",
                choices: scopeChoices,
                selection: Binding(get: { scopeKey }, set: { scopeKey = $0 }),
                accessibilityPrefix: "skill-scope"
            )
            .presentationDetents([.medium, .large])
            .quartetSheetStyle()
        case .install:
            SkillInstallSheet(
                scopeTitle: activeScopeTitle,
                agentTargets: Self.agentTargets,
                selectedAgents: $installAgents,
                package: $installPackage,
                searchQuery: $searchQuery,
                searchResults: $searchResults,
                isSearching: $isSearching,
                isInstalling: isMutating,
                onSearch: { search() },
                onInstall: { package in install(package) }
            )
            .presentationDetents([.large])
            .quartetSheetStyle()
        case .output:
            SkillCommandOutputSheet(title: commandOutputTitle, output: commandOutput)
                .presentationDetents([.medium, .large])
                .quartetSheetStyle()
        }
    }

    private var removalAlertBinding: Binding<Bool> {
        Binding(
            get: { pendingRemoval != nil },
            set: { presented in if !presented { pendingRemoval = nil } }
        )
    }

    private var removalAlertMessage: String {
        guard let skill = pendingRemoval else { return "" }
        return AppLanguage.localizedFormat(
            "将从「%@」作用域移除 %@，技能文件与各 Agent 的链接都会被删除。",
            skill.isGlobal ? "全局级".localizedForApp : "项目级".localizedForApp,
            skill.name
        )
    }

    // MARK: - 作用域

    private var scopeChoices: [QuartetChoice] {
        var choices = [QuartetChoice(
            id: SkillScope.global.key,
            title: "全局级",
            detail: "所有运行都能用到的技能"
        )]
        guard model.can("workspace.read") else { return choices }
        for workspace in model.workspaces {
            choices.append(QuartetChoice(
                id: SkillScope.workspace(workspace.id).key,
                title: workspace.displayName,
                detail: workspace.workdir
            ))
        }
        return choices
    }

    private var activeScopeTitle: String {
        switch activeScope {
        case .global:
            return "全局级".localizedForApp
        case .workspace(let id):
            return model.workspaces.first { $0.id == id }?.displayName ?? id
        }
    }

    private var activeScopeWorkdir: String {
        guard case .workspace(let id) = activeScope else { return "" }
        return model.workspaces.first { $0.id == id }?.workdir ?? ""
    }

    // MARK: - 列表筛选

    private var filteredSkills: [SkillInfo] {
        let keyword = filterText.trimmingCharacters(in: .whitespacesAndNewlines).lowercased()
        guard !keyword.isEmpty else { return skills }
        let agentKeyword = Self.normalizeAgent(keyword)
        return skills.filter { skill in
            if skill.name.lowercased().contains(keyword) { return true }
            if skill.path.lowercased().contains(keyword) { return true }
            if (skill.source ?? "").lowercased().contains(keyword) { return true }
            guard !agentKeyword.isEmpty else { return false }
            return (skill.agents ?? []).contains { Self.normalizeAgent($0).contains(agentKeyword) }
        }
    }

    /// Agent 标识安装时是 slug（`claude-code`），列表里回报的是展示名
    /// （`Claude Code`），折成同一种形式后两种写法都能搜到。
    private static func normalizeAgent(_ value: String) -> String {
        value.lowercased().filter { $0.isLetter || $0.isNumber }
    }

    // MARK: - 读取

    private func load() async {
        loadSequence += 1
        let sequence = loadSequence
        let scope = activeScope
        isLoading = true
        loadError = ""

        for _ in 0..<Self.readyPollAttempts {
            do {
                let response = try await model.skills(
                    global: scope.isGlobal,
                    workspaceID: scope.workspaceID
                )
                guard sequence == loadSequence else { return }
                // ready=false 表示后端还在跑首次 `skills ls`，此时的空列表不是
                // “没有技能”，要继续等。
                if response.ready == false {
                    try await Task.sleep(for: Self.readyPollDelay)
                    guard sequence == loadSequence else { return }
                    continue
                }
                skills = response.skills ?? []
                // 读取可以只成功一半：缓存里还有旧列表，同时带着这次刷新的失败原文。
                loadError = response.error ?? ""
            } catch is CancellationError {
                return
            } catch {
                guard sequence == loadSequence else { return }
                skills = []
                loadError = agentSettingsErrorDetail(error)
            }
            isLoading = false
            return
        }
        guard sequence == loadSequence else { return }
        isLoading = false
        loadError = "等待技能列表超时，请稍后重试。".localizedForApp
    }

    // MARK: - 写操作

    private func install(_ package: String) {
        let trimmed = package.trimmingCharacters(in: .whitespacesAndNewlines)
        guard canManage, !trimmed.isEmpty, !isMutating else { return }
        let scope = activeScope
        let agents = installAgents
        run(
            title: AppLanguage.localizedFormat("安装 %@", trimmed),
            success: AppLanguage.localizedFormat("已安装 %@", trimmed),
            operation: {
                try await model.addSkill(
                    package: trimmed,
                    global: scope.isGlobal,
                    workspaceID: scope.workspaceID,
                    agents: agents
                )
            },
            onSuccess: {
                sheet = nil
                installPackage = ""
                searchQuery = ""
                searchResults = []
            }
        )
    }

    private func remove(_ skill: SkillInfo) {
        guard canManage, !isMutating else { return }
        // 按技能自己回报的作用域卸载，而不是当前选中的作用域。
        let workspaceID = skill.isGlobal ? "" : activeScope.workspaceID
        run(
            title: AppLanguage.localizedFormat("卸载 %@", skill.name),
            success: AppLanguage.localizedFormat("已卸载 %@", skill.name),
            operation: {
                try await model.removeSkill(
                    name: skill.name,
                    global: skill.isGlobal,
                    workspaceID: workspaceID
                )
            }
        )
    }

    private func updateAll() {
        guard canManage, !isMutating else { return }
        let scope = activeScope
        run(
            title: "更新全部技能",
            success: "所有技能已更新到最新版本",
            operation: {
                try await model.updateSkills(global: scope.isGlobal, workspaceID: scope.workspaceID)
            }
        )
    }

    private func installProjectTools() {
        guard canManage, !isMutating else { return }
        run(
            title: "安装 CLI + 项目技能",
            success: "quartet-cli 和项目技能安装完成",
            operation: { try await model.installProjectTools() }
        )
    }

    /// 写操作的统一外壳：占位、成功 / 失败提示、保留命令输出、结束后重新读取列表。
    private func run(
        title: String,
        success: String,
        operation: @escaping () async throws -> String,
        onSuccess: @escaping () -> Void = {}
    ) {
        isMutating = true
        message = nil
        Task { @MainActor in
            do {
                let output = try await operation()
                commandOutputTitle = title.localizedForApp
                commandOutput = output
                message = .success(success.localizedForApp)
                onSuccess()
                await load()
            } catch is CancellationError {
                isMutating = false
                return
            } catch {
                commandOutputTitle = title.localizedForApp
                commandOutput = agentSettingsErrorDetail(error)
                message = .failure(agentSettingsErrorDetail(error))
            }
            isMutating = false
        }
    }

    private func search() {
        let trimmed = searchQuery.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty, !isSearching else { return }
        isSearching = true
        searchResults = []
        message = nil
        Task { @MainActor in
            do {
                searchResults = try await model.findSkills(query: trimmed)
                if searchResults.isEmpty {
                    message = .failure(AppLanguage.localizedFormat("没有找到与 %@ 相关的技能", trimmed))
                }
            } catch is CancellationError {
                isSearching = false
                return
            } catch {
                message = .failure(agentSettingsErrorDetail(error))
            }
            isSearching = false
        }
    }
}

// MARK: - Agent 标签行

/// 默认只显示前几个 Agent 标签，其余折叠。
private struct SkillAgentTagRow: View {
    let agents: [String]
    let previewCount: Int

    @State private var isExpanded = false

    var body: some View {
        let hidden = agents.count - previewCount
        let shown = isExpanded ? agents : Array(agents.prefix(previewCount))
        return VStack(alignment: .leading, spacing: 6) {
            agentSettingsFieldLabel("已安装到的 Agent")
            WrappingHStack(spacing: 6) {                ForEach(shown, id: \.self) { agent in
                    Text(agent)
                        .font(.quartet(.compact))
                        .foregroundStyle(QuartetTheme.primaryText)
                        .padding(.horizontal, 8)
                        .padding(.vertical, 4)
                        .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 8, style: .continuous))
                }
                if hidden > 0 {
                    Button {
                        isExpanded.toggle()
                    } label: {
                        Text(isExpanded
                            ? "收起".localizedForApp
                            : AppLanguage.localizedFormat("还有 %d 个", hidden))
                            .font(.quartet(.compact, weight: .semibold))
                            .foregroundStyle(QuartetTheme.accent)
                            .padding(.horizontal, 8)
                            .padding(.vertical, 4)
                            .background(
                                QuartetTheme.accent.opacity(0.1),
                                in: RoundedRectangle(cornerRadius: 8, style: .continuous)
                            )
                    }
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("skill-agents-toggle")
                }
            }
        }
    }
}

// MARK: - 安装弹窗

private struct SkillInstallSheet: View {
    let scopeTitle: String
    let agentTargets: [SkillAgentTarget]
    @Binding var selectedAgents: [String]
    @Binding var package: String
    @Binding var searchQuery: String
    @Binding var searchResults: [SkillFindResult]
    @Binding var isSearching: Bool
    let isInstalling: Bool
    let onSearch: () -> Void
    let onInstall: (String) -> Void

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(spacing: 12) {
                    AgentSettingsCard("安装到", systemImage: "scope") {
                        AgentSettingsMonoRow(label: "作用域", value: scopeTitle)
                        agentSettingsHint("安装作用域跟随列表页当前选中的作用域，返回后可在那里切换。")
                    }

                    AgentSettingsCard("包名或仓库地址", systemImage: "shippingbox") {
                        AgentSettingsTextField(
                            title: "包名或仓库地址",
                            text: $package,
                            identifier: "skill-install-package-field",
                            placeholder: "例如：vercel-labs/agent-skills",
                            monospaced: true
                        )
                        installButton(package, identifier: "skill-install-submit")
                    }

                    AgentSettingsCard("安装到以下 Agent", systemImage: "person.2") {
                        ForEach(agentTargets, id: \.slug) { target in
                            agentToggle(target)
                        }
                        if selectedAgents.isEmpty {
                            agentSettingsHint("没有勾选任何 Agent 时，skills CLI 会按自己的默认规则决定装到哪里。")
                        }
                    }

                    AgentSettingsCard("搜索技能", systemImage: "magnifyingglass") {
                        AgentSettingsTextField(
                            title: "关键词",
                            text: $searchQuery,
                            identifier: "skill-search-field",
                            placeholder: "例如：pptx, react"
                        )
                        Button {
                            quartetDismissKeyboard()
                            onSearch()
                        } label: {
                            HStack(spacing: 8) {
                                if isSearching { ProgressView().tint(QuartetTheme.accent) }
                                Text(LocalizedStringKey(isSearching ? "正在搜索…" : "搜索"))
                                    .font(.quartet(.control, weight: .semibold))
                            }
                            .foregroundStyle(QuartetTheme.accent)
                            .frame(maxWidth: .infinity)
                            .frame(height: 46)
                            .background(
                                QuartetTheme.accent.opacity(0.1),
                                in: RoundedRectangle(cornerRadius: 12, style: .continuous)
                            )
                        }
                        .buttonStyle(.plain)
                        .disabled(isSearching || searchQuery.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty)
                        .opacity(isSearching || searchQuery.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? 0.45 : 1)
                        .accessibilityIdentifier("skill-search-submit")
                    }

                    ForEach(searchResults) { result in
                        searchResultCard(result)
                    }
                }
                .padding(.horizontal, 18)
                .padding(.vertical, 12)
            }
            .scrollDismissesKeyboard(.interactively)
            .background(QuartetTheme.canvas)
            .quartetNavigationTitle("安装技能")
        }
    }

    private func agentToggle(_ target: SkillAgentTarget) -> some View {
        let isOn = selectedAgents.contains(target.slug)
        return Button {
            quartetDismissKeyboard()
            if isOn {
                selectedAgents.removeAll { $0 == target.slug }
            } else {
                selectedAgents.append(target.slug)
            }
        } label: {
            HStack(spacing: 12) {
                Image(systemName: isOn ? "checkmark.circle.fill" : "circle")
                    .font(.quartet(.control))
                    .foregroundStyle(isOn ? QuartetTheme.accent : QuartetTheme.secondaryText)
                Text(target.label)
                    .font(.quartet(.control))
                    .foregroundStyle(QuartetTheme.primaryText)
                Spacer(minLength: 8)
                Text(target.slug)
                    .font(.quartet(.compact, design: .monospaced))
                    .foregroundStyle(QuartetTheme.secondaryText)
            }
            .frame(minHeight: 44)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityAddTraits(isOn ? .isSelected : [])
        .accessibilityIdentifier("skill-agent-\(target.slug)")
    }

    private func searchResultCard(_ result: SkillFindResult) -> some View {
        AgentSettingsCard {
            Text(result.name)
                .font(.quartet(.detail, design: .monospaced))
                .foregroundStyle(QuartetTheme.primaryText)
                .textSelection(.enabled)
                .fixedSize(horizontal: false, vertical: true)
                .frame(maxWidth: .infinity, alignment: .leading)
            HStack(spacing: 8) {
                Text(AppLanguage.localizedFormat("%@ 次安装", result.installs))
                    .font(.quartet(.compact))
                    .foregroundStyle(QuartetTheme.secondaryText)
                Spacer(minLength: 8)
                if let url = URL(string: result.url), !result.url.isEmpty {
                    Link(destination: url) {
                        Label("详情", systemImage: "safari")
                            .font(.quartet(.compact, weight: .semibold))
                            .foregroundStyle(QuartetTheme.accent)
                    }
                }
            }
            installButton(result.name, identifier: "skill-search-install-\(result.name)")
        }
    }

    private func installButton(_ target: String, identifier: String) -> some View {
        let trimmed = target.trimmingCharacters(in: .whitespacesAndNewlines)
        let disabled = trimmed.isEmpty || isInstalling
        return Button {
            quartetDismissKeyboard()
            onInstall(trimmed)
        } label: {
            HStack(spacing: 8) {
                if isInstalling { ProgressView().tint(QuartetTheme.onAccent) }
                Text(LocalizedStringKey(isInstalling ? "正在安装…" : "安装"))
                    .font(.quartet(.control, weight: .semibold))
            }
            .foregroundStyle(QuartetTheme.onAccent)
            .frame(maxWidth: .infinity)
            .frame(height: 46)
            .background(QuartetTheme.accent, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
        }
        .buttonStyle(.plain)
        .disabled(disabled)
        .opacity(disabled ? 0.45 : 1)
        .accessibilityIdentifier(identifier)
    }
}

// MARK: - 命令输出弹窗

/// 原样展示 skills CLI 的输出，保留缩进并支持整段复制。
private struct SkillCommandOutputSheet: View {
    @Environment(\.dismiss) private var dismiss
    let title: String
    let output: String

    var body: some View {
        NavigationStack {
            ScrollView {
                Text(output)
                    .font(.quartet(.compact, design: .monospaced))
                    .foregroundStyle(QuartetTheme.primaryText)
                    .textSelection(.enabled)
                    .fixedSize(horizontal: false, vertical: true)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(12)
                    .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                    .padding(.horizontal, 18)
                    .padding(.vertical, 12)
            }
            .background(QuartetTheme.canvas)
            .quartetNavigationTitle(title)
            .safeAreaInset(edge: .bottom, spacing: 0) {
                HStack(spacing: 10) {
                    Button("复制全部输出") { UIPasteboard.general.string = output }
                        .foregroundStyle(QuartetTheme.primaryText)
                        .frame(maxWidth: .infinity).frame(height: 50)
                        .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
                    Button("关闭") { dismiss() }
                        .foregroundStyle(QuartetTheme.onAccent)
                        .frame(maxWidth: .infinity).frame(height: 50)
                        .background(QuartetTheme.accent, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
                }
                .font(.quartet(.control, weight: .semibold))
                .buttonStyle(.plain)
                .padding(.horizontal, 18)
                .padding(.vertical, 10)
                .background(.ultraThinMaterial)
            }
        }
    }
}
