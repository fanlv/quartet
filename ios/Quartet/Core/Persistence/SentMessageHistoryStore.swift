import Foundation

struct SentMessageHistoryItem: Codable, Hashable, Identifiable, Sendable {
    let id: String
    let createdAt: Int64
    let content: String
    let attachments: [SentMessageHistoryAttachment]?

    var storedAttachments: [SentMessageHistoryAttachment] { attachments ?? [] }
    var imageAttachmentCount: Int { storedAttachments.lazy.filter(\.isImage).count }
    var fileAttachmentCount: Int { storedAttachments.count - imageAttachmentCount }

    var composerContent: String {
        if (content == "[image]" || content == "[file]"), !storedAttachments.isEmpty {
            return ""
        }
        return content
    }
}

struct SentMessageHistoryAttachment: Codable, Hashable, Sendable {
    let id: String
    let filename: String
    let mimeType: String
    let isImage: Bool
}

@MainActor
final class SentMessageHistoryStore {
    private struct Payload: Codable {
        let version: Int
        let items: [SentMessageHistoryItem]
    }

    private static let itemLimit = 50
    private let defaults: UserDefaults
    private let fileManager: FileManager
    private let attachmentsDirectory: URL

    init(
        defaults: UserDefaults,
        fileManager: FileManager = .default,
        directoryName: String = "Quartet"
    ) {
        self.defaults = defaults
        self.fileManager = fileManager
        let baseURL = fileManager.urls(for: .applicationSupportDirectory, in: .userDomainMask).first
            ?? fileManager.temporaryDirectory
        attachmentsDirectory = baseURL
            .appendingPathComponent(directoryName, isDirectory: true)
            .appendingPathComponent("sent-message-history", isDirectory: true)
    }

    func items(scope: String) throws -> [SentMessageHistoryItem] {
        let key = storageKey(scope: scope)
        guard let data = defaults.data(forKey: key) else { return [] }
        do {
            let payload = try JSONDecoder().decode(Payload.self, from: data)
            return Array(payload.items.prefix(Self.itemLimit))
        } catch {
            throw StoreError(operation: "decode", key: key, underlying: error)
        }
    }

    @discardableResult
    func append(
        content: String,
        attachments: [PendingUpload],
        scope: String
    ) throws -> [SentMessageHistoryItem] {
        let content = content.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !content.isEmpty || !attachments.isEmpty else { return try items(scope: scope) }

        let key = storageKey(scope: scope)
        var history = try items(scope: scope)
        let itemID = UUID().uuidString.lowercased()
        let storedAttachments = attachments.map { attachment in
            SentMessageHistoryAttachment(
                id: UUID().uuidString.lowercased(),
                filename: attachment.filename,
                mimeType: attachment.mimeType,
                isImage: attachment.isImage
            )
        }
        history.insert(
            SentMessageHistoryItem(
                id: itemID,
                createdAt: Int64(Date().timeIntervalSince1970 * 1_000),
                content: content,
                attachments: storedAttachments.isEmpty ? nil : storedAttachments
            ),
            at: 0
        )
        let discardedItems = Array(history.dropFirst(Self.itemLimit))
        history = Array(history.prefix(Self.itemLimit))

        do {
            try persist(attachments, metadata: storedAttachments, itemID: itemID)
            let data = try JSONEncoder().encode(Payload(version: 2, items: history))
            defaults.set(data, forKey: key)
            for item in discardedItems {
                guard let directory = try? attachmentDirectoryURL(itemID: item.id) else { continue }
                try? fileManager.removeItem(at: directory)
            }
            return history
        } catch {
            try? fileManager.removeItem(at: attachmentDirectory(itemID: itemID))
            throw StoreError(operation: "save", key: key, underlying: error)
        }
    }

    func attachments(for item: SentMessageHistoryItem) throws -> [PendingUpload] {
        try item.storedAttachments.map { attachment in
            let url = try attachmentURL(itemID: item.id, attachmentID: attachment.id)
            do {
                return PendingUpload(
                    data: try Data(contentsOf: url),
                    filename: attachment.filename,
                    mimeType: attachment.mimeType,
                    isImage: attachment.isImage
                )
            } catch {
                throw StoreError(operation: "read attachment", key: url.path, underlying: error)
            }
        }
    }

    private func persist(
        _ attachments: [PendingUpload],
        metadata: [SentMessageHistoryAttachment],
        itemID: String
    ) throws {
        guard !attachments.isEmpty else { return }
        let directory = try attachmentDirectoryURL(itemID: itemID)
        do {
            try fileManager.createDirectory(at: directory, withIntermediateDirectories: true)
            for (attachment, stored) in zip(attachments, metadata) {
                let url = try attachmentURL(itemID: itemID, attachmentID: stored.id)
                try attachment.data.write(to: url, options: [.atomic])
            }
        } catch {
            throw StoreError(operation: "write attachment", key: directory.path, underlying: error)
        }
    }

    private func attachmentDirectory(itemID: String) -> URL {
        attachmentsDirectory.appendingPathComponent(itemID, isDirectory: true)
    }

    private func attachmentDirectoryURL(itemID: String) throws -> URL {
        guard isSafePathComponent(itemID) else {
            throw CocoaError(.fileReadInvalidFileName)
        }
        return attachmentDirectory(itemID: itemID)
    }

    private func attachmentURL(itemID: String, attachmentID: String) throws -> URL {
        guard isSafePathComponent(itemID), isSafePathComponent(attachmentID) else {
            throw CocoaError(.fileReadInvalidFileName)
        }
        return attachmentDirectory(itemID: itemID)
            .appendingPathComponent(attachmentID, isDirectory: false)
    }

    private func isSafePathComponent(_ value: String) -> Bool {
        !value.isEmpty
            && value != "."
            && value != ".."
            && !value.contains("/")
            && !value.contains("\\")
    }

    private func storageKey(scope: String) -> String {
        let encodedScope = Data(scope.utf8).base64EncodedString()
        return "quartet.sentMessageHistory.\(encodedScope)"
    }
}

private struct StoreError: Error, CustomStringConvertible {
    let operation: String
    let key: String
    let underlying: Error

    var description: String {
        "SentMessageHistoryStore \(operation) failed for key \(key): \(String(reflecting: underlying))"
    }
}
