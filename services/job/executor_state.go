package job

import (
	"context"
	"errors"
	"fmt"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
)

func (s *serviceImpl) publishRunOutcome(jobID, sessionID string, path []int, runID string, err error, terminalAt int64) {
	if terminalAt <= 0 {
		terminalAt = nowMillis()
	}

	// User-initiated stop (context.Canceled) is not an error — publish
	// RunFinished so the frontend doesn't show a spurious error toast.
	if err == nil || errors.Is(err, context.Canceled) {
		s.Publish(jobID, &model.RunFinishedEvent{
			BaseEvent: model.BaseEvent{
				Type: model.EventTypeRunFinished, JobID: jobID,
				SessionID: sessionID, RunID: runID,
				Path:      path,
				Timestamp: terminalAt,
			},
		})
		return
	}

	s.Publish(jobID, &model.RunErrorEvent{
		BaseEvent: model.BaseEvent{
			Type: model.EventTypeRunError, JobID: jobID,
			SessionID: sessionID, RunID: runID,
			Path:      path,
			Timestamp: terminalAt,
		},
		Message: err.Error(),
		Code:    "-1",
	})
}

func (s *serviceImpl) recordIterationResult(job *model.Job, result *model.IterationResult) {
	s.appendAndSaveResult(job, *result, nil, false)
	s.publishIterationEvent(job.ID, result)
}

// recordIterationAndAdvanceResume records the iteration result AND advances
// the resume pointer in a single persist. Used by the loop success path so
// per-step persists drop from 2 saves (record + advance_resume) to 1.
func (s *serviceImpl) recordIterationAndAdvanceResume(job *model.Job, result *model.IterationResult, nextResume *model.JobResume) {
	s.appendAndSaveResult(job, *result, nextResume, true)
	s.publishIterationEvent(job.ID, result)
}

func (s *serviceImpl) finishJob(ctx context.Context, job *model.Job, isLoopRun bool) {
	s.mu.Lock()
	resolution := s.applyTerminalStatusLocked(job, isLoopRun, model.JobStatusCompleted, true)
	snap := captureTerminalSnapshotLocked(job)
	s.mu.Unlock()
	// Publish the terminal event that matches the final status; runOutcome
	// records that *this* run (loop or interactive send) actually
	// completed successfully, regardless of whether finalStatus is a
	// restored prior status.
	finalStatus := s.persistAndPublishTerminal(ctx, job, snap, "finish", "", model.RunOutcomeCompleted)
	logLifecycleTerminal(ctx, job.ID, "finish", jobRunSource(isLoopRun), finalStatus, model.RunOutcomeCompleted, resolution, "")
	if isLoopRun && finalStatus == model.JobStatusCompleted {
		s.notifyJobDone(job)
	}
}

func jobRunSource(isLoopRun bool) string {
	if isLoopRun {
		return "loop"
	}
	return "interactive"
}

func (s *serviceImpl) clearCancel(jobID string, entry *cancelEntry) {
	// Always release the context resources owned by this run. CancelFunc is
	// idempotent, so this is safe if Stop / StopAll already cancelled it. Keep
	// the map deletion guarded by entry identity so an old run cannot remove a
	// newer run's cancel registered by a rapid Stop+Continue.
	entry.cancel()

	s.cancelMu.Lock()
	// Only remove if the map still points to our entry; a rapid Stop+Continue
	// can register a new run's cancel before the old run's deferred cleanup runs.
	if s.cancels[jobID] == entry {
		delete(s.cancels, jobID)
	}
	s.cancelMu.Unlock()
}

func (s *serviceImpl) progressCounts(job *model.Job) (completedCount int, failedCount int, totalSteps int) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return job.Progress.CompletedCount, job.Progress.FailedCount, job.Progress.TotalSteps
}

func (s *serviceImpl) stopJob(ctx context.Context, job *model.Job, isLoopRun bool) {
	s.mu.Lock()
	resolution := s.applyTerminalStatusLocked(job, isLoopRun, model.JobStatusStopped, false)
	snap := captureTerminalSnapshotLocked(job)
	s.mu.Unlock()
	finalStatus := s.persistAndPublishTerminal(ctx, job, snap, "stop", "", model.RunOutcomeStopped)
	logLifecycleTerminal(ctx, job.ID, "stop", jobRunSource(isLoopRun), finalStatus, model.RunOutcomeStopped, resolution, "")
	if isLoopRun {
		s.notifyJobDone(job)
	}
}

