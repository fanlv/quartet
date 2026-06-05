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

type IMMessageRepo interface {
	Append(ctx context.Context, msg *model.IMMessage) error
}

type imMessageRepo struct {
	sandbox fileserver.FileManager
}

func NewIMMessageRepo() IMMessageRepo {
	sb := fileserver.GetFileManager()
	return &imMessageRepo{sandbox: sb}
}

func (r *imMessageRepo) Append(_ context.Context, msg *model.IMMessage) error {
	if msg == nil {
		return fmt.Errorf("im message is nil")
	}
	dir := path.IMMessageDir(msg.ChatID)
	if err := r.sandbox.MkDir(&fsmodel.MkDirRequest{Path: dir}); err != nil {
		return fmt.Errorf("create im message dir failed: %w", err)
	}

	data, err := json.Marshal(msg)
	if err != nil {
		return fmt.Errorf("marshal im message failed: %w", err)
	}

	filePath := path.IMMessageFilePath(msg.ChatID, time.Now())
	if err := r.sandbox.FileAppend(&fsmodel.FileAppendRequest{
		File:    filePath,
		Content: string(data) + "\n",
	}); err != nil {
		return fmt.Errorf("write im message failed: %w", err)
	}
	return nil
}
