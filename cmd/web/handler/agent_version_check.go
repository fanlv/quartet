package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/fanlv/quartet/pkg/httputil"
	agentinstall "github.com/fanlv/quartet/services/agent/install"
	"github.com/fanlv/quartet/services/agent/probe"
	"github.com/fanlv/quartet/types/model"
)

// AgentVersionCheck returns the installed component versions for the Agent
// management directory. `force=1` bypasses the short process-local cache for
// the explicit "check again" action in the UI.
func (h *Handler) AgentVersionCheck(ctx context.Context, c *app.RequestContext) {
	c.Header("Cache-Control", "no-store")
	force := string(c.Query("force")) == "1"
	agents, checkedAt, err := h.agentVersions.Check(ctx, force)
	if err != nil {
		httputil.InternalErrorLog(ctx, c, "agent.versions", err)
		return
	}
	c.JSON(http.StatusOK, model.AgentVersionCheckResponse{
		Code:      0,
		CheckedAt: checkedAt,
		Agents:    agents,
	})
}

// AgentUpgrade re-runs one installed built-in Agent's catalog-controlled
// install flow. No command or package name is accepted from the client.
func (h *Handler) AgentUpgrade(ctx context.Context, c *app.RequestContext) {
	agentID := c.Param("agentId")
	if agentID == "" {
		httputil.BadRequest(c, "agentId is required")
		return
	}

	result, err := h.acpProbeCache.UpgradeBuiltinAgent(ctx, agentID)
	if err != nil {
		switch {
		case errors.Is(err, probe.ErrUnknownAgentID),
			errors.Is(err, probe.ErrAgentDeprecated),
			errors.Is(err, probe.ErrManualInstallOnly),
			errors.Is(err, probe.ErrAgentNotInstalled):
			httputil.BadRequest(c, err.Error())
		case errors.Is(err, agentinstall.ErrInstallInFlight):
			httputil.Conflict(c, err.Error())
		default:
			httputil.InternalErrorLog(ctx, c, "agent.upgrade", err)
		}
		return
	}
	h.agentVersions.Invalidate()
	c.JSON(http.StatusOK, model.AgentInstallResponse{Code: 0, Result: result})
}
