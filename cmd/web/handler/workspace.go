package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/services/workspace"
	"github.com/fanlv/quartet/types/consts"
	"github.com/fanlv/quartet/types/model"

	"github.com/cloudwego/hertz/pkg/app"
)

func (h *Handler) WorkspaceCreate(ctx context.Context, c *app.RequestContext) {
	var req model.CreateWorkspaceRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "Invalid request body")
		return
	}
	if req.Title == "" {
		httputil.BadRequest(c, "title is required")
		return
	}
	if req.Workdir == "" {
		httputil.BadRequest(c, "workdir is required")
		return
	}
	defaultAgent := strings.TrimSpace(req.DefaultAgent)
	defaultModel := strings.TrimSpace(req.DefaultModel)
	if err := h.validateWorkspaceDefaults(ctx, defaultAgent, defaultModel); err != nil {
		httputil.BadRequest(c, err.Error())
		return
	}

	ws := model.NewWorkspace(req.Title, req.Description, req.Workdir)
	ws.DefaultAgent = defaultAgent
	ws.DefaultModel = defaultModel
	if err := h.workspaceService.Create(ws); err != nil {
		logger.Errorf(ctx, "[WorkspaceCreate] Failed: %v", err)
		if isInvalidWorkdirErr(err) {
			httputil.BadRequest(c, err.Error())
			return
		}
		httputil.InternalError(c, err.Error())
		return
	}

	c.JSON(http.StatusOK, toWorkspaceInfo(ws))
}

// GitBranch reports the current git branch for a directory so the composer's
// workspace tag can show it next to the path. Returns an empty branch string
// when the directory is not inside a git repository (or HEAD is detached).
func (h *Handler) GitBranch(ctx context.Context, c *app.RequestContext) {
	dir := strings.TrimSpace(string(c.Query("path")))
	if dir == "" {
		httputil.BadRequest(c, "path is required")
		return
	}
	c.JSON(http.StatusOK, map[string]any{
		"code":   0,
		"branch": h.workspaceService.GitBranch(dir),
	})
}

// WorkspaceDefaultWorkdir returns the path the new-workspace dialog should
// prefill into its workdir picker. Mirrors the same fallback chain
// EnsureDefault uses (sandbox UserHomeDir → $HOME → sandbox TempDir) so the
// suggested path is always valid for immediate creation.
func (h *Handler) WorkspaceDefaultWorkdir(ctx context.Context, c *app.RequestContext) {
	c.JSON(http.StatusOK, map[string]any{
		"code":    0,
		"workdir": h.workspaceService.DefaultWorkdir(),
	})
}

func (h *Handler) WorkspaceList(ctx context.Context, c *app.RequestContext) {
	list := h.workspaceService.List()
	infos := make([]model.WorkspaceInfo, 0, len(list))
	for _, ws := range list {
		infos = append(infos, toWorkspaceInfo(ws))
	}
	c.JSON(http.StatusOK, model.ListWorkspacesResponse{Workspaces: infos})
}

func (h *Handler) WorkspaceGet(ctx context.Context, c *app.RequestContext) {
	id := c.Param("id")
	ws, ok := h.workspaceService.Get(id)
	if !ok {
		httputil.NotFound(c, "workspace not found")
		return
	}
	c.JSON(http.StatusOK, toWorkspaceInfo(ws))
}

