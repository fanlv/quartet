package handler

import (
	"context"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
)

// agentDisplayResolveMaxIDs bounds one resolve request so a caller cannot
// make the catalog scan an unbounded identifier list.
const agentDisplayResolveMaxIDs = 200

// AgentDisplayInfoResolve batch-resolves historical Agent references
// (session serve commands, graph node snapshot agent types) to their minimal
// display projection. Identifiers that match no retained record are simply
// absent from the result — the client renders those as an unknown Agent.
func (h *Handler) AgentDisplayInfoResolve(ctx context.Context, c *app.RequestContext) {
	var req model.ResolveAgentDisplayInfoRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request")
		return
	}
	if len(req.IDs) == 0 {
		httputil.BadRequest(c, "ids is required")
		return
	}
	if len(req.IDs) > agentDisplayResolveMaxIDs {
		httputil.BadRequest(c, "ids exceeds the maximum of 200 per request")
		return
	}
	agents, err := h.agentCatalog.ResolveDisplayInfos(ctx, req.IDs)
	if err != nil {
		httputil.InternalErrorLog(ctx, c, "agent.display-info.resolve", err)
		return
	}
	for id, info := range agents {
		info.IconURL = IconCacheURL(info.IconURL)
		agents[id] = info
	}
	c.JSON(http.StatusOK, model.ResolveAgentDisplayInfoResponse{Agents: agents})
}

// resolvePublicAgents resolves the minimal display info attached to public
// share responses. Failures stay out of the share payload: they are logged
// and the response simply carries no agents.
func (h *Handler) resolvePublicAgents(ctx context.Context, refs []string, jobID string) map[string]model.AgentDisplayInfo {
	if len(refs) == 0 {
		return nil
	}
	agents, err := h.agentCatalog.ResolveDisplayInfos(ctx, refs)
	if err != nil {
		logger.Warnf(ctx, "[agent.display-info] public resolve failed: %v", err)
		return nil
	}
	if len(agents) == 0 {
		return nil
	}
	for ref, info := range agents {
		if isRemoteIconURL(info.IconURL) {
			info.IconURL = PublicAgentIconURL(jobID, info.AgentID)
		}
		agents[ref] = info
	}
	return agents
}

// collectGraphRunAgentRefs gathers Agent references from durable runtime
// bindings: the base/version snapshots and every persisted Job session. It
// deliberately does not enumerate node config AgentType fields because inherit
// nodes keep hidden, non-executing values there; their real Agent comes from the
// upstream session binding. Must run before slimGraphRunStatus strips snapshots.
func (h *Handler) collectGraphRunAgentRefs(resp *model.GraphRunStatusResponse, job *model.Job) []string {
	var refs []string
	seen := make(map[string]bool)
	add := func(ref string) {
		if ref == "" || seen[ref] {
			return
		}
		seen[ref] = true
		refs = append(refs, ref)
	}
	addSnapshotRefs := func(snapshots map[string]model.GraphAgentSnapshot) {
		for _, snapshot := range snapshots {
			if snapshot.AgentType != "" {
				add(snapshot.AgentType)
				continue
			}
			add(snapshot.AgentID)
		}
	}
	if resp != nil && resp.Run != nil {
		addSnapshotRefs(resp.Run.BaseSnapshot.AgentSnapshots)
		for _, version := range resp.Run.Versions {
			addSnapshotRefs(version.AgentSnapshots)
		}
	}
	if job != nil {
		for _, sessionID := range jobAllSessionIDs(job) {
			if session, ok := h.lookupSession(sessionID); ok && session != nil {
				if session.AgentID != "" {
					add(session.AgentID)
					continue
				}
				add(session.Type)
			}
		}
	}
	return refs
}
