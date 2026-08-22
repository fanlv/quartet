package graph

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"sort"
	"strings"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
)

// Run-control service methods (step 16) and resume support (step 15).

// StopRun hard-stops a running GraphRun.
func (s *serviceImpl) StopRun(ctx context.Context, runID, reason string) (*model.GraphRun, error) {
	reason = orDefault(reason, "hard stopped by user")
	run, err := s.signalAndSnapshot(ctx, runID, controlSignal{kind: ctrlHardStop, reason: reason})
	if err == nil {
		return run, nil
	}
	if !errors.Is(err, ErrGraphRunNotRunning) {
		return nil, err
	}
	// No live scheduler holds this run in this process. If it is still persisted
	// in an in-flight status it is an orphan — the scheduler died (crash/restart)
	// without writing a terminal status, so the run looks "运行中" forever and a
	// plain stop 409s. Force it to stopped (resumable) and reconcile the bound
	// Job instead of surfacing ErrGraphRunNotRunning to the user.
	forced, ferr := s.forceTerminalNoScheduler(ctx, runID, model.GraphRunStatusStopped, reason, "")
	if ferr != nil {
		return nil, ferr
	}
	if forced == nil {
		// Not in-flight — there is genuinely no running scheduler to signal.
		return nil, err
	}
	return forced, nil
}

// StopRunAndWait hard-stops a live scheduler and joins its complete lifecycle.
// The handle is captured before consulting persistence: the run files may have
// disappeared concurrently, but an in-process producer still must be stopped
// before a caller can safely delete surrounding artifacts. Settled runs are an
// idempotent success, while persisted in-flight runs without a scheduler are
// repaired through the same orphan path as StopRun.
func (s *serviceImpl) StopRunAndWait(ctx context.Context, runID, reason string) (*model.GraphRun, error) {
	lifecycle := s.lifecycle(runID)
	reason = orDefault(reason, "hard stopped by user")

	// Deliberately perform the first persistent read without the lifecycle lock
	// when there is no handle. A concurrently-started ResumeRun may claim its
	// generation while this read is in flight; the loop below then observes and
	// joins it instead of incorrectly repairing the run as schedulerless.
	lifecycle.mu.Lock()
	handle := lifecycle.handle
	lifecycle.mu.Unlock()
	var observed *model.GraphRun
	if handle == nil {
		run, err := s.runRepo.GetRun(ctx, runID)
		if err != nil {
			return nil, graphRunLoadError(runID, err)
		}
		observed = run
	}

	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		lifecycle.mu.Lock()
		if lifecycle.deleted {
			lifecycle.mu.Unlock()
			if observed != nil {
				return observed, nil
			}
			return nil, ErrGraphRunNotFound
		}
		handle = lifecycle.handle
		if handle != nil {
			lifecycle.mu.Unlock()
			// A destructive stop is never dropped when the ordinary control queue
			// is full. Wait until the scheduler accepts it or completes; unlike
			// ordinary controls, queue pressure is not exposed as ControlBusy.
			select {
			case handle.controlCh <- controlSignal{kind: ctrlHardStop, reason: reason}:
			case <-handle.done:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			select {
			case <-handle.done:
			case <-ctx.Done():
				return nil, ctx.Err()
			}
			// A resume that was already contending for this lifecycle may have
			// installed the next generation. Loop until we can linearize a point
			// with no live producer.
			continue
		}
		lifecycle.mu.Unlock()

		run, err := s.runRepo.GetRun(ctx, runID)
		if err != nil {
			return nil, graphRunLoadError(runID, err)
		}
		observed = run
		if !isSchedulerlessInFlight(run.Status) {
			// Linearize the no-handle return with ResumeRun and DeleteRun. A
			// generation that registers before this lock is observed and joined; a
			// generation that registers after it is ordered after this completed
			// stop operation.
			lifecycle.mu.Lock()
			if lifecycle.deleted {
				lifecycle.mu.Unlock()
				return observed, nil
			}
			if lifecycle.handle != nil {
				lifecycle.mu.Unlock()
				continue
			}
			latest, latestErr := s.runRepo.GetRun(ctx, runID)
			lifecycle.mu.Unlock()
			if latestErr != nil {
				return nil, graphRunLoadError(runID, latestErr)
			}
			if isSchedulerlessInFlight(latest.Status) {
				continue
			}
			return latest, nil
		}
		forced, err := s.forceTerminalNoSchedulerLocked(ctx, lifecycle, runID, model.GraphRunStatusStopped, reason, "")
		if errors.Is(err, ErrGraphRunNotFound) {
			// Concurrent deletion won before the orphan repair commit. The
			// destructive postcondition is already stronger than stopped.
			return observed, nil
		}
		if err != nil {
			return nil, err
		}
		if forced != nil {
			return forced, nil
		}
	}
}

