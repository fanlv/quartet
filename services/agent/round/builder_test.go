package round

import (
	"testing"

	"github.com/cloudwego/eino/schema"
	"github.com/fanlv/quartet/types/agentstream"
	"github.com/fanlv/quartet/types/msgextra"
)

// stubHandler is a no-op agui.EventHandler used to drive a Builder without
// a live UI. It also records the sequence of id emissions so tests can
// assert that captureMsgIDAfterStart captured the correct id.
type stubHandler struct {
	nextID string
	lastID string

	// Call logs: tests that care only about state ignore these; tests
	// for EmitPendingEnds assert on the exact end-event sequence.
	messageEnds   int
	thoughtEnds   int
	toolCallEnded []string
	// toolResultContent captures the content passed to OnToolCallResult
	// per tool-call id; tests assert that the [failed] prefix is forwarded
	// live so the UI sees the same text as disk.
	toolResultContent map[string]string
	// toolEndSuccess captures the success flag passed to OnToolCallEnd
	// per id so tests can assert failure propagation.
	toolEndSuccess map[string]bool
	// toolResultSuccess captures the success flag passed to
	// OnToolCallResult per id.
	toolResultSuccess map[string]bool
	// toolInterruptedReason captures the reason string passed to
	// OnToolCallInterrupted per id so tests can assert that the live
	// placeholder reason matches the on-disk placeholder reason.
	toolInterruptedReason map[string]string
	// toolStitchedContent / toolStitchedSuccess / toolStitchedAgeMs
	// capture the payload passed to OnToolCallStitched per id so tests
	// can assert that a late-arriving terminal also fires a live UI
	// correction (and not just a disk stitch).
	toolStitchedContent map[string]string
	toolStitchedSuccess map[string]bool
	toolStitchedAgeMs   map[string]int64
}

func (h *stubHandler) setNextID(id string)   { h.nextID = id }
func (h *stubHandler) LastMessageID() string { return h.lastID }

func (h *stubHandler) OnMessageStart() error                { h.lastID = h.nextID; return nil }
func (h *stubHandler) OnMessageDelta(string) error          { return nil }
func (h *stubHandler) OnMessageEnd() error                  { h.messageEnds++; return nil }
func (h *stubHandler) OnThoughtStart() error                { h.lastID = h.nextID; return nil }
func (h *stubHandler) OnThoughtDelta(string) error          { return nil }
func (h *stubHandler) OnThoughtEnd() error                  { h.thoughtEnds++; return nil }
func (h *stubHandler) OnToolCallStart(string, string) error { return nil }
func (h *stubHandler) OnToolCallArgs(string, string) error  { return nil }
func (h *stubHandler) OnToolCallResult(id, content string, success bool) error {
	if h.toolResultContent == nil {
		h.toolResultContent = map[string]string{}
	}
	if h.toolResultSuccess == nil {
		h.toolResultSuccess = map[string]bool{}
	}
	h.toolResultContent[id] = content
	h.toolResultSuccess[id] = success
	return nil
}
func (h *stubHandler) OnToolCallEnd(id string, success bool) error {
	if h.toolEndSuccess == nil {
		h.toolEndSuccess = map[string]bool{}
	}
	h.toolCallEnded = append(h.toolCallEnded, id)
	h.toolEndSuccess[id] = success
	return nil
}
func (h *stubHandler) OnToolCallInterrupted(id string, reason string) error {
	if h.toolEndSuccess == nil {
		h.toolEndSuccess = map[string]bool{}
	}
	if h.toolInterruptedReason == nil {
		h.toolInterruptedReason = map[string]string{}
	}
	h.toolCallEnded = append(h.toolCallEnded, id)
	h.toolEndSuccess[id] = false
	h.toolInterruptedReason[id] = reason
	return nil
}
func (h *stubHandler) OnToolCallStitched(id string, content string, success bool, supersededAgoMs int64) error {
	if h.toolStitchedContent == nil {
		h.toolStitchedContent = map[string]string{}
	}
	if h.toolStitchedSuccess == nil {
		h.toolStitchedSuccess = map[string]bool{}
	}
	if h.toolStitchedAgeMs == nil {
		h.toolStitchedAgeMs = map[string]int64{}
	}
	h.toolStitchedContent[id] = content
	h.toolStitchedSuccess[id] = success
	h.toolStitchedAgeMs[id] = supersededAgoMs
	return nil
}
func (h *stubHandler) OnTokenUsage(int) error { return nil }
func (h *stubHandler) OnError(error)          {}

