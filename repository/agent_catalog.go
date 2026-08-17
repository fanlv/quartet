package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"

	"github.com/fanlv/quartet/pkg/fileserver"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/types/model"
	"github.com/fanlv/quartet/types/path"
)

type AgentCatalogRepo interface {
	Load(ctx context.Context) (*model.AgentCatalogSnapshot, error)
	Save(ctx context.Context, snapshot *model.AgentCatalogSnapshot) error
}

type fileAgentCatalogRepo struct {
	filePath string
	sandbox  fileserver.FileManager
	mu       sync.RWMutex
}

func NewAgentCatalogRepo() (AgentCatalogRepo, error) {
	filePath, err := path.AgentCatalogFile()
	if err != nil {
		return nil, fmt.Errorf("get agent catalog path failed: %w", err)
	}
	return &fileAgentCatalogRepo{
		filePath: filePath,
		sandbox:  fileserver.GetFileManager(),
	}, nil
}

func (r *fileAgentCatalogRepo) Load(ctx context.Context) (*model.AgentCatalogSnapshot, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result, err := r.sandbox.FileRead(&fsmodel.FileReadRequest{File: r.filePath})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return emptyAgentCatalogSnapshot(), nil
		}
		return nil, fmt.Errorf("read agent catalog %q failed: %w", r.filePath, err)
	}

	var snapshot model.AgentCatalogSnapshot
	if err := json.Unmarshal([]byte(result.Content), &snapshot); err != nil {
		backupCorruptFile(ctx, r.filePath, err)
		return nil, fmt.Errorf("unmarshal agent catalog %q failed: %w", r.filePath, err)
	}
	if snapshot.Version != model.AgentCatalogVersion {
		return nil, fmt.Errorf(
			"unsupported agent catalog version in %q: got=%d want=%d",
			r.filePath,
			snapshot.Version,
			model.AgentCatalogVersion,
		)
	}
	if snapshot.Agents == nil {
		snapshot.Agents = []model.CustomAgent{}
	}
	if snapshot.BuiltinRevisions == nil {
		snapshot.BuiltinRevisions = make(map[string][]model.AgentRuntimeRevision)
	}
	return &snapshot, nil
}

func (r *fileAgentCatalogRepo) Save(ctx context.Context, snapshot *model.AgentCatalogSnapshot) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	if snapshot == nil {
		return fmt.Errorf("save agent catalog failed: snapshot is nil")
	}
	if snapshot.Version != model.AgentCatalogVersion {
		return fmt.Errorf(
			"save agent catalog failed: unsupported version got=%d want=%d",
			snapshot.Version,
			model.AgentCatalogVersion,
		)
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal agent catalog failed: %w", err)
	}
	if err := AtomicWriteFile(r.filePath, data, 0o600); err != nil {
		return fmt.Errorf("save agent catalog %q failed: %w", r.filePath, err)
	}
	return nil
}

func emptyAgentCatalogSnapshot() *model.AgentCatalogSnapshot {
	return &model.AgentCatalogSnapshot{
		Version:          model.AgentCatalogVersion,
		Agents:           []model.CustomAgent{},
		BuiltinRevisions: make(map[string][]model.AgentRuntimeRevision),
	}
}
