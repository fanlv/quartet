package graph

import (
	"testing"

	"github.com/fanlv/quartet/types/model"
)

// --- builders ---

func node(id string, t model.GraphNodeType) model.GraphNode {
	return model.GraphNode{ID: id, Type: t}
}

func edge(id, src, dst string) model.GraphEdge {
	return model.GraphEdge{ID: id, SourceNodeID: src, TargetNodeID: dst}
}

func portEdge(id, src, dst string, port model.GraphEdgePort) model.GraphEdge {
	return model.GraphEdge{ID: id, SourceNodeID: src, TargetNodeID: dst, SourcePort: port}
}

// hasErrForNode reports whether any error targets the given node ID.
func hasErrForNode(errs []model.GraphValidationError, nodeID string) bool {
	for _, e := range errs {
		if e.NodeID == nodeID {
			return true
		}
	}
	return false
}

func hasErrForEdge(errs []model.GraphValidationError, edgeID string) bool {
	for _, e := range errs {
		if e.EdgeID == edgeID {
			return true
		}
	}
	return false
}

func hasErrForVar(errs []model.GraphValidationError, variable string) bool {
	for _, e := range errs {
		if e.Variable == variable {
			return true
		}
	}
	return false
}

func hasStructErr(errs []model.GraphValidationError) bool {
	for _, e := range errs {
		if e.Type == model.GraphValidationErrorTypeStructure {
			return true
		}
	}
	return false
}

// linearValid builds a minimal valid graph: start -> shell -> end.
func linearValid() *model.GraphConfig {
	return &model.GraphConfig{
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			node("sh", model.GraphNodeTypeShell),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("e1", "s", "sh"),
			edge("e2", "sh", "e"),
		},
	}
}

// --- tests ---

func TestValidate_LinearValid(t *testing.T) {
	if errs := validateConfig(linearValid()); len(errs) != 0 {
		t.Fatalf("expected valid graph, got errors: %+v", errs)
	}
}

func TestValidate_MissingStartAndEnd(t *testing.T) {
	cfg := &model.GraphConfig{
		Nodes: []model.GraphNode{node("sh", model.GraphNodeTypeShell)},
	}
	errs := validateConfig(cfg)
	if !hasStructErr(errs) {
		t.Fatalf("expected structure errors for missing start/end, got: %+v", errs)
	}
}

func TestValidate_PureStartEnd(t *testing.T) {
	cfg := &model.GraphConfig{
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{edge("e1", "s", "e")},
	}
	errs := validateConfig(cfg)
	if !hasStructErr(errs) {
		t.Fatalf("expected structure error for pure start→end (zero denominator), got: %+v", errs)
	}
}

func TestValidate_ControlNodeWithBusinessConfig(t *testing.T) {
	cfg := linearValid()
	cfg.Nodes[0].Config.Script = "echo hi" // start node with a script
	cfg.Nodes[2].Config.OutputVariables = []string{"x"}
	errs := validateConfig(cfg)
	if !hasErrForNode(errs, "s") {
		t.Errorf("expected error on start node with business config")
	}
	if !hasErrForNode(errs, "e") {
		t.Errorf("expected error on end node declaring output variables")
	}
}

func TestValidate_UnreachableNode(t *testing.T) {
	cfg := linearValid()
	// orphan node not connected to start
	cfg.Nodes = append(cfg.Nodes, node("orphan", model.GraphNodeTypeShell))
	cfg.Edges = append(cfg.Edges, edge("e3", "orphan", "e"))
	errs := validateConfig(cfg)
	if !hasErrForNode(errs, "orphan") {
		t.Fatalf("expected unreachable-node error for orphan, got: %+v", errs)
	}
}

func TestValidate_MissingInOutEdges(t *testing.T) {
	cfg := &model.GraphConfig{
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			node("sh", model.GraphNodeTypeShell), // no out-edge → can't reach end
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{edge("e1", "s", "sh")}, // e has no in-edge
	}
	errs := validateConfig(cfg)
	if !hasErrForNode(errs, "sh") {
		t.Errorf("expected out-edge error on sh")
	}
	if !hasErrForNode(errs, "e") {
		t.Errorf("expected in-edge error on end")
	}
}