// assertToolResultOrder checks that `roleTool` messages in `msgs` appear
// in the exact order given by `wantIDs`. Non-tool messages are ignored.
func assertToolResultOrder(t *testing.T, msgs []*schema.Message, wantIDs []string) {
	t.Helper()
	got := []string{}
	for _, m := range msgs {
		if m.Role == schema.Tool {
			got = append(got, m.ToolCallID)
		}
	}
	if len(got) != len(wantIDs) {
		t.Fatalf("tool result count: got %d, want %d (got ids=%v want=%v)", len(got), len(wantIDs), got, wantIDs)
	}
	for i := range got {
		if got[i] != wantIDs[i] {
			t.Fatalf("tool result[%d]: got %q want %q (full got=%v want=%v)", i, got[i], wantIDs[i], got, wantIDs)
		}
	}
}

// TestRound_PlaceholderSynthesisOnPartialRound verifies that flushing a
// half-complete round synthesises [placeholder] role=tool entries for
// every missing tool_call_id and that the round still contains one
// role=tool per declared tool call.
func TestRound_PlaceholderSynthesisOnPartialRound(t *testing.T) {
	b := New()
	h := &stubHandler{}
	var flushed [][]*schema.Message
	b.Reset(h, func(msgs []*schema.Message) {
		flushed = append(flushed, msgs)
	})

	b.OnMessageChunk("hello")
	b.OnToolCall("tc-A", "ToolA")
	b.OnToolCall("tc-B", "ToolB")
	b.OnToolCall("tc-C", "ToolC")
	// Only tc-B completes; A and C remain pending.
	b.OnToolCallUpdate("tc-B", "B result", agentstream.ToolCallStatusCompleted)

	// Pending: the round did not flush naturally (len(results) < len(calls)).
	collected := b.CollectMessages(ReasonCanceled)
	if len(collected) == 0 {
		t.Fatalf("expected CollectMessages to return the flushed round")
	}
	assertToolResultOrder(t, collected, []string{"tc-A", "tc-B", "tc-C"})

	// Verify tc-A and tc-C are placeholders; tc-B is not.
	for _, m := range collected {
		if m.Role != schema.Tool {
			continue
		}
		isPh := isPlaceholder(m)
		switch m.ToolCallID {
		case "tc-A", "tc-C":
			if !isPh {
				t.Errorf("expected placeholder for %s, got real", m.ToolCallID)
			}
		case "tc-B":
			if isPh {
				t.Errorf("expected real result for tc-B, got placeholder")
			}
			if m.Content != "B result" {
				t.Errorf("tc-B content: got %q want %q", m.Content, "B result")
			}
		}
	}

	// onFlush should have fired exactly once with the full round.
	if len(flushed) != 1 {
		t.Fatalf("onFlush fired %d times, expected 1", len(flushed))
	}
}

// TestRound_StableSortByDeclaration verifies that tool results arriving
// in a different order than the assistant declared them are reordered
// on flush to match assistant.ToolCalls.
func TestRound_StableSortByDeclaration(t *testing.T) {
	b := New()
	h := &stubHandler{}
	b.Reset(h, func([]*schema.Message) {})

	b.OnMessageChunk("thinking")
	b.OnToolCall("tc-A", "ToolA")
	b.OnToolCall("tc-B", "ToolB")
	b.OnToolCall("tc-C", "ToolC")
	// Results arrive B, A, C — out of declaration order.
	b.OnToolCallUpdate("tc-B", "B", agentstream.ToolCallStatusCompleted)
	b.OnToolCallUpdate("tc-A", "A", agentstream.ToolCallStatusCompleted)
	b.OnToolCallUpdate("tc-C", "C", agentstream.ToolCallStatusCompleted)

	collected := b.CollectMessages(ReasonInterrupted)
	if len(collected) == 0 {
		t.Fatalf("expected flushed round")
	}
	assertToolResultOrder(t, collected, []string{"tc-A", "tc-B", "tc-C"})
	for _, m := range collected {
		if m.Role == schema.Tool && isPlaceholder(m) {
			t.Errorf("did not expect placeholders when all results arrived: %s", m.ToolCallID)
		}
	}
}

