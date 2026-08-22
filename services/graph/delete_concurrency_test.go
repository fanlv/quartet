package graph

import (
	"context"
	"errors"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/types/agui"
	"github.com/fanlv/quartet/types/model"
	typepath "github.com/fanlv/quartet/types/path"
)

type lifecycleFirstGetGateRepo struct {
	repository.GraphRunRepo
	count   atomic.Int32
	entered chan struct{}
	release chan struct{}
}

func (r *lifecycleFirstGetGateRepo) GetRun(ctx context.Context, runID string) (*model.GraphRun, error) {
	if r.count.Add(1) == 1 {
		close(r.entered)
		<-r.release
	}
	return r.GraphRunRepo.GetRun(ctx, runID)
}

type lifecycleTwoGetGateRepo struct {
	repository.GraphRunRepo
	mu      sync.Mutex
	count   int
	both    chan struct{}
	release chan struct{}
}

func (r *lifecycleTwoGetGateRepo) GetRun(ctx context.Context, runID string) (*model.GraphRun, error) {
	run, err := r.GraphRunRepo.GetRun(ctx, runID)
	r.mu.Lock()
	r.count++
	n := r.count
	if n == 2 {
		close(r.both)
	}
	r.mu.Unlock()
	if n <= 2 {
		<-r.release
	}
	return run, err
}

type lifecycleSecondGetGateRepo struct {
	repository.GraphRunRepo
	count   atomic.Int32
	loaded  chan struct{}
	release chan struct{}
}

type lifecycleFailOnceDeleteRepo struct {
	repository.GraphRunRepo
	mu        sync.Mutex
	fail      bool
	deleteErr error
}

type lifecyclePartialDeleteRepo struct {
	repository.GraphRunRepo
	workspaceID string
	jobID       string
	deleteErr   error
	mu          sync.Mutex
	calls       int
}

func (r *lifecyclePartialDeleteRepo) DeleteRun(ctx context.Context, runID string) error {
	r.mu.Lock()
	r.calls++
	call := r.calls
	r.mu.Unlock()
	if call == 1 {
		if err := os.Remove(typepath.GraphRunFile(r.workspaceID, r.jobID)); err != nil {
			return err
		}
		return r.deleteErr
	}
	return r.GraphRunRepo.DeleteRun(ctx, runID)
}

func (r *lifecycleFailOnceDeleteRepo) DeleteRun(ctx context.Context, runID string) error {
	r.mu.Lock()
	if r.fail {
		r.fail = false
		err := r.deleteErr
		r.mu.Unlock()
		return err
	}
	r.mu.Unlock()
	return r.GraphRunRepo.DeleteRun(ctx, runID)
}

func (r *lifecycleSecondGetGateRepo) GetRun(ctx context.Context, runID string) (*model.GraphRun, error) {
	run, err := r.GraphRunRepo.GetRun(ctx, runID)
	if r.count.Add(1) == 2 {
		close(r.loaded)
		<-r.release
	}
	return run, err
}

type lifecycleCancellableRunner struct {
	stubSnapshotSource
	started  chan struct{}
	canceled chan struct{}
	once     sync.Once
}

func (r *lifecycleCancellableRunner) InitSession(context.Context, string, *model.SessionOverrides) (string, error) {
	return "lifecycle-session", nil
}

func (r *lifecycleCancellableRunner) RunIteration(ctx context.Context, _ string, _ []*schema.Message, _ agui.EventHandler) error {
	r.once.Do(func() { close(r.started) })
	<-ctx.Done()
	close(r.canceled)
	return ctx.Err()
}

func (r *lifecycleCancellableRunner) SessionModelID(string) string { return "" }

type lifecycleSingleAttemptRunner struct {
	stubSnapshotSource
	calls        atomic.Int32
	started      chan struct{}
	releaseFirst chan struct{}
}

func (r *lifecycleSingleAttemptRunner) InitSession(context.Context, string, *model.SessionOverrides) (string, error) {
	return "single-resume-session", nil
}

