package handler

import (
	"context"
	"strconv"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/types/model"
)

func (h *Handler) GetModelList(ctx context.Context, c *app.RequestContext) {
	lang := string(c.Query("lang"))

	list, err := h.modelConfig.GetProviderModelList(ctx)
	if err != nil {
		httputil.InternalError(c, err.Error())
		return
	}
	list = sanitizeProviderModelList(list)
	if lang == "en" {
		localizeProviderModelListEN(list)
	}
	c.JSON(200, map[string]any{
		"code":                0,
		"provider_model_list": list,
	})
}

func sanitizeProviderModelList(list []*model.ProviderModelList) []*model.ProviderModelList {
	if list == nil {
		return nil
	}

	out := make([]*model.ProviderModelList, 0, len(list))
	for _, item := range list {
		if item == nil {
			continue
		}

		newItem := &model.ProviderModelList{
			Provider:  item.Provider,
			ModelList: make([]*model.ModelInstance, 0, len(item.ModelList)),
		}

		for _, m := range item.ModelList {
			if m == nil {
				continue
			}

			mCopy := *m
			if mCopy.Connection != nil {
				connCopy := *mCopy.Connection
				connCopy.APIKey = maskSecret(connCopy.APIKey)
				mCopy.Connection = &connCopy
			}
			newItem.ModelList = append(newItem.ModelList, &mCopy)
		}
		out = append(out, newItem)
	}
	return out
}

func localizeProviderModelListEN(list []*model.ProviderModelList) {
	for _, item := range list {
		if item == nil || item.Provider == nil {
			continue
		}
		if item.Provider.NameEN != "" {
			item.Provider.Name = item.Provider.NameEN
		}
		if item.Provider.DescriptionEN != "" {
			item.Provider.Description = item.Provider.DescriptionEN
		}
	}
}

func maskSecret(s string) string {
	if s == "" {
		return ""
	}
	if len(s) <= 4 {
		return "****"
	}
	if len(s) <= 8 {
		return strings.Repeat("*", len(s)-4) + s[len(s)-4:]
	}
	return s[:4] + strings.Repeat("*", len(s)-8) + s[len(s)-4:]
}

func (h *Handler) CreateModel(ctx context.Context, c *app.RequestContext) {
	var req model.CreateModelRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}

	if req.DisplayName == "" || req.Connection == nil || req.Connection.Model == "" {
		httputil.BadRequest(c, "display_name and connection.model are required")
		return
	}

	id, err := h.modelConfig.CreateModel(ctx, &req)
	if err != nil {
		httputil.InternalError(c, err.Error())
		return
	}

	c.JSON(200, map[string]any{
		"code": 0,
		"id":   id,
	})
}

func (h *Handler) DeleteModel(ctx context.Context, c *app.RequestContext) {
	idStr := c.Query("id")
	if idStr == "" {
		var body struct {
			ID string `json:"id"`
		}
		if err := c.BindJSON(&body); err == nil {
			idStr = body.ID
		}
	}

	if idStr == "" {
		httputil.BadRequest(c, "id is required")
		return
	}

	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		httputil.BadRequest(c, "invalid id")
		return
	}

	if err := h.modelConfig.DeleteModel(ctx, id); err != nil {
		httputil.InternalError(c, err.Error())
		return
	}

	c.JSON(200, map[string]any{
		"code": 0,
	})
}
