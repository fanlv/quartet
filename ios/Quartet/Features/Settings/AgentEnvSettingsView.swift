import SwiftUI

/// 环境变量的一行。`id` 只用于列表复用，不参与提交。
private struct AgentEnvRow: Identifiable, Equatable {
    let id = UUID()
    var key: String
    var value: String
    var enabled: Bool
}

/// 一个可编辑环境变量的对象。`envKey` 是设置里的存储键，`agentId` 是保存时的路径参数。
private struct AgentEnvTarget: Identifiable, Hashable {
    let envKey: String
    let agentId: String
    let displayName: String
    let installed: Bool
    /// ACP 启动命令。空串表示这条只是历史遗留的存储键，没有对应 Agent，也就读不到版本与用量。
    let command: String
    /// ACP 适配器用于选择外部 CLI 的环境变量。空串表示该 Agent 不支持来源切换。
    let cliExecutableEnv: String
    let cliExecutable: String

    var id: String { envKey }

    init(
        envKey: String,
        agentId: String,
        displayName: String,
        installed: Bool = true,
        command: String = "",
        cliExecutableEnv: String = "",
        cliExecutable: String = ""
    ) {
        self.envKey = envKey
        self.agentId = agentId
        self.displayName = displayName
        self.installed = installed
        self.command = command
        self.cliExecutableEnv = cliExecutableEnv
        self.cliExecutable = cliExecutable
    }
}

private enum AgentCLISource: String {
    case installed
    case bundled
    case custom

    var title: String {
        switch self {
        case .installed: "本机安装".localizedForApp
        case .bundled: "ACP 内置".localizedForApp
        case .custom: "自定义路径".localizedForApp
        }
    }
}

/// 与 Web 端一致的占位默认值：默认关闭，只是把常用变量和 CLI 来源变量先摆出来。
private func agentEnvDefaultRows(for target: AgentEnvTarget) -> [AgentEnvRow] {
    var rows = [
        AgentEnvRow(key: "http_proxy", value: "http://127.0.0.1:8890", enabled: false),
        AgentEnvRow(key: "https_proxy", value: "http://127.0.0.1:8890", enabled: false),
    ]
    if !target.cliExecutableEnv.isEmpty {
        rows.insert(AgentEnvRow(key: target.cliExecutableEnv, value: "", enabled: false), at: 0)
    }
    return rows
}

private func mergeCLISourceRow(_ saved: [AgentEnvRow], for target: AgentEnvTarget) -> [AgentEnvRow] {
    guard !target.cliExecutableEnv.isEmpty,
          !saved.contains(where: { $0.key.trimmingCharacters(in: .whitespaces) == target.cliExecutableEnv }) else {
        return saved
    }
    var rows = saved
    rows.insert(AgentEnvRow(key: target.cliExecutableEnv, value: "", enabled: false), at: 0)
    return rows
}

@MainActor
struct AgentEnvSettingsView: View {
    @EnvironmentObject private var model: AppModel

    /// “选择 Agent”弹窗副标题要显示的版本号与用量。全局单例，跨弹窗复用缓存与节流。
    @ObservedObject private var agentUsageSummaries = AgentUsageSummaryStore.shared

    @State private var targets: [AgentEnvTarget] = []
    @State private var activeKey = ""
    @State private var envMap: [String: [AgentEnvRow]] = [:]
    @State private var isLoading = true
    @State private var loadError = ""
    @State private var isSaving = false
    @State private var message: AgentSettingsMessage?
    @State private var showsTargetPicker = false
    @State private var showsCLISourcePicker = false

    private var canWrite: Bool { model.can("config.write") }
    private var activeTarget: AgentEnvTarget? { targets.first { $0.envKey == activeKey } }
    private var activeRows: [AgentEnvRow] { envMap[activeKey] ?? [] }
    private var activeCLISource: AgentCLISource {
        guard let target = activeTarget, !target.cliExecutableEnv.isEmpty,
              let row = activeRows.first(where: {
                  $0.key.trimmingCharacters(in: .whitespaces) == target.cliExecutableEnv
              }), row.enabled else {
            return .installed
        }
        return row.value.isEmpty ? .bundled : .custom
    }

