package handler

import (
	"context"
	"fmt"

	"github.com/fanlv/quartet/services/agent/probe"
	"github.com/fanlv/quartet/services/job"
	"github.com/fanlv/quartet/services/schedule"
	"github.com/fanlv/quartet/types/consts"
	"github.com/fanlv/quartet/types/model"
)

func (h *Handler) StartTimers(ctx context.Context) error {
	timers, err := schedule.NewTimerManager(schedule.TimerJobs{
		Resolve: h.resolveTimerTarget, Validate: h.validateTimerTarget, Find: h.jobService.Get, List: h.jobService.List,
		ID: timerJobID, Prepare: h.prepareTimerJob, Launch: h.launchTimerJob,
	})
	if err != nil {
		return err
	}
	h.timers = timers
	timers.Start(ctx)
	return nil
}

func timerJobID(executionID string) string {
	return job.IdempotentJobID("timer:" + executionID)
}

func (h *Handler) resolveTimerTarget(ctx context.Context, task *model.ScheduledTask) (*model.TimerTarget, error) {
	target := &model.TimerTarget{Task: *task}
	t := &target.Task
	if t.WorkspaceID == "" {
		t.WorkspaceID = consts.DefaultWorkspaceID
	}
	ws, found := h.workspaceService.Get(t.WorkspaceID)
	if !found {
		return nil, fmt.Errorf("workspace %s not found", t.WorkspaceID)
	}
	if t.Workdir == "" {
		t.Workdir = ws.Workdir
	}
	if err := validateWorkdir(t.Workdir); err != nil {
		return nil, err
	}
	if err := ensureWorkdirWithinWorkspace(t.Workdir, ws.Workdir); err != nil {
		return nil, err
	}
	if t.Kind == model.TimerGraph {
		workflow, err := h.graphService.GetWorkflow(ctx, t.GraphWorkflowID)
		if err != nil {
			return nil, fmt.Errorf("load workflow %s: %w", t.GraphWorkflowID, err)
		}
		target.Workflow = workflow
		return target, nil
	}
	if t.AgentID == "" {
		t.AgentID = ws.DefaultAgent
	}
	if t.ModelID == "" && t.AgentID == ws.DefaultAgent {
		t.ModelID = ws.DefaultModel
	}
	if t.AgentID == "" || t.ModelID == "" {
		return nil, fmt.Errorf("timer requires an Agent and model or usable workspace defaults")
	}
	binding, found, err := h.agentCatalog.ResolveBinding(ctx, t.AgentID, "")
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, fmt.Errorf("Agent %s not found", t.AgentID)
	}
	if err := h.validateInteractiveExecutionAgent(ctx, binding.AgentID, binding.Revision); err != nil {
		return nil, err
	}
	t.AgentID, target.AgentRevision = binding.AgentID, binding.Revision
	return target, nil
}

func (h *Handler) validateTimerTarget(ctx context.Context, target *model.TimerTarget) error {
	if workflow := target.Workflow; workflow != nil {
		cfg := workflow.Config
		cfg.WorkspaceID, cfg.Workdir = target.Task.WorkspaceID, target.Task.Workdir
		if errs := h.graphService.ValidateConfig(ctx, &cfg); len(errs) != 0 {
			return fmt.Errorf("invalid workflow %s: %s", workflow.ID, formatGraphValidationErrors(errs))
		}
		return nil
	}
	t := target.Task
	envVersion := h.settingsService.GetACPEnvVersion(t.AgentID)
	if validation, ok := probe.CachedAgentValidation(t.AgentID, target.AgentRevision, envVersion); ok && validation.Models != nil {
		for _, model := range validation.Models.AvailableModels {
			if model.ModelId == t.ModelID {
				return nil
			}
		}
		if len(validation.Models.AvailableModels) > 0 {
			return fmt.Errorf("model %s is unavailable for Agent %s", t.ModelID, t.AgentID)
		}
	}
	return nil
}

