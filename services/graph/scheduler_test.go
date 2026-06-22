package graph

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/types/agui"
	"github.com/fanlv/quartet/types/model"
)

// shellNode builds a Shell business node with the given script and declared
// output variables.
func shellNode(id, script string, outputs ...string) model.GraphNode {
	return model.GraphNode{ID: id, Type: model.GraphNodeTypeShell, Config: model.GraphNodeConfig{
		Script:          script,
		OutputVariables: outputs,
	}}
}

func ifElseNode(id, condition string) model.GraphNode {
	return model.GraphNode{ID: id, Type: model.GraphNodeTypeIfElse, Config: model.GraphNodeConfig{Condition: condition}}
}

func instByNode(resp *model.GraphRunStatusResponse, nodeID string) (model.GraphInstanceState, bool) {
	for _, in := range resp.Instances {
		if in.NodeID == nodeID {
			return in, true
		}
	}
	return model.GraphInstanceState{}, false
}

// TestSchedulerDiamondJoin runs start → (a, b) parallel → join → end and checks
// the join merges both upstreams' variables.
func TestSchedulerDiamondJoin(t *testing.T) {
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
			shellNode("a", "quartet_set va A", "va"),
			shellNode("b", "quartet_set vb B", "vb"),
			shellNode("j", "echo {{va}}-{{vb}}"),
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
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-1", Config: &cfg}, stubGraphRunner{}, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	got := waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusCompleted)
	if got.Progress.TotalCount != 3 || got.Progress.CompletedCount != 3 {
		t.Fatalf("progress = %+v, want total=3 completed=3", got.Progress)
	}
	j, ok := instByNode(got, "j")
	if !ok {
		t.Fatal("join instance missing")
	}
	if j.VisibleVariables["va"] != "A" || j.VisibleVariables["vb"] != "B" {
		t.Fatalf("join visible vars = %+v, want va=A vb=B", j.VisibleVariables)
	}
	if j.VisibleVariables[lastAssistantKey] != "A-B\n" {
		t.Fatalf("join _last_assistant_msg = %q, want %q", j.VisibleVariables[lastAssistantKey], "A-B\n")
	}
}

// TestSchedulerIfElsePrunesAndJoinResolves verifies If-Else activates only one
// branch, the pruned branch's business node is skipped, and a downstream join
// fed by the pruned branch still resolves (no deadlock).
func TestSchedulerIfElsePrunesAndJoinResolves(t *testing.T) {
	uniqueMemoryRoot(t)
	workdir := t.TempDir()
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	cfg := model.GraphConfig{
		Workdir:   workdir,
		Variables: map[string]string{"flag": "yes"},
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			ifElseNode("ie", `{{flag}} == "yes"`),
			shellNode("yes", "echo took-yes"),
			shellNode("no", "echo took-no"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_ie", "s", "ie"),
			portEdge("ie_yes", "ie", "yes", model.GraphEdgePortYes),
			portEdge("ie_no", "ie", "no", model.GraphEdgePortNo),
			edge("yes_e", "yes", "e"),
			edge("no_e", "no", "e"),
		},
	}
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-1", Config: &cfg}, stubGraphRunner{}, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	got := waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusCompleted)
	yes, _ := instByNode(got, "yes")
	no, _ := instByNode(got, "no")
	if yes.Status != model.GraphInstanceStatusSucceeded {
		t.Fatalf("yes branch status = %s, want succeeded", yes.Status)
	}
	if no.Status != model.GraphInstanceStatusSkipped {
		t.Fatalf("no branch status = %s, want skipped", no.Status)
	}
	// ifElse + yes succeeded (completed 2); 'no' is skipped and reclaimed out of
	// the denominator → total 2 (ifElse, yes), completed 2, skipped 1.
	if got.Progress.TotalCount != 2 || got.Progress.CompletedCount != 2 || got.Progress.SkippedCount != 1 {
		t.Fatalf("progress = %+v, want total=2 completed=2 skipped=1", got.Progress)
	}
}

