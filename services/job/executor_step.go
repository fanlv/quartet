package job

import (
	"context"
	"errors"
	"net"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/strutil"
	"github.com/fanlv/quartet/services/usagestats"
	"github.com/fanlv/quartet/types/consts"
	"github.com/fanlv/quartet/types/model"
)

// loopTransientRetries is the maximum number of automatic retries for transient
// errors (network resets, HTTP/2 stream errors) before failing the loop job.
const loopTransientRetries = 2

// loopTransientRetryDelay is the backoff between transient retries.
const loopTransientRetryDelay = 3 * time.Second

// loopRateLimitRetries is the maximum number of retries for rate-limit/quota errors.
// Higher than transient retries because the provider explicitly tells us to wait.
const loopRateLimitRetries = 3

// loopRateLimitBaseDelay is the initial backoff for rate-limit retries.
// Each retry doubles the delay (exponential backoff).
const loopRateLimitBaseDelay = 30 * time.Second

// isTransientNetworkError returns true if the error looks like a temporary
// network issue that is likely to succeed on retry (HTTP/2 stream resets,
// connection resets, DNS temporary failures, etc.).
func isTransientNetworkError(err error) bool {
	if err == nil {
		return false
	}
	// net.Error with Temporary() or Timeout()
	var netErr net.Error
	if errors.As(err, &netErr) {
		if netErr.Timeout() {
			return true
		}
	}
	// String-based heuristics for errors that don't implement net.Error
	// (e.g. wrapped http2 stream errors from upstream).
	msg := err.Error()
	transientPatterns := []string{
		"INTERNAL_ERROR",           // HTTP/2 stream reset
		"stream error",             // HTTP/2 stream error
		"connection reset by peer", // TCP RST
		"broken pipe",              // write to closed connection
		"GOAWAY",                   // HTTP/2 GOAWAY
		"EOF",                      // unexpected connection close
		"TLS handshake timeout",
		"i/o timeout",
		"no such host", // transient DNS (debatable, but safer to retry)
		"connection refused",
	}
	for _, p := range transientPatterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	// Subprocess spawn failures (fork/exec) due to transient resource
	// exhaustion (fd limit, process limit, NFS stale handle) should be
	// retried rather than immediately killing the job.
	if isTransientProcessError(err) {
		return true
	}
	return false
}

// isTransientProcessError returns true if the error looks like a subprocess
// spawn failure that may be caused by transient resource exhaustion (e.g.
// too many open files, process limit reached, temporary filesystem issues).
// These errors are distinct from permanent failures like "permission denied"
// or "no such file or directory".
func isTransientProcessError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	// fork/exec failures with "invalid argument", "resource temporarily
	// unavailable", "too many open files", or "cannot allocate memory"
	// are typically transient resource-exhaustion issues.
	if !strings.Contains(msg, "fork/exec") {
		return false
	}
	transientExecPatterns := []string{
		"invalid argument",                 // often caused by fd/process exhaustion
		"resource temporarily unavailable", // EAGAIN
		"too many open files",              // EMFILE/ENFILE
		"cannot allocate memory",           // ENOMEM
		"text file busy",                   // binary being replaced
	}
	for _, p := range transientExecPatterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

// isRateLimitError returns true if the error indicates the upstream provider
// hit a rate limit or usage quota that will recover after a cooldown period.
// These errors are distinct from transient network errors: the request did
// reach the server, but the server is refusing to process it temporarily.
func isRateLimitError(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	rateLimitPatterns := []string{
		"usage_limit_exceeded",
		"rate_limit_exceeded",
		"rate_limit",
		"quota_exceeded",
		"too many requests",
		"Too Many Requests",
		"resource_exhausted",
		"tokens_exceeded",
		"status 429",
		"status_code: 429",
		"StatusCode: 429",
		"HTTP 429",
		"code 429",
		"status=429",
	}
	for _, p := range rateLimitPatterns {
		if strings.Contains(msg, p) {
			return true
		}
	}
	return false
}

// retryAfterRe matches machine-oriented retry hints like "retry after 60", "retry_after: 120", or "Retry-After: 30".
var retryAfterRe = regexp.MustCompile(`(?i)retry[-_ ]?after[: ]*(\d+)`)

