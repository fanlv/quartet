// Package sessioncache is the single implementation of the
// "per-session agent cache" pattern used by the eino and acp paths.
//
// Both paths need: a bounded map of sessionID -> agent, singleflight
// deduplication on create, last-access LRU eviction that skips running
// entries, and release-on-delete / release-on-evict semantics. Keeping
// two copies of this logic (services/agent/acp/manager.go and
// services/agent/eino/manager.go before this refactor) made the concurrency-
// sensitive invariants drift between paths — any fix to the create-race
// double-check, the evict-skip rule, or the close-on-race cleanup had
// to be threaded through both files. This package collapses both.
//
// # Leases
//
// Get and GetOrCreate hand back a Lease[T] rather than the bare value.
// While at least one Lease for an entry is alive, eviction and Delete
// will not Close() the underlying value: Delete removes the entry from
// the map immediately but defers the actual Close to the last Release;
// eviction skips entries with outstanding leases altogether. This closes
// the close-under-use window between "caller obtained agent" and
// "caller invoked Run" — without it, a concurrent GetOrCreate that
// triggers eviction could pick the just-checked-out agent (its
// IsRunning() is still false because Run() has not entered its body yet)
// and Close() it before the caller's Run() begins.
//
// Callers MUST treat Release as `defer lease.Release()` immediately
// after acquisition; double Release is safe (idempotent), forgetting
// Release pins the entry forever.
//
// The cache is parameterised by the agent type T, which must satisfy
// Entry (IsRunning + Close). Constructors are injected as closures, so
// each path can pass whatever extra parameters it needs (workdir,
// model config, job id, ...) without widening the cache API.
package sessioncache

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"golang.org/x/sync/singleflight"
)

// ErrCapacityExceeded is returned by GetOrCreate when the cache is at
// capacity and no idle entry (non-running, no outstanding leases) is
// available for eviction. Matches the error surfaced by the old acp/eino
// managers so existing callers (tests, UI error paths) keep working.
var ErrCapacityExceeded = errors.New("session cache capacity exceeded")

// ErrEntryDetached is returned by GetOrCreate when a concurrent Delete
// removed the freshly-created (or reused) entry between insertion and the
// per-waiter ref acquisition. The newly-built agent is still
// constructable, but it has been Closed by the racing Delete; the caller
// should retry or surface the error. This is a rare race because Delete
// for a session typically happens at session shutdown, not concurrently
// with create.
var ErrEntryDetached = errors.New("sessioncache: entry detached during create handoff")

// Entry is the contract each cached value must satisfy. Both *ACPAgent
// and *Quartet already implement these methods.
type Entry interface {
	IsRunning() bool
	Close()
}

// Ctor is invoked under singleflight on cache miss. A non-nil error
// aborts the insert and is returned to the caller unwrapped.
type Ctor[T Entry] func(ctx context.Context) (T, error)

type entryBox[T Entry] struct {
	v          T
	lastAccess atomic.Int64 // UnixNano

	// refs counts outstanding Leases for this entry. Eviction skips
	// entries with refs > 0 so a goroutine that holds a Lease cannot
	// have its agent Closed() under it.
	refs atomic.Int64

	// detached is set true once the entry has been removed from the
	// cache map (via Delete or eviction). Combined with refs hitting
	// zero, it triggers the deferred Close. New leases never form
	// against a detached box because Get / GetOrCreate look up
	// through the map under lock.
	detached atomic.Bool

	// closeOnce guarantees Close is invoked at most once across the
	// (Delete-races-Release) and (evict-races-Release) paths.
	closeOnce sync.Once
}

func (b *entryBox[T]) closeNow() {
	b.closeOnce.Do(func() { b.v.Close() })
}

// releaseOne decrements refs and closes the underlying value when this
// was the last reference and the box has been detached from the cache.
// Safe to call concurrently with Delete / eviction; closeOnce guards
// the actual Close.
func (b *entryBox[T]) releaseOne() {
	n := b.refs.Add(-1)
	if n < 0 {
		// Defensive: should never happen — Lease.Release is idempotent
		// at the lease level, and acquireRef pairs each lease with one
		// release. A negative count means a double-release slipped
		// through; clamping silently here would mask the bug, so panic
		// to surface it in tests.
		panic("sessioncache: refs went negative")
	}
	if n == 0 && b.detached.Load() {
		b.closeNow()
	}
}

