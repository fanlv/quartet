package sessioncache

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// fakeEntry is a minimal *Entry double: we only need IsRunning and Close,
// plus a handle so tests can flip running on and assert Close was called.
type fakeEntry struct {
	id      string
	running atomic.Bool
	closed  atomic.Bool
}

func (f *fakeEntry) IsRunning() bool { return f.running.Load() }
func (f *fakeEntry) Close()          { f.closed.Store(true) }

func newEntry(id string) *fakeEntry { return &fakeEntry{id: id} }

// TestCache_EvictIfFullLocked_UsesConfiguredCapAndSkipsRunning ports the
// old eino-level test for the core LRU contract: when capacity is hit,
// pick the least-recently-used non-running entry, skip the keeper id,
// and never evict an entry whose IsRunning reports true.
func TestCache_EvictIfFullLocked_UsesConfiguredCapAndSkipsRunning(t *testing.T) {
	c := New[*fakeEntry](2)

	now := time.Now()

	busy := newEntry("busy")
	busy.running.Store(true)
	addBox(c, "busy", busy, now.Add(-2*time.Hour).UnixNano())

	old := newEntry("old")
	addBox(c, "old", old, now.Add(-time.Hour).UnixNano())

	keep := newEntry("keep")
	addBox(c, "keep", keep, now.UnixNano())

	c.mu.Lock()
	victim, ok := c.evictIfFullLocked("keep")
	c.mu.Unlock()

	if !ok || victim == nil || victim.v != old {
		t.Fatalf("victim=%v ok=%v, want old/true", victim, ok)
	}
	if l, stillBusy := c.Get("busy"); !stillBusy {
		t.Fatalf("running agent must not be evicted")
	} else {
		l.Release()
	}
	if l, stillOld := c.Get("old"); stillOld {
		l.Release()
		t.Fatalf("evicted agent must be removed from cache")
	}
}

// TestCache_GetOrCreate_SingleflightDedup proves that concurrent
// GetOrCreate calls for the same sessionID share ONE ctor invocation.
// The ctor counts its calls; with N goroutines racing on the same id,
// the count must be exactly 1 and every goroutine must see the same
// entry pointer.
func TestCache_GetOrCreate_SingleflightDedup(t *testing.T) {
	c := New[*fakeEntry](8)

	var ctorCalls atomic.Int32
	ctor := func(ctx context.Context) (*fakeEntry, error) {
		ctorCalls.Add(1)
		// Small sleep so concurrent callers pile onto the singleflight
		// group before the first one completes — otherwise the test
		// trivially hits the fast path.
		time.Sleep(30 * time.Millisecond)
		return newEntry("singleton"), nil
	}

	const N = 16
	var wg sync.WaitGroup
	results := make([]*fakeEntry, N)
	for i := range N {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			lease, err := c.GetOrCreate(context.Background(), "same-id", ctor)
			if err != nil {
				t.Errorf("GetOrCreate: %v", err)
				return
			}
			results[i] = lease.Value
			lease.Release()
		}(i)
	}
	wg.Wait()

	if n := ctorCalls.Load(); n != 1 {
		t.Fatalf("ctor called %d times, want 1 (singleflight broken)", n)
	}
	for i := 1; i < N; i++ {
		if results[i] != results[0] {
			t.Fatalf("goroutine %d got different entry: %v vs %v", i, results[i], results[0])
		}
	}
}

// TestCache_GetOrCreate_CapacityExceeded covers the path where every
// existing entry is running (so none can be evicted) and the cache is
// at capacity: the freshly-created entry must be Close()'d and the
// caller must see ErrCapacityExceeded.
func TestCache_GetOrCreate_CapacityExceeded(t *testing.T) {
	c := New[*fakeEntry](1)

	// Pre-fill with a running entry so eviction is blocked.
	running := newEntry("hot")
	running.running.Store(true)
	addBox(c, "hot", running, time.Now().UnixNano())

	var createdRef *fakeEntry
	_, err := c.GetOrCreate(context.Background(), "new", func(ctx context.Context) (*fakeEntry, error) {
		createdRef = newEntry("new")
		return createdRef, nil
	})
	if !errors.Is(err, ErrCapacityExceeded) {
		t.Fatalf("want ErrCapacityExceeded, got %v", err)
	}
	// The just-built entry that couldn't be admitted must be released
	// so its underlying resources (subprocess, connection, ...) don't leak.
	if createdRef == nil || !createdRef.closed.Load() {
		t.Fatalf("created entry must be Close()'d on rejection, closed=%v", createdRef)
	}
}

