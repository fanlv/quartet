package handler

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/fanlv/quartet/services/job"
	"github.com/fanlv/quartet/types/model"
)

// fakeJobService embeds job.Service so tests only override the methods the
// exercised path actually calls; any unexpected call panics on the nil
// embedded interface, which is what we want.
type fakeJobService struct {
	job.Service
	mu         sync.Mutex
	titleCalls []string
}

func (f *fakeJobService) UpdateTitle(jobID string, title string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.titleCalls = append(f.titleCalls, title)
	return nil
}

func (f *fakeJobService) titleCallCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.titleCalls)
}

// A message sent to a Running job is rejected by SendMessage with
// ErrJobRunning (409). prepareJobSend must reject it UP FRONT: the pre-send
// flow applies metadata side effects (job title from the first message,
// session model / ACP field updates) that must not land for a send that is
// about to be rejected — otherwise the failed send's selection leaks into
// the next successful run.
func TestPrepareJobSend_RunningJobRejectedBeforeSideEffects(t *testing.T) {
	fjs := &fakeJobService{}
	h := &Handler{jobService: fjs, settingsService: &fakeSettings{}}
	// Default title + first user message: the buggy path would push a title
	// update before SendMessage rejected the send.
	j := &model.Job{ID: "j1", WorkspaceID: "ws1", Status: model.JobStatusRunning}
	req := &model.JobMessageRequest{Messages: []model.RequestMessage{{Content: "hello"}}}

	runner, opts, err := h.prepareJobSend(context.Background(), j, req)
	if !errors.Is(err, job.ErrJobRunning) {
		t.Fatalf("expected ErrJobRunning for a Running job, got err=%v runner=%v opts=%v", err, runner, opts)
	}
	if runner != nil || opts != nil {
		t.Errorf("rejected send must not produce runner/opts, got runner=%v opts=%v", runner, opts)
	}
	if n := fjs.titleCallCount(); n != 0 {
		t.Errorf("rejected send must not apply title side effects, got %d UpdateTitle calls", n)
	}
}

// Same gate for a soft-deleted job: SendMessage would reject with
// ErrJobDeleted, so prepareJobSend must not touch metadata either.
func TestPrepareJobSend_DeletedJobRejectedBeforeSideEffects(t *testing.T) {
	fjs := &fakeJobService{}
	h := &Handler{jobService: fjs, settingsService: &fakeSettings{}}
	j := &model.Job{ID: "j2", WorkspaceID: "ws1", Deleted: true}
	req := &model.JobMessageRequest{Messages: []model.RequestMessage{{Content: "hello"}}}

	_, _, err := h.prepareJobSend(context.Background(), j, req)
	if !errors.Is(err, job.ErrJobDeleted) {
		t.Fatalf("expected ErrJobDeleted for a deleted job, got %v", err)
	}
	if n := fjs.titleCallCount(); n != 0 {
		t.Errorf("rejected send must not apply title side effects, got %d UpdateTitle calls", n)
	}
}

// Happy path: a sendable job (terminal status, default title) still gets its
// title update and a ready runner — the early gate must not block legit sends.
func TestPrepareJobSend_SendableJobStillAppliesSideEffects(t *testing.T) {
	fjs := &fakeJobService{}
	h := &Handler{jobService: fjs, settingsService: &fakeSettings{}}
	j := &model.Job{ID: "j3", WorkspaceID: "ws1", Status: model.JobStatusCompleted}
	req := &model.JobMessageRequest{Messages: []model.RequestMessage{{Content: "hello"}}}

	runner, opts, err := h.prepareJobSend(context.Background(), j, req)
	if err != nil {
		t.Fatalf("prepareJobSend failed for a sendable job: %v", err)
	}
	if runner == nil || opts == nil {
		t.Fatal("expected runner and opts for a sendable job")
	}
	if n := fjs.titleCallCount(); n != 1 {
		t.Errorf("expected the first-message title update to be applied once, got %d calls", n)
	}
}
