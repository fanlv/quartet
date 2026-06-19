package graph

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/services/usagestats"
	"github.com/fanlv/quartet/types/agui"
	"github.com/fanlv/quartet/types/model"
)

var (
	// ErrWorkflowNotFound is returned when a workflow ID does not resolve to a
	// live (non-soft-deleted) GraphWorkflow.
	ErrWorkflowNotFound = errors.New("graph workflow not found")
	// ErrInvalidGraphConfig wraps graph config validation failures so the HTTP
	// layer can return them with the full GraphValidationError list. The
	// per-error detail is carried separately via ValidationErrors.
	ErrInvalidGraphConfig = errors.New("invalid graph workflow config")
)

// ValidationError wraps the full list of GraphValidationError so the handler
// can surface every located error at once while still using errors.Is against
// ErrInvalidGraphConfig.
type ValidationError struct {
	Errors []model.GraphValidationError
}

func (e *ValidationError) Error() string {
	return fmt.Sprintf("%s: %d validation error(s)", ErrInvalidGraphConfig.Error(), len(e.Errors))
}

func (e *ValidationError) Is(target error) bool { return target == ErrInvalidGraphConfig }

// Service exposes Graph workflow config management. Runtime concerns (GraphRun
// execution, scheduling, sessions) live in other modules.
type Service interface {
	CreateWorkflow(ctx context.Context, req *model.CreateGraphWorkflowRequest) (*model.GraphWorkflow, error)
	GetWorkflow(ctx context.Context, id string) (*model.GraphWorkflow, error)
	ListWorkflows(ctx context.Context) ([]*model.GraphWorkflow, error)
	UpdateWorkflow(ctx context.Context, id string, req *model.UpdateGraphWorkflowRequest) (*model.GraphWorkflow, error)
	DeleteWorkflow(ctx context.Context, id string) error
	// ValidateConfig runs the full static legality check without persisting.
	ValidateConfig(ctx context.Context, cfg *model.GraphConfig) []model.GraphValidationError
	StartRun(ctx context.Context, req *model.StartGraphRunRequest, runner Runner, jobs JobStateSink) (*model.GraphRun, error)
	GetRunStatus(ctx context.Context, runID string) (*model.GraphRunStatusResponse, error)
	ListRunEvents(ctx context.Context, runID string, startLine int, count *int) (*model.GraphRunEventsResponse, error)
	ListRuns(ctx context.Context) ([]*model.GraphRun, error)
	// StopRun hard-stops a running GraphRun: in-flight instances are cancelled
	// and marked interrupted, the run becomes "stopped" and stays resumable.
	StopRun(ctx context.Context, runID string) (*model.GraphRun, error)
	// PauseRun gracefully pauses: no new instances dispatch, in-flight ones
	// finish, then the run becomes "paused" and stays resumable.
	PauseRun(ctx context.Context, runID string) (*model.GraphRun, error)
	// StepStopRun freezes the current ready batch and stops after its members
	// reach a terminal state; the run becomes "stepStopped" and stays resumable.
	StepStopRun(ctx context.Context, runID string) (*model.GraphRun, error)
	// ResumeRun re-launches a resumable GraphRun (failed/paused/stepStopped/
	// stopped/timedOut/recovering): succeeded/skipped instances are kept,
	// failed/interrupted ones are reset and rescheduled.
	ResumeRun(ctx context.Context, runID string, runner Runner, jobs JobStateSink) (*model.GraphRun, error)
	// DeleteRun deletes a non-in-flight GraphRun, cascading all of its run
	// artifacts and clearing the bound Job's GraphRunID linkage.
	DeleteRun(ctx context.Context, runID string, jobs JobStateSink) error
	// UpdateRunVersion appends a new graph version to a GraphRun after validating
	// the edit against the persisted run state (already-running/succeeded/skipped
	// instances and their config are immutable; paths/edges depended on by
	// completed instances may not be removed). The new version's referenced
	// Agent/model config content is frozen into the run. On a live run the
	// scheduler refreshes its effective topology, so not-yet-started instances
	// use the new version while in-flight and completed instances keep their
	// execution-time version.
	UpdateRunVersion(ctx context.Context, runID string, req *model.UpdateGraphRunVersionRequest, src Runner) (*model.GraphRun, error)
	// ReconcileRuns reconciles GraphRuns left in-flight by a process crash on
	// startup: their running instances are marked interrupted and the run is
	// moved to a resumable terminal state without re-executing anything.
	ReconcileRuns(ctx context.Context, jobs JobStateSink) error
	// SetUsageRecorder wires the optional usage-stats sink for Agent-class graph
	// nodes. Passing nil disables recording (used by tests).
	SetUsageRecorder(r usagestats.Recorder)
}

