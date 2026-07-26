package repository

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"time"

	"github.com/fanlv/quartet/pkg/fileserver"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
	"github.com/fanlv/quartet/types/path"
)

var ErrGraphWorkflowVersionConflict = errors.New("graph workflow version conflict")

// GraphWorkflowRepo persists editable/saved Graph workflow configs, one JSON
// file per workflow keyed by ID.
type GraphWorkflowRepo interface {
	Save(ctx context.Context, wf *model.GraphWorkflow) error
	Get(ctx context.Context, id string) (*model.GraphWorkflow, error)
	List(ctx context.Context) ([]*model.GraphWorkflow, []model.GraphWorkflowWarning, error)
	Update(ctx context.Context, id string, wf *model.GraphWorkflow) error
	UpdateIfUnchanged(ctx context.Context, id string, wf *model.GraphWorkflow, expectedUpdatedAt *time.Time) error
	Delete(ctx context.Context, id string, expectedUpdatedAt *time.Time) error
}

type fileGraphWorkflowRepo struct {
	dir     string
	sandbox fileserver.FileManager
	locks   lockShard
}

func NewGraphWorkflowRepo() (GraphWorkflowRepo, error) {
	dir, err := path.GraphWorkflowsDir()
	if err != nil {
		return nil, err
	}
	sb := fileserver.GetFileManager()
	return &fileGraphWorkflowRepo{dir: dir, sandbox: sb}, nil
}

func validateGraphWorkflowID(id string) error {
	return validateID(id)
}

// normalizeGraphWorkflowType fills an empty Type with the default "user" so any
// read path returns a non-empty library tag. Legacy workflows persisted before
// the Type field existed have an empty type on disk; rather than run a one-off
// migration, every deserialization point normalizes at read time. New writes
// (Create/Update in the service) persist the canonical value, so stale files
// are upgraded the first time they are saved.
func normalizeGraphWorkflowType(wf *model.GraphWorkflow) {
	if wf != nil && wf.Type == "" {
		wf.Type = model.GraphWorkflowTypeUser
	}
}

func (r *fileGraphWorkflowRepo) Save(_ context.Context, wf *model.GraphWorkflow) error {
	if wf == nil {
		return os.ErrInvalid
	}
	if err := validateGraphWorkflowID(wf.ID); err != nil {
		return err
	}
	mu := r.locks.lockFor(wf.ID)
	mu.Lock()
	defer mu.Unlock()
	data, err := json.MarshalIndent(wf, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWriteFile(filepath.Join(r.dir, wf.ID+".json"), data, 0644)
}

func (r *fileGraphWorkflowRepo) Get(_ context.Context, id string) (*model.GraphWorkflow, error) {
	if err := validateGraphWorkflowID(id); err != nil {
		return nil, err
	}
	readResult, err := r.sandbox.FileRead(&fsmodel.FileReadRequest{
		File: filepath.Join(r.dir, id+".json"),
	})
	if err != nil {
		return nil, err
	}
	var wf model.GraphWorkflow
	if err := json.Unmarshal([]byte(readResult.Content), &wf); err != nil {
		return nil, err
	}
	normalizeGraphWorkflowType(&wf)
	return &wf, nil
}

func (r *fileGraphWorkflowRepo) List(ctx context.Context) ([]*model.GraphWorkflow, []model.GraphWorkflowWarning, error) {
	result, err := r.sandbox.FileList(&fsmodel.FileListRequest{Path: r.dir})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil, nil
		}
		return nil, nil, err
	}

	var workflows []*model.GraphWorkflow
	var warnings []model.GraphWorkflowWarning
	for _, f := range result.Files {
		if f.IsDir || filepath.Ext(f.Name) != ".json" {
			continue
		}
		filePath := filepath.Join(r.dir, f.Name)
		readResult, err := r.sandbox.FileRead(&fsmodel.FileReadRequest{
			File: filePath,
		})
		if err != nil {
			// Corrupt / unreadable entries should not silently disappear. Log for
			// operators AND surface them to the caller so the UI can show the
			// offending file and the raw error instead of the workflow just
			// vanishing from the list (per "errors are shown to the user in full").
			logger.Warnf(ctx, "[graphWorkflowRepo] skip unreadable file %s: %v", filePath, err)
			warnings = append(warnings, model.GraphWorkflowWarning{File: filePath, Error: err.Error()})
			continue
		}
		var wf model.GraphWorkflow
		if err := json.Unmarshal([]byte(readResult.Content), &wf); err != nil {
			logger.Warnf(ctx, "[graphWorkflowRepo] skip malformed JSON %s: %v", filePath, err)
			warnings = append(warnings, model.GraphWorkflowWarning{File: filePath, Error: err.Error()})
			continue
		}
		// Soft-deleted workflows are kept on disk so historical runs can still
		// resolve the workflow name/baseline, but they are excluded from list.
		if wf.Deleted {
			continue
		}
		normalizeGraphWorkflowType(&wf)
		workflows = append(workflows, &wf)
	}

	sort.Slice(workflows, func(i, j int) bool {
		return workflows[i].CreatedAt.After(workflows[j].CreatedAt)
	})

	return workflows, warnings, nil
}

