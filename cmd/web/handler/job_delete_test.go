package handler

import (
	"context"
	"errors"
	"net/http"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/cloudwego/hertz/pkg/app"
	"github.com/cloudwego/hertz/pkg/route/param"
	"github.com/fanlv/quartet/services/agent/acp"
	graphsvc "github.com/fanlv/quartet/services/graph"
	jobsvc "github.com/fanlv/quartet/services/job"
	"github.com/fanlv/quartet/services/session"
	workspacesvc "github.com/fanlv/quartet/services/workspace"
	"github.com/fanlv/quartet/types/agui"
	"github.com/fanlv/quartet/types/model"
	typepath "github.com/fanlv/quartet/types/path"
)

type deleteCallLog struct {
	mu    sync.Mutex
	calls []string
}

func (l *deleteCallLog) add(call string) {
	l.mu.Lock()
	l.calls = append(l.calls, call)
	l.mu.Unlock()
}

func (l *deleteCallLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.calls...)
}

type deleteJobService struct {
	jobsvc.Service
	mu        sync.Mutex
	job       *model.Job
	markErr   error
	deleteErr error
	log       *deleteCallLog
}

func (s *deleteJobService) Get(jobID string) (*model.Job, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.job == nil || s.job.ID != jobID {
		return nil, false
	}
	return s.job.DeepCopy(), true
}

func (s *deleteJobService) ListByWorkspace(workspaceID string) []*model.Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.job == nil || s.job.WorkspaceID != workspaceID {
		return nil
	}
	return []*model.Job{s.job.DeepCopy()}
}

func (s *deleteJobService) MarkDeleted(jobID string) error {
	s.log.add("job.mark-deleted")
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.markErr != nil {
		return s.markErr
	}
	if s.job != nil && s.job.ID == jobID {
		s.job.Deleted = true
	}
	return nil
}

func (s *deleteJobService) StopAndWait(string) { s.log.add("job.stop-and-wait") }
func (s *deleteJobService) Delete(string) error {
	s.log.add("job.delete")
	return s.deleteErr
}

type deleteGraphService struct {
	graphsvc.Service
	log               *deleteCallLog
	waitEntered       chan struct{}
	waitRelease       chan struct{}
	onStop            func()
	onStopAfterDelete bool
	forceInFlightOnce bool
	mu                sync.Mutex
	deleteCalls       int
}

type stopErrorGraphService struct {
	graphsvc.Service
	err error
}

type failFirstUnlinkJobService struct {
	jobsvc.Service
	err   error
	calls int
}

func (s *failFirstUnlinkJobService) ClearGraphRunLinkage(ctx context.Context, jobID, runID string) error {
	s.calls++
	if s.calls == 1 {
		return s.err
	}
	return s.Service.ClearGraphRunLinkage(ctx, jobID, runID)
}

func (s stopErrorGraphService) RegisterRunLocation(context.Context, string, string, string) error {
	return nil
}

func (s stopErrorGraphService) StopRun(context.Context, string, string) (*model.GraphRun, error) {
	return nil, s.err
}

func (s *deleteGraphService) RegisterRunLocation(context.Context, string, string, string) error {
	s.log.add("graph.register")
	return nil
}

