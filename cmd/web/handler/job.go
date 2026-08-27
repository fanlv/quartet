package handler

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/cloudwego/hertz/pkg/app"
	"github.com/fanlv/quartet/pkg/fileserver"
	fsmodel "github.com/fanlv/quartet/pkg/fileserver/model"
	"github.com/fanlv/quartet/pkg/httputil"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/services/auth"
	jobsvc "github.com/fanlv/quartet/services/job"
	"github.com/fanlv/quartet/types/model"
)

func (h *Handler) JobCreate(ctx context.Context, c *app.RequestContext) {
	var req model.CreateJobRequest
	if err := c.BindJSON(&req); err != nil {
		logger.Warnf(ctx, "[job] create: parse request failed: %v", err)
		httputil.BadRequest(c, "Invalid request body")
		return
	}

	job, duplicate, err := h.createJobIdempotent(ctx, &req)
	if err != nil {
		logger.Errorf(ctx, "[job] create failed: type=%s err=%v", req.AgentType, err)
		if errors.Is(err, jobsvc.ErrClientMessageIDConflict) {
			httputil.Conflict(c, err.Error())
		} else if errors.Is(err, errJobPersistFailed) {
			httputil.InternalError(c, err.Error())
		} else {
			httputil.BadRequest(c, err.Error())
		}
		return
	}

	logger.Debugf(ctx, "[job] created: jobId=%s type=%s", job.ID, req.AgentType)

	c.JSON(http.StatusOK, model.CreateJobResponse{
		JobID:     job.ID,
		CreatedAt: job.CreatedAt.UnixMilli(),
		Status:    map[bool]string{false: "created", true: "duplicate"}[duplicate],
	})
}

func (h *Handler) createJobIdempotent(ctx context.Context, req *model.CreateJobRequest) (*model.Job, bool, error) {
	job, err := h.buildJob(ctx, req)
	if err != nil {
		return nil, false, err
	}
	if req.ClientMessageID == "" {
		if err := h.persistCreatedJob(ctx, job, req); err != nil {
			return nil, false, err
		}
		return job, false, nil
	}
	payload := struct {
		ModelID         string        `json:"modelId"`
		AgentType       string        `json:"agentType"`
		ACPMode         string        `json:"acpMode"`
		ACPThoughtLevel string        `json:"acpThoughtLevel"`
		Mode            model.JobMode `json:"mode"`
		Workdir         string        `json:"workdir"`
		WorkspaceID     string        `json:"workspaceId"`
	}{req.ModelID, req.AgentType, req.ACPMode, req.ACPThoughtLevel, req.Mode, req.Workdir, req.WorkspaceID}
	data, err := json.Marshal(payload)
	if err != nil {
		return nil, false, fmt.Errorf("hash create job payload: %w", err)
	}
	sum := sha256.Sum256(data)
	job.ID = jobsvc.IdempotentJobID(req.ClientMessageID)
	job.CreationClientMessageID = req.ClientMessageID
	job.CreationPayloadHash = hex.EncodeToString(sum[:])
	created, duplicate, err := h.jobService.CreateIdempotent(job)
	if err != nil {
		if errors.Is(err, jobsvc.ErrClientMessageIDConflict) {
			return nil, false, err
		}
		return nil, false, fmt.Errorf("%w: %v", errJobPersistFailed, err)
	}
	if !duplicate && req.Workdir != "" && h.recentDirsRepo != nil {
		if err := h.recentDirsRepo.Add(ctx, req.Workdir); err != nil {
			logger.Warnf(ctx, "[job] save recent dir failed: dir=%s err=%v", req.Workdir, err)
		}
	}
	return created, duplicate, nil
}

// errJobPersistFailed wraps errors from persisting a newly created job so
// the HTTP handler can map them to 500 while validation errors map to 400.
var errJobPersistFailed = errors.New("failed to persist job")

