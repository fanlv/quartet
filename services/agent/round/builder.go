// Package round implements the protocol-neutral "message round" aggregator
// shared by every agent path. A Builder consumes chunk-level events via the
// agentstream.StreamHandler contract, forwards pre-aggregated UI events via
// agui.EventHandler, and emits complete rounds (assistant message + tool
// results) via a persistence callback.
//
// # Concurrency
//
// Builder methods are internally synchronised with a single mutex; callers
// do not need to add their own locking. Streaming events may arrive on a
// different goroutine from the cancel / finalize path — the builder
// serialises them.
//
// # onFlush context
//
// The onFlush callback installed via Reset is invoked to persist a
// completed round. Implementations MUST use a detached ctx (e.g.
// context.Background()) rather than the run ctx: rounds can be flushed
// after a cancel, and persisting them is precisely the behaviour we want
// to preserve in that case.
//
// # Flush policy
//
// There are two eager-flush triggers:
//
//  1. A new message / thought chunk arrives while tool calls have already
//     accumulated — the previous round is complete and is flushed before
//     the new round begins.
//  2. The tool-result count catches up with the tool-call count — the
//     round is complete and flushed.
//
// Any flush path (eager + explicit CollectMessages) goes through the same
// half-complete-round handling: each tool call that has not received a
// result is filled with a [placeholder] role=tool message, so the on-disk
// history is always tool_use / tool_result paired. Placeholders are
// synthesised before flushing, never after — the invariant "any flushed
// output is paired" must hold at every disk write.
//
// The role=tool messages (real + synthetic) in a flushed round are
// reordered to match the declared order of the assistant's ToolCalls,
// regardless of the order in which results arrived. This keeps summary
// index bookkeeping and LLM context reconstruction deterministic.
package round

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/pkg/logger"
	"github.com/fanlv/quartet/types/agentstream"
	"github.com/fanlv/quartet/types/agui"
	"github.com/fanlv/quartet/types/msgextra"
)

// lateStitchBurstThreshold escalates the per-Run summary line to INFO
// when a single Run accumulates this many stitches. A burst is the only
// signal we trust to flag a real upstream sequencing issue across the
// Run, because supersededAgoMs alone cannot distinguish "tool genuinely
// ran for that long" from "tool finished early but terminal delivery
// stalled" — the discriminator (toolFinishedAt arriving BEFORE the
// terminal) is something we never receive. We previously also escalated
// when the per-Run maxSupersededAgoMs crossed 30 s, but that produced
// false anomalies for normal long-running task tools and was the same
// flawed criterion the per-event path explicitly retired. The summary
// still reports maxSupersededAgoMs as a number for human inspection,
// but it does not drive the log level. Below the burst threshold the
// summary stays at Debug.
const lateStitchBurstThreshold = 5

// supersededInfo captures the per-tool context recorded at eager-flush
// time so a late-arriving terminal can log a self-contained diagnostic
// without re-deriving anything from current builder state (which has
// already been reset for the next round).
type supersededInfo struct {
	// supersededAt is the millis timestamp of the eager flush that
	// placeholdered this tool. The age the late terminal log reports is
	// time.Now() - supersededAt, i.e. the gap between supersede and
	// terminal arrival. NOT the tool's runtime.
	supersededAt int64
	// toolName is the function name from the originating ToolCall, kept
	// here because accToolCalls is cleared on flush.
	toolName string
	// toolStartedAt is the millis timestamp recorded when OnToolCall
	// fired for this id. Combined with the late terminal's arrival
	// instant it gives the tool's true wall-clock runtime — the signal
	// that distinguishes "ACP delivered terminal late" (toolElapsedMs ≈
	// supersededAgoMs because supersede landed right after OnToolCall)
	// from "tool actually ran 130s" (toolElapsedMs much larger than
	// supersededAgoMs because the supersede only triggered well into the
	// run).
	toolStartedAt int64
	// kind names the upstream callback that triggered the supersede,
	// e.g. "messageChunk" or "thoughtChunk". Useful for spotting whether
	// one path dominates the anomalies.
	kind string
}

// PlaceholderReason signals why a tool-call placeholder was synthesised at
// flush time. It affects only the placeholder's content prefix; builder
// behaviour is otherwise identical across reasons.
type PlaceholderReason string

const (
	// ReasonCanceled: run cancelled before the tool produced a result.
	ReasonCanceled PlaceholderReason = msgextra.PlaceholderReasonCanceled
	// ReasonInterrupted: run exited with an error / panic before the
	// tool produced a result.
	ReasonInterrupted PlaceholderReason = msgextra.PlaceholderReasonInterrupted
	// ReasonSuperseded: eager flush triggered by the next round while
	// tool calls were still pending (rare).
	ReasonSuperseded PlaceholderReason = msgextra.PlaceholderReasonSuperseded
)

// Builder aggregates streaming events into complete rounds. One Builder
// per agent Run; call Reset before each Run.
type Builder struct {
	mu sync.Mutex

	handler  agui.EventHandler
	onFlush  func([]*schema.Message)
	onStitch func(toolCallID string, real *schema.Message)

	inMessage bool
	inThought bool

	// currentMsgID / currentThoughtMsgID are captured by the builder
	// immediately after OnMessageStart / OnThoughtStart fires (the UI
	// handler assigns the id at that moment). Capturing at the start
	// event — not at flush — is essential: eager flush finalises the
	// previous round before the new start event is emitted, so reading
	// handler.LastMessageID at flush time would capture the next round's
	// id and attach it to the round being flushed.
	currentMsgID        string
	currentThoughtMsgID string

	accMessageParts []string
	accThoughtParts []string
	accToolCalls    []schema.ToolCall
	accToolResults  []*schema.Message

	// Duration tracking: timestamps (unix millis) for each phase
	msgStartedAt      int64
	thoughtStartedAt  int64
	msgFinishedAt     int64
	thoughtFinishedAt int64
	toolStartedAt     map[string]int64
	toolFinishedAt    map[string]int64

	completedMessages []*schema.Message
	sawTokenUsage     bool

	// logLabel is an opaque identifier the caller can attach via
	// SetLogLabel so the builder's internal warnings (superseded flushes,
	// undeclared / duplicate terminals) can be traced back to a concrete
	// job / session without tying the builder itself to any id schema.
	logLabel string

	// recentlySuperseded records tool-call IDs whose round was eager-flushed
	// with a synthesised placeholder (the LLM started the next assistant
	// chunk before the tool's terminal arrived). A late terminal that
	// references one of these IDs is the expected fallout — it should log
	// as an INFO "late terminal" instead of the scarier WARN
	// "undeclared tool_call". Cleared on Reset; bounded by flushes per Run.
	//
	// The value carries enough context to make the late-terminal log
	// self-contained: tool name, the original toolStartedAt, and which
	// upstream callback (message chunk vs thought chunk) caused the
	// supersede. Without this the log line only carries supersededAgoMs,
	// which is the gap from supersede → terminal — not the tool's true
	// runtime, and not enough to tell apart "ACP delivered terminal late"
	// from "tool actually ran for 130s".
	recentlySuperseded map[string]supersededInfo

	// handlerErrLogged is set the first time this Run surfaces a non-nil
	// error from the agui handler. Handler errors (broken SSE / closed
	// websocket / UI-side write failures) cascade rapidly — a single SSE
	// disconnect turns every following OnMessageDelta / OnToolCallResult
	// into an error — so we emit at most one WARN per Run to keep logs
	// useful while still making the broken state observable. Cleared on
	// Reset.
	handlerErrLogged bool

	// lateStitchCount accumulates late-terminal stitches in the current
	// Run. Per-event detail lines always log at Debug — supersededAgoMs
	// alone cannot tell "tool genuinely ran for that long" apart from
	// "tool finished early but terminal delivery stalled". Reset emits a
	// summary line carrying totalStitched / suppressed / maxSupersededAgoMs
	// / maxToolElapsedMs and clears the counters so the next Run starts
	// fresh. The summary is INFO only when the burst threshold is crossed
	// (the only signal robust to that ambiguity); otherwise Debug,
	// preserving observability without flooding.
	//
	// maxToolElapsedMs is the discriminator. By construction
	// toolElapsedMs >= supersededAgoMs (the tool must start before it can
	// be superseded), so the gap between the two equals
	// supersededAt - toolStartedAt — the time the tool spent running before
	// the eager flush placeholdered it. When the two are similar the tool
	// was superseded almost immediately after OnToolCall and the terminal
	// arrived much later: a delivery stall. When toolElapsedMs is much
	// larger than maxSupersededAgoMs the tool ran for a long time before
	// being superseded and the terminal followed close behind: a normal
	// long-running tool getting eager-flushed. Including it in the INFO
	// summary keeps the line self-contained for triage without grepping
	// the per-event Debug log.
	lateStitchCount            int
	lateStitchSuppressed       int
	lateStitchMaxAgeMs         int64
	lateStitchMaxToolElapsedMs int64
}

