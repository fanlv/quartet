package usagestats

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// BackfillWorkspaceNames adds the current workspace title to legacy daily
// buckets that predate name persistence. Record calls and disk flushes are
// serialized with the migration so an older snapshot cannot overwrite it.
func (s *service) BackfillWorkspaceNames(ctx context.Context) error {
	if s.workspaceName == nil {
		return nil
	}

	s.recordMu.Lock()
	defer s.recordMu.Unlock()

	s.store.flushMu.Lock()
	defer s.store.flushMu.Unlock()

	keys, err := listExistingMonthKeys()
	if err != nil {
		return fmt.Errorf("list usage stats months: %w", err)
	}
	s.store.mu.Lock()
	seen := make(map[string]struct{}, len(keys)+len(s.store.months))
	for _, key := range keys {
		seen[key] = struct{}{}
	}
	for key := range s.store.months {
		if _, ok := seen[key]; !ok {
			keys = append(keys, key)
			seen[key] = struct{}{}
		}
	}
	s.store.mu.Unlock()
	sort.Strings(keys)

	resolvedNames := make(map[string]string)
	var failures []string
	for _, key := range keys {
		s.store.mu.Lock()
		mf, loadErr := s.store.loadMonthLocked(ctx, key)
		if loadErr != nil {
			s.store.mu.Unlock()
			failures = append(failures, fmt.Sprintf("load month %s: %v", key, loadErr))
			continue
		}
		changed := backfillMonthWorkspaceNames(mf, s.workspaceName, resolvedNames)
		snapshot := cloneMonthFile(mf)
		s.store.mu.Unlock()
		if !changed {
			continue
		}
		if writeErr := s.store.writeMonthFile(ctx, key, snapshot); writeErr != nil {
			failures = append(failures, fmt.Sprintf("write month %s: %v", key, writeErr))
			s.store.mu.Lock()
			s.store.markDirtyLocked(key)
			s.store.mu.Unlock()
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("backfill usage stats workspace names failed: %s", strings.Join(failures, "; "))
	}
	return nil
}

func backfillMonthWorkspaceNames(mf *MonthFile, resolve func(string) string, cache map[string]string) bool {
	if mf == nil || resolve == nil {
		return false
	}
	changed := false
	for workspaceID, days := range mf.Workspaces {
		workspaceName, resolved := cache[workspaceID]
		if !resolved {
			workspaceName = strings.TrimSpace(resolve(workspaceID))
			cache[workspaceID] = workspaceName
		}
		if workspaceName == "" {
			continue
		}
		for _, day := range days {
			if day == nil || strings.TrimSpace(day.WorkspaceName) != "" {
				continue
			}
			day.WorkspaceName = workspaceName
			changed = true
		}
	}
	return changed
}
