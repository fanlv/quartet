package graph

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/pkg/fileserver"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/repository"
	jobsvc "github.com/fanlv/quartet/services/job"
	"github.com/fanlv/quartet/services/usagestats"
	workspacesvc "github.com/fanlv/quartet/services/workspace"
	"github.com/fanlv/quartet/types/agui"
	"github.com/fanlv/quartet/types/consts"
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
	// ErrWorkflowConflict is returned when an update is based on a stale
	// workflow snapshot, usually from another open tab.
	ErrWorkflowConflict = errors.New("graph workflow has been modified")
	// ErrWorkflowBadRequest is returned when request parameters are invalid.
	ErrWorkflowBadRequest = errors.New("invalid graph workflow request")
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

func normalizeWorkflowWorkspace(wf *model.GraphWorkflow, fallback string) {
	if wf == nil {
		return
	}
	wsID := workflowWorkspaceID(wf, fallback)
	wf.WorkspaceID = wsID
	wf.Config.WorkspaceID = wsID
}

func workflowWorkspaceID(wf *model.GraphWorkflow, fallback string) string {
	if wf == nil {
		return fallback
	}
	return firstNonEmpty(wf.WorkspaceID, wf.Config.WorkspaceID, fallback)
}

// Service exposes Graph workflow config management. Runtime concerns (GraphRun
// execution, scheduling, sessions) live in other modules.
type Service interface {
	CreateWorkflow(ctx context.Context, req *model.CreateGraphWorkflowRequest) (*model.GraphWorkflow, error)
	GetWorkflow(ctx context.Context, id string) (*model.GraphWorkflow, error)
	ListWorkflows(ctx context.Context) ([]*model.GraphWorkflow, []model.GraphWorkflowWarning, error)
	UpdateWorkflow(ctx context.Context, id string, req *model.UpdateGraphWorkflowRequest) (*model.GraphWorkflow, error)
	DeleteWorkflow(ctx context.Context, id string, expectedUpdatedAt *time.Time) error
	CreateRunJob(ctx context.Context, req *model.StartGraphRunRequest, jobs jobsvc.Service, workspaces workspacesvc.Service) (*model.Job, error)
	CreateScheduledRunJob(ctx context.Context, task *model.ScheduledTask, jobs jobsvc.Service, workspaces workspacesvc.Service) (*model.Job, error)
	// ValidateConfig runs the full static legality check without persisting.
	ValidateConfig(ctx context.Context, cfg *model.GraphConfig) []model.GraphValidationError
	StartRun(ctx context.Context, req *model.StartGraphRunRequest, runner Runner, jobs JobStateSink) (*model.GraphRun, error)
	RegisterRunLocation(ctx context.Context, runID, workspaceID, jobID string) error
	GetRunStatus(ctx context.Context, runID string) (*model.GraphRunStatusResponse, error)
	ListRunEvents(ctx context.Context, runID string, startLine int, count *int) (*model.GraphRunEventsResponse, error)
	// ListReplayEvents reads at most limit persistable structural events from
	// startLine for SSE disk-replay (a run with no live buffer in this process).
	// It bounds the read so a pathologically large event log can't be streamed in
	// full, and filters out any agent streaming deltas that may linger in legacy
	// logs. The canvas is rebuilt from the run snapshot, so this stream is only a
	// best-effort structural backfill.
	ListReplayEvents(ctx context.Context, runID string, startLine, limit int) (*model.GraphRunEventsResponse, error)
	// ListHookResults projects a run's persisted hook (completed/failed) events
	// into per-node results for the run-view node-detail panel, keeping the latest
	// result per node.
	ListHookResults(ctx context.Context, runID string) (*model.GraphHookResultsResponse, error)
	// SubscribeRunEvents attaches an SSE reader to a live run's in-memory event
	// buffer, resuming after startSeq. live=false means no buffer exists in this
	// process (run not active here, e.g. after a restart) — the caller degrades
	// to replaying persisted structural events from disk. A non-nil error means
	// the resume point has been GC'd (caller maps to HTTP 410). The returned
	// reader must be Closed by the caller.
	SubscribeRunEvents(runID string, startSeq uint64) (reader GraphEventReader, live bool, err error)
	// RunEventSnapshotSeq returns the seq a fresh subscriber should resume from
	// for a live run, and ok=false when no live buffer exists.
	RunEventSnapshotSeq(runID string) (seq uint64, ok bool)
	// StopRun hard-stops a running GraphRun: in-flight instances are cancelled
	// and marked interrupted, the run becomes "stopped" and stays resumable.
	StopRun(ctx context.Context, runID, reason string) (*model.GraphRun, error)
	// StepStopRun freezes the current ready batch and stops after its members
	// reach a terminal state; the run becomes "stepStopped" and stays resumable.
	StepStopRun(ctx context.Context, runID, reason string) (*model.GraphRun, error)
	// CancelStopRun cancels a pending step-stop that has not yet settled (run
	// still in stepStopping): the held dispatch frontier is released and the run
	// returns to "running".
	CancelStopRun(ctx context.Context, runID, reason string) (*model.GraphRun, error)
	// ResumeRun re-launches a resumable GraphRun (failed/stepStopped/stopped/
	// timedOut/recovering): succeeded/skipped instances are kept,
	// failed/interrupted ones are reset and rescheduled.
	ResumeRun(ctx context.Context, runID string, runner Runner, jobs JobStateSink) (*model.GraphRun, error)
	// ContinueRun continues a GraphRun parked at awaitingInput (§ 交互澄清结点): it
	// finalizes the clarify instances (capturing each session's discussion 结论
	// into their output variables), resolves their held out-edges, and resumes the
	// DAG. Rejected unless the run is in awaitingInput.
	ContinueRun(ctx context.Context, runID string, runner Runner, jobs JobStateSink) (*model.GraphRun, error)
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
	// SetUsageRecorder wires the optional usage-stats sink for Agent-class graph
	// nodes. Passing nil disables recording (used by tests).
	SetUsageRecorder(r usagestats.Recorder)
	// SetEndHookScriptProvider wires the getter for the global default End-node
	// hook script (settings.GraphEndHookScript). Read at hook time so a mid-run
	// edit takes effect; nil (or returning "") disables the "default" End hook.
	SetEndHookScriptProvider(fn func() string)
	// SetJobStateSink wires the persistent Job state sink used by the
	// orphan-reconcile paths (StopRun's no-scheduler fallback and
	// ReconcileInterruptedRun) that run without a live scheduler. Set once at
	// startup, before serving.
	SetJobStateSink(jobs JobStateSink)
	// ReconcileInterruptedRun repairs a run left in an in-flight status by a
	// crash/restart: no live scheduler can exist for it after boot, so its
	// still-running instances are marked interrupted and the run is moved to
	// `recovering` (a static, resumable state) with the bound Job set to a
	// non-running status. No-op for runs already in a settled status. Called
	// once per graph job at startup.
	ReconcileInterruptedRun(ctx context.Context, runID string) error
}

