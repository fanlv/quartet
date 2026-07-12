package handler

import (
	"context"
	"errors"
	"net/http"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/services/schedule"
	"github.com/fanlv/quartet/types/model"
)

func scheduleToInfo(t *model.ScheduledTask) model.ScheduleInfo {
	info := model.ScheduleInfo{
		ID:               t.ID,
		Name:             t.Name,
		Enabled:          t.Enabled,
		CronExpr:         t.CronExpr,
		GraphWorkflowID:  t.GraphWorkflowID,
		WorkspaceID:      t.WorkspaceID,
		Workdir:          t.Workdir,
		MaxConcurrent:    t.MaxConcurrent,
		Timeout:          t.Timeout,
		LastRunJobID:     t.LastRunJobID,
		LastStatus:       t.LastStatus,
		LastTriggerError: t.LastTriggerError,
		RunCount:         t.RunCount,
		CreatedAt:        t.CreatedAt.UnixMilli(),
		UpdatedAt:        t.UpdatedAt.UnixMilli(),
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

func (h *Handler) ScheduleCreate(ctx context.Context, c *app.RequestContext) {
	var req model.CreateScheduleRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}

	if req.Name == "" {
		httputil.BadRequest(c, "name is required")
		return
	}
	if req.CronExpr == "" {
		httputil.BadRequest(c, "cronExpr is required")
		return
	}
	if err := schedule.ValidateCronExpr(req.CronExpr); err != nil {
		httputil.BadRequest(c, err.Error())
		return
	}
	if req.WorkspaceID != "" {
		if _, ok := h.workspaceService.Get(req.WorkspaceID); !ok {
			httputil.BadRequest(c, "workspace not found")
			return
		}
	}

	task, err := h.scheduleService.Create(ctx, &req)
	if err != nil {
		logger.Errorf(ctx, "[ScheduleCreate] failed: %v", err)
		writeScheduleServiceError(c, err)
		return
	}

	// Register with scheduler
	if h.scheduler != nil {
		h.scheduler.Reload()
	}

	c.JSON(http.StatusOK, model.CreateScheduleResponse{Schedule: scheduleToInfo(task)})
}

func (h *Handler) ScheduleList(ctx context.Context, c *app.RequestContext) {
	wsID := string(c.Query("workspaceId"))
	var tasks []*model.ScheduledTask
	var err error
	if wsID != "" {
		tasks, err = h.scheduleService.ListByWorkspace(ctx, wsID)
	} else {
		tasks, err = h.scheduleService.List(ctx)
	}
	if err != nil {
		logger.Errorf(ctx, "[ScheduleList] failed: workspaceId=%s err=%v", wsID, err)
		httputil.InternalError(c, err.Error())
		return
	}

	infos := make([]model.ScheduleInfo, 0, len(tasks))
	for _, t := range tasks {
		infos = append(infos, scheduleToInfo(t))
	}
	c.JSON(http.StatusOK, model.ListSchedulesResponse{Schedules: infos})
}

func (h *Handler) ScheduleGet(ctx context.Context, c *app.RequestContext) {
	id := c.Param("scheduleId")
	if id == "" {
		httputil.BadRequest(c, "scheduleId is required")
		return
	}
	task, err := h.scheduleService.Get(ctx, id)
	if err != nil {
		logger.Errorf(ctx, "[ScheduleGet] failed: scheduleId=%s err=%v", id, err)
		httputil.InternalError(c, err.Error())
		return
	}
	if task == nil {
		httputil.NotFound(c, "schedule not found")
		return
	}
	c.JSON(http.StatusOK, scheduleToInfo(task))
}

func (h *Handler) ScheduleUpdate(ctx context.Context, c *app.RequestContext) {
	id := c.Param("scheduleId")
	if id == "" {
		httputil.BadRequest(c, "scheduleId is required")
		return
	}

	var req model.UpdateScheduleRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}

	if req.CronExpr != nil {
		if err := schedule.ValidateCronExpr(*req.CronExpr); err != nil {
			httputil.BadRequest(c, err.Error())
			return
		}
	}

	task, err := h.scheduleService.Update(ctx, id, &req)
	if err != nil {
		logger.Errorf(ctx, "[ScheduleUpdate] failed: %v", err)
		writeScheduleServiceError(c, err)
		return
	}

	if h.scheduler != nil {
		h.scheduler.Reload()
	}

	c.JSON(http.StatusOK, scheduleToInfo(task))
}

