package repository

import (
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"sync"

	"github.com/fanlv/quartet/pkg/fileserver"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/types/model"
	"github.com/fanlv/quartet/types/path"
)

type IMJobMappingRepo interface {
	Get(platform, chatID string) (*model.IMJobMapping, error)
	Save(m *model.IMJobMapping) error
}

type imJobMappingRepo struct {
	sandbox fileserver.FileManager
	locks   [64]sync.RWMutex
}

func (r *imJobMappingRepo) lockFor(platform, chatID string) *sync.RWMutex {
	h := fnv.New32a()
	_, _ = h.Write([]byte(platform))
	_, _ = h.Write([]byte{':'})
	_, _ = h.Write([]byte(chatID))
	idx := h.Sum32() % uint32(len(r.locks))
	return &r.locks[idx]
}

func NewIMJobMappingRepo() (IMJobMappingRepo, error) {
	dir := path.IMJobMappingDir()
	sb := fileserver.GetFileManager()
	if err := sb.MkDir(&fsmodel.MkDirRequest{Path: dir}); err != nil {
		return nil, fmt.Errorf("create im job mapping dir failed: %w", err)
	}
	return &imJobMappingRepo{sandbox: sb}, nil
}

func (r *imJobMappingRepo) Get(platform, chatID string) (*model.IMJobMapping, error) {
	mu := r.lockFor(platform, chatID)
	mu.RLock()
	defer mu.RUnlock()

	// Prefer the new collision-free layout; fall back to legacy flat filenames
	// for backward compatibility.
	var result *fsmodel.FileReadResult
	var err error
	for _, fp := range []string{
		path.IMJobMappingFilePath(platform, chatID),
		path.IMJobMappingLegacyFilePath(platform, chatID),
	} {
		result, err = r.sandbox.FileRead(&fsmodel.FileReadRequest{File: fp})
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return nil, fmt.Errorf("read im job mapping failed: %w", err)
		}
		break
	}
	if err != nil {
		// All candidates were missing.
		return nil, nil
	}

	var m model.IMJobMapping
	if err := json.Unmarshal([]byte(result.Content), &m); err != nil {
		return nil, fmt.Errorf("unmarshal im job mapping failed: %w", err)
	}
	return &m, nil
}

func (r *imJobMappingRepo) Save(m *model.IMJobMapping) error {
	if m == nil {
		return fmt.Errorf("im job mapping: nil mapping")
	}
	mu := r.lockFor(m.Platform, m.ChatID)
	mu.Lock()
	defer mu.Unlock()

	data, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal im job mapping failed: %w", err)
	}

	fp := path.IMJobMappingFilePath(m.Platform, m.ChatID)
	if err := AtomicWriteFile(fp, data, 0644); err != nil {
		return fmt.Errorf("write im job mapping failed: %w", err)
	}
	return nil
}
