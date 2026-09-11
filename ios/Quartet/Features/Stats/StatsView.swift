import Charts
import SwiftUI
import UIKit

struct StatsView: View {
    @EnvironmentObject private var model: AppModel
    @Environment(\.locale) private var locale
    @Environment(\.mainTabBarInset) private var mainTabBarInset
    @ObservedObject private var agentUsageStore = AgentUsageSummaryStore.shared

    @State private var preset: StatsRangePreset = .sevenDays
    @State private var customFrom = Calendar.current.date(byAdding: .day, value: -29, to: Date()) ?? Date()
    @State private var customTo = Date()
    @State private var metric: StatsTrendMetric = .tokens
    @State private var report: UsageStatsReport?
    @State private var isLoading = false
    @State private var errorDetail: String?
    @State private var refreshRevision = 0
    @State private var requestSequence: UInt64 = 0
    @State private var agents: [AgentSummary] = []
    @State private var isLoadingAgentUsage = false
    @State private var agentCatalogError: String?
    @State private var agentRequestSequence: UInt64 = 0
    @State private var agentNamespace = ""

    var body: some View {
        NavigationStack {
            ScrollView {
                LazyVStack(alignment: .leading, spacing: 16) {
                    rangeCard

                    if isLoading, report == nil {
                        loadingState
                    }

                    if let errorDetail {
                        errorCard(errorDetail)
                    }

                    if let report, report.hasData {
                        StatsKPIGrid(report: report, periodDays: periodDays(in: report.range))
                    }

                    StatsAgentUsageCard(
                        agents: agents,
                        store: agentUsageStore,
                        isLoadingCatalog: isLoadingAgentUsage,
                        catalogError: agentCatalogError,
                        canReadAgents: model.can("agent.read"),
                        onRefresh: { Task { await loadAgentUsage(force: true) } }
                    )

                    if let report, report.hasData {
                        StatsTrendCard(report: report, metric: $metric)
                        StatsWorkspaceRankCard(rows: report.byWorkspace)
                        StatsModelRankCard(rows: report.byModel)
                        StatsToolRankCard(rows: report.byTool)
                    } else if !isLoading, errorDetail == nil {
                        emptyState
                    }
                }
                .padding(.horizontal, 16)
                .padding(.vertical, 14)
            }
            .background(QuartetTheme.canvas)
            .mainTabBarBottomInset(mainTabBarInset)
            .quartetNavigationTitle("使用统计")
            .refreshable {
                await loadStats()
                await loadAgentUsage(force: true)
            }
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button {
                        refreshRevision &+= 1
                        Task { await loadAgentUsage(force: true) }
                    } label: {
                        if isLoading || isLoadingAgentUsage {
                            ProgressView()
                        } else {
                            Image(systemName: "arrow.clockwise")
                        }
                    }
                    .disabled(isLoading || isLoadingAgentUsage)
                    .accessibilityLabel("刷新使用统计")
                    .accessibilityIdentifier("stats-refresh")
                }
                .sharedBackgroundVisibility(.hidden)
            }
        }
        .toolbarBackground(QuartetTheme.canvas, for: .navigationBar)
        .toolbarBackground(.visible, for: .navigationBar)
        .task(id: loadKey) {
            await loadStats()
        }
        .task(id: model.connectionRevision) {
            await loadAgentUsage(force: false)
        }
    }

    private var rangeCard: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack {
                Label("统计范围", systemImage: "calendar")
                    .font(.quartet(.control, weight: .semibold))
                    .foregroundStyle(QuartetTheme.primaryText)
                Spacer()
                if let report {
                    Text(rangeLabel(report.range))
                        .font(.quartet(.detail, design: .monospaced))
                        .foregroundStyle(QuartetTheme.secondaryText)
                }
            }

            ScrollView(.horizontal) {
                HStack(spacing: 8) {
                    ForEach(StatsRangePreset.allCases) { item in
                        Button { preset = item } label: {
                            Text(item.title.localized(in: locale))
                                .font(.quartet(.detail, weight: .semibold))
                                .foregroundStyle(preset == item ? QuartetTheme.onAccent : QuartetTheme.secondaryText)
                                .padding(.horizontal, 13)
                                .frame(height: 32)
                                .background(
                                    preset == item ? QuartetTheme.accent : QuartetTheme.elevated,
                                    in: Capsule()
                                )
                        }
                        .buttonStyle(.plain)
                        .accessibilityValue(preset == item ? "已选择".localizedForApp : "")
                        .accessibilityIdentifier("stats-range-\(item.rawValue)")
                    }
                }
            }
            .scrollIndicators(.hidden)

            if preset == .custom {
                VStack(spacing: 8) {
                    DatePicker("开始日期", selection: $customFrom, displayedComponents: .date)
                    Divider().overlay(QuartetTheme.divider)
                    DatePicker("结束日期", selection: $customTo, displayedComponents: .date)
                }
                .font(.quartet(.control))
                .onChange(of: customFrom) { _, value in
                    if value > customTo { customTo = value }
                }
                .onChange(of: customTo) { _, value in
                    if value < customFrom { customFrom = value }
                }
            }
        }
        .statsCard()
    }

    private var loadingState: some View {
        VStack(spacing: 12) {
            ProgressView()
                .controlSize(.large)
                .tint(QuartetTheme.accent)
            Text("正在加载使用统计…")
                .font(.quartet(.control))
                .foregroundStyle(QuartetTheme.secondaryText)
        }
        .frame(maxWidth: .infinity, minHeight: 180)
        .statsCard()
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("stats-loading")
    }

    private func errorCard(_ detail: String) -> some View {
        VStack(alignment: .leading, spacing: 12) {
            Label("使用统计加载失败", systemImage: "exclamationmark.triangle.fill")
                .font(.quartet(.headline, weight: .semibold))
                .foregroundStyle(QuartetTheme.failed)

            Text(detail)
                .font(.quartet(.detail, design: .monospaced))
                .foregroundStyle(QuartetTheme.primaryText)
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)

            HStack(spacing: 16) {
                Button("重试") { refreshRevision &+= 1 }
                Button("复制完整错误") { UIPasteboard.general.string = detail }
            }
            .font(.quartet(.control, weight: .semibold))
        }
        .statsCard(stroke: QuartetTheme.failed.opacity(0.35))
        .accessibilityIdentifier("stats-error")
    }

    private var emptyState: some View {
        ContentUnavailableView {
            Label("所选范围暂无数据", systemImage: "chart.xyaxis.line")
                .font(.quartet(.headline, weight: .semibold))
        } description: {
            Text("完成一次 Agent 运行后，这里会显示耗时、Token、工具调用和趋势。")
                .font(.quartet(.control))
        } actions: {
            Button("刷新") { refreshRevision &+= 1 }
                .font(.quartet(.control, weight: .semibold))
        }
        .frame(maxWidth: .infinity, minHeight: 300)
        .statsCard()
        .accessibilityIdentifier("stats-empty")
    }

    private var loadKey: String {
        let from = preset == .custom ? StatsFormat.dateKey(customFrom) : ""
        let to = preset == .custom ? StatsFormat.dateKey(customTo) : ""
        return "\(preset.rawValue)|\(from)|\(to)|\(refreshRevision)|\(model.connectionRevision)"
    }

    private func loadStats() async {
        requestSequence &+= 1
        let sequence = requestSequence
        isLoading = true
        errorDetail = nil

        let bounds = requestedBounds
        do {
            let loaded = try await model.fetchUsageStats(
                from: bounds.from,
                to: bounds.to,
                allTime: preset == .allTime,
                compareWithPrevious: preset != .allTime
            )
            guard !Task.isCancelled, sequence == requestSequence else { return }
            report = loaded
        } catch is CancellationError {
            return
        } catch {
            guard !Task.isCancelled, sequence == requestSequence else { return }
            if let apiError = error as? APIError {
                errorDetail = apiError.detail
            } else {
                errorDetail = String(describing: error)
            }
        }

        if sequence == requestSequence { isLoading = false }
    }

    private func loadAgentUsage(force: Bool) async {
        guard model.can("agent.read") else {
            agents = []
            agentCatalogError = nil
            isLoadingAgentUsage = false
            return
        }

        agentRequestSequence &+= 1
        let sequence = agentRequestSequence
        if agentNamespace != model.serverAddress {
            agentNamespace = model.serverAddress
            agents = []
        }
        if agents.isEmpty { agents = model.agentCatalogSnapshot }
        isLoadingAgentUsage = true
        agentCatalogError = nil
        do {
            let loadedAgents = try await model.agentCatalog()
            guard !Task.isCancelled, sequence == agentRequestSequence else { return }
            let displayAgents = usageAgents(from: loadedAgents)
            agents = displayAgents
            var seen: Set<String> = []
            let targets = displayAgents.compactMap { agent -> AgentUsageProbeTarget? in
                guard !agent.type.isEmpty, seen.insert(agent.type).inserted else { return nil }
                return AgentUsageProbeTarget(command: agent.type, displayName: agent.displayName)
            }
            await agentUsageStore.load(targets: targets, model: model, force: force)
        } catch is CancellationError {
            return
        } catch {
            guard !Task.isCancelled, sequence == agentRequestSequence else { return }
            agentCatalogError = agentSettingsErrorDetail(error)
        }
        if sequence == agentRequestSequence { isLoadingAgentUsage = false }
    }

    private func usageAgents(from loaded: [AgentSummary]) -> [AgentSummary] {
#if DEBUG
        guard model.isRunningUITests,
              !loaded.contains(where: { AgentUsageProvider.resolve(command: $0.type, displayName: $0.displayName) != nil }) else {
            return loaded
        }
        return loaded + [AgentSummary(
            agentId: "claude",
            type: "claude",
            modelId: "claude-sonnet-4-6",
            displayName: "Claude",
            availability: "available",
            available: true,
            refreshing: false,
            error: nil,
            models: nil,
            modes: nil,
            thoughtLevels: nil
        )]
#else
        return loaded
#endif
    }

    private var requestedBounds: (from: String?, to: String?) {
        if preset == .allTime { return (nil, nil) }
        if preset == .custom {
            return (StatsFormat.dateKey(customFrom), StatsFormat.dateKey(customTo))
        }
        let today = Calendar.current.startOfDay(for: Date())
        let from = Calendar.current.date(byAdding: .day, value: -(preset.dayCount - 1), to: today) ?? today
        return (StatsFormat.dateKey(from), StatsFormat.dateKey(today))
    }

    private func rangeLabel(_ range: UsageStatsRange) -> String {
        guard !range.from.isEmpty, !range.to.isEmpty else { return "全部".localized(in: locale) }
        return "\(String(range.from.dropFirst(5))) – \(String(range.to.dropFirst(5)))"
    }

    private func periodDays(in range: UsageStatsRange) -> Int {
        guard let from = StatsFormat.date(range.from), let to = StatsFormat.date(range.to) else { return 0 }
        return (Calendar.current.dateComponents([.day], from: from, to: to).day ?? -1) + 1
    }
}