func (r *lifecycleSingleAttemptRunner) RunIteration(context.Context, string, []*schema.Message, agui.EventHandler) error {
	if r.calls.Add(1) != 1 {
		return errors.New("unexpected second scheduler attempt")
	}
	close(r.started)
	<-r.releaseFirst
	return nil
}

func (r *lifecycleSingleAttemptRunner) SessionModelID(string) string { return "" }

type lifecycleSnapshotGateRunner struct {
	stubGraphRunner
	entered chan struct{}
	release chan struct{}
}

type lifecycleTerminalGateSink struct {
	terminalEntered chan struct{}
	releaseTerminal chan struct{}
	once            sync.Once
}

type lifecycleRunningGateSink struct {
	runningEntered chan struct{}
	releaseRunning chan struct{}
	once           sync.Once
}

func (s *lifecycleRunningGateSink) SetGraphRunState(_ context.Context, _, _ string, status model.JobStatus, _ model.GraphRunStatus, _, _ int64, _ string) error {
	if status == model.JobStatusRunning {
		s.once.Do(func() { close(s.runningEntered) })
		<-s.releaseRunning
	}
	return nil
}

func (*lifecycleRunningGateSink) ClearGraphRunLinkage(context.Context, string, string) error {
	return nil
}
func (*lifecycleRunningGateSink) AttachGraphSession(context.Context, string, string) error {
	return nil
}
func (*lifecycleRunningGateSink) JobTitle(context.Context, string) string { return "" }

func (s *lifecycleTerminalGateSink) SetGraphRunState(_ context.Context, _, _ string, _ model.JobStatus, graphStatus model.GraphRunStatus, _, _ int64, _ string) error {
	if graphStatus == model.GraphRunStatusCompleted {
		s.once.Do(func() { close(s.terminalEntered) })
		<-s.releaseTerminal
	}
	return nil
}

func (*lifecycleTerminalGateSink) ClearGraphRunLinkage(context.Context, string, string) error {
	return nil
}
func (*lifecycleTerminalGateSink) AttachGraphSession(context.Context, string, string) error {
	return nil
}
func (*lifecycleTerminalGateSink) JobTitle(context.Context, string) string { return "" }

func (r *lifecycleSnapshotGateRunner) ResolveAgentSnapshot(ctx context.Context, _ string) (model.GraphAgentSnapshot, error) {
	close(r.entered)
	select {
	case <-r.release:
		return model.GraphAgentSnapshot{}, nil
	case <-ctx.Done():
		return model.GraphAgentSnapshot{}, ctx.Err()
	}
}

func lifecycleFailedRun(t *testing.T, svc *serviceImpl, jobID string) *model.GraphRun {
	t.Helper()
	cfg := model.GraphConfig{
		Workdir: t.TempDir(),
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart), promptNode("p"), node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{edge("s_p", "s", "p"), edge("p_e", "p", "e")},
	}
	runner := newCountingRunner()
	runner.failNode["do p"] = true
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: jobID, Config: &cfg}, runner, nil)
	if err != nil {
		t.Fatalf("StartRun failed: %v", err)
	}
	waitGraphRunStatus(t, svc, run.ID, model.GraphRunStatusFailed)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := svc.StopRunAndWait(ctx, run.ID, "join failed generation"); err != nil {
		t.Fatalf("join failed generation: %v", err)
	}
	return run
}

func TestStopRunAndWaitJoinsConcurrentResume(t *testing.T) {
	uniqueMemoryRoot(t)
	api, err := NewService()
	if err != nil {
		t.Fatal(err)
	}
	svc := api.(*serviceImpl)
	run := lifecycleFailedRun(t, svc, "job-stop-resume-race")
	gate := &lifecycleFirstGetGateRepo{GraphRunRepo: svc.runRepo, entered: make(chan struct{}), release: make(chan struct{})}
	svc.runRepo = gate
	stopDone := make(chan error, 1)
	go func() {
		_, stopErr := svc.StopRunAndWait(context.Background(), run.ID, "concurrent stop")
		stopDone <- stopErr
	}()
	<-gate.entered

	runner := &lifecycleCancellableRunner{started: make(chan struct{}), canceled: make(chan struct{})}
	if _, err := svc.ResumeRun(context.Background(), run.ID, runner, nil); err != nil {
		t.Fatalf("ResumeRun failed: %v", err)
	}
	<-runner.started
	close(gate.release)
	if err := <-stopDone; err != nil {
		t.Fatalf("StopRunAndWait failed: %v", err)
	}
	select {
	case <-runner.canceled:
	default:
		t.Fatal("StopRunAndWait returned before canceling resumed worker")
	}
	if svc.getControl(run.ID) != nil {
		t.Fatal("StopRunAndWait returned before resumed generation completed")
	}
}

