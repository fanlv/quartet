package job

import (
	"context"
	"hash/fnv"
	"sync"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/repository"
	"github.com/fanlv/quartet/services/script"
	"github.com/fanlv/quartet/services/usagestats"
	"github.com/fanlv/quartet/services/workspace"
	"github.com/fanlv/quartet/types/model"
)

// serviceImpl is the core job service implementation. It manages job CRUD,
// execution lifecycle, SSE pub/sub, and cancellation tracking.
//
// The implementation is split across files inside this package:
//
//   - executor_store.go      — struct, infrastructure helpers, load/store,
//     run-state snapshot/restore, OnJobDone wiring
//   - executor_crud.go       — Create, Get, List*, Delete, pagination
//   - executor_mutators.go   — targeted single-field updaters (UpdateTitle,
//     MarkDeleted, EnsureShareToken,
//     ClearShareToken, SetFirstModelID)
//   - executor_persist.go    — saveJobWithRetry + recordPersistWarning
//   - executor_run.go        — Start / Continue / SendMessage / Stop
//   - executor_loop.go       — loop iteration scheduling
//   - executor_step.go       — per-step execution and var extraction
//   - executor_state.go      — terminal status transitions
//   - executor_pubsub.go     — SSE event bus
//   - executor_shell.go      — shell-step plumbing
//
// # Field ownership model
//
// The internal *model.Job pointer stored in s.jobs is shared between the
// handler goroutine (via targeted mutators) and the runLoop goroutine. To
// avoid data races, fields are split by ownership:
//
//   - Handler-owned (mutated by targeted methods): Title, Mode, Workdir, ShareToken, Deleted
//   - RunLoop-owned: Status, StartedAt, FinishedAt, LoopConfig, Progress, Resume, SessionIDs
//   - Service-owned denormalized cache (targeted mutator only): FirstModelID
//
// All reads and writes of job fields MUST hold s.mu. Targeted mutators
// (UpdateTitle, EnsureShareToken, ClearShareToken, SetFirstModelID,
// MarkDeleted) only touch their named field plus UpdatedAt, so they
// cannot race with runLoop-owned writes on the same pointer.
//
// Note: LoopConfig.Variables may also be written by handler-side methods
// (e.g. UpdateTitle syncs VarJobTitle) as a targeted key update under s.mu.
// This is safe and intentional — the ownership rule prevents wholesale
// replacement of LoopConfig from a stale snapshot, not individual key updates.
//
// # Invariants
//
//   - Progress is non-nil for every Job in s.jobs. ensureProgress establishes
//     this at the two entry points (load on startup, Create) so internal
//     paths can dereference Progress without per-call nil guards.
//
// # Lock ordering
//
// The mutexes in this struct protect independent data and must NEVER be
// nested (no lock should be held while acquiring another). Each method should
// acquire at most one lock, release it, then optionally acquire another. The
// only exception is persistence: methods that snapshot and save a Job take
// the per-job persist shard first, then s.mu, so disk writes preserve the
// same order as the in-memory snapshots they persist.
//
// SSE pub/sub has its own internal lock graph encapsulated in *eventBus —
// from serviceImpl's perspective the bus is a black box with method calls.
type serviceImpl struct {
	jobs map[string]*model.Job
	mu   sync.RWMutex
	// persistShards serializes (DeepCopy + repo.Save) on a per-job basis.
	// Sharded by FNV-1a(jobID) so saves on different jobs don't block each
	// other — the previous global persistMu serialized every job's saves
	// across the whole process, which became visible when several scheduled
	// loops fanned out at the same time. Within a single job the order is
	// still preserved (an older best-effort save cannot overtake a newer
	// handler-side mutation), which is the actual invariant we need.
	persistShards [persistShardCount]sync.Mutex

	// per-workspace repos: wsID -> JobRepo
	repos  map[string]repository.JobRepo
	repoMu sync.RWMutex
	wsSvc  workspace.Service

	// script service for loading shell scripts
	scriptSvc script.Service

	// pub/sub: per-job append-only event buffer + cursor readers. Owns its
	// own mutex graph internally; serviceImpl exposes Publish / Subscribe /
	// PublishTransient as thin proxies and routes resetForRun / remove from
	// the lifecycle paths (Start / Delete) directly.
	bus *busOwner

	// stop control: jobID -> cancel entry. The entry identity is used during
	// deferred cleanup so an old run cannot delete a newer run's cancel func.
	cancels  map[string]*cancelEntry
	cancelMu sync.Mutex

	// runLoop tracking: jobID -> done channel (closed when runLoop exits)
	dones  map[string]chan struct{}
	doneMu sync.Mutex

	// Tracks the job status prior to an interactive SendMessage flipping it to
	// Running. When the interactive run terminates (stop/finish/fail), the
	// prior terminal state is restored so an ad-hoc message cannot regress a
	// Completed/Failed/Stopped loop into a different state.
	//
	// Protected by s.mu — the consume path is reached from
	// applyTerminalStatusLocked which already holds s.mu, and folding this
	// rarely-touched map into s.mu avoids an mu→other nested-lock that the
	// "must NEVER be nested" invariant on this struct otherwise forbids.
	interactivePriorStatus map[string]model.JobStatus

	// optional callback when a loop job finishes
	onJobDone   func(job *model.Job)
	onJobDoneMu sync.RWMutex

	// Tracks job IDs that have already fired notifyJobDone so a duplicate
	// trigger (stopJob → finishJob race, or an external state-transition bug)
	// cannot emit two `[scheduler] done:` lines / two MarkDone calls for the
	// same run. Cleared in Create so a re-launched jobID (e.g. Continue after
	// Stopped) can fire again.
	notifiedJobs   map[string]struct{}
	notifiedJobsMu sync.Mutex

	// runStates tracks per-job loop-run state behind a single mutex so the
	// "is there an active loop run?" check and the "mark a graceful stop
	// pending" write are one atomic step. Splitting these across two mutexes
	// (the previous loopRunsMu + gracefulStopsMu) left a window where
	// RequestGracefulStop could observe an active run, the run could then exit
	// and clear its (still-empty) pending flag, and the request would write a
	// pending flag onto a job with no active run left to consume it — a stale
	// flag that Get would keep synthesizing onto the terminal snapshot.
	//
	// An entry exists only while a loop run is active (markLoopRun on launch,
	// clearLoopRun on exit). The gracefulPending flag is meaningful only on an
	// existing entry; clearing the entry necessarily clears any pending flag,
	// so a non-active job can never carry one.
	runStates  map[string]*loopRunState
	runStateMu sync.Mutex

	// Monotonic per-workspace list version; incremented on every job mutation
	// that would affect the listing (create/delete/save/status change). Used to
	// build ETags for conditional GETs on the list endpoint. A workspace with no
	// mutations since startup stays at 0. The "" key holds the global counter
	// (used by the workspace-less /job/list variant).
	wsListVersion   map[string]int64
	wsListVersionMu sync.Mutex

	// usageRecorder is the optional usage-stats sink. When set, every
	// completed step (interactive round / loop iteration / shell step)
	// submits a Snapshot with timings, counts and tokens. Nil means
	// stats are simply not recorded — the executor never depends on it
	// returning anything.
	usageRecorder usagestats.Recorder
}

