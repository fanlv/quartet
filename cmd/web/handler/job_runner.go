package handler

import (
	"context"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/services/job"
	"github.com/fanlv/quartet/types/agui"
	"github.com/fanlv/quartet/types/consts"
	"github.com/fanlv/quartet/types/model"
)

// jobRunnerImpl implements job.JobRunner using Handler's internal methods.
// It handles both interactive (single message) and loop (preconfigured) modes.
type jobRunnerImpl struct {
	h       *Handler
	workdir string
	wsID    string
}

var _ job.JobRunner = (*jobRunnerImpl)(nil)

// newJobRunner creates a jobRunnerImpl from a job's current fields.
func newJobRunner(h *Handler, j *model.Job) *jobRunnerImpl {
	return &jobRunnerImpl{
		h:       h,
		workdir: j.Workdir,
		wsID:    j.WorkspaceID,
	}
}

func (r *jobRunnerImpl) InitSession(ctx context.Context, jobID string, overrides *model.SessionOverrides) (string, error) {
	var agentType string
	var modelID string
	var acpMode string
	if overrides != nil {
		agentType = overrides.AgentType
		modelID = overrides.ModelID
		acpMode = overrides.ACPMode
	}

	s, err := r.h.createSession(ctx, modelID, agentType, r.workdir, r.wsID, jobID)
	if err != nil {
		return "", err
	}
	if acpMode != "" {
		ss, err := r.h.getOrCreateSessionService(r.wsID, jobID)
		if err == nil {
			if err := ss.UpdateACPMode(s.ID, acpMode); err != nil {
				logger.Errorf(ctx, "[InitSession] save session ACP fields failed: %v, sessionId=%s", err, s.ID)
			}
		}
	}
	return s.ID, nil
}

func (r *jobRunnerImpl) RunIteration(ctx context.Context, sessionID string, messages []*schema.Message, handler agui.EventHandler) error {
	s, _, ok := r.h.getSessionByID(sessionID)
	if !ok {
		if reloaded, rok := r.h.reloadSessionByID(sessionID); rok {
			s = reloaded
			ok = true
		}
	}
	if !ok {
		return errSessionNotFound(sessionID)
	}

	// Try to update session title from the first user message content
	if len(messages) > 0 && messages[0].Role == schema.User && messages[0].Content != "" {
		r.h.tryUpdateSessionTitleFromUserContent(ctx, s, messages[0].Content)
	}

	switch s.Type {
	case consts.AgentTypeEino:
		return r.h.runEinoInternal(ctx, s, messages, handler)
	default:
		return r.h.runACPInternal(ctx, s, messages, handler)
	}
}

func errSessionNotFound(sessionID string) error {
	return &sessionNotFoundError{sessionID: sessionID}
}

// SessionModelID resolves the bound model id for sessionID, going through the
// shared session-service lookup so an evicted entry is reloaded from disk
// before we give up.
func (r *jobRunnerImpl) SessionModelID(sessionID string) string {
	if sessionID == "" {
		return ""
	}
	s, ok := r.h.lookupSession(sessionID)
	if !ok || s == nil {
		return ""
	}
	return s.ModelID
}

type sessionNotFoundError struct {
	sessionID string
}

func (e *sessionNotFoundError) Error() string {
	return "session not found: " + e.sessionID
}
