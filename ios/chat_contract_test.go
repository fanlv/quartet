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

func TestInteractiveChatLoadsAllSessionsAndKeepsGraphTargetScoped(t *testing.T) {
	source := chatSource(t, "Quartet/Features/Chat/JobChatView.swift")
	for _, contract := range []string{
		"let interactiveSessions = detail.sessionIds ?? []",
		"loadInteractiveHistory(sessionIDs: interactiveSessions)",
		"for (index, currentSessionID) in nonEmptySessionIDs.enumerated()",
		"if requestedSession?.isEmpty == false, let sessionID",
		"ChatMessage(history: $0, idPrefix: isLatestSession ? nil : currentSessionID)",
	} {
		if !strings.Contains(source, contract) {
			t.Fatalf("interactive history source contract missing %q", contract)
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
	source := chatSource(t, "Quartet/Features/Chat/JobChatView.swift")
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
	for _, removedPresentation := range []string{
		"private var statusStrip",
		"Text(chat.streamStateLabel)",
		"var streamStateLabel",
	} {
		if strings.Contains(source, removedPresentation) {
			t.Fatalf("chat must not present connection/status chrome %q", removedPresentation)
		}
	}
}

func TestOpeningChatDoesNotMutateACPConfigurationForDisplayMetadata(t *testing.T) {
	source := chatSource(t, "Quartet/Features/Chat/JobChatView.swift")
	for _, forbidden := range []string{
		"thoughtLevelDisplayConfigurationKey",
		"refreshThoughtLevelDisplayOptions",
		"relinkACPThoughtLevels(",
	} {
		if strings.Contains(source, forbidden) {
			t.Fatalf("opening an existing chat must not probe or mutate ACP config just to render metadata: found %q", forbidden)
		}
	}
	if !strings.Contains(source, "AgentConfigurationDisplay.thoughtLevelName(") {
		t.Fatal("chat metadata must resolve the thought-level label from the cached Agent catalog")
	}
}

func TestExistingChatCanSwitchModelAndThoughtLevelAndShowsWorkspaceContext(t *testing.T) {
	chat := chatSource(t, "Quartet/Features/Chat/JobChatView.swift")
	client := chatSource(t, "Quartet/Core/Networking/APIClient.swift")
	models := chatSource(t, "Quartet/Core/Models/APIModels.swift")

	for _, contract := range []string{
		`Menu {`,
		`accessibilityIdentifier("chat-model-selector")`,
		`accessibilityIdentifier("chat-thought-level-selector")`,
		`appModel.setACPConfig(SetACPConfigRequest(`,
		`sessionId: sessionID`,
		`Workspace(`,
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
	chat := chatSource(t, "Quartet/Features/Chat/JobChatView.swift")

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
	chat := chatSource(t, "Quartet/Features/Chat/JobChatView.swift")
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
	chat := chatSource(t, "Quartet/Features/Chat/JobChatView.swift")
	for _, contract := range []string{
		"private struct OutboxRequestContext",
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
	chat := chatSource(t, "Quartet/Features/Chat/JobChatView.swift")
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
