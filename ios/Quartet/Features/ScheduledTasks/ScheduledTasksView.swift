import SwiftUI
import UIKit

struct ScheduledTasksView: View {
    @EnvironmentObject private var model: AppModel
    @Environment(\.mainTabBarInset) private var mainTabBarInset
    @State private var schedules: [ScheduleInfo] = []
    @State private var workflows: [GraphWorkflowSummary] = []
    @State private var warnings: [GraphWorkflowWarning] = []
    @State private var isLoading = true
    @State private var refreshRevision = 0
    @State private var editingSchedule: ScheduleInfo?
    @State private var isCreating = false
    @State private var actionSchedule: ScheduleInfo?
    @State private var pendingEditSchedule: ScheduleInfo?
    @State private var operationMessage: String?
    @State private var error: PresentedError?

    var body: some View {
        NavigationStack {
            Group {
                if isLoading, schedules.isEmpty {
                    VStack(spacing: 12) {
                        ProgressView().controlSize(.large).tint(QuartetTheme.accent)
                        Text("正在加载定时任务…")
                            .font(.quartet(.control))
                            .foregroundStyle(QuartetTheme.secondaryText)
                    }
                    .frame(maxWidth: .infinity, maxHeight: .infinity)
                    .accessibilityIdentifier("schedule-loading")
                } else if schedules.isEmpty {
                    ContentUnavailableView {
                        Label("暂无定时任务", systemImage: "calendar.badge.clock")
                    } description: {
                        Text("创建定时任务，让已保存的 Graph Workflow 按计划自动运行。")
                    } actions: {
                        if model.can("schedule.write") {
                            Button("新增定时任务") { isCreating = true }
                                .accessibilityIdentifier("schedule-empty-add")
                        }
                    }
                    .accessibilityIdentifier("schedule-empty")
                } else {
                    ScrollView {
                        LazyVStack(spacing: 10) {
                            if let operationMessage {
                                Label(operationMessage, systemImage: "checkmark.circle.fill")
                                    .font(.quartet(.control, weight: .semibold))
                                    .foregroundStyle(QuartetTheme.success)
                                    .frame(maxWidth: .infinity, alignment: .leading)
                                    .padding(.horizontal, 4)
                                    .accessibilityIdentifier("schedule-operation-status")
                            }
                            ForEach(schedules) { schedule in
                                ScheduleCard(
                                    schedule: schedule,
                                    workflowName: workflowName(for: schedule),
                                    onShowActions: { actionSchedule = schedule }
                                )
                            }
                        }
                        .padding(.horizontal, 16)
                        .padding(.vertical, 12)
                    }
                    .refreshable { await load() }
                }
            }
            .background(QuartetTheme.canvas)
            .mainTabBarBottomInset(mainTabBarInset)
            .navigationTitle("定时任务")
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                if model.can("schedule.write") {
                    ToolbarItem(placement: .topBarTrailing) {
                        Button { isCreating = true } label: { Image(systemName: "plus") }
                            .accessibilityLabel("新增定时任务")
                            .accessibilityIdentifier("schedule-add")
                    }
                    .sharedBackgroundVisibility(.hidden)
                }
            }
        }
        .toolbarBackground(QuartetTheme.canvas, for: .navigationBar)
        .toolbarBackground(.visible, for: .navigationBar)
        .task(id: refreshRevision) { await load() }
        .sheet(isPresented: $isCreating) {
            editor(schedule: nil)
                .presentationDetents([.large])
                .quartetSheetStyle()
        }
        .sheet(item: $editingSchedule) {
            editor(schedule: $0)
                .presentationDetents([.large])
                .quartetSheetStyle()
        }
        .sheet(item: $actionSchedule, onDismiss: {
            if let pending = pendingEditSchedule {
                pendingEditSchedule = nil
                editingSchedule = pending
            }
        }) { schedule in
            ScheduleActionsSheet(
                schedule: schedule,
                canWrite: model.can("schedule.write"),
                canRun: model.can("schedule.execute"),
                onRun: {
                    actionSchedule = nil
                    run(schedule)
                },
                onToggle: {
                    actionSchedule = nil
                    toggle(schedule)
                },
                onEdit: {
                    pendingEditSchedule = schedule
                    actionSchedule = nil
                },
                onDelete: {
                    actionSchedule = nil
                    delete(schedule)
                }
            )
            .presentationDetents([.medium])
            .quartetSheetStyle()
        }
        .sheet(item: $error) {
            ScheduleErrorSheet(error: $0)
                .presentationDetents([.medium, .large])
                .quartetSheetStyle()
        }
    }

    private func editor(schedule: ScheduleInfo?) -> some View {
        ScheduleEditorView(
            schedule: schedule,
            workflows: workflows,
            warnings: warnings,
            workspaces: model.workspaces,
            onSave: { request in try await save(schedule, request: request) }
        )
    }

    @MainActor
    private func load() async {
        isLoading = true
        defer { isLoading = false }
        do {
            let client = try model.apiClient()
            async let scheduleRequest = client.schedules()
            async let workflowRequest = client.graphWorkflows()
            let (scheduleResponse, workflowResponse) = try await (scheduleRequest, workflowRequest)
            schedules = scheduleResponse.schedules.sorted { $0.updatedAt > $1.updatedAt }
            workflows = workflowResponse.workflows.sorted {
                $0.name.localizedStandardCompare($1.name) == .orderedAscending
            }
            warnings = workflowResponse.warnings ?? []
        } catch {
            present(error, summary: "定时任务加载失败")
        }
    }

    @MainActor
    private func save(_ existing: ScheduleInfo?, request: ScheduleMutationRequest) async throws {
        let client = try model.apiClient()
        let saved: ScheduleInfo
        if let existing {
            saved = try await client.updateSchedule(id: existing.id, body: request)
            operationMessage = "已更新“\(saved.name)”"
        } else {
            saved = try await client.createSchedule(request)
            operationMessage = "已创建“\(saved.name)”"
        }
        upsert(saved)
    }

    private func toggle(_ schedule: ScheduleInfo) {
        Task { @MainActor in
            do {
                let updated = try await model.apiClient().toggleSchedule(id: schedule.id)
                upsert(updated)
                operationMessage = updated.enabled ? "已启用“\(updated.name)”" : "已停用“\(updated.name)”"
            } catch { present(error, summary: "切换任务状态失败") }
        }
    }

    private func run(_ schedule: ScheduleInfo) {
        Task { @MainActor in
            do {
                let response = try await model.apiClient().runSchedule(id: schedule.id)
                operationMessage = response.jobId.map { "已触发“\(schedule.name)” · Job \($0)" } ?? "已触发“\(schedule.name)”"
                let responseList = try await model.apiClient().schedules()
                schedules = responseList.schedules.sorted { $0.updatedAt > $1.updatedAt }
            } catch { present(error, summary: "立即运行失败") }
        }
    }

    private func delete(_ schedule: ScheduleInfo) {
        Task { @MainActor in
            do {
                try await model.apiClient().deleteSchedule(id: schedule.id)
                schedules.removeAll { $0.id == schedule.id }
                operationMessage = "已删除“\(schedule.name)”"
            } catch { present(error, summary: "删除定时任务失败") }
        }
    }

    private func upsert(_ schedule: ScheduleInfo) {
        if let index = schedules.firstIndex(where: { $0.id == schedule.id }) {
            schedules[index] = schedule
        } else {
            schedules.insert(schedule, at: 0)
        }
        schedules.sort { $0.updatedAt > $1.updatedAt }
    }

    private func workflowName(for schedule: ScheduleInfo) -> String {
        guard let id = schedule.graphWorkflowId, !id.isEmpty else { return "未绑定工作流" }
        return workflows.first(where: { $0.id == id })?.name ?? id
    }

    private func present(_ caught: Error, summary: String) {
        if let apiError = caught as? APIError {
            error = PresentedError(title: apiError.summary, detail: apiError.detail)
        } else {
            error = PresentedError(title: summary, detail: String(describing: caught))
        }
    }
}

