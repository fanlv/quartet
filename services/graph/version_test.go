package graph

import (
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/types/agui"
	"github.com/fanlv/quartet/types/model"
)

// recordingPromptRunner records, per call, the exact prompt content it received,
// so a test can assert which graph version a node executed against. failContent
// forces a failure for a given prompt content (first occurrence latch cleared by
// the test).
type recordingPromptRunner struct {
	stubSnapshotSource
	mu           sync.Mutex
	seen         []string
	failContent  map[string]bool
	blockContent map[string]chan struct{}
}

func newRecordingPromptRunner() *recordingPromptRunner {
	return &recordingPromptRunner{failContent: map[string]bool{}, blockContent: map[string]chan struct{}{}}
}

func (r *recordingPromptRunner) InitSession(context.Context, string, *model.SessionOverrides) (string, error) {
	return "session-1", nil
}

func (r *recordingPromptRunner) RunIteration(ctx context.Context, _ string, messages []*schema.Message, handler agui.EventHandler) error {
	content := ""
	if len(messages) > 0 {
		content = messages[0].Content
	}
	r.mu.Lock()
	r.seen = append(r.seen, content)
	fail := r.failContent[content]
	block := r.blockContent[content]
	r.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	_ = handler.OnMessageDelta("out")
	if fail {
		return errors.New("forced failure for: " + content)
	}
	return nil
}

func (r *recordingPromptRunner) SessionModelID(string) string { return "" }

func (r *recordingPromptRunner) sawContent(want string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, c := range r.seen {
		if c == want {
			return true
		}
	}
	return false
}

func setPrompt(cfg model.GraphConfig, nodeID, prompt string) model.GraphConfig {
	out := cloneGraphConfig(cfg)
	for i := range out.Nodes {
		if out.Nodes[i].ID == nodeID {
			out.Nodes[i].Config.Prompt = prompt
		}
	}
	return out
}

func (r *recordingPromptRunner) blockOn(content string) chan struct{} {
	ch := make(chan struct{})
	r.mu.Lock()
	r.blockContent[content] = ch
	r.mu.Unlock()
	return ch
}

// TestUpdateRunVersionInFlightAppliesToFutureNode asserts a running GraphRun can
// append a new version, and a not-yet-ready downstream instance executes against
// that new version without restarting the scheduler.
func TestUpdateRunVersionInFlightAppliesToFutureNode(t *testing.T) {
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
	runner := newRecordingPromptRunner()
	releaseA := runner.blockOn("do a")
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-1", Config: &cfg}, runner, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	waitRunningCount(t, svc, run.ID, 1)
	edited := setPrompt(cfg, "b", "do b live")
	updated, err := svc.UpdateRunVersion(context.Background(), run.ID, &model.UpdateGraphRunVersionRequest{Config: edited, Reason: "live tweak b"}, stubGraphRunner{})
	if err != nil {
		t.Fatalf("UpdateRunVersion while running failed: %v", err)
	}
	if updated.CurrentVersion != 2 {
		t.Fatalf("expected current version 2, got %d", updated.CurrentVersion)
	}
	close(releaseA)
	got := waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusCompleted)
	if !runner.sawContent("do b live") {
		t.Fatalf("downstream node b must execute against live-edited prompt; seen=%v", runner.seen)
	}
	aInst, ok := instByNode(got, "a")
	if !ok {
		t.Fatalf("missing instance for a")
	}
	if aInst.Version != 1 {
		t.Fatalf("in-flight a should keep version 1, got %d", aInst.Version)
	}
	bInst, ok := instByNode(got, "b")
	if !ok {
		t.Fatalf("missing instance for b")
	}
	if bInst.Version != 2 {
		t.Fatalf("future b should run on version 2, got %d", bInst.Version)
	}
}

