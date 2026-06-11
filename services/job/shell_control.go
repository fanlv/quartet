package job

import (
	"context"
	"encoding/base64"
	"fmt"
	"os"
	"strings"

	"github.com/fanlv/quartet/pkg/fileserver"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/pkg/logger"
)

const (
	controlStopLoop     = "STOP_LOOP"
	controlStopWorkflow = "STOP_WORKFLOW"
)

// shellHelpers are injected into every shell script so users can call
// quartet_set / quartet_break / quartet_return instead of manually writing to the
// control file. Values are base64-encoded to safely handle special chars.
const shellHelpers = `
# quartet built-in helpers
quartet_set() { local _v="$2"; [ -z "$_v" ] && _v="STOP_LOOP"; echo "[quartet] quartet_set key=$1 value=$_v" >&2; printf '%s\n' "B64:$1=$(printf '%s' "$_v" | base64 -w0 2>/dev/null || printf '%s' "$_v" | base64 | tr -d '\n')" >> "$QUARTET_CONTROL"; }
quartet_break() { echo "[quartet] quartet_break" >&2; printf '%s\n' "STOP_LOOP" >> "$QUARTET_CONTROL"; }
quartet_return() { echo "[quartet] quartet_return" >&2; printf '%s\n' "STOP_WORKFLOW" >> "$QUARTET_CONTROL"; }
quartet_stop() { echo "[quartet] quartet_stop" >&2; quartet_break; }
export -f quartet_set quartet_break quartet_return quartet_stop
`

// parseControlFile reads a control file written by a shell script and parses
// its directives. Each non-empty line is either:
//   - "STOP_LOOP"          → sets stopLoop = true
//   - "STOP_WORKFLOW"      → sets stopWorkflow = true
//   - "B64:key=base64val"  → base64-decoded value (written by quartet_set helper)
//   - "key=value"          → plain text value (legacy)
//
// The control file path is exposed to scripts via the QUARTET_CONTROL env var.
//
// The successful read is logged at DEBUG — byte/line counts are pure
// diagnostic noise at INFO because every shell iteration that uses the
// control file would repeat the same line, and the meaningful outcomes
// (STOP_LOOP / STOP_WORKFLOW / decode failures) are already surfaced by
// their own log entries. Read failures stay at WARN. Pass path == os.DevNull
// when no control file was created — it is treated as an empty no-op and
// not logged.
func parseControlFile(ctx context.Context, fm fileserver.FileManager, jobID, path string) (vars map[string]string, stopLoop bool, stopWorkflow bool) {
	if path == "" || path == os.DevNull {
		return nil, false, false
	}
	result, err := fm.FileRead(&fsmodel.FileReadRequest{File: path})
	if err != nil {
		logger.Warnf(ctx, "[shell] control file read failed: jobId=%s file=%s err=%v", jobID, path, err)
		return nil, false, false
	}
	logger.Debugf(ctx, "[shell] control file read: jobId=%s file=%s bytes=%d lines=%d",
		jobID, path, len(result.Content), strings.Count(result.Content, "\n"))
	for idx, line := range strings.Split(result.Content, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		if line == controlStopLoop {
			stopLoop = true
			continue
		}
		if line == controlStopWorkflow {
			stopWorkflow = true
			continue
		}
		// Base64-encoded value from quartet_set helper
		if strings.HasPrefix(line, "B64:") {
			if k, v, ok := strings.Cut(line[4:], "="); ok && k != "" {
				if decoded, err := base64.StdEncoding.DecodeString(v); err == nil {
					if vars == nil {
						vars = make(map[string]string)
					}
					vars[k] = string(decoded)
				} else {
					logger.Warnf(ctx, "[shell] control file decode failed: jobId=%s file=%s line=%d key=%s err=%v", jobID, path, idx+1, k, err)
				}
			}
			continue
		}
		// Plain key=value (legacy / manual echo)
		if k, v, ok := strings.Cut(line, "="); ok && k != "" {
			if vars == nil {
				vars = make(map[string]string)
			}
			vars[k] = v
		}
	}
	return
}

// shellOutputTail returns the last maxBytes of the combined stdout+stderr for
// use in failure log lines. It snaps to a rune boundary so the truncated tail
// never splits a multi-byte UTF-8 sequence, and strips trailing whitespace so
// %q-formatted output in a single log line stays compact.
func shellOutputTail(output string, maxBytes int) string {
	trimmed := strings.TrimRight(output, " \t\r\n")
	if len(trimmed) <= maxBytes {
		return trimmed
	}
	tail := trimmed[len(trimmed)-maxBytes:]
	for i := 0; i < len(tail) && i < 4; i++ {
		if (tail[i] & 0xC0) != 0x80 {
			return tail[i:]
		}
	}
	return tail
}

// shellFailureMessage builds the human-readable failure summary persisted on
// IterationResult.Error / JobProgress.LastError. The raw exec error (e.g.
// "exit status 1") is too generic on its own to be actionable, so we append
// the tail of stdout+stderr that actually carries the root cause (timeouts,
// "command not found" lines, error JSON, etc.). Returning just the exec
// error when there is no captured output keeps short messages tidy.
func shellFailureMessage(cmdErr error, outputTail string) string {
	if cmdErr == nil {
		return outputTail
	}
	if outputTail == "" {
		return cmdErr.Error()
	}
	return fmt.Sprintf("%s: %s", cmdErr.Error(), outputTail)
}