// bumpListVersion increments both the workspace-scoped and global list versions
// so clients polling either the workspace-filtered or unfiltered endpoint see a
// changed ETag. Safe to call under any lock — takes its own mutex.
func (s *serviceImpl) bumpListVersion(wsID string) {
	s.wsListVersionMu.Lock()
	s.wsListVersion[wsID]++
	s.wsListVersion[""]++
	s.wsListVersionMu.Unlock()
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
	s.wsListVersionMu.Lock()
	v := s.wsListVersion[wsID]
	s.wsListVersionMu.Unlock()
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

// shouldPreservePriorStatus reports whether a job's pre-SendMessage status
// should be restored after the interactive run ends. Completed/Failed are
// loop terminal outcomes that ad-hoc messages must not regress. Stopped is
// only worth preserving when the job has a Resume (a paused loop where
// Continue should still work) — a Stopped chat job (Resume==nil) should
// instead be promoted by the new send's outcome.
func shouldPreservePriorStatus(s model.JobStatus, resume *model.JobResume) bool {
	if !isTerminalJobStatus(s) {
		return false
	}
	if s == model.JobStatusStopped && resume == nil {
		return false
	}
	return true
}

func (s *serviceImpl) SetOnJobDone(fn func(job *model.Job)) {
	s.onJobDoneMu.Lock()
	s.onJobDone = fn
	s.onJobDoneMu.Unlock()
}

func (s *serviceImpl) notifyJobDone(job *model.Job) {
	// Idempotency: finishJob/stopJob/failJob each call notifyJobDone, and the
	// runLoop defer is guarded against calling >1 of them in a single run —
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
		// still being mutated by the runLoop goroutine that called us
		// (terminal Status was written under lock and unlocked just before
		// notifyJobDone fired) and may race with a concurrent Start /
		// SendMessage that flips it back to Running.
		s.mu.RLock()
		cp := job.DeepCopy()
		s.mu.RUnlock()
		fn(cp)
	}
}

