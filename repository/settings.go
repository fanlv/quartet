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

type SettingsRepo interface {
	Get() (*model.Settings, error)
	Save(s *model.Settings) error
}

type fileSettingsRepo struct {
	filePath string
	sandbox  fileserver.FileManager
	mu       sync.RWMutex
}

func NewSettingsRepo() (SettingsRepo, error) {
	fp, err := path.SettingsConfigFile()
	if err != nil {
		return nil, fmt.Errorf("get settings file path failed: %w", err)
	}
	sb := fileserver.GetFileManager()
	return &fileSettingsRepo{filePath: fp, sandbox: sb}, nil
}

func (r *fileSettingsRepo) Get() (*model.Settings, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	result, err := r.sandbox.FileRead(&fsmodel.FileReadRequest{File: r.filePath})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &model.Settings{}, nil
		}
		return nil, fmt.Errorf("read settings file failed: %w", err)
	}

	var s model.Settings
	if err := json.Unmarshal([]byte(result.Content), &s); err != nil {
		backupCorruptFile(context.Background(), r.filePath, err)
		return &model.Settings{}, fmt.Errorf("parse settings file failed: %w", err)
	}
	return &s, nil
}

func (r *fileSettingsRepo) Save(s *model.Settings) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	// Contains secrets (LarkAppSecret, admin IDs); restrict to owner-only.
	return AtomicWriteFile(r.filePath, data, 0600)
}
