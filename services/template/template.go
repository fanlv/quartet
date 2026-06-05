package template

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
	ErrTemplateReferenced = errors.New("template is referenced by scheduled task")
	ErrTemplateNotFound   = errors.New("template not found")
)

type Service interface {
	Save(ctx context.Context, req *model.SaveTemplateRequest) (*model.LoopTemplate, error)
	Get(ctx context.Context, id string) (*model.LoopTemplate, error)
	Update(ctx context.Context, id string, req *model.UpdateTemplateRequest) (*model.LoopTemplate, error)
	List(ctx context.Context) ([]*model.LoopTemplate, error)
	Delete(ctx context.Context, id string) error
}

type serviceImpl struct {
	repo         repository.TemplateRepo
	scheduleRepo repository.ScheduleRepo
}

func NewService() (Service, error) {
	repo, err := repository.NewTemplateRepo()
	if err != nil {
		return nil, fmt.Errorf("init template repo failed: %w", err)
	}
	scheduleRepo, err := repository.NewScheduleRepo()
	if err != nil {
		return nil, fmt.Errorf("init schedule repo failed: %w", err)
	}
	return &serviceImpl{repo: repo, scheduleRepo: scheduleRepo}, nil
}

func (s *serviceImpl) Save(ctx context.Context, req *model.SaveTemplateRequest) (*model.LoopTemplate, error) {
	tmpl := &model.LoopTemplate{
		ID:        req.ID,
		Name:      req.Name,
		Config:    req.Config,
		CreatedAt: time.Now(),
	}
	if tmpl.ID == "" {
		tmpl.ID = model.NewTemplateID()
	}
	if err := s.repo.Save(ctx, tmpl); err != nil {
		return nil, err
	}
	return tmpl, nil
}

func (s *serviceImpl) Get(ctx context.Context, id string) (*model.LoopTemplate, error) {
	tmpl, err := s.repo.Get(ctx, id)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrTemplateNotFound, id)
		}
		return nil, err
	}
	return tmpl, nil
}

func (s *serviceImpl) Update(ctx context.Context, id string, req *model.UpdateTemplateRequest) (*model.LoopTemplate, error) {
	existing, err := s.repo.Get(ctx, id)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: %s", ErrTemplateNotFound, id)
		}
		return nil, err
	}
	existing.Name = req.Name
	existing.Config = req.Config
	existing.UpdatedAt = time.Now()
	if err := s.repo.Update(ctx, id, existing); err != nil {
		return nil, err
	}
	return existing, nil
}

func (s *serviceImpl) List(ctx context.Context) ([]*model.LoopTemplate, error) {
	templates, err := s.repo.List(ctx)
	if err != nil {
		return nil, err
	}
	tasks, err := s.scheduleRepo.List(ctx)
	if err != nil {
		return nil, fmt.Errorf("list scheduled tasks failed: %w", err)
	}
	counts := make(map[string]int, len(tasks))
	for _, task := range tasks {
		if task.TemplateID == "" {
			continue
		}
		counts[task.TemplateID]++
	}
	for _, tmpl := range templates {
		tmpl.ScheduleCount = counts[tmpl.ID]
	}
	return templates, nil
}

func (s *serviceImpl) Delete(ctx context.Context, id string) error {
	tasks, err := s.scheduleRepo.List(ctx)
	if err != nil {
		return fmt.Errorf("list scheduled tasks failed: %w", err)
	}
	for _, task := range tasks {
		if task.TemplateID == id {
			if task.Name != "" {
				return fmt.Errorf("%w: %s (%s)", ErrTemplateReferenced, task.Name, task.ID)
			}
			return fmt.Errorf("%w: %s", ErrTemplateReferenced, task.ID)
		}
	}
	return s.repo.Delete(ctx, id)
}