func (s *deleteGraphService) StopRunAndWait(ctx context.Context, _, _ string) (*model.GraphRun, error) {
	s.log.add("graph.stop-and-wait.enter")
	s.mu.Lock()
	deleteAttempted := s.deleteCalls > 0
	s.mu.Unlock()
	if s.onStop != nil && (!s.onStopAfterDelete || deleteAttempted) {
		s.onStop()
	}
	if s.waitEntered != nil {
		close(s.waitEntered)
	}
	if s.waitRelease != nil {
		select {
		case <-s.waitRelease:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	s.log.add("graph.stop-and-wait.exit")
	return &model.GraphRun{Status: model.GraphRunStatusStopped}, nil
}

func (s *deleteGraphService) DeleteRun(context.Context, string, graphsvc.JobStateSink) error {
	s.log.add("graph.delete.attempt")
	s.mu.Lock()
	call := s.deleteCalls
	s.deleteCalls++
	s.mu.Unlock()
	if s.forceInFlightOnce && call == 0 {
		return graphsvc.ErrGraphRunInFlight
	}
	s.log.add("graph.delete.done")
	return nil
}

type deleteWorkspaceService struct {
	workspacesvc.Service
	workspace *model.Workspace
	log       *deleteCallLog
	markErr   error
}

func (s *deleteWorkspaceService) Get(id string) (*model.Workspace, bool) {
	if s.workspace == nil || s.workspace.ID != id {
		return nil, false
	}
	copy := *s.workspace
	return &copy, true
}

func (s *deleteWorkspaceService) MarkDeleted(string) error {
	s.log.add("workspace.mark-deleted")
	return s.markErr
}

func (s *deleteWorkspaceService) Delete(string) error {
	s.log.add("workspace.delete")
	return nil
}

func TestDeleteMarkedJob_GraphWaitsBeforeDestructiveCleanup(t *testing.T) {
	log := &deleteCallLog{}
	job := &model.Job{
		ID: "graph-job", WorkspaceID: "ws-delete", Mode: model.JobModeGraph,
		GraphRunID: "graph-run", Status: model.JobStatusRunning, Deleted: true,
	}
	jobs := &deleteJobService{job: job, log: log}
	graphs := &deleteGraphService{
		log: log, waitEntered: make(chan struct{}), waitRelease: make(chan struct{}),
	}
	h := &Handler{jobService: jobs, graphService: graphs, sessionServices: map[string]*sessionEntry{}}
	done := make(chan error, 1)
	go func() { done <- h.deleteMarkedJob(context.Background(), job.DeepCopy()) }()

	select {
	case <-graphs.waitEntered:
	case <-time.After(time.Second):
		t.Fatal("graph stop-and-wait was not entered")
	}
	if got := log.snapshot(); containsDeleteCall(got, "job.delete") || containsDeleteCall(got, "graph.delete.done") {
		t.Fatalf("destructive cleanup ran before graph join: calls=%v", got)
	}

	close(graphs.waitRelease)
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("deleteMarkedJob failed: %v", err)
		}
	case <-time.After(time.Second):
		t.Fatal("deleteMarkedJob did not finish after graph scheduler joined")
	}
	want := []string{
		"job.stop-and-wait",
		"graph.register",
		"graph.stop-and-wait.enter",
		"graph.stop-and-wait.exit",
		"graph.delete.attempt",
		"graph.delete.done",
		"job.delete",
	}
	if got := log.snapshot(); !equalDeleteCalls(got, want) {
		t.Fatalf("delete lifecycle calls = %v, want %v", got, want)
	}
}

func TestDeleteMarkedJob_RefreshesGraphBindingAfterTombstone(t *testing.T) {
	log := &deleteCallLog{}
	// The caller's pre-tombstone snapshot predates StartRun binding the graph.
	stale := &model.Job{ID: "graph-race", WorkspaceID: "ws-delete", Mode: model.JobModeInteractive}
	current := &model.Job{
		ID: stale.ID, WorkspaceID: stale.WorkspaceID, Mode: model.JobModeGraph,
		GraphRunID: "graph-run", Status: model.JobStatusRunning, Deleted: true,
	}
	h := &Handler{
		jobService:      &deleteJobService{job: current, log: log},
		graphService:    &deleteGraphService{log: log},
		sessionServices: map[string]*sessionEntry{},
	}

	if err := h.deleteMarkedJob(context.Background(), stale); err != nil {
		t.Fatalf("deleteMarkedJob failed: %v", err)
	}
	if got := log.snapshot(); !containsDeleteCall(got, "graph.delete.done") {
		t.Fatalf("refreshed graph binding was not fenced and deleted: calls=%v", got)
	}
}