private enum StatsRangePreset: String, CaseIterable, Identifiable {
    case sevenDays = "7d"
    case thirtyDays = "30d"
    case ninetyDays = "90d"
    case allTime = "all"
    case custom

    var id: String { rawValue }

    var title: String {
        switch self {
        case .sevenDays: "7 天"
        case .thirtyDays: "30 天"
        case .ninetyDays: "90 天"
        case .allTime: "全部"
        case .custom: "自定义"
        }
    }

    var dayCount: Int {
        switch self {
        case .sevenDays: 7
        case .thirtyDays, .custom: 30
        case .ninetyDays: 90
        case .allTime: 0
        }
    }
}

private enum StatsTrendMetric: String, CaseIterable, Identifiable {
    case duration
    case turns
    case tokens
    case cache

    var id: String { rawValue }

    var title: String {
        switch self {
        case .duration: "耗时"
        case .turns: "Turn"
        case .tokens: "Token"
        case .cache: "缓存"
        }
    }
}

private struct StatsAgentUsageCard: View {
    @Environment(\.locale) private var locale
    @ObservedObject var store: AgentUsageSummaryStore

    let agents: [AgentSummary]
    let isLoadingCatalog: Bool
    let catalogError: String?
    let canReadAgents: Bool
    let onRefresh: () -> Void

    @State private var presentedError: PresentedError?

    private var visibleAgents: [AgentSummary] {
        var seen: Set<String> = []
        return agents.filter { agent in
            !agent.type.isEmpty && seen.insert(agent.agentId).inserted
        }
    }

    private var isRefreshing: Bool {
        isLoadingCatalog || visibleAgents.contains { store.entries[$0.type]?.loading == true }
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 0) {
            HStack(spacing: 10) {
                Label("Agent 版本与套餐", systemImage: "gauge.with.dots.needle.33percent")
                    .font(.quartet(.headline, weight: .semibold))
                    .foregroundStyle(QuartetTheme.primaryText)
                Spacer(minLength: 8)
                if isRefreshing {
                    ProgressView()
                        .controlSize(.small)
                        .tint(QuartetTheme.accent)
                        .accessibilityLabel("正在获取 Agent 用量")
                }
                Button(action: onRefresh) {
                    Image(systemName: "arrow.clockwise")
                        .font(.quartet(.control, weight: .semibold))
                        .foregroundStyle(QuartetTheme.accent)
                        .frame(width: 44, height: 44)
                        .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
                .disabled(isRefreshing || !canReadAgents)
                .opacity(canReadAgents ? 1 : 0.45)
                .accessibilityLabel("刷新 Agent 用量")
                .accessibilityIdentifier("stats-agent-usage-refresh")
            }
            .padding(.leading, 16)
            .padding(.trailing, 2)
            .padding(.vertical, 6)

            Text("查看本机 Agent 的版本、套餐和当前额度。用量来自各服务商的实时数据。")
                .font(.quartet(.detail))
                .foregroundStyle(QuartetTheme.secondaryText)
                .padding(.horizontal, 16)
                .padding(.bottom, 13)

            Divider().overlay(QuartetTheme.divider)

            if !canReadAgents {
                statusRow(icon: "lock.fill", text: "当前账号缺少 agent.read 权限。")
            } else if visibleAgents.isEmpty, isLoadingCatalog {
                statusRow(icon: "arrow.triangle.2.circlepath", text: "正在读取 Agent 信息…")
            } else if visibleAgents.isEmpty, let catalogError {
                errorRow(detail: catalogError, title: "Agent 列表加载失败")
            } else if visibleAgents.isEmpty {
                statusRow(icon: "shippingbox", text: "未检测到已安装的 Agent。")
            } else {
                ForEach(Array(visibleAgents.enumerated()), id: \.element.agentId) { index, agent in
                    if index > 0 {
                        Divider()
                            .overlay(QuartetTheme.divider)
                            .padding(.leading, 58)
                    }
                    StatsAgentUsageRow(
                        agent: agent,
                        entry: store.entries[agent.type],
                        locale: locale,
                        onShowError: { presentedError = $0 }
                    )
                }

                if let catalogError {
                    Divider().overlay(QuartetTheme.divider)
                    errorRow(detail: catalogError, title: "Agent 列表加载失败")
                }
            }
        }
        .statsCard(contentPadding: 0)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("stats-agent-usage")
        .sheet(item: $presentedError) { error in
            ErrorDetailView(error: error)
        }
    }

    private func statusRow(icon: String, text: String) -> some View {
        HStack(spacing: 10) {
            Image(systemName: icon)
                .foregroundStyle(QuartetTheme.secondaryText)
            Text(text.localized(in: locale))
                .font(.quartet(.control))
                .foregroundStyle(QuartetTheme.secondaryText)
            Spacer(minLength: 0)
        }
        .padding(16)
    }

    private func errorRow(detail: String, title: String) -> some View {
        Button {
            presentedError = PresentedError(title: title, detail: detail)
        } label: {
            HStack(spacing: 10) {
                Image(systemName: "exclamationmark.triangle.fill")
                    .foregroundStyle(QuartetTheme.failed)
                VStack(alignment: .leading, spacing: 3) {
                    Text(title.localized(in: locale))
                        .font(.quartet(.control, weight: .semibold))
                        .foregroundStyle(QuartetTheme.failed)
                    Text("查看完整错误".localized(in: locale))
                        .font(.quartet(.detail))
                        .foregroundStyle(QuartetTheme.secondaryText)
                }
                Spacer(minLength: 8)
                Image(systemName: "chevron.right")
                    .font(.quartet(.compact, weight: .semibold))
                    .foregroundStyle(QuartetTheme.secondaryText)
            }
            .padding(16)
            .contentShape(Rectangle())
        }
        .buttonStyle(.plain)
    }
}

private struct StatsAgentUsageRow: View {
    let agent: AgentSummary
    let entry: AgentUsageSummaryStore.Entry?
    let locale: Locale
    let onShowError: (PresentedError) -> Void

    private var provider: AgentUsageProvider? {
        AgentUsageProvider.resolve(command: agent.type, displayName: agent.displayName)
    }

    private var displayName: String {
        agent.displayName.isEmpty ? agent.agentId : agent.displayName
    }

    private var version: String? {
        if let provider, let usage = entry?.usage, let version = usage.version(for: provider) {
            return version
        }
        return AgentUsageFormat.trimmed(entry?.version)
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 12) {
            HStack(alignment: .top, spacing: 11) {
                Image(systemName: provider == nil ? "terminal" : "sparkles")
                    .font(.quartet(.control, weight: .semibold))
                    .foregroundStyle(provider == nil ? QuartetTheme.secondaryText : QuartetTheme.accent)
                    .frame(width: 31, height: 31)
                    .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 9, style: .continuous))

                VStack(alignment: .leading, spacing: 2) {
                    Text(displayName)
                        .font(.quartet(.control, weight: .semibold))
                        .foregroundStyle(QuartetTheme.primaryText)
                        .lineLimit(1)
                    if agent.type != displayName {
                        Text(agent.type)
                            .font(.quartet(.compact, design: .monospaced))
                            .foregroundStyle(QuartetTheme.secondaryText)
                            .lineLimit(1)
                    }
                }

                Spacer(minLength: 8)

