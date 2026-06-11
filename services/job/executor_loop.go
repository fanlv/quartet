package job

import (
	"context"
	"fmt"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
)

// runLoop is the long-running execution loop for Start / Continue. It walks
// the job's LoopConfig flow tree to completion (or cancellation) and drives
// the job's lifecycle defers (finish/stop/fail) on exit. Interactive
// SendMessage rounds use the separate runInteractive entry point — runLoop
// is loop-mode-only.
func (s *serviceImpl) runLoop(ctx context.Context, job *model.Job, runner JobRunner, cfg *model.LoopConfig, cancelEntry *cancelEntry) {
	defer s.clearCancel(job.ID, cancelEntry)
	defer func() {
		// Read Status under the lock — by the time we get here failJob /
		// stopJob may have already flipped it (in this same goroutine) and
		// a concurrent handler-side Start / SendMessage on a now-terminal
		// job could be racing on the same field. The lock both pairs with
		// failJob's Unlock for visibility and avoids a race-detector trip
		// on the cross-goroutine read.
		s.mu.RLock()
		status := job.Status
		s.mu.RUnlock()
		if status != model.JobStatusRunning {
			return
		}
		if ctx.Err() != nil {
			s.stopJob(ctx, job, true)
			return
		}

		s.finishJob(ctx, job, true)
		completedCount, failedCount, totalSteps := s.progressCounts(job)
		if totalSteps > 0 {
			if failedCount > 0 {
				logger.Infof(ctx, "[loop] done: jobId=%s ok=%d fail=%d", job.ID, completedCount, failedCount)
				return
			}
			logger.Debugf(ctx, "[loop] done: jobId=%s ok=%d fail=%d", job.ID, completedCount, failedCount)
			return
		}
		logger.Debugf(ctx, "[loop] done: jobId=%s", job.ID)
	}()
	defer s.recoverRunPanic(ctx, job, jobRunSourceLoop, true)

	logger.Debugf(ctx, "[loop] start: jobId=%s nodes=%d", job.ID, len(cfg.Flow))

	s.publishJobStarted(job)

	// Walk a private deep-copy of the flow tree for ALL structure navigation.
	// The live job.LoopConfig.Flow is mutated in place by UpdateRunningStepFields
	// (a mid-run edit) under s.mu; reading it here without the lock — even the
	// implicit struct copy in `for _, node := range nodes` — races on the string
	// fields it rewrites. Structure can't change mid-run (UpdateRunningStepFields
	// rejects that with ErrLoopStructureChanged), so this snapshot stays a faithful
	// map of the tree shape for its whole lifetime. Per-step editable fields
	// (prompt/model/agent/mode) are NOT read off this snapshot — they're re-read
	// live, under the lock, via liveStepFields just before each step runs.
	//
	// Take s.mu while building the snapshot: MigrateLoopConfig may write cfg.Flow
	// and DeepCopyFlowNodes reads every node's string fields, both of which race
	// with UpdateRunningStepFields' locked applyEditableFields write. The persist
	// lock Start/Continue held is already released by the time this goroutine runs.
	s.mu.Lock()
	model.MigrateLoopConfig(cfg)
	flowRoot := model.DeepCopyFlowNodes(cfg.Flow)
	s.mu.Unlock()

	// currentSessionID tracks the session across steps; empty means "need to create one".
	currentSessionID := ""
	if job.Resume != nil {
		currentSessionID = job.Resume.SessionID
	}

	s.injectBuiltinVars(ctx, job)

	// Walk the snapshot (flowRoot) for BOTH the flowRoot and nodes arguments.
	// Passing cfg.Flow (the live job.LoopConfig.Flow) as the traversal slice
	// would defeat the snapshot: the `for _, node := range nodes` struct-copy
	// reads node fields without s.mu while UpdateRunningStepFields rewrites the
	// same string fields under the lock — a data race. Editable fields are still
	// picked up live via liveStepFields under the lock just before each step runs.
	run := newFlowExecution(job, runner, flowRoot, &currentSessionID)
	sr, _, _ := s.runFlowNodes(ctx, run, flowRoot, nil, 0)
	if sr == stepStopWorkflow || sr == stepStopLoop {
		// stepStopWorkflow exits the whole workflow mid-flight; a stepStopLoop that
		// bubbles all the way up to runLoop came from a TOP-LEVEL step (a group
		// consumes its children's stepStopLoop internally via backfillGroupTotal —
		// it never propagates one upward). A top-level STOP_LOOP has no enclosing
		// group to break, so it can only mean "stop the workflow here". Both cases
		// skip every static leaf slot after the stopping step, so nothing else
		// collapses the workflow-level denominator — without this the job finishes
		// Completed but the progress bar stalls below 100%. Pin TotalSteps to what
		// actually ran/failed so the bar finishes full.
		s.backfillWorkflowTotal(job)
		if s.consumeGracefulStop(run.job.ID) {
			logger.Infof(ctx, "[loop] graceful stop request consumed at completed workflow boundary: jobId=%s result=%v", job.ID, sr)
		}
	}
	if sr == stepStopGraceful {
		// Graceful stop: the current step finished cleanly and resume already
		// points at the next step. End the run Stopped (not Completed) with
		// Resume preserved so Continue picks up from the next step. Driving the
		// terminal state here (rather than letting the defer call finishJob)
		// flips Status to Stopped; the defer then sees a non-Running status and
		// returns without finishing. Unlike STOP_WORKFLOW we do NOT backfill the
		// denominator — the remaining steps are "to be continued", not skipped,
		// so the bar should stay partial.
		s.stopJob(ctx, job, true)
	}
}

