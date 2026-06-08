package job

import (
	"context"
	"fmt"
	"reflect"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/types/agui"
	"github.com/fanlv/quartet/types/model"
)

type loopRecordingRunner struct {
	initSessions []string
	runSessions  []string
	runPaths     [][]int
}

func (r *loopRecordingRunner) InitSession(ctx context.Context, jobID string, overrides *model.SessionOverrides) (string, error) {
	sessionID := fmt.Sprintf("session-%d", len(r.initSessions)+1)
	r.initSessions = append(r.initSessions, sessionID)
	return sessionID, nil
}

func (r *loopRecordingRunner) RunIteration(ctx context.Context, sessionID string, messages []*schema.Message, handler agui.EventHandler) error {
	r.runSessions = append(r.runSessions, sessionID)
	if h, ok := handler.(*loopEventHandler); ok {
		r.runPaths = append(r.runPaths, model.CopyPath(h.path))
	}
	return nil
}

func (r *loopRecordingRunner) SessionModelID(sessionID string) string {
	return ""
}

func newLoopTestJob(id string, flow []model.FlowNode) *model.Job {
	cfg := &model.LoopConfig{Flow: flow}
	return &model.Job{
		ID:          id,
		WorkspaceID: "",
		Status:      model.JobStatusRunning,
		LoopConfig:  cfg,
		Progress:    buildProgress(cfg),
	}
}

func promptStep(message string, repeatCount int, roundMode model.RoundMode) model.FlowNode {
	return model.FlowNode{
		Type:        model.FlowNodeTypeStep,
		Message:     message,
		RepeatCount: repeatCount,
		RoundMode:   roundMode,
		RoundType:   model.RoundTypePrompt,
		AgentType:   "test-agent",
	}
}

func group(iterationCount int, children ...model.FlowNode) model.FlowNode {
	return model.FlowNode{
		Type:           model.FlowNodeTypeGroup,
		IterationCount: iterationCount,
		Children:       children,
	}
}

func runLoopNodesForTest(t *testing.T, job *model.Job, currentSessionID string) *loopRecordingRunner {
	t.Helper()
	svc := newStateTestService()
	runner := &loopRecordingRunner{}
	result := svc.runFlowNodes(context.Background(), job, runner, job.LoopConfig.Flow, nil, 0, &currentSessionID, false)
	if result != stepCompleted {
		t.Fatalf("runFlowNodes result=%v, want %v", result, stepCompleted)
	}
	return runner
}

func wantSequentialSessions(n int) []string {
	sessions := make([]string, n)
	for i := 0; i < n; i++ {
		sessions[i] = fmt.Sprintf("session-%d", i+1)
	}
	return sessions
}

func TestRunFlowNodesEachRepeatCreatesSessionPerGroupIteration(t *testing.T) {
	flow := []model.FlowNode{
		group(10, promptStep("round", 1, model.RoundModeEachRepeat)),
	}
	job := newLoopTestJob("job-each-repeat-group", flow)

	runner := runLoopNodesForTest(t, job, "")

	wantSessions := wantSequentialSessions(10)
	if !reflect.DeepEqual(runner.initSessions, wantSessions) {
		t.Fatalf("created sessions=%v, want %v", runner.initSessions, wantSessions)
	}
	if !reflect.DeepEqual(runner.runSessions, wantSessions) {
		t.Fatalf("run sessions=%v, want %v", runner.runSessions, wantSessions)
	}
	if job.Progress.CompletedCount != 10 {
		t.Fatalf("completed count=%d, want 10", job.Progress.CompletedCount)
	}
	if job.Resume != nil {
		t.Fatalf("resume=%+v, want nil after completing all steps", job.Resume)
	}
}

