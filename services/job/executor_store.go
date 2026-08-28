package job

import (
	"context"
	"hash/fnv"
	"sync"
	"time"

	"github.com/fanlv/quartet/pkg/fileserver"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/services/usagestats"
	"github.com/fanlv/quartet/services/workspace"
	"github.com/fanlv/quartet/types/model"
)

const (
	jobRunActionSendMessage      = "send_message"
	jobRunActionSendMessageStart = "send_message_start"
	jobRunActionFinish           = "finish"
	jobRunActionStop             = "stop"
	jobRunActionFail             = "fail"

	jobRunSourceInteractive = "interactive"

	jobPersistActionAttachSession = "attach_session"
)

type clock interface {
	NowMillis() int64
}

type realClock struct{}

func (realClock) NowMillis() int64 {
	return time.Now().UnixMilli()
}

func (s *serviceImpl) nowMillis() int64 {
	if s != nil && s.clock != nil {
		return s.clock.NowMillis()
	}
	return realClock{}.NowMillis()
}

// serviceImpl is the core job service implementation. It manages job CRUD,
// execution lifecycle, SSE pub/sub, and cancellation tracking.
//
// The implementation is split by responsibility across CRUD/mutators,
// persistence, lifecycle/run resources, stop/recovery handling, run execution,
// usage recording, and SSE event buffering
// files. Keep this comment at the responsibility level: concrete filenames move
// during refactors and quickly become stale.
//
// # Field ownership model
//
// The internal *model.Job pointer stored in s.jobs is shared between the
// handler goroutine (via targeted mutators) and the run goroutine. To
// avoid data races, fields are split by ownership:
//
//   - Handler-owned (mutated by targeted methods): Title, Mode, Workdir, ShareToken, ShareShowWorkspaceName, Deleted
//   - Run-owned: Status, StartedAt, FinishedAt, Progress, SessionIDs
//   - Service-owned denormalized cache (targeted mutator only): FirstModelID
//
// All reads and writes of job fields MUST hold s.mu. Targeted mutators
// (UpdateTitle, ConfigureShare, ClearShareToken, SetFirstModelID,
// MarkDeleted) only touch their named field plus UpdatedAt, so they
// cannot race with run-owned writes on the same pointer.
//
// # Invariants
//
//   - Progress is non-nil for every Job in s.jobs. ensureProgress establishes
//     this at the two entry points (load on startup, Create) so internal
//     paths can dereference Progress without per-call nil guards.
//
// # Lock ordering
//
// Most mutexes in this struct protect independent data and should not be
// nested: acquire one lock, release it, then optionally acquire another. The
// allowed nested orders are deliberately small and must not be reversed:
//
//   - persistence paths take the per-job persist shard first, then s.mu, so
//     disk writes preserve the same order as the in-memory snapshots they
//     persist.
//   - run launch paths (SendMessage) take the persist shard,
//     then s.mu, then the short-lived run-resource locks used by
//     prepareRunResources (notifiedJobsMu, cancelMu, doneMu). This
//     keeps Status=running and the cancel/done registrations atomic
//     from observers' perspective. Code holding any of those resource locks
//     must never call back into s.mu.
//
// SSE pub/sub has its own internal lock graph encapsulated by busOwner and
// jobEventBuffer — from serviceImpl's perspective the bus is a black box with
// method calls.
type serviceImpl struct {
	jobs map[string]*model.Job
	mu   sync.RWMutex
	// persistShards serializes every job.json writer and physical Job deletion
	// on a per-job basis.
	// Sharded by FNV-1a(jobID) so saves on different jobs don't block each
	// other — the previous global persistMu serialized every job's saves
	// across the whole process, which became visible when several scheduled
	// graph runs fanned out at the same time. Within a single job the order is
	// still preserved, and Delete cannot remove a directory while an older
	// Save is able to recreate it.
	persistShards [persistShardCount]sync.Mutex

	// per-workspace repos: wsID -> JobRepo
	repos          map[string]repository.JobRepo
	newJobRepo     func(wsID string) (repository.JobRepo, error)
	newSessionRepo func(wsID, jobID string) (repository.SessionRepo, error)
	repoMu         sync.RWMutex
	wsSvc          workspace.Service
	fileManager    fileserver.FileManager

	// pub/sub: per-job append-only event buffer + cursor readers. Owns its
	// own mutex graph internally; serviceImpl exposes Publish / Subscribe /
	// PublishTransient as thin proxies and routes buffer removal from
	// the lifecycle paths (Delete) directly.
	bus *busOwner

	// stop control: jobID -> cancel entry. The entry identity is used during
	// deferred cleanup so an old run cannot delete a newer run's cancel func.
	cancels  map[string]*cancelEntry
	cancelMu sync.Mutex

	// run tracking: jobID -> done channel (closed when the run goroutine exits)
	dones  map[string]chan struct{}
	doneMu sync.Mutex

	// Tracks the job status prior to an interactive SendMessage flipping it to
	// Running. When the interactive run terminates (stop/finish/fail), the
	// prior terminal state is restored so an ad-hoc message cannot regress a
	// Completed/Failed/Stopped job into a different state.
	//
	// Protected by s.mu — the consume path is reached from
	// applyTerminalStatusLocked which already holds s.mu, and folding this
	// rarely-touched map into s.mu avoids an mu→other nested-lock that the
	// "must NEVER be nested" invariant on this struct otherwise forbids.
	interactivePriorStatus map[string]model.JobStatus

	// optional callback when a job finishes
	onJobDone   func(job *model.Job)
	onJobDoneMu sync.RWMutex

	// Tracks job IDs that have already fired notifyJobDone so a duplicate
	// trigger (stopJob → finishJob race, or an external state-transition bug)
	// cannot emit two `[scheduler] done:` lines / two MarkDone calls for the
	// same run. Cleared on relaunch so a re-launched jobID can fire again.
	notifiedJobs   map[string]struct{}
	notifiedJobsMu sync.Mutex

	// listVersions owns the monotonic per-workspace list counters used for
	// conditional GET ETags. Keep this state behind a small component so the
	// service struct does not need to expose another map+mutex pair directly.
	listVersions *listVersionTracker
	observations jobObservationTracker

	// usageRecorder is the optional usage-stats sink. When set, every
	// completed interactive round submits a Snapshot with timings, counts
	// and tokens. Nil means
	// stats are simply not recorded — the executor never depends on it
	// returning anything.
	usageRecorder usagestats.Recorder

	// endHookScriptFn returns the global default end-hook script run after every
	// interactive round terminates. Set once at startup via
	// SetEndHookScriptProvider (before serving); read at hook time so editing the
	// script in Settings takes effect on the next round. nil → no end hook.
	endHookScriptFn func() string

	messageQueueDispatcher   MessageQueueDispatcher
	messageQueueDispatcherMu sync.RWMutex
	messageQueueDispatching  map[string]bool
	messageQueueDispatchMu   sync.Mutex
	stopping                 bool

	// clock is instance-scoped so run/event millisecond timestamps can be
	// deterministic in focused tests without mutating package globals.
	clock clock
}

