package graph

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/types/agui"
	"github.com/fanlv/quartet/types/model"
)

type sessionDrainSink struct {
	mu       sync.Mutex
	attached map[string][]string
}

func (*sessionDrainSink) SetGraphRunState(context.Context, string, string, model.JobStatus, model.GraphRunStatus, int64, int64, string) error {
	return nil
}

func (*sessionDrainSink) ClearGraphRunLinkage(context.Context, string, string) error {
	return nil
}

func (s *sessionDrainSink) AttachGraphSession(_ context.Context, jobID, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, attached := range s.attached[jobID] {
		if attached == sessionID {
			return nil
		}
	}
	s.attached[jobID] = append(s.attached[jobID], sessionID)
	return nil
}

func (*sessionDrainSink) JobTitle(context.Context, string) string { return "" }

func (s *sessionDrainSink) hasSession(jobID, sessionID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, attached := range s.attached[jobID] {
		if attached == sessionID {
			return true
		}
	}
	return false
}

type sessionDrainCancelAwareRunner struct {
	stubSnapshotSource
	initEntered chan struct{}
	created     atomic.Bool
}

func (r *sessionDrainCancelAwareRunner) InitSession(ctx context.Context, _ string, _ *model.SessionOverrides) (string, error) {
	close(r.initEntered)
	<-ctx.Done()
	r.created.Store(true) // Models a durable commit that races with cancellation.
	return "session-drain-cancel-aware", nil
}

func (*sessionDrainCancelAwareRunner) RunIteration(ctx context.Context, _ string, _ []*schema.Message, _ agui.EventHandler) error {
	return ctx.Err()
}

func (*sessionDrainCancelAwareRunner) SessionModelID(string) string { return "" }

type sessionDrainIgnoredCancelRunner struct {
	stubSnapshotSource
	initEntered    chan struct{}
	cancelObserved chan struct{}
	releaseInit    chan struct{}
	created        atomic.Bool
}

func (r *sessionDrainIgnoredCancelRunner) InitSession(ctx context.Context, _ string, _ *model.SessionOverrides) (string, error) {
	close(r.initEntered)
	go func() {
		<-ctx.Done()
		close(r.cancelObserved)
	}()
	<-r.releaseInit // Deliberately ignore cancellation until durable creation finishes.
	r.created.Store(true)
	return "session-drain-ignores-cancel", nil
}

func (*sessionDrainIgnoredCancelRunner) RunIteration(ctx context.Context, _ string, _ []*schema.Message, _ agui.EventHandler) error {
	return ctx.Err()
}

func (*sessionDrainIgnoredCancelRunner) SessionModelID(string) string { return "" }

func sessionDrainConfig(t *testing.T) model.GraphConfig {
	t.Helper()
	return model.GraphConfig{
		Workdir: t.TempDir(),
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			promptNode("p"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{edge("s_p", "s", "p"), edge("p_e", "p", "e")},
	}
}

func assertSessionDrainStopped(t *testing.T, svc Service, runID string) {
	t.Helper()
	status, err := svc.GetRunStatus(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetRunStatus: %v", err)
	}
	if status.Run.Status != model.GraphRunStatusStopped {
		t.Fatalf("run status = %s, want %s", status.Run.Status, model.GraphRunStatusStopped)
	}
	for _, instance := range status.Instances {
		if instance.NodeID != "p" {
			continue
		}
		if instance.Status != model.GraphInstanceStatusInterrupted {
			t.Fatalf("prompt status = %s, want %s", instance.Status, model.GraphInstanceStatusInterrupted)
		}
		return
	}
	t.Fatal("prompt instance is missing from persisted run state")
}

// A context-aware InitSession may durably commit at the same instant the hard
// stop cancels it. StopRunAndWait must not finish before that session is on the
// Job cleanup whitelist.
func TestReviewStopDuringInitMustNotLoseCreatedSession(t *testing.T) {
	uniqueMemoryRoot(t)
	svcAPI, err := NewService()
	if err != nil {
		t.Fatal(err)
	}
	runner := &sessionDrainCancelAwareRunner{initEntered: make(chan struct{})}
	sink := &sessionDrainSink{attached: map[string][]string{}}
	cfg := sessionDrainConfig(t)
	run, err := svcAPI.StartRun(context.Background(), &model.StartGraphRunRequest{
		JobID: "job-session-drain-cancel-aware", Config: &cfg,
	}, runner, sink)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	<-runner.initEntered

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := svcAPI.StopRunAndWait(ctx, run.ID, "stop during init"); err != nil {
		t.Fatalf("StopRunAndWait: %v", err)
	}
	if !runner.created.Load() {
		t.Fatal("fixture did not durably create its session")
	}
	if !sink.hasSession(run.JobID, "session-drain-cancel-aware") {
		t.Fatal("durably created session was not attached before scheduler completion")
	}
	assertSessionDrainStopped(t, svcAPI, run.ID)
}

// InitSession implementations are not required to return promptly on context
// cancellation. The hard-stop barrier must wait for one that commits late and
// attach its session before reporting scheduler completion.
func TestReviewStopWaitsForLateInitButStillLosesSessionReference(t *testing.T) {
	uniqueMemoryRoot(t)
	svcAPI, err := NewService()
	if err != nil {
		t.Fatal(err)
	}
	runner := &sessionDrainIgnoredCancelRunner{
		initEntered: make(chan struct{}), cancelObserved: make(chan struct{}), releaseInit: make(chan struct{}),
	}
	sink := &sessionDrainSink{attached: map[string][]string{}}
	cfg := sessionDrainConfig(t)
	run, err := svcAPI.StartRun(context.Background(), &model.StartGraphRunRequest{
		JobID: "job-session-drain-ignores-cancel", Config: &cfg,
	}, runner, sink)
	if err != nil {
		t.Fatalf("StartRun: %v", err)
	}
	<-runner.initEntered

	stopDone := make(chan error, 1)
	go func() {
		_, stopErr := svcAPI.StopRunAndWait(context.Background(), run.ID, "stop during late init")
		stopDone <- stopErr
	}()
	<-runner.cancelObserved
	select {
	case err := <-stopDone:
		t.Fatalf("StopRunAndWait returned before InitSession unwound: %v", err)
	default:
	}

	close(runner.releaseInit)
	select {
	case err := <-stopDone:
		if err != nil {
			t.Fatalf("StopRunAndWait: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("StopRunAndWait did not finish after InitSession returned")
	}
	if !runner.created.Load() {
		t.Fatal("fixture did not durably create its session")
	}
	if !sink.hasSession(run.JobID, "session-drain-ignores-cancel") {
		t.Fatal("late durable session was not attached before scheduler completion")
	}
	assertSessionDrainStopped(t, svcAPI, run.ID)
}