// backfillWorkflowTotal rewrites TotalSteps down to the leaves that actually
// ran (completed + failed) after a STOP_WORKFLOW, then pushes the recomputed
// denominator to connected clients. Mirrors backfillGroupTotal's display-only
// contract: persisted along with the job (so a reload shows the same full bar)
// but never consulted for resume.
func (s *serviceImpl) backfillWorkflowTotal(job *model.Job) {
	s.mu.Lock()
	if job.Progress == nil {
		s.mu.Unlock()
		return
	}
	ran := job.Progress.CompletedCount + job.Progress.FailedCount
	if job.Progress.TotalSteps <= ran {
		s.mu.Unlock()
		return
	}
	job.Progress.TotalSteps = ran
	newTotal := job.Progress.TotalSteps
	actualMap := copyIntMap(job.Progress.GroupActualIterations)
	leafMap := copyIntMap(job.Progress.GroupActualLeafCounts)
	s.mu.Unlock()

	s.publishProgressTotalUpdated(job.ID, newTotal, actualMap, leafMap)
}

// flowExecution carries the long-lived context for one loop traversal. The
// recursive flow walkers still take the per-call traversal state explicitly
// (nodes/basePath/depth/resume), but shared execution state no longer has to be
// threaded through every function signature.
type flowExecution struct {
	job              *model.Job
	runner           JobRunner
	flowRoot         []model.FlowNode
	currentSessionID *string
}

func newFlowExecution(job *model.Job, runner JobRunner, flowRoot []model.FlowNode, currentSessionID *string) *flowExecution {
	return &flowExecution{
		job:              job,
		runner:           runner,
		flowRoot:         flowRoot,
		currentSessionID: currentSessionID,
	}
}

// runFlowNodes recursively executes the flow node tree (loop mode only).
// Returns stepCompleted if all nodes ran, stepAborted if job was cancelled/failed,
// stepStopLoop if STOP_LOOP was triggered (breaks the parent group's iteration),
// stepStopWorkflow if STOP_WORKFLOW was triggered (exits the entire workflow).
//
// An evaluator step or a Shell STOP_LOOP both return stepStopLoop, which breaks
// the innermost group's iteration. A group that breaks early this way backfills
// its progress denominator and advances resume past itself (§5.2 / §5.4).
//
// executedLeaves counts the leaf-step static slots this call consumed — leaves
// that actually ran this run PLUS leaves skipped because they were already done
// before the resume point (they ran in a prior run). backfilledLeaves counts the
// TotalSteps reduction already applied by nested groups that broke early. The two
// let an outer group that breaks early compute its own denominator reduction
// without double-subtracting a nested group's backfill (see backfillGroupTotal).
func (s *serviceImpl) runFlowNodes(ctx context.Context, run *flowExecution, nodes []model.FlowNode, basePath []int, depth int) (result stepResult, executedLeaves int, backfilledLeaves int) {
	// Defense-in-depth: ValidateFlow enforces MaxFlowDepth at config time,
	// but guard here too in case validation is bypassed or the tree is mutated.
	if depth >= model.MaxFlowDepth {
		err := fmt.Errorf("flow nesting depth %d exceeds maximum %d", depth, model.MaxFlowDepth)
		logger.Errorf(ctx, "[loop] abort: %v, jobId=%s", err, run.job.ID)
		s.failJob(ctx, run.job, err.Error(), true, false)
		return stepAborted, executedLeaves, backfilledLeaves
	}

	resumePath, resumeSessionID := s.getResumeSnapshot(run.job)

	for i, node := range nodes {
		var res stepResult
		var nodeExecuted, nodeBackfilled int
		switch node.Type {
		case model.FlowNodeTypeGroup:
			res, nodeExecuted, nodeBackfilled = s.runGroupNode(ctx, run, node, basePath, i, depth, resumePath)

		case model.FlowNodeTypeStep:
			res, nodeExecuted, nodeBackfilled = s.runStepNode(ctx, run, node, basePath, i, &resumePath, resumeSessionID)
		default:
			err := fmt.Errorf("unknown flow node type %q at path %v", node.Type, appendNodePath(basePath, i))
			logger.Errorf(ctx, "[loop] abort: %v, jobId=%s", err, run.job.ID)
			s.failJob(ctx, run.job, err.Error(), true, false)
			return stepAborted, executedLeaves, backfilledLeaves
		}
		executedLeaves += nodeExecuted
		backfilledLeaves += nodeBackfilled
		if res != stepCompleted {
			return res, executedLeaves, backfilledLeaves
		}
	}
	return stepCompleted, executedLeaves, backfilledLeaves
}

