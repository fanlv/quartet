package job

import (
	"context"
	"fmt"
	"reflect"
	"strings"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/types/agui"
	"github.com/fanlv/quartet/types/model"
)

// judgeRunner is a fake runner for conditional-loop tests. Business-step runs
// succeed (or fail when failBusiness is set). Judge runs (detected by the judge
// prompt text) emit a scripted assistant text drawn from judgeOutputs in order
// — letting a test inject STOP / CONTINUE / malformed output per round.
type judgeRunner struct {
	initCount int

	// judgeOutputs are the assistant texts the judge turns emit, consumed in
	// order. When exhausted, the last one repeats. Empty string => no assistant
	// text at all (used to exercise the "no output" hard-failure path together
	// with judgeErr).
	judgeOutputs []string
	judgeIdx     int
	judgeErr     error // returned by judge RunIteration (after emitting any text)

	// failBusiness, when set, makes every business-step run emit text then
	// return this error (exercises the conditional "failure doesn't fail job").
	failBusiness error

	businessRuns int
	judgeRuns    int
}

func (r *judgeRunner) InitSession(ctx context.Context, jobID string, overrides *model.SessionOverrides) (string, error) {
	r.initCount++
	return fmt.Sprintf("session-%d", r.initCount), nil
}

func isJudgePrompt(messages []*schema.Message) bool {
	for _, m := range messages {
		if strings.Contains(m.Content, "完成条件") && strings.Contains(m.Content, judgeDecisionStop) {
			return true
		}
	}
	return false
}

func (r *judgeRunner) RunIteration(ctx context.Context, sessionID string, messages []*schema.Message, handler agui.EventHandler) error {
	emit := func(text string) {
		if text == "" {
			return
		}
		_ = handler.OnMessageStart()
		_ = handler.OnMessageDelta(text)
		_ = handler.OnMessageEnd()
	}

	if isJudgePrompt(messages) {
		r.judgeRuns++
		out := ""
		if len(r.judgeOutputs) > 0 {
			if r.judgeIdx < len(r.judgeOutputs) {
				out = r.judgeOutputs[r.judgeIdx]
			} else {
				out = r.judgeOutputs[len(r.judgeOutputs)-1]
			}
		}
		r.judgeIdx++
		emit(out)
		return r.judgeErr
	}

	r.businessRuns++
	emit("business output")
	return r.failBusiness
}

func (r *judgeRunner) SessionModelID(sessionID string) string { return "" }

func runConditionalForTest(t *testing.T, job *model.Job, runner JobRunner) {
	t.Helper()
	svc := newStateTestService()
	svc.jobs[job.ID] = job
	currentSessionID := ""
	lastSessionID := ""
	svc.runFlowNodes(context.Background(), job, runner, job.LoopConfig.Flow, nil, 0, &currentSessionID, &lastSessionID, false)
}

func conditionalGroup(condition string, maxIter int, children ...model.FlowNode) model.FlowNode {
	g := group(maxIter, children...)
	g.CompletionCondition = condition
	return g
}

// --- parser unit tests -----------------------------------------------------

func TestParseJudgeDecision(t *testing.T) {
	cases := []struct {
		name string
		text string
		stop bool
	}{
		{"exact stop on last line", "all tests pass now\nLOOP_DECISION: STOP", true},
		{"stop with trailing whitespace", "done\n  LOOP_DECISION: STOP  \n", true},
		{"continue keyword", "still failing\nLOOP_DECISION: CONTINUE", false},
		{"no marker", "looks good but not done", false},
		{"marker not on last line", "LOOP_DECISION: STOP\nactually wait, not yet", false},
		{"marker mentioned mid-text", "the protocol says output LOOP_DECISION: STOP when done", false},
		{"empty", "", false},
		{"lowercase", "loop_decision: stop", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := parseJudgeDecision(c.text); got != c.stop {
				t.Fatalf("parseJudgeDecision(%q) = %t, want %t", c.text, got, c.stop)
			}
		})
	}
}

// --- conditional loop behavior ---------------------------------------------

// STOP on the first judge breaks the group and the loop falls through to the
// following sibling node.
func TestConditionalStopBreaksGroupAndRunsSibling(t *testing.T) {
	flow := []model.FlowNode{
		conditionalGroup("done?", 5, promptStep("work", 1, model.RoundModeBeforeRound)),
		group(1, promptStep("after", 1, model.RoundModeBeforeRound)),
	}
	job := newLoopTestJob("job-cond-stop-sibling", flow)
	runner := &judgeRunner{judgeOutputs: []string{"good\nLOOP_DECISION: STOP"}}

	runConditionalForTest(t, job, runner)

	// 1 conditional round + 1 sibling round = 2 business runs.
	if runner.businessRuns != 2 {
		t.Fatalf("businessRuns = %d, want 2 (1 conditional + 1 sibling)", runner.businessRuns)
	}
	if runner.judgeRuns != 1 {
		t.Fatalf("judgeRuns = %d, want 1", runner.judgeRuns)
	}
	// Only the 2 business steps count; the judge turn does not.
	if job.Progress.CompletedCount != 2 {
		t.Fatalf("completedCount = %d, want 2 (judge not counted)", job.Progress.CompletedCount)
	}
}

