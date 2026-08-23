import Foundation
import Combine
import SwiftUI

@MainActor
final class AppModel: ObservableObject {
    enum ConnectionPhase: Equatable, Sendable {
        case booting
        case disconnected
        case connecting
        case connected
    }

    struct ConnectionState: Equatable, Sendable {
        let phase: ConnectionPhase
        let lastSuccessfulSyncAt: Date?
        let lastFailureMessage: String?
        let isStale: Bool
        let hasPendingSync: Bool
        let isUsingCachedData: Bool

        var isConnected: Bool { phase == .connected }
    }

    struct GraphJobState: Equatable, Sendable {
        let status: String
        let lastError: String?
        let updatedAt: Date
    }

    static let defaultServerAddress = "https://devbox.fanlv.fun/"

    @Published var phase: ConnectionPhase = .booting
    @Published var serverAddress: String {
        didSet {
            guard StorageKey.connectionIdentity(for: serverAddress) != StorageKey.connectionIdentity(for: oldValue) else {
                return
            }
            connectionGeneration &+= 1
            _ = invalidateDashboardRequests()
            if phase == .connecting {
                phase = .disconnected
            }
            if StorageKey.tokenAccount(for: serverAddress) != StorageKey.tokenAccount(for: credentialServerAddress) {
                token = ""
            }
        }
    }
    @Published var token: String
    @Published private(set) var health: HealthResponse?
    @Published private(set) var workspaces: [WorkspaceSummary] = []
    @Published private(set) var jobs: [JobSummary] = []
    @Published private(set) var selectedWorkspaceID: String?
    @Published private(set) var hideScheduledJobs: Bool
    @Published private(set) var isRefreshing = false
    @Published private(set) var isLoadingMore = false
    @Published private(set) var hasMoreJobs = false
    @Published private(set) var lastSuccessfulSyncAt: Date?
    @Published private(set) var lastSyncFailureMessage: String?
    @Published private(set) var isDataStale = false
    @Published private(set) var hasPendingSync = false
    @Published private(set) var isUsingCachedData = false
    @Published private(set) var graphJobStates: [String: GraphJobState] = [:]
    @Published private(set) var isRestartingWeb = false
    @Published var presentedError: PresentedError?

    private let defaults: UserDefaults
    private let cacheStore: DashboardCacheStore
    private let sentMessageHistoryStore: SentMessageHistoryStore
    private let uiTestScenario: String?
    private var credentialServerAddress: String
    private var credentialCacheNamespace: String
    private var nextCursor: String?
    private var hasLoadedCachedDashboard = false
    private var dashboardGeneration: UInt64 = 0
    private var connectionGeneration: UInt64 = 0
    private var dashboardRefreshTask: Task<Void, Never>?
    private var loadMoreTask: Task<Void, Never>?
    private var isDashboardPolling = false
    private var dashboardPollETag: String?
    private var dashboardPollScope: DashboardPollScope?
    private var uiTestJobs: [JobSummary] = []

    init(
        defaults: UserDefaults = .standard,
        cacheStore: DashboardCacheStore = DashboardCacheStore(),
        processArguments: [String] = ProcessInfo.processInfo.arguments
    ) {
#if DEBUG
        let detectedUITestScenario = processArguments.first(where: { $0.hasPrefix("--ui-testing-") })
#else
        let detectedUITestScenario: String? = nil
#endif
        let effectiveDefaults: UserDefaults
        if detectedUITestScenario != nil,
           let testDefaults = UserDefaults(suiteName: "fun.fanlv.quartet.ios.ui-tests") {
            testDefaults.removePersistentDomain(forName: "fun.fanlv.quartet.ios.ui-tests")
            effectiveDefaults = testDefaults
        } else {
            effectiveDefaults = defaults
        }
        self.defaults = effectiveDefaults
        sentMessageHistoryStore = SentMessageHistoryStore(defaults: effectiveDefaults)
        self.cacheStore = detectedUITestScenario == nil
            ? cacheStore
            : DashboardCacheStore(directoryName: "QuartetUITests")
        uiTestScenario = detectedUITestScenario
        hideScheduledJobs = effectiveDefaults.object(forKey: StorageKey.hideScheduledJobs) as? Bool ?? true
        let storedServerAddress = effectiveDefaults.string(forKey: StorageKey.serverAddress) ?? Self.defaultServerAddress
        let storedCredentialNamespace = effectiveDefaults.string(forKey: StorageKey.credentialCacheNamespace) ?? UUID().uuidString
        serverAddress = storedServerAddress
        credentialServerAddress = storedServerAddress
        credentialCacheNamespace = storedCredentialNamespace
        effectiveDefaults.set(storedCredentialNamespace, forKey: StorageKey.credentialCacheNamespace)
        token = detectedUITestScenario == nil
            ? Self.loadStoredToken(for: storedServerAddress, migrateLegacyCredential: true)
            : ""
        selectedWorkspaceID = effectiveDefaults.string(forKey: StorageKey.selectedWorkspaceID)
        if let timestamp = effectiveDefaults.object(forKey: StorageKey.lastSuccessfulSyncAt) as? Double {
            lastSuccessfulSyncAt = Date(timeIntervalSince1970: timestamp)
        }
    }

    var activeJobCount: Int {
        jobs.filter { job in
            let status = displayedStatus(for: job)
            return status == "running" || status == "pending" || status == "stepStopping"
        }.count
    }

    var selectedWorkspace: WorkspaceSummary? {
        guard let selectedWorkspaceID else { return nil }
        return workspaces.first { $0.id == selectedWorkspaceID }
    }

    var canPresentDashboard: Bool {
        phase == .connected || hasDashboardContent
    }

    var isRunningUITests: Bool { uiTestScenario != nil }

    var connectionState: ConnectionState {
        ConnectionState(
            phase: phase,
            lastSuccessfulSyncAt: lastSuccessfulSyncAt,
            lastFailureMessage: lastSyncFailureMessage,
            isStale: isDataStale,
            hasPendingSync: hasPendingSync,
            isUsingCachedData: isUsingCachedData
        )
    }

    func bootstrap() async {
        guard phase == .booting else { return }
        if uiTestScenario == "--ui-testing-onboarding" {
            serverAddress = Self.defaultServerAddress
            token = ""
            phase = .disconnected
            return
        }
        if uiTestScenario != nil {
            seedUITestDashboard()
            return
        }
        let hasValidatedConnection = defaults.bool(forKey: StorageKey.connectionValidated)
        if hasValidatedConnection {
            await loadCachedDashboardIfNeeded()
        } else {
            await cacheStore.clear()
        }
        guard hasValidatedConnection else {
            phase = .disconnected
            return
        }
        await connect()
    }

