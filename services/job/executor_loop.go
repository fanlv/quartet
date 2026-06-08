package job

import (
	"context"
	"fmt"
	"runtime/debug"
	"strings"

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

	s.runFlowNodes(ctx, job, runner, cfg.Flow, nil, 0, &currentSessionID, false)
}

// runFlowNodes recursively executes the flow node tree (loop mode only).
// Returns stepCompleted if all nodes ran, stepAborted if job was cancelled/failed,
// stepStopLoop if STOP_LOOP was triggered (breaks the parent group's iteration),
// stepStopWorkflow if STOP_WORKFLOW was triggered (exits the entire workflow).
//
// inConditional is true when this subtree executes inside a conditional group
// (one with a non-empty CompletionCondition). In that context business-step
// failures are recorded and the round keeps running (so the judge turn can see
// the failure in history) instead of failing the job — see §2.4.
func (s *serviceImpl) runFlowNodes(
	ctx context.Context, job *model.Job, runner JobRunner,
	nodes []model.FlowNode, basePath []int, depth int,
	currentSessionID *string, inConditional bool,
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
			conditional := node.CompletionCondition != ""
			// Inside a conditional group the children run with inConditional=true
			// so business failures don't fail the job. A nested fixed group keeps
			// its own children non-conditional unless it too is conditional —
			// inConditional is inherited (a child subtree of a conditional group
			// stays conditional) OR set by this group being conditional.
			childInConditional := inConditional || conditional
			actualIters := 0
			for iter := 0; iter < ic; iter++ {
				groupPath := appendPath(basePath, i, iter)
				if resumePath != nil && s.isSubtreeBeforeResume(node.Children, groupPath, resumePath) {
					continue
				}
				actualIters = iter + 1
				result := s.runFlowNodes(ctx, job, runner, node.Children, groupPath, depth+1, currentSessionID, childInConditional)
				if result == stepAborted || result == stepStopWorkflow {
					return result
				}
				if result == stepStopLoop {
					// STOP_LOOP (Shell control) breaks only this group's
					// iteration and takes priority over the judge — don't
					// propagate further and don't run the judge this round.
					break
				}

				// Conditional groups run a judge turn after each round's
				// children complete (and only when the round wasn't cut short
				// by a Shell STOP, handled above). STOP_WORKFLOW > STOP_LOOP >
				// judge (§4).
				if conditional {
					judgePath := lastChildStepPath(node.Children, groupPath)
					stop, hardErr := s.runJudgeTurn(ctx, job, runner, node, groupPath, judgePath, iter+1, ic, *currentSessionID)
					if hardErr != nil {
						if isInterruptedRun(hardErr) {
							return stepAborted
						}
						s.failJob(ctx, job, hardErr.Error(), true, true)
						return stepAborted
					}
					if stop {
						break
					}
				}
			}
			// On exit (STOP, judge-STOP, or cap reached) backfill the progress
			// denominator for a conditional group from "cap × children" to
			// "actual rounds × children" so the bar finishes full instead of
			// stalling at e.g. 3/10. In-memory only (§5.4) — not persisted.
			if conditional && actualIters < ic {
				s.backfillConditionalTotal(job, node, appendNodePath(basePath, i), ic, actualIters)
				// §5.2: a conditional group that STOPs early left job.Resume
				// pointing into the NEXT round (the last business step
				// precomputed its nextResume via the static NextStepPath, which
				// assumes the cap). If the job is then stopped after this group,
				// Continue would re-enter the already-finished group and re-judge.
				// Advance resume to the first node AFTER this whole group (or
				// clear it when the group is the last node) so Continue resumes
				// past it. Use the cap-th iteration's last step as the anchor so
				// NextStepPath skips the entire group.
				s.advanceResumePastConditionalGroup(ctx, job, node, basePath, i, ic)
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
					sr = s.executeShellRepeat(ctx, job, runner, node, stepPath, sessionID, nextResume, inConditional)
				case "", model.RoundTypePrompt:
					sr = s.executeRepeat(ctx, job, runner, node, stepPath, sessionID, nil, true, nextResume, inConditional)
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
// (rooted at basePath). Used to give a conditional group's judge turn a valid
// SSE base path; the judge events carry isJudge=true so this path never
// advances step progress. Falls back to basePath when there is no leaf step.
func lastChildStepPath(children []model.FlowNode, basePath []int) []int {
	var last []int
	enumLastPath(children, basePath, &last)
	if last == nil {
		return model.CopyPath(basePath)
	}
	return last
}

// backfillConditionalTotal rewrites the in-memory TotalSteps contribution of a
// conditional group from "cap × childrenSteps" down to "actualIters ×
// childrenSteps" so the progress bar finishes full when the loop stopped early
// (§5.4). In-memory only — not persisted, since §5.2 already accepts a restart
// re-running from round 0 (where the static estimate re-expands anyway).
func (s *serviceImpl) backfillConditionalTotal(job *model.Job, node model.FlowNode, groupPath []int, cap, actualIters int) {
	childSteps := model.CalcTotalSteps(node.Children)
	if childSteps == 0 {
		return
	}
	delta := (cap - actualIters) * childSteps
	if delta <= 0 {
		return
	}
	pathKey := pathKeyString(groupPath)
	s.mu.Lock()
	var newTotal int
	var actualMap map[string]int
	if job.Progress != nil {
		job.Progress.TotalSteps -= delta
		if job.Progress.TotalSteps < 0 {
			job.Progress.TotalSteps = 0
		}
		if job.Progress.ConditionalActualIterations == nil {
			job.Progress.ConditionalActualIterations = make(map[string]int)
		}
		job.Progress.ConditionalActualIterations[pathKey] = actualIters
		newTotal = job.Progress.TotalSteps
		actualMap = copyIntMap(job.Progress.ConditionalActualIterations)
	}
	s.mu.Unlock()

	// Push the recomputed denominator so connected clients update the
	// progress text / bar live (mirrors the in-memory backfill). The
	// frontend recomputes its session plan with the actual iteration counts.
	s.Publish(job.ID, &model.CustomEvent{
		BaseEvent: model.BaseEvent{
			Type: model.EventTypeCustom, JobID: job.ID,
			Timestamp: nowMillis(),
		},
		Name: "progress_total_updated",
		Value: map[string]any{
			"totalSteps":                  newTotal,
			"conditionalActualIterations": actualMap,
		},
	})
}

// pathKeyString renders a node path as a dot-joined string (e.g. "0.1") used
// as the key in JobProgress.ConditionalActualIterations and matched by the
// frontend session-plan walk.
func pathKeyString(path []int) string {
	parts := make([]string, len(path))
	for i, p := range path {
		parts[i] = fmt.Sprintf("%d", p)
	}
	return strings.Join(parts, ".")
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

// advanceResumePastConditionalGroup moves job.Resume to the first leaf step
// AFTER a conditional group that stopped early (§5.2). Anchoring NextStepPath
// on the group's cap-th iteration last step makes it skip every round of the
// group, so Continue resumes at the following sibling (or clears resume when
// the group is the last node). Without this, the last round's precomputed
// nextResume would point back into the group and re-judge an already-finished
// loop on Continue.
func (s *serviceImpl) advanceResumePastConditionalGroup(ctx context.Context, job *model.Job, node model.FlowNode, basePath []int, nodeIdx, cap int) {
	capGroupPath := appendPath(basePath, nodeIdx, cap-1)
	anchor := lastChildStepPath(node.Children, capGroupPath)
	nextPath := model.NextStepPath(job.LoopConfig.Flow, anchor)

	s.mu.Lock()
	if nextPath == nil {
		job.Resume = nil
	} else {
		job.Resume = &model.JobResume{NextPath: model.CopyPath(nextPath)}
	}
	s.mu.Unlock()
	if err := s.saveJobWithRetry(ctx, job, "conditional_group_exit"); err != nil {
		s.recordPersistWarning(ctx, job, "conditional_group_exit", err)
	}
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
