package job

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/types/model"
	"github.com/fanlv/quartet/types/msgextra"
)

const (
	maxStderrSize                  = 10 << 20        // 10 MB cap on stderr capture to prevent OOM
	shellGracePeriod               = 3 * time.Second // time to wait after SIGTERM before SIGKILL
	shellInterruptedPersistTimeout = 2 * time.Second // best-effort history persist after cancellation
	maxFilteredEnvKeysLog          = 20
	// shellOutputTailBytes caps the trailing output included in error logs and
	// per-iteration error messages — enough to surface the actual failure reason
	// (e.g. "command not found") without bloating the main log or job.json.
	shellOutputTailBytes = 1024
)

func shellFilteredEnvKeysForLog(keys []string) []string {
	if len(keys) <= maxFilteredEnvKeysLog {
		return keys
	}
	return keys[:maxFilteredEnvKeysLog]
}

// resolveShellScript loads the script body (inline or from scriptSvc) and
// applies loop variable substitution. A referenced script that cannot be loaded
// returns an error instead of falling back to stale inline content; the caller
// publishes that setup failure after the iteration/run events are open.
func (s *serviceImpl) resolveShellScript(ctx context.Context, job *model.Job, node model.FlowNode) (string, error) {
	scriptContent := node.Message
	if node.ScriptID != "" {
		switch {
		case s.scriptSvc == nil:
			return scriptContent, fmt.Errorf("shell step references scriptId %q but no script service is configured", node.ScriptID)
		default:
			sc, err := s.scriptSvc.Get(ctx, node.ScriptID)
			switch {
			case err != nil:
				logger.Errorf(ctx, "[shell] load script failed: scriptId=%s err=%v", node.ScriptID, err)
				return scriptContent, fmt.Errorf("load shell script %q failed: %w", node.ScriptID, err)
			case sc == nil:
				return scriptContent, fmt.Errorf("shell script %q not found", node.ScriptID)
			default:
				scriptContent = sc.Content
			}
		}
	}

	if job.LoopConfig != nil {
		scriptContent = s.substituteVars(scriptContent, job)
	}
	return scriptContent, nil
}

// shellSetupContext groups the run metadata needed to report setup failures.
type shellSetupContext struct {
	sessionID    string
	runID        string
	handler      *loopEventHandler
	modelID      string
	runStartedAt int64
}

// shellSetupErr handles a setup-phase error in executeShellRepeat by publishing
// the error event, recording the result, and stopping the job.
func (s *serviceImpl) shellSetupErr(ctx context.Context, job *model.Job, path []int, setup shellSetupContext, err error) stepResult {
	setupFinishedAt := s.nowMillis()
	s.publishRunOutcome(job.ID, setup.sessionID, path, setup.runID, withRunErrorCode(err, runErrorCodeShell), setupFinishedAt)
	s.recordShellIterationResult(ctx, job, path, setup.sessionID, "", err, 0)
	runStartedAt := setup.runStartedAt
	if runStartedAt <= 0 {
		runStartedAt = setupFinishedAt
	}
	durationMs := setupFinishedAt - runStartedAt
	if durationMs < 0 {
		durationMs = 0
	}
	s.recordUsageSnapshot(job, setup.handler, setup.modelID, setupFinishedAt, durationMs)
	logger.Errorf(ctx, "[shell] setup failed, failing job: jobId=%s path=%v err=%v", job.ID, path, err)
	s.failJob(ctx, job, err.Error(), true, true)
	return stepAborted
}

type shellExecution struct {
	workdir         string
	scriptContent   string
	scriptFile      string
	ctrlFile        string
	sessionID       string
	stepModelID     string
	handler         *loopEventHandler
	runStartedAt    int64
	cmd             *exec.Cmd
	stdout          io.ReadCloser
	stderr          io.ReadCloser
	filteredEnvKeys []string
	cleanupFn       func()
}

func (e *shellExecution) cleanup() {
	if e != nil && e.cleanupFn != nil {
		e.cleanupFn()
	}
}