                if let version {
                    ViewThatFits(in: .horizontal) {
                        HStack(spacing: 4) {
                            Text("版本".localized(in: locale))
                            Text(version)
                        }
                        Text(version)
                    }
                    .font(.quartet(.compact, weight: .semibold, design: .monospaced))
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .padding(.horizontal, 8)
                    .frame(minHeight: 27)
                    .background(QuartetTheme.elevated, in: Capsule())
                    .accessibilityElement(children: .combine)
                }
            }

            if !agent.available {
                HStack(spacing: 7) {
                    Image(systemName: "exclamationmark.circle.fill")
                    Text(agent.availabilityLabel)
                }
                .font(.quartet(.detail, weight: .medium))
                .foregroundStyle(QuartetTheme.warning)

                if let error = AgentUsageFormat.trimmed(agent.error) {
                    Text(error)
                        .font(.quartet(.detail, design: .monospaced))
                        .foregroundStyle(QuartetTheme.primaryText)
                        .textSelection(.enabled)
                }
            }

            if entry?.loading == true, entry?.usage == nil, version == nil {
                HStack(spacing: 8) {
                    ProgressView().controlSize(.small).tint(QuartetTheme.accent)
                    Text("正在获取 Agent 用量".localized(in: locale))
                        .font(.quartet(.detail))
                        .foregroundStyle(QuartetTheme.secondaryText)
                }
            } else if agent.available || entry?.usage != nil || version != nil {
                usageContent
            }

            if let failure = entry?.failure {
                Button {
                    onShowError(PresentedError(
                        title: failure.summary,
                        detail: [agent.type, failure.detail].joined(separator: "\n\n")
                    ))
                } label: {
                    HStack(spacing: 7) {
                        Image(systemName: "exclamationmark.triangle.fill")
                        Text((entry?.usage == nil && version == nil ? "Agent 用量加载失败" : "刷新失败").localized(in: locale))
                        Spacer(minLength: 6)
                        Text("查看完整错误".localized(in: locale))
                        Image(systemName: "chevron.right")
                    }
                    .font(.quartet(.detail, weight: .semibold))
                    .foregroundStyle(QuartetTheme.failed)
                    .contentShape(Rectangle())
                }
                .buttonStyle(.plain)
            }
        }
        .padding(16)
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("stats-agent-usage-\(agent.agentId)")
    }

    @ViewBuilder
    private var usageContent: some View {
        if let provider, let usage = entry?.usage {
            switch provider {
            case .codex:
                if let value = usage.codex { codexUsage(value) } else { noUsageDetails }
            case .claude:
                if let value = usage.claude { claudeUsage(value) } else { noUsageDetails }
            case .antigravity:
                if let value = usage.antigravity { antigravityUsage(value) } else { noUsageDetails }
            case .kimi:
                if let value = usage.kimi { kimiUsage(value) } else { noUsageDetails }
            case .qoder:
                if let value = usage.qoder { qoderUsage(value) } else { noUsageDetails }
            case .cursor:
                if let value = usage.cursor { cursorUsage(value) } else { noUsageDetails }
            case .codebuddy:
                // CodeBuddy 的额度看板要用户自己配 PAT，没配时后端返回空快照，不是错误。
                if let value = usage.codebuddy { codebuddyUsage(value) } else { quotaNotConfigured }
            }
        } else if provider == nil, version != nil {
            Text("此 Agent 仅提供版本信息。".localized(in: locale))
                .font(.quartet(.detail))
                .foregroundStyle(QuartetTheme.secondaryText)
        } else if entry?.failure == nil {
            Text("未检测到版本或套餐信息。".localized(in: locale))
                .font(.quartet(.detail))
                .foregroundStyle(QuartetTheme.secondaryText)
        }
    }

    @ViewBuilder
    private func codexUsage(_ value: CodexAgentUsage) -> some View {
        let plan = AgentUsageFormat.trimmed(value.planType).map { prettyPlan($0) }
        let email = AgentUsageFormat.trimmed(value.email)
        if plan != nil || email != nil {
            StatsAgentMetadataRow(items: [
                plan.map { ("套餐".localized(in: locale), $0) },
                email.map { ("账号".localized(in: locale), $0) }
            ].compactMap { $0 })
        }
        ForEach(Array([value.primaryWindow, value.secondaryWindow].compactMap { $0 }.enumerated()), id: \.offset) { _, window in
            StatsAgentQuotaMeter(label: window.durationLabel, window: window, locale: locale)
        }
        HStack(spacing: 7) {
            Image(systemName: "arrow.counterclockwise.circle")
                .foregroundStyle(value.resetCredits > 0 ? QuartetTheme.accentDeep : QuartetTheme.secondaryText)
            Text("重置额度".localized(in: locale))
            Spacer(minLength: 8)
            Text(String(value.resetCredits))
                .fontWeight(.bold)
                .monospacedDigit()
        }
        .font(.quartet(.detail))
        .foregroundStyle(QuartetTheme.secondaryText)
        if let expiries = value.resetCreditExpiries, !expiries.isEmpty {
            Text(String(
                format: "到期：%@".localized(in: locale),
                locale: locale,
                expiries.map { formattedDate($0, includesDate: true) }.joined(separator: " · ")
            ))
                .font(.quartet(.compact, design: .monospaced))
                .foregroundStyle(QuartetTheme.secondaryText.opacity(0.82))
        }
    }

    @ViewBuilder
    private func claudeUsage(_ value: ClaudeAgentUsage) -> some View {
        let plan = AgentUsageFormat.trimmed(value.planType).map { prettyPlan($0) }
        let tier = AgentUsageFormat.trimmed(value.rateLimitTier)
        if plan != nil || tier != nil {
            StatsAgentMetadataRow(items: [
                plan.map { ("套餐".localized(in: locale), $0) },
                tier.map { ("限额等级".localized(in: locale), $0) }
            ].compactMap { $0 })
        }
        if let window = value.fiveHour {
            StatsAgentQuotaMeter(label: "5h", window: window, locale: locale)
        }
        if let window = value.sevenDay {
            StatsAgentQuotaMeter(label: "7d", window: window, locale: locale)
        }
        if let window = value.sevenDayOpus {
            StatsAgentQuotaMeter(label: "Opus", window: window, locale: locale)
        }
        ForEach(Array((value.weeklyScoped ?? []).enumerated()), id: \.offset) { _, scoped in
            StatsAgentQuotaMeter(label: scoped.label, window: scoped.window, locale: locale)
        }
        if let extra = value.extraUsage, extra.enabled {
            StatsAgentExtraUsage(extra: extra, locale: locale)
        }
        if plan == nil, tier == nil, value.fiveHour == nil, value.sevenDay == nil,
           value.sevenDayOpus == nil, (value.weeklyScoped ?? []).isEmpty,
           value.extraUsage?.enabled != true {
            noUsageDetails
        }
    }

    @ViewBuilder
    private func antigravityUsage(_ value: AntigravityAgentUsage) -> some View {
        if value.claude5h != nil || value.claudeWeekly != nil {
            StatsAgentQuotaGroup(
                title: "Claude / GPT",
                windows: [("5h", value.claude5h), ("7d", value.claudeWeekly)],
                locale: locale
            )
        }
        if value.gemini5h != nil || value.geminiWeekly != nil {
            StatsAgentQuotaGroup(
                title: "Gemini",
                windows: [("5h", value.gemini5h), ("7d", value.geminiWeekly)],
                locale: locale
            )
        }
        if value.claude5h == nil, value.claudeWeekly == nil, value.gemini5h == nil, value.geminiWeekly == nil {
            noUsageDetails
        }
    }

    @ViewBuilder
    private func kimiUsage(_ value: KimiAgentUsage) -> some View {
        if let parallel = value.parallelLimit, parallel > 0 {
            StatsAgentMetadataRow(items: [("并发上限".localized(in: locale), String(parallel))])
        }
        if let window = value.fiveHour {
            StatsAgentQuotaMeter(label: window.durationLabel, window: window, locale: locale)
        }
        if let window = value.weekly {
            StatsAgentQuotaMeter(label: window.durationLabel, window: window, locale: locale)
        }
        if let window = value.total {
            StatsAgentQuotaMeter(label: "累计额度".localized(in: locale), window: window, locale: locale, showsReset: false)
        }
        if value.fiveHour == nil, value.weekly == nil, value.total == nil { noUsageDetails }
    }

    @ViewBuilder
    private func qoderUsage(_ value: QoderAgentUsage) -> some View {
        if let plan = AgentUsageFormat.trimmed(value.planType) {
            StatsAgentMetadataRow(items: [("套餐".localized(in: locale), prettyPlan(plan))])
        }
        StatsAgentQuotaMeter(
            label: value.unit?.lowercased() == "credits" ? "Credits" : "额度".localized(in: locale),
            usedPercent: value.usedPercent,
            value: "\(AgentUsageFormat.credits(value.used)) / \(AgentUsageFormat.credits(value.total))",
            detail: value.expiresAt.map {
                String(
                    format: "到期：%@".localized(in: locale),
                    locale: locale,
                    formattedDate($0, includesDate: true)
                )
            },
            locale: locale
        )
        HStack {
            Text("剩余".localized(in: locale))
            Spacer(minLength: 8)
            Text(AgentUsageFormat.credits(value.remaining))
                .fontWeight(.semibold)
                .monospacedDigit()
        }
        .font(.quartet(.detail))
        .foregroundStyle(value.quotaExceeded ? QuartetTheme.failed : QuartetTheme.secondaryText)
        if value.quotaExceeded {
            Label("额度已用尽", systemImage: "exclamationmark.octagon.fill")
                .font(.quartet(.detail, weight: .semibold))
                .foregroundStyle(QuartetTheme.failed)
        }
    }

    @ViewBuilder
    private func cursorUsage(_ value: CursorAgentUsage) -> some View {
        if let plan = AgentUsageFormat.plan(value.membershipType) {
            StatsAgentMetadataRow(items: [("套餐".localized(in: locale), plan)])
        }
        if let window = value.primaryWindow {
            StatsAgentQuotaMeter(label: "总量".localized(in: locale), window: window, locale: locale)
        }
        if let window = value.secondaryWindow {
            StatsAgentQuotaMeter(label: "Auto", window: window, locale: locale)
        }
        if let window = value.tertiaryWindow {
            StatsAgentQuotaMeter(label: "API", window: window, locale: locale)
        }
        if let window = value.grokBotWindow {
            StatsAgentQuotaMeter(label: "Grok", window: window, locale: locale)
        }
        if value.primaryWindow == nil, value.secondaryWindow == nil,
           value.tertiaryWindow == nil, value.grokBotWindow == nil {
            noUsageDetails
        }
    }

    @ViewBuilder
    private func codebuddyUsage(_ value: CodeBuddyAgentUsage) -> some View {
        let cost = value.cost.map(AgentUsageFormat.cny) ?? AgentUsageFormat.trimmed(value.costText)
        let quota = value.quota.map(AgentUsageFormat.cny) ?? AgentUsageFormat.trimmed(value.quotaText)
        if let username = AgentUsageFormat.trimmed(value.username) {
            StatsAgentMetadataRow(items: [("账号".localized(in: locale), username)])
        }
        if let cost {
            StatsAgentQuotaMeter(
                label: "本月已用".localized(in: locale),
                // 额度是 “-” 这类特殊状态时没有百分比，进度条按 0 画，只把金额说清楚。
                usedPercent: value.usedPercent ?? 0,
                value: quota.map { "\(cost) / \($0)" } ?? cost,
                detail: value.remaining.map {
                    String(
                        format: "剩余 %@".localized(in: locale),
                        locale: locale,
                        AgentUsageFormat.cny($0)
                    )
                },
                locale: locale
            )
        }
        if value.quota == nil {
            Text("额度处于特殊状态，仅显示已用金额".localized(in: locale))
                .font(.quartet(.detail))
                .foregroundStyle(QuartetTheme.secondaryText)
        }
        if cost == nil, quota == nil { noUsageDetails }
    }

    private var noUsageDetails: some View {
        Text("未返回可用的套餐数据。".localized(in: locale))
            .font(.quartet(.detail))
            .foregroundStyle(QuartetTheme.secondaryText)
    }

    /// 额度看板需要用户自己配置，没配置时和“读取失败”是两回事，说明白省得用户去查错误。
    private var quotaNotConfigured: some View {
        Text("未配置额度看板凭据，仅显示版本信息。".localized(in: locale))
            .font(.quartet(.detail))
            .foregroundStyle(QuartetTheme.secondaryText)
    }

    private func prettyPlan(_ value: String) -> String {
        AgentUsageFormat.plan(value) ?? value
    }

    private func formattedDate(_ unixSeconds: Int64, includesDate: Bool) -> String {
        let formatter = DateFormatter()
        formatter.locale = locale
        formatter.setLocalizedDateFormatFromTemplate(includesDate ? "MMM d HH:mm" : "HH:mm")
        return formatter.string(from: Date(timeIntervalSince1970: TimeInterval(unixSeconds)))
    }
}

