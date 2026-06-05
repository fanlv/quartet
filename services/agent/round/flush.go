package round

import (
	"context"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/pkg/logger"
)

// FlushPending drains any in-flight round from the builder and persists the
// collected messages when no onFlush callback is still installed. This is the
// shared cleanup primitive used by every agent implementation.
//
// Ordering is load-bearing: EmitPendingEnds MUST run before CollectMessages.
// CollectMessages clears the tool-call accumulators and synthesises
// [placeholder] role=tool messages for any unresolved ids; if UI end events
// fire after, the Run's deferred cleanup sees no pending ids and never emits
// OnToolCallEnd for them, leaving the UI bubbles stuck in "pending" until
// history reload.
//
// If onFlush is still wired (a Run is active), CollectMessages simply drains
// — the callback has already persisted everything. Otherwise persist is
// invoked inline with the drained buffer.
//
// label identifies the agent path in warning logs (e.g. "eino jobId=…",
// "acp acpSession=…"). FlushPending is typically called from a cleanup path
// that has no live ctx (external Cancel()), so log fallback uses
// context.Background(); callers with a live ctx can log around it.
func FlushPending(b *Builder, persist func([]*schema.Message) error, label string) {
	b.EmitPendingEnds(ReasonCanceled)
	hasOnFlush := b.HasOnFlush()
	msgs := b.CollectMessages(ReasonCanceled)
	if len(msgs) == 0 || hasOnFlush {
		return
	}
	if err := persist(msgs); err != nil {
		logger.Warnf(context.Background(), "[%s] flush pending messages failed: %v", label, err)
	}
}
