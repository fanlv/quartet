package handler

import (
	"context"
	"errors"
	"fmt"
	"io"
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
	"github.com/fanlv/quartet/services/job"
	"github.com/fanlv/quartet/services/session"
	"github.com/fanlv/quartet/types/model"
	"github.com/google/uuid"
)

// isClientDisconnectErr identifies errors that indicate the SSE client has
// closed the connection (tab closed, navigation, network drop). Those are
// a normal end-of-stream, not a server fault — they should log at Debug,
// not Error, so genuine write failures keep their signal-to-noise ratio.
func isClientDisconnectErr(err error) bool {
	if err == nil {
		return false
	}
	if errors.Is(err, io.EOF) || errors.Is(err, context.Canceled) || errors.Is(err, io.ErrClosedPipe) {
		return true
	}
	msg := err.Error()
	for _, s := range []string{
		"connection has been closed",
		"broken pipe",
		"connection reset by peer",
		"use of closed network connection",
		"client disconnected",
	} {
		if strings.Contains(msg, s) {
			return true
		}
	}
	return false
}

// sseKeepAliveInterval is how often a keep-alive comment is sent on idle SSE
// connections to prevent proxy/CDN timeout. 10s is chosen to stay well under
// common reverse-proxy idle timeouts (nginx default 60s, some CDNs as low as
// 15s), preventing unnecessary client reconnections.
const sseKeepAliveInterval = 10 * time.Second

// sseTerminalIdleTimeout is the maximum duration an SSE connection may remain
// idle (no new events) after the job enters a terminal state (completed /
// stopped / failed). After this window, the server proactively closes the
// connection to reclaim goroutine + fd resources. The client can reconnect
// if a new run starts (via SendMessage).
const sseTerminalIdleTimeout = 5 * time.Minute

// jobErrMappings maps job service sentinel errors to HTTP status codes.
var jobErrMappings = []httputil.ErrorMapping{
	{Err: job.ErrJobNotFound, Status: http.StatusNotFound},
	{Err: job.ErrJobDeleted, Status: http.StatusNotFound},
	{Err: job.ErrJobRunning, Status: http.StatusConflict},
	{Err: job.ErrJobNotRunning, Status: http.StatusConflict},
	{Err: job.ErrJobNotRunnable, Status: http.StatusBadRequest},
	{Err: job.ErrEmptyMessage, Status: http.StatusBadRequest},
}

func (h *Handler) JobStop(ctx context.Context, c *app.RequestContext) {
	jobID := c.Param("jobId")
	if jobID == "" {
		httputil.BadRequest(c, "jobId is required")
		return
	}

	j, ok := h.jobService.Get(jobID)
	if !ok {
		httputil.NotFound(c, "job not found")
		return
	}

	if j.Status != model.JobStatusRunning {
		c.JSON(http.StatusOK, map[string]any{"code": 0, "status": "stopped"})
		return
	}

	// Graph jobs are driven by the graph scheduler, whose control handle lives
	// in graphService keyed by GraphRunID — not in jobService's cancel table.
	// A plain jobService.Stop would no-op (the job ID was never registered as a
	// cancelable loop), so route hard-stop to the graph service instead.
	// If the scheduler has already exited (ErrGraphRunNotRunning), fall through
	// to the normal stopAndWait path — the job may have an interactive run active.
	if j.Mode == model.JobModeGraph && j.GraphRunID != "" {
		if err := h.graphService.RegisterRunLocation(ctx, j.GraphRunID, j.WorkspaceID, j.ID); err != nil {
			logger.Errorf(ctx, "[job] register graph run location failed: jobId=%s graphRunId=%s err=%v", jobID, j.GraphRunID, err)
			httputil.InternalError(c, err.Error())
			return
		}
		if _, err := h.graphService.StopRun(ctx, j.GraphRunID, "hard stopped by user"); err != nil {
			logger.Errorf(ctx, "[job] stop graph run failed: jobId=%s graphRunId=%s err=%v", jobID, j.GraphRunID, err)
			if !errors.Is(err, graphsvc.ErrGraphRunNotRunning) {
				c.JSON(http.StatusOK, map[string]any{"code": 0, "status": "stopped"})
				return
			}
			// Graph scheduler gone — fall through to stopAndWait for the interactive run.
		} else {
			c.JSON(http.StatusOK, map[string]any{"code": 0, "status": "stopped"})
			return
		}
	}

	if err := h.stopAndWait(ctx, j); err != nil {
		logger.Errorf(ctx, "[job] stop failed: jobId=%s err=%v", jobID, err)
	}
	c.JSON(http.StatusOK, map[string]any{"code": 0, "status": "stopped"})
}

