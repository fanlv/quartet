import Foundation

struct HealthResponse: Decodable, Equatable, Sendable {
    let status: String
    let time: String?
    let buildTime: String?
    let instanceId: String?
    let authState: String
    let authError: String?
}

struct AuthUser: Decodable, Sendable {
    let id: String
    let username: String
    let displayName: String
    let roleIds: [String]
    let status: String
    let mustChangePassword: Bool
}

struct AuthPrincipal: Decodable, Sendable {
    let user: AuthUser
    let permissions: [String]
    let csrfToken: String
}

struct LoginRequest: Encodable, Sendable {
    let username: String
    let password: String
}

struct WebRestartResponse: Decodable, Sendable {
    let code: Int
    let msg: String?
    let logPath: String?

    enum CodingKeys: String, CodingKey {
        case code
        case msg
        case logPath = "log_path"
    }
}

struct UsageStatsReport: Decodable, Sendable {
    let range: UsageStatsRange
    let byWorkspace: [UsageStatsWorkspaceRow]
    let byModel: [UsageStatsModelRow]
    let byTool: [UsageStatsToolRow]
    let daily: [UsageStatsDailyRow]
    let previous: UsageStatsPreviousTotals?
    let note: String
    let failed: Bool?
    let error: String?

    var hasData: Bool {
        !byWorkspace.isEmpty || !byModel.isEmpty || !byTool.isEmpty || !daily.isEmpty
    }
}

struct UsageStatsRange: Decodable, Hashable, Sendable {
    let from: String
    let to: String
}

struct UsageStatsTokenTotals: Decodable, Hashable, Sendable {
    let total: Int
    let assistant: Int
    let thought: Int
    let toolCall: Int
}

protocol UsageStatsTotals {
    var totalMs: Int64 { get }
    var turnCount: Int { get }
    var assistantCount: Int { get }
    var thoughtCount: Int { get }
    var toolCallCount: Int { get }
    var tokens: UsageStatsTokenTotals { get }
}

struct UsageStatsSectionTotals: Decodable, Hashable, Sendable, UsageStatsTotals {
    let totalMs: Int64
    let turnCount: Int
    let assistantCount: Int
    let thoughtCount: Int
    let toolCallCount: Int
    let tokens: UsageStatsTokenTotals
}

struct UsageStatsWorkspaceRow: Decodable, Identifiable, Hashable, Sendable, UsageStatsTotals {
    let workspaceId: String
    let workspaceName: String?
    let deleted: Bool?
    let totalMs: Int64
    let turnCount: Int
    let assistantCount: Int
    let thoughtCount: Int
    let toolCallCount: Int
    let tokens: UsageStatsTokenTotals

    var id: String { workspaceId }
}

struct UsageStatsModelRow: Decodable, Identifiable, Hashable, Sendable, UsageStatsTotals {
    let modelId: String
    let modelName: String?
    let totalMs: Int64
    let turnCount: Int
    let assistantCount: Int
    let thoughtCount: Int
    let toolCallCount: Int
    let tokens: UsageStatsTokenTotals

    var id: String { modelId }
}

struct UsageStatsToolRow: Decodable, Identifiable, Hashable, Sendable {
    let toolKey: String
    let count: Int
    let totalMs: Int64

    var id: String { toolKey }
}

struct UsageStatsDailyRow: Decodable, Identifiable, Hashable, Sendable, UsageStatsTotals {
    let date: String
    let totalMs: Int64
    let turnCount: Int
    let assistantCount: Int
    let thoughtCount: Int
    let toolCallCount: Int
    let tokens: UsageStatsTokenTotals
    let models: [String: UsageStatsSectionTotals]?
    let modelNames: [String: String]?

    var id: String { date }
}

struct UsageStatsPreviousTotals: Decodable, Hashable, Sendable {
    let totalMs: Int64
    let turnCount: Int
    let toolCallCount: Int
    let tokensTotal: Int
    let workspaceCount: Int
}

struct WorkspaceSummary: Codable, Identifiable, Hashable, Sendable {
    let id: String
    var version: UInt64
    var title: String
    var description: String
    var workdir: String
    var defaultAgent: String?
    var defaultModel: String?
    var color: String?
    var favorite: Bool
    var sortOrder: Int
    var createdAt: Int64
    var updatedAt: Int64

    var displayName: String {
        title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? id : title
    }
}

struct WorkspacesResponse: Decodable, Sendable {
    let workspaces: [WorkspaceSummary]
}

struct GitBranchResponse: Decodable, Sendable {
    let code: Int
    let branch: String
}

struct UpdateWorkspaceRequest: Encodable, Sendable {
    let expectedVersion: UInt64
    let defaultAgent: String
    let defaultModel: String
}

struct AgentSummary: Decodable, Identifiable, Hashable, Sendable {
    let agentId: String
    let type: String
    let modelId: String
    let displayName: String
    let availability: String?
    let available: Bool
    let refreshing: Bool?
    let error: String?
    let models: AgentModelState?
    let modes: AgentModeState?
    let thoughtLevels: AgentThoughtLevelState?

    var id: String { agentId }
    var isValidationPending: Bool {
        availability == "pending_validation" || availability == "validating" || refreshing == true
    }

    var availabilityLabel: String {
        switch availability {
        case "pending_validation": "等待验证"
        case "validating": "正在验证"
        case "available": "可用"
        case "unavailable": "不可用"
        default: available ? "可用" : "状态未知"
        }
    }

    enum CodingKeys: String, CodingKey {
        case agentId = "agent_id"
        case type
        case modelId = "model_id"
        case displayName = "display_name"
        case availability
        case available
        case refreshing
        case error
        case models
        case modes
        case thoughtLevels
    }
}

