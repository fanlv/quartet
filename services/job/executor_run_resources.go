package job

import (
	"context"
	"time"

	"github.com/fanlv/quartet/pkg/safe"
	"github.com/fanlv/quartet/types/model"
)

// defaultScheduleTimeoutMinutes is the fallback timeout for scheduled jobs that
// don't have an explicit timeout set. Prevents runaway shell scripts from leaking
// resources indefinitely when no one is around to manually stop them.
const defaultScheduleTimeoutMinutes = 120

// loopTimeoutMinutes returns the runLoop context timeout for a job, applying
// the scheduled-job default when the job has a ScheduleID but no explicit
// timeout. ScheduleID and TimeoutMinutes are immutable after job creation,
// so no lock is required.
func loopTimeoutMinutes(job *model.Job) int {
	timeout := job.TimeoutMinutes
	if timeout == 0 && job.ScheduleID != "" {
		timeout = defaultScheduleTimeoutMinutes
	}
	return timeout
}

// runResources groups the cancel/done tracking allocated for a single run.
// Splitting acquisition (in Start / Continue / SendMessage, BEFORE
// saveJobWithRetry) from goroutine spawn (in launchLoop / SendMessage,
// AFTER saveJobWithRetry) closes the race where a Stop / Delete observed
// during the persist window would miss the cancel registration — which used
// to leak the goroutine and let it resurrect the job dir on disk after
// Delete cleaned it up.
type runResources struct {
	ctx   context.Context
	entry *cancelEntry
	done  chan struct{}
}

// loopRunState is the per-job loop-run state guarded by runStateMu. An entry
// exists in runStates only while a loop run is active; gracefulPending is
// meaningful only on an existing entry, so clearing the entry (run exit) is
// what guarantees a non-active job can never carry a pending flag.
type loopRunState struct {
	gracefulPending bool
}

type cancelEntry struct {
	cancel context.CancelFunc
}

// prepareRunResources allocates and registers the cancel/done tracking for a
// new run. Pass timeoutMinutes > 0 to wrap the context with a deadline (used
// for scheduled loops); pass 0 for interactive sends and unbounded loops.
func (s *serviceImpl) prepareRunResources(jobID string, timeoutMinutes int, loopRun bool) *runResources {
	// Reset the per-job notifyJobDone dedup flag so a re-launched jobID
	// (Continue after Stopped, Start after a previous terminal) can emit a
	// fresh done event. Interactive sends don't call notifyJobDone but we
	// clear here too to keep the invariant symmetric.
	s.clearJobDoneNotified(jobID)
	// Drop any stale graceful-stop request so it can't immediately stop a
	// freshly started / continued run before its first step boundary.
	s.clearGracefulStop(jobID)
	if loopRun {
		s.markLoopRun(jobID)
	} else {
		s.clearLoopRun(jobID)
	}

	var ctx context.Context
	var cancel context.CancelFunc
	if timeoutMinutes > 0 {
		ctx, cancel = context.WithTimeout(context.Background(), time.Duration(timeoutMinutes)*time.Minute)
	} else {
		ctx, cancel = context.WithCancel(context.Background())
	}
	return &runResources{
		ctx:   ctx,
		entry: s.registerCancel(jobID, cancel),
		done:  s.registerDone(jobID),
	}
}

// abortRunResources rolls back a prepareRunResources call when the pre-launch
// persist failed and no goroutine will be spawned. clearCancel and cleanupDone
// guard their map deletes by entry / channel identity, so this is safe to call
// even if a concurrent Stop already cancelled and dropped the entry.
func (s *serviceImpl) abortRunResources(jobID string, res *runResources) {
	s.clearCancel(jobID, res.entry)
	s.cleanupDone(jobID, res.done)
	s.clearLoopRun(jobID)
}

// launchLoop spawns the runLoop goroutine using cancel/done resources that
// were already registered (before the persist barrier) by prepareRunResources.
func (s *serviceImpl) launchLoop(job *model.Job, runner JobRunner, cfg *model.LoopConfig, res *runResources) {
	safe.Go(res.ctx, func() {
		// clearLoopRun removes the run entry on exit for ANY reason (completion,
		// failure, timeout, panic, hard cancel) and broadcasts a cleared
		// graceful-stop state if one was still pending — so an abnormal exit
		// can't leave a stale flag that Get keeps synthesizing onto the terminal
		// snapshot. consumeGracefulStop handles the clean step-boundary case.
		defer s.clearLoopRun(job.ID)
		defer s.cleanupDone(job.ID, res.done)
		s.runLoop(res.ctx, job, runner, cfg, res.entry)
	})
}

// markLoopRun records that a loop run is active for jobID, creating the
// runStates entry that RequestGracefulStop / consumeGracefulStop key off of.
func (s *serviceImpl) markLoopRun(jobID string) {
	s.runStateMu.Lock()
	if s.runStates == nil {
		s.runStates = make(map[string]*loopRunState)
	}
	if _, ok := s.runStates[jobID]; !ok {
		s.runStates[jobID] = &loopRunState{}
	}
	s.runStateMu.Unlock()
}

// clearLoopRun removes the run entry for jobID when the loop run exits. Removing
// the entry necessarily drops any pending graceful-stop flag it held; if a flag
// was pending, broadcast the cleared state so connected tabs drop the "keep
// running" affordance. Because the active-run check and the pending write in
// RequestGracefulStop share runStateMu, no request can slip a pending flag onto
// this jobID after the entry is gone.
func (s *serviceImpl) clearLoopRun(jobID string) {
	s.runStateMu.Lock()
	st, ok := s.runStates[jobID]
	wasPending := ok && st.gracefulPending
	delete(s.runStates, jobID)
	s.runStateMu.Unlock()
	if wasPending {
		s.publishGracefulStopPending(jobID, false)
	}
}

func (s *serviceImpl) registerCancel(jobID string, cancel context.CancelFunc) *cancelEntry {
	entry := &cancelEntry{cancel: cancel}
	s.cancelMu.Lock()
	s.cancels[jobID] = entry
	s.cancelMu.Unlock()
	return entry
}

func (s *serviceImpl) registerDone(jobID string) chan struct{} {
	done := make(chan struct{})
	s.doneMu.Lock()
	s.dones[jobID] = done
	s.doneMu.Unlock()
	return done
}

func (s *serviceImpl) cleanupDone(jobID string, done chan struct{}) {
	close(done)
	s.doneMu.Lock()
	// Only remove if the map still points to our channel;
	// a rapid Stop+Start could have already replaced it.
	if s.dones[jobID] == done {
		delete(s.dones, jobID)
	}
	s.doneMu.Unlock()
}