// TestCache_GetOrCreate_DoubleCheckClosesLoser drives the race where two
// goroutines both get past the first RLock check, both run the ctor
// under singleflight (the ctor here is invoked once via singleflight, but
// this test pretends a concurrent direct insert happened). We simulate
// by pre-inserting after the ctor decides to proceed: the cache must
// detect the winner under the final write lock and Close the loser.
func TestCache_GetOrCreate_DoubleCheckClosesLoser(t *testing.T) {
	c := New[*fakeEntry](4)

	// Winner is inserted out-of-band while the "racing" ctor is in
	// flight. The ctor sleeps so we have time to inject the winner.
	winner := newEntry("winner")

	var created *fakeEntry
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, err := c.GetOrCreate(context.Background(), "race", func(ctx context.Context) (*fakeEntry, error) {
			time.Sleep(50 * time.Millisecond)
			created = newEntry("loser")
			return created, nil
		})
		if err != nil {
			t.Errorf("GetOrCreate: %v", err)
		}
	}()

	// Inject winner under the write lock while ctor sleeps.
	time.Sleep(10 * time.Millisecond)
	addBox(c, "race", winner, time.Now().UnixNano())

	<-done

	if created == nil || !created.closed.Load() {
		t.Fatalf("loser must be Close()'d on double-check miss, created=%v", created)
	}
	got, ok := c.Get("race")
	if !ok || got == nil || got.Value != winner {
		t.Fatalf("cache must still hold the winner, got=%v ok=%v", got, ok)
	}
	got.Release()
}

// TestCache_Delete_ClosesEntry ensures Delete calls Close on the removed
// entry (otherwise subprocess / connection resources leak on session end).
func TestCache_Delete_ClosesEntry(t *testing.T) {
	c := New[*fakeEntry](4)
	e := newEntry("x")
	addBox(c, "x", e, time.Now().UnixNano())

	c.Delete("x")

	if !e.closed.Load() {
		t.Fatalf("Delete must Close the entry")
	}
	if l, ok := c.Get("x"); ok {
		l.Release()
		t.Fatalf("entry must be gone after Delete")
	}
}

// TestEnvInt covers the invalid-fallback behaviour that used to live in
// each manager: empty, non-numeric, zero and negative values all return
// the default.
func TestEnvInt(t *testing.T) {
	const key = "TEST_ENVINT_KEY"
	cases := map[string]int{"": 5, "abc": 5, "0": 5, "-3": 5, "7": 7}
	for raw, want := range cases {
		t.Setenv(key, raw)
		if got := EnvInt(key, 5); got != want {
			t.Fatalf("EnvInt(%q) = %d, want %d", raw, got, want)
		}
	}
}

// addBox is a test helper that inserts a value directly into the cache
// map without going through GetOrCreate. Used by tests that need to
// construct a specific pre-state (capacity pressure, running flags, ...)
// before exercising the code under test.
func addBox[T Entry](c *Cache[T], id string, v T, accessNanos int64) {
	c.mu.Lock()
	defer c.mu.Unlock()
	box := &entryBox[T]{v: v}
	box.lastAccess.Store(accessNanos)
	c.entries[id] = box
}

// TestCache_Eviction_SkipsLeasedEntries proves the close-under-use fix:
// while a Lease is outstanding for an idle entry, eviction must not pick
// it even when it is the LRU candidate. Without this guard, a concurrent
// GetOrCreate that triggers eviction could Close the agent under the
// borrower's still-pending Run() call.
func TestCache_Eviction_SkipsLeasedEntries(t *testing.T) {
	c := New[*fakeEntry](2)

	old := newEntry("old")
	addBox(c, "old", old, time.Now().Add(-time.Hour).UnixNano())

	idle := newEntry("idle")
	addBox(c, "idle", idle, time.Now().Add(-30*time.Minute).UnixNano())

	// Borrow "old" via Get — it's now leased even though IsRunning is
	// false. With the lease held, eviction must skip it and pick "idle"
	// instead, even though "old" is the LRU candidate.
	lease, ok := c.Get("old")
	if !ok {
		t.Fatalf("Get(old) must succeed")
	}

	c.mu.Lock()
	victim, evicted := c.evictIfFullLocked("new")
	c.mu.Unlock()
	if !evicted {
		t.Fatalf("eviction must succeed when an unleased non-running entry exists")
	}
	if victim.v == old {
		t.Fatalf("eviction must not pick the leased entry")
	}
	if victim.v != idle {
		t.Fatalf("eviction must pick the idle entry, got %v", victim.v)
	}
	if old.closed.Load() {
		t.Fatalf("leased entry must not be closed by eviction")
	}
	lease.Release()
}

