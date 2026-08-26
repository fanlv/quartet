package usagestats

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
	typepath "github.com/fanlv/quartet/types/path"
)

// store owns the on-disk monthly-sharded files for usage stats. It keeps an
// in-memory cache of the most-recently-touched months (LRU), with dirty
// months flushed to disk through a debounced writer.
//
// Lifetime: one store per process. All public methods are safe for
// concurrent callers.
type store struct {
	mu sync.Mutex
	// flushMu serializes disk flushes so an older snapshot cannot finish after
	// and overwrite a newer one when explicit Flush / rollover flush / debounce
	// flush happen close together.
	flushMu sync.Mutex

	// months caches month-key -> file contents. month-key is "YYYY-MM"
	// (server local time). Bounded by maxCachedMonths via simple LRU
	// (`order` is most-recent at the back).
	months map[string]*MonthFile
	dirty  map[string]struct{}
	order  []string // LRU: oldest .. newest

	// flushPending is true when a flush is scheduled within the debounce
	// window. Reset in flushLocked after writing pending months.
	flushPending bool
	debounce     time.Duration

	// onDirty is invoked the first time a month is marked dirty after a
	// successful flush; the recorder uses it to schedule the debounced
	// flush goroutine.
	onDirty func()
}

const maxCachedMonths = 12

var errUnsupportedSchema = errors.New("unsupported usage stats schema")

// migrateLegacyUsageStats copies monthly files from the former
// quartet/data location into the persistent usage-stats directory. Existing
// destination files always win, and source files are retained so an
// interrupted rollout remains recoverable.
func migrateLegacyUsageStats() (int, error) {
	legacyDir, err := typepath.LegacyUsageStatsDir()
	if err != nil {
		return 0, err
	}
	destinationDir, err := typepath.UsageStatsDir()
	if err != nil {
		return 0, err
	}
	if filepath.Clean(legacyDir) == filepath.Clean(destinationDir) {
		return 0, nil
	}
	entries, err := os.ReadDir(legacyDir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return 0, nil
		}
		return 0, fmt.Errorf("read legacy directory %q failed: %w", legacyDir, err)
	}
	if err := os.MkdirAll(destinationDir, 0o755); err != nil {
		return 0, fmt.Errorf("create destination directory %q failed: %w", destinationDir, err)
	}

	copied := 0
	for _, entry := range entries {
		if entry.IsDir() || filepath.Ext(entry.Name()) != ".json" {
			continue
		}
		if _, err := time.Parse("2006-01", entry.Name()[:len(entry.Name())-len(".json")]); err != nil {
			continue
		}
		source := filepath.Join(legacyDir, entry.Name())
		destination := filepath.Join(destinationDir, entry.Name())
		if _, err := os.Stat(destination); err == nil {
			continue
		} else if !errors.Is(err, os.ErrNotExist) {
			return copied, fmt.Errorf("stat destination %q failed: %w", destination, err)
		}
		data, err := os.ReadFile(source)
		if err != nil {
			return copied, fmt.Errorf("read legacy month file %q failed: %w", source, err)
		}
		tmp, err := os.CreateTemp(destinationDir, ".usage-migration-*.tmp")
		if err != nil {
			return copied, fmt.Errorf("create migration temp file in %q failed: %w", destinationDir, err)
		}
		tmpName := tmp.Name()
		if _, err := tmp.Write(data); err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
			return copied, fmt.Errorf("write migration temp file %q failed: %w", tmpName, err)
		}
		if err := tmp.Chmod(0o644); err != nil {
			_ = tmp.Close()
			_ = os.Remove(tmpName)
			return copied, fmt.Errorf("chmod migration temp file %q failed: %w", tmpName, err)
		}
		if err := tmp.Close(); err != nil {
			_ = os.Remove(tmpName)
			return copied, fmt.Errorf("close migration temp file %q failed: %w", tmpName, err)
		}
		if err := os.Rename(tmpName, destination); err != nil {
			_ = os.Remove(tmpName)
			return copied, fmt.Errorf("rename migration temp file %q to %q failed: %w", tmpName, destination, err)
		}
		copied++
	}
	return copied, nil
}

func newStore() *store {
	return &store{
		months:   make(map[string]*MonthFile),
		dirty:    make(map[string]struct{}),
		debounce: time.Second,
	}
}

// monthKey returns the "YYYY-MM" key for a wall-clock time in local zone.
func monthKey(t time.Time) string {
	return t.Format("2006-01")
}

// dayKey returns the "YYYY-MM-DD" key.
func dayKey(t time.Time) string {
	return t.Format("2006-01-02")
}

