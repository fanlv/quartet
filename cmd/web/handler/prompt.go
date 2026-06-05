package handler

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/types/model"
)

func (h *Handler) GetPrompt(ctx context.Context, c *app.RequestContext) {
	var req model.GetPromptRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}

	if req.Key == "" {
		httputil.BadRequest(c, "key is required")
		return
	}

	content, err := h.promptService.GetPrompt(ctx, req.Key)
	if err != nil {
		httputil.InternalError(c, err.Error())
		return
	}

	// Resolve the on-disk location (empty for non-file-backed keys) so
	// the settings UI can show "saved to <path>" without hardcoding any
	// filesystem layout on the frontend.
	filePath, _ := h.promptService.PromptFilePath(req.Key)

	c.JSON(200, model.GetPromptResponse{
		Code:   0,
		Prompt: content,
		Path:   filePath,
	})
}

func (h *Handler) SavePrompt(ctx context.Context, c *app.RequestContext) {
	var req model.SavePromptRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}

	if req.Key == "" {
		httputil.BadRequest(c, "key is required")
		return
	}

	if err := h.promptService.SavePrompt(ctx, req.Key, req.Prompt); err != nil {
		httputil.InternalError(c, err.Error())
		return
	}

	c.JSON(200, model.SavePromptResponse{
		Code: 0,
	})
}
