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
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
	"github.com/fanlv/quartet/types/path"
)

// GraphRunRepo owns GraphRun runtime artifacts. A run is stored as a directory
// under AgentDir so deleting one run can remove all of its snapshots, state,
// lineage and event logs without touching workflow configs or other runs.
type GraphRunRepo interface {
	SaveRun(ctx context.Context, run *model.GraphRun) error
	GetRun(ctx context.Context, runID string) (*model.GraphRun, error)
	ListRuns(ctx context.Context) ([]*model.GraphRun, error)

	SaveInstances(ctx context.Context, runID string, instances map[string]model.GraphInstanceState) error
	GetInstances(ctx context.Context, runID string) (map[string]model.GraphInstanceState, error)

	SaveEdges(ctx context.Context, runID string, edges map[string]model.GraphEdgeState) error
	GetEdges(ctx context.Context, runID string) (map[string]model.GraphEdgeState, error)

	SaveVariables(ctx context.Context, runID string, variables map[string]map[string]string) error
	GetVariables(ctx context.Context, runID string) (map[string]map[string]string, error)

	SaveSessionLineage(ctx context.Context, runID string, lineage map[string]model.GraphSessionLineage) error
	GetSessionLineage(ctx context.Context, runID string) (map[string]model.GraphSessionLineage, error)

	SaveProgress(ctx context.Context, runID string, progress *model.GraphProgress) error
	GetProgress(ctx context.Context, runID string) (*model.GraphProgress, error)

	SaveResume(ctx context.Context, runID string, resume *model.GraphResumeState) error
	GetResume(ctx context.Context, runID string) (*model.GraphResumeState, error)

	AppendEvent(ctx context.Context, runID string, event *model.GraphEvent) error
	ListEvents(ctx context.Context, runID string, startLine int, count *int) ([]model.GraphEvent, error)
	// CountEvents returns the number of persisted event lines without reading
	// their content. Used by GetRunStatus to expose an event count (the SSE
	// resume cursor seed) without serialising the whole event log into the
	// status response. A missing events file yields (0, nil).
	CountEvents(ctx context.Context, runID string) (int, error)

	DeleteRun(ctx context.Context, runID string) error
}

type fileGraphRunRepo struct {
	dir     string
	storage fileserver.Storage
	locks   lockShard
}

func NewGraphRunRepo() (GraphRunRepo, error) {
	dir, err := path.GraphRunsDir()
	if err != nil {
		return nil, err
	}
	st := fileserver.GetStorage()
	if err := st.MkDir(&fsmodel.MkDirRequest{Path: dir}); err != nil {
		return nil, fmt.Errorf("mk graph runs dir failed: %w", err)
	}
	return &fileGraphRunRepo{dir: dir, storage: st}, nil
}

func validateGraphRunID(id string) error {
	return validateID(id)
}

func (r *fileGraphRunRepo) SaveRun(_ context.Context, run *model.GraphRun) error {
	if run == nil {
		return os.ErrInvalid
	}
	if err := validateGraphRunID(run.ID); err != nil {
		return err
	}
	return r.writeJSON(run.ID, graphRunFilePath, run)
}

func (r *fileGraphRunRepo) GetRun(_ context.Context, runID string) (*model.GraphRun, error) {
	if err := validateGraphRunID(runID); err != nil {
		return nil, err
	}
	var run model.GraphRun
	if err := r.readJSON(graphRunFilePath, runID, &run); err != nil {
		return nil, err
	}
	return &run, nil
}

func (r *fileGraphRunRepo) ListRuns(ctx context.Context) ([]*model.GraphRun, error) {
	result, err := r.storage.FileList(&fsmodel.FileListRequest{Path: r.dir})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var runs []*model.GraphRun
	for _, f := range result.Files {
		if !f.IsDir {
			continue
		}
		runID := f.Name
		if err := validateGraphRunID(runID); err != nil {
			logger.Warnf(ctx, "[graphRunRepo] skip invalid run dir %s: %v", f.Path, err)
			continue
		}
		run, err := r.GetRun(ctx, runID)
		if err != nil {
			missing, statErr := r.graphRunMetadataMissing(runID)
			if statErr != nil {
				logger.Warnf(ctx, "[graphRunRepo] skip unreadable run %s: %v (stat run metadata failed: %v)", runID, err, statErr)
				continue
			}
			if missing {
				if cleanupErr := r.cleanupIncompleteRun(ctx, runID); cleanupErr != nil {
					logger.Warnf(ctx, "[graphRunRepo] cleanup incomplete run %s failed: %v", runID, cleanupErr)
				}
				continue
			}
			logger.Warnf(ctx, "[graphRunRepo] skip unreadable run %s: %v", runID, err)
			continue
		}
		runs = append(runs, run)
	}
	sort.Slice(runs, func(i, j int) bool {
		return runs[i].CreatedAt.After(runs[j].CreatedAt)
	})
	return runs, nil
}

