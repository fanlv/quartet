package repository

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/fanlv/quartet/pkg/fileserver"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/types/model"
	"github.com/fanlv/quartet/types/path"
)

// UserInputRepo appends "真实用户输入" entries — the narrow-spec stream that
// covers IM admin private chat + Web, flat-dated under
// {LOCAL_MEMORY}/quartet/data/user-input/YYYY-MM-DD.jsonl. Coexists with IMMessageRepo:
// the two feeds have different intakes and different consumers (see
// docs/feature-2026-05-03-user-input-logging.md §4).
type UserInputRepo interface {
	Append(ctx context.Context, input *model.UserInput) error
}

type userInputRepo struct {
	sandbox fileserver.FileManager
}

func NewUserInputRepo() UserInputRepo {
	sb := fileserver.GetFileManager()
	return &userInputRepo{sandbox: sb}
}

func (r *userInputRepo) Append(_ context.Context, input *model.UserInput) error {
	if input == nil {
		return fmt.Errorf("user input is nil")
	}
	dir := path.UserInputDir()
	if err := r.sandbox.MkDir(&fsmodel.MkDirRequest{Path: dir}); err != nil {
		return fmt.Errorf("create user input dir failed: %w", err)
	}

	data, err := json.Marshal(input)
	if err != nil {
		return fmt.Errorf("marshal user input failed: %w", err)
	}

	ts := time.UnixMilli(input.ReceivedAt)
	if input.ReceivedAt == 0 {
		ts = time.Now()
	}
	filePath := path.UserInputFilePath(ts)
	if err := r.sandbox.FileAppend(&fsmodel.FileAppendRequest{
		File:    filePath,
		Content: string(data) + "\n",
	}); err != nil {
		return fmt.Errorf("write user input failed: %w", err)
	}
	return nil
}