func TestValidate_IllegalCycle(t *testing.T) {
	cfg := &model.GraphConfig{
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			node("a", model.GraphNodeTypeShell),
			node("b", model.GraphNodeTypeShell),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("e1", "s", "a"),
			edge("e2", "a", "b"),
			edge("e3", "b", "a"), // cycle a<->b without a loop container
			edge("e4", "b", "e"),
		},
	}
	errs := validateConfig(cfg)
	if !hasStructErr(errs) {
		t.Fatalf("expected cycle structure error, got: %+v", errs)
	}
}

func TestValidate_IfElseMissingPorts(t *testing.T) {
	cfg := &model.GraphConfig{
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			func() model.GraphNode {
				n := node("if", model.GraphNodeTypeIfElse)
				n.Config.Condition = `{{a}} == "1"`
				return n
			}(),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("e1", "s", "if"),
			// only one default out-edge, no yes/no ports
			edge("e2", "if", "e"),
		},
	}
	errs := validateConfig(cfg)
	if !hasErrForNode(errs, "if") {
		t.Fatalf("expected if-else port error, got: %+v", errs)
	}
}

func TestValidate_IfElseValid(t *testing.T) {
	cfg := &model.GraphConfig{
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			func() model.GraphNode {
				n := node("if", model.GraphNodeTypeIfElse)
				n.Config.Condition = `{{a}} == "1"`
				return n
			}(),
			node("y", model.GraphNodeTypeShell),
			node("n", model.GraphNodeTypeShell),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("e1", "s", "if"),
			portEdge("e2", "if", "y", model.GraphEdgePortYes),
			portEdge("e3", "if", "n", model.GraphEdgePortNo),
			edge("e4", "y", "e"),
			edge("e5", "n", "e"),
		},
	}
	if errs := validateConfig(cfg); len(errs) != 0 {
		t.Fatalf("expected valid if-else graph, got: %+v", errs)
	}
}

func TestValidate_BadConditionExpression(t *testing.T) {
	cfg := &model.GraphConfig{
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			func() model.GraphNode {
				n := node("if", model.GraphNodeTypeIfElse)
				n.Config.Condition = `{{a}}` // bare variable: invalid
				return n
			}(),
			node("y", model.GraphNodeTypeShell),
			node("n", model.GraphNodeTypeShell),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("e1", "s", "if"),
			portEdge("e2", "if", "y", model.GraphEdgePortYes),
			portEdge("e3", "if", "n", model.GraphEdgePortNo),
			edge("e4", "y", "e"),
			edge("e5", "n", "e"),
		},
	}
	if !hasErrForNode(validateConfig(cfg), "if") {
		t.Fatal("expected invalid condition error on if-else node")
	}
}

func TestValidate_EvaluatorRequiresOutputAndNoBranch(t *testing.T) {
	// evaluator with no output vars + a yes/no out-edge
	cfg := &model.GraphConfig{
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			node("ev", model.GraphNodeTypeEvaluator),
			node("a", model.GraphNodeTypeShell),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("e1", "s", "ev"),
			portEdge("e2", "ev", "a", model.GraphEdgePortYes),
			edge("e3", "a", "e"),
		},
	}
	errs := validateConfig(cfg)
	if !hasErrForNode(errs, "ev") {
		t.Fatalf("expected evaluator errors (missing output + branch port), got: %+v", errs)
	}
}

func TestValidate_ReservedAndDuplicateVariables(t *testing.T) {
	cfg := linearValid()
	cfg.Variables = map[string]string{"_secret": "x", "good": "y"}
	cfg.Nodes[1].Config.OutputVariables = []string{"dup", "dup", "_last_assistant_msg"}
	errs := validateConfig(cfg)
	if !hasErrForVar(errs, "_secret") {
		t.Error("expected reserved initial variable error")
	}
	if !hasErrForVar(errs, "dup") {
		t.Error("expected duplicate output variable error")
	}
	if !hasErrForVar(errs, "_last_assistant_msg") {
		t.Error("expected reserved output variable error")
	}
}