func writeScheduleServiceError(c *app.RequestContext, err error) {
	switch {
	case errors.Is(err, schedule.ErrGraphWorkflowRequired),
		errors.Is(err, schedule.ErrGraphWorkflowNotFound):
		httputil.BadRequest(c, err.Error())
	default:
		httputil.InternalError(c, err.Error())
	}
}

func (h *Handler) ScheduleDelete(ctx context.Context, c *app.RequestContext) {
	id := c.Param("scheduleId")
	if id == "" {
		httputil.BadRequest(c, "scheduleId is required")
		return
	}

	if err := h.scheduleService.Delete(ctx, id); err != nil {
		logger.Errorf(ctx, "[ScheduleDelete] failed: scheduleId=%s err=%v", id, err)
		httputil.InternalError(c, err.Error())
		return
	}

	if h.scheduler != nil {
		h.scheduler.Reload()
	}

	c.JSON(http.StatusOK, map[string]any{"code": 0, "status": "ok"})
}

func (h *Handler) ScheduleToggle(ctx context.Context, c *app.RequestContext) {
	id := c.Param("scheduleId")
	if id == "" {
		httputil.BadRequest(c, "scheduleId is required")
		return
	}
	task, err := h.scheduleService.Get(ctx, id)
	if err != nil {
		logger.Errorf(ctx, "[ScheduleToggle] get failed: scheduleId=%s err=%v", id, err)
		httputil.InternalError(c, err.Error())
		return
	}
	if task == nil {
		httputil.NotFound(c, "schedule not found")
		return
	}

	task.Enabled = !task.Enabled
	task.UpdatedAt = time.Now()
	task.StateUpdatedAt = task.UpdatedAt
	if task.Enabled {
		task.NextRunAt = schedule.NextCronTime(task.CronExpr, time.Now())
	} else {
		task.NextRunAt = nil
	}
	if err := h.scheduleService.Save(ctx, task); err != nil {
		logger.Errorf(ctx, "[ScheduleToggle] save failed: scheduleId=%s enabled=%v err=%v", id, task.Enabled, err)
		httputil.InternalError(c, err.Error())
		return
	}

	if h.scheduler != nil {
		h.scheduler.Reload()
	}

	c.JSON(http.StatusOK, scheduleToInfo(task))
}

func (h *Handler) ScheduleRun(ctx context.Context, c *app.RequestContext) {
	id := c.Param("scheduleId")
	if id == "" {
		httputil.BadRequest(c, "scheduleId is required")
		return
	}
	task, err := h.scheduleService.Get(ctx, id)
	if err != nil {
		logger.Errorf(ctx, "[ScheduleRun] get failed: scheduleId=%s err=%v", id, err)
		httputil.InternalError(c, err.Error())
		return
	}
	if task == nil {
		httputil.NotFound(c, "schedule not found")
		return
	}

	if h.scheduler == nil {
		httputil.InternalError(c, "scheduler not initialized")
		return
	}

	// Use RunNow to respect maxConcurrent limits and track running count.
	// RunNow records the trigger outcome itself (status / LastRunJobID /
	// LastTriggerError), unifying the manual path with cron per the design's
	// "手动立即运行与 cron 触发统一失败记录" — so we don't RecordTrigger here.
	jobID, err := h.scheduler.RunNow(ctx, task)
	if err != nil {
		// Two cases, both surfaced to the caller as 409 so the error text shows
		// up immediately: ErrScheduleMaxConcurrent (over the limit — no slot
		// taken, nothing recorded) and a real trigger failure (already recorded
		// by RunNow; we additionally return it here for immediate display).
		logger.Errorf(ctx, "[ScheduleRun] trigger failed: %v", err)
		httputil.Conflict(c, err.Error())
		return
	}

	c.JSON(http.StatusOK, map[string]any{
		"status": "triggered",
		"jobId":  jobID,
	})
}
