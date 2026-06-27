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
	_, _ = svc.StopRun(context.Background(), run.ID, "")
	waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusStopped)
}

// TestResetArchivesSessionedInstances verifies that a wholesale loop reset
// preserves the sessions of removed instances (including already-succeeded
// siblings) into the archive, while session-less instances are dropped silently.
// This backs the Chat sidebar's prior-attempt conversation list across a resume.
func TestResetArchivesSessionedInstances(t *testing.T) {
	// A loop (loop-1) with: a succeeded Agent (prompt-ok, has session), a failed
	// Agent (prompt-bad, has session), and a succeeded If-Else (no session). With
	// no persisted loop scope (nil loopState), the failed instance forces a
	// wholesale reset of the whole loop subtree (the fallback path).
	loopKey := func(node string) model.GraphInstanceKey {
		return model.GraphInstanceKey{NodeID: node, Iterations: []model.GraphLoopIteration{{LoopNodeID: "loop-1", Index: 0}}}
	}
	instances := map[string]model.GraphInstanceState{
		"loop-1": {Key: model.GraphInstanceKey{NodeID: "loop-1"}, NodeID: "loop-1", NodeType: model.GraphNodeTypeLoop, Status: model.GraphInstanceStatusRunning},
		instanceKeyString(loopKey("prompt-ok")): {
			Key: loopKey("prompt-ok"), NodeID: "prompt-ok", NodeType: model.GraphNodeTypePrompt,
			Status: model.GraphInstanceStatusSucceeded, SessionID: "session-ok", DisplaySessionID: "session-ok",
		},
		instanceKeyString(loopKey("prompt-bad")): {
			Key: loopKey("prompt-bad"), NodeID: "prompt-bad", NodeType: model.GraphNodeTypePrompt,
			Status: model.GraphInstanceStatusFailed, SessionID: "session-bad", DisplaySessionID: "session-bad",
		},
		instanceKeyString(loopKey("ifElse")): {
			Key: loopKey("ifElse"), NodeID: "ifElse", NodeType: model.GraphNodeTypeIfElse,
			Status: model.GraphInstanceStatusSucceeded,
		},
	}

	rb := newResumeBuilder(model.GraphConfig{}, instances, map[string]model.GraphEdgeState{}, map[string]map[string]string{}, nil)
	rb.resetResettable()

	// Whole loop subtree reset → no instances survive.
	if len(rb.instances) != 0 {
		t.Fatalf("expected all loop-subtree instances reset, got %d remaining: %v", len(rb.instances), rb.instances)
	}
	// Both sessioned instances archived (succeeded sibling + failed node); the
	// session-less If-Else and loop container are not.
	if len(rb.archived) != 2 {
		t.Fatalf("expected 2 archived sessioned instances, got %d: %v", len(rb.archived), rb.archived)
	}
	okArchived, ok := rb.archived[instanceKeyString(loopKey("prompt-ok"))]
	if !ok || okArchived.DisplaySessionID != "session-ok" {
		t.Fatalf("succeeded sessioned sibling not archived correctly: %+v (ok=%v)", okArchived, ok)
	}
	if _, ok := rb.archived[instanceKeyString(loopKey("prompt-bad"))]; !ok {
		t.Fatal("failed sessioned instance should be archived")
	}
	if _, ok := rb.archived[instanceKeyString(loopKey("ifElse"))]; ok {
		t.Fatal("session-less If-Else must NOT be archived")
	}
	if _, ok := rb.archived["loop-1"]; ok {
		t.Fatal("session-less loop container must NOT be archived")
	}
}

// releaseAfter closes the gate's release channel once (guarded for re-use).
func releaseAfter(g *gatedRunner) {
	defer func() { _ = recover() }()
	close(g.release)
}
