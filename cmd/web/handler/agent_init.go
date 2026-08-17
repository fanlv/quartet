package handler

import (
	"context"
	"fmt"

	agentinstall "github.com/fanlv/quartet/services/agent/install"
	"github.com/fanlv/quartet/services/agent/probe"
	"github.com/fanlv/quartet/types/model"
)

// createSession creates a new session with sandbox setup, used by Job runner.
func (h *Handler) createSession(ctx context.Context, modelID string, agentType, workdir, wsID, jobID string) (*model.Session, error) {
	return h.createSessionWithBinding(ctx, modelID, agentType, workdir, wsID, jobID, nil)
}

func (h *Handler) createSessionWithBinding(
	ctx context.Context,
	modelID string,
	agentType string,
	workdir string,
	wsID string,
	jobID string,
	pinned *model.AgentRuntimeBinding,
) (*model.Session, error) {
	ss, err := h.getOrCreateSessionService(wsID, jobID)
	if err != nil {
		return nil, fmt.Errorf("get session service for job %s: %w", jobID, err)
	}

	binding := pinned
	if binding == nil && agentType != "" && agentType != shellSessionType {
		resolved, found, resolveErr := h.agentCatalog.Resolve(ctx, agentType)
		if resolveErr != nil {
			return nil, fmt.Errorf("resolve Agent %q for new session failed: %w", agentType, resolveErr)
		}
		if !found {
			return nil, fmt.Errorf("resolve Agent %q for new session failed: Agent does not exist", agentType)
		}
		if resolved.Deprecated || resolved.Lifecycle != model.AgentLifecycleActive {
			return nil, fmt.Errorf(
				"AgentID %q cannot create a session: deprecated=%t lifecycle=%s",
				resolved.AgentID,
				resolved.Deprecated,
				resolved.Lifecycle,
			)
		}
		binding = &model.AgentRuntimeBinding{
			AgentID:    resolved.AgentID,
			Revision:   resolved.Revision,
			RuntimeKey: resolved.RuntimeKey,
			Definition: resolved.Definition,
		}
		installed := (agentinstall.Checker{}).Check(agentinstall.Definition{
			Bin:        binding.Definition.Bin,
			ACPProgram: binding.Definition.ACPProgram,
		})
		if !installed.Installed {
			return nil, fmt.Errorf(
				"AgentID %q revision %q cannot create a session: %s",
				binding.AgentID,
				binding.Revision,
				installed.Error,
			)
		}
		validation, matched := probe.CachedAgentValidation(
			binding.AgentID,
			binding.Revision,
			h.settingsService.GetACPEnvVersion(binding.AgentID),
		)
		if !matched || !validation.Success {
			return nil, fmt.Errorf(
				"AgentID %q revision %q cannot create a session: availability=%s error=%s",
				binding.AgentID,
				binding.Revision,
				map[bool]string{true: "unavailable", false: "pending_validation"}[matched],
				validation.Error,
			)
		}
	}
	if binding != nil {
		if err := registerAgentRuntimeBinding(*binding); err != nil {
			return nil, err
		}
	}
	s, err := ss.New(modelID, agentType, workdir, binding)
	if err != nil {
		return nil, fmt.Errorf("create session: %w", err)
	}
	if err := ss.SetInitFields(s.ID, jobID, wsID); err != nil {
		return nil, fmt.Errorf("save session: %w", err)
	}
	return s, nil
}