// TestRound_LateTerminalAfterSupersededStitchesBack is the regression test
// for the "silently dropped real tool result" bug. Before the fix, a
// terminal arriving AFTER an eager superseded flush was logged at INFO
// and discarded, leaving the round permanently pinned to its
// [placeholder] superseded row (both on disk and in the next round's LLM
// context). The builder should now call the stitch callback AND rewrite
// the matching placeholder in completedMessages so a later
// CollectMessages / history reload sees the real result.
func TestRound_LateTerminalAfterSupersededStitchesBack(t *testing.T) {
	b := New()
	h := &stubHandler{}
	var flushed [][]*schema.Message
	b.Reset(h, func(msgs []*schema.Message) {
		flushed = append(flushed, msgs)
	})

	type stitchCall struct {
		id   string
		real *schema.Message
	}
	var stitches []stitchCall
	b.SetStitcher(func(toolCallID string, real *schema.Message) {
		stitches = append(stitches, stitchCall{id: toolCallID, real: real})
	})

	// Round 1: assistant declares tc-A and tc-B, only tc-B terminates.
	b.OnMessageChunk("round1 msg")
	b.OnToolCall("tc-A", "ToolA")
	b.OnToolCall("tc-B", "ToolB")
	b.OnToolCallUpdate("tc-B", "B done", agentstream.ToolCallStatusCompleted)

	// A new assistant chunk arrives before tc-A's terminal → eager flush
	// materialises tc-A as a superseded placeholder on disk.
	b.OnMessageChunk("round2 msg")
	if len(flushed) != 1 {
		t.Fatalf("expected exactly 1 eager flush, got %d", len(flushed))
	}
	// Sanity check: the flushed round has a placeholder for tc-A.
	var placeholderBefore *schema.Message
	for _, m := range flushed[0] {
		if m.Role == schema.Tool && m.ToolCallID == "tc-A" {
			placeholderBefore = m
		}
	}
	if placeholderBefore == nil || !isPlaceholder(placeholderBefore) {
		t.Fatalf("expected tc-A placeholder in the eager-flushed round, got %+v", placeholderBefore)
	}

	// tc-A's real terminal finally arrives, AFTER round 1 was flushed.
	b.OnToolCallUpdate("tc-A", "A late result", agentstream.ToolCallStatusCompleted)

	// The stitcher must have been invoked with tc-A and the real result.
	if len(stitches) != 1 {
		t.Fatalf("expected exactly 1 stitch call, got %d", len(stitches))
	}
	got := stitches[0]
	if got.id != "tc-A" {
		t.Errorf("stitch id: got %q want %q", got.id, "tc-A")
	}
	if got.real == nil || got.real.Role != schema.Tool || got.real.ToolCallID != "tc-A" {
		t.Fatalf("stitch payload malformed: %+v", got.real)
	}
	if got.real.Content != "A late result" {
		t.Errorf("stitch content: got %q want %q", got.real.Content, "A late result")
	}
	if isPlaceholder(got.real) {
		t.Errorf("stitch payload must not carry the placeholder flag")
	}

	// completedMessages must also have been rewritten: a subsequent
	// CollectMessages should return the real result, not the placeholder.
	// We drain via CollectMessages so this asserts the same buffer that
	// downstream consumers (e.g. ACP agent.CollectMessages) read.
	collected := b.CollectMessages(ReasonInterrupted)
	var stitchedInMemory *schema.Message
	for _, m := range collected {
		if m.Role == schema.Tool && m.ToolCallID == "tc-A" {
			stitchedInMemory = m
			break
		}
	}
	if stitchedInMemory == nil {
		t.Fatalf("tc-A missing from completedMessages after stitch")
	}
	if isPlaceholder(stitchedInMemory) {
		t.Errorf("tc-A still placeholder in completedMessages after stitch")
	}
	if stitchedInMemory.Content != "A late result" {
		t.Errorf("tc-A content in completedMessages: got %q want %q", stitchedInMemory.Content, "A late result")
	}

	// The live UI handler must also have been notified so an open page
	// rewrites the Placeholder bubble in place — without this, the bubble
	// stays "interrupted" until a refresh even though disk and memory now
	// carry the real result.
	if got, ok := h.toolStitchedContent["tc-A"]; !ok {
		t.Fatalf("OnToolCallStitched not invoked for tc-A on late terminal")
	} else if got != "A late result" {
		t.Errorf("OnToolCallStitched content: got %q want %q", got, "A late result")
	}
	if got := h.toolStitchedSuccess["tc-A"]; !got {
		t.Errorf("OnToolCallStitched success: got %v want true", got)
	}
	if got, ok := h.toolStitchedAgeMs["tc-A"]; !ok || got < 0 {
		t.Errorf("OnToolCallStitched supersededAgoMs: got %v want >=0", got)
	}
}

// TestRound_LateFailedTerminalAfterSupersededStitchesWithFailedPrefix
// checks that a late FAILED terminal follows the same on-disk contract
// as a normal failed terminal: the stitched content carries the
// [failed] prefix so the next round's LLM context renders the failure.
func TestRound_LateFailedTerminalAfterSupersededStitchesWithFailedPrefix(t *testing.T) {
	b := New()
	h := &stubHandler{}
	b.Reset(h, func([]*schema.Message) {})

	var stitched *schema.Message
	b.SetStitcher(func(toolCallID string, real *schema.Message) {
		if toolCallID == "tc-A" {
			stitched = real
		}
	})

	b.OnMessageChunk("r1")
	b.OnToolCall("tc-A", "ToolA")
	b.OnToolCall("tc-B", "ToolB")
	b.OnToolCallUpdate("tc-B", "B", agentstream.ToolCallStatusCompleted)
	b.OnMessageChunk("r2") // supersede
	b.OnToolCallUpdate("tc-A", "boom", agentstream.ToolCallStatusFailed)

	if stitched == nil {
		t.Fatalf("expected stitcher to be invoked for tc-A on late failed terminal")
	}
	want := msgextra.FailedPrefix + "boom"
	if stitched.Content != want {
		t.Errorf("stitched content: got %q want %q", stitched.Content, want)
	}
	// Live UI must also receive the failed payload (with the [failed]
	// prefix) so the bubble flips from Placeholder to Error in place.
	if got, ok := h.toolStitchedContent["tc-A"]; !ok {
		t.Fatalf("OnToolCallStitched not invoked for tc-A on late failed terminal")
	} else if got != want {
		t.Errorf("OnToolCallStitched content: got %q want %q", got, want)
	}
	if got := h.toolStitchedSuccess["tc-A"]; got {
		t.Errorf("OnToolCallStitched success: got true want false on failed terminal")
	}
}