private struct StatsAgentMetadataRow: View {
    let items: [(String, String)]

    var body: some View {
        WrappingHStack(spacing: 7, rowAlignment: .center) {
            ForEach(Array(items.enumerated()), id: \.offset) { _, item in
                HStack(spacing: 5) {
                    Text(item.0)
                        .foregroundStyle(QuartetTheme.secondaryText)
                    Text(item.1)
                        .fontWeight(.semibold)
                        .foregroundStyle(QuartetTheme.primaryText)
                }
                .font(.quartet(.compact))
                .padding(.horizontal, 8)
                .padding(.vertical, 5)
                .background(QuartetTheme.elevated, in: Capsule())
            }
        }
    }
}

private struct StatsAgentQuotaGroup: View {
    let title: String
    let windows: [(String, AgentUsageWindow?)]
    let locale: Locale

    var body: some View {
        VStack(alignment: .leading, spacing: 8) {
            Text(title)
                .font(.quartet(.detail, weight: .semibold))
                .foregroundStyle(QuartetTheme.primaryText)
            ForEach(Array(windows.compactMap { label, window in window.map { (label, $0) } }.enumerated()), id: \.offset) { _, item in
                StatsAgentQuotaMeter(label: item.0, window: item.1, locale: locale)
            }
        }
        .padding(10)
        .background(QuartetTheme.elevated.opacity(0.72), in: RoundedRectangle(cornerRadius: 12, style: .continuous))
    }
}

private struct StatsAgentExtraUsage: View {
    let extra: ClaudeExtraAgentUsage
    let locale: Locale

    private var currency: String {
        AgentUsageFormat.trimmed(extra.currency) ?? "USD"
    }

    private var value: String {
        let used = extra.usedCredits.map(AgentUsageFormat.credits) ?? "—"
        guard let limit = extra.monthlyLimit else { return "\(used) \(currency)" }
        return "\(used) / \(AgentUsageFormat.credits(limit)) \(currency)"
    }

    var body: some View {
        HStack(spacing: 8) {
            Label("额外用量".localized(in: locale), systemImage: "creditcard")
                .font(.quartet(.detail, weight: .semibold))
                .foregroundStyle(QuartetTheme.secondaryText)
            Spacer(minLength: 8)
            Text(value)
                .font(.quartet(.detail, weight: .bold, design: .monospaced))
                .foregroundStyle(QuartetTheme.primaryText)
                .monospacedDigit()
        }
        .padding(10)
        .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 11, style: .continuous))
        .accessibilityElement(children: .combine)
    }
}

private struct StatsAgentQuotaMeter: View {
    let label: String
    let usedPercent: Double
    let value: String
    let detail: String?
    let locale: Locale

    init(label: String, window: AgentUsageWindow, locale: Locale, showsReset: Bool = true) {
        self.label = label.isEmpty ? "额度".localized(in: locale) : label
        usedPercent = window.usedPercent
        value = window.percentLabel
        self.locale = locale
        if showsReset, window.resetAt > 0 || window.resetAfterSeconds > 0 {
            let seconds = window.resetAt > 0
                ? window.resetAt
                : Int64(Date().timeIntervalSince1970) + window.resetAfterSeconds
            let formatter = DateFormatter()
            formatter.locale = locale
            formatter.setLocalizedDateFormatFromTemplate(window.limitWindowSeconds >= 86_400 ? "MMM d HH:mm" : "HH:mm")
            detail = String(
                format: "重置于 %@".localized(in: locale),
                locale: locale,
                formatter.string(from: Date(timeIntervalSince1970: TimeInterval(seconds)))
            )
        } else {
            detail = nil
        }
    }

    init(label: String, usedPercent: Double, value: String, detail: String?, locale: Locale) {
        self.label = label
        self.usedPercent = usedPercent
        self.value = value
        self.detail = detail
        self.locale = locale
    }

    private var color: Color {
        if usedPercent >= 80 { return QuartetTheme.failed }
        if usedPercent >= 50 { return QuartetTheme.warning }
        return QuartetTheme.success
    }

    var body: some View {
        VStack(alignment: .leading, spacing: 5) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Text(label)
                    .font(.quartet(.detail, weight: .semibold, design: .monospaced))
                    .foregroundStyle(QuartetTheme.primaryText)
                Spacer(minLength: 8)
                Text(value)
                    .font(.quartet(.detail, weight: .bold, design: .monospaced))
                    .foregroundStyle(color)
                    .monospacedDigit()
            }
            GeometryReader { proxy in
                ZStack(alignment: .leading) {
                    Capsule().fill(QuartetTheme.divider)
                    Capsule()
                        .fill(color)
                        .frame(width: proxy.size.width * min(max(usedPercent, 0), 100) / 100)
                }
            }
            .frame(height: 6)
            .accessibilityHidden(true)
            if let detail {
                Text(detail)
                    .font(.quartet(.compact, design: .monospaced))
                    .foregroundStyle(QuartetTheme.secondaryText)
            }
        }
        .accessibilityElement(children: .combine)
        .accessibilityLabel("\(label)，\("已用".localized(in: locale)) \(value)")
    }
}

private struct StatsKPIGrid: View {
    @Environment(\.dynamicTypeSize) private var dynamicTypeSize
    let report: UsageStatsReport
    let periodDays: Int

    var body: some View {
        let cards = self.cards
        VStack(spacing: 0) {
            if dynamicTypeSize.isAccessibilitySize {
                ForEach(Array(cards.enumerated()), id: \.element.id) { index, card in
                    StatsKPIAccessibilityRow(card: card, periodDays: periodDays)
                    if index < cards.count - 1 {
                        Divider().overlay(QuartetTheme.divider)
                    }
                }
            } else {
                StatsKPIRow(cards: Array(cards.prefix(3)), periodDays: periodDays)
                Divider().overlay(QuartetTheme.divider)
                StatsKPIRow(cards: Array(cards.suffix(3)), periodDays: periodDays)
            }
        }
        .statsCard(contentPadding: 4)
        .accessibilityIdentifier("stats-kpis")
    }

    private var cards: [StatsKPICard] {
        var totalMs: Int64 = 0
        var turns = 0
        var tokens = 0
        var reported = 0
        var input = 0
        var output = 0
        var cachedRead = 0
        var cachedWrite = 0
        var tools = 0
        for row in report.byWorkspace {
            totalMs += row.totalMs
            turns += row.turnCount
            tokens += row.tokens.total
            reported += row.tokens.reported
            input += row.tokens.input
            output += row.tokens.output
            cachedRead += row.tokens.cachedRead
            cachedWrite += row.tokens.cachedWrite
            tools += row.toolCallCount
        }
        let cacheHitRate = StatsFormat.cacheHitRate(UsageStatsTokenTotals(
            total: tokens,
            reported: reported,
            input: input,
            output: output,
            cachedRead: cachedRead,
            cachedWrite: cachedWrite
        ))
        return [
            StatsKPICard(id: "duration", title: "总耗时", value: StatsFormat.duration(totalMs), current: Double(totalMs), previous: report.previous.map { Double($0.totalMs) }, icon: "clock", color: QuartetTheme.accent),
            StatsKPICard(id: "turns", title: "Turn", value: StatsFormat.count(turns), current: Double(turns), previous: report.previous.map { Double($0.turnCount) }, icon: "bubble.left.and.bubble.right", color: QuartetTheme.chartGreen),
            StatsKPICard(id: "tokens", title: "Token", value: StatsFormat.count(tokens), current: Double(tokens), previous: report.previous.map { Double($0.tokensTotal) }, icon: "text.word.spacing", color: QuartetTheme.running),
            StatsKPICard(id: "tools", title: "工具调用", value: StatsFormat.count(tools), current: Double(tools), previous: report.previous.map { Double($0.toolCallCount) }, icon: "wrench.and.screwdriver", color: QuartetTheme.chartForest),
            StatsKPICard(id: "cache", title: "缓存命中率", value: StatsFormat.percentage(cacheHitRate), current: cacheHitRate, previous: report.previous?.cacheHitRate, icon: "archivebox", color: QuartetTheme.chartMutedGreen),
            StatsKPICard(id: "workspaces", title: "统计工作区", value: StatsFormat.count(report.byWorkspace.count), current: Double(report.byWorkspace.count), previous: report.previous.map { Double($0.workspaceCount) }, icon: "square.grid.2x2", color: QuartetTheme.chartGraphite)
        ]
    }
}

private struct StatsKPIRow: View {
    let cards: [StatsKPICard]
    let periodDays: Int

    var body: some View {
        HStack(spacing: 0) {
            ForEach(Array(cards.enumerated()), id: \.element.id) { index, card in
                StatsKPICell(card: card, periodDays: periodDays)
                if index < cards.count - 1 {
                    Divider()
                        .overlay(QuartetTheme.divider)
                        .padding(.vertical, 10)
                }
            }
        }
    }
}

private struct StatsKPICell: View {
    @Environment(\.locale) private var locale
    let card: StatsKPICard
    let periodDays: Int

    var body: some View {
        VStack(alignment: .leading, spacing: 4) {
            HStack(spacing: 5) {
                Image(systemName: card.icon)
                    .font(.quartet(.compact, weight: .semibold))
                    .foregroundStyle(card.color)
                    .accessibilityHidden(true)
                Text(card.title.localized(in: locale))
                    .font(.quartet(.compact, weight: .medium))
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .lineLimit(1)
                    .minimumScaleFactor(0.8)
            }

            Text(card.value)
                .font(.quartet(.headline, weight: .semibold))
                .monospacedDigit()
                .foregroundStyle(QuartetTheme.primaryText)
                .minimumScaleFactor(0.72)
                .lineLimit(1)

            StatsDeltaLabel(current: card.current, previous: card.previous, periodDays: periodDays)
                .lineLimit(1)
                .minimumScaleFactor(0.75)
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 9)
        .frame(maxWidth: .infinity, minHeight: 76, alignment: .leading)
    }
}

