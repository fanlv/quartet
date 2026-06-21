package graph

import (
	"context"
	"fmt"
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/types/agui"
	"github.com/fanlv/quartet/types/model"
)

// countingRunner records how many times each node's session ran, and can be
// told to fail a specific node until a later phase. Node id is derived from the
// session id which the stub returns as "session-<jobID>" — instead we track by
// call order is fragile, so we key on the prompt content "do <id>".
type countingRunner struct {
	stubSnapshotSource
	mu       sync.Mutex
	calls    map[string]int
	failNode map[string]bool // node id -> should fail this call
}

func newCountingRunner() *countingRunner {
	return &countingRunner{calls: map[string]int{}, failNode: map[string]bool{}}
}

func (r *countingRunner) InitSession(context.Context, string, *model.SessionOverrides) (string, error) {
	return "session-1", nil
}

func (r *countingRunner) RunIteration(_ context.Context, _ string, messages []*schema.Message, handler agui.EventHandler) error {
	node := ""
	if len(messages) > 0 {
		node = messages[0].Content
	}
	r.mu.Lock()
	r.calls[node]++
	fail := r.failNode[node]
	r.mu.Unlock()
	_ = handler.OnMessageDelta("out")
	if fail {
		return fmt.Errorf("forced failure for %s", node)
	}
	return nil
}

func (r *countingRunner) SessionModelID(string) string { return "" }

func (r *countingRunner) callCount(node string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls[node]
}

// TestResumeAfterFailureReRunsOnlyResettable verifies that on resume the
// succeeded upstream is NOT re-run, while the failed node is reset and re-run
// to completion.
func TestResumeAfterFailureReRunsOnlyResettable(t *testing.T) {
	uniqueMemoryRoot(t)
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	// start → a (prompt, succeeds) → b (prompt, fails first time) → end
	cfg := model.GraphConfig{
		Workdir: t.TempDir(),
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			promptNode("a"),
			promptNode("b"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{edge("s_a", "s", "a"), edge("a_b", "a", "b"), edge("b_e", "b", "e")},
	}
	runner := newCountingRunner()
	runner.failNode["do b"] = true
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-1", Config: &cfg}, runner, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusFailed)
	if runner.callCount("do a") != 1 {
		t.Fatalf("a should have run once, got %d", runner.callCount("do a"))
	}

	// Stop failing b, then resume.
	runner.mu.Lock()
	runner.failNode["do b"] = false
	runner.mu.Unlock()
	if _, err := svc.ResumeRun(context.Background(), run.ID, runner, nil); err != nil {
		t.Fatalf("ResumeRun failed: %v", err)
	}
	got := waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusCompleted)
	if runner.callCount("do a") != 1 {
		t.Fatalf("a must NOT be re-run on resume; got %d calls", runner.callCount("do a"))
	}
	if runner.callCount("do b") != 2 {
		t.Fatalf("b should have run twice (fail + resume), got %d", runner.callCount("do b"))
	}
	if got.Progress.CompletedCount != got.Progress.TotalCount {
		t.Fatalf("expected completed==total, got %+v", got.Progress)
	}
}

// TestResumeAfterPauseCompletes verifies a paused run resumes and finishes with
// completed==total, without re-running the already-succeeded node.
func TestResumeAfterPauseCompletes(t *testing.T) {
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
		Edges: []model.GraphEdge{edge("s_a", "s", "a"), edge("a_b", "a", "b"), edge("b_e", "b", "e")},
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
	releaseAfter(gate)
	waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusPaused)

	resumeRunner := newCountingRunner()
	if _, err := svc.ResumeRun(context.Background(), run.ID, resumeRunner, nil); err != nil {
		t.Fatalf("ResumeRun failed: %v", err)
	}
	got := waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusCompleted)
	// a already succeeded before pause; resume must not re-run it.
	if resumeRunner.callCount("do a") != 0 {
		t.Fatalf("a was succeeded before pause; resume must not re-run it, got %d", resumeRunner.callCount("do a"))
	}
	if resumeRunner.callCount("do b") != 1 {
		t.Fatalf("b should run once on resume, got %d", resumeRunner.callCount("do b"))
	}
	if got.Progress.CompletedCount != got.Progress.TotalCount {
		t.Fatalf("expected completed==total, got %+v", got.Progress)
	}
}

// TestResumeRejectsRunning verifies ResumeRun on a running run is rejected.
func TestResumeRejectsRunning(t *testing.T) {
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
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-1", Config: &cfg}, blockingGraphRunner{}, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	waitRunningCount(t, svc, run.ID, 1)
	_, err = svc.ResumeRun(context.Background(), run.ID, blockingGraphRunner{}, nil)
	if err == nil {
		t.Fatal("ResumeRun on running run should be rejected")
	}
	_, _ = svc.StopRun(context.Background(), run.ID)
	waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusStopped)
}

// releaseAfter closes the gate's release channel once (guarded for re-use).
func releaseAfter(g *gatedRunner) {
	defer func() { _ = recover() }()
	close(g.release)
}
