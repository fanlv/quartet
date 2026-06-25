package graph

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/services/usagestats"
	"github.com/fanlv/quartet/types/agui"
	"github.com/fanlv/quartet/types/consts"
	"github.com/fanlv/quartet/types/model"
	"github.com/fanlv/quartet/types/msgextra"
	"github.com/google/uuid"
)

var (
	ErrGraphRunNotFound = errors.New("graph run not found")
	// ErrGraphRunUnsupported is returned when a graph uses features the current
	// scheduler does not yet drive (loop containers / subgraph child nodes).
	ErrGraphRunUnsupported = errors.New("graph run uses features not yet supported by the scheduler")
	ErrGraphRunnerMissing  = errors.New("graph runner is required")
	// ErrGraphRunNotRunning is returned when a control action (stop/pause/
	// step-stop) targets a run with no live scheduler.
	ErrGraphRunNotRunning = errors.New("graph run is not currently running")
	// ErrGraphRunControlBusy is returned when a live scheduler's control queue
	// is full and the requested control signal was not accepted.
	ErrGraphRunControlBusy = errors.New("graph run control queue is busy")
	// ErrGraphRunNotResumable is returned when ResumeRun targets a run that is
	// not in a resumable terminal state.
	ErrGraphRunNotResumable = errors.New("graph run is not resumable")
	// ErrGraphRunInFlight is returned when DeleteRun targets a run that is still
	// in flight.
	ErrGraphRunInFlight = errors.New("graph run is in flight and cannot be deleted")
	// ErrGraphRunNotEditable is returned when UpdateRunVersion cannot reach the
	// live scheduler for an in-flight run, or the run state otherwise cannot
	// accept a version edit.
	ErrGraphRunNotEditable = errors.New("graph run cannot be edited")
)

func graphRunLoadError(runID string, err error) error {
	if err == nil {
		return nil
	}
	if errors.Is(err, os.ErrNotExist) || strings.Contains(err.Error(), "graph run location is not registered") {
		return ErrGraphRunNotFound
	}
	return fmt.Errorf("load graph run %s failed: %w", runID, err)
}

func (s *serviceImpl) StartRun(ctx context.Context, req *model.StartGraphRunRequest, runner Runner, jobs JobStateSink) (*model.GraphRun, error) {
	if req == nil {
		return nil, fmt.Errorf("request is required")
	}
	if runner == nil {
		return nil, ErrGraphRunnerMissing
	}

	wf, cfg, err := s.resolveStartConfig(ctx, req)
	if err != nil {
		return nil, err
	}
	if errs := validateConfig(&cfg); len(errs) > 0 {
		return nil, &ValidationError{Errors: errs}
	}
	if err := ensureSchedulable(cfg); err != nil {
		return nil, err
	}

	jobID := strings.TrimSpace(req.JobID)
	if jobID == "" {
		return nil, fmt.Errorf("jobId is required")
	}
	now := time.Now()
	models, agents := buildSnapshotContent(ctx, cfg, runner)
	run := &model.GraphRun{
		ID:          model.NewGraphRunID(),
		WorkflowID:  "",
		JobID:       jobID,
		WorkspaceID: firstNonEmpty(req.WorkspaceID, cfg.WorkspaceID, consts.DefaultWorkspaceID),
		Status:      model.GraphRunStatusPending,
		BaseSnapshot: model.GraphRunSnapshot{
			Config:         cloneGraphConfig(cfg),
			ModelSnapshots: models,
			AgentSnapshots: agents,
			CapturedAt:     now.UnixMilli(),
		},
		CurrentVersion: 1,
		CreatedAt:      now,
		UpdatedAt:      now,
	}
	if wf != nil {
		run.WorkflowID = wf.ID
		run.WorkspaceID = firstNonEmpty(run.WorkspaceID, wf.WorkspaceID)
		run.BaseSnapshot.WorkflowID = wf.ID
		run.BaseSnapshot.WorkflowName = wf.Name
	}
	run.Versions = []model.GraphRunVersion{{
		Version:        1,
		Config:         cloneGraphConfig(cfg),
		ModelSnapshots: models,
		AgentSnapshots: agents,
		Reason:         "baseline",
		CreatedAt:      now.UnixMilli(),
	}}
	run.Progress = initialGraphProgress(cfg)
	run.Resume = &model.GraphResumeState{
		EdgeStates:     map[string]model.GraphEdgeState{},
		VariablesByKey: map[string]map[string]string{},
	}

	if err := s.runRepo.RegisterRun(ctx, run); err != nil {
		return nil, err
	}
	if err := s.persistRuntimeState(ctx, run, map[string]model.GraphInstanceState{}, map[string]model.GraphEdgeState{}, map[string]map[string]string{}); err != nil {
		s.cleanupUnboundRun(ctx, run.ID, err)
		return nil, err
	}
	if jobs != nil {
		if err := jobs.SetGraphRunState(ctx, jobID, run.ID, model.JobStatusPending, 0, 0); err != nil {
			s.cleanupUnboundRun(ctx, run.ID, err)
			return nil, err
		}
	}

	logger.Infof(ctx, "[graph] run created: runId=%s jobId=%s workflowId=%s workspaceId=%s nodes=%d edges=%d concurrency=%d jobTimeoutSec=%d",
		run.ID, run.JobID, run.WorkflowID, run.WorkspaceID, len(cfg.Nodes), len(cfg.Edges),
		concurrencyLimit(cfg.RunConfig.ConcurrencyLimit), cfg.RunConfig.JobTimeoutSec)
	go s.runGraph(context.Background(), run.ID, runner, jobs, false)
	return run, nil
}

func (s *serviceImpl) cleanupUnboundRun(ctx context.Context, runID string, cause error) {
	if strings.TrimSpace(runID) == "" {
		return
	}
	if err := s.runRepo.DeleteRun(ctx, runID); err != nil {
		logger.Warnf(ctx, "[graph] cleanup unbound graph run failed: runId=%s cause=%v cleanupErr=%v", runID, cause, err)
	}
	s.removeBuffer(runID)
}

