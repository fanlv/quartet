package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	"github.com/fanlv/quartet/pkg/fileserver"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/types/model"
	"github.com/fanlv/quartet/types/path"
)

// GraphRunRepo owns GraphRun runtime artifacts. A run is stored as a directory
// under its bound Job directory so deleting a Job can keep its graph runtime
// artifacts colocated with sessions and metadata.
type GraphRunRepo interface {
	RegisterRun(ctx context.Context, run *model.GraphRun) error
	RegisterRunLocation(ctx context.Context, runID, workspaceID, jobID string) error
	SaveRun(ctx context.Context, run *model.GraphRun) error
	GetRun(ctx context.Context, runID string) (*model.GraphRun, error)

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
	storage   fileserver.Storage
	locks     lockShard
	locations sync.Map
}

func NewGraphRunRepo() (GraphRunRepo, error) {
	return &fileGraphRunRepo{storage: fileserver.GetStorage()}, nil
}

func validateGraphRunID(id string) error {
	return validateID(id)
}

type graphRunLocation struct {
	RunID       string `json:"runId"`
	WorkspaceID string `json:"workspaceId"`
	JobID       string `json:"jobId"`
}

func (r *fileGraphRunRepo) RegisterRun(ctx context.Context, run *model.GraphRun) error {
	return r.registerRun(run)
}

func (r *fileGraphRunRepo) RegisterRunLocation(_ context.Context, runID, workspaceID, jobID string) error {
	return r.registerLocation(runID, workspaceID, jobID)
}

func (r *fileGraphRunRepo) SaveRun(_ context.Context, run *model.GraphRun) error {
	if err := r.registerRun(run); err != nil {
		return err
	}
	return r.writeJSONAt(run.ID, path.GraphRunFile(run.WorkspaceID, run.JobID), run)
}

func (r *fileGraphRunRepo) registerRun(run *model.GraphRun) error {
	if run == nil {
		return os.ErrInvalid
	}
	if err := validateGraphRunID(run.ID); err != nil {
		return err
	}
	if strings.TrimSpace(run.WorkspaceID) == "" {
		return fmt.Errorf("graph run workspaceId is required")
	}
	if strings.TrimSpace(run.JobID) == "" {
		return fmt.Errorf("graph run jobId is required")
	}
	return r.registerLocation(run.ID, run.WorkspaceID, run.JobID)
}

func (r *fileGraphRunRepo) registerLocation(runID, workspaceID, jobID string) error {
	if err := validateGraphRunID(runID); err != nil {
		return err
	}
	if strings.TrimSpace(workspaceID) == "" {
		return fmt.Errorf("graph run workspaceId is required")
	}
	if strings.TrimSpace(jobID) == "" {
		return fmt.Errorf("graph run jobId is required")
	}
	r.locations.Store(runID, graphRunLocation{
		RunID:       runID,
		WorkspaceID: workspaceID,
		JobID:       jobID,
	})
	return nil
}

func (r *fileGraphRunRepo) GetRun(_ context.Context, runID string) (*model.GraphRun, error) {
	if err := validateGraphRunID(runID); err != nil {
		return nil, err
	}
	var run model.GraphRun
	if err := r.readJSON(path.GraphRunFile, runID, &run); err != nil {
		return nil, err
	}
	return &run, nil
}

func (r *fileGraphRunRepo) SaveInstances(_ context.Context, runID string, instances map[string]model.GraphInstanceState) error {
	if instances == nil {
		instances = map[string]model.GraphInstanceState{}
	}
	return r.writeJSON(runID, path.GraphRunInstancesFile, instances)
}

func (r *fileGraphRunRepo) GetInstances(_ context.Context, runID string) (map[string]model.GraphInstanceState, error) {
	instances := map[string]model.GraphInstanceState{}
	err := r.readJSON(path.GraphRunInstancesFile, runID, &instances)
	return instances, err
}

func (r *fileGraphRunRepo) SaveEdges(_ context.Context, runID string, edges map[string]model.GraphEdgeState) error {
	if edges == nil {
		edges = map[string]model.GraphEdgeState{}
	}
	return r.writeJSON(runID, path.GraphRunEdgesFile, edges)
}

func (r *fileGraphRunRepo) GetEdges(_ context.Context, runID string) (map[string]model.GraphEdgeState, error) {
	edges := map[string]model.GraphEdgeState{}
	err := r.readJSON(path.GraphRunEdgesFile, runID, &edges)
	return edges, err
}

func (r *fileGraphRunRepo) SaveVariables(_ context.Context, runID string, variables map[string]map[string]string) error {
	if variables == nil {
		variables = map[string]map[string]string{}
	}
	return r.writeJSON(runID, path.GraphRunVariablesFile, variables)
}