func (s *serviceImpl) runGroupNode(ctx context.Context, run *flowExecution, node model.FlowNode, basePath []int, nodeIdx, depth int, resumePath []int) (stepResult, int, int) {
	ic := node.IterationCount
	if ic < 1 {
		ic = 1
	}
	childSteps := model.CalcTotalSteps(node.Children)
	actualIters := 0
	groupExecuted := 0
	groupBackfilled := 0
	stoppedEarly := false
	for iter := 0; iter < ic; iter++ {
		groupPath := appendPath(basePath, nodeIdx, iter)
		if resumePath != nil && s.isSubtreeBeforeResume(node.Children, groupPath, resumePath) {
			// Iteration already finished in a prior run — its static
			// slots count as consumed so the early-exit denominator math
			// matches the old actualIters semantics across a Continue.
			groupExecuted += childSteps
			continue
		}
		actualIters = iter + 1
		res, childExec, childBackfill := s.runFlowNodes(ctx, run, node.Children, groupPath, depth+1)
		groupExecuted += childExec
		groupBackfilled += childBackfill
		if res == stepAborted || res == stepStopWorkflow || res == stepStopGraceful {
			return res, groupExecuted, groupBackfilled
		}
		if res == stepStopLoop {
			// stepStopLoop (evaluator STOP or Shell STOP_LOOP) breaks
			// only this group's iteration.
			stoppedEarly = true
			break
		}
	}
	// On an early break (stepStopLoop) backfill the progress denominator
	// from the static "cap × children" estimate down to the slots that
	// actually ran — this covers BOTH the future iterations that never
	// started AND the sibling steps skipped after the STOP within the
	// stopping iteration — so the bar finishes full instead of stalling.
	// Also advance resume past this whole group so a Stop+Continue during
	// the post-break window doesn't re-enter a skipped sibling (§5.2/§5.4).
	if stoppedEarly {
		res, updatedBackfilled := s.handleGroupEarlyExit(ctx, run.job, run.flowRoot, node, basePath, nodeIdx, ic, actualIters, groupExecuted, groupBackfilled, *run.currentSessionID)
		groupBackfilled = updatedBackfilled
		if res == stepStopGraceful {
			return res, groupExecuted, groupBackfilled
		}
	}
	return stepCompleted, groupExecuted, groupBackfilled
}

func (s *serviceImpl) runStepNode(ctx context.Context, run *flowExecution, node model.FlowNode, basePath []int, nodeIdx int, resumePath *[]int, resumeSessionID string) (stepResult, int, int) {
	rc := node.RepeatCount
	if rc < 1 {
		rc = 1
	}
	executedLeaves := 0
	for r := 0; r < rc; r++ {
		stepPath := appendPath(basePath, nodeIdx, r)

		// Skip steps before the resume point — they ran in a prior run,
		// so their static slot is already accounted for.
		if shouldSkipStepBeforeResume(stepPath, *resumePath) {
			executedLeaves++
			continue
		}

		if ctx.Err() != nil {
			return stepAborted, executedLeaves, 0
		}

		stepCfg := s.prepareStepRuntimeConfig(run.job, node, stepPath)

		created, failSR := s.ensureStepSession(ctx, run, stepCfg, stepPath, *resumePath, resumeSessionID)
		if !created {
			// Session init failed. tryCreateSession already recorded
			// the failed iteration and called failJob (stepAborted) —
			// propagate to stop the run.
			if failSR == stepAborted {
				return failSR, executedLeaves, 0
			}
			executedLeaves++
			continue
		}

		sessionID := *run.currentSessionID

		// Mark which step is about to run so Continue knows where to
		// resume and can clean up the stale iteration result if the step
		// fails. We no longer snapshot message count — rerunning the
		// step with full history + orphan-tail cleanup in BeginRun is
		// enough.
		s.updateResume(ctx, run.job, &model.JobResume{
			NextPath:  model.CopyPath(stepPath),
			SessionID: sessionID,
		}, "step_start")

		// Once we start executing, clear the resume path
		if *resumePath != nil {
			*resumePath = nil
		}

		postStep := computePostStepResume(run.flowRoot, stepPath, stepCfg.roundMode, *run.currentSessionID)

		// Execute the step
		s.injectPerRoundVars(ctx, run.job, stepPath)
		sr := s.dispatchStep(ctx, run, stepCfg.node, stepPath, sessionID, postStep.nextResume)
		// The leaf ran (success, STOP_LOOP and STOP_WORKFLOW all execute
		// the step before signalling); only stepAborted leaves the slot
		// unconsumed (resumable re-run). Count before returning so a
		// stop signal still credits this leaf in the denominator math.
		if sr != stepAborted {
			executedLeaves++
		}
		if sr != stepCompleted {
			// stepStopLoop / stepStopWorkflow: the step DID execute and
			// recorded its result; we just skip the post-step session
			// reset and return so the group-early-exit logic in the
			// caller can run. Intentionally leaving *currentSessionID at
			// the stopping step's session means a following roundMode=none
			// node continues in that session — the same reuse the
			// cap-reached path produces for roundMode=none, and the
			// reuse-after-stop behaviour we want. (A beforeRound/eachRepeat
			// successor spawns its own session regardless, overwriting it.)
			return sr, executedLeaves, 0
		}

		if postStep.resetSessionAfterStep {
			*run.currentSessionID = ""
		}

		if sr := s.handlePostStepBoundary(ctx, run.job, stepPath, postStep); sr != stepCompleted {
			return sr, executedLeaves, 0
		}

	}
	return stepCompleted, executedLeaves, 0
}

