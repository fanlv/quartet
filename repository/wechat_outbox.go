package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"

	"github.com/fanlv/quartet/pkg/fileserver"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/types/model"
	"github.com/fanlv/quartet/types/path"
)

type WeChatOutboxRepo interface {
	Save(ctx context.Context, task *model.WeChatOutboxTask) error
	Get(ctx context.Context, id string) (*model.WeChatOutboxTask, error)
	List(ctx context.Context) ([]*model.WeChatOutboxTask, error)
}

type fileWeChatOutboxRepo struct {
	dir     string
	sandbox fileserver.FileManager
	locks   lockShard
}

func NewWeChatOutboxRepo() (WeChatOutboxRepo, error) {
	return &fileWeChatOutboxRepo{
		dir:     path.WeChatOutboxDir(),
		sandbox: fileserver.GetFileManager(),
	}, nil
}

func (r *fileWeChatOutboxRepo) Save(_ context.Context, task *model.WeChatOutboxTask) error {
	if task == nil {
		return os.ErrInvalid
	}
	if err := validateID(task.ID); err != nil {
		return err
	}

	mu := r.locks.lockFor(task.ID)
	mu.Lock()
	defer mu.Unlock()

	data, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal wechat outbox task %s failed: %w", task.ID, err)
	}
	if err := AtomicWriteFile(path.WeChatOutboxTaskFile(task.ID), append(data, '\n'), 0o600); err != nil {
		return fmt.Errorf("write wechat outbox task %s failed: %w", task.ID, err)
	}
	return nil
}

func (r *fileWeChatOutboxRepo) Get(_ context.Context, id string) (*model.WeChatOutboxTask, error) {
	if err := validateID(id); err != nil {
		return nil, err
	}
	result, err := r.sandbox.FileRead(&fsmodel.FileReadRequest{File: path.WeChatOutboxTaskFile(id)})
	if err != nil {
		return nil, err
	}
	var task model.WeChatOutboxTask
	if err := json.Unmarshal([]byte(result.Content), &task); err != nil {
		return nil, fmt.Errorf("parse wechat outbox task %s failed: %w", id, err)
	}
	return &task, nil
}

func (r *fileWeChatOutboxRepo) List(_ context.Context) ([]*model.WeChatOutboxTask, error) {
	result, err := r.sandbox.FileList(&fsmodel.FileListRequest{Path: r.dir})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("list wechat outbox failed: %w", err)
	}

	tasks := make([]*model.WeChatOutboxTask, 0, len(result.Files))
	for _, file := range result.Files {
		if file.IsDir || filepath.Ext(file.Name) != ".json" {
			continue
		}
		id := file.Name[:len(file.Name)-len(filepath.Ext(file.Name))]
		task, err := r.Get(context.Background(), id)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, task)
	}
	sort.Slice(tasks, func(i, j int) bool {
		if tasks[i].CreatedAt.Equal(tasks[j].CreatedAt) {
			return tasks[i].ID < tasks[j].ID
		}
		return tasks[i].CreatedAt.Before(tasks[j].CreatedAt)
	})
	return tasks, nil
}
