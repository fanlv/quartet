package job

import (
	"context"
	"fmt"
	"time"

	"github.com/fanlv/quartet/types/model"
)

// Targeted mutators that only update a single named field (plus UpdatedAt) on
// the in-memory job. They follow the locking contract documented on
// serviceImpl in executor_store.go:
//
//   - Take the per-job persist shard first to serialise concurrent writes.
//   - Snapshot under s.mu, write to disk outside it, then mirror the change
//     back into memory under s.mu — and only when the in-memory pointer has
//     not been swapped out by a concurrent reload.
//
// A targeted mutator must never replace a run-owned field (Status,
// Progress, LoopConfig as a whole, Resume, SessionIDs); see the ownership
// model on serviceImpl.
//
// UpdateTitle / UpdatePinned / SetFirstModelID share the simple "snapshot →
// save → mirror" shape and are routed through updateJobField. MarkDeleted is
// deliberately separate: it is the one mutator allowed to observe an existing
// tombstone and return success. EnsureShareToken
// and ClearShareToken are NOT eligible: the former runs a caller-supplied
// generator outside the lock and uses double-checked locking; both touch
// externally-visible state and require explicit rollback when Save fails.
// They keep their bespoke implementations.

// updateJobField is the shared backbone for targeted mutators that only need
// "snapshot under s.mu → save outside it → mirror under s.mu". mutate runs on
// the deep-copied snapshot before the disk write; mirror reapplies the same
// change to the live in-memory pointer once disk has committed, gated on the
// pointer not having been swapped by a concurrent reload. UpdatedAt is bumped
// by this helper.
//
// Save failure leaves the in-memory job untouched, so callers do not need to
// implement rollback. Mutators that touch externally-visible state and DO
// need rollback (EnsureShareToken / ClearShareToken) use a different shape
// and are not routed through here.
func (s *serviceImpl) updateJobField(jobID string, mutate, mirror func(j *model.Job)) error {
	lock := s.persistLock(jobID)
	lock.Lock()
	defer lock.Unlock()

	s.mu.Lock()
	existing, ok := s.jobs[jobID]
	if !ok {
		s.mu.Unlock()
		return ErrJobNotFound
	}
	if existing.Deleted {
		s.mu.Unlock()
		return ErrJobDeleted
	}
	cp := existing.DeepCopy()
	s.mu.Unlock()

	mutate(cp)
	cp.UpdatedAt = time.Now()

	repo, err := s.getOrCreateRepo(cp.WorkspaceID)
	if err != nil {
		return fmt.Errorf("get repo for workspace %s failed: %w", cp.WorkspaceID, err)
	}
	if err := repo.Save(cp.ID, cp); err != nil {
		return err
	}

	// Disk committed. Mirror the change to in-memory state, but only if the
	// pointer hasn't been swapped (a concurrent reload could have replaced it).
	s.mu.Lock()
	if cur, ok := s.jobs[jobID]; ok && cur == existing {
		mirror(existing)
		existing.UpdatedAt = cp.UpdatedAt
	}
	s.mu.Unlock()

	s.bumpListVersion(cp.WorkspaceID)
	return nil
}

// MarkDeleted atomically sets Deleted=true on the in-memory job and persists
// the change. Disk is written BEFORE the in-memory mutation so a failing
// repo.Save does not leave memory and disk diverged. Serialized by persist
// shard so a concurrent full-job save cannot overwrite the flag with a stale
// snapshot. Callers should still wait for the run to exit before cleaning up
// on-disk artefacts.
func (s *serviceImpl) MarkDeleted(jobID string) error {
	lock := s.persistLock(jobID)
	lock.Lock()
	defer lock.Unlock()

	s.mu.Lock()
	existing, ok := s.jobs[jobID]
	if !ok {
		s.mu.Unlock()
		return ErrJobNotFound
	}
	if existing.Deleted {
		s.mu.Unlock()
		return nil
	}
	cp := existing.DeepCopy()
	cp.Deleted = true
	cp.UpdatedAt = time.Now()
	s.mu.Unlock()

	repo, err := s.getOrCreateRepo(cp.WorkspaceID)
	if err != nil {
		return fmt.Errorf("get repo for workspace %s failed: %w", cp.WorkspaceID, err)
	}
	if err := repo.Save(cp.ID, cp); err != nil {
		return err
	}

	s.mu.Lock()
	if cur, exists := s.jobs[jobID]; exists && cur == existing {
		existing.Deleted = true
		existing.UpdatedAt = cp.UpdatedAt
	}
	s.mu.Unlock()
	s.bumpListVersion(cp.WorkspaceID)
	return nil
}

