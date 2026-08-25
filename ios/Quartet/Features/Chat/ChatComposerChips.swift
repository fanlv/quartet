import SwiftUI
import UIKit

struct ComposerMetadataChip: View {
    let icon: String?
    let agentIconUrl: String?
    let text: String
    let accessibilityLabel: String

    init(icon: String, text: String, accessibilityLabel: String) {
        self.icon = icon
        agentIconUrl = nil
        self.text = text
        self.accessibilityLabel = accessibilityLabel
    }

    init(agentIconUrl: String?, text: String, accessibilityLabel: String) {
        icon = nil
        self.agentIconUrl = agentIconUrl
        self.text = text
        self.accessibilityLabel = accessibilityLabel
    }

    var body: some View {
        HStack(spacing: 5) {
            if let icon {
                Image(systemName: icon)
                    .font(.chat(.detail, weight: .semibold))
            } else {
                AgentIdentityIcon(iconUrl: agentIconUrl)
            }
            Text(text)
                .font(.chat(.detail, weight: .medium))
                .lineLimit(1)
        }
        .foregroundStyle(QuartetTheme.secondaryText)
        .padding(.horizontal, 9)
        .frame(height: 30)
        .background(QuartetTheme.elevated, in: RoundedRectangle(cornerRadius: 8, style: .continuous))
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(accessibilityLabel)
    }
}

enum AgentUsageProvider: String {
    case codex
    case claude
    case antigravity
    case kimi
    case qoder

    static func resolve(command: String, displayName: String) -> Self? {
        let candidate = "\(command) \(displayName)".lowercased()
        if candidate.contains("antigravity") { return .antigravity }
        if candidate.contains("codex") { return .codex }
        if candidate.contains("claude") { return .claude }
        if candidate.contains("qoder") || candidate.contains("qcode") { return .qoder }
        if candidate.contains("kimi") { return .kimi }
        return nil
    }
}

enum AgentUsageCache {
    static func usage(provider: AgentUsageProvider, namespace: String) -> AgentUsageResponse? {
        guard let data = UserDefaults.standard.data(forKey: key("agentUsage_\(provider.rawValue)", namespace: namespace)) else { return nil }
        return try? JSONDecoder().decode(AgentUsageResponse.self, from: data)
    }

    static func setUsage(_ usage: AgentUsageResponse, provider: AgentUsageProvider, namespace: String) {
        guard let data = try? JSONEncoder().encode(usage) else { return }
        UserDefaults.standard.set(data, forKey: key("agentUsage_\(provider.rawValue)", namespace: namespace))
    }

    static func version(command: String, namespace: String) -> String {
        UserDefaults.standard.string(forKey: key("agentVersion_\(command)", namespace: namespace)) ?? ""
    }

    static func setVersion(_ version: String, command: String, namespace: String) {
        let key = key("agentVersion_\(command)", namespace: namespace)
        if version.isEmpty {
            UserDefaults.standard.removeObject(forKey: key)
        } else {
            UserDefaults.standard.set(version, forKey: key)
        }
    }

    private static func key(_ value: String, namespace: String) -> String {
        "\(namespace)|\(value)"
    }
}

struct AgentUsageDetail: Identifiable {
    let id = UUID()
    let title: String
    let lines: [String]
}

struct AgentUsageStrip: View {
    @EnvironmentObject private var appModel: AppModel

    let command: String
    let displayName: String

    @State private var usage: AgentUsageResponse?
    @State private var version = ""
    @State private var loading = false
    @State private var requestError: APIError?
    @State private var detail: AgentUsageDetail?

    private var provider: AgentUsageProvider? {
        AgentUsageProvider.resolve(command: command, displayName: displayName)
    }

    private var identity: String {
        "\(appModel.serverAddress):\(provider?.rawValue ?? "version"):\(command)"
    }