private struct StatsKPIAccessibilityRow: View {
    @Environment(\.locale) private var locale
    let card: StatsKPICard
    let periodDays: Int

    var body: some View {
        HStack(alignment: .firstTextBaseline, spacing: 10) {
            Image(systemName: card.icon)
                .foregroundStyle(card.color)
                .accessibilityHidden(true)
            VStack(alignment: .leading, spacing: 3) {
                Text(card.title.localized(in: locale))
                    .font(.quartet(.detail, weight: .medium))
                    .foregroundStyle(QuartetTheme.secondaryText)
                StatsDeltaLabel(current: card.current, previous: card.previous, periodDays: periodDays)
            }
            Spacer(minLength: 12)
            Text(card.value)
                .font(.quartet(.large, weight: .semibold))
                .monospacedDigit()
                .foregroundStyle(QuartetTheme.primaryText)
                .multilineTextAlignment(.trailing)
        }
        .padding(.horizontal, 12)
        .padding(.vertical, 10)
    }
}

private struct StatsKPICard: Identifiable {
    let id: String
    let title: String
    let value: String
    let current: Double?
    let previous: Double?
    let icon: String
    let color: Color
}

private struct StatsDeltaLabel: View {
    @Environment(\.locale) private var locale
    let current: Double?
    let previous: Double?
    let periodDays: Int

    var body: some View {
        Group {
            if let current, let previous, previous > 0 {
                let delta = (current - previous) / previous * 100
                let roundedDelta = Int(abs(delta).rounded())
                Label(
                    "\(roundedDelta)%",
                    systemImage: delta >= 0 ? "arrow.up.right" : "arrow.down.right"
                )
                .foregroundStyle(delta >= 0 ? QuartetTheme.accent : QuartetTheme.secondaryText)
                .accessibilityLabel(String(
                    format: (delta >= 0 ? "较前 %lld 天增加 %lld%%" : "较前 %lld 天减少 %lld%%").localized(in: locale),
                    locale: locale,
                    Int64(periodDays),
                    Int64(roundedDelta)
                ))
            } else if let current, previous == 0, current > 0 {
                Text("—  前期无数据")
                    .foregroundStyle(QuartetTheme.secondaryText)
            } else if previous != nil {
                Text("无对比周期")
                    .foregroundStyle(QuartetTheme.secondaryText)
            } else {
                Text(" ")
                    .accessibilityHidden(true)
            }
        }
        .font(.quartet(.compact, weight: .medium))
        .frame(minHeight: 14)
    }
}

private struct StatsTrendCard: View {
    @Environment(\.locale) private var locale
    let report: UsageStatsReport
    @Binding var metric: StatsTrendMetric

    var body: some View {
        // 派生数据在这里算一次后按值下传：图内选中日变化只会重算内容视图，
        // 不会再走一遍补齐日期、解析日期串和拆分模型序列的计算。
        StatsTrendCardContent(
            data: StatsTrendData(report: report, metric: metric, locale: locale),
            metric: $metric
        )
    }
}

private struct StatsTrendCardContent: View {
    @Environment(\.locale) private var locale
    let data: StatsTrendData
    @Binding var metric: StatsTrendMetric
    @State private var selectedDate: Date?

    var body: some View {
        let selectedIndex = data.nearestIndex(to: selectedDate ?? Calendar.current.startOfDay(for: Date()))
        let selectedDay = selectedIndex.map { data.days[$0] }
        let selectedDayDate = selectedIndex.map { data.dates[$0] }
        let selectedKey = selectedDay?.date
        let entries = selectedKey.map { data.entries(forDateKey: $0, metric: metric) } ?? []

        VStack(alignment: .leading, spacing: 14) {
            VStack(alignment: .leading, spacing: 10) {
                Text(trendTitle.localized(in: locale))
                    .font(.quartet(.headline, weight: .semibold))
                    .foregroundStyle(QuartetTheme.primaryText)

                Picker("趋势指标", selection: $metric) {
                    ForEach(StatsTrendMetric.allCases) { item in
                        Text(item.title.localized(in: locale)).tag(item)
                    }
                }
                .pickerStyle(.segmented)
                .font(.quartet(.compact, weight: .semibold))
                .frame(maxWidth: .infinity)
                .accessibilityLabel("趋势指标")
                .accessibilityValue(metric.title.localized(in: locale))
                .accessibilityIdentifier("stats-trend-metric")
            }

            if metric == .cache {
                Label("按厂商上报的缓存读取占输入总量计算；本地估算 Turn 不参与。", systemImage: "externaldrive.badge.checkmark")
                    .font(.quartet(.compact))
                    .foregroundStyle(QuartetTheme.secondaryText)
            }

            if !data.hasData {
                Text((metric == .cache ? "所选范围内没有可计算缓存命中率的模型输入数据。" : "所选范围暂无数据").localized(in: locale))
                    .font(.quartet(.control))
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .frame(maxWidth: .infinity, minHeight: 180)
            } else {
                Chart(data.series) { line in
                    ForEach(line.points) { point in
                        LineMark(
                            x: .value("日期".localized(in: locale), point.date),
                            y: .value(metric.title.localized(in: locale), point.value),
                            series: .value("系列".localized(in: locale), line.id)
                        )
                        .foregroundStyle(line.color)
                        .lineStyle(StrokeStyle(lineWidth: line.isTotal ? 2.7 : 1.8, lineCap: .round, lineJoin: .round))
                        .interpolationMethod(data.interpolation)

                        let isSelected = selectedKey == point.dateKey
                        let alwaysMarked = line.isTotal || metric == .cache
                        let showsPoint = data.showsAllPointMarks
                            ? (alwaysMarked || (isSelected && point.value > 0))
                            : (isSelected && (alwaysMarked || point.value > 0))
                        if showsPoint {
                            PointMark(
                                x: .value("日期".localized(in: locale), point.date),
                                y: .value(metric.title.localized(in: locale), point.value)
                            )
                            .foregroundStyle(line.color)
                            .symbolSize(isSelected ? (line.isTotal ? 34 : 26) : (line.isTotal ? 22 : 14))
                        }
                    }

                    if let selectedDayDate {
                        RuleMark(x: .value("选中日期".localized(in: locale), selectedDayDate))
                            .foregroundStyle(QuartetTheme.secondaryText.opacity(0.55))
                            .lineStyle(StrokeStyle(lineWidth: 1, dash: [4, 4]))
                    }
                }
                .chartXScale(domain: data.domain)
                .chartXAxis {
                    AxisMarks(values: .automatic(desiredCount: 5)) {
                        AxisGridLine().foregroundStyle(QuartetTheme.divider.opacity(0.45))
                        AxisValueLabel(format: .dateTime.month(.twoDigits).day(.twoDigits))
                            .font(.quartet(.compact))
                            .foregroundStyle(QuartetTheme.secondaryText)
                    }
                }
                .chartYAxis {
                    AxisMarks(position: .leading, values: .automatic(desiredCount: 4)) { value in
                        AxisGridLine().foregroundStyle(QuartetTheme.divider.opacity(0.55))
                        AxisValueLabel {
                            if let raw = value.as(Double.self) {
                                Text(StatsFormat.trend(raw, metric: metric))
                                    .font(.quartet(.compact))
                                    .foregroundStyle(QuartetTheme.secondaryText)
                            }
                        }
                    }
                }
                .chartXSelection(value: chartSelection(current: selectedDayDate))
                .modifier(StatsTrendScaleModifier(metric: metric))
                .frame(height: 220)
                .accessibilityElement(children: .ignore)
                .accessibilityLabel(String(
                    format: "%@使用趋势图".localized(in: locale),
                    locale: locale,
                    metric.title.localized(in: locale)
                ))
                .accessibilityValue(accessibilityValue(for: selectedDay))
                .accessibilityHint("上下轻扫以逐日浏览")
                .accessibilityAdjustableAction { direction in
                    adjustAccessibilitySelection(direction, from: selectedIndex)
                }

                if let selectedDay {
                    if metric == .tokens {
                        StatsTokenDayDetail(
                            day: selectedDay,
                            modelEntries: entries.filter { !$0.isTotal }
                        )
                    } else {
                        StatsTrendDayTip(
                            date: selectedDay.date,
                            metric: metric,
                            entries: entries
                        )
                    }
                }

                ScrollView(.horizontal) {
                    HStack(spacing: 14) {
                        ForEach(data.series) { line in
                            Label {
                                Text(line.name).lineLimit(1)
                            } icon: {
                                Circle().fill(line.color).frame(width: 8, height: 8)
                            }
                            .font(.quartet(.detail))
                            .foregroundStyle(QuartetTheme.secondaryText)
                        }
                    }
                }
                .scrollIndicators(.hidden)
            }
        }
        .statsCard()
        .accessibilityIdentifier("stats-trend")
    }

    private func chartSelection(current: Date?) -> Binding<Date?> {
        Binding(
            get: { current },
            set: { value in
                // 手指在同一天内移动时不改状态，避免整卡片跟着每个触摸事件重绘。
                guard let value, let index = data.nearestIndex(to: value) else { return }
                let day = data.dates[index]
                guard day != selectedDate else { return }
                selectedDate = day
            }
        )
    }

    private var trendTitle: String {
        switch metric {
        case .tokens: "每日 Token"
        case .cache: "每日缓存命中率"
        case .duration, .turns: "使用趋势"
        }
    }

    private func accessibilityValue(for selectedDay: UsageStatsDailyRow?) -> String {
        guard let selectedDay else {
            return String(
                format: "共 %lld 天".localized(in: locale),
                locale: locale,
                Int64(data.days.count)
            )
        }
        let value = metric == .tokens
            ? StatsFormat.count(selectedDay.tokens.total)
            : StatsFormat.trend(StatsFormat.optionalMetricValue(selectedDay, metric: metric), metric: metric)
        return String(
            format: "%@，%@".localized(in: locale),
            locale: locale,
            selectedDay.date,
            value
        )
    }