func (s *serviceImpl) UpdateTitle(jobID string, title string) error {
	apply := func(j *model.Job) {
		j.Title = title
	}
	return s.updateJobField(jobID, apply, apply)
}

func (s *serviceImpl) UpdateTitleGenerationError(jobID string, message string) error {
	apply := func(j *model.Job) {
		j.TitleGenerationError = message
	}
	return s.updateJobField(jobID, apply, apply)
}

func (s *serviceImpl) UpdatePinned(jobID string, pinned bool) (int64, error) {
	pinnedAt := int64(0)
	if pinned {
		pinnedAt = time.Now().UnixMilli()
	}
	apply := func(j *model.Job) { j.PinnedAt = pinnedAt }
	return pinnedAt, s.updateJobField(jobID, apply, apply)
}

// SetFirstModelID denormalizes the first session's ModelID onto the Job. See
// the interface comment for the motivation (eliminating the JobList N+1).
// Idempotent: if the job already carries this modelID we skip the disk write
// and the in-memory mirror entirely.
func (s *serviceImpl) SetFirstModelID(jobID string, modelID string) error {
	lock := s.persistLock(jobID)
	lock.Lock()
	defer lock.Unlock()

	s.mu.Lock()
	existing, ok := s.jobs[jobID]
	if !ok {
		s.mu.Unlock()
		return ErrJobNotFound
	}
	if existing.Deleted {
		s.mu.Unlock()
		return ErrJobDeleted
	}
	if modelID == "" || existing.FirstModelID == modelID {
		s.mu.Unlock()
		return nil
	}
	cp := existing.DeepCopy()
	cp.FirstModelID = modelID
	cp.UpdatedAt = time.Now()
	s.mu.Unlock()

	repo, err := s.getOrCreateRepo(cp.WorkspaceID)
	if err != nil {
		return fmt.Errorf("get repo for workspace %s failed: %w", cp.WorkspaceID, err)
	}
	if err := repo.Save(cp.ID, cp); err != nil {
		return err
	}
	s.mu.Lock()
	if cur, exists := s.jobs[jobID]; exists && cur == existing {
		existing.FirstModelID = modelID
		existing.UpdatedAt = cp.UpdatedAt
	}
	s.mu.Unlock()
	s.bumpListVersion(cp.WorkspaceID)
	return nil
}

// AttachGraphSession appends sessionID to the job's GraphSessionIDs whitelist
// (de-duplicated), so an interactive SendMessage may later target a graph
// node's session — letting a user keep chatting in a finished node after the
// run stops. Idempotent: a no-op (no disk write) if the session is already
// recorded, which is the common case since graph re-writes the same
// DisplaySessionID at open, success and failure. Kept off SessionIDs so the
// linear-iteration semantics of that field are unaffected. Follows the same
// snapshot → save → mirror locking contract as SetGraphRunState.
// JobTitle implements graph.JobStateSink / Service: it returns the Job's display
// title so a graph node hook can inject it as $QUARTET_JOB_TITLE. Best-effort —
// an unknown job returns "" (hooks log-and-continue, never erroring on a miss).
func (s *serviceImpl) JobTitle(_ context.Context, jobID string) string {
	if j, ok := s.Get(jobID); ok {
		return j.Title
	}
	return ""
}

