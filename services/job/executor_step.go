package job

import (
	"context"
	"errors"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/strutil"
	"github.com/fanlv/quartet/types/model"
)

// stepResult represents the outcome of executing a step.
type stepResult int

const (
	stepCompleted    stepResult = iota // step finished normally
	stepAborted                        // job was interrupted/cancelled/failed — stop everything
	stepStopLoop                       // STOP_LOOP detected — break the innermost group iteration
	stepStopWorkflow                   // STOP_WORKFLOW detected — exit the entire workflow
	stepStopGraceful                   // graceful stop requested — current step finished cleanly; stop at this boundary, preserve resume
)

func (s *serviceImpl) executeRepeat(ctx context.Context, job *model.Job, runner JobRunner, node model.FlowNode, path []int, sessionID string, opts *SendMessageOptions, isLoopRun bool, nextResume *model.JobResume) stepResult {
	isEvaluator := node.RoundType == model.RoundTypeEvaluator
	msg := node.Message
	// Resolve {{var}} placeholders for both prompt and evaluator steps.
	// Control signals (STOP_LOOP / STOP_WORKFLOW) are NOT derived from prompt
	// variables: a STOP must be an explicit, recorded step outcome (a Shell
	// control-file directive or an evaluator's LOOP_DECISION:STOP), never a
	// short-circuit that skips the model run, the IterationStarted/RunStarted
	// events, usage recording and the IterationResult — runFlowNodes counts the
	// leaf as executed regardless, so a silent short-circuit would leave the
	// progress denominator off by one.
	//
	// The one sanctioned exception is the empty-prompt skip (executor_skip.go):
	// a prompt step whose message renders to an empty string is skipped BEFORE
	// this function is reached, with the denominator deducted, the slot recorded
	// in Progress.SkippedPaths and no round opened at all — so neither invariant
	// this comment protects (denominator accuracy, paired round events) is
	// violated. By the time executeRepeat runs, the rendered prompt is known to
	// be non-empty (or the step is an evaluator / interactive send, which never
	// skip).
	if job.LoopConfig != nil {
		msg = s.substituteVars(msg, job)
	}

	// Evaluator step: append the fixed output protocol to the user's evaluation
	// prompt before sending (§2.1/§2.2). The turn renders like any other step;
	// only the final assistant text's last line is parsed afterwards as a stop
	// signal. The published IterationStarted message below shows the full prompt
	// (protocol included) so the chat stream matches what the model received.
	if isEvaluator {
		msg = buildEvaluatorPrompt(msg)
	}

	logger.Debugf(ctx, "[step] run: jobId=%s path=%v msg=%s", job.ID, path, strutil.TruncateRunesWithEllipsis(msg, 200))

	// Per-step agent/model (must be set on each FlowNode)
	iterAgentType := node.AgentType
	iterModelID := node.StepModelID
	iterACPMode := node.ACPMode
	iterACPThoughtLevel := node.ACPThoughtLevel

	iterationStartedAt := s.persistIterationStart(ctx, job, path)

	s.Publish(job.ID, &model.IterationStartedEvent{
		BaseEvent: model.BaseEvent{
			Type: model.EventTypeIterationStarted, JobID: job.ID,
			SessionID: sessionID, Path: path,
			Timestamp: iterationStartedAt,
		},
		Message:         msg,
		ClientMessageID: optsClientMessageID(opts),
		ModelID:         iterModelID,
		AgentType:       iterAgentType,
		ACPMode:         iterACPMode,
		ACPThoughtLevel: iterACPThoughtLevel,
	})

	handler := newLoopEventHandler(ctx, job.ID, sessionID, path, s)
	// Interactive send (isLoopRun=false) treats RUN_STARTED as the run's
	// semantic boundary used by the UI for per-round duration. It MUST share
	// the same clock read as the persisted job.StartedAt to keep live vs reload
	// consistent.
	runStartedAt := s.nowMillis()
	if !isLoopRun && job.StartedAt > 0 {
		runStartedAt = job.StartedAt
	}
	s.Publish(job.ID, &model.RunStartedEvent{
		BaseEvent: model.BaseEvent{
			Type: model.EventTypeRunStarted, JobID: job.ID,
			SessionID: sessionID, RunID: handler.runID,
			Path:      path,
			Timestamp: runStartedAt,
		},
	})

	messages := opts.getMessages()
	if messages == nil {
		messages = []*schema.Message{schema.UserMessage(msg)}
	}

	// CONTRACT: RunIteration must not return until messages.jsonl has been
	// flushed for this round. The §2.4 "先写 messages.jsonl，再发轮次结束
	// 事件" invariant — and therefore A-class GC safety — depends on this:
	// publishIterationEvent below fires the round-end event that lets the
	// buffer reclaim the round's chunks. If a future agent framework moves
	// jsonl write to an async path, A-class GC could reclaim chunks before
	// they appear in messages.jsonl, leaving reconnecting clients blind.
	handler, start, duration, err := s.runIterationWithRetries(ctx, job.ID, sessionID, path, runner, messages, handler, isLoopRun)

	logger.Debugf(ctx, "[step] iter done: jobId=%s path=%v session=%s durMs=%d", job.ID, path, sessionID, duration.Milliseconds())
	// Resolve usage attribution from the session after the run. FlowNode.ModelID
	// can contain a backfilled default (not an explicit override), while the
	// session is the source of truth for the model that actually executed — this
	// also covers pause → interactive message switches → Continue.
	stepModelID := resolveUsageSessionModelID(runner, sessionID)
	fin := repeatFinalization{
		ctx:         ctx,
		job:         job,
		path:        path,
		sessionID:   sessionID,
		handler:     handler,
		start:       start,
		duration:    duration,
		err:         err,
		stepModelID: stepModelID,
	}
	if isInterruptedRun(err) {
		return s.finalizeInterruptedRepeat(fin)
	}
	return s.finalizeRepeat(fin, isLoopRun, node, nextResume)
}

