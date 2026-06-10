package job

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
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

// shellSetupErr handles a setup-phase error in executeShellRepeat by publishing
// the error event, recording the result, and stopping the job.
func (s *serviceImpl) shellSetupErr(ctx context.Context, job *model.Job, path []int,
	sessionID, runID string, err error,
	handler *loopEventHandler, modelID string, runStartedAt int64) stepResult {
	setupFinishedAt := nowMillis()
	s.publishRunOutcome(job.ID, sessionID, path, runID, err, 0)
	s.recordShellIterationResult(job, path, sessionID, "", err, 0)
	if runStartedAt <= 0 {
		runStartedAt = setupFinishedAt
	}
	durationMs := setupFinishedAt - runStartedAt
	if durationMs < 0 {
		durationMs = 0
	}
	s.recordUsageSnapshot(job, handler, modelID, setupFinishedAt, durationMs)
	logger.Errorf(ctx, "[shell] setup failed, failing job: jobId=%s path=%v err=%v", job.ID, path, err)
	s.failJob(ctx, job, err.Error(), true, true)
	return stepAborted
}

type shellProcessResult struct {
	started    bool
	startedAt  int64
	finishedAt int64
	durationMs int64
	output     string
	err        error
}

func (s *serviceImpl) prepareShellProcess(ctx context.Context, job *model.Job, workdir, scriptFile, ctrlFile string) (*exec.Cmd, io.ReadCloser, io.ReadCloser, []string, error) {
	// Use a plain exec.Command (NOT CommandContext) because Go's CommandContext
	// only kills the direct child process. Instead we manage cancellation ourselves
	// by killing the entire process group, which also covers background subprocesses
	// spawned by the script (e.g. "sleep 999 &").
	cmd := exec.Command("bash", scriptFile)
	cmd.SysProcAttr = shellSysProcAttr()
	if workdir != "" {
		// Validate workdir up front so a missing / inaccessible path
		// surfaces a clear error instead of an opaque "chdir: no such
		// file" from cmd.Start().
		info, statErr := os.Stat(workdir)
		if statErr != nil {
			logger.Errorf(ctx, "[shell] workdir stat failed: jobId=%s workdir=%s err=%v", job.ID, workdir, statErr)
			return nil, nil, nil, nil, fmt.Errorf("invalid workdir %q: %w", workdir, statErr)
		}
		if !info.IsDir() {
			logger.Errorf(ctx, "[shell] workdir not directory: jobId=%s workdir=%s", job.ID, workdir)
			return nil, nil, nil, nil, fmt.Errorf("workdir %q is not a directory", workdir)
		}
		cmd.Dir = workdir
	}
	env, filteredEnvKeys := sanitizedShellEnvWithFiltered()
	cmd.Env = append(env, "QUARTET_CONTROL="+ctrlFile)
	if len(filteredEnvKeys) > 0 {
		logger.Debugf(ctx, "[shell] env filtered: jobId=%s keys=%v total=%d passthroughHint=%s", job.ID, shellFilteredEnvKeysForLog(filteredEnvKeys), len(filteredEnvKeys), envShellPassthrough)
	}

	stdout, err := cmd.StdoutPipe()
	if err != nil {
		logger.Errorf(ctx, "[shell] stdout pipe failed: jobId=%s err=%v", job.ID, err)
		return nil, nil, nil, nil, err
	}

	stderr, err := cmd.StderrPipe()
	if err != nil {
		logger.Errorf(ctx, "[shell] stderr pipe failed: jobId=%s err=%v", job.ID, err)
		return nil, nil, nil, nil, err
	}

	return cmd, stdout, stderr, filteredEnvKeys, nil
}

