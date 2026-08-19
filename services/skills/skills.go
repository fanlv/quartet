package skills

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
)

type SkillInfo struct {
	Name   string   `json:"name"`
	Path   string   `json:"path"`
	Scope  string   `json:"scope"`
	Agents []string `json:"agents"`
}

type scopeCache struct {
	mu        sync.RWMutex
	skills    []SkillInfo
	updatedAt time.Time
	loaded    bool
	loading   bool
}

type Service struct {
	ctx            context.Context
	project        scopeCache
	global         scopeCache
	ttl            time.Duration
	projectInstall sync.Mutex
}

func NewService(ctx context.Context) *Service {
	s := &Service{ctx: ctx, ttl: 5 * time.Minute}
	s.refreshAsync(false)
	s.refreshAsync(true)
	return s
}

func (s *Service) List(global bool) ([]SkillInfo, bool) {
	sc := s.scope(global)

	sc.mu.RLock()
	skills := sc.skills
	loaded := sc.loaded
	expired := loaded && time.Since(sc.updatedAt) > s.ttl
	sc.mu.RUnlock()

	if !loaded || expired {
		s.refreshAsync(global)
	}

	if skills == nil {
		return []SkillInfo{}, loaded
	}
	return skills, loaded
}

func (s *Service) Invalidate() {
	s.project.mu.Lock()
	s.project.updatedAt = time.Time{}
	s.project.mu.Unlock()

	s.global.mu.Lock()
	s.global.updatedAt = time.Time{}
	s.global.mu.Unlock()

	s.refreshAsync(false)
	s.refreshAsync(true)
}

// InstallProjectTools builds and installs quartet-cli, then installs every
// skill shipped by this Quartet checkout for all agents supported by the
// skills CLI. Only the repository-owned Make target can be executed; callers
// cannot supply commands or paths.
func (s *Service) InstallProjectTools(ctx context.Context) (*model.ProjectToolsInstallResult, error) {
	if !s.projectInstall.TryLock() {
		return nil, fmt.Errorf("Quartet CLI and project skills installation is already in progress")
	}
	defer s.projectInstall.Unlock()

	repoRoot, err := s.repositoryRoot()
	if err != nil {
		return nil, err
	}

	const command = "make install-project-tools"
	logger.Infof(ctx, "[skills] starting project tools install from %s", repoRoot)
	started := time.Now()
	cmdCtx, cancel := context.WithTimeout(ctx, 10*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "make", "install-project-tools")
	cmd.Dir = repoRoot
	cmd.Env = append(os.Environ(), "NO_COLOR=1", "FORCE_COLOR=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	runErr := cmd.Run()
	result := &model.ProjectToolsInstallResult{
		Command:    command,
		Output:     s.commandOutput(stdout.String(), stderr.String()),
		ExitCode:   -1,
		DurationMs: time.Since(started).Milliseconds(),
	}
	if cmd.ProcessState != nil {
		result.ExitCode = cmd.ProcessState.ExitCode()
	}
	if runErr != nil {
		cause := runErr
		if cmdCtx.Err() != nil {
			cause = cmdCtx.Err()
		}
		return result, fmt.Errorf("%s failed (exit code %d, duration %d ms): %v\n%s", command, result.ExitCode, result.DurationMs, cause, result.Output)
	}

	s.Invalidate()
	logger.Infof(ctx, "[skills] project tools install completed in %d ms", result.DurationMs)
	return result, nil
}

func (s *Service) commandOutput(stdout, stderr string) string {
	parts := make([]string, 0, 2)
	if text := strings.TrimSpace(stdout); text != "" {
		parts = append(parts, text)
	}
	if text := strings.TrimSpace(stderr); text != "" {
		parts = append(parts, "stderr:\n"+text)
	}
	if len(parts) == 0 {
		return "Command completed without output."
	}
	return strings.Join(parts, "\n\n")
}

func (s *Service) repositoryRoot() (string, error) {
	starts := make([]string, 0, 2)
	if wd, err := os.Getwd(); err == nil {
		starts = append(starts, wd)
	}
	if executable, err := os.Executable(); err == nil {
		starts = append(starts, filepath.Dir(executable))
	}

	for _, start := range starts {
		dir, err := filepath.Abs(start)
		if err != nil {
			continue
		}
		for {
			if s.isQuartetRepositoryRoot(dir) {
				return dir, nil
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}
	return "", fmt.Errorf("cannot locate the Quartet repository root containing Makefile, cmd/quartet-cli, and skill")
}

func (s *Service) isQuartetRepositoryRoot(dir string) bool {
	for _, relativePath := range []string{"Makefile", filepath.Join("cmd", "quartet-cli"), "skill"} {
		if _, err := os.Stat(filepath.Join(dir, relativePath)); err != nil {
			return false
		}
	}
	return true
}

func (s *Service) scope(global bool) *scopeCache {
	if global {
		return &s.global
	}
	return &s.project
}

func (s *Service) refreshAsync(global bool) {
	sc := s.scope(global)
	sc.mu.Lock()
	if sc.loading {
		sc.mu.Unlock()
		return
	}
	sc.loading = true
	sc.mu.Unlock()

	go func() {
		defer func() {
			sc.mu.Lock()
			sc.loading = false
			sc.mu.Unlock()
		}()
		s.refresh(global)
	}()
}

func (s *Service) refresh(global bool) {
	sc := s.scope(global)

	args := []string{"skills", "ls", "--json"}
	if global {
		args = append(args, "-g")
	}

	cmdCtx, cancel := context.WithTimeout(s.ctx, 30*time.Second)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "npx", args...)
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		logger.Warnf(s.ctx, "[skills] refresh failed (global=%v): %v: %s", global, err, stderr.String())
		return
	}

	var skills []SkillInfo
	if err := json.Unmarshal(stdout.Bytes(), &skills); err != nil {
		logger.Warnf(s.ctx, "[skills] parse failed (global=%v): %v", global, err)
		return
	}

	sc.mu.Lock()
	sc.skills = skills
	sc.updatedAt = time.Now()
	sc.loaded = true
	sc.mu.Unlock()
}
