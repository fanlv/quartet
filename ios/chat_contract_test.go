package ios

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func chatSource(t *testing.T, relativePath string) string {
	t.Helper()
	_, filename, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate iOS chat contract test")
	}
	data, err := os.ReadFile(filepath.Join(filepath.Dir(filename), relativePath))
	if err != nil {
		t.Fatalf("read %s: %v", relativePath, err)
	}
	return string(data)
}

func TestInteractiveChatPagesNewestSessionAndKeepsGraphTargetScoped(t *testing.T) {
	source := chatSource(t, "Quartet/Features/Chat/ChatViewModel.swift")
	for _, contract := range []string{
		"let interactiveSessions = detail.sessionIds ?? []",
		"loadInteractiveHistory(sessionIDs: interactiveSessions)",
		"guard let currentSessionID = nonEmptySessionIDs.last else { return }",
		"historySessionIDs = nonEmptySessionIDs",
		"func loadEarlierMessages() async",
		"beforeCursor: pageInfo?.beforeCursor",
		"preloadEarlierMessages()",
		"private static let historyPageSize = 80",
		"if requestedSession?.isEmpty == false, let sessionID",
		"idPrefix: isNewestInteractiveSession || isGraph ? nil : currentSessionID",
	} {
		if !strings.Contains(source, contract) {
			t.Fatalf("interactive history source contract missing %q", contract)
		}
	}
	client := chatSource(t, "Quartet/Core/Networking/APIClient.swift")
	for _, contract := range []string{
		`URLQueryItem(name: "paged", value: "true")`,
		`URLQueryItem(name: "before", value: beforeCursor)`,
	} {
		if !strings.Contains(client, contract) {
			t.Fatalf("paged history client contract missing %q", contract)
		}
	}
}

func TestAgentValidationStateIsVisibleAndRetried(t *testing.T) {
	models := chatSource(t, "Quartet/Core/Models/APIModels.swift")
	view := chatSource(t, "Quartet/Features/Chat/NewConversationView.swift")
	for _, contract := range []string{
		`case availability`,
		`availability == "pending_validation"`,
		`availability == "validating"`,
	} {
		if !strings.Contains(models, contract) {
			t.Fatalf("Agent DTO source contract missing %q", contract)
		}
	}
	for _, contract := range []string{
		"for attempt in 1...15",
		"try await Task.sleep(for: .seconds(2))",
		"agent?.available != true",
		"重新检查",
	} {
		if !strings.Contains(view, contract) {
			t.Fatalf("Agent validation source contract missing %q", contract)
		}
	}
}

func TestStreamConnectionStateRemainsInternalToChat(t *testing.T) {
	source := chatSource(t, "Quartet/Features/Chat/ChatViewModel.swift")
	for _, contract := range []string{
		"case connecting",
		"case live",
		"case reconnecting",
		"streamState = .offline",
		"self.streamState = .live",
		"if self.isGraph && !self.serverQueue.willContinue { self.stopStreaming() }",
	} {
		if !strings.Contains(source, contract) {
			t.Fatalf("SSE status source contract missing %q", contract)
		}
	}
	view := chatSource(t, "Quartet/Features/Chat/JobChatView.swift")
	for _, removedPresentation := range []string{
		"private var statusStrip",
		"Text(chat.streamStateLabel)",
		"var streamStateLabel",
	} {
		if strings.Contains(view, removedPresentation) {
			t.Fatalf("chat must not present connection/status chrome %q", removedPresentation)
		}
	}
}

func TestOpeningChatRefreshesThoughtLevelsForTheRestoredAgentAndModel(t *testing.T) {
	source := chatSource(t, "Quartet/Features/Chat/JobChatView.swift")
	appModel := chatSource(t, "Quartet/App/AppModel.swift")
	for _, contract := range []string{
		".task(id: thoughtLevelSelection)",
		"await refreshThoughtLevels(for: thoughtLevelSelection)",
		"appModel.relinkACPThoughtLevels(",
		"guard thoughtLevelSelection == selection, thoughtLevelRequestID == requestID",
		"chat.reconcileThoughtLevelID(currentThoughtLevelID)",
	} {
		if !strings.Contains(source, contract) {
			t.Fatalf("restored chat thought-level refresh contract missing %q", contract)
		}
	}
	for _, contract := range []string{
		"func relinkACPThoughtLevels(agentType: String, modelID: String)",
		"target: .model",
		"agentType: agentType",
		"model: modelID",
	} {
		if !strings.Contains(appModel, contract) {
			t.Fatalf("thought-level refresh must use the Agent/model preview path: missing %q", contract)
		}
	}
}

