package graph

import (
	"context"
	"fmt"
	"os"
	"strings"
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

// childStart builds the loop-scoped start node (the container's entry marker).
// Every loop subgraph requires exactly one; wire it to the body entry node.
func childStart(id, parent string) model.GraphNode {
	return model.GraphNode{ID: id, Type: model.GraphNodeTypeStart, ParentID: parent}
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
			childStart("ls", "lp"),
			childEnd("le", "lp"),
			shellNode("after", "echo done"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_lp", "s", "lp"),
			edge("ls_body", "ls", "body"),
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
			childStart("ls", "lp"),
			childEnd("le", "lp"),
			shellNode("after", "echo {{seed}}"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_lp", "s", "lp"),
			edge("ls_body", "ls", "body"),
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
			childStart("ls", "lp"),
			childEnd("le", "lp"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_lp", "s", "lp"),
			edge("ls_body", "ls", "body"),
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

// TestLoopUntilMaxItersCompletes: an until loop whose condition never holds
// finishes successfully when it reaches the max-iterations backstop.
func TestLoopUntilMaxItersCompletes(t *testing.T) {
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
			childStart("ls", "lp"),
			childEnd("le", "lp"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_lp", "s", "lp"),
			edge("ls_body", "ls", "body"),
			edge("body_le", "body", "le"),
			edge("lp_e", "lp", "e"),
		},
	}
	runID := mustStart(t, svc, cfg, "job-loop-maxiter")
	got := waitGraphRunStatus(t, svc, runID, model.GraphRunStatusCompleted)
	if got.Run.LastError != nil {
		t.Fatalf("expected successful max-iterations completion, got error %+v", got.Run.LastError)
	}
	rounds := 0
	for _, inst := range got.Instances {
		if inst.NodeID == "body" {
			rounds++
		}
	}
	if rounds != 3 {
		t.Fatalf("until loop ran %d rounds, want 3", rounds)
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
			childStart("ls", "lp"),
			childEnd("le", "lp"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_lp", "s", "lp"),
			edge("ls_body", "ls", "body"),
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

// TestLoopIterationVarsInjected: the engine injects QUARTET_LOOP_* into the loop
// body across all channels — the shell environment ($QUARTET_LOOP_INDEX) and the
// {{...}} text substitution ({{QUARTET_LOOP_INDEX}}). A fixed 3-round loop logs
// the index/fixed-count/max-iters of each round; the recorded lines prove the
// index advances 0,1,2 and the static fields are populated.
func TestLoopIterationVarsInjected(t *testing.T) {
	uniqueMemoryRoot(t)
	workdir := t.TempDir()
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	log := workdir + "/iters.txt"
	// Body writes one line per round: env index | substituted index | fixed | max.
	script := `echo "env=$QUARTET_LOOP_INDEX tmpl={{QUARTET_LOOP_INDEX}} fixed=$QUARTET_LOOP_FIXED_COUNT max=$QUARTET_LOOP_MAX_ITERS" >> ` + log
	cfg := model.GraphConfig{
		Workdir: workdir,
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			loopFixed("lp", 3, 7),
			childShell("body", "lp", script),
			childStart("ls", "lp"),
			childEnd("le", "lp"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_lp", "s", "lp"),
			edge("ls_body", "ls", "body"),
			edge("body_le", "body", "le"),
			edge("lp_e", "lp", "e"),
		},
	}
	runID := mustStart(t, svc, cfg, "job-loop-itervars")
	waitGraphRunStatus(t, svc, runID, model.GraphRunStatusCompleted)

	data, rerr := os.ReadFile(log)
	if rerr != nil {
		t.Fatalf("read iter log: %v", rerr)
	}
	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	if len(lines) != 3 {
		t.Fatalf("got %d round lines, want 3:\n%s", len(lines), string(data))
	}
	for i, line := range lines {
		want := fmt.Sprintf("env=%d tmpl=%d fixed=3 max=7", i, i)
		if line != want {
			t.Fatalf("round %d line = %q, want %q", i, line, want)
		}
	}
}

// TestLoopUntilConditionSeesIndex: an until-mode loop can reference
// {{QUARTET_LOOP_INDEX}} in its condition, and QUARTET_LOOP_FIXED_COUNT is empty
// (not "0") for until loops. The condition stops the loop once the just-finished
// round index reaches 2, so the loop runs exactly rounds 0,1,2 (3 rounds).
func TestLoopUntilConditionSeesIndex(t *testing.T) {
	uniqueMemoryRoot(t)
	workdir := t.TempDir()
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	counter := workdir + "/n.txt"
	cfg := model.GraphConfig{
		Workdir: workdir,
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			loopUntil("lp", `{{QUARTET_LOOP_INDEX}} >= "2"`, 50),
			childShell("body", "lp", "echo x >> "+counter+`; quartet_set fixed "$QUARTET_LOOP_FIXED_COUNT"`, "fixed"),
			childStart("ls", "lp"),
			childEnd("le", "lp"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_lp", "s", "lp"),
			edge("ls_body", "ls", "body"),
			edge("body_le", "body", "le"),
			edge("lp_e", "lp", "e"),
		},
	}
	runID := mustStart(t, svc, cfg, "job-loop-until-index")
	got := waitGraphRunStatus(t, svc, runID, model.GraphRunStatusCompleted)
	lp, _ := instByNode(got, "lp")
	if lp.Status != model.GraphInstanceStatusSucceeded {
		t.Fatalf("loop status = %s, want succeeded", lp.Status)
	}
	if lp.VisibleVariables["fixed"] != "" {
		t.Fatalf("QUARTET_LOOP_FIXED_COUNT for until loop = %q, want empty", lp.VisibleVariables["fixed"])
	}
	data, rerr := os.ReadFile(counter)
	if rerr != nil {
		t.Fatalf("read counter: %v", rerr)
	}
	rounds := len(strings.Split(strings.TrimSpace(string(data)), "\n"))
	if rounds != 3 {
		t.Fatalf("until loop ran %d rounds, want 3", rounds)
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
			childStart("ls", "lp"),
			childEnd("le", "lp"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_lp", "s", "lp"),
			edge("ls_body", "ls", "body"),
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
			// outer subgraph: entry start → inner loop → outer end.
			childStart("outer_start", "outer"),
			{ID: "inner", Type: model.GraphNodeTypeLoop, ParentID: "outer", Config: model.GraphNodeConfig{
				LoopMode: model.GraphLoopModeFixed, FixedCount: 3,
			}},
			childEnd("outer_end", "outer"),
			// inner subgraph: entry start → body → inner end.
			childStart("inner_start", "inner"),
			childShell("ibody", "inner", "echo x >> "+counter+"; quartet_set total \"$(wc -l < "+counter+" | tr -d ' ')\"", "total"),
			childEnd("inner_end", "inner"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_outer", "s", "outer"),
			edge("outerstart_inner", "outer_start", "inner"),
			edge("inner_outerend", "inner", "outer_end"),
			edge("innerstart_ibody", "inner_start", "ibody"),
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
			childStart("ls", "lp"),
			childEnd("le", "lp"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_lp", "s", "lp"),
			edge("ls_body", "ls", "body"),
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
			childStart("outer_start", "outer"),
			{ID: "inner", Type: model.GraphNodeTypeLoop, ParentID: "outer", Config: model.GraphNodeConfig{
				LoopMode: model.GraphLoopModeFixed, FixedCount: 2,
			}},
			childEnd("outer_end", "outer"),
			childStart("inner_start", "inner"),
			childShell("ibody", "inner", "echo work"),
			childEnd("inner_end", "inner"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_outer", "s", "outer"),
			edge("outerstart_inner", "outer_start", "inner"),
			edge("inner_outerend", "inner", "outer_end"),
			edge("innerstart_ibody", "inner_start", "ibody"),
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

// TestResumeLoopFedByStartKeepsInitialVars is a regression test: a loop fed
// directly by the main-graph start node, whose body references an initial
// variable in an If-Else condition, must keep that variable after a resume.
//
// On resume the loop scope is rebuilt at its persisted round (step-level resume)
// and its in-flight round is re-seeded from the round-entry snapshot, which for
// round 0 derives from the start-sourced in-edge. The start node has no
// persisted instance/variable snapshot, so before the fix its contribution
// resolved to an empty map and the initial variable vanished, making the body's
// condition fail with "variable {{seed}} is unknown at evaluation time".
// (Mirrors the reported "MultiWorker is unknown after 恢复" bug.)
func TestResumeLoopFedByStartKeepsInitialVars(t *testing.T) {
	uniqueMemoryRoot(t)
	workdir := t.TempDir()
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	// The body's first invocation fails (exit 1) so the run ends "failed"; the
	// counter file persists across the resume so the second invocation succeeds.
	body := `n=$(cat .body_counter 2>/dev/null || echo 0)
n=$((n+1))
echo $n > .body_counter
if [ "$n" -eq 1 ]; then exit 1; fi
echo ok`
	cfg := model.GraphConfig{
		Workdir:   workdir,
		Variables: map[string]string{"seed": "1"},
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			loopFixed("lp", 1, 0),
			childStart("ls", "lp"),
			ifElseInLoop("cond", "lp", `{{seed}} == "1"`),
			childShell("body", "lp", body),
			childShell("skip", "lp", "echo skipped"),
			childEnd("le", "lp"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_lp", "s", "lp"),
			edge("ls_cond", "ls", "cond"),
			portEdge("cond_body", "cond", "body", model.GraphEdgePortYes),
			portEdge("cond_skip", "cond", "skip", model.GraphEdgePortNo),
			edge("body_le", "body", "le"),
			edge("skip_le", "skip", "le"),
			edge("lp_e", "lp", "e"),
		},
	}
	runID := mustStart(t, svc, cfg, "job-resume-seed")
	waitGraphRunStatus(t, svc, runID, model.GraphRunStatusFailed)

	if _, err := svc.ResumeRun(context.Background(), runID, stubGraphRunner{}, nil); err != nil {
		t.Fatalf("ResumeRun failed: %v", err)
	}
	got := waitGraphRunStatus(t, svc, runID, model.GraphRunStatusCompleted)
	if got.Run.Status != model.GraphRunStatusCompleted {
		t.Fatalf("run status = %s, want completed (lastErr=%q)", got.Run.Status, got.Run.Progress.LastError)
	}
	// The If-Else must have routed to the yes branch (seed=="1") on the resumed
	// round: the body executed and the no branch was pruned. A pruned business
	// node is still recorded as a "skipped" instance, so assert on status — the
	// pre-fix bug would have lost {{seed}} and failed the condition outright.
	bodyInst, ok := instByNode(got, "body")
	if !ok || bodyInst.Status != model.GraphInstanceStatusSucceeded {
		t.Fatalf("body status = %v (ok=%v), want succeeded", bodyInst.Status, ok)
	}
	skipInst, ok := instByNode(got, "skip")
	if !ok || skipInst.Status != model.GraphInstanceStatusSkipped {
		t.Fatalf("skip status = %v (ok=%v), want skipped; {{seed}} should still equal 1 after resume", skipInst.Status, ok)
	}
}

// ifElseInLoop builds an If-Else node inside a loop container.
func ifElseInLoop(id, parent, condition string) model.GraphNode {
	n := ifElseNode(id, condition)
	n.ParentID = parent
	return n
}

// TestResumeNestedLoopFedByStartKeepsInitialVars is the two-level analogue of
// TestResumeLoopFedByStartKeepsInitialVars, mirroring the reported production
// topology exactly: an outer loop fed by the main start, an inner loop nested in
// the outer loop's body, and an If-Else inside the INNER loop referencing an
// initial variable ({{seed}}, like the reported {{MultiWorker}}).
//
// On resume both loop scopes are rebuilt at their persisted round (step-level
// resume) and the in-flight round is re-seeded from its entry snapshot. That
// entry snapshot is derived, for the round-0 outer loop, from the start-sourced
// in-edge — so the initial-variable injection still applies and the variable
// propagates DOWN through both rebuilt loop scopes (outer accumSnapshot → inner
// accumSnapshot → If-Else visible set) so the deeply-nested condition resolves
// after 恢复. Without the fix the condition fails with
// "{{seed}} is unknown at evaluation time".
func TestResumeNestedLoopFedByStartKeepsInitialVars(t *testing.T) {
	uniqueMemoryRoot(t)
	workdir := t.TempDir()
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	// Inner body fails on its first invocation (run ends "failed") then succeeds
	// after resume; the counter file persists across the resume.
	body := `n=$(cat .body_counter 2>/dev/null || echo 0)
n=$((n+1))
echo $n > .body_counter
if [ "$n" -eq 1 ]; then exit 1; fi
echo ok`
	cfg := model.GraphConfig{
		Workdir:   workdir,
		Variables: map[string]string{"seed": "1"},
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			loopFixed("outer", 1, 0),
			// outer loop body: start → inner loop → end
			childStart("os", "outer"),
			loopFixedChild("inner", "outer", 1, 0),
			childEnd("oe", "outer"),
			// inner loop body: start → ifElse → (body|skip) → end
			childStart("is", "inner"),
			ifElseInLoop("cond", "inner", `{{seed}} == "1"`),
			childShell("body", "inner", body),
			childShell("skip", "inner", "echo skipped"),
			childEnd("ie", "inner"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_outer", "s", "outer"),
			edge("os_inner", "os", "inner"),
			edge("inner_oe", "inner", "oe"),
			edge("is_cond", "is", "cond"),
			portEdge("cond_body", "cond", "body", model.GraphEdgePortYes),
			portEdge("cond_skip", "cond", "skip", model.GraphEdgePortNo),
			edge("body_ie", "body", "ie"),
			edge("skip_ie", "skip", "ie"),
			edge("outer_e", "outer", "e"),
		},
	}
	runID := mustStart(t, svc, cfg, "job-resume-nested-seed")
	waitGraphRunStatus(t, svc, runID, model.GraphRunStatusFailed)

	if _, err := svc.ResumeRun(context.Background(), runID, stubGraphRunner{}, nil); err != nil {
		t.Fatalf("ResumeRun failed: %v", err)
	}
	got := waitGraphRunStatus(t, svc, runID, model.GraphRunStatusCompleted)
	if got.Run.Status != model.GraphRunStatusCompleted {
		t.Fatalf("run status = %s, want completed (lastErr=%q)", got.Run.Status, got.Run.Progress.LastError)
	}
	// The inner If-Else must have routed to the yes branch ({{seed}}=="1") on the
	// resumed round: body executed, skip pruned. The pre-fix bug would have lost
	// {{seed}} two scopes deep and failed the condition outright.
	bodyInst, ok := instByNode(got, "body")
	if !ok || bodyInst.Status != model.GraphInstanceStatusSucceeded {
		t.Fatalf("body status = %v (ok=%v), want succeeded", bodyInst.Status, ok)
	}
	skipInst, ok := instByNode(got, "skip")
	if !ok || skipInst.Status != model.GraphInstanceStatusSkipped {
		t.Fatalf("skip status = %v (ok=%v), want skipped; {{seed}} should still equal 1 two scopes deep after resume", skipInst.Status, ok)
	}
}

// loopFixedChild builds a fixed-count loop node nested inside another container.
func loopFixedChild(id, parent string, count, maxIters int) model.GraphNode {
	n := loopFixed(id, count, maxIters)
	n.ParentID = parent
	return n
}

// childPrompt builds a Prompt (Agent) node inside a loop container. Its prompt
// text is "do <id>" so countingRunner can count per-node executions across
// rounds and resumes.
func childPrompt(id, parent string) model.GraphNode {
	n := promptNode(id)
	n.ParentID = parent
	return n
}

// TestResumeFixedLoopPreservesCompletedSteps is the core step-level resume
// regression: a loop body A→B where B fails on its first attempt. On resume the
// loop scope is rebuilt at its current round and ONLY the failed step (and its
// downstream) re-runs — the already-succeeded sibling step A is NOT re-run (no
// new Agent session, no duplicate side effect). Pre-change this whole loop was
// reset wholesale and A re-ran.
func TestResumeFixedLoopPreservesCompletedSteps(t *testing.T) {
	uniqueMemoryRoot(t)
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	// Loop body: ls → A(prompt) → B(prompt, fails first time) → le. Fixed 1 round.
	cfg := model.GraphConfig{
		Workdir: t.TempDir(),
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			loopFixed("lp", 1, 0),
			childStart("ls", "lp"),
			childPrompt("A", "lp"),
			childPrompt("B", "lp"),
			childEnd("le", "lp"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_lp", "s", "lp"),
			edge("ls_A", "ls", "A"),
			edge("A_B", "A", "B"),
			edge("B_le", "B", "le"),
			edge("lp_e", "lp", "e"),
		},
	}
	runner := newCountingRunner()
	runner.failNode["do B"] = true
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-step-resume", Config: &cfg}, runner, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusFailed)
	if runner.callCount("do A") != 1 {
		t.Fatalf("A should have run once before failure, got %d", runner.callCount("do A"))
	}

	runner.mu.Lock()
	runner.failNode["do B"] = false
	runner.mu.Unlock()
	if _, err := svc.ResumeRun(context.Background(), run.ID, runner, nil); err != nil {
		t.Fatalf("ResumeRun failed: %v", err)
	}
	got := waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusCompleted)
	if got.Run.Status != model.GraphRunStatusCompleted {
		t.Fatalf("run status = %s, want completed (lastErr=%q)", got.Run.Status, got.Run.Progress.LastError)
	}
	// The whole point: A — succeeded in the failed round — must NOT re-run.
	if c := runner.callCount("do A"); c != 1 {
		t.Fatalf("A must NOT re-run on step-level resume; got %d calls (want 1)", c)
	}
	// B re-runs exactly once (fail + resume = 2 total).
	if c := runner.callCount("do B"); c != 2 {
		t.Fatalf("B should have run twice (fail + resume), got %d", c)
	}
	if got.Progress.CompletedCount != got.Progress.TotalCount {
		t.Fatalf("expected completed==total, got %+v", got.Progress)
	}
}

// TestResumeFixedLoopPreservesCompletedRounds: a 3-round loop whose body fails in
// the LAST round. On resume the two completed rounds are NOT re-run; only the
// failed round's step re-runs. Uses a shell counter so each round's body
// execution is observable, plus a flag file to fail exactly once in round 2.
func TestResumeFixedLoopPreservesCompletedRounds(t *testing.T) {
	uniqueMemoryRoot(t)
	workdir := t.TempDir()
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	runs := workdir + "/runs.txt"
	// Append a marker every execution; fail once when this is the 3rd round
	// (QUARTET_LOOP_INDEX==2) and no prior failure flag exists.
	body := `echo "$QUARTET_LOOP_INDEX" >> ` + runs + `
if [ "$QUARTET_LOOP_INDEX" = "2" ] && [ ! -f .failed_once ]; then touch .failed_once; exit 1; fi
echo ok`
	cfg := model.GraphConfig{
		Workdir: workdir,
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			loopFixed("lp", 3, 0),
			childStart("ls", "lp"),
			childShell("body", "lp", body),
			childEnd("le", "lp"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_lp", "s", "lp"),
			edge("ls_body", "ls", "body"),
			edge("body_le", "body", "le"),
			edge("lp_e", "lp", "e"),
		},
	}
	runID := mustStart(t, svc, cfg, "job-rounds-resume")
	waitGraphRunStatus(t, svc, runID, model.GraphRunStatusFailed)

	// Before resume: rounds 0,1 ran ok and round 2 ran once (and failed) → 3 lines.
	linesBefore := countLines(t, runs)
	if linesBefore != 3 {
		t.Fatalf("expected 3 body executions before resume (rounds 0,1,2-fail), got %d", linesBefore)
	}

	if _, err := svc.ResumeRun(context.Background(), runID, stubGraphRunner{}, nil); err != nil {
		t.Fatalf("ResumeRun failed: %v", err)
	}
	got := waitGraphRunStatus(t, svc, runID, model.GraphRunStatusCompleted)
	if got.Run.Status != model.GraphRunStatusCompleted {
		t.Fatalf("run status = %s, want completed (lastErr=%q)", got.Run.Status, got.Run.Progress.LastError)
	}
	// After resume: only round 2 re-runs once → exactly one more line (total 4).
	// Wholesale reset would have re-run rounds 0,1,2 → 3 more lines (total 6).
	linesAfter := countLines(t, runs)
	if linesAfter != 4 {
		t.Fatalf("expected exactly 1 more body execution on resume (only the failed round), got %d more (total %d)", linesAfter-linesBefore, linesAfter)
	}
}

// TestResumeUntilLoopPreservesCompletedRounds: an until-mode loop that fails in a
// middle round resumes without re-running completed rounds and still terminates
// on its condition with the correct accumulated variable.
func TestResumeUntilLoopPreservesCompletedRounds(t *testing.T) {
	uniqueMemoryRoot(t)
	workdir := t.TempDir()
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	counter := workdir + "/c.txt"
	runs := workdir + "/runs.txt"
	// Each round increments n (line count). Fail once at round index 1.
	body := `echo x >> ` + counter + `
echo "$QUARTET_LOOP_INDEX" >> ` + runs + `
quartet_set n "$(wc -l < ` + counter + ` | tr -d ' ')"
if [ "$QUARTET_LOOP_INDEX" = "1" ] && [ ! -f .failed_once ]; then touch .failed_once; exit 1; fi
echo ok`
	cfg := model.GraphConfig{
		Workdir: workdir,
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			loopUntil("lp", `{{n}} == "3"`, 100),
			childShell("body", "lp", body, "n"),
			childStart("ls", "lp"),
			childEnd("le", "lp"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_lp", "s", "lp"),
			edge("ls_body", "ls", "body"),
			edge("body_le", "body", "le"),
			edge("lp_e", "lp", "e"),
		},
	}
	runID := mustStart(t, svc, cfg, "job-until-resume")
	waitGraphRunStatus(t, svc, runID, model.GraphRunStatusFailed)
	// Round 0 ok, round 1 failed → counter has 2 lines, runs has 2 lines.
	if got := countLines(t, counter); got != 2 {
		t.Fatalf("expected counter=2 before resume, got %d", got)
	}

	if _, err := svc.ResumeRun(context.Background(), runID, stubGraphRunner{}, nil); err != nil {
		t.Fatalf("ResumeRun failed: %v", err)
	}
	got := waitGraphRunStatus(t, svc, runID, model.GraphRunStatusCompleted)
	if got.Run.Status != model.GraphRunStatusCompleted {
		t.Fatalf("run status = %s, want completed (lastErr=%q)", got.Run.Status, got.Run.Progress.LastError)
	}
	lp, _ := instByNode(got, "lp")
	if lp.VisibleVariables["n"] != "3" {
		t.Fatalf("loop external snapshot n = %q, want 3", lp.VisibleVariables["n"])
	}
	// Round 0 must not re-run: round 1 re-runs (counter→3 via the failed round
	// retry) then round 2 runs and the condition n==3 stops it. The counter
	// reaching exactly 3 (not 4) proves round 0 did not re-increment.
	if got := countLines(t, counter); got != 3 {
		t.Fatalf("counter = %d, want 3 (round 0 must not re-run on resume)", got)
	}
}

// TestResumeLoopStateNonEmptyOnFailure guards step 1: a loop-body failure must
// persist the in-flight loop scope into run.Resume.LoopState so resume can
// rebuild it. Without this the run would fall back to wholesale reset.
func TestResumeLoopStateNonEmptyOnFailure(t *testing.T) {
	uniqueMemoryRoot(t)
	workdir := t.TempDir()
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	body := `if [ "$QUARTET_LOOP_INDEX" = "1" ]; then exit 1; fi
echo ok`
	cfg := model.GraphConfig{
		Workdir: workdir,
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			loopFixed("lp", 3, 0),
			childStart("ls", "lp"),
			childShell("body", "lp", body),
			childEnd("le", "lp"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_lp", "s", "lp"),
			edge("ls_body", "ls", "body"),
			edge("body_le", "body", "le"),
			edge("lp_e", "lp", "e"),
		},
	}
	runID := mustStart(t, svc, cfg, "job-loopstate")
	waitGraphRunStatus(t, svc, runID, model.GraphRunStatusFailed)

	impl := svc.(*serviceImpl)
	run, err := impl.runRepo.GetRun(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetRun failed: %v", err)
	}
	if run.Resume == nil || len(run.Resume.LoopState) == 0 {
		t.Fatal("expected Resume.LoopState to be persisted on loop-body failure")
	}
	ls, ok := run.Resume.LoopState["lp"]
	if !ok {
		t.Fatalf("expected loop state for container 'lp', got keys %v", keysOf(run.Resume.LoopState))
	}
	if ls.CurrentIteration != 1 {
		t.Fatalf("loop state CurrentIteration = %d, want 1 (failed in round index 1)", ls.CurrentIteration)
	}
}

// countLines returns the number of newline-terminated lines in a file, or 0 if
// the file does not exist.
func countLines(t *testing.T, path string) int {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read %s failed: %v", path, err)
	}
	return strings.Count(string(data), "\n")
}

// keysOf returns the keys of a loop-state map for diagnostics.
func keysOf(m map[string]model.GraphLoopState) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}

// TestResumeNestedLoopPreservesCompletedInnerRounds: a 2×3 nested loop whose
// inner body fails once at outer round 1 / inner round 1. On resume neither
// outer round 0 (all 3 inner bodies) nor outer round 1's already-completed inner
// rounds re-run — only the failed inner body and the remaining inner rounds run.
// Wholesale reset would re-run the entire outer loop (all 6 inner bodies again).
func TestResumeNestedLoopPreservesCompletedInnerRounds(t *testing.T) {
	uniqueMemoryRoot(t)
	workdir := t.TempDir()
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	runs := workdir + "/runs.txt"
	// Every inner-body execution appends "<outer>:<inner>". The engine exposes
	// only the innermost loop's QUARTET_LOOP_INDEX, so the outer index is tracked
	// via a separate per-outer-round marker file written by the outer body entry.
	// Simpler: fail once when a flag file is absent and we are on the 5th total
	// execution (outer1/inner1) — deterministic given strict round ordering.
	body := `n=$(cat .exec_counter 2>/dev/null || echo 0); n=$((n+1)); echo $n > .exec_counter
echo "$n" >> ` + runs + `
if [ "$n" = "5" ] && [ ! -f .failed_once ]; then touch .failed_once; exit 1; fi
echo ok`
	cfg := model.GraphConfig{
		Workdir: workdir,
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			loopFixed("outer", 2, 0),
			childStart("outer_start", "outer"),
			loopFixedChild("inner", "outer", 3, 0),
			childEnd("outer_end", "outer"),
			childStart("inner_start", "inner"),
			childShell("ibody", "inner", body),
			childEnd("inner_end", "inner"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_outer", "s", "outer"),
			edge("outerstart_inner", "outer_start", "inner"),
			edge("inner_outerend", "inner", "outer_end"),
			edge("innerstart_ibody", "inner_start", "ibody"),
			edge("ibody_innerend", "ibody", "inner_end"),
			edge("outer_e", "outer", "e"),
		},
	}
	runID := mustStart(t, svc, cfg, "job-nested-resume")
	waitGraphRunStatus(t, svc, runID, model.GraphRunStatusFailed)
	// 5 executions happened before failure (4 ok + the 5th failing).
	if got := countLines(t, runs); got != 5 {
		t.Fatalf("expected 5 inner-body executions before resume, got %d", got)
	}

	if _, err := svc.ResumeRun(context.Background(), runID, stubGraphRunner{}, nil); err != nil {
		t.Fatalf("ResumeRun failed: %v", err)
	}
	got := waitGraphRunStatus(t, svc, runID, model.GraphRunStatusCompleted)
	if got.Run.Status != model.GraphRunStatusCompleted {
		t.Fatalf("run status = %s, want completed (lastErr=%q)", got.Run.Status, got.Run.Progress.LastError)
	}
	// Resume re-runs only the failed inner body (outer1/inner1) + the last inner
	// round (outer1/inner2): 2 more executions → total 7. The 4 completed inner
	// bodies (outer0×3 + outer1/inner0) are preserved. Wholesale reset → 6 more.
	if got := countLines(t, runs); got != 7 {
		t.Fatalf("expected 2 more inner-body executions on resume (total 7), got total %d", got)
	}
	// Final: 6 distinct succeeded inner-body instances (2×3).
	succeeded := 0
	for _, in := range got.Instances {
		if in.NodeID == "ibody" && in.Status == model.GraphInstanceStatusSucceeded {
			succeeded++
		}
	}
	if succeeded != 6 {
		t.Fatalf("succeeded inner-body instances = %d, want 6", succeeded)
	}
}
