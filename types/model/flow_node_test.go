package model

import (
	"testing"
)

func TestMigrateLoopConfig_Noop(t *testing.T) {
	// Already has Flow — should be a no-op.
	cfg := &LoopConfig{
		Flow:   []FlowNode{{ID: "existing", Type: FlowNodeTypeStep, Message: "hello"}},
		Rounds: []LoopRound{{Message: "legacy"}},
	}
	MigrateLoopConfig(cfg)
	if len(cfg.Flow) != 1 || cfg.Flow[0].ID != "existing" {
		t.Fatalf("MigrateLoopConfig should not modify existing Flow")
	}
}

func TestMigrateLoopConfig_LegacyRounds(t *testing.T) {
	cfg := &LoopConfig{
		IterationCount: 3,
		Rounds: []LoopRound{
			{Message: "round1", RepeatCount: 2, RoundMode: RoundModeBeforeRound},
			{Message: "round2", RepeatCount: 1, RoundMode: RoundModeNone, RoundType: RoundTypeShell},
		},
	}
	MigrateLoopConfig(cfg)

	if len(cfg.Flow) != 1 {
		t.Fatalf("expected 1 top-level group, got %d", len(cfg.Flow))
	}
	group := cfg.Flow[0]
	if group.Type != FlowNodeTypeGroup {
		t.Fatalf("expected group, got %s", group.Type)
	}
	if group.IterationCount != 3 {
		t.Fatalf("expected iterationCount=3, got %d", group.IterationCount)
	}
	if len(group.Children) != 2 {
		t.Fatalf("expected 2 children, got %d", len(group.Children))
	}
	if group.Children[0].Message != "round1" || group.Children[0].RepeatCount != 2 {
		t.Errorf("child[0] mismatch: %+v", group.Children[0])
	}
	if group.Children[1].RoundType != RoundTypeShell {
		t.Errorf("child[1] should be shell type: %+v", group.Children[1])
	}
}

func TestMigrateLoopConfig_Nil(t *testing.T) {
	MigrateLoopConfig(nil) // should not panic
}

func TestMigrateLoopConfig_NoRounds(t *testing.T) {
	cfg := &LoopConfig{IterationCount: 5}
	MigrateLoopConfig(cfg)
	if len(cfg.Flow) != 0 {
		t.Fatalf("expected empty flow for no rounds, got %d", len(cfg.Flow))
	}
}

func TestCalcTotalSteps(t *testing.T) {
	tests := []struct {
		name  string
		nodes []FlowNode
		want  int
	}{
		{"empty", nil, 0},
		{"single step", []FlowNode{{Type: FlowNodeTypeStep, RepeatCount: 1}}, 1},
		{"step repeat=3", []FlowNode{{Type: FlowNodeTypeStep, RepeatCount: 3}}, 3},
		{"step repeat=0 defaults to 1", []FlowNode{{Type: FlowNodeTypeStep, RepeatCount: 0}}, 1},
		{"group 2 iterations x 1 step", []FlowNode{{
			Type: FlowNodeTypeGroup, IterationCount: 2,
			Children: []FlowNode{{Type: FlowNodeTypeStep, RepeatCount: 1}},
		}}, 2},
		{"nested", []FlowNode{{
			Type: FlowNodeTypeGroup, IterationCount: 3,
			Children: []FlowNode{
				{Type: FlowNodeTypeStep, RepeatCount: 2},
				{Type: FlowNodeTypeStep, RepeatCount: 1},
			},
		}}, 9}, // 3 * (2 + 1)
		{"two groups", []FlowNode{
			{Type: FlowNodeTypeGroup, IterationCount: 2, Children: []FlowNode{
				{Type: FlowNodeTypeStep, RepeatCount: 1},
			}},
			{Type: FlowNodeTypeStep, RepeatCount: 5},
		}, 7}, // 2*1 + 5
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := CalcTotalSteps(tt.nodes)
			if got != tt.want {
				t.Errorf("CalcTotalSteps = %d, want %d", got, tt.want)
			}
		})
	}
}

