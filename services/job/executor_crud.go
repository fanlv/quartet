package job

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/fanlv/quartet/pkg/fileserver"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
	typepath "github.com/fanlv/quartet/types/path"
)

// CRUD and listing operations on the in-memory job map plus the on-disk
// repository. Lock ordering follows the rules documented on serviceImpl in
// executor_store.go: persist shard before s.mu when both are needed.

func (s *serviceImpl) Create(job *model.Job) error {
	// Establish the "Progress is never nil in s.jobs" invariant at the only
	// other entry point besides load(). Subsequent code paths (Start,
	// Continue, SendMessage, recordIterationResult, ...) can then dereference
	// Progress without a per-call nil guard. Done on the input pointer first
	// so the caller's local reference also sees the same shape we serialise
	// to disk and store in s.jobs.
	ensureProgress(job)
	// DeepCopy after ensureProgress so the in-memory map and the handler's
	// local reference are not aliased — same convention as Get(), which also
	// returns a copy. The handler-side ownership protocol (see struct doc)
	// keeps targeted mutators safe even with aliasing, but copying here
	// removes a fragile invariant that a future "read job.X right after
	// Create" addition could break.
	cp := job.DeepCopy()
	repo, err := s.getOrCreateRepo(cp.WorkspaceID)
	if err != nil {
		return fmt.Errorf("get repo for workspace %s failed: %w", cp.WorkspaceID, err)
	}
	if err := repo.Save(cp.ID, cp); err != nil {
		return err
	}
	s.store(cp.ID, cp)
	s.bumpListVersion(cp.WorkspaceID)
	return nil
}

func (s *serviceImpl) Get(jobID string) (*model.Job, bool) {
	s.mu.RLock()
	j, ok := s.jobs[jobID]
	if !ok {
		s.mu.RUnlock()
		return nil, false
	}
	cp := j.DeepCopy()
	s.mu.RUnlock()
	// Synthesize the runtime-only graceful-stop pending flag onto the snapshot
	// so a refresh / second tab can restore the "stop after step" affordance.
	// It is never persisted (see JobProgress.GracefulStopPending) — read it
	// from the live map at snapshot time instead.
	if cp.Progress != nil {
		cp.Progress.GracefulStopPending = s.isGracefulStopPending(jobID)
	}
	return cp, true
}

func (s *serviceImpl) GetWithSnapshotSeq(jobID string) (*model.Job, uint64, bool) {
	seq := s.SnapshotSeq(jobID)
	job, ok := s.Get(jobID)
	if !ok {
		return nil, 0, false
	}
	return job, seq, true
}

func (s *serviceImpl) List() []*model.Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	jobs := make([]*model.Job, 0, len(s.jobs))
	for _, j := range s.jobs {
		if !j.Deleted {
			jobs = append(jobs, j.DeepCopy())
		}
	}
	return jobs
}

func (s *serviceImpl) ListByWorkspace(wsID string) []*model.Job {
	s.mu.RLock()
	defer s.mu.RUnlock()
	var jobs []*model.Job
	for _, j := range s.jobs {
		if !j.Deleted && j.WorkspaceID == wsID {
			jobs = append(jobs, j.DeepCopy())
		}
	}
	return jobs
}

type jobCursor struct {
	PinnedAt  int64
	UpdatedAt int64
	JobID     string
}

// parseJobCursor decodes a cursor produced by encodeJobCursor. Invalid cursors
// fall back to the beginning — defensive, since a stale cursor should not 400.
// The old "<updatedAt>|<jobID>" shape is still accepted as an unpinned cursor.
func parseJobCursor(cursor string) (jobCursor, bool) {
	if cursor == "" {
		return jobCursor{}, false
	}
	parts := strings.SplitN(cursor, "|", 3)
	if len(parts) == 3 {
		pinnedAt, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil {
			return jobCursor{}, false
		}
		updatedAt, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil || parts[2] == "" {
			return jobCursor{}, false
		}
		return jobCursor{PinnedAt: pinnedAt, UpdatedAt: updatedAt, JobID: parts[2]}, true
	}
	parts = strings.SplitN(cursor, "|", 2)
	if len(parts) == 2 {
		updatedAt, err := strconv.ParseInt(parts[0], 10, 64)
		if err != nil || parts[1] == "" {
			return jobCursor{}, false
		}
		return jobCursor{UpdatedAt: updatedAt, JobID: parts[1]}, true
	}
	return jobCursor{}, false
}