// controlSignalKind enumerates the run-control intents delivered to the
// scheduler goroutine over its control channel.
type controlSignalKind int

const (
	ctrlHardStop      controlSignalKind = iota // 硬停止
	ctrlStepStop                               // 步骤后停止
	ctrlCancelStop                             // 取消待生效的步骤后停止
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

	// bufMu guards eventBufs, the per-run in-memory SSE event buffers. A live
	// run publishes its streaming agent events (and a copy of structural
	// events) here for real-time delivery; the buffer is absent for runs not
	// active in this process (e.g. after restart), and the SSE handler degrades
	// to replaying persisted structural events from disk.
	bufMu     sync.Mutex
	eventBufs map[string]*graphEventBuffer

	usageMu       sync.RWMutex
	usageRecorder usagestats.Recorder

	// endHookScriptFn returns the global default End-node hook script. Set once
	// at startup via SetEndHookScriptProvider (before any run); read at hook
	// time. nil → no "default" End hook.
	endHookScriptFn func() string

	// jobSink is the persistent Job state sink used by the orphan-reconcile
	// paths (StopRun's no-scheduler fallback and ReconcileInterruptedRun) that
	// must write the bound Job's status without a live scheduler carrying a
	// per-run sink. Set once at startup via SetJobStateSink. nil disables the
	// job-side reconcile (the GraphRun status is still repaired).
	jobSink JobStateSink
}

