package job

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/types/model"
)

func TestObserveJobsBaselineAndLifecycleChanges(t *testing.T) {
	service := newObservationTestService(
		observationTestJob("active-old", model.JobStatusRunning, 1),
		observationTestJob("finished-old", model.JobStatusCompleted, 1),
	)

	baseline, err := service.ObserveJobs("", 100)
	if err != nil {
		t.Fatal(err)
	}
	if !baseline.Reset || len(baseline.Changes) != 0 {
		t.Fatalf("baseline = %#v, want reset without historical changes", baseline)
	}
	if len(baseline.ActiveJobs) != 1 || baseline.ActiveJobs[0].ID != "active-old" {
		t.Fatalf("active baseline = %#v, want active-old", baseline.ActiveJobs)
	}

	service.mu.Lock()
	service.jobs["active-old"].Status = model.JobStatusCompleted
	service.jobs["active-old"].FinishedAt = 2
	service.jobs["active-old"].UpdatedAt = time.UnixMilli(2)
	activeCompleted := service.jobs["active-old"].DeepCopy()
	quick := observationTestJob("quick-new", model.JobStatusCompleted, 3)
	service.jobs[quick.ID] = quick
	service.mu.Unlock()
	service.recordJobObservation(activeCompleted, "")
	service.recordJobObservation(quick.DeepCopy(), "")

	next, err := service.ObserveJobs(baseline.Cursor, 100)
	if err != nil {
		t.Fatal(err)
	}
	if next.Reset || next.HasMore || len(next.ActiveJobs) != 0 {
		t.Fatalf("next metadata = %#v", next)
	}
	if len(next.Changes) != 2 {
		t.Fatalf("changes = %#v, want active terminal plus quick new job", next.Changes)
	}
	byID := map[string]model.JobObservationEvent{}
	for _, change := range next.Changes {
		byID[change.Job.ID] = change
	}
	if got := byID["active-old"].PreviousState; got != model.JobStatusRunning {
		t.Fatalf("active-old previous status = %q, want running", got)
	}
	if got := byID["quick-new"].PreviousState; got != "" {
		t.Fatalf("quick-new previous status = %q, want empty for newly observed job", got)
	}
}

func TestObserveJobsPaginatesStableChangeJournal(t *testing.T) {
	service := newObservationTestService()
	baseline, err := service.ObserveJobs("", 2)
	if err != nil {
		t.Fatal(err)
	}

	for index := 0; index < 5; index++ {
		id := fmt.Sprintf("job-%d", index)
		job := observationTestJob(id, model.JobStatusCompleted, int64(index+1))
		service.mu.Lock()
		service.jobs[id] = job
		service.mu.Unlock()
		service.recordJobObservation(job.DeepCopy(), "")
	}

	var ids []string
	cursor := baseline.Cursor
	pageNumber := 0
	for {
		page, err := service.ObserveJobs(cursor, 2)
		if err != nil {
			t.Fatal(err)
		}
		for _, change := range page.Changes {
			ids = append(ids, change.Job.ID)
		}
		cursor = page.Cursor
		if pageNumber == 0 {
			job := observationTestJob("job-5", model.JobStatusCompleted, 6)
			service.mu.Lock()
			service.jobs[job.ID] = job
			service.mu.Unlock()
			service.recordJobObservation(job.DeepCopy(), "")
		}
		pageNumber++
		if !page.HasMore {
			break
		}
	}
	if got, want := fmt.Sprint(ids), "[job-0 job-1 job-2 job-3 job-4 job-5]"; got != want {
		t.Fatalf("ids = %s, want %s", got, want)
	}
}

