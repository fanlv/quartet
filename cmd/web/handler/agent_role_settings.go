package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/services/agent/probe"
	"github.com/fanlv/quartet/types/model"
)

type saveAgentEnvRequest struct {
	Entries []model.ACPEnvVarEntry `json:"entries"`
}

type saveAgentPrefsRequest struct {
	Prefs model.AgentPrefs `json:"prefs"`
}

func (h *Handler) GetTitleGenerationAgent(ctx context.Context, c *app.RequestContext) {
	settings, err := h.settingsService.GetSettings()
	if err != nil {
		httputil.InternalError(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{
		"code":   0,
		"config": settings.TitleGenerationAgent,
	})
}

func (h *Handler) SaveTitleGenerationAgent(ctx context.Context, c *app.RequestContext) {
	var req model.AgentRoleConfig
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	release, ok := h.acquireAgentSettingWrite(c, req.AgentID)
	if !ok {
		return
	}
	defer release()
	if err := h.settingsService.SaveTitleGenerationAgent(&req); err != nil {
		httputil.BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"code": 0})
}

func (h *Handler) GetGroupReplyAgent(ctx context.Context, c *app.RequestContext) {
	settings, err := h.settingsService.GetSettings()
	if err != nil {
		httputil.InternalError(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{
		"code":   0,
		"config": settings.GroupReplyAgent,
	})
}

func (h *Handler) SaveGroupReplyAgent(ctx context.Context, c *app.RequestContext) {
	var req model.AgentRoleConfig
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	release, ok := h.acquireAgentSettingWrite(c, req.AgentID)
	if !ok {
		return
	}
	defer release()
	if err := h.settingsService.SaveGroupReplyAgent(&req); err != nil {
		httputil.BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"code": 0})
}

func (h *Handler) GetIMSessionAgent(ctx context.Context, c *app.RequestContext) {
	settings, err := h.settingsService.GetSettings()
	if err != nil {
		httputil.InternalError(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{
		"code":   0,
		"config": settings.IMSessionAgent,
	})
}

func (h *Handler) SaveIMSessionAgent(ctx context.Context, c *app.RequestContext) {
	var req model.IMSessionAgentConfig
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	release, ok := h.acquireAgentSettingWrite(c, req.AgentID)
	if !ok {
		return
	}
	defer release()
	if err := h.settingsService.SaveIMSessionAgent(&req); err != nil {
		httputil.BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"code": 0})
}

func (h *Handler) SaveAgentEnvVars(ctx context.Context, c *app.RequestContext) {
	agentID := strings.TrimSpace(c.Param("agentId"))
	if agentID == "" {
		httputil.BadRequest(c, "agentId is required")
		return
	}
	var req saveAgentEnvRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	releaseManagement, err := customAgentManagement.reserve("save-environment", agentID, nil)
	if err != nil {
		httputil.Conflict(c, err.Error())
		return
	}
	defer releaseManagement()
	release, ok := h.acquireAgentSettingWrite(c, agentID)
	if !ok {
		return
	}
	defer release()
	version, changed, err := h.settingsService.SaveACPEnvVars(agentID, req.Entries)
	if err != nil {
		httputil.BadRequest(c, err.Error())
		return
	}
	if changed {
		h.acpAgentService.DeleteByAgentIdentifiers(probe.ACPAgentEnvLookupKeys(agentID))
		response := map[string]any{"code": 0, "version": version, "changed": true}
		if err := h.acpProbeCache.InvalidateAgentAndPersist(ctx, agentID); err != nil {
			response["warning"] = fmt.Sprintf(
				"environment was saved, but persisting the invalidated ACP validation cache failed: %v",
				err,
			)
		}
		if binding, found, resolveErr := h.agentCatalog.ResolveBinding(ctx, agentID, ""); resolveErr != nil {
			appendAgentSettingWarning(response, fmt.Sprintf(
				"environment was saved, but resolving the current Agent binding for asynchronous validation failed: %v",
				resolveErr,
			))
		} else if found {
			if !h.validateBindingAsyncWithExecution(ctx, binding, version) {
				appendAgentSettingWarning(response, fmt.Sprintf(
					"environment was saved, but AgentID %q entered deletion before asynchronous validation could start",
					agentID,
				))
			}
		} else {
			appendAgentSettingWarning(response, fmt.Sprintf(
				"environment was saved, but AgentID %q has no current runtime binding to validate",
				agentID,
			))
		}
		c.JSON(http.StatusOK, response)
		return
	}
	c.JSON(http.StatusOK, map[string]any{"code": 0, "version": version, "changed": false})
}

func appendAgentSettingWarning(response map[string]any, warning string) {
	if warning == "" {
		return
	}
	if previous, ok := response["warning"].(string); ok && previous != "" {
		response["warning"] = previous + "\n" + warning
		return
	}
	response["warning"] = warning
}

func (h *Handler) SaveAgentPrefs(ctx context.Context, c *app.RequestContext) {
	agentID := c.Param("agentId")
	if agentID == "" {
		httputil.BadRequest(c, "agentId is required")
		return
	}
	var req saveAgentPrefsRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	release, ok := h.acquireAgentSettingWrite(c, agentID)
	if !ok {
		return
	}
	defer release()
	if err := h.settingsService.SaveAgentPrefs(agentID, req.Prefs); err != nil {
		httputil.BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"code": 0})
}

func (h *Handler) acquireAgentSettingWrite(c *app.RequestContext, agentID string) (func(), bool) {
	if agentID == "" {
		return func() {}, true
	}
	release, ok := h.agentExecutions.acquireExecution(agentID)
	if !ok {
		httputil.Conflict(c, "AgentID "+agentID+" is deleting; settings update was rejected")
		return nil, false
	}
	return release, true
}
