package graph

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/strutil"
)

// Node hooks (§ 节点 Hook): a pure side-effect shell script run AFTER a node
// completes — used by Prompt nodes and main-graph End nodes for notifications /
// logging / marking. A hook NEVER produces variables, changes node status, or
// fails the run: a non-zero exit, a timeout, or a spawn error is logged at Warn
// level and otherwise ignored. This is intentionally simpler than
// executeShellNode (no control file, no output parsing, no graphShellHelpers) —
// it only injects environment and runs the script.

// hookTimeout bounds a single hook so a hung script cannot leak a goroutine for
// the lifetime of the process. The node has already succeeded by the time its
// hook runs, so the timeout only affects the side effect, never the run.
const hookTimeout = 60 * time.Second

// hookOutputMaxRunes bounds each captured stream (stdout/stderr) before it is
// put on a hookCompleted/hookFailed event. A hook is meant for short side
// effects (a notification, a log line); a runaway script must not be able to
// bloat events.jsonl or the detail panel. Truncation happens in runHook so the
// persisted event is already bounded.
const hookOutputMaxRunes = 4000

// hookOutcome is a node hook's execution result, handed to hookRequest.emit so
// the caller can surface it as an event. exitCode is the script's process exit
// code on a clean run; for a setup failure (mkdir/tempfile/spawn) or a timeout
// the script never produced a code, so a sentinel is used (see runHook).
type hookOutcome struct {
	exitCode int
	stdout   string
	stderr   string
	message  string // non-empty on failure: human-readable cause
	failed   bool
}

// hookRequest carries everything a hook script reads. The caller assembles it on
// the scheduler goroutine (snapshotting scheduler-owned state) so runHook can
// run off-thread without touching any shared map.
type hookRequest struct {
	script   string              // the resolved script body (chosen by the caller)
	workdir  string              // effectiveConfig(run).Workdir; "" → os.TempDir()
	visible  map[string]string   // the node's visible variable snapshot (already cloned)
	disabled map[string]struct{} // globally disabled variable names (render empty)

	jobTitle  string
	jobID     string
	runID     string
	nodeID    string
	nodeTitle string
	nodeType  string // string(node.Type)
	output    string // the node's output / _last_assistant_msg

	// emit, when non-nil, receives the hook's result so the caller can publish a
	// hookCompleted/hookFailed event. Invoked from the hook goroutine; the caller
	// must ensure the closure is safe to call off the scheduler goroutine (it
	// captures value copies and routes through the thread-safe event sink). A nil
	// emit means "no event" (e.g. a code path with no sink wired).
	emit func(hookOutcome)
}

// runHook executes a hook script as a pure side effect. It never returns an
// error: every failure mode (mkdir, tempfile, spawn, non-zero exit, timeout) is
// logged and swallowed. parent is used only for log correlation — the script
// runs under its OWN timeout context detached from parent's cancellation, so a
// run-level cancel (hard stop / run timeout / worker cancel) cannot kill a
// side-effect that has already been triggered by a succeeded node.
//
// On every terminal path (success or any failure) it invokes req.emit once (if
// set) so the caller can surface the result as an event — a configured hook
// always produces exactly one signal, never silence. A blank script is the lone
// exception: it is a deliberate no-op and emits nothing.
func runHook(parent context.Context, req hookRequest) {
	script := strings.TrimSpace(req.script)
	if script == "" {
		return
	}

	// emitFailure logs at Warn and reports a hookFailed outcome with a sentinel
	// exit code (the script never produced one). Used for every setup failure.
	emitFailure := func(msg string, err error) {
		full := msg
		if err != nil {
			full = fmt.Sprintf("%s: %v", msg, err)
		}
		logger.Warnf(parent, "[graph] hook skipped (%s): runId=%s nodeId=%s err=%v", msg, req.runID, req.nodeID, err)
		if req.emit != nil {
			req.emit(hookOutcome{exitCode: -1, message: full, failed: true})
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), hookTimeout)
	defer cancel()

	tmpDir := req.workdir
	if tmpDir == "" {
		tmpDir = os.TempDir()
	}
	if err := os.MkdirAll(tmpDir, 0755); err != nil {
		emitFailure("mkdir failed", err)
		return
	}
	f, err := os.CreateTemp(tmpDir, ".quartet-hook-*.sh")
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
	if req.workdir != "" {
		cmd.Dir = req.workdir
	}
	cmd.Env = buildHookEnv(req)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	runErr := cmd.Run()
	outStr := strutil.TruncateRunesWithEllipsis(stdout.String(), hookOutputMaxRunes)
	errStr := strutil.TruncateRunesWithEllipsis(stderr.String(), hookOutputMaxRunes)
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
			msg = fmt.Sprintf("hook timed out after %s", hookTimeout)
		}
		logger.Warnf(parent, "[graph] hook failed (ignored): runId=%s nodeId=%s type=%s exitCode=%d err=%v stdout=%s stderr=%s",
			req.runID, req.nodeID, req.nodeType, exitCode, runErr, outStr, errStr)
		if req.emit != nil {
			req.emit(hookOutcome{exitCode: exitCode, stdout: outStr, stderr: errStr, message: msg, failed: true})
		}
		return
	}
	logger.Infof(parent, "[graph] hook completed: runId=%s nodeId=%s type=%s", req.runID, req.nodeID, req.nodeType)
	if req.emit != nil {
		req.emit(hookOutcome{exitCode: 0, stdout: outStr, stderr: errStr})
	}
}

// buildHookEnv builds the hook process environment, mirroring executeShellNode's
// rule (runtime.go): visible variables are exported as $name (a disabled
// variable renders to the empty string regardless of its stored value), then the
// QUARTET_* hook-context variables are appended LAST so a user variable can
// never shadow them.
func buildHookEnv(req hookRequest) []string {
	env := os.Environ()
	for k, v := range req.visible {
		if strings.HasPrefix(k, "QUARTET_") {
			continue
		}
		if _, off := req.disabled[k]; off {
			v = ""
		}
		env = append(env, k+"="+v)
	}
	for k := range req.disabled {
		if strings.HasPrefix(k, "QUARTET_") {
			continue
		}
		if _, ok := req.visible[k]; !ok {
			env = append(env, k+"=")
		}
	}
	env = append(env,
		"QUARTET_JOB_TITLE="+req.jobTitle,
		"QUARTET_JOB_ID="+req.jobID,
		"QUARTET_RUN_ID="+req.runID,
		"QUARTET_NODE_ID="+req.nodeID,
		"QUARTET_NODE_TITLE="+req.nodeTitle,
		"QUARTET_NODE_TYPE="+req.nodeType,
		"QUARTET_LAST_ASSISTANT="+req.output,
	)
	return env
}