type listVersionTracker struct {
	mu       sync.Mutex
	versions map[string]int64
}

func newListVersionTracker() *listVersionTracker {
	return &listVersionTracker{versions: make(map[string]int64)}
}

// bumpListVersion increments both the workspace-scoped and global list versions
// so clients polling either the workspace-filtered or unfiltered endpoint see a
// changed ETag. Safe to call under any lock — takes its own mutex.
func (s *serviceImpl) bumpListVersion(wsID string) {
	s.listVersions.bump(wsID)
}

func (t *listVersionTracker) bump(wsID string) {
	t.mu.Lock()
	t.versions[wsID]++
	t.versions[""]++
	t.mu.Unlock()
}

// persistShardCount sets the granularity of per-job save serialization. 64
// shards is the same fan-out the repository layer uses for its file shard
// locks — collisions are rare enough that two unrelated jobs hashing into the
// same shard see negligible serialization, while the array stays cheap (a few
// hundred bytes) and lock-free of map allocations.
const persistShardCount = 64

// persistLock returns the persist mutex for jobID. The returned pointer is
// stable for the lifetime of the service — shards are a fixed array on
// serviceImpl, never replaced — so callers can safely defer Unlock on it.
func (s *serviceImpl) persistLock(jobID string) *sync.Mutex {
	h := fnv.New32a()
	_, _ = h.Write([]byte(jobID))
	return &s.persistShards[h.Sum32()%persistShardCount]
}

// WorkspaceListVersion returns the current monotonic list version. Empty wsID
// returns the global counter.
func (s *serviceImpl) WorkspaceListVersion(wsID string) int64 {
	return s.listVersions.current(wsID)
}

