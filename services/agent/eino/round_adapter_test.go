package eino

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/types/agentstream"
)

// fakeStreamHandler records every StreamHandler call verbatim so tests can
// assert on exact event order and payloads. The adapter's contract is all
// about ordering (reasoning before content, OnToolCall before OnToolCallUpdate,
// buffered args flushed in arrival order) so any laxness on the recording side
// would dilute the assertions.
type fakeStreamHandler struct {
	events []string
	// terminalStatus records the raw ToolCallStatus of the last terminal
	// update per tool_call id. Kept alongside the stringified events so
	// existing tests can keep asserting on "update:id:content:terminal"
	// while tests that need to distinguish Failed from Completed can
	// look up the concrete status without re-parsing.
	terminalStatus map[string]agentstream.ToolCallStatus
}

func (h *fakeStreamHandler) OnMessageChunk(text string) {
	h.events = append(h.events, "msg:"+text)
}

func (h *fakeStreamHandler) OnThoughtChunk(text string) {
	h.events = append(h.events, "thought:"+text)
}

func (h *fakeStreamHandler) OnToolCall(id, title string) {
	h.events = append(h.events, fmt.Sprintf("toolcall:%s:%s", id, title))
}

func (h *fakeStreamHandler) OnToolCallUpdate(id, content string, status agentstream.ToolCallStatus) {
	term := "progress"
	if status.IsTerminal() {
		term = "terminal"
		if h.terminalStatus == nil {
			h.terminalStatus = make(map[string]agentstream.ToolCallStatus)
		}
		h.terminalStatus[id] = status
	}
	h.events = append(h.events, fmt.Sprintf("update:%s:%s:%s", id, content, term))
}

func (h *fakeStreamHandler) OnTokenUsage(totalTokens int) {
	h.events = append(h.events, fmt.Sprintf("usage:%d", totalTokens))
}

// intPtr is a small helper — eino schema.ToolCall.Index is a *int.
func intPtr(v int) *int { return &v }

// TestRoundAdapter_ArgsArriveBeforeIDName exercises the core buffering
// contract from round_adapter.go:30 — when args are seen before (id, name)
// have both arrived, they must be buffered per-position and only replayed
// once OnToolCall has fired.
func TestRoundAdapter_ArgsArriveBeforeIDName(t *testing.T) {
	a := newRoundAdapter(nil)
	h := &fakeStreamHandler{}

	// Chunk 1: only args — no id, no name. Must NOT produce any StreamHandler
	// call; the fragment is held in the per-position buffer.
	a.forwardChunk(h, &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			Index:    intPtr(0),
			Function: schema.FunctionCall{Arguments: "frag1"},
		}},
	})
	if len(h.events) != 0 {
		t.Fatalf("args before id/name must not emit any event, got %v", h.events)
	}

	// Chunk 2: name only — still incomplete (id missing). Args chunk-2 is
	// also buffered.
	a.forwardChunk(h, &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			Index:    intPtr(0),
			Function: schema.FunctionCall{Name: "T", Arguments: "frag2"},
		}},
	})
	if len(h.events) != 0 {
		t.Fatalf("name-only chunk still incomplete, got %v", h.events)
	}

	// Chunk 3: id arrives (plus a new args fragment). Contract:
	//   1. OnToolCall(id, name) fires first.
	//   2. Then ALL buffered args (frag1, frag2, frag3) replay in arrival
	//      order as non-terminal OnToolCallUpdate calls.
	a.forwardChunk(h, &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			Index:    intPtr(0),
			ID:       "A",
			Function: schema.FunctionCall{Arguments: "frag3"},
		}},
	})

	want := []string{
		"toolcall:A:T",
		"update:A:frag1:progress",
		"update:A:frag2:progress",
		"update:A:frag3:progress",
	}
	if !eventsEqual(h.events, want) {
		t.Fatalf("flush order mismatch:\nwant %v\ngot  %v", want, h.events)
	}

	// Chunk 4: post-flush args go through as a plain non-terminal update
	// (no re-buffering, no duplicate OnToolCall).
	a.forwardChunk(h, &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			Index:    intPtr(0),
			Function: schema.FunctionCall{Arguments: "frag4"},
		}},
	})
	if h.events[len(h.events)-1] != "update:A:frag4:progress" {
		t.Fatalf("post-start args must flow through as plain update, got %v", h.events)
	}
	// Must still be exactly one OnToolCall for id=A — never a duplicate.
	starts := 0
	for _, e := range h.events {
		if e == "toolcall:A:T" {
			starts++
		}
	}
	if starts != 1 {
		t.Fatalf("OnToolCall(A) fired %d times, want 1", starts)
	}
}

