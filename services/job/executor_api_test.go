package job

import (
	"context"
	"errors"
	"reflect"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/types/agui"
	"github.com/fanlv/quartet/types/consts"
	"github.com/fanlv/quartet/types/model"
)

type recordingJobRepo struct {
	mu       sync.Mutex
	saveErrs []error
	saved    []*model.Job
	loadAll  []*model.Job
}

func (r *recordingJobRepo) Save(jobID string, job *model.Job) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.saveErrs) > 0 {
		err := r.saveErrs[0]
		r.saveErrs = r.saveErrs[1:]
		if err != nil {
			return err
		}
	}
	r.saved = append(r.saved, job.DeepCopy())
	return nil
}

func (r *recordingJobRepo) Load(jobID string) (*model.Job, error) { return nil, nil }
func (r *recordingJobRepo) ListIDs() ([]string, error)            { return nil, nil }
func (r *recordingJobRepo) LoadAll() ([]*model.Job, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*model.Job, 0, len(r.loadAll))
	for _, j := range r.loadAll {
		out = append(out, j.DeepCopy())
	}
	return out, nil
}

func (r *recordingJobRepo) saveCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.saved)
}

func (r *recordingJobRepo) lastSaved() *model.Job {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.saved) == 0 {
		return nil
	}
	return r.saved[len(r.saved)-1].DeepCopy()
}

type testWorkspaceService struct {
	workspaces []*model.Workspace
}

func (s *testWorkspaceService) Create(ws *model.Workspace) error { return nil }
func (s *testWorkspaceService) Get(id string) (*model.Workspace, bool) {
	for _, ws := range s.workspaces {
		if ws.ID == id && !ws.Deleted {
			cp := *ws
			return &cp, true
		}
	}
	return nil, false
}
func (s *testWorkspaceService) List() []*model.Workspace {
	out := make([]*model.Workspace, 0, len(s.workspaces))
	for _, ws := range s.workspaces {
		if ws.Deleted {
			continue
		}
		cp := *ws
		out = append(out, &cp)
	}
	return out
}
func (s *testWorkspaceService) Update(id string, title, description, workdir string) (*model.Workspace, error) {
	return nil, nil
}
func (s *testWorkspaceService) SetSandboxRef(id string, ref *model.SandboxRef) error { return nil }
func (s *testWorkspaceService) Revision() uint64                                     { return 0 }
func (s *testWorkspaceService) TrustedFileWorkspaceRoots() []string                  { return nil }
func (s *testWorkspaceService) MarkDeleted(id string) error                          { return nil }
func (s *testWorkspaceService) Delete(id string) error                               { return nil }
func (s *testWorkspaceService) EnsureDefault() error                                 { return nil }
func (s *testWorkspaceService) RegenerateAllColors() ([]*model.Workspace, error)     { return nil, nil }
func (s *testWorkspaceService) DefaultWorkdir() string                               { return "" }

func newAPITestService(repo *recordingJobRepo) *serviceImpl {
	if repo == nil {
		repo = &recordingJobRepo{}
	}
	svc := newStateTestService()
	svc.repos[""] = repo
	svc.repos["ws"] = repo
	svc.repos["other"] = repo
	return svc
}

func testLoopConfig() *model.LoopConfig {
	return &model.LoopConfig{Flow: []model.FlowNode{{
		ID:          "step-1",
		Type:        model.FlowNodeTypeStep,
		Message:     "hello",
		RepeatCount: 1,
		RoundMode:   model.RoundModeNone,
		RoundType:   model.RoundTypePrompt,
	}}}
}

func testJob(id string, status model.JobStatus) *model.Job {
	return &model.Job{
		ID:          id,
		Title:       "title-" + id,
		CreatedAt:   time.Unix(100, 0),
		UpdatedAt:   time.Unix(100, 0),
		Status:      status,
		Mode:        model.JobModeLoop,
		WorkspaceID: "ws",
		Workdir:     "/tmp/ws",
		LoopConfig:  testLoopConfig(),
		Progress:    &model.JobProgress{TotalSteps: 1},
	}
}

