import SwiftUI
import UIKit

// MARK: - 状态载体

enum AgentCatalogAction: String, Sendable {
    case install
    case upgrade
    case uninstall

    var progressTitle: String {
        switch self {
        case .install: "正在安装 Agent…"
        case .upgrade: "正在升级 Agent…"
        case .uninstall: "正在卸载 Agent…"
        }
    }

    var name: String {
        switch self {
        case .install: "安装".localizedForApp
        case .upgrade: "升级".localizedForApp
        case .uninstall: "卸载".localizedForApp
        }
    }
}

private struct AgentCatalogBusy: Equatable {
    let agentId: String
    let action: AgentCatalogAction
}

private struct AgentCatalogBatchProgress: Equatable {
    let completed: Int
    let total: Int
    let currentName: String
}

private struct AgentCatalogActionFailure: Identifiable {
    let agentId: String
    let displayName: String
    let detail: String

    var id: String { agentId }
}

/// 一次安装 / 升级 / 卸载的完整结果。刷新目录后仍然保留，方便继续查看步骤输出。
private struct AgentCatalogActionResult: Identifiable {
    let id = UUID()
    let agentId: String
    let displayName: String
    let action: AgentCatalogAction
    let result: AgentInstallResult

    var ok: Bool {
        switch action {
        case .uninstall:
            return !result.installed && result.stepsSucceeded
        case .install, .upgrade:
            return result.installed && result.stepsSucceeded && (result.validation?.ok ?? false)
        }
    }

    var summary: String {
        switch action {
        case .uninstall:
            return ok ? "卸载成功".localizedForApp : "卸载失败".localizedForApp
        case .install:
            if ok { return "安装成功".localizedForApp }
            return result.installed ? "安装完成，但校验失败".localizedForApp : "安装失败".localizedForApp
        case .upgrade:
            if ok { return "升级成功".localizedForApp }
            if result.installed && result.stepsSucceeded { return "升级完成，但校验失败".localizedForApp }
            return "升级失败".localizedForApp
        }
    }
}

private struct AgentCatalogCheckFeedback {
    enum Status {
        case checking
        case success
        case warning
        case failure
    }

    let status: Status
    let message: String

    var title: String {
        switch status {
        case .checking: "正在检查"
        case .success: "检查通过"
        case .warning: "检查有告警"
        case .failure: "检查失败"
        }
    }
}

/// 需要二次确认的目录操作。确认弹窗只携带意图，真正的执行留在页面里。
private enum AgentCatalogIntent: Hashable {
    case upgrade(agentId: String)
    case upgradeAll(agentIds: [String])
    case uninstall(agentId: String)
    case delete(agentId: String, force: Bool)
}

private struct AgentCatalogConfirmation: Identifiable, Hashable {
    let intent: AgentCatalogIntent
    let title: String
    let message: String
    let confirmTitle: String
    let cancelTitle: String
    let destructive: Bool

    var id: AgentCatalogIntent { intent }
}

/// 自定义 Agent 表单。`agentId` 为空表示新建；`restore` 表示恢复一条已删除记录。
struct CustomAgentFormState: Identifiable {
    let id = UUID()
    var agentId: String?
    var restore: Bool = false
    var displayName: String = ""
    var iconURL: String = ""
    var bin: String = ""
    var acpProgram: String = ""
    var argsText: String = ""
    var supportsHeadlessPrint: Bool = false
    var environmentText: String = ""

    var canEditEnvironment: Bool { agentId == nil || restore }

    var title: String {
        guard agentId != nil else { return "新增自定义 Agent" }
        return restore ? "恢复自定义 Agent" : "编辑自定义 Agent"
    }
}

private enum AgentCatalogPresentation: Identifiable {
    case actions(AgentCatalogItem)
    case editor(CustomAgentFormState)
    case confirmation(AgentCatalogConfirmation)
    case result(AgentCatalogActionResult)
    case deleteOutcome(AgentDeleteResult, String)
    case error(PresentedError)

    var id: String {
        switch self {
        case .actions(let item): "actions-\(item.agentId)"
        case .editor(let form): "editor-\(form.id)"
        case .confirmation(let confirmation): "confirmation-\(confirmation.intent)"
        case .result(let result): "result-\(result.id)"
        case .deleteOutcome(_, let agentId): "delete-outcome-\(agentId)"
        case .error(let error): "error-\(error.id)"
        }
    }
}

// MARK: - Agent 目录

@MainActor
struct AgentCatalogSettingsView: View {
    @EnvironmentObject private var model: AppModel

    @State private var items: [AgentCatalogItem] = []
    @State private var versions: [String: AgentVersionInfo] = [:]
    @State private var versionsCheckedAt: Int64?
    @State private var isLoading = true
    @State private var loadError = ""
    @State private var isCheckingVersions = false
    @State private var versionError = ""
    @State private var busy: AgentCatalogBusy?
    @State private var batchProgress: AgentCatalogBatchProgress?
    @State private var results: [String: AgentCatalogActionResult] = [:]
    @State private var actionFailures: [String: AgentCatalogActionFailure] = [:]
    @State private var checkFeedback: [String: AgentCatalogCheckFeedback] = [:]
    @State private var revisions: [String: [AgentRuntimeRevision]] = [:]
    @State private var expandedRevisions: Set<String> = []
    @State private var statusMessage: String?
    @State private var batchMessage: AgentSettingsMessage?
    @State private var pendingAgentId: String?
    @State private var presentation: AgentCatalogPresentation?
    @State private var presentationIsActive = false
    @State private var pendingPresentation: AgentCatalogPresentation?

    private var canManage: Bool { model.can("agent.manage") }

    var body: some View {
        Group {
            if isLoading && items.isEmpty {
                AgentSettingsLoadingView(title: "正在加载 Agent 目录…")
            } else if !loadError.isEmpty && items.isEmpty {
                AgentSettingsLoadFailure(detail: loadError) { Task { await load() } }
            } else {
                list
            }
        }
        .background(QuartetTheme.canvas)
        .task { await initialLoad() }
        .sheet(item: $presentation, onDismiss: { presentationDidDismiss() }) { presentation in
            presentationSheet(presentation)
        }
    }

    private func actionsSheet(_ item: AgentCatalogItem) -> some View {
        AgentCatalogActionsSheet(
            item: item,
            version: versions[item.agentId],
            canManage: canManage,
            installLocked: busy != nil,
            pending: pendingAgentId != nil,
            result: results[item.agentId].map { AgentCatalogResultBadge(summary: $0.summary, ok: $0.ok) },
            onInstall: {
                presentation = nil
                install(item)
            },
            onUpgrade: { requestUpgrade(item) },
            onUninstall: { requestUninstall(item) },
            onRevalidate: {
                presentation = nil
                revalidate(item)
            },
            onEdit: { queue(.editor(editorForm(item))) },
            onDelete: { force in
                presentation = nil
                requestDelete(item, force: force)
            },
            onRestore: { queue(.editor(editorForm(item, restore: true))) },
            onInspectResult: {
                guard let result = results[item.agentId] else { return }
                queue(.result(result))
            }
        )
        .presentationDetents([.medium, .large])
        .quartetSheetStyle()
    }

    @ViewBuilder
    private func presentationSheet(_ presentation: AgentCatalogPresentation) -> some View {
        switch presentation {
        case .actions(let item):
            actionsSheet(item)
        case .editor(let form):
            CustomAgentEditorSheet(form: form) { submitted in
                try await submitCustomAgent(submitted)
            }
            .presentationDetents([.large])
            .quartetSheetStyle()
        case .confirmation(let confirmation):
            AgentCatalogConfirmationSheet(confirmation: confirmation) { intent in
                perform(intent)
            }
            .presentationDetents([.medium, .large])
            .quartetSheetStyle()
        case .result(let result):
            AgentInstallResultSheet(
                title: result.displayName,
                summary: result.summary,
                ok: result.ok,
                result: result.result
            )
            .presentationDetents([.large])
            .quartetSheetStyle()
        case .deleteOutcome(let outcome, _):
            AgentDeleteOutcomeSheet(outcome: outcome)
                .presentationDetents([.medium, .large])
                .quartetSheetStyle()
        case .error(let error):
            AgentSettingsErrorSheet(error: error)
                .presentationDetents([.medium, .large])
                .quartetSheetStyle()
        }
    }

