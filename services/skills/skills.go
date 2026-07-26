package skills

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"sync"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
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
	ctx     context.Context
	project scopeCache
	global  scopeCache
	ttl     time.Duration
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
