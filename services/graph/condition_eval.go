package graph

import (
	"fmt"
	"strconv"
	"strings"
	"unicode"
)

// Runtime evaluation of a condition expression (§1 条件表达式 + §3 变量引用语义).
// condition.go produces the AST statically at save time; this file evaluates it
// against an instance's visible variable snapshot at run time.
//
// All values are strings (the design has no number/bool type). Ordered
// comparisons (`>`/`>=`/`<`/`<=`) use Go's native string comparison, which for
// valid UTF-8 is byte-wise and therefore equals Unicode code-point
// lexicographic order — so "10" > "9" is false ('1' < '9').

// CondEvalInput carries everything an expression needs to resolve its
// operands at run time.
type CondEvalInput struct {
	// Variables is the instance's visible variable snapshot (named outputs,
	// _last_assistant_msg, aliases and initial variables already merged).
	Variables map[string]string
	// Disabled holds variable names toggled off. A disabled variable
	// participates in comparisons as the empty string (§3), regardless of any
	// value it might still carry in Variables.
	Disabled map[string]struct{}
	// Pruned optionally records variables known to belong to a pruned upstream
	// (never produced). It is used only to label the failure ("pruned" vs
	// "unknown"); either way an absent variable fails evaluation.
	Pruned map[string]struct{}
}

// CondEvalError is a runtime condition failure carrying the full context the
// error-display spec (§4) requires: the expression text, the offending
// variable, its state, and the comparison operator/options in play.
type CondEvalError struct {
	Expr        string
	Var         string
	State       string // "unknown" | "pruned"
	Op          string
	IgnoreCase  bool
	IgnoreSpace bool
	Message     string
}

func (e *CondEvalError) Error() string { return e.Message }

// EvaluateConditionExpr evaluates a pre-parsed AST. exprText is only used to
// annotate errors.
func EvaluateConditionExpr(exprText string, root ConditionExpr, in CondEvalInput) (bool, *CondEvalError) {
	res, err := evalExpr(root, &in)
	if err != nil {
		err.Expr = exprText
		return false, err
	}
	return res, nil
}

// EvaluateCondition parses exprText and evaluates it in one call. A parse error
// (which should not happen for save-validated configs) is surfaced as a
// CondEvalError so callers have a single error type to handle.
func EvaluateCondition(exprText string, in CondEvalInput) (bool, *CondEvalError) {
	root, perr := ParseCondition(exprText)
	if perr != nil {
		return false, &CondEvalError{Expr: exprText, Message: fmt.Sprintf("condition parse failed: %v", perr)}
	}
	return EvaluateConditionExpr(exprText, root, in)
}

func evalExpr(e ConditionExpr, in *CondEvalInput) (bool, *CondEvalError) {
	switch n := e.(type) {
	case *CondBinary:
		left, err := evalExpr(n.Left, in)
		if err != nil {
			return false, err
		}
		// Short-circuit: 且 stops on false, 或 stops on true. This is
		// deterministic and reproducible across runs/recovery.
		switch n.Op {
		case "且":
			if !left {
				return false, nil
			}
		case "或":
			if left {
				return true, nil
			}
		}
		return evalExpr(n.Right, in)
	case *CondNot:
		x, err := evalExpr(n.X, in)
		if err != nil {
			return false, err
		}
		return !x, nil
	case *CondCompare:
		return evalCompare(n, in)
	default:
		return false, &CondEvalError{Message: fmt.Sprintf("unsupported condition node %T", e)}
	}
}

func evalCompare(c *CondCompare, in *CondEvalInput) (bool, *CondEvalError) {
	left, err := in.resolve(c.Left)
	if err != nil {
		err.Op, err.IgnoreCase, err.IgnoreSpace = c.Op, c.IgnoreCase, c.IgnoreSpace
		return false, err
	}
	// Unary (postfix) operators have no right operand.
	if c.Op == opIsEven {
		left = applyCompareOptions(left, c.IgnoreSpace, c.IgnoreCase)
		return isEvenInteger(left), nil
	}
	right, err := in.resolve(c.Right)
	if err != nil {
		err.Op, err.IgnoreCase, err.IgnoreSpace = c.Op, c.IgnoreCase, c.IgnoreSpace
		return false, err
	}
	// Comparison options: remove whitespace first, then case-fold (fixed order
	// so results are replayable — §1 比较选项).
	left = applyCompareOptions(left, c.IgnoreSpace, c.IgnoreCase)
	right = applyCompareOptions(right, c.IgnoreSpace, c.IgnoreCase)

	switch c.Op {
	case "==":
		return left == right, nil
	case "!=":
		return left != right, nil
	case ">":
		return left > right, nil
	case ">=":
		return left >= right, nil
	case "<":
		return left < right, nil
	case "<=":
		return left <= right, nil
	case "StartWith":
		return strings.HasPrefix(left, right), nil
	case "EndWith":
		return strings.HasSuffix(left, right), nil
	default:
		return false, &CondEvalError{Op: c.Op, Message: fmt.Sprintf("unsupported comparison operator %q", c.Op)}
	}
}

// resolve turns an operand into its runtime string value. A literal is returned
// verbatim. A variable is resolved against the snapshot: disabled → empty
// string (takes precedence over any stored value); present → its value; absent
// → evaluation failure (unknown or pruned).
func (in *CondEvalInput) resolve(op CondOperand) (string, *CondEvalError) {
	if !op.IsVar {
		return op.Lit, nil
	}
	name := op.Var
	if _, off := in.Disabled[name]; off {
		return "", nil
	}
	if val, ok := in.Variables[name]; ok {
		return val, nil
	}
	state := "unknown"
	if _, pruned := in.Pruned[name]; pruned {
		state = "pruned"
	}
	return "", &CondEvalError{
		Var:     name,
		State:   state,
		Message: fmt.Sprintf("variable {{%s}} is %s at evaluation time and cannot be used in a condition", name, state),
	}
}

// isEvenInteger reports whether s parses as an integer with an even value.
// A value that cannot be parsed as an integer (empty, non-numeric, float, or
// out of int64 range) is treated as false rather than an evaluation error, per
// the 是偶数 operator semantics. Surrounding whitespace is tolerated.
func isEvenInteger(s string) bool {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return false
	}
	return n%2 == 0
}

// applyCompareOptions implements the two per-comparison options in the fixed
// order documented in §1: strip all Unicode whitespace first, then case-fold.
// Case folding uses strings.ToLower, which is deterministic.
func applyCompareOptions(s string, ignoreSpace, ignoreCase bool) string {
	if ignoreSpace {
		s = strings.Map(func(r rune) rune {
			if unicode.IsSpace(r) {
				return -1
			}
			return r
		}, s)
	}
	if ignoreCase {
		s = strings.ToLower(s)
	}
	return s
}
