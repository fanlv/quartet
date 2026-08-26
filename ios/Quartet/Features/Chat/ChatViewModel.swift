import SwiftUI
import UIKit

struct ComposerDraft: Hashable, Sendable {
    let text: String
    let attachment: PendingUpload?
}

struct OutboxRequestContext: Hashable, Sendable {
    let targetSessionID: String?
    let modelID: String?
    let agentType: String?
    let modeID: String?
    let thoughtLevelID: String?
    let bypassCommand: Bool
}

struct LocalOutboxItem: Identifiable, Hashable, Sendable {
    enum State: Hashable, Sendable {
        case queued
        case uploading
        case sending
        case awaitingEcho
        case failed(detail: String, requiresNewMessageID: Bool)
    }

    let id: String
    let draft: ComposerDraft
    let createdAt: Int64
    let isInitialDraft: Bool
    let requestContext: OutboxRequestContext
    var remoteImagePaths: [String]
    var remoteFileAttachments: [FileAttachment]
    var state: State

    var displayText: String {
        let trimmed = draft.text.trimmingCharacters(in: .whitespacesAndNewlines)
        if trimmed.isEmpty, let attachment = draft.attachment { return attachment.isImage ? "[image]" : "[file]" }
        return trimmed.isEmpty ? "…" : trimmed
    }

    var attachment: PendingUpload? {
        draft.attachment
    }

    var statusTitle: String {
        switch state {
        case .queued: "QUEUED"
        case .uploading: "UPLOADING"
        case .sending: "SENDING"
        case .awaitingEcho: "SYNCING"
        case .failed: "FAILED"
        }
    }

    var failureDetail: String? {
        if case let .failed(detail, _) = state { return detail }
        return nil
    }

    var isFailed: Bool {
        if case .failed = state { return true }
        return false
    }

    var isCancelable: Bool {
        if case .queued = state { return true }
        return false
    }

    var retryRequiresNewMessageID: Bool {
        if case let .failed(_, requiresNewMessageID) = state { return requiresNewMessageID }
        return false
    }

    var isVisibleInTimeline: Bool {
        if case .awaitingEcho = state { return true }
        return false
    }

    var summaryLine: String {
        let trimmed = draft.text.trimmingCharacters(in: .whitespacesAndNewlines)
        if !trimmed.isEmpty { return trimmed }
        if let attachment = draft.attachment { return (attachment.isImage ? "[image]" : "[file]").localizedForApp }
        return "空消息".localizedForApp
    }
}

enum StreamConnectionState: Equatable {
    case offline
    case connecting
    case live
    case reconnecting
}

@MainActor
final class ChatViewModel: ObservableObject {
    @Published var messages: [ChatMessage] = []
    @Published var outbox: [LocalOutboxItem] = []
    @Published var serverQueue = MessageQueueSnapshot(jobId: "", version: 0, paused: false, pauseReason: nil, willContinue: false, active: nil, items: [])
    @Published var deletingQueueIDs: Set<String> = []
    @Published var title = ""
    @Published private(set) var authoritativeTitleVersion = 0
    @Published var status = "pending"
    @Published var loading = true
    @Published var sending = false
    @Published private var streamState: StreamConnectionState = .offline
    @Published var errorDetail: String?
    @Published var restoreDraft: ComposerDraft?
    @Published var restoreDraftVersion = 0
    @Published var scrollAnchor = 0
    @Published var terminalStateVersion = 0
    @Published private(set) var totalTokens = 0
    @Published private(set) var runStartedAt: Int64?
    @Published private(set) var runFinishedAt: Int64?
    @Published private(set) var accumulatedDurationMs: Int64 = 0

    private var client: APIClient?
    private var jobID = ""
    private(set) var sessionID: String?
    private var preferredSessionID: String?
    @Published private var modelID: String?
    @Published private var agentType: String?
    @Published private var agentDisplayName: String?
    @Published private var agentIconUrl: String?
    @Published private var modeID: String?
    @Published private var thoughtLevelID: String?
    private var lastEventID: UInt64 = 0
    private var lastGraphEventID: UInt64 = 0
    private var streamTask: Task<Void, Never>?
    private var streamGeneration: UInt64 = 0
    private var pendingDeltas: [PendingDelta] = []
    private var deltaFlushTask: Task<Void, Never>?
    private var scrollAnchorThrottleTask: Task<Void, Never>?
    private var graphReconcileTask: Task<Void, Never>?
    private var graphMonitorTask: Task<Void, Never>?
    private var didSeedInitialDraft = false
    private var knownQueuedItems: [String: QueuedJobMessage] = [:]
    private var agentDisplayInfoByReference: [String: AgentDisplayInfo] = [:]
    private var currentTurnIncludedInAccumulatedDuration = true
    private var interactiveAccumulatedDurationMs: Int64 = 0
    private var graphBaseDurationMs: Int64 = 0
    private var graphRunningStartedAts: [Int64] = []
    private var serverClockAnchor: (serverTimeMs: Int64, uptime: TimeInterval)?
    private var isTurnRunning = false
    private var isProcessingOutbox = false
    private var isGraph = false
    private var graphRunLive = false

    var isRunning: Bool { isTurnRunning }
    var expectsExecution: Bool {
        if isTurnRunning { return true }
        return outbox.contains { item in
            switch item.state {
            case .queued, .uploading, .sending, .awaitingEcho:
                return true
            case .failed:
                return false
            }
        }
    }
    var hasQueuedMessages: Bool {
        outbox.contains { if case .queued = $0.state { return true } else { return false } }
    }
    var timelineOutboxItems: [LocalOutboxItem] {
        // 常态下 outbox 是空的，先短路掉全量 ID Set 的构造 —— 这两个属性都在 body 里读。
        guard !outbox.isEmpty else { return [] }
        let optimisticMessageIDs = Set(messages.map(\.id))
        return outbox.filter { $0.isVisibleInTimeline && !optimisticMessageIDs.contains($0.id) }
    }
    var composerOutboxItems: [LocalOutboxItem] {
        guard !outbox.isEmpty else { return [] }
        let optimisticMessageIDs = Set(messages.map(\.id))
        return outbox.filter { item in
            switch item.state {
            case .queued, .uploading, .sending:
                return !optimisticMessageIDs.contains(item.id)
            case .failed:
                return true
            case .awaitingEcho:
                return false
            }
        }
    }
    var agentDisplayLabel: String {
        displayValue(agentDisplayName) ?? displayValue(agentType) ?? "未指定 Agent".localizedForApp
    }
    var agentDisplayIconUrl: String? { displayValue(agentIconUrl) }
    var agentRuntimeType: String? { displayValue(agentType) }
    var configurationSessionID: String? { displayValue(preferredSessionID) ?? displayValue(sessionID) }
    var modelIDForDisplay: String? { displayValue(modelID) }
    var agentReferenceForDisplay: String? { displayValue(agentType) }
    var modeIDForDisplay: String? { displayValue(modeID) }
    var thoughtLevelIDForDisplay: String? { displayValue(thoughtLevelID) }
    var tokenCountLabel: String { "Tokens: \(Self.compactCount(totalTokens))" }
    var tokenCountAccessibilityLabel: String { "Token 数量，\(totalTokens)" }
    var showsDuration: Bool {
        accumulatedDurationMs > 0 || !graphRunningStartedAts.isEmpty
            || (runStartedAt != nil && !currentTurnIncludedInAccumulatedDuration)
    }

    func durationLabel(at _: Date) -> String {
        let now = estimatedServerNow()
        let graphLiveDuration = graphRunningStartedAts.reduce(Int64(0)) { total, start in
            total + max(0, now - start)
        }
        let currentTurnDuration: Int64
        if let runStartedAt, !currentTurnIncludedInAccumulatedDuration {
            currentTurnDuration = max(0, (runFinishedAt ?? now) - runStartedAt)
        } else {
            currentTurnDuration = 0
        }
        return Self.formatDuration(accumulatedDurationMs + graphLiveDuration + currentTurnDuration)
    }

    func applyACPConfiguration(
        _ response: SetACPConfigResponse,
        target: ACPConfigTarget,
        selectedModelID: String?,
        selectedThoughtLevelID: String?
    ) {
        if let models = response.models {
            modelID = displayValue(models.currentModelId) ?? modelID
        } else if target == .model {
            modelID = displayValue(selectedModelID) ?? modelID
        }

        if let thoughtLevels = response.thoughtLevels {
            thoughtLevelID = displayValue(thoughtLevels.currentThoughtLevelId)
        } else if target == .model {
            thoughtLevelID = nil
        } else if target == .thoughtLevel {
            thoughtLevelID = displayValue(selectedThoughtLevelID) ?? thoughtLevelID
        }
    }

    func reconcileThoughtLevelID(_ refreshedThoughtLevelID: String) {
        thoughtLevelID = displayValue(refreshedThoughtLevelID)
    }