// TestRound_EagerFlushSupersededByNewMessage exercises the rare
// "superseded" path: assistant declared A and B, only B terminal
// arrives, then a new message chunk starts — the old round must flush
// with A as placeholder[superseded] and B as real, preserving
// declaration order.
func TestRound_EagerFlushSupersededByNewMessage(t *testing.T) {
	b := New()
	h := &stubHandler{}
	var flushed [][]*schema.Message
	b.Reset(h, func(msgs []*schema.Message) {
		flushed = append(flushed, msgs)
	})

	b.OnMessageChunk("round1 msg")
	b.OnToolCall("tc-A", "ToolA")
	b.OnToolCall("tc-B", "ToolB")
	b.OnToolCallUpdate("tc-B", "B done", agentstream.ToolCallStatusCompleted)
	// Round has 2 calls but only 1 result, so no natural flush yet.

	// New message chunk — round 1 is now superseded. Builder must eagerly
	// flush round 1 before accumulating round 2.
	b.OnMessageChunk("round2 msg")

	if len(flushed) != 1 {
		t.Fatalf("expected exactly 1 eager flush, got %d", len(flushed))
	}
	round1 := flushed[0]
	assertToolResultOrder(t, round1, []string{"tc-A", "tc-B"})

	for _, m := range round1 {
		if m.Role != schema.Tool {
			continue
		}
		if m.ToolCallID == "tc-A" {
			if !isPlaceholder(m) {
				t.Errorf("tc-A must be placeholder on superseded flush")
			}
			// Content must indicate superseded reason.
			want := msgextra.PlaceholderPrefix + " " + msgextra.PlaceholderReasonSuperseded
			if m.Content != want {
				t.Errorf("tc-A placeholder content: got %q want %q", m.Content, want)
			}
		}
	}
}

// TestRound_CompleteRoundFlushesOnLastTerminal verifies the natural
// flush path: when the tool-result count catches up with the tool-call
// count, the builder flushes immediately without a CollectMessages call.
func TestRound_CompleteRoundFlushesOnLastTerminal(t *testing.T) {
	b := New()
	h := &stubHandler{}
	var flushed [][]*schema.Message
	b.Reset(h, func(msgs []*schema.Message) {
		flushed = append(flushed, msgs)
	})

	b.OnMessageChunk("hi")
	b.OnToolCall("tc-A", "ToolA")
	b.OnToolCallUpdate("tc-A", "A result", agentstream.ToolCallStatusCompleted)

	if len(flushed) != 1 {
		t.Fatalf("expected exactly 1 natural flush, got %d", len(flushed))
	}
	round := flushed[0]
	assertToolResultOrder(t, round, []string{"tc-A"})
	for _, m := range round {
		if m.Role == schema.Tool && isPlaceholder(m) {
			t.Errorf("did not expect placeholder for complete round")
		}
	}
}

