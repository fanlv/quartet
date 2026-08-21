package handler

import (
	"context"
	"fmt"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/services/agent/catalog"
	agentinstall "github.com/fanlv/quartet/services/agent/install"
	"github.com/fanlv/quartet/services/agent/probe"
	"github.com/fanlv/quartet/types/model"
)

// AgentCatalog returns the complete management directory: ordered built-ins
// followed by persisted custom entries.
func (h *Handler) AgentCatalog(ctx context.Context, c *app.RequestContext) {
	agents, err := h.agentCatalog.ListItems(ctx)
	if err != nil {
		httputil.InternalErrorLog(ctx, c, "agent.catalog", err)
		return
	}
	checker := agentinstall.Checker{}
	for index := range agents {
		item := &agents[index]
		status := checker.Check(agentinstall.Definition{
			Bin:        item.Definition.Bin,
			ACPProgram: item.Definition.ACPProgram,
		})
		item.Installed = status.Installed
		revision := item.CurrentRevision
		if item.Source == model.AgentCatalogSourceBuiltin {
			if builtin, ok := catalog.FindBuiltinByID(item.AgentID); ok {
				revision = catalog.BindingForBuiltin(builtin).Revision
			}
		}
		item.CurrentRevision = revision
		validation, matched := probe.CachedAgentValidation(
			item.AgentID,
			revision,
			h.settingsService.GetACPEnvVersion(item.AgentID),
		)
		if validation.RefreshedAt > 0 {
			if validation.Success {
				item.LastValidationStatus = "available"
			} else {
				item.LastValidationStatus = "unavailable"
				item.LastValidationError = validation.Error
			}
			item.LastValidationAt = validation.RefreshedAt
		}
		switch {
		case item.Deprecated:
			item.Availability = "deprecated"
		case item.Lifecycle == model.AgentLifecycleDeleting:
			item.Availability = "deleting"
		case item.Lifecycle == model.AgentLifecycleDeleted:
			item.Availability = "deleted"
		case !status.Installed:
			item.Availability = "not_installed"
			item.AvailabilityError = status.Error
		default:
			switch {
			case !matched:
				item.Availability = "pending_validation"
			case validation.Refreshing && validation.RefreshedAt == 0:
				item.Availability = "validating"
			case validation.Success:
				item.Availability = "available"
			default:
				item.Availability = "unavailable"
				item.AvailabilityError = validation.Error
			}
			item.Refreshing = validation.Refreshing
		}
	}
	c.JSON(http.StatusOK, model.AgentCatalogResponse{
		Code:   0,
		Agents: agents,
	})
}

func (h *Handler) DeletedAgentCatalog(ctx context.Context, c *app.RequestContext) {
	agents, err := h.agentCatalog.DeletedItems(ctx)
	if err != nil {
		httputil.InternalErrorLog(ctx, c, "agent.catalog.deleted", err)
		return
	}
	c.JSON(http.StatusOK, model.AgentCatalogResponse{Code: 0, Agents: agents})
}

func (h *Handler) AgentCatalogDetail(ctx context.Context, c *app.RequestContext) {
	agentID := c.Param("agentId")
	if agentID == "" {
		httputil.BadRequest(c, "agentId is required")
		return
	}
	entry, found, err := h.agentCatalog.Find(ctx, agentID)
	if err != nil {
		httputil.InternalErrorLog(ctx, c, "agent.catalog.detail", err)
		return
	}
	if !found {
		httputil.NotFound(c, "AgentID "+agentID+" does not exist")
		return
	}
	item, err := h.catalogItemByIDIncludingDeleted(ctx, agentID)
	if err != nil {
		httputil.InternalErrorLog(ctx, c, "agent.catalog.detail-item", err)
		return
	}
	var revisions []model.AgentRuntimeRevision
	switch entry.Source {
	case model.AgentCatalogSourceBuiltin:
		if entry.Builtin != nil {
			revisions, err = h.agentCatalog.BuiltinRevisions(ctx, entry.Builtin.AgentID)
			if err != nil {
				httputil.InternalErrorLog(ctx, c, "agent.catalog.detail-built-in-revisions", err)
				return
			}
		}
	case model.AgentCatalogSourceCustom:
		if entry.Custom != nil {
			revisions = append([]model.AgentRuntimeRevision(nil), entry.Custom.Revisions...)
		}
	}
	c.JSON(http.StatusOK, model.AgentCatalogDetailResponse{
		Code:      0,
		Agent:     item,
		Revisions: revisions,
	})
}

func (h *Handler) catalogItemByIDIncludingDeleted(ctx context.Context, agentID string) (model.AgentCatalogItem, error) {
	items, err := h.agentCatalog.ListItems(ctx)
	if err != nil {
		return model.AgentCatalogItem{}, err
	}
	deleted, err := h.agentCatalog.DeletedItems(ctx)
	if err != nil {
		return model.AgentCatalogItem{}, err
	}
	items = append(items, deleted...)
	for _, item := range items {
		if item.AgentID == agentID {
			return item, nil
		}
	}
	return model.AgentCatalogItem{}, fmt.Errorf("AgentID %q is missing from the management projection", agentID)
}