    private var list: some View {
        ScrollView {
            LazyVStack(spacing: 12) {
                toolbar
                if !loadError.isEmpty {
                    AgentSettingsMessageView(kind: .failure, text: loadError)
                }
                if !versionError.isEmpty {
                    AgentSettingsMessageView(kind: .failure, text: versionError)
                }
                if let statusMessage {
                    AgentSettingsMessageView(kind: .success, text: statusMessage)
                }
                if let batchMessage {
                    AgentSettingsMessageView(batchMessage)
                }
                if let batchProgress {
                    AgentSettingsMessageView(
                        kind: .info,
                        text: AppLanguage.localizedFormat(
                            "正在更新 %@（%d/%d）",
                            batchProgress.currentName,
                            min(batchProgress.completed + 1, batchProgress.total),
                            batchProgress.total
                        )
                    )
                    .accessibilityIdentifier("agent-catalog-upgrade-all-progress")
                }
                ForEach(actionFailures.keys.sorted(), id: \.self) { agentId in
                    if let failure = actionFailures[agentId] {
                        AgentSettingsMessageView(
                            kind: .failure,
                            text: "\(failure.displayName)\n\(failure.detail)"
                        )
                    }
                }
                ForEach(sortedResults) { result in
                    resultBanner(result)
                }
                if items.isEmpty {
                    AgentSettingsCard {
                        agentSettingsHint("Agent 目录为空。安装内置 Agent 或新增一个自定义 Agent 后会出现在这里。")
                    }
                }
                ForEach(items) { item in
                    card(item)
                }
            }
            .padding(.horizontal, 18)
            .padding(.vertical, 12)
        }
        .refreshable {
            guard busy == nil, pendingAgentId == nil, batchProgress == nil, !isCheckingVersions else { return }
            await load(showLoading: false)
            await loadVersions(force: false)
        }
        .onChange(of: batchProgress) { _, progress in
            guard UIAccessibility.isVoiceOverRunning, let progress else { return }
            UIAccessibility.post(notification: .announcement, argument: batchProgressText(progress))
        }
        .onChange(of: batchMessage) { _, message in
            guard UIAccessibility.isVoiceOverRunning, let message else { return }
            UIAccessibility.post(notification: .announcement, argument: message.text)
        }
    }

    private var sortedResults: [AgentCatalogActionResult] {
        results.keys.sorted().compactMap { results[$0] }
    }

    private var updatableCount: Int {
        upgradeCandidates.count
    }

    private var upgradeCandidates: [AgentCatalogItem] {
        items.filter { item in
            guard item.isBuiltin, !item.deprecated, item.installed,
                  let version = versions[item.agentId] else { return false }
            return version.updateAvailable && version.upgradeSupported
        }
    }

    private func batchProgressText(_ progress: AgentCatalogBatchProgress) -> String {
        AppLanguage.localizedFormat(
            "正在更新 %@（%d/%d）",
            progress.currentName,
            min(progress.completed + 1, progress.total),
            progress.total
        )
    }

    // MARK: 工具条

    private var toolbar: some View {
        AgentSettingsCard("Agent 目录", systemImage: "square.grid.2x2") {
            agentSettingsHint("内置 Agent 只能按目录预置的流程安装、升级和卸载；自定义 Agent 由本机手动登记的启动命令组成。")
            HStack(spacing: 10) {
                Text(AppLanguage.localizedFormat("已登记 %d 个", items.count))
                    .font(.quartet(.detail, weight: .semibold))
                    .foregroundStyle(QuartetTheme.secondaryText)
                if updatableCount > 0 {
                    Text(AppLanguage.localizedFormat("%d 个可更新", updatableCount))
                        .font(.quartet(.detail, weight: .semibold))
                        .foregroundStyle(QuartetTheme.warning)
                }
                Spacer(minLength: 0)
            }
            if let versionsCheckedAt {
                agentSettingsHint(AppLanguage.localizedFormat(
                    "版本检查时间：%@",
                    agentSettingsTimestamp(versionsCheckedAt)
                ))
            }
            toolbarButtons
        }
    }

