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

type TemplateRepo interface {
	Save(ctx context.Context, tmpl *model.LoopTemplate) error
	Get(ctx context.Context, id string) (*model.LoopTemplate, error)
	List(ctx context.Context) ([]*model.LoopTemplate, error)
	Delete(ctx context.Context, id string) error
	Update(ctx context.Context, id string, tmpl *model.LoopTemplate) error
}

type fileTemplateRepo struct {
	dir     string
	sandbox fileserver.FileManager
	locks   lockShard
}

func NewTemplateRepo() (TemplateRepo, error) {
	dir, err := path.TemplatesDir()
	if err != nil {
		return nil, err
	}
	sb := fileserver.GetFileManager()
	return &fileTemplateRepo{dir: dir, sandbox: sb}, nil
}

func validateTemplateID(id string) error {
	return validateID(id)
}

func (r *fileTemplateRepo) Save(_ context.Context, tmpl *model.LoopTemplate) error {
	if tmpl == nil {
		return os.ErrInvalid
	}
	if err := validateTemplateID(tmpl.ID); err != nil {
		return err
	}
	mu := r.locks.lockFor(tmpl.ID)
	mu.Lock()
	defer mu.Unlock()
	data, err := json.MarshalIndent(tmpl, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWriteFile(filepath.Join(r.dir, tmpl.ID+".json"), data, 0644)
}

func (r *fileTemplateRepo) Get(_ context.Context, id string) (*model.LoopTemplate, error) {
	if err := validateTemplateID(id); err != nil {
		return nil, err
	}
	readResult, err := r.sandbox.FileRead(&fsmodel.FileReadRequest{
		File: filepath.Join(r.dir, id+".json"),
	})
	if err != nil {
		return nil, err
	}
	var tmpl model.LoopTemplate
	if err := json.Unmarshal([]byte(readResult.Content), &tmpl); err != nil {
		return nil, err
	}
	return &tmpl, nil
}

func (r *fileTemplateRepo) List(ctx context.Context) ([]*model.LoopTemplate, error) {
	result, err := r.sandbox.FileList(&fsmodel.FileListRequest{Path: r.dir})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}

	var templates []*model.LoopTemplate
	for _, f := range result.Files {
		if f.IsDir || filepath.Ext(f.Name) != ".json" {
			continue
		}
		filePath := filepath.Join(r.dir, f.Name)
		readResult, err := r.sandbox.FileRead(&fsmodel.FileReadRequest{
			File: filePath,
		})
		if err != nil {
			// Corrupt / unreadable entries should not silently disappear —
			// surface them in the log so operators can investigate instead
			// of chasing phantom missing templates.
			logger.Warnf(ctx, "[templateRepo] skip unreadable file %s: %v", filePath, err)
			continue
		}
		var tmpl model.LoopTemplate
		if err := json.Unmarshal([]byte(readResult.Content), &tmpl); err != nil {
			logger.Warnf(ctx, "[templateRepo] skip malformed JSON %s: %v", filePath, err)
			continue
		}
		templates = append(templates, &tmpl)
	}

	sort.Slice(templates, func(i, j int) bool {
		return templates[i].CreatedAt.After(templates[j].CreatedAt)
	})

	return templates, nil
}

func (r *fileTemplateRepo) Update(_ context.Context, id string, tmpl *model.LoopTemplate) error {
	if tmpl == nil {
		return os.ErrInvalid
	}
	if err := validateTemplateID(id); err != nil {
		return err
	}
	mu := r.locks.lockFor(id)
	mu.Lock()
	defer mu.Unlock()
	data, err := json.MarshalIndent(tmpl, "", "  ")
	if err != nil {
		return err
	}
	return AtomicWriteFile(filepath.Join(r.dir, id+".json"), data, 0644)
}

func (r *fileTemplateRepo) Delete(_ context.Context, id string) error {
	if err := validateTemplateID(id); err != nil {
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
