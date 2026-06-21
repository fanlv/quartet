package graph

import (
	"fmt"
	"strings"

	"github.com/fanlv/quartet/types/model"
)

// validateConfig runs the full §1 graph legality check and returns ALL
// violations at once, each carrying location info (NodeID / EdgeID / Variable /
// ConfigKey) so the frontend can pin them to the offending element. An empty
// slice means the config is valid.
//
// This is a save-time STATIC check only. Runtime concerns (variable existence
// during evaluation, session fork/replay, scheduling) are validated by the
// execution engine, not here.
func validateConfig(cfg *model.GraphConfig) []model.GraphValidationError {
	v := &validator{cfg: cfg}
	v.run()
	return v.errs
}

const reservedLastAssistant = "_last_assistant_msg"

// Run-config bounds (§1 运行配置).
const (
	maxConcurrency  = 16
	maxLoopMaxIters = 1000
)

type validator struct {
	cfg  *model.GraphConfig
	errs []model.GraphValidationError

	nodesByID map[string]*model.GraphNode
	// scopes maps a container ID ("" = main graph) to the nodes inside it.
	scopes map[string][]*model.GraphNode
	// outEdges/inEdges only count edges whose endpoints share a scope.
	outEdges map[string][]*model.GraphEdge // by source node ID
	inDeg    map[string]int                // by target node ID (intra-scope)
}

func (v *validator) addErr(e model.GraphValidationError) { v.errs = append(v.errs, e) }

func (v *validator) structErr(msg string) {
	v.addErr(model.GraphValidationError{Type: model.GraphValidationErrorTypeStructure, Message: msg})
}

func (v *validator) nodeErr(nodeID, msg string) {
	v.addErr(model.GraphValidationError{Type: model.GraphValidationErrorTypeNode, NodeID: nodeID, Message: msg})
}

func (v *validator) edgeErr(edgeID, msg string) {
	v.addErr(model.GraphValidationError{Type: model.GraphValidationErrorTypeEdge, EdgeID: edgeID, Message: msg})
}

func (v *validator) varErr(nodeID, variable, msg string) {
	v.addErr(model.GraphValidationError{Type: model.GraphValidationErrorTypeVariable, NodeID: nodeID, Variable: variable, Message: msg})
}

func (v *validator) configErr(key, msg string) {
	v.addErr(model.GraphValidationError{Type: model.GraphValidationErrorTypeConfig, ConfigKey: key, Message: msg})
}

func (v *validator) sessionErr(nodeID, msg string) {
	v.addErr(model.GraphValidationError{Type: model.GraphValidationErrorTypeSession, NodeID: nodeID, Message: msg})
}

func (v *validator) run() {
	v.index()
	v.validateRunConfig()
	v.validateInitialVariables()
	v.validateNodeConfigs()
	v.validatePorts()
	v.validateEdgeScopes()
	v.validateStructure()
	v.validateCycles()
	v.validateLoopSubgraphs()
	v.validateFirstAgentNewSession()
	v.validateOutputConflicts()
	v.validateParallelSessionReuse()
}

// --- indexing ---

func (v *validator) index() {
	v.nodesByID = make(map[string]*model.GraphNode, len(v.cfg.Nodes))
	v.scopes = make(map[string][]*model.GraphNode)
	v.outEdges = make(map[string][]*model.GraphEdge)
	v.inDeg = make(map[string]int)

	for i := range v.cfg.Nodes {
		n := &v.cfg.Nodes[i]
		if n.ID == "" {
			v.structErr("node with empty ID is not allowed")
			continue
		}
		if _, dup := v.nodesByID[n.ID]; dup {
			v.nodeErr(n.ID, fmt.Sprintf("duplicate node ID %q", n.ID))
			continue
		}
		v.nodesByID[n.ID] = n
	}
	for id, n := range v.nodesByID {
		v.scopes[n.ParentID] = append(v.scopes[n.ParentID], n)
		_ = id
	}

	for i := range v.cfg.Edges {
		e := &v.cfg.Edges[i]
		src, srcOK := v.nodesByID[e.SourceNodeID]
		dst, dstOK := v.nodesByID[e.TargetNodeID]
		if !srcOK {
			v.edgeErr(e.ID, fmt.Sprintf("edge source node %q does not exist", e.SourceNodeID))
			continue
		}
		if !dstOK {
			v.edgeErr(e.ID, fmt.Sprintf("edge target node %q does not exist", e.TargetNodeID))
			continue
		}
		// Only intra-scope edges participate in degree/adjacency. Cross-scope
		// edges are reported separately by validateEdgeScopes.
		if src.ParentID == dst.ParentID {
			v.outEdges[e.SourceNodeID] = append(v.outEdges[e.SourceNodeID], e)
			v.inDeg[e.TargetNodeID]++
		}
	}
}

func isAgent(t model.GraphNodeType) bool {
	return t == model.GraphNodeTypePrompt || t == model.GraphNodeTypeEvaluator
}

