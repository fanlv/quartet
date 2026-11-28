import SwiftUI
import UIKit

struct ScheduledTasksView: View {
    @EnvironmentObject private var model: AppModel
    @Environment(\.mainTabBarInset) private var mainTabBarInset
    @Binding private var showsMainTabBar: Bool
    @State private var path = NavigationPath()
    @State private var schedules: [ScheduleInfo] = []
    @State private var workflows: [GraphWorkflowSummary] = []
    @State private var warnings: [GraphWorkflowWarning] = []
    @State private var agents: [AgentSummary] = []
    @State private var isLoading = true
    @State private var refreshRevision = 0
    @State private var editingSchedule: ScheduleInfo?
    @State private var isCreating = false
    @State private var actionSchedule: ScheduleInfo?
    @State private var pendingEditSchedule: ScheduleInfo?
    @State private var operationMessage: String?
    @State private var error: PresentedError?
    @State private var openingJobID: String?
    @State private var pendingEnable: ScheduleInfo?
    @State private var runIntents: [String: String] = [:]

    init(showsMainTabBar: Binding<Bool>) {
        _showsMainTabBar = showsMainTabBar
    }

    var body: some View {
        NavigationStack(path: $path) {
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
                        Text("创建定时任务，按计划运行 Prompt 或图工作流。")
                    } actions: {
                        if model.can("schedule.write") {
                            Button("新增定时任务") { isCreating = true }
                                .accessibilityIdentifier("schedule-empty-add")
                        }
                    }
                    .accessibilityIdentifier("schedule-empty")
                } else {
                    ScrollView {
                        LazyVStack(alignment: .leading, spacing: 0) {
                            if let operationMessage {
                                Label(operationMessage, systemImage: "checkmark.circle.fill")
                                    .font(.quartet(.control, weight: .semibold))
                                    .foregroundStyle(QuartetTheme.success)
                                    .frame(maxWidth: .infinity, alignment: .leading)
                                    .padding(.horizontal, 20)
                                    .padding(.vertical, 10)
                                    .accessibilityIdentifier("schedule-operation-status")
                            }

                            HStack(alignment: .firstTextBaseline) {
                                Text("全部任务")
                                    .font(.quartet(.regular, weight: .semibold))
                                    .foregroundStyle(QuartetTheme.primaryText)
                                Spacer()
                                Text("\(schedules.count) 个")
                                    .font(.quartet(.detail))
                                    .foregroundStyle(QuartetTheme.secondaryText)
                            }
                            .padding(.horizontal, 20)
                            .padding(.top, operationMessage == nil ? 14 : 4)
                            .padding(.bottom, 10)

                            ForEach(schedules) { schedule in
                                VStack(spacing: 0) {
                                    ScheduleCard(
                                        schedule: schedule,
                                        workflowName: workflowName(for: schedule),
                                        onShowActions: { actionSchedule = schedule }
                                    )

                                    if schedule.id != schedules.last?.id {
                                        Divider()
                                            .overlay(QuartetTheme.divider)
                                            .padding(.leading, 62)
                                    }
                                }
                                .background(QuartetTheme.surface)
                            }
                        }
                    }
                    .refreshable { await load() }
                }
            }
            .background(QuartetTheme.canvas)
            .mainTabBarBottomInset(mainTabBarInset)
            .quartetNavigationTitle("定时任务")
            .safeAreaInset(edge: .top, spacing: 0) {
                HStack {
                    NavigationLink(value: TimerHistoryRoute()) { Text("执行记录") }
                        .accessibilityIdentifier("timer-all-executions")
                    if model.can("job.read") {
                        NavigationLink(value: TimerHistoryRoute(sessions: true)) { Text("会话") }
                            .accessibilityIdentifier("timer-all-sessions")
                    }
                }
                .font(.quartet(.control, weight: .semibold))
                .foregroundStyle(QuartetTheme.accentDeep)
                .frame(minHeight: 44).frame(maxWidth: .infinity)
                .background(QuartetTheme.canvas)
            }
            .navigationDestination(for: TimerHistoryRoute.self) { route in
                TimerHistoryView(route: route, schedules: schedules) { jobID, sessionID in
                    openTimerJob(jobID, sessionID: sessionID)
                }
            }
            .navigationDestination(for: JobSummary.self) { summary in
                GraphRunView(summary: summary)
            }
            .navigationDestination(for: ChatRoute.self) { route in
                JobChatView(route: route)
            }
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
        .onAppear { setMainTabBarVisible(path.isEmpty) }
        .onChange(of: path.isEmpty) { _, isAtRoot in
            setMainTabBarVisible(isAtRoot)
        }
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
                canRun: model.can("schedule.execute") && canExecute(schedule.kind) &&
                    schedule.planState != "ended" && schedule.planState != "pendingApproval",
                canEnable: canExecute(schedule.kind) && schedule.approvalPreview != nil,
                canReadJobs: model.can("job.read"),
                onHistory: { sessions in
                    actionSchedule = nil
                    path.append(TimerHistoryRoute(scheduleId: schedule.id, sessions: sessions))
                },
                onRun: {
                    actionSchedule = nil
                    run(schedule)
                },
                onToggle: {
                    actionSchedule = nil
                    if schedule.enabled { toggle(schedule) } else { pendingEnable = schedule }
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
            .presentationDetents([.medium, .large])
            .quartetSheetStyle()
        }
        .sheet(item: $pendingEnable) { schedule in
            NavigationStack {
                ScrollView {
                    VStack(alignment: .leading, spacing: 12) {
                        Text(schedule.name).font(.quartet(.regular, weight: .semibold))
                        Text(schedule.prompt ?? [workflowName(for: schedule), schedule.workflowVersion].compactMap { $0 }.joined(separator: " · "))
                        Text([schedule.workspaceId, schedule.workdir, schedule.agentId, schedule.modelId].compactMap { $0 }.joined(separator: " · "))
                        Text([schedule.runAt ?? schedule.cronExpr, schedule.timeZone, schedule.endDate].compactMap { $0 }.joined(separator: " · "))
                        Button("确认并启用") { pendingEnable = nil; toggle(schedule) }
                            .accessibilityIdentifier("schedule-confirm-enable")
                    }
                    .font(.quartet(.control))
                    .foregroundStyle(QuartetTheme.primaryText)
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .padding(20)
                }
                .background(QuartetTheme.canvas)
                .quartetNavigationTitle("确认执行配置")
            }
            .presentationDetents([.medium, .large])
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
            agents: agents,
            canExecute: canExecute,
            onPreview: { request in try await model.apiClient().previewSchedule(id: schedule?.id, body: request) },
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
            let response = try await scheduleRequest
            guard !Task.isCancelled else { return }
            schedules = response.schedules.sorted { $0.updatedAt > $1.updatedAt }
            if model.can("workflow.read") {
                let workflowsResponse = try await client.graphWorkflows()
                guard !Task.isCancelled else { return }
                workflows = workflowsResponse.workflows.sorted {
                    $0.name.localizedStandardCompare($1.name) == .orderedAscending
                }
                warnings = workflowsResponse.warnings ?? []
            }
            if model.can("agent.read") {
                let response = try await client.agents()
                guard !Task.isCancelled else { return }
                agents = response.agentList
            }
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
                let updated = try await model.apiClient().setTimerEnabled(
                    id: schedule.id, enabled: !schedule.enabled, approval: schedule.approvalPreview
                )
                upsert(updated)
                operationMessage = AppLanguage.localizedFormat(updated.enabled ? "已在本机启用“%@”" : "已在本机停用“%@”", updated.name)
            } catch { present(error, summary: "切换任务状态失败") }
        }
    }

    private func run(_ schedule: ScheduleInfo) {
        Task { @MainActor in
            do {
                let intent = runIntents[schedule.id] ?? UUID().uuidString
                runIntents[schedule.id] = intent
                let response = try await model.apiClient().runSchedule(id: schedule.id, intent: intent)
                runIntents.removeValue(forKey: schedule.id)
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
        if schedule.kind == "prompt" { return "Prompt" }
        guard let id = schedule.graphWorkflowId, !id.isEmpty else { return "未绑定工作流".localizedForApp }
        return workflows.first(where: { $0.id == id })?.name ?? id
    }

    private func canExecute(_ kind: String?) -> Bool {
        model.can(kind == "prompt" ? "job.execute" : "workflow.execute")
    }

    private func openTimerJob(_ jobID: String, sessionID: String?) {
        guard openingJobID == nil else { return }
        openingJobID = jobID
        Task { @MainActor in
            defer { openingJobID = nil }
            do {
                let summary: JobSummary
                if let cached = model.jobSummary(id: jobID) {
                    summary = cached
                } else {
                    let detail = try await model.jobDetail(id: jobID)
                    let runTime = Int64((timerDateValue(detail.createdAt)?.timeIntervalSince1970 ?? 0) * 1000)
                    summary = JobSummary(
                        id: detail.id,
                        title: detail.title,
                        modelId: detail.firstModelId,
                        status: detail.status,
                        mode: detail.mode,
                        workspaceId: detail.workspaceId,
                        workdir: detail.workdir,
                        createdAt: runTime,
                        updatedAt: runTime,
                        pinnedAt: nil,
                        sessionCount: detail.sessionCount,
                        scheduleId: detail.scheduleId,
                        shareToken: nil,
                        agentId: detail.initialAgentId,
                        acpMode: detail.initialAcpMode,
                        acpThoughtLevel: detail.initialAcpThoughtLevel
                    )
                }
                setMainTabBarVisible(false)
                if summary.mode == "graph", sessionID == nil {
                    path.append(summary)
                } else {
                    path.append(ChatRoute(summary: summary, targetSessionID: sessionID))
                }
            } catch {
                present(error, summary: "最近执行 Job 加载失败".localizedForApp)
            }
        }
    }

    private func setMainTabBarVisible(_ isVisible: Bool) {
        var transaction = Transaction()
        transaction.disablesAnimations = true
        withTransaction(transaction) {
            showsMainTabBar = isVisible
        }
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
        HStack(alignment: .top, spacing: 8) {
            Button(action: onShowActions) {
                HStack(alignment: .top, spacing: 12) {
                    ZStack {
                        RoundedRectangle(cornerRadius: 8, style: .continuous)
                            .fill(statusColor.opacity(0.11))
                        RoundedRectangle(cornerRadius: 8, style: .continuous)
                            .strokeBorder(statusColor.opacity(0.28), lineWidth: 1)
                        Image(systemName: schedule.enabled ? "calendar.badge.clock" : "calendar.badge.minus")
                            .font(.quartet(.control, weight: .semibold))
                            .foregroundStyle(statusColor)
                    }
                    .frame(width: 34, height: 34)
                    .padding(.top, 1)
                    .accessibilityHidden(true)

                    VStack(alignment: .leading, spacing: 7) {
                        Text(schedule.name)
                            .font(.quartet(.control, weight: .semibold))
                            .foregroundStyle(QuartetTheme.primaryText)
                            .lineLimit(1)
                            .frame(maxWidth: .infinity, alignment: .leading)

                        VStack(alignment: .leading, spacing: 4) {
                            HStack(spacing: 5) {
                                Text(workflowName)
                                    .lineLimit(1)
                                    .layoutPriority(1)
                                metadataSeparator
                                Label(timerPlan(schedule), systemImage: "clock")
                                    .lineLimit(1)
                                    .font(.quartet(.compact, design: .monospaced))
                            }

                            HStack(spacing: 5) {
                                Text(timerPlanStatus(schedule))
                                    .foregroundStyle(schedule.enabled ? QuartetTheme.success : QuartetTheme.secondaryText)
                                if let nextRunAt = schedule.nextRunAt, schedule.enabled {
                                    metadataSeparator
                                    Text("下次 \(scheduleDate(nextRunAt, zone: schedule.timeZone))")
                                        .lineLimit(1)
                                } else if schedule.runCount > 0 {
                                    metadataSeparator
                                    Text("已运行 \(schedule.runCount) 次")
                                        .lineLimit(1)
                                }
                            }
                            .accessibilityElement(children: .combine)
                        }
                        .font(.quartet(.compact))
                        .foregroundStyle(QuartetTheme.secondaryText)
                    }
                }
                .frame(maxWidth: .infinity, alignment: .leading)
                .contentShape(Rectangle())
            }
            .buttonStyle(.plain)
            .frame(maxWidth: .infinity, alignment: .leading)
            .accessibilityLabel(scheduleAccessibilityLabel)
            .accessibilityHint("点按打开任务操作")
            .accessibilityIdentifier("schedule-row-\(schedule.name)")

            VStack(spacing: 0) {
                Button(action: onShowActions) {
                    Image(systemName: "ellipsis")
                        .font(.quartet(.control, weight: .semibold))
                        .foregroundStyle(QuartetTheme.secondaryText)
                        .frame(width: 36, height: 36)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .accessibilityLabel("\(schedule.name) 的任务操作")
                .accessibilityIdentifier("schedule-more-\(schedule.name)")


            }
        }
        .padding(.leading, 16)
        .padding(.trailing, 12)
        .padding(.vertical, 12)
        .frame(maxWidth: .infinity, alignment: .leading)
        .background(QuartetTheme.surface)
    }

    private var metadataSeparator: some View {
        Text("·")
            .foregroundStyle(QuartetTheme.secondaryText.opacity(0.72))
            .accessibilityHidden(true)
    }

    private var scheduleAccessibilityLabel: String {
        var values = [schedule.name, workflowName, timerPlan(schedule), timerPlanStatus(schedule)]
        if let nextRunAt = schedule.nextRunAt, schedule.enabled {
            values.append("下次运行 \(scheduleDate(nextRunAt, zone: schedule.timeZone))")
        } else if schedule.runCount > 0 {
            values.append("已运行 \(schedule.runCount) 次")
        }
        return values.joined(separator: "，")
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
    let canEnable: Bool
    let canReadJobs: Bool
    let onHistory: (Bool) -> Void
    let onRun: () -> Void
    let onToggle: () -> Void
    let onEdit: () -> Void
    let onDelete: () -> Void
    @State private var content: ScheduleActionContent = .actions

    var body: some View {
        NavigationStack {
            ScrollView {
                Group {
                    switch content {
                    case .actions: actions
                    case .deleteConfirmation: deleteConfirmation
                    }
                }
                .transition(.opacity.combined(with: .move(edge: .bottom)))
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
                actionRow(title: "执行记录", detail: "查看定时器的执行结果", systemImage: "clock.arrow.circlepath",
                          tint: QuartetTheme.accentDeep, identifier: "schedule-action-history",
                          showsDisclosure: true, action: { onHistory(false) })
                if canReadJobs {
                    divider
                    actionRow(title: "会话", detail: "查看执行产生的会话", systemImage: "bubble.left",
                              tint: QuartetTheme.accentDeep, identifier: "schedule-action-sessions",
                              showsDisclosure: true, action: { onHistory(true) })
                }
                divider
                if canRun {
                    actionRow(
                        title: "立即运行",
                        detail: "立即触发一次执行",
                        systemImage: "play.fill",
                        tint: QuartetTheme.accent,
                        identifier: "schedule-action-run",
                        showsDisclosure: false,
                        action: onRun
                    )
                }
                if canRun, canWrite { divider }
                if canWrite {
                    if schedule.enabled || canEnable {
                        actionRow(
                            title: schedule.enabled ? "在本机停用任务" : "在本机启用任务",
                            detail: schedule.enabled ? "暂停本机后续 Cron 自动触发" : "恢复本机后续 Cron 自动触发",
                            systemImage: schedule.enabled ? "pause.fill" : "play.circle.fill",
                            tint: QuartetTheme.warning,
                            identifier: "schedule-action-toggle",
                            showsDisclosure: false,
                            action: onToggle
                        )
                        divider
                    }
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

            DisclosureGroup("定时器详情") {
                VStack(alignment: .leading, spacing: 8) {
                    LabeledContent("定时器标识", value: schedule.id)
                    LabeledContent("执行计划", value: timerPlan(schedule))
                    if let timeZone = schedule.timeZone { LabeledContent("时区", value: timeZone) }
                    if let endDate = schedule.endDate { LabeledContent("结束日期", value: endDate) }
                    LabeledContent("运行次数", value: "\(schedule.runCount) / \(schedule.runAt != nil ? "1" : schedule.maxRuns.map(String.init) ?? "∞")")
                    LabeledContent("最大并发", value: String(schedule.maxConcurrent ?? 1))
                    LabeledContent("创建时间", value: scheduleDate(schedule.createdAt, zone: schedule.timeZone))
                    LabeledContent("更新时间", value: scheduleDate(schedule.updatedAt, zone: schedule.timeZone))
                    if let workdir = schedule.workdir { LabeledContent("工作目录", value: workdir) }
                    if let workspace = schedule.workspaceId { LabeledContent("工作区", value: workspace) }
                    if let workflow = schedule.graphWorkflowId { LabeledContent("Graph Workflow", value: workflow) }
                    if let agent = schedule.agentId { LabeledContent("Agent", value: agent) }
                    if let model = schedule.modelId { LabeledContent("模型", value: model) }
                    if schedule.kind == "prompt" {
                        LabeledContent("运行超时", value: String(schedule.timeout ?? 0))
                        if let prompt = schedule.prompt { Text(prompt).textSelection(.enabled) }
                    }
                    if let reason = schedule.stateReason { Text(reason).textSelection(.enabled) }
                }
                .padding(.top, 8)
            }
            .font(.quartet(.control))
            .foregroundStyle(QuartetTheme.primaryText)
            .padding(14)
            .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 14))
            .accessibilityIdentifier("schedule-metadata")

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
                    Text(title.localizedForApp)
                        .font(.quartet(.control, weight: .semibold))
                        .foregroundStyle(isDestructive ? QuartetTheme.failed : QuartetTheme.primaryText)
                    Text(detail.localizedForApp)
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
    let agents: [AgentSummary]
    let canExecute: (String?) -> Bool
    let onPreview: (ScheduleMutationRequest) async throws -> ScheduleInfo
    let onSave: (ScheduleMutationRequest) async throws -> Void

    @State private var name: String
    @State private var cronExpr: String
    @State private var frequency: String
    @State private var enabled: Bool
    @State private var graphWorkflowId: String
    @State private var kind: String
    @State private var prompt: String
    @State private var agentId: String
    @State private var modelId: String
    @State private var workdir: String
    @State private var single: Bool
    @State private var runAt: Date
    @State private var timeZone: String
    @State private var endDate: String
    @State private var maxRuns: String
    @State private var preview: ScheduleInfo?
    @State private var pendingRequest: ScheduleMutationRequest?
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
        agents: [AgentSummary],
        canExecute: @escaping (String?) -> Bool,
        onPreview: @escaping (ScheduleMutationRequest) async throws -> ScheduleInfo,
        onSave: @escaping (ScheduleMutationRequest) async throws -> Void
    ) {
        self.schedule = schedule
        self.workflows = workflows
        self.warnings = warnings
        self.workspaces = workspaces
        self.agents = agents
        self.canExecute = canExecute
        self.onPreview = onPreview
        self.onSave = onSave
        _name = State(initialValue: schedule?.name ?? "")
        let initialCron = schedule?.cronExpr.isEmpty == false ? schedule?.cronExpr ?? "0 9 * * *" : "0 9 * * *"
        _cronExpr = State(initialValue: initialCron)
        _frequency = State(initialValue: ["0 * * * *", "0 9 * * *", "0 9 * * 1"].contains(initialCron) ? initialCron : "")
        _enabled = State(initialValue: schedule?.enabled ?? true)
        _graphWorkflowId = State(initialValue: schedule?.graphWorkflowId ?? "")
        _kind = State(initialValue: schedule?.kind ?? "graph")
        _prompt = State(initialValue: schedule?.prompt ?? "")
        _agentId = State(initialValue: schedule?.agentId ?? "")
        _modelId = State(initialValue: schedule?.modelId ?? "")
        _workdir = State(initialValue: schedule?.workdir ?? "")
        _single = State(initialValue: schedule?.runAt != nil)
        _runAt = State(initialValue: schedule?.runAt.flatMap(timerDateValue) ?? Date().addingTimeInterval(3600))
        _timeZone = State(initialValue: schedule?.timeZone ?? "")
        _endDate = State(initialValue: schedule?.endDate ?? "")
        _maxRuns = State(initialValue: schedule?.maxRuns.map(String.init) ?? "")
        _workspaceId = State(initialValue: schedule?.workspaceId ?? "")
        _maxConcurrent = State(initialValue: max(schedule?.maxConcurrent ?? 1, 1))
        _timeout = State(initialValue: max(schedule?.timeout ?? 0, 0))
    }

    var body: some View {
        NavigationStack {
            ScrollView {
                VStack(spacing: 12) {
                    if let preview {
                        editorCard("确认执行配置", systemImage: "checkmark.circle") {
                            Text(preview.name)
                            Text(preview.prompt ?? [selectedWorkflow?.name ?? preview.graphWorkflowId, preview.workflowVersion].compactMap { $0 }.joined(separator: " · "))
                            Text([preview.workspaceId, preview.workdir, preview.agentId, preview.modelId].compactMap { $0 }.joined(separator: " · "))
                            Text([timerPlan(preview), preview.timeZone, preview.endDate].compactMap { $0 }.joined(separator: " · "))
                            LabeledContent("最大并发", value: String(preview.maxConcurrent ?? 1))
                            LabeledContent("最大执行次数", value: String(preview.maxRuns ?? 0))
                            if preview.kind == "prompt" { LabeledContent("运行超时", value: String(preview.timeout ?? 0)) }
                            Button("返回编辑") { self.preview = nil; pendingRequest = nil }
                                .accessibilityIdentifier("schedule-back-to-edit")
                        }
                    } else {
                        editorCard("任务", systemImage: "calendar.badge.clock") {
                            editorTextField("任务名称", text: $name, identifier: "schedule-name")
                            Picker("执行类型", selection: $kind) {
                                Text("Graph Workflow").tag("graph")
                                Text("Prompt").tag("prompt")
                            }
                            .pickerStyle(.segmented)
                            .accessibilityIdentifier("schedule-kind")
                            if kind == "graph" {
                                selectionField(title: "Graph Workflow", value: selectedWorkflow?.name ?? "请选择",
                                               identifier: "schedule-workflow-picker") { picker = .workflow }
                            } else {
                                TextField("Prompt", text: $prompt, axis: .vertical)
                                    .font(.quartet(.control))
                                    .padding(12)
                                    .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 12))
                                    .accessibilityIdentifier("schedule-prompt")
                                selectionField(title: "Agent", value: selectedAgent?.displayName ?? (agentId.isEmpty ? "工作区默认值" : agentId),
                                               identifier: "schedule-agent") { picker = .agent }
                                if selectedAgent?.models?.availableModels.isEmpty == false {
                                    selectionField(title: "模型", value: modelId.isEmpty ? "工作区默认值" : modelId,
                                                   identifier: "schedule-model") { picker = .model }
                                } else {
                                    editorTextField("模型 ID（可用工作区默认值）", text: $modelId, identifier: "schedule-model")
                                }
                            }
                            cardDivider
                            Toggle("在本机启用", isOn: $enabled)
                                .font(.quartet(.control, weight: .medium))
                                .foregroundStyle(QuartetTheme.primaryText)
                                .toggleStyle(QuartetCheckmarkToggleStyle())
                                .disabled(!canExecute(kind) && schedule?.enabled != true)
                                .accessibilityIdentifier("schedule-enabled")
                        }

                        editorCard("执行计划", systemImage: "clock") {
                            Picker("执行计划", selection: $single) {
                                Text("重复").tag(false)
                                Text("单次").tag(true)
                            }
                            .pickerStyle(.segmented)
                            .accessibilityIdentifier("schedule-timing")
                            editorTextField("时区（留空使用后端默认）", text: $timeZone, identifier: "schedule-time-zone")
                            if single {
                                DatePicker("计划时间", selection: $runAt, displayedComponents: [.date, .hourAndMinute])
                                    .environment(\.timeZone, TimeZone(identifier: timeZone) ?? .current)
                                    .accessibilityIdentifier("schedule-once")
                            } else {
                                Picker("重复频率", selection: $frequency) {
                                    Text("每小时").tag("0 * * * *")
                                    Text("每天 09:00").tag("0 9 * * *")
                                    Text("每周一 09:00").tag("0 9 * * 1")
                                    Text("自定义 Cron").tag("")
                                }
                                .font(.quartet(.control))
                                .accessibilityIdentifier("schedule-frequency")
                                if frequency.isEmpty {
                                    editorTextField("Cron 表达式", text: $cronExpr, identifier: "schedule-cron", monospaced: true)
                                }
                                editorTextField("结束日期（YYYY-MM-DD）", text: $endDate, identifier: "schedule-end-date")
                                editorTextField("最大执行次数", text: $maxRuns, identifier: "schedule-max-runs")
                            }
                        }

                        editorCard("运行空间", systemImage: "folder") {
                            selectionField(title: "绑定工作区",
                                           value: selectedWorkspace?.displayName ?? "不绑定（使用默认工作区）",
                                           identifier: "schedule-workspace-picker") { picker = .workspace }
                            editorTextField("工作目录（留空使用工作区目录）", text: $workdir, identifier: "schedule-workdir")
                        }

                        editorCard("执行策略", systemImage: "slider.horizontal.3") {
                            strategyControl(title: "最大并发", detail: "允许同时运行的任务数",
                                            valueText: "\(maxConcurrent)", value: $maxConcurrent, range: 1...32,
                                            identifier: "schedule-max-concurrent")
                            if kind == "prompt" {
                                cardDivider
                                strategyControl(title: "运行超时", detail: "设为 0 时不限制运行时间",
                                                valueText: timeout == 0 ? "不限制" : "\(timeout) 分钟",
                                                value: $timeout, range: 0...525600, identifier: "schedule-timeout")
                            }
                        }
                        if let schedule { runHistoryCard(schedule) }
                    }
                    if !warnings.isEmpty { warningsCard }
                }
                .padding(.horizontal, 16)
                .padding(.top, 10)
                .padding(.bottom, 18)
            }
            .scrollDismissesKeyboard(.interactively)
            .background(QuartetTheme.canvas)
            .quartetNavigationTitle(schedule == nil ? "新增定时任务" : "编辑定时任务")
            .safeAreaInset(edge: .bottom, spacing: 0) {
                Button { save() } label: {
                    HStack(spacing: 8) {
                        if isSaving { ProgressView().tint(QuartetTheme.onAccent) }
                        Text(isSaving ? "正在保存…" : preview == nil ? "保存定时任务" : "确认并启用")
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
                .padding(.horizontal, 16)
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
        .onChange(of: workspaceId) { _, _ in workdir = "" }
        .onChange(of: kind) { _, newKind in
            if schedule == nil { enabled = canExecute(newKind) }
        }
        .onChange(of: agentId) { _, _ in modelId = "" }
        .onChange(of: frequency) { _, rule in if !rule.isEmpty { cronExpr = rule } }
        .onAppear { if schedule == nil { enabled = canExecute(kind) } }
        .sheet(item: $picker) { target in
            switch target {
            case .agent:
                ScheduleChoiceSheet(title: "选择 Agent", choices: defaultChoice + agents.map {
                    ScheduleChoice(id: $0.id, title: $0.displayName, detail: $0.id)
                }, selection: $agentId, accessibilityPrefix: "schedule-agent-option")
                    .presentationDetents([.medium, .large]).quartetSheetStyle()
            case .model:
                ScheduleChoiceSheet(title: "选择模型", choices: defaultChoice + (selectedAgent?.models?.availableModels ?? []).map {
                    ScheduleChoice(id: $0.id, title: $0.name, detail: $0.description)
                }, selection: $modelId, accessibilityPrefix: "schedule-model-option")
                    .presentationDetents([.medium, .large]).quartetSheetStyle()
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

    private var defaultChoice: [ScheduleChoice] {
        [ScheduleChoice(id: "", title: "工作区默认值".localizedForApp, detail: nil)]
    }
    private var selectedAgent: AgentSummary? { agents.first { $0.id == agentId } }
    private var selectedWorkflow: GraphWorkflowSummary? { workflows.first { $0.id == graphWorkflowId } }
    private var selectedWorkspace: WorkspaceSummary? { workspaces.first { $0.id == workspaceId } }

    private var isValid: Bool {
        !name.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty &&
        (single || !cronExpr.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty) &&
        (kind == "graph" ? !graphWorkflowId.isEmpty : !prompt.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty) &&
        (single || maxRuns.isEmpty || Int(maxRuns).map { $0 >= 0 } == true)

    }

    private func editorCard<Content: View>(_ title: String, systemImage: String, @ViewBuilder content: () -> Content) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            Label(title.localizedForApp, systemImage: systemImage)
                .font(.quartet(.regular, weight: .semibold))
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
            Text(title.localizedForApp)
                .font(.quartet(.detail, weight: .semibold))
                .foregroundStyle(QuartetTheme.secondaryText)
            TextField(title, text: text)
                .font(monospaced ? .quartet(.control, weight: .medium, design: .monospaced) : .quartet(.control, weight: .medium))
                .foregroundStyle(QuartetTheme.primaryText)
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
            Text(title.localizedForApp)
                .font(.quartet(.detail, weight: .semibold))
                .foregroundStyle(QuartetTheme.secondaryText)
            Button(action: action) {
                HStack {
                    Text(value.localizedForApp)
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

    private func strategyControl(
        title: String,
        detail: String,
        valueText: String,
        value: Binding<Int>,
        range: ClosedRange<Int>,
        identifier: String
    ) -> some View {
        ViewThatFits(in: .horizontal) {
            HStack(spacing: 12) {
                strategyDescription(title: title, detail: detail)
                Spacer(minLength: 8)
                strategyValueControl(valueText: valueText, value: value, range: range, title: title)
            }

            VStack(alignment: .leading, spacing: 4) {
                strategyDescription(title: title, detail: detail)
                strategyValueControl(valueText: valueText, value: value, range: range, title: title)
                    .frame(maxWidth: .infinity, alignment: .trailing)
            }
        }
        .frame(minHeight: 52)
        .accessibilityIdentifier(identifier)
    }

    private func strategyDescription(title: String, detail: String) -> some View {
        VStack(alignment: .leading, spacing: 3) {
            Text(title.localizedForApp)
                .font(.quartet(.control, weight: .medium))
                .foregroundStyle(QuartetTheme.primaryText)
            Text(detail.localizedForApp)
                .font(.quartet(.detail))
                .foregroundStyle(QuartetTheme.secondaryText)
                .fixedSize(horizontal: false, vertical: true)
        }
    }

    private func strategyValueControl(
        valueText: String,
        value: Binding<Int>,
        range: ClosedRange<Int>,
        title: String
    ) -> some View {
        HStack(spacing: 0) {
            strategyButton(
                systemImage: "minus",
                accessibilityLabel: "减少\(title)",
                isEnabled: value.wrappedValue > range.lowerBound
            ) {
                value.wrappedValue = max(range.lowerBound, value.wrappedValue - 1)
            }

            Text(valueText.localizedForApp)
                .font(.quartet(.control, weight: .semibold, design: .monospaced))
                .foregroundStyle(QuartetTheme.primaryText)
                .multilineTextAlignment(.center)
                .frame(minWidth: 54)

            strategyButton(
                systemImage: "plus",
                accessibilityLabel: "增加\(title)",
                isEnabled: value.wrappedValue < range.upperBound
            ) {
                value.wrappedValue = min(range.upperBound, value.wrappedValue + 1)
            }
        }
        .fixedSize(horizontal: true, vertical: false)
    }

    private func strategyButton(
        systemImage: String,
        accessibilityLabel: String,
        isEnabled: Bool,
        action: @escaping () -> Void
    ) -> some View {
        Button(action: action) {
            Image(systemName: systemImage)
                .font(.quartet(.control, weight: .semibold))
                .foregroundStyle(QuartetTheme.accentDeep)
                .frame(width: 44, height: 44)
                .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
        .disabled(!isEnabled)
        .opacity(isEnabled ? 1 : 0.3)
        .accessibilityLabel(accessibilityLabel)
    }

    private func runHistoryCard(_ schedule: ScheduleInfo) -> some View {
        editorCard("运行记录", systemImage: "clock.arrow.circlepath") {
            LabeledContent("运行次数", value: "\(schedule.runCount)")
            if let lastRunAt = schedule.lastRunAt { cardDivider; LabeledContent("上次运行", value: scheduleDate(lastRunAt)) }
            if let nextRunAt = schedule.nextRunAt, schedule.enabled { cardDivider; LabeledContent("下次运行", value: scheduleDate(nextRunAt, zone: schedule.timeZone)) }
            if let status = schedule.lastStatus, !status.isEmpty { cardDivider; LabeledContent("最近状态", value: status) }
            if let detail = schedule.lastTriggerError, !detail.isEmpty {
                cardDivider
                Text(detail)
                    .font(.quartet(.detail, design: .monospaced))
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
                    .font(.quartet(.detail, design: .monospaced))
                    .foregroundStyle(QuartetTheme.warning)
            }
        }
    }

    private var cardDivider: some View { Divider().overlay(QuartetTheme.divider) }

    private func save() {
        isSaving = true
        let formatter = DateFormatter()
        formatter.locale = Locale(identifier: "en_US_POSIX")
        formatter.calendar = Calendar(identifier: .gregorian)
        formatter.timeZone = TimeZone(identifier: timeZone) ?? .current
        formatter.dateFormat = "yyyy-MM-dd'T'HH:mm"
        let request = ScheduleMutationRequest(
            name: name.trimmingCharacters(in: .whitespacesAndNewlines),
            cronExpr: single ? "" : cronExpr.trimmingCharacters(in: .whitespacesAndNewlines),
            enabled: enabled,
            graphWorkflowId: graphWorkflowId,
            workspaceId: workspaceId,
            workdir: workdir,
            maxConcurrent: maxConcurrent,
            timeout: timeout,
            kind: kind, prompt: prompt, agentId: agentId, modelId: modelId,
            runAt: single ? formatter.string(from: runAt) : "", timeZone: timeZone,
            endDate: single ? "" : endDate, maxRuns: single ? 0 : Int(maxRuns) ?? 0
        )
        Task { @MainActor in
            defer { isSaving = false }
            do {
                if let pendingRequest {
                    try await onSave(pendingRequest)
                    dismiss()
                } else if request.enabled && canExecute(kind) {
                    let resolved = try await onPreview(request)
                    var resolvedRequest = request
                    resolvedRequest.expectedApproval = resolved.approvalPreview
                    preview = resolved
                    pendingRequest = resolvedRequest
                } else {
                    try await onSave(request)
                    dismiss()
                }
            } catch {
                self.error = presented(error, summary: "保存定时任务失败")
            }
        }
    }

    private func presented(_ caught: Error, summary: String) -> PresentedError {
        if let apiError = caught as? APIError { return PresentedError(title: apiError.summary, detail: apiError.detail) }
        return PresentedError(title: summary, detail: String(describing: caught))
    }
}

private enum SchedulePickerTarget: String, Identifiable {
    case agent
    case model
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
                                    .font(.quartet(.regular, weight: .medium))
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
            .quartetNavigationTitle(title)
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
                            .font(.quartet(.detail, design: .monospaced))
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
            .quartetNavigationTitle(error.title.localizedForApp)
        }
    }
}

@MainActor
private func scheduleDate(_ milliseconds: Int64, zone: String? = nil) -> String {
    timerDateLabel(Date(timeIntervalSince1970: Double(milliseconds) / 1000), zone: zone)
}

private func timerDateValue(_ value: String) -> Date? {
    let formatter = ISO8601DateFormatter()
    formatter.formatOptions = [.withInternetDateTime, .withFractionalSeconds]
    if let date = formatter.date(from: value) { return date }
    formatter.formatOptions = [.withInternetDateTime]
    return formatter.date(from: value)
}

private func timerDateLabel(_ date: Date, zone: String?) -> String {
    let formatter = DateFormatter()
    formatter.dateStyle = .medium
    formatter.timeStyle = .short
    if let zone, let location = TimeZone(identifier: zone) { formatter.timeZone = location }
    return formatter.string(from: date)
}

private func timerPlan(_ schedule: ScheduleInfo) -> String {
    guard let value = schedule.runAt, let date = timerDateValue(value) else { return schedule.cronExpr }
    return timerDateLabel(date, zone: schedule.timeZone)
}

private func timerPlanStatus(_ schedule: ScheduleInfo) -> String {
    switch schedule.planState {
    case "pendingApproval": return "待确认".localizedForApp
    case "ended": return "已结束".localizedForApp
    default: return (schedule.enabled ? "本机已启用" : "本机已停用").localizedForApp
    }
}

private struct TimerHistoryRoute: Hashable {
    var scheduleId: String?
    var executionId: String?
    var sessions = false
}

private struct TimerHistoryView: View {
    @EnvironmentObject private var model: AppModel
    @Environment(\.scenePhase) private var scenePhase
    let route: TimerHistoryRoute
    let schedules: [ScheduleInfo]
    let openJob: (String, String?) -> Void

    @State private var scheduleId = ""
    @State private var workspaceId = ""
    @State private var status = ""
    @State private var executions: [TimerExecution] = []
    @State private var sessions: [TimerSession] = []
    @State private var cursor = ""
    @State private var requestId = UUID()
    @State private var loading = true
    @State private var error: PresentedError?
    @State private var filter: TimerHistoryFilter?

    private var filterKey: [String] { [scheduleId, workspaceId, status] }

    var body: some View {
        ScrollView {
            LazyVStack(alignment: .leading, spacing: 12) {
                HStack(spacing: 10) {
                    if route.scheduleId == nil {
                        filterButton(scheduleId.isEmpty ? "全部定时器" : schedules.first { $0.id == scheduleId }?.name ?? scheduleId,
                                     "timer-filter-schedule", .schedule)
                    }
                    if !route.sessions && route.executionId == nil {
                        filterButton(workspaceId.isEmpty ? "全部工作区" : model.workspaces.first { $0.id == workspaceId }?.displayName ?? workspaceId,
                                     "timer-filter-workspace", .workspace)
                        filterButton(status.isEmpty ? "全部状态" : timerExecutionStatus(status), "timer-filter-status", .status)
                    }
                }
                if route.sessions {
                    ForEach(sessions) { session in sessionRow(session) }
                } else {
                    ForEach(executions) { execution in executionRow(execution) }
                }
                if loading { ProgressView().tint(QuartetTheme.accent) }
                else if executions.isEmpty && sessions.isEmpty {
                    Text("暂无记录")
                        .font(.quartet(.control))
                        .foregroundStyle(QuartetTheme.secondaryText)
                }
                if !loading && !cursor.isEmpty {
                    Button("加载更多") { Task { await load(reset: false) } }
                        .accessibilityIdentifier("timer-load-more")
                }
            }
            .padding(16)
        }
        .background(QuartetTheme.canvas)
        .quartetNavigationTitle(route.sessions ? "会话" : "执行记录")
        .quartetPlainNavigationBackButton()
        .refreshable { await load(reset: true) }
        .task(id: filterKey) { await load(reset: true) }
        .onChange(of: scenePhase) { _, phase in
            if phase == .active { Task { await load(reset: true) } }
        }
        .sheet(item: $filter) { field in
            ScheduleChoiceSheet(title: field.title, choices: choices(field), selection: binding(field),
                                accessibilityPrefix: "timer-history-choice")
                .presentationDetents([.medium, .large])
                .quartetSheetStyle()
        }
        .sheet(item: $error) {
            ScheduleErrorSheet(error: $0).presentationDetents([.medium, .large]).quartetSheetStyle()
        }
    }

    private func filterButton(_ title: String, _ identifier: String, _ target: TimerHistoryFilter) -> some View {
        Button { filter = target } label: {
            Text(title.localizedForApp)
                .font(.quartet(.detail, weight: .semibold))
                .foregroundStyle(QuartetTheme.accentDeep)
                .frame(minHeight: 44)
        }
        .accessibilityIdentifier(identifier)
    }

    private func choices(_ field: TimerHistoryFilter) -> [ScheduleChoice] {
        let all = ScheduleChoice(id: "", title: "全部".localizedForApp, detail: nil)
        switch field {
        case .schedule: return [all] + schedules.map { ScheduleChoice(id: $0.id, title: $0.name, detail: nil) }
        case .workspace: return [all] + model.workspaces.map { ScheduleChoice(id: $0.id, title: $0.displayName, detail: nil) }
        case .status:
            let values = ["accepted", "pending", "running", "completed", "failed", "stopped", "interrupted", "missed", "skipped"]
            return [all] + values.map { ScheduleChoice(id: $0, title: timerExecutionStatus($0), detail: nil) }
        }
    }

    private func binding(_ field: TimerHistoryFilter) -> Binding<String> {
        switch field {
        case .schedule: return $scheduleId
        case .workspace: return $workspaceId
        case .status: return $status
        }
    }

    private func executionRow(_ execution: TimerExecution) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(execution.name).font(.quartet(.control, weight: .semibold))
                .accessibilityIdentifier("timer-execution-\(execution.id)")
            Text(timerExecutionStatus(execution.status))
                .foregroundStyle(QuartetTheme.statusColor(execution.status))
            Text([execution.kind == "prompt" ? "Prompt" : "Graph Workflow",
                  model.workspaces.first { $0.id == execution.workspaceId }?.displayName ?? execution.workspaceId,
                  execution.trigger == "manual" ? "立即运行".localizedForApp : "自动运行".localizedForApp]
                .compactMap { $0 }.joined(separator: " · "))
                .foregroundStyle(QuartetTheme.secondaryText)
            if let date = execution.scheduledAt { LabeledContent("计划时间", value: formatted(date)) }
            if let through = execution.missedThrough { LabeledContent("截至", value: formatted(through)) }
            if let date = execution.startedAt { LabeledContent("开始时间", value: formatted(date)) }
            if let date = execution.finishedAt { LabeledContent("结束时间", value: formatted(date)) }
            if let config = execution.configuration {
                DisclosureGroup("执行配置") {
                    VStack(alignment: .leading, spacing: 8) {
                        if let prompt = config.prompt { Text(prompt).textSelection(.enabled) }
                        if let workflow = config.graphWorkflowId { LabeledContent("Graph Workflow", value: workflow) }
                        if let version = config.workflowVersion { LabeledContent("工作流版本", value: version) }
                        if let workdir = config.workdir { LabeledContent("工作目录", value: workdir) }
                        if let agent = config.agentId { LabeledContent("Agent", value: agent) }
                        if let model = config.modelId { LabeledContent("模型", value: model) }
                        if let zone = config.timeZone { LabeledContent("时区", value: zone) }
                        Text(config.runAt.map { formatted($0) } ?? config.cronExpr)
                        if let end = config.endDate { LabeledContent("结束日期", value: end) }
                        LabeledContent("最大并发", value: String(config.maxConcurrent ?? 1))
                        if execution.kind == "prompt" { LabeledContent("运行超时", value: String(config.timeout ?? 0)) }
                    }
                    .padding(.top, 8)
                }
            }
            if let error = execution.error { Text(error).textSelection(.enabled).foregroundStyle(QuartetTheme.failed) }
            if let error = execution.detailsError { Text(error).textSelection(.enabled).foregroundStyle(QuartetTheme.secondaryText) }
            if route.scheduleId == nil {
                NavigationLink(value: TimerHistoryRoute(scheduleId: execution.scheduleId)) {
                    Text("查看此定时器的记录").frame(minHeight: 44)
                }
                .accessibilityIdentifier("timer-filter-execution-\(execution.id)")
            }
            if let jobID = execution.jobId, execution.detailsError == nil, model.can("job.read") {
                HStack {
                    Button("打开 Job") { openJob(jobID, nil) }
                        .accessibilityIdentifier("timer-open-job-\(execution.id)")
                    NavigationLink(value: TimerHistoryRoute(scheduleId: execution.scheduleId, executionId: execution.id, sessions: true)) {
                        Text("会话")
                    }
                    .accessibilityIdentifier("timer-execution-sessions-\(execution.id)")
                }
                .foregroundStyle(QuartetTheme.accentDeep)
                .frame(minHeight: 44)
            }
        }
        .timerRecordStyle()
    }

    private func sessionRow(_ session: TimerSession) -> some View {
        VStack(alignment: .leading, spacing: 8) {
            Button { openJob(session.jobId, session.id) } label: {
                Text(session.title.isEmpty ? session.id : session.title)
                    .font(.quartet(.control, weight: .semibold))
                    .frame(minHeight: 44)
            }
            .accessibilityIdentifier("timer-open-session-\(session.id)")
            Text([session.scheduleName, session.agentId, session.modelId, session.nodeName ?? session.nodeId]
                .compactMap { $0 }.joined(separator: " · "))
                .foregroundStyle(QuartetTheme.secondaryText)
            if let loop = session.loop, !loop.isEmpty {
                LabeledContent("循环轮次", value: loop.map { String($0.index) }.joined(separator: " / "))
            }
            Text(formatted(session.createdAt))
            NavigationLink(value: TimerHistoryRoute(scheduleId: session.scheduleId, executionId: session.executionId)) {
                Text("返回执行记录").frame(minHeight: 44)
            }
            .accessibilityIdentifier("timer-return-execution-\(session.id)")
        }
        .timerRecordStyle()
    }

    private func formatted(_ value: String) -> String {
        let zone = schedules.first { $0.id == (route.scheduleId ?? scheduleId) }?.timeZone
        return timerDateValue(value).map { timerDateLabel($0, zone: zone) } ?? value
    }

    @MainActor
    private func load(reset: Bool) async {
        let id = UUID()
        requestId = id
        loading = true
        if reset { cursor = "" }
        defer { if requestId == id { loading = false } }
        do {
            let client = try model.apiClient()
            let plan = route.scheduleId ?? (scheduleId.isEmpty ? nil : scheduleId)
            if route.sessions {
                let page = try await client.timerSessions(scheduleId: plan, executionId: route.executionId, cursor: cursor)
                guard requestId == id, !Task.isCancelled else { return }
                sessions = reset ? page.sessions : sessions + page.sessions
                cursor = page.nextCursor
            } else {
                let page = try await client.timerExecutions(scheduleId: plan, executionId: route.executionId,
                                                           workspaceId: workspaceId, status: status, cursor: cursor)
                guard requestId == id, !Task.isCancelled else { return }
                executions = reset ? page.executions : executions + page.executions
                cursor = page.nextCursor
            }
        } catch {
            guard requestId == id, !Task.isCancelled else { return }
            if let apiError = error as? APIError {
                self.error = PresentedError(title: apiError.summary, detail: apiError.detail)
            } else {
                self.error = PresentedError(title: "定时任务加载失败".localizedForApp, detail: String(describing: error))
            }
        }
    }
}

private enum TimerHistoryFilter: String, Identifiable {
    case schedule, workspace, status
    var id: String { rawValue }
    var title: String {
        switch self {
        case .schedule: "选择定时器"
        case .workspace: "选择运行空间"
        case .status: "选择执行状态"
        }
    }
}

private extension View {
    func timerRecordStyle() -> some View {
        self.font(.quartet(.control))
            .foregroundStyle(QuartetTheme.primaryText)
            .frame(maxWidth: .infinity, alignment: .leading)
            .padding(16)
            .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 14))
            .overlay(RoundedRectangle(cornerRadius: 14).strokeBorder(QuartetTheme.divider))
    }
}

private func timerExecutionStatus(_ status: String) -> String {
    switch status {
    case "accepted": "已受理".localizedForApp
    case "dispatching": "正在启动".localizedForApp
    case "pending": "待启动".localizedForApp
    case "running": "运行中".localizedForApp
    case "completed": "已完成".localizedForApp
    case "failed": "失败".localizedForApp
    case "stopped": "已停止".localizedForApp
    case "interrupted": "已中断".localizedForApp
    case "missed": "已错过".localizedForApp
    case "skipped": "并发跳过".localizedForApp
    default: status
    }
}
