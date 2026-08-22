import Foundation

struct APIClient: @unchecked Sendable {
    let baseURL: URL
    private let token: String
    private let session: URLSession

    init(serverAddress: String, token: String, session: URLSession = .shared) throws {
        let trimmed = serverAddress.trimmingCharacters(in: .whitespacesAndNewlines)
        guard var components = URLComponents(string: trimmed),
              let scheme = components.scheme?.lowercased(),
              scheme == "https" || scheme == "http",
              components.host != nil,
              components.user == nil,
              components.password == nil else {
            throw APIError(
                summary: "服务地址无效",
                detail: "请输入不含用户名或密码的完整 HTTP/HTTPS 地址。\n当前值：\(trimmed)"
            )
        }
        let cleanPath = components.path.trimmingCharacters(in: CharacterSet(charactersIn: "/"))
        components.path = cleanPath.isEmpty ? "" : "/" + cleanPath
        components.query = nil
        components.fragment = nil
        guard let normalized = components.url else {
            throw APIError(summary: "服务地址无效", detail: "无法解析地址：\(trimmed)")
        }
        self.baseURL = normalized
        self.token = token.trimmingCharacters(in: .whitespacesAndNewlines)
        self.session = session
    }

    private static func decode<Response: Decodable>(_ type: Response.Type, from data: Data) throws -> Response {
        try JSONDecoder().decode(type, from: data)
    }

    func health() async throws -> HealthResponse {
        try await request(path: "api/v1/health", authenticated: false)
    }

    func restartHealthProbe() async throws -> HealthResponse {
        let query = [
            URLQueryItem(
                name: "restartProbe",
                value: String(Int64(Date().timeIntervalSince1970 * 1_000))
            )
        ]
        let endpoint = endpointURL(path: "api/v1/health", query: query)
        var request = URLRequest(
            url: endpoint,
            cachePolicy: .reloadIgnoringLocalCacheData,
            timeoutInterval: 3
        )
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        request.setValue("no-cache", forHTTPHeaderField: "Cache-Control")

        let data: Data
        let response: URLResponse
        do {
            (data, response) = try await session.data(for: request)
        } catch {
            throw APIError(
                summary: "无法连接 Quartet",
                detail: "GET \(endpoint.absoluteString)\n\n\(String(describing: error))"
            )
        }

        guard let http = response as? HTTPURLResponse else {
            throw APIError(
                summary: "响应无效",
                detail: "GET \(endpoint.absoluteString)\n\n服务未返回 HTTP 响应。"
            )
        }
        let body = String(data: data, encoding: .utf8) ?? "<\(data.count) bytes of non-UTF-8 data>"
        guard (200..<300).contains(http.statusCode) else {
            throw APIError(
                summary: "Quartet 请求失败",
                detail: "GET \(endpoint.absoluteString)\nHTTP \(http.statusCode)\n\n\(body)"
            )
        }

        do {
            return try Self.decode(HealthResponse.self, from: data)
        } catch {
            throw APIError(
                summary: "无法解析 Quartet 响应",
                detail: "GET \(endpoint.absoluteString)\nHTTP \(http.statusCode)\n\n解析错误：\(String(describing: error))\n\n原始响应：\n\(body)"
            )
        }
    }

    func restartWeb() async throws -> WebRestartResponse {
        try await request(path: "api/v1/system/restart-web", method: "POST")
    }

    func verifyAuthentication() async throws {
        let _: StatusResponse = try await request(path: "api/v1/auth/verify")
    }

    func workspaces() async throws -> WorkspacesResponse {
        try await request(path: "api/v1/workspace/list")
    }