// parseRetryAfter attempts to extract a retry-after duration hint from error
// messages like "retry after 60s" or "retry_after: 120". Returns 0 if no
// parseable hint is found. Note: human-readable hints like "try again at
// 10:40 PM" are NOT parsed — the caller falls back to default backoff.
func parseRetryAfter(err error) time.Duration {
	if err == nil {
		return 0
	}
	msg := err.Error()
	if matches := retryAfterRe.FindStringSubmatch(msg); len(matches) >= 2 {
		if secs, e := strconv.Atoi(matches[1]); e == nil {
			return time.Duration(secs) * time.Second
		}
	}
	return 0
}

func isInterruptedRun(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}

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

	s.persistIterationStart(ctx, job, path)

	s.Publish(job.ID, &model.IterationStartedEvent{
		BaseEvent: model.BaseEvent{
			Type: model.EventTypeIterationStarted, JobID: job.ID,
			SessionID: sessionID, Path: path,
			Timestamp: nowMillis(),
		},
		Message:         msg,
		ClientMessageID: optsClientMessageID(opts),
		ModelID:         iterModelID,
		AgentType:       iterAgentType,
		ACPMode:         iterACPMode,
	})

	handler := newLoopEventHandler(ctx, job.ID, sessionID, path, s)
	// Interactive send (isLoopRun=false) treats RUN_STARTED as the run's
	// semantic boundary used by the UI for per-round duration. It MUST share
	// the same clock read as the persisted job.StartedAt to keep live vs reload
	// consistent.
	runStartedAt := nowMillis()
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

	start := time.Now()
	// CONTRACT: RunIteration must not return until messages.jsonl has been
	// flushed for this round. The §2.4 "先写 messages.jsonl，再发轮次结束
	// 事件" invariant — and therefore A-class GC safety — depends on this:
	// publishIterationEvent below fires the round-end event that lets the
	// buffer reclaim the round's chunks. If a future agent framework moves
	// jsonl write to an async path, A-class GC could reclaim chunks before
	// they appear in messages.jsonl, leaving reconnecting clients blind.
	err := runner.RunIteration(ctx, sessionID, messages, handler)
	duration := time.Since(start)

	// Transient-error retry for loop runs: network blips (HTTP/2 stream
	// resets, connection resets) are common in long-running unattended loops.
	// Retry up to loopTransientRetries times before declaring failure.
	if err != nil && isLoopRun && !isInterruptedRun(err) && isTransientNetworkError(err) {
		for attempt := 1; attempt <= loopTransientRetries; attempt++ {
			logger.Warnf(ctx, "[step] transient error (attempt %d/%d), retrying in %s: jobId=%s path=%v err=%v",
				attempt, loopTransientRetries, loopTransientRetryDelay, job.ID, path, err)

			// Wait before retrying, or bail if context is cancelled.
			timer := time.NewTimer(loopTransientRetryDelay)
			select {
			case <-ctx.Done():
				timer.Stop()
				err = ctx.Err()
			case <-timer.C:
			}
			if ctx.Err() != nil {
				err = ctx.Err()
				break
			}

			// Reset handler for the retry — accumulator must start fresh.
			handler = newLoopEventHandler(ctx, job.ID, sessionID, path, s)
			retryStart := time.Now()
			err = runner.RunIteration(ctx, sessionID, messages, handler)
			duration = time.Since(retryStart)

			if err == nil || isInterruptedRun(err) || !isTransientNetworkError(err) {
				if err == nil {
					logger.Infof(ctx, "[step] transient error recovered after %d attempt(s): jobId=%s path=%v", attempt, job.ID, path)
				}
				break
			}
		}
		if err != nil && !isInterruptedRun(err) && isTransientNetworkError(err) {
			logger.Errorf(ctx, "[step] transient error persisted after %d retries: jobId=%s path=%v err=%v",
				loopTransientRetries, job.ID, path, err)
		}
	}

	// Rate-limit retry for loop runs: upstream providers (Claude, OpenAI, etc.)
	// may return usage_limit_exceeded or 429 with a "try again later" hint.
	// Unlike transient network errors, these need longer backoff (30s/60s/120s).
	if err != nil && isLoopRun && !isInterruptedRun(err) && isRateLimitError(err) {
		retryAfterHint := parseRetryAfter(err)
		for attempt := 1; attempt <= loopRateLimitRetries; attempt++ {
			delay := loopRateLimitBaseDelay * time.Duration(1<<(attempt-1)) // 30s, 60s, 120s
			if retryAfterHint > 0 && retryAfterHint > delay {
				delay = retryAfterHint
			}
			logger.Warnf(ctx, "[step] rate limit hit (attempt %d/%d), retrying in %s: jobId=%s path=%v err=%v",
				attempt, loopRateLimitRetries, delay, job.ID, path, err)

			timer := time.NewTimer(delay)
			select {
			case <-ctx.Done():
				timer.Stop()
				err = ctx.Err()
			case <-timer.C:
			}
			if ctx.Err() != nil {
				err = ctx.Err()
				break
			}

			handler = newLoopEventHandler(ctx, job.ID, sessionID, path, s)
			retryStart := time.Now()
			err = runner.RunIteration(ctx, sessionID, messages, handler)
			duration = time.Since(retryStart)

			if err == nil || isInterruptedRun(err) || !isRateLimitError(err) {
				if err == nil {
					logger.Infof(ctx, "[step] rate limit recovered after %d attempt(s): jobId=%s path=%v", attempt, job.ID, path)
				}
				break
			}
		}
		if err != nil && !isInterruptedRun(err) && isRateLimitError(err) {
			logger.Errorf(ctx, "[step] rate limit persisted after %d retries (total wait ~%s): jobId=%s path=%v err=%v",
				loopRateLimitRetries, loopRateLimitBaseDelay*time.Duration((1<<loopRateLimitRetries)-1), job.ID, path, err)
		}
	}

	logger.Debugf(ctx, "[step] iter done: jobId=%s path=%v session=%s durMs=%d", job.ID, path, sessionID, duration.Milliseconds())
	// Resolve usage attribution from the session after the run. FlowNode.ModelID
	// can contain a backfilled default (not an explicit override), while the
	// session is the source of truth for the model that actually executed — this
	// also covers pause → interactive message switches → Continue.
	stepModelID := resolveUsageSessionModelID(runner, sessionID)
	if isInterruptedRun(err) {
		// err distinguishes user Stop (context.Canceled) from job-level
		// deadline (context.DeadlineExceeded) — both fall under "interrupted"
		// but the operator cause is different and the bare log without err
		// can't tell them apart.
		logger.Debugf(ctx, "[step] iter interrupted: jobId=%s path=%v session=%s durMs=%d err=%v", job.ID, path, sessionID, duration.Milliseconds(), err)
		// Stop / cancel still consumed wall-clock and tokens — record what
		// the accumulator captured so usage stats reflect reality (spec
		// 01-data-model: "Completed / Failed / Stopped 都计入").
		interruptedAt := start.Add(duration).UnixMilli()
		s.recordUsageSnapshot(job, handler, stepModelID, interruptedAt, duration.Milliseconds())
		// Close the buffer round even on cancel: RUN_STARTED + ITERATION_STARTED
		// were already published, so the buffer's openRoundID points at this
		// round. Without the closing pair, ResumeGC on Continue cannot reclaim
		// the round's A-class chunks (gc condition is round.closed && cursor
		// >= endSeq, which never holds when no end event arrives). Publish
		// only — do not record into Progress.Results so Continue can still
		// re-run this step at the same path.
		s.publishRunOutcome(job.ID, sessionID, path, handler.runID, err, interruptedAt)
		// For user-initiated stop (context.Canceled), don't report as error.
		iterError := ""
		if !errors.Is(err, context.Canceled) {
			iterError = err.Error()
		}
		s.publishIterationEvent(job.ID, &model.IterationResult{
			Path:       model.CopyPath(path),
			SessionID:  sessionID,
			Success:    false,
			DurationMs: duration.Milliseconds(),
			Tokens:     handler.tokens,
			Content:    handler.AccumulatedContent(),
			Error:      iterError,
		})
		return stepAborted
	}

	runFinishedAt := int64(0)
	if !isLoopRun {
		runFinishedAt = nowMillis()
		s.mu.Lock()
		job.FinishedAt = runFinishedAt
		s.mu.Unlock()
	}

	s.publishRunOutcome(job.ID, sessionID, path, handler.runID, err, runFinishedAt)

	// Record per-step usage stats. For interactive runs the run-finished
	// timestamp doubles as the step finalize moment; for loop iterations
	// we derive it from start+duration so it matches the iteration result.
	stepFinishedAt := runFinishedAt
	if stepFinishedAt == 0 {
		stepFinishedAt = start.Add(duration).UnixMilli()
	}
	s.recordUsageSnapshot(job, handler, stepModelID, stepFinishedAt, duration.Milliseconds())

	// NOTE: <<SET_VAR:key=value>> extraction is only done in shell steps
	// (executeShellRepeat). AI responses may mention these patterns in
	// discussion, so we skip extraction here to avoid false positives.

	result := &model.IterationResult{
		Path:       model.CopyPath(path),
		SessionID:  sessionID,
		Success:    err == nil,
		DurationMs: duration.Milliseconds(),
		Tokens:     handler.tokens,
		Content:    handler.AccumulatedContent(),
	}
	if err != nil {
		result.Error = err.Error()
	}

	if !isLoopRun {
		// Interactive run: publish the round-end event so the buffer can
		// release this round's A-class chunks once cursors cross. We
		// deliberately skip appendAndSaveResult — Progress.Results is
		// loop bookkeeping and an interactive send must not touch it.
		// Order matches the loop path: RunFinished/Error (already
		// published above) → IterationCompleted/Failed → JobCompleted/
		// Failed (issued by finishJob/failJob in runInteractive).
		s.publishIterationEvent(job.ID, result)
		// On failure, drive the terminal state through failJob rather than
		// falling through to finishJob. Without this, an interactive send
		// on a job with no prior terminal status (e.g. the first message
		// to a Pending job) would be finished as Completed even though
		// the run errored. failJob already restores the prior terminal
		// status when one was recorded, so Completed/Failed/Stopped loops
		// continue to be preserved on interactive errors.
		if err != nil {
			s.failJob(ctx, job, err.Error(), isLoopRun, false)
			return stepAborted
		}
		return stepCompleted
	}

	if err != nil {
		// Failure path: plain record. The failJob call below issues the
		// next persist (terminal status), so combining record + resume
		// here would produce no savings.
		s.recordIterationResult(job, result)

		// If execution failed in loop mode, mark the job as Failed so the
		// terminal status reflects the actual outcome. Resume is preserved so
		// the user can Continue and retry from this step.
		if isLoopRun {
			// duration disambiguates "all models instantly returned errors"
			// (~ms — upstream-wide outage) from "a model hung for the full
			// timeout" (likely a single slow model + retry chain). Without
			// it the err string alone can't tell you which: both render as
			// "stream all models failed".
			errKind := "non-transient"
			if isTransientNetworkError(err) {
				errKind = "transient (retries exhausted)"
			} else if isRateLimitError(err) {
				errKind = "rate-limit (retries exhausted)"
			}
			logger.Errorf(ctx, "[step] iter failed, failing job: jobId=%s path=%v duration=%s errKind=%s err=%v", job.ID, path, duration.Round(time.Millisecond), errKind, err)
			s.failJob(ctx, job, err.Error(), isLoopRun, true)
			return stepAborted
		}
		// Non-loop failures fall through; nothing else to do here.
		return stepCompleted
	}

	// Success. Decide the evaluator's STOP signal BEFORE persisting so the
	// resume pointer is written exactly once with the correct target. If we
	// always advanced the plain nextResume here and let the caller's
	// advanceResumePastGroup re-correct it on a STOP, a crash in that window
	// would leave a persisted resume pointing back into the group (re-running
	// a step the STOP meant to skip). Evaluator step (§2.1/§4): an exact
	// LOOP_DECISION:STOP on the final assistant text's last line breaks the
	// enclosing group (same semantics as a Shell STOP_LOOP); any other output
	// continues. The evaluator is a real, counted step either way.
	evaluatorStop := isLoopRun && isEvaluator && parseEvaluatorDecision(result.Content)
	if evaluatorStop {
		// STOP: record the result but DON'T advance the plain resume — the
		// caller's group-early-exit logic owns the only resume write (past the
		// whole group). This keeps persistence single-writer and crash-safe.
		s.recordIterationResult(job, result)
		logger.Infof(ctx, "[step] evaluator STOP: jobId=%s path=%v", job.ID, path)
		return stepStopLoop
	}
	// Continue: record result and advance resume in a single save.
	s.recordIterationAndAdvanceResume(job, result, nextResume)
	if isLoopRun && isEvaluator {
		logger.Debugf(ctx, "[step] evaluator continue: jobId=%s path=%v", job.ID, path)
	}

	// NOTE: STOP_LOOP / STOP_WORKFLOW markers are only honoured in shell steps
	// (executeShellRepeat) and evaluator steps (above). Ordinary AI prompt
	// responses may mention these markers in discussion or code review, so we
	// intentionally skip detection here to avoid false-positive termination.

	return stepCompleted
}