// TestRoundAdapter_MultipleToolCallsInterleaved covers the second contract
// from round_adapter.go:30 — two tool_calls whose fragments arrive
// interleaved across chunks must route to the correct id on each side and
// flush independently.
func TestRoundAdapter_MultipleToolCallsInterleaved(t *testing.T) {
	a := newRoundAdapter(nil)
	h := &fakeStreamHandler{}

	// Chunk 1: position 0 gets args-only; position 1 gets args-only.
	a.forwardChunk(h, &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{
			{Index: intPtr(0), Function: schema.FunctionCall{Arguments: "a-1"}},
			{Index: intPtr(1), Function: schema.FunctionCall{Arguments: "b-1"}},
		},
	})
	if len(h.events) != 0 {
		t.Fatalf("both tool_calls still incomplete, got %v", h.events)
	}

	// Chunk 2: position 1 completes (id+name). Must flush tool B only;
	// tool A still pending.
	a.forwardChunk(h, &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{
			{Index: intPtr(1), ID: "B", Function: schema.FunctionCall{Name: "TB", Arguments: "b-2"}},
		},
	})
	want := []string{
		"toolcall:B:TB",
		"update:B:b-1:progress",
		"update:B:b-2:progress",
	}
	if !eventsEqual(h.events, want) {
		t.Fatalf("B should flush alone first:\nwant %v\ngot  %v", want, h.events)
	}

	// Chunk 3: position 0 completes (id+name); interleaved with a later
	// args fragment for B. Expectation: A's buffered args flush, then B's
	// new fragment flows through as plain update.
	a.forwardChunk(h, &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{
			{Index: intPtr(0), ID: "A", Function: schema.FunctionCall{Name: "TA", Arguments: "a-2"}},
			{Index: intPtr(1), Function: schema.FunctionCall{Arguments: "b-3"}},
		},
	})
	got := h.events[3:] // events after the first flush
	want = []string{
		"toolcall:A:TA",
		"update:A:a-1:progress",
		"update:A:a-2:progress",
		"update:B:b-3:progress",
	}
	if !eventsEqual(got, want) {
		t.Fatalf("interleaved flush mismatch:\nwant %v\ngot  %v", want, got)
	}
}

// TestRoundAdapter_DirectMessage_ReasoningBeforeContent verifies the
// non-streaming directMessage path follows the same "reasoning first, then
// content" ordering that the stream path guarantees, and that tool_calls
// declared inline (with id+name ready) emit OnToolCall+OnToolCallUpdate
// without buffering.
func TestRoundAdapter_DirectMessage_ReasoningBeforeContent(t *testing.T) {
	a := newRoundAdapter(nil)
	h := &fakeStreamHandler{}

	a.forwardDirectMessage(context.Background(), h, &schema.Message{
		Role:             schema.Assistant,
		ReasoningContent: "think hard",
		Content:          "final answer",
		ToolCalls: []schema.ToolCall{{
			ID:       "A",
			Function: schema.FunctionCall{Name: "T", Arguments: "{\"x\":1}"},
		}},
	})

	want := []string{
		"thought:think hard",
		"msg:final answer",
		"toolcall:A:T",
		"update:A:{\"x\":1}:progress",
	}
	if !eventsEqual(h.events, want) {
		t.Fatalf("directMessage ordering mismatch:\nwant %v\ngot  %v", want, h.events)
	}
}