    func connect() async {
        guard phase != .connecting else { return }
        connectionGeneration &+= 1
        let generation = connectionGeneration
        let requestedToken = token.trimmingCharacters(in: .whitespacesAndNewlines)
        phase = .connecting
        hasPendingSync = true
        do {
            let client = try makeClient()
            let credentialAccount = StorageKey.tokenAccount(for: client.baseURL.absoluteString)
            let health = try await client.health()
            guard isCurrentConnectionRequest(
                generation: generation,
                client: client,
                requestedToken: requestedToken
            ) else {
                finishSupersededConnectionIfNeeded(generation: generation)
                return
            }
            if health.authRequired {
                try await client.verifyAuthentication()
                guard isCurrentConnectionRequest(
                    generation: generation,
                    client: client,
                    requestedToken: requestedToken
                ) else {
                    finishSupersededConnectionIfNeeded(generation: generation)
                    return
                }
            }
            try KeychainStore.write(requestedToken, account: credentialAccount)
            credentialServerAddress = client.baseURL.absoluteString
            serverAddress = credentialServerAddress
            defaults.set(serverAddress, forKey: StorageKey.serverAddress)
            defaults.set(true, forKey: StorageKey.connectionValidated)
            self.health = health
            lastSyncFailureMessage = nil
            await refreshDashboard(
                userInitiated: false,
                presentFailure: true,
                disconnectOnFailure: false
            )
        } catch {
            guard generation == connectionGeneration else { return }
            phase = .disconnected
            await handleSyncFailure(
                error,
                presentToUser: !hasDashboardContent,
                disconnect: true
            )
        }
    }

    func refreshDashboard(
        userInitiated: Bool = true,
        presentFailure: Bool = false,
        clearCachedSnapshot: Bool = false,
        disconnectOnFailure: Bool = true
    ) async {
        if isRunningUITests {
            lastSuccessfulSyncAt = Date()
            lastSyncFailureMessage = nil
            isDataStale = false
            hasPendingSync = false
            return
        }
        guard phase != .connecting || presentFailure else { return }
        let generation = beginDashboardRequest()
        let workspaceID = selectedWorkspaceID
        let excludeScheduled = hideScheduledJobs
        let shouldDisconnectOnFailure = disconnectOnFailure || phase == .disconnected
        isRefreshing = true
        defer {
            if dashboardGeneration == generation {
                dashboardRefreshTask = nil
                isRefreshing = false
            }
        }
        await cacheStore.advanceGeneration(
            to: generation,
            clearingExistingCache: clearCachedSnapshot
        )
        guard isCurrentDashboardRequest(generation: generation, workspaceID: workspaceID) else { return }

        let refreshTask = Task { @MainActor [weak self] in
            guard let self else { return }
            await self.performDashboardRefresh(
                generation: generation,
                workspaceID: workspaceID,
                excludeScheduled: excludeScheduled,
                presentFailure: userInitiated || presentFailure,
                disconnectOnFailure: shouldDisconnectOnFailure
            )
        }
        dashboardRefreshTask = refreshTask
        await withTaskCancellationHandler {
            await refreshTask.value
        } onCancel: {
            refreshTask.cancel()
        }

    }

    private func performDashboardRefresh(
        generation: UInt64,
        workspaceID: String?,
        excludeScheduled: Bool,
        presentFailure: Bool,
        disconnectOnFailure: Bool
    ) async {
        var failureWorkspaceID = workspaceID
        do {
            let client = try makeClient()
            async let workspaceRequest = client.workspaces()
            async let visibleJobsRequest = client.jobs(
                workspaceID: workspaceID,
                limit: 100,
                excludeScheduled: excludeScheduled
            )
            let (workspaceResponse, requestedVisibleJobsResponse) = try await (
                workspaceRequest,
                visibleJobsRequest
            )
            guard isCurrentDashboardRequest(generation: generation, workspaceID: workspaceID) else { return }

            workspaces = workspaceResponse.workspaces
            var visibleJobsResponse = requestedVisibleJobsResponse
            if let workspaceID, !workspaces.contains(where: { $0.id == workspaceID }) {
                self.selectedWorkspaceID = nil
                defaults.removeObject(forKey: StorageKey.selectedWorkspaceID)
                jobs = []
                nextCursor = nil
                hasMoreJobs = false
                await cacheStore.clear()
                failureWorkspaceID = nil
                visibleJobsResponse = try await client.jobs(
                    workspaceID: nil,
                    limit: 100,
                    excludeScheduled: excludeScheduled
                )
                guard isCurrentDashboardRequest(generation: generation, workspaceID: nil) else { return }
            }
            applyFirstPage(visibleJobsResponse)
            markSyncSucceeded()
            let cacheSnapshot = dashboardCacheSnapshot()
            await cacheStore.save(
                cacheSnapshot,
                generation: generation
            )
        } catch {
            guard isCurrentDashboardRequest(generation: generation, workspaceID: failureWorkspaceID),
                  !Task.isCancelled else { return }
            await handleSyncFailure(
                error,
                presentToUser: presentFailure,
                disconnect: disconnectOnFailure
            )
        }
    }

    func selectWorkspace(_ id: String?) async {
        guard selectedWorkspaceID != id else { return }
        selectedWorkspaceID = id
        if isRunningUITests {
            jobs = filteredUITestJobs(workspaceID: id)
            return
        }
        if let id {
            defaults.set(id, forKey: StorageKey.selectedWorkspaceID)
        } else {
            defaults.removeObject(forKey: StorageKey.selectedWorkspaceID)
        }
        jobs = []
        nextCursor = nil
        hasMoreJobs = false
        await refreshDashboard(userInitiated: false, clearCachedSnapshot: true)
    }

    func setHideScheduledJobs(_ hidden: Bool) async {
        guard hideScheduledJobs != hidden else { return }
        hideScheduledJobs = hidden
        defaults.set(hidden, forKey: StorageKey.hideScheduledJobs)
        if isRunningUITests {
            jobs = filteredUITestJobs(workspaceID: selectedWorkspaceID)
            return
        }
        jobs = []
        nextCursor = nil
        hasMoreJobs = false
        await refreshDashboard(userInitiated: false, clearCachedSnapshot: true)
    }

    func reloadJobs() async {
        guard !isRunningUITests else { return }
        guard phase == .connected else { return }
        await refreshDashboard(userInitiated: false)
    }

