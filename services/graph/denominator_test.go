package graph

import (
	"context"
	"testing"

	"github.com/fanlv/quartet/types/model"
)

// TestDenominatorUntilEarlyStop verifies an until-loop that satisfies its
// condition before maxIters reclaims the unrun rounds so completed==total.
func TestDenominatorUntilEarlyStop(t *testing.T) {
	uniqueMemoryRoot(t)
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	// Loop runs until {{done}} == "yes". The body sets done=yes on the first
	// round, so it runs exactly 1 of a possible maxIters rounds.
	loop := model.GraphNode{ID: "lp", Type: model.GraphNodeTypeLoop, Config: model.GraphNodeConfig{
		LoopMode:       model.GraphLoopModeUntil,
		UntilCondition: `{{done}} == "yes"`,
		MaxIterations:  10,
	}}
	body := model.GraphNode{ID: "body", Type: model.GraphNodeTypeShell, ParentID: "lp", Config: model.GraphNodeConfig{
		Script:          "quartet_set done yes",
		OutputVariables: []string{"done"},
	}}
	internalEnd := model.GraphNode{ID: "ie", Type: model.GraphNodeTypeEnd, ParentID: "lp"}
	loopStart := model.GraphNode{ID: "ls", Type: model.GraphNodeTypeStart, ParentID: "lp"}
	cfg := model.GraphConfig{
		Workdir: t.TempDir(),
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart), loop, loopStart, body, internalEnd, node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_lp", "s", "lp"), edge("lp_e", "lp", "e"), edge("ls_body", "ls", "body"), edge("body_ie", "body", "ie"),
		},
	}
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-1", Config: &cfg}, stubGraphRunner{}, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	got := waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusCompleted)
	if got.Progress.CompletedCount != got.Progress.TotalCount {
		t.Fatalf("expected completed==total after early stop, got %+v", got.Progress)
	}
	// The body ran once; the loop container completed once. Denominator should be
	// the 2 materialized business instances, not 1 + 10*1.
	if got.Progress.TotalCount != 2 {
		t.Fatalf("expected total=2 (loop + 1 body round), got %d", got.Progress.TotalCount)
	}
}

// TestDenominatorIfElseSkipCountsInNumerator verifies a pruned If-Else branch's
// business node materializes as skipped and counts in the numerator (not a
// denominator reclaim), and the run completes with completed==total.
func TestDenominatorIfElseSkipCountsInNumerator(t *testing.T) {
	uniqueMemoryRoot(t)
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	cfg := model.GraphConfig{
		Workdir:   t.TempDir(),
		Variables: map[string]string{"flag": "yes"},
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			ifElseNode("ie", `{{flag}} == "yes"`),
			shellNode("yes", "echo yes"),
			shellNode("no", "echo no"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_ie", "s", "ie"),
			portEdge("ie_yes", "ie", "yes", model.GraphEdgePortYes),
			portEdge("ie_no", "ie", "no", model.GraphEdgePortNo),
			edge("yes_e", "yes", "e"), edge("no_e", "no", "e"),
		},
	}
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-1", Config: &cfg}, stubGraphRunner{}, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	got := waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusCompleted)
	no, _ := instByNode(got, "no")
	if no.Status != model.GraphInstanceStatusSkipped {
		t.Fatalf("pruned branch node 'no' = %s, want skipped", no.Status)
	}
	// ie + yes + no all materialize (3 business nodes); completed(ie,yes) +
	// skipped(no) == total.
	if got.Progress.TotalCount != 3 {
		t.Fatalf("expected total=3, got %d", got.Progress.TotalCount)
	}
	if got.Progress.CompletedCount+got.Progress.SkippedCount != got.Progress.TotalCount {
		t.Fatalf("completed+skipped != total: %+v", got.Progress)
	}
}

// TestDenominatorZeroCountLoop verifies a 0-count fixed loop contributes only
// its container to the denominator (no subgraph instances).
func TestDenominatorZeroCountLoop(t *testing.T) {
	uniqueMemoryRoot(t)
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	loop := model.GraphNode{ID: "lp", Type: model.GraphNodeTypeLoop, Config: model.GraphNodeConfig{
		LoopMode: model.GraphLoopModeFixed, FixedCount: 0,
	}}
	body := model.GraphNode{ID: "body", Type: model.GraphNodeTypeShell, ParentID: "lp", Config: model.GraphNodeConfig{Script: "echo body"}}
	internalEnd := model.GraphNode{ID: "ie", Type: model.GraphNodeTypeEnd, ParentID: "lp"}
	loopStart := model.GraphNode{ID: "ls", Type: model.GraphNodeTypeStart, ParentID: "lp"}
	cfg := model.GraphConfig{
		Workdir: t.TempDir(),
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart), loop, loopStart, body, internalEnd, node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_lp", "s", "lp"), edge("lp_e", "lp", "e"), edge("ls_body", "ls", "body"), edge("body_ie", "body", "ie"),
		},
	}
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-1", Config: &cfg}, stubGraphRunner{}, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	got := waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusCompleted)
	if got.Progress.TotalCount != 1 {
		t.Fatalf("expected total=1 (loop container only), got %d", got.Progress.TotalCount)
	}
	if got.Progress.CompletedCount != 1 {
		t.Fatalf("expected completed=1, got %+v", got.Progress)
	}
}
