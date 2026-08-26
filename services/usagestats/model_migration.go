package usagestats

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// MigrateModelIDs replaces legacy model bucket keys with canonical model IDs
// in every persisted month. It also updates cached months, so callers may run
// it while the service is live without a later debounced flush restoring the
// old keys. Record calls are serialized with the migration.
func (s *service) MigrateModelIDs(ctx context.Context, aliases map[string]string) error {
	canonical := canonicalModelAliases(aliases)
	if len(canonical) == 0 {
		return nil
	}

	s.recordMu.Lock()
	defer s.recordMu.Unlock()

	// A flush that already captured a pre-migration snapshot must finish first,
	// otherwise it could overwrite a migrated file after this method returns.
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

	var failures []string
	for _, key := range keys {
		s.store.mu.Lock()
		mf, loadErr := s.store.loadMonthLocked(ctx, key)
		if loadErr != nil {
			s.store.mu.Unlock()
			failures = append(failures, fmt.Sprintf("load month %s: %v", key, loadErr))
			continue
		}
		changed := migrateMonthModelIDs(mf, canonical)
		snapshot := cloneMonthFile(mf)
		s.store.mu.Unlock()
		if !changed {
			continue
		}
		if writeErr := s.store.writeMonthFile(ctx, key, snapshot); writeErr != nil {
			failures = append(failures, fmt.Sprintf("write month %s: %v", key, writeErr))
			// The cache already contains the canonical form. Mark it dirty so a
			// later regular flush gets another chance to persist the migration.
			s.store.mu.Lock()
			s.store.markDirtyLocked(key)
			s.store.mu.Unlock()
		}
	}
	if len(failures) > 0 {
		return fmt.Errorf("migrate usage stats model IDs failed: %s", strings.Join(failures, "; "))
	}
	return nil
}

func canonicalModelAliases(aliases map[string]string) map[string]string {
	out := make(map[string]string, len(aliases))
	for from, to := range aliases {
		from = strings.TrimSpace(from)
		to = strings.TrimSpace(to)
		if from == "" || to == "" || from == to {
			continue
		}
		out[from] = to
	}
	return out
}

func migrateMonthModelIDs(mf *MonthFile, aliases map[string]string) bool {
	if mf == nil || len(aliases) == 0 {
		return false
	}
	changed := false
	for _, days := range mf.Workspaces {
		for _, day := range days {
			if day == nil || len(day.Models) == 0 {
				continue
			}
			for oldID, canonicalID := range aliases {
				source, ok := day.Models[oldID]
				if !ok {
					continue
				}
				if target := day.Models[canonicalID]; target != nil {
					mergeModelBucket(target, source)
				} else {
					day.Models[canonicalID] = source
				}
				delete(day.Models, oldID)
				changed = true
			}
		}
	}
	return changed
}

func mergeModelBucket(dst, src *ModelBucket) {
	if dst == nil || src == nil {
		return
	}
	addSection(&dst.SectionTotals, &src.SectionTotals)
	if len(src.Tools) == 0 {
		return
	}
	if dst.Tools == nil {
		dst.Tools = make(map[string]*ToolBucket, len(src.Tools))
	}
	for key, source := range src.Tools {
		if source == nil {
			continue
		}
		canonicalKey := canonicalToolBucketKey(key)
		if target := dst.Tools[canonicalKey]; target != nil {
			target.Count += source.Count
			target.TotalMs += source.TotalMs
			continue
		}
		copy := *source
		dst.Tools[canonicalKey] = &copy
	}
}
