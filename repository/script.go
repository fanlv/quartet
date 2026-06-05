package repository

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"

	"github.com/fanlv/quartet/pkg/fileserver"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
	"github.com/fanlv/quartet/types/path"
)

type ScriptRepo interface {
	Save(ctx context.Context, script *model.Script) error
	Get(ctx context.Context, id string) (*model.Script, error)
	List(ctx context.Context) ([]*model.Script, error)
	Delete(ctx context.Context, id string) error
}

type fileScriptRepo struct {
	dir     string
	sandbox fileserver.FileManager
	locks   lockShard
}

func NewScriptRepo() (ScriptRepo, error) {
	dir, err := path.ScriptsDir()
	if err != nil {
		return nil, err
	}
	sb := fileserver.GetFileManager()
	return &fileScriptRepo{dir: dir, sandbox: sb}, nil
}

func validateScriptID(id string) error {
	return validateID(id)
}

func (r *fileScriptRepo) Save(_ context.Context, script *model.Script) error {
	if script == nil {
		return os.ErrInvalid
	}
	if err := validateScriptID(script.ID); err != nil {
		return err
	}
	mu := r.locks.lockFor(script.ID)
	mu.Lock()
	defer mu.Unlock()
	data, err := json.MarshalIndent(script, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWriteFile(filepath.Join(r.dir, script.ID+".json"), data, 0644)
}

func (r *fileScriptRepo) Get(_ context.Context, id string) (*model.Script, error) {
	if err := validateScriptID(id); err != nil {
		return nil, err
	}
	result, err := r.sandbox.FileRead(&fsmodel.FileReadRequest{
		File: filepath.Join(r.dir, id+".json"),
	})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var script model.Script
	if err := json.Unmarshal([]byte(result.Content), &script); err != nil {
		return nil, err
	}
	return &script, nil
}

func (r *fileScriptRepo) List(ctx context.Context) ([]*model.Script, error) {
	listResult, err := r.sandbox.FileList(&fsmodel.FileListRequest{Path: r.dir})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var scripts []*model.Script
	for _, f := range listResult.Files {
		if f.IsDir || filepath.Ext(f.Name) != ".json" {
			continue
		}
		filePath := filepath.Join(r.dir, f.Name)
		readResult, err := r.sandbox.FileRead(&fsmodel.FileReadRequest{
			File: filePath,
		})
		if err != nil {
			logger.Warnf(ctx, "[scriptRepo] skip unreadable file %s: %v", filePath, err)
			continue
		}
		var script model.Script
		if err := json.Unmarshal([]byte(readResult.Content), &script); err != nil {
			logger.Warnf(ctx, "[scriptRepo] skip malformed JSON %s: %v", filePath, err)
			continue
		}
		scripts = append(scripts, &script)
	}

	sort.Slice(scripts, func(i, j int) bool {
		return scripts[i].UpdatedAt.After(scripts[j].UpdatedAt)
	})

	return scripts, nil
}

func (r *fileScriptRepo) Delete(_ context.Context, id string) error {
	if err := validateScriptID(id); err != nil {
		return err
	}
	mu := r.locks.lockFor(id)
	mu.Lock()
	defer mu.Unlock()
	err := r.sandbox.FileDelete(&fsmodel.FileDeleteRequest{
		Path: filepath.Join(r.dir, id+".json"),
	})
	return err
}