    func pollDashboard() async {
        guard !isRunningUITests,
              phase == .connected,
              !isRefreshing,
              !isLoadingMore,
              !isDashboardPolling else { return }

        let generation = dashboardGeneration
        let workspaceID = selectedWorkspaceID
        let excludeScheduled = hideScheduledJobs
        let scope = DashboardPollScope(
            connectionIdentity: StorageKey.connectionIdentity(for: serverAddress),
            workspaceID: workspaceID,
            excludeScheduled: excludeScheduled
        )
        let etag = dashboardPollScope == scope ? dashboardPollETag : nil
        isDashboardPolling = true
        defer { isDashboardPolling = false }

        do {
            let result = try await makeClient().pollJobs(
                workspaceID: workspaceID,
                limit: 100,
                excludeScheduled: excludeScheduled,
                etag: etag
            )
            guard isCurrentDashboardRequest(generation: generation, workspaceID: workspaceID) else { return }

            dashboardPollScope = scope
            switch result {
            case .notModified(let responseETag):
                dashboardPollETag = responseETag
            case .updated(let response, let responseETag):
                dashboardPollETag = responseETag
                applyPolledFirstPage(response)
                let cacheSnapshot = dashboardCacheSnapshot()
                await cacheStore.save(cacheSnapshot, generation: generation)
            }
            markSyncSucceeded()
        } catch {
            guard isCurrentDashboardRequest(generation: generation, workspaceID: workspaceID),
                  !Task.isCancelled else { return }
            await handleSyncFailure(error, presentToUser: false, disconnect: false)
        }
    }

    func fetchUsageStats(
        from: String?,
        to: String?,
        allTime: Bool,
        compareWithPrevious: Bool
    ) async throws -> UsageStatsReport {
        if isRunningUITests {
            return uiTestUsageStats(from: from, to: to)
        }
        return try await makeClient().usageStats(
            from: from,
            to: to,
            allTime: allTime,
            compareWithPrevious: compareWithPrevious
        )
    }

    func loadMoreJobs() async {
        guard phase == .connected,
              hasMoreJobs,
              !isRefreshing,
              !isLoadingMore,
              let nextCursor else { return }
        let generation = dashboardGeneration
        let workspaceID = selectedWorkspaceID
        let excludeScheduled = hideScheduledJobs
        isLoadingMore = true
        let pageTask = Task { @MainActor [weak self] in
            guard let self else { return }
            await self.performLoadMoreJobs(
                generation: generation,
                workspaceID: workspaceID,
                cursor: nextCursor,
                excludeScheduled: excludeScheduled
            )
        }
        loadMoreTask = pageTask
        await withTaskCancellationHandler {
            await pageTask.value
        } onCancel: {
            pageTask.cancel()
        }
        if dashboardGeneration == generation, selectedWorkspaceID == workspaceID {
            loadMoreTask = nil
            isLoadingMore = false
        }
    }

    private func performLoadMoreJobs(
        generation: UInt64,
        workspaceID: String?,
        cursor: String,
        excludeScheduled: Bool
    ) async {
        do {
            let response = try await makeClient().jobs(
                workspaceID: workspaceID,
                cursor: cursor,
                excludeScheduled: excludeScheduled
            )
            guard isCurrentPageRequest(
                generation: generation,
                workspaceID: workspaceID,
                cursor: cursor
            ) else { return }

            let existingIDs = Set(jobs.map(\.id))
            jobs.append(contentsOf: response.jobs.filter { !existingIDs.contains($0.id) })
            self.nextCursor = response.nextCursor
            hasMoreJobs = response.hasMore
            let cacheSnapshot = dashboardCacheSnapshot()
            await cacheStore.save(
                cacheSnapshot,
                generation: generation
            )
        } catch {
            guard isCurrentDashboardRequest(generation: generation, workspaceID: workspaceID),
                  !Task.isCancelled else { return }
            present(error)
        }
    }

    func jobDetail(id: String) async throws -> JobDetail {
        if isRunningUITests {
            return try uiTestJobDetail(id: id)
        }
        return try await makeClient().job(id: id)
    }

    func graphRunStatus(jobID: String) async throws -> GraphRunStatusResponse {
        if isRunningUITests {
            return uiTestGraphRunStatus(jobID: jobID)
        }
        return try await makeClient().graphRunStatus(jobID: jobID)
    }

    func performGraphAction(jobID: String, action: String) async throws -> GraphRunActionResponse {
        if isRunningUITests {
            return GraphRunActionResponse(run: uiTestGraphRunStatus(jobID: jobID).run)
        }
        return try await makeClient().graphRunAction(jobID: jobID, action: action)
    }

    func refreshGraphStatus(jobID: String) async {
        do {
            let response = try await makeClient().graphRunStatus(jobID: jobID)
            guard let job = jobSummary(id: jobID) else { return }
            applyGraphStatus(job: job, response: response)
        } catch {
            // Fall back to the fresh Job status instead of allowing an older
            // detailed Graph state to override it indefinitely.
            graphJobStates.removeValue(forKey: jobID)
        }
    }

    func observeGraphStatus(job: JobSummary, response: GraphRunStatusResponse) {
        applyGraphStatus(job: job, response: response)
    }

    func refreshGraphStatusIfNeeded(for job: JobSummary, force: Bool = false) async {
        guard job.mode == "graph" else { return }
        if !force,
           let current = graphJobStates[job.id],
           Date().timeIntervalSince(current.updatedAt) < 10 {
            return
        }
        await refreshGraphStatus(jobID: job.id)
    }

    func jobSummary(id: String) -> JobSummary? {
        jobs.first(where: { $0.id == id })
    }

    func displayedStatus(for job: JobSummary) -> String {
        graphJobStates[job.id]?.status
            ?? job.status
    }

    func displayedStatusLabel(for job: JobSummary) -> String {
        statusLabel(displayedStatus(for: job))
    }

    func displayedStatusColorKey(for job: JobSummary) -> String {
        statusColorKey(displayedStatus(for: job))
    }

    func graphState(for jobID: String) -> GraphJobState? {
        graphJobStates[jobID]
    }

    func availableAgents() async throws -> [AgentSummary] {
        let response = try await makeClient().agents()
        let available = response.agentList.filter(\.available)
        guard !available.isEmpty else {
            let details = response.agentList.map { agent in
                "\(agent.displayName.isEmpty ? agent.type : agent.displayName): \(agent.error ?? "不可用")"
            }.joined(separator: "\n")
            throw APIError(
                summary: "没有可用的 Agent",
                detail: details.isEmpty ? "GET /api/v1/agent/list 返回空列表。" : details
            )
        }
        return available
    }