// compile-time check: Builder implements the shared StreamHandler contract.
var _ agentstream.StreamHandler = (*Builder)(nil)

// New returns a fresh Builder. Call Reset to wire a handler and onFlush
// callback before feeding events.
func New() *Builder { return &Builder{} }

// Reset clears all accumulators and installs the active handler + onFlush
// for a new Run. onStitch is also cleared — production callers that care
// about late-terminal stitch must call SetStitcher after Reset (the ACP
// and Eino runners do). Safe to call between Runs on the same Builder.
func (b *Builder) Reset(h agui.EventHandler, onFlush func([]*schema.Message)) {
	b.mu.Lock()
	defer b.mu.Unlock()

	// If the previous Run accumulated late-terminal stitches, emit a
	// summary line so the suppressed (Debug-level) ones are not silently
	// lost. The summary is INFO only when the Run crossed the burst
	// threshold — that is the one signal robust to the supersededAgoMs
	// ambiguity (a sustained run of stitches points to a real upstream
	// sequencing issue rather than a single long-running tool). Otherwise
	// Debug — routine race-window recovery is observable at Debug
	// without flooding the operator log. Done before the assignments
	// below so the values reflect the run we're closing out.
	if b.lateStitchCount > 0 {
		// Classify the dominant stitch pattern for this Run based on the
		// gap between maxToolElapsedMs and maxSupersededAgoMs (see field
		// comment for the full rationale). When the two are within 10s
		// the tool was superseded almost immediately — terminal delivery
		// stalled. When toolElapsedMs is much larger the tool genuinely
		// ran a long time before being eager-flushed.
		pattern := "deliveryStall"
		if b.lateStitchMaxToolElapsedMs > 0 && b.lateStitchMaxAgeMs > 0 {
			gap := b.lateStitchMaxToolElapsedMs - b.lateStitchMaxAgeMs
			if gap > 10000 {
				pattern = "longRunningTool"
			}
		}
		summaryNotable := b.lateStitchCount >= lateStitchBurstThreshold
		if summaryNotable {
			logger.Infof(context.Background(),
				"[round] late terminal stitch summary: label=%s totalStitched=%d suppressed=%d maxSupersededAgoMs=%d maxToolElapsedMs=%d pattern=%s burstThreshold=%d",
				b.logLabel, b.lateStitchCount, b.lateStitchSuppressed, b.lateStitchMaxAgeMs, b.lateStitchMaxToolElapsedMs, pattern, lateStitchBurstThreshold)
		} else {
			logger.Debugf(context.Background(),
				"[round] late terminal stitch summary (routine): label=%s totalStitched=%d suppressed=%d maxSupersededAgoMs=%d maxToolElapsedMs=%d pattern=%s",
				b.logLabel, b.lateStitchCount, b.lateStitchSuppressed, b.lateStitchMaxAgeMs, b.lateStitchMaxToolElapsedMs, pattern)
		}
	}

	b.handler = h
	b.onFlush = onFlush
	b.onStitch = nil
	b.inMessage = false
	b.inThought = false
	b.currentMsgID = ""
	b.currentThoughtMsgID = ""
	b.accMessageParts = nil
	b.accThoughtParts = nil
	b.accToolCalls = nil
	b.accToolResults = nil
	b.completedMessages = nil
	b.sawTokenUsage = false
	b.msgStartedAt = 0
	b.thoughtStartedAt = 0
	b.msgFinishedAt = 0
	b.thoughtFinishedAt = 0
	b.toolStartedAt = nil
	b.toolFinishedAt = nil
	b.recentlySuperseded = nil
	b.handlerErrLogged = false
	b.lateStitchCount = 0
	b.lateStitchSuppressed = 0
	b.lateStitchMaxAgeMs = 0
	b.lateStitchMaxToolElapsedMs = 0
}

// SetStitcher installs the callback invoked when a late tool terminal
// arrives AFTER an eager superseded flush has already persisted a
// [placeholder] superseded row. The callback's job is to replace that
// placeholder with the real tool result on disk — the builder also
// fixes up its in-memory completedMessages before invoking this. Call
// AFTER Reset and before feeding any events. nil disables stitching
// (tests that don't care can skip it).
func (b *Builder) SetStitcher(fn func(toolCallID string, real *schema.Message)) {
	b.mu.Lock()
	b.onStitch = fn
	b.mu.Unlock()
}

// ClearOnFlush removes the onFlush and onStitch callbacks so late
// events do not try to persist via handlers that have already
// returned. Kept under the old name to avoid churn at the callsites.
func (b *Builder) ClearOnFlush() {
	b.mu.Lock()
	b.onFlush = nil
	b.onStitch = nil
	b.mu.Unlock()
}

// ClearHandler removes the agui handler reference without touching the
// accumulated message state.
func (b *Builder) ClearHandler() {
	b.mu.Lock()
	b.handler = nil
	b.mu.Unlock()
}

// SawTokenUsage reports whether the stream has surfaced a token-usage
// event at least once. Consumers use this as a fallback toggle: if the
// underlying path never reports usage, they can compute a local estimate.
func (b *Builder) SawTokenUsage() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.sawTokenUsage
}

// HasAccumulatedContent reports whether the builder has received any
// meaningful content (message chunks, thought chunks, or tool calls)
// since the last Reset. Used for diagnostics: if a Run completes
// successfully but the builder is empty, the subprocess likely returned
// a vacuous response.
func (b *Builder) HasAccumulatedContent() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return len(b.accMessageParts) > 0 || len(b.accThoughtParts) > 0 || len(b.accToolCalls) > 0
}

// HasOnFlush reports whether an onFlush callback is currently installed.
func (b *Builder) HasOnFlush() bool {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.onFlush != nil
}

