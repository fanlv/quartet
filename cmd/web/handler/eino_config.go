package handler

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/types/model"
)

// Eino config handlers proxy the settings page's eino tab to the standalone
// eino-cli binary (see services/einocli). quartet stores nothing itself.

func (h *Handler) GetEinoModelList(ctx context.Context, c *app.RequestContext) {
	models, err := h.einoCLI.ListModels(ctx)
	if err != nil {
		httputil.InternalError(c, err.Error())
		return
	}
	c.JSON(200, map[string]any{
		"code":   0,
		"models": models,
	})
}

func (h *Handler) CreateEinoModel(ctx context.Context, c *app.RequestContext) {
	var req model.CreateEinoModelRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request body: "+err.Error())
		return
	}
	if req.DisplayName == "" {
		httputil.BadRequest(c, "display_name is required")
		return
	}
	if req.ModelClass == "" {
		httputil.BadRequest(c, "model_class is required")
		return
	}
	if req.Connection == nil || req.Connection.Model == "" {
		httputil.BadRequest(c, "connection.model is required")
		return
	}
	created, err := h.einoCLI.AddModel(ctx, &req)
	if err != nil {
		httputil.InternalError(c, err.Error())
		return
	}
	c.JSON(200, map[string]any{
		"code":  0,
		"model": created,
	})
}

func (h *Handler) DeleteEinoModel(ctx context.Context, c *app.RequestContext) {
	id := c.Param("modelId")
	if id == "" {
		httputil.BadRequest(c, "model id is required")
		return
	}
	if err := h.einoCLI.DeleteModel(ctx, id); err != nil {
		httputil.InternalError(c, err.Error())
		return
	}
	c.JSON(200, map[string]any{"code": 0})
}

func (h *Handler) GetEinoSystemPrompt(ctx context.Context, c *app.RequestContext) {
	prompt, err := h.einoCLI.GetSystemPrompt(ctx)
	if err != nil {
		httputil.InternalError(c, err.Error())
		return
	}
	c.JSON(200, map[string]any{
		"code":          0,
		"system_prompt": prompt,
	})
}

func (h *Handler) SaveEinoSystemPrompt(ctx context.Context, c *app.RequestContext) {
	var req model.SaveEinoSystemPromptRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request body: "+err.Error())
		return
	}
	if err := h.einoCLI.SetSystemPrompt(ctx, req.SystemPrompt); err != nil {
		httputil.InternalError(c, err.Error())
		return
	}
	c.JSON(200, map[string]any{
		"code":          0,
		"system_prompt": req.SystemPrompt,
	})
}
