package graph

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	jobsvc "github.com/fanlv/quartet/services/job"
	"github.com/fanlv/quartet/services/usagestats"
	"github.com/fanlv/quartet/services/workspace"
	"github.com/fanlv/quartet/types/agui"
	"github.com/fanlv/quartet/types/consts"
	"github.com/fanlv/quartet/types/model"
	typepath "github.com/fanlv/quartet/types/path"
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

func (s *stubGraphJobSink) AttachGraphSession(context.Context, string, string) error {
	return nil
}

func (s *stubGraphJobSink) JobTitle(context.Context, string) string { return "" }

type failingGraphJobSink struct {
	err error
}

func (s failingGraphJobSink) SetGraphRunState(context.Context, string, string, model.JobStatus, int64, int64) error {
	return s.err
}

func (s failingGraphJobSink) ClearGraphRunLinkage(context.Context, string, string) error {
	return nil
}

func (s failingGraphJobSink) AttachGraphSession(context.Context, string, string) error {
	return nil
}

func (s failingGraphJobSink) JobTitle(context.Context, string) string { return "" }

func TestStartRunMissingWorkflowAndConfigIsBadRequest(t *testing.T) {
	uniqueMemoryRoot(t)
	svc, err := NewService()
	if err != nil {
		t.Fatalf("graph service failed: %v", err)
	}
	_, err = svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-1"}, stubGraphRunner{}, nil)
	if !errors.Is(err, ErrWorkflowBadRequest) {
		t.Fatalf("StartRun err = %v, want ErrWorkflowBadRequest", err)
	}
	if got := err.Error(); got != "invalid graph workflow request: workflowId or config is required" {
		t.Fatalf("StartRun err text = %q", got)
	}
}

func TestCreateRunJobResolvesWorkflowWorkspaceBeforeDefaulting(t *testing.T) {
	uniqueMemoryRoot(t)
	defaultWorkdir := filepath.Join(typepath.LocalWorkspaceDir(consts.DefaultWorkspaceID), "files")
	if err := os.MkdirAll(defaultWorkdir, 0755); err != nil {
		t.Fatalf("mkdir default workdir failed: %v", err)
	}
	workflowWorkdir := filepath.Join(t.TempDir(), "workflow-workdir")
	if err := os.MkdirAll(workflowWorkdir, 0755); err != nil {
		t.Fatalf("mkdir workflow workdir failed: %v", err)
	}
	wsSvc, err := workspace.NewService()
	if err != nil {
		t.Fatalf("workspace service failed: %v", err)
	}
	workflowWS := model.NewWorkspace("Workflow", "", workflowWorkdir)
	workflowWS.ID = "ws-workflow"
	workflowWS.Workdir = workflowWorkdir
	if err := wsSvc.Create(workflowWS); err != nil {
		t.Fatalf("create workflow workspace failed: %v", err)
	}
	jobs, err := jobsvc.NewService(wsSvc)
	if err != nil {
		t.Fatalf("job service failed: %v", err)
	}
	svc, err := NewService()
	if err != nil {
		t.Fatalf("graph service failed: %v", err)
	}
	created, err := svc.CreateWorkflow(context.Background(), &model.CreateGraphWorkflowRequest{
		Name:        "workflow workspace",
		WorkspaceID: workflowWS.ID,
		Config: model.GraphConfig{
			WorkspaceID: workflowWS.ID,
			Workdir:     workflowWorkdir,
			Nodes: []model.GraphNode{
				node("s", model.GraphNodeTypeStart),
				{ID: "sh", Type: model.GraphNodeTypeShell, Config: model.GraphNodeConfig{Script: "echo ok"}},
				node("e", model.GraphNodeTypeEnd),
			},
			Edges: []model.GraphEdge{
				edge("e1", "s", "sh"),
				edge("e2", "sh", "e"),
			},
		},
	})
	if err != nil {
		t.Fatalf("create workflow failed: %v", err)
	}
	req := &model.StartGraphRunRequest{WorkflowID: created.ID}
	j, err := svc.CreateRunJob(context.Background(), req, jobs, wsSvc)
	if err != nil {
		t.Fatalf("CreateRunJob failed: %v", err)
	}
	if j.WorkspaceID != workflowWS.ID || req.WorkspaceID != workflowWS.ID {
		t.Fatalf("workspace = job %q req %q, want %q", j.WorkspaceID, req.WorkspaceID, workflowWS.ID)
	}
	if j.Workdir != workflowWorkdir || req.Workdir != workflowWorkdir {
		t.Fatalf("workdir = job %q req %q, want %q", j.Workdir, req.Workdir, workflowWorkdir)
	}
}