func TestConcurrentResumeOnlyOneGenerationSucceeds(t *testing.T) {
	uniqueMemoryRoot(t)
	api, err := NewService()
	if err != nil {
		t.Fatal(err)
	}
	svc := api.(*serviceImpl)
	run := lifecycleFailedRun(t, svc, "job-concurrent-resume")
	gate := &lifecycleTwoGetGateRepo{GraphRunRepo: svc.runRepo, both: make(chan struct{}), release: make(chan struct{})}
	svc.runRepo = gate
	runner := &lifecycleSingleAttemptRunner{started: make(chan struct{}), releaseFirst: make(chan struct{})}
	results := make(chan error, 2)
	for i := 0; i < 2; i++ {
		go func() {
			_, resumeErr := svc.ResumeRun(context.Background(), run.ID, runner, nil)
			results <- resumeErr
		}()
	}
	<-gate.both
	close(gate.release)
	if err := <-results; err != nil {
		t.Fatalf("winning ResumeRun failed: %v", err)
	}
	<-runner.started
	select {
	case err := <-results:
		t.Fatalf("losing ResumeRun returned before winner settled: %v", err)
	default:
	}
	close(runner.releaseFirst)
	if err := <-results; !errors.Is(err, ErrGraphRunNotResumable) {
		t.Fatalf("losing ResumeRun error = %v, want ErrGraphRunNotResumable", err)
	}
	if got := runner.calls.Load(); got != 1 {
		t.Fatalf("scheduler attempts = %d, want 1", got)
	}
}

