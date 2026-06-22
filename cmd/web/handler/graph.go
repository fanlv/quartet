package handler

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/bytedance/sonic"
	"github.com/cloudwego/hertz/pkg/app"
	hertzConsts "github.com/cloudwego/hertz/pkg/protocol/consts"
	"github.com/cloudwego/hertz/pkg/protocol/sse"
	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/pkg/logger"
	graphsvc "github.com/fanlv/quartet/services/graph"
	"github.com/fanlv/quartet/types/consts"
	"github.com/fanlv/quartet/types/model"
	"github.com/google/uuid"
)

// formatGraphValidationErrors renders graph validation errors into a single
// human-readable line, preserving each error's locating context (node / edge /
// variable / config key) so the full reason reaches the user verbatim — per the
// repo convention of never hiding error detail. Used when a scheduled graph
// trigger refuses an invalid workflow template.
func formatGraphValidationErrors(errs []model.GraphValidationError) string {
	parts := make([]string, 0, len(errs))
	for _, e := range errs {
		var loc string
		switch {
		case e.NodeID != "":
			loc = "node " + e.NodeID
		case e.EdgeID != "":
			loc = "edge " + e.EdgeID
		case e.ConfigKey != "":
			loc = "config " + e.ConfigKey
		}
		if e.Variable != "" {
			if loc != "" {
				loc += " "
			}
			loc += "var " + e.Variable
		}
		if loc != "" {
			parts = append(parts, loc+": "+e.Message)
		} else {
			parts = append(parts, e.Message)
		}
	}
	return strings.Join(parts, "; ")
}

// CreateGraphWorkflow persists a new GraphWorkflow after full static
// validation. On validation failure it returns 400 with the complete list of
// located errors (not just a summary), so the frontend can pin every offending
// node/edge/variable/config key.
func (h *Handler) CreateGraphWorkflow(ctx context.Context, c *app.RequestContext) {
	var req model.CreateGraphWorkflowRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	if req.Name == "" {
		httputil.BadRequest(c, "name is required")
		return
	}

	wf, err := h.graphService.CreateWorkflow(ctx, &req)
	if err != nil {
		if verrs, ok := validationErrors(err); ok {
			c.JSON(http.StatusBadRequest, model.GraphWorkflowResponse{Errors: verrs})
			return
		}
		httputil.InternalError(c, err.Error())
		return
	}

	c.JSON(http.StatusOK, model.GraphWorkflowResponse{Workflow: wf})
}

func (h *Handler) ListGraphWorkflows(ctx context.Context, c *app.RequestContext) {
	workflows, err := h.graphService.ListWorkflows(ctx)
	if err != nil {
		httputil.InternalError(c, err.Error())
		return
	}
	if workflows == nil {
		workflows = []*model.GraphWorkflow{}
	}
	resp := model.GraphListWorkflowsResponse{Workflows: make([]model.GraphWorkflow, 0, len(workflows))}
	for _, wf := range workflows {
		resp.Workflows = append(resp.Workflows, *wf)
	}
	c.JSON(http.StatusOK, resp)
}

func (h *Handler) GetGraphWorkflow(ctx context.Context, c *app.RequestContext) {
	id := c.Param("workflowId")
	if id == "" {
		httputil.BadRequest(c, "workflowId is required")
		return
	}
	wf, err := h.graphService.GetWorkflow(ctx, id)
	if err != nil {
		httputil.MapError(c, err, []httputil.ErrorMapping{
			{Err: graphsvc.ErrWorkflowNotFound, Status: http.StatusNotFound},
		})
		return
	}
	c.JSON(http.StatusOK, model.GraphWorkflowResponse{Workflow: wf})
}

func (h *Handler) UpdateGraphWorkflow(ctx context.Context, c *app.RequestContext) {
	id := c.Param("workflowId")
	if id == "" {
		httputil.BadRequest(c, "workflowId is required")
		return
	}
	var req model.UpdateGraphWorkflowRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}

	wf, err := h.graphService.UpdateWorkflow(ctx, id, &req)
	if err != nil {
		if verrs, ok := validationErrors(err); ok {
			c.JSON(http.StatusBadRequest, model.GraphWorkflowResponse{Errors: verrs})
			return
		}
		httputil.MapError(c, err, []httputil.ErrorMapping{
			{Err: graphsvc.ErrWorkflowNotFound, Status: http.StatusNotFound},
		})
		return
	}

	c.JSON(http.StatusOK, model.GraphWorkflowResponse{Workflow: wf})
}