func TestDeleteMarkedJob_CleansSessionCreatedByConcurrentResume(t *testing.T) {
	t.Setenv("LOCAL_MEMORY", t.TempDir())
	log := &deleteCallLog{}
	job := &model.Job{
		ID: "graph-resume-race", WorkspaceID: "ws-delete", Mode: model.JobModeGraph,
		GraphRunID: "graph-run", Status: model.JobStatusRunning, Deleted: true,
	}
	jobs := &deleteJobService{job: job, log: log}
	sessions, err := session.NewService(job.WorkspaceID, job.ID)
	if err != nil {
		t.Fatalf("create session service: %v", err)
	}
	lateSession, err := sessions.New("", "test", t.TempDir(), nil)
	if err != nil {
		t.Fatalf("create late session fixture: %v", err)
	}
	if err := sessions.SetInitFields(lateSession.ID, job.ID, job.WorkspaceID); err != nil {
		t.Fatalf("bind late session fixture: %v", err)
	}
	graphs := &deleteGraphService{
		log: log, forceInFlightOnce: true, onStopAfterDelete: true,
		onStop: func() {
			// Model the concurrently resumed generation publishing its newly
			// opened session while StopRunAndWait joins it.
			jobs.mu.Lock()
			jobs.job.GraphSessionIDs = append(jobs.job.GraphSessionIDs, lateSession.ID)
			jobs.mu.Unlock()
		},
	}
	h := &Handler{
		jobService: jobs, graphService: graphs, acpAgentService: acp.NewACPService(),
		sessionServices: map[string]*sessionEntry{job.ID: newSessionEntry(sessions)},
	}

	if err := h.deleteMarkedJob(context.Background(), job.DeepCopy()); err != nil {
		t.Fatalf("deleteMarkedJob failed: %v", err)
	}
	if _, ok := sessions.Get(lateSession.ID); ok {
		t.Fatal("session created by concurrent resume survived Job deletion")
	}
	want := []string{
		"job.stop-and-wait",
		"graph.register",
		"graph.stop-and-wait.enter",
		"graph.stop-and-wait.exit",
		"graph.delete.attempt",
		"graph.stop-and-wait.enter",
		"graph.stop-and-wait.exit",
		"graph.delete.attempt",
		"graph.delete.done",
		"job.delete",
	}
	if got := log.snapshot(); !equalDeleteCalls(got, want) {
		t.Fatalf("concurrent resume delete calls = %v, want %v", got, want)
	}
}

func TestDeleteMarkedJob_InteractiveStillUsesJobJoin(t *testing.T) {
	log := &deleteCallLog{}
	job := &model.Job{
		ID: "interactive-job", WorkspaceID: "ws-delete",
		Mode: model.JobModeInteractive, Status: model.JobStatusRunning, Deleted: true,
	}
	h := &Handler{
		jobService:      &deleteJobService{job: job, log: log},
		sessionServices: map[string]*sessionEntry{},
	}
	if err := h.deleteMarkedJob(context.Background(), job.DeepCopy()); err != nil {
		t.Fatalf("deleteMarkedJob failed: %v", err)
	}
	want := []string{"job.stop-and-wait", "job.delete"}
	if got := log.snapshot(); !equalDeleteCalls(got, want) {
		t.Fatalf("interactive delete calls = %v, want %v", got, want)
	}
}

func TestJobDelete_MarkDeletedFailureStopsLifecycle(t *testing.T) {
	log := &deleteCallLog{}
	markErr := errors.New("persist tombstone failed")
	job := &model.Job{ID: "job-mark-fails", WorkspaceID: "ws-delete"}
	h := &Handler{jobService: &deleteJobService{job: job, markErr: markErr, log: log}}
	c := requestContextWithParam("jobId", job.ID)

	h.JobDelete(context.Background(), c)

	if got := c.Response.StatusCode(); got != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body=%s", got, http.StatusInternalServerError, c.Response.Body())
	}
	want := []string{"job.mark-deleted"}
	if got := log.snapshot(); !equalDeleteCalls(got, want) {
		t.Fatalf("calls after MarkDeleted failure = %v, want %v", got, want)
	}
}

