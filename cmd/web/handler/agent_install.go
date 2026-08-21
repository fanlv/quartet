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

// AgentInstallCandidates lists built-in agents that are not installed and not
// deprecated, with their preset install method and instructions. Manual-only
// entries are included (the UI shows their guide instead of an install
// button).
func (h *Handler) AgentInstallCandidates(ctx context.Context, c *app.RequestContext) {
	checker := agentinstall.Checker{}
	candidates := make([]model.AgentInstallCandidate, 0)
	for _, a := range probe.KnownACPAgents {
		if a.Deprecated {
			continue
		}
		status := checker.Check(agentinstall.Definition{Bin: a.Bin, ACPProgram: a.ACPProgram})
		if status.Installed {
			continue
		}
		candidates = append(candidates, model.AgentInstallCandidate{
			AgentID:         a.AgentID,
			Bin:             a.Bin,
			ACPProgram:      a.ACPProgram,
			Command:         a.Command,
			DisplayName:     a.DisplayName,
			IconURL:         IconCacheURL(a.IconURL),
			InstallMethod:   string(a.Install.Method),
			InstallCommands: a.Install.StepDisplays(),
			Instructions:    a.Install.Instructions,
			AutoInstallable: a.Install.AutoInstallable(),
		})
	}
	c.JSON(http.StatusOK, model.AgentInstallCandidatesResponse{Code: 0, Candidates: candidates})
}

// AgentInstall runs the preset install flow for one built-in agent. The
// request only carries an AgentID; the executed commands always come from the
// catalog. The executed steps' full output, the install recheck, and the ACP
// validation result are returned verbatim in the result payload.
func (h *Handler) AgentInstall(ctx context.Context, c *app.RequestContext) {
	var req model.AgentInstallRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	if req.AgentID == "" {
		httputil.BadRequest(c, "agent_id is required")
		return
	}

	result, err := h.acpProbeCache.InstallBuiltinAgent(ctx, req.AgentID)
	if err != nil {
		switch {
		case errors.Is(err, probe.ErrUnknownAgentID):
			httputil.BadRequest(c, err.Error())
		case errors.Is(err, probe.ErrAgentDeprecated), errors.Is(err, probe.ErrManualInstallOnly):
			httputil.BadRequest(c, err.Error())
		case errors.Is(err, agentinstall.ErrInstallInFlight):
			httputil.Conflict(c, err.Error())
		default:
			httputil.InternalErrorLog(ctx, c, "agent.install", err)
		}
		return
	}
	h.agentVersions.Invalidate()
	c.JSON(http.StatusOK, model.AgentInstallResponse{Code: 0, Result: result})
}

// AgentUninstall runs the automatic uninstall flow for one built-in agent
// (npm-method only). The request only carries the AgentID in the route; the
// executed `npm uninstall -g` commands are derived from the catalog. The
// executed steps' full output and the post-uninstall recheck are returned
// verbatim in the result payload.
func (h *Handler) AgentUninstall(ctx context.Context, c *app.RequestContext) {
	agentID := c.Param("agentId")
	if agentID == "" {
		httputil.BadRequest(c, "agentId is required")
		return
	}

	result, err := h.acpProbeCache.UninstallBuiltinAgent(ctx, agentID)
	if err != nil {
		switch {
		case errors.Is(err, probe.ErrUnknownAgentID), errors.Is(err, probe.ErrNotUninstallable):
			httputil.BadRequest(c, err.Error())
		case errors.Is(err, agentinstall.ErrInstallInFlight):
			httputil.Conflict(c, err.Error())
		default:
			httputil.InternalErrorLog(ctx, c, "agent.uninstall", err)
		}
		return
	}
	h.agentVersions.Invalidate()
	c.JSON(http.StatusOK, model.AgentInstallResponse{Code: 0, Result: result})
}