// clearJobDoneNotified removes the dedup flag so a fresh launch of the same
// jobID (Continue after Stopped, Start after previous terminal) can fire the
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
	repo, err := repository.NewJobRepo(wsID)
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
		jobs, err := repo.LoadAll()
		if err != nil {
			logger.Errorf(ctx, "[job.Service] load jobs failed: workspace=%s err=%v", ws.ID, err)
			continue
		}
		for _, j := range jobs {
			if j.Deleted {
				continue
			}
			// Establish the in-memory invariant: every Job in s.jobs has a
			// non-nil Progress. Pre-LoopConfig legacy records persisted with
			// Progress=nil, so we lazy-init here once at load — every other
			// access path can then dereference Progress without a guard.
			ensureProgress(j)
			// Reset running jobs to failed on startup (they were interrupted)
			if j.Status == model.JobStatusRunning {
				j.Status = model.JobStatusFailed
				j.Progress.LastError = "interrupted: process restarted while running"
				if err := repo.Save(j.ID, j); err != nil {
					logger.Errorf(ctx, "[job.Service] reset running->failed persist failed: workspace=%s jobId=%s err=%v", ws.ID, j.ID, err)
				}
			}
			// Clear Content from older loaded results to save memory.
			// Preserve the LAST result's Content so injectPerRoundVars can
			// populate _last_assistant_msg after a process restart + Continue
			// — otherwise the variable resolves to empty AND the next save
			// overwrites disk with the empty value, permanently losing the
			// data. Mirrors the in-memory invariant maintained by
			// appendAndSaveResult (only the latest result keeps Content).
			if n := len(j.Progress.Results); n > 1 {
				for i := 0; i < n-1; i++ {
					j.Progress.Results[i].Content = ""
				}
			}
			// Prefill FirstModelID for legacy jobs so the list endpoint never
			// has to open session metadata files at request time (avoids the
			// N+1 I/O hit on first listing after an upgrade). This runs once
			// at startup and is idempotent — if already cached we skip the
			// repo load.
			if j.FirstModelID == "" && len(j.SessionIDs) > 0 {
				if srepo, err := repository.NewSessionRepo(j.WorkspaceID, j.ID); err == nil {
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
			}
			s.store(j.ID, j)
			loadedJobs++
		}
	}
	logger.Infof(ctx, "[job.Service] startup load done: workspaces=%d jobs=%d durMs=%d", len(workspaces), loadedJobs, time.Since(start).Milliseconds())
}

func (s *serviceImpl) store(jobID string, job *model.Job) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.jobs[jobID] = job
}

// jobRunStateSnapshot captures the runLoop-owned subset of fields used to roll
// back a fail-fast persist failure in Start / Continue / SendMessage. Handler-
// owned fields are intentionally excluded so a concurrent targeted update
// (e.g. UpdateTitle) is not clobbered by the rollback.
type jobRunStateSnapshot struct {
	Status     model.JobStatus
	StartedAt  int64
	FinishedAt int64
	LoopConfig *model.LoopConfig
	SessionIDs []string
	Progress   *model.JobProgress
	Resume     *model.JobResume
}

// snapshotRunStateLocked captures the runLoop-owned fields that may be
// mutated before a recovery-critical save. Caller must hold s.mu.
func snapshotRunStateLocked(job *model.Job) jobRunStateSnapshot {
	cp := job.DeepCopy()
	return jobRunStateSnapshot{
		Status:     cp.Status,
		StartedAt:  cp.StartedAt,
		FinishedAt: cp.FinishedAt,
		LoopConfig: cp.LoopConfig,
		SessionIDs: cp.SessionIDs,
		Progress:   cp.Progress,
		Resume:     cp.Resume,
	}
}

// restoreRunStateLocked restores runLoop-owned fields from a snapshot. Caller
// must hold s.mu.
func restoreRunStateLocked(job *model.Job, snap jobRunStateSnapshot) {
	job.Status = snap.Status
	job.StartedAt = snap.StartedAt
	job.FinishedAt = snap.FinishedAt
	job.LoopConfig = snap.LoopConfig
	job.SessionIDs = snap.SessionIDs
	job.Progress = snap.Progress
	job.Resume = snap.Resume
}

// restoreRunStateAfterPersistFailure rolls back only runLoop-owned fields after
// a fail-fast start/continue/send save fails. Handler-owned fields are left
// untouched so a concurrent targeted update (e.g. title edit) is not clobbered.
func (s *serviceImpl) restoreRunStateAfterPersistFailure(ctx context.Context, job *model.Job, snap jobRunStateSnapshot, action string, err error) {
	s.mu.Lock()
	restoreRunStateLocked(job, snap)
	jobID := job.ID
	s.mu.Unlock()
	logger.Warnf(ctx, "[job.Service] rolled back run state after persist failure: action=%s jobId=%s err=%v", action, jobID, err)
}
