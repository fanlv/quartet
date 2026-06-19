package graph

import (
	"context"
	"testing"

	"github.com/fanlv/quartet/types/model"
)

// loopNode builds a loop container node.
func loopFixed(id string, count, maxIters int) model.GraphNode {
	return model.GraphNode{ID: id, Type: model.GraphNodeTypeLoop, Config: model.GraphNodeConfig{
		LoopMode:      model.GraphLoopModeFixed,
		FixedCount:    count,
		MaxIterations: maxIters,
	}}
}

func loopUntil(id, condition string, maxIters int) model.GraphNode {
	return model.GraphNode{ID: id, Type: model.GraphNodeTypeLoop, Config: model.GraphNodeConfig{
		LoopMode:       model.GraphLoopModeUntil,
		UntilCondition: condition,
		MaxIterations:  maxIters,
	}}
}

// childShell builds a Shell node inside a loop container.
func childShell(id, parent, script string, outputs ...string) model.GraphNode {
	n := shellNode(id, script, outputs...)
	n.ParentID = parent
	return n
}

func childEnd(id, parent string) model.GraphNode {
	return model.GraphNode{ID: id, Type: model.GraphNodeTypeEnd, ParentID: parent}
}

func mustStart(t *testing.T, svc Service, cfg model.GraphConfig, jobID string) string {
	t.Helper()
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: jobID, Config: &cfg}, stubGraphRunner{}, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	return run.ID
}

// TestLoopFixedCountRepeats: a fixed-count loop runs its subgraph N times, each
// round accumulating into a control file, and the downstream main node reads the
// final accumulated variable.
func TestLoopFixedCountRepeats(t *testing.T) {
	uniqueMemoryRoot(t)
	workdir := t.TempDir()
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	// Loop body appends to a file and exports its line count as `count`.
	counter := workdir + "/counter.txt"
	cfg := model.GraphConfig{
		Workdir: workdir,
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			loopFixed("lp", 3, 0),
			childShell("body", "lp", "echo x >> "+counter+"; quartet_set count \"$(wc -l < "+counter+" | tr -d ' ')\"", "count"),
			childEnd("le", "lp"),
			shellNode("after", "echo done"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_lp", "s", "lp"),
			edge("body_le", "body", "le"),
			edge("lp_after", "lp", "after"),
			edge("after_e", "after", "e"),
		},
	}
	runID := mustStart(t, svc, cfg, "job-loop-fixed")
	got := waitGraphRunStatus(t, svc, runID, model.GraphRunStatusCompleted)
	lp, ok := instByNode(got, "lp")
	if !ok {
		t.Fatal("loop instance missing")
	}
	if lp.Status != model.GraphInstanceStatusSucceeded {
		t.Fatalf("loop status = %s, want succeeded", lp.Status)
	}
	// After 3 rounds the accumulated count must be "3".
	if lp.VisibleVariables["count"] != "3" {
		t.Fatalf("loop external snapshot count = %q, want 3", lp.VisibleVariables["count"])
	}
	after, _ := instByNode(got, "after")
	if after.Status != model.GraphInstanceStatusSucceeded {
		t.Fatalf("after status = %s, want succeeded", after.Status)
	}
	if after.VisibleVariables["count"] != "3" {
		t.Fatalf("downstream count = %q, want 3", after.VisibleVariables["count"])
	}
}

