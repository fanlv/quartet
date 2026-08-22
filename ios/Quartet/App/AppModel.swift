import Foundation
import Combine
import SwiftUI
import UserNotifications

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
    @Published private(set) var isRefreshing = false
    @Published private(set) var isLoadingMore = false
    @Published private(set) var hasMoreJobs = false
    @Published private(set) var lastSuccessfulSyncAt: Date?
    @Published private(set) var lastSyncFailureMessage: String?
    @Published private(set) var isDataStale = false
    @Published private(set) var hasPendingSync = false
    @Published private(set) var isUsingCachedData = false
    @Published private(set) var graphJobStates: [String: GraphJobState] = [:]
    @Published private(set) var notifications: [QuartetNotificationRecord] = []
    @Published private(set) var notificationPreferences = QuartetNotificationPreferences()
    @Published private(set) var notificationAuthorizationStatus: UNAuthorizationStatus = .notDetermined
    @Published private(set) var pendingNotificationDestination: QuartetNotificationDestination?
    @Published var presentedError: PresentedError?

    private let defaults: UserDefaults
    private let cacheStore: DashboardCacheStore
    private let notificationCenterModel: QuartetNotificationCenter
    private var cancellables = Set<AnyCancellable>()
    private var credentialServerAddress: String
    private var credentialCacheNamespace: String
    private var nextCursor: String?
    private var hasLoadedCachedDashboard = false
    private var latestObservedJobs: [String: JobSummary] = [:]
    private var latestGraphRunVersions: [String: Int] = [:]
    private var lastNotifiedGraphTransitions: [String: String] = [:]
    private var interactiveFinalStatusesAwaitingSync: [String: String] = [:]
    private var pendingGraphStatusObservationIDs: [String] = []
    private var jobObservationCursor: String?
    private var dashboardGeneration: UInt64 = 0
    private var connectionGeneration: UInt64 = 0
    private var dashboardRefreshTask: Task<Void, Never>?
    private var loadMoreTask: Task<Void, Never>?

    init(
        defaults: UserDefaults = .standard,
        cacheStore: DashboardCacheStore = DashboardCacheStore(),
        notificationCenterModel: QuartetNotificationCenter? = nil
    ) {
        self.defaults = defaults
        self.cacheStore = cacheStore
        self.notificationCenterModel = notificationCenterModel ?? QuartetNotificationCenter(
            defaults: .standard,
            center: .current()
        )
        let storedServerAddress = defaults.string(forKey: StorageKey.serverAddress) ?? Self.defaultServerAddress
        let storedCredentialNamespace = defaults.string(forKey: StorageKey.credentialCacheNamespace) ?? UUID().uuidString
        serverAddress = storedServerAddress
        credentialServerAddress = storedServerAddress
        credentialCacheNamespace = storedCredentialNamespace
        defaults.set(storedCredentialNamespace, forKey: StorageKey.credentialCacheNamespace)
        token = Self.loadStoredToken(for: storedServerAddress, migrateLegacyCredential: true)
        selectedWorkspaceID = defaults.string(forKey: StorageKey.selectedWorkspaceID)
        if let timestamp = defaults.object(forKey: StorageKey.lastSuccessfulSyncAt) as? Double {
            lastSuccessfulSyncAt = Date(timeIntervalSince1970: timestamp)
        }
        bindNotifications()
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
        let hasValidatedConnection = defaults.bool(forKey: StorageKey.connectionValidated)
        if hasValidatedConnection {
            await loadCachedDashboardIfNeeded()
        } else {
            await cacheStore.clear()
        }
        await refreshNotificationAuthorization()
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
                emitsNotification: hasDashboardContent,
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
        guard phase != .connecting || presentFailure else { return }
        let generation = beginDashboardRequest()
        let workspaceID = selectedWorkspaceID
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
        presentFailure: Bool,
        disconnectOnFailure: Bool
    ) async {
        var failureWorkspaceID = workspaceID
        do {
            let client = try makeClient()
            async let workspaceRequest = client.workspaces()
            async let visibleJobsRequest = client.jobs(workspaceID: workspaceID, limit: 100)
            async let observationRequest = fetchJobObservations(client: client)
            let previousJobs = latestObservedJobs
            let (workspaceResponse, requestedVisibleJobsResponse, observationResponse) = try await (
                workspaceRequest,
                visibleJobsRequest,
                observationRequest
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
                visibleJobsResponse = try await client.jobs(workspaceID: nil, limit: 100)
                guard isCurrentDashboardRequest(generation: generation, workspaceID: nil) else { return }
            }
            applyFirstPage(visibleJobsResponse)
            markSyncSucceeded()
            let cacheSnapshot = dashboardCacheSnapshot()
            await cacheStore.save(
                cacheSnapshot,
                generation: generation
            )
            guard isCurrentDashboardGeneration(generation), !Task.isCancelled else { return }

            processDashboardNotifications(
                previousJobs: previousJobs,
                observation: observationResponse
            )
            let graphCandidates = graphStatusObservationCandidates(
                changes: observationResponse.changes,
                activeJobs: observationResponse.activeJobs
            )
            await refreshGraphStatuses(for: graphCandidates, generation: generation)
            guard isCurrentDashboardGeneration(generation), !Task.isCancelled else { return }
            applyObservationSnapshot(observationResponse)
        } catch {
            guard isCurrentDashboardRequest(generation: generation, workspaceID: failureWorkspaceID),
                  !Task.isCancelled else { return }
            await handleSyncFailure(
                error,
                presentToUser: presentFailure,
                emitsNotification: true,
                disconnect: disconnectOnFailure
            )
        }
    }

    func selectWorkspace(_ id: String?) async {
        guard selectedWorkspaceID != id else { return }
        selectedWorkspaceID = id
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

    func reloadJobs() async {
        guard phase == .connected else { return }
        await refreshDashboard(userInitiated: false)
    }

    func loadMoreJobs() async {
        guard phase == .connected,
              hasMoreJobs,
              !isRefreshing,
              !isLoadingMore,
              let nextCursor else { return }
        let generation = dashboardGeneration
        let workspaceID = selectedWorkspaceID
        isLoadingMore = true
        let pageTask = Task { @MainActor [weak self] in
            guard let self else { return }
            await self.performLoadMoreJobs(
                generation: generation,
                workspaceID: workspaceID,
                cursor: nextCursor
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
        cursor: String
    ) async {
        do {
            let response = try await makeClient().jobs(
                workspaceID: workspaceID,
                cursor: cursor
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
        try await makeClient().job(id: id)
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
        jobs.first(where: { $0.id == id }) ?? latestObservedJobs[id]
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

    func notificationDestinationSummary() async -> JobSummary? {
        guard let jobID = pendingNotificationDestination?.jobID else { return nil }
        if let cached = jobSummary(id: jobID) { return cached }
        guard let detail = try? await makeClient().job(id: jobID) else { return nil }
        let now = Int64(Date().timeIntervalSince1970 * 1_000)
        return JobSummary(
            id: detail.id,
            title: detail.title,
            modelId: detail.firstModelId,
            status: detail.status,
            mode: detail.mode,
            workspaceId: detail.workspaceId,
            workdir: detail.workdir,
            createdAt: now,
            updatedAt: now,
            pinnedAt: nil,
            sessionCount: detail.sessionCount,
            scheduleId: detail.scheduleId,
            shareToken: nil
        )
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

    func createJob(request: CreateJobRequest) async throws -> String {
        let response = try await makeClient().createJob(request)
        hasPendingSync = true
        return response.jobId
    }

    func apiClient() throws -> APIClient {
        try makeClient()
    }

    func saveWorkspaceDefaults(workspaceID: String, agent: String, model: String) async throws {
        guard let index = workspaces.firstIndex(where: { $0.id == workspaceID }) else {
            throw APIError(summary: "工作空间不存在", detail: "未找到工作空间 \(workspaceID)")
        }
        let saved = try await makeClient().updateWorkspace(
            workspaces[index],
            defaultAgent: agent,
            defaultModel: model
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

    func handleScenePhaseChange(_ phase: ScenePhase) async {
        switch phase {
        case .active:
            await refreshNotificationAuthorization()
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

    func recordInteractiveTerminalEvent(
        jobID: String,
        outcome: String,
        finalStatus: String,
        occurredAt: Int64?,
        fallbackTitle: String,
        fallbackWorkspaceID: String?
    ) async {
        let cachedJob = jobSummary(id: jobID)
        let detail = try? await makeClient().job(id: jobID)
        let resolvedOutcome = detail?.latestTerminalRunOutcome ?? outcome
        let normalizedOutcome = Self.normalizedNotificationOutcome(resolvedOutcome)
        guard ["completed", "failed", "stopped"].contains(normalizedOutcome) else { return }
        let eventIdentity = "interactive-\(occurredAt ?? Int64(Date().timeIntervalSince1970 * 1_000))"
        emitNotificationForOutcome(
            outcome: normalizedOutcome,
            jobID: jobID,
            title: detail?.title ?? cachedJob?.displayTitle ?? fallbackTitle,
            workspaceID: detail?.workspaceId ?? cachedJob?.workspaceId ?? fallbackWorkspaceID,
            eventIdentity: eventIdentity,
            occurredAtMilliseconds: occurredAt
        )
        interactiveFinalStatusesAwaitingSync[jobID] = detail?.status ?? finalStatus
    }

    func setNotificationPreference(_ kind: QuartetNotificationKind, enabled: Bool) {
        notificationCenterModel.togglePreference(kind, enabled: enabled)
    }

    func requestNotificationAuthorization() async {
        _ = await notificationCenterModel.requestAuthorization()
        await refreshNotificationAuthorization()
    }

    func openNotification(_ record: QuartetNotificationRecord) {
        notificationCenterModel.select(record)
    }

    func markNotificationRead(_ record: QuartetNotificationRecord) {
        notificationCenterModel.markRead(record)
    }

    func markAllNotificationsRead() {
        notificationCenterModel.markAllRead()
    }

    func clearNotificationRecords() {
        notificationCenterModel.clearRecords()
    }

    func clearPendingNotificationDestination() {
        pendingNotificationDestination = nil
    }

    func editConnection() {
        let generation = invalidateDashboardRequests()
        connectionGeneration &+= 1
        try? KeychainStore.delete(account: StorageKey.tokenAccount(for: credentialServerAddress))
        try? KeychainStore.delete(account: StorageKey.legacyTokenAccount)
        defaults.set(false, forKey: StorageKey.connectionValidated)
        defaults.removeObject(forKey: StorageKey.selectedWorkspaceID)
        defaults.removeObject(forKey: StorageKey.lastSuccessfulSyncAt)
        health = nil
        workspaces = []
        jobs = []
        latestObservedJobs = [:]
        graphJobStates = [:]
        latestGraphRunVersions = [:]
        lastNotifiedGraphTransitions = [:]
        interactiveFinalStatusesAwaitingSync = [:]
        pendingGraphStatusObservationIDs = []
        jobObservationCursor = nil
        selectedWorkspaceID = nil
        nextCursor = nil
        hasMoreJobs = false
        isDataStale = false
        hasPendingSync = false
        isUsingCachedData = false
        lastSuccessfulSyncAt = nil
        lastSyncFailureMessage = nil
        pendingNotificationDestination = nil
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
        try? KeychainStore.delete(account: StorageKey.tokenAccount(for: credentialServerAddress))
        try? KeychainStore.delete(account: StorageKey.legacyTokenAccount)
        serverAddress = Self.defaultServerAddress
        credentialServerAddress = Self.defaultServerAddress
        token = ""
        health = nil
        workspaces = []
        jobs = []
        latestObservedJobs = [:]
        graphJobStates = [:]
        latestGraphRunVersions = [:]
        lastNotifiedGraphTransitions = [:]
        interactiveFinalStatusesAwaitingSync = [:]
        pendingGraphStatusObservationIDs = []
        jobObservationCursor = nil
        selectedWorkspaceID = nil
        nextCursor = nil
        hasMoreJobs = false
        isDataStale = false
        hasPendingSync = false
        isUsingCachedData = false
        lastSuccessfulSyncAt = nil
        lastSyncFailureMessage = nil
        pendingNotificationDestination = nil
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

    private func bindNotifications() {
        notificationCenterModel.$records
            .sink { [weak self] in self?.notifications = $0 }
            .store(in: &cancellables)
        notificationCenterModel.$preferences
            .sink { [weak self] in self?.notificationPreferences = $0 }
            .store(in: &cancellables)
        notificationCenterModel.$authorizationStatus
            .sink { [weak self] in self?.notificationAuthorizationStatus = $0 }
            .store(in: &cancellables)
        notificationCenterModel.onDestinationSelected = { [weak self] destination in
            guard let self else { return }
            Task { @MainActor in
                self.pendingNotificationDestination = destination
                if let workspaceID = destination.workspaceID, workspaceID != self.selectedWorkspaceID {
                    await self.selectWorkspace(workspaceID)
                }
            }
        }
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
        jobs = snapshot.jobs.map(\.jobSummary)
        isUsingCachedData = true
        isDataStale = true
        hasPendingSync = true
        if lastSuccessfulSyncAt == nil {
            lastSuccessfulSyncAt = snapshot.savedAt
        }
        latestObservedJobs = Dictionary(uniqueKeysWithValues: jobs.map { ($0.id, $0) })
    }

    private func refreshNotificationAuthorization() async {
        await notificationCenterModel.refreshAuthorizationStatus()
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
        emitsNotification: Bool,
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
        if emitsNotification {
            notificationCenterModel.record(
                kind: .connectionIssue,
                title: "Quartet 连接异常",
                body: hasDashboardContent
                    ? "当前展示的是本地缓存，待网络恢复后会重新同步。"
                    : "同步失败，请在应用内查看完整错误。",
                dedupeKey: "connection:\(serverAddress):\(lastSuccessfulSyncAt?.timeIntervalSince1970 ?? 0):\(failureMessage)"
            )
        }
        if presentToUser {
            present(error)
        }
    }

    private func processDashboardNotifications(
        previousJobs: [String: JobSummary],
        observation: JobObservationPage
    ) {
        guard !observation.reset else { return }
        for change in observation.changes {
            let job = change.job
            let oldStatus = interactiveFinalStatusesAwaitingSync.removeValue(forKey: job.id)
                ?? change.previousGraphStatus
                ?? change.previousStatus
                ?? previousJobs[job.id]?.status
            let newStatus = change.graphStatus ?? job.status
            guard let oldStatus, oldStatus != newStatus else { continue }
            if job.mode == "graph", Self.isActivelyRunningStatus(newStatus) {
                graphJobStates.removeValue(forKey: job.id)
                continue
            }
            emitNotificationIfNeeded(
                oldStatus: oldStatus,
                newStatus: newStatus,
                jobID: job.id,
                title: job.displayTitle,
                workspaceID: job.workspaceId,
                eventIdentity: change.eventId,
                graphSessionID: change.graphSessionId,
                occurredAtMilliseconds: change.occurredAt
            )
        }
    }

    private func fetchJobObservations(client: APIClient) async throws -> JobObservationPage {
        var activeJobsByID: [String: JobSummary] = [:]
        var changes: [JobObservationEvent] = []
        var seenCursors = Set<String>()
        var cursor = jobObservationCursor
        var reset = false

        while true {
            let page = try await client.jobObservations(cursor: cursor, limit: 200)
            for job in page.activeJobs {
                activeJobsByID[job.id] = job
            }
            changes.append(contentsOf: page.changes)
            reset = reset || page.reset
            guard page.hasMore else {
                return JobObservationPage(
                    activeJobs: Array(activeJobsByID.values),
                    changes: changes,
                    cursor: page.cursor,
                    hasMore: false,
                    reset: reset
                )
            }
            let nextCursor = page.cursor
            guard !nextCursor.isEmpty else {
                throw APIError(
                    summary: "Job 通知观察响应无效",
                    detail: "GET /api/v1/job/observations 返回 hasMore=true，但没有 cursor；本轮未提交不完整的通知观察结果。"
                )
            }
            guard seenCursors.insert(nextCursor).inserted else {
                throw APIError(
                    summary: "Job 通知观察响应无效",
                    detail: "GET /api/v1/job/observations 重复返回 cursor \(nextCursor)；本轮未提交不完整的通知观察结果。"
                )
            }
            cursor = nextCursor
        }
    }

    private func applyObservationSnapshot(_ observation: JobObservationPage) {
        var latest = Dictionary(uniqueKeysWithValues: observation.activeJobs.map { ($0.id, $0) })
        for change in observation.changes {
            latest[change.job.id] = change.job
        }
        latestObservedJobs = latest
        jobObservationCursor = observation.cursor
    }

    private func graphStatusObservationCandidates(
        changes: [JobObservationEvent],
        activeJobs: [JobSummary]
    ) -> [JobSummary] {
        var candidates: [JobSummary] = []
        var candidateIDs = Set<String>()
        let currentGraphJobs = Dictionary(uniqueKeysWithValues: (
            activeJobs + changes.map(\.job)
        ).filter { $0.mode == "graph" }.map { ($0.id, $0) })

        for change in changes {
            let job = change.job
            guard job.mode == "graph",
                  change.graphStatus == nil,
                  job.status == "stopped",
                  candidateIDs.insert(job.id).inserted else { continue }
            candidates.append(job)
        }
        for jobID in pendingGraphStatusObservationIDs {
            guard let job = currentGraphJobs[jobID],
                  job.status == "stopped",
                  candidateIDs.insert(jobID).inserted else { continue }
            candidates.append(job)
        }
        return candidates
    }

    private func refreshGraphStatuses(for jobs: [JobSummary], generation: UInt64) async {
        guard isCurrentDashboardGeneration(generation) else { return }
        let graphJobs = jobs.filter { $0.mode == "graph" }
        guard !graphJobs.isEmpty else {
            pendingGraphStatusObservationIDs = []
            return
        }

        let maximumRequestsPerRefresh = 24
        let requestedJobs = Array(graphJobs.prefix(maximumRequestsPerRefresh))
        let jobsByID = Dictionary(uniqueKeysWithValues: requestedJobs.map { ($0.id, $0) })
        let jobIDs = requestedJobs.map(\.id)
        let address = serverAddress
        let currentToken = token
        // Put unserved work before failures. Persistent failures therefore
        // rotate behind later candidates instead of monopolizing every round.
        var failedIDs = graphJobs.dropFirst(maximumRequestsPerRefresh).map(\.id)
        var nextIndex = 0
        let maximumConcurrentRequests = 6

        await withTaskGroup(of: (String, GraphRunStatusResponse?).self) { group in
            func addNextRequest() {
                guard nextIndex < jobIDs.count else { return }
                let jobID = jobIDs[nextIndex]
                nextIndex += 1
                group.addTask {
                    do {
                        let client = try APIClient(serverAddress: address, token: currentToken)
                        return (jobID, try await client.graphRunStatus(jobID: jobID))
                    } catch {
                        return (jobID, nil)
                    }
                }
            }

            for _ in 0..<min(maximumConcurrentRequests, jobIDs.count) { addNextRequest() }
            while let (jobID, response) = await group.next() {
                guard isCurrentDashboardGeneration(generation) else {
                    group.cancelAll()
                    return
                }
                if let response, let job = jobsByID[jobID] {
                    applyGraphStatus(job: job, response: response)
                } else {
                    failedIDs.append(jobID)
                }
                addNextRequest()
            }
        }
        guard isCurrentDashboardGeneration(generation) else { return }
        pendingGraphStatusObservationIDs = failedIDs
    }

    private func applyGraphStatus(job: JobSummary, response: GraphRunStatusResponse) {
        guard let run = response.run else { return }
        let jobID = job.id
        let state = GraphJobState(
            status: run.status,
            lastError: run.lastError?.fullDetail ?? response.progress?.lastError,
            updatedAt: Date()
        )
        let previousStatus = graphJobStates[jobID]?.status
            ?? latestObservedJobs[jobID]?.status
        let previousVersion = latestGraphRunVersions[jobID]
        graphJobStates[jobID] = state
        latestGraphRunVersions[jobID] = run.currentVersion
        let transitionKey = "\(run.id):v\(run.currentVersion):\(run.status)"
        if let previousStatus,
           (previousStatus != run.status || previousVersion != run.currentVersion),
           lastNotifiedGraphTransitions[jobID] != transitionKey {
            let awaitingInstance = response.instances?.first(where: { $0.status == "awaitingInput" })
            let graphSessionID = awaitingInstance?.displaySessionId ?? awaitingInstance?.sessionId
            let transitionStamp = run.finishedAt ?? awaitingInstance?.startedAt ?? job.updatedAt
            emitNotificationIfNeeded(
                oldStatus: previousStatus,
                newStatus: run.status,
                jobID: jobID,
                title: job.displayTitle,
                workspaceID: run.workspaceId ?? job.workspaceId,
                eventIdentity: "graph-\(run.id)-v\(run.currentVersion)-\(transitionStamp)-\(graphSessionID ?? "none")",
                graphSessionID: graphSessionID,
                occurredAtMilliseconds: transitionStamp
            )
            lastNotifiedGraphTransitions[jobID] = transitionKey
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

    private func emitNotificationIfNeeded(
        oldStatus: String,
        newStatus: String,
        jobID: String,
        title: String,
        workspaceID: String?,
        eventIdentity: String,
        graphSessionID: String? = nil,
        occurredAtMilliseconds: Int64? = nil
    ) {
        let oldOutcome = Self.normalizedNotificationOutcome(oldStatus)
        let newOutcome = Self.normalizedNotificationOutcome(newStatus)
        guard oldOutcome != newOutcome else { return }
        emitNotificationForOutcome(
            outcome: newOutcome,
            displayStatus: newStatus,
            jobID: jobID,
            title: title,
            workspaceID: workspaceID,
            eventIdentity: eventIdentity,
            graphSessionID: graphSessionID,
            occurredAtMilliseconds: occurredAtMilliseconds
        )
    }

    private func emitNotificationForOutcome(
        outcome: String,
        displayStatus: String? = nil,
        jobID: String,
        title: String,
        workspaceID: String?,
        eventIdentity: String,
        graphSessionID: String? = nil,
        occurredAtMilliseconds: Int64? = nil
    ) {
        let occurredAt = occurredAtMilliseconds.map { $0.quartetDate } ?? Date()
        let workspaceName = workspaces.first(where: { $0.id == workspaceID })?.displayName
            ?? workspaceID
            ?? "未指定工作空间"
        let body = "\(title) · \(workspaceName) · \(statusLabel(displayStatus ?? outcome)) · \(occurredAt.formatted(date: .abbreviated, time: .shortened))"
        let dedupeKey = "job:\(jobID):\(outcome):\(eventIdentity)"
        switch outcome {
        case "completed":
            notificationCenterModel.record(
                kind: .jobCompleted,
                title: "Job 已完成",
                body: body,
                jobID: jobID,
                workspaceID: workspaceID,
                dedupeKey: dedupeKey
            )
        case "failed":
            notificationCenterModel.record(
                kind: .jobFailed,
                title: "Job 执行失败",
                body: body,
                jobID: jobID,
                workspaceID: workspaceID,
                dedupeKey: dedupeKey
            )
        case "awaitingInput":
            notificationCenterModel.record(
                kind: .awaitingInput,
                title: "需要人工处理",
                body: body,
                jobID: jobID,
                workspaceID: workspaceID,
                graphSessionID: graphSessionID,
                dedupeKey: dedupeKey
            )
        case "stopped":
            // User-requested stops are visible in the Job state but are not a
            // configured notification category. Keeping the event here still
            // lets the conversation queue use the authoritative terminal edge.
            break
        default:
            break
        }
    }

    private static func normalizedNotificationOutcome(_ status: String) -> String {
        status == "timedOut" ? "failed" : status
    }

    private static func isActivelyRunningStatus(_ status: String) -> Bool {
        status == "running" || status == "pending" || status == "stepStopping"
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

    private enum StorageKey {
        static let serverAddress = "quartet.serverAddress"
        static let connectionValidated = "quartet.connectionValidated"
        static let selectedWorkspaceID = "quartet.selectedWorkspaceID"
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
}
