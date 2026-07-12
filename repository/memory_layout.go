package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"

	"github.com/fanlv/quartet/types/model"
	"github.com/fanlv/quartet/types/path"
)

type MemoryLayoutRepo interface {
	Load(ctx context.Context) (*model.MemoryLayoutManifest, error)
	Save(ctx context.Context, manifest *model.MemoryLayoutManifest) error
}

type fileMemoryLayoutRepo struct {
	filePath string
}

func NewMemoryLayoutRepo() (MemoryLayoutRepo, error) {
	filePath, err := path.MemoryLayoutFile()
	if err != nil {
		return nil, fmt.Errorf("resolve memory layout manifest path failed: %w", err)
	}
	return &fileMemoryLayoutRepo{filePath: filePath}, nil
}

func (r *fileMemoryLayoutRepo) Load(_ context.Context) (*model.MemoryLayoutManifest, error) {
	data, err := os.ReadFile(r.filePath)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, fmt.Errorf("memory layout manifest is missing at %s: %w", r.filePath, err)
		}
		return nil, fmt.Errorf("read memory layout manifest %s failed: %w", r.filePath, err)
	}
	if len(data) == 0 {
		return nil, fmt.Errorf("memory layout manifest is empty: %s", r.filePath)
	}
	var manifest model.MemoryLayoutManifest
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("parse memory layout manifest %s failed: %w", r.filePath, err)
	}
	return &manifest, nil
}

func (r *fileMemoryLayoutRepo) Save(_ context.Context, manifest *model.MemoryLayoutManifest) error {
	if manifest == nil {
		return fmt.Errorf("save memory layout manifest failed: manifest is nil")
	}
	data, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal memory layout manifest failed: %w", err)
	}
	if err := AtomicWriteFile(r.filePath, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("write memory layout manifest %s failed: %w", r.filePath, err)
	}
	return nil
}