func resolveUsageSessionModelID(runner JobRunner, sessionID string) string {
	if runner == nil || sessionID == "" {
		return ""
	}
	return runner.SessionModelID(sessionID)
}

// substituteVars performs a single-pass replacement of {{key}} placeholders
// using strings.NewReplacer. This avoids nondeterminism from Go map iteration
// order: with a naive for-range + ReplaceAll loop, if variable A's value
// contains "{{B}}", the result depends on whether A or B is iterated first.
// NewReplacer scans left-to-right once, so replacement output is never re-matched.
func (s *serviceImpl) substituteVars(text string, job *model.Job) string {
	s.mu.RLock()
	vars := job.LoopConfig.Variables
	if len(vars) == 0 {
		s.mu.RUnlock()
		return text
	}
	oldnew := make([]string, 0, len(vars)*2)
	for k, v := range vars {
		oldnew = append(oldnew, "{{"+k+"}}", v)
	}
	s.mu.RUnlock()
	return strings.NewReplacer(oldnew...).Replace(text)
}

func (s *serviceImpl) injectBuiltinVars(ctx context.Context, job *model.Job) {
	if job.LoopConfig == nil {
		return
	}
	s.mu.Lock()
	if job.LoopConfig.Variables == nil {
		job.LoopConfig.Variables = make(map[string]string)
	}
	logger.Debugf(ctx, "[step] injectBuiltinVars: jobId=%s title=%s", job.ID, job.Title)
	builtins := map[string]string{
		consts.VarJobID:       job.ID,
		consts.VarJobTitle:    job.Title,
		consts.VarJobWorkdir:  job.Workdir,
		consts.VarWorkspaceID: job.WorkspaceID,
	}
	for k, v := range builtins {
		job.LoopConfig.Variables[k] = v
	}
	s.mu.Unlock()
}

