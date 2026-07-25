package runtime

import (
	"context"
	"fmt"
	"io"
	"strings"
	"sync"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/einocli/logger"
	"github.com/fanlv/quartet/einocli/types/agentstream"
)

// roundAdapter translates eino adk message events into
// agentstream.StreamHandler method calls. It bridges two impedance
// mismatches:
//
//  1. A single eino adk stream chunk can carry both Content and
//     ReasoningContent; agentstream expects them on separate methods.
//     The adapter always emits OnThoughtChunk before OnMessageChunk so the
//     builder's state machine sees "reasoning first" consistently across
//     chunks, matching ACP's ordering.
//
//  2. eino adk tool-call chunks identify fragments by position/Index; id
//     and Function.Name may arrive in later chunks. The adapter buffers
//     argument deltas per-position until (id, name) have both been
//     observed, then emits OnToolCall(id, title) followed by
//     OnToolCallUpdate(id, bufferedArgs, non-terminal). Subsequent args
//     flow through as OnToolCallUpdate(id, chunkArgs, non-terminal)
//     without re-buffering. Tests must cover "args arrive before id/name"
//     and "multiple tool_calls interleaved across chunks".
//
//  3. eino adk does not carry an explicit terminal status on tool
//     results. A role=Tool directMessage (and the synthesised EOF
//     terminal in forwardStream) would otherwise always be Completed.
//     The toolWrap middleware, which swallows tool-endpoint errors by
//     returning the error text as a success-looking result, records
//     the failing tool_call id in a shared *sync.Map the adapter holds
//     (toolFailures). emitToolTerminal consumes that entry via
//     LoadAndDelete at terminal time to downgrade Completed to Failed
//     — the single point where a Failed status is injected
//     into round.Builder (and thereby into live success=false events
//     and the on-disk FailedPrefix).
//
// # State scope
//
// The per-position buffers (toolCall*) are scoped to a single eino adk
// stream — one LLM call / one assistant message. Tool-call Index values
// restart from 0 on each new assistant message, so reusing state across
// streams would collide: a new round's Index=0 would hit stale
// toolCallStarted=true from the previous round, skip OnToolCall(newID),
// and drop the new call's args into nowhere. forwardStream therefore
// calls resetStreamState on entry; forwardDirectMessage does not touch
// the maps (direct messages carry id+name eagerly and route through
// emitToolCall).
type roundAdapter struct {
	mu sync.Mutex
	// Per-position buffers for tool_call fragments seen before both id
	// and Function.Name have arrived. Indexed by toolCallKey(tc, pos).
	// Reset at the start of each forwardStream (see State scope above).
	toolCallIDs     map[int]string
	toolCallNames   map[int]string
	toolCallArgsBuf map[int][]string
	toolCallStarted map[int]bool

	// declaredIDs tracks every tool_call id for which this adapter has
	// already emitted OnToolCall to the StreamHandler, scoped to the
	// adapter's lifetime (one Run). It deliberately is NOT reset by
	// resetStreamState: tool results may arrive in a later stream than
	// the stream that declared the tool_call, so per-stream scoping would
	// wrongly drop legitimate cross-stream terminals. We use it to filter
	// EOF / direct-message terminals so the builder never receives a
	// terminal for an id it has not seen declared — matching the builder's
	// own invariant.
	declaredIDs map[string]bool

	// toolFailures, when non-nil, is the shared registry written by the
	// toolWrap middleware whenever a tool endpoint returns an error. At
	// tool-terminal emission time (direct-message role=Tool and stream
	// EOF paths) the adapter consumes entries via LoadAndDelete to
	// downgrade the terminal from Completed to Failed, which in turn
	// drives round.Builder's FailedPrefix on disk and the live success=
	// false event to the UI. Consume-and-delete avoids id collisions
	// across rounds (tool_call ids are LLM-generated and unique, but
	// consuming keeps memory bounded on long-lived agents anyway).
	toolFailures *sync.Map
}

func newRoundAdapter(failures *sync.Map) *roundAdapter {
	return &roundAdapter{
		toolCallIDs:     make(map[int]string),
		toolCallNames:   make(map[int]string),
		toolCallArgsBuf: make(map[int][]string),
		toolCallStarted: make(map[int]bool),
		declaredIDs:     make(map[string]bool),
		toolFailures:    failures,
	}
}

// emitToolTerminal issues the Completed or Failed terminal for a tool
// result. Failure status is consumed from toolFailures (written by the
// toolWrap middleware on tool-endpoint error) via LoadAndDelete — this
// is the single point where ToolCallStatusFailed is injected
// into the round builder, since the adk stream itself never carries an
// explicit failed status on tool results.
func (a *roundAdapter) emitToolTerminal(h agentstream.StreamHandler, id, content string) {
	status := agentstream.ToolCallStatusCompleted
	if a.toolFailures != nil {
		if _, failed := a.toolFailures.LoadAndDelete(id); failed {
			status = agentstream.ToolCallStatusFailed
		}
	}
	h.OnToolCallUpdate(id, content, status)
}