func storeTestJob(svc *serviceImpl, job *model.Job) {
	svc.mu.Lock()
	svc.jobs[job.ID] = job
	svc.mu.Unlock()
}

func waitForJobStatus(t *testing.T, svc *serviceImpl, jobID string, want model.JobStatus) *model.Job {
	t.Helper()
	deadline := time.After(2 * time.Second)
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for {
		got, ok := svc.Get(jobID)
		if !ok {
			t.Fatalf("job %s not found", jobID)
		}
		if got.Status == want {
			return got
		}
		select {
		case <-deadline:
			t.Fatalf("job %s status=%s, want %s", jobID, got.Status, want)
		case <-tick.C:
		}
	}
}

type blockingRunner struct{}

func (r blockingRunner) InitSession(ctx context.Context, jobID string, overrides *model.SessionOverrides) (string, error) {
	return "blocking-session", nil
}

func (r blockingRunner) RunIteration(ctx context.Context, sessionID string, messages []*schema.Message, handler agui.EventHandler) error {
	<-ctx.Done()
	return ctx.Err()
}

func (r blockingRunner) SessionModelID(sessionID string) string { return "" }

type failingRunner struct{ err error }

func (r failingRunner) InitSession(ctx context.Context, jobID string, overrides *model.SessionOverrides) (string, error) {
	return "failing-session", nil
}

func (r failingRunner) RunIteration(ctx context.Context, sessionID string, messages []*schema.Message, handler agui.EventHandler) error {
	return r.err
}

func (r failingRunner) SessionModelID(sessionID string) string { return "" }

func assertJobRunState(t *testing.T, got *model.Job, want jobRunStateSnapshot) {
	t.Helper()
	if got.Status != want.Status || got.StartedAt != want.StartedAt || got.FinishedAt != want.FinishedAt {
		t.Fatalf("run timestamps/status not restored: got status=%s started=%d finished=%d; want status=%s started=%d finished=%d",
			got.Status, got.StartedAt, got.FinishedAt, want.Status, want.StartedAt, want.FinishedAt)
	}
	if !reflect.DeepEqual(got.SessionIDs, want.SessionIDs) {
		t.Fatalf("sessionIDs not restored: got=%v want=%v", got.SessionIDs, want.SessionIDs)
	}
	if !reflect.DeepEqual(got.Progress, want.Progress) {
		t.Fatalf("progress not restored: got=%+v want=%+v", got.Progress, want.Progress)
	}
	if !reflect.DeepEqual(got.Resume, want.Resume) {
		t.Fatalf("resume not restored: got=%+v want=%+v", got.Resume, want.Resume)
	}
	if !reflect.DeepEqual(got.LoopConfig, want.LoopConfig) {
		t.Fatalf("loopConfig not restored: got=%+v want=%+v", got.LoopConfig, want.LoopConfig)
	}
}