func TestExistingChatCanSwitchModelAndThoughtLevelAndShowsWorkspaceContext(t *testing.T) {
	chat := chatSource(t, "Quartet/Features/Chat/JobChatView.swift")
	client := chatSource(t, "Quartet/Core/Networking/APIClient.swift")
	models := chatSource(t, "Quartet/Core/Models/APIModels.swift")

	for _, contract := range []string{
		`ChatConfigurationSelectionSheet(`,
		`agentPreferences = try await appModel.agentPreferences()`,
		`optionGroup(favoriteOptions, title: "收藏".localizedForApp)`,
		`optionGroup(otherOptions, title: "其他模型".localizedForApp)`,
		`.padding(.top, 8)`,
		`.background(QuartetTheme.canvas)`,
		`accessibilityIdentifier("chat-model-selector")`,
		`accessibilityIdentifier("chat-thought-level-selector")`,
		`appModel.setACPConfig(SetACPConfigRequest(`,
		`sessionId: sessionID`,
		`workspaceName ?? route.summary.workspaceId`,
		`workspaceWorkdir`,
		`accessibilityIdentifier("workspace-footer")`,
		`gitBranch = response.branch`,
	} {
		if !strings.Contains(chat, contract) {
			t.Fatalf("existing-chat composer contract missing %q", contract)
		}
	}
	if !strings.Contains(client, `path: "api/v1/git-branch"`) {
		t.Fatal("iOS client must expose the workspace git-branch endpoint")
	}
	if !strings.Contains(models, "struct GitBranchResponse") {
		t.Fatal("iOS models must decode the workspace git-branch response")
	}
	if strings.Contains(chat, `Label("历史会话", systemImage: "clock.arrow.circlepath")`) {
		t.Fatal("the chat history action must render as an icon-only button")
	}
}

func TestJobRouteAndIdempotentSendUsePersistedServerState(t *testing.T) {
	handler := chatSource(t, "../cmd/web/handler/job.go")
	models := chatSource(t, "Quartet/Core/Models/APIModels.swift")
	client := chatSource(t, "Quartet/Core/Networking/APIClient.swift")
	jobsView := chatSource(t, "Quartet/Features/Jobs/JobsView.swift")
	chat := chatSource(t, "Quartet/Features/Chat/ChatViewModel.swift")

	for _, contract := range []string{
		"job.InitialAgentID = req.AgentType",
		"job.FirstModelID = req.ModelID",
	} {
		if !strings.Contains(handler, contract) {
			t.Fatalf("Job creation source contract missing %q", contract)
		}
	}
	if !strings.Contains(jobsView, "agentType: job.agentId") || strings.Contains(jobsView, "agentType: workspace(for: job)?.defaultAgent") {
		t.Fatal("Job route must use the persisted Job Agent, never the mutable workspace default")
	}
	for _, contract := range []string{
		"struct SendMessageResponse: Decodable",
		"let messageState: String?",
	} {
		if !strings.Contains(models, contract) {
			t.Fatalf("send response DTO source contract missing %q", contract)
		}
	}
	if !strings.Contains(client, "async throws -> SendMessageResponse") {
		t.Fatal("APIClient.sendMessage must return the receipt response")
	}
	for _, contract := range []string{
		`case "processing":`,
		`case "completed":`,
		`case "failed", "stopped", "interrupted":`,
		"requiresNewMessageID: true",
		"let startsNewExecution = item.retryRequiresNewMessageID",
		"id: startsNewExecution ? UUID().uuidString.lowercased() : item.id",
	} {
		if !strings.Contains(chat, contract) {
			t.Fatalf("idempotent send source contract missing %q", contract)
		}
	}
}

