package job

import (
	"context"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/safe"
	"github.com/fanlv/quartet/types/model"
)

// Start launches the job's LoopConfig execution. Resets progress.
func (s *serviceImpl) Start(ctx context.Context, jobID string, runner JobRunner) error {
	// Hold the persist shard across the entire check→flip→persist sequence so a
	// concurrent ReplaceLoopConfig (which also holds it for its whole body)
	// cannot interleave: without this, ReplaceLoopConfig could observe a
	// non-Running snapshot, then mirror a stale config/progress back over the
	// fresh Running state we set here. See executor_loopconfig.go.
	lock := s.persistLock(jobID)
	lock.Lock()
	defer lock.Unlock()

	s.mu.Lock()
	job, ok := s.jobs[jobID]
	if !ok {
		s.mu.Unlock()
		return ErrJobNotFound
	}
	if job.Deleted {
		s.mu.Unlock()
		return ErrJobDeleted
	}
	if job.LoopConfig == nil {
		s.mu.Unlock()
		return ErrNoLoopConfig
	}
	if job.Status == model.JobStatusRunning {
		s.mu.Unlock()
		return ErrJobRunning
	}

	prevRunState := snapshotRunStateLocked(job)
	job.Progress = buildProgress(job.LoopConfig)
	job.Resume = &model.JobResume{}
	job.SessionIDs = nil
	job.Status = model.JobStatusRunning
	job.StartedAt = s.nowMillis()
	job.FinishedAt = 0
	cfg := job.LoopConfig
	startCtx := lifecycleStartContext{
		action:         jobRunActionStart,
		source:         jobRunSourceLoop,
		totalSteps:     job.Progress.TotalSteps,
		scheduleID:     job.ScheduleID,
		timeoutMinutes: loopTimeoutMinutes(job),
	}
	// Register cancel/done/loopRun BEFORE releasing s.mu so the run resources
	// are in place the instant Status=running becomes observable. Observers
	// read status under s.mu (Get) and only then act on it: a Stop that sees
	// running is guaranteed to find the cancel entry, and a graceful-stop
	// request is guaranteed to see the active loop run. Registering after the
	// unlock left a window where the job looked running but had no resources,
	// so Stop could no-op while the run went on to launch. prepareRunResources
	// takes only the cancel/done/runState mutexes (never s.mu), so calling it
	// here cannot deadlock. It must still precede saveJobWithRetryUnderPersistLock so the
	// persist-window race fix below stays intact.
	res := s.prepareRunResources(jobID, startCtx.timeoutMinutes, true)
	s.mu.Unlock()
	if err := s.saveJobWithRetryUnderPersistLock(ctx, job, jobRunActionStart); err != nil {
		s.abortRunResources(jobID, res)
		s.restoreRunStateAfterPersistFailure(ctx, job, prevRunState, jobRunActionStart, err)
		return err
	}
	// Reset the event buffer for the new run: a fresh sequence space + a
	// fresh in-flight tail. Stale events from a previous run would only
	// confuse late subscribers using a Last-Event-ID from the prior run.
	s.bus.resetForRun(job.ID)

	logLifecycleStart(ctx, jobID, startCtx)
	s.launchLoop(job, runner, cfg, res)
	return nil
}