func TestLifecycleAPIPreconditionErrors(t *testing.T) {
	ctx := context.Background()
	svc := newAPITestService(nil)

	if err := svc.Start(ctx, "missing", nil); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("Start missing err=%v, want %v", err, ErrJobNotFound)
	}
	deleted := testJob("deleted", model.JobStatusPending)
	deleted.Deleted = true
	storeTestJob(svc, deleted)
	if err := svc.Start(ctx, deleted.ID, nil); !errors.Is(err, ErrJobDeleted) {
		t.Fatalf("Start deleted err=%v, want %v", err, ErrJobDeleted)
	}
	noLoop := testJob("no-loop", model.JobStatusPending)
	noLoop.LoopConfig = nil
	storeTestJob(svc, noLoop)
	if err := svc.Start(ctx, noLoop.ID, nil); !errors.Is(err, ErrNoLoopConfig) {
		t.Fatalf("Start no loop err=%v, want %v", err, ErrNoLoopConfig)
	}
	running := testJob("running", model.JobStatusRunning)
	storeTestJob(svc, running)
	if err := svc.Start(ctx, running.ID, nil); !errors.Is(err, ErrJobRunning) {
		t.Fatalf("Start running err=%v, want %v", err, ErrJobRunning)
	}

	completed := testJob("completed", model.JobStatusCompleted)
	storeTestJob(svc, completed)
	if err := svc.Continue(ctx, completed.ID, nil); !errors.Is(err, ErrJobNotRunnable) {
		t.Fatalf("Continue completed err=%v, want %v", err, ErrJobNotRunnable)
	}
	stoppedDone := testJob("stopped-done", model.JobStatusStopped)
	stoppedDone.Progress = &model.JobProgress{TotalSteps: 1, CompletedCount: 1, CurrentPath: []int{0, 0}}
	storeTestJob(svc, stoppedDone)
	if err := svc.Continue(ctx, stoppedDone.ID, nil); !errors.Is(err, ErrNoResumable) {
		t.Fatalf("Continue non-resumable err=%v, want %v", err, ErrNoResumable)
	}

	if err := svc.SendMessage(ctx, "missing", nil, nil); !errors.Is(err, ErrEmptyMessage) {
		t.Fatalf("SendMessage empty err=%v, want %v", err, ErrEmptyMessage)
	}
	msgOpts := &SendMessageOptions{Messages: []*schema.Message{schema.UserMessage("hi")}}
	if err := svc.SendMessage(ctx, "missing", nil, msgOpts); !errors.Is(err, ErrJobNotFound) {
		t.Fatalf("SendMessage missing err=%v, want %v", err, ErrJobNotFound)
	}
	if err := svc.SendMessage(ctx, deleted.ID, nil, msgOpts); !errors.Is(err, ErrJobDeleted) {
		t.Fatalf("SendMessage deleted err=%v, want %v", err, ErrJobDeleted)
	}
	if err := svc.SendMessage(ctx, running.ID, nil, msgOpts); !errors.Is(err, ErrJobRunning) {
		t.Fatalf("SendMessage running err=%v, want %v", err, ErrJobRunning)
	}
}