// TestRound_MsgIDCapturedAtStartNotFlush verifies the critical ordering
// property: when a new message chunk triggers an eager flush of a
// prior round and then a new start event fires, the new start's id
// lands on the NEW round, and the flushed (old) round keeps the id
// captured at its own start time.
func TestRound_MsgIDCapturedAtStartNotFlush(t *testing.T) {
	b := New()
	h := &stubHandler{}
	var flushed [][]*schema.Message
	b.Reset(h, func(msgs []*schema.Message) {
		flushed = append(flushed, msgs)
	})

	// Round 1 start: handler will hand out id "M1".
	h.setNextID("M1")
	b.OnMessageChunk("round 1")
	b.OnToolCall("tc-A", "ToolA")
	// only partial — force eager flush on next message chunk.

	// Round 2 start: handler hands out "M2". The flush of round 1 must
	// embed "M1" in its assistant Extra; round 2 should accumulate "M2".
	h.setNextID("M2")
	b.OnMessageChunk("round 2")

	if len(flushed) != 1 {
		t.Fatalf("expected 1 eager flush, got %d", len(flushed))
	}
	round1 := flushed[0]
	if len(round1) == 0 || round1[0].Role != schema.Assistant {
		t.Fatalf("round1 must start with assistant message")
	}
	if got, _ := round1[0].Extra[msgextra.KeyMsgID].(string); got != "M1" {
		t.Errorf("round1 msg_id: got %q want %q", got, "M1")
	}

	// Collect drains completedMessages (both round1 previously flushed
	// and the newly flushed round2) plus any in-flight round at the
	// time of call. Round2 is the last assistant in the returned slice.
	all := b.CollectMessages(ReasonInterrupted)
	var round2Assistant *schema.Message
	for i := len(all) - 1; i >= 0; i-- {
		if all[i].Role == schema.Assistant {
			round2Assistant = all[i]
			break
		}
	}
	if round2Assistant == nil {
		t.Fatalf("expected to find round2 assistant in CollectMessages output")
	}
	if got, _ := round2Assistant.Extra[msgextra.KeyMsgID].(string); got != "M2" {
		t.Errorf("round2 msg_id: got %q want %q", got, "M2")
	}
}

// TestEmitPendingEnds_ToolCallsPendingOnStop pins down the core
// regression guard for the "tool bubble stuck pending" bug: when a
// Run is stopped mid-flight with tool calls that never received a
// terminal, EmitPendingEnds MUST fire OnToolCallEnd for each pending
// id so the UI can close the bubble. Before the fix, FlushPendingMessages
// only called CollectMessages, which cleared accToolCalls (synthesising
// [placeholder] on disk) but never emitted UI end events — the bubble
// stayed "pending" until history reload.
func TestEmitPendingEnds_ToolCallsPendingOnStop(t *testing.T) {
	b := New()
	h := &stubHandler{}
	b.Reset(h, func([]*schema.Message) {})

	b.OnMessageChunk("msg before tool")
	b.OnToolCall("tc-A", "ToolA")
	b.OnToolCall("tc-B", "ToolB")
	// No message/thought chunk afterwards: accToolCalls stays populated,
	// no eager flush. inMessage was ended by OnToolCall's state machine
	// so OnMessageEnd was already fired (baseline).
	baselineMsgEnds := h.messageEnds
	baselineToolEnds := len(h.toolCallEnded)

	b.EmitPendingEnds(ReasonInterrupted)

	if h.messageEnds != baselineMsgEnds {
		t.Errorf("no in-flight message expected; OnMessageEnd delta: got %d want 0", h.messageEnds-baselineMsgEnds)
	}
	newlyEnded := h.toolCallEnded[baselineToolEnds:]
	if len(newlyEnded) != 2 || newlyEnded[0] != "tc-A" || newlyEnded[1] != "tc-B" {
		t.Errorf("OnToolCallEnd for pending ids: got %v want [tc-A tc-B]", newlyEnded)
	}
}

// TestEmitPendingEnds_InFlightMessage covers the second branch: a
// Run stopped while a message chunk is streaming (no tool calls yet).
// EmitPendingEnds must fire OnMessageEnd and clear inMessage so a
// subsequent call is a no-op.
func TestEmitPendingEnds_InFlightMessage(t *testing.T) {
	b := New()
	h := &stubHandler{}
	b.Reset(h, func([]*schema.Message) {})

	b.OnMessageChunk("streaming msg...")
	// inMessage=true, accToolCalls=[] — typical "user hit stop mid-sentence".

	b.EmitPendingEnds(ReasonCanceled)
	if h.messageEnds != 1 {
		t.Errorf("OnMessageEnd should fire once for in-flight message: got %d", h.messageEnds)
	}
	// Second call is a no-op because FinalizeStreaming cleared the flag.
	b.EmitPendingEnds(ReasonCanceled)
	if h.messageEnds != 1 {
		t.Errorf("OnMessageEnd must not re-emit after FinalizeStreaming cleared flag: got %d", h.messageEnds)
	}
}

// TestEmitPendingEnds_InFlightThought covers the thought branch.
func TestEmitPendingEnds_InFlightThought(t *testing.T) {
	b := New()
	h := &stubHandler{}
	b.Reset(h, func([]*schema.Message) {})

	b.OnThoughtChunk("thinking...")
	b.EmitPendingEnds(ReasonCanceled)
	if h.thoughtEnds != 1 {
		t.Errorf("OnThoughtEnd should fire once for in-flight thought: got %d", h.thoughtEnds)
	}
}

// TestEmitPendingEnds_ClearedHandlerIsNoOp: ClearHandler is called by
// the acp/eino agents when the subprocess is torn down out of order.
// EmitPendingEnds on a nil handler must not panic.
func TestEmitPendingEnds_ClearedHandlerIsNoOp(t *testing.T) {
	b := New()
	b.Reset(&stubHandler{}, func([]*schema.Message) {})
	b.OnMessageChunk("hi")
	b.ClearHandler()

	// Would panic on nil-deref if EmitPendingEnds skipped the guard.
	b.EmitPendingEnds(ReasonInterrupted)
}