func (h *Handler) DeleteGraphWorkflow(ctx context.Context, c *app.RequestContext) {
	id := c.Param("workflowId")
	if id == "" {
		httputil.BadRequest(c, "workflowId is required")
		return
	}
	if err := h.graphService.DeleteWorkflow(ctx, id); err != nil {
		httputil.MapError(c, err, []httputil.ErrorMapping{
			{Err: graphsvc.ErrWorkflowNotFound, Status: http.StatusNotFound},
		})
		return
	}
	c.JSON(http.StatusOK, model.GraphWorkflowResponse{})
}

// ValidateGraphWorkflow runs the static legality check without persisting and
// always returns 200 with {valid, errors}.
func (h *Handler) ValidateGraphWorkflow(ctx context.Context, c *app.RequestContext) {
	var req model.ValidateGraphWorkflowRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	errs := h.graphService.ValidateConfig(ctx, &req.Config)
	c.JSON(http.StatusOK, model.GraphValidationResponse{
		Valid:  len(errs) == 0,
		Errors: errs,
	})
}

func (h *Handler) StartGraphRun(ctx context.Context, c *app.RequestContext) {
	var req model.StartGraphRunRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}

	// Launching a GraphWorkflow creates a Graph-type Job and binds a GraphRun to
	// it (see design §"GraphRun 与 Job 关系"). The frontend launches without a
	// jobId, so create one here when absent; an explicit jobId re-binds an
	// existing Job (e.g. resume of an interactive flow).
	freshJob := req.JobID == ""
	if freshJob {
		j, err := h.createGraphJob(ctx, &req)
		if err != nil {
			httputil.BadRequest(c, err.Error())
			return
		}
		req.JobID = j.ID
	}

	j, ok := h.jobService.Get(req.JobID)
	if !ok {
		httputil.NotFound(c, "job not found")
		return
	}
	runner := newJobRunner(h, j)
	run, err := h.graphService.StartRun(ctx, &req, runner, h.jobService)
	if err != nil {
		if verrs, ok := validationErrors(err); ok {
			c.JSON(http.StatusBadRequest, model.GraphRunResponse{Errors: verrs})
			return
		}
		httputil.MapError(c, err, []httputil.ErrorMapping{
			{Err: graphsvc.ErrWorkflowNotFound, Status: http.StatusNotFound},
			{Err: graphsvc.ErrGraphRunUnsupported, Status: http.StatusBadRequest},
			{Err: graphsvc.ErrGraphRunnerMissing, Status: http.StatusBadRequest},
		})
		return
	}

	// Generate a Job title from the workflow config (the full JSON) for freshly
	// launched runs, mirroring chat/loop Jobs. The resolved config on the run's
	// base snapshot is the complete workflow definition that actually executed.
	if freshJob && run != nil {
		if cfgJSON, mErr := sonic.MarshalString(run.BaseSnapshot.Config); mErr == nil {
			h.asyncUpdateGraphJobTitle(ctx, run.JobID, cfgJSON)
		} else {
			logger.Warnf(ctx, "[graph] marshal config for title failed: jobId=%s err=%v", run.JobID, mErr)
		}
	}

	c.JSON(http.StatusOK, model.GraphRunResponse{Run: run})
}

// createGraphJob builds and persists a Graph-type Job for a freshly launched
// GraphRun, resolving workspace/workdir the same way interactive and scheduled
// Jobs do (see createJob / triggerSchedule). req.WorkspaceID/Workdir are
// normalized to the resolved values so StartRun snapshots the same directory.
func (h *Handler) createGraphJob(ctx context.Context, req *model.StartGraphRunRequest) (*model.Job, error) {
	wsID := req.WorkspaceID
	if wsID == "" {
		wsID = consts.DefaultWorkspaceID
	}
	ws, ok := h.workspaceService.Get(wsID)
	if !ok {
		return nil, fmt.Errorf("workspace %s not found", wsID)
	}

	workdir := req.Workdir
	if workdir == "" {
		workdir = ws.Workdir
	}
	if err := validateWorkdir(workdir); err != nil {
		return nil, err
	}
	if err := ensureWorkdirWithinWorkspace(workdir, ws.Workdir); err != nil {
		return nil, err
	}

	j := model.NewJob(workdir, wsID)
	j.Mode = model.JobModeGraph
	j.Title = "Graph Run"

	if err := h.jobService.Create(j); err != nil {
		return nil, fmt.Errorf("create graph job failed: %w", err)
	}

	req.WorkspaceID = wsID
	req.Workdir = workdir
	logger.Infof(ctx, "[graph] created graph job: jobId=%s workspaceId=%s workdir=%s", j.ID, wsID, workdir)
	return j, nil
}