func (s *serviceImpl) RegisterRunLocation(ctx context.Context, runID, workspaceID, jobID string) error {
	return s.runRepo.RegisterRunLocation(ctx, runID, workspaceID, jobID)
}

func (s *serviceImpl) GetRunStatus(ctx context.Context, runID string) (*model.GraphRunStatusResponse, error) {
	run, err := s.runRepo.GetRun(ctx, runID)
	if err != nil {
		return nil, graphRunLoadError(runID, err)
	}
	instances, err := s.runRepo.GetInstances(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("load graph run instances failed: %w", err)
	}
	edges, err := s.runRepo.GetEdges(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("load graph run edges failed: %w", err)
	}
	progress, err := s.runRepo.GetProgress(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("load graph run progress failed: %w", err)
	}
	// Return only the event COUNT, not the events themselves. The full log used
	// to be serialised here (hundreds of MB on a long loop) just for the client
	// to read its length as an SSE resume cursor — the bodies were discarded.
	// Live events arrive over the SSE stream; historical agent conversation
	// loads per-node from session messages. A run that exists always has its
	// runtime artifacts persisted (StartRun → persistRuntimeState), so a read or
	// parse error here means a corrupt/truncated artifact — surface it in full
	// instead of masking data loss as an empty status. Mirrors ResumeRun.
	eventCount, err := s.runRepo.CountEvents(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("count graph run events failed: %w", err)
	}
	instanceList := make([]model.GraphInstanceState, 0, len(instances))
	for _, st := range instances {
		instanceList = append(instanceList, st)
	}
	sort.Slice(instanceList, func(i, j int) bool {
		return instanceKeyString(instanceList[i].Key) < instanceKeyString(instanceList[j].Key)
	})
	edgeList := make([]model.GraphEdgeState, 0, len(edges))
	for _, st := range edges {
		edgeList = append(edgeList, st)
	}
	sort.Slice(edgeList, func(i, j int) bool { return edgeList[i].EdgeID < edgeList[j].EdgeID })
	return &model.GraphRunStatusResponse{
		Run:        run,
		Progress:   progress,
		Instances:  instanceList,
		Edges:      edgeList,
		EventCount: eventCount,
	}, nil
}

func (s *serviceImpl) ListRunEvents(ctx context.Context, runID string, startLine int, count *int) (*model.GraphRunEventsResponse, error) {
	if startLine < 0 {
		startLine = 0
	}
	if _, err := s.runRepo.GetRun(ctx, runID); err != nil {
		return nil, graphRunLoadError(runID, err)
	}
	events, err := s.runRepo.ListEvents(ctx, runID, startLine, count)
	if err != nil {
		return nil, err
	}
	lastEventID := ""
	if len(events) > 0 {
		lastEventID = fmt.Sprintf("%d", startLine+len(events))
	}
	return &model.GraphRunEventsResponse{
		Events:    events,
		NextLine:  startLine + len(events),
		LastEvent: lastEventID,
	}, nil
}

// graphReplayMaxEvents bounds how many persisted events a single SSE disk-replay
// streams. It is a safety net, not a UI driver: the canvas is rebuilt from the
// run snapshot (GET /job/:jobId/graph-run), so a truncated replay never loses correctness.
// It exists so a pathologically large legacy event log can't be streamed in full
// on every (re)connect.
const graphReplayMaxEvents = 2000

func (s *serviceImpl) ListReplayEvents(ctx context.Context, runID string, startLine, limit int) (*model.GraphRunEventsResponse, error) {
	if startLine < 0 {
		startLine = 0
	}
	if limit <= 0 {
		limit = graphReplayMaxEvents
	}
	count := limit
	resp, err := s.ListRunEvents(ctx, runID, startLine, &count)
	if err != nil {
		return nil, err
	}
	// Defence in depth: even though agent streaming deltas are never written to
	// events.jsonl (isPersistableGraphEvent gates the write), a legacy log may
	// still contain them. Drop them so replay carries only structural events.
	filtered := resp.Events[:0]
	for _, ev := range resp.Events {
		if isPersistableGraphEvent(ev.Type) {
			filtered = append(filtered, ev)
		}
	}
	resp.Events = filtered
	if resp.NextLine-startLine >= limit {
		logger.Warnf(ctx, "[graph] replay truncated at limit: runId=%s startLine=%d limit=%d (canvas is rebuilt from the run snapshot)", runID, startLine, limit)
	}
	return resp, nil
}

