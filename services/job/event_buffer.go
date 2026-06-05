package job

import (
	"context"
	"errors"
	"strconv"
	"sync"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/model"
)

// ErrSeqGone is returned by Subscribe when the requested resume sequence has
// already been GC'd from the buffer. The SSE handler maps this to HTTP 410
// so the client falls back to the "re-fetch snapshot" path.
var ErrSeqGone = errors.New("event buffer: requested sequence has been garbage-collected")

// roundInfo tracks one A-class round's boundaries. A round is open from
// IterationStarted to IterationCompleted / IterationFailed; while open, all
// non-boundary events published belong to it.
type roundInfo struct {
	id       string
	startSeq uint64
	endSeq   uint64 // 0 while open
	closed   bool
}

type bufferedEvent struct {
	seq      uint64
	event    any
	eventTyp model.EventType
	classB   bool
	// roundID is the round this event belongs to. Empty for events
	// published outside any round (e.g. JOB_STARTED before the first
	// iteration, or JOB_COMPLETED after the last iteration closed).
	roundID  string
	released bool
}

// JobEventBuffer is a per-job append-only event log with multi-cursor
// readers and classified GC. See
// docs/feature-2026-05-13-sse-event-buffer-detail.md for the design.
type JobEventBuffer struct {
	jobID string

	mu   sync.Mutex
	cond *sync.Cond

	events  []bufferedEvent
	nextSeq uint64 // seq to assign to the next event; first event gets seq=1
	headSeq uint64 // seq of the most recently GC'd event (events <= headSeq are gone)

	rounds      map[string]*roundInfo
	roundOrder  []string
	openRoundID string

	readers map[*bufferReader]struct{}

	closed   bool // buffer fully released (job deleted)
	terminal bool // job entered terminal state — stop GC

	// gcSuspendedUntilReader is set by ResumeGC() to prevent the first
	// gcLocked() pass from reclaiming events before any SSE reader has
	// reconnected. Without this, the window between resumeGC (terminal=false)
	// and the client's reconnect allows gcLocked to advance headSeq (since
	// minCursor=MaxUint64 with no readers), causing the reconnecting client
	// to receive ErrSeqGone (410). Cleared on the first Subscribe().
	gcSuspendedUntilReader bool
	// gcSuspendedAt records when gcSuspendedUntilReader was set. Used to
	// auto-expire the suspension after gcSuspensionTimeout so a permanently
	// absent reader doesn't block GC forever.
	gcSuspendedAt time.Time

	// lastReaderLeftAt records when the last reader disconnected mid-run.
	// gcLocked uses this to defer GC for gcGracePeriod, giving the client
	// time to reconnect before headSeq advances (prevents the mid-run 410
	// race condition where reader.Close() → gcLocked() immediately GC's
	// closed rounds while readers==0).
	lastReaderLeftAt time.Time

	// warnedAt10k / warnedAt25k prevent repeated "buffer growing large"
	// warnings within the buffer's lifetime (never reset once triggered).
	warnedAt10k bool
	warnedAt25k bool

	// gcRunCount counts gcLocked invocations that physically dropped at
	// least one event. gcReleasedEvents accumulates the number of events
	// physically dropped across all GC runs. Both are monotonic counters
	// for §6.1 "可观测性" — exposed via Stats() for follow-up wiring to
	// metrics / debug endpoints.
	gcRunCount       uint64
	gcReleasedEvents uint64
}

func newJobEventBuffer(jobID string) *JobEventBuffer {
	b := &JobEventBuffer{
		jobID:   jobID,
		rounds:  make(map[string]*roundInfo),
		readers: make(map[*bufferReader]struct{}),
	}
	b.cond = sync.NewCond(&b.mu)
	return b
}

// Publish appends one event to the buffer, assigns it a monotonic sequence
// number, classifies it, updates round boundaries, wakes readers, and runs
// a GC pass. Returns the assigned seq (>= 1), or 0 if the buffer was
// closed (the job was deleted while a producer was still in flight —
// drop silently because the job state file is gone anyway).
func (b *JobEventBuffer) Publish(event any) uint64 {
	typ, classB, isRoundStart, isRoundEnd := classifyEvent(event)

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.closed {
		// Closed buffer means the job was deleted while a producer was
		// still in flight. Drop silently — the job state file is gone
		// anyway and there's no consumer to deliver to.
		return 0
	}

	b.nextSeq++
	seq := b.nextSeq

	roundID := b.openRoundID
	if isRoundStart {
		// Use the seq itself as the round id for uniqueness; iteration
		// events carry their path so the id doesn't need to be human-
		// readable.
		roundID = roundIDFor(seq)
		b.openRoundID = roundID
		b.rounds[roundID] = &roundInfo{id: roundID, startSeq: seq}
		b.roundOrder = append(b.roundOrder, roundID)
	}

	be := bufferedEvent{
		seq:      seq,
		event:    event,
		eventTyp: typ,
		classB:   classB,
		roundID:  roundID,
	}
	b.events = append(b.events, be)

	if isRoundEnd && roundID != "" {
		if r, ok := b.rounds[roundID]; ok {
			r.endSeq = seq
			r.closed = true
		}
		b.openRoundID = ""
		// Note: warnedAt10k/warnedAt25k are intentionally NOT reset here.
		// They guard per-buffer-lifetime warnings to avoid log spam in
		// multi-round loop jobs where each round produces 10k+ events.
	}

	b.cond.Broadcast()
	b.gcLocked()
	return seq
}