func (h *Handler) GetGraphRunStatus(ctx context.Context, c *app.RequestContext) {
	runID := c.Param("runId")
	if runID == "" {
		httputil.BadRequest(c, "runId is required")
		return
	}
	resp, err := h.graphService.GetRunStatus(ctx, runID)
	if err != nil {
		httputil.MapError(c, err, []httputil.ErrorMapping{
			{Err: graphsvc.ErrGraphRunNotFound, Status: http.StatusNotFound},
		})
		return
	}
	c.JSON(http.StatusOK, resp)
}

func (h *Handler) GraphRunEvents(ctx context.Context, c *app.RequestContext) {
	runID := c.Param("runId")
	if runID == "" {
		httputil.BadRequest(c, "runId is required")
		return
	}
	if _, err := h.graphService.GetRunStatus(ctx, runID); err != nil {
		httputil.MapError(c, err, []httputil.ErrorMapping{
			{Err: graphsvc.ErrGraphRunNotFound, Status: http.StatusNotFound},
		})
		return
	}

	connID := uuid.NewString()[:8]
	startLine := parseGraphEventLine(string(c.GetHeader("Last-Event-ID")))
	logger.Infof(ctx, "[graph-sse] subscribe: connId=%s runId=%s startLine=%d", connID, runID, startLine)
	defer func(started time.Time) {
		logger.Debugf(ctx, "[graph-sse] unsubscribe: connId=%s runId=%s lifetime=%s", connID, runID, time.Since(started).Round(time.Millisecond))
	}(time.Now())

	c.SetStatusCode(hertzConsts.StatusOK)
	w := sse.NewWriter(c)
	if err := w.WriteKeepAlive(); err != nil {
		if isClientDisconnectErr(err) {
			logger.Debugf(ctx, "[graph-sse] initial keep-alive failed (client disconnected): connId=%s runId=%s err=%v", connID, runID, err)
		} else {
			logger.Errorf(ctx, "[graph-sse] initial keep-alive failed: connId=%s runId=%s err=%v", connID, runID, err)
		}
		return
	}

	idleSince := time.Now()
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}

		resp, err := h.graphService.ListRunEvents(ctx, runID, startLine, nil)
		if err != nil {
			logger.Errorf(ctx, "[graph-sse] list events failed: connId=%s runId=%s startLine=%d err=%v", connID, runID, startLine, err)
			return
		}
		if len(resp.Events) == 0 {
			runStatus, err := h.graphService.GetRunStatus(ctx, runID)
			if err != nil {
				logger.Errorf(ctx, "[graph-sse] refresh status failed: connId=%s runId=%s err=%v", connID, runID, err)
				return
			}
			if runStatus.Run != nil && graphRunSSETerminal(runStatus.Run.Status) && time.Since(idleSince) >= sseTerminalIdleTimeout {
				logger.Debugf(ctx, "[graph-sse] closing idle terminal connection: connId=%s runId=%s status=%s", connID, runID, runStatus.Run.Status)
				return
			}
			if time.Since(idleSince) >= sseKeepAliveInterval {
				if err := writeWithTimeout(ctx, c, connID, runID, 0, func() error { return w.WriteKeepAlive() }, sseWriteTimeout); err != nil {
					logGraphSSEWriteError(ctx, "keep-alive", connID, runID, 0, err)
					return
				}
				idleSince = time.Now()
			}
			continue
		}

		idleSince = time.Now()
		for i, event := range resp.Events {
			lineID := startLine + i + 1
			data, err := sonic.Marshal(event)
			if err != nil {
				logger.Errorf(ctx, "[graph-sse] marshal event failed: connId=%s runId=%s line=%d err=%v", connID, runID, lineID, err)
				continue
			}
			if err := writeWithTimeout(ctx, c, connID, runID, uint64(lineID), func() error {
				return w.WriteEvent(strconv.Itoa(lineID), "message", data)
			}, sseWriteTimeout); err != nil {
				logGraphSSEWriteError(ctx, "event", connID, runID, uint64(lineID), err)
				return
			}
		}
		startLine = resp.NextLine
	}
}