func TestValidate_LastAssistantAliasReserved(t *testing.T) {
	cfg := linearValid()
	cfg.Nodes[1].Config.LastAssistantAlias = "_bad"
	if !hasErrForVar(validateConfig(cfg), "_bad") {
		t.Fatal("expected reserved alias error")
	}
}

func TestValidate_MultiInEdgeAgentInheritRejected(t *testing.T) {
	// two shells fan into a prompt node configured to inherit → must fail
	cfg := &model.GraphConfig{
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			node("a", model.GraphNodeTypeShell),
			node("b", model.GraphNodeTypeShell),
			func() model.GraphNode {
				n := node("p", model.GraphNodeTypePrompt)
				n.Config.SessionStrategy = model.GraphSessionStrategyInherit
				return n
			}(),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("e1", "s", "a"),
			edge("e2", "s", "b"),
			edge("e3", "a", "p"),
			edge("e4", "b", "p"),
			edge("e5", "p", "e"),
		},
	}
	errs := validateConfig(cfg)
	found := false
	for _, e := range errs {
		if e.Type == model.GraphValidationErrorTypeSession && e.NodeID == "p" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected session inheritance error on multi-in-edge prompt, got: %+v", errs)
	}
}

func TestValidate_FirstAgentInheritRejected(t *testing.T) {
	// start → shell → prompt(inherit): the prompt is the first Agent on the start
	// chain (shell is not an Agent), so inherit must be rejected.
	cfg := &model.GraphConfig{
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			node("sh", model.GraphNodeTypeShell),
			func() model.GraphNode {
				n := node("p", model.GraphNodeTypePrompt)
				n.Config.SessionStrategy = model.GraphSessionStrategyInherit
				return n
			}(),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("e1", "s", "sh"),
			edge("e2", "sh", "p"),
			edge("e3", "p", "e"),
		},
	}
	errs := validateConfig(cfg)
	found := false
	for _, e := range errs {
		if e.Type == model.GraphValidationErrorTypeSession && e.NodeID == "p" {
			found = true
		}
	}
	if !found {
		t.Fatalf("expected first-Agent session error on prompt p, got: %+v", errs)
	}
}

func TestValidate_DownstreamAgentInheritAllowed(t *testing.T) {
	// start → a(new) → b(inherit) → end: b is NOT the first Agent (a is), so b's
	// inherit is legal — the graph must validate cleanly.
	cfg := &model.GraphConfig{
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			func() model.GraphNode {
				n := node("a", model.GraphNodeTypePrompt)
				n.Config.SessionStrategy = model.GraphSessionStrategyNew
				return n
			}(),
			func() model.GraphNode {
				n := node("b", model.GraphNodeTypePrompt)
				n.Config.SessionStrategy = model.GraphSessionStrategyInherit
				return n
			}(),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("e1", "s", "a"),
			edge("e2", "a", "b"),
			edge("e3", "b", "e"),
		},
	}
	if errs := validateConfig(cfg); len(errs) != 0 {
		t.Fatalf("expected valid graph (downstream inherit allowed), got: %+v", errs)
	}
}

func TestValidate_ParallelOutputConflict(t *testing.T) {
	// start fans out to two shells (parallel) both writing "x", then join at end.
	cfg := &model.GraphConfig{
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			func() model.GraphNode {
				n := node("a", model.GraphNodeTypeShell)
				n.Config.OutputVariables = []string{"x"}
				return n
			}(),
			func() model.GraphNode {
				n := node("b", model.GraphNodeTypeShell)
				n.Config.OutputVariables = []string{"x"}
				return n
			}(),
			node("j", model.GraphNodeTypeShell),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("e1", "s", "a"),
			edge("e2", "s", "b"),
			edge("e3", "a", "j"),
			edge("e4", "b", "j"),
			edge("e5", "j", "e"),
		},
	}
	if !hasErrForVar(validateConfig(cfg), "x") {
		t.Fatalf("expected parallel output conflict on variable x")
	}
}