    func usageStats(
        from: String?,
        to: String?,
        allTime: Bool,
        compareWithPrevious: Bool
    ) async throws -> UsageStatsReport {
        var query: [URLQueryItem] = []
        if let from, !from.isEmpty {
            query.append(URLQueryItem(name: "from", value: from))
        }
        if let to, !to.isEmpty {
            query.append(URLQueryItem(name: "to", value: to))
        }
        if allTime {
            query.append(URLQueryItem(name: "all", value: "1"))
        }
        if compareWithPrevious {
            query.append(URLQueryItem(name: "compare", value: "1"))
        }

        let endpoint = endpointURL(path: "api/v1/stats/usage", query: query)
        var urlRequest = URLRequest(url: endpoint)
        urlRequest.setValue("application/json", forHTTPHeaderField: "Accept")
        if !token.isEmpty {
            urlRequest.setValue(token, forHTTPHeaderField: "X-AGENT-AUTH")
        }

        let data: Data
        let response: URLResponse
        do {
            (data, response) = try await session.data(for: urlRequest)
        } catch {
            throw APIError(
                summary: "无法连接 Quartet",
                detail: "GET \(endpoint.absoluteString)\n\n\(String(describing: error))"
            )
        }

        guard let http = response as? HTTPURLResponse else {
            throw APIError(
                summary: "响应无效",
                detail: "GET \(endpoint.absoluteString)\n\n服务未返回 HTTP 响应。"
            )
        }
        let body = String(data: data, encoding: .utf8) ?? "<\(data.count) bytes of non-UTF-8 data>"
        guard (200..<300).contains(http.statusCode) else {
            throw APIError(
                summary: http.statusCode == 403 ? "Token 验证失败" : "Quartet 请求失败",
                detail: "GET \(endpoint.absoluteString)\nHTTP \(http.statusCode)\n\n\(body)",
                requestWasRejected: true
            )
        }

        let report: UsageStatsReport
        do {
            report = try Self.decode(UsageStatsReport.self, from: data)
        } catch {
            throw APIError(
                summary: "无法解析 Quartet 响应",
                detail: "GET \(endpoint.absoluteString)\nHTTP \(http.statusCode)\n\n解析错误：\(String(describing: error))\n\n原始响应：\n\(body)"
            )
        }
        if report.failed == true || report.error?.isEmpty == false {
            throw APIError(
                summary: "使用统计加载失败",
                detail: "GET \(endpoint.absoluteString)\nHTTP \(http.statusCode)\n\n\(body)"
            )
        }
        return report
    }

    func updateWorkspaceDefaults(_ workspace: WorkspaceSummary, defaultAgent: String, defaultModel: String) async throws -> WorkspaceSummary {
        return try await request(
            path: "api/v1/workspace/\(workspace.id)",
            method: "PATCH",
            body: UpdateWorkspaceRequest(
                expectedVersion: workspace.version,
                defaultAgent: defaultAgent,
                defaultModel: defaultModel
            )
        )
    }

    func agents() async throws -> AgentListResponse {
        try await request(path: "api/v1/agent/list")
    }

    func agentPreferences() async throws -> AgentPreferencesResponse {
        try await request(path: "api/v1/config/settings/get")
    }

    func jobs(
        workspaceID: String?,
        cursor: String? = nil,
        limit: Int = 50,
        excludeScheduled: Bool = false
    ) async throws -> JobsPage {
        var query = [URLQueryItem(name: "limit", value: String(limit))]
        if let workspaceID, !workspaceID.isEmpty {
            query.append(URLQueryItem(name: "workspaceId", value: workspaceID))
        }
        if let cursor, !cursor.isEmpty {
            query.append(URLQueryItem(name: "cursor", value: cursor))
        }
        if excludeScheduled {
            query.append(URLQueryItem(name: "excludeScheduled", value: "true"))
        }
        return try await request(path: "api/v1/job/list", query: query)
    }

    func pollJobs(
        workspaceID: String?,
        limit: Int = 50,
        excludeScheduled: Bool = false,
        etag: String?
    ) async throws -> ConditionalJobsPage {
        var query = [URLQueryItem(name: "limit", value: String(limit))]
        if let workspaceID, !workspaceID.isEmpty {
            query.append(URLQueryItem(name: "workspaceId", value: workspaceID))
        }
        if excludeScheduled {
            query.append(URLQueryItem(name: "excludeScheduled", value: "true"))
        }

        let endpoint = endpointURL(path: "api/v1/job/list", query: query)
        var request = URLRequest(url: endpoint)
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        if let etag, !etag.isEmpty {
            request.setValue(etag, forHTTPHeaderField: "If-None-Match")
        }
        if !token.isEmpty {
            request.setValue(token, forHTTPHeaderField: "X-AGENT-AUTH")
        }

        let data: Data
        let response: URLResponse
        do {
            (data, response) = try await session.data(for: request)
        } catch {
            throw APIError(
                summary: "无法连接 Quartet",
                detail: "GET \(endpoint.absoluteString)\n\n\(String(describing: error))"
            )
        }

        guard let http = response as? HTTPURLResponse else {
            throw APIError(
                summary: "响应无效",
                detail: "GET \(endpoint.absoluteString)\n\n服务未返回 HTTP 响应。"
            )
        }
        let responseETag = http.value(forHTTPHeaderField: "ETag") ?? etag
        if http.statusCode == 304 {
            return .notModified(etag: responseETag)
        }

        let body = String(data: data, encoding: .utf8) ?? "<\(data.count) bytes of non-UTF-8 data>"
        guard (200..<300).contains(http.statusCode) else {
            throw APIError(
                summary: http.statusCode == 403 ? "Token 验证失败" : "Quartet 请求失败",
                detail: "GET \(endpoint.absoluteString)\nHTTP \(http.statusCode)\n\n\(body)",
                requestWasRejected: true
            )
        }

        do {
            return .updated(try Self.decode(JobsPage.self, from: data), etag: responseETag)
        } catch {
            throw APIError(
                summary: "无法解析 Quartet 响应",
                detail: "GET \(endpoint.absoluteString)\nHTTP \(http.statusCode)\n\n解析错误：\(String(describing: error))\n\n原始响应：\n\(body)"
            )
        }
    }