// PublishTransient delivers a fire-and-forget event to all currently
// connected readers without buffering it. Transient events do not get a
// sequence number and never appear in the resume stream — they are used
// for slash-command results (e.g. ws switch) that must not reappear on
// page refresh.
func (b *JobEventBuffer) PublishTransient(event any) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return
	}
	for r := range b.readers {
		r.transient = append(r.transient, event)
	}
	b.cond.Broadcast()
}

// MarkTerminal flips the buffer into "no GC" mode. Called from the job
// service after a terminal event has been published, so the remaining
// chunks of the last round and any tail B-class events stay around for
// late refreshes until the job is explicitly deleted.
func (b *JobEventBuffer) MarkTerminal() {
	b.mu.Lock()
	b.terminal = true
	b.mu.Unlock()
}

// HasOpenRound reports whether a round is currently open (an
// IterationStarted has been published without a matching
// IterationCompleted/Failed yet). Used by panic-recovery defers to decide
// whether they need to publish a synthetic round-close pair before the
// terminal event — without that pair the round stays closed=false and its
// A-class chunks never become reclaimable once Continue resumes GC.
func (b *JobEventBuffer) HasOpenRound() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.openRoundID != ""
}

// ResumeGC flips a previously terminal buffer back into "GC active" mode.
// Continue and SendMessage on a job that has reached a terminal status
// reuse the same buffer (only Start calls resetForRun); without resuming
// GC, the previous run's MarkTerminal would keep gcLocked short-circuited
// for the entire lifetime of the new run, causing unbounded growth.
//
// Idempotent. When no readers are connected at the time of this call, GC
// is suspended until the first reader Subscribe()s — this prevents the
// race where gcLocked advances headSeq (minCursor=MaxUint64 with no
// readers) before the client can reconnect, which would cause ErrSeqGone.
// Once a reader is present, normal GC resumes protected by minCursor.
func (b *JobEventBuffer) ResumeGC() {
	b.mu.Lock()
	b.terminal = false
	if len(b.readers) == 0 {
		b.gcSuspendedUntilReader = true
		b.gcSuspendedAt = time.Now()
		logger.Infof(context.Background(),
			"[event_buffer] resumeGC: no active readers, suspending GC until first subscriber (timeout=%v), jobID=%s headSeq=%d nextSeq=%d",
			gcSuspensionTimeout, b.jobID, b.headSeq, b.nextSeq)
	}
	b.mu.Unlock()
}

// IsFresh reports whether the buffer has never been published to and is
// not in a terminal/closed state. Used by resetForRun to skip the
// close+recreate path when the existing buffer's sequence space is
// already fresh — recreating it would needlessly evict any SSE
// subscriber that attached during the gap between job creation and
// Start (the FE opens SSE before POSTing /start), surfacing as a
// spurious "服务器断开" banner flash on every loop entry.
func (b *JobEventBuffer) IsFresh() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.nextSeq == 0 && !b.closed && !b.terminal
}

// Close releases the entire buffer and wakes all readers. Called when the
// job is explicitly deleted. After Close, Publish becomes a no-op and
// readers see ok=false from Read.
func (b *JobEventBuffer) Close() {
	b.mu.Lock()
	b.closed = true
	b.events = nil
	b.cond.Broadcast()
	b.mu.Unlock()
}