// createJob validates a CreateJobRequest and persists a new Job. It also
// triggers async title refinement and updates recent-dirs tracking. This is
// shared between the HTTP JobCreate handler and the IM gateway so both flows
// produce identically configured jobs.
func (h *Handler) createJob(ctx context.Context, req *model.CreateJobRequest) (*model.Job, error) {
	job, err := h.buildJob(ctx, req)
	if err != nil {
		return nil, err
	}
	if err := h.persistCreatedJob(ctx, job, req); err != nil {
		return nil, err
	}
	return job, nil
}

func (h *Handler) buildJob(ctx context.Context, req *model.CreateJobRequest) (*model.Job, error) {
	if req.AgentType == "" {
		return nil, fmt.Errorf("agentType is required")
	}

	if req.Mode == "" {
		req.Mode = model.JobModeInteractive
	}
	if req.Mode != model.JobModeInteractive {
		return nil, fmt.Errorf("mode must be interactive")
	}

	if req.WorkspaceID == "" {
		return nil, fmt.Errorf("workspaceId is required")
	}
	ws, ok := h.workspaceService.Get(req.WorkspaceID)
	if !ok {
		return nil, fmt.Errorf("workspace not found")
	}

	// Front-end can momentarily send an empty / stale workdir while its
	// workspace metadata is still being hydrated (stub workspace created from
	// URL before /workspace/list returns). Fall back to the workspace's own
	// workdir so the Job always lands in the right directory.
	if req.Workdir == "" {
		req.Workdir = ws.Workdir
	}

	if err := validateWorkdir(req.Workdir); err != nil {
		return nil, err
	}

	// Even though the DirPicker UI clamps selection to the workspace
	// workdir, the server must re-verify: scripted requests or stale clients
	// can otherwise pin a Job to workspace A while running it in workspace
	// B's directory.
	if err := ensureWorkdirWithinWorkspace(req.Workdir, ws.Workdir); err != nil {
		return nil, err
	}

	job := model.NewJob(req.Workdir, req.WorkspaceID)
	job.Mode = req.Mode
	job.InitialAgentID = req.AgentType
	job.FirstModelID = req.ModelID
	job.InitialACPMode = req.ACPMode
	job.InitialACPThoughtLevel = req.ACPThoughtLevel

	return job, nil
}

func (h *Handler) persistCreatedJob(ctx context.Context, job *model.Job, req *model.CreateJobRequest) error {
	if err := h.jobService.Create(job); err != nil {
		return fmt.Errorf("%w: %v", errJobPersistFailed, err)
	}
	if req.Workdir != "" && h.recentDirsRepo != nil {
		if err := h.recentDirsRepo.Add(ctx, req.Workdir); err != nil {
			logger.Warnf(ctx, "[job] save recent dir failed: dir=%s err=%v", req.Workdir, err)
		}
	}
	return nil
}

func (h *Handler) JobList(ctx context.Context, c *app.RequestContext) {
	wsID := string(c.Query("workspaceId"))
	cursor := string(c.Query("cursor"))
	const (
		defaultLimit = 50
		// maxLimit bounds how many jobs a single page can ask for so a
		// client can't force the handler to materialise half a million
		// summaries per request. 500 is well above any real UI page size.
		maxLimit = 500
	)
	limit := defaultLimit
	if s := string(c.Query("limit")); s != "" {
		if n, err := strconv.Atoi(s); err == nil && n > 0 {
			if n > maxLimit {
				n = maxLimit
			}
			limit = n
		}
	}
	excludeScheduled := string(c.Query("excludeScheduled")) == "true"

	// Build a weak ETag from the workspace-scoped job-list version only.
	// Cursor and excludeScheduled keep different pages / filters from sharing
	// one. Daily usage stats are intentionally not folded in, so stats writes do
	// not cause high-frequency list cache invalidation.
	jobsVersion := h.jobService.WorkspaceListVersion(wsID)
	etag := fmt.Sprintf(`W/"%s:%d:%s:%d:%t"`, wsID, jobsVersion, cursor, limit, excludeScheduled)
	c.Header("Cache-Control", "no-cache, must-revalidate")
	c.Header("ETag", etag)
	if ifNoneMatch := string(c.GetHeader("If-None-Match")); ifNoneMatch == etag {
		c.Status(http.StatusNotModified)
		return
	}

	summaries, nextCursor, hasMore, _ := h.jobService.ListByWorkspacePaged(wsID, cursor, limit, excludeScheduled)
	principal, _ := CurrentPrincipal(c)
	if !h.authService.HasPermission(principal, auth.PermissionJobShare) {
		for i := range summaries {
			summaries[i].ShareToken = ""
		}
	}

	dailyStats := h.collectDailyStats(ctx, wsID, summaries)

	c.JSON(http.StatusOK, model.ListJobsResponse{
		Jobs:       summaries,
		NextCursor: nextCursor,
		HasMore:    hasMore,
		Version:    jobsVersion,
		DailyStats: dailyStats,
	})
}

