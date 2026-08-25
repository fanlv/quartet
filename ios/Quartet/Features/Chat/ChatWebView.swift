import Foundation
import SwiftUI
import WebKit

struct ChatWebDestination: Identifiable {
    let id = UUID()
    let url: URL
    let title: String
}

/// 与 Web `MessageItem` 保持一致的聊天链接判定和文件路径转换。
enum ChatLinkTarget {
    static let fileScheme = "quartet-file"

    private static let localPathExpression = try! NSRegularExpression(
        pattern: #"((?:\.{1,2}/)?(?:/)?(?:[\w./_-]+\.[a-zA-Z][\w]*|[\w./_-]*\.[a-zA-Z][\w.-]*)(?::\d+(?:-\d+)?(?::\d+)?)?(?:#L\d+(?:-L?\d+)?(?:C\d+)?)?)"#
    )

    static func fileLinkURL(for target: String) -> URL? {
        var components = URLComponents()
        components.scheme = fileScheme
        components.host = "open"
        components.queryItems = [URLQueryItem(name: "target", value: target)]
        return components.url
    }

    static func fileTarget(from url: URL) -> String? {
        guard url.scheme?.lowercased() == fileScheme,
              let components = URLComponents(url: url, resolvingAgainstBaseURL: false) else {
            return nil
        }
        return components.queryItems?.first(where: { $0.name == "target" })?.value
    }