// failJob marks a job as failed and publishes the matching terminal event.
//
// preserveResume controls whether job.Resume is kept when the failure is
// recorded as JobStatusFailed. Loop-run failures coming from a known
// step (shell exit, agent iteration error) should pass true so that the
// user can Continue from the failing path; unrecoverable failures
// (panics, structural errors) should pass false so we don't leave a
// stale resume pointer behind. Interactive-run callers can pass either
// — applyTerminalStatusLocked overrides the status before Resume is
// cleared, so preserveResume has no effect for interactive runs that hit
// the prior-status / paused-loop branches.
func (s *serviceImpl) failJob(ctx context.Context, job *model.Job, message string, isLoopRun bool, preserveResume bool) {
	s.mu.Lock()
	resolution := s.applyTerminalStatusLocked(job, isLoopRun, model.JobStatusFailed, !preserveResume)
	// Defensive: ensure Progress is non-nil before recording LastError.
	// A panic-recovery caller would otherwise re-panic here, leaving the
	// Job stuck in Running until the next process load() reset.
	if job.Progress == nil {
		job.Progress = &model.JobProgress{}
	}
	// Persist the failure reason on the progress so it survives a page
	// refresh — SSE events alone only reach clients still connected at
	// the moment of failure.
	if message != "" {
		job.Progress.LastError = message
	}
	snap := captureTerminalSnapshotLocked(job)
	s.mu.Unlock()
	finalStatus := s.persistAndPublishTerminal(ctx, job, snap, "fail", message, model.RunOutcomeFailed)
	logLifecycleTerminal(ctx, job.ID, "fail", jobRunSource(isLoopRun), finalStatus, model.RunOutcomeFailed, resolution, message)
	if isLoopRun && finalStatus == model.JobStatusFailed {
		s.notifyJobDone(job)
	}
}

type terminalStatusResolution struct {
	targetStatus          model.JobStatus
	statusReason          string
	priorStatus           model.JobStatus
	restoredPriorStatus   bool
	resumePresentAtFinish bool
	resumeCleared         bool
}

// applyTerminalStatusLocked decides and writes the final job.Status for a
// terminal transition. Caller MUST hold s.mu.
//
// Resolution order (interactive sends only — loop runs always take the
// natural target path):
//  1. A stored prior terminal status (Completed/Failed/Stopped) recorded
//     by SendMessage             → restore it; leave Resume untouched.
//  2. job.Resume != nil (paused loop)
//     → JobStatusStopped so Continue still works;
//     leave Resume untouched.
//  3. otherwise                  → targetStatus, clearing Resume iff clearResume.
func (s *serviceImpl) applyTerminalStatusLocked(job *model.Job, isLoopRun bool, targetStatus model.JobStatus, clearResume bool) terminalStatusResolution {
	resolution := terminalStatusResolution{
		targetStatus:          targetStatus,
		statusReason:          "target_status",
		resumePresentAtFinish: job.Resume != nil,
	}
	if !isLoopRun {
		if prior, ok := s.consumeInteractivePriorStatusLocked(job.ID); ok && isTerminalJobStatus(prior) {
			job.Status = prior
			resolution.priorStatus = prior
			resolution.restoredPriorStatus = true
			resolution.statusReason = "restored_prior_status"
			return resolution
		}
		if job.Resume != nil {
			job.Status = model.JobStatusStopped
			resolution.statusReason = "resume_present"
			return resolution
		}
	}
	job.Status = targetStatus
	if clearResume {
		job.Resume = nil
		resolution.resumeCleared = resolution.resumePresentAtFinish
	}
	return resolution
}

