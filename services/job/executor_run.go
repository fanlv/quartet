package job

import (
	"context"
	"fmt"
	"runtime/debug"
	"sync"
	"time"

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
	job.StartedAt = nowMillis()
	job.FinishedAt = 0
	// Ensure Flow tree is populated (migrate legacy format if needed)
	model.MigrateLoopConfig(job.LoopConfig)
	cfg := job.LoopConfig
	startCtx := lifecycleStartContext{
		action:         "start",
		source:         "loop",
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
	// here cannot deadlock. It must still precede saveJobWithRetryLocked so the
	// persist-window race fix below stays intact.
	res := s.prepareRunResources(jobID, startCtx.timeoutMinutes, true)
	s.mu.Unlock()
	if err := s.saveJobWithRetryLocked(ctx, job, "start"); err != nil {
		s.abortRunResources(jobID, res)
		s.restoreRunStateAfterPersistFailure(ctx, job, prevRunState, "start", err)
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
	job.StartedAt = nowMillis()
	job.FinishedAt = 0
	cfg := job.LoopConfig
	startCtx := lifecycleStartContext{
		action:         "continue",
		source:         "loop",
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
	if err := s.saveJobWithRetryLocked(ctx, job, "continue"); err != nil {
		s.abortRunResources(jobID, res)
		s.restoreRunStateAfterPersistFailure(ctx, job, prevRunState, "continue", err)
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
	job.StartedAt = nowMillis()
	job.FinishedAt = 0
	startCtx := lifecycleStartContext{
		action:      "send_message",
		source:      "interactive",
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
	if err := s.saveJobWithRetryLocked(ctx, job, "send_message_start"); err != nil {
		s.abortRunResources(job.ID, res)
		s.restoreRunStateAfterPersistFailure(ctx, job, prevRunState, "send_message_start", err)
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
		if r := recover(); r != nil {
			panicErr := fmt.Errorf("job panicked: %v", r)
			logger.Errorf(ctx, "[interactive] panic: jobId=%s err=%v\n%s", job.ID, r, string(debug.Stack()))
			// Close any in-flight buffer round before the terminal event,
			// so SendMessage / Continue's ResumeGC can reclaim its A-class
			// chunks. See closePanicRoundIfOpen for the leak this guards
			// against.
			s.closePanicRoundIfOpen(job, panicErr)
			s.failJob(ctx, job, panicErr.Error(), false, false)
			return
		}

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
			AgentType: opts.AgentType,
			ModelID:   opts.ModelID,
			ACPMode:   opts.ACPMode,
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
		ID:          model.NewFlowNodeID(),
		Type:        model.FlowNodeTypeStep,
		Message:     opts.Messages[0].Content,
		RepeatCount: 1,
		RoundMode:   model.RoundModeNone,
		AgentType:   opts.AgentType,
		StepModelID: opts.ModelID,
		ACPMode:     opts.ACPMode,
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

// stopAndWaitTimeout bounds how long StopAndWait blocks waiting for a single
// runLoop goroutine to exit after cancellation. Generous because a single Stop
// is rare and a stuck goroutine here is much more important to surface than to
// silently drop on the floor; this becomes the upper bound on the cancellation
// signal propagating through context-aware code paths (LLM HTTP clients,
// streaming readers).
const stopAndWaitTimeout = 60 * time.Second

// stopAllPerJobTimeout is the per-job ceiling on graceful shutdown waits.
// Smaller than stopAndWaitTimeout because StopAll runs at process exit and
// budget is multiplied by the number of live jobs — a 60s cap with N=10 live
// jobs would block shutdown for up to 10 minutes. 10s is enough for a shell
// step to receive SIGTERM, run the 3s shellGracePeriod, get SIGKILL, and have
// cmd.Wait() return; longer-running cancellation paths fall back to logging
// the timeout and letting the OS reap them when the process exits.
const stopAllPerJobTimeout = 10 * time.Second

// Stop cancels a running job.
func (s *serviceImpl) Stop(jobID string) {
	// A hard Stop never reaches a graceful step boundary (it cancels the
	// context mid-step), so consumeGracefulStop won't run. Clear any pending
	// graceful-stop request here so an escalation from "stop after step" to
	// "stop now" doesn't leave a stale pending flag that Get would keep
	// synthesizing onto the stopped snapshot. (The launchLoop defer is the
	// catch-all for timeout/fail/panic paths; this closes the gap immediately
	// for the explicit-stop path.)
	s.clearGracefulStop(jobID)

	s.cancelMu.Lock()
	entry, ok := s.cancels[jobID]
	if ok {
		entry.cancel()
		delete(s.cancels, jobID)
	}
	s.cancelMu.Unlock()
}

// loopRunState is the per-job loop-run state guarded by runStateMu. An entry
// exists in runStates only while a loop run is active; gracefulPending is
// meaningful only on an existing entry, so clearing the entry (run exit) is
// what guarantees a non-active job can never carry a pending flag.
type loopRunState struct {
	gracefulPending bool
}

// RequestGracefulStop marks a running loop job to stop at the next step
// boundary: the in-flight step runs to completion (records its result, advances
// resume) and then runFlowNodes returns instead of starting the next step. The
// job ends Stopped with Resume preserved, so Continue resumes cleanly from the
// next step — unlike the hard Stop, which cancels the context mid-step and
// re-runs the interrupted step on Continue.
//
// Best-effort: if the current step never finishes (e.g. a hung LLM call), this
// will not interrupt it — the user can escalate to a hard Stop. Idempotent.
//
// No-op unless the job currently has an active loop run that can consume the
// request. The active-run check and the pending write happen under the SAME
// runStateMu, so a run cannot exit (and drop its entry) between them: either
// the entry is present and we set its flag, or it's gone and we no-op. This is
// what keeps a non-running or interactive job from accumulating a stale pending
// flag that a later run would have to clear at launch.
func (s *serviceImpl) RequestGracefulStop(jobID string) {
	s.runStateMu.Lock()
	st, ok := s.runStates[jobID]
	if !ok {
		s.runStateMu.Unlock()
		return
	}
	alreadyPending := st.gracefulPending
	st.gracefulPending = true
	s.runStateMu.Unlock()
	if !alreadyPending {
		s.publishGracefulStopPending(jobID, true)
	}
}

// isGracefulStopPending reports whether a graceful stop is currently pending
// for jobID. Used to synthesize the runtime-only JobProgress.GracefulStopPending
// view field onto a Get snapshot.
func (s *serviceImpl) isGracefulStopPending(jobID string) bool {
	s.runStateMu.Lock()
	defer s.runStateMu.Unlock()
	st, ok := s.runStates[jobID]
	return ok && st.gracefulPending
}

// publishGracefulStopPending broadcasts the runtime-only graceful-stop pending
// state as a transient SSE event so other connected tabs update their "stop
// after step" / "keep running" affordance live. Transient (not buffered): the
// flag is never persisted, and a refresh re-reads it from the Get snapshot.
func (s *serviceImpl) publishGracefulStopPending(jobID string, pending bool) {
	s.PublishTransient(jobID, &model.CustomEvent{
		BaseEvent: model.BaseEvent{
			Type: model.EventTypeCustom, JobID: jobID,
			Timestamp: nowMillis(),
		},
		Name:  "graceful_stop_pending",
		Value: map[string]any{"pending": pending},
	})
}

// consumeGracefulStop reports whether a graceful stop was requested for jobID
// and clears the flag. Called by runFlowNodes at each step boundary.
func (s *serviceImpl) consumeGracefulStop(jobID string) bool {
	s.runStateMu.Lock()
	st, ok := s.runStates[jobID]
	consumed := ok && st.gracefulPending
	if consumed {
		st.gracefulPending = false
	}
	s.runStateMu.Unlock()
	if consumed {
		// The pending request is now consumed (the loop is stopping); tell
		// connected tabs so they drop the "keep running" affordance.
		s.publishGracefulStopPending(jobID, false)
	}
	return consumed
}

// CancelGracefulStop drops a pending graceful-stop request so the loop keeps
// running. Thin exported wrapper over clearGracefulStop for the HTTP handler;
// no-op if nothing is pending.
func (s *serviceImpl) CancelGracefulStop(jobID string) {
	s.clearGracefulStop(jobID)
}

// clearGracefulStop drops any pending graceful-stop request for jobID without
// removing the run entry itself. Called by Stop (hard-stop escalation) and
// CancelGracefulStop. Broadcasts the cleared state only when a request was
// actually pending so connected tabs drop the "keep running" affordance
// without spamming no-op events.
func (s *serviceImpl) clearGracefulStop(jobID string) {
	s.runStateMu.Lock()
	st, ok := s.runStates[jobID]
	wasPending := ok && st.gracefulPending
	if wasPending {
		st.gracefulPending = false
	}
	s.runStateMu.Unlock()
	if wasPending {
		s.publishGracefulStopPending(jobID, false)
	}
}

// StopAndWait cancels a running job and blocks until its runLoop goroutine exits
// or stopAndWaitTimeout is reached.
func (s *serviceImpl) StopAndWait(jobID string) {
	s.Stop(jobID)

	s.doneMu.Lock()
	done := s.dones[jobID]
	s.doneMu.Unlock()

	if done != nil {
		select {
		case <-done:
		case <-time.After(stopAndWaitTimeout):
			logger.Errorf(context.Background(), "[job.Service] StopAndWait timed out for job %s", jobID)
		}
	}
}

// StopAll cancels all running jobs and waits for their goroutines to exit.
// Used during graceful shutdown.
func (s *serviceImpl) StopAll() {
	// Snapshot all cancel funcs under lock, then cancel outside lock.
	s.cancelMu.Lock()
	cancels := make(map[string]context.CancelFunc, len(s.cancels))
	for id, entry := range s.cancels {
		cancels[id] = entry.cancel
	}
	s.cancelMu.Unlock()

	for id, cancel := range cancels {
		logger.Infof(context.Background(), "[job.Service] StopAll: cancelling job %s", id)
		cancel()
	}

	// Wait for all runLoop goroutines to exit.
	s.doneMu.Lock()
	dones := make(map[string]chan struct{}, len(s.dones))
	for id, done := range s.dones {
		dones[id] = done
	}
	s.doneMu.Unlock()

	var wg sync.WaitGroup
	wg.Add(len(dones))
	for id, done := range dones {
		go func() {
			defer wg.Done()
			select {
			case <-done:
			case <-time.After(stopAllPerJobTimeout):
				logger.Errorf(context.Background(), "[job.Service] StopAll: timed out waiting for job %s", id)
			}
		}()
	}
	wg.Wait()
}

// defaultScheduleTimeoutMinutes is the fallback timeout for scheduled jobs that
// don't have an explicit timeout set. Prevents runaway shell scripts from leaking
// resources indefinitely when no one is around to manually stop them.
const defaultScheduleTimeoutMinutes = 120

// loopTimeoutMinutes returns the runLoop context timeout for a job, applying
// the scheduled-job default when the job has a ScheduleID but no explicit
// timeout. ScheduleID and TimeoutMinutes are immutable after job creation,
// so no lock is required.
func loopTimeoutMinutes(job *model.Job) int {
	timeout := job.TimeoutMinutes
	if timeout == 0 && job.ScheduleID != "" {
		timeout = defaultScheduleTimeoutMinutes
	}
	return timeout
}

// runResources groups the cancel/done tracking allocated for a single run.
// Splitting acquisition (in Start / Continue / SendMessage, BEFORE
// saveJobWithRetry) from goroutine spawn (in launchLoop / SendMessage,
// AFTER saveJobWithRetry) closes the race where a Stop / Delete observed
// during the persist window would miss the cancel registration — which used
// to leak the goroutine and let it resurrect the job dir on disk after
// Delete cleaned it up.
type runResources struct {
	ctx   context.Context
	entry *cancelEntry
	done  chan struct{}
}

// prepareRunResources allocates and registers the cancel/done tracking for a
// new run. Pass timeoutMinutes > 0 to wrap the context with a deadline (used
// for scheduled loops); pass 0 for interactive sends and unbounded loops.
func (s *serviceImpl) prepareRunResources(jobID string, timeoutMinutes int, loopRun bool) *runResources {
	// Reset the per-job notifyJobDone dedup flag so a re-launched jobID
	// (Continue after Stopped, Start after a previous terminal) can emit a
	// fresh done event. Interactive sends don't call notifyJobDone but we
	// clear here too to keep the invariant symmetric.
	s.clearJobDoneNotified(jobID)
	// Drop any stale graceful-stop request so it can't immediately stop a
	// freshly started / continued run before its first step boundary.
	s.clearGracefulStop(jobID)
	if loopRun {
		s.markLoopRun(jobID)
	} else {
		s.clearLoopRun(jobID)
	}

	var ctx context.Context
	var cancel context.CancelFunc
	if timeoutMinutes > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), time.Duration(timeoutMinutes)*time.Minute)
	} else {
		ctx, cancel = context.WithCancel(context.Background())
	}
	return &runResources{
		ctx:   ctx,
		entry: s.registerCancel(jobID, cancel),
		done:  s.registerDone(jobID),
	}
}

// abortRunResources rolls back a prepareRunResources call when the pre-launch
// persist failed and no goroutine will be spawned. clearCancel and cleanupDone
// guard their map deletes by entry / channel identity, so this is safe to call
// even if a concurrent Stop already cancelled and dropped the entry.
func (s *serviceImpl) abortRunResources(jobID string, res *runResources) {
	s.clearCancel(jobID, res.entry)
	s.cleanupDone(jobID, res.done)
	s.clearLoopRun(jobID)
}

// launchLoop spawns the runLoop goroutine using cancel/done resources that
// were already registered (before the persist barrier) by prepareRunResources.
func (s *serviceImpl) launchLoop(job *model.Job, runner JobRunner, cfg *model.LoopConfig, res *runResources) {
	safe.Go(res.ctx, func() {
		// clearLoopRun removes the run entry on exit for ANY reason (completion,
		// failure, timeout, panic, hard cancel) and broadcasts a cleared
		// graceful-stop state if one was still pending — so an abnormal exit
		// can't leave a stale flag that Get keeps synthesizing onto the terminal
		// snapshot. consumeGracefulStop handles the clean step-boundary case.
		defer s.clearLoopRun(job.ID)
		defer s.cleanupDone(job.ID, res.done)
		s.runLoop(res.ctx, job, runner, cfg, res.entry)
	})
}

// markLoopRun records that a loop run is active for jobID, creating the
// runStates entry that RequestGracefulStop / consumeGracefulStop key off of.
func (s *serviceImpl) markLoopRun(jobID string) {
	s.runStateMu.Lock()
	if s.runStates == nil {
		s.runStates = make(map[string]*loopRunState)
	}
	if _, ok := s.runStates[jobID]; !ok {
		s.runStates[jobID] = &loopRunState{}
	}
	s.runStateMu.Unlock()
}

// clearLoopRun removes the run entry for jobID when the loop run exits. Removing
// the entry necessarily drops any pending graceful-stop flag it held; if a flag
// was pending, broadcast the cleared state so connected tabs drop the "keep
// running" affordance. Because the active-run check and the pending write in
// RequestGracefulStop share runStateMu, no request can slip a pending flag onto
// this jobID after the entry is gone.
func (s *serviceImpl) clearLoopRun(jobID string) {
	s.runStateMu.Lock()
	st, ok := s.runStates[jobID]
	wasPending := ok && st.gracefulPending
	delete(s.runStates, jobID)
	s.runStateMu.Unlock()
	if wasPending {
		s.publishGracefulStopPending(jobID, false)
	}
}

func (s *serviceImpl) IsGracefulStopSupported(jobID string) bool {
	s.runStateMu.Lock()
	defer s.runStateMu.Unlock()
	_, ok := s.runStates[jobID]
	return ok
}

type cancelEntry struct {
	cancel context.CancelFunc
}

func (s *serviceImpl) registerCancel(jobID string, cancel context.CancelFunc) *cancelEntry {
	entry := &cancelEntry{cancel: cancel}
	s.cancelMu.Lock()
	s.cancels[jobID] = entry
	s.cancelMu.Unlock()
	return entry
}

func (s *serviceImpl) registerDone(jobID string) chan struct{} {
	done := make(chan struct{})
	s.doneMu.Lock()
	s.dones[jobID] = done
	s.doneMu.Unlock()
	return done
}

func (s *serviceImpl) cleanupDone(jobID string, done chan struct{}) {
	close(done)
	s.doneMu.Lock()
	// Only remove if the map still points to our channel;
	// a rapid Stop+Start could have already replaced it.
	if s.dones[jobID] == done {
		delete(s.dones, jobID)
	}
	s.doneMu.Unlock()
}