    static func isLocalFileTarget(_ rawTarget: String) -> Bool {
        let target = rawTarget.replacingOccurrences(of: #"\/"#, with: "/")
        let clean = target.replacingOccurrences(
            of: #"[#:].*$"#,
            with: "",
            options: .regularExpression
        )
        let basename = clean.split(separator: "/", omittingEmptySubsequences: false).last.map(String.init) ?? ""
        let isDotfile = matches(#"^\.[a-zA-Z][\w.-]*$"#, in: basename)
        let hasFileExtension = matches(#"^.+\.[a-zA-Z]\w*$"#, in: basename)
        guard hasFileExtension || isDotfile else { return false }

        if target.hasPrefix("/"), !target.hasPrefix("//") { return true }
        if target.hasPrefix("./") || target.hasPrefix("../") { return true }
        if isDotfile { return true }
        return matches(#"^[\w][\w./_-]*\.[a-zA-Z]+"#, in: clean)
    }

    static func normalizeFileTarget(_ rawTarget: String) -> String {
        let decoded = rawTarget.removingPercentEncoding ?? rawTarget
        return decoded
            .replacingOccurrences(of: #"\/"#, with: "/")
            .replacingOccurrences(
                of: #"#L\d+(-L?\d+)?(C\d+)?$"#,
                with: "",
                options: .regularExpression
            )
            .replacingOccurrences(
                of: #":\d+(-\d+)?(:\d+)?$"#,
                with: "",
                options: .regularExpression
            )
    }

    static func resolvedFilePath(target: String, workdir: String?) -> String? {
        let filePath = normalizeFileTarget(target).trimmingCharacters(in: .whitespacesAndNewlines)
        guard !filePath.isEmpty else { return nil }
        if filePath.hasPrefix("/") {
            return URL(fileURLWithPath: filePath).standardizedFileURL.path
        }
        guard let workdir = workdir?.trimmingCharacters(in: .whitespacesAndNewlines),
              !workdir.isEmpty else {
            return nil
        }
        return URL(fileURLWithPath: workdir, isDirectory: true)
            .appendingPathComponent(filePath)
            .standardizedFileURL
            .path
    }

    static func filePreviewURL(baseURL: URL, path: String, jobID: String) -> URL? {
        guard var components = URLComponents(url: baseURL, resolvingAgainstBaseURL: false) else {
            return nil
        }
        components.fragment = nil
        components.queryItems = [
            URLQueryItem(name: "view", value: "file-preview"),
            URLQueryItem(name: "path", value: path),
            URLQueryItem(name: "jobId", value: jobID)
        ]
        return components.url
    }

    /// 把普通 Markdown 链接中的文件路径和正文里的裸文件路径都标成内部文件链接。
    static func decorateFileLinks(in attributed: inout AttributedString) {
        let markdownLinks = attributed.runs.compactMap { run -> (Range<AttributedString.Index>, URL)? in
            guard let link = run.link else { return nil }
            return (run.range, link)
        }
        for (range, link) in markdownLinks where isLocalFileTarget(link.absoluteString) {
            attributed[range].link = fileLinkURL(for: link.absoluteString)
        }

        let visibleText = String(attributed.characters)
        let matches = localPathExpression.matches(
            in: visibleText,
            range: NSRange(visibleText.startIndex..., in: visibleText)
        )
        for match in matches {
            guard let stringRange = Range(match.range(at: 1), in: visibleText) else { continue }
            let target = String(visibleText[stringRange])
            guard isLocalFileTarget(target),
                  let lower = AttributedString.Index(stringRange.lowerBound, within: attributed),
                  let upper = AttributedString.Index(stringRange.upperBound, within: attributed) else {
                continue
            }
            let range = lower..<upper
            let alreadySemantic = attributed[range].runs.contains { run in
                run.link != nil || run.inlinePresentationIntent?.contains(.code) == true
            }
            guard !alreadySemantic else { continue }
            attributed[range].link = fileLinkURL(for: target)
        }
    }

    private static func matches(_ pattern: String, in value: String) -> Bool {
        value.range(of: pattern, options: .regularExpression) != nil
    }
}

/// `OpenURLAction` 必须保持稳定，避免聊天时间线每次重绘都生成新动作、连带重建所有气泡。
@MainActor
final class ChatLinkOpener {
    var presentError: ((APIError) -> Void)?
    var presentDestination: ((ChatWebDestination) -> Void)?

    private var baseURL: URL?
    private var workdir: String?
    private var jobID = ""
    private var canReadFiles = false

    private(set) lazy var action = OpenURLAction { [weak self] url in
        self?.open(url)
        return .handled
    }

    func configure(baseURL: URL?, workdir: String?, jobID: String, canReadFiles: Bool) {
        self.baseURL = baseURL
        self.workdir = workdir
        self.jobID = jobID
        self.canReadFiles = canReadFiles
    }

    private func open(_ url: URL) {
        if let target = ChatLinkTarget.fileTarget(from: url) {
            openFile(target)
            return
        }

        let destinationURL: URL?
        if let scheme = url.scheme?.lowercased(), !scheme.isEmpty {
            destinationURL = ["http", "https"].contains(scheme) ? url : nil
        } else if let baseURL {
            destinationURL = URL(string: url.absoluteString, relativeTo: baseURL)?.absoluteURL
        } else {
            destinationURL = nil
        }

        guard let destinationURL,
              let scheme = destinationURL.scheme?.lowercased(),
              ["http", "https"].contains(scheme) else {
            presentError?(APIError(
                summary: "链接已拦截",
                detail: "仅允许在 App 内打开 http/https 链接。\n当前链接：\n\(url.absoluteString)"
            ))
            return
        }
        presentDestination?(ChatWebDestination(
            url: destinationURL,
            title: destinationURL.host ?? "网页"
        ))
    }

    private func openFile(_ target: String) {
        guard canReadFiles else {
            presentError?(APIError(
                summary: "无法预览文件",
                detail: "当前账号缺少 file.read 权限。\n文件路径：\n\(target)"
            ))
            return
        }
        guard let path = ChatLinkTarget.resolvedFilePath(target: target, workdir: workdir) else {
            presentError?(APIError(
                summary: "无法解析文件路径",
                detail: "相对文件路径需要当前 Job 的工作目录。\n文件路径：\n\(target)\n工作目录：\n\(workdir ?? "<empty>")"
            ))
            return
        }
        guard let baseURL,
              let previewURL = ChatLinkTarget.filePreviewURL(baseURL: baseURL, path: path, jobID: jobID) else {
            presentError?(APIError(
                summary: "无法生成文件 URL",
                detail: "Quartet 服务地址或文件预览 URL 无效。\n服务地址：\n\(baseURL?.absoluteString ?? "<empty>")\n文件路径：\n\(path)\nJob ID：\n\(jobID)"
            ))
            return
        }
        presentDestination?(ChatWebDestination(
            url: previewURL,
            title: URL(fileURLWithPath: path).lastPathComponent
        ))
    }
}

struct ChatWebViewPage: View {
    @Environment(\.dismiss) private var dismiss

    let destination: ChatWebDestination
    let onError: (APIError) -> Void

    var body: some View {
        ChatWebView(url: destination.url, onError: onError)
            .ignoresSafeArea(.container, edges: .bottom)
            .navigationTitle(destination.title)
            .navigationBarTitleDisplayMode(.inline)
            .toolbar {
                ToolbarItem(placement: .topBarLeading) {
                    Button("关闭") { dismiss() }
                }
            }
    }
}

private struct ChatWebView: UIViewRepresentable {
    let url: URL
    let onError: (APIError) -> Void

    func makeCoordinator() -> Coordinator {
        Coordinator(onError: onError)
    }

    func makeUIView(context: Context) -> WKWebView {
        let configuration = WKWebViewConfiguration()
        configuration.websiteDataStore = .default()
        let webView = WKWebView(frame: .zero, configuration: configuration)
        webView.navigationDelegate = context.coordinator
        webView.uiDelegate = context.coordinator
        webView.allowsBackForwardNavigationGestures = true
        context.coordinator.loadIfNeeded(url, in: webView)
        return webView
    }

    func updateUIView(_ webView: WKWebView, context: Context) {
        context.coordinator.onError = onError
        context.coordinator.loadIfNeeded(url, in: webView)
    }

    @MainActor
    final class Coordinator: NSObject, WKNavigationDelegate, WKUIDelegate {
        var onError: (APIError) -> Void
        private var requestedURL: URL?

        init(onError: @escaping (APIError) -> Void) {
            self.onError = onError
        }

        func loadIfNeeded(_ url: URL, in webView: WKWebView) {
            guard requestedURL != url else { return }
            requestedURL = url
            let cookies = HTTPCookieStorage.shared.cookies(for: url) ?? []
            install(cookies, at: 0, into: webView.configuration.websiteDataStore.httpCookieStore) {
                webView.load(URLRequest(url: url))
            }
        }

        private func install(
            _ cookies: [HTTPCookie],
            at index: Int,
            into store: WKHTTPCookieStore,
            completion: @escaping () -> Void
        ) {
            guard index < cookies.count else {
                completion()
                return
            }
            store.setCookie(cookies[index]) { [weak self] in
                self?.install(cookies, at: index + 1, into: store, completion: completion)
            }
        }

        func webView(
            _ webView: WKWebView,
            createWebViewWith configuration: WKWebViewConfiguration,
            for navigationAction: WKNavigationAction,
            windowFeatures: WKWindowFeatures
        ) -> WKWebView? {
            if navigationAction.targetFrame == nil {
                webView.load(navigationAction.request)
            }
            return nil
        }

        func webView(
            _ webView: WKWebView,
            decidePolicyFor navigationAction: WKNavigationAction
        ) async -> WKNavigationActionPolicy {
            guard let destinationURL = navigationAction.request.url,
                  let scheme = destinationURL.scheme?.lowercased(),
                  !["http", "https"].contains(scheme) else {
                return .allow
            }
            onError(APIError(
                summary: "链接已拦截",
                detail: "WebView 仅允许打开 http/https 链接。\n当前链接：\n\(destinationURL.absoluteString)"
            ))
            return .cancel
        }

        func webView(_ webView: WKWebView, didFail navigation: WKNavigation!, withError error: Error) {
            report(error, url: webView.url ?? requestedURL)
        }

        func webView(_ webView: WKWebView, didFailProvisionalNavigation navigation: WKNavigation!, withError error: Error) {
            report(error, url: webView.url ?? requestedURL)
        }

        private func report(_ error: Error, url: URL?) {
            let nsError = error as NSError
            guard nsError.code != NSURLErrorCancelled else { return }
            onError(APIError(
                summary: "网页加载失败",
                detail: "GET \(url?.absoluteString ?? "<empty>")\n\n错误：\n\(String(reflecting: error))\n\nNSError：\nDomain=\(nsError.domain)\nCode=\(nsError.code)\nDescription=\(nsError.localizedDescription)\nUserInfo=\(nsError.userInfo)"
            ))
        }
    }
}