func logLifecycleTerminal(ctx context.Context, jobID, action, source string, finalStatus model.JobStatus, runOutcome model.RunOutcome, resolution terminalStatusResolution, message string) {
	verb := lifecycleActionVerb(action)
	// Failed/stopped runs stay at INFO for operator triage; successful
	// completions from scheduled (loop) tasks are demoted to DEBUG to
	// reduce log noise from high-frequency cron runs. Interactive job
	// completions remain at INFO so operators can always correlate the
	// "run starting" / "run finished" pair without switching to DEBUG.
	logFn := logger.Debugf
	if finalStatus == model.JobStatusFailed || finalStatus == model.JobStatusStopped || message != "" || source == "interactive" {
		logFn = logger.Infof
	}
	if message != "" {
		logFn(ctx, "[job.lifecycle] run %s: jobId=%s action=%s source=%s targetStatus=%s finalStatus=%s runOutcome=%s statusReason=%s priorStatus=%s restoredPriorStatus=%t resumePresentAtFinish=%t resumeCleared=%t err=%q",
			verb, jobID, action, source, resolution.targetStatus, finalStatus, runOutcome, resolution.statusReason, statusLogValue(resolution.priorStatus), resolution.restoredPriorStatus, resolution.resumePresentAtFinish, resolution.resumeCleared, message)
		return
	}
	logFn(ctx, "[job.lifecycle] run %s: jobId=%s action=%s source=%s targetStatus=%s finalStatus=%s runOutcome=%s statusReason=%s priorStatus=%s restoredPriorStatus=%t resumePresentAtFinish=%t resumeCleared=%t",
		verb, jobID, action, source, resolution.targetStatus, finalStatus, runOutcome, resolution.statusReason, statusLogValue(resolution.priorStatus), resolution.restoredPriorStatus, resolution.resumePresentAtFinish, resolution.resumeCleared)
}

// lifecycleStartContext carries the snapshot needed to emit the run-start
// counterpart to logLifecycleTerminal. Captured under s.mu in the caller so
// the log line reflects the exact state that the goroutine is about to run on.
type lifecycleStartContext struct {
	action         string // "start" / "continue" / "send_message"
	source         string // "loop" / "interactive"
	totalSteps     int
	completedCount int
	failedCount    int
	hasResume      bool
	priorStatus    model.JobStatus // empty for loop runs; set for send_message
	scheduleID     string
	timeoutMinutes int
}

// logLifecycleStart emits the symmetric "run starting" line so operators
// can correlate every terminal log with the originating Start / Continue /
// SendMessage call. Scheduled-task runs are logged at DEBUG to reduce noise;
// manual/interactive runs stay at INFO.
func logLifecycleStart(ctx context.Context, jobID string, sc lifecycleStartContext) {
	scheduleID := sc.scheduleID
	if scheduleID == "" {
		scheduleID = "-"
	}
	logFn := logger.Infof
	if sc.scheduleID != "" {
		logFn = logger.Debugf
	}
	logFn(ctx, "[job.lifecycle] run starting: jobId=%s action=%s source=%s totalSteps=%d completed=%d failed=%d hasResume=%t priorStatus=%s scheduleId=%s timeoutMin=%d",
		jobID, sc.action, sc.source, sc.totalSteps, sc.completedCount, sc.failedCount, sc.hasResume, statusLogValue(sc.priorStatus), scheduleID, sc.timeoutMinutes)
}

func lifecycleActionVerb(action string) string {
	switch action {
	case "finish":
		return "finished"
	case "stop":
		return "stopped"
	case "fail":
		return "failed"
	default:
		return "terminal"
	}
}

func statusLogValue(status model.JobStatus) model.JobStatus {
	if status == "" {
		return "-"
	}
	return status
}

// terminalSnapshot captures the fields needed to publish a terminal event after
// s.mu has been released. captureTerminalSnapshotLocked builds it under the lock
// (and stamps FinishedAt as a side effect when missing); persistAndPublishTerminal
// consumes it without touching s.mu.
type terminalSnapshot struct {
	progress    *model.JobProgress
	terminalAt  int64
	finalStatus model.JobStatus
}

// captureTerminalSnapshotLocked snapshots the terminal-event-relevant fields
// from job and ensures FinishedAt is populated. Caller MUST hold s.mu.
func captureTerminalSnapshotLocked(job *model.Job) terminalSnapshot {
	// Defensive: a malformed Job (e.g. loaded from disk with a corrupted
	// payload) could reach a terminal transition with nil Progress. Avoid a
	// nil-pointer panic here so finishJob / stopJob / failJob can still
	// flip Status off Running.
	if job.Progress == nil {
		job.Progress = &model.JobProgress{}
	}
	progressSnap := *job.Progress
	terminalAt := job.FinishedAt
	if terminalAt <= 0 {
		terminalAt = nowMillis()
		job.FinishedAt = terminalAt
	}
	return terminalSnapshot{
		progress:    &progressSnap,
		terminalAt:  terminalAt,
		finalStatus: job.Status,
	}
}