// SnapshotSeq returns the sequence number a brand-new SSE subscriber
// should resume from. If a round is currently in flight, returns
// (round_start - 1) so the subscriber sees the round's *_START boundary
// and rebuilds the in-progress chunks. Otherwise returns the current
// tail (so already-flushed rounds are not re-delivered, since
// messages.jsonl already covers them).
func (b *JobEventBuffer) SnapshotSeq() uint64 {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.openRoundID != "" {
		if r, ok := b.rounds[b.openRoundID]; ok && r.startSeq > 0 {
			seq := r.startSeq - 1
			// Clamp: if the open round has produced more events than
			// replayGapLimit, return a seq closer to the tail so that
			// Subscribe won't reject the client with ErrSeqGone.
			// Leave a slack of 200 so that events published between
			// SnapshotSeq() and Subscribe() (separate lock acquisitions)
			// don't push the gap over the limit. Previously 50 which was
			// insufficient under sustained high throughput (>50 events/sec
			// between the two calls would cause Subscribe to reject with
			// 410, triggering a reconnect death loop).
			const clampSlack uint64 = 200
			if b.nextSeq-seq > replayGapLimit-clampSlack {
				logger.Debugf(context.Background(),
					"[event_buffer] SnapshotSeq clamped: jobID=%s roundStartSeq=%d nextSeq=%d gap=%d limit=%d slack=%d headSeq=%d, returning tail-based seq",
					b.jobID, r.startSeq, b.nextSeq, b.nextSeq-seq, replayGapLimit, clampSlack, b.headSeq)
				seq = b.nextSeq - replayGapLimit + clampSlack
			}
			// Floor: after hardcap eviction headSeq may have advanced past the
			// clamped value. Ensure we never return a seq that Subscribe() would
			// immediately reject with ErrSeqGone.
			if seq < b.headSeq {
				logger.Warnf(context.Background(),
					"[event_buffer] SnapshotSeq floored to headSeq: jobID=%s seq=%d headSeq=%d nextSeq=%d",
					b.jobID, seq, b.headSeq, b.nextSeq)
				seq = b.headSeq
			}
			return seq
		}
	}
	return b.nextSeq
}

// BufferStats is a snapshot of a JobEventBuffer's runtime state for
// observability (§6.1 item 10). Exposed read-only via JobEventBuffer.Stats
// and Service.BufferStats; safe to log or serialize.
type BufferStats struct {
	// Events is the number of events physically held in the buffer.
	Events int
	// Readers is the number of live SSE subscribers attached.
	Readers int
	// HeadSeq is the seq of the most recently GC'd event; events with
	// seq > HeadSeq are still buffered.
	HeadSeq uint64
	// NextSeq is the seq assigned to the most recently published event
	// (== 0 when nothing has been published yet).
	NextSeq uint64
	// MaxLagSeq is the largest "tail - cursor" gap across all readers,
	// i.e. how far the slowest subscriber has fallen behind. 0 when
	// there are no readers or every cursor is at the tail.
	MaxLagSeq uint64
	// GCRunCount is the count of gcLocked passes that physically dropped
	// at least one event. Monotonic for the lifetime of the buffer.
	GCRunCount uint64
	// GCReleasedEvents is the cumulative number of events dropped by GC
	// across the buffer's lifetime. Monotonic.
	GCReleasedEvents uint64
	// Terminal indicates the buffer has stopped GC because the job
	// entered a terminal state.
	Terminal bool
	// Closed indicates the buffer has been released (job deleted).
	Closed bool
}

// Stats returns a snapshot of the buffer's current state for monitoring.
// Holds b.mu only for the duration of the read; callers can poll freely.
func (b *JobEventBuffer) Stats() BufferStats {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.statsLocked()
}

func (b *JobEventBuffer) statsLocked() BufferStats {
	stats := BufferStats{
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
			lag := b.nextSeq - r.cursor
			if lag > stats.MaxLagSeq {
				stats.MaxLagSeq = lag
			}
		}
	}
	return stats
}

// Subscribe creates a new reader cursored at startSeq+1. Returns ErrSeqGone
// when the caller's resume point is outside the buffer's current sequence
// space — the SSE handler maps this to 410.
//
// Valid range is [headSeq, nextSeq]:
//   - startSeq < headSeq: the next event has already been GC'd
//   - startSeq > nextSeq: the cursor belongs to a different buffer epoch
//     (typical after resetForRun on Job restart). Without this guard the
//     reader would block forever waiting for a seq that this buffer will
//     never reach, while silently skipping events 1..startSeq of the
//     fresh run when nextSeq finally catches up.
//
// replayGapLimit is the maximum number of events a new subscriber is allowed
// to replay on connect. If the gap between startSeq and nextSeq exceeds this
// limit, Subscribe returns ErrSeqGone so the client falls back to the snapshot
// path — avoiding a replay storm that overwhelms slow clients (e.g. mobile)
// and triggers repeated write-timeout → reconnect → replay cycles.
const replayGapLimit = 1000