func isBusiness(t model.GraphNodeType) bool {
	switch t {
	case model.GraphNodeTypeShell, model.GraphNodeTypePrompt, model.GraphNodeTypeEvaluator,
		model.GraphNodeTypeIfElse, model.GraphNodeTypeLoop:
		return true
	default:
		return false
	}
}

// isReservedVar reports whether a variable name lies in a namespace the engine
// owns and users may not declare: names starting with '_' (e.g.
// _last_assistant_msg) and the 'QUARTET_' prefix (engine-injected loop
// iteration vars QUARTET_LOOP_*, and the QUARTET_CONTROL shell env). Reserving
// the whole prefix prevents a user output/alias/initial variable from colliding
// with — and being clobbered by — an injected value across all channels.
func isReservedVar(name string) bool {
	return len(name) > 0 && (name[0] == '_' || strings.HasPrefix(name, reservedQuartetPrefix))
}

// --- run config ---

func (v *validator) validateRunConfig() {
	rc := v.cfg.RunConfig
	if rc.ConcurrencyLimit < 0 || rc.ConcurrencyLimit > maxConcurrency {
		v.configErr("concurrencyLimit", fmt.Sprintf("concurrency limit must be between 1 and %d (0 = default), got %d", maxConcurrency, rc.ConcurrencyLimit))
	}
	if rc.DefaultNodeTimeoutSec < 0 {
		v.configErr("defaultNodeTimeoutSec", fmt.Sprintf("default node timeout must be >= 0 (0 = unlimited), got %d", rc.DefaultNodeTimeoutSec))
	}
	if rc.JobTimeoutSec < 0 {
		v.configErr("jobTimeoutSec", fmt.Sprintf("job timeout must be >= 0 (0 = unlimited), got %d", rc.JobTimeoutSec))
	}
	if rc.DefaultLoopMaxIters < 0 || rc.DefaultLoopMaxIters > maxLoopMaxIters {
		v.configErr("defaultLoopMaxIters", fmt.Sprintf("default loop max iterations must be between 1 and %d (0 = default), got %d", maxLoopMaxIters, rc.DefaultLoopMaxIters))
	}
	if rc.InstanceLimit < 0 {
		v.configErr("instanceLimit", fmt.Sprintf("instance limit must be > 0 (0 = default), got %d", rc.InstanceLimit))
	}
	if rc.SnapshotByteLimit < 0 {
		v.configErr("snapshotByteLimit", fmt.Sprintf("snapshot byte limit must be > 0 (0 = default), got %d", rc.SnapshotByteLimit))
	}
}

// --- initial variables ---

func (v *validator) validateInitialVariables() {
	for name := range v.cfg.Variables {
		if isReservedVar(name) {
			v.varErr("", name, fmt.Sprintf("initial variable %q uses the reserved namespace (names starting with '_' (including %q) or 'QUARTET_' are reserved)", name, reservedLastAssistant))
			continue
		}
		if !isValidVarName(name) {
			v.varErr("", name, fmt.Sprintf("initial variable name %q is invalid (must match [A-Za-z_][A-Za-z0-9_]*)", name))
		}
	}
}

// --- per-node config ---

func (v *validator) validateNodeConfigs() {
	for i := range v.cfg.Nodes {
		n := &v.cfg.Nodes[i]
		if n.ID == "" {
			continue
		}
		switch n.Type {
		case model.GraphNodeTypeStart, model.GraphNodeTypeEnd:
			v.validateControlConfig(n)
		case model.GraphNodeTypeShell:
			v.validateOutputDecls(n, false)
		case model.GraphNodeTypePrompt:
			v.validateOutputDecls(n, false)
			v.validateAgentNewSession(n)
		case model.GraphNodeTypeEvaluator:
			v.validateOutputDecls(n, true)
			v.validateAgentNewSession(n)
		case model.GraphNodeTypeIfElse:
			v.validateIfElseConfig(n)
		case model.GraphNodeTypeLoop:
			v.validateLoopConfig(n)
		default:
			v.nodeErr(n.ID, fmt.Sprintf("unknown node type %q", n.Type))
		}
		if n.Config.TimeoutSeconds != nil && *n.Config.TimeoutSeconds < 0 {
			v.nodeErr(n.ID, fmt.Sprintf("node timeout must be >= 0 (0 = unlimited), got %d", *n.Config.TimeoutSeconds))
		}
	}
}

func (v *validator) validateControlConfig(n *model.GraphNode) {
	c := n.Config
	if c.Script != "" || c.Prompt != "" || c.AgentType != "" || c.Condition != "" ||
		c.LoopMode != "" || c.UntilCondition != "" || c.FixedCount != 0 || c.MaxIterations != 0 {
		v.nodeErr(n.ID, fmt.Sprintf("control node %q (%s) must not configure business actions", n.ID, n.Type))
	}
	if len(c.OutputVariables) > 0 || c.LastAssistantAlias != "" {
		v.nodeErr(n.ID, fmt.Sprintf("control node %q (%s) must not declare output variables", n.ID, n.Type))
	}
}