private struct ScheduleCard: View {
    let schedule: ScheduleInfo
    let workflowName: String
    let onShowActions: () -> Void

    var body: some View {
        HStack(alignment: .top, spacing: 10) {
            Button(action: onShowActions) {
                HStack(alignment: .top, spacing: 10) {
                    Circle()
                        .fill(statusColor)
                        .frame(width: 9, height: 9)
                        .padding(.top, 6)
                    VStack(alignment: .leading, spacing: 8) {
                        VStack(alignment: .leading, spacing: 4) {
                            Text(schedule.name)
                                .font(.quartet(.regular, weight: .semibold))
                                .foregroundStyle(QuartetTheme.primaryText)
                                .lineLimit(2)
                            Text(workflowName)
                                .font(.quartet(.detail))
                                .foregroundStyle(QuartetTheme.secondaryText)
                                .lineLimit(1)
                        }
                        HStack(spacing: 12) {
                            Label(schedule.cronExpr, systemImage: "clock")
                            if schedule.runCount > 0 {
                                Label("\(schedule.runCount) 次", systemImage: "arrow.trianglehead.2.clockwise.rotate.90")
                            }
                            Spacer()
                            Text(schedule.enabled ? "已启用" : "已停用")
                                .foregroundStyle(schedule.enabled ? QuartetTheme.success : QuartetTheme.secondaryText)
                        }
                        .font(.quartet(.compact, design: .monospaced))
                        .foregroundStyle(QuartetTheme.secondaryText)
                        if let nextRunAt = schedule.nextRunAt, schedule.enabled {
                            Text("下次运行：\(scheduleDate(nextRunAt))")
                                .font(.quartet(.detail))
                                .foregroundStyle(QuartetTheme.secondaryText)
                        }
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier("schedule-row-\(schedule.name)")

            Button(action: onShowActions) {
                Image(systemName: "ellipsis.circle")
                    .font(.title3)
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .frame(width: 40, height: 40)
            }
            .buttonStyle(.plain)
            .accessibilityLabel("\(schedule.name) 的任务操作")
            .accessibilityIdentifier("schedule-more-\(schedule.name)")
        }
        .padding(14)
        .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 18, style: .continuous).stroke(QuartetTheme.divider))
    }

    private var statusColor: Color {
        guard schedule.enabled else { return QuartetTheme.stopped }
        return schedule.lastStatus.map(QuartetTheme.statusColor) ?? QuartetTheme.success
    }
}

private enum ScheduleActionContent {
    case actions
    case deleteConfirmation
}

private struct ScheduleActionsSheet: View {
    let schedule: ScheduleInfo
    let canWrite: Bool
    let canRun: Bool
    let onRun: () -> Void
    let onToggle: () -> Void
    let onEdit: () -> Void
    let onDelete: () -> Void
    @State private var content: ScheduleActionContent = .actions