func (t *listVersionTracker) current(wsID string) int64 {
	t.mu.Lock()
	v := t.versions[wsID]
	t.mu.Unlock()
	return v
}

// setInteractivePriorStatus records the job status before an interactive
// SendMessage flipped it to Running.
func (s *serviceImpl) setInteractivePriorStatus(jobID string, status model.JobStatus) {
	s.mu.Lock()
	s.interactivePriorStatus[jobID] = status
	s.mu.Unlock()
}

// consumeInteractivePriorStatusLocked retrieves and clears the saved prior
// status. Returns (status, true) if one was recorded. Caller MUST hold s.mu.
func (s *serviceImpl) consumeInteractivePriorStatusLocked(jobID string) (model.JobStatus, bool) {
	prior, ok := s.interactivePriorStatus[jobID]
	if ok {
		delete(s.interactivePriorStatus, jobID)
	}
	return prior, ok
}

// isTerminalJobStatus reports whether the status represents a non-running
// final state (Completed/Failed/Stopped) that should be preserved across
// interactive message runs.
func isTerminalJobStatus(s model.JobStatus) bool {
	return s == model.JobStatusCompleted || s == model.JobStatusFailed || s == model.JobStatusStopped
}

// shouldPreservePriorStatus reports whether an interactive send should restore
// the Job's prior terminal status. A stopped chat can start a new turn, while
// completed and failed records remain historical terminal outcomes.
func shouldPreservePriorStatus(s model.JobStatus) bool {
	return s == model.JobStatusCompleted || s == model.JobStatusFailed
}

func (s *serviceImpl) SetOnJobDone(fn func(job *model.Job)) {
	s.onJobDoneMu.Lock()
	s.onJobDone = fn
	s.onJobDoneMu.Unlock()
}

func (s *serviceImpl) notifyJobDone(job *model.Job) {
	// Idempotency: finishJob/stopJob/failJob each call notifyJobDone, and the
	// run defer is guarded against calling >1 of them in a single run —
	// but a state-transition race (e.g. a step stopJob during an in-flight
	// finishJob, or a zombie second backend process sharing the log file)
	// has produced duplicate `[scheduler] done:` entries in the past. Dedup
	// per jobID so MarkDone (and the on-disk schedule state it mutates) stays
	// consistent even if callers double-fire.
	s.notifiedJobsMu.Lock()
	if _, already := s.notifiedJobs[job.ID]; already {
		s.notifiedJobsMu.Unlock()
		s.mu.RLock()
		status := job.Status
		s.mu.RUnlock()
		logger.Warnf(context.Background(), "[job.Service] notifyJobDone skipped (duplicate): jobId=%s scheduleId=%s status=%s", job.ID, job.ScheduleID, status)
		return
	}
	s.notifiedJobs[job.ID] = struct{}{}
	s.notifiedJobsMu.Unlock()

	s.onJobDoneMu.RLock()
	fn := s.onJobDone
	s.onJobDoneMu.RUnlock()
	if fn != nil {
		// Hand the callback a deep copy so it can read fields like Status /
		// Progress without acquiring s.mu. The shared internal pointer is
		// still being mutated by the run goroutine that called us
		// (terminal Status was written under lock and unlocked just before
		// notifyJobDone fired) and may race with a concurrent
		// SendMessage that flips it back to Running.
		s.mu.RLock()
		cp := job.DeepCopy()
		s.mu.RUnlock()
		fn(cp)
	}
}

// clearJobDoneNotified removes the dedup flag so a fresh launch of the same
// jobID (SendMessage after a previous terminal run) can fire the
// OnJobDone callback again.
func (s *serviceImpl) clearJobDoneNotified(jobID string) {
	s.notifiedJobsMu.Lock()
	delete(s.notifiedJobs, jobID)
	s.notifiedJobsMu.Unlock()
}

// getOrCreateRepo returns or lazily creates a JobRepo for the given workspace.
func (s *serviceImpl) getOrCreateRepo(wsID string) (repository.JobRepo, error) {
	s.repoMu.RLock()
	repo, ok := s.repos[wsID]
	s.repoMu.RUnlock()
	if ok {
		return repo, nil
	}

	s.repoMu.Lock()
	defer s.repoMu.Unlock()
	// Double-check after acquiring write lock
	if repo, ok := s.repos[wsID]; ok {
		return repo, nil
	}
	newJobRepo := s.newJobRepo
	if newJobRepo == nil {
		newJobRepo = repository.NewJobRepo
	}
	repo, err := newJobRepo(wsID)
	if err != nil {
		return nil, err
	}
	s.repos[wsID] = repo
	return repo, nil
}

