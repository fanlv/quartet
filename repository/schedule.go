package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/fanlv/quartet/pkg/fileserver"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/types/model"
	"github.com/fanlv/quartet/types/path"
)

type ScheduleRepo interface {
	Save(ctx context.Context, task *model.ScheduledTask) error
	SaveState(ctx context.Context, task *model.ScheduledTask) error
	Get(ctx context.Context, id string) (*model.ScheduledTask, error)
	List(ctx context.Context) ([]*model.ScheduledTask, error)
	Delete(ctx context.Context, id string) error
}

type fileScheduleRepo struct {
	definitionsDir string
	statesDir      string
	sandbox        fileserver.FileManager
	locks          lockShard
}

func NewScheduleRepo() (ScheduleRepo, error) {
	definitionsDir, err := path.SchedulesDir()
	if err != nil {
		return nil, err
	}
	statesDir, err := path.ScheduleStatesDir()
	if err != nil {
		return nil, err
	}
	return &fileScheduleRepo{
		definitionsDir: definitionsDir,
		statesDir:      statesDir,
		sandbox:        fileserver.GetFileManager(),
	}, nil
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

	definitionData, err := json.MarshalIndent(definitionFromTask(task), "", "  ")
	if err != nil {
		return fmt.Errorf("marshal schedule definition %s failed: %w", task.ID, err)
	}
	stateData, err := json.MarshalIndent(stateFromTask(task), "", "  ")
	if err != nil {
		return fmt.Errorf("marshal schedule state %s failed: %w", task.ID, err)
	}
	if err := AtomicWriteFile(r.definitionFile(task.ID), append(definitionData, '\n'), 0o644); err != nil {
		return fmt.Errorf("write schedule definition %s failed: %w", task.ID, err)
	}
	if err := AtomicWriteFile(r.stateFile(task.ID), append(stateData, '\n'), 0o644); err != nil {
		return fmt.Errorf("write schedule state %s failed: %w", task.ID, err)
	}
	return nil
}

func (r *fileScheduleRepo) SaveState(_ context.Context, task *model.ScheduledTask) error {
	if task == nil {
		return os.ErrInvalid
	}
	if err := validateScheduleID(task.ID); err != nil {
		return err
	}
	mu := r.locks.lockFor(task.ID)
	mu.Lock()
	defer mu.Unlock()

	stateData, err := json.MarshalIndent(stateFromTask(task), "", "  ")
	if err != nil {
		return fmt.Errorf("marshal schedule state %s failed: %w", task.ID, err)
	}
	if err := AtomicWriteFile(r.stateFile(task.ID), append(stateData, '\n'), 0o644); err != nil {
		return fmt.Errorf("write schedule state %s failed: %w", task.ID, err)
	}
	return nil
}

func (r *fileScheduleRepo) Get(_ context.Context, id string) (*model.ScheduledTask, error) {
	if err := validateScheduleID(id); err != nil {
		return nil, err
	}
	definition, err := r.readDefinition(id)
	if err != nil || definition == nil {
		return nil, err
	}
	state, err := r.readState(id)
	if err != nil {
		return nil, err
	}
	return mergeSchedule(definition, state), nil
}

func (r *fileScheduleRepo) List(_ context.Context) ([]*model.ScheduledTask, error) {
	listResult, err := r.sandbox.FileList(&fsmodel.FileListRequest{Path: r.definitionsDir})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("list schedule definitions %s failed: %w", r.definitionsDir, err)
	}

	var tasks []*model.ScheduledTask
	for _, f := range listResult.Files {
		if f.IsDir || filepath.Ext(f.Name) != ".json" {
			continue
		}
		id := f.Name[:len(f.Name)-len(filepath.Ext(f.Name))]
		if err := validateScheduleID(id); err != nil {
			return nil, fmt.Errorf("invalid schedule definition filename %s: %w", filepath.Join(r.definitionsDir, f.Name), err)
		}
		definition, err := r.readDefinition(id)
		if err != nil {
			return nil, err
		}
		state, err := r.readState(id)
		if err != nil {
			return nil, err
		}
		tasks = append(tasks, mergeSchedule(definition, state))
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

	var deleteErrors []error
	for _, filePath := range []string{r.definitionFile(id), r.stateFile(id)} {
		if err := r.sandbox.FileDelete(&fsmodel.FileDeleteRequest{Path: filePath}); err != nil && !errors.Is(err, os.ErrNotExist) {
			deleteErrors = append(deleteErrors, fmt.Errorf("delete %s failed: %w", filePath, err))
		}
	}
	return errors.Join(deleteErrors...)
}