func (s *serviceImpl) runShellProcess(ctx context.Context, job *model.Job, cmd *exec.Cmd, stdout, stderr io.Reader, handler *loopEventHandler) shellProcessResult {
	start := time.Now()
	if err := cmd.Start(); err != nil {
		logger.Errorf(ctx, "[shell] start failed: jobId=%s err=%v", job.ID, err)
		startedAt := start.UnixMilli()
		finishedAt := nowMillis()
		return shellProcessResult{startedAt: startedAt, finishedAt: finishedAt, err: err}
	}

	// Monitor context cancellation and kill the entire process group.
	// This ensures background subprocesses spawned by the script are also killed.
	processExited := make(chan struct{})
	go func() {
		select {
		case <-ctx.Done():
			shellKillProcessGroup(cmd, processExited, shellGracePeriod)
		case <-processExited:
			// cmd.Wait() returned first — process exited normally, nothing to kill.
		}
	}()

	// Stream stdout+stderr concurrently to avoid pipe deadlock.
	_ = handler.OnMessageStart()

	// Read stderr in background to prevent pipe buffer from filling up.
	// Limit to maxStderrSize to prevent OOM from misbehaving scripts.
	var stderrBuf strings.Builder
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		scanner := bufio.NewScanner(stderr)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			line := scanner.Text() + "\n"
			if stderrBuf.Len()+len(line) > maxStderrSize {
				if stderrBuf.Len() < maxStderrSize {
					stderrBuf.WriteString("\n... stderr truncated (exceeded 10MB) ...\n")
				}
				continue // keep draining to prevent pipe deadlock
			}
			stderrBuf.WriteString(line)
		}
		// A single line longer than the 1MB scanner cap makes Scan() return
		// false with bufio.ErrTooLong, which would leave the OS pipe unread
		// and block the child on its next stderr write — eventually hanging
		// cmd.Wait(). Drain the rest of the pipe to keep the child writable.
		if err := scanner.Err(); err != nil {
			logger.Warnf(ctx, "[shell] stderr scanner error, draining remaining: jobId=%s err=%v", job.ID, err)
			_, _ = io.Copy(io.Discard, stderr)
		}
	}()

	// Stream stdout to handler in the current goroutine.
	stdoutScanner := bufio.NewScanner(stdout)
	stdoutScanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for stdoutScanner.Scan() {
		line := stdoutScanner.Text() + "\n"
		_ = handler.OnMessageDelta(line)
	}
	if err := stdoutScanner.Err(); err != nil {
		// A single line longer than the 1MB scanner cap (e.g. unformatted JSON
		// from `curl`, base64 dumps, minified build output) makes Scan() return
		// false with bufio.ErrTooLong. Don't discard the rest — fall back to
		// chunked reads so subsequent output is still streamed to the user
		// instead of silently disappearing mid-command.
		logger.Warnf(ctx, "[shell] stdout scanner error, falling back to chunk read: jobId=%s err=%v", job.ID, err)
		buf := make([]byte, 64*1024)
		for {
			n, readErr := stdout.Read(buf)
			if n > 0 {
				_ = handler.OnMessageDelta(string(buf[:n]))
			}
			if readErr != nil {
				if readErr != io.EOF {
					logger.Warnf(ctx, "[shell] stdout fallback read error: jobId=%s err=%v", job.ID, readErr)
				}
				break
			}
		}
	}

	// Wait for stderr goroutine to finish before calling Wait.
	<-stderrDone
	if stderrContent := stderrBuf.String(); stderrContent != "" {
		_ = handler.OnMessageDelta(stderrContent)
	}

	// Wait must be called after draining stdout/stderr to avoid pipe deadlock.
	cmdErr := cmd.Wait()
	close(processExited) // signal the cancel-monitor goroutine that the process exited

	// Pin a single wall-clock read for the message boundary so that:
	//   - the live SSE TEXT_MESSAGE_END timestamp
	//   - the persisted finishedAt
	// share the same instant (scheme doc: "同一次时钟读数").
	startedAt := start.UnixMilli()
	finishedAt := nowMillis()
	durationMs := finishedAt - startedAt
	if durationMs < 0 {
		durationMs = 0
	}
	handler.SetNextBoundaryTimestamp(finishedAt)
	_ = handler.OnMessageEnd()

	return shellProcessResult{
		started:    true,
		startedAt:  startedAt,
		finishedAt: finishedAt,
		durationMs: durationMs,
		output:     handler.AccumulatedContent(),
		err:        cmdErr,
	}
}

