package job

import (
	"context"
	"errors"
	"fmt"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fanlv/quartet/pkg/fileserver"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/services/workspace"
	"github.com/fanlv/quartet/types/model"
	typepath "github.com/fanlv/quartet/types/path"
)

type deleteBlockingSaveJobRepo struct {
	repository.JobRepo
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (r *deleteBlockingSaveJobRepo) Save(id string, job *model.Job) error {
	r.once.Do(func() { close(r.entered) })
	<-r.release
	return r.JobRepo.Save(id, job)
}

type failDeleteFileManager struct {
	fileserver.FileManager
	fail atomic.Bool
}

var errForcedJobDelete = errors.New("forced file delete failure")

func (m *failDeleteFileManager) FileDelete(req *fsmodel.FileDeleteRequest) error {
	if m.fail.Load() {
		return errForcedJobDelete
	}
	return m.FileManager.FileDelete(req)
}

func newDeleteTestService(t *testing.T, jobID string) (*serviceImpl, *model.Job, string) {
	t.Helper()
	t.Setenv("LOCAL_MEMORY", t.TempDir())
	wsSvc, err := workspace.NewService()
	if err != nil {
		t.Fatalf("create workspace service: %v", err)
	}
	ws := model.NewWorkspace("delete-test", "", t.TempDir())
	ws.ID = "ws-" + jobID
	if err := wsSvc.Create(ws); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	api, err := NewService(wsSvc)
	if err != nil {
		t.Fatalf("create job service: %v", err)
	}
	svc := api.(*serviceImpl)
	job := model.NewJob(ws.Workdir, ws.ID)
	job.ID = jobID
	if err := svc.Create(job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	return svc, job, typepath.LocalJobDirInWorkspace(ws.ID, job.ID)
}

func TestDeleteWaitsForTargetedWriterAndDoesNotResurrectJob(t *testing.T) {
	svc, job, jobDir := newDeleteTestService(t, "job-delete-writer-fence")

	svc.repoMu.Lock()
	base := svc.repos[job.WorkspaceID]
	blocker := &deleteBlockingSaveJobRepo{
		JobRepo: base,
		entered: make(chan struct{}),
		release: make(chan struct{}),
	}
	svc.repos[job.WorkspaceID] = blocker
	svc.repoMu.Unlock()

	updateDone := make(chan error, 1)
	go func() { updateDone <- svc.UpdateTitle(job.ID, "committed before deletion") }()
	select {
	case <-blocker.entered:
	case <-time.After(time.Second):
		t.Fatal("title update did not reach blocked Save")
	}

	deleteDone := make(chan error, 1)
	go func() { deleteDone <- svc.Delete(job.ID) }()
	select {
	case err := <-deleteDone:
		t.Fatalf("Delete returned before the older writer left the persist shard: %v", err)
	case <-time.After(50 * time.Millisecond):
	}

	close(blocker.release)
	if err := <-updateDone; err != nil {
		t.Fatalf("UpdateTitle: %v", err)
	}
	if err := <-deleteDone; err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(jobDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("job dir exists after writer/Delete serialization: %v", err)
	}
	if _, ok := svc.Get(job.ID); ok {
		t.Fatal("deleted job remains in memory")
	}
}

func TestDeleteSerializesOtherJobPersistenceWriters(t *testing.T) {
	tests := []struct {
		name  string
		setup func(*testing.T, *serviceImpl, *model.Job)
		write func(*serviceImpl, *model.Job) error
	}{
		{
			name: "create same id",
			write: func(s *serviceImpl, j *model.Job) error {
				replacement := model.NewJob(j.Workdir, j.WorkspaceID)
				replacement.ID = j.ID
				return s.Create(replacement)
			},
		},
		{
			name:  "title generation error",
			write: func(s *serviceImpl, j *model.Job) error { return s.UpdateTitleGenerationError(j.ID, "late") },
		},
		{
			name:  "pin",
			write: func(s *serviceImpl, j *model.Job) error { _, err := s.UpdatePinned(j.ID, true); return err },
		},
		{
			name:  "first model",
			write: func(s *serviceImpl, j *model.Job) error { return s.SetFirstModelID(j.ID, "model-late") },
		},
		{
			name: "share token create",
			write: func(s *serviceImpl, j *model.Job) error {
				_, err := s.EnsureShareToken(j.ID, func() (string, error) { return "token-late", nil })
				return err
			},
		},
		{
			name: "share token clear",
			setup: func(t *testing.T, s *serviceImpl, j *model.Job) {
				t.Helper()
				if _, err := s.EnsureShareToken(j.ID, func() (string, error) { return "token-seed", nil }); err != nil {
					t.Fatalf("seed share token: %v", err)
				}
			},
			write: func(s *serviceImpl, j *model.Job) error { return s.ClearShareToken(j.ID) },
		},
		{
			name: "graph session",
			write: func(s *serviceImpl, j *model.Job) error {
				return s.AttachGraphSession(context.Background(), j.ID, "session-late")
			},
		},
		{
			name: "graph state",
			write: func(s *serviceImpl, j *model.Job) error {
				return s.SetGraphRunState(context.Background(), j.ID, "run-late", model.JobStatusRunning, model.GraphRunStatusRunning, 10, 0, "")
			},
		},
		{
			name: "graph linkage clear",
			setup: func(t *testing.T, s *serviceImpl, j *model.Job) {
				t.Helper()
				if err := s.SetGraphRunState(context.Background(), j.ID, "run-seed", model.JobStatusRunning, model.GraphRunStatusRunning, 10, 0, ""); err != nil {
					t.Fatalf("seed graph linkage: %v", err)
				}
			},
			write: func(s *serviceImpl, j *model.Job) error {
				return s.ClearGraphRunLinkage(context.Background(), j.ID, "run-seed")
			},
		},
		{
			name: "command receipt",
			write: func(s *serviceImpl, j *model.Job) error {
				_, _, err := s.ExecuteCommand(context.Background(), j.ID, "message-late", "/help", func() *model.CommandSystemMessageEvent {
					return &model.CommandSystemMessageEvent{}
				})
				return err
			},
		},
		{
			name: "full job save",
			write: func(s *serviceImpl, j *model.Job) error {
				s.mu.RLock()
				live := s.jobs[j.ID]
				s.mu.RUnlock()
				return s.saveJobWithRetry(context.Background(), live, "delete-fence-test")
			},
		},
	}

	for index, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc, job, jobDir := newDeleteTestService(t, fmt.Sprintf("job-writer-fence-%d", index))
			if tt.setup != nil {
				tt.setup(t, svc, job)
			}
			svc.repoMu.Lock()
			blocker := &deleteBlockingSaveJobRepo{
				JobRepo: svc.repos[job.WorkspaceID],
				entered: make(chan struct{}),
				release: make(chan struct{}),
			}
			svc.repos[job.WorkspaceID] = blocker
			svc.repoMu.Unlock()

			writeDone := make(chan error, 1)
			go func() { writeDone <- tt.write(svc, job) }()
			select {
			case <-blocker.entered:
			case err := <-writeDone:
				t.Fatalf("writer returned before Save: %v", err)
			case <-time.After(time.Second):
				t.Fatal("writer did not reach blocked Save")
			}
			deleteDone := make(chan error, 1)
			go func() { deleteDone <- svc.Delete(job.ID) }()
			select {
			case err := <-deleteDone:
				t.Fatalf("Delete overtook writer: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			close(blocker.release)
			if err := <-writeDone; err != nil {
				t.Fatalf("writer: %v", err)
			}
			if err := <-deleteDone; err != nil {
				t.Fatalf("Delete: %v", err)
			}
			if _, err := os.Stat(jobDir); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("job dir exists after serialized Delete: %v", err)
			}
		})
	}
}