func TestStartLoopRetriesTransientFailureThroughServiceLifecycle(t *testing.T) {
	ctx := context.Background()
	svc := newAPITestService(nil)
	withFastRetryDelays(t, svc)
	job := testJob("service-retry", model.JobStatusPending)
	job.LoopConfig.Flow[0].Message = "hello retry"
	storeTestJob(svc, job)

	reader, err := svc.Subscribe(job.ID, 0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer reader.Close()

	runner := &retrySequenceRunner{errs: []error{errors.New("stream error: INTERNAL_ERROR"), nil}}
	if err := svc.Start(ctx, job.ID, runner); err != nil {
		t.Fatalf("Start: %v", err)
	}
	got := waitForJobStatus(t, svc, job.ID, model.JobStatusCompleted)
	if runner.calls != 2 {
		t.Fatalf("RunIteration calls=%d, want 2", runner.calls)
	}
	if got.Progress.CompletedCount != 1 || got.Progress.FailedCount != 0 {
		t.Fatalf("progress completed=%d failed=%d, want 1/0", got.Progress.CompletedCount, got.Progress.FailedCount)
	}
	if got.Resume != nil {
		t.Fatalf("resume=%+v, want nil after completed run", got.Resume)
	}

	readCtx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for {
		entries, ok := reader.Read(readCtx, 32)
		if !ok {
			t.Fatal("timeout waiting for JOB_COMPLETED")
		}
		for _, entry := range entries {
			if _, ok := entry.Event.(*model.JobCompletedEvent); ok {
				return
			}
			if entry.Seq > 0 {
				reader.Ack(entry.Seq)
			}
		}
	}
}

func TestLifecycleAPIRollsBackRunStateOnPersistFailure(t *testing.T) {
	ctx := context.Background()
	saveBoom := errors.New("save boom")

	t.Run("Start", func(t *testing.T) {
		repo := &recordingJobRepo{saveErrs: []error{saveBoom, saveBoom}}
		svc := newAPITestService(repo)
		job := testJob("start-rollback", model.JobStatusCompleted)
		job.StartedAt = 111
		job.FinishedAt = 222
		job.SessionIDs = []string{"old-session"}
		job.Progress = &model.JobProgress{TotalSteps: 1, CompletedCount: 1, CurrentPath: []int{0, 0}, Results: []model.IterationResult{{Path: []int{0, 0}, Success: true}}}
		job.Resume = &model.JobResume{NextPath: []int{0, 0}, SessionID: "old-session"}
		want := snapshotRunStateLocked(job)
		storeTestJob(svc, job)

		if err := svc.Start(ctx, job.ID, nil); !errors.Is(err, saveBoom) {
			t.Fatalf("Start err=%v, want %v", err, saveBoom)
		}
		got, _ := svc.Get(job.ID)
		assertJobRunState(t, got, want)
		if repo.saveCount() != 0 {
			t.Fatalf("failed saves should not be recorded as committed saves, got %d", repo.saveCount())
		}
		if len(svc.cancels) != 0 || len(svc.dones) != 0 {
			t.Fatalf("Start should not launch loop after persist failure")
		}
	})

	t.Run("Continue", func(t *testing.T) {
		repo := &recordingJobRepo{saveErrs: []error{saveBoom, saveBoom}}
		svc := newAPITestService(repo)
		job := testJob("continue-rollback", model.JobStatusStopped)
		job.StartedAt = 111
		job.FinishedAt = 222
		job.SessionIDs = []string{"resume-session"}
		job.Progress = &model.JobProgress{TotalSteps: 1, CompletedCount: 1, CurrentPath: []int{0, 0}, Results: []model.IterationResult{{Path: []int{0, 0}, Success: true}}}
		job.Resume = &model.JobResume{NextPath: []int{0, 0}, SessionID: "resume-session"}
		want := snapshotRunStateLocked(job)
		storeTestJob(svc, job)

		if err := svc.Continue(ctx, job.ID, nil); !errors.Is(err, saveBoom) {
			t.Fatalf("Continue err=%v, want %v", err, saveBoom)
		}
		got, _ := svc.Get(job.ID)
		assertJobRunState(t, got, want)
	})

	t.Run("SendMessage", func(t *testing.T) {
		repo := &recordingJobRepo{saveErrs: []error{saveBoom, saveBoom}}
		svc := newAPITestService(repo)
		job := testJob("send-rollback", model.JobStatusFailed)
		job.StartedAt = 111
		job.FinishedAt = 222
		job.SessionIDs = []string{"chat-session"}
		job.Progress = &model.JobProgress{TotalSteps: 1, FailedCount: 1, CurrentPath: []int{0, 0}, Results: []model.IterationResult{{Path: []int{0, 0}, Success: false, Error: "old"}}}
		job.Resume = &model.JobResume{NextPath: []int{0, 0}, SessionID: "chat-session"}
		want := snapshotRunStateLocked(job)
		storeTestJob(svc, job)

		opts := &SendMessageOptions{Messages: []*schema.Message{schema.UserMessage("hi")}}
		if err := svc.SendMessage(ctx, job.ID, nil, opts); !errors.Is(err, saveBoom) {
			t.Fatalf("SendMessage err=%v, want %v", err, saveBoom)
		}
		got, _ := svc.Get(job.ID)
		assertJobRunState(t, got, want)
		if len(svc.cancels) != 0 || len(svc.dones) != 0 {
			t.Fatalf("SendMessage should not launch interactive run after persist failure")
		}
	})
}

func TestSendMessageRestoresInteractivePriorStatus(t *testing.T) {
	ctx := context.Background()
	msgOpts := &SendMessageOptions{
		SessionID: "existing-session",
		Messages:  []*schema.Message{schema.UserMessage("hi")},
	}

	tests := []struct {
		name       string
		initial    model.JobStatus
		resume     *model.JobResume
		runner     JobRunner
		wantStatus model.JobStatus
		wantResume *model.JobResume
	}{
		{
			name:       "completed stays completed after successful interactive send",
			initial:    model.JobStatusCompleted,
			runner:     stubRunner{},
			wantStatus: model.JobStatusCompleted,
		},
		{
			name:       "failed stays failed after failed interactive send",
			initial:    model.JobStatusFailed,
			resume:     &model.JobResume{NextPath: []int{0, 0}, SessionID: "resume-session"},
			runner:     failingRunner{err: errors.New("interactive failed")},
			wantStatus: model.JobStatusFailed,
			wantResume: &model.JobResume{NextPath: []int{0, 0}, SessionID: "resume-session"},
		},
		{
			name:       "stopped resumable loop stays stopped and keeps resume",
			initial:    model.JobStatusStopped,
			resume:     &model.JobResume{NextPath: []int{0, 0}, SessionID: "resume-session"},
			runner:     stubRunner{},
			wantStatus: model.JobStatusStopped,
			wantResume: &model.JobResume{NextPath: []int{0, 0}, SessionID: "resume-session"},
		},
		{
			name:       "pending finishes as completed",
			initial:    model.JobStatusPending,
			runner:     stubRunner{},
			wantStatus: model.JobStatusCompleted,
		},
		{
			name:       "pending failure becomes failed",
			initial:    model.JobStatusPending,
			runner:     failingRunner{err: errors.New("interactive failed")},
			wantStatus: model.JobStatusFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newAPITestService(nil)
			job := testJob("job-"+tt.name, tt.initial)
			job.Resume = copyResume(tt.resume)
			storeTestJob(svc, job)

			if err := svc.SendMessage(ctx, job.ID, tt.runner, msgOpts); err != nil {
				t.Fatalf("SendMessage failed: %v", err)
			}

			got := waitForJobStatus(t, svc, job.ID, tt.wantStatus)
			if !reflect.DeepEqual(got.Resume, tt.wantResume) {
				t.Fatalf("resume=%+v, want %+v", got.Resume, tt.wantResume)
			}
		})
	}
}

