package usagestats

import (
	"context"
	"strings"
	"sync"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
)

// Recorder writes accumulated step snapshots into the store.
//
// Persistence is asynchronous-debounced within a month: the snapshot is
// applied to the in-memory month file synchronously (so a follow-up read on
// the same process sees it), and a flush is scheduled to run after a short
// window. Multiple Records that land in the same window collapse into one disk
// write. On month rollover, dirty old-month data is synchronously flushed
// before applying the new-month snapshot. Crashes within the debounce window
// can still lose recent same-month step records — by design (statistics is
// best-effort, never a job-execution blocker).
type Recorder interface {
	Record(snap Snapshot)
}

// Reader serves both the per-day totals (Job List header) and the range
// aggregates (stats page).
type Reader interface {
	// GetDailyTotals returns total/turn/etc stats for the given workspace
	// across the requested days. Days with no data are simply absent from
	// the returned map.
	GetDailyTotals(workspaceID string, days []time.Time) (map[string]DailyTotals, error)

	// GetUsage aggregates across the inclusive [from, to] date range.
	// from/to are interpreted in the server's local timezone; either may
	// be zero to mean unbounded (use earliest / latest existing data).
	GetUsage(from, to time.Time) (UsageReport, error)

	// GetUsageForWorkspaces aggregates across the inclusive [from, to] date
	// range, but only includes workspace ids present in workspaceIDs. An empty
	// workspaceIDs slice intentionally returns an empty report for the normalized
	// range; callers that want unfiltered aggregation should use GetUsage.
	GetUsageForWorkspaces(from, to time.Time, workspaceIDs []string) (UsageReport, error)
}

// DailyTotals is the compact value returned by GetDailyTotals: total
// duration and turn count for one (workspace, day). Other fields are
// available via GetUsage.
type DailyTotals struct {
	TotalMs   int64 `json:"totalMs"`
	TurnCount int   `json:"turnCount"`
}

// UsageReport is the response to GetUsage. The same struct backs the HTTP
// stats endpoint.
type UsageReport struct {
	From        string               `json:"from"`
	To          string               `json:"to"`
	ByWorkspace []WorkspaceAggregate `json:"byWorkspace"`
	ByModel     []ModelAggregate     `json:"byModel"`
	ByTool      []ToolAggregate      `json:"byTool"`
	Daily       []DailyAggregate     `json:"daily"`
}

type WorkspaceAggregate struct {
	WorkspaceID   string   `json:"workspaceId"`
	WorkspaceName string   `json:"workspaceName,omitempty"`
	WorkspaceIDs  []string `json:"-"`
	SectionTotals
}

type ModelAggregate struct {
	ModelID string `json:"modelId"`
	SectionTotals
}

type ToolAggregate struct {
	ToolKey string `json:"toolKey"`
	Count   int    `json:"count"`
	TotalMs int64  `json:"totalMs"`
}

type DailyAggregate struct {
	Date string `json:"date"`
	SectionTotals
	Models map[string]SectionTotals `json:"models,omitempty"`
}

// Service is the public face of the package: a single object that
// implements both Recorder and Reader. Construct with NewService.
type Service interface {
	Recorder
	Reader

	// BackfillWorkspaceNames persists names for legacy daily buckets that only
	// contain a workspace ID. Existing historical names are never overwritten.
	BackfillWorkspaceNames(ctx context.Context) error

	// MigrateModelIDs rewrites historical per-model buckets according to the
	// supplied aliases. When both the old and canonical IDs exist in one day,
	// their counters are merged. Day-level totals are unchanged.
	MigrateModelIDs(ctx context.Context, aliases map[string]string) error

	// Flush blocks until any pending writes are durable. Used at shutdown
	// to avoid losing the last debounced batch.
	Flush(ctx context.Context)

	// Version returns a monotonically increasing fingerprint of the in-memory
	// stats state. Bumped on every successful Record call. Callers (e.g. the
	// Job List ETag) fold this into their cache key so a 304 short-circuit
	// cannot hide a freshly recorded snapshot from clients.
	Version() uint64
}