func (s *serviceImpl) handlePostStepBoundary(ctx context.Context, job *model.Job, stepPath []int, postStep postStepResume) stepResult {
	// Graceful stop boundary: the step just completed cleanly — its result is
	// recorded and resume already points at the next step
	// (recordIterationAndAdvanceResume). If a graceful stop was requested, stop
	// here instead of starting the next step. Resume is preserved, so Continue
	// resumes from the next step with no re-run (unlike the hard Stop, which
	// cancels mid-step). Bubble stepStopGraceful up to runLoop, which drives the
	// Stopped terminal state.
	//
	// Always consume the request at a clean step boundary. Only honour it as a
	// Stopped terminal state when there IS a next step (nextPath != nil). If this
	// was the last step the whole flow is finished and there is nothing to resume
	// into — bubbling stepStopGraceful would mark a completed run as Stopped with
	// no resumable cursor. Consume and fall through to natural completion instead.
	if !s.consumeGracefulStop(job.ID) {
		return stepCompleted
	}
	if postStep.nextPath != nil {
		logger.Infof(ctx, "[loop] graceful stop at step boundary: jobId=%s path=%v", job.ID, stepPath)
		return stepStopGraceful
	}
	logger.Infof(ctx, "[loop] graceful stop request consumed at completed tail step: jobId=%s path=%v", job.ID, stepPath)
	return stepCompleted
}

type stepRuntimeConfig struct {
	node      model.FlowNode
	roundMode model.RoundMode
	overrides *model.SessionOverrides
}

func (s *serviceImpl) prepareStepRuntimeConfig(job *model.Job, node model.FlowNode, stepPath []int) stepRuntimeConfig {
	// Re-read the editable fields from the live LoopConfig so a
	// mid-run edit (UpdateRunningStepFields) reaches this not-yet-
	// started step. runFlowNodes walks a by-value flow snapshot, so
	// without this the running loop would never see the new prompt /
	// model / agent / mode. Structure fields stay on the snapshot
	// node (running edits can't change structure). Both
	// tryCreateSession (overrides) and executeRepeat (node.Message)
	// read these off node below, so updating node covers both.
	if msg, at, mid, mode, ok := s.liveStepFields(job, stepPath); ok {
		node.Message = msg
		node.AgentType = at
		node.StepModelID = mid
		node.ACPMode = mode
	}

	roundMode := node.RoundMode
	if roundMode == "" {
		roundMode = model.RoundModeNone
	}

	return stepRuntimeConfig{
		node:      node,
		roundMode: roundMode,
		overrides: &model.SessionOverrides{
			AgentType: node.AgentType,
			ModelID:   node.StepModelID,
			ACPMode:   node.ACPMode,
		},
	}
}

func (s *serviceImpl) ensureStepSession(ctx context.Context, run *flowExecution, stepCfg stepRuntimeConfig, stepPath, resumePath []int, resumeSessionID string) (bool, stepResult) {
	resumingStep := resumePath != nil && model.EqualPaths(stepPath, resumePath)
	reason := stepSessionReason(stepCfg.roundMode, resumingStep, resumeSessionID, *run.currentSessionID)
	if reason == "" {
		return true, stepCompleted
	}
	return s.tryCreateSession(ctx, run.job, run.runner, stepCfg.node, stepPath, run.currentSessionID, reason, stepCfg.overrides)
}

type postStepResume struct {
	nextPath              []int
	nextResume            *model.JobResume
	resetSessionAfterStep bool
}

func computePostStepResume(flowRoot []model.FlowNode, stepPath []int, roundMode model.RoundMode, currentSessionID string) postStepResume {
	// Pre-compute the post-step resume pointer (and whether the
	// next step should reuse the current session) so the step
	// executor can persist iteration result + advance_resume in a
	// single save on success. roundMode/eachRepeat and a fresh
	// session for the next step both reset the session pointer —
	// commit those side-effects only after the step actually
	// completes successfully so a failure leaves the in-memory
	// pointer alone (and Continue can reuse it).
	nextPath := model.NextStepPath(flowRoot, stepPath)
	resetSessionAfterStep := roundMode == model.RoundModeEachRepeat || nextStepStartsFreshSession(flowRoot, nextPath)
	nextSessionID := currentSessionID
	if resetSessionAfterStep {
		nextSessionID = ""
	}
	var nextResume *model.JobResume
	if nextPath != nil {
		nextResume = &model.JobResume{NextPath: nextPath, SessionID: nextSessionID}
	}
	return postStepResume{nextPath: nextPath, nextResume: nextResume, resetSessionAfterStep: resetSessionAfterStep}
}

