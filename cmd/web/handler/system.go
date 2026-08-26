package handler

import (
	"context"
	"net/http"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/pkg/logger"
	runtimesvc "github.com/fanlv/quartet/services/runtime"
)

// SystemRestartWeb synchronously builds and boots a candidate release on an
// isolated ephemeral listener. Only that validated candidate is handed to the
// detached process switch, which rolls back if the live launch is not ready.
func (h *Handler) SystemRestartWeb(ctx context.Context, c *app.RequestContext) {
	if err := runtimesvc.RestartWeb(ctx); err != nil {
		httputil.InternalErrorLog(ctx, c, "SystemRestartWeb", err)
		return
	}
	logger.Infof(ctx, "[SystemRestartWeb] validated Web release activation scheduled")
	c.JSON(http.StatusOK, map[string]any{
		"code":     0,
		"msg":      "build and startup check passed; restart scheduled with automatic rollback",
		"log_path": runtimesvc.WebRestartLogPath,
	})
}