// persistAndPublishTerminal finishes the shared terminal-transition postamble:
// persist the job, stamp a breadcrumb on persistence failure, and publish the
// matching SSE event. Caller MUST have released s.mu before calling.
func (s *serviceImpl) persistAndPublishTerminal(ctx context.Context, job *model.Job, snap terminalSnapshot, action string, failMessage string, runOutcome model.RunOutcome) model.JobStatus {
	// Persist the actual run outcome so snapshot-based recovery (syncJobState)
	// can finalize in-flight UI correctly even when job.Status is a restored
	// prior status (interactive sends on already-terminal jobs).
	job.LastRunOutcome = runOutcome

	progressCopy := snap.progress
	if err := s.saveJobWithRetry(ctx, job, action); err != nil {
		// Record the persistence failure on progress so a subsequent refresh
		// (when save eventually succeeds or during recovery) at least shows why
		// disk diverged from the event stream.
		if s.recordTerminalPersistError(job, err) {
			s.mu.RLock()
			refreshed := *job.Progress
			progressCopy = &refreshed
			s.mu.RUnlock()
			// Best-effort second persistence pass after stamping the breadcrumb.
			// The live terminal event is still published below so an fs hiccup does
			// not strand the UI in Running forever, but a transient first failure can
			// now still persist both terminal state and the diagnostic marker.
			_ = s.saveJobWithRetry(ctx, job, action+"_persist_error")
		}
	}
	s.publishTerminalEvent(job.ID, snap.finalStatus, failMessage, progressCopy, runOutcome, snap.terminalAt)
	return snap.finalStatus
}