// startSeq=0 means "start at current tail" (the snapshot's "no in-flight
// round" branch); startSeq==nextSeq is fine (no events yet); startSeq
// equal to headSeq is fine (next event is buffered[0]).
func (b *JobEventBuffer) Subscribe(startSeq uint64) (*bufferReader, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.closed {
		return nil, ErrSeqGone
	}
	if startSeq < b.headSeq {
		lastReaderLeftAgo := "never"
		if !b.lastReaderLeftAt.IsZero() {
			dur := time.Since(b.lastReaderLeftAt)
			if dur > 24*time.Hour || dur < 0 {
				// Monotonic clock mismatch (container/VM migration) — show raw timestamp instead
				lastReaderLeftAgo = "at:" + b.lastReaderLeftAt.Format(time.RFC3339)
			} else {
				lastReaderLeftAgo = dur.String()
			}
		}
		logger.Warnf(context.Background(),
			"[event_buffer] subscribe rejected: startSeq < headSeq (events already GC'd), jobID=%s startSeq=%d headSeq=%d nextSeq=%d readers=%d gcRuns=%d gcReleased=%d lastReaderLeftAgo=%s",
			b.jobID, startSeq, b.headSeq, b.nextSeq, len(b.readers), b.gcRunCount, b.gcReleasedEvents, lastReaderLeftAgo)
		return nil, ErrSeqGone
	}
	if startSeq > b.nextSeq {
		logger.Warnf(context.Background(),
			"[event_buffer] subscribe rejected: startSeq > nextSeq (seq from the future), jobID=%s startSeq=%d nextSeq=%d headSeq=%d",
			b.jobID, startSeq, b.nextSeq, b.headSeq)
		return nil, ErrSeqGone
	}
	// Guard against replay storms: if the client is too far behind, reject
	// the subscribe and force a snapshot reload. This prevents slow clients
	// from entering a reconnect-replay death loop.
	if b.nextSeq-startSeq > replayGapLimit {
		logger.Warnf(context.Background(),
			"[event_buffer] subscribe rejected: replay gap too large, jobID=%s nextSeq=%d startSeq=%d gap=%d limit=%d",
			b.jobID, b.nextSeq, startSeq, b.nextSeq-startSeq, replayGapLimit)
		return nil, ErrSeqGone
	}
	r := &bufferReader{buf: b, cursor: startSeq}
	b.readers[r] = struct{}{}
	// Clear grace period: a reader has arrived, normal GC is now safe.
	b.lastReaderLeftAt = time.Time{}
	// First reader after ResumeGC: clear the GC suspension so subsequent
	// Publish/gcLocked calls can reclaim normally (protected by minCursor).
	if b.gcSuspendedUntilReader {
		b.gcSuspendedUntilReader = false
		logger.Infof(context.Background(),
			"[event_buffer] GC suspension lifted: first reader connected after resumeGC, jobID=%s startSeq=%d headSeq=%d nextSeq=%d",
			b.jobID, startSeq, b.headSeq, b.nextSeq)
	}
	return r, nil
}

// bufferHardcap is the maximum number of events the buffer may hold. When
// exceeded during an open round, the buffer force-evicts all events (both
// A-class content and B-class boundary) that all readers have already consumed,
// even though the round has not yet closed. This bounds memory for long-running
// iterations (e.g. a shell producing output for hours). Evicted events cannot
// be replayed on reconnect — the client falls back to snapshot mode via
// ErrSeqGone. Round metadata is preserved in b.rounds and does not require
// physical events to remain in the buffer.
const bufferHardcap = 50000

// gcGracePeriod is the duration after the last reader disconnects during which
// gcLocked will skip reclamation of closed-round events. This prevents the
// mid-run 410 race where reader.Close() immediately GC's events that the
// reconnecting client still needs. 30s is generous — typical reconnects happen
// within 1-3s (the client retries at 200ms/1s/3s).
const gcGracePeriod = 30 * time.Second

// gcSuspensionTimeout is the maximum time gcSuspendedUntilReader remains in
// effect without a reader connecting. After this timeout, the suspension
// auto-expires so GC can proceed normally. Without this, a permanently absent
// reader (e.g. user closed the tab) would block GC indefinitely, causing
// unbounded buffer growth until hardcap eviction.
const gcSuspensionTimeout = 5 * time.Minute

