package handler

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/messaging"
	larklisten "github.com/fanlv/quartet/pkg/messaging/lark"
	wechatlisten "github.com/fanlv/quartet/pkg/messaging/wechat"
	"github.com/fanlv/quartet/pkg/messaging/wechat/ilink"
	"github.com/fanlv/quartet/pkg/sandbox"
	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/services/agent/acp"
	"github.com/fanlv/quartet/services/agent/eino"
	"github.com/fanlv/quartet/services/agent/probe"
	"github.com/fanlv/quartet/services/config"
	"github.com/fanlv/quartet/services/graph"
	"github.com/fanlv/quartet/services/job"
	"github.com/fanlv/quartet/services/prompt"
	"github.com/fanlv/quartet/services/schedule"
	"github.com/fanlv/quartet/services/session"
	"github.com/fanlv/quartet/services/template"
	"github.com/fanlv/quartet/services/usagestats"
	"github.com/fanlv/quartet/services/workspace"
	"github.com/fanlv/quartet/types/consts"
	"github.com/fanlv/quartet/types/model"
)

// workspaceRootsProvider returns trusted workspace workdirs that should be
// treated as additional allowlist roots for file RW endpoints.
//
// It is wired by NewHandler (from workspace service state) but is also
// overridden by tests.
type workspaceRootsProviderFn func() []string

var workspaceRootsProvider atomic.Value // stores workspaceRootsProviderFn

func init() {
	workspaceRootsProvider.Store(workspaceRootsProviderFn(func() []string { return nil }))
}

func SetWorkspaceRootsProvider(provider func() []string) {
	if provider == nil {
		provider = func() []string { return nil }
	}
	workspaceRootsProvider.Store(workspaceRootsProviderFn(provider))
}

func workspaceRoots() []string {
	fn, _ := workspaceRootsProvider.Load().(workspaceRootsProviderFn)
	if fn == nil {
		return nil
	}
	return fn()
}

type cachedWorkspaceRoots struct {
	revision uint64
	roots    []string
}

// sessionEntry wraps a session.Service with a last-access timestamp
// so idle entries can be evicted to prevent unbounded memory growth.
// lastAccess uses atomic.Int64 (storing UnixNano) so it can be safely
// updated under RLock or even without any lock held on sessionMu.
type sessionEntry struct {
	svc        session.Service
	lastAccess atomic.Int64 // UnixNano
}

func newSessionEntry(svc session.Service) *sessionEntry {
	e := &sessionEntry{svc: svc}
	e.lastAccess.Store(time.Now().UnixNano())
	return e
}

func (e *sessionEntry) touch() {
	e.lastAccess.Store(time.Now().UnixNano())
}

func (e *sessionEntry) idleDuration() time.Duration {
	return time.Since(time.Unix(0, e.lastAccess.Load()))
}

type Handler struct {
	// rootCtx is derived from the process root ctx (signal-cancellable) and is
	// used as the parent for fire-and-forget goroutines launched from request
	// handlers. It must NOT be the per-request ctx (which is cancelled when
	// the response is sent) nor context.Background (which is never cancelled
	// and leaks long-running retries past shutdown).
	rootCtx          context.Context
	sessionServices  map[string]*sessionEntry // jobID -> sessionEntry
	sessionMu        sync.RWMutex
	titleInFlight    sync.Map     // jobID -> struct{}, prevents concurrent title generation
	titleFailCount   atomic.Int32 // consecutive title-generation failures across all jobs (circuit breaker)
	titleOpenSince   atomic.Int64 // unix seconds when circuit opened; 0 means closed
	titleProbing     atomic.Bool  // CAS guard: only one goroutine may probe during half-open state
	agentService     eino.Service
	acpAgentService  acp.ACPService
	modelConfig      config.ModelConfigService
	settingsService  config.SettingsService
	promptService    prompt.Service
	templateService  template.Service
	graphService     graph.Service
	jobService       job.Service
	recentDirsRepo   repository.RecentDirsRepo
	userInputRepo    repository.UserInputRepo
	workspaceService workspace.Service
	scheduleService  schedule.Service
	scheduler        *schedule.Scheduler
	usageStats       usagestats.Service

	// imGateway is shared across all IM platforms. It is initialized lazily
	// by ensureIMGateway so each StartLarkListener / StartWeiXinListener can
	// be called independently (e.g. directly from settings Restart paths).
	// A plain mutex is used instead of sync.Once so a transient failure
	// (e.g. disk-full while creating the mapping repo) doesn't permanently
	// disable IM — the next call retries.
	imGateway   *imGateway
	imGatewayMu sync.RWMutex

	larkManager   *larklisten.Manager
	wechatManager *wechatlisten.Manager
}