// ReconcileInterruptedRun repairs a run orphaned by a crash/restart, moving an
// in-flight run to `recovering` (resumable) with its running instances marked
// interrupted and the bound Job set non-running. No-op for settled runs.
func (s *serviceImpl) ReconcileInterruptedRun(ctx context.Context, runID string) error {
	_, err := s.forceTerminalNoScheduler(ctx, runID, model.GraphRunStatusRecovering,
		"startup recovery", "interrupted: process restarted while running")
	return err
}

// forceTerminalNoScheduler repairs a run that has no live scheduler in this
// process but is still persisted in a scheduler-bound in-flight status
// (running/stepStopping/pending). Still-running instances are marked
// interrupted — so a later resume re-runs them (isResettableStatus) and the
// progress no longer reports phantom running nodes — the run is moved to
// `target`, and the bound Job is reconciled to a non-running status via the
// persistent job sink. Returns (nil, nil) when the run is not scheduler-bound
// in-flight (nothing to repair). `progressErr` is recorded as the run's
// interruption reason (empty clears it).
func (s *serviceImpl) forceTerminalNoScheduler(ctx context.Context, runID string, target model.GraphRunStatus, reason, progressErr string) (*model.GraphRun, error) {
	lifecycle := s.lifecycle(runID)
	return s.forceTerminalNoSchedulerLocked(ctx, lifecycle, runID, target, reason, progressErr)
}

// forceTerminalNoSchedulerLocked takes the lifecycle lock for its commit. It
// revalidates the persisted status under that lock so DeleteRun or ResumeRun
// cannot interleave with the terminal writes.
func (s *serviceImpl) forceTerminalNoSchedulerLocked(ctx context.Context, lifecycle *runLifecycle, runID string, target model.GraphRunStatus, reason, progressErr string) (*model.GraphRun, error) {
	run, err := s.runRepo.GetRun(ctx, runID)
	if err != nil {
		return nil, graphRunLoadError(runID, err)
	}
	if !isSchedulerlessInFlight(run.Status) {
		return nil, nil
	}
	finishedAt := time.Now().UnixMilli()
	var insts map[string]model.GraphInstanceState
	instancesChanged := false
	if insts, ierr := s.runRepo.GetInstances(ctx, runID); ierr == nil {
		for k, st := range insts {
			if st.Status == model.GraphInstanceStatusRunning {
				st.Status = model.GraphInstanceStatusInterrupted
				insts[k] = st
				instancesChanged = true
			}
		}
	} else {
		logger.Warnf(ctx, "[graph] reconcile orphan: load instances failed: runId=%s err=%v", runID, ierr)
	}
	prior := run.Status
	run.Status = target
	run.FinishedAt = finishedAt
	run.UpdatedAt = time.Now()
	if run.Progress != nil {
		run.Progress.LastError = progressErr
	}

	// The loads and transformation above need not exclude DeleteRun. Only the
	// commit must be linearized. Recheck both the tombstone and handle while
	// holding the lifecycle lock, then keep it through every write.
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	if lifecycle.deleted {
		return nil, ErrGraphRunNotFound
	}
	if lifecycle.handle != nil {
		return nil, nil
	}
	latest, err := s.runRepo.GetRun(ctx, runID)
	if err != nil {
		return nil, graphRunLoadError(runID, err)
	}
	if !isSchedulerlessInFlight(latest.Status) {
		return nil, nil
	}
	if instancesChanged {
		if serr := s.runRepo.SaveInstances(ctx, runID, insts); serr != nil {
			logger.Warnf(ctx, "[graph] reconcile orphan: save instances failed: runId=%s err=%v", runID, serr)
		} else {
			updateRunProgress(run, insts)
		}
	}
	if err := s.runRepo.SaveRun(ctx, run); err != nil {
		return nil, err
	}
	logger.Infof(ctx, "[graph] reconciled schedulerless run: runId=%s jobId=%s priorStatus=%s newStatus=%s reason=%s",
		runID, run.JobID, prior, target, reason)
	if s.jobSink != nil && run.JobID != "" {
		if err := s.jobSink.SetGraphRunState(ctx, run.JobID, runID, model.JobStatusStopped, target, run.StartedAt, finishedAt, ""); err != nil {
			logger.Warnf(ctx, "[graph] reconcile orphan: set job state failed: jobId=%s runId=%s err=%v", run.JobID, runID, err)
		}
	}
	return run, nil
}