struct AgentModelState: Decodable, Hashable, Sendable {
    let availableModels: [AgentModel]
    let currentModelId: String
}

struct AgentModel: Decodable, Identifiable, Hashable, Sendable {
    let modelId: String
    let name: String
    let description: String?
    var id: String { modelId }
}

struct AgentModeState: Decodable, Hashable, Sendable {
    let availableModes: [AgentOption]
    let currentModeId: String
}

struct AgentThoughtLevelState: Decodable, Hashable, Sendable {
    let availableThoughtLevels: [AgentOption]
    let currentThoughtLevelId: String
}

enum ACPConfigTarget: String, Encodable, Equatable, Sendable {
    case model
    case mode
    case thoughtLevel
}

struct SetACPConfigRequest: Encodable, Sendable {
    let target: ACPConfigTarget
    let sessionId: String?
    let agentType: String?
    let model: String?
    let mode: String?
    let thoughtLevel: String?

    init(
        target: ACPConfigTarget,
        sessionId: String? = nil,
        agentType: String? = nil,
        model: String? = nil,
        mode: String? = nil,
        thoughtLevel: String? = nil
    ) {
        self.target = target
        self.sessionId = sessionId
        self.agentType = agentType
        self.model = model
        self.mode = mode
        self.thoughtLevel = thoughtLevel
    }
}

struct SetACPConfigResponse: Decodable, Sendable {
    let code: Int
    let models: AgentModelState?
    let modes: AgentModeState?
    let thoughtLevels: AgentThoughtLevelState?
}

struct AgentOption: Decodable, Identifiable, Hashable, Sendable {
    let id: String
    let name: String
    let description: String?
}

enum AgentConfigurationDisplay {
    static func modelName(
        _ modelID: String?,
        agentReference: String?,
        agents: [AgentSummary]
    ) -> String? {
        guard let modelID = displayValue(modelID) else { return nil }
        let candidates = matchingAgents(agentReference, in: agents)
        let name = candidates
            .lazy
            .compactMap { agent in
                agent.models?.availableModels.first(where: { $0.modelId == modelID })?.name
            }
            .compactMap { displayValue($0) }
            .first
        return name ?? modelID
    }

    static func modeName(
        _ modeID: String?,
        agentReference: String?,
        agents: [AgentSummary]
    ) -> String? {
        optionName(
            modeID,
            agentReference: agentReference,
            agents: agents,
            options: { $0.modes?.availableModes ?? [] }
        )
    }

    static func thoughtLevelName(
        _ thoughtLevelID: String?,
        agentReference: String?,
        agents: [AgentSummary]
    ) -> String? {
        optionName(
            thoughtLevelID,
            agentReference: agentReference,
            agents: agents,
            options: { $0.thoughtLevels?.availableThoughtLevels ?? [] }
        )
    }

    private static func optionName(
        _ optionID: String?,
        agentReference: String?,
        agents: [AgentSummary],
        options: @escaping (AgentSummary) -> [AgentOption]
    ) -> String? {
        guard let optionID = displayValue(optionID) else { return nil }
        let name = matchingAgents(agentReference, in: agents)
            .lazy
            .compactMap { agent in
                options(agent).first(where: { $0.id == optionID })?.name
            }
            .compactMap { displayValue($0) }
            .first
        return name ?? optionID
    }

    private static func matchingAgents(
        _ reference: String?,
        in agents: [AgentSummary]
    ) -> [AgentSummary] {
        guard let reference = displayValue(reference) else { return agents }
        return agents.filter { $0.agentId == reference || $0.type == reference }
    }

    private static func displayValue(_ value: String?) -> String? {
        guard let trimmed = value?.trimmingCharacters(in: .whitespacesAndNewlines),
              !trimmed.isEmpty else { return nil }
        return trimmed
    }
}

struct AgentListResponse: Decodable, Sendable {
    let code: Int
    let agentList: [AgentSummary]
    let workdir: String
    let jobEnable: Bool

    enum CodingKeys: String, CodingKey {
        case code
        case agentList = "agent_list"
        case workdir
        case jobEnable = "job_enable"
    }
}

struct AgentUsageResponse: Codable, Hashable, Sendable {
    let code: Int
    let type: String
    let codex: CodexAgentUsage?
    let claude: ClaudeAgentUsage?
    let antigravity: AntigravityAgentUsage?
    let kimi: KimiAgentUsage?
    let qoder: QoderAgentUsage?
}

struct AgentUsageWindow: Codable, Hashable, Sendable {
    let usedPercent: Double
    let limitWindowSeconds: Int64
    let resetAfterSeconds: Int64
    let resetAt: Int64

    enum CodingKeys: String, CodingKey {
        case usedPercent = "used_percent"
        case limitWindowSeconds = "limit_window_seconds"
        case resetAfterSeconds = "reset_after_seconds"
        case resetAt = "reset_at"
    }
}

struct CodexAgentUsage: Codable, Hashable, Sendable {
    let email: String?
    let planType: String?
    let version: String?
    let primaryWindow: AgentUsageWindow?
    let secondaryWindow: AgentUsageWindow?
    let resetCredits: Int
    let resetCreditExpiries: [Int64]?

    enum CodingKeys: String, CodingKey {
        case email
        case planType = "plan_type"
        case version
        case primaryWindow = "primary_window"
        case secondaryWindow = "secondary_window"
        case resetCredits = "reset_credits"
        case resetCreditExpiries = "reset_credit_expiries"
    }
}

struct ClaudeAgentUsage: Codable, Hashable, Sendable {
    let name: String?
    let keySuffix: String?
    let version: String?
    let todayCost: Double
    let totalCost: Double

    enum CodingKeys: String, CodingKey {
        case name
        case keySuffix = "key_suffix"
        case version
        case todayCost = "today_cost"
        case totalCost = "total_cost"
    }
}

