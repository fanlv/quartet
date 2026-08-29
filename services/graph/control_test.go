package graph

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/types/agui"
	"github.com/fanlv/quartet/types/model"
)

// recordingSink records ClearGraphRunLinkage calls so a test can assert a run's
// Job linkage was cleared on delete.
type recordingSink struct {
	mu            sync.Mutex
	clearedRuns   map[string]bool
	attachedByJob map[string][]string
	updates       []graphSinkUpdate
}

type retryableUnlinkSink struct {
	err   error
	calls int
}

func (*retryableUnlinkSink) SetGraphRunState(context.Context, string, string, model.JobStatus, model.GraphRunStatus, int64, int64, string) error {
	return nil
}
func (s *retryableUnlinkSink) ClearGraphRunLinkage(context.Context, string, string) error {
	s.calls++
	return s.err
}
func (*retryableUnlinkSink) AttachGraphSession(context.Context, string, string) error { return nil }
func (*retryableUnlinkSink) JobTitle(context.Context, string) string                  { return "" }

type graphSinkUpdate struct {
	jobStatus      model.JobStatus
	graphStatus    model.GraphRunStatus
	graphSessionID string
}

func activeControl(svc *serviceImpl, runID string) *runControl {
	lifecycle := svc.lifecycle(runID)
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	return lifecycle.handle
}

// lifecycleBlockingRunner exposes channel checkpoints for scheduler lifecycle
// tests. It blocks until the scheduler cancels the node context and reports
// both entry and exit without polling or sleeps.
type lifecycleBlockingRunner struct {
	stubSnapshotSource
	started chan struct{}
	exited  chan struct{}
	once    sync.Once
}

func newLifecycleBlockingRunner() *lifecycleBlockingRunner {
	return &lifecycleBlockingRunner{started: make(chan struct{}), exited: make(chan struct{})}
}

func (r *lifecycleBlockingRunner) InitSession(context.Context, string, *model.SessionOverrides) (string, error) {
	return "session-lifecycle", nil
}

func (r *lifecycleBlockingRunner) RunIteration(ctx context.Context, _ string, _ []*schema.Message, _ agui.EventHandler) error {
	r.once.Do(func() { close(r.started) })
	<-ctx.Done()
	close(r.exited)
	return ctx.Err()
}

func (r *lifecycleBlockingRunner) SessionModelID(string) string { return "" }

// stoppedGateSink holds the scheduler in its final Job stopped update. It lets
// the test prove handle.done is not closed merely because run.json is terminal.
type stoppedGateSink struct {
	stoppedEntered chan struct{}
	releaseStopped chan struct{}
	once           sync.Once
}

func newStoppedGateSink() *stoppedGateSink {
	return &stoppedGateSink{stoppedEntered: make(chan struct{}), releaseStopped: make(chan struct{})}
}

func (s *stoppedGateSink) SetGraphRunState(_ context.Context, _ string, _ string, status model.JobStatus, _ model.GraphRunStatus, _, _ int64, _ string) error {
	if status == model.JobStatusStopped {
		s.once.Do(func() { close(s.stoppedEntered) })
		<-s.releaseStopped
	}
	return nil
}

func (*stoppedGateSink) ClearGraphRunLinkage(context.Context, string, string) error { return nil }
func (*stoppedGateSink) AttachGraphSession(context.Context, string, string) error   { return nil }
func (*stoppedGateSink) JobTitle(context.Context, string) string                    { return "" }

func (s *recordingSink) SetGraphRunState(_ context.Context, _, _ string, jobStatus model.JobStatus, graphStatus model.GraphRunStatus, _, _ int64, graphSessionID string) error {
	s.mu.Lock()
	s.updates = append(s.updates, graphSinkUpdate{
		jobStatus: jobStatus, graphStatus: graphStatus, graphSessionID: graphSessionID,
	})
	s.mu.Unlock()
	return nil
}

func (s *recordingSink) JobTitle(context.Context, string) string { return "" }

func (s *recordingSink) AttachGraphSession(_ context.Context, jobID, sessionID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.attachedByJob == nil {
		s.attachedByJob = map[string][]string{}
	}
	for _, sid := range s.attachedByJob[jobID] {
		if sid == sessionID {
			return nil
		}
	}
	s.attachedByJob[jobID] = append(s.attachedByJob[jobID], sessionID)
	return nil
}

func (s *recordingSink) ClearGraphRunLinkage(_ context.Context, _, graphRunID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.clearedRuns == nil {
		s.clearedRuns = map[string]bool{}
	}
	s.clearedRuns[graphRunID] = true
	return nil
}