    func start(route: ChatRoute, client: APIClient) async {
        stopStreaming()
        let changesJob = !jobID.isEmpty && jobID != route.summary.id
        if changesJob {
            messages = []
            outbox = []
            serverQueue = MessageQueueSnapshot(jobId: route.summary.id, version: 0, paused: false, pauseReason: nil, willContinue: false, active: nil, items: [])
            sessionID = nil
            agentDisplayName = nil
            agentIconUrl = nil
            agentDisplayInfoByReference = [:]
            totalTokens = 0
            runStartedAt = nil
            runFinishedAt = nil
            accumulatedDurationMs = 0
            currentTurnIncludedInAccumulatedDuration = true
            interactiveAccumulatedDurationMs = 0
            graphBaseDurationMs = 0
            graphRunningStartedAts = []
            serverClockAnchor = nil
            knownQueuedItems = [:]
        }
        self.client = client
        jobID = route.summary.id
        isGraph = route.summary.mode == "graph"
        title = route.summary.displayTitle
        status = route.summary.status
        preferredSessionID = route.targetSessionID
        modelID = route.modelID ?? route.summary.modelId
        agentType = route.agentType
        modeID = route.modeID
        thoughtLevelID = route.thoughtLevelID
        loading = true
        errorDetail = nil
        let seededInitialDraftID = seedInitialDraftIfNeeded(route: route)

        do {
            let detail = try await client.job(id: jobID)
            updateServerClock(detail.serverTime)
            if !isGraph {
                do {
                    applyServerQueue(try await client.messageQueue(jobID: jobID))
                } catch {
                    errorDetail = errorText(error)
                }
            }
            applyAuthoritativeTitle(detail.title)
            status = detail.status
            applyInteractiveAccumulatedDuration(detail.totalTurnDurationMs)
            runStartedAt = detail.status == "running" ? detail.startedAt : nil
            runFinishedAt = nil
            currentTurnIncludedInAccumulatedDuration = detail.status != "running"
            lastEventID = detail.lastEventSeq
            lastGraphEventID = 0
            var graphDefaultSessionID: String?
            if isGraph {
                let graphSnapshot = try await client.graphRunStatus(jobID: jobID)
                applyGraphDuration(graphSnapshot)
                let graphStatus = graphSnapshot.run?.status
                graphRunLive = graphStatus.map(isLiveGraphStatus) ?? false
                graphDefaultSessionID = latestGraphSessionID(in: graphSnapshot)
                if detail.status != "running", let graphStatus {
                    status = graphStatus
                }
            } else {
                graphRunLive = false
            }
            isTurnRunning = graphRunLive
                || detail.status == "running"
                || (detail.status == "pending" && hasPriorConversation(detail))
                || serverQueue.willContinue

            let interactiveSessions = detail.sessionIds ?? []
            let fallbackSession = interactiveSessions.last ?? detail.graphSessionIds?.last
            let requestedSession = preferredSessionID?.trimmingCharacters(in: .whitespacesAndNewlines)
            sessionID = (requestedSession?.isEmpty == false ? requestedSession : nil) ?? graphDefaultSessionID ?? fallbackSession
            if interactiveSessions.isEmpty && requestedSession?.isEmpty != false {
                agentType = detail.initialAgentId ?? agentType
                modelID = detail.firstModelId ?? modelID
                modeID = detail.initialAcpMode ?? modeID
                thoughtLevelID = detail.initialAcpThoughtLevel ?? thoughtLevelID
            }
            if let reference = displayValue(agentType),
               let agentInfo = await resolveAgentDisplayInfo(reference: reference) {
                agentDisplayName = resolvedAgentName(agentInfo)
                agentIconUrl = displayValue(agentInfo.iconUrl)
            }

            if requestedSession?.isEmpty == false, let sessionID {
                try await loadHistory(sessionID: sessionID)
            } else if !interactiveSessions.isEmpty {
                try await loadInteractiveHistory(sessionIDs: interactiveSessions)
            } else if let sessionID {
                try await loadHistory(sessionID: sessionID)
            } else {
                messages = []
            }
            if !isGraph { applyServerQueue(serverQueue) }

            loading = false
            if isTurnRunning || sessionID != nil || route.initialMessage != nil || route.initialAttachment != nil || route.initialImagePaths != nil || route.initialFileAttachments != nil {
                startStreaming()
            }
            scheduleOutboxProcessing()
        } catch {
            loading = false
            let detail = errorText(error)
            errorDetail = detail
            restoreInitialDraftIfNeeded(id: seededInitialDraftID, detail: detail)
        }
    }

    func startUITestPreview(route: ChatRoute) {
        stopStreaming()
        jobID = route.summary.id
        isGraph = route.summary.mode == "graph"
        graphRunLive = isGraph && ["pending", "running", "stepStopping"].contains(route.summary.status)
        title = route.summary.displayTitle
        status = route.summary.status
        sessionID = "session-preview"
        modelID = route.modelID ?? route.summary.modelId
        agentType = route.agentType ?? route.summary.agentId
        modeID = route.modeID ?? route.summary.acpMode
        thoughtLevelID = route.thoughtLevelID ?? route.summary.acpThoughtLevel
        agentDisplayName = "TraeCode"
        agentIconUrl = "✨"
        totalTokens = 12_480
        runStartedAt = Int64(Date().addingTimeInterval(-83).timeIntervalSince1970 * 1_000)
        runFinishedAt = isGraph || route.summary.status == "running" ? nil : Int64(Date().timeIntervalSince1970 * 1_000)
        accumulatedDurationMs = 0
        interactiveAccumulatedDurationMs = 0
        graphBaseDurationMs = 0
        currentTurnIncludedInAccumulatedDuration = false
        var previewMessages = [
            ChatMessage(
                id: "preview-user", kind: .user,
                content: "请检查 iOS 端的核心交互和状态展示。", detail: nil,
                isFinished: true, isFailed: false, timestamp: nil
            ),
            ChatMessage(
                id: "preview-assistant", kind: .assistant,
                content: "已完成第一轮检查。运行状态和操作反馈都已同步。", detail: nil,
                isFinished: true, isFailed: false, timestamp: nil
            )
        ]
        if let initialMessage = route.initialMessage, !initialMessage.isEmpty {
            previewMessages.insert(
                ChatMessage(
                    id: "preview-initial", kind: .user, content: initialMessage, detail: nil,
                    isFinished: true, isFailed: false, timestamp: nil
                ),
                at: 0
            )
        }
        messages = previewMessages
        isTurnRunning = route.summary.status == "running"
        loading = false
        bumpScrollAnchor()
    }

    @discardableResult
    func enqueueDraft(
        text: String,
        attachment: PendingUpload?,
        remoteImagePaths: [String] = [],
        remoteFileAttachments: [FileAttachment] = [],
        isInitialDraft: Bool = false
    ) -> String? {
        let trimmed = text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !trimmed.isEmpty || attachment != nil || !remoteImagePaths.isEmpty || !remoteFileAttachments.isEmpty else { return nil }
        let item = LocalOutboxItem(
            id: UUID().uuidString.lowercased(),
            draft: ComposerDraft(text: trimmed, attachment: attachment),
            createdAt: Int64(Date().timeIntervalSince1970 * 1_000),
            isInitialDraft: isInitialDraft,
            requestContext: currentRequestContext(bypassCommand: isInitialDraft),
            remoteImagePaths: remoteImagePaths,
            remoteFileAttachments: remoteFileAttachments,
            state: .queued
        )
        outbox.append(item)
        if isInitialDraft && (attachment == nil || !remoteImagePaths.isEmpty || !remoteFileAttachments.isEmpty) {
            upsertOptimisticUserMessage(for: item)
        }
        bumpScrollAnchor()
        scheduleOutboxProcessing()
        return item.id
    }

    func cancelOutboxItem(id: String) {
        outbox.removeAll { $0.id == id && $0.isCancelable }
        bumpScrollAnchor()
    }

    func deleteQueuedMessage(id: String) async {
        guard let client else { return }
        guard deletingQueueIDs.insert(id).inserted else { return }
        defer { deletingQueueIDs.remove(id) }
        do {
            applyServerQueue(try await client.deleteQueuedMessage(jobID: jobID, messageID: id))
        } catch {
            errorDetail = errorText(error)
        }
    }

    func showQueueError(_ item: QueuedJobMessage) {
        guard let detail = item.error, !detail.isEmpty else { return }
        errorDetail = detail
    }

    func continueQueue() async {
        guard let client else { return }
        do {
            applyServerQueue(try await client.continueMessageQueue(jobID: jobID))
            if serverQueue.willContinue {
                isTurnRunning = true
                startStreaming()
            }
        } catch {
            errorDetail = errorText(error)
        }
    }

    func retryOutboxItem(id: String) {
        guard let index = outbox.firstIndex(where: { $0.id == id }) else { return }
        let item = outbox[index]
        let startsNewExecution = item.retryRequiresNewMessageID
        if startsNewExecution {
            messages.removeAll { $0.id == item.id && $0.isOptimistic }
        }
        outbox[index] = LocalOutboxItem(
            id: startsNewExecution ? UUID().uuidString.lowercased() : item.id,
            draft: item.draft,
            createdAt: startsNewExecution ? Int64(Date().timeIntervalSince1970 * 1_000) : item.createdAt,
            isInitialDraft: item.isInitialDraft,
            requestContext: startsNewExecution
                ? currentRequestContext(bypassCommand: item.isInitialDraft)
                : item.requestContext,
            remoteImagePaths: item.remoteImagePaths,
            remoteFileAttachments: item.remoteFileAttachments,
            state: .queued
        )
        scheduleOutboxProcessing()
    }

