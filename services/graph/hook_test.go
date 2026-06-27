package graph

import (
	"context"
	"testing"
	"time"

	"github.com/fanlv/quartet/types/model"
)

// waitHookResult polls ListHookResults until a result for nodeID appears (the
// hook runs in a detached goroutine joined only when the run goroutine exits, so
// it may land just after the run reaches its terminal status).
func waitHookResult(t *testing.T, svc Service, runID, nodeID string) model.GraphHookResult {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		resp, err := svc.ListHookResults(context.Background(), runID)
		if err != nil {
			t.Fatalf("ListHookResults failed: %v", err)
		}
		for _, r := range resp.Results {
			if r.NodeID == nodeID {
				return r
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("hook result for node %s did not appear", nodeID)
	return model.GraphHookResult{}
}

func startRunWithEndHook(t *testing.T, script string) (Service, string) {
	t.Helper()
	uniqueMemoryRoot(t)
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	cfg := model.GraphConfig{
		Workdir: t.TempDir(),
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			node("sh", model.GraphNodeTypeShell),
			{ID: "e", Type: model.GraphNodeTypeEnd, Config: model.GraphNodeConfig{
				EndHookMode: model.GraphEndHookModeCustom,
				HookScript:  script,
			}},
		},
		Edges: []model.GraphEdge{
			edge("e1", "s", "sh"),
			edge("e2", "sh", "e"),
		},
	}
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{
		JobID:  "job-1",
		Config: &cfg,
	}, stubGraphRunner{}, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusCompleted)
	return svc, run.ID
}

// TestEndHookSuccessSurfacesResult locks the happy path: a custom End hook that
// exits 0 produces a hookCompleted result with exit code 0 and its stdout.
func TestEndHookSuccessSurfacesResult(t *testing.T) {
	svc, runID := startRunWithEndHook(t, "echo hello-hook")
	res := waitHookResult(t, svc, runID, "e")
	if res.Status != "completed" {
		t.Fatalf("status = %q, want completed", res.Status)
	}
	if res.ExitCode == nil || *res.ExitCode != 0 {
		t.Fatalf("exitCode = %v, want 0", res.ExitCode)
	}
	if res.Source != "end" {
		t.Fatalf("source = %q, want end", res.Source)
	}
	if res.NodeType != model.GraphNodeTypeEnd {
		t.Fatalf("nodeType = %q, want end", res.NodeType)
	}
	if res.Stdout != "hello-hook\n" {
		t.Fatalf("stdout = %q, want hello-hook\\n", res.Stdout)
	}
}

// TestEndHookFailureSurfacesResult locks the failure path that motivated this
// feature: a hook whose command fails (here a non-zero exit with stderr) is
// surfaced as a hookFailed result carrying the exit code and stderr, instead of
// being silently swallowed into the log.
func TestEndHookFailureSurfacesResult(t *testing.T) {
	svc, runID := startRunWithEndHook(t, "echo boom 1>&2\nexit 2")
	res := waitHookResult(t, svc, runID, "e")
	if res.Status != "failed" {
		t.Fatalf("status = %q, want failed", res.Status)
	}
	if res.ExitCode == nil || *res.ExitCode != 2 {
		t.Fatalf("exitCode = %v, want 2", res.ExitCode)
	}
	if res.Stderr != "boom\n" {
		t.Fatalf("stderr = %q, want boom\\n", res.Stderr)
	}
}
