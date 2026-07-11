package probe

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/safe"
	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/types/model"
)

const acpProbeDiskRefreshInterval = 10 * time.Minute

// CacheService owns the persisted ACP selector snapshot and coordinates it
// with the process-wide live probe cache.
type CacheService struct {
	repo      repository.ACPProbeCacheRepo
	persistMu sync.Mutex
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
	snapshot, err := s.repo.Load(ctx)
	if err != nil {
		return nil, err
	}
	seedMemoryCache(snapshot)
	return snapshot, nil
}

// Warmup initializes the live cache from disk. A missing/empty snapshot makes
// startup block for one full probe and forces the first snapshot to disk;
// subsequent starts return after the disk read and refresh in the background.
func (s *CacheService) Warmup(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("initialize ACP probe cache failed: %w", err)
	}
	persisted, err := s.LoadPersisted(ctx)
	if err != nil {
		return fmt.Errorf("load persisted ACP cache before warmup failed: %w", err)
	}
	if len(persisted.Entries) > 0 {
		s.RefreshAsync(ctx)
		return nil
	}

	if !acpRefreshing.CompareAndSwap(false, true) {
		return fmt.Errorf("initialize empty ACP probe cache failed: refresh already in flight")
	}
	defer acpRefreshing.Store(false)
	logger.Infof(ctx, "[probe] persisted ACP cache is empty; blocking startup until initial probe completes")
	if err := s.refreshAndPersist(ctx, true); err != nil {
		return fmt.Errorf("initial ACP probe cache refresh failed: %w", err)
	}
	logger.Infof(ctx, "[probe] initial ACP session cache ready")
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

func (s *CacheService) refreshAndPersist(ctx context.Context, forcePersist bool) error {
	refreshACPSessionCache(ctx)
	return s.persistIfDue(ctx, forcePersist)
}

func (s *CacheService) persistIfDue(ctx context.Context, force bool) error {
	s.persistMu.Lock()
	defer s.persistMu.Unlock()

	persisted, err := s.repo.Load(ctx)
	if err != nil {
		return err
	}
	now := time.Now()
	if !force && persisted.RefreshedAt > 0 {
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
		return err
	}
	logger.Infof(ctx, "[probe] persisted ACP session cache: agents=%d refreshedAt=%s",
		len(snapshot.Entries), now.Format(time.RFC3339))
	return nil
}

func seedMemoryCache(snapshot *model.ACPProbeCacheSnapshot) {
	if snapshot == nil || len(snapshot.Entries) == 0 {
		return
	}
	acpSessionCacheMu.Lock()
	defer acpSessionCacheMu.Unlock()
	for command, persisted := range snapshot.Entries {
		if acpSessionCache[command] != nil {
			continue
		}
		modelID := currentModelID(persisted.Models)
		thoughtLevelsByModel := make(map[string]*model.SessionThoughtLevelState)
		if modelID != "" {
			thoughtLevelsByModel[modelID] = cloneSessionThoughtLevelState(persisted.ThoughtLevels)
		}
		acpSessionCache[command] = &acpSessionInfoCache{
			models:               cloneSessionModelState(persisted.Models),
			modes:                cloneSessionModeState(persisted.Modes),
			thoughtLevelsByModel: thoughtLevelsByModel,
		}
	}
}

func snapshotMemoryCache(refreshedAt time.Time) *model.ACPProbeCacheSnapshot {
	acpSessionCacheMu.RLock()
	defer acpSessionCacheMu.RUnlock()
	entries := make(map[string]model.ACPProbeCacheEntry, len(acpSessionCache))
	for command, cached := range acpSessionCache {
		if cached == nil {
			continue
		}
		modelID := currentModelID(cached.models)
		entries[command] = model.ACPProbeCacheEntry{
			Models:        cloneSessionModelState(cached.models),
			Modes:         cloneSessionModeState(cached.modes),
			ThoughtLevels: cloneSessionThoughtLevelState(cached.thoughtLevelsByModel[modelID]),
		}
	}
	return &model.ACPProbeCacheSnapshot{
		Version:     model.ACPProbeCacheVersion,
		RefreshedAt: refreshedAt.UnixMilli(),
		Entries:     entries,
	}
}
