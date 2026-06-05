package handler

import (
	"context"
	"strings"
	"time"

	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/messaging"
	wechatlisten "github.com/fanlv/quartet/pkg/messaging/wechat"
	"github.com/fanlv/quartet/pkg/messaging/wechat/ilink"

	"github.com/cloudwego/hertz/pkg/app"
)

// loginStatusTimeout bounds how long /wechat/login/status blocks waiting for
// the user to scan and confirm. The frontend polls repeatedly with this
// budget per call, so long-standing tabs stay alive across several polls.
const loginStatusTimeout = 90 * time.Second

// WeChatLoginStart triggers an iLink get_bot_qrcode call and returns a PNG
// QR code the frontend can display. The returned `qrcode` handle is passed
// back to /wechat/login/status to poll for confirmation.
func (h *Handler) WeChatLoginStart(ctx context.Context, c *app.RequestContext) {
	qrcode, imgBase64, err := wechatlisten.StartLogin(ctx)
	if err != nil {
		logger.Errorf(ctx, "[wechat] StartLogin failed: %v", err)
		httputil.InternalError(c, "获取二维码失败: "+err.Error())
		return
	}

	c.JSON(200, map[string]any{
		"code":       0,
		"qrcode":     qrcode,
		"img_base64": imgBase64,
	})
}

// WeChatLoginStatus long-polls iLink for the scan-to-login confirmation and
// returns as soon as a meaningful state change occurs:
//   - "wait"      — no scan yet (or 90s budget elapsed); frontend re-polls.
//   - "scaned"    — user scanned the QR on their phone but hasn't tapped
//     confirm; frontend shows "已扫描，请在手机上确认" and re-polls.
//   - "confirmed" — credentials saved, listener restarted, admin seeded.
//   - "expired"   — the QR handle itself died; frontend must restart login.
//
// Returning early on "scaned" is what gives the user the mid-scan UI update
// that doc §4.2 calls out; otherwise the server would sit on this handler
// for the full 90s and the browser would be stuck at "请扫码".
func (h *Handler) WeChatLoginStatus(ctx context.Context, c *app.RequestContext) {
	qrcode := strings.TrimSpace(c.Query("qrcode"))
	if qrcode == "" {
		httputil.BadRequest(c, "qrcode 参数必填")
		return
	}

	pollCtx, cancel := context.WithTimeout(ctx, loginStatusTimeout)
	defer cancel()

	for {
		select {
		case <-pollCtx.Done():
			// Budget exhausted without scan/confirm — tell frontend to
			// poll again so the panel stays responsive.
			c.JSON(200, map[string]any{"code": 0, "status": "wait"})
			return
		default:
		}

		resp, err := ilink.PollQRStatusOnce(pollCtx, qrcode)
		if err != nil {
			// iLink long-poll ends with a timeout when no state change
			// occurred — surface that as "wait" to the frontend so it
			// can immediately re-poll.
			if pollCtx.Err() != nil && ctx.Err() == nil {
				c.JSON(200, map[string]any{"code": 0, "status": "wait"})
				return
			}
			logger.Warnf(ctx, "[wechat] PollQRStatusOnce: %v", err)
			c.JSON(200, map[string]any{"code": 0, "status": "error", "msg": err.Error()})
			return
		}

		switch resp.Status {
		case "scaned":
			c.JSON(200, map[string]any{"code": 0, "status": "scaned"})
			return

		case "confirmed":
			creds := &ilink.Credentials{
				BotToken:    resp.BotToken,
				ILinkBotID:  resp.ILinkBotID,
				BaseURL:     resp.BaseURL,
				ILinkUserID: resp.ILinkUserID,
			}
			if err := ilink.SaveCredentials(creds); err != nil {
				logger.Errorf(ctx, "[wechat] SaveCredentials: %v", err)
				c.JSON(200, map[string]any{"code": 0, "status": "error", "msg": "保存凭证失败"})
				return
			}
			// Seed the admin whitelist with the logged-in account itself
			// so the scanner can immediately self-test without also going
			// through the first-contact approval dance.
			if err := h.settingsService.AddWeChatAdminID(creds.ILinkUserID); err != nil {
				logger.Warnf(ctx, "[wechat] seed admin id failed: %v", err)
			}
			if h.wechatManager != nil {
				h.wechatManager.Restart()
			} else {
				logger.Warnf(ctx, "[wechat] wechatManager not initialized — listener may not start")
			}
			c.JSON(200, map[string]any{
				"code":   0,
				"status": "confirmed",
				"account": map[string]any{
					"ilink_bot_id":  creds.ILinkBotID,
					"ilink_user_id": creds.ILinkUserID,
				},
			})
			return

		case "expired":
			c.JSON(200, map[string]any{"code": 0, "status": "expired"})
			return

		case "wait", "":
			// No state change this cycle — next iteration issues another
			// long-poll (bounded by pollCtx's 90s budget).
		default:
			// Unknown status: pass through to frontend unchanged so bugs
			// are visible rather than silently ignored.
			c.JSON(200, map[string]any{"code": 0, "status": resp.Status})
			return
		}
	}
}