// TestRoundAdapter_DirectMessage_RoleToolIsTerminal verifies role=Tool
// directMessages map to a terminal OnToolCallUpdate. With no failure
// flagged (nil toolFailures), the terminal is Completed — failure
// injection is covered by TestRoundAdapter_ToolFailureTagged. Requires
// a prior declaration (otherwise the guard drops the terminal; see
// TestRoundAdapter_DirectMessage_RoleTool_UndeclaredDropped).
func TestRoundAdapter_DirectMessage_RoleToolIsTerminal(t *testing.T) {
	a := newRoundAdapter(nil)
	h := &fakeStreamHandler{}

	// Declare the tool_call first via an assistant direct message so the
	// adapter's declaredIDs set carries A.
	a.forwardDirectMessage(context.Background(), h, &schema.Message{
		Role: schema.Assistant,
		ToolCalls: []schema.ToolCall{{
			ID:       "A",
			Function: schema.FunctionCall{Name: "T"},
		}},
	})
	a.forwardDirectMessage(context.Background(), h, &schema.Message{
		Role:       schema.Tool,
		ToolCallID: "A",
		Content:    "ok",
	})

	want := []string{
		"toolcall:A:T",
		"update:A:ok:terminal",
	}
	if !eventsEqual(h.events, want) {
		t.Fatalf("tool directMessage should emit terminal update:\nwant %v\ngot  %v", want, h.events)
	}
}

// TestRoundAdapter_ToolFailureTagged verifies the Critical bridge between
// the toolWrap middleware and the round builder: when the middleware
// records a tool_call id in the shared failures map (because the
// underlying tool endpoint returned an error), the adapter emits a
// Failed terminal instead of Completed — driving round.Builder's
// FailedPrefix on disk and the UI's success=false event. Without this
// wiring, an eino tool failure surfaces as a green "Completed" bubble
// whose content is an error string. Covers both the direct-message
// and stream-EOF terminal paths.
func TestRoundAdapter_ToolFailureTagged(t *testing.T) {
	t.Run("direct_message", func(t *testing.T) {
		failures := &sync.Map{}
		a := newRoundAdapter(failures)
		h := &fakeStreamHandler{}

		a.forwardDirectMessage(context.Background(), h, &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:       "F1",
				Function: schema.FunctionCall{Name: "T"},
			}},
		})

		// Middleware recorded this id as failed before the terminal arrived.
		failures.Store("F1", struct{}{})

		a.forwardDirectMessage(context.Background(), h, &schema.Message{
			Role:       schema.Tool,
			ToolCallID: "F1",
			Content:    "boom",
		})

		if got := h.terminalStatus["F1"]; got != agentstream.ToolCallStatusFailed {
			t.Fatalf("expected Failed terminal for F1, got status=%v events=%v", got, h.events)
		}
		// The adapter uses LoadAndDelete so the entry must be gone after
		// emission — otherwise a reused id (rare but possible with
		// degenerate upstreams) would stick to a stale failure flag.
		if _, still := failures.Load("F1"); still {
			t.Fatalf("failure flag must be consumed after emission (LoadAndDelete), still present")
		}
	})

	t.Run("stream_eof", func(t *testing.T) {
		failures := &sync.Map{}
		a := newRoundAdapter(failures)
		h := &fakeStreamHandler{}

		// Declare F2 via an assistant chunk (id+name eager) so the
		// declaredIDs guard accepts its EOF terminal.
		a.forwardChunk(h, &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				Index:    intPtr(0),
				ID:       "F2",
				Function: schema.FunctionCall{Name: "T", Arguments: "{}"},
			}},
		})

		failures.Store("F2", struct{}{})

		sr := schema.StreamReaderFromArray([]*schema.Message{
			{Role: schema.Tool, ToolCallID: "F2", Content: "partial-err"},
		})
		a.forwardStream(h, nil, sr)

		if got := h.terminalStatus["F2"]; got != agentstream.ToolCallStatusFailed {
			t.Fatalf("expected Failed terminal for F2 via stream EOF, got status=%v events=%v", got, h.events)
		}
		if _, still := failures.Load("F2"); still {
			t.Fatalf("failure flag must be consumed after stream-EOF emission, still present")
		}
	})

	t.Run("no_failure_flag_stays_completed", func(t *testing.T) {
		// Regression: a non-nil failures map must not colour unrelated
		// ids as Failed. Only the recorded ids flip; everyone else
		// stays Completed.
		failures := &sync.Map{}
		a := newRoundAdapter(failures)
		h := &fakeStreamHandler{}

		a.forwardDirectMessage(context.Background(), h, &schema.Message{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				ID:       "OK",
				Function: schema.FunctionCall{Name: "T"},
			}},
		})
		a.forwardDirectMessage(context.Background(), h, &schema.Message{
			Role:       schema.Tool,
			ToolCallID: "OK",
			Content:    "result",
		})

		if got := h.terminalStatus["OK"]; got != agentstream.ToolCallStatusCompleted {
			t.Fatalf("unflagged id must stay Completed, got %v", got)
		}
	})
}

