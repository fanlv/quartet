package job

import (
	"context"
	"sync"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/safe"
	"github.com/fanlv/quartet/types/model"
)

// stopAndWaitTimeout bounds how long StopAndWait blocks waiting for a single
// runLoop goroutine to exit after cancellation. Generous because a single Stop
// is rare and a stuck goroutine here is much more important to surface than to
// silently drop on the floor; this becomes the upper bound on the cancellation
// signal propagating through context-aware code paths (LLM HTTP clients,
// streaming readers).
const stopAndWaitTimeout = 60 * time.Second

// stopAllPerJobTimeout is the per-job ceiling on graceful shutdown waits.
// Smaller than stopAndWaitTimeout because StopAll runs at process exit and
// budget is multiplied by the number of live jobs — a 60s cap with N=10 live
// jobs would block shutdown for up to 10 minutes. 10s is enough for a shell
// step to receive SIGTERM, run the 3s shellGracePeriod, get SIGKILL, and have
// cmd.Wait() return; longer-running cancellation paths fall back to logging
// the timeout and letting the OS reap them when the process exits.
const stopAllPerJobTimeout = 10 * time.Second

// Stop cancels a running job.
func (s *serviceImpl) Stop(jobID string) {
	// A hard Stop never reaches a graceful step boundary (it cancels the
	// context mid-step), so consumeGracefulStop won't run. Clear any pending
	// graceful-stop request here so an escalation from "stop after step" to
	// "stop now" doesn't leave a stale pending flag that Get would keep
	// synthesizing onto the stopped snapshot. (The launchLoop defer is the
	// catch-all for timeout/fail/panic paths; this closes the gap immediately
	// for the explicit-stop path.)
	s.clearGracefulStop(jobID)

	s.cancelMu.Lock()
	entry, ok := s.cancels[jobID]
	if ok {
		entry.cancel()
		delete(s.cancels, jobID)
	}
	s.cancelMu.Unlock()
}

// RequestGracefulStop marks a running loop job to stop at the next step
// boundary: the in-flight step runs to completion (records its result, advances
// resume) and then runFlowNodes returns instead of starting the next step. The
// job ends Stopped with Resume preserved, so Continue resumes cleanly from the
// next step — unlike the hard Stop, which cancels the context mid-step and
// re-runs the interrupted step on Continue.
//
// Best-effort: if the current step never finishes (e.g. a hung LLM call), this
// will not interrupt it — the user can escalate to a hard Stop. Idempotent.
//
// No-op unless the job currently has an active loop run that can consume the
// request. The active-run check and the pending write happen under the SAME
// runStateMu, so a run cannot exit (and drop its entry) between them: either
// the entry is present and we set its flag, or it's gone and we no-op. This is
// what keeps a non-running or interactive job from accumulating a stale pending
// flag that a later run would have to clear at launch.
func (s *serviceImpl) RequestGracefulStop(jobID string) {
	s.runStateMu.Lock()
	st, ok := s.runStates[jobID]
	if !ok {
		s.runStateMu.Unlock()
		return
	}
	alreadyPending := st.gracefulPending
	st.gracefulPending = true
	s.runStateMu.Unlock()
	if !alreadyPending {
		s.publishGracefulStopPending(jobID, true)
	}
}

// isGracefulStopPending reports whether a graceful stop is currently pending
// for jobID. Used to synthesize the runtime-only JobProgress.GracefulStopPending
// view field onto a Get snapshot.
func (s *serviceImpl) isGracefulStopPending(jobID string) bool {
	s.runStateMu.Lock()
	defer s.runStateMu.Unlock()
	st, ok := s.runStates[jobID]
	return ok && st.gracefulPending
}

