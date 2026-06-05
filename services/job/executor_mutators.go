package job

import (
	"fmt"
	"time"

	"github.com/fanlv/quartet/types/consts"
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
// A targeted mutator must never replace a runLoop-owned field (Status,
// Progress, LoopConfig as a whole, Resume, SessionIDs); see the ownership
// model on serviceImpl.
//
// MarkDeleted / UpdateTitle / UpdatePinned / SetFirstModelID share the simple "snapshot →
// save → mirror" shape and are routed through updateJobField. EnsureShareToken
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
// snapshot. Callers should still wait for runLoop to exit before cleaning up
// on-disk artefacts.
func (s *serviceImpl) MarkDeleted(jobID string) error {
	set := func(j *model.Job) { j.Deleted = true }
	return s.updateJobField(jobID, set, set)
}

func (s *serviceImpl) UpdateTitle(jobID string, title string) error {
	apply := func(j *model.Job) {
		j.Title = title
		if j.LoopConfig != nil {
			if j.LoopConfig.Variables == nil {
				j.LoopConfig.Variables = make(map[string]string)
			}
			j.LoopConfig.Variables[consts.VarJobTitle] = title
		}
	}
	return s.updateJobField(jobID, apply, apply)
}

func (s *serviceImpl) UpdatePinned(jobID string, pinned bool) (int64, error) {
	pinnedAt := int64(0)
	if pinned {
		pinnedAt = time.Now().UnixMilli()
	}
	lock := s.persistLock(jobID)
	lock.Lock()
	defer lock.Unlock()

	s.mu.Lock()
	existing, ok := s.jobs[jobID]
	if !ok {
		s.mu.Unlock()
		return 0, ErrJobNotFound
	}
	cp := existing.DeepCopy()
	s.mu.Unlock()

	cp.PinnedAt = pinnedAt
	repo, err := s.getOrCreateRepo(cp.WorkspaceID)
	if err != nil {
		return 0, fmt.Errorf("get repo for workspace %s failed: %w", cp.WorkspaceID, err)
	}
	if err := repo.Save(cp.ID, cp); err != nil {
		return 0, err
	}

	s.mu.Lock()
	if cur, ok := s.jobs[jobID]; ok && cur == existing {
		existing.PinnedAt = pinnedAt
	}
	s.mu.Unlock()

	s.bumpListVersion(cp.WorkspaceID)
	return pinnedAt, nil
}

// SetFirstModelID denormalizes the first session's ModelID onto the Job. See
// the interface comment for the motivation (eliminating the JobList N+1).
// Idempotent: if the job already carries this modelID we skip the disk write
// and the in-memory mirror entirely.
func (s *serviceImpl) SetFirstModelID(jobID string, modelID string) error {
	if modelID == "" {
		return nil
	}
	s.mu.RLock()
	existing, ok := s.jobs[jobID]
	if !ok {
		s.mu.RUnlock()
		return ErrJobNotFound
	}
	if existing.FirstModelID == modelID {
		s.mu.RUnlock()
		return nil
	}
	s.mu.RUnlock()

	set := func(j *model.Job) { j.FirstModelID = modelID }
	return s.updateJobField(jobID, set, set)
}

func (s *serviceImpl) EnsureShareToken(jobID string, generate func() (string, error)) (string, error) {
	s.mu.Lock()
	existing, ok := s.jobs[jobID]
	if !ok {
		s.mu.Unlock()
		return "", ErrJobNotFound
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
