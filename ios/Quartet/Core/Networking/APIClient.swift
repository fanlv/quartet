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

    func verifyAuthentication() async throws {
        let _: StatusResponse = try await request(path: "api/v1/auth/verify")
    }

    func workspaces() async throws -> WorkspacesResponse {
        try await request(path: "api/v1/workspace/list")
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

    func job(id: String) async throws -> JobDetail {
        try await request(path: "api/v1/job/\(id)")
    }

    func createJob(_ body: CreateJobRequest) async throws -> CreateJobResponse {
        try await request(path: "api/v1/job/create", method: "POST", body: body)
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
        var dataLines: [String] = []
        var eventID: UInt64?
        for try await line in bytes.lines {
            try Task.checkCancellation()
            if line.isEmpty {
                guard !dataLines.isEmpty else { continue }
                let data = dataLines.joined(separator: "\n")
                dataLines.removeAll(keepingCapacity: true)
                do {
                    let event = try Self.decode(eventType, from: Data(data.utf8))
                    await onEvent(event, eventID)
                } catch {
                    throw APIError(
                        summary: "无法解析实时事件",
                        detail: "GET \(endpoint.absoluteString)\n\n解析错误：\(String(describing: error))\n\n原始事件：\n\(data)"
                    )
                }
                eventID = nil
            } else if line.hasPrefix("data:") {
                dataLines.append(String(line.dropFirst(5)).trimmingCharacters(in: .whitespaces))
            } else if line.hasPrefix("id:") {
                eventID = UInt64(String(line.dropFirst(3)).trimmingCharacters(in: .whitespaces))
            }
        }
        if !dataLines.isEmpty {
            let data = dataLines.joined(separator: "\n")
            do {
                let event = try Self.decode(eventType, from: Data(data.utf8))
                await onEvent(event, eventID)
            } catch {
                throw APIError(
                    summary: "无法解析实时事件",
                    detail: "GET \(endpoint.absoluteString)\n\n解析错误：\(String(describing: error))\n\n原始事件：\n\(data)"
                )
            }
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