func (s *serviceImpl) load() {
	// Load jobs from all workspace directories.
	// Errors are logged and skipped per-workspace so that one bad workspace
	// doesn't prevent other jobs from loading.
	ctx := context.Background()
	start := time.Now()
	workspaces := s.wsSvc.List()
	loadedJobs := 0
	for _, ws := range workspaces {
		repo, err := s.getOrCreateRepo(ws.ID)
		if err != nil {
			logger.Errorf(ctx, "[job.Service] create repo failed: workspace=%s err=%v", ws.ID, err)
			continue
		}
		// Complete any two-phase deletion interrupted after its durable
		// Deleted tombstone was written. SweepDeleted is best-effort per Job:
		// failed removals remain tombstoned and LoadAll below keeps them out of
		// memory, while a later process restart retries the physical cleanup.
		if err := repo.SweepDeleted(); err != nil {
			logger.Errorf(ctx, "[job.Service] sweep deleted jobs failed: workspace=%s err=%v", ws.ID, err)
		}
		jobs, err := repo.LoadAll()
		if err != nil {
			logger.Errorf(ctx, "[job.Service] load jobs failed: workspace=%s err=%v", ws.ID, err)
			continue
		}
		for _, j := range jobs {
			if s.reconcileLoadedJob(ctx, repo, j) {
				s.store(j.ID, j)
				loadedJobs++
			}
		}
	}
	logger.Infof(ctx, "[job.Service] startup load done: workspaces=%d jobs=%d durMs=%d", len(workspaces), loadedJobs, time.Since(start).Milliseconds())
}

// reconcileLoadedJob applies startup-only repair and denormalization to one
// loaded job. It returns false when the job should be skipped from the in-memory
// store (currently only soft-deleted jobs). Keeping this logic out of load()
// makes the per-job reconciliation independently testable and keeps load()
// focused on workspace/repo traversal.
func (s *serviceImpl) reconcileLoadedJob(ctx context.Context, repo repository.JobRepo, j *model.Job) bool {
	// Startup currently calls this before the service is published, but keeping
	// its repair writes on the normal shard makes the persistence contract true
	// even for focused tests and future live-reconciliation callers.
	lock := s.persistLock(j.ID)
	lock.Lock()
	defer lock.Unlock()

	if j.Deleted {
		return false
	}
	// Establish the in-memory invariant: every Job in s.jobs has a non-nil
	// Progress so every other access path can dereference it without a guard.
	ensureProgress(j)
	// Reset running jobs to failed on startup (they were interrupted)
	if j.Status == model.JobStatusRunning {
		interruptedAt := s.nowMillis()
		j.Status = model.JobStatusFailed
		j.FinishedAt = interruptedAt
		// The process crash time is unknown. Do not count the server's offline
		// interval as turn execution time when repairing the stale run.
		j.TurnDurationPending = false
		j.Progress.LastError = "interrupted: process restarted while running"
		if clientMessageID := j.ActiveClientMessageID; clientMessageID != "" {
			if receipt, ok := j.ClientMessageReceipts[clientMessageID]; ok {
				receipt.State = model.ClientMessageStateInterrupted
				receipt.FinishedAt = interruptedAt
				j.ClientMessageReceipts[clientMessageID] = receipt
			}
			j.ActiveClientMessageID = ""
		}
		if err := repo.Save(j.ID, j); err != nil {
			logger.Errorf(ctx, "[job.Service] reset running->failed persist failed: workspace=%s jobId=%s err=%v", j.WorkspaceID, j.ID, err)
		}
	}
	// Prefill FirstModelID for legacy jobs so the list endpoint never
	// has to open session metadata files at request time (avoids the
	// N+1 I/O hit on first listing after an upgrade). This runs once
	// at startup and is idempotent — if already cached we skip the
	// repo load.
	if j.FirstModelID == "" && len(j.SessionIDs) > 0 {
		s.prefillLoadedFirstModelID(ctx, repo, j)
	}
	return true
}

