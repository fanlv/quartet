package repository

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/fanlv/quartet/pkg/fileserver"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
	"github.com/fanlv/quartet/types/path"
)

type ModelConfigRepo interface {
	Load(ctx context.Context) ([]*model.ModelInstance, error)
	GetByID(ctx context.Context, id int64) (*model.ModelInstance, error)
	Save(ctx context.Context, m *model.ModelInstance) error
}

type fileModelConfigRepo struct {
	models   []*model.ModelInstance
	filePath string
	sandbox  fileserver.FileManager
	mu       sync.RWMutex
}

type modelConfigFile struct {
	Models []*model.ModelInstance `json:"models"`
}

func NewModelConfigRepo() (ModelConfigRepo, error) {
	modelsFile, err := path.ModelsConfigFile()
	if err != nil {
		return nil, fmt.Errorf("get models file path failed: %w", err)
	}
	sb := fileserver.GetFileManager()
	return &fileModelConfigRepo{filePath: modelsFile, sandbox: sb}, nil
}

func (r *fileModelConfigRepo) Load(ctx context.Context) ([]*model.ModelInstance, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	state := &modelConfigFile{}
	result, err := r.sandbox.FileRead(&fsmodel.FileReadRequest{File: r.filePath})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			if err := r.saveNoLock([]*model.ModelInstance{}); err != nil {
				return nil, fmt.Errorf("create models config file failed: %w", err)
			}
			r.models = []*model.ModelInstance{}
			return r.models, nil
		}
		return nil, fmt.Errorf("read models config file failed: %w", err)
	}
	if err := json.Unmarshal([]byte(result.Content), state); err != nil {
		return nil, fmt.Errorf("unmarshal models config failed: %w", err)
	}

	r.models = state.Models
	return state.Models, nil
}

// GetByID finds a model by ID from the in-memory cache (loaded from {LOCAL_MEMORY}/quartet/data/models.json).
func (r *fileModelConfigRepo) GetByID(ctx context.Context, id int64) (*model.ModelInstance, error) {
	r.mu.RLock()
	defer r.mu.RUnlock()

	models := r.models
	for _, m := range models {
		if m != nil && m.ID == id && m.DeletedAt == 0 {
			return m, nil
		}
	}
	return nil, fmt.Errorf("model not found: %d", id)
}

func genModelID(models []*model.ModelInstance) int64 {
	exists := make(map[int64]struct{}, len(models))
	for _, x := range models {
		exists[x.ID] = struct{}{}
	}
	id := time.Now().Unix()
	for {
		if _, ok := exists[id]; !ok {
			return id
		}
		id++
	}
}

// Save inserts or updates a model config in {LOCAL_MEMORY}/quartet/data/models.json.
func (r *fileModelConfigRepo) Save(ctx context.Context, m *model.ModelInstance) error {
	if m == nil {
		return fmt.Errorf("model is nil")
	}

	r.mu.Lock()
	defer r.mu.Unlock()

	models, err := r.loadNoLock()
	if err != nil {
		return err
	}

	nowMillis := time.Now().UnixMilli()
	if m.ID != 0 {
		if err := updateModel(models, m, nowMillis); err != nil {
			return err
		}
	} else {
		insertModel(&models, m, nowMillis)
	}

	if err := r.saveNoLock(models); err != nil {
		return err
	}
	r.models = models
	return nil
}

func (r *fileModelConfigRepo) loadNoLock() ([]*model.ModelInstance, error) {
	state := &modelConfigFile{}
	result, err := r.sandbox.FileRead(&fsmodel.FileReadRequest{File: r.filePath})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return []*model.ModelInstance{}, nil
		}
		logger.Errorf(nil, "[model_config] read failed: file=%s err=%v", r.filePath, err)
		return nil, fmt.Errorf("read models config file failed: %w", err)
	}
	if err := json.Unmarshal([]byte(result.Content), state); err != nil {
		return nil, fmt.Errorf("unmarshal models config failed: %w", err)
	}
	if state.Models == nil {
		return []*model.ModelInstance{}, nil
	}
	return state.Models, nil
}

// saveNoLock writes models to {LOCAL_MEMORY}/quartet/data/models.json without acquiring lock.
func (r *fileModelConfigRepo) saveNoLock(models []*model.ModelInstance) error {
	data, err := json.MarshalIndent(&modelConfigFile{Models: models}, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWriteFile(r.filePath, data, 0644)
}

func updateModel(models []*model.ModelInstance, m *model.ModelInstance, nowMillis int64) error {
	foundIdx := -1
	for i, x := range models {
		if x.ID == m.ID {
			foundIdx = i
			break
		}
	}
	if foundIdx < 0 {
		return fmt.Errorf("model not found: %d", m.ID)
	}

	x := models[foundIdx]
	if m.CreatedAt == 0 {
		m.CreatedAt = x.CreatedAt
	}
	m.UpdatedAt = nowMillis

	if m.ModelClass != "" {
		x.ModelClass = m.ModelClass
	}
	if m.DisplayName != "" {
		x.DisplayName = m.DisplayName
	}
	if m.Connection != nil {
		x.Connection = m.Connection
	}
	if m.ThinkingType != "" {
		x.ThinkingType = m.ThinkingType
	}
	if m.Status != 0 {
		x.Status = m.Status
	}
	if m.CreatedAt != 0 {
		x.CreatedAt = m.CreatedAt
	}
	x.UpdatedAt = m.UpdatedAt
	if m.DeletedAt != 0 {
		x.DeletedAt = m.DeletedAt
	}
	models[foundIdx] = x
	return nil
}

func insertModel(models *[]*model.ModelInstance, m *model.ModelInstance, nowMillis int64) {
	ms := *models
	m.ID = genModelID(ms)
	if m.CreatedAt == 0 {
		m.CreatedAt = nowMillis
	}
	m.UpdatedAt = nowMillis
	*models = append(ms, m)
}