type Runner interface {
	InitSession(ctx context.Context, jobID string, overrides *model.SessionOverrides) (sessionID string, err error)
	RunIteration(ctx context.Context, sessionID string, messages []*schema.Message, handler agui.EventHandler) error
	SessionModelID(sessionID string) string
	// ResolveModelSnapshot returns the display form of the model bound to
	// modelID, so a GraphRun can freeze it into its snapshot. The numeric eino
	// model-config store is gone; a graph node's ModelID is now the ACP model
	// identifier, which is already display-ready and snapshotted as-is.
	// ok=false when modelID is empty — the caller treats this as a degraded
	// (best-effort) snapshot rather than a start failure.
	ResolveModelSnapshot(ctx context.Context, modelID string) (string, bool)
}

// ShellSessionRecorder is an optional capability a Runner may implement to give
// Shell nodes their own session (Graph Shell 默认新开 session). Recording is
// split in two so the session — and the script as its user message — surfaces
// the instant the node starts rather than only after the shell exits:
//
//   - BeginShellSession is called by the scheduler when a Shell node is enqueued
//     (before the shell runs). It mints a fresh session for the job, appends the
//     script as the user message, and returns the new session id for
//     GraphInstanceState.DisplaySessionID so the Chat sidebar lists it
//     immediately (a slow shell no longer hides the session for its whole run).
//   - FinishShellSession is called when the node completes (success or failure)
//     to append the combined stdout/stderr as the assistant message.
//
// This session is purely for display — it never participates in §3 会话血缘
// lineage. A runner that does not implement it, or a Begin failure, leaves the
// Shell node without a display session and is treated as best-effort, not a
// node failure.
type ShellSessionRecorder interface {
	BeginShellSession(ctx context.Context, jobID, script string, startedAt int64) (sessionID string, err error)
	FinishShellSession(ctx context.Context, jobID, sessionID, output string, startedAt, finishedAt int64) error
}

// PromptUserMessageRecorder is an optional capability a Runner may implement so
// an Agent-class node (Prompt/Clarify) can persist its rendered prompt as the
// session's user message at enqueue time — before the agent subprocess spawns
// and starts replying. Without it, the user message is only written inside the
// agent's Run (ctxManager.BeginRun), which for a freshly-minted ACP session
// sits behind subprocess warmup, so the Chat sidebar lists the session but
// shows nothing for the whole startup. Recording it up front makes the
// auto-sent prompt visible the instant the node starts (the Chat reconcile
// path reloads history on "session opened").
//
// The recorded message is tagged in-memory with msgextra.KeyPrePersisted when
// handed to RunIteration so BeginRun knows it is already on disk and must not
// append it again (see chatctx.BeginRun). Only used for `new`/unset session
// strategy — never `inherit`, whose continuation session would otherwise drift
// the ACP fingerprint and force a needless subprocess reset every turn. Like
// ShellSessionRecorder this is best-effort: a missing implementation or a
// record failure leaves the node to persist normally inside its Run.
type PromptUserMessageRecorder interface {
	RecordPromptUserMessage(ctx context.Context, jobID, sessionID, content string, startedAt int64) error
}

// SessionLastAssistantReader is an optional capability a Runner may implement so
// the graph engine can read the latest assistant reply of a session — used by
// the Clarify node (§ 交互澄清结点) to capture the「讨论结论」at continue time: the
// authoritative result of an open-ended discussion is the session's last
// assistant message, written into _last_assistant_msg / the optional alias /
// declared output variables when the user clicks「讨论完成」. Returns the trimmed
// content of the last non-empty assistant message, or "" (ok=false) when the
// session has no assistant turn yet (e.g. a clarify node opened with no initial
// prompt that the user continued without ever getting a reply). Best-effort: a
// missing implementation or a read error degrades to an empty 结论, not a failure.
type SessionLastAssistantReader interface {
	SessionLastAssistantMessage(ctx context.Context, jobID, sessionID string) (content string, ok bool, err error)
}