    var body: some View {
        Group {
            if isLoading && targets.isEmpty {
                AgentSettingsLoadingView(title: "正在加载环境变量…")
            } else if !loadError.isEmpty {
                AgentSettingsLoadFailure(detail: loadError) { Task { await load() } }
            } else if targets.isEmpty {
                emptyState
            } else {
                editor
            }
        }
        .background(QuartetTheme.canvas)
        .task { await initialLoad() }
        .onChange(of: activeKey) { _, _ in message = nil }
        .sheet(isPresented: $showsTargetPicker) {
            QuartetChoiceSheet(
                title: "选择 Agent",
                choices: targets.map { target in
                    .agent(
                        id: target.envKey,
                        title: target.displayName,
                        command: target.envKey,
                        note: target.installed ? nil : "已卸载".localizedForApp,
                        usage: target.command.isEmpty
                            ? nil
                            : agentUsageSummaries.summary(command: target.command, displayName: target.displayName),
                        retry: { Task { await loadAgentUsageSummaries() } }
                    )
                },
                selection: $activeKey,
                accessibilityPrefix: "agent-env-target"
            )
            .presentationDetents([.medium, .large])
            .quartetSheetStyle()
            .task { await loadAgentUsageSummaries() }
        }
        .sheet(isPresented: $showsCLISourcePicker) {
            if let target = activeTarget, !target.cliExecutableEnv.isEmpty {
                QuartetChoiceSheet(
                    title: "选择 CLI 来源",
                    choices: cliSourceChoices(for: target),
                    selection: Binding(
                        get: { activeCLISource.rawValue },
                        set: { rawValue in
                            guard let source = AgentCLISource(rawValue: rawValue) else { return }
                            applyCLISource(source, to: target)
                        }
                    ),
                    accessibilityPrefix: "agent-env-cli-source-choice"
                )
                .presentationDetents([.medium])
                .quartetSheetStyle()
            }
        }
    }

    private var emptyState: some View {
        ScrollView {
            AgentSettingsCard("ACP 环境变量", systemImage: "key") {
                agentSettingsHint("当前没有可配置的 Agent。先在“Agent 目录”里安装或新增一个 Agent。")
            }
            .padding(18)
        }
    }

    private var editor: some View {
        ScrollView {
            LazyVStack(spacing: 12) {
                targetCard
                if activeTarget?.cliExecutableEnv.isEmpty == false {
                    cliSourceCard
                }
                variablesCard
            }
            .padding(.horizontal, 18)
            .padding(.vertical, 12)
        }
        .scrollDismissesKeyboard(.interactively)
        .safeAreaInset(edge: .bottom, spacing: 0) {
            if canWrite {
                AgentSettingsSaveBar(
                    title: "保存环境变量",
                    savingTitle: "正在保存…",
                    isSaving: isSaving,
                    isEnabled: activeTarget != nil,
                    message: message,
                    identifier: "agent-env-save",
                    action: { save() }
                )
            }
        }
    }