    var body: some View {
        Group {
            if provider != nil || !version.isEmpty || requestError != nil {
                HStack(spacing: 5) {
                    usageContent

                    if loading {
                        ProgressView()
                            .controlSize(.mini)
                            .tint(QuartetTheme.secondaryText)
                            .accessibilityLabel("正在获取 Agent 用量")
                    } else {
                        Button {
                            Task { await refresh() }
                        } label: {
                            Image(systemName: "arrow.clockwise")
                                .font(.system(size: 10, weight: .semibold))
                                .foregroundStyle(QuartetTheme.secondaryText.opacity(0.72))
                                .frame(width: 22, height: 22)
                        }
                        .buttonStyle(.plain)
                        .accessibilityLabel("刷新 Agent 用量")
                    }

                    if let requestError {
                        Button {
                            appModel.present(requestError)
                        } label: {
                            Image(systemName: "exclamationmark.triangle.fill")
                                .font(.system(size: 10, weight: .semibold))
                                .foregroundStyle(QuartetTheme.failed)
                                .frame(width: 22, height: 22)
                        }
                        .buttonStyle(.plain)
                        .accessibilityLabel("查看 Agent 用量错误")
                    }
                }
                .padding(.leading, 4)
                .accessibilityElement(children: .contain)
            }
        }
        .task(id: identity) {
            restoreCache()
            if appModel.isRunningUITests {
                version = "v1.0.0"
                return
            }
            await refresh()
        }
        .popover(item: $detail, attachmentAnchor: .rect(.bounds), arrowEdge: .bottom) { value in
            VStack(alignment: .leading, spacing: 6) {
                Text(value.title)
                    .font(.chat(.detail, weight: .semibold))
                    .foregroundStyle(QuartetTheme.primaryText)
                ForEach(Array(value.lines.enumerated()), id: \.offset) { _, line in
                    Text(line)
                        .font(.chat(.compact, design: .monospaced))
                        .foregroundStyle(QuartetTheme.secondaryText)
                }
            }
            .padding(12)
            .background(QuartetTheme.surface)
            .presentationCompactAdaptation(.popover)
            .presentationBackground(QuartetTheme.surface)
        }
    }

    @ViewBuilder
    private var usageContent: some View {
        if let provider, let usage {
            switch provider {
            case .codex:
                if let value = usage.codex { codexContent(value) }
            case .claude:
                if let value = usage.claude { claudeContent(value) }
            case .antigravity:
                if let value = usage.antigravity { antigravityContent(value) }
            case .kimi:
                if let value = usage.kimi { kimiContent(value) }
            case .qoder:
                if let value = usage.qoder { qoderContent(value) }
            }
        } else if !version.isEmpty {
            versionLabel(version)
        }
    }

    private func codexContent(_ value: CodexAgentUsage) -> some View {
        Group {
            if let version = displayValue(value.version) { versionLabel(version) }
            if let window = value.primaryWindow { usageRing(label: windowLabel(window), window: window) }
            if let window = value.secondaryWindow { usageRing(label: windowLabel(window), window: window) }
            usageRing(
                label: String(value.resetCredits),
                percent: value.resetCredits > 0 ? 100 : 0,
                color: value.resetCredits > 0 ? QuartetTheme.accentDeep : QuartetTheme.secondaryText.opacity(0.55),
                detail: AgentUsageDetail(
                    title: "剩余 \(value.resetCredits) 次重置额度",
                    lines: (value.resetCreditExpiries ?? []).map { "\(formatDate($0, includesDate: true)) 到期" }
                )
            )
        }
    }

    private func claudeContent(_ value: ClaudeAgentUsage) -> some View {
        Group {
            if let version = displayValue(value.version) { versionLabel(version) }
            usageMetric(icon: "calendar", text: money(value.todayCost), emphasis: QuartetTheme.running, label: "今日花费 \(money(value.todayCost))")
            usageMetric(icon: "sum", text: money(value.totalCost), emphasis: QuartetTheme.primaryText, label: "累计花费 \(money(value.totalCost))")
        }
    }

    private func antigravityContent(_ value: AntigravityAgentUsage) -> some View {
        Group {
            if let version = displayValue(value.version) { versionLabel(version) }
            if value.claude5h != nil || value.claudeWeekly != nil {
                usageGroup(mark: "C", windows: [("5h", value.claude5h), ("7d", value.claudeWeekly)])
            }
            if value.gemini5h != nil || value.geminiWeekly != nil {
                usageGroup(mark: "G", windows: [("5h", value.gemini5h), ("7d", value.geminiWeekly)])
            }
        }
    }

    private func kimiContent(_ value: KimiAgentUsage) -> some View {
        Group {
            if let version = displayValue(value.version) { versionLabel(version) }
            if let window = value.weekly { usageRing(label: windowLabel(window), window: window) }
            if let window = value.fiveHour { usageRing(label: windowLabel(window), window: window) }
            if let window = value.total {
                usageRing(
                    label: "Σ",
                    percent: window.usedPercent,
                    color: usageColor(window.usedPercent),
                    detail: AgentUsageDetail(
                        title: "累计额度 \(Int(window.usedPercent.rounded()))%",
                        lines: value.parallelLimit.map { ["并发上限 \($0)"] } ?? []
                    )
                )
            }
        }
    }