    private func currentRequestContext(bypassCommand: Bool) -> OutboxRequestContext {
        OutboxRequestContext(
            targetSessionID: preferredSessionID ?? sessionID,
            modelID: modelID,
            agentType: agentType,
            modeID: modeID,
            thoughtLevelID: thoughtLevelID,
            bypassCommand: bypassCommand
        )
    }

    func restoreOutboxItem(id: String) {
        guard let index = outbox.firstIndex(where: { $0.id == id }) else { return }
        let item = outbox.remove(at: index)
        messages.removeAll { $0.id == id && $0.isOptimistic }
        publishRestore(item.draft)
        bumpScrollAnchor()
    }

    func stopStreaming() {
        // 先把缓冲落地再停：否则最后 40ms 内到达的文本会随 flush task 一起被丢掉。
        flushPendingDeltas()
        deltaFlushTask?.cancel()
        deltaFlushTask = nil
        scrollAnchorThrottleTask?.cancel()
        scrollAnchorThrottleTask = nil
        streamGeneration &+= 1
        streamTask?.cancel()
        streamTask = nil
        graphReconcileTask?.cancel()
        graphReconcileTask = nil
        graphMonitorTask?.cancel()
        graphMonitorTask = nil
        streamState = .offline
    }

    func markStopped() {
        status = "stopped"
        let now = estimatedServerNow()
        runFinishedAt = now
        finishOpenMessages(outcome: "stopped", timestamp: now)
        isTurnRunning = false
        scheduleSnapshotRefresh()
    }

    private func seedInitialDraftIfNeeded(route: ChatRoute) -> String? {
        guard !didSeedInitialDraft else { return nil }
        let hasInitialContent = (route.initialMessage?.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty == false)
            || route.initialAttachment != nil
            || !(route.initialImagePaths ?? []).isEmpty
            || !(route.initialFileAttachments ?? []).isEmpty
        guard hasInitialContent else { return nil }

        didSeedInitialDraft = true
        return enqueueDraft(
            text: route.initialMessage?.trimmingCharacters(in: .whitespacesAndNewlines) ?? "",
            attachment: route.initialAttachment,
            remoteImagePaths: route.initialImagePaths ?? [],
            remoteFileAttachments: route.initialFileAttachments ?? [],
            isInitialDraft: true
        )
    }

    private func restoreInitialDraftIfNeeded(id: String?, detail: String) {
        guard let id, let index = outbox.firstIndex(where: { $0.id == id }) else { return }
        let item = outbox.remove(at: index)
        messages.removeAll { $0.id == id && $0.isOptimistic }
        publishRestore(item.draft)
        errorDetail = detail
        bumpScrollAnchor()
    }

    private func loadHistory(sessionID: String, preservesLiveMessages: Bool = true) async throws {
        guard let client else { return }
        let response = try await client.sessionMessages(id: sessionID)
        let agentInfo = await resolveAgentDisplayInfo(for: response)
        applySessionMetadata(response, agentInfo: agentInfo)
        let historyMessages = convertHistoryMessages(response.messages, agentInfo: agentInfo)
        // 上面有 await，缓冲里可能又攒了新 delta；紧贴写入点 flush，别让它们在替换之后才落地。
        flushPendingDeltas()
        if isGraph && graphRunLive && preservesLiveMessages {
            mergeGraphHistory(historyMessages)
        } else {
            messages = historyMessages
        }
        removeEchoedOutboxItems()
        bumpScrollAnchor()
    }

    private func loadInteractiveHistory(sessionIDs: [String]) async throws {
        guard let client else { return }
        var combined: [ChatMessage] = []
        let nonEmptySessionIDs = sessionIDs.filter { !$0.isEmpty }
        for (index, currentSessionID) in nonEmptySessionIDs.enumerated() {
            let response = try await client.sessionMessages(id: currentSessionID)
            let agentInfo = await resolveAgentDisplayInfo(for: response)
            let isLatestSession = index == nonEmptySessionIDs.count - 1
            combined.append(contentsOf: convertHistoryMessages(
                response.messages,
                idPrefix: isLatestSession ? nil : currentSessionID,
                agentInfo: agentInfo
            ))
            if isLatestSession {
                applySessionMetadata(response, agentInfo: agentInfo)
            }
        }
        flushPendingDeltas()
        messages = combined
        removeEchoedOutboxItems()
        bumpScrollAnchor()
    }

    // Match the Web client's history projection: assistant tool-call metadata
    // creates the card, then the later role=tool row fills its result/status.
    // Keeping this pairing structured is important because a single free-form
    // detail string cannot distinguish a tool name from streamed arguments.
    private func convertHistoryMessages(
        _ history: [HistoryMessage],
        idPrefix: String? = nil,
        agentInfo: AgentDisplayInfo? = nil
    ) -> [ChatMessage] {
        func scopedID(_ id: String) -> String {
            idPrefix.map { "\($0):\(id)" } ?? id
        }

        // Keep one canonical base projection per history row, then expand the
        // assistant/tool relationship into the richer timeline below.
        let isLatestSession = idPrefix == nil
        let currentSessionID = idPrefix ?? ""
        let baseMessages = history.map {
            ChatMessage(history: $0, idPrefix: isLatestSession ? nil : currentSessionID)
        }
        var converted: [ChatMessage] = []
        var toolIndexByCallID: [String: Int] = [:]

        for (offset, item) in history.enumerated() {
            if item.role == "assistant" {
                var assistant = baseMessages[offset]
                assistant.detail = nil
                if item.isThinking == true || !item.content.trimmingCharacters(in: .whitespacesAndNewlines).isEmpty
                    || !(item.reasoningContent ?? "").isEmpty || !(item.imageUrls ?? []).isEmpty {
                    converted.append(assistant)
                }

                for call in item.toolCalls ?? [] {
                    let callID = scopedID(call.id)
                    let toolName = call.name == "undefined" ? "" : call.name
                    toolIndexByCallID[callID] = converted.count
                    converted.append(ChatMessage(
                        id: callID,
                        kind: .tool,
                        content: "",
                        detail: nil,
                        isFinished: false,
                        isFailed: false,
                        timestamp: item.finishedAt ?? item.thoughtFinishedAt ?? item.startedAt,
                        toolCallID: call.id,
                        toolName: toolName,
                        toolArguments: call.arguments,
                        toolStatus: .processing
                    ))
                }
                continue
            }

            if item.role == "tool" {
                let rawCallID = item.toolCallId ?? item.id
                let callID = scopedID(rawCallID)
                let finalStatus: ChatMessage.ToolStatus = item.placeholder == true
                    ? .placeholder
                    : (item.failed == true ? .error : .success)
                if let index = toolIndexByCallID[callID] {
                    converted[index].content = item.content
                    converted[index].isFinished = true
                    converted[index].isFailed = item.failed == true
                    converted[index].toolStatus = finalStatus
                    converted[index].placeholderReason = item.placeholderReason
                    converted[index].finishedAt = item.finishedAt
                    converted[index].imagePaths = item.imageUrls ?? []
                    if let startedAt = item.startedAt { converted[index].timestamp = startedAt }
                } else {
                    var orphan = baseMessages[offset]
                    orphan.toolCallID = rawCallID
                    orphan.toolStatus = finalStatus
                    converted.append(orphan)
                    toolIndexByCallID[callID] = converted.count - 1
                }
                continue
            }

            converted.append(baseMessages[offset])
        }

        // A call without a persisted result was interrupted. Do not paint it
        // as successful after reload.
        for index in converted.indices where converted[index].kind == .tool && !converted[index].isFinished {
            converted[index].isFinished = true
            converted[index].toolStatus = .placeholder
            converted[index].placeholderReason = "unknown"
        }
        for index in converted.indices where converted[index].kind == .assistant {
            converted[index].agentDisplayName = resolvedAgentName(agentInfo)
            converted[index].agentIconUrl = displayValue(agentInfo?.iconUrl)
        }
        return converted
    }

    private func startStreaming() {
        guard streamTask == nil, let client else { return }
        let jobID = self.jobID
        streamGeneration &+= 1
        let generation = streamGeneration
        if isGraph && graphRunLive {
            startGraphMonitor()
        } else {
            graphMonitorTask?.cancel()
            graphMonitorTask = nil
        }
        streamState = .connecting
        streamTask = Task { @MainActor [weak self] in
            var attempts = 0
            while !Task.isCancelled {
                guard let self else { return }
                let resumeID = self.lastEventID
                do {
                    if self.isGraph && self.graphRunLive {
                        try await client.streamGraphEvents(
                            jobID: jobID,
                            lastEventID: self.lastGraphEventID,
                            onOpen: {
                                guard self.streamGeneration == generation, !Task.isCancelled else { return }
                                self.streamState = .live
                                self.errorDetail = nil
                            },
                            onEvent: { event, id in
                                guard self.streamGeneration == generation, !Task.isCancelled else { return }
                                self.applyGraph(event, id: id)
                            }
                        )
                    } else {
                        try await client.streamEvents(
                            jobID: jobID,
                            lastEventID: resumeID,
                            onOpen: {
                                guard self.streamGeneration == generation, !Task.isCancelled else { return }
                                self.streamState = .live
                                self.errorDetail = nil
                                self.scheduleOutboxProcessing()
                            },
                            onEvent: { event, id in
                                guard self.streamGeneration == generation, !Task.isCancelled else { return }
                                self.apply(event, id: id)
                            }
                        )
                    }
                    guard self.streamGeneration == generation, !Task.isCancelled else { return }
                    attempts = 0
                    self.streamState = .reconnecting
                    try await Task.sleep(for: .seconds(1))
                } catch is CancellationError {
                    return
                } catch {
                    guard self.streamGeneration == generation, !Task.isCancelled else { return }
                    attempts += 1
                    self.streamState = .reconnecting
                    self.errorDetail = self.errorText(error)
                    do {
                        try await Task.sleep(for: .seconds(min(8, attempts * 2)))
                        self.lastGraphEventID = 0
                        try await self.refreshSnapshotAndHistory()
                    } catch is CancellationError {
                        return
                    } catch {
                        guard self.streamGeneration == generation, !Task.isCancelled else { return }
                        self.errorDetail = self.errorText(error)
                    }
                }
            }
        }
    }