struct AntigravityAgentUsage: Codable, Hashable, Sendable {
    let version: String?
    let claudeWeekly: AgentUsageWindow?
    let claude5h: AgentUsageWindow?
    let geminiWeekly: AgentUsageWindow?
    let gemini5h: AgentUsageWindow?

    enum CodingKeys: String, CodingKey {
        case version
        case claudeWeekly = "claude_weekly"
        case claude5h = "claude_5h"
        case geminiWeekly = "gemini_weekly"
        case gemini5h = "gemini_5h"
    }
}

struct KimiAgentUsage: Codable, Hashable, Sendable {
    let version: String?
    let parallelLimit: Int64?
    let weekly: AgentUsageWindow?
    let fiveHour: AgentUsageWindow?
    let total: AgentUsageWindow?

    enum CodingKeys: String, CodingKey {
        case version
        case parallelLimit = "parallel_limit"
        case weekly
        case fiveHour = "five_hour"
        case total
    }
}

struct QoderAgentUsage: Codable, Hashable, Sendable {
    let version: String?
    let planType: String?
    let unit: String?
    let total: Double
    let used: Double
    let remaining: Double
    let usedPercent: Double
    let expiresAt: Int64?
    let quotaExceeded: Bool

    enum CodingKeys: String, CodingKey {
        case version
        case planType = "plan_type"
        case unit
        case total
        case used
        case remaining
        case usedPercent = "used_percent"
        case expiresAt = "expires_at"
        case quotaExceeded = "quota_exceeded"
    }
}

struct AgentVersionResponse: Codable, Hashable, Sendable {
    let code: Int
    let version: String?
}

struct AgentPreferences: Decodable, Hashable, Sendable {
    let favoriteModelIDs: [String]?
    let defaultModelID: String?
    let defaultMode: String?
    let defaultThoughtLevel: String?

    enum CodingKeys: String, CodingKey {
        case favoriteModelIDs = "favorite_model_ids"
        case defaultModelID = "default_model_id"
        case defaultMode = "default_mode"
        case defaultThoughtLevel = "default_thought_level"
    }
}

struct AgentPreferencesSettings: Decodable, Sendable {
    let agentPreferences: [String: AgentPreferences]?

    enum CodingKeys: String, CodingKey {
        case agentPreferences = "agent_prefs"
    }
}

struct AgentPreferencesResponse: Decodable, Sendable {
    let code: Int
    let settings: AgentPreferencesSettings?
}

struct MessagePreset: Decodable, Identifiable, Hashable, Sendable {
    let id: String
    let name: String?
    let content: String
}

struct MessagePresetLoadError: Decodable, Hashable, Sendable {
    let scope: String
    let file: String
    let error: String
}

struct EffectiveMessagePresetsResponse: Decodable, Sendable {
    let code: Int
    let workspaceId: String
    let project: [MessagePreset]
    let global: [MessagePreset]
    let errors: [MessagePresetLoadError]?
}

struct JobSummary: Decodable, Identifiable, Hashable, Sendable {
    let id: String
    let title: String
    let modelId: String?
    let agentId: String?
    let acpMode: String?
    let acpThoughtLevel: String?
    let status: String
    let mode: String?
    let workspaceId: String?
    let workdir: String?
    let createdAt: Int64
    let updatedAt: Int64
    let pinnedAt: Int64?
    let sessionCount: Int
    let scheduleId: String?
    let shareToken: String?

    init(
        id: String,
        title: String,
        modelId: String?,
        status: String,
        mode: String?,
        workspaceId: String?,
        workdir: String?,
        createdAt: Int64,
        updatedAt: Int64,
        pinnedAt: Int64?,
        sessionCount: Int,
        scheduleId: String?,
        shareToken: String?,
        agentId: String? = nil,
        acpMode: String? = nil,
        acpThoughtLevel: String? = nil
    ) {
        self.id = id
        self.title = title
        self.modelId = modelId
        self.agentId = agentId
        self.acpMode = acpMode
        self.acpThoughtLevel = acpThoughtLevel
        self.status = status
        self.mode = mode
        self.workspaceId = workspaceId
        self.workdir = workdir
        self.createdAt = createdAt
        self.updatedAt = updatedAt
        self.pinnedAt = pinnedAt
        self.sessionCount = sessionCount
        self.scheduleId = scheduleId
        self.shareToken = shareToken
    }

    private enum CodingKeys: String, CodingKey {
        case id, title, modelId, agentId, acpMode, acpThoughtLevel, status, mode
        case workspaceId, workdir, createdAt, updatedAt, pinnedAt, sessionCount
        case scheduleId, shareToken
    }

    init(from decoder: Decoder) throws {
        let values = try decoder.container(keyedBy: CodingKeys.self)
        id = try values.decode(String.self, forKey: .id)
        title = try values.decode(String.self, forKey: .title)
        modelId = try values.decodeIfPresent(String.self, forKey: .modelId)
        agentId = try values.decodeIfPresent(String.self, forKey: .agentId)
        acpMode = try values.decodeIfPresent(String.self, forKey: .acpMode)
        acpThoughtLevel = try values.decodeIfPresent(String.self, forKey: .acpThoughtLevel)
        status = try values.decode(String.self, forKey: .status)
        mode = try values.decodeIfPresent(String.self, forKey: .mode)
        workspaceId = try values.decodeIfPresent(String.self, forKey: .workspaceId)
        workdir = try values.decodeIfPresent(String.self, forKey: .workdir)
        createdAt = try values.decode(Int64.self, forKey: .createdAt)
        updatedAt = try values.decode(Int64.self, forKey: .updatedAt)
        pinnedAt = try values.decodeIfPresent(Int64.self, forKey: .pinnedAt)
        sessionCount = try values.decodeIfPresent(Int.self, forKey: .sessionCount) ?? 0
        scheduleId = try values.decodeIfPresent(String.self, forKey: .scheduleId)
        shareToken = try values.decodeIfPresent(String.self, forKey: .shareToken)
    }

