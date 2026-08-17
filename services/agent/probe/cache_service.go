package probe

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/safe"
	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/services/agent/catalog"
	"github.com/fanlv/quartet/types/model"
)

const acpProbeDiskRefreshInterval = 10 * time.Minute

// CacheService owns the persisted ACP selector snapshot and coordinates it
// with the process-wide live probe cache.
type CacheService struct {
	repo           repository.ACPProbeCacheRepo
	persistMu      sync.Mutex
	persistPending atomic.Bool
}

func (s *CacheService) InvalidateAgent(agentID string) {
	agentID = strings.TrimSpace(agentID)
	if agentID == "" {
		return
	}
	prefix := agentID + "@"
	acpSessionCacheMu.Lock()
	for key := range acpSessionCache {
		if key == agentID || strings.HasPrefix(key, prefix) {
			delete(acpSessionCache, key)
		}
	}
	acpSessionCacheMu.Unlock()
	acpValidationCacheMu.Lock()
	for key, entry := range acpValidationCache {
		if entry.AgentID == agentID || strings.HasPrefix(key, prefix) {
			delete(acpValidationCache, key)
		}
	}
	acpValidationCacheMu.Unlock()
	acpProbeFailuresMu.Lock()
	for key := range acpProbeFailures {
		if key == agentID || strings.HasPrefix(key, prefix) {
			delete(acpProbeFailures, key)
		}
	}
	acpProbeFailuresMu.Unlock()
}

func (s *CacheService) InvalidateBinding(agentID, revision string) {
	runtimeKey := catalog.RuntimeKey(strings.TrimSpace(agentID), strings.TrimSpace(revision))
	if agentID == "" || revision == "" {
		return
	}
	acpSessionCacheMu.Lock()
	delete(acpSessionCache, runtimeKey)
	acpSessionCacheMu.Unlock()
	acpValidationCacheMu.Lock()
	delete(acpValidationCache, runtimeKey)
	acpValidationCacheMu.Unlock()
	acpProbeFailuresMu.Lock()
	delete(acpProbeFailures, runtimeKey)
	acpProbeFailuresMu.Unlock()
}

func (s *CacheService) InvalidateAgentAndPersist(ctx context.Context, agentID string) error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	s.InvalidateAgent(agentID)
	return s.persistLocked(ctx, true)
}

func (s *CacheService) InvalidateBindingsAndPersist(
	ctx context.Context,
	bindings []model.AgentRuntimeBinding,
) error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	for _, binding := range bindings {
		s.InvalidateBinding(binding.AgentID, binding.Revision)
	}
	return s.persistLocked(ctx, true)
}

func NewCacheService() (*CacheService, error) {
	repo, err := repository.NewACPProbeCacheRepo()
	if err != nil {
		return nil, fmt.Errorf("create ACP probe cache repository failed: %w", err)
	}
	return &CacheService{repo: repo}, nil
}

// LoadPersisted reads the disk snapshot for the current request. Missing
// in-memory entries are seeded from it so a later live refresh merges rather
// than discards the last known-good value when an agent is temporarily down.
func (s *CacheService) LoadPersisted(ctx context.Context) (*model.ACPProbeCacheSnapshot, error) {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	snapshot, err := s.repo.Load(ctx)
	if err != nil {
		return nil, err
	}
	if !s.persistPending.Load() {
		seedMemoryCache(snapshot)
	}
	return snapshot, nil
}

// Warmup initializes the live cache from disk and always refreshes in the
// background. An empty or schema-reset snapshot must not delay HTTP readiness;
// callers see pending_validation until the first refresh finishes.
func (s *CacheService) Warmup(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("initialize ACP probe cache failed: %w", err)
	}
	persisted, err := s.LoadPersisted(ctx)
	if err != nil {
		return fmt.Errorf("load persisted ACP cache before warmup failed: %w", err)
	}
	if len(persisted.Entries) == 0 {
		logger.Infof(ctx, "[probe] persisted ACP cache is empty; agents remain pending while the initial refresh runs asynchronously")
	}
	s.RefreshAsync(ctx)
	return nil
}

// RefreshAsync attempts a live refresh on every request. Concurrent requests
// coalesce behind acpRefreshing; once the current refresh finishes, the next
// request starts another one.
func (s *CacheService) RefreshAsync(ctx context.Context) {
	if !acpRefreshing.CompareAndSwap(false, true) {
		return
	}
	bgCtx := context.WithoutCancel(ctx)
	safe.Go(bgCtx, func() {
		defer acpRefreshing.Store(false)
		if err := s.refreshAndPersist(bgCtx, false); err != nil {
			logger.Errorf(bgCtx, "[probe] refresh and persist ACP session cache failed: %v", err)
		}
	})
}