func (e *shellExecution) setupContext() shellSetupContext {
	return shellSetupContext{
		sessionID:    e.sessionID,
		runID:        e.handler.runID,
		handler:      e.handler,
		modelID:      e.stepModelID,
		runStartedAt: e.runStartedAt,
	}
}

func (s *serviceImpl) prepareShellExecution(ctx context.Context, job *model.Job, runner JobRunner, node model.FlowNode, path []int, sessionID string) (*shellExecution, stepResult) {
	s.mu.RLock()
	workdir := job.Workdir
	s.mu.RUnlock()

	// Resolve script content before opening the iteration. If script loading
	// fails, keep the error until the handler/runID exist so shellSetupErr can
	// publish the failure on the iteration like any other setup error.
	scriptContent, scriptLoadErr := s.resolveShellScript(ctx, job, node)

	// Log only the script path / size, never the content: substituted
	// scripts routinely contain secrets that the user injected via vars
	// (API keys, tokens, cloud creds). Logging even a truncated prefix at
	// debug level risks leaking them into log aggregation systems.
	logger.Debugf(ctx, "[shell] run: jobId=%s path=%v scriptBytes=%d", job.ID, path, len(scriptContent))

	// Publish iteration started
	iterationStartedAt := s.persistIterationStart(ctx, job, path)
	s.Publish(job.ID, &model.IterationStartedEvent{
		BaseEvent: model.BaseEvent{
			Type: model.EventTypeIterationStarted, JobID: job.ID,
			SessionID: sessionID, Path: path,
			Timestamp: iterationStartedAt,
		},
		Message:   scriptContent,
		ModelID:   node.StepModelID,
		AgentType: node.AgentType,
	})

	handler := newLoopEventHandler(ctx, job.ID, sessionID, path, s)
	handler.shellMode = true

	runStartedAt := s.nowMillis()
	s.Publish(job.ID, &model.RunStartedEvent{
		BaseEvent: model.BaseEvent{
			Type: model.EventTypeRunStarted, JobID: job.ID,
			SessionID: sessionID, RunID: handler.runID,
			Path:      path,
			Timestamp: runStartedAt,
		},
	})
	// Shell attribution follows the job session's currently-bound model. The
	// FlowNode may carry a backfilled default model, but shell execution itself
	// runs under the session context; use the session as the source of truth.
	stepModelID := resolveUsageSessionModelID(runner, sessionID)
	setup := shellSetupContext{
		sessionID:    sessionID,
		runID:        handler.runID,
		handler:      handler,
		modelID:      stepModelID,
		runStartedAt: runStartedAt,
	}

	// Surface a script-load failure now that the iteration/run events have been
	// published, so it is reported and fails the job exactly like other setup
	// errors instead of silently running fallback content.
	if scriptLoadErr != nil {
		return nil, s.shellSetupErr(ctx, job, path, setup, scriptLoadErr)
	}

	var cleanups []func()
	cleanupAll := func() {
		for i := len(cleanups) - 1; i >= 0; i-- {
			cleanups[i]()
		}
	}

	// Materialize the script so bash-specific vars like BASH_SOURCE[0] exist.
	scriptFile, cleanupScript, err := writeShellTempFile(s.fileManager, workdir, scriptContent)
	if err != nil {
		logger.Errorf(ctx, "[shell] create temp script failed: jobId=%s err=%v", job.ID, err)
		return nil, s.shellSetupErr(ctx, job, path, setup, err)
	}
	cleanups = append(cleanups, cleanupScript)

	// Create a control file for the script to write directives into.
	ctrlFile, cleanupCtrl, err := createControlFile(s.fileManager, workdir)
	if err != nil {
		logger.Warnf(ctx, "[shell] create control file failed: jobId=%s err=%v", job.ID, err)
		// Fallback to /dev/null so scripts don't error on >> "$QUARTET_CONTROL"
		ctrlFile = os.DevNull
	} else {
		cleanups = append(cleanups, cleanupCtrl)
	}

	cmd, stdout, stderr, filteredEnvKeys, err := s.prepareShellProcess(ctx, job, workdir, scriptFile, ctrlFile)
	if err != nil {
		cleanupAll()
		return nil, s.shellSetupErr(ctx, job, path, setup, err)
	}

	return &shellExecution{
		workdir:         workdir,
		scriptContent:   scriptContent,
		scriptFile:      scriptFile,
		ctrlFile:        ctrlFile,
		sessionID:       sessionID,
		stepModelID:     stepModelID,
		handler:         handler,
		runStartedAt:    runStartedAt,
		cmd:             cmd,
		stdout:          stdout,
		stderr:          stderr,
		filteredEnvKeys: filteredEnvKeys,
		cleanupFn:       cleanupAll,
	}, stepCompleted
}