func TestComparePaths(t *testing.T) {
	tests := []struct {
		a, b []int
		want int
	}{
		{nil, nil, 0},
		{[]int{0, 0}, []int{0, 0}, 0},
		{[]int{0, 0}, []int{0, 1}, -1},
		{[]int{0, 1}, []int{0, 0}, 1},
		{[]int{0}, []int{0, 0}, -1},
		{[]int{0, 0}, []int{0}, 1},
		{[]int{1, 0}, []int{0, 1}, 1},
	}
	for _, tt := range tests {
		got := ComparePaths(tt.a, tt.b)
		if got != tt.want {
			t.Errorf("ComparePaths(%v, %v) = %d, want %d", tt.a, tt.b, got, tt.want)
		}
	}
}

func TestEqualPaths(t *testing.T) {
	if !EqualPaths([]int{0, 1, 2}, []int{0, 1, 2}) {
		t.Error("should be equal")
	}
	if EqualPaths([]int{0, 1}, []int{0, 1, 2}) {
		t.Error("different length should not be equal")
	}
	if EqualPaths([]int{0, 1}, []int{0, 2}) {
		t.Error("different values should not be equal")
	}
	if !EqualPaths(nil, nil) {
		t.Error("nil paths should be equal")
	}
}

func TestCopyPath(t *testing.T) {
	orig := []int{1, 2, 3}
	cp := CopyPath(orig)
	if !EqualPaths(orig, cp) {
		t.Error("copy should equal original")
	}
	cp[0] = 99
	if orig[0] == 99 {
		t.Error("modifying copy should not affect original")
	}
	if CopyPath(nil) != nil {
		t.Error("CopyPath(nil) should be nil")
	}
}

func TestNextStepPath(t *testing.T) {
	// Simple flow: step(repeat=2), step(repeat=1)
	flow := []FlowNode{
		{Type: FlowNodeTypeStep, RepeatCount: 2},
		{Type: FlowNodeTypeStep, RepeatCount: 1},
	}
	// Paths: [0,0], [0,1], [1,0]

	// First step
	first := NextStepPath(flow, nil)
	if !EqualPaths(first, []int{0, 0}) {
		t.Errorf("first step should be [0,0], got %v", first)
	}

	// After [0,0] → [0,1]
	next := NextStepPath(flow, []int{0, 0})
	if !EqualPaths(next, []int{0, 1}) {
		t.Errorf("after [0,0] should be [0,1], got %v", next)
	}

	// After [0,1] → [1,0]
	next = NextStepPath(flow, []int{0, 1})
	if !EqualPaths(next, []int{1, 0}) {
		t.Errorf("after [0,1] should be [1,0], got %v", next)
	}

	// After [1,0] → nil (done)
	next = NextStepPath(flow, []int{1, 0})
	if next != nil {
		t.Errorf("after last step should be nil, got %v", next)
	}
}

func TestNextStepPath_WithGroup(t *testing.T) {
	// Group(iter=2) → step(repeat=1)
	flow := []FlowNode{{
		Type: FlowNodeTypeGroup, IterationCount: 2,
		Children: []FlowNode{{Type: FlowNodeTypeStep, RepeatCount: 1}},
	}}
	// Paths: [0,0, 0,0], [0,1, 0,0]

	first := NextStepPath(flow, nil)
	if !EqualPaths(first, []int{0, 0, 0, 0}) {
		t.Errorf("first should be [0,0,0,0], got %v", first)
	}

	next := NextStepPath(flow, []int{0, 0, 0, 0})
	if !EqualPaths(next, []int{0, 1, 0, 0}) {
		t.Errorf("after [0,0,0,0] should be [0,1,0,0], got %v", next)
	}

	next = NextStepPath(flow, []int{0, 1, 0, 0})
	if next != nil {
		t.Errorf("after last should be nil, got %v", next)
	}
}