// logHandlerErr records and surfaces a non-nil error returned by the agui
// handler. Handler failures (broken SSE / closed websocket / UI write
// errors) cascade — a single disconnect turns every subsequent handler
// call into an error — so we emit at most one WARN per Run. Subsequent
// errors are silently ignored; Reset re-arms the latch for the next Run.
// The op tag ("OnMessageEnd", "OnToolCallResult", …) names the callback
// that produced the error so log consumers can pinpoint the failure
// shape; logLabel carries the per-Run job / session attribution.
func (b *Builder) logHandlerErr(op string, err error) {
	if err == nil {
		return
	}
	b.mu.Lock()
	if b.handlerErrLogged {
		b.mu.Unlock()
		return
	}
	b.handlerErrLogged = true
	label := b.logLabel
	b.mu.Unlock()
	logger.Warnf(context.Background(), "[round] handler %s failed: label=%s err=%v (further handler errors this Run will be suppressed)", op, label, err)
}

// SetLogLabel attaches a tracing label (typically a session / job id) to
// the builder. It is included in the builder's anomaly warnings so log
// consumers can aggregate "upstream stream ordering" signals per caller.
func (b *Builder) SetLogLabel(label string) {
	b.mu.Lock()
	b.logLabel = label
	b.mu.Unlock()
}

// missingToolEntries pairs each superseded/missing tool-call ID with its
// declared function name and the millis since OnToolCall fired, so
// eager-flush WARN lines point directly at the tool that stalled, e.g.
// "[call_abc=todo_write(age=1420ms) call_xyz=bash(age=350ms)]". The age
// lets the operator tell a just-declared race apart from a genuinely
// stalled terminal. Without the pairing the log has a flat toolNames list
// and the operator has to cross reference ids manually.
// Caller must hold b.mu so b.toolStartedAt stays consistent with the read.
func (b *Builder) missingToolEntriesLocked(tcs []schema.ToolCall, missingIDs []string) []string {
	if len(missingIDs) == 0 {
		return nil
	}
	nameByID := make(map[string]string, len(tcs))
	for _, tc := range tcs {
		nameByID[tc.ID] = tc.Function.Name
	}
	now := time.Now().UnixMilli()
	out := make([]string, 0, len(missingIDs))
	for _, id := range missingIDs {
		name := nameByID[id]
		if name == "" {
			name = "<unknown>"
		}
		entry := id + "=" + name
		if b.toolStartedAt != nil {
			if startedAt, ok := b.toolStartedAt[id]; ok && startedAt > 0 {
				entry += fmt.Sprintf("(age=%dms)", now-startedAt)
			}
		}
		out = append(out, entry)
	}
	return out
}

// rememberSupersededLocked records tool-call IDs whose result was
// replaced with a placeholder by an eager flush, so a late-arriving
// terminal for these IDs can be logged as an expected fallout instead
// of a mysterious "undeclared tool_call". It snapshots the tool's name
// and start timestamp from the live builder state because both are
// cleared by the upcoming flush — the late terminal can arrive after
// many subsequent rounds, by which time accToolCalls / toolStartedAt
// no longer reference this id. kind is the upstream callback that
// caused the supersede (e.g. "messageChunk" / "thoughtChunk"), and ends
// up in the late-terminal log so we can tell whether one path dominates
// the anomalies. Caller must hold b.mu.
func (b *Builder) rememberSupersededLocked(ids []string, supersededAt int64, kind string) {
	if len(ids) == 0 {
		return
	}
	if b.recentlySuperseded == nil {
		b.recentlySuperseded = make(map[string]supersededInfo, len(ids))
	}
	nameByID := make(map[string]string, len(b.accToolCalls))
	for _, tc := range b.accToolCalls {
		nameByID[tc.ID] = tc.Function.Name
	}
	for _, id := range ids {
		info := supersededInfo{
			supersededAt: supersededAt,
			toolName:     nameByID[id],
			kind:         kind,
		}
		if b.toolStartedAt != nil {
			info.toolStartedAt = b.toolStartedAt[id]
		}
		b.recentlySuperseded[id] = info
	}
}

// FinalizeStreaming reports and clears the inMessage / inThought flags so
// the caller can emit matching OnMessageEnd / OnThoughtEnd events.
func (b *Builder) FinalizeStreaming() (inMsg bool, inThought bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	inMsg = b.inMessage
	inThought = b.inThought
	b.inMessage = false
	b.inThought = false
	return
}

// PendingToolCallIDs returns tool-call IDs that were started but never
// received a terminal (completed/failed) status, so the caller can emit
// OnToolCallEnd for each.
func (b *Builder) PendingToolCallIDs() []string {
	b.mu.Lock()
	defer b.mu.Unlock()

	closedSet := make(map[string]bool, len(b.accToolResults))
	for _, r := range b.accToolResults {
		closedSet[r.ToolCallID] = true
	}

	var pending []string
	for _, tc := range b.accToolCalls {
		if !closedSet[tc.ID] {
			pending = append(pending, tc.ID)
		}
	}
	return pending
}

// EmitPendingEnds finalises any in-flight message/thought streaming and
// emits matching OnMessageEnd / OnThoughtEnd / OnToolCallEnd events via
// the stored handler. Invariant: callers MUST invoke this BEFORE
// CollectMessages on any flush-cleanup path, otherwise CollectMessages
// will clear the tool-call accumulators and synthesise [placeholder]
// results on disk without the UI ever seeing OnToolCallEnd — the tool
// bubble would stay "pending" until history reload.
//
// reason is the same PlaceholderReason the caller will pass to the
// immediately-following CollectMessages. It governs the live
// OnToolCallInterrupted payload so the tooltip the user sees during
// stop/cancel matches the reason baked into the on-disk placeholder —
// otherwise the live UI says one thing (e.g. "interrupted") and after
// reload the same bubble says another ("canceled").
//
// Duration bookkeeping: the flip from inMessage/inThought=true to false
// ALSO records the matching *FinishedAt timestamp, and every pending
// tool-call gets a toolFinishedAt entry. Without this, the subsequent
// CollectMessages flush would persist assistant finished_at as the
// moment thinking ended (or 0 → fallback to flush time) for a round
// that actually ended with streaming text, making history reload
// report a near-zero assistant-text duration.
//
// Reading the handler + state under b.mu then releasing the lock before
// invoking handler methods follows the same pattern as OnMessageChunk
// etc., so handler callbacks that re-enter the builder do not deadlock.
func (b *Builder) EmitPendingEnds(reason PlaceholderReason) {
	b.mu.Lock()
	h := b.handler
	now := time.Now().UnixMilli()
	inMsg := b.inMessage
	inThought := b.inThought
	if inMsg {
		b.inMessage = false
		b.msgFinishedAt = now
	}
	if inThought {
		b.inThought = false
		b.thoughtFinishedAt = now
	}

	closedSet := make(map[string]bool, len(b.accToolResults))
	for _, r := range b.accToolResults {
		closedSet[r.ToolCallID] = true
	}
	var pending []string
	for _, tc := range b.accToolCalls {
		if !closedSet[tc.ID] {
			pending = append(pending, tc.ID)
		}
	}
	if len(pending) > 0 {
		if b.toolFinishedAt == nil {
			b.toolFinishedAt = make(map[string]int64)
		}
		for _, id := range pending {
			if _, exists := b.toolFinishedAt[id]; !exists {
				b.toolFinishedAt[id] = now
			}
		}
	}
	b.mu.Unlock()

	if h == nil {
		return
	}
	if inMsg {
		pinBoundaryTimestamp(h, now)
		b.logHandlerErr("OnMessageEnd", h.OnMessageEnd())
	}
	if inThought {
		pinBoundaryTimestamp(h, now)
		b.logHandlerErr("OnThoughtEnd", h.OnThoughtEnd())
	}
	for _, tcID := range pending {
		// Pending tool calls never received a terminal status. On disk
		// they will be synthesised as [placeholder] messages by the
		// next CollectMessages. Signal this to the live UI as a
		// Placeholder (matching reason) so the tooltip the user sees
		// now matches what history reload will render after refresh.
		pinBoundaryTimestamp(h, now)
		b.logHandlerErr("OnToolCallInterrupted", h.OnToolCallInterrupted(tcID, string(reason)))
	}
}