type JobStateSink interface {
	SetGraphRunState(ctx context.Context, jobID, graphRunID string, status model.JobStatus, startedAt, finishedAt int64) error
	// ClearGraphRunLinkage detaches a Job from a deleted GraphRun, but only if
	// the Job is still bound to that exact run (it may have been re-bound to a
	// newer run since).
	ClearGraphRunLinkage(ctx context.Context, jobID, graphRunID string) error
	// AttachGraphSession records an Agent node's session on the job's
	// GraphSessionIDs whitelist (de-duplicated) so an interactive message may
	// later target it — letting a user keep chatting in a finished node's
	// session after the run stops. Best-effort and idempotent; kept off
	// SessionIDs to preserve that field's linear-iteration semantics.
	AttachGraphSession(ctx context.Context, jobID, sessionID string) error
	// JobTitle returns the bound Job's display title for hook env injection
	// ($QUARTET_JOB_TITLE). Best-effort: an unknown job (or any lookup miss)
	// returns "" rather than an error, since hooks only log-and-continue.
	JobTitle(ctx context.Context, jobID string) string
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
		eventBufs:               map[string]*graphEventBuffer{},
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

// eventBuffer returns the run's in-memory event buffer, creating it on first
// use. Called from the scheduler goroutine (runGraph entry, event publish) so a
// live run always has a buffer to broadcast through.
func (s *serviceImpl) eventBuffer(runID string) *graphEventBuffer {
	s.bufMu.Lock()
	defer s.bufMu.Unlock()
	buf := s.eventBufs[runID]
	if buf == nil {
		buf = newGraphEventBuffer(runID)
		s.eventBufs[runID] = buf
	}
	return buf
}

// getBuffer returns the run's live buffer, or nil when none exists in this
// process (run not active here — e.g. after a restart, or already deleted). The
// SSE handler uses nil to switch to its disk-replay degraded path.
func (s *serviceImpl) getBuffer(runID string) *graphEventBuffer {
	s.bufMu.Lock()
	defer s.bufMu.Unlock()
	return s.eventBufs[runID]
}

// removeBuffer closes and forgets a run's buffer. Called when the run is
// deleted. Idempotent.
func (s *serviceImpl) removeBuffer(runID string) {
	s.bufMu.Lock()
	buf := s.eventBufs[runID]
	delete(s.eventBufs, runID)
	s.bufMu.Unlock()
	if buf != nil {
		buf.Close()
	}
}

// SubscribeRunEvents attaches a new SSE reader to a live run's event buffer at
// startSeq. live=false means no live buffer exists in this process (caller
// degrades to disk replay); a non-nil error means the resume point is gone
// (caller maps to HTTP 410).
func (s *serviceImpl) SubscribeRunEvents(runID string, startSeq uint64) (GraphEventReader, bool, error) {
	buf := s.getBuffer(runID)
	if buf == nil {
		return nil, false, nil
	}
	r, err := buf.Subscribe(startSeq)
	if err != nil {
		return nil, true, err
	}
	return r, true, nil
}

// RunEventSnapshotSeq returns the resume seq a fresh subscriber should use for a
// live run, and ok=false when no live buffer exists in this process.
func (s *serviceImpl) RunEventSnapshotSeq(runID string) (uint64, bool) {
	buf := s.getBuffer(runID)
	if buf == nil {
		return 0, false
	}
	return buf.SnapshotSeq(), true
}

// sendControl delivers a control signal to a running run's scheduler goroutine
// without blocking. Returns ErrGraphRunNotRunning if no scheduler is live and
// ErrGraphRunControlBusy if the signal was not accepted.
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
		return ErrGraphRunControlBusy
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

// SetEndHookScriptProvider wires the global default End-node hook script getter.
// Called once at startup (handler wiring) before any run launches, so it needs
// no locking against runs.
func (s *serviceImpl) SetEndHookScriptProvider(fn func() string) {
	s.endHookScriptFn = fn
}

// SetJobStateSink wires the persistent Job state sink used by orphan-reconcile
// paths (StopRun fallback, ReconcileInterruptedRun) that run without a live
// scheduler. Called once at startup before any run launches, so it needs no
// locking against runs.
func (s *serviceImpl) SetJobStateSink(jobs JobStateSink) {
	s.jobSink = jobs
}

func (s *serviceImpl) CreateWorkflow(ctx context.Context, req *model.CreateGraphWorkflowRequest) (*model.GraphWorkflow, error) {
	if req == nil {
		return nil, fmt.Errorf("request is required")
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		return nil, fmt.Errorf("%w: name is required", ErrWorkflowBadRequest)
	}
	// Type is a closed enum (empty defaults to user). Reject any other value at
	// creation so a bogus type can never reach disk — a workflow with an
	// unknown type would be invisible in the UI (neither the user nor the agent
	// tab matches it).
	wfType := model.GraphWorkflowType(firstNonEmpty(string(req.Type), string(model.GraphWorkflowTypeUser)))
	if wfType != model.GraphWorkflowTypeUser && wfType != model.GraphWorkflowTypeAgent {
		return nil, fmt.Errorf("%w: invalid type %q (must be %q or %q)", ErrWorkflowBadRequest, req.Type, model.GraphWorkflowTypeUser, model.GraphWorkflowTypeAgent)
	}
	if errs := validateConfig(&req.Config); len(errs) > 0 {
		return nil, &ValidationError{Errors: errs}
	}
	now := time.Now()
	config := req.Config
	workspaceID := firstNonEmpty(req.WorkspaceID, config.WorkspaceID)
	config.WorkspaceID = workspaceID
	wf := &model.GraphWorkflow{
		ID:          model.NewGraphWorkflowID(),
		WorkspaceID: workspaceID,
		Name:        name,
		Description: req.Description,
		Type:        wfType,
		Config:      config,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	normalizeWorkflowWorkspace(wf, "")
	if err := s.repo.Save(ctx, wf); err != nil {
		return nil, err
	}
	return wf, nil
}

func (s *serviceImpl) GetWorkflow(ctx context.Context, id string) (*model.GraphWorkflow, error) {
	wf, err := s.repo.Get(ctx, id)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrWorkflowNotFound
	}
	if isInvalidIDError(err) {
		return nil, fmt.Errorf("%w: workflowId %q: %v", ErrWorkflowBadRequest, id, err)
	}
	if err != nil {
		return nil, fmt.Errorf("load graph workflow %s failed: %w", id, err)
	}
	if wf.Deleted {
		return nil, ErrWorkflowNotFound
	}
	return wf, nil
}

func (s *serviceImpl) ListWorkflows(ctx context.Context) ([]*model.GraphWorkflow, []model.GraphWorkflowWarning, error) {
	return s.repo.List(ctx)
}

func (s *serviceImpl) UpdateWorkflow(ctx context.Context, id string, req *model.UpdateGraphWorkflowRequest) (*model.GraphWorkflow, error) {
	if req == nil {
		return nil, fmt.Errorf("request is required")
	}
	if req.UpdatedAt == nil {
		return nil, fmt.Errorf("%w: updatedAt is required", ErrWorkflowBadRequest)
	}
	wf, err := s.repo.Get(ctx, id)
	if errors.Is(err, os.ErrNotExist) {
		return nil, ErrWorkflowNotFound
	}
	if isInvalidIDError(err) {
		return nil, fmt.Errorf("%w: workflowId %q: %v", ErrWorkflowBadRequest, id, err)
	}
	if err != nil {
		return nil, fmt.Errorf("load graph workflow %s failed: %w", id, err)
	}
	if wf.Deleted {
		return nil, ErrWorkflowNotFound
	}
	if req.UpdatedAt != nil && !wf.UpdatedAt.Equal(*req.UpdatedAt) {
		return nil, fmt.Errorf("%w: current updatedAt=%s, request updatedAt=%s", ErrWorkflowConflict, wf.UpdatedAt.Format(time.RFC3339Nano), req.UpdatedAt.Format(time.RFC3339Nano))
	}
	workspaceID := workflowWorkspaceID(wf, "")
	if req.WorkspaceID != nil {
		workspaceID = strings.TrimSpace(*req.WorkspaceID)
		wf.WorkspaceID = workspaceID
	}
	if req.Config != nil {
		if errs := validateConfig(req.Config); len(errs) > 0 {
			return nil, &ValidationError{Errors: errs}
		}
		config := *req.Config
		config.WorkspaceID = workspaceID
		wf.Config = config
	}
	normalizeWorkflowWorkspace(wf, "")
	// Type is a create-time, immutable library tag — UpdateGraphWorkflowRequest
	// carries no Type, so it stays as loaded. repo.Get already normalizes an
	// empty (legacy) type to "user"; guard once more so the persisted value is
	// always canonical even if a future caller bypasses that path.
	if wf.Type == "" {
		wf.Type = model.GraphWorkflowTypeUser
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			return nil, fmt.Errorf("%w: name is required", ErrWorkflowBadRequest)
		}
		wf.Name = name
	}
	if req.Description != nil {
		wf.Description = *req.Description
	}
	wf.UpdatedAt = time.Now()
	if err := s.repo.UpdateIfUnchanged(ctx, id, wf, req.UpdatedAt); err != nil {
		if errors.Is(err, repository.ErrGraphWorkflowVersionConflict) {
			return nil, fmt.Errorf("%w: workflow was changed before this update could be saved", ErrWorkflowConflict)
		}
		if errors.Is(err, os.ErrNotExist) {
			return nil, ErrWorkflowNotFound
		}
		if isInvalidIDError(err) {
			return nil, fmt.Errorf("%w: workflowId %q: %v", ErrWorkflowBadRequest, id, err)
		}
		return nil, err
	}
	return wf, nil
}

