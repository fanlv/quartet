package job

import (
	"context"
	"fmt"
	"runtime/debug"

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
		if r := recover(); r != nil {
			panicErr := fmt.Errorf("job panicked: %v", r)
			logger.Errorf(ctx, "[loop] panic: jobId=%s err=%v\n%s", job.ID, r, string(debug.Stack()))
			// Close any in-flight buffer round before the terminal event,
			// so Continue's ResumeGC can reclaim its A-class chunks. See
			// closePanicRoundIfOpen for the leak this guards against.
			s.closePanicRoundIfOpen(job, panicErr)
			s.failJob(ctx, job, panicErr.Error(), true, false)
			return
		}

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

	// Ensure Flow is populated
	model.MigrateLoopConfig(cfg)
	logger.Debugf(ctx, "[loop] start: jobId=%s nodes=%d", job.ID, len(cfg.Flow))

	s.publishJobStarted(job)

	// currentSessionID tracks the session across steps; empty means "need to create one".
	currentSessionID := ""
	if job.Resume != nil {
		currentSessionID = job.Resume.SessionID
	}

	s.injectBuiltinVars(ctx, job)

	s.runFlowNodes(ctx, job, runner, cfg.Flow, nil, 0, &currentSessionID)
}

// runFlowNodes recursively executes the flow node tree (loop mode only).
// Returns stepCompleted if all nodes ran, stepAborted if job was cancelled/failed,
// stepStopLoop if STOP_LOOP was triggered (breaks the parent group's iteration),
// stepStopWorkflow if STOP_WORKFLOW was triggered (exits the entire workflow).
func (s *serviceImpl) runFlowNodes(
	ctx context.Context, job *model.Job, runner JobRunner,
	nodes []model.FlowNode, basePath []int, depth int,
	currentSessionID *string,
) stepResult {
	// Defense-in-depth: ValidateFlow enforces MaxFlowDepth at config time,
	// but guard here too in case validation is bypassed or the tree is mutated.
	if depth >= model.MaxFlowDepth {
		logger.Errorf(ctx, "[loop] abort: depth %d exceeds max %d, jobId=%s", depth, model.MaxFlowDepth, job.ID)
		return stepAborted
	}

	resumePath, resumeSessionID := s.getResumeSnapshot(job)

	for i, node := range nodes {
		switch node.Type {
		case model.FlowNodeTypeGroup:
			ic := node.IterationCount
			if ic < 1 {
				ic = 1
			}
			for iter := 0; iter < ic; iter++ {
				groupPath := appendPath(basePath, i, iter)
				if resumePath != nil && s.isSubtreeBeforeResume(node.Children, groupPath, resumePath) {
					continue
				}
				result := s.runFlowNodes(ctx, job, runner, node.Children, groupPath, depth+1, currentSessionID)
				if result == stepAborted || result == stepStopWorkflow {
					return result
				}
				if result == stepStopLoop {
					// STOP_LOOP breaks only this group's iteration — don't propagate further
					break
				}
			}

		case model.FlowNodeTypeStep:
			rc := node.RepeatCount
			if rc < 1 {
				rc = 1
			}
			for r := 0; r < rc; r++ {
				stepPath := appendPath(basePath, i, r)

				// Skip steps before the resume point
				if resumePath != nil && model.ComparePaths(stepPath, resumePath) < 0 {
					continue
				}

				if ctx.Err() != nil {
					return stepAborted
				}

				roundMode := node.RoundMode
				if roundMode == "" {
					roundMode = model.RoundModeNone
				}

				// Build per-step overrides from FlowNode configuration.
				stepOverrides := &model.SessionOverrides{
					AgentType: node.AgentType,
					ModelID:   node.StepModelID,
					ACPMode:   node.ACPMode,
				}

				resumingStep := resumePath != nil && model.EqualPaths(stepPath, resumePath)

				if reason := stepSessionReason(roundMode, resumingStep, resumeSessionID, *currentSessionID); reason != "" {
					if !s.tryCreateSession(ctx, job, runner, node, stepPath, currentSessionID, reason, stepOverrides) {
						continue
					}
				}

				sessionID := *currentSessionID

				// Mark which step is about to run so Continue knows where to
				// resume and can clean up the stale iteration result if the step
				// fails. We no longer snapshot message count — rerunning the
				// step with full history + orphan-tail cleanup in BeginRun is
				// enough.
				s.updateResume(ctx, job, &model.JobResume{
					NextPath:  model.CopyPath(stepPath),
					SessionID: sessionID,
				}, "step_start")

				// Once we start executing, clear the resume path
				if resumePath != nil {
					resumePath = nil
				}

				// Pre-compute the post-step resume pointer (and whether the
				// next step should reuse the current session) so the step
				// executor can persist iteration result + advance_resume in a
				// single save on success. roundMode/eachRepeat and a fresh
				// session for the next step both reset the session pointer —
				// commit those side-effects only after the step actually
				// completes successfully so a failure leaves the in-memory
				// pointer alone (and Continue can reuse it).
				nextPath := model.NextStepPath(job.LoopConfig.Flow, stepPath)
				resetSessionAfterStep := roundMode == model.RoundModeEachRepeat || nextStepStartsFreshSession(job.LoopConfig.Flow, nextPath)
				nextSessionID := *currentSessionID
				if resetSessionAfterStep {
					nextSessionID = ""
				}
				var nextResume *model.JobResume
				if nextPath != nil {
					nextResume = &model.JobResume{NextPath: nextPath, SessionID: nextSessionID}
				}

				// Execute the step
				var sr stepResult
				s.injectPerRoundVars(ctx, job, stepPath)
				switch node.RoundType {
				case model.RoundTypeShell:
					sr = s.executeShellRepeat(ctx, job, runner, node, stepPath, sessionID, nextResume)
				case "", model.RoundTypePrompt:
					sr = s.executeRepeat(ctx, job, runner, node, stepPath, sessionID, nil, true, nextResume)
				default:
					logger.Errorf(ctx, "[loop] unknown RoundType %q, failing job: jobId=%s path=%v", node.RoundType, job.ID, stepPath)
					s.failJob(ctx, job, fmt.Sprintf("unsupported step type: %s", node.RoundType), true, false)
					return stepAborted
				}
				if sr != stepCompleted {
					return sr
				}

				if resetSessionAfterStep {
					*currentSessionID = ""
				}

			}
		}
	}
	return stepCompleted
}