func TestTerminalFailureUsesANewRetryIDAndDropsTheEchoedOutboxBubble(t *testing.T) {
	chat := chatSource(t, "Quartet/Features/Chat/ChatViewModel.swift")
	applyStart := strings.Index(chat, "private func applyRunOutcome")
	if applyStart < 0 {
		t.Fatal("cannot locate applyRunOutcome")
	}
	applyBody := chat[applyStart:]
	if !strings.Contains(applyBody, "requiresNewMessageID: true") {
		t.Fatal("terminal failure must force an explicit retry to use a new clientMessageId")
	}
	if !strings.Contains(applyBody, "removeEchoedOutboxItems()") {
		t.Fatal("terminal failure must reconcile its optimistic bubble after history becomes durable")
	}
	markStart := strings.Index(chat, "private func markOutboxFailed")
	if markStart < 0 || !strings.Contains(chat[markStart:], "removeEchoedOutboxItems()") {
		t.Fatal("duplicate terminal receipts must also reconcile a now-persisted optimistic bubble")
	}
	if !strings.Contains(chat, "case .awaitingEcho, .failed:") {
		t.Fatal("history reconciliation must remove both awaiting and failed optimistic bubbles once their stable IDs are persisted")
	}
}

func TestOutboxFreezesTheEntireIdempotentRequestAcrossSameIDRetries(t *testing.T) {
	chat := chatSource(t, "Quartet/Features/Chat/ChatViewModel.swift")
	for _, contract := range []string{
		"struct OutboxRequestContext",
		"let targetSessionID: String?",
		"let modelID: String?",
		"let agentType: String?",
		"let modeID: String?",
		"let thoughtLevelID: String?",
		"requestContext: currentRequestContext(bypassCommand: isInitialDraft)",
		"let startsNewExecution = item.retryRequiresNewMessageID",
		"? currentRequestContext(bypassCommand: item.isInitialDraft)",
		": item.requestContext",
		"createdAt: startsNewExecution ? Int64(Date().timeIntervalSince1970 * 1_000) : item.createdAt",
		"modelId: item.requestContext.modelID",
		"agentType: item.requestContext.agentType",
		"sessionId: item.requestContext.targetSessionID",
		"acpMode: item.requestContext.modeID",
		"acpThoughtLevel: item.requestContext.thoughtLevelID",
		"remoteImagePaths: item.remoteImagePaths",
		"isTurnRunning = status == \"running\"",
	} {
		if !strings.Contains(chat, contract) {
			t.Fatalf("frozen outbox request contract missing %q", contract)
		}
	}
}

func TestCommandDuplicateAndInlineSSEDedupUseClientMessageID(t *testing.T) {
	models := chatSource(t, "Quartet/Core/Models/APIModels.swift")
	chat := chatSource(t, "Quartet/Features/Chat/ChatViewModel.swift")
	if !strings.Contains(models, "let clientMessageId: String?") ||
		!strings.Contains(models, "case type, sessionId, clientMessageId") ||
		!strings.Contains(models, "clientMessageId = try values.decodeIfPresent(String.self, forKey: .clientMessageId)") {
		t.Fatal("ServerEvent must decode command clientMessageId for inline/SSE deduplication")
	}
	for _, contract := range []string{
		`response.status == "command_dispatched" || response.status == "command_duplicate"`,
		"applyCommandEvent(event, fallbackClientMessageID: itemID)",
		"private func applyCommandEvent(_ event: ServerEvent, fallbackClientMessageID: String? = nil)",
		"let messageID = event.clientMessageId ?? fallbackClientMessageID ?? UUID().uuidString",
		"id: messageID,",
		"kind: .system,",
		"outbox.removeAll { $0.id == messageID }",
	} {
		if !strings.Contains(chat, contract) {
			t.Fatalf("command duplicate source contract missing %q", contract)
		}
	}
}