func TestDeletedJobRejectsTargetedMutatorsButMarkDeletedIsIdempotent(t *testing.T) {
	svc, job, _ := newDeleteTestService(t, "job-deleted-mutators")
	if _, err := svc.EnsureShareToken(job.ID, func() (string, error) { return "share-token", nil }); err != nil {
		t.Fatalf("seed share token: %v", err)
	}
	job.Mode = model.JobModeGraph
	if err := svc.SetGraphRunState(context.Background(), job.ID, "run-1", model.JobStatusRunning, model.GraphRunStatusRunning, 1, 0, ""); err != nil {
		t.Fatalf("bind graph run: %v", err)
	}
	if err := svc.MarkDeleted(job.ID); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}
	if err := svc.MarkDeleted(job.ID); err != nil {
		t.Fatalf("idempotent MarkDeleted: %v", err)
	}

	checks := []struct {
		name string
		run  func() error
	}{
		{"UpdateTitle", func() error { return svc.UpdateTitle(job.ID, "late") }},
		{"UpdateTitleGenerationError", func() error { return svc.UpdateTitleGenerationError(job.ID, "late") }},
		{"UpdatePinned", func() error { _, err := svc.UpdatePinned(job.ID, true); return err }},
		{"SetFirstModelID", func() error { return svc.SetFirstModelID(job.ID, "model") }},
		{"EnsureShareToken", func() error {
			_, err := svc.EnsureShareToken(job.ID, func() (string, error) { return "new", nil })
			return err
		}},
		{"ClearShareToken", func() error { return svc.ClearShareToken(job.ID) }},
		{"ClearGraphRunLinkage", func() error { return svc.ClearGraphRunLinkage(context.Background(), job.ID, "run-1") }},
	}
	for _, check := range checks {
		t.Run(check.name, func(t *testing.T) {
			if err := check.run(); !errors.Is(err, ErrJobDeleted) {
				t.Fatalf("error = %v, want ErrJobDeleted", err)
			}
		})
	}
}