// gcLocked walks events from the head and physically drops any prefix of
// already-released entries. It also re-evaluates which entries are now
// release-eligible based on cursor advance + round closure.
//
// When the buffer exceeds bufferHardcap, a forced eviction pass releases
// all events (both A-class content and B-class boundary) in the open round
// that all readers have crossed, so that the physical prefix can be dropped.
// Round metadata is preserved in b.rounds and does not require physical
// events to remain in the buffer.
//
// Caller must hold b.mu.
func (b *JobEventBuffer) gcLocked() {
	if b.terminal {
		return
	}
	// If GC was just resumed (terminal flipped to false) but no reader has
	// connected yet, suspend GC to avoid advancing headSeq and causing
	// ErrSeqGone for the reconnecting client. Exception: if the buffer has
	// hit the hardcap, we must still evict to avoid OOM.
	// Auto-expire the suspension after gcSuspensionTimeout.
	if b.gcSuspendedUntilReader && len(b.readers) == 0 && len(b.events) <= bufferHardcap {
		if time.Since(b.gcSuspendedAt) < gcSuspensionTimeout {
			return
		}
		// Suspension expired — clear it and proceed with GC.
		b.gcSuspendedUntilReader = false
		logger.Warnf(context.Background(),
			"[event_buffer] GC suspension auto-expired after %v without any reader connecting: jobID=%s headSeq=%d nextSeq=%d bufLen=%d",
			gcSuspensionTimeout, b.jobID, b.headSeq, b.nextSeq, len(b.events))
	}

	// Grace period: when the last reader just disconnected mid-run, defer GC
	// briefly to allow reconnection. Without this, reader.Close() → gcLocked()
	// with readers=0 immediately GC's closed-round events, causing the
	// reconnecting client to get ErrSeqGone (410).
	if len(b.readers) == 0 && !b.lastReaderLeftAt.IsZero() && len(b.events) <= bufferHardcap {
		if time.Since(b.lastReaderLeftAt) < gcGracePeriod {
			return
		}
	}

	minCursor := b.minCursorLocked()

	// Early warning: log when the buffer is growing large during an open
	// round. We log at 10k and 25k thresholds (once per buffer lifetime) so
	// operators get advance notice before hitting the 50k hardcap.
	bufLen := len(b.events)
	if b.openRoundID != "" {
		if bufLen >= 10000 && !b.warnedAt10k {
			b.warnedAt10k = true
			minCursorStr := "inf(no_readers)"
			if len(b.readers) > 0 {
				minCursorStr = strconv.FormatUint(minCursor, 10)
			}
			logger.Warnf(context.Background(),
				"[event_buffer] buffer growing large in open round (threshold=10k): jobID=%s bufLen=%d openRound=%s minCursor=%s headSeq=%d nextSeq=%d readers=%d gcRuns=%d gcReleased=%d",
				b.jobID, bufLen, b.openRoundID, minCursorStr, b.headSeq, b.nextSeq, len(b.readers), b.gcRunCount, b.gcReleasedEvents)
		}
		if bufLen >= 25000 && !b.warnedAt25k {
			b.warnedAt25k = true
			minCursorStr := "inf(no_readers)"
			if len(b.readers) > 0 {
				minCursorStr = strconv.FormatUint(minCursor, 10)
			}
			logger.Warnf(context.Background(),
				"[event_buffer] buffer growing large in open round (threshold=25k): jobID=%s bufLen=%d openRound=%s minCursor=%s headSeq=%d nextSeq=%d readers=%d gcRuns=%d gcReleased=%d",
				b.jobID, bufLen, b.openRoundID, minCursorStr, b.headSeq, b.nextSeq, len(b.readers), b.gcRunCount, b.gcReleasedEvents)
		}
	}

	// First pass: mark release-eligible.
	for i := range b.events {
		e := &b.events[i]
		if e.released || e.seq > minCursor {
			continue
		}
		if e.classB && e.roundID == "" {
			// B-class outside any round: physically reclaimable as
			// soon as cursor crosses it.
			e.released = true
			continue
		}
		// Both A-class chunks and B-class events that fell inside a
		// round are reclaimed together with the round, once the round
		// is closed and minCursor has crossed its end. Round end
		// fires only after messages.jsonl has been written for the
		// round (see §2.4 contract).
		if e.roundID != "" {
			if r, ok := b.rounds[e.roundID]; ok && r.closed && minCursor >= r.endSeq {
				e.released = true
			}
		}
	}

	// Hardcap eviction: when the buffer exceeds the hard limit, force-release
	// events in the OPEN round that all readers have already consumed. This
	// allows long-running iterations to shed memory. Both A-class content and
	// B-class boundary events are released — round metadata is preserved in
	// b.rounds and does not require physical events to remain in the buffer.
	if len(b.events) > bufferHardcap {
		hardcapReleased := 0
		for i := range b.events {
			e := &b.events[i]
			if e.seq > minCursor {
				break // events are ordered by seq; no point scanning further
			}
			if e.released {
				continue // already released by the first pass, skip to next
			}
			e.released = true
			hardcapReleased++
		}
		if hardcapReleased > 0 {
			logger.Warnf(context.Background(),
				"[event_buffer] hardcap eviction triggered: jobID=%s bufLen=%d hardcap=%d forceReleased=%d minCursor=%d headSeq=%d",
				b.jobID, len(b.events), bufferHardcap, hardcapReleased, minCursor, b.headSeq)
		} else {
			// Buffer exceeds hardcap but no events could be evicted — readers
			// are connected but their cursors haven't advanced past the oldest
			// event yet. Evict the most lagging reader to unblock GC and prevent
			// unbounded memory growth (OOM risk for long-running Loop jobs).
			var stuckReader *bufferReader
			var stuckCursor uint64 = ^uint64(0)
			for r := range b.readers {
				if r.cursor < stuckCursor {
					stuckCursor = r.cursor
					stuckReader = r
				}
			}
			if stuckReader != nil {
				logger.Errorf(context.Background(),
					"[event_buffer] hardcap exceeded, evicting stuck reader: jobID=%s bufLen=%d hardcap=%d readerCursor=%d headSeq=%d nextSeq=%d lag=%d readers=%d openRound=%s",
					b.jobID, len(b.events), bufferHardcap, stuckCursor, b.headSeq, b.nextSeq, b.nextSeq-stuckCursor, len(b.readers), b.openRoundID)
				stuckReader.closed = true
				delete(b.readers, stuckReader)
				// Wake the evicted reader's Read() goroutine so it sees closed=true
				// and returns ok=false, allowing the SSE handler to exit cleanly.
				b.cond.Broadcast()
				// After eviction, minCursor may have advanced — immediately
				// release newly-unlocked events instead of waiting for the
				// next Publish/Ack cycle.
				newMinCursor := b.minCursorLocked()
				evictReleased := 0
				for i := range b.events {
					e := &b.events[i]
					if e.seq > newMinCursor {
						break
					}
					if e.released {
						continue
					}
					e.released = true
					evictReleased++
				}
				if evictReleased > 0 {
					logger.Warnf(context.Background(),
						"[event_buffer] post-eviction GC released %d events: jobID=%s bufLen=%d newMinCursor=%d headSeq=%d",
						evictReleased, b.jobID, len(b.events), newMinCursor, b.headSeq)
				}
			} else {
				logger.Errorf(context.Background(),
					"[event_buffer] hardcap exceeded but no readers to evict: jobID=%s bufLen=%d hardcap=%d minCursor=%d headSeq=%d nextSeq=%d openRound=%s",
					b.jobID, len(b.events), bufferHardcap, minCursor, b.headSeq, b.nextSeq, b.openRoundID)
			}
		}
	}

	// Second pass: physically drop the released prefix. Sparse holes are
	// avoided by design — we only drop a contiguous head block, so the
	// remaining tail keeps its [oldestSeq, tailSeq] continuity.
	cut := 0
	for cut < len(b.events) && b.events[cut].released {
		cut++
	}
	if cut > 0 {
		dropped := b.events[:cut]
		// Forget round metadata for rounds that have just been physically
		// dropped in their entirety.
		for _, e := range dropped {
			if r, ok := b.rounds[e.roundID]; ok && e.seq == r.endSeq && r.closed {
				delete(b.rounds, e.roundID)
				for i, id := range b.roundOrder {
					if id == e.roundID {
						b.roundOrder = append(b.roundOrder[:i], b.roundOrder[i+1:]...)
						break
					}
				}
			}
		}
		b.headSeq = b.events[cut-1].seq
		b.gcRunCount++
		b.gcReleasedEvents += uint64(cut)
		// Re-allocate when cap >> len so the underlying array doesn't
		// keep references to old payloads. cap/4 threshold trades a bit
		// of allocation for predictable memory release.
		remaining := len(b.events) - cut
		if remaining*4 < cap(b.events) {
			nb := make([]bufferedEvent, remaining)
			copy(nb, b.events[cut:])
			b.events = nb
		} else {
			b.events = b.events[cut:]
		}
	}
}

