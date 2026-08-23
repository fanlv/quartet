package handler

import (
	"context"
	"net/http"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/services/messagepreset"
	"github.com/fanlv/quartet/types/model"
)

func writeMessagePresetError(c *app.RequestContext, err error) {
	httputil.MapError(c, err, []httputil.ErrorMapping{
		{Err: messagepreset.ErrConflict, Status: http.StatusConflict},
		{Err: messagepreset.ErrNotFound, Status: http.StatusNotFound},
		{Err: messagepreset.ErrValidation, Status: http.StatusBadRequest},
	})
}

func (h *Handler) EffectiveMessagePresets(_ context.Context, c *app.RequestContext) {
	workspaceID := strings.TrimSpace(string(c.Query("workspaceId")))
	if workspaceID == "" {
		httputil.BadRequest(c, "workspaceId is required")
		return
	}
	result, err := h.messagePresetService.GetEffective(workspaceID)
	if err != nil {
		writeMessagePresetError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) GetGlobalMessagePresets(_ context.Context, c *app.RequestContext) {
	result, err := h.messagePresetService.GetGlobal()
	if err != nil {
		writeMessagePresetError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) SaveGlobalMessagePresets(_ context.Context, c *app.RequestContext) {
	var req model.SaveMessagePresetScopeRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Revision) == "" {
		httputil.BadRequest(c, "revision is required")
		return
	}
	result, err := h.messagePresetService.SaveGlobal(req)
	if err != nil {
		writeMessagePresetError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) GetWorkspaceMessagePresets(_ context.Context, c *app.RequestContext) {
	result, err := h.messagePresetService.GetWorkspace(c.Param("id"))
	if err != nil {
		writeMessagePresetError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) SaveWorkspaceMessagePresets(_ context.Context, c *app.RequestContext) {
	var req model.SaveMessagePresetScopeRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Revision) == "" {
		httputil.BadRequest(c, "revision is required")
		return
	}
	result, err := h.messagePresetService.SaveWorkspace(c.Param("id"), req)
	if err != nil {
		writeMessagePresetError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) ListOrphanMessagePresets(_ context.Context, c *app.RequestContext) {
	result, err := h.messagePresetService.ListOrphans()
	if err != nil {
		writeMessagePresetError(c, err)
		return
	}
	c.JSON(http.StatusOK, result)
}

func (h *Handler) DeleteOrphanMessagePresets(_ context.Context, c *app.RequestContext) {
	revision := strings.TrimSpace(string(c.Query("revision")))
	if revision == "" {
		httputil.BadRequest(c, "revision is required")
		return
	}
	if err := h.messagePresetService.DeleteOrphan(c.Param("id"), revision); err != nil {
		writeMessagePresetError(c, err)
		return
	}
	c.JSON(http.StatusOK, map[string]any{"code": 0})
}

func (h *Handler) RebindOrphanMessagePresets(_ context.Context, c *app.RequestContext) {
	var req model.RebindMessagePresetRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	if strings.TrimSpace(req.Revision) == "" {
		httputil.BadRequest(c, "revision is required")
		return
	}
	if strings.TrimSpace(req.TargetWorkspaceID) == "" {
		httputil.BadRequest(c, "targetWorkspaceId is required")
		return
	}
	if err := h.messagePresetService.RebindOrphan(c.Param("id"), req); err != nil {
		writeMessagePresetError(c, err)
		return
	}
	c.JSON(http.StatusOK, map[string]any{"code": 0})
}