// validateOutputDecls checks output variable declarations + the optional
// _last_assistant_msg alias for shell/prompt/evaluator nodes. requireOutput
// forces >= 1 output variable (evaluator).
func (v *validator) validateOutputDecls(n *model.GraphNode, requireOutput bool) {
	seen := make(map[string]bool)
	for _, name := range n.Config.OutputVariables {
		if isReservedVar(name) {
			v.varErr(n.ID, name, fmt.Sprintf("output variable %q uses the reserved namespace (names starting with '_' (including %q) or 'QUARTET_' are reserved)", name, reservedLastAssistant))
			continue
		}
		if !isValidVarName(name) {
			v.varErr(n.ID, name, fmt.Sprintf("output variable name %q is invalid (must match [A-Za-z_][A-Za-z0-9_]*)", name))
			continue
		}
		if seen[name] {
			v.varErr(n.ID, name, fmt.Sprintf("output variable %q is declared more than once on the same node", name))
			continue
		}
		seen[name] = true
	}
	if requireOutput && len(n.Config.OutputVariables) == 0 {
		v.nodeErr(n.ID, fmt.Sprintf("evaluator node %q must declare at least one output variable", n.ID))
	}
	if alias := n.Config.LastAssistantAlias; alias != "" {
		if isReservedVar(alias) {
			v.varErr(n.ID, alias, fmt.Sprintf("_last_assistant_msg alias %q uses the reserved namespace", alias))
		} else if !isValidVarName(alias) {
			v.varErr(n.ID, alias, fmt.Sprintf("_last_assistant_msg alias %q is invalid (must match [A-Za-z_][A-Za-z0-9_]*)", alias))
		} else if seen[alias] {
			v.varErr(n.ID, alias, fmt.Sprintf("_last_assistant_msg alias %q collides with an explicit output variable on the same node", alias))
		}
	}
}

// validateAgentNewSession enforces that an Agent-class node (prompt/evaluator)
// which creates a NEW session must specify an Agent. The `inherit` strategy is
// exempt: it forks the upstream Agent's session and reuses its Agent/model when
// no override is given, so a missing agentType is legal there.
func (v *validator) validateAgentNewSession(n *model.GraphNode) {
	if n.Config.SessionStrategy == model.GraphSessionStrategyInherit {
		return
	}
	if n.Config.AgentType == "" {
		v.nodeErr(n.ID, fmt.Sprintf("agent node %q creates a new session and must specify an Agent", n.ID))
	}
}

func (v *validator) validateIfElseConfig(n *model.GraphNode) {
	if n.Config.Condition == "" {
		v.nodeErr(n.ID, fmt.Sprintf("if-else node %q must configure a condition expression", n.ID))
	} else if _, err := ParseCondition(n.Config.Condition); err != nil {
		v.addErr(model.GraphValidationError{
			Type:    model.GraphValidationErrorTypeNode,
			NodeID:  n.ID,
			Message: fmt.Sprintf("invalid condition expression: %v", err),
		})
	}
	if len(n.Config.OutputVariables) > 0 || n.Config.LastAssistantAlias != "" {
		v.nodeErr(n.ID, fmt.Sprintf("if-else node %q only routes and must not declare output variables", n.ID))
	}
}

func (v *validator) validateLoopConfig(n *model.GraphNode) {
	c := n.Config
	switch c.LoopMode {
	case model.GraphLoopModeFixed:
		if c.FixedCount < 0 {
			v.nodeErr(n.ID, fmt.Sprintf("loop node %q fixed count must be >= 0, got %d", n.ID, c.FixedCount))
		}
	case model.GraphLoopModeUntil:
		if c.UntilCondition == "" {
			v.nodeErr(n.ID, fmt.Sprintf("loop node %q in 'until' mode must configure an until condition", n.ID))
		} else if _, err := ParseCondition(c.UntilCondition); err != nil {
			v.nodeErr(n.ID, fmt.Sprintf("invalid until condition expression: %v", err))
		}
	default:
		v.nodeErr(n.ID, fmt.Sprintf("loop node %q has unknown loop mode %q (expected 'fixed' or 'until')", n.ID, c.LoopMode))
	}
	if c.MaxIterations < 0 || c.MaxIterations > maxLoopMaxIters {
		v.nodeErr(n.ID, fmt.Sprintf("loop node %q max iterations must be between 1 and %d (0 = default), got %d", n.ID, maxLoopMaxIters, c.MaxIterations))
	}
	if len(c.OutputVariables) > 0 || c.LastAssistantAlias != "" {
		v.nodeErr(n.ID, fmt.Sprintf("loop node %q must not declare output variables (loop output is the round-end snapshot)", n.ID))
	}
}

// --- ports ---

