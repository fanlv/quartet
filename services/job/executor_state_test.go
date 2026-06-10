package job

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/types/model"
)

// captureTerminalEvent registers a subscriber for jobID, runs fn (which is
// expected to publish exactly one terminal event for the job), then returns
// that event. Subscribing before the publish call is required because the
// buffer may GC reclaimed events once cursors advance — observing on the
// live read path is the only reliable way for the test.
func captureTerminalEvent(t *testing.T, svc *serviceImpl, jobID string, fn func()) any {
	t.Helper()
	reader, err := svc.Subscribe(jobID, 0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer reader.Close()

	fn()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	for {
		entries, ok := reader.Read(ctx, 32)
		if !ok {
			t.Fatalf("timed out waiting for terminal event for jobId=%s", jobID)
			return nil
		}
		for _, e := range entries {
			if isTerminalEvent(e.Event) {
				return e.Event
			}
			if e.Seq > 0 {
				reader.Ack(e.Seq)
			}
		}
	}
}

type stubJobRepo struct{}

var _ repository.JobRepo = (*stubJobRepo)(nil)

func (r *stubJobRepo) Save(jobID string, job *model.Job) error { return nil }
func (r *stubJobRepo) Load(jobID string) (*model.Job, error)   { return nil, nil }
func (r *stubJobRepo) ListIDs() ([]string, error)              { return nil, nil }
func (r *stubJobRepo) LoadAll() ([]*model.Job, error)          { return nil, nil }

func newStateTestService() *serviceImpl {
	return &serviceImpl{
		jobs:                   make(map[string]*model.Job),
		repos:                  map[string]repository.JobRepo{"": &stubJobRepo{}},
		bus:                    newBusOwner(),
		cancels:                make(map[string]*cancelEntry),
		dones:                  make(map[string]chan struct{}),
		interactivePriorStatus: make(map[string]model.JobStatus),
		wsListVersion:          make(map[string]int64),
		notifiedJobs:           make(map[string]struct{}),
		runStates:              make(map[string]*loopRunState),
	}
}

func TestTerminalEventsReusePersistedFinishedAt(t *testing.T) {
	tests := []struct {
		name      string
		run       func(*serviceImpl, *model.Job)
		wantEvent model.EventType
	}{
		{
			name: "finish",
			run: func(s *serviceImpl, job *model.Job) {
				s.finishJob(context.Background(), job, false)
			},
			wantEvent: model.EventTypeJobCompleted,
		},
		{
			name: "stop",
			run: func(s *serviceImpl, job *model.Job) {
				s.stopJob(context.Background(), job, false)
			},
			wantEvent: model.EventTypeJobStopped,
		},
		{
			name: "fail",
			run: func(s *serviceImpl, job *model.Job) {
				s.failJob(context.Background(), job, "boom", false, false)
			},
			wantEvent: model.EventTypeJobFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			svc := newStateTestService()
			job := &model.Job{ID: tt.name, WorkspaceID: "", Progress: &model.JobProgress{}}

			ev := captureTerminalEvent(t, svc, job.ID, func() {
				tt.run(svc, job)
			})

			if job.FinishedAt <= 0 {
				t.Fatalf("job.FinishedAt not set: %d", job.FinishedAt)
			}

			switch event := ev.(type) {
			case *model.JobCompletedEvent:
				if tt.wantEvent != model.EventTypeJobCompleted {
					t.Fatalf("got JobCompletedEvent, want %s", tt.wantEvent)
				}
				if event.Timestamp != job.FinishedAt {
					t.Fatalf("event timestamp = %d, want persisted FinishedAt %d", event.Timestamp, job.FinishedAt)
				}
			case *model.JobStoppedEvent:
				if tt.wantEvent != model.EventTypeJobStopped {
					t.Fatalf("got JobStoppedEvent, want %s", tt.wantEvent)
				}
				if event.Timestamp != job.FinishedAt {
					t.Fatalf("event timestamp = %d, want persisted FinishedAt %d", event.Timestamp, job.FinishedAt)
				}
			case *model.JobFailedEvent:
				if tt.wantEvent != model.EventTypeJobFailed {
					t.Fatalf("got JobFailedEvent, want %s", tt.wantEvent)
				}
				if event.Timestamp != job.FinishedAt {
					t.Fatalf("event timestamp = %d, want persisted FinishedAt %d", event.Timestamp, job.FinishedAt)
				}
			default:
				t.Fatalf("unexpected event type %T", ev)
			}
		})
	}
}

