import Foundation
import Combine
import UserNotifications

enum QuartetNotificationKind: String, CaseIterable, Codable, Identifiable, Sendable {
    case jobCompleted
    case jobFailed
    case awaitingInput
    case connectionIssue

    var id: String { rawValue }

    var title: String {
        switch self {
        case .jobCompleted: "Job 完成"
        case .jobFailed: "Job 失败"
        case .awaitingInput: "等待人工处理"
        case .connectionIssue: "连接异常"
        }
    }

    var systemImage: String {
        switch self {
        case .jobCompleted: "checkmark.circle"
        case .jobFailed: "xmark.octagon"
        case .awaitingInput: "person.crop.circle.badge.questionmark"
        case .connectionIssue: "wifi.exclamationmark"
        }
    }
}

struct QuartetNotificationPreferences: Codable, Equatable, Sendable {
    var jobCompleted = true
    var jobFailed = true
    var awaitingInput = true
    var connectionIssue = true

    func isEnabled(_ kind: QuartetNotificationKind) -> Bool {
        switch kind {
        case .jobCompleted: jobCompleted
        case .jobFailed: jobFailed
        case .awaitingInput: awaitingInput
        case .connectionIssue: connectionIssue
        }
    }

    mutating func set(_ kind: QuartetNotificationKind, enabled: Bool) {
        switch kind {
        case .jobCompleted: jobCompleted = enabled
        case .jobFailed: jobFailed = enabled
        case .awaitingInput: awaitingInput = enabled
        case .connectionIssue: connectionIssue = enabled
        }
    }
}

struct QuartetNotificationRecord: Codable, Identifiable, Hashable, Sendable {
    let id: String
    let kind: QuartetNotificationKind
    let title: String
    let body: String
    let jobID: String?
    let workspaceID: String?
    let graphSessionID: String?
    let createdAt: Date
    var readAt: Date?

    var isUnread: Bool { readAt == nil }

    private enum CodingKeys: String, CodingKey {
        case id, kind, title, body, jobID, workspaceID, graphSessionID, createdAt, readAt
    }

    init(
        id: String,
        kind: QuartetNotificationKind,
        title: String,
        body: String,
        jobID: String?,
        workspaceID: String?,
        graphSessionID: String?,
        createdAt: Date,
        readAt: Date?
    ) {
        self.id = id
        self.kind = kind
        self.title = title
        self.body = body
        self.jobID = jobID
        self.workspaceID = workspaceID
        self.graphSessionID = graphSessionID
        self.createdAt = createdAt
        self.readAt = readAt
    }

    init(from decoder: Decoder) throws {
        let values = try decoder.container(keyedBy: CodingKeys.self)
        id = try values.decode(String.self, forKey: .id)
        kind = try values.decode(QuartetNotificationKind.self, forKey: .kind)
        title = try values.decode(String.self, forKey: .title)
        body = try values.decode(String.self, forKey: .body)
        jobID = try values.decodeIfPresent(String.self, forKey: .jobID)
        workspaceID = try values.decodeIfPresent(String.self, forKey: .workspaceID)
        graphSessionID = try values.decodeIfPresent(String.self, forKey: .graphSessionID)
        createdAt = try values.decode(Date.self, forKey: .createdAt)
        readAt = try values.decodeIfPresent(Date.self, forKey: .readAt)
    }
}

struct QuartetNotificationDestination: Hashable, Sendable {
    let jobID: String?
    let workspaceID: String?
    let graphSessionID: String?
}

@MainActor
final class QuartetNotificationCenter: NSObject, ObservableObject, UNUserNotificationCenterDelegate {
    @Published private(set) var records: [QuartetNotificationRecord] = []
    @Published private(set) var authorizationStatus: UNAuthorizationStatus = .notDetermined
    @Published var preferences: QuartetNotificationPreferences {
        didSet { savePreferences() }
    }

    var onDestinationSelected: ((QuartetNotificationDestination) -> Void)?

    private let defaults: UserDefaults
    private let center: UNUserNotificationCenter

    init(defaults: UserDefaults, center: UNUserNotificationCenter) {
        self.defaults = defaults
        self.center = center
        preferences = Self.loadPreferences(defaults: defaults)
        records = Self.loadRecords(defaults: defaults)
        super.init()
        center.delegate = self
    }

    var unreadCount: Int {
        records.filter(\.isUnread).count
    }

    func refreshAuthorizationStatus() async {
        let settings = await center.notificationSettings()
        authorizationStatus = settings.authorizationStatus
    }

    @discardableResult
    func requestAuthorization() async -> Bool {
        do {
            let granted = try await center.requestAuthorization(options: [.alert, .badge, .sound])
            await refreshAuthorizationStatus()
            return granted
        } catch {
            await refreshAuthorizationStatus()
            return false
        }
    }

    func togglePreference(_ kind: QuartetNotificationKind, enabled: Bool) {
        preferences.set(kind, enabled: enabled)
    }