// TestRoundAdapter_DirectMessage_RoleTool_UndeclaredDropped pins down the
// guard against stray role=Tool direct messages whose ToolCallID was
// never declared via OnToolCall. Forwarding would violate
// round.Builder's "terminal must reference a declared tool_call"
// invariant, which would then cause the real result to be silently
// dropped at flush time.
func TestRoundAdapter_DirectMessage_RoleTool_UndeclaredDropped(t *testing.T) {
	a := newRoundAdapter(nil)
	h := &fakeStreamHandler{}

	a.forwardDirectMessage(context.Background(), h, &schema.Message{
		Role:       schema.Tool,
		ToolCallID: "ghost",
		Content:    "nope",
	})

	if len(h.events) != 0 {
		t.Fatalf("undeclared tool result must be dropped, got %v", h.events)
	}
}

// TestRoundAdapter_StreamChunk_ReasoningAndContentSameChunk verifies the
// first impedance mismatch from round_adapter.go:30 — a single streaming
// chunk that carries both ReasoningContent and Content must split into
// OnThoughtChunk (first) and OnMessageChunk (second).
func TestRoundAdapter_StreamChunk_ReasoningAndContentSameChunk(t *testing.T) {
	a := newRoundAdapter(nil)
	h := &fakeStreamHandler{}

	a.forwardChunk(h, &schema.Message{
		Role:             schema.Assistant,
		ReasoningContent: "let me see",
		Content:          "the answer is 42",
	})

	want := []string{
		"thought:let me see",
		"msg:the answer is 42",
	}
	if !eventsEqual(h.events, want) {
		t.Fatalf("same-chunk reasoning/content ordering:\nwant %v\ngot  %v", want, h.events)
	}
}

// TestRoundAdapter_StreamChunk_RoleToolEmptyAccumulatedOnEOF pins down
// the current stream-path contract for role=Tool chunks: they are NOT
// forwarded from forwardChunk (which only handles assistant content +
// tool_call declarations). Instead, forwardStream accumulates tool
// content per ToolCallID across the whole stream and emits exactly
// ONE terminal OnToolCallUpdate at EOF — even when the accumulated
// content is empty.
//
// Regression guard: before the fix, forwardChunk emitted a terminal
// immediately for every non-empty role=Tool chunk and silently
// dropped empty ones. That double-counted multi-chunk tool results
// (breaking the builder's pairing math) AND lost legitimately-empty
// tool outputs (leaving the tool call pending → [placeholder]).
func TestRoundAdapter_StreamChunk_RoleToolEmptyAccumulatedOnEOF(t *testing.T) {
	a := newRoundAdapter(nil)
	h := &fakeStreamHandler{}

	// Two role=Tool chunks: the "start" chunk (empty content) and the
	// content chunk. Single terminal on EOF with the joined content.
	// Declare A first via an assistant chunk so the EOF terminal isn't
	// filtered out by the "undeclared terminal" guard.
	chunks := []*schema.Message{
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				Index:    intPtr(0),
				ID:       "A",
				Function: schema.FunctionCall{Name: "T"},
			}},
		},
		{Role: schema.Tool, ToolCallID: "A"},
		{Role: schema.Tool, ToolCallID: "A", Content: "done"},
	}
	stream := schema.StreamReaderFromArray(chunks)
	a.forwardStream(h, nil, stream)

	want := []string{
		"toolcall:A:T",
		"update:A:done:terminal",
	}
	if !eventsEqual(h.events, want) {
		t.Fatalf("multi-chunk tool result should flush once at EOF:\nwant %v\ngot  %v", want, h.events)
	}
}

