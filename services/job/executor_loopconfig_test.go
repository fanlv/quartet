package job

import (
	"context"
	"errors"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/types/agui"
	"github.com/fanlv/quartet/types/model"
)

// loopEditTestJob builds a small two-step loop job registered in the service,
// in the given status, for exercising the LoopConfig edit paths.
func loopEditTestJob(svc *serviceImpl, status model.JobStatus) *model.Job {
	job := model.NewJob("/tmp", "")
	job.Mode = model.JobModeLoop
	job.Status = status
	job.LoopConfig = &model.LoopConfig{
		Flow: []model.FlowNode{
			{
				ID:             "g1",
				Type:           model.FlowNodeTypeGroup,
				IterationCount: 2,
				Children: []model.FlowNode{
					{ID: "s1", Type: model.FlowNodeTypeStep, RoundMode: model.RoundModeBeforeRound, Message: "old-1", AgentType: "claude"},
					{ID: "s2", Type: model.FlowNodeTypeStep, RoundMode: model.RoundModeNone, Message: "old-2", AgentType: "claude"},
				},
			},
		},
	}
	job.Progress = buildProgress(job.LoopConfig)
	svc.jobs[job.ID] = job
	return job
}

func cloneFlow(f []model.FlowNode) []model.FlowNode {
	return model.DeepCopyFlowNodes(f)
}

func TestUpdateRunningStepFields_FieldsOnly(t *testing.T) {
	svc := newStateTestService()
	job := loopEditTestJob(svc, model.JobStatusRunning)

	newFlow := cloneFlow(job.LoopConfig.Flow)
	newFlow[0].Children[0].Message = "new-1"
	newFlow[0].Children[0].StepModelID = "gpt-5"
	newFlow[0].Children[1].Message = "new-2"

	if err := svc.UpdateRunningStepFields(context.Background(), job.ID, newFlow); err != nil {
		t.Fatalf("UpdateRunningStepFields: %v", err)
	}

	got := svc.jobs[job.ID].LoopConfig.Flow
	if got[0].Children[0].Message != "new-1" || got[0].Children[0].StepModelID != "gpt-5" {
		t.Errorf("step 1 not updated: %+v", got[0].Children[0])
	}
	if got[0].Children[1].Message != "new-2" {
		t.Errorf("step 2 not updated: %+v", got[0].Children[1])
	}
	// liveStepFields must surface the new values for the running loop.
	msg, _, mid, _, ok := svc.liveStepFields(job, []int{0, 0, 0, 0})
	if !ok || msg != "new-1" || mid != "gpt-5" {
		t.Errorf("liveStepFields = (%q,%q,%v), want (new-1,gpt-5,true)", msg, mid, ok)
	}
}

func TestUpdateRunningStepFields_RejectsStructureChange(t *testing.T) {
	svc := newStateTestService()
	job := loopEditTestJob(svc, model.JobStatusRunning)

	cases := map[string]func([]model.FlowNode) []model.FlowNode{
		"add step": func(f []model.FlowNode) []model.FlowNode {
			f[0].Children = append(f[0].Children, model.FlowNode{ID: "s3", Type: model.FlowNodeTypeStep, RoundMode: model.RoundModeNone, Message: "x"})
			return f
		},
		"change iterationCount": func(f []model.FlowNode) []model.FlowNode {
			f[0].IterationCount = 5
			return f
		},
		"change roundMode": func(f []model.FlowNode) []model.FlowNode {
			f[0].Children[1].RoundMode = model.RoundModeEachRepeat
			return f
		},
		"change id": func(f []model.FlowNode) []model.FlowNode {
			f[0].Children[0].ID = "renamed"
			return f
		},
	}
	for name, mutate := range cases {
		t.Run(name, func(t *testing.T) {
			if err := svc.UpdateRunningStepFields(context.Background(), job.ID, mutate(cloneFlow(job.LoopConfig.Flow))); err != ErrLoopStructureChanged {
				t.Fatalf("got %v, want ErrLoopStructureChanged", err)
			}
		})
	}
}

