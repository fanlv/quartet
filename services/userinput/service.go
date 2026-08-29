package userinput

import (
	"context"

	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/types/model"
)

// Service records normalized user inputs received from Web and IM adapters.
type Service interface {
	Append(ctx context.Context, input *model.UserInput) error
}

type serviceImpl struct {
	repo repository.UserInputRepo
}

func NewService() Service {
	return &serviceImpl{repo: repository.NewUserInputRepo()}
}

func (s *serviceImpl) Append(ctx context.Context, input *model.UserInput) error {
	return s.repo.Append(ctx, input)
}