func (v *validator) validatePorts() {
	for i := range v.cfg.Edges {
		e := &v.cfg.Edges[i]
		src, ok := v.nodesByID[e.SourceNodeID]
		if !ok {
			continue
		}
		isBranchPort := e.SourcePort == model.GraphEdgePortYes || e.SourcePort == model.GraphEdgePortNo
		if src.Type == model.GraphNodeTypeIfElse {
			// If-Else may only use yes/no ports.
			if !isBranchPort {
				v.edgeErr(e.ID, fmt.Sprintf("if-else node %q out-edge must use a yes/no port", src.ID))
			}
		} else if isBranchPort {
			v.edgeErr(e.ID, fmt.Sprintf("node %q (%s) must not use yes/no branch ports", src.ID, src.Type))
		}
	}

	// If-Else must have exactly one yes and one no out-edge; evaluator must not
	// branch.
	for i := range v.cfg.Nodes {
		n := &v.cfg.Nodes[i]
		if n.ID == "" {
			continue
		}
		if n.Type == model.GraphNodeTypeIfElse {
			var yes, no int
			for _, e := range v.outEdges[n.ID] {
				switch e.SourcePort {
				case model.GraphEdgePortYes:
					yes++
				case model.GraphEdgePortNo:
					no++
				}
			}
			if yes != 1 || no != 1 {
				v.nodeErr(n.ID, fmt.Sprintf("if-else node %q must have exactly one 'yes' and one 'no' out-edge (got yes=%d, no=%d)", n.ID, yes, no))
			}
		}
		if n.Type == model.GraphNodeTypeEvaluator {
			for _, e := range v.outEdges[n.ID] {
				if e.SourcePort == model.GraphEdgePortYes || e.SourcePort == model.GraphEdgePortNo {
					v.nodeErr(n.ID, fmt.Sprintf("evaluator node %q must not use branch (yes/no) out-edges", n.ID))
					break
				}
			}
		}
	}
}

// --- edge scope (cross-container) ---

func (v *validator) validateEdgeScopes() {
	for i := range v.cfg.Edges {
		e := &v.cfg.Edges[i]
		src, srcOK := v.nodesByID[e.SourceNodeID]
		dst, dstOK := v.nodesByID[e.TargetNodeID]
		if !srcOK || !dstOK {
			continue
		}
		if src.ParentID != dst.ParentID {
			v.edgeErr(e.ID, fmt.Sprintf("edge %q crosses container boundary: source scope %q != target scope %q (loop subgraphs may only connect to the loop node itself from outside)", e.ID, scopeLabel(src.ParentID), scopeLabel(dst.ParentID)))
		}
	}
}

func scopeLabel(parentID string) string {
	if parentID == "" {
		return "main"
	}
	return parentID
}

// --- structure & reachability (main scope) ---

func (v *validator) validateStructure() {
	var starts, ends, business int
	for _, n := range v.scopes[""] {
		switch {
		case n.Type == model.GraphNodeTypeStart:
			starts++
		case n.Type == model.GraphNodeTypeEnd:
			ends++
		case isBusiness(n.Type):
			business++
		}
	}
	if starts == 0 {
		v.structErr("graph must have at least one start node")
	}
	if ends == 0 {
		v.structErr("graph must have at least one end node")
	}
	if business == 0 {
		v.structErr("graph must have at least one business node or loop container reachable from start (a pure start→end graph has a zero progress denominator)")
	}

	// in/out edge requirements per node (intra-scope degrees).
	for i := range v.cfg.Nodes {
		n := &v.cfg.Nodes[i]
		if n.ID == "" {
			continue
		}
		in := v.inDeg[n.ID]
		out := len(v.outEdges[n.ID])
		switch n.Type {
		case model.GraphNodeTypeStart:
			if in != 0 {
				v.nodeErr(n.ID, fmt.Sprintf("start node %q must not have in-edges", n.ID))
			}
			if out == 0 {
				v.nodeErr(n.ID, fmt.Sprintf("start node %q must have at least one out-edge", n.ID))
			}
		case model.GraphNodeTypeEnd:
			if out != 0 {
				v.nodeErr(n.ID, fmt.Sprintf("end node %q must not have out-edges", n.ID))
			}
			if in == 0 {
				v.nodeErr(n.ID, fmt.Sprintf("end node %q must have at least one in-edge", n.ID))
			}
		default:
			// Business nodes always need an in-edge: in the main scope from a
			// start/upstream, inside a loop from the loop's entry start or an
			// upstream body node. (The loop entry marker is a start node, handled
			// by the start case above, so it is exempt from this requirement.)
			if in == 0 {
				v.nodeErr(n.ID, fmt.Sprintf("node %q must have at least one in-edge", n.ID))
			}
			if out == 0 {
				v.nodeErr(n.ID, fmt.Sprintf("node %q must have at least one out-edge (all paths must reach an end)", n.ID))
			}
		}
	}

	// Reachability from starts within the main scope.
	mainEntries := v.entriesOfScope("", true)
	reach := v.reachableFrom(mainEntries, "", "")
	for _, n := range v.scopes[""] {
		if n.Type == model.GraphNodeTypeStart {
			continue
		}
		if !reach[n.ID] {
			v.nodeErr(n.ID, fmt.Sprintf("node %q is not reachable from any start node", n.ID))
		}
	}
	// At least one end reachable.
	endReached := false
	for _, n := range v.scopes[""] {
		if n.Type == model.GraphNodeTypeEnd && reach[n.ID] {
			endReached = true
			break
		}
	}
	if ends > 0 && !endReached {
		v.structErr("no end node is reachable from any start node")
	}

	// Every node must be able to reach an end (no implicit terminal).
	v.checkCanReachEnd("", v.mainEnds())
}

