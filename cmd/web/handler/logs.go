package handler

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/pkg/logger"
)

// logsListResponse is returned by GET /api/v1/logs/list. Entries are newest
// first so the UI can render them directly without an extra reverse.
type logsListResponse struct {
	Code    int            `json:"code"`
	Level   string         `json:"level"`
	Entries []logger.Entry `json:"entries"`
}

// LogsList returns recent backend + frontend log entries matching the
// supplied filters. The UI polls this endpoint a few times per second when
// the logs tab is open.
func (h *Handler) LogsList(ctx context.Context, c *app.RequestContext) {
	f := logger.Filter{
		MinLevel: string(c.Query("level")),
		Keyword:  string(c.Query("keyword")),
	}
	// "kind" is the high-level backend/frontend bucket the settings UI exposes.
	// We apply it server-side BEFORE the limit cutoff so the UI never has to
	// guess whether older entries were dropped because of the cap or because
	// they didn't match. "sources" is kept as a separate exact-match filter
	// for finer-grained component selection.
	if raw := strings.TrimSpace(string(c.Query("kind"))); raw != "" {
		f.Kind = raw
	}
	if raw := string(c.Query("sources")); raw != "" {
		f.Sources = strings.Split(raw, ",")
	}
	if raw := string(c.Query("since")); raw != "" {
		if v, err := strconv.ParseUint(raw, 10, 64); err == nil {
			f.Since = v
		}
	}
	if raw := string(c.Query("limit")); raw != "" {
		if v, err := strconv.Atoi(raw); err == nil && v > 0 {
			f.Limit = v
		}
	}
	if f.Limit == 0 {
		f.Limit = 500
	}

	entries := logger.RecentEntries(f)
	c.JSON(http.StatusOK, logsListResponse{
		Code:    0,
		Level:   logger.GetLevel(),
		Entries: entries,
	})
}

// LogsClear wipes the in-memory ring buffer. The UI calls this when the user
// hits the "Clear" button.
func (h *Handler) LogsClear(ctx context.Context, c *app.RequestContext) {
	logger.ClearBuffer()
	c.JSON(http.StatusOK, map[string]any{"code": 0})
}

type logsLevelRequest struct {
	Level string `json:"level"`
}

// LogsSetLevel changes the runtime log level. Unknown values return 400 so
// the UI can surface a validation error instead of silently succeeding.
func (h *Handler) LogsSetLevel(ctx context.Context, c *app.RequestContext) {
	var req logsLevelRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid body")
		return
	}
	if !logger.SetLevel(req.Level) {
		httputil.BadRequest(c, "invalid level; want one of debug|info|warn|error")
		return
	}
	c.JSON(http.StatusOK, map[string]any{"code": 0, "level": logger.GetLevel()})
}

// logsFrontendRequest lets the browser push captured console logs (errors,
// warnings, SSE diagnostics) into the same ring buffer so operators can see
// backend + frontend issues side-by-side in the settings UI.
type logsFrontendRequest struct {
	Entries []frontendEntry `json:"entries"`
}

type frontendEntry struct {
	Timestamp int64  `json:"timestamp"`
	Level     string `json:"level"`
	Source    string `json:"source"`
	Message   string `json:"message"`
}

// maxFrontendMessageLen caps per-entry message length so a misbehaving page
// can't push megabytes of text into the ring buffer with one request.
const maxFrontendMessageLen = 4096

// LogsFrontendReport accepts one or more browser log entries in a batch.
func (h *Handler) LogsFrontendReport(ctx context.Context, c *app.RequestContext) {
	var req logsFrontendRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid body")
		return
	}
	for _, e := range req.Entries {
		if e.Message == "" {
			continue
		}
		msg := e.Message
		if len(msg) > maxFrontendMessageLen {
			msg = msg[:maxFrontendMessageLen] + "…"
		}
		source := e.Source
		if source == "" {
			source = "frontend"
		} else if !strings.HasPrefix(source, "frontend") {
			source = "frontend/" + source
		}
		ts := time.Now()
		if e.Timestamp > 0 {
			ts = time.UnixMilli(e.Timestamp)
		}
		logger.AppendWithTime(ts, e.Level, source, msg)
	}
	c.JSON(http.StatusOK, map[string]any{"code": 0})
}
