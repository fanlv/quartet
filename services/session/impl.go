package session

import (
	"sync"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/types/model"
)

type serviceImpl struct {
	sessions map[string]*model.Session
	mu       sync.RWMutex
	repo     repository.SessionRepo
}

func (m *serviceImpl) load() error {
	metas, err := m.repo.LoadAll()
	if err != nil {
		return err
	}

	for _, meta := range metas {
		if !meta.Deleted {
			m.store(meta.ID, meta)
		}
	}

	return nil
}

func (m *serviceImpl) New(modelID string, systemPrompt string, agentType, workdir string) (*model.Session, error) {
	s := model.NewSession()
	s.ModelID = modelID
	s.SystemPrompt = systemPrompt
	s.Type = agentType
	s.Workdir = workdir
	if err := m.repo.Save(s.ID, s); err != nil {
		return nil, err
	}

	m.store(s.ID, s)
	return s, nil
}

func (m *serviceImpl) store(sid string, s *model.Session) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.sessions[sid] = s
}

func (m *serviceImpl) Get(sid string) (*model.Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[sid]
	return s, ok
}

func (m *serviceImpl) Delete(sid string) {
	m.mu.RLock()
	s, ok := m.sessions[sid]
	if !ok {
		m.mu.RUnlock()
		return
	}
	cp := *s
	m.mu.RUnlock()

	cp.Deleted = true
	cp.UpdatedAt = time.Now()
	if err := m.repo.Save(cp.ID, &cp); err != nil {
		// Save failed: leave the in-memory entry in place so the next
		// reload still sees a non-deleted session and the operator can
		// retry. Removing it from the map before persistence would make
		// the session vanish from this process while disk still says
		// "live" — on restart it would reappear and silently undo the
		// user's delete intent.
		logger.Error("[session.Delete] save deleted session %s failed: %v", cp.ID, err)
		return
	}

	m.mu.Lock()
	if cur, ok := m.sessions[sid]; ok {
		cur.Deleted = true
		delete(m.sessions, sid)
	}
	m.mu.Unlock()
}

// commit applies a successfully-persisted set of field updates back to the
// in-memory session. Callers MUST have called repo.Save with the new values
// before invoking commit; we do not reload from disk here. apply receives the
// live in-memory pointer (still under m.mu) so the caller can write only the
// fields that were just persisted, leaving any other concurrently-updated
// fields untouched.
//
// Why this matters: every Update* below now persists BEFORE touching the
// in-memory state. If Save fails, the function returns the error and memory
// stays consistent with disk; the next loadPersistedACPState / Get caller
// sees the previous (still-valid) values. The previous "write memory then
// Save" pattern left memory holding a value that was never on disk, which
// confused the ACP drift / replay logic on the very next Run within the
// same process.
func (m *serviceImpl) commit(sid string, apply func(*model.Session)) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if cur, ok := m.sessions[sid]; ok {
		apply(cur)
	}
}

// snapshot returns a shallow copy of the named session under the read lock,
// or (zero, false) if the session is unknown. Callers mutate the copy and
// pass it to repo.Save; the in-memory session is untouched.
func (m *serviceImpl) snapshot(sid string) (model.Session, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	s, ok := m.sessions[sid]
	if !ok {
		return model.Session{}, false
	}
	return *s, true
}

// SetInitFields is called once right after New() to stamp JobID / WorkspaceID
// onto the freshly-created session. Persists first, then commits the new
// fields to memory only on Save success — see commit() for rationale.
func (m *serviceImpl) SetInitFields(sid, jobID, wsID string) error {
	cp, ok := m.snapshot(sid)
	if !ok {
		return nil
	}
	now := time.Now()
	cp.JobID = jobID
	cp.WorkspaceID = wsID
	cp.UpdatedAt = now
	if err := m.repo.Save(cp.ID, &cp); err != nil {
		return err
	}
	m.commit(sid, func(s *model.Session) {
		s.JobID = jobID
		s.WorkspaceID = wsID
		s.UpdatedAt = now
	})
	return nil
}

// UpdateModelID atomically sets ModelID and persists it. On Save failure the
// in-memory ModelID stays at its prior value so a retry observes the same
// "needs change" delta and the next Run does not route through a model the
// user did not actually choose. See commit() for the broader rationale.
func (m *serviceImpl) UpdateModelID(sid, modelID string) error {
	cp, ok := m.snapshot(sid)
	if !ok {
		return nil
	}
	if cp.ModelID == modelID {
		return nil
	}
	now := time.Now()
	cp.ModelID = modelID
	cp.UpdatedAt = now
	if err := m.repo.Save(cp.ID, &cp); err != nil {
		return err
	}
	m.commit(sid, func(s *model.Session) {
		s.ModelID = modelID
		s.UpdatedAt = now
	})
	return nil
}