// publishGracefulStopPending broadcasts the runtime-only graceful-stop pending
// state as a transient SSE event so other connected tabs update their "stop
// after step" / "keep running" affordance live. Transient (not buffered): the
// flag is never persisted, and a refresh re-reads it from the Get snapshot.
func (s *serviceImpl) publishGracefulStopPending(jobID string, pending bool) {
	s.PublishTransient(jobID, &model.CustomEvent{
		BaseEvent: model.BaseEvent{
			Type: model.EventTypeCustom, JobID: jobID,
			Timestamp: s.nowMillis(),
		},
		Name:  "graceful_stop_pending",
		Value: map[string]any{"pending": pending},
	})
}

// consumeGracefulStop reports whether a graceful stop was requested for jobID
// and clears the flag. Called by runFlowNodes at each step boundary.
func (s *serviceImpl) consumeGracefulStop(jobID string) bool {
	s.runStateMu.Lock()
	st, ok := s.runStates[jobID]
	consumed := ok && st.gracefulPending
	if consumed {
		st.gracefulPending = false
	}
	s.runStateMu.Unlock()
	if consumed {
		// The pending request is now consumed (the loop is stopping); tell
		// connected tabs so they drop the "keep running" affordance.
		s.publishGracefulStopPending(jobID, false)
	}
	return consumed
}

// CancelGracefulStop drops a pending graceful-stop request so the loop keeps
// running. Thin exported wrapper over clearGracefulStop for the HTTP handler;
// no-op if nothing is pending.
func (s *serviceImpl) CancelGracefulStop(jobID string) {
	s.clearGracefulStop(jobID)
}

// clearGracefulStop drops any pending graceful-stop request for jobID without
// removing the run entry itself. Called by Stop (hard-stop escalation) and
// CancelGracefulStop. Broadcasts the cleared state only when a request was
// actually pending so connected tabs drop the "keep running" affordance
// without spamming no-op events.
func (s *serviceImpl) clearGracefulStop(jobID string) {
	s.runStateMu.Lock()
	st, ok := s.runStates[jobID]
	wasPending := ok && st.gracefulPending
	if wasPending {
		st.gracefulPending = false
	}
	s.runStateMu.Unlock()
	if wasPending {
		s.publishGracefulStopPending(jobID, false)
	}
}

func (s *serviceImpl) IsGracefulStopSupported(jobID string) bool {
	s.runStateMu.Lock()
	defer s.runStateMu.Unlock()
	_, ok := s.runStates[jobID]
	return ok
}

// StopAndWait cancels a running job and blocks until its runLoop goroutine exits
// or stopAndWaitTimeout is reached.
func (s *serviceImpl) StopAndWait(jobID string) {
	s.Stop(jobID)

	s.doneMu.Lock()
	done := s.dones[jobID]
	s.doneMu.Unlock()

	if done != nil {
		select {
		case <-done:
		case <-time.After(stopAndWaitTimeout):
			logger.Errorf(context.Background(), "[job.Service] StopAndWait timed out for job %s", jobID)
		}
	}
}

// StopAll cancels all running jobs and waits for their goroutines to exit.
// Used during graceful shutdown.
func (s *serviceImpl) StopAll() {
	// Snapshot all cancel funcs under lock, then cancel outside lock.
	s.cancelMu.Lock()
	cancels := make(map[string]context.CancelFunc, len(s.cancels))
	for id, entry := range s.cancels {
		cancels[id] = entry.cancel
	}
	s.cancelMu.Unlock()

	for id, cancel := range cancels {
		logger.Infof(context.Background(), "[job.Service] StopAll: cancelling job %s", id)
		cancel()
	}

	// Wait for all runLoop goroutines to exit.
	s.doneMu.Lock()
	dones := make(map[string]chan struct{}, len(s.dones))
	for id, done := range s.dones {
		dones[id] = done
	}
	s.doneMu.Unlock()

	var wg sync.WaitGroup
	wg.Add(len(dones))
	for id, done := range dones {
		id, done := id, done
		safe.Go(context.Background(), func() {
			defer wg.Done()
			select {
			case <-done:
			case <-time.After(stopAllPerJobTimeout):
				logger.Errorf(context.Background(), "[job.Service] StopAll: timed out waiting for job %s", id)
			}
		})
	}
	wg.Wait()
}
