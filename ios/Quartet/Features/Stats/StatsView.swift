import Charts
import SwiftUI
import UIKit

struct StatsView: View {
    @EnvironmentObject private var model: AppModel
    @Environment(\.scenePhase) private var scenePhase

    @State private var preset: StatsRangePreset = .thirtyDays
    @State private var customFrom = Calendar.current.date(byAdding: .day, value: -29, to: Date()) ?? Date()
    @State private var customTo = Date()
    @State private var metric: StatsTrendMetric = .duration
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
                    .font(.subheadline.weight(.semibold))
                    .foregroundStyle(QuartetTheme.primaryText)
                Spacer()
                if let report {
                    Text(rangeLabel(report.range))
                        .font(.caption.monospacedDigit())
                        .foregroundStyle(QuartetTheme.secondaryText)
                }
            }

            ScrollView(.horizontal) {
                HStack(spacing: 8) {
                    ForEach(StatsRangePreset.allCases) { item in
                        Button { preset = item } label: {
                            Text(item.title)
                                .font(.caption.weight(.semibold))
                                .foregroundStyle(preset == item ? QuartetTheme.onAccent : QuartetTheme.secondaryText)
                                .padding(.horizontal, 13)
                                .frame(height: 32)
                                .background(
                                    preset == item ? QuartetTheme.accent : QuartetTheme.elevated,
                                    in: Capsule()
                                )
                        }
                        .buttonStyle(.plain)
                        .accessibilityValue(preset == item ? "已选择" : "")
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
                .font(.subheadline)
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
                .font(.subheadline)
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
                .font(.headline)
                .foregroundStyle(QuartetTheme.failed)

            Text(detail)
                .font(.system(.caption, design: .monospaced))
                .foregroundStyle(QuartetTheme.primaryText)
                .textSelection(.enabled)
                .frame(maxWidth: .infinity, alignment: .leading)

            HStack(spacing: 16) {
                Button("重试") { refreshRevision &+= 1 }
                Button("复制完整错误") { UIPasteboard.general.string = detail }
            }
            .font(.subheadline.weight(.semibold))
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
        Label("Token 数为本地分词器估算值，非 API 计费口径。统计页沿用 Web 端展示口径，将已记录的输出侧估算值乘以 2。", systemImage: "info.circle")
            .font(.footnote)
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
        guard !range.from.isEmpty, !range.to.isEmpty else { return "全部" }
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
    let report: UsageStatsReport
    let periodDays: Int

    var body: some View {
        LazyVGrid(columns: [GridItem(.adaptive(minimum: 144), spacing: 12)], spacing: 12) {
            ForEach(cards) { card in
                VStack(alignment: .leading, spacing: 8) {
                    HStack {
                        Image(systemName: card.icon)
                            .foregroundStyle(card.color)
                        Text(card.title)
                            .font(.caption.weight(.medium))
                            .foregroundStyle(QuartetTheme.secondaryText)
                        Spacer(minLength: 0)
                    }
                    Text(card.value)
                        .font(.system(size: 25, weight: .semibold, design: .rounded))
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
            tokens += StatsFormat.displayedTokens(row.tokens.total)
            tools += row.toolCallCount
        }
        return [
            StatsKPICard(id: "duration", title: "总耗时", value: StatsFormat.duration(totalMs), current: Double(totalMs), previous: report.previous.map { Double($0.totalMs) }, icon: "clock", color: QuartetTheme.accent),
            StatsKPICard(id: "turns", title: "总轮次", value: StatsFormat.count(turns), current: Double(turns), previous: report.previous.map { Double($0.turnCount) }, icon: "bubble.left.and.bubble.right", color: QuartetTheme.chartGreen),
            StatsKPICard(id: "tokens", title: "Token", value: StatsFormat.count(tokens), current: Double(tokens), previous: report.previous.map { Double(StatsFormat.displayedTokens($0.tokensTotal)) }, icon: "text.word.spacing", color: QuartetTheme.running),
            StatsKPICard(id: "tools", title: "工具调用", value: StatsFormat.count(tools), current: Double(tools), previous: report.previous.map { Double($0.toolCallCount) }, icon: "wrench.and.screwdriver", color: QuartetTheme.chartForest),
            StatsKPICard(id: "workspaces", title: "工作区", value: StatsFormat.count(report.byWorkspace.count), current: Double(report.byWorkspace.count), previous: report.previous.map { Double($0.workspaceCount) }, icon: "square.grid.2x2", color: QuartetTheme.chartGraphite)
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
    let current: Double
    let previous: Double?
    let periodDays: Int

    var body: some View {
        Group {
            if let previous, previous > 0 {
                let delta = (current - previous) / previous * 100
                let direction = delta >= 0 ? "增加" : "减少"
                Label(
                    "\(Int(abs(delta).rounded()))%",
                    systemImage: delta >= 0 ? "arrow.up.right" : "arrow.down.right"
                )
                .foregroundStyle(delta >= 0 ? QuartetTheme.accent : QuartetTheme.secondaryText)
                .accessibilityLabel("较前 \(periodDays) 天\(direction) \(Int(abs(delta).rounded()))%")
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
        .font(.caption2.weight(.medium))
        .frame(minHeight: 14)
    }
}

private struct StatsTrendCard: View {
    let report: UsageStatsReport
    @Binding var metric: StatsTrendMetric
    @State private var selectedDate: Date?

    var body: some View {
        VStack(alignment: .leading, spacing: 14) {
            HStack {
                Text("使用趋势")
                    .font(.headline)
                    .foregroundStyle(QuartetTheme.primaryText)
                Spacer()
                Menu {
                    ForEach(StatsTrendMetric.allCases) { item in
                        Button { metric = item } label: {
                            if metric == item {
                                Label(item.title, systemImage: "checkmark")
                            } else {
                                Text(item.title)
                            }
                        }
                    }
                } label: {
                    HStack(spacing: 5) {
                        Text(metric.title)
                        Image(systemName: "chevron.up.chevron.down")
                            .font(.caption2.weight(.bold))
                    }
                    .font(.caption.weight(.semibold))
                    .foregroundStyle(QuartetTheme.accent)
                    .padding(.horizontal, 10)
                    .frame(height: 30)
                    .background(QuartetTheme.accent.opacity(0.1), in: Capsule())
                }
                .accessibilityLabel("趋势指标，当前为\(metric.title)")
                .accessibilityIdentifier("stats-trend-metric")
            }

            if series.allSatisfy({ $0.points.allSatisfy { $0.value <= 0 } }) {
                Text("所选范围暂无数据")
                    .font(.subheadline)
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .frame(maxWidth: .infinity, minHeight: 180)
            } else {
                Chart(series) { line in
                    ForEach(line.points) { point in
                        LineMark(
                            x: .value("日期", point.date),
                            y: .value(metric.title, point.value),
                            series: .value("系列", line.id)
                        )
                        .foregroundStyle(line.color)
                        .lineStyle(StrokeStyle(lineWidth: line.isTotal ? 2.7 : 1.8, lineCap: .round, lineJoin: .round))
                        .interpolationMethod(.catmullRom)

                        if line.isTotal {
                            PointMark(
                                x: .value("日期", point.date),
                                y: .value(metric.title, point.value)
                            )
                            .foregroundStyle(line.color)
                            .symbolSize(18)
                        }
                    }

                    if let selectedDate {
                        RuleMark(x: .value("选中日期", selectedDate))
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
                .accessibilityLabel("\(metric.title)使用趋势图")

                if let selectedDay {
                    HStack {
                        Text(selectedDay.date)
                            .foregroundStyle(QuartetTheme.secondaryText)
                        Spacer()
                        Text(StatsFormat.trend(StatsFormat.metricValue(selectedDay, metric: metric), metric: metric))
                            .fontWeight(.semibold)
                            .monospacedDigit()
                            .foregroundStyle(QuartetTheme.primaryText)
                    }
                    .font(.caption)
                    .padding(.horizontal, 10)
                    .frame(height: 34)
                    .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 9))
                }

                ScrollView(.horizontal) {
                    HStack(spacing: 14) {
                        ForEach(series) { line in
                            Label {
                                Text(line.name).lineLimit(1)
                            } icon: {
                                Circle().fill(line.color).frame(width: 8, height: 8)
                            }
                            .font(.caption)
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
        guard let selectedDate else { return nil }
        return filledDays.min { lhs, rhs in
            abs((StatsFormat.date(lhs.date) ?? .distantPast).timeIntervalSince(selectedDate))
                < abs((StatsFormat.date(rhs.date) ?? .distantPast).timeIntervalSince(selectedDate))
        }
    }

    private var series: [StatsTrendSeries] {
        let days = filledDays
        guard !days.isEmpty else { return [] }
        var result = [StatsTrendSeries(
            id: "__total__",
            name: "总计",
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
            let name = days.compactMap { $0.modelNames?[modelID] }.first ?? StatsFormat.modelName(modelID)
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
                id: modelID, name: StatsFormat.modelName(name),
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

private struct StatsWorkspaceRankCard: View {
    let rows: [UsageStatsWorkspaceRow]

    var body: some View {
        StatsRankCard(
            title: "按工作区",
            emptyText: "所选范围暂无数据",
            items: rows.map { row in
                StatsRankItem(
                    id: row.workspaceId,
                    label: (row.workspaceName?.isEmpty == false ? row.workspaceName! : row.workspaceId) + (row.deleted == true ? "（已删除）" : ""),
                    value: StatsFormat.duration(row.totalMs),
                    raw: Double(row.totalMs)
                )
            }
        )
    }
}

private struct StatsModelRankCard: View {
    let rows: [UsageStatsModelRow]

    var body: some View {
        StatsRankCard(
            title: "按模型",
            emptyText: "所选范围暂无数据",
            items: rows.map { row in
                StatsRankItem(
                    id: row.modelId,
                    label: StatsFormat.modelName(row.modelName?.isEmpty == false ? row.modelName! : row.modelId),
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
            Text(title)
                .font(.headline)
                .foregroundStyle(QuartetTheme.primaryText)

            if ranked.isEmpty {
                Text(emptyText)
                    .font(.subheadline)
                    .foregroundStyle(QuartetTheme.secondaryText)
                    .frame(maxWidth: .infinity, minHeight: 72)
            } else {
                ForEach(Array(ranked.enumerated()), id: \.element.id) { index, item in
                    VStack(spacing: 7) {
                        HStack(spacing: 12) {
                            Text(item.label)
                                .font(.subheadline)
                                .foregroundStyle(QuartetTheme.primaryText)
                                .lineLimit(1)
                            Spacer(minLength: 8)
                            Text(item.value)
                                .font(.caption.weight(.semibold))
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
                    .accessibilityLabel("\(item.label)，\(item.value)")
                }

                if hiddenCount > 0 {
                    Text("另有 \(hiddenCount) 项未显示")
                        .font(.caption)
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

    static func displayedTokens(_ storedTokens: Int) -> Int {
        storedTokens.multipliedReportingOverflow(by: 2).overflow ? Int.max : storedTokens * 2
    }

    static func metricValue(_ totals: some UsageStatsTotals, metric: StatsTrendMetric) -> Double {
        switch metric {
        case .duration: Double(totals.totalMs)
        case .turns: Double(totals.turnCount)
        case .tokens: Double(displayedTokens(totals.tokens.total))
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

    static func modelName(_ value: String) -> String {
        value.isEmpty || value == unknownModelID || value == "__unknown_model__" ? "未知模型" : value
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
            tokens: UsageStatsTokenTotals(total: 0, assistant: 0, thought: 0, toolCall: 0),
            models: [:], modelNames: [:]
        )
    }
}