    private func waitForStreamReady() async throws {
        for _ in 0..<150 {
            try Task.checkCancellation()
            if streamState == .live { return }
            try await Task.sleep(for: .milliseconds(100))
        }
        throw APIError(
            summary: "实时连接超时",
            detail: "GET /api/v1/job/\(jobID)/events 在 15 秒内未建立连接，消息尚未发送。"
        )
    }

    private func scheduleOutboxProcessing() {
        guard !isProcessingOutbox else { return }
        Task { @MainActor [weak self] in
            await self?.processOutboxIfPossible()
        }
    }

    private func processOutboxIfPossible() async {
        guard !isProcessingOutbox else { return }
        guard !loading, !sending else { return }
        // Graph messages retain their existing client-side serialization. The
        // durable server queue only applies to ordinary interactive Jobs.
        if isGraph && isTurnRunning { return }
        guard let index = outbox.firstIndex(where: {
            if case .queued = $0.state { return true }
            return false
        }) else { return }

        isProcessingOutbox = true
        defer {
            isProcessingOutbox = false
            if outbox.contains(where: { if case .queued = $0.state { return true }; return false }) {
                scheduleOutboxProcessing()
            }
        }
        await dispatchOutboxItem(at: index)
    }

    private func dispatchOutboxItem(at index: Int) async {
        guard outbox.indices.contains(index), let client else { return }
        let itemID = outbox[index].id
        sending = true
        defer { sending = false }

        do {
            outbox[index].state = .sending
            if outbox[index].attachment == nil || !outbox[index].remoteImagePaths.isEmpty || !outbox[index].remoteFileAttachments.isEmpty {
                upsertOptimisticUserMessage(for: outbox[index])
            }
            startStreaming()
            try await waitForStreamReady()

            if let attachment = outbox[index].attachment, outbox[index].remoteImagePaths.isEmpty, outbox[index].remoteFileAttachments.isEmpty {
                outbox[index].state = .uploading
                let uploaded = try await client.uploadFile(
                    data: attachment.data,
                    filename: attachment.filename,
                    mimeType: attachment.mimeType
                )
                guard let refreshed = outbox.firstIndex(where: { $0.id == itemID }) else { return }
                if attachment.isImage {
                    outbox[refreshed].remoteImagePaths = [uploaded.path]
                } else {
                    outbox[refreshed].remoteFileAttachments = [uploaded]
                }
                upsertOptimisticUserMessage(for: outbox[refreshed])
            }

            guard let refreshed = outbox.firstIndex(where: { $0.id == itemID }) else { return }
            let item = outbox[refreshed]
            outbox[refreshed].state = .sending
            let content = item.displayText
            let response = try await client.sendMessage(
                jobID: jobID,
                body: SendMessageRequest(
                    messages: [.init(
                        id: item.id,
                        content: content,
                        timestamp: item.createdAt,
                        imageUrls: item.remoteImagePaths.isEmpty ? nil : item.remoteImagePaths,
                        fileAttachments: item.remoteFileAttachments.isEmpty ? nil : item.remoteFileAttachments
                    )],
                    modelId: item.requestContext.modelID,
                    agentType: item.requestContext.agentType,
                    sessionId: item.requestContext.targetSessionID,
                    clientMessageId: item.id,
                    acpMode: item.requestContext.modeID,
                    acpThoughtLevel: item.requestContext.thoughtLevelID,
                    bypassCommand: item.requestContext.bypassCommand
                )
            )
            await reconcileSendResponse(response, itemID: itemID)
        } catch {
            let detail = errorText(error)
            if let failedIndex = outbox.firstIndex(where: { $0.id == itemID }),
               case .failed = outbox[failedIndex].state {
                errorDetail = outbox[failedIndex].failureDetail ?? detail
            } else if let failedIndex = outbox.firstIndex(where: { $0.id == itemID }) {
                outbox[failedIndex].state = .failed(detail: detail, requiresNewMessageID: false)
                publishRestore(outbox[failedIndex].draft)
                errorDetail = detail
            }
            // The POST result may be unknown while JOB_STARTED already arrived
            // over SSE. Do not reopen the queue/composer as idle in that case.
            isTurnRunning = status == "running"
            bumpScrollAnchor()
        }
    }

    private func reconcileSendResponse(_ response: SendMessageResponse, itemID: String) async {
        if response.status == "command_dispatched" || response.status == "command_duplicate" {
            if let event = response.event {
                applyCommandEvent(event, fallbackClientMessageID: itemID)
            }
            outbox.removeAll { $0.id == itemID }
            bumpScrollAnchor()
            return
        }
        if response.status == "queued" {
            if let current = outbox.first(where: { $0.id == itemID }), case .awaitingEcho = current.state {
                return
            }
            if let queue = response.queue { applyServerQueue(queue) }
            messages.removeAll { $0.id == itemID && $0.isOptimistic }
            outbox.removeAll { $0.id == itemID }
            isTurnRunning = serverQueue.willContinue
            bumpScrollAnchor()
            return
        }
        if response.status == "deleted" {
            if let queue = response.queue { applyServerQueue(queue) }
            messages.removeAll { $0.id == itemID && $0.isOptimistic }
            outbox.removeAll { $0.id == itemID }
            bumpScrollAnchor()
            return
        }
        guard response.status == "started" || response.isDuplicate else {
            markOutboxFailed(
                itemID: itemID,
                detail: "POST /api/v1/job/\(jobID)/message 返回未知状态：\(response.status)"
            )
            return
        }
        if let responseID = response.clientMessageId, responseID != itemID {
            markOutboxFailed(
                itemID: itemID,
                detail: "服务端返回 clientMessageId=\(responseID)，但当前消息是 \(itemID)。"
            )
            return
        }
        guard response.isDuplicate else {
            setAwaitingEcho(itemID: itemID)
            return
        }
        if response.messageState == "queued" || response.messageState == "blocked" || response.messageState == "deleted" {
            if let current = outbox.first(where: { $0.id == itemID }), case .awaitingEcho = current.state {
                return
            }
            if let queue = response.queue { applyServerQueue(queue) }
            messages.removeAll { $0.id == itemID && $0.isOptimistic }
            outbox.removeAll { $0.id == itemID }
            isTurnRunning = serverQueue.willContinue
            bumpScrollAnchor()
            return
        }

        switch response.messageState {
        case "processing":
            setAwaitingEcho(itemID: itemID)
            do {
                try await refreshSnapshotAndHistory()
                if let index = outbox.firstIndex(where: { $0.id == itemID }) {
                    outbox[index].state = .awaitingEcho
                }
                if outbox.contains(where: { $0.id == itemID }) {
                    isTurnRunning = true
                    status = "running"
                } else {
                    // The history already contains the stable clientMessageId.
                    // Treat the durable history as newer than a stale processing
                    // receipt and keep the authoritative snapshot status.
                    isTurnRunning = status == "running" || status == "pending"
                }
            } catch {
                // The durable receipt already proved the original request is in
                // progress. Keep following its SSE stream; a transient snapshot
                // failure must not turn it into a locally retryable send.
                errorDetail = errorText(error)
                isTurnRunning = true
                status = "running"
            }
        case "completed":
            do {
                try await refreshSnapshotAndHistory()
            } catch {
                errorDetail = errorText(error)
            }
            outbox.removeAll { $0.id == itemID }
            isTurnRunning = serverQueue.willContinue
            if isGraph { stopStreaming() }
            scheduleOutboxProcessing()
            bumpScrollAnchor()
        case "queued", "blocked":
            if let queue = response.queue { applyServerQueue(queue) }
            messages.removeAll { $0.id == itemID && $0.isOptimistic }
            outbox.removeAll { $0.id == itemID }
            isTurnRunning = serverQueue.willContinue
            bumpScrollAnchor()
        case "failed", "stopped", "interrupted":
            let failedItem = outbox.first(where: { $0.id == itemID })
            do {
                try await refreshSnapshotAndHistory()
            } catch {
                errorDetail = errorText(error)
            }
            let state = response.messageState ?? "unknown"
            let detail = "服务端确认消息 \(itemID) 已被处理，但最终状态为 \(state)。草稿和附件已恢复，可使用新的消息 ID 重试。"
            markOutboxFailed(itemID: itemID, detail: detail, requiresNewMessageID: true, fallback: failedItem)
            isTurnRunning = serverQueue.willContinue
            if isGraph { stopStreaming() }
        case let state?:
            markOutboxFailed(
                itemID: itemID,
                detail: "POST /api/v1/job/\(jobID)/message 返回未知 messageState：\(state)"
            )
        case nil:
            markOutboxFailed(
                itemID: itemID,
                detail: "POST /api/v1/job/\(jobID)/message 返回 duplicate，但缺少 messageState。"
            )
        }
    }