func (s *serviceImpl) resolveStartConfig(ctx context.Context, req *model.StartGraphRunRequest) (*model.GraphWorkflow, model.GraphConfig, error) {
	if req.Config != nil {
		cfg := cloneGraphConfig(*req.Config)
		cfg.WorkspaceID = firstNonEmpty(req.WorkspaceID, cfg.WorkspaceID)
		cfg.Workdir = firstNonEmpty(req.Workdir, cfg.Workdir)
		// The frontend launches a saved workflow by sending BOTH its workflowId
		// and the live (possibly edited) canvas config. The config is the
		// execution snapshot, but we still resolve the workflow so the run binds
		// its source metadata (WorkflowID / WorkflowName). If a workflowId is
		// supplied it is part of the contract; a deleted/corrupt workflow must be
		// surfaced instead of silently creating an untraceable ad-hoc run.
		if id := strings.TrimSpace(req.WorkflowID); id != "" {
			wf, err := s.repo.Get(ctx, id)
			if errors.Is(err, os.ErrNotExist) {
				return nil, model.GraphConfig{}, ErrWorkflowNotFound
			}
			if isInvalidIDError(err) {
				return nil, model.GraphConfig{}, fmt.Errorf("%w: workflowId %q: %v", ErrWorkflowBadRequest, id, err)
			}
			if err != nil {
				return nil, model.GraphConfig{}, fmt.Errorf("load graph workflow %s failed: %w", id, err)
			}
			if wf.Deleted {
				return nil, model.GraphConfig{}, ErrWorkflowNotFound
			}
			if req.WorkflowUpdatedAt != nil && !wf.UpdatedAt.Equal(*req.WorkflowUpdatedAt) {
				return nil, model.GraphConfig{}, fmt.Errorf("%w: current updatedAt=%s, request updatedAt=%s", ErrWorkflowConflict, wf.UpdatedAt.Format(time.RFC3339Nano), req.WorkflowUpdatedAt.Format(time.RFC3339Nano))
			}
			cfg.WorkspaceID = firstNonEmpty(cfg.WorkspaceID, wf.WorkspaceID)
			return wf, cfg, nil
		}
		return nil, cfg, nil
	}
	if strings.TrimSpace(req.WorkflowID) == "" {
		return nil, model.GraphConfig{}, fmt.Errorf("%w: workflowId or config is required", ErrWorkflowBadRequest)
	}
	wf, err := s.repo.Get(ctx, req.WorkflowID)
	if errors.Is(err, os.ErrNotExist) {
		return nil, model.GraphConfig{}, ErrWorkflowNotFound
	}
	if isInvalidIDError(err) {
		return nil, model.GraphConfig{}, fmt.Errorf("%w: workflowId %q: %v", ErrWorkflowBadRequest, req.WorkflowID, err)
	}
	if err != nil {
		return nil, model.GraphConfig{}, fmt.Errorf("load graph workflow %s failed: %w", req.WorkflowID, err)
	}
	if wf.Deleted {
		return nil, model.GraphConfig{}, ErrWorkflowNotFound
	}
	if req.WorkflowUpdatedAt != nil && !wf.UpdatedAt.Equal(*req.WorkflowUpdatedAt) {
		return nil, model.GraphConfig{}, fmt.Errorf("%w: current updatedAt=%s, request updatedAt=%s", ErrWorkflowConflict, wf.UpdatedAt.Format(time.RFC3339Nano), req.WorkflowUpdatedAt.Format(time.RFC3339Nano))
	}
	cfg := cloneGraphConfig(wf.Config)
	cfg.WorkspaceID = firstNonEmpty(req.WorkspaceID, cfg.WorkspaceID, wf.WorkspaceID)
	cfg.Workdir = firstNonEmpty(req.Workdir, cfg.Workdir)
	return wf, cfg, nil
}

// nodeOutcome is a business node's successful execution result. Besides the raw
// final output and declared named outputs, it carries the Shell control signals
// STOP_LOOP / STOP_WORKFLOW so the scheduler can apply them with scope context
// (STOP_LOOP ends the enclosing loop container, or is ignored as a no-op when
// the node is not inside a loop; STOP_WORKFLOW ends the run with early success).
// (the session the node created with `new`, or the inflow session it reused with
// `inherit`), which the scheduler records as the instance's session lineage
// (§3 会话血缘).
type nodeOutcome struct {
	output       string
	produced     map[string]string
	stopLoop     bool
	stopWorkflow bool
	sessionID    string
	usage        *usagestats.Accumulator
	modelID      string

	// Shell display-session fields (§ Graph Shell 默认新开 session): the captured
	// stderr and start/finish timing used to complete the shell display session's
	// transcript via FinishShellSession when the node ends. The session itself
	// (and the script as its user message) is created at enqueue time by
	// BeginShellSession; these feed the assistant-side output message and never
	// participate in session lineage.
	stderr     string
	startedAt  int64
	finishedAt int64
}

// executeNode runs one business node against its visible variable snapshot.
// inflowSession is the session flowing in along the node's in-edges (§3 会话血缘):
// an Agent node with the `inherit` strategy reuses it verbatim (appending its
// turn to the same session's message list); a `new`/unset Agent creates a fresh
// session; Shell nodes ignore it (the scheduler passes the inflow through as the
// Shell's outflow).
//
// loopVars carries the QUARTET_LOOP_* iteration context of the innermost
// enclosing loop (nil in the main scope). It is overlaid onto the {{...}}
// substitution for Prompt/Evaluator and onto both the substitution and the
// environment for Shell, without being persisted into the node's snapshot.
//
// notify is called once with the opened session id as soon as an Agent node has
// created/forked its session and BEFORE the agent runs. The scheduler uses it
// to surface the node's session in the UI the instant work starts (eager
// session visibility) instead of only after the agent replies. It is nil-safe
// and unused by Shell nodes (their display session is minted at enqueue time).
func (s *serviceImpl) executeNode(ctx context.Context, run *model.GraphRun, node model.GraphNode, key model.GraphInstanceKey, vars map[string]string, disabled map[string]struct{}, runner Runner, inflowSession string, loopVars map[string]string, notify func(sessionID string)) (nodeOutcome, error) {
	switch node.Type {
	case model.GraphNodeTypeShell:
		return s.runShellWithRetries(ctx, run, node, vars, disabled, loopVars)
	case model.GraphNodeTypePrompt, model.GraphNodeTypeEvaluator:
		prompt := node.Config.Prompt
		if node.Type == model.GraphNodeTypeEvaluator {
			prompt = buildEvaluatorPrompt(prompt, node.Config.OutputVariables)
		} else {
			prompt += buildOutputProtocolSuffix(node.Config.OutputVariables)
		}
		prompt = substituteVariables(prompt, mergeLoopVars(vars, loopVars), disabled)
		overrides := &model.SessionOverrides{
			AgentType:       node.Config.AgentType,
			ModelID:         node.Config.ModelID,
			ACPMode:         node.Config.ACPMode,
			ACPThoughtLevel: node.Config.ACPThoughtLevel,
		}
		sessionID, err := openNodeSession(ctx, runner, run.JobID, node, inflowSession, overrides)
		if err != nil {
			return nodeOutcome{}, err
		}
		userMsg := &schema.Message{Role: schema.User, Content: prompt}
		// Persist the rendered prompt as the session's user message NOW — before
		// the agent subprocess spawns and starts replying — so the Chat sidebar
		// shows the auto-sent prompt the instant the node starts instead of
		// sitting blank through agent warmup. Only for `new`/unset strategy: an
		// `inherit` session is a continuation whose extra up-front write would
		// drift the ACP fingerprint and force a needless subprocess reset every
		// turn. Skip an empty prompt (a degenerate node) so we never leave an
		// orphan blank user bubble on disk. On success, tag the in-memory copy
		// handed to the agent with KeyPrePersisted so its BeginRun skips the
		// re-append (chatctx.BeginRun); the tag rides only the in-memory copy and
		// never reaches disk. Best-effort: a record failure logs and falls back
		// to the agent's own in-Run persistence (message just shows a bit later).
		if node.Config.SessionStrategy != model.GraphSessionStrategyInherit && strings.TrimSpace(prompt) != "" {
			if recorder, ok := runner.(PromptUserMessageRecorder); ok {
				if err := recorder.RecordPromptUserMessage(ctx, run.JobID, sessionID, prompt, time.Now().UnixMilli()); err != nil {
					logger.Warnf(ctx, "[graph] record prompt user message failed: runId=%s nodeId=%s sessionId=%s err=%v", run.ID, node.ID, sessionID, err)
				} else {
					userMsg.Extra = map[string]any{msgextra.KeyPrePersisted: true}
				}
			}
		}
		// Announce the session the instant it exists so the UI lists it (and its
		// just-persisted user message) while the agent is still replying, rather
		// than only after the node completes. Non-blocking on the scheduler side;
		// see scheduler.handleSessionOpened.
		if notify != nil {
			notify(sessionID)
		}
		modelID := firstNonEmpty(runner.SessionModelID(sessionID), node.Config.ModelID)
		result := s.runPromptWithRetries(ctx, run.ID, run.JobID, sessionID, node.ID, key, runner, []*schema.Message{userMsg})
		if result.err != nil {
			return nodeOutcome{output: result.handler.AccumulatedContent(), sessionID: sessionID, usage: result.handler.usage, modelID: modelID}, withGraphRetryCount(result.err, result.retryCount)
		}
		output := result.handler.AccumulatedContent()
		parsed, perr := ParseQuartetOutput(output, node.Config.OutputVariables)
		if perr != nil {
			return nodeOutcome{output: output, sessionID: sessionID, usage: result.handler.usage, modelID: modelID}, perr
		}
		return nodeOutcome{output: output, produced: parsed.Variables, sessionID: sessionID, usage: result.handler.usage, modelID: modelID}, nil
	default:
		return nodeOutcome{}, fmt.Errorf("node type %q is not supported by the graph engine", node.Type)
	}
}

