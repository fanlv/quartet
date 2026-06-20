package graph

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/fanlv/quartet/types/model"
)

// recordingSink records ClearGraphRunLinkage calls so a test can assert a run's
// Job linkage was cleared on delete.
type recordingSink struct {
	mu          sync.Mutex
	clearedRuns map[string]bool
}

func (s *recordingSink) SetGraphRunState(context.Context, string, string, model.JobStatus, int64, int64) error {
	return nil
}

func (s *recordingSink) ClearGraphRunLinkage(_ context.Context, _, graphRunID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.clearedRuns == nil {
		s.clearedRuns = map[string]bool{}
	}
	s.clearedRuns[graphRunID] = true
	return nil
}

func (s *recordingSink) cleared(runID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clearedRuns[runID]
}

// promptNode builds a Prompt business node (uses the runner, unlike Shell).
func promptNode(id string) model.GraphNode {
	return model.GraphNode{ID: id, Type: model.GraphNodeTypePrompt, Config: model.GraphNodeConfig{Prompt: "do " + id, AgentType: "tester"}}
}

// waitRunningCount polls the run until at least n instances are running.
func waitRunningCount(t *testing.T, svc Service, runID string, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st, err := svc.GetRunStatus(context.Background(), runID)
		if err == nil && st.Run != nil {
			running := 0
			for _, in := range st.Instances {
				if in.Status == model.GraphInstanceStatusRunning {
					running++
				}
			}
			if running >= n {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("did not observe %d running instances", n)
}

// TestHardStopInterruptsRunning verifies a hard stop cancels in-flight
// instances (marking them interrupted) and settles the run to "stopped".
func TestHardStopInterruptsRunning(t *testing.T) {
	uniqueMemoryRoot(t)
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	cfg := model.GraphConfig{
		Workdir: t.TempDir(),
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			promptNode("a"),
			promptNode("b"),
			shellNode("j", "echo done"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_a", "s", "a"), edge("s_b", "s", "b"),
			edge("a_j", "a", "j"), edge("b_j", "b", "j"),
			edge("j_e", "j", "e"),
		},
	}
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-1", Config: &cfg}, blockingGraphRunner{}, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	waitRunningCount(t, svc, run.ID, 2)
	if _, err := svc.StopRun(context.Background(), run.ID); err != nil {
		t.Fatalf("StopRun failed: %v", err)
	}
	got := waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusStopped)
	a, _ := instByNode(got, "a")
	b, _ := instByNode(got, "b")
	if a.Status != model.GraphInstanceStatusInterrupted || b.Status != model.GraphInstanceStatusInterrupted {
		t.Fatalf("expected a,b interrupted; got a=%s b=%s", a.Status, b.Status)
	}
	if _, ok := instByNode(got, "j"); ok {
		t.Fatal("join should not have run after hard stop")
	}
}

// TestPauseLetsInFlightFinish verifies a pause stops dispatching new instances,
// lets in-flight ones finish, and settles to "paused".
func TestPauseLetsInFlightFinish(t *testing.T) {
	uniqueMemoryRoot(t)
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	// start → a (gated prompt) → b (prompt) → end. Pause while a is in flight;
	// a finishes, b must NOT dispatch.
	cfg := model.GraphConfig{
		Workdir: t.TempDir(),
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			promptNode("a"),
			promptNode("b"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_a", "s", "a"), edge("a_b", "a", "b"), edge("b_e", "b", "e"),
		},
	}
	gate := &gatedRunner{release: make(chan struct{})}
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-1", Config: &cfg}, gate, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	waitRunningCount(t, svc, run.ID, 1)
	if _, err := svc.PauseRun(context.Background(), run.ID); err != nil {
		t.Fatalf("PauseRun failed: %v", err)
	}
	// Give the pause signal time to land, then release the in-flight node.
	time.Sleep(100 * time.Millisecond)
	close(gate.release)
	got := waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusPaused)
	a, _ := instByNode(got, "a")
	if a.Status != model.GraphInstanceStatusSucceeded {
		t.Fatalf("expected a succeeded, got %s", a.Status)
	}
	if b, ok := instByNode(got, "b"); ok && b.Status == model.GraphInstanceStatusRunning {
		t.Fatalf("b should not have dispatched after pause, got %s", b.Status)
	}
}

