package job

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
)

// Persistence helpers shared by the run lifecycle and best-effort save sites.
//
// saveJobWithRetry is the canonical "snapshot under s.mu, save outside it,
// surface the error" path. Recovery-critical callers (SendMessage /
// terminal status writes) propagate the error to roll back in-memory state.
// Best-effort callers (attach_session and similar metadata updates) pair the
// call with recordPersistWarning so a failure
// leaves a visible breadcrumb on the job instead of vanishing into the log.

// saveJobWithRetry persists a job and returns the error so callers that
// update recovery-critical state (terminal status, message receipt, etc.) can
// log or annotate the divergence between in-memory/event
// state and on-disk state. Best-effort callers pair this with
// recordPersistWarning instead of swallowing the error.
//
// It also does one quick retry so a transient filesystem hiccup (temp rename,
// EINTR) doesn't immediately diverge state.
func (s *serviceImpl) saveJobWithRetry(ctx context.Context, job *model.Job, action string) error {
	lock := s.persistLock(job.ID)
	lock.Lock()
	defer lock.Unlock()
	return s.saveJobWithRetryUnderPersistLock(ctx, job, action)
}

// saveJobWithRetryUnderPersistLock is saveJobWithRetry's body, minus the
// persist-shard acquisition. Callers that already hold persistLock(job.ID) —
// lifecycle transitions that must keep their check→flip→persist sequence
// serialized call this directly; the persist shard is NOT reentrant, so
// calling saveJobWithRetry while holding it would deadlock. Before touching
// disk it also verifies that job is still the live map entry and is not
// tombstoned. That check is the final fence against an old run pointer writing
// after Delete has started or completed.
func (s *serviceImpl) saveJobWithRetryUnderPersistLock(ctx context.Context, job *model.Job, action string) error {
	s.mu.Lock()
	current, ok := s.jobs[job.ID]
	if !ok {
		s.mu.Unlock()
		return ErrJobNotFound
	}
	if current.Deleted {
		s.mu.Unlock()
		return ErrJobDeleted
	}
	if current != job {
		s.mu.Unlock()
		return ErrJobNotFound
	}
	job.UpdatedAt = time.Now()
	cp := job.DeepCopy()
	s.mu.Unlock()
	repo, err := s.getOrCreateRepo(cp.WorkspaceID)
	if err != nil {
		return fmt.Errorf("get repo for workspace %s: %w", cp.WorkspaceID, err)
	}
	saveErr := repo.Save(cp.ID, cp)
	if saveErr != nil {
		// One quick retry — most Save failures here are transient (fsync
		// contention, temp-file rename). If both fail, surface the error
		// so the caller can decide whether to annotate state, fail fast, or
		// continue with live in-memory/event state.
		if retryErr := repo.Save(cp.ID, cp); retryErr == nil {
			saveErr = nil
		} else {
			saveErr = fmt.Errorf("initial save failed; retry also failed: %w", errors.Join(saveErr, retryErr))
		}
	}
	if saveErr != nil {
		logger.Errorf(ctx, "[job.Service] saveJobWithRetry failed: action=%s jobId=%s err=%v", action, job.ID, saveErr)
		return saveErr
	}
	s.bumpListVersion(cp.WorkspaceID)
	s.recordJobObservation(cp, "")
	return nil
}

// recordPersistWarning leaves an in-memory breadcrumb when a non-terminal,
// best-effort save fails. The original saveJobWithRetry call already logged at
// ERROR; this marker keeps the degraded state observable on the live Job and
// is naturally flushed to disk by the next successful save (terminal status
// transition, next turn, etc.). Re-saving here is intentionally avoided
// — when persistence is genuinely failing (disk full, EROFS) it just amplifies
// IO and ERROR-log noise without ever succeeding.
func (s *serviceImpl) recordPersistWarning(_ context.Context, job *model.Job, action string, err error) {
	if err == nil {
		return
	}
	msg := fmt.Sprintf("persist failed after %s: %v", action, err)
	s.mu.Lock()
	job.Progress.PersistWarnings = append(job.Progress.PersistWarnings, msg)
	s.mu.Unlock()
}
