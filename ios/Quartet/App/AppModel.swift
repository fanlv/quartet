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
        /// `JobSummary.updatedAt` of the newest Job record this Graph status was observed against.
        /// The detailed Graph status is only trusted while the Job record has not advanced past it:
        /// every Graph transition also rewrites the Job (bumping `updatedAt`), so a newer Job record
        /// means this entry describes a superseded run state and must not override `job.status`.
        let jobUpdatedAt: Int64
        let updatedAt: Date
    }

    private struct OptimisticJobExecution {
        let baseline: JobSummary
        let display: JobSummary
        let startedAt: Date
    }

    static let defaultServerAddress = "https://devbox.fanlv.fun/"

    @Published var phase: ConnectionPhase = .booting
    @Published var serverAddress: String {
        didSet {
            guard StorageKey.connectionIdentity(for: serverAddress) != StorageKey.connectionIdentity(for: oldValue) else {
                return
            }
            resolvedServerAddress = StorageKey.connectionIdentity(for: serverAddress)
            hasResolvedServerAddress = false
            defaults.removeObject(forKey: StorageKey.resolvedServerAddress)
            defaults.removeObject(forKey: StorageKey.resolvedServerAddressEntryIdentity)
            connectionGeneration &+= 1
            _ = invalidateDashboardRequests()
            if phase == .connecting {
                phase = .disconnected
            }
            username = ""
            password = ""
            csrfToken = ""
            permissions = []
            agentCatalogSnapshot = []
        }
    }
    @Published var username: String = ""
    @Published var password: String = ""
    @Published private(set) var health: HealthResponse?
    @Published private(set) var workspaces: [WorkspaceSummary] = []
    @Published private(set) var jobs: [JobSummary] = []
    @Published private(set) var agentCatalogSnapshot: [AgentSummary] = []
    @Published private(set) var selectedWorkspaceID: String?
    @Published private(set) var hideScheduledJobs: Bool
    @Published private(set) var appLanguage: AppLanguage
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
    @Published private(set) var permissions: Set<String> = []

    private let defaults: UserDefaults
    private let cacheStore: DashboardCacheStore
    private let sentMessageHistoryStore: SentMessageHistoryStore
    private let uiTestScenario: String?
    private var csrfToken: String = ""
    private var resolvedServerAddress: String?
    private var hasResolvedServerAddress = false
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
#if DEBUG
    private var uiTestUpgradedAgentIDs: Set<String> = []