// TestSchedulerConcurrencyBound verifies the global semaphore caps simultaneous
// in-flight nodes at the configured limit.
func TestSchedulerConcurrencyBound(t *testing.T) {
	uniqueMemoryRoot(t)
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	// Three parallel Prompt nodes; concurrency limit 2 means peak in-flight ≤ 2.
	nodes := []model.GraphNode{node("s", model.GraphNodeTypeStart), node("e", model.GraphNodeTypeEnd)}
	edges := []model.GraphEdge{}
	for _, id := range []string{"p1", "p2", "p3"} {
		nodes = append(nodes, model.GraphNode{ID: id, Type: model.GraphNodeTypePrompt, Config: model.GraphNodeConfig{Prompt: "hi", AgentType: "tester"}})
		edges = append(edges, edge("s_"+id, "s", id), edge(id+"_e", id, "e"))
	}
	cfg := model.GraphConfig{
		Nodes:     nodes,
		Edges:     edges,
		RunConfig: model.GraphRunConfig{ConcurrencyLimit: 2},
	}
	gate := &gatedRunner{release: make(chan struct{})}
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-1", Config: &cfg}, gate, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	// Let the scheduler reach peak in-flight, then release the gate.
	gate.waitAndRelease(t, 2)
	got := waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusCompleted)
	if got.Progress.CompletedCount != 3 {
		t.Fatalf("completed = %d, want 3", got.Progress.CompletedCount)
	}
	if peak := atomic.LoadInt32(&gate.peak); peak > 2 {
		t.Fatalf("peak in-flight = %d, want <= 2", peak)
	}
}

// TestSchedulerBranchFailurePropagates verifies one parallel branch failing
// fails the whole run.
func TestSchedulerBranchFailurePropagates(t *testing.T) {
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
			shellNode("ok", "echo fine"),
			shellNode("bad", "exit 3"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_ok", "s", "ok"),
			edge("s_bad", "s", "bad"),
			edge("ok_e", "ok", "e"),
			edge("bad_e", "bad", "e"),
		},
	}
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-1", Config: &cfg}, stubGraphRunner{}, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	got := waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusFailed)
	if got.Run.LastError == nil {
		t.Fatal("expected LastError on failed run")
	}
	bad, _ := instByNode(got, "bad")
	if bad.Status != model.GraphInstanceStatusFailed {
		t.Fatalf("bad node status = %s, want failed", bad.Status)
	}
	if bad.Error == nil || bad.Error.NodeID != "bad" {
		t.Fatalf("bad node error missing context: %+v", bad.Error)
	}
}

func TestSchedulerInstanceLimitFailsRun(t *testing.T) {
	uniqueMemoryRoot(t)
	workdir := t.TempDir()
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	cfg := model.GraphConfig{
		Workdir: workdir,
		RunConfig: model.GraphRunConfig{
			InstanceLimit: 1,
		},
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			shellNode("a", "echo a"),
			shellNode("b", "echo b"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_a", "s", "a"),
			edge("a_b", "a", "b"),
			edge("b_e", "b", "e"),
		},
	}
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-1", Config: &cfg}, stubGraphRunner{}, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	got := waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusFailed)
	if got.Run.LastError == nil {
		t.Fatal("expected LastError on failed run")
	}
	msg := got.Run.LastError.Message
	if !strings.Contains(msg, "instance limit exceeded") || !strings.Contains(msg, "current instances=2") || !strings.Contains(msg, "limit=1") {
		t.Fatalf("limit error message missing detail: %q", msg)
	}
}

func TestSchedulerSnapshotByteLimitFailsRun(t *testing.T) {
	uniqueMemoryRoot(t)
	workdir := t.TempDir()
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	cfg := model.GraphConfig{
		Workdir: workdir,
		RunConfig: model.GraphRunConfig{
			SnapshotByteLimit: 4,
		},
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			shellNode("big", "quartet_set payload 1234567890", "payload"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_big", "s", "big"),
			edge("big_e", "big", "e"),
		},
	}
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-1", Config: &cfg}, stubGraphRunner{}, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	got := waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusFailed)
	if got.Run.LastError == nil {
		t.Fatal("expected LastError on failed run")
	}
	msg := got.Run.LastError.Message
	if !strings.Contains(msg, "snapshot byte limit exceeded") || !strings.Contains(msg, "current snapshot bytes=") || !strings.Contains(msg, "limit=4") {
		t.Fatalf("snapshot limit error message missing detail: %q", msg)
	}
}

