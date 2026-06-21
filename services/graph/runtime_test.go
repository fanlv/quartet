package graph

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/services/usagestats"
	"github.com/fanlv/quartet/types/agui"
	"github.com/fanlv/quartet/types/model"
)

type stubGraphRunner struct{ stubSnapshotSource }

func (stubGraphRunner) InitSession(context.Context, string, *model.SessionOverrides) (string, error) {
	return "session-1", nil
}

func (stubGraphRunner) RunIteration(context.Context, string, []*schema.Message, agui.EventHandler) error {
	return nil
}

func (stubGraphRunner) SessionModelID(string) string { return "" }

// stubSnapshotSource satisfies the snapshot-content half of the Runner interface
// for tests: no model resolves and the system prompt is empty. Embed it in any
// test runner that does not exercise snapshot capture.
type stubSnapshotSource struct{}

func (stubSnapshotSource) ResolveModelSnapshot(context.Context, string) (model.ModelInstance, bool) {
	return model.ModelInstance{}, false
}

func (stubSnapshotSource) ResolveSystemPrompt(context.Context) (string, error) { return "", nil }

type stubGraphJobSink struct {
	updates chan graphJobUpdate
}

type graphJobUpdate struct {
	status model.JobStatus
	runID  string
}

func (s *stubGraphJobSink) SetGraphRunState(_ context.Context, _ string, graphRunID string, status model.JobStatus, _, _ int64) error {
	if s.updates != nil {
		s.updates <- graphJobUpdate{runID: graphRunID, status: status}
	}
	return nil
}

func (s *stubGraphJobSink) ClearGraphRunLinkage(context.Context, string, string) error {
	return nil
}

func TestStartRunLinearShellCompletes(t *testing.T) {
	root := uniqueMemoryRoot(t)
	workdir := t.TempDir()

	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	cfg := model.GraphConfig{
		Workdir: workdir,
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			{ID: "sh", Type: model.GraphNodeTypeShell, Config: model.GraphNodeConfig{
				Script:          "quartet_set answer 42\necho done",
				OutputVariables: []string{"answer"},
			}},
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("e1", "s", "sh"),
			edge("e2", "sh", "e"),
		},
	}
	sink := &stubGraphJobSink{updates: make(chan graphJobUpdate, 8)}
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{
		JobID:  "job-1",
		Config: &cfg,
	}, stubGraphRunner{}, sink)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}

	got := waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusCompleted)
	if got.Progress == nil {
		t.Fatal("progress is nil")
	}
	if got.Progress.TotalCount != 1 || got.Progress.CompletedCount != 1 || got.Progress.FailedCount != 0 {
		t.Fatalf("progress = %+v, want total=1 completed=1 failed=0", got.Progress)
	}
	if len(got.Instances) != 1 {
		t.Fatalf("instances len=%d, want 1", len(got.Instances))
	}
	inst := got.Instances[0]
	if inst.OutputVariables["answer"] != "42" {
		t.Fatalf("answer output=%q, want 42", inst.OutputVariables["answer"])
	}
	if inst.VisibleVariables["answer"] != "42" || inst.VisibleVariables["_last_assistant_msg"] != "done\n" {
		t.Fatalf("visible vars = %+v", inst.VisibleVariables)
	}
	if !waitSinkStatus(sink.updates, run.ID, model.JobStatusCompleted) {
		t.Fatalf("job sink did not see completed update for %s", run.ID)
	}
	if _, err := os.Stat(filepath.Join(root, "agent", "graph_runs", run.ID, "run.json")); err != nil {
		t.Fatalf("run.json not persisted: %v", err)
	}
}

func TestStartRunAcceptsLoopNode(t *testing.T) {
	uniqueMemoryRoot(t)
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	cfg := model.GraphConfig{
		Workdir: t.TempDir(),
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			{ID: "lp", Type: model.GraphNodeTypeLoop, Config: model.GraphNodeConfig{LoopMode: model.GraphLoopModeFixed, FixedCount: 1}},
			{ID: "ls", Type: model.GraphNodeTypeStart, ParentID: "lp"},
			node("body", model.GraphNodeTypeShell),
			{ID: "ie", Type: model.GraphNodeTypeEnd, ParentID: "lp"},
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("e1", "s", "lp"),
			edge("e2", "lp", "e"),
			edge("e0", "ls", "body"),
			edge("e3", "body", "ie"),
		},
	}
	cfg.Nodes[3].ParentID = "lp"
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-1", Config: &cfg}, stubGraphRunner{}, nil)
	if err != nil {
		t.Fatalf("expected loop graph to be accepted, got %v", err)
	}
	got := waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusCompleted)
	if got.Run.Status != model.GraphRunStatusCompleted {
		t.Fatalf("run status = %s, want completed", got.Run.Status)
	}
}

type streamingGraphRunner struct{ stubSnapshotSource }

func (streamingGraphRunner) InitSession(context.Context, string, *model.SessionOverrides) (string, error) {
	return "session-stream", nil
}

