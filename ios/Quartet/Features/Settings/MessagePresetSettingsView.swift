import SwiftUI

/// 预置消息的一行。`id` 只用于列表复用，`presetID` 才是提交给后端的稳定 ID。
private struct MessagePresetRow: Identifiable, Equatable {
    let id = UUID()
    var presetID: String
    var name: String
    var content: String
}

/// 配置范围：全部项目、某个工作空间，或工作空间已删除后残留的未绑定配置。
private enum MessagePresetScope: Equatable {
    case global
    case workspace(String)
    case orphan(String)

    static let workspacePrefix = "workspace:"
    static let orphanPrefix = "orphan:"

    var key: String {
        switch self {
        case .global: "global"
        case .workspace(let id): Self.workspacePrefix + id
        case .orphan(let id): Self.orphanPrefix + id
        }
    }

    init(key: String) {
        if key.hasPrefix(Self.workspacePrefix) {
            self = .workspace(String(key.dropFirst(Self.workspacePrefix.count)))
        } else if key.hasPrefix(Self.orphanPrefix) {
            self = .orphan(String(key.dropFirst(Self.orphanPrefix.count)))
        } else {
            self = .global
        }
    }
}

/// 需要弹窗选择的两件事：切换配置范围，以及把未绑定配置重新绑定到某个工作空间。
private enum MessagePresetPicker: String, Identifiable {
    case scope
    case rebindTarget

    var id: String { rawValue }
}

/// 配置范围弹窗的一行。颜色与首页工作空间选择器使用同一套工作空间标识规则。
private struct MessagePresetScopeChoice: Identifiable {
    let id: String
    let title: String
    let detail: String?
    let tint: Color
}

/// 配置范围与首页“选择工作空间”保持同一套标题、容器、行高、标识和选中态。
private struct MessagePresetScopePickerSheet: View {
    @Environment(\.dismiss) private var dismiss

    let choices: [MessagePresetScopeChoice]
    let selectedID: String
    let onSelect: (String) -> Void