func (s *serviceImpl) executeShellRepeat(ctx context.Context, job *model.Job, runner JobRunner, node model.FlowNode, path []int, sessionID string, nextResume *model.JobResume) stepResult {
	s.mu.RLock()
	workdir := job.Workdir
	s.mu.RUnlock()

	// Resolve script content: load from scriptSvc if scriptID is set, otherwise
	// use message directly. A configured ScriptID that fails to load (or
	// resolves to nil) must NOT silently fall back to node.Message — that would
	// run stale/empty content while the user believes the library script ran.
	// Defer surfacing the error until the handler/runID exist so shellSetupErr
	// can publish the failure on the iteration like any other setup error.
	scriptContent := node.Message
	var scriptLoadErr error
	if node.ScriptID != "" {
		switch {
		case s.scriptSvc == nil:
			scriptLoadErr = fmt.Errorf("shell step references scriptId %q but no script service is configured", node.ScriptID)
		default:
			sc, err := s.scriptSvc.Get(ctx, node.ScriptID)
			switch {
			case err != nil:
				logger.Errorf(ctx, "[shell] load script failed: scriptId=%s err=%v", node.ScriptID, err)
				scriptLoadErr = fmt.Errorf("load shell script %q failed: %w", node.ScriptID, err)
			case sc == nil:
				scriptLoadErr = fmt.Errorf("shell script %q not found", node.ScriptID)
			default:
				scriptContent = sc.Content
			}
		}
	}

	// Variable substitution (lock required: Variables may be concurrently written by applyVarsToJob)
	if job.LoopConfig != nil && scriptLoadErr == nil {
		scriptContent = s.substituteVars(scriptContent, job)
	}

	// Log only the script path / size, never the content: substituted
	// scripts routinely contain secrets that the user injected via vars
	// (API keys, tokens, cloud creds). Logging even a truncated prefix at
	// debug level risks leaking them into log aggregation systems.
	logger.Debugf(ctx, "[shell] run: jobId=%s path=%v scriptBytes=%d", job.ID, path, len(scriptContent))

	// Publish iteration started
	s.persistIterationStart(ctx, job, path)
	s.Publish(job.ID, &model.IterationStartedEvent{
		BaseEvent: model.BaseEvent{
			Type: model.EventTypeIterationStarted, JobID: job.ID,
			SessionID: sessionID, Path: path,
			Timestamp: nowMillis(),
		},
		Message:   scriptContent,
		ModelID:   node.StepModelID,
		AgentType: node.AgentType,
	})

	handler := newLoopEventHandler(ctx, job.ID, sessionID, path, s)
	handler.shellMode = true

	runStartedAt := nowMillis()
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

	// Surface a script-load failure now that the iteration/run events have been
	// published, so it is reported and fails the job exactly like other setup
	// errors instead of silently running fallback content.
	if scriptLoadErr != nil {
		return s.shellSetupErr(ctx, job, path, sessionID, handler.runID, scriptLoadErr, handler, stepModelID, runStartedAt)
	}

	// Materialize the script so bash-specific vars like BASH_SOURCE[0] exist.
	scriptFile, cleanupScript, err := writeShellTempFile(workdir, scriptContent)
	if err != nil {
		logger.Errorf(ctx, "[shell] create temp script failed: jobId=%s err=%v", job.ID, err)
		return s.shellSetupErr(ctx, job, path, sessionID, handler.runID, err, handler, stepModelID, runStartedAt)
	}
	defer cleanupScript()

	// Create a control file for the script to write directives into.
	ctrlFile, cleanupCtrl, err := createControlFile(workdir)
	if err != nil {
		logger.Warnf(ctx, "[shell] create control file failed: jobId=%s err=%v", job.ID, err)
		// Fallback to /dev/null so scripts don't error on >> "$QUARTET_CONTROL"
		ctrlFile = os.DevNull
	} else {
		defer cleanupCtrl()
	}

	cmd, stdout, stderr, filteredEnvKeys, err := s.prepareShellProcess(ctx, job, workdir, scriptFile, ctrlFile)
	if err != nil {
		return s.shellSetupErr(ctx, job, path, sessionID, handler.runID, err, handler, stepModelID, runStartedAt)
	}
	procResult := s.runShellProcess(ctx, job, cmd, stdout, stderr, handler)
	if !procResult.started {
		return s.shellSetupErr(ctx, job, path, sessionID, handler.runID, procResult.err, handler, stepModelID, runStartedAt)
	}
	shellStartedAt := procResult.startedAt
	shellFinishedAt := procResult.finishedAt
	durationMs := procResult.durationMs
	cmdErr := procResult.err
	accumulatedOutput := procResult.output

	// Shell cancellation uses manual process-group killing (not exec.CommandContext),
	// so cmd.Wait() typically returns *exec.ExitError (signal killed/terminated)
	// rather than context.Canceled. Treat ctx cancellation as an interrupted run.
	if isInterruptedRun(cmdErr) || ctx.Err() != nil {
		logger.Debugf(ctx, "[shell] interrupted: jobId=%s path=%v", job.ID, path)
		// Stop/Cancel keeps the iteration resumable (stepAborted), but the shell
		// output that was already streamed live must still be written to history so
		// refresh/reload matches what the user just saw.
		persistCtx, persistCancel := context.WithTimeout(context.Background(), shellInterruptedPersistTimeout)
		s.persistShellMessages(persistCtx, job.WorkspaceID, job.ID, sessionID, scriptContent, accumulatedOutput, shellStartedAt, shellFinishedAt, handler.msgID)
		persistCancel()
		// Also account for the wall-clock and any captured tool / token data
		// (spec 01-data-model: "Completed / Failed / Stopped 都计入").
		s.recordUsageSnapshot(job, handler, stepModelID, shellFinishedAt, durationMs)
		// Close the buffer round even on cancel: RUN_STARTED + ITERATION_STARTED
		// were already published. Without the closing pair, ResumeGC on Continue
		// cannot reclaim this round's A-class chunks — see the matching comment
		// in executeRepeat's interrupted branch. cmdErr may be nil when the
		// cancel beat the process exit (ctx.Err() set, no exec error captured),
		// so fall back to ctx.Err() for the message.
		interruptErr := cmdErr
		if interruptErr == nil {
			interruptErr = ctx.Err()
		}
		s.publishRunOutcome(job.ID, sessionID, path, handler.runID, interruptErr, shellFinishedAt)
		s.publishIterationEvent(job.ID, &model.IterationResult{
			Path:       model.CopyPath(path),
			SessionID:  sessionID,
			Success:    false,
			DurationMs: durationMs,
			Content:    accumulatedOutput,
			Error:      interruptErr.Error(),
		})
		return stepAborted
	}

	s.publishRunOutcome(job.ID, sessionID, path, handler.runID, cmdErr, 0)

	// Record per-step usage stats for shell. Uses the shell-side timestamps
	// (the ones aligned with the message END boundary) so the stat row
	// matches what the iteration result records as DurationMs.
	s.recordUsageSnapshot(job, handler, stepModelID, shellFinishedAt, durationMs)

	// Parse control file for directives (SET_VAR, STOP_LOOP, STOP_WORKFLOW).
	// parseControlFile logs the read itself (bytes/lines on success, err on
	// failure) so we don't do a pre-read here to avoid duplicate I/O on the
	// same file path.
	ctrlVars, ctrlStopLoop, ctrlStopWorkflow := parseControlFile(ctx, job.ID, ctrlFile)

	// Also support legacy <<SET_VAR:key=value>> from stdout for backward compatibility
	if legacyVars := extractSetVars(accumulatedOutput); len(legacyVars) > 0 {
		if ctrlVars == nil {
			ctrlVars = legacyVars
		} else {
			for k, v := range legacyVars {
				if _, exists := ctrlVars[k]; !exists {
					ctrlVars[k] = v
				}
			}
		}
	}

	// Apply extracted variables
	s.applyVarsToJob(ctx, job, ctrlVars, "extract_set_vars_shell")

	// Persist messages with timing info so history reload can render the
	// duration badge and dedupe against live SSE by msgID.
	s.persistShellMessages(ctx, job.WorkspaceID, job.ID, sessionID, scriptContent, accumulatedOutput, shellStartedAt, shellFinishedAt, handler.msgID)

	// Record iteration result and handle failure modes.
	if cmdErr == nil {
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
		result := buildShellIterationResult(path, sessionID, accumulatedOutput, nil, durationMs)
		switch {
		case ctrlStopWorkflow:
			s.recordIterationAndAdvanceResume(job, result, nil)
		case ctrlStopLoop:
			s.recordIterationResult(job, result)
		default:
			s.recordIterationAndAdvanceResume(job, result, nextResume)
		}
	} else {
		// Include scriptFile / workdir and a tail of the combined stdout+stderr
		// so the backend main log carries enough context to diagnose exit-code
		// failures (e.g. "exit status 127" with the actual "command not found"
		// line from bash) without having to open the per-job job.json.
		tail := shellOutputTail(accumulatedOutput, shellOutputTailBytes)
		if len(filteredEnvKeys) > 0 {
			logger.Warnf(ctx,
				"[shell] env vars were filtered when this job ran: jobId=%s keys=%v total=%d — if the script needs these, set %s",
				job.ID, shellFilteredEnvKeysForLog(filteredEnvKeys), len(filteredEnvKeys), envShellPassthrough)
		}

		// Hard failure: record the failed iteration; failJob below issues the
		// terminal persist so combining record + resume saves nothing here.
		s.recordShellIterationResult(job, path, sessionID, accumulatedOutput, cmdErr, durationMs)
		logger.Errorf(ctx,
			"[shell] run failed, failing job: jobId=%s path=%v scriptFile=%s workdir=%s err=%v outputTail=%q",
			job.ID, path, scriptFile, workdir, cmdErr, tail)
		s.failJob(ctx, job, shellFailureMessage(cmdErr, tail), true, true)
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

func (s *serviceImpl) persistShellMessages(ctx context.Context, wsID, jobID, sessionID, scriptContent, output string, startedAt, finishedAt int64, msgID string) {
	repo, err := repository.NewChatContextRepo(wsID, jobID, sessionID)
	if err != nil {
		logger.Errorf(ctx, "[shell] persist: create repo failed: jobId=%s err=%v", jobID, err)
		return
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
		logger.Errorf(ctx, "[shell] persist: append failed: jobId=%s err=%v", jobID, err)
	}
}

func (s *serviceImpl) recordShellIterationResult(job *model.Job, path []int, sessionID, output string, cmdErr error, durationMs int64) {
	result := buildShellIterationResult(path, sessionID, output, cmdErr, durationMs)
	s.recordIterationResult(job, result)
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