// isSchedulerlessInFlight reports whether a persisted status implies a live
// scheduler that, absent from this process, marks the run as an orphan needing
// reconcile. Recovering/awaitingInput and the terminal states already have no
// scheduler by design and are left untouched.
func isSchedulerlessInFlight(st model.GraphRunStatus) bool {
	switch st {
	case model.GraphRunStatusRunning, model.GraphRunStatusStepStopping, model.GraphRunStatusPending:
		return true
	default:
		return false
	}
}

// StepStopRun freezes the current ready batch and stops after it.
func (s *serviceImpl) StepStopRun(ctx context.Context, runID, reason string) (*model.GraphRun, error) {
	return s.signalAndSnapshot(ctx, runID, controlSignal{kind: ctrlStepStop, reason: orDefault(reason, "step-stopped by user")})
}

// CancelStopRun cancels a pending step-stop that has not yet settled,
// releasing the held dispatch frontier and returning the run to running.
func (s *serviceImpl) CancelStopRun(ctx context.Context, runID, reason string) (*model.GraphRun, error) {
	return s.signalAndSnapshot(ctx, runID, controlSignal{kind: ctrlCancelStop, reason: orDefault(reason, "stop cancelled by user")})
}

// signalAndSnapshot delivers a control signal then returns the current run
// snapshot. The actual state transition happens asynchronously in the
// scheduler goroutine.
func (s *serviceImpl) signalAndSnapshot(ctx context.Context, runID string, sig controlSignal) (*model.GraphRun, error) {
	lifecycle := s.lifecycle(runID)
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	deleted := lifecycle.deleted
	handle := lifecycle.handle
	if deleted {
		return nil, ErrGraphRunNotFound
	}
	run, err := s.runRepo.GetRun(ctx, runID)
	if err != nil {
		return nil, graphRunLoadError(runID, err)
	}
	// A scheduler handle may outlive its persisted running state while it drains
	// final Job updates/hooks. Ordinary controls are state transitions, not joins,
	// and must reject that post-terminal window.
	if !isSchedulerlessInFlight(run.Status) || handle == nil {
		return nil, ErrGraphRunNotRunning
	}
	if err := sendControlToHandle(handle, sig); err != nil {
		return nil, err
	}
	return s.runRepo.GetRun(ctx, runID)
}

// resumableStatuses are the GraphRun statuses from which ResumeRun is allowed.
// awaitingInput is included: a parked clarify run is a resumable terminal (its
// scheduler has exited and state is persisted), continued via ContinueRun which
// shares this resume kernel — the continue finalizes the clarify instances on
// the way through seedResume (finalizeAwaitingClarify).
func isResumableStatus(st model.GraphRunStatus) bool {
	switch st {
	case model.GraphRunStatusFailed, model.GraphRunStatusStepStopped,
		model.GraphRunStatusStopped,
		model.GraphRunStatusTimedOut, model.GraphRunStatusRecovering,
		model.GraphRunStatusAwaitingInput:
		return true
	default:
		return false
	}
}

// isInFlightStatus reports whether a run is actively scheduling (and so cannot
// be deleted). Recovering is deliberately excluded: it is a static resumable
// state after crash recovery, with no live scheduler to accept controls.
func isInFlightStatus(st model.GraphRunStatus) bool {
	switch st {
	case model.GraphRunStatusRunning, model.GraphRunStatusStepStopping:
		return true
	default:
		return false
	}
}