#endif
    private var optimisticJobExecutions: [String: OptimisticJobExecution] = [:]

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
        if let storedLanguage = effectiveDefaults.string(forKey: StorageKey.appLanguage),
           let language = AppLanguage(rawValue: storedLanguage) {
            appLanguage = language
        } else {
            // Existing UI tests assert the original Chinese copy. Keep their fixture
            // deterministic while production installs follow the device by default.
            appLanguage = processArguments.contains("--ui-testing-language-en")
                ? .english
                : (detectedUITestScenario == nil ? .system : .simplifiedChinese)
        }
        let storedServerAddress = effectiveDefaults.string(forKey: StorageKey.serverAddress) ?? Self.defaultServerAddress
        let storedServerIdentity = StorageKey.connectionIdentity(for: storedServerAddress)
        let storedResolvedServerAddress = effectiveDefaults.string(forKey: StorageKey.resolvedServerAddress)
        let storedResolvedEntryIdentity = effectiveDefaults.string(forKey: StorageKey.resolvedServerAddressEntryIdentity)
        let storedCredentialNamespace = effectiveDefaults.string(forKey: StorageKey.credentialCacheNamespace) ?? UUID().uuidString
        serverAddress = storedServerAddress
        if storedResolvedEntryIdentity == storedServerIdentity,
           let storedResolvedServerAddress,
           let normalizedResolvedAddress = StorageKey.connectionIdentity(for: storedResolvedServerAddress) {
            resolvedServerAddress = normalizedResolvedAddress
            hasResolvedServerAddress = true
        } else {
            resolvedServerAddress = storedServerIdentity
        }
        credentialCacheNamespace = storedCredentialNamespace
        effectiveDefaults.set(storedCredentialNamespace, forKey: StorageKey.credentialCacheNamespace)
        username = effectiveDefaults.string(forKey: StorageKey.username) ?? ""
        if detectedUITestScenario == nil {
            try? KeychainStore.delete(account: StorageKey.legacyTokenAccount)
            try? KeychainStore.delete(account: StorageKey.legacyTokenAccount(for: storedServerAddress))
        }
        selectedWorkspaceID = effectiveDefaults.string(forKey: StorageKey.selectedWorkspaceID)
        if let timestamp = effectiveDefaults.object(forKey: StorageKey.lastSuccessfulSyncAt) as? Double {
            lastSuccessfulSyncAt = Date(timeIntervalSince1970: timestamp)
        }
    }

    var activeJobs: [JobSummary] {
        jobs.filter { job in
            let status = displayedStatus(for: job)
            return status == "running" || status == "pending" || status == "stepStopping"
        }
    }

    var activeJobCount: Int {
        activeJobs.count
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
            username = ""
            password = ""
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
        _ = invalidateDashboardRequests()
        let requestedServerAddress = serverAddress
        let requestedUsername = username.trimmingCharacters(in: .whitespacesAndNewlines)
        let hadRequestedCredentials = !requestedUsername.isEmpty && !password.isEmpty
        phase = .connecting
        hasPendingSync = true
        do {
            let entryClient = try APIClient(serverAddress: requestedServerAddress)
            let resolvedBaseURL: URL
            if hasResolvedServerAddress, let resolvedServerAddress {
                resolvedBaseURL = try APIClient(serverAddress: resolvedServerAddress).baseURL
            } else {
                resolvedBaseURL = try await entryClient.resolvedBaseURL()
            }
            guard isCurrentConnectionRequest(
                generation: generation,
                requestedServerAddress: requestedServerAddress,
                requestedUsername: requestedUsername
            ) else {
                finishSupersededConnectionIfNeeded(generation: generation)
                return
            }
            resolvedServerAddress = resolvedBaseURL.absoluteString
            let client = try makeClient(
                serverAddress: resolvedBaseURL.absoluteString,
                notifyUnauthorized: false
            )
            let health = try await client.health()
            guard isCurrentConnectionRequest(
                generation: generation,
                requestedServerAddress: requestedServerAddress,
                requestedUsername: requestedUsername
            ) else {
                finishSupersededConnectionIfNeeded(generation: generation)
                return
            }
            guard health.authState == "ready" else {
                let stateDetail = health.authError.flatMap { $0.isEmpty ? nil : $0 }.map { "\n\n\($0)" } ?? ""
                throw APIError(
                    summary: health.authState == "uninitialized" ? "Quartet 尚未初始化" : "认证配置需要恢复",
                    detail: "请先在 Web 页面完成管理员初始化或恢复认证配置。\(stateDetail)"
                )
            }
            let principal: AuthPrincipal
            do {
                principal = try await client.currentUser()
            } catch {
                guard !requestedUsername.isEmpty, !password.isEmpty else { throw error }
                principal = try await client.login(username: requestedUsername, password: password)
            }
            guard isCurrentConnectionRequest(
                generation: generation,
                requestedServerAddress: requestedServerAddress,
                requestedUsername: requestedUsername
            ) else {
                finishSupersededConnectionIfNeeded(generation: generation)
                return
            }
            if principal.user.mustChangePassword {
                throw APIError(summary: "需要修改密码", detail: "该账号使用临时密码，请先在 Web 页面修改密码后再连接 iOS。")
            }
            serverAddress = entryClient.baseURL.absoluteString
            resolvedServerAddress = client.baseURL.absoluteString
            hasResolvedServerAddress = true
            csrfToken = principal.csrfToken
            permissions = Set(principal.permissions)
            username = principal.user.username
            password = ""
            defaults.set(serverAddress, forKey: StorageKey.serverAddress)
            defaults.set(resolvedServerAddress, forKey: StorageKey.resolvedServerAddress)
            defaults.set(
                StorageKey.connectionIdentity(for: serverAddress),
                forKey: StorageKey.resolvedServerAddressEntryIdentity
            )
            defaults.set(username, forKey: StorageKey.username)
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
            if let apiError = error as? APIError,
               apiError.httpStatusCode == 401,
               !hadRequestedCredentials {
                await handleUnauthorized(
                    apiError,
                    requestGeneration: generation,
                    requestConnectionIdentity: StorageKey.connectionIdentity(for: serverAddress)
                )
                return
            }
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

    func setAppLanguage(_ language: AppLanguage) {
        guard appLanguage != language else { return }
        defaults.set(language.rawValue, forKey: StorageKey.appLanguage)
        appLanguage = language
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

    func shareJob(id: String) async throws -> String {
#if DEBUG
        if isRunningUITests {
            return "ui-test-share-token"
        }
#endif
        return try await makeClient().shareJob(id: id).shareToken
    }

    func graphRunStatus(jobID: String) async throws -> GraphRunStatusResponse {
        if isRunningUITests {
            return uiTestGraphRunStatus(jobID: jobID)
        }
        return try await makeClient().graphRunStatus(jobID: jobID)
    }

    func updateGraphRunVersion(jobID: String, config: GraphConfig) async throws -> GraphRunActionResponse {
#if DEBUG
        if isRunningUITests {
            return GraphRunActionResponse(run: uiTestGraphRunStatus(jobID: jobID).run)
        }
#endif
        return try await makeClient().updateGraphRunVersion(jobID: jobID, config: config)
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
           current.jobUpdatedAt >= job.updatedAt,
           Date().timeIntervalSince(current.updatedAt) < 10 {
            return
        }
        await refreshGraphStatus(jobID: job.id)
    }

    func jobSummary(id: String) -> JobSummary? {
        jobs.first(where: { $0.id == id })
    }

    /// Mirrors an authoritative title observed by a Job screen into the shared dashboard state.
    /// The optimistic copies must move together with the visible row; otherwise a later execution
    /// reconciliation or cache save can restore the placeholder title used when the Job was created.
    func synchronizeJobTitle(id: String, title: String, fallback: JobSummary) {
        guard !title.isEmpty else { return }

        var fallbackForVisibleList = fallback.updating(title: title)
        if let optimistic = optimisticJobExecutions[id] {
            let synchronizedDisplay = optimistic.display.updating(title: title)
            optimisticJobExecutions[id] = OptimisticJobExecution(
                baseline: optimistic.baseline.updating(title: title),
                display: synchronizedDisplay,
                startedAt: optimistic.startedAt
            )
            fallbackForVisibleList = synchronizedDisplay
        }

        if let index = jobs.firstIndex(where: { $0.id == id }) {
            guard jobs[index].title != title else { return }
            jobs[index] = jobs[index].updating(title: title)
        } else {
            upsertVisibleJob(fallbackForVisibleList)
        }
    }

    func displayedStatus(for job: JobSummary) -> String {
        if optimisticJobExecutions[job.id] != nil {
            return "running"
        }
        guard let graphState = graphJobStates[job.id],
              graphState.jobUpdatedAt >= job.updatedAt else {
            return job.status
        }
        return graphState.status
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
        let catalog: [AgentSummary]
        if isRunningUITests {
            catalog = [
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
                        availableThoughtLevels: [
                            AgentOption(id: "low", name: "快速", description: nil),
                            AgentOption(id: "medium", name: "标准", description: nil),
                            AgentOption(id: "high", name: "深入", description: nil)
                        ],
                        currentThoughtLevelId: "medium"
                    )
                )
            ]
        } else {
            catalog = try await makeClient().agents().agentList
        }
        agentCatalogSnapshot = catalog
        return catalog
    }

    func refreshAgentCatalog() async {
        do {
            _ = try await agentCatalog()
        } catch {
            present(error)
        }
    }

    func managedAgentCatalogItems() async throws -> [AgentCatalogItem] {
#if DEBUG
        if isRunningUITests { return try uiTestManagedAgentCatalogItems() }
#endif
        let client = try makeClient()
        async let active = client.agentCatalogItems()
        async let deleted = client.deletedAgentCatalogItems()
        let (activeResponse, deletedResponse) = try await (active, deleted)
        return (activeResponse.agents ?? []) + (deletedResponse.agents ?? [])
    }

    func managedAgentVersions(force: Bool) async throws -> AgentVersionCheckResponse {
#if DEBUG
        if isRunningUITests { return try uiTestManagedAgentVersions() }
#endif
        return try await makeClient().agentVersionCheck(force: force)
    }

    func upgradeManagedAgent(agentID: String, client: APIClient? = nil) async throws -> AgentInstallResponse {
#if DEBUG
        if isRunningUITests { return try uiTestManagedAgentUpgrade(agentID: agentID) }
#endif
        return try await (client ?? makeClient()).upgradeAgent(agentID: agentID)
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

    func agentEnvironmentSettings() async throws -> [String: [AgentEnvironmentItem]] {
        if isRunningUITests { return [:] }
        let response = try await makeClient().agentPreferences()
        guard response.code == 0 else {
            throw APIError(
                summary: "无法读取 Agent 环境变量",
                detail: "GET /api/v1/config/settings/get 返回 code=\(response.code)。"
            )
        }
        return response.settings?.acpEnvVars ?? [:]
    }

    func effectiveMessagePresets(workspaceID: String) async throws -> EffectiveMessagePresetsResponse {
        if isRunningUITests {
            return EffectiveMessagePresetsResponse(
                code: 0,
                workspaceId: workspaceID,
                project: [
                    MessagePreset(
                        id: "ios-ui-test-project-preset",
                        name: "检查当前改动",
                        content: "请检查当前工作区的改动并给出风险清单。"
                    )
                ],
                global: [
                    MessagePreset(
                        id: "ios-ui-test-global-preset",
                        name: "总结进展",
                        content: "请总结当前进展、遗留问题和下一步建议。"
                    )
                ],
                errors: []
            )
        }
        let response = try await makeClient().effectiveMessagePresets(workspaceID: workspaceID)
        guard response.code == 0 else {
            throw APIError(
                summary: "无法读取预置消息",
                detail: "GET /api/v1/config/message-presets/effective?workspaceId=\(workspaceID) 返回 code=\(response.code)。"
            )
        }
        return response
    }

    func relinkACPThoughtLevels(agentType: String, modelID: String) async throws -> AgentThoughtLevelState {
        if isRunningUITests {
            return AgentThoughtLevelState(
                availableThoughtLevels: [AgentOption(id: "medium", name: "标准", description: nil)],
                currentThoughtLevelId: "medium"
            )
        }
        let response = try await makeClient().setACPConfig(SetACPConfigRequest(
            target: .model,
            agentType: agentType,
            model: modelID
        ))
        guard response.code == 0 else {
            throw APIError(
                summary: "无法刷新思考等级",
                detail: "POST /api/v1/agent/config 返回 code=\(response.code)。"
            )
        }
        return response.thoughtLevels ?? AgentThoughtLevelState(
            availableThoughtLevels: [],
            currentThoughtLevelId: ""
        )
    }

    func setACPConfig(_ request: SetACPConfigRequest) async throws -> SetACPConfigResponse {
        if isRunningUITests {
            let models = AgentModelState(
                availableModels: [
                    AgentModel(modelId: "gpt-5.6", name: "GPT-5.6", description: "默认模型"),
                    AgentModel(modelId: "gpt-5.4", name: "GPT-5.4", description: "快速模型")
                ],
                currentModelId: request.model ?? "gpt-5.6"
            )
            let thoughtLevels = AgentThoughtLevelState(
                availableThoughtLevels: [
                    AgentOption(id: "low", name: "快速", description: nil),
                    AgentOption(id: "medium", name: "标准", description: nil),
                    AgentOption(id: "high", name: "深入", description: nil)
                ],
                currentThoughtLevelId: request.thoughtLevel ?? "medium"
            )
            return SetACPConfigResponse(
                code: 0,
                models: request.target == .model ? models : nil,
                modes: nil,
                thoughtLevels: thoughtLevels
            )
        }
        let response = try await makeClient().setACPConfig(request)
        guard response.code == 0 else {
            throw APIError(
                summary: "无法切换 Agent 配置",
                detail: "POST /api/v1/agent/config 返回 code=\(response.code)。"
            )
        }
        return response
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

    func beginOptimisticJobExecution(id: String, fallback: JobSummary? = nil) {
        guard let current = jobs.first(where: { $0.id == id }) ?? fallback else { return }
        let now = Int64(Date().timeIntervalSince1970 * 1_000)
        let baseline = optimisticJobExecutions[id]?.baseline ?? current
        let display = current.updating(status: "running", updatedAt: max(current.updatedAt, now))
        optimisticJobExecutions[id] = OptimisticJobExecution(
            baseline: baseline,
            display: display,
            startedAt: Date()
        )
        upsertVisibleJob(display)
        hasPendingSync = true
    }

    func cancelOptimisticJobExecution(id: String) {
        guard let optimistic = optimisticJobExecutions.removeValue(forKey: id) else { return }
        if shouldDisplayJob(optimistic.baseline) {
            upsertVisibleJob(optimistic.baseline)
        } else {
            jobs.removeAll { $0.id == id }
        }
        hasPendingSync = isDataStale || !optimisticJobExecutions.isEmpty
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

    var lastSentMessageWorkspaceID: String? {
        defaults.string(forKey: StorageKey.lastSentMessageWorkspaceID(for: serverAddress))
    }

    var newConversationDraft: String {
        defaults.string(
            forKey: StorageKey.newConversationDraft(for: serverAddress, username: username)
        ) ?? ""
    }

    func saveNewConversationDraft(_ content: String) {
        let key = StorageKey.newConversationDraft(for: serverAddress, username: username)
        if content.isEmpty {
            defaults.removeObject(forKey: key)
        } else {
            defaults.set(content, forKey: key)
        }
    }

    func clearNewConversationDraft() {
        defaults.removeObject(
            forKey: StorageKey.newConversationDraft(for: serverAddress, username: username)
        )
    }

    /// Graph 启动页记住的运行空间，和聊天页的最近发送空间分开：两条流程的空间选择互不影响。
    var lastGraphWorkspaceID: String? {
        defaults.string(forKey: StorageKey.lastGraphWorkspaceID(for: serverAddress))
    }

    func recordGraphWorkspace(_ workspaceID: String) {
        guard !workspaceID.isEmpty else { return }
        defaults.set(workspaceID, forKey: StorageKey.lastGraphWorkspaceID(for: serverAddress))
    }

    /// 文件浏览 tab 记住的工作空间，和运行台筛选、Graph 启动页都分开：浏览文件不该改动任务列表的筛选。
    var lastFilesWorkspaceID: String? {
        defaults.string(forKey: StorageKey.lastFilesWorkspaceID(for: serverAddress))
    }

    func recordFilesWorkspace(_ workspaceID: String) {
        guard !workspaceID.isEmpty else { return }
        defaults.set(workspaceID, forKey: StorageKey.lastFilesWorkspaceID(for: serverAddress))
    }

    @discardableResult
    func recordSentMessage(_ content: String, workspaceID: String?) throws -> [SentMessageHistoryItem] {
        let scope = sentMessageHistoryScope(workspaceID: workspaceID)
        do {
            let history = try sentMessageHistoryStore.append(content: content, scope: scope)
            if let workspaceID, !workspaceID.isEmpty {
                defaults.set(
                    workspaceID,
                    forKey: StorageKey.lastSentMessageWorkspaceID(for: serverAddress)
                )
            }
            return history
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

    func can(_ permission: String) -> Bool {
        isRunningUITests || permissions.contains(permission)
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
                if self.phase == .disconnected || self.phase == .connected {
                    await connect()
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

    func logout() {
        Task { await leaveConnection() }
    }

    private func leaveConnection(revokeServerSession: Bool = true) async {
        var logoutError: Error?
        if revokeServerSession, phase == .connected, !isRunningUITests {
            do {
                try await makeClient(notifyUnauthorized: false).logout()
            } catch {
                logoutError = error
            }
        }
        let generation = invalidateDashboardRequests()
        connectionGeneration &+= 1
        clearSessionCookies(for: serverAddress)
        if let resolvedServerAddress {
            clearSessionCookies(for: resolvedServerAddress)
        }
        resolvedServerAddress = StorageKey.connectionIdentity(for: serverAddress)
        hasResolvedServerAddress = false
        defaults.removeObject(forKey: StorageKey.resolvedServerAddress)
        defaults.removeObject(forKey: StorageKey.resolvedServerAddressEntryIdentity)
        defaults.set(false, forKey: StorageKey.connectionValidated)
        defaults.removeObject(forKey: StorageKey.selectedWorkspaceID)
        defaults.removeObject(forKey: StorageKey.lastSuccessfulSyncAt)
        health = nil
        workspaces = []
        jobs = []
        graphJobStates = [:]
        optimisticJobExecutions = [:]
        selectedWorkspaceID = nil
        nextCursor = nil
        hasMoreJobs = false
        isDataStale = false
        hasPendingSync = false
        isUsingCachedData = false
        lastSuccessfulSyncAt = nil
        lastSyncFailureMessage = nil
        phase = .disconnected
        password = ""
        csrfToken = ""
        permissions = []
        rotateCredentialCacheNamespace()
        Task { await cacheStore.advanceGeneration(to: generation, clearingExistingCache: true) }
        if let logoutError {
            present(logoutError)
        }
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

    private func makeClient(
        serverAddress clientServerAddress: String? = nil,
        notifyUnauthorized: Bool = true
    ) throws -> APIClient {
        let effectiveServerAddress = clientServerAddress ?? resolvedServerAddress ?? serverAddress
        let requestGeneration = connectionGeneration
        let requestConnectionIdentity = StorageKey.connectionIdentity(for: serverAddress)
        let unauthorizedHandler: (@MainActor @Sendable (APIError) async -> Void)?
        if notifyUnauthorized {
            unauthorizedHandler = { [weak self] error in
                guard let self else { return }
                await self.handleUnauthorized(
                    error,
                    requestGeneration: requestGeneration,
                    requestConnectionIdentity: requestConnectionIdentity
                )
            }
        } else {
            unauthorizedHandler = nil
        }
        return try APIClient(
            serverAddress: effectiveServerAddress,
            csrfToken: csrfToken,
            onUnauthorized: unauthorizedHandler
        )
    }

    private func handleUnauthorized(
        _ error: APIError,
        requestGeneration: UInt64,
        requestConnectionIdentity: String?
    ) async {
        guard requestGeneration == connectionGeneration,
              requestConnectionIdentity == StorageKey.connectionIdentity(for: serverAddress) else {
            return
        }
        await leaveConnection(revokeServerSession: false)
        present(error)
    }

    private func seedUITestDashboard() {
        let now = Int64(Date().timeIntervalSince1970 * 1_000)
        serverAddress = "https://quartet.example.test/"
        resolvedServerAddress = serverAddress
        hasResolvedServerAddress = true
        username = "admin"
        password = ""
        health = HealthResponse(
            status: "ok",
            time: nil,
            buildTime: "UI Test",
            instanceId: "ios-e2e",
            authState: "ready",
            authError: nil
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
        if uiTestScenario == "--ui-testing-docked-tabbar" {
            uiTestJobs.append(contentsOf: (1...14).map { index in
                JobSummary(
                    id: "job-tabbar-\(index)",
                    title: String(format: "吸底栏滚动验证 %02d", index),
                    modelId: "gpt-5.6",
                    status: "completed",
                    mode: "interactive",
                    workspaceId: "ws-studio",
                    workdir: "/workspace/quartet",
                    createdAt: now - Int64(index) * 60_000,
                    updatedAt: now - Int64(index) * 60_000,
                    pinnedAt: nil,
                    sessionCount: 1,
                    scheduleId: nil,
                    shareToken: nil,
                    agentId: "trae",
                    acpMode: "default",
                    acpThoughtLevel: "medium"
                )
            })
        }
        jobs = filteredUITestJobs(workspaceID: nil)
        selectedWorkspaceID = nil
        lastSuccessfulSyncAt = Date()
        lastSyncFailureMessage = nil
        isDataStale = false
        hasPendingSync = false
        isUsingCachedData = false
        if let waitingGraphJob = uiTestJobs.first(where: { $0.id == "job-graph-waiting" }) {
            graphJobStates[waitingGraphJob.id] = GraphJobState(
                status: "awaitingInput",
                lastError: nil,
                jobUpdatedAt: waitingGraphJob.updatedAt,
                updatedAt: Date()
            )
        }
        phase = .connected
    }

#if DEBUG
    private func uiTestManagedAgentCatalogItems() throws -> [AgentCatalogItem] {
        var items: [AgentCatalogItem] = [
            try AgentCatalogItem.uiTest(agentId: "trae", displayName: "TraeCode"),
            try AgentCatalogItem.uiTest(agentId: "codex", displayName: "Codex"),
            try AgentCatalogItem.uiTest(agentId: "manual-agent", displayName: "Manual Agent"),
        ]
        if uiTestScenario == "--ui-testing-agent-upgrade-failures" {
            items.insert(try AgentCatalogItem.uiTest(agentId: "after-conflict", displayName: "After Conflict"), at: 2)
        }
        return items
    }

    private func uiTestManagedAgentVersions() throws -> AgentVersionCheckResponse {
        var agents: [AgentVersionInfo] = [
            try AgentVersionInfo.uiTest(
                agentId: "trae",
                updateAvailable: !uiTestUpgradedAgentIDs.contains("trae"),
                upgradeSupported: true
            ),
            try AgentVersionInfo.uiTest(
                agentId: "codex",
                updateAvailable: !uiTestUpgradedAgentIDs.contains("codex"),
                upgradeSupported: true
            ),
        ]
        if uiTestScenario == "--ui-testing-agent-upgrade-failures" {
            agents.append(try .uiTest(agentId: "after-conflict", updateAvailable: true, upgradeSupported: true))
        }
        agents.append(try .uiTest(agentId: "manual-agent", updateAvailable: true, upgradeSupported: false))
        return AgentVersionCheckResponse(
            code: 0,
            checkedAt: Int64(Date().timeIntervalSince1970 * 1_000),
            agents: agents
        )
    }

    private func uiTestManagedAgentUpgrade(agentID: String) throws -> AgentInstallResponse {
        if uiTestScenario == "--ui-testing-agent-upgrade-failures" {
            if agentID == "trae" {
                throw APIError(
                    summary: "升级 Agent 失败",
                    detail: "模拟网络错误：保留完整错误并继续后续 Agent。"
                )
            }
            if agentID == "codex" {
                throw APIError(
                    summary: "Quartet 请求失败",
                    detail: "POST /api/v1/agent/codex/upgrade\nHTTP 409\n\n{\"code\":-1,\"msg\":\"another agent install is already in progress\"}",
                    requestWasRejected: true,
                    httpStatusCode: 409
                )
            }
        }
        uiTestUpgradedAgentIDs.insert(agentID)
        let object: [String: Any] = [
            "code": 0,
            "result": [
                "agent_id": agentID,
                "steps": [[
                    "display": "npm update -g \(agentID)",
                    "stdout": "updated \(agentID)",
                    "stderr": "",
                    "exit_code": 0,
                    "timed_out": false,
                    "duration_ms": 120,
                ]],
                "installed": true,
                "validation": ["ok": true],
            ],
        ]
        return try JSONDecoder().decode(
            AgentInstallResponse.self,
            from: JSONSerialization.data(withJSONObject: object)
        )
    }
#endif

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
        let tokens = UsageStatsTokenTotals(
            total: 14_800, reported: 12_300, input: 9_100, output: 3_200, cachedRead: 2_400,
            cachedWrite: 800, reasoning: 1_100, imageEstimate: 640, estimated: 2_500,
            reportedTurns: 15, estimatedTurns: 3, assistant: 5_300, thought: 4_100, toolCall: 2_200
        )
        let modelA = UsageStatsSectionTotals(
            totalMs: 2_160_000, turnCount: 18, assistantCount: 18, thoughtCount: 13,
            toolCallCount: 34, tokens: tokens
        )
        let modelB = UsageStatsSectionTotals(
            totalMs: 960_000, turnCount: 8, assistantCount: 8, thoughtCount: 6,
            toolCallCount: 12,
            tokens: UsageStatsTokenTotals(
                total: 6_200, reported: 4_700, input: 3_400, output: 1_300, cachedRead: 900,
                reasoning: 450, imageEstimate: 224, estimated: 1_500, reportedTurns: 6,
                estimatedTurns: 2, assistant: 2_400, thought: 1_700, toolCall: 900
            )
        )
        let daily = [
            UsageStatsDailyRow(
                date: dateKey(-3), totalMs: 540_000, turnCount: 5, assistantCount: 5, thoughtCount: 4, toolCallCount: 8,
                tokens: UsageStatsTokenTotals(
                    total: 4_100, reported: 3_300, input: 2_500, output: 800, cachedRead: 600,
                    reasoning: 300, imageEstimate: 196, estimated: 800, reportedTurns: 4,
                    estimatedTurns: 1, assistant: 1_400, thought: 1_200, toolCall: 600
                ),
                models: ["gpt-5.6": UsageStatsSectionTotals(
                    totalMs: 540_000, turnCount: 5, assistantCount: 5, thoughtCount: 4, toolCallCount: 8,
                    tokens: UsageStatsTokenTotals(
                        total: 4_100, reported: 3_300, input: 2_500, output: 800, cachedRead: 600,
                        reasoning: 300, imageEstimate: 196, estimated: 800, reportedTurns: 4,
                        estimatedTurns: 1, assistant: 1_400, thought: 1_200, toolCall: 600
                    )
                )], modelNames: ["gpt-5.6": "gpt-5.6"]
            ),
            UsageStatsDailyRow(
                date: dateKey(-2), totalMs: 1_020_000, turnCount: 9, assistantCount: 9, thoughtCount: 7, toolCallCount: 14,
                tokens: UsageStatsTokenTotals(
                    total: 7_000, reported: 5_600, input: 4_100, output: 1_500, cachedRead: 1_000,
                    cachedWrite: 300, reasoning: 500, imageEstimate: 224, estimated: 1_400,
                    reportedTurns: 7, estimatedTurns: 2, assistant: 2_600, thought: 1_900, toolCall: 1_100
                ),
                models: [
                    "gpt-5.6": UsageStatsSectionTotals(
                        totalMs: 660_000, turnCount: 6, assistantCount: 6, thoughtCount: 5, toolCallCount: 9,
                        tokens: UsageStatsTokenTotals(total: 4_500, reported: 4_000, input: 2_900, output: 1_100, estimated: 500, reportedTurns: 5, estimatedTurns: 1, assistant: 1_700, thought: 1_200, toolCall: 700)
                    ),
                    "gpt-5.4": UsageStatsSectionTotals(
                        totalMs: 360_000, turnCount: 3, assistantCount: 3, thoughtCount: 2, toolCallCount: 5,
                        tokens: UsageStatsTokenTotals(total: 2_500, reported: 1_600, input: 1_200, output: 400, estimated: 900, reportedTurns: 2, estimatedTurns: 1, assistant: 900, thought: 700, toolCall: 400)
                    )
                ],
                modelNames: ["gpt-5.6": "gpt-5.6", "gpt-5.4": "gpt-5.4"]
            ),
            UsageStatsDailyRow(
                date: dateKey(-1), totalMs: 780_000, turnCount: 6, assistantCount: 6, thoughtCount: 5, toolCallCount: 11,
                tokens: UsageStatsTokenTotals(total: 5_200, reported: 4_500, input: 3_200, output: 1_300, imageEstimate: 196, estimated: 700, reportedTurns: 5, estimatedTurns: 1, assistant: 2_100, thought: 1_400, toolCall: 700),
                models: ["gpt-5.4": UsageStatsSectionTotals(
                    totalMs: 780_000, turnCount: 6, assistantCount: 6, thoughtCount: 5, toolCallCount: 11,
                    tokens: UsageStatsTokenTotals(total: 5_200, reported: 4_500, input: 3_200, output: 1_300, imageEstimate: 196, estimated: 700, reportedTurns: 5, estimatedTurns: 1, assistant: 2_100, thought: 1_400, toolCall: 700)
                )], modelNames: ["gpt-5.4": "gpt-5.4"]
            ),
            UsageStatsDailyRow(
                date: dateKey(0), totalMs: 780_000, turnCount: 6, assistantCount: 6, thoughtCount: 3, toolCallCount: 13,
                tokens: UsageStatsTokenTotals(total: 4_700, reported: 3_600, input: 2_700, output: 900, cachedRead: 700, imageEstimate: 224, estimated: 1_100, reportedTurns: 4, estimatedTurns: 2, assistant: 1_600, thought: 1_300, toolCall: 700),
                models: ["gpt-5.6": UsageStatsSectionTotals(
                    totalMs: 780_000, turnCount: 6, assistantCount: 6, thoughtCount: 3, toolCallCount: 13,
                    tokens: UsageStatsTokenTotals(total: 4_700, reported: 3_600, input: 2_700, output: 900, cachedRead: 700, imageEstimate: 224, estimated: 1_100, reportedTurns: 4, estimatedTurns: 2, assistant: 1_600, thought: 1_300, toolCall: 700)
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
                workspaceId: "ws-studio", status: "awaitingInput",
                baseSnapshot: GraphRunSnapshot(
                    workflowId: "release-check", workflowName: "发布检查",
                    config: GraphConfig(nodes: [], edges: []), capturedAt: 0
                ),
                versions: nil, archivedInstances: nil, currentVersion: 3,
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
        requestedServerAddress: String,
        requestedUsername: String
    ) -> Bool {
        generation == connectionGeneration
            && StorageKey.connectionIdentity(for: serverAddress)
                == StorageKey.connectionIdentity(for: requestedServerAddress)
            && username.trimmingCharacters(in: .whitespacesAndNewlines) == requestedUsername
    }

    private func finishSupersededConnectionIfNeeded(generation: UInt64) {
        if generation == connectionGeneration, phase == .connecting {
            phase = .disconnected
            hasPendingSync = false
        }
    }

    private func applyFirstPage(_ response: JobsPage) {
        pruneExpiredOptimisticJobExecutions()
        var merged = response.jobs.map(reconcileOptimisticJobExecution)
        appendMissingOptimisticJobs(to: &merged)
        merged.sort(by: jobComesBefore)
        jobs = merged
        nextCursor = response.nextCursor
        hasMoreJobs = response.hasMore
    }

    private func applyPolledFirstPage(_ response: JobsPage) {
        pruneExpiredOptimisticJobExecutions()
        let previousJobs = jobs
        let previousByID = Dictionary(uniqueKeysWithValues: previousJobs.map { ($0.id, $0) })
        let fetchedIDs = Set(response.jobs.map(\.id))
        var merged = response.jobs.map { fresh in
            let fresh = reconcileOptimisticJobExecution(fresh)
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
        appendMissingOptimisticJobs(to: &merged)
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

    private func reconcileOptimisticJobExecution(_ fresh: JobSummary) -> JobSummary {
        guard let optimistic = optimisticJobExecutions[fresh.id] else { return fresh }
        let hasStarted = fresh.status == "running" || fresh.status == "stepStopping"
        let hasNewerTerminalState = fresh.status != "pending"
            && fresh.updatedAt > optimistic.baseline.updatedAt
        if hasStarted || hasNewerTerminalState {
            optimisticJobExecutions.removeValue(forKey: fresh.id)
            return fresh
        }
        return fresh.updating(
            status: "running",
            updatedAt: max(fresh.updatedAt, optimistic.display.updatedAt)
        )
    }

    private func appendMissingOptimisticJobs(to merged: inout [JobSummary]) {
        let existingIDs = Set(merged.map(\.id))
        for (id, optimistic) in optimisticJobExecutions
        where !existingIDs.contains(id) && shouldDisplayJob(optimistic.display) {
            merged.append(optimistic.display)
        }
    }

    /// Bounds how long an optimistic "running" overlay may outlive the sync that should have replaced
    /// it. `reconcileOptimisticJobExecution` needs the server to report either `running` or a state
    /// newer than the baseline, and neither arrives when a run starts and finishes inside a single poll
    /// interval, or when the baseline was stamped from a device clock running ahead of the server's.
    /// Without an expiry that row would show a spinning "running" for the rest of the session, keeping
    /// the pending-sync notice up and the dashboard pinned to the 5 second poll interval with it.
    private func pruneExpiredOptimisticJobExecutions() {
        guard !optimisticJobExecutions.isEmpty else { return }
        let now = Date()
        optimisticJobExecutions = optimisticJobExecutions.filter { _, optimistic in
            now.timeIntervalSince(optimistic.startedAt) <= Self.optimisticJobExecutionLifetime
        }
    }

    private static let optimisticJobExecutionLifetime: TimeInterval = 120

    private func upsertVisibleJob(_ job: JobSummary) {
        guard shouldDisplayJob(job) else { return }
        if let index = jobs.firstIndex(where: { $0.id == job.id }) {
            jobs[index] = job
        } else {
            jobs.append(job)
        }
        jobs.sort(by: jobComesBefore)
    }

    private func shouldDisplayJob(_ job: JobSummary) -> Bool {
        if let selectedWorkspaceID, job.workspaceId != selectedWorkspaceID { return false }
        if hideScheduledJobs, job.scheduleId?.isEmpty == false { return false }
        return true
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
            jobs: jobs.map { job in
                CachedJobSummary(optimisticJobExecutions[job.id]?.baseline ?? job)
            },
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
        hasPendingSync = !optimisticJobExecutions.isEmpty
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
        // The response was just fetched from the server, so it is at least as fresh as every Job record
        // already in hand: stamp it with the newest known revision so an older route snapshot (a pushed
        // GraphRunView holds a frozen `JobSummary`) cannot make the entry look outdated right away.
        let observedJobUpdatedAt = max(job.updatedAt, jobSummary(id: jobID)?.updatedAt ?? 0)
        let state = GraphJobState(
            status: run.status,
            lastError: run.lastError?.fullDetail ?? response.progress?.lastError,
            jobUpdatedAt: observedJobUpdatedAt,
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
        case "pending": "等待中".localizedForApp
        case "running": "运行中".localizedForApp
        case "awaitingInput": "等待人工".localizedForApp
        case "stepStopping": "步骤后停止中".localizedForApp
        case "stepStopped": "已在步骤后停止".localizedForApp
        case "completed": "已完成".localizedForApp
        case "failed": "失败".localizedForApp
        case "timedOut": "已超时".localizedForApp
        case "stopped": "已停止".localizedForApp
        default: status
        }
    }

    private func clearSessionCookies(for serverAddress: String) {
        guard let url = URL(string: serverAddress),
              let cookies = HTTPCookieStorage.shared.cookies(for: url) else { return }
        for cookie in cookies where cookie.name == "quartet_session" {
            HTTPCookieStorage.shared.deleteCookie(cookie)
        }
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
        static let resolvedServerAddress = "quartet.resolvedServerAddress"
        static let resolvedServerAddressEntryIdentity = "quartet.resolvedServerAddressEntryIdentity"
        static let username = "quartet.username"
        static let connectionValidated = "quartet.connectionValidated"
        static let selectedWorkspaceID = "quartet.selectedWorkspaceID"
        static let hideScheduledJobs = "quartet.hideScheduledJobs"
        static let appLanguage = "quartet.appLanguage"
        static let lastSuccessfulSyncAt = "quartet.lastSuccessfulSyncAt"
        static let credentialCacheNamespace = "quartet.credentialCacheNamespace"
        static let legacyTokenAccount = "agent-auth-token"

        static func lastSentMessageWorkspaceID(for serverAddress: String) -> String {
            let server = connectionIdentity(for: serverAddress) ?? serverAddress
            let encodedServer = Data(server.utf8).base64EncodedString()
            return "quartet.lastSentMessageWorkspaceID.\(encodedServer)"
        }

        static func newConversationDraft(for serverAddress: String, username: String) -> String {
            let server = connectionIdentity(for: serverAddress) ?? serverAddress
            let scope = "\(server)|\(username)"
            let encodedScope = Data(scope.utf8).base64EncodedString()
            return "quartet.newConversationDraft.\(encodedScope)"
        }

        static func lastGraphWorkspaceID(for serverAddress: String) -> String {
            let server = connectionIdentity(for: serverAddress) ?? serverAddress
            let encodedServer = Data(server.utf8).base64EncodedString()
            return "quartet.lastGraphWorkspaceID.\(encodedServer)"
        }

        static func lastFilesWorkspaceID(for serverAddress: String) -> String {
            let server = connectionIdentity(for: serverAddress) ?? serverAddress
            let encodedServer = Data(server.utf8).base64EncodedString()
            return "quartet.lastFilesWorkspaceID.\(encodedServer)"
        }

        static func legacyTokenAccount(for serverAddress: String) -> String {
            "agent-auth-token|\(connectionIdentity(for: serverAddress) ?? "invalid-server")"
        }

        static func connectionIdentity(for serverAddress: String) -> String? {
            guard let client = try? APIClient(serverAddress: serverAddress) else {
                return nil
            }
            return client.baseURL.absoluteString
        }

    }

    private struct DashboardPollScope: Equatable {
        let connectionIdentity: String?
        let workspaceID: String?
        let excludeScheduled: Bool
    }
}