func (h *Handler) stopAndWait(ctx context.Context, job *model.Job) error {
	h.jobService.StopAndWait(job.ID)
	// Flush any pending rounds so messages.jsonl reflects the final state at
	// the moment of Stop. Messages are intentionally NOT truncated here —
	// Continue re-runs the interrupted step with the full history in place,
	// relying on BeginRun's orphan-tail cleanup to keep tool_call/tool_result
	// pairs consistent.
	return h.cancelJobSessions(ctx, job)
}

func (h *Handler) cancelJobSessions(ctx context.Context, job *model.Job) error {
	// Re-fetch the job after Stop so we get the final list of session IDs,
	// including any that the run may have appended between the caller's
	// Get and the Stop call above.
	if updated, ok := h.jobService.Get(job.ID); ok {
		job = updated
	}

	if len(job.SessionIDs) == 0 {
		return nil
	}

	// The session service may be missing (idle eviction or never loaded
	// because the job has been quiet since startup). Look it up best-effort:
	// even with a nil sessionSVC we can still flush the in-memory ACP agent
	// leases that are keyed by (wsId, jobId, sessionId), so the cancel path
	// is still useful.
	h.sessionMu.RLock()
	entry, ok := h.sessionServices[job.ID]
	h.sessionMu.RUnlock()
	var sessionSVC session.Service
	if ok {
		sessionSVC = entry.svc
	}

	logger.Debugf(ctx, "[job] stop sessions: jobId=%s sessions=%d sessionSvcLoaded=%v", job.ID, len(job.SessionIDs), sessionSVC != nil)
	for _, sid := range job.SessionIDs {
		// ACP agent lease is keyed independently of the session service —
		// look it up directly so an evicted/unloaded session service does
		// not cause the lease to leak.
		if lease, ok := h.acpAgentService.Get(job.WorkspaceID, job.ID, sid); ok {
			logger.Debugf(ctx, "[job] stop+flush ACP: jobId=%s acpSession=%s", job.ID, lease.Value.SessionID())
			lease.Value.StopAndFlush()
			lease.Release()
		}
	}

	return nil
}