// ResumeRun relaunches a resumable GraphRun. It resets the resettable terminal
// instances (failed/interrupted) and their downstream, keeps succeeded/skipped
// instances, then re-launches the scheduler in resume mode.
func (s *serviceImpl) ResumeRun(ctx context.Context, runID string, runner Runner, jobs JobStateSink) (*model.GraphRun, error) {
	if runner == nil {
		return nil, ErrGraphRunnerMissing
	}
	lifecycle := s.lifecycle(runID)
	run, err := s.runRepo.GetRun(ctx, runID)
	if err != nil {
		return nil, graphRunLoadError(runID, err)
	}
	if !isResumableStatus(run.Status) {
		return nil, fmt.Errorf("%w: status=%s", ErrGraphRunNotResumable, run.Status)
	}
	return s.relaunchResumableRunLocked(ctx, lifecycle, run, runner, jobs)
}

// ContinueRun continues a GraphRun parked at「等待人工」(§5 讨论完成/续跑): the user
// finished discussing in the clarify node session(s) and clicked「讨论完成」. It
// shares the resume kernel with ResumeRun — relaunchResumableRun re-enters the
// scheduler in resume mode, and seedResume's finalizeAwaitingClarify captures
// each clarify's 结论 (the session's last assistant message), flips it to
// succeeded, and resolves its held out-edges so the DAG continues. Guarded to the
// awaitingInput status specifically so a misfired continue on a failed/stopped
// run returns a clear error rather than silently resuming.
func (s *serviceImpl) ContinueRun(ctx context.Context, runID string, runner Runner, jobs JobStateSink) (*model.GraphRun, error) {
	if runner == nil {
		return nil, ErrGraphRunnerMissing
	}
	lifecycle := s.lifecycle(runID)
	run, err := s.runRepo.GetRun(ctx, runID)
	if err != nil {
		return nil, graphRunLoadError(runID, err)
	}
	if run.Status != model.GraphRunStatusAwaitingInput {
		return nil, fmt.Errorf("%w: continue requires awaitingInput, status=%s", ErrGraphRunNotResumable, run.Status)
	}
	return s.relaunchResumableRunLocked(ctx, lifecycle, run, runner, jobs)
}

// relaunchResumableRun is the shared resume kernel for ResumeRun and ContinueRun:
// it resets the resettable terminal instances (failed/interrupted) and their
// downstream, keeps succeeded/skipped (and awaitingInput) instances, persists the
// post-reset state, then re-launches the scheduler in resume mode. The caller is
// responsible for status validation; `run` is the already-loaded run.
func (s *serviceImpl) relaunchResumableRun(ctx context.Context, run *model.GraphRun, runner Runner, jobs JobStateSink) (*model.GraphRun, error) {
	lifecycle := s.lifecycle(run.ID)
	return s.relaunchResumableRunLocked(ctx, lifecycle, run, runner, jobs)
}

