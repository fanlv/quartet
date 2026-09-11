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
    case cursor
    case codebuddy

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
        case "cursor-agent", "cursor-agent acp": return .cursor
        case "codebuddy", "codebuddy --acp": return .codebuddy
        default: return nil
        }
    }
}

extension AgentUsageResponse {
    /// 请求成功但没带上快照：provider 侧属于“未开启”的正常状态（例如 CodeBuddy 的额度看板
    /// 需要用户自己配 PAT），不是错误，调用方应退回版本探测。
    func hasPayload(for provider: AgentUsageProvider) -> Bool {
        switch provider {
        case .codex: codex != nil
        case .claude: claude != nil
        case .antigravity: antigravity != nil
        case .kimi: kimi != nil
        case .qoder: qoder != nil
        case .cursor: cursor != nil
        case .codebuddy: codebuddy != nil
        }
    }

    /// 快照里自带的 CLI 版本号。
    func version(for provider: AgentUsageProvider) -> String? {
        let raw: String?
        switch provider {
        case .codex: raw = codex?.version
        case .claude: raw = claude?.version
        case .antigravity: raw = antigravity?.version
        case .kimi: raw = kimi?.version
        case .qoder: raw = qoder?.version
        case .cursor: raw = cursor?.version
        case .codebuddy: raw = codebuddy?.version
        }
        return AgentUsageFormat.trimmed(raw)
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

/// 用量窗口的短标签：聊天页用量条、“选择 Agent”弹窗和统计页共用同一套推导规则。
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
}

/// 用量数字的统一格式化入口。
enum AgentUsageFormat {
    static func credits(_ value: Double) -> String {
        value.rounded() == value ? String(Int64(value)) : String(format: "%.1f", value)
    }

    /// 人民币金额：整数保持干净（¥1400），小数保留两位（¥625.80）。
    static func cny(_ value: Double) -> String {
        value.rounded() == value ? "¥\(Int64(value))" : String(format: "¥%.2f", value)
    }

    /// 去掉首尾空白，空串按“没有值”处理。
    static func trimmed(_ value: String?) -> String? {
        guard let value = value?.trimmingCharacters(in: .whitespacesAndNewlines), !value.isEmpty else { return nil }
        return value
    }

    /// 接口给的套餐 ID（`personal_professional_trial`）转成可读标签（`Personal Professional Trial`）。
    static func plan(_ value: String?) -> String? {
        guard let value = trimmed(value) else { return nil }
        return value.split(separator: "_").map { $0.capitalized }.joined(separator: " ")
    }
}

/// “选择 Agent”弹窗里一行的用量摘要：`text` 是单行文本（有数据时是版本号，
/// 没有数据时是“正在读取 / 读取失败”），`badges` 是额度类信息，单独占一行并自动折行。
/// `isFailure` 时 `text` 按警示色渲染。
struct AgentUsageSummaryLine: Equatable, Sendable {
    let text: String?
    let badges: [QuartetUsageBadge]
    let isFailure: Bool
    /// 失败时的完整错误原文（请求方法、URL、状态码、响应正文），供行内错误入口原样展示并复制。
    let detail: String?