type repeatFinalization struct {
	ctx         context.Context
	job         *model.Job
	path        []int
	sessionID   string
	handler     *loopEventHandler
	start       time.Time
	duration    time.Duration
	err         error
	stepModelID string
}

func (s *serviceImpl) finalizeInterruptedRepeat(fin repeatFinalization) stepResult {
	// err distinguishes user Stop (context.Canceled) from job-level
	// deadline (context.DeadlineExceeded) — both fall under "interrupted"
	// but the operator cause is different and the bare log without err
	// can't tell them apart.
	logger.Debugf(fin.ctx, "[step] iter interrupted: jobId=%s path=%v session=%s durMs=%d err=%v", fin.job.ID, fin.path, fin.sessionID, fin.duration.Milliseconds(), fin.err)
	// Stop / cancel still consumed wall-clock and tokens — record what
	// the accumulator captured so usage stats reflect reality (spec
	// 01-data-model: "Completed / Failed / Stopped 都计入").
	interruptedAt := fin.start.Add(fin.duration).UnixMilli()
	s.recordUsageSnapshot(fin.job, fin.handler, fin.stepModelID, interruptedAt, fin.duration.Milliseconds())
	// Close the buffer round even on cancel: RUN_STARTED + ITERATION_STARTED
	// were already published, so the buffer's openRoundID points at this
	// round. Without the closing pair, ResumeGC on Continue cannot reclaim
	// the round's A-class chunks (gc condition is round.closed && cursor
	// >= endSeq, which never holds when no end event arrives). Publish
	// only — do not record into Progress.Results so Continue can still
	// re-run this step at the same path.
	s.publishRunOutcome(fin.job.ID, fin.sessionID, fin.path, fin.handler.runID, fin.err, interruptedAt)
	// For user-initiated stop (context.Canceled), don't report as error.
	iterError := ""
	if !errors.Is(fin.err, context.Canceled) {
		iterError = fin.err.Error()
	}
	s.publishIterationEvent(fin.job.ID, &model.IterationResult{
		Path:       model.CopyPath(fin.path),
		SessionID:  fin.sessionID,
		Success:    false,
		DurationMs: fin.duration.Milliseconds(),
		Tokens:     fin.handler.tokens,
		Content:    fin.handler.AccumulatedContent(),
		Error:      iterError,
	})
	return stepAborted
}

func (s *serviceImpl) finalizeRepeat(fin repeatFinalization, isLoopRun bool, node model.FlowNode, nextResume *model.JobResume) stepResult {
	runFinishedAt := int64(0)
	if !isLoopRun {
		runFinishedAt = s.nowMillis()
		s.mu.Lock()
		fin.job.FinishedAt = runFinishedAt
		s.mu.Unlock()
	}

	s.publishRunOutcome(fin.job.ID, fin.sessionID, fin.path, fin.handler.runID, fin.err, runFinishedAt)

	// Record per-step usage stats. For interactive runs the run-finished
	// timestamp doubles as the step finalize moment; for loop iterations
	// we derive it from start+duration so it matches the iteration result.
	stepFinishedAt := runFinishedAt
	if stepFinishedAt == 0 {
		stepFinishedAt = fin.start.Add(fin.duration).UnixMilli()
	}
	s.recordUsageSnapshot(fin.job, fin.handler, fin.stepModelID, stepFinishedAt, fin.duration.Milliseconds())

	// NOTE: <<SET_VAR:key=value>> extraction is only done in shell steps
	// (executeShellRepeat). AI responses may mention these patterns in
	// discussion, so we skip extraction here to avoid false positives.

	result := &model.IterationResult{
		Path:       model.CopyPath(fin.path),
		SessionID:  fin.sessionID,
		Success:    fin.err == nil,
		DurationMs: fin.duration.Milliseconds(),
		Tokens:     fin.handler.tokens,
		Content:    fin.handler.AccumulatedContent(),
	}
	if fin.err != nil {
		result.Error = fin.err.Error()
	}

	if !isLoopRun {
		return s.finalizeInteractiveRepeat(fin, result)
	}
	return s.finalizeLoopRepeat(fin, result, node, nextResume)
}

