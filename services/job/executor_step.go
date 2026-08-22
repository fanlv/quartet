package job

import (
	"context"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/strutil"
	"github.com/fanlv/quartet/types/model"
)

func (s *serviceImpl) executeRepeat(ctx context.Context, job *model.Job, runner JobRunner, msg string, sessionID string, opts *SendMessageOptions) {
	logger.Debugf(ctx, "[step] run: jobId=%s msg=%s", job.ID, strutil.TruncateRunesWithEllipsis(msg, 200))

	handler := newLoopEventHandler(ctx, job.ID, sessionID, s)
	clientMessageID := ""
	if opts != nil {
		clientMessageID = opts.ClientMessageID
	}
	// An interactive send treats RUN_STARTED as the run's semantic boundary
	// used by the UI for per-round duration. It MUST share the same clock read
	// as the persisted job.StartedAt to keep live vs reload consistent.
	runStartedAt := s.nowMillis()
	if job.StartedAt > 0 {
		runStartedAt = job.StartedAt
	}
	s.Publish(job.ID, &model.RunStartedEvent{
		BaseEvent: model.BaseEvent{
			Type: model.EventTypeRunStarted, JobID: job.ID,
			SessionID: sessionID, RunID: handler.runID,
			Timestamp: runStartedAt,
		},
		ClientMessageID: clientMessageID,
	})

	messages := opts.getMessages()
	if messages == nil {
		messages = []*schema.Message{schema.UserMessage(msg)}
	}

	// CONTRACT: RunIteration must not return until messages.jsonl has been
	// flushed for this round. The §2.4 "先写 messages.jsonl，再发轮次结束
	// 事件" invariant — and therefore A-class GC safety — depends on this:
	// publishRunOutcome below fires the round-end event that lets the
	// buffer reclaim the round's chunks. If a future agent framework moves
	// jsonl write to an async path, A-class GC could reclaim chunks before
	// they appear in messages.jsonl, leaving reconnecting clients blind.
	start := time.Now()
	err := runner.RunIteration(ctx, sessionID, messages, handler)
	duration := time.Since(start)

	logger.Debugf(ctx, "[step] iter done: jobId=%s session=%s durMs=%d", job.ID, sessionID, duration.Milliseconds())
	// Resolve usage attribution from the session after the run: the session is
	// the source of truth for the model that actually executed.
	stepModelID := resolveUsageSessionModelID(runner, sessionID)
	fin := repeatFinalization{
		ctx:         ctx,
		job:         job,
		sessionID:   sessionID,
		handler:     handler,
		start:       start,
		duration:    duration,
		err:         err,
		stepModelID: stepModelID,
	}
	if isInterruptedRun(err) {
		s.finalizeInterruptedRepeat(fin)
		return
	}
	s.finalizeRepeat(fin)
}

type repeatFinalization struct {
	ctx         context.Context
	job         *model.Job
	sessionID   string
	handler     *loopEventHandler
	start       time.Time
	duration    time.Duration
	err         error
	stepModelID string
}

func (s *serviceImpl) finalizeInterruptedRepeat(fin repeatFinalization) {
	// err distinguishes user Stop (context.Canceled) from job-level
	// deadline (context.DeadlineExceeded) — both fall under "interrupted"
	// but the operator cause is different and the bare log without err
	// can't tell them apart.
	logger.Debugf(fin.ctx, "[step] iter interrupted: jobId=%s session=%s durMs=%d err=%v", fin.job.ID, fin.sessionID, fin.duration.Milliseconds(), fin.err)
	// Stop / cancel still consumed wall-clock and tokens — record what
	// the accumulator captured so usage stats reflect reality (spec
	// 01-data-model: "Completed / Failed / Stopped 都计入").
	interruptedAt := fin.start.Add(fin.duration).UnixMilli()
	s.recordUsageSnapshot(fin.job, fin.handler, fin.stepModelID, interruptedAt, fin.duration.Milliseconds())
	// Close the buffer round even on cancel: RUN_STARTED was already
	// published, so the buffer's openRoundID points at this round. Without
	// the closing event, ResumeGC on the next SendMessage cannot reclaim the
	// round's A-class chunks (gc condition is round.closed && cursor >=
	// endSeq, which never holds when no end event arrives). Publish only —
	// do not record into Progress.Results (interactive runs never touch it).
	s.publishRunOutcome(fin.job.ID, fin.sessionID, fin.handler.runID, fin.err, interruptedAt)
}

func (s *serviceImpl) finalizeRepeat(fin repeatFinalization) {
	runFinishedAt := s.nowMillis()
	s.mu.Lock()
	fin.job.FinishedAt = runFinishedAt
	s.mu.Unlock()

	s.publishRunOutcome(fin.job.ID, fin.sessionID, fin.handler.runID, fin.err, runFinishedAt)

	// Record per-turn usage stats; the run-finished timestamp doubles as the
	// finalize moment.
	s.recordUsageSnapshot(fin.job, fin.handler, fin.stepModelID, runFinishedAt, fin.duration.Milliseconds())

	// On failure, drive the terminal state through failJob rather than falling
	// through to finishJob. Without this, an interactive send on a job with no
	// prior terminal status would be finished as Completed even though the run
	// errored. failJob already restores the prior terminal status when one was
	// recorded, so Completed/Failed/Stopped jobs continue to be preserved.
	if fin.err != nil {
		s.failJob(fin.ctx, fin.job, fin.err.Error())
	}
}

func resolveUsageSessionModelID(runner JobRunner, sessionID string) string {
	if runner == nil || sessionID == "" {
		return ""
	}
	return runner.SessionModelID(sessionID)
}