func (r *fileGraphRunRepo) GetVariables(_ context.Context, runID string) (map[string]map[string]string, error) {
	variables := map[string]map[string]string{}
	err := r.readJSON(path.GraphRunVariablesFile, runID, &variables)
	return variables, err
}

func (r *fileGraphRunRepo) SaveSessionLineage(_ context.Context, runID string, lineage map[string]model.GraphSessionLineage) error {
	if lineage == nil {
		lineage = map[string]model.GraphSessionLineage{}
	}
	return r.writeJSON(runID, path.GraphRunSessionLineageFile, lineage)
}

func (r *fileGraphRunRepo) GetSessionLineage(_ context.Context, runID string) (map[string]model.GraphSessionLineage, error) {
	lineage := map[string]model.GraphSessionLineage{}
	err := r.readJSON(path.GraphRunSessionLineageFile, runID, &lineage)
	return lineage, err
}

func (r *fileGraphRunRepo) SaveProgress(_ context.Context, runID string, progress *model.GraphProgress) error {
	if progress == nil {
		progress = &model.GraphProgress{}
	}
	return r.writeJSON(runID, path.GraphRunProgressFile, progress)
}

func (r *fileGraphRunRepo) GetProgress(_ context.Context, runID string) (*model.GraphProgress, error) {
	progress := &model.GraphProgress{}
	if err := r.readJSON(path.GraphRunProgressFile, runID, progress); err != nil {
		return nil, err
	}
	return progress, nil
}

func (r *fileGraphRunRepo) SaveResume(_ context.Context, runID string, resume *model.GraphResumeState) error {
	if resume == nil {
		resume = &model.GraphResumeState{}
	}
	return r.writeJSON(runID, path.GraphRunResumeFile, resume)
}

func (r *fileGraphRunRepo) GetResume(_ context.Context, runID string) (*model.GraphResumeState, error) {
	resume := &model.GraphResumeState{}
	if err := r.readJSON(path.GraphRunResumeFile, runID, resume); err != nil {
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
	fp, err := r.pathForRun(runID, path.GraphRunEventsFile)
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
	fp, err := r.pathForRun(runID, path.GraphRunEventsFile)
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
	fp, err := r.pathForRun(runID, path.GraphRunEventsFile)
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
	loc, err := r.readIndex(runID)
	if err != nil {
		return err
	}
	runDir := path.GraphRunDir(loc.WorkspaceID, loc.JobID)
	mu := r.locks.lockFor(runID)
	mu.Lock()
	defer mu.Unlock()
	if err := r.storage.FileDelete(&fsmodel.FileDeleteRequest{Path: runDir}); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	r.locations.Delete(runID)
	return nil
}

func (r *fileGraphRunRepo) writeJSON(runID string, pathFn func(string, string) string, v any) error {
	if err := validateGraphRunID(runID); err != nil {
		return err
	}
	fp, err := r.pathForRun(runID, pathFn)
	if err != nil {
		return err
	}
	return r.writeJSONAt(runID, fp, v)
}

func (r *fileGraphRunRepo) writeJSONAt(runID, fp string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	mu := r.locks.lockFor(runID)
	mu.Lock()
	defer mu.Unlock()
	if err := r.ensureRunDirPath(filepath.Dir(fp)); err != nil {
		return err
	}
	return AtomicWriteFile(fp, data, 0644)
}

func (r *fileGraphRunRepo) readJSON(pathFn func(string, string) string, runID string, v any) error {
	if err := validateGraphRunID(runID); err != nil {
		return err
	}
	fp, err := r.pathForRun(runID, pathFn)
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
	loc, err := r.readIndex(runID)
	if err != nil {
		return err
	}
	return r.ensureRunDirPath(path.GraphRunDir(loc.WorkspaceID, loc.JobID))
}

func (r *fileGraphRunRepo) ensureRunDirPath(runDir string) error {
	if clean := filepath.Clean(runDir); clean != runDir {
		return fmt.Errorf("invalid graph run dir %q", runDir)
	}
	if err := r.storage.MkDir(&fsmodel.MkDirRequest{Path: runDir}); err != nil {
		return fmt.Errorf("mk graph run dir failed: %w", err)
	}
	return nil
}

func (r *fileGraphRunRepo) pathForRun(runID string, pathFn func(string, string) string) (string, error) {
	loc, err := r.readIndex(runID)
	if err != nil {
		return "", err
	}
	return pathFn(loc.WorkspaceID, loc.JobID), nil
}

func (r *fileGraphRunRepo) readIndex(runID string) (*graphRunLocation, error) {
	if err := validateGraphRunID(runID); err != nil {
		return nil, err
	}
	if cached, ok := r.locations.Load(runID); ok {
		if loc, ok := cached.(graphRunLocation); ok {
			return &loc, nil
		}
	}
	return nil, fmt.Errorf("graph run location is not registered: %s", runID)
}
