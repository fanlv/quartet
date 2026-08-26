package runtime

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/adk"
	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/einocli/logger"
	"github.com/fanlv/quartet/einocli/round"
	"github.com/fanlv/quartet/einocli/tokenizer"
	"github.com/fanlv/quartet/einocli/types/agui"
)

// tokenUsageMinInterval throttles per-flush token recomputes. Each
// recompute reloads the entire session history and tokenises it; on long
// sessions with many tool rounds the cost compounds toward
// O(history*flushCount). 1s is short enough that the UI's running usage
// counter still moves visibly during a turn while bursts of small
// flushes (eager superseded, late stitches, rapid tool rounds) coalesce
// into a single recompute. The deferred flush in Run guarantees the
// final, exact post-Run value is always emitted regardless of the
// debounce.
const tokenUsageMinInterval = 1 * time.Second

// Run feeds a prompt through the eino adk runner, aggregates adk events
// into rounds via round.Builder, and persists completed rounds
// incrementally. Messages on disk have identical shape to the acp path:
// one assistant message per round followed by its role=tool results in
// declaration order.
//
// # Concurrency / cancellation
//
// The runner wraps ctx with a cancelable child (runCtx) and stores the
// cancel on Agent so Cancel() can be called from another goroutine.
// runCtx being cancelled simply exits the event loop; a deferred
// cleanup block (FinalizeStreaming + PendingToolCallIDs + flush) ensures
// a clean round close in all paths, including mid-loop panic.
func (d *Agent) Run(ctx context.Context, userMessages []*schema.Message, handler agui.EventHandler) error {
	return d.run(ctx, userMessages, handler, nil)
}

// RunWithUsage is Run plus the provider-reported token usage for this prompt
// turn. The returned usage aggregates every underlying ChatModel invocation
// (including tool-loop follow-ups); nil means none of the model calls reported
// usage. On cancellation or another run error, usage from every observed
// streaming chunk and completed non-streaming call is returned.
func (d *Agent) RunWithUsage(ctx context.Context, userMessages []*schema.Message, handler agui.EventHandler) (*ProviderUsage, error) {
	collector := newProviderUsageCollector()
	err := d.run(ctx, userMessages, handler, collector)
	collector.finish()
	return collector.snapshot(), err
}