    private func setAwaitingEcho(itemID: String) {
        guard let index = outbox.firstIndex(where: { $0.id == itemID }) else { return }
        outbox[index].state = .awaitingEcho
        upsertOptimisticUserMessage(for: outbox[index])
        isTurnRunning = true
        status = "running"
        bumpScrollAnchor()
    }

    private func upsertOptimisticUserMessage(for item: LocalOutboxItem) {
        flushPendingDeltas()
        if let index = messages.lastIndex(where: { $0.id == item.id }) {
            messages[index].content = item.draft.text
            messages[index].imagePaths = item.remoteImagePaths
            messages[index].fileAttachments = item.remoteFileAttachments
            messages[index].isFailed = false
            return
        }
        messages.append(ChatMessage(
            id: item.id,
            kind: .user,
            content: item.draft.text,
            detail: nil,
            isFinished: true,
            isFailed: false,
            timestamp: item.createdAt,
            imagePaths: item.remoteImagePaths,
            fileAttachments: item.remoteFileAttachments,
            isOptimistic: true
        ))
    }

    private func markOutboxFailed(
        itemID: String,
        detail: String,
        requiresNewMessageID: Bool = false,
        fallback: LocalOutboxItem? = nil
    ) {
        if let index = outbox.firstIndex(where: { $0.id == itemID }) {
            outbox[index].state = .failed(detail: detail, requiresNewMessageID: requiresNewMessageID)
            publishRestore(outbox[index].draft)
            if let messageIndex = messages.lastIndex(where: { $0.id == itemID }) {
                messages[messageIndex].isFailed = true
            }
        } else if let fallback {
            // A history refresh may already have replaced the optimistic item
            // with its persisted user message. Restore the draft without adding
            // a second copy of that same message to the timeline.
            publishRestore(fallback.draft)
        } else {
            errorDetail = detail
            bumpScrollAnchor()
            return
        }
        removeEchoedOutboxItems()
        errorDetail = detail
        bumpScrollAnchor()
    }

    private func refreshSnapshotAndHistory() async throws {
        guard let client else { return }
        let snapshot = try await client.job(id: jobID)
        updateServerClock(snapshot.serverTime)
        if !isGraph {
            do { applyServerQueue(try await client.messageQueue(jobID: jobID)) }
            catch { errorDetail = errorText(error) }
        }
        applyAuthoritativeTitle(snapshot.title)
        status = snapshot.status
        applyInteractiveAccumulatedDuration(snapshot.totalTurnDurationMs)
        runStartedAt = snapshot.status == "running" ? snapshot.startedAt : nil
        runFinishedAt = nil
        currentTurnIncludedInAccumulatedDuration = snapshot.status != "running"
        lastEventID = snapshot.lastEventSeq
        if isGraph {
            let graphSnapshot = try await client.graphRunStatus(jobID: jobID)
            applyGraphDuration(graphSnapshot)
            let graphStatus = graphSnapshot.run?.status
            graphRunLive = graphStatus.map(isLiveGraphStatus) ?? false
            if snapshot.status != "running", let graphStatus {
                status = graphStatus
            }
        }
        let interactiveSessions = snapshot.sessionIds ?? []
        let fallbackSession = interactiveSessions.last ?? snapshot.graphSessionIds?.last
        sessionID = preferredSessionID ?? fallbackSession
        if interactiveSessions.isEmpty && preferredSessionID == nil {
            agentType = snapshot.initialAgentId ?? agentType
            modelID = snapshot.firstModelId ?? modelID
            modeID = snapshot.initialAcpMode ?? modeID
            thoughtLevelID = snapshot.initialAcpThoughtLevel ?? thoughtLevelID
        }
        if preferredSessionID != nil, let sessionID {
            try await loadHistory(sessionID: sessionID)
        } else if !interactiveSessions.isEmpty {
            try await loadInteractiveHistory(sessionIDs: interactiveSessions)
        } else if let sessionID {
            try await loadHistory(sessionID: sessionID)
        }
        if !isGraph { applyServerQueue(serverQueue) }
        isTurnRunning = graphRunLive
            || snapshot.status == "running"
            || (snapshot.status == "pending" && hasPriorConversation(snapshot))
            || serverQueue.willContinue
    }

    private func applyGraph(_ event: GraphStreamEvent, id: UInt64?) {
        if let id { lastGraphEventID = id }
        updateServerClock(event.createdAt)

        let payload = event.payload ?? [:]
        let eventSessionID = payload["sessionId"]
        let belongsToVisibleSession = eventSessionID == nil || sessionID == nil || eventSessionID == sessionID

        // 收敛点：除纯 delta 之外的任何事件都可能读 `messages`，统一在分发前把缓冲落地，
        // 免得每个分支各自记得 flush。
        if !Self.deltaEventTypes.contains(event.type) { flushPendingDeltas() }

        switch event.type {
        case "agentMessageStart":
            guard belongsToVisibleSession, let messageID = payload["messageId"], !messageID.isEmpty else { return }
            upsert(
                id: messageID, kind: .assistant, content: "", detail: nil,
                finished: false, failed: false, timestamp: event.createdAt
            )
        case "agentMessageDelta":
            guard belongsToVisibleSession, let messageID = payload["messageId"], !messageID.isEmpty else { return }
            append(
                id: messageID, kind: .assistant,
                text: payload["delta"] ?? event.message ?? "", timestamp: event.createdAt
            )
        case "agentMessageEnd":
            guard belongsToVisibleSession else { return }
            finish(id: payload["messageId"], timestamp: event.createdAt)
        case "agentThoughtStart":
            guard belongsToVisibleSession, let messageID = payload["messageId"], !messageID.isEmpty else { return }
            upsert(
                id: messageID, kind: .thought, content: "", detail: nil,
                finished: false, failed: false, timestamp: event.createdAt
            )
        case "agentThoughtDelta":
            guard belongsToVisibleSession, let messageID = payload["messageId"], !messageID.isEmpty else { return }
            append(
                id: messageID, kind: .thought,
                text: payload["delta"] ?? event.message ?? "", timestamp: event.createdAt
            )
        case "agentThoughtEnd":
            guard belongsToVisibleSession else { return }
            finish(id: payload["messageId"], timestamp: event.createdAt)
        case "agentToolStart":
            guard belongsToVisibleSession, let toolID = payload["toolCallId"], !toolID.isEmpty else { return }
            upsert(
                id: toolID, kind: .tool, content: "", detail: nil,
                finished: false, failed: false, timestamp: event.createdAt
            )
            configureTool(id: toolID, name: payload["toolName"], status: payload["status"])
        case "agentToolArgs":
            guard belongsToVisibleSession, let toolID = payload["toolCallId"], !toolID.isEmpty else { return }
            appendToolArguments(id: toolID, text: payload["delta"] ?? "", replace: payload["replace"] == "true")
        case "agentToolResult":
            guard belongsToVisibleSession, let toolID = payload["toolCallId"], !toolID.isEmpty else { return }
            if payload["stitched"] == "true", let index = messages.lastIndex(where: { $0.id == toolID }) {
                messages[index].content = payload["delta"] ?? event.message ?? messages[index].content
                messages[index].isFinished = true
                messages[index].isFailed = payload["status"] == "Error"
                messages[index].toolStatus = ChatMessage.ToolStatus(serverValue: payload["status"])
                messages[index].finishedAt = event.createdAt
                bumpScrollAnchor()
            } else {
                append(
                    id: toolID, kind: .tool,
                    text: payload["delta"] ?? event.message ?? "", timestamp: event.createdAt
                )
                // 紧接着要按 id 读回这条消息，所以这里必须让上面的追加立即落地。
                flushPendingDeltas()
                if let index = messages.lastIndex(where: { $0.id == toolID }) {
                    messages[index].isFailed = payload["status"] == "Error"
                    messages[index].toolStatus = ChatMessage.ToolStatus(serverValue: payload["status"])
                }
            }
        case "agentToolEnd":
            guard belongsToVisibleSession else { return }
            if let toolID = payload["toolCallId"], let index = messages.lastIndex(where: { $0.id == toolID }) {
                messages[index].isFailed = payload["status"] == "Error"
                messages[index].toolStatus = ChatMessage.ToolStatus(serverValue: payload["status"])
                if let reason = payload["placeholderReason"], !reason.isEmpty {
                    messages[index].placeholderReason = reason
                    messages[index].toolStatus = .placeholder
                }
            }
            finish(id: payload["toolCallId"], timestamp: event.createdAt)
        case "error":
            let detail = event.error?.fullDetail
            errorDetail = detail?.isEmpty == false ? detail : (event.message ?? "Graph run stream reported an error")
            scheduleGraphReconcile(immediate: true)
        case "instanceStarted", "instanceCompleted", "instanceFailed", "instanceSkipped",
             "edgeResolved", "loopIteration", "progressUpdated":
            scheduleGraphReconcile(immediate: event.type == "progressUpdated" || event.message == "session opened")
        default:
            break
        }
    }

