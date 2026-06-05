package handler

import (
	"context"
	"fmt"

	"github.com/fanlv/quartet/types/consts"
	"github.com/fanlv/quartet/types/model"
)

// createSession creates a new session with sandbox setup, used by Job runner.
func (h *Handler) createSession(ctx context.Context, modelID string, agentType, workdir, wsID, jobID string) (*model.Session, error) {
	systemPrompt, err := h.promptService.ResolvePrompt(ctx, consts.KeySystemPrompt)
	if err != nil {
		return nil, fmt.Errorf("load system prompt: %w", err)
	}

	ss, err := h.getOrCreateSessionService(wsID, jobID)
	if err != nil {
		return nil, fmt.Errorf("get session service for job %s: %w", jobID, err)
	}

	s, err := ss.New(modelID, systemPrompt, agentType, workdir)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	if err := ss.SetInitFields(s.ID, jobID, wsID); err != nil {
		return nil, fmt.Errorf("save session: %w", err)
	}
	return s, nil
}