func (d *Agent) run(ctx context.Context, userMessages []*schema.Message, handler agui.EventHandler, usageCollector *providerUsageCollector) (retErr error) {
	d.runStart()
	defer d.runEnd()

	// Reject no-op user messages early. Without this guard, a caller that
	// passed len(messages) > 0 but whose only message had empty Content
	// and no usable multimodal parts would still reach BeginRun, persist
	// a phantom user turn, and generate an assistant reply against stale
	// history.
	if !hasEffectiveUserInput(userMessages) {
		return fmt.Errorf("empty prompt")
	}

	// Cancel any in-flight Run on this Agent BEFORE acquiring runSem — the
	// old Run holds the slot, and we need it to drop into its deferred
	// cleanup path so it can release it.
	d.mu.Lock()
	prevCancel := d.cancel
	d.mu.Unlock()
	if prevCancel != nil {
		prevCancel()
	}

	// Serialise Runs on this Agent. The round.Builder is shared across
	// Runs, so a new Run's builder.Reset must not interleave with an old
	// Run's deferred cleanup (EmitPendingEnds / CollectMessages); held
	// until the new Run's own cleanup completes. Acquire is ctx-aware: if
	// the previous Run's detached-context cleanup hangs on slow I/O, this
	// Run unblocks on its own ctx cancellation rather than waiting forever.
	select {
	case d.runSem <- struct{}{}:
	case <-ctx.Done():
		return ctx.Err()
	}
	defer func() { <-d.runSem }()

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel() // ensure runCtx resources are always released

	gen := d.storeCancel(cancel)
	defer d.clearCancel(gen)

	if _, err := d.ctxManager.BeginRun(runCtx, userMessages...); err != nil {
		return fmt.Errorf("persist user messages failed: %w", err)
	}

	chatMessages, err := d.ctxManager.LoadMessagesForLLM(runCtx)
	if err != nil {
		return err
	}

	// persistErr captures the first incremental persistence or
	// placeholder-stitch failure observed during this Run. Streaming keeps
	// going so the UI sees the rest of the response, but the Run returns
	// this error after cleanup so the caller can mark the iteration as
	// failed and surface that to the user — without this, messages.jsonl
	// could silently miss a round (breaking the next round's LLM context
	// and history reload) while the run is reported as completed.
	// Goroutine-safe via round.PersistErr because onFlush / onStitch may
	// fire from the deferred cleanup path after the main loop exits.
	var persistErr round.PersistErr

	// Wire the round builder for this Run. onFlush wraps the caller's
	// ctx in PersistContext: detached from caller cancellation (so a
	// flush firing during deferred cleanup after runCtx is cancelled
	// still lands on disk), and tagged with PersistTimeout so the
	// ctx-aware repo / chatctx layer observes the canonical deadline
	// (lock-wait phase is bounded; underlying file I/O is not — see
	// round.PersistTimeout). onStitch
	// is paired with onFlush so a late tool terminal arriving after an
	// eager superseded flush can replace the [placeholder] superseded
	// row with the real result — without it, history and the next
	// round's LLM context would be pinned to the placeholder.
	ctxMgr := d.ctxManager
	logCtx := context.WithoutCancel(ctx)
	d.builder.SetLogLabel(fmt.Sprintf("eino sessionId=%s", d.sessionID))

	// Token usage is recomputed by reloading the full history and
	// tokenising it. On long sessions doing this per flush approaches
	// O(history * flushCount) per Run. Debounce so the UI still gets
	// running usage but the work amortises across bursts: at most one
	// recompute per tokenUsageMinInterval, plus a final guaranteed
	// recompute at Run end (deferred below).
	var (
		lastTokenUsageAt time.Time
		lastTokenUsageMu sync.Mutex
		tokenUsageDirty  bool
	)
	recomputeTokenUsage := func(force bool) {
		lastTokenUsageMu.Lock()
		if !force && time.Since(lastTokenUsageAt) < tokenUsageMinInterval {
			tokenUsageDirty = true
			lastTokenUsageMu.Unlock()
			return
		}
		lastTokenUsageAt = time.Now()
		tokenUsageDirty = false
		lastTokenUsageMu.Unlock()

		reloadCtx, reloadCancel := round.PersistContext(ctx)
		defer reloadCancel()
		reloaded, err := ctxMgr.LoadMessagesForLLM(reloadCtx)
		if err != nil {
			// Reload may legitimately race a concurrent summarisation /
			// truncation, so this is best-effort: skip the recompute and
			// rely on the deferred final pass at Run end. Logged at Debug
			// so persistent failures (e.g. corrupted messages.jsonl) leave
			// a breadcrumb instead of vanishing silently.
			logger.Debugf(logCtx, "[eino] recompute token usage skipped: sessionId=%s err=%v", d.sessionID, err)
			return
		}
		if err := handler.OnTokenUsage(tokenizer.MessagesTokenCounter(logCtx, reloaded)); err != nil {
			// Recompute path bypasses the round.Builder's logHandlerErr
			// funnel because the Builder never saw a usage_update event
			// here — we recomputed locally on top of disk state. Log at
			// Debug so a disconnected SSE handler leaves the same
			// breadcrumb the Builder emits on its own usage callbacks.
			logger.Debugf(logCtx, "[eino] handler OnTokenUsage failed: sessionId=%s err=%v", d.sessionID, err)
		}
	}

	d.builder.Reset(handler, func(msgs []*schema.Message) {
		persistCtx, cancel := round.PersistContext(ctx)
		defer cancel()
		if err := ctxMgr.AppendMessages(persistCtx, msgs...); err != nil {
			logger.Warnf(logCtx, "[eino] incremental persist failed: sessionId=%s err=%v", d.sessionID, err)
			persistErr.Record(fmt.Errorf("eino incremental persist: %w", err))
			// Gate token usage on persistence success: emitting usage
			// after a failed append decouples the UI update from the
			// on-disk state.
			return
		}
		recomputeTokenUsage(false)
	})
	d.builder.SetStitcher(func(toolCallID string, real *schema.Message) {
		persistCtx, cancel := round.PersistContext(ctx)
		defer cancel()
		if _, err := ctxMgr.ReplacePlaceholderToolResult(persistCtx, toolCallID, real); err != nil {
			logger.Warnf(logCtx, "[eino] placeholder stitch failed: sessionId=%s toolCallId=%s err=%v", d.sessionID, toolCallID, err)
			persistErr.Record(fmt.Errorf("eino placeholder stitch: %w", err))
		}
	})
	defer d.builder.ClearOnFlush()

	// Final token usage flush: ensure the UI always sees the exact
	// post-Run value even if the most recent flush hit the debounce
	// window. Defers are LIFO, so this defer runs AFTER both the
	// CollectMessages defer and the persist-error defer below: by the
	// time we read tokenUsageDirty, the final round has already been
	// drained through onFlush and recomputeTokenUsage(false) has had a
	// chance to set the dirty flag. The recompute is read-only against
	// disk so its position relative to the persist-error defer does not
	// matter — both can observe the post-flush state independently.
	defer func() {
		lastTokenUsageMu.Lock()
		dirty := tokenUsageDirty
		lastTokenUsageMu.Unlock()
		if dirty {
			recomputeTokenUsage(true)
		}
	}()

	// Surface the first persistence failure (incremental flush or late
	// stitch) into retErr AFTER the final CollectMessages flush has run.
	// Defers are LIFO, so this defer (declared BEFORE the CollectMessages
	// defer below) runs AFTER CollectMessages has drained the final round
	// through onFlush, ensuring we observe any failure from that final
	// write. PersistErr.CapturePersistErrTo never overwrites an existing
	// retErr (stream/cancel error takes precedence) — a persist failure on
	// top of a stream error is noise; the stream error is the more
	// actionable signal.
	defer persistErr.CapturePersistErrTo(&retErr, "eino stream completed but persist failed")

	// Round close-out runs as a deferred closure so it fires on every
	// exit path — normal return, early error, or mid-loop panic.
	// Declared AFTER ClearOnFlush so LIFO order executes this first,
	// while onFlush is still installed: the final half-complete round
	// reaches disk via the callback rather than being dropped.
	// FinalizeRound picks reason=Canceled when runCtx was cancelled and
	// reason=Interrupted otherwise, then drains the round and clears the
	// agui handler so any late callback after Run returns sees nil.
	defer round.FinalizeRound(d.builder, runCtx)

	adapter := newRoundAdapter(d.toolFailures)

	runOptions := make([]adk.AgentRunOption, 0, 1)
	if usageCollector != nil {
		runOptions = append(runOptions, adk.WithCallbacks(usageCollector.handler()))
	}
	iter := d.runner.Run(runCtx, chatMessages, runOptions...)

	// streamErr captures the first fatal stream / event error so it can be
	// returned to the caller as a Run failure. Without this, an event.Err
	// or stream Recv error would only reach handler.OnError (UI surface) and
	// the Run would return nil — making the caller believe the iteration
	// succeeded while the user just saw an error event.
	var streamErr error
	for {
		event, ok := iter.Next()
		if !ok {
			break
		}
		if err := d.handleEvent(runCtx, handler, adapter, event, usageCollector); err != nil {
			streamErr = err
			break
		}
	}

	if runCtx.Err() != nil {
		return runCtx.Err()
	}

	if streamErr != nil {
		return streamErr
	}

	// Persist failures are surfaced by the deferred persistErr-watcher
	// declared above so they include the final CollectMessages flush.
	return nil
}