func (s *serviceImpl) dispatchStep(ctx context.Context, run *flowExecution, node model.FlowNode, stepPath []int, sessionID string, nextResume *model.JobResume) stepResult {
	switch node.RoundType {
	case model.RoundTypeShell:
		return s.executeShellRepeat(ctx, run.job, run.runner, node, stepPath, sessionID, nextResume)
	case model.RoundTypeEvaluator:
		return s.executeRepeat(ctx, run.job, run.runner, node, stepPath, sessionID, nil, true, nextResume)
	case "", model.RoundTypePrompt:
		return s.executeRepeat(ctx, run.job, run.runner, node, stepPath, sessionID, nil, true, nextResume)
	default:
		logger.Errorf(ctx, "[loop] unknown RoundType %q, failing job: jobId=%s path=%v", node.RoundType, run.job.ID, stepPath)
		s.failJob(ctx, run.job, fmt.Sprintf("unsupported step type: %s", node.RoundType), true, false)
		return stepAborted
	}
}

// shouldSkipStepBeforeResume reports whether stepPath was already completed in
// a previous run and should only be credited as consumed during Continue.
func shouldSkipStepBeforeResume(stepPath, resumePath []int) bool {
	return resumePath != nil && model.ComparePaths(stepPath, resumePath) < 0
}

func (s *serviceImpl) handleGroupEarlyExit(ctx context.Context, job *model.Job, flowRoot []model.FlowNode, node model.FlowNode, basePath []int, nodeIdx, iterationCap, actualIters, groupExecuted, groupBackfilled int, stopSessionID string) (stepResult, int) {
	delta := s.backfillGroupTotal(job, appendNodePath(basePath, nodeIdx), iterationCap, model.CalcTotalSteps(node.Children), actualIters, groupExecuted, groupBackfilled)
	groupBackfilled += delta
	hasNext := s.advanceResumePastGroup(ctx, job, flowRoot, node, basePath, nodeIdx, iterationCap, stopSessionID)
	// A step inside this group finished cleanly and then signalled STOP_LOOP
	// (evaluator STOP / shell STOP_LOOP), so group-level early-exit handling has
	// already persisted the resume target past the whole group. If a graceful stop
	// was requested while that step was in flight, consume it here before starting
	// any outer sibling / iteration so the run still honours the "stop after the
	// current step" boundary even on the stepStopLoop path.
	//
	// Consume the request at this boundary even when the group is the tail of the
	// flow. If there is no next step, the right terminal state is still Completed,
	// but leaving the pending flag behind would make the in-memory graceful-stop
	// state stale for this job.
	if s.consumeGracefulStop(job.ID) {
		if hasNext {
			return stepStopGraceful, groupBackfilled
		}
		logger.Infof(ctx, "[loop] graceful stop request consumed at completed tail group: jobId=%s path=%v", job.ID, appendNodePath(basePath, nodeIdx))
	}
	return stepCompleted, groupBackfilled
}

// tryCreateSession attempts to create and attach a new session.
//
// Returns (true, stepCompleted) on success. On failure it emits a paired
// ITERATION_STARTED + ITERATION_FAILED so the SSE round opens and closes
// cleanly (per §1.1 "all entry points open a round with ITERATION_STARTED")
// and records the iteration as failed. The failure then mirrors a regular
// step's execution-failure handling (executeRepeat): call failJob (preserving
// resume so the user can Continue) and return (false, stepAborted) so the
// caller stops the run.
//
// Previously this unconditionally advanced resume and returned false, which the
// caller treated as a plain "continue" — silently swallowing the failure and
// letting the job finish as Completed despite the error.
func (s *serviceImpl) tryCreateSession(
	ctx context.Context, job *model.Job, runner JobRunner,
	node model.FlowNode, stepPath []int, currentSessionID *string, source string,
	overrides *model.SessionOverrides,
) (created bool, failResult stepResult) {
	sid, err := s.initAndAttachSession(ctx, job, runner, overrides)
	if err == nil {
		*currentSessionID = sid
		return true, stepCompleted
	}

	logger.Errorf(ctx, "[loop] init session failed: source=%s jobId=%s path=%v err=%v",
		source, job.ID, stepPath, err)

	// Open the round before closing it. Without this pair the SSE stream
	// would carry an orphan ITERATION_FAILED with no matching
	// ITERATION_STARTED, violating §1.1 round-boundary contract and
	// confusing UI state machines that key on iteration open/close.
	iterationStartedAt := s.persistIterationStart(ctx, job, stepPath)
	s.Publish(job.ID, &model.IterationStartedEvent{
		BaseEvent: model.BaseEvent{
			Type: model.EventTypeIterationStarted, JobID: job.ID,
			Path:      stepPath,
			Timestamp: iterationStartedAt,
		},
		Message:   node.Message,
		ModelID:   node.StepModelID,
		AgentType: node.AgentType,
		ACPMode:   node.ACPMode,
	})

	// Hard failure: record the failed iteration, then failJob. failJob issues
	// the terminal persist (preserving resume so the user can Continue from this
	// step), so a plain record here keeps the failure visible without a
	// redundant resume advance.
	s.recordIterationResult(ctx, job, &model.IterationResult{
		Path:      model.CopyPath(stepPath),
		SessionID: "",
		Success:   false,
		Error:     err.Error(),
	})
	s.failJob(ctx, job, err.Error(), true, true)
	return false, stepAborted
}