    private func adjustAccessibilitySelection(
        _ direction: AccessibilityAdjustmentDirection,
        from currentIndex: Int?
    ) {
        guard !data.days.isEmpty else { return }
        let targetIndex: Int
        switch direction {
        case .increment:
            targetIndex = min((currentIndex ?? -1) + 1, data.days.count - 1)
        case .decrement:
            targetIndex = max((currentIndex ?? data.days.count) - 1, 0)
        @unknown default:
            return
        }
        selectedDate = data.dates[targetIndex]
    }
}

// 趋势图需要的全部派生数据。补齐范围内缺失的日期、解析日期串、按模型拆分
// 序列都集中在初始化里做一次，视图只做读取，避免在图表内容闭包里对每个数据
// 点重复做日历运算——90 天范围下这曾让页面卡住。
private struct StatsTrendData {
    // 与 dates 一一对应，按日期升序且已补齐范围内缺失的天。
    let days: [UsageStatsDailyRow]
    let dates: [Date]
    let series: [StatsTrendSeries]
    let domain: ClosedRange<Date>
    let hasData: Bool
    // 天数多时逐日圆点既拥挤又拖慢渲染，此时只画选中日的圆点。
    let showsAllPointMarks: Bool
    let interpolation: InterpolationMethod

    // 超过这个天数就按「密集范围」处理：去掉逐日圆点，插值退化为直线。
    private static let denseRangeLimit = 45
    private static let totalSeriesID = "__total__"

    init(report: UsageStatsReport, metric: StatsTrendMetric, locale: Locale) {
        let calendar = Calendar.current
        var days: [UsageStatsDailyRow] = []
        var dates: [Date] = []

        if let from = StatsFormat.date(report.range.from, calendar: calendar),
           let to = StatsFormat.date(report.range.to, calendar: calendar),
           from <= to {
            let byDate = Dictionary(report.daily.map { ($0.date, $0) }, uniquingKeysWith: { _, latest in latest })
            var current = from
            while current <= to {
                let key = StatsFormat.dateKey(current, calendar: calendar)
                days.append(byDate[key] ?? .empty(date: key))
                dates.append(current)
                guard let next = calendar.date(byAdding: .day, value: 1, to: current) else { break }
                current = next
            }
        } else {
            for row in report.daily.sorted(by: { $0.date < $1.date }) {
                guard let date = StatsFormat.date(row.date, calendar: calendar) else { continue }
                days.append(row)
                dates.append(date)
            }
        }

        let series = Self.makeSeries(days: days, dates: dates, metric: metric, locale: locale)
        self.days = days
        self.dates = dates
        self.series = series
        self.domain = Self.makeDomain(dates: dates, calendar: calendar)
        self.hasData = metric == .cache
            ? series.contains { !$0.points.isEmpty }
            : series.contains { line in line.points.contains { $0.value > 0 } }
        self.showsAllPointMarks = days.count <= Self.denseRangeLimit
        self.interpolation = days.count <= Self.denseRangeLimit ? .catmullRom : .linear
    }

    // dates 已按升序排列，二分定位最接近的一天。
    func nearestIndex(to date: Date) -> Int? {
        guard !dates.isEmpty else { return nil }
        var low = 0
        var high = dates.count - 1
        while low < high {
            let middle = low + (high - low) / 2
            if dates[middle] < date {
                low = middle + 1
            } else {
                high = middle
            }
        }
        guard low > 0 else { return low }
        let previousGap = abs(dates[low - 1].timeIntervalSince(date))
        let currentGap = abs(dates[low].timeIntervalSince(date))
        return previousGap <= currentGap ? low - 1 : low
    }

    func entries(forDateKey key: String, metric: StatsTrendMetric) -> [StatsTrendTipEntry] {
        series.compactMap { line in
            guard let value = line.valueByDateKey[key] else { return nil }
            guard line.isTotal || metric == .cache || value > 0 else { return nil }
            return StatsTrendTipEntry(
                id: line.id,
                name: line.name,
                value: value,
                color: line.color,
                isTotal: line.isTotal
            )
        }
    }

    private static func makeDomain(dates: [Date], calendar: Calendar) -> ClosedRange<Date> {
        func nextDay(after date: Date) -> Date {
            calendar.date(byAdding: .day, value: 1, to: date) ?? date.addingTimeInterval(86_400)
        }
        guard let first = dates.first, let last = dates.last else {
            let today = calendar.startOfDay(for: Date())
            return today ... nextDay(after: today)
        }
        guard first < last else { return first ... nextDay(after: first) }
        return first ... last
    }

    private static func makeSeries(
        days: [UsageStatsDailyRow],
        dates: [Date],
        metric: StatsTrendMetric,
        locale: Locale
    ) -> [StatsTrendSeries] {
        guard !days.isEmpty else { return [] }

        var totalPoints: [StatsTrendPoint] = []
        totalPoints.reserveCapacity(days.count)
        var totalValues = [String: Double](minimumCapacity: days.count)
        for (index, row) in days.enumerated() {
            guard let value = StatsFormat.optionalMetricValue(row, metric: metric) else { continue }
            totalPoints.append(StatsTrendPoint(dateKey: row.date, date: dates[index], value: value))
            totalValues[row.date] = value
        }
        var result = [StatsTrendSeries(
            id: totalSeriesID,
            name: "总计".localized(in: locale),
            color: QuartetTheme.accent,
            isTotal: true,
            points: totalPoints,
            valueByDateKey: totalValues
        )]

        // 每天已归属到具体模型的量，用来补出未归属的差额。
        var attributed = [Double](repeating: 0, count: days.count)
        if metric != .cache {
            for (index, row) in days.enumerated() {
                attributed[index] = row.models?.values.reduce(0) { partial, totals in
                    partial + StatsFormat.metricValue(totals, metric: metric)
                } ?? 0
            }
        }

        var modelIDSet = Set<String>()
        // 模型显示名按日期升序取首次出现的那个，一次遍历收齐，不用按模型反复扫全部天。
        var modelNames: [String: String] = [:]
        for row in days {
            if let names = row.modelNames {
                for (modelID, name) in names where modelNames[modelID] == nil {
                    modelNames[modelID] = name
                }
            }
            guard let models = row.models else { continue }
            for (modelID, totals) in models
            where StatsFormat.optionalMetricValue(totals, metric: metric) != nil {
                modelIDSet.insert(modelID)
            }
        }
        if metric != .cache, days.indices.contains(where: { index in
            StatsFormat.metricValue(days[index], metric: metric) > attributed[index]
        }) {
            modelIDSet.insert(StatsFormat.unknownModelID)
        }

        let palette: [Color] = [
            QuartetTheme.chartBlue,
            QuartetTheme.chartOrange,
            QuartetTheme.chartViolet,
            QuartetTheme.chartRose,
            QuartetTheme.chartCyan,
            QuartetTheme.chartAmber,
            QuartetTheme.chartGraphite
        ]
        for (index, modelID) in modelIDSet.sorted().enumerated() {
            let name = modelNames[modelID] ?? StatsFormat.modelName(modelID, locale: locale)
            var points: [StatsTrendPoint] = []
            points.reserveCapacity(days.count)
            var values = [String: Double](minimumCapacity: days.count)
            for (dayIndex, row) in days.enumerated() {
                let value: Double
                if metric == .cache {
                    guard let totals = row.models?[modelID],
                          let rate = StatsFormat.optionalMetricValue(totals, metric: metric) else { continue }
                    value = rate
                } else {
                    var attributedValue = row.models?[modelID].map { StatsFormat.metricValue($0, metric: metric) } ?? 0
                    if modelID == StatsFormat.unknownModelID {
                        attributedValue += max(0, StatsFormat.metricValue(row, metric: metric) - attributed[dayIndex])
                    }
                    value = attributedValue
                }
                points.append(StatsTrendPoint(dateKey: row.date, date: dates[dayIndex], value: value))
                values[row.date] = value
            }
            result.append(StatsTrendSeries(
                id: modelID,
                name: StatsFormat.modelName(name, locale: locale),
                color: palette[index % palette.count],
                isTotal: false,
                points: points,
                valueByDateKey: values
            ))
        }
        return result
    }
}

private struct StatsTrendScaleModifier: ViewModifier {
    let metric: StatsTrendMetric

    @ViewBuilder
    func body(content: Content) -> some View {
        if metric == .cache {
            content.chartYScale(domain: 0 ... 1)
        } else {
            content
        }
    }
}

private struct StatsTrendSeries: Identifiable {
    let id: String
    let name: String
    let color: Color
    let isTotal: Bool
    let points: [StatsTrendPoint]
    // 按日期直接取值，避免为选中日在点数组里线性查找。
    let valueByDateKey: [String: Double]
}

private struct StatsTrendPoint: Identifiable {
    let dateKey: String
    let date: Date
    let value: Double

    var id: String { dateKey }
}

private struct StatsTrendTipEntry: Identifiable {
    let id: String
    let name: String
    let value: Double
    let color: Color
    let isTotal: Bool
}

private struct StatsTrendDayTip: View {
    @Environment(\.locale) private var locale
    let date: String
    let metric: StatsTrendMetric
    let entries: [StatsTrendTipEntry]

    var body: some View {
        VStack(alignment: .leading, spacing: 9) {
            Text(date)
                .font(.quartet(.detail, weight: .semibold))
                .foregroundStyle(QuartetTheme.primaryText)

            if entries.isEmpty {
                Text((metric == .cache ? "该日没有可计算的缓存命中率。" : "所选范围暂无数据").localized(in: locale))
                    .font(.quartet(.compact))
                    .foregroundStyle(QuartetTheme.secondaryText)
            } else {
                ForEach(entries) { entry in
                    HStack(spacing: 8) {
                        Circle()
                            .fill(entry.color)
                            .frame(width: entry.isTotal ? 9 : 7, height: entry.isTotal ? 9 : 7)
                            .accessibilityHidden(true)

                        Text(entry.name)
                            .font(.quartet(.detail, weight: entry.isTotal ? .semibold : .regular))
                            .foregroundStyle(entry.isTotal ? QuartetTheme.primaryText : QuartetTheme.secondaryText)
                            .lineLimit(1)

                        Spacer(minLength: 12)

                        Text(StatsFormat.trend(entry.value, metric: metric))
                            .font(.quartet(.detail, weight: .semibold))
                            .monospacedDigit()
                            .foregroundStyle(QuartetTheme.primaryText)
                    }
                    .accessibilityElement(children: .combine)
                }
            }
        }
        .padding(10)
        .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 9))
        .accessibilityElement(children: .contain)
        .accessibilityIdentifier("stats-trend-day-tip")
    }
}

