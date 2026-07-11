package handler

import (
	"context"
	"net/http"
	"strconv"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/services/agent/probe"
	"github.com/fanlv/quartet/types/consts"
	"github.com/fanlv/quartet/types/model"
)

func (h *Handler) AgentList(ctx context.Context, c *app.RequestContext) {
	list, err := h.modelConfig.GetProviderModelList(ctx)
	if err != nil {
		logger.Errorf(ctx, "[agent.list] get provider model list failed: %v", err)
		httputil.InternalError(c, err.Error())
		return
	}

	persisted, err := h.acpProbeCache.LoadPersisted(ctx)
	// Every list request attempts a live refresh. It never delays this response;
	// concurrent requests coalesce into the refresh already in flight.
	h.acpProbeCache.RefreshAsync(ctx)
	if err != nil {
		logger.Errorf(ctx, "[agent.list] load persisted ACP cache failed: %v", err)
		httputil.InternalError(c, err.Error())
		return
	}

	agentList := make([]model.AgentInfo, 0, len(list))
	for _, a := range probe.InstalledACPAgents() {
		cached, ok := persisted.Entries[a.Command]
		if !ok {
			continue
		}
		info := model.AgentInfo{
			Type:          a.Command,
			EnvKey:        probe.ACPAgentEnvKey(a.Command),
			ModelID:       "",
			DisplayName:   a.DisplayName,
			IconURL:       a.IconURL,
			Models:        cached.Models,
			Modes:         cached.Modes,
			ThoughtLevels: cached.ThoughtLevels,
		}
		agentList = append(agentList, info)
	}

	for _, provider := range list {
		for _, m := range provider.ModelList {
			agentList = append(agentList, model.AgentInfo{
				Type:        consts.AgentTypeEino,
				ModelID:     strconv.FormatInt(m.ID, 10),
				DisplayName: m.DisplayName,
				IconURL:     provider.Provider.IconURL,
			})
		}
	}

	einoWorkdir := probe.EinoWorkdir()

	// Sandbox availability used to come from a process-global URL probe.
	// With per-workspace containers there is no meaningful global answer
	// anymore; the list page doesn't know which workspace the user is
	// about to pick. The flag is kept in the response for backwards
	// compatibility with the frontend and always reports false — chat
	// pages that care can query per-workspace status separately.
	sandboxUnavailable := false

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
		Code:               0,
		AgentList:          agentList,
		Workdir:            workdir,
		EinoWorkdir:        einoWorkdir,
		SandboxUnavailable: sandboxUnavailable,
		JobEnable:          jobEnable,
	})
}