func encodeJobCursor(pinnedAt, updatedAtMillis int64, jobID string) string {
	return fmt.Sprintf("%d|%d|%s", pinnedAt, updatedAtMillis, jobID)
}

func compareJobSummaryAfterCursor(sum model.JobSummary, cur jobCursor) bool {
	sumPinned := sum.PinnedAt > 0
	curPinned := cur.PinnedAt > 0
	if sumPinned != curPinned {
		return curPinned && !sumPinned
	}
	if sum.PinnedAt != cur.PinnedAt {
		return sum.PinnedAt < cur.PinnedAt
	}
	if sum.UpdatedAt != cur.UpdatedAt {
		return sum.UpdatedAt < cur.UpdatedAt
	}
	return sum.ID > cur.JobID
}

// summarize builds a JobSummary from the internal job pointer. Must be called
// under s.mu (read or write). All fields copied are scalars/immutable strings,
// so the result is safe to return without further copying.
func summarize(j *model.Job) model.JobSummary {
	sum := model.JobSummary{
		ID:           j.ID,
		Title:        j.Title,
		ModelID:      j.FirstModelID,
		Status:       j.Status,
		Mode:         j.Mode,
		WorkspaceID:  j.WorkspaceID,
		Workdir:      j.Workdir,
		CreatedAt:    j.CreatedAt.UnixMilli(),
		UpdatedAt:    j.UpdatedAt.UnixMilli(),
		PinnedAt:     j.PinnedAt,
		SessionCount: len(j.SessionIDs),
		ScheduleID:   j.ScheduleID,
		ShareToken:   j.ShareToken,
	}
	return sum
}