func TestSchedulerNodeTimeoutFailsInstance(t *testing.T) {
	uniqueMemoryRoot(t)
	workdir := t.TempDir()
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	timeout := 1
	cfg := model.GraphConfig{
		Workdir: workdir,
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			{ID: "slow", Type: model.GraphNodeTypeShell, Config: model.GraphNodeConfig{
				Script:         "sleep 3",
				TimeoutSeconds: &timeout,
			}},
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_slow", "s", "slow"),
			edge("slow_e", "slow", "e"),
		},
	}
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-timeout-node", Config: &cfg}, stubGraphRunner{}, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	got := waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusFailed)
	inst, ok := instByNode(got, "slow")
	if !ok {
		t.Fatal("slow instance missing")
	}
	if inst.Status != model.GraphInstanceStatusFailed {
		t.Fatalf("slow status = %s, want failed", inst.Status)
	}
	if inst.Error == nil || !contains(inst.Error.Message, "timed out after 1s") {
		t.Fatalf("timeout error missing full detail: %+v", inst.Error)
	}
}

func TestSchedulerJobTimeoutCancelsRunningInstances(t *testing.T) {
	uniqueMemoryRoot(t)
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	cfg := model.GraphConfig{
		RunConfig: model.GraphRunConfig{JobTimeoutSec: 1},
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			{ID: "slow", Type: model.GraphNodeTypePrompt, Config: model.GraphNodeConfig{Prompt: "wait", AgentType: "tester"}},
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_slow", "s", "slow"),
			edge("slow_e", "slow", "e"),
		},
	}
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-timeout-run", Config: &cfg}, blockingGraphRunner{}, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	got := waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusTimedOut)
	if got.Run.LastError == nil || !contains(got.Run.LastError.Message, "job timed out after 1s") {
		t.Fatalf("job timeout error missing full detail: %+v", got.Run.LastError)
	}
	inst, ok := instByNode(got, "slow")
	if !ok {
		t.Fatal("slow instance missing")
	}
	if inst.Status != model.GraphInstanceStatusInterrupted {
		t.Fatalf("slow status = %s, want interrupted", inst.Status)
	}
	if got.Progress.InterruptedCount != 1 {
		t.Fatalf("progress = %+v, want interrupted=1", got.Progress)
	}
}

func TestPromptTransientRetryRecovers(t *testing.T) {
	uniqueMemoryRoot(t)
	svcIface, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	svc := svcIface.(*serviceImpl)
	svc.transientRetryDelay = time.Millisecond
	cfg := model.GraphConfig{
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			{ID: "p", Type: model.GraphNodeTypePrompt, Config: model.GraphNodeConfig{Prompt: "hello", AgentType: "tester"}},
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_p", "s", "p"),
			edge("p_e", "p", "e"),
		},
	}
	runner := &sequenceGraphRunner{errs: []error{io.EOF, nil}, outputs: []string{"", "ok"}}
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-retry-transient", Config: &cfg}, runner, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	got := waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusCompleted)
	if runner.calls != 2 {
		t.Fatalf("runner calls = %d, want 2", runner.calls)
	}
	inst, _ := instByNode(got, "p")
	if inst.VisibleVariables[lastAssistantKey] != "ok" {
		t.Fatalf("_last_assistant_msg = %q, want ok", inst.VisibleVariables[lastAssistantKey])
	}
}