func (v *validator) mainEnds() map[string]bool {
	ends := make(map[string]bool)
	for _, n := range v.scopes[""] {
		if n.Type == model.GraphNodeTypeEnd {
			ends[n.ID] = true
		}
	}
	return ends
}

// checkCanReachEnd reports nodes in the scope that cannot reach any terminal
// (scope-local end node). Reachable-from-entry nodes only.
func (v *validator) checkCanReachEnd(scope string, ends map[string]bool) {
	if len(ends) == 0 {
		return
	}
	// reverse adjacency within scope
	canReach := make(map[string]bool, len(ends))
	for id := range ends {
		canReach[id] = true
	}
	changed := true
	for changed {
		changed = false
		for src, edges := range v.outEdges {
			if canReach[src] {
				continue
			}
			n, ok := v.nodesByID[src]
			if !ok || n.ParentID != scope {
				continue
			}
			for _, e := range edges {
				if canReach[e.TargetNodeID] {
					canReach[src] = true
					changed = true
					break
				}
			}
		}
	}
	entries := v.entriesOfScope(scope, scope == "")
	reach := v.reachableFrom(entries, scope, "")
	for _, n := range v.scopes[scope] {
		if n.Type == model.GraphNodeTypeEnd || n.Type == model.GraphNodeTypeStart {
			continue
		}
		if reach[n.ID] && !canReach[n.ID] {
			v.nodeErr(n.ID, fmt.Sprintf("node %q cannot reach an end node (every path must terminate at an end; no implicit terminals allowed)", n.ID))
		}
	}
}

// entriesOfScope returns the entry node IDs of a scope. For the main scope,
// entries are the start nodes. For a loop scope, the entry is the loop-scoped
// start node (the container's entry marker).
func (v *validator) entriesOfScope(scope string, isMain bool) []string {
	var entries []string
	for _, n := range v.scopes[scope] {
		if isMain {
			if n.Type == model.GraphNodeTypeStart {
				entries = append(entries, n.ID)
			}
			continue
		}
		if n.Type == model.GraphNodeTypeStart {
			entries = append(entries, n.ID)
		}
	}
	return entries
}

// reachableFrom does a BFS within a single scope following intra-scope edges,
// optionally excluding one edge ID (for dominator analysis).
func (v *validator) reachableFrom(entries []string, scope, excludeEdgeID string) map[string]bool {
	reach := make(map[string]bool)
	var queue []string
	for _, id := range entries {
		if !reach[id] {
			reach[id] = true
			queue = append(queue, id)
		}
	}
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		for _, e := range v.outEdges[cur] {
			if e.ID == excludeEdgeID {
				continue
			}
			if dst, ok := v.nodesByID[e.TargetNodeID]; ok && dst.ParentID == scope && !reach[e.TargetNodeID] {
				reach[e.TargetNodeID] = true
				queue = append(queue, e.TargetNodeID)
			}
		}
	}
	return reach
}

// --- cycle detection (every scope must be a DAG) ---

func (v *validator) validateCycles() {
	for scope := range v.scopes {
		v.detectCycleInScope(scope)
	}
}

func (v *validator) detectCycleInScope(scope string) {
	const (
		white = 0
		gray  = 1
		black = 2
	)
	color := make(map[string]int)
	var stack []string
	var dfs func(id string) bool
	dfs = func(id string) bool {
		color[id] = gray
		stack = append(stack, id)
		for _, e := range v.outEdges[id] {
			dst, ok := v.nodesByID[e.TargetNodeID]
			if !ok || dst.ParentID != scope {
				continue
			}
			switch color[e.TargetNodeID] {
			case white:
				if dfs(e.TargetNodeID) {
					return true
				}
			case gray:
				v.structErr(fmt.Sprintf("cycle detected in scope %q involving node %q — cycles may only be expressed by a loop container", scopeLabel(scope), e.TargetNodeID))
				return true
			}
		}
		stack = stack[:len(stack)-1]
		color[id] = black
		return false
	}
	for _, n := range v.scopes[scope] {
		if color[n.ID] == white {
			if dfs(n.ID) {
				return // one cycle report per scope is enough
			}
		}
	}
}

// --- loop subgraph boundaries ---