// injectPerRoundVars injects dynamic per-round builtin variables:
//   - _current_time: current timestamp (RFC3339)
//   - _current_path: the step path as a string (e.g. "0.1.2.0")
//   - _last_assistant_msg: the Content from the last completed iteration result
func (s *serviceImpl) injectPerRoundVars(ctx context.Context, job *model.Job, path []int) {
	if job.LoopConfig == nil {
		return
	}

	now := time.Now().Format(time.RFC3339)
	pathStr := formatPath(path)

	var lastAssistant string
	s.mu.Lock()
	if job.Progress != nil && len(job.Progress.Results) > 0 {
		lastAssistant = job.Progress.Results[len(job.Progress.Results)-1].Content
	}
	if job.LoopConfig.Variables == nil {
		job.LoopConfig.Variables = make(map[string]string)
	}
	job.LoopConfig.Variables[consts.VarCurrentTime] = now
	job.LoopConfig.Variables[consts.VarCurrentPath] = pathStr
	job.LoopConfig.Variables[consts.VarLastAssistantMsg] = lastAssistant
	s.mu.Unlock()

	logger.Debugf(ctx, "[step] injectPerRoundVars: jobId=%s path=%s time=%s lastAssistant=%s",
		job.ID, pathStr, now, strutil.TruncateRunesWithEllipsis(lastAssistant, 100))
}