// CollectMessages flushes the current round (synthesising placeholders
// for any missing tool results with the given reason) and returns every
// completed round message accumulated since the last call.
//
// Typical use: on Run end use ReasonCanceled when ctx was cancelled,
// ReasonInterrupted when the Run errored, or pass ReasonInterrupted
// unconditionally — the reason only affects placeholder content.
func (b *Builder) CollectMessages(reason PlaceholderReason) []*schema.Message {
	b.mu.Lock()
	msgs, onFlush := b.flushCurrentRoundDeferredLocked(reason)
	collected := b.completedMessages
	b.completedMessages = nil
	b.mu.Unlock()

	if len(msgs) > 0 && onFlush != nil {
		onFlush(msgs)
	}
	return collected
}

// stampPendingToolsFinishedLocked finds every accumulated tool call
// that has not received a result yet and records a finished_at
// timestamp for it, so the subsequent flush can emit placeholder
// messages with a valid duration window. Returns the pending tool ids
// (for caller-side live TOOL_CALL_END signals) and the shared
// finished_at timestamp so the live event can pin the same instant.
// Caller must hold b.mu.
func (b *Builder) stampPendingToolsFinishedLocked() ([]string, int64) {
	if len(b.accToolCalls) == 0 {
		return nil, 0
	}
	closed := make(map[string]bool, len(b.accToolResults))
	for _, r := range b.accToolResults {
		closed[r.ToolCallID] = true
	}
	var pending []string
	for _, tc := range b.accToolCalls {
		if !closed[tc.ID] {
			pending = append(pending, tc.ID)
		}
	}
	if len(pending) == 0 {
		return nil, 0
	}
	now := time.Now().UnixMilli()
	if b.toolFinishedAt == nil {
		b.toolFinishedAt = make(map[string]int64)
	}
	for _, id := range pending {
		if _, exists := b.toolFinishedAt[id]; !exists {
			b.toolFinishedAt[id] = now
		}
	}
	return pending, now
}

// flushCurrentRoundDeferredLocked builds the current round, synthesises
// placeholder tool results for any missing ids, reorders role=tool
// messages by declaration order, appends to completedMessages, and
// resets the accumulators. Returns the round messages + onFlush callback
// so the caller can invoke onFlush outside the lock. Caller must hold
// b.mu.
func (b *Builder) flushCurrentRoundDeferredLocked(reason PlaceholderReason) ([]*schema.Message, func([]*schema.Message)) {
	content := strings.Join(b.accMessageParts, "")
	reasoning := strings.Join(b.accThoughtParts, "")
	toolCalls := b.accToolCalls
	toolResults := b.accToolResults

	if content == "" && reasoning == "" && len(toolCalls) == 0 {
		return nil, nil
	}

	assistantMsg := &schema.Message{
		Role:             schema.Assistant,
		Content:          content,
		ReasoningContent: reasoning,
		ToolCalls:        toolCalls,
	}
	if b.currentMsgID != "" || b.currentThoughtMsgID != "" {
		if assistantMsg.Extra == nil {
			assistantMsg.Extra = make(map[string]any)
		}
		if b.currentMsgID != "" {
			assistantMsg.Extra[msgextra.KeyMsgID] = b.currentMsgID
			b.currentMsgID = ""
		}
		if b.currentThoughtMsgID != "" {
			assistantMsg.Extra[msgextra.KeyThoughtMsgID] = b.currentThoughtMsgID
			b.currentThoughtMsgID = ""
		}
	}

	// Write duration timestamps into assistant message Extra
	{
		if assistantMsg.Extra == nil {
			assistantMsg.Extra = make(map[string]any)
		}
		// started_at = earliest of thoughtStartedAt / msgStartedAt
		startedAt := b.thoughtStartedAt
		if startedAt == 0 || (b.msgStartedAt > 0 && b.msgStartedAt < startedAt) {
			startedAt = b.msgStartedAt
		}
		if startedAt > 0 {
			assistantMsg.Extra[msgextra.KeyStartedAt] = startedAt
		}
		// finished_at = latest of msgFinishedAt / thoughtFinishedAt; fallback to now
		finishedAt := b.msgFinishedAt
		if b.thoughtFinishedAt > finishedAt {
			finishedAt = b.thoughtFinishedAt
		}
		if finishedAt == 0 {
			finishedAt = time.Now().UnixMilli()
		}
		assistantMsg.Extra[msgextra.KeyFinishedAt] = finishedAt
		// Thought timing
		if b.thoughtStartedAt > 0 {
			assistantMsg.Extra[msgextra.KeyThoughtStartedAt] = b.thoughtStartedAt
		}
		if b.thoughtFinishedAt > 0 {
			assistantMsg.Extra[msgextra.KeyThoughtFinishedAt] = b.thoughtFinishedAt
		}
	}

	// Capture tool-timing maps BEFORE resetting builder state so placeholders
	// can carry started_at/finished_at for stop/cancel/superseded tools. Real
	// results already embed their own timing at OnToolCallUpdate time.
	toolStarted := b.toolStartedAt
	toolFinished := b.toolFinishedAt

	// Reset timing for next round
	b.msgStartedAt = 0
	b.thoughtStartedAt = 0
	b.msgFinishedAt = 0
	b.thoughtFinishedAt = 0
	b.toolStartedAt = nil
	b.toolFinishedAt = nil

	orderedResults := reorderToolResults(toolCalls, toolResults, reason, toolStarted, toolFinished)

	roundMsgs := make([]*schema.Message, 0, 1+len(orderedResults))
	roundMsgs = append(roundMsgs, assistantMsg)
	roundMsgs = append(roundMsgs, orderedResults...)

	b.completedMessages = append(b.completedMessages, roundMsgs...)

	b.accMessageParts = nil
	b.accThoughtParts = nil
	b.accToolCalls = nil
	b.accToolResults = nil

	return roundMsgs, b.onFlush
}