    private var toolbarButtons: some View {
        VStack(spacing: 10) {
            HStack(spacing: 10) {
                Button {
                    Task { await loadVersions(force: true) }
                } label: {
                    HStack(spacing: 6) {
                        if isCheckingVersions {
                            ProgressView().controlSize(.small).tint(QuartetTheme.primaryText)
                        }
                        Text((isCheckingVersions ? "正在检查更新…" : "检查更新").localizedForApp)
                    }
                    .font(.quartet(.control, weight: .semibold))
                    .foregroundStyle(QuartetTheme.primaryText)
                    .frame(maxWidth: .infinity)
                    .frame(height: 46)
                    .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                }
                .buttonStyle(.plain)
                .disabled(isCheckingVersions || busy != nil || batchProgress != nil)
                .opacity(isCheckingVersions || busy != nil || batchProgress != nil ? 0.5 : 1)
                .accessibilityIdentifier("agent-catalog-check-versions")

                if canManage {
                    Button { requestUpgradeAll() } label: {
                        HStack(spacing: 6) {
                            if batchProgress != nil {
                                ProgressView().controlSize(.small).tint(QuartetTheme.onAccent)
                            }
                            Text((batchProgress == nil ? "更新全部" : "正在更新…").localizedForApp)
                        }
                        .font(.quartet(.control, weight: .semibold))
                        .foregroundStyle(QuartetTheme.onAccent)
                        .frame(maxWidth: .infinity)
                        .frame(height: 46)
                        .background(QuartetTheme.warning, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                    }
                    .buttonStyle(.plain)
                    .disabled(upgradeCandidates.isEmpty || isCheckingVersions || busy != nil || pendingAgentId != nil || batchProgress != nil)
                    .opacity(upgradeCandidates.isEmpty || isCheckingVersions || busy != nil || pendingAgentId != nil || batchProgress != nil ? 0.5 : 1)
                    .accessibilityIdentifier("agent-catalog-upgrade-all")
                }
            }

            if canManage {
                Button {
                    present(.editor(CustomAgentFormState()))
                } label: {
                    Text("新增自定义 Agent")
                        .font(.quartet(.control, weight: .semibold))
                        .foregroundStyle(QuartetTheme.onAccent)
                        .frame(maxWidth: .infinity)
                        .frame(height: 46)
                        .background(QuartetTheme.accent, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                }
                .buttonStyle(.plain)
                .disabled(busy != nil || pendingAgentId != nil || batchProgress != nil)
                .opacity(busy != nil || pendingAgentId != nil || batchProgress != nil ? 0.5 : 1)
                .accessibilityIdentifier("agent-catalog-add-custom")
            }
        }
    }

    // MARK: 卡片

    private func card(_ item: AgentCatalogItem) -> some View {
        AgentSettingsCard {
            cardHeader(item)
            agentSettingsDivider()
            cardIdentity(item)
            cardDiagnostics(item)
            if canUpgrade(item) {
                Button { requestUpgrade(item) } label: {
                    Label("升级 Agent", systemImage: "arrow.up.circle.fill")
                        .font(.quartet(.control, weight: .semibold))
                        .foregroundStyle(QuartetTheme.onAccent)
                        .frame(maxWidth: .infinity)
                        .frame(height: 44)
                        .background(QuartetTheme.warning, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                }
                .buttonStyle(.plain)
                .disabled(busy != nil || pendingAgentId != nil || batchProgress != nil)
                .opacity(busy != nil || pendingAgentId != nil || batchProgress != nil ? 0.5 : 1)
                .accessibilityIdentifier("agent-catalog-upgrade-\(item.agentId)")
            }
            revisionSection(item)
        }
    }

    private func canUpgrade(_ item: AgentCatalogItem) -> Bool {
        guard canManage, item.isBuiltin, !item.deprecated, item.installed,
              let version = versions[item.agentId] else { return false }
        return version.updateAvailable && version.upgradeSupported
    }

    @ViewBuilder
    private func cardHeader(_ item: AgentCatalogItem) -> some View {
        HStack(alignment: .top, spacing: 10) {
            VStack(alignment: .leading, spacing: 6) {
                Text(item.displayName.isEmpty ? item.agentId : item.displayName)
                    .font(.quartet(.regular, weight: .semibold))
                    .foregroundStyle(QuartetTheme.primaryText)
                    .frame(maxWidth: .infinity, alignment: .leading)
                HStack(spacing: 6) {
                    badge(item.sourceLabel, tint: item.isCustom ? QuartetTheme.accentDeep : QuartetTheme.secondaryText)
                    if item.isBuiltin, let method = item.installMethodLabel {
                        badge(method, tint: QuartetTheme.secondaryText)
                    }
                    badge(item.availabilityLabel, tint: availabilityTint(item))
                    Spacer(minLength: 0)
                }
            }
            Button { present(.actions(item)) } label: {
                Image(systemName: "ellipsis.circle")
                    .font(.title3)
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .frame(width: 44, height: 44)
                    .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .frame(width: 44, height: 44)
            .contentShape(Rectangle())
            .disabled(busy != nil || pendingAgentId != nil || batchProgress != nil)
            .opacity(busy != nil || pendingAgentId != nil || batchProgress != nil ? 0.45 : 1)
            .accessibilityLabel(AppLanguage.localizedFormat("%@ 的 Agent 操作", item.displayName))
            .accessibilityIdentifier("agent-catalog-more-\(item.agentId)")
        }
        if item.refreshing {
            HStack(spacing: 6) {
                ProgressView().controlSize(.small).tint(QuartetTheme.secondaryText)
                Text("正在后台刷新校验结果")
                    .font(.quartet(.detail))
                    .foregroundStyle(QuartetTheme.secondaryText)
            }
        }
    }

    @ViewBuilder
    private func cardIdentity(_ item: AgentCatalogItem) -> some View {
        AgentSettingsMonoRow(label: "Agent ID", value: item.agentId)
        if !item.definition.commandLine.isEmpty {
            AgentSettingsMonoRow(label: "启动命令", value: item.definition.commandLine)
        }
        if !item.definition.bin.isEmpty {
            AgentSettingsMonoRow(label: "可执行文件", value: item.definition.bin)
        }
        if let revision = item.currentRevision, !revision.isEmpty {
            AgentSettingsMonoRow(label: "当前修订", value: revision)
        }
        if !item.installCommands.isEmpty {
            AgentSettingsMonoRow(label: "安装命令", value: item.installCommands.joined(separator: "\n"))
        }
        if item.supportsHeadlessPrint {
            agentSettingsHint("支持 bin -p 单次执行，可用于标题生成和群回复角色。")
        }
    }

    @ViewBuilder
    private func cardDiagnostics(_ item: AgentCatalogItem) -> some View {
        if let instructions = item.installInstructions,
           !instructions.isEmpty,
           item.isBuiltin, !item.deprecated, !item.installed, !item.autoInstallable {
            agentSettingsHint(instructions)
        }
        if let detail = item.availabilityError ?? item.deleteError, !detail.isEmpty {
            AgentSettingsMessageView(kind: .failure, text: detail)
        }
        if item.availability == "not_installed",
           let detail = item.lastValidationError, !detail.isEmpty {
            AgentSettingsMessageView(kind: .failure, text: detail)
        }
        if let status = item.lastValidationStatus, let at = item.lastValidationAt, at > 0 {
            agentSettingsHint(AppLanguage.localizedFormat(
                "最近校验：%@ · %@",
                validationStatusLabel(status),
                agentSettingsTimestamp(at)
            ))
        }
        if let feedback = checkFeedback[item.agentId] {
            checkFeedbackView(feedback)
        }
        if item.installed, let version = versions[item.agentId] {
            versionPanel(version)
        } else if item.installed && isCheckingVersions {
            HStack(spacing: 6) {
                ProgressView().controlSize(.small).tint(QuartetTheme.secondaryText)
                Text("正在检查版本…")
                    .font(.quartet(.detail))
                    .foregroundStyle(QuartetTheme.secondaryText)
            }
        }
        if let busy, busy.agentId == item.agentId {
            busyIndicator(busy)
        }
    }

    private func busyIndicator(_ busy: AgentCatalogBusy) -> some View {
        HStack(spacing: 8) {
            ProgressView().controlSize(.small).tint(QuartetTheme.accent)
            VStack(alignment: .leading, spacing: 2) {
                Text(LocalizedStringKey(busy.action.progressTitle))
                    .font(.quartet(.detail, weight: .semibold))
                    .foregroundStyle(QuartetTheme.primaryText)
                Text("请求会一直等到命令跑完，请不要退出页面。")
                    .font(.quartet(.detail))
                    .foregroundStyle(QuartetTheme.secondaryText)
            }
            Spacer(minLength: 0)
        }
        .frame(maxWidth: .infinity, alignment: .leading)
        .accessibilityElement(children: .combine)
    }

    private func badge(_ text: String, tint: Color) -> some View {
        Text(text.localizedForApp)
            .font(.quartet(.compact, weight: .semibold))
            .foregroundStyle(tint)
            .padding(.horizontal, 8)
            .padding(.vertical, 4)
            .background(tint.opacity(0.12), in: Capsule())
    }

    private func availabilityTint(_ item: AgentCatalogItem) -> Color {
        switch item.availability {
        case "available": QuartetTheme.success
        case "unavailable", "deleted": QuartetTheme.failed
        case "not_installed", "deprecated": QuartetTheme.secondaryText
        case "pending_validation", "validating", "deleting": QuartetTheme.warning
        default: QuartetTheme.secondaryText
        }
    }

    private func validationStatusLabel(_ status: String) -> String {
        switch status {
        case "available": "可用".localizedForApp
        case "unavailable": "不可用".localizedForApp
        default: status
        }
    }

    private func checkFeedbackView(_ feedback: AgentCatalogCheckFeedback) -> some View {
        let tint: Color = switch feedback.status {
        case .checking: QuartetTheme.secondaryText
        case .success: QuartetTheme.success
        case .warning: QuartetTheme.warning
        case .failure: QuartetTheme.failed
        }
        return HStack(alignment: .top, spacing: 8) {
            if feedback.status == .checking {
                ProgressView().controlSize(.small).tint(tint)
            } else {
                Image(systemName: feedback.status == .success
                    ? "checkmark.circle.fill"
                    : feedback.status == .warning ? "exclamationmark.circle.fill" : "xmark.circle.fill")
                    .font(.quartet(.control, weight: .semibold))
                    .foregroundStyle(tint)
                    .accessibilityHidden(true)
            }
            VStack(alignment: .leading, spacing: 3) {
                Text(LocalizedStringKey(feedback.title))
                    .font(.quartet(.detail, weight: .semibold))
                    .foregroundStyle(tint)
                Text(feedback.message)
                    .font(.quartet(.detail, design: .monospaced))
                    .foregroundStyle(QuartetTheme.primaryText)
                    .textSelection(.enabled)
                    .fixedSize(horizontal: false, vertical: true)
            }
            Spacer(minLength: 0)
        }
        .padding(12)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(tint.opacity(0.08), in: RoundedRectangle(cornerRadius: 14, style: .continuous))
    }

    private func versionPanel(_ version: AgentVersionInfo) -> some View {
        let tint: Color = version.updateAvailable
            ? QuartetTheme.warning
            : version.hasKnownLatest ? QuartetTheme.success : QuartetTheme.secondaryText
        let title = version.updateAvailable
            ? "有新版本可用"
            : version.hasKnownLatest ? "已是最新版本" : "只读到本机版本"
        return VStack(alignment: .leading, spacing: 8) {
            Text(LocalizedStringKey(title))
                .font(.quartet(.detail, weight: .semibold))
                .foregroundStyle(tint)
            ForEach(version.components) { component in
                versionComponentRow(component)
            }
        }
        .padding(12)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(tint.opacity(0.08), in: RoundedRectangle(cornerRadius: 14, style: .continuous))
    }

    private func versionComponentRow(_ component: AgentVersionComponent) -> some View {
        VStack(alignment: .leading, spacing: 3) {
            HStack(spacing: 6) {
                Text(component.name)
                    .font(.quartet(.detail, design: .monospaced))
                    .foregroundStyle(QuartetTheme.primaryText)
                Text(component.kind)
                    .font(.quartet(.compact, weight: .semibold))
                    .foregroundStyle(QuartetTheme.secondaryText)
                Spacer(minLength: 0)
                Text(componentVersionText(component))
                    .font(.quartet(.detail, design: .monospaced))
                    .foregroundStyle(component.updateAvailable ? QuartetTheme.warning : QuartetTheme.secondaryText)
            }
            if let detail = component.error, !detail.isEmpty {
                Text(detail)
                    .font(.quartet(.compact, design: .monospaced))
                    .foregroundStyle(QuartetTheme.failed)
                    .textSelection(.enabled)
                    .fixedSize(horizontal: false, vertical: true)
            }
        }
        .accessibilityElement(children: .combine)
    }

    private func componentVersionText(_ component: AgentVersionComponent) -> String {
        let current = component.currentVersion.flatMap { $0.isEmpty ? nil : $0 } ?? "未安装".localizedForApp
        guard component.updateAvailable, let latest = component.latestVersion, !latest.isEmpty else {
            return current
        }
        return "\(current) → \(latest)"
    }

    @ViewBuilder
    private func revisionSection(_ item: AgentCatalogItem) -> some View {
        let expanded = expandedRevisions.contains(item.agentId)
        Button {
            if expanded {
                expandedRevisions.remove(item.agentId)
            } else {
                expandedRevisions.insert(item.agentId)
                Task { await loadRevisions(item.agentId) }
            }
        } label: {
            HStack(spacing: 6) {
                Text("修订历史")
                    .font(.quartet(.detail, weight: .semibold))
                    .foregroundStyle(QuartetTheme.accent)
                Image(systemName: expanded ? "chevron.up" : "chevron.down")
                    .font(.quartet(.compact, weight: .bold))
                    .foregroundStyle(QuartetTheme.accent)
                Spacer(minLength: 0)
            }
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier("agent-catalog-revisions-\(item.agentId)")

        if expanded {
            let list = revisions[item.agentId] ?? []
            if list.isEmpty {
                agentSettingsHint("暂无修订记录。")
            } else {
                VStack(alignment: .leading, spacing: 8) {
                    ForEach(list) { revision in
                        VStack(alignment: .leading, spacing: 3) {
                            Text(revision.revision)
                                .font(.quartet(.compact, design: .monospaced))
                                .foregroundStyle(QuartetTheme.secondaryText)
                            Text(revision.definition.commandLine)
                                .font(.quartet(.detail, design: .monospaced))
                                .foregroundStyle(QuartetTheme.primaryText)
                                .textSelection(.enabled)
                                .fixedSize(horizontal: false, vertical: true)
                        }
                        .frame(maxWidth: .infinity, alignment: .leading)
                    }
                }
                .padding(12)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
            }
        }
    }

    private func resultBanner(_ result: AgentCatalogActionResult) -> some View {
        let tint = result.ok ? QuartetTheme.success : QuartetTheme.failed
        return HStack(alignment: .top, spacing: 10) {
            Image(systemName: result.ok ? "checkmark.circle.fill" : "xmark.circle.fill")
                .font(.quartet(.control, weight: .semibold))
                .foregroundStyle(tint)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 4) {
                Text("\(result.displayName) · \(result.summary)")
                    .font(.quartet(.detail, weight: .semibold))
                    .foregroundStyle(tint)
                    .fixedSize(horizontal: false, vertical: true)
                Button("查看命令输出") { present(.result(result)) }
                    .font(.quartet(.detail, weight: .semibold))
                    .foregroundStyle(QuartetTheme.accent)
                    .buttonStyle(.plain)
                    .accessibilityIdentifier("agent-catalog-result-detail-\(result.agentId)")
            }
            Spacer(minLength: 0)
            Button {
                results.removeValue(forKey: result.agentId)
            } label: {
                Image(systemName: "xmark")
                    .font(.quartet(.detail, weight: .bold))
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .frame(width: 32, height: 32)
            }
            .buttonStyle(.plain)
            .accessibilityLabel("关闭结果提示")
        }
        .padding(12)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(tint.opacity(0.08), in: RoundedRectangle(cornerRadius: 14, style: .continuous))
        .accessibilityIdentifier("agent-catalog-result-\(result.agentId)")
    }

    // MARK: 弹窗调度

    /// 操作弹窗必须先关掉，下一个弹窗才能弹出来，否则 SwiftUI 会吞掉后一个。
    private func queue(_ next: AgentCatalogPresentation) {
        guard presentationIsActive else {
            present(next)
            return
        }
        if pendingPresentation == nil { pendingPresentation = next }
        presentation = nil
    }

    private func present(_ next: AgentCatalogPresentation) {
        guard !presentationIsActive else {
            if pendingPresentation == nil { pendingPresentation = next }
            presentation = nil
            return
        }
        presentationIsActive = true
        presentation = next
    }

    private func presentationDidDismiss() {
        presentationIsActive = false
        guard let pending = pendingPresentation else { return }
        pendingPresentation = nil
        present(pending)
    }

    private func editorForm(_ item: AgentCatalogItem, restore: Bool = false) -> CustomAgentFormState {
        CustomAgentFormState(
            agentId: item.agentId,
            restore: restore,
            displayName: item.displayName,
            iconURL: item.iconUrl,
            bin: item.definition.bin,
            acpProgram: item.definition.acpProgram,
            argsText: item.definition.acpArgs.joined(separator: "\n"),
            supportsHeadlessPrint: item.supportsHeadlessPrint,
            environmentText: ""
        )
    }

    // MARK: 数据

    private func initialLoad() async {
        guard items.isEmpty else { return }
        await load()
        await loadVersions(force: false)
    }

    private func load(showLoading: Bool = true) async {
        if showLoading { isLoading = true }
        loadError = ""
        do {
            items = try await model.managedAgentCatalogItems()
        } catch {
            loadError = agentSettingsErrorDetail(error)
        }
        if showLoading { isLoading = false }
    }

    private func loadVersions(force: Bool) async {
        guard !isCheckingVersions else { return }
        isCheckingVersions = true
        versionError = ""
        do {
            let response = try await model.managedAgentVersions(force: force)
            versions = Dictionary(
                (response.agents ?? []).map { ($0.agentId, $0) },
                uniquingKeysWith: { _, latest in latest }
            )
            versionsCheckedAt = response.checkedAt
        } catch {
            versions = [:]
            versionsCheckedAt = nil
            versionError = agentSettingsErrorDetail(error)
        }
        isCheckingVersions = false
    }

    private func loadRevisions(_ agentId: String) async {
        guard revisions[agentId] == nil else { return }
        do {
            let response = try await model.apiClient().agentCatalogDetail(agentID: agentId)
            revisions[agentId] = response.revisions ?? []
        } catch {
            presentError(error, summary: "读取修订历史失败")
        }
    }

    // MARK: 安装 / 升级 / 卸载

    private func perform(_ intent: AgentCatalogIntent) {
        switch intent {
        case .upgrade(let agentId):
            guard let item = items.first(where: { $0.agentId == agentId }) else { return }
            runInstallAction(item, action: .upgrade) { client in
                try await model.upgradeManagedAgent(agentID: item.agentId, client: client)
            }
        case .upgradeAll(let agentIds):
            runUpgradeAll(agentIds: agentIds)
        case .uninstall(let agentId):
            guard let item = items.first(where: { $0.agentId == agentId }) else { return }
            runInstallAction(item, action: .uninstall) { client in
                try await client.uninstallAgent(agentID: item.agentId)
            }
        case .delete(let agentId, let force):
            guard let item = items.first(where: { $0.agentId == agentId }) else { return }
            performDelete(item, force: force)
        }
    }

    private func install(_ item: AgentCatalogItem) {
        runInstallAction(item, action: .install) { client in
            try await client.installAgent(agentID: item.agentId)
        }
    }

    private func requestUpgrade(_ item: AgentCatalogItem) {
        guard let version = versions[item.agentId], version.updateAvailable, version.upgradeSupported else { return }
        let changes = version.components
            .filter(\.updateAvailable)
            .map { "\($0.name)：\(componentVersionText($0))" }
            .joined(separator: "\n")
        queue(.confirmation(AgentCatalogConfirmation(
            intent: .upgrade(agentId: item.agentId),
            title: "升级这个 Agent？",
            message: changes.isEmpty
                ? AppLanguage.localizedFormat("将按目录预置流程升级“%@”。", item.displayName)
                : AppLanguage.localizedFormat("将按目录预置流程升级“%@”：\n%@", item.displayName, changes),
            confirmTitle: "确认升级",
            cancelTitle: "关闭",
            destructive: false
        )))
    }

    private func requestUpgradeAll() {
        let candidates = upgradeCandidates
        guard !candidates.isEmpty else { return }
        let names = candidates
            .map { "• \($0.displayName.isEmpty ? $0.agentId : $0.displayName)" }
            .joined(separator: "\n")
        queue(.confirmation(AgentCatalogConfirmation(
            intent: .upgradeAll(agentIds: candidates.map(\.agentId)),
            title: "更新全部 Agent？",
            message: AppLanguage.localizedFormat(
                "将按顺序更新以下 %d 个 Agent；单个 Agent 失败时会保留完整结果并继续更新其余项目：\n\n%@",
                candidates.count,
                names
            ),
            confirmTitle: "确认全部更新",
            cancelTitle: "关闭",
            destructive: false
        )))
    }

    private func requestUninstall(_ item: AgentCatalogItem) {
        queue(.confirmation(AgentCatalogConfirmation(
            intent: .uninstall(agentId: item.agentId),
            title: "卸载这个 Agent？",
            message: AppLanguage.localizedFormat(
                "将按目录预置流程卸载“%@”。已有的 Job、会话和工作流记录会保留，但在重新安装前无法再运行。",
                item.displayName
            ),
            confirmTitle: "确认卸载",
            cancelTitle: "保留安装",
            destructive: true
        )))
    }

    private func runInstallAction(
        _ item: AgentCatalogItem,
        action: AgentCatalogAction,
        request: @escaping @Sendable (APIClient) async throws -> AgentInstallResponse
    ) {
        guard busy == nil, pendingAgentId == nil, batchProgress == nil else { return }
        busy = AgentCatalogBusy(agentId: item.agentId, action: action)
        statusMessage = nil
        batchMessage = nil
        actionFailures.removeValue(forKey: item.agentId)
        results.removeValue(forKey: item.agentId)
        let displayName = item.displayName.isEmpty ? item.agentId : item.displayName
        let agentId = item.agentId
        Task { @MainActor in
            do {
                let client = try model.apiClient()
                let response = try await request(client)
                guard let result = response.result else {
                    throw APIError(
                        summary: AppLanguage.localizedFormat("%@ Agent 失败", action.name),
                        detail: AppLanguage.localizedFormat(
                            "服务端返回 code=%d，但没有带回执行结果。",
                            response.code
                        )
                    )
                }
                results[agentId] = AgentCatalogActionResult(
                    agentId: agentId,
                    displayName: displayName,
                    action: action,
                    result: result
                )
                await load(showLoading: false)
                await loadVersions(force: true)
                await model.refreshAgentCatalog()
                busy = nil
            } catch {
                busy = nil
                results.removeValue(forKey: agentId)
                presentError(error, summary: AppLanguage.localizedFormat("%@ Agent 失败", action.name))
            }
        }
    }

    private func runUpgradeAll(agentIds: [String]) {
        guard busy == nil, pendingAgentId == nil, batchProgress == nil else { return }
        let candidates = agentIds.compactMap { agentId in
            items.first { $0.agentId == agentId }
        }
        guard !candidates.isEmpty else { return }

        let firstName = candidates[0].displayName.isEmpty ? candidates[0].agentId : candidates[0].displayName
        batchProgress = AgentCatalogBatchProgress(completed: 0, total: candidates.count, currentName: firstName)
        busy = AgentCatalogBusy(agentId: candidates[0].agentId, action: .upgrade)
        statusMessage = nil
        batchMessage = nil
        actionFailures.removeAll()
        Task { @MainActor in
            var succeeded = 0
            var failed = 0
            var stoppedByConflict = false

            do {
                let client = try model.apiClient()
                for (index, item) in candidates.enumerated() {
                    let displayName = item.displayName.isEmpty ? item.agentId : item.displayName
                    batchProgress = AgentCatalogBatchProgress(
                        completed: index,
                        total: candidates.count,
                        currentName: displayName
                    )
                    busy = AgentCatalogBusy(agentId: item.agentId, action: .upgrade)
                    actionFailures.removeValue(forKey: item.agentId)
                    results.removeValue(forKey: item.agentId)

                    do {
                        let response = try await model.upgradeManagedAgent(agentID: item.agentId, client: client)
                        guard let result = response.result else {
                            throw APIError(
                                summary: "升级 Agent 失败",
                                detail: AppLanguage.localizedFormat(
                                    "服务端返回 code=%d，但没有带回执行结果。",
                                    response.code
                                )
                            )
                        }
                        let actionResult = AgentCatalogActionResult(
                            agentId: item.agentId,
                            displayName: displayName,
                            action: .upgrade,
                            result: result
                        )
                        results[item.agentId] = actionResult
                        if actionResult.ok { succeeded += 1 } else { failed += 1 }
                    } catch {
                        failed += 1
                        actionFailures[item.agentId] = AgentCatalogActionFailure(
                            agentId: item.agentId,
                            displayName: displayName,
                            detail: agentSettingsErrorDetail(error)
                        )
                        if let apiError = error as? APIError, apiError.httpStatusCode == 409 {
                            stoppedByConflict = true
                            break
                        }
                    }
                }
            } catch {
                failed = candidates.count
                actionFailures["batch"] = AgentCatalogActionFailure(
                    agentId: "batch",
                    displayName: "批量更新".localizedForApp,
                    detail: agentSettingsErrorDetail(error)
                )
            }

            let summary = stoppedByConflict
                ? "检测到另一个安装任务正在执行，已停止剩余更新。".localizedForApp
                : AppLanguage.localizedFormat("批量更新完成：%d 个成功，%d 个失败。", succeeded, failed)
            batchMessage = AgentSettingsMessage(
                kind: failed == 0 && !stoppedByConflict ? .success : .failure,
                text: summary
            )
            await load(showLoading: false)
            await loadVersions(force: true)
            await model.refreshAgentCatalog()
            busy = nil
            batchProgress = nil
        }
    }

    private func revalidate(_ item: AgentCatalogItem) {
        guard pendingAgentId == nil else { return }
        let agentId = item.agentId
        pendingAgentId = agentId
        statusMessage = nil
        checkFeedback[agentId] = AgentCatalogCheckFeedback(
            status: .checking,
            message: "正在重新拉起 ACP 会话验证可用性…".localizedForApp
        )
        Task { @MainActor in
            do {
                let response = try await model.apiClient().revalidateAgent(agentID: agentId)
                let warning = response.warning ?? ""
                checkFeedback[agentId] = AgentCatalogCheckFeedback(
                    status: warning.isEmpty ? .success : .warning,
                    message: warning.isEmpty
                        ? "Agent 可用，ACP 会话已成功建立。".localizedForApp
                        : warning
                )
                pendingAgentId = nil
                await load(showLoading: false)
                await model.refreshAgentCatalog()
            } catch {
                checkFeedback[agentId] = AgentCatalogCheckFeedback(
                    status: .failure,
                    message: agentSettingsErrorDetail(error)
                )
                pendingAgentId = nil
                await load(showLoading: false)
            }
        }
    }

    // MARK: 自定义 Agent

    private func submitCustomAgent(_ form: CustomAgentFormState) async throws {
        let environment = form.canEditEnvironment ? try parseEnvironment(form.environmentText) : nil
        let body = CustomAgentUpsertRequest(
            displayName: form.displayName.trimmingCharacters(in: .whitespacesAndNewlines),
            iconUrl: form.iconURL.trimmingCharacters(in: .whitespacesAndNewlines),
            supportsHeadlessPrint: form.supportsHeadlessPrint,
            definition: AgentRuntimeDefinition(
                bin: form.bin.trimmingCharacters(in: .whitespacesAndNewlines),
                acpProgram: form.acpProgram.trimmingCharacters(in: .whitespacesAndNewlines),
                acpArgs: form.argsText
                    .split(separator: "\n", omittingEmptySubsequences: false)
                    .map { $0.trimmingCharacters(in: CharacterSet(charactersIn: "\r")) }
                    .filter { !$0.isEmpty }
            ),
            environment: environment
        )
        let client = try model.apiClient()
        let response: CustomAgentResponse
        if let agentId = form.agentId {
            response = form.restore
                ? try await client.restoreCustomAgent(agentID: agentId, body: body)
                : try await client.updateCustomAgent(agentID: agentId, body: body)
        } else {
            response = try await client.createCustomAgent(body)
        }
        let warning = response.warning ?? ""
        statusMessage = warning.isEmpty
            ? "已保存".localizedForApp
            : "已保存".localizedForApp + "\n" + warning
        await load(showLoading: false)
        await loadVersions(force: true)
        await model.refreshAgentCatalog()
    }

    /// 每行一条 `KEY=value`。缺少等号或键为空时直接报错，不静默丢弃。
    private func parseEnvironment(_ text: String) throws -> [AgentEnvironmentItem] {
        var entries: [AgentEnvironmentItem] = []
        for rawLine in text.split(separator: "\n", omittingEmptySubsequences: false) {
            let line = rawLine.trimmingCharacters(in: .whitespacesAndNewlines)
            if line.isEmpty { continue }
            guard let separator = line.firstIndex(of: "="), separator != line.startIndex else {
                throw APIError(
                    summary: "环境变量格式错误",
                    detail: AppLanguage.localizedFormat(
                        "每行需要写成 KEY=value，下面这行无法解析：\n%@",
                        line
                    )
                )
            }
            entries.append(AgentEnvironmentItem(
                key: String(line[line.startIndex..<separator]).trimmingCharacters(in: .whitespaces),
                value: String(line[line.index(after: separator)...]),
                enabled: true
            ))
        }
        return entries
    }

    private func requestDelete(_ item: AgentCatalogItem, force: Bool) {
        guard pendingAgentId == nil else { return }
        let agentId = item.agentId
        pendingAgentId = agentId
        statusMessage = nil
        Task { @MainActor in
            do {
                let response = try await model.apiClient().customAgentDeleteImpact(agentID: agentId)
                pendingAgentId = nil
                guard let impact = response.impact else {
                    throw APIError(
                        summary: "读取删除影响失败",
                        detail: AppLanguage.localizedFormat("服务端返回 code=%d，但没有带回影响范围。", response.code)
                    )
                }
                queue(.confirmation(AgentCatalogConfirmation(
                    intent: .delete(agentId: agentId, force: force),
                    title: force ? "强制删除这个 Agent？" : "删除这个 Agent？",
                    message: deleteMessage(item, impact: impact, force: force),
                    confirmTitle: force ? "确认强制删除" : "确认删除",
                    cancelTitle: "保留 Agent",
                    destructive: true
                )))
            } catch {
                pendingAgentId = nil
                presentError(error, summary: "读取删除影响失败")
            }
        }
    }

    private func deleteMessage(_ item: AgentCatalogItem, impact: AgentDeleteImpact, force: Bool) -> String {
        var lines: [String] = []
        lines.append(force
            ? AppLanguage.localizedFormat("将强制删除“%@”，正在运行的 Job 会先被停止。", item.displayName)
            : AppLanguage.localizedFormat("将删除“%@”。", item.displayName))
        lines.append("")
        lines.append(AppLanguage.localizedFormat("会清空的设置：%@", agentSettingsListValue(impact.clearedSettings)))
        lines.append(AppLanguage.localizedFormat("保留引用的工作流：%@", agentSettingsListValue(impact.retainedWorkflows)))
        lines.append(AppLanguage.localizedFormat("保留引用的定时任务：%@", agentSettingsListValue(impact.retainedSchedules)))
        lines.append(AppLanguage.localizedFormat("保留引用的 Job：%@", agentSettingsListValue(impact.retainedJobs)))
        lines.append(AppLanguage.localizedFormat("保留引用的会话：%@", agentSettingsListValue(impact.retainedSessions)))
        lines.append(AppLanguage.localizedFormat("阻塞删除的 Job：%@", agentSettingsListValue(impact.blockingJobIds)))
        return lines.joined(separator: "\n")
    }

    private func performDelete(_ item: AgentCatalogItem, force: Bool) {
        let agentId = item.agentId
        pendingAgentId = agentId
        statusMessage = "正在删除 Agent…".localizedForApp
        Task { @MainActor in
            do {
                let response = try await model.apiClient().deleteCustomAgent(agentID: agentId, force: force)
                pendingAgentId = nil
                statusMessage = "已删除 Agent".localizedForApp
                await load(showLoading: false)
                await loadVersions(force: true)
                await model.refreshAgentCatalog()
                if let outcome = response.result {
                    queue(.deleteOutcome(outcome, agentId))
                }
            } catch {
                pendingAgentId = nil
                statusMessage = nil
                presentError(error, summary: "删除 Agent 失败")
                await load(showLoading: false)
            }
        }
    }

    private func presentError(_ caught: Error, summary: String) {
        queue(.error(agentSettingsPresentedError(caught, summary: summary)))
    }
}

// MARK: - 操作弹窗

private struct AgentCatalogResultBadge {
    let summary: String
    let ok: Bool
}

private struct AgentCatalogActionsSheet: View {
    let item: AgentCatalogItem
    let version: AgentVersionInfo?
    let canManage: Bool
    let installLocked: Bool
    let pending: Bool
    let result: AgentCatalogResultBadge?
    let onInstall: () -> Void
    let onUpgrade: () -> Void
    let onUninstall: () -> Void
    let onRevalidate: () -> Void
    let onEdit: () -> Void
    let onDelete: (Bool) -> Void
    let onRestore: () -> Void
    let onInspectResult: () -> Void

    private var canInstall: Bool {
        canManage && item.isBuiltin && !item.deprecated && !item.installed && item.autoInstallable
    }

    private var canUpgrade: Bool {
        canManage && item.isBuiltin && !item.deprecated && item.installed
            && (version?.updateAvailable ?? false) && (version?.upgradeSupported ?? false)
    }

    private var canUninstall: Bool {
        canManage && item.isBuiltin && !item.deprecated && item.installed && item.autoUninstallable
    }

    private var canRevalidate: Bool {
        canManage && item.lifecycle != "deleted" && !item.deprecated && item.installed
    }

    private var canEditCustom: Bool { canManage && item.isCustom && item.lifecycle == "active" }
    private var canRetryDelete: Bool { canManage && item.isCustom && item.lifecycle == "deleting" }
    private var canRestore: Bool { canManage && item.isCustom && item.lifecycle == "deleted" }
    private var hasPrimaryActions: Bool {
        canInstall || canUpgrade || canRevalidate || canEditCustom || canRestore || result != nil
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(spacing: 12) {
                    if !canManage {
                        agentSettingsHint("当前账号没有 agent.manage 权限，只能查看 Agent 目录。")
                            .padding(.horizontal, 4)
                    }
                    if installLocked {
                        agentSettingsHint("已有安装 / 升级 / 卸载在执行，后端会串行处理，请等它跑完。")
                            .padding(.horizontal, 4)
                    }
                    if hasPrimaryActions { primaryActions }
                    if canUninstall || canEditCustom || canRetryDelete { destructiveActions }
                    if !hasPrimaryActions && !canUninstall && !canEditCustom && !canRetryDelete {
                        agentSettingsHint("这个 Agent 当前没有可执行的管理操作。")
                            .padding(.horizontal, 4)
                    }
                }
                .padding(.horizontal, 20)
                .padding(.top, 8)
                .padding(.bottom, 24)
            }
            .background(QuartetTheme.canvas)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .principal) {
                    Text(item.displayName.isEmpty ? item.agentId : item.displayName)
                        .font(.quartet(.regular, weight: .semibold))
                        .foregroundStyle(QuartetTheme.primaryText)
                        .lineLimit(1)
                        .accessibilityAddTraits(.isHeader)
                }
            }
        }
    }