    init(text: String?, badges: [QuartetUsageBadge] = [], isFailure: Bool = false, detail: String? = nil) {
        self.text = text
        self.badges = badges
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
                )
            )
        case .claude:
            AgentUsageResponse(
                code: 0, type: provider.rawValue,
                claude: ClaudeAgentUsage(
                    planType: "max", rateLimitTier: "tier-1", version: "v2.1.202",
                    fiveHour: fiveHours, sevenDay: sevenDays, sevenDayOpus: nil,
                    weeklyScoped: nil, extraUsage: nil
                )
            )
        case .antigravity:
            AgentUsageResponse(
                code: 0, type: provider.rawValue,
                antigravity: AntigravityAgentUsage(
                    version: "v1.1.1", claudeWeekly: sevenDays, claude5h: fiveHours,
                    geminiWeekly: sevenDays, gemini5h: fiveHours
                )
            )
        case .kimi:
            AgentUsageResponse(
                code: 0, type: provider.rawValue,
                kimi: KimiAgentUsage(
                    version: "v0.1.0", parallelLimit: 4, weekly: sevenDays,
                    fiveHour: fiveHours,
                    total: AgentUsageWindow(
                        usedPercent: 41, limitWindowSeconds: 0,
                        resetAfterSeconds: 0, resetAt: 0
                    )
                )
            )
        case .qoder:
            AgentUsageResponse(
                code: 0, type: provider.rawValue,
                qoder: QoderAgentUsage(
                    version: "v1.0.48", planType: "personal_professional_trial",
                    unit: "credits", total: 1_000, used: 325, remaining: 675,
                    usedPercent: 32.5, expiresAt: now + 1_209_600, quotaExceeded: false
                )
            )
        case .cursor:
            AgentUsageResponse(
                code: 0, type: provider.rawValue,
                cursor: CursorAgentUsage(
                    version: "v2026.09.08", membershipType: "pro",
                    primaryWindow: sevenDays, secondaryWindow: fiveHours,
                    tertiaryWindow: nil, grokBotWindow: nil
                )
            )
        case .codebuddy:
            AgentUsageResponse(
                code: 0, type: provider.rawValue,
                codebuddy: CodeBuddyAgentUsage(
                    version: "v2.6.0", username: "developer", quotaText: "1400",
                    costText: "625.7965", quota: 1_400, cost: 625.7965,
                    remaining: 774.2035, usedPercent: 44.7
                )
            )
        }
    }

    /// 行内摘要：Agent 行直接传 `AgentSummary`，命令与显示名的取法全局一致。
    func summary(agent: AgentSummary) -> AgentUsageSummaryLine? {
        summary(command: agent.type, displayName: agent.displayName.isEmpty ? agent.type : agent.displayName)
    }

    /// 副标题内容。没有任何可显示内容（既没缓存也没在读）时返回 nil，行内不占位。
    func summary(command: String, displayName: String) -> AgentUsageSummaryLine? {
        guard let entry = entries[command] else { return nil }

        var version: String?
        var badges: [QuartetUsageBadge] = []
        if let provider = AgentUsageProvider.resolve(command: command, displayName: displayName),
           let usage = entry.usage {
            version = usage.version(for: provider)
            badges = Self.usageBadges(provider: provider, usage: usage)
        }
        // provider 没给版本号（未开启额度看板、或本身不带版本）时退回版本探测的结果。
        if version == nil { version = AgentUsageFormat.trimmed(entry.version) }

        if version == nil, badges.isEmpty {
            if entry.loading {
                return AgentUsageSummaryLine(text: "正在读取版本与用量…".localizedForApp)
            }
            if let failure = entry.failure {
                return AgentUsageSummaryLine(
                    text: String(
                        format: "版本与用量读取失败：%@".localizedForApp,
                        failure.summary
                    ),
                    isFailure: true,
                    detail: Self.failureDetail(command: command, failure: failure)
                )
            }
            return nil
        }

        // 有旧数据时刷新失败不清空：行尾已经有警示按钮和重试按钮，这里照常显示读到过的内容。
        return AgentUsageSummaryLine(
            text: version,
            badges: badges,
            detail: entry.failure.map { Self.failureDetail(command: command, failure: $0) }
        )
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
                    guard response.hasPayload(for: provider) else {
                        // 额度看板未开启（如 CodeBuddy 没配 PAT）：不是错误，但行里得留下本机 CLI 版本。
                        let version = try? await client.agentVersion(command: job.versionCommand).version
                        return ProbeResult(
                            job: job,
                            usage: response,
                            version: version?.trimmingCharacters(in: .whitespacesAndNewlines) ?? "",
                            failure: nil
                        )
                    }
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

    /// 每个 provider 的额度角标：先套餐、后额度，顺序与聊天页用量条、统计页一致。
    /// 版本号不在这里 —— 它跟着命令走在副标题那一行（“先版本号、再套餐 / 额度”的展示约定）。
    private static func usageBadges(provider: AgentUsageProvider, usage: AgentUsageResponse) -> [QuartetUsageBadge] {
        switch provider {
        case .codex:
            guard let value = usage.codex else { return [] }
            var badges: [QuartetUsageBadge] = []
            if let window = value.primaryWindow { badges.append(windowBadge(id: "codex.primary", window: window)) }
            if let window = value.secondaryWindow { badges.append(windowBadge(id: "codex.secondary", window: window)) }
            if value.resetCredits > 0 {
                badges.append(QuartetUsageBadge(
                    id: "codex.reset",
                    label: "重置".localizedForApp,
                    text: String(value.resetCredits)
                ))
            }
            return badges
        case .claude:
            guard let value = usage.claude else { return [] }
            var badges: [QuartetUsageBadge] = []
            if let plan = AgentUsageFormat.plan(value.planType) {
                badges.append(QuartetUsageBadge(id: "claude.plan", text: plan))
            }
            if let window = value.fiveHour { badges.append(windowBadge(id: "claude.5h", label: "5h", window: window)) }
            if let window = value.sevenDay { badges.append(windowBadge(id: "claude.7d", label: "7d", window: window)) }
            if let window = value.sevenDayOpus {
                badges.append(windowBadge(id: "claude.opus", label: "Opus", window: window))
            }
            for (index, window) in (value.weeklyScoped ?? []).enumerated() {
                badges.append(windowBadge(id: "claude.scoped.\(index)", label: window.label, window: window.window))
            }
            if let extra = value.extraUsage, extra.enabled {
                let currency = AgentUsageFormat.trimmed(extra.currency) ?? "USD"
                let used = extra.usedCredits.map(AgentUsageFormat.credits) ?? "—"
                let text = extra.monthlyLimit
                    .map { "\(used) / \(AgentUsageFormat.credits($0)) \(currency)" }
                    ?? "\(used) \(currency)"
                badges.append(QuartetUsageBadge(
                    id: "claude.extra",
                    label: "额外用量".localizedForApp,
                    text: text
                ))
            }
            return badges
        case .antigravity:
            guard let value = usage.antigravity else { return [] }
            // agy 的窗口不带 limit_window_seconds，5h / 7d 由 bucket 本身的语义写死。
            return [
                groupBadge(id: "agy.claude", name: "Claude", windows: [("5h", value.claude5h), ("7d", value.claudeWeekly)]),
                groupBadge(id: "agy.gemini", name: "Gemini", windows: [("5h", value.gemini5h), ("7d", value.geminiWeekly)])
            ].compactMap { $0 }
        case .kimi:
            guard let value = usage.kimi else { return [] }
            var badges: [QuartetUsageBadge] = []
            if let window = value.fiveHour { badges.append(windowBadge(id: "kimi.5h", window: window)) }
            if let window = value.weekly { badges.append(windowBadge(id: "kimi.weekly", window: window)) }
            if let window = value.total {
                badges.append(windowBadge(id: "kimi.total", label: "Σ", window: window))
            }
            return badges
        case .qoder:
            guard let value = usage.qoder else { return [] }
            var badges = [QuartetUsageBadge(
                id: "qoder.credits",
                label: value.unit?.lowercased() == "credits" ? "Credits" : "额度".localizedForApp,
                text: "\(AgentUsageFormat.credits(value.used)) / \(AgentUsageFormat.credits(value.total))",
                percent: value.usedPercent
            )]
            if value.quotaExceeded {
                badges.append(QuartetUsageBadge(id: "qoder.exceeded", text: "额度已用尽".localizedForApp, percent: 100))
            }
            return badges
        case .cursor:
            guard let value = usage.cursor else { return [] }
            var badges: [QuartetUsageBadge] = []
            if let plan = AgentUsageFormat.plan(value.membershipType) {
                badges.append(QuartetUsageBadge(id: "cursor.plan", text: plan))
            }
            let lanes: [(String, String, AgentUsageWindow?)] = [
                ("cursor.total", "总量".localizedForApp, value.primaryWindow),
                ("cursor.auto", "Auto", value.secondaryWindow),
                ("cursor.api", "API", value.tertiaryWindow),
                ("cursor.grok", "Grok", value.grokBotWindow)
            ]
            for (id, label, window) in lanes {
                guard let window else { continue }
                badges.append(windowBadge(id: id, label: label, window: window))
            }
            return badges
        case .codebuddy:
            guard let value = usage.codebuddy else { return [] }
            let cost = value.cost.map(AgentUsageFormat.cny) ?? AgentUsageFormat.trimmed(value.costText)
            let quota = value.quota.map(AgentUsageFormat.cny) ?? AgentUsageFormat.trimmed(value.quotaText)
            guard let cost else { return [] }
            // 额度是 “-” 这类特殊状态时只报已用金额，不拿它做百分比。
            return [QuartetUsageBadge(
                id: "codebuddy.month",
                label: "本月".localizedForApp,
                text: quota.map { "\(cost) / \($0)" } ?? cost,
                percent: value.usedPercent
            )]
        }
    }

    private static func windowBadge(id: String, label: String? = nil, window: AgentUsageWindow) -> QuartetUsageBadge {
        let resolved = label ?? (window.durationLabel.isEmpty ? "额度".localizedForApp : window.durationLabel)
        return QuartetUsageBadge(id: id, label: resolved, text: window.percentLabel, percent: window.usedPercent)
    }

    /// 同一个模型组的多个窗口合成一枚角标：组名只写一次，每个百分比各自着色。
    private static func groupBadge(
        id: String,
        name: String,
        windows: [(String, AgentUsageWindow?)]
    ) -> QuartetUsageBadge? {
        let items = windows.compactMap { label, window -> QuartetUsageBadge.Item? in
            guard let window else { return nil }
            return QuartetUsageBadge.Item("\(label) \(window.percentLabel)", percent: window.usedPercent)
        }
        return items.isEmpty ? nil : QuartetUsageBadge(id: id, label: name, items: items)
    }
}