    var displayTitle: String {
        title.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty ? "未命名 Job" : title
    }

    var isActive: Bool {
        status == "running" || status == "pending"
    }

    var statusLabel: String {
        switch status {
        case "pending": "等待中"
        case "running": "运行中"
        case "completed": "已完成"
        case "failed": "失败"
        case "stopped": "已停止"
        default: status
        }
    }

    var modeLabel: String {
        switch mode {
        case "graph": "GRAPH"
        case "loop": "LOOP"
        default: "CHAT"
        }
    }

    func updating(status: String? = nil, updatedAt: Int64? = nil) -> JobSummary {
        JobSummary(
            id: id,
            title: title,
            modelId: modelId,
            status: status ?? self.status,
            mode: mode,
            workspaceId: workspaceId,
            workdir: workdir,
            createdAt: createdAt,
            updatedAt: updatedAt ?? self.updatedAt,
            pinnedAt: pinnedAt,
            sessionCount: sessionCount,
            scheduleId: scheduleId,
            shareToken: shareToken,
            agentId: agentId,
            acpMode: acpMode,
            acpThoughtLevel: acpThoughtLevel
        )
    }
}

struct JobsPage: Decodable, Sendable {
    let jobs: [JobSummary]
    let nextCursor: String?
    let hasMore: Bool
    let version: Int64?
}

struct JobDetail: Decodable, Identifiable, Sendable {
    let id: String
    let title: String
    let status: String
    let mode: String
    let workspaceId: String
    let workdir: String?
    let scheduleId: String?
    let createdAt: String
    let updatedAt: String
    let startedAt: Int64?
    let finishedAt: Int64?
    let graphRunId: String?
    let sessionIds: [String]?
    let graphSessionIds: [String]?
    let progress: JobRunProgress?
    let lastRunOutcome: String?
    let firstModelId: String?
    let initialAgentId: String?
    let initialAcpMode: String?
    let initialAcpThoughtLevel: String?
    let lastEventSeq: UInt64

    var isActive: Bool { status == "running" || status == "pending" }

    var sessionCount: Int { (sessionIds?.count ?? 0) + (graphSessionIds?.count ?? 0) }

    var latestTerminalRunOutcome: String? {
        guard let lastRunOutcome, Self.terminalStatuses.contains(lastRunOutcome) else {
            return nil
        }
        return lastRunOutcome
    }

    var latestRunLastError: String? {
        progress?.lastError
    }

    private static let terminalStatuses: Set<String> = ["completed", "stopped", "failed"]

    private enum CodingKeys: String, CodingKey {
        case id, title, status, mode, workspaceId, workdir, scheduleId
        case createdAt, updatedAt, startedAt, finishedAt, graphRunId
        case sessionIds, graphSessionIds, progress, lastRunOutcome, firstModelId
        case initialAgentId, initialAcpMode, initialAcpThoughtLevel, lastEventSeq
    }

    init(from decoder: Decoder) throws {
        let values = try decoder.container(keyedBy: CodingKeys.self)
        id = try values.decode(String.self, forKey: .id)
        title = try values.decode(String.self, forKey: .title)
        status = try values.decode(String.self, forKey: .status)
        mode = try values.decode(String.self, forKey: .mode)
        workspaceId = try values.decode(String.self, forKey: .workspaceId)
        workdir = try values.decodeIfPresent(String.self, forKey: .workdir)
        scheduleId = try values.decodeIfPresent(String.self, forKey: .scheduleId)
        createdAt = try values.decode(String.self, forKey: .createdAt)
        updatedAt = try values.decode(String.self, forKey: .updatedAt)
        startedAt = try values.decodeIfPresent(Int64.self, forKey: .startedAt)
        finishedAt = try values.decodeIfPresent(Int64.self, forKey: .finishedAt)
        graphRunId = try values.decodeIfPresent(String.self, forKey: .graphRunId)
        sessionIds = try values.decodeIfPresent([String].self, forKey: .sessionIds)
        graphSessionIds = try values.decodeIfPresent([String].self, forKey: .graphSessionIds)
        progress = try values.decodeIfPresent(JobRunProgress.self, forKey: .progress)
        lastRunOutcome = try values.decodeIfPresent(String.self, forKey: .lastRunOutcome)
        firstModelId = try values.decodeIfPresent(String.self, forKey: .firstModelId)
        initialAgentId = try values.decodeIfPresent(String.self, forKey: .initialAgentId)
        initialAcpMode = try values.decodeIfPresent(String.self, forKey: .initialAcpMode)
        initialAcpThoughtLevel = try values.decodeIfPresent(String.self, forKey: .initialAcpThoughtLevel)
        lastEventSeq = try values.decodeIfPresent(UInt64.self, forKey: .lastEventSeq) ?? 0
    }

}

struct JobRunProgress: Decodable, Sendable {
    let totalSteps: Int?
    let currentPath: [Int]?
    let currentStartedAt: Int64?
    let completedCount: Int?
    let failedCount: Int?
    let results: [JobIterationResult]?
    let lastError: String?
    let persistWarnings: [String]?
    let groupActualIterations: [String: Int]?
    let groupActualLeafCounts: [String: Int]?
    let skippedPaths: [String: Bool]?
    let gracefulStopPending: Bool?
}

struct JobIterationResult: Decodable, Sendable {
    let path: [Int]
    let sessionId: String
    let success: Bool
    let durationMs: Int64
    let tokens: Int
    let error: String?
    let content: String?
}

