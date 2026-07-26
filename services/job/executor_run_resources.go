package job

import (
	"context"
	"time"
)

// runResources groups the cancel/done tracking allocated for a single run.
// Splitting acquisition (in SendMessage, BEFORE saveJobWithRetry) from
// goroutine spawn (in SendMessage, AFTER saveJobWithRetry) closes the race
// where a Stop / Delete observed during the persist window would miss the
// cancel registration — which used to leak the goroutine and let it resurrect
// the job dir on disk after Delete cleaned it up.
type runResources struct {
	ctx   context.Context
	entry *cancelEntry
	done  chan struct{}
}

type cancelEntry struct {
	cancel context.CancelFunc
}

// prepareRunResources allocates and registers the cancel/done tracking for a
// new run. Pass timeoutMinutes > 0 to wrap the context with a deadline; pass 0
// for interactive sends.
func (s *serviceImpl) prepareRunResources(jobID string, timeoutMinutes int) *runResources {
	// Reset the per-job notifyJobDone dedup flag so a re-launched jobID
	// (SendMessage after a previous terminal run) can emit a
	// fresh done event. Interactive sends don't call notifyJobDone but we
	// clear here too to keep the invariant symmetric.
	s.clearJobDoneNotified(jobID)

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
	// a rapid Stop+SendMessage could have already replaced it.
	if s.dones[jobID] == done {
		delete(s.dones, jobID)
	}
	s.doneMu.Unlock()
}