func (s *recordingSink) cleared(runID string) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.clearedRuns[runID]
}

func (s *recordingSink) lastUpdate() (graphSinkUpdate, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.updates) == 0 {
		return graphSinkUpdate{}, false
	}
	return s.updates[len(s.updates)-1], true
}

func TestFinishAwaitingReportsGraphStatusAndSession(t *testing.T) {
	uniqueMemoryRoot(t)
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	sink := &recordingSink{}
	cfg := model.GraphConfig{
		Workdir: t.TempDir(),
		Nodes: []model.GraphNode{
			node("start", model.GraphNodeTypeStart),
			{ID: "clarify", Type: model.GraphNodeTypeClarify, Config: model.GraphNodeConfig{AgentType: "tester"}},
			node("end", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("start_clarify", "start", "clarify"),
			edge("clarify_end", "clarify", "end"),
		},
	}
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{
		JobID: "job-awaiting", Config: &cfg,
	}, stubGraphRunner{}, sink)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusAwaitingInput)
	if _, err := svc.StopRunAndWait(context.Background(), run.ID, "join awaiting run"); err != nil {
		t.Fatalf("join awaiting run: %v", err)
	}

	update, ok := sink.lastUpdate()
	if !ok {
		t.Fatal("finishAwaiting did not update the Job sink")
	}
	if update.jobStatus != model.JobStatusStopped || update.graphStatus != model.GraphRunStatusAwaitingInput || update.graphSessionID != "session-1" {
		t.Fatalf("sink update = %+v, want stopped/awaitingInput/session-1", update)
	}
}

// promptNode builds a Prompt business node (uses the runner, unlike Shell).
func promptNode(id string) model.GraphNode {
	return model.GraphNode{ID: id, Type: model.GraphNodeTypePrompt, Config: model.GraphNodeConfig{Prompt: "do " + id, AgentType: "tester"}}
}

// waitRunningCount polls the run until at least n instances are running.
func waitRunningCount(t *testing.T, svc Service, runID string, n int) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		st, err := svc.GetRunStatus(context.Background(), runID)
		if err == nil && st.Run != nil {
			running := 0
			for _, in := range st.Instances {
				if in.Status == model.GraphInstanceStatusRunning {
					running++
				}
			}
			if running >= n {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("did not observe %d running instances", n)
}

// TestHardStopInterruptsRunning verifies a hard stop cancels in-flight
// instances (marking them interrupted) and settles the run to "stopped".
func TestHardStopInterruptsRunning(t *testing.T) {
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
			shellNode("j", "echo done"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("s_a", "s", "a"), edge("s_b", "s", "b"),
			edge("a_j", "a", "j"), edge("b_j", "b", "j"),
			edge("j_e", "j", "e"),
		},
	}
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-1", Config: &cfg}, blockingGraphRunner{}, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	waitRunningCount(t, svc, run.ID, 2)
	if _, err := svc.StopRun(context.Background(), run.ID, ""); err != nil {
		t.Fatalf("StopRun failed: %v", err)
	}
	got := waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusStopped)
	a, _ := instByNode(got, "a")
	b, _ := instByNode(got, "b")
	if a.Status != model.GraphInstanceStatusInterrupted || b.Status != model.GraphInstanceStatusInterrupted {
		t.Fatalf("expected a,b interrupted; got a=%s b=%s", a.Status, b.Status)
	}
	if _, ok := instByNode(got, "j"); ok {
		t.Fatal("join should not have run after hard stop")
	}
}