func TestPublishRunOutcomeUsesProvidedTerminalTimestamp(t *testing.T) {
	svc := newStateTestService()
	const terminalAt int64 = 123456789

	finishEvent := captureSinglePublish(t, svc, "job-finish", func() {
		svc.publishRunOutcome("job-finish", "session-1", []int{0}, "run-1", nil, terminalAt)
	})
	finish, ok := finishEvent.(*model.RunFinishedEvent)
	if !ok {
		t.Fatalf("finish event type = %T, want *model.RunFinishedEvent", finishEvent)
	}
	if finish.Timestamp != terminalAt {
		t.Fatalf("RUN_FINISHED.timestamp = %d, want %d", finish.Timestamp, terminalAt)
	}

	// A genuine error (not user-initiated stop) must surface as RUN_ERROR so
	// the frontend can report it. context.Canceled is deliberately excluded
	// here — see the canceled case below.
	errorEvent := captureSinglePublish(t, svc, "job-error", func() {
		svc.publishRunOutcome("job-error", "session-1", []int{0}, "run-2", errors.New("boom"), terminalAt)
	})
	errEvent, ok := errorEvent.(*model.RunErrorEvent)
	if !ok {
		t.Fatalf("error event type = %T, want *model.RunErrorEvent", errorEvent)
	}
	if errEvent.Timestamp != terminalAt {
		t.Fatalf("RUN_ERROR.timestamp = %d, want %d", errEvent.Timestamp, terminalAt)
	}

	// User-initiated stop (context.Canceled) is NOT an error: publishRunOutcome
	// emits RUN_FINISHED so the frontend doesn't show a spurious error toast.
	canceledEvent := captureSinglePublish(t, svc, "job-canceled", func() {
		svc.publishRunOutcome("job-canceled", "session-1", []int{0}, "run-3", context.Canceled, terminalAt)
	})
	canceledFinish, ok := canceledEvent.(*model.RunFinishedEvent)
	if !ok {
		t.Fatalf("canceled event type = %T, want *model.RunFinishedEvent", canceledEvent)
	}
	if canceledFinish.Timestamp != terminalAt {
		t.Fatalf("RUN_FINISHED.timestamp = %d, want %d", canceledFinish.Timestamp, terminalAt)
	}
}