func (s *CacheService) ValidateBindingAsync(
	ctx context.Context,
	binding model.AgentRuntimeBinding,
	envVersion int64,
	done func(),
) {
	bgCtx := context.WithoutCancel(ctx)
	safe.Go(bgCtx, func() {
		if done != nil {
			defer done()
		}
		if _, err := ValidateBinding(bgCtx, binding, envVersion, nil); err != nil {
			logger.Warnf(
				bgCtx,
				"[probe] async Agent validation failed: agentId=%s revision=%s err=%v",
				binding.AgentID,
				binding.Revision,
				err,
			)
		}
		if err := s.PersistNow(bgCtx); err != nil {
			logger.Errorf(
				bgCtx,
				"[probe] persist async Agent validation failed: agentId=%s revision=%s err=%v",
				binding.AgentID,
				binding.Revision,
				err,
			)
		}
	})
}

// PersistNow forces the current in-memory selector cache to disk, bypassing
// the disk-write throttle. Used after an explicit install+validation so the
// freshly probed result survives a restart.
func (s *CacheService) PersistNow(ctx context.Context) error {
	return s.persistIfDue(ctx, true)
}

func (s *CacheService) PersistPending() bool {
	return s.persistPending.Load()
}

func (s *CacheService) refreshAndPersist(ctx context.Context, forcePersist bool) error {
	refreshACPSessionCache(ctx)
	return s.persistIfDue(ctx, forcePersist)
}

func (s *CacheService) persistIfDue(ctx context.Context, force bool) error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()
	return s.persistLocked(ctx, force)
}

func (s *CacheService) persistLocked(ctx context.Context, force bool) error {
	persisted, err := s.repo.Load(ctx)
	if err != nil {
		s.persistPending.Store(true)
		return err
	}
	now := time.Now()
	if !force && !s.persistPending.Load() && persisted.RefreshedAt > 0 {
		lastRefresh := time.UnixMilli(persisted.RefreshedAt)
		if !lastRefresh.After(now) && now.Sub(lastRefresh) < acpProbeDiskRefreshInterval {
			return nil
		}
		if lastRefresh.After(now) {
			return nil
		}
	}

	snapshot := snapshotMemoryCache(now)
	if err := s.repo.Save(ctx, snapshot); err != nil {
		s.persistPending.Store(true)
		return err
	}
	s.persistPending.Store(false)
	logger.Infof(ctx, "[probe] persisted ACP session cache: agents=%d refreshedAt=%s",
		len(snapshot.Entries), now.Format(time.RFC3339))
	return nil
}

func seedMemoryCache(snapshot *model.ACPProbeCacheSnapshot) {
	if snapshot == nil || len(snapshot.Entries) == 0 {
		return
	}
	acpSessionCacheMu.Lock()
	acpValidationCacheMu.Lock()
	defer acpSessionCacheMu.Unlock()
	defer acpValidationCacheMu.Unlock()
	for key, persisted := range snapshot.Entries {
		_, validationExists := acpValidationCache[key]
		if !validationExists {
			acpValidationCache[key] = persisted
		}
		if validationExists || !persisted.Success ||
			persisted.RuntimeKey == "" || acpSessionCache[persisted.RuntimeKey] != nil {
			continue
		}
		modelID := currentModelID(persisted.Models)
		thoughtLevelsByModel := make(map[string]*model.SessionThoughtLevelState)
		if modelID != "" {
			thoughtLevelsByModel[modelID] = cloneSessionThoughtLevelState(persisted.ThoughtLevels)
		}
		acpSessionCache[persisted.RuntimeKey] = &acpSessionInfoCache{
			models:               cloneSessionModelState(persisted.Models),
			modes:                cloneSessionModeState(persisted.Modes),
			thoughtLevelsByModel: thoughtLevelsByModel,
		}
	}
}

func snapshotMemoryCache(refreshedAt time.Time) *model.ACPProbeCacheSnapshot {
	acpValidationCacheMu.RLock()
	defer acpValidationCacheMu.RUnlock()
	entries := make(map[string]model.ACPProbeCacheEntry, len(acpValidationCache))
	for key, entry := range acpValidationCache {
		entry.Refreshing = false
		entry.Models = cloneSessionModelState(entry.Models)
		entry.Modes = cloneSessionModeState(entry.Modes)
		entry.ThoughtLevels = cloneSessionThoughtLevelState(entry.ThoughtLevels)
		entries[key] = entry
	}
	return &model.ACPProbeCacheSnapshot{
		Version:     model.ACPProbeCacheVersion,
		RefreshedAt: refreshedAt.UnixMilli(),
		Entries:     entries,
	}
}