// resetStreamState clears the per-position tool_call buffers. Called at
// the start of every forwardStream to ensure the buffering state does
// not leak across streams — a single Run may consume many streams (one
// per LLM call / assistant message), and eino adk's tool_call Index
// restarts from 0 on each new assistant message.
func (a *roundAdapter) resetStreamState() {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.toolCallIDs = make(map[int]string)
	a.toolCallNames = make(map[int]string)
	a.toolCallArgsBuf = make(map[int][]string)
	a.toolCallStarted = make(map[int]bool)
}

// forwardDirectMessage translates a non-streaming eino adk message
// (handleDirectMessage equivalent) into StreamHandler calls. Rules:
//   - role=Tool → OnToolCallUpdate(id, content, Completed). Treated as a
//     terminal result.
//   - role=Assistant (or similar): emit OnThoughtChunk first if
//     ReasoningContent is non-empty (acp ordering: reasoning before
//     content), then OnMessageChunk for Content, then OnToolCall +
//     OnToolCallUpdate(args, non-terminal) for any declared tool calls
//     with id+name ready.
func (a *roundAdapter) forwardDirectMessage(ctx context.Context, h agentstream.StreamHandler, m *schema.Message) {
	if m == nil {
		return
	}
	if m.Role == schema.Tool {
		// Treat a direct role=Tool message as a terminal tool result.
		// Content may be empty — the builder will still synthesise an
		// OnToolCallEnd via IsTerminal semantics. Only forward when the
		// id has been declared via a prior OnToolCall; otherwise the
		// builder would drop the terminal anyway (see round.Builder
		// OnToolCallUpdate guard) and we avoid a misleading warn there.
		if !a.isDeclared(m.ToolCallID) {
			logger.Warnf(ctx, "[round_adapter] skip terminal tool result for undeclared id=%s", m.ToolCallID)
			return
		}
		a.emitToolTerminal(h, m.ToolCallID, m.Content)
		return
	}

	if m.ReasoningContent != "" {
		h.OnThoughtChunk(m.ReasoningContent)
	}
	if text := directMessageText(m); text != "" {
		h.OnMessageChunk(text)
	}

	for i, tc := range m.ToolCalls {
		a.emitToolCall(ctx, h, tc, i)
	}
}

// forwardStream pumps chunks from an eino adk stream into StreamHandler
// calls. Tool-call argument fragments are buffered across chunks until
// id and name are both known (see forwardChunk). Tool result chunks
// (role=Tool) are also accumulated: eino adk may split a tool result
// across multiple chunks (empty "start" chunk + content chunks + empty
// trailing chunk). Emitting a terminal OnToolCallUpdate per chunk
// double-appends to the builder's accToolResults (causing spurious
// round flushes), while the previous "emit only when content != ”"
// guard silently dropped legitimately-empty tool results and left the
// tool call pending forever. Correct behaviour is: collect all
// role=Tool content per ToolCallID across the stream, emit exactly one
// terminal OnToolCallUpdate per ToolCallID at stream EOF with the
// concatenated content (which may be empty — the builder is fine with
// an empty terminal and will pair correctly).
//
// Error (non-EOF) exits do not emit terminals: the Run's deferred
// cleanup will synthesise [placeholder] results for any ids still
// pending on the builder, matching the behaviour before this refactor.
// maxBufferedToolResultBytes caps the total bytes accumulated for a
// single tool_call's streamed result in forwardStream. Tool results are
// buffered in memory until EOF (see forwardStream doc comment for why
// we can't emit a terminal per chunk), so an unbounded tool output
// would push the accumulated bytes into the roundAdapter's heap without
// backpressure. Once a tool exceeds this cap the buffer stops growing,
// subsequent chunks are dropped, and a truncation marker is appended
// at EOF so both the LLM's next-round prompt and the UI see an
// unambiguous "this result was too large" tail rather than silent
// truncation. 16 MiB is conservative against a ~200k-token context
// window (roughly 800 KiB at 4 bytes/token) — anything larger is
// always anomalous and should be caught upstream, not carried further.
const maxBufferedToolResultBytes = 16 * 1024 * 1024

