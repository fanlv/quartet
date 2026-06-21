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

// sessionRunner records every session created so tests can assert the runtime
// session-lineage decisions (§3 会话血缘). It hands out a distinct session id per
// InitSession call and counts RunIteration calls per session id so a test can
// verify that an `inherit` node REUSES the upstream session (two turns land on
// one id) rather than minting/forking a new one.
type sessionRunner struct {
	stubSnapshotSource
	mu       sync.Mutex
	seq      int
	newCalls []string       // session ids minted via InitSession, in order
	runs     map[string]int // session id -> number of RunIteration calls
}

func newSessionRunner() *sessionRunner {
	return &sessionRunner{runs: map[string]int{}}
}

func (r *sessionRunner) InitSession(context.Context, string, *model.SessionOverrides) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	id := fmt.Sprintf("sess-%d", r.seq)
	r.newCalls = append(r.newCalls, id)
	return id, nil
}

func (r *sessionRunner) RunIteration(_ context.Context, sessionID string, _ []*schema.Message, handler agui.EventHandler) error {
	r.mu.Lock()
	r.runs[sessionID]++
	r.mu.Unlock()
	_ = handler.OnMessageDelta("ok")
	return nil
}

func (r *sessionRunner) SessionModelID(string) string { return "" }

func (r *sessionRunner) runCount(sessionID string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.runs[sessionID]
}

func (r *sessionRunner) newCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.newCalls)
}

func agentNode(id string, strategy model.GraphSessionStrategy) model.GraphNode {
	cfg := model.GraphNodeConfig{
		Prompt:          "do " + id,
		SessionStrategy: strategy,
	}
	// A new session requires an Agent; inherited sessions reuse the upstream one.
	if strategy != model.GraphSessionStrategyInherit {
		cfg.AgentType = "tester"
	}
	return model.GraphNode{ID: id, Type: model.GraphNodeTypePrompt, Config: cfg}
}

// TestSessionInheritReusesUpstream verifies start → a(new) → b(inherit) → end:
// a mints a new session; b REUSES a's session (no new session minted) and appends
// its turn there, so both turns run against one session id. The lineage records
// b's reuse (outflow == inflow == a's session).
func TestSessionInheritReusesUpstream(t *testing.T) {
	uniqueMemoryRoot(t)
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	cfg := model.GraphConfig{
		Workdir: t.TempDir(),
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			agentNode("a", model.GraphSessionStrategyNew),
			agentNode("b", model.GraphSessionStrategyInherit),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_a", "s", "a"),
			edge("a_b", "a", "b"),
			edge("b_e", "b", "e"),
		},
	}
	runner := newSessionRunner()
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-1", Config: &cfg}, runner, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	got := waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusCompleted)

	if runner.newCount() != 1 {
		t.Fatalf("InitSession calls = %d, want 1 (only node a; b reuses)", runner.newCount())
	}
	a, _ := instByNode(got, "a")
	b, _ := instByNode(got, "b")
	if a.SessionID == "" || b.SessionID == "" {
		t.Fatalf("missing session ids: a=%q b=%q", a.SessionID, b.SessionID)
	}
	if a.SessionID != b.SessionID {
		t.Fatalf("b session = %q, want reuse of a's session %q", b.SessionID, a.SessionID)
	}
	if got := runner.runCount(a.SessionID); got != 2 {
		t.Fatalf("RunIteration calls on shared session %q = %d, want 2 (a's turn + b's turn)", a.SessionID, got)
	}
	// Lineage persisted for resume: b reused a's session (outflow == inflow).
	lineage := got.Run.Resume.SessionLineageByKey
	if lin := lineage["b"]; lin.Strategy != model.GraphSessionStrategyInherit || lin.ParentSessionID != a.SessionID || lin.SessionID != a.SessionID {
		t.Fatalf("b lineage = %+v, want inherit reusing %q", lin, a.SessionID)
	}
	if lin := lineage["a"]; lin.Strategy != model.GraphSessionStrategyNew || lin.SessionID != a.SessionID {
		t.Fatalf("a lineage = %+v, want new", lin)
	}
}

// TestSessionParallelInheritRejected verifies the validator rejects a graph where
// one upstream Agent fans out to two `inherit` Agents on parallel branches: both
// would reuse the same session and collide (two concurrent turns on one session).
func TestSessionParallelInheritRejected(t *testing.T) {
	cfg := &model.GraphConfig{
		Workdir: t.TempDir(),
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			agentNode("a", model.GraphSessionStrategyNew),
			agentNode("b", model.GraphSessionStrategyInherit),
			agentNode("c", model.GraphSessionStrategyInherit),
			node("e1", model.GraphNodeTypeEnd),
			node("e2", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_a", "s", "a"),
			edge("a_b", "a", "b"),
			edge("a_c", "a", "c"),
			edge("b_e1", "b", "e1"),
			edge("c_e2", "c", "e2"),
		},
	}
	errs := validateConfig(cfg)
	if !hasErrForNode(errs, "b") || !hasErrForNode(errs, "c") {
		t.Fatalf("expected session-collision errors for parallel inherit nodes b and c, got: %+v", errs)
	}
}