    var body: some View {
        NavigationStack {
            VStack(spacing: 20) {
                Group {
                    switch content {
                    case .actions: actions
                    case .deleteConfirmation: deleteConfirmation
                    }
                }
                .transition(.opacity.combined(with: .move(edge: .bottom)))
                Spacer(minLength: 0)
            }
            .padding(.horizontal, 20)
            .padding(.top, 8)
            .background(QuartetTheme.canvas)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .principal) {
                    Text(schedule.name)
                        .font(.quartet(.regular, weight: .semibold))
                        .foregroundStyle(QuartetTheme.primaryText)
                        .lineLimit(1)
                        .accessibilityAddTraits(.isHeader)
                }
            }
        }
        .animation(.snappy(duration: 0.28), value: content)
    }

    private var actions: some View {
        VStack(spacing: 12) {
            VStack(spacing: 0) {
                if canRun {
                    actionRow(
                        title: "立即运行",
                        detail: "立即触发一次 Graph Workflow",
                        systemImage: "play.fill",
                        tint: QuartetTheme.accent,
                        identifier: "schedule-action-run",
                        showsDisclosure: false,
                        action: onRun
                    )
                }
                if canRun, canWrite { divider }
                if canWrite {
                    actionRow(
                        title: schedule.enabled ? "停用任务" : "启用任务",
                        detail: schedule.enabled ? "暂停后续 Cron 自动触发" : "恢复后续 Cron 自动触发",
                        systemImage: schedule.enabled ? "pause.fill" : "play.circle.fill",
                        tint: QuartetTheme.warning,
                        identifier: "schedule-action-toggle",
                        showsDisclosure: false,
                        action: onToggle
                    )
                    divider
                    actionRow(
                        title: "编辑任务",
                        detail: "调整工作流、Cron 与执行策略",
                        systemImage: "pencil",
                        tint: QuartetTheme.accentDeep,
                        identifier: "schedule-action-edit",
                        action: onEdit
                    )
                }
            }
            .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: 18, style: .continuous).stroke(QuartetTheme.divider.opacity(0.8)))

            if canWrite {
                actionRow(
                    title: "删除任务",
                    detail: "删除调度规则，不删除历史运行记录",
                    systemImage: "trash.fill",
                    tint: QuartetTheme.failed,
                    isDestructive: true,
                    identifier: "schedule-action-delete"
                ) {
                    content = .deleteConfirmation
                }
                .background(QuartetTheme.failed.opacity(0.07), in: RoundedRectangle(cornerRadius: 18, style: .continuous))
                .overlay(RoundedRectangle(cornerRadius: 18, style: .continuous).stroke(QuartetTheme.failed.opacity(0.18)))
            }
        }
    }

    private var deleteConfirmation: some View {
        VStack(spacing: 18) {
            HStack(alignment: .top, spacing: 14) {
                Image(systemName: "trash.fill")
                    .font(.quartet(.regular, weight: .semibold))
                    .foregroundStyle(QuartetTheme.failed)
                    .frame(width: 44, height: 44)
                    .background(QuartetTheme.failed.opacity(0.12), in: Circle())
                VStack(alignment: .leading, spacing: 5) {
                    Text("确定删除这个定时任务？")
                        .font(.quartet(.regular, weight: .semibold))
                        .foregroundStyle(QuartetTheme.primaryText)
                    Text("仅删除调度规则，已产生的历史运行记录会保留。此操作无法撤销。")
                        .font(.quartet(.detail))
                        .foregroundStyle(QuartetTheme.secondaryText)
                        .fixedSize(horizontal: false, vertical: true)
                }
            }
            .padding(16)
            .frame(maxWidth: .infinity, alignment: .leading)
            .background(QuartetTheme.failed.opacity(0.07), in: RoundedRectangle(cornerRadius: 18, style: .continuous))
            .overlay(RoundedRectangle(cornerRadius: 18, style: .continuous).stroke(QuartetTheme.failed.opacity(0.2)))

            HStack(spacing: 10) {
                sheetButton("保留任务", color: QuartetTheme.elevated, foreground: QuartetTheme.primaryText) {
                    content = .actions
                }
                sheetButton("确认删除", color: QuartetTheme.failed, foreground: QuartetTheme.onDanger, action: onDelete)
                    .accessibilityIdentifier("schedule-delete-confirm")
            }
        }
    }

    private var divider: some View {
        Divider().overlay(QuartetTheme.divider).padding(.leading, 62)
    }

    private func actionRow(
        title: String,
        detail: String,
        systemImage: String,
        tint: Color,
        isDestructive: Bool = false,
        identifier: String,
        showsDisclosure: Bool = true,
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
                    Text(title)
                        .font(.quartet(.control, weight: .semibold))
                        .foregroundStyle(isDestructive ? QuartetTheme.failed : QuartetTheme.primaryText)
                    Text(detail)
                        .font(.quartet(.detail))
                        .foregroundStyle(QuartetTheme.secondaryText)
                        .lineLimit(1)
                }
                Spacer(minLength: 8)
                if showsDisclosure {
                    Image(systemName: "chevron.right")
                        .font(.quartet(.compact, weight: .bold))
                        .foregroundStyle(QuartetTheme.secondaryText.opacity(0.7))
                }
            }
            .padding(.horizontal, 13)
            .frame(minHeight: 64)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .accessibilityIdentifier(identifier)
    }

    private func sheetButton(_ title: String, color: Color, foreground: Color, action: @escaping () -> Void) -> some View {
        Button(title, action: action)
            .font(.quartet(.control, weight: .semibold))
            .foregroundStyle(foreground)
            .frame(maxWidth: .infinity)
            .frame(height: 50)
            .background(color, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
            .buttonStyle(.plain)
    }
}