// TestRoundAdapter_Stream_EmptyOnlyStillEmitsTerminal: a role=Tool
// stream whose chunks all carry empty content (e.g. a tool that
// legitimately returns no output) must still emit exactly one
// terminal OnToolCallUpdate at EOF with empty content. Before the
// fix the stream path dropped this and left the tool call pending.
func TestRoundAdapter_Stream_EmptyOnlyStillEmitsTerminal(t *testing.T) {
	a := newRoundAdapter(nil)
	h := &fakeStreamHandler{}

	chunks := []*schema.Message{
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				Index:    intPtr(0),
				ID:       "A",
				Function: schema.FunctionCall{Name: "T"},
			}},
		},
		{Role: schema.Tool, ToolCallID: "A"},
	}
	stream := schema.StreamReaderFromArray(chunks)
	a.forwardStream(h, nil, stream)

	want := []string{
		"toolcall:A:T",
		"update:A::terminal",
	}
	if !eventsEqual(h.events, want) {
		t.Fatalf("single empty tool chunk should still emit terminal:\nwant %v\ngot  %v", want, h.events)
	}
}

// TestRoundAdapter_Stream_MultipleToolResults: a stream carrying
// terminal results for two distinct tool calls emits one terminal
// per ToolCallID, in arrival order.
func TestRoundAdapter_Stream_MultipleToolResults(t *testing.T) {
	a := newRoundAdapter(nil)
	h := &fakeStreamHandler{}

	chunks := []*schema.Message{
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{
				{Index: intPtr(0), ID: "A", Function: schema.FunctionCall{Name: "TA"}},
				{Index: intPtr(1), ID: "B", Function: schema.FunctionCall{Name: "TB"}},
			},
		},
		{Role: schema.Tool, ToolCallID: "A", Content: "a1"},
		{Role: schema.Tool, ToolCallID: "B"},
		{Role: schema.Tool, ToolCallID: "A", Content: "a2"},
		{Role: schema.Tool, ToolCallID: "B", Content: "b1"},
	}
	stream := schema.StreamReaderFromArray(chunks)
	a.forwardStream(h, nil, stream)

	// A arrived first (id), so its terminal fires first; then B.
	want := []string{
		"toolcall:A:TA",
		"toolcall:B:TB",
		"update:A:a1a2:terminal",
		"update:B:b1:terminal",
	}
	if !eventsEqual(h.events, want) {
		t.Fatalf("multi-tool stream flush order mismatch:\nwant %v\ngot  %v", want, h.events)
	}
}

// TestRoundAdapter_Stream_UndeclaredEOFTerminalDropped pins down the
// guard against stray EOF terminals: if a stream delivers a role=Tool
// chunk whose ToolCallID was never declared via an assistant tool_call
// (id+name both resolved), the EOF terminal MUST be filtered out.
// Otherwise it lands on the builder as a terminal for an undeclared
// id, whose reorderToolResults would silently drop the real result at
// flush time.
func TestRoundAdapter_Stream_UndeclaredEOFTerminalDropped(t *testing.T) {
	a := newRoundAdapter(nil)
	h := &fakeStreamHandler{}

	// Assistant chunk delivers id but never name → OnToolCall is
	// never emitted, declaredIDs stays empty for this position.
	// Tool result for the same id follows.
	chunks := []*schema.Message{
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				Index: intPtr(0),
				ID:    "ghost",
			}},
		},
		{Role: schema.Tool, ToolCallID: "ghost", Content: "orphan"},
	}
	stream := schema.StreamReaderFromArray(chunks)
	a.forwardStream(h, nil, stream)

	if len(h.events) != 0 {
		t.Fatalf("undeclared EOF terminal must be dropped, got %v", h.events)
	}
}