type service struct {
	// recordMu serializes Record calls so rollover flushes cannot be bypassed by
	// another concurrent snapshot for the old month.
	recordMu sync.Mutex
	store    *store
	// rootCtx is used for warn logs from the background flush goroutine.
	// Not for cancellation of in-flight writes — those are best-effort.
	rootCtx context.Context
	// workspaceName resolves the current display name at record time. Keeping
	// the name in the historical bucket lets copied/synced stats remain readable
	// on another machine where the same logical workspace has a different ID.
	workspaceName func(workspaceID string) string
	// nowFn lets tests pin the clock.
	nowFn func() time.Time
	// version is bumped on every Record under store.mu so readers picking
	// it up after a Record observe the new value.
	version uint64
	// lastRecordMonth tracks the last month a Record wrote. Protected by store.mu.
	lastRecordMonth string
}

// NewService constructs a usage-stats service with the default disk-backed
// store and a 1s debounce. workspaceName is optional; when present, its result
// is persisted with each daily workspace bucket.
func NewService(rootCtx context.Context, workspaceName func(workspaceID string) string) (Service, error) {
	if rootCtx == nil {
		rootCtx = context.Background()
	}
	st := newStore()
	s := &service{
		store:         st,
		rootCtx:       rootCtx,
		workspaceName: workspaceName,
		nowFn:         time.Now,
	}
	st.onDirty = func() {
		// Sleep then flush. The flag is reset inside flushNow via
		// snapshotDirtyLocked; while we sleep, additional Record calls
		// won't re-arm because flushPending stays true.
		time.Sleep(st.debounce)
		st.flushNow(s.rootCtx)
	}
	return s, nil
}

// Record applies one step's stats into the in-memory month file and marks
// the month dirty. All field merges are additive (no field is ever
// decremented or replaced). A nil-ish snapshot (zero workspace + zero
// duration + zero counts) is silently dropped.
func (s *service) Record(snap Snapshot) {
	if snap.WorkspaceID == "" {
		// We refuse to bucket without a workspace; this would otherwise
		// produce an empty-string key that pollutes the by-workspace
		// table. Log so a bug surfaces, but don't fail the caller.
		logger.Warnf(s.rootCtx, "[usagestats] dropping snapshot with empty workspaceID")
		return
	}
	if snap.EventID == "" {
		// Without an event id the same completion could be counted twice on a
		// retry or replay. Refuse rather than inflate the numbers.
		logger.Warnf(s.rootCtx, "[usagestats] dropping snapshot with empty eventID: workspace=%s", snap.WorkspaceID)
		return
	}
	if snap.FinishedAtMs <= 0 {
		snap.FinishedAtMs = s.nowFn().UnixMilli()
	}
	snap.WorkspaceName = strings.TrimSpace(snap.WorkspaceName)
	if snap.WorkspaceName == "" && s.workspaceName != nil {
		snap.WorkspaceName = strings.TrimSpace(s.workspaceName(snap.WorkspaceID))
	}

	finishedAt := time.UnixMilli(snap.FinishedAtMs)
	mKey := monthKey(finishedAt)
	dKey := dayKey(finishedAt)

	s.recordMu.Lock()
	defer s.recordMu.Unlock()

	s.store.mu.Lock()
	rollover := s.lastRecordMonth != "" && s.lastRecordMonth != mKey
	s.store.mu.Unlock()
	if rollover {
		// Month rollover has a stronger durability requirement than regular
		// same-month writes: before writing the first snapshot for a different month,
		// flush any already-dirty old month once. The flush keeps IO outside store.mu
		// but is serialized with other flushes so an older disk snapshot cannot race
		// and overwrite a newer one.
		s.store.flushDirtyExcept(s.rootCtx, mKey)
	}

	s.store.mu.Lock()
	defer s.store.mu.Unlock()

	mf, err := s.store.loadMonthLocked(s.rootCtx, mKey)
	if err != nil {
		logger.Warnf(s.rootCtx, "[usagestats] load month %s before record failed: %v", mKey, err)
		// A failed read returns an empty in-memory value for query resilience.
		// Never write that value back over an existing unreadable/corrupt/future
		// file; dropping one best-effort snapshot is safer than destroying data
		// that may still be recoverable.
		return
	}
	if mf.AppliedEvents[snap.EventID] {
		return
	}
	if mf.AppliedEvents == nil {
		mf.AppliedEvents = make(map[string]bool)
	}
	wsDays, ok := mf.Workspaces[snap.WorkspaceID]
	if !ok {
		wsDays = make(map[string]*DayBucket)
		mf.Workspaces[snap.WorkspaceID] = wsDays
	}
	day, ok := wsDays[dKey]
	if !ok {
		day = &DayBucket{}
		wsDays[dKey] = day
	}
	if snap.WorkspaceName != "" {
		day.WorkspaceName = snap.WorkspaceName
	}

	applySnapshotToBucket(snap, &day.SectionTotals)
	mergeToolsInto(&day.Tools, snap.Tools)

	if snap.ModelID != "" {
		if day.Models == nil {
			day.Models = make(map[string]*ModelBucket)
		}
		mb, ok := day.Models[snap.ModelID]
		if !ok {
			mb = &ModelBucket{}
			day.Models[snap.ModelID] = mb
		}
		applySnapshotToBucket(snap, &mb.SectionTotals)
		mergeToolsInto(&mb.Tools, snap.Tools)
	}
	mf.AppliedEvents[snap.EventID] = true
	mf.SchemaVersion = currentSchemaVersion

	s.store.markDirtyLocked(mKey)
	s.lastRecordMonth = mKey
	s.version++
}