func TestJobDelete_PhysicalDeleteFailureIsReturned(t *testing.T) {
	log := &deleteCallLog{}
	deleteErr := errors.New("remove job directory failed")
	job := &model.Job{ID: "job-delete-fails", WorkspaceID: "ws-delete"}
	h := &Handler{
		jobService:      &deleteJobService{job: job, deleteErr: deleteErr, log: log},
		sessionServices: map[string]*sessionEntry{},
	}
	c := requestContextWithParam("jobId", job.ID)

	h.JobDelete(context.Background(), c)

	if got := c.Response.StatusCode(); got != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body=%s", got, http.StatusInternalServerError, c.Response.Body())
	}
	want := []string{"job.mark-deleted", "job.stop-and-wait", "job.delete"}
	if got := log.snapshot(); !equalDeleteCalls(got, want) {
		t.Fatalf("calls after physical delete failure = %v, want %v", got, want)
	}
}

func TestJobStop_GraphControlErrorsAreNotReportedAsSuccess(t *testing.T) {
	tests := []struct {
		name       string
		err        error
		wantStatus int
	}{
		{name: "control queue busy", err: graphsvc.ErrGraphRunControlBusy, wantStatus: http.StatusConflict},
		{name: "unexpected service failure", err: errors.New("graph storage unavailable"), wantStatus: http.StatusInternalServerError},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			log := &deleteCallLog{}
			job := &model.Job{
				ID: "job-stop-error", WorkspaceID: "ws-delete", Mode: model.JobModeGraph,
				GraphRunID: "graph-run", Status: model.JobStatusRunning,
			}
			h := &Handler{
				jobService:   &deleteJobService{job: job, log: log},
				graphService: stopErrorGraphService{err: tt.err},
			}
			c := requestContextWithParam("jobId", job.ID)

			h.JobStop(context.Background(), c)

			if got := c.Response.StatusCode(); got != tt.wantStatus {
				t.Fatalf("status = %d, want %d; body=%s", got, tt.wantStatus, c.Response.Body())
			}
			if got := log.snapshot(); containsDeleteCall(got, "job.stop-and-wait") {
				t.Fatalf("graph control rejection unexpectedly fell back to Job stop: calls=%v", got)
			}
		})
	}
}

func TestWorkspaceDelete_UsesGraphJoinBeforeCascadeDelete(t *testing.T) {
	log := &deleteCallLog{}
	workspace := &model.Workspace{ID: "ws-delete"}
	job := &model.Job{
		ID: "graph-job", WorkspaceID: workspace.ID, Mode: model.JobModeGraph,
		GraphRunID: "graph-run", Status: model.JobStatusRunning,
	}
	jobs := &deleteJobService{job: job, log: log}
	graphs := &deleteGraphService{
		log: log, waitEntered: make(chan struct{}), waitRelease: make(chan struct{}),
	}
	h := &Handler{
		jobService: jobs, graphService: graphs,
		workspaceService: &deleteWorkspaceService{workspace: workspace, log: log},
		sessionServices:  map[string]*sessionEntry{},
	}
	c := requestContextWithParam("id", workspace.ID)
	done := make(chan struct{})
	go func() {
		h.WorkspaceDelete(context.Background(), c)
		close(done)
	}()

	select {
	case <-graphs.waitEntered:
	case <-time.After(time.Second):
		t.Fatal("workspace cascade did not enter graph stop-and-wait")
	}
	if got := log.snapshot(); containsDeleteCall(got, "job.delete") || containsDeleteCall(got, "graph.delete.done") || containsDeleteCall(got, "workspace.delete") {
		t.Fatalf("cascade delete ran before graph join: calls=%v", got)
	}

	close(graphs.waitRelease)
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("WorkspaceDelete did not finish after graph scheduler joined")
	}
	if got := c.Response.StatusCode(); got != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", got, http.StatusOK, c.Response.Body())
	}
	want := []string{
		"workspace.mark-deleted",
		"job.mark-deleted",
		"job.stop-and-wait",
		"graph.register",
		"graph.stop-and-wait.enter",
		"graph.stop-and-wait.exit",
		"graph.delete.attempt",
		"graph.delete.done",
		"job.delete",
		"workspace.delete",
	}
	if got := log.snapshot(); !equalDeleteCalls(got, want) {
		t.Fatalf("workspace delete calls = %v, want %v", got, want)
	}
}