func (s *serviceImpl) DeleteWorkflow(ctx context.Context, id string, expectedUpdatedAt *time.Time) error {
	if expectedUpdatedAt == nil {
		return fmt.Errorf("%w: updatedAt is required", ErrWorkflowBadRequest)
	}
	wf, err := s.repo.Get(ctx, id)
	if errors.Is(err, os.ErrNotExist) {
		return ErrWorkflowNotFound
	}
	if isInvalidIDError(err) {
		return fmt.Errorf("%w: workflowId %q: %v", ErrWorkflowBadRequest, id, err)
	}
	if err != nil {
		return fmt.Errorf("load graph workflow %s failed: %w", id, err)
	}
	if wf.Deleted {
		return ErrWorkflowNotFound
	}
	if err := s.repo.Delete(ctx, id, expectedUpdatedAt); err != nil {
		if errors.Is(err, repository.ErrGraphWorkflowVersionConflict) {
			return fmt.Errorf("%w: workflow was changed before this delete could be saved", ErrWorkflowConflict)
		}
		if errors.Is(err, os.ErrNotExist) {
			return ErrWorkflowNotFound
		}
		if isInvalidIDError(err) {
			return fmt.Errorf("%w: workflowId %q: %v", ErrWorkflowBadRequest, id, err)
		}
		return err
	}
	return nil
}

