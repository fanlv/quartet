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

func graphWorkflowErrorMappings() []httputil.ErrorMapping {
	return []httputil.ErrorMapping{
		{Err: graphsvc.ErrWorkflowBadRequest, Status: http.StatusBadRequest},
		{Err: graphsvc.ErrWorkflowNotFound, Status: http.StatusNotFound},
		{Err: graphsvc.ErrWorkflowConflict, Status: http.StatusConflict},
	}
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
	wf, err := h.graphService.CreateWorkflow(ctx, &req)
	if err != nil {
		if verrs, ok := validationErrors(err); ok {
			c.JSON(http.StatusBadRequest, model.GraphWorkflowResponse{Errors: verrs})
			return
		}
		httputil.MapError(c, err, graphWorkflowErrorMappings())
		return
	}

	c.JSON(http.StatusOK, model.GraphWorkflowResponse{Workflow: wf})
}

func (h *Handler) ListGraphWorkflows(ctx context.Context, c *app.RequestContext) {
	// Workflows are global: any workflow can run in any workspace, so the
	// library lists every workflow regardless of the caller's active workspace.
	// A workflow's workspaceId is kept only as a display hint and as the default
	// workspace pre-selected when launching a run.
	workflows, warnings, err := h.graphService.ListWorkflows(ctx)
	if err != nil {
		httputil.InternalError(c, err.Error())
		return
	}
	if workflows == nil {
		workflows = []*model.GraphWorkflow{}
	}
	resp := model.GraphListWorkflowsResponse{
		Workflows: make([]model.GraphWorkflowSummary, 0, len(workflows)),
		Warnings:  warnings,
	}
	for _, wf := range workflows {
		if wf == nil {
			continue
		}
		resp.Workflows = append(resp.Workflows, model.GraphWorkflowSummary{
			ID:          wf.ID,
			WorkspaceID: wf.WorkspaceID,
			Name:        wf.Name,
			Description: wf.Description,
			Type:        wf.Type,
			CreatedAt:   wf.CreatedAt,
			UpdatedAt:   wf.UpdatedAt,
			NodeCount:   len(wf.Config.Nodes),
			EdgeCount:   len(wf.Config.Edges),
		})
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
		httputil.MapError(c, err, graphWorkflowErrorMappings())
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
		httputil.MapError(c, err, graphWorkflowErrorMappings())
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
	var req model.DeleteGraphWorkflowRequest
	var expectedUpdatedAt *time.Time
	if len(c.Request.Body()) > 0 {
		if err := c.BindJSON(&req); err != nil {
			httputil.BadRequest(c, "invalid request: "+err.Error())
			return
		}
		expectedUpdatedAt = req.UpdatedAt
	}
	if err := h.graphService.DeleteWorkflow(ctx, id, expectedUpdatedAt); err != nil {
		httputil.MapError(c, err, graphWorkflowErrorMappings())
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
		j, err := h.graphService.CreateRunJob(ctx, &req, h.jobService, h.workspaceService)
		if err != nil {
			httputil.MapError(c, err, []httputil.ErrorMapping{
				{Err: graphsvc.ErrWorkflowNotFound, Status: http.StatusNotFound},
				{Err: graphsvc.ErrWorkflowConflict, Status: http.StatusConflict},
				{Err: graphsvc.ErrWorkflowBadRequest, Status: http.StatusBadRequest},
			})
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
		// StartRun validates the config and persists the GraphRun before binding
		// it to the Job (SetGraphRunState). Every error path here returns before
		// that bind and before the scheduler goroutine launches, so a Job created
		// just above (freshJob) would otherwise linger with an empty GraphRunID —
		// a phantom "Graph Run" row that resolveJobGraphRun later rejects with
		// "graph run not found". Roll it back. An explicit jobId (resume) is owned
		// by the caller and never auto-deleted.
		if freshJob {
			if mErr := h.jobService.MarkDeleted(req.JobID); mErr != nil {
				logger.Warnf(ctx, "[graph] rollback mark-deleted failed: jobId=%s err=%v", req.JobID, mErr)
			}
			h.jobService.Delete(req.JobID)
			logger.Infof(ctx, "[graph] rolled back empty graph job after StartRun failure: jobId=%s err=%v", req.JobID, err)
		}
		if verrs, ok := validationErrors(err); ok {
			c.JSON(http.StatusBadRequest, model.GraphRunResponse{Errors: verrs})
			return
		}
		httputil.MapError(c, err, []httputil.ErrorMapping{
			{Err: graphsvc.ErrWorkflowNotFound, Status: http.StatusNotFound},
			{Err: graphsvc.ErrWorkflowConflict, Status: http.StatusConflict},
			{Err: graphsvc.ErrWorkflowBadRequest, Status: http.StatusBadRequest},
			{Err: graphsvc.ErrGraphRunUnsupported, Status: http.StatusBadRequest},
			{Err: graphsvc.ErrGraphRunnerMissing, Status: http.StatusBadRequest},
		})
		return
	}

	// Generate a Job title from the workflow config (the full JSON) for freshly
	// launched runs, mirroring chat/loop Jobs. effectiveConfig is the complete
	// workflow definition that actually executed (current version), and stays
	// correct now that the base snapshot no longer stores the config.
	if freshJob && run != nil {
		if cfgJSON, mErr := sonic.MarshalString(graphsvc.EffectiveConfig(run)); mErr == nil {
			h.asyncUpdateGraphJobTitle(ctx, run.JobID, cfgJSON)
		} else {
			logger.Warnf(ctx, "[graph] marshal config for title failed: jobId=%s err=%v", run.JobID, mErr)
		}
	}

	c.JSON(http.StatusOK, model.GraphRunResponse{Run: run})
}

func (h *Handler) resolveJobGraphRun(ctx context.Context, c *app.RequestContext) (*model.Job, string, bool) {
	jobID := c.Param("jobId")
	if jobID == "" {
		httputil.BadRequest(c, "jobId is required")
		return nil, "", false
	}
	j, ok := h.jobService.Get(jobID)
	if !ok || j == nil || j.Deleted {
		httputil.NotFound(c, "job not found")
		return nil, "", false
	}
	if j.Mode != model.JobModeGraph {
		httputil.BadRequest(c, "job is not a graph job")
		return nil, "", false
	}
	if strings.TrimSpace(j.GraphRunID) == "" {
		httputil.NotFound(c, "graph run not found")
		return nil, "", false
	}
	if err := h.graphService.RegisterRunLocation(ctx, j.GraphRunID, j.WorkspaceID, j.ID); err != nil {
		httputil.InternalError(c, err.Error())
		return nil, "", false
	}
	return j, j.GraphRunID, true
}

func (h *Handler) GetJobGraphRunStatus(ctx context.Context, c *app.RequestContext) {
	_, runID, ok := h.resolveJobGraphRun(ctx, c)
	if !ok {
		return
	}
	resp, err := h.graphService.GetRunStatus(ctx, runID)
	if err != nil {
		httputil.MapError(c, err, []httputil.ErrorMapping{
			{Err: graphsvc.ErrGraphRunNotFound, Status: http.StatusNotFound},
		})
		return
	}
	c.JSON(http.StatusOK, slimGraphRunStatus(resp))
}

// JobGraphRunHooks returns the per-node hook (§ 节点 Hook) execution results for
// a job's current graph run, so the run-view node-detail panel can show what each
// node's side-effect script did (exit code, stdout, stderr). Mirrors
// GetJobGraphRunStatus's job→run resolution and not-found mapping.
func (h *Handler) JobGraphRunHooks(ctx context.Context, c *app.RequestContext) {
	_, runID, ok := h.resolveJobGraphRun(ctx, c)
	if !ok {
		return
	}
	resp, err := h.graphService.ListHookResults(ctx, runID)
	if err != nil {
		httputil.MapError(c, err, []httputil.ErrorMapping{
			{Err: graphsvc.ErrGraphRunNotFound, Status: http.StatusNotFound},
		})
		return
	}
	c.JSON(http.StatusOK, resp)
}

// slimGraphRunStatus trims redundancy from the run-status payload before it is
// serialised to the client. A long graph run shipped this response three times
// over (the full instance set appeared as the top-level Instances list, again
// as progress.Instances, and a third time nested under run.Progress.Instances),
// and every instance inlined its variable snapshot — visibleVariables /
// outputVariables hold large node outputs such as _last_assistant_msg (up to
// ~12KB each). On a 91-instance run that was ~3.1MB of a 3.5MB response.
//
// On top of that, run.baseSnapshot and every run.versions[] entry each carry a
// full model/agent content snapshot (frozen system prompts), and those are the
// single largest remaining contributor (~200KB each, duplicated across the base
// snapshot and the baseline version). The client never reads them — it only
// uses versions[].config to draw the canvas — so they are stripped here too.
//
// The authoritative copy the client renders is the top-level Instances list;
// it reads progress only for its count fields and run.Progress only as a
// fallback, and never reads instance variable snapshots (they load per node
// from session messages). Trimming lives in the handler so the service keeps
// returning the full state for callers that need it (e.g. tests asserting
// engine-computed variables, and audit reads of the frozen prompts). The
// service hands back freshly-deserialised objects with no shared cache, so
// mutating them here cannot affect on-disk state or other requests.
func slimGraphRunStatus(resp *model.GraphRunStatusResponse) *model.GraphRunStatusResponse {
	if resp == nil {
		return resp
	}
	for i := range resp.Instances {
		resp.Instances[i].VisibleVariables = nil
		resp.Instances[i].OutputVariables = nil
		resp.Instances[i].VariableWriters = nil
	}
	if resp.Progress != nil {
		resp.Progress.Instances = nil
	}
	if resp.Run != nil {
		if resp.Run.Progress != nil {
			resp.Run.Progress.Instances = nil
		}
		// The frozen prompt snapshots are audit-only and never read by the
		// client; drop them from both the base snapshot and every version.
		resp.Run.BaseSnapshot.ModelSnapshots = nil
		resp.Run.BaseSnapshot.AgentSnapshots = nil
		for i := range resp.Run.Versions {
			resp.Run.Versions[i].ModelSnapshots = nil
			resp.Run.Versions[i].AgentSnapshots = nil
		}
		// archivedInstances feeds the chat session sidebar (key + sessionId);
		// it never needs the variable snapshots either.
		for k, st := range resp.Run.ArchivedInstances {
			st.VisibleVariables = nil
			st.OutputVariables = nil
			st.VariableWriters = nil
			resp.Run.ArchivedInstances[k] = st
		}
	}
	return resp
}

func (h *Handler) JobGraphRunEvents(ctx context.Context, c *app.RequestContext) {
	_, runID, ok := h.resolveJobGraphRun(ctx, c)
	if !ok {
		return
	}
	if _, err := h.graphService.GetRunStatus(ctx, runID); err != nil {
		httputil.MapError(c, err, []httputil.ErrorMapping{
			{Err: graphsvc.ErrGraphRunNotFound, Status: http.StatusNotFound},
		})
		return
	}

	connID := uuid.NewString()[:8]
	startSeq := graphParseLastEventSeq(string(c.GetHeader("Last-Event-ID")))

	// A fresh subscriber (no Last-Event-ID → startSeq=0) means "start at the
	// buffer tail". The buffer's GC advances headSeq freely while no reader is
	// connected (minCursor=MaxUint64), so by the time the first client connects
	// headSeq has almost always moved past 0 — a literal Subscribe(0) would then
	// be rejected as ErrSeqGone and the client would stop reconnecting, killing
	// the live stream until a manual refresh. Resolve 0 to the live buffer's
	// current SnapshotSeq so the new reader cursors at the tail. Only for live
	// runs: the disk-replay path below uses startSeq as a line offset where 0
	// correctly means "replay from the first event".
	if startSeq == 0 {
		if freshSeq, ok := h.graphService.RunEventSnapshotSeq(runID); ok {
			startSeq = freshSeq
		}
	}

	// Subscribe to the live in-memory buffer. live=false means this run is not
	// active in this process (e.g. opened after a restart, or already terminal
	// with its buffer freed) — fall back to a one-time disk replay of the
	// persisted structural events. ErrSeqGone (resume point GC'd) maps to 410
	// so the client reconciles from the run snapshot.
	reader, live, err := h.graphService.SubscribeRunEvents(runID, startSeq)
	if errors.Is(err, graphsvc.ErrSeqGone) && startSeq > 0 {
		// Snapshot/subscribe race: GC advanced headSeq between the client's
		// status fetch and this subscribe. Retry once from the buffer's
		// current tail.
		if freshSeq, ok := h.graphService.RunEventSnapshotSeq(runID); ok {
			logger.Infof(ctx, "[graph-sse] subscribe race hit, retrying: connId=%s runId=%s originalSeq=%d freshSeq=%d", connID, runID, startSeq, freshSeq)
			reader, live, err = h.graphService.SubscribeRunEvents(runID, freshSeq)
		}
	}
	if errors.Is(err, graphsvc.ErrSeqGone) {
		logger.Infof(ctx, "[graph-sse] subscribe gone: connId=%s runId=%s startSeq=%d (client should reconcile snapshot)", connID, runID, startSeq)
		c.AbortWithStatusJSON(http.StatusGone, map[string]string{
			"error": fmt.Sprintf("event buffer no longer contains seq=%d for run %s; reconcile snapshot", startSeq, runID),
		})
		return
	}
	if err != nil {
		logger.Errorf(ctx, "[graph-sse] subscribe failed: connId=%s runId=%s startSeq=%d err=%v", connID, runID, startSeq, err)
		httputil.InternalError(c, fmt.Sprintf("subscribe failed: %v", err))
		return
	}

	logger.Infof(ctx, "[graph-sse] subscribe: connId=%s runId=%s startSeq=%d live=%t", connID, runID, startSeq, live)
	defer func(started time.Time) {
		logger.Debugf(ctx, "[graph-sse] unsubscribe: connId=%s runId=%s live=%t lifetime=%s", connID, runID, live, time.Since(started).Round(time.Millisecond))
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

	if !live {
		h.graphRunEventsReplayFromDisk(ctx, c, w, connID, runID, startSeq)
		return
	}

	defer reader.Close()
	// Single-writer loop: events and keep-alives are written from the same
	// goroutine. ReadWithTimeout returns GraphReadTimeout when no event arrives
	// within sseKeepAliveInterval; we then write a keep-alive and re-read. The
	// loop does NOT exit on terminal events — a resume reuses the same buffer,
	// so its events land on this reader without a reconnect. It exits on:
	//   - GraphReadClosed: buffer closed (run deleted)
	//   - write failure: client disconnected or TCP wedged
	//   - terminal idle timeout: no new events for sseTerminalIdleTimeout after
	//     the run reached a terminal state.
	var terminalIdleSince time.Time
	for {
		entries, status := reader.ReadWithTimeout(ctx, sseKeepAliveInterval, graphSSEReadBatchSize)
		switch status {
		case graphsvc.GraphReadClosed:
			return
		case graphsvc.GraphReadTimeout:
			if runStatus, err := h.graphService.GetRunStatus(ctx, runID); err == nil &&
				runStatus.Run != nil && graphRunSSETerminal(runStatus.Run.Status) {
				if terminalIdleSince.IsZero() {
					terminalIdleSince = time.Now()
				} else if time.Since(terminalIdleSince) >= sseTerminalIdleTimeout {
					logger.Debugf(ctx, "[graph-sse] closing idle terminal connection: connId=%s runId=%s status=%s", connID, runID, runStatus.Run.Status)
					return
				}
			} else {
				terminalIdleSince = time.Time{}
			}
			if err := writeWithTimeout(ctx, c, connID, runID, 0, func() error { return w.WriteKeepAlive() }, sseWriteTimeout); err != nil {
				logGraphSSEWriteError(ctx, "keep-alive", connID, runID, 0, err)
				return
			}
			continue
		}

		terminalIdleSince = time.Time{}
		for _, entry := range entries {
			data, err := sonic.Marshal(entry.Event)
			if err != nil {
				logger.Errorf(ctx, "[graph-sse] marshal event failed: connId=%s runId=%s seq=%d err=%v", connID, runID, entry.Seq, err)
				if entry.Seq > 0 {
					reader.Ack(entry.Seq)
				}
				continue
			}
			id := ""
			if entry.Seq > 0 {
				id = strconv.FormatUint(entry.Seq, 10)
			}
			if err := writeWithTimeout(ctx, c, connID, runID, entry.Seq, func() error {
				return w.WriteEvent(id, "message", data)
			}, sseWriteTimeout); err != nil {
				logGraphSSEWriteError(ctx, "event", connID, runID, entry.Seq, err)
				return
			}
			if entry.Seq > 0 {
				reader.Ack(entry.Seq)
			}
		}
	}
}

// graphRunEventsReplayFromDisk serves the SSE stream for a run that has no live
// in-memory buffer in this process (opened after a restart, or already terminal
// with its buffer freed). It replays the persisted structural events once from
// startSeq — streaming agent deltas were never persisted, so the conversation
// is recovered separately from each node's session messages, and the canvas
// from the replayed instance lifecycle events + the run-status snapshot — then
// holds the connection with keep-alives until the client leaves or the terminal
// idle timeout elapses. No new events can arrive (the run is not active here),
// so this is replay-then-idle, not a live tail.
func (h *Handler) graphRunEventsReplayFromDisk(ctx context.Context, c *app.RequestContext, w *sse.Writer, connID, runID string, startSeq uint64) {
	startLine := int(startSeq)
	// limit=0 → service applies its default replay bound. The replay is a
	// best-effort structural backfill; the canvas is rebuilt from the run
	// snapshot (GET /job/:jobId/graph-run), so a bounded/truncated replay is safe.
	resp, err := h.graphService.ListReplayEvents(ctx, runID, startLine, 0)
	if err != nil {
		logger.Errorf(ctx, "[graph-sse] replay list events failed: connId=%s runId=%s startLine=%d err=%v", connID, runID, startLine, err)
		h.writeGraphSSEError(ctx, c, w, connID, runID, "replay list events failed: "+err.Error())
		return
	}
	for i, event := range resp.Events {
		lineID := startLine + i + 1
		data, err := sonic.Marshal(event)
		if err != nil {
			logger.Errorf(ctx, "[graph-sse] replay marshal event failed: connId=%s runId=%s line=%d err=%v", connID, runID, lineID, err)
			continue
		}
		if err := writeWithTimeout(ctx, c, connID, runID, uint64(lineID), func() error {
			return w.WriteEvent(strconv.Itoa(lineID), "message", data)
		}, sseWriteTimeout); err != nil {
			logGraphSSEWriteError(ctx, "replay-event", connID, runID, uint64(lineID), err)
			return
		}
	}

	// Replay done; no live producer in this process. Hold with keep-alives so
	// the client's EventSource stays open, closing after the terminal idle
	// timeout (the run is terminal whenever its buffer is absent here).
	idleSince := time.Now()
	ticker := time.NewTicker(sseKeepAliveInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		if time.Since(idleSince) >= sseTerminalIdleTimeout {
			logger.Debugf(ctx, "[graph-sse] closing idle replay connection: connId=%s runId=%s", connID, runID)
			return
		}
		if err := writeWithTimeout(ctx, c, connID, runID, 0, func() error { return w.WriteKeepAlive() }, sseWriteTimeout); err != nil {
			logGraphSSEWriteError(ctx, "replay-keep-alive", connID, runID, 0, err)
			return
		}
	}
}

func (h *Handler) writeGraphSSEError(ctx context.Context, c *app.RequestContext, w *sse.Writer, connID, runID, message string) {
	event := model.GraphEvent{
		ID:      model.NewGraphEventID(),
		RunID:   runID,
		Type:    model.GraphEventTypeError,
		Message: message,
		Error: &model.GraphRuntimeError{
			RunID:   runID,
			Message: message,
		},
		CreatedAt: time.Now().UnixMilli(),
	}
	data, err := sonic.Marshal(event)
	if err != nil {
		logger.Errorf(ctx, "[graph-sse] replay error marshal failed: connId=%s runId=%s err=%v original=%s", connID, runID, err, message)
		return
	}
	if err := writeWithTimeout(ctx, c, connID, runID, 0, func() error {
		return w.WriteEvent("", "message", data)
	}, sseWriteTimeout); err != nil {
		logGraphSSEWriteError(ctx, "replay-error", connID, runID, 0, err)
	}
}

// StopJobGraphRun hard-stops a running GraphRun bound to a Job.
func (h *Handler) StopJobGraphRun(ctx context.Context, c *app.RequestContext) {
	h.jobGraphRunControl(ctx, c, func(runID, reason string) (*model.GraphRun, error) {
		return h.graphService.StopRun(ctx, runID, reason)
	})
}

// StepStopJobGraphRun freezes the current ready batch and stops after it.
func (h *Handler) StepStopJobGraphRun(ctx context.Context, c *app.RequestContext) {
	h.jobGraphRunControl(ctx, c, func(runID, reason string) (*model.GraphRun, error) {
		return h.graphService.StepStopRun(ctx, runID, reason)
	})
}

// CancelStopJobGraphRun cancels a pending step-stop, returning the run to
// running.
func (h *Handler) CancelStopJobGraphRun(ctx context.Context, c *app.RequestContext) {
	h.jobGraphRunControl(ctx, c, func(runID, reason string) (*model.GraphRun, error) {
		return h.graphService.CancelStopRun(ctx, runID, reason)
	})
}

// jobGraphRunControl runs a control action that resolves to a run snapshot and
// maps the common control errors.
func (h *Handler) jobGraphRunControl(ctx context.Context, c *app.RequestContext, action func(runID, reason string) (*model.GraphRun, error)) {
	_, runID, ok := h.resolveJobGraphRun(ctx, c)
	if !ok {
		return
	}
	var req model.GraphRunActionRequest
	if len(c.Request.Body()) > 0 {
		if err := c.BindJSON(&req); err != nil {
			httputil.BadRequest(c, "invalid request: "+err.Error())
			return
		}
	}
	run, err := action(runID, req.Reason)
	if err != nil {
		httputil.MapError(c, err, []httputil.ErrorMapping{
			{Err: graphsvc.ErrGraphRunNotFound, Status: http.StatusNotFound},
			{Err: graphsvc.ErrGraphRunNotRunning, Status: http.StatusConflict},
			{Err: graphsvc.ErrGraphRunControlBusy, Status: http.StatusConflict},
		})
		return
	}
	c.JSON(http.StatusOK, model.GraphRunResponse{Run: run})
}

// ResumeJobGraphRun relaunches a resumable GraphRun on its bound Job.
func (h *Handler) ResumeJobGraphRun(ctx context.Context, c *app.RequestContext) {
	j, runID, ok := h.resolveJobGraphRun(ctx, c)
	if !ok {
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

// ContinueJobGraphRun continues a GraphRun parked at「等待人工」on its bound Job
// (§5 讨论完成/续跑): the user finished discussing in the clarify node session and
// clicked「讨论完成」. The clarify instances are finalized (capturing each session's
// discussion 结论 into their output variables) and the DAG resumes.
func (h *Handler) ContinueJobGraphRun(ctx context.Context, c *app.RequestContext) {
	j, runID, ok := h.resolveJobGraphRun(ctx, c)
	if !ok {
		return
	}
	runner := newJobRunner(h, j)
	run, err := h.graphService.ContinueRun(ctx, runID, runner, h.jobService)
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
func (h *Handler) UpdateJobGraphRunVersion(ctx context.Context, c *app.RequestContext) {
	j, runID, ok := h.resolveJobGraphRun(ctx, c)
	if !ok {
		return
	}
	var req model.UpdateGraphRunVersionRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request: "+err.Error())
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
			{Err: graphsvc.ErrGraphRunControlBusy, Status: http.StatusConflict},
		})
		return
	}
	c.JSON(http.StatusOK, model.GraphRunResponse{Run: run})
}

// DeleteJobGraphRun deletes a non-in-flight GraphRun, cascading its artifacts.
func (h *Handler) DeleteJobGraphRun(ctx context.Context, c *app.RequestContext) {
	_, runID, ok := h.resolveJobGraphRun(ctx, c)
	if !ok {
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

// graphParseLastEventSeq parses the SSE Last-Event-ID header into a resume seq.
// Empty / non-numeric / negative all collapse to 0 ("start at the buffer tail").
func graphParseLastEventSeq(value string) uint64 {
	if value == "" {
		return 0
	}
	seq, err := strconv.ParseUint(value, 10, 64)
	if err != nil {
		return 0
	}
	return seq
}

// graphSSEReadBatchSize bounds how many buffered events one ReadWithTimeout
// returns, so a burst of streaming deltas is written in chunks interleaved with
// keep-alive opportunities rather than one giant batch.
const graphSSEReadBatchSize = 32

func graphRunSSETerminal(status model.GraphRunStatus) bool {
	switch status {
	case model.GraphRunStatusCompleted,
		model.GraphRunStatusFailed,
		model.GraphRunStatusStepStopped,
		model.GraphRunStatusStopped,
		model.GraphRunStatusTimedOut,
		model.GraphRunStatusRecovering,
		model.GraphRunStatusAwaitingInput:
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