    func job(id: String) async throws -> JobDetail {
        try await request(path: "api/v1/job/\(id)")
    }

    func createJob(_ body: CreateJobRequest) async throws -> CreateJobResponse {
        try await request(path: "api/v1/job/create", method: "POST", body: body)
    }

    func graphWorkflows() async throws -> GraphWorkflowListResponse {
        try await request(path: "api/v1/graph/workflow/list")
    }

    func graphWorkflow(id: String) async throws -> GraphWorkflow {
        let response: GraphWorkflowResponse = try await request(path: "api/v1/graph/workflow/\(id)")
        guard let workflow = response.workflow else {
            throw APIError(
                summary: "工作流响应为空",
                detail: "GET \(endpointURL(path: "api/v1/graph/workflow/\(id)", query: []).absoluteString)\nHTTP 200\n\n响应中缺少 workflow。"
            )
        }
        return workflow
    }

    func validateGraphWorkflow(config: GraphConfig) async throws -> GraphValidationResponse {
        try await request(
            path: "api/v1/graph/workflow/validate",
            method: "POST",
            body: GraphValidationRequest(config: config)
        )
    }

    func startGraphRun(_ body: StartGraphRunRequest) async throws -> GraphRunSummary {
        let response: StartGraphRunResponse = try await request(
            path: "api/v1/graph/run/start",
            method: "POST",
            body: body
        )
        guard let run = response.run else {
            let validation = response.errors?.map { error in
                [error.location, error.message].compactMap { $0 }.joined(separator: ": ")
            }.joined(separator: "\n")
            throw APIError(
                summary: "Graph 工作流未启动",
                detail: "POST \(endpointURL(path: "api/v1/graph/run/start", query: []).absoluteString)\nHTTP 200\n\n\(validation?.isEmpty == false ? validation! : "响应中缺少 run。")"
            )
        }
        return run
    }

    func sessionMessages(id: String) async throws -> SessionMessagesResponse {
        try await request(path: "api/v1/sessions/\(id)/messages")
    }

    func graphRunHooks(jobID: String) async throws -> GraphHookResultsResponse {
        try await request(path: "api/v1/job/\(jobID)/graph-run/hooks")
    }

    func resolveAgentDisplayInfo(ids: [String]) async throws -> ResolveAgentDisplayInfoResponse {
        try await request(
            path: "api/v1/agent/display-info/resolve",
            method: "POST",
            body: ResolveAgentDisplayInfoRequest(ids: ids)
        )
    }

    func sendMessage(jobID: String, body: SendMessageRequest) async throws -> SendMessageResponse {
        try await request(path: "api/v1/job/\(jobID)/message", method: "POST", body: body)
    }