struct SessionMessagesResponse: Decodable, Sendable {
    let modelId: String
    let type: String?
    let messages: [HistoryMessage]
    let tokenUsage: TokenUsage?
    let workdir: String?
    let acpMode: String?
    let acpThoughtLevel: String?
    let agents: [String: AgentDisplayInfo]?
}

struct TokenUsage: Decodable, Hashable, Sendable {
    let totalTokens: Int
}

struct AgentDisplayInfo: Decodable, Hashable, Sendable {
    let agentId: String
    let displayName: String
    let iconUrl: String
    let deleted: Bool
}

struct ResolveAgentDisplayInfoResponse: Decodable, Sendable {
    let agents: [String: AgentDisplayInfo]
}

struct ResolveAgentDisplayInfoRequest: Encodable, Sendable {
    let ids: [String]
}

struct HistoryMessage: Decodable, Identifiable, Hashable, Sendable {
    let id: String
    let role: String
    let content: String
    let reasoningContent: String?
    let toolCallId: String?
    let toolCalls: [HistoryToolCall]?
    let imageUrls: [String]?
    let fileAttachments: [FileAttachment]?
    let isShellOutput: Bool?
    let isThinking: Bool?
    let failed: Bool?
    let placeholder: Bool?
    let placeholderReason: String?
    let startedAt: Int64?
    let finishedAt: Int64?
    let thoughtStartedAt: Int64?
    let thoughtFinishedAt: Int64?
}

struct HistoryToolCall: Decodable, Hashable, Sendable {
    let id: String
    let name: String
    let arguments: String
}

struct ChatMessage: Identifiable, Hashable, Sendable {
    enum Kind: Hashable, Sendable {
        case user
        case assistant
        case thought
        case tool
        case system
    }

    enum ToolStatus: String, Hashable, Sendable {
        case processing = "Processing"
        case success = "Success"
        case error = "Error"
        case placeholder = "Placeholder"

        init(serverValue: String?) {
            switch serverValue?.lowercased() {
            case "success": self = .success
            case "error": self = .error
            case "placeholder": self = .placeholder
            default: self = .processing
            }
        }
    }

    let id: String
    var kind: Kind
    var content: String
    var detail: String?
    var isFinished: Bool
    var isFailed: Bool
    var timestamp: Int64?
    var imagePaths: [String]
    var fileAttachments: [FileAttachment]
    var finishedAt: Int64?
    var thinkingContent: String?
    var thinkingFinishedAt: Int64?
    var isShellOutput: Bool
    var toolCallID: String?
    var toolName: String?
    var toolArguments: String?
    var toolStatus: ToolStatus?
    var placeholderReason: String?
    var agentDisplayName: String?
    var agentIconUrl: String?
    var isOptimistic: Bool

    init(
        id: String,
        kind: Kind,
        content: String,
        detail: String?,
        isFinished: Bool,
        isFailed: Bool,
        timestamp: Int64?,
        imagePaths: [String] = [],
        fileAttachments: [FileAttachment] = [],
        finishedAt: Int64? = nil,
        thinkingContent: String? = nil,
        thinkingFinishedAt: Int64? = nil,
        isShellOutput: Bool = false,
        toolCallID: String? = nil,
        toolName: String? = nil,
        toolArguments: String? = nil,
        toolStatus: ToolStatus? = nil,
        placeholderReason: String? = nil,
        agentDisplayName: String? = nil,
        agentIconUrl: String? = nil,
        isOptimistic: Bool = false
    ) {
        self.id = id
        self.kind = kind
        self.content = content
        self.detail = detail
        self.isFinished = isFinished
        self.isFailed = isFailed
        self.timestamp = timestamp
        self.imagePaths = imagePaths
        self.fileAttachments = fileAttachments
        self.finishedAt = finishedAt
        self.thinkingContent = thinkingContent
        self.thinkingFinishedAt = thinkingFinishedAt
        self.isShellOutput = isShellOutput
        self.toolCallID = toolCallID
        self.toolName = toolName
        self.toolArguments = toolArguments
        self.toolStatus = toolStatus
        self.placeholderReason = placeholderReason
        self.agentDisplayName = agentDisplayName
        self.agentIconUrl = agentIconUrl
        self.isOptimistic = isOptimistic
    }

    init(history: HistoryMessage, idPrefix: String? = nil) {
        id = idPrefix.map { "\($0):\(history.id)" } ?? history.id
        if history.role == "user" {
            kind = .user
        } else if history.role == "tool" {
            kind = .tool
        } else if history.isThinking == true {
            kind = .thought
        } else if history.role == "system" {
            kind = .system
        } else {
            kind = .assistant
        }
        content = history.isThinking == true ? (history.reasoningContent ?? history.content) : history.content
        if history.role == "tool" {
            detail = history.placeholderReason
        } else if let toolCalls = history.toolCalls, !toolCalls.isEmpty {
            detail = toolCalls.map { "\($0.name)\n\($0.arguments)" }.joined(separator: "\n\n")
        } else {
            detail = nil
        }
        isFinished = true
        isFailed = history.failed == true
        timestamp = history.startedAt
        imagePaths = history.imageUrls ?? []
        fileAttachments = history.fileAttachments ?? []
        finishedAt = history.finishedAt
        thinkingContent = history.isThinking == true ? nil : history.reasoningContent
        thinkingFinishedAt = history.thoughtFinishedAt
        isShellOutput = history.isShellOutput == true
        toolCallID = history.toolCallId
        toolName = nil
        toolArguments = nil
        if history.role == "tool" {
            toolStatus = history.placeholder == true ? .placeholder : (history.failed == true ? .error : .success)
        } else {
            toolStatus = nil
        }
        placeholderReason = history.placeholderReason
        agentDisplayName = nil
        agentIconUrl = nil
        isOptimistic = false
    }
}