// reorderToolResults returns role=tool messages in the order declared by
// toolCalls, synthesising placeholders for any tool_call_id that did not
// receive a real result. Real results are preserved verbatim; placeholders
// use the msgextra.PlaceholderPrefix + reason content form and carry the
// KeyPlaceholderToolResult flag in Extra. toolStartedAt / toolFinishedAt
// carry the builder's running timing for each declared tool call so
// placeholders can persist started_at / finished_at; real results already
// embedded their own timing at OnToolCallUpdate time.
func reorderToolResults(toolCalls []schema.ToolCall, realResults []*schema.Message, reason PlaceholderReason, toolStartedAt, toolFinishedAt map[string]int64) []*schema.Message {
	if len(toolCalls) == 0 {
		// No declared tool_calls: any stray tool results can only be
		// preserved in arrival order (this path is not exercised by
		// normal flows).
		if len(realResults) == 0 {
			return nil
		}
		out := make([]*schema.Message, len(realResults))
		copy(out, realResults)
		return out
	}

	// Bucket real results by id; a tool call should receive at most one
	// terminal result, but if the upstream misbehaves and sends two,
	// keep the first (the rest are dropped silently — this is the same
	// behaviour all other builders have today).
	realByID := make(map[string]*schema.Message, len(realResults))
	for _, r := range realResults {
		if _, exists := realByID[r.ToolCallID]; exists {
			continue
		}
		realByID[r.ToolCallID] = r
	}

	out := make([]*schema.Message, 0, len(toolCalls))
	for _, tc := range toolCalls {
		if r, ok := realByID[tc.ID]; ok {
			out = append(out, r)
			continue
		}
		out = append(out, newPlaceholder(tc.ID, reason, toolStartedAt[tc.ID], toolFinishedAt[tc.ID]))
	}
	return out
}

func newPlaceholder(toolCallID string, reason PlaceholderReason, startedAt, finishedAt int64) *schema.Message {
	content := msgextra.PlaceholderPrefix + " " + string(reason)
	extra := map[string]any{
		msgextra.KeyPlaceholderToolResult: true,
	}
	if startedAt > 0 {
		extra[msgextra.KeyStartedAt] = startedAt
	}
	if finishedAt > 0 {
		extra[msgextra.KeyFinishedAt] = finishedAt
	}
	return &schema.Message{
		Role:       schema.Tool,
		Content:    content,
		ToolCallID: toolCallID,
		Extra:      extra,
	}
}

func isPlaceholder(m *schema.Message) bool {
	if m == nil || m.Extra == nil {
		return false
	}
	v, ok := m.Extra[msgextra.KeyPlaceholderToolResult]
	if !ok {
		return false
	}
	b, ok := v.(bool)
	return ok && b
}

// captureMsgIDAfterStart reads the SSE message id the agui handler just
// assigned in OnMessageStart / OnThoughtStart and stores it on the
// builder for the upcoming round. Must be invoked with b.mu NOT held —
// the caller is responsible for sequencing.
func (b *Builder) captureMsgIDAfterStart(h agui.EventHandler, thought bool) {
	msgID := h.LastMessageID()
	if msgID == "" {
		return
	}
	b.mu.Lock()
	if thought {
		b.currentThoughtMsgID = msgID
	} else {
		b.currentMsgID = msgID
	}
	b.mu.Unlock()
}

// pinBoundaryTimestamp shares the persist-side boundary timestamp with
// the handler so its next event carries the same instant that was
// written to disk. No-op when the handler does not opt into the
// BoundaryTimestampSetter contract (e.g. test stubs).
func pinBoundaryTimestamp(h agui.EventHandler, ts int64) {
	if ts == 0 || h == nil {
		return
	}
	if setter, ok := h.(agui.BoundaryTimestampSetter); ok {
		setter.SetNextBoundaryTimestamp(ts)
	}
}

// flushSlowThreshold is the wall-clock budget for a single onFlush call. Past
// this we log a warning so slow disk I/O / lock contention shows up in the
// logs as a flush-side problem rather than as undifferentiated "stream is
// slow". onFlush is invoked synchronously from streaming callbacks (Eager
// flush in OnMessageChunk / OnThoughtChunk and terminal flush in
// OnToolCallUpdate) — keeping it synchronous is intentional (we want the
// round on disk before any handler callback can panic), but stream tail
// latency tracks flush latency, so we want it observable.
const flushSlowThreshold = 500 * time.Millisecond

// invokeFlush runs the persistence callback and emits a warning when it takes
// longer than flushSlowThreshold. label identifies the trigger site so a
// single log line is enough to localise the slow path. Caller must already
// have ensured fn != nil and len(msgs) > 0.
func (b *Builder) invokeFlush(label string, fn func([]*schema.Message), msgs []*schema.Message) {
	start := time.Now()
	fn(msgs)
	if cost := time.Since(start); cost >= flushSlowThreshold {
		logger.Warnf(context.Background(),
			"[round] slow flush: label=%s site=%s cost=%s msgs=%d",
			b.logLabel, label, cost.Truncate(time.Millisecond), len(msgs))
	}
}

// -----------------------------------------------------------------------------
// agentstream.StreamHandler implementation
// -----------------------------------------------------------------------------

// OnMessageChunk appends a text fragment to the current assistant message.
// If tool calls have accumulated, this triggers an eager flush of the
// prior round before the new chunk starts (previous tool calls without
// results become [placeholder]superseded).
func (b *Builder) OnMessageChunk(text string) {
	var (
		endedThought      bool
		startedMsg        bool
		thoughtEndedAt    int64
		msgStartedAt      int64
		supersededTs      int64
		flushMsgs         []*schema.Message
		flushFn           func([]*schema.Message)
		supersededToolIDs []string
	)

	b.mu.Lock()
	h := b.handler
	if h == nil {
		b.mu.Unlock()
		return
	}
	if len(b.accToolCalls) > 0 {
		// Upstream (LLM / ACP) produced the next assistant chunk before the
		// prior tool call's terminal event arrived. The builder tolerates
		// this by synthesising [placeholder]superseded on flush and keeping
		// the round self-consistent. In practice this is not rare — some
		// backends emit thought/message chunks opportunistically between
		// tool events — so we log at Debug to avoid flooding the error
		// channel. A late terminal whose id was never superseded is still
		// escalated to Warn in OnToolCallUpdate's undeclared branch.
		supersededToolIDs, supersededTs = b.stampPendingToolsFinishedLocked()
		logger.Debugf(context.Background(), "[round] eager flush superseded on message chunk: label=%s toolCalls=%d results=%d pending=%d missing=%v", b.logLabel, len(b.accToolCalls), len(b.accToolResults), len(supersededToolIDs), b.missingToolEntriesLocked(b.accToolCalls, supersededToolIDs))
		b.rememberSupersededLocked(supersededToolIDs, supersededTs, "messageChunk")
		flushMsgs, flushFn = b.flushCurrentRoundDeferredLocked(ReasonSuperseded)
	}
	if b.inThought {
		b.inThought = false
		endedThought = true
		b.thoughtFinishedAt = time.Now().UnixMilli()
		thoughtEndedAt = b.thoughtFinishedAt
	}
	if !b.inMessage {
		b.inMessage = true
		startedMsg = true
		b.msgStartedAt = time.Now().UnixMilli()
		msgStartedAt = b.msgStartedAt
	}
	b.accMessageParts = append(b.accMessageParts, text)
	b.mu.Unlock()

	// Persist the eager-flushed round BEFORE firing handler callbacks.
	// Deferring until after the callbacks leaves a window where a
	// handler panic would skip flushFn entirely — the round is already
	// in completedMessages but never reaches disk, and Run's deferred
	// CollectMessages would silently discard it.
	if len(flushMsgs) > 0 && flushFn != nil {
		b.invokeFlush("messageChunkEager", flushFn, flushMsgs)
	}

	// Signal superseded tools to the live UI so their bubbles transition
	// from Processing → Placeholder immediately; history reload will see
	// the same Placeholder state from the disk flush above.
	for _, tcID := range supersededToolIDs {
		pinBoundaryTimestamp(h, supersededTs)
		b.logHandlerErr("OnToolCallInterrupted", h.OnToolCallInterrupted(tcID, string(msgextra.PlaceholderReasonSuperseded)))
	}

	if endedThought {
		pinBoundaryTimestamp(h, thoughtEndedAt)
		b.logHandlerErr("OnThoughtEnd", h.OnThoughtEnd())
	}
	if startedMsg {
		pinBoundaryTimestamp(h, msgStartedAt)
		b.logHandlerErr("OnMessageStart", h.OnMessageStart())
		b.captureMsgIDAfterStart(h, false)
	}
	b.logHandlerErr("OnMessageDelta", h.OnMessageDelta(text))
}

