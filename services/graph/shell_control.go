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
// Validation (§1, relaxed): produced variable names must still be valid and
// non-reserved, and a base64 decode failure still fails the node. For a Shell
// node, declaration is OPTIONAL and does NOT gate output: EVERY quartet_set
// variable flows downstream whether or not it was declared, so a script can
// publish variables ad hoc and have them visible to later nodes without
// pre-declaring them on the node. The declared set is used only for the
// completeness check below — every DECLARED variable must still be produced
// (a missing declared output fails the node). STOP_LOOP / STOP_WORKFLOW are
// control signals, not subject to either check.
//
// Trade-off: because undeclared outputs are no longer dropped, the save-time
// parallel-writer conflict check (validateOutputConflicts) — which only looks
// at DECLARED names — cannot catch two parallel Shell nodes that quartet_set
// the same undeclared variable. That collision is the author's responsibility;
// it mirrors the intentionally permissive "set whatever you want" Shell model.
//
// Note: STOP_LOOP is a scope-dependent signal; the scheduler applies it inside
// a loop container and drops it (no-op) outside one. That contextual rule needs
// scheduler/scope context not available to this pure parser.
func ParseShellControl(controlBody string, declared []string) (*ShellControlResult, *ShellControlError) {
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
		// Permissive contract: every produced variable flows downstream,
		// declared or not. Declaration on a Shell node is optional and only
		// drives the completeness check below. Same name on multiple lines:
		// last wins.
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