// TestEmitPendingEnds_ReasonMatchesCollectMessages pins down the live vs
// history consistency guard: whatever PlaceholderReason the caller passes
// into EmitPendingEnds must land in the live OnToolCallInterrupted event
// so the tooltip the user sees now matches the reason baked into the
// on-disk placeholder by the immediately-following CollectMessages.
// Before the fix, EmitPendingEnds hard-coded "interrupted" even when
// CollectMessages wrote "canceled" → live said "interrupted", history
// reload said "canceled".
func TestEmitPendingEnds_ReasonMatchesCollectMessages(t *testing.T) {
	b := New()
	h := &stubHandler{}
	b.Reset(h, func([]*schema.Message) {})

	b.OnMessageChunk("plan")
	b.OnToolCall("tc-A", "ToolA")

	b.EmitPendingEnds(ReasonCanceled)

	if got := h.toolInterruptedReason["tc-A"]; got != string(ReasonCanceled) {
		t.Errorf("live OnToolCallInterrupted reason: got %q want %q", got, ReasonCanceled)
	}
}

// TestRound_FailedToolCall_PropagatesToHandlerAndDisk pins down the fix for
// the "live UI shows success, disk shows [failed]" inconsistency. The same
// "[failed] " prefix must land on disk (for the LLM) AND be delivered to
// the handler with success=false so the UI bubble can render as an error
// in real time, matching what history reload will later show.
func TestRound_FailedToolCall_PropagatesToHandlerAndDisk(t *testing.T) {
	b := New()
	h := &stubHandler{}
	var flushed [][]*schema.Message
	b.Reset(h, func(msgs []*schema.Message) {
		flushed = append(flushed, msgs)
	})

	b.OnMessageChunk("calling tool")
	b.OnToolCall("tc-A", "ToolA")
	b.OnToolCallUpdate("tc-A", "boom", agentstream.ToolCallStatusFailed)

	// Handler side: result + end must both carry success=false, and the
	// result content must already contain the "[failed] " prefix so
	// live rendering matches history reload.
	if got := h.toolResultContent["tc-A"]; got != "[failed] boom" {
		t.Errorf("OnToolCallResult content: got %q want %q", got, "[failed] boom")
	}
	if h.toolResultSuccess["tc-A"] {
		t.Errorf("OnToolCallResult success flag: got true, want false")
	}
	if _, ended := h.toolEndSuccess["tc-A"]; !ended {
		t.Fatalf("OnToolCallEnd never fired for tc-A")
	}
	if h.toolEndSuccess["tc-A"] {
		t.Errorf("OnToolCallEnd success flag: got true, want false")
	}

	// Disk side: the flushed role=tool message must carry the prefix so
	// the next LLM round sees the failure in its context.
	if len(flushed) != 1 {
		t.Fatalf("expected one flush, got %d", len(flushed))
	}
	var toolMsg *schema.Message
	for _, m := range flushed[0] {
		if m.Role == schema.Tool && m.ToolCallID == "tc-A" {
			toolMsg = m
			break
		}
	}
	if toolMsg == nil {
		t.Fatalf("no role=tool message found in flushed round")
	}
	if toolMsg.Content != "[failed] boom" {
		t.Errorf("disk content: got %q want %q", toolMsg.Content, "[failed] boom")
	}
}

// TestRound_CompletedToolCall_MarksSuccess is the success-path counterpart
// of TestRound_FailedToolCall_PropagatesToHandlerAndDisk: content is
// forwarded verbatim (no prefix injection) and success=true lands on both
// OnToolCallResult and OnToolCallEnd.
func TestRound_CompletedToolCall_MarksSuccess(t *testing.T) {
	b := New()
	h := &stubHandler{}
	b.Reset(h, func([]*schema.Message) {})

	b.OnMessageChunk("calling tool")
	b.OnToolCall("tc-A", "ToolA")
	b.OnToolCallUpdate("tc-A", "ok", agentstream.ToolCallStatusCompleted)

	if got := h.toolResultContent["tc-A"]; got != "ok" {
		t.Errorf("OnToolCallResult content: got %q want %q (must not inject prefix)", got, "ok")
	}
	if !h.toolResultSuccess["tc-A"] {
		t.Errorf("OnToolCallResult success flag: got false, want true")
	}
	if !h.toolEndSuccess["tc-A"] {
		t.Errorf("OnToolCallEnd success flag: got false, want true")
	}
}