// stepSessionReason decides whether (and why) the step about to run needs a
// fresh session. Returns "" when the current session can be reused. Loop mode
// only (interactive runs go through runInteractive, not runFlowNodes).
//
// Decision order:
//  1. RoundModeBeforeRound — spawn a fresh session at the top of every step,
//     UNLESS this exact step is being resumed mid-run (Stop → Continue): in
//     that case reuse the session captured in job.Resume so the resumed step
//     continues against its original context instead of restarting in a brand
//     new session. Shares the same resume guard as RoundModeEachRepeat below.
//  2. RoundModeEachRepeat — spawn a fresh session unless this exact step is
//     being resumed mid-run. The resume guard keys off the snapshot
//     resumeSessionID (captured from job.Resume.SessionID at the top of the
//     recursive runFlowNodes call), NOT currentSessionID: the in-memory
//     pointer lingers from the previous step's execution and would otherwise
//     make an outer group's second iteration look like a resume, silently
//     reusing the prior session instead of spawning a new one.
//  3. Fallback — anything else: spawn a session only when none exists.
func stepSessionReason(roundMode model.RoundMode, resumingStep bool, resumeSessionID, currentSessionID string) string {
	resumingThisStep := resumingStep && resumeSessionID != ""
	switch roundMode {
	case model.RoundModeBeforeRound:
		if !resumingThisStep {
			return "beforeRound"
		}
	case model.RoundModeEachRepeat:
		if !resumingThisStep {
			return "eachRepeat"
		}
	}
	if currentSessionID == "" {
		return "fallback"
	}
	return ""
}

// getResumeSnapshot returns the resume path and the session ID associated
// with it if there is an active resume. Callers snapshot both values together
// so that downstream logic can distinguish "this step is being resumed from
// a pause" (sessionID non-empty) from "the previous step's advance_resume
// simply points here" (sessionID empty).
func (s *serviceImpl) getResumeSnapshot(job *model.Job) ([]int, string) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if job.Resume == nil || len(job.Resume.NextPath) == 0 {
		return nil, ""
	}
	return model.CopyPath(job.Resume.NextPath), job.Resume.SessionID
}

// isSubtreeBeforeResume checks if all leaf steps in a subtree are before the resume path.
func (s *serviceImpl) isSubtreeBeforeResume(nodes []model.FlowNode, basePath []int, resumePath []int) bool {
	var lastPath []int
	enumLastPath(nodes, basePath, &lastPath)
	if lastPath == nil {
		return true
	}
	return model.ComparePaths(lastPath, resumePath) < 0
}

// enumLastPath finds the last leaf step path in the tree.
func enumLastPath(nodes []model.FlowNode, basePath []int, last *[]int) {
	for i := len(nodes) - 1; i >= 0; i-- {
		n := nodes[i]
		switch n.Type {
		case model.FlowNodeTypeStep:
			rc := n.RepeatCount
			if rc < 1 {
				rc = 1
			}
			*last = appendPath(basePath, i, rc-1)
			return
		case model.FlowNodeTypeGroup:
			ic := n.IterationCount
			if ic < 1 {
				ic = 1
			}
			groupPath := appendPath(basePath, i, ic-1)
			enumLastPath(n.Children, groupPath, last)
			if *last != nil {
				return
			}
		}
	}
}

// lastChildStepPath returns the path of the last leaf step under children
// (rooted at basePath). Used to anchor NextStepPath when advancing resume past
// a group that broke early. Falls back to basePath when there is no leaf step.
func lastChildStepPath(children []model.FlowNode, basePath []int) []int {
	var last []int
	enumLastPath(children, basePath, &last)
	if last == nil {
		return model.CopyPath(basePath)
	}
	return last
}

