package job

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
	typepath "github.com/fanlv/quartet/types/path"
)

// CRUD and listing operations on the in-memory job map plus the on-disk
// repository. Lock ordering follows the rules documented on serviceImpl in
// executor_store.go: persist shard before s.mu when both are needed.

func (s *serviceImpl) Create(job *model.Job) error {
	if job == nil {
		return fmt.Errorf("job is nil")
	}
	if job.Mode != model.JobModeInteractive && job.Mode != model.JobModeGraph {
		return fmt.Errorf("unsupported job mode %q", job.Mode)
	}
	// Establish the "Progress is never nil in s.jobs" invariant at the only
	// other entry point besides load(). Subsequent code paths (SendMessage,
	// finishJob, ...) can then dereference
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
	lock := s.persistLock(cp.ID)
	lock.Lock()
	defer lock.Unlock()

	// A Create racing teardown for the same ID must not write through a
	// durable tombstone. IDs are normally unique, but this check also covers
	// deterministic/idempotent callers and keeps every job.json writer behind
	// the same deletion fence.
	s.mu.RLock()
	existing := s.jobs[cp.ID]
	deleted := existing != nil && existing.Deleted
	s.mu.RUnlock()
	if deleted {
		return ErrJobDeleted
	}

	repo, err := s.getOrCreateRepo(cp.WorkspaceID)
	if err != nil {
		return fmt.Errorf("get repo for workspace %s failed: %w", cp.WorkspaceID, err)
	}
	if err := repo.Save(cp.ID, cp); err != nil {
		return err
	}
	observationCopy := cp.DeepCopy()
	s.store(cp.ID, cp)
	s.bumpListVersion(cp.WorkspaceID)
	s.recordJobObservation(observationCopy, "")
	return nil
}

func (s *serviceImpl) CreateIdempotent(job *model.Job) (*model.Job, bool, error) {
	if job == nil {
		return nil, false, fmt.Errorf("job is nil")
	}
	if job.Mode != model.JobModeInteractive && job.Mode != model.JobModeGraph {
		return nil, false, fmt.Errorf("unsupported job mode %q", job.Mode)
	}
	if job.CreationClientMessageID == "" {
		if err := s.Create(job); err != nil {
			return nil, false, err
		}
		return job.DeepCopy(), false, nil
	}
	ensureProgress(job)
	if job.CreationPayloadHash == "" {
		return nil, false, fmt.Errorf("creation payload hash is required for clientMessageId %q", job.CreationClientMessageID)
	}

	lock := s.persistLock(job.ID)
	lock.Lock()
	defer lock.Unlock()
	s.mu.RLock()
	existing, exists := s.jobs[job.ID]
	if exists {
		cp := existing.DeepCopy()
		s.mu.RUnlock()
		if cp.Deleted {
			return nil, false, ErrJobDeleted
		}
		if cp.CreationClientMessageID != job.CreationClientMessageID || cp.CreationPayloadHash != job.CreationPayloadHash {
			return nil, false, fmt.Errorf("%w: %q", ErrClientMessageIDConflict, job.CreationClientMessageID)
		}
		return cp, true, nil
	}
	s.mu.RUnlock()

	cp := job.DeepCopy()
	repo, err := s.getOrCreateRepo(cp.WorkspaceID)
	if err != nil {
		return nil, false, fmt.Errorf("get repo for workspace %s failed: %w", cp.WorkspaceID, err)
	}
	// A process restart may load the deterministic job ID after this service
	// instance was constructed. Consult disk before creating so retries remain
	// idempotent even across that boundary.
	if persisted, loadErr := repo.Load(cp.ID); loadErr == nil {
		if persisted != nil {
			if persisted.Deleted {
				return nil, false, ErrJobDeleted
			}
			if persisted.CreationClientMessageID != cp.CreationClientMessageID || persisted.CreationPayloadHash != cp.CreationPayloadHash {
				return nil, false, fmt.Errorf("%w: %q", ErrClientMessageIDConflict, cp.CreationClientMessageID)
			}
			s.mu.Lock()
			if existing, exists := s.jobs[cp.ID]; exists {
				existingCopy := existing.DeepCopy()
				s.mu.Unlock()
				if existingCopy.CreationClientMessageID != cp.CreationClientMessageID || existingCopy.CreationPayloadHash != cp.CreationPayloadHash {
					return nil, false, fmt.Errorf("%w: %q", ErrClientMessageIDConflict, cp.CreationClientMessageID)
				}
				return existingCopy, true, nil
			}
			s.jobs[cp.ID] = persisted
			s.mu.Unlock()
			return persisted.DeepCopy(), true, nil
		}
	} else if !errors.Is(loadErr, os.ErrNotExist) && !strings.Contains(loadErr.Error(), "no such file or directory") {
		return nil, false, fmt.Errorf("load idempotent job %s: %w", cp.ID, loadErr)
	}
	if err := repo.Save(cp.ID, cp); err != nil {
		return nil, false, err
	}
	observationCopy := cp.DeepCopy()
	s.mu.Lock()
	if existing, exists := s.jobs[cp.ID]; exists {
		existingCopy := existing.DeepCopy()
		s.mu.Unlock()
		if existingCopy.CreationClientMessageID != cp.CreationClientMessageID || existingCopy.CreationPayloadHash != cp.CreationPayloadHash {
			return nil, false, fmt.Errorf("%w: %q", ErrClientMessageIDConflict, cp.CreationClientMessageID)
		}
		return existingCopy, true, nil
	}
	s.jobs[cp.ID] = cp
	s.mu.Unlock()
	s.bumpListVersion(cp.WorkspaceID)
	s.recordJobObservation(observationCopy, "")
	return cp.DeepCopy(), false, nil
}