    private func qoderContent(_ value: QoderAgentUsage) -> some View {
        Group {
            if let version = displayValue(value.version) { versionLabel(version) }
            Button {
                var lines: [String] = []
                if let plan = displayValue(value.planType) {
                    lines.append(plan.split(separator: "_").map { $0.capitalized }.joined(separator: " "))
                }
                if let expiresAt = value.expiresAt, expiresAt > 0 {
                    lines.append("\(formatDate(expiresAt, includesDate: true)) 到期")
                }
                if value.quotaExceeded { lines.append("额度已用尽") }
                detail = AgentUsageDetail(
                    title: "已用 \(credits(value.used)) / \(credits(value.total))",
                    lines: lines
                )
            } label: {
                HStack(spacing: 4) {
                    Image(systemName: "dollarsign.circle")
                        .font(.system(size: 11, weight: .medium))
                    Text(credits(value.used))
                        .fontWeight(.bold)
                        .foregroundStyle(usageColor(value.usedPercent))
                    Text("/ \(credits(value.total))")
                        .foregroundStyle(QuartetTheme.secondaryText.opacity(0.75))
                    GeometryReader { proxy in
                        ZStack(alignment: .leading) {
                            Capsule().fill(QuartetTheme.divider)
                            Capsule()
                                .fill(usageColor(value.usedPercent))
                                .frame(width: proxy.size.width * min(max(value.usedPercent, 0), 100) / 100)
                        }
                    }
                    .frame(width: 38, height: 4)
                }
                .font(.chat(.detail))
                .foregroundStyle(QuartetTheme.secondaryText)
            }
            .buttonStyle(.plain)
            .accessibilityLabel("Qoder 已用 \(credits(value.used))，总额度 \(credits(value.total))")
        }
    }

    private func versionLabel(_ value: String) -> some View {
        Text(value)
            .font(.chat(.detail, weight: .medium, design: .monospaced))
            .foregroundStyle(QuartetTheme.secondaryText.opacity(0.74))
            .lineLimit(1)
            .accessibilityLabel("Agent 版本 \(value)")
    }

    private func usageMetric(icon: String, text: String, emphasis: Color, label: String) -> some View {
        HStack(spacing: 3) {
            Image(systemName: icon)
                .font(.system(size: 10, weight: .medium))
            Text(text)
                .fontWeight(.bold)
                .foregroundStyle(emphasis)
        }
        .font(.chat(.detail, design: .monospaced))
        .foregroundStyle(QuartetTheme.secondaryText)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel(label)
    }

    private func usageGroup(mark: String, windows: [(String, AgentUsageWindow?)]) -> some View {
        HStack(spacing: 2) {
            Text(mark)
                .font(.system(size: 9, weight: .bold, design: .monospaced))
                .foregroundStyle(QuartetTheme.secondaryText)
                .padding(.trailing, 2)
            ForEach(Array(windows.enumerated()), id: \.offset) { _, item in
                if let window = item.1 { usageRing(label: item.0, window: window) }
            }
        }
        .padding(2)
        .background(QuartetTheme.elevated.opacity(0.8), in: RoundedRectangle(cornerRadius: 6, style: .continuous))
        .overlay(RoundedRectangle(cornerRadius: 6, style: .continuous).stroke(QuartetTheme.divider, lineWidth: 1))
        .accessibilityElement(children: .contain)
    }

    private func usageRing(label: String, window: AgentUsageWindow) -> some View {
        usageRing(
            label: label,
            percent: window.usedPercent,
            color: usageColor(window.usedPercent),
            detail: AgentUsageDetail(
                title: "\(label) \(Int(window.usedPercent.rounded()))%",
                lines: ["\(formatReset(window)) 重置"]
            )
        )
    }

    private func usageRing(label: String, percent: Double, color: Color, detail value: AgentUsageDetail) -> some View {
        Button { detail = value } label: {
            ZStack {
                Circle()
                    .stroke(QuartetTheme.divider, lineWidth: 3)
                Circle()
                    .trim(from: 0, to: min(max(percent, 0), 100) / 100)
                    .stroke(color, style: StrokeStyle(lineWidth: 3, lineCap: .round))
                    .rotationEffect(.degrees(-90))
                Text(label)
                    .font(.system(size: 8, weight: .semibold, design: .monospaced))
                    .foregroundStyle(color)
                    .minimumScaleFactor(0.7)
            }
            .frame(width: 22, height: 22)
        }
        .buttonStyle(.plain)
        .accessibilityLabel("\(value.title)，\(value.lines.joined(separator: "，"))")
    }

    private func restoreCache() {
        requestError = nil
        usage = provider.flatMap {
            AgentUsageCache.usage(provider: $0, namespace: appModel.serverAddress)
        }
        version = provider == nil
            ? AgentUsageCache.version(command: command, namespace: appModel.serverAddress)
            : ""
    }