func (h *Handler) prepareTimerJob(ctx context.Context, entry *model.TimerEntry) (*model.Job, error) {
	t := &entry.Target.Task
	if err := h.validateTimerTarget(ctx, entry.Target); err != nil {
		return nil, err
	}
	ws, found := h.workspaceService.Get(t.WorkspaceID)
	if !found {
		return nil, fmt.Errorf("workspace %s not found", t.WorkspaceID)
	}
	if err := validateWorkdir(t.Workdir); err != nil {
		return nil, err
	}
	if err := ensureWorkdirWithinWorkspace(t.Workdir, ws.Workdir); err != nil {
		return nil, err
	}
	id := timerJobID(entry.ID)
	if existing, ok := h.jobService.Get(id); ok {
		if existing.Deleted || existing.ScheduleID != entry.ScheduleID || existing.CreationClientMessageID != entry.ID {
			return nil, fmt.Errorf("Job %s cannot be reused for execution %s", id, entry.ID)
		}
		return existing, nil
	}
	j := model.NewJob(t.Workdir, t.WorkspaceID)
	j.ID, j.ScheduleID, j.CreationClientMessageID, j.CreationPayloadHash = id, entry.ScheduleID, entry.ID, entry.ID
	j.Title = consts.ScheduleJobTitlePrefix + t.Name
	j.InitialAgentID, j.FirstModelID = t.AgentID, t.ModelID
	if t.Kind == model.TimerGraph {
		j.Mode = model.JobModeGraph
	}
	j, _, err := h.jobService.CreateIdempotent(j)
	return j, err
}

func (h *Handler) launchTimerJob(ctx context.Context, entry *model.TimerEntry, j *model.Job) error {
	t := &entry.Target.Task
	if t.Kind == model.TimerGraph {
		if j.GraphRunID != "" {
			return nil
		}
		_, err := h.graphService.StartRun(ctx, &model.StartGraphRunRequest{
			JobID: j.ID, WorkspaceID: j.WorkspaceID, Workdir: j.Workdir, ScheduledWorkflow: entry.Target.Workflow,
		}, newJobRunner(h, j), h.jobService)
		if err != nil {
			if failErr := h.jobService.FailGraphJob(ctx, j.ID, err.Error()); failErr != nil {
				return fmt.Errorf("launch workflow: %w (Job failure could not be saved: %v)", err, failErr)
			}
		}
		return err
	}
	binding, found, err := h.agentCatalog.ResolveBinding(ctx, t.AgentID, entry.Target.AgentRevision)
	if err != nil {
		return err
	}
	if !found {
		return fmt.Errorf("Agent %s revision %s not found", t.AgentID, entry.Target.AgentRevision)
	}
	if err := h.validateInteractiveExecutionAgent(ctx, binding.AgentID, binding.Revision); err != nil {
		return err
	}
	release, acquired := h.agentExecutions.acquireExecution(binding.AgentID)
	if !acquired {
		return fmt.Errorf("Agent %s is being deleted", binding.AgentID)
	}
	runner := newJobRunner(h, j)
	runner.holdPreparedExecution(release)
	opts, err := h.prepareJobMessage(j, &model.JobMessageRequest{
		Messages:  []model.RequestMessage{{Type: "text", Role: "user", Content: t.Prompt}},
		AgentType: binding.AgentID, ModelID: t.ModelID, ClientMessageID: entry.ID, BypassCommand: true,
	})
	if err != nil {
		runner.ReleasePreparedExecution()
		return err
	}
	opts.AgentBinding = &binding
	opts.TimeoutMinutes = t.Timeout
	_, err = h.jobService.SendMessage(ctx, j.ID, runner, opts)
	return err
}

func (h *Handler) resumeTimerGraph(ctx context.Context, j *model.Job, resume func() error) error {
	if h.timers == nil {
		return resume()
	}
	return h.timers.Resume(ctx, j, resume)
}