// collectDailyStats builds the per-day totals for the page returned by
// JobList. We project the days the current page actually touches (via
// summary.UpdatedAt) so the response stays bounded by the page size, and
// look them up via the usage-stats reader.
//
// When wsID is empty (all-workspaces filter) the reader aggregates across
// every workspace for those days so the day-group header still renders a
// useful number instead of staying blank.
//
// Returns nil when no usage-stats service is wired or when the page has
// nothing to report — the frontend treats nil / empty identically.
func (h *Handler) collectDailyStats(ctx context.Context, wsID string, summaries []model.JobSummary) map[string]model.DailyStatsEntry {
	if h.usageStats == nil || len(summaries) == 0 {
		return nil
	}
	seen := make(map[string]struct{}, len(summaries))
	days := make([]time.Time, 0, len(summaries))
	for _, s := range summaries {
		if s.UpdatedAt <= 0 {
			continue
		}
		t := time.UnixMilli(s.UpdatedAt)
		key := t.Format("2006-01-02")
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		days = append(days, t)
	}
	if len(days) == 0 {
		return nil
	}
	raw, err := h.usageStats.GetDailyTotals(wsID, days)
	if err != nil {
		logger.Warnf(ctx, "[job] collect daily usage stats failed: %v", err)
		if len(raw) == 0 {
			return nil
		}
	}
	if len(raw) == 0 {
		return nil
	}
	out := make(map[string]model.DailyStatsEntry, len(raw))
	for k, v := range raw {
		out[k] = model.DailyStatsEntry{TotalMs: v.TotalMs, TurnCount: v.TurnCount}
	}
	return out
}

func (h *Handler) JobGet(ctx context.Context, c *app.RequestContext) {
	jobID := c.Param("jobId")
	if jobID == "" {
		httputil.BadRequest(c, "jobId is required")
		return
	}

	// The snapshot's lastEventSeq is the SSE resume point — a moving target
	// the client must re-fetch on every reconnect / 410 fallback. Caching
	// this response (even briefly) would feed stale seqs to the SSE
	// subscribe path and produce phantom 410s the server didn't actually
	// emit on the current request.
	c.Header("Cache-Control", "no-store")

	job, lastSeq, ok := h.jobService.GetWithSnapshotSeq(jobID)
	if !ok {
		httputil.NotFound(c, "job not found")
		return
	}
	if _, isPublic := getPublicJob(c); isPublic {
		refs := h.collectJobAgentRefs(ctx, job)
		resp := model.PublicJobResponse{
			ID:                  job.ID,
			Title:               job.Title,
			Mode:                job.Mode,
			Status:              job.Status,
			StartedAt:           job.StartedAt,
			FinishedAt:          job.FinishedAt,
			TotalTurnDurationMs: job.TotalTurnDurationMs,
			GraphRunID:          job.GraphRunID,
			SessionIDs:          append([]string(nil), job.SessionIDs...),
			LastRunOutcome:      job.LastRunOutcome,
			LastEventSeq:        lastSeq,
			ServerTime:          time.Now().UnixMilli(),
			Agents:              h.resolvePublicAgents(ctx, refs, job.ID),
		}
		if job.Progress != nil && job.Progress.LastError != "" {
			resp.Progress = &model.PublicJobProgress{LastError: job.Progress.LastError}
		}
		if job.ShareShowWorkspaceName && h.workspaceService != nil {
			if ws, found := h.workspaceService.Get(job.WorkspaceID); found && ws != nil {
				resp.ShareContext.WorkspaceName = ws.Title
			}
		}
		c.JSON(http.StatusOK, resp)
		return
	}
	principal, _ := CurrentPrincipal(c)
	if !h.authService.HasPermission(principal, auth.PermissionJobShare) {
		job.ShareToken = ""
	}

	// Marshal as a flat envelope so the existing client shape (model.Job
	// fields at the root) continues to work while the new lastEventSeq
	// piggybacks alongside it. The client uses lastEventSeq as the
	// initial Last-Event-ID for the SSE subscription so resume after
	// page refresh / reconnect lands at the right point in the buffer.
	envelope := jobGetEnvelope{
		Job:          job,
		LastEventSeq: lastSeq,
		ServerTime:   time.Now().UnixMilli(),
	}
	c.JSON(http.StatusOK, envelope)
}

