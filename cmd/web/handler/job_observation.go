package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/fanlv/quartet/pkg/httputil"
	jobsvc "github.com/fanlv/quartet/services/job"
	"github.com/fanlv/quartet/types/model"
)

func (h *Handler) JobObservations(ctx context.Context, c *app.RequestContext) {
	limit := 200
	if raw := string(c.Query("limit")); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed <= 0 {
			httputil.BadRequest(c, "limit must be a positive integer")
			return
		}
		limit = parsed
	}
	response, err := h.jobService.ObserveJobs(string(c.Query("cursor")), limit)
	if err != nil {
		if errors.Is(err, jobsvc.ErrInvalidObservationCursor) {
			httputil.BadRequest(c, err.Error())
			return
		}
		httputil.InternalError(c, err.Error())
		return
	}
	h.enrichLegacyGraphObservations(ctx, &response)
	c.JSON(http.StatusOK, response)
}

func (h *Handler) enrichLegacyGraphObservations(ctx context.Context, response *model.JobObservationResponse) {
	if response == nil || h.graphService == nil {
		return
	}
	const maximumFallbackLookups = 24
	lookups := 0
	for index := range response.Changes {
		change := &response.Changes[index]
		if change.Job.Mode != model.JobModeGraph || change.GraphStatus != "" || change.Job.Status != model.JobStatusStopped {
			continue
		}
		if lookups >= maximumFallbackLookups {
			return
		}
		if change.GraphRunID == "" {
			continue
		}
		lookups++
		status, err := h.graphService.GetRunStatus(ctx, change.GraphRunID)
		if err != nil || status == nil || status.Run == nil {
			continue
		}
		change.GraphStatus = string(status.Run.Status)
		if status.Run.FinishedAt > 0 {
			change.OccurredAt = status.Run.FinishedAt
		}
		if status.Run.Status == model.GraphRunStatusAwaitingInput {
			for _, instance := range status.Instances {
				if instance.Status != model.GraphInstanceStatusAwaitingInput {
					continue
				}
				change.GraphSessionID = instance.DisplaySessionID
				if change.GraphSessionID == "" {
					change.GraphSessionID = instance.SessionID
				}
				if change.GraphSessionID != "" {
					break
				}
			}
		}
	}
}