func TestAttachGraphSessionAfterTombstoneIsMemoryOnlyForCleanup(t *testing.T) {
	svc, job, jobDir := newDeleteTestService(t, "job-late-graph-session")
	if err := svc.MarkDeleted(job.ID); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}
	metaPath := typepath.JobMetaFilePath(jobDir)
	before, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read tombstone: %v", err)
	}

	if err := svc.AttachGraphSession(context.Background(), job.ID, "late-session"); err != nil {
		t.Fatalf("AttachGraphSession after tombstone: %v", err)
	}
	after, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read tombstone after late attach: %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("late cleanup registration rewrote the durable tombstone")
	}
	got, ok := svc.Get(job.ID)
	if !ok || len(got.GraphSessionIDs) != 1 || got.GraphSessionIDs[0] != "late-session" {
		t.Fatalf("in-memory graph sessions = %#v, want late-session", got)
	}
	if err := svc.Delete(job.ID); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, ok := svc.Get(job.ID); ok {
		t.Fatal("Get found job after successful Delete")
	}
}

func TestDeletedBoundGraphTerminalNotifiesExactlyOnceWithoutPersisting(t *testing.T) {
	svc, job, jobDir := newDeleteTestService(t, "job-deleted-graph-done")
	if err := svc.SetGraphRunState(context.Background(), job.ID, "run-1", model.JobStatusRunning, model.GraphRunStatusRunning, 10, 0, ""); err != nil {
		t.Fatalf("bind graph run: %v", err)
	}
	if err := svc.MarkDeleted(job.ID); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}
	metaPath := typepath.JobMetaFilePath(jobDir)
	before, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read tombstone: %v", err)
	}
	var calls atomic.Int32
	svc.SetOnJobDone(func(done *model.Job) {
		if done.ID != job.ID || done.Status != model.JobStatusStopped || !done.Deleted {
			t.Errorf("done snapshot = %#v", done)
		}
		calls.Add(1)
	})

	for i := 0; i < 2; i++ {
		if err := svc.SetGraphRunState(context.Background(), job.ID, "run-1", model.JobStatusStopped, model.GraphRunStatusStopped, 10, 20, ""); err != nil {
			t.Fatalf("terminal update %d: %v", i, err)
		}
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("OnJobDone calls = %d, want 1", got)
	}
	after, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatalf("read tombstone after terminal callback: %v", err)
	}
	if string(after) != string(before) {
		t.Fatal("deleted graph terminal update rewrote the durable tombstone")
	}
	if err := svc.SetGraphRunState(context.Background(), job.ID, "other-run", model.JobStatusStopped, model.GraphRunStatusStopped, 10, 20, ""); !errors.Is(err, ErrJobDeleted) {
		t.Fatalf("unrelated run terminal error = %v, want ErrJobDeleted", err)
	}
}