func TestConditionalJudgeUsesLastRoundSessionWhenNextIterationStartsFresh(t *testing.T) {
	flow := []model.FlowNode{
		conditionalGroup("done?", 2, promptStep("work", 1, model.RoundModeBeforeRound)),
	}
	job := newLoopTestJob("job-cond-judge-last-session", flow)

	runner := runLoopNodesForTest(t, job, "")

	wantRuns := []string{"session-1", "session-1", "session-2", "session-2"}
	if !reflect.DeepEqual(runner.runSessions, wantRuns) {
		t.Fatalf("run sessions=%v, want %v", runner.runSessions, wantRuns)
	}
}

// CONTINUE keeps looping until the cap; STOP arriving mid-way stops earlier.
func TestConditionalContinueThenStop(t *testing.T) {
	flow := []model.FlowNode{
		conditionalGroup("done?", 5, promptStep("work", 1, model.RoundModeBeforeRound)),
	}
	job := newLoopTestJob("job-cond-continue", flow)
	// Round 1 & 2 continue, round 3 stops.
	runner := &judgeRunner{judgeOutputs: []string{
		"not yet\nLOOP_DECISION: CONTINUE",
		"still working",
		"done\nLOOP_DECISION: STOP",
	}}

	runConditionalForTest(t, job, runner)

	if runner.businessRuns != 3 {
		t.Fatalf("businessRuns = %d, want 3", runner.businessRuns)
	}
	if runner.judgeRuns != 3 {
		t.Fatalf("judgeRuns = %d, want 3", runner.judgeRuns)
	}
	if job.Progress.LastJudgeDecision == nil || !job.Progress.LastJudgeDecision.Stop {
		t.Fatalf("lastJudgeDecision = %+v, want Stop=true", job.Progress.LastJudgeDecision)
	}
}

// Malformed / missing-marker judge output is treated as "continue", bounded by
// the max cap.
func TestConditionalMalformedTreatedAsContinueUntilCap(t *testing.T) {
	flow := []model.FlowNode{
		conditionalGroup("done?", 3, promptStep("work", 1, model.RoundModeBeforeRound)),
	}
	job := newLoopTestJob("job-cond-malformed", flow)
	runner := &judgeRunner{judgeOutputs: []string{"garbage with no marker"}}

	runConditionalForTest(t, job, runner)

	if runner.businessRuns != 3 {
		t.Fatalf("businessRuns = %d, want 3 (ran to cap)", runner.businessRuns)
	}
	if runner.judgeRuns != 3 {
		t.Fatalf("judgeRuns = %d, want 3", runner.judgeRuns)
	}
}

// The judge turn writes no IterationResult and does not bump Completed/Failed.
func TestConditionalJudgeTurnNotCountedAsStep(t *testing.T) {
	flow := []model.FlowNode{
		conditionalGroup("done?", 4, promptStep("work", 1, model.RoundModeBeforeRound)),
	}
	job := newLoopTestJob("job-cond-judge-count", flow)
	// Never stop → runs to cap (4 rounds, 4 judges).
	runner := &judgeRunner{judgeOutputs: []string{"keep going"}}

	runConditionalForTest(t, job, runner)

	if job.Progress.CompletedCount != 4 {
		t.Fatalf("completedCount = %d, want 4 (only business steps)", job.Progress.CompletedCount)
	}
	if len(job.Progress.Results) != 4 {
		t.Fatalf("results = %d, want 4 (no judge results recorded)", len(job.Progress.Results))
	}
}

// Reaching the cap without a STOP exits the group.
func TestConditionalReachesCap(t *testing.T) {
	flow := []model.FlowNode{
		conditionalGroup("never?", 6, promptStep("work", 1, model.RoundModeBeforeRound)),
	}
	job := newLoopTestJob("job-cond-cap", flow)
	runner := &judgeRunner{judgeOutputs: []string{"no marker ever"}}

	runConditionalForTest(t, job, runner)

	if runner.businessRuns != 6 {
		t.Fatalf("businessRuns = %d, want 6 (cap)", runner.businessRuns)
	}
}