// TestLoopZeroCountSkips: a fixed loop with count 0 runs no rounds; the body
// never executes and the main graph continues.
func TestLoopZeroCountSkips(t *testing.T) {
	uniqueMemoryRoot(t)
	workdir := t.TempDir()
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	cfg := model.GraphConfig{
		Workdir:   workdir,
		Variables: map[string]string{"seed": "v0"},
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			loopFixed("lp", 0, 0),
			childShell("body", "lp", "echo ran"),
			childEnd("le", "lp"),
			shellNode("after", "echo {{seed}}"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_lp", "s", "lp"),
			edge("body_le", "body", "le"),
			edge("lp_after", "lp", "after"),
			edge("after_e", "after", "e"),
		},
	}
	runID := mustStart(t, svc, cfg, "job-loop-zero")
	got := waitGraphRunStatus(t, svc, runID, model.GraphRunStatusCompleted)
	if _, ok := instByNode(got, "body"); ok {
		t.Fatal("loop body must not run with fixed count 0")
	}
	lp, _ := instByNode(got, "lp")
	// 0-count loop inherits its entry snapshot.
	if lp.VisibleVariables["seed"] != "v0" {
		t.Fatalf("loop external snapshot seed = %q, want v0", lp.VisibleVariables["seed"])
	}
	after, _ := instByNode(got, "after")
	if after.Status != model.GraphInstanceStatusSucceeded {
		t.Fatalf("after status = %s, want succeeded", after.Status)
	}
}

// TestLoopUntilDoWhile: an until loop runs at least once and stops when the
// round-end condition becomes true; the body increments a counter file.
func TestLoopUntilDoWhile(t *testing.T) {
	uniqueMemoryRoot(t)
	workdir := t.TempDir()
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	counter := workdir + "/c.txt"
	cfg := model.GraphConfig{
		Workdir: workdir,
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			loopUntil("lp", `{{n}} == "3"`, 100),
			childShell("body", "lp", "echo x >> "+counter+"; quartet_set n \"$(wc -l < "+counter+" | tr -d ' ')\"", "n"),
			childEnd("le", "lp"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_lp", "s", "lp"),
			edge("body_le", "body", "le"),
			edge("lp_e", "lp", "e"),
		},
	}
	runID := mustStart(t, svc, cfg, "job-loop-until")
	got := waitGraphRunStatus(t, svc, runID, model.GraphRunStatusCompleted)
	lp, _ := instByNode(got, "lp")
	if lp.VisibleVariables["n"] != "3" {
		t.Fatalf("loop external snapshot n = %q, want 3", lp.VisibleVariables["n"])
	}
}

// TestLoopUntilMaxItersFails: an until loop whose condition never holds fails
// the run when it reaches the max-iterations backstop.
func TestLoopUntilMaxItersFails(t *testing.T) {
	uniqueMemoryRoot(t)
	workdir := t.TempDir()
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	cfg := model.GraphConfig{
		Workdir: workdir,
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			loopUntil("lp", `{{n}} == "never"`, 3),
			childShell("body", "lp", "quartet_set n hello", "n"),
			childEnd("le", "lp"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_lp", "s", "lp"),
			edge("body_le", "body", "le"),
			edge("lp_e", "lp", "e"),
		},
	}
	runID := mustStart(t, svc, cfg, "job-loop-maxiter")
	got := waitGraphRunStatus(t, svc, runID, model.GraphRunStatusFailed)
	if got.Run.LastError == nil || !contains(got.Run.LastError.Message, "max iterations") {
		t.Fatalf("expected max-iterations failure, got %+v", got.Run.LastError)
	}
}

// TestLoopAccumulatesAcrossRounds: the next round's entry reads the previous
// round's variables (accumulation, not isolation); same-node writes overwrite
// across rounds.
func TestLoopAccumulatesAcrossRounds(t *testing.T) {
	uniqueMemoryRoot(t)
	workdir := t.TempDir()
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	// Each round appends "*" to {{acc}} (seeded empty → "*", then "**", ...).
	cfg := model.GraphConfig{
		Workdir:   workdir,
		Variables: map[string]string{"acc": ""},
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			loopFixed("lp", 3, 0),
			childShell("body", "lp", `quartet_set acc "{{acc}}*"`, "acc"),
			childEnd("le", "lp"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_lp", "s", "lp"),
			edge("body_le", "body", "le"),
			edge("lp_e", "lp", "e"),
		},
	}
	runID := mustStart(t, svc, cfg, "job-loop-accum")
	got := waitGraphRunStatus(t, svc, runID, model.GraphRunStatusCompleted)
	lp, _ := instByNode(got, "lp")
	if lp.VisibleVariables["acc"] != "***" {
		t.Fatalf("accumulated acc = %q, want ***", lp.VisibleVariables["acc"])
	}
}

