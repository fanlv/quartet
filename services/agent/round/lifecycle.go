package round

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// PersistTimeout is the deadline attached to onFlush / onStitch / final
// post-Run sync persist contexts. It bounds how long any one persist
// step can wait on the per-session lock or on a ctx-aware repo
// operation before returning ctx.DeadlineExceeded, so callers can
// reason about a "this should not have blocked beyond N seconds" budget
// when surfacing warnings.
//
// Coverage today: the per-session lock in repository/session_locks.go
// honours ctx via ctxRWMutex, and every ChatContextRepo /
// ChatContextManager method threads ctx through to the lock acquire,
// so a contended lock or hung competing writer surfaces as ctx.Err()
// within this budget. Underlying file I/O (sandbox FileRead /
// JSONLAppendLine / AtomicWriteFile) is still blocking-only — once the
// goroutine has the lock it can stall arbitrarily on a sick disk; only
// the lock-wait phase is bounded today. 10s sized to comfortably cover
// normal write latency while keeping a stuck disk from pinning the
// deferred chain (and through it runSem) indefinitely.
const PersistTimeout = 10 * time.Second

// PersistContext returns a detached context with the canonical persist
// timeout applied. Detached so caller-side Stop / DeadlineExceeded do
// NOT cancel the persist write (we want flush after Stop to land on
// disk), and bounded by PersistTimeout so the ctx-aware repo /
// chatctx layer observes a single canonical deadline. See
// PersistTimeout for the current scope of what the deadline covers.
//
// Use as:
//
//	persistCtx, cancel := round.PersistContext(ctx)
//	defer cancel()
//	if err := ctxMgr.AppendMessages(persistCtx, msgs...); err != nil { ... }
//
// Retains the caller's ctx values (logger / trace attrs) via
// context.WithoutCancel so post-cancel writes are still attributable in
// logs.
func PersistContext(parent context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(parent), PersistTimeout)
}

// PersistErr accumulates the first persistence error observed across a
// Run's onFlush / onStitch callbacks. Multiple agent backends used to
// hand-roll this exact pattern (mutex-guarded "first error wins",
// surfaced to retErr only AFTER the final flush has had a chance to
// fail too); centralising it stops the "is the CapturePersistErrTo
// defer placed correctly?" question from being a footgun that has to
// be re-answered per backend on every change.
//
// The zero value is ready to use. Methods are goroutine-safe — onFlush
// and onStitch can fire from a detached cleanup goroutine concurrently
// with the main Run loop.
type PersistErr struct {
	mu  sync.Mutex
	err error
}

// Record stores err iff no earlier error was already captured. nil is a
// no-op so callers can pipe ordinary err returns straight in without a
// defensive `if err != nil` at every site.
func (p *PersistErr) Record(err error) {
	if err == nil {
		return
	}
	p.mu.Lock()
	if p.err == nil {
		p.err = err
	}
	p.mu.Unlock()
}

// Err returns the first recorded error, or nil if none.
func (p *PersistErr) Err() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.err
}

// CapturePersistErrTo writes the captured persist error into *retErr,
// wrapping it with prefix. No-op if *retErr already holds an error
// (stream / cancel error takes precedence — a persist failure on top
// of a stream error is noise; the stream error is the more actionable
// signal).
//
// Use as `defer p.CapturePersistErrTo(&retErr, "<backend> stream completed but persist failed")`.
// Defers are LIFO, so this MUST be declared BEFORE the final-flush defer
// so it fires LAST and observes the post-flush state.
func (p *PersistErr) CapturePersistErrTo(retErr *error, prefix string) {
	if retErr == nil || *retErr != nil {
		return
	}
	if e := p.Err(); e != nil {
		*retErr = fmt.Errorf("%s: %w", prefix, e)
	}
}

// FinalizeRound drains the round through onFlush with the right reason
// (Canceled when runCtx is cancelled, Interrupted otherwise), then
// clears the handler so any late callback into the builder between Runs
// dispatches to nil rather than the previous Run's stale agui handler.
//
// Use as `defer round.FinalizeRound(builder, runCtx)`. MUST be declared
// AFTER `defer builder.ClearOnFlush()` so the LIFO order keeps onFlush
// installed at the moment this defer's CollectMessages drains the final
// round — without that ordering, the last half-complete round would be
// dropped instead of persisted.
//
// EmitPendingEnds runs before CollectMessages so UI end events reach the
// handler before the tool-call accumulators are cleared. Both calls
// share the same reason so the live placeholder tooltip matches the
// on-disk placeholder a subsequent reload renders.
func FinalizeRound(b *Builder, runCtx context.Context) {
	reason := ReasonInterrupted
	if runCtx != nil && runCtx.Err() != nil {
		reason = ReasonCanceled
	}
	b.EmitPendingEnds(reason)
	b.CollectMessages(reason)
	b.ClearHandler()
}
