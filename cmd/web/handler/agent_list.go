package handler

import (
	"context"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/services/agent/probe"
	"github.com/fanlv/quartet/types/consts"
	"github.com/fanlv/quartet/types/model"
)

func (h *Handler) AgentList(ctx context.Context, c *app.RequestContext) {
	persisted, err := h.acpProbeCache.LoadPersisted(ctx)
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
		// 权威数据源是进程内探测缓存：某个 agent 一旦探测成功就立刻出现在
		// 列表里，无需等被 10 分钟节流的磁盘快照落盘。内存尚未预热时(刚重启、
		// 后台首刷未完成)退回磁盘快照,避免已知 agent 在列表里短暂消失;两者都
		// 没有才跳过。
		models, modes, thoughtLevels, ok := probe.CachedACPSessionInfo(a.Command)
		if !ok {
			cached, disk := persisted.Entries[a.Command]
			if !disk {
				continue
			}
			models, modes, thoughtLevels = cached.Models, cached.Modes, cached.ThoughtLevels
		}
		info := model.AgentInfo{
			Type:          a.Command,
			EnvKey:        probe.ACPAgentEnvKey(a.Command),
			ModelID:       "",
			DisplayName:   a.DisplayName,
			IconURL:       a.IconURL,
			Models:        models,
			Modes:         modes,
			ThoughtLevels: thoughtLevels,
		}
		agentList = append(agentList, info)
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