// loadMonthLocked returns the in-memory MonthFile for the given month key.
// It loads from disk on miss. Missing files yield an empty (non-nil)
// MonthFile so callers can write into it without re-checking. Failed loads are
// deliberately not cached: Record drops the current best-effort snapshot, and
// the next call retries the disk read. This both lets transient IO errors heal
// and prevents an empty fallback from ever being written over a corrupt or
// future-schema file.
//
// Caller must hold s.mu.
func (s *store) loadMonthLocked(ctx context.Context, key string) (*MonthFile, error) {
	if mf, ok := s.months[key]; ok {
		s.touchOrderLocked(key)
		return mf, nil
	}

	mf, err := s.readMonthFile(ctx, key)
	if err != nil {
		return mf, err
	}
	s.months[key] = mf
	s.order = append(s.order, key)
	s.evictIfNeededLocked()
	return mf, nil
}

// loadMonthSnapshot returns a stable copy of one month for read-side
// aggregation. It deliberately keeps disk IO and JSON parsing outside s.mu so
// stats queries do not block Record() through slow filesystem work. If a writer
// populates the same month while the disk read is in progress, the fresher
// in-memory month wins and is cloned instead of the just-read file.
func (s *store) loadMonthSnapshot(ctx context.Context, key string) (*MonthFile, error) {
	s.mu.Lock()
	if mf, ok := s.months[key]; ok {
		s.touchOrderLocked(key)
		out := cloneMonthFile(mf)
		s.mu.Unlock()
		return out, nil
	}
	s.mu.Unlock()

	mf, err := s.readMonthFile(ctx, key)

	s.mu.Lock()
	defer s.mu.Unlock()
	if existing, ok := s.months[key]; ok {
		// A concurrent Record/load won the race while we were reading from disk.
		// Prefer the in-memory copy so pending debounced writes are visible.
		s.touchOrderLocked(key)
		return cloneMonthFile(existing), nil
	}
	if err != nil {
		return cloneMonthFile(mf), err
	}
	s.months[key] = mf
	s.order = append(s.order, key)
	s.evictIfNeededLocked()
	return cloneMonthFile(mf), nil
}

func (s *store) touchOrderLocked(key string) {
	for i, k := range s.order {
		if k == key {
			s.order = append(s.order[:i], s.order[i+1:]...)
			break
		}
	}
	s.order = append(s.order, key)
}

func (s *store) evictIfNeededLocked() {
	for len(s.order) > maxCachedMonths {
		victim := s.order[0]
		// Don't evict a dirty month — flush it first so we don't lose
		// pending writes.
		if _, isDirty := s.dirty[victim]; isDirty {
			break
		}
		s.order = s.order[1:]
		delete(s.months, victim)
	}
}

// readMonthFile loads YYYY-MM.json from disk. Missing files yield an empty
// MonthFile and nil error. Other read/parse errors yield an empty MonthFile
// plus the original error so HTTP read paths can expose the failure while the
// writer can still degrade best-effort.
func (s *store) readMonthFile(ctx context.Context, key string) (*MonthFile, error) {
	t, err := time.Parse("2006-01", key)
	if err != nil {
		logger.Warnf(ctx, "[usagestats] invalid month key %q: %v", key, err)
		return emptyMonthFile(), err
	}
	path, err := typepath.UsageStatsMonthFile(t)
	if err != nil {
		logger.Warnf(ctx, "[usagestats] resolve month file path failed: %v", err)
		return emptyMonthFile(), err
	}
	bs, err := os.ReadFile(path)
	if err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			logger.Warnf(ctx, "[usagestats] read month file %s failed: %v", path, err)
			return emptyMonthFile(), err
		}
		return emptyMonthFile(), nil
	}
	mf := &MonthFile{}
	if err := json.Unmarshal(bs, mf); err != nil {
		logger.Warnf(ctx, "[usagestats] parse month file %s failed: %v (treating as empty)", path, err)
		return emptyMonthFile(), err
	}
	if mf.SchemaVersion > currentSchemaVersion {
		err := fmt.Errorf("%w: month %s has schemaVersion %d (current %d)", errUnsupportedSchema, key, mf.SchemaVersion, currentSchemaVersion)
		logger.Warnf(ctx, "[usagestats] %v", err)
		return emptyMonthFile(), err
	}
	if mf.Workspaces == nil {
		mf.Workspaces = map[string]map[string]*DayBucket{}
	}
	upgradeMonthFile(mf)
	normalizeMonthFileTools(mf)
	return mf, nil
}

func emptyMonthFile() *MonthFile {
	return &MonthFile{
		SchemaVersion: currentSchemaVersion,
		Workspaces:    map[string]map[string]*DayBucket{},
	}
}

