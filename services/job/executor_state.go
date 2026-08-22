package job

import (
	"context"
	"errors"
	"fmt"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
)

func (s *serviceImpl) publishRunOutcome(jobID, sessionID string, runID string, err error, terminalAt int64) {
	if terminalAt <= 0 {
		terminalAt = s.nowMillis()
	}

	// User-initiated stop (context.Canceled) is not an error — publish
	// RunFinished so the frontend doesn't show a spurious error toast.
	if err == nil || errors.Is(err, context.Canceled) {
		s.Publish(jobID, &model.RunFinishedEvent{
			BaseEvent: model.BaseEvent{
				Type: model.EventTypeRunFinished, JobID: jobID,
				SessionID: sessionID, RunID: runID,
				Timestamp: terminalAt,
			},
		})
		return
	}

	s.Publish(jobID, &model.RunErrorEvent{
		BaseEvent: model.BaseEvent{
			Type: model.EventTypeRunError, JobID: jobID,
			SessionID: sessionID, RunID: runID,
			Timestamp: terminalAt,
		},
		Message: err.Error(),
		Code:    classifyRunErrorCode(err),
	})
}

func (s *serviceImpl) finishJob(ctx context.Context, job *model.Job) {
	s.mu.Lock()
	resolution := s.applyTerminalStatusLocked(job, model.JobStatusCompleted, true)
	snap := captureTerminalSnapshotLocked(job, model.RunOutcomeCompleted, s.nowMillis)
	s.mu.Unlock()
	// Publish the terminal event that matches the final status; runOutcome
	// records that *this* run (the interactive send) actually
	// completed successfully, regardless of whether finalStatus is a
	// restored prior status.
	finalStatus := s.persistAndPublishTerminal(ctx, job, snap, jobRunActionFinish, "", model.RunOutcomeCompleted)
	logLifecycleTerminal(ctx, job.ID, jobRunActionFinish, finalStatus, model.RunOutcomeCompleted, resolution, "")
}

func (s *serviceImpl) clearCancel(jobID string, entry *cancelEntry) {
	// Always release the context resources owned by this run. CancelFunc is
	// idempotent, so this is safe if Stop / StopAll already cancelled it. Keep
	// the map deletion guarded by entry identity so an old run cannot remove a
	// newer run's cancel registered by a rapid Stop+SendMessage.
	entry.cancel()

	s.cancelMu.Lock()
	// Only remove if the map still points to our entry; a rapid Stop+SendMessage
	// can register a new run's cancel before the old run's deferred cleanup runs.
	if s.cancels[jobID] == entry {
		delete(s.cancels, jobID)
	}
	s.cancelMu.Unlock()
}

func (s *serviceImpl) stopJob(ctx context.Context, job *model.Job) {
	s.mu.Lock()
	resolution := s.applyTerminalStatusLocked(job, model.JobStatusStopped, false)
	snap := captureTerminalSnapshotLocked(job, model.RunOutcomeStopped, s.nowMillis)
	s.mu.Unlock()
	finalStatus := s.persistAndPublishTerminal(ctx, job, snap, jobRunActionStop, "", model.RunOutcomeStopped)
	logLifecycleTerminal(ctx, job.ID, jobRunActionStop, finalStatus, model.RunOutcomeStopped, resolution, "")
}