private struct ScheduleEditorView: View {
    @Environment(\.dismiss) private var dismiss
    let schedule: ScheduleInfo?
    let workflows: [GraphWorkflowSummary]
    let warnings: [GraphWorkflowWarning]
    let workspaces: [WorkspaceSummary]
    let onSave: (ScheduleMutationRequest) async throws -> Void

    @State private var name: String
    @State private var cronExpr: String
    @State private var enabled: Bool
    @State private var graphWorkflowId: String
    @State private var workspaceId: String
    @State private var maxConcurrent: Int
    @State private var timeout: Int
    @State private var isSaving = false
    @State private var error: PresentedError?
    @State private var picker: SchedulePickerTarget?

    init(
        schedule: ScheduleInfo?,
        workflows: [GraphWorkflowSummary],
        warnings: [GraphWorkflowWarning],
        workspaces: [WorkspaceSummary],
        onSave: @escaping (ScheduleMutationRequest) async throws -> Void
    ) {
        self.schedule = schedule
        self.workflows = workflows
        self.warnings = warnings
        self.workspaces = workspaces
        self.onSave = onSave
        _name = State(initialValue: schedule?.name ?? "")
        _cronExpr = State(initialValue: schedule?.cronExpr ?? "0 9 * * *")
        _enabled = State(initialValue: schedule?.enabled ?? true)
        _graphWorkflowId = State(initialValue: schedule?.graphWorkflowId ?? "")
        _workspaceId = State(initialValue: schedule?.workspaceId ?? "")
        _maxConcurrent = State(initialValue: max(schedule?.maxConcurrent ?? 1, 1))
        _timeout = State(initialValue: max(schedule?.timeout ?? 0, 0))
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(spacing: 14) {
                    editorCard("任务", systemImage: "calendar.badge.clock") {
                        editorTextField("任务名称", text: $name, identifier: "schedule-name")
                        cardDivider
                        selectionField(
                            title: "Graph Workflow",
                            value: selectedWorkflow?.name ?? "请选择",
                            identifier: "schedule-workflow-picker"
                        ) { picker = .workflow }
                        cardDivider
                        Toggle("启用", isOn: $enabled)
                            .font(.quartet(.control, weight: .medium))
                            .tint(QuartetTheme.accent)
                            .accessibilityIdentifier("schedule-enabled")
                    }

                    editorCard("执行计划", systemImage: "clock") {
                        editorTextField("Cron 表达式", text: $cronExpr, identifier: "schedule-cron", monospaced: true)
                        Text("格式：分 时 日 月 周，例如 0 9 * * * 表示每天 09:00。")
                            .font(.quartet(.detail))
                            .foregroundStyle(QuartetTheme.secondaryText)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }

                    editorCard("运行空间", systemImage: "folder") {
                        selectionField(
                            title: "绑定工作区",
                            value: selectedWorkspace?.displayName ?? "不绑定（使用默认工作区）",
                            identifier: "schedule-workspace-picker"
                        ) { picker = .workspace }
                        if let selectedWorkspace {
                            Text(selectedWorkspace.workdir)
                                .font(.system(.caption, design: .monospaced))
                                .foregroundStyle(QuartetTheme.secondaryText)
                                .frame(maxWidth: .infinity, alignment: .leading)
                        }
                    }

                    editorCard("执行策略", systemImage: "slider.horizontal.3") {
                        Stepper("最大并发：\(maxConcurrent)", value: $maxConcurrent, in: 1...32)
                            .accessibilityIdentifier("schedule-max-concurrent")
                        cardDivider
                        Stepper(timeout == 0 ? "超时：不限制" : "超时：\(timeout) 分钟", value: $timeout, in: 0...1440)
                            .accessibilityIdentifier("schedule-timeout")
                    }

                    if let schedule { runHistoryCard(schedule) }
                    if !warnings.isEmpty { warningsCard }
                }
                .padding(.horizontal, 18)
                .padding(.top, 10)
                .padding(.bottom, 18)
            }
            .scrollDismissesKeyboard(.interactively)
            .background(QuartetTheme.canvas)
            .navigationTitle(schedule == nil ? "新增定时任务" : "编辑定时任务")
            .navigationBarTitleDisplayMode(.inline)
            .safeAreaInset(edge: .bottom, spacing: 0) {
                Button { save() } label: {
                    HStack(spacing: 8) {
                        if isSaving { ProgressView().tint(QuartetTheme.onAccent) }
                        Text(isSaving ? "正在保存…" : "保存定时任务")
                            .font(.quartet(.control, weight: .semibold))
                    }
                    .foregroundStyle(QuartetTheme.onAccent)
                    .frame(maxWidth: .infinity)
                    .frame(height: 50)
                    .background(QuartetTheme.accent, in: RoundedRectangle(cornerRadius: 14, style: .continuous))
                }
                .buttonStyle(.plain)
                .disabled(!isValid || isSaving)
                .opacity(!isValid || isSaving ? 0.45 : 1)
                .accessibilityIdentifier("schedule-save")
                .padding(.horizontal, 18)
                .padding(.vertical, 10)
                .background(.ultraThinMaterial)
            }
            .interactiveDismissDisabled(isSaving)
        }
        .sheet(item: $error) {
            ScheduleErrorSheet(error: $0)
                .presentationDetents([.medium, .large])
                .quartetSheetStyle()
        }
        .sheet(item: $picker) { target in
            switch target {
            case .workflow:
                ScheduleChoiceSheet(
                    title: "选择 Graph Workflow",
                    choices: workflows.map { ScheduleChoice(id: $0.id, title: $0.name, detail: "\($0.nodeCount) 个节点 · \($0.edgeCount) 条连线") },
                    selection: $graphWorkflowId,
                    accessibilityPrefix: "schedule-workflow-option"
                )
                .presentationDetents([.medium, .large])
                .quartetSheetStyle()
            case .workspace:
                ScheduleChoiceSheet(
                    title: "选择运行空间",
                    choices: [ScheduleChoice(id: "", title: "不绑定（使用默认工作区）", detail: nil)] + workspaces.map { ScheduleChoice(id: $0.id, title: $0.displayName, detail: $0.workdir) },
                    selection: $workspaceId,
                    accessibilityPrefix: "schedule-workspace-option"
                )
                .presentationDetents([.medium, .large])
                .quartetSheetStyle()
            }
        }
    }

    private var selectedWorkflow: GraphWorkflowSummary? { workflows.first { $0.id == graphWorkflowId } }
    private var selectedWorkspace: WorkspaceSummary? { workspaces.first { $0.id == workspaceId } }

    private var isValid: Bool {
        !name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty &&
        !cronExpr.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty &&
        !graphWorkflowId.isEmpty
    }

    private func editorCard<Content: View>(_ title: String, systemImage: String, @ViewBuilder content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            Label(title, systemImage: systemImage)
                .font(.quartet(.control, weight: .semibold))
                .foregroundStyle(QuartetTheme.primaryText)
            content()
        }
        .padding(16)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 18, style: .continuous).stroke(QuartetTheme.divider.opacity(0.8)))
    }

    private func editorTextField(_ title: String, text: Binding<String>, identifier: String, monospaced: Bool = false) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(title)
                .font(.quartet(.detail, weight: .semibold))
                .foregroundStyle(QuartetTheme.secondaryText)
            TextField(title, text: text)
                .font(monospaced ? .system(.body, design: .monospaced) : .quartet(.regular))
                .textInputAutocapitalization(.never)
                .autocorrectionDisabled()
                .padding(.horizontal, 14)
                .frame(height: 48)
                .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
                .accessibilityIdentifier(identifier)
        }
    }

    private func selectionField(
        title: String,
        value: String,
        identifier: String,
        action: @escaping () -> Void
    ) -> some View {
        VStack(alignment: .leading, spacing: 6) {
            Text(title)
                .font(.quartet(.detail, weight: .semibold))
                .foregroundStyle(QuartetTheme.secondaryText)
            Button(action: action) {
                HStack {
                    Text(value)
                        .font(.quartet(.control, weight: .medium))
                        .foregroundStyle(graphWorkflowId.isEmpty && title == "Graph Workflow" ? QuartetTheme.secondaryText : QuartetTheme.primaryText)
                        .lineLimit(1)
                    Spacer()
                    Image(systemName: "chevron.up.chevron.down")
                        .font(.quartet(.compact, weight: .bold))
                        .foregroundStyle(QuartetTheme.secondaryText)
                }
                .padding(.horizontal, 14)
                .frame(height: 48)
                .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
            }
            .buttonStyle(.plain)
            .accessibilityIdentifier(identifier)
        }
    }

    private func runHistoryCard(_ schedule: ScheduleInfo) -> some View {
        editorCard("运行记录", systemImage: "clock.arrow.circlepath") {
            LabeledContent("运行次数", value: "\(schedule.runCount)")
            if let lastRunAt = schedule.lastRunAt { cardDivider; LabeledContent("上次运行", value: scheduleDate(lastRunAt)) }
            if let nextRunAt = schedule.nextRunAt, schedule.enabled { cardDivider; LabeledContent("下次运行", value: scheduleDate(nextRunAt)) }
            if let status = schedule.lastStatus, !status.isEmpty { cardDivider; LabeledContent("最近状态", value: status) }
            if let detail = schedule.lastTriggerError, !detail.isEmpty {
                cardDivider
                Text(detail)
                    .font(.system(.caption, design: .monospaced))
                    .foregroundStyle(QuartetTheme.failed)
                    .textSelection(.enabled)
            }
        }
        .font(.quartet(.control))
    }

    private var warningsCard: some View {
        editorCard("工作流读取警告", systemImage: "exclamationmark.triangle.fill") {
            ForEach(warnings, id: \.self) { warning in
                Text("\(warning.file)：\(warning.error)")
                    .font(.system(.caption, design: .monospaced))
                    .foregroundStyle(QuartetTheme.warning)
            }
        }
    }

    private var cardDivider: some View { Divider().overlay(QuartetTheme.divider) }

    private func save() {
        isSaving = true
        let request = ScheduleMutationRequest(
            name: name.trimmingCharacters(in: .whitespacesAndNewlines),
            cronExpr: cronExpr.trimmingCharacters(in: .whitespacesAndNewlines),
            enabled: enabled,
            graphWorkflowId: graphWorkflowId,
            workspaceId: workspaceId,
            workdir: selectedWorkspace?.workdir ?? "",
            maxConcurrent: maxConcurrent,
            timeout: timeout
        )
        Task { @MainActor in
            do {
                try await onSave(request)
                dismiss()
            } catch {
                self.error = presented(error, summary: "保存定时任务失败")
                isSaving = false
            }
        }
    }

    private func presented(_ caught: Error, summary: String) -> PresentedError {
        if let apiError = caught as? APIError { return PresentedError(title: apiError.summary, detail: apiError.detail) }
        return PresentedError(title: summary, detail: String(describing: caught))
    }
}