    func uploadImage(data: Data, filename: String, mimeType: String) async throws -> String {
        let endpoint = endpointURL(path: "api/v1/upload-file", query: [])
        let boundary = "Boundary-\(UUID().uuidString)"
        var body = Data()
        body.append(Data("--\(boundary)\r\n".utf8))
        body.append(Data("Content-Disposition: form-data; name=\"file\"; filename=\"\(filename)\"\r\n".utf8))
        body.append(Data("Content-Type: \(mimeType)\r\n\r\n".utf8))
        body.append(data)
        body.append(Data("\r\n--\(boundary)--\r\n".utf8))

        var request = URLRequest(url: endpoint)
        request.httpMethod = "POST"
        request.httpBody = body
        request.setValue("multipart/form-data; boundary=\(boundary)", forHTTPHeaderField: "Content-Type")
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        if !token.isEmpty { request.setValue(token, forHTTPHeaderField: "X-AGENT-AUTH") }

        let responseData: Data
        let response: URLResponse
        do {
            (responseData, response) = try await session.data(for: request)
        } catch {
            throw APIError(summary: "图片上传失败", detail: "POST \(endpoint.absoluteString)\n\n\(String(describing: error))")
        }
        let status = (response as? HTTPURLResponse)?.statusCode ?? 0
        let raw = String(data: responseData, encoding: .utf8) ?? "<\(responseData.count) bytes of non-UTF-8 data>"
        guard (200..<300).contains(status) else {
            throw APIError(summary: "图片上传失败", detail: "POST \(endpoint.absoluteString)\nHTTP \(status)\n\n\(raw)")
        }
        do {
            let response = try Self.decode(UploadResponse.self, from: responseData)
            return response.path
        } catch {
            throw APIError(summary: "无法解析上传响应", detail: "\(String(describing: error))\n\n原始响应：\n\(raw)")
        }
    }

    func renameJob(id: String, title: String) async throws {
        let _: JobTitleResponse = try await request(
            path: "api/v1/job/\(id)/title", method: "PUT", body: JobTitleRequest(title: title)
        )
    }

    func setJobPinned(id: String, pinned: Bool) async throws {
        let _: JobPinResponse = try await request(
            path: "api/v1/job/\(id)/pin", method: "PUT", body: JobPinRequest(pinned: pinned)
        )
    }

    func deleteJob(id: String) async throws {
        let _: StatusResponse = try await request(path: "api/v1/job/\(id)", method: "DELETE")
    }

    func graphRunStatus(jobID: String) async throws -> GraphRunStatusResponse {
        try await request(path: "api/v1/job/\(jobID)/graph-run")
    }

    func graphRunAction(jobID: String, action: String) async throws -> GraphRunActionResponse {
        try await request(
            path: "api/v1/job/\(jobID)/graph-run/\(action)",
            method: "POST",
            body: EmptyRequest()
        )
    }

    func stopJob(id: String) async throws {
        let _: StatusResponse = try await request(path: "api/v1/job/\(id)/stop", method: "POST")
    }

    func streamEvents(
        jobID: String,
        lastEventID: UInt64,
        onOpen: @escaping @MainActor () async -> Void = {},
        onEvent: @escaping @MainActor (ServerEvent, UInt64?) async -> Void
    ) async throws {
        try await streamSSE(
            path: "api/v1/job/\(jobID)/events",
            lastEventID: lastEventID,
            eventType: ServerEvent.self,
            onOpen: onOpen,
            onEvent: onEvent
        )
    }

    func streamGraphEvents(
        jobID: String,
        lastEventID: UInt64,
        onOpen: @escaping @MainActor () async -> Void = {},
        onEvent: @escaping @MainActor (GraphStreamEvent, UInt64?) async -> Void
    ) async throws {
        try await streamSSE(
            path: "api/v1/job/\(jobID)/graph-run/events",
            lastEventID: lastEventID,
            eventType: GraphStreamEvent.self,
            onOpen: onOpen,
            onEvent: onEvent
        )
    }