func (s *serviceImpl) executeShellRepeat(ctx context.Context, job *model.Job, runner JobRunner, node model.FlowNode, path []int, sessionID string, nextResume *model.JobResume) stepResult {
	exec, setupResult := s.prepareShellExecution(ctx, job, runner, node, path, sessionID)
	if setupResult != stepCompleted {
		return setupResult
	}
	defer exec.cleanup()

	procResult := s.runShellProcess(ctx, job, exec.cmd, exec.stdout, exec.stderr, exec.handler)
	if !procResult.started {
		return s.shellSetupErr(ctx, job, path, exec.setupContext(), procResult.err)
	}

	// Shell cancellation uses manual process-group killing (not exec.CommandContext),
	// so cmd.Wait() typically returns *exec.ExitError (signal killed/terminated)
	// rather than context.Canceled. Treat ctx cancellation as an interrupted run.
	if isInterruptedRun(procResult.err) || ctx.Err() != nil {
		return s.handleShellInterruptedResult(ctx, job, exec, path, sessionID, procResult)
	}

	return s.finalizeShellResult(ctx, job, exec, path, sessionID, nextResume, procResult)
}

func (s *serviceImpl) handleShellInterruptedResult(ctx context.Context, job *model.Job, exec *shellExecution, path []int, sessionID string, procResult shellProcessResult) stepResult {
	logger.Debugf(ctx, "[shell] interrupted: jobId=%s path=%v", job.ID, path)
	// Stop/Cancel keeps the iteration resumable (stepAborted), but the shell
	// output that was already streamed live must still be written to history so
	// refresh/reload matches what the user just saw.
	persistCtx, persistCancel := context.WithTimeout(context.Background(), shellInterruptedPersistTimeout)
	if err := s.persistShellMessages(persistCtx, job.WorkspaceID, job.ID, sessionID, exec.scriptContent, procResult.output, procResult.startedAt, procResult.finishedAt, exec.handler.msgID); err != nil {
		logger.Errorf(persistCtx, "[shell] persist interrupted messages failed: jobId=%s err=%v", job.ID, err)
	}
	persistCancel()
	// Also account for the wall-clock and any captured tool / token data
	// (spec 01-data-model: "Completed / Failed / Stopped 都计入").
	s.recordUsageSnapshot(job, exec.handler, exec.stepModelID, procResult.finishedAt, procResult.durationMs)
	// Close the buffer round even on cancel: RUN_STARTED + ITERATION_STARTED
	// were already published. Without the closing pair, ResumeGC on Continue
	// cannot reclaim this round's A-class chunks — see the matching comment
	// in executeRepeat's interrupted branch. cmdErr may be nil when the
	// cancel beat the process exit (ctx.Err() set, no exec error captured),
	// so fall back to ctx.Err() for the message.
	interruptErr := procResult.err
	if interruptErr == nil {
		interruptErr = ctx.Err()
	}
	s.publishRunOutcome(job.ID, sessionID, path, exec.handler.runID, interruptErr, procResult.finishedAt)
	s.publishIterationEvent(job.ID, &model.IterationResult{
		Path:       model.CopyPath(path),
		SessionID:  sessionID,
		Success:    false,
		DurationMs: procResult.durationMs,
		Content:    procResult.output,
		Error:      interruptErr.Error(),
	})
	return stepAborted
}