func (r *fileGraphRunRepo) SaveInstances(_ context.Context, runID string, instances map[string]model.GraphInstanceState) error {
	if instances == nil {
		instances = map[string]model.GraphInstanceState{}
	}
	return r.writeJSON(runID, graphRunInstancesFilePath, instances)
}

func (r *fileGraphRunRepo) GetInstances(_ context.Context, runID string) (map[string]model.GraphInstanceState, error) {
	instances := map[string]model.GraphInstanceState{}
	err := r.readJSON(graphRunInstancesFilePath, runID, &instances)
	return instances, err
}

func (r *fileGraphRunRepo) SaveEdges(_ context.Context, runID string, edges map[string]model.GraphEdgeState) error {
	if edges == nil {
		edges = map[string]model.GraphEdgeState{}
	}
	return r.writeJSON(runID, graphRunEdgesFilePath, edges)
}

func (r *fileGraphRunRepo) GetEdges(_ context.Context, runID string) (map[string]model.GraphEdgeState, error) {
	edges := map[string]model.GraphEdgeState{}
	err := r.readJSON(graphRunEdgesFilePath, runID, &edges)
	return edges, err
}

func (r *fileGraphRunRepo) SaveVariables(_ context.Context, runID string, variables map[string]map[string]string) error {
	if variables == nil {
		variables = map[string]map[string]string{}
	}
	return r.writeJSON(runID, graphRunVariablesFilePath, variables)
}

func (r *fileGraphRunRepo) GetVariables(_ context.Context, runID string) (map[string]map[string]string, error) {
	variables := map[string]map[string]string{}
	err := r.readJSON(graphRunVariablesFilePath, runID, &variables)
	return variables, err
}

func (r *fileGraphRunRepo) SaveSessionLineage(_ context.Context, runID string, lineage map[string]model.GraphSessionLineage) error {
	if lineage == nil {
		lineage = map[string]model.GraphSessionLineage{}
	}
	return r.writeJSON(runID, graphRunSessionLineageFilePath, lineage)
}

func (r *fileGraphRunRepo) GetSessionLineage(_ context.Context, runID string) (map[string]model.GraphSessionLineage, error) {
	lineage := map[string]model.GraphSessionLineage{}
	err := r.readJSON(graphRunSessionLineageFilePath, runID, &lineage)
	return lineage, err
}

func (r *fileGraphRunRepo) SaveProgress(_ context.Context, runID string, progress *model.GraphProgress) error {
	if progress == nil {
		progress = &model.GraphProgress{}
	}
	return r.writeJSON(runID, graphRunProgressFilePath, progress)
}

func (r *fileGraphRunRepo) GetProgress(_ context.Context, runID string) (*model.GraphProgress, error) {
	progress := &model.GraphProgress{}
	if err := r.readJSON(graphRunProgressFilePath, runID, progress); err != nil {
		return nil, err
	}
	return progress, nil
}

func (r *fileGraphRunRepo) SaveResume(_ context.Context, runID string, resume *model.GraphResumeState) error {
	if resume == nil {
		resume = &model.GraphResumeState{}
	}
	return r.writeJSON(runID, graphRunResumeFilePath, resume)
}

func (r *fileGraphRunRepo) GetResume(_ context.Context, runID string) (*model.GraphResumeState, error) {
	resume := &model.GraphResumeState{}
	if err := r.readJSON(graphRunResumeFilePath, runID, resume); err != nil {
		return nil, err
	}
	return resume, nil
}

func (r *fileGraphRunRepo) AppendEvent(_ context.Context, runID string, event *model.GraphEvent) error {
	if event == nil {
		return os.ErrInvalid
	}
	if err := validateGraphRunID(runID); err != nil {
		return err
	}
	fp, err := graphRunEventsFilePath(runID)
	if err != nil {
		return err
	}
	line, err := json.Marshal(event)
	if err != nil {
		return err
	}
	mu := r.locks.lockFor(runID)
	mu.Lock()
	defer mu.Unlock()
	if err := r.ensureRunDir(runID); err != nil {
		return err
	}
	return r.storage.JSONLAppendLine(&fsmodel.JSONLAppendRequest{
		File:       fp,
		JSONString: []string{string(line)},
	})
}