    private var primaryActions: some View {
        VStack(spacing: 0) {
            if canInstall {
                row(
                    title: "安装 Agent",
                    detail: "按目录预置的安装命令执行",
                    systemImage: "arrow.down.circle.fill",
                    tint: QuartetTheme.accent,
                    identifier: "agent-catalog-action-install",
                    disabled: installLocked,
                    action: onInstall
                )
            }
            if canUpgrade {
                if canInstall { divider }
                row(
                    title: "升级 Agent",
                    detail: upgradeDetail,
                    systemImage: "arrow.up.circle.fill",
                    tint: QuartetTheme.warning,
                    identifier: "agent-catalog-action-upgrade",
                    disabled: installLocked,
                    action: onUpgrade
                )
            }
            if canRevalidate {
                if canInstall || canUpgrade { divider }
                row(
                    title: "检查可用性",
                    detail: "重新拉起 ACP 会话验证",
                    systemImage: "checkmark.seal.fill",
                    tint: QuartetTheme.accentDeep,
                    identifier: "agent-catalog-action-revalidate",
                    disabled: installLocked || pending,
                    action: onRevalidate
                )
            }
            if canEditCustom {
                if canInstall || canUpgrade || canRevalidate { divider }
                row(
                    title: "编辑自定义 Agent",
                    detail: "修改名称、启动命令与参数",
                    systemImage: "pencil",
                    tint: QuartetTheme.accentDeep,
                    identifier: "agent-catalog-action-edit",
                    disabled: installLocked || pending,
                    action: onEdit
                )
            }
            if canRestore {
                if canRevalidate || canEditCustom { divider }
                row(
                    title: "恢复自定义 Agent",
                    detail: "沿用原 AgentID 重新登记",
                    systemImage: "arrow.uturn.backward.circle.fill",
                    tint: QuartetTheme.accent,
                    identifier: "agent-catalog-action-restore",
                    disabled: installLocked || pending,
                    action: onRestore
                )
            }
            if let result {
                if canInstall || canUpgrade || canRevalidate || canEditCustom || canRestore { divider }
                row(
                    title: "查看命令输出",
                    detail: result.summary,
                    systemImage: "text.alignleft",
                    tint: result.ok ? QuartetTheme.success : QuartetTheme.failed,
                    identifier: "agent-catalog-action-result",
                    disabled: false,
                    action: onInspectResult
                )
            }
        }
        .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 18, style: .continuous).stroke(QuartetTheme.divider.opacity(0.8)))
    }

    private var destructiveActions: some View {
        VStack(spacing: 0) {
            if canUninstall {
                row(
                    title: "卸载 Agent",
                    detail: "按目录预置的卸载命令执行",
                    systemImage: "trash.fill",
                    tint: QuartetTheme.failed,
                    identifier: "agent-catalog-action-uninstall",
                    disabled: installLocked,
                    isDestructive: true,
                    action: onUninstall
                )
            }
            if canEditCustom {
                if canUninstall { divider }
                row(
                    title: "删除 Agent",
                    detail: "有运行中的 Job 时会被拒绝",
                    systemImage: "trash.fill",
                    tint: QuartetTheme.failed,
                    identifier: "agent-catalog-action-delete",
                    disabled: installLocked || pending,
                    isDestructive: true,
                    action: { onDelete(false) }
                )
                divider
                row(
                    title: "强制删除 Agent",
                    detail: "先停止运行中的 Job 再删除",
                    systemImage: "exclamationmark.triangle.fill",
                    tint: QuartetTheme.failed,
                    identifier: "agent-catalog-action-force-delete",
                    disabled: installLocked || pending,
                    isDestructive: true,
                    action: { onDelete(true) }
                )
            }
            if canRetryDelete {
                if canUninstall || canEditCustom { divider }
                row(
                    title: "重试强制删除",
                    detail: "上次删除没有跑完，重新执行清理",
                    systemImage: "arrow.clockwise",
                    tint: QuartetTheme.failed,
                    identifier: "agent-catalog-action-retry-delete",
                    disabled: installLocked || pending,
                    isDestructive: true,
                    action: { onDelete(true) }
                )
            }
        }
        .background(QuartetTheme.failed.opacity(0.07), in: RoundedRectangle(cornerRadius: 18, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 18, style: .continuous).stroke(QuartetTheme.failed.opacity(0.18)))
    }

    private var upgradeDetail: String {
        let changes = (version?.components ?? [])
            .filter(\.updateAvailable)
            .map(\.name)
            .joined(separator: "、")
        return changes.isEmpty ? "按目录预置的升级命令执行".localizedForApp : changes
    }

    private var divider: some View {
        Divider().overlay(QuartetTheme.divider).padding(.leading, 62)
    }

    private func row(
        title: String,
        detail: String,
        systemImage: String,
        tint: Color,
        identifier: String,
        disabled: Bool,
        isDestructive: Bool = false,
        action: @escaping () -> Void
    ) -> some View {
        Button(role: isDestructive ? .destructive : nil, action: action) {
            HStack(spacing: 12) {
                Image(systemName: systemImage)
                    .font(.quartet(.control, weight: .semibold))
                    .foregroundStyle(tint)
                    .frame(width: 38, height: 38)
                    .background(tint.opacity(0.11), in: Circle())
                VStack(alignment: .leading, spacing: 3) {
                    Text(title.localizedForApp)
                        .font(.quartet(.control, weight: .semibold))
                        .foregroundStyle(isDestructive ? QuartetTheme.failed : QuartetTheme.primaryText)
                    Text(detail.localizedForApp)
                        .font(.quartet(.detail))
                        .foregroundStyle(QuartetTheme.secondaryText)
                        .lineLimit(2)
                }
                Spacer(minLength: 8)
            }
            .padding(.horizontal, 13)
            .frame(minHeight: 64)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(disabled)
        .opacity(disabled ? 0.45 : 1)
        .accessibilityIdentifier(identifier)
    }
}