// TestCache_Delete_DefersCloseUntilLastRelease proves Delete does not
// Close an entry while leases are outstanding — the close is deferred
// to the last Release. This prevents tearing down an ACP connection
// under a goroutine that is still using it.
func TestCache_Delete_DefersCloseUntilLastRelease(t *testing.T) {
	c := New[*fakeEntry](4)
	e := newEntry("x")
	addBox(c, "x", e, time.Now().UnixNano())

	lease, ok := c.Get("x")
	if !ok {
		t.Fatalf("Get must succeed")
	}

	c.Delete("x")

	if e.closed.Load() {
		t.Fatalf("Delete must NOT close while leases are outstanding")
	}
	if _, ok := c.Get("x"); ok {
		t.Fatalf("entry must be gone from the map immediately on Delete")
	}

	lease.Release()

	if !e.closed.Load() {
		t.Fatalf("entry must be closed after the last Release")
	}
}

// TestCache_Lease_Release_Idempotent proves that double-Release is a
// no-op. Callers writing `defer lease.Release()` next to an explicit
// Release in an error path must not corrupt the refcount.
func TestCache_Lease_Release_Idempotent(t *testing.T) {
	c := New[*fakeEntry](4)
	e := newEntry("x")
	addBox(c, "x", e, time.Now().UnixNano())

	lease, _ := c.Get("x")
	lease.Release()
	lease.Release() // must not panic and must not over-decrement
	lease.Release()

	c.Delete("x")
	if !e.closed.Load() {
		t.Fatalf("entry must be closed cleanly after Delete with no live leases")
	}
}

// TestCache_GetOrCreate_RespectsCallerCtx proves that a caller cancel
// during agent creation returns immediately with ctx.Err rather than
// blocking up to the (60s) create timeout.
func TestCache_GetOrCreate_RespectsCallerCtx(t *testing.T) {
	c := New[*fakeEntry](4)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		_, err := c.GetOrCreate(ctx, "slow", func(createCtx context.Context) (*fakeEntry, error) {
			// Simulate slow creation that ignores caller cancellation
			// (real agents do mostly synchronous I/O during construction).
			time.Sleep(2 * time.Second)
			return newEntry("slow"), nil
		})
		done <- err
	}()

	// Cancel quickly; the call must return promptly.
	time.Sleep(20 * time.Millisecond)
	cancel()

	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("GetOrCreate did not return after caller cancel")
	}
}

// TestCache_GetOrCreate_AbandonedCreateDoesNotLeakLease proves that when
// the caller cancels but the background ctor finishes anyway, the
// freshly-created entry is NOT pinned by a phantom lease. With the new
// design (each waiter does its own acquireRef post-Do), a cancelled
// caller never acquires a ref in the first place, so refs stays at 0
// and the entry can be Delete'd / evicted normally.
func TestCache_GetOrCreate_AbandonedCreateDoesNotLeakLease(t *testing.T) {
	c := New[*fakeEntry](4)

	ctx, cancel := context.WithCancel(context.Background())

	createDone := make(chan *fakeEntry, 1)
	callerDone := make(chan error, 1)
	go func() {
		_, err := c.GetOrCreate(ctx, "abandoned", func(createCtx context.Context) (*fakeEntry, error) {
			// Long enough to be still mid-flight when caller cancels,
			// but still completes within the test window.
			time.Sleep(80 * time.Millisecond)
			e := newEntry("abandoned")
			createDone <- e
			return e, nil
		})
		callerDone <- err
	}()

	// Cancel before the ctor returns: caller takes the ctx.Done branch
	// and returns immediately, but the singleflight goroutine keeps going.
	time.Sleep(10 * time.Millisecond)
	cancel()

	select {
	case err := <-callerDone:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want context.Canceled, got %v", err)
		}
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("caller did not return promptly after cancel")
	}

	// Wait for the background create to actually finish and deposit
	// its result so the cache has the entry inserted.
	var entry *fakeEntry
	select {
	case entry = <-createDone:
	case <-time.After(500 * time.Millisecond):
		t.Fatalf("background ctor did not finish")
	}

	// The new design never pre-bumps refs, so the entry sits in the
	// cache at refs=0 once the inner goroutine finishes — no need to
	// poll for cleanup. Verify directly.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		c.mu.RLock()
		box, ok := c.entries["abandoned"]
		c.mu.RUnlock()
		if ok && box != nil {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	c.mu.RLock()
	box, ok := c.entries["abandoned"]
	c.mu.RUnlock()
	if !ok || box == nil {
		t.Fatalf("entry must still be cached after abandoned create")
	}
	if got := box.refs.Load(); got != 0 {
		t.Fatalf("refs leaked: refs=%d, want 0 (cancelled caller never acquireRef'd)", got)
	}

	// Sanity: Delete now must Close immediately because no leases
	// are outstanding.
	c.Delete("abandoned")
	if !entry.closed.Load() {
		t.Fatalf("Delete must Close the entry now that no lease is held")
	}
}