// jobGetEnvelope wraps the job snapshot with the SSE resume sequence the
// client should hand back on its first / next subscribe. Embedding *Job
// keeps the JSON shape backwards-compatible (job fields stay at the
// root) while adding lastEventSeq alongside.
type jobGetEnvelope struct {
	*model.Job
	LastEventSeq uint64 `json:"lastEventSeq"`
	ServerTime   int64  `json:"serverTime"`
}

func (h *Handler) collectJobAgentRefs(ctx context.Context, job *model.Job) []string {
	if job == nil {
		return nil
	}
	var refs []string
	seen := make(map[string]bool)
	if job.InitialAgentID != "" {
		seen[job.InitialAgentID] = true
		refs = append(refs, job.InitialAgentID)
	}
	for _, sessionID := range jobAllSessionIDs(job) {
		session, ok := h.lookupSession(sessionID)
		if !ok {
			session, ok = h.reloadSessionByID(sessionID)
		}
		if !ok || session == nil {
			continue
		}
		ref := session.AgentID
		if ref == "" {
			ref = session.Type
		}
		if ref != "" && !seen[ref] {
			seen[ref] = true
			refs = append(refs, ref)
		}
	}
	if job.GraphRunID != "" {
		if status, err := h.graphService.GetRunStatus(ctx, job.GraphRunID); err == nil && status.Run != nil {
			addSnapshot := func(snapshot model.GraphAgentSnapshot) {
				ref := snapshot.AgentID
				if ref == "" {
					ref = snapshot.AgentType
				}
				if ref != "" && !seen[ref] {
					seen[ref] = true
					refs = append(refs, ref)
				}
			}
			for _, snapshot := range status.Run.BaseSnapshot.AgentSnapshots {
				addSnapshot(snapshot)
			}
			for _, version := range status.Run.Versions {
				for _, snapshot := range version.AgentSnapshots {
					addSnapshot(snapshot)
				}
			}
		}
	}
	return refs
}

func (h *Handler) JobDelete(ctx context.Context, c *app.RequestContext) {
	jobID := c.Param("jobId")
	if jobID == "" {
		httputil.BadRequest(c, "jobId is required")
		return
	}

	job, ok := h.jobService.Get(jobID)
	if !ok {
		httputil.NotFound(c, "job not found")
		return
	}

	// Mark as deleted first to prevent concurrent SendMessage
	// from launching new runs while we are cleaning up. MarkDeleted
	// runs under the service's internal lock so a concurrent Save() can't
	// overwrite the flag with a stale snapshot.
	if err := h.jobService.MarkDeleted(jobID); err != nil {
		logger.Errorf(ctx, "[job] delete: mark deleted failed: jobId=%s err=%v", jobID, err)
		httputil.InternalError(c, err.Error())
		return
	}

	if err := h.deleteMarkedJob(ctx, job); err != nil {
		logger.Errorf(ctx, "[job] delete failed: jobId=%s err=%v", jobID, err)
		httputil.InternalError(c, err.Error())
		return
	}
	c.JSON(http.StatusOK, map[string]any{"code": 0, "status": "ok"})
}

