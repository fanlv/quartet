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

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/safe"
	"github.com/fanlv/quartet/types/model"
)

type shellCommandFactory func(name string, arg ...string) *exec.Cmd

type shellProcessResult struct {
	started    bool
	startedAt  int64
	finishedAt int64
	durationMs int64
	output     string
	err        error
}

func (s *serviceImpl) newShellCommand(name string, arg ...string) *exec.Cmd {
	if s != nil && s.shellCommandFactory != nil {
		return s.shellCommandFactory(name, arg...)
	}
	return exec.Command(name, arg...)
}

func (s *serviceImpl) prepareShellProcess(ctx context.Context, job *model.Job, workdir, scriptFile, ctrlFile string) (*exec.Cmd, io.ReadCloser, io.ReadCloser, []string, error) {
	// Use a plain exec.Command (NOT CommandContext) because Go's CommandContext
	// only kills the direct child process. Instead we manage cancellation ourselves
	// by killing the entire process group, which also covers background subprocesses
	// spawned by the script (e.g. "sleep 999 &").
	cmd := s.newShellCommand("bash", scriptFile)
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
		finishedAt := s.nowMillis()
		return shellProcessResult{startedAt: startedAt, finishedAt: finishedAt, err: err}
	}

	// Monitor context cancellation and kill the entire process group.
	// This ensures background subprocesses spawned by the script are also killed.
	processExited := make(chan struct{})
	safe.Go(ctx, func() {
		select {
		case <-ctx.Done():
			shellKillProcessGroup(cmd, processExited, shellGracePeriod)
		case <-processExited:
			// cmd.Wait() returned first — process exited normally, nothing to kill.
		}
	})

	// Stream stdout+stderr concurrently to avoid pipe deadlock.
	_ = handler.OnMessageStart()

	// Read stderr in background to prevent pipe buffer from filling up.
	// Limit to maxStderrSize to prevent OOM from misbehaving scripts.
	var stderrBuf strings.Builder
	stderrDone := make(chan struct{})
	safe.Go(ctx, func() {
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
	})

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
	finishedAt := s.nowMillis()
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
