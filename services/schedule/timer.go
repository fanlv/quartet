package schedule

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/types/model"
)

var (
	ErrTimerForbidden = errors.New("missing timer execution permission")
	ErrTimerNotFound  = errors.New("timer not found")
	ErrTimerConflict  = errors.New("timer cannot be run or authorized")
	ErrTimerInvalid   = errors.New("invalid timer configuration")
)

type TimerJobs struct {
	Resolve  func(context.Context, *model.ScheduledTask) (*model.TimerTarget, error)
	Validate func(context.Context, *model.TimerTarget) error
	Find     func(string) (*model.Job, bool)
	List     func() []*model.Job
	ID       func(string) string
	Prepare  func(context.Context, *model.TimerEntry) (*model.Job, error)
	Launch   func(context.Context, *model.TimerEntry, *model.Job) error
}

type TimerManager struct {
	mu          sync.Mutex
	definitions repository.ScheduleRepo
	history     *repository.TimerRepo
	jobs        TimerJobs
	resuming    map[string]string
}

func NewTimerManager(jobs TimerJobs) (*TimerManager, error) {
	definitions, err := repository.NewScheduleRepo()
	if err != nil {
		return nil, err
	}
	history, err := repository.NewTimerRepo()
	if err != nil {
		return nil, err
	}
	return &TimerManager{definitions: definitions, history: history, jobs: jobs, resuming: make(map[string]string)}, nil
}

func (m *TimerManager) Preview(ctx context.Context, req *model.CreateScheduleRequest) (*model.ScheduledTask, error) {
	task := timerTaskFromRequest(req, time.Now())
	target, candidate, err := m.resolve(ctx, &task)
	if err == nil {
		err = m.validateTarget(ctx, target)
	}
	return candidate, err
}

func (m *TimerManager) PreviewUpdate(ctx context.Context, id string, req *model.UpdateScheduleRequest) (*model.ScheduledTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	task, _, err := m.load(ctx, id, time.Now())
	if err != nil {
		return nil, err
	}
	_ = normalizeTimer(task)
	patchTimer(task, req)
	target, preview, err := m.resolve(ctx, task)
	if err == nil {
		err = m.validateTarget(ctx, target)
	}
	return preview, err
}

func (m *TimerManager) resolve(ctx context.Context, task *model.ScheduledTask) (*model.TimerTarget, *model.ScheduledTask, error) {
	if err := normalizeTimer(task); err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrTimerInvalid, err)
	}
	target, err := m.jobs.Resolve(ctx, task)
	if err != nil {
		return nil, nil, fmt.Errorf("%w: %w", ErrTimerInvalid, err)
	}
	preview := target.Task
	if target.Workflow != nil {
		preview.WorkflowVersion = target.Workflow.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	preview.ApprovalPreview, err = timerFingerprint(target)
	return target, &preview, err
}

func (m *TimerManager) validateTarget(ctx context.Context, target *model.TimerTarget) error {
	if err := m.jobs.Validate(ctx, target); err != nil {
		return fmt.Errorf("%w: %w", ErrTimerInvalid, err)
	}
	return nil
}

func (m *TimerManager) checkApproval(task *model.ScheduledTask, ledger *model.TimerLedger, candidate string, now time.Time) error {
	if candidate == "" || candidate != task.ApprovalPreview {
		return fmt.Errorf("%w: execution configuration must match the latest preview", ErrTimerConflict)
	}
	if reason := timerEndReason(task, ledger, now); reason != "" {
		return fmt.Errorf("%w: %s", ErrTimerConflict, reason)
	}
	if nextTimerTime(task, now) == nil {
		return fmt.Errorf("%w: no future automatic trigger in the plan", ErrTimerConflict)
	}
	return nil
}