func (v *validator) validateLoopSubgraphs() {
	for i := range v.cfg.Nodes {
		n := &v.cfg.Nodes[i]
		if n.Type != model.GraphNodeTypeLoop {
			continue
		}
		scope := n.ID
		children := v.scopes[scope]
		if len(children) == 0 {
			v.nodeErr(n.ID, fmt.Sprintf("loop container %q has an empty subgraph", n.ID))
			continue
		}
		// A loop subgraph has exactly one entry marker (a loop-scoped start node,
		// rendered on the container's left border) and at least one exit marker (a
		// loop-scoped end node on the right border). The user wires the entry start
		// to the body and the body to the exit end. This mirrors the main graph's
		// start→…→end structure, scoped to the container.
		var starts []string
		var internalEnds int
		for _, c := range children {
			switch c.Type {
			case model.GraphNodeTypeStart:
				starts = append(starts, c.ID)
			case model.GraphNodeTypeEnd:
				internalEnds++
			}
		}
		if len(starts) != 1 {
			v.nodeErr(n.ID, fmt.Sprintf("loop subgraph of %q must have exactly one entry node (a loop-scoped start with parentId=%q), got %d", n.ID, n.ID, len(starts)))
		}
		if internalEnds == 0 {
			v.nodeErr(n.ID, fmt.Sprintf("loop subgraph of %q must contain at least one exit node (an end node with parentId=%q)", n.ID, n.ID))
		}
		// All internal paths must reach an internal end.
		ends := make(map[string]bool)
		for _, c := range children {
			if c.Type == model.GraphNodeTypeEnd {
				ends[c.ID] = true
			}
		}
		v.checkCanReachEnd(scope, ends)
		// Unreachable internal nodes (from the entry start).
		if len(starts) == 1 {
			reach := v.reachableFrom(starts, scope, "")
			for _, c := range children {
				if c.ID == starts[0] || c.Type == model.GraphNodeTypeStart {
					continue
				}
				if !reach[c.ID] {
					v.nodeErr(c.ID, fmt.Sprintf("node %q is not reachable from the loop entry of %q", c.ID, n.ID))
				}
			}
		}
	}
}

// --- session inheritance ---

// validateFirstAgentNewSession enforces "每条 start 链路首个可执行 Agent 必须新建
// 会话" (§3 会话血缘): the first Agent-class node reachable from any main-graph
// start, traversing only non-Agent nodes, must not declare the `inherit`
// strategy — there is no upstream Agent session for it to inherit. A BFS from
// each start stops descending as soon as it hits an Agent node (that Agent is
// the "first" on every path through it); non-Agent nodes (Shell/If-Else/loop
// container/end) are traversed through. Loop subgraph entries are not start
// chains: a loop's first round inherits the session flowing into the container,
// so an `inherit` Agent at a subgraph entry is legal and not checked here.
//
// Multi-in-edge Agents MAY inherit (they fork the greatest-node-ID upstream
// session via pickInflowSession); this rule is the safety backstop that keeps
// such inheritance well-defined: if a join Agent is the first Agent on ANY
// start chain (some in-edge path reaches it through non-Agent nodes only), that
// path carries no upstream session and inherit is rejected at save time.
func (v *validator) validateFirstAgentNewSession() {
	starts := v.entriesOfScope("", true)
	reported := map[string]bool{}
	for _, startID := range starts {
		visited := map[string]bool{startID: true}
		queue := []string{startID}
		for len(queue) > 0 {
			cur := queue[0]
			queue = queue[1:]
			for _, e := range v.outEdges[cur] {
				next, ok := v.nodesByID[e.TargetNodeID]
				if !ok || visited[next.ID] {
					continue
				}
				visited[next.ID] = true
				if isAgent(next.Type) {
					// First Agent on this path: it cannot inherit. Do not descend
					// past it (downstream Agents may legally inherit from it).
					if next.Config.SessionStrategy == model.GraphSessionStrategyInherit && !reported[next.ID] {
						reported[next.ID] = true
						v.sessionErr(next.ID, fmt.Sprintf("agent node %q is the first Agent reachable from start node %q and declares 'inherit' session strategy, but a start chain's first Agent has no upstream session to inherit and must create a new session", next.ID, startID))
					}
					continue
				}
				queue = append(queue, next.ID)
			}
		}
	}
}

func (v *validator) validateOutputConflicts() {
	// Per scope: two nodes that are not in an ancestor/descendant relation and
	// are not mutually exclusive (yes/no branches of the same if-else) may run
	// in parallel and therefore cannot declare the same output variable.
	for scope := range v.scopes {
		v.checkScopeOutputConflicts(scope)
	}
}

