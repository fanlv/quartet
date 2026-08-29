// Package shellhook runs a user-configured shell script as a pure side effect
// (§ 结束 Hook): a notification / logging / marking script fired AFTER some unit
// of work already finished. A hook NEVER produces variables, changes any status,
// or fails its caller: a non-zero exit, a timeout, or a spawn error is logged at
// Warn level and otherwise ignored.
//
// It is intentionally simpler than a Shell workflow node (no control file, no
// output parsing, no helper functions) — it only injects environment and runs
// the script. Both the Graph engine (Prompt / End node hooks) and the Job
// service (interactive round end) share this executor so the two entry points
// keep identical semantics: same timeout, same env rules, same failure handling.
package shellhook

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"sort"
	"strings"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/strutil"
	typepath "github.com/fanlv/quartet/types/path"
)

// Timeout bounds a single hook so a hung script cannot leak a goroutine for the
// lifetime of the process. The work has already succeeded by the time its hook
// runs, so the timeout only affects the side effect, never the caller.
const Timeout = 60 * time.Second

// OutputMaxRunes bounds each captured stream (stdout/stderr) before it is handed
// to Request.Emit. A hook is meant for short side effects (a notification, a log
// line); a runaway script must not be able to bloat a persisted event or a
// detail panel. Truncation happens in Run so what the caller emits is already
// bounded.
const OutputMaxRunes = 4000

// contextPrefix is the reserved namespace for caller-injected context variables.
// A user variable using this prefix is dropped so it can never shadow one.
const contextPrefix = "QUARTET_"

// Outcome is a hook's execution result, handed to Request.Emit so the caller can
// surface it (as an event, a log line, …). ExitCode is the script's process exit
// code on a clean run; for a setup failure (mkdir/tempfile/spawn) or a timeout
// the script never produced a code, so -1 is used.
type Outcome struct {
	ExitCode int
	Stdout   string
	Stderr   string
	Message  string // non-empty on failure: human-readable cause
	Failed   bool
}

// Request carries everything a hook script reads. The caller assembles it on its
// own goroutine (snapshotting any owned state) so Run can execute off-thread
// without touching shared state.
type Request struct {
	// Script is the resolved script body. Blank (or whitespace-only) is a
	// deliberate no-op: nothing runs and nothing is emitted.
	Script string
	// Workdir is the directory the script RUNS in; "" leaves the child with the
	// server process's own working directory. It is deliberately not where the
	// script FILE is written — see scriptDir.
	Workdir string
	// Vars are user-defined variables exported as $name. Keys in the reserved
	// QUARTET_ namespace are ignored.
	Vars map[string]string
	// Disabled names render as the empty string regardless of their stored value
	// (and are exported empty even when absent from Vars).
	Disabled map[string]struct{}
	// Context holds the QUARTET_* runtime context variables, exported LAST so a
	// user variable can never shadow them.
	Context map[string]string

	// LogFields is appended to every log line for correlation, e.g.
	// "runId=r-1 nodeId=n-2" or "jobId=j-1 sessionId=s-3".
	LogFields string

	// Emit, when non-nil, receives the hook's result exactly once on every
	// terminal path (success or any failure), so a configured hook always
	// produces a signal, never silence. Invoked from the calling goroutine of
	// Run — the caller must ensure the closure is safe there.
	Emit func(Outcome)
}