// acquireRef is called under the cache lock (or with confidence the
// box is still in the map) to bump refs before handing out a Lease.
// Calling on a detached box is a programming error and the caller is
// responsible for not doing so.
func (b *entryBox[T]) acquireRef() {
	b.refs.Add(1)
}

// Lease is a borrowed reference to a cached value. Hold it for the
// duration of any operation on Value; the entry will not be Closed by
// eviction or Delete while at least one Lease is outstanding. Release
// MUST be called exactly once per Lease (typically via defer); calling
// it more than once is a no-op.
type Lease[T Entry] struct {
	Value    T
	box      *entryBox[T]
	released atomic.Bool
}

// Release returns the lease to the cache. Idempotent — extra Release
// calls are silently ignored, so `defer lease.Release()` next to an
// explicit Release() in an error path is safe.
func (l *Lease[T]) Release() {
	if l == nil || l.box == nil {
		return
	}
	if l.released.CompareAndSwap(false, true) {
		l.box.releaseOne()
	}
}

// Cache is a bounded singleflight-dedup'd LRU keyed by sessionID. The
// zero value is NOT usable — use New.
type Cache[T Entry] struct {
	maxAgents     int
	createTimeout time.Duration

	mu      sync.RWMutex
	entries map[string]*entryBox[T]
	sf      singleflight.Group

	// onReuse is an optional hook fired on any cache-hit path (fast
	// RLock path and double-check slow path). Kept as a hook so acp
	// can log its acpSession field without the cache package knowing
	// about acp types.
	onReuse func(sessionID string, v T)
}

// New returns a fresh cache with capacity cap (0 or negative disables
// the bound entirely) and the standard 60s create timeout.
func New[T Entry](cap int) *Cache[T] {
	return &Cache[T]{
		maxAgents:     cap,
		createTimeout: 60 * time.Second,
		entries:       make(map[string]*entryBox[T]),
	}
}

// WithReuseLog installs a callback invoked whenever GetOrCreate returns
// an existing entry. Returns the receiver for chaining.
func (c *Cache[T]) WithReuseLog(fn func(sessionID string, v T)) *Cache[T] {
	c.onReuse = fn
	return c
}

