package graph

import (
	"time"

	"github.com/fanlv/quartet/types/consts"
	"github.com/fanlv/quartet/types/model"
)

// Loop iteration variables (§3 循环变量语义 — iteration context injection).
//
// A node inside a loop body can read three engine-injected values describing
// its enclosing (innermost) loop: the current 0-based round index, the loop's
// fixed count (empty for until-mode loops), and the max-iterations backstop.
// They are exposed identically across all three channels — Shell environment
// variables, {{...}} text substitution (Shell/Prompt), and condition
// evaluation (If-Else / 直到条件) — under the same QUARTET_LOOP_* names.
//
// These values are computed on the fly from the live loop scope at decision /
// enqueue time; they are NEVER written into an instance's persisted visible
// snapshot. That keeps them from leaking out of the loop via the round-end
// accumulated snapshot (finishLoop's external contribution) or via join merges,
// and makes resume trivially correct — a reset loop re-runs from round 0 and
// recomputes the values from the freshly rebuilt scope.
const (
	// reservedQuartetPrefix is the engine namespace. isReservedVar rejects any
	// user-declared variable under it (output / alias / initial), so an injected
	// QUARTET_LOOP_* value can never collide with a user variable.
	reservedQuartetPrefix = "QUARTET_"

	loopVarIndex      = "QUARTET_LOOP_INDEX"       // 0-based current round
	loopVarFixedCount = "QUARTET_LOOP_FIXED_COUNT" // fixed-mode count; "" for until
	loopVarMaxIters   = "QUARTET_LOOP_MAX_ITERS"   // effective max-iterations backstop
)

// loopIterationVars returns the QUARTET_LOOP_* values for the innermost active
// loop scope, or nil when the scope is the main graph (no loop context). Only
// the innermost loop is exposed; node IDs are not shell-identifier-safe so we
// deliberately do not emit per-loop-id-suffixed names for ancestor loops.
func loopIterationVars(scope *scopeRun) map[string]string {
	if scope == nil || scope.container == "" {
		return nil
	}
	fixed := ""
	if scope.loopNode.Config.LoopMode == "" || scope.loopNode.Config.LoopMode == model.GraphLoopModeFixed {
		// Fixed-mode count as a decimal. Until-mode leaves it empty (an empty
		// value, not omitted, so a condition referencing it does not hard-fail on
		// an unknown variable — and reads as "no fixed count" rather than "0").
		fixed = itoa(scope.loopNode.Config.FixedCount)
	}
	return map[string]string{
		loopVarIndex:      itoa(scope.iterIndex),
		loopVarFixedCount: fixed,
		loopVarMaxIters:   itoa(scope.maxIters),
	}
}

// runtimeVars returns the full set of engine-injected runtime variables overlaid
// onto a node's visible snapshot at dispatch / condition-eval time: the
// QUARTET_LOOP_* loop iteration context (only inside a loop body) plus the
// _current_time builtin (always). Like the loop vars, _current_time is computed
// on the fly and NEVER written into a persisted snapshot — so it stays out of
// round-end accumulated snapshots and join merges, and a resume recomputes it
// from scratch.
//
// The timestamp is stamped once here. On the execution path the result is
// captured on the ready item at enqueue time and reused for both the display
// substitution and the actual run, so the script shown in the sidebar and the
// script that executes carry the identical timestamp.
func runtimeVars(scope *scopeRun) map[string]string {
	out := loopIterationVars(scope)
	if out == nil {
		out = make(map[string]string, 1)
	}
	out[consts.VarCurrentTime] = time.Now().Format(time.RFC3339)
	return out
}

// withLoopVars returns a fresh clone of base overlaid with the engine runtime
// vars for scope (QUARTET_LOOP_* iteration context + _current_time). Used only
// for ephemeral substitution / condition-eval maps so the caller's persisted
// snapshot stays pristine. The timestamp is stamped at call time.
func withLoopVars(base map[string]string, scope *scopeRun) map[string]string {
	rv := runtimeVars(scope)
	out := make(map[string]string, len(base)+len(rv))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range rv {
		out[k] = v
	}
	return out
}

// mergeLoopVars overlays loopVars onto a clone of base (no scope lookup). Used
// on the execution path where the loop vars were precomputed at enqueue time
// and carried on the ready item. A nil loopVars returns base unchanged (no
// clone) since the caller does not mutate it.
func mergeLoopVars(base, loopVars map[string]string) map[string]string {
	if len(loopVars) == 0 {
		return base
	}
	out := make(map[string]string, len(base)+len(loopVars))
	for k, v := range base {
		out[k] = v
	}
	for k, v := range loopVars {
		out[k] = v
	}
	return out
}