    private func scheduleGraphReconcile(immediate: Bool) {
        guard graphReconcileTask == nil, client != nil else { return }
        graphReconcileTask = Task { @MainActor [weak self] in
            guard let self else { return }
            defer { self.graphReconcileTask = nil }
            if !immediate {
                do {
                    try await Task.sleep(for: .milliseconds(400))
                } catch {
                    return
                }
            }
            let wasLive = self.graphRunLive
            do {
                try await self.refreshGraphRunState()
            } catch {
                self.errorDetail = self.errorText(error)
                return
            }
            guard wasLive, !self.graphRunLive else { return }
            if let sessionID = self.sessionID {
                do {
                    try await self.loadHistory(sessionID: sessionID)
                } catch {
                    self.errorDetail = self.errorText(error)
                }
            }
            self.finishOpenMessages(outcome: self.status, timestamp: Int64(Date().timeIntervalSince1970 * 1_000))
            self.streamTask?.cancel()
            self.streamTask = nil
            self.graphMonitorTask?.cancel()
            self.graphMonitorTask = nil
            self.streamState = .offline
            self.startStreaming()
        }
    }

    private func refreshGraphRunState() async throws {
        guard let client else { return }
        let snapshot = try await client.graphRunStatus(jobID: jobID)
        guard let run = snapshot.run else { return }
        applyGraphDuration(snapshot)
        status = run.status
        graphRunLive = isLiveGraphStatus(run.status)
        isTurnRunning = graphRunLive
        if preferredSessionID == nil, let latestSession = latestGraphSessionID(in: snapshot), latestSession != sessionID {
            sessionID = latestSession
            try await loadHistory(sessionID: latestSession, preservesLiveMessages: false)
        }
        if let error = run.lastError?.fullDetail, !error.isEmpty {
            errorDetail = error
        } else if let error = snapshot.progress?.lastError, !error.isEmpty {
            errorDetail = error
        } else {
            errorDetail = nil
        }
    }

    private func startGraphMonitor() {
        guard graphMonitorTask == nil, client != nil else { return }
        graphMonitorTask = Task { @MainActor [weak self] in
            while !Task.isCancelled {
                do {
                    try await Task.sleep(for: .seconds(2))
                } catch {
                    return
                }
                guard let self, self.graphRunLive else { return }
                self.scheduleGraphReconcile(immediate: true)
            }
        }
    }

    private func mergeGraphHistory(_ persisted: [ChatMessage]) {
        let persistedIDs = Set(persisted.map(\.id))
        let inFlight = messages.filter { !persistedIDs.contains($0.id) }
        messages = persisted + inFlight
    }

    private func latestGraphSessionID(in snapshot: GraphRunStatusResponse) -> String? {
        snapshot.instances?
            .compactMap { instance -> (String, Int64)? in
                guard let candidate = instance.displaySessionId ?? instance.sessionId, !candidate.isEmpty else { return nil }
                return (candidate, instance.startedAt ?? 0)
            }
            .max(by: { $0.1 < $1.1 })?
            .0
    }

    private func isLiveGraphStatus(_ status: String) -> Bool {
        ["pending", "running", "stepStopping"].contains(status)
    }

    private func apply(_ event: ServerEvent, id: UInt64?) {
        if let id { lastEventID = id }
        updateServerClock(event.timestamp)
        if let sessionId = event.sessionId, !sessionId.isEmpty {
            sessionID = sessionId
        }
        // 同上：非 delta 事件统一先把缓冲落地。
        if !Self.deltaEventTypes.contains(event.type) { flushPendingDeltas() }
        switch event.type {
        case "JOB_STARTED":
            status = "running"
            isTurnRunning = true
            runStartedAt = event.timestamp ?? runStartedAt
            runFinishedAt = nil
            currentTurnIncludedInAccumulatedDuration = false
        case "RUN_STARTED":
            isTurnRunning = true
            runStartedAt = event.timestamp ?? runStartedAt
            runFinishedAt = nil
            currentTurnIncludedInAccumulatedDuration = false
            if let clientMessageID = event.clientMessageId {
                let queued = knownQueuedItems[clientMessageID]
                if let queued,
                   !messages.contains(where: { $0.id == clientMessageID }) {
                    messages.append(ChatMessage(
                        id: queued.id, kind: .user, content: queued.summaryLine, detail: nil,
                        isFinished: true, isFailed: false, timestamp: event.timestamp,
                        imagePaths: queued.imagePaths, fileAttachments: queued.fileAttachments
                    ))
                }
                if serverQueue.items.contains(where: { $0.id == clientMessageID }) {
                    serverQueue = MessageQueueSnapshot(
                        jobId: serverQueue.jobId, version: serverQueue.version, paused: serverQueue.paused,
                        pauseReason: serverQueue.pauseReason, willContinue: true,
                        active: queued,
                        items: serverQueue.items.filter { $0.id != clientMessageID }
                    )
                }
                setAwaitingEcho(itemID: clientMessageID)
            }
        case "RUN_FINISHED":
            runFinishedAt = event.timestamp ?? runFinishedAt
            finishOpenMessages(outcome: "completed", timestamp: event.timestamp)
            // RUN_FINISHED closes the Agent round, but the backend publishes the
            // authoritative JOB_* terminal event only after it has persisted the
            // Job transition. Keep the local queue paused until that event arrives;
            // sending here races the backend's still-running gate and returns 409.
        case "JOB_COMPLETED":
            status = "completed"
            isTurnRunning = false
            runFinishedAt = event.timestamp ?? runFinishedAt
            applyTerminalDuration(event)
            let outcome = event.runOutcome ?? "completed"
            publishTerminalStateChange()
            applyRunOutcome(outcome)
            finishOpenMessages(outcome: outcome, timestamp: event.timestamp)
            scheduleSnapshotRefresh()
        case "JOB_FAILED":
            status = "failed"
            isTurnRunning = false
            runFinishedAt = event.timestamp ?? runFinishedAt
            applyTerminalDuration(event)
            let outcome = event.runOutcome ?? "failed"
            publishTerminalStateChange()
            applyRunOutcome(outcome)
            if let message = event.message, !message.isEmpty { errorDetail = message }
            finishOpenMessages(outcome: outcome, timestamp: event.timestamp)
            scheduleSnapshotRefresh()
        case "JOB_STOPPED":
            status = "stopped"
            isTurnRunning = false
            runFinishedAt = event.timestamp ?? runFinishedAt
            applyTerminalDuration(event)
            let outcome = event.runOutcome ?? "stopped"
            publishTerminalStateChange()
            applyRunOutcome(outcome)
            finishOpenMessages(outcome: outcome, timestamp: event.timestamp)
            scheduleSnapshotRefresh()
        case "RUN_ERROR":
            errorDetail = [event.code, event.message].compactMap { $0 }.joined(separator: "\n")
        case "COMMAND_SYSTEM_MESSAGE":
            applyCommandEvent(event)
        case "CUSTOM":
            applyCustomEvent(event)
        case "TEXT_MESSAGE_START":
            guard let messageID = event.messageId else { return }
            let kind: ChatMessage.Kind = event.external?.isThinking == true ? .thought : .assistant
            upsert(id: messageID, kind: kind, content: "", detail: nil, finished: false, failed: false, timestamp: event.timestamp)
        case "TEXT_MESSAGE_CONTENT":
            guard let messageID = event.messageId else { return }
            let kind: ChatMessage.Kind = event.external?.isThinking == true ? .thought : .assistant
            append(id: messageID, kind: kind, text: event.delta ?? "", timestamp: event.timestamp)
        case "TEXT_MESSAGE_END":
            finish(id: event.messageId, timestamp: event.timestamp)
        case "TOOL_CALL_START":
            guard let toolID = event.toolCallId else { return }
            upsert(id: toolID, kind: .tool, content: "", detail: nil, finished: false, failed: false, timestamp: event.timestamp)
            configureTool(id: toolID, name: event.toolCallName, status: event.toolCallStatus)
        case "TOOL_CALL_ARGS":
            guard let toolID = event.toolCallId else { return }
            appendToolArguments(id: toolID, text: event.delta ?? "", replace: event.replace == true)
        case "TOOL_CALL_RESULT":
            guard let toolID = event.toolCallId else { return }
            append(id: toolID, kind: .tool, text: event.delta ?? "", timestamp: event.timestamp)
            // 紧接着要按 id 读回这条消息，所以这里必须让上面的追加立即落地。
            flushPendingDeltas()
            if let index = messages.lastIndex(where: { $0.id == toolID }) {
                messages[index].isFailed = event.toolCallStatus == "Error"
                messages[index].toolStatus = ChatMessage.ToolStatus(serverValue: event.toolCallStatus)
            }
        case "TOOL_CALL_END":
            if let toolID = event.toolCallId, let index = messages.lastIndex(where: { $0.id == toolID }) {
                messages[index].toolStatus = ChatMessage.ToolStatus(serverValue: event.toolCallStatus ?? (messages[index].isFailed ? "Error" : "Success"))
            }
            finish(id: event.toolCallId, timestamp: event.timestamp)
        case "TOOL_CALL_STITCHED":
            guard let toolID = event.toolCallId else { return }
            if let index = messages.lastIndex(where: { $0.id == toolID }) {
                messages[index].content = event.delta ?? messages[index].content
                messages[index].isFinished = true
                messages[index].isFailed = event.toolCallStatus == "Error"
                messages[index].toolStatus = ChatMessage.ToolStatus(serverValue: event.toolCallStatus)
                messages[index].finishedAt = event.timestamp
            }
        default:
            break
        }
    }