func (m *TimerManager) Create(ctx context.Context, req *model.CreateScheduleRequest, permitted func(string) bool) (*model.ScheduledTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	task := timerTaskFromRequest(req, now)
	task.ID = model.NewScheduleID()
	target, preview, err := m.resolve(ctx, &task)
	if err != nil {
		return nil, err
	}
	allowed := permitted(task.Kind)
	enabled := allowed
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	if enabled && !allowed {
		return nil, fmt.Errorf("%w: %s timer", ErrTimerForbidden, task.Kind)
	}
	if task.RunAt != "" && nextTimerTime(&task, now) == nil {
		return nil, fmt.Errorf("%w: single execution time must be in the future", ErrTimerInvalid)
	}
	ledger := &model.TimerLedger{ID: task.ID, CheckedThrough: now.Truncate(time.Minute)}
	task.WorkspaceID, task.Workdir = target.Task.WorkspaceID, target.Task.Workdir
	task.AgentID, task.ModelID = target.Task.AgentID, target.Task.ModelID
	if err := m.validateTarget(ctx, target); err != nil {
		return nil, err
	}
	if enabled {
		if err := m.checkApproval(preview, ledger, req.ExpectedApproval, now); err != nil {
			return nil, err
		}
		if err := m.approve(&task, target, ledger, now); err != nil {
			return nil, err
		}
	}
	if err := m.definitions.Create(ctx, &task); err != nil {
		return nil, err
	}
	if err := m.history.Save(ledger); err != nil {
		return nil, errors.Join(err, m.definitions.Delete(ctx, task.ID))
	}
	return m.present(&task, ledger, preview, now), nil
}

func (m *TimerManager) approve(task *model.ScheduledTask, target *model.TimerTarget, ledger *model.TimerLedger, now time.Time) error {
	definition, err := timerDefinitionFingerprint(task)
	if err != nil {
		return err
	}
	approval, err := timerFingerprint(target)
	if err != nil {
		return err
	}
	ledger.Definition, ledger.Approval, ledger.ApprovedTarget = definition, approval, target
	ledger.Enabled, ledger.PendingReason = true, ""
	ledger.CheckedThrough = now.Truncate(time.Minute)
	return nil
}

func (m *TimerManager) Update(ctx context.Context, id string, req *model.UpdateScheduleRequest, permitted func(string) bool) (*model.ScheduledTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	task, ledger, err := m.load(ctx, id, now)
	if err != nil {
		return nil, err
	}
	fields := *req
	fields.Enabled, fields.ExpectedApproval = nil, ""
	if req.Enabled != nil && !*req.Enabled && fields == (model.UpdateScheduleRequest{}) {
		ledger.Enabled = false
		if err := m.history.Save(ledger); err != nil {
			return nil, err
		}
		_, preview, err := m.inspect(ctx, task, ledger)
		if err != nil {
			return nil, err
		}
		return m.present(task, ledger, preview, now), nil
	}
	if _, _, err := m.inspect(ctx, task, ledger); err != nil {
		return nil, err
	}
	previous, err := timerDefinitionFingerprint(task)
	if err != nil {
		return nil, err
	}
	oldName := task.Name
	patchTimer(task, req)
	if err := normalizeTimer(task); err != nil {
		return nil, fmt.Errorf("%w: %w", ErrTimerInvalid, err)
	}
	current, err := timerDefinitionFingerprint(task)
	if err != nil {
		return nil, err
	}
	changed := current != previous
	enable := req.Enabled != nil && *req.Enabled
	if (enable || changed && (ledger.Enabled || ledger.PendingReason != "")) && !permitted(task.Kind) {
		return nil, fmt.Errorf("%w: %s timer", ErrTimerForbidden, task.Kind)
	}
	var preview *model.ScheduledTask
	if changed || enable {
		if task.RunAt != "" && changed && nextTimerTime(task, now) == nil {
			return nil, fmt.Errorf("%w: single execution time must be in the future", ErrTimerInvalid)
		}
		var target *model.TimerTarget
		candidate := *task
		if !changed && ledger.Definition == current && ledger.ApprovedTarget != nil {
			useApprovedDefaults(&candidate, ledger.ApprovedTarget.Task)
		}
		target, preview, err = m.resolve(ctx, &candidate)
		if err != nil {
			return nil, err
		}
		if err := m.validateTarget(ctx, target); err != nil {
			return nil, err
		}
		task.WorkspaceID, task.Workdir = target.Task.WorkspaceID, target.Task.Workdir
		task.AgentID, task.ModelID = target.Task.AgentID, target.Task.ModelID
		if enable || ledger.Enabled && ledger.PendingReason == "" && changed && req.Enabled == nil {
			if err := m.checkApproval(preview, ledger, req.ExpectedApproval, now); err != nil {
				return nil, err
			}
			if err := m.approve(task, target, ledger, now); err != nil {
				return nil, err
			}
		}
	}
	if req.Enabled != nil && !*req.Enabled {
		ledger.Enabled = false
	}
	if changed || enable || oldName != task.Name {
		task.UpdatedAt = now
		if err := m.definitions.SaveDefinition(ctx, task); err != nil {
			return nil, err
		}
	}
	if err := m.history.Save(ledger); err != nil {
		return nil, err
	}
	return m.present(task, ledger, preview, now), nil
}