// upgradeMonthFile upgrades the decoded representation in memory. Pre-v2
// files used tokens.total for a local whole-history estimate. Copying it to the
// explicit legacy field preserves the API-facing Total while making its source
// distinguishable from provider-reported consumption. The upgraded marker is
// persisted when this month next receives a snapshot; until then each disk read
// starts from the original v1 document and performs the same idempotent in-memory
// projection.
func upgradeMonthFile(mf *MonthFile) {
	if mf == nil || mf.SchemaVersion >= currentSchemaVersion {
		return
	}
	for _, wsDays := range mf.Workspaces {
		for _, day := range wsDays {
			if day == nil {
				continue
			}
			upgradeLegacyTokenTotals(&day.Tokens)
			for _, model := range day.Models {
				if model != nil {
					upgradeLegacyTokenTotals(&model.Tokens)
				}
			}
		}
	}
	mf.SchemaVersion = currentSchemaVersion
}

func upgradeLegacyTokenTotals(tokens *TokenTotals) {
	if tokens == nil {
		return
	}
	tokens.LegacyTotal += tokens.Total
}

// writeMonthFile atomically replaces YYYY-MM.json. Caller must NOT hold
// s.mu; the file write is the slow part of flushLocked and we don't want
// to block readers.
func (s *store) writeMonthFile(ctx context.Context, key string, mf *MonthFile) error {
	t, err := time.Parse("2006-01", key)
	if err != nil {
		return fmt.Errorf("invalid month key %q: %w", key, err)
	}
	dir, err := typepath.UsageStatsDir()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	path, err := typepath.UsageStatsMonthFile(t)
	if err != nil {
		return err
	}
	// Defensive normalization at the persistence boundary. This protects disk
	// from both legacy month files loaded before migration and any in-process
	// cache that still carries pre-canonical tool keys.
	normalizeMonthFileTools(mf)
	bs, err := json.MarshalIndent(mf, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, bs, 0o644); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

func normalizeMonthFileTools(mf *MonthFile) {
	if mf == nil {
		return
	}
	for _, wsDays := range mf.Workspaces {
		for _, day := range wsDays {
			if day == nil {
				continue
			}
			normalizeToolBucketMap(&day.Tools)
			for _, mb := range day.Models {
				if mb == nil {
					continue
				}
				normalizeToolBucketMap(&mb.Tools)
			}
		}
	}
}

func normalizeToolBucketMap(m *map[string]*ToolBucket) {
	if m == nil || len(*m) == 0 {
		return
	}
	out := make(map[string]*ToolBucket, len(*m))
	for key, bucket := range *m {
		if bucket == nil {
			continue
		}
		canonicalKey := canonicalToolBucketKey(key)
		if existing, ok := out[canonicalKey]; ok {
			existing.Count += bucket.Count
			existing.TotalMs += bucket.TotalMs
			continue
		}
		cp := *bucket
		out[canonicalKey] = &cp
	}
	*m = out
}

// markDirty tags a month for the next flush and triggers the onDirty
// callback (the recorder uses it to schedule a debounced flush). Caller
// must hold s.mu.
func (s *store) markDirtyLocked(key string) {
	if _, ok := s.dirty[key]; ok {
		return
	}
	s.dirty[key] = struct{}{}
	if !s.flushPending && s.onDirty != nil {
		s.flushPending = true
		go s.onDirty()
	}
}

// snapshotDirty returns and clears the current dirty set, plus a copy of
// the corresponding MonthFile structs. Caller must hold s.mu.
func (s *store) snapshotDirtyLocked() map[string]*MonthFile {
	if len(s.dirty) == 0 {
		return nil
	}
	out := make(map[string]*MonthFile, len(s.dirty))
	for key := range s.dirty {
		mf := s.months[key]
		out[key] = cloneMonthFile(mf)
	}
	s.dirty = make(map[string]struct{})
	s.flushPending = false
	return out
}

// snapshotDirtyExceptLocked returns and clears dirty months except excludeKey.
// It is used on month rollover so the previous dirty month is written before
// the first snapshot for the new month is applied. Caller must hold s.mu.
func (s *store) snapshotDirtyExceptLocked(excludeKey string) map[string]*MonthFile {
	if len(s.dirty) == 0 {
		return nil
	}
	out := make(map[string]*MonthFile, len(s.dirty))
	for key := range s.dirty {
		if key == excludeKey {
			continue
		}
		mf := s.months[key]
		out[key] = cloneMonthFile(mf)
		delete(s.dirty, key)
	}
	if len(out) == 0 {
		return nil
	}
	if len(s.dirty) == 0 {
		// Any already-scheduled debounce goroutine may still wake up, but it
		// will find no dirty months. Resetting here lets a failed rollover flush
		// or a later Record schedule the next retry promptly.
		s.flushPending = false
	}
	return out
}

// flushNow synchronously writes all currently dirty months. Used at
// shutdown / from the debounce goroutine.
func (s *store) flushNow(ctx context.Context) {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()

	s.mu.Lock()
	pending := s.snapshotDirtyLocked()
	s.mu.Unlock()
	s.writePending(ctx, pending)
}

// flushDirtyExcept synchronously writes dirty months other than excludeKey.
// The recorder calls this before applying a snapshot to a different month so
// the old month's last debounced batch is durable at rollover time.
func (s *store) flushDirtyExcept(ctx context.Context, excludeKey string) {
	s.flushMu.Lock()
	defer s.flushMu.Unlock()

	s.mu.Lock()
	pending := s.snapshotDirtyExceptLocked(excludeKey)
	s.mu.Unlock()
	s.writePending(ctx, pending)
}

func (s *store) writePending(ctx context.Context, pending map[string]*MonthFile) {
	for key, mf := range pending {
		if err := s.writeMonthFile(ctx, key, mf); err != nil {
			logger.Warnf(ctx, "[usagestats] flush month %s failed: %v", key, err)
			// Re-mark dirty so a later flush retries.
			s.mu.Lock()
			s.markDirtyLocked(key)
			s.mu.Unlock()
		}
	}
}

// listMonthKeysInRange returns the sorted month keys ("YYYY-MM") covering
// the inclusive [from, to] day range.
func listMonthKeysInRange(from, to time.Time) []string {
	if from.After(to) {
		return nil
	}
	from = time.Date(from.Year(), from.Month(), 1, 0, 0, 0, 0, from.Location())
	to = time.Date(to.Year(), to.Month(), 1, 0, 0, 0, 0, to.Location())
	var out []string
	cur := from
	for !cur.After(to) {
		out = append(out, cur.Format("2006-01"))
		cur = cur.AddDate(0, 1, 0)
	}
	return out
}

// listExistingMonthKeys returns the month keys for which a file actually
// exists on disk (used by All-Time queries to avoid loading empty months).
func listExistingMonthKeys() ([]string, error) {
	dir, err := typepath.UsageStatsDir()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil, nil
		}
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		name := e.Name()
		if filepath.Ext(name) != ".json" {
			continue
		}
		key := name[:len(name)-len(".json")]
		if _, err := time.Parse("2006-01", key); err != nil {
			continue
		}
		out = append(out, key)
	}
	sort.Strings(out)
	return out, nil
}