// minCursorLocked returns the smallest cursor across all live readers, or
// math.MaxUint64 when no readers exist. With no readers the "all cursors
// crossed seq X" predicate is vacuously true for every X, so GC may
// reclaim freely (within the round-end + jsonl contract).
//
// Caller must hold b.mu.
func (b *JobEventBuffer) minCursorLocked() uint64 {
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

// roundIDFor builds a deterministic round id from the seq of its
// IterationStarted event. Internal-only — not surfaced to clients.
func roundIDFor(seq uint64) string {
	// Avoid the strconv import for a hot path; produce a minimal stable
	// identifier. The seq alone is unique within a buffer's lifetime.
	return seqToHex(seq)
}

func seqToHex(n uint64) string {
	const hex = "0123456789abcdef"
	if n == 0 {
		return "0"
	}
	var buf [16]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = hex[n&0xf]
		n >>= 4
	}
	return string(buf[i:])
}

// classifyEvent maps a payload to (eventType, isClassB, isRoundStart,
// isRoundEnd). C-class events (ARTIFACT_*, STATE_SNAPSHOT) have been
// removed from the codebase, so this function only needs to recognise A
// and B class events plus round boundaries.
func classifyEvent(event any) (typ model.EventType, classB, isStart, isEnd bool) {
	switch ev := event.(type) {
	// A-class — chunks of an in-flight round.
	case *model.TextMessageStartEvent:
		return ev.Type, false, false, false
	case *model.TextMessageContentEvent:
		return ev.Type, false, false, false
	case *model.TextMessageEndEvent:
		return ev.Type, false, false, false
	case *model.ToolCallStartEvent:
		return ev.Type, false, false, false
	case *model.ToolCallArgsEvent:
		return ev.Type, false, false, false
	case *model.ToolCallResultEvent:
		return ev.Type, false, false, false
	case *model.ToolCallEndEvent:
		return ev.Type, false, false, false
	case *model.ToolCallStitchedEvent:
		return ev.Type, false, false, false

	// Round boundaries.
	case *model.IterationStartedEvent:
		return ev.Type, true, true, false
	case *model.IterationCompletedEvent:
		return ev.Type, true, false, true
	case *model.IterationFailedEvent:
		return ev.Type, true, false, true

	// B-class — state deltas with their truth in job.json or session meta.
	case *model.JobStartedEvent:
		return ev.Type, true, false, false
	case *model.JobCompletedEvent:
		return ev.Type, true, false, false
	case *model.JobStoppedEvent:
		return ev.Type, true, false, false
	case *model.JobFailedEvent:
		return ev.Type, true, false, false
	case *model.RunStartedEvent:
		return ev.Type, true, false, false
	case *model.RunFinishedEvent:
		return ev.Type, true, false, false
	case *model.RunErrorEvent:
		return ev.Type, true, false, false
	case *model.CustomEvent:
		// CustomEvent currently only carries token_usage and
		// job_title_updated. Treat as B-class either way; both have
		// their truth in job.json.
		return ev.Type, true, false, false

	default:
		// Defensive: unknown event types should still buffer (avoid
		// silent loss). Treat as B-class outside any round so they
		// reclaim quickly once cursors cross.
		return "", true, false, false
	}
}