    private func refresh() async {
        loading = true
        requestError = nil
        defer { loading = false }

        do {
            if let provider {
                let response = try await appModel.apiClient().agentUsage(provider: provider.rawValue)
                try Task.checkCancellation()
                usage = response
                AgentUsageCache.setUsage(response, provider: provider, namespace: appModel.serverAddress)
            } else {
                let response = try await appModel.apiClient().agentVersion(command: command)
                try Task.checkCancellation()
                version = response.version?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
                AgentUsageCache.setVersion(version, command: command, namespace: appModel.serverAddress)
            }
        } catch is CancellationError {
            return
        } catch let error as APIError {
            requestError = error
        } catch {
            requestError = APIError(summary: "Agent 用量加载失败", detail: String(describing: error))
        }
    }

    private func usageColor(_ percent: Double) -> Color {
        if percent >= 80 { return QuartetTheme.failed }
        if percent >= 50 { return QuartetTheme.warning }
        return QuartetTheme.success
    }

    private func windowLabel(_ window: AgentUsageWindow) -> String {
        let seconds = window.limitWindowSeconds
        if seconds > 0, seconds % 86_400 == 0 { return "\(seconds / 86_400)d" }
        if seconds > 0, seconds % 3_600 == 0 { return "\(seconds / 3_600)h" }
        if seconds > 0, seconds % 60 == 0 { return "\(seconds / 60)m" }
        return "\(max(0, seconds))s"
    }

    private func formatReset(_ window: AgentUsageWindow) -> String {
        let seconds = window.resetAt > 0
            ? TimeInterval(window.resetAt)
            : Date().timeIntervalSince1970 + TimeInterval(window.resetAfterSeconds)
        return formatDate(Int64(seconds), includesDate: window.limitWindowSeconds >= 86_400)
    }

    /// 两个 formatter 复用，不再每次调用新建 —— 用量胶囊会随 composer 一起频繁重排。
    @MainActor
    private static let dateTimeFormatter: DateFormatter = {
        let formatter = DateFormatter()
        formatter.locale = Locale(identifier: "zh_CN")
        formatter.dateFormat = "MM-dd HH:mm"
        return formatter
    }()

    @MainActor
    private static let timeOnlyFormatter: DateFormatter = {
        let formatter = DateFormatter()
        formatter.locale = Locale(identifier: "zh_CN")
        formatter.dateFormat = "HH:mm"
        return formatter
    }()

    private func formatDate(_ unixSeconds: Int64, includesDate: Bool) -> String {
        let formatter = includesDate ? Self.dateTimeFormatter : Self.timeOnlyFormatter
        return formatter.string(from: Date(timeIntervalSince1970: TimeInterval(unixSeconds)))
    }

    private func money(_ value: Double) -> String { String(format: "$%.2f", value) }

    private func credits(_ value: Double) -> String {
        value.rounded() == value ? String(Int64(value)) : String(format: "%.1f", value)
    }

    private func displayValue(_ value: String?) -> String? {
        guard let value = value?.trimmingCharacters(in: .whitespacesAndNewlines), !value.isEmpty else { return nil }
        return value
    }
}

struct WrappingHStack: Layout {
    let spacing: CGFloat

    func sizeThatFits(
        proposal: ProposedViewSize,
        subviews: Subviews,
        cache: inout ()
    ) -> CGSize {
        let maxWidth = proposal.width ?? .greatestFiniteMagnitude
        let result = layout(subviews: subviews, maxWidth: maxWidth)
        return CGSize(width: result.width, height: result.height)
    }

    func placeSubviews(
        in bounds: CGRect,
        proposal: ProposedViewSize,
        subviews: Subviews,
        cache: inout ()
    ) {
        let result = layout(subviews: subviews, maxWidth: bounds.width)
        for item in result.items {
            subviews[item.index].place(
                at: CGPoint(x: bounds.minX + item.origin.x, y: bounds.minY + item.origin.y),
                anchor: .topLeading,
                proposal: ProposedViewSize(item.size)
            )
        }
    }

    private func layout(subviews: Subviews, maxWidth: CGFloat) -> (items: [Item], width: CGFloat, height: CGFloat) {
        var items: [Item] = []
        var x: CGFloat = 0
        var y: CGFloat = 0
        var rowHeight: CGFloat = 0
        var usedWidth: CGFloat = 0

        for index in subviews.indices {
            var size = subviews[index].sizeThatFits(ProposedViewSize(width: maxWidth, height: nil))
            size.width = min(size.width, maxWidth)
            if x > 0, x + size.width > maxWidth {
                x = 0
                y += rowHeight + spacing
                rowHeight = 0
            }
            items.append(Item(index: index, origin: CGPoint(x: x, y: y), size: size))
            usedWidth = max(usedWidth, x + size.width)
            rowHeight = max(rowHeight, size.height)
            x += size.width + spacing
        }
        return (items, usedWidth, items.isEmpty ? 0 : y + rowHeight)
    }

    private struct Item {
        let index: Int
        let origin: CGPoint
        let size: CGSize
    }
}