// tryCreateSession attempts to create and attach a new session. On failure it
// emits a paired ITERATION_STARTED + ITERATION_FAILED so the SSE round opens
// and closes cleanly (per §1.1 "all entry points open a round with
// ITERATION_STARTED"), records the iteration as failed, AND advances the
// resume pointer in a single save, then returns false so the caller can skip
// (continue) the current step.
func (s *serviceImpl) tryCreateSession(
	ctx context.Context, job *model.Job, runner JobRunner,
	node model.FlowNode, stepPath []int, currentSessionID *string, source string,
	overrides *model.SessionOverrides,
) bool {
	sid, err := s.initAndAttachSession(ctx, job, runner, overrides)
	if err != nil {
		logger.Errorf(ctx, "[loop] init session failed: source=%s jobId=%s path=%v err=%v", source, job.ID, stepPath, err)
		nextPath := model.NextStepPath(job.LoopConfig.Flow, stepPath)
		var nextResume *model.JobResume
		if nextPath != nil {
			nextResume = &model.JobResume{NextPath: nextPath}
		}
		// Open the round before closing it. Without this pair the SSE stream
		// would carry an orphan ITERATION_FAILED with no matching
		// ITERATION_STARTED, violating §1.1 round-boundary contract and
		// confusing UI state machines that key on iteration open/close.
		s.persistIterationStart(ctx, job, stepPath)
		s.Publish(job.ID, &model.IterationStartedEvent{
			BaseEvent: model.BaseEvent{
				Type: model.EventTypeIterationStarted, JobID: job.ID,
				Path:      stepPath,
				Timestamp: nowMillis(),
			},
			Message:   node.Message,
			ModelID:   node.StepModelID,
			AgentType: node.AgentType,
			ACPMode:   node.ACPMode,
		})
		s.recordFailedIterationAndAdvanceResume(job, stepPath, "", err, nextResume)
		return false
	}
	*currentSessionID = sid
	return true
}

// stepSessionReason decides whether (and why) the step about to run needs a
// fresh session. Returns "" when the current session can be reused. Loop mode
// only (interactive runs go through runInteractive, not runFlowNodes).
//
// Decision order:
//  1. RoundModeBeforeRound — always spawn a fresh session at the top of every
//     step.
//  2. RoundModeEachRepeat — spawn a fresh session unless this exact step is
//     being resumed mid-run. The resume guard keys off the snapshot
//     resumeSessionID (captured from job.Resume.SessionID at the top of the
//     recursive runFlowNodes call), NOT currentSessionID: the in-memory
//     pointer lingers from the previous step's execution and would otherwise
//     make an outer group's second iteration look like a resume, silently
//     reusing the prior session instead of spawning a new one.
//  3. Fallback — anything else: spawn a session only when none exists.
func stepSessionReason(roundMode model.RoundMode, resumingStep bool, resumeSessionID, currentSessionID string) string {
	switch roundMode {
	case model.RoundModeBeforeRound:
		return "beforeRound"
	case model.RoundModeEachRepeat:
		if !(resumingStep && resumeSessionID != "") {
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

func nextStepStartsFreshSession(nodes []model.FlowNode, path []int) bool {
	roundMode, ok := roundModeForStepPath(nodes, path)
	return ok && roundMode == model.RoundModeEachRepeat
}

func roundModeForStepPath(nodes []model.FlowNode, path []int) (model.RoundMode, bool) {
	if len(path) == 0 || len(path)%2 != 0 {
		return "", false
	}
	current := nodes
	for i := 0; i < len(path); i += 2 {
		nodeIdx := path[i]
		if nodeIdx < 0 || nodeIdx >= len(current) {
			return "", false
		}
		node := current[nodeIdx]
		if i == len(path)-2 {
			if node.Type != model.FlowNodeTypeStep {
				return "", false
			}
			if node.RoundMode == "" {
				return model.RoundModeNone, true
			}
			return node.RoundMode, true
		}
		if node.Type != model.FlowNodeTypeGroup {
			return "", false
		}
		current = node.Children
	}
	return "", false
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
	if err := s.saveJobWithRetry(ctx, job, "attach_session"); err != nil {
		s.recordPersistWarning(ctx, job, "attach_session", err)
	}
	return sid, nil
}