func TestCreateJobUsesAStableIntentIDUntilTheSemanticPayloadChanges(t *testing.T) {
	models := chatSource(t, "Quartet/Core/Models/APIModels.swift")
	view := chatSource(t, "Quartet/Features/Chat/NewConversationView.swift")
	client := chatSource(t, "Quartet/Core/Networking/APIClient.swift")
	for _, contract := range []string{
		"let status: String?",
		"let clientMessageId: String?",
		"clientMessageId: String? = nil",
	} {
		if !strings.Contains(models, contract) {
			t.Fatalf("CreateJob DTO source contract missing %q", contract)
		}
	}
	for _, contract := range []string{
		"private struct CreateJobIntentPayload: Equatable",
		"@State private var createIntent: CreateJobIntent?",
		"return CreateJobIntentPayload(",
		"if createIntent?.payload != payload",
		"id: UUID().uuidString.lowercased()",
		"clientMessageId: createIntent.id",
		"createIntent = nil",
		"if isDefinitelyRejected(error)",
		"agentType: payload.agentType",
		"modelID: payload.modelID",
	} {
		if !strings.Contains(view, contract) {
			t.Fatalf("CreateJob intent source contract missing %q", contract)
		}
	}
	if !strings.Contains(client, "requestWasRejected: true") {
		t.Fatal("definite client-side/HTTP rejection must rotate the CreateJob intent ID")
	}
}

// The message queue reports the message the backend is running as `active`, and
// the run persists it before the agent produces anything. Pagination only loads
// the newest page, so on a long turn that message is on disk but ABOVE the
// loaded window: "not in the list" must not be read as "not sent yet", or the
// user's own question renders below the replies to it.
func TestRunningQueueMessageOutsideTheWindowIsPinnedAboveItInsteadOfAppended(t *testing.T) {
	source := chatSource(t, "Quartet/Features/Chat/ChatViewModel.swift")
	for _, contract := range []string{
		// Single placement helper shared by the queue snapshot and RUN_STARTED.
		"private func insertRunningQueueMessage(_ item: QueuedJobMessage, timestamp: Int64?, isOptimistic: Bool)",
		"insertRunningQueueMessage(active, timestamp: active.createdAt, isOptimistic: true)",
		"insertRunningQueueMessage(queued, timestamp: event.timestamp ?? queued.createdAt, isOptimistic: false)",
		// Older than the loaded window => pinned to its front, never appended.
		"if hasMoreEarlierMessages, let timestamp, timestamp < newestLoaded {",
		"pinned.isRoundHeadPinned = true",
		"messages.insert(pinned, at: 0)",
		// Not synthesised until the window it is placed against is known.
		"if historyWindowEstablished, !messages.contains(where: { $0.id == active.id }) {",
		// Backwards paging hands the position back to the real record.
		"let pinnedIDs = Set(messages.filter(\\.isRoundHeadPinned).map(\\.id))",
		"Set(messages.map(\\.id)).subtracting(pinnedIDs)",
		"prependEarlierPage(uniqueEarlier, into: messages, pageIDs: Set(collected.map(\\.id)))",
	} {
		if !strings.Contains(source, contract) {
			t.Fatalf("running queue message placement contract missing %q", contract)
		}
	}
	models := chatSource(t, "Quartet/Core/Models/APIModels.swift")
	if !strings.Contains(models, "var isRoundHeadPinned: Bool") {
		t.Fatal("ChatMessage must carry the pinned round-head marker")
	}
}

