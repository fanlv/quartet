package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/fanlv/quartet/pkg/fileserver"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/types/model"
	"github.com/fanlv/quartet/types/path"
)

type ACPProbeCacheRepo interface {
	Load(ctx context.Context) (*model.ACPProbeCacheSnapshot, error)
	Save(ctx context.Context, snapshot *model.ACPProbeCacheSnapshot) error
}

type fileACPProbeCacheRepo struct {
	filePath string
	sandbox  fileserver.FileManager
}

func NewACPProbeCacheRepo() (ACPProbeCacheRepo, error) {
	filePath, err := path.ACPProbeCacheFile()
	if err != nil {
		return nil, fmt.Errorf("get ACP probe cache path failed: %w", err)
	}
	return &fileACPProbeCacheRepo{
		filePath: filePath,
		sandbox:  fileserver.GetFileManager(),
	}, nil
}

func (r *fileACPProbeCacheRepo) Load(ctx context.Context) (*model.ACPProbeCacheSnapshot, error) {
	result, err := r.sandbox.FileRead(&fsmodel.FileReadRequest{File: r.filePath})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &model.ACPProbeCacheSnapshot{
				Version: model.ACPProbeCacheVersion,
				Entries: make(map[string]model.ACPProbeCacheEntry),
			}, nil
		}
		return nil, fmt.Errorf("read ACP probe cache %q failed: %w", r.filePath, err)
	}

	var snapshot model.ACPProbeCacheSnapshot
	if err := json.Unmarshal([]byte(result.Content), &snapshot); err != nil {
		backupCorruptFile(ctx, r.filePath, err)
		return nil, fmt.Errorf("unmarshal ACP probe cache %q failed: %w", r.filePath, err)
	}
	if snapshot.Version != model.ACPProbeCacheVersion {
		// Probe cache is disposable. A schema change must not prevent quartet
		// from starting; old command-keyed entries are intentionally rebuilt.
		return &model.ACPProbeCacheSnapshot{
			Version: model.ACPProbeCacheVersion,
			Entries: make(map[string]model.ACPProbeCacheEntry),
		}, nil
	}
	if snapshot.Entries == nil {
		snapshot.Entries = make(map[string]model.ACPProbeCacheEntry)
	}
	return &snapshot, nil
}

func (r *fileACPProbeCacheRepo) Save(ctx context.Context, snapshot *model.ACPProbeCacheSnapshot) error {
	if snapshot == nil {
		return fmt.Errorf("save ACP probe cache failed: snapshot is nil")
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal ACP probe cache failed: %w", err)
	}
	if err := AtomicWriteFile(r.filePath, data, 0o644); err != nil {
		return fmt.Errorf("save ACP probe cache %q failed: %w", r.filePath, err)
	}
	return nil
}