func (s *serviceImpl) finalizeShellResult(ctx context.Context, job *model.Job, exec *shellExecution, path []int, sessionID string, nextResume *model.JobResume, procResult shellProcessResult) stepResult {
	s.publishRunOutcome(job.ID, sessionID, path, exec.handler.runID, withRunErrorCode(procResult.err, runErrorCodeShell), procResult.finishedAt)

	// Record per-step usage stats for shell. Uses the shell-side timestamps
	// (the ones aligned with the message END boundary) so the stat row
	// matches what the iteration result records as DurationMs.
	s.recordUsageSnapshot(job, exec.handler, exec.stepModelID, procResult.finishedAt, procResult.durationMs)

	// Parse control file for directives (SET_VAR, STOP_LOOP, STOP_WORKFLOW).
	// parseControlFile logs the read itself (bytes/lines on success, err on
	// failure) so we don't do a pre-read here to avoid duplicate I/O on the
	// same file path.
	ctrlVars, ctrlStopLoop, ctrlStopWorkflow := parseControlFile(ctx, s.fileManager, job.ID, exec.ctrlFile)
	ctrlVars = mergeLegacySetVars(ctrlVars, extractSetVars(procResult.output))

	// Apply extracted variables
	s.applyVarsToJob(ctx, job, ctrlVars, jobPersistActionExtractSetVarsShell)

	// Persist messages with timing info so history reload can render the
	// duration badge and dedupe against live SSE by msgID.
	if err := s.persistShellMessages(ctx, job.WorkspaceID, job.ID, sessionID, exec.scriptContent, procResult.output, procResult.startedAt, procResult.finishedAt, exec.handler.msgID); err != nil {
		s.recordPersistWarning(ctx, job, jobPersistActionPersistShellMessages, err)
	}

	// Record iteration result and handle failure modes.
	if procResult.err == nil {
		// Success. The control file was already parsed above, so we know the
		// control signal BEFORE persisting and can write the resume pointer
		// exactly once with the correct target:
		//   - STOP_LOOP: the caller's group-early-exit logic owns the resume
		//     write (it advances past the whole group). Advancing the plain
		//     nextResume here too would mean a crash between the two writes
		//     leaves a persisted resume pointing back into the group, re-running
		//     a step the STOP meant to skip.
		//   - STOP_WORKFLOW: the workflow exits and finishes; clear resume so a
		//     Continue doesn't re-enter at the next sibling.
		//   - neither: advance the plain nextResume in a single save.
		result := buildShellIterationResult(path, sessionID, procResult.output, nil, procResult.durationMs)
		switch {
		case ctrlStopWorkflow:
			s.recordIterationAndAdvanceResume(ctx, job, result, nil)
		case ctrlStopLoop:
			s.recordIterationResult(ctx, job, result)
		default:
			s.recordIterationAndAdvanceResume(ctx, job, result, nextResume)
		}
	} else {
		// Include scriptFile / workdir and a tail of the combined stdout+stderr
		// so the backend main log carries enough context to diagnose exit-code
		// failures (e.g. "exit status 127" with the actual "command not found"
		// line from bash) without having to open the per-job job.json.
		tail := shellOutputTail(procResult.output, shellOutputTailBytes)
		if len(exec.filteredEnvKeys) > 0 {
			logger.Warnf(ctx,
				"[shell] env vars were filtered when this job ran: jobId=%s keys=%v total=%d — if the script needs these, set %s",
				job.ID, shellFilteredEnvKeysForLog(exec.filteredEnvKeys), len(exec.filteredEnvKeys), envShellPassthrough)
		}

		// Hard failure: record the failed iteration; failJob below issues the
		// terminal persist so combining record + resume saves nothing here.
		s.recordShellIterationResult(ctx, job, path, sessionID, procResult.output, procResult.err, procResult.durationMs)
		logger.Errorf(ctx,
			"[shell] run failed, failing job: jobId=%s path=%v scriptFile=%s workdir=%s err=%v outputTail=%q",
			job.ID, path, exec.scriptFile, exec.workdir, procResult.err, tail)
		s.failJob(ctx, job, shellFailureMessage(procResult.err, tail), true, true)
		return stepAborted
	}

	// Check for STOP_WORKFLOW directive — exits the entire workflow
	if ctrlStopWorkflow {
		logger.Infof(ctx, "[shell] %s: jobId=%s path=%v, exiting workflow", controlStopWorkflow, job.ID, path)
		return stepStopWorkflow
	}

	// Check for STOP_LOOP directive — only breaks the innermost group.
	// This is normal control flow, fires once per iteration that opts to
	// break, and the outer scheduler/job summary already captures the
	// terminal status, so keep it at DEBUG to avoid flooding the main log.
	// STOP_WORKFLOW above stays at INFO because it ends the entire workflow
	// — a rare, high-signal event worth a single line in the access log.
	if ctrlStopLoop {
		logger.Debugf(ctx, "[shell] %s: jobId=%s path=%v, breaking inner loop", controlStopLoop, job.ID, path)
		return stepStopLoop
	}

	return stepCompleted
}

