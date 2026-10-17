package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
	pkgacp "github.com/fanlv/quartet/pkg/acp"
	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/services/agent/codexaccounts"
	"github.com/fanlv/quartet/services/agent/probe"
	"github.com/fanlv/quartet/types/model"
)

func (h *Handler) acquireCodexAccountUpdate(ctx context.Context) (func(), error) {
	release, err := h.agentExecutions.beginMaintenance("codex")
	if err != nil {
		return nil, fmt.Errorf("%w: %v", codexaccounts.ErrConflict, err)
	}
	releaseRuntime, err := pkgacp.BeginRuntimeMaintenance("codex")
	if err != nil {
		release()
		return nil, fmt.Errorf("%w: %v", codexaccounts.ErrConflict, err)
	}
	h.acpAgentService.DeleteByAgent("codex")
	return func() { releaseRuntime(); release() }, nil
}

func (h *Handler) invalidateCodexAccount(ctx context.Context) error {
	settings, err := h.settingsService.GetSettings()
	if err != nil {
		return err
	}
	// Credential changes invalidate in-flight results just as environment
	// changes do. The version is bumped without changing any environment values.
	if _, err := h.settingsService.StageACPEnvVars("codex", settings.ACPEnvVars["codex"]); err != nil {
		return err
	}
	return h.acpProbeCache.InvalidateAgentAndPersist(ctx, "codex")
}

func (h *Handler) CodexAccountList(ctx context.Context, c *app.RequestContext) {
	c.Header("Cache-Control", "no-store")
	result, err := h.codexAccounts.List(ctx)
	if err != nil {
		httputil.BadRequest(c, err.Error())
		return
	}
	h.decorateCodexAccounts(ctx, result)
	c.JSON(http.StatusOK, result)
}

func (h *Handler) CodexAccountMutate(ctx context.Context, c *app.RequestContext) {
	c.Header("Cache-Control", "no-store")
	var request model.CodexAccountMutation
	if err := c.BindJSON(&request); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	action := c.Param("action")
	if c.Request.Header.IsDelete() {
		action = "delete"
	}
	if c.Request.Header.IsPut() {
		action = "update"
	}
	result, err := h.codexAccounts.Mutate(ctx, action, c.Param("accountId"), request)
	if err != nil {
		if errors.Is(err, codexaccounts.ErrConflict) {
			httputil.Conflict(c, err.Error())
		} else {
			httputil.BadRequest(c, err.Error())
		}
		return
	}
	if action == "activate" || action == "restore" || action == "update" || action == "capture" {
		binding, found, resolveErr := h.agentCatalog.ResolveBinding(ctx, "codex", "")
		if resolveErr != nil {
			result.Warning = resolveErr.Error()
		} else if found {
			h.validateBindingAsyncWithExecution(h.rootCtx, binding, h.settingsService.GetACPEnvVersion("codex"))
		}
	}
	h.decorateCodexAccounts(ctx, result)
	c.JSON(http.StatusOK, result)
}

func (h *Handler) decorateCodexAccounts(ctx context.Context, result *model.CodexAccountResponse) {
	result.Current.Availability = "pending_validation"
	binding, found, err := h.agentCatalog.ResolveBinding(ctx, "codex", "")
	if err != nil {
		result.Current.Availability = "unavailable"
		result.Current.ValidationError = err.Error()
		return
	}
	if !found {
		result.Current.Availability = "unavailable"
		result.Current.ValidationError = "Codex Agent does not exist"
		return
	}
	if entry, ok := probe.CachedAgentValidation("codex", binding.Revision, h.settingsService.GetACPEnvVersion("codex")); ok {
		result.Current.Availability = entry.Availability()
		result.Current.ValidationError, result.Current.ValidatedAt = entry.Error, entry.RefreshedAt
	} else {
		h.validateBindingAsyncWithExecution(h.rootCtx, binding, h.settingsService.GetACPEnvVersion("codex"))
	}
}

func (h *Handler) CodexAccountLogin(_ context.Context, c *app.RequestContext) {
	c.Header("Cache-Control", "no-store")
	var request model.CodexAccountMutation
	if err := c.BindJSON(&request); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	login, err := h.codexAccounts.Login(request)
	if err != nil {
		if errors.Is(err, codexaccounts.ErrConflict) {
			httputil.Conflict(c, err.Error())
		} else {
			httputil.BadRequest(c, err.Error())
		}
		return
	}
	c.JSON(http.StatusOK, model.CodexAccountLoginResponse{Login: login})
}

func (h *Handler) CodexAccountLoginState(_ context.Context, c *app.RequestContext) {
	c.Header("Cache-Control", "no-store")
	login, err := h.codexAccounts.ReadLogin(c.Param("loginId"))
	if err != nil {
		httputil.NotFound(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, model.CodexAccountLoginResponse{Login: login})
}

func (h *Handler) CodexAccountLoginCancel(_ context.Context, c *app.RequestContext) {
	if err := h.codexAccounts.CancelLogin(c.Param("loginId")); err != nil {
		httputil.NotFound(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"code": 0})
}