func (r *fileGraphWorkflowRepo) Update(_ context.Context, id string, wf *model.GraphWorkflow) error {
	if wf == nil {
		return os.ErrInvalid
	}
	if err := validateGraphWorkflowID(id); err != nil {
		return err
	}
	mu := r.locks.lockFor(id)
	mu.Lock()
	defer mu.Unlock()
	data, err := json.MarshalIndent(wf, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWriteFile(filepath.Join(r.dir, id+".json"), data, 0644)
}

func (r *fileGraphWorkflowRepo) UpdateIfUnchanged(_ context.Context, id string, wf *model.GraphWorkflow, expectedUpdatedAt *time.Time) error {
	if wf == nil {
		return os.ErrInvalid
	}
	if err := validateGraphWorkflowID(id); err != nil {
		return err
	}
	mu := r.locks.lockFor(id)
	mu.Lock()
	defer mu.Unlock()

	fp := filepath.Join(r.dir, id+".json")
	current, err := r.readWorkflowFile(fp)
	if err != nil {
		return err
	}
	if current.Deleted {
		return os.ErrNotExist
	}
	if expectedUpdatedAt != nil && !current.UpdatedAt.Equal(*expectedUpdatedAt) {
		return ErrGraphWorkflowVersionConflict
	}
	data, err := json.MarshalIndent(wf, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWriteFile(fp, data, 0644)
}

// Delete soft-deletes the workflow: the file stays on disk with Deleted=true so
// that historical GraphRuns referencing it keep resolving, while List filters
// it out. Returns os.ErrNotExist when the workflow does not exist.
func (r *fileGraphWorkflowRepo) Delete(ctx context.Context, id string, expectedUpdatedAt *time.Time) error {
	if err := validateGraphWorkflowID(id); err != nil {
		return err
	}
	mu := r.locks.lockFor(id)
	mu.Lock()
	defer mu.Unlock()

	fp := filepath.Join(r.dir, id+".json")
	wf, err := r.readWorkflowFile(fp)
	if err != nil {
		return err
	}
	if wf.Deleted {
		if expectedUpdatedAt != nil && !wf.UpdatedAt.Equal(*expectedUpdatedAt) {
			return ErrGraphWorkflowVersionConflict
		}
		return os.ErrNotExist
	}
	if expectedUpdatedAt != nil && !wf.UpdatedAt.Equal(*expectedUpdatedAt) {
		return ErrGraphWorkflowVersionConflict
	}
	wf.Deleted = true
	data, err := json.MarshalIndent(wf, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWriteFile(fp, data, 0644)
}

func (r *fileGraphWorkflowRepo) readWorkflowFile(fp string) (*model.GraphWorkflow, error) {
	readResult, err := r.sandbox.FileRead(&fsmodel.FileReadRequest{File: fp})
	if err != nil {
		return nil, err
	}
	var wf model.GraphWorkflow
	if err := json.Unmarshal([]byte(readResult.Content), &wf); err != nil {
		return nil, err
	}
	normalizeGraphWorkflowType(&wf)
	return &wf, nil
}