private enum SchedulePickerTarget: String, Identifiable {
    case workflow
    case workspace
    var id: String { rawValue }
}

private struct ScheduleChoice: Identifiable {
    let id: String
    let title: String
    let detail: String?
}

private struct ScheduleChoiceSheet: View {
    @Environment(\.dismiss) private var dismiss
    let title: String
    let choices: [ScheduleChoice]
    @Binding var selection: String
    let accessibilityPrefix: String

    var body: some View {
        NavigationStack {
            ScrollView {
                LazyVStack(spacing: 0) {
                    ForEach(choices) { choice in
                        Button {
                            selection = choice.id
                            dismiss()
                        } label: {
                            HStack(spacing: 12) {
                                Image(systemName: selection == choice.id ? "checkmark.circle.fill" : "circle")
                                    .font(.title3)
                                    .foregroundStyle(selection == choice.id ? QuartetTheme.accent : QuartetTheme.secondaryText)
                                VStack(alignment: .leading, spacing: 3) {
                                    Text(choice.title)
                                        .font(.quartet(.control, weight: .semibold))
                                        .foregroundStyle(QuartetTheme.primaryText)
                                    if let detail = choice.detail, !detail.isEmpty {
                                        Text(detail)
                                            .font(.quartet(.detail))
                                            .foregroundStyle(QuartetTheme.secondaryText)
                                            .lineLimit(2)
                                    }
                                }
                                Spacer()
                            }
                            .padding(.horizontal, 14)
                            .frame(minHeight: 60)
                            .contentShape(Rectangle())
                        }
                        .buttonStyle(.plain)
                        .accessibilityIdentifier("\(accessibilityPrefix)-\(choice.id)")
                        if choice.id != choices.last?.id {
                            Divider().overlay(QuartetTheme.divider).padding(.leading, 50)
                        }
                    }
                }
                .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
                .overlay(RoundedRectangle(cornerRadius: 18, style: .continuous).stroke(QuartetTheme.divider.opacity(0.8)))
                .padding(.horizontal, 20)
                .padding(.top, 8)
            }
            .background(QuartetTheme.canvas)
            .navigationTitle(title)
            .navigationBarTitleDisplayMode(.inline)
        }
    }
}

