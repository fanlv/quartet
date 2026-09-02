import Foundation

/// 本机保存的一台 Quartet 后端。`id` 直接用归一化后的 base URL 绝对串，
/// 既是唯一键也是切换时要连接的地址；密码不在此保存，登录态仍由系统 Cookie 持有。
struct ServerBookmark: Codable, Identifiable, Hashable, Sendable {
    let id: String
    var name: String
    var username: String
    var lastConnectedAt: Date?

    init(id: String, name: String = "", username: String = "", lastConnectedAt: Date? = nil) {
        self.id = id
        self.name = name
        self.username = username
        self.lastConnectedAt = lastConnectedAt
    }

    /// 备注名优先；没有备注名时退回主机名（带非默认端口），比完整 URL 更适合做单行标题。
    var displayName: String {
        let trimmedName = name.trimmingCharacters(in: .whitespacesAndNewlines)
        if !trimmedName.isEmpty { return trimmedName }
        guard let components = URLComponents(string: id), let host = components.host else { return id }
        guard let port = components.port else { return host }
        return "\(host):\(port)"
    }
}

@MainActor
final class ServerBookmarkStore {
    private struct Payload: Codable {
        let version: Int
        let items: [ServerBookmark]
    }

    private static let storageKey = "quartet.serverBookmarks"
    private static let itemLimit = 20
    private let defaults: UserDefaults

    init(defaults: UserDefaults) {
        self.defaults = defaults
    }

    func load() throws -> [ServerBookmark] {
        guard let data = defaults.data(forKey: Self.storageKey) else { return [] }
        do {
            let payload = try JSONDecoder().decode(Payload.self, from: data)
            return Array(payload.items.prefix(Self.itemLimit))
        } catch {
            throw StoreError(operation: "decode", key: Self.storageKey, underlying: error)
        }
    }

    /// 返回真正落盘的清单：超出上限的条目会被丢弃，调用方必须用返回值更新内存状态，
    /// 否则内存里会多出重启后就消失的条目。
    @discardableResult
    func save(_ bookmarks: [ServerBookmark]) throws -> [ServerBookmark] {
        let items = Array(bookmarks.prefix(Self.itemLimit))
        do {
            let data = try JSONEncoder().encode(Payload(version: 1, items: items))
            defaults.set(data, forKey: Self.storageKey)
            return items
        } catch {
            throw StoreError(operation: "encode", key: Self.storageKey, underlying: error)
        }
    }
}

private struct StoreError: Error, CustomStringConvertible {
    let operation: String
    let key: String
    let underlying: Error

    var description: String {
        "ServerBookmarkStore \(operation) failed for key \(key): \(String(reflecting: underlying))"
    }
}