    private func applyCustomEvent(_ event: ServerEvent) {
        switch event.name {
        case "token_usage":
            if let tokens = event.value?.totalTokens {
                totalTokens = tokens
            }
        case "job_title_updated":
            if let updatedTitle = event.value?.title, !updatedTitle.isEmpty {
                applyAuthoritativeTitle(updatedTitle)
            }
        case "message_queue_changed":
            if let version = event.value?.version, version > serverQueue.version, let client {
                Task { [weak self] in
                    guard let self else { return }
                    do { self.applyServerQueue(try await client.messageQueue(jobID: self.jobID)) }
                    catch { self.errorDetail = self.errorText(error) }
                }
            }
        default:
            break
        }
    }

    private func applyAuthoritativeTitle(_ title: String) {
        guard !title.isEmpty, title != self.title else { return }
        self.title = title
        authoritativeTitleVersion &+= 1
    }

    private func applyCommandEvent(_ event: ServerEvent, fallbackClientMessageID: String? = nil) {
        guard let text = event.text, !text.isEmpty else { return }
        let messageID = event.clientMessageId ?? fallbackClientMessageID ?? UUID().uuidString
        upsert(
            id: messageID,
            kind: .system,
            content: text,
            detail: nil,
            finished: true,
            failed: false,
            timestamp: event.timestamp
        )
        // Commands are transient and never enter session history. Remove the
        // optimistic outbox copy as soon as either delivery path arrives; the
        // matching inline/SSE copy then upserts the same stable system message.
        if event.clientMessageId != nil || fallbackClientMessageID != nil {
            outbox.removeAll { $0.id == messageID }
        }
    }

    private func upsert(id: String, kind: ChatMessage.Kind, content: String, detail: String?, finished: Bool, failed: Bool, timestamp: Int64?) {
        flushPendingDeltas()
        if let index = messages.lastIndex(where: { $0.id == id }) {
            guard !messages[index].isFinished else { return }
            messages[index].kind = kind
            messages[index].detail = detail ?? messages[index].detail
            return
        }
        messages.append(ChatMessage(
            id: id,
            kind: kind,
            content: content,
            detail: detail,
            isFinished: finished,
            isFailed: failed,
            timestamp: timestamp
        ))
        bumpScrollAnchor()
    }

    private func append(id: String, kind: ChatMessage.Kind, text: String, timestamp: Int64?) {
        enqueueDelta(.text(id: id, kind: kind, text: text, timestamp: timestamp))
    }

    private func applyAppend(id: String, kind: ChatMessage.Kind, text: String, timestamp: Int64?) {
        if let index = messages.lastIndex(where: { $0.id == id }) {
            guard !messages[index].isFinished else { return }
            messages[index].content += text
        } else {
            messages.append(ChatMessage(
                id: id,
                kind: kind,
                content: text,
                detail: nil,
                isFinished: false,
                isFailed: false,
                timestamp: timestamp
            ))
        }
    }

    // MARK: - SSE delta 合流

    /// 每个 delta 原本都直接改 `@Published messages` 并 bump 一次滚动锚点，于是几十个
    /// delta/秒 就是几十次全列表发布加几十个互相叠加的滚动动画。这里把纯文本追加按到达
    /// 顺序攒进缓冲，由一个 40ms 的 flush 一次性应用并只 bump 一次 —— 把 UI 更新频率钉在
    /// ≤25Hz，与 delta 到达速率解耦。
    ///
    /// 所有依赖 `messages` 一致状态的操作都必须先调 `flushPendingDeltas()`。
    private enum PendingDelta {
        case text(id: String, kind: ChatMessage.Kind, text: String, timestamp: Int64?)
        case toolArguments(id: String, text: String, replace: Bool)
    }

    /// 可以进缓冲的纯 delta 事件；其余事件一律先 flush 再处理。
    static let deltaEventTypes: Set<String> = [
        "agentMessageDelta", "agentThoughtDelta", "agentToolArgs",
        "TEXT_MESSAGE_CONTENT", "TOOL_CALL_ARGS",
    ]

    private func enqueueDelta(_ delta: PendingDelta) {
        switch delta {
        case .text(let id, let kind, let text, let timestamp):
            // 相邻的同目标追加并成一条，flush 时少一轮字符串拷贝。
            if case .text(let lastID, _, let lastText, _) = pendingDeltas.last, lastID == id {
                pendingDeltas[pendingDeltas.count - 1] = .text(
                    id: id, kind: kind, text: lastText + text, timestamp: timestamp
                )
            } else {
                pendingDeltas.append(delta)
            }
        case .toolArguments(let id, let text, let replace):
            // replace 语义会丢弃之前的内容，不能和前面的追加合并。
            if !replace,
               case .toolArguments(let lastID, let lastText, let lastReplace) = pendingDeltas.last,
               lastID == id {
                pendingDeltas[pendingDeltas.count - 1] = .toolArguments(
                    id: id, text: lastText + text, replace: lastReplace
                )
            } else {
                pendingDeltas.append(delta)
            }
        }
        scheduleDeltaFlush()
    }

    private func scheduleDeltaFlush() {
        guard deltaFlushTask == nil else { return }
        deltaFlushTask = Task { @MainActor [weak self] in
            try? await Task.sleep(for: .milliseconds(40))
            guard let self else { return }
            self.deltaFlushTask = nil
            self.flushPendingDeltas()
        }
    }

    private func flushPendingDeltas() {
        guard !pendingDeltas.isEmpty else { return }
        let deltas = pendingDeltas
        pendingDeltas.removeAll(keepingCapacity: true)
        for delta in deltas {
            switch delta {
            case .text(let id, let kind, let text, let timestamp):
                applyAppend(id: id, kind: kind, text: text, timestamp: timestamp)
            case .toolArguments(let id, let text, let replace):
                applyToolArguments(id: id, text: text, replace: replace)
            }
        }
        bumpScrollAnchorThrottled()
    }

    private func configureTool(id: String, name: String?, status: String?) {
        flushPendingDeltas()
        guard let index = messages.lastIndex(where: { $0.id == id }) else { return }
        messages[index].toolCallID = id
        if let name, !name.isEmpty { messages[index].toolName = name }
        messages[index].toolStatus = ChatMessage.ToolStatus(serverValue: status)
    }

    private func appendToolArguments(id: String, text: String, replace: Bool) {
        enqueueDelta(.toolArguments(id: id, text: text, replace: replace))
    }

    private func applyToolArguments(id: String, text: String, replace: Bool) {
        guard let index = messages.lastIndex(where: { $0.id == id }) else { return }
        messages[index].toolArguments = replace ? text : (messages[index].toolArguments ?? "") + text
    }

    private func finish(id: String?, timestamp: Int64? = nil) {
        // 必须先 flush：`applyAppend` 会跳过已 finished 的消息，顺序反了会丢掉尾部文本。
        flushPendingDeltas()
        guard let id, let index = messages.lastIndex(where: { $0.id == id }) else { return }
        messages[index].isFinished = true
        messages[index].finishedAt = timestamp ?? messages[index].finishedAt
        if messages[index].kind == .tool, messages[index].toolStatus == .processing {
            messages[index].toolStatus = messages[index].isFailed ? .error : .success
        }
        bumpScrollAnchor()
    }

    private func finishOpenMessages(outcome: String = "completed", timestamp: Int64? = nil) {
        flushPendingDeltas()
        for index in messages.indices {
            guard !messages[index].isFinished else { continue }
            messages[index].isFinished = true
            messages[index].finishedAt = timestamp ?? messages[index].finishedAt
            guard messages[index].kind == .tool, messages[index].toolStatus == .processing else { continue }
            if outcome == "completed" {
                messages[index].toolStatus = .success
            } else {
                messages[index].toolStatus = .placeholder
                messages[index].placeholderReason = outcome == "failed" ? "job_failed" : "interrupted"
            }
        }
        bumpScrollAnchor()
    }

    private func applyServerQueue(_ snapshot: MessageQueueSnapshot) {
        guard snapshot.jobId == jobID else { return }
        if serverQueue.jobId == jobID, snapshot.version < serverQueue.version { return }
        for item in snapshot.items { knownQueuedItems[item.id] = item }
        if let active = snapshot.active {
            knownQueuedItems[active.id] = active
            if !messages.contains(where: { $0.id == active.id }) {
                messages.append(ChatMessage(
                    id: active.id, kind: .user, content: active.summaryLine, detail: nil,
                    isFinished: true, isFailed: false, timestamp: active.createdAt,
                    imagePaths: active.imagePaths, fileAttachments: active.fileAttachments, isOptimistic: true
                ))
            }
        }
        serverQueue = snapshot
        isTurnRunning = snapshot.willContinue || status == "running" || graphRunLive
    }

