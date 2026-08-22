package contracts

import (
	"os"
	"strings"
	"testing"
)

func TestGlobalNotificationObservationTraversesEveryChangePage(t *testing.T) {
	source := readSource(t, "../Quartet/App/AppModel.swift")
	fetchBody := swiftFunctionBody(t, source, "private func fetchJobObservations")

	requireContains(t, fetchBody, "while true")
	requireContains(t, fetchBody, "client.jobObservations(cursor: cursor, limit: 200)")
	requireContains(t, fetchBody, "activeJobsByID.removeAll(keepingCapacity: true)")
	requireContains(t, fetchBody, "activeJobsByID[job.id] = job")
	requireContains(t, fetchBody, "changes.append(contentsOf: page.changes)")
	requireContains(t, fetchBody, "guard page.hasMore else")
	requireContains(t, fetchBody, "let nextCursor = page.cursor")
	requireContains(t, fetchBody, "seenCursors.insert(nextCursor).inserted")

	refreshBody := swiftFunctionBody(t, source, "private func performDashboardRefresh")
	requireContains(t, refreshBody, "fetchJobObservations(client: client)")
	requireContains(t, refreshBody, "observation: observationResponse")
	requireContains(t, refreshBody, "changes: observationResponse.changes")
	requireContains(t, refreshBody, "activeJobs: observationResponse.activeJobs")
	requireContains(t, refreshBody, "applyObservationSnapshot(observationResponse)")
	applyBody := swiftFunctionBody(t, source, "private func applyObservationSnapshot")
	requireContains(t, applyBody, "observation.activeJobs.map { ($0.id, $0) }")
	requireContains(t, applyBody, "for change in observation.changes")
	requireContains(t, applyBody, "jobObservationCursor = observation.cursor")

	models := readSource(t, "../Quartet/Core/Models/APIModels.swift")
	requireContains(t, models, "struct JobObservationPage: Decodable, Sendable")
	requireContains(t, models, "struct JobObservationEvent: Decodable, Sendable")
	requireContains(t, models, "let eventId: String")
	requireContains(t, models, "let previousStatus: String?")
	requireContains(t, models, "let graphRunId: String?")
	requireContains(t, models, "let graphStatus: String?")
	requireContains(t, models, "let previousGraphStatus: String?")
	requireContains(t, models, "let graphSessionId: String?")
	requireContains(t, models, "let runOutcome: String?")
	requireContains(t, models, "let occurredAt: Int64")

	client := readSource(t, "../Quartet/Core/Networking/APIClient.swift")
	requireContains(t, client, "func jobObservations(cursor: String? = nil, limit: Int = 200)")
	requireContains(t, client, "api/v1/job/observations")
}

func TestGraphStatusObservationIsChangeDrivenAndConcurrencyBounded(t *testing.T) {
	source := readSource(t, "../Quartet/App/AppModel.swift")
	candidateBody := swiftFunctionBody(t, source, "private func graphStatusObservationCandidates")
	requireContains(t, source, "changes: [JobObservationEvent]")
	requireContains(t, source, "activeJobs: [JobSummary]")
	requireContains(t, candidateBody, "for pendingJob in pendingGraphStatusObservationJobs")
	requireContains(t, candidateBody, "latestJobsByID[change.job.id] = change.job")
	if strings.Contains(candidateBody, "Dictionary(uniqueKeysWithValues:") {
		t.Fatal("multiple lifecycle changes for one Graph Job must not trap on duplicate dictionary keys")
	}
	requireContains(t, candidateBody, "change.graphStatus == nil")
	requireContains(t, candidateBody, "job.status == \"stopped\"")
	requireContains(t, candidateBody, "candidates.append(job)")

	refreshBody := swiftFunctionBody(t, source, "private func refreshGraphStatuses")
	requireContains(t, refreshBody, "let maximumRequestsPerRefresh = 24")
	requireContains(t, refreshBody, "Array(graphJobs.prefix(maximumRequestsPerRefresh))")
	requireContains(t, refreshBody, "Array(graphJobs.dropFirst(maximumRequestsPerRefresh))")
	requireContains(t, refreshBody, "failedRequestedJobs.append((requestIndex, job))")
	requireContains(t, refreshBody, "pendingGraphStatusObservationJobs = retryJobs")
	if strings.Contains(candidateBody, "sorted {") {
		t.Fatal("fallback candidates must retain FIFO order across polling rounds")
	}
	requireContains(t, refreshBody, "let maximumConcurrentRequests = 6")
	requireContains(t, refreshBody, "0..<min(maximumConcurrentRequests, jobIDs.count)")
	requireContains(t, refreshBody, "while let (requestIndex, jobID, response) = await group.next()")
	requireContains(t, refreshBody, "failedRequestedJobs.append((requestIndex, job))")
	requireContains(t, refreshBody, "failedRequestedJobs.sorted { $0.0 < $1.0 }")
	if strings.Contains(refreshBody, "for job in graphJobs {") {
		t.Fatal("refreshGraphStatuses must not create one child task for every historical Graph Job")
	}
}

