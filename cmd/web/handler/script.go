package handler

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/types/model"
)

func (h *Handler) SaveScript(ctx context.Context, c *app.RequestContext) {
	var req model.SaveScriptRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}

	if req.Name == "" {
		httputil.BadRequest(c, "name is required")
		return
	}

	if req.Content == "" {
		httputil.BadRequest(c, "content is required")
		return
	}

	script, err := h.scriptService.Save(ctx, &req)
	if err != nil {
		httputil.InternalError(c, err.Error())
		return
	}

	c.JSON(200, model.SaveScriptResponse{
		Code:   0,
		Script: script,
	})
}

func (h *Handler) GetScript(ctx context.Context, c *app.RequestContext) {
	id := c.Param("scriptId")
	if id == "" {
		httputil.BadRequest(c, "scriptId is required")
		return
	}

	script, err := h.scriptService.Get(ctx, id)
	if err != nil {
		httputil.InternalError(c, err.Error())
		return
	}

	if script == nil {
		httputil.NotFound(c, "script not found")
		return
	}

	c.JSON(200, map[string]any{
		"code":   0,
		"script": script,
	})
}

func (h *Handler) ListScripts(ctx context.Context, c *app.RequestContext) {
	scripts, err := h.scriptService.List(ctx)
	if err != nil {
		httputil.InternalError(c, err.Error())
		return
	}

	if scripts == nil {
		scripts = []*model.Script{}
	}

	c.JSON(200, model.ListScriptsResponse{
		Code:    0,
		Scripts: scripts,
	})
}

func (h *Handler) DeleteScript(ctx context.Context, c *app.RequestContext) {
	id := c.Param("scriptId")
	if id == "" {
		httputil.BadRequest(c, "scriptId is required")
		return
	}

	if err := h.scriptService.Delete(ctx, id); err != nil {
		httputil.InternalError(c, err.Error())
		return
	}

	c.JSON(200, model.DeleteScriptResponse{
		Code: 0,
	})
}