func (s *serviceImpl) CreateRunJob(ctx context.Context, req *model.StartGraphRunRequest, jobs jobsvc.Service, workspaces workspacesvc.Service) (*model.Job, error) {
	if req == nil {
		return nil, fmt.Errorf("request is required")
	}
	if jobs == nil {
		return nil, fmt.Errorf("job service is required")
	}
	_, cfg, err := s.resolveStartConfig(ctx, req)
	if err != nil {
		return nil, err
	}
	wsID, workdir, _, err := resolveGraphRunWorkspace(ctx, firstNonEmpty(req.WorkspaceID, cfg.WorkspaceID), firstNonEmpty(req.Workdir, cfg.Workdir), workspaces, false, "")
	if err != nil {
		return nil, err
	}
	j := model.NewJob(workdir, wsID)
	j.Mode = model.JobModeGraph
	j.Title = "Graph Run"
	if err := jobs.Create(j); err != nil {
		return nil, fmt.Errorf("create graph job failed: %w", err)
	}
	req.WorkspaceID = wsID
	req.Workdir = workdir
	logger.Infof(ctx, "[graph] created graph job: jobId=%s workspaceId=%s workdir=%s", j.ID, wsID, workdir)
	return j, nil
}

func (s *serviceImpl) CreateScheduledRunJob(ctx context.Context, task *model.ScheduledTask, jobs jobsvc.Service, workspaces workspacesvc.Service) (*model.Job, error) {
	if task == nil {
		return nil, fmt.Errorf("scheduled task is required")
	}
	if jobs == nil {
		return nil, fmt.Errorf("job service is required")
	}
	wsID, workdir, _, err := resolveGraphRunWorkspace(ctx, task.WorkspaceID, task.Workdir, workspaces, true, task.ID)
	if err != nil {
		return nil, err
	}
	j := model.NewJob(workdir, wsID)
	j.Mode = model.JobModeGraph
	j.ScheduleID = task.ID
	j.Title = consts.ScheduleJobTitlePrefix + task.Name + " (" + time.Now().Format("15:04") + ")"
	if err := jobs.Create(j); err != nil {
		return nil, err
	}
	return j, nil
}