// openNodeSession opens the session an Agent-class node executes against per its
// declared strategy (§3 会话血缘). `inherit` REUSES the inflow session verbatim:
// the node appends its turn to the SAME session's message list, continuing one
// continuous conversation (no fork, no history copy). `new` (or unset) mints a
// fresh session. An `inherit` declaration with no inflow session is a node
// failure — it means an upstream Agent was expected but none ran (the validator
// forbids this statically for the first Agent on a start chain, so at runtime it
// can only arise from a reset/pruned upstream).
//
// 循环跨轮语义（§3 不轮间隔离 / B 跨轮连续）: a loop body whose first Agent is
// `inherit` reuses the session flowing into the container; because finishIteration
// carries the round-end session forward as the next round's inflow, every
// iteration appends to the SAME session — one continuous conversation spanning all
// rounds. This is safe: iterations run strictly sequentially on the scheduler
// goroutine, so reuse never yields concurrent turns on one session.
func openNodeSession(ctx context.Context, runner Runner, jobID string, node model.GraphNode, inflowSession string, overrides *model.SessionOverrides) (sessionID string, err error) {
	if node.Config.SessionStrategy == model.GraphSessionStrategyInherit {
		if inflowSession == "" {
			return "", fmt.Errorf("node %q declares 'inherit' session strategy but no upstream session is available to inherit", node.ID)
		}
		logger.Infof(ctx, "[graph] reusing upstream session: jobId=%s nodeId=%s sessionId=%s", jobID, node.ID, inflowSession)
		return inflowSession, nil
	}
	sessionID, err = runner.InitSession(ctx, jobID, overrides)
	if err != nil {
		logger.Errorf(ctx, "[graph] init session failed: jobId=%s nodeId=%s err=%v", jobID, node.ID, err)
		return "", err
	}
	logger.Infof(ctx, "[graph] initialized session: jobId=%s nodeId=%s sessionId=%s strategy=%s", jobID, node.ID, sessionID, firstNonEmpty(string(node.Config.SessionStrategy), string(model.GraphSessionStrategyNew)))
	return sessionID, nil
}

// runShellWithRetries executes a Shell node through the shared transient/rate-
// limit retry driver (§2 瞬态错误重试), matching Prompt/评估 behavior: network
// reset / HTTP2 stream errors retry twice (fixed backoff), rate-limit errors
// retry three times (exponential backoff). Retries happen inside the node so
// edge state and progress are unaffected; the surfaced error carries the retry
// count and the full stdout/stderr/exit code via executeShellNode's error text.
// STOP_LOOP / STOP_WORKFLOW and parse/declaration failures are deterministic
// node outcomes, not transient, so they are not retried (their errors are not
// classified as transient/rate-limit).
func (s *serviceImpl) runShellWithRetries(ctx context.Context, run *model.GraphRun, node model.GraphNode, vars map[string]string, disabled map[string]struct{}, loopVars map[string]string) (nodeOutcome, error) {
	var outcome nodeOutcome
	attempt := func(ctx context.Context) error {
		var err error
		outcome, err = executeShellNode(ctx, run, node, vars, disabled, loopVars)
		return err
	}
	retryCount, err := s.runWithRetries(ctx, run.ID, run.JobID, node.ID, attempt)
	if err != nil {
		return outcome, withGraphRetryCount(err, retryCount)
	}
	return outcome, nil
}

// shellSessionOutput combines stdout and stderr into the assistant-side message
// content of a shell display session, mirroring how the user perceives a shell
// run (stdout first, stderr appended when present).
func shellSessionOutput(stdout, stderr string) string {
	if stderr == "" {
		return stdout
	}
	if stdout == "" {
		return stderr
	}
	return stdout + "\n" + stderr
}