func TestReplaceLoopConfig_RejectsRunning(t *testing.T) {
	svc := newStateTestService()
	job := loopEditTestJob(svc, model.JobStatusRunning)
	if _, err := svc.ReplaceLoopConfig(context.Background(), job.ID, &model.LoopConfig{Flow: cloneFlow(job.LoopConfig.Flow)}); err != ErrJobRunning {
		t.Fatalf("got %v, want ErrJobRunning", err)
	}
}

func TestReplaceLoopConfig_RecomputesTotalAndReconciles(t *testing.T) {
	svc := newStateTestService()
	job := loopEditTestJob(svc, model.JobStatusStopped)
	// Pretend step s2 (path 0.0.1.0) already ran, and resume points past it.
	job.Progress.Results = []model.IterationResult{
		{Path: []int{0, 0, 0, 0}, Success: true},
		{Path: []int{0, 0, 1, 0}, Success: true},
	}
	job.Progress.CompletedCount = 2
	job.Progress.CurrentPath = []int{0, 0, 1, 0}
	job.Resume = &model.JobResume{NextPath: []int{0, 1, 0, 0}}

	// New flow keeps s1 but drops s2 and the group; a single top-level step.
	newFlow := []model.FlowNode{
		{ID: "s1", Type: model.FlowNodeTypeStep, RoundMode: model.RoundModeBeforeRound, Message: "only", AgentType: "claude"},
	}
	progress, err := svc.ReplaceLoopConfig(context.Background(), job.ID, &model.LoopConfig{Flow: cloneFlow(newFlow)})
	if err != nil {
		t.Fatalf("ReplaceLoopConfig: %v", err)
	}
	if progress.TotalSteps != 1 {
		t.Errorf("TotalSteps = %d, want 1", progress.TotalSteps)
	}
	// The s2 result (path 0.0.1.0) is gone; the s1 result (0.0.0.0) is also
	// gone because its old group path no longer resolves in the new flat flow.
	for _, r := range progress.Results {
		if model.EqualPaths(r.Path, []int{0, 0, 1, 0}) {
			t.Errorf("stale result for removed step survived: %+v", r)
		}
	}
	// Resume pointed at 0.1.0.0 which no longer exists → cleared.
	if svc.jobs[job.ID].Resume != nil {
		t.Errorf("Resume should be cleared when its path is invalid, got %+v", svc.jobs[job.ID].Resume)
	}
	if progress.CompletedCount != len(progress.Results) {
		t.Errorf("CompletedCount %d out of sync with %d results", progress.CompletedCount, len(progress.Results))
	}
}

func TestReplaceLoopConfig_KeepsValidResume(t *testing.T) {
	svc := newStateTestService()
	job := loopEditTestJob(svc, model.JobStatusStopped)
	job.Resume = &model.JobResume{NextPath: []int{0, 0, 1, 0}} // s2 in round 0

	// Edit only step messages; structure (hence all paths) stays valid.
	newFlow := cloneFlow(job.LoopConfig.Flow)
	newFlow[0].Children[0].Message = "edited"

	if _, err := svc.ReplaceLoopConfig(context.Background(), job.ID, &model.LoopConfig{Flow: newFlow}); err != nil {
		t.Fatalf("ReplaceLoopConfig: %v", err)
	}
	r := svc.jobs[job.ID].Resume
	if r == nil || !model.EqualPaths(r.NextPath, []int{0, 0, 1, 0}) {
		t.Errorf("valid resume should be preserved, got %+v", r)
	}
	if svc.jobs[job.ID].LoopConfig.Flow[0].Children[0].Message != "edited" {
		t.Errorf("flow edit not applied")
	}
}

