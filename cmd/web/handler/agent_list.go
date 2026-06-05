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

	agentList := make([]model.AgentInfo, 0, len(list))
	for _, a := range probe.InstalledACPAgents() {
		info := model.AgentInfo{
			Type:        a.Command,
			ModelID:     "",
			DisplayName: a.DisplayName,
			IconURL:     a.IconURL,
		}
		if models, modes := probe.GetACPSessionInfo(a.Command); models != nil || modes != nil {
			info.Models = models
			info.Modes = modes
		}
		if info.Modes != nil {
			if id := probe.PickDefaultModeID(info.Modes.AvailableModes); id != "" {
				info.Modes.CurrentModeId = id
			}
		}
		agentList = append(agentList, info)
	}

	probe.RefreshACPSessionCacheAsync(ctx)
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
