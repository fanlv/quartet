package job

import (
	"context"
	"time"
)

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
	buf       *jobEventBuffer
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

	// Wait until something is available. sync.Cond cannot wait on ctx directly,
	// so bridge ctx cancellation to a broadcast. context.AfterFunc avoids the
	// per-Read goroutine churn of the traditional ctx.Done() watcher.
	stopCtxWake := context.AfterFunc(ctx, func() {
		r.buf.mu.Lock()
		r.buf.cond.Broadcast()
		r.buf.mu.Unlock()
	})
	defer stopCtxWake()

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
