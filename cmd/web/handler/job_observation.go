package handler

import (
	"context"
	"errors"
	"net/http"
	"strconv"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/fanlv/quartet/pkg/httputil"
	jobsvc "github.com/fanlv/quartet/services/job"
)

func (h *Handler) JobObservations(_ context.Context, c *app.RequestContext) {
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
	c.JSON(http.StatusOK, response)
}
