package schedule

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/types/model"
)

var (
	ErrInvalidTargetType     = errors.New("invalid schedule target type")
	ErrInvalidLoopConfig     = errors.New("invalid schedule loop config")
	ErrGraphWorkflowRequired = errors.New("graph workflow id is required")
	ErrGraphWorkflowNotFound = errors.New("graph workflow not found")
)

// Service provides CRUD operations for scheduled tasks.
type Service interface {
	Create(ctx context.Context, req *model.CreateScheduleRequest) (*model.ScheduledTask, error)
	Get(ctx context.Context, id string) (*model.ScheduledTask, error)
	List(ctx context.Context) ([]*model.ScheduledTask, error)
	ListByWorkspace(ctx context.Context, wsID string) ([]*model.ScheduledTask, error)
	Update(ctx context.Context, id string, req *model.UpdateScheduleRequest) (*model.ScheduledTask, error)
	Delete(ctx context.Context, id string) error
	Save(ctx context.Context, task *model.ScheduledTask) error
}

type serviceImpl struct {
	repo      repository.ScheduleRepo
	graphRepo repository.GraphWorkflowRepo
}

func NewService() (Service, error) {
	repo, err := repository.NewScheduleRepo()
	if err != nil {
		return nil, fmt.Errorf("init schedule repo failed: %w", err)
	}
	graphRepo, err := repository.NewGraphWorkflowRepo()
	if err != nil {
		return nil, fmt.Errorf("init graph workflow repo failed: %w", err)
	}
	return &serviceImpl{repo: repo, graphRepo: graphRepo}, nil
}

func (s *serviceImpl) Create(ctx context.Context, req *model.CreateScheduleRequest) (*model.ScheduledTask, error) {
	now := time.Now()
	enabled := true
	if req.Enabled != nil {
		enabled = *req.Enabled
	}
	task := &model.ScheduledTask{
		ID:              model.NewScheduleID(),
		Name:            req.Name,
		Enabled:         enabled,
		CronExpr:        req.CronExpr,
		TargetType:      model.NormalizeScheduleTargetType(req.TargetType),
		TemplateID:      req.TemplateID,
		GraphWorkflowID: req.GraphWorkflowID,
		LoopConfig:      req.LoopConfig,
		WorkspaceID:     req.WorkspaceID,
		Workdir:         req.Workdir,
		MaxConcurrent:   req.MaxConcurrent,
		Timeout:         req.Timeout,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := s.normalizeTaskForTarget(ctx, task); err != nil {
		return nil, err
	}
	// Mirror Update / Toggle: NextRunAt is meaningful only while Enabled.
	// Leaving it populated on a disabled task produces a contradictory
	// "disabled but fires at X" state that UIs and any NextRunAt-based
	// liveness check would misread.
	if enabled {
		task.NextRunAt = NextCronTime(req.CronExpr, now)
	}
	if task.MaxConcurrent <= 0 {
		task.MaxConcurrent = 1
	}
	if err := s.repo.Save(ctx, task); err != nil {
		return nil, err
	}
	return task, nil
}

func (s *serviceImpl) Get(ctx context.Context, id string) (*model.ScheduledTask, error) {
	return s.repo.Get(ctx, id)
}

func (s *serviceImpl) List(ctx context.Context) ([]*model.ScheduledTask, error) {
	return s.repo.List(ctx)
}

func (s *serviceImpl) ListByWorkspace(ctx context.Context, wsID string) ([]*model.ScheduledTask, error) {
	all, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	var filtered []*model.ScheduledTask
	for _, t := range all {
		if t.WorkspaceID == wsID {
			filtered = append(filtered, t)
		}
	}
	return filtered, nil
}

func (s *serviceImpl) Update(ctx context.Context, id string, req *model.UpdateScheduleRequest) (*model.ScheduledTask, error) {
	task, err := s.repo.Get(ctx, id)
	if err != nil {
		return nil, err
	}
	if task == nil {
		return nil, fmt.Errorf("schedule not found: %s", id)
	}

	if req.Name != nil {
		task.Name = *req.Name
	}
	if req.CronExpr != nil {
		task.CronExpr = *req.CronExpr
	}
	if req.Enabled != nil {
		task.Enabled = *req.Enabled
	}
	if req.TargetType != nil {
		task.TargetType = model.NormalizeScheduleTargetType(*req.TargetType)
	}
	if req.TemplateID != nil {
		task.TemplateID = *req.TemplateID
	}
	if req.GraphWorkflowID != nil {
		task.GraphWorkflowID = *req.GraphWorkflowID
	}
	if req.LoopConfig != nil {
		task.LoopConfig = *req.LoopConfig
	}
	if req.MaxConcurrent != nil {
		task.MaxConcurrent = *req.MaxConcurrent
	}
	if req.Timeout != nil {
		task.Timeout = *req.Timeout
	}
	if err := s.normalizeTaskForTarget(ctx, task); err != nil {
		return nil, err
	}
	task.UpdatedAt = time.Now()

	// Recompute NextRunAt based on current enabled/cron state
	if task.Enabled {
		task.NextRunAt = NextCronTime(task.CronExpr, time.Now())
	} else {
		task.NextRunAt = nil
	}

	if err := s.repo.Save(ctx, task); err != nil {
		return nil, err
	}
	return task, nil
}

func (s *serviceImpl) Delete(ctx context.Context, id string) error {
	return s.repo.Delete(ctx, id)
}

func (s *serviceImpl) Save(ctx context.Context, task *model.ScheduledTask) error {
	if task != nil {
		task.TargetType = model.NormalizeScheduleTargetType(task.TargetType)
	}
	return s.repo.Save(ctx, task)
}

func (s *serviceImpl) normalizeTaskForTarget(ctx context.Context, task *model.ScheduledTask) error {
	if task == nil {
		return fmt.Errorf("schedule task is required")
	}
	task.TargetType = model.NormalizeScheduleTargetType(task.TargetType)
	switch task.TargetType {
	case model.ScheduleTargetTypeLoop:
		if err := model.NormalizeAndValidateLoopConfig(&task.LoopConfig, model.FlowDefaults{}); err != nil {
			return fmt.Errorf("%w: %s", ErrInvalidLoopConfig, err.Error())
		}
		task.GraphWorkflowID = ""
	case model.ScheduleTargetTypeGraphWorkflow:
		if task.GraphWorkflowID == "" {
			return fmt.Errorf("%w for schedule target type %q", ErrGraphWorkflowRequired, task.TargetType)
		}
		if err := s.ensureGraphWorkflowExists(ctx, task.GraphWorkflowID); err != nil {
			return err
		}
		task.TemplateID = ""
		task.LoopConfig = model.LoopConfig{}
	default:
		return fmt.Errorf("%w: %q", ErrInvalidTargetType, task.TargetType)
	}
	return nil
}

func (s *serviceImpl) ensureGraphWorkflowExists(ctx context.Context, workflowID string) error {
	if s.graphRepo == nil {
		return fmt.Errorf("graph workflow repo is not initialized")
	}
	wf, err := s.graphRepo.Get(ctx, workflowID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("%w: %s", ErrGraphWorkflowNotFound, workflowID)
		}
		return fmt.Errorf("get graph workflow %s failed: %w", workflowID, err)
	}
	if wf == nil || wf.Deleted {
		return fmt.Errorf("%w: %s", ErrGraphWorkflowNotFound, workflowID)
	}
	return nil
}