func (h *Handler) JobEvents(ctx context.Context, c *app.RequestContext) {
	jobID := c.Param("jobId")
	if jobID == "" {
		httputil.BadRequest(c, "jobId is required")
		return
	}

	// SSE responses — including 410 Gone fast paths — must never be cached.
	// 410 Gone is cacheable by default per RFC 7231, so without this the
	// browser pins the first 410 body (with its specific seq=N) and serves
	// it to every retry from disk cache, defeating the snapshot+resubscribe
	// recovery: the client thinks the server keeps rejecting fresh seqs but
	// actually the request never leaves the browser.
	c.Header("Cache-Control", "no-store")
	c.Header("X-Accel-Buffering", "no")

	jobObj, ok := h.jobService.Get(jobID)
	if !ok {
		httputil.NotFound(c, "job not found")
		return
	}
	// connectStatus is the job's status at SSE connect time. It only feeds
	// the two connect-time logs below ([TRACE-SEQ0] and the subscribe
	// Debug). DO NOT reuse it in any log emitted later in this handler —
	// SSE connections can live tens of minutes, during which the job moves
	// pending → running → completed, and a stale snapshot produces
	// misleading WARN/ERROR lines (we have seen "jobStatus=pending" on a
	// timeout WARN fired 49 minutes into a long-running job). The
	// post-connect log paths call currentJobStatus() below instead.
	connectStatus := jobObj.Status
	currentJobStatus := func() model.JobStatus {
		if j, ok := h.jobService.Get(jobID); ok {
			return j.Status
		}
		// Job was deleted mid-stream — fall back to the connect snapshot
		// so the log line still carries a value; the missing-job condition
		// itself is observable via other paths (Subscribe will close, the
		// loop exits).
		return connectStatus
	}

	// connID is a short per-connection tag stamped on every log line this
	// handler emits. A single job can hold several concurrent SSE
	// connections (reconnect storms after a write timeout, multiple tabs,
	// HMR re-subscribes), and jobId alone cannot tell them apart. With
	// connID you can join one connection's full lifecycle — subscribe →
	// write timeout → unsubscribe → the next subscribe (resume) — and
	// answer the only question that matters for a write-timeout teardown:
	// did the client come back and resume from Last-Event-ID, or did the
	// stream actually break?
	connID := uuid.NewString()[:8]

	// Resume point: Last-Event-ID request header. Falsy values (missing,
	// empty, "0", non-numeric) parse to 0; Subscribe(0) succeeds only on a
	// fresh buffer (headSeq=0) and returns ErrSeqGone — which we map to 410
	// — once any GC has happened. Clients are expected to seed Last-Event-
	// ID with the snapshot endpoint's lastEventSeq on first connect after a
	// page load; sending 0 is the new-buffer fast path, not a "tail"
	// fallback for an established stream.
	rawLEI := string(c.GetHeader("Last-Event-ID"))
	startSeq := parseLastEventID(rawLEI)

	// [TRACE-SEQ0] capture which client / page sent us this request so we
	// can correlate startSeq=0 occurrences to a specific frontend path.
	// connectStatus is included so reconnect storms against an already-
	// terminal job (e.g. after a buffer-reset or idle-connection timeout)
	// are visible at a glance — those connections never receive new events
	// and are torn down only when a keep-alive write times out, so the
	// count + status correlate 1:1 with the "[sse] keep-alive timed out"
	// WARNs. Post-connect log
	// lines refresh via currentJobStatus() because the job's state moves
	// during the connection's lifetime.
	referer := string(c.GetHeader("Referer"))
	userAgent := string(c.GetHeader("User-Agent"))
	// Downgrade to DEBUG for terminal jobs to reduce log noise from
	// repeated subscribes (e.g. HMR or tab re-focus on completed jobs).
	if connectStatus == model.JobStatusCompleted || connectStatus == model.JobStatusFailed || connectStatus == model.JobStatusStopped {
		logger.Debugf(ctx, "[sse] subscribe req (terminal): connId=%s jobId=%s jobStatus=%s rawLEI=%q startSeq=%d referer=%q ua=%q", connID, jobID, connectStatus, rawLEI, startSeq, referer, userAgent)
	} else if startSeq == 0 {
		logger.Infof(ctx, "[sse][TRACE-SEQ0] subscribe req: connId=%s jobId=%s jobStatus=%s rawLEI=%q startSeq=%d referer=%q ua=%q", connID, jobID, connectStatus, rawLEI, startSeq, referer, userAgent)
	} else {
		logger.Infof(ctx, "[sse] subscribe req (resume): connId=%s jobId=%s jobStatus=%s rawLEI=%q startSeq=%d referer=%q ua=%q", connID, jobID, connectStatus, rawLEI, startSeq, referer, userAgent)
	}

	// Retry once on ErrSeqGone when startSeq>0: between the client's
	// snapshot fetch and this subscribe call, buffer GC may advance headSeq
	// past the client's Last-Event-ID. A single retry with the server's
	// current SnapshotSeq resolves it — same strategy as im_gateway.go.
	var reader *job.Reader
	subscribeSeq := startSeq
	subscribeRetried := false
	for attempt := 0; attempt < 2; attempt++ {
		var err error
		reader, err = h.jobService.Subscribe(jobID, subscribeSeq)
		if err == nil {
			break
		}
		if errors.Is(err, job.ErrSeqGone) && attempt == 0 && startSeq > 0 {
			freshSeq := h.jobService.SnapshotSeq(jobID)
			logger.Infof(ctx, "[sse] subscribe race hit, retrying: connId=%s jobId=%s originalSeq=%d freshSeq=%d", connID, jobID, startSeq, freshSeq)
			subscribeSeq = freshSeq
			subscribeRetried = true
			continue
		}
		if errors.Is(err, job.ErrSeqGone) {
			// Pull the buffer's current bounds so this single line is
			// self-contained for triage — no need to cross-reference the
			// buffer's own "subscribe rejected" WARN. gap = how far the
			// client's resume point fell behind headSeq: a gap of a few
			// events is a snapshot/subscribe race (the retry above should
			// normally absorb it; retried=true here means it ran and still
			// failed), while a gap of thousands means the client was
			// disconnected long enough to fall off the replay window — 410 +
			// snapshot reload is then the correct, expected outcome, not a bug.
			stats := h.jobService.BufferStats(jobID)
			var gap uint64
			if stats.HeadSeq > subscribeSeq {
				gap = stats.HeadSeq - subscribeSeq
			}
			cause := "buffer GC overtook client or replay gap too large"
			if startSeq == 0 {
				cause = "client did not seed Last-Event-ID — likely missed snapshot pre-fetch"
			}
			logger.Infof(ctx, "[sse] subscribe gone: connId=%s jobId=%s startSeq=%d subscribeSeq=%d headSeq=%d nextSeq=%d gap=%d retried=%t cause=%q (client should reload snapshot)", connID, jobID, startSeq, subscribeSeq, stats.HeadSeq, stats.NextSeq, gap, subscribeRetried, cause)
			c.AbortWithStatusJSON(http.StatusGone, map[string]string{
				"error": fmt.Sprintf("event buffer no longer contains seq=%d for job %s; reload snapshot", subscribeSeq, jobID),
			})
			return
		}
		logger.Errorf(ctx, "[sse] subscribe failed: connId=%s jobId=%s startSeq=%d err=%v", connID, jobID, subscribeSeq, err)
		httputil.InternalError(c, fmt.Sprintf("subscribe failed: %v", err))
		return
	}
	if subscribeRetried {
		logger.Infof(ctx, "[sse] subscribe retry succeeded: connId=%s jobId=%s originalSeq=%d resolvedSeq=%d", connID, jobID, startSeq, subscribeSeq)
	}
	connectedAt := time.Now()
	defer func() {
		reader.Close()
		// Connection-lifetime log: makes the "long idle SSE for a
		// completed job" pattern obvious — every WARN keep-alive timeout
		// is preceded by this at the corresponding subscribe. lifetime
		// + jobStatus together tell you whether the connection was
		// short-lived (network blip on a running job) or a long idle
		// connection on a terminal job that only exits via ReadClosed
		// or a write failure. Refresh the status here — connectStatus
		// would lie for any connection that lasted long enough to
		// outlive a job transition.
		logger.Debugf(ctx, "[sse] unsubscribe: connId=%s jobId=%s jobStatus=%s lifetime=%s", connID, jobID, currentJobStatus(), time.Since(connectedAt).Round(time.Millisecond))
	}()

	c.SetStatusCode(hertzConsts.StatusOK)
	w := sse.NewWriter(c)

	if err := w.WriteKeepAlive(); err != nil {
		// Client disconnected before we could send anything — common for users
		// who navigate away fast. Log at Debug to keep the error channel clean,
		// but surface real errors (rare here) at Error.
		if isClientDisconnectErr(err) {
			logger.Debugf(ctx, "[sse] initial keep-alive failed (client disconnected): connId=%s jobId=%s err=%v", connID, jobID, err)
		} else {
			logger.Errorf(ctx, "[sse] initial keep-alive failed: connId=%s jobId=%s err=%v", connID, jobID, err)
		}
		return
	}

	// Single-writer loop: events and keep-alives are written from the
	// same goroutine. ReadWithTimeout returns ReadTimeout when no event
	// arrives within sseKeepAliveInterval, at which point we write a
	// keep-alive comment and re-read. Hertz's sse.Writer has an internal
	// mutex so concurrent calls won't race, but a write-timeout still
	// leaves the previous write goroutine alive (TCP write is not
	// cancellable). If a second writer ran against the same Writer it
	// could deliver the same event twice once the original write finally
	// drained — so on any write timeout we tear the connection down and
	// let the client resume via Last-Event-ID, instead of retrying.
	//
	// The loop does NOT exit on terminal events (JOB_COMPLETED / STOPPED
	// / FAILED). SendMessage reuses the same buffer, so new
	// events for the next run land on this reader without a reconnect.
	// The connection only exits on:
	//   - ReadClosed: buffer was closed (Delete)
	//   - write failure: client disconnected or TCP wedged
	//   - terminal idle timeout: no new events for sseTerminalIdleTimeout
	//     after job enters terminal state
	var terminalIdleSince time.Time // zero until job becomes terminal and idle
	for {
		entries, status := reader.ReadWithTimeout(ctx, sseKeepAliveInterval, sseReadBatchSize)
		switch status {
		case job.ReadClosed:
			return
		case job.ReadTimeout:
			// Check terminal idle timeout: if the job is in a terminal state
			// and no events have arrived for sseTerminalIdleTimeout, close the
			// connection to free resources.
			js := currentJobStatus()
			if js == model.JobStatusCompleted || js == model.JobStatusFailed || js == model.JobStatusStopped {
				if terminalIdleSince.IsZero() {
					terminalIdleSince = time.Now()
				} else if time.Since(terminalIdleSince) >= sseTerminalIdleTimeout {
					logger.Debugf(ctx, "[sse] closing idle terminal connection: connId=%s jobId=%s jobStatus=%s idleSince=%s lifetime=%s",
						connID, jobID, js, terminalIdleSince.Format(time.RFC3339), time.Since(connectedAt).Round(time.Millisecond))
					return
				}
			} else {
				terminalIdleSince = time.Time{} // reset if job is no longer terminal (e.g. restarted)
			}

			if err := writeWithTimeout(ctx, c, connID, jobID, 0, func() error { return w.WriteKeepAlive() }, sseWriteTimeout); err != nil {
				switch {
				case errors.Is(err, errSSEWriteTimeout):
					logger.Warnf(ctx, "[sse] keep-alive timed out (connection stuck, tearing down): connId=%s jobId=%s jobStatus=%s timeout=%s lifetime=%s", connID, jobID, currentJobStatus(), sseWriteTimeout, time.Since(connectedAt).Round(time.Millisecond))
				case isClientDisconnectErr(err):
					logger.Debugf(ctx, "[sse] keep-alive failed (client disconnected): connId=%s jobId=%s jobStatus=%s lifetime=%s err=%v", connID, jobID, currentJobStatus(), time.Since(connectedAt).Round(time.Millisecond), err)
				default:
					logger.Errorf(ctx, "[sse] keep-alive failed: connId=%s jobId=%s jobStatus=%s lifetime=%s err=%v", connID, jobID, currentJobStatus(), time.Since(connectedAt).Round(time.Millisecond), err)
				}
				return
			}
			continue
		}

		// Events arrived — reset terminal idle tracker since the buffer is active.
		terminalIdleSince = time.Time{}

		for _, entry := range entries {
			data, err := sonic.Marshal(entry.Event)
			if err != nil {
				logger.Errorf(ctx, "[sse] marshal event failed: connId=%s jobId=%s seq=%d err=%v", connID, jobID, entry.Seq, err)
				continue
			}
			id := ""
			if entry.Seq > 0 {
				id = strconv.FormatUint(entry.Seq, 10)
			}
			if err := writeWithTimeout(ctx, c, connID, jobID, entry.Seq, func() error { return w.WriteEvent(id, "message", data) }, sseWriteTimeout); err != nil {
				switch {
				case errors.Is(err, errSSEWriteTimeout):
					logger.Warnf(ctx, "[sse] write event timed out (connection stuck, tearing down): connId=%s jobId=%s jobStatus=%s seq=%d timeout=%s lifetime=%s", connID, jobID, currentJobStatus(), entry.Seq, sseWriteTimeout, time.Since(connectedAt).Round(time.Millisecond))
				case isClientDisconnectErr(err):
					logger.Debugf(ctx, "[sse] client disconnected mid-stream: connId=%s jobId=%s jobStatus=%s seq=%d lifetime=%s err=%v", connID, jobID, currentJobStatus(), entry.Seq, time.Since(connectedAt).Round(time.Millisecond), err)
				default:
					logger.Errorf(ctx, "[sse] write event failed: connId=%s jobId=%s jobStatus=%s seq=%d lifetime=%s err=%v", connID, jobID, currentJobStatus(), entry.Seq, time.Since(connectedAt).Round(time.Millisecond), err)
				}
				return
			}
			if entry.Seq > 0 {
				reader.Ack(entry.Seq)
			}
		}
	}
}