// formatPath converts a path slice to a dot-separated string, e.g. [0,1,2,0] → "0.1.2.0"
func formatPath(path []int) string {
	if len(path) == 0 {
		return ""
	}
	parts := make([]string, len(path))
	for i, v := range path {
		parts[i] = strconv.Itoa(v)
	}
	return strings.Join(parts, ".")
}

// applyVarsToJob merges extracted variables into job.LoopConfig.Variables
// and persists the job state. Variable extraction is best-effort (the loop
// can keep running with the live in-memory map even if disk write fails),
// so a persist failure is annotated via recordPersistWarning rather than
// surfaced — same pattern as updateResume and initAndAttachSession.
func (s *serviceImpl) applyVarsToJob(ctx context.Context, job *model.Job, vars map[string]string, source string) {
	if len(vars) == 0 {
		return
	}
	s.mu.Lock()
	if job.LoopConfig != nil {
		if job.LoopConfig.Variables == nil {
			job.LoopConfig.Variables = make(map[string]string)
		}
		for k, v := range vars {
			job.LoopConfig.Variables[k] = v
		}
	}
	s.mu.Unlock()
	if err := s.saveJobWithRetry(ctx, job, source); err != nil {
		s.recordPersistWarning(ctx, job, source, err)
	}
	logger.Debugf(ctx, "[step] extracted %d vars: source=%s jobId=%s", len(vars), source, job.ID)
}