// OnThoughtChunk appends a text fragment to the current reasoning segment.
// Eager-flush semantics mirror OnMessageChunk.
func (b *Builder) OnThoughtChunk(text string) {
	var (
		endedMsg          bool
		startedThought    bool
		msgEndedAt        int64
		thoughtStartedAt  int64
		supersededTs      int64
		flushMsgs         []*schema.Message
		flushFn           func([]*schema.Message)
		supersededToolIDs []string
	)

	b.mu.Lock()
	h := b.handler
	if h == nil {
		b.mu.Unlock()
		return
	}
	if len(b.accToolCalls) > 0 {
		// Symmetric to OnMessageChunk — see the note there. Demoted to
		// Debug because upstream interleaving of thought chunks with
		// in-flight tool calls is a tolerated, self-healing pattern.
		supersededToolIDs, supersededTs = b.stampPendingToolsFinishedLocked()
		logger.Debugf(context.Background(), "[round] eager flush superseded on thought chunk: label=%s toolCalls=%d results=%d pending=%d missing=%v", b.logLabel, len(b.accToolCalls), len(b.accToolResults), len(supersededToolIDs), b.missingToolEntriesLocked(b.accToolCalls, supersededToolIDs))
		b.rememberSupersededLocked(supersededToolIDs, supersededTs, "thoughtChunk")
		flushMsgs, flushFn = b.flushCurrentRoundDeferredLocked(ReasonSuperseded)
	}
	if b.inMessage {
		b.inMessage = false
		endedMsg = true
		b.msgFinishedAt = time.Now().UnixMilli()
		msgEndedAt = b.msgFinishedAt
	}
	if !b.inThought {
		b.inThought = true
		startedThought = true
		b.thoughtStartedAt = time.Now().UnixMilli()
		thoughtStartedAt = b.thoughtStartedAt
	}
	b.accThoughtParts = append(b.accThoughtParts, text)
	b.mu.Unlock()

	// Persist the eager-flushed round BEFORE firing handler callbacks —
	// same rationale as in OnMessageChunk.
	if len(flushMsgs) > 0 && flushFn != nil {
		b.invokeFlush("thoughtChunkEager", flushFn, flushMsgs)
	}

	for _, tcID := range supersededToolIDs {
		pinBoundaryTimestamp(h, supersededTs)
		b.logHandlerErr("OnToolCallInterrupted", h.OnToolCallInterrupted(tcID, string(msgextra.PlaceholderReasonSuperseded)))
	}

	if endedMsg {
		pinBoundaryTimestamp(h, msgEndedAt)
		b.logHandlerErr("OnMessageEnd", h.OnMessageEnd())
	}
	if startedThought {
		pinBoundaryTimestamp(h, thoughtStartedAt)
		b.logHandlerErr("OnThoughtStart", h.OnThoughtStart())
		b.captureMsgIDAfterStart(h, true)
	}
	b.logHandlerErr("OnThoughtDelta", h.OnThoughtDelta(text))
}

// OnToolCall declares a new tool call. The assistant message (if any) and
// reasoning segment are closed before the tool-call UI event fires.
func (b *Builder) OnToolCall(id, title string) {
	var endedMsg, endedThought bool

	b.mu.Lock()
	h := b.handler
	if h == nil {
		b.mu.Unlock()
		return
	}
	now := time.Now().UnixMilli()
	if b.inMessage {
		b.inMessage = false
		endedMsg = true
		b.msgFinishedAt = now
	} else if b.inThought {
		b.inThought = false
		endedThought = true
		b.thoughtFinishedAt = now
	}
	b.accToolCalls = append(b.accToolCalls, schema.ToolCall{
		ID:       id,
		Function: schema.FunctionCall{Name: title},
	})
	if b.toolStartedAt == nil {
		b.toolStartedAt = make(map[string]int64)
	}
	b.toolStartedAt[id] = now
	b.mu.Unlock()

	if endedMsg {
		pinBoundaryTimestamp(h, now)
		b.logHandlerErr("OnMessageEnd", h.OnMessageEnd())
	}
	if endedThought {
		pinBoundaryTimestamp(h, now)
		b.logHandlerErr("OnThoughtEnd", h.OnThoughtEnd())
	}
	pinBoundaryTimestamp(h, now)
	b.logHandlerErr("OnToolCallStart", h.OnToolCallStart(id, title))
}