func NewHandler(ctx context.Context) (*Handler, error) {
	wss, err := workspace.NewService()
	if err != nil {
		return nil, err
	}

	// Let the sandbox Manager route SandboxRef writes through the
	// workspace service so in-memory and on-disk state stay in sync.
	// Without this, a compose-up that publishes a ref would be silently
	// overwritten by the next service.Update / EnsureDefault.
	sandbox.SetRefSink(wss)

	// Trust each user-created workspace's workdir as an additional file-
	// handler root. Without this, a workspace whose workdir sits outside
	// LOCAL_MEMORY (e.g. a custom project directory) would get 403 when
	// the UI tries to open its file browser or AGENTS.md editor.
	//
	// File endpoints call this on every request, so we cache the derived
	// slice keyed on workspaceService.Revision() — the counter bumps on
	// any state change, so callers see workspace mutations immediately
	// while steady-state traffic avoids a RLock + map iterate + sort on
	// each request.
	var rootsCache atomic.Pointer[cachedWorkspaceRoots]
	SetWorkspaceRootsProvider(func() []string {
		rev := wss.Revision()
		if snap := rootsCache.Load(); snap != nil && snap.revision == rev {
			return snap.roots
		}
		roots := wss.TrustedFileWorkspaceRoots()
		// Store under the rev we read BEFORE List(): if a mutation races in
		// between, the cached snapshot may already be fresher than `rev`, but
		// never stale relative to it. The next caller reads the new revision,
		// sees a miss, and rebuilds — so the cache can at worst lag by one
		// observed mutation, which is what we want.
		rootsCache.Store(&cachedWorkspaceRoots{revision: rev, roots: roots})
		return roots
	})

	ps, err := prompt.NewService()
	if err != nil {
		return nil, err
	}

	js, err := job.NewService(wss)
	if err != nil {
		return nil, err
	}

	ss, err := config.NewSettingsService()
	if err != nil {
		return nil, err
	}

	ts, err := template.NewService()
	if err != nil {
		return nil, err
	}

	gs, err := graph.NewService()
	if err != nil {
		return nil, err
	}

	rdr, err := repository.NewRecentDirsRepo()
	if err != nil {
		return nil, err
	}

	mc, err := config.NewModelConfigService(ctx)
	if err != nil {
		return nil, err
	}

	schSvc, err := schedule.NewService()
	if err != nil {
		return nil, err
	}

	h := &Handler{
		rootCtx:          ctx,
		sessionServices:  make(map[string]*sessionEntry),
		agentService:     eino.NewService(),
		acpAgentService:  acp.NewACPService(),
		modelConfig:      mc,
		settingsService:  ss,
		promptService:    ps,
		templateService:  ts,
		graphService:     gs,
		jobService:       js,
		recentDirsRepo:   rdr,
		userInputRepo:    repository.NewUserInputRepo(),
		workspaceService: wss,
		scheduleService:  schSvc,
		usageStats:       usagestats.NewService(ctx),
	}

	// Wire usage-stats sink into the job service so every step finalize
	// position records counts, durations and tokens. Without this, the
	// SDK is constructed but never receives any snapshots.
	js.SetUsageRecorder(h.usageStats)
	gs.SetUsageRecorder(h.usageStats)

	// Wire the global default End-node hook script getter. Read at hook time so
	// editing the script in Settings takes effect on the next End hook (a pure
	// side-effect, so no replay-snapshot freezing is needed).
	gs.SetEndHookScriptProvider(func() string {
		s, err := h.settingsService.GetSettings()
		if err != nil || s == nil {
			return ""
		}
		return s.GraphEndHookScript
	})

	// Session services are created on demand via getOrCreateSessionService
	// (and recreated on miss via reloadSessionByID). Preloading every job's
	// session.Service at startup would force a full scan of every job's
	// session metadata directory just to populate a cache that the idle
	// evictor would clear an hour later for jobs that never get accessed.
	// Functionality does not depend on the cache being warm — the lookup
	// path scans jobService.List() / job.SessionIDs to locate the owner job
	// and reloads the service from disk on first access.

	probe.WarmupACPSessionCache(ctx)

	// Register job completion callback to update schedule status and release resources.
	js.SetOnJobDone(func(j *model.Job) {
		if j.ScheduleID != "" && h.scheduler != nil {
			h.scheduler.MarkDone(h.rootCtx, j.ScheduleID, j.ID, j.Status)
		}

		// Release agent resources for loop jobs that finished/stopped/failed
		// to prevent unbounded memory growth from accumulated sessions.
		h.releaseJobAgents(j)
	})

	// Start periodic eviction of idle session service entries. The goroutine
	// exits when the root ctx is cancelled (i.e. during shutdown).
	go h.evictIdleSessionServices(h.rootCtx)

	return h, nil
}