// extractSetVars parses the agent response for <<SET_VAR:key=value>> patterns
// and returns the extracted key-value pairs.
var setVarPattern = regexp.MustCompile(`<<SET_VAR:(\w+)=(.+?)>>`)

func extractSetVars(content string) map[string]string {
	matches := setVarPattern.FindAllStringSubmatch(content, -1)
	if len(matches) == 0 {
		return nil
	}
	vars := make(map[string]string, len(matches))
	for _, m := range matches {
		vars[m[1]] = m[2]
	}
	return vars
}

// timeNow is the package-level clock used by nowMillis. Tests override it to
// freeze time around assertions on user-visible run boundaries (StartedAt,
// FinishedAt and matching boundary events). Internal metadata / operational
// windows such as UpdatedAt, elapsed durations and cleanup cutoffs may use
// time.Now directly because tests should not need to freeze those clocks.
var timeNow = time.Now

func nowMillis() int64 {
	return timeNow().UnixMilli()
}

// SetUsageRecorder wires the optional usage-stats sink. Once set, every
// step finalize point hands a Snapshot to the recorder. Passing nil
// disables recording (used by tests).
func (s *serviceImpl) SetUsageRecorder(r usagestats.Recorder) {
	s.mu.Lock()
	s.usageRecorder = r
	s.mu.Unlock()
}

// recordUsageSnapshot is the single call site for handing per-step usage
// stats to the recorder. It centralises:
//   - the nil-recorder gate (recorder is optional)
//   - duration clamping (sub-millisecond successful steps still bump turn count)
//
// Model attribution is the caller's responsibility — pass the resolved
// per-step / session model id, or empty when unknown. We deliberately do
// NOT fall back to Job.FirstModelID because that field is the JobList
// "first session" denormalisation and would mis-attribute model time when
// the per-step or session model has moved on.
//
// Called from interactive / loop iteration / shell finalize positions.
func (s *serviceImpl) recordUsageSnapshot(job *model.Job, handler *loopEventHandler, modelID string, finishedAtMs, durationMs int64) {
	s.mu.RLock()
	recorder := s.usageRecorder
	s.mu.RUnlock()
	if recorder == nil || handler == nil || handler.usage == nil {
		return
	}
	if durationMs < 0 {
		durationMs = 0
	}
	wsID := ""
	if job != nil {
		wsID = job.WorkspaceID
	}
	snap := handler.usage.Snapshot(wsID, modelID, finishedAtMs, durationMs)
	recorder.Record(snap)
}

func optsClientMessageID(opts *SendMessageOptions) string {
	if opts == nil {
		return ""
	}
	return opts.ClientMessageID
}
