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

// sessionRunner records every session created (new vs forked) so tests can
// assert the runtime session-lineage decisions (§3 会话血缘). It hands out a
// distinct session id per InitSession call and remembers the parent id of every
// ForkSession call.
type sessionRunner struct {
	stubSnapshotSource
	mu       sync.Mutex
	seq      int
	newCalls []string          // session ids minted via InitSession, in order
	forks    map[string]string // child session id -> parent session id
}

func newSessionRunner() *sessionRunner {
	return &sessionRunner{forks: map[string]string{}}
}

func (r *sessionRunner) InitSession(context.Context, string, *model.SessionOverrides) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	id := fmt.Sprintf("sess-%d", r.seq)
	r.newCalls = append(r.newCalls, id)
	return id, nil
}

func (r *sessionRunner) ForkSession(_ context.Context, parentSessionID, _ string, _ *model.SessionOverrides) (string, int, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.seq++
	id := fmt.Sprintf("fork-%d", r.seq)
	r.forks[id] = parentSessionID
	return id, 1, nil
}

func (r *sessionRunner) RunIteration(_ context.Context, _ string, _ []*schema.Message, handler agui.EventHandler) error {
	_ = handler.OnMessageDelta("ok")
	return nil
}

func (r *sessionRunner) SessionModelID(string) string { return "" }

func (r *sessionRunner) forkCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.forks)
}

func (r *sessionRunner) newCount() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return len(r.newCalls)
}

func agentNode(id string, strategy model.GraphSessionStrategy) model.GraphNode {
	return model.GraphNode{ID: id, Type: model.GraphNodeTypePrompt, Config: model.GraphNodeConfig{
		Prompt:          "do " + id,
		SessionStrategy: strategy,
	}}
}

// TestSessionInheritForksUpstream verifies start → a(new) → b(inherit) → end:
// a mints a new session, b forks from a's session, and the lineage records both.
func TestSessionInheritForksUpstream(t *testing.T) {
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
		t.Fatalf("InitSession calls = %d, want 1 (only node a)", runner.newCount())
	}
	if runner.forkCount() != 1 {
		t.Fatalf("ForkSession calls = %d, want 1 (node b)", runner.forkCount())
	}
	a, _ := instByNode(got, "a")
	b, _ := instByNode(got, "b")
	if a.SessionID == "" || b.SessionID == "" {
		t.Fatalf("missing session ids: a=%q b=%q", a.SessionID, b.SessionID)
	}
	if parent := runner.forks[b.SessionID]; parent != a.SessionID {
		t.Fatalf("b forked from %q, want a's session %q", parent, a.SessionID)
	}
	// Lineage persisted for resume.
	lineage := got.Run.Resume.SessionLineageByKey
	if lin := lineage["b"]; lin.Strategy != model.GraphSessionStrategyInherit || lin.ParentSessionID != a.SessionID || lin.SessionID != b.SessionID {
		t.Fatalf("b lineage = %+v, want inherit from %q", lin, a.SessionID)
	}
	if lin := lineage["a"]; lin.Strategy != model.GraphSessionStrategyNew || lin.SessionID != a.SessionID {
		t.Fatalf("a lineage = %+v, want new", lin)
	}
}

// TestSessionParallelForkIndependent verifies that two inherit Agents on
// parallel branches off a single upstream Agent each fork independently from the
// same parent (no session-id reuse, both forks recorded).
func TestSessionParallelForkIndependent(t *testing.T) {
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
	runner := newSessionRunner()
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-1", Config: &cfg}, runner, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	got := waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusCompleted)

	a, _ := instByNode(got, "a")
	b, _ := instByNode(got, "b")
	c, _ := instByNode(got, "c")
	if b.SessionID == c.SessionID {
		t.Fatalf("parallel forks reused the same session id %q", b.SessionID)
	}
	if runner.forks[b.SessionID] != a.SessionID || runner.forks[c.SessionID] != a.SessionID {
		t.Fatalf("both b and c must fork from a (%q): b<-%q c<-%q", a.SessionID, runner.forks[b.SessionID], runner.forks[c.SessionID])
	}
}

// TestSessionShellPassthrough verifies a Shell node between two Agents passes the
// upstream Agent's session through so the downstream inherit Agent still forks
// from the original Agent session (Shell does not create a session).
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
	if runner.forks[b.SessionID] != a.SessionID {
		t.Fatalf("b forked from %q, want a's session %q (through shell)", runner.forks[b.SessionID], a.SessionID)
	}
}

// TestSessionMultiInEdgeCreatesNew verifies a join Agent (multiple in-edges)
// uses a new session — the validator forbids inherit on a join, so the only
// legal strategy is new, and at runtime it must mint a fresh session.
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

	if runner.forkCount() != 0 {
		t.Fatalf("ForkSession calls = %d, want 0 (all new)", runner.forkCount())
	}
	if runner.newCount() != 3 {
		t.Fatalf("InitSession calls = %d, want 3 (a, b, j)", runner.newCount())
	}
	j, _ := instByNode(got, "j")
	a, _ := instByNode(got, "a")
	b, _ := instByNode(got, "b")
	if j.SessionID == a.SessionID || j.SessionID == b.SessionID || j.SessionID == "" {
		t.Fatalf("join session %q must be a fresh session distinct from upstreams %q/%q", j.SessionID, a.SessionID, b.SessionID)
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
	_, _, err := openNodeSession(context.Background(), runner, "job-1", node, "", &model.SessionOverrides{})
	if err == nil {
		t.Fatalf("openNodeSession with empty inflow + inherit must fail")
	}
	if runner.forkCount() != 0 || runner.newCount() != 0 {
		t.Fatalf("no session should be created on inherit-without-parent: new=%d fork=%d", runner.newCount(), runner.forkCount())
	}
}