// TestSessionShellPassthrough verifies a Shell node between two Agents passes the
// upstream Agent's session through so the downstream inherit Agent reuses the
// original Agent session (Shell does not create a session of its own).
func TestSessionShellPassthrough(t *testing.T) {
	uniqueMemoryRoot(t)
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	cfg := model.GraphConfig{
		Workdir: t.TempDir(),
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			agentNode("a", model.GraphSessionStrategyNew),
			shellNode("sh", "echo hi"),
			agentNode("b", model.GraphSessionStrategyInherit),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_a", "s", "a"),
			edge("a_sh", "a", "sh"),
			edge("sh_b", "sh", "b"),
			edge("b_e", "b", "e"),
		},
	}
	runner := newSessionRunner()
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-1", Config: &cfg}, runner, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	got := waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusCompleted)

	a, _ := instByNode(got, "a")
	sh, _ := instByNode(got, "sh")
	b, _ := instByNode(got, "b")
	if sh.SessionID != a.SessionID {
		t.Fatalf("shell outflow session = %q, want passthrough of a's %q", sh.SessionID, a.SessionID)
	}
	if b.SessionID != a.SessionID {
		t.Fatalf("b session = %q, want reuse of a's session %q (through shell)", b.SessionID, a.SessionID)
	}
}

// TestSessionMultiInEdgeCreatesNew verifies a join Agent (multiple in-edges)
// configured with the `new` strategy mints a fresh session at runtime,
// independent of its upstreams.
func TestSessionMultiInEdgeCreatesNew(t *testing.T) {
	uniqueMemoryRoot(t)
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	cfg := model.GraphConfig{
		Workdir: t.TempDir(),
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			agentNode("a", model.GraphSessionStrategyNew),
			agentNode("b", model.GraphSessionStrategyNew),
			agentNode("j", model.GraphSessionStrategyNew),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_a", "s", "a"),
			edge("s_b", "s", "b"),
			edge("a_j", "a", "j"),
			edge("b_j", "b", "j"),
			edge("j_e", "j", "e"),
		},
	}
	runner := newSessionRunner()
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-1", Config: &cfg}, runner, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	got := waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusCompleted)

	if runner.newCount() != 3 {
		t.Fatalf("InitSession calls = %d, want 3 (a, b, j all new)", runner.newCount())
	}
	j, _ := instByNode(got, "j")
	a, _ := instByNode(got, "a")
	b, _ := instByNode(got, "b")
	if j.SessionID == a.SessionID || j.SessionID == b.SessionID || j.SessionID == "" {
		t.Fatalf("join session %q must be a fresh session distinct from upstreams %q/%q", j.SessionID, a.SessionID, b.SessionID)
	}
}

// TestSessionMultiInEdgeInheritReusesGreatestNodeID verifies a join Agent
// declaring `inherit` with two Agent upstreams (a, b) reuses the session of the
// upstream with the greatest node ID (ascending sort, last wins — §3 会话血缘),
// independent of completion order, so the choice is replayable across reruns /
// crash recovery. Here "b" > "a", so the join must reuse b's session, not a's.
// a and b are parallel but both `new` (distinct session sources), so the
// validator allows the graph.
func TestSessionMultiInEdgeInheritReusesGreatestNodeID(t *testing.T) {
	uniqueMemoryRoot(t)
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	cfg := model.GraphConfig{
		Workdir: t.TempDir(),
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			agentNode("a", model.GraphSessionStrategyNew),
			agentNode("b", model.GraphSessionStrategyNew),
			agentNode("j", model.GraphSessionStrategyInherit),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_a", "s", "a"),
			edge("s_b", "s", "b"),
			edge("a_j", "a", "j"),
			edge("b_j", "b", "j"),
			edge("j_e", "j", "e"),
		},
	}
	runner := newSessionRunner()
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-1", Config: &cfg}, runner, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	got := waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusCompleted)

	if runner.newCount() != 2 {
		t.Fatalf("InitSession calls = %d, want 2 (a, b; j reuses)", runner.newCount())
	}
	j, _ := instByNode(got, "j")
	b, _ := instByNode(got, "b")
	if j.SessionID != b.SessionID {
		t.Fatalf("join session = %q, want reuse of b's session %q (greatest node ID upstream)", j.SessionID, b.SessionID)
	}
}

// TestSessionInheritWithoutParentFails verifies the runtime guard: an Agent
// declaring inherit whose inflow session is empty (no upstream Agent ran) fails
// the node with a full error. Validation forbids this on a start chain's first
// Agent, so the only way to reach it at runtime is a non-Agent-only upstream
// chain — modelled here by a Shell-first inherit Agent which the validator does
// NOT reject (the validator's first-Agent rule only forbids inherit when the
// node itself is the first Agent; here "b" IS first, so we instead drive the
// guard directly via openNodeSession unit-style with an empty inflow).
func TestSessionInheritWithoutParentFails(t *testing.T) {
	node := agentNode("b", model.GraphSessionStrategyInherit)
	runner := newSessionRunner()
	_, err := openNodeSession(context.Background(), runner, "job-1", node, "", &model.SessionOverrides{})
	if err == nil {
		t.Fatalf("openNodeSession with empty inflow + inherit must fail")
	}
	if runner.newCount() != 0 {
		t.Fatalf("no session should be created on inherit-without-parent: new=%d", runner.newCount())
	}
}
