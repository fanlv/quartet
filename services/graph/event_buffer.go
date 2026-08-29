package graph

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
)

// ErrSeqGone is returned by Subscribe when the requested resume sequence has
// already been GC'd from the buffer. The SSE handler maps this to HTTP 410 so
// the client falls back to its reconcile (re-fetch run status) path.
var ErrSeqGone = errors.New("graph event buffer: requested sequence has been garbage-collected")

// Buffer tuning. Mirrors the job event buffer (services/job/event_buffer.go);
// graph has no "round" concept, so there is no per-round GC — only the global
// cursor-crossing rule plus the four safety nets below.
const (
	// replayGapLimit is the max number of events a new subscriber may replay on
	// connect. Beyond it, Subscribe returns ErrSeqGone so the client reconciles
	// from the run snapshot instead of replaying a storm of buffered events.
	graphReplayGapLimit = 1000
	// bufferHardcap bounds buffer memory: once exceeded, already-consumed events
	// are force-evicted (and the slowest reader dropped if none are consumable).
	graphBufferHardcap = 50000
	// gcGracePeriod defers GC briefly after the last reader leaves mid-run, so a
	// reconnecting client does not immediately hit ErrSeqGone.
	graphGCGracePeriod = 30 * time.Second
	// gcSuspensionTimeout auto-expires a reader-less GC suspension so a
	// permanently absent reader cannot block GC forever.
	graphGCSuspensionTimeout = 5 * time.Minute
)

// bufferedGraphEvent is one event held in the ring, tagged with its assigned
// sequence and a release flag the GC uses to drop a contiguous head prefix.
type bufferedGraphEvent struct {
	seq      uint64
	event    *model.GraphEvent
	released bool
}

// graphEventBuffer is a per-run append-only event log with multi-cursor readers
// and cursor-based GC. It is the in-memory transport for a live GraphRun's SSE
// stream: streaming agent events (message/thought/tool deltas) flow ONLY through
// here and are never persisted, while structural events are both persisted (for
// resume/audit) and published here for real-time delivery.
//
// Unlike the job buffer it has no round boundaries and no A/B classification:
// every event is reclaimable as soon as all reader cursors cross it (subject to
// the terminal / grace-period / suspension / hardcap guards).
type graphEventBuffer struct {
	runID string

	mu   sync.Mutex
	cond *sync.Cond

	events  []bufferedGraphEvent
	nextSeq uint64 // seq assigned to the most recent event; first event gets seq=1
	headSeq uint64 // seq of the most recently GC'd event (events <= headSeq are gone)

	readers map[*graphReader]struct{}

	closed   bool // buffer released (run deleted)
	terminal bool // run reached a terminal state — stop GC, keep tail for late refreshes

	// gcSuspendedUntilReader defers GC after ResumeGC() until the first reader
	// reconnects, so headSeq does not advance (minCursor=MaxUint64 with no
	// readers) and reject the reconnecting client with ErrSeqGone.
	gcSuspendedUntilReader bool
	gcSuspendedAt          time.Time

	// lastReaderLeftAt records when the last reader disconnected mid-run, so
	// gcLocked can defer reclamation for graphGCGracePeriod.
	lastReaderLeftAt time.Time

	warnedAt10k bool
	warnedAt25k bool

	gcRunCount       uint64
	gcReleasedEvents uint64
}

func newGraphEventBuffer(runID string) *graphEventBuffer {
	b := &graphEventBuffer{
		runID:   runID,
		readers: make(map[*graphReader]struct{}),
	}
	b.cond = sync.NewCond(&b.mu)
	return b
}

// Publish appends one event, assigns it a monotonic seq, wakes readers, and runs
// a GC pass. Returns the assigned seq (>= 1), or 0 if the buffer was already
// closed (run deleted while a producer was in flight — drop silently).
func (b *graphEventBuffer) Publish(event *model.GraphEvent) uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return 0
	}
	b.nextSeq++
	seq := b.nextSeq
	b.events = append(b.events, bufferedGraphEvent{seq: seq, event: event})
	b.cond.Broadcast()
	b.gcLocked()
	return seq
}

// MarkTerminal flips the buffer into "no GC" mode so the tail stays available
// for late refreshes until the run is explicitly deleted. Idempotent.
func (b *graphEventBuffer) MarkTerminal() {
	b.mu.Lock()
	b.terminal = true
	b.mu.Unlock()
}

// ResumeGC flips a previously terminal buffer back into "GC active" mode for a
// resumed run that reuses the same buffer. When no readers are connected, GC is
// suspended until the first Subscribe() so a reconnecting client is not rejected
// with ErrSeqGone. Idempotent.
func (b *graphEventBuffer) ResumeGC() {
	b.mu.Lock()
	b.terminal = false
	if len(b.readers) == 0 {
		b.gcSuspendedUntilReader = true
		b.gcSuspendedAt = time.Now()
		logger.Infof(context.Background(),
			"[graph-buffer] resumeGC: no active readers, suspending GC until first subscriber (timeout=%v), runID=%s headSeq=%d nextSeq=%d",
			graphGCSuspensionTimeout, b.runID, b.headSeq, b.nextSeq)
	}
	b.mu.Unlock()
}