// TestRoundAdapter_Stream_CrossStreamDeclarationCarriesOver verifies
// that declaredIDs persists across streams within a Run: when stream 1
// declares tool_call A and stream 2 delivers A's tool result in a
// stand-alone stream, the EOF terminal in stream 2 is NOT filtered by
// the undeclared-id guard.
func TestRoundAdapter_Stream_CrossStreamDeclarationCarriesOver(t *testing.T) {
	a := newRoundAdapter(nil)
	h := &fakeStreamHandler{}

	// Stream 1: only the assistant declaration, no tool result yet.
	stream1 := schema.StreamReaderFromArray([]*schema.Message{
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				Index:    intPtr(0),
				ID:       "A",
				Function: schema.FunctionCall{Name: "TA"},
			}},
		},
	})
	a.forwardStream(h, nil, stream1)

	// Stream 2: only the tool result. Even though this stream never
	// re-declares A, declaredIDs still carries it from stream 1.
	stream2 := schema.StreamReaderFromArray([]*schema.Message{
		{Role: schema.Tool, ToolCallID: "A", Content: "a-result"},
	})
	a.forwardStream(h, nil, stream2)

	want := []string{
		"toolcall:A:TA",
		"update:A:a-result:terminal",
	}
	if !eventsEqual(h.events, want) {
		t.Fatalf("cross-stream declaration carry-over:\nwant %v\ngot  %v", want, h.events)
	}
}

// TestRoundAdapter_Stream_ErrorSkipsToolTerminals: when the stream
// errors (non-EOF), the accumulated tool results are NOT emitted.
// The Run's deferred cleanup will synthesise [placeholder] results
// for any still-pending ids in the builder, matching the behaviour
// before this refactor.
func TestRoundAdapter_Stream_ErrorSkipsToolTerminals(t *testing.T) {
	a := newRoundAdapter(nil)
	h := &fakeStreamHandler{}

	sr, sw := schema.Pipe[*schema.Message](2)
	sw.Send(&schema.Message{Role: schema.Tool, ToolCallID: "A", Content: "partial"}, nil)
	sw.Send(nil, errors.New("network broke"))
	sw.Close()

	var gotErr error
	a.forwardStream(h, func(e error) { gotErr = e }, sr)

	if gotErr == nil || gotErr.Error() != "network broke" {
		t.Fatalf("expected stream error propagated, got %v", gotErr)
	}
	if len(h.events) != 0 {
		t.Fatalf("error paths must not emit terminals (builder's deferred cleanup handles pending ids), got %v", h.events)
	}
}

// TestRoundAdapter_Stream_IndexReusedAcrossStreams pins down the fix for
// the cross-round state leak in forwardStream. eino adk restarts
// tool_call Index from 0 on each new assistant message, so a single Run
// that consumes two streams will see Index=0 in both. The adapter must
// reset its per-position buffers at every forwardStream entry — otherwise
// stream 2's tool_call (Index=0, ID=B) hits stale toolCallStarted[0]=true
// from stream 1, OnToolCall(B, ...) is skipped, and the args + terminal
// for B land on the handler without a declaring OnToolCall, corrupting
// both the UI bubble state and the builder's on-disk pairing.
func TestRoundAdapter_Stream_IndexReusedAcrossStreams(t *testing.T) {
	a := newRoundAdapter(nil)
	h := &fakeStreamHandler{}

	// Stream 1: one tool call A at Index 0, with its terminal result.
	stream1 := schema.StreamReaderFromArray([]*schema.Message{
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				Index:    intPtr(0),
				ID:       "A",
				Function: schema.FunctionCall{Name: "TA", Arguments: "a-args"},
			}},
		},
		{Role: schema.Tool, ToolCallID: "A", Content: "a-result"},
	})
	a.forwardStream(h, nil, stream1)

	// Stream 2: a DIFFERENT tool call B, also at Index 0. Without the
	// fix, toolCallStarted[0]=true from stream 1 would cause OnToolCall(B)
	// to be skipped.
	stream2 := schema.StreamReaderFromArray([]*schema.Message{
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				Index:    intPtr(0),
				ID:       "B",
				Function: schema.FunctionCall{Name: "TB", Arguments: "b-args"},
			}},
		},
		{Role: schema.Tool, ToolCallID: "B", Content: "b-result"},
	})
	a.forwardStream(h, nil, stream2)

	want := []string{
		"toolcall:A:TA",
		"update:A:a-args:progress",
		"update:A:a-result:terminal",
		"toolcall:B:TB",
		"update:B:b-args:progress",
		"update:B:b-result:terminal",
	}
	if !eventsEqual(h.events, want) {
		t.Fatalf("cross-stream Index=0 reuse:\nwant %v\ngot  %v", want, h.events)
	}
}

func eventsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// TestRoundAdapter_Stream_ToolResultBufferCap pins down the memory
// guard introduced with maxBufferedToolResultBytes: a tool whose
// streamed result exceeds the cap must stop growing the buffer and
// surface the truncation to the caller via a sentinel marker at EOF.
// Silent growth would let an unbounded tool output push the
// roundAdapter heap arbitrarily large before EOF arrives.
func TestRoundAdapter_Stream_ToolResultBufferCap(t *testing.T) {
	a := newRoundAdapter(nil)
	h := &fakeStreamHandler{}

	// Emit assistant declaration, then stream a tool result in chunks
	// large enough to exceed maxBufferedToolResultBytes. Each chunk is
	// 1 MiB of 'x'; the cap is 16 MiB, so 18 chunks forces the final
	// two chunks into the truncation path.
	chunkText := strings.Repeat("x", 1024*1024)
	chunks := []*schema.Message{
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				Index: intPtr(0), ID: "A",
				Function: schema.FunctionCall{Name: "TA"},
			}},
		},
	}
	for range 18 {
		chunks = append(chunks, &schema.Message{Role: schema.Tool, ToolCallID: "A", Content: chunkText})
	}
	stream := schema.StreamReaderFromArray(chunks)
	a.forwardStream(h, nil, stream)

	// Expect exactly one terminal, capped at maxBufferedToolResultBytes
	// plus the truncation marker tail.
	var terminal string
	for _, ev := range h.events {
		if strings.HasPrefix(ev, "update:A:") && strings.HasSuffix(ev, ":terminal") {
			terminal = ev
			break
		}
	}
	if terminal == "" {
		t.Fatalf("expected terminal update for A, got events=%v", h.events)
	}
	// terminal is "update:A:<content>:terminal"; strip the fixed prefix
	// and suffix to pull out <content>.
	content := strings.TrimSuffix(strings.TrimPrefix(terminal, "update:A:"), ":terminal")
	if !strings.HasSuffix(content, toolResultTruncatedMarker) {
		t.Fatalf("truncated terminal must end with truncation marker, got trailing %q", content[max(0, len(content)-80):])
	}
	bodyLen := len(content) - len(toolResultTruncatedMarker)
	if bodyLen != maxBufferedToolResultBytes {
		t.Fatalf("truncated body length: got %d want exactly %d (cap)", bodyLen, maxBufferedToolResultBytes)
	}
}

// TestRoundAdapter_Stream_ToolResultBufferCap_UnderCap makes sure the
// cap logic does NOT alter tool results that fit within the limit —
// the truncation marker must not appear in the common path.
func TestRoundAdapter_Stream_ToolResultBufferCap_UnderCap(t *testing.T) {
	a := newRoundAdapter(nil)
	h := &fakeStreamHandler{}

	chunks := []*schema.Message{
		{
			Role: schema.Assistant,
			ToolCalls: []schema.ToolCall{{
				Index: intPtr(0), ID: "A",
				Function: schema.FunctionCall{Name: "TA"},
			}},
		},
		{Role: schema.Tool, ToolCallID: "A", Content: "hello"},
		{Role: schema.Tool, ToolCallID: "A", Content: " world"},
	}
	stream := schema.StreamReaderFromArray(chunks)
	a.forwardStream(h, nil, stream)

	want := []string{
		"toolcall:A:TA",
		"update:A:hello world:terminal",
	}
	if !eventsEqual(h.events, want) {
		t.Fatalf("under-cap tool stream must pass through verbatim:\nwant %v\ngot  %v", want, h.events)
	}
}
