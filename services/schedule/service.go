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
		GraphWorkflowID: req.GraphWorkflowID,
		WorkspaceID:     req.WorkspaceID,
		Workdir:         req.Workdir,
		MaxConcurrent:   req.MaxConcurrent,
		Timeout:         req.Timeout,
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := s.validateGraphWorkflow(ctx, task); err != nil {
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
	if req.GraphWorkflowID != nil {
		task.GraphWorkflowID = *req.GraphWorkflowID
	}
	if req.WorkspaceID != nil {
		task.WorkspaceID = *req.WorkspaceID
	}
	if req.Workdir != nil {
		task.Workdir = *req.Workdir
	}
	if req.MaxConcurrent != nil {
		task.MaxConcurrent = *req.MaxConcurrent
	}
	if req.Timeout != nil {
		task.Timeout = *req.Timeout
	}
	if err := s.validateGraphWorkflow(ctx, task); err != nil {
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
	return s.repo.Save(ctx, task)
}

func (s *serviceImpl) validateGraphWorkflow(ctx context.Context, task *model.ScheduledTask) error {
	if task == nil {
		return fmt.Errorf("schedule task is required")
	}
	if task.GraphWorkflowID == "" {
		return ErrGraphWorkflowRequired
	}
	return s.ensureGraphWorkflowExists(ctx, task.GraphWorkflowID)
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