// mergeLegacySetVars preserves backward compatibility for stdout markers while
// keeping the control file as the source of truth when both specify the same key.
func mergeLegacySetVars(ctrlVars, legacyVars map[string]string) map[string]string {
	if len(legacyVars) == 0 {
		return ctrlVars
	}
	if ctrlVars == nil {
		return legacyVars
	}
	for k, v := range legacyVars {
		if _, exists := ctrlVars[k]; !exists {
			ctrlVars[k] = v
		}
	}
	return ctrlVars
}

func (s *serviceImpl) persistShellMessages(ctx context.Context, wsID, jobID, sessionID, scriptContent, output string, startedAt, finishedAt int64, msgID string) error {
	repo, err := repository.NewChatContextRepo(wsID, jobID, sessionID)
	if err != nil {
		return fmt.Errorf("create shell chat context repo: %w", err)
	}

	userMsg := schema.UserMessage(scriptContent)
	userMsg.Extra = map[string]any{
		msgextra.KeyShellOutput: true,
		msgextra.KeyStartedAt:   startedAt,
	}
	assistantMsg := schema.AssistantMessage(output, nil)
	assistantExtra := map[string]any{
		msgextra.KeyShellOutput: true,
		msgextra.KeyStartedAt:   startedAt,
		msgextra.KeyFinishedAt:  finishedAt,
	}
	if msgID != "" {
		assistantExtra[msgextra.KeyMsgID] = msgID
	}
	assistantMsg.Extra = assistantExtra

	if err := repo.AppendMessages(ctx, []*schema.Message{userMsg, assistantMsg}); err != nil {
		return fmt.Errorf("append shell messages: %w", err)
	}
	return nil
}

func (s *serviceImpl) recordShellIterationResult(ctx context.Context, job *model.Job, path []int, sessionID, output string, cmdErr error, durationMs int64) {
	result := buildShellIterationResult(path, sessionID, output, cmdErr, durationMs)
	s.recordIterationResult(ctx, job, result)
}

func buildShellIterationResult(path []int, sessionID, output string, cmdErr error, durationMs int64) *model.IterationResult {
	result := &model.IterationResult{
		Path:       model.CopyPath(path),
		SessionID:  sessionID,
		Success:    cmdErr == nil,
		DurationMs: durationMs,
		Content:    output,
	}
	if cmdErr != nil {
		// Persist the raw exec error PLUS the tail of stdout/stderr so the
		// per-iteration error and JobProgress.LastError carry an actionable
		// reason (e.g. timeout / "command not found" / error JSON), not just
		// "exit status 1". The full output stays in result.Content for the
		// detail panel.
		result.Error = shellFailureMessage(cmdErr, shellOutputTail(output, shellOutputTailBytes))
	}
	return result
}