extension QuartetChoice {
    /// 所有 Agent 选择器共用的一行：
    /// 副标题只补充标题里没有的信息（命令、不可用原因）加上 CLI 版本号，额度类信息另起一行，
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
            badges: usage?.badges ?? [],
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
            case .cursor:
                if let value = usage.cursor { cursorContent(value) }
            case .codebuddy:
                if let value = usage.codebuddy { codebuddyContent(value) }
            }
            if usage.version(for: provider) == nil, !version.isEmpty {
                versionLabel(version)
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

    /// Cursor 的一条通道。窗口为空（账号没有该通道）时整条不出现。
    private struct UsageLane: Identifiable {
        let id: String
        let label: String
        let window: AgentUsageWindow

        init?(id: String, label: String, window: AgentUsageWindow?) {
            guard let window else { return nil }
            self.id = id
            self.label = label
            self.window = window
        }
    }

    /// Cursor 的通道用词（总量 / Auto / API / Grok）塞不进 22pt 的圆环，改成横向胶囊：
    /// 名字和百分比都直接读得到，重置时间仍在点开的浮层里。
    private func cursorContent(_ value: CursorAgentUsage) -> some View {
        let lanes = [
            UsageLane(id: "total", label: "总量".localizedForApp, window: value.primaryWindow),
            UsageLane(id: "auto", label: "Auto", window: value.secondaryWindow),
            UsageLane(id: "api", label: "API", window: value.tertiaryWindow),
            UsageLane(id: "grok", label: "Grok", window: value.grokBotWindow)
        ].compactMap { $0 }
        return WrappingHStack(spacing: 4, rowAlignment: .center) {
            if let version = displayValue(value.version) { versionLabel(version) }
            if let plan = AgentUsageFormat.plan(value.membershipType) { planLabel(plan) }
            ForEach(lanes) { lane in
                lanePill(label: lane.label, window: lane.window)
            }
        }
    }

    private func codebuddyContent(_ value: CodeBuddyAgentUsage) -> some View {
        let cost = value.cost.map(AgentUsageFormat.cny) ?? displayValue(value.costText)
        let quota = value.quota.map(AgentUsageFormat.cny) ?? displayValue(value.quotaText)
        let color = value.usedPercent.map { usageColor($0) } ?? QuartetTheme.primaryText
        return Group {
            if let version = displayValue(value.version) { versionLabel(version) }
            if let cost {
                Button {
                    var lines: [String] = []
                    if let quota {
                        lines.append("\("本月已用".localizedForApp) \(cost) / \(quota)")
                    }
                    if let remaining = value.remaining {
                        lines.append("\("剩余".localizedForApp) \(AgentUsageFormat.cny(remaining))")
                    }
                    // 额度为 “-” 之类的特殊状态：说明清楚为什么只有已用金额、没有百分比。
                    if value.quota == nil {
                        lines.append("额度处于特殊状态，仅显示已用金额".localizedForApp)
                    }
                    if let username = displayValue(value.username) { lines.append(username) }
                    detail = AgentUsageDetail(title: "CodeBuddy · \("本月".localizedForApp)", lines: lines)
                } label: {
                    HStack(spacing: 4) {
                        Image(systemName: "creditcard")
                            .font(.chat(.detail, weight: .medium))
                        Text(cost)
                            .font(.chat(.detail, weight: .bold))
                            .monospacedDigit()
                            .foregroundStyle(color)
                        if let quota {
                            Text("/ \(quota)")
                                .monospacedDigit()
                                .foregroundStyle(QuartetTheme.secondaryText.opacity(0.75))
                        }
                        if let percent = value.usedPercent {
                            ZStack(alignment: .leading) {
                                Capsule().fill(QuartetTheme.divider)
                                Capsule()
                                    .fill(color)
                                    .frame(width: max(2, 38 * min(max(percent, 0), 100) / 100))
                            }
                            .frame(width: 38, height: 4)
                        }
                    }
                    .font(.chat(.detail))
                    .foregroundStyle(QuartetTheme.secondaryText)
                }
                .buttonStyle(.plain)
                .accessibilityLabel(
                    "CodeBuddy \("本月已用".localizedForApp) \(cost)" + (quota.map { " / \($0)" } ?? "")
                )
            }
        }
    }

    private func planLabel(_ value: String) -> some View {
        Text(value)
            .font(.chat(.compact, weight: .semibold))
            .foregroundStyle(QuartetTheme.secondaryText)
            .lineLimit(1)
            .padding(.horizontal, 7)
            .padding(.vertical, 3)
            .background(QuartetTheme.elevated, in: Capsule())
            .accessibilityLabel("\("套餐".localizedForApp) \(value)")
    }

    private func lanePill(label: String, window: AgentUsageWindow) -> some View {
        let color = usageColor(window.usedPercent)
        return Button {
            detail = AgentUsageDetail(title: "\(label) \(window.percentLabel)", lines: resetLines(window))
        } label: {
            HStack(spacing: 4) {
                Text(label)
                    .font(.chat(.compact, weight: .medium))
                    .foregroundStyle(QuartetTheme.secondaryText)
                Text(window.percentLabel)
                    .font(.chat(.compact, weight: .semibold))
                    .monospacedDigit()
                    .foregroundStyle(color)
            }
            .lineLimit(1)
            .padding(.horizontal, 7)
            .padding(.vertical, 3)
            .background(QuartetTheme.elevated, in: Capsule())
            .contentShape(Capsule())
        }
        .buttonStyle(.plain)
        .accessibilityLabel("\(label)，\("已用".localizedForApp) \(window.percentLabel)")
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
        usageRing(
            label: label,
            percent: window.usedPercent,
            color: usageColor(window.usedPercent),
            detail: AgentUsageDetail(
                title: "\(label) \(window.percentLabel)",
                lines: resetLines(window)
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
                // 额度看板未开启（如 CodeBuddy 没配 PAT）：接口正常但没有快照，退回版本探测。
                // 版本探测本身是补充信息，失败不该把这次成功的用量请求报成错误。
                if !response.hasPayload(for: provider) {
                    try? await refreshVersionOnly()
                }
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

    /// 浮层里的重置时间。窗口本身没有重置语义（累计额度池）时没有这一行。
    private func resetLines(_ window: AgentUsageWindow) -> [String] {
        guard window.resetAt > 0 || window.resetAfterSeconds > 0 else { return [] }
        return ["\(formatReset(window)) \("重置".localizedForApp)"]
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