func TestGraphTimedOutUsesFailedNotificationOutcome(t *testing.T) {
	source := readSource(t, "../Quartet/App/AppModel.swift")
	normalizeBody := swiftFunctionBody(t, source, "private static func normalizedNotificationOutcome")
	requireContains(t, normalizeBody, "status == \"timedOut\" ? \"failed\" : status")

	emitBody := swiftFunctionBody(t, source, "private func emitNotificationIfNeeded")
	requireContains(t, emitBody, "let oldOutcome = Self.normalizedNotificationOutcome(oldStatus)")
	requireContains(t, emitBody, "let newOutcome = Self.normalizedNotificationOutcome(newStatus)")
	requireContains(t, emitBody, "outcome: newOutcome")
	requireContains(t, emitBody, "displayStatus: newStatus")

	statusLabelBody := swiftFunctionBody(t, source, "private func statusLabel")
	requireContains(t, statusLabelBody, "case \"timedOut\": \"已超时\"")
	processBody := swiftFunctionBody(t, source, "private func processDashboardNotifications")
	requireContains(t, processBody, "let oldStatus = change.previousGraphStatus")
	requireContains(t, processBody, "change.graphStatus ?? change.runOutcome ?? job.status")
	requireContains(t, processBody, "graphSessionID: change.graphSessionId")
	requireContains(t, processBody, "occurredAtMilliseconds: change.occurredAt")
	requireContains(t, processBody, "graphJobStates[job.id] = GraphJobState(status: graphStatus")
	requireContains(t, processBody, "repeatsActionableGraphState")
	requireContains(t, processBody, "newStatus == \"awaitingInput\"")
}

func TestJournalTerminalSuppressesDuplicateInteractiveNotification(t *testing.T) {
	source := readSource(t, "../Quartet/App/AppModel.swift")
	processBody := swiftFunctionBody(t, source, "private func processDashboardNotifications")
	requireContains(t, processBody, "interactiveFinalStatusesAwaitingSync[job.id]")
	requireContains(t, processBody, "interactiveFinalStatusesAwaitingSync[job.id] == suppressionKey")
	requireContains(t, processBody, "interactiveFinalStatusesAwaitingSync.removeValue(forKey: job.id)")
	requireContains(t, processBody, "Self.terminalEventIdentity(change.occurredAt)")
	recordBody := swiftFunctionBody(t, source, "func recordInteractiveTerminalEvent")
	requireContains(t, recordBody, "let eventIdentity = Self.terminalEventIdentity(terminalTimestamp)")
	requireContains(t, recordBody, "Self.interactiveTerminalSuppressionKey(")
	if strings.Contains(processBody, "interactiveFinalStatusesAwaitingSync.removeValue(forKey: job.id)\n                ??") {
		t.Fatal("a non-terminal journal event must not consume terminal notification suppression")
	}
}

func TestObservationCursorCommitsOnlyAfterCompleteConsumption(t *testing.T) {
	source := readSource(t, "../Quartet/App/AppModel.swift")
	fetchBody := swiftFunctionBody(t, source, "private func fetchJobObservations")
	requireContains(t, fetchBody, "if page.reset { return page }")
	if strings.Contains(fetchBody, "jobObservationCursor =") {
		t.Fatal("partial observation pages must not advance the committed cursor")
	}
	applyBody := swiftFunctionBody(t, source, "private func applyObservationSnapshot")
	requireContains(t, applyBody, "jobObservationCursor = observation.cursor")
	processBody := swiftFunctionBody(t, source, "private func processDashboardNotifications")
	requireContains(t, processBody, "guard !observation.reset else")
	requireContains(t, processBody, "pendingGraphStatusObservationJobs = []")
}