    private func scheduleSnapshotRefresh() {
        guard client != nil else { return }
        Task { [weak self] in
            guard let self else { return }
            do {
                try await self.refreshSnapshotAndHistory()
                self.reconcileAwaitingEchoIfNeeded()
                self.scheduleOutboxProcessing()
                if self.isGraph && !self.serverQueue.willContinue { self.stopStreaming() }
            } catch {
                self.errorDetail = self.errorText(error)
            }
        }
    }

    private func removeEchoedOutboxItems() {
        flushPendingDeltas()
        let serverIDs = Set(messages.filter { !$0.isOptimistic }.map(\.id))
        outbox.removeAll { item in
            switch item.state {
            case .awaitingEcho, .failed:
                return serverIDs.contains(item.id)
            default:
                return false
            }
        }
    }

    private func reconcileAwaitingEchoIfNeeded() {
        flushPendingDeltas()
        let serverIDs = Set(messages.filter { !$0.isOptimistic }.map(\.id))
        for index in outbox.indices {
            guard case .awaitingEcho = outbox[index].state else { continue }
            if serverIDs.contains(outbox[index].id) {
                continue
            }
            outbox[index].state = .failed(
                detail: "消息已提交，但当前会话历史未出现该条消息。草稿和附件已恢复，可重试。",
                requiresNewMessageID: true
            )
            publishRestore(outbox[index].draft)
        }
        removeEchoedOutboxItems()
    }

    private func publishRestore(_ draft: ComposerDraft) {
        restoreDraft = draft
        restoreDraftVersion &+= 1
    }

    private func publishTerminalStateChange() {
        terminalStateVersion &+= 1
    }

    private func applyRunOutcome(_ outcome: String) {
        guard outcome != "completed" else { return }
        let detail = outcome == "stopped"
            ? "消息对应的运行已停止。草稿和附件已恢复，可按需重试。"
            : "消息对应的运行失败。草稿和附件已恢复，可按需重试。"
        guard let index = outbox.firstIndex(where: { $0.state == .awaitingEcho }) else { return }
        let failedItem = outbox[index]
        outbox[index].state = .failed(detail: detail, requiresNewMessageID: true)
        if let messageIndex = messages.lastIndex(where: { $0.id == failedItem.id && $0.isOptimistic }) {
            messages[messageIndex].isFailed = true
        }
        publishRestore(failedItem.draft)
        removeEchoedOutboxItems()
        bumpScrollAnchor()
    }

    private func bumpScrollAnchor() {
        scrollAnchorThrottleTask?.cancel()
        scrollAnchorThrottleTask = nil
        scrollAnchor &+= 1
    }

    /// 流式追加专用的跟随滚动提示，按 240ms 节流。
    ///
    /// 视图侧收到锚点变化就得在 `LazyVStack` 上按当次 contentSize 反算“滚到底”的目标偏移。
    /// delta 合流已经是 25Hz，如果每次 flush 都请求一次滚动，就有 25 次/秒的机会撞上内容
    /// 高度的剧烈变化（长工具输出收起时卡片会从几千点塌到一行），一撞上就会算出内容之外的
    /// 偏移，`LazyVStack` 一个 cell 都不物化，整屏空白。肉眼分不出 25Hz 和 4Hz 的跟随，
    /// 把频率降下来就把撞上的窗口缩小了一个量级。
    ///
    /// 用户能感知的离散跳转（发送、历史加载完、运行收尾）继续走 `bumpScrollAnchor()`，
    /// 它会取消挂起的节流，立刻到底。
    private func bumpScrollAnchorThrottled() {
        guard scrollAnchorThrottleTask == nil else { return }
        scrollAnchorThrottleTask = Task { @MainActor [weak self] in
            try? await Task.sleep(for: .milliseconds(240))
            guard let self, !Task.isCancelled else { return }
            self.scrollAnchorThrottleTask = nil
            self.scrollAnchor &+= 1
        }
    }

    private func hasPriorConversation(_ detail: JobDetail) -> Bool {
        detail.sessionCount > 0 || messages.contains { !$0.isOptimistic }
    }

    private func applySessionMetadata(_ response: SessionMessagesResponse, agentInfo: AgentDisplayInfo?) {
        modelID = response.modelId
        agentType = response.type
        modeID = response.acpMode
        thoughtLevelID = response.acpThoughtLevel
        totalTokens = response.tokenUsage?.totalTokens ?? totalTokens
        agentDisplayName = resolvedAgentName(agentInfo)
        agentIconUrl = displayValue(agentInfo?.iconUrl)
    }

    private func resolveAgentDisplayInfo(for response: SessionMessagesResponse) async -> AgentDisplayInfo? {
        guard let reference = displayValue(response.type) else { return nil }
        if let embedded = response.agents?[reference] {
            agentDisplayInfoByReference[reference] = embedded
            return embedded
        }
        return await resolveAgentDisplayInfo(reference: reference)
    }

    private func resolveAgentDisplayInfo(reference: String) async -> AgentDisplayInfo? {
        if let cached = agentDisplayInfoByReference[reference] { return cached }
        guard let client else { return nil }
        do {
            let info = try await client.resolveAgentDisplayInfo(ids: [reference]).agents[reference]
            if let info { agentDisplayInfoByReference[reference] = info }
            return info
        } catch {
            errorDetail = errorText(error)
            return nil
        }
    }

    private func resolvedAgentName(_ info: AgentDisplayInfo?) -> String? {
        guard let info, let name = displayValue(info.displayName) else { return nil }
        return info.deleted ? "\(name)（已删除）" : name
    }

    private func applyTerminalDuration(_ event: ServerEvent) {
        if let total = event.totalTurnDurationMs {
            applyInteractiveAccumulatedDuration(total)
            currentTurnIncludedInAccumulatedDuration = true
        }
        guard currentTurnIncludedInAccumulatedDuration else { return }
        runStartedAt = nil
        runFinishedAt = nil
    }

    private func applyGraphDuration(_ snapshot: GraphRunStatusResponse) {
        guard isGraph else { return }
        let live = snapshot.instances ?? []
        let liveKeys = Set(live.map { $0.key.backendKey })
        let archived = snapshot.run?.archivedInstances?.compactMap { key, instance in
            liveKeys.contains(key) ? nil : instance
        } ?? []
        let timed = (live + archived).filter { instance in
            ["prompt", "clarify", "shell"].contains(instance.nodeType.lowercased())
                && instance.preferredSessionID != nil
        }
        graphBaseDurationMs = timed.reduce(Int64(0)) { $0 + max(0, $1.durationMs ?? 0) }
        refreshAccumulatedDuration()
        graphRunningStartedAts = timed.compactMap { instance in
            instance.status == "running" ? instance.startedAt : nil
        }
        runStartedAt = nil
        runFinishedAt = nil
        currentTurnIncludedInAccumulatedDuration = true
    }

    private func updateServerClock(_ serverTimeMs: Int64?) {
        guard let serverTimeMs, serverTimeMs > 0 else { return }
        let uptime = ProcessInfo.processInfo.systemUptime
        if let anchor = serverClockAnchor {
            let projected = anchor.serverTimeMs + Int64((uptime - anchor.uptime) * 1_000)
            guard serverTimeMs > projected else { return }
        }
        serverClockAnchor = (serverTimeMs, uptime)
    }

    private func applyInteractiveAccumulatedDuration(_ milliseconds: Int64) {
        interactiveAccumulatedDurationMs = max(0, milliseconds)
        refreshAccumulatedDuration()
    }

    private func refreshAccumulatedDuration() {
        accumulatedDurationMs = interactiveAccumulatedDurationMs + graphBaseDurationMs
    }

    private func estimatedServerNow() -> Int64 {
        guard let anchor = serverClockAnchor else {
            return Int64(Date().timeIntervalSince1970 * 1_000)
        }
        let elapsed = max(0, ProcessInfo.processInfo.systemUptime - anchor.uptime)
        return anchor.serverTimeMs + Int64(elapsed * 1_000)
    }

    private func displayValue(_ value: String?) -> String? {
        guard let trimmed = value?.trimmingCharacters(in: .whitespacesAndNewlines), !trimmed.isEmpty else { return nil }
        return trimmed
    }

    // 与 Web 端 formatTokenCount 保持一致：保留两位小数让数字在每一轮之间可见地增长，
    // 过百万切 M，否则长上下文模型会显示成 "1234.57K"。
    private static func compactCount(_ value: Int) -> String {
        guard value > 0 else { return "0" }
        guard value >= 1_000 else { return String(value) }
        guard value >= 1_000_000 else { return String(format: "%.2fK", Double(value) / 1_000) }
        return String(format: "%.2fM", Double(value) / 1_000_000)
    }

    private static func formatDuration(_ milliseconds: Int64) -> String {
        guard milliseconds >= 1_000 else { return "\(milliseconds)ms" }
        if milliseconds < 60_000 {
            return String(format: "%.1fs", Double(milliseconds) / 1_000)
        }
        if milliseconds < 3_600_000 {
            return "\(milliseconds / 60_000)m \((milliseconds % 60_000) / 1_000)s"
        }
        return "\(milliseconds / 3_600_000)h \((milliseconds % 3_600_000) / 60_000)m"
    }

    private func errorText(_ error: Error) -> String {
        if let error = error as? APIError { return error.detail }
        return String(describing: error)
    }
}