    func agentCatalog() async throws -> [AgentSummary] {
        if isRunningUITests {
            return [
                AgentSummary(
                    agentId: "trae",
                    type: "trae",
                    modelId: "gpt-5.6",
                    displayName: "TraeCode",
                    availability: "available",
                    available: true,
                    refreshing: false,
                    error: nil,
                    models: AgentModelState(
                        availableModels: [
                            AgentModel(modelId: "gpt-5.6", name: "GPT-5.6", description: "默认模型"),
                            AgentModel(modelId: "gpt-5.4", name: "GPT-5.4", description: "快速模型")
                        ],
                        currentModelId: "gpt-5.6"
                    ),
                    modes: AgentModeState(
                        availableModes: [AgentOption(id: "default", name: "默认", description: nil)],
                        currentModeId: "default"
                    ),
                    thoughtLevels: AgentThoughtLevelState(
                        availableThoughtLevels: [AgentOption(id: "medium", name: "标准", description: nil)],
                        currentThoughtLevelId: "medium"
                    )
                )
            ]
        }
        return try await makeClient().agents().agentList
    }

    func agentPreferences() async throws -> [String: AgentPreferences] {
        if isRunningUITests {
            return [
                "trae": AgentPreferences(
                    favoriteModelIDs: ["gpt-5.6"],
                    defaultModelID: "gpt-5.4",
                    defaultMode: "default",
                    defaultThoughtLevel: "medium"
                )
            ]
        }
        let response = try await makeClient().agentPreferences()
        guard response.code == 0 else {
            throw APIError(
                summary: "无法读取 Agent 默认设置",
                detail: "GET /api/v1/config/settings/get 返回 code=\(response.code)。"
            )
        }
        return response.settings?.agentPreferences ?? [:]
    }

    func createJob(request: CreateJobRequest) async throws -> String {
        if isRunningUITests {
            hasPendingSync = true
            return "job-e2e-created"
        }
        let response = try await makeClient().createJob(request)
        hasPendingSync = true
        return response.jobId
    }

    func sentMessageHistory(workspaceID: String?) throws -> [SentMessageHistoryItem] {
        let scope = sentMessageHistoryScope(workspaceID: workspaceID)
        do {
            return try sentMessageHistoryStore.items(scope: scope)
        } catch {
            throw APIError(
                summary: "无法读取发送历史",
                detail: "读取本地发送历史失败。\n服务：\(serverAddress)\n工作空间：\(workspaceID ?? "default")\n\n\(String(reflecting: error))"
            )
        }
    }

    @discardableResult
    func recordSentMessage(_ content: String, workspaceID: String?) throws -> [SentMessageHistoryItem] {
        let scope = sentMessageHistoryScope(workspaceID: workspaceID)
        do {
            return try sentMessageHistoryStore.append(content: content, scope: scope)
        } catch {
            throw APIError(
                summary: "无法保存发送历史",
                detail: "保存本地发送历史失败。\n服务：\(serverAddress)\n工作空间：\(workspaceID ?? "default")\n\n\(String(reflecting: error))"
            )
        }
    }

    func apiClient() throws -> APIClient {
        try makeClient()
    }

    func saveWorkspaceDefaults(workspaceID: String, agent: String, model: String) async throws {
        if isRunningUITests { return }
        guard let index = workspaces.firstIndex(where: { $0.id == workspaceID }) else {
            throw APIError(summary: "工作空间不存在", detail: "未找到工作空间 \(workspaceID)")
        }
        let available = try await makeClient().agents().agentList.first { candidate in
            candidate.available && (candidate.agentId == agent || candidate.type == agent)
        }
        guard let available else {
            throw APIError(summary: "Agent 不可用", detail: "Agent \(agent) 不存在或当前不可用。")
        }
        let canonicalAgent = available.agentId
        let validModel = available.models?.availableModels.contains(where: { $0.modelId == model }) == true
            ? model
            : ""
        let saved = try await makeClient().updateWorkspaceDefaults(
            workspaces[index],
            defaultAgent: canonicalAgent,
            defaultModel: validModel
        )
        workspaces[index] = saved
    }

    func stopJob(id: String) async throws {
        try await makeClient().stopJob(id: id)
        hasPendingSync = true
        await reloadJobs()
    }

    func renameJob(id: String, title: String) async throws {
        try await makeClient().renameJob(id: id, title: title)
        hasPendingSync = true
        await reloadJobs()
    }

    func setJobPinned(id: String, pinned: Bool) async throws {
        try await makeClient().setJobPinned(id: id, pinned: pinned)
        hasPendingSync = true
        await reloadJobs()
    }

    func deleteJob(id: String) async throws {
        try await makeClient().deleteJob(id: id)
        hasPendingSync = true
        await reloadJobs()
    }

    func restartWeb() async throws {
        guard !isRestartingWeb else { return }
        isRestartingWeb = true
        defer { isRestartingWeb = false }

        let client = try makeClient()
        let previousHealth: HealthResponse?
        do {
            previousHealth = try await client.restartHealthProbe()
        } catch is CancellationError {
            throw CancellationError()
        } catch {
            previousHealth = nil
        }
        let response = try await client.restartWeb()
        guard response.code == 0 else {
            throw APIError(
                summary: "重启 Web 失败",
                detail: "POST \(client.baseURL.appendingPathComponent("api/v1/system/restart-web").absoluteString)\nHTTP 200\n\ncode: \(response.code)\nmsg: \(response.msg ?? "<empty>")\nlog_path: \(response.logPath ?? "<empty>")"
            )
        }

        let deadline = Date().addingTimeInterval(180)
        var sawUnavailable = false
        var lastProbeError: Error?
        while Date() < deadline {
            try await Task.sleep(for: .milliseconds(500))
            do {
                let currentHealth = try await client.restartHealthProbe()
                let previousInstanceID = previousHealth?.instanceId ?? ""
                let currentInstanceID = currentHealth.instanceId ?? ""
                let instanceChanged = !previousInstanceID.isEmpty
                    && !currentInstanceID.isEmpty
                    && currentInstanceID != previousInstanceID
                let instanceAdded = previousHealth != nil
                    && previousInstanceID.isEmpty
                    && !currentInstanceID.isEmpty
                if instanceChanged || instanceAdded || sawUnavailable {
                    health = currentHealth
                    await refreshDashboard(
                        userInitiated: false,
                        presentFailure: false,
                        disconnectOnFailure: false
                    )
                    return
                }
            } catch is CancellationError {
                throw CancellationError()
            } catch {
                sawUnavailable = true
                lastProbeError = error
            }
        }

        let probeDetail: String
        if let apiError = lastProbeError as? APIError {
            probeDetail = "\n\n最后一次健康探测：\n\(apiError.summary)\n\n\(apiError.detail)"
        } else if let lastProbeError {
            probeDetail = "\n\n最后一次健康探测：\n\(String(describing: lastProbeError))"
        } else {
            probeDetail = ""
        }
        throw APIError(
            summary: "重启 Web 超时",
            detail: "等待重启后的 Web 服务就绪超时。\n\n完整重启日志：\(response.logPath ?? "/tmp/quartet-web-restart.log")\(probeDetail)"
        )
    }

