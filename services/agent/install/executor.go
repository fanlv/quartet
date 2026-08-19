package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sync"
	"time"
)

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

func runStep(ctx context.Context, step InstallStep, timeout time.Duration) StepResult {
	result := StepResult{Display: step.Display}
	started := time.Now()
	stepCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(stepCtx, step.Program, step.Args...)
	cmd.Dir = step.Dir
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	result.DurationMs = time.Since(started).Milliseconds()
	result.Stdout = stdout.String()
	result.Stderr = stderr.String()
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if stepCtx.Err() == context.DeadlineExceeded {
		result.TimedOut = true
		result.Error = fmt.Sprintf("install step timed out after %s", timeout)
		return result
	}
	if runErr != nil {
		result.Error = fmt.Sprintf("run install step failed: %v", runErr)
	}
	return result
}
