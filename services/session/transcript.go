package session

import (
	"context"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/repository"
)

// TranscriptStore exposes session transcript operations to application
// adapters while keeping repository construction inside the service layer.
type TranscriptStore interface {
	Load(ctx context.Context, workspaceID, jobID, sessionID string) ([]*schema.Message, error)
	Append(ctx context.Context, workspaceID, jobID, sessionID string, messages []*schema.Message) error
}

type transcriptStore struct{}

func NewTranscriptStore() TranscriptStore {
	return transcriptStore{}
}

func (transcriptStore) Load(
	ctx context.Context,
	workspaceID, jobID, sessionID string,
) ([]*schema.Message, error) {
	repo, err := repository.NewChatContextRepo(workspaceID, jobID, sessionID)
	if err != nil {
		return nil, err
	}
	return repo.LoadAllMessages(ctx)
}

func (transcriptStore) Append(
	ctx context.Context,
	workspaceID, jobID, sessionID string,
	messages []*schema.Message,
) error {
	repo, err := repository.NewChatContextRepo(workspaceID, jobID, sessionID)
	if err != nil {
		return err
	}
	return repo.AppendMessages(ctx, messages)
}