func isInvalidIDError(err error) bool {
	return errors.Is(err, os.ErrInvalid) || errors.Is(err, os.ErrPermission)
}

func resolveGraphRunWorkspace(ctx context.Context, requestedWorkspaceID, requestedWorkdir string, workspaces workspacesvc.Service, allowScheduleFallback bool, scheduleID string) (wsID, workdir string, ws *model.Workspace, err error) {
	if workspaces == nil {
		return "", "", nil, fmt.Errorf("workspace service is required")
	}
	wsID = requestedWorkspaceID
	workdir = requestedWorkdir
	if wsID == "" {
		wsID = consts.DefaultWorkspaceID
	}
	var ok bool
	ws, ok = workspaces.Get(wsID)
	if !ok {
		if !allowScheduleFallback {
			return "", "", nil, fmt.Errorf("%w: workspace %s not found", ErrWorkflowBadRequest, wsID)
		}
		logger.Warnf(ctx, "[ScheduleTrigger] workspace %s not found for task %s, falling back to %s", wsID, scheduleID, consts.DefaultWorkspaceID)
		wsID = consts.DefaultWorkspaceID
		workdir = ""
		ws, ok = workspaces.Get(wsID)
		if !ok {
			return "", "", nil, fmt.Errorf("schedule %s: default workspace %s is missing; ensure-default may have failed at startup", scheduleID, consts.DefaultWorkspaceID)
		}
	}
	if workdir == "" && ws != nil {
		workdir = ws.Workdir
	}
	if err := validateGraphWorkdir(workdir); err != nil {
		if allowScheduleFallback {
			return "", "", nil, fmt.Errorf("schedule %s: invalid workdir: %w", scheduleID, err)
		}
		return "", "", nil, fmt.Errorf("%w: %v", ErrWorkflowBadRequest, err)
	}
	if ws != nil {
		if err := ensureGraphWorkdirWithinWorkspace(workdir, ws.Workdir); err != nil {
			if allowScheduleFallback {
				return "", "", nil, fmt.Errorf("schedule %s: %w", scheduleID, err)
			}
			return "", "", nil, fmt.Errorf("%w: %v", ErrWorkflowBadRequest, err)
		}
	}
	return wsID, workdir, ws, nil
}

func validateGraphWorkdir(workdir string) error {
	if workdir == "" {
		return nil
	}
	sb := fileserver.GetFileManager()
	stat, err := sb.FileStat(&fsmodel.FileStatRequest{Path: workdir})
	if err != nil {
		return fmt.Errorf("failed to check workdir: %w", err)
	}
	if !stat.Exists {
		return fmt.Errorf("workdir does not exist: %s", workdir)
	}
	if !stat.IsDir {
		return fmt.Errorf("workdir is not a directory: %s", workdir)
	}
	return nil
}

func ensureGraphWorkdirWithinWorkspace(workdir, wsWorkdir string) error {
	if workdir == "" || wsWorkdir == "" {
		return nil
	}
	realWs, err := resolveGraphPathForContainment(wsWorkdir)
	if err != nil {
		return fmt.Errorf("resolve workspace dir %s: %w", wsWorkdir, err)
	}
	realWd, err := resolveGraphPathForContainment(workdir)
	if err != nil {
		return fmt.Errorf("resolve workdir %s: %w", workdir, err)
	}
	if realWd == realWs {
		return nil
	}
	rel, err := filepath.Rel(realWs, realWd)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("workdir %s is outside workspace directory %s", workdir, wsWorkdir)
	}
	return nil
}

func resolveGraphPathForContainment(p string) (string, error) {
	abs, err := filepath.Abs(p)
	if err != nil {
		return "", err
	}
	real, err := filepath.EvalSymlinks(abs)
	if err != nil {
		return "", err
	}
	return filepath.Clean(real), nil
}