func (streamingGraphRunner) RunIteration(_ context.Context, _ string, _ []*schema.Message, handler agui.EventHandler) error {
	_ = handler.OnMessageStart()
	_ = handler.OnMessageDelta("hello")
	_ = handler.OnMessageEnd()
	_ = handler.OnThoughtStart()
	_ = handler.OnThoughtDelta("thinking")
	_ = handler.OnThoughtEnd()
	_ = handler.OnToolCallStart("tool-1", "shell")
	_ = handler.OnToolCallArgs("tool-1", `{"command":"pwd"}`)
	_ = handler.OnToolCallResult("tool-1", "/tmp\n", true)
	_ = handler.OnToolCallEnd("tool-1", true)
	_ = handler.OnTokenUsage(123)
	return nil
}

func (streamingGraphRunner) SessionModelID(string) string { return "model-stream" }

type recordingUsageRecorder struct {
	ch chan usagestats.Snapshot
}

func (r *recordingUsageRecorder) Record(snap usagestats.Snapshot) {
	r.ch <- snap
}

func TestPromptNodeStreamsAgentEventsAndRecordsUsage(t *testing.T) {
	uniqueMemoryRoot(t)
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	recorder := &recordingUsageRecorder{ch: make(chan usagestats.Snapshot, 1)}
	svc.SetUsageRecorder(recorder)
	cfg := model.GraphConfig{
		WorkspaceID: "ws-graph",
		Workdir:     t.TempDir(),
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			{ID: "p", Type: model.GraphNodeTypePrompt, Title: "Prompt", Config: model.GraphNodeConfig{Prompt: "say hi", AgentType: "tester"}},
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_p", "s", "p"),
			edge("p_e", "p", "e"),
		},
	}
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-stream", Config: &cfg}, streamingGraphRunner{}, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	got := waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusCompleted)
	seen := map[model.GraphEventType]model.GraphEvent{}
	for _, ev := range got.Events {
		seen[ev.Type] = ev
	}
	for _, typ := range []model.GraphEventType{
		model.GraphEventTypeAgentMessageStart,
		model.GraphEventTypeAgentMessageDelta,
		model.GraphEventTypeAgentMessageEnd,
		model.GraphEventTypeAgentThoughtStart,
		model.GraphEventTypeAgentThoughtDelta,
		model.GraphEventTypeAgentThoughtEnd,
		model.GraphEventTypeAgentToolStart,
		model.GraphEventTypeAgentToolArgs,
		model.GraphEventTypeAgentToolResult,
		model.GraphEventTypeAgentToolEnd,
		model.GraphEventTypeAgentTokenUsage,
	} {
		if _, ok := seen[typ]; !ok {
			t.Fatalf("missing graph event type %s in %#v", typ, got.Events)
		}
	}
	if got := seen[model.GraphEventTypeAgentMessageDelta].Payload["delta"]; got != "hello" {
		t.Fatalf("message delta payload = %q, want hello", got)
	}
	if got := seen[model.GraphEventTypeAgentTokenUsage].Payload["totalTokens"]; got != "123" {
		t.Fatalf("token usage payload = %q, want 123", got)
	}

	select {
	case snap := <-recorder.ch:
		if snap.WorkspaceID != "ws-graph" || snap.ModelID != "model-stream" {
			t.Fatalf("snapshot attribution = workspace %q model %q", snap.WorkspaceID, snap.ModelID)
		}
		if snap.AssistantCount != 1 || snap.ThoughtCount != 1 || snap.ToolCallCount != 1 {
			t.Fatalf("snapshot counts = assistant %d thought %d tool %d", snap.AssistantCount, snap.ThoughtCount, snap.ToolCallCount)
		}
		if snap.Tokens.Total != 123 {
			t.Fatalf("snapshot total tokens = %d, want 123", snap.Tokens.Total)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("usage snapshot was not recorded")
	}
}

func waitGraphRunStatus(t *testing.T, svc Service, runID string, want model.GraphRunStatus) *model.GraphRunStatusResponse {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		got, err := svc.GetRunStatus(context.Background(), runID)
		if err != nil {
			t.Fatalf("GetRunStatus failed: %v", err)
		}
		if got.Run != nil && got.Run.Status == want {
			return got
		}
		time.Sleep(20 * time.Millisecond)
	}
	got, _ := svc.GetRunStatus(context.Background(), runID)
	t.Fatalf("run did not reach %s, last=%+v", want, got.Run)
	return nil
}

func waitSinkStatus(ch <-chan graphJobUpdate, runID string, want model.JobStatus) bool {
	deadline := time.After(2 * time.Second)
	for {
		select {
		case update := <-ch:
			if update.runID == runID && update.status == want {
				return true
			}
		case <-deadline:
			return false
		}
	}
}

func uniqueMemoryRoot(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), fmt.Sprintf("memory-%d", time.Now().UnixNano()))
	if err := os.MkdirAll(dir, 0755); err != nil {
		t.Fatalf("mkdir memory root failed: %v", err)
	}
	t.Setenv("LOCAL_MEMORY", dir)
	return dir
}