func TestStartRunCleansArtifactsWhenExplicitJobBindFails(t *testing.T) {
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
			{ID: "sh", Type: model.GraphNodeTypeShell, Config: model.GraphNodeConfig{Script: "echo ok"}},
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("e1", "s", "sh"),
			edge("e2", "sh", "e"),
		},
	}
	bindErr := errors.New("bind failed")
	_, err = svc.StartRun(context.Background(), &model.StartGraphRunRequest{
		JobID:  "job-explicit",
		Config: &cfg,
	}, stubGraphRunner{}, failingGraphJobSink{err: bindErr})
	if !errors.Is(err, bindErr) {
		t.Fatalf("StartRun err = %v, want bind error", err)
	}
	if _, statErr := os.Stat(filepath.Join(root, "quartet", "data", "workspaces", consts.DefaultWorkspaceID, "jobs", "job-explicit", "graph_run")); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("graph_run dir stat err = %v, want not exist", statErr)
	}
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
	if _, err := os.Stat(filepath.Join(root, "quartet", "data", "workspaces", run.WorkspaceID, "jobs", run.JobID, "graph_run", "run.json")); err != nil {
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

type streamingGraphRunner struct {
	stubSnapshotSource
	// gate, when non-nil, blocks RunIteration until closed — lets a test attach
	// an SSE subscriber before the streaming (never-persisted) events are
	// produced, so they aren't GC'd before the subscriber sees them.
	gate <-chan struct{}
}

func (streamingGraphRunner) InitSession(context.Context, string, *model.SessionOverrides) (string, error) {
	return "session-stream", nil
}

func (r streamingGraphRunner) RunIteration(_ context.Context, _ string, _ []*schema.Message, handler agui.EventHandler) error {
	if r.gate != nil {
		<-r.gate
	}
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
	gate := make(chan struct{})
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-stream", Config: &cfg}, streamingGraphRunner{gate: gate}, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}

	// Streaming agent events (message/thought/tool deltas) are no longer
	// persisted to events.jsonl — they flow only through the in-memory event
	// buffer for real-time SSE delivery (the authoritative conversation lives in
	// the node's session messages). Attach a subscriber at the buffer's current
	// tail BEFORE releasing the runner gate, then collect concurrently, so the
	// never-persisted streaming events are observed as they are produced.
	// (Subscribing from seq 0 would race the start-node seeding's structural
	// events, which GC reclaims before any reader attaches.)
	var reader GraphEventReader
	for i := 0; i < 200; i++ {
		tail, ok := svc.RunEventSnapshotSeq(run.ID)
		if !ok {
			time.Sleep(5 * time.Millisecond)
			continue
		}
		r, live, err := svc.SubscribeRunEvents(run.ID, tail)
		if err != nil {
			// Snapshot/subscribe race: GC advanced past tail between the two
			// calls (no reader attached yet). Retry with a fresher tail.
			time.Sleep(5 * time.Millisecond)
			continue
		}
		if live {
			reader = r
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	if reader == nil {
		t.Fatal("run buffer never became live")
	}
	collectDone := make(chan map[model.GraphEventType]model.GraphEvent, 1)
	go func() {
		seen := map[model.GraphEventType]model.GraphEvent{}
		for {
			entries, status := reader.ReadWithTimeout(context.Background(), 200*time.Millisecond, 64)
			for _, e := range entries {
				if e.Seq > 0 {
					reader.Ack(e.Seq)
				}
				if e.Event != nil {
					seen[e.Event.Type] = *e.Event
				}
			}
			if status == GraphReadClosed {
				break
			}
			if status == GraphReadTimeout {
				if st, err := svc.GetRunStatus(context.Background(), run.ID); err == nil &&
					st.Run != nil && graphRunTerminalForTest(st.Run.Status) {
					break
				}
			}
		}
		collectDone <- seen
	}()
	close(gate) // release the runner now that a subscriber is attached

	got := waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusCompleted)
	var seen map[model.GraphEventType]model.GraphEvent
	select {
	case seen = <-collectDone:
	case <-time.After(3 * time.Second):
		reader.Close()
		t.Fatal("buffer collection did not finish")
	}
	reader.Close()
	// The PERSISTED event log must NOT carry the streaming events — they flow
	// only through the in-memory buffer. Read the persisted log directly.
	persisted, err := svc.ListRunEvents(context.Background(), run.ID, 0, nil)
	if err != nil {
		t.Fatalf("ListRunEvents failed: %v", err)
	}
	for _, ev := range persisted.Events {
		switch ev.Type {
		case model.GraphEventTypeAgentMessageStart, model.GraphEventTypeAgentMessageDelta,
			model.GraphEventTypeAgentMessageEnd, model.GraphEventTypeAgentThoughtStart,
			model.GraphEventTypeAgentThoughtDelta, model.GraphEventTypeAgentThoughtEnd,
			model.GraphEventTypeAgentToolStart, model.GraphEventTypeAgentToolArgs,
			model.GraphEventTypeAgentToolResult, model.GraphEventTypeAgentToolEnd,
			model.GraphEventTypeAgentTokenUsage:
			t.Fatalf("streaming event %s must not be persisted, but found it in the event log", ev.Type)
		}
	}
	// The status response exposes a count, not the bodies.
	if got.EventCount <= 0 {
		t.Fatalf("GetRunStatus EventCount = %d, want > 0", got.EventCount)
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
			t.Fatalf("missing graph event type %s in buffered events %#v", typ, seen)
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

// TestIsPersistableGraphEvent locks the persistence policy: agent streaming
// deltas must never reach events.jsonl (their authoritative copy lives in node
// session messages), while every structural event type must persist so resume /
// audit / post-restart replay can rebuild the run.
func TestIsPersistableGraphEvent(t *testing.T) {
	streaming := []model.GraphEventType{
		model.GraphEventTypeAgentMessageStart, model.GraphEventTypeAgentMessageDelta,
		model.GraphEventTypeAgentMessageEnd, model.GraphEventTypeAgentThoughtStart,
		model.GraphEventTypeAgentThoughtDelta, model.GraphEventTypeAgentThoughtEnd,
		model.GraphEventTypeAgentToolStart, model.GraphEventTypeAgentToolArgs,
		model.GraphEventTypeAgentToolResult, model.GraphEventTypeAgentToolEnd,
		model.GraphEventTypeAgentTokenUsage,
	}
	for _, typ := range streaming {
		if isPersistableGraphEvent(typ) {
			t.Errorf("streaming event %s must not be persistable", typ)
		}
	}
	structural := []model.GraphEventType{
		model.GraphEventTypeInstanceStarted, model.GraphEventTypeInstanceCompleted,
		model.GraphEventTypeInstanceFailed, model.GraphEventTypeInstanceSkipped,
		model.GraphEventTypeEdgeResolved, model.GraphEventTypeVariableWritten,
		model.GraphEventTypeLoopIteration, model.GraphEventTypeProgressUpdated,
		model.GraphEventTypeLog, model.GraphEventTypeError,
	}
	for _, typ := range structural {
		if !isPersistableGraphEvent(typ) {
			t.Errorf("structural event %s must be persistable", typ)
		}
	}
}