func TestGraphEventsAreConsumedWithoutDetailReplay(t *testing.T) {
	source := readSource(t, "../Quartet/App/AppModel.swift")
	processBody := swiftFunctionBody(t, source, "private func processDashboardNotifications")
	requireContains(t, processBody, "if let graphStatus = change.graphStatus")
	requireContains(t, processBody, "pendingGraphStatusObservationJobs.removeAll { $0.id == job.id }")
	requireContains(t, processBody, "Self.graphEventIdentity(")
	requireContains(t, processBody, "Self.terminalEventIdentity(change.occurredAt)")

	candidateBody := swiftFunctionBody(t, source, "private func graphStatusObservationCandidates")
	requireContains(t, candidateBody, "change.graphStatus == nil")
	requireContains(t, candidateBody, "for pendingJob in pendingGraphStatusObservationJobs")
}

func TestLegacyGraphEnrichmentNeverOverwritesExactJournalState(t *testing.T) {
	source := readSource(t, "../../cmd/web/handler/job_observation.go")
	requireContains(t, source, "change.GraphStatus != \"\"")
	requireContains(t, source, "change.GraphRunID == \"\"")
	requireContains(t, source, "GetRunStatus(ctx, change.GraphRunID)")
}

func TestDashboardPollingIsFiveSecondsAndWorkspaceIndependent(t *testing.T) {
	source := readSource(t, "../Quartet/App/RootView.swift")
	requireContains(t, source, "var nextPoll = clock.now.advanced(by: .seconds(5))")
	requireContains(t, source, "try await clock.sleep(until: nextPoll)")
	requireContains(t, source, "nextPoll = nextPoll.advanced(by: .seconds(5))")
	requireContains(t, source, "await model.pollNotifications()")
	requireContains(t, source, "scenePhase == .active && model.phase == .connected")
	jobsView := readSource(t, "../Quartet/Features/Jobs/JobsView.swift")
	if strings.Contains(jobsView, "ContinuousClock()") {
		t.Fatal("notification polling must stay at RootView so switching tabs does not cancel it")
	}
	if strings.Contains(source, "Task.sleep(for: .seconds(model.activeJobCount") {
		t.Fatal("notification observation cadence must not depend on the selected workspace active count")
	}
}

func TestNotificationDestinationDoesNotFailOnInitialNilTaskID(t *testing.T) {
	source := readSource(t, "../Quartet/Features/Jobs/JobsView.swift")
	taskStart := strings.Index(source, ".task(id: model.pendingNotificationDestination)")
	if taskStart < 0 {
		t.Fatal("missing notification destination task")
	}
	taskBody := source[taskStart:]
	requireContains(t, taskBody, "guard let destination = model.pendingNotificationDestination else { return }")
	requireContains(t, taskBody, "guard let summary = await model.notificationDestinationSummary() else")
	requireContains(t, taskBody, "guard model.pendingNotificationDestination == destination else { return }")
}

func readSource(t *testing.T, path string) string {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	return string(data)
}

func swiftFunctionBody(t *testing.T, source, signature string) string {
	t.Helper()
	start := strings.Index(source, signature)
	if start < 0 {
		t.Fatalf("missing Swift function %q", signature)
	}
	openOffset := strings.Index(source[start:], "{")
	if openOffset < 0 {
		t.Fatalf("missing opening brace for Swift function %q", signature)
	}
	open := start + openOffset
	depth := 0
	for index := open; index < len(source); index++ {
		switch source[index] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return source[open : index+1]
			}
		}
	}
	t.Fatalf("missing closing brace for Swift function %q", signature)
	return ""
}

func requireContains(t *testing.T, source, fragment string) {
	t.Helper()
	if !strings.Contains(source, fragment) {
		t.Fatalf("missing contract fragment %q", fragment)
	}
}