func (s *serviceImpl) relaunchResumableRunLocked(ctx context.Context, lifecycle *runLifecycle, run *model.GraphRun, runner Runner, jobs JobStateSink) (*model.GraphRun, error) {
	runID := run.ID
	var handle *runControl
	var runCtx context.Context
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		lifecycle.mu.Lock()
		if lifecycle.deleted {
			lifecycle.mu.Unlock()
			return nil, ErrGraphRunNotFound
		}
		if existing := lifecycle.handle; existing != nil {
			lifecycle.mu.Unlock()
			select {
			case <-existing.done:
				continue
			case <-ctx.Done():
				return nil, ctx.Err()
			}
		}
		// Re-read after winning the lifecycle slot. A concurrent resume may have
		// completed while this caller waited, invalidating its earlier snapshot.
		latest, err := s.runRepo.GetRun(ctx, runID)
		if err != nil {
			lifecycle.mu.Unlock()
			return nil, graphRunLoadError(runID, err)
		}
		if !isResumableStatus(latest.Status) {
			lifecycle.mu.Unlock()
			return nil, fmt.Errorf("%w: status=%s", ErrGraphRunNotResumable, latest.Status)
		}
		handle, runCtx = newRunControl()
		lifecycle.handle = handle
		lifecycle.mu.Unlock()
		run = latest
		break
	}
	launched := false
	defer func() {
		if !launched {
			s.completeControl(runID, handle, s.getBuffer(runID))
		}
	}()
	instances, err := s.runRepo.GetInstances(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("load instances failed: %w", err)
	}
	edges, err := s.runRepo.GetEdges(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("load edges failed: %w", err)
	}
	vars, err := s.runRepo.GetVariables(ctx, runID)
	if err != nil {
		return nil, fmt.Errorf("load variables failed: %w", err)
	}
	originalRun := cloneGraphRun(run)
	originalInstances := maps.Clone(instances)
	originalEdges := maps.Clone(edges)
	originalVars := maps.Clone(vars)
	restoreOriginal := func(cause error) error {
		var restoreErrors []error
		if err := s.runRepo.SaveInstances(ctx, runID, originalInstances); err != nil {
			restoreErrors = append(restoreErrors, fmt.Errorf("restore instances failed: %w", err))
		}
		if err := s.runRepo.SaveEdges(ctx, runID, originalEdges); err != nil {
			restoreErrors = append(restoreErrors, fmt.Errorf("restore edges failed: %w", err))
		}
		if err := s.runRepo.SaveVariables(ctx, runID, originalVars); err != nil {
			restoreErrors = append(restoreErrors, fmt.Errorf("restore variables failed: %w", err))
		}
		if err := s.runRepo.SaveRun(ctx, originalRun); err != nil {
			restoreErrors = append(restoreErrors, fmt.Errorf("restore run failed: %w", err))
		}
		if len(restoreErrors) == 0 {
			return cause
		}
		return errors.Join(append([]error{cause}, restoreErrors...)...)
	}

	cfg := effectiveConfig(run)
	var loopState map[string]model.GraphLoopState
	if run.Resume != nil {
		loopState = run.Resume.LoopState
	}
	rb := newResumeBuilder(cfg, instances, edges, vars, loopState)
	rb.resetResettable()
	releaseAgentLeases, err := validateResumableAgentBindings(
		ctx,
		run,
		rb.instances,
		rb.archived,
		rb.resetVersion,
		runner,
	)
	if err != nil {
		return nil, err
	}
	defer releaseAgentLeases()

	// Preserve the sessions of instances the reset removed so the Chat sidebar
	// keeps listing prior-attempt conversations across the resume. A re-run that
	// reaches the same instance key overwrites the live instance; the archive is
	// only consulted for keys no longer present live (frontend merges live-first).
	if len(rb.archived) > 0 {
		if run.ArchivedInstances == nil {
			run.ArchivedInstances = map[string]model.GraphInstanceState{}
		}
		maps.Copy(run.ArchivedInstances, rb.archived)
	}
	if len(rb.resetVersion) > 0 {
		if run.RetryInstanceVersions == nil {
			run.RetryInstanceVersions = map[string]int{}
		}
		maps.Copy(run.RetryInstanceVersions, rb.resetVersion)
	}

	if err := s.runRepo.SaveInstances(ctx, runID, rb.instances); err != nil {
		return nil, restoreOriginal(err)
	}
	if err := s.runRepo.SaveEdges(ctx, runID, rb.edges); err != nil {
		return nil, restoreOriginal(err)
	}
	if err := s.runRepo.SaveVariables(ctx, runID, rb.vars); err != nil {
		return nil, restoreOriginal(err)
	}
	// Clear the frozen batch / step flags so a fresh scheduler does not re-freeze.
	if run.Resume != nil {
		run.Resume.FrozenBatch = nil
	}
	run.Status = model.GraphRunStatusRunning
	run.LastError = nil
	if run.Progress != nil {
		run.Progress.LastError = ""
	}
	run.UpdatedAt = time.Now()
	if err := s.runRepo.SaveRun(ctx, run); err != nil {
		return nil, restoreOriginal(err)
	}
	if jobs != nil {
		if err := jobs.SetGraphRunState(ctx, run.JobID, run.ID, model.JobStatusRunning, model.GraphRunStatusRunning, run.StartedAt, 0, ""); err != nil {
			return nil, restoreOriginal(fmt.Errorf("set GraphRun Job state before resume failed: %w", err))
		}
	}

	releaseAgentLeases()
	releaseAgentLeases = func() {}
	launched = true
	go s.runGraph(runCtx, runID, runner, jobs, true, handle)
	return run, nil
}