// MARK: - 确认弹窗

private struct AgentCatalogConfirmationSheet: View {
    @Environment(\.dismiss) private var dismiss
    let confirmation: AgentCatalogConfirmation
    let onConfirm: (AgentCatalogIntent) -> Void

    private var tint: Color { confirmation.destructive ? QuartetTheme.failed : QuartetTheme.accent }

    var body: some View {
        NavigationStack {
            VStack(spacing: 18) {
                HStack(alignment: .top, spacing: 14) {
                    Image(systemName: confirmation.destructive ? "exclamationmark.triangle.fill" : "questionmark.circle.fill")
                        .font(.quartet(.regular, weight: .semibold))
                        .foregroundStyle(tint)
                        .frame(width: 44, height: 44)
                        .background(tint.opacity(0.12), in: Circle())
                    ScrollView {
                        Text(confirmation.message)
                            .font(.quartet(.detail))
                            .foregroundStyle(QuartetTheme.primaryText)
                            .textSelection(.enabled)
                            .fixedSize(horizontal: false, vertical: true)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                }
                .padding(16)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(tint.opacity(0.07), in: RoundedRectangle(cornerRadius: 18, style: .continuous))
                .overlay(RoundedRectangle(cornerRadius: 18, style: .continuous).stroke(tint.opacity(0.2)))

                HStack(spacing: 10) {
                    Button(LocalizedStringKey(confirmation.cancelTitle)) { dismiss() }
                        .foregroundStyle(QuartetTheme.primaryText)
                        .frame(maxWidth: .infinity).frame(height: 50)
                        .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
                    Button(LocalizedStringKey(confirmation.confirmTitle)) {
                        dismiss()
                        onConfirm(confirmation.intent)
                    }
                    .foregroundStyle(confirmation.destructive ? QuartetTheme.onDanger : QuartetTheme.onAccent)
                    .frame(maxWidth: .infinity).frame(height: 50)
                    .background(tint, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
                    .accessibilityIdentifier("agent-catalog-confirm")
                }
                .font(.quartet(.control, weight: .semibold))
                .buttonStyle(.plain)
                Spacer(minLength: 0)
            }
            .padding(.horizontal, 20)
            .padding(.top, 8)
            .background(QuartetTheme.canvas)
            .navigationTitle(confirmation.title.localizedForApp)
            .navigationBarTitleDisplayMode(.inline)
        }
    }
}

// MARK: - 命令输出弹窗

private struct AgentInstallResultSheet: View {
    @Environment(\.dismiss) private var dismiss
    let title: String
    let summary: String
    let ok: Bool
    let result: AgentInstallResult

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 12) {
                    AgentSettingsMessageView(kind: ok ? .success : .failure, text: summary)
                    ForEach(Array(result.steps.enumerated()), id: \.offset) { index, step in
                        stepCard(step, index: index)
                    }
                    if let detail = result.installError, !detail.isEmpty {
                        AgentSettingsCard("安装后复检", systemImage: "arrow.triangle.2.circlepath") {
                            outputBlock(detail, failed: true)
                        }
                    }
                    if let validation = result.validation {
                        validationCard(validation)
                    }
                }
                .padding(.horizontal, 18)
                .padding(.vertical, 12)
            }
            .background(QuartetTheme.canvas)
            .navigationTitle(title)
            .navigationBarTitleDisplayMode(.inline)
            .safeAreaInset(edge: .bottom, spacing: 0) {
                HStack(spacing: 10) {
                    Button("复制全部输出") { UIPasteboard.general.string = fullOutput }
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

    private func stepCard(_ step: AgentInstallStepResult, index: Int) -> some View {
        AgentSettingsCard {
            Text(step.display)
                .font(.quartet(.detail, design: .monospaced))
                .foregroundStyle(QuartetTheme.primaryText)
                .textSelection(.enabled)
                .fixedSize(horizontal: false, vertical: true)
                .frame(maxWidth: .infinity, alignment: .leading)
            Text(stepMeta(step, index: index))
                .font(.quartet(.compact))
                .foregroundStyle(step.succeeded ? QuartetTheme.secondaryText : QuartetTheme.failed)
            if let detail = step.error, !detail.isEmpty {
                outputBlock(detail, failed: true)
            }
            if !step.stdout.isEmpty { outputBlock(step.stdout, failed: false) }
            if !step.stderr.isEmpty { outputBlock(step.stderr, failed: true) }
        }
    }

    private func validationCard(_ validation: AgentValidationResult) -> some View {
        AgentSettingsCard("ACP 校验", systemImage: "checkmark.seal") {
            Text(validation.ok ? "通过" : "失败")
                .font(.quartet(.detail, weight: .semibold))
                .foregroundStyle(validation.ok ? QuartetTheme.success : QuartetTheme.failed)
            if let detail = validation.error, !detail.isEmpty {
                outputBlock(detail, failed: true)
            }
        }
    }

    private func stepMeta(_ step: AgentInstallStepResult, index: Int) -> String {
        var parts = [AppLanguage.localizedFormat("步骤 %d", index + 1)]
        parts.append(AppLanguage.localizedFormat("退出码 %d", step.exitCode))
        if step.timedOut { parts.append("已超时".localizedForApp) }
        parts.append(String(format: "%.1fs", Double(step.durationMs) / 1_000))
        return parts.joined(separator: " · ")
    }

    private func outputBlock(_ text: String, failed: Bool) -> some View {
        Text(text)
            .font(.quartet(.compact, design: .monospaced))
            .foregroundStyle(failed ? QuartetTheme.failed : QuartetTheme.primaryText)
            .textSelection(.enabled)
            .fixedSize(horizontal: false, vertical: true)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(10)
            .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
    }

    private var fullOutput: String {
        var parts = [summary]
        for (index, step) in result.steps.enumerated() {
            parts.append("[\(index + 1)] \(step.display)")
            parts.append(stepMeta(step, index: index))
            if let detail = step.error, !detail.isEmpty { parts.append("error:\n\(detail)") }
            if !step.stdout.isEmpty { parts.append("stdout:\n\(step.stdout)") }
            if !step.stderr.isEmpty { parts.append("stderr:\n\(step.stderr)") }
        }
        if let detail = result.installError, !detail.isEmpty { parts.append("install recheck:\n\(detail)") }
        if let validation = result.validation {
            parts.append("validation: \(validation.ok ? "ok" : "failed")")
            if let detail = validation.error, !detail.isEmpty { parts.append(detail) }
        }
        return parts.joined(separator: "\n\n")
    }
}

// MARK: - 删除结果弹窗

private struct AgentDeleteOutcomeSheet: View {
    @Environment(\.dismiss) private var dismiss
    let outcome: AgentDeleteResult

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(alignment: .leading, spacing: 12) {
                    if let impact = outcome.impact {
                        impactCard(impact)
                    }
                    if let stopResults = outcome.stopResults, !stopResults.isEmpty {
                        stopResultsCard(stopResults)
                    }
                    if !outcome.status.isEmpty {
                        agentSettingsHint(AppLanguage.localizedFormat("服务端状态：%@", outcome.status))
                    }
                }
                .padding(.horizontal, 18)
                .padding(.vertical, 12)
            }
            .background(QuartetTheme.canvas)
            .navigationTitle("删除结果")
            .navigationBarTitleDisplayMode(.inline)
            .safeAreaInset(edge: .bottom, spacing: 0) {
                Button("关闭") { dismiss() }
                    .font(.quartet(.control, weight: .semibold))
                    .foregroundStyle(QuartetTheme.onAccent)
                    .frame(maxWidth: .infinity).frame(height: 50)
                    .background(QuartetTheme.accent, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
                    .buttonStyle(.plain)
                    .padding(.horizontal, 18)
                    .padding(.vertical, 10)
                    .background(.ultraThinMaterial)
            }
        }
    }

