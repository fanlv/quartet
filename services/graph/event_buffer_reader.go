package graph

import (
	"context"
	"time"

	"github.com/fanlv/quartet/types/model"
)

// GraphEventReader is the SSE handler's view of a buffer subscription. It is
// satisfied by *graphReader and lets the handler stream events without
// importing buffer internals.
type GraphEventReader interface {
	// ReadWithTimeout returns up to maxN buffered events after the cursor, or a
	// timeout/closed status. See graphReader.ReadWithTimeout.
	ReadWithTimeout(ctx context.Context, timeout time.Duration, maxN int) ([]GraphReadEntry, GraphReadStatus)
	// Ack advances the cursor past seq (forward only) and triggers GC.
	Ack(seq uint64)
	// Close detaches the reader from its buffer.
	Close()
}

// GraphReadEntry is one delivered event with its sequence. Exported for the SSE
// handler.
type GraphReadEntry struct {
	Seq   uint64
	Event *model.GraphEvent
}

// GraphReadStatus communicates how a ReadWithTimeout call ended.
type GraphReadStatus int

const (
	// GraphReadOK means the call returned at least one entry.
	GraphReadOK GraphReadStatus = iota
	// GraphReadTimeout means the per-call deadline elapsed before any event
	// arrived. The reader is still alive — the caller may loop and read again
	// (e.g. to write an SSE keep-alive from the single writer goroutine).
	GraphReadTimeout
	// GraphReadClosed means the parent ctx was cancelled or the buffer/reader
	// was closed. The caller should release the reader and exit.
	GraphReadClosed
)

// graphReader is one SSE subscriber's read handle on a graphEventBuffer. The
// cursor advances only after Ack, giving the SSE writer "at-least-once with
// cursor rewind" semantics: a network-write failure can re-Read the same event
// after reconnect.
type graphReader struct {
	buf    *graphEventBuffer
	cursor uint64
	closed bool
}

var _ GraphEventReader = (*graphReader)(nil)

// Read blocks until at least one event is available, the context is cancelled,
// or the buffer is closed. Returns up to maxN entries starting after cursor.
// ok=false means the reader/buffer was closed or ctx fired before any event.
func (r *graphReader) Read(ctx context.Context, maxN int) ([]GraphReadEntry, bool) {
	if maxN <= 0 {
		maxN = 1
	}
	r.buf.mu.Lock()
	defer r.buf.mu.Unlock()

	stopCtxWake := context.AfterFunc(ctx, func() {
		r.buf.mu.Lock()
		r.buf.cond.Broadcast()
		r.buf.mu.Unlock()
	})
	defer stopCtxWake()

	for {
		for {
			if r.closed || r.buf.closed {
				return nil, false
			}
			if ctx.Err() != nil {
				return nil, false
			}
			if r.cursor < r.buf.nextSeq {
				break
			}
			r.buf.cond.Wait()
		}

		out := make([]GraphReadEntry, 0, maxN)
		if r.cursor < r.buf.nextSeq {
			i := graphIndexAfter(r.buf.events, r.cursor)
			for i < len(r.buf.events) && len(out) < maxN {
				e := r.buf.events[i]
				out = append(out, GraphReadEntry{Seq: e.seq, Event: e.event})
				i++
			}
		}
		if len(out) > 0 {
			return out, true
		}

		// GC gap: cursor < nextSeq but everything in (cursor, nextSeq) was GC'd.
		// Advance past the gap and re-block so we don't busy-loop.
		r.cursor = r.buf.nextSeq
	}
}

// graphIndexAfter returns the index of the first event with seq > cursor.
// events is sorted by seq ascending (append-only with monotonic seq).
func graphIndexAfter(events []bufferedGraphEvent, cursor uint64) int {
	for i, e := range events {
		if e.seq > cursor {
			return i
		}
	}
	return len(events)
}

// ReadWithTimeout behaves like Read but returns GraphReadTimeout if no event
// becomes available within timeout (the reader stays alive). A cancelled parent
// ctx or a closed buffer/reader returns GraphReadClosed. timeout <= 0 blocks
// like plain Read.
func (r *graphReader) ReadWithTimeout(ctx context.Context, timeout time.Duration, maxN int) ([]GraphReadEntry, GraphReadStatus) {
	if timeout <= 0 {
		entries, ok := r.Read(ctx, maxN)
		if !ok {
			return nil, GraphReadClosed
		}
		return entries, GraphReadOK
	}
	childCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	entries, ok := r.Read(childCtx, maxN)
	if ok {
		return entries, GraphReadOK
	}
	if ctx.Err() != nil {
		return nil, GraphReadClosed
	}
	r.buf.mu.Lock()
	closed := r.closed || r.buf.closed
	r.buf.mu.Unlock()
	if closed {
		return nil, GraphReadClosed
	}
	return nil, GraphReadTimeout
}

// Ack marks seq as successfully delivered, advancing the cursor (forward only)
// and triggering GC re-evaluation.
func (r *graphReader) Ack(seq uint64) {
	r.buf.mu.Lock()
	defer r.buf.mu.Unlock()
	if seq > r.cursor {
		r.cursor = seq
	}
	r.buf.gcLocked()
}

// Close removes this reader from the buffer and wakes any in-flight Read.
func (r *graphReader) Close() {
	r.buf.mu.Lock()
	if !r.closed {
		r.closed = true
		delete(r.buf.readers, r)
		if len(r.buf.readers) == 0 && !r.buf.terminal {
			r.buf.lastReaderLeftAt = time.Now()
		}
		r.buf.gcLocked()
	}
	r.buf.cond.Broadcast()
	r.buf.mu.Unlock()
}