    func record(
        kind: QuartetNotificationKind,
        title: String,
        body: String,
        jobID: String? = nil,
        workspaceID: String? = nil,
        graphSessionID: String? = nil,
        dedupeKey: String,
        postsLocalNotification: Bool = true
    ) {
        guard preferences.isEnabled(kind), !records.contains(where: { $0.id == dedupeKey }) else { return }

        let record = QuartetNotificationRecord(
            id: dedupeKey,
            kind: kind,
            title: title,
            body: body,
            jobID: jobID,
            workspaceID: workspaceID,
            graphSessionID: graphSessionID,
            createdAt: Date(),
            readAt: nil
        )
        records.insert(record, at: 0)
        records = Array(records.prefix(100))
        saveRecords()

        guard postsLocalNotification else { return }
        Task { @MainActor [weak self] in
            await self?.scheduleLocalNotification(record)
        }
    }

    func markRead(_ record: QuartetNotificationRecord) {
        guard let index = records.firstIndex(where: { $0.id == record.id }) else { return }
        records[index].readAt = records[index].readAt ?? Date()
        saveRecords()
    }

    func markAllRead() {
        let now = Date()
        records = records.map { record in
            var updated = record
            updated.readAt = updated.readAt ?? now
            return updated
        }
        saveRecords()
    }

    func clearRecords() {
        records = []
        defaults.removeObject(forKey: StorageKey.records)
    }

    func select(_ record: QuartetNotificationRecord) {
        markRead(record)
        guard record.jobID != nil else { return }
        onDestinationSelected?(QuartetNotificationDestination(
            jobID: record.jobID,
            workspaceID: record.workspaceID,
            graphSessionID: record.graphSessionID
        ))
    }

    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        willPresent notification: UNNotification
    ) async -> UNNotificationPresentationOptions {
        [.banner, .list, .sound]
    }

    nonisolated func userNotificationCenter(
        _ center: UNUserNotificationCenter,
        didReceive response: UNNotificationResponse
    ) async {
        let info = response.notification.request.content.userInfo
        let jobID = info[UserInfoKey.jobID] as? String
        let workspaceID = info[UserInfoKey.workspaceID] as? String
        let graphSessionID = info[UserInfoKey.graphSessionID] as? String
        let recordID = info[UserInfoKey.recordID] as? String
        await MainActor.run { [self] in
            if let recordID, let record = self.records.first(where: { $0.id == recordID }) {
                self.markRead(record)
            }
            guard let jobID, !jobID.isEmpty else { return }
            self.onDestinationSelected?(QuartetNotificationDestination(
                jobID: jobID,
                workspaceID: workspaceID,
                graphSessionID: graphSessionID
            ))
        }
    }

    private func scheduleLocalNotification(_ record: QuartetNotificationRecord) async {
        await refreshAuthorizationStatus()
        if authorizationStatus == .notDetermined {
            _ = await requestAuthorization()
        }
        guard authorizationStatus == .authorized || authorizationStatus == .provisional || authorizationStatus == .ephemeral else {
            return
        }

        let content = UNMutableNotificationContent()
        content.title = record.title
        content.body = record.body
        content.sound = .default
        var info: [String: String] = [:]
        if let jobID = record.jobID { info[UserInfoKey.jobID] = jobID }
        if let workspaceID = record.workspaceID { info[UserInfoKey.workspaceID] = workspaceID }
        if let graphSessionID = record.graphSessionID { info[UserInfoKey.graphSessionID] = graphSessionID }
        info[UserInfoKey.recordID] = record.id
        content.userInfo = info

        let request = UNNotificationRequest(
            identifier: record.id,
            content: content,
            trigger: UNTimeIntervalNotificationTrigger(timeInterval: 1, repeats: false)
        )
        try? await center.add(request)
    }

    private func savePreferences() {
        guard let data = try? JSONEncoder().encode(preferences) else { return }
        defaults.set(data, forKey: StorageKey.preferences)
    }

    private func saveRecords() {
        guard let data = try? JSONEncoder().encode(records) else { return }
        defaults.set(data, forKey: StorageKey.records)
    }

    private static func loadPreferences(defaults: UserDefaults) -> QuartetNotificationPreferences {
        guard let data = defaults.data(forKey: StorageKey.preferences),
              let value = try? JSONDecoder().decode(QuartetNotificationPreferences.self, from: data) else {
            return QuartetNotificationPreferences()
        }
        return value
    }

    private static func loadRecords(defaults: UserDefaults) -> [QuartetNotificationRecord] {
        guard let data = defaults.data(forKey: StorageKey.records),
              let value = try? JSONDecoder().decode([QuartetNotificationRecord].self, from: data) else {
            return []
        }
        return value
    }

    private enum StorageKey {
        static let preferences = "quartet.notificationPreferences"
        static let records = "quartet.notificationRecords"
    }

    private enum UserInfoKey {
        static let jobID = "jobID"
        static let workspaceID = "workspaceID"
        static let graphSessionID = "graphSessionID"
        static let recordID = "recordID"
    }
}
