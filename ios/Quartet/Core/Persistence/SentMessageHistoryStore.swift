import Foundation

struct SentMessageHistoryItem: Codable, Hashable, Identifiable, Sendable {
    let id: String
    let createdAt: Int64
    let content: String
}

@MainActor
final class SentMessageHistoryStore {
    private struct Payload: Codable {
        let version: Int
        let items: [SentMessageHistoryItem]
    }

    private static let itemLimit = 50
    private let defaults: UserDefaults

    init(defaults: UserDefaults) {
        self.defaults = defaults
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
    func append(content: String, scope: String) throws -> [SentMessageHistoryItem] {
        let content = content.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !content.isEmpty else { return try items(scope: scope) }

        let key = storageKey(scope: scope)
        var history = try items(scope: scope)
        history.insert(
            SentMessageHistoryItem(
                id: UUID().uuidString.lowercased(),
                createdAt: Int64(Date().timeIntervalSince1970 * 1_000),
                content: content
            ),
            at: 0
        )
        history = Array(history.prefix(Self.itemLimit))

        do {
            let data = try JSONEncoder().encode(Payload(version: 1, items: history))
            defaults.set(data, forKey: key)
            return history
        } catch {
            throw StoreError(operation: "encode", key: key, underlying: error)
        }
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
