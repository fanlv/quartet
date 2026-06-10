package model

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const MaxFlowDepth = 5

// MigrateLoopConfig converts a legacy LoopConfig (with IterationCount + Rounds)
// into the new Flow tree format. If Flow is already populated, this is a no-op.
func MigrateLoopConfig(cfg *LoopConfig) {
	if cfg == nil {
		return
	}
	if len(cfg.Flow) > 0 {
		return
	}
	if len(cfg.Rounds) == 0 {
		return
	}

	children := make([]FlowNode, len(cfg.Rounds))
	for i, r := range cfg.Rounds {
		children[i] = FlowNode{
			ID:          NewFlowNodeID(),
			Type:        FlowNodeTypeStep,
			Label:       fmt.Sprintf("Round %d", i+1),
			Message:     r.Message,
			RepeatCount: r.RepeatCount,
			RoundMode:   r.RoundMode,
			RoundType:   r.RoundType,
			ScriptID:    r.ScriptID,
			ScriptName:  r.ScriptName,
		}
	}

	iterCount := cfg.IterationCount
	if iterCount < 1 {
		iterCount = 1
	}

	cfg.Flow = []FlowNode{{
		ID:             NewFlowNodeID(),
		Type:           FlowNodeTypeGroup,
		Label:          "主循环",
		IterationCount: iterCount,
		Children:       children,
	}}
}

// CalcTotalSteps recursively computes the total number of executable steps.
func CalcTotalSteps(nodes []FlowNode) int {
	total := 0
	for _, n := range nodes {
		switch n.Type {
		case FlowNodeTypeStep:
			rc := n.RepeatCount
			if rc < 1 {
				rc = 1
			}
			total += rc
		case FlowNodeTypeGroup:
			ic := n.IterationCount
			if ic < 1 {
				ic = 1
			}
			total += ic * CalcTotalSteps(n.Children)
		}
	}
	return total
}

// CountFlowNodes returns the total number of nodes (steps + groups) in the
// flow tree. Mirrors the frontend countNodes helper so the summary surfaced
// in user_input loop_start entries matches the "N nodes" label shown in the
// Loop Config panel.
func CountFlowNodes(nodes []FlowNode) int {
	count := 0
	for _, n := range nodes {
		count++
		if n.Type == FlowNodeTypeGroup {
			count += CountFlowNodes(n.Children)
		}
	}
	return count
}

// ComparePaths does lexicographic comparison of two paths.
// Returns -1 if a < b, 0 if equal, 1 if a > b.
func ComparePaths(a, b []int) int {
	for i := 0; i < len(a) && i < len(b); i++ {
		if a[i] < b[i] {
			return -1
		}
		if a[i] > b[i] {
			return 1
		}
	}
	if len(a) < len(b) {
		return -1
	}
	if len(a) > len(b) {
		return 1
	}
	return 0
}