func (s *serviceImpl) AttachGraphSession(_ context.Context, jobID, sessionID string) error {
	if sessionID == "" {
		return nil
	}
	lock := s.persistLock(jobID)
	lock.Lock()
	defer lock.Unlock()

	s.mu.Lock()
	existing, ok := s.jobs[jobID]
	if !ok {
		s.mu.Unlock()
		return ErrJobNotFound
	}
	for _, sid := range existing.GraphSessionIDs {
		if sid == sessionID {
			s.mu.Unlock()
			return nil // already whitelisted — skip the redundant save
		}
	}
	if existing.Deleted {
		// A graph node may finish opening its session while Job deletion is
		// draining the scheduler. Keep that late ownership fact in memory so
		// deleteMarkedJob's final snapshot can remove the session, but never
		// rewrite the durable tombstone or recreate an already-removed dir.
		existing.GraphSessionIDs = append(existing.GraphSessionIDs, sessionID)
		s.mu.Unlock()
		return nil
	}
	cp := existing.DeepCopy()
	cp.GraphSessionIDs = append(cp.GraphSessionIDs, sessionID)
	cp.UpdatedAt = time.Now()
	s.mu.Unlock()

	repo, err := s.getOrCreateRepo(cp.WorkspaceID)
	if err != nil {
		return fmt.Errorf("get repo for workspace %s failed: %w", cp.WorkspaceID, err)
	}
	if err := repo.Save(cp.ID, cp); err != nil {
		return err
	}

	s.mu.Lock()
	if cur, ok := s.jobs[jobID]; ok && cur == existing {
		existing.GraphSessionIDs = append(existing.GraphSessionIDs, sessionID)
		existing.UpdatedAt = cp.UpdatedAt
	}
	s.mu.Unlock()

	s.bumpListVersion(cp.WorkspaceID)
	return nil
}

func (s *serviceImpl) SetGraphRunState(_ context.Context, jobID, graphRunID string, status model.JobStatus, graphStatus model.GraphRunStatus, startedAt, finishedAt int64, graphSessionID string) error {
	lock := s.persistLock(jobID)
	lock.Lock()

	var doneJob *model.Job
	s.mu.Lock()
	existing, ok := s.jobs[jobID]
	if !ok {
		s.mu.Unlock()
		lock.Unlock()
		return ErrJobNotFound
	}
	if existing.Deleted {
		// MarkDeleted fences every durable writer, but a scheduler that was
		// already bound to this exact GraphRun still owns one final completion
		// notification. Deliver it from an in-memory terminal snapshot so a
		// scheduled task releases its concurrency slot without rewriting the
		// tombstone (or resurrecting job.json after physical deletion).
		if graphRunID == "" || existing.GraphRunID != graphRunID || !isTerminalJobStatus(status) {
			s.mu.Unlock()
			lock.Unlock()
			return ErrJobDeleted
		}
		existing.Mode = model.JobModeGraph
		existing.Status = status
		if startedAt > 0 {
			existing.StartedAt = startedAt
		}
		if finishedAt > 0 {
			existing.FinishedAt = finishedAt
		}
		existing.UpdatedAt = time.Now()
		doneJob = existing.DeepCopy()
		s.mu.Unlock()
		lock.Unlock()
		s.notifyJobDone(doneJob)
		return nil
	}
	cp := existing.DeepCopy()
	cp.Mode = model.JobModeGraph
	cp.GraphRunID = graphRunID
	cp.Status = status
	if startedAt > 0 {
		cp.StartedAt = startedAt
	}
	if finishedAt > 0 {
		cp.FinishedAt = finishedAt
	}
	cp.UpdatedAt = time.Now()
	s.mu.Unlock()

	repo, err := s.getOrCreateRepo(cp.WorkspaceID)
	if err != nil {
		lock.Unlock()
		return fmt.Errorf("get repo for workspace %s failed: %w", cp.WorkspaceID, err)
	}
	if err := repo.Save(cp.ID, cp); err != nil {
		if isTerminalJobStatus(status) {
			s.recordJobObservationWithGraphSession(cp, string(graphStatus), graphSessionID)
		}
		lock.Unlock()
		return err
	}
	if isTerminalJobStatus(cp.Status) {
		doneJob = cp
	}

	s.mu.Lock()
	if cur, ok := s.jobs[jobID]; ok && cur == existing {
		existing.Mode = cp.Mode
		existing.GraphRunID = cp.GraphRunID
		existing.Status = cp.Status
		existing.StartedAt = cp.StartedAt
		existing.FinishedAt = cp.FinishedAt
		existing.UpdatedAt = cp.UpdatedAt
	}
	s.mu.Unlock()

	s.bumpListVersion(cp.WorkspaceID)
	s.recordJobObservationWithGraphSession(cp, string(graphStatus), graphSessionID)
	if status == model.JobStatusPending || status == model.JobStatusRunning {
		// Clear before releasing the persistence shard. Otherwise a tombstone
		// and its terminal callback could overtake this post-save bookkeeping,
		// then this older running update would erase the exactly-once marker.
		s.clearJobDoneNotified(jobID)
	}
	lock.Unlock()
	if doneJob != nil {
		s.notifyJobDone(doneJob)
	}
	return nil
}