// cloneMonthFile returns a deep copy. Used so a flush can write a stable
// snapshot without holding the store lock through the IO.
func cloneMonthFile(in *MonthFile) *MonthFile {
	if in == nil {
		return emptyMonthFile()
	}
	out := &MonthFile{
		SchemaVersion: in.SchemaVersion,
		Workspaces:    make(map[string]map[string]*DayBucket, len(in.Workspaces)),
	}
	if len(in.AppliedEvents) > 0 {
		out.AppliedEvents = make(map[string]bool, len(in.AppliedEvents))
		for eventID, applied := range in.AppliedEvents {
			out.AppliedEvents[eventID] = applied
		}
	}
	for ws, days := range in.Workspaces {
		dst := make(map[string]*DayBucket, len(days))
		for d, day := range days {
			dst[d] = cloneDayBucket(day)
		}
		out.Workspaces[ws] = dst
	}
	return out
}

func cloneDayBucket(in *DayBucket) *DayBucket {
	if in == nil {
		return nil
	}
	out := &DayBucket{SectionTotals: in.SectionTotals}
	if len(in.Tools) > 0 {
		out.Tools = make(map[string]*ToolBucket, len(in.Tools))
		for k, v := range in.Tools {
			cp := *v
			out.Tools[k] = &cp
		}
	}
	if len(in.Models) > 0 {
		out.Models = make(map[string]*ModelBucket, len(in.Models))
		for k, v := range in.Models {
			out.Models[k] = cloneModelBucket(v)
		}
	}
	return out
}

func cloneModelBucket(in *ModelBucket) *ModelBucket {
	if in == nil {
		return nil
	}
	out := &ModelBucket{SectionTotals: in.SectionTotals}
	if len(in.Tools) > 0 {
		out.Tools = make(map[string]*ToolBucket, len(in.Tools))
		for k, v := range in.Tools {
			cp := *v
			out.Tools[k] = &cp
		}
	}
	return out
}