// GetOrCreate returns a Lease for the cached entry, creating it via
// ctor on miss. Concurrent callers for the same id share a single
// ctor invocation (singleflight). Create runs under a 60s context
// detached from the caller's cancellation, but the WAIT for that
// create respects the caller's ctx, so a Stop while the agent is
// being built returns immediately rather than blocking up to 60s.
//
// The returned Lease MUST be Released — typically via
// `defer lease.Release()` — once the caller is done using Value.
// While at least one Lease is outstanding, the entry will not be
// Closed by eviction or Delete.
//
// # Why singleflight returns *entryBox[T] (not *Lease[T])
//
// singleflight.Do shares ONE return value across all waiters for the
// same key. If the inner function returned a *Lease[T] (with refs
// pre-bumped to 1), every concurrent waiter would receive the SAME
// *Lease pointer. Lease.Release uses a lease-local atomic.Bool to
// guard releaseOne, so only the first caller's Release would
// decrement refs — refs would drop to 0 while N-1 callers still held
// the same pointer and used Value, allowing Delete or eviction to
// Close the underlying agent under them. Returning the box and
// letting each waiter independently acquireRef + construct its own
// Lease in its own goroutine fixes the close-under-use race.
func (c *Cache[T]) GetOrCreate(ctx context.Context, sessionID string, ctor Ctor[T]) (*Lease[T], error) {
	// Hit path: read under RLock + atomic LRU touch + ref bump so
	// concurrent reuses don't serialise behind writers.
	c.mu.RLock()
	if box, ok := c.entries[sessionID]; ok && box != nil {
		box.lastAccess.Store(time.Now().UnixNano())
		box.acquireRef()
		v := box.v
		c.mu.RUnlock()
		if c.onReuse != nil {
			c.onReuse(sessionID, v)
		}
		return &Lease[T]{Value: v, box: box}, nil
	}
	c.mu.RUnlock()

	// done relays the singleflight result back to this caller. Buffered
	// so the singleflight goroutine can deposit the result and exit
	// even if our ctx fired and we already returned — no draining
	// needed, the channel is GC'd once both sides drop.
	type result struct {
		raw any
		err error
	}
	done := make(chan result, 1)

	go func() {
		raw, err, _ := c.sf.Do(sessionID, func() (any, error) {
			// Use a context detached from the caller's cancellation so
			// that one caller's cancel doesn't abort the create for
			// every other singleflight participant. Add a timeout to
			// prevent a stuck creation from blocking the singleflight
			// group forever.
			createCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), c.createTimeout)
			defer cancel()
			return c.createOrReuseBox(createCtx, sessionID, ctor)
		})
		done <- result{raw: raw, err: err}
	}()

	select {
	case <-ctx.Done():
		// The singleflight goroutine keeps running so other waiters
		// still get the result. Unlike the prior design, the inner
		// function does NOT pre-bump refs and does NOT hand back a
		// *Lease — each waiter acquires its own ref post-Do. A caller
		// that abandons the wait therefore never holds a ref to leak;
		// the freshly-created box simply sits in the cache with refs=0,
		// available for the next caller to lease.
		return nil, ctx.Err()
	case r := <-done:
		if r.err != nil {
			return nil, r.err
		}
		box := r.raw.(*entryBox[T])
		// Each concurrent waiter independently acquires its own ref so
		// the returned Lease has independent semantics. The previous
		// design returned a SHARED *Lease via singleflight, which broke
		// refcounting under contention (Lease.Release's lease-local
		// atomic.Bool meant only one Release fired even when N callers
		// held the same pointer).
		c.mu.RLock()
		if box.detached.Load() {
			// Concurrent Delete fired between createOrReuseBox
			// inserting the box and our acquireRef. refs may already
			// be 0 and closeNow may have run; returning a Lease here
			// would hand the caller a Closed agent. Surface as an
			// error so the caller can retry. Rare race; Delete during
			// create is unusual.
			c.mu.RUnlock()
			return nil, ErrEntryDetached
		}
		box.acquireRef()
		v := box.v
		c.mu.RUnlock()
		return &Lease[T]{Value: v, box: box}, nil
	}
}

// createOrReuseBox runs inside the singleflight callback. It re-checks
// the cache under lock (another group may have finished while we were
// waiting for ctor), then either returns that result or builds a fresh
// entry and inserts it.
//
// The returned box has refs == 0; the caller (GetOrCreate, in each
// waiter's own goroutine) is responsible for acquireRef + Lease
// construction post-Do. Pre-bumping here is intentionally avoided so
// that each concurrent singleflight waiter ends up with an independent
// Lease — the prior design returned *Lease[T] which singleflight then
// shared across all waiters, collapsing all their Releases into one.
func (c *Cache[T]) createOrReuseBox(createCtx context.Context, sessionID string, ctor Ctor[T]) (*entryBox[T], error) {
	// Double-check under RLock in case another group already finished.
	c.mu.RLock()
	if box, ok := c.entries[sessionID]; ok && box != nil {
		box.lastAccess.Store(time.Now().UnixNano())
		v := box.v
		c.mu.RUnlock()
		if c.onReuse != nil {
			c.onReuse(sessionID, v)
		}
		return box, nil
	}
	c.mu.RUnlock()

	created, err := ctor(createCtx)
	if err != nil {
		return nil, err
	}

	var (
		evicted     *entryBox[T]
		haveEvicted bool
	)
	c.mu.Lock()
	if box, ok := c.entries[sessionID]; ok && box != nil {
		// Someone won the race while we were creating. Drop our
		// freshly-built entry so it doesn't leak resources (sandbox
		// subprocess, ACP connection, ...), and return theirs. The
		// caller will acquireRef on this box in its own goroutine.
		v := box.v
		c.mu.Unlock()
		created.Close()
		if c.onReuse != nil {
			c.onReuse(sessionID, v)
		}
		return box, nil
	}
	evicted, haveEvicted = c.evictIfFullLocked(sessionID)
	if !haveEvicted && c.maxAgents > 0 && len(c.entries) >= c.maxAgents {
		c.mu.Unlock()
		created.Close()
		return nil, fmt.Errorf("%w (max=%d)", ErrCapacityExceeded, c.maxAgents)
	}
	box := &entryBox[T]{v: created}
	box.lastAccess.Store(time.Now().UnixNano())
	c.entries[sessionID] = box
	c.mu.Unlock()

	if haveEvicted {
		// Evicted box has refs == 0 (eviction skips otherwise) and
		// detached has been set under lock; close immediately.
		evicted.closeNow()
	}
	return box, nil
}