func TestValidateFlow(t *testing.T) {
	tests := []struct {
		name    string
		nodes   []FlowNode
		wantErr bool
	}{
		{"valid step", []FlowNode{{Type: FlowNodeTypeStep, Message: "hi", RoundMode: RoundModeBeforeRound, AgentType: "claude"}}, false},
		{"valid shell with message", []FlowNode{{Type: FlowNodeTypeStep, RoundType: RoundTypeShell, Message: "echo hi", RoundMode: RoundModeBeforeRound}}, false},
		{"valid shell with scriptId", []FlowNode{{Type: FlowNodeTypeStep, RoundType: RoundTypeShell, ScriptID: "s1", RoundMode: RoundModeBeforeRound}}, false},
		{"valid group", []FlowNode{{
			Type: FlowNodeTypeGroup, IterationCount: 2,
			Children: []FlowNode{{Type: FlowNodeTypeStep, Message: "hi", RoundMode: RoundModeBeforeRound, AgentType: "claude"}},
		}}, false},
		{"empty flow", nil, true},
		{"first step roundMode=none", []FlowNode{{Type: FlowNodeTypeStep, Message: "hi", RoundMode: RoundModeNone}}, true},
		{"prompt step no message", []FlowNode{{Type: FlowNodeTypeStep, RoundMode: RoundModeBeforeRound, AgentType: "claude"}}, true},
		{"shell step no content", []FlowNode{{Type: FlowNodeTypeStep, RoundType: RoundTypeShell, RoundMode: RoundModeBeforeRound}}, true},
		{"unknown roundType", []FlowNode{{Type: FlowNodeTypeStep, RoundType: "http", Message: "GET /", RoundMode: RoundModeBeforeRound, AgentType: "claude"}}, true},
		{"valid evaluator in group", []FlowNode{{
			Type: FlowNodeTypeGroup, IterationCount: 2,
			Children: []FlowNode{
				{Type: FlowNodeTypeStep, Message: "work", RoundMode: RoundModeBeforeRound, AgentType: "claude"},
				{Type: FlowNodeTypeStep, RoundType: RoundTypeEvaluator, Message: "done?", RoundMode: RoundModeNone},
			},
		}}, false},
		{"evaluator at top level", []FlowNode{{Type: FlowNodeTypeStep, RoundType: RoundTypeEvaluator, Message: "done?", RoundMode: RoundModeBeforeRound, AgentType: "claude"}}, true},
		{"evaluator no message", []FlowNode{{
			Type: FlowNodeTypeGroup, IterationCount: 2,
			Children: []FlowNode{
				{Type: FlowNodeTypeStep, Message: "work", RoundMode: RoundModeBeforeRound, AgentType: "claude"},
				{Type: FlowNodeTypeStep, RoundType: RoundTypeEvaluator, RoundMode: RoundModeNone},
			},
		}}, true},
		{"group no children", []FlowNode{{Type: FlowNodeTypeGroup, IterationCount: 1}}, true},
		{"group iteration < 1", []FlowNode{{
			Type: FlowNodeTypeGroup, IterationCount: 0,
			Children: []FlowNode{{Type: FlowNodeTypeStep, Message: "hi", RoundMode: RoundModeBeforeRound, AgentType: "claude"}},
		}}, true},
		{"unknown type", []FlowNode{{Type: "unknown"}}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := ValidateFlow(tt.nodes, 0)
			if (err != nil) != tt.wantErr {
				t.Errorf("ValidateFlow() error = %v, wantErr %v", err, tt.wantErr)
			}
		})
	}
}