// SetScheduler sets the scheduler on the handler.
// Called after NewHandler to wire up the scheduler with the handler's job creation logic.
func (h *Handler) SetScheduler(scheduler *schedule.Scheduler) {
	h.scheduler = scheduler
}

// GetScheduleService returns the handler's schedule service for reuse by the scheduler.
func (h *Handler) GetScheduleService() schedule.Service {
	return h.scheduleService
}

// GetJobService returns the handler's job service for use during shutdown.
func (h *Handler) GetJobService() job.Service {
	return h.jobService
}

// GetUsageStats exposes the usage-stats service so the main shutdown path
// can flush pending writes before exit and any future feature can read
// without going through the Handler.
func (h *Handler) GetUsageStats() usagestats.Service {
	return h.usageStats
}

// ensureIMGateway lazily builds the shared imGateway. Called by both
// StartLarkListener and StartWeiXinListener so either platform can bring up
// the shared gateway independently. Returns true if the gateway is ready.
func (h *Handler) ensureIMGateway(ctx context.Context) bool {
	h.imGatewayMu.Lock()
	if h.imGateway != nil {
		h.imGatewayMu.Unlock()
		return true
	}
	h.imGatewayMu.Unlock()

	// Build the mapping repo outside the lock so that a panic in the
	// constructor can't corrupt mutex state for the deferred unlock.
	mappingRepo, err := repository.NewIMJobMappingRepo()
	if err != nil {
		logger.Errorf(ctx, "[Handler] create im job mapping repo failed: %v", err)
		return false
	}

	h.imGatewayMu.Lock()
	defer h.imGatewayMu.Unlock()
	if h.imGateway != nil {
		return true
	}
	h.imGateway = newIMGateway(h, mappingRepo)
	return h.imGateway != nil
}

// StartIMListeners starts all IM platform listeners in sequence. Used from
// cmd/web/main.go; per-platform Start* helpers remain public so settings/
// restart flows can target them individually.
func (h *Handler) StartIMListeners(ctx context.Context) {
	if !h.ensureIMGateway(ctx) {
		return
	}
	h.startLarkListener(ctx)
	h.startWeiXinListener(ctx)
}

// StopIMListeners stops every IM platform listener that StartIMListeners
// brought up. Called from cmd/web/main.go's shutdown path BEFORE HTTP
// shutdown so the listeners' WebSocket / long-poll goroutines unwind under
// our control instead of being torn down implicitly when the root context
// cancels — the implicit teardown path produces a misleading
// "[lark/sdk] receive message failed: ... use of closed network connection"
// WARN on every restart even though the close is fully expected.
func (h *Handler) StopIMListeners() {
	if h.larkManager != nil {
		h.larkManager.Stop()
	}
	if h.wechatManager != nil {
		h.wechatManager.Stop()
	}
}

func (h *Handler) startLarkListener(ctx context.Context) {
	settingsSvc := h.settingsService
	replier := larklisten.NewReplier(settingsSvc.GetLarkConfig)
	h.imGateway.RegisterReplier(messaging.PlatformLark, replier)

	mgr := larklisten.NewManager(h.imGateway, settingsSvc.GetLarkConfig)
	mgr.Start(ctx)
	h.larkManager = mgr
}