    func handleScenePhaseChange(_ phase: ScenePhase) async {
        guard !isRunningUITests else { return }
        switch phase {
        case .active:
            if defaults.bool(forKey: StorageKey.connectionValidated) {
                if self.phase == .disconnected && !token.isEmpty {
                    await connect()
                } else if self.phase == .connected {
                    await refreshDashboard(userInitiated: false)
                }
            }
        case .background:
            hasPendingSync = self.phase == .connected && (isDataStale || hasPendingSync || activeJobCount > 0)
        default:
            break
        }
    }

    func updateConnectionState(isStale: Bool? = nil, hasPendingSync: Bool? = nil, failureMessage: String? = nil) {
        if let isStale {
            isDataStale = isStale
        }
        if let hasPendingSync {
            self.hasPendingSync = hasPendingSync
        }
        if let failureMessage {
            lastSyncFailureMessage = failureMessage
        }
    }

    func editConnection() {
        let generation = invalidateDashboardRequests()
        connectionGeneration &+= 1
        if !isRunningUITests {
            try? KeychainStore.delete(account: StorageKey.tokenAccount(for: credentialServerAddress))
            try? KeychainStore.delete(account: StorageKey.legacyTokenAccount)
        }
        defaults.set(false, forKey: StorageKey.connectionValidated)
        defaults.removeObject(forKey: StorageKey.selectedWorkspaceID)
        defaults.removeObject(forKey: StorageKey.lastSuccessfulSyncAt)
        health = nil
        workspaces = []
        jobs = []
        graphJobStates = [:]
        selectedWorkspaceID = nil
        nextCursor = nil
        hasMoreJobs = false
        isDataStale = false
        hasPendingSync = false
        isUsingCachedData = false
        lastSuccessfulSyncAt = nil
        lastSyncFailureMessage = nil
        phase = .disconnected
        token = ""
        rotateCredentialCacheNamespace()
        Task { await cacheStore.advanceGeneration(to: generation, clearingExistingCache: true) }
    }

    func clearConnection() {
        let generation = invalidateDashboardRequests()
        connectionGeneration &+= 1
        defaults.removeObject(forKey: StorageKey.serverAddress)
        defaults.set(false, forKey: StorageKey.connectionValidated)
        defaults.removeObject(forKey: StorageKey.selectedWorkspaceID)
        defaults.removeObject(forKey: StorageKey.lastSuccessfulSyncAt)
        if !isRunningUITests {
            try? KeychainStore.delete(account: StorageKey.tokenAccount(for: credentialServerAddress))
            try? KeychainStore.delete(account: StorageKey.legacyTokenAccount)
        }
        serverAddress = Self.defaultServerAddress
        credentialServerAddress = Self.defaultServerAddress
        token = ""
        health = nil
        workspaces = []
        jobs = []
        graphJobStates = [:]
        selectedWorkspaceID = nil
        nextCursor = nil
        hasMoreJobs = false
        isDataStale = false
        hasPendingSync = false
        isUsingCachedData = false
        lastSuccessfulSyncAt = nil
        lastSyncFailureMessage = nil
        phase = .disconnected
        rotateCredentialCacheNamespace()
        Task { await cacheStore.advanceGeneration(to: generation, clearingExistingCache: true) }
    }

    func present(_ error: Error) {
        if let apiError = error as? APIError {
            presentedError = PresentedError(title: apiError.summary, detail: apiError.detail)
            return
        }
        presentedError = PresentedError(
            title: "操作失败",
            detail: String(describing: error)
        )
    }

    private func makeClient() throws -> APIClient {
        try APIClient(serverAddress: serverAddress, token: token)
    }

    private func seedUITestDashboard() {
        let now = Int64(Date().timeIntervalSince1970 * 1_000)
        serverAddress = "https://quartet.example.test/"
        token = ""
        health = HealthResponse(
            status: "ok",
            time: nil,
            buildTime: "UI Test",
            instanceId: "ios-e2e",
            authRequired: false
        )
        workspaces = [
            WorkspaceSummary(
                id: "ws-studio", version: 1, title: "Quartet Studio",
                description: "主工作空间", workdir: "/workspace/quartet",
                defaultAgent: "trae", defaultModel: "gpt-5.6", color: "18B8A7",
                favorite: true, sortOrder: 0, createdAt: now - 86_400_000, updatedAt: now
            ),
            WorkspaceSummary(
                id: "ws-lab", version: 1, title: "实验室",
                description: "实验任务", workdir: "/workspace/lab",
                defaultAgent: "trae", defaultModel: "gpt-5.4", color: "F2A83B",
                favorite: false, sortOrder: 1, createdAt: now - 43_200_000, updatedAt: now
            )
        ]
        uiTestJobs = [
            JobSummary(
                id: "job-chat-running", title: "优化 iOS 交互体验", modelId: "gpt-5.6",
                status: "running", mode: "interactive", workspaceId: "ws-studio",
                workdir: "/workspace/quartet", createdAt: now - 900_000, updatedAt: now - 15_000,
                pinnedAt: now - 800_000, sessionCount: 2, scheduleId: nil, shareToken: nil,
                agentId: "trae", acpMode: "default", acpThoughtLevel: "medium"
            ),
            JobSummary(
                id: "job-graph-waiting", title: "发布前检查流水线", modelId: nil,
                status: "awaitingInput", mode: "graph", workspaceId: "ws-studio",
                workdir: "/workspace/quartet", createdAt: now - 3_600_000, updatedAt: now - 120_000,
                pinnedAt: nil, sessionCount: 3, scheduleId: nil, shareToken: nil
            ),
            JobSummary(
                id: "job-lab-complete", title: "组件回归检查", modelId: "gpt-5.4",
                status: "completed", mode: "interactive", workspaceId: "ws-lab",
                workdir: "/workspace/lab", createdAt: now - 7_200_000, updatedAt: now - 1_800_000,
                pinnedAt: nil, sessionCount: 1, scheduleId: "nightly-ui", shareToken: nil,
                agentId: "trae", acpMode: "default", acpThoughtLevel: "medium"
            )
        ]
        jobs = filteredUITestJobs(workspaceID: nil)
        selectedWorkspaceID = nil
        lastSuccessfulSyncAt = Date()
        lastSyncFailureMessage = nil
        isDataStale = false
        hasPendingSync = false
        isUsingCachedData = false
        graphJobStates["job-graph-waiting"] = GraphJobState(
            status: "awaitingInput",
            lastError: nil,
            updatedAt: Date()
        )
        phase = .connected
    }