func (s *serviceImpl) Continue(ctx context.Context, jobID string, runner JobRunner) error {
	// Hold the persist shard across check→flip→persist, mutually exclusive with
	// ReplaceLoopConfig (see Start for the rationale).
	lock := s.persistLock(jobID)
	lock.Lock()
	defer lock.Unlock()

	s.mu.Lock()
	job, ok := s.jobs[jobID]
	if !ok {
		s.mu.Unlock()
		return ErrJobNotFound
	}
	if job.Deleted {
		s.mu.Unlock()
		return ErrJobDeleted
	}
	if job.LoopConfig == nil {
		s.mu.Unlock()
		return ErrNoLoopConfig
	}
	if job.Status == model.JobStatusRunning {
		s.mu.Unlock()
		return ErrJobRunning
	}
	if job.Status != model.JobStatusStopped && job.Status != model.JobStatusFailed {
		s.mu.Unlock()
		return ErrJobNotRunnable
	}
	prevRunState := snapshotRunStateLocked(job)
	// Ensure Flow tree is populated (migrate legacy format if needed)
	model.MigrateLoopConfig(job.LoopConfig)
	resume := s.resumeForContinue(job)
	if resume == nil {
		restoreRunStateLocked(job, prevRunState)
		s.mu.Unlock()
		return ErrNoResumable
	}
	// Remove any stale iteration result at the resume path so Continue can
	// re-run that step without double-counting. Nothing happens if there's
	// no result there (e.g. the step was cancelled before recording one).
	if len(resume.NextPath) > 0 {
		filtered := job.Progress.Results[:0]
		for _, r := range job.Progress.Results {
			if model.EqualPaths(r.Path, resume.NextPath) {
				continue
			}
			filtered = append(filtered, r)
		}
		job.Progress.Results = filtered
		job.Progress.CompletedCount, job.Progress.FailedCount = countIterationResults(filtered)
	}
	// The previous run's failure reason is no longer the current state once
	// the user opts to Continue. Leaving it set surfaces stale errors on a
	// successfully-completed Continue — finishJob / applyTerminalStatusLocked
	// never touch LastError, so a SUCCESS terminal event would still carry
	// the prior failure in JobProgress. A fresh failure during this Continue
	// will be recorded by appendAndSaveResult / failJob.
	job.Progress.LastError = ""
	job.Resume = resume
	job.Status = model.JobStatusRunning
	job.StartedAt = s.nowMillis()
	job.FinishedAt = 0
	cfg := job.LoopConfig
	startCtx := lifecycleStartContext{
		action:         jobRunActionContinue,
		source:         jobRunSourceLoop,
		totalSteps:     job.Progress.TotalSteps,
		completedCount: job.Progress.CompletedCount,
		failedCount:    job.Progress.FailedCount,
		hasResume:      true,
		scheduleID:     job.ScheduleID,
		timeoutMinutes: loopTimeoutMinutes(job),
	}
	// Register run resources before releasing s.mu so they exist the instant
	// Status=running is observable — see the rationale in Start.
	res := s.prepareRunResources(jobID, startCtx.timeoutMinutes, true)
	s.mu.Unlock()
	if err := s.saveJobWithRetryUnderPersistLock(ctx, job, jobRunActionContinue); err != nil {
		s.abortRunResources(jobID, res)
		s.restoreRunStateAfterPersistFailure(ctx, job, prevRunState, jobRunActionContinue, err)
		return err
	}
	// Continue reuses the existing buffer (only Start calls resetForRun).
	// The prior terminal event marked it; flip back so GC runs again over
	// the new run's events.
	s.bus.resumeGC(job.ID)

	logLifecycleStart(ctx, jobID, startCtx)
	s.launchLoop(job, runner, cfg, res)
	return nil
}