// backfillGroupTotal rewrites the in-memory TotalSteps contribution of a group
// that broke early (stepStopLoop) so the progress bar finishes full (§5.4).
//
// The group statically contributed cap×childSteps to TotalSteps. Its real
// contribution is groupExecuted — the leaf slots that actually ran (or were
// credited as already-done before the resume point). That difference covers
// BOTH the iterations that never started AND the sibling steps skipped after a
// STOP within the stopping iteration. groupBackfilled is the reduction nested
// child groups already applied to TotalSteps; subtracting it here avoids
// double-counting a nested group's own early-exit backfill.
//
// Returns the delta actually subtracted so the caller can fold it into its own
// groupBackfilled total. This mutates job.Progress.TotalSteps plus the
// GroupActualIterations / GroupActualLeafCounts display maps in memory. These
// fields ARE persisted with the job (the advanceResumePastGroup save right
// after, and the terminal finish save), so a reload / reconnect shows the same
// full bar. They are a display aid only and are never consulted for resume —
// resume is driven solely by job.Resume.
func (s *serviceImpl) backfillGroupTotal(job *model.Job, groupPath []int, cap, childSteps, actualIters, groupExecuted, groupBackfilled int) int {
	if childSteps == 0 {
		return 0
	}
	delta := (cap*childSteps - groupExecuted) - groupBackfilled
	if delta <= 0 {
		return 0
	}
	pathKey := model.StepPathKey(groupPath)
	s.mu.Lock()
	var newTotal int
	var actualMap map[string]int
	var leafMap map[string]int
	if job.Progress != nil {
		job.Progress.TotalSteps -= delta
		if job.Progress.TotalSteps < 0 {
			job.Progress.TotalSteps = 0
		}
		if job.Progress.GroupActualIterations == nil {
			job.Progress.GroupActualIterations = make(map[string]int)
		}
		job.Progress.GroupActualIterations[pathKey] = actualIters
		if job.Progress.GroupActualLeafCounts == nil {
			job.Progress.GroupActualLeafCounts = make(map[string]int)
		}
		job.Progress.GroupActualLeafCounts[pathKey] = groupExecuted
		newTotal = job.Progress.TotalSteps
		actualMap = copyIntMap(job.Progress.GroupActualIterations)
		leafMap = copyIntMap(job.Progress.GroupActualLeafCounts)
	}
	s.mu.Unlock()

	// Push the recomputed denominator so connected clients update the
	// progress text / bar live (mirrors the in-memory backfill). The
	// frontend recomputes its session plan with the actual iteration counts.
	s.publishProgressTotalUpdated(job.ID, newTotal, actualMap, leafMap)
	return delta
}

func (s *serviceImpl) publishProgressTotalUpdated(jobID string, totalSteps int, actualMap, leafMap map[string]int) {
	s.Publish(jobID, &model.CustomEvent{
		BaseEvent: model.BaseEvent{
			Type:      model.EventTypeCustom,
			JobID:     jobID,
			Timestamp: s.nowMillis(),
		},
		Name: "progress_total_updated",
		Value: map[string]any{
			"totalSteps":            totalSteps,
			"groupActualIterations": actualMap,
			"groupActualLeafCounts": leafMap,
		},
	})
}

// appendNodePath returns a new path with a single node index appended (no
// iteration component) — identifies a group node position regardless of which
// iteration it is on.
func appendNodePath(base []int, nodeIdx int) []int {
	p := make([]int, len(base)+1)
	copy(p, base)
	p[len(base)] = nodeIdx
	return p
}

func copyIntMap(m map[string]int) map[string]int {
	out := make(map[string]int, len(m))
	for k, v := range m {
		out[k] = v
	}
	return out
}

// advanceResumePastGroup moves job.Resume to the first leaf step AFTER a group
// that broke early via stepStopLoop (§5.2). Anchoring NextStepPath on the
// group's cap-th iteration last step makes it skip every round of the group, so
// Continue resumes at the following sibling (or clears resume when the group is
// the last node). Without this, the last round's precomputed nextResume would
// point back into the group and re-run an already-finished loop on Continue.
//
// stopSessionID is the session the stopping step left in *currentSessionID.
// runFlowNodes intentionally keeps that session live so a roundMode=none
// sibling after the group continues in it (the reuse-after-stop behaviour). The
// persisted Resume must carry the same session, otherwise a Stop+Continue in
// the post-break window would resume the sibling with an empty SessionID and
// fall back to spawning a fresh session — losing the stopping step's context.
// A successor that spawns its own session (beforeRound/eachRepeat) overwrites it
// anyway, so carrying the session is safe in every case.
func (s *serviceImpl) advanceResumePastGroup(ctx context.Context, job *model.Job, flowRoot []model.FlowNode, node model.FlowNode, basePath []int, nodeIdx, cap int, stopSessionID string) (hasNext bool) {
	capGroupPath := appendPath(basePath, nodeIdx, cap-1)
	anchor := lastChildStepPath(node.Children, capGroupPath)
	nextPath := model.NextStepPath(flowRoot, anchor)

	s.mu.Lock()
	if nextPath == nil {
		job.Resume = nil
	} else {
		sessionID := stopSessionID
		if nextStepStartsFreshSession(flowRoot, nextPath) {
			// The next step spawns its own session; don't carry one that would
			// make its resume guard mistake the advance for a mid-run resume.
			sessionID = ""
		}
		job.Resume = &model.JobResume{NextPath: model.CopyPath(nextPath), SessionID: sessionID}
	}
	s.mu.Unlock()
	if err := s.saveJobWithRetry(ctx, job, jobPersistActionGroupEarlyExit); err != nil {
		s.recordPersistWarning(ctx, job, jobPersistActionGroupEarlyExit, err)
	}
	return nextPath != nil
}