// ClearGraphRunLinkage detaches a Job from a deleted GraphRun, but only if the
// Job is still bound to that exact run (it may have been re-bound to a newer run
// since). It is a best-effort cleanup called when a GraphRun is deleted.
func (s *serviceImpl) ClearGraphRunLinkage(_ context.Context, jobID, graphRunID string) error {
	lock := s.persistLock(jobID)
	lock.Lock()
	defer lock.Unlock()

	s.mu.Lock()
	existing, ok := s.jobs[jobID]
	if !ok {
		s.mu.Unlock()
		return ErrJobNotFound
	}
	if existing.Deleted {
		s.mu.Unlock()
		return ErrJobDeleted
	}
	if existing.GraphRunID != graphRunID {
		// Already re-bound to a different run; nothing to clear.
		s.mu.Unlock()
		return nil
	}
	cp := existing.DeepCopy()
	cp.GraphRunID = ""
	cp.UpdatedAt = time.Now()
	s.mu.Unlock()

	repo, err := s.getOrCreateRepo(cp.WorkspaceID)
	if err != nil {
		return fmt.Errorf("get repo for workspace %s failed: %w", cp.WorkspaceID, err)
	}
	if err := repo.Save(cp.ID, cp); err != nil {
		return err
	}

	s.mu.Lock()
	if cur, ok := s.jobs[jobID]; ok && cur == existing {
		existing.GraphRunID = ""
		existing.UpdatedAt = cp.UpdatedAt
	}
	s.mu.Unlock()

	s.bumpListVersion(cp.WorkspaceID)
	return nil
}

// FailGraphJob forces a Graph-type Job to the Failed terminal status with
// message on Progress.LastError. See the Service interface for why it must NOT
// emit OnJobDone (the scheduler releases the slot on the failed trigger; firing
// the done callback would double-release).
//
// It reuses failJob — the same terminal path interactive runs use — so status flip,
// persistence and the terminal SSE event all stay consistent. failJob never emits
// notifyJobDone (graph runs fire it via SetGraphRunState; FailGraphJob must not);
// a Graph Job carries no Resume and no
// interactive prior status, so applyTerminalStatusLocked writes Failed directly.
func (s *serviceImpl) FailGraphJob(ctx context.Context, jobID, message string) error {
	s.mu.Lock()
	existing, ok := s.jobs[jobID]
	if !ok {
		s.mu.Unlock()
		return ErrJobNotFound
	}
	if existing.Deleted {
		s.mu.Unlock()
		return ErrJobDeleted
	}
	// Idempotent: if a terminal status is already set (e.g. a racing GraphRun
	// terminal callback won), don't re-fail it — that would publish a second
	// terminal event and could clobber a more specific status/error.
	if isTerminalJobStatus(existing.Status) {
		s.mu.Unlock()
		return nil
	}
	existing.Mode = model.JobModeGraph
	job := existing
	s.mu.Unlock()

	// failJob re-acquires s.mu and persists via saveJobWithRetry (which takes
	// the per-job persist shard), so it must be called without holding s.mu.
	s.failJob(ctx, job, message)
	return nil
}