func TestSendMessagePendingStopBecomesStopped(t *testing.T) {
	ctx := context.Background()
	svc := newAPITestService(nil)
	job := testJob("job-pending-stop", model.JobStatusPending)
	storeTestJob(svc, job)

	opts := &SendMessageOptions{Messages: []*schema.Message{schema.UserMessage("hi")}}
	if err := svc.SendMessage(ctx, job.ID, blockingRunner{}, opts); err != nil {
		t.Fatalf("SendMessage failed: %v", err)
	}
	waitForJobStatus(t, svc, job.ID, model.JobStatusRunning)

	svc.StopAndWait(job.ID)
	got := waitForJobStatus(t, svc, job.ID, model.JobStatusStopped)
	if got.Resume != nil {
		t.Fatalf("pending interactive stop should not create resume, got %+v", got.Resume)
	}
}

func TestCRUDListAndPagedListAPIs(t *testing.T) {
	repo := &recordingJobRepo{}
	svc := newAPITestService(repo)

	newJob := testJob("new", model.JobStatusPending)
	newJob.UpdatedAt = time.Unix(300, 0)
	oldJob := testJob("old", model.JobStatusCompleted)
	oldJob.UpdatedAt = time.Unix(100, 0)
	scheduled := testJob("scheduled", model.JobStatusCompleted)
	scheduled.UpdatedAt = time.Unix(200, 0)
	scheduled.ScheduleID = "schedule-1"
	other := testJob("other", model.JobStatusPending)
	other.WorkspaceID = "other"
	deleted := testJob("deleted", model.JobStatusPending)
	deleted.Deleted = true
	orphan := testJob("orphan", model.JobStatusPending)
	orphan.WorkspaceID = ""

	for _, j := range []*model.Job{newJob, oldJob, scheduled, other, deleted, orphan} {
		if err := svc.Create(j); err != nil {
			t.Fatalf("Create(%s) failed: %v", j.ID, err)
		}
	}
	if repo.saveCount() != 6 {
		t.Fatalf("Create should persist each job, got saveCount=%d", repo.saveCount())
	}

	got, ok := svc.Get("new")
	if !ok {
		t.Fatalf("Get(new) not found")
	}
	got.Title = "mutated copy"
	got.Progress.CompletedCount = 99
	again, _ := svc.Get("new")
	if again.Title == "mutated copy" || again.Progress.CompletedCount == 99 {
		t.Fatalf("Get must return a deep copy, got title=%q progress=%+v", again.Title, again.Progress)
	}

	if got := len(svc.List()); got != 5 {
		t.Fatalf("List should exclude deleted jobs only, got %d", got)
	}
	if got := len(svc.ListByWorkspace("ws")); got != 3 {
		t.Fatalf("ListByWorkspace(ws) should include non-deleted ws jobs, got %d", got)
	}

	page1, cursor, hasMore, version := svc.ListByWorkspacePaged("ws", "", 1, true)
	if version == 0 {
		t.Fatalf("workspace list version should be bumped by Create")
	}
	if len(page1) != 1 || page1[0].ID != "new" || !hasMore || cursor == "" {
		t.Fatalf("page1 mismatch: page=%+v cursor=%q hasMore=%v", page1, cursor, hasMore)
	}
	page2, cursor2, hasMore2, _ := svc.ListByWorkspacePaged("ws", cursor, 10, true)
	if len(page2) != 1 || page2[0].ID != "old" || hasMore2 || cursor2 != "" {
		t.Fatalf("page2 mismatch: page=%+v cursor=%q hasMore=%v", page2, cursor2, hasMore2)
	}
}

