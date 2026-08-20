package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"

	"github.com/fanlv/quartet/pkg/safe"
)

const processTreeWaitDelay = 2 * time.Second

// installMu serializes all automatic installs process-wide. quartet is a
// single-user backend, and concurrent global installs (npm especially) can
// corrupt each other, so only one install flow runs at a time.
var installMu sync.Mutex

// ErrInstallInFlight is returned when another install flow is already running.
var ErrInstallInFlight = errors.New("another agent install is already in progress")

// StepResult captures the complete outcome of one executed install step.
// Stdout/Stderr are kept verbatim — errors must reach the user in full.
type StepResult struct {
	Display    string
	Stdout     string
	Stderr     string
	ExitCode   int
	TimedOut   bool
	Error      string
	DurationMs int64
}

// RunSteps executes the given preset steps sequentially with a per-step
// timeout, stopping at the first failed step. It returns the results of every
// step that was started. ErrInstallInFlight is returned (without results) when
// another install is already running.
func RunSteps(ctx context.Context, steps []InstallStep, perStepTimeout time.Duration) ([]StepResult, error) {
	if !installMu.TryLock() {
		return nil, ErrInstallInFlight
	}
	defer installMu.Unlock()

	results := make([]StepResult, 0, len(steps))
	for _, step := range steps {
		result := runStep(ctx, step, perStepTimeout)
		results = append(results, result)
		if result.Error != "" || result.TimedOut || result.ExitCode != 0 {
			break
		}
	}
	return results, nil
}

func runStep(ctx context.Context, step InstallStep, timeout time.Duration) (result StepResult) {
	result = StepResult{Display: step.Display, ExitCode: -1}
	started := time.Now()
	stepCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.Command(step.Program, step.Args...)
	cmd.Dir = step.Dir
	processTree, err := newProcessTree(cmd)
	if err != nil {
		result.DurationMs = time.Since(started).Milliseconds()
		result.Error = fmt.Sprintf("prepare install process tree failed: %v", err)
		return result
	}
	defer func() {
		if err := processTree.close(); err != nil {
			appendStepError(&result, fmt.Sprintf("close install process tree failed: %v", err))
		}
	}()
	// This is a final guard for a child that deliberately leaves the managed
	// process tree but keeps stdout/stderr open. Normal timeout cleanup kills
	// the whole tree immediately; WaitDelay keeps Wait itself bounded if the
	// process violates that ownership contract.
	cmd.WaitDelay = processTreeWaitDelay
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := stepCtx.Err(); err != nil {
		result.DurationMs = time.Since(started).Milliseconds()
		setStepContextError(&result, err, timeout)
		return result
	}
	if err := cmd.Start(); err != nil {
		result.DurationMs = time.Since(started).Milliseconds()
		result.Error = fmt.Sprintf("start install step failed: %v", err)
		return result
	}
	if err := processTree.attach(cmd); err != nil {
		cleanupErr := processTree.terminate(cmd)
		waitErr := cmd.Wait()
		result.DurationMs = time.Since(started).Milliseconds()
		result.Stdout = stdout.String()
		result.Stderr = stderr.String()
		if cmd.ProcessState != nil {
			result.ExitCode = cmd.ProcessState.ExitCode()
		}
		result.Error = fmt.Sprintf("attach install process tree failed: %v", err)
		if cleanupErr != nil {
			appendStepError(&result, fmt.Sprintf("terminate unowned install process failed: %v", cleanupErr))
		}
		if waitErr != nil && !errors.Is(waitErr, exec.ErrWaitDelay) {
			appendStepError(&result, fmt.Sprintf("wait for unowned install process failed: %v", waitErr))
		}
		return result
	}

	waitDone := make(chan error, 1)
	safe.Go(stepCtx, func() {
		waitDone <- cmd.Wait()
	})

	var (
		runErr     error
		cleanupErr error
		canceled   bool
	)
	select {
	case runErr = <-waitDone:
		// A shell can exit while a background installer descendant continues
		// with redirected output. Always clean the step-owned process tree
		// before releasing installMu; for a normal foreground command whose
		// tree is already gone this is a no-op.
		cleanupErr = processTree.terminate(cmd)
	case <-stepCtx.Done():
		canceled = true
		cleanupErr = processTree.terminate(cmd)
		// Do not release installMu until Wait has reaped the direct process and
		// all managed descendants have closed their inherited output pipes.
		// WaitDelay bounds this wait even for a process that escaped the tree.
		runErr = <-waitDone
	}

	result.DurationMs = time.Since(started).Milliseconds()
	result.Stdout = stdout.String()
	result.Stderr = stderr.String()
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if canceled {
		setStepContextError(&result, stepCtx.Err(), timeout)
		if cleanupErr != nil {
			result.Error += fmt.Sprintf("; terminate install process tree failed: %v", cleanupErr)
		}
		return result
	}
	if runErr != nil {
		result.Error = fmt.Sprintf("run install step failed: %v", runErr)
		if cleanupErr != nil {
			result.Error += fmt.Sprintf("; terminate install process tree failed: %v", cleanupErr)
		}
	} else if cleanupErr != nil {
		result.Error = fmt.Sprintf("install step completed, but terminating leftover process tree failed: %v", cleanupErr)
	}
	return result
}

func setStepContextError(result *StepResult, err error, timeout time.Duration) {
	if errors.Is(err, context.DeadlineExceeded) {
		result.TimedOut = true
		result.Error = fmt.Sprintf("install step timed out after %s", timeout)
		return
	}
	result.Error = fmt.Sprintf("install step canceled: %v", err)
}

func appendStepError(result *StepResult, detail string) {
	if result == nil || detail == "" {
		return
	}
	if result.Error == "" {
		result.Error = detail
		return
	}
	result.Error += "; " + detail
}
