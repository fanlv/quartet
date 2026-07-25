package store

import (
	"context"
	"sync"
)

// sessionFileLocks is a process-wide registry of per-session ctxRWMutexes
// guarding reads and writes against the on-disk chat-context files
// (messages.jsonl, summary.json) for each session directory.
//
// Why this lives at the repository layer (not on the chatContextRepo
// instance):
//
// Multiple ChatContextRepo instances are routinely created for the same
// session — eino, acp, shell-persist, web reload handlers each call
// NewChatContextRepo independently. Per-instance mutexes can't serialise
// writes across those instances, so a read-modify-write rewrite (e.g.
// ReplacePlaceholderToolResult, BeginRun's truncate) on one instance
// could be interleaved with an AppendMessages on another instance and
// silently drop a message.
//
// The lock is keyed by sessionDir because that's the file-system root
// every chatContextRepo is bound to (LocalSessionDirInWorkspaceJob).
// Two instances pointing at the same directory share the same mutex.
//
// Lock entries are never evicted. Session IDs are bounded at human
// scale and quartet is single-user local, so the retained mutexes
// are a negligible leak; eviction would add complexity (refcounting,
// cross-path handoff) for no measurable win.
var (
	sessionFileLocksMu sync.Mutex
	sessionFileLocks   = make(map[string]*ctxRWMutex)
)

// sessionFileLock returns the process-wide ctxRWMutex for the given
// session directory, creating it on first use. An empty key routes to
// a shared fallback so unit tests / callers without a real sessionDir
// still observe sane semantics under parallel execution.
func sessionFileLock(sessionDir string) *ctxRWMutex {
	if sessionDir == "" {
		sessionDir = "__no_session_dir__"
	}
	sessionFileLocksMu.Lock()
	defer sessionFileLocksMu.Unlock()
	mu, ok := sessionFileLocks[sessionDir]
	if !ok {
		mu = &ctxRWMutex{}
		sessionFileLocks[sessionDir] = mu
	}
	return mu
}

// ctxRWMutex wraps sync.RWMutex with ctx-aware acquisition. Lock /
// RLock return ctx.Err() when the caller's deadline expires before
// the lock becomes available, so a Run whose persist budget
// (round.PersistTimeout) elapses can unwind instead of pinning its
// deferred cleanup goroutine on a slow disk.
//
// Acquisition is implemented by handing the actual Lock / RLock call
// off to a goroutine and racing it against ctx.Done(). When ctx
// cancels first, the goroutine still completes (the underlying
// sync.RWMutex isn't cancellable) and a follow-up goroutine releases
// the lock as soon as it lands. The cost is two short-lived
// goroutines per cancelled acquire; the benefit is that the caller
// sees ctx.Err() within bounded latency rather than blocking on the
// lock indefinitely.
//
// The Unlock / RUnlock methods are direct passthroughs because there
// is no benefit to making lock release ctx-aware (the caller already
// holds the lock and is just relinquishing it).
type ctxRWMutex struct {
	rw sync.RWMutex
}

// Lock acquires the write lock or returns ctx.Err() if ctx fires
// first. On success, the caller must defer mu.Unlock() exactly once.
func (m *ctxRWMutex) Lock(ctx context.Context) error {
	return acquireLock(ctx, m.rw.Lock, m.rw.Unlock)
}

// RLock acquires the read lock or returns ctx.Err() if ctx fires
// first. On success, the caller must defer mu.RUnlock() exactly once.
func (m *ctxRWMutex) RLock(ctx context.Context) error {
	return acquireLock(ctx, m.rw.RLock, m.rw.RUnlock)
}

// Unlock releases the write lock. Mirrors sync.RWMutex.Unlock.
func (m *ctxRWMutex) Unlock() { m.rw.Unlock() }

// RUnlock releases the read lock. Mirrors sync.RWMutex.RUnlock.
func (m *ctxRWMutex) RUnlock() { m.rw.RUnlock() }

// acquireLock blocks until lock() succeeds or ctx fires, whichever
// comes first. When ctx wins the race, lock() will still complete on
// its own goroutine; a chaser releases it as soon as it lands so the
// lock doesn't stay pinned by the abandoned acquire.
func acquireLock(ctx context.Context, lock, unlock func()) error {
	// Fast path: ctx already cancelled — don't spawn a goroutine that
	// will just turn around and unlock.
	if err := ctx.Err(); err != nil {
		return err
	}
	done := make(chan struct{})
	go func() {
		lock()
		close(done)
	}()
	select {
	case <-done:
		return nil
	case <-ctx.Done():
		// The Lock goroutine is still in flight. Hand off the
		// release so the lock doesn't stay pinned forever once it
		// finally lands.
		go func() {
			<-done
			unlock()
		}()
		return ctx.Err()
	}
}
