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
    /// ACP 启动命令。空串表示这条只是历史遗留的存储键，没有对应 Agent，也就读不到版本与用量。
    let command: String

    var id: String { envKey }

    init(envKey: String, agentId: String, displayName: String, command: String = "") {
        self.envKey = envKey
        self.agentId = agentId
        self.displayName = displayName
        self.command = command
    }
}

/// 与 Web 端一致的占位默认值：默认关闭，只是把常用的代理变量先摆出来。
private let agentEnvDefaultRows: [AgentEnvRow] = [
    AgentEnvRow(key: "http_proxy", value: "http://127.0.0.1:8890", enabled: false),
    AgentEnvRow(key: "https_proxy", value: "http://127.0.0.1:8890", enabled: false),
]

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

    private var canWrite: Bool { model.can("config.write") }
    private var activeTarget: AgentEnvTarget? { targets.first { $0.envKey == activeKey } }
    private var activeRows: [AgentEnvRow] { envMap[activeKey] ?? [] }

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
        .sheet(isPresented: $showsTargetPicker) {
            QuartetChoiceSheet(
                title: "选择 Agent",
                choices: targets.map { target in
                    .agent(
                        id: target.envKey,
                        title: target.displayName,
                        command: target.envKey,
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
                AgentSettingsCard("ACP 环境变量", systemImage: "key") {
                    agentSettingsHint("这些变量只在启动对应 Agent 的 ACP 进程时注入；关闭的条目会被保留但不生效。")
                    AgentSettingsSelectionRow(
                        title: "Agent",
                        value: activeTarget?.displayName ?? "请选择",
                        placeholder: activeTarget == nil,
                        identifier: "agent-env-target-picker"
                    ) { showsTargetPicker = true }
                    if let activeTarget {
                        AgentSettingsMonoRow(label: "存储键", value: activeTarget.envKey)
                    }
                }

                variablesCard

                if let message {
                    AgentSettingsMessageView(message)
                }
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
                    identifier: "agent-env-save",
                    action: { save() }
                )
            }
        }
    }

    private var variablesCard: some View {
        AgentSettingsCard("变量列表", systemImage: "list.bullet") {
            if activeRows.isEmpty {
                agentSettingsHint("还没有变量。点下面的按钮添加一条 KEY=value。")
            } else {
                ForEach(activeRows) { row in
                    variableRow(row)
                }
            }
            if canWrite {
                Button {
                    quartetDismissKeyboard()
                    envMap[activeKey, default: []].append(AgentEnvRow(key: "", value: "", enabled: true))
                } label: {
                    Label("添加变量", systemImage: "plus")
                        .font(.quartet(.control, weight: .semibold))
                        .foregroundStyle(QuartetTheme.accent)
                        .frame(maxWidth: .infinity)
                        .frame(height: 46)
                        .background(QuartetTheme.accent.opacity(0.1), in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                }
                .buttonStyle(.plain)
                .accessibilityIdentifier("agent-env-add")
            } else {
                agentSettingsHint("当前账号没有 config.write 权限，只能查看环境变量。")
            }
        }
    }

    private func variableRow(_ row: AgentEnvRow) -> some View {
        let binding = bindingFor(row.id)
        return VStack(alignment: .leading, spacing: 8) {
            HStack(spacing: 10) {
                Toggle("", isOn: binding.enabled)
                    .labelsHidden()
                    .tint(QuartetTheme.accent)
                    .disabled(!canWrite)
                    .accessibilityLabel(row.key.isEmpty ? "启用这条变量".localizedForApp : AppLanguage.localizedFormat("启用 %@", row.key))
                TextField("变量名", text: binding.key)
                    .font(.quartet(.control, design: .monospaced))
                    .textInputAutocapitalization(.never)
                    .autocorrectionDisabled()
                    .disabled(!canWrite)
                    .padding(.horizontal, 12)
                    .frame(height: 44)
                    .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                    .accessibilityIdentifier("agent-env-key-field")
                if canWrite {
                    Button {
                        quartetDismissKeyboard()
                        envMap[activeKey]?.removeAll { $0.id == row.id }
                    } label: {
                        Image(systemName: "trash")
                            .font(.quartet(.control, weight: .semibold))
                            .foregroundStyle(QuartetTheme.failed)
                            .frame(width: 44, height: 44)
                            .background(QuartetTheme.failed.opacity(0.1), in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                    }
                    .buttonStyle(.plain)
                    .accessibilityLabel(AppLanguage.localizedFormat("删除变量 %@", row.key))
                }
            }
            TextField("变量值", text: binding.value)
                .font(.quartet(.control, design: .monospaced))
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .disabled(!canWrite)
                .padding(.horizontal, 12)
                .frame(height: 44)
                .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                .accessibilityIdentifier("agent-env-value-field")
        }
        .opacity(row.enabled ? 1 : 0.6)
        .padding(.vertical, 2)
    }

    /// 按行 ID 定位，避免在增删过程中把编辑内容写到别的行上。
    private func bindingFor(_ id: UUID) -> (key: Binding<String>, value: Binding<String>, enabled: Binding<Bool>) {
        let key = Binding<String>(
            get: { envMap[activeKey]?.first { $0.id == id }?.key ?? "" },
            set: { newValue in
                guard let index = envMap[activeKey]?.firstIndex(where: { $0.id == id }) else { return }
                envMap[activeKey]?[index].key = newValue
            }
        )
        let value = Binding<String>(
            get: { envMap[activeKey]?.first { $0.id == id }?.value ?? "" },
            set: { newValue in
                guard let index = envMap[activeKey]?.firstIndex(where: { $0.id == id }) else { return }
                envMap[activeKey]?[index].value = newValue
            }
        )
        let enabled = Binding<Bool>(
            get: { envMap[activeKey]?.first { $0.id == id }?.enabled ?? false },
            set: { newValue in
                guard let index = envMap[activeKey]?.firstIndex(where: { $0.id == id }) else { return }
                envMap[activeKey]?[index].enabled = newValue
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
            async let settingsRequest = model.agentEnvironmentSettings()
            let (agentList, saved) = try await (agentRequest, settingsRequest)

            var resolved: [AgentEnvTarget] = []
            var rows: [String: [AgentEnvRow]] = [:]
            for agent in agentList {
                let envKey = agent.environmentKey
                guard !resolved.contains(where: { $0.envKey == envKey }) else { continue }
                resolved.append(AgentEnvTarget(
                    envKey: envKey,
                    agentId: agent.agentId,
                    displayName: agent.displayName.isEmpty ? agent.agentId : agent.displayName,
                    command: agent.available ? agent.type : ""
                ))
                let entries = saved[envKey] ?? saved[agent.type]
                if let entries, !entries.isEmpty {
                    rows[envKey] = entries.map { AgentEnvRow(key: $0.key, value: $0.value, enabled: $0.enabled) }
                } else {
                    rows[envKey] = agentEnvDefaultRows
                }
            }
            // 已保存但当前列表里没有的存储键仍然要露出来，否则用户改不掉也删不掉。
            for (envKey, entries) in saved.sorted(by: { $0.key < $1.key })
            where !resolved.contains(where: { $0.envKey == envKey }) {
                resolved.append(AgentEnvTarget(envKey: envKey, agentId: envKey, displayName: envKey))
                rows[envKey] = entries.map { AgentEnvRow(key: $0.key, value: $0.value, enabled: $0.enabled) }
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