func TestObserveJobsKeepsCompletedRunningCompletedBetweenPolls(t *testing.T) {
	job := observationTestJob("repeat", model.JobStatusCompleted, 1)
	service := newObservationTestService(job)
	baseline, err := service.ObserveJobs("", 100)
	if err != nil {
		t.Fatal(err)
	}

	service.mu.Lock()
	job.Status = model.JobStatusRunning
	job.StartedAt = 2
	job.FinishedAt = 0
	job.UpdatedAt = time.UnixMilli(2)
	running := job.DeepCopy()
	service.mu.Unlock()
	service.recordJobObservation(running, "")

	service.mu.Lock()
	job.Status = model.JobStatusCompleted
	job.FinishedAt = 3
	job.UpdatedAt = time.UnixMilli(3)
	completedAgain := job.DeepCopy()
	service.mu.Unlock()
	service.recordJobObservation(completedAgain, "")

	response, err := service.ObserveJobs(baseline.Cursor, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Changes) != 2 {
		t.Fatalf("changes = %#v, want running and second completed events", response.Changes)
	}
	if response.Changes[0].Job.Status != model.JobStatusRunning || response.Changes[1].Job.Status != model.JobStatusCompleted {
		t.Fatalf("statuses = [%s %s], want [running completed]", response.Changes[0].Job.Status, response.Changes[1].Job.Status)
	}
	if response.Changes[1].PreviousState != model.JobStatusRunning {
		t.Fatalf("second completed previous status = %q, want running", response.Changes[1].PreviousState)
	}
	if response.Changes[0].EventID == response.Changes[1].EventID {
		t.Fatal("separate runs must have distinct observation event IDs")
	}
}

func TestObserveJobsKeepsRepeatedTerminalOutcomeForDifferentRuns(t *testing.T) {
	job := observationTestJob("repeat-terminal", model.JobStatusCompleted, 1)
	service := newObservationTestService(job)
	baseline, err := service.ObserveJobs("", 100)
	if err != nil {
		t.Fatal(err)
	}

	for run := int64(2); run <= 3; run++ {
		service.mu.Lock()
		job.Status = model.JobStatusRunning
		job.StartedAt = run * 10
		job.FinishedAt = 0
		running := job.DeepCopy()
		service.mu.Unlock()
		service.recordJobObservation(running, "")

		service.mu.Lock()
		job.Status = model.JobStatusCompleted
		job.FinishedAt = run*10 + 1
		job.LastRunOutcome = model.RunOutcomeCompleted
		completed := job.DeepCopy()
		service.mu.Unlock()
		service.recordJobObservation(completed, "")
	}

	response, err := service.ObserveJobs(baseline.Cursor, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Changes) != 4 {
		t.Fatalf("changes = %#v, want two complete run lifecycles", response.Changes)
	}
	for _, index := range []int{1, 3} {
		if response.Changes[index].RunOutcome != model.RunOutcomeCompleted {
			t.Fatalf("terminal change %d outcome = %q, want completed", index, response.Changes[index].RunOutcome)
		}
	}
	if response.Changes[1].EventID == response.Changes[3].EventID {
		t.Fatal("distinct runs reused a terminal event ID")
	}
}

func TestTerminalPersistenceFailureStillRecordsVolatileObservation(t *testing.T) {
	repo := newIdempotencyTestRepo()
	service := newStateTestService()
	service.repos["workspace"] = repo
	job := observationTestJob("volatile-terminal", model.JobStatusRunning, 1)
	job.Progress = &model.JobProgress{}
	service.jobs[job.ID] = job
	repo.seed(job)
	baseline, err := service.ObserveJobs("", 100)
	if err != nil {
		t.Fatal(err)
	}

	saveErr := errors.New("forced terminal persistence failure")
	// Two saveJobWithRetry calls, each with two attempts, all fail. The live
	// in-memory terminal state and SSE event are still published by contract.
	repo.failNext(4, saveErr)
	_ = captureTerminalEvent(t, service, job.ID, func() {
		service.finishJob(context.Background(), job)
	})

	response, err := service.ObserveJobs(baseline.Cursor, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Changes) != 1 {
		t.Fatalf("changes = %#v, want volatile terminal observation", response.Changes)
	}
	change := response.Changes[0]
	if change.Job.Status != model.JobStatusCompleted || change.RunOutcome != model.RunOutcomeCompleted {
		t.Fatalf("volatile terminal change = %#v, want completed outcome", change)
	}
	if persisted, ok := repo.saved(job.ID); !ok || persisted.Status != model.JobStatusRunning {
		t.Fatalf("durable state = %#v, want pre-terminal running state", persisted)
	}
}