// evictIfFullLocked picks the least-recently-used non-running, lease-
// free entry (excluding keepID), removes it from the map, and marks
// it detached. Returns ok=false when the cache is under capacity or
// every other entry is still running / leased. Caller MUST hold write
// lock and is responsible for calling closeNow on the returned box
// AFTER releasing the lock.
func (c *Cache[T]) evictIfFullLocked(keepID string) (*entryBox[T], bool) {
	if c.maxAgents <= 0 {
		return nil, false
	}
	if len(c.entries) < c.maxAgents {
		return nil, false
	}
	var victimID string
	var victim *entryBox[T]
	for id, box := range c.entries {
		if id == keepID || box == nil {
			continue
		}
		if box.v.IsRunning() {
			continue
		}
		// Skip entries with outstanding leases — closing them under a
		// borrower would surface as an opaque "client closed" error
		// once the borrower finally invokes Run().
		if box.refs.Load() > 0 {
			continue
		}
		if victim == nil || box.lastAccess.Load() < victim.lastAccess.Load() {
			victim = box
			victimID = id
		}
	}
	if victim == nil {
		return nil, false
	}
	delete(c.entries, victimID)
	victim.detached.Store(true)
	return victim, true
}

// Get returns a Lease for the cached entry without creating it.
// Touches lastAccess on hit so a bare Get counts as "recent use" for
// LRU purposes (matches the old managers). Caller MUST Release the
// lease when done.
func (c *Cache[T]) Get(sessionID string) (*Lease[T], bool) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	box, ok := c.entries[sessionID]
	if !ok || box == nil {
		return nil, false
	}
	box.lastAccess.Store(time.Now().UnixNano())
	box.acquireRef()
	return &Lease[T]{Value: box.v, box: box}, true
}

// Delete removes the entry for sessionID. If no leases are
// outstanding, the underlying value is Close()d immediately; otherwise
// the close is deferred to the last Release. No-op when the id is not
// cached.
func (c *Cache[T]) Delete(sessionID string) {
	c.mu.Lock()
	box, ok := c.entries[sessionID]
	if ok {
		delete(c.entries, sessionID)
		// Mark detached under the SAME write lock as the map delete so
		// the two transitions are observed atomically by every RLock
		// holder. GetOrCreate's post-Do path RLocks the cache and then
		// checks box.detached to decide between "issue a fresh Lease"
		// and "return ErrEntryDetached"; if detached were stored after
		// the WUnlock, a waiter that RLocked between WUnlock and the
		// store would read detached==false on an entry that was just
		// removed from the map and acquireRef against a soon-to-be-
		// closed agent — exactly the handoff race the detached flag
		// was designed to prevent.
		if box != nil {
			box.detached.Store(true)
		}
	}
	c.mu.Unlock()
	if !ok || box == nil {
		return
	}
	// closeOnce inside closeNow guarantees Close fires at most once
	// across this branch and any concurrent Release that observes
	// detached==true after its decrement and races us into closeNow.
	if box.refs.Load() == 0 {
		box.closeNow()
	}
}

// List snapshots the current values. Callers get a new slice they can
// iterate without holding the cache lock. List is informational —
// callers MUST NOT retain the returned values past the call, since no
// leases are issued and the entries can be evicted at any time.
func (c *Cache[T]) List() []T {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make([]T, 0, len(c.entries))
	for _, box := range c.entries {
		if box != nil {
			out = append(out, box.v)
		}
	}
	return out
}

// EnvInt reads an integer env var with a default fallback. Exposed so
// the acp / eino managers can share capacity-parsing instead of each
// re-implementing it. Treats empty / non-numeric / non-positive as def.
func EnvInt(key string, def int) int {
	raw := os.Getenv(key)
	if raw == "" {
		return def
	}
	v, err := strconv.Atoi(raw)
	if err != nil || v <= 0 {
		return def
	}
	return v
}
