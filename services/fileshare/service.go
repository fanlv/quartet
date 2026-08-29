package fileshare

import (
	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/types/model"
)

// Service exposes file-share persistence without leaking repository types into
// the HTTP layer. Path authorization remains with the caller because the same
// policy is shared by all file endpoints.
type Service interface {
	Create(filePath string) (*model.FileShare, error)
	Get(token string) (*model.FileShare, bool)
	GetByPath(filePath string) (*model.FileShare, bool)
	Delete(token string) error
}

type serviceImpl struct {
	repo repository.FileShareRepo
}

func NewService() Service {
	return &serviceImpl{repo: repository.GetFileShareRepo()}
}

func (s *serviceImpl) Create(filePath string) (*model.FileShare, error) {
	return s.repo.Create(filePath)
}

func (s *serviceImpl) Get(token string) (*model.FileShare, bool) {
	return s.repo.Get(token)
}

func (s *serviceImpl) GetByPath(filePath string) (*model.FileShare, bool) {
	return s.repo.GetByPath(filePath)
}

func (s *serviceImpl) Delete(token string) error {
	return s.repo.Delete(token)
}
