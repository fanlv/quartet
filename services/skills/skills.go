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

// Scope identifies which skills-CLI scope a call targets.
//
// Global skills live under the user's home directory and are shared by every
// agent run. Project skills live under one directory — and the only directory
// that matters is the workspace an agent actually runs in, because ACP agents
// are spawned with the workspace workdir as their cwd. Dir must therefore be a
// workspace workdir for project scope; the backend process cwd is never used
// as an implicit fallback.
type Scope struct {
	Global bool
	Dir    string
}

func (sc Scope) valid() error {
	if !sc.Global && strings.TrimSpace(sc.Dir) == "" {
		return fmt.Errorf("project-scope skill operations require a workspace directory")
	}
	return nil
}

// cacheKey separates the single global cache from one cache per project dir.
func (sc Scope) cacheKey() string {
	if sc.Global {
		return "global"
	}
	return "project:" + sc.Dir
}

// dir returns the working directory the skills CLI should run in. Global
// operations always pass an explicit -g flag, so their cwd is irrelevant and
// left as the backend's own.
func (sc Scope) dir() string {
	if sc.Global {
		return ""
	}
	return sc.Dir
}

const (
	listTimeout           = 60 * time.Second
	addTimeout            = 5 * time.Minute
	removeTimeout         = 2 * time.Minute
	updateTimeout         = 10 * time.Minute
	findTimeout           = 60 * time.Second
	projectToolsTimeout   = 10 * time.Minute
	cacheTTL              = 5 * time.Minute
	refreshFailureBackoff = 15 * time.Second
)

type scopeCache struct {
	mu        sync.Mutex
	skills    []model.SkillInfo
	updatedAt time.Time
	loaded    bool
	loading   bool
	// lastErr keeps the full failure text of the most recent listing attempt so
	// the API can surface it instead of returning a silently empty list.
	lastErr  string
	failedAt time.Time
}

type Service struct {
	ctx context.Context

	cachesMu sync.Mutex
	caches   map[string]*scopeCache

	// mutate serializes every writing skills-CLI command (add / remove /
	// update / project-tools install). The CLI rewrites shared agent skill
	// directories and skills-lock.json, so concurrent writers clobber
	// each other.
	mutate sync.Mutex
}

func NewService(ctx context.Context) *Service {
	s := &Service{ctx: ctx, caches: make(map[string]*scopeCache)}
	// Only the global scope can be warmed at boot: project scopes are keyed on
	// a workspace directory that is not known until a request names one.
	s.refreshAsync(Scope{Global: true})
	return s
}

// List returns the cached skill listing for scope. ready reports whether a
// listing attempt has completed, so callers can tell "still loading" apart from
// "nothing installed"; errText carries the full text of the last failed attempt.
func (s *Service) List(scope Scope) (items []model.SkillInfo, ready bool, errText string) {
	if err := scope.valid(); err != nil {
		return []model.SkillInfo{}, true, err.Error()
	}
	sc := s.scope(scope)

	sc.mu.Lock()
	items = sc.skills
	loaded := sc.loaded
	errText = sc.lastErr
	expired := loaded && time.Since(sc.updatedAt) > cacheTTL
	cooling := errText != "" && time.Since(sc.failedAt) < refreshFailureBackoff
	sc.mu.Unlock()

	if (!loaded || expired) && !cooling {
		s.refreshAsync(scope)
	}
	if items == nil {
		items = []model.SkillInfo{}
	}
	// A recorded error is itself a completed attempt: without this the UI would
	// poll forever on a permanently broken skills CLI.
	return items, loaded || errText != "", errText
}

// Refresh re-reads the skill list for scope and blocks until done. Mutating
// endpoints use it so the list the UI reloads right after an install or
// uninstall already reflects the change.
func (s *Service) Refresh(ctx context.Context, scope Scope) error {
	if err := scope.valid(); err != nil {
		return err
	}
	return s.refresh(ctx, scope)
}