// recordTerminalPersistError annotates the in-memory job progress with a
// "terminal persist failed" marker so the next successful save (or a
// server restart that re-reads the previous snapshot) doesn't quietly
// swallow the divergence between in-memory/event state and disk state.
// Logging at ERROR is already done inside saveJobWithRetry; this helper
// is only about leaving a breadcrumb on the job itself so the UI /
// downstream consumers can see the anomaly. It returns true when it actually
// changed the job and the caller may want to retry persistence.
func (s *serviceImpl) recordTerminalPersistError(job *model.Job, err error) bool {
	if err == nil {
		return false
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	// Only stamp LastError if it's empty — a real iteration error is more
	// informative than "terminal persist failed", so don't clobber it.
	if job.Progress.LastError == "" {
		job.Progress.LastError = fmt.Sprintf("terminal persist failed: %v", err)
		return true
	}
	return false
}

// publishTerminalEvent publishes the SSE event matching a job's final
// status. runOutcome carries the actual outcome of the run that just
// ended — for loop runs this equals the final status, but for
// interactive sends on an already-terminal job the final status is the
// restored prior status, while runOutcome reflects what actually
// happened in this send. Frontends that finalise in-flight UI state
// (tool bubbles, streaming bubbles) should key off runOutcome.
func (s *serviceImpl) publishTerminalEvent(jobID string, status model.JobStatus, failMessage string, progress *model.JobProgress, runOutcome model.RunOutcome, terminalAt int64) {
	base := func(t model.EventType) model.BaseEvent {
		return model.BaseEvent{Type: t, JobID: jobID, Timestamp: terminalAt}
	}
	switch status {
	case model.JobStatusCompleted:
		s.Publish(jobID, &model.JobCompletedEvent{
			BaseEvent:  base(model.EventTypeJobCompleted),
			Progress:   progress,
			RunOutcome: runOutcome,
		})
	case model.JobStatusFailed:
		s.Publish(jobID, &model.JobFailedEvent{
			BaseEvent:  base(model.EventTypeJobFailed),
			Message:    failMessage,
			Progress:   progress,
			RunOutcome: runOutcome,
		})
	default:
		s.Publish(jobID, &model.JobStoppedEvent{
			BaseEvent:  base(model.EventTypeJobStopped),
			Progress:   progress,
			RunOutcome: runOutcome,
		})
	}
}

// closePanicRoundIfOpen publishes RUN_ERROR + ITERATION_FAILED to close
// any in-flight buffer round when a panic interrupts a run before
// executeRepeat / executeShellRepeat could publish their own closing
// pair. Without this pair the round stays closed=false; once Continue
// flips terminal off via ResumeGC, gcLocked's `r.closed && minCursor >=
// r.endSeq` predicate is never satisfied and the round's A-class chunks
// leak forever. It also lets the buffer clear openRoundID, so the next
// run's first event is not mis-attributed to the orphan round.
//
// Panic recovery doesn't have direct access to the in-flight step's runID
// or path, so we use Progress.CurrentPath (set by persistIterationStart at
// the top of executeRepeat) and the most recent SessionIDs entry as
// best-effort attribution. Callers must NOT also touch Progress.Results
// from the panic path — interactive runs never write Progress.Results,
// and loop runs would produce a duplicate failure entry on the next
// Continue. publish only.
func (s *serviceImpl) closePanicRoundIfOpen(job *model.Job, panicErr error) {
	buf := s.bus.get(job.ID)
	if buf == nil || !buf.HasOpenRound() {
		return
	}

	s.mu.RLock()
	var path []int
	if job.Progress != nil {
		path = model.CopyPath(job.Progress.CurrentPath)
	}
	sessionID := ""
	if n := len(job.SessionIDs); n > 0 {
		sessionID = job.SessionIDs[n-1]
	}
	s.mu.RUnlock()

	terminalAt := nowMillis()
	s.publishRunOutcome(job.ID, sessionID, path, "", panicErr, terminalAt)
	s.publishIterationEvent(job.ID, &model.IterationResult{
		Path:      path,
		SessionID: sessionID,
		Success:   false,
		Error:     panicErr.Error(),
	})
}

// recordFailedIterationAndAdvanceResume records a failed iteration AND advances
// the resume pointer in a single persist. Used by the tryCreateSession failure
// path so the "skip + advance" sequence is one save instead of two.
func (s *serviceImpl) recordFailedIterationAndAdvanceResume(job *model.Job, path []int, sessionID string, err error, nextResume *model.JobResume) {
	result := model.IterationResult{
		Path:      model.CopyPath(path),
		SessionID: sessionID,
		Success:   false,
		Error:     err.Error(),
	}
	s.appendAndSaveResult(job, result, nextResume, true)
	s.publishIterationEvent(job.ID, &result)
}

// persistIterationStart records the iteration path on job.Progress.CurrentPath
// and persists job.json BEFORE the caller publishes IterationStarted.
// Required by the §1.4 write-order contract: B-class state must reach disk
// before its event is published, so a client that refreshes job.json
// immediately after seeing IterationStarted reads a state at least as fresh
// as the event stream — never the previous round's path.
func (s *serviceImpl) persistIterationStart(ctx context.Context, job *model.Job, path []int) {
	s.mu.Lock()
	job.Progress.CurrentPath = model.CopyPath(path)
	s.mu.Unlock()
	if err := s.saveJobWithRetry(ctx, job, "iteration_started"); err != nil {
		s.recordPersistWarning(ctx, job, "iteration_started", err)
	}
}

// appendAndSaveResult appends an iteration result to the job progress under lock,
// clears old Content fields to free memory, updates counts, optionally advances
// the resume pointer, and persists in a single save.
//
// When advanceResume is true, job.Resume is replaced with nextResume (a nil
// nextResume clears it — used when the just-recorded step was the final one in
// the flow). When advanceResume is false, job.Resume is left untouched and
// nextResume is ignored.
func (s *serviceImpl) appendAndSaveResult(job *model.Job, result model.IterationResult, nextResume *model.JobResume, advanceResume bool) {
	s.mu.Lock()
	// Clear Content from previous in-memory results to free string memory.
	for i := range job.Progress.Results {
		job.Progress.Results[i].Content = ""
	}
	job.Progress.Results = append(job.Progress.Results, result)
	if result.Success {
		job.Progress.CompletedCount++
	} else {
		job.Progress.FailedCount++
		if result.Error != "" {
			job.Progress.LastError = result.Error
		}
	}
	job.Progress.CurrentPath = model.CopyPath(result.Path)
	if advanceResume {
		job.Resume = copyResume(nextResume)
	}
	s.mu.Unlock()
	action := "record_iteration_result"
	if advanceResume {
		action = "record_and_advance_resume"
	}
	ctx := context.Background()
	if err := s.saveJobWithRetry(ctx, job, action); err != nil {
		s.recordPersistWarning(ctx, job, action, err)
	}
}

// publishIterationEvent publishes the appropriate completed/failed event.
func (s *serviceImpl) publishIterationEvent(jobID string, result *model.IterationResult) {
	eventType := model.EventTypeIterationFailed
	if result.Success {
		eventType = model.EventTypeIterationCompleted
	}
	baseEvent := model.BaseEvent{
		Type: eventType, JobID: jobID,
		SessionID: result.SessionID, Path: result.Path,
		Timestamp: nowMillis(),
	}
	if result.Success {
		s.Publish(jobID, &model.IterationCompletedEvent{BaseEvent: baseEvent, Result: result})
	} else {
		s.Publish(jobID, &model.IterationFailedEvent{BaseEvent: baseEvent, Result: result})
	}
}