func (v *validator) checkScopeOutputConflicts(scope string) {
	// Collect producer nodes (those declaring output vars or an alias).
	type producer struct {
		node  *model.GraphNode
		names []string
	}
	var producers []producer
	for _, n := range v.scopes[scope] {
		var names []string
		names = append(names, n.Config.OutputVariables...)
		if n.Config.LastAssistantAlias != "" {
			names = append(names, n.Config.LastAssistantAlias)
		}
		if len(names) > 0 {
			producers = append(producers, producer{node: n, names: names})
		}
	}
	if len(producers) < 2 {
		return
	}

	isMain := scope == ""
	entries := v.entriesOfScope(scope, isMain)
	// reach[a][b] = b reachable from a within scope.
	reach := make(map[string]map[string]bool)
	for _, p := range producers {
		reach[p.node.ID] = v.reachableFrom([]string{p.node.ID}, scope, "")
	}

	// Dominator-by-port sets for each if-else in this scope, used to detect
	// mutual exclusion.
	domYes, domNo := v.branchDominators(scope, entries)

	for i := 0; i < len(producers); i++ {
		for j := i + 1; j < len(producers); j++ {
			a, b := producers[i].node, producers[j].node
			// Sequential (ancestor/descendant) → safe.
			if reach[a.ID][b.ID] || reach[b.ID][a.ID] {
				continue
			}
			// Mutually exclusive via some if-else yes/no → safe.
			if v.mutuallyExclusive(a.ID, b.ID, domYes, domNo) {
				continue
			}
			// Potentially parallel: any shared name is a conflict.
			shared := intersect(producers[i].names, producers[j].names)
			for _, name := range shared {
				v.addErr(model.GraphValidationError{
					Type:     model.GraphValidationErrorTypeVariable,
					NodeID:   a.ID,
					Variable: name,
					Message:  fmt.Sprintf("output variable %q is declared by potentially-parallel nodes %q and %q in the same scope; parallel writers to the same variable are not allowed", name, a.ID, b.ID),
				})
				v.addErr(model.GraphValidationError{
					Type:     model.GraphValidationErrorTypeVariable,
					NodeID:   b.ID,
					Variable: name,
					Message:  fmt.Sprintf("output variable %q is declared by potentially-parallel nodes %q and %q in the same scope; parallel writers to the same variable are not allowed", name, b.ID, a.ID),
				})
			}
		}
	}
}

// validateParallelSessionReuse rejects graphs where two Agent nodes that can run
// in parallel both write to the SAME session (§3 会话复用). Under the reuse
// semantics an `inherit` node appends its turn to its inflow session rather than
// forking a private copy; two concurrent turns on one session collide (the ACP
// agent cancels the in-flight Run when a second Run starts on the same cached
// session, services/agent/acp/agent.go). Per-scope, mirroring
// validateOutputConflicts: a loop body is its own scope and is checked
// independently. Iterations run strictly sequentially, so cross-iteration reuse
// (§ 跨轮连续) is safe and not flagged here — only within-scope parallelism is.
func (v *validator) validateParallelSessionReuse() {
	for scope := range v.scopes {
		v.checkScopeSessionCollisions(scope)
	}
}

// sessionSourceSets statically computes, for each node in the scope, the set of
// "session sources" whose session could flow into it. A session source is the
// node that ORIGINATES a session: a `new` Agent is its own source; a non-Agent
// node passes its inflow sources through unchanged; an `inherit` Agent reuses its
// inflow sources. Because pickInflowSession's "greatest node ID" choice is only
// determined at runtime over the activated in-edges, an `inherit` node's source
// set is the UNION over all its in-edge upstreams — a sound over-approximation
// (it can only over-reject, never miss a real collision). The scope's start node
// seeds the set: the main scope's start carries no session (empty set), while a
// loop subgraph's start carries the loop's inflow session as a single synthetic
// token so two body agents that both trace back to it count as same-source.
func (v *validator) sessionSourceSets(scope string) map[string]map[string]bool {
	srcSet := make(map[string]map[string]bool)
	entryToken := "loop-inflow:" + scope // synthetic source for a loop body's inflow

	// Kahn topological order over intra-scope edges (the scope is a DAG —
	// validateCycles guarantees it). Use a local in-degree copy so we don't
	// mutate v.inDeg.
	remaining := make(map[string]int)
	var queue []string
	for _, n := range v.scopes[scope] {
		d := v.inDeg[n.ID]
		remaining[n.ID] = d
		if d == 0 {
			queue = append(queue, n.ID)
		}
	}
	for len(queue) > 0 {
		id := queue[0]
		queue = queue[1:]
		n := v.nodesByID[id]

		// Union of in-edge upstreams' source sets.
		in := make(map[string]bool)
		for _, up := range v.intraScopeInEdgeSources(scope, id) {
			for s := range srcSet[up] {
				in[s] = true
			}
		}
		switch {
		case n != nil && n.Type == model.GraphNodeTypeStart:
			if scope == "" {
				srcSet[id] = map[string]bool{} // main start: no session yet
			} else {
				srcSet[id] = map[string]bool{entryToken: true} // loop inflow session
			}
		case n != nil && isAgent(n.Type) && n.Config.SessionStrategy != model.GraphSessionStrategyInherit:
			srcSet[id] = map[string]bool{id: true} // `new` (or unset): fresh source = itself
		default:
			// `inherit` Agent OR passthrough (Shell/IfElse/Loop container): reuse/forward inflow sources.
			srcSet[id] = in
		}

		for _, e := range v.outEdges[id] {
			if dst, ok := v.nodesByID[e.TargetNodeID]; ok && dst.ParentID == scope {
				remaining[e.TargetNodeID]--
				if remaining[e.TargetNodeID] == 0 {
					queue = append(queue, e.TargetNodeID)
				}
			}
		}
	}
	return srcSet
}