// Add installs a skill package into scope and returns the CLI output.
func (s *Service) Add(ctx context.Context, req model.SkillAddRequest, scope Scope) (string, error) {
	if err := scope.valid(); err != nil {
		return "", err
	}
	pkg := strings.TrimSpace(req.Package)
	if pkg == "" {
		return "", fmt.Errorf("package is required")
	}

	args := []string{"skills", "add", pkg, "-y"}
	if scope.Global {
		args = append(args, "-g")
	}
	for _, agent := range req.Agents {
		if agent = strings.TrimSpace(agent); agent != "" {
			args = append(args, "-a", agent)
		}
	}
	for _, skill := range req.Skills {
		if skill = strings.TrimSpace(skill); skill != "" {
			args = append(args, "-s", skill)
		}
	}
	return s.runMutation(ctx, scope, addTimeout, args)
}

// Remove uninstalls one skill from scope and returns the CLI output.
func (s *Service) Remove(ctx context.Context, name string, scope Scope) (string, error) {
	if err := scope.valid(); err != nil {
		return "", err
	}
	if name = strings.TrimSpace(name); name == "" {
		return "", fmt.Errorf("name is required")
	}

	args := []string{"skills", "remove", name, "-y"}
	if scope.Global {
		args = append(args, "-g")
	}
	return s.runMutation(ctx, scope, removeTimeout, args)
}

// Update upgrades every installed skill in scope to its latest version.
//
// The scope flag is always passed explicitly: left to auto-detect, the CLI
// picks project-if-any-else-global from the backend's own cwd and silently
// skips the other scope.
func (s *Service) Update(ctx context.Context, scope Scope) (string, error) {
	if err := scope.valid(); err != nil {
		return "", err
	}
	args := []string{"skills", "update", "-y"}
	if scope.Global {
		args = append(args, "-g")
	} else {
		args = append(args, "-p")
	}
	return s.runMutation(ctx, scope, updateTimeout, args)
}

// Find searches the public skills registry.
func (s *Service) Find(ctx context.Context, query string) ([]model.SkillFindResult, error) {
	if query = strings.TrimSpace(query); query == "" {
		return nil, fmt.Errorf("query is required")
	}

	stdout, stderr, err := s.runCLI(ctx, "", findTimeout, "skills", "find", query)
	results := parseFindOutput(stdout)
	if err != nil && len(results) == 0 {
		// `find` exits non-zero on an empty result set too, so only a failure
		// that also produced nothing parseable is worth reporting.
		return nil, fmt.Errorf("skills find failed: %v\n%s", err, combineOutput(stdout, stderr))
	}
	if results == nil {
		results = []model.SkillFindResult{}
	}
	return results, nil
}

// runMutation serializes a writing CLI command and refreshes the affected
// cache before returning, so the caller's follow-up List is never stale.
func (s *Service) runMutation(ctx context.Context, scope Scope, timeout time.Duration, args []string) (string, error) {
	s.mutate.Lock()
	defer s.mutate.Unlock()

	stdout, stderr, err := s.runCLI(ctx, scope.dir(), timeout, args...)
	output := combineOutput(stdout, stderr)
	if err != nil {
		return output, fmt.Errorf("%s failed: %v\n%s", strings.Join(args, " "), err, output)
	}
	if refreshErr := s.refresh(ctx, scope); refreshErr != nil {
		logger.Warnf(ctx, "[skills] refresh after %q failed: %v", strings.Join(args, " "), refreshErr)
	}
	return output, nil
}