    var body: some View {
        NavigationStack {
            ScrollView {
                LazyVStack(spacing: 0) {
                    ForEach(Array(choices.enumerated()), id: \.element.id) { index, choice in
                        if index > 0 {
                            Divider()
                                .overlay(QuartetTheme.divider)
                                .padding(.leading, 62)
                        }
                        choiceRow(choice)
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
                    Text("配置范围".localizedForApp)
                        .font(.quartet(.regular, weight: .semibold))
                        .foregroundStyle(QuartetTheme.primaryText)
                        .accessibilityAddTraits(.isHeader)
                }
            }
        }
    }

    private func choiceRow(_ choice: MessagePresetScopeChoice) -> some View {
        let selected = choice.id == selectedID
        let detail = choice.detail?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""

        return Button {
            onSelect(choice.id)
            dismiss()
        } label: {
            HStack(spacing: 12) {
                Circle()
                    .fill(choice.tint.opacity(0.11))
                    .frame(width: 38, height: 38)
                    .overlay {
                        Circle()
                            .fill(choice.tint)
                            .frame(width: 10, height: 10)
                    }
                    .accessibilityHidden(true)

                VStack(alignment: .leading, spacing: 3) {
                    Text(choice.title.localizedForApp)
                        .font(.quartet(.regular, weight: .semibold))
                        .foregroundStyle(QuartetTheme.primaryText)
                        .lineLimit(1)
                    if !detail.isEmpty {
                        Text(detail.localizedForApp)
                            .font(.quartet(.detail))
                            .foregroundStyle(QuartetTheme.secondaryText)
                            .lineLimit(1)
                            .truncationMode(.middle)
                    }
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
        .accessibilityLabel([choice.title.localizedForApp, detail.localizedForApp].filter { !$0.isEmpty }.joined(separator: "，"))
        .accessibilityValue(selected ? "已选择".localizedForApp : "")
        .accessibilityHint("选择此配置范围并关闭弹窗".localizedForApp)
        .accessibilityIdentifier("message-preset-scope-\(choice.id)")
    }
}

/// 与 Web 端“预置消息”设置一致：维护全部项目或单个项目可复用的消息文本，
/// 并处理工作空间删除后残留的未绑定配置。
@MainActor
struct MessagePresetSettingsView: View {
    /// 与后端 `maxMessagesPerScope` 对齐，超出后后端会直接拒绝保存。
    private static let maxPresetsPerScope = 100

    @EnvironmentObject private var model: AppModel

    @State private var scopeKey = ""
    @State private var revision = "missing"
    @State private var rows: [MessagePresetRow] = []
    @State private var orphans: [OrphanMessagePreset] = []
    @State private var orphanErrors: [String] = []
    @State private var isLoading = true
    @State private var loadError = ""
    @State private var isSaving = false
    @State private var isDirty = false
    @State private var message: AgentSettingsMessage?
    @State private var picker: MessagePresetPicker?
    @State private var stagedScopeKey: String?
    @State private var pendingScopeKey: String?
    @State private var confirmsOrphanDelete = false
    @State private var loadSequence = 0

    private var canReadGlobal: Bool { model.can("config.read") }
    private var canWriteGlobal: Bool { model.can("config.write") }
    private var canReadWorkspace: Bool { model.can("workspace.read") }
    private var canWriteWorkspace: Bool { model.can("workspace.write") }

    private var activeScope: MessagePresetScope { MessagePresetScope(key: scopeKey) }

    private var canWriteScope: Bool {
        switch activeScope {
        case .global: canWriteGlobal
        case .workspace: canWriteWorkspace
        case .orphan: false
        }
    }

    private var isOrphanScope: Bool {
        if case .orphan = activeScope { return true }
        return false
    }

    private var activeOrphan: OrphanMessagePreset? {
        guard case .orphan(let id) = activeScope else { return nil }
        return orphans.first { ($0.config.workspaceId ?? "") == id }
    }

    var body: some View {
        Group {
            if isLoading && rows.isEmpty && scopeKey.isEmpty {
                AgentSettingsLoadingView(title: "正在加载预置消息…")
            } else if scopeChoices.isEmpty {
                emptyState
            } else {
                editor
            }
        }
        .background(QuartetTheme.canvas)
        .task { await initialLoad() }
        .onChange(of: scopeKey) { _, key in
            guard !key.isEmpty else { return }
            Task { await load(scopeKey: key) }
        }
        // 首页还没拉到工作空间列表时进设置页，等列表到达后再选中默认范围。
        .onChange(of: model.workspaces.map(\.id)) { _, _ in
            guard scopeKey.isEmpty else { return }
            scopeKey = defaultScopeKey()
        }
        .sheet(item: $picker, onDismiss: { pickerDidDismiss() }) { picker in
            pickerSheet(picker)
        }
        .alert("放弃未保存的修改？", isPresented: discardAlertBinding) {
            Button("关闭", role: .cancel) { pendingScopeKey = nil }
            Button("放弃修改", role: .destructive) {
                guard let next = pendingScopeKey else { return }
                pendingScopeKey = nil
                message = nil
                scopeKey = next
            }
        } message: {
            Text("当前范围的预置消息有未保存修改，切换配置范围会丢弃这些修改。")
        }
        .alert("删除这份未绑定配置？", isPresented: $confirmsOrphanDelete) {
            Button("关闭", role: .cancel) {}
            Button("确认删除", role: .destructive) { deleteOrphan() }
        } message: {
            Text("配置文件会被删除，且无法恢复。")
        }
    }

    // MARK: - 页面骨架

    private var emptyState: some View {
        ScrollView {
            AgentSettingsCard("预置消息", systemImage: "text.bubble") {
                agentSettingsHint("维护全部项目或指定项目可复用的消息文本。选择后只填入输入框，不会自动发送。")
                agentSettingsHint(canReadGlobal || canReadWorkspace
                    ? "当前还没有可配置的项目，请先在 Web 端创建工作空间。"
                    : "当前账号没有 config.read 或 workspace.read 权限，无法查看预置消息。")
                if !loadError.isEmpty {
                    AgentSettingsMessageView(kind: .failure, text: loadError)
                }
                ForEach(orphanErrors, id: \.self) { error in
                    AgentSettingsMessageView(kind: .failure, text: error)
                }
            }
            .padding(18)
        }
    }

    private var editor: some View {
        ScrollView {
            LazyVStack(spacing: 12) {
                scopeCard
                if let orphan = activeOrphan {
                    orphanActionsCard(orphan)
                }
                ForEach(orphanErrors, id: \.self) { error in
                    AgentSettingsMessageView(kind: .failure, text: error)
                }
                if !loadError.isEmpty {
                    AgentSettingsMessageView(kind: .failure, text: loadError)
                }
                if isLoading {
                    AgentSettingsCard {
                        agentSettingsHint("正在加载预置消息…")
                    }
                } else {
                    presetList
                }
            }
            .padding(.horizontal, 18)
            .padding(.vertical, 12)
        }
        .scrollDismissesKeyboard(.interactively)
        .safeAreaInset(edge: .bottom, spacing: 0) {
            if canWriteScope {
                AgentSettingsSaveBar(
                    title: "保存预置消息",
                    savingTitle: "正在保存…",
                    isSaving: isSaving,
                    isEnabled: isDirty && !isLoading,
                    message: message,
                    identifier: "message-preset-save",
                    action: { save() }
                )
            } else if let message {
                AgentSettingsMessageView(message)
                    .padding(.horizontal, 18)
                    .padding(.vertical, 10)
                    .background(.ultraThinMaterial)
                    .accessibilityIdentifier("message-preset-feedback")
            }
        }
    }

    private var scopeCard: some View {
        AgentSettingsCard("预置消息", systemImage: "text.bubble") {
            agentSettingsHint("维护全部项目或指定项目可复用的消息文本。选择后只填入输入框，不会自动发送。")
            AgentSettingsSelectionRow(
                title: "配置范围",
                value: activeScopeTitle,
                identifier: "message-preset-scope-picker"
            ) { picker = .scope }
            if !activeScopeWorkdir.isEmpty {
                AgentSettingsMonoRow(label: "工作目录", value: activeScopeWorkdir)
            }
            if !canWriteScope && !isOrphanScope {
                agentSettingsHint(readOnlyScopeHint)
            }
        }
    }

    private func orphanActionsCard(_ orphan: OrphanMessagePreset) -> some View {
        AgentSettingsCard("未绑定配置", systemImage: "questionmark.folder") {
            agentSettingsHint("这份配置对应的工作空间已经不存在，只能删除或重新绑定到现有项目，不能直接编辑。")
            if canWriteGlobal && canWriteWorkspace {
                Button {
                    quartetDismissKeyboard()
                    message = nil
                    picker = .rebindTarget
                } label: {
                    Label("重新绑定到项目", systemImage: "arrow.triangle.branch")
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
                .disabled(isSaving || rebindTargets.isEmpty)
                .opacity(isSaving || rebindTargets.isEmpty ? 0.45 : 1)
                .accessibilityIdentifier("message-preset-rebind")
                if rebindTargets.isEmpty {
                    agentSettingsHint("当前没有可绑定的项目。")
                }
            }
            if canWriteGlobal {
                Button {
                    quartetDismissKeyboard()
                    confirmsOrphanDelete = true
                } label: {
                    Label("删除未绑定配置", systemImage: "trash")
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
                .disabled(isSaving)
                .opacity(isSaving ? 0.45 : 1)
                .accessibilityIdentifier("message-preset-delete-orphan")
            } else {
                agentSettingsHint("当前账号没有 config.write 权限，不能删除或重新绑定这份配置。")
            }
            AgentSettingsMonoRow(label: "原工作空间 ID", value: orphan.config.workspaceId ?? "")
        }
    }

    @ViewBuilder
    private var presetList: some View {
        if rows.isEmpty {
            AgentSettingsCard {
                agentSettingsHint("当前范围暂无预置消息")
            }
        } else {
            ForEach(Array(rows.enumerated()), id: \.element.id) { index, row in
                presetCard(row, index: index)
            }
        }
        if canWriteScope {
            AgentSettingsCard {
                Button {
                    quartetDismissKeyboard()
                    addRow()
                } label: {
                    Label("添加预置消息", systemImage: "plus")
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
                .disabled(rows.count >= Self.maxPresetsPerScope)
                .opacity(rows.count >= Self.maxPresetsPerScope ? 0.45 : 1)
                .accessibilityIdentifier("message-preset-add")
                agentSettingsHint(rows.count >= Self.maxPresetsPerScope
                    ? "已达到 100 条上限，先删除一些条目再添加。"
                    : "名称最多 80 个字符，正文最多 32 KB，单个范围最多 100 条。")
            }
        }
    }

    private func presetCard(_ row: MessagePresetRow, index: Int) -> some View {
        let binding = bindingFor(row.id)
        return AgentSettingsCard {
            HStack(spacing: 8) {
                Text(AppLanguage.localizedFormat("第 %d 条", index + 1))
                    .font(.quartet(.detail, weight: .semibold))
                    .foregroundStyle(QuartetTheme.secondaryText)
                Spacer(minLength: 8)
                if canWriteScope {
                    orderButton(
                        systemImage: "arrow.up",
                        label: presetActionLabel("上移预置消息 %@", row: row, index: index),
                        identifier: "message-preset-move-up",
                        disabled: index == 0
                    ) { moveRow(row.id, by: -1) }
                    orderButton(
                        systemImage: "arrow.down",
                        label: presetActionLabel("下移预置消息 %@", row: row, index: index),
                        identifier: "message-preset-move-down",
                        disabled: index == rows.count - 1
                    ) { moveRow(row.id, by: 1) }
                    orderButton(
                        systemImage: "trash",
                        label: presetActionLabel("删除预置消息 %@", row: row, index: index),
                        identifier: "message-preset-delete-row",
                        disabled: false,
                        destructive: true
                    ) { removeRow(row.id) }
                }
            }
            TextField("名称（选填，最多 80 个字符）", text: binding.name)
                .font(.quartet(.control))
                .autocorrectionDisabled()
                .disabled(!canWriteScope)
                .padding(.horizontal, 14)
                .frame(height: 48)
                .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                .accessibilityIdentifier("message-preset-name-field")
            contentEditor(binding.content)
        }
    }

    private func orderButton(
        systemImage: String,
        label: String,
        identifier: String,
        disabled: Bool,
        destructive: Bool = false,
        action: @escaping () -> Void
    ) -> some View {
        Button {
            quartetDismissKeyboard()
            action()
        } label: {
            Image(systemName: systemImage)
                .font(.quartet(.control, weight: .semibold))
                .foregroundStyle(destructive ? QuartetTheme.failed : QuartetTheme.accent)
                .frame(width: 44, height: 44)
                .background(
                    (destructive ? QuartetTheme.failed : QuartetTheme.accent).opacity(0.1),
                    in: RoundedRectangle(cornerRadius: 10, style: .continuous)
                )
        }
        .buttonStyle(.plain)
        .disabled(disabled || isSaving)
        .opacity(disabled || isSaving ? 0.4 : 1)
        .accessibilityLabel(label)
        .accessibilityIdentifier(identifier)
    }

    private func contentEditor(_ text: Binding<String>) -> some View {
        ZStack(alignment: .topLeading) {
            if text.wrappedValue.isEmpty {
                Text("消息正文".localizedForApp)
                    .font(.quartet(.control))
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .padding(.horizontal, 15)
                    .padding(.vertical, 18)
                    .allowsHitTesting(false)
            }
            TextEditor(text: text)
                .font(.quartet(.control))
                .scrollContentBackground(.hidden)
                .disabled(!canWriteScope)
                .padding(10)
                .frame(minHeight: 120)
        }
        .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
        .accessibilityLabel("消息正文".localizedForApp)
        .accessibilityIdentifier("message-preset-content-field")
    }

    // MARK: - 弹窗

    @ViewBuilder
    private func pickerSheet(_ picker: MessagePresetPicker) -> some View {
        switch picker {
        case .scope:
            MessagePresetScopePickerSheet(
                choices: scopeChoices,
                selectedID: scopeKey,
                onSelect: requestScope
            )
            .presentationDetents([.medium, .large])
            .quartetSheetStyle()
        case .rebindTarget:
            QuartetChoiceSheet(
                title: "重新绑定到项目",
                choices: rebindTargets.map { workspace in
                    QuartetChoice(id: workspace.id, title: workspace.displayName, detail: workspace.workdir)
                },
                selection: Binding(get: { "" }, set: { rebind(to: $0) }),
                accessibilityPrefix: "message-preset-rebind-target"
            )
            .presentationDetents([.medium, .large])
            .quartetSheetStyle()
        }
    }

    private var discardAlertBinding: Binding<Bool> {
        Binding(
            get: { pendingScopeKey != nil },
            set: { presented in if !presented { pendingScopeKey = nil } }
        )
    }

    // MARK: - 配置范围

    private var scopeChoices: [MessagePresetScopeChoice] {
        var choices: [MessagePresetScopeChoice] = []
        if canReadGlobal {
            choices.append(MessagePresetScopeChoice(
                id: MessagePresetScope.global.key,
                title: "全部项目",
                detail: "所有项目都能用到的预置消息",
                tint: QuartetTheme.accent
            ))
        }
        if canReadWorkspace {
            for workspace in model.workspaces {
                choices.append(MessagePresetScopeChoice(
                    id: MessagePresetScope.workspace(workspace.id).key,
                    title: workspace.displayName,
                    detail: workspace.workdir,
                    tint: QuartetTheme.workspaceTint(workspace)
                ))
            }
        }
        if canReadGlobal {
            for orphan in orphans {
                let id = orphan.config.workspaceId ?? ""
                choices.append(MessagePresetScopeChoice(
                    id: MessagePresetScope.orphan(id).key,
                    title: orphanTitle(orphan),
                    detail: orphan.config.workspaceWorkdir,
                    tint: QuartetTheme.warning
                ))
            }
        }
        return choices
    }

    private var rebindTargets: [WorkspaceSummary] {
        model.workspaces.filter { workspace in
            !orphans.contains { ($0.config.workspaceId ?? "") == workspace.id }
        }
    }

    private var activeScopeTitle: String {
        switch activeScope {
        case .global:
            return "全部项目"
        case .workspace(let id):
            return model.workspaces.first { $0.id == id }?.displayName ?? id
        case .orphan:
            guard let activeOrphan else { return scopeKey }
            return orphanTitle(activeOrphan)
        }
    }

    private var activeScopeWorkdir: String {
        switch activeScope {
        case .global:
            return ""
        case .workspace(let id):
            return model.workspaces.first { $0.id == id }?.workdir ?? ""
        case .orphan:
            return activeOrphan?.config.workspaceWorkdir ?? ""
        }
    }

    private var readOnlyScopeHint: String {
        if case .global = activeScope {
            return "当前账号没有 config.write 权限，只能查看全部项目的预置消息。"
        }
        return "当前账号没有 workspace.write 权限，只能查看这个项目的预置消息。"
    }

    private func orphanTitle(_ orphan: OrphanMessagePreset) -> String {
        let id = orphan.config.workspaceId ?? ""
        let title = orphan.config.workspaceTitle?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return AppLanguage.localizedFormat("未绑定：%@", title.isEmpty ? id : title)
    }

    private func defaultScopeKey() -> String {
        if canReadGlobal { return MessagePresetScope.global.key }
        if canReadWorkspace, let first = model.workspaces.first {
            return MessagePresetScope.workspace(first.id).key
        }
        return ""
    }

    /// 用户点选范围时先拦一道：有未保存修改就让用户确认是否放弃。
    private func requestScope(_ key: String) {
        guard key != scopeKey else { return }
        guard isDirty else {
            message = nil
            scopeKey = key
            return
        }
        // 确认弹窗要等选择弹窗收起后再出，否则同一时刻的两个 presentation 会互相顶掉。
        stagedScopeKey = key
    }

    private func pickerDidDismiss() {
        guard let staged = stagedScopeKey else { return }
        stagedScopeKey = nil
        pendingScopeKey = staged
    }

    // MARK: - 读取与保存

    private func initialLoad() async {
        guard scopeKey.isEmpty else { return }
        await loadOrphans()
        let initial = defaultScopeKey()
        guard !initial.isEmpty else {
            isLoading = false
            return
        }
        // 赋值会触发 onChange 里的读取，这里不再重复请求。
        scopeKey = initial
    }

    private func loadOrphans() async {
        guard canReadGlobal else {
            orphans = []
            orphanErrors = []
            return
        }
        do {
            let response = try await model.orphanMessagePresets()
            orphans = response.configs ?? []
            orphanErrors = (response.errors ?? []).map { "\($0.file): \($0.error)" }
        } catch {
            orphans = []
            orphanErrors = [agentSettingsErrorDetail(error)]
        }
    }

    private func load(scopeKey key: String) async {
        loadSequence += 1
        let sequence = loadSequence
        isLoading = true
        loadError = ""
        do {
            switch MessagePresetScope(key: key) {
            case .global:
                let response = try await model.globalMessagePresets()
                guard sequence == loadSequence else { return }
                apply(response.revision, messages: response.config.messages ?? [])
            case .workspace(let id):
                let response = try await model.workspaceMessagePresets(workspaceID: id)
                guard sequence == loadSequence else { return }
                apply(response.revision, messages: response.config.messages ?? [])
            case .orphan(let id):
                guard let orphan = orphans.first(where: { ($0.config.workspaceId ?? "") == id }) else {
                    throw APIError(
                        summary: "未绑定配置不存在",
                        detail: "未绑定配置不存在或已被修改，请刷新后重试。\n工作空间 ID：\(id)"
                    )
                }
                apply(orphan.revision, messages: orphan.config.messages ?? [])
            }
        } catch {
            guard sequence == loadSequence else { return }
            revision = "missing"
            rows = []
            isDirty = false
            loadError = agentSettingsErrorDetail(error)
        }
        guard sequence == loadSequence else { return }
        isLoading = false
    }

    private func apply(_ newRevision: String, messages: [MessagePreset]) {
        revision = newRevision
        rows = messages.map { preset in
            MessagePresetRow(presetID: preset.id, name: preset.name ?? "", content: preset.content)
        }
        isDirty = false
    }

    private func save() {
        guard canWriteScope else { return }
        // 范围和修订都在进 Task 前取好：保存过程中用户切换范围时，不能把两个范围的状态混起来。
        let scope = activeScope
        let savedRevision = revision
        let messages = rows.map { row in
            MessagePreset(
                id: row.presetID,
                name: row.name.trimmingCharacters(in: .whitespacesAndNewlines),
                content: row.content
            )
        }
        isSaving = true
        message = nil
        Task { @MainActor in
            do {
                let response: MessagePresetScopeResponse
                switch scope {
                case .global:
                    response = try await model.saveGlobalMessagePresets(
                        revision: savedRevision,
                        messages: messages
                    )
                case .workspace(let id):
                    response = try await model.saveWorkspaceMessagePresets(
                        workspaceID: id,
                        revision: savedRevision,
                        messages: messages
                    )
                case .orphan:
                    isSaving = false
                    return
                }
                // 保存过程中用户切走了范围就只提示结果，不再把响应写回当前列表。
                if scope == activeScope {
                    apply(response.revision, messages: response.config.messages ?? [])
                }
                message = .success("预置消息已保存".localizedForApp)
                await loadOrphans()
            } catch {
                message = .failure(agentSettingsErrorDetail(error))
            }
            isSaving = false
        }
    }

    private func deleteOrphan() {
        guard canWriteGlobal, case .orphan(let id) = activeScope, let orphan = activeOrphan else { return }
        isSaving = true
        message = nil
        Task { @MainActor in
            do {
                try await model.deleteOrphanMessagePresets(workspaceID: id, revision: orphan.revision)
                isDirty = false
                await loadOrphans()
                message = .success("未绑定配置已删除".localizedForApp)
                let next = defaultScopeKey()
                if next.isEmpty {
                    scopeKey = ""
                    rows = []
                    revision = "missing"
                } else {
                    scopeKey = next
                }
            } catch {
                message = .failure(agentSettingsErrorDetail(error))
            }
            isSaving = false
        }
    }

    private func rebind(to targetWorkspaceID: String) {
        guard canWriteGlobal, canWriteWorkspace, !targetWorkspaceID.isEmpty,
              case .orphan(let id) = activeScope, let orphan = activeOrphan else { return }
        isSaving = true
        message = nil
        Task { @MainActor in
            do {
                // 后端只接受目标项目还没有预置消息的情况，先探一次避免把用户的配置覆盖掉。
                let target = try await model.workspaceMessagePresets(workspaceID: targetWorkspaceID)
                guard target.revision == "missing" else {
                    message = .failure("目标项目已经有预置消息，请先处理目标配置后再重新绑定。".localizedForApp)
                    isSaving = false
                    return
                }
                try await model.rebindOrphanMessagePresets(
                    workspaceID: id,
                    revision: orphan.revision,
                    targetWorkspaceID: targetWorkspaceID
                )
                isDirty = false
                await loadOrphans()
                message = .success("未绑定配置已重新绑定".localizedForApp)
                scopeKey = MessagePresetScope.workspace(targetWorkspaceID).key
            } catch {
                message = .failure(agentSettingsErrorDetail(error))
            }
            isSaving = false
        }
    }

    // MARK: - 条目编辑

    private func addRow() {
        guard canWriteScope, rows.count < Self.maxPresetsPerScope else { return }
        rows.append(MessagePresetRow(presetID: "preset-\(UUID().uuidString)", name: "", content: ""))
        isDirty = true
        message = nil
    }

    private func removeRow(_ id: UUID) {
        guard canWriteScope else { return }
        rows.removeAll { $0.id == id }
        isDirty = true
        message = nil
    }

    private func moveRow(_ id: UUID, by offset: Int) {
        guard canWriteScope, let index = rows.firstIndex(where: { $0.id == id }) else { return }
        let target = index + offset
        guard rows.indices.contains(target) else { return }
        rows.swapAt(index, target)
        isDirty = true
        message = nil
    }

    /// 按行 ID 定位，避免在增删和排序过程中把编辑内容写到别的行上。
    private func bindingFor(_ id: UUID) -> (name: Binding<String>, content: Binding<String>) {
        let name = Binding<String>(
            get: { rows.first { $0.id == id }?.name ?? "" },
            set: { newValue in
                guard let index = rows.firstIndex(where: { $0.id == id }) else { return }
                rows[index].name = newValue
                isDirty = true
                message = nil
            }
        )
        let content = Binding<String>(
            get: { rows.first { $0.id == id }?.content ?? "" },
            set: { newValue in
                guard let index = rows.firstIndex(where: { $0.id == id }) else { return }
                rows[index].content = newValue
                isDirty = true
                message = nil
            }
        )
        return (name, content)
    }

    private func presetActionLabel(_ format: String, row: MessagePresetRow, index: Int) -> String {
        let name = row.name.trimmingCharacters(in: .whitespacesAndNewlines)
        let subject = name.isEmpty ? AppLanguage.localizedFormat("第 %d 条", index + 1) : name
        return AppLanguage.localizedFormat(format, subject)
    }
}