// Run executes a hook script as a pure side effect. It never returns an error:
// every failure mode (mkdir, tempfile, spawn, non-zero exit, timeout) is logged
// and swallowed. parent is used only for log correlation — the script runs under
// its OWN timeout context detached from parent's cancellation, so a caller-level
// cancel (hard stop, run timeout, worker cancel) cannot kill a side effect that
// has already been triggered by finished work.
func Run(parent context.Context, req Request) {
	script := strings.TrimSpace(req.Script)
	if script == "" {
		return
	}

	// emitFailure logs at Warn and reports a failed Outcome with a sentinel exit
	// code (the script never produced one). Used for every setup failure.
	emitFailure := func(msg string, err error) {
		full := msg
		if err != nil {
			full = fmt.Sprintf("%s: %v", msg, err)
		}
		logger.Warnf(parent, "[hook] skipped (%s): %s err=%v", msg, req.LogFields, err)
		if req.Emit != nil {
			req.Emit(Outcome{ExitCode: -1, Message: full, Failed: true})
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), Timeout)
	defer cancel()

	dir, err := scriptDir()
	if err != nil {
		emitFailure("mkdir failed", err)
		return
	}
	f, err := os.CreateTemp(dir, ".quartet-hook-*.sh")
	if err != nil {
		emitFailure("tempfile failed", err)
		return
	}
	scriptPath := f.Name()
	defer os.Remove(scriptPath)
	if _, err := f.WriteString(script); err != nil {
		_ = f.Close()
		emitFailure("write failed", err)
		return
	}
	if err := f.Close(); err != nil {
		emitFailure("close failed", err)
		return
	}

	cmd := exec.CommandContext(ctx, "bash", scriptPath)
	if req.Workdir != "" {
		cmd.Dir = req.Workdir
	}
	cmd.Env = buildEnv(req)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	outStr := strutil.TruncateRunesWithEllipsis(stdout.String(), OutputMaxRunes)
	errStr := strutil.TruncateRunesWithEllipsis(stderr.String(), OutputMaxRunes)
	if runErr != nil {
		// Distinguish a non-zero exit (we have an exit code) from a timeout or a
		// spawn failure (no code; the deadline context is the tell). The user most
		// wants the stderr, which is captured above either way.
		exitCode := -1
		msg := runErr.Error()
		var exitErr *exec.ExitError
		if errors.As(runErr, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else if ctx.Err() == context.DeadlineExceeded {
			msg = fmt.Sprintf("hook timed out after %s", Timeout)
		}
		logger.Warnf(parent, "[hook] failed (ignored): %s exitCode=%d err=%v stdout=%s stderr=%s",
			req.LogFields, exitCode, runErr, outStr, errStr)
		if req.Emit != nil {
			req.Emit(Outcome{ExitCode: exitCode, Stdout: outStr, Stderr: errStr, Message: msg, Failed: true})
		}
		return
	}
	logger.Infof(parent, "[hook] completed: %s", req.LogFields)
	if req.Emit != nil {
		req.Emit(Outcome{ExitCode: 0, Stdout: outStr, Stderr: errStr})
	}
}

// scriptDir returns the directory the hook script file is written to, creating
// it if needed. It is deliberately NOT the hook's workdir: the script is a
// process-owned temp artifact, and a workdir is typically the user's git
// checkout, so writing there leaves .quartet-hook-*.sh droppings in a tracked
// tree whenever the deferred cleanup does not run (the server is killed, the
// host loses power). Under LOCAL_MEMORY's tmp root a leaked file is invisible
// and swept with the rest of the reconstructable state.
//
// os.TempDir() is the fallback for a process with no usable LOCAL_MEMORY: a
// hook is a side effect that must still fire, so an unresolvable data root
// degrades the file's location rather than skipping the script.
func scriptDir() (string, error) {
	dir, err := typepath.ShellTempDir()
	if err != nil {
		dir = os.TempDir()
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", err
	}
	return dir, nil
}

// buildEnv builds the hook process environment, mirroring a Shell node's rule:
// user variables are exported as $name (a disabled variable renders to the empty
// string regardless of its stored value), then the QUARTET_* context variables
// are appended LAST so a user variable can never shadow them. Context keys are
// exported in sorted order so the environment is deterministic.
func buildEnv(req Request) []string {
	env := os.Environ()
	for k, v := range req.Vars {
		if strings.HasPrefix(k, contextPrefix) {
			continue
		}
		if _, off := req.Disabled[k]; off {
			v = ""
		}
		env = append(env, k+"="+v)
	}
	for k := range req.Disabled {
		if strings.HasPrefix(k, contextPrefix) {
			continue
		}
		if _, ok := req.Vars[k]; !ok {
			env = append(env, k+"=")
		}
	}
	keys := make([]string, 0, len(req.Context))
	for k := range req.Context {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		env = append(env, k+"="+req.Context[k])
	}
	return env
}