func (s *serviceImpl) prefillLoadedFirstModelID(ctx context.Context, repo repository.JobRepo, j *model.Job) {
	newSessionRepo := s.newSessionRepo
	if newSessionRepo == nil {
		newSessionRepo = repository.NewSessionRepo
	}
	srepo, err := newSessionRepo(j.WorkspaceID, j.ID)
	if err != nil {
		logger.Debugf(ctx, "[job.Service] prefill FirstModelID: create session repo failed: jobId=%s err=%v", j.ID, err)
		return
	}
	for _, sid := range j.SessionIDs {
		meta, err := srepo.Load(sid)
		if err != nil || meta == nil || meta.Deleted {
			continue
		}
		j.FirstModelID = meta.ModelID
		break
	}
	if j.FirstModelID != "" {
		if err := repo.Save(j.ID, j); err != nil {
			logger.Warnf(ctx, "[job.Service] prefill FirstModelID save failed: jobId=%s err=%v", j.ID, err)
		}
	}
}

func (s *serviceImpl) store(jobID string, job *model.Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[jobID] = job
}

// jobRunStateSnapshot captures the run-owned subset of fields used to roll
// back a fail-fast persist failure in SendMessage. Handler-
// owned fields are intentionally excluded so a concurrent targeted update
// (e.g. UpdateTitle) is not clobbered by the rollback.
type jobRunStateSnapshot struct {
	Status                  model.JobStatus
	StartedAt               int64
	FinishedAt              int64
	TotalTurnDurationMs     int64
	TurnDurationPending     bool
	SessionIDs              []string
	Progress                *model.JobProgress
	LastRunOutcome          model.RunOutcome
	ActiveClientMessageID   string
	ClientMessageReceipts   map[string]model.ClientMessageReceipt
	MessageQueue            []model.QueuedJobMessage
	MessageQueueVersion     int64
	MessageQueuePaused      bool
	MessageQueuePauseReason string
}

// snapshotRunStateLocked captures the run-owned fields that may be
// mutated before a recovery-critical save. Caller must hold s.mu.
func snapshotRunStateLocked(job *model.Job) jobRunStateSnapshot {
	cp := job.DeepCopy()
	return jobRunStateSnapshot{
		Status:                  cp.Status,
		StartedAt:               cp.StartedAt,
		FinishedAt:              cp.FinishedAt,
		TotalTurnDurationMs:     cp.TotalTurnDurationMs,
		TurnDurationPending:     cp.TurnDurationPending,
		SessionIDs:              cp.SessionIDs,
		Progress:                cp.Progress,
		LastRunOutcome:          cp.LastRunOutcome,
		ActiveClientMessageID:   cp.ActiveClientMessageID,
		ClientMessageReceipts:   cp.ClientMessageReceipts,
		MessageQueue:            cp.MessageQueue,
		MessageQueueVersion:     cp.MessageQueueVersion,
		MessageQueuePaused:      cp.MessageQueuePaused,
		MessageQueuePauseReason: cp.MessageQueuePauseReason,
	}
}

// restoreRunStateLocked restores run-owned fields from a snapshot. Caller
// must hold s.mu.
func restoreRunStateLocked(job *model.Job, snap jobRunStateSnapshot) {
	job.Status = snap.Status
	job.StartedAt = snap.StartedAt
	job.FinishedAt = snap.FinishedAt
	job.TotalTurnDurationMs = snap.TotalTurnDurationMs
	job.TurnDurationPending = snap.TurnDurationPending
	job.SessionIDs = snap.SessionIDs
	job.Progress = snap.Progress
	job.LastRunOutcome = snap.LastRunOutcome
	job.ActiveClientMessageID = snap.ActiveClientMessageID
	job.ClientMessageReceipts = snap.ClientMessageReceipts
	job.MessageQueue = snap.MessageQueue
	job.MessageQueueVersion = snap.MessageQueueVersion
	job.MessageQueuePaused = snap.MessageQueuePaused
	job.MessageQueuePauseReason = snap.MessageQueuePauseReason
}

// restoreRunStateAfterPersistFailure rolls back only run-owned fields after
// a fail-fast send save fails. Handler-owned fields are left
// untouched so a concurrent targeted update (e.g. title edit) is not clobbered.
func (s *serviceImpl) restoreRunStateAfterPersistFailure(ctx context.Context, job *model.Job, snap jobRunStateSnapshot, action string, err error) {
	s.mu.Lock()
	restoreRunStateLocked(job, snap)
	jobID := job.ID
	s.mu.Unlock()
	logger.Warnf(ctx, "[job.Service] rolled back run state after persist failure: action=%s jobId=%s err=%v", action, jobID, err)
}
