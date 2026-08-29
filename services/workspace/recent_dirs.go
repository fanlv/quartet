package workspace

import (
	"context"

	"github.com/fanlv/quartet/repository"
)

// RecentDirsService owns the recently-used workspace directory list.
type RecentDirsService interface {
	List() ([]string, error)
	Add(ctx context.Context, dir string) error
}

type recentDirsService struct {
	repo repository.RecentDirsRepo
}

func NewRecentDirsService() (RecentDirsService, error) {
	repo, err := repository.NewRecentDirsRepo()
	if err != nil {
		return nil, err
	}
	return &recentDirsService{repo: repo}, nil
}

func (s *recentDirsService) List() ([]string, error) {
	recent, err := s.repo.Get()
	if err != nil {
		return nil, err
	}
	return append([]string(nil), recent.Dirs...), nil
}

func (s *recentDirsService) Add(ctx context.Context, dir string) error {
	return s.repo.Add(ctx, dir)
}