func TestWorkspaceDelete_JobTombstoneFailureStopsCascade(t *testing.T) {
	log := &deleteCallLog{}
	workspace := &model.Workspace{ID: "ws-delete"}
	job := &model.Job{ID: "job-mark-fails", WorkspaceID: workspace.ID}
	h := &Handler{
		jobService: &deleteJobService{
			job: job, markErr: errors.New("persist job tombstone failed"), log: log,
		},
		workspaceService: &deleteWorkspaceService{workspace: workspace, log: log},
	}
	c := requestContextWithParam("id", workspace.ID)

	h.WorkspaceDelete(context.Background(), c)

	if got := c.Response.StatusCode(); got != http.StatusInternalServerError {
		t.Fatalf("status = %d, want %d; body=%s", got, http.StatusInternalServerError, c.Response.Body())
	}
	want := []string{"workspace.mark-deleted", "job.mark-deleted"}
	if got := log.snapshot(); !equalDeleteCalls(got, want) {
		t.Fatalf("calls after Job MarkDeleted failure = %v, want %v", got, want)
	}
}

type graphDeleteBlockingRunner struct {
	started        chan struct{}
	cancelObserved chan struct{}
	release        chan struct{}
	sessionID      string
	startOnce      sync.Once
}

func (r *graphDeleteBlockingRunner) InitSession(context.Context, string, *model.SessionOverrides) (string, error) {
	return r.sessionID, nil
}

func (r *graphDeleteBlockingRunner) RunIteration(ctx context.Context, _ string, _ []*schema.Message, _ agui.EventHandler) error {
	if r.started != nil {
		r.startOnce.Do(func() { close(r.started) })
	}
	<-ctx.Done()
	close(r.cancelObserved)
	<-r.release
	return ctx.Err()
}

func (*graphDeleteBlockingRunner) SessionModelID(string) string { return "" }

func (*graphDeleteBlockingRunner) ResolveModelSnapshot(context.Context, string) (string, bool) {
	return "", false
}