// Len returns the current number of events physically held in the buffer.
// Useful for lightweight growth checks without the overhead of full Stats().
func (b *JobEventBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.events)
}

// Reader is one SSE subscriber's read handle. Cursor advances only after
// Ack — this gives the SSE writer "at-least-once with cursor rewind"
// semantics: a network-write failure can re-Read the same event after
// reconnect.
type Reader = bufferReader

// ReadEntry is what Read returns. Transient events have Seq=0 and must
// not be acknowledged (they don't gate cursor or GC).
type ReadEntry = readEntry

// bufferReader is one SSE subscriber's read handle. Cursor advances only
// after Ack — this gives the SSE writer "at-least-once with cursor
// rewind" semantics: a network-write failure can re-Read the same event
// after reconnect.
type bufferReader struct {
	buf       *JobEventBuffer
	cursor    uint64
	transient []any
	closed    bool
}

// readEntry is what Read returns. Transient events have Seq=0 and must
// not be acknowledged (they don't gate cursor or GC).
type readEntry struct {
	Seq       uint64
	Event     any
	Transient bool
}

// Read blocks until at least one event is available for this reader, the
// context is cancelled, or the buffer is closed. Returns up to maxN
// entries starting at cursor+1, plus any transient events queued for
// this reader. ok=false means the reader was closed or ctx fired before
// any event arrived.
func (r *bufferReader) Read(ctx context.Context, maxN int) ([]readEntry, bool) {
	if maxN <= 0 {
		maxN = 1
	}
	r.buf.mu.Lock()
	defer r.buf.mu.Unlock()

	// Wait until something is available. We watch ctx via a short-lived
	// goroutine that broadcasts on the cond when cancelled — a common
	// pattern when mixing sync.Cond with ctx.
	stop := make(chan struct{})
	defer close(stop)
	go func() {
		select {
		case <-ctx.Done():
			r.buf.mu.Lock()
			r.buf.cond.Broadcast()
			r.buf.mu.Unlock()
		case <-stop:
		}
	}()

	for {
		// Wait until something is available.
		for {
			if r.closed || r.buf.closed {
				return nil, false
			}
			if ctx.Err() != nil {
				return nil, false
			}
			if len(r.transient) > 0 || r.cursor < r.buf.nextSeq {
				break
			}
			r.buf.cond.Wait()
		}

		out := make([]readEntry, 0, maxN)

		// Transients first (older slash-command feedback before live tail).
		for len(r.transient) > 0 && len(out) < maxN {
			out = append(out, readEntry{Event: r.transient[0], Transient: true})
			r.transient = r.transient[1:]
		}

		// Then buffered events from cursor+1 onwards. Find the index of the
		// first event with seq > cursor and stream forward.
		if r.cursor < r.buf.nextSeq && len(out) < maxN {
			i := indexAfter(r.buf.events, r.cursor)
			for i < len(r.buf.events) && len(out) < maxN {
				e := r.buf.events[i]
				out = append(out, readEntry{Seq: e.seq, Event: e.event})
				i++
			}
		}

		if len(out) > 0 {
			return out, true
		}

		// GC gap: cursor < nextSeq held but all events in [cursor+1, nextSeq)
		// have been GC'd so nothing was deliverable. Without this guard the
		// caller would see ok=true + empty slice and immediately re-call Read,
		// creating a tight busy loop that never sends keepalives and starves the
		// SSE connection. Advance cursor past the gap and re-enter the wait loop
		// so we properly block until genuinely new events arrive.
		r.cursor = r.buf.nextSeq
		// Fall through to outer loop → inner wait loop blocks until new events.
	}
}

