package handler

import (
	"context"
	"strings"

	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/messaging"
	"github.com/fanlv/quartet/types/model"

	"github.com/cloudwego/hertz/pkg/app"
)

// WeChatSend handles POST /api/v1/wechat/send — the proactive WeChat push
// entry point for scheduled jobs / scripts (as opposed to the reply path,
// which is driven by an incoming message). Content is chunked with the same
// UTF-8 safe splitter the reply path uses, then sent via Replier.SendText.
//
// Recipients default to the WeChat admin whitelist (settings.wechat_admin_ids)
// when the request does not name explicit toUserIds. Per-recipient failures
// are reported in full: any failure flips the status to 500 with every error
// in the body, so callers (quartet-cli, workflow prompts) can surface them
// verbatim.
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

	if h.imGateway == nil {
		httputil.InternalError(c, "IM 网关未初始化")
		return
	}
	r := h.imGateway.replier(messaging.PlatformWeChat)
	if r == nil {
		c.JSON(503, map[string]any{
			"code":  1,
			"error": "微信未登录或无可用凭证，请先在设置页扫码登录微信",
		})
		return
	}

	chunks := splitReplyContent(content, maxReplyChunkBytesForPlatform(messaging.PlatformWeChat))
	results := make([]model.WeChatSendResult, 0, len(toUserIDs))
	failed := false
	for _, toUserID := range toUserIDs {
		res := model.WeChatSendResult{ToUserID: toUserID}
		for i, chunk := range chunks {
			if err := r.SendText(ctx, toUserID, chunk); err != nil {
				res.Error = err.Error()
				logger.Errorf(ctx, "[wechat] proactive send failed: to=%s chunk=%d/%d err=%v", toUserID, i+1, len(chunks), err)
				break
			}
			res.Chunks++
		}
		if res.Error != "" {
			failed = true
		}
		results = append(results, res)
	}

	status := 200
	if failed {
		status = 500
	}
	c.JSON(status, map[string]any{
		"code":    0,
		"results": results,
	})
}