// startWeiXinListener wires the WeChat iLink listener into the shared
// imGateway. When no WeChat credentials are saved yet, the Manager logs a
// warning and stays idle — the Web scan-to-login API later calls
// wechatManager.Restart() after saving credentials to bring the listener up.
func (h *Handler) startWeiXinListener(ctx context.Context) {
	credsProvider := func() []*ilink.Credentials {
		creds, err := ilink.LoadAllCredentials()
		if err != nil {
			logger.Warnf(ctx, "[Handler] load wechat credentials failed: %v", err)
			return nil
		}
		return creds
	}

	// Listener and Replier share one instance so the listener can call
	// replier.RegisterIncoming(msg, botID) to populate msgMeta/userToken.
	replier := wechatlisten.NewReplier(credsProvider)
	h.imGateway.RegisterReplier(messaging.PlatformWeChat, replier)

	mgr := wechatlisten.NewManager(h.imGateway, replier, credsProvider)
	mgr.Start(ctx)
	h.wechatManager = mgr
}

// GetWeChatManager exposes the WeChat listener manager for Restart() calls
// from wechat_login_api.go after credentials are saved / deleted.
func (h *Handler) GetWeChatManager() *wechatlisten.Manager {
	return h.wechatManager
}

// GetIMGateway exposes the shared gateway for HTTP handlers that need to read
// pending-contact state (see wechat_login_api.go).
func (h *Handler) GetIMGateway() *imGateway {
	h.imGatewayMu.RLock()
	defer h.imGatewayMu.RUnlock()
	return h.imGateway
}

// GetSettingsService exposes the settings service for handlers outside this
// file (e.g. wechat_login_api.go).
func (h *Handler) GetSettingsService() config.SettingsService {
	return h.settingsService
}

// ScheduleTrigger creates and starts a job for the given scheduled task. It
// launches a GraphRun from the referenced workflow template.
func (h *Handler) ScheduleTrigger(ctx context.Context, task *model.ScheduledTask) (string, error) {
	return h.triggerGraphSchedule(ctx, task)
}

// triggerGraphSchedule launches a GraphRun for a graph-workflow-type schedule.
// It follows the design's three-stage failure model:
//
//   - Stage one (before the run Job is created): the live workflow template is
//     re-read and validated, and the workspace/workdir is resolved. Any failure
//     here returns an error WITHOUT creating a Job, so a missing/invalid
//     template leaves no orphan run record. The scheduler records the failure
//     on the schedule (LastStatus=Failed + LastTriggerError) and releases the
//     slot.
//   - Stage two (Job created, StartRun failed synchronously): StartRun fails
//     before runGraph launches, so the GraphRun terminal bridge never fires.
//     We do NOT roll back the Job; instead FailGraphJob lands the full error on
//     it and returns (jobID, err) so the scheduler points LastRunJobID at this
//     real run record and releases the slot. The slot is released exactly once
//     (the scheduler's failure path), since FailGraphJob deliberately skips the
//     done callback.
//   - Stage three (StartRun succeeded): the run is live; the GraphRun terminal
//     bridge handles status write-back and slot release on completion.
//
// The graph run follows the LIVE workflow template (StartRun re-reads it by ID
// and freezes its own snapshot), so no snapshot is kept on the schedule. The
// run timeout comes from the workflow's RunConfig, not task.Timeout (graph runs
// have their own run-config semantics; see the design's scope note).
func (h *Handler) triggerGraphSchedule(ctx context.Context, task *model.ScheduledTask) (string, error) {
	// Stage one: re-read and validate the live workflow template.
	wf, err := h.graphService.GetWorkflow(ctx, task.GraphWorkflowID)
	if err != nil {
		return "", fmt.Errorf("schedule %s: graph workflow %s: %w", task.ID, task.GraphWorkflowID, err)
	}
	if verrs := h.graphService.ValidateConfig(ctx, &wf.Config); len(verrs) > 0 {
		return "", fmt.Errorf("schedule %s: graph workflow %s is invalid: %s", task.ID, task.GraphWorkflowID, formatGraphValidationErrors(verrs))
	}

	// Stage one/two boundary: graph service resolves workspace/workdir with the
	// schedule fallback rules, then creates the run Job only after validation.
	j, err := h.graphService.CreateScheduledRunJob(ctx, task, h.jobService, h.workspaceService)
	if err != nil {
		return "", err
	}

	// Stage two: launch the GraphRun on the freshly created Job.
	req := &model.StartGraphRunRequest{
		WorkflowID:  task.GraphWorkflowID,
		JobID:       j.ID,
		WorkspaceID: j.WorkspaceID,
		Workdir:     j.Workdir,
	}
	runner := newJobRunner(h, j)
	if _, err := h.graphService.StartRun(ctx, req, runner, h.jobService); err != nil {
		// Land the full error on the run record and report it. The scheduler
		// releases the slot on this failed trigger; FailGraphJob must not emit
		// the done callback or the slot would be released twice.
		if failErr := h.jobService.FailGraphJob(ctx, j.ID, err.Error()); failErr != nil {
			logger.Errorf(ctx, "[ScheduleTrigger] mark graph job failed: schedule=%s job=%s err=%v", task.ID, j.ID, failErr)
		}
		return j.ID, fmt.Errorf("schedule %s: start graph run failed: %w", task.ID, err)
	}

	// Stage three: run is live; the GraphRun terminal bridge takes over.
	return j.ID, nil
}