func validateResumableAgentBindings(
	ctx context.Context,
	run *model.GraphRun,
	surviving map[string]model.GraphInstanceState,
	archived map[string]model.GraphInstanceState,
	retryVersions map[string]int,
	runner Runner,
) (func(), error) {
	releaseAll := func() {}
	bindingResolver, hasBindingResolver := runner.(AgentBindingLeaseResolver)
	sessionResolver, hasSessionResolver := runner.(AgentSessionLeaseResolver)
	legacyResolver, hasLegacyResolver := runner.(LegacyAgentBindingResolver)
	if !hasBindingResolver && !hasSessionResolver {
		return releaseAll, nil
	}
	cfg := effectiveConfig(run)

	var releases []func()
	releaseAll = func() {
		for index := len(releases) - 1; index >= 0; index-- {
			if releases[index] != nil {
				releases[index]()
			}
		}
	}
	validatedBindings := make(map[string]bool)
	validatedSessions := make(map[string]bool)
	validateBinding := func(binding *model.AgentRuntimeBinding, nodeID string) error {
		if binding == nil || validatedBindings[binding.RuntimeKey] || !hasBindingResolver {
			return nil
		}
		release, err := bindingResolver.ValidateAgentBindingWithLease(ctx, *binding)
		if err != nil {
			return fmt.Errorf(
				"Graph node %q AgentID %q revision %q cannot resume: %w",
				nodeID,
				binding.AgentID,
				binding.Revision,
				err,
			)
		}
		validatedBindings[binding.RuntimeKey] = true
		releases = append(releases, release)
		return nil
	}
	validateSession := func(sessionID, nodeID string) error {
		if sessionID == "" || validatedSessions[sessionID] || !hasSessionResolver {
			return nil
		}
		release, err := sessionResolver.ValidateSessionAgentWithLease(ctx, sessionID)
		if err != nil {
			return fmt.Errorf("Graph node %q session %q cannot resume: %w", nodeID, sessionID, err)
		}
		validatedSessions[sessionID] = true
		releases = append(releases, release)
		return nil
	}

	inheritedSources := possibleInheritedSessionSources(cfg, surviving, retryVersions)
	for _, state := range surviving {
		if state.SessionID != "" && inheritedSources[state.NodeID] {
			if err := validateSession(state.SessionID, state.NodeID); err != nil {
				releaseAll()
				return func() {}, err
			}
		}
	}
	for _, state := range archived {
		sessionID := firstNonEmpty(state.SessionID, state.DisplaySessionID)
		if sessionID != "" &&
			state.NodeType == model.GraphNodeTypePrompt &&
			state.Status == model.GraphInstanceStatusInterrupted {
			if err := validateSession(sessionID, state.NodeID); err != nil {
				releaseAll()
				return func() {}, err
			}
		}
	}
	for _, node := range cfg.Nodes {
		if !isAgent(node.Type) || node.Config.SessionStrategy == model.GraphSessionStrategyInherit {
			continue
		}
		versions := map[int]bool{}
		for key, version := range retryVersions {
			if version > 0 && retryInstanceNodeID(key) == node.ID {
				versions[version] = true
			}
		}
		if shouldValidateCurrentNodeVersion(node, cfg, surviving, retryVersions) {
			versions[run.CurrentVersion] = true
		}
		for version := range versions {
			binding := agentBindingForNodeVersion(run, node.ID, version)
			if binding == nil && hasLegacyResolver {
				legacy, err := legacyResolver.ResolveLegacyAgentBinding(ctx, node.Config.AgentType)
				if err != nil {
					releaseAll()
					return func() {}, fmt.Errorf(
						"Graph node %q Agent %q cannot resume version %d: %w",
						node.ID,
						node.Config.AgentType,
						version,
						err,
					)
				}
				binding = legacy
			}
			if binding == nil {
				releaseAll()
				return func() {}, fmt.Errorf(
					"Graph node %q Agent %q cannot resume version %d: runtime binding is missing",
					node.ID,
					node.Config.AgentType,
					version,
				)
			}
			if err := validateBinding(binding, node.ID); err != nil {
				releaseAll()
				return func() {}, err
			}
		}
	}
	return releaseAll, nil
}

