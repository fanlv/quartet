package repository

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"

	"github.com/fanlv/quartet/pkg/fileserver"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
	"github.com/fanlv/quartet/types/path"
)

type ScheduleRepo interface {
	Save(ctx context.Context, task *model.ScheduledTask) error
	Get(ctx context.Context, id string) (*model.ScheduledTask, error)
	List(ctx context.Context) ([]*model.ScheduledTask, error)
	Delete(ctx context.Context, id string) error
}

type fileScheduleRepo struct {
	dir     string
	sandbox fileserver.FileManager
	locks   lockShard
}

func NewScheduleRepo() (ScheduleRepo, error) {
	dir, err := path.SchedulesDir()
	if err != nil {
		return nil, err
	}
	sb := fileserver.GetFileManager()
	return &fileScheduleRepo{dir: dir, sandbox: sb}, nil
}

func validateScheduleID(id string) error {
	return validateID(id)
}

func (r *fileScheduleRepo) Save(_ context.Context, task *model.ScheduledTask) error {
	if task == nil {
		return os.ErrInvalid
	}
	if err := validateScheduleID(task.ID); err != nil {
		return err
	}
	mu := r.locks.lockFor(task.ID)
	mu.Lock()
	defer mu.Unlock()
	data, err := json.MarshalIndent(task, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWriteFile(filepath.Join(r.dir, task.ID+".json"), data, 0644)
}

func (r *fileScheduleRepo) Get(_ context.Context, id string) (*model.ScheduledTask, error) {
	if err := validateScheduleID(id); err != nil {
		return nil, err
	}
	result, err := r.sandbox.FileRead(&fsmodel.FileReadRequest{
		File: filepath.Join(r.dir, id+".json"),
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var task model.ScheduledTask
	if err := json.Unmarshal([]byte(result.Content), &task); err != nil {
		return nil, err
	}
	return &task, nil
}

func (r *fileScheduleRepo) List(ctx context.Context) ([]*model.ScheduledTask, error) {
	listResult, err := r.sandbox.FileList(&fsmodel.FileListRequest{Path: r.dir})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var tasks []*model.ScheduledTask
	for _, f := range listResult.Files {
		if f.IsDir || filepath.Ext(f.Name) != ".json" {
			continue
		}
		filePath := filepath.Join(r.dir, f.Name)
		readResult, err := r.sandbox.FileRead(&fsmodel.FileReadRequest{
			File: filePath,
		})
		if err != nil {
			logger.Warnf(ctx, "[scheduleRepo] skip unreadable file %s: %v", filePath, err)
			continue
		}
		var task model.ScheduledTask
		if err := json.Unmarshal([]byte(readResult.Content), &task); err != nil {
			logger.Warnf(ctx, "[scheduleRepo] skip malformed JSON %s: %v", filePath, err)
			continue
		}
		tasks = append(tasks, &task)
	}

	sort.Slice(tasks, func(i, j int) bool {
		return tasks[i].CreatedAt.After(tasks[j].CreatedAt)
	})

	return tasks, nil
}

func (r *fileScheduleRepo) Delete(_ context.Context, id string) error {
	if err := validateScheduleID(id); err != nil {
		return err
	}
	mu := r.locks.lockFor(id)
	mu.Lock()
	defer mu.Unlock()
	return r.sandbox.FileDelete(&fsmodel.FileDeleteRequest{
		Path: filepath.Join(r.dir, id+".json"),
	})
}
