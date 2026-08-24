import ImageIO
import UIKit

/// 聊天时间线里同一个 Agent 头像会出现在每一条 assistant 消息上，而消息视图会随
/// SwiftUI 的重新求值反复触发 `.task`。没有缓存时这意味着「一条消息一个 HTTP 请求」，
/// 滚走再滚回来还会重新下载一遍。
///
/// 这里做三件事：
/// 1. 解码后的 `UIImage` 按「服务地址 + 路径 + 目标尺寸」缓存，命中即同步返回；
/// 2. 在途请求按同一个键合并，20 条消息共用一个头像只会真正发 1 个请求；
/// 3. 按展示尺寸降采样，避免把原图全尺寸位图留在内存里。
@MainActor
final class ChatImageLoader {
    static let shared = ChatImageLoader()

    /// 头像等小图的展示边长上限，用于降采样。传 `nil` 表示按原图解码。
    typealias PixelSize = CGFloat

    private let cache: NSCache<NSString, UIImage> = {
        let cache = NSCache<NSString, UIImage>()
        cache.countLimit = 120
        // 以解码后位图字节数计费，约 48MB 上限。
        cache.totalCostLimit = 48 * 1024 * 1024
        return cache
    }()

    private var inFlight: [String: Task<UIImage, Error>] = [:]

    private init() {}

    /// 载入并缓存图片。`maxPixelSize` 为展示区域的最大边长（point 会在内部换成 pixel）；
    /// 传 `nil` 表示不降采样。
    func image(
        path: String,
        namespace: String,
        maxPixelSize: PixelSize?,
        loader: @escaping @Sendable () async throws -> Data
    ) async throws -> UIImage {
        let key = cacheKey(path: path, namespace: namespace, maxPixelSize: maxPixelSize)
        if let cached = cache.object(forKey: key as NSString) {
            return cached
        }
        if let existing = inFlight[key] {
            return try await existing.value
        }
        // 屏幕缩放只能在主线程读，先取出来交给下面的解码用。
        let scale = max(UITraitCollection.current.displayScale, 1)

        let task = Task<UIImage, Error> {
            let data = try await loader()
            return try await Self.decode(data, maxPixelSize: maxPixelSize, scale: scale, path: path)
        }
        inFlight[key] = task

        do {
            let image = try await task.value
            inFlight[key] = nil
            cache.setObject(image, forKey: key as NSString, cost: Self.cost(of: image))
            return image
        } catch {
            inFlight[key] = nil
            throw error
        }
    }

    private func cacheKey(path: String, namespace: String, maxPixelSize: PixelSize?) -> String {
        let size = maxPixelSize.map { String(Int($0)) } ?? "orig"
        return "\(namespace)|\(size)|\(path)"
    }

    private static func cost(of image: UIImage) -> Int {
        guard let cgImage = image.cgImage else { return 1 }
        return cgImage.bytesPerRow * cgImage.height
    }

    /// 用 ImageIO 直接解出目标尺寸的缩略图：比 `UIImage(data:)` 之后再缩放少一次全尺寸位图。
    ///
    /// 标成 `nonisolated async` 是为了让解码离开主线程 —— 原实现在 `.task` 里解码，
    /// 继承的是 MainActor，大图会直接卡住滚动。
    private nonisolated static func decode(
        _ data: Data,
        maxPixelSize: PixelSize?,
        scale: CGFloat,
        path: String
    ) async throws -> UIImage {
        func fallback() throws -> UIImage {
            guard let image = UIImage(data: data) else {
                throw APIError(summary: "图片数据无效", detail: "无法将 \(path) 解码为图片。")
            }
            return image
        }
        guard let maxPixelSize else { return try fallback() }
        let pixelLimit = max(Int((maxPixelSize * scale).rounded()), 1)
        guard let source = CGImageSourceCreateWithData(data as CFData, nil) else {
            return try fallback()
        }
        let options: [CFString: Any] = [
            kCGImageSourceCreateThumbnailFromImageAlways: true,
            kCGImageSourceCreateThumbnailWithTransform: true,
            kCGImageSourceShouldCacheImmediately: true,
            kCGImageSourceThumbnailMaxPixelSize: pixelLimit,
        ]
        guard let thumbnail = CGImageSourceCreateThumbnailAtIndex(source, 0, options as CFDictionary) else {
            return try fallback()
        }
        return UIImage(cgImage: thumbnail, scale: scale, orientation: .up)
    }
}
