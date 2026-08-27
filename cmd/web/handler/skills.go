package handler

import (
	"context"
	"fmt"
	"net/http"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/services/skills"
	"github.com/fanlv/quartet/types/model"
)

// resolveSkillScope maps the request's scope selection onto a skills service
// scope. Project scope is resolved through the workspace record rather than the
// backend's own working directory: ACP agents run with the workspace workdir as
// their cwd, so that directory is the only one whose project skills an agent can
// actually load.
func (h *Handler) resolveSkillScope(global bool, workspaceID string) (skills.Scope, error) {
	if global {
		return skills.Scope{Global: true}, nil
	}
	workspaceID = strings.TrimSpace(workspaceID)
	if workspaceID == "" {
		return skills.Scope{}, fmt.Errorf("workspaceId is required for project-scope skill operations")
	}
	ws, ok := h.workspaceService.Get(workspaceID)
	if !ok || ws == nil {
		return skills.Scope{}, fmt.Errorf("workspace not found: %s", workspaceID)
	}
	if strings.TrimSpace(ws.Workdir) == "" {
		return skills.Scope{}, fmt.Errorf("workspace %s has no workdir configured", workspaceID)
	}
	return skills.Scope{Dir: ws.Workdir}, nil
}

// SkillList returns installed skills for one scope from cache.
func (h *Handler) SkillList(ctx context.Context, c *app.RequestContext) {
	scope, err := h.resolveSkillScope(string(c.Query("global")) == "true", string(c.Query("workspaceId")))
	if err != nil {
		httputil.BadRequest(c, err.Error())
		return
	}

	items, ready, errText := h.skillsService.List(scope)
	c.JSON(http.StatusOK, model.SkillListResponse{Code: 0, Skills: items, Ready: ready, Error: errText})
}

// SkillInstallProjectTools installs the quartet-cli binary and every skill
// shipped by the current Quartet checkout through the skills service.
func (h *Handler) SkillInstallProjectTools(ctx context.Context, c *app.RequestContext) {
	result, err := h.skillsService.InstallProjectTools(ctx)
	if err != nil {
		httputil.InternalErrorLog(ctx, c, "skills.install-project-tools", err)
		return
	}
	c.JSON(http.StatusOK, model.ProjectToolsInstallResponse{Code: 0, Result: result})
}

// SkillAdd installs a skill package.
func (h *Handler) SkillAdd(ctx context.Context, c *app.RequestContext) {
	var req model.SkillAddRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	scope, err := h.resolveSkillScope(req.Global, req.WorkspaceID)
	if err != nil {
		httputil.BadRequest(c, err.Error())
		return
	}

	output, err := h.skillsService.Add(ctx, req, scope)
	if err != nil {
		h.respondSkillCommandError(ctx, c, "skills.add", output, err)
		return
	}
	c.JSON(http.StatusOK, model.SkillCommandResponse{Code: 0, Output: output})
}

// SkillRemove uninstalls a skill.
func (h *Handler) SkillRemove(ctx context.Context, c *app.RequestContext) {
	var req model.SkillRemoveRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	scope, err := h.resolveSkillScope(req.Global, req.WorkspaceID)
	if err != nil {
		httputil.BadRequest(c, err.Error())
		return
	}

	output, err := h.skillsService.Remove(ctx, req.Name, scope)
	if err != nil {
		h.respondSkillCommandError(ctx, c, "skills.remove", output, err)
		return
	}
	c.JSON(http.StatusOK, model.SkillCommandResponse{Code: 0, Output: output})
}

// SkillUpdate upgrades every installed skill in one scope to its latest version.
//
// There is deliberately no "check for updates" endpoint: the skills CLI's
// `check` verb is an alias of `update`, so a check-shaped endpoint would write
// while pretending to only read.
func (h *Handler) SkillUpdate(ctx context.Context, c *app.RequestContext) {
	var req model.SkillUpdateRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	scope, err := h.resolveSkillScope(req.Global, req.WorkspaceID)
	if err != nil {
		httputil.BadRequest(c, err.Error())
		return
	}

	output, err := h.skillsService.Update(ctx, scope)
	if err != nil {
		h.respondSkillCommandError(ctx, c, "skills.update", output, err)
		return
	}
	c.JSON(http.StatusOK, model.SkillCommandResponse{Code: 0, Output: output})
}

// SkillFind searches the public skills registry.
func (h *Handler) SkillFind(ctx context.Context, c *app.RequestContext) {
	results, err := h.skillsService.Find(ctx, string(c.Query("query")))
	if err != nil {
		httputil.InternalErrorLog(ctx, c, "skills.find", err)
		return
	}
	c.JSON(http.StatusOK, model.SkillFindResponse{Code: 0, Results: results})
}

// respondSkillCommandError reports the full CLI failure text: the message the
// UI shows and the raw command output are both preserved.
func (h *Handler) respondSkillCommandError(ctx context.Context, c *app.RequestContext, op, output string, err error) {
	logger.Errorf(ctx, "[%s] %v", op, err)
	c.JSON(http.StatusInternalServerError, model.SkillCommandResponse{Code: -1, Msg: err.Error(), Output: output})
}