    private func uiTestUsageStats(from: String?, to: String?) -> UsageStatsReport {
        let formatter = DateFormatter()
        formatter.calendar = Calendar(identifier: .gregorian)
        formatter.locale = Locale(identifier: "en_US_POSIX")
        formatter.timeZone = .current
        formatter.dateFormat = "yyyy-MM-dd"
        let calendar = Calendar.current
        let today = calendar.startOfDay(for: Date())
        let dateKey: (Int) -> String = { offset in
            formatter.string(from: calendar.date(byAdding: .day, value: offset, to: today) ?? today)
        }
        let tokens = UsageStatsTokenTotals(total: 14_800, assistant: 5_300, thought: 4_100, toolCall: 2_200)
        let modelA = UsageStatsSectionTotals(
            totalMs: 2_160_000, turnCount: 18, assistantCount: 18, thoughtCount: 13,
            toolCallCount: 34, tokens: tokens
        )
        let modelB = UsageStatsSectionTotals(
            totalMs: 960_000, turnCount: 8, assistantCount: 8, thoughtCount: 6,
            toolCallCount: 12,
            tokens: UsageStatsTokenTotals(total: 6_200, assistant: 2_400, thought: 1_700, toolCall: 900)
        )
        let daily = [
            UsageStatsDailyRow(
                date: dateKey(-3), totalMs: 540_000, turnCount: 5, assistantCount: 5, thoughtCount: 4, toolCallCount: 8,
                tokens: UsageStatsTokenTotals(total: 4_100, assistant: 1_400, thought: 1_200, toolCall: 600),
                models: ["gpt-5.6": UsageStatsSectionTotals(
                    totalMs: 540_000, turnCount: 5, assistantCount: 5, thoughtCount: 4, toolCallCount: 8,
                    tokens: UsageStatsTokenTotals(total: 4_100, assistant: 1_400, thought: 1_200, toolCall: 600)
                )], modelNames: ["gpt-5.6": "gpt-5.6"]
            ),
            UsageStatsDailyRow(
                date: dateKey(-2), totalMs: 1_020_000, turnCount: 9, assistantCount: 9, thoughtCount: 7, toolCallCount: 14,
                tokens: UsageStatsTokenTotals(total: 7_000, assistant: 2_600, thought: 1_900, toolCall: 1_100),
                models: [
                    "gpt-5.6": UsageStatsSectionTotals(
                        totalMs: 660_000, turnCount: 6, assistantCount: 6, thoughtCount: 5, toolCallCount: 9,
                        tokens: UsageStatsTokenTotals(total: 4_500, assistant: 1_700, thought: 1_200, toolCall: 700)
                    ),
                    "gpt-5.4": UsageStatsSectionTotals(
                        totalMs: 360_000, turnCount: 3, assistantCount: 3, thoughtCount: 2, toolCallCount: 5,
                        tokens: UsageStatsTokenTotals(total: 2_500, assistant: 900, thought: 700, toolCall: 400)
                    )
                ],
                modelNames: ["gpt-5.6": "gpt-5.6", "gpt-5.4": "gpt-5.4"]
            ),
            UsageStatsDailyRow(
                date: dateKey(-1), totalMs: 780_000, turnCount: 6, assistantCount: 6, thoughtCount: 5, toolCallCount: 11,
                tokens: UsageStatsTokenTotals(total: 5_200, assistant: 2_100, thought: 1_400, toolCall: 700),
                models: ["gpt-5.4": UsageStatsSectionTotals(
                    totalMs: 780_000, turnCount: 6, assistantCount: 6, thoughtCount: 5, toolCallCount: 11,
                    tokens: UsageStatsTokenTotals(total: 5_200, assistant: 2_100, thought: 1_400, toolCall: 700)
                )], modelNames: ["gpt-5.4": "gpt-5.4"]
            ),
            UsageStatsDailyRow(
                date: dateKey(0), totalMs: 780_000, turnCount: 6, assistantCount: 6, thoughtCount: 3, toolCallCount: 13,
                tokens: UsageStatsTokenTotals(total: 4_700, assistant: 1_600, thought: 1_300, toolCall: 700),
                models: ["gpt-5.6": UsageStatsSectionTotals(
                    totalMs: 780_000, turnCount: 6, assistantCount: 6, thoughtCount: 3, toolCallCount: 13,
                    tokens: UsageStatsTokenTotals(total: 4_700, assistant: 1_600, thought: 1_300, toolCall: 700)
                )], modelNames: ["gpt-5.6": "gpt-5.6"]
            )
        ]
        return UsageStatsReport(
            range: UsageStatsRange(from: from ?? dateKey(-29), to: to ?? dateKey(0)),
            byWorkspace: [
                UsageStatsWorkspaceRow(
                    workspaceId: "ws-studio", workspaceName: "Quartet Studio", deleted: false,
                    totalMs: 2_160_000, turnCount: 18, assistantCount: 18, thoughtCount: 13, toolCallCount: 34, tokens: tokens
                ),
                UsageStatsWorkspaceRow(
                    workspaceId: "ws-lab", workspaceName: "实验室", deleted: false,
                    totalMs: 960_000, turnCount: 8, assistantCount: 8, thoughtCount: 6, toolCallCount: 12, tokens: modelB.tokens
                )
            ],
            byModel: [
                UsageStatsModelRow(
                    modelId: "gpt-5.6", modelName: "gpt-5.6", totalMs: modelA.totalMs, turnCount: modelA.turnCount,
                    assistantCount: modelA.assistantCount, thoughtCount: modelA.thoughtCount, toolCallCount: modelA.toolCallCount, tokens: modelA.tokens
                ),
                UsageStatsModelRow(
                    modelId: "gpt-5.4", modelName: "gpt-5.4", totalMs: modelB.totalMs, turnCount: modelB.turnCount,
                    assistantCount: modelB.assistantCount, thoughtCount: modelB.thoughtCount, toolCallCount: modelB.toolCallCount, tokens: modelB.tokens
                )
            ],
            byTool: [
                UsageStatsToolRow(toolKey: "exec_command", count: 21, totalMs: 780_000),
                UsageStatsToolRow(toolKey: "apply_patch", count: 15, totalMs: 480_000),
                UsageStatsToolRow(toolKey: "view_image", count: 10, totalMs: 240_000)
            ],
            daily: daily,
            previous: UsageStatsPreviousTotals(
                totalMs: 2_640_000, turnCount: 22, toolCallCount: 38, tokensTotal: 17_400, workspaceCount: 2
            ),
            note: "stats.tokensLocalEstimateNote", failed: false, error: nil
        )
    }