// maxBufferedToolResultTotalBytes caps the SUM of buffered tool-result
// bytes across every tool_call within a single stream. The per-tool cap
// (maxBufferedToolResultBytes) bounds one runaway result, but a stream
// fan-out with N concurrent tools can each independently fill 16 MiB,
// so without this aggregate cap a single Run's heap could spike to
// N * maxBufferedToolResultBytes. Sized at 2x the per-tool cap so a
// normal multi-tool round still fits comfortably while a flood of
// large outputs is bounded; the (N+1)th tool that would push the total
// over this limit gets truncated immediately and tagged with the same
// marker as a per-tool overflow.
const maxBufferedToolResultTotalBytes = 32 * 1024 * 1024

// toolResultTruncatedMarker is appended to a tool result whose buffered
// bytes exceeded maxBufferedToolResultBytes. Kept as a distinct sentinel
// (not msgextra.FailedPrefix) because the tool itself did not fail —
// its output was just too large to forward verbatim.
const toolResultTruncatedMarker = "\n\n[truncated: tool result exceeded buffer cap]"

func (a *roundAdapter) forwardStream(h agentstream.StreamHandler, onError func(error), stream *schema.StreamReader[*schema.Message]) error {
	defer stream.Close()

	// Scope the per-position buffers to this stream. See the roundAdapter
	// doc comment ("State scope") for why cross-stream carry-over would
	// corrupt the next round's OnToolCall / args routing.
	a.resetStreamState()

	toolParts := make(map[string][]string)
	// toolBytes tracks the running byte total per tool_call so we can
	// apply maxBufferedToolResultBytes without re-summing on every chunk.
	toolBytes := make(map[string]int)
	// totalBytes tracks the aggregate of toolBytes across all tools in
	// this stream so a fan-out of large results cannot push memory past
	// maxBufferedToolResultTotalBytes.
	totalBytes := 0
	// toolTruncated records which tool_calls have already hit the cap so
	// the truncation marker is appended exactly once (at EOF) and later
	// chunks are dropped cheaply without re-logging.
	toolTruncated := make(map[string]bool)
	// toolOrder preserves first-arrival order so emission is deterministic
	// across repeated runs even though map iteration is not.
	var toolOrder []string

	for {
		chunk, err := stream.Recv()
		if err != nil {
			if err == io.EOF {
				for _, id := range toolOrder {
					if !a.isDeclared(id) {
						// Upstream produced a tool result for an id
						// that was never declared as a tool_call (e.g.
						// an assistant chunk delivered the id but never
						// its name, so OnToolCall was never emitted).
						// Forwarding it would violate round.Builder's
						// "terminal must reference a declared tool_call"
						// invariant and cause the real result to be
						// silently dropped at flush time.
						logger.Warnf(context.Background(), "[round_adapter] skip EOF terminal for undeclared tool_result id=%s", id)
						continue
					}
					content := strings.Join(toolParts[id], "")
					if toolTruncated[id] {
						content += toolResultTruncatedMarker
					}
					a.emitToolTerminal(h, id, content)
				}
				return nil
			}
			if onError != nil {
				onError(err)
			}
			return fmt.Errorf("eino stream recv: %w", err)
		}
		if chunk == nil {
			continue
		}
		if chunk.Role == schema.Tool {
			id := chunk.ToolCallID
			if id == "" {
				// Defensive: an upstream chunk with role=Tool but no
				// ToolCallID would otherwise pollute the aggregation map
				// under the empty-string key, merging unrelated results.
				logger.Warnf(context.Background(), "[round_adapter] drop tool chunk with empty ToolCallID")
				continue
			}
			if _, seen := toolParts[id]; !seen {
				toolOrder = append(toolOrder, id)
				toolParts[id] = nil
			}
			text := streamChunkText(chunk)
			if text == "" {
				continue
			}
			if toolTruncated[id] {
				// Already capped — drop this chunk. We still keep id
				// in toolOrder / toolParts so the EOF emission loop
				// produces exactly one terminal per declared id.
				continue
			}
			// Cap against both the per-tool budget and the per-stream
			// aggregate. Whichever is smaller wins so a tool already
			// near its individual limit doesn't escape via the stream
			// budget, and a stream already near the aggregate doesn't
			// let a fresh tool consume its full per-tool allowance.
			perToolRemaining := maxBufferedToolResultBytes - toolBytes[id]
			streamRemaining := maxBufferedToolResultTotalBytes - totalBytes
			remaining := perToolRemaining
			if streamRemaining < remaining {
				remaining = streamRemaining
			}
			if len(text) > remaining {
				if remaining > 0 {
					toolParts[id] = append(toolParts[id], text[:remaining])
					toolBytes[id] += remaining
					totalBytes += remaining
				}
				toolTruncated[id] = true
				if perToolRemaining <= streamRemaining {
					logger.Warnf(context.Background(), "[round_adapter] tool result exceeded per-tool cap %d bytes, truncating id=%s", maxBufferedToolResultBytes, id)
				} else {
					logger.Warnf(context.Background(), "[round_adapter] tool result exceeded per-stream cap %d bytes, truncating id=%s", maxBufferedToolResultTotalBytes, id)
				}
				continue
			}
			toolParts[id] = append(toolParts[id], text)
			toolBytes[id] += len(text)
			totalBytes += len(text)
			continue
		}
		a.forwardChunk(h, chunk)
	}
}