// failJob marks a job as failed and publishes the matching terminal event.
//
// The failure is recorded with Resume cleared: interactive runs never carry a
// resumable cursor, and the prior-status / paused-run branches of
// applyTerminalStatusLocked return before Resume would be touched, so
// historical (legacy loop) Resume data survives an interactive failure.
func (s *serviceImpl) failJob(ctx context.Context, job *model.Job, message string) {
	s.mu.Lock()
	resolution := s.applyTerminalStatusLocked(job, model.JobStatusFailed, true)
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
	snap := captureTerminalSnapshotLocked(job, model.RunOutcomeFailed, s.nowMillis)
	s.mu.Unlock()
	finalStatus := s.persistAndPublishTerminal(ctx, job, snap, jobRunActionFail, message, model.RunOutcomeFailed)
	logLifecycleTerminal(ctx, job.ID, jobRunActionFail, finalStatus, model.RunOutcomeFailed, resolution, message)
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
// Resolution order:
//  1. A stored prior status recorded by SendMessage → restore it; leave Resume
//     untouched. For non-graph jobs this only fires for a terminal prior
//     (Completed/Failed/Stopped); for graph jobs it fires for ANY prior, because
//     a graph job's status is owned by the graph run lifecycle and an
//     interactive discussion turn must never write it.
//  2. job.Resume != nil (paused run, legacy loop job)
//     → JobStatusStopped;
//     leave Resume untouched.
//  3. otherwise                  → targetStatus, clearing Resume iff clearResume.
func (s *serviceImpl) applyTerminalStatusLocked(job *model.Job, targetStatus model.JobStatus, clearResume bool) terminalStatusResolution {
	resolution := terminalStatusResolution{
		targetStatus:          targetStatus,
		statusReason:          "target_status",
		resumePresentAtFinish: job.Resume != nil,
	}
	if prior, ok := s.consumeInteractivePriorStatusLocked(job.ID); ok && (isTerminalJobStatus(prior) || job.Mode == model.JobModeGraph) {
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
	job.Status = targetStatus
	if clearResume {
		job.Resume = nil
		resolution.resumeCleared = resolution.resumePresentAtFinish
	}
	return resolution
}

func logLifecycleTerminal(ctx context.Context, jobID, action string, finalStatus model.JobStatus, runOutcome model.RunOutcome, resolution terminalStatusResolution, message string) {
	verb := lifecycleActionVerb(action)
	// Interactive run terminals stay at INFO so operators can always correlate
	// the "run starting" / "run finished" pair without switching to DEBUG.
	if message != "" {
		logger.Infof(ctx, "[job.lifecycle] run %s: jobId=%s action=%s targetStatus=%s finalStatus=%s runOutcome=%s statusReason=%s priorStatus=%s restoredPriorStatus=%t resumePresentAtFinish=%t resumeCleared=%t err=%q",
			verb, jobID, action, resolution.targetStatus, finalStatus, runOutcome, resolution.statusReason, statusLogValue(resolution.priorStatus), resolution.restoredPriorStatus, resolution.resumePresentAtFinish, resolution.resumeCleared, message)
		return
	}
	logger.Infof(ctx, "[job.lifecycle] run %s: jobId=%s action=%s targetStatus=%s finalStatus=%s runOutcome=%s statusReason=%s priorStatus=%s restoredPriorStatus=%t resumePresentAtFinish=%t resumeCleared=%t",
		verb, jobID, action, resolution.targetStatus, finalStatus, runOutcome, resolution.statusReason, statusLogValue(resolution.priorStatus), resolution.restoredPriorStatus, resolution.resumePresentAtFinish, resolution.resumeCleared)
}

// lifecycleStartContext carries the snapshot needed to emit the run-start
// counterpart to logLifecycleTerminal. Captured under s.mu in the caller so
// the log line reflects the exact state that the goroutine is about to run on.
type lifecycleStartContext struct {
	action      string // "send_message"
	hasResume   bool
	priorStatus model.JobStatus
	scheduleID  string
}

// logLifecycleStart emits the symmetric "run starting" line so operators
// can correlate every terminal log with the originating SendMessage call.
// Scheduled-task runs are logged at DEBUG to reduce noise;
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
	logFn(ctx, "[job.lifecycle] run starting: jobId=%s action=%s hasResume=%t priorStatus=%s scheduleId=%s",
		jobID, sc.action, sc.hasResume, statusLogValue(sc.priorStatus), scheduleID)
}

func lifecycleActionVerb(action string) string {
	switch action {
	case jobRunActionFinish:
		return "finished"
	case jobRunActionStop:
		return "stopped"
	case jobRunActionFail:
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
	terminalAt  int64
	finalStatus model.JobStatus
}

// captureTerminalSnapshotLocked snapshots the terminal-event-relevant fields
// from job and ensures FinishedAt is populated. It also records runOutcome onto
// job.LastRunOutcome here, under the lock, so the write is ordered with the
// concurrent locked readers (Get / DeepCopy / saveJobWithRetry) that share the
// same Job pointer — persistAndPublishTerminal runs after s.mu is released and
// must not touch the field itself. Caller MUST hold s.mu.
func captureTerminalSnapshotLocked(job *model.Job, runOutcome model.RunOutcome, nowMillis func() int64) terminalSnapshot {
	// Defensive: a malformed Job (e.g. loaded from disk with a corrupted
	// payload) could reach a terminal transition with nil Progress. Avoid a
	// nil-pointer panic here so finishJob / stopJob / failJob can still
	// flip Status off Running.
	if job.Progress == nil {
		job.Progress = &model.JobProgress{}
	}
	// Persist the actual run outcome so snapshot-based recovery (syncJobState)
	// can finalize in-flight UI correctly even when job.Status is a restored
	// prior status (interactive sends on already-terminal jobs).
	job.LastRunOutcome = runOutcome
	terminalAt := job.FinishedAt
	if terminalAt <= 0 {
		terminalAt = nowMillis()
		job.FinishedAt = terminalAt
	}
	finishActiveClientMessageLocked(job, runOutcome, terminalAt)
	return terminalSnapshot{
		terminalAt:  terminalAt,
		finalStatus: job.Status,
	}
}

func finishActiveClientMessageLocked(job *model.Job, runOutcome model.RunOutcome, finishedAt int64) {
	clientMessageID := job.ActiveClientMessageID
	if clientMessageID == "" {
		return
	}
	receipt, ok := job.ClientMessageReceipts[clientMessageID]
	if !ok {
		job.ActiveClientMessageID = ""
		return
	}
	switch runOutcome {
	case model.RunOutcomeCompleted:
		receipt.State = model.ClientMessageStateCompleted
	case model.RunOutcomeStopped:
		receipt.State = model.ClientMessageStateStopped
	case model.RunOutcomeFailed:
		receipt.State = model.ClientMessageStateFailed
	default:
		return
	}
	receipt.FinishedAt = finishedAt
	job.ClientMessageReceipts[clientMessageID] = receipt
	job.ActiveClientMessageID = ""
}

// persistAndPublishTerminal finishes the shared terminal-transition postamble:
// persist the job, stamp a breadcrumb on persistence failure, and publish the
// matching SSE event. Caller MUST have released s.mu before calling.
func (s *serviceImpl) persistAndPublishTerminal(ctx context.Context, job *model.Job, snap terminalSnapshot, action string, failMessage string, runOutcome model.RunOutcome) model.JobStatus {
	// job.LastRunOutcome was already stamped under s.mu by
	// captureTerminalSnapshotLocked — don't write it here (this runs after the
	// lock is released and would race the locked readers).
	if err := s.saveJobWithRetry(ctx, job, action); err != nil {
		// Record the persistence failure on progress so a subsequent refresh
		// (when save eventually succeeds or during recovery) at least shows why
		// disk diverged from the event stream.
		if s.recordTerminalPersistError(job, err) {
			// Best-effort second persistence pass after stamping the breadcrumb.
			// The live terminal event is still published below so an fs hiccup does
			// not strand the UI in Running forever, but a transient first failure can
			// now still persist both terminal state and the diagnostic marker.
			_ = s.saveJobWithRetry(ctx, job, action+"_persist_error")
		}
	}
	s.publishTerminalEvent(job.ID, snap.finalStatus, failMessage, runOutcome, snap.terminalAt)
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
// ended — for interactive sends on an already-terminal job the final status
// is the restored prior status, while runOutcome reflects what actually
// happened in this send. Frontends that finalise in-flight UI state
// (tool bubbles, streaming bubbles) should key off runOutcome.
func (s *serviceImpl) publishTerminalEvent(jobID string, status model.JobStatus, failMessage string, runOutcome model.RunOutcome, terminalAt int64) {
	base := func(t model.EventType) model.BaseEvent {
		return model.BaseEvent{Type: t, JobID: jobID, Timestamp: terminalAt}
	}
	switch status {
	case model.JobStatusCompleted:
		s.Publish(jobID, &model.JobCompletedEvent{
			BaseEvent:  base(model.EventTypeJobCompleted),
			RunOutcome: runOutcome,
		})
	case model.JobStatusFailed:
		s.Publish(jobID, &model.JobFailedEvent{
			BaseEvent:  base(model.EventTypeJobFailed),
			Message:    failMessage,
			RunOutcome: runOutcome,
		})
	default:
		s.Publish(jobID, &model.JobStoppedEvent{
			BaseEvent:  base(model.EventTypeJobStopped),
			RunOutcome: runOutcome,
		})
	}
}

// closePanicRoundIfOpen publishes RUN_ERROR to close any in-flight buffer
// round when a panic interrupts a run before executeRepeat could publish its
// own closing event. Without it the round stays closed=false; once the next
// SendMessage flips terminal off via ResumeGC, gcLocked's `r.closed &&
// minCursor >= r.endSeq` predicate is never satisfied and the round's
// A-class chunks leak forever. It also lets the buffer clear openRoundID, so
// the next run's first event is not mis-attributed to the orphan round.
//
// Panic recovery doesn't have direct access to the in-flight run's runID, so
// it uses the most recent SessionIDs entry as best-effort attribution.
// Callers must NOT also touch Progress.Results from the panic path —
// interactive runs never write Progress.Results. publish only.
func (s *serviceImpl) closePanicRoundIfOpen(job *model.Job, panicErr error) {
	buf := s.bus.get(job.ID)
	if buf == nil || !buf.HasOpenRound() {
		return
	}

	s.mu.RLock()
	sessionID := ""
	if n := len(job.SessionIDs); n > 0 {
		sessionID = job.SessionIDs[n-1]
	}
	s.mu.RUnlock()

	s.publishRunOutcome(job.ID, sessionID, "", panicErr, s.nowMillis())
}
