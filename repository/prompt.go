package repository

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"

	"github.com/fanlv/quartet/pkg/fileserver"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/types/path"
)

type PromptRepo interface {
	Get(ctx context.Context, key string) (string, error)
	Save(ctx context.Context, key string, content string) error
}

type filePromptRepo struct {
	dir     string
	sandbox fileserver.FileManager
}

func NewPromptRepo() (PromptRepo, error) {
	dir, err := path.PromptsDir()
	if err != nil {
		return nil, err
	}
	sb := fileserver.GetFileManager()
	return &filePromptRepo{dir: dir, sandbox: sb}, nil
}

func (r *filePromptRepo) validateKey(key string) error {
	if key == "" {
		return os.ErrInvalid
	}
	if strings.Contains(key, "..") || strings.ContainsAny(key, "/\\") {
		return os.ErrPermission
	}
	return nil
}

func (r *filePromptRepo) Get(_ context.Context, key string) (string, error) {
	if err := r.validateKey(key); err != nil {
		return "", err
	}
	filePath := filepath.Join(r.dir, key+".md")
	result, err := r.sandbox.FileRead(&fsmodel.FileReadRequest{File: filePath})
	if err != nil {
		// os.IsNotExist can't see through the upstream SDK's fmt.Errorf wrap.
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	return result.Content, nil
}

func (r *filePromptRepo) Save(_ context.Context, key string, content string) error {
	if err := r.validateKey(key); err != nil {
		return err
	}
	filePath := filepath.Join(r.dir, key+".md")
	return AtomicWriteFile(filePath, []byte(content), 0644)
}
