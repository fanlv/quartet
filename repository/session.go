package repository

import (
	"encoding/json"
	"fmt"
	"path/filepath"

	"github.com/fanlv/quartet/pkg/fileserver"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
	"github.com/fanlv/quartet/types/path"
)

type SessionRepo interface {
	Save(sessionID string, meta *model.Session) error
	Load(sessionID string) (*model.Session, error)
	ListIDs() ([]string, error)
	LoadAll() ([]*model.Session, error)
}

type sessionRepo struct {
	sandbox    fileserver.FileManager
	sessionDir string
	// locks shard Save per session so independent sessions don't block each
	// other on slow file I/O, while still preventing lost updates when the
	// same session is saved concurrently.
	locks lockShard
}

// validateSessionID refuses path-traversal attempts and empty IDs so callers
// cannot walk out of the jobs/<jobID>/sessions directory.
func validateSessionID(sessionID string) error {
	return validateID(sessionID)
}

// NewSessionRepo creates a SessionRepo. Root dir: {LOCAL_MEMORY}/quartet/data/workspaces/{wsID}/jobs/{jobID}/sessions/.
func NewSessionRepo(wsID, jobID string) (SessionRepo, error) {
	sessionDir := path.LocalSessionsDirInWorkspaceJob(wsID, jobID)
	sb := fileserver.GetFileManager()
	err := sb.MkDir(&fsmodel.MkDirRequest{
		Path: sessionDir,
	})
	if err != nil {
		return nil, fmt.Errorf("mk dir failed: %w", err)
	}

	return &sessionRepo{sandbox: sb, sessionDir: sessionDir}, nil
}

func (r *sessionRepo) Save(sessionID string, meta *model.Session) error {
	if err := validateSessionID(sessionID); err != nil {
		return fmt.Errorf("invalid session id: %w", err)
	}
	if meta == nil {
		return fmt.Errorf("session meta is nil")
	}
	mu := r.locks.lockFor(sessionID)
	mu.Lock()
	defer mu.Unlock()

	sessionDir := filepath.Join(r.sessionDir, sessionID)
	if err := r.sandbox.MkDir(&fsmodel.MkDirRequest{Path: sessionDir}); err != nil {
		return fmt.Errorf("ensure session dir failed: %w", err)
	}
	data, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("marshal meta failed: %w", err)
	}

	metaPath := path.MetaFilePath(sessionDir)
	if err := AtomicWriteFile(metaPath, data, 0644); err != nil {
		return fmt.Errorf("write meta file failed: %w", err)
	}

	return nil
}

// Load reads session metadata from {LOCAL_MEMORY}/jobs/{jobID}/sessions/{sessionID}/.meta/meta.json
func (r *sessionRepo) Load(sessionID string) (*model.Session, error) {
	if err := validateSessionID(sessionID); err != nil {
		return nil, fmt.Errorf("invalid session id: %w", err)
	}
	sessionDir := filepath.Join(r.sessionDir, sessionID)
	metaPath := path.MetaFilePath(sessionDir)
	result, err := r.sandbox.FileRead(&fsmodel.FileReadRequest{
		File: metaPath,
	})
	if err != nil {
		return nil, fmt.Errorf("read meta file failed: %w", err)
	}

	var meta model.Session
	if err := json.Unmarshal([]byte(result.Content), &meta); err != nil {
		return nil, fmt.Errorf("unmarshal meta failed: %w", err)
	}

	return &meta, nil
}

// ListIDs lists subdirectories under {LOCAL_MEMORY}/jobs/{jobID}/sessions/, returns those containing .meta/meta.json
func (r *sessionRepo) ListIDs() ([]string, error) {
	result, err := r.sandbox.FileList(&fsmodel.FileListRequest{
		Path: r.sessionDir,
	})
	if err != nil {
		return nil, fmt.Errorf("list sessions failed: %w", err)
	}

	var sessionIDs []string
	for _, file := range result.Files {
		if !file.IsDir {
			continue
		}
		sessionID := file.Name
		metaPath := path.MetaFilePath(filepath.Join(r.sessionDir, sessionID))
		exists, err := r.sandbox.FileExists(metaPath)
		if err != nil || !exists.Exists {
			continue
		}

		sessionIDs = append(sessionIDs, sessionID)
	}

	return sessionIDs, nil
}

// LoadAll loads all non-deleted sessions from {LOCAL_MEMORY}/jobs/{jobID}/sessions/
func (r *sessionRepo) LoadAll() ([]*model.Session, error) {
	sessionIDs, err := r.ListIDs()
	if err != nil {
		return nil, err
	}

	var metas []*model.Session
	for _, sessionID := range sessionIDs {
		meta, err := r.Load(sessionID)
		if err != nil {
			logger.Error("[sessionRepo] load session %s failed: %v", sessionID, err)
			continue
		}
		if meta.Deleted {
			continue
		}
		metas = append(metas, meta)
	}

	return metas, nil
}