// Close releases the buffer and wakes all readers. Called when the run is
// deleted. After Close, Publish is a no-op and readers see ok=false from Read.
func (b *graphEventBuffer) Close() {
	b.mu.Lock()
	b.closed = true
	b.events = nil
	b.cond.Broadcast()
	b.mu.Unlock()
}

// SnapshotSeq returns the seq a brand-new SSE subscriber should resume from.
// Graph has no in-flight round to rewind into — already-flushed agent
// conversation is recoverable from each node's session messages.jsonl — so a
// fresh subscriber simply starts at the current tail.
func (b *graphEventBuffer) SnapshotSeq() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.nextSeq
}

// Subscribe creates a reader cursored at startSeq. Returns ErrSeqGone when the
// resume point is outside the buffer's live sequence space [headSeq, nextSeq]
// or further behind than graphReplayGapLimit — the SSE handler maps that to 410.
func (b *graphEventBuffer) Subscribe(startSeq uint64) (*graphReader, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, ErrSeqGone
	}
	if startSeq < b.headSeq {
		logger.Warnf(context.Background(),
			"[graph-buffer] subscribe rejected: startSeq < headSeq (events already GC'd), runID=%s startSeq=%d headSeq=%d nextSeq=%d readers=%d",
			b.runID, startSeq, b.headSeq, b.nextSeq, len(b.readers))
		return nil, ErrSeqGone
	}
	if startSeq > b.nextSeq {
		logger.Warnf(context.Background(),
			"[graph-buffer] subscribe rejected: startSeq > nextSeq (seq from the future), runID=%s startSeq=%d nextSeq=%d headSeq=%d",
			b.runID, startSeq, b.nextSeq, b.headSeq)
		return nil, ErrSeqGone
	}
	if b.nextSeq-startSeq > graphReplayGapLimit {
		logger.Warnf(context.Background(),
			"[graph-buffer] subscribe rejected: replay gap too large, runID=%s nextSeq=%d startSeq=%d gap=%d limit=%d",
			b.runID, b.nextSeq, startSeq, b.nextSeq-startSeq, graphReplayGapLimit)
		return nil, ErrSeqGone
	}
	r := &graphReader{buf: b, cursor: startSeq}
	b.readers[r] = struct{}{}
	b.lastReaderLeftAt = time.Time{}
	if b.gcSuspendedUntilReader {
		b.gcSuspendedUntilReader = false
		logger.Infof(context.Background(),
			"[graph-buffer] GC suspension lifted: first reader connected after resumeGC, runID=%s startSeq=%d headSeq=%d nextSeq=%d",
			b.runID, startSeq, b.headSeq, b.nextSeq)
	}
	return r, nil
}

// Len returns the number of events physically held in the buffer.
func (b *graphEventBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.events)
}

// GraphBufferStats is a read-only snapshot of a buffer's runtime state for
// observability. Mirrors job.BufferStats.
type GraphBufferStats struct {
	Events           int
	Readers          int
	HeadSeq          uint64
	NextSeq          uint64
	MaxLagSeq        uint64
	GCRunCount       uint64
	GCReleasedEvents uint64
	Terminal         bool
	Closed           bool
}

// Stats returns a snapshot of the buffer's current state.
func (b *graphEventBuffer) Stats() GraphBufferStats {
	b.mu.Lock()
	defer b.mu.Unlock()
	stats := GraphBufferStats{
		Events:           len(b.events),
		Readers:          len(b.readers),
		HeadSeq:          b.headSeq,
		NextSeq:          b.nextSeq,
		GCRunCount:       b.gcRunCount,
		GCReleasedEvents: b.gcReleasedEvents,
		Terminal:         b.terminal,
		Closed:           b.closed,
	}
	for r := range b.readers {
		if r.cursor < b.nextSeq {
			if lag := b.nextSeq - r.cursor; lag > stats.MaxLagSeq {
				stats.MaxLagSeq = lag
			}
		}
	}
	return stats
}

