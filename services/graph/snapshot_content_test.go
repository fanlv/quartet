package graph

import (
	"context"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/types/agui"
	"github.com/fanlv/quartet/types/model"
)

// snapshotRunner is a stubGraphRunner that resolves a fixed model table, so a
// test can assert StartRun freezes referenced Agent/model content into the run
// snapshot.
type snapshotRunner struct {
	models map[string]string
}

func (snapshotRunner) InitSession(context.Context, string, *model.SessionOverrides) (string, error) {
	return "session-1", nil
}

func (snapshotRunner) RunIteration(context.Context, string, []*schema.Message, agui.EventHandler) error {
	return nil
}

func (snapshotRunner) SessionModelID(string) string { return "" }

func (r snapshotRunner) ResolveModelSnapshot(_ context.Context, modelID string) (string, bool) {
	inst, ok := r.models[modelID]
	return inst, ok
}

func promptNodeWithModel(id, modelID, agentType string) model.GraphNode {
	return model.GraphNode{ID: id, Type: model.GraphNodeTypePrompt, Config: model.GraphNodeConfig{
		Prompt:    "do " + id,
		ModelID:   modelID,
		AgentType: agentType,
	}}
}

// TestStartRunCapturesSnapshotContent asserts StartRun freezes the referenced
// model content (keyed by string modelID, deduped across nodes) and the per-node
// Agent snapshot (with the resolved system prompt) into BaseSnapshot, and that
// the baseline version carries the same content.
func TestStartRunCapturesSnapshotContent(t *testing.T) {
	uniqueMemoryRoot(t)
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	cfg := model.GraphConfig{
		Workdir: t.TempDir(),
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			promptNodeWithModel("a", "10", "eino"),
			promptNodeWithModel("b", "10", "eino"), // shares model 10 with a
			shellNode("sh", "echo hi"),             // not an Agent node: no snapshot
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_a", "s", "a"), edge("a_b", "a", "b"),
			edge("b_sh", "b", "sh"), edge("sh_e", "sh", "e"),
		},
	}
	runner := snapshotRunner{
		models: map[string]string{"10": "model-ten"},
	}
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-1", Config: &cfg}, runner, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusCompleted)

	status, err := svc.GetRunStatus(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("GetRunStatus failed: %v", err)
	}
	base := status.Run.BaseSnapshot
	if len(base.ModelSnapshots) != 1 {
		t.Fatalf("expected 1 deduped model snapshot, got %d: %+v", len(base.ModelSnapshots), base.ModelSnapshots)
	}
	if got := base.ModelSnapshots["10"]; got != "model-ten" {
		t.Fatalf("model snapshot content wrong: %q", got)
	}
	if len(base.AgentSnapshots) != 2 {
		t.Fatalf("expected 2 agent snapshots (a,b), got %d: %+v", len(base.AgentSnapshots), base.AgentSnapshots)
	}
	for _, id := range []string{"a", "b"} {
		ag, ok := base.AgentSnapshots[id]
		if !ok {
			t.Fatalf("missing agent snapshot for node %q", id)
		}
		if ag.ModelID != "10" || ag.AgentType != "eino" {
			t.Fatalf("agent snapshot %q wrong: %+v", id, ag)
		}
	}
	// Baseline version carries the same frozen content.
	if len(status.Run.Versions) != 1 || status.Run.Versions[0].Version != 1 {
		t.Fatalf("expected single baseline version, got %+v", status.Run.Versions)
	}
	if status.Run.Versions[0].ModelSnapshots["10"] != "model-ten" {
		t.Fatalf("baseline version missing model content: %+v", status.Run.Versions[0].ModelSnapshots)
	}
}

// TestStartRunDegradedSnapshotDoesNotBlock asserts a node referencing a model
// that does not resolve still starts the run (best-effort snapshot), with no
// model snapshot recorded for the missing id but the agent snapshot still
// present.
func TestStartRunDegradedSnapshotDoesNotBlock(t *testing.T) {
	uniqueMemoryRoot(t)
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	cfg := model.GraphConfig{
		Workdir: t.TempDir(),
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			promptNodeWithModel("a", "999", "eino"), // 999 won't resolve
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{edge("s_a", "s", "a"), edge("a_e", "a", "e")},
	}
	runner := snapshotRunner{models: map[string]string{}}
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-1", Config: &cfg}, runner, nil)
	if err != nil {
		t.Fatalf("StartRun must not fail on missing model: %v", err)
	}
	waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusCompleted)
	status, _ := svc.GetRunStatus(context.Background(), run.ID)
	if len(status.Run.BaseSnapshot.ModelSnapshots) != 0 {
		t.Fatalf("expected no model snapshot for unresolved model, got %+v", status.Run.BaseSnapshot.ModelSnapshots)
	}
	if _, ok := status.Run.BaseSnapshot.AgentSnapshots["a"]; !ok {
		t.Fatalf("agent snapshot should still be captured for node a")
	}
}