// ListByWorkspacePaged returns a page of summaries for the given workspace (or
// all workspaces when wsID is empty), sorted by pinned jobs first, then
// UpdatedAt DESC with ID as the tiebreaker. This is the canonical endpoint
// used by the HTTP list handler.
//
// The implementation snapshots summaries under s.mu (no deep copy — scalar
// fields only) then sorts outside the lock to minimize lock-hold time.
//
// When excludeScheduled is true, jobs with a non-empty ScheduleID are dropped
// before pagination so the caller's page stays full even when the workspace
// has a large tail of scheduled-task jobs.
func (s *serviceImpl) ListByWorkspacePaged(wsID, cursor string, limit int, excludeScheduled bool) ([]model.JobSummary, string, bool, int64) {
	start := time.Now()
	if limit <= 0 {
		limit = 50
	}
	// Cap per-page to keep response size bounded even if a client sends a huge
	// limit. Callers that need everything can page.
	const maxLimit = 200
	if limit > maxLimit {
		limit = maxLimit
	}

	// Snapshot under read lock — scalar copies only, no deep copy.
	s.mu.RLock()
	totalJobs := len(s.jobs)
	orphanSkipped := 0
	scheduledSkipped := 0
	summaries := make([]model.JobSummary, 0, totalJobs)
	for _, j := range s.jobs {
		if j.Deleted {
			continue
		}
		// Skip orphan jobs (no workspace binding). Physical files are kept on
		// disk; they just never surface in any list view.
		if j.WorkspaceID == "" {
			orphanSkipped++
			continue
		}
		if wsID != "" && j.WorkspaceID != wsID {
			continue
		}
		if excludeScheduled && j.ScheduleID != "" {
			scheduledSkipped++
			continue
		}
		summaries = append(summaries, summarize(j))
	}
	s.mu.RUnlock()

	version := s.WorkspaceListVersion(wsID)

	// Sort pinned jobs first, then UpdatedAt DESC, ID ASC as tiebreaker for stable cursor pagination.
	// UpdatedAt is preferred over CreatedAt so recently-active jobs float to
	// the top — matches the UI's day-divider grouping which also keys off
	// UpdatedAt.
	sort.Slice(summaries, func(i, j int) bool {
		if (summaries[i].PinnedAt > 0) != (summaries[j].PinnedAt > 0) {
			return summaries[i].PinnedAt > 0
		}
		if summaries[i].PinnedAt != summaries[j].PinnedAt {
			return summaries[i].PinnedAt > summaries[j].PinnedAt
		}
		if summaries[i].UpdatedAt != summaries[j].UpdatedAt {
			return summaries[i].UpdatedAt > summaries[j].UpdatedAt
		}
		return summaries[i].ID < summaries[j].ID
	})

	// Advance past the cursor. Cursor is exclusive — it marks the last item
	// of the previous page.
	startIdx := 0
	if cursor != "" {
		cur, ok := parseJobCursor(cursor)
		if ok {
			for i, sum := range summaries {
				if sum.ID == cur.JobID {
					startIdx = i + 1
					break
				}
				if compareJobSummaryAfterCursor(sum, cur) {
					startIdx = i
					break
				}
				startIdx = i + 1
			}
		}
	}

	end := startIdx + limit
	hasMore := false
	if end < len(summaries) {
		hasMore = true
	} else {
		end = len(summaries)
	}

	page := summaries[startIdx:end]
	var nextCursor string
	if hasMore && len(page) > 0 {
		last := page[len(page)-1]
		nextCursor = encodeJobCursor(last.PinnedAt, last.UpdatedAt, last.ID)
	}

	// Observability: log query latency. Debug for normal traffic; Warn above
	// 100ms as an early signal that the in-memory scan is getting expensive.
	durMs := time.Since(start).Milliseconds()
	if durMs >= 100 {
		logger.Warnf(context.Background(), "[job.Service] ListByWorkspacePaged slow: wsID=%q totalJobs=%d orphanSkipped=%d scheduledSkipped=%d matched=%d returned=%d durMs=%d", wsID, totalJobs, orphanSkipped, scheduledSkipped, len(summaries), len(page), durMs)
	} else {
		logger.Debugf(context.Background(), "[job.Service] ListByWorkspacePaged: wsID=%q totalJobs=%d orphanSkipped=%d scheduledSkipped=%d matched=%d returned=%d durMs=%d", wsID, totalJobs, orphanSkipped, scheduledSkipped, len(summaries), len(page), durMs)
	}

	return page, nextCursor, hasMore, version
}

func (s *serviceImpl) Delete(jobID string) {
	// Defensive: ensure any in-flight runLoop has exited before tearing down
	// memory and disk state. Without this, a tail-end saveJobWithRetry inside
	// finishJob / stopJob / failJob would recreate the job directory we are
	// about to FileDelete, leaving a half-resurrected ghost on disk and
	// leaking the goroutine. StopAndWait is idempotent — when no run is
	// active it returns immediately. Callers SHOULD still MarkDeleted before
	// calling Delete (so concurrent Start / Continue / SendMessage are
	// rejected during teardown), but a forgotten MarkDeleted no longer
	// produces a goroutine leak or file resurrection.
	s.StopAndWait(jobID)

	s.mu.Lock()
	j, ok := s.jobs[jobID]
	if ok {
		j.Deleted = true
	}
	var cp *model.Job
	if ok {
		cp = j.DeepCopy()
		// Remove the job from the in-memory map so it can be garbage-collected.
		delete(s.jobs, jobID)
	}
	s.mu.Unlock()
	if ok {
		s.clearJobDoneNotified(jobID)
		s.bumpListVersion(cp.WorkspaceID)
		jobDir := typepath.LocalJobDirInWorkspace(cp.WorkspaceID, cp.ID)
		sb := fileserver.GetFileManager()
		if err := sb.FileDelete(&fsmodel.FileDeleteRequest{Path: jobDir}); err != nil {
			logger.Errorf(context.Background(), "[job.Service] remove job dir failed: dir=%s err=%v", jobDir, err)
		}
	}
	// Close and remove the per-job event buffer so any connected SSE
	// clients receive the stream-end signal and the in-memory tail is
	// released. After remove, future Subscribe / Publish calls for this
	// jobID create a fresh empty buffer (which will never be touched
	// because the job is gone from s.jobs).
	s.bus.remove(jobID)
}