func applySnapshotToBucket(snap Snapshot, dst *SectionTotals) {
	dst.TotalMs += snap.DurationMs
	dst.TurnCount++
	dst.AssistantCount += snap.AssistantCount
	dst.ThoughtCount += snap.ThoughtCount
	dst.ToolCallCount += snap.ToolCallCount
	dst.Tokens.Total += snap.Tokens.Total
	dst.Tokens.Reported += snap.Tokens.Reported
	dst.Tokens.Input += snap.Tokens.Input
	dst.Tokens.Output += snap.Tokens.Output
	dst.Tokens.CachedRead += snap.Tokens.CachedRead
	dst.Tokens.CachedWrite += snap.Tokens.CachedWrite
	dst.Tokens.Reasoning += snap.Tokens.Reasoning
	dst.Tokens.ImageEstimate += snap.Tokens.ImageEstimate
	dst.Tokens.Estimated += snap.Tokens.Estimated
	dst.Tokens.ReportedTurns += snap.Tokens.ReportedTurns
	dst.Tokens.EstimatedTurns += snap.Tokens.EstimatedTurns
	dst.Tokens.Assistant += snap.Tokens.Assistant
	dst.Tokens.Thought += snap.Tokens.Thought
	dst.Tokens.ToolCall += snap.Tokens.ToolCall
}

func addTokenTotals(dst, src *TokenTotals) {
	if dst == nil || src == nil {
		return
	}
	dst.Total += src.Total
	dst.Reported += src.Reported
	dst.Input += src.Input
	dst.Output += src.Output
	dst.CachedRead += src.CachedRead
	dst.CachedWrite += src.CachedWrite
	dst.Reasoning += src.Reasoning
	dst.ImageEstimate += src.ImageEstimate
	dst.Estimated += src.Estimated
	dst.ReportedTurns += src.ReportedTurns
	dst.EstimatedTurns += src.EstimatedTurns
	dst.Assistant += src.Assistant
	dst.Thought += src.Thought
	dst.ToolCall += src.ToolCall
}

func mergeToolsInto(dst *map[string]*ToolBucket, src map[string]ToolBucket) {
	if len(src) == 0 {
		return
	}
	if *dst == nil {
		*dst = make(map[string]*ToolBucket, len(src))
	}
	for k, v := range src {
		bucketKey := canonicalToolBucketKey(k)
		existing, ok := (*dst)[bucketKey]
		if !ok {
			cp := v
			(*dst)[bucketKey] = &cp
			continue
		}
		existing.Count += v.Count
		existing.TotalMs += v.TotalMs
	}
}

// Flush blocks the caller until pending writes are durable. Idempotent.
func (s *service) Flush(ctx context.Context) {
	s.store.flushNow(ctx)
}

// Version returns the in-memory record counter. Read under store.mu so the
// value is consistent with whatever a concurrent reader observes.
func (s *service) Version() uint64 {
	s.store.mu.Lock()
	defer s.store.mu.Unlock()
	return s.version
}
