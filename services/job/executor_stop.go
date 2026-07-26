package job

import (
	"context"
	"sync"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/pkg/safe"
)

// stopAndWaitTimeout bounds how long StopAndWait blocks waiting for a single
// run goroutine to exit after cancellation. Generous because a single Stop
// is rare and a stuck goroutine here is much more important to surface than to
// silently drop on the floor; this becomes the upper bound on the cancellation
// signal propagating through context-aware code paths (LLM HTTP clients,
// streaming readers).
const stopAndWaitTimeout = 60 * time.Second

// stopAllPerJobTimeout is the per-job ceiling on graceful shutdown waits.
// Smaller than stopAndWaitTimeout because StopAll runs at process exit and
// budget is multiplied by the number of live jobs — a 60s cap with N=10 live
// jobs would block shutdown for up to 10 minutes. 10s is enough for the
// cancellation signal to propagate through context-aware code paths;
// longer-running cancellation paths fall back to logging
// the timeout and letting the OS reap them when the process exits.
const stopAllPerJobTimeout = 10 * time.Second

// Stop cancels a running job.
func (s *serviceImpl) Stop(jobID string) {
	s.cancelMu.Lock()
	entry, ok := s.cancels[jobID]
	if ok {
		entry.cancel()
		delete(s.cancels, jobID)
	}
	s.cancelMu.Unlock()
}

// StopAndWait cancels a running job and blocks until its run goroutine exits
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

	// Wait for all run goroutines to exit.
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