// resolveScheduleWorkspaceWorkdir resolves the workspace and working directory a
// scheduled task's run should use, applying the design's fallback rules:
//
//   - Workspace unset → default workspace (ws-1); not a failure.
//   - Workspace deleted out of band → fall back to ws-1 AND clear the saved
//     workdir (it pointed into the deleted workspace and is almost certainly
//     gone too); warn only, not a failure.
//   - Workdir unset → backfill the workspace's default workdir; not a failure.
//   - Workdir invalid (missing / not a dir / outside the workspace) → failure.
//   - Fallback default workspace still missing (ensure-default failed at boot)
//     → failure.
//
// Scheduled tasks live in a global store independent of the workspace directory,
// so deleting a workspace does NOT delete its schedules — they keep firing and
// need a valid target resolved here.
func (h *Handler) resolveScheduleWorkspaceWorkdir(ctx context.Context, task *model.ScheduledTask) (wsID, workdir string, ws *model.Workspace, err error) {
	wsID = task.WorkspaceID
	workdir = task.Workdir
	if wsID == "" {
		wsID = consts.DefaultWorkspaceID
	}
	ws, ok := h.workspaceService.Get(wsID)
	if !ok {
		logger.Warnf(ctx, "[ScheduleTrigger] workspace %s not found for task %s, falling back to %s", wsID, task.ID, consts.DefaultWorkspaceID)
		wsID = consts.DefaultWorkspaceID
		workdir = ""
		ws, ok = h.workspaceService.Get(wsID)
		if !ok {
			return "", "", nil, fmt.Errorf("schedule %s: default workspace %s is missing; ensure-default may have failed at startup", task.ID, consts.DefaultWorkspaceID)
		}
	}
	if workdir == "" && ws != nil {
		workdir = ws.Workdir
	}
	if err := validateWorkdir(workdir); err != nil {
		return "", "", nil, fmt.Errorf("schedule %s: invalid workdir: %w", task.ID, err)
	}
	if ws != nil {
		if err := ensureWorkdirWithinWorkspace(workdir, ws.Workdir); err != nil {
			return "", "", nil, fmt.Errorf("schedule %s: %w", task.ID, err)
		}
	}
	return wsID, workdir, ws, nil
}

// getOrCreateSessionService returns (or creates) the session.Service for the given jobID.
func (h *Handler) getOrCreateSessionService(wsID, jobID string) (session.Service, error) {
	h.sessionMu.RLock()
	entry, ok := h.sessionServices[jobID]
	h.sessionMu.RUnlock()
	if ok {
		entry.touch()
		return entry.svc, nil
	}

	h.sessionMu.Lock()
	defer h.sessionMu.Unlock()

	// Double-check after acquiring write lock
	if entry, ok := h.sessionServices[jobID]; ok {
		entry.touch()
		return entry.svc, nil
	}

	ss, err := session.NewService(wsID, jobID)
	if err != nil {
		return nil, err
	}
	h.sessionServices[jobID] = newSessionEntry(ss)
	return ss, nil
}