func TestReplaceLoopConfig_ReconcilesByNodeID(t *testing.T) {
	svc := newStateTestService()
	job := loopEditTestJob(svc, model.JobStatusStopped)
	job.Progress.Results = []model.IterationResult{
		{Path: []int{0, 0, 0, 0}, Success: true, SessionID: "old-s1"},
		{Path: []int{0, 0, 1, 0}, Success: true, SessionID: "old-s2"},
	}
	job.Progress.CompletedCount = 2
	job.Progress.CurrentPath = []int{0, 0, 0, 0}
	job.Resume = &model.JobResume{NextPath: []int{0, 0, 0, 0}, SessionID: "old-s1"}

	newFlow := cloneFlow(job.LoopConfig.Flow)
	newFlow[0].Children[0].ID = "s1-replaced"
	newFlow[0].Children[0].Message = "new step at old path"

	progress, err := svc.ReplaceLoopConfig(context.Background(), job.ID, &model.LoopConfig{Flow: newFlow})
	if err != nil {
		t.Fatalf("ReplaceLoopConfig: %v", err)
	}
	if svc.jobs[job.ID].Resume != nil {
		t.Fatalf("resume for replaced node should be cleared, got %+v", svc.jobs[job.ID].Resume)
	}
	if progress.CurrentPath != nil {
		t.Fatalf("currentPath for replaced node should be cleared, got %v", progress.CurrentPath)
	}
	if len(progress.Results) != 1 || !model.EqualPaths(progress.Results[0].Path, []int{0, 0, 1, 0}) {
		t.Fatalf("results = %+v, want only unchanged s2 result", progress.Results)
	}
	if progress.CompletedCount != 1 || progress.FailedCount != 0 {
		t.Fatalf("counts completed=%d failed=%d, want 1/0", progress.CompletedCount, progress.FailedCount)
	}
}

func TestReplaceLoopConfig_CopiesInputOwnership(t *testing.T) {
	svc := newStateTestService()
	job := loopEditTestJob(svc, model.JobStatusStopped)

	cfg := &model.LoopConfig{
		Flow:      cloneFlow(job.LoopConfig.Flow),
		Variables: map[string]string{"user": "before"},
	}
	if _, err := svc.ReplaceLoopConfig(context.Background(), job.ID, cfg); err != nil {
		t.Fatalf("ReplaceLoopConfig: %v", err)
	}

	cfg.Flow[0].Children[0].Message = "mutated-after-return"
	cfg.Variables["user"] = "after"

	got := svc.jobs[job.ID].LoopConfig
	if got.Flow[0].Children[0].Message == "mutated-after-return" {
		t.Fatalf("live flow aliases caller-owned cfg.Flow")
	}
	if got.Variables["user"] != "before" {
		t.Fatalf("live variables alias caller-owned cfg.Variables: %+v", got.Variables)
	}
}

func TestLoopConfigValidationErrorsAreMappedSentinel(t *testing.T) {
	svc := newStateTestService()
	job := loopEditTestJob(svc, model.JobStatusStopped)

	_, err := svc.ReplaceLoopConfig(context.Background(), job.ID, &model.LoopConfig{Flow: nil})
	if !errors.Is(err, ErrLoopConfigInvalid) {
		t.Fatalf("ReplaceLoopConfig err=%v, want ErrLoopConfigInvalid", err)
	}

	running := loopEditTestJob(svc, model.JobStatusRunning)
	err = svc.UpdateRunningStepFields(context.Background(), running.ID, nil)
	if !errors.Is(err, ErrLoopConfigInvalid) {
		t.Fatalf("UpdateRunningStepFields err=%v, want ErrLoopConfigInvalid", err)
	}
}

// gracefulStopRunner requests a graceful stop on the Nth RunIteration call, so
// the test can assert the loop stops at the boundary after that step.
type gracefulStopRunner struct {
	svc          *serviceImpl
	jobID        string
	stopAfterRun int // request graceful stop during this run number (1-based)
	runPaths     [][]int
}

func (r *gracefulStopRunner) InitSession(ctx context.Context, jobID string, overrides *model.SessionOverrides) (string, error) {
	return "session-graceful", nil
}