// TestRound_EmptyToolResult_StillEmitsOnToolCallResult pins down the
// "live/disk parity on empty tool output" invariant. A terminal update
// with empty content still writes an empty role=tool message to disk; the
// live path MUST also call OnToolCallResult (with empty content) so the
// UI's rendering matches what history reload will later show. Before the
// fix, the builder skipped OnToolCallResult for empty content, which left
// live UI without a result bubble while reload surfaced one.
func TestRound_EmptyToolResult_StillEmitsOnToolCallResult(t *testing.T) {
	b := New()
	h := &stubHandler{}
	b.Reset(h, func([]*schema.Message) {})

	b.OnMessageChunk("calling tool")
	b.OnToolCall("tc-A", "ToolA")
	b.OnToolCallUpdate("tc-A", "", agentstream.ToolCallStatusCompleted)

	if _, ok := h.toolResultContent["tc-A"]; !ok {
		t.Fatalf("OnToolCallResult must fire for empty terminal content (live/disk parity)")
	}
	if got := h.toolResultContent["tc-A"]; got != "" {
		t.Errorf("OnToolCallResult content: got %q want %q", got, "")
	}
	if !h.toolResultSuccess["tc-A"] {
		t.Errorf("OnToolCallResult success flag: got false, want true")
	}
	if !h.toolEndSuccess["tc-A"] {
		t.Errorf("OnToolCallEnd success flag: got false, want true")
	}
}

// panicOnMessageDelta is a stubHandler variant whose OnMessageDelta panics
// once its armed flag flips to true — used to reproduce the "eager flush
// + handler panic = silent message loss" scenario. OnMessageDelta is
// the final handler callback in OnMessageChunk's sequence (after the
// eager flush, OnThoughtEnd / OnMessageStart), so making it panic
// deterministically exercises the ordering invariant "onFlush runs
// before any handler callback that could panic".
//
// The arming gate lets the test complete the first-round setup (which
// also calls OnMessageDelta) without panicking; only the second-round
// eager-flush path is expected to panic.
type panicOnMessageDelta struct {
	stubHandler
	armed bool
}

func (p *panicOnMessageDelta) OnMessageDelta(string) error {
	if p.armed {
		panic("boom")
	}
	return nil
}

// TestRound_HandlerPanicMidEagerFlush_DoesNotLoseRound pins down the
// regression fix for Issue 2. Previously, OnMessageChunk invoked the
// eager flush's onFlush AFTER calling handler callbacks (OnThoughtEnd /
// OnMessageStart / OnMessageDelta): if any callback panicked, the round
// sat in completedMessages but never reached disk, and Run's deferred
// CollectMessages would discard the return value. After the reorder,
// onFlush runs first, so the round is durably persisted before any
// handler callback that might panic.
func TestRound_HandlerPanicMidEagerFlush_DoesNotLoseRound(t *testing.T) {
	b := New()
	h := &panicOnMessageDelta{}
	var flushed [][]*schema.Message
	b.Reset(h, func(msgs []*schema.Message) {
		// Copy to avoid sharing a slice the builder may reset later.
		cp := make([]*schema.Message, len(msgs))
		copy(cp, msgs)
		flushed = append(flushed, cp)
	})

	// Install the recover BEFORE any calls that may panic so the test
	// can still assert after the panic unwinds.
	defer func() {
		_ = recover()

		if len(flushed) != 1 {
			t.Fatalf("round 1 must reach onFlush before the handler panic: flushed %d rounds", len(flushed))
		}
		round1 := flushed[0]
		var sawToolPlaceholder bool
		for _, m := range round1 {
			if m.Role == schema.Tool && m.ToolCallID == "tc-A" && isPlaceholder(m) {
				sawToolPlaceholder = true
			}
		}
		if !sawToolPlaceholder {
			t.Errorf("expected [placeholder]superseded for tc-A in flushed round 1")
		}
	}()

	// Prime the "accToolCalls > 0, inMessage=false, inThought=false"
	// state by declaring a tool call after a message chunk. OnToolCall
	// closes the prior in-flight message cleanly.
	b.OnMessageChunk("r1 msg")
	b.OnToolCall("tc-A", "ToolA")
	// tc-A never terminals → will become [placeholder]superseded.

	// Arm the panic handler only for round 2 so the round 1 setup above
	// can complete without crashing.
	h.armed = true

	// Round 2 starts with a message chunk, triggering the eager flush
	// of round 1 AND then invoking OnMessageDelta (which panics). With
	// the fix, onFlush runs first so round 1 is on disk before the
	// panic unwinds.
	b.OnMessageChunk("r2 msg")
}