func (m *TimerManager) Delete(ctx context.Context, id string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, _, err := m.load(ctx, id, time.Now()); err != nil {
		return err
	}
	return m.definitions.Delete(ctx, id)
}

func (m *TimerManager) Get(ctx context.Context, id string) (*model.ScheduledTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	task, ledger, err := m.load(ctx, id, now)
	if err != nil {
		return nil, err
	}
	_, preview, err := m.inspect(ctx, task, ledger)
	if err != nil {
		return nil, err
	}
	return m.present(task, ledger, preview, now), nil
}

func (m *TimerManager) List(ctx context.Context, workspace string) ([]*model.ScheduledTask, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	tasks, err := m.definitions.List(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]*model.ScheduledTask, 0, len(tasks))
	for _, task := range tasks {
		ledger, err := m.loadLedger(ctx, task, now)
		if err != nil {
			return nil, err
		}
		_, preview, err := m.inspect(ctx, task, ledger)
		if err != nil {
			return nil, err
		}
		task = m.present(task, ledger, preview, now)
		if workspace == "" || task.WorkspaceID == workspace {
			out = append(out, task)
		}
	}
	return out, nil
}

func (m *TimerManager) load(ctx context.Context, id string, now time.Time) (*model.ScheduledTask, *model.TimerLedger, error) {
	task, err := m.definitions.Get(ctx, id)
	if err != nil {
		return nil, nil, err
	}
	if task == nil {
		return nil, nil, ErrTimerNotFound
	}
	ledger, err := m.loadLedger(ctx, task, now)
	return task, ledger, err
}

func (m *TimerManager) loadLedger(ctx context.Context, task *model.ScheduledTask, now time.Time) (*model.TimerLedger, error) {
	ledger, err := m.history.Load(task.ID)
	if err != nil || ledger != nil {
		return ledger, err
	}
	ledger = &model.TimerLedger{ID: task.ID, CheckedThrough: now.Truncate(time.Minute)}
	if task.Kind != "" {
		return ledger, nil
	}
	legacy := *task
	if task.Enabled {
		target, _, err := m.resolve(ctx, task)
		if err == nil {
			err = m.validateTarget(ctx, target)
		}
		if err != nil {
			ledger.PendingReason = err.Error()
		} else if err := m.approve(task, target, ledger, now); err != nil {
			return nil, err
		}
	}
	for _, job := range m.jobs.List() {
		if job.ScheduleID != task.ID || job.Deleted {
			continue
		}
		trigger := job.CreatedAt
		ledger.Entries = append(ledger.Entries, model.TimerEntry{TimerExecution: model.TimerExecution{
			ID: job.ID, ScheduleID: task.ID, Name: task.Name, Kind: model.TimerGraph, WorkflowID: task.GraphWorkflowID,
			WorkspaceID: job.WorkspaceID, JobID: job.ID, Trigger: "legacy", TriggeredAt: &trigger, Status: model.TimerLaunched,
		}})
	}
	if legacy.LastTriggerError != "" && legacy.LastRunAt != nil {
		found := false
		for i := range ledger.Entries {
			if ledger.Entries[i].JobID == legacy.LastRunJobID {
				if job, ok := m.jobs.Find(legacy.LastRunJobID); ok && job.Status == model.JobStatusFailed {
					ledger.Entries[i].Error = legacy.LastTriggerError
					found = true
				}
				break
			}
		}
		if !found {
			ledger.Entries = append(ledger.Entries, model.TimerEntry{TimerExecution: model.TimerExecution{
				ID: task.ID + "-legacy-failure", ScheduleID: task.ID, Name: task.Name, Kind: model.TimerGraph,
				WorkflowID: task.GraphWorkflowID, WorkspaceID: task.WorkspaceID, Trigger: "legacy",
				TriggeredAt: legacy.LastRunAt, Status: string(model.JobStatusFailed), Error: legacy.LastTriggerError,
			}})
		}
	}
	ledger.PreviousCount = max(0, legacy.RunCount-len(ledger.Entries))
	if err := m.history.Save(ledger); err != nil {
		return nil, err
	}
	return ledger, nil
}