func (h *Handler) ListGraphRuns(ctx context.Context, c *app.RequestContext) {
	runs, err := h.graphService.ListRuns(ctx)
	if err != nil {
		httputil.InternalError(c, err.Error())
		return
	}
	resp := model.GraphRunHistoryResponse{Runs: make([]model.GraphRun, 0, len(runs))}
	for _, run := range runs {
		resp.Runs = append(resp.Runs, *run)
	}
	c.JSON(http.StatusOK, resp)
}

// StopGraphRun hard-stops a running GraphRun.
func (h *Handler) StopGraphRun(ctx context.Context, c *app.RequestContext) {
	h.graphRunControl(ctx, c, func(runID string) (*model.GraphRun, error) {
		return h.graphService.StopRun(ctx, runID)
	})
}

// PauseGraphRun gracefully pauses a running GraphRun.
func (h *Handler) PauseGraphRun(ctx context.Context, c *app.RequestContext) {
	h.graphRunControl(ctx, c, func(runID string) (*model.GraphRun, error) {
		return h.graphService.PauseRun(ctx, runID)
	})
}

// StepStopGraphRun freezes the current ready batch and stops after it.
func (h *Handler) StepStopGraphRun(ctx context.Context, c *app.RequestContext) {
	h.graphRunControl(ctx, c, func(runID string) (*model.GraphRun, error) {
		return h.graphService.StepStopRun(ctx, runID)
	})
}

// CancelStopGraphRun cancels a pending pause / step-stop, returning the run to
// running.
func (h *Handler) CancelStopGraphRun(ctx context.Context, c *app.RequestContext) {
	h.graphRunControl(ctx, c, func(runID string) (*model.GraphRun, error) {
		return h.graphService.CancelStopRun(ctx, runID)
	})
}

// graphRunControl runs a control action that resolves to a run snapshot and
// maps the common control errors.
func (h *Handler) graphRunControl(_ context.Context, c *app.RequestContext, action func(runID string) (*model.GraphRun, error)) {
	runID := c.Param("runId")
	if runID == "" {
		httputil.BadRequest(c, "runId is required")
		return
	}
	run, err := action(runID)
	if err != nil {
		httputil.MapError(c, err, []httputil.ErrorMapping{
			{Err: graphsvc.ErrGraphRunNotFound, Status: http.StatusNotFound},
			{Err: graphsvc.ErrGraphRunNotRunning, Status: http.StatusConflict},
		})
		return
	}
	c.JSON(http.StatusOK, model.GraphRunResponse{Run: run})
}

// ResumeGraphRun relaunches a resumable GraphRun on its bound Job.
func (h *Handler) ResumeGraphRun(ctx context.Context, c *app.RequestContext) {
	runID := c.Param("runId")
	if runID == "" {
		httputil.BadRequest(c, "runId is required")
		return
	}
	status, err := h.graphService.GetRunStatus(ctx, runID)
	if err != nil {
		httputil.MapError(c, err, []httputil.ErrorMapping{
			{Err: graphsvc.ErrGraphRunNotFound, Status: http.StatusNotFound},
		})
		return
	}
	if status.Run == nil {
		httputil.NotFound(c, "graph run not found")
		return
	}
	j, ok := h.jobService.Get(status.Run.JobID)
	if !ok {
		httputil.NotFound(c, "job not found")
		return
	}
	runner := newJobRunner(h, j)
	run, err := h.graphService.ResumeRun(ctx, runID, runner, h.jobService)
	if err != nil {
		httputil.MapError(c, err, []httputil.ErrorMapping{
			{Err: graphsvc.ErrGraphRunNotFound, Status: http.StatusNotFound},
			{Err: graphsvc.ErrGraphRunNotResumable, Status: http.StatusConflict},
			{Err: graphsvc.ErrGraphRunnerMissing, Status: http.StatusBadRequest},
		})
		return
	}
	c.JSON(http.StatusOK, model.GraphRunResponse{Run: run})
}