// EqualPaths returns true if two paths are element-wise equal.
func EqualPaths(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// NextStepPath computes the path of the next leaf step after currentPath.
// Returns nil if there is no next step (all done).
func NextStepPath(nodes []FlowNode, currentPath []int) []int {
	var allPaths [][]int
	enumStepPaths(nodes, nil, &allPaths)

	if currentPath == nil {
		if len(allPaths) > 0 {
			return allPaths[0]
		}
		return nil
	}

	for i, p := range allPaths {
		if EqualPaths(p, currentPath) {
			if i+1 < len(allPaths) {
				return allPaths[i+1]
			}
			return nil
		}
	}
	return nil
}

// StepPathSet is a precomputed lookup of every valid leaf step path in a flow.
// Used when reconciling a job's bookkeeping (Resume.NextPath /
// Progress.CurrentPath / recorded Results) after the LoopConfig is edited: a
// path that no longer resolves to a real step (its step was removed or the tree
// was restructured) must be discarded so resume falls back to a recomputed
// position. Building it enumerates the flow once; each Contains check is then
// O(depth) rather than re-enumerating the whole tree per path, which matters
// for a long Results list. An empty path is always valid ("before the first
// step", i.e. start from the beginning).
type StepPathSet struct {
	keys map[string]struct{}
}

// BuildStepPathSet enumerates all leaf step paths in nodes once into a set.
func BuildStepPathSet(nodes []FlowNode) StepPathSet {
	var allPaths [][]int
	enumStepPaths(nodes, nil, &allPaths)
	keys := make(map[string]struct{}, len(allPaths))
	for _, p := range allPaths {
		keys[stepPathKey(p)] = struct{}{}
	}
	return StepPathSet{keys: keys}
}

// Contains reports whether path resolves to a real leaf step in the flow the
// set was built from. Mirrors IsValidStepPath's empty-path semantics.
func (s StepPathSet) Contains(path []int) bool {
	if len(path) == 0 {
		return true
	}
	_, ok := s.keys[stepPathKey(path)]
	return ok
}

// stepPathKey renders a path as a stable string key (e.g. "0.1.0.2"). Kept
// local to the model package so StepPathSet stays self-contained.
func stepPathKey(path []int) string {
	if len(path) == 0 {
		return ""
	}
	var b strings.Builder
	for i, p := range path {
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(strconv.Itoa(p))
	}
	return b.String()
}

// enumStepPaths enumerates all leaf step paths in execution order.
func enumStepPaths(nodes []FlowNode, basePath []int, out *[][]int) {
	for i, n := range nodes {
		switch n.Type {
		case FlowNodeTypeStep:
			rc := n.RepeatCount
			if rc < 1 {
				rc = 1
			}
			for r := 0; r < rc; r++ {
				p := make([]int, len(basePath)+2)
				copy(p, basePath)
				p[len(basePath)] = i
				p[len(basePath)+1] = r
				*out = append(*out, p)
			}
		case FlowNodeTypeGroup:
			ic := n.IterationCount
			if ic < 1 {
				ic = 1
			}
			for iter := 0; iter < ic; iter++ {
				groupPath := make([]int, len(basePath)+2)
				copy(groupPath, basePath)
				groupPath[len(basePath)] = i
				groupPath[len(basePath)+1] = iter
				enumStepPaths(n.Children, groupPath, out)
			}
		}
	}
}

// FlowDefaults carries request-level defaults that are backfilled onto flow
// steps which don't specify their own agent/model/ACP mode.
type FlowDefaults struct {
	AgentType string
	ModelID   string
	ACPMode   string
}

// NormalizeAndValidateLoopConfig brings a LoopConfig into its canonical
// executable form and validates it in one call. The order is load-bearing:
//
//  1. MigrateLoopConfig — fold legacy IterationCount+Rounds into the Flow tree.
//  2. BackfillFlowDefaults — inherit request-level agent/model onto steps that
//     omit their own.
//  3. ValidateFlow — enforce structure.
//
// Backfill must run BEFORE validate: ValidateFlow requires a non-empty
// AgentType on session-creating steps (beforeRound/eachRepeat), which may only
// be present after the request-level default is filled in. Callers must use
// this entry point instead of composing the three steps by hand, so the order
// can't drift.
func NormalizeAndValidateLoopConfig(cfg *LoopConfig, defaults FlowDefaults) error {
	if cfg == nil {
		return fmt.Errorf("loopConfig is required")
	}
	MigrateLoopConfig(cfg)
	if len(cfg.Flow) == 0 {
		return fmt.Errorf("loopConfig.flow must not be empty")
	}
	BackfillFlowDefaults(cfg.Flow, defaults.AgentType, defaults.ModelID, defaults.ACPMode)
	return ValidateFlow(cfg.Flow, 0)
}

// ValidateFlow recursively validates the flow tree structure.
func ValidateFlow(nodes []FlowNode, depth int) error {
	if depth >= MaxFlowDepth {
		return fmt.Errorf("flow nesting depth exceeds maximum of %d", MaxFlowDepth)
	}
	if len(nodes) == 0 {
		return fmt.Errorf("flow must contain at least one node")
	}
	for i, n := range nodes {
		switch n.Type {
		case FlowNodeTypeStep:
			switch n.RoundType {
			case "", RoundTypePrompt:
				// agentType is required when creating a new session with a prompt step.
				// Empty RoundType is accepted as legacy prompt format.
				needsAgent := n.RoundMode != RoundModeNone && n.RoundMode != ""
				if needsAgent && n.AgentType == "" {
					return fmt.Errorf("step node [%d] at depth %d: agentType is required when creating a new session", i, depth)
				}
				if strings.TrimSpace(n.Message) == "" {
					return fmt.Errorf("step node [%d] at depth %d: prompt step requires message", i, depth)
				}
			case RoundTypeEvaluator:
				// An evaluator must live inside a group (a top-level STOP_LOOP
				// has nothing to break) and needs a non-empty evaluation prompt;
				// the agent requirement matches a prompt step.
				if depth == 0 {
					return fmt.Errorf("step node [%d] at depth %d: evaluator must be inside a group", i, depth)
				}
				needsAgent := n.RoundMode != RoundModeNone && n.RoundMode != ""
				if needsAgent && n.AgentType == "" {
					return fmt.Errorf("step node [%d] at depth %d: agentType is required when creating a new session", i, depth)
				}
				if strings.TrimSpace(n.Message) == "" {
					return fmt.Errorf("step node [%d] at depth %d: evaluator step requires message", i, depth)
				}
			case RoundTypeShell:
				if n.ScriptID == "" && n.Message == "" {
					return fmt.Errorf("step node [%d] at depth %d: shell step requires scriptId or message", i, depth)
				}
			default:
				return fmt.Errorf("step node [%d] at depth %d: unknown roundType %q", i, depth, n.RoundType)
			}
		case FlowNodeTypeGroup:
			if n.IterationCount < 1 {
				return fmt.Errorf("group node [%d] at depth %d: iterationCount must be >= 1", i, depth)
			}
			if len(n.Children) == 0 {
				return fmt.Errorf("group node [%d] at depth %d: must have at least one child", i, depth)
			}
			if err := ValidateFlow(n.Children, depth+1); err != nil {
				return err
			}
		default:
			return fmt.Errorf("node [%d] at depth %d: unknown type %q", i, depth, n.Type)
		}
	}

	// At the top level (depth 0), the first reachable step must create a new session.
	if depth == 0 {
		firstMode := firstStepRoundMode(nodes)
		if firstMode == RoundModeNone || firstMode == "" {
			return fmt.Errorf("the first step in the flow must create a new session (roundMode must be beforeRound or eachRepeat)")
		}
	}

	return nil
}

// firstStepRoundMode walks the flow tree depth-first and returns the RoundMode
// of the first step node encountered.
func firstStepRoundMode(nodes []FlowNode) RoundMode {
	for _, n := range nodes {
		switch n.Type {
		case FlowNodeTypeStep:
			return n.RoundMode
		case FlowNodeTypeGroup:
			if rm := firstStepRoundMode(n.Children); rm != "" {
				return rm
			}
		}
	}
	return ""
}

// NewFlowNodeID generates a unique ID for a flow node.
func NewFlowNodeID() string {
	t := time.Now()
	var buf [3]byte
	rand.Read(buf[:])
	return fmt.Sprintf("fn-%s-%s", t.Format("20060102150405"), hex.EncodeToString(buf[:]))
}

// FindFirstStepMessage walks the flow tree and returns the message of the first prompt step found.
func FindFirstStepMessage(nodes []FlowNode) string {
	for _, n := range nodes {
		switch n.Type {
		case FlowNodeTypeStep:
			if n.Message != "" {
				return n.Message
			}
		case FlowNodeTypeGroup:
			if msg := FindFirstStepMessage(n.Children); msg != "" {
				return msg
			}
		}
	}
	return ""
}

// IsTemplateVar returns true if the string looks like an unresolved template
// variable, e.g. "{{some_var}}" with optional surrounding whitespace.
func IsTemplateVar(s string) bool {
	// Check if the entire trimmed string is a single {{...}} pattern,
	// or if the string consists solely of template variables.
	trimmed := strings.TrimSpace(s)
	if trimmed == "" {
		return false
	}
	// Remove all {{...}} patterns; if nothing meaningful remains, it's all templates.
	cleaned := strings.TrimSpace(templateVarRe.ReplaceAllString(trimmed, ""))
	return cleaned == ""
}

// templateVarRe matches {{variable_name}} patterns.
var templateVarRe = regexp.MustCompile(`\{\{[^}]+\}\}`)

// FindFlowTitle extracts a meaningful title from the flow tree.
// It prefers the first non-template step message, then falls back to
// labels and script names from the tree.
func FindFlowTitle(nodes []FlowNode) string {
	// First pass: look for a non-template step message.
	if msg := findFirstNonTemplateMessage(nodes); msg != "" {
		return msg
	}
	// Second pass: collect labels and scriptNames as fallback.
	if title := findFirstLabel(nodes); title != "" {
		return title
	}
	// Last resort: return the raw first message (may be a template variable).
	return FindFirstStepMessage(nodes)
}

// findFirstNonTemplateMessage returns the first step message that is not
// purely composed of {{...}} template variables.
func findFirstNonTemplateMessage(nodes []FlowNode) string {
	for _, n := range nodes {
		switch n.Type {
		case FlowNodeTypeStep:
			if n.Message != "" && !IsTemplateVar(n.Message) {
				return n.Message
			}
		case FlowNodeTypeGroup:
			if msg := findFirstNonTemplateMessage(n.Children); msg != "" {
				return msg
			}
		}
	}
	return ""
}

// findFirstLabel returns the first non-empty label or scriptName in the tree.
func findFirstLabel(nodes []FlowNode) string {
	for _, n := range nodes {
		switch n.Type {
		case FlowNodeTypeStep:
			if n.ScriptName != "" {
				return n.ScriptName
			}
			if n.Label != "" {
				return n.Label
			}
		case FlowNodeTypeGroup:
			// Check children first for more specific labels.
			if label := findFirstLabel(n.Children); label != "" {
				return label
			}
			if n.Label != "" {
				return n.Label
			}
		}
	}
	return ""
}

// CopyPath returns a deep copy of a path slice.
func CopyPath(p []int) []int {
	if p == nil {
		return nil
	}
	c := make([]int, len(p))
	copy(c, p)
	return c
}
