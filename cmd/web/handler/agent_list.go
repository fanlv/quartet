package handler

import (
	"context"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/services/agent/catalog"
	agentinstall "github.com/fanlv/quartet/services/agent/install"
	"github.com/fanlv/quartet/services/agent/probe"
	"github.com/fanlv/quartet/types/consts"
	"github.com/fanlv/quartet/types/model"
)

func (h *Handler) AgentList(ctx context.Context, c *app.RequestContext) {
	_, err := h.acpProbeCache.LoadPersisted(ctx)
	// Every list request attempts a live refresh. It never delays this response;
	// concurrent requests coalesce into the refresh already in flight.
	h.acpProbeCache.RefreshAsync(ctx)
	if err != nil {
		logger.Errorf(ctx, "[agent.list] load persisted ACP cache failed: %v", err)
		httputil.InternalError(c, err.Error())
		return
	}

	agentList := make([]model.AgentInfo, 0)
	for _, a := range probe.InstalledACPAgents() {
		binding := catalog.BindingForBuiltin(a)
		envVersion := h.settingsService.GetACPEnvVersion(a.AgentID)
		validation, matched := probe.CachedAgentValidation(a.AgentID, binding.Revision, envVersion)
		availability := "pending_validation"
		available := false
		if matched {
			if validation.Refreshing && validation.RefreshedAt == 0 {
				availability = "validating"
			} else if validation.Success {
				availability = "available"
				available = true
			} else {
				availability = "unavailable"
			}
		}
		capabilities := []string{"acp"}
		if a.SupportsHeadlessPrint {
			capabilities = append(capabilities, "headless_print")
		}
		info := model.AgentInfo{
			AgentID:       a.AgentID,
			Revision:      binding.Revision,
			Type:          a.AgentID,
			EnvKey:        probe.ACPAgentEnvKey(a.Command),
			ModelID:       "",
			DisplayName:   a.DisplayName,
			IconURL:       a.IconURL,
			Availability:  availability,
			Available:     available,
			Refreshing:    validation.Refreshing,
			Error:         validation.Error,
			Capabilities:  capabilities,
			Models:        validation.Models,
			Modes:         validation.Modes,
			ThoughtLevels: validation.ThoughtLevels,
		}
		agentList = append(agentList, info)
	}
	customAgents, customErr := h.agentCatalog.Custom(ctx)
	if customErr != nil {
		httputil.InternalErrorLog(ctx, c, "agent.list.custom", customErr)
		return
	}
	checker := agentinstall.Checker{}
	for _, agent := range customAgents {
		if agent.Lifecycle != model.AgentLifecycleActive {
			continue
		}
		binding, bindErr := catalog.BindingForCustom(agent, agent.CurrentRevision)
		if bindErr != nil {
			logger.Errorf(ctx, "[agent.list] resolve custom Agent binding failed: agentId=%s err=%v", agent.AgentID, bindErr)
			continue
		}
		installed := checker.Check(agentinstall.Definition{
			Bin:        binding.Definition.Bin,
			ACPProgram: binding.Definition.ACPProgram,
		})
		if !installed.Installed {
			continue
		}
		envVersion := h.settingsService.GetACPEnvVersion(agent.AgentID)
		validation, matched := probe.CachedAgentValidation(agent.AgentID, binding.Revision, envVersion)
		availability := "pending_validation"
		available := false
		if matched {
			if validation.Refreshing && validation.RefreshedAt == 0 {
				availability = "validating"
			} else if validation.Success {
				availability = "available"
				available = true
			} else {
				availability = "unavailable"
			}
		}
		if !matched {
			h.validateBindingAsyncWithExecution(ctx, binding, envVersion)
		}
		capabilities := []string{"acp"}
		if agent.SupportsHeadlessPrint {
			capabilities = append(capabilities, "headless_print")
		}
		agentList = append(agentList, model.AgentInfo{
			AgentID:       agent.AgentID,
			Revision:      binding.Revision,
			Type:          agent.AgentID,
			EnvKey:        agent.AgentID,
			DisplayName:   agent.DisplayName,
			IconURL:       agent.IconURL,
			Availability:  availability,
			Available:     available,
			Refreshing:    validation.Refreshing,
			Error:         validation.Error,
			Capabilities:  capabilities,
			Models:        validation.Models,
			Modes:         validation.Modes,
			ThoughtLevels: validation.ThoughtLevels,
		})
	}

	workdir, err := defaultBrowseRoot()
	if err != nil {
		// LOCAL_MEMORY is required at boot, so allowedRoots() normally
		// seeds a usable directory; if we still end up here, log loudly
		// — an empty Workdir in the response silently blanks the
		// DirPicker and is otherwise hard to trace back.
		logger.Warnf(ctx, "[agent.list] defaultBrowseRoot failed (workdir will be empty in response): %v", err)
	}
	// Probe-only: this drives the response's JobEnable flag, not request
	// gating, so call the pure form and silently accept failure. The
	// public /api/v1/public/agent/list route in particular is reached
	// without an agent token (it is gated by shareToken instead), and
	// noisy ERRORs here would obscure real auth failures.
	token := string(c.GetHeader(consts.HeaderAgentAuth))
	jobEnable := CheckAgentAuth(token)
	c.JSON(http.StatusOK, model.AgentListResponse{
		Code:      0,
		AgentList: agentList,
		Workdir:   workdir,
		JobEnable: jobEnable,
	})
}

