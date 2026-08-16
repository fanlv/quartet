package handler

import (
	"context"
	"errors"
	"net/http"
	"os"
	"strings"

	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/types/model"

	"github.com/cloudwego/hertz/pkg/app"
)

// WeChatSend persists proactive messages into the durable outbox. A single
// background worker owns chunking, rate limiting and retries; this request
// never calls WeChat directly.
//
// Recipients default to the WeChat admin whitelist (settings.wechat_admin_ids)
// when the request does not name explicit toUserIds.
func (h *Handler) WeChatSend(ctx context.Context, c *app.RequestContext) {
	var req model.WeChatSendMessageRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	content := strings.TrimSpace(req.Content)
	if content == "" {
		httputil.BadRequest(c, "content 必填")
		return
	}

	toUserIDs := make([]string, 0, len(req.ToUserIDs))
	seen := make(map[string]bool, len(req.ToUserIDs))
	for _, id := range req.ToUserIDs {
		id = strings.TrimSpace(id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		toUserIDs = append(toUserIDs, id)
	}
	if len(toUserIDs) == 0 {
		toUserIDs = h.settingsService.GetWeChatAdminIDs()
	}
	if len(toUserIDs) == 0 {
		httputil.BadRequest(c, "未指定收件人且微信 admin 白名单为空（settings.wechat_admin_ids）")
		return
	}

	if h.wechatOutbox == nil {
		httputil.InternalError(c, "微信发送队列未初始化")
		return
	}
	req.Content = content
	results, err := h.wechatOutbox.Enqueue(ctx, &req, toUserIDs)
	if err != nil {
		httputil.InternalError(c, err.Error())
		return
	}
	c.JSON(http.StatusAccepted, map[string]any{
		"code":    0,
		"results": results,
	})
}

func (h *Handler) WeChatOutboxGet(ctx context.Context, c *app.RequestContext) {
	taskID := strings.TrimSpace(c.Param("taskId"))
	if taskID == "" {
		httputil.BadRequest(c, "taskId 必填")
		return
	}
	if h.wechatOutbox == nil {
		httputil.InternalError(c, "微信发送队列未初始化")
		return
	}
	result, err := h.wechatOutbox.GetResult(ctx, taskID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			httputil.NotFound(c, "微信发送任务不存在: "+taskID)
			return
		}
		httputil.InternalError(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{
		"code":   0,
		"result": result,
	})
}

func (h *Handler) WeChatOutboxStatus(_ context.Context, c *app.RequestContext) {
	if h.wechatOutbox == nil {
		httputil.InternalError(c, "微信发送队列未初始化")
		return
	}
	c.JSON(http.StatusOK, map[string]any{
		"code":   0,
		"status": "ok",
	})
}