// SendMessage sends a message to an existing job session. Appends to progress.
//
// Interactive messages vs loop Resume:
//
//	Scenario                              | Status after | Resume
//	---                                   | ---          | ---
//	Stopped (paused) → send → msg done    | stopped      | preserved → Continue works
//	Stopped (paused) → send → msg fails   | stopped      | preserved → Continue works
//	Stopped (paused) → send → Stop msg    | stopped      | preserved → Continue works
//	Completed → send → any outcome        | completed    | nil (unchanged)
//	Failed → send → any outcome           | failed       | preserved → Continue works
//	Pending (never ran) → send → msg done | completed    | nil
//	Pending (never ran) → send → Stop msg | stopped      | nil
//	Loop finishes normally                | completed    | nil
//	Loop step fails (shell / agent)       | failed       | preserved → Continue retries this step
//	Loop panics                           | failed       | nil
//
// Core rule: interactive messages never touch Resume, and any pre-existing
// terminal status (Completed/Failed/Stopped) is restored when the interactive
// run ends so an ad-hoc message never regresses it.
func (s *serviceImpl) SendMessage(ctx context.Context, jobID string, runner JobRunner, opts *SendMessageOptions) error {
	// Validate before modifying any state to avoid leaving the job in Running
	// status if validation fails.
	if len(opts.getMessages()) == 0 {
		return ErrEmptyMessage
	}

	// Hold the persist shard across check→flip→persist, mutually exclusive with
	// ReplaceLoopConfig (see Start for the rationale).
	lock := s.persistLock(jobID)
	lock.Lock()
	defer lock.Unlock()

	s.mu.Lock()
	job, ok := s.jobs[jobID]
	if !ok {
		s.mu.Unlock()
		return ErrJobNotFound
	}
	if job.Deleted {
		s.mu.Unlock()
		return ErrJobDeleted
	}
	if job.Status == model.JobStatusRunning {
		s.mu.Unlock()
		return ErrJobRunning
	}

	prevRunState := snapshotRunStateLocked(job)
	// Don't clear Resume — preserve it so loop Continue still works after interactive messages.
	// Remember the prior status so that terminal states (Completed/Failed/
	// Stopped) are restored when the interactive run ends, instead of being
	// overwritten by the interactive run's outcome.
	priorStatus := job.Status
	priorResume := job.Resume
	job.Status = model.JobStatusRunning
	job.StartedAt = s.nowMillis()
	job.FinishedAt = 0
	startCtx := lifecycleStartContext{
		action:      jobRunActionSendMessage,
		source:      jobRunSourceInteractive,
		totalSteps:  job.Progress.TotalSteps,
		hasResume:   priorResume != nil,
		priorStatus: priorStatus,
		scheduleID:  job.ScheduleID,
	}
	// Register cancel/done before releasing s.mu so they exist the instant
	// Status=running is observable — see the rationale in Start. (loopRun=false:
	// an interactive send doesn't walk runFlowNodes and can't consume a graceful
	// stop, so no runState entry is created.)
	res := s.prepareRunResources(job.ID, 0, false)
	s.mu.Unlock()
	if err := s.saveJobWithRetryUnderPersistLock(ctx, job, jobRunActionSendMessageStart); err != nil {
		s.abortRunResources(job.ID, res)
		s.restoreRunStateAfterPersistFailure(ctx, job, prevRunState, jobRunActionSendMessageStart, err)
		return err
	}
	// SendMessage reuses the existing buffer (only Start calls resetForRun).
	// If the job already reached a terminal status, MarkTerminal disabled
	// GC; flip it back so the interactive run's events get reclaimed.
	s.bus.resumeGC(job.ID)
	// Stopped without Resume is a chat-style stop (no continuability) — let
	// the new send's outcome drive the next status (success → Completed,
	// failure → Failed). Only preserve Stopped when there's a Resume (a
	// paused loop where Continue should still work).
	if shouldPreservePriorStatus(priorStatus, priorResume) {
		s.setInteractivePriorStatus(job.ID, priorStatus)
	}

	logLifecycleStart(ctx, job.ID, startCtx)

	safe.Go(ctx, func() {
		defer s.cleanupDone(job.ID, res.done)
		s.runInteractive(res.ctx, job, runner, opts, res.entry)
	})
	return nil
}

// runInteractive executes a single user-initiated message round against the
// job's existing session (or a freshly created one). Unlike runLoop it does
// not iterate over a flow tree, doesn't touch Resume, and the prior terminal
// status (if any) is restored by the deferred finish/stop/fail path.
func (s *serviceImpl) runInteractive(ctx context.Context, job *model.Job, runner JobRunner, opts *SendMessageOptions, cancelEntry *cancelEntry) {
	defer s.clearCancel(job.ID, cancelEntry)
	defer func() {
		// Read Status under the lock — failJob / stopJob may have already
		// flipped it in this goroutine, and a concurrent handler-side
		// Start / SendMessage that observes a terminal status could be
		// writing it back to Running on another goroutine. The lock pairs
		// with those writes for visibility and silences the race detector.
		s.mu.RLock()
		status := job.Status
		s.mu.RUnlock()
		if status != model.JobStatusRunning {
			return
		}
		if ctx.Err() != nil {
			s.stopJob(ctx, job, false)
			return
		}

		s.finishJob(ctx, job, false)
	}()
	defer s.recoverRunPanic(ctx, job, jobRunSourceInteractive, false)

	logger.Debugf(ctx, "[interactive] start: jobId=%s", job.ID)

	// Mirror the loop entry: publish JOB_STARTED so other SSE subscribers
	// (e.g. another tab watching this job) see the run re-enter Running.
	// SendMessage already persisted Status=running + StartedAt before
	// reaching here, so the §1.4 "state before publish" contract holds.
	s.publishJobStarted(job)

	s.injectBuiltinVars(ctx, job)

	sessionID := opts.SessionID
	if sessionID == "" {
		sid, err := s.initAndAttachSession(ctx, job, runner, &model.SessionOverrides{
			AgentType:       opts.AgentType,
			ModelID:         opts.ModelID,
			ACPMode:         opts.ACPMode,
			ACPThoughtLevel: opts.ACPThoughtLevel,
		})
		if err != nil {
			logger.Errorf(ctx, "[interactive] init session failed: jobId=%s err=%v", job.ID, err)
			// Don't write Progress.Results — interactive runs never touch
			// it (see executeRepeat's !isLoopRun branch). failJob persists
			// the error on Progress.LastError and publishes JOB_FAILED;
			// no IterationStarted has been published yet, so we must not
			// emit a stray IterationFailed either.
			s.failJob(ctx, job, err.Error(), false, false)
			return
		}
		sessionID = sid
	}

	// Build a synthetic single-step FlowNode so executeRepeat — which is
	// shared with loop step iteration — can publish IterationStarted /
	// RunStarted / RunOutcome exactly as it does for a loop step.
	path := []int{0, 0}
	node := model.FlowNode{
		ID:              model.NewFlowNodeID(),
		Type:            model.FlowNodeTypeStep,
		Message:         opts.Messages[0].Content,
		RepeatCount:     1,
		RoundMode:       model.RoundModeNone,
		AgentType:       opts.AgentType,
		StepModelID:     opts.ModelID,
		ACPMode:         opts.ACPMode,
		ACPThoughtLevel: opts.ACPThoughtLevel,
	}
	if node.Message == "" && len(opts.Messages[0].UserInputMultiContent) > 0 {
		for _, part := range opts.Messages[0].UserInputMultiContent {
			if part.Text != "" {
				node.Message = part.Text
				break
			}
		}
	}

	s.injectPerRoundVars(ctx, job, path)
	s.executeRepeat(ctx, job, runner, node, path, sessionID, opts, false /* isLoopRun */, nil /* nextResume — interactive runs don't drive loop resume */)
}