// captureSinglePublish subscribes, runs fn (which publishes exactly one
// event), and returns that event. Used by tests that previously inspected
// the per-job replay buffer directly.
func captureSinglePublish(t *testing.T, svc *serviceImpl, jobID string, fn func()) any {
	t.Helper()
	reader, err := svc.Subscribe(jobID, 0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer reader.Close()

	fn()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	entries, ok := reader.Read(ctx, 1)
	if !ok || len(entries) == 0 {
		t.Fatalf("timed out waiting for event on jobId=%s", jobID)
	}
	return entries[0].Event
}

func TestFinishJobReusesPrepopulatedFinishedAt(t *testing.T) {
	svc := newStateTestService()
	job := &model.Job{ID: "job-prepopulated", WorkspaceID: "", FinishedAt: 987654321, Progress: &model.JobProgress{}}

	ev := captureTerminalEvent(t, svc, job.ID, func() {
		svc.finishJob(context.Background(), job, false)
	})

	if job.FinishedAt != 987654321 {
		t.Fatalf("job.FinishedAt = %d, want preserved value 987654321", job.FinishedAt)
	}
	event, ok := ev.(*model.JobCompletedEvent)
	if !ok {
		t.Fatalf("event type = %T, want *model.JobCompletedEvent", ev)
	}
	if event.Timestamp != job.FinishedAt {
		t.Fatalf("event timestamp = %d, want persisted FinishedAt %d", event.Timestamp, job.FinishedAt)
	}
}

// TestClosePanicRoundIfOpen_ClosesAndAllowsReclaim guards the panic-recovery
// path described in §1.1 / §6.1 #12 of the SSE buffer spec. The recovery
// must:
//
//  1. Close the in-flight round (HasOpenRound flips to false) so the buffer
//     can later reclaim the round's A-class chunks.
//  2. Clear openRoundID so subsequent events from the next run are NOT
//     attributed to the orphan round.
//  3. After ResumeGC + cursor advance, the orphan round becomes
//     reclaim-eligible — without this, panic recovery would leak the round's
//     chunks for the lifetime of the buffer.
func TestClosePanicRoundIfOpen_ClosesAndAllowsReclaim(t *testing.T) {
	svc := newStateTestService()
	job := &model.Job{
		ID:         "job-panic",
		Progress:   &model.JobProgress{CurrentPath: []int{0, 0}},
		SessionIDs: []string{"sess-1"},
	}

	reader, err := svc.Subscribe(job.ID, 0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer reader.Close()

	// Open a round and emit one in-flight chunk (mirrors executeRepeat
	// publishing IterationStarted + a streaming text delta).
	svc.Publish(job.ID, &model.IterationStartedEvent{
		BaseEvent: model.BaseEvent{
			Type: model.EventTypeIterationStarted, JobID: job.ID,
			Path: []int{0, 0}, Timestamp: nowMillis(),
		},
	})
	svc.Publish(job.ID, &model.TextMessageContentEvent{
		BaseEvent: model.BaseEvent{Type: model.EventTypeTextMessageContent, JobID: job.ID},
		MessageID: "m1",
		Delta:     "in-flight chunk",
	})

	buf := svc.bus.get(job.ID)
	if buf == nil || !buf.HasOpenRound() {
		t.Fatalf("expected open round before panic recovery (buf=%v)", buf)
	}

	// Simulate runLoop / runInteractive's recover() path.
	svc.closePanicRoundIfOpen(job, errors.New("simulated panic"))

	if buf.HasOpenRound() {
		t.Fatalf("HasOpenRound=true after closePanicRoundIfOpen; recovery would leak round")
	}

	// The next event published must NOT be attributed to the orphan round —
	// otherwise the next run's first event would inherit the dangling
	// roundID and confuse GC boundaries (§1.1 第 12 条).
	svc.Publish(job.ID, &model.JobFailedEvent{
		BaseEvent: model.BaseEvent{Type: model.EventTypeJobFailed, JobID: job.ID},
		Message:   "simulated panic",
	})

	buf.mu.Lock()
	var lastRoundID string
	if n := len(buf.events); n > 0 {
		lastRoundID = buf.events[n-1].roundID
	}
	buf.mu.Unlock()
	if lastRoundID != "" {
		t.Fatalf("post-recovery event roundID=%q, want empty (orphan round still attaching events)", lastRoundID)
	}

	// Drain everything and ack so the cursor crosses the closed round and
	// the trailing JobFailed event.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	sawTerminal := false
	for !sawTerminal {
		entries, ok := reader.Read(ctx, 32)
		if !ok {
			t.Fatalf("reader closed before draining recovery events")
		}
		for _, e := range entries {
			reader.Ack(e.Seq)
			if isTerminalEvent(e.Event) {
				sawTerminal = true
			}
		}
	}

	// Mirror Continue / SendMessage on a terminal job: ResumeGC re-enables
	// reclamation that MarkTerminal disabled when JobFailed went through.
	svc.bus.resumeGC(job.ID)
	// Trigger another GC pass after the flag flipped.
	svc.Publish(job.ID, &model.CustomEvent{
		BaseEvent: model.BaseEvent{Type: model.EventTypeCustom, JobID: job.ID},
		Name:      "token_usage",
	})
	entries, ok := reader.Read(ctx, 32)
	if !ok {
		t.Fatalf("reader closed before draining trailing event")
	}
	for _, e := range entries {
		reader.Ack(e.Seq)
	}

	buf.mu.Lock()
	bufLen := len(buf.events)
	buf.mu.Unlock()
	if bufLen != 0 {
		t.Fatalf("buffer still holds %d events after ResumeGC + cursor advance; orphan round leaked", bufLen)
	}
}

// TestClosePanicRoundIfOpen_NoOpWhenNoRound guards the early-return branch:
// if the buffer has no open round (recovery hit before any IterationStarted,
// or after a normal close), the helper must not synthesise a stray pair of
// RUN_ERROR / ITERATION_FAILED events that no consumer expects.
func TestClosePanicRoundIfOpen_NoOpWhenNoRound(t *testing.T) {
	svc := newStateTestService()
	job := &model.Job{
		ID:       "job-no-round",
		Progress: &model.JobProgress{},
	}

	reader, err := svc.Subscribe(job.ID, 0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer reader.Close()

	svc.closePanicRoundIfOpen(job, errors.New("panic before any round"))

	buf := svc.bus.get(job.ID)
	if buf == nil {
		t.Fatalf("buffer should have been created by Subscribe")
	}
	buf.mu.Lock()
	bufLen := len(buf.events)
	buf.mu.Unlock()
	if bufLen != 0 {
		t.Fatalf("expected no events published when there was no open round, got %d", bufLen)
	}
}
