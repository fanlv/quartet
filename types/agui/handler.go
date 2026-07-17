package agui

type TokenUsageHandler interface {
	OnTokenUsage(totalTokens int) error
}

type MessageHandler interface {
	OnMessageStart() error
	OnMessageDelta(content string) error
	OnMessageEnd() error
	// LastMessageID returns the ID assigned to the current message by the
	// most recent OnMessageStart or OnThoughtStart call. Implementations
	// that do not generate IDs (e.g. CLI) may return "".
	LastMessageID() string
}

type ThoughtHandler interface {
	OnThoughtStart() error
	OnThoughtDelta(content string) error
	OnThoughtEnd() error
}

type ToolCallHandler interface {
	OnToolCallStart(id, name string) error
	// OnToolCallArgs delivers either an incremental argument fragment or a
	// complete argument snapshot. replace=true means consumers must replace
	// the arguments collected for this tool call instead of appending args.
	OnToolCallArgs(id, args string, replace bool) error
	// OnToolCallResult delivers the tool call's terminal content. success
	// is false when the tool reported a failure status; the content passed
	// here matches what is written to disk (failed results carry the
	// "[failed] " prefix), so live-stream and history-reload renderings
	// stay in sync.
	OnToolCallResult(id, content string, success bool) error
	// OnToolCallEnd closes the tool call bubble. success mirrors the flag
	// that was passed to OnToolCallResult for the same id.
	OnToolCallEnd(id string, success bool) error
	// OnToolCallInterrupted closes the tool call bubble when the
	// surrounding run was cancelled / interrupted / superseded and the
	// tool never received a real terminal status. The UI renders this
	// as a "placeholder" state distinct from Success / Error so it
	// matches what history reload shows. reason is a short tag (e.g.
	// "interrupted" / "superseded") surfaced to the user.
	OnToolCallInterrupted(id string, reason string) error
	// OnToolCallStitched rewrites a previously-placeholdered tool bubble
	// with its real terminal payload after a late terminal arrived for
	// an eagerly-flushed (superseded) tool call. content matches what is
	// written to disk (failed results carry the "[failed] " prefix);
	// success is the real terminal status. supersededAgoMs is the gap
	// between the eager flush and the late terminal — passed through to
	// observability so operators can spot upstream stalls vs short race
	// windows. The handler is expected to override the live UI's
	// "Placeholder / Finished" state, unlike OnToolCallResult /
	// OnToolCallEnd which are not allowed to update finished bubbles.
	OnToolCallStitched(id string, content string, success bool, supersededAgoMs int64) error
}

// EventHandler receives a single agent run's UI callbacks. Implementations are
// not required to be goroutine-safe; producers must invoke callbacks
// sequentially for a run unless a concrete handler explicitly documents
// stronger concurrency guarantees.
type EventHandler interface {
	MessageHandler
	ThoughtHandler
	ToolCallHandler
	TokenUsageHandler
	OnError(err error)
}

// BoundaryTimestampSetter is an optional capability a handler can expose
// to let its producer (e.g. the round Builder) pin the exact timestamp
// that will be embedded into the next "boundary" event it publishes.
//
// Rationale: persisted history and live SSE must agree on when a
// semantic boundary (message start/end, thought start/end, tool
// finished) happened. The Builder records persist-side timestamps with
// its own `time.Now()` and then later invokes handler callbacks, which
// historically called `time.Now()` a second time for the SSE event —
// the gap (persistence flushes, lock churn, callback dispatch) makes
// live and reloaded duration badges disagree. By letting the Builder
// push its authoritative boundary timestamp into the handler just
// before each boundary callback, both paths share a single clock read.
//
// The setter semantics are "consume once": the handler reads and
// clears the pending timestamp when it emits the next event, so a
// missing set for a non-boundary event (deltas, token usage, errors)
// still falls back to `time.Now()`.
type BoundaryTimestampSetter interface {
	SetNextBoundaryTimestamp(ts int64)
}
