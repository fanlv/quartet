package script

import (
	"context"
	"fmt"
	"time"

	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/types/model"
)

type Service interface {
	Save(ctx context.Context, req *model.SaveScriptRequest) (*model.Script, error)
	Get(ctx context.Context, id string) (*model.Script, error)
	List(ctx context.Context) ([]*model.Script, error)
	Delete(ctx context.Context, id string) error
}

type serviceImpl struct {
	repo repository.ScriptRepo
}

func NewService() (Service, error) {
	repo, err := repository.NewScriptRepo()
	if err != nil {
		return nil, fmt.Errorf("init script repo failed: %w", err)
	}
	return &serviceImpl{repo: repo}, nil
}

func (s *serviceImpl) Save(ctx context.Context, req *model.SaveScriptRequest) (*model.Script, error) {
	now := time.Now()
	script := &model.Script{
		ID:          req.ID,
		Name:        req.Name,
		Content:     req.Content,
		Description: req.Description,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	if script.ID == "" {
		script.ID = model.NewScriptID()
	} else {
		// Update: preserve original CreatedAt
		existing, err := s.repo.Get(ctx, script.ID)
		if err == nil && existing != nil {
			script.CreatedAt = existing.CreatedAt
		}
	}

	if err := s.repo.Save(ctx, script); err != nil {
		return nil, err
	}
	return script, nil
}

func (s *serviceImpl) Get(ctx context.Context, id string) (*model.Script, error) {
	return s.repo.Get(ctx, id)
}

func (s *serviceImpl) List(ctx context.Context) ([]*model.Script, error) {
	return s.repo.List(ctx)
}

func (s *serviceImpl) Delete(ctx context.Context, id string) error {
	return s.repo.Delete(ctx, id)
}