// TestStepStopFreezesBatch verifies step-stop runs the frozen batch to terminal
// and holds downstream instances, settling to "stepStopped" with a persisted
// frozen batch.
func TestStepStopFreezesBatch(t *testing.T) {
	uniqueMemoryRoot(t)
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	cfg := model.GraphConfig{
		Workdir: t.TempDir(),
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			promptNode("a"),
			promptNode("b"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_a", "s", "a"), edge("a_b", "a", "b"), edge("b_e", "b", "e"),
		},
	}
	gate := &gatedRunner{release: make(chan struct{})}
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-1", Config: &cfg}, gate, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	waitRunningCount(t, svc, run.ID, 1)
	if _, err := svc.StepStopRun(context.Background(), run.ID); err != nil {
		t.Fatalf("StepStopRun failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	close(gate.release)
	got := waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusStepStopped)
	a, _ := instByNode(got, "a")
	if a.Status != model.GraphInstanceStatusSucceeded {
		t.Fatalf("expected frozen-batch member a succeeded, got %s", a.Status)
	}
	if b, ok := instByNode(got, "b"); ok && b.Status == model.GraphInstanceStatusRunning {
		t.Fatalf("downstream b should not dispatch after step-stop, got %s", b.Status)
	}
	if got.Run.Resume == nil || got.Run.Resume.FrozenBatch == nil {
		t.Fatal("frozen batch not persisted")
	}
	if _, ok := got.Run.Resume.FrozenBatch.Members[instanceKeyString(a.Key)]; !ok {
		t.Fatalf("frozen batch should contain member a; members=%+v", got.Run.Resume.FrozenBatch.Members)
	}
}

// TestControlOnNonRunningRun verifies control actions on a run with no live
// scheduler return ErrGraphRunNotRunning.
func TestControlOnNonRunningRun(t *testing.T) {
	uniqueMemoryRoot(t)
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	cfg := linearShellCfg(t)
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-1", Config: &cfg}, stubGraphRunner{}, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusCompleted)
	if _, err := svc.StopRun(context.Background(), run.ID); err != ErrGraphRunNotRunning {
		t.Fatalf("StopRun on completed run = %v, want ErrGraphRunNotRunning", err)
	}
}

// TestDeleteRunRejectsInFlightAndUnlinksJob verifies DeleteRun refuses an
// in-flight run, then on a terminal run cascades the delete and unlinks the Job.
func TestDeleteRunRejectsInFlightAndUnlinksJob(t *testing.T) {
	uniqueMemoryRoot(t)
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	cfg := model.GraphConfig{
		Workdir: t.TempDir(),
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			promptNode("a"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{edge("s_a", "s", "a"), edge("a_e", "a", "e")},
	}
	sink := &recordingSink{}
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-1", Config: &cfg}, blockingGraphRunner{}, sink)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	waitRunningCount(t, svc, run.ID, 1)
	if err := svc.DeleteRun(context.Background(), run.ID, sink); !errors.Is(err, ErrGraphRunInFlight) {
		t.Fatalf("DeleteRun on running run = %v, want ErrGraphRunInFlight", err)
	}
	if _, err := svc.StopRun(context.Background(), run.ID); err != nil {
		t.Fatalf("StopRun failed: %v", err)
	}
	waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusStopped)
	if err := svc.DeleteRun(context.Background(), run.ID, sink); err != nil {
		t.Fatalf("DeleteRun on stopped run failed: %v", err)
	}
	if !sink.cleared(run.ID) {
		t.Fatal("expected job linkage cleared for deleted run")
	}
	if _, err := svc.GetRunStatus(context.Background(), run.ID); err == nil {
		t.Fatal("run should be gone after delete")
	}
}

// linearShellCfg is start → shell → end.
func linearShellCfg(t *testing.T) model.GraphConfig {
	t.Helper()
	return model.GraphConfig{
		Workdir: t.TempDir(),
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			shellNode("sh", "echo hi"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{edge("s_sh", "s", "sh"), edge("sh_e", "sh", "e")},
	}
}