func TestTerminalPersistenceFailureCannotAppendStaleTerminalAfterNextRun(t *testing.T) {
	repo := newIdempotencyTestRepo()
	service := newStateTestService()
	service.repos["workspace"] = repo
	job := observationTestJob("volatile-order", model.JobStatusRunning, 1)
	job.Progress = &model.JobProgress{}
	service.jobs[job.ID] = job
	repo.seed(job)
	baseline, err := service.ObserveJobs("", 100)
	if err != nil {
		t.Fatal(err)
	}
	repo.failNext(4, errors.New("forced terminal persistence failure"))
	service.finishJob(context.Background(), job)

	lock := service.persistLock(job.ID)
	lock.Lock()
	service.mu.Lock()
	job.Status = model.JobStatusRunning
	job.StartedAt = job.FinishedAt + 1
	job.FinishedAt = 0
	job.LastRunOutcome = ""
	nextRun := job.DeepCopy()
	service.mu.Unlock()
	service.recordJobObservation(nextRun, "")
	lock.Unlock()

	response, err := service.ObserveJobs(baseline.Cursor, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Changes) != 2 {
		t.Fatalf("changes = %#v, want terminal then next running", response.Changes)
	}
	if response.Changes[0].Job.Status != model.JobStatusCompleted || response.Changes[1].Job.Status != model.JobStatusRunning {
		t.Fatalf("ordered statuses = [%s %s], want completed then running", response.Changes[0].Job.Status, response.Changes[1].Job.Status)
	}
}