func TestRunFlowNodesEachRepeatCreatesSessionPerRepeat(t *testing.T) {
	flow := []model.FlowNode{
		group(3, promptStep("round", 4, model.RoundModeEachRepeat)),
	}
	job := newLoopTestJob("job-each-repeat-repeat", flow)

	runner := runLoopNodesForTest(t, job, "")

	wantSessions := wantSequentialSessions(12)
	if !reflect.DeepEqual(runner.initSessions, wantSessions) {
		t.Fatalf("created sessions=%v, want %v", runner.initSessions, wantSessions)
	}
	if !reflect.DeepEqual(runner.runSessions, wantSessions) {
		t.Fatalf("run sessions=%v, want %v", runner.runSessions, wantSessions)
	}
	if job.Progress.CompletedCount != 12 {
		t.Fatalf("completed count=%d, want 12", job.Progress.CompletedCount)
	}
}

func TestRunFlowNodesEachRepeatResumeReusesPersistedSession(t *testing.T) {
	flow := []model.FlowNode{
		promptStep("round", 1, model.RoundModeEachRepeat),
	}
	job := newLoopTestJob("job-each-repeat-resume", flow)
	job.Resume = &model.JobResume{NextPath: []int{0, 0}, SessionID: "existing-session"}

	runner := runLoopNodesForTest(t, job, "existing-session")

	if len(runner.initSessions) != 0 {
		t.Fatalf("created sessions=%v, want none for true resume", runner.initSessions)
	}
	wantRuns := []string{"existing-session"}
	if !reflect.DeepEqual(runner.runSessions, wantRuns) {
		t.Fatalf("run sessions=%v, want %v", runner.runSessions, wantRuns)
	}
}

func TestRunFlowNodesBeforeRoundResumeReusesPersistedSession(t *testing.T) {
	flow := []model.FlowNode{
		promptStep("round", 1, model.RoundModeBeforeRound),
	}
	job := newLoopTestJob("job-before-round-resume", flow)
	job.Resume = &model.JobResume{NextPath: []int{0, 0}, SessionID: "existing-session"}

	runner := runLoopNodesForTest(t, job, "existing-session")

	if len(runner.initSessions) != 0 {
		t.Fatalf("created sessions=%v, want none for true resume", runner.initSessions)
	}
	wantRuns := []string{"existing-session"}
	if !reflect.DeepEqual(runner.runSessions, wantRuns) {
		t.Fatalf("run sessions=%v, want %v", runner.runSessions, wantRuns)
	}
}

func TestRunFlowNodesEachRepeatThenNoneDoesNotLeakSession(t *testing.T) {
	flow := []model.FlowNode{
		group(2,
			promptStep("fresh", 1, model.RoundModeEachRepeat),
			promptStep("reuse", 1, model.RoundModeNone),
		),
	}
	job := newLoopTestJob("job-each-repeat-none", flow)

	runner := runLoopNodesForTest(t, job, "")

	wantSessions := wantSequentialSessions(4)
	if !reflect.DeepEqual(runner.initSessions, wantSessions) {
		t.Fatalf("created sessions=%v, want %v", runner.initSessions, wantSessions)
	}
	if !reflect.DeepEqual(runner.runSessions, wantSessions) {
		t.Fatalf("run sessions=%v, want %v", runner.runSessions, wantSessions)
	}
}

func TestRunFlowNodesBeforeRoundStillCreatesSessionEachRun(t *testing.T) {
	flow := []model.FlowNode{
		group(3, promptStep("round", 4, model.RoundModeBeforeRound)),
	}
	job := newLoopTestJob("job-before-round", flow)

	runner := runLoopNodesForTest(t, job, "")

	wantSessions := wantSequentialSessions(12)
	if !reflect.DeepEqual(runner.initSessions, wantSessions) {
		t.Fatalf("created sessions=%v, want %v", runner.initSessions, wantSessions)
	}
	if !reflect.DeepEqual(runner.runSessions, wantSessions) {
		t.Fatalf("run sessions=%v, want %v", runner.runSessions, wantSessions)
	}
}