    private func uiTestJobDetail(id: String) throws -> JobDetail {
        let summary = uiTestJobs.first(where: { $0.id == id }) ?? uiTestJobs[0]
        let json = """
        {
          "id": "\(summary.id)",
          "title": "\(summary.displayTitle)",
          "status": "\(displayedStatus(for: summary))",
          "mode": "\(summary.mode ?? "interactive")",
          "workspaceId": "\(summary.workspaceId ?? "ws-studio")",
          "workdir": "\(summary.workdir ?? "/workspace/quartet")",
          "createdAt": "2026-08-22T10:00:00Z",
          "updatedAt": "2026-08-22T12:00:00Z",
          "sessionIds": ["session-1"],
          "graphSessionIds": [],
          "firstModelId": "\(summary.modelId ?? "gpt-5.6")",
          "initialAgentId": "trae",
          "lastEventSeq": 42
        }
        """
        return try JSONDecoder().decode(JobDetail.self, from: Data(json.utf8))
    }

    private func filteredUITestJobs(workspaceID: String?) -> [JobSummary] {
        uiTestJobs.filter { job in
            let matchesWorkspace = workspaceID == nil || job.workspaceId == workspaceID
            let isScheduled = job.scheduleId?.isEmpty == false
            return matchesWorkspace && (!hideScheduledJobs || !isScheduled)
        }
    }

    private func uiTestGraphRunStatus(jobID: String) -> GraphRunStatusResponse {
        let progress = GraphProgressSummary(
            totalCount: 4, completedCount: 2, failedCount: 0, skippedCount: 0,
            interruptedCount: 0, runningCount: 0, lastError: nil
        )
        return GraphRunStatusResponse(
            run: GraphRunSummary(
                id: "run-e2e", workflowId: "release-check", jobId: jobID,
                workspaceId: "ws-studio", status: "awaitingInput", currentVersion: 3,
                startedAt: Int64(Date().timeIntervalSince1970 * 1_000) - 180_000,
                finishedAt: nil, lastError: nil, progress: progress
            ),
            progress: progress,
            instances: [
                GraphInstanceSummary(
                    key: GraphInstanceKeySummary(nodeId: "review", iterations: nil),
                    nodeId: "review", nodeTitle: "人工确认发布", nodeType: "clarify",
                    status: "awaitingInput", version: 3, sessionId: "session-review",
                    displaySessionId: "session-review", startedAt: nil, finishedAt: nil,
                    durationMs: nil, error: nil, blockedReason: "等待确认发布范围"
                )
            ]
        )
    }

    private func isCurrentConnectionRequest(
        generation: UInt64,
        client: APIClient,
        requestedToken: String
    ) -> Bool {
        generation == connectionGeneration
            && StorageKey.connectionIdentity(for: serverAddress) == client.baseURL.absoluteString
            && token.trimmingCharacters(in: .whitespacesAndNewlines) == requestedToken
    }

    private func finishSupersededConnectionIfNeeded(generation: UInt64) {
        if generation == connectionGeneration, phase == .connecting {
            phase = .disconnected
            hasPendingSync = false
        }
    }

    private func applyFirstPage(_ response: JobsPage) {
        jobs = response.jobs
        nextCursor = response.nextCursor
        hasMoreJobs = response.hasMore
    }

    private func applyPolledFirstPage(_ response: JobsPage) {
        let previousJobs = jobs
        let previousByID = Dictionary(uniqueKeysWithValues: previousJobs.map { ($0.id, $0) })
        let fetchedIDs = Set(response.jobs.map(\.id))
        var merged = response.jobs.map { fresh in
            guard let existing = previousByID[fresh.id] else { return fresh }
            return existing == fresh ? existing : fresh
        }

        if response.hasMore, let cutoff = response.jobs.last {
            for existing in previousJobs where !fetchedIDs.contains(existing.id) {
                if !jobComesBefore(existing, cutoff) {
                    merged.append(existing)
                }
            }
        }
        merged.sort(by: jobComesBefore)
        jobs = merged

        let hadLoadedAdditionalPages = previousJobs.count > 100
        if !hadLoadedAdditionalPages || !response.hasMore {
            nextCursor = response.nextCursor
            hasMoreJobs = response.hasMore
        }
    }

    private func jobComesBefore(_ lhs: JobSummary, _ rhs: JobSummary) -> Bool {
        let lhsPinnedAt = lhs.pinnedAt ?? 0
        let rhsPinnedAt = rhs.pinnedAt ?? 0
        let lhsPinned = lhsPinnedAt > 0
        let rhsPinned = rhsPinnedAt > 0
        if lhsPinned != rhsPinned { return lhsPinned }
        if lhsPinnedAt != rhsPinnedAt { return lhsPinnedAt > rhsPinnedAt }
        if lhs.updatedAt != rhs.updatedAt { return lhs.updatedAt > rhs.updatedAt }
        return lhs.id < rhs.id
    }

    private func beginDashboardRequest() -> UInt64 {
        dashboardGeneration &+= 1
        dashboardRefreshTask?.cancel()
        loadMoreTask?.cancel()
        dashboardRefreshTask = nil
        loadMoreTask = nil
        isLoadingMore = false
        return dashboardGeneration
    }

    @discardableResult
    private func invalidateDashboardRequests() -> UInt64 {
        let generation = beginDashboardRequest()
        isRefreshing = false
        return generation
    }

    private func isCurrentDashboardGeneration(_ generation: UInt64) -> Bool {
        generation == dashboardGeneration && !Task.isCancelled
    }