func TestValidateFlow_MaxDepth(t *testing.T) {
	// Build a deeply nested structure exceeding MaxFlowDepth
	node := FlowNode{Type: FlowNodeTypeStep, Message: "leaf", RoundMode: RoundModeBeforeRound, AgentType: "claude"}
	for i := 0; i < MaxFlowDepth+1; i++ {
		node = FlowNode{
			Type:           FlowNodeTypeGroup,
			IterationCount: 1,
			Children:       []FlowNode{node},
		}
	}
	if err := ValidateFlow([]FlowNode{node}, 0); err == nil {
		t.Error("expected depth error")
	}
}

func TestFindFirstStepMessage(t *testing.T) {
	flow := []FlowNode{{
		Type: FlowNodeTypeGroup, IterationCount: 1,
		Children: []FlowNode{
			{Type: FlowNodeTypeStep, RoundType: RoundTypeShell, ScriptID: "s1"},
			{Type: FlowNodeTypeStep, Message: "found me"},
		},
	}}
	msg := FindFirstStepMessage(flow)
	if msg != "found me" {
		t.Errorf("expected 'found me', got %q", msg)
	}

	if FindFirstStepMessage(nil) != "" {
		t.Error("nil flow should return empty")
	}
}

func TestValidateFlowRejectsBlankMessage(t *testing.T) {
	// A whitespace-only prompt message must be rejected (it trims to empty).
	prompt := []FlowNode{{Type: FlowNodeTypeStep, RoundType: RoundTypePrompt, Message: "   "}}
	if err := ValidateFlow(prompt, 0); err == nil {
		t.Fatalf("blank prompt message: got nil error, want validation failure")
	}

	// A whitespace-only evaluator message inside a group must be rejected too.
	eval := []FlowNode{{
		Type: FlowNodeTypeGroup, IterationCount: 1,
		Children: []FlowNode{{Type: FlowNodeTypeStep, RoundType: RoundTypeEvaluator, Message: "\n \t"}},
	}}
	if err := ValidateFlow(eval, 0); err == nil {
		t.Fatalf("blank evaluator message: got nil error, want validation failure")
	}

	// A real message still passes.
	ok := []FlowNode{{Type: FlowNodeTypeStep, RoundType: RoundTypePrompt, Message: "do the thing", RoundMode: RoundModeBeforeRound, AgentType: "claude"}}
	if err := ValidateFlow(ok, 0); err != nil {
		t.Fatalf("valid prompt message: got error %v, want nil", err)
	}
}

// A flow whose session-creating step omits AgentType is valid only AFTER the
// request-level default is backfilled. ValidateFlow alone rejects it;
// NormalizeAndValidateLoopConfig must backfill first so the same config passes.
// Regression for createJob validating before backfilling.
func TestNormalizeAndValidateBackfillsBeforeValidate(t *testing.T) {
	makeCfg := func() *LoopConfig {
		return &LoopConfig{Flow: []FlowNode{{
			Type: FlowNodeTypeStep, RoundType: RoundTypePrompt,
			Message: "do it", RoundMode: RoundModeBeforeRound,
			// AgentType intentionally empty — relies on request-level default.
		}}}
	}

	// Validate-only (the old order) rejects it.
	if err := ValidateFlow(makeCfg().Flow, 0); err == nil {
		t.Fatalf("ValidateFlow without backfill: got nil, want failure on missing agentType")
	}

	// Normalize entry backfills the default first, so it passes and the step
	// inherits the agent.
	cfg := makeCfg()
	if err := NormalizeAndValidateLoopConfig(cfg, FlowDefaults{AgentType: "claude"}); err != nil {
		t.Fatalf("NormalizeAndValidateLoopConfig: got error %v, want nil", err)
	}
	if cfg.Flow[0].AgentType != "claude" {
		t.Fatalf("step AgentType = %q, want backfilled %q", cfg.Flow[0].AgentType, "claude")
	}

	// With no default available either, it still (correctly) fails.
	if err := NormalizeAndValidateLoopConfig(makeCfg(), FlowDefaults{}); err == nil {
		t.Fatalf("NormalizeAndValidateLoopConfig without any default: got nil, want failure")
	}
}