func (r *gracefulStopRunner) RunIteration(ctx context.Context, sessionID string, messages []*schema.Message, handler agui.EventHandler) error {
	if h, ok := handler.(*loopEventHandler); ok {
		r.runPaths = append(r.runPaths, model.CopyPath(h.path))
	}
	if len(r.runPaths) == r.stopAfterRun {
		r.svc.RequestGracefulStop(r.jobID)
	}
	return nil
}

func (r *gracefulStopRunner) SessionModelID(sessionID string) string { return "" }

func TestGracefulStop_StopsAtNextBoundary(t *testing.T) {
	svc := newStateTestService()
	// A single group of 3 sequential prompt steps; first creates a session.
	flow := []model.FlowNode{
		group(1,
			model.FlowNode{ID: "a", Type: model.FlowNodeTypeStep, Message: "1", RepeatCount: 1, RoundMode: model.RoundModeBeforeRound, RoundType: model.RoundTypePrompt, AgentType: "x"},
			model.FlowNode{ID: "b", Type: model.FlowNodeTypeStep, Message: "2", RepeatCount: 1, RoundMode: model.RoundModeNone, RoundType: model.RoundTypePrompt, AgentType: "x"},
			model.FlowNode{ID: "c", Type: model.FlowNodeTypeStep, Message: "3", RepeatCount: 1, RoundMode: model.RoundModeNone, RoundType: model.RoundTypePrompt, AgentType: "x"},
		),
	}
	job := newLoopTestJob("job-graceful", flow)
	svc.jobs[job.ID] = job

	runner := &gracefulStopRunner{svc: svc, jobID: job.ID, stopAfterRun: 1}
	sid := ""
	result, _, _ := svc.runFlowNodes(context.Background(), job, runner, job.LoopConfig.Flow, nil, 0, &sid)

	if result != stepStopGraceful {
		t.Fatalf("result = %v, want stepStopGraceful", result)
	}
	// Only the first step (path 0.0.0.0) ran; steps b and c did not.
	if len(runner.runPaths) != 1 {
		t.Fatalf("ran %d steps, want 1: %v", len(runner.runPaths), runner.runPaths)
	}
	// Resume should point at the second step (path 0.0.1.0) so Continue resumes
	// cleanly without re-running step a.
	if job.Resume == nil || !model.EqualPaths(job.Resume.NextPath, []int{0, 0, 1, 0}) {
		t.Fatalf("resume = %+v, want NextPath 0.0.1.0", job.Resume)
	}
	// The completed step recorded one result; the denominator stays at 3 (the
	// remaining steps are "to be continued", not skipped).
	if job.Progress.CompletedCount != 1 || job.Progress.TotalSteps != 3 {
		t.Fatalf("progress completed=%d total=%d, want 1/3", job.Progress.CompletedCount, job.Progress.TotalSteps)
	}
	// The flag was consumed, so a later run isn't immediately stopped.
	if svc.consumeGracefulStop(job.ID) {
		t.Errorf("graceful-stop flag should have been consumed")
	}
}

func TestGracefulStop_ClearedAtLaunch(t *testing.T) {
	svc := newStateTestService()
	svc.RequestGracefulStop("job-x")
	svc.markLoopRun("job-x")
	if !svc.IsGracefulStopSupported("job-x") {
		t.Fatalf("loop run should support graceful stop")
	}
	svc.clearGracefulStop("job-x")
	svc.clearLoopRun("job-x")
	if svc.consumeGracefulStop("job-x") {
		t.Errorf("clearGracefulStop should drop the pending request")
	}
	if svc.IsGracefulStopSupported("job-x") {
		t.Errorf("clearLoopRun should make graceful stop unsupported")
	}
}

func TestGracefulStop_UnsupportedForInteractiveRun(t *testing.T) {
	svc := newStateTestService()
	res := svc.prepareRunResources("job-interactive", 0, false)
	defer svc.abortRunResources("job-interactive", res)
	if svc.IsGracefulStopSupported("job-interactive") {
		t.Fatalf("interactive run should not support graceful stop")
	}
}
