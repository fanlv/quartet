package handler

import (
	"context"
	"fmt"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/types/model"
)

// AgentUsage returns live subscription / quota information for a supported ACP
// agent. Clients refresh when the selected agent changes, so this endpoint does
// not use an HTTP cache. Errors are returned in full per the project convention.
func (h *Handler) AgentUsage(ctx context.Context, c *app.RequestContext) {
	// Never cache: this quota reading changes continuously, and a cached GET
	// response (browser or any intermediary) would surface a stale window
	// (e.g. an old Codex 5h percentage) after a refresh.
	c.Header("Cache-Control", "no-store")
	typ := string(c.Query("type"))
	switch typ {
	case "codex":
		u, err := h.usageService.CodexUsage(ctx)
		if err != nil {
			httputil.InternalErrorLog(ctx, c, "[agent.usage] codex", err)
			return
		}
		c.JSON(http.StatusOK, model.AgentUsageResponse{Code: 0, Type: typ, Codex: u})
	case "claude":
		u, err := h.usageService.ClaudeUsage(ctx)
		if err != nil {
			httputil.InternalErrorLog(ctx, c, "[agent.usage] claude", err)
			return
		}
		c.JSON(http.StatusOK, model.AgentUsageResponse{Code: 0, Type: typ, Claude: u})
	case "antigravity":
		u, err := h.usageService.AntigravityUsage(ctx)
		if err != nil {
			httputil.InternalErrorLog(ctx, c, "[agent.usage] antigravity", err)
			return
		}
		c.JSON(http.StatusOK, model.AgentUsageResponse{Code: 0, Type: typ, Antigravity: u})
	case "kimi":
		u, err := h.usageService.KimiUsage(ctx)
		if err != nil {
			httputil.InternalErrorLog(ctx, c, "[agent.usage] kimi", err)
			return
		}
		c.JSON(http.StatusOK, model.AgentUsageResponse{Code: 0, Type: typ, Kimi: u})
	case "qoder":
		u, err := h.usageService.QoderUsage(ctx)
		if err != nil {
			httputil.InternalErrorLog(ctx, c, "[agent.usage] qoder", err)
			return
		}
		c.JSON(http.StatusOK, model.AgentUsageResponse{Code: 0, Type: typ, Qoder: u})
	case "cursor":
		u, err := h.usageService.CursorUsage(ctx)
		if err != nil {
			httputil.InternalErrorLog(ctx, c, "[agent.usage] cursor", err)
			return
		}
		c.JSON(http.StatusOK, model.AgentUsageResponse{Code: 0, Type: typ, Cursor: u})
	case "codebuddy":
		u, err := h.usageService.CodeBuddyUsage(ctx)
		if err != nil {
			httputil.InternalErrorLog(ctx, c, "[agent.usage] codebuddy", err)
			return
		}
		c.JSON(http.StatusOK, model.AgentUsageResponse{Code: 0, Type: typ, CodeBuddy: u})
	default:
		httputil.BadRequest(c, fmt.Sprintf("invalid type %q (want codex|claude|antigravity|kimi|qoder|cursor|codebuddy)", typ))
	}
}

// AgentVersion returns the installed CLI version of a known built-in or custom
// ACP agent, keyed by AgentInfo.Type. It backs the composer usage strip for
// agents that have no quota view of their own.
func (h *Handler) AgentVersion(ctx context.Context, c *app.RequestContext) {
	c.Header("Cache-Control", "no-store")
	command := string(c.Query("command"))
	if command == "" {
		httputil.BadRequest(c, "command is required")
		return
	}
	v, err := h.usageService.AgentVersion(ctx, command)
	if err != nil {
		httputil.InternalErrorLog(ctx, c, "[agent.version]", err)
		return
	}
	c.JSON(http.StatusOK, model.AgentVersionResponse{Code: 0, Version: v})
}