func (h *Handler) WorkspaceUpdate(ctx context.Context, c *app.RequestContext) {
	id := c.Param("id")
	var req model.UpdateWorkspaceRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "Invalid request body")
		return
	}
	if req.ExpectedVersion == 0 {
		httputil.BadRequest(c, "expectedVersion is required")
		return
	}
	if req.Title != nil && strings.TrimSpace(*req.Title) == "" {
		httputil.BadRequest(c, "title is required")
		return
	}
	if req.Workdir != nil && strings.TrimSpace(*req.Workdir) == "" {
		httputil.BadRequest(c, "workdir is required")
		return
	}
	if (req.DefaultAgent == nil) != (req.DefaultModel == nil) {
		httputil.BadRequest(c, "defaultAgent and defaultModel must be updated together")
		return
	}
	if req.DefaultAgent != nil {
		agentID := strings.TrimSpace(*req.DefaultAgent)
		modelID := strings.TrimSpace(*req.DefaultModel)
		if err := h.validateWorkspaceDefaults(ctx, agentID, modelID); err != nil {
			httputil.BadRequest(c, err.Error())
			return
		}
		req.DefaultAgent = &agentID
		req.DefaultModel = &modelID
	}

	ws, err := h.workspaceService.Patch(id, req.ExpectedVersion, workspace.Patch{
		Title: req.Title, Description: req.Description, Workdir: req.Workdir,
		DefaultAgent: req.DefaultAgent, DefaultModel: req.DefaultModel,
	})
	if err != nil {
		logger.Errorf(ctx, "[WorkspaceUpdate] Failed: %v", err)
		if errors.Is(err, workspace.ErrVersionConflict) {
			httputil.Conflict(c, err.Error())
			return
		}
		if isInvalidWorkdirErr(err) {
			httputil.BadRequest(c, err.Error())
			return
		}
		httputil.InternalError(c, err.Error())
		return
	}

	c.JSON(http.StatusOK, toWorkspaceInfo(ws))
}

func (h *Handler) validateWorkspaceDefaults(ctx context.Context, agentID, modelID string) error {
	if agentID == "" {
		if modelID != "" {
			return fmt.Errorf("defaultModel requires defaultAgent")
		}
		return nil
	}
	entry, found, err := h.agentCatalog.Find(ctx, agentID)
	if err != nil {
		return fmt.Errorf("resolve default AgentID %q failed: %w", agentID, err)
	}
	if !found {
		return fmt.Errorf("default AgentID %q does not exist", agentID)
	}
	switch entry.Source {
	case model.AgentCatalogSourceBuiltin:
		if entry.Builtin == nil || entry.Builtin.Deprecated {
			return fmt.Errorf("default AgentID %q is not active", agentID)
		}
	case model.AgentCatalogSourceCustom:
		if entry.Custom == nil || entry.Custom.Lifecycle != model.AgentLifecycleActive {
			return fmt.Errorf("default AgentID %q is not active", agentID)
		}
	default:
		return fmt.Errorf("default AgentID %q is not active", agentID)
	}
	return nil
}

// isInvalidWorkdirErr reports whether err was produced by the workspace
// service's internal workdir validation. The workspace service returns
// errors wrapped as `invalid workdir: ...` so we key off that prefix.
func isInvalidWorkdirErr(err error) bool {
	return err != nil && strings.Contains(err.Error(), "invalid workdir")
}

func toWorkspaceInfo(ws *model.Workspace) model.WorkspaceInfo {
	return model.WorkspaceInfo{
		ID:           ws.ID,
		Version:      ws.Version,
		Title:        ws.Title,
		Description:  ws.Description,
		Workdir:      ws.Workdir,
		DefaultAgent: ws.DefaultAgent,
		DefaultModel: ws.DefaultModel,
		Color:        ws.Color,
		Favorite:     ws.Favorite,
		SortOrder:    ws.SortOrder,
		CreatedAt:    ws.CreatedAt.UnixMilli(),
		UpdatedAt:    ws.UpdatedAt.UnixMilli(),
	}
}

func (h *Handler) WorkspaceUpdateFavorite(ctx context.Context, c *app.RequestContext) {
	id := c.Param("id")
	var req model.UpdateWorkspaceFavoriteRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, err.Error())
		return
	}
	if _, err := h.workspaceService.SetFavorite(id, req.Favorite); err != nil {
		logger.Errorf(ctx, "[WorkspaceUpdateFavorite] Failed: %v", err)
		httputil.InternalError(c, err.Error())
		return
	}
	h.writeWorkspaceList(c)
}