func TestValidate_SequentialSameVarAllowed(t *testing.T) {
	// a -> b in sequence both writing x is NOT a parallel conflict.
	cfg := &model.GraphConfig{
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			func() model.GraphNode {
				n := node("a", model.GraphNodeTypeShell)
				n.Config.OutputVariables = []string{"x"}
				return n
			}(),
			func() model.GraphNode {
				n := node("b", model.GraphNodeTypeShell)
				n.Config.OutputVariables = []string{"x"}
				return n
			}(),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("e1", "s", "a"),
			edge("e2", "a", "b"),
			edge("e3", "b", "e"),
		},
	}
	if hasErrForVar(validateConfig(cfg), "x") {
		t.Fatalf("sequential same-variable writers should be allowed: %+v", validateConfig(cfg))
	}
}

func TestValidate_MutuallyExclusiveBranchSameVarAllowed(t *testing.T) {
	// if-else yes/no branches both writing x are mutually exclusive → allowed.
	cfg := &model.GraphConfig{
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			func() model.GraphNode {
				n := node("if", model.GraphNodeTypeIfElse)
				n.Config.Condition = `{{a}} == "1"`
				return n
			}(),
			func() model.GraphNode {
				n := node("y", model.GraphNodeTypeShell)
				n.Config.OutputVariables = []string{"x"}
				return n
			}(),
			func() model.GraphNode {
				n := node("n", model.GraphNodeTypeShell)
				n.Config.OutputVariables = []string{"x"}
				return n
			}(),
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("e1", "s", "if"),
			portEdge("e2", "if", "y", model.GraphEdgePortYes),
			portEdge("e3", "if", "n", model.GraphEdgePortNo),
			edge("e4", "y", "e"),
			edge("e5", "n", "e"),
		},
	}
	if hasErrForVar(validateConfig(cfg), "x") {
		t.Fatalf("mutually-exclusive branch writers should be allowed: %+v", validateConfig(cfg))
	}
}

func TestValidate_InitialAndOutputSameNameAllowed(t *testing.T) {
	cfg := linearValid()
	cfg.Variables = map[string]string{"shared": "init"}
	cfg.Nodes[1].Config.OutputVariables = []string{"shared"}
	for _, e := range validateConfig(cfg) {
		if e.Variable == "shared" {
			t.Fatalf("initial+output same name should be allowed, got: %+v", e)
		}
	}
}

func TestValidate_AllErrorsReturnedAtOnce(t *testing.T) {
	// graph with several independent violations
	cfg := &model.GraphConfig{
		Nodes: []model.GraphNode{
			node("sh", model.GraphNodeTypeShell), // no start, no end, no edges
		},
		Variables: map[string]string{"_bad": "x"},
	}
	errs := validateConfig(cfg)
	if len(errs) < 2 {
		t.Fatalf("expected multiple errors at once, got %d: %+v", len(errs), errs)
	}
}

// --- loop subgraph ---

func loopValid() *model.GraphConfig {
	loop := node("loop", model.GraphNodeTypeLoop)
	loop.Config.LoopMode = model.GraphLoopModeFixed
	loop.Config.FixedCount = 3
	loop.Config.MaxIterations = 10
	inner := node("in", model.GraphNodeTypeShell)
	inner.ParentID = "loop"
	innerEnd := node("inEnd", model.GraphNodeTypeEnd)
	innerEnd.ParentID = "loop"
	return &model.GraphConfig{
		Nodes: []model.GraphNode{
			node("s", model.GraphNodeTypeStart),
			loop,
			inner,
			innerEnd,
			node("e", model.GraphNodeTypeEnd),
		},
		Edges: []model.GraphEdge{
			edge("e1", "s", "loop"),
			edge("e2", "loop", "e"),
			edge("e3", "in", "inEnd"), // intra-loop
		},
	}
}

