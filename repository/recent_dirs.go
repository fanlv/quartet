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
	"github.com/fanlv/quartet/types/path"
)

const maxRecentDirs = 20

type RecentDirs struct {
	Dirs []string `json:"dirs"`
}

type RecentDirsRepo interface {
	Get() (*RecentDirs, error)
	Add(ctx context.Context, dir string) error
}

type fileRecentDirsRepo struct {
	filePath string
	sandbox  fileserver.FileManager
	mu       sync.Mutex
}

func NewRecentDirsRepo() (RecentDirsRepo, error) {
	fp, err := path.RecentDirsFile()
	if err != nil {
		return nil, fmt.Errorf("get recent dirs file path failed: %w", err)
	}
	sb := fileserver.GetFileManager()
	return &fileRecentDirsRepo{filePath: fp, sandbox: sb}, nil
}

func (r *fileRecentDirsRepo) Get() (*RecentDirs, error) {
	r.mu.Lock()
	defer r.mu.Unlock()

	result, err := r.sandbox.FileRead(&fsmodel.FileReadRequest{File: r.filePath})
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &RecentDirs{Dirs: []string{}}, nil
		}
		return nil, fmt.Errorf("read recent dirs file failed: %w", err)
	}

	var rd RecentDirs
	if err := json.Unmarshal([]byte(result.Content), &rd); err != nil {
		backupCorruptFile(context.Background(), r.filePath, err)
		return &RecentDirs{Dirs: []string{}}, fmt.Errorf("parse recent dirs file failed: %w", err)
	}
	if rd.Dirs == nil {
		rd.Dirs = []string{}
	}
	return &rd, nil
}

func (r *fileRecentDirsRepo) Add(ctx context.Context, dir string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	var rd RecentDirs
	result, err := r.sandbox.FileRead(&fsmodel.FileReadRequest{File: r.filePath})
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("read recent dirs file failed: %w", err)
		}
	} else {
		if err := json.Unmarshal([]byte(result.Content), &rd); err != nil {
			// Back the corrupt file up so the upcoming write does not clobber it.
			backupCorruptFile(ctx, r.filePath, err)
			rd = RecentDirs{}
		}
	}

	filtered := make([]string, 0, len(rd.Dirs))
	for _, d := range rd.Dirs {
		if d != dir {
			filtered = append(filtered, d)
		}
	}

	rd.Dirs = append([]string{dir}, filtered...)

	if len(rd.Dirs) > maxRecentDirs {
		rd.Dirs = rd.Dirs[:maxRecentDirs]
	}

	out, err := json.MarshalIndent(rd, "", "  ")
	if err != nil {
		return err
	}
	if err := ensureParentDir(r.filePath); err != nil {
		return err
	}
	return AtomicWriteFile(r.filePath, out, 0o644)
}