// TestLoopInternalFailurePropagates: a failure inside a loop body fails the run.
func TestLoopInternalFailurePropagates(t *testing.T) {
	uniqueMemoryRoot(t)
	workdir := t.TempDir()
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	cfg := model.GraphConfig{
		Workdir: workdir,
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			loopFixed("lp", 5, 0),
			childShell("body", "lp", "exit 7"),
			childEnd("le", "lp"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_lp", "s", "lp"),
			edge("body_le", "body", "le"),
			edge("lp_e", "lp", "e"),
		},
	}
	runID := mustStart(t, svc, cfg, "job-loop-fail")
	got := waitGraphRunStatus(t, svc, runID, model.GraphRunStatusFailed)
	body, ok := instByNode(got, "body")
	if !ok || body.Status != model.GraphInstanceStatusFailed {
		t.Fatalf("body status = %+v, want failed", body)
	}
}

// TestNestedLoops: a loop inside a loop runs the inner subgraph
// outer×inner times with unique instance keys, and the outer loop produces a
// correct external snapshot.
func TestNestedLoops(t *testing.T) {
	uniqueMemoryRoot(t)
	workdir := t.TempDir()
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	counter := workdir + "/n.txt"
	// Outer loop (2x) contains an inner loop (3x); inner body appends to a file.
	// Total inner-body executions = 6.
	cfg := model.GraphConfig{
		Workdir: workdir,
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			loopFixed("outer", 2, 0),
			// inner loop is the outer subgraph entry (no in-edge inside outer).
			{ID: "inner", Type: model.GraphNodeTypeLoop, ParentID: "outer", Config: model.GraphNodeConfig{
				LoopMode: model.GraphLoopModeFixed, FixedCount: 3,
			}},
			childEnd("outer_end", "outer"),
			// inner subgraph body + end (parent = inner).
			childShell("ibody", "inner", "echo x >> "+counter+"; quartet_set total \"$(wc -l < "+counter+" | tr -d ' ')\"", "total"),
			childEnd("inner_end", "inner"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_outer", "s", "outer"),
			edge("inner_outerend", "inner", "outer_end"),
			edge("ibody_innerend", "ibody", "inner_end"),
			edge("outer_e", "outer", "e"),
		},
	}
	runID := mustStart(t, svc, cfg, "job-nested-loop")
	got := waitGraphRunStatus(t, svc, runID, model.GraphRunStatusCompleted)
	outer, _ := instByNode(got, "outer")
	if outer.Status != model.GraphInstanceStatusSucceeded {
		t.Fatalf("outer loop status = %s, want succeeded", outer.Status)
	}
	if outer.VisibleVariables["total"] != "6" {
		t.Fatalf("nested total = %q, want 6", outer.VisibleVariables["total"])
	}
	// Inner body must have run 6 times → 6 distinct instance keys.
	bodyInstances := 0
	for _, in := range got.Instances {
		if in.NodeID == "ibody" {
			bodyInstances++
		}
	}
	if bodyInstances != 6 {
		t.Fatalf("inner body instances = %d, want 6", bodyInstances)
	}
}

// TestLoopStopLoopEndsContainer: quartet_break (STOP_LOOP) inside a loop ends
// the container early.
func TestLoopStopLoopEndsContainer(t *testing.T) {
	uniqueMemoryRoot(t)
	workdir := t.TempDir()
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	counter := workdir + "/s.txt"
	// Fixed 10x but body breaks on the first round → only one body execution.
	cfg := model.GraphConfig{
		Workdir: workdir,
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			loopFixed("lp", 10, 0),
			childShell("body", "lp", "echo x >> "+counter+"; quartet_break"),
			childEnd("le", "lp"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_lp", "s", "lp"),
			edge("body_le", "body", "le"),
			edge("lp_e", "lp", "e"),
		},
	}
	runID := mustStart(t, svc, cfg, "job-loop-break")
	got := waitGraphRunStatus(t, svc, runID, model.GraphRunStatusCompleted)
	lp, _ := instByNode(got, "lp")
	if lp.Status != model.GraphInstanceStatusSucceeded {
		t.Fatalf("loop status = %s, want succeeded", lp.Status)
	}
	bodyInstances := 0
	for _, in := range got.Instances {
		if in.NodeID == "body" {
			bodyInstances++
		}
	}
	if bodyInstances != 1 {
		t.Fatalf("body instances = %d, want 1 (STOP_LOOP after first round)", bodyInstances)
	}
}