// UpdateGraphRunVersion appends a new graph version after validating the edit
// against persisted run state. For live runs the scheduler applies the new
// version to not-yet-started instances while in-flight/completed instances keep
// their execution-time version.
func (h *Handler) UpdateGraphRunVersion(ctx context.Context, c *app.RequestContext) {
	runID := c.Param("runId")
	if runID == "" {
		httputil.BadRequest(c, "runId is required")
		return
	}
	var req model.UpdateGraphRunVersionRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
		return
	}
	status, err := h.graphService.GetRunStatus(ctx, runID)
	if err != nil {
		httputil.MapError(c, err, []httputil.ErrorMapping{
			{Err: graphsvc.ErrGraphRunNotFound, Status: http.StatusNotFound},
		})
		return
	}
	if status.Run == nil {
		httputil.NotFound(c, "graph run not found")
		return
	}
	j, ok := h.jobService.Get(status.Run.JobID)
	if !ok {
		httputil.NotFound(c, "job not found")
		return
	}
	runner := newJobRunner(h, j)
	run, err := h.graphService.UpdateRunVersion(ctx, runID, &req, runner)
	if err != nil {
		if verrs, ok := validationErrors(err); ok {
			c.JSON(http.StatusBadRequest, model.GraphRunResponse{Errors: verrs})
			return
		}
		httputil.MapError(c, err, []httputil.ErrorMapping{
			{Err: graphsvc.ErrGraphRunNotFound, Status: http.StatusNotFound},
			{Err: graphsvc.ErrGraphRunNotEditable, Status: http.StatusConflict},
		})
		return
	}
	c.JSON(http.StatusOK, model.GraphRunResponse{Run: run})
}

// DeleteGraphRun deletes a non-in-flight GraphRun, cascading its artifacts.
func (h *Handler) DeleteGraphRun(ctx context.Context, c *app.RequestContext) {
	runID := c.Param("runId")
	if runID == "" {
		httputil.BadRequest(c, "runId is required")
		return
	}
	if err := h.graphService.DeleteRun(ctx, runID, h.jobService); err != nil {
		httputil.MapError(c, err, []httputil.ErrorMapping{
			{Err: graphsvc.ErrGraphRunNotFound, Status: http.StatusNotFound},
			{Err: graphsvc.ErrGraphRunInFlight, Status: http.StatusConflict},
		})
		return
	}
	c.JSON(http.StatusOK, model.GraphRunResponse{})
}

// validationErrors unwraps a service ValidationError into its located error
// list. Returns ok=false for non-validation errors.
func validationErrors(err error) ([]model.GraphValidationError, bool) {
	var verr *graphsvc.ValidationError
	if errors.As(err, &verr) {
		return verr.Errors, true
	}
	return nil, false
}

func parseGraphEventLine(value string) int {
	if value == "" {
		return 0
	}
	line, err := strconv.Atoi(value)
	if err != nil || line < 0 {
		return 0
	}
	return line
}

func graphRunSSETerminal(status model.GraphRunStatus) bool {
	switch status {
	case model.GraphRunStatusCompleted,
		model.GraphRunStatusFailed,
		model.GraphRunStatusPaused,
		model.GraphRunStatusStepStopped,
		model.GraphRunStatusStopped,
		model.GraphRunStatusTimedOut,
		model.GraphRunStatusRecovering:
		return true
	default:
		return false
	}
}

func logGraphSSEWriteError(ctx context.Context, kind, connID, runID string, seq uint64, err error) {
	switch {
	case errors.Is(err, errSSEWriteTimeout):
		logger.Warnf(ctx, "[graph-sse] %s write timed out: connId=%s runId=%s seq=%d timeout=%s", kind, connID, runID, seq, sseWriteTimeout)
	case isClientDisconnectErr(err):
		logger.Debugf(ctx, "[graph-sse] client disconnected during %s write: connId=%s runId=%s seq=%d err=%v", kind, connID, runID, seq, err)
	default:
		logger.Errorf(ctx, "[graph-sse] %s write failed: connId=%s runId=%s seq=%d err=%v", kind, connID, runID, seq, err)
	}
}