// nextStepStartsFreshSession reports whether the step at path will spawn its
// own session at the top of its run (RoundModeEachRepeat or
// RoundModeBeforeRound). The caller uses this to drop the carried SessionID
// from a successful step's precomputed nextResume: if the next step makes its
// own session, the advance pointer must not look like a mid-run resume of an
// existing session (which would wrongly make the resume guard in
// stepSessionReason reuse it on an outer group's later iteration).
func nextStepStartsFreshSession(nodes []model.FlowNode, path []int) bool {
	roundMode, ok := roundModeForStepPath(nodes, path)
	return ok && (roundMode == model.RoundModeEachRepeat || roundMode == model.RoundModeBeforeRound)
}

func roundModeForStepPath(nodes []model.FlowNode, path []int) (model.RoundMode, bool) {
	node, ok := stepNodeForPath(nodes, path)
	if !ok {
		return "", false
	}
	if node.RoundMode == "" {
		return model.RoundModeNone, true
	}
	return node.RoundMode, true
}

// stepNodeForPath walks the flow tree along path and returns the leaf step node
// it points at. path alternates [nodeIdx, subIdx, nodeIdx, subIdx, …]; the
// trailing pair must land on a step node. Returns ok=false for malformed paths
// or paths that resolve to a group. The returned FlowNode is a copy of the live
// tree node, so callers reading it under s.mu see the latest edited fields.
func stepNodeForPath(nodes []model.FlowNode, path []int) (model.FlowNode, bool) {
	if len(path) == 0 || len(path)%2 != 0 {
		return model.FlowNode{}, false
	}
	current := nodes
	for i := 0; i < len(path); i += 2 {
		nodeIdx := path[i]
		if nodeIdx < 0 || nodeIdx >= len(current) {
			return model.FlowNode{}, false
		}
		node := current[nodeIdx]
		if i == len(path)-2 {
			if node.Type != model.FlowNodeTypeStep {
				return model.FlowNode{}, false
			}
			return node, true
		}
		if node.Type != model.FlowNodeTypeGroup {
			return model.FlowNode{}, false
		}
		current = node.Children
	}
	return model.FlowNode{}, false
}

// liveStepFields reads the per-step editable fields (message, agentType,
// modelId, acpMode) for the step at stepPath from the job's live LoopConfig,
// under s.mu. This is what makes a mid-run edit (UpdateRunningStepFields) take
// effect: runFlowNodes captured the flow as a by-value snapshot, so without
// re-reading here a running edit would never reach the next step. Only the
// editable fields are returned — structure fields stay on the snapshot node
// (running edits cannot change structure, so they always agree). Returns
// ok=false when the path can't be resolved (then the caller keeps the snapshot).
func (s *serviceImpl) liveStepFields(job *model.Job, stepPath []int) (message, agentType, modelID, acpMode string, ok bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if job.LoopConfig == nil {
		return "", "", "", "", false
	}
	node, found := stepNodeForPath(job.LoopConfig.Flow, stepPath)
	if !found {
		return "", "", "", "", false
	}
	return node.Message, node.AgentType, node.StepModelID, node.ACPMode, true
}

// appendPath creates a new path by appending two indices to basePath.
func appendPath(base []int, nodeIdx, subIdx int) []int {
	p := make([]int, len(base)+2)
	copy(p, base)
	p[len(base)] = nodeIdx
	p[len(base)+1] = subIdx
	return p
}

func (s *serviceImpl) publishJobStarted(job *model.Job) {
	s.Publish(job.ID, &model.JobStartedEvent{
		BaseEvent:  model.BaseEvent{Type: model.EventTypeJobStarted, JobID: job.ID, Timestamp: job.StartedAt},
		TotalSteps: job.Progress.TotalSteps,
	})
}

func (s *serviceImpl) initAndAttachSession(ctx context.Context, job *model.Job, runner JobRunner, overrides *model.SessionOverrides) (string, error) {
	sid, err := runner.InitSession(ctx, job.ID, overrides)
	if err != nil {
		return "", err
	}
	s.mu.Lock()
	firstSession := len(job.SessionIDs) == 0
	job.SessionIDs = append(job.SessionIDs, sid)
	if firstSession && job.FirstModelID == "" && overrides != nil && overrides.ModelID != "" {
		job.FirstModelID = overrides.ModelID
	}
	s.mu.Unlock()
	if err := s.saveJobWithRetry(ctx, job, jobPersistActionAttachSession); err != nil {
		s.recordPersistWarning(ctx, job, jobPersistActionAttachSession, err)
	}
	return sid, nil
}
