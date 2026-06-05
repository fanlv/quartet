package session

import (
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/types/model"
)

type Service interface {
	New(modelID string, systemPrompt string, agentType, workdir string) (*model.Session, error)
	// Get returns the session pointer for sid. Callers MUST treat the returned
	// *model.Session as read-only. Metadata updates must go through one of the
	// Update* methods; mutating the pointer directly races with concurrent
	// writers and produces lost-update and internal/disk divergence bugs.
	Get(sid string) (*model.Session, bool)
	// SetInitFields is used once at session creation to stamp JobID/WorkspaceID
	// onto the in-memory session and persist. After this call external callers
	// must not mutate fields on the pointer returned by Get.
	SetInitFields(sid, jobID, wsID string) error
	Delete(sid string)
	// UpdateModelID atomically sets ModelID on the in-memory session and
	// persists the change. Handlers must never mutate Session fields on the
	// pointer returned by Get(). Returns nil (not an error) when the session
	// is missing so legacy call sites using best-effort semantics keep working.
	UpdateModelID(sid, modelID string) error
	// UpdateACPMode is the ACPMode counterpart to UpdateModelID.
	UpdateACPMode(sid, acpMode string) error
	// UpdateTitle atomically sets Title + UpdatedAt and persists.
	UpdateTitle(sid, title string) error
	// UpdateACPState atomically sets ACPSessionID + the ACP sync
	// fingerprint (count + content hash) and persists. Used by the ACP
	// agent when a fresh subprocess session is minted so id and
	// fingerprint advance as a pair — a partial write would let the
	// next Run prompt a session id whose sync baseline does not match
	// disk, so the next drift check would either miss a real divergence
	// or force a spurious reset.
	UpdateACPState(sid, acpSessionID string, fingerprint repository.MessagesFingerprint) error
	// UpdateACPSyncFingerprint atomically advances the sync fingerprint
	// alone. Used at the end of a Run when the acp session id is
	// unchanged and only the post-Run disk state needs to be recorded.
	UpdateACPSyncFingerprint(sid string, fingerprint repository.MessagesFingerprint) error
	// Touch bumps UpdatedAt and persists.
	Touch(sid string) error
}

func NewService(wsID, jobID string) (Service, error) {
	repo, err := repository.NewSessionRepo(wsID, jobID)
	if err != nil {
		return nil, err
	}

	m := &serviceImpl{
		sessions: make(map[string]*model.Session),
		repo:     repo,
	}

	if err := m.load(); err != nil {
		logger.Error("[session.Manager] load sessions failed: %v", err)
	}

	return m, nil
}