func TestDeleteRunWinsAgainstSchedulerlessStopCommit(t *testing.T) {
	uniqueMemoryRoot(t)
	api, err := NewService()
	if err != nil {
		t.Fatal(err)
	}
	svc := api.(*serviceImpl)
	run := &model.GraphRun{
		ID: model.NewGraphRunID(), JobID: "job-orphan-stop-delete", WorkspaceID: "ws-1",
		Status: model.GraphRunStatusPending, Progress: &model.GraphProgress{}, Resume: &model.GraphResumeState{},
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := svc.runRepo.RegisterRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if err := svc.persistRuntimeState(context.Background(), run, map[string]model.GraphInstanceState{}, map[string]model.GraphEdgeState{}, map[string]map[string]string{}); err != nil {
		t.Fatal(err)
	}
	gate := &lifecycleSecondGetGateRepo{GraphRunRepo: svc.runRepo, loaded: make(chan struct{}), release: make(chan struct{})}
	svc.runRepo = gate
	stopDone := make(chan error, 1)
	go func() {
		_, stopErr := svc.StopRunAndWait(context.Background(), run.ID, "orphan stop")
		stopDone <- stopErr
	}()
	<-gate.loaded
	if err := svc.DeleteRun(context.Background(), run.ID, nil); err != nil {
		t.Fatalf("DeleteRun failed: %v", err)
	}
	close(gate.release)
	if err := <-stopDone; err != nil {
		t.Fatalf("StopRunAndWait failed: %v", err)
	}
	if resurrected, err := svc.runRepo.GetRun(context.Background(), run.ID); err == nil {
		t.Fatalf("deleted run resurrected as %s", resurrected.Status)
	}
}

func TestDeleteRunWinsAgainstPreparedStaticVersionCommit(t *testing.T) {
	uniqueMemoryRoot(t)
	api, err := NewService()
	if err != nil {
		t.Fatal(err)
	}
	svc := api.(*serviceImpl)
	// Use a fresh static run with no frozen instances so adding an Agent node is
	// a legal edit that reaches snapshot resolution.
	staticRun := &model.GraphRun{
		ID: model.NewGraphRunID(), JobID: "job-static-version-delete", WorkspaceID: "ws-1",
		Status: model.GraphRunStatusStopped, Progress: &model.GraphProgress{}, Resume: &model.GraphResumeState{},
		CurrentVersion: 1, CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	baseCfg := model.GraphConfig{
		Workdir: t.TempDir(),
		Nodes:   []model.GraphNode{node("s", model.GraphNodeTypeStart), node("e", model.GraphNodeTypeEnd)},
		Edges:   []model.GraphEdge{edge("s_e", "s", "e")},
	}
	staticRun.Versions = []model.GraphRunVersion{{Version: 1, Config: baseCfg}}
	if err := svc.runRepo.RegisterRun(context.Background(), staticRun); err != nil {
		t.Fatal(err)
	}
	if err := svc.persistRuntimeState(context.Background(), staticRun, map[string]model.GraphInstanceState{}, map[string]model.GraphEdgeState{}, map[string]map[string]string{}); err != nil {
		t.Fatal(err)
	}
	stale := staticRun
	req := &model.UpdateGraphRunVersionRequest{Config: cloneGraphConfig(baseCfg), Reason: "race"}
	req.Config.Nodes = []model.GraphNode{
		node("s", model.GraphNodeTypeStart),
		{ID: "new-prompt", Type: model.GraphNodeTypePrompt, Config: model.GraphNodeConfig{Prompt: "new", AgentType: "agent-x"}},
		node("e", model.GraphNodeTypeEnd),
	}
	req.Config.Edges = []model.GraphEdge{edge("s_new", "s", "new-prompt"), edge("new_e", "new-prompt", "e")}
	runner := &lifecycleSnapshotGateRunner{entered: make(chan struct{}), release: make(chan struct{})}
	updateDone := make(chan error, 1)
	go func() {
		_, updateErr := svc.UpdateRunVersion(context.Background(), staticRun.ID, req, runner)
		updateDone <- updateErr
	}()
	<-runner.entered
	if err := svc.DeleteRun(context.Background(), staticRun.ID, nil); err != nil {
		t.Fatalf("DeleteRun failed: %v", err)
	}
	jobDir := typepath.LocalJobDirInWorkspace(stale.WorkspaceID, stale.JobID)
	if err := os.RemoveAll(jobDir); err != nil {
		t.Fatal(err)
	}
	close(runner.release)
	if err := <-updateDone; !errors.Is(err, ErrGraphRunNotFound) {
		t.Fatalf("UpdateRunVersion error = %v, want ErrGraphRunNotFound", err)
	}
	if _, err := os.Stat(typepath.GraphRunFile(stale.WorkspaceID, stale.JobID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted run file was recreated: %v", err)
	}
	if _, err := os.Stat(jobDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("deleted job directory was recreated: %v", err)
	}
}

func TestDeleteRun_ArtifactFailureKeepsLinkAndRetryState(t *testing.T) {
	uniqueMemoryRoot(t)
	api, err := NewService()
	if err != nil {
		t.Fatal(err)
	}
	svc := api.(*serviceImpl)
	run := &model.GraphRun{
		ID: model.NewGraphRunID(), JobID: "job-artifact-retry", WorkspaceID: "ws-1",
		Status: model.GraphRunStatusStopped, Progress: &model.GraphProgress{}, Resume: &model.GraphResumeState{},
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := svc.runRepo.RegisterRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if err := svc.persistRuntimeState(context.Background(), run, map[string]model.GraphInstanceState{}, map[string]model.GraphEdgeState{}, map[string]map[string]string{}); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("forced graph artifact delete failure")
	svc.runRepo = &lifecycleFailOnceDeleteRepo{GraphRunRepo: svc.runRepo, fail: true, deleteErr: wantErr}
	sink := &retryableUnlinkSink{}

	if err := svc.DeleteRun(context.Background(), run.ID, sink); !errors.Is(err, wantErr) {
		t.Fatalf("first DeleteRun error = %v, want %v", err, wantErr)
	}
	if sink.calls != 0 {
		t.Fatalf("unlink calls after failed artifact delete = %d, want 0", sink.calls)
	}
	if _, err := svc.GetRunStatus(context.Background(), run.ID); err != nil {
		t.Fatalf("failed artifact deletion removed run: %v", err)
	}
	lifecycle := svc.lifecycle(run.ID)
	lifecycle.mu.Lock()
	deleted := lifecycle.deleted
	lifecycle.mu.Unlock()
	if deleted {
		t.Fatal("failed artifact delete left lifecycle fenced")
	}

	if err := svc.DeleteRun(context.Background(), run.ID, sink); err != nil {
		t.Fatalf("retry DeleteRun failed: %v", err)
	}
	if sink.calls != 1 {
		t.Fatalf("unlink calls after successful retry = %d, want 1", sink.calls)
	}
	if _, err := svc.GetRunStatus(context.Background(), run.ID); !errors.Is(err, ErrGraphRunNotFound) {
		t.Fatalf("run after successful retry = %v, want ErrGraphRunNotFound", err)
	}
}

func TestDeleteRun_RetryFinishesPartiallyDeletedArtifactsBeforeUnlink(t *testing.T) {
	uniqueMemoryRoot(t)
	api, err := NewService()
	if err != nil {
		t.Fatal(err)
	}
	svc := api.(*serviceImpl)
	run := &model.GraphRun{
		ID: model.NewGraphRunID(), JobID: "job-partial-artifact-retry", WorkspaceID: "ws-1",
		Status: model.GraphRunStatusStopped, Progress: &model.GraphProgress{}, Resume: &model.GraphResumeState{},
		CreatedAt: time.Now(), UpdatedAt: time.Now(),
	}
	if err := svc.runRepo.RegisterRun(context.Background(), run); err != nil {
		t.Fatal(err)
	}
	if err := svc.persistRuntimeState(context.Background(), run, map[string]model.GraphInstanceState{}, map[string]model.GraphEdgeState{}, map[string]map[string]string{}); err != nil {
		t.Fatal(err)
	}
	wantErr := errors.New("forced partial graph delete failure")
	repo := &lifecyclePartialDeleteRepo{
		GraphRunRepo: svc.runRepo, workspaceID: run.WorkspaceID, jobID: run.JobID, deleteErr: wantErr,
	}
	svc.runRepo = repo
	sink := &retryableUnlinkSink{}

	if err := svc.DeleteRun(context.Background(), run.ID, sink); !errors.Is(err, wantErr) {
		t.Fatalf("first DeleteRun error = %v, want %v", err, wantErr)
	}
	if sink.calls != 0 {
		t.Fatalf("unlink calls after partial artifact failure = %d, want 0", sink.calls)
	}
	if _, err := os.Stat(typepath.GraphRunFile(run.WorkspaceID, run.JobID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("fixture did not remove run.json: %v", err)
	}

	if err := svc.DeleteRun(context.Background(), run.ID, sink); err != nil {
		t.Fatalf("retry DeleteRun failed: %v", err)
	}
	if sink.calls != 1 {
		t.Fatalf("unlink calls after retry = %d, want 1", sink.calls)
	}
	if repo.calls != 2 {
		t.Fatalf("artifact delete calls = %d, want 2", repo.calls)
	}
	if _, err := os.Stat(typepath.GraphRunDir(run.WorkspaceID, run.JobID)); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial graph artifacts survived retry: %v", err)
	}
}

func TestOrdinaryControlsRejectPersistedTerminalWithLiveHandle(t *testing.T) {
	uniqueMemoryRoot(t)
	api, err := NewService()
	if err != nil {
		t.Fatal(err)
	}
	svc := api.(*serviceImpl)
	sink := &lifecycleTerminalGateSink{terminalEntered: make(chan struct{}), releaseTerminal: make(chan struct{})}
	cfg := linearShellCfg(t)
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-terminal-control", Config: &cfg}, stubGraphRunner{}, sink)
	if err != nil {
		t.Fatal(err)
	}
	<-sink.terminalEntered
	if svc.getControl(run.ID) == nil {
		t.Fatal("fixture requires a live post-terminal handle")
	}
	for name, call := range map[string]func() error{
		"stop":        func() error { _, err := svc.StopRun(context.Background(), run.ID, "late"); return err },
		"step-stop":   func() error { _, err := svc.StepStopRun(context.Background(), run.ID, "late"); return err },
		"cancel-stop": func() error { _, err := svc.CancelStopRun(context.Background(), run.ID, "late"); return err },
	} {
		if err := call(); !errors.Is(err, ErrGraphRunNotRunning) {
			t.Errorf("%s error = %v, want ErrGraphRunNotRunning", name, err)
		}
	}

	joined := make(chan error, 1)
	go func() {
		_, joinErr := svc.StopRunAndWait(context.Background(), run.ID, "join terminal")
		joined <- joinErr
	}()
	select {
	case err := <-joined:
		t.Fatalf("StopRunAndWait returned before post-terminal handle completed: %v", err)
	default:
	}
	close(sink.releaseTerminal)
	if err := <-joined; err != nil {
		t.Fatalf("StopRunAndWait failed: %v", err)
	}
}

func TestQueuedVersionUpdateReturnsWhenDestructiveStopRetiresGeneration(t *testing.T) {
	uniqueMemoryRoot(t)
	api, err := NewService()
	if err != nil {
		t.Fatal(err)
	}
	svc := api.(*serviceImpl)
	sink := &lifecycleRunningGateSink{runningEntered: make(chan struct{}), releaseRunning: make(chan struct{})}
	cfg := model.GraphConfig{
		Workdir: t.TempDir(),
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart), promptNode("p"), node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{edge("s_p", "s", "p"), edge("p_e", "p", "e")},
	}
	run, err := svc.StartRun(context.Background(), &model.StartGraphRunRequest{JobID: "job-update-stop", Config: &cfg}, stubGraphRunner{}, sink)
	if err != nil {
		t.Fatal(err)
	}
	<-sink.runningEntered
	lifecycle := svc.lifecycle(run.ID)
	handle := svc.getControl(run.ID)
	if handle == nil {
		t.Fatal("missing scheduler handle")
	}
	req := &model.UpdateGraphRunVersionRequest{Config: cloneGraphConfig(cfg), Reason: "queued update"}
	req.Config.Nodes[1].Title = "display-only change"
	gate := &lifecycleFirstGetGateRepo{GraphRunRepo: svc.runRepo, entered: make(chan struct{}), release: make(chan struct{})}
	svc.runRepo = gate
	updateCtx, cancelUpdate := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancelUpdate()
	updateDone := make(chan error, 1)
	go func() {
		_, updateErr := svc.UpdateRunVersion(updateCtx, run.ID, req, stubGraphRunner{})
		updateDone <- updateErr
	}()
	<-gate.entered

	// Barrier: UpdateRunVersion keeps lifecycle.mu until after enqueueing. Taking
	// it here therefore proves the update signal is already queued.
	close(gate.release)
	lifecycle.mu.Lock()
	queued := len(handle.controlCh)
	lifecycle.mu.Unlock()
	if queued != 1 {
		t.Fatalf("control queue length = %d, want one queued version update", queued)
	}

	stopDone := make(chan error, 1)
	go func() {
		_, stopErr := svc.StopRunAndWait(context.Background(), run.ID, "delete")
		stopDone <- stopErr
	}()
	close(sink.releaseRunning)
	if err := <-stopDone; err != nil {
		t.Fatalf("StopRunAndWait failed: %v", err)
	}
	if err := <-updateDone; errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("queued UpdateRunVersion was stranded until its deadline: %v", err)
	} else if err != nil && !errors.Is(err, ErrGraphRunNotEditable) {
		t.Fatalf("UpdateRunVersion error = %v, want success or ErrGraphRunNotEditable", err)
	}
}