// intraScopeInEdgeSources returns the source node IDs of every intra-scope edge
// terminating at nodeID. Built from v.outEdges (already intra-scope filtered).
func (v *validator) intraScopeInEdgeSources(scope, nodeID string) []string {
	var srcs []string
	for _, n := range v.scopes[scope] {
		for _, e := range v.outEdges[n.ID] {
			if e.TargetNodeID == nodeID {
				srcs = append(srcs, n.ID)
			}
		}
	}
	return srcs
}

func (v *validator) checkScopeSessionCollisions(scope string) {
	var agents []*model.GraphNode
	for _, n := range v.scopes[scope] {
		if isAgent(n.Type) {
			agents = append(agents, n)
		}
	}
	if len(agents) < 2 {
		return
	}

	isMain := scope == ""
	entries := v.entriesOfScope(scope, isMain)
	reach := make(map[string]map[string]bool)
	for _, a := range agents {
		reach[a.ID] = v.reachableFrom([]string{a.ID}, scope, "")
	}
	domYes, domNo := v.branchDominators(scope, entries)
	srcSet := v.sessionSourceSets(scope)

	for i := 0; i < len(agents); i++ {
		for j := i + 1; j < len(agents); j++ {
			a, b := agents[i], agents[j]
			// Sequential (ancestor/descendant) → one finishes before the other → safe.
			if reach[a.ID][b.ID] || reach[b.ID][a.ID] {
				continue
			}
			// Mutually exclusive via some if-else yes/no → never run together → safe.
			if v.mutuallyExclusive(a.ID, b.ID, domYes, domNo) {
				continue
			}
			// Potentially parallel: a shared session source means they could both
			// write to one session concurrently → reject.
			if !setsIntersect(srcSet[a.ID], srcSet[b.ID]) {
				continue
			}
			v.sessionErr(a.ID, fmt.Sprintf("agent nodes %q and %q can run in parallel and both reuse the same session; a shared session cannot serve two concurrent agent turns — set one of them to a new session, or serialize them", a.ID, b.ID))
			v.sessionErr(b.ID, fmt.Sprintf("agent nodes %q and %q can run in parallel and both reuse the same session; a shared session cannot serve two concurrent agent turns — set one of them to a new session, or serialize them", b.ID, a.ID))
		}
	}
}

func setsIntersect(a, b map[string]bool) bool {
	// Iterate the smaller set for speed.
	if len(b) < len(a) {
		a, b = b, a
	}
	for k := range a {
		if b[k] {
			return true
		}
	}
	return false
}

// branchDominators returns, for every node in the scope, which if-else yes/no
// edges dominate it (i.e. removing that edge makes the node unreachable from
// entries). domYes[ifElseID] is the set of nodes dominated by that if-else's
// yes edge; domNo likewise.
func (v *validator) branchDominators(scope string, entries []string) (domYes, domNo map[string]map[string]bool) {
	domYes = make(map[string]map[string]bool)
	domNo = make(map[string]map[string]bool)
	full := v.reachableFrom(entries, scope, "")
	for _, n := range v.scopes[scope] {
		if n.Type != model.GraphNodeTypeIfElse {
			continue
		}
		for _, e := range v.outEdges[n.ID] {
			without := v.reachableFrom(entries, scope, e.ID)
			dom := make(map[string]bool)
			for id := range full {
				if !without[id] {
					dom[id] = true
				}
			}
			switch e.SourcePort {
			case model.GraphEdgePortYes:
				domYes[n.ID] = dom
			case model.GraphEdgePortNo:
				domNo[n.ID] = dom
			}
		}
	}
	return domYes, domNo
}

func (v *validator) mutuallyExclusive(a, b string, domYes, domNo map[string]map[string]bool) bool {
	for ifID := range domYes {
		yes := domYes[ifID]
		no := domNo[ifID]
		if yes == nil || no == nil {
			continue
		}
		if (yes[a] && no[b]) || (no[a] && yes[b]) {
			return true
		}
	}
	return false
}

func intersect(a, b []string) []string {
	set := make(map[string]bool, len(a))
	for _, s := range a {
		set[s] = true
	}
	var out []string
	seen := make(map[string]bool)
	for _, s := range b {
		if set[s] && !seen[s] {
			out = append(out, s)
			seen[s] = true
		}
	}
	return out
}
