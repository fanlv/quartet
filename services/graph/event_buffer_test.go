package graph

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/fanlv/quartet/types/model"
)

func graphRunTerminalForTest(s model.GraphRunStatus) bool {
	switch s {
	case model.GraphRunStatusCompleted, model.GraphRunStatusFailed,
		model.GraphRunStatusStopped, model.GraphRunStatusPaused,
		model.GraphRunStatusStepStopped, model.GraphRunStatusTimedOut:
		return true
	default:
		return false
	}
}

func testGraphEvent(typ model.GraphEventType) *model.GraphEvent {
	return &model.GraphEvent{Type: typ, CreatedAt: 1}
}

func TestGraphEventBufferSeqMonotonicAndRead(t *testing.T) {
	b := newGraphEventBuffer("run-1")
	r, err := b.Subscribe(0)
	if err != nil {
		t.Fatalf("subscribe failed: %v", err)
	}
	defer r.Close()

	var seqs []uint64
	for i := 0; i < 5; i++ {
		seqs = append(seqs, b.Publish(testGraphEvent(model.GraphEventTypeLog)))
	}
	for i := 1; i < len(seqs); i++ {
		if seqs[i] != seqs[i-1]+1 {
			t.Fatalf("seq not monotonic: %v", seqs)
		}
	}
	if seqs[0] != 1 {
		t.Fatalf("first seq = %d, want 1", seqs[0])
	}

	got := 0
	for got < 5 {
		entries, status := r.ReadWithTimeout(context.Background(), time.Second, 10)
		if status == GraphReadClosed {
			t.Fatalf("reader closed early after %d events", got)
		}
		for _, e := range entries {
			r.Ack(e.Seq)
			got++
		}
	}
}

func TestGraphEventBufferMultiReader(t *testing.T) {
	b := newGraphEventBuffer("run-1")
	r1, _ := b.Subscribe(0)
	r2, _ := b.Subscribe(0)
	defer r1.Close()
	defer r2.Close()

	const n = 4
	for i := 0; i < n; i++ {
		b.Publish(testGraphEvent(model.GraphEventTypeInstanceStarted))
	}

	var wg sync.WaitGroup
	read := func(r *graphReader) {
		defer wg.Done()
		got := 0
		for got < n {
			entries, status := r.ReadWithTimeout(context.Background(), time.Second, 10)
			if status == GraphReadClosed {
				return
			}
			for _, e := range entries {
				r.Ack(e.Seq)
				got++
			}
		}
	}
	wg.Add(2)
	go read(r1)
	go read(r2)
	wg.Wait()
}

func TestGraphEventBufferAckAdvancesGC(t *testing.T) {
	b := newGraphEventBuffer("run-1")
	r, _ := b.Subscribe(0)
	defer r.Close()

	for i := 0; i < 3; i++ {
		b.Publish(testGraphEvent(model.GraphEventTypeLog))
	}
	// Read + ack all three so the cursor crosses them.
	got := 0
	for got < 3 {
		entries, _ := r.ReadWithTimeout(context.Background(), time.Second, 10)
		for _, e := range entries {
			r.Ack(e.Seq)
			got++
		}
	}
	// After acks, the head prefix all readers crossed should be reclaimed.
	if b.Len() != 0 {
		t.Fatalf("buffer len = %d after acking all, want 0 (GC should reclaim crossed prefix)", b.Len())
	}
}

func TestGraphEventBufferSubscribeErrSeqGone(t *testing.T) {
	b := newGraphEventBuffer("run-1")
	// Publish with no readers; GC reclaims immediately (minCursor=MaxUint64).
	for i := 0; i < 5; i++ {
		b.Publish(testGraphEvent(model.GraphEventTypeLog))
	}
	// headSeq has advanced; subscribing from 0 (< headSeq) must be rejected.
	if _, err := b.Subscribe(0); err != ErrSeqGone {
		t.Fatalf("Subscribe(0) after GC = %v, want ErrSeqGone", err)
	}
	// A seq from the future must be rejected.
	if _, err := b.Subscribe(b.nextSeq + 100); err != ErrSeqGone {
		t.Fatalf("Subscribe(future) = %v, want ErrSeqGone", err)
	}
	// The current tail is always valid.
	if _, err := b.Subscribe(b.nextSeq); err != nil {
		t.Fatalf("Subscribe(tail) = %v, want nil", err)
	}
}

func TestGraphEventBufferTerminalStopsGC(t *testing.T) {
	b := newGraphEventBuffer("run-1")
	b.MarkTerminal()
	for i := 0; i < 5; i++ {
		b.Publish(testGraphEvent(model.GraphEventTypeLog))
	}
	// Terminal buffers never GC, so all events stay (allowing late refreshes).
	if b.Len() != 5 {
		t.Fatalf("terminal buffer len = %d, want 5 (GC must be off)", b.Len())
	}
	// A fresh subscriber after terminal can still replay from seq 0.
	if _, err := b.Subscribe(0); err != nil {
		t.Fatalf("Subscribe(0) on terminal buffer = %v, want nil", err)
	}
}

func TestGraphEventBufferReplayGapLimit(t *testing.T) {
	b := newGraphEventBuffer("run-1")
	r, _ := b.Subscribe(0) // hold a reader so GC can't advance headSeq
	defer r.Close()
	for i := 0; i < graphReplayGapLimit+10; i++ {
		b.Publish(testGraphEvent(model.GraphEventTypeLog))
	}
	// A second subscriber resuming from 0 is now too far behind the tail.
	if _, err := b.Subscribe(0); err != ErrSeqGone {
		t.Fatalf("Subscribe(0) beyond replay gap = %v, want ErrSeqGone", err)
	}
}

func TestGraphEventBufferCloseWakesReader(t *testing.T) {
	b := newGraphEventBuffer("run-1")
	r, _ := b.Subscribe(0)
	done := make(chan graphReadStatusResult, 1)
	go func() {
		entries, status := r.ReadWithTimeout(context.Background(), 2*time.Second, 10)
		done <- graphReadStatusResult{entries: entries, status: status}
	}()
	time.Sleep(50 * time.Millisecond)
	b.Close()
	select {
	case res := <-done:
		if res.status != GraphReadClosed {
			t.Fatalf("read after Close status = %v, want GraphReadClosed", res.status)
		}
	case <-time.After(time.Second):
		t.Fatal("reader was not woken by Close")
	}
}

type graphReadStatusResult struct {
	entries []GraphReadEntry
	status  GraphReadStatus
}