// forwardChunk translates a single non-tool-result streaming chunk.
// Reasoning is emitted before content (so the builder's thought/message
// state machine matches acp's ordering). Tool-call fragments are
// buffered per-position until id and name are both known, at which
// point OnToolCall + OnToolCallUpdate fire.
//
// role=Tool chunks are handled one level up in forwardStream (they
// need cross-chunk accumulation); forwardChunk is never called with a
// role=Tool chunk.
func (a *roundAdapter) forwardChunk(h agentstream.StreamHandler, chunk *schema.Message) {
	if chunk == nil {
		return
	}

	if chunk.ReasoningContent != "" {
		h.OnThoughtChunk(chunk.ReasoningContent)
	}

	if chunk.Content != "" {
		h.OnMessageChunk(chunk.Content)
	}

	for i, tc := range chunk.ToolCalls {
		key := toolCallKey(tc, i)

		a.mu.Lock()
		if tc.ID != "" {
			a.toolCallIDs[key] = tc.ID
		}
		if tc.Function.Name != "" {
			a.toolCallNames[key] = tc.Function.Name
		}
		id := a.toolCallIDs[key]
		name := a.toolCallNames[key]
		started := a.toolCallStarted[key]
		bufferArgs := !started || id == "" || name == ""
		if bufferArgs && tc.Function.Arguments != "" {
			a.toolCallArgsBuf[key] = append(a.toolCallArgsBuf[key], tc.Function.Arguments)
		}
		var flushed []string
		if !started && id != "" && name != "" {
			a.toolCallStarted[key] = true
			flushed = a.toolCallArgsBuf[key]
			a.toolCallArgsBuf[key] = nil
		}
		a.mu.Unlock()

		if !started && id != "" && name != "" {
			a.markDeclared(id)
			h.OnToolCall(id, name)
			for _, fragment := range flushed {
				h.OnToolCallUpdate(id, fragment, agentstream.ToolCallStatusInProgress)
			}
		}

		if started && tc.Function.Arguments != "" && id != "" {
			h.OnToolCallUpdate(id, tc.Function.Arguments, agentstream.ToolCallStatusInProgress)
		}
	}
}

// emitToolCall flushes an adk-direct (non-streaming) tool call: OnToolCall
// then, if Arguments are present, one non-terminal OnToolCallUpdate with
// the full arg blob.
func (a *roundAdapter) emitToolCall(ctx context.Context, h agentstream.StreamHandler, tc schema.ToolCall, pos int) {
	if tc.ID == "" || tc.Function.Name == "" {
		logger.Warnf(ctx, "[round_adapter] skip tool_call without id/name: pos=%d id=%s name=%s", pos, tc.ID, tc.Function.Name)
		return
	}
	a.markDeclared(tc.ID)
	h.OnToolCall(tc.ID, tc.Function.Name)
	if tc.Function.Arguments != "" {
		h.OnToolCallUpdate(tc.ID, tc.Function.Arguments, agentstream.ToolCallStatusInProgress)
	}
}

// markDeclared records that OnToolCall has been emitted for id. See the
// declaredIDs field doc on roundAdapter for why this is Run-scoped (not
// reset by resetStreamState).
func (a *roundAdapter) markDeclared(id string) {
	if id == "" {
		return
	}
	a.mu.Lock()
	a.declaredIDs[id] = true
	a.mu.Unlock()
}

// isDeclared reports whether OnToolCall has been emitted for id in this
// adapter's lifetime.
func (a *roundAdapter) isDeclared(id string) bool {
	if id == "" {
		return false
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.declaredIDs[id]
}

func toolCallKey(tc schema.ToolCall, pos int) int {
	if tc.Index != nil {
		return *tc.Index
	}
	return pos
}

func directMessageText(m *schema.Message) string {
	if m == nil {
		return ""
	}
	if m.Content != "" {
		return m.Content
	}
	switch len(m.UserInputMultiContent) {
	case 0:
		return ""
	case 1:
		return m.UserInputMultiContent[0].Text
	}
	var sb strings.Builder
	for _, part := range m.UserInputMultiContent {
		if part.Text != "" {
			sb.WriteString(part.Text)
		}
	}
	return sb.String()
}

func streamChunkText(m *schema.Message) string {
	return directMessageText(m)
}