struct ServerEvent: Decodable, Sendable {
    let type: String
    let sessionId: String?
    let clientMessageId: String?
    let name: String?
    let value: ServerEventValue?
    let timestamp: Int64?
    let messageId: String?
    let role: String?
    let delta: String?
    let message: String?
    let text: String?
    let code: String?
    let toolCallId: String?
    let toolCallName: String?
    let toolCallStatus: String?
    let replace: Bool?
    let external: EventExternal?
    let runOutcome: String?

    private enum CodingKeys: String, CodingKey {
        case type, sessionId, clientMessageId, name, value, timestamp, messageId, role, delta, message, text
        case code, toolCallId, toolCallName, toolCallStatus, replace, external, runOutcome
    }

    init(from decoder: Decoder) throws {
        let values = try decoder.container(keyedBy: CodingKeys.self)
        type = try values.decode(String.self, forKey: .type)
        sessionId = try values.decodeIfPresent(String.self, forKey: .sessionId)
        clientMessageId = try values.decodeIfPresent(String.self, forKey: .clientMessageId)
        name = try values.decodeIfPresent(String.self, forKey: .name)
        value = try values.decodeIfPresent(ServerEventValue.self, forKey: .value)
        timestamp = try values.decodeIfPresent(Int64.self, forKey: .timestamp)
        messageId = try values.decodeIfPresent(String.self, forKey: .messageId)
        role = try values.decodeIfPresent(String.self, forKey: .role)
        delta = try values.decodeIfPresent(String.self, forKey: .delta)
        message = try values.decodeIfPresent(String.self, forKey: .message)
        text = try values.decodeIfPresent(String.self, forKey: .text)
        code = try values.decodeIfPresent(String.self, forKey: .code)
        toolCallId = try values.decodeIfPresent(String.self, forKey: .toolCallId)
        toolCallName = try values.decodeIfPresent(String.self, forKey: .toolCallName)
        toolCallStatus = try values.decodeIfPresent(String.self, forKey: .toolCallStatus)
        replace = try values.decodeIfPresent(Bool.self, forKey: .replace)
        external = try values.decodeIfPresent(EventExternal.self, forKey: .external)
        runOutcome = try values.decodeIfPresent(String.self, forKey: .runOutcome)
    }
}

struct EventExternal: Decodable, Sendable {
    let isThinking: Bool?
    let isShellOutput: Bool?
    let placeholderReason: String?
}

struct ServerEventValue: Decodable, Sendable {
    let phase: String?
    let detail: String?
    let title: String?
    let error: String?
    let totalTokens: Int?
    let version: Int64?
}

struct CreateJobResponse: Decodable, Sendable {
    let jobId: String
    let createdAt: Int64
    let status: String?
}

struct CreateJobRequest: Encodable, Sendable {
    let modelId: String
    let agentType: String
    let acpMode: String?
    let acpThoughtLevel: String?
    let mode = "interactive"
    let workdir: String?
    let workspaceId: String
    let clientMessageId: String?

    init(
        modelId: String,
        agentType: String,
        acpMode: String?,
        acpThoughtLevel: String?,
        workdir: String?,
        workspaceId: String,
        clientMessageId: String? = nil
    ) {
        self.modelId = modelId
        self.agentType = agentType
        self.acpMode = acpMode
        self.acpThoughtLevel = acpThoughtLevel
        self.workdir = workdir
        self.workspaceId = workspaceId
        self.clientMessageId = clientMessageId
    }
}

struct SendMessageRequest: Encodable, Sendable {
    struct Message: Encodable, Sendable {
        let id: String
        let type = "text"
        let content: String
        let timestamp: Int64
        let role = "user"
        let imageUrls: [String]?
        let fileAttachments: [FileAttachment]?

        init(id: String, content: String, timestamp: Int64, imageUrls: [String]? = nil, fileAttachments: [FileAttachment]? = nil) {
            self.id = id
            self.content = content
            self.timestamp = timestamp
            self.imageUrls = imageUrls
            self.fileAttachments = fileAttachments
        }
    }

    let messages: [Message]
    let modelId: String?
    let agentType: String?
    let sessionId: String?
    let clientMessageId: String
    let acpMode: String?
    let acpThoughtLevel: String?
    let bypassCommand: Bool

    init(
        messages: [Message],
        modelId: String?,
        agentType: String?,
        sessionId: String?,
        clientMessageId: String,
        acpMode: String?,
        acpThoughtLevel: String?,
        bypassCommand: Bool = false
    ) {
        self.messages = messages
        self.modelId = modelId
        self.agentType = agentType
        self.sessionId = sessionId
        self.clientMessageId = clientMessageId
        self.acpMode = acpMode
        self.acpThoughtLevel = acpThoughtLevel
        self.bypassCommand = bypassCommand
    }
}

struct SendMessageResponse: Decodable, Sendable {
    let code: Int?
    let status: String
    let clientMessageId: String?
    let messageState: String?
    let event: ServerEvent?
    let queue: MessageQueueSnapshot?

    var isDuplicate: Bool { status == "duplicate" }
}

struct MessageQueueEnvelope: Decodable, Sendable {
    let code: Int?
    let queue: MessageQueueSnapshot
}

struct MessageQueueSnapshot: Decodable, Hashable, Sendable {
    let jobId: String
    let version: Int64
    let paused: Bool
    let pauseReason: String?
    let willContinue: Bool
    let active: QueuedJobMessage?
    let items: [QueuedJobMessage]
}

struct QueuedJobMessage: Decodable, Identifiable, Hashable, Sendable {
    struct Message: Decodable, Hashable, Sendable {
        let content: String
        let imageUrls: [String]?
        let fileAttachments: [FileAttachment]?
    }