func executeShellNode(ctx context.Context, run *model.GraphRun, node model.GraphNode, vars map[string]string, disabled map[string]struct{}, loopVars map[string]string) (nodeOutcome, error) {
	workdir := run.BaseSnapshot.Config.Workdir
	// displayScript is the user-authored script after variable substitution but
	// without the injected helper preamble. The scheduler computes the same
	// substitution at enqueue time to seed the shell display session's user
	// message, so it is not carried back on the outcome. Loop iteration vars are
	// overlaid so {{QUARTET_LOOP_INDEX}} & co. resolve inside a loop body.
	displayScript := substituteVariables(node.Config.Script, mergeLoopVars(vars, loopVars), disabled)
	script := graphShellHelpers + "\n" + displayScript
	tmpDir := workdir
	if tmpDir == "" {
		tmpDir = os.TempDir()
	}
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		return nodeOutcome{}, err
	}
	scriptFile, err := os.CreateTemp(tmpDir, ".quartet-graph-*.sh")
	if err != nil {
		return nodeOutcome{}, err
	}
	scriptPath := scriptFile.Name()
	defer os.Remove(scriptPath)
	if _, err := scriptFile.WriteString(script); err != nil {
		_ = scriptFile.Close()
		return nodeOutcome{}, err
	}
	if err := scriptFile.Close(); err != nil {
		return nodeOutcome{}, err
	}
	ctrlFile, err := os.CreateTemp(tmpDir, ".quartet-graph-*.ctrl")
	if err != nil {
		return nodeOutcome{}, err
	}
	ctrlPath := ctrlFile.Name()
	_ = ctrlFile.Close()
	defer os.Remove(ctrlPath)

	cmd := exec.CommandContext(ctx, "bash", scriptPath)
	if workdir != "" {
		cmd.Dir = workdir
	}
	// Inject the visible variable snapshot as environment variables so scripts
	// can read them as $name in addition to the {{name}} text substitution
	// above. Variable names are save-validated to [A-Za-z_][A-Za-z0-9_]*, which
	// is exactly a legal shell identifier, so no name escaping is needed.
	// Semantics mirror substituteVariables: a disabled variable renders to the
	// empty string regardless of its stored value. QUARTET_CONTROL is appended
	// last so a user variable of that name can never clobber the control file
	// path (later entries win in cmd.Env).
	env := os.Environ()
	for k, v := range vars {
		if k == "QUARTET_CONTROL" {
			continue
		}
		if _, off := disabled[k]; off {
			v = ""
		}
		env = append(env, k+"="+v)
	}
	for k := range disabled {
		if k == "QUARTET_CONTROL" {
			continue
		}
		if _, ok := vars[k]; !ok {
			env = append(env, k+"=")
		}
	}
	// Inject the loop iteration context (QUARTET_LOOP_*) after the user vars so
	// scripts in a loop body can read $QUARTET_LOOP_INDEX etc. These are an
	// engine-owned, reserved namespace (isReservedVar), so no user variable can
	// shadow them. Appended before QUARTET_CONTROL so control still wins last.
	for k, v := range loopVars {
		env = append(env, k+"="+v)
	}
	cmd.Env = append(env, "QUARTET_CONTROL="+ctrlPath)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	startedAt := time.Now().UnixMilli()
	err = cmd.Run()
	finishedAt := time.Now().UnixMilli()
	control, readErr := os.ReadFile(filepath.Clean(ctrlPath))
	if readErr != nil {
		return nodeOutcome{output: stdout.String()}, fmt.Errorf("read shell control file failed: %w; stdout=%s stderr=%s", readErr, stdout.String(), stderr.String())
	}
	if err != nil {
		return nodeOutcome{output: stdout.String()}, fmt.Errorf("shell failed: %v; stdout=%s stderr=%s control=%s", err, stdout.String(), stderr.String(), string(control))
	}
	parsed, perr := ParseShellControl(string(control), node.Config.OutputVariables)
	if perr != nil {
		return nodeOutcome{output: stdout.String()}, fmt.Errorf("%w; stdout=%s stderr=%s control=%s", perr, stdout.String(), stderr.String(), string(control))
	}
	// STOP_LOOP / STOP_WORKFLOW are scope-dependent control signals; the
	// scheduler applies them (STOP_LOOP only inside a loop container). They are
	// returned here rather than treated as errors.
	return nodeOutcome{
		output:       stdout.String(),
		produced:     parsed.Variables,
		stopLoop:     parsed.StopLoop,
		stopWorkflow: parsed.StopWorkflow,
		stderr:       stderr.String(),
		startedAt:    startedAt,
		finishedAt:   finishedAt,
	}, nil
}

func initialGraphProgress(cfg model.GraphConfig) *model.GraphProgress {
	nodesByID := make(map[string]model.GraphNode, len(cfg.Nodes))
	for _, n := range cfg.Nodes {
		nodesByID[n.ID] = n
	}
	// Static upper bound on the progress denominator: every business node (loop
	// containers included) contributes its maximum instance count, which is the
	// product of the static round bounds of all ancestor loop containers (1 for a
	// main-scope node). This guarantees completed ≤ total even with nested loops.
	// Run-time pruning / early-stop denominator精修 lives in step 15.
	total := 0
	for _, n := range cfg.Nodes {
		if isBusiness(n.Type) {
			total += nodeMaxInstances(cfg.RunConfig, nodesByID, n)
		}
	}
	return &model.GraphProgress{
		TotalCount: total,
		Instances:  map[string]model.GraphInstanceState{},
	}
}

// nodeMaxInstances returns the static upper bound on how many instances of a
// node can exist: the product of the round bounds of every ancestor loop
// container (1 for a main-scope node).
func nodeMaxInstances(rc model.GraphRunConfig, nodesByID map[string]model.GraphNode, node model.GraphNode) int {
	prod := 1
	for pid := node.ParentID; pid != ""; {
		loop, ok := nodesByID[pid]
		if !ok {
			break
		}
		prod *= loopMaxRounds(rc, loop)
		pid = loop.ParentID
	}
	return prod
}

// loopMaxRounds is the static upper bound on how many times a loop container's
// subgraph runs, used only for the initial progress denominator.
func loopMaxRounds(rc model.GraphRunConfig, loop model.GraphNode) int {
	if loop.Config.LoopMode == model.GraphLoopModeFixed {
		if loop.Config.FixedCount < 0 {
			return 0
		}
		return loop.Config.FixedCount
	}
	return effectiveLoopMaxIters(rc, loop)
}

