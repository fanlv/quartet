package handler

import (
	"context"
	"net/http"
	"strings"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/types/model"
)

// Viewer presence HTTP surface (§ 结束 Hook「无人查看才通知」). The SSE handlers
// register a viewer for the lifetime of their connection; this endpoint carries
// the one thing the connection cannot express — whether the page is actually on
// screen. See services/job/viewer.go.

// parseViewerVisible reads the `visible` query parameter of an event stream.
// Absent / unparseable means visible: a client that says nothing about
// visibility (iOS, quartet-cli, curl) is one that tears the stream down when it
// stops watching, so treating it as on screen is the accurate reading.
func parseViewerVisible(raw string) bool {
	switch strings.ToLower(strings.TrimSpace(raw)) {
	case "0", "false", "hidden":
		return false
	default:
		return true
	}
}

// JobViewerState updates a live viewer's on-screen state. It is a presence
// report, not a state mutation: an unknown or already-detached viewerId is a
// no-op (applied=false) rather than an error, because the client may report a
// visibility change just after its stream was torn down.
func (h *Handler) JobViewerState(ctx context.Context, c *app.RequestContext) {
	jobID := c.Param("jobId")
	if jobID == "" {
		httputil.BadRequest(c, "jobId is required")
		return
	}

	var req model.JobViewerStateRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, err.Error())
		return
	}
	viewerID := strings.TrimSpace(req.ViewerID)
	if viewerID == "" {
		httputil.BadRequest(c, "viewerId is required")
		return
	}

	applied := h.jobService.SetViewerVisible(jobID, viewerID, req.Visible)
	c.JSON(http.StatusOK, map[string]any{"code": 0, "applied": applied})
}