// cleanupSessions cancels and deletes all agents/sessions for the given job.
// Cache keys for agent services are (wsID, jobID, sessionID), so we must
// pass the job dimensions through; using sessionID alone would silently
// no-op if a different job happened to mint the same sessionID.
func (h *Handler) cleanupSessions(wsID, jobID string, sessionIDs []string) {
	if len(sessionIDs) == 0 {
		return
	}
	// Reload the session service from disk if it was evicted (idle timeout
	// or never preloaded). Without this fallback, ss.Delete(sid) would
	// silently no-op and leave on-disk session metadata flagged as live
	// even though the caller intended to delete it.
	ss, err := h.getOrCreateSessionService(wsID, jobID)
	if err != nil {
		logger.Errorf(context.Background(),
			"[Handler] cleanupSessions: load session service failed: wsId=%s jobId=%s err=%v",
			wsID, jobID, err)
	}
	for _, sid := range sessionIDs {
		if lease, ok := h.agentService.Get(wsID, jobID, sid); ok {
			lease.Value.StopAndFlush()
			lease.Release()
		}
		if lease, ok := h.acpAgentService.Get(wsID, jobID, sid); ok {
			lease.Value.StopAndFlush()
			lease.Release()
		}
		if ss != nil {
			ss.Delete(sid)
		}
		h.agentService.Delete(wsID, jobID, sid)
		h.acpAgentService.Delete(wsID, jobID, sid)
	}
}

// jobAllSessionIDs returns every session owned by a job — its loop/interactive
// SessionIDs plus its graph node GraphSessionIDs — de-duplicated. Used by the
// delete paths so a graph job's node sessions are torn down too and never leak.
func jobAllSessionIDs(j *model.Job) []string {
	if len(j.GraphSessionIDs) == 0 {
		return j.SessionIDs
	}
	seen := make(map[string]struct{}, len(j.SessionIDs)+len(j.GraphSessionIDs))
	all := make([]string, 0, len(j.SessionIDs)+len(j.GraphSessionIDs))
	for _, sid := range j.SessionIDs {
		if _, ok := seen[sid]; ok {
			continue
		}
		seen[sid] = struct{}{}
		all = append(all, sid)
	}
	for _, sid := range j.GraphSessionIDs {
		if _, ok := seen[sid]; ok {
			continue
		}
		seen[sid] = struct{}{}
		all = append(all, sid)
	}
	return all
}

// releaseJobAgents releases agent resources (eino/ACP in-memory objects) for a
// completed loop job. Unlike cleanupSessions, this does NOT delete session
// metadata — it only frees the in-memory agent instances that hold model
// connections and sandbox references, so they can be garbage collected.
// For completed loop jobs, also removes the session service entry.
// Stopped/failed loop jobs keep their session service because they can still
// receive interactive messages; the idle eviction will clean them up later.
func (h *Handler) releaseJobAgents(j *model.Job) {
	for _, sid := range j.SessionIDs {
		h.agentService.Delete(j.WorkspaceID, j.ID, sid)
		h.acpAgentService.Delete(j.WorkspaceID, j.ID, sid)
	}

	// Only evict session service for completed loop jobs. Stopped/failed jobs
	// may still receive interactive messages that need the session service.
	if j.Mode == model.JobModeLoop && j.Status == model.JobStatusCompleted {
		h.sessionMu.Lock()
		delete(h.sessionServices, j.ID)
		h.sessionMu.Unlock()
	}
}

// getSessionByID looks up a session across all job-scoped session services.
func (h *Handler) getSessionByID(sessionID string) (*model.Session, session.Service, bool) {
	h.sessionMu.RLock()
	defer h.sessionMu.RUnlock()
	for _, entry := range h.sessionServices {
		if s, ok := entry.svc.Get(sessionID); ok {
			entry.touch()
			return s, entry.svc, true
		}
	}
	return nil, nil, false
}

// lookupSessionService returns the session.Service that owns sessionID,
// reloading from disk when the service entry was evicted (idle timeout or
// completed loop). Metadata update paths should prefer this over
// getSessionByID so eviction does not cause silent no-ops on title / model /
// ACP-mode updates.
func (h *Handler) lookupSessionService(sessionID string) (session.Service, bool) {
	if _, ss, ok := h.getSessionByID(sessionID); ok {
		return ss, true
	}
	if _, ok := h.reloadSessionByID(sessionID); !ok {
		return nil, false
	}
	if _, ss, ok := h.getSessionByID(sessionID); ok {
		return ss, true
	}
	return nil, false
}