// Early STOP backfills TotalSteps from cap*children to actualRounds*children so
// the progress bar finishes full.
func TestConditionalEarlyStopBackfillsTotalSteps(t *testing.T) {
	flow := []model.FlowNode{
		conditionalGroup("done?", 10, promptStep("work", 1, model.RoundModeBeforeRound)),
	}
	job := newLoopTestJob("job-cond-backfill", flow)
	if job.Progress.TotalSteps != 10 {
		t.Fatalf("initial totalSteps = %d, want 10", job.Progress.TotalSteps)
	}
	// Continue twice, stop on round 3.
	runner := &judgeRunner{judgeOutputs: []string{
		"LOOP_DECISION: CONTINUE",
		"LOOP_DECISION: CONTINUE",
		"LOOP_DECISION: STOP",
	}}

	runConditionalForTest(t, job, runner)

	// 3 actual rounds × 1 child step = 3.
	if job.Progress.TotalSteps != 3 {
		t.Fatalf("totalSteps after early stop = %d, want 3 (backfilled)", job.Progress.TotalSteps)
	}
	if job.Progress.CompletedCount != 3 {
		t.Fatalf("completedCount = %d, want 3", job.Progress.CompletedCount)
	}
}

// A business-step failure inside a conditional group does NOT fail the job: the
// round keeps running and the judge still runs.
func TestConditionalBusinessFailureDoesNotFailJob(t *testing.T) {
	flow := []model.FlowNode{
		conditionalGroup("done?", 2, promptStep("work", 1, model.RoundModeBeforeRound)),
	}
	job := newLoopTestJob("job-cond-fail", flow)
	runner := &judgeRunner{
		failBusiness: fmt.Errorf("tests failing"),
		judgeOutputs: []string{"still failing\nLOOP_DECISION: CONTINUE"},
	}

	runConditionalForTest(t, job, runner)

	if job.Status == model.JobStatusFailed {
		t.Fatalf("job status = %s, want not Failed (conditional failure must not fail job)", job.Status)
	}
	if runner.businessRuns != 2 {
		t.Fatalf("businessRuns = %d, want 2 (ran to cap despite failures)", runner.businessRuns)
	}
	if job.Progress.FailedCount != 2 {
		t.Fatalf("failedCount = %d, want 2 (failures recorded)", job.Progress.FailedCount)
	}
}

// A judge call that fails outright with no assistant text fails the job (§2.3).
func TestConditionalJudgeHardFailureFailsJob(t *testing.T) {
	flow := []model.FlowNode{
		conditionalGroup("done?", 3, promptStep("work", 1, model.RoundModeBeforeRound)),
	}
	job := newLoopTestJob("job-cond-judge-hardfail", flow)
	runner := &judgeRunner{
		judgeOutputs: []string{""}, // no text
		judgeErr:     fmt.Errorf("network exploded"),
	}

	runConditionalForTest(t, job, runner)

	if job.Status != model.JobStatusFailed {
		t.Fatalf("job status = %s, want Failed (judge hard failure)", job.Status)
	}
}

// A judge call that errors but still produced text is parsed normally (tool
// failure ≠ judge failure).
func TestConditionalJudgeErrorWithTextParsedNormally(t *testing.T) {
	flow := []model.FlowNode{
		conditionalGroup("done?", 3, promptStep("work", 1, model.RoundModeBeforeRound)),
	}
	job := newLoopTestJob("job-cond-judge-err-text", flow)
	runner := &judgeRunner{
		judgeOutputs: []string{"a tool errored but I conclude\nLOOP_DECISION: STOP"},
		judgeErr:     fmt.Errorf("a tool call failed mid-turn"),
	}

	runConditionalForTest(t, job, runner)

	if job.Status == model.JobStatusFailed {
		t.Fatalf("job status = %s, want not Failed (judge had text, parse normally)", job.Status)
	}
	if runner.businessRuns != 1 {
		t.Fatalf("businessRuns = %d, want 1 (stopped after round 1)", runner.businessRuns)
	}
}

// An empty completion condition behaves exactly like a fixed-count group: no
// judge turns, runs the full iteration count.
func TestEmptyConditionRunsFixedLoop(t *testing.T) {
	flow := []model.FlowNode{
		group(3, promptStep("work", 1, model.RoundModeBeforeRound)),
	}
	job := newLoopTestJob("job-fixed", flow)
	runner := &judgeRunner{judgeOutputs: []string{"LOOP_DECISION: STOP"}}

	runConditionalForTest(t, job, runner)

	if runner.judgeRuns != 0 {
		t.Fatalf("judgeRuns = %d, want 0 (no condition => no judge)", runner.judgeRuns)
	}
	if runner.businessRuns != 3 {
		t.Fatalf("businessRuns = %d, want 3 (fixed iterations)", runner.businessRuns)
	}
	if job.Progress.LastJudgeDecision != nil {
		t.Fatalf("lastJudgeDecision = %+v, want nil", job.Progress.LastJudgeDecision)
	}
}