func (s *serviceImpl) EnsureShareToken(jobID string, generate func() (string, error)) (string, error) {
	s.mu.Lock()
	existing, ok := s.jobs[jobID]
	if !ok {
		s.mu.Unlock()
		return "", ErrJobNotFound
	}
	if existing.Deleted {
		s.mu.Unlock()
		return "", ErrJobDeleted
	}
	if existing.ShareToken != "" {
		tok := existing.ShareToken
		s.mu.Unlock()
		return tok, nil
	}
	wsID := existing.WorkspaceID
	// Release the lock while we call the caller-supplied generator, which
	// may be doing crypto/rand I/O we don't want to serialise across jobs.
	s.mu.Unlock()
	token, err := generate()
	if err != nil {
		return "", err
	}
	repo, err := s.getOrCreateRepo(wsID)
	if err != nil {
		return "", fmt.Errorf("get repo for workspace %s failed: %w", wsID, err)
	}

	lock := s.persistLock(jobID)
	lock.Lock()
	defer lock.Unlock()

	s.mu.Lock()
	existing, ok = s.jobs[jobID]
	if !ok {
		s.mu.Unlock()
		return "", ErrJobNotFound
	}
	if existing.Deleted {
		s.mu.Unlock()
		return "", ErrJobDeleted
	}
	if existing.ShareToken != "" {
		// Another caller won the race; use their token.
		tok := existing.ShareToken
		s.mu.Unlock()
		return tok, nil
	}
	oldUpdatedAt := existing.UpdatedAt
	existing.ShareToken = token
	existing.UpdatedAt = time.Now()
	cp := existing.DeepCopy()
	s.mu.Unlock()

	if err := repo.Save(cp.ID, cp); err != nil {
		// Keep in-memory state aligned with disk when persistence fails. Share
		// token mutations are externally visible, so unlike best-effort progress
		// saves we must not leave a token that only exists until process restart.
		s.mu.Lock()
		// Roll back only if our write is still the one in memory — a concurrent
		// reload could have swapped the pointer or another caller could have
		// completed a successful write since.
		if cur, ok := s.jobs[jobID]; ok && cur == existing && cur.ShareToken == token {
			existing.ShareToken = ""
			existing.UpdatedAt = oldUpdatedAt
		}
		s.mu.Unlock()
		return "", err
	}
	s.bumpListVersion(cp.WorkspaceID)
	return token, nil
}

func (s *serviceImpl) ClearShareToken(jobID string) error {
	s.mu.Lock()
	existing, ok := s.jobs[jobID]
	if !ok {
		s.mu.Unlock()
		return ErrJobNotFound
	}
	if existing.Deleted {
		s.mu.Unlock()
		return ErrJobDeleted
	}
	if existing.ShareToken == "" {
		s.mu.Unlock()
		return nil
	}
	wsID := existing.WorkspaceID
	s.mu.Unlock()
	repo, err := s.getOrCreateRepo(wsID)
	if err != nil {
		return fmt.Errorf("get repo for workspace %s failed: %w", wsID, err)
	}

	lock := s.persistLock(jobID)
	lock.Lock()
	defer lock.Unlock()

	s.mu.Lock()
	existing, ok = s.jobs[jobID]
	if !ok {
		s.mu.Unlock()
		return ErrJobNotFound
	}
	if existing.Deleted {
		s.mu.Unlock()
		return ErrJobDeleted
	}
	if existing.ShareToken == "" {
		s.mu.Unlock()
		return nil
	}
	oldToken := existing.ShareToken
	oldUpdatedAt := existing.UpdatedAt
	existing.ShareToken = ""
	existing.UpdatedAt = time.Now()
	cp := existing.DeepCopy()
	s.mu.Unlock()

	if err := repo.Save(cp.ID, cp); err != nil {
		s.mu.Lock()
		if cur, ok := s.jobs[jobID]; ok && cur == existing && cur.ShareToken == "" {
			existing.ShareToken = oldToken
			existing.UpdatedAt = oldUpdatedAt
		}
		s.mu.Unlock()
		return err
	}
	s.bumpListVersion(cp.WorkspaceID)
	return nil
}