func (h *Handler) refreshCustomAgentValidations(ctx context.Context) {
	customAgents, err := h.agentCatalog.Custom(ctx)
	if err != nil {
		logger.Errorf(ctx, "[agent.list] load custom Agents for refresh failed: %v", err)
		return
	}
	checker := agentinstall.Checker{}
	for _, agent := range customAgents {
		if agent.Lifecycle != model.AgentLifecycleActive {
			continue
		}
		binding, err := catalog.BindingForCustom(agent, agent.CurrentRevision)
		if err != nil {
			logger.Errorf(ctx, "[agent.list] resolve custom Agent for refresh failed: agentId=%s err=%v", agent.AgentID, err)
			continue
		}
		if installed := checker.Check(agentinstall.Definition{
			Bin:        binding.Definition.Bin,
			ACPProgram: binding.Definition.ACPProgram,
		}); !installed.Installed {
			continue
		}
		h.validateBindingAsyncWithExecution(
			ctx,
			binding,
			h.settingsService.GetACPEnvVersion(agent.AgentID),
		)
	}
}

func (h *Handler) validateBindingAsyncWithExecution(
	ctx context.Context,
	binding model.AgentRuntimeBinding,
	envVersion int64,
) bool {
	release, acquired := h.agentExecutions.acquireExecution(binding.AgentID)
	if !acquired {
		return false
	}
	h.acpProbeCache.ValidateBindingAsync(ctx, binding, envVersion, release)
	return true
}

func (h *Handler) PublicAgentList(ctx context.Context, c *app.RequestContext) {
	_, err := h.acpProbeCache.LoadPersisted(ctx)
	if err != nil {
		httputil.InternalErrorLog(ctx, c, "agent.public-list", err)
		return
	}
	publicAgents := make([]model.PublicAgentInfo, 0)
	for _, builtin := range probe.InstalledACPAgents() {
		binding := catalog.BindingForBuiltin(builtin)
		validation, matched := probe.CachedAgentValidation(
			builtin.AgentID,
			binding.Revision,
			h.settingsService.GetACPEnvVersion(builtin.AgentID),
		)
		if !matched || !validation.Success {
			continue
		}
		publicAgents = append(publicAgents, model.PublicAgentInfo{
			AgentID:       builtin.AgentID,
			DisplayName:   builtin.DisplayName,
			IconURL:       builtin.IconURL,
			Models:        validation.Models,
			Modes:         validation.Modes,
			ThoughtLevels: validation.ThoughtLevels,
		})
	}
	custom, err := h.agentCatalog.Custom(ctx)
	if err != nil {
		httputil.InternalErrorLog(ctx, c, "agent.public-list.custom", err)
		return
	}
	for _, agent := range custom {
		if agent.Lifecycle != model.AgentLifecycleActive {
			continue
		}
		binding, err := catalog.BindingForCustom(agent, agent.CurrentRevision)
		if err != nil {
			continue
		}
		installed := (agentinstall.Checker{}).Check(agentinstall.Definition{
			Bin:        binding.Definition.Bin,
			ACPProgram: binding.Definition.ACPProgram,
		})
		if !installed.Installed {
			continue
		}
		validation, matched := probe.CachedAgentValidation(
			agent.AgentID,
			binding.Revision,
			h.settingsService.GetACPEnvVersion(agent.AgentID),
		)
		if !matched || !validation.Success {
			continue
		}
		publicAgents = append(publicAgents, model.PublicAgentInfo{
			AgentID:       agent.AgentID,
			DisplayName:   agent.DisplayName,
			IconURL:       agent.IconURL,
			Models:        validation.Models,
			Modes:         validation.Modes,
			ThoughtLevels: validation.ThoughtLevels,
		})
	}
	c.JSON(http.StatusOK, model.PublicAgentListResponse{
		Code:      0,
		AgentList: publicAgents,
	})
}