func (h *Handler) WorkspaceReorder(ctx context.Context, c *app.RequestContext) {
	var req model.ReorderWorkspacesRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, err.Error())
		return
	}
	if err := h.workspaceService.Reorder(req.WorkspaceIDs); err != nil {
		logger.Errorf(ctx, "[WorkspaceReorder] Failed: %v", err)
		httputil.BadRequest(c, err.Error())
		return
	}
	h.writeWorkspaceList(c)
}

func (h *Handler) writeWorkspaceList(c *app.RequestContext) {
	list := h.workspaceService.List()
	infos := make([]model.WorkspaceInfo, 0, len(list))
	for _, ws := range list {
		infos = append(infos, toWorkspaceInfo(ws))
	}
	c.JSON(http.StatusOK, model.ListWorkspacesResponse{Workspaces: infos})
}

// WorkspaceRegenerateColors assigns a new random color to every workspace and
// returns the refreshed list, letting the settings UI re-roll the whole palette
// when the initial colors happen to clash.
func (h *Handler) WorkspaceRegenerateColors(ctx context.Context, c *app.RequestContext) {
	list, err := h.workspaceService.RegenerateAllColors()
	if err != nil {
		logger.Errorf(ctx, "[WorkspaceRegenerateColors] Failed: %v", err)
		httputil.InternalError(c, err.Error())
		return
	}
	infos := make([]model.WorkspaceInfo, 0, len(list))
	for _, ws := range list {
		infos = append(infos, toWorkspaceInfo(ws))
	}
	c.JSON(http.StatusOK, model.ListWorkspacesResponse{Workspaces: infos})
}

func (h *Handler) WorkspaceDelete(ctx context.Context, c *app.RequestContext) {
	id := c.Param("id")

	if id == consts.DefaultWorkspaceID {
		httputil.BadRequest(c, "default workspace cannot be deleted")
		return
	}

	if _, ok := h.workspaceService.Get(id); !ok {
		httputil.NotFound(c, "workspace not found")
		return
	}

	// Close the TOCTOU window before cascading into jobs: once MarkDeleted
	// returns, workspaceService.Get starts returning false, so any concurrent
	// request that tries to resolve this workspace (new IM message routing,
	// UI job-create) fails fast and cannot slip a fresh Job in while we are
	// enumerating old ones.
	if err := h.workspaceService.MarkDeleted(id); err != nil {
		logger.Errorf(ctx, "[WorkspaceDelete] mark deleted failed: %v", err)
		httputil.InternalError(c, err.Error())
		return
	}

	// Cascade-delete all jobs belonging to this workspace.
	// Mark all jobs as deleted first to prevent concurrent SendMessage
	// from launching new runs while we are cleaning up. Use MarkDeleted
	// rather than Save on a stale snapshot — Save merges Deleted from the
	// caller's copy, so a concurrent Save holding an older (Deleted=false)
	// snapshot would silently revert the flag.
	jobs := h.jobService.ListByWorkspace(id)
	for _, job := range jobs {
		if err := h.jobService.MarkDeleted(job.ID); err != nil {
			logger.Errorf(ctx, "[WorkspaceDelete] mark job deleted failed: %v, jobId=%s", err, job.ID)
			httputil.InternalError(c, err.Error())
			return
		}
	}

	for _, job := range jobs {
		if err := h.deleteMarkedJob(ctx, job); err != nil {
			logger.Errorf(ctx, "[WorkspaceDelete] delete job failed: %v, jobId=%s", err, job.ID)
			httputil.InternalError(c, err.Error())
			return
		}
	}

	if err := h.workspaceService.Delete(id); err != nil {
		logger.Errorf(ctx, "[WorkspaceDelete] Failed: %v", err)
		httputil.InternalError(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"code": 0, "status": "ok"})
}
