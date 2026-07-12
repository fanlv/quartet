package memorylayout

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/types/model"
	typepath "github.com/fanlv/quartet/types/path"
)

type Service interface {
	Validate(ctx context.Context) error
}

type serviceImpl struct {
	repo repository.MemoryLayoutRepo
}

func NewService() (Service, error) {
	repo, err := repository.NewMemoryLayoutRepo()
	if err != nil {
		return nil, err
	}
	return &serviceImpl{repo: repo}, nil
}

func (s *serviceImpl) Validate(ctx context.Context) error {
	var problems []string

	manifest, err := s.repo.Load(ctx)
	if err != nil {
		problems = append(problems, err.Error())
	} else {
		if manifest.Version != model.CurrentMemoryLayoutVersion {
			problems = append(problems, fmt.Sprintf(
				"memory layout version mismatch: manifest=%d binary=%d",
				manifest.Version, model.CurrentMemoryLayoutVersion,
			))
		}
		if manifest.Status != model.MemoryLayoutStatusComplete {
			problems = append(problems, fmt.Sprintf(
				"memory layout migration is not complete: status=%q batchId=%q",
				manifest.Status, manifest.BatchID,
			))
		}
		if strings.TrimSpace(manifest.BatchID) == "" {
			problems = append(problems, "memory layout manifest batchId is empty")
		}
		if manifest.CompletedAt.IsZero() {
			problems = append(problems, "memory layout manifest completedAt is empty")
		}
	}

	requiredDirs, pathProblems := requiredDirectoryPaths()
	problems = append(problems, pathProblems...)
	for _, dir := range requiredDirs {
		info, statErr := os.Lstat(dir)
		if statErr != nil {
			problems = append(problems, fmt.Sprintf("required memory directory %s is unavailable: %v", dir, statErr))
			continue
		}
		if info.Mode()&os.ModeSymlink != 0 {
			problems = append(problems, fmt.Sprintf("required memory directory must not be a symbolic link: %s", dir))
			continue
		}
		if !info.IsDir() {
			problems = append(problems, fmt.Sprintf("required memory path is not a directory: %s", dir))
		}
	}

	for _, filePath := range requiredJSONFiles() {
		data, readErr := os.ReadFile(filePath)
		if readErr != nil {
			problems = append(problems, fmt.Sprintf("required memory data file %s is unavailable: %v", filePath, readErr))
			continue
		}
		if len(bytes.TrimSpace(data)) == 0 {
			problems = append(problems, fmt.Sprintf("required memory data file is empty: %s", filePath))
			continue
		}
		if !json.Valid(data) {
			problems = append(problems, fmt.Sprintf("required memory data file contains invalid JSON: %s", filePath))
		}
	}

	root, rootErr := typepath.LocalMemoryDir()
	if rootErr != nil {
		problems = append(problems, rootErr.Error())
	} else {
		for _, name := range []string{
			"agent", "workspaces", "im", "uploads", "user_input", "wechat",
			"shell", "tools", "fanlv", "oncall",
		} {
			legacyPath := filepath.Join(root, name)
			if _, statErr := os.Lstat(legacyPath); statErr == nil {
				problems = append(problems, fmt.Sprintf("legacy memory entry still exists: %s", legacyPath))
			} else if !os.IsNotExist(statErr) {
				problems = append(problems, fmt.Sprintf("inspect legacy memory entry %s failed: %v", legacyPath, statErr))
			}
		}
	}

	if len(problems) > 0 {
		return fmt.Errorf("memory layout validation failed:\n- %s", strings.Join(problems, "\n- "))
	}
	return nil
}

func requiredDirectoryPaths() ([]string, []string) {
	resolvers := []func() (string, error){
		typepath.PromptsDir,
		typepath.TemplatesDir,
		typepath.GraphWorkflowsDir,
		typepath.SchedulesDir,
		typepath.UsageStatsDir,
		typepath.UploadsDir,
		typepath.PersistentIMMediaDir,
		typepath.ScheduleStatesDir,
		typepath.QuartetCacheDir,
		typepath.IMMediaCacheDir,
		typepath.QuartetTmpDir,
		typepath.SandboxComposeStateDir,
	}
	dirs := make([]string, 0, len(resolvers)+4)
	var problems []string
	for _, resolve := range resolvers {
		dir, err := resolve()
		if err != nil {
			problems = append(problems, err.Error())
			continue
		}
		dirs = append(dirs, dir)
	}
	dataDir, err := typepath.QuartetDataDir()
	if err != nil {
		problems = append(problems, err.Error())
	} else {
		for _, name := range []string{"workspaces", "im", "user-input", "wechat"} {
			dirs = append(dirs, filepath.Join(dataDir, name))
		}
	}
	return dirs, problems
}

func requiredJSONFiles() []string {
	var files []string
	if path, err := typepath.ModelsConfigFile(); err == nil {
		files = append(files, path)
	}
	if path, err := typepath.SettingsConfigFile(); err == nil {
		files = append(files, path)
	}
	return files
}