func TestObserveJobsKeepsRecordedEventsBeforeFirstPollAsBaseline(t *testing.T) {
	job := observationTestJob("quick", model.JobStatusPending, 1)
	service := newObservationTestService(job)
	service.recordJobObservation(job.DeepCopy(), "")

	service.mu.Lock()
	job.Status = model.JobStatusCompleted
	job.StartedAt = 2
	job.FinishedAt = 3
	job.UpdatedAt = time.UnixMilli(3)
	completed := job.DeepCopy()
	service.mu.Unlock()
	service.recordJobObservation(completed, "")

	baseline, err := service.ObserveJobs("", 100)
	if err != nil {
		t.Fatal(err)
	}
	if !baseline.Reset || len(baseline.Changes) != 0 {
		t.Fatalf("first poll must establish baseline without replay: %#v", baseline)
	}
	cursor, err := decodeJobObservationCursor(baseline.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	if cursor.Sequence != 2 {
		t.Fatalf("baseline sequence = %d, want 2 recorded mutations", cursor.Sequence)
	}
}

func TestObserveJobsPreservesExactGraphTransitionsAndSession(t *testing.T) {
	job := observationTestJob("graph", model.JobStatusPending, 1)
	job.Mode = model.JobModeGraph
	job.GraphRunID = "run-1"
	service := newObservationTestService(job)
	baseline, err := service.ObserveJobs("", 100)
	if err != nil {
		t.Fatal(err)
	}

	service.recordJobObservationWithGraphSession(job.DeepCopy(), string(model.GraphRunStatusAwaitingInput), "session-await")
	service.mu.Lock()
	job.Status = model.JobStatusRunning
	job.StartedAt = 2
	job.FinishedAt = 0
	running := job.DeepCopy()
	service.mu.Unlock()
	service.recordJobObservation(running, string(model.GraphRunStatusRunning))
	service.mu.Lock()
	job.Status = model.JobStatusCompleted
	job.FinishedAt = 3
	completed := job.DeepCopy()
	service.mu.Unlock()
	service.recordJobObservation(completed, string(model.GraphRunStatusCompleted))

	response, err := service.ObserveJobs(baseline.Cursor, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Changes) != 3 {
		t.Fatalf("Graph changes = %#v, want awaiting/running/completed", response.Changes)
	}
	if got := response.Changes[0]; got.GraphStatus != "awaitingInput" || got.GraphSessionID != "session-await" {
		t.Fatalf("awaiting change = %#v, want exact status and session", got)
	}
	if response.Changes[1].GraphStatus != "running" || response.Changes[2].GraphStatus != "completed" {
		t.Fatalf("Graph statuses = [%s %s %s]", response.Changes[0].GraphStatus, response.Changes[1].GraphStatus, response.Changes[2].GraphStatus)
	}
}

func TestSetGraphRunStatePreservesAwaitingInputSession(t *testing.T) {
	job := observationTestJob("graph-await", model.JobStatusRunning, 1)
	job.Mode = model.JobModeGraph
	job.GraphRunID = "run-await"
	service := newObservationTestService(job)
	service.newJobRepo = func(string) (repository.JobRepo, error) { return &stubJobRepo{}, nil }
	baseline, err := service.ObserveJobs("", 100)
	if err != nil {
		t.Fatal(err)
	}

	if err := service.SetGraphRunState(
		context.Background(),
		job.ID,
		job.GraphRunID,
		model.JobStatusStopped,
		model.GraphRunStatusAwaitingInput,
		1,
		2,
		"session-await",
	); err != nil {
		t.Fatal(err)
	}
	response, err := service.ObserveJobs(baseline.Cursor, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Changes) != 1 {
		t.Fatalf("changes = %#v, want one awaiting event", response.Changes)
	}
	change := response.Changes[0]
	if change.GraphStatus != "awaitingInput" || change.GraphSessionID != "session-await" {
		t.Fatalf("awaiting event = %#v, want exact status and deep-link session", change)
	}
}

func TestSetGraphRunStateRecordsExactTimedOutStatus(t *testing.T) {
	job := observationTestJob("graph", model.JobStatusRunning, 1)
	job.Mode = model.JobModeGraph
	job.GraphRunID = "run-1"
	service := newObservationTestService(job)
	service.newJobRepo = func(string) (repository.JobRepo, error) { return &stubJobRepo{}, nil }
	service.listVersions = newListVersionTracker()
	baseline, err := service.ObserveJobs("", 100)
	if err != nil {
		t.Fatal(err)
	}

	if err := service.SetGraphRunState(
		context.Background(),
		job.ID,
		job.GraphRunID,
		model.JobStatusFailed,
		model.GraphRunStatusTimedOut,
		1,
		2,
		"",
	); err != nil {
		t.Fatal(err)
	}
	response, err := service.ObserveJobs(baseline.Cursor, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.Changes) != 1 {
		t.Fatalf("changes = %#v, want one Graph terminal event", response.Changes)
	}
	change := response.Changes[0]
	if change.GraphStatus != string(model.GraphRunStatusTimedOut) {
		t.Fatalf("graph status = %q, want timedOut", change.GraphStatus)
	}
	if change.PreviousGraphState != "" {
		t.Fatalf("previous Graph status = %q, want empty baseline detail", change.PreviousGraphState)
	}
}

func TestSetGraphRunStatePersistenceFailureStillRecordsVolatileTerminal(t *testing.T) {
	repo := newIdempotencyTestRepo()
	job := observationTestJob("graph-volatile", model.JobStatusRunning, 1)
	job.Mode = model.JobModeGraph
	job.GraphRunID = "run-volatile"
	service := newObservationTestService(job)
	service.repos[job.WorkspaceID] = repo
	repo.seed(job)
	baseline, err := service.ObserveJobs("", 100)
	if err != nil {
		t.Fatal(err)
	}
	repo.failNext(1, errors.New("forced graph terminal save failure"))

	err = service.SetGraphRunState(
		context.Background(), job.ID, job.GraphRunID,
		model.JobStatusFailed, model.GraphRunStatusTimedOut, 1, 2, "",
	)
	if err == nil {
		t.Fatal("SetGraphRunState unexpectedly succeeded")
	}
	response, observeErr := service.ObserveJobs(baseline.Cursor, 100)
	if observeErr != nil {
		t.Fatal(observeErr)
	}
	if len(response.Changes) != 1 || response.Changes[0].GraphStatus != "timedOut" {
		t.Fatalf("volatile Graph observation = %#v, want exact timedOut event", response.Changes)
	}
}

func TestObserveJobsNeverReturnsShareToken(t *testing.T) {
	job := observationTestJob("secret", model.JobStatusRunning, 1)
	job.ShareToken = "must-not-leak"
	service := newObservationTestService(job)
	baseline, err := service.ObserveJobs("", 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(baseline.ActiveJobs) != 1 || baseline.ActiveJobs[0].ShareToken != "" {
		t.Fatalf("active observation leaked share token: %#v", baseline.ActiveJobs)
	}

	service.mu.Lock()
	job.Status = model.JobStatusCompleted
	job.FinishedAt = 2
	job.UpdatedAt = time.UnixMilli(2)
	completed := job.DeepCopy()
	service.mu.Unlock()
	service.recordJobObservation(completed, "")
	next, err := service.ObserveJobs(baseline.Cursor, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Changes) != 1 || next.Changes[0].Job.ShareToken != "" {
		t.Fatalf("change observation leaked share token: %#v", next.Changes)
	}
}

func TestObserveJobsLargeHistoryReturnsOnlyActiveSnapshot(t *testing.T) {
	service := newObservationTestService()
	for index := 0; index < 13_266; index++ {
		id := fmt.Sprintf("history-%05d", index)
		service.jobs[id] = observationTestJob(id, model.JobStatusCompleted, int64(index+1))
	}
	service.jobs["active-other-workspace"] = observationTestJob("active-other-workspace", model.JobStatusRunning, 20_000)
	service.jobs["active-other-workspace"].WorkspaceID = "workspace-other"

	response, err := service.ObserveJobs("", 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(response.ActiveJobs) != 1 || response.ActiveJobs[0].ID != "active-other-workspace" {
		t.Fatalf("active jobs = %#v, want only cross-workspace active job", response.ActiveJobs)
	}
	if len(response.Changes) != 0 || !response.Reset {
		t.Fatalf("large history baseline = %#v, want reset without historical events", response)
	}
}

func TestObserveJobsRejectsMalformedAndResetsForeignCursor(t *testing.T) {
	service := newObservationTestService(observationTestJob("active", model.JobStatusRunning, 1))
	if _, err := service.ObserveJobs("not-base64!", 100); err == nil {
		t.Fatal("malformed cursor unexpectedly accepted")
	}
	foreign := encodeJobObservationCursor(jobObservationCursor{Epoch: "other-process", Sequence: 1})
	response, err := service.ObserveJobs(foreign, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !response.Reset || len(response.Changes) != 0 || len(response.ActiveJobs) != 1 {
		t.Fatalf("foreign cursor response = %#v, want fresh active baseline", response)
	}
}

func TestObserveJobsResetsExpiredCursor(t *testing.T) {
	service := newObservationTestService()
	baseline, err := service.ObserveJobs("", 100)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := decodeJobObservationCursor(baseline.Cursor)
	if err != nil {
		t.Fatal(err)
	}
	service.observations.mu.Lock()
	service.observations.sequence = 2
	service.observations.events = []jobObservationEvent{{sequence: 2}}
	service.observations.mu.Unlock()

	response, err := service.ObserveJobs(encodeJobObservationCursor(decoded), 100)
	if err != nil {
		t.Fatal(err)
	}
	if !response.Reset || len(response.Changes) != 0 {
		t.Fatalf("expired cursor response = %#v, want reset baseline", response)
	}
}

func TestObserveJobsDoesNotEmitPresentationOnlyChanges(t *testing.T) {
	job := observationTestJob("job", model.JobStatusRunning, 1)
	service := newObservationTestService(job)
	baseline, err := service.ObserveJobs("", 100)
	if err != nil {
		t.Fatal(err)
	}

	service.mu.Lock()
	service.jobs[job.ID].Title = "renamed"
	service.jobs[job.ID].PinnedAt = 99
	service.jobs[job.ID].UpdatedAt = time.UnixMilli(99)
	service.mu.Unlock()

	next, err := service.ObserveJobs(baseline.Cursor, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(next.Changes) != 0 {
		t.Fatalf("presentation-only mutation emitted lifecycle changes: %#v", next.Changes)
	}
}

func newObservationTestService(jobs ...*model.Job) *serviceImpl {
	service := &serviceImpl{
		jobs:         make(map[string]*model.Job),
		repos:        make(map[string]repository.JobRepo),
		listVersions: newListVersionTracker(),
		notifiedJobs: make(map[string]struct{}),
	}
	for _, job := range jobs {
		service.jobs[job.ID] = job
	}
	return service
}

func observationTestJob(id string, status model.JobStatus, timestamp int64) *model.Job {
	return &model.Job{
		ID:          id,
		Title:       id,
		WorkspaceID: "workspace",
		Status:      status,
		Mode:        model.JobModeInteractive,
		CreatedAt:   time.UnixMilli(timestamp),
		UpdatedAt:   time.UnixMilli(timestamp),
	}
}