// parseLastEventID accepts the Last-Event-ID header value and returns the
// resume seq. Non-numeric / empty / "0" all collapse to 0. NOTE: 0 is NOT
// a generic "start at tail" sentinel — Subscribe(0) only succeeds on a
// fresh buffer (headSeq=0) and returns ErrSeqGone (mapped to 410) once
// any GC has happened. Clients should seed Last-Event-ID with the
// snapshot endpoint's lastEventSeq before opening /events; the
// fall-through to 0 only stays valid as a fast path for a brand-new job.
func parseLastEventID(s string) uint64 {
	if s == "" {
		return 0
	}
	v, err := strconv.ParseUint(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0
	}
	return v
}

// sseWriteTimeout bounds how long a single SSE write (event or keep-alive)
// may block before we give up on the connection. 30s is short enough to
// surface a stuck write within one heartbeat cycle and long enough to
// tolerate routine network blips on slow mobile uplinks.
const sseWriteTimeout = 30 * time.Second

// sseReadBatchSize is the maximum number of events one Read call returns.
// Bigger batches amortise lock acquisitions on busy producers; small
// enough that a single overloaded subscriber can't starve the rest by
// holding the reader for too long per write batch.
const sseReadBatchSize = 32

// writeWithTimeout runs fn in a separate goroutine and returns either fn's
// error or errSSEWriteTimeout if it didn't finish within timeout. TCP writes
// are not cancellable from userspace, so when a write blocks for longer than
// timeout we cannot just abandon the goroutine and return: the goroutine is
// still inside hertz' sse.Writer → chunkedBodyWriter → netpoll
// connection.Flush(), where the netpoll layer holds a non-blocking `flushing`
// CAS lock for the entire duration of the syscall. Once the SSE handler
// returns, hertz' Serve loop calls writeResponse → HijackWriter.Finalize() →
// Flush() on the SAME connection; that second flush also tries to acquire
// the CAS lock, fails, and surfaces as
//
//	"ERROR HERTZ: Error=concurrent connection access when flush"
//
// in the access log. The leaked goroutine is also wedged forever — netpoll
// will not unblock it on its own.
//
// We break the deadlock by closing the underlying TCP connection on timeout.
// Closing kicks netpoll's epoll loop, which fails the in-flight write with a
// clean network error, releases the CAS lock, and lets the leaked goroutine
// exit. We then wait briefly for it to drain so hertz' subsequent Finalize
// runs against a fully idle connection (it will still fail because the
// socket is gone, but with a benign ErrConnClosed instead of the misleading
// ErrConcurrentAccess).
//
// `seq` is 0 for keep-alive writes (which have no event id) and the event's
// resume seq for data writes; both feed the diagnostic log only. `connID`
// is the per-connection tag so these low-level close/drain lines join the
// teardown WARN emitted by the caller.
func writeWithTimeout(ctx context.Context, c *app.RequestContext, connID, jobID string, seq uint64, fn func() error, timeout time.Duration) error {
	done := make(chan error, 1)
	go func() { done <- fn() }()
	t := time.NewTimer(timeout)
	defer t.Stop()
	select {
	case err := <-done:
		return err
	case <-t.C:
		// Connection is presumed wedged — close the underlying socket to
		// unblock netpoll. The outer caller (keep-alive / write-event
		// path in JobEvents) already logs a single Warn for the teardown
		// with full business context (jobStatus, lifetime, seq), so this
		// inner step only needs to surface the close-syscall outcome at
		// Debug — closeErr is almost always nil and is a low-level
		// detail that's only useful when the netpoll teardown itself
		// goes sideways.
		conn := c.GetConn()
		if conn != nil {
			closeErr := conn.Close()
			logger.Debugf(ctx, "[sse] write timeout, closed underlying conn to unblock netpoll: connId=%s jobId=%s seq=%d timeout=%s closeErr=%v", connID, jobID, seq, timeout, closeErr)
		} else {
			logger.Warnf(ctx, "[sse] write timeout but conn was nil — cannot force-close: connId=%s jobId=%s seq=%d timeout=%s", connID, jobID, seq, timeout)
		}
		// Drain the leaked goroutine so it releases netpoll's flushing
		// CAS lock before hertz runs Finalize on the hijacked writer.
		// 2s is comfortably longer than the time it takes netpoll to
		// fail an in-flight write after Close on a healthy host;
		// anything past that means something deeper is stuck and the
		// best we can do is log and move on.
		select {
		case err := <-done:
			logger.Debugf(ctx, "[sse] leaked write goroutine drained after conn close: connId=%s jobId=%s seq=%d err=%v", connID, jobID, seq, err)
		case <-time.After(sseWriteDrainTimeout):
			// The goroutine is still wedged. We've already closed the
			// conn, so further damage is bounded — log it loudly so
			// the operator can investigate (kernel-level socket buffer
			// stuck, broken peer, netpoll bug, etc.).
			logger.Errorf(ctx, "[sse] leaked write goroutine did NOT drain within %s after conn close — netpoll may still hold the flushing lock and the next Finalize may log ErrConcurrentAccess: connId=%s jobId=%s seq=%d", sseWriteDrainTimeout, connID, jobID, seq)
		}
		return errSSEWriteTimeout
	}
}

// sseWriteDrainTimeout bounds how long we wait for a leaked write goroutine
// to exit after we close the connection on it. See writeWithTimeout for the
// full reasoning; in healthy conditions the drain happens within tens of
// milliseconds (netpoll just needs one epoll wakeup).
const sseWriteDrainTimeout = 2 * time.Second

var errSSEWriteTimeout = errors.New("sse: write timed out")