// OnToolCallUpdate appends argument deltas for non-terminal updates and
// records the final tool result for terminal updates. ACP full argument
// snapshots enter through OnToolCallArgsSnapshot instead. When the result
// count catches up with the tool-call count, the round is flushed.
func (b *Builder) OnToolCallUpdate(id, content string, status agentstream.ToolCallStatus) {
	if !status.IsTerminal() {
		b.onToolCallArgs(id, content, false)
		return
	}

	var (
		emitResult       bool
		emitEnd          bool
		resultForHandler string
		success          bool
		toolFinishedAt   int64
		flushMsgs        []*schema.Message
		flushFn          func([]*schema.Message)
	)

	b.mu.Lock()
	h := b.handler
	if h == nil {
		b.mu.Unlock()
		return
	}

	if status.IsTerminal() {
		// Defensive guard: a terminal must reference a tool_call that was
		// declared via OnToolCall in the current round. Without this,
		// upstream misbehaviour (tool result arriving without a matching
		// declaration) would inflate accToolResults past len(accToolCalls),
		// trigger a flush whose reorderToolResults silently drops the
		// real result (unknown id not in toolCalls), and leave phantom
		// entries when no declared tool_calls exist to be cleared at all.
		declared := false
		for _, tc := range b.accToolCalls {
			if tc.ID == id {
				declared = true
				break
			}
		}
		if !declared {
			info, wasSuperseded := b.recentlySuperseded[id]
			if wasSuperseded {
				// The tool call was placeholdered by an eager flush (the
				// next assistant chunk started before this terminal
				// arrived). The round is already on disk with a
				// superseded placeholder; stitch the real result back
				// in so history reload and the next round's LLM context
				// see the true tool output instead of the placeholder.
				// An earlier iteration of the builder silently dropped
				// the late terminal — see git log for context.
				//
				// Helper takes ownership of releasing b.mu after the
				// in-memory stitch + counter updates are recorded; the
				// disk stitch and log emission happen outside the lock.
				b.handleLateTerminalStitchLocked(id, content, status, info)
				return
			}
			label := b.logLabel
			knownIDs := make([]string, 0, len(b.accToolCalls))
			for _, tc := range b.accToolCalls {
				knownIDs = append(knownIDs, tc.ID)
			}
			// Snapshot the supersede map too. The most common path into
			// this branch is a session reload after the ACP idle reaper
			// killed the previous subprocess (see pkg/acp/conn.go's
			// connIdleTimeout): on reconnect the new ACP can re-emit
			// terminals from the previous session whose Builder is long
			// gone, so accToolCalls is empty AND recentlySuperseded is
			// empty. With both included on the WARN line, the operator can
			// tell that case apart from the genuine bug — declared list
			// non-empty but our id missing — without grepping git history.
			// contentLen disambiguates "stale heartbeat-style empty
			// terminal" (contentLen=0, harmless) from "we just dropped
			// real tool output" (contentLen>0, worth investigating).
			supersededIDs := make([]string, 0, len(b.recentlySuperseded))
			for sid := range b.recentlySuperseded {
				supersededIDs = append(supersededIDs, sid)
			}
			contentLen := len(content)
			contentPreview := ""
			if contentLen > 0 {
				preview := content
				if len(preview) > 128 {
					preview = preview[:128] + "..."
				}
				contentPreview = preview
			}
			b.mu.Unlock()
			// Demote to Info when this is the known harmless path: Builder has
			// been Reset (declaredIds=[] AND supersededIds=[]) and the
			// terminal carries no payload (contentLen=0). The dominant
			// trigger is an ACP session reload after the idle reaper killed
			// the previous subprocess — the new ACP re-emits empty terminals
			// for the prior session and there's nothing to stitch anyway. A
			// non-empty Builder, a non-empty supersede map, or a non-zero
			// payload all keep WARN: those are either a genuine "declared
			// but our id missing" bug, or a real tool result being dropped.
			isHarmlessGhost := len(knownIDs) == 0 && len(supersededIDs) == 0 && contentLen == 0
			if isHarmlessGhost {
				logger.Infof(context.Background(), "[round] drop ghost terminal after builder reset: label=%s id=%s status=%v contentLen=%d declaredIds=%v supersededIds=%v", label, id, status, contentLen, knownIDs, supersededIDs)
			} else {
				logger.Warnf(context.Background(), "[round] drop terminal for undeclared tool_call: label=%s id=%s status=%v contentLen=%d declaredIds=%v supersededIds=%v contentPreview=%q", label, id, status, contentLen, knownIDs, supersededIDs, contentPreview)
			}
			return
		}
		// Also guard against a duplicate terminal for the same id. Without
		// this, two terminals for id A (before B's terminal arrives) would
		// satisfy len(accToolResults) >= len(accToolCalls) on the second
		// A and trigger a premature flush, placeholdering B even though
		// its result was still in-flight.
		for _, r := range b.accToolResults {
			if r.ToolCallID == id {
				label := b.logLabel
				b.mu.Unlock()
				logger.Warnf(context.Background(), "[round] drop duplicate terminal for tool_call: label=%s id=%s status=%v", label, id, status)
				return
			}
		}

		success = status != agentstream.ToolCallStatusFailed
		resultContent := content
		if !success {
			resultContent = msgextra.FailedPrefix + resultContent
		}
		// Always emit OnToolCallResult on a terminal status so live UI and
		// history reload stay in sync. We unconditionally append a role=tool
		// message to accToolResults below (even for empty content), so the
		// flushed round will carry a tool result on disk; if the live path
		// skipped OnToolCallResult for empty content, reload would show a
		// tool-result bubble that the user never saw during the run.
		emitResult = true
		emitEnd = true
		resultForHandler = resultContent

		// Record tool finished time
		finishedAt := time.Now().UnixMilli()
		toolFinishedAt = finishedAt
		if b.toolFinishedAt == nil {
			b.toolFinishedAt = make(map[string]int64)
		}
		b.toolFinishedAt[id] = finishedAt

		// Build tool result with timing in Extra
		toolExtra := map[string]any{
			msgextra.KeyFinishedAt: finishedAt,
		}
		if b.toolStartedAt != nil {
			if st, ok := b.toolStartedAt[id]; ok {
				toolExtra[msgextra.KeyStartedAt] = st
			}
		}

		b.accToolResults = append(b.accToolResults, &schema.Message{
			Role:       schema.Tool,
			Content:    resultContent,
			ToolCallID: id,
			Extra:      toolExtra,
		})

		if len(b.accToolResults) >= len(b.accToolCalls) {
			// All declared tool calls now have a result — round is
			// complete and can be flushed. No placeholder is needed.
			flushMsgs, flushFn = b.flushCurrentRoundDeferredLocked(ReasonInterrupted)
		}
	}
	b.mu.Unlock()

	// Persist the completed round BEFORE firing handler callbacks. If a
	// handler callback panics, the eager-flushed round is already on
	// disk; deferring it until after the callbacks would leave the round
	// sitting in completedMessages but unpersisted, and Run's deferred
	// CollectMessages path would then silently discard it.
	if len(flushMsgs) > 0 && flushFn != nil {
		b.invokeFlush("toolCallTerminal", flushFn, flushMsgs)
	}

	if emitResult {
		pinBoundaryTimestamp(h, toolFinishedAt)
		b.logHandlerErr("OnToolCallResult", h.OnToolCallResult(id, resultForHandler, success))
	}
	if emitEnd {
		pinBoundaryTimestamp(h, toolFinishedAt)
		b.logHandlerErr("OnToolCallEnd", h.OnToolCallEnd(id, success))
	}
}

// OnToolCallArgsSnapshot replaces the arguments accumulated for a tool call.
// ACP rawInput and replacement content collections use snapshot semantics;
// they must never be concatenated like model token deltas.
func (b *Builder) OnToolCallArgsSnapshot(id, args string) {
	b.onToolCallArgs(id, args, true)
}

func (b *Builder) onToolCallArgs(id, args string, replace bool) {
	if args == "" && !replace {
		return
	}

	b.mu.Lock()
	h := b.handler
	if h == nil {
		b.mu.Unlock()
		return
	}
	for i := range b.accToolCalls {
		if b.accToolCalls[i].ID != id {
			continue
		}
		if replace {
			b.accToolCalls[i].Function.Arguments = args
		} else {
			b.accToolCalls[i].Function.Arguments += args
		}
		break
	}
	b.mu.Unlock()

	b.logHandlerErr("OnToolCallArgs", h.OnToolCallArgs(id, args, replace))
}