// lookupSession returns the session pointer (read-only) across all
// job-scoped session services, reloading from disk when the service entry
// was evicted.
func (h *Handler) lookupSession(sessionID string) (*model.Session, bool) {
	if s, _, ok := h.getSessionByID(sessionID); ok {
		return s, true
	}
	return h.reloadSessionByID(sessionID)
}

// reloadSessionByID finds a session by scanning all jobs for the given session ID,
// then reloads the session service from disk via getOrCreateSessionService.
// This handles the case where the session service was evicted from memory
// (e.g., idle timeout or loop-job completion).
func (h *Handler) reloadSessionByID(sessionID string) (*model.Session, bool) {
	// Find the job that owns this session. Scan both SessionIDs (loop/
	// interactive) and GraphSessionIDs (graph node sessions) so an interactive
	// message targeting a finished graph node's session can reload it from disk
	// after the session service was evicted.
	for _, j := range h.jobService.List() {
		if !sessionBelongsToJob(j, sessionID) {
			continue
		}
		ss, err := h.getOrCreateSessionService(j.WorkspaceID, j.ID)
		if err != nil {
			logger.Error("[reloadSessionByID] getOrCreateSessionService failed: %v, sessionId=%s, jobId=%s, wsId=%s", err, sessionID, j.ID, j.WorkspaceID)
			return nil, false
		}
		if s, ok := ss.Get(sessionID); ok {
			return s, true
		}
		logger.Error("[reloadSessionByID] session not found after reload from disk, sessionId=%s, jobId=%s", sessionID, j.ID)
		return nil, false
	}
	return nil, false
}

// sessionBelongsToJob reports whether sessionID is owned by job j, checking
// both its loop/interactive SessionIDs and its graph node GraphSessionIDs.
func sessionBelongsToJob(j *model.Job, sessionID string) bool {
	for _, sid := range j.SessionIDs {
		if sid == sessionID {
			return true
		}
	}
	for _, sid := range j.GraphSessionIDs {
		if sid == sessionID {
			return true
		}
	}
	return false
}

// sessionIdleTimeout is how long a session service entry can sit idle
// (no getOrCreateSessionService / getSessionByID calls) before eviction.
// Evicted entries are re-created from disk on next access.
const sessionIdleTimeout = 1 * time.Hour

// sessionEvictInterval controls how often the idle session eviction runs.
const sessionEvictInterval = 10 * time.Minute

// evictIdleSessionServices periodically removes session service entries
// for jobs that haven't been accessed recently. This prevents unbounded
// memory growth from interactive jobs that accumulate over time.
// Evicted entries will be lazily re-created by getOrCreateSessionService
// on the next request, so this is safe even if the job is still active.
func (h *Handler) evictIdleSessionServices(ctx context.Context) {
	ticker := time.NewTicker(sessionEvictInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			h.doEvictIdleSessions()
		}
	}
}

func (h *Handler) doEvictIdleSessions() {
	// Phase 1: collect candidates under read lock (no external calls).
	h.sessionMu.RLock()
	var candidates []string
	for jobID, entry := range h.sessionServices {
		if entry.idleDuration() > sessionIdleTimeout {
			candidates = append(candidates, jobID)
		}
	}
	h.sessionMu.RUnlock()

	if len(candidates) == 0 {
		return
	}

	// Phase 2: filter out running jobs without holding sessionMu.
	var toEvict []string
	for _, jobID := range candidates {
		if j, ok := h.jobService.Get(jobID); ok && j.Status == model.JobStatusRunning {
			continue
		}
		toEvict = append(toEvict, jobID)
	}

	if len(toEvict) == 0 {
		return
	}

	// Phase 3: evict under write lock.
	h.sessionMu.Lock()
	var evicted int
	for _, jobID := range toEvict {
		if entry, ok := h.sessionServices[jobID]; ok && entry.idleDuration() > sessionIdleTimeout {
			delete(h.sessionServices, jobID)
			evicted++
		}
	}
	h.sessionMu.Unlock()
	if evicted > 0 {
		logger.Info("[Handler] evicted %d idle session service entries", evicted)
	}
}
