package graph

import (
	"context"
	"testing"
	"time"

	"github.com/fanlv/quartet/types/model"
)

// TestReconcileMarksRunningInterrupted simulates a process crash: a run left in
// "running" with a running instance is reconciled to "recovering" with the
// instance interrupted, and nothing is re-executed.
func TestReconcileMarksRunningInterrupted(t *testing.T) {
	uniqueMemoryRoot(t)
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	impl := svc.(*serviceImpl)

	runID := model.NewGraphRunID()
	run := &model.GraphRun{
		ID:     runID,
		JobID:  "job-1",
		Status: model.GraphRunStatusRunning,
		BaseSnapshot: model.GraphRunSnapshot{
			Config: linearShellCfg(t),
		},
		CurrentVersion: 1,
		StartedAt:      time.Now().UnixMilli(),
		Progress:       &model.GraphProgress{TotalCount: 1},
	}
	if err := impl.runRepo.SaveRun(context.Background(), run); err != nil {
		t.Fatalf("SaveRun failed: %v", err)
	}
	instances := map[string]model.GraphInstanceState{
		"sh": {Key: model.GraphInstanceKey{NodeID: "sh"}, NodeID: "sh", NodeType: model.GraphNodeTypeShell, Status: model.GraphInstanceStatusRunning},
	}
	if err := impl.runRepo.SaveInstances(context.Background(), runID, instances); err != nil {
		t.Fatalf("SaveInstances failed: %v", err)
	}

	sink := &stubGraphJobSink{updates: make(chan graphJobUpdate, 8)}
	if err := svc.ReconcileRuns(context.Background(), sink); err != nil {
		t.Fatalf("ReconcileRuns failed: %v", err)
	}

	got, err := svc.GetRunStatus(context.Background(), runID)
	if err != nil {
		t.Fatalf("GetRunStatus failed: %v", err)
	}
	if got.Run.Status != model.GraphRunStatusRecovering {
		t.Fatalf("run status = %s, want recovering", got.Run.Status)
	}
	sh, ok := instByNode(got, "sh")
	if !ok || sh.Status != model.GraphInstanceStatusInterrupted {
		t.Fatalf("sh status = %v (ok=%v), want interrupted", sh.Status, ok)
	}
	if sh.BlockedReason == "" {
		t.Fatal("interrupted instance should record a blocked reason")
	}
}

// TestReconcileSkipsTerminalRuns verifies a completed run is untouched.
func TestReconcileSkipsTerminalRuns(t *testing.T) {
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

	if err := svc.ReconcileRuns(context.Background(), nil); err != nil {
		t.Fatalf("ReconcileRuns failed: %v", err)
	}
	got, err := svc.GetRunStatus(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetRunStatus failed: %v", err)
	}
	if got.Run.Status != model.GraphRunStatusCompleted {
		t.Fatalf("completed run was altered by reconcile: %s", got.Run.Status)
	}
}
