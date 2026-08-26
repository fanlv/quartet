package job

import (
	"context"
	"strconv"
	"time"

	"github.com/fanlv/quartet/pkg/logger"
)

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
func (b *jobEventBuffer) gcLocked() {
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
			// unbounded memory growth (OOM risk for long-running jobs).
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
func (b *jobEventBuffer) minCursorLocked() uint64 {
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
