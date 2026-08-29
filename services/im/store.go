package im

import (
	"context"

	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/types/model"
)

// Store is the persistence-facing capability used by the IM gateway. Keeping
// it in the service layer prevents transport adapters from depending directly
// on repository implementations.
type Store interface {
	GetJobMapping(platform, chatID string) (*model.IMJobMapping, error)
	SaveJobMapping(mapping *model.IMJobMapping) error
	AppendMessage(ctx context.Context, message *model.IMMessage) error
}

type storeImpl struct {
	mappings repository.IMJobMappingRepo
	messages repository.IMMessageRepo
}

func NewStore() (Store, error) {
	mappings, err := repository.NewIMJobMappingRepo()
	if err != nil {
		return nil, err
	}
	return &storeImpl{
		mappings: mappings,
		messages: repository.NewIMMessageRepo(),
	}, nil
}

func (s *storeImpl) GetJobMapping(platform, chatID string) (*model.IMJobMapping, error) {
	return s.mappings.Get(platform, chatID)
}

func (s *storeImpl) SaveJobMapping(mapping *model.IMJobMapping) error {
	return s.mappings.Save(mapping)
}

func (s *storeImpl) AppendMessage(ctx context.Context, message *model.IMMessage) error {
	return s.messages.Append(ctx, message)
}