// controlSignalKind enumerates the run-control intents delivered to the
// scheduler goroutine over its control channel.
type controlSignalKind int

const (
	ctrlHardStop      controlSignalKind = iota // 硬停止
	ctrlPause                                  // 暂停 / 优雅停止
	ctrlStepStop                               // 步骤后停止
	ctrlUpdateVersion                          // 运行中追加图版本
)

type controlSignal struct {
	kind          controlSignalKind
	reason        string
	versionReq    *model.UpdateGraphRunVersionRequest
	versionRunner Runner
	versionResp   chan versionUpdateResult
}

type versionUpdateResult struct {
	run *model.GraphRun
	err error
}

// runControl is the per-run control handle: a buffered channel delivering
// control intents into the scheduler goroutine, plus the cancel func for the
// run's root context (used by hard stop).
type runControl struct {
	controlCh chan controlSignal
	cancel    context.CancelFunc
}

type serviceImpl struct {
	repo                    repository.GraphWorkflowRepo
	runRepo                 repository.GraphRunRepo
	transientRetryDelay     time.Duration
	rateLimitRetryBaseDelay time.Duration

	controlMu   sync.Mutex
	runControls map[string]*runControl

	usageMu       sync.RWMutex
	usageRecorder usagestats.Recorder
}

type Runner interface {
	InitSession(ctx context.Context, jobID string, overrides *model.SessionOverrides) (sessionID string, err error)
	// ForkSession creates a NEW independent session that inherits the upstream
	// session's context (§3 会话血缘: "复制上游上下文新建独立 session"). It mints a
	// fresh session via the same path as InitSession and copies the parent
	// session's persisted conversation history into it, so both engines reach an
	// equivalent session without reusing the parent session ID: eino loads the
	// copied history at run time, and a fresh ACP subprocess replays it. The
	// returned replayCount is the number of history messages copied (recorded in
	// the run's session lineage). A copy/replay failure returns an error so the
	// scheduler can fail the node start with full context.
	ForkSession(ctx context.Context, parentSessionID, jobID string, overrides *model.SessionOverrides) (sessionID string, replayCount int, err error)
	RunIteration(ctx context.Context, sessionID string, messages []*schema.Message, handler agui.EventHandler) error
	SessionModelID(sessionID string) string
	// ResolveModelSnapshot returns the current content of the model config bound
	// to modelID (string form), so a GraphRun can freeze it into its snapshot and
	// replay immune to later global model-config edits. ok=false when modelID is
	// empty or no live model resolves — the caller treats this as a degraded
	// (best-effort) snapshot rather than a start failure.
	ResolveModelSnapshot(ctx context.Context, modelID string) (model.ModelInstance, bool)
	// ResolveSystemPrompt returns the resolved (placeholder-expanded) system
	// prompt content at this instant, captured into Agent snapshots so a replay
	// uses the prompt the run actually executed against.
	ResolveSystemPrompt(ctx context.Context) (string, error)
}

// ShellSessionRecorder is an optional capability a Runner may implement to give
// Shell nodes their own session (Graph Shell 默认新开 session). The scheduler
// type-asserts the runner for it after a Shell node succeeds: when present, it
// mints a fresh session for the job and appends the script (user message) and
// the combined output (assistant message) as a shell-output transcript, then
// returns the new session id for GraphInstanceState.DisplaySessionID. This
// session is purely for display — it never participates in §3 会话血缘 lineage.
// A nil/zero return (or a runner that does not implement it) leaves the Shell
// node without a display session and is treated as best-effort, not a failure.
type ShellSessionRecorder interface {
	RecordShellSession(ctx context.Context, jobID, script, output string, startedAt, finishedAt int64) (sessionID string, err error)
}

type JobStateSink interface {
	SetGraphRunState(ctx context.Context, jobID, graphRunID string, status model.JobStatus, startedAt, finishedAt int64) error
	// ClearGraphRunLinkage detaches a Job from a deleted GraphRun, but only if
	// the Job is still bound to that exact run (it may have been re-bound to a
	// newer run since).
	ClearGraphRunLinkage(ctx context.Context, jobID, graphRunID string) error
}

func NewService() (Service, error) {
	repo, err := repository.NewGraphWorkflowRepo()
	if err != nil {
		return nil, fmt.Errorf("init graph workflow repo failed: %w", err)
	}
	runRepo, err := repository.NewGraphRunRepo()
	if err != nil {
		return nil, fmt.Errorf("init graph run repo failed: %w", err)
	}
	return &serviceImpl{
		repo:                    repo,
		runRepo:                 runRepo,
		transientRetryDelay:     defaultGraphTransientRetryDelay,
		rateLimitRetryBaseDelay: defaultGraphRateLimitRetryBaseDelay,
		runControls:             map[string]*runControl{},
	}, nil
}

