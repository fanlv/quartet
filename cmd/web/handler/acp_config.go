package handler

import (
	"context"
	"fmt"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/services/agent/probe"
	"github.com/fanlv/quartet/types/model"
)

// SetACPConfig applies an ACP live-config switch (model / mode / thought_level)
// and returns the refreshed selector lists. With a sessionId it switches on
// that session's live ACP agent and persists the new selection to the session;
// without one it updates and refreshes the Home selector cache for agentType.
func (h *Handler) SetACPConfig(ctx context.Context, c *app.RequestContext) {
	var req model.SetACPConfigRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, err.Error())
		return
	}
	switch req.Target {
	case model.ACPConfigTargetModel, model.ACPConfigTargetMode, model.ACPConfigTargetThoughtLevel:
	default:
		httputil.BadRequest(c, fmt.Sprintf("invalid target %q", req.Target))
		return
	}

	var (
		state *model.ACPConfigState
		err   error
	)
	if req.SessionID != "" {
		state, err = h.setACPConfigForSession(ctx, &req)
	} else {
		state, err = h.setACPConfigPreview(ctx, &req)
	}
	if err != nil {
		httputil.InternalErrorLog(ctx, c, "[acp.config] set", err)
		return
	}

	c.JSON(http.StatusOK, model.SetACPConfigResponse{ACPConfigState: *state})
}

// setACPConfigForSession applies the switch on the session's live ACP agent
// and persists the new selection so the next Run re-applies it after
// reconnect / restart.
func (h *Handler) setACPConfigForSession(ctx context.Context, req *model.SetACPConfigRequest) (*model.ACPConfigState, error) {
	s, ok := h.lookupSession(req.SessionID)
	if !ok {
		return nil, fmt.Errorf("session not found: %s", req.SessionID)
	}
	ss, err := h.getOrCreateSessionService(s.WorkspaceID, s.JobID)
	if err != nil {
		return nil, fmt.Errorf("get session service: %w", err)
	}

	switch req.Target {
	case model.ACPConfigTargetModel:
		state, err := h.acpAgentService.SetModel(ctx, ss, s.WorkspaceID, s.JobID, s.ID, s.Type, s.Workdir, req.Model)
		if err != nil {
			return nil, err
		}
		probe.CacheACPConfigState(s.Type, req.Target, req.Model, s.ACPMode, s.ACPThoughtLevel, state)
		if err := ss.UpdateModelID(s.ID, req.Model); err != nil {
			logger.Errorf(ctx, "[acp.config] persist model failed: sessionId=%s err=%v", s.ID, err)
		}
		return state, nil
	case model.ACPConfigTargetMode:
		state, err := h.acpAgentService.SetMode(ctx, ss, s.WorkspaceID, s.JobID, s.ID, s.Type, s.Workdir, req.Mode)
		if err != nil {
			return nil, err
		}
		probe.CacheACPConfigState(s.Type, req.Target, s.ModelID, req.Mode, s.ACPThoughtLevel, state)
		if err := ss.UpdateACPMode(s.ID, req.Mode); err != nil {
			logger.Errorf(ctx, "[acp.config] persist mode failed: sessionId=%s err=%v", s.ID, err)
		}
		return state, nil
	case model.ACPConfigTargetThoughtLevel:
		state, err := h.acpAgentService.SetThoughtLevel(ctx, ss, s.WorkspaceID, s.JobID, s.ID, s.Type, s.Workdir, req.ThoughtLevel)
		if err != nil {
			return nil, err
		}
		probe.CacheACPConfigState(s.Type, req.Target, s.ModelID, s.ACPMode, req.ThoughtLevel, state)
		if err := ss.UpdateACPThoughtLevel(s.ID, req.ThoughtLevel); err != nil {
			logger.Errorf(ctx, "[acp.config] persist thought_level failed: sessionId=%s err=%v", s.ID, err)
		}
		return state, nil
	}
	return nil, fmt.Errorf("invalid target %q", req.Target)
}

// setACPConfigPreview runs a Home (session-less) cache selection. Cache hits
// return immediately and refresh asynchronously; misses probe synchronously.
func (h *Handler) setACPConfigPreview(ctx context.Context, req *model.SetACPConfigRequest) (*model.ACPConfigState, error) {
	if req.AgentType == "" {
		return nil, fmt.Errorf("agentType is required for a session-less config switch")
	}
	sel := probe.PreviewSelection{
		Model:        req.Model,
		Mode:         req.Mode,
		ThoughtLevel: req.ThoughtLevel,
	}
	switch req.Target {
	case model.ACPConfigTargetModel:
		sel.Target = probe.PreviewTargetModel
	case model.ACPConfigTargetMode:
		sel.Target = probe.PreviewTargetMode
	case model.ACPConfigTargetThoughtLevel:
		sel.Target = probe.PreviewTargetThoughtLevel
	}
	return probe.PreviewSetConfig(ctx, req.AgentType, sel)
}
