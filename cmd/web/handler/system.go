package handler

import (
	"context"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/pkg/logger"
	runtimesvc "github.com/fanlv/quartet/services/runtime"
)

// SystemRestartWeb schedules a detached `make web` restart and returns before
// the current backend process is stopped by the restart command.
func (h *Handler) SystemRestartWeb(ctx context.Context, c *app.RequestContext) {
	if err := runtimesvc.RestartWeb(ctx); err != nil {
		httputil.InternalErrorLog(ctx, c, "SystemRestartWeb", err)
		return
	}
	logger.Infof(ctx, "[SystemRestartWeb] detached make web restart scheduled")
	c.JSON(http.StatusOK, map[string]any{
		"code":     0,
		"msg":      "restart scheduled",
		"log_path": runtimesvc.WebRestartLogPath,
	})
}