// validateWorkdir checks whether the given workdir exists and is a directory.
func validateWorkdir(workdir string) error {
	if workdir == "" {
		return nil
	}
	sb := fileserver.GetFileManager()
	stat, err := sb.FileStat(&fsmodel.FileStatRequest{Path: workdir})
	if err != nil {
		return fmt.Errorf("failed to check workdir: %w", err)
	}
	if !stat.Exists {
		return fmt.Errorf("workdir does not exist: %s", workdir)
	}
	if !stat.IsDir {
		return fmt.Errorf("workdir is not a directory: %s", workdir)
	}
	return nil
}

// ensureWorkdirWithinWorkspace rejects requests whose Workdir falls outside
// the workspace's own Workdir. Frontend DirPicker already clamps selection,
// but we re-verify server-side so stale/scripted requests cannot pin a Job
// to workspace A while running it inside workspace B's tree.
//
// An empty wsWorkdir means the workspace has no boundary configured (legacy
// or partially-provisioned); we fall back to the existence check only.
//
// Symlinks are resolved on both sides before the containment check, otherwise
// a symlink placed inside the workspace (by the agent itself or a prior job)
// could point at a sibling tree and still pass a lexical Rel() comparison.
func ensureWorkdirWithinWorkspace(workdir, wsWorkdir string) error {
	if workdir == "" || wsWorkdir == "" {
		return nil
	}
	realWs, err := resolveForContainment(wsWorkdir)
	if err != nil {
		return fmt.Errorf("resolve workspace dir %s: %w", wsWorkdir, err)
	}
	realWd, err := resolveForContainment(workdir)
	if err != nil {
		return fmt.Errorf("resolve workdir %s: %w", workdir, err)
	}
	if realWd == realWs {
		return nil
	}
	rel, err := filepath.Rel(realWs, realWd)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(os.PathSeparator)) || filepath.IsAbs(rel) {
		return fmt.Errorf("workdir %s is outside workspace directory %s", workdir, wsWorkdir)
	}
	return nil
}

// resolveForContainment returns the canonical absolute path with symlinks
// resolved. validateWorkdir already guarantees the path exists; we only
// reach here after that check has passed for both paths.
func resolveForContainment(p string) (string, error) {
	sb := fileserver.GetFileManager()
	res, err := sb.FileEvalSymlinks(&fsmodel.FileEvalSymlinksRequest{Path: filepath.Clean(p)})
	if err != nil {
		return "", err
	}
	return filepath.Clean(res.ResolvedPath), nil
}

func (h *Handler) JobShare(ctx context.Context, c *app.RequestContext) {
	jobID := c.Param("jobId")
	if jobID == "" {
		httputil.BadRequest(c, "jobId is required")
		return
	}

	var req model.ConfigureJobShareRequest
	if len(c.Request.Body()) > 0 {
		if err := c.BindJSON(&req); err != nil {
			httputil.BadRequest(c, "invalid request body")
			return
		}
	}
	showWorkspaceName := false
	if existing, found := h.jobService.Get(jobID); found && existing != nil && existing.ShareToken != "" {
		showWorkspaceName = existing.ShareShowWorkspaceName
	}
	if req.ShowWorkspaceName != nil {
		showWorkspaceName = *req.ShowWorkspaceName
	}

	// ConfigureShare reads the current token and — if empty — mints and
	// persists a new one under the job service's internal lock, so
	// concurrent requests can't each generate a different token and
	// overwrite each other's Save.
	token, err := h.jobService.ConfigureShare(jobID, showWorkspaceName, func() (string, error) {
		buf := make([]byte, 32)
		if _, err := rand.Read(buf); err != nil {
			return "", err
		}
		return hex.EncodeToString(buf), nil
	})
	if err != nil {
		if errors.Is(err, jobsvc.ErrJobNotFound) {
			httputil.NotFound(c, "job not found")
			return
		}
		logger.Errorf(ctx, "[job] share: configure failed: jobId=%s err=%v", jobID, err)
		httputil.InternalError(c, "failed to save share settings")
		return
	}

	c.JSON(http.StatusOK, model.ConfigureJobShareResponse{
		ShareToken: token, ShowWorkspaceName: showWorkspaceName,
	})
}