    private func streamSSE<Event: Decodable & Sendable>(
        path: String,
        lastEventID: UInt64,
        eventType: Event.Type,
        onOpen: @escaping @MainActor () async -> Void,
        onEvent: @escaping @MainActor (Event, UInt64?) async -> Void
    ) async throws {
        let endpoint = endpointURL(path: path, query: [])
        var request = URLRequest(url: endpoint)
        request.setValue("text/event-stream", forHTTPHeaderField: "Accept")
        request.setValue(String(lastEventID), forHTTPHeaderField: "Last-Event-ID")
        if !token.isEmpty { request.setValue(token, forHTTPHeaderField: "X-AGENT-AUTH") }

        let bytes: URLSession.AsyncBytes
        let response: URLResponse
        do {
            (bytes, response) = try await session.bytes(for: request)
        } catch {
            throw APIError(
                summary: "实时连接失败",
                detail: "GET \(endpoint.absoluteString)\n\n\(String(describing: error))"
            )
        }
        guard let http = response as? HTTPURLResponse, (200..<300).contains(http.statusCode) else {
            let status = (response as? HTTPURLResponse)?.statusCode ?? 0
            var bodyLines: [String] = []
            do {
                for try await line in bytes.lines { bodyLines.append(line) }
            } catch {
                bodyLines.append("读取错误响应失败：\(String(describing: error))")
            }
            let body = bodyLines.joined(separator: "\n")
            throw APIError(
                summary: status == 410 ? "事件位置已过期" : "实时连接被拒绝",
                detail: "GET \(endpoint.absoluteString)\nHTTP \(status)\n\n\(body)"
            )
        }

        await onOpen()
        var parser = SSEFrameParser()
        var lineBytes: [UInt8] = []

        func deliver(_ frame: SSEFrame) async throws {
            do {
                let event = try Self.decode(eventType, from: Data(frame.data.utf8))
                await onEvent(event, frame.id)
            } catch {
                throw APIError(
                    summary: "无法解析实时事件",
                    detail: "GET \(endpoint.absoluteString)\n\n解析错误：\(String(describing: error))\n\n原始事件：\n\(frame.data)"
                )
            }
        }

        for try await byte in bytes {
            try Task.checkCancellation()
            guard byte == 0x0A else {
                lineBytes.append(byte)
                continue
            }
            if lineBytes.last == 0x0D {
                lineBytes.removeLast()
            }
            let line = String(decoding: lineBytes, as: UTF8.self)
            lineBytes.removeAll(keepingCapacity: true)
            if let frame = parser.consume(line) {
                try await deliver(frame)
            }
        }
        if !lineBytes.isEmpty {
            let line = String(decoding: lineBytes, as: UTF8.self)
            if let frame = parser.consume(line) {
                try await deliver(frame)
            }
        }
        if let frame = parser.finish() {
            try await deliver(frame)
        }
    }

    func fileData(path: String) async throws -> Data {
        if path.hasPrefix("data:"), let comma = path.firstIndex(of: ","), path[..<comma].contains(";base64") {
            let encoded = String(path[path.index(after: comma)...])
            guard let data = Data(base64Encoded: encoded) else {
                throw APIError(summary: "图片数据无效", detail: "无法解码 data URL。")
            }
            return data
        }
        let endpoint: URL
        let isBackendFileRequest: Bool
        if let remoteURL = URL(string: path),
           let scheme = remoteURL.scheme?.lowercased(),
           scheme == "https" || scheme == "http" {
            endpoint = remoteURL
            isBackendFileRequest = false
        } else {
            endpoint = endpointURL(
                path: "api/v1/serve-file",
                query: [URLQueryItem(name: "path", value: path)]
            )
            isBackendFileRequest = true
        }
        var request = URLRequest(url: endpoint)
        if isBackendFileRequest, !token.isEmpty {
            request.setValue(token, forHTTPHeaderField: "X-AGENT-AUTH")
        }
        do {
            let (data, response) = try await session.data(for: request)
            let status = (response as? HTTPURLResponse)?.statusCode ?? 0
            guard (200..<300).contains(status) else {
                let body = String(data: data, encoding: .utf8) ?? "<\(data.count) bytes of non-UTF-8 data>"
                throw APIError(summary: "图片加载失败", detail: "GET \(endpoint.absoluteString)\nHTTP \(status)\n\n\(body)")
            }
            return data
        } catch let error as APIError {
            throw error
        } catch {
            throw APIError(summary: "图片加载失败", detail: "GET \(endpoint.absoluteString)\n\n\(String(describing: error))")
        }
    }

    private func request<Response: Decodable & Sendable>(
        path: String,
        method: String = "GET",
        query: [URLQueryItem] = [],
        authenticated: Bool = true
    ) async throws -> Response {
        try await request(path: path, method: method, query: query, bodyData: nil, authenticated: authenticated)
    }

    private func request<Response: Decodable & Sendable, Body: Encodable & Sendable>(
        path: String,
        method: String,
        query: [URLQueryItem] = [],
        body: Body,
        authenticated: Bool = true
    ) async throws -> Response {
        let bodyData: Data
        do {
            bodyData = try JSONEncoder().encode(body)
        } catch {
            throw APIError(
                summary: "无法编码请求",
                detail: String(describing: error),
                requestWasRejected: true
            )
        }
        return try await request(path: path, method: method, query: query, bodyData: bodyData, authenticated: authenticated)
    }

