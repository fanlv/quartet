package session

import (
	"fmt"
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
	// persistKey identifies the on-disk session namespace shared by all
	// serviceImpl instances for the same Job. See sessionPersistGate.
	persistKey string
}

// sessionPersistState is shared process-wide, not per serviceImpl. A session
// service may be evicted and recreated while an older reference is still in
// use; a lock stored on either instance would therefore leave two writers for
// the same metadata file unsynchronised. The stable ws/job/session key makes
// every instance participate in the same ordering. Entries intentionally live
// for the process lifetime: session IDs are unique and bounded at human scale,
// while evicting a successful delete tombstone could re-open the resurrection
// window for a stale service reference.
type sessionPersistState struct {
	mu      sync.Mutex
	deleted bool
}

var sessionPersistStates sync.Map // map[string]*sessionPersistState

func sessionPersistGate(key string) *sessionPersistState {
	state, _ := sessionPersistStates.LoadOrStore(key, &sessionPersistState{})
	return state.(*sessionPersistState)
}

// persistSessionKey includes sid because each session has an independent
// metadata file. Empty persistKey is used only by package tests that inject a
// repository directly; the repo pointer keeps those unrelated fixtures from
// sharing a gate while still making repeated operations on one fixture meet.
func (m *serviceImpl) persistSessionKey(sid string) string {
	if m.persistKey != "" {
		return m.persistKey + "/" + sid
	}
	return fmt.Sprintf("test:%p/%s", m.repo, sid)
}

func (m *serviceImpl) persistState(sid string) *sessionPersistState {
	return sessionPersistGate(m.persistSessionKey(sid))
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

func (m *serviceImpl) New(modelID string, agentType, workdir string, binding *model.AgentRuntimeBinding) (*model.Session, error) {
	s := model.NewSession()
	s.ModelID = modelID
	s.Type = agentType
	s.Workdir = workdir
	if binding != nil {
		applyAgentBinding(s, *binding)
	}
	state := m.persistState(s.ID)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.deleted {
		// IDs are generated uniquely, so this can only be reached by a stale
		// in-process creator attempting to reuse an already-deleted ID.
		return nil, fmt.Errorf("session %s was already deleted", s.ID)
	}
	if err := m.repo.Save(s.ID, s); err != nil {
		return nil, err
	}

	m.store(s.ID, s)
	return s, nil
}

func (m *serviceImpl) UpdateAgentBinding(sid string, binding model.AgentRuntimeBinding) error {
	state := m.persistState(sid)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.deleted {
		return nil
	}
	cp, ok := m.snapshot(sid)
	if !ok {
		return nil
	}
	if cp.AgentID == binding.AgentID &&
		cp.AgentRevision == binding.Revision &&
		cp.AgentRuntimeKey == binding.RuntimeKey {
		return nil
	}
	now := time.Now()
	applyAgentBinding(&cp, binding)
	cp.UpdatedAt = now
	if err := m.repo.Save(cp.ID, &cp); err != nil {
		return err
	}
	m.commit(sid, func(s *model.Session) {
		applyAgentBinding(s, binding)
		s.UpdatedAt = now
	})
	return nil
}

func applyAgentBinding(session *model.Session, binding model.AgentRuntimeBinding) {
	session.AgentID = binding.AgentID
	session.AgentRevision = binding.Revision
	session.AgentRuntimeKey = binding.RuntimeKey
	session.AgentDefinition = binding.Definition
	session.AgentDefinition.ACPArgs = append([]string(nil), binding.Definition.ACPArgs...)
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

func (m *serviceImpl) Delete(sid string) error {
	state := m.persistState(sid)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.deleted {
		return nil
	}

	cp, ok := m.snapshot(sid)
	if !ok {
		return nil
	}

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
		return err
	}

	m.mu.Lock()
	if cur, ok := m.sessions[sid]; ok {
		cur.Deleted = true
		delete(m.sessions, sid)
	}
	m.mu.Unlock()
	// Publish the monotonic deletion fence only after the tombstone is
	// durable. On Save failure the session remains live and a retry is safe.
	state.deleted = true
	return nil
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
	state := m.persistState(sid)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.deleted {
		return nil
	}
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
	state := m.persistState(sid)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.deleted {
		return nil
	}
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
	state := m.persistState(sid)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.deleted {
		return nil
	}
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

// UpdateACPThoughtLevel is the thought_level counterpart to UpdateACPMode.
// Same persist-then-commit ordering.
func (m *serviceImpl) UpdateACPThoughtLevel(sid, acpThoughtLevel string) error {
	state := m.persistState(sid)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.deleted {
		return nil
	}
	cp, ok := m.snapshot(sid)
	if !ok {
		return nil
	}
	if cp.ACPThoughtLevel == acpThoughtLevel {
		return nil
	}
	now := time.Now()
	cp.ACPThoughtLevel = acpThoughtLevel
	cp.UpdatedAt = now
	if err := m.repo.Save(cp.ID, &cp); err != nil {
		return err
	}
	m.commit(sid, func(s *model.Session) {
		s.ACPThoughtLevel = acpThoughtLevel
		s.UpdatedAt = now
	})
	return nil
}

func (m *serviceImpl) UpdateTitle(sid, title string) error {
	state := m.persistState(sid)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.deleted {
		return nil
	}
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
	state := m.persistState(sid)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.deleted {
		return nil
	}
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
	state := m.persistState(sid)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.deleted {
		return nil
	}
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
	state := m.persistState(sid)
	state.mu.Lock()
	defer state.mu.Unlock()
	if state.deleted {
		return nil
	}
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