func possibleInheritedSessionSources(
	cfg model.GraphConfig,
	surviving map[string]model.GraphInstanceState,
	retryVersions map[string]int,
) map[string]bool {
	nodes := make(map[string]model.GraphNode, len(cfg.Nodes))
	reverse := make(map[string][]string)
	var pending []string
	for _, node := range cfg.Nodes {
		nodes[node.ID] = node
		if isAgent(node.Type) &&
			node.Config.SessionStrategy == model.GraphSessionStrategyInherit &&
			nodeMayExecuteOnResume(node, cfg, surviving, retryVersions) {
			pending = append(pending, node.ID)
		}
	}
	for _, edge := range cfg.Edges {
		reverse[edge.TargetNodeID] = append(reverse[edge.TargetNodeID], edge.SourceNodeID)
	}
	result := make(map[string]bool)
	seen := make(map[string]bool)
	for len(pending) > 0 {
		nodeID := pending[len(pending)-1]
		pending = pending[:len(pending)-1]
		if seen[nodeID] {
			continue
		}
		seen[nodeID] = true
		node := nodes[nodeID]
		if node.ParentID != "" && node.Type == model.GraphNodeTypeStart {
			pending = append(pending, node.ParentID)
		}
		for _, sourceID := range reverse[nodeID] {
			result[sourceID] = true
			pending = append(pending, sourceID)
		}
	}
	return result
}

func shouldValidateCurrentNodeVersion(
	node model.GraphNode,
	cfg model.GraphConfig,
	surviving map[string]model.GraphInstanceState,
	retryVersions map[string]int,
) bool {
	if !nodeMayExecuteOnResume(node, cfg, surviving, retryVersions) {
		return false
	}
	return node.ParentID != "" || !nodeHasRetryVersion(node.ID, retryVersions)
}

func nodeMayExecuteOnResume(
	node model.GraphNode,
	cfg model.GraphConfig,
	surviving map[string]model.GraphInstanceState,
	retryVersions map[string]int,
) bool {
	hasRetry := nodeHasRetryVersion(node.ID, retryVersions)
	if node.ParentID == "" {
		if hasRetry {
			return true
		}
		for _, state := range surviving {
			if state.NodeID == node.ID {
				return false
			}
		}
		return true
	}
	nodes := make(map[string]model.GraphNode, len(cfg.Nodes))
	for _, candidate := range cfg.Nodes {
		nodes[candidate.ID] = candidate
	}
	for parentID := node.ParentID; parentID != ""; parentID = nodes[parentID].ParentID {
		completed := false
		for _, state := range surviving {
			if state.NodeID == parentID &&
				(state.Status == model.GraphInstanceStatusSucceeded ||
					state.Status == model.GraphInstanceStatusSkipped) {
				completed = true
				break
			}
		}
		if !completed {
			return true
		}
	}
	return false
}

func nodeHasRetryVersion(nodeID string, retryVersions map[string]int) bool {
	for key := range retryVersions {
		if retryInstanceNodeID(key) == nodeID {
			return true
		}
	}
	return false
}

func retryInstanceNodeID(key string) string {
	if slash := strings.LastIndex(key, "/"); slash >= 0 {
		return key[slash+1:]
	}
	return key
}

func agentBindingForNodeVersion(run *model.GraphRun, nodeID string, targetVersion int) *model.AgentRuntimeBinding {
	if run == nil {
		return nil
	}
	for _, candidate := range run.Versions {
		if candidate.Version != targetVersion {
			continue
		}
		if binding := bindingFromGraphSnapshot(candidate.AgentSnapshots[nodeID]); binding != nil {
			return binding
		}
	}
	return bindingFromGraphSnapshot(run.BaseSnapshot.AgentSnapshots[nodeID])
}