func TestUpdateRunVersionNoOpDoesNotAppend(t *testing.T) {
	uniqueMemoryRoot(t)
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	cfg := model.GraphConfig{
		Workdir:      t.TempDir(),
		Variables:    map[string]string{},
		DisabledVars: []string{},
		Canvas:       model.GraphCanvasState{Viewport: model.GraphCanvasViewport{X: 10, Y: 20, Zoom: 1.2}},
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			promptNode("a"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{edge("s_a", "s", "a"), edge("a_e", "a", "e")},
	}
	runner := newRecordingPromptRunner()
	runner.failContent["do a"] = true
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-1", Config: &cfg}, runner, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	got := waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusFailed)
	if got.Run == nil {
		t.Fatalf("missing run snapshot")
	}

	equivalent := cloneGraphConfig(cfg)
	equivalent.Variables = nil
	equivalent.DisabledVars = nil
	equivalent.Canvas = model.GraphCanvasState{}
	updated, err := svc.UpdateRunVersion(context.Background(), got.Run.ID, &model.UpdateGraphRunVersionRequest{Config: equivalent}, stubGraphRunner{})
	if err != nil {
		t.Fatalf("UpdateRunVersion no-op failed: %v", err)
	}
	if updated.CurrentVersion != got.Run.CurrentVersion {
		t.Fatalf("current version = %d, want %d", updated.CurrentVersion, got.Run.CurrentVersion)
	}
	if len(updated.Versions) != len(got.Run.Versions) {
		t.Fatalf("versions len = %d, want %d", len(updated.Versions), len(got.Run.Versions))
	}

	withEmptyBuiltins := cloneGraphConfig(equivalent)
	withEmptyBuiltins.Variables = map[string]string{"Code": "", "Doc": ""}
	updated, err = svc.UpdateRunVersion(context.Background(), got.Run.ID, &model.UpdateGraphRunVersionRequest{Config: withEmptyBuiltins}, stubGraphRunner{})
	if err != nil {
		t.Fatalf("UpdateRunVersion empty builtins no-op failed: %v", err)
	}
	if updated.CurrentVersion != got.Run.CurrentVersion || len(updated.Versions) != len(got.Run.Versions) {
		t.Fatalf("empty builtins appended a version: current=%d versions=%d, want current=%d versions=%d", updated.CurrentVersion, len(updated.Versions), got.Run.CurrentVersion, len(got.Run.Versions))
	}
}

// TestUpdateRunVersionAppendsAndApplies asserts a legal edit (changing the
// prompt of a not-yet-started node) appends a new version, and on resume the
// not-yet-started node executes against the NEW version while the already
// succeeded node is not re-run.
func TestUpdateRunVersionAppendsAndApplies(t *testing.T) {
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
	// b fails the first run so a succeeds but b is resettable; then we edit b and resume.
	runner := newRecordingPromptRunner()
	runner.failContent["do b"] = true
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-1", Config: &cfg}, runner, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusFailed)

	// Edit b's prompt; a (succeeded) is untouched.
	edited := setPrompt(cfg, "b", "do b v2")
	updated, err := svc.UpdateRunVersion(context.Background(), run.ID, &model.UpdateGraphRunVersionRequest{Config: edited, Reason: "tweak b"}, stubGraphRunner{})
	if err != nil {
		t.Fatalf("UpdateRunVersion failed: %v", err)
	}
	if updated.CurrentVersion != 2 || len(updated.Versions) != 2 {
		t.Fatalf("expected version 2 appended, got current=%d versions=%d", updated.CurrentVersion, len(updated.Versions))
	}

	if _, err := svc.ResumeRun(context.Background(), run.ID, runner, nil); err != nil {
		t.Fatalf("ResumeRun failed: %v", err)
	}
	got := waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusCompleted)
	if !runner.sawContent("do b v2") {
		t.Fatalf("resume must execute b against the new version prompt; seen=%v", runner.seen)
	}
	// a succeeded before the edit; it must keep version 1 and not re-run.
	aInst, ok := instByNode(got, "a")
	if !ok {
		t.Fatalf("missing instance for a")
	}
	if aInst.Version != 1 {
		t.Fatalf("a should keep execution-time version 1, got %d", aInst.Version)
	}
	aRuns := 0
	for _, c := range runner.seen {
		if c == "do a" {
			aRuns++
		}
	}
	if aRuns != 1 {
		t.Fatalf("a must run exactly once (not re-run on resume), got %d", aRuns)
	}
}

// TestUpdateRunVersionRejectsFrozenNodeEdit asserts editing the execution config
// of an already-succeeded node, or deleting an edge a completed instance depends
// on, fails with located validation errors.
func TestUpdateRunVersionRejectsFrozenNodeEdit(t *testing.T) {
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
	runner := newRecordingPromptRunner()
	runner.failContent["do b"] = true // a succeeds, b fails
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-1", Config: &cfg}, runner, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusFailed)

	// 1) Changing succeeded node a's prompt must fail with a node error on "a".
	badNode := setPrompt(cfg, "a", "do a CHANGED")
	_, err = svc.UpdateRunVersion(context.Background(), run.ID, &model.UpdateGraphRunVersionRequest{Config: badNode}, stubGraphRunner{})
	verrs, ok := asValidationErrors(err)
	if !ok {
		t.Fatalf("expected ValidationError for frozen node edit, got %v", err)
	}
	if !hasErrForNode(verrs, "a") {
		t.Fatalf("expected located error on node a, got %+v", verrs)
	}

	// 2) Removing edge s_a (incident to completed node a) must fail with an edge error.
	delEdge := cloneGraphConfig(cfg)
	kept := delEdge.Edges[:0]
	for _, ed := range delEdge.Edges {
		if ed.ID != "s_a" {
			kept = append(kept, ed)
		}
	}
	delEdge.Edges = kept
	_, err = svc.UpdateRunVersion(context.Background(), run.ID, &model.UpdateGraphRunVersionRequest{Config: delEdge}, stubGraphRunner{})
	verrs, ok = asValidationErrors(err)
	if !ok {
		t.Fatalf("expected ValidationError for removing depended-on edge, got %v", err)
	}
	if !hasErrForEdge(verrs, "s_a") {
		t.Fatalf("expected located error on edge s_a, got %+v", verrs)
	}
}

func asValidationErrors(err error) ([]model.GraphValidationError, bool) {
	var verr *ValidationError
	if errors.As(err, &verr) {
		return verr.Errors, errors.Is(err, ErrInvalidGraphConfig)
	}
	return nil, false
}