    private func request<Response: Decodable & Sendable>(
        path: String,
        method: String,
        query: [URLQueryItem],
        bodyData: Data?,
        authenticated: Bool
    ) async throws -> Response {
        let endpoint = endpointURL(path: path, query: query)
        var request = URLRequest(url: endpoint)
        request.httpMethod = method
        request.setValue("application/json", forHTTPHeaderField: "Accept")
        if let bodyData {
            request.httpBody = bodyData
            request.setValue("application/json", forHTTPHeaderField: "Content-Type")
        }
        if authenticated, !token.isEmpty {
            request.setValue(token, forHTTPHeaderField: "X-AGENT-AUTH")
        }

        let data: Data
        let response: URLResponse
        do {
            (data, response) = try await session.data(for: request)
        } catch {
            throw APIError(
                summary: "无法连接 Quartet",
                detail: "\(method) \(endpoint.absoluteString)\n\n\(String(describing: error))"
            )
        }

        guard let http = response as? HTTPURLResponse else {
            throw APIError(
                summary: "响应无效",
                detail: "\(method) \(endpoint.absoluteString)\n\n服务未返回 HTTP 响应。"
            )
        }
        let body = String(data: data, encoding: .utf8) ?? "<\(data.count) bytes of non-UTF-8 data>"
        guard (200..<300).contains(http.statusCode) else {
            throw APIError(
                summary: http.statusCode == 403 ? "Token 验证失败" : "Quartet 请求失败",
                detail: "\(method) \(endpoint.absoluteString)\nHTTP \(http.statusCode)\n\n\(body)",
                requestWasRejected: true
            )
        }

        do {
            return try Self.decode(Response.self, from: data)
        } catch {
            throw APIError(
                summary: "无法解析 Quartet 响应",
                detail: "\(method) \(endpoint.absoluteString)\nHTTP \(http.statusCode)\n\n解析错误：\(String(describing: error))\n\n原始响应：\n\(body)"
            )
        }
    }

    private func endpointURL(path: String, query: [URLQueryItem]) -> URL {
        var url = baseURL
        for component in path.split(separator: "/") {
            url = url.appendingPathComponent(String(component))
        }
        guard !query.isEmpty, var components = URLComponents(url: url, resolvingAgainstBaseURL: false) else {
            return url
        }
        components.queryItems = query
        return components.url ?? url
    }

}

private struct SSEFrame {
    let id: UInt64?
    let data: String
}

private struct SSEFrameParser {
    private var eventID: UInt64?
    private var dataLines: [String] = []

    mutating func consume(_ line: String) -> SSEFrame? {
        if line.isEmpty {
            return finish()
        }
        if line.hasPrefix("data:") {
            dataLines.append(fieldValue(line, prefixLength: 5))
        } else if line.hasPrefix("id:") {
            eventID = UInt64(fieldValue(line, prefixLength: 3))
        }
        return nil
    }

    mutating func finish() -> SSEFrame? {
        guard !dataLines.isEmpty else {
            eventID = nil
            return nil
        }
        let frame = SSEFrame(id: eventID, data: dataLines.joined(separator: "\n"))
        eventID = nil
        dataLines.removeAll(keepingCapacity: true)
        return frame
    }

    private func fieldValue(_ line: String, prefixLength: Int) -> String {
        var value = line.dropFirst(prefixLength)
        if value.first == " " {
            value.removeFirst()
        }
        return String(value)
    }
}

enum ConditionalJobsPage: Sendable {
    case notModified(etag: String?)
    case updated(JobsPage, etag: String?)
}

struct APIError: Error, Sendable {
    let summary: String
    let detail: String
    let requestWasRejected: Bool

    init(summary: String, detail: String, requestWasRejected: Bool = false) {
        self.summary = summary
        self.detail = detail
        self.requestWasRejected = requestWasRejected
    }
}

private struct StatusResponse: Decodable, Sendable {
    let status: String?
    let code: Int?
}

private struct JobTitleRequest: Encodable, Sendable { let title: String }
private struct JobTitleResponse: Decodable, Sendable { let title: String }
private struct JobPinRequest: Encodable, Sendable { let pinned: Bool }
private struct JobPinResponse: Decodable, Sendable { let pinned: Bool; let pinnedAt: Int64; let updatedAt: Int64 }
private struct EmptyRequest: Encodable, Sendable {}