func (h *Handler) JobUnshare(ctx context.Context, c *app.RequestContext) {
	jobID := c.Param("jobId")
	if jobID == "" {
		httputil.BadRequest(c, "jobId is required")
		return
	}

	// ClearShareToken wipes the token atomically under the job service's
	// internal lock; a Get+Save pair would let a concurrent UpdateTitle/
	// Save overwrite our cleared token with a stale snapshot.
	if err := h.jobService.ClearShareToken(jobID); err != nil {
		if errors.Is(err, jobsvc.ErrJobNotFound) {
			httputil.NotFound(c, "job not found")
			return
		}
		logger.Errorf(ctx, "[job] unshare: clear token failed: jobId=%s err=%v", jobID, err)
		httputil.InternalError(c, "failed to remove share token")
		return
	}

	c.JSON(http.StatusOK, map[string]bool{"ok": true})
}

// jobTitleMaxLen bounds the manually-set title length. Same limit the
// frontend enforces; mirrored here so a hand-crafted request can't bypass
// the UI and persist a megabyte-long Title into Job meta.
const jobTitleMaxLen = 200

type updateJobTitleRequest struct {
	Title string `json:"title"`
}

type updateJobPinRequest struct {
	Pinned bool `json:"pinned"`
}

func (h *Handler) JobUpdateTitle(ctx context.Context, c *app.RequestContext) {
	jobID := c.Param("jobId")
	if jobID == "" {
		httputil.BadRequest(c, "jobId is required")
		return
	}

	var req updateJobTitleRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request body")
		return
	}

	title := strings.TrimSpace(req.Title)
	if title == "" {
		httputil.BadRequest(c, "title cannot be empty")
		return
	}
	if utf8.RuneCountInString(title) > jobTitleMaxLen {
		httputil.BadRequest(c, fmt.Sprintf("title too long (max %d characters)", jobTitleMaxLen))
		return
	}

	if err := h.jobService.UpdateTitle(jobID, title); err != nil {
		if errors.Is(err, jobsvc.ErrJobNotFound) {
			httputil.NotFound(c, "job not found")
			return
		}
		logger.Errorf(ctx, "[job] update title failed: jobId=%s err=%v", jobID, err)
		httputil.InternalError(c, fmt.Sprintf("failed to update title: %v", err))
		return
	}

	// Broadcast the same event the async auto-generator emits so every
	// subscriber (other tabs, the read-only share page) refreshes without
	// waiting on a poll.
	h.jobService.Publish(jobID, &model.CustomEvent{
		BaseEvent: model.BaseEvent{
			Type:      model.EventTypeCustom,
			JobID:     jobID,
			Timestamp: time.Now().UnixMilli(),
		},
		Name: "job_title_updated",
		Value: map[string]any{
			"title": title,
		},
	})

	c.JSON(http.StatusOK, map[string]string{"title": title})
}

func (h *Handler) JobUpdatePin(ctx context.Context, c *app.RequestContext) {
	jobID := c.Param("jobId")
	if jobID == "" {
		httputil.BadRequest(c, "jobId is required")
		return
	}

	var req updateJobPinRequest
	if err := c.BindJSON(&req); err != nil {
		httputil.BadRequest(c, "invalid request body")
		return
	}

	pinnedAt, err := h.jobService.UpdatePinned(jobID, req.Pinned)
	if err != nil {
		if errors.Is(err, jobsvc.ErrJobNotFound) {
			httputil.NotFound(c, "job not found")
			return
		}
		logger.Errorf(ctx, "[job] update pin failed: jobId=%s pinned=%v err=%v", jobID, req.Pinned, err)
		httputil.InternalError(c, fmt.Sprintf("failed to update pin: %v", err))
		return
	}
	updatedAt := int64(0)
	if j, ok := h.jobService.Get(jobID); ok {
		updatedAt = j.UpdatedAt.UnixMilli()
	}

	c.JSON(http.StatusOK, map[string]any{
		"pinned":    req.Pinned,
		"pinnedAt":  pinnedAt,
		"updatedAt": updatedAt,
	})
}