func (r *fileScheduleRepo) readDefinition(id string) (*model.ScheduleDefinition, error) {
	filePath := r.definitionFile(id)
	result, err := r.sandbox.FileRead(&fsmodel.FileReadRequest{File: filePath})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read schedule definition %s failed: %w", filePath, err)
	}
	var definition model.ScheduleDefinition
	if err := json.Unmarshal([]byte(result.Content), &definition); err != nil {
		return nil, fmt.Errorf("parse schedule definition %s failed: %w", filePath, err)
	}
	if definition.ID != id {
		return nil, fmt.Errorf("schedule definition ID mismatch in %s: file=%s content=%s", filePath, id, definition.ID)
	}
	return &definition, nil
}

func (r *fileScheduleRepo) readState(id string) (*model.ScheduleState, error) {
	filePath := r.stateFile(id)
	result, err := r.sandbox.FileRead(&fsmodel.FileReadRequest{File: filePath})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("read schedule state %s failed: %w", filePath, err)
	}
	var state model.ScheduleState
	if err := json.Unmarshal([]byte(result.Content), &state); err != nil {
		return nil, fmt.Errorf("parse schedule state %s failed: %w", filePath, err)
	}
	if state.ID != id {
		return nil, fmt.Errorf("schedule state ID mismatch in %s: file=%s content=%s", filePath, id, state.ID)
	}
	return &state, nil
}

func (r *fileScheduleRepo) definitionFile(id string) string {
	return filepath.Join(r.definitionsDir, id+".json")
}

func (r *fileScheduleRepo) stateFile(id string) string {
	return filepath.Join(r.statesDir, id+".json")
}

func definitionFromTask(task *model.ScheduledTask) *model.ScheduleDefinition {
	return &model.ScheduleDefinition{
		ID:              task.ID,
		Name:            task.Name,
		Enabled:         task.Enabled,
		CronExpr:        task.CronExpr,
		CreatedAt:       task.CreatedAt,
		UpdatedAt:       task.UpdatedAt,
		GraphWorkflowID: task.GraphWorkflowID,
		WorkspaceID:     task.WorkspaceID,
		Workdir:         task.Workdir,
		MaxConcurrent:   task.MaxConcurrent,
		Timeout:         task.Timeout,
	}
}

func stateFromTask(task *model.ScheduledTask) *model.ScheduleState {
	updatedAt := task.StateUpdatedAt
	if updatedAt.IsZero() {
		updatedAt = time.Now()
	}
	return &model.ScheduleState{
		ID:               task.ID,
		LastRunAt:        task.LastRunAt,
		LastRunJobID:     task.LastRunJobID,
		LastStatus:       task.LastStatus,
		LastTriggerError: task.LastTriggerError,
		NextRunAt:        task.NextRunAt,
		RunCount:         task.RunCount,
		UpdatedAt:        updatedAt,
	}
}

func mergeSchedule(definition *model.ScheduleDefinition, state *model.ScheduleState) *model.ScheduledTask {
	task := &model.ScheduledTask{
		ID:              definition.ID,
		Name:            definition.Name,
		Enabled:         definition.Enabled,
		CronExpr:        definition.CronExpr,
		CreatedAt:       definition.CreatedAt,
		UpdatedAt:       definition.UpdatedAt,
		GraphWorkflowID: definition.GraphWorkflowID,
		WorkspaceID:     definition.WorkspaceID,
		Workdir:         definition.Workdir,
		MaxConcurrent:   definition.MaxConcurrent,
		Timeout:         definition.Timeout,
	}
	if state != nil {
		task.LastRunAt = state.LastRunAt
		task.LastRunJobID = state.LastRunJobID
		task.LastStatus = state.LastStatus
		task.LastTriggerError = state.LastTriggerError
		task.NextRunAt = state.NextRunAt
		task.RunCount = state.RunCount
		task.StateUpdatedAt = state.UpdatedAt
	}
	return task
}
