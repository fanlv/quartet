package job

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/fanlv/quartet/types/model"
)

// helper: build a publish-classified event for tests.
func runStarted(jobID string) *model.RunStartedEvent {
	return &model.RunStartedEvent{
		BaseEvent: model.BaseEvent{Type: model.EventTypeRunStarted, JobID: jobID},
	}
}

func runFinished(jobID string) *model.RunFinishedEvent {
	return &model.RunFinishedEvent{
		BaseEvent: model.BaseEvent{Type: model.EventTypeRunFinished, JobID: jobID},
	}
}

func textDelta(jobID, msgID string) *model.TextMessageContentEvent {
	return &model.TextMessageContentEvent{
		BaseEvent: model.BaseEvent{Type: model.EventTypeTextMessageContent, JobID: jobID},
		MessageID: msgID,
		Delta:     "hi",
	}
}

func tokenUsage(jobID string) *model.CustomEvent {
	return &model.CustomEvent{
		BaseEvent: model.BaseEvent{Type: model.EventTypeCustom, JobID: jobID},
		Name:      "token_usage",
	}
}

func TestBuffer_PublishAssignsMonotonicSeq(t *testing.T) {
	b := newJobEventBuffer("job-1")
	defer b.Close()

	s1 := b.Publish(runStarted("job-1"))
	s2 := b.Publish(textDelta("job-1", "m1"))
	s3 := b.Publish(runFinished("job-1"))
	if s1 != 1 || s2 != 2 || s3 != 3 {
		t.Fatalf("expected seqs 1,2,3 got %d,%d,%d", s1, s2, s3)
	}
}