// registerControl creates and stores a control handle for a run and returns a
// child context cancellable via the handle. Called before launching runGraph.
func (s *serviceImpl) registerControl(runID string, parent context.Context) (*runControl, context.Context) {
	ctx, cancel := context.WithCancel(parent)
	handle := &runControl{controlCh: make(chan controlSignal, 4), cancel: cancel}
	s.controlMu.Lock()
	s.runControls[runID] = handle
	s.controlMu.Unlock()
	return handle, ctx
}

// clearControl removes a run's control handle, but only if it is still the
// handle passed in (a resume may have registered a fresh one). Idempotent.
func (s *serviceImpl) clearControl(runID string, handle *runControl) {
	s.controlMu.Lock()
	if s.runControls[runID] == handle {
		delete(s.runControls, runID)
	}
	s.controlMu.Unlock()
}

// sendControl delivers a control signal to a running run's scheduler goroutine
// without blocking. Returns ErrGraphRunNotRunning if no scheduler is live.
func (s *serviceImpl) sendControl(runID string, sig controlSignal) error {
	s.controlMu.Lock()
	handle := s.runControls[runID]
	s.controlMu.Unlock()
	if handle == nil {
		return ErrGraphRunNotRunning
	}
	select {
	case handle.controlCh <- sig:
		return nil
	default:
		// A signal is already queued; the scheduler will observe the run-state
		// transition. Treat as accepted.
		return nil
	}
}

func (s *serviceImpl) ValidateConfig(_ context.Context, cfg *model.GraphConfig) []model.GraphValidationError {
	if cfg == nil {
		return []model.GraphValidationError{{
			Type:    model.GraphValidationErrorTypeStructure,
			Message: "config is required",
		}}
	}
	return validateConfig(cfg)
}

func (s *serviceImpl) SetUsageRecorder(r usagestats.Recorder) {
	s.usageMu.Lock()
	s.usageRecorder = r
	s.usageMu.Unlock()
}

func (s *serviceImpl) CreateWorkflow(ctx context.Context, req *model.CreateGraphWorkflowRequest) (*model.GraphWorkflow, error) {
	if req == nil {
		return nil, fmt.Errorf("request is required")
	}
	if errs := validateConfig(&req.Config); len(errs) > 0 {
		return nil, &ValidationError{Errors: errs}
	}
	now := time.Now()
	wf := &model.GraphWorkflow{
		ID:          model.NewGraphWorkflowID(),
		WorkspaceID: req.WorkspaceID,
		Name:        req.Name,
		Description: req.Description,
		Config:      req.Config,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.repo.Save(ctx, wf); err != nil {
		return nil, err
	}
	return wf, nil
}

func (s *serviceImpl) GetWorkflow(ctx context.Context, id string) (*model.GraphWorkflow, error) {
	wf, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, ErrWorkflowNotFound
	}
	if wf.Deleted {
		return nil, ErrWorkflowNotFound
	}
	return wf, nil
}

func (s *serviceImpl) ListWorkflows(ctx context.Context) ([]*model.GraphWorkflow, error) {
	return s.repo.List(ctx)
}

func (s *serviceImpl) UpdateWorkflow(ctx context.Context, id string, req *model.UpdateGraphWorkflowRequest) (*model.GraphWorkflow, error) {
	if req == nil {
		return nil, fmt.Errorf("request is required")
	}
	wf, err := s.repo.Get(ctx, id)
	if err != nil || wf.Deleted {
		return nil, ErrWorkflowNotFound
	}
	if req.Config != nil {
		if errs := validateConfig(req.Config); len(errs) > 0 {
			return nil, &ValidationError{Errors: errs}
		}
		wf.Config = *req.Config
	}
	if req.Name != nil {
		wf.Name = *req.Name
	}
	if req.Description != nil {
		wf.Description = *req.Description
	}
	wf.UpdatedAt = time.Now()
	if err := s.repo.Update(ctx, id, wf); err != nil {
		return nil, err
	}
	return wf, nil
}

func (s *serviceImpl) DeleteWorkflow(ctx context.Context, id string) error {
	wf, err := s.repo.Get(ctx, id)
	if err != nil || wf.Deleted {
		return ErrWorkflowNotFound
	}
	return s.repo.Delete(ctx, id)
}