// A newest-page reload describes only the tail of the transcript. Rebuilding
// the list from it drops the earlier pages the user scrolled in, and
// re-appending the live bubbles it cannot carry renders an older bubble below a
// newer one. Only a page for a different session may replace the list.
func TestNewestHistoryPageIsSplicedInsteadOfReplacingTheList(t *testing.T) {
	source := chatSource(t, "Quartet/Features/Chat/ChatViewModel.swift")
	for _, contract := range []string{
		"private var loadedMessagesSessionID: String?",
		"applyNewestHistoryPage(historyMessages, sessionID: sessionID)",
		"applyNewestHistoryPage(combined, sessionID: currentSessionID)",
		"if loadedMessagesSessionID == sessionID {",
		"messages = Self.mergeLatestHistoryPage(existing: messages, page: page)",
		"static func mergeLatestHistoryPage(existing: [ChatMessage], page: [ChatMessage]) -> [ChatMessage]",
		// The page is a spine, not a rebuild: transient bubbles keep their slot.
		"guard let spineStart = body.firstIndex(where: { pagePositionByID[$0.id] != nil }) else {",
		"} else if !isSuperseded(message), isTransientBubble(message) {",
		"static func dedupedByID(_ messages: [ChatMessage]) -> [ChatMessage]",
	} {
		if !strings.Contains(source, contract) {
			t.Fatalf("newest page splice contract missing %q", contract)
		}
	}
	// The wholesale replace of the visible timeline must not come back.
	for _, banned := range []string{"messages = historyMessages", "messages = combined"} {
		if strings.Contains(source, banned) {
			t.Fatalf("newest page must not replace the visible timeline: found %q", banned)
		}
	}
}

// Pinning is pointless if the render window drops it: the timeline only renders
// a tail-aligned slice, and the pinned round head sits at the very front, which
// is exactly what that window discards first.
func TestPinnedRoundHeadIsAlwaysRenderedAndNeverUsedAsThePrependAnchor(t *testing.T) {
	view := chatSource(t, "Quartet/Features/Chat/JobChatView.swift")
	for _, contract := range []string{
		"private var pinnedRoundHeadCount: Int",
		"chat.messages.prefix(while: { $0.isRoundHeadPinned }).count",
		// Outside the window quota, always rendered in front of it.
		"return Array(chat.messages.prefix(pinnedCount))",
		"+ Array(chat.messages.dropFirst(pinnedCount).suffix(effectiveTimelineMessageCount))",
		// Not counted as hidden earlier history, so the load-earlier affordance
		// still describes the real remainder.
		"max(0, chat.messages.count - pinnedRoundHeadCount - effectiveTimelineMessageCount)",
		// A pin does not move across a prepend, so it cannot anchor the restore.
		"private var timelinePrependAnchorID: String?",
		"timelineMessages.first(where: { !$0.isRoundHeadPinned })?.id",
	} {
		if !strings.Contains(view, contract) {
			t.Fatalf("pinned round head rendering contract missing %q", contract)
		}
	}
	if strings.Contains(view, "let anchor = timelineMessages.first?.id") {
		t.Fatal("prepend anchor must skip pinned round heads")
	}
}

// Backwards paging must not park a prepended page above the render window, and
// must not make the user hit the very top before the next page is fetched.
func TestBackwardsPagingKeepsTwoPagesBufferedAndRendersEveryLoadedRecord(t *testing.T) {
	view := chatSource(t, "Quartet/Features/Chat/JobChatView.swift")
	for _, contract := range []string{
		// Two pages buffered after the first paint, without a prepend anchor so
		// the follow-the-bottom anchoring is not disturbed.
		"private func primeEarlierTimelineBuffer() {",
		"guard timelineMode.isFollowing, pendingTimelinePrependAnchor == nil, !earlierPageRequestInFlight else { return }",
		"await chat.start(route: route, client: client)\n                primeEarlierTimelineBuffer()",
		// One page from the top is the fetch trigger, not the top itself.
		"private var earlierBufferSentinelIndex: Int?",
		"let index = pinnedRoundHeadCount + ChatTimelineWindow.earlierPageSize",
		"if index == earlierBufferSentinelIndex {",
	} {
		if !strings.Contains(view, contract) {
			t.Fatalf("earlier-page buffering contract missing %q", contract)
		}
	}
	// Both prepend paths must widen the window by what they added. The reveal
	// branch used to skip it, which parked the whole new page above the window -
	// and that region is the TOP of the list, so it un-rendered what the user was
	// reading. Three sites: prime, reveal-chained fetch, direct fetch.
	if got := strings.Count(view, "visibleTimelineMessageCount += loadedCount"); got != 3 {
		t.Fatalf("every prepend must widen the render window: got %d sites, want 3", got)
	}
}