// gcLocked drops the contiguous head prefix of events that all readers have
// crossed. Guards (in order): terminal stops GC; a reader-less suspension after
// ResumeGC defers until the first subscriber (auto-expiring after
// graphGCSuspensionTimeout); a mid-run grace period after the last reader
// leaves; and a hardcap force-eviction that bounds memory. Caller holds b.mu.
func (b *graphEventBuffer) gcLocked() {
	if b.terminal {
		return
	}
	if b.gcSuspendedUntilReader && len(b.readers) == 0 && len(b.events) <= graphBufferHardcap {
		if time.Since(b.gcSuspendedAt) < graphGCSuspensionTimeout {
			return
		}
		b.gcSuspendedUntilReader = false
		logger.Warnf(context.Background(),
			"[graph-buffer] GC suspension auto-expired after %v without any reader connecting: runID=%s headSeq=%d nextSeq=%d bufLen=%d",
			graphGCSuspensionTimeout, b.runID, b.headSeq, b.nextSeq, len(b.events))
	}
	if len(b.readers) == 0 && !b.lastReaderLeftAt.IsZero() && len(b.events) <= graphBufferHardcap {
		if time.Since(b.lastReaderLeftAt) < graphGCGracePeriod {
			return
		}
	}

	minCursor := b.minCursorLocked()

	bufLen := len(b.events)
	if bufLen >= 10000 && !b.warnedAt10k {
		b.warnedAt10k = true
		logger.Warnf(context.Background(),
			"[graph-buffer] buffer growing large (threshold=10k): runID=%s bufLen=%d headSeq=%d nextSeq=%d readers=%d gcRuns=%d gcReleased=%d",
			b.runID, bufLen, b.headSeq, b.nextSeq, len(b.readers), b.gcRunCount, b.gcReleasedEvents)
	}
	if bufLen >= 25000 && !b.warnedAt25k {
		b.warnedAt25k = true
		logger.Warnf(context.Background(),
			"[graph-buffer] buffer growing large (threshold=25k): runID=%s bufLen=%d headSeq=%d nextSeq=%d readers=%d gcRuns=%d gcReleased=%d",
			b.runID, bufLen, b.headSeq, b.nextSeq, len(b.readers), b.gcRunCount, b.gcReleasedEvents)
	}

	// Mark release-eligible: every event whose seq all readers have crossed.
	for i := range b.events {
		e := &b.events[i]
		if e.released || e.seq > minCursor {
			continue
		}
		e.released = true
	}

	// Hardcap eviction: when over the limit, force-release consumed events; if
	// none are consumable, evict the slowest reader to unblock GC (OOM guard).
	if len(b.events) > graphBufferHardcap {
		forced := 0
		for i := range b.events {
			e := &b.events[i]
			if e.seq > minCursor {
				break
			}
			if e.released {
				continue
			}
			e.released = true
			forced++
		}
		if forced > 0 {
			logger.Warnf(context.Background(),
				"[graph-buffer] hardcap eviction triggered: runID=%s bufLen=%d hardcap=%d forceReleased=%d minCursor=%d headSeq=%d",
				b.runID, len(b.events), graphBufferHardcap, forced, minCursor, b.headSeq)
		} else {
			var stuck *graphReader
			var stuckCursor uint64 = ^uint64(0)
			for r := range b.readers {
				if r.cursor < stuckCursor {
					stuckCursor = r.cursor
					stuck = r
				}
			}
			if stuck != nil {
				logger.Errorf(context.Background(),
					"[graph-buffer] hardcap exceeded, evicting stuck reader: runID=%s bufLen=%d hardcap=%d readerCursor=%d headSeq=%d nextSeq=%d lag=%d readers=%d",
					b.runID, len(b.events), graphBufferHardcap, stuckCursor, b.headSeq, b.nextSeq, b.nextSeq-stuckCursor, len(b.readers))
				stuck.closed = true
				delete(b.readers, stuck)
				b.cond.Broadcast()
				newMin := b.minCursorLocked()
				for i := range b.events {
					e := &b.events[i]
					if e.seq > newMin {
						break
					}
					e.released = true
				}
			} else {
				logger.Errorf(context.Background(),
					"[graph-buffer] hardcap exceeded but no readers to evict: runID=%s bufLen=%d hardcap=%d minCursor=%d headSeq=%d nextSeq=%d",
					b.runID, len(b.events), graphBufferHardcap, minCursor, b.headSeq, b.nextSeq)
			}
		}
	}

	// Physically drop the released head prefix.
	cut := 0
	for cut < len(b.events) && b.events[cut].released {
		cut++
	}
	if cut > 0 {
		b.headSeq = b.events[cut-1].seq
		b.gcRunCount++
		b.gcReleasedEvents += uint64(cut)
		remaining := len(b.events) - cut
		if remaining*4 < cap(b.events) {
			nb := make([]bufferedGraphEvent, remaining)
			copy(nb, b.events[cut:])
			b.events = nb
		} else {
			b.events = b.events[cut:]
		}
	}
}

// minCursorLocked returns the smallest cursor across live readers, or MaxUint64
// when there are none (every "all cursors crossed X" predicate is then vacuously
// true, so GC may reclaim freely). Caller holds b.mu.
func (b *graphEventBuffer) minCursorLocked() uint64 {
	if len(b.readers) == 0 {
		return ^uint64(0)
	}
	min := ^uint64(0)
	for r := range b.readers {
		if r.cursor < min {
			min = r.cursor
		}
	}
	return min
}