    let id: String
    let messages: [Message]
    let state: String
    let error: String?
    let createdAt: Int64

    var summaryLine: String {
        let text = messages.map(\.content).filter { !$0.isEmpty }.joined(separator: "\n")
        if !text.isEmpty { return text }
        if !imagePaths.isEmpty { return "[图片]" }
        return fileAttachments.isEmpty ? "空消息" : "[文件]"
    }

    var imagePaths: [String] { messages.flatMap { $0.imageUrls ?? [] } }
    var fileAttachments: [FileAttachment] { messages.flatMap { $0.fileAttachments ?? [] } }
}

struct UploadResponse: Decodable, Sendable {
    let code: Int
    let path: String
    let name: String?
    let mimeType: String?
    let size: Int64?
}

struct FileAttachment: Codable, Hashable, Sendable {
    let path: String
    let name: String
    let mimeType: String?
    let size: Int64?
}

struct PendingUpload: Hashable, Sendable {
    let data: Data
    let filename: String
    let mimeType: String
    let isImage: Bool
}

struct GraphWorkflowSummary: Decodable, Identifiable, Hashable, Sendable {
    let id: String
    let workspaceId: String?
    let name: String
    let description: String?
    let type: String?
    let createdAt: String
    let updatedAt: String
    let nodeCount: Int
    let edgeCount: Int
}

struct GraphWorkflowWarning: Decodable, Hashable, Sendable {
    let file: String
    let error: String
}

struct GraphWorkflowListResponse: Decodable, Sendable {
    let workflows: [GraphWorkflowSummary]
    let warnings: [GraphWorkflowWarning]?
}

struct GraphWorkflow: Codable, Identifiable, Hashable, Sendable {
    let id: String
    let workspaceId: String?
    let name: String
    let description: String?
    let type: String?
    var config: GraphConfig
    let createdAt: String
    let updatedAt: String
    let deleted: Bool?
}

struct GraphConfig: Codable, Hashable, Sendable {
    var nodes: [GraphNode]
    var edges: [GraphEdge]
    var variables: [String: String]? = nil
    var disabledVars: [String]? = nil
    var canvas: GraphCanvasState? = nil
    var runConfig: GraphRunConfiguration? = nil
    var workspaceId: String? = nil
    var workdir: String? = nil
    var sandboxId: String? = nil
}

struct GraphNode: Codable, Identifiable, Hashable, Sendable {
    let id: String
    let type: String
    var title: String?
    let parentId: String?
    var config: GraphNodeConfiguration?
    let layout: GraphNodeLayout?
    let metadata: [String: String]?

    var displayName: String {
        let trimmed = title?.trimmingCharacters(in: .whitespacesAndNewlines) ?? ""
        return trimmed.isEmpty ? id : trimmed
    }
}

struct GraphNodeConfiguration: Codable, Hashable, Sendable {
    var script: String? = nil
    var prompt: String? = nil
    var agentType: String? = nil
    var modelId: String? = nil
    var acpMode: String? = nil
    var acpThoughtLevel: String? = nil
    var sessionStrategy: String? = nil
    var outputVariables: [String]? = nil
    var lastAssistantAlias: String? = nil
    var timeoutSeconds: Int? = nil
    var condition: String? = nil
    var loopMode: String? = nil
    var fixedCount: Int? = nil
    var untilCondition: String? = nil
    var maxIterations: Int? = nil
    var hookScript: String? = nil
    var endHookMode: String? = nil
}

struct GraphNodeLayout: Codable, Hashable, Sendable {
    let x: Double
    let y: Double
    let width: Double?
    let height: Double?
}

struct GraphEdge: Codable, Identifiable, Hashable, Sendable {
    let id: String
    let sourceNodeId: String
    let targetNodeId: String
    let sourcePort: String?
    let targetPort: String?
    let metadata: [String: String]?
}

struct GraphCanvasState: Codable, Hashable, Sendable {
    let viewport: GraphCanvasViewport?
}

struct GraphCanvasViewport: Codable, Hashable, Sendable {
    let x: Double
    let y: Double
    let zoom: Double
}

struct GraphRunConfiguration: Codable, Hashable, Sendable {
    var concurrencyLimit: Int? = nil
    var defaultNodeTimeoutSec: Int? = nil
    var jobTimeoutSec: Int? = nil
    var defaultLoopMaxIters: Int? = nil
    var instanceLimit: Int? = nil
    var snapshotByteLimit: Int64? = nil
}

struct GraphWorkflowResponse: Decodable, Sendable {
    let workflow: GraphWorkflow?
    let errors: [GraphValidationError]?
}

struct GraphValidationRequest: Encodable, Sendable {
    let config: GraphConfig
}

struct GraphValidationResponse: Decodable, Sendable {
    let valid: Bool
    let errors: [GraphValidationError]?
}

struct GraphValidationError: Decodable, Hashable, Sendable {
    let type: String
    let message: String
    let nodeId: String?
    let edgeId: String?
    let variable: String?
    let configKey: String?

    var location: String? {
        if let nodeId, !nodeId.isEmpty { return "节点 \(nodeId)" }
        if let edgeId, !edgeId.isEmpty { return "连线 \(edgeId)" }
        if let variable, !variable.isEmpty { return "变量 \(variable)" }
        if let configKey, !configKey.isEmpty { return "配置 \(configKey)" }
        return nil
    }
}

struct StartGraphRunRequest: Encodable, Sendable {
    let workflowId: String
    let workflowUpdatedAt: String
    let workspaceId: String
    let workdir: String
    let config: GraphConfig
}

struct StartGraphRunResponse: Decodable, Sendable {
    let run: GraphRunSummary?
    let errors: [GraphValidationError]?
}