func (r *fileGraphRunRepo) ListEvents(_ context.Context, runID string, startLine int, count *int) ([]model.GraphEvent, error) {
	if err := validateGraphRunID(runID); err != nil {
		return nil, err
	}
	fp, err := graphRunEventsFilePath(runID)
	if err != nil {
		return nil, err
	}
	result, err := r.storage.JSONLReadLines(&fsmodel.JSONLReadRequest{
		File:      fp,
		StartLine: startLine,
		Count:     count,
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	events := make([]model.GraphEvent, 0, len(result.Lines))
	for i, line := range result.Lines {
		var event model.GraphEvent
		if err := json.Unmarshal([]byte(line), &event); err != nil {
			return nil, fmt.Errorf("unmarshal graph event run=%s line=%d: %w", runID, startLine+i, err)
		}
		events = append(events, event)
	}
	return events, nil
}

func (r *fileGraphRunRepo) CountEvents(_ context.Context, runID string) (int, error) {
	if err := validateGraphRunID(runID); err != nil {
		return 0, err
	}
	fp, err := graphRunEventsFilePath(runID)
	if err != nil {
		return 0, err
	}
	result, err := r.storage.JSONLCountLines(&fsmodel.JSONLCountRequest{File: fp})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, err
	}
	return result.Lines, nil
}

func (r *fileGraphRunRepo) DeleteRun(_ context.Context, runID string) error {
	if err := validateGraphRunID(runID); err != nil {
		return err
	}
	runDir, err := path.GraphRunDir(runID)
	if err != nil {
		return err
	}
	mu := r.locks.lockFor(runID)
	mu.Lock()
	defer mu.Unlock()
	return r.storage.FileDelete(&fsmodel.FileDeleteRequest{Path: runDir})
}

func (r *fileGraphRunRepo) cleanupIncompleteRun(_ context.Context, runID string) error {
	if err := validateGraphRunID(runID); err != nil {
		return err
	}
	runDir, err := path.GraphRunDir(runID)
	if err != nil {
		return err
	}
	mu := r.locks.lockFor(runID)
	mu.Lock()
	defer mu.Unlock()
	missing, err := r.graphRunMetadataMissing(runID)
	if err != nil {
		return err
	}
	if !missing {
		return nil
	}
	return r.storage.FileDelete(&fsmodel.FileDeleteRequest{Path: runDir})
}

func (r *fileGraphRunRepo) graphRunMetadataMissing(runID string) (bool, error) {
	if err := validateGraphRunID(runID); err != nil {
		return false, err
	}
	fp, err := graphRunFilePath(runID)
	if err != nil {
		return false, err
	}
	stat, err := r.storage.FileStat(&fsmodel.FileStatRequest{Path: fp})
	if err != nil {
		return false, err
	}
	return !stat.Exists, nil
}

func (r *fileGraphRunRepo) writeJSON(runID string, pathFn func(string) (string, error), v any) error {
	if err := validateGraphRunID(runID); err != nil {
		return err
	}
	fp, err := pathFn(runID)
	if err != nil {
		return err
	}
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	mu := r.locks.lockFor(runID)
	mu.Lock()
	defer mu.Unlock()
	if err := r.ensureRunDir(runID); err != nil {
		return err
	}
	return AtomicWriteFile(fp, data, 0644)
}

func (r *fileGraphRunRepo) readJSON(pathFn func(string) (string, error), runID string, v any) error {
	if err := validateGraphRunID(runID); err != nil {
		return err
	}
	fp, err := pathFn(runID)
	if err != nil {
		return err
	}
	result, err := r.storage.FileRead(&fsmodel.FileReadRequest{File: fp})
	if err != nil {
		return err
	}
	if err := json.Unmarshal([]byte(result.Content), v); err != nil {
		return err
	}
	return nil
}

func (r *fileGraphRunRepo) ensureRunDir(runID string) error {
	runDir, err := path.GraphRunDir(runID)
	if err != nil {
		return err
	}
	if clean := filepath.Clean(runDir); clean != runDir {
		return fmt.Errorf("invalid graph run dir %q", runDir)
	}
	if err := r.storage.MkDir(&fsmodel.MkDirRequest{Path: runDir}); err != nil {
		return fmt.Errorf("mk graph run dir failed: %w", err)
	}
	return nil
}

func graphRunFilePath(runID string) (string, error) {
	return path.GraphRunFile(runID)
}

func graphRunInstancesFilePath(runID string) (string, error) {
	return path.GraphRunInstancesFile(runID)
}

func graphRunEdgesFilePath(runID string) (string, error) {
	return path.GraphRunEdgesFile(runID)
}

func graphRunVariablesFilePath(runID string) (string, error) {
	return path.GraphRunVariablesFile(runID)
}

func graphRunSessionLineageFilePath(runID string) (string, error) {
	return path.GraphRunSessionLineageFile(runID)
}

func graphRunProgressFilePath(runID string) (string, error) {
	return path.GraphRunProgressFile(runID)
}

func graphRunResumeFilePath(runID string) (string, error) {
	return path.GraphRunResumeFile(runID)
}

func graphRunEventsFilePath(runID string) (string, error) {
	return path.GraphRunEventsFile(runID)
}