private struct StatsTokenSourceSummary: View {
    @Environment(\.locale) private var locale
    let rows: [UsageStatsDailyRow]
    var compact = false
    var title = "Token 统计方式（按 Turn）"

    var body: some View {
        let coverage = StatsTokenCoverage(rows: rows)
        VStack(alignment: .leading, spacing: 9) {
            HStack(alignment: .firstTextBaseline, spacing: 8) {
                Text(title.localized(in: locale))
                    .font(.quartet(.detail, weight: .semibold))
                    .foregroundStyle(QuartetTheme.primaryText)
                Spacer(minLength: 8)
                if coverage.totalTurns > 0 {
                    Text(String(
                        format: "%lld Turn".localized(in: locale),
                        locale: locale,
                        Int64(coverage.totalTurns)
                    ))
                        .font(.quartet(.compact))
                        .foregroundStyle(QuartetTheme.secondaryText)
                        .multilineTextAlignment(.trailing)
                }
            }

            if coverage.totalTurns <= 0 {
                Text("暂无可计算来源的 Turn。")
                    .font(.quartet(.compact))
                    .foregroundStyle(QuartetTheme.secondaryText)
            } else {
                GeometryReader { proxy in
                    HStack(spacing: 0) {
                        Rectangle()
                            .fill(QuartetTheme.accent)
                            .frame(width: proxy.size.width * coverage.reportedRatio)
                        Rectangle()
                            .fill(QuartetTheme.secondaryText.opacity(0.48))
                    }
                    .clipShape(Capsule())
                }
                .frame(height: 7)
                .accessibilityElement(children: .ignore)
                .accessibilityLabel(String(
                    format: "按 Turn：厂商上报占 %lld%%，本地估算占 %lld%%".localized(in: locale),
                    locale: locale,
                    Int64(coverage.reportedPercent),
                    Int64(coverage.estimatedPercent)
                ))

                VStack(spacing: 0) {
                    StatsTokenSourceRow(
                        title: "厂商上报",
                        tokenCount: coverage.reportedTokens,
                        runCount: coverage.reportedTurns,
                        percent: coverage.reportedPercent,
                        detail: compact ? nil : "由厂商 CLI 提供的 Token 用量，通常更准确。",
                        color: QuartetTheme.accent
                    )
                    Divider().overlay(QuartetTheme.divider)
                    StatsTokenSourceRow(
                        title: "本地估算",
                        tokenCount: coverage.estimatedTokens,
                        runCount: coverage.estimatedTurns,
                        percent: coverage.estimatedPercent,
                        detail: compact ? nil : "未收到厂商用量时，由 Quartet 根据可见内容在本地估算，仅供参考。",
                        color: QuartetTheme.secondaryText.opacity(0.65)
                    )
                }
                .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 10, style: .continuous))
                .overlay {
                    RoundedRectangle(cornerRadius: 10, style: .continuous)
                        .stroke(QuartetTheme.divider.opacity(0.75), lineWidth: 1)
                }

            }
        }
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("stats-token-coverage")
    }
}

private struct StatsTokenSourceRow: View {
    @Environment(\.locale) private var locale
    let title: String
    let tokenCount: Int
    let runCount: Int
    let percent: Int
    let detail: String?
    let color: Color

    var body: some View {
        HStack(alignment: .top, spacing: 9) {
            RoundedRectangle(cornerRadius: 2)
                .fill(color)
                .frame(width: 8, height: 8)
                .padding(.top, 4)
                .accessibilityHidden(true)

            VStack(alignment: .leading, spacing: 3) {
                Text(title.localized(in: locale))
                    .font(.quartet(.detail, weight: .semibold))
                    .foregroundStyle(QuartetTheme.primaryText)
                Text(String(
                    format: "%lld Turn · %lld%%".localized(in: locale),
                    locale: locale,
                    Int64(runCount),
                    Int64(percent)
                ))
                    .font(.quartet(.compact))
                    .foregroundStyle(QuartetTheme.secondaryText)
                if let detail {
                    Text(detail.localized(in: locale))
                        .font(.quartet(.compact))
                        .foregroundStyle(QuartetTheme.secondaryText.opacity(0.82))
                        .fixedSize(horizontal: false, vertical: true)
                }
            }

            Spacer(minLength: 8)

            Text(StatsFormat.count(tokenCount))
                .font(.quartet(.headline, weight: .semibold))
                .monospacedDigit()
                .foregroundStyle(QuartetTheme.primaryText)
        }
        .padding(.horizontal, 10)
        .padding(.vertical, 9)
    }
}

// Every recorded turn is classified as either vendor-reported or locally
// estimated, so the two counters always add up to the turn count.
private struct StatsTokenCoverage {
    let totalTurns: Int
    let reportedTurns: Int
    let estimatedTurns: Int
    let reportedPercent: Int
    let estimatedPercent: Int
    let reportedTokens: Int
    let estimatedTokens: Int

    var reportedRatio: Double {
        totalTurns > 0 ? min(1, max(0, Double(reportedTurns) / Double(totalTurns))) : 0
    }

    init(rows: [UsageStatsDailyRow]) {
        totalTurns = rows.reduce(0) { $0 + max(0, $1.turnCount) }
        reportedTurns = rows.reduce(0) { $0 + max(0, $1.tokens.reportedTurns) }
        estimatedTurns = rows.reduce(0) { $0 + max(0, $1.tokens.estimatedTurns) }
        reportedTokens = rows.reduce(0) { $0 + max(0, $1.tokens.reported) }
        estimatedTokens = rows.reduce(0) { $0 + max(0, $1.tokens.estimated) }
        reportedPercent = totalTurns > 0
            ? Int((Double(reportedTurns) / Double(totalTurns) * 100).rounded())
            : 0
        estimatedPercent = totalTurns > 0 ? max(0, 100 - reportedPercent) : 0
    }
}

private struct StatsTokenDayDetail: View {
    @Environment(\.locale) private var locale
    let day: UsageStatsDailyRow
    let modelEntries: [StatsTrendTipEntry]

    var body: some View {
        let cacheHitRate = StatsFormat.cacheHitRate(day.tokens)

        VStack(alignment: .leading, spacing: 10) {
            HStack(alignment: .firstTextBaseline) {
                VStack(alignment: .leading, spacing: 2) {
                    Text(day.date)
                        .font(.quartet(.detail))
                        .foregroundStyle(QuartetTheme.secondaryText)
                    Text("总 Token")
                        .font(.quartet(.compact))
                        .foregroundStyle(QuartetTheme.secondaryText)
                }
                Spacer(minLength: 12)
                VStack(alignment: .trailing, spacing: 3) {
                    Text(StatsFormat.count(day.tokens.total))
                        .contentTransition(.numericText())
                        .font(.quartet(.headline, weight: .semibold))
                        .monospacedDigit()
                        .foregroundStyle(QuartetTheme.primaryText)

                    HStack(spacing: 5) {
                        Text("缓存命中率")
                            .foregroundStyle(QuartetTheme.secondaryText)
                        Text(StatsFormat.percentage(cacheHitRate, locale: locale))
                            .contentTransition(.numericText())
                            .foregroundStyle(QuartetTheme.accent)
                    }
                    .font(.quartet(.compact, weight: .semibold))
                    .monospacedDigit()
                }
            }

            StatsTokenSourceSummary(rows: [day], compact: true, title: "当日 Token 统计方式（按 Turn）")

            if !modelEntries.isEmpty {
                Divider().overlay(QuartetTheme.divider)

                Text("模型".localized(in: locale))
                    .font(.quartet(.detail, weight: .semibold))
                    .foregroundStyle(QuartetTheme.primaryText)

                ForEach(modelEntries) { entry in
                    HStack(spacing: 8) {
                        Circle()
                            .fill(entry.color)
                            .frame(width: 7, height: 7)
                            .accessibilityHidden(true)

                        Text(entry.name)
                            .font(.quartet(.detail))
                            .foregroundStyle(QuartetTheme.secondaryText)
                            .lineLimit(1)

                        Spacer(minLength: 12)

                        Text(StatsFormat.count(Int(max(0, entry.value))))
                            .font(.quartet(.detail, weight: .semibold))
                            .monospacedDigit()
                            .foregroundStyle(QuartetTheme.primaryText)
                    }
                    .accessibilityElement(children: .combine)
                }
            }

            Divider().overlay(QuartetTheme.divider)

            Text("厂商上报的用量明细")
                .font(.quartet(.detail, weight: .semibold))
                .foregroundStyle(QuartetTheme.primaryText)

            Text("仅统计“厂商上报”的 Token；缓存与推理是其中的明细，不能与输入、输出重复相加。")
                .font(.quartet(.compact))
                .foregroundStyle(QuartetTheme.secondaryText)

            LazyVGrid(columns: columns, alignment: .leading, spacing: 9) {
                ForEach(details) { detail in
                    VStack(alignment: .leading, spacing: 2) {
                        Text(detail.title.localized(in: locale))
                            .font(.quartet(.compact))
                            .foregroundStyle(QuartetTheme.secondaryText)
                        Text(StatsFormat.count(detail.value))
                            .font(.quartet(.detail, weight: .semibold))
                            .monospacedDigit()
                            .foregroundStyle(QuartetTheme.primaryText)
                    }
                    .frame(maxWidth: .infinity, alignment: .leading)
                    .accessibilityElement(children: .combine)
                    .accessibilityLabel(String(
                        format: "%@，%@".localized(in: locale),
                        locale: locale,
                        detail.title.localized(in: locale),
                        StatsFormat.count(detail.value)
                    ))
                }
            }

            Text("按厂商上报的缓存读取占输入总量计算；本地估算 Turn 不参与。")
                .font(.quartet(.compact))
                .foregroundStyle(QuartetTheme.secondaryText)
        }
        .padding(12)
        .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
        .accessibilityElement(children: .contain)
        .accessibilityLabel(String(
            format: "%@ Token 明细，总 Token %@，缓存命中率 %@".localized(in: locale),
            locale: locale,
            day.date,
            StatsFormat.count(day.tokens.total),
            StatsFormat.percentage(cacheHitRate, locale: locale)
        ))
        .accessibilityIdentifier("stats-token-day-detail")
    }