func TestPromptRateLimitRetriesExhausted(t *testing.T) {
	uniqueMemoryRoot(t)
	svcIface, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	svc := svcIface.(*serviceImpl)
	svc.rateLimitRetryBaseDelay = time.Millisecond
	cfg := model.GraphConfig{
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			{ID: "p", Type: model.GraphNodeTypePrompt, Config: model.GraphNodeConfig{Prompt: "hello", AgentType: "tester"}},
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_p", "s", "p"),
			edge("p_e", "p", "e"),
		},
	}
	runner := &sequenceGraphRunner{errs: []error{
		errors.New("HTTP 429"),
		errors.New("StatusCode: 429"),
		errors.New("too many requests"),
		errors.New("rate_limit_exceeded"),
	}}
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-retry-rate", Config: &cfg}, runner, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	got := waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusFailed)
	if runner.calls != 4 {
		t.Fatalf("runner calls = %d, want 4", runner.calls)
	}
	inst, _ := instByNode(got, "p")
	if inst.Error == nil || inst.Error.RetryCount != 3 {
		t.Fatalf("retry count not surfaced: %+v", inst.Error)
	}
}

// gatedRunner is a Runner whose Prompt iterations block until released, letting
// a test observe the peak number of concurrently in-flight nodes.
type gatedRunner struct {
	stubSnapshotSource
	mu      sync.Mutex
	cur     int32
	peak    int32
	release chan struct{}
}

func (g *gatedRunner) InitSession(context.Context, string, *model.SessionOverrides) (string, error) {
	return "session-1", nil
}

func (g *gatedRunner) RunIteration(_ context.Context, _ string, _ []*schema.Message, _ agui.EventHandler) error {
	n := atomic.AddInt32(&g.cur, 1)
	g.mu.Lock()
	if n > g.peak {
		g.peak = n
	}
	g.mu.Unlock()
	<-g.release
	atomic.AddInt32(&g.cur, -1)
	return nil
}

func (g *gatedRunner) SessionModelID(string) string { return "" }

