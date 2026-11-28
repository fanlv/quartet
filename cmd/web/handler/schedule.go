package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"slices"
	"sort"
	"strconv"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/services/auth"
	"github.com/fanlv/quartet/services/schedule"
	"github.com/fanlv/quartet/types/model"
)

func scheduleToInfo(t *model.ScheduledTask) model.ScheduleInfo {
	info := model.ScheduleInfo{
		TimerSpec: t.TimerSpec, PlanState: t.PlanState, StateReason: t.StateReason,
		WorkflowVersion: t.WorkflowVersion, ApprovalPreview: t.ApprovalPreview,
		ID: t.ID, Name: t.Name, Enabled: t.Enabled, CronExpr: t.CronExpr, GraphWorkflowID: t.GraphWorkflowID,
		WorkspaceID: t.WorkspaceID, Workdir: t.Workdir, MaxConcurrent: t.MaxConcurrent, Timeout: t.Timeout,
		LastRunJobID: t.LastRunJobID, LastStatus: t.LastStatus, LastTriggerError: t.LastTriggerError,
		RunCount: t.RunCount, CreatedAt: t.CreatedAt.UnixMilli(), UpdatedAt: t.UpdatedAt.UnixMilli(),
	}
	if t.LastRunAt != nil {
		ms := t.LastRunAt.UnixMilli()
		info.LastRunAt = &ms
	}
	if t.NextRunAt != nil {
		ms := t.NextRunAt.UnixMilli()
		info.NextRunAt = &ms
	}
	return info
}

func (h *Handler) timerPermission(c *app.RequestContext) func(string) bool {
	principal, _ := CurrentPrincipal(c)
	return func(kind string) bool {
		if kind == model.TimerPrompt {
			return h.authService.HasPermission(principal, auth.PermissionJobExecute)
		}
		return h.authService.HasPermission(principal, auth.PermissionWorkflowExecute)
	}
}

func writeTimerError(c *app.RequestContext, err error) {
	switch {
	case errors.Is(err, schedule.ErrTimerNotFound):
		httputil.NotFound(c, err.Error())
	case errors.Is(err, schedule.ErrTimerForbidden):
		c.JSON(http.StatusForbidden, map[string]string{"error": err.Error()})
	case errors.Is(err, schedule.ErrTimerInvalid):
		httputil.BadRequest(c, err.Error())
	case errors.Is(err, schedule.ErrTimerConflict):
		httputil.Conflict(c, err.Error())
	default:
		httputil.InternalError(c, err.Error())
	}
}

func (h *Handler) SchedulePreview(ctx context.Context, c *app.RequestContext) {
	var task *model.ScheduledTask
	var err error
	if id := c.Param("scheduleId"); id != "" {
		var req model.UpdateScheduleRequest
		if err := c.BindJSON(&req); err != nil {
			httputil.BadRequest(c, err.Error())
			return
		}
		task, err = h.timers.PreviewUpdate(ctx, id, &req)
	} else {
		var req model.CreateScheduleRequest
		if err := c.BindJSON(&req); err != nil {
			httputil.BadRequest(c, err.Error())
			return
		}
		task, err = h.timers.Preview(ctx, &req)
	}
	if err != nil {
		writeTimerError(c, err)
		return
	}
	c.JSON(http.StatusOK, scheduleToInfo(task))
}

func (h *Handler) ScheduleCreate(ctx context.Context, c *app.RequestContext) {
	var req model.CreateScheduleRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, err.Error())
		return
	}
	task, err := h.timers.Create(ctx, &req, h.timerPermission(c))
	if err != nil {
		writeTimerError(c, err)
		return
	}
	c.JSON(http.StatusOK, model.CreateScheduleResponse{Schedule: scheduleToInfo(task)})
}

func (h *Handler) ScheduleList(ctx context.Context, c *app.RequestContext) {
	tasks, err := h.timers.List(ctx, c.Query("workspaceId"))
	if err != nil {
		writeTimerError(c, err)
		return
	}
	infos := make([]model.ScheduleInfo, 0, len(tasks))
	for _, task := range tasks {
		infos = append(infos, scheduleToInfo(task))
	}
	c.JSON(http.StatusOK, model.ListSchedulesResponse{Schedules: infos})
}

func (h *Handler) ScheduleGet(ctx context.Context, c *app.RequestContext) {
	task, err := h.timers.Get(ctx, c.Param("scheduleId"))
	if err != nil {
		writeTimerError(c, err)
		return
	}
	c.JSON(http.StatusOK, scheduleToInfo(task))
}

func (h *Handler) ScheduleUpdate(ctx context.Context, c *app.RequestContext) {
	var req model.UpdateScheduleRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, err.Error())
		return
	}
	task, err := h.timers.Update(ctx, c.Param("scheduleId"), &req, h.timerPermission(c))
	if err != nil {
		writeTimerError(c, err)
		return
	}
	c.JSON(http.StatusOK, scheduleToInfo(task))
}

