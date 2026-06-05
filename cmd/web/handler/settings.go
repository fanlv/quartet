package handler

import (
	"context"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/fanlv/quartet/pkg/httputil"
	larklisten "github.com/fanlv/quartet/pkg/messaging/lark"
	"github.com/fanlv/quartet/repository"
)

func (h *Handler) GetSettings(ctx context.Context, c *app.RequestContext) {
	settings, err := h.settingsService.GetSettings()
	if err != nil {
		httputil.InternalError(c, err.Error())
		return
	}
	c.JSON(200, map[string]any{
		"code":     0,
		"settings": settings,
	})
}

func (h *Handler) SaveSettings(ctx context.Context, c *app.RequestContext) {
	var req repository.Settings
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}

	larkConfigChanged := false
	if req.LarkAppID != "" || req.LarkAppSecret != "" {
		if req.LarkAppID == "" || req.LarkAppSecret == "" {
			httputil.BadRequest(c, "Lark App ID 和 App Secret 需要同时设置")
			return
		}
		old, _ := h.settingsService.GetSettings()
		if old == nil || old.LarkAppID != req.LarkAppID || old.LarkAppSecret != req.LarkAppSecret {
			if err := larklisten.ValidateCredentials(ctx, req.LarkAppID, req.LarkAppSecret); err != nil {
				httputil.BadRequest(c, "Lark 凭据验证失败: "+err.Error())
				return
			}
			larkConfigChanged = true
		}
	} else {
		old, _ := h.settingsService.GetSettings()
		if old != nil && (old.LarkAppID != "" || old.LarkAppSecret != "") {
			larkConfigChanged = true
		}
	}

	if err := h.settingsService.SaveSettings(&req); err != nil {
		httputil.InternalError(c, err.Error())
		return
	}

	if larkConfigChanged && h.larkManager != nil {
		h.larkManager.Restart()
	}

	c.JSON(200, map[string]any{
		"code": 0,
	})
}