// handleLateTerminalStitchLocked rewrites a placeholdered tool result back
// into completedMessages with the real terminal payload, updates stitch
// counters, then performs the disk stitch + log emission outside the lock.
//
// Caller MUST hold b.mu and MUST NOT touch b.mu after invoking this method;
// the helper releases the lock before performing the disk stitch (which can
// take arbitrary time) and the log emission.
func (b *Builder) handleLateTerminalStitchLocked(id, content string, status agentstream.ToolCallStatus, info supersededInfo) {
	label := b.logLabel
	now := time.Now().UnixMilli()
	ageMs := now - info.supersededAt
	successLocal := status != agentstream.ToolCallStatusFailed
	resultContent := content
	if !successLocal {
		resultContent = msgextra.FailedPrefix + resultContent
	}
	toolExtra := map[string]any{}
	// b.toolStartedAt is per-round and was cleared by the supersede flush,
	// so it almost never contains this id by the time the late terminal
	// arrives. Fall back to the snapshot we took at supersede time so the
	// stitched result still carries a started_at, otherwise history reload
	// would render a 0ms tool runtime.
	startedAt := int64(0)
	if b.toolStartedAt != nil {
		if st, ok := b.toolStartedAt[id]; ok {
			startedAt = st
		}
	}
	if startedAt == 0 {
		startedAt = info.toolStartedAt
	}
	if startedAt > 0 {
		toolExtra[msgextra.KeyStartedAt] = startedAt
	}
	// finished_at: prefer the stamp recorded at eager-flush time so
	// placeholder and stitched result share the same duration window. Fall
	// back to now if missing.
	finishedAt := now
	if b.toolFinishedAt != nil {
		if ft, ok := b.toolFinishedAt[id]; ok && ft > 0 {
			finishedAt = ft
		}
	}
	toolExtra[msgextra.KeyFinishedAt] = finishedAt
	real := &schema.Message{
		Role:       schema.Tool,
		Content:    resultContent,
		ToolCallID: id,
		Extra:      toolExtra,
	}
	// Swap the placeholder in completedMessages so a later CollectMessages
	// returns the real result. Scan from the tail: the most recent
	// placeholder for this id is the one we wrote at the superseded flush.
	memoryStitched := false
	for i := len(b.completedMessages) - 1; i >= 0; i-- {
		m := b.completedMessages[i]
		if m == nil || m.Role != schema.Tool || m.ToolCallID != id {
			continue
		}
		if !isPlaceholder(m) {
			break
		}
		b.completedMessages[i] = real
		memoryStitched = true
		break
	}
	// Drop from recentlySuperseded so a duplicate late terminal (if the
	// upstream ever sends two) takes the normal "duplicate terminal" path
	// instead of re-stitching.
	delete(b.recentlySuperseded, id)
	// Track stitch volume for the per-Run summary emitted at Reset. Every
	// per-event line stays at Debug regardless of supersededAgoMs because
	// the metric cannot distinguish "tool genuinely ran for that long"
	// from "tool finished early, terminal delayed". The previous
	// "ageMs >= 30s ⇒ INFO per event" classifier produced false anomalies
	// every time a long-running task tool was eager-flushed past its
	// supersede point. The per-Run summary line at Reset escalates to
	// INFO only on burst (count >= burstThreshold), since a sustained
	// run of stitches is the only signal robust to that ambiguity. We
	// previously also escalated on large maxSupersededAgoMs, but that
	// reproduced the same false-positive pattern at the aggregate level
	// whenever a single long-running tool dominated the Run.
	b.lateStitchCount++
	if ageMs > b.lateStitchMaxAgeMs {
		b.lateStitchMaxAgeMs = ageMs
	}
	// Also track the worst toolElapsedMs across the Run so the INFO summary
	// line carries the discriminator inline. By construction
	// toolElapsedMs >= supersededAgoMs (the tool must start before it can
	// be superseded), and the gap equals supersededAt - toolStartedAt. If
	// maxToolElapsedMs is similar to maxSupersededAgoMs the tool was
	// superseded almost immediately after OnToolCall and the terminal
	// arrived much later — a delivery stall. If maxToolElapsedMs is much
	// larger than maxSupersededAgoMs the tool ran for a long time before
	// the supersede and its terminal followed close behind — a normal
	// long-running tool getting eager-flushed. Without this the operator
	// has to drop to Debug logs to triage. info.toolStartedAt is 0 when
	// the supersede was synthesised without a matching OnToolCall (rare);
	// skip the update in that case to avoid clamping the max to 0.
	if info.toolStartedAt > 0 {
		toolElapsedForMax := now - info.toolStartedAt
		if toolElapsedForMax > b.lateStitchMaxToolElapsedMs {
			b.lateStitchMaxToolElapsedMs = toolElapsedForMax
		}
	}
	// suppressed == count of per-event lines emitted at Debug instead of
	// INFO. With the per-event INFO path retired, every stitch is
	// suppressed; the field is kept so the summary line still reports
	// "all N stitches were Debug-level" — useful when reading old logs
	// where the field has historical meaning.
	b.lateStitchSuppressed++
	stitchFn := b.onStitch
	// Snapshot the live handler reference while we still hold the lock; we
	// release the lock before notifying so the handler call (which may in
	// turn block on SSE delivery) does not stall b.mu. nil if the handler
	// has been cleared (e.g. Run already returned and ClearHandler ran).
	stitchHandler := b.handler
	// Compute the tool's true wall-clock runtime while we still hold the
	// lock-snapshot values. toolElapsedMs is the discriminator: by
	// construction toolElapsedMs >= supersededAgoMs (the tool must start
	// before it can be superseded), and the gap between them equals
	// supersededAt - toolStartedAt. When the two are similar the tool was
	// superseded almost immediately after OnToolCall and the terminal
	// arrived much later — "ACP delivered terminal late". When elapsed is
	// much larger than supersededAgoMs the tool actually ran a long time
	// before being superseded and the terminal followed close behind.
	toolName := info.toolName
	if toolName == "" {
		toolName = "<unknown>"
	}
	toolElapsedMs := int64(-1)
	if info.toolStartedAt > 0 {
		toolElapsedMs = now - info.toolStartedAt
	}
	supersedeKind := info.kind
	b.mu.Unlock()

	diskStitchInvoked := stitchFn != nil
	if stitchFn != nil {
		// Disk-level outcome (found / not-found / I/O error) is logged by
		// the ChatContextManager wrapper; here we only care about
		// surfacing that the late terminal was handled instead of
		// silently dropped.
		stitchFn(id, real)
	}
	// Notify the live UI so the open page rewrites the placeholder bubble
	// with the real result, matching what history reload will render after
	// refresh. Without this the user sees a Placeholder until they
	// refresh, even though the round is now correct on disk and in memory.
	liveStitchInvoked := false
	if stitchHandler != nil {
		liveStitchInvoked = true
		pinBoundaryTimestamp(stitchHandler, finishedAt)
		b.logHandlerErr("OnToolCallStitched", stitchHandler.OnToolCallStitched(id, resultContent, successLocal, ageMs))
	}
	// Per-event stitch line is always Debug — see comment near
	// lateStitchCount++ for the rationale (supersededAgoMs alone cannot
	// distinguish long tool from delayed terminal). The aggregate burst
	// signal (count >= burstThreshold) surfaces at INFO via the summary
	// line at Reset.
	logger.Debugf(context.Background(),
		"[round] late terminal stitched after superseded flush: label=%s id=%s tool=%s status=%v supersededAgoMs=%d toolElapsedMs=%d supersedeKind=%s memoryStitched=%v diskStitchInvoked=%v liveStitchInvoked=%v",
		label, id, toolName, status, ageMs, toolElapsedMs, supersedeKind, memoryStitched, diskStitchInvoked, liveStitchInvoked)
}

// OnTokenUsage forwards the token count to the UI handler and records
// that the upstream path has surfaced at least one usage event.
func (b *Builder) OnTokenUsage(totalTokens int) {
	b.mu.Lock()
	h := b.handler
	b.sawTokenUsage = true
	b.mu.Unlock()
	if h == nil {
		return
	}
	b.logHandlerErr("OnTokenUsage", h.OnTokenUsage(totalTokens))
}