func TestStopRunAndWaitJoinsSchedulerFinalJobUpdate(t *testing.T) {
	uniqueMemoryRoot(t)
	svcAPI, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	svc := svcAPI.(*serviceImpl)
	runner := newLifecycleBlockingRunner()
	sink := newStoppedGateSink()
	cfg := model.GraphConfig{
		Workdir: t.TempDir(),
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			promptNode("p"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{edge("s_p", "s", "p"), edge("p_e", "p", "e")},
	}
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-join", Config: &cfg}, runner, sink)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	<-runner.started

	type stopResult struct {
		run *model.GraphRun
		err error
	}
	resultCh := make(chan stopResult, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	go func() {
		got, stopErr := svc.StopRunAndWait(ctx, run.ID, "join test")
		resultCh <- stopResult{run: got, err: stopErr}
	}()

	<-runner.exited
	<-sink.stoppedEntered
	select {
	case got := <-resultCh:
		t.Fatalf("StopRunAndWait returned before final Job sink update completed: %+v", got)
	default:
	}
	if activeControl(svc, run.ID) == nil {
		t.Fatal("control handle cleared before final Job sink update completed")
	}

	close(sink.releaseStopped)
	got := <-resultCh
	if got.err != nil {
		t.Fatalf("StopRunAndWait failed: %v", got.err)
	}
	if got.run == nil || got.run.Status != model.GraphRunStatusStopped {
		t.Fatalf("StopRunAndWait run = %+v, want stopped", got.run)
	}
	if activeControl(svc, run.ID) != nil {
		t.Fatal("control handle still registered after StopRunAndWait returned")
	}
}

func TestStopRunAndWaitImmediatelyAfterStart(t *testing.T) {
	uniqueMemoryRoot(t)
	svcAPI, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	svc := svcAPI.(*serviceImpl)
	cfg := model.GraphConfig{
		Workdir: t.TempDir(),
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			promptNode("p"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{edge("s_p", "s", "p"), edge("p_e", "p", "e")},
	}

	for i := 0; i < 50; i++ {
		runner := newLifecycleBlockingRunner()
		run, startErr := svc.StartRun(context.Background(), &model.StartGraphRunRequest{
			JobID:  fmt.Sprintf("job-immediate-%d", i),
			Config: &cfg,
		}, runner, nil)
		if startErr != nil {
			t.Fatalf("iteration %d: StartRun failed: %v", i, startErr)
		}
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		got, stopErr := svc.StopRunAndWait(ctx, run.ID, "immediate stop")
		cancel()
		if stopErr != nil {
			t.Fatalf("iteration %d: StopRunAndWait failed: %v", i, stopErr)
		}
		if got.Status != model.GraphRunStatusStopped {
			t.Fatalf("iteration %d: status = %s, want stopped", i, got.Status)
		}
		if activeControl(svc, run.ID) != nil {
			t.Fatalf("iteration %d: control handle still registered", i)
		}
	}
}

func TestDeleteRunFencesLateSchedulerRegistration(t *testing.T) {
	uniqueMemoryRoot(t)
	svcAPI, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	svc := svcAPI.(*serviceImpl)
	cfg := linearShellCfg(t)
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{
		JobID: "job-delete-fence", Config: &cfg,
	}, stubGraphRunner{}, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusCompleted)
	// StopRunAndWait also joins the small post-terminal window before the
	// scheduler closes its generation handle.
	if _, err := svc.StopRunAndWait(context.Background(), run.ID, "delete"); err != nil {
		t.Fatalf("StopRunAndWait failed: %v", err)
	}
	stale, err := svc.runRepo.GetRun(context.Background(), run.ID)
	if err != nil {
		t.Fatalf("load stale run snapshot: %v", err)
	}
	if err := svc.DeleteRun(context.Background(), run.ID, nil); err != nil {
		t.Fatalf("DeleteRun failed: %v", err)
	}

	// Model a ResumeRun request that resolved the terminal snapshot immediately
	// before DeleteRun installed its fence. It must fail before writing any
	// runtime files or launching a scheduler generation.
	if _, err := svc.relaunchResumableRunLocked(context.Background(), svc.lifecycle(stale.ID), stale, stubGraphRunner{}, nil); !errors.Is(err, ErrGraphRunNotFound) {
		t.Fatalf("late relaunch error = %v, want ErrGraphRunNotFound", err)
	}
	if activeControl(svc, run.ID) != nil {
		t.Fatal("late relaunch registered a scheduler after DeleteRun")
	}
	if _, err := svc.GetRunStatus(context.Background(), run.ID); !errors.Is(err, ErrGraphRunNotFound) {
		t.Fatalf("deleted run was recreated: err=%v", err)
	}
}

// TestStepStopFreezesBatch verifies step-stop runs the frozen batch to terminal
// and holds downstream instances, settling to "stepStopped" with a persisted
// frozen batch.
func TestStepStopFreezesBatch(t *testing.T) {
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
		Edges: []model.GraphEdge{
			edge("s_a", "s", "a"), edge("a_b", "a", "b"), edge("b_e", "b", "e"),
		},
	}
	gate := &gatedRunner{release: make(chan struct{})}
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-1", Config: &cfg}, gate, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	waitRunningCount(t, svc, run.ID, 1)
	if _, err := svc.StepStopRun(context.Background(), run.ID, ""); err != nil {
		t.Fatalf("StepStopRun failed: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
	close(gate.release)
	got := waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusStepStopped)
	a, _ := instByNode(got, "a")
	if a.Status != model.GraphInstanceStatusSucceeded {
		t.Fatalf("expected frozen-batch member a succeeded, got %s", a.Status)
	}
	if b, ok := instByNode(got, "b"); ok && b.Status == model.GraphInstanceStatusRunning {
		t.Fatalf("downstream b should not dispatch after step-stop, got %s", b.Status)
	}
	if got.Run.Resume == nil || got.Run.Resume.FrozenBatch == nil {
		t.Fatal("frozen batch not persisted")
	}
	if _, ok := got.Run.Resume.FrozenBatch.Members[instanceKeyString(a.Key)]; !ok {
		t.Fatalf("frozen batch should contain member a; members=%+v", got.Run.Resume.FrozenBatch.Members)
	}
}

// TestControlOnNonRunningRun verifies control actions on a run with no live
// scheduler return ErrGraphRunNotRunning.
func TestControlOnNonRunningRun(t *testing.T) {
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
	if _, err := svc.StopRun(context.Background(), run.ID, ""); err != ErrGraphRunNotRunning {
		t.Fatalf("StopRun on completed run = %v, want ErrGraphRunNotRunning", err)
	}
}

// TestDeleteRunRejectsInFlightAndUnlinksJob verifies DeleteRun refuses an
// in-flight run, then on a terminal run cascades the delete and unlinks the Job.
func TestDeleteRunRejectsInFlightAndUnlinksJob(t *testing.T) {
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
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{edge("s_a", "s", "a"), edge("a_e", "a", "e")},
	}
	sink := &recordingSink{}
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-1", Config: &cfg}, blockingGraphRunner{}, sink)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	waitRunningCount(t, svc, run.ID, 1)
	if err := svc.DeleteRun(context.Background(), run.ID, sink); !errors.Is(err, ErrGraphRunInFlight) {
		t.Fatalf("DeleteRun on running run = %v, want ErrGraphRunInFlight", err)
	}
	if _, err := svc.StopRunAndWait(context.Background(), run.ID, ""); err != nil {
		t.Fatalf("StopRunAndWait failed: %v", err)
	}
	if err := svc.DeleteRun(context.Background(), run.ID, sink); err != nil {
		t.Fatalf("DeleteRun on stopped run failed: %v", err)
	}
	if !sink.cleared(run.ID) {
		t.Fatal("expected job linkage cleared for deleted run")
	}
	if _, err := svc.GetRunStatus(context.Background(), run.ID); err == nil {
		t.Fatal("run should be gone after delete")
	}
}

func TestDeleteRun_UnlinkFailureKeepsSecondPhaseRetryable(t *testing.T) {
	uniqueMemoryRoot(t)
	svc, err := NewService()
	if err != nil {
		t.Fatalf("NewService failed: %v", err)
	}
	run := &model.GraphRun{
		ID: model.NewGraphRunID(), JobID: "job-unlink-retry", WorkspaceID: "ws-1",
		Status: model.GraphRunStatusStopped, Progress: &model.GraphProgress{}, Resume: &model.GraphResumeState{},
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	impl := svc.(*serviceImpl)
	if err := impl.runRepo.RegisterRun(context.Background(), run); err != nil {
		t.Fatalf("RegisterRun failed: %v", err)
	}
	if err := impl.persistRuntimeState(context.Background(), run, map[string]model.GraphInstanceState{}, map[string]model.GraphEdgeState{}, map[string]map[string]string{}); err != nil {
		t.Fatalf("persist run failed: %v", err)
	}

	wantErr := errors.New("forced unlink failure")
	sink := &retryableUnlinkSink{err: wantErr}
	if err := svc.DeleteRun(context.Background(), run.ID, sink); !errors.Is(err, wantErr) {
		t.Fatalf("DeleteRun error = %v, want unlink failure", err)
	}
	if _, err := svc.GetRunStatus(context.Background(), run.ID); !errors.Is(err, ErrGraphRunNotFound) {
		t.Fatalf("run artifacts after completed first phase = %v, want ErrGraphRunNotFound", err)
	}

	sink.err = nil
	if err := svc.DeleteRun(context.Background(), run.ID, sink); err != nil {
		t.Fatalf("retry DeleteRun failed: %v", err)
	}
	if sink.calls != 2 {
		t.Fatalf("unlink calls = %d, want 2", sink.calls)
	}
	if _, err := svc.GetRunStatus(context.Background(), run.ID); !errors.Is(err, ErrGraphRunNotFound) {
		t.Fatalf("run after successful retry = %v, want ErrGraphRunNotFound", err)
	}
}

// linearShellCfg is start → shell → end.
func linearShellCfg(t *testing.T) model.GraphConfig {
	t.Helper()
	return model.GraphConfig{
		Workdir: t.TempDir(),
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			shellNode("sh", "echo hi"),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{edge("s_sh", "s", "sh"), edge("sh_e", "sh", "e")},
	}
}
