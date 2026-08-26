package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/fanlv/quartet/pkg/executil"
	"github.com/fanlv/quartet/pkg/safe"
)

const processTreeWaitDelay = 2 * time.Second

const (
	InternalProgramRemovePaths  = "quartet-internal:remove-user-paths"
	InternalProgramBuildEinoCLI = "quartet-internal:build-eino-cli"
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

func runStep(ctx context.Context, step InstallStep, timeout time.Duration) (result StepResult) {
	result = StepResult{Display: step.Display, ExitCode: -1}
	started := time.Now()
	stepCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	if strings.HasPrefix(step.Program, "quartet-internal:") {
		return runInternalStep(stepCtx, step, started, timeout)
	}

	cmd := commandForStep(step.Program, step.Args...)
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
		if step.SkipIfMissing && errors.Is(err, exec.ErrNotFound) {
			result.ExitCode = 0
			result.Stdout = fmt.Sprintf("skipped %s because %s is not installed\n", step.Display, step.Program)
			return result
		}
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

// commandForStep accounts for Windows package-manager shims. os/exec can find
// .cmd files through PATHEXT, but CreateProcess cannot execute them directly.
// Running only the trusted catalog step through cmd.exe preserves argv
// boundaries and does not expose client input to a shell.
func commandForStep(program string, args ...string) *exec.Cmd {
	return executil.Command(program, args...)
}

func runInternalStep(ctx context.Context, step InstallStep, started time.Time, timeout time.Duration) StepResult {
	result := StepResult{Display: step.Display, ExitCode: -1}
	var err error
	switch step.Program {
	case InternalProgramRemovePaths:
		err = removeUserPaths(ctx, step.Args, &result)
	case InternalProgramBuildEinoCLI:
		err = buildEinoCLI(ctx, &result)
	default:
		err = fmt.Errorf("unknown internal install program %q", step.Program)
	}
	result.DurationMs = time.Since(started).Milliseconds()
	if err == nil {
		result.ExitCode = 0
		return result
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		setStepContextError(&result, ctx.Err(), timeout)
		return result
	}
	if errors.Is(ctx.Err(), context.Canceled) {
		setStepContextError(&result, ctx.Err(), timeout)
		return result
	}
	result.Error = err.Error()
	return result
}

func removeUserPaths(ctx context.Context, relativePaths []string, result *StepResult) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve user home for uninstall failed: %w", err)
	}
	home, err = filepath.Abs(home)
	if err != nil {
		return fmt.Errorf("resolve absolute user home for uninstall failed: %w", err)
	}
	realHome, err := filepath.EvalSymlinks(home)
	if err != nil {
		return fmt.Errorf("resolve real user home for uninstall failed: %w", err)
	}
	for _, relativePath := range relativePaths {
		if err := ctx.Err(); err != nil {
			return err
		}
		originalPath := relativePath
		relativePath = filepath.FromSlash(strings.TrimSpace(relativePath))
		if relativePath == "" || filepath.IsAbs(relativePath) {
			return fmt.Errorf("refuse unsafe uninstall path %q: expected a path relative to the user home", originalPath)
		}
		target, err := filepath.Abs(filepath.Join(home, relativePath))
		if err != nil {
			return fmt.Errorf("resolve uninstall path %q failed: %w", relativePath, err)
		}
		rel, err := filepath.Rel(home, target)
		if err != nil || rel == "." || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
			return fmt.Errorf("refuse unsafe uninstall path %q outside user home %q", target, home)
		}
		if err := validateExistingParentInsideHome(realHome, target); err != nil {
			return err
		}
		if err := os.RemoveAll(target); err != nil {
			return fmt.Errorf("remove %q failed: %w", target, err)
		}
		result.Stdout += fmt.Sprintf("removed %s\n", target)
	}
	return nil
}

func validateExistingParentInsideHome(realHome, target string) error {
	parent := filepath.Dir(target)
	for {
		realParent, err := filepath.EvalSymlinks(parent)
		if err == nil {
			rel, relErr := filepath.Rel(realHome, realParent)
			if relErr != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				return fmt.Errorf("refuse uninstall path %q: parent resolves outside user home %q", target, realHome)
			}
			return nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("resolve uninstall path parent %q failed: %w", parent, err)
		}
		next := filepath.Dir(parent)
		if next == parent {
			return fmt.Errorf("resolve existing parent for uninstall path %q failed", target)
		}
		parent = next
	}
}

func buildEinoCLI(ctx context.Context, result *StepResult) error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("resolve user home for eino-cli install failed: %w", err)
	}
	installDir := strings.TrimSpace(os.Getenv("INSTALL_BIN_DIR"))
	if installDir == "" {
		installDir = filepath.Join(home, ".local", "bin")
	}
	installDir, err = filepath.Abs(installDir)
	if err != nil {
		return fmt.Errorf("resolve eino-cli install directory failed: %w", err)
	}
	if err := os.MkdirAll(installDir, 0o755); err != nil {
		return fmt.Errorf("create eino-cli install directory %q failed: %w", installDir, err)
	}
	binaryName := "eino-cli"
	if runtime.GOOS == "windows" {
		binaryName += ".exe"
	}
	target := filepath.Join(installDir, binaryName)
	temporary := target + fmt.Sprintf(".tmp.%d", os.Getpid())
	cmd := executil.CommandContext(ctx, "go", "build", "-o", temporary, "./cmd/eino-cli")
	cmd.Dir = "."
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	err = cmd.Run()
	result.Stdout += stdout.String()
	result.Stderr += stderr.String()
	if err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("build eino-cli failed: %w", err)
	}
	if err := replaceFile(temporary, target); err != nil {
		_ = os.Remove(temporary)
		return fmt.Errorf("install eino-cli to %q failed: %w", target, err)
	}
	result.Stdout += fmt.Sprintf("installed eino-cli to %s\n", target)
	return nil
}

func replaceFile(source, target string) error {
	if err := os.Rename(source, target); err == nil {
		return nil
	}
	if err := os.Remove(target); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return os.Rename(source, target)
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