// This integration test locks the original regression: a Graph runner may take
// time to unwind after cancellation. DELETE must stay blocked until it exits
// and the scheduler has completed its final persistence, then remove the whole
// Job directory without leaving a producer that can recreate graph_run.
func TestJobDelete_RunningGraphJoinsSchedulerBeforeRemovingDirectory(t *testing.T) {
	t.Setenv("LOCAL_MEMORY", t.TempDir())
	ctx := context.Background()
	workspaces, err := workspacesvc.NewService()
	if err != nil {
		t.Fatalf("create workspace service: %v", err)
	}
	workspace := model.NewWorkspace("Graph delete", "", t.TempDir())
	workspace.ID = "ws-graph-delete"
	if err := workspaces.Create(workspace); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	jobs, err := jobsvc.NewService(workspaces)
	if err != nil {
		t.Fatalf("create job service: %v", err)
	}
	graphs, err := graphsvc.NewService()
	if err != nil {
		t.Fatalf("create graph service: %v", err)
	}
	job := model.NewJob(workspace.Workdir, workspace.ID)
	job.ID = "job-running-graph-delete"
	if err := jobs.Create(job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	sessions, err := session.NewService(workspace.ID, job.ID)
	if err != nil {
		t.Fatalf("create session service: %v", err)
	}
	graphSession, err := sessions.New("", "test", workspace.Workdir, nil)
	if err != nil {
		t.Fatalf("create graph session: %v", err)
	}
	if err := sessions.SetInitFields(graphSession.ID, job.ID, workspace.ID); err != nil {
		t.Fatalf("bind graph session: %v", err)
	}
	if err := jobs.AttachGraphSession(ctx, job.ID, graphSession.ID); err != nil {
		t.Fatalf("attach graph session: %v", err)
	}

	runner := &graphDeleteBlockingRunner{
		started:        make(chan struct{}),
		cancelObserved: make(chan struct{}),
		release:        make(chan struct{}),
		sessionID:      graphSession.ID,
	}
	cfg := model.GraphConfig{
		WorkspaceID: workspace.ID,
		Workdir:     workspace.Workdir,
		Nodes: []model.GraphNode{
			{ID: "start", Type: model.GraphNodeTypeStart},
			{ID: "agent", Type: model.GraphNodeTypePrompt, Config: model.GraphNodeConfig{Prompt: "wait", AgentType: "test"}},
			{ID: "end", Type: model.GraphNodeTypeEnd},
		},
		Edges: []model.GraphEdge{
			{ID: "start-agent", SourceNodeID: "start", TargetNodeID: "agent"},
			{ID: "agent-end", SourceNodeID: "agent", TargetNodeID: "end"},
		},
	}
	run, err := graphs.StartRun(ctx, &model.StartGraphRunRequest{
		JobID: job.ID, WorkspaceID: workspace.ID, Workdir: workspace.Workdir, Config: &cfg,
	}, runner, jobs)
	if err != nil {
		t.Fatalf("start graph run: %v", err)
	}
	if run == nil {
		t.Fatal("StartRun returned a nil run")
	}

	select {
	case <-runner.started:
	case <-time.After(3 * time.Second):
		t.Fatal("graph worker did not start")
	}
	h := &Handler{
		jobService: jobs, graphService: graphs, workspaceService: workspaces,
		acpAgentService: acp.NewACPService(),
		sessionServices: map[string]*sessionEntry{job.ID: newSessionEntry(sessions)},
	}
	c := requestContextWithParam("jobId", job.ID)
	deleteDone := make(chan struct{})
	go func() {
		h.JobDelete(ctx, c)
		close(deleteDone)
	}()

	select {
	case <-runner.cancelObserved:
	case <-time.After(3 * time.Second):
		t.Fatal("DELETE did not cancel the running graph worker")
	}
	select {
	case <-deleteDone:
		t.Fatal("DELETE returned before the graph worker exited")
	default:
	}
	close(runner.release)
	select {
	case <-deleteDone:
	case <-time.After(3 * time.Second):
		t.Fatal("DELETE did not return after the graph scheduler exited")
	}
	if got := c.Response.StatusCode(); got != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", got, http.StatusOK, c.Response.Body())
	}
	if _, ok := jobs.Get(job.ID); ok {
		t.Fatal("job still exists after DELETE returned")
	}
	if _, ok := sessions.Get(graphSession.ID); ok {
		t.Fatal("graph session still exists after DELETE returned")
	}
	jobDir := typepath.LocalJobDirInWorkspace(workspace.ID, job.ID)
	if _, err := os.Stat(jobDir); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("job directory stat after DELETE = %v, want not-exist", err)
	}
	if _, err := graphs.GetRunStatus(ctx, run.ID); !errors.Is(err, graphsvc.ErrGraphRunNotFound) {
		t.Fatalf("GetRunStatus after DELETE = %v, want ErrGraphRunNotFound", err)
	}
}