    private var columns: [GridItem] {
        [GridItem(.flexible(), spacing: 12), GridItem(.flexible(), spacing: 12)]
    }

    private var details: [StatsTokenDetail] {
        [
            StatsTokenDetail(id: "input", title: "输入 Token", value: day.tokens.input),
            StatsTokenDetail(id: "output", title: "输出 Token", value: day.tokens.output),
            StatsTokenDetail(id: "cached-read", title: "缓存读取", value: day.tokens.cachedRead),
            StatsTokenDetail(id: "cached-write", title: "缓存写入", value: day.tokens.cachedWrite),
            StatsTokenDetail(id: "reasoning", title: "推理 Token", value: day.tokens.reasoning),
            StatsTokenDetail(id: "image-estimate", title: "图片估算", value: day.tokens.imageEstimate)
        ]
    }
}

private struct StatsTokenDetail: Identifiable {
    let id: String
    let title: String
    let value: Int
}

private struct StatsWorkspaceRankCard: View {
    @Environment(\.locale) private var locale
    let rows: [UsageStatsWorkspaceRow]

    var body: some View {
        StatsRankCard(
            title: "按工作区",
            emptyText: "所选范围暂无数据",
            items: rows.map { row in
                StatsRankItem(
                    id: row.workspaceId,
                    label: (row.workspaceName?.isEmpty == false ? row.workspaceName! : row.workspaceId)
                        + (row.deleted == true ? "（已删除）".localized(in: locale) : ""),
                    value: StatsFormat.duration(row.totalMs),
                    raw: Double(row.totalMs)
                )
            }
        )
    }
}

private struct StatsModelRankCard: View {
    @Environment(\.locale) private var locale
    let rows: [UsageStatsModelRow]

    var body: some View {
        StatsRankCard(
            title: "按模型",
            emptyText: "所选范围暂无数据",
            items: rows.map { row in
                StatsRankItem(
                    id: row.modelId,
                    label: StatsFormat.modelName(
                        row.modelName?.isEmpty == false ? row.modelName! : row.modelId,
                        locale: locale
                    ),
                    value: StatsFormat.duration(row.totalMs),
                    raw: Double(row.totalMs)
                )
            }
        )
    }
}

private struct StatsToolRankCard: View {
    let rows: [UsageStatsToolRow]

    var body: some View {
        StatsRankCard(
            title: "按工具",
            emptyText: "所选范围内没有工具调用",
            items: rows.map { row in
                StatsRankItem(id: row.toolKey, label: row.toolKey, value: StatsFormat.count(row.count), raw: Double(row.count))
            }
        )
    }
}

private struct StatsRankCard: View {
    @Environment(\.locale) private var locale
    let title: String
    let emptyText: String
    let items: [StatsRankItem]

    var body: some View {
        let ranked = Array(items.filter { $0.raw > 0 }.sorted { lhs, rhs in
            lhs.raw == rhs.raw ? lhs.label.localizedStandardCompare(rhs.label) == .orderedAscending : lhs.raw > rhs.raw
        }.prefix(8))
        let hiddenCount = max(0, items.filter { $0.raw > 0 }.count - ranked.count)
        let maximum = ranked.map(\.raw).max() ?? 0

        VStack(alignment: .leading, spacing: 14) {
            Text(title.localized(in: locale))
                .font(.quartet(.headline, weight: .semibold))
                .foregroundStyle(QuartetTheme.primaryText)

            if ranked.isEmpty {
                Text(emptyText.localized(in: locale))
                    .font(.quartet(.control))
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .frame(maxWidth: .infinity, minHeight: 72)
            } else {
                ForEach(Array(ranked.enumerated()), id: \.element.id) { index, item in
                    VStack(spacing: 7) {
                        HStack(spacing: 12) {
                            Text(item.label)
                                .font(.quartet(.control))
                                .foregroundStyle(QuartetTheme.primaryText)
                                .lineLimit(1)
                            Spacer(minLength: 8)
                            Text(item.value)
                                .font(.quartet(.detail, weight: .semibold))
                                .monospacedDigit()
                                .foregroundStyle(QuartetTheme.secondaryText)
                        }
                        GeometryReader { proxy in
                            ZStack(alignment: .leading) {
                                Capsule().fill(QuartetTheme.elevated)
                                Capsule()
                                    .fill(StatsFormat.rankColor(index))
                                    .frame(width: max(4, proxy.size.width * (maximum > 0 ? item.raw / maximum : 0)))
                            }
                        }
                        .frame(height: 6)
                        .accessibilityHidden(true)
                    }
                    .accessibilityElement(children: .combine)
                    .accessibilityLabel(String(
                        format: "%@，%@".localized(in: locale),
                        locale: locale,
                        item.label,
                        item.value
                    ))
                }

                if hiddenCount > 0 {
                    Text("另有 \(hiddenCount) 项未显示")
                        .font(.quartet(.detail))
                        .foregroundStyle(QuartetTheme.secondaryText)
                }
            }
        }
        .statsCard()
        .accessibilityIdentifier("stats-rank-\(title)")
    }
}

private struct StatsRankItem: Identifiable {
    let id: String
    let label: String
    let value: String
    let raw: Double
}

private enum StatsFormat {
    static let unknownModelID = "(unknown model)"

    static func duration(_ milliseconds: Int64) -> String {
        guard milliseconds >= 1_000 else { return "0s" }
        if milliseconds < 60_000 { return "\(milliseconds / 1_000)s" }
        if milliseconds < 3_600_000 { return "\(milliseconds / 60_000)m" }
        let hours = milliseconds / 3_600_000
        let minutes = milliseconds % 3_600_000 / 60_000
        return minutes == 0 ? "\(hours)h" : "\(hours)h \(minutes)m"
    }

    static func count(_ value: Int) -> String {
        guard value > 0 else { return "0" }
        if value < 1_000 { return String(value) }
        if value < 1_000_000 {
            return compactCount(value, divisor: 1_000, suffix: "K")
        }
        if value < 1_000_000_000 {
            return compactCount(value, divisor: 1_000_000, suffix: "M")
        }
        return compactCount(value, divisor: 1_000_000_000, suffix: "B")
    }

    private static func compactCount(_ value: Int, divisor: Double, suffix: String) -> String {
        let number = String(format: "%.1f", Double(value) / divisor)
        let trimmed = number.hasSuffix(".0") ? String(number.dropLast(2)) : number
        return trimmed + suffix
    }

    // Provider APIs differ on whether input already includes cache reads and
    // writes. Provider total minus output gives the shared input total; the
    // other candidates preserve partial reports and cap the rate at 100%.
    static func cacheHitRate(_ tokens: UsageStatsTokenTotals) -> Double? {
        let reportedInput = max(0, Double(tokens.reported) - Double(tokens.output))
        let input = max(0, Double(tokens.input))
        let cachedRead = max(0, Double(tokens.cachedRead))
        let cachedWrite = max(0, Double(tokens.cachedWrite))
        let providerInput = max(max(reportedInput, input), cachedRead + cachedWrite)
        guard providerInput > 0 else { return nil }
        return min(1, cachedRead / providerInput)
    }

    static func percentage(_ value: Double?, locale: Locale = AppLanguage.currentLocale) -> String {
        guard let value else { return "—" }
        return value.formatted(.percent.precision(.fractionLength(1)).locale(locale))
    }

    static func metricValue(_ totals: some UsageStatsTotals, metric: StatsTrendMetric) -> Double {
        optionalMetricValue(totals, metric: metric) ?? 0
    }

    static func optionalMetricValue(_ totals: some UsageStatsTotals, metric: StatsTrendMetric) -> Double? {
        switch metric {
        case .duration: Double(totals.totalMs)
        case .turns: Double(totals.turnCount)
        case .tokens: Double(totals.tokens.total)
        case .cache: cacheHitRate(totals.tokens)
        }
    }

    static func trend(_ value: Double?, metric: StatsTrendMetric) -> String {
        guard let value else { return "—" }
        return switch metric {
        case .duration: duration(Int64(max(0, value)))
        case .turns, .tokens: count(Int(max(0, value)))
        case .cache: percentage(value)
        }
    }

    // 批量转换时传入同一个 Calendar：Calendar.current 每次取值都会复制一份日历。
    static func dateKey(_ date: Date, calendar: Calendar = .current) -> String {
        let components = calendar.dateComponents([.year, .month, .day], from: date)
        return String(format: "%04d-%02d-%02d", components.year ?? 0, components.month ?? 0, components.day ?? 0)
    }

    static func date(_ key: String, calendar: Calendar = .current) -> Date? {
        let values = key.split(separator: "-").compactMap { Int($0) }
        guard values.count == 3 else { return nil }
        return calendar.date(from: DateComponents(year: values[0], month: values[1], day: values[2]))
    }

    static func modelName(_ value: String, locale: Locale = AppLanguage.currentLocale) -> String {
        value.isEmpty || value == unknownModelID || value == "__unknown_model__"
            ? "未知模型".localized(in: locale)
            : value
    }

    static func rankColor(_ index: Int) -> Color {
        let opacity = max(0.34, 1 - Double(index) * 0.085)
        return QuartetTheme.accent.opacity(opacity)
    }
}

private extension View {
    func statsCard(
        stroke: Color = QuartetTheme.divider,
        contentPadding: CGFloat = 16
    ) -> some View {
        padding(contentPadding)
            .background(QuartetTheme.surface, in: RoundedRectangle(cornerRadius: 18, style: .continuous))
            .overlay(
                RoundedRectangle(cornerRadius: 18, style: .continuous)
                    .stroke(stroke, lineWidth: 1)
            )
    }
}

private extension UsageStatsDailyRow {
    static func empty(date: String) -> UsageStatsDailyRow {
        UsageStatsDailyRow(
            date: date, totalMs: 0, turnCount: 0, assistantCount: 0, thoughtCount: 0, toolCallCount: 0,
            tokens: UsageStatsTokenTotals(total: 0),
            models: [:], modelNames: [:]
        )
    }
}
