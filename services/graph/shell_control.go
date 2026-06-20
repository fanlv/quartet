package graph

import (
	"encoding/base64"
	"fmt"
	"sort"
	"strings"
)

// Shell output variable protocol for Graph nodes (§1 输出变量契约). The format
// is the same control-file protocol the Loop engine uses, but this is an
// INDEPENDENT implementation: the graph module must not import services/job
// (two engines are physically isolated, see AGENTS.md).

const (
	graphControlStopLoop     = "STOP_LOOP"
	graphControlStopWorkflow = "STOP_WORKFLOW"
)

// graphShellHelpers is injected into every Graph Shell script so users can call
// quartet_set / quartet_break / quartet_return instead of writing the control
// file by hand. Values are base64-encoded to safely carry empty values,
// whitespace, and '=' characters. Kept byte-identical in behaviour to the Loop
// helper but defined independently here.
//
// Inside a loop body, the engine also exports the innermost loop's iteration
// context as environment variables (and as {{...}} placeholders):
//   - QUARTET_LOOP_INDEX        0-based index of the current round
//   - QUARTET_LOOP_FIXED_COUNT  the loop's fixed count (empty for until loops)
//   - QUARTET_LOOP_MAX_ITERS    the max-iterations backstop
const graphShellHelpers = `
# quartet built-in helpers (graph engine)
quartet_set() { echo "[quartet] quartet_set key=$1 value=$2" >&2; printf '%s\n' "B64:$1=$(printf '%s' "$2" | base64 -w0 2>/dev/null || printf '%s' "$2" | base64 | tr -d '\n')" >> "$QUARTET_CONTROL"; }
quartet_break() { echo "[quartet] quartet_break" >&2; printf '%s\n' "STOP_LOOP" >> "$QUARTET_CONTROL"; }
quartet_return() { echo "[quartet] quartet_return" >&2; printf '%s\n' "STOP_WORKFLOW" >> "$QUARTET_CONTROL"; }
quartet_stop() { echo "[quartet] quartet_stop" >&2; quartet_break; }
export -f quartet_set quartet_break quartet_return quartet_stop
`

// ShellControlResult is the parsed outcome of a Graph Shell node's control file.
type ShellControlResult struct {
	Variables    map[string]string
	StopLoop     bool
	StopWorkflow bool
}

// ShellControlError describes a Shell output protocol violation. Callers attach
// stdout/stderr/exit code; this carries the variable and reason.
type ShellControlError struct {
	Variable string
	Message  string
}

func (e *ShellControlError) Error() string { return e.Message }

// ParseShellControl parses a Graph Shell control file body and validates the
// produced output variables against the declared set.
//
// Each non-empty line (after TrimSpace) is one of:
//   - "STOP_LOOP"          → StopLoop = true
//   - "STOP_WORKFLOW"      → StopWorkflow = true
//   - "B64:key=base64val"  → base64-decoded value (written by quartet_set)
//   - "key=value"          → plain text value (legacy compatibility)
//
// Validation (§1): produced variable names must be valid and non-reserved; the
// produced set must EXACTLY match declared (no undeclared, no missing); a
// base64 decode failure fails the node. STOP_LOOP / STOP_WORKFLOW are control
// signals and are not subject to the declared-set check.
//
// Note: the caller is responsible for the contextual rule that STOP_LOOP is
// only legal inside a loop container (a non-loop STOP_LOOP fails the node);
// that requires scheduler/scope context not available to this pure parser.
func ParseShellControl(controlBody string, declared []string) (*ShellControlResult, *ShellControlError) {
	declaredSet := make(map[string]struct{}, len(declared))
	for _, name := range declared {
		declaredSet[name] = struct{}{}
	}

	res := &ShellControlResult{Variables: make(map[string]string)}
	for _, line := range strings.Split(controlBody, "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		switch line {
		case graphControlStopLoop:
			res.StopLoop = true
			continue
		case graphControlStopWorkflow:
			res.StopWorkflow = true
			continue
		}

		var name, value string
		if rest, ok := strings.CutPrefix(line, "B64:"); ok {
			k, v, found := strings.Cut(rest, "=")
			if !found || k == "" {
				return nil, &ShellControlError{Message: fmt.Sprintf("malformed control line (missing key=value): %q", line)}
			}
			decoded, err := base64.StdEncoding.DecodeString(v)
			if err != nil {
				return nil, &ShellControlError{
					Variable: k,
					Message:  fmt.Sprintf("control file base64 decode failed for variable %q: %v", k, err),
				}
			}
			name, value = k, string(decoded)
		} else {
			k, v, found := strings.Cut(line, "=")
			if !found || k == "" {
				// A non key=value, non-signal line is ignored (shells may write
				// arbitrary diagnostic lines); only declared-set completeness is
				// enforced below.
				continue
			}
			name, value = k, v
		}

		if isReservedVar(name) {
			return nil, &ShellControlError{
				Variable: name,
				Message:  fmt.Sprintf("shell wrote reserved variable name %q (names starting with '_' or 'QUARTET_' are reserved)", name),
			}
		}
		if !isValidVarName(name) {
			return nil, &ShellControlError{
				Variable: name,
				Message:  fmt.Sprintf("shell wrote invalid variable name %q (must match [A-Za-z_][A-Za-z0-9_]*)", name),
			}
		}
		if _, ok := declaredSet[name]; !ok {
			return nil, &ShellControlError{
				Variable: name,
				Message:  fmt.Sprintf("shell produced undeclared variable %q", name),
			}
		}
		// Same name on multiple lines: last wins.
		res.Variables[name] = value
	}

	var missing []string
	for _, name := range declared {
		if _, ok := res.Variables[name]; !ok {
			missing = append(missing, name)
		}
	}
	if len(missing) > 0 {
		sort.Strings(missing)
		return nil, &ShellControlError{
			Variable: missing[0],
			Message:  fmt.Sprintf("shell is missing declared output variable(s): %s", strings.Join(missing, ", ")),
		}
	}

	return res, nil
}