// hasEffectiveUserInput reports whether the supplied user messages carry
// any usable input — non-empty text content, a text part in
// UserInputMultiContent, or an attached image / audio / video / file
// reference. Without this check an empty Content + empty
// UserInputMultiContent slice would pass through and produce a no-op
// assistant turn against stale history, while still admitting valid
// image-only multimodal turns whose text Content is understandably empty.
func hasEffectiveUserInput(messages []*schema.Message) bool {
	for _, m := range messages {
		if m == nil || m.Role != schema.User {
			continue
		}
		if strings.TrimSpace(m.Content) != "" {
			return true
		}
		for _, part := range m.UserInputMultiContent {
			if strings.TrimSpace(part.Text) != "" {
				return true
			}
			if part.Image != nil || part.Audio != nil || part.Video != nil || part.File != nil {
				return true
			}
		}
	}
	return false
}

// handleEvent routes an adk event into the round builder via the
// adapter. A non-nil return aborts the Run loop and is propagated as the
// Run's error so callers can mark the iteration as failed; the live
// UI handler still sees OnError before the abort so the bubble closes.
func (d *Agent) handleEvent(ctx context.Context, handler agui.EventHandler, adapter *roundAdapter, event *AgentEvent, usageCollector *providerUsageCollector) error {
	if event.Err != nil {
		handler.OnError(event.Err)
		return fmt.Errorf("eino agent event error: %w", event.Err)
	}

	if event.Output == nil || event.Output.MessageOutput == nil {
		return nil
	}

	output := event.Output.MessageOutput
	if output.Message != nil {
		adapter.forwardDirectMessage(ctx, d.builder, output.Message)
		return nil
	}

	if output.MessageStream != nil {
		return adapter.forwardStream(d.builder, handler.OnError, output.MessageStream, usageCollector)
	}
	return nil
}