// TestCache_Delete_AtomicMapDeleteAndDetach is the regression test for
// the post-Do detached-handoff race in GetOrCreate: Delete must mark
// box.detached=true under the SAME write lock that removes the entry
// from c.entries. Without that ordering, a concurrent reader (e.g. a
// GetOrCreate waiter taking the post-singleflight RLock) can observe
// a deleted-but-not-detached box, interpret it as "still alive", and
// hand back a fresh Lease for an entry that should be gone.
//
// The stress loop races a spinning reader against Delete on many
// iterations so the unfixed code is caught with high probability under
// `go test -race`. With the fix, the reader can only ever observe
// (in-map ⇔ not-detached) — never the (gone, not-detached) interleave.
func TestCache_Delete_AtomicMapDeleteAndDetach(t *testing.T) {
	const iterations = 2000
	for i := range iterations {
		c := New[*fakeEntry](16)
		e := newEntry("x")
		addBox(c, "x", e, time.Now().UnixNano())

		c.mu.RLock()
		box := c.entries["x"]
		c.mu.RUnlock()

		readerDone := make(chan struct{})
		go func() {
			defer close(readerDone)
			for {
				c.mu.RLock()
				_, stillMapped := c.entries["x"]
				detached := box.detached.Load()
				c.mu.RUnlock()
				if !stillMapped {
					if !detached {
						t.Errorf("iteration %d: observed entry gone from map with detached=false — Delete violated atomic-delete-and-detach contract", i)
					}
					return
				}
			}
		}()

		c.Delete("x")
		<-readerDone
	}
}

// TestCache_GetOrCreate_ConcurrentWaitersGetIndependentLeases is the
// regression test for the singleflight close-under-use bug: when N
// goroutines miss the cache concurrently for the same session, each
// must receive a DISTINCT *Lease (not a shared pointer), and refs must
// equal N. The prior design returned a shared *Lease via singleflight
// — Lease.Release's lease-local atomic.Bool meant only ONE Release
// actually decremented refs, letting eviction / Delete close the agent
// under callers still using it.
func TestCache_GetOrCreate_ConcurrentWaitersGetIndependentLeases(t *testing.T) {
	c := New[*fakeEntry](8)

	var ctorCalls atomic.Int32
	ctor := func(ctx context.Context) (*fakeEntry, error) {
		ctorCalls.Add(1)
		// Sleep so concurrent callers pile onto the singleflight group
		// before the first one completes — otherwise the test trivially
		// hits the fast path.
		time.Sleep(30 * time.Millisecond)
		return newEntry("singleton"), nil
	}

	const N = 16
	var wg sync.WaitGroup
	leases := make([]*Lease[*fakeEntry], N)
	errs := make([]error, N)
	for i := range N {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			l, err := c.GetOrCreate(context.Background(), "same-id", ctor)
			leases[i] = l
			errs[i] = err
		}(i)
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: GetOrCreate err: %v", i, err)
		}
		if leases[i] == nil {
			t.Fatalf("goroutine %d got nil lease", i)
		}
	}

	if n := ctorCalls.Load(); n != 1 {
		t.Fatalf("ctor called %d times, want 1 (singleflight broken)", n)
	}

	// Every waiter must get a DISTINCT *Lease pointer. Without the fix
	// they'd all share the same pointer and Release would CAS exactly
	// once across all of them.
	for i := 1; i < N; i++ {
		if leases[i] == leases[0] {
			t.Fatalf("goroutine %d shares *Lease with 0 — singleflight returned a shared lease, regression of the close-under-use bug", i)
		}
		if leases[i].Value != leases[0].Value {
			t.Fatalf("goroutine %d got different agent value, want shared underlying", i)
		}
	}

	// refs must equal N — each independent lease contributes exactly
	// one ref. Under the bug, refs was 1 (only the first acquireRef
	// inside createOrReuse ran).
	box := leases[0].box
	if got := box.refs.Load(); got != int64(N) {
		t.Fatalf("refs=%d, want %d (each waiter must contribute its own ref)", got, N)
	}

	// Releasing N-1 leases drops refs to 1; the entry must NOT be
	// closed yet because the last lease still holds it.
	for i := 1; i < N; i++ {
		leases[i].Release()
	}
	if got := box.refs.Load(); got != 1 {
		t.Fatalf("refs=%d after N-1 Releases, want 1 (each Release must decrement)", got)
	}

	// Delete with one outstanding lease must defer Close.
	c.Delete("same-id")
	if leases[0].Value.closed.Load() {
		t.Fatalf("Delete closed the agent while a lease is still outstanding — close-under-use regression")
	}

	// Final Release closes the agent.
	leases[0].Release()
	if !leases[0].Value.closed.Load() {
		t.Fatalf("Delete must Close after the last Release")
	}
}