// WeChatAccounts lists the currently-logged-in account. Only non-sensitive
// fields (bot / user IDs) leak to the frontend — BotToken and BaseURL stay
// server-side. The account carries a "status" that reflects whether the
// backing iLink session is still usable: "online" when the monitor is
// running cleanly, "expired" when iLink has latched errcode=-14 with empty
// sync buf (bot_token dead). The frontend renders the expired state as
// "已掉线 · 请重新扫码".
//
// v1 is single-account: the runtime listener/replier only drive creds[0]
// (see pkg/messaging/wechat/types.go and pkg/messaging/wechat/listener.go). We truncate the
// response to match that reality so the UI can't show phantom "online"
// accounts for stale files left on disk.
func (h *Handler) WeChatAccounts(ctx context.Context, c *app.RequestContext) {
	all, err := ilink.LoadAllCredentials()
	if err != nil {
		logger.Errorf(ctx, "[wechat] LoadAllCredentials: %v", err)
		httputil.InternalError(c, "读取账号失败")
		return
	}

	status := "online"
	if h.wechatManager != nil && h.wechatManager.IsExpired() {
		status = "expired"
	}

	accounts := make([]map[string]string, 0, 1)
	if len(all) > 0 {
		cr := all[0]
		accounts = append(accounts, map[string]string{
			"ilink_bot_id":  cr.ILinkBotID,
			"ilink_user_id": cr.ILinkUserID,
			"status":        status,
		})
	}

	c.JSON(200, map[string]any{
		"code":     0,
		"accounts": accounts,
	})
}

// WeChatLogout deletes the credentials file for a bot ID and restarts the
// listener so it drops the now-stale client. Also removes the account's
// ILinkUserID from the admin whitelist (best-effort).
func (h *Handler) WeChatLogout(ctx context.Context, c *app.RequestContext) {
	var req struct {
		ILinkBotID  string `json:"ilink_bot_id"`
		ILinkUserID string `json:"ilink_user_id"`
	}
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	botID := strings.TrimSpace(req.ILinkBotID)
	if botID == "" {
		httputil.BadRequest(c, "ilink_bot_id 必填")
		return
	}

	if err := ilink.RemoveCredentials(botID); err != nil {
		logger.Errorf(ctx, "[wechat] RemoveCredentials: %v", err)
		httputil.InternalError(c, "删除凭证失败")
		return
	}
	if req.ILinkUserID != "" {
		if err := h.settingsService.RemoveWeChatAdminID(req.ILinkUserID); err != nil {
			logger.Warnf(ctx, "[wechat] remove admin id failed: %v", err)
		}
	}
	if h.wechatManager != nil {
		h.wechatManager.Restart()
	}
	c.JSON(200, map[string]any{"code": 0})
}

// WeChatPending returns the pending-contact ring buffer (most-recent first).
// Frontend renders this as the "waiting for approval" list.
func (h *Handler) WeChatPending(ctx context.Context, c *app.RequestContext) {
	if h.imGateway == nil {
		c.JSON(200, map[string]any{"code": 0, "pending": []any{}})
		return
	}

	entries := h.imGateway.ListPendingContacts(messaging.PlatformWeChat)
	out := make([]map[string]any, 0, len(entries))
	for _, pc := range entries {
		out = append(out, map[string]any{
			"sender_id":    pc.SenderID,
			"message_id":   pc.MessageID,
			"content_hint": pc.ContentHint,
			"received_at":  pc.ReceivedAt.Format(time.RFC3339),
		})
	}
	c.JSON(200, map[string]any{"code": 0, "pending": out})
}

// WeChatAdminAdd adds a sender ID to the admin whitelist. Typically called
// from the pending-contact UI's "加为 admin" button. After adding, the
// corresponding pending entry is cleared so it doesn't keep showing.
func (h *Handler) WeChatAdminAdd(ctx context.Context, c *app.RequestContext) {
	var req struct {
		ID string `json:"id"`
	}
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		httputil.BadRequest(c, "id 必填")
		return
	}

	if err := h.settingsService.AddWeChatAdminID(id); err != nil {
		logger.Errorf(ctx, "[wechat] AddWeChatAdminID: %v", err)
		httputil.InternalError(c, "保存失败")
		return
	}
	if h.imGateway != nil {
		h.imGateway.RemovePendingContact(messaging.PlatformWeChat, id)
	}
	c.JSON(200, map[string]any{"code": 0})
}

// WeChatAdminRemove drops a sender ID from the admin whitelist. Rejects
// removing the currently logged-in account's ILinkUserID — the login
// account should stay a valid admin as long as it's logged in (matches the
// "自己 · 不可删" UI affordance). Remove the credentials first (/logout) if
// the user truly wants to drop it.
func (h *Handler) WeChatAdminRemove(ctx context.Context, c *app.RequestContext) {
	var req struct {
		ID string `json:"id"`
	}
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	id := strings.TrimSpace(req.ID)
	if id == "" {
		httputil.BadRequest(c, "id 必填")
		return
	}

	// Refuse to strip the currently logged-in account. Logging out via
	// /wechat/logout is the intended way to clear it.
	if accounts, err := ilink.LoadAllCredentials(); err == nil {
		for _, a := range accounts {
			if a.ILinkUserID == id {
				httputil.BadRequest(c, "无法移除当前登录账号；如需移除请先退出登录")
				return
			}
		}
	}

	if err := h.settingsService.RemoveWeChatAdminID(id); err != nil {
		logger.Errorf(ctx, "[wechat] RemoveWeChatAdminID: %v", err)
		httputil.InternalError(c, "保存失败")
		return
	}
	c.JSON(200, map[string]any{"code": 0})
}

// WeChatPendingDismiss drops a pending-contact entry without adding to the
// whitelist — used for the "ignore" button in the pending list UI.
func (h *Handler) WeChatPendingDismiss(ctx context.Context, c *app.RequestContext) {
	var req struct {
		SenderID string `json:"sender_id"`
	}
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	id := strings.TrimSpace(req.SenderID)
	if id == "" {
		httputil.BadRequest(c, "sender_id 必填")
		return
	}
	if h.imGateway != nil {
		h.imGateway.RemovePendingContact(messaging.PlatformWeChat, id)
	}
	c.JSON(200, map[string]any{"code": 0})
}