// A scheduled Graph Job owns one scheduler concurrency slot. Deletion marks
// the Job first, but the graph's terminal callback must still be delivered
// exactly once so the schedule cannot remain stuck at maxConcurrent until a
// process restart.
func TestJobDelete_RunningScheduledGraphSignalsDoneExactlyOnce(t *testing.T) {
	t.Setenv("LOCAL_MEMORY", t.TempDir())
	ctx := context.Background()
	workspaces, err := workspacesvc.NewService()
	if err != nil {
		t.Fatalf("create workspace service: %v", err)
	}
	workspace := model.NewWorkspace("Scheduled graph delete", "", t.TempDir())
	workspace.ID = "ws-scheduled-graph-delete"
	if err := workspaces.Create(workspace); err != nil {
		t.Fatalf("create workspace: %v", err)
	}
	jobs, err := jobsvc.NewService(workspaces)
	if err != nil {
		t.Fatalf("create job service: %v", err)
	}
	graphs, err := graphsvc.NewService()
	if err != nil {
		t.Fatalf("create graph service: %v", err)
	}
	job := model.NewJob(workspace.Workdir, workspace.ID)
	job.ID = "job-scheduled-graph-delete"
	job.ScheduleID = "schedule-delete"
	if err := jobs.Create(job); err != nil {
		t.Fatalf("create job: %v", err)
	}
	var doneCalls int
	var doneMu sync.Mutex
	jobs.SetOnJobDone(func(*model.Job) {
		doneMu.Lock()
		doneCalls++
		doneMu.Unlock()
	})

	runner := &graphDeleteBlockingRunner{
		started: make(chan struct{}), cancelObserved: make(chan struct{}),
		release: make(chan struct{}), sessionID: "scheduled-session",
	}
	cfg := model.GraphConfig{
		WorkspaceID: workspace.ID, Workdir: workspace.Workdir,
		Nodes: []model.GraphNode{
			{ID: "start", Type: model.GraphNodeTypeStart},
			{ID: "agent", Type: model.GraphNodeTypePrompt, Config: model.GraphNodeConfig{Prompt: "wait", AgentType: "test"}},
			{ID: "end", Type: model.GraphNodeTypeEnd},
		},
		Edges: []model.GraphEdge{
			{ID: "start-agent", SourceNodeID: "start", TargetNodeID: "agent"},
			{ID: "agent-end", SourceNodeID: "agent", TargetNodeID: "end"},
		},
	}
	_, err = graphs.StartRun(ctx, &model.StartGraphRunRequest{
		JobID: job.ID, WorkspaceID: workspace.ID, Workdir: workspace.Workdir, Config: &cfg,
	}, runner, jobs)
	if err != nil {
		t.Fatalf("start graph run: %v", err)
	}
	select {
	case <-runner.started:
	case <-time.After(3 * time.Second):
		t.Fatal("scheduled graph worker did not start")
	}

	h := &Handler{
		jobService: jobs, graphService: graphs, workspaceService: workspaces,
		acpAgentService: acp.NewACPService(), sessionServices: map[string]*sessionEntry{},
	}
	c := requestContextWithParam("jobId", job.ID)
	deleted := make(chan struct{})
	go func() {
		h.JobDelete(ctx, c)
		close(deleted)
	}()
	select {
	case <-runner.cancelObserved:
	case <-time.After(3 * time.Second):
		t.Fatal("DELETE did not cancel scheduled graph runner")
	}
	close(runner.release)
	select {
	case <-deleted:
	case <-time.After(3 * time.Second):
		t.Fatal("DELETE did not finish")
	}
	if got := c.Response.StatusCode(); got != http.StatusOK {
		t.Fatalf("status = %d, want %d; body=%s", got, http.StatusOK, c.Response.Body())
	}
	doneMu.Lock()
	gotDoneCalls := doneCalls
	doneMu.Unlock()
	if gotDoneCalls != 1 {
		t.Fatalf("terminal callback count = %d, want 1", gotDoneCalls)
	}
}