func bindingFromGraphSnapshot(snapshot model.GraphAgentSnapshot) *model.AgentRuntimeBinding {
	if snapshot.AgentID == "" || snapshot.Revision == "" ||
		snapshot.RuntimeKey == "" || snapshot.Definition.ACPProgram == "" {
		return nil
	}
	return &model.AgentRuntimeBinding{
		AgentID:    snapshot.AgentID,
		Revision:   snapshot.Revision,
		RuntimeKey: snapshot.RuntimeKey,
		Definition: snapshot.Definition,
	}
}

// DeleteRun deletes a non-in-flight GraphRun and clears the bound Job linkage.
func (s *serviceImpl) DeleteRun(ctx context.Context, runID string, jobs JobStateSink) error {
	lifecycle := s.lifecycle(runID)
	lifecycle.mu.Lock()
	defer lifecycle.mu.Unlock()
	if lifecycle.deleted {
		if jobs != nil && lifecycle.deleteJobID != "" {
			if err := jobs.ClearGraphRunLinkage(ctx, lifecycle.deleteJobID, runID); err != nil {
				return fmt.Errorf("clear job graph-run linkage: %w", err)
			}
			lifecycle.deleteJobID = ""
		}
		return nil
	}
	if lifecycle.handle != nil {
		return ErrGraphRunInFlight
	}
	run, err := s.runRepo.GetRun(ctx, runID)
	if err != nil {
		if errors.Is(graphRunLoadError(runID, err), ErrGraphRunNotFound) && jobs != nil && lifecycle.deleteJobID != "" {
			// A prior attempt may have removed run.json and then failed part-way
			// through deleting the rest of graph_run. Retry the idempotent artifact
			// deletion before unlinking; otherwise the missing run.json would make
			// us strand the remaining artifacts permanently. The handler has
			// re-registered the run location, so this also works after restart.
			lifecycle.deleted = true
			if deleteErr := s.runRepo.DeleteRun(ctx, runID); deleteErr != nil {
				lifecycle.deleted = false
				return deleteErr
			}
			s.removeBuffer(runID)
			if unlinkErr := jobs.ClearGraphRunLinkage(ctx, lifecycle.deleteJobID, runID); unlinkErr != nil {
				return fmt.Errorf("clear job graph-run linkage: %w", unlinkErr)
			}
			lifecycle.deleteJobID = ""
			return nil
		}
		return graphRunLoadError(runID, err)
	}
	if isInFlightStatus(run.Status) {
		return fmt.Errorf("%w: status=%s", ErrGraphRunInFlight, run.Status)
	}
	// Persist the delete intent in the process lifecycle before removing the
	// artifacts. A failed artifact delete leaves the Job linkage intact. Once
	// artifacts are gone, a failed unlink remains retryable through the cached
	// Job ID (RegisterRunLocation rebuilds it after process restart).
	lifecycle.deleteJobID = run.JobID
	lifecycle.deleted = true
	if err := s.runRepo.DeleteRun(ctx, runID); err != nil {
		lifecycle.deleted = false
		return err
	}
	// Free the in-memory event buffer (if any reader is still attached, Close
	// wakes it so its SSE handler exits). The run's status is already terminal
	// (in-flight runs are rejected above), so no producer is publishing.
	s.removeBuffer(runID)
	if jobs != nil && lifecycle.deleteJobID != "" {
		if err := jobs.ClearGraphRunLinkage(ctx, lifecycle.deleteJobID, runID); err != nil {
			return fmt.Errorf("clear job graph-run linkage: %w", err)
		}
		lifecycle.deleteJobID = ""
	}
	return nil
}

// sortInstanceKeysByDepth orders instance-key strings by iteration depth so
// parent loop scopes are rebuilt before their children on resume.
func sortInstanceKeysByDepth(keys []model.GraphInstanceKey) {
	sort.Slice(keys, func(i, j int) bool {
		if len(keys[i].Iterations) != len(keys[j].Iterations) {
			return len(keys[i].Iterations) < len(keys[j].Iterations)
		}
		return instanceKeyString(keys[i]) < instanceKeyString(keys[j])
	})
}
