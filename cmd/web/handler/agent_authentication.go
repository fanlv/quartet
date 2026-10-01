package handler

import (
	"context"
	"errors"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/services/agent/authentication"
	"github.com/fanlv/quartet/types/model"
)

func (h *Handler) AgentAuthenticationInfo(ctx context.Context, c *app.RequestContext) {
	c.Header("Cache-Control", "no-store")
	info, err := h.agentAuthentication.Info(ctx, c.Param("agentId"))
	if err != nil {
		httputil.BadRequest(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, info)
}

func (h *Handler) StartAgentAuthentication(ctx context.Context, c *app.RequestContext) {
	c.Header("Cache-Control", "no-store")
	var request model.AgentAuthRequest
	if err := c.BindJSON(&request); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	attempt, err := h.agentAuthentication.Start(ctx, c.Param("agentId"), request)
	if err != nil {
		if errors.Is(err, authentication.ErrInProgress) {
			httputil.Conflict(c, err.Error())
		} else {
			httputil.BadRequest(c, err.Error())
		}
		return
	}
	c.JSON(http.StatusOK, map[string]any{"code": 0, "attempt": attempt})
}

func (h *Handler) AgentAuthenticationAttempt(_ context.Context, c *app.RequestContext) {
	c.Header("Cache-Control", "no-store")
	attempt, err := h.agentAuthentication.Read(c.Param("agentId"), c.Param("attemptId"))
	if err != nil {
		httputil.NotFound(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"code": 0, "attempt": attempt})
}

func (h *Handler) CancelAgentAuthentication(_ context.Context, c *app.RequestContext) {
	if err := h.agentAuthentication.Cancel(c.Param("agentId"), c.Param("attemptId")); err != nil {
		httputil.NotFound(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"code": 0})
}