private struct ScheduleErrorSheet: View {
    @Environment(\.dismiss) private var dismiss
    let error: PresentedError

    var body: some View {
        NavigationStack {
            VStack(spacing: 18) {
                HStack(alignment: .top, spacing: 14) {
                    Image(systemName: "exclamationmark.triangle.fill")
                        .font(.quartet(.regular, weight: .semibold))
                        .foregroundStyle(QuartetTheme.failed)
                        .frame(width: 44, height: 44)
                        .background(QuartetTheme.failed.opacity(0.12), in: Circle())
                    ScrollView {
                        Text(error.detail)
                            .font(.system(.caption, design: .monospaced))
                            .foregroundStyle(QuartetTheme.primaryText)
                            .textSelection(.enabled)
                            .frame(maxWidth: .infinity, alignment: .leading)
                    }
                }
                .padding(16)
                .frame(maxWidth: .infinity, alignment: .leading)
                .background(QuartetTheme.failed.opacity(0.07), in: RoundedRectangle(cornerRadius: 18, style: .continuous))
                .overlay(RoundedRectangle(cornerRadius: 18, style: .continuous).stroke(QuartetTheme.failed.opacity(0.2)))

                HStack(spacing: 10) {
                    Button("复制错误") { UIPasteboard.general.string = error.detail }
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
                Spacer(minLength: 0)
            }
            .padding(.horizontal, 20)
            .padding(.top, 8)
            .background(QuartetTheme.canvas)
            .navigationTitle(error.title)
            .navigationBarTitleDisplayMode(.inline)
        }
    }
}

@MainActor
private func scheduleDate(_ milliseconds: Int64) -> String {
    let date = Date(timeIntervalSince1970: TimeInterval(milliseconds) / 1_000)
    return date.formatted(date: .abbreviated, time: .shortened)
}