// waitAndRelease blocks until at least want nodes are in-flight, then closes the
// release channel so all blocked iterations finish.
func (g *gatedRunner) waitAndRelease(t *testing.T, want int32) {
	t.Helper()
	for i := 0; i < 200; i++ {
		if atomic.LoadInt32(&g.cur) >= want {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	close(g.release)
}

type blockingGraphRunner struct{ stubSnapshotSource }

func (blockingGraphRunner) InitSession(context.Context, string, *model.SessionOverrides) (string, error) {
	return "session-1", nil
}

func (blockingGraphRunner) RunIteration(ctx context.Context, _ string, _ []*schema.Message, _ agui.EventHandler) error {
	<-ctx.Done()
	return ctx.Err()
}

func (blockingGraphRunner) SessionModelID(string) string { return "" }

type sequenceGraphRunner struct {
	stubSnapshotSource
	mu      sync.Mutex
	errs    []error
	outputs []string
	calls   int
}

func (r *sequenceGraphRunner) InitSession(context.Context, string, *model.SessionOverrides) (string, error) {
	return "session-1", nil
}

func (r *sequenceGraphRunner) RunIteration(_ context.Context, _ string, _ []*schema.Message, handler agui.EventHandler) error {
	r.mu.Lock()
	idx := r.calls
	r.calls++
	var output string
	if idx < len(r.outputs) {
		output = r.outputs[idx]
	}
	var err error
	if idx < len(r.errs) {
		err = r.errs[idx]
	}
	r.mu.Unlock()
	if output != "" {
		_ = handler.OnMessageDelta(output)
	}
	return err
}

func (r *sequenceGraphRunner) SessionModelID(string) string { return "" }

func contains(s, substr string) bool {
	return strings.Contains(s, substr)
}

// TestShellTransientRetryRecovers verifies a Shell node whose first invocation
// emits a transient (HTTP2 stream) error and exits non-zero is retried in place
// and succeeds on the next attempt, producing its declared output variable and
// surfacing no error. A counter file persisted in the workdir distinguishes the
// failing first run from the recovering retry.
func TestShellTransientRetryRecovers(t *testing.T) {
	uniqueMemoryRoot(t)
	workdir := t.TempDir()
	svcIface, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	svc := svcIface.(*serviceImpl)
	svc.transientRetryDelay = time.Millisecond
	// First attempt: print a transient stream error to stderr and exit 1.
	// Subsequent attempts: set the output variable and exit 0.
	script := `n=$(cat .retry_counter 2>/dev/null || echo 0)
n=$((n+1))
echo $n > .retry_counter
if [ "$n" -eq 1 ]; then
  echo "stream error: received RST_STREAM" >&2
  exit 1
fi
quartet_set out done`
	cfg := model.GraphConfig{
		Workdir: workdir,
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			shellNode("sh", script, "out"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_sh", "s", "sh"),
			edge("sh_e", "sh", "e"),
		},
	}
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-shell-transient", Config: &cfg}, stubGraphRunner{}, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	got := waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusCompleted)
	inst, ok := instByNode(got, "sh")
	if !ok {
		t.Fatal("shell instance missing")
	}
	if inst.Status != model.GraphInstanceStatusSucceeded {
		t.Fatalf("shell status = %s, want succeeded (err=%+v)", inst.Status, inst.Error)
	}
	if inst.VisibleVariables["out"] != "done" {
		t.Fatalf("out = %q, want done", inst.VisibleVariables["out"])
	}
}

// TestShellRateLimitRetriesExhausted verifies a Shell node that always emits a
// rate-limit error is retried the rate-limit budget (3) then fails, and the
// surfaced error carries the retry count plus the full stdout/stderr/exit code.
func TestShellRateLimitRetriesExhausted(t *testing.T) {
	uniqueMemoryRoot(t)
	workdir := t.TempDir()
	svcIface, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	svc := svcIface.(*serviceImpl)
	svc.rateLimitRetryBaseDelay = time.Millisecond
	script := `echo "out-line"
echo "rate_limit_exceeded: too many requests" >&2
exit 7`
	cfg := model.GraphConfig{
		Workdir: workdir,
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			shellNode("sh", script),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_sh", "s", "sh"),
			edge("sh_e", "sh", "e"),
		},
	}
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-shell-rate", Config: &cfg}, stubGraphRunner{}, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	got := waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusFailed)
	inst, ok := instByNode(got, "sh")
	if !ok {
		t.Fatal("shell instance missing")
	}
	if inst.Error == nil {
		t.Fatal("shell error not surfaced")
	}
	if inst.Error.RetryCount != graphRateLimitRetries {
		t.Fatalf("retry count = %d, want %d", inst.Error.RetryCount, graphRateLimitRetries)
	}
	if !strings.Contains(inst.Error.Message, "exit status 7") ||
		!strings.Contains(inst.Error.Message, "out-line") ||
		!strings.Contains(inst.Error.Message, "rate_limit_exceeded") {
		t.Fatalf("error message missing stdout/stderr/exit code: %q", inst.Error.Message)
	}
}

// TestShellDeterministicFailureNotRetried verifies a Shell node that fails
// deterministically (non-transient exit) is not retried — the surfaced error
// carries retryCount=0.
func TestShellDeterministicFailureNotRetried(t *testing.T) {
	uniqueMemoryRoot(t)
	workdir := t.TempDir()
	svcIface, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	svc := svcIface.(*serviceImpl)
	svc.transientRetryDelay = time.Millisecond
	svc.rateLimitRetryBaseDelay = time.Millisecond
	cfg := model.GraphConfig{
		Workdir: workdir,
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			shellNode("sh", "echo boom >&2; exit 1"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_sh", "s", "sh"),
			edge("sh_e", "sh", "e"),
		},
	}
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-shell-fail", Config: &cfg}, stubGraphRunner{}, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	got := waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusFailed)
	inst, ok := instByNode(got, "sh")
	if !ok {
		t.Fatal("shell instance missing")
	}
	if inst.Error == nil || inst.Error.RetryCount != 0 {
		t.Fatalf("deterministic shell failure should not retry: %+v", inst.Error)
	}
}