// TestRound_UndeclaredTerminalDropped pins down the defensive guard
// against OnToolCallUpdate(terminal) arriving for a tool_call id that
// was never declared via OnToolCall. Without the guard:
//   - accToolResults would grow past len(accToolCalls), triggering a
//     spurious flush whose reorderToolResults silently drops the real
//     result (its id is not in toolCalls).
//   - If declaration ever catches up in a later round, the stray
//     result remains in the accumulator and contaminates that round.
func TestRound_UndeclaredTerminalDropped(t *testing.T) {
	b := New()
	h := &stubHandler{}
	var flushed [][]*schema.Message
	b.Reset(h, func(msgs []*schema.Message) {
		flushed = append(flushed, msgs)
	})

	// Declare tc-A and complete it normally — baseline flush.
	b.OnMessageChunk("round 1")
	b.OnToolCall("tc-A", "ToolA")
	b.OnToolCallUpdate("tc-A", "A result", agentstream.ToolCallStatusCompleted)

	if len(flushed) != 1 {
		t.Fatalf("round 1 should flush once, got %d", len(flushed))
	}

	// Now fire a terminal for a ghost id with no prior declaration.
	// It must not mutate any builder state, not trigger a flush, and
	// not surface OnToolCallResult / OnToolCallEnd to the handler.
	priorEnded := len(h.toolCallEnded)
	priorResults := len(h.toolResultContent)
	b.OnToolCallUpdate("ghost", "should-be-dropped", agentstream.ToolCallStatusCompleted)

	if len(flushed) != 1 {
		t.Fatalf("stray terminal must not trigger a flush, got %d flushes", len(flushed))
	}
	if got := len(h.toolCallEnded) - priorEnded; got != 0 {
		t.Fatalf("stray terminal must not fire OnToolCallEnd, got %d new ends", got)
	}
	if got := len(h.toolResultContent) - priorResults; got != 0 {
		t.Fatalf("stray terminal must not fire OnToolCallResult, got %d new results", got)
	}

	// Start round 2 to prove the stray result did not contaminate
	// accToolResults: if it had, the reorderToolResults in round 2's
	// flush would either drop a legitimate result or trip the pairing
	// math.
	b.OnMessageChunk("round 2")
	b.OnToolCall("tc-B", "ToolB")
	b.OnToolCallUpdate("tc-B", "B result", agentstream.ToolCallStatusCompleted)

	if len(flushed) != 2 {
		t.Fatalf("round 2 should flush once, got %d total flushes", len(flushed))
	}
	assertToolResultOrder(t, flushed[1], []string{"tc-B"})
	for _, m := range flushed[1] {
		if m.Role == schema.Tool && m.ToolCallID == "tc-B" && m.Content != "B result" {
			t.Fatalf("round 2 tc-B content: got %q want %q", m.Content, "B result")
		}
	}
}

// TestRound_DuplicateTerminalDropped verifies that a second terminal
// for an id whose result is already buffered in accToolResults is
// ignored. Without the guard, the duplicate count would satisfy
// len(accToolResults) >= len(accToolCalls) ahead of a still-pending
// sibling tool_call, triggering a premature flush that placeholders
// the sibling even though its real result was still inbound.
func TestRound_DuplicateTerminalDropped(t *testing.T) {
	b := New()
	h := &stubHandler{}
	var flushed [][]*schema.Message
	b.Reset(h, func(msgs []*schema.Message) {
		flushed = append(flushed, msgs)
	})

	b.OnMessageChunk("two calls")
	b.OnToolCall("tc-A", "ToolA")
	b.OnToolCall("tc-B", "ToolB")

	// A completes.
	b.OnToolCallUpdate("tc-A", "A result", agentstream.ToolCallStatusCompleted)
	if len(flushed) != 0 {
		t.Fatalf("round must not flush until B arrives, got %d flushes", len(flushed))
	}

	// Duplicate terminal for A — must be dropped. Without the guard
	// this would hit len(results)=2 >= len(calls)=2 and flush with B
	// placeholdered.
	b.OnToolCallUpdate("tc-A", "A dup", agentstream.ToolCallStatusCompleted)
	if len(flushed) != 0 {
		t.Fatalf("duplicate terminal must not trigger a flush, got %d", len(flushed))
	}

	// B arrives for real — round now flushes with both real results.
	b.OnToolCallUpdate("tc-B", "B result", agentstream.ToolCallStatusCompleted)
	if len(flushed) != 1 {
		t.Fatalf("round must flush exactly once on B's terminal, got %d", len(flushed))
	}

	assertToolResultOrder(t, flushed[0], []string{"tc-A", "tc-B"})
	for _, m := range flushed[0] {
		if m.Role != schema.Tool {
			continue
		}
		if isPlaceholder(m) {
			t.Fatalf("no placeholder expected in a fully-paired round, id=%s", m.ToolCallID)
		}
		switch m.ToolCallID {
		case "tc-A":
			if m.Content != "A result" {
				t.Fatalf("tc-A content: got %q want %q (duplicate must not override)", m.Content, "A result")
			}
		case "tc-B":
			if m.Content != "B result" {
				t.Fatalf("tc-B content: got %q want %q", m.Content, "B result")
			}
		}
	}
}