func updateRunProgress(run *model.GraphRun, instances map[string]model.GraphInstanceState) {
	if run.Progress == nil {
		run.Progress = &model.GraphProgress{}
	}
	p := run.Progress
	p.CompletedCount = 0
	p.FailedCount = 0
	p.SkippedCount = 0
	p.InterruptedCount = 0
	p.RunningCount = 0
	p.CurrentKeys = nil
	p.Instances = make(map[string]model.GraphInstanceState, len(instances))
	for k, st := range instances {
		p.Instances[k] = st
		switch st.Status {
		case model.GraphInstanceStatusSucceeded:
			p.CompletedCount++
		case model.GraphInstanceStatusFailed:
			p.FailedCount++
		case model.GraphInstanceStatusSkipped:
			p.SkippedCount++
		case model.GraphInstanceStatusInterrupted:
			p.InterruptedCount++
		case model.GraphInstanceStatusRunning:
			p.RunningCount++
			p.CurrentKeys = append(p.CurrentKeys, st.Key)
		}
	}
}

func (s *serviceImpl) persistRuntimeState(ctx context.Context, run *model.GraphRun, instances map[string]model.GraphInstanceState, edges map[string]model.GraphEdgeState, vars map[string]map[string]string) error {
	if err := s.runRepo.SaveInstances(ctx, run.ID, instances); err != nil {
		return err
	}
	if err := s.runRepo.SaveEdges(ctx, run.ID, edges); err != nil {
		return err
	}
	if err := s.runRepo.SaveVariables(ctx, run.ID, vars); err != nil {
		return err
	}
	if err := s.runRepo.SaveProgress(ctx, run.ID, run.Progress); err != nil {
		return err
	}
	if err := s.runRepo.SaveResume(ctx, run.ID, run.Resume); err != nil {
		return err
	}
	return s.runRepo.SaveRun(ctx, run)
}

// appendEvent records a structural run event (instance lifecycle, edge
// resolution, loop iteration, variable write, progress update, log, error).
// It is double-path: structural events are persisted to events.jsonl (so
// resume, audit, and post-restart SSE replay can rebuild the run) AND published
// to the live in-memory buffer (so connected SSE readers get it in real time).
// Agent streaming deltas are published only (never persisted — the authoritative
// conversation lives in each node's session messages.jsonl); isPersistableGraphEvent
// gates the disk write. The same event object — identical ID and CreatedAt —
// flows to both paths, so a client that sees the live copy and a client that
// replays it from disk observe the same event.
//
// Events never embed a progress snapshot: progress is sourced exclusively from
// the run snapshot (progress.json via GET /job/:jobId/graph-run). progressUpdated is a
// pure "refetch the snapshot now" signal, not a progress carrier.
func (s *serviceImpl) appendEvent(ctx context.Context, runID string, typ model.GraphEventType, key *model.GraphInstanceKey, nodeID, edgeID, message string, rerr *model.GraphRuntimeError) {
	if s.runRepo == nil {
		return
	}
	evt := &model.GraphEvent{
		ID:          model.NewGraphEventID(),
		RunID:       runID,
		Type:        typ,
		InstanceKey: key,
		NodeID:      nodeID,
		EdgeID:      edgeID,
		Message:     message,
		Payload:     map[string]string{},
		Error:       rerr,
		CreatedAt:   time.Now().UnixMilli(),
	}
	if isPersistableGraphEvent(typ) {
		_ = s.runRepo.AppendEvent(ctx, runID, evt)
	}
	if buf := s.getBuffer(runID); buf != nil {
		buf.Publish(evt)
	}
}

// isPersistableGraphEvent reports whether an event should be written to
// events.jsonl. Agent streaming deltas are never persisted — the authoritative
// conversation already lives in each node's session messages.jsonl, and
// persisting per-token deltas would let events.jsonl grow without bound on long
// loops. Everything else (structural lifecycle / edge / loop / progress / log /
// error events) is persisted so resume, audit, and post-restart replay can
// rebuild the run. Blacklisting deltas (rather than whitelisting structural
// types) means a newly added structural event type defaults to persisted.
func isPersistableGraphEvent(typ model.GraphEventType) bool {
	switch typ {
	case model.GraphEventTypeAgentMessageStart, model.GraphEventTypeAgentMessageDelta,
		model.GraphEventTypeAgentMessageEnd, model.GraphEventTypeAgentThoughtStart,
		model.GraphEventTypeAgentThoughtDelta, model.GraphEventTypeAgentThoughtEnd,
		model.GraphEventTypeAgentToolStart, model.GraphEventTypeAgentToolArgs,
		model.GraphEventTypeAgentToolResult, model.GraphEventTypeAgentToolEnd,
		model.GraphEventTypeAgentTokenUsage:
		return false
	default:
		return true
	}
}

func (s *serviceImpl) recordUsageSnapshot(run *model.GraphRun, outcome nodeOutcome, finishedAtMs, durationMs int64) {
	s.usageMu.RLock()
	recorder := s.usageRecorder
	s.usageMu.RUnlock()
	if recorder == nil || outcome.usage == nil {
		return
	}
	if durationMs < 0 {
		durationMs = 0
	}
	workspaceID := ""
	if run != nil {
		workspaceID = run.WorkspaceID
	}
	recorder.Record(outcome.usage.Snapshot(workspaceID, outcome.modelID, finishedAtMs, durationMs))
}

func runtimeError(runID string, key model.GraphInstanceKey, node model.GraphNode, err error) *model.GraphRuntimeError {
	return &model.GraphRuntimeError{
		RunID:       runID,
		InstanceKey: &key,
		NodeID:      node.ID,
		NodeTitle:   node.Title,
		NodeType:    node.Type,
		Message:     err.Error(),
		RetryCount:  graphRetryCount(err),
		CanResume:   true,
	}
}

func instanceKeyString(key model.GraphInstanceKey) string {
	if len(key.Iterations) == 0 {
		return key.NodeID
	}
	parts := make([]string, 0, len(key.Iterations)+1)
	for _, it := range key.Iterations {
		parts = append(parts, fmt.Sprintf("%s#%d", it.LoopNodeID, it.Index))
	}
	parts = append(parts, key.NodeID)
	return strings.Join(parts, "/")
}