// inspect checks the local approval against a single observed definition and workflow.
func (m *TimerManager) inspect(ctx context.Context, task *model.ScheduledTask, ledger *model.TimerLedger) (*model.TimerTarget, *model.ScheduledTask, error) {
	if err := normalizeTimer(task); err != nil {
		if ledger.Enabled && ledger.PendingReason == "" {
			ledger.PendingReason = err.Error()
			return nil, nil, m.history.Save(ledger)
		}
		return nil, nil, nil
	}
	definition, err := timerDefinitionFingerprint(task)
	if err != nil {
		return nil, nil, err
	}
	candidate := *task
	if ledger.Definition == definition && ledger.ApprovedTarget != nil {
		useApprovedDefaults(&candidate, ledger.ApprovedTarget.Task)
	}
	target, preview, resolveErr := m.resolve(ctx, &candidate)
	if !ledger.Enabled || ledger.PendingReason != "" {
		return target, preview, nil
	}
	reason := ""
	switch {
	case ledger.Definition != definition:
		reason = "Timer configuration changed since local approval"
	case resolveErr == nil && ledger.Approval != preview.ApprovalPreview:
		reason = "Execution dependencies changed since local approval"
	}
	if reason != "" {
		ledger.PendingReason = reason
		return nil, preview, m.history.Save(ledger)
	}
	if resolveErr != nil {
		return nil, nil, nil
	}
	return target, preview, nil
}

func useApprovedDefaults(task *model.ScheduledTask, approval model.ScheduledTask) {
	task.WorkspaceID, task.Workdir = approval.WorkspaceID, approval.Workdir
	task.AgentID, task.ModelID = approval.AgentID, approval.ModelID
}

func (m *TimerManager) present(task *model.ScheduledTask, ledger *model.TimerLedger, preview *model.ScheduledTask, now time.Time) *model.ScheduledTask {
	out := *task
	if out.Kind == "" {
		out.Kind = model.TimerGraph
	}
	if preview != nil {
		out.ApprovalPreview, out.WorkflowVersion = preview.ApprovalPreview, preview.WorkflowVersion
		out.WorkspaceID, out.Workdir, out.AgentID, out.ModelID = preview.WorkspaceID, preview.Workdir, preview.AgentID, preview.ModelID
	} else if ledger.ApprovedTarget != nil && ledger.ApprovedTarget.Workflow != nil {
		out.WorkflowVersion = ledger.ApprovedTarget.Workflow.UpdatedAt.UTC().Format(time.RFC3339Nano)
	}
	if ledger.ApprovedTarget != nil && ledger.Enabled && ledger.PendingReason == "" {
		approved := ledger.ApprovedTarget.Task
		out.WorkspaceID, out.Workdir, out.AgentID, out.ModelID = approved.WorkspaceID, approved.Workdir, approved.AgentID, approved.ModelID
	}
	out.Enabled, out.NextRunAt, out.PlanState = false, nil, model.TimerDisabled
	out.RunCount = timerRunCount(ledger)
	switch {
	case ledger.PendingReason != "":
		out.PlanState, out.StateReason = model.TimerPendingApproval, ledger.PendingReason
	default:
		out.StateReason = timerEndReason(task, ledger, now)
		if out.StateReason != "" {
			out.PlanState = model.TimerEnded
		} else if ledger.Enabled {
			out.Enabled, out.PlanState = true, model.TimerEnabled
			out.NextRunAt = nextTimerTime(task, now)
		}
	}
	for _, entry := range ledger.Entries {
		if entry.TriggeredAt == nil || out.LastRunAt != nil && out.LastRunAt.After(*entry.TriggeredAt) {
			continue
		}
		record := m.executionRecord(entry)
		out.LastRunAt, out.LastRunJobID, out.LastStatus, out.LastTriggerError = record.TriggeredAt, record.JobID, model.JobStatus(record.Status), record.Error
	}
	return &out
}