func TestValidate_LoopValid(t *testing.T) {
	if errs := validateConfig(loopValid()); len(errs) != 0 {
		t.Fatalf("expected valid loop graph, got: %+v", errs)
	}
}

func TestValidate_LoopCrossBoundaryEdge(t *testing.T) {
	cfg := loopValid()
	// add an edge from inside the loop directly to the main-scope end node
	cfg.Edges = append(cfg.Edges, edge("bad", "in", "e"))
	if !hasErrForEdge(validateConfig(cfg), "bad") {
		t.Fatalf("expected cross-boundary edge error, got: %+v", validateConfig(cfg))
	}
}

func TestValidate_LoopMissingInternalEnd(t *testing.T) {
	cfg := loopValid()
	// drop the internal end node and its edge
	var nodes []model.GraphNode
	for _, n := range cfg.Nodes {
		if n.ID == "inEnd" {
			continue
		}
		nodes = append(nodes, n)
	}
	cfg.Nodes = nodes
	var edges []model.GraphEdge
	for _, e := range cfg.Edges {
		if e.ID == "e3" {
			continue
		}
		edges = append(edges, e)
	}
	cfg.Edges = edges
	if !hasErrForNode(validateConfig(cfg), "loop") {
		t.Fatalf("expected missing-internal-end error on loop, got: %+v", validateConfig(cfg))
	}
}

func TestValidate_LoopMultipleEntries(t *testing.T) {
	cfg := loopValid()
	// add a second entry node inside the loop with no in-edge
	extra := node("in2", model.GraphNodeTypeShell)
	extra.ParentID = "loop"
	cfg.Nodes = append(cfg.Nodes, extra)
	cfg.Edges = append(cfg.Edges, edge("e4", "in2", "inEnd"))
	if !hasErrForNode(validateConfig(cfg), "loop") {
		t.Fatalf("expected single-entry violation on loop, got: %+v", validateConfig(cfg))
	}
}

func TestValidate_LoopUntilValid(t *testing.T) {
	cfg := loopValid()
	for i := range cfg.Nodes {
		if cfg.Nodes[i].ID == "loop" {
			cfg.Nodes[i].Config.LoopMode = model.GraphLoopModeUntil
			cfg.Nodes[i].Config.FixedCount = 0
			cfg.Nodes[i].Config.UntilCondition = `{{done}} == "1"`
		}
	}
	if errs := validateConfig(cfg); len(errs) != 0 {
		t.Fatalf("expected valid until-loop, got: %+v", errs)
	}
}

func TestValidate_LoopFixedCountExceedsMax(t *testing.T) {
	cfg := loopValid()
	for i := range cfg.Nodes {
		if cfg.Nodes[i].ID == "loop" {
			cfg.Nodes[i].Config.FixedCount = 100
			cfg.Nodes[i].Config.MaxIterations = 10
		}
	}
	if !hasErrForNode(validateConfig(cfg), "loop") {
		t.Fatal("expected fixed-count-exceeds-max error")
	}
}

// --- run config ---

func TestValidate_RunConfigBounds(t *testing.T) {
	cfg := linearValid()
	cfg.RunConfig = model.GraphRunConfig{
		ConcurrencyLimit:      99, // > 16
		DefaultNodeTimeoutSec: -1,
		JobTimeoutSec:         -5,
		DefaultLoopMaxIters:   5000, // > 1000
		InstanceLimit:         -1,
		SnapshotByteLimit:     -1,
	}
	errs := validateConfig(cfg)
	keys := map[string]bool{}
	for _, e := range errs {
		if e.Type == model.GraphValidationErrorTypeConfig {
			keys[e.ConfigKey] = true
		}
	}
	for _, k := range []string{"concurrencyLimit", "defaultNodeTimeoutSec", "jobTimeoutSec", "defaultLoopMaxIters", "instanceLimit", "snapshotByteLimit"} {
		if !keys[k] {
			t.Errorf("expected config error for key %q", k)
		}
	}
}