// UpdateACPMode is the mode counterpart to UpdateModelID. Same persist-then-
// commit ordering: a Save failure must not leave memory ahead of disk,
// because subsequent Get() / loadPersistedACPState() consumers would
// otherwise act on a value that the next process restart cannot reproduce.
func (m *serviceImpl) UpdateACPMode(sid, acpMode string) error {
	cp, ok := m.snapshot(sid)
	if !ok {
		return nil
	}
	if cp.ACPMode == acpMode {
		return nil
	}
	now := time.Now()
	cp.ACPMode = acpMode
	cp.UpdatedAt = now
	if err := m.repo.Save(cp.ID, &cp); err != nil {
		return err
	}
	m.commit(sid, func(s *model.Session) {
		s.ACPMode = acpMode
		s.UpdatedAt = now
	})
	return nil
}

func (m *serviceImpl) UpdateTitle(sid, title string) error {
	cp, ok := m.snapshot(sid)
	if !ok {
		return nil
	}
	if cp.Title == title {
		return nil
	}
	now := time.Now()
	cp.Title = title
	cp.UpdatedAt = now
	if err := m.repo.Save(cp.ID, &cp); err != nil {
		return err
	}
	m.commit(sid, func(s *model.Session) {
		s.Title = title
		s.UpdatedAt = now
	})
	return nil
}

// UpdateACPState writes the (acpSessionID, fingerprint) pair atomically.
// Save runs BEFORE the in-memory commit so a failed persist never leaves
// memory holding an ACP session id / sync fingerprint that disk does not
// know about — loadPersistedACPState reads through Get() in the same
// process, so the previous "write memory then Save" pattern would have
// made a follow-up Run replay against a baseline that vanished on
// restart.
func (m *serviceImpl) UpdateACPState(sid, acpSessionID string, fingerprint repository.MessagesFingerprint) error {
	cp, ok := m.snapshot(sid)
	if !ok {
		return nil
	}
	if cp.ACPSessionID == acpSessionID &&
		cp.ACPLastSyncedMessageCount == fingerprint.Count &&
		cp.ACPLastSyncedMessageHash == fingerprint.Hash {
		return nil
	}
	now := time.Now()
	cp.ACPSessionID = acpSessionID
	cp.ACPLastSyncedMessageCount = fingerprint.Count
	cp.ACPLastSyncedMessageHash = fingerprint.Hash
	cp.UpdatedAt = now
	if err := m.repo.Save(cp.ID, &cp); err != nil {
		return err
	}
	m.commit(sid, func(s *model.Session) {
		s.ACPSessionID = acpSessionID
		s.ACPLastSyncedMessageCount = fingerprint.Count
		s.ACPLastSyncedMessageHash = fingerprint.Hash
		s.UpdatedAt = now
	})
	return nil
}

func (m *serviceImpl) UpdateACPSyncFingerprint(sid string, fingerprint repository.MessagesFingerprint) error {
	cp, ok := m.snapshot(sid)
	if !ok {
		return nil
	}
	if cp.ACPLastSyncedMessageCount == fingerprint.Count &&
		cp.ACPLastSyncedMessageHash == fingerprint.Hash {
		return nil
	}
	now := time.Now()
	cp.ACPLastSyncedMessageCount = fingerprint.Count
	cp.ACPLastSyncedMessageHash = fingerprint.Hash
	cp.UpdatedAt = now
	if err := m.repo.Save(cp.ID, &cp); err != nil {
		return err
	}
	m.commit(sid, func(s *model.Session) {
		s.ACPLastSyncedMessageCount = fingerprint.Count
		s.ACPLastSyncedMessageHash = fingerprint.Hash
		s.UpdatedAt = now
	})
	return nil
}

func (m *serviceImpl) Touch(sid string) error {
	cp, ok := m.snapshot(sid)
	if !ok {
		return nil
	}
	now := time.Now()
	cp.UpdatedAt = now
	if err := m.repo.Save(cp.ID, &cp); err != nil {
		return err
	}
	m.commit(sid, func(s *model.Session) {
		s.UpdatedAt = now
	})
	return nil
}
