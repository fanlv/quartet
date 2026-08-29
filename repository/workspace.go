package repository

import (
	"encoding/json"
	"fmt"

	"github.com/fanlv/quartet/pkg/fileserver"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
	"github.com/fanlv/quartet/types/path"
)

type WorkspaceRepo interface {
	Save(id string, ws *model.Workspace) error
	Load(id string) (*model.Workspace, error)
	ListIDs() ([]string, error)
	LoadAll() ([]*model.Workspace, error)
	// SweepDeleted removes on-disk directories for workspaces marked as
	// deleted. Split from LoadAll so the read path has no write side effects.
	SweepDeleted() error
	RemoveDir(id string) error
}

type workspaceRepo struct {
	sandbox fileserver.FileManager
	baseDir string
	locks   lockShard
}

func NewWorkspaceRepo() (WorkspaceRepo, error) {
	baseDir := path.LocalWorkspacesDir()
	sb := fileserver.GetFileManager()
	err := sb.MkDir(&fsmodel.MkDirRequest{
		Path: baseDir,
	})
	if err != nil {
		return nil, fmt.Errorf("mk dir failed: %w", err)
	}
	return &workspaceRepo{sandbox: sb, baseDir: baseDir}, nil
}

func (r *workspaceRepo) ensureDir(id string) (string, error) {
	wsDir := path.LocalWorkspaceDir(id)
	if err := r.sandbox.MkDir(&fsmodel.MkDirRequest{Path: wsDir}); err != nil {
		return "", fmt.Errorf("mk workspace dir failed: %w", err)
	}
	metaDir := path.WorkspaceMetaDir(wsDir)
	if err := r.sandbox.MkDir(&fsmodel.MkDirRequest{Path: metaDir}); err != nil {
		return "", fmt.Errorf("mk meta dir failed: %w", err)
	}
	return wsDir, nil
}

func (r *workspaceRepo) Save(id string, ws *model.Workspace) error {
	if err := validateID(id); err != nil {
		return fmt.Errorf("invalid workspace id: %w", err)
	}
	mu := r.locks.lockFor(id)
	mu.Lock()
	defer mu.Unlock()

	wsDir, err := r.ensureDir(id)
	if err != nil {
		return fmt.Errorf("ensure workspace dir failed: %w", err)
	}

	data, err := json.MarshalIndent(ws, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal workspace failed: %w", err)
	}

	metaPath := path.WorkspaceMetaFilePath(wsDir)
	if err := AtomicWriteFile(metaPath, data, 0644); err != nil {
		return fmt.Errorf("write workspace file failed: %w", err)
	}
	return nil
}

func (r *workspaceRepo) Load(id string) (*model.Workspace, error) {
	if err := validateID(id); err != nil {
		return nil, fmt.Errorf("invalid workspace id: %w", err)
	}
	wsDir := path.LocalWorkspaceDir(id)
	metaPath := path.WorkspaceMetaFilePath(wsDir)
	result, err := r.sandbox.FileRead(&fsmodel.FileReadRequest{
		File: metaPath,
	})
	if err != nil {
		return nil, fmt.Errorf("read workspace file failed: %w", err)
	}

	var ws model.Workspace
	if err := json.Unmarshal([]byte(result.Content), &ws); err != nil {
		return nil, fmt.Errorf("unmarshal workspace failed: %w", err)
	}
	return &ws, nil
}

func (r *workspaceRepo) ListIDs() ([]string, error) {
	result, err := r.sandbox.FileList(&fsmodel.FileListRequest{
		Path: r.baseDir,
	})
	if err != nil {
		return nil, fmt.Errorf("list workspaces failed: %w", err)
	}

	var ids []string
	for _, file := range result.Files {
		if !file.IsDir {
			continue
		}
		id := file.Name
		metaPath := path.WorkspaceMetaFilePath(path.LocalWorkspaceDir(id))
		exists, err := r.sandbox.FileExists(metaPath)
		if err != nil || !exists.Exists {
			continue
		}
		ids = append(ids, id)
	}
	return ids, nil
}

func (r *workspaceRepo) LoadAll() ([]*model.Workspace, error) {
	ids, err := r.ListIDs()
	if err != nil {
		return nil, err
	}

	var workspaces []*model.Workspace
	for _, id := range ids {
		ws, err := r.Load(id)
		if err != nil {
			logger.Error("[workspaceRepo] load workspace %s failed: %v", id, err)
			continue
		}
		if ws.Deleted {
			continue
		}
		workspaces = append(workspaces, ws)
	}
	return workspaces, nil
}

// SweepDeleted removes the on-disk directory for any workspace whose meta
// record is flagged Deleted. Residue from a crashed / failed two-phase delete;
// run at boot so orphaned dirs do not sit forever. Best-effort per entry —
// failure on one id does not abort the sweep.
func (r *workspaceRepo) SweepDeleted() error {
	ids, err := r.ListIDs()
	if err != nil {
		return err
	}
	for _, id := range ids {
		ws, loadErr := r.Load(id)
		if loadErr != nil {
			logger.Error("[workspaceRepo] sweep load %s failed: %v", id, loadErr)
			continue
		}
		if !ws.Deleted {
			continue
		}
		if rmErr := r.RemoveDir(id); rmErr != nil {
			logger.Error("[workspaceRepo] sweep cleanup %s failed: %v", id, rmErr)
		}
	}
	return nil
}

func (r *workspaceRepo) RemoveDir(id string) error {
	if err := validateID(id); err != nil {
		return fmt.Errorf("invalid workspace id: %w", err)
	}
	wsDir := path.LocalWorkspaceDir(id)
	// Idempotent: if the directory is already gone (crash between RemoveDir
	// and caller-side cleanup, manual rm -rf, etc.), treat as success so the
	// caller can finish its two-phase delete instead of getting stuck.
	exists, err := r.sandbox.FileExists(wsDir)
	if err == nil && exists != nil && !exists.Exists {
		return nil
	}
	if err := r.sandbox.FileDelete(&fsmodel.FileDeleteRequest{Path: wsDir}); err != nil {
		return fmt.Errorf("remove workspace dir failed: %w", err)
	}
	return nil
}