// buildProgress creates a fresh JobProgress from a LoopConfig. It migrates the
// config first so a legacy (Rounds-only) config — whose Flow is still nil —
// gets its tree populated before CalcTotalSteps runs; otherwise TotalSteps
// would be computed as 0 and the progress denominator would be wrong for the
// whole run.
func buildProgress(cfg *model.LoopConfig) *model.JobProgress {
	model.MigrateLoopConfig(cfg)
	return &model.JobProgress{
		TotalSteps: model.CalcTotalSteps(cfg.Flow),
	}
}

func countIterationResults(results []model.IterationResult) (completed, failed int) {
	for _, r := range results {
		if r.Success {
			completed++
		} else {
			failed++
		}
	}
	return completed, failed
}

// ensureProgress guarantees job.Progress is non-nil so other code paths can
// dereference it without per-call nil guards. When the job has a LoopConfig
// the lazy-init seeds TotalSteps (so Continue on a never-started loop can
// still reason about completion); otherwise an empty progress is enough.
//
// Callers must invoke this only at the boundary where a *model.Job enters
// s.jobs (load on startup, Create) — runtime paths assume the invariant.
func ensureProgress(job *model.Job) {
	if job == nil || job.Progress != nil {
		return
	}
	if job.LoopConfig != nil {
		job.Progress = buildProgress(job.LoopConfig)
		return
	}
	job.Progress = &model.JobProgress{}
}

func copyResume(resume *model.JobResume) *model.JobResume {
	if resume == nil {
		return nil
	}
	cp := *resume
	cp.NextPath = model.CopyPath(resume.NextPath)
	return &cp
}

func (s *serviceImpl) updateResume(ctx context.Context, job *model.Job, resume *model.JobResume, action string) {
	s.mu.Lock()
	job.Resume = copyResume(resume)
	s.mu.Unlock()
	if err := s.saveJobWithRetry(ctx, job, action); err != nil {
		s.recordPersistWarning(ctx, job, action, err)
	}
}

func (s *serviceImpl) resumeForContinue(job *model.Job) *model.JobResume {
	if job == nil || job.LoopConfig == nil {
		return nil
	}
	if job.Resume != nil {
		return copyResume(job.Resume)
	}
	if job.Progress.CompletedCount+job.Progress.FailedCount >= job.Progress.TotalSteps {
		return nil
	}
	if job.Progress.CompletedCount+job.Progress.FailedCount == 0 {
		return &model.JobResume{}
	}
	// Compute next path from the current position
	nextPath := model.NextStepPath(job.LoopConfig.Flow, job.Progress.CurrentPath)
	if nextPath == nil {
		return nil
	}
	return &model.JobResume{NextPath: nextPath}
}