func (s *serviceImpl) finalizeInteractiveRepeat(fin repeatFinalization, result *model.IterationResult) stepResult {
	// Interactive run: publish the round-end event so the buffer can release
	// this round's A-class chunks once cursors cross. Progress.Results is loop
	// bookkeeping and an interactive send must not touch it.
	s.publishIterationEvent(fin.job.ID, result)
	// On failure, drive the terminal state through failJob rather than falling
	// through to finishJob. Without this, an interactive send on a job with no
	// prior terminal status would be finished as Completed even though the run
	// errored. failJob already restores the prior terminal status when one was
	// recorded, so Completed/Failed/Stopped loops continue to be preserved.
	if fin.err != nil {
		s.failJob(fin.ctx, fin.job, fin.err.Error(), false, false)
		return stepAborted
	}
	return stepCompleted
}

func (s *serviceImpl) finalizeLoopRepeat(fin repeatFinalization, result *model.IterationResult, node model.FlowNode, nextResume *model.JobResume) stepResult {
	if fin.err != nil {
		// Failure path: plain record. The failJob call below issues the
		// next persist (terminal status), so combining record + resume
		// here would produce no savings.
		s.recordIterationResult(fin.ctx, fin.job, result)

		// At this point non-loop runs have already returned above. In loop mode,
		// mark the job as Failed so the terminal status reflects the actual
		// outcome. Resume is preserved so the user can Continue and retry from
		// this step.
		// duration disambiguates "all models instantly returned errors"
		// (~ms — upstream-wide outage) from "a model hung for the full
		// timeout" (likely a single slow model + retry chain). Without
		// it the err string alone can't tell you which: both render as
		// "stream all models failed".
		errKind := "non-transient"
		if isTransientNetworkError(fin.err) {
			errKind = "transient (retries exhausted)"
		} else if isRateLimitError(fin.err) {
			errKind = "rate-limit (retries exhausted)"
		}
		logger.Errorf(fin.ctx, "[step] iter failed, failing job: jobId=%s path=%v duration=%s errKind=%s err=%v", fin.job.ID, fin.path, fin.duration.Round(time.Millisecond), errKind, fin.err)
		s.failJob(fin.ctx, fin.job, fin.err.Error(), true, true)
		return stepAborted
	}

	// Success. Decide the evaluator's STOP signal BEFORE persisting so the
	// resume pointer is written exactly once with the correct target. If we
	// always advanced the plain nextResume here and let the caller's
	// advanceResumePastGroup re-correct it on a STOP, a crash in that window
	// would leave a persisted resume pointing back into the group (re-running
	// a step the STOP meant to skip). Evaluator step (§2.1/§4): once the final
	// assistant text, after case-folding and whitespace removal, ends with
	// LOOP_DECISION:STOP, we break the enclosing group (same semantics as a
	// Shell STOP_LOOP); any other output continues. The evaluator is a real,
	// counted step either way.
	evaluatorStop := evaluatorStopRequested(node, result.Content)
	if evaluatorStop {
		// STOP: record the result but DON'T advance the plain resume — the
		// caller's group-early-exit logic owns the only resume write (past the
		// whole group), keeping persistence single-writer.
		//
		// This is two saves (record here, advance in advanceResumePastGroup),
		// not one atomic write, so a crash in the gap leaves resume still
		// pointing at this evaluator step. That is self-healing rather than
		// unsafe: on Continue, resumeForContinue returns that resume, Continue
		// strips the stale result at the resume path, and the evaluator re-runs
		// and re-emits STOP — converging on the same "advance past the group"
		// outcome without double-counting. The cost is at most one replayed
		// evaluator step, never a corrupted cursor.
		s.recordIterationResult(fin.ctx, fin.job, result)
		logger.Infof(fin.ctx, "[step] evaluator STOP: jobId=%s path=%v", fin.job.ID, fin.path)
		return stepStopLoop
	}
	// Continue: record result and advance resume in a single save.
	s.recordIterationAndAdvanceResume(fin.ctx, fin.job, result, nextResume)
	if node.RoundType == model.RoundTypeEvaluator {
		logger.Debugf(fin.ctx, "[step] evaluator continue: jobId=%s path=%v", fin.job.ID, fin.path)
	}

	// NOTE: STOP_LOOP / STOP_WORKFLOW markers are only honoured in shell steps
	// (executeShellRepeat) and evaluator steps (above). Ordinary AI prompt
	// responses may mention these markers in discussion or code review, so we
	// intentionally skip detection here to avoid false-positive termination.

	return stepCompleted
}

func evaluatorStopRequested(node model.FlowNode, content string) bool {
	return node.RoundType == model.RoundTypeEvaluator && parseEvaluatorDecision(content)
}

func resolveUsageSessionModelID(runner JobRunner, sessionID string) string {
	if runner == nil || sessionID == "" {
		return ""
	}
	return runner.SessionModelID(sessionID)
}

func optsClientMessageID(opts *SendMessageOptions) string {
	if opts == nil {
		return ""
	}
	return opts.ClientMessageID
}