    private func isCurrentDashboardRequest(generation: UInt64, workspaceID: String?) -> Bool {
        isCurrentDashboardGeneration(generation) && selectedWorkspaceID == workspaceID
    }

    private func isCurrentPageRequest(
        generation: UInt64,
        workspaceID: String?,
        cursor: String
    ) -> Bool {
        isCurrentDashboardRequest(generation: generation, workspaceID: workspaceID)
            && nextCursor == cursor
    }

    private func dashboardCacheSnapshot() -> DashboardCacheSnapshot {
        DashboardCacheSnapshot(
            workspaces: workspaces,
            jobs: jobs.map(CachedJobSummary.init),
            selectedWorkspaceID: selectedWorkspaceID,
            serverAddress: serverAddress,
            credentialNamespace: credentialCacheNamespace,
            savedAt: lastSuccessfulSyncAt ?? Date()
        )
    }

    private func loadCachedDashboardIfNeeded() async {
        guard !hasLoadedCachedDashboard else { return }
        hasLoadedCachedDashboard = true
        guard let snapshot = await cacheStore.load() else { return }
        guard StorageKey.connectionIdentity(for: snapshot.serverAddress) == StorageKey.connectionIdentity(for: serverAddress) else {
            await cacheStore.clear()
            return
        }
        guard snapshot.credentialNamespace == credentialCacheNamespace else {
            await cacheStore.clear()
            return
        }
        guard snapshot.selectedWorkspaceID == selectedWorkspaceID else {
            await cacheStore.clear()
            return
        }
        workspaces = snapshot.workspaces
        let cachedJobs = snapshot.jobs.map(\.jobSummary)
        jobs = hideScheduledJobs
            ? cachedJobs.filter { $0.scheduleId?.isEmpty != false }
            : cachedJobs
        isUsingCachedData = true
        isDataStale = true
        hasPendingSync = true
        if lastSuccessfulSyncAt == nil {
            lastSuccessfulSyncAt = snapshot.savedAt
        }
    }

    private func markSyncSucceeded() {
        phase = .connected
        let now = Date()
        lastSuccessfulSyncAt = now
        defaults.set(now.timeIntervalSince1970, forKey: StorageKey.lastSuccessfulSyncAt)
        lastSyncFailureMessage = nil
        isDataStale = false
        hasPendingSync = false
        isUsingCachedData = false
    }

    private func handleSyncFailure(
        _ error: Error,
        presentToUser: Bool,
        disconnect: Bool
    ) async {
        if disconnect {
            phase = .disconnected
        } else {
            phase = .connected
        }
        if hasDashboardContent {
            isDataStale = true
            hasPendingSync = true
        }
        let failureMessage: String
        if let apiError = error as? APIError {
            failureMessage = "\(apiError.summary)\n\n\(apiError.detail)"
        } else {
            failureMessage = String(describing: error)
        }
        lastSyncFailureMessage = failureMessage
        if presentToUser {
            present(error)
        }
    }

    private func applyGraphStatus(
        job: JobSummary,
        response: GraphRunStatusResponse,
        graphStates: inout [String: GraphJobState]
    ) {
        guard let run = response.run else { return }
        let jobID = job.id
        let state = GraphJobState(
            status: run.status,
            lastError: run.lastError?.fullDetail ?? response.progress?.lastError,
            updatedAt: Date()
        )
        graphStates[jobID] = state
    }

    private func applyGraphStatus(job: JobSummary, response: GraphRunStatusResponse) {
        var updatedGraphStates = graphJobStates
        applyGraphStatus(job: job, response: response, graphStates: &updatedGraphStates)
        if graphJobStates != updatedGraphStates {
            graphJobStates = updatedGraphStates
        }
    }

    private func statusColorKey(_ status: String) -> String {
        switch status {
        case "running", "pending", "awaitingInput":
            return "running"
        case "completed":
            return "completed"
        case "failed", "timedOut":
            return "failed"
        default:
            return "stopped"
        }
    }

    private func statusLabel(_ status: String) -> String {
        switch status {
        case "pending": "等待中"
        case "running": "运行中"
        case "awaitingInput": "等待人工"
        case "stepStopping": "步骤后停止中"
        case "stepStopped": "已在步骤后停止"
        case "completed": "已完成"
        case "failed": "失败"
        case "timedOut": "已超时"
        case "stopped": "已停止"
        default: status
        }
    }

    private static func loadStoredToken(
        for serverAddress: String,
        migrateLegacyCredential: Bool
    ) -> String {
        let account = StorageKey.tokenAccount(for: serverAddress)
        if let token = try? KeychainStore.read(account: account) {
            return token
        }
        guard migrateLegacyCredential,
              let legacyToken = try? KeychainStore.read(account: StorageKey.legacyTokenAccount) else {
            return ""
        }
        try? KeychainStore.write(legacyToken, account: account)
        try? KeychainStore.delete(account: StorageKey.legacyTokenAccount)
        return legacyToken
    }

    private func rotateCredentialCacheNamespace() {
        credentialCacheNamespace = UUID().uuidString
        defaults.set(credentialCacheNamespace, forKey: StorageKey.credentialCacheNamespace)
    }

    private var hasDashboardContent: Bool {
        !workspaces.isEmpty || !jobs.isEmpty
    }

    private func sentMessageHistoryScope(workspaceID: String?) -> String {
        let server = StorageKey.connectionIdentity(for: serverAddress) ?? serverAddress
        return "\(server)|\(workspaceID ?? "default")"
    }

    private enum StorageKey {
        static let serverAddress = "quartet.serverAddress"
        static let connectionValidated = "quartet.connectionValidated"
        static let selectedWorkspaceID = "quartet.selectedWorkspaceID"
        static let hideScheduledJobs = "quartet.hideScheduledJobs"
        static let lastSuccessfulSyncAt = "quartet.lastSuccessfulSyncAt"
        static let credentialCacheNamespace = "quartet.credentialCacheNamespace"
        static let legacyTokenAccount = "agent-auth-token"

        static func connectionIdentity(for serverAddress: String) -> String? {
            guard let client = try? APIClient(serverAddress: serverAddress, token: "") else {
                return nil
            }
            return client.baseURL.absoluteString
        }

        static func tokenAccount(for serverAddress: String) -> String {
            "agent-auth-token|\(connectionIdentity(for: serverAddress) ?? "invalid-server")"
        }
    }

    private struct DashboardPollScope: Equatable {
        let connectionIdentity: String?
        let workspaceID: String?
        let excludeScheduled: Bool
    }
}