    private func impactCard(_ impact: AgentDeleteImpact) -> some View {
        AgentSettingsCard("影响范围", systemImage: "list.bullet.rectangle") {
            impactRow("已清空的设置", agentSettingsListValue(impact.clearedSettings))
            impactRow("保留引用的工作流", agentSettingsListValue(impact.retainedWorkflows))
            impactRow("保留引用的定时任务", agentSettingsListValue(impact.retainedSchedules))
            impactRow("保留引用的 Job", agentSettingsListValue(impact.retainedJobs))
            impactRow("保留引用的会话", agentSettingsListValue(impact.retainedSessions))
        }
    }

    private func stopResultsCard(_ stopResults: [AgentDeleteStopResult]) -> some View {
        AgentSettingsCard("停止运行中的 Job", systemImage: "stop.circle") {
            ForEach(stopResults) { stopResult in
                VStack(alignment: .leading, spacing: 3) {
                    Text(stopResult.jobId)
                        .font(.quartet(.detail, design: .monospaced))
                        .foregroundStyle(QuartetTheme.primaryText)
                    Text(stopResult.stopped
                        ? "已停止".localizedForApp
                        : (stopResult.error ?? "停止失败".localizedForApp))
                        .font(.quartet(.detail))
                        .foregroundStyle(stopResult.stopped ? QuartetTheme.success : QuartetTheme.failed)
                        .textSelection(.enabled)
                        .fixedSize(horizontal: false, vertical: true)
                }
                .frame(maxWidth: .infinity, alignment: .leading)
            }
        }
    }