func IdempotentJobID(clientMessageID string) string {
	sum := sha256.Sum256([]byte(clientMessageID))
	return "job-idem-" + hex.EncodeToString(sum[:16])
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
	return cp, true
}

func (s *serviceImpl) LookupMessage(jobID string, opts *SendMessageOptions) (MessageReceipt, bool, error) {
	if opts == nil || opts.ClientMessageID == "" {
		return MessageReceipt{}, false, nil
	}
	payloadHash, err := clientMessagePayloadHash(opts)
	if err != nil {
		return MessageReceipt{}, false, err
	}
	// Pair with SendMessage's claim persistence. Without this lock, a reader
	// could observe the in-memory processing receipt before job.json commits,
	// acknowledge the retry, and then watch the original save fail and roll the
	// claim back. Returning only committed receipts keeps every 200 durable.
	lock := s.persistLock(jobID)
	lock.Lock()
	defer lock.Unlock()
	s.mu.RLock()
	defer s.mu.RUnlock()
	j, ok := s.jobs[jobID]
	if !ok {
		return MessageReceipt{}, false, ErrJobNotFound
	}
	if j.Deleted {
		return MessageReceipt{}, false, ErrJobDeleted
	}
	if _, exists := j.CommandReceipts[opts.ClientMessageID]; exists {
		return MessageReceipt{}, false, fmt.Errorf("%w: %q was used for a slash command", ErrClientMessageIDConflict, opts.ClientMessageID)
	}
	receipt, ok := j.ClientMessageReceipts[opts.ClientMessageID]
	if !ok {
		return MessageReceipt{}, false, nil
	}
	if receipt.PayloadHash != payloadHash {
		return MessageReceipt{}, false, fmt.Errorf("%w: %q", ErrClientMessageIDConflict, opts.ClientMessageID)
	}
	return newMessageReceipt(opts.ClientMessageID, receipt), true, nil
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
		ID:              j.ID,
		Title:           j.Title,
		AgentID:         j.InitialAgentID,
		ModelID:         j.FirstModelID,
		ACPMode:         j.InitialACPMode,
		ACPThoughtLevel: j.InitialACPThoughtLevel,
		Status:          j.Status,
		Mode:            j.Mode,
		WorkspaceID:     j.WorkspaceID,
		Workdir:         j.Workdir,
		CreatedAt:       j.CreatedAt.UnixMilli(),
		UpdatedAt:       j.UpdatedAt.UnixMilli(),
		PinnedAt:        j.PinnedAt,
		SessionCount:    len(j.SessionIDs),
		ScheduleID:      j.ScheduleID,
		ShareToken:      j.ShareToken,
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

func (s *serviceImpl) Delete(jobID string) error {
	// Install the durable tombstone before stopping producers. This is
	// idempotent when the handler already called MarkDeleted and prevents any
	// new Job writer from starting while StopAndWait drains an interactive run.
	if err := s.MarkDeleted(jobID); err != nil {
		if errors.Is(err, ErrJobNotFound) {
			return nil
		}
		return fmt.Errorf("mark job deleted: %w", err)
	}
	s.StopAndWait(jobID)

	// Physical deletion shares the exact shard used by every job.json writer.
	// Holding it through lookup, FileDelete, and map removal ensures an older
	// Save either commits before this removal or observes the tombstone after
	// it; it can never recreate the directory after FileDelete succeeds.
	lock := s.persistLock(jobID)
	lock.Lock()
	defer lock.Unlock()

	s.mu.RLock()
	j, ok := s.jobs[jobID]
	if !ok {
		s.mu.RUnlock()
		s.clearJobDoneNotified(jobID)
		s.bus.remove(jobID)
		return nil
	}
	cp := j.DeepCopy()
	s.mu.RUnlock()
	if !cp.Deleted {
		return fmt.Errorf("delete job %s without durable tombstone", jobID)
	}

	jobDir := typepath.LocalJobDirInWorkspace(cp.WorkspaceID, cp.ID)
	if err := s.fileManager.FileDelete(&fsmodel.FileDeleteRequest{Path: jobDir}); err != nil {
		// Keep the in-memory tombstone on failure. Get can still retrieve it for
		// the same DELETE request to retry, while List and every writer continue
		// to treat it as deleted. The event buffer is likewise retained until a
		// successful physical delete.
		return fmt.Errorf("remove job dir %s: %w", jobDir, err)
	}

	s.mu.Lock()
	if current, exists := s.jobs[jobID]; exists && current == j {
		delete(s.jobs, jobID)
	}
	s.mu.Unlock()
	s.clearJobDoneNotified(jobID)
	s.bumpListVersion(cp.WorkspaceID)
	// Close and remove the per-job event buffer only after durable removal
	// succeeds, so a failed Delete preserves a fully retryable tombstone.
	s.bus.remove(jobID)
	return nil
}