func TestTargetedMutatorAPIsPersistAndRollbackExternalState(t *testing.T) {
	repo := &recordingJobRepo{}
	svc := newAPITestService(repo)
	job := testJob("mutators", model.JobStatusPending)
	job.LoopConfig.Variables = map[string]string{"keep": "value"}
	storeTestJob(svc, job)

	if err := svc.UpdateTitle(job.ID, "renamed"); err != nil {
		t.Fatalf("UpdateTitle failed: %v", err)
	}
	got, _ := svc.Get(job.ID)
	if got.Title != "renamed" || got.LoopConfig.Variables[consts.VarJobTitle] != "renamed" || got.LoopConfig.Variables["keep"] != "value" {
		t.Fatalf("UpdateTitle did not update title variables correctly: %+v", got)
	}

	generated := 0
	tok, err := svc.EnsureShareToken(job.ID, func() (string, error) {
		generated++
		return "token-1", nil
	})
	if err != nil || tok != "token-1" || generated != 1 {
		t.Fatalf("EnsureShareToken first call tok=%q generated=%d err=%v", tok, generated, err)
	}
	tok, err = svc.EnsureShareToken(job.ID, func() (string, error) {
		generated++
		return "token-2", nil
	})
	if err != nil || tok != "token-1" || generated != 1 {
		t.Fatalf("EnsureShareToken second call tok=%q generated=%d err=%v", tok, generated, err)
	}
	if err := svc.ClearShareToken(job.ID); err != nil {
		t.Fatalf("ClearShareToken failed: %v", err)
	}
	got, _ = svc.Get(job.ID)
	if got.ShareToken != "" {
		t.Fatalf("ClearShareToken left token=%q", got.ShareToken)
	}

	if err := svc.SetFirstModelID(job.ID, "model-1"); err != nil {
		t.Fatalf("SetFirstModelID failed: %v", err)
	}
	got, _ = svc.Get(job.ID)
	if got.FirstModelID != "model-1" {
		t.Fatalf("FirstModelID=%q, want model-1", got.FirstModelID)
	}

	if err := svc.MarkDeleted(job.ID); err != nil {
		t.Fatalf("MarkDeleted failed: %v", err)
	}
	got, _ = svc.Get(job.ID)
	if !got.Deleted {
		t.Fatalf("MarkDeleted did not set Deleted")
	}

	saveBoom := errors.New("save boom")
	failingRepo := &recordingJobRepo{saveErrs: []error{saveBoom, saveBoom}}
	failingSvc := newAPITestService(failingRepo)
	failingJob := testJob("share-rollback", model.JobStatusPending)
	storeTestJob(failingSvc, failingJob)
	if _, err := failingSvc.EnsureShareToken(failingJob.ID, func() (string, error) { return "lost-token", nil }); !errors.Is(err, saveBoom) {
		t.Fatalf("EnsureShareToken failure err=%v, want %v", err, saveBoom)
	}
	got, _ = failingSvc.Get(failingJob.ID)
	if got.ShareToken != "" {
		t.Fatalf("EnsureShareToken should roll back failed token, got %q", got.ShareToken)
	}

	failingJob.ShareToken = "kept-token"
	failingRepo.saveErrs = []error{saveBoom}
	if err := failingSvc.ClearShareToken(failingJob.ID); !errors.Is(err, saveBoom) {
		t.Fatalf("ClearShareToken failure err=%v, want %v", err, saveBoom)
	}
	got, _ = failingSvc.Get(failingJob.ID)
	if got.ShareToken != "kept-token" {
		t.Fatalf("ClearShareToken should restore failed token, got %q", got.ShareToken)
	}
}

