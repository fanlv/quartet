import Foundation

struct DashboardCacheSnapshot: Codable, Sendable {
    let workspaces: [WorkspaceSummary]
    let jobs: [CachedJobSummary]
    let selectedWorkspaceID: String?
    let serverAddress: String
    let credentialNamespace: String?
    let savedAt: Date
}

struct CachedJobSummary: Codable, Sendable {
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

    init(_ job: JobSummary) {
        id = job.id
        title = job.title
        modelId = job.modelId
        agentId = job.agentId
        acpMode = job.acpMode
        acpThoughtLevel = job.acpThoughtLevel
        status = job.status
        mode = job.mode
        workspaceId = job.workspaceId
        workdir = job.workdir
        createdAt = job.createdAt
        updatedAt = job.updatedAt
        pinnedAt = job.pinnedAt
        sessionCount = job.sessionCount
        scheduleId = job.scheduleId
    }

    var jobSummary: JobSummary {
        JobSummary(
            id: id,
            title: title,
            modelId: modelId,
            status: status,
            mode: mode,
            workspaceId: workspaceId,
            workdir: workdir,
            createdAt: createdAt,
            updatedAt: updatedAt,
            pinnedAt: pinnedAt,
            sessionCount: sessionCount,
            scheduleId: scheduleId,
            shareToken: nil,
            agentId: agentId,
            acpMode: acpMode,
            acpThoughtLevel: acpThoughtLevel
        )
    }
}

actor DashboardCacheStore {
    private let cacheURL: URL
    private var minimumWritableGeneration: UInt64 = 0

    init(fileManager: FileManager = .default) {
        let baseURL = fileManager.urls(for: .applicationSupportDirectory, in: .userDomainMask).first
            ?? fileManager.temporaryDirectory
        cacheURL = baseURL
            .appendingPathComponent("Quartet", isDirectory: true)
            .appendingPathComponent("dashboard-cache.json", isDirectory: false)
    }

    func load() -> DashboardCacheSnapshot? {
        guard let data = try? Data(contentsOf: cacheURL) else { return nil }
        guard let decodedSnapshot = try? JSONDecoder().decode(DashboardCacheSnapshot.self, from: data) else {
            try? FileManager.default.removeItem(at: cacheURL)
            return nil
        }
        let selectedWorkspaceID = decodedSnapshot.selectedWorkspaceID.flatMap { selectedID in
            decodedSnapshot.workspaces.contains(where: { $0.id == selectedID }) ? selectedID : nil
        }
        let cachedJobs: [CachedJobSummary]
        if let selectedWorkspaceID {
            cachedJobs = decodedSnapshot.jobs.filter { $0.workspaceId == selectedWorkspaceID }
        } else if decodedSnapshot.selectedWorkspaceID == nil {
            cachedJobs = decodedSnapshot.jobs
        } else {
            cachedJobs = []
        }
        let snapshot = DashboardCacheSnapshot(
            workspaces: decodedSnapshot.workspaces,
            jobs: cachedJobs,
            selectedWorkspaceID: selectedWorkspaceID,
            serverAddress: decodedSnapshot.serverAddress,
            credentialNamespace: decodedSnapshot.credentialNamespace,
            savedAt: decodedSnapshot.savedAt
        )

        // Re-encode on read so caches written by older builds are immediately
        // stripped of sensitive fields and inconsistent workspace/job pairs.
        try? FileManager.default.removeItem(at: cacheURL)
        write(snapshot)
        return snapshot
    }

    func advanceGeneration(to generation: UInt64, clearingExistingCache: Bool = false) {
        guard generation > minimumWritableGeneration else { return }
        minimumWritableGeneration = generation
        if clearingExistingCache {
            try? FileManager.default.removeItem(at: cacheURL)
        }
    }

    func save(_ snapshot: DashboardCacheSnapshot, generation: UInt64) {
        guard generation >= minimumWritableGeneration else { return }
        minimumWritableGeneration = generation
        write(snapshot)
    }

    private func write(_ snapshot: DashboardCacheSnapshot) {
        do {
            let directoryURL = cacheURL.deletingLastPathComponent()
            try FileManager.default.createDirectory(at: directoryURL, withIntermediateDirectories: true)
            let data = try JSONEncoder().encode(snapshot)
            try data.write(to: cacheURL, options: [.atomic])
        } catch {
            #if DEBUG
            print("Dashboard cache save failed: \(error)")
            #endif
        }
    }

    func clear() {
        try? FileManager.default.removeItem(at: cacheURL)
    }
}