struct GraphRunStatusResponse: Decodable, Sendable {
    let run: GraphRunSummary?
    let progress: GraphProgressSummary?
    let instances: [GraphInstanceSummary]?
    let edges: [GraphEdgeSummary]?
    let eventCount: Int?
    let agents: [String: AgentDisplayInfo]?

    init(
        run: GraphRunSummary?,
        progress: GraphProgressSummary?,
        instances: [GraphInstanceSummary]?,
        edges: [GraphEdgeSummary]? = nil,
        eventCount: Int? = nil,
        agents: [String: AgentDisplayInfo]? = nil
    ) {
        self.run = run
        self.progress = progress
        self.instances = instances
        self.edges = edges
        self.eventCount = eventCount
        self.agents = agents
    }
}

struct GraphRunActionResponse: Decodable, Sendable {
    let run: GraphRunSummary?
}

struct GraphStreamEvent: Decodable, Sendable {
    let id: String
    let runId: String
    let type: String
    let nodeId: String?
    let message: String?
    let payload: [String: String]?
    let error: GraphStreamError?
    let createdAt: Int64
}

struct GraphStreamError: Decodable, Sendable {
    let message: String?
    let stdout: String?
    let stderr: String?
    let modelOutput: String?
    let details: [String: String]?

    var fullDetail: String {
        var parts: [String] = []
        if let message, !message.isEmpty { parts.append(message) }
        if let stdout, !stdout.isEmpty { parts.append("stdout:\n\(stdout)") }
        if let stderr, !stderr.isEmpty { parts.append("stderr:\n\(stderr)") }
        if let modelOutput, !modelOutput.isEmpty { parts.append("model output:\n\(modelOutput)") }
        if let details, !details.isEmpty {
            parts.append(details.sorted(by: { $0.key < $1.key }).map { "\($0.key): \($0.value)" }.joined(separator: "\n"))
        }
        return parts.joined(separator: "\n\n")
    }
}

struct GraphRunSummary: Decodable, Sendable {
    let id: String
    let workflowId: String?
    let jobId: String
    let workspaceId: String?
    let status: String
    let currentVersion: Int
    let startedAt: Int64?
    let finishedAt: Int64?
    let lastError: GraphRuntimeErrorSummary?
    let progress: GraphProgressSummary?
}

struct GraphProgressSummary: Decodable, Sendable {
    let totalCount: Int
    let completedCount: Int
    let failedCount: Int
    let skippedCount: Int
    let interruptedCount: Int
    let runningCount: Int
    let lastError: String?
}

struct GraphEdgeSummary: Decodable, Identifiable, Sendable {
    let edgeId: String
    let sourceInstanceKey: GraphInstanceKeySummary
    let targetInstanceKey: GraphInstanceKeySummary
    let status: String
    let resolvedAt: Int64?
    let reason: String?

    var id: String { edgeId }
}

struct GraphInstanceSummary: Decodable, Identifiable, Sendable {
    let key: GraphInstanceKeySummary
    let nodeId: String
    let nodeTitle: String?
    let nodeType: String
    let status: String
    let version: Int
    let sessionId: String?
    let displaySessionId: String?
    let startedAt: Int64?
    let finishedAt: Int64?
    let durationMs: Int64?
    let error: GraphRuntimeErrorSummary?
    let blockedReason: String?

    var id: String {
        key.id
    }

    var displayName: String {
        guard let nodeTitle, !nodeTitle.isEmpty else { return nodeId }
        return nodeTitle
    }
}

struct GraphInstanceKeySummary: Decodable, Hashable, Sendable {
    let nodeId: String
    let iterations: [GraphLoopIterationSummary]?

    var id: String {
        let suffix = (iterations ?? []).map { "\($0.loopNodeId):\($0.index)" }.joined(separator: "/")
        return suffix.isEmpty ? nodeId : "\(nodeId)/\(suffix)"
    }
}

struct GraphLoopIterationSummary: Decodable, Hashable, Sendable {
    let loopNodeId: String
    let index: Int
}

struct GraphRuntimeErrorSummary: Decodable, Sendable {
    let nodeId: String?
    let nodeTitle: String?
    let nodeType: String?
    let message: String
    let retryCount: Int?
    let canResume: Bool
    let stdout: String?
    let stderr: String?
    let exitCode: Int?
    let modelOutput: String?
    let details: [String: String]?

    var fullDetail: String {
        var parts = [message]
        if let nodeTitle, !nodeTitle.isEmpty { parts.append("Node: \(nodeTitle)") }
        if let nodeId, !nodeId.isEmpty { parts.append("Node ID: \(nodeId)") }
        if let exitCode { parts.append("Exit code: \(exitCode)") }
        if let stdout, !stdout.isEmpty { parts.append("stdout:\n\(stdout)") }
        if let stderr, !stderr.isEmpty { parts.append("stderr:\n\(stderr)") }
        if let modelOutput, !modelOutput.isEmpty { parts.append("model output:\n\(modelOutput)") }
        if let details, !details.isEmpty {
            parts.append(details.sorted(by: { $0.key < $1.key }).map { "\($0.key): \($0.value)" }.joined(separator: "\n"))
        }
        return parts.joined(separator: "\n\n")
    }
}

struct GraphHookResultsResponse: Decodable, Sendable {
    let results: [GraphHookResult]
}

struct GraphHookResult: Decodable, Identifiable, Sendable {
    let nodeId: String
    let nodeTitle: String?
    let nodeType: String?
    let source: String?
    let status: String
    let exitCode: Int?
    let stdout: String?
    let stderr: String?
    let message: String?
    let finishedAt: Int64

    var id: String {
        "\(nodeId):\(finishedAt):\(status)"
    }
}

extension Int64 {
    var quartetDate: Date { Date(timeIntervalSince1970: TimeInterval(self) / 1_000) }
}