func TestLoadResetsRunningJobsAndInitializesLoadedState(t *testing.T) {
	repo := &recordingJobRepo{loadAll: []*model.Job{
		{
			ID:          "running-on-disk",
			Title:       "running",
			Status:      model.JobStatusRunning,
			WorkspaceID: "ws",
			Progress:    nil,
		},
		{
			ID:          "with-content",
			Title:       "content",
			Status:      model.JobStatusCompleted,
			WorkspaceID: "ws",
			Progress: &model.JobProgress{Results: []model.IterationResult{
				{Path: []int{0, 0}, Content: "older content"},
				{Path: []int{0, 1}, Content: "middle content"},
				{Path: []int{0, 2}, Content: "latest content"},
			}},
		},
		{
			ID:          "deleted-on-disk",
			Deleted:     true,
			WorkspaceID: "ws",
		},
	}}
	svc := newAPITestService(repo)
	svc.wsSvc = &testWorkspaceService{workspaces: []*model.Workspace{{ID: "ws", Workdir: "/tmp/ws"}}}

	svc.load()

	running, ok := svc.Get("running-on-disk")
	if !ok {
		t.Fatalf("running job not loaded")
	}
	if running.Status != model.JobStatusFailed {
		t.Fatalf("running job status=%s, want failed", running.Status)
	}
	if running.Progress == nil {
		t.Fatalf("load should initialize nil Progress")
	}
	if repo.saveCount() != 1 || repo.lastSaved().Status != model.JobStatusFailed {
		t.Fatalf("load should persist running->failed reset, saveCount=%d last=%+v", repo.saveCount(), repo.lastSaved())
	}

	loaded, ok := svc.Get("with-content")
	if !ok {
		t.Fatalf("content job not loaded")
	}
	// Older results should have their Content cleared to free memory.
	if got := loaded.Progress.Results[0].Content; got != "" {
		t.Fatalf("load should clear older result content, got %q", got)
	}
	if got := loaded.Progress.Results[1].Content; got != "" {
		t.Fatalf("load should clear middle result content, got %q", got)
	}
	// The LAST result's Content must be preserved so injectPerRoundVars
	// can populate _last_assistant_msg after a process restart + Continue.
	// Clearing it would resolve the variable to "" AND the next save
	// would overwrite disk with the empty value (permanent data loss).
	if got := loaded.Progress.Results[2].Content; got != "latest content" {
		t.Fatalf("load should preserve last result content, got %q", got)
	}
	if _, ok := svc.Get("deleted-on-disk"); ok {
		t.Fatalf("deleted jobs should be skipped on load")
	}
}
