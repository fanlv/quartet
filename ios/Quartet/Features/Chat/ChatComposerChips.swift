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

    private var resolvedSystemIcon: String? {
        guard let icon else { return nil }
        return UIImage(systemName: icon) == nil ? "questionmark.square.dashed" : icon
    }

    var body: some View {
        HStack(spacing: 5) {
            if let icon = resolvedSystemIcon {
                Image(systemName: icon)
                    .id(icon)
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

    /// Only stable built-in IDs and declared historical commands participate.
    /// A custom display name such as "Claude Reviewer" must not expose the
    /// machine owner's Claude account quota on that custom Agent's row.
    static func resolve(command: String, displayName _: String) -> Self? {
        let normalized = command
            .split(whereSeparator: \.isWhitespace)
            .joined(separator: " ")
            .lowercased()
        switch normalized {
        case "antigravity", "agy", "antigravity-acp": return .antigravity
        case "codex", "codex-acp",
             "npx @agentclientprotocol/codex-acp",
             "npx @zed-industries/codex-acp": return .codex
        case "claude", "claude-agent-acp",
             "npx @agentclientprotocol/claude-agent-acp": return .claude
        case "qoderclicn", "qoderclicn --acp", "qwen", "qwen --acp": return .qoder
        case "kimi", "kimi acp": return .kimi
        default: return nil
        }
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

/// 用量窗口的短标签：聊天页用量条和“选择 Agent”弹窗副标题共用同一套推导规则。
extension AgentUsageWindow {
    /// 窗口长度（5h / 7d）。上游没给窗口长度（如 Kimi 的累计额度池）时返回空串，
    /// 由调用方决定怎么退化。
    var durationLabel: String {
        guard limitWindowSeconds > 0 else { return "" }
        if limitWindowSeconds % 86_400 == 0 { return "\(limitWindowSeconds / 86_400)d" }
        if limitWindowSeconds % 3_600 == 0 { return "\(limitWindowSeconds / 3_600)h" }
        if limitWindowSeconds % 60 == 0 { return "\(limitWindowSeconds / 60)m" }
        return "\(limitWindowSeconds)s"
    }

    var percentLabel: String { "\(Int(usedPercent.rounded()))%" }

    /// “窗口长度 + 已用百分比”，窗口长度未知时只留百分比。
    var usageLabel: String {
        let duration = durationLabel
        return duration.isEmpty ? percentLabel : "\(duration) \(percentLabel)"
    }
}

/// 用量数字的统一格式化入口。
enum AgentUsageFormat {
    static func credits(_ value: Double) -> String {
        value.rounded() == value ? String(Int64(value)) : String(format: "%.1f", value)
    }

    /// 去掉首尾空白，空串按“没有值”处理。
    static func trimmed(_ value: String?) -> String? {
        guard let value = value?.trimmingCharacters(in: .whitespacesAndNewlines), !value.isEmpty else { return nil }
        return value
    }
}

/// “选择 Agent”弹窗里一行的版本 + 用量副标题。`isFailure` 时整行按警示色渲染。
struct AgentUsageSummaryLine: Equatable, Sendable {
    let text: String
    let isFailure: Bool
    /// 失败时的完整错误原文（请求方法、URL、状态码、响应正文），供行内错误入口原样展示并复制。
    let detail: String?

    init(text: String, isFailure: Bool, detail: String? = nil) {
        self.text = text
        self.isFailure = isFailure
        self.detail = detail
    }
}

/// 一个待探测的 Agent。`command` 就是 ACP 启动命令（`AgentSummary.type`），也是缓存键。
struct AgentUsageProbeTarget: Sendable, Hashable {
    let command: String
    let displayName: String

    init(command: String, displayName: String) {
        self.command = command
        self.displayName = displayName.isEmpty ? command : displayName
    }

    /// 只探测可用的 Agent：不可用的行本身已经写着不可用原因，再探测一次只会白等超时。
    static func targets(_ agents: [AgentSummary]) -> [AgentUsageProbeTarget] {
        var seen: Set<String> = []
        var targets: [AgentUsageProbeTarget] = []
        for agent in agents where agent.available && !agent.type.isEmpty {
            guard seen.insert(agent.type).inserted else { continue }
            targets.append(AgentUsageProbeTarget(command: agent.type, displayName: agent.displayName))
        }
        return targets
    }
}

/// “选择 Agent”弹窗副标题（版本号 + Usage）的数据源。
///
/// 弹窗一次要显示所有 Agent，而版本号要 exec 一次 CLI、用量还可能在宿主机上拉起进程，
/// 所以策略是：先用聊天页用量条留下的本地缓存立即出字，再按 provider 去重、限并发刷新，
/// 同一个命令在 `refreshInterval` 内不重复探测。换服务地址时内存态整体作废。
@MainActor
final class AgentUsageSummaryStore: ObservableObject {
    /// 节流与内存缓存要跨弹窗多次打开复用，所以是全局单例。
    static let shared = AgentUsageSummaryStore()

    /// 单个 Agent 命令的探测结果。有 provider 的 Agent 版本号来自用量响应，
    /// 其余 Agent 只读版本接口。
    struct Entry: Sendable {
        var usage: AgentUsageResponse?
        var version = ""
        var loading = false
        var failure: APIError?
    }

    /// 一次探测：要么按 provider 拉用量（映射到同一 provider 的命令共用一次请求），
    /// 要么按命令读 CLI 版本。
    private struct ProbeJob: Sendable {
        let provider: AgentUsageProvider?
        let commands: [String]

        var versionCommand: String { commands[0] }
    }

    private struct ProbeResult: Sendable {
        let job: ProbeJob
        let usage: AgentUsageResponse?
        let version: String?
        let failure: APIError?
    }

    private static let refreshInterval: TimeInterval = 60
    /// 探测都落在宿主机上，放开并发会同时 fork 出一堆 CLI 进程。
    private static let maxConcurrentProbes = 3

    @Published private(set) var entries: [String: Entry] = [:]

    private var lastProbedAt: [String: Date] = [:]
    private var inFlight: Set<String> = []
    private var namespace = ""
    private var generation: UInt64 = 0

    /// 弹窗打开时调用：先把本地缓存补进内存，再刷新过期的命令。
    /// 请求失败不占用节流窗口，所以“重试”就是再调一次本方法。
    func load(agents: [AgentSummary], model: AppModel, force: Bool = false) async {
        await load(targets: AgentUsageProbeTarget.targets(agents), model: model, force: force)
    }

    func load(targets: [AgentUsageProbeTarget], model: AppModel, force: Bool = false) async {
        let requestNamespace = model.serverAddress
        if requestNamespace != namespace {
            namespace = requestNamespace
            generation &+= 1
            entries = [:]
            lastProbedAt = [:]
            inFlight = []
        }
        guard !targets.isEmpty else { return }
        let requestGeneration = generation
        if model.isRunningUITests {
            applyUITestStub(targets: targets)
            return
        }
        restoreCache(targets)
        let client: APIClient
        do {
            client = try model.apiClient()
        } catch {
            guard requestGeneration == generation, requestNamespace == namespace else { return }
            // 服务地址本身不可用：错误直接落到对应行上，用户能看到全文也能点重试。
            recordFailure(targets: targets, error: error)
            return
        }
        await refresh(
            targets: targets,
            namespace: requestNamespace,
            generation: requestGeneration,
            client: client,
            force: force
        )
    }

    private func refresh(
        targets: [AgentUsageProbeTarget],
        namespace: String,
        generation: UInt64,
        client: APIClient,
        force: Bool
    ) async {
        guard generation == self.generation, namespace == self.namespace else { return }

        let jobs = plannedJobs(targets, force: force)
        guard !jobs.isEmpty else { return }
        beginProbing(jobs)
        defer { endProbing(jobs, generation: generation, namespace: namespace) }
        await runProbes(jobs, client: client, generation: generation, namespace: namespace)
    }

    private func recordFailure(targets: [AgentUsageProbeTarget], error: Error) {
        let failure = (error as? APIError)
            ?? APIError(summary: "Agent 用量加载失败", detail: String(describing: error))
        for target in targets {
            var entry = entries[target.command] ?? Entry()
            entry.loading = false
            entry.failure = failure
            entries[target.command] = entry
            lastProbedAt[target.command] = nil
        }
    }

    /// UI 测试不打真实后端：为套餐型 Agent 塞入完整用量，其余 Agent 提供占位版本号。
    private func applyUITestStub(targets: [AgentUsageProbeTarget]) {
        for target in targets {
            guard let provider = AgentUsageProvider.resolve(
                command: target.command,
                displayName: target.displayName
            ) else {
                entries[target.command] = Entry(version: "v1.0.0")
                continue
            }
            let now = Int64(Date().timeIntervalSince1970)
            let fiveHours = AgentUsageWindow(
                usedPercent: 36, limitWindowSeconds: 18_000,
                resetAfterSeconds: 5_400, resetAt: now + 5_400
            )
            let sevenDays = AgentUsageWindow(
                usedPercent: 62, limitWindowSeconds: 604_800,
                resetAfterSeconds: 172_800, resetAt: now + 172_800
            )
            let response = Self.uiTestUsage(
                provider: provider,
                now: now,
                fiveHours: fiveHours,
                sevenDays: sevenDays
            )
            entries[target.command] = Entry(usage: response)
        }
    }

    private static func uiTestUsage(
        provider: AgentUsageProvider,
        now: Int64,
        fiveHours: AgentUsageWindow,
        sevenDays: AgentUsageWindow
    ) -> AgentUsageResponse {
        switch provider {
        case .codex:
            AgentUsageResponse(
                code: 0, type: provider.rawValue,
                codex: CodexAgentUsage(
                    email: "developer@example.com", planType: "plus", version: "v0.144.0",
                    primaryWindow: fiveHours, secondaryWindow: sevenDays, resetCredits: 2,
                    resetCreditExpiries: [now + 259_200, now + 604_800]
                ),
                claude: nil, antigravity: nil, kimi: nil, qoder: nil
            )
        case .claude:
            AgentUsageResponse(
                code: 0, type: provider.rawValue, codex: nil,
                claude: ClaudeAgentUsage(
                    planType: "max", rateLimitTier: "tier-1", version: "v2.1.202",
                    fiveHour: fiveHours, sevenDay: sevenDays, sevenDayOpus: nil,
                    weeklyScoped: nil, extraUsage: nil
                ),
                antigravity: nil, kimi: nil, qoder: nil
            )
        case .antigravity:
            AgentUsageResponse(
                code: 0, type: provider.rawValue, codex: nil, claude: nil,
                antigravity: AntigravityAgentUsage(
                    version: "v1.1.1", claudeWeekly: sevenDays, claude5h: fiveHours,
                    geminiWeekly: sevenDays, gemini5h: fiveHours
                ),
                kimi: nil, qoder: nil
            )
        case .kimi:
            AgentUsageResponse(
                code: 0, type: provider.rawValue, codex: nil, claude: nil, antigravity: nil,
                kimi: KimiAgentUsage(
                    version: "v0.1.0", parallelLimit: 4, weekly: sevenDays,
                    fiveHour: fiveHours,
                    total: AgentUsageWindow(
                        usedPercent: 41, limitWindowSeconds: 0,
                        resetAfterSeconds: 0, resetAt: 0
                    )
                ),
                qoder: nil
            )
        case .qoder:
            AgentUsageResponse(
                code: 0, type: provider.rawValue, codex: nil, claude: nil, antigravity: nil, kimi: nil,
                qoder: QoderAgentUsage(
                    version: "v1.0.48", planType: "personal_professional_trial",
                    unit: "credits", total: 1_000, used: 325, remaining: 675,
                    usedPercent: 32.5, expiresAt: now + 1_209_600, quotaExceeded: false
                )
            )
        }
    }

    /// 行内摘要：Agent 行直接传 `AgentSummary`，命令与显示名的取法全局一致。
    func summary(agent: AgentSummary) -> AgentUsageSummaryLine? {
        summary(command: agent.type, displayName: agent.displayName.isEmpty ? agent.type : agent.displayName)
    }

    /// 副标题文本。没有任何可显示内容（既没缓存也没在读）时返回 nil，行内不占位。
    func summary(command: String, displayName: String) -> AgentUsageSummaryLine? {
        guard let entry = entries[command] else { return nil }

        var parts: [String] = []
        if let provider = AgentUsageProvider.resolve(command: command, displayName: displayName),
           let usage = entry.usage {
            parts = Self.usageParts(provider: provider, usage: usage)
        } else if let version = AgentUsageFormat.trimmed(entry.version) {
            parts = [version]
        }

        if parts.isEmpty {
            if entry.loading {
                return AgentUsageSummaryLine(text: "Loading version & usage…", isFailure: false)
            }
            if let failure = entry.failure {
                return AgentUsageSummaryLine(
                    text: "Version & usage failed: \(failure.summary)",
                    isFailure: true,
                    detail: Self.failureDetail(command: command, failure: failure)
                )
            }
            return nil
        }

        // 有旧数据时刷新失败不清空，改成在行尾标一下，避免把已经读到的信息又抹掉。
        if let failure = entry.failure {
            parts.append("⚠︎ Refresh failed")
            return AgentUsageSummaryLine(
                text: parts.joined(separator: " · "),
                isFailure: false,
                detail: Self.failureDetail(command: command, failure: failure)
            )
        }
        return AgentUsageSummaryLine(text: parts.joined(separator: " · "), isFailure: false)
    }

    /// 行内只放一句摘要，完整错误原文交给错误详情弹窗，一个字都不裁。
    private static func failureDetail(command: String, failure: APIError) -> String {
        [command, failure.summary, "", failure.detail].joined(separator: "\n")
    }

    private func restoreCache(_ targets: [AgentUsageProbeTarget]) {
        for target in targets where entries[target.command] == nil {
            var entry = Entry()
            if let provider = AgentUsageProvider.resolve(command: target.command, displayName: target.displayName) {
                entry.usage = AgentUsageCache.usage(provider: provider, namespace: namespace)
            } else {
                entry.version = AgentUsageCache.version(command: target.command, namespace: namespace)
            }
            entries[target.command] = entry
        }
    }

    /// 按 provider（没有 provider 时按命令）分组，只保留还需要刷新的那几组。
    private func plannedJobs(_ targets: [AgentUsageProbeTarget], force: Bool) -> [ProbeJob] {
        var keys: [String] = []
        var providers: [String: AgentUsageProvider?] = [:]
        var commands: [String: [String]] = [:]

        for target in targets {
            let provider = AgentUsageProvider.resolve(command: target.command, displayName: target.displayName)
            let key = provider.map { "usage:\($0.rawValue)" } ?? "version:\(target.command)"
            if commands[key] == nil {
                keys.append(key)
                providers[key] = provider
                commands[key] = []
            }
            if !(commands[key] ?? []).contains(target.command) {
                commands[key]?.append(target.command)
            }
        }

        return keys.compactMap { key in
            guard let list = commands[key], !list.isEmpty else { return nil }
            guard !list.contains(where: inFlight.contains) else { return nil }
            guard force || list.contains(where: isDue) else { return nil }
            return ProbeJob(provider: providers[key] ?? nil, commands: list)
        }
    }

    private func isDue(_ command: String) -> Bool {
        guard let stamp = lastProbedAt[command] else { return true }
        return Date().timeIntervalSince(stamp) >= Self.refreshInterval
    }

    private func beginProbing(_ jobs: [ProbeJob]) {
        for command in jobs.flatMap(\.commands) {
            inFlight.insert(command)
            var entry = entries[command] ?? Entry()
            entry.loading = true
            // 重试期间先撤掉上一次的失败，行内立刻变成“正在读取”，避免重试看起来没反应。
            entry.failure = nil
            entries[command] = entry
        }
    }

    private func endProbing(_ jobs: [ProbeJob], generation: UInt64, namespace: String) {
        guard generation == self.generation, namespace == self.namespace else { return }
        for command in jobs.flatMap(\.commands) {
            inFlight.remove(command)
            guard var entry = entries[command] else { continue }
            entry.loading = false
            entries[command] = entry
        }
    }

    private func runProbes(
        _ jobs: [ProbeJob],
        client: APIClient,
        generation: UInt64,
        namespace: String
    ) async {
        var next = 0
        await withTaskGroup(of: ProbeResult.self) { group in
            while next < jobs.count, next < Self.maxConcurrentProbes {
                let job = jobs[next]
                group.addTask { await Self.probe(job: job, client: client) }
                next += 1
            }
            while let result = await group.next() {
                guard generation == self.generation, namespace == self.namespace else {
                    group.cancelAll()
                    return
                }
                apply(result, namespace: namespace)
                guard !Task.isCancelled, next < jobs.count else { continue }
                let job = jobs[next]
                group.addTask { await Self.probe(job: job, client: client) }
                next += 1
            }
        }
    }

    private nonisolated static func probe(job: ProbeJob, client: APIClient) async -> ProbeResult {
        do {
            if let provider = job.provider {
                do {
                    let response = try await client.agentUsage(provider: provider.rawValue)
                    return ProbeResult(job: job, usage: response, version: nil, failure: nil)
                } catch {
                    // Quota access can fail independently (expired OAuth, provider
                    // outage). Still ask the lightweight version endpoint so the
                    // Agent row keeps its useful local-version information.
                    let version = try? await client.agentVersion(command: job.versionCommand).version
                    let failure = (error as? APIError)
                        ?? APIError(summary: "Agent 用量加载失败", detail: String(describing: error))
                    return ProbeResult(job: job, usage: nil, version: version, failure: failure)
                }
            }
            let response = try await client.agentVersion(command: job.versionCommand)
            return ProbeResult(
                job: job,
                usage: nil,
                version: response.version?.trimmingCharacters(in: .whitespacesAndNewlines) ?? "",
                failure: nil
            )
        } catch let error as APIError {
            return ProbeResult(job: job, usage: nil, version: nil, failure: error)
        } catch {
            return ProbeResult(
                job: job,
                usage: nil,
                version: nil,
                failure: APIError(summary: "Agent 用量加载失败", detail: String(describing: error))
            )
        }
    }

    private func apply(_ result: ProbeResult, namespace: String) {
        for command in result.job.commands {
            var entry = entries[command] ?? Entry()
            entry.loading = false
            entry.failure = result.failure
            if let usage = result.usage { entry.usage = usage }
            if let version = result.version { entry.version = version }
            entries[command] = entry
            // 失败不占用节流窗口：下次打开弹窗仍然重试，成功了才开始计时。
            lastProbedAt[command] = result.failure == nil ? Date() : nil
        }

        // 成功结果写回和聊天页用量条共用的本地缓存。
        if let provider = result.job.provider, let usage = result.usage {
            AgentUsageCache.setUsage(usage, provider: provider, namespace: namespace)
        }
        if let version = result.version {
            AgentUsageCache.setVersion(version, command: result.job.versionCommand, namespace: namespace)
        }
    }

    /// 每个 provider 的摘要片段：版本号在前，后面按“压力最直观”的顺序排用量。
    /// 这一行是给选择器用的紧凑信息，标签统一用英文短词，不随 App 语言变。
    private static func usageParts(provider: AgentUsageProvider, usage: AgentUsageResponse) -> [String] {
        switch provider {
        case .codex:
            guard let value = usage.codex else { return [] }
            var parts = [AgentUsageFormat.trimmed(value.version)].compactMap { $0 }
            if let window = value.primaryWindow { parts.append(window.usageLabel) }
            if let window = value.secondaryWindow { parts.append(window.usageLabel) }
            if value.resetCredits > 0 {
                parts.append("Reset \(value.resetCredits)")
            }
            return parts
        case .claude:
            guard let value = usage.claude else { return [] }
            var parts = [AgentUsageFormat.trimmed(value.version)].compactMap { $0 }
            if let window = value.fiveHour { parts.append(window.usageLabel) }
            if let window = value.sevenDay { parts.append(window.usageLabel) }
            if let window = value.sevenDayOpus { parts.append("Opus \(window.percentLabel)") }
            for window in value.weeklyScoped ?? [] {
                parts.append("\(window.label) \(window.percentLabel)")
            }
            return parts
        case .antigravity:
            guard let value = usage.antigravity else { return [] }
            var parts = [AgentUsageFormat.trimmed(value.version)].compactMap { $0 }
            // agy 的窗口不带 limit_window_seconds，5h / 7d 由 bucket 本身的语义写死。
            let claude = [("5h", value.claude5h), ("7d", value.claudeWeekly)]
            let gemini = [("5h", value.gemini5h), ("7d", value.geminiWeekly)]
            if let group = Self.windowGroup("Claude", claude) { parts.append(group) }
            if let group = Self.windowGroup("Gemini", gemini) { parts.append(group) }
            return parts
        case .kimi:
            guard let value = usage.kimi else { return [] }
            var parts = [AgentUsageFormat.trimmed(value.version)].compactMap { $0 }
            if let window = value.fiveHour { parts.append(window.usageLabel) }
            if let window = value.weekly { parts.append(window.usageLabel) }
            if let window = value.total {
                parts.append("Sum \(window.percentLabel)")
            }
            return parts
        case .qoder:
            guard let value = usage.qoder else { return [] }
            var parts = [AgentUsageFormat.trimmed(value.version)].compactMap { $0 }
            parts.append(
                "Used \(AgentUsageFormat.credits(value.used))"
                    + "/\(AgentUsageFormat.credits(value.total))"
            )
            if value.quotaExceeded { parts.append("Quota exhausted") }
            return parts
        }
    }

    private static func windowGroup(_ mark: String, _ windows: [(String, AgentUsageWindow?)]) -> String? {
        let labels = windows.compactMap { item -> String? in
            guard let window = item.1 else { return nil }
            return "\(item.0) \(window.percentLabel)"
        }
        return labels.isEmpty ? nil : "\(mark) \(labels.joined(separator: " / "))"
    }
}

extension QuartetChoice {
    /// 所有 Agent 选择器共用的一行：
    /// 副标题只补充标题里没有的信息（命令、不可用原因），footnote 挂版本号与用量，
    /// 读取失败时行尾出现错误详情和重试入口。
    static func agent(
        id: String,
        title: String,
        command: String? = nil,
        note: String? = nil,
        disabled: Bool = false,
        usage: AgentUsageSummaryLine?,
        retry: @escaping () -> Void
    ) -> QuartetChoice {
        let trimmedTitle = title.trimmingCharacters(in: .whitespacesAndNewlines)
        let details = [command, note]
            .compactMap { $0?.trimmingCharacters(in: .whitespacesAndNewlines) }
            .filter { !$0.isEmpty && $0 != trimmedTitle }
        return QuartetChoice(
            id: id,
            title: title,
            detail: details.isEmpty ? nil : details.joined(separator: " · "),
            footnote: usage?.text,
            footnoteIsFailure: usage?.isFailure ?? false,
            footnoteDetail: usage?.detail,
            // 只有读失败的行才需要重试，正常行不摆多余按钮。
            footnoteRetry: usage?.detail == nil ? nil : retry,
            disabled: disabled
        )
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
                                .font(.chat(.detail, weight: .semibold))
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
                                .font(.chat(.detail, weight: .semibold))
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
            if usageVersion(provider: provider, usage: usage) == nil, !version.isEmpty {
                versionLabel(version)
            }
        } else if !version.isEmpty {
            versionLabel(version)
        }
    }

    private func usageVersion(provider: AgentUsageProvider, usage: AgentUsageResponse) -> String? {
        switch provider {
        case .codex: return displayValue(usage.codex?.version)
        case .claude: return displayValue(usage.claude?.version)
        case .antigravity: return displayValue(usage.antigravity?.version)
        case .kimi: return displayValue(usage.kimi?.version)
        case .qoder: return displayValue(usage.qoder?.version)
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
            if let window = value.fiveHour { usageRing(label: "5h", window: window) }
            if let window = value.sevenDay { usageRing(label: "7d", window: window) }
            if let window = value.sevenDayOpus { usageRing(label: "Opus", window: window) }
            ForEach(Array((value.weeklyScoped ?? []).enumerated()), id: \.offset) { _, window in
                usageRing(label: window.label, window: window.window)
            }
        }
    }

    private func antigravityContent(_ value: AntigravityAgentUsage) -> some View {
        HStack(spacing: 5) {
            if let version = displayValue(value.version) { versionLabel(version) }
            HStack(spacing: 3) {
                if value.claude5h != nil || value.claudeWeekly != nil {
                    antigravityQuotaGroup(
                        name: "Claude",
                        windows: [("5h", value.claude5h), ("7d", value.claudeWeekly)]
                    )
                }
                if value.gemini5h != nil || value.geminiWeekly != nil {
                    antigravityQuotaGroup(
                        name: "Gemini",
                        windows: [("5h", value.gemini5h), ("7d", value.geminiWeekly)]
                    )
                }
            }
        }
        .fixedSize(horizontal: true, vertical: true)
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
                        title: "累计额度 \(window.percentLabel)",
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
                        .font(.chat(.detail, weight: .medium))
                    Text(credits(value.used))
                        .font(.chat(.detail, weight: .bold))
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

    private func antigravityQuotaGroup(
        name: String,
        windows: [(String, AgentUsageWindow?)]
    ) -> some View {
        let visibleWindows = windows.compactMap { label, window in
            window.map { (label, $0) }
        }
        let usedLabel = "已用".localizedForApp
        let detailLines = visibleWindows.map { label, window in
            "\(label)  \(window.percentLabel) · \(formatReset(window)) \("重置".localizedForApp)"
        }
        let accessibilityValue = visibleWindows
            .map { "\($0.0) \($0.1.percentLabel)" }
            .joined(separator: "，")

        return Button {
            detail = AgentUsageDetail(
                title: "\(name) · \(usedLabel)",
                lines: detailLines
            )
        } label: {
            VStack(alignment: .leading, spacing: 2) {
                HStack(alignment: .firstTextBaseline, spacing: 5) {
                    Text(name)
                        .font(.chat(.compact, weight: .semibold))
                        .foregroundStyle(QuartetTheme.primaryText)
                    Spacer(minLength: 2)
                    Text(usedLabel)
                        .font(.chat(.compact, weight: .medium))
                        .foregroundStyle(QuartetTheme.secondaryText.opacity(0.72))
                }

                ForEach(Array(visibleWindows.enumerated()), id: \.offset) { _, item in
                    antigravityWindowRow(label: item.0, window: item.1)
                }
            }
            .padding(.horizontal, 6)
            .padding(.vertical, 4)
            .frame(width: 90, alignment: .leading)
            .background(QuartetTheme.elevated.opacity(0.72), in: RoundedRectangle(cornerRadius: 8, style: .continuous))
            .overlay(
                RoundedRectangle(cornerRadius: 8, style: .continuous)
                    .stroke(QuartetTheme.divider, lineWidth: 1)
            )
            .contentShape(RoundedRectangle(cornerRadius: 8, style: .continuous))
        }
        .frame(width: 90)
        .frame(minHeight: 44)
        .buttonStyle(.plain)
        .accessibilityElement(children: .ignore)
        .accessibilityLabel("\(name)，\(usedLabel)，\(accessibilityValue)")
        .accessibilityHint("查看重置时间".localizedForApp)
    }

    private func antigravityWindowRow(label: String, window: AgentUsageWindow) -> some View {
        let color = usageColor(window.usedPercent)
        return HStack(spacing: 4) {
            Text(label)
                .font(.chat(.compact, weight: .medium, design: .monospaced))
                .foregroundStyle(QuartetTheme.secondaryText.opacity(0.78))
                .frame(width: 17, alignment: .leading)
            Text(window.percentLabel)
                .font(.chat(.detail, weight: .semibold, design: .monospaced))
                .foregroundStyle(color)
                .frame(width: 27, alignment: .trailing)
            ZStack(alignment: .leading) {
                Capsule().fill(QuartetTheme.divider)
                Capsule()
                    .fill(color)
                    .frame(
                        width: max(2, 23 * min(max(window.usedPercent, 0), 100) / 100)
                    )
            }
            .frame(width: 23, height: 3)
        }
    }

    private func usageRing(label: String, window: AgentUsageWindow) -> some View {
        let resetLines = window.resetAt > 0 || window.resetAfterSeconds > 0
            ? ["\(formatReset(window)) \("重置".localizedForApp)"]
            : []
        return usageRing(
            label: label,
            percent: window.usedPercent,
            color: usageColor(window.usedPercent),
            detail: AgentUsageDetail(
                title: "\(label) \(window.percentLabel)",
                lines: resetLines
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
                    .font(.chat(.compact, weight: .semibold, design: .monospaced))
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
        version = AgentUsageCache.version(command: command, namespace: appModel.serverAddress)
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
            if provider != nil {
                do {
                    try await refreshVersionOnly()
                } catch is CancellationError {
                    return
                } catch {
                    // The quota error remains the primary failure shown to the
                    // user; the best-effort version fallback is supplementary.
                }
            }
            requestError = error
        } catch {
            if provider != nil {
                do {
                    try await refreshVersionOnly()
                } catch is CancellationError {
                    return
                } catch {
                    // Keep the original quota error below.
                }
            }
            requestError = APIError(summary: "Agent 用量加载失败", detail: String(describing: error))
        }
    }

    private func refreshVersionOnly() async throws {
        let response = try await appModel.apiClient().agentVersion(command: command)
        try Task.checkCancellation()
        version = response.version?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        AgentUsageCache.setVersion(version, command: command, namespace: appModel.serverAddress)
    }

    private func usageColor(_ percent: Double) -> Color {
        if percent >= 80 { return QuartetTheme.failed }
        if percent >= 50 { return QuartetTheme.warning }
        return QuartetTheme.success
    }

    private func windowLabel(_ window: AgentUsageWindow) -> String {
        let label = window.durationLabel
        return label.isEmpty ? "\(max(0, window.limitWindowSeconds))s" : label
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

    private func credits(_ value: Double) -> String { AgentUsageFormat.credits(value) }

    private func displayValue(_ value: String?) -> String? { AgentUsageFormat.trimmed(value) }
}

struct WrappingHStack: Layout {
    enum RowAlignment: Equatable {
        case top
        case center
    }

    let spacing: CGFloat
    var rowAlignment: RowAlignment = .top

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
        var rowStartIndex = 0
        var usedWidth: CGFloat = 0

        for index in subviews.indices {
            var size = subviews[index].sizeThatFits(ProposedViewSize(width: maxWidth, height: nil))
            size.width = min(size.width, maxWidth)
            if x > 0, x + size.width > maxWidth {
                alignRowItems(&items, from: rowStartIndex, rowHeight: rowHeight)
                x = 0
                y += rowHeight + spacing
                rowHeight = 0
                rowStartIndex = items.count
            }
            items.append(Item(index: index, origin: CGPoint(x: x, y: y), size: size))
            usedWidth = max(usedWidth, x + size.width)
            rowHeight = max(rowHeight, size.height)
            x += size.width + spacing
        }
        alignRowItems(&items, from: rowStartIndex, rowHeight: rowHeight)
        return (items, usedWidth, items.isEmpty ? 0 : y + rowHeight)
    }

    private func alignRowItems(_ items: inout [Item], from startIndex: Int, rowHeight: CGFloat) {
        guard rowAlignment == .center else { return }
        for index in startIndex..<items.count {
            items[index].origin.y += (rowHeight - items[index].size.height) / 2
        }
    }

    private struct Item {
        let index: Int
        var origin: CGPoint
        let size: CGSize
    }
}