func TestBuffer_SubscribeFromTailDeliversNewEvents(t *testing.T) {
	b := newJobEventBuffer("job-1")
	defer b.Close()

	r, err := b.Subscribe(0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer r.Close()

	go func() {
		time.Sleep(10 * time.Millisecond)
		b.Publish(runStarted("job-1"))
		b.Publish(runFinished("job-1"))
	}()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// Reader intentionally provides at-least-once delivery until the caller
	// acknowledges a sequence. The two publishes can straddle two Read calls,
	// so mirror the SSE writer and acknowledge each delivered batch; otherwise
	// a second Read is allowed to return seq=1 again.
	got := make([]readEntry, 0, 2)
	for len(got) < 2 {
		batch, ok := r.Read(ctx, 2-len(got))
		if !ok {
			t.Fatalf("reader returned ok=false before 2 events (got %d)", len(got))
		}
		got = append(got, batch...)
		for _, entry := range batch {
			r.Ack(entry.Seq)
		}
	}
	if len(got) != 2 {
		t.Fatalf("expected 2 events, got %d", len(got))
	}
	if got[0].Seq != 1 || got[1].Seq != 2 {
		t.Fatalf("expected seqs 1,2 got %d,%d", got[0].Seq, got[1].Seq)
	}
}

func TestBuffer_SubscribeMidStreamGetsLaterEventsOnly(t *testing.T) {
	b := newJobEventBuffer("job-1")
	defer b.Close()

	// Publish 3 events with no subscriber. With no subscriber, B-class
	// outside any round (this includes a completed round's surrounding
	// state events) will GC; but a fresh subscriber starting at the
	// current tail (snapshotSeq) should not see anything yet.
	b.Publish(runStarted("job-1"))
	b.Publish(textDelta("job-1", "m1"))
	b.Publish(runFinished("job-1"))

	tail := b.SnapshotSeq()
	r, err := b.Subscribe(tail)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer r.Close()

	// New event after the snapshot — reader should receive only this.
	go func() {
		time.Sleep(10 * time.Millisecond)
		b.Publish(runStarted("job-1"))
	}()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got := drainReader(t, ctx, r, 1)
	if len(got) != 1 || got[0].Seq != tail+1 {
		t.Fatalf("expected one event at seq=%d, got %d events seq=%d", tail+1, len(got), seqOrZero(got))
	}
}

func TestBuffer_SnapshotSeqInsideRoundReturnsRoundStartMinus1(t *testing.T) {
	b := newJobEventBuffer("job-1")
	defer b.Close()

	b.Publish(runStarted("job-1")) // seq=1, round_start=1
	b.Publish(textDelta("job-1", "m1"))

	want := uint64(0) // round_start - 1
	if got := b.SnapshotSeq(); got != want {
		t.Fatalf("expected %d, got %d", want, got)
	}

	// Close the round → SnapshotSeq should now return tail.
	b.Publish(runFinished("job-1"))
	if got := b.SnapshotSeq(); got != 3 {
		t.Fatalf("expected tail seq=3, got %d", got)
	}
}

func TestBuffer_GCReclaimsClosedRoundWhenCursorAdvances(t *testing.T) {
	b := newJobEventBuffer("job-1")
	defer b.Close()

	r, err := b.Subscribe(0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer r.Close()

	// Publish a complete round.
	b.Publish(runStarted("job-1"))
	b.Publish(textDelta("job-1", "m1"))
	b.Publish(runFinished("job-1"))

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got := drainReader(t, ctx, r, 3)
	for _, e := range got {
		r.Ack(e.Seq)
	}

	// After ack of the round_end, GC should have reclaimed the round.
	b.mu.Lock()
	bufLen := len(b.events)
	headSeq := b.headSeq
	b.mu.Unlock()
	if bufLen != 0 {
		t.Fatalf("expected buffer empty after round GC, got %d events", bufLen)
	}
	if headSeq != 3 {
		t.Fatalf("expected headSeq=3, got %d", headSeq)
	}
}

func TestBuffer_BClassOutsideRoundReclaimsImmediately(t *testing.T) {
	b := newJobEventBuffer("job-1")
	defer b.Close()

	r, err := b.Subscribe(0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer r.Close()

	b.Publish(tokenUsage("job-1")) // B-class, outside any round
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got := drainReader(t, ctx, r, 1)
	r.Ack(got[0].Seq)

	b.mu.Lock()
	bufLen := len(b.events)
	b.mu.Unlock()
	if bufLen != 0 {
		t.Fatalf("expected B-class outside round to GC immediately, %d still buffered", bufLen)
	}
}

func TestBuffer_SlowReaderHoldsGCBack(t *testing.T) {
	b := newJobEventBuffer("job-1")
	defer b.Close()

	slow, err := b.Subscribe(0)
	if err != nil {
		t.Fatalf("subscribe slow: %v", err)
	}
	defer slow.Close()

	b.Publish(runStarted("job-1"))
	b.Publish(textDelta("job-1", "m1"))
	b.Publish(runFinished("job-1"))

	// Slow reader hasn't acked yet — buffer must keep all events.
	b.mu.Lock()
	bufLen := len(b.events)
	b.mu.Unlock()
	if bufLen != 3 {
		t.Fatalf("expected slow reader to pin 3 events, got %d", bufLen)
	}

	// Now drain + ack and the buffer reclaims.
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got := drainReader(t, ctx, slow, 3)
	for _, e := range got {
		slow.Ack(e.Seq)
	}
	b.mu.Lock()
	bufLen = len(b.events)
	b.mu.Unlock()
	if bufLen != 0 {
		t.Fatalf("expected buffer empty after slow reader caught up, got %d", bufLen)
	}
}

func TestBuffer_TerminalDisablesGC(t *testing.T) {
	b := newJobEventBuffer("job-1")
	defer b.Close()

	r, err := b.Subscribe(0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer r.Close()

	b.Publish(runStarted("job-1"))
	b.Publish(textDelta("job-1", "m1"))
	b.Publish(runFinished("job-1"))
	b.MarkTerminal()

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got := drainReader(t, ctx, r, 3)
	for _, e := range got {
		r.Ack(e.Seq)
	}
	b.mu.Lock()
	bufLen := len(b.events)
	b.mu.Unlock()
	if bufLen != 3 {
		t.Fatalf("expected terminal buffer to keep 3 events, got %d", bufLen)
	}
}

// TestBuffer_ResumeGCReenablesReclaim covers the SendMessage
// path: a job that hit a terminal status had MarkTerminal called, and
// the same buffer is reused for the new run. Without ResumeGC, gcLocked
// would short-circuit forever and the buffer would grow unbounded.
func TestBuffer_ResumeGCReenablesReclaim(t *testing.T) {
	b := newJobEventBuffer("job-1")
	defer b.Close()

	// Round 1 publishes and terminates (mirrors a finished interactive run).
	b.Publish(runStarted("job-1"))
	b.Publish(textDelta("job-1", "m1"))
	b.Publish(runFinished("job-1"))
	b.MarkTerminal()

	// Resume — like SendMessage on a terminal job.
	b.ResumeGC()

	// New subscriber attaches at the tail (no in-flight round → tail = 3).
	r, err := b.Subscribe(b.SnapshotSeq())
	if err != nil {
		t.Fatalf("subscribe after resume: %v", err)
	}
	defer r.Close()

	// Round 2 — chunks for the new run should be reclaimable once the
	// reader has acked round_end.
	b.Publish(runStarted("job-1"))
	b.Publish(textDelta("job-1", "m2"))
	b.Publish(runFinished("job-1"))

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got := drainReader(t, ctx, r, 3)
	for _, e := range got {
		r.Ack(e.Seq)
	}

	b.mu.Lock()
	bufLen := len(b.events)
	b.mu.Unlock()
	// The first round (events 1-3) gets reclaimed once the reader's
	// cursor crosses them — the cursor started at SnapshotSeq()=3 so they
	// were already past minCursor. Round 2 (events 4-6) reclaims after
	// round_end ack. Net: buffer empty.
	if bufLen != 0 {
		t.Fatalf("expected buffer empty after ResumeGC + acked round, got %d events", bufLen)
	}
}

// TestBuffer_ResumeGCNoReaderRace verifies the fix for the race condition
// where resumeGC + Publish (with no active readers) would advance headSeq,
// causing a subsequent Subscribe with the client's Last-Event-ID to fail
// with ErrSeqGone (410). The fix suspends GC until the first reader connects.
func TestBuffer_ResumeGCNoReaderRace(t *testing.T) {
	b := newJobEventBuffer("job-race")
	defer b.Close()

	// Simulate a completed prior run: publish events and mark terminal.
	b.Publish(runStarted("job-race"))
	b.Publish(textDelta("job-race", "hello"))
	lastSeq := b.Publish(runFinished("job-race"))
	b.MarkTerminal()

	// Client was subscribed but disconnected (no active readers).
	// The client's Last-Event-ID = lastSeq.

	// Now a new send_message triggers ResumeGC.
	b.ResumeGC()

	// Publish new events (like publishJobStarted would).
	b.Publish(runStarted("job-race"))
	b.Publish(textDelta("job-race", "world"))

	// The client reconnects with its old Last-Event-ID.
	// Without the fix, this would fail with ErrSeqGone because headSeq
	// would have advanced past lastSeq.
	r, err := b.Subscribe(lastSeq)
	if err != nil {
		t.Fatalf("subscribe after resumeGC with no readers should succeed, got: %v (headSeq advanced prematurely)", err)
	}
	r.Close()
}

func TestBuffer_SubscribeStaleSeqReturnsErrSeqGone(t *testing.T) {
	b := newJobEventBuffer("job-1")
	defer b.Close()

	r0, err := b.Subscribe(0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	b.Publish(runStarted("job-1"))
	b.Publish(textDelta("job-1", "m1"))
	b.Publish(runFinished("job-1"))

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got := drainReader(t, ctx, r0, 3)
	for _, e := range got {
		r0.Ack(e.Seq)
	}
	r0.Close()

	// All events have been GC'd to head=3. Subscribing at any seq < head
	// must 410 because the next event the reader needs (seq+1) is gone.
	if _, err := b.Subscribe(0); !errors.Is(err, ErrSeqGone) {
		t.Fatalf("expected ErrSeqGone for seq=0 (head=3), got %v", err)
	}
	if _, err := b.Subscribe(1); !errors.Is(err, ErrSeqGone) {
		t.Fatalf("expected ErrSeqGone for seq=1 (head=3), got %v", err)
	}
	// Subscribing exactly at head is fine: the next event hasn't arrived
	// yet, but cursor==head means we're caught up.
	r1, err := b.Subscribe(3)
	if err != nil {
		t.Fatalf("subscribe at exactly head should succeed, got %v", err)
	}
	r1.Close()
}

// TestBuffer_SubscribeFutureSeqReturnsErrSeqGone covers the buffer-epoch
// boundary: after the buffer is recreated (job deleted), a client reconnecting
// with a Last-Event-ID from the previous epoch must see 410, not silently
// block until the new run catches up. Without this guard the reader would
// wait for nextSeq > startSeq and then skip events 1..startSeq of the
// fresh run via indexAfter — invisible data loss for the client.
func TestBuffer_SubscribeFutureSeqReturnsErrSeqGone(t *testing.T) {
	b := newJobEventBuffer("job-1")
	defer b.Close()

	// Fresh buffer: nextSeq=0. A startSeq of 500 (from a prior epoch) is
	// outside the [headSeq, nextSeq] window and must be rejected.
	if _, err := b.Subscribe(500); !errors.Is(err, ErrSeqGone) {
		t.Fatalf("expected ErrSeqGone for startSeq=500 on fresh buffer, got %v", err)
	}

	// After publishing 2 events nextSeq=2; startSeq=3 is still in the
	// future and must 410.
	b.Publish(runStarted("job-1"))
	b.Publish(runFinished("job-1"))
	if _, err := b.Subscribe(3); !errors.Is(err, ErrSeqGone) {
		t.Fatalf("expected ErrSeqGone for startSeq=3 (nextSeq=2), got %v", err)
	}

	// startSeq == nextSeq is the valid "subscribe at tail" case.
	r, err := b.Subscribe(2)
	if err != nil {
		t.Fatalf("subscribe at tail should succeed, got %v", err)
	}
	r.Close()
}

func TestBuffer_PublishTransientDoesNotEnterBuffer(t *testing.T) {
	b := newJobEventBuffer("job-1")
	defer b.Close()

	r, err := b.Subscribe(0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer r.Close()

	b.PublishTransient(&model.CommandSystemMessageEvent{Text: "ws switched"})

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	got := drainReader(t, ctx, r, 1)
	if len(got) != 1 || !got[0].Transient || got[0].Seq != 0 {
		t.Fatalf("expected one transient event with seq=0, got %+v", got)
	}
	b.mu.Lock()
	bufLen := len(b.events)
	b.mu.Unlock()
	if bufLen != 0 {
		t.Fatalf("transient event must not enter buffer, got %d", bufLen)
	}
}

func TestBuffer_CloseUnblocksReaders(t *testing.T) {
	b := newJobEventBuffer("job-1")
	r, err := b.Subscribe(0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	var wg sync.WaitGroup
	wg.Add(1)
	go func() {
		defer wg.Done()
		ctx, cancel := context.WithTimeout(context.Background(), time.Second)
		defer cancel()
		_, ok := r.Read(ctx, 10)
		if ok {
			t.Errorf("expected ok=false after Close")
		}
	}()
	time.Sleep(10 * time.Millisecond)
	b.Close()
	wg.Wait()
}

func TestBuffer_ReadWithTimeout(t *testing.T) {
	t.Run("returns ReadOK when events are available", func(t *testing.T) {
		b := newJobEventBuffer("job-1")
		defer b.Close()
		r, err := b.Subscribe(0)
		if err != nil {
			t.Fatalf("subscribe: %v", err)
		}
		defer r.Close()

		b.Publish(runStarted("job-1"))
		entries, status := r.ReadWithTimeout(context.Background(), time.Second, 10)
		if status != ReadOK {
			t.Fatalf("status: got %v want ReadOK", status)
		}
		if len(entries) != 1 {
			t.Fatalf("entries: got %d want 1", len(entries))
		}
	})

	t.Run("returns ReadTimeout when nothing arrives but reader stays alive", func(t *testing.T) {
		b := newJobEventBuffer("job-1")
		defer b.Close()
		r, err := b.Subscribe(0)
		if err != nil {
			t.Fatalf("subscribe: %v", err)
		}
		defer r.Close()

		entries, status := r.ReadWithTimeout(context.Background(), 50*time.Millisecond, 10)
		if status != ReadTimeout {
			t.Fatalf("status: got %v want ReadTimeout", status)
		}
		if entries != nil {
			t.Fatalf("entries: got %v want nil", entries)
		}

		b.Publish(runStarted("job-1"))
		entries, status = r.ReadWithTimeout(context.Background(), time.Second, 10)
		if status != ReadOK || len(entries) != 1 {
			t.Fatalf("after publish: status=%v entries=%d", status, len(entries))
		}
	})

	t.Run("returns ReadClosed when parent ctx is cancelled", func(t *testing.T) {
		b := newJobEventBuffer("job-1")
		defer b.Close()
		r, err := b.Subscribe(0)
		if err != nil {
			t.Fatalf("subscribe: %v", err)
		}
		defer r.Close()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		_, status := r.ReadWithTimeout(ctx, time.Second, 10)
		if status != ReadClosed {
			t.Fatalf("status: got %v want ReadClosed", status)
		}
	})

	t.Run("returns ReadClosed when buffer closes mid-wait", func(t *testing.T) {
		b := newJobEventBuffer("job-1")
		r, err := b.Subscribe(0)
		if err != nil {
			t.Fatalf("subscribe: %v", err)
		}

		var wg sync.WaitGroup
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, status := r.ReadWithTimeout(context.Background(), time.Second, 10)
			if status != ReadClosed {
				t.Errorf("status: got %v want ReadClosed", status)
			}
		}()
		time.Sleep(10 * time.Millisecond)
		b.Close()
		wg.Wait()
	})
}

func TestBuffer_ConcurrentPublishKeepsOrder(t *testing.T) {
	b := newJobEventBuffer("job-1")
	defer b.Close()

	r, err := b.Subscribe(0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}
	defer r.Close()

	var wg sync.WaitGroup
	const n = 200
	wg.Add(n)
	// Publishers are serialized internally by the buffer's mu, so even
	// if multiple goroutines call Publish concurrently, the assigned
	// seq must remain monotonic — the test asserts that the reader sees
	// strictly increasing seqs.
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			b.Publish(tokenUsage("job-1"))
		}()
	}
	wg.Wait()

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	got := drainReader(t, ctx, r, n)
	if len(got) != n {
		t.Fatalf("expected %d events, got %d", n, len(got))
	}
	prev := uint64(0)
	for _, e := range got {
		if e.Seq <= prev {
			t.Fatalf("seq not monotonic: prev=%d, current=%d", prev, e.Seq)
		}
		prev = e.Seq
	}
}

// TestBuffer_GCGapDoesNotBusyLoop verifies that when a reader's cursor is
// behind nextSeq but all events in that range have been GC'd, Read blocks
// (advancing cursor past the gap) instead of returning empty with ok=true
// repeatedly — which would cause a tight busy loop in the SSE handler.
func TestBuffer_GCGapDoesNotBusyLoop(t *testing.T) {
	b := newJobEventBuffer("job-1")
	defer b.Close()

	// Publish a round with a reader present so we can advance cursor and GC.
	r1, err := b.Subscribe(0)
	if err != nil {
		t.Fatalf("subscribe: %v", err)
	}

	b.Publish(runStarted("job-1"))      // seq=1
	b.Publish(textDelta("job-1", "m1")) // seq=2
	b.Publish(runFinished("job-1"))     // seq=3, closes round

	ctx1, cancel1 := context.WithTimeout(context.Background(), time.Second)
	defer cancel1()
	got := drainReader(t, ctx1, r1, 3)
	for _, e := range got {
		r1.Ack(e.Seq)
	}
	// Reader has cursor=3. Close it so GC has no readers.
	r1.Close()

	// Buffer state: headSeq=3 (all GC'd), nextSeq=3, events=[], terminal=false.
	// Simulate the bug scenario: resumeGC + fast run with no active readers.
	// Publish a terminal event (B-class, outside round → immediately GC'd).
	b.Publish(tokenUsage("job-1")) // seq=4, B-class outside round → GC'd immediately
	b.MarkTerminal()

	// State: headSeq=4, nextSeq=4, events=[], terminal=true actually
	// Wait — tokenUsage is CustomEvent. Let's check if it's B-class...
	// Use SnapshotSeq to get the right subscribe point.
	snapSeq := b.SnapshotSeq()

	r2, err := b.Subscribe(snapSeq)
	if err != nil {
		t.Fatalf("subscribe at snapSeq=%d: %v", snapSeq, err)
	}
	defer r2.Close()

	// If the buffer's cursor == nextSeq, Read would block normally.
	// If cursor < nextSeq but events are GC'd, the fix must make Read
	// block (via cursor advancement) instead of busy-looping.
	// Either way, with no new events, ReadWithTimeout should return ReadTimeout.
	entries, status := r2.ReadWithTimeout(context.Background(), 50*time.Millisecond, 32)
	if status != ReadTimeout {
		t.Fatalf("expected ReadTimeout (no events), got status=%v entries=%v", status, entries)
	}

	// Now publish a new event and verify it's delivered.
	b.ResumeGC()
	b.Publish(textDelta("job-1", "m3"))

	ctx2, cancel2 := context.WithTimeout(context.Background(), time.Second)
	defer cancel2()
	entries, status = r2.ReadWithTimeout(ctx2, time.Second, 32)
	if status != ReadOK {
		t.Fatalf("expected ReadOK after new publish, got status=%v", status)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least one event after new publish")
	}
}

// drainReader keeps calling Read until n entries have been collected or ctx is done.
func drainReader(t *testing.T, ctx context.Context, r *bufferReader, n int) []readEntry {
	t.Helper()
	got := make([]readEntry, 0, n)
	for len(got) < n {
		batch, ok := r.Read(ctx, n-len(got))
		if !ok {
			t.Fatalf("reader returned ok=false before %d events (got %d)", n, len(got))
		}
		got = append(got, batch...)
	}
	return got
}

func seqOrZero(e []readEntry) uint64 {
	if len(e) == 0 {
		return 0
	}
	return e[0].Seq
}
