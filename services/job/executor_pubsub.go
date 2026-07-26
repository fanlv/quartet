package job

import "sync"

// busOwner is the per-job event buffer registry that serviceImpl exposes
// to publishers and SSE subscribers. The buffer's GC and cursor model
// guarantee:
//
//   - publishers are never blocked by slow subscribers
//   - non-terminal events are never silently dropped
//   - reconnects resume from Last-Event-ID without duplication
//
// See docs/arch/sse-event-buffer-design.md.
type busOwner struct {
	mu      sync.RWMutex
	buffers map[string]*jobEventBuffer
}

func newBusOwner() *busOwner {
	return &busOwner{buffers: make(map[string]*jobEventBuffer)}
}

// getOrCreate returns the buffer for jobID, lazily creating one if absent.
// Returns nil only if the buffer was previously closed via Remove and the
// caller is racing a concurrent Delete — Publish in that case is a no-op.
func (b *busOwner) getOrCreate(jobID string) *jobEventBuffer {
	b.mu.RLock()
	if buf, ok := b.buffers[jobID]; ok {
		b.mu.RUnlock()
		return buf
	}
	b.mu.RUnlock()

	b.mu.Lock()
	defer b.mu.Unlock()
	if buf, ok := b.buffers[jobID]; ok {
		return buf
	}
	buf := newJobEventBuffer(jobID)
	b.buffers[jobID] = buf
	return buf
}

// get returns the buffer for jobID, or nil if none exists.
func (b *busOwner) get(jobID string) *jobEventBuffer {
	b.mu.RLock()
	defer b.mu.RUnlock()
	return b.buffers[jobID]
}

// remove closes and forgets the buffer for jobID. Called from Delete.
func (b *busOwner) remove(jobID string) {
	b.mu.Lock()
	buf := b.buffers[jobID]
	delete(b.buffers, jobID)
	b.mu.Unlock()
	if buf != nil {
		buf.Close()
	}
}

// resumeGC re-enables GC on a buffer that was previously marked terminal.
// SendMessage on a terminal job reuses the same buffer; without resuming GC
// the prior run's MarkTerminal would keep gcLocked short-circuited for the
// entire new run.
func (b *busOwner) resumeGC(jobID string) {
	if buf := b.get(jobID); buf != nil {
		buf.ResumeGC()
	}
}

// Subscribe creates a reader for jobID resuming from startSeq+1. The
// buffer is lazily created so a brand-new job that has not yet published
// can still attach a subscriber (it just blocks until the first event).
// Returns ErrSeqGone when startSeq is older than the buffer's GC head.
func (s *serviceImpl) Subscribe(jobID string, startSeq uint64) (*Reader, error) {
	buf := s.bus.getOrCreate(jobID)
	return buf.Subscribe(startSeq)
}

// SnapshotSeq returns the resume point a fresh subscriber should use as
// Last-Event-ID after fetching the snapshot. Returns 0 if no events have
// been published yet for this job.
func (s *serviceImpl) SnapshotSeq(jobID string) uint64 {
	buf := s.bus.get(jobID)
	if buf == nil {
		return 0
	}
	return buf.SnapshotSeq()
}

// BufferStats returns a snapshot of the per-job event buffer state for
// observability. Returns the zero value if the buffer for jobID has not
// been created yet (job has not published any event).
func (s *serviceImpl) BufferStats(jobID string) BufferStats {
	buf := s.bus.get(jobID)
	if buf == nil {
		return BufferStats{}
	}
	return buf.Stats()
}

// Publish appends an event to the per-job buffer. Errors from the buffer
// (closed during job deletion) are intentionally swallowed at this layer
// — the publisher cannot meaningfully recover from a deleted job, and
// the read side is already gone.
func (s *serviceImpl) Publish(jobID string, event any) {
	buf := s.bus.getOrCreate(jobID)
	buf.publishClassified(event)
}

// PublishTransient delivers an event to currently connected readers
// without buffering. Used for slash-command results that must not
// reappear on page refresh.
func (s *serviceImpl) PublishTransient(jobID string, event any) {
	if buf := s.bus.get(jobID); buf != nil {
		buf.PublishTransient(event)
	}
}

// isTerminalEvent returns true for events that mark a job's final
// transition. Buffer GC is disabled after a terminal event so refreshing
// a finished job's page still shows the last round of chunks.
func isTerminalEvent(event any) bool {
	return classifyEvent(event).isTerminal
}