    private var targetCard: some View {
        AgentSettingsCard {
            agentSettingsHint("这些变量只在启动对应 Agent 的 ACP 进程时注入；关闭的条目会被保留但不生效。")
            agentSettingsFieldLabel("Agent")
            Button {
                quartetDismissKeyboard()
                showsTargetPicker = true
            } label: {
                HStack(spacing: 12) {
                    Image(systemName: "terminal")
                        .font(.quartet(.regular, weight: .semibold))
                        .foregroundStyle(QuartetTheme.accent)
                        .frame(width: 38, height: 38)
                        .background(QuartetTheme.accent.opacity(0.1), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                        .accessibilityHidden(true)

                    VStack(alignment: .leading, spacing: 3) {
                        HStack(spacing: 7) {
                            Text(activeTarget?.displayName ?? "请选择".localizedForApp)
                                .font(.quartet(.control, weight: .semibold))
                                .foregroundStyle(activeTarget == nil ? QuartetTheme.secondaryText : QuartetTheme.primaryText)
                                .lineLimit(1)
                            if activeTarget?.installed == false {
                                statusBadge("已卸载", color: QuartetTheme.warning)
                            }
                        }
                        if let activeTarget {
                            Text(activeTarget.envKey)
                                .font(.quartet(.detail, design: .monospaced))
                                .foregroundStyle(QuartetTheme.secondaryText)
                                .lineLimit(1)
                        }
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)

                    Image(systemName: "chevron.up.chevron.down")
                        .font(.quartet(.compact, weight: .bold))
                        .foregroundStyle(QuartetTheme.secondaryText)
                        .accessibilityHidden(true)
                }
                .padding(12)
                .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
                .contentShape(RoundedRectangle(cornerRadius: 14, style: .continuous))
            }
            .buttonStyle(.plain)
            .accessibilityLabel("Agent".localizedForApp)
            .accessibilityValue(activeTargetAccessibilityValue)
            .accessibilityIdentifier("agent-env-target-picker")
        }
    }

    private var activeTargetAccessibilityValue: String {
        guard let activeTarget else { return "请选择".localizedForApp }
        return activeTarget.installed
            ? activeTarget.displayName
            : "\(activeTarget.displayName)，\("已卸载".localizedForApp)"
    }

    private var variablesCard: some View {
        AgentSettingsCard("变量列表", systemImage: "list.bullet") {
            if activeRows.isEmpty {
                VStack(spacing: 8) {
                    Image(systemName: "text.badge.plus")
                        .font(.quartet(.large))
                        .foregroundStyle(QuartetTheme.secondaryText)
                    agentSettingsHint("还没有变量。点下面的按钮添加一条 KEY=value。")
                        .multilineTextAlignment(.center)
                }
                .frame(maxWidth: .infinity)
                .padding(.vertical, 10)
            } else {
                ForEach(activeRows) { row in
                    variableRow(row)
                }
            }
            if canWrite {
                Button {
                    quartetDismissKeyboard()
                    envMap[activeKey, default: []].append(AgentEnvRow(key: "", value: "", enabled: true))
                    message = nil
                } label: {
                    Label("添加变量", systemImage: "plus")
                        .font(.quartet(.control, weight: .semibold))
                        .foregroundStyle(QuartetTheme.accent)
                        .frame(maxWidth: .infinity)
                        .frame(height: 44)
                        .background(QuartetTheme.accent.opacity(0.08), in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                        .overlay {
                            RoundedRectangle(cornerRadius: 12, style: .continuous)
                                .stroke(QuartetTheme.accent.opacity(0.22))
                        }
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("agent-env-add")
            } else {
                agentSettingsHint("当前账号没有 config.write 权限，只能查看环境变量。")
            }
        }
    }

    private var cliSourceCard: some View {
        AgentSettingsCard("CLI 来源", systemImage: "shippingbox") {
            agentSettingsHint("选择 ACP 适配器实际使用的 CLI。自定义命令或路径可在下方环境变量中编辑。")
            AgentSettingsSelectionRow(
                title: "CLI 来源",
                value: activeCLISource.title,
                identifier: "agent-env-cli-source"
            ) { showsCLISourcePicker = true }
            .disabled(!canWrite)
            .opacity(canWrite ? 1 : 0.55)
        }
    }

    private func cliSourceChoices(for target: AgentEnvTarget) -> [QuartetChoice] {
        [
            QuartetChoice(
                id: AgentCLISource.installed.rawValue,
                title: "本机安装",
                detail: AppLanguage.localizedFormat("动态使用 PATH 中的 %@，升级后自动跟随新版本", target.cliExecutable)
            ),
            QuartetChoice(
                id: AgentCLISource.bundled.rawValue,
                title: "ACP 内置",
                detail: "使用适配器自带且经过版本约束的 CLI，兼容性更稳妥"
            ),
            QuartetChoice(
                id: AgentCLISource.custom.rawValue,
                title: "自定义路径",
                detail: AppLanguage.localizedFormat("使用下方 %@ 中填写的命令或路径", target.cliExecutableEnv)
            ),
        ]
    }

    private func applyCLISource(_ source: AgentCLISource, to target: AgentEnvTarget) {
        var rows = envMap[target.envKey] ?? []
        let index = rows.firstIndex {
            $0.key.trimmingCharacters(in: .whitespaces) == target.cliExecutableEnv
        }
        let existingValue = index.map { rows[$0].value } ?? ""
        let value: String
        switch source {
        case .bundled:
            value = ""
        case .custom where existingValue.isEmpty:
            value = target.cliExecutable
        case .installed, .custom:
            value = existingValue
        }
        let row = AgentEnvRow(
            key: target.cliExecutableEnv,
            value: value,
            enabled: source != .installed
        )
        if let index {
            rows[index] = row
        } else {
            rows.insert(row, at: 0)
        }
        envMap[target.envKey] = rows
        message = nil
    }

    private func variableRow(_ row: AgentEnvRow) -> some View {
        let binding = bindingFor(row.id)
        return VStack(alignment: .leading, spacing: 10) {
            HStack(spacing: 8) {
                Toggle(row.enabled ? "已启用" : "已停用", isOn: binding.enabled)
                    .font(.quartet(.detail, weight: .medium))
                    .foregroundStyle(row.enabled ? QuartetTheme.accentDeep : QuartetTheme.secondaryText)
                    .toggleStyle(QuartetCheckmarkToggleStyle(layout: .compact))
                    .disabled(!canWrite)
                    .accessibilityLabel(row.key.isEmpty ? "启用这条变量".localizedForApp : AppLanguage.localizedFormat("启用 %@", row.key))

                Spacer(minLength: 8)

                if canWrite {
                    Button {
                        quartetDismissKeyboard()
                        envMap[activeKey]?.removeAll { $0.id == row.id }
                        message = nil
                    } label: {
                        Image(systemName: "trash")
                            .font(.quartet(.detail, weight: .semibold))
                            .foregroundStyle(QuartetTheme.failed)
                            .frame(width: 44, height: 44)
                            .contentShape(Rectangle())
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(AppLanguage.localizedFormat("删除变量 %@", row.key))
                }
            }

            envField("变量名", text: binding.key, identifier: "agent-env-key-field")
            envField("变量值", text: binding.value, identifier: "agent-env-value-field")
        }
        .padding(12)
        .background(QuartetTheme.elevated.opacity(row.enabled ? 0.72 : 0.38), in: RoundedRectangle(cornerRadius: 14, style: .continuous))
        .overlay {
            RoundedRectangle(cornerRadius: 14, style: .continuous)
                .stroke(QuartetTheme.divider.opacity(row.enabled ? 0.72 : 0.45))
        }
        .animation(.easeInOut(duration: 0.15), value: row.enabled)
    }

    private func envField(_ title: String, text: Binding<String>, identifier: String) -> some View {
        VStack(alignment: .leading, spacing: 5) {
            agentSettingsFieldLabel(title)
            TextField(LocalizedStringKey(title), text: text)
                // KEY/value 是代码型内容；统一字体入口会选择 SF Mono，并为汉字补思源黑体回退。
                .font(.quartet(.control, design: .monospaced))
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .disabled(!canWrite)
                .padding(.horizontal, 12)
                .frame(height: 44)
                .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                .overlay {
                    RoundedRectangle(cornerRadius: 10, style: .continuous)
                        .stroke(QuartetTheme.divider.opacity(0.65))
                }
                .accessibilityIdentifier(identifier)
        }
    }

    private func statusBadge(_ title: String, color: Color) -> some View {
        Text(title.localizedForApp)
            .font(.quartet(.tiny, weight: .semibold))
            .foregroundStyle(color)
            .padding(.horizontal, 7)
            .padding(.vertical, 3)
            .background(color.opacity(0.1), in: Capsule())
            .overlay { Capsule().stroke(color.opacity(0.24)) }
    }

    /// 按行 ID 定位，避免在增删过程中把编辑内容写到别的行上。
    private func bindingFor(_ id: UUID) -> (key: Binding<String>, value: Binding<String>, enabled: Binding<Bool>) {
        let key = Binding<String>(
            get: { envMap[activeKey]?.first { $0.id == id }?.key ?? "" },
            set: { newValue in
                guard let index = envMap[activeKey]?.firstIndex(where: { $0.id == id }) else { return }
                envMap[activeKey]?[index].key = newValue
                message = nil
            }
        )
        let value = Binding<String>(
            get: { envMap[activeKey]?.first { $0.id == id }?.value ?? "" },
            set: { newValue in
                guard let index = envMap[activeKey]?.firstIndex(where: { $0.id == id }) else { return }
                envMap[activeKey]?[index].value = newValue
                message = nil
            }
        )
        let enabled = Binding<Bool>(
            get: { envMap[activeKey]?.first { $0.id == id }?.enabled ?? false },
            set: { newValue in
                guard let index = envMap[activeKey]?.firstIndex(where: { $0.id == id }) else { return }
                envMap[activeKey]?[index].enabled = newValue
                message = nil
            }
        )
        return (key, value, enabled)
    }

    private func initialLoad() async {
        guard targets.isEmpty else { return }
        await load()
    }

    /// 打开“选择 Agent”弹窗时读取每个 Agent 的版本号与用量：先出缓存，再后台刷新。
    /// 失败不占用节流窗口，所以行内“重试”按钮直接再调一次。
    private func loadAgentUsageSummaries() async {
        let probeTargets = targets
            .filter { !$0.command.isEmpty }
            .map { AgentUsageProbeTarget(command: $0.command, displayName: $0.displayName) }
        await agentUsageSummaries.load(targets: probeTargets, model: model)
    }

    private func load() async {
        isLoading = true
        loadError = ""
        message = nil
        do {
            async let agentRequest = model.agentCatalog()
            async let catalogRequest = model.managedAgentCatalogItems()
            async let settingsRequest = model.agentEnvironmentSettings()
            let (agentList, catalog, saved) = try await (agentRequest, catalogRequest, settingsRequest)
            let catalogByID = Dictionary(
                catalog.filter { $0.lifecycle == "active" }.map { ($0.agentId, $0) },
                uniquingKeysWith: { first, _ in first }
            )

            var resolved: [AgentEnvTarget] = []
            var rows: [String: [AgentEnvRow]] = [:]
            for agent in agentList {
                let envKey = agent.environmentKey
                guard !resolved.contains(where: { $0.envKey == envKey }) else { continue }
                let catalogAgent = catalogByID[agent.agentId]
                let target = AgentEnvTarget(
                    envKey: envKey,
                    agentId: agent.agentId,
                    displayName: agent.displayName.isEmpty ? agent.agentId : agent.displayName,
                    installed: catalogAgent?.installed ?? true,
                    command: agent.available ? agent.type : "",
                    cliExecutableEnv: catalogAgent?.cliExecutableEnv ?? "",
                    cliExecutable: catalogAgent?.definition.bin ?? agent.agentId
                )
                resolved.append(target)
                let entries = saved[envKey] ?? saved[agent.type]
                if let entries, !entries.isEmpty {
                    rows[envKey] = mergeCLISourceRow(
                        entries.map { AgentEnvRow(key: $0.key, value: $0.value, enabled: $0.enabled) },
                        for: target
                    )
                } else {
                    rows[envKey] = agentEnvDefaultRows(for: target)
                }
            }
            // 已保存但当前列表里没有的存储键仍然要露出来，否则用户改不掉也删不掉。
            for (envKey, entries) in saved.sorted(by: { $0.key < $1.key })
            where !resolved.contains(where: { $0.envKey == envKey }) {
                let catalogAgent = catalogByID[envKey]
                let target = AgentEnvTarget(
                    envKey: envKey,
                    agentId: catalogAgent?.agentId ?? envKey,
                    displayName: catalogAgent.flatMap { $0.displayName.isEmpty ? nil : $0.displayName } ?? envKey,
                    installed: catalogAgent?.installed ?? false,
                    cliExecutableEnv: catalogAgent?.cliExecutableEnv ?? "",
                    cliExecutable: catalogAgent?.definition.bin ?? envKey
                )
                resolved.append(target)
                rows[envKey] = mergeCLISourceRow(
                    entries.map { AgentEnvRow(key: $0.key, value: $0.value, enabled: $0.enabled) },
                    for: target
                )
            }

            targets = resolved
            envMap = rows
            if activeKey.isEmpty || !resolved.contains(where: { $0.envKey == activeKey }) {
                activeKey = resolved.first?.envKey ?? ""
            }
        } catch {
            targets = []
            envMap = [:]
            activeKey = ""
            loadError = agentSettingsErrorDetail(error)
        }
        isLoading = false
    }

    private func save() {
        guard let target = activeTarget else { return }
        isSaving = true
        message = nil
        let entries = (envMap[target.envKey] ?? [])
            .filter { !$0.key.trimmingCharacters(in: .whitespaces).isEmpty }
            .map {
                AgentEnvironmentItem(
                    key: $0.key.trimmingCharacters(in: .whitespaces),
                    value: $0.value,
                    enabled: $0.enabled
                )
            }
        Task { @MainActor in
            do {
                let response = try await model.apiClient().saveAgentEnvVars(
                    agentID: target.agentId,
                    entries: entries
                )
                let warning = response.warning ?? ""
                message = warning.isEmpty
                    ? .success("已保存".localizedForApp)
                    : .failure("已保存".localizedForApp + "\n" + warning)
                await model.refreshAgentCatalog()
            } catch {
                message = .failure(agentSettingsErrorDetail(error))
            }
            isSaving = false
        }
    }
}