// TestStopLoopOutsideLoopFails: STOP_LOOP in a main-graph Shell node fails it.
func TestStopLoopOutsideLoopFails(t *testing.T) {
	uniqueMemoryRoot(t)
	workdir := t.TempDir()
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	cfg := model.GraphConfig{
		Workdir: workdir,
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			shellNode("bad", "quartet_break"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_bad", "s", "bad"),
			edge("bad_e", "bad", "e"),
		},
	}
	runID := mustStart(t, svc, cfg, "job-break-outside")
	got := waitGraphRunStatus(t, svc, runID, model.GraphRunStatusFailed)
	bad, _ := instByNode(got, "bad")
	if bad.Status != model.GraphInstanceStatusFailed {
		t.Fatalf("bad status = %s, want failed", bad.Status)
	}
	if bad.Error == nil || !contains(bad.Error.Message, "STOP_LOOP is only supported inside loop") {
		t.Fatalf("error missing STOP_LOOP detail: %+v", bad.Error)
	}
}

// TestStopWorkflowEndsRunEarly: quartet_return (STOP_WORKFLOW) ends the run with
// early success and stops downstream scheduling.
func TestStopWorkflowEndsRunEarly(t *testing.T) {
	uniqueMemoryRoot(t)
	workdir := t.TempDir()
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	cfg := model.GraphConfig{
		Workdir: workdir,
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			shellNode("stop", "quartet_return"),
			shellNode("after", "echo should-not-run"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_stop", "s", "stop"),
			edge("stop_after", "stop", "after"),
			edge("after_e", "after", "e"),
		},
	}
	runID := mustStart(t, svc, cfg, "job-stop-workflow")
	got := waitGraphRunStatus(t, svc, runID, model.GraphRunStatusCompleted)
	stop, _ := instByNode(got, "stop")
	if stop.Status != model.GraphInstanceStatusSucceeded {
		t.Fatalf("stop status = %s, want succeeded", stop.Status)
	}
	if _, ok := instByNode(got, "after"); ok {
		t.Fatal("downstream node must not run after STOP_WORKFLOW")
	}
}

// TestLoopSerialConcurrencyNoDeadlock: a loop graph with concurrency limit 1
// (and the loop container not consuming a slot) completes without deadlock.
func TestLoopSerialConcurrencyNoDeadlock(t *testing.T) {
	uniqueMemoryRoot(t)
	workdir := t.TempDir()
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	cfg := model.GraphConfig{
		Workdir:   workdir,
		RunConfig: model.GraphRunConfig{ConcurrencyLimit: 1},
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			loopFixed("outer", 2, 0),
			{ID: "inner", Type: model.GraphNodeTypeLoop, ParentID: "outer", Config: model.GraphNodeConfig{
				LoopMode: model.GraphLoopModeFixed, FixedCount: 2,
			}},
			childEnd("outer_end", "outer"),
			childShell("ibody", "inner", "echo work"),
			childEnd("inner_end", "inner"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_outer", "s", "outer"),
			edge("inner_outerend", "inner", "outer_end"),
			edge("ibody_innerend", "ibody", "inner_end"),
			edge("outer_e", "outer", "e"),
		},
	}
	runID := mustStart(t, svc, cfg, "job-loop-serial")
	got := waitGraphRunStatus(t, svc, runID, model.GraphRunStatusCompleted)
	if got.Run.Status != model.GraphRunStatusCompleted {
		t.Fatalf("run status = %s, want completed", got.Run.Status)
	}
}
