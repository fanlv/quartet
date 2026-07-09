package handler

import (
	"context"
	"fmt"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/types/model"
)

// AgentUsage returns the live subscription / quota info for the Codex or
// Claude ACP agent. The Home page requests a refresh on every agent-type
// switch, so this always fetches fresh (no caching). Errors are returned in
// full per the project convention.
func (h *Handler) AgentUsage(ctx context.Context, c *app.RequestContext) {
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
	default:
		httputil.BadRequest(c, fmt.Sprintf("invalid type %q (want codex|claude)", typ))
	}
}