// indexAfter returns the index of the first event in events with seq > cursor.
// events is sorted by seq ascending (append-only with monotonic seq).
func indexAfter(events []bufferedEvent, cursor uint64) int {
	// Linear scan from the head is O(n) worst case but the buffer never
	// holds more than a few hundred events under normal GC, so binary
	// search would be over-engineering. Still, we start from the head
	// because Read is called repeatedly with cursor advancing — most
	// calls will find their target near the head of the live tail.
	for i, e := range events {
		if e.seq > cursor {
			return i
		}
	}
	return len(events)
}

// Ack marks seq as successfully delivered. Cursor only advances forward
// (Acks for older seqs are ignored). Triggers GC re-evaluation.
func (r *bufferReader) Ack(seq uint64) {
	r.buf.mu.Lock()
	defer r.buf.mu.Unlock()
	if seq > r.cursor {
		r.cursor = seq
	}
	r.buf.gcLocked()
}

// Close removes this reader from the buffer's active set and wakes any
// in-flight Read so the caller can return.
func (r *bufferReader) Close() {
	r.buf.mu.Lock()
	if !r.closed {
		r.closed = true
		delete(r.buf.readers, r)
		// Record when the last reader left so gcLocked can defer reclamation
		// during the grace period, preventing the mid-run 410 race.
		if len(r.buf.readers) == 0 && !r.buf.terminal {
			r.buf.lastReaderLeftAt = time.Now()
		}
		r.buf.gcLocked()
	}
	r.buf.cond.Broadcast()
	r.buf.mu.Unlock()
}

// Cursor returns the last acknowledged seq. Surfaced for observability.
func (r *bufferReader) Cursor() uint64 {
	r.buf.mu.Lock()
	defer r.buf.mu.Unlock()
	return r.cursor
}

// ReadStatus communicates how a ReadWithTimeout call ended.
type ReadStatus int

const (
	// ReadOK means the call returned at least one entry.
	ReadOK ReadStatus = iota
	// ReadTimeout means the per-call deadline elapsed before any event
	// arrived. The reader is still alive — the caller may loop and read
	// again. Used by the SSE handler to write a keep-alive frame from
	// the same goroutine that does event writes (single-writer
	// guarantee).
	ReadTimeout
	// ReadClosed means the parent context was cancelled or the buffer/
	// reader was closed. The caller should release the reader and exit.
	ReadClosed
)

// ReadWithTimeout behaves like Read, but returns ReadTimeout if no event
// becomes available within timeout. The parent ctx is honoured: if it is
// cancelled the call returns ReadClosed. timeout <= 0 falls back to plain
// Read semantics (i.e. blocks until events / ctx / close).
func (r *bufferReader) ReadWithTimeout(ctx context.Context, timeout time.Duration, maxN int) ([]readEntry, ReadStatus) {
	if timeout <= 0 {
		entries, ok := r.Read(ctx, maxN)
		if !ok {
			return nil, ReadClosed
		}
		return entries, ReadOK
	}
	childCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	entries, ok := r.Read(childCtx, maxN)
	if ok {
		return entries, ReadOK
	}
	// ok=false means: parent cancelled, reader/buffer closed, or child
	// timeout. Disambiguate so the caller can keep going on a pure
	// timeout while still exiting on a real shutdown.
	if ctx.Err() != nil {
		return nil, ReadClosed
	}
	r.buf.mu.Lock()
	closed := r.closed || r.buf.closed
	r.buf.mu.Unlock()
	if closed {
		return nil, ReadClosed
	}
	return nil, ReadTimeout
}