func TestDeleteJobGraphRun_UnlinkFailureIsRetryable(t *testing.T) {
	t.Setenv("LOCAL_MEMORY", t.TempDir())
	ctx := context.Background()
	workspaces, err := workspacesvc.NewService()
	if err != nil {
		t.Fatal(err)
	}
	workspace := model.NewWorkspace("unlink retry", "", t.TempDir())
	workspace.ID = "ws-unlink-retry"
	if err := workspaces.Create(workspace); err != nil {
		t.Fatal(err)
	}
	jobs, err := jobsvc.NewService(workspaces)
	if err != nil {
		t.Fatal(err)
	}
	job := model.NewJob(workspace.Workdir, workspace.ID)
	job.ID = "job-unlink-retry"
	if err := jobs.Create(job); err != nil {
		t.Fatal(err)
	}
	graphs, err := graphsvc.NewService()
	if err != nil {
		t.Fatal(err)
	}
	cfg := model.GraphConfig{
		WorkspaceID: workspace.ID, Workdir: workspace.Workdir,
		Nodes: []model.GraphNode{
			{ID: "start", Type: model.GraphNodeTypeStart},
			{ID: "shell", Type: model.GraphNodeTypeShell, Config: model.GraphNodeConfig{Script: "true"}},
			{ID: "end", Type: model.GraphNodeTypeEnd},
		},
		Edges: []model.GraphEdge{
			{ID: "start-shell", SourceNodeID: "start", TargetNodeID: "shell"},
			{ID: "shell-end", SourceNodeID: "shell", TargetNodeID: "end"},
		},
	}
	run, err := graphs.StartRun(ctx, &model.StartGraphRunRequest{
		JobID: job.ID, WorkspaceID: workspace.ID, Workdir: workspace.Workdir, Config: &cfg,
	}, &graphDeleteBlockingRunner{
		started: make(chan struct{}), cancelObserved: make(chan struct{}), release: make(chan struct{}), sessionID: "unused",
	}, jobs)
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.After(3 * time.Second)
	for {
		status, statusErr := graphs.GetRunStatus(ctx, run.ID)
		if statusErr == nil && status.Run.Status == model.GraphRunStatusCompleted {
			break
		}
		select {
		case <-deadline:
			t.Fatal("graph run did not complete")
		case <-time.After(time.Millisecond):
		}
	}
	if _, err := graphs.StopRunAndWait(ctx, run.ID, "join completed run"); err != nil {
		t.Fatal(err)
	}

	wantErr := errors.New("forced unlink failure")
	failingJobs := &failFirstUnlinkJobService{Service: jobs, err: wantErr}
	h := &Handler{jobService: failingJobs, graphService: graphs}
	first := requestContextWithParam("jobId", job.ID)
	h.DeleteJobGraphRun(ctx, first)
	if got := first.Response.StatusCode(); got != http.StatusInternalServerError {
		t.Fatalf("first status = %d, want 500; body=%s", got, first.Response.Body())
	}
	if linked, ok := jobs.Get(job.ID); !ok || linked.GraphRunID != run.ID {
		t.Fatalf("failed unlink changed Job linkage: %#v", linked)
	}
	if _, err := graphs.GetRunStatus(ctx, run.ID); !errors.Is(err, graphsvc.ErrGraphRunNotFound) {
		t.Fatalf("first delete phase did not remove GraphRun artifacts: %v", err)
	}

	// Model a process restart between the two phases. resolveJobGraphRun will
	// register the stale Job linkage with the new graph service, which must be
	// sufficient to finish unlinking even though run.json is already gone.
	graphs, err = graphsvc.NewService()
	if err != nil {
		t.Fatalf("restart graph service: %v", err)
	}
	h.graphService = graphs
	second := requestContextWithParam("jobId", job.ID)
	h.DeleteJobGraphRun(ctx, second)
	if got := second.Response.StatusCode(); got != http.StatusOK {
		t.Fatalf("retry status = %d, want 200; body=%s", got, second.Response.Body())
	}
	if linked, ok := jobs.Get(job.ID); !ok || linked.GraphRunID != "" {
		t.Fatalf("retry did not clear Job linkage: %#v", linked)
	}
	if _, err := graphs.GetRunStatus(ctx, run.ID); !errors.Is(err, graphsvc.ErrGraphRunNotFound) {
		t.Fatalf("retry did not remove GraphRun: %v", err)
	}
}

func requestContextWithParam(key, value string) *app.RequestContext {
	c := app.NewContext(1)
	c.Params = append(c.Params, param.Param{Key: key, Value: value})
	return c
}

func containsDeleteCall(calls []string, want string) bool {
	for _, call := range calls {
		if call == want {
			return true
		}
	}
	return false
}

func equalDeleteCalls(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}
	for i := range got {
		if got[i] != want[i] {
			return false
		}
	}
	return true
}