func edgeStateKey(edgeID string, target model.GraphInstanceKey) string {
	return edgeID + "@" + instanceKeyString(target)
}

func disabledNameSet(names []string) map[string]struct{} {
	out := make(map[string]struct{}, len(names))
	for _, name := range names {
		out[name] = struct{}{}
	}
	return out
}

func cloneStringMap(in map[string]string) map[string]string {
	out := make(map[string]string, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneStringSet(in map[string]struct{}) map[string]struct{} {
	out := make(map[string]struct{}, len(in))
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneGraphConfig(in model.GraphConfig) model.GraphConfig {
	out := in
	if in.Nodes != nil {
		out.Nodes = append([]model.GraphNode(nil), in.Nodes...)
	}
	if in.Edges != nil {
		out.Edges = cloneGraphEdges(in.Edges)
	}
	out.Variables = cloneStringMap(in.Variables)
	if in.DisabledVars != nil {
		out.DisabledVars = append([]string(nil), in.DisabledVars...)
	}
	return out
}

func cloneGraphEdges(in []model.GraphEdge) []model.GraphEdge {
	if in == nil {
		return nil
	}
	return append([]model.GraphEdge(nil), in...)
}

func cloneGraphRun(in *model.GraphRun) *model.GraphRun {
	if in == nil {
		return nil
	}
	out := *in
	out.BaseSnapshot.Config = cloneGraphConfig(in.BaseSnapshot.Config)
	if in.BaseSnapshot.ModelSnapshots != nil {
		out.BaseSnapshot.ModelSnapshots = make(map[string]model.ModelInstance, len(in.BaseSnapshot.ModelSnapshots))
		for k, v := range in.BaseSnapshot.ModelSnapshots {
			out.BaseSnapshot.ModelSnapshots[k] = v
		}
	}
	if in.BaseSnapshot.AgentSnapshots != nil {
		out.BaseSnapshot.AgentSnapshots = make(map[string]model.GraphAgentSnapshot, len(in.BaseSnapshot.AgentSnapshots))
		for k, v := range in.BaseSnapshot.AgentSnapshots {
			out.BaseSnapshot.AgentSnapshots[k] = v
		}
	}
	if in.Versions != nil {
		out.Versions = make([]model.GraphRunVersion, len(in.Versions))
		for i, v := range in.Versions {
			out.Versions[i] = v
			out.Versions[i].Config = cloneGraphConfig(v.Config)
		}
	}
	return &out
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

type graphEventHandler struct {
	ctx       context.Context
	svc       *serviceImpl
	buf       *graphEventBuffer
	runID     string
	jobID     string
	sessionID string
	nodeID    string
	key       model.GraphInstanceKey

	mu        sync.Mutex
	content   strings.Builder
	msgID     string
	thoughtID string
	// lastStartedID is the id assigned by the most recent OnMessageStart /
	// OnThoughtStart and is what LastMessageID returns. The round.Builder
	// calls LastMessageID immediately after each Start to capture either the
	// message id (KeyMsgID) or the thought id (KeyThoughtMsgID) for the round
	// it is about to flush. Returning only msgID here would leave
	// currentThoughtMsgID empty, so the reasoning segment is persisted WITHOUT
	// a thought_msg_id — which makes the history API fold the thought into the
	// content bubble instead of emitting it as its own ordered entry, and the
	// frontend's mergeMessages then appends every live thought bubble to the
	// END of the conversation on reload. Tracking the last-started id (matching
	// services/job loopEventHandler's single-id semantics) keeps thought and
	// content correlatable across live SSE and history reload.
	lastStartedID     string
	currentMessageBuf strings.Builder
	nextBoundaryTs    int64
	usage             *usagestats.Accumulator
}

var _ agui.EventHandler = (*graphEventHandler)(nil)
var _ agui.BoundaryTimestampSetter = (*graphEventHandler)(nil)

func (s *serviceImpl) newGraphEventHandler(ctx context.Context, runID, jobID, sessionID, nodeID string, key model.GraphInstanceKey) *graphEventHandler {
	return &graphEventHandler{
		ctx:       ctx,
		svc:       s,
		buf:       s.eventBuffer(runID),
		runID:     runID,
		jobID:     jobID,
		sessionID: sessionID,
		nodeID:    nodeID,
		key:       key,
		usage:     usagestats.NewAccumulator(),
	}
}

func (h *graphEventHandler) SetNextBoundaryTimestamp(ts int64) {
	h.mu.Lock()
	h.nextBoundaryTs = ts
	h.mu.Unlock()
}

func (h *graphEventHandler) timestampLocked() int64 {
	ts := h.nextBoundaryTs
	if ts == 0 {
		ts = time.Now().UnixMilli()
	}
	h.nextBoundaryTs = 0
	return ts
}

// appendEventLocked publishes a streaming agent event (message/thought/tool
// deltas) to the run's in-memory event buffer for real-time SSE delivery. These
// events are NEVER persisted: the authoritative agent conversation already
// lives in each node's session messages.jsonl, so persisting the per-token
// deltas here would duplicate it and let events.jsonl grow without bound on
// long loops. A missing buffer (run not active in this process) drops the
// event — there is no live SSE reader to deliver to anyway.
func (h *graphEventHandler) appendEventLocked(typ model.GraphEventType, message string, payload map[string]string, createdAt int64) {
	if h.buf == nil {
		return
	}
	if payload == nil {
		payload = map[string]string{}
	}
	payload["sessionId"] = h.sessionID
	payload["jobId"] = h.jobID
	h.buf.Publish(&model.GraphEvent{
		ID:          model.NewGraphEventID(),
		RunID:       h.runID,
		Type:        typ,
		InstanceKey: &h.key,
		NodeID:      h.nodeID,
		Message:     message,
		Payload:     payload,
		CreatedAt:   createdAt,
	})
}

func (h *graphEventHandler) OnMessageStart() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.msgID = uuid.NewString()
	h.lastStartedID = h.msgID
	h.currentMessageBuf.Reset()
	h.appendEventLocked(model.GraphEventTypeAgentMessageStart, "agent message started", map[string]string{"messageId": h.msgID}, h.timestampLocked())
	return nil
}

func (h *graphEventHandler) OnMessageDelta(content string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.content.WriteString(content)
	h.currentMessageBuf.WriteString(content)
	h.appendEventLocked(model.GraphEventTypeAgentMessageDelta, content, map[string]string{"messageId": h.msgID, "delta": content}, time.Now().UnixMilli())
	return nil
}

func (h *graphEventHandler) OnMessageEnd() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.usage != nil {
		h.usage.OnAssistantText(h.ctx, h.currentMessageBuf.String())
	}
	h.currentMessageBuf.Reset()
	h.appendEventLocked(model.GraphEventTypeAgentMessageEnd, "agent message ended", map[string]string{"messageId": h.msgID}, h.timestampLocked())
	return nil
}
func (h *graphEventHandler) LastMessageID() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.lastStartedID
}
func (h *graphEventHandler) OnThoughtStart() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.thoughtID = uuid.NewString()
	h.lastStartedID = h.thoughtID
	h.currentMessageBuf.Reset()
	h.appendEventLocked(model.GraphEventTypeAgentThoughtStart, "agent thought started", map[string]string{"messageId": h.thoughtID}, h.timestampLocked())
	return nil
}
func (h *graphEventHandler) OnThoughtDelta(content string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.currentMessageBuf.WriteString(content)
	h.appendEventLocked(model.GraphEventTypeAgentThoughtDelta, content, map[string]string{"messageId": h.thoughtID, "delta": content}, time.Now().UnixMilli())
	return nil
}
func (h *graphEventHandler) OnThoughtEnd() error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.usage != nil {
		h.usage.OnThoughtText(h.ctx, h.currentMessageBuf.String())
	}
	h.currentMessageBuf.Reset()
	h.appendEventLocked(model.GraphEventTypeAgentThoughtEnd, "agent thought ended", map[string]string{"messageId": h.thoughtID}, h.timestampLocked())
	return nil
}
func (h *graphEventHandler) OnToolCallStart(id, name string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.timestampLocked()
	if h.usage != nil {
		h.usage.OnToolCallStart(id, name, now)
	}
	h.appendEventLocked(model.GraphEventTypeAgentToolStart, "tool call started", map[string]string{
		"toolCallId": id,
		"toolName":   name,
		"status":     string(model.ToolCallStatusProcessing),
	}, now)
	return nil
}
func (h *graphEventHandler) OnToolCallArgs(id, args string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.usage != nil {
		h.usage.OnToolCallArgsDelta(id, args)
	}
	h.appendEventLocked(model.GraphEventTypeAgentToolArgs, args, map[string]string{
		"toolCallId": id,
		"delta":      args,
		"status":     string(model.ToolCallStatusProcessing),
	}, time.Now().UnixMilli())
	return nil
}
func (h *graphEventHandler) OnToolCallResult(id, content string, success bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	status := model.ToolCallStatusSuccess
	if !success {
		status = model.ToolCallStatusError
	}
	h.appendEventLocked(model.GraphEventTypeAgentToolResult, content, map[string]string{
		"toolCallId": id,
		"delta":      content,
		"status":     string(status),
	}, time.Now().UnixMilli())
	return nil
}
func (h *graphEventHandler) OnToolCallEnd(id string, success bool) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.timestampLocked()
	if h.usage != nil {
		h.usage.OnToolCallEnd(h.ctx, id, now)
	}
	status := model.ToolCallStatusSuccess
	if !success {
		status = model.ToolCallStatusError
	}
	h.appendEventLocked(model.GraphEventTypeAgentToolEnd, "tool call ended", map[string]string{
		"toolCallId": id,
		"status":     string(status),
	}, now)
	return nil
}
func (h *graphEventHandler) OnToolCallInterrupted(id string, reason string) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.appendEventLocked(model.GraphEventTypeAgentToolEnd, "tool call interrupted", map[string]string{
		"toolCallId":        id,
		"status":            string(model.ToolCallStatusPlaceholder),
		"placeholderReason": reason,
	}, h.timestampLocked())
	return nil
}
func (h *graphEventHandler) OnToolCallStitched(id string, content string, success bool, supersededAgoMs int64) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	now := h.timestampLocked()
	if h.usage != nil {
		h.usage.OnToolCallEnd(h.ctx, id, now)
	}
	status := model.ToolCallStatusSuccess
	if !success {
		status = model.ToolCallStatusError
	}
	h.appendEventLocked(model.GraphEventTypeAgentToolResult, content, map[string]string{
		"toolCallId":      id,
		"delta":           content,
		"status":          string(status),
		"supersededAgoMs": fmt.Sprintf("%d", supersededAgoMs),
		"stitched":        "true",
	}, now)
	return nil
}
func (h *graphEventHandler) OnTokenUsage(totalTokens int) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.usage != nil {
		h.usage.OnTokenUsage(totalTokens)
	}
	h.appendEventLocked(model.GraphEventTypeAgentTokenUsage, "token usage updated", map[string]string{"totalTokens": fmt.Sprintf("%d", totalTokens)}, time.Now().UnixMilli())
	return nil
}
func (h *graphEventHandler) OnError(err error) {
	if err == nil {
		return
	}
	if ctxErr := h.ctx.Err(); ctxErr != nil {
		logger.Debugf(h.ctx, "[graphEventHandler] context done, suppressing agent error event: runId=%s nodeId=%s ctxErr=%v err=%v",
			h.runID, h.nodeID, ctxErr, err)
		return
	}
	logger.Errorf(h.ctx, "[graphEventHandler] agent error: runId=%s nodeId=%s err=%v", h.runID, h.nodeID, err)
	h.mu.Lock()
	defer h.mu.Unlock()
	h.appendEventLocked(model.GraphEventTypeError, err.Error(), map[string]string{"agentError": "true"}, time.Now().UnixMilli())
}

func (h *graphEventHandler) AccumulatedContent() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.content.String()
}
