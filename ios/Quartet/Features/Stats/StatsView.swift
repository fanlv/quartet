import Charts
import SwiftUI
import UIKit

struct StatsView: View {
    @EnvironmentObject private var model: AppModel
    @Environment(\.scenePhase) private var scenePhase
    @Environment(\.locale) private var locale
    @Environment(\.mainTabBarInset) private var mainTabBarInset

    @State private var preset: StatsRangePreset = .thirtyDays
    @State private var customFrom = Calendar.current.date(byAdding: .day, value: -29, to: Date()) ?? Date()
    @State private var customTo = Date()
    @State private var metric: StatsTrendMetric = .tokens
    @State private var report: UsageStatsReport?
    @State private var isLoading = false
    @State private var errorDetail: String?
    @State private var refreshRevision = 0
    @State private var requestSequence: UInt64 = 0

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
                        StatsTrendCard(report: report, metric: $metric)
                        StatsWorkspaceRankCard(rows: report.byWorkspace)
                        StatsModelRankCard(rows: report.byModel)
                        StatsToolRankCard(rows: report.byTool)
                        noteCard
                    } else if !isLoading, errorDetail == nil {
                        emptyState
                    }
                }
                .padding(.horizontal, 16)
                .padding(.vertical, 14)
            }
            .background(QuartetTheme.canvas)
            .mainTabBarBottomInset(mainTabBarInset)
            .navigationTitle("使用统计")
            .navigationBarTitleDisplayMode(.inline)
            .refreshable { await loadStats() }
            .toolbar {
                ToolbarItem(placement: .topBarTrailing) {
                    Button { refreshRevision &+= 1 } label: {
                        if isLoading {
                            ProgressView()
                        } else {
                            Image(systemName: "arrow.clockwise")
                        }
                    }
                    .disabled(isLoading)
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
        .onChange(of: scenePhase) { _, phase in
            if phase == .active, report != nil {
                refreshRevision &+= 1
            }
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
        } description: {
            Text("完成一次 Agent 运行后，这里会显示耗时、Token、工具调用和趋势。")
        } actions: {
            Button("刷新") { refreshRevision &+= 1 }
        }
        .frame(maxWidth: .infinity, minHeight: 300)
        .statsCard()
        .accessibilityIdentifier("stats-empty")
    }

    private var noteCard: some View {
        Label("总 Token 优先使用服务商上报；未上报的轮次使用含图片的本地估算。来源明细可能重叠，不能相加。", systemImage: "info.circle")
            .font(.quartet(.detail))
            .foregroundStyle(QuartetTheme.secondaryText)
            .lineSpacing(3)
            .padding(.horizontal, 4)
    }

    private var loadKey: String {
        let from = preset == .custom ? StatsFormat.dateKey(customFrom) : ""
        let to = preset == .custom ? StatsFormat.dateKey(customTo) : ""
        return "\(preset.rawValue)|\(from)|\(to)|\(refreshRevision)"
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

    var id: String { rawValue }

    var title: String {
        switch self {
        case .duration: "耗时"
        case .turns: "轮次"
        case .tokens: "Token"
        }
    }
}

private struct StatsKPIGrid: View {
    @Environment(\.locale) private var locale
    let report: UsageStatsReport
    let periodDays: Int

    var body: some View {
        LazyVGrid(columns: [GridItem(.adaptive(minimum: 144), spacing: 12)], spacing: 12) {
            ForEach(cards) { card in
                VStack(alignment: .leading, spacing: 8) {
                    HStack {
                        Image(systemName: card.icon)
                            .foregroundStyle(card.color)
                        Text(card.title.localized(in: locale))
                            .font(.quartet(.detail, weight: .medium))
                            .foregroundStyle(QuartetTheme.secondaryText)
                        Spacer(minLength: 0)
                    }
                    Text(card.value)
                        .font(.quartet(.large, weight: .semibold))
                        .monospacedDigit()
                        .foregroundStyle(QuartetTheme.primaryText)
                        .minimumScaleFactor(0.72)
                        .lineLimit(1)
                    StatsDeltaLabel(current: card.current, previous: card.previous, periodDays: periodDays)
                }
                .frame(maxWidth: .infinity, minHeight: 104, alignment: .leading)
                .statsCard()
            }
        }
        .accessibilityIdentifier("stats-kpis")
    }

    private var cards: [StatsKPICard] {
        var totalMs: Int64 = 0
        var turns = 0
        var tokens = 0
        var tools = 0
        for row in report.byWorkspace {
            totalMs += row.totalMs
            turns += row.turnCount
            tokens += row.tokens.total
            tools += row.toolCallCount
        }
        return [
            StatsKPICard(id: "duration", title: "总耗时", value: StatsFormat.duration(totalMs), current: Double(totalMs), previous: report.previous.map { Double($0.totalMs) }, icon: "clock", color: QuartetTheme.accent),
            StatsKPICard(id: "turns", title: "总轮次", value: StatsFormat.count(turns), current: Double(turns), previous: report.previous.map { Double($0.turnCount) }, icon: "bubble.left.and.bubble.right", color: QuartetTheme.chartGreen),
            StatsKPICard(id: "tokens", title: "Token", value: StatsFormat.count(tokens), current: Double(tokens), previous: report.previous.map { Double($0.tokensTotal) }, icon: "text.word.spacing", color: QuartetTheme.running),
            StatsKPICard(id: "tools", title: "工具调用", value: StatsFormat.count(tools), current: Double(tools), previous: report.previous.map { Double($0.toolCallCount) }, icon: "wrench.and.screwdriver", color: QuartetTheme.chartForest),
            StatsKPICard(id: "workspaces", title: "统计工作区", value: StatsFormat.count(report.byWorkspace.count), current: Double(report.byWorkspace.count), previous: report.previous.map { Double($0.workspaceCount) }, icon: "square.grid.2x2", color: QuartetTheme.chartGraphite)
        ]
    }
}

private struct StatsKPICard: Identifiable {
    let id: String
    let title: String
    let value: String
    let current: Double
    let previous: Double?
    let icon: String
    let color: Color
}

private struct StatsDeltaLabel: View {
    @Environment(\.locale) private var locale
    let current: Double
    let previous: Double?
    let periodDays: Int

    var body: some View {
        Group {
            if let previous, previous > 0 {
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
            } else if previous == 0, current > 0 {
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
    @State private var selectedDate: Date?

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack(spacing: 12) {
                Text(metric == .tokens ? "每日 Token" : "使用趋势")
                    .font(.quartet(.headline, weight: .semibold))
                    .foregroundStyle(QuartetTheme.primaryText)
                Spacer()
                Picker("趋势指标", selection: $metric) {
                    ForEach(StatsTrendMetric.allCases) { item in
                        Text(item.title.localized(in: locale)).tag(item)
                    }
                }
                .pickerStyle(.segmented)
                .font(.quartet(.compact, weight: .semibold))
                .frame(maxWidth: 194)
                .accessibilityLabel("趋势指标")
                .accessibilityValue(metric.title.localized(in: locale))
                .accessibilityIdentifier("stats-trend-metric")
            }

            if metric == .tokens {
                StatsTokenCoverageNote(rows: filledDays)
            }

            if series.allSatisfy({ $0.points.allSatisfy { $0.value <= 0 } }) {
                Text("所选范围暂无数据")
                    .font(.quartet(.control))
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .frame(maxWidth: .infinity, minHeight: 180)
            } else {
                Chart(series) { line in
                    ForEach(line.points) { point in
                        LineMark(
                            x: .value("日期".localized(in: locale), point.date),
                            y: .value(metric.title.localized(in: locale), point.value),
                            series: .value("系列".localized(in: locale), line.id)
                        )
                        .foregroundStyle(line.color)
                        .lineStyle(StrokeStyle(lineWidth: line.isTotal ? 2.7 : 1.8, lineCap: .round, lineJoin: .round))
                        .interpolationMethod(.catmullRom)

                        if line.isTotal {
                            PointMark(
                                x: .value("日期".localized(in: locale), point.date),
                                y: .value(metric.title.localized(in: locale), point.value)
                            )
                            .foregroundStyle(line.color)
                            .symbolSize(18)
                        }
                    }

                    if let selectedDate {
                        RuleMark(x: .value("选中日期".localized(in: locale), selectedDate))
                            .foregroundStyle(QuartetTheme.secondaryText.opacity(0.55))
                            .lineStyle(StrokeStyle(lineWidth: 1, dash: [4, 4]))
                    }
                }
                .chartXAxis {
                    AxisMarks(values: .automatic(desiredCount: 5)) {
                        AxisGridLine().foregroundStyle(QuartetTheme.divider.opacity(0.45))
                        AxisValueLabel(format: .dateTime.month(.twoDigits).day(.twoDigits))
                            .foregroundStyle(QuartetTheme.secondaryText)
                    }
                }
                .chartYAxis {
                    AxisMarks(position: .leading, values: .automatic(desiredCount: 4)) { value in
                        AxisGridLine().foregroundStyle(QuartetTheme.divider.opacity(0.55))
                        AxisValueLabel {
                            if let raw = value.as(Double.self) {
                                Text(StatsFormat.trend(raw, metric: metric))
                                    .foregroundStyle(QuartetTheme.secondaryText)
                            }
                        }
                    }
                }
                .chartXSelection(value: $selectedDate)
                .frame(height: 220)
                .accessibilityElement(children: .ignore)
                .accessibilityLabel(String(
                    format: "%@使用趋势图".localized(in: locale),
                    locale: locale,
                    metric.title.localized(in: locale)
                ))
                .accessibilityValue(chartAccessibilityValue)
                .accessibilityHint("上下轻扫以逐日浏览")
                .accessibilityAdjustableAction { direction in
                    adjustAccessibilitySelection(direction)
                }

                if let selectedDay {
                    if metric == .tokens {
                        StatsTokenDayDetail(day: selectedDay)
                    } else {
                        HStack {
                            Text(selectedDay.date)
                                .foregroundStyle(QuartetTheme.secondaryText)
                            Spacer()
                            Text(StatsFormat.trend(StatsFormat.metricValue(selectedDay, metric: metric), metric: metric))
                                .font(.quartet(.detail, weight: .semibold))
                                .monospacedDigit()
                                .foregroundStyle(QuartetTheme.primaryText)
                        }
                        .font(.quartet(.detail))
                        .padding(.horizontal, 10)
                        .frame(height: 34)
                        .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 9))
                        .accessibilityElement(children: .combine)
                    }
                }

                ScrollView(.horizontal) {
                    HStack(spacing: 14) {
                        ForEach(series) { line in
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

    private var selectedDay: UsageStatsDailyRow? {
        guard let selectedDate else {
            return metric == .tokens ? filledDays.last : nil
        }
        return filledDays.min { lhs, rhs in
            abs((StatsFormat.date(lhs.date) ?? .distantPast).timeIntervalSince(selectedDate))
                < abs((StatsFormat.date(rhs.date) ?? .distantPast).timeIntervalSince(selectedDate))
        }
    }

    private var chartAccessibilityValue: String {
        guard let selectedDay else {
            return String(
                format: "共 %lld 天".localized(in: locale),
                locale: locale,
                Int64(filledDays.count)
            )
        }
        let value = metric == .tokens
            ? StatsFormat.exactCount(selectedDay.tokens.total, locale: locale)
            : StatsFormat.trend(StatsFormat.metricValue(selectedDay, metric: metric), metric: metric)
        return String(
            format: "%@，%@".localized(in: locale),
            locale: locale,
            selectedDay.date,
            value
        )
    }

    private func adjustAccessibilitySelection(_ direction: AccessibilityAdjustmentDirection) {
        guard !filledDays.isEmpty else { return }
        let currentIndex = selectedDay.flatMap { selected in
            filledDays.firstIndex { $0.date == selected.date }
        }
        let targetIndex: Int
        switch direction {
        case .increment:
            targetIndex = min((currentIndex ?? -1) + 1, filledDays.count - 1)
        case .decrement:
            targetIndex = max((currentIndex ?? filledDays.count) - 1, 0)
        @unknown default:
            return
        }
        selectedDate = StatsFormat.date(filledDays[targetIndex].date)
    }

    private var series: [StatsTrendSeries] {
        let days = filledDays
        guard !days.isEmpty else { return [] }
        var result = [StatsTrendSeries(
            id: "__total__",
            name: "总计".localized(in: locale),
            color: QuartetTheme.accent,
            isTotal: true,
            points: days.compactMap { row in
                guard let date = StatsFormat.date(row.date) else { return nil }
                return StatsTrendPoint(dateKey: row.date, date: date, value: StatsFormat.metricValue(row, metric: metric))
            }
        )]

        var modelIDSet = Set(days.flatMap { $0.models?.keys.map { $0 } ?? [] })
        if days.contains(where: { row in
            let attributed = row.models?.values.reduce(0) { partial, totals in
                partial + StatsFormat.metricValue(totals, metric: metric)
            } ?? 0
            return StatsFormat.metricValue(row, metric: metric) > attributed
        }) {
            modelIDSet.insert(StatsFormat.unknownModelID)
        }
        let modelIDs = modelIDSet.sorted()
        let palette: [Color] = [
            QuartetTheme.chartGreen,
            QuartetTheme.chartForest,
            QuartetTheme.chartMutedGreen,
            QuartetTheme.chartRed,
            QuartetTheme.chartMint,
            QuartetTheme.chartGraphite
        ]
        for (index, modelID) in modelIDs.enumerated() {
            let name = days.compactMap { $0.modelNames?[modelID] }.first
                ?? StatsFormat.modelName(modelID, locale: locale)
            let points = days.compactMap { row -> StatsTrendPoint? in
                guard let date = StatsFormat.date(row.date) else { return nil }
                var value = row.models?[modelID].map { StatsFormat.metricValue($0, metric: metric) } ?? 0
                if modelID == StatsFormat.unknownModelID {
                    let attributed = row.models?.values.reduce(0) { partial, totals in
                        partial + StatsFormat.metricValue(totals, metric: metric)
                    } ?? 0
                    value += max(0, StatsFormat.metricValue(row, metric: metric) - attributed)
                }
                return StatsTrendPoint(dateKey: row.date, date: date, value: value)
            }
            result.append(StatsTrendSeries(
                id: modelID, name: StatsFormat.modelName(name, locale: locale),
                color: palette[index % palette.count], isTotal: false, points: points
            ))
        }
        return result
    }

    private var filledDays: [UsageStatsDailyRow] {
        guard let from = StatsFormat.date(report.range.from),
              let to = StatsFormat.date(report.range.to),
              from <= to else {
            return report.daily.sorted { $0.date < $1.date }
        }
        let byDate = Dictionary(uniqueKeysWithValues: report.daily.map { ($0.date, $0) })
        var result: [UsageStatsDailyRow] = []
        var current = from
        while current <= to {
            let key = StatsFormat.dateKey(current)
            result.append(byDate[key] ?? .empty(date: key))
            guard let next = Calendar.current.date(byAdding: .day, value: 1, to: current) else { break }
            current = next
        }
        return result
    }
}

private struct StatsTrendSeries: Identifiable {
    let id: String
    let name: String
    let color: Color
    let isTotal: Bool
    let points: [StatsTrendPoint]
}

private struct StatsTrendPoint: Identifiable {
    let dateKey: String
    let date: Date
    let value: Double

    var id: String { dateKey }
}

private struct StatsTokenCoverageNote: View {
    @Environment(\.locale) private var locale
    let rows: [UsageStatsDailyRow]

    var body: some View {
        let coverage = StatsTokenCoverage(rows: rows)
        VStack(alignment: .leading, spacing: 3) {
            Label("Token 来源", systemImage: "checkmark.seal")
                .font(.quartet(.detail, weight: .semibold))
                .foregroundStyle(QuartetTheme.primaryText)

            if coverage.totalTurns <= 0 {
                Text("暂无可计算来源的轮次。")
            } else if coverage.estimatedTurns <= 0 {
                Text(String(
                    format: "全部 %lld 轮均由 Provider 上报。".localized(in: locale),
                    locale: locale,
                    Int64(coverage.totalTurns)
                ))
            } else {
                Text(String(
                    format: "Provider 上报覆盖 %lld/%lld 轮（%lld%%），本地估算 %lld 轮。".localized(in: locale),
                    locale: locale,
                    Int64(coverage.reportedTurns),
                    Int64(coverage.totalTurns),
                    Int64(coverage.reportedPercent),
                    Int64(coverage.estimatedTurns)
                ))
            }
        }
        .font(.quartet(.compact))
        .foregroundStyle(QuartetTheme.secondaryText)
        .accessibilityElement(children: .combine)
        .accessibilityIdentifier("stats-token-coverage")
    }
}

// Every recorded turn is classified as either provider-reported or locally
// estimated, so the two counters always add up to the turn count.
private struct StatsTokenCoverage {
    let totalTurns: Int
    let reportedTurns: Int
    let estimatedTurns: Int
    let reportedPercent: Int

    init(rows: [UsageStatsDailyRow]) {
        totalTurns = rows.reduce(0) { $0 + max(0, $1.turnCount) }
        reportedTurns = rows.reduce(0) { $0 + max(0, $1.tokens.reportedTurns) }
        estimatedTurns = rows.reduce(0) { $0 + max(0, $1.tokens.estimatedTurns) }
        reportedPercent = totalTurns > 0
            ? Int((Double(reportedTurns) / Double(totalTurns) * 100).rounded())
            : 0
    }
}

private struct StatsTokenDayDetail: View {
    @Environment(\.locale) private var locale
    let day: UsageStatsDailyRow

    var body: some View {
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
                Text(StatsFormat.exactCount(day.tokens.total, locale: locale))
                    .contentTransition(.numericText())
                    .font(.quartet(.headline, weight: .semibold))
                    .monospacedDigit()
                    .foregroundStyle(QuartetTheme.primaryText)
            }

            Text("总量由服务端计算；下列来源与明细可能重叠，请勿相加。")
                .font(.quartet(.compact))
                .foregroundStyle(QuartetTheme.secondaryText)

            Divider().overlay(QuartetTheme.divider)

            Text("来源与明细")
                .font(.quartet(.detail, weight: .semibold))
                .foregroundStyle(QuartetTheme.primaryText)

            LazyVGrid(columns: columns, alignment: .leading, spacing: 9) {
                ForEach(details) { detail in
                    VStack(alignment: .leading, spacing: 2) {
                        Text(detail.title.localized(in: locale))
                            .font(.quartet(.compact))
                            .foregroundStyle(QuartetTheme.secondaryText)
                        Text(StatsFormat.exactCount(detail.value, locale: locale))
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
                        StatsFormat.exactCount(detail.value, locale: locale)
                    ))
                }
            }

            Text("图片 Token 为估算值，可能已包含在输入 Token 中。")
                .font(.quartet(.compact))
                .foregroundStyle(QuartetTheme.secondaryText)

            StatsTokenCoverageNote(rows: [day])
        }
        .padding(12)
        .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 12, style: .continuous))
        .accessibilityElement(children: .contain)
        .accessibilityLabel(String(
            format: "%@ Token 明细，总 Token %@".localized(in: locale),
            locale: locale,
            day.date,
            StatsFormat.exactCount(day.tokens.total, locale: locale)
        ))
        .accessibilityIdentifier("stats-token-day-detail")
    }

    private var columns: [GridItem] {
        [GridItem(.flexible(), spacing: 12), GridItem(.flexible(), spacing: 12)]
    }

    private var details: [StatsTokenDetail] {
        [
            StatsTokenDetail(id: "reported", title: "Provider 上报", value: day.tokens.reported),
            StatsTokenDetail(id: "input", title: "输入 Token", value: day.tokens.input),
            StatsTokenDetail(id: "output", title: "输出 Token", value: day.tokens.output),
            StatsTokenDetail(id: "cached-read", title: "缓存读取", value: day.tokens.cachedRead),
            StatsTokenDetail(id: "cached-write", title: "缓存写入", value: day.tokens.cachedWrite),
            StatsTokenDetail(id: "reasoning", title: "推理 Token", value: day.tokens.reasoning),
            StatsTokenDetail(id: "image", title: "图片估算", value: day.tokens.imageEstimate),
            StatsTokenDetail(id: "estimated", title: "本地估算", value: day.tokens.estimated)
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
            return value < 10_000
                ? String(format: "%.1fK", Double(value) / 1_000)
                : String(format: "%.0fK", Double(value) / 1_000)
        }
        return value < 10_000_000
            ? String(format: "%.1fM", Double(value) / 1_000_000)
            : String(format: "%.0fM", Double(value) / 1_000_000)
    }

    static func exactCount(_ value: Int, locale: Locale = AppLanguage.currentLocale) -> String {
        max(0, value).formatted(.number.locale(locale).grouping(.automatic))
    }

    static func metricValue(_ totals: some UsageStatsTotals, metric: StatsTrendMetric) -> Double {
        switch metric {
        case .duration: Double(totals.totalMs)
        case .turns: Double(totals.turnCount)
        case .tokens: Double(totals.tokens.total)
        }
    }

    static func trend(_ value: Double, metric: StatsTrendMetric) -> String {
        switch metric {
        case .duration: duration(Int64(max(0, value)))
        case .turns, .tokens: count(Int(max(0, value)))
        }
    }

    static func dateKey(_ date: Date) -> String {
        let components = Calendar.current.dateComponents([.year, .month, .day], from: date)
        return String(format: "%04d-%02d-%02d", components.year ?? 0, components.month ?? 0, components.day ?? 0)
    }

    static func date(_ key: String) -> Date? {
        let values = key.split(separator: "-").compactMap { Int($0) }
        guard values.count == 3 else { return nil }
        return Calendar.current.date(from: DateComponents(year: values[0], month: values[1], day: values[2]))
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
    func statsCard(stroke: Color = QuartetTheme.divider) -> some View {
        padding(16)
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