// InstallProjectTools builds and installs quartet-cli, then installs every
// skill shipped by this Quartet checkout for all agents supported by the
// skills CLI. Only the repository-owned Make target can be executed; callers
// cannot supply commands or paths.
func (s *Service) InstallProjectTools(ctx context.Context) (*model.ProjectToolsInstallResult, error) {
	if !s.mutate.TryLock() {
		return nil, fmt.Errorf("another skill operation is already in progress")
	}
	defer s.mutate.Unlock()

	repoRoot, err := s.repositoryRoot()
	if err != nil {
		return nil, err
	}

	const command = "make install-project-tools"
	logger.Infof(ctx, "[skills] starting project tools install from %s", repoRoot)
	started := time.Now()
	cmdCtx, cancel := context.WithTimeout(ctx, projectToolsTimeout)
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

	// The Make target installs into the global scope; refresh it synchronously
	// so the caller's follow-up List already lists the new skills. Project
	// caches are left to their TTL — the target never touches them.
	if err := s.refresh(ctx, Scope{Global: true}); err != nil {
		logger.Warnf(ctx, "[skills] refresh after project tools install failed: %v", err)
	}
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

func (s *Service) scope(scope Scope) *scopeCache {
	key := scope.cacheKey()
	s.cachesMu.Lock()
	defer s.cachesMu.Unlock()
	sc, ok := s.caches[key]
	if !ok {
		sc = &scopeCache{}
		s.caches[key] = sc
	}
	return sc
}

func (s *Service) refreshAsync(scope Scope) {
	sc := s.scope(scope)
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
		if err := s.listInto(s.ctx, scope, sc); err != nil {
			logger.Warnf(s.ctx, "[skills] refresh failed (%s): %v", scope.cacheKey(), err)
		}
	}()
}

func (s *Service) refresh(ctx context.Context, scope Scope) error {
	sc := s.scope(scope)
	sc.mu.Lock()
	sc.loading = true
	sc.mu.Unlock()
	defer func() {
		sc.mu.Lock()
		sc.loading = false
		sc.mu.Unlock()
	}()
	return s.listInto(ctx, scope, sc)
}

func (s *Service) listInto(ctx context.Context, scope Scope, sc *scopeCache) error {
	args := []string{"skills", "ls", "--json"}
	if scope.Global {
		args = append(args, "-g")
	}

	stdout, stderr, err := s.runCLI(ctx, scope.dir(), listTimeout, args...)
	if err == nil {
		var items []model.SkillInfo
		if err = json.Unmarshal([]byte(jsonPayload(stdout)), &items); err == nil {
			sc.mu.Lock()
			sc.skills = items
			sc.updatedAt = time.Now()
			sc.loaded = true
			sc.lastErr = ""
			sc.failedAt = time.Time{}
			sc.mu.Unlock()
			return nil
		}
		err = fmt.Errorf("cannot parse `skills ls` output: %v\n%s", err, combineOutput(stdout, stderr))
	} else {
		err = fmt.Errorf("`%s` failed: %v\n%s", strings.Join(args, " "), err, combineOutput(stdout, stderr))
	}

	sc.mu.Lock()
	sc.lastErr = err.Error()
	sc.failedAt = time.Now()
	sc.mu.Unlock()
	return err
}

// runCLI executes the skills CLI through npx. stdin is left unset (/dev/null)
// on purpose: the CLI skips its interactive scope prompt when stdin is not a
// TTY, which keeps every command non-blocking.
func (s *Service) runCLI(ctx context.Context, dir string, timeout time.Duration, args ...string) (string, string, error) {
	cmdCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, "npx", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "NO_COLOR=1", "FORCE_COLOR=0")
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil && cmdCtx.Err() != nil {
		err = fmt.Errorf("%v (timed out after %s)", cmdCtx.Err(), timeout)
	}
	return stdout.String(), stderr.String(), err
}

// jsonPayload trims any non-JSON prologue (npm notices that leak onto stdout)
// so a stray warning line cannot turn a good listing into a parse failure.
func jsonPayload(raw string) string {
	trimmed := strings.TrimSpace(raw)
	if start := strings.IndexAny(trimmed, "[{"); start > 0 {
		return trimmed[start:]
	}
	return trimmed
}

func combineOutput(stdout, stderr string) string {
	parts := make([]string, 0, 2)
	if text := CleanTerminalOutput(stdout); text != "" {
		parts = append(parts, text)
	}
	if text := CleanTerminalOutput(stderr); text != "" {
		parts = append(parts, "stderr:\n"+text)
	}
	if len(parts) == 0 {
		return "(no output)"
	}
	return strings.Join(parts, "\n\n")
}
