package job

import (
	"context"
	"errors"
	"fmt"
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

// jobEventBuffer is a per-job append-only event log with multi-cursor
// readers and classified GC. See
// docs/arch/sse-event-buffer-design.md for the design.
type jobEventBuffer struct {
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

func newJobEventBuffer(jobID string) *jobEventBuffer {
	b := &jobEventBuffer{
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
func (b *jobEventBuffer) Publish(event any) uint64 {
	seq, _ := b.publishClassified(event)
	return seq
}

func (b *jobEventBuffer) publishClassified(event any) (uint64, eventClassification) {
	cls := classifyEvent(event)
	if !cls.known {
		warnUnknownBufferedEventType(event)
	}
	return b.appendClassified(event, cls), cls
}

func (b *jobEventBuffer) appendClassified(event any, cls eventClassification) uint64 {

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
	if cls.isRoundStart {
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
		eventTyp: cls.typ,
		classB:   cls.classB,
		roundID:  roundID,
	}
	b.events = append(b.events, be)

	if cls.isRoundEnd && roundID != "" {
		if r, ok := b.rounds[roundID]; ok {
			r.endSeq = seq
			r.closed = true
		}
		b.openRoundID = ""
		// Note: warnedAt10k/warnedAt25k are intentionally NOT reset here.
		// They guard per-buffer-lifetime warnings to avoid log spam in
		// multi-round loop jobs where each round produces 10k+ events.
	}
	if cls.isTerminal {
		b.terminal = true
	}

	b.cond.Broadcast()
	b.gcLocked()
	return seq
}

type eventClassification struct {
	typ          model.EventType
	known        bool
	classB       bool
	isRoundStart bool
	isRoundEnd   bool
	isTerminal   bool
}

// classifyEvent maps a payload to its buffer class, round-boundary flags, and
// job-terminal flag. C-class events (ARTIFACT_*, STATE_SNAPSHOT) have been
// removed from the codebase, so this function only needs to recognise A and B
// class events plus round boundaries.
func classifyEvent(event any) eventClassification {
	switch ev := event.(type) {
	// A-class — chunks of an in-flight round.
	case *model.TextMessageStartEvent:
		return eventClassification{typ: ev.Type, known: true}
	case *model.TextMessageContentEvent:
		return eventClassification{typ: ev.Type, known: true}
	case *model.TextMessageEndEvent:
		return eventClassification{typ: ev.Type, known: true}
	case *model.ToolCallStartEvent:
		return eventClassification{typ: ev.Type, known: true}
	case *model.ToolCallArgsEvent:
		return eventClassification{typ: ev.Type, known: true}
	case *model.ToolCallResultEvent:
		return eventClassification{typ: ev.Type, known: true}
	case *model.ToolCallEndEvent:
		return eventClassification{typ: ev.Type, known: true}
	case *model.ToolCallStitchedEvent:
		return eventClassification{typ: ev.Type, known: true}

	// Round boundaries.
	case *model.IterationStartedEvent:
		return eventClassification{typ: ev.Type, known: true, classB: true, isRoundStart: true}
	case *model.IterationCompletedEvent:
		return eventClassification{typ: ev.Type, known: true, classB: true, isRoundEnd: true}
	case *model.IterationFailedEvent:
		return eventClassification{typ: ev.Type, known: true, classB: true, isRoundEnd: true}

	// B-class — state deltas with their truth in job.json or session meta.
	case *model.JobStartedEvent:
		return eventClassification{typ: ev.Type, known: true, classB: true}
	case *model.JobCompletedEvent:
		return eventClassification{typ: ev.Type, known: true, classB: true, isTerminal: true}
	case *model.JobStoppedEvent:
		return eventClassification{typ: ev.Type, known: true, classB: true, isTerminal: true}
	case *model.JobFailedEvent:
		return eventClassification{typ: ev.Type, known: true, classB: true, isTerminal: true}
	case *model.RunStartedEvent:
		return eventClassification{typ: ev.Type, known: true, classB: true}
	case *model.RunFinishedEvent:
		return eventClassification{typ: ev.Type, known: true, classB: true}
	case *model.RunErrorEvent:
		return eventClassification{typ: ev.Type, known: true, classB: true}
	case *model.CustomEvent:
		// CustomEvent currently only carries token_usage and
		// job_title_updated. Treat as B-class either way; both have
		// their truth in job.json.
		return eventClassification{typ: ev.Type, known: true, classB: true}

	default:
		// Defensive: unknown event types should still buffer (avoid silent
		// loss). Treat as B-class outside any round so they reclaim quickly
		// once cursors cross. The publish path logs the one-time warning;
		// pure classification stays side-effect free for tests and predicates.
		return eventClassification{classB: true}
	}
}

var unknownBufferedEventTypes sync.Map

func warnUnknownBufferedEventType(event any) {
	typeName := "<nil>"
	if event != nil {
		typeName = fmt.Sprintf("%T", event)
	}
	if _, loaded := unknownBufferedEventTypes.LoadOrStore(typeName, struct{}{}); loaded {
		return
	}
	logger.Warnf(context.Background(), "[event_buffer] unregistered event type %s, treating as B-class", typeName)
}

// PublishTransient delivers a fire-and-forget event to all currently
// connected readers without buffering it. Transient events do not get a
// sequence number and never appear in the resume stream — they are used
// for slash-command results (e.g. ws switch) that must not reappear on
// page refresh.
func (b *jobEventBuffer) PublishTransient(event any) {
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
func (b *jobEventBuffer) MarkTerminal() {
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
func (b *jobEventBuffer) HasOpenRound() bool {
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
func (b *jobEventBuffer) ResumeGC() {
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
func (b *jobEventBuffer) IsFresh() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.nextSeq == 0 && !b.closed && !b.terminal
}

// Close releases the entire buffer and wakes all readers. Called when the
// job is explicitly deleted. After Close, Publish becomes a no-op and
// readers see ok=false from Read.
func (b *jobEventBuffer) Close() {
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
func (b *jobEventBuffer) SnapshotSeq() uint64 {
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

// BufferStats is a snapshot of a job event buffer's runtime state for
// observability (§6.1 item 10). Exposed read-only via buffer stats
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
func (b *jobEventBuffer) Stats() BufferStats {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.statsLocked()
}

func (b *jobEventBuffer) statsLocked() BufferStats {
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
func (b *jobEventBuffer) Subscribe(startSeq uint64) (*Reader, error) {
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

// Len returns the current number of events physically held in the buffer.
// Useful for lightweight growth checks without the overhead of full Stats().
func (b *jobEventBuffer) Len() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.events)
}