func (h *Handler) ScheduleDelete(ctx context.Context, c *app.RequestContext) {
	if err := h.timers.Delete(ctx, c.Param("scheduleId")); err != nil {
		writeTimerError(c, err)
		return
	}
	c.JSON(http.StatusOK, map[string]any{"code": 0, "status": "ok"})
}

func (h *Handler) ScheduleRun(ctx context.Context, c *app.RequestContext) {
	var req struct {
		ClientRequestID string `json:"clientRequestId"`
	}
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, err.Error())
		return
	}
	entry, err := h.timers.Run(ctx, c.Param("scheduleId"), req.ClientRequestID, h.timerPermission(c))
	if err != nil {
		writeTimerError(c, err)
		return
	}
	c.JSON(http.StatusOK, entry)
}

func (h *Handler) ScheduleExecutions(ctx context.Context, c *app.RequestContext) {
	records, err := h.timers.Executions(ctx, c.Query("scheduleId"), c.Query("executionId"), c.Query("workspaceId"), c.Query("status"))
	if err != nil {
		writeTimerError(c, err)
		return
	}
	records, next := timerPage(c, records, func(record model.TimerExecution) string { return record.ID })
	c.JSON(http.StatusOK, map[string]any{"executions": records, "nextCursor": next})
}

func (h *Handler) ScheduleSessions(ctx context.Context, c *app.RequestContext) {
	records, err := h.timers.Executions(ctx, c.Query("scheduleId"), c.Query("executionId"), "", "")
	if err != nil {
		writeTimerError(c, err)
		return
	}
	requested := c.Query("executionId")
	sessions := make([]model.TimerSession, 0)
	for _, record := range records {
		if record.JobID == "" || requested != "" && requested != record.ID {
			continue
		}
		j, ok := h.jobService.Get(record.JobID)
		if !ok || j.Deleted {
			if requested != "" {
				httputil.NotFound(c, record.DetailsError)
				return
			}
			continue
		}
		sessionIDs := slices.Concat(j.SessionIDs, j.GraphSessionIDs)
		nodes := make(map[string]model.GraphInstanceState)
		if j.GraphRunID != "" {
			run, err := h.graphService.GetRunStatus(ctx, j.GraphRunID)
			if err != nil {
				writeTimerError(c, fmt.Errorf("load workflow sessions for job %s: %w", j.ID, err))
				return
			}
			addInstance := func(instance model.GraphInstanceState) {
				id := firstNonEmptyString(instance.DisplaySessionID, instance.SessionID)
				if id != "" {
					if prior, exists := nodes[id]; !exists || instance.StartedAt < prior.StartedAt {
						nodes[id] = instance
					}
				}
			}
			for _, instance := range run.Instances {
				addInstance(instance)
			}
			for _, instance := range run.Run.ArchivedInstances {
				addInstance(instance)
			}
			for id := range nodes {
				sessionIDs = append(sessionIDs, id)
			}
		}
		if len(sessionIDs) == 0 {
			continue
		}
		store, err := h.getOrCreateSessionService(j.WorkspaceID, j.ID)
		if err != nil {
			writeTimerError(c, fmt.Errorf("load sessions for Job %s: %w", j.ID, err))
			return
		}
		slices.Sort(sessionIDs)
		for _, id := range slices.Compact(sessionIDs) {
			session, ok := store.Get(id)
			if !ok || session.Deleted {
				continue
			}
			node := nodes[id]
			sessions = append(sessions, model.TimerSession{
				ID: id, ScheduleID: record.ScheduleID, ScheduleName: record.Name, ExecutionID: record.ID,
				JobID: j.ID, WorkspaceID: j.WorkspaceID, Title: session.Title, CreatedAt: session.CreatedAt,
				AgentID: firstNonEmptyString(session.AgentID, session.Type), ModelID: session.ModelID,
				NodeID: node.NodeID, NodeName: node.NodeTitle, Loop: node.Key.Iterations,
			})
		}
	}
	sort.Slice(sessions, func(i, j int) bool {
		a, b := sessions[i], sessions[j]
		return a.CreatedAt.After(b.CreatedAt) || a.CreatedAt.Equal(b.CreatedAt) && a.ID > b.ID
	})
	sessions, next := timerPage(c, sessions, func(session model.TimerSession) string { return session.ID })
	c.JSON(http.StatusOK, map[string]any{"sessions": sessions, "nextCursor": next})
}

func timerPage[T any](c *app.RequestContext, items []T, key func(T) string) ([]T, string) {
	limit, err := strconv.Atoi(c.Query("limit"))
	if err != nil || limit < 1 {
		limit = 30
	}
	limit = min(limit, 100)
	if cursor := c.Query("cursor"); cursor != "" {
		if index := slices.IndexFunc(items, func(item T) bool { return key(item) == cursor }); index >= 0 {
			items = items[index+1:]
		}
	}
	if len(items) > limit {
		return items[:limit], key(items[limit-1])
	}
	return items, ""
}