// shellStopStep is a shell step that writes a STOP directive to the control
// file. Used to verify Shell STOP takes priority over the judge (§4).
func shellStopStep(directive string) model.FlowNode {
	return model.FlowNode{
		Type:      model.FlowNodeTypeStep,
		RoundType: model.RoundTypeShell,
		RoundMode: model.RoundModeBeforeRound,
		AgentType: "test-agent",
		Message:   "echo running\necho " + directive + " >> \"$QUARTET_CONTROL\"\n",
	}
}

// Shell STOP_LOOP inside a conditional group breaks the group BEFORE the judge
// runs (STOP_WORKFLOW > STOP_LOOP > judge, §4), then falls through to the sibling.
func TestConditionalShellStopLoopPreemptsJudge(t *testing.T) {
	memoryRoot := t.TempDir()
	t.Setenv("LOCAL_MEMORY", memoryRoot)

	flow := []model.FlowNode{
		conditionalGroup("done?", 5, shellStopStep("STOP_LOOP")),
		group(1, promptStep("after", 1, model.RoundModeBeforeRound)),
	}
	job := newLoopTestJob("job-cond-shell-stoploop", flow)
	job.Workdir = t.TempDir()
	runner := &judgeRunner{judgeOutputs: []string{"LOOP_DECISION: STOP"}}

	runConditionalForTest(t, job, runner)

	if runner.judgeRuns != 0 {
		t.Fatalf("judgeRuns = %d, want 0 (Shell STOP_LOOP preempts judge)", runner.judgeRuns)
	}
	// The sibling group's prompt step still runs after the conditional group breaks.
	if runner.businessRuns != 1 {
		t.Fatalf("businessRuns(prompt) = %d, want 1 (sibling after break)", runner.businessRuns)
	}
}

// Shell STOP_WORKFLOW inside a conditional group exits the whole workflow before
// the judge runs and without running any following sibling.
func TestConditionalShellStopWorkflowPreemptsJudge(t *testing.T) {
	memoryRoot := t.TempDir()
	t.Setenv("LOCAL_MEMORY", memoryRoot)

	flow := []model.FlowNode{
		conditionalGroup("done?", 5, shellStopStep("STOP_WORKFLOW")),
		group(1, promptStep("after", 1, model.RoundModeBeforeRound)),
	}
	job := newLoopTestJob("job-cond-shell-stopwf", flow)
	job.Workdir = t.TempDir()
	runner := &judgeRunner{judgeOutputs: []string{"LOOP_DECISION: STOP"}}

	runConditionalForTest(t, job, runner)

	if runner.judgeRuns != 0 {
		t.Fatalf("judgeRuns = %d, want 0 (Shell STOP_WORKFLOW preempts judge)", runner.judgeRuns)
	}
	if runner.businessRuns != 0 {
		t.Fatalf("businessRuns(prompt) = %d, want 0 (workflow exited, no sibling)", runner.businessRuns)
	}
}

// After a conditional group STOPs early, resume is advanced past the group so a
// Continue does not re-enter and re-judge it (§5.2).
func TestConditionalEarlyStopAdvancesResumePastGroup(t *testing.T) {
	flow := []model.FlowNode{
		conditionalGroup("done?", 10, promptStep("work", 1, model.RoundModeBeforeRound)),
		group(1, promptStep("after", 1, model.RoundModeBeforeRound)),
	}
	job := newLoopTestJob("job-cond-resume", flow)
	runner := &judgeRunner{judgeOutputs: []string{"done\nLOOP_DECISION: STOP"}}

	svc := newStateTestService()
	svc.jobs[job.ID] = job
	currentSessionID := ""
	lastSessionID := ""
	svc.runFlowNodes(context.Background(), job, runner, job.LoopConfig.Flow, nil, 0, &currentSessionID, &lastSessionID, false)

	// After the whole flow completes, resume is cleared by the last step. The
	// important property: resume never pointed back INTO the conditional group
	// after it STOPped — verify by re-running resumeForContinue against an
	// interrupted state. Here we assert the sibling ran (proving we advanced
	// past the group rather than re-judging it).
	if runner.businessRuns != 2 {
		t.Fatalf("businessRuns = %d, want 2 (conditional round + sibling, no re-entry)", runner.businessRuns)
	}
	if runner.judgeRuns != 1 {
		t.Fatalf("judgeRuns = %d, want 1 (judged once, not re-entered)", runner.judgeRuns)
	}
}
