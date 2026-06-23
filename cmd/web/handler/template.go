package handler

import (
	"context"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/fanlv/quartet/pkg/httputil"
	templatesvc "github.com/fanlv/quartet/services/template"
	"github.com/fanlv/quartet/types/model"
)

func (h *Handler) SaveTemplate(ctx context.Context, c *app.RequestContext) {
	var req model.SaveTemplateRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}

	if req.Name == "" {
		httputil.BadRequest(c, "name is required")
		return
	}

	tmpl, err := h.templateService.Save(ctx, &req)
	if err != nil {
		httputil.MapError(c, err, []httputil.ErrorMapping{{Err: templatesvc.ErrInvalidTemplateConfig, Status: http.StatusBadRequest}})
		return
	}

	c.JSON(200, model.SaveTemplateResponse{
		Code:     0,
		Template: tmpl,
	})
}

func (h *Handler) UpdateTemplate(ctx context.Context, c *app.RequestContext) {
	id := c.Param("templateId")
	if id == "" {
		httputil.BadRequest(c, "templateId is required")
		return
	}

	var req model.UpdateTemplateRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}

	if req.Name == "" {
		httputil.BadRequest(c, "name is required")
		return
	}

	tmpl, err := h.templateService.Update(ctx, id, &req)
	if err != nil {
		httputil.MapError(c, err, []httputil.ErrorMapping{
			{Err: templatesvc.ErrTemplateNotFound, Status: http.StatusNotFound},
			{Err: templatesvc.ErrInvalidTemplateConfig, Status: http.StatusBadRequest},
		})
		return
	}

	c.JSON(200, model.UpdateTemplateResponse{
		Code:     0,
		Template: tmpl,
	})
}

func (h *Handler) ListTemplates(ctx context.Context, c *app.RequestContext) {
	templates, err := h.templateService.List(ctx)
	if err != nil {
		httputil.InternalError(c, err.Error())
		return
	}

	if templates == nil {
		templates = []*model.LoopTemplate{}
	}

	c.JSON(200, model.ListTemplatesResponse{
		Code:      0,
		Templates: templates,
	})
}

func (h *Handler) DeleteTemplate(ctx context.Context, c *app.RequestContext) {
	id := c.Param("templateId")
	if id == "" {
		httputil.BadRequest(c, "templateId is required")
		return
	}

	if err := h.templateService.Delete(ctx, id); err != nil {
		httputil.InternalError(c, err.Error())
		return
	}

	c.JSON(200, model.DeleteTemplateResponse{
		Code: 0,
	})
}