    private func impactRow(_ label: String, _ value: String) -> some View {
        VStack(alignment: .leading, spacing: 3) {
            agentSettingsFieldLabel(label)
            Text(value)
                .font(.quartet(.detail))
                .foregroundStyle(QuartetTheme.primaryText)
                .textSelection(.enabled)
                .fixedSize(horizontal: false, vertical: true)
                .frame(maxWidth: .infinity, alignment: .leading)
        }
    }
}

// MARK: - 自定义 Agent 编辑弹窗

private struct CustomAgentEditorSheet: View {
    @Environment(\.dismiss) private var dismiss
    @State private var form: CustomAgentFormState
    @State private var isSaving = false
    @State private var error: PresentedError?
    private let onSave: @MainActor (CustomAgentFormState) async throws -> Void

    init(
        form: CustomAgentFormState,
        onSave: @escaping @MainActor (CustomAgentFormState) async throws -> Void
    ) {
        _form = State(initialValue: form)
        self.onSave = onSave
    }

    private var isValid: Bool {
        !form.displayName.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
            && !form.bin.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
            && !form.acpProgram.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(spacing: 14) {
                    basicsCard
                    definitionCard
                    environmentCard
                }
                .padding(.horizontal, 18)
                .padding(.top, 10)
                .padding(.bottom, 18)
            }
            .scrollDismissesKeyboard(.interactively)
            .background(QuartetTheme.canvas)
            .navigationTitle(form.title.localizedForApp)
            .navigationBarTitleDisplayMode(.inline)
            .safeAreaInset(edge: .bottom, spacing: 0) {
                AgentSettingsSaveBar(
                    title: "保存 Agent",
                    savingTitle: "正在保存…",
                    isSaving: isSaving,
                    isEnabled: isValid,
                    identifier: "agent-custom-save",
                    action: { save() }
                )
            }
            .interactiveDismissDisabled(isSaving)
        }
        .sheet(item: $error) {
            AgentSettingsErrorSheet(error: $0)
                .presentationDetents([.medium, .large])
                .quartetSheetStyle()
        }
    }

    private var basicsCard: some View {
        AgentSettingsCard("基本信息", systemImage: "square.grid.2x2") {
            AgentSettingsTextField(
                title: "显示名称",
                text: $form.displayName,
                identifier: "agent-custom-display-name"
            )
            agentSettingsDivider()
            AgentSettingsTextField(
                title: "图标",
                text: $form.iconURL,
                identifier: "agent-custom-icon",
                placeholder: "emoji 或图片地址"
            )
            agentSettingsHint("可以填一个 emoji，也可以填图片地址；留空时列表只显示名称。")
        }
    }

    private var definitionCard: some View {
        AgentSettingsCard("启动定义", systemImage: "terminal") {
            AgentSettingsTextField(
                title: "可执行文件",
                text: $form.bin,
                identifier: "agent-custom-bin",
                monospaced: true
            )
            agentSettingsHint("后端用它判断这个 Agent 是否已安装。")
            agentSettingsDivider()
            AgentSettingsTextField(
                title: "ACP 启动程序",
                text: $form.acpProgram,
                identifier: "agent-custom-acp-program",
                monospaced: true
            )
            agentSettingsDivider()
            AgentSettingsTextEditor(
                title: "启动参数",
                text: $form.argsText,
                identifier: "agent-custom-args",
                hint: "每行一个参数，按顺序原样传给进程，不经过 shell。"
            )
            agentSettingsDivider()
            Toggle("支持 bin -p 单次执行", isOn: $form.supportsHeadlessPrint)
                .font(.quartet(.control, weight: .medium))
                .tint(QuartetTheme.accent)
                .accessibilityIdentifier("agent-custom-headless")
            agentSettingsHint("打开后这个 Agent 才能被标题生成和群回复这类无会话角色选用。")
        }
    }

    private var environmentCard: some View {
        AgentSettingsCard("环境变量", systemImage: "key") {
            if form.canEditEnvironment {
                AgentSettingsTextEditor(
                    title: "环境变量",
                    text: $form.environmentText,
                    identifier: "agent-custom-environment",
                    hint: "每行一条 KEY=value，保存后可在“环境变量”标签里继续调整。"
                )
            } else {
                agentSettingsHint("编辑已有 Agent 时不改动环境变量，请到“环境变量”标签里维护。")
            }
        }
    }

    private func save() {
        isSaving = true
        Task { @MainActor in
            do {
                try await onSave(form)
                dismiss()
            } catch {
                self.error = agentSettingsPresentedError(error, summary: "保存自定义 Agent 失败")
                isSaving = false
            }
        }
    }
}