func TestDeletedJobGraphTeardownExceptionsStayNarrow(t *testing.T) {
	svc, job, _ := newDeleteTestService(t, "job-deleted-graph-exceptions")
	if err := svc.SetGraphRunState(context.Background(), job.ID, "run-1", model.JobStatusRunning, model.GraphRunStatusRunning, 10, 0, ""); err != nil {
		t.Fatalf("bind graph run: %v", err)
	}
	if err := svc.AttachGraphSession(context.Background(), job.ID, "existing-session"); err != nil {
		t.Fatalf("seed graph session: %v", err)
	}
	if err := svc.MarkDeleted(job.ID); err != nil {
		t.Fatalf("MarkDeleted: %v", err)
	}

	// A duplicate cleanup registration is idempotent, and a genuinely late
	// session is accepted into memory. Neither exception opens other writes.
	if err := svc.AttachGraphSession(context.Background(), job.ID, "existing-session"); err != nil {
		t.Fatalf("duplicate teardown session: %v", err)
	}
	if err := svc.AttachGraphSession(context.Background(), job.ID, "late-session"); err != nil {
		t.Fatalf("late teardown session: %v", err)
	}
	if err := svc.SetGraphRunState(context.Background(), job.ID, "run-1", model.JobStatusRunning, model.GraphRunStatusRunning, 10, 0, ""); !errors.Is(err, ErrJobDeleted) {
		t.Fatalf("non-terminal graph update = %v, want ErrJobDeleted", err)
	}
}

func TestDeleteFailureKeepsTombstoneAndRetrySucceeds(t *testing.T) {
	svc, job, jobDir := newDeleteTestService(t, "job-delete-retry")
	reader, err := svc.Subscribe(job.ID, 0)
	if err != nil {
		t.Fatalf("Subscribe: %v", err)
	}
	defer reader.Close()
	manager := &failDeleteFileManager{FileManager: svc.fileManager}
	manager.fail.Store(true)
	svc.fileManager = manager

	err = svc.Delete(job.ID)
	if !errors.Is(err, errForcedJobDelete) {
		t.Fatalf("Delete error = %v, want forced file deletion failure", err)
	}
	got, ok := svc.Get(job.ID)
	if !ok || !got.Deleted {
		t.Fatalf("failed Delete lost tombstone: job=%#v ok=%t", got, ok)
	}
	if _, statErr := os.Stat(jobDir); statErr != nil {
		t.Fatalf("failed Delete removed job dir unexpectedly: %v", statErr)
	}
	repo, err := svc.getOrCreateRepo(job.WorkspaceID)
	if err != nil {
		t.Fatalf("get repo after failed Delete: %v", err)
	}
	persisted, err := repo.Load(job.ID)
	if err != nil || persisted == nil || !persisted.Deleted {
		t.Fatalf("durable tombstone after failed Delete = %#v, err=%v", persisted, err)
	}
	if svc.bus.get(job.ID) == nil {
		t.Fatal("failed Delete removed the event buffer")
	}
	if titleErr := svc.UpdateTitle(job.ID, "must stay fenced"); !errors.Is(titleErr, ErrJobDeleted) {
		t.Fatalf("UpdateTitle after failed Delete = %v, want ErrJobDeleted", titleErr)
	}

	manager.fail.Store(false)
	if err := svc.Delete(job.ID); err != nil {
		t.Fatalf("retry Delete: %v", err)
	}
	if _, ok := svc.Get(job.ID); ok {
		t.Fatal("successful retry left job in memory")
	}
	if svc.bus.get(job.ID) != nil {
		t.Fatal("successful retry left the event buffer registered")
	}
	if _, statErr := os.Stat(jobDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("job dir remains after retry: %v", statErr)
	}
}
